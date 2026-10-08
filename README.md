<div align="center">

<img src="web/public/logo.svg" width="72" height="72" alt="HiTechCloud" />

# HiTechCloud Software License & Commerce Platform

**Open source software license and commerce platform.**

The self-hosted alternative to Keygen, Cryptlex, and LicenseSpring.

[Website](https://hitechcloud.vn) · [Documentation](https://hitechcloud.vn/docs) · [Community](https://github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/discussions)

[![License](https://img.shields.io/badge/license-AGPL%20v3-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/hitechcloud-vietnam/hitechcloud_keygate_licensing?label=release&color=green)](https://github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/releases)
[![Stars](https://img.shields.io/github/stars/hitechcloud-vietnam/hitechcloud_keygate_licensing?style=flat)](https://github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/stargazers)

**[English](README.md)**

<br />

<img src="web/public/screenshot.png" width="800" alt="HiTechCloud Dashboard" />

</div>

<br />

## Why HiTechCloud?

You've built great software. Now you need to decide who can use it, how they pay for it, and what features they get access to.

Commercial license platforms charge per-seat, per-month, and your customer data lives on someone else's servers. Building your own takes months of engineering on activation logic, payment webhooks, quota tracking, and all the edge cases that come at 2 AM.

**HiTechCloud is the middle ground.** A production-ready license server you deploy on your own infrastructure, connect to your own Stripe, and manage through a clean dashboard. It handles everything from activation to dunning — so you can focus on building your product.

One server. One database. Full control. Free, forever.

<br />

## Who is it for?

| | |
|:---|:---|
| **🧑‍💻 Indie Developers** — Selling a desktop app, CLI tool, or Electron app? HiTechCloud handles license keys, activation limits, and trials so you can focus on shipping. | **🏢 SaaS Companies** — Managing subscription tiers with different feature sets? Define plans with entitlements, track usage, and let Stripe handle billing automatically. |
| **🏭 Enterprise Vendors** — Need floating licenses for large teams? Concurrent seat checkout with heartbeat monitoring, perfect for shared-seat environments. | **⚡ API Providers** — Enforcing rate limits and usage quotas? Atomic quota enforcement tracks every call and warns customers before they hit limits. |

<br />

## Features

### 🔑 License Management

Every model in one platform — **subscriptions**, **perpetual**, **perpetual with a maintenance period** (buy once, get a year of updates, renew from the portal), **trials**, and **floating** (concurrent) licenses. Create, activate, verify, suspend, reinstate, and revoke with full audit trail. Per-device or per-user activation limits with **atomic enforcement** (no double-counting under retry). Grace periods. License keys hashed with SHA-256, encrypted at rest. Signed tokens for offline verification. **`Idempotency-Key` header** support on writes — retries never duplicate.

Public SDK endpoints (activate / verify / deactivate / usage / download) take `license_key` directly — no embedded API keys to leak from your binaries. Customers can self-serve activation slots from the portal (free up a lost laptop without a support ticket).

### 🚀 Software Distribution

Ship signed updates to your installed clients. **Sparkle** (macOS), **Velopack** (Windows), and **Tauri** (cross-platform) updaters all consume the same release feed — one publish, every updater compatible. Per-platform binaries grouped under a single release, **atomic publish gate** (no half-uploaded releases ever leak), **yank** for instant rollback. Per-product **Ed25519 signing keys** with private keys encrypted at rest under AES-256-GCM + HKDF-derived subkeys. Server-side SHA-256 for integrity (never trust the client's hash). Stable feeds are public — your customers' auto-updater never breaks when a license rotates. Products sold with a maintenance period turn on `feed_license_required` — the updater then sends the license and only sees releases published inside the customer's update period, while the app itself keeps running either way. Per-product `minimum_supported_version` floor for forced upgrades.

Object storage is S3-compatible — Cloudflare R2, AWS S3, MinIO, anything that speaks SigV4. Presigned URLs for direct browser upload (no proxying through HiTechCloud), and license-gated short-TTL download URLs.

### 📊 Usage Metering

Track API calls, storage, bandwidth, or any custom metric. Quotas enforced **atomically at the database level** — even under high concurrency, limits are never exceeded. Hourly, daily, monthly, or yearly cycles with automatic reset. Threshold warnings via webhooks.

### 💳 Payments

Stripe integrated end-to-end with **three-layer reliability** — webhook, success-page verification, and periodic sync ensure no payment is ever missed. Customer pays → license created automatically. Payment fails → dunning emails on schedule. Supports checkout, plan upgrades/downgrades with proration, cancellations, refunds, billing portal, and one-time renewals of a perpetual license's update period from the customer portal. Stripe webhook is auto-configured — just set your API key.

### 👥 Team Seats & Entitlements

Customers manage their own teams within a license. Seat roles (owner/admin/member), configurable limits per plan. Feature entitlements as boolean flags, numeric limits, or usage quotas. Purchasable add-ons that extend plan capabilities.

### 🔧 Server-to-Server API

Programmatic admin access via `Authorization: Bearer kg_live_…` — mint licenses from your Stripe webhook, run nightly usage exports from cron, automate everything `/admin/*` can do. **Scope-based authorization** with fail-closed defaults (an API key with no scopes can do nothing). System-wide admin keys or per-product keys — same model as Stripe `sk_live_` and GitHub PATs.

### 📈 Admin Dashboard

Products, plans, licenses, customers, API keys, webhooks, analytics, audit logs, team management, email templates, and brand customization — all from one interface. Search, filter, and export (CSV/JSON).

### 🛡️ Security

Email OTP login with constant-time hash verification, role-based access checked per-request from database, brute-force protection, rate limiting, HMAC-signed webhooks, SameSite cookies, HSTS, and startup validation that rejects weak secrets. License verify endpoints collapse all "license-knowable" failures to a single 404 so `license_key` enumeration is closed off. Idempotency-Key middleware prevents double-execution on retried writes.

### 🌍 Self-Hosted

A Go server + PostgreSQL + (optional) S3-compatible storage for release artifacts. No Redis, no microservices. Auto-migration on startup. Setup wizard for first run. Custom branding, email templates, and i18n (English/Chinese built-in).

<br />

## Quick Start

### Docker (recommended)

```bash
# 1. Download
curl -O https://raw.githubusercontent.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/main/docker-compose.yml
curl -O https://raw.githubusercontent.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/main/.env.example
cp .env.example .env

# 2. Set your secrets
# Edit .env: set JWT_SECRET and LICENSE_SIGNING_KEY (openssl rand -hex 32).
# For production also set SECRET_ENCRYPTION_KEY, REFERRAL_HASH_SALT and
# RELEASE_KEY_ENCRYPTION_KEY — see docs/DEPLOYMENT.md for every variable.

# 3. Run
docker compose up -d
```

### From source

```bash
git clone https://github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing.git
cd hitechcloud_keygate_licensing && cp .env.example .env
make build && ./bin/hitechcloud_keygate_licensing
```

Open **http://localhost:9000** — the setup wizard guides you from there.

> 📖 **Operations:** environment reference, upgrades and rollback procedure in [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md).

> 📖 Full docs, deployment guides, and SDK examples at **[hitechcloud.vn/docs](https://hitechcloud.vn/docs)**

<br />

## Documentation

Project documentation lives in [`docs/`](docs):

- [`ARCHITECTURE.md`](docs/ARCHITECTURE.md) — module layout, request flow, surface routing, config-in-DB
- [`LICENSE-COMPLIANCE.md`](docs/LICENSE-COMPLIANCE.md) — AGPL v3 §7(b) attribution obligations
- [`API.md`](docs/API.md) — endpoint map, auth models, error envelope, idempotency
- [`LICENSE-ENGINE.md`](docs/LICENSE-ENGINE.md) — license lifecycle, seats, floating, key format
- [`ENTITLEMENTS.md`](docs/ENTITLEMENTS.md) — entitlement checks, quotas, feature flags
- [`COMMERCE.md`](docs/COMMERCE.md) — orders, invoices, coupons, taxes, refunds, metrics
- [`SUBSCRIPTIONS.md`](docs/SUBSCRIPTIONS.md) — Stripe lifecycle, proration, renewals
- [`MARKETPLACE.md`](docs/MARKETPLACE.md) — catalog, reviews, release feeds
- [`RESELLER.md`](docs/RESELLER.md) — resellers, affiliates, commissions
- [`SECURITY.md`](docs/SECURITY.md) — threat model, auth, secrets, retention
- [`DATABASE.md`](docs/DATABASE.md) — schema families, money rule, migrations
- [`MULTI-TENANCY.md`](docs/MULTI-TENANCY.md) — single-tenant status + multi-org design (not implemented)
- [`CONTRIBUTING.md`](../CONTRIBUTING.md) — dev setup, test discipline, conventions

<br />

## Compared to Alternatives

| | **HiTechCloud** | Keygen | Cryptlex | LicenseSpring |
|:---|:---:|:---:|:---:|:---:|
| Open source | **✅ AGPL v3** | Partial | ❌ | ❌ |
| Self-hosted | **✅** | ✅ | ❌ | ❌ |
| Price | **Free** | From $99/mo | From $249/mo | From $50/mo |
| Floating licenses | ✅ | ✅ | ✅ | ✅ |
| Usage metering | **✅** | ❌ | ❌ | ❌ |
| Built-in payments | **✅** | ❌ | ❌ | ❌ |
| Auto-update distribution | **✅ Sparkle / Velopack / Tauri** | ✅ Paid add-on | ❌ | ❌ |
| Customer portal | ✅ | ❌ | ✅ | ✅ |
| Admin dashboard | ✅ | ✅ | ✅ | ✅ |
| Webhook system | ✅ | ✅ | ✅ | ✅ |
| Audit trail | ✅ | ✅ | ❌ | ❌ |
| Idempotency-Key | **✅** | ❌ | ❌ | ❌ |
| i18n | ✅ | ❌ | ❌ | ❌ |

<br />

## Community

- **[Discussions](https://github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/discussions)** — Questions, ideas, show & tell
- **[Issues](https://github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/issues)** — Bug reports and feature requests

## Sponsors


<!-- sponsors:start -->
_Be the first._
<!-- sponsors:end -->

## Contributing

All contributions welcome — bugs, features, docs, translations. Check [open issues](https://github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/issues) or start a [discussion](https://github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/discussions), then submit a PR.

## Commercial License

Using HiTechCloud Software License & Commerce Platform is free, commercially included. Run it for your own business, license the software you sell with it, or serve your own customers from it, and you owe nothing.

The AGPL asks two things in return: the **"Powered by Keygate"** attribution line stays visible in the interface (see [NOTICE](NOTICE)), and if you modify the software and let others use it, your changes are published. A commercial license lifts both, which is what white labelling or reselling it as a closed service needs.

Contact [hello@hitechcloud.vn](mailto:hello@hitechcloud.vn).

## License

[AGPL v3 License](LICENSE) with additional terms per [Section 7(b)](https://www.gnu.org/licenses/agpl-3.0.en.html#section7) — Copyright © 2026 [HiTechCloud Viet Nam by Pho Tue SoftWare Solutions JSC](https://hitechcloud.vn)

You are free to fork, modify, and self-host this software under the AGPL v3. The **"Powered by Keygate"** attribution in the UI must be preserved (see [NOTICE](NOTICE)).

## Star History

<a href="https://star-history.com/#hitechcloud-vietnam/hitechcloud_keygate_licensing&Date">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/svg?repos=hitechcloud-vietnam/hitechcloud_keygate_licensing&type=Date&theme=dark" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/svg?repos=hitechcloud-vietnam/hitechcloud_keygate_licensing&type=Date" />
   <img alt="Star History Chart" src="https://api.star-history.com/svg?repos=hitechcloud-vietnam/hitechcloud_keygate_licensing&type=Date" width="600" />
 </picture>
</a>

---

<div align="center">
<sub>If HiTechCloud Software License & Commerce Platform helps your business, consider giving it a ⭐</sub>
</div>
