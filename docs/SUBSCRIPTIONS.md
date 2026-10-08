# Subscriptions

Subscription commerce is implemented on **Stripe Billing**
(`internal/payment/stripe.go`, `change_plan.go`, `subscriptions` table).
The Vietnamese gateways (Pay2S / ZaloPay / payOS) handle one-time VND
payments only — see [COMMERCE.md](COMMERCE.md).

## Lifecycle

| State | Source | Notes |
|---|---|---|
| `trialing` | `plan.trial_days > 0` at checkout | Stripe trial. |
| `active` | Successful checkout / renewal | License `valid_until` tracks the current period end. |
| `past_due` | `invoice.payment_failed` | Dunning: `past_due_at` anchors the reminder ladder; grace from `plan.grace_days`. |
| `canceled` | Customer cancel, admin, or Stripe | License keeps working until period end unless revoked. |
| `paused` (→ suspended) | Stripe pause | License `suspended_by=stripe`; resumes with Stripe. |

One subscription row per license (`licenses.stripe_subscription_id` unique).
Licenses without a subscription (perpetual, admin-issued) have no
subscription row — `CreateLicenseWithSubscription` writes both atomically
for subscription/trial plans.

## Checkout

- `GET /pay/:checkout_id?coupon_code=&country=` creates a Stripe Checkout
  Session for the plan (`plan.stripe_price_id`).
- Coupons become one-time fixed-amount Stripe coupons (the recurring price is
  untouched); exclusive tax becomes an extra line (recurring copy in
  subscription mode); inclusive tax stays folded in.
- Attribution metadata (reseller/affiliate) is stamped on the session and
  copied onto the order at fulfilment.
- Fulfilment (`fulfillCheckout`) is **idempotent per Stripe session** — a
  replayed webhook cannot create a second license.

## Renewal

Stripe renews automatically; `invoice.paid` extends `valid_until`. The
5-minute sync loop (`SyncRecentCheckouts`, `SyncPendingCheckouts`,
`SyncCancelStates`) heals missed webhooks so an outage cannot strand a paid
subscription. Plan drift is repaired every renewal
(`reconcilePlanFromBilledPrice`).

## Proration & plan changes (plan §77/78)

`POST /api/v1/portal/subscription/change-plan`
body `{license_id, plan_id|new_price_id, timing: "immediate"|"next_period", prorate?}`:

- **Upgrade (or unknown direction) → immediate** — Stripe
  `proration_behavior=always_invoice`; all proration math is Stripe's,
  integer minor units, no local float math.
- **Downgrade → next_period** — the change is recorded as metadata intent
  (`pending_plan_id`, `pending_change_at`) and executed idempotently at
  renewal inside the same Stripe subscription update that bills it; an
  applied-marker in the notification dedup table stops the
  `invoice.paid` + `subscription.updated` double-fire.
- Cancellation (`/portal/subscription/cancel`) clears any pending change.
- Known limits: an immediate downgrade refuses new over-cap sessions but does
  not evict existing ones; no `plan.change_scheduled` event yet.

Self-service billing history and payment-method updates go through the Stripe
Customer Portal (`POST /portal/subscription/billing-portal`,
`GET /portal/subscription/invoices`).

## Webhooks

`POST /api/v1/webhook/stripe` (signature-verified with
`STRIPE_WEBHOOK_SECRET`; auto-configured at boot when possible). Events
handled: `checkout.session.completed|expired`, `invoice.paid`,
`invoice.payment_failed`, `customer.subscription.updated|deleted`,
`charge.refunded` (matched to a license via `stripe_payment_intent_id`).

The `processed_events` table makes webhook handling idempotent — a redelivered
event is acknowledged and skipped. Outbound fan-out from these transitions
uses the events hub (`license.payment_failed`, `license.payment_recovered`,
`license.expired`, … → [SECURITY.md](SECURITY.md) / customer webhooks).

## Metered billing

Entitlements with `stripe_meter_event_name` forward usage to Stripe Billing
Meters (`service.NewMeteredBillingSyncer`, 5-minute cadence). Internal quota
accounting is authoritative; the meter sync is best-effort and resumes from
`usage_counters`.

## Perpetual maintenance renewals

Not subscriptions: `POST /portal/updates/renew` is a one-time Stripe
purchase extending `updates_until` by `plan.renewal_days` (sold at
`plan.stripe_renewal_price_id`). See
[LICENSE-ENGINE.md](LICENSE-ENGINE.md).
