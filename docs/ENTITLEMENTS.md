# Entitlements, Quotas & Feature Flags

Entitlements are per-plan feature rows (`entitlements` table) evaluated at
runtime by `internal/service/entitlement.go` and metered by
`internal/service/usage.go`. They are the bridge between what a plan sells and
what the product enforces.

## Model

One entitlement row: `(plan_id, feature)` — unique per plan.

| Column | Meaning |
|---|---|
| `feature` | Feature identifier (free-form string, namespaced by the product). |
| `value_type` | `bool` or `quota`. |
| `value` | `true`/`false` for `bool`; numeric limit for `quota`. |
| `quota_period` | `hourly` \| `daily` \| `monthly` \| `yearly` (quota resets; empty = lifetime limit). |
| `quota_unit` | Display/unit label for the metered quantity. |
| `stripe_meter_event_name` | Optional Stripe Billing Meter event emitted on each usage record (empty = internal quota only). |

Entitlements are managed from the admin API (`POST/PUT/DELETE
/api/v1/admin/entitlements*`, plan permissions) and hang off a plan —
changing the plan a license is on changes its entitlements.

## Check semantics — `POST /api/v1/license/entitlements`

Request: `{ "license_key": …, "feature": …? }` (feature optional — omit for
the whole map). Response:

```json
{
  "licensed": true,
  "status": "active",
  "plan_id": "…", "plan_name": "Professional",
  "features": {
    "api_access":  { "enabled": true,  "value_type": "bool",  "value": "true" },
    "api_calls":   { "enabled": true,  "value_type": "quota", "value": "10000",
                     "used": 4200, "limit": 10000, "remaining": 5800,
                     "period": "monthly", "resets_at": "2026-11-01T00:00:00Z" }
  }
}
```

Rules (all enforced in `EntitlementService.Check`):

- `licensed` is false and `status` is the sanitized license status for
  unusable licenses (expired/revoked/suspended) — statuses never leak
  internal detail beyond the documented vocabulary.
- `bool`: `enabled = (value == "true")`.
- `quota`: `enabled = true`; `used/limit/remaining` computed from
  `usage_counters`; `resets_at` is the next period boundary
  (`hourly/daily/monthly/yearly`, UTC).
- A feature with no entitlement row is simply absent from the map — the
  client treats absence as "not part of this plan".
- Admin plan/entitlement CRUD validates `value_type` and refuses unknown
  shapes before write.

## Usage metering — `POST /api/v1/license/usage`

Request: `{ "license_key": …, "feature": …, "quantity": 1, "metadata": {} }`
(quantity defaults to 1; idempotent with `Idempotency-Key`). Writes
`usage_events` (audit-grade event log) and increments `usage_counters`
(aggregate keyed by license + feature + period).

- Quota enforcement happens at record time; over-quota attempts are refused
  with a quota error rather than silently truncated.
- `POST /api/v1/license/usage/status` reports the same `used/limit/remaining`
  view as the entitlement check without asserting anything.
- The portal mirrors this read-only (`/portal/usage/status`); the SDK
  endpoint `/license/usage` is **ingest**, never use it for reads.
- Quota warning threshold (`app.quota_warning_threshold`, default 8000 bps =
  80%) triggers a warning email per (license, feature) — deduplicated through
  the `notifications` table.
- When `stripe_meter_event_name` is set, the metered-sync loop forwards
  increments to Stripe Billing Meters (5-minute cadence; Stripe aggregates
  server-side).

## Feature-flag semantics

Entitlements are the platform's feature flags: distribution per plan, checked
server-side at every enforcement point (verify envelope includes the feature
map; the signed offline token carries it too so offline checks see the same
answers). Guidance:

- **Flags** (`bool`): gate capabilities (`api_access`, `sso`, `white_label`…).
  The client must handle "feature absent" and `"false"` identically.
- **Limits** (`quota`): gate volumes (`api_calls`, `seats`, `projects`…).
  Never hardcode plan limits in product code (plan §104) — read the
  entitlement.
- Feature names are a closed contract with each product's clients; renaming a
  feature silently disables it for every existing plan — add the new name,
  migrate plans, then retire the old one.
- `plan.max_activations`, `plan.max_seats`, `plan.floating_timeout` and
  `plan.token_ttl_days` are **not** entitlements — they are structural plan
  columns enforced by the license engine itself (see
  [LICENSE-ENGINE.md](LICENSE-ENGINE.md)).

## Add-ons

`addons` are product-level optional capabilities attachable to a license
(`license_addons`), managed at `/admin/addons` and
`/admin/licenses/:id/addons`. Add-ons extend what a license may do without
creating a new plan; entitlement evaluation and quota metering treat them as
plan entitlements plus add-on entitlements.
