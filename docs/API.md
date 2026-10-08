# API Reference

Base path: `/api/v1`. Machine-readable contract: [`docs/openapi.yaml`](openapi.yaml)
(rendered at `/docs`), conventions: [`docs/api-contract.md`](api-contract.md).
This document is the human-readable endpoint map — it does not replace those
two, and it must not drift from the route table in `cmd/server/main.go`.

## Response envelope

```json
{ "success": true, "data": { } }
{ "success": false, "error": { "code": "LICENSE_NOT_FOUND", "message": "…" } }
```

- **One HTTP status per error code, repo-wide.** Enforced by
  `pkg/response/contract_test.go` (`TestErrorCodeMapsToExactlyOneStatus`).
- Unknown routes answer `404` with `NOT_FOUND` in the envelope
  (`r.NoRoute`), never plain text.
- `500 INTERNAL_ERROR` bodies are deliberately empty of detail; the cause is
  logged server-side with the request id.
- Pagination: `?limit=&offset=`, list responses carry
  `{ …, total, limit, offset }`. Sorting: `?sort=&order=` where supported
  (whitelisted columns only).

## Authentication models

| Model | Credential | Used by |
|---|---|---|
| Session | Login cookie + JWT (`Authorization: Bearer` accepted); refresh-token rotation at `POST /auth/refresh` | Portal, admin SPA |
| Admin API key | `api_keys` rows, `Authorization: Bearer`, scopes `admin` \| `licenses:write` \| `releases:write` | Server-to-server, CI release publishing |
| Customer API key | `customer_api_keys`, secret `htc_sk_…` shown once, scopes `orders:read`, `licenses:read` | Developer API (`/me`, `/orders`, `/licenses`) |
| SCIM token | `htc_scim_…` bearer | `/scim/v2/*` |
| License key | `license_key` in the request body **is** the credential | `/license/*` SDK endpoints |

## Idempotency

Mutating SDK endpoints (`/license/activate`, `/license/usage`,
`/license/floating/checkout`) accept an `Idempotency-Key` header. Semantics:
scoped per caller (user id, else client IP), 24 h TTL, replay returns the
stored status + body with `Idempotent-Replay: true`, the same key with a
different body → `409`, concurrent duplicates → `409`. Absent header =
pass-through.

## Endpoint map

### System & public config
| Method & path | Auth | Notes |
|---|---|---|
| `GET /health` | — | `{status, checks:{database}, version}`; 503 when degraded. Not under `/api/v1`. |
| `GET /metrics` | — | Prometheus. |
| `GET /api/v1/version` | — | Build version. |
| `GET /api/v1/config` | — | Public settings + `attribution_text`/`attribution_url` + surface map + enabled payment methods. |
| `GET /api/v1/site-config` | — | Site branding + domain surface map. |
| `GET /api/v1/license/pubkey` | — | ed25519 offline-token public key (hex). |
| `GET /docs`, `GET /docs/openapi.yaml` | — | API reference. |

### Setup
| `GET /api/v1/setup/status` | surface dashboard | `{needed, step}` |
| `POST /api/v1/setup/initialize` | surface dashboard | First-run wizard; only while setup incomplete (`409 SETUP_COMPLETE` otherwise). |

### Auth & session
| `GET /api/v1/auth/providers` | surface customer | Login methods available. |
| `POST /api/v1/auth/otp/send` | surface customer | Email OTP (tight rate limit). |
| `POST /api/v1/auth/otp/verify` | surface customer | Returns session + sets cookies. |
| `POST /api/v1/auth/dev-login` | surface customer | Development only (`ENVIRONMENT=development`). |
| `POST /api/v1/auth/refresh` | cookie | Rotating refresh token. |
| `POST /api/v1/auth/logout` | session | Clears cookies. |
| `GET/POST /api/v1/auth/sso/…` | surface customer | SAML start/ACS, OIDC callback. |
| `POST /api/v1/invites/accept` | invite token | Seat invite acceptance. |

### License verification SDK (surface `verify`)
| `POST /api/v1/license/activate` | license_key | Idempotent. Registers an activation, returns signed offline token. |
| `POST /api/v1/license/verify` | license_key | Re-check; returns status + features + fresh token. |
| `POST /api/v1/license/deactivate` | license_key | Removes an activation. |
| `POST /api/v1/license/entitlements` | license_key | Feature map (bool flags + quotas). |
| `POST /api/v1/license/usage` | license_key | Record metered usage (idempotent). |
| `POST /api/v1/license/usage/status` | license_key | Quota status. |
| `POST /api/v1/license/floating/checkout` | license_key | Take a floating seat (idempotent). |
| `POST /api/v1/license/floating/checkin` | license_key | Release a floating seat. |
| `POST /api/v1/license/floating/heartbeat` | license_key | Keep a floating seat alive. |
| `POST /api/v1/license/download` | license_key | Signed, expiring artifact URL. |

### Releases (public feeds)
| `GET /api/v1/releases/:product_slug/feed.xml` \| `feed.json` \| `upgrade.json` | — | Sparkle / Velopack / Tauri feeds. |
| `GET /api/v1/releases/:product_slug/velopack/*path` | — | Velopack index. |
| `GET /api/v1/products/:product_slug/plans` | — | Anonymous plan catalogue. |

### Marketplace (surface `verify`)
| `GET /api/v1/marketplace/categories` | — | |
| `GET /api/v1/marketplace/products` | — | `?search&category&sort&order&limit&offset`. |
| `GET /api/v1/marketplace/products/:slug` | — | Detail + plans + latest releases. |
| `GET /api/v1/marketplace/products/:slug/reviews` | — | Approved reviews + rating aggregate. |
| `GET /api/v1/marketplace/products/:slug/related` | — | Related products. |

### Checkout & payments (surface `payments`)
| `POST /api/v1/checkout/quote` | — | Pricing preview (server-side prices; coupon + tax). |
| `GET /pay/:checkout_id` | — | Stripe checkout redirect (`?coupon_code=&country=`). |
| `GET /api/v1/checkout/verify` | — | Post-checkout session verification. |
| `POST /api/v1/checkout/cart` | — | Multi-item cart → one Stripe session or gateway payment. |
| `GET /api/v1/checkout/gateway-pay/methods` | — | Enabled VND gateways. |
| `POST /api/v1/checkout/gateway-pay` | — | One-off VND payment (Pay2S / ZaloPay / payOS). |
| `GET /api/v1/checkout/gateway-pay/status?order=` | — | Poll gateway payment status. |
| `POST /api/v1/webhook/stripe` | Stripe signature | Subscription + payment events. |
| `POST /api/v1/webhook/pay2s` \| `/webhook/zalopay` \| `/webhook/payos` | provider MAC | IPN endpoints; per-gateway ack shapes. |

### Developer API (customer API key, surface `verify`)
| `GET /api/v1/me` | key | Key owner identity. |
| `GET /api/v1/orders` | key + `orders:read` | |
| `GET /api/v1/licenses` | key + `licenses:read` | Metadata only — never key material. |

### Affiliate
| `GET /r/:code` | surface apex | Click capture + 302 to stored landing URL (`htc_ref` cookie, 30 d). |
| `POST /api/v1/affiliates/convert` | — | Conversion attribution (idempotent per order). |

### Customer portal (session, surfaces `customer` + `merchant`)
`GET /portal/me`, `GET /portal/licenses` (own keys included), `PUT /portal/profile`,
`GET /portal/plans`, `POST /portal/usage[/status]`, `POST /portal/seats[/add|/remove]`,
`GET|DELETE /portal/licenses/:key/activations[/:id]`,
`POST /portal/subscription/change-plan|cancel|billing-portal`, `GET /portal/subscription/invoices`,
`POST /portal/updates/renew`,
`GET /portal/orders[/:id]`, `GET /portal/orders/:id/invoices`, `GET /portal/invoices/:id`,
`GET /portal/downloads`, `GET|POST /portal/api-keys[/:id]`, `DELETE /portal/api-keys/:id`,
`GET|POST /portal/webhooks`, `PATCH|DELETE /portal/webhooks/:id`, `POST /portal/webhooks/:id/test|rotate`,
`GET /portal/reseller/me|licenses|commissions|prices`,
`POST|PATCH|DELETE /portal/products/:id/reviews`,
`GET /portal/notifications`, `GET /portal/notifications/unread-count`,
`POST /portal/notifications/:id/read`, `POST /portal/notifications/read-all`.

### Admin (session-or-API-key + per-route RBAC, surface `dashboard`)
Full table in `cmd/server/main.go` (≈130 routes). Highlights:

- **Catalog**: `/admin/products` (+`/:id/categories`), `/admin/plans`,
  `/admin/entitlements`, `/admin/addons`, `/admin/categories`, `/admin/releases*`,
  `/admin/products/:id/signing-key*`.
- **Licenses** (`licenses:write` keys accepted): `/admin/licenses*` incl.
  revoke/suspend/reinstate/refund/usage/seats/addons/floating,
  `/admin/activations/:id`.
- **Commerce**: `/admin/orders*` (refund, billing, invoice void /
  mark-uncollectible), `/admin/quotes`, `/admin/coupons`, `/admin/tax-rates`,
  `/admin/orders/:id/refunds`, `/admin/metrics/mrr`, `/admin/metrics/mrr-series`.
- **Subscriptions & payments**: Stripe-backed license actions,
  `/admin/users`, `/admin/customers` views.
- **Marketplace & partners**: `/admin/reviews*`, `/admin/resellers*` (licenses,
  commissions, prices), `/admin/affiliates*` (codes, conversions, payouts).
- **Platform**: `/admin/config` (GET/PUT/PATCH + `/:key` DELETE +
  `/:key/reset`), `/admin/settings`, `/admin/search`, `/admin/stats`,
  `/admin/reports/:type[/export]`, `/admin/analytics*`, `/admin/audit-logs`,
  `/admin/webhooks*` (incl. delivery replay + secret rotate), `/admin/api-keys`,
  `/admin/team`, `/admin/sso/*`, `/admin/rbac/*`, `/admin/system/*`,
  `/admin/retention/run`.

### SCIM 2.0 (surface `verify`)
`/scim/v2/Users` — `GET` (list + `userName eq` filter), `POST`, `GET|PUT|PATCH|DELETE /Users/:id`.
`application/scim+json` wire format and SCIM error envelope (not the
`pkg/response` envelope).

## Error-code conventions (excerpt)

| Code | Status | Meaning |
|---|---|---|
| `BAD_REQUEST` | 400 | Validation failure (message says which field). |
| `UNAUTHORIZED` | 401 | Missing/invalid session, API key or SCIM token. |
| `FORBIDDEN` | 403 | Authenticated but not permitted (RBAC / scope). |
| `NOT_FOUND` | 404 | Unknown resource **or** cross-tenant read (no existence oracle). |
| `DUPLICATE` | 409 | Unique-constraint conflict (e.g. second review). |
| `IDEMPOTENCY_KEY_REUSED` / in-progress | 409 | Idempotency conflicts. |
| `CURRENCY_NOT_SUPPORTED` | 400 | Plan has no price row for that currency. |
| `MISSING_CUSTOMER` | 400 | Gateway checkout without a customer email. |
| `PROVIDER_NOT_CONFIGURED` | 503 | Gateway credentials absent. |
| `PAYMENT_GATEWAY_ERROR` | 502 | Upstream gateway failure. |
| `PRICE_UNAVAILABLE` | 502 | Stripe price lookup failed. |
| `SETUP_COMPLETE` | 409 | Setup re-run refused. |
| `INTERNAL_ERROR` | 500 | Unhandled failure (detail logged, not returned). |

The authoritative list is the code literals in `pkg/response` call sites;
`contract_test.go` guarantees each maps to exactly one status.
