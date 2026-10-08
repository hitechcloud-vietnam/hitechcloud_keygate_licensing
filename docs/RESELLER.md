# Reseller & Affiliate Programs

Two partner models, both under Phase 7 (plan §32): **resellers** buy at
wholesale and manage allocated licenses; **affiliates** refer traffic and
earn commission on attributed orders. Backend: `internal/model/{reseller,
commission,affiliate}.go`, `internal/store/{resellers,commissions,
reseller_pricing,affiliates}.go`, `internal/handler/{reseller_admin,
affiliate_admin,affiliate_public,portal_reseller}.go`, checkout attribution
in `internal/payment/attribution.go`.

## Resellers

| Concept | Storage | Notes |
|---|---|---|
| Reseller account | `resellers` | `contact_email` is the identity — a portal session whose email matches **is** that reseller (anyone else gets quiet 404; no partner-existence oracle). |
| Commission rate | `resellers.commission_bps` | 0–10000 bps (`model.ValidCommissionBPS`). |
| License allocation | `reseller_licenses` | One license ≤ one reseller (unique); delete refused while allocations exist. |
| Customer mapping | `reseller_customers` | Join of reseller to end customers. |

Admin API: `GET|POST /api/v1/admin/resellers`,
`GET|PATCH|DELETE /admin/resellers/:id`,
`POST|DELETE /admin/resellers/:id/licenses[/:license_id]`.
Portal self-service: `GET /api/v1/portal/reseller/{me,licenses,commissions,prices}`.

## Wholesale pricing

`reseller_price_overrides` is a per-(reseller, plan) config row
(`currency CHAR(3)`, `amount_minor ≥ 0`, int64 minor units). At checkout
(`?reseller_code=<contact_email>` — resellers have no separate code field):

- An override **lower than** catalog is charged as the unit price (buyer
  coupons + tax compute on what is actually paid).
- The `catalog − override` cut is applied as a **one-time** fixed Stripe
  coupon named `wholesale` (buyer coupon stacks safely: it is priced on the
  override, so combined ≤ list).
- An override ≥ catalog, or in a different currency than the plan, is
  ignored (catalog price wins). Pricing-relevant lookup failures fail the
  checkout loudly — a partner is never silently charged list price.
- **Known limit:** renewals bill the plan's Stripe Price at catalog (the
  wholesale coupon is one-time).

## Commission ledger

`commissions` rows snapshot `basis_minor`, `bps` and computed `amount_minor`
(floor, exact overflow-free math in `model.CommissionAmount`) at accrual time
— history survives reseller edits and order retention (no FK on `order_id`).

- Statuses: `accrued → approved | cancelled`, `approved → paid`
  (`paid_at` stamped; cancelled refuses → `COMMISSION_CANCELLED` 409).
- Accrual is **idempotent per (reseller_id, order_id)** (unique index +
  `ON CONFLICT DO NOTHING` + read-back): `POST
  /admin/resellers/:id/commissions` replays return the original row.
- Checkout auto-accrues on fulfilment (`recordAttributionEffects`), best
  effort — fulfilment never fails on commission errors. Basis = order total.
- Admin: `GET/POST /admin/resellers/:id/commissions`,
  `POST …/commissions/:commission_id/paid`.

## Affiliates

| Concept | Storage | Notes |
|---|---|---|
| Affiliate | `affiliates` | Account with payout details. |
| Referral code | `referral_codes` | `active` flag, stored `landing_url` (local path clamped — **no open redirect**). |
| Click | `referral_clicks` | IP hashed `SHA-256("ip\0"+REFERRAL_HASH_SALT+"\0"+ip)`; 30-minute dedup window. |
| Conversion | `affiliate_conversions` | **Unique per `order_id`** — one conversion per order, forever. |
| Payout | `affiliate_payouts` | `created → paid | failed`; claimed conversions must be reviewed first (freeze payout before reversing). |

### Attribution flow — `/r/:code`

1. `GET /r/:code` (surface apex) records the click and 302s to the code's
   stored landing URL; sets first-party `htc_ref` cookie (30 d,
   HttpOnly, SameSite=Lax).
2. Checkout reads `?reseller_code=` (wins conflicts), then `?ref=` / the
   `htc_ref` cookie, then buyer-email reseller match (pricing authority).
3. `POST /api/v1/affiliates/convert` `{order_id, order_total_minor, …}`
   records the conversion (cookie-or-code); then admin review:
   `POST /admin/conversions/:id/{approve,reject,reverse}`.

Commission: percent (`round-half-up`, `(total*bps+5000)/10000`) or fixed;
`MaxCommissionableOrderMinor` (9e14) guards absurd totals. Fraud controls:
suspended affiliates / inactive codes never convert; self-purchase via own
email **does** accrue (documented — set `bps=0` to opt out); reverse moves a
paid conversion back for clawback.

### Affiliate admin API

`GET|POST /admin/affiliates`, `GET|PATCH|DELETE /admin/affiliates/:id`,
`/admin/affiliates/:id/codes*`, `/admin/affiliates/:id/conversions`,
`/admin/affiliates/:id/payouts*`, `/admin/conversions/:id/*`,
`/admin/payouts/:id/{paid,failed}`.

## Related

Checkout attribution metadata keys mirror the order columns
(`reseller_id`, `reseller_email`, `referral_code`, `affiliate_id`,
`wholesale_override*`) — see [COMMERCE.md](COMMERCE.md) and
[API.md](API.md).
