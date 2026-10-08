# Commerce

Orders, invoices, pricing, coupons, taxes, refunds and revenue metrics.
Engines: `internal/money`, `internal/coupon`, `internal/tax` (pure,
std-lib-only); persistence `internal/store/orders.go`, `coupons.go`,
`plan_prices.go`; orchestration `internal/service/order.go`; admin API under
`/api/v1/admin/orders|coupons|tax-rates|quotes|metrics`.

## Money discipline (plan §51)

**All money is `int64` minor units** at the currency's ISO-4217 exponent
(`internal/money` holds the exponent table). VND has exponent **0** — 1 unit =
1 whole dong (`499000` = 499,000 ₫). USD/EUR have exponent 2
(`1999` = $19.99). Floats never appear in amounts, storage, or math;
percentages travel as basis points (bps, 10000 = 100%). Line splits use
largest-remainder allocation so rounding never loses or invents a penny.

## Catalog pricing

- Plan prices live on Stripe (`plan.stripe_price_id`) as the subscription
  source of truth, **plus** optional local multi-currency rows in
  `plan_prices` (`store.PlanPrice`: `plan_id`, `currency`, `amount_minor`,
  `stripe_price_id`, `is_default`). Resolution: exact currency row → default
  row → `CURRENCY_NOT_SUPPORTED` (400). `(plan_id, currency)` is unique;
  at most one default per plan.
- Client-supplied amounts are **never** trusted. Checkout, cart, gateway-pay
  and quote all resolve prices server-side
  (`handler/checkout_quote.go`, `payment/checkout_terms.go`,
  `handler/cart_checkout.go`).

## Orders & invoices

- `orders` (`HTC-…` order numbers) + `order_items` (per-line: qty, unit,
  discount, tax) + `invoices` (`INV-…` numbers, one per order).
- `OrderService.Calculate` composes money + coupon + tax and maintains the
  invariants: exclusive tax `subtotal − discount + tax == total`; inclusive
  tax `subtotal − discount == total`; per-line sums are exact.
- Order billing block (PO workflow, plan §26): billing name/company/address,
  `customer_tax_id`, `po_number`, `billing_email` — PATCH merge semantics
  (absent = keep, `""` = clear).
- Invoice state machine (`model.CanTransitionInvoice`):
  `draft → open | void`; `open → paid | void | uncollectible`;
  `uncollectible → open | paid`; `paid → refunded` only. Paid → void is
  refused (`INVOICE_NOT_VOIDABLE` — refund first).
- Orders are also recorded best-effort from Stripe sessions and gateway
  fulfilment (`payment/order_record.go`, `payment/gateway_checkout.go`), so
  the ledger reflects revenue regardless of checkout path.

## Quotes & checkout preview

- `POST /api/v1/admin/quotes` — admin price preview (no persistence).
- `POST /api/v1/checkout/quote` — public preview: items accept `checkout_id`
  or `plan_id`, plus `coupon_code` and `country`. Response itemizes subtotal,
  discount, tax (inclusive/exclusive), total — all int64 minor units.

## Coupons (`internal/coupon`)

Persisted vocabulary is exactly `percent_off` | `fixed_off`
(`model.CouponTypePercentOff|FixedOff`):

| Field | Notes |
|---|---|
| `value_bps` | percent_off size (10000 = 100%). |
| `value_minor` + `currency` | fixed_off size in minor units; currency required. |
| `starts_at` / `ends_at` | Inclusive validity window. |
| `max_redemptions`, `max_redemptions_per_customer` | 0 = unlimited. |
| `minimum_order_minor` | Subtotal floor. |
| `applies_to` | Optional product/plan restriction. |
| `stackable`, `active` | Master switches. |

Redemption is penny-exact (largest-remainder allocation across lines);
counters move only via `IncrementCouponRedemptions`. Admin CRUD:
`/api/v1/admin/coupons` (update is **PUT**). At Stripe checkout a coupon
becomes a one-time fixed-amount Stripe coupon so the plan's recurring price
stays intact.

## Taxes (`internal/tax`)

Tax rates are bps values, exclusive or inclusive, multi-jurisdiction with an
ADDITIVE breakdown per line. Exclusive tax is added as its own checkout line
(and recurring copy in subscription mode); inclusive tax is folded into the
unit price. No-lost-pennies allocation across multiple lines.

## Multi-currency

Amounts are always minor units of an explicit ISO-4217 code; mixed-currency
arithmetic is refused (`internal/money`). Checkout is additionally
gateway-constrained: the Vietnamese gateways are **VND-only** — a non-VND cart
falls back to Stripe or is refused with `CURRENCY_NOT_SUPPORTED`.

## Refunds (plan §79)

`refunds` table + two refund endpoints (plus
`POST /api/v1/admin/licenses/:id/refund`):

- `POST /api/v1/admin/orders/:id/refunds` creates a refund ledger entry —
  body `{amount_minor?, reason}`, **full / partial / manual**, all int64
  minor units, idempotent per request.
- `POST /api/v1/admin/orders/:id/refund` is the legacy full-order refund
  (no body).
- Refunding a license triggers revocation with an explicit reason from the
  closed vocabulary (`model.RevokeReasons()`): `fraud`, `refund`,
  `chargeback`, `policy_violation`, `customer_request`,
  `security_incident`, `administrative_action`.
- Gateway refunds: ZaloPay refunds are asynchronous (`pending` until
  confirmed); Pay2S / payOS refunds are `ErrNotSupported` today — refund
  manually and revoke with reason.
- `GET /api/v1/admin/orders/:id/refunds` lists the refund history.

## Revenue metrics

- `GET /api/v1/admin/metrics/mrr` and `/metrics/mrr-series` — MRR/ARR from
  active subscriptions and recurring plan prices (plan §42).
- `GET /api/v1/admin/reports/:type[/export]` — revenue, sales, refunds,
  failed-payments and 10 more report families with CSV/JSON export; CSV money
  cells are integer `*_minor` columns.
- Renewal revenue is tracked in the `license_renewals` ledger; perpetual
  maintenance renewals contribute `renewal_minor` amounts.

## Retention & money records

The retention job (90/30/90/365 days — see
[DATA-RETENTION.md](DATA-RETENTION.md)) never touches financial tables:
orders, invoices, refunds, commissions and payouts are permanent records.
