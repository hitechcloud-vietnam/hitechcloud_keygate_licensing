# Data retention (plan §92)

Keygate keeps operational records on a schedule and financial records
forever. One background job — `service.NewRetentionJob` — runs the
pass; one admin endpoint — `POST /api/v1/admin/retention/run` — runs
it on demand.

## What is deleted, when

Every horizon is a setting (integer **days**), read fresh on every
pass so an operator can change it without a restart:

| Table                | Setting key                        | Clock column | Default | Batch |
|----------------------|------------------------------------|--------------|---------|-------|
| `notifications`      | `retention.notifications_days`     | `sent_at`    | 90      | 1000  |
| `processed_events`   | `retention.processed_events_days`  | `created_at` | 30      | 1000  |
| `webhook_deliveries` | `retention.webhook_deliveries_days`| `created_at` | 90      | 1000  |
| `audit_logs`         | `retention.audit_logs_days`        | `created_at` | 365     | 1000  |

A row is deleted when its clock column is **strictly older** than
`now − horizon` (calendar days, UTC).

### Setting semantics

* a positive integer `N` → delete rows older than `N` days
* **`0` → never delete this table** (explicitly recorded as 0 in the
  result, not by omission)
* a missing setting, an empty value, a non-integer or a negative
  number → the **default** above (a broken setting fails safe: it
  never deletes more than the default horizon would)

## What is NEVER deleted

The financial record set is permanent — an order ledger that loses
rows stops being a ledger:

* `orders`, `order_items`, `invoices`, `refunds`
* `licenses`, `subscriptions`, `plans`, `products`, `plan_prices`
* payments, refunds, commissions, conversions, payouts, coupons,
  tax rates, settings, users, API keys

The retention job only ever touches the four tables listed above; it
has no code path that reaches a financial table.

## Mechanics

* Rows go in **batches** (`store.RetentionBatchSize`, 1000): each
  statement deletes at most one batch, and the pass loops until a
  batch comes back short. Row locks stay short on big tables.
* The pass is **idempotent**: a second run deletes nothing new and
  reports 0. A missed tick loses nothing — the next run catches up.
* Partial failure: the tables are drained one after another
  (hot/cheap first, `audit_logs` last); counts already collected are
  reported even when a later table fails.

## API

`POST /api/v1/admin/retention/run` (permission:
`settings:manage`)

```json
{
  "success": true,
  "data": {
    "deleted": {
      "notifications": 12,
      "processed_events": 0,
      "webhook_deliveries": 3,
      "audit_logs": 0
    }
  }
}
```

Every run writes an audit entry (`entity=settings`,
`action=retention_run`) with the per-table counts.

## Scheduling

`service.NewRetentionJob(store, logger).Run(ctx)` performs one pass
and logs its counts. Wire it to the existing hourly cleanup ticker in
`cmd/server/main.go` — the job is stateless and safe to run on any
replica, any number of times.
