# License Compliance

This project is licensed under **AGPL v3 with additional terms per Section 7(b)**
(see [`LICENSE`](../LICENSE) and [`NOTICE`](../NOTICE)).

## The attribution obligation

The **"Powered by Keygate"** attribution notice (hyperlinked to
<https://keygate.app>) is a **legally required license obligation** — not a
feature, theme or configuration option. It must remain visible and functional
in every deployment of this software, including modified versions and versions
served over a network.

This document explains **how to comply**. It does not explain — and will not
be extended to explain — how to remove the attribution. Removal requires a
commercial license: **hello@keygate.app**.

## Where the attribution appears

| Surface | Mechanism | Code |
|---|---|---|
| Admin panel, customer portal, marketplace, checkout, login, setup | SPA footer / pages | `web/src/components/layout.tsx`, `web/src/hooks/use-site-config.tsx`, page footers |
| Email footers | HTML footer appended to every outgoing mail | `internal/branding/branding.go` → `EmailFooter` |
| API config responses | `attribution_text` / `attribution_url` fields | `/api/v1/config`, `/api/v1/site-config` (`cmd/server/main.go`, `internal/handler/site_config.go`) |
| Every HTTP response | `X-Powered-By` header | `internal/branding/branding.go` (`HeaderKey`), security-headers middleware in `cmd/server/main.go` |

The constants live in one place (`internal/branding`) and are covered by tests
(`internal/branding/branding_test.go`, `internal/handler/site_config_test.go`).
API tests pin the attribution fields so regressions fail CI.

## Obligations checklist for operators

- [ ] The `X-Powered-By` response header is present and unmodified on all
      responses (do not strip it in your reverse proxy / CDN).
- [ ] The "Powered by Keygate" link is visible in the UI (admin, portal,
      login, checkout) and points to `https://keygate.app`.
- [ ] Outgoing email footers keep the attribution line.
- [ ] API consumers of `/api/v1/config` and `/api/v1/site-config` receive
      `attribution_text` / `attribution_url` unchanged.
- [ ] If you run a **modified** version and let others interact with it over a
      network, you offer those users the corresponding source of your
      modifications (AGPL v3 §13 — remote network interaction). Publishing a
      public source repository is the usual way to satisfy this.
- [ ] `NOTICE` and `LICENSE` travel with the software and any distribution.

## Forks, white-labeling and resale

- Forking and self-hosting for your own business is permitted and free.
- **White-labeling** (removing/replacing the attribution) or reselling the
  platform as a closed service requires the **commercial license**:
  **hello@keygate.app**.
- Do not "rebrand" by editing `internal/branding` or patching the SPA footer.
  Both are covered by the additional terms; doing so terminates the AGPL grant.

## Dependency licenses

Runtime dependencies are permissive or compatible (Gin, Bun ORM, pgdriver,
prometheus client, goxmldsig, AWS SDK for S3-compatible storage, Stripe SDK;
MIT-licensed React/Vite/Biome toolchain on the frontend). Re-verify the full
set with `go-licenses` / `bun licenses` before redistribution if your policy
requires an SBOM.

## Related

- [`NOTICE`](../NOTICE) — the attribution requirement in full.
- [`docs/SECURITY.md`](SECURITY.md) — vulnerability disclosure contact.
