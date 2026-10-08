# Multi-Tenancy (plan §7)

> **Status: NOT implemented — documented deferral.**
> The platform is **single-tenant** today: one install serves one merchant
> organization. There is no `organizations`/`tenants` table and no `org_id`
> column anywhere in the schema (`db/migrations/`). This document records the
> current reality and the concrete design + rollout plan for adding
> organizations. Nothing here describes shipping behavior.

## Current model (as built)

- One deployment = one operator's business. Isolation between *customers* of
  that merchant is by **resource ownership**: license `email`, seat
  membership, session user id. Cross-user/cross-customer reads answer 404
  (no existence oracle) — see [SECURITY.md](SECURITY.md).
- Isolation between *operators* is by **deployment** (separate database /
  docker-compose stack). That is the only supported "tenant" boundary.
- The admin surface is flat: `users.role` (`owner|admin|user`) + granular RBAC
  (`custom_roles`, 23 permissions) govern access within the single
  organization.
- Surface routing (`internal/surface`) splits **hostnames**, not tenants —
  payments./dashboard./merchant./customer./verify. are views of the same data.

Consequences to be honest about: two businesses cannot share one install; a
future "SaaS-of-SaaS" offering requires the work below.

## Target design

### Schema

```sql
CREATE TABLE organizations (
    id          TEXT PRIMARY KEY,          -- uuid, like every other PK
    name        TEXT NOT NULL,
    slug        TEXT NOT NULL UNIQUE,      -- handle, host mapping
    status      TEXT NOT NULL DEFAULT 'active',
    settings    JSONB NOT NULL DEFAULT '{}',  -- per-org config overrides
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE org_memberships (
    id         TEXT PRIMARY KEY,
    org_id     TEXT NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       TEXT NOT NULL,              -- org-scoped role bundle
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (org_id, user_id)
);
```

- Every tenant-owned table gains `org_id TEXT NOT NULL REFERENCES
  organizations(id)` **with an index**: products, plans, entitlements,
  addons, licenses, activations, seats, usage_*, orders, order_items,
  invoices, refunds, coupons, tax_rates, subscriptions, releases,
  release_artifacts, release_signing_keys, webhooks, webhook_deliveries,
  customer_webhooks, categories, product_reviews, resellers*, commissions,
  affiliates*, api_keys, customer_api_keys, sso_connections, scim_tokens,
  notifications, user_notifications, audit_logs, idempotency_keys,
  gateway_payments, plan_prices.
- Global-only tables stay unscoped: `users`, `organizations`,
  `org_memberships`, `settings` (platform config) — with per-org overrides in
  `organizations.settings`.
- All unique constraints become tenant-scoped:
  `UNIQUE(org_id, slug)` on products/categories, `UNIQUE(org_id, code)` on
  coupons, `UNIQUE(org_id, license_key)` is pointless (keys are globally
  random) but lookups add `org_id = ?` anyway for defense in depth.

### Data isolation levels (choose per surface)

| Level | Mechanism | Applies to |
|---|---|---|
| **L1 — Query scoping** (start here) | Every store method takes `orgID` and filters `WHERE org_id = ?`; repository layer refuses unscoped queries (lint/test pin) | All application code |
| **L2 — DB enforcement** | PostgreSQL **Row-Level Security** policy `USING (org_id = current_setting('app.org_id'))` with `SET LOCAL app.org_id` per request/transaction | Defense in depth for every tenant table |
| **L3 — Credential scoping** | JWT carries `org_id` claim; API keys, SCIM tokens, license keys resolve to exactly one org; SSO connections are per-org | Auth middleware |

L1 is mandatory; L2 is the rollout payoff (a missed `WHERE org_id` becomes a
deny, not a leak); L3 keeps credentials from crossing tenants even when
guessed.

### Application changes per surface

- **Store layer**: every `internal/store` method signature gains `orgID
  string` (or an `OrgScope` struct); unscoped variants are deleted. Bun
  aliases and query-shape tests updated accordingly.
- **Auth**: `SessionAuth`/`SessionOrAPIKey` populate `org_id` from membership
  (a user may belong to N orgs — org selection via header
  `X-Org-Id`/subdomain, default = first membership). `RequirePermission`
  resolves within the current org.
- **License API (`/license/*`)**: license key → org automatically (FK);
  no client-visible change.
- **Payments**: Stripe keys per org (each org connects its own Stripe —
  `organizations.settings` holds sealed creds via the existing
  `crypto.SecretBox`), order/invoice numbering prefixed per org.
- **Surfaces**: `domain.<surface>` becomes per-org host maps
  (`organizations.settings`), `surface.FromSettings` learns an org resolver.
- **Admin/portal UI**: an org switcher (like the existing language
  switcher); every list page passes the org implicitly via session.
- **Reports/metrics**: all aggregations group/filter by `org_id`; MRR/ARR are
  per-org with a platform-level rollup for the operator.
- **Webhooks/emails**: branding (site name, logo, email templates) moves to
  per-org settings; attribution requirements are unchanged
  (see [LICENSE-COMPLIANCE.md](LICENSE-COMPLIANCE.md) — the attribution is
  platform-level and not tenant-configurable).
- **Search/exports**: admin search, CSV exports and retention jobs all
  iterate per org.

### Migration strategy

1. **Phase A — schema + backfill.** Add `organizations`/`org_memberships`;
   add nullable `org_id` columns; create a default organization
   (`slug='default'`) and backfill every existing row in one migration
   (single-tenant installs are small enough). Then set `NOT NULL` +
   FK + indexes in a follow-up migration.
2. **Phase B — scoped store layer.** Thread `org_id` through store methods
   behind the existing API surface (handlers derive org from session; behavior
   unchanged for single-org installs). Contract tests assert scoped queries
   (extend the existing query-shape pin tests).
3. **Phase C — RLS.** Enable RLS + `SET LOCAL app.org_id` in a request-scoped
   transaction wrapper. Run the tenant-isolation audit suite (the existing
   DB-gated isolation tests generalize) against a two-org fixture.
4. **Phase D — product surface.** Org switcher, org settings, per-org
   Stripe/domain config, org-scoped numbering. Signup flow gains
   org creation.
5. **Phase E — enforcement hardening.** Remove the last unscoped paths,
   add cross-org negative tests to CI, publish the tenant-isolation audit.

Rollback: Phases A–C are additive and reversible (`.down.sql` per phase);
RLS can be disabled per table without dropping columns.

### Risks

- **Unique-constraint rewrites** can break external integrations that assume
  globally unique slugs/codes — document per-tenant semantics before Phase D.
- **RLS + connection pooling**: every pooled transaction must set
  `app.org_id`; a leaked `SET` across transactions is a cross-tenant read —
  Phase C must include a regression test for this exact failure.
- **Stripe key handling** multiplies secret surface — reuse `SecretBox`, add
  per-org key rotation before enabling multi-org billing.
- **Performance**: composite indexes `(org_id, …)` everywhere; pagination
  queries must include `org_id` in the sort key to stay stable.

## Related

- [ARCHITECTURE.md](ARCHITECTURE.md) — current single-tenant architecture.
- [DATABASE.md](DATABASE.md) — schema/migration conventions this plan extends.
- `docs/AUDIT.md` — roadmap status (multi-tenancy tracked as deferred).
