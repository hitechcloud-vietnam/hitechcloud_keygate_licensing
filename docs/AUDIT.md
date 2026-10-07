# HiTechCloud — Repository Audit (Phase 0)

> Phase 0 deliverable per `plan.md` §106 (Task 0) and §99 (Implementation Phases).
> Purpose: establish the source of truth before parallel feature work, per §96–98 (multi-agent
> coordination) and §108 (file/domain ownership boundaries). Last reviewed: 2026-10-07.

## 1. Repository structure

- `cmd/server/` — single binary entrypoint, route registration, Redis, frontend embedding.
- `internal/` — domain code: `model/`, `store/`, `service/`, `handler/`, `middleware/`, `payment/`, `license/`, `crypto/`, `config/`, `branding/`, `breaker/`, `storage/`, `version/`, `testsupport/`.
  - New pricing engines (this cycle): `internal/money/`, `internal/coupon/`, `internal/tax/`.
- `pkg/` — shared transport helpers: `apperr/`, `response/`.
- `db/migrations/` — ordered up/down SQL migrations.
- `web/` — React + Vite + TypeScript (Bun) frontend.
- `docs/` — `openapi.yaml`, `api-contract.md`, this audit.

## 2. Framework / runtime

- Backend: Go 1.27, Gin (HTTP), Bun ORM, PostgreSQL 18, Redis (optional cache/rate-limit).
- Frontend: React 19 + Vite + TypeScript, Biome (lint/format), Bun package manager.
- Deployment: single static binary (embedded frontend), Docker, docker-compose.

## 3. Backend architecture

Layered: `handler` (HTTP) → `service` (business logic) → `store` (Bun ORM persistence) → `model` (structs). Payment integrations live in `internal/payment/`. Cross-cutting: middleware (auth, API-key scopes, rate-limit, metrics, idempotency), `pkg/apperr`/`pkg/response` for the API error contract.

## 4. Frontend architecture

React SPA with three surfaces: public (login/setup/checkout), admin console (`/admin`), customer portal (`/portal`). i18n via `web/src/i18n/` (en + zh locales). State via `web/src/hooks/` (e.g. `use-site-config`).

## 5. Database architecture

PostgreSQL, migrations in `db/migrations/`. Core tables: users, oauth_accounts, otp_codes, products, api_keys, plans, entitlements, licenses, activations, audit_logs; SaaS extension: seats, usage_events, usage_counters, webhooks, webhook_deliveries, analytics_snapshots, subscriptions; releases + release_artifacts + release_signing_keys; floating_sessions, addons, license_addons, metered_billing, plan_update_terms, license_renewals, idempotency_keys, settings, notifications, refresh_tokens, email_queue.

## 6. Authentication

Passwordless email OTP, OAuth account linkage, dev-login (non-prod), refresh-token rotation, session cookie for portal/admin. Invite acceptance flow.

## 7. Authorization / RBAC

User roles `owner` / `admin` / `user` (`users.role`, migration `20260325_user_roles`). API keys carry scopes (`admin`, `licenses:write`, `releases:write`). Route groups guarded by role + scope middleware in `cmd/server/main.go`. Frontend never trusted for authorization.

## 8. License system (Phase 2 — mostly complete)

License generation/verify (`service/license.go`, `handler/license.go`), activation/device lock (`Activation`), seats, expiry + grace math, renewal (`license_renewals`, `RenewUpdates`), revoke/suspend/reinstate, floating/concurrent (`floating_sessions`), offline activation via Ed25519 signed tokens (`internal/license/token.go`, `plans.token_ttl_days`). License keys encrypted at rest (HKDF `keygate-v1-`, migration `20260514`).

## 9. Subscription system (Phase 3 — delegated to Stripe)

Trial (`trial_days`), subscription lifecycle, renewal, cancellation (incl. cancel-at-period-end), upgrade/downgrade with Stripe proration (`payment/stripe.go` `ChangePlan`), grace period, past-due dunning ladder (`past_due_at`).

## 10. API

Public machine API `/api/v1` (license activate/verify/deactivate/usage/entitlements, floating, seats, release feeds, public pricing, setup, auth, Stripe webhooks). Customer portal `/api/v1/portal` (session). Admin `/api/v1/admin` (role + scopes): catalog, licenses, releases/signing, webhooks, settings, analytics, audit, users. Contract in `docs/openapi.yaml`.

## 11. UI

Admin console: dashboard, products, plans, releases, licenses, api-keys, webhooks, addons, analytics, audit, customers, settings/email-templates. Portal: licenses (activations, usage, seats, invoices, change-plan, renew), account. Public: login, setup, accept-invite, checkout-success.

## 12. Tests

Go table-driven unit tests across `internal/*` and `pkg/*`. Integration tests gated on `TEST_DATABASE_URL` (`-run Atomic ./internal/store/`). Frontend: typecheck (`tsc -b`), lint (`biome check`), build. New pricing engines ship with thorough table-driven tests.

## 13. CI/CD

`.github/workflows/ci.yml`: backend (go vet, go test, integration, build) + frontend (bun install --frozen-lockfile, typecheck, lint, build). `.github/workflows/release.yml`: tag-triggered draft GitHub release + ghcr.io Docker image.

## 14. Docker / deployment

`Dockerfile` (multi-stage, output binary `hitechcloud`), `docker-compose.yml` (app + postgres + redis). Self-hosted, single-binary deployment.

## 15. Dependency inventory

Go: gin, bun ORM, stripe-go/v82, crypto (Ed25519, HKDF), redis client. Frontend: react, react-router, vite, i18n, biome. No microservices; single deployable.

## 16. License compliance

AGPL v3 + Section 7(b). "Powered by Keygate" attribution is a legally required obligation preserved across UI (`use-site-config`, layout, login), `internal/branding/`, `X-Powered-By` header, `attribution_text`/`attribution_url` API fields. See `NOTICE`/`LICENSE`. NOT removable under AGPL.

## 17. Extension points

- Payment abstraction seam: `licenses.payment_provider` / `subscriptions.payment_provider` + `external_id` (Stripe wired; others pluggable).
- `LicenseRenewal` ledger is a ready pattern for an Order/Invoice ledger.
- Public pricing API (`plans_public.go`) is a base for a Marketplace storefront.
- OAuth groundwork (`oauth.go`) is a base for SAML/SSO.

## 18. Conflicts with the specification

- Marketing/commit text advertises "VNPay, ZaloPay, Pay2s" but only **Stripe** is implemented.
- No `Tenant`/`Organization` entities (only `licenses.org_name` text) — multi-tenancy absent.
- No `Price`, `Feature` entities as first-class models (price lives on `Plan.StripePriceID`).
- No `Order`, `Invoice`, `Coupon`, `Tax`, `Cart` models yet (Phase 4 gap) — coupon/tax **engines** added this cycle as building blocks.

## 19. Recommended implementation order

1. **Phase 4 Commerce (in progress):** `money`/`coupon`/`tax` engines DONE (this cycle). Next: `Order` + `Invoice` ledger (reuse `LicenseRenewal` pattern) → wire coupon/tax into checkout → `Coupon`/`Tax` DB models + admin CRUD.
2. **Phase 5 completion:** portal orders page, downloads UI, customer API keys/webhooks.
3. **Phase 6 Marketplace:** storefront, product pages, search (build on public pricing API).
4. **Phase 7 Reseller/Affiliate** (greenfield) and **Phase 8 Enterprise** (SAML/SSO/SCIM, contracts).
5. Multi-tenancy (Tenant/Organization) if targeting SaaS.

## 20. Risk register

| Risk | Impact | Mitigation |
|---|---|---|
| Payment-provider claims vs reality | Misleading to users | Implement VNPay/ZaloPay/Pay2s or adjust marketing text |
| Single-file `model.go` + `main.go` route block | Merge conflicts under parallel agents | Lead Architect owns shared core; agents own disjoint packages (§108) |
| Floating-point money bugs | Financial errors | `internal/money` integer minor units; no float in pricing paths |
| License signing key rotation | Token verification break | Version/key-ID tokens before automating rotation |
| Tenant isolation absent | Cross-tenant risk if SaaS | Add Tenant/Organization + server-side isolation before SaaS launch |
