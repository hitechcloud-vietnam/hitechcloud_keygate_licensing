# Contributing

Thanks for contributing. This project is **AGPL v3 with a Section 7(b)
attribution clause** — before anything else, read
[`docs/LICENSE-COMPLIANCE.md`](docs/LICENSE-COMPLIANCE.md). The "Powered by
Keygate" attribution is a legally required obligation: PRs that remove or
hide it cannot be accepted (a commercial license exists for white-labeling:
hello@keygate.app).

## Development setup

Requirements:

- **Go 1.27** (`go.mod`)
- **bun** (frontend toolchain: install, typecheck, lint, build)
- **PostgreSQL 18** — via `docker compose up -d postgres` (the compose file
  bundles `postgres:18-alpine`), or any local instance.

```powershell
# Windows / PowerShell
git clone <repo>; Set-Location hitechcloud_keygate_licensing
Copy-Item .env.example .env          # set JWT_SECRET + LICENSE_SIGNING_KEY (openssl rand -hex 32)
docker compose up -d postgres
go run ./cmd/server                   # auto-migrates on boot; API on :9000
```

```bash
# Linux / macOS
cp .env.example .env && docker compose up -d postgres
go run ./cmd/server
```

Frontend dev server (hot reload, proxies to the API):

```bash
cd web && bun install && bun run dev
```

Useful endpoints while developing: `GET /health`, `GET /api/v1/config`,
`GET /docs` (OpenAPI). In `ENVIRONMENT=development`,
`POST /api/v1/auth/dev-login` gives a session without OTP.

### Seed data

```bash
go run ./scripts/seed        # idempotent — safe to re-run
```

Seeds an owner user, demo products (HiTechCloud Panel …), plans with
entitlements and multi-currency prices (int64 minor units), coupons, and a
demo customer + license. **No real credentials are ever seeded** (plan §65).
Flags: `go run ./scripts/seed -h`.

## Make targets

| Target | What it does |
|---|---|
| `make build` | `build-web` + `build-go` → `bin/…` |
| `make build-go` / `make build-web` | Backend binary / `web/dist` |
| `make dev` / `make dev-web` | `air` hot reload / Vite dev server |
| `make fmt` | `goimports` + `gofmt` (Go), `biome check --fix` (web) |
| `make lint` | `go vet ./...` + `biome check src` |
| `make test` | `go test ./...` |
| `make check` | lint + test + build (what CI runs) |
| `make db-backup` / `make db-restore BACKUP=…` | pg_dump / psql |
| `make docker-build` / `docker-up` / `docker-down` / `docker-logs` | Compose workflow |
| `make clean` / `clean-all` | Remove build artifacts / + node_modules |

## Test discipline

A change is done when (plan §100):

- `go test ./...` passes. DB-backed tests skip unless `TEST_DATABASE_URL`
  points at a disposable database — run them before merging anything touching
  `internal/store`.
- `cd web && bun run typecheck && bun run lint && bun run build` all exit 0.
- **i18n key parity**: `web/src/lib/i18n` locales (en, vi, zh) are one
  `Translations`-typed set — every new UI string lands in **all** locales or
  the type check fails.
- New behavior ships with tests; `pkg/response`'s contract test enforces
  **one HTTP status per error code repo-wide** — reuse existing codes before
  inventing one.
- No floats in money paths — int64 minor units + bps only (plan §51).

## Code style

- **Go**: `gofmt` clean, `go vet` clean, std-lib-first (think before adding a
  dependency), table-driven tests, sentinels over string matching for store
  errors. House pitfalls (Bun aliases by snake_case struct name, no
  `bun:"default:N"` on zero-meaningful fields) are documented in
  [`docs/DATABASE.md`](docs/DATABASE.md).
- **TypeScript/React**: Biome (`biome check src`), typed API client in
  `web/src/lib/api.ts`, money handled by `web/src/lib/money.ts` (string-based
  minor units), accessible components (aria labels, keyboard paths).
- **SQL**: migrations only — see the migration workflow in
  [`docs/DATABASE.md`](docs/DATABASE.md). Every migration needs a working
  `.down.sql`.

## Commits & pull requests

- Commit convention (plan §102): `feat:`, `fix:`, `refactor:`, `perf:`,
  `security:`, `test:`, `docs:`, `chore:` — e.g.
  `feat: add concurrent license activation`.
- Feature branches off `main` (plan §101): `feature/license-engine`,
  `feature/commerce`, … — don't push directly to `main` during active
  development.
- Every PR uses the template in `.github/`: describe what/why, database and
  API changes, test plan, and confirm the attribution is untouched (plan
  §103).
- Keep PRs focused; large multi-domain changes should be split.

## Legal / license headers

- This repository is AGPL v3 + Section 7(b); contributions are accepted under
  the same terms (see [`LICENSE`](LICENSE), [`NOTICE`](NOTICE)).
- Do **not** add per-file license headers unless the file is copied from
  another project that requires its notice preserved — keep third-party
  notices in `NOTICE`.
- Never commit secrets, `.env`, private keys or production credentials
  (plan §67). `.env.example` documents variable names only.
- Documentation and UI copy must present the attribution as a compliance
  obligation, never suggest removing it.
