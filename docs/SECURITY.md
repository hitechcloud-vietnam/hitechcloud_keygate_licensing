# Security

## Threat model summary

Assets: license keys (customer credentials), signing keys (product trust
roots), payment gateway credentials, personal data, revenue records. Adversaries:
unauthenticated internet users, malicious customers (IDOR, key enumeration),
compromised client binaries (extracted API keys — never trusted), webhook
spoofing from payment providers, SSRF via operator-configured webhook URLs,
credential leaks from logs/config. Trusted computing base: the Go binary +
PostgreSQL; Stripe/gateways/S3/SMTP are untrusted remote services whose
input is verified before use.

## Session & authentication

- **Login**: email OTP (`otp_codes`, hashed codes, attempt limits) or SSO
  (SAML/OIDC, `internal/sso` — identity only from digest-verified
  assertions; fail-closed time windows). `dev-login` exists only when
  `ENVIRONMENT=development`.
- **Session**: JWT access token + rotating refresh tokens
  (`refresh_tokens`; reuse of a rotated token is detectable and pruned only
  past its original expiry). Cookies are HttpOnly/SameSite; the domain
  attribute follows the `session_cookie_domain` setting and the surface host
  map (`internal/handler/oauth.go`).
- **SSO**: SAML via XML-DSig against a pinned IdP cert
  (`github.com/russellhaering/goxmldsig`), OIDC via RS* ID tokens with
  constant-time nonce/state compare (`alg: none`/HS* rejected). A rejected
  assertion provisions **nothing** (domain gate after verify, before
  provision). SCIM deprovision (`active=false`) blocks sign-in while
  preserving licenses and audit history.

## Credentials & scopes

| Credential | Format | Storage | Scopes / notes |
|---|---|---|---|
| Admin API key | `api_keys` | SHA-256 `key_hash` + prefix | `admin` (wildcard), `licenses:write`, `releases:write`; empty scope list = can do nothing (fail-closed). |
| Customer API key | `htc_sk_…` (40 chars, unambiguous alphabet) | SHA-256 hash + 12-char prefix; secret shown **once** | `orders:read`, `licenses:read`; soft revoke (`revoked_at`); throttled `last_used_at`. |
| SCIM token | `htc_scim_…` | hash + display prefix | Identical 401 for missing/unknown/revoked. |
| License key | `KG-…` / `licgw-…` | `key_hash` + AES-GCM `license_key_encrypted` | The license key **is** the SDK credential (product-scoped via FK). |

RBAC: 23 granular permissions (`model.Perm*`) with per-route
`RequirePermission` gates across the admin surface; 12 built-in role bundles
+ custom roles (`custom_roles`). `is_admin` is a documented wildcard
passthrough for backward compatibility; API-key routes keep `RequireScope`.

## Rate limits & brute force

Per-family IP buckets (`middleware.RateLimitByIPScoped`) so one noisy family
never locks out another behind the same NAT: `auth` (60/min), `otp_send`
(30/hr), `admin` (120/min), `license` (2×api, min 120/min), `feed` (4×api),
`marketplace`, `checkout_*`, `gateway_pay`, `gateway_ipn` (120/min — gateways
retry aggressively and must never be locked out), `stripe_webhook`, `dev_api`,
`affiliate_*`, `sso_auth`. Backed by Redis when `REDIS_URL` is set (shared
across instances), memory otherwise. Trusted-proxy aware — `X-Forwarded-For`
from non-private hops cannot spoof the limiter key.

`LicenseBruteForceGuard` throttles failed license verifies per IP on top of
the route limit; login lockout is `middleware.NewBruteForceProtection`
(fail counter + lockout window).

## Secret protection

- **Config secrets** (gateway keys, SMTP password, webhook secrets,
  `oidc_client_secret`) are sealed at rest by `crypto.SecretBox`:
  AES-256-GCM under an HKDF subkey (`secrets-at-rest-v1`) of
  `SECRET_ENCRYPTION_KEY`, stored as `enc:v1:<base64>`; unconfigured master
  key = explicit dev mode with a boot warning. Legacy plaintext rows are read
  forever (migration-safe) and sealed on next write.
- **License keys**: AES-GCM under HKDF `license-key`
  (`RELEASE_KEY_ENCRYPTION_KEY`), AAD = license id (ciphertext cannot be
  swapped between rows), with backfill and decrypt-failure metrics.
- **Release signing keys**: Ed25519 private keys sealed under HKDF
  `release-signing-private-key` (requires storage + master key).
- Secrets are `json:"-"` in models (pinned by leak tests), masked in admin
  config responses, and redacted from request logs (query strings are
  redacted — updaters send license keys as query params).

## Webhook signature verification

- **Inbound (providers)**: verify-before-trust, constant-time compare
  (`hmac.Equal`). Stripe (signed payloads), Pay2S (HMAC-SHA256 over a
  13-field sorted raw string), ZaloPay (callback mac + type check), payOS
  (ksorted `data` signature). Malformed/failed → error ack, nothing written.
- **Outbound** (merchant + customer webhooks): `X-HiTechCloud-Event`,
  `X-HiTechCloud-Signature: sha256=<hex HMAC-SHA256(body, secret)>`,
  `X-HiTechCloud-Delivery`. Secrets returned once, sealed at rest, rotatable
  with immediate invalidation (`POST …/webhooks/:id/rotate`); replays are
  byte-identical payloads with a `X-HiTechCloud-Replay` marker and fresh
  delivery id.
- **SSRF**: webhook target URLs refuse loopback, RFC1918, link-local,
  CGNAT `100.64/10` (cloud metadata), `0.0.0.0/8`, multicast/broadcast,
  NAT64/Teredo/6to4 (`WEBHOOK_ALLOW_PRIVATE=false` default).

## Other controls

- Input validation at the boundary (URL schemes, lengths, closed
  vocabularies); SQL via Bun parameterization — SQLi skeleton pins in tests.
- Authorization defense: cross-tenant/cross-user reads answer **404**
  (no existence oracle) — IDOR sweep tests cover portal/admin surfaces.
- CORS is surface-aware; `X-Frame-Options: DENY`, `nosniff`, HSTS on HTTPS;
  `RedirectTrailingSlash` off so no HTML body escapes the JSON envelope.
- Idempotency keys on mutating SDK endpoints (scoped, 24 h TTL, replay-safe).
- Audit log (`audit_logs`) records admin mutations with actor, entity and
  change diff; secrets are recorded as `***`.
- Dependency surface: std-lib-first; the only notable third-party security
  dependency is goxmldsig for SAML verification.

## Data retention

Automated horizons (config keys `retention.*`; `0` = never delete; financial
tables are never touched) — full table in
[DATA-RETENTION.md](DATA-RETENTION.md):

| Table | Default |
|---|---|
| `notifications` | 90 days |
| `processed_events` | 30 days |
| `webhook_deliveries` | 90 days |
| `audit_logs` | 365 days |

Hourly sweep (`service.RetentionJob`) + on-demand `POST
/api/v1/admin/retention/run`. Expired OTPs, past-expiry refresh tokens and
idempotency keys are pruned on the same hourly tick.

## Vulnerability disclosure

Report security issues to **hello@keygate.app** (see
[LICENSE-COMPLIANCE.md](LICENSE-COMPLIANCE.md) for license questions).
Please include reproduction steps and affected versions; allow reasonable
time before public disclosure.
