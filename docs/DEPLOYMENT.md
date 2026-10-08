# Deployment & Operations

How to deploy, upgrade and roll back the HiTechCloud Software License & Commerce Platform.

> **License notice (AGPL v3, Section 7(b))** — the "Powered by Keygate"
> attribution shown in the UI, API config responses and the `X-Powered-By`
> header is a legally required license obligation. It must remain visible
> in any deployment. See `NOTICE` and `LICENSE`; removal requires a
> commercial license (hello@keygate.app).

---

## 1. Requirements

- Docker + Docker Compose **or** a Linux host with PostgreSQL 18 and the
  single Go binary (`cmd/server` — embeds the built frontend).
- Stripe account (subscriptions/commerce work without it only for
  license-key features; checkout requires `STRIPE_SECRET_KEY`).

## 2. Environment variables

Copy `.env.example` to `.env` and fill it in. **Never commit `.env`.**

### Required

| Variable | Purpose | Generate |
|---|---|---|
| `JWT_SECRET` | Session/refresh-token signing | `openssl rand -hex 32` |
| `LICENSE_SIGNING_KEY` | License-key signing (ed25519 seed material) | `openssl rand -hex 32` |
| `DATABASE_URL` | PostgreSQL DSN | compose sets a default |

### Strongly recommended (production)

| Variable | Purpose | Generate |
|---|---|---|
| `SECRET_ENCRYPTION_KEY` | AES-256-GCM master key for secrets at rest (webhook secrets, SMTP password, OIDC client secrets, secret settings). Unset = plaintext storage, dev only. Enabling does not rewrite old rows — legacy values keep working and re-seal on next write. | `openssl rand -hex 32` |
| `REFERRAL_HASH_SALT` | Salts affiliate referral-IP fraud hashes (SHA-256). Unset = unsalted hashes, dev only. | `openssl rand -hex 32` |
| `RELEASE_KEY_ENCRYPTION_KEY` | Wraps product release-signing private keys at rest | `openssl rand -hex 32` |

### Payments (Stripe)

| Variable | Purpose |
|---|---|
| `STRIPE_SECRET_KEY` | Stripe API key (required for checkout/subscriptions) |
| `STRIPE_WEBHOOK_SECRET` | Optional explicit webhook signing secret |

### Email (optional — OTP codes print to logs if unset)

`SMTP_HOST`, `SMTP_PORT`, `SMTP_USERNAME`, `SMTP_PASSWORD`, `SMTP_FROM`

### Cache / rate limiting (optional)

`REDIS_URL` — when set, rate limits are shared across instances; unset,
each instance limits in memory. Fails fast and degrades to in-memory when
the endpoint is down.

### Webhooks

| Variable | Default | Purpose |
|---|---|---|
| `WEBHOOK_MAX_ATTEMPTS` | 5 | Delivery retry budget |
| `WEBHOOK_RETRY_INTERVAL` | 30s | Backoff interval |
| `WEBHOOK_HTTP_TIMEOUT` | 10s | Per-delivery HTTP timeout |
| `WEBHOOK_ALLOW_PRIVATE` | false | Allow loopback/private targets (same-host receivers). Keep off — SSRF surface. |

### Release storage (optional, S3-compatible)

`STORAGE_ENDPOINT`, `STORAGE_REGION`, `STORAGE_BUCKET`,
`STORAGE_ACCESS_KEY`, `STORAGE_SECRET_KEY`, `STORAGE_PUBLIC_URL`,
`STORAGE_FORCE_PATH_STYLE` — needed for release artifacts/update feeds;
license and billing work without.

### Other

`PORT` (9000), `ENVIRONMENT`, `BASE_URL`, `ADMIN_EMAILS` (comma-separated
bootstrap admins), `RATE_LIMIT_API/ADMIN/AUTH/OTP_SEND`,
`QUOTA_WARNING_THRESHOLD`.

## 3. Deploy

```bash
git clone <repo> && cd hitechcloud_keygate_licensing
cp .env.example .env   # fill in the required keys
docker compose up -d   # app on :9000 + PostgreSQL 18
curl -fsS http://localhost:9000/health
```

Release images are published by CI on every `v*` tag:

```
ghcr.io/hitechcloud-vietnam/hitechcloud_keygate_licensing:<version>
ghcr.io/hitechcloud-vietnam/hitechcloud_keygate_licensing:latest
```

Pin a version in production; use `latest` only for quick trials.

## 4. Database migrations

- Every schema change is a migration in `db/migrations/` (plan §64).
  **All 53 migrations ship with matching `.down.sql` files** — the
  schema is reversible. Verified per release.
- Migrations run automatically at server startup.
- Never alter production schema by hand.

### Pre-upgrade checklist

1. **Back up the database** (see §6).
2. Read the release notes for new environment variables.
3. Test the upgrade on a staging copy when the release contains
   migrations (the release notes say so).

## 5. Rollback procedure

1. **Stop the new version** and pin the previous image tag:
   ```bash
   docker compose pull app:<previous-tag>   # or edit compose image tag
   docker compose up -d
   ```
2. **Revert schema migrations** when the upgrade added any. The migration
   names in the release notes tell you which. Migrations are ordered by
   timestamp — revert only the ones the failed release added, newest
   first:
   ```bash
   # inside the app container (or any host with the binary + DATABASE_URL)
   server migrate down --steps <N>
   ```
   Each migration's `.down.sql` restores the previous schema exactly
   (down files are tested to reverse their `up`).
3. **Restore from backup** if data was already written against the new
   schema and the down migration cannot faithfully reverse it (the
   release notes flag any such migration).
4. Verify: `/health` returns 200 and login works.

Rollback does NOT undo: emails/webhooks already sent, Stripe objects
created (subscriptions, invoices — reconcile in the Stripe dashboard),
files uploaded to release storage.

## 6. Backup / restore

```bash
# backup (before every upgrade)
docker compose exec -T postgres pg_dump -U <db-user> -d <db-name> > backup-$(date +%F).sql

# restore
docker compose exec -T postgres psql -U <db-user> -d <db-name> < backup-<date>.sql
```

## 7. Release process (maintainers)

1. All checks green: `go test ./... -count=1`, `cd web && bun run typecheck && bun run lint && bun run build`.
2. Update release notes (new env vars, migrations, behavior changes).
3. Tag and push:
   ```bash
   git tag v<MAJOR>.<MINOR>.<PATCH>
   git push origin v<MAJOR>.<MINOR>.<PATCH>
   ```
4. CI (`.github/workflows/release.yml`) builds the image, publishes to
   ghcr and opens a **draft** GitHub release — review and publish it.

## 8. Known limitations (v0.2.0)

- Renewals bill the plan's Stripe Price — wholesale reseller overrides
  apply to the initial checkout only; renewal-time wholesale pricing is
  documented future work.
- Immediate plan downgrades stop granting new activations/seats over the
  new caps but do not evict existing over-cap sessions.
- Subscription ChangePlan scheduling dispatches `plan.changed` at
  execution; there is no separate `plan.change_scheduled` event.
- PDF report export is intentionally not implemented (CSV + JSON are).
- Run `go test ./internal/payment/ -count=1` with `TEST_DATABASE_URL` in
  CI to execute the Stripe integration suite.
