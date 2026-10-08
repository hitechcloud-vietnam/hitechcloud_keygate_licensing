## What does this PR do?

<!-- Brief description of the change -->

**plan.md section:** <!-- e.g. §65 Seed data — or "n/a" -->

## Why?

<!-- Link to issue or explain the motivation -->

## Changes

- **Database:** <!-- migration files added/changed, or "none" -->
- **API:** <!-- new/changed endpoints or error codes, or "none" -->
- **UI:** <!-- user-visible changes, or "none" -->
- **Security impact:** <!-- auth/secret/tenant-isolation implications, or "none" -->

## How to test

<!-- Steps to verify the change works -->

## Checklist

- [ ] `go vet ./...` passes
- [ ] `go test ./...` passes (DB-backed tests run with `TEST_DATABASE_URL` when affected)
- [ ] Frontend builds (`cd web && bun run typecheck && bun run lint && bun run build`)
- [ ] New/changed endpoints documented or OpenAPI updated
- [ ] Migrations have working `.down.sql` (if schema changed)
- [ ] Tested manually in browser

## Legal

- [ ] The "Powered by Keygate" attribution is untouched (UI, email footers, `attribution_text`/`attribution_url`, `X-Powered-By`) — required by AGPL v3 §7(b); see `docs/LICENSE-COMPLIANCE.md`. Removal requires a commercial license and must not be attempted in a PR.
