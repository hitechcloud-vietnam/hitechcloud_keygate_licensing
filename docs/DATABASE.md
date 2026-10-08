# Database

PostgreSQL 18 (bundled in `docker-compose.yml`), accessed through Bun ORM
(`github.com/uptrace/bun` + `pgdriver`). All schema changes are migrations in
`db/migrations/` — never edit production schema by hand (plan §64).

## Money rule (plan §51)

**Every monetary column is `BIGINT` int64 minor units** at the currency's
ISO-4217 exponent: VND exponent 0 (whole dong), USD/EUR exponent 2. Percent
rates are `INT` basis points (10000 = 100%). No floats anywhere in the
schema or the code paths that read it (`internal/money`).

## Schema by migration family

| Family | Tables | Purpose |
|---|---|---|
| `20260320_init` | `users`, `oauth_accounts`, `products`, `api_keys`, `plans`, `entitlements`, `licenses`, `activations`, `audit_logs` | Core identity + catalog + license engine. |
| `20260321_saas_extension` | `seats`, `usage_events`, `usage_counters`, `webhooks`, `webhook_deliveries`, `analytics_snapshots`, `subscriptions` | SaaS features: seats, metering, webhooks, Stripe subscriptions. |
| `20260322_*` | `email_queue`, `floating_sessions`, `addons`, `license_addons`, `metered_billing`, `notifications`, `processed_events`, `refresh_tokens`, `settings` + `licenses.key_hash` | Queue, floating licenses, add-ons, dedup/idempotency primitives, config store. |
| `20260401_*` | `otp_codes`, `plans.checkout_id` | Passwordless login; stable checkout URLs. |
| `20260510–20260516` | `releases`, `release_artifacts`, `release_signing_keys`, `idempotency_keys`, `licenses.license_key_encrypted` | Release bundles (20260515 refactor keeps `releases_legacy` as rollback evidence), Ed25519 signing, key encryption at rest. |
| `20260908_maintenance_period` | `plan_update_terms`, `license_renewals` | Perpetual maintenance terms history (DB trigger appends) + renewal ledger. |
| `20261007_*` | `coupons`, `tax_rates`, `orders`, `order_items`, `invoices` | Commerce ledger (Phase 4). |
| `20261008_131000–142000` | `customer_api_keys`, `categories`, `product_categories`, `customer_webhooks`, `resellers`, `reseller_licenses`, `reseller_customers`, `commissions`, `reseller_price_overrides`, `affiliates`, `referral_codes`, `referral_clicks`, `affiliate_conversions`, `affiliate_payouts`, `product_reviews`, `sso_connections`, `scim_tokens`, `scim_identities` | Portal keys, marketplace catalog, self-service webhooks, partner programs, reviews, enterprise SSO/SCIM. |
| `20261008_143000–149000` | `custom_roles`, `custom_role_permissions`, `user_custom_roles`, `user_notifications`, `gateway_payments`, `refunds`, `plan_prices` + `orders` attribution columns + perf indexes | Advanced RBAC, notification inbox, VN gateway payments (UNIQUE `(provider, provider_ref)`), refunds, multi-currency prices. |
| `20261008_148000` | `settings` catalog seed | config-in-DB: 71-key catalog rows (see [ARCHITECTURE.md](ARCHITECTURE.md)). |

Key constraints worth knowing:

- `licenses.license_key` UNIQUE; `licenses.stripe_subscription_id` UNIQUE;
  `activations UNIQUE(license_id, identifier)`.
- `plans UNIQUE(product_id, slug)`; `entitlements UNIQUE(plan_id, feature)`;
  `coupons.code` UNIQUE (case-folded on lookup).
- `plan_prices UNIQUE(plan_id, currency)` + partial unique index enforcing at
  most one `is_default` per plan.
- `gateway_payments UNIQUE(provider, provider_ref)` — payment-idempotency.
- `affiliate_conversions UNIQUE(order_id)` — one conversion per order.
- `commissions UNIQUE(reseller_id, order_id)` — accrual idempotency.
- `product_reviews UNIQUE(product_id, customer_email)` (case-folded).
- `idempotency_keys UNIQUE(scope, idempotency_key)`.

## Migration workflow

`store.RunMigrations("db/migrations")` runs automatically at server boot:

1. Advisory lock (`pg_advisory_lock`) — only one instance migrates at a time.
2. Each `*.up.sql` and its `schema_migrations` record commit in **one
   transaction** — a failure rolls back fully, never half-applied.
3. SHA-256 checksums of applied files are verified on every boot — editing an
   applied migration aborts startup with a clear error.
4. Every migration has a matching `*.down.sql` (verified for all files).

Conventions:

- New change = **new** `YYYYMMDD_HHMMSS_name.up.sql` + `.down.sql` pair. Never
  modify an applied file.
- Name with a timestamp, not sequence numbers, so parallel agents don't
  collide.
- Keep `.down.sql` genuinely reversible (rename-and-keep beats `DROP` when
  rollback evidence matters — see `releases_legacy`).
- Data backfills in the migration itself when small; use a resumable
  background job when large (see `BackfillLicenseKeyEncrypted`).
- Check migration status in the admin UI (`GET /api/v1/admin/system/migrations`).

## Bun ORM notes (house pitfalls)

- Bun aliases models by the **snake_case of the struct name**, not the table
  name: `FROM "orders" AS "order"`, `"plan_price"`, `"commission"`,
  `"user_notification"`. Hand-written SQL qualifiers must use that alias
  (`"order".id` — quote it, ORDER is reserved) or Postgres errors
  "missing FROM-clause entry". Pinned by `internal/store/bun_alias_test.go`.
- Never add `bun:"default:N"` to model fields whose zero value is meaningful
  (plans, coupons) — bun would rewrite a deliberate `0` into the column
  default. The writer is the source of truth.
- All-caps field names need explicit column tags (`bun:"column:saml_sso_url"`).
- Money arithmetic in SQL is integer-only; rounding helpers live in
  `internal/money`, never inline.

## Seeding

Development data: `go run ./scripts/seed` (idempotent — safe to re-run; see
[CONTRIBUTING.md](../CONTRIBUTING.md)). Never seed real credentials (plan
§65).
