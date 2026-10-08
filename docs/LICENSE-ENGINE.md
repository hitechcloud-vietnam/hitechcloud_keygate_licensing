# License Engine

The license engine covers the full lifecycle of a license: issue → activate →
verify → (renew / change plan) → deactivate → revoke. Code lives in
`internal/service/license.go` (lifecycle), `internal/license/` (key material),
`internal/store/store.go` (persistence) and the `/api/v1/license/*` handlers.

## Lifecycle

| Stage | Trigger | Effect |
|---|---|---|
| **Issue** | Admin `POST /admin/licenses`, Stripe checkout fulfilment, gateway fulfilment (`licgw-…`), reseller allocation | License row created with generated key, `status=active`, `valid_from=now`. Subscription plans also get a `subscriptions` row in one transaction. |
| **Activate** | `POST /license/activate` | Registers an `activations` row (device identifier). Enforces `plan.max_activations`. Returns a signed offline token. |
| **Verify** | `POST /license/verify` | Full re-check of status/dates/caps; refreshes `last_verified`; returns status + feature map + fresh token. |
| **Deactivate** | `POST /license/deactivate`, portal activation delete | Frees the activation slot. |
| **Suspend** | Admin / paused Stripe subscription | `status=suspended`, `suspended_by=admin|stripe`. Admin suspends require admin reinstate; Stripe suspends clear when Stripe resumes. |
| **Renew** | Stripe renewal webhook, `POST /portal/updates/renew` (perpetual maintenance) | Extends `valid_until` / `updates_until`; payment-failure dunning ladder (`past_due`). |
| **Change plan** | `POST /portal/subscription/change-plan`, admin | Immediate (with proration) or deferred to next period; plan + subscription rewritten atomically. |
| **Revoke** | Admin `POST /admin/licenses/:id/revoke` (body `{"reason":…}`) | Terminal. `revoked_at/revoked_by/revoke_reason` recorded. |
| **Expire** | Hourly expiry checker | `status=expired`, `license.expired` event + reminders. |

Statuses: `active, trialing, past_due, canceled, expired, suspended, revoked`
(`model.Status*`). Every enforcement point goes through
`LicenseService.assertUsable` so status handling is uniform.

### Revocation reasons

`revoke` accepts a reason validated against the closed vocabulary
(`model.ValidRevokeReason`): `fraud`, `refund`, `chargeback`,
`policy_violation`, `customer_request`, `security_incident`,
`administrative_action` (default). Refund flows stamp the reason
automatically (`refund`, `chargeback`, …).

## Key format

- Standard keys: `PREFIX-XXXXXXXX-XXXXXXXX-XXXXXXXX-XXXXXXX`X — 32 random
  chars from a 32-char unambiguous alphabet (`ABCDEFGHJKLMNPQRSTUVWXYZ23456789`),
  generated with `crypto/rand` (`internal/license/keygen.go`). ≈160 bits of
  entropy; not sequential, not guessable.
- Gateway-fulfilled licenses use a deterministic provider handle:
  `licgw-<provider>-<id>` (`internal/payment/gateway_checkout.go`) — adopted
  idempotently on payment retry.
- At rest: `license_key_encrypted` stores the key AES-GCM-encrypted (HKDF
  subkey `license-key` of `RELEASE_KEY_ENCRYPTION_KEY`, AAD = license id)
  alongside the plaintext column during the 20260514+ transition
  (`store.DecryptLicenseKey` reads encrypted-first with plaintext fallback).
  `key_hash` (SHA-256) supports lookups without exposing the key.
- The key is `json:"-"` everywhere except three explicit opt-ins: admin
  reveal, create response, and the customer's own portal list.

## Offline tokens

`/license/activate` and `/license/verify` return an ed25519-signed token
covering the license id, identifier, features and expiry. Public key:
`GET /api/v1/license/pubkey`. TTL: `plan.token_ttl_days`, default 7 days —
it is a **check-in interval**, not an offline license: the token never
outlives the license (`expires_at` is clamped to `valid_until` + grace).

## Seat model

- **Activations** (device seats): `plan.max_activations` caps concurrent
  activations per license; each activation is one device identifier.
- **User seats** (SaaS): `plan.max_seats`; seats are invited by email,
  two-tier roles (`admin` / `member`), acceptance via `POST /invites/accept`
  (token proves email ownership). Seat mutations live in the portal
  (`/portal/seats/*`) — a license key on a device cannot change the member
  roster.
- Which model applies is driven by product type
  (`model.ProductSupports`: `desktop`→activations, `saas`→seats,
  `hybrid`→both).

## Floating licenses

Plans with `license_model=floating` trade activations for concurrent sessions
(`floating_sessions`):

1. `POST /license/floating/checkout` — take a seat (idempotent); refuses when
   the pool (`max_activations` as floating capacity) is full.
2. `POST /license/floating/heartbeat` — renew the lease
   (`plan.floating_timeout` minutes, default 30).
3. `POST /license/floating/checkin` — release early.

A cleanup loop expires idle sessions every minute. `GET
/admin/licenses/:id/floating` shows current occupancy; portal reports both
`activation_count` and `active_session_count` because the meaning of "full"
depends on the license model.

## Renewal & maintenance periods (perpetual plans)

Perpetual licenses get `updates_until = valid_from + plan.updates_days`
(0 = updates for life). A renewal is a one-time purchase of
`plan.renewal_days` more, sold at `plan.stripe_renewal_price_id`; both empty
means renewals are not offered. The `plan_update_terms` history table (DB
trigger) records terms changes so a renewal bills the terms that were on
sale at purchase time. Feeds can enforce the update window
(`product.feed_license_required`) once presigned links have drained
(`STORAGE_FEED_URL_TTL` bound is tracked across replicas).

## Release signing keys

Each product optionally owns an Ed25519 **release signing key**
(`release_signing_keys`, migrations 20260511–20260513):

- Generate / rotate / deactivate: `POST|DELETE /admin/products/:id/signing-key*`
  (settings-manage permission — signing identity is platform security config).
- Private keys are sealed with the HKDF subkey
  `release-signing-private-key` (requires `RELEASE_KEY_ENCRYPTION_KEY` **and**
  storage).
- Public key downloads: `…/signing-key/public.pem` and `…/tauri-pubkey`.
- Artifacts carry `sha256` + `ed25519_sig` + `signing_key_id`; products can
  require signing (`product.require_signing`, default true) before a release
  may be published.

## Performance notes (plan §62)

Verification is self-contained: one indexed lookup by `key_hash`/unique
`license_key`, no external service calls on the verify path (Stripe is only
consulted on billing mutations). Idempotency and the brute-force guard are
in-memory/Redis cheap. Horizontal scaling is stateless — all state is in
PostgreSQL; signed tokens make intermittent offline verification possible.
