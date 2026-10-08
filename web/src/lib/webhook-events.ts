// The vocabulary of webhook event names a webhook can subscribe to.
//
// This is the single source of truth shared by the admin webhook
// console and the customer portal's webhook page, so the two can never
// drift apart. Dispatch only delivers to webhooks subscribed to a
// matching event, so an event missing from this list can never be
// subscribed to and would be undeliverable for anyone configuring from
// a dashboard. The names must stay in sync with what the backend
// actually dispatches (see internal/service, internal/payment,
// internal/handler — "license.created", "license.activated",
// "quota.warning", "seat.added", "plan.changed", …). Do not invent
// event names here: an invented name is an undeliverable subscription.
export const WEBHOOK_EVENTS = [
  "license.created",
  "license.activated",
  "license.deactivated",
  "license.expiry_changed",
  "license.expired",
  "license.canceled",
  "license.suspended",
  "license.reinstated",
  "license.revoked",
  "license.payment_failed",
  "license.payment_recovered",
  "quota.warning",
  "quota.exceeded",
  "seat.added",
  "seat.removed",
  "plan.changed",
  "release.published",
  "release.yanked",
  "release.unyanked",
] as const

export type WebhookEvent = (typeof WEBHOOK_EVENTS)[number]
