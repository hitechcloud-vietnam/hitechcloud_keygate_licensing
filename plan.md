# HiTechCloud Software License & Commerce Platform
## MASTER SPECIFICATION — Keygate Extension Program

> **Base platform:** `tabloy/keygate`
>
> **Target:** Build a production-grade software licensing, subscription, commerce, marketplace and entitlement platform for HiTechCloud.
>
> **Primary languages:** English + Vietnamese
>
> **Development principle:** Extend the existing Keygate codebase in-place. Preserve the existing repository structure, conventions, modules, migrations and working functionality unless a change is demonstrably necessary.

---

# 1. PROJECT OBJECTIVE

Transform the existing Keygate platform into: **HiTechCloud Software License & Commerce Platform**

The platform must support:

- Software licensing, SaaS licensing, API licensing, digital products
- License key management, activation/deactivation
- Device/node/user binding, seat management
- Floating/concurrent licensing
- Trial, perpetual, subscription, usage-based, enterprise, OEM and reseller licensing
- Entitlements, features, quotas, usage metering
- Product/version/update entitlement
- Marketplace/storefront, cart, checkout, orders, payments, invoices, coupons, discounts, refunds, taxes
- Subscription lifecycle
- Customer portal
- Vendor/reseller/affiliate management
- Developer API, webhooks, audit logs
- Multi-tenant architecture, RBAC, admin console
- Customer self-service
- License validation API, offline activation
- Download/update center

The final system must be suitable for real commercial deployment.

---

# 2. BASE REPOSITORY

Upstream repository: https://github.com/tabloy/keygate

Do NOT create a completely unrelated application. The implementation must be based on the existing Keygate codebase.

Before modifying anything:

1. Inspect the complete repository.
2. Identify the framework and runtime.
3. Identify backend architecture.
4. Identify frontend architecture.
5. Identify database and migrations.
6. Identify authentication.
7. Identify authorization/RBAC.
8. Identify existing license functionality.
9. Identify existing subscription functionality.
10. Identify existing API.
11. Identify existing UI components.
12. Identify test infrastructure.
13. Identify Docker/dev environment.
14. Identify CI/CD.
15. Read all license files.
16. Read dependency licenses where relevant.
17. Document architectural constraints.

Do not assume the technology stack before inspecting the repository.

---

# 3. CRITICAL DEVELOPMENT RULES

## 3.1 Preserve existing project

Do NOT:

- Rewrite the entire project unnecessarily.
- Replace working modules without evidence.
- Create a second application beside Keygate.
- Create duplicate frontend/backend projects.
- Create unnecessary folders.
- Move the repository structure unnecessarily.
- Break existing APIs without migration.
- Delete existing migrations.
- Delete existing features merely because a redesign is planned.

Prefer incremental extension.

## 3.2 Upstream compatibility

Keep the original Keygate core as close to upstream as reasonably possible.

```text
Keygate Core
    │
    ├── License Engine
    ├── Subscription
    ├── Entitlement
    ├── Authentication
    └── API
            │
            ▼
HiTechCloud Extensions
    │
    ├── Product Catalog
    ├── Marketplace
    ├── Commerce
    ├── Payment
    ├── Invoice
    ├── Tax
    ├── Reseller
    ├── Affiliate
    ├── Customer Portal
    ├── Update Center
    └── HiTechCloud UI/UX
```

The extension architecture should make future upstream synchronization possible.

---

# 4. LICENSE COMPLIANCE — MANDATORY

Before substantial implementation, audit: repository LICENSE, copyright notices, additional license files, dependency licenses (frontend and backend), assets, icons, fonts, templates, third-party UI components.

Determine:

- Whether commercial use is permitted.
- Whether SaaS use is permitted.
- Whether self-hosted commercial use is permitted.
- Whether modification is permitted.
- Whether redistribution is permitted.
- Whether rebranding is permitted.
- Attribution requirements.
- NOTICE requirements.
- Copyleft obligations.
- Whether proprietary HiTechCloud extensions can remain proprietary.

Create: `docs/LICENSE-COMPLIANCE.md`

Do not remove upstream attribution where legally required.

---

# 5. PRODUCT VISION

```text
Product → Product Version → Plan → Price → Entitlements → Cart → Checkout
→ Order → Payment → Subscription / Purchase → License → Activation
→ Device / User / Node / Seat → Entitlement Enforcement → Usage Metering
→ Renewal / Upgrade / Downgrade / Cancellation
```

---

# 6. CORE DOMAIN MODEL

```text
Organization
 ├── Users
 ├── Roles
 ├── Customers
 ├── Products
 │    ├── Versions
 │    ├── Releases
 │    ├── Plans
 │    │    ├── Prices
 │    │    ├── Features
 │    │    ├── Entitlements
 │    │    └── Add-ons
 │    └── Downloads
 │
 ├── Orders
 ├── Payments
 ├── Invoices
 ├── Subscriptions
 │    └── Licenses
 │         ├── Activations
 │         ├── Devices
 │         ├── Users
 │         ├── Seats
 │         └── Usage
 │
 └── Audit Logs
```

---

# 7. MULTI-TENANCY

The platform must support multiple organizations/tenants.

Tenant isolation must apply to: users, customers, products, plans, prices, licenses, orders, payments, invoices, subscriptions, devices, activations, API credentials, webhooks, audit logs, files/downloads, reports.

Never allow cross-tenant access. Every API endpoint must enforce tenant scope.

---

# 8. RBAC

Roles: Owner, Super Admin, Admin, Finance, Product Manager, License Manager, Support, Developer, Reseller, Affiliate, Customer, Viewer.

RBAC must support granular permissions. Examples:

```text
products.read / products.create / products.update / products.delete
plans.read / plans.create / plans.update / plans.delete
licenses.read / licenses.create / licenses.activate / licenses.deactivate / licenses.revoke
orders.read / orders.create / orders.refund
payments.read / payments.manage
customers.read / customers.manage
reports.read
audit.read
settings.manage
```

---

# 9. PRODUCT MANAGEMENT

Product types: Software, SaaS, API, Plugin, Extension, Digital service, Digital download, Infrastructure software, Cloud service, AI service, Enterprise product.

Product fields: Name, Slug, SKU, Product code, Description, Short description, Logo, Images, Category, Vendor, Status, Visibility, Tax category, Support policy, Documentation URL, Website URL, Repository URL, License model, Version, Release channel.

Statuses: Draft, Active, Archived, Deprecated, Hidden.

---

# 10. PRODUCT VERSIONS

Support: Major/Minor/Patch version, Build number, Release date, Release channel, Changelog, Download artifacts, Checksums, Signature, Minimum OS, Supported OS, Minimum license version, Update entitlement.

Release channels: Stable, Beta, RC, Nightly, LTS.

---

# 11. PLANS

The plan engine must NOT hardcode product-specific limits.

```text
Product → Plan → Entitlements
```

Recommended default plans: FREE, TRIAL, STARTER, PROFESSIONAL, BUSINESS, ENTERPRISE, LIFETIME, FLOATING, USAGE, OEM, RESELLER, CUSTOM.

---

# 12. PLAN TYPES

- **Free** — Price = 0, duration unlimited.
- **Trial** — e.g. 7 / 14 / 30 days. May automatically convert into a paid plan.
- **Subscription** — Billing: Monthly, Quarterly, Yearly, 2 Years, 3 Years.
- **Perpetual** — One-time purchase. Optional: updates = 12 months, support = 12 months. After update entitlement expires, customer may renew updates/support.
- **Lifetime** — Lifetime license.
- **Floating** — License can move between machines/users.
- **Concurrent** — Enforce simultaneous usage.
- **Usage** — Meter by API requests, compute hours, storage, bandwidth, tokens, jobs, credits, seats, other custom meters.
- **OEM** — OEM customer, OEM product, white-label, custom licensing, contract-based pricing.
- **Reseller** — Wholesale price, retail price, discount, customer allocation, quota, sub-resellers.

---

# 13. ENTITLEMENT ENGINE

Entitlements are the heart of the platform.

Numeric examples:

```text
max_users = 10
max_devices = 5
max_servers = 3
max_domains = 100
max_websites = 500
max_projects = 20
max_api_requests = 100000
storage_gb = 500
bandwidth_gb = 5000
```

Boolean features: backup, waf, api_access, priority_support, sso, saml, scim, white_label.

Entitlement value types: Boolean, Integer, Decimal, String, Enum, JSON, Unlimited.

---

# 14. ADD-ONS

Plans must support optional add-ons. Examples: +10 Servers, +100 Domains, +10 Users, +1 TB Storage, +1 TB Bandwidth, Advanced WAF, Priority Support, Premium Backup, Dedicated IP, Enterprise SSO.

Add-ons may be one-time, recurring or usage-based.

---

# 15. LICENSE ENGINE

Support: license key generation, validation, activation, deactivation, revocation, suspension, renewal, expiration, grace period, upgrade, downgrade, transfer, reissue, regeneration.

License statuses: Pending, Active, Suspended, Expired, Revoked, Cancelled, Deactivated.

---

# 16. LICENSE MODELS

Node Locked, Device Locked, User Locked, Account Locked, Domain Locked, IP Locked, Floating, Concurrent, Perpetual, Subscription, Trial, Usage Based, Seat Based, Hybrid.

A license may combine multiple restrictions. Example: Professional — 10 users, 5 devices, 3 servers, 100 domains, 1 year.

---

# 17. ACTIVATION

```text
POST /v1/licenses/activate
POST /v1/licenses/deactivate
POST /v1/licenses/validate
POST /v1/licenses/refresh
POST /v1/licenses/heartbeat
```

Activation metadata: License ID, Device ID, Machine ID, Hostname, IP, OS, Architecture, Application version, Client version, User, Timestamp, Location metadata where legally appropriate.

Do not collect unnecessary personal data.

---

# 18. DEVICE MANAGEMENT

Device fields: Device ID, Machine fingerprint, Hostname, OS, Architecture, Application version, Last seen, First seen, Status, Activation state.

Statuses: Active, Inactive, Revoked, Blocked.

Customer must be able to deactivate devices from the portal when permitted.

---

# 19. SEATS

Named seats, Floating seats, Concurrent seats, User seats, Device seats.

Example — Enterprise: 100 named users, 20 concurrent users, 50 devices.

---

# 20. OFFLINE ACTIVATION

```text
Client → Generate activation request → Offline file → Admin/customer portal
→ Sign activation response → Client imports license
```

Activation responses must be cryptographically signed. Never expose private signing keys to clients.

---

# 21. CRYPTOGRAPHY

License tokens should use secure modern cryptography. Support: key rotation, public/private key separation, signature verification, token expiration, replay protection where appropriate, key versioning.

Private signing keys must never be stored in frontend code. Prefer a dedicated secret/key management mechanism in production.

---

# 22. SUBSCRIPTIONS

Statuses: Trialing, Active, Past Due, Paused, Cancelled, Expired, Incomplete, Incomplete Expired.

Support: Trial, Renewal, Grace period, Upgrade, Downgrade, Proration, Cancellation, Scheduled cancellation, Reactivation.

---

# 23. COMMERCE

Build: Catalog, Cart, Checkout, Order, Payment, Invoice, Refund, Coupon, Tax, Subscription.

Order statuses: Pending, Processing, Paid, Failed, Cancelled, Refunded, Partially Refunded.

---

# 24. CHECKOUT

Support: guest checkout where appropriate, account checkout, customer information, billing address, tax information, coupon, discount, payment method, invoice, terms acceptance. Checkout must be mobile responsive.

---

# 25. PAYMENT ARCHITECTURE

Do NOT hardcode one payment provider. Create a provider abstraction:

```text
PaymentProvider
 ├── createPayment()
 ├── capturePayment()
 ├── refundPayment()
 ├── voidPayment()
 ├── verifyWebhook()
 └── getPaymentStatus()
```

Possible providers: Stripe, PayPal, Adyen, Paddle, Lemon Squeezy, Bank Transfer, Manual Invoice.

Provider availability must depend on actual integration capability.

---

# 26. INVOICES

Fields: Invoice number, Customer, Organization, Billing address, Tax ID, Items, Quantity, Unit price, Discount, Tax, Total, Currency, Payment status, Due date, Paid date.

States: Draft, Open, Paid, Void, Uncollectible, Refunded.

---

# 27. COUPONS

Percentage discount, Fixed amount, Product restriction, Plan restriction, Customer restriction, First purchase, Renewal, Maximum redemptions, Start date, End date, Minimum order amount.

---

# 28. TAX

Support: tax-inclusive pricing, tax-exclusive pricing, customer tax ID, country, region, tax rules, exempt customers. Do not hardcode tax rules into product code.

---

# 29. CUSTOMER PORTAL

Sections: Dashboard, Products, Licenses, Activations, Devices, Subscriptions, Orders, Invoices, Downloads, API Keys, Webhooks, Profile, Security, Support.

Customer should be able to: view licenses, activate/deactivate devices, download software, view invoices, manage subscription, update payment method where supported, generate API keys, view usage, view entitlements.

---

# 30. MARKETPLACE

Product discovery, Categories, Search, Filters, Product detail, Pricing, Reviews if implemented, Vendor, Documentation, Changelog, Versions, Downloads, Related products, Add-ons.

Architecture must allow future third-party vendors.

---

# 31. RESELLER

```text
Reseller
 ├── Customers
 ├── Products
 ├── Plans
 ├── Pricing
 ├── Licenses
 ├── Orders
 ├── Quotas
 ├── Commissions
 └── Reports
```

Support: wholesale pricing, retail pricing, customer allocation, license allocation, reseller API, white-label portal, sub-reseller, commission.

---

# 32. AFFILIATE

Affiliate account, Referral code, Referral link, Attribution, Commission, Conversion, Payout status, Fraud controls.

Commission models: Percentage, Fixed, Recurring, One-time.

---

# 33. DEVELOPER API

API must be versioned (`/v1`, `/v2`).

Categories: Authentication, Products, Plans, Prices, Customers, Orders, Payments, Subscriptions, Licenses, Activations, Devices, Entitlements, Usage, Invoices, Webhooks, Downloads.

Use consistent: HTTP status codes, error format, pagination, filtering, sorting, idempotency, rate limiting.

---

# 34. API KEY MANAGEMENT

Publishable keys where applicable, Secret keys, Restricted keys, Expiration, Rotation, Revocation, Last-used timestamp, IP restrictions, Scope/permissions.

Never display a secret key again after creation.

---

# 35. WEBHOOKS

Events:

```text
product.created, product.updated
order.created, order.paid, order.failed, order.refunded
payment.succeeded, payment.failed
subscription.created, subscription.updated, subscription.cancelled, subscription.renewed
license.created, license.activated, license.deactivated, license.expired, license.revoked
device.activated, device.deactivated
invoice.created, invoice.paid, invoice.refunded
usage.threshold_reached
```

Webhook system must support: signing, retry, backoff, delivery logs, replay, disable/enable, secret rotation.

---

# 36. IDEMPOTENCY

Payment, order, subscription and license mutation endpoints must support idempotency.

```text
Idempotency-Key: <unique-key>
```

Never create duplicate orders, payments, licenses, activations or refunds because of retry requests.

---

# 37. AUDIT LOG

Audit all security-sensitive operations: login, logout, password change, role change, product/plan/price change, license creation/activation/deactivation/revocation, subscription change, payment change, refund, API key creation/revocation, webhook changes, admin settings.

Audit record: actor, tenant, action, resource, resource_id, before, after, ip, user_agent, timestamp, request_id.

---

# 38. SECURITY

Implement: CSRF protection where applicable, CORS policy, rate limiting, brute-force protection, secure cookies, secure sessions, password hashing, MFA-ready architecture, RBAC, API scopes, secret protection, input validation, output encoding, SQL injection protection, XSS protection, SSRF protection, webhook signature validation, replay protection where appropriate, audit logging.

Never log: passwords, API secrets, private signing keys, full payment credentials, sensitive tokens.

---

# 39. UI/UX DIRECTION

The interface should feel like a premium modern SaaS platform. Inspiration: Stripe, GitHub, Lemon Squeezy, Keygen, modern cloud control panels, HiTechCloud existing visual identity. Do NOT clone another company's interface.

Use: clean spacing, strong typography, clear information hierarchy, professional tables, useful filters, search, command/action menus, contextual actions, responsive layouts, accessible components, dark mode if existing architecture supports it, light mode, consistent empty states, skeleton loading, error states, confirmation dialogs, toast notifications.

Avoid: excessive gradients, fake AI aesthetics, excessive glassmorphism, unnecessary animations, dense unreadable dashboards.

---

# 40. PUBLIC WEBSITE

Multilingual. Priority: English, Vietnamese. Architecture must allow adding Japanese, Korean, Chinese later.

Pages: Home, Products, Marketplace, Pricing, Product Details, Features, Documentation, Changelog, Downloads, Enterprise, Resellers, Developers, API, About, Contact, Terms, Privacy, Refund Policy, License Agreement.

SEO: metadata, OpenGraph, sitemap, robots, canonical URLs, structured data, localized URLs where appropriate.

---

# 41. ADMIN CONSOLE

```text
Dashboard
Catalog: Products, Versions, Plans, Prices, Features, Entitlements, Add-ons
Licensing: Licenses, Activations, Devices, Seats, Usage
Commerce: Orders, Payments, Invoices, Coupons, Refunds, Taxes
Customers: Customers, Organizations, Users
Subscriptions
Marketplace
Resellers
Affiliates
Developers: API Keys, Webhooks, API Logs
Reports
Audit Logs
Settings
```

---

# 42. DASHBOARD

Metrics: Revenue, MRR, ARR, Active subscriptions, New customers, Active licenses, Activations, Renewals, Churn, Failed payments, Usage, Top products, Top plans, Top resellers. Allow date filtering.

---

# 43. REPORTING

Reports: Revenue, Sales, Subscriptions, Renewals, Churn, Licenses, Activations, Devices, Usage, Customers, Resellers, Affiliates, Refunds, Failed payments.

Export: CSV, JSON, PDF — only implement formats supported reliably by the project stack.

---

# 44. NOTIFICATION SYSTEM

Channels: Email, In-app, Webhook.

Events: Welcome, Email verification, Password reset, Order confirmation, Payment confirmation, Invoice, Trial ending, Subscription renewal, Payment failed, License activated, License expiring, License expired, License revoked, Device activated, Device limit reached, Usage threshold.

Notification templates must be editable.

---

# 45. EMAIL TEMPLATE SYSTEM

Support: English, Vietnamese, variables, preview, test send, versioning if practical.

Variables:

```text
{{customer.name}}  {{product.name}}  {{license.key}}  {{license.expires_at}}
{{order.number}}  {{invoice.number}}  {{subscription.next_billing_at}}
```

Never expose secret credentials in templates.

---

# 46. DOWNLOAD CENTER

Each product release can have: Artifact, Platform, Architecture, Version, Checksum, Signature, Release channel, Minimum version, License requirement.

Platforms: Linux, Windows, macOS, Docker, Kubernetes, Other. Architectures: amd64, arm64, armv7, other.

---

# 47. UPDATE ENTITLEMENT

Example: license purchased at v1, updates valid until 2027-01-01.

If entitlement expires:

- Existing licensed version remains usable according to license terms.
- New versions require valid update entitlement.
- Customer can renew update entitlement.

---

# 48. USAGE METERING

```text
Meter: API Requests, Storage, Bandwidth, CPU Hours, GPU Hours, AI Tokens, Jobs, Custom
```

Usage record: tenant, customer, license, meter, quantity, timestamp, metadata.

Support: aggregation, daily/monthly usage, thresholds, quotas, overages, usage export.

---

# 49. PLAN EXAMPLES

- **FREE** — $0, Devices 1, Users 1, Community support, limited API.
- **TRIAL** — 14 days, Professional features, $0.
- **STARTER** — Monthly/Yearly. Devices 2, Users 3, Domains 25, basic API, standard support.
- **PROFESSIONAL** — Monthly/Yearly. Devices 10, Users 20, Domains 250, advanced API, automation, priority support.
- **BUSINESS** — Monthly/Yearly. Devices 50, Users 100, Domains 1000, advanced security, SSO-ready, priority support.
- **ENTERPRISE** — Custom pricing. Unlimited/contract-defined, SSO/SAML, SCIM, advanced RBAC, audit, private deployment, SLA, dedicated support, custom contract, Invoice/PO, OEM, Reseller.
- **LIFETIME** — One-time, perpetual license, optional annual updates/support.
- **FLOATING** — Concurrent licenses, central license server, heartbeat, lease.
- **USAGE** — Pay-as-you-go, metered billing, overage support.
- **OEM** — Custom contract, white-label, embedded licensing, custom entitlements.
- **RESELLER** — Wholesale pricing, license allocation, customer management, reseller API, commission.

---

# 50. DATABASE DESIGN PRINCIPLES

Use normalized relational data where appropriate. Avoid putting the entire domain model into JSON; use JSON only for genuinely dynamic metadata.

Every major table: `id`, `created_at`, `updated_at`. Where appropriate: `deleted_at`, `created_by`, `updated_by`, `tenant_id`.

Use: foreign keys, unique constraints, indexes, check constraints where supported, proper cascading behavior, transaction boundaries. Never rely only on application-level uniqueness.

---

# 51. MONEY

Never use floating point for money. Use integer minor units (e.g. $19.99 => 1999). Store amount and currency. Use ISO currency codes: USD, VND, EUR, SGD, JPY, KRW, AUD.

---

# 52. ORDER CALCULATION

```text
Subtotal - Discount + Tax + Shipping (if applicable) = Total
```

For digital products normally: Subtotal - Discount + Tax = Total.

Store calculated totals at order time. Do not recalculate historical invoices using current prices.

---

# 53. CURRENCY

Products may define: base currency, supported currencies, currency-specific price. Do not silently change historical order currency.

---

# 54. API ERROR FORMAT

```json
{
  "error": {
    "code": "license_limit_reached",
    "message": "The license activation limit has been reached.",
    "request_id": "..."
  }
}
```

Do not leak stack traces in production.

---

# 55. PAGINATION

Prefer cursor pagination for large datasets. Support: limit, cursor, sort, filter. Where offset pagination is already established by Keygate, preserve it until a migration is justified.

---

# 56. OBSERVABILITY

Add: structured logs, request ID, correlation ID, metrics, health/readiness/liveness endpoints, error tracking integration point. Endpoints: `/health`, `/ready`. Do not expose secrets or internal infrastructure details.

---

# 57. BACKGROUND JOBS

Use the existing project job system where possible.

Jobs: subscription renewal, license expiration, trial expiration, usage aggregation, invoice generation, payment reconciliation, webhook retry, email delivery, cleanup, analytics. Jobs must be idempotent.

---

# 58. CACHING

Cache carefully: product catalog, public pricing, entitlements where safe, configuration, frequently accessed validation data.

Do not cache mutable authorization decisions longer than safe. License revocation must propagate quickly.

---

# 59. RATE LIMITING

Apply limits to: login, password reset, license validation, activation, API, webhooks, checkout, coupon validation, usage ingestion.

Use different limits for: anonymous, customer, API key, admin, internal services.

---

# 60. TESTING

Layers: unit, integration, API, database, authorization, license engine, payment, webhook, end-to-end.

Critical scenarios:

1. Create product / plan / entitlement / price.
2. Purchase plan; payment succeeds; license generated.
3. License activates; device limit enforced.
4. License expires; renewal succeeds / fails.
5. License revoked; revoked license cannot activate.
6. Upgrade works; downgrade works.
7. Refund works.
8. Webhook retry works.
9. Duplicate request is idempotent.
10. Cross-tenant access is blocked.

---

# 61. SECURITY TESTING

Test: broken access control, IDOR, tenant isolation, privilege escalation, SQL injection, XSS, CSRF, SSRF, authentication bypass, API key leakage, webhook spoofing, replay attacks, rate-limit bypass, license activation abuse, device fingerprint abuse, coupon abuse.

---

# 62. PERFORMANCE

The license validation endpoint is high priority. Target architecture: high request volume, low latency, horizontal scaling, stateless API nodes, centralized data store, cache where appropriate. Do not make license validation depend on unnecessary slow external services.

---

# 63. BACKWARD COMPATIBILITY

Before modifying existing Keygate APIs: identify existing consumers; preserve existing behavior where possible; add new versioned endpoints when breaking changes are required; add migration logic; add compatibility tests.

---

# 64. MIGRATIONS

Every database change must be represented by a migration. Never manually alter production schema without a corresponding migration. Migrations must be reproducible, ordered, reversible where practical, tested.

---

# 65. SEED DATA

Create development seed data for: products, plans, features, entitlements, prices, customers, users, licenses.

Example product: HiTechCloud Panel. Example plans: Free, Starter, Professional, Business, Enterprise, Lifetime.

Seed data must never contain real credentials.

---

# 66. LOCAL DEVELOPMENT

Preserve the repository's existing development workflow. Document: install, environment variables, database setup, migration, seed, run backend, run frontend, run tests, build. Update README only after verifying actual commands.

---

# 67. ENVIRONMENT CONFIGURATION

Document variables without committing secrets. Categories: APP, DATABASE, CACHE, QUEUE, MAIL, PAYMENT, LICENSE_SIGNING, STORAGE, WEBHOOK, OBSERVABILITY. Use `.env.example`.

Never commit: `.env`, private keys, API secrets, production credentials.

---

# 68. CI/CD

CI should validate: formatting, linting, type checking, unit tests, integration tests, build, migration validation, security checks. Do not add unnecessary CI complexity. Preserve existing pipeline conventions.

---

# 69. DOCUMENTATION

Create/update: README.md, docs/ARCHITECTURE.md, docs/LICENSE-COMPLIANCE.md, docs/API.md, docs/LICENSE-ENGINE.md, docs/ENTITLEMENTS.md, docs/COMMERCE.md, docs/SUBSCRIPTIONS.md, docs/MARKETPLACE.md, docs/RESELLER.md, docs/SECURITY.md, docs/DEPLOYMENT.md, docs/DATABASE.md, docs/CONTRIBUTING.md.

Only create files that fit the existing project structure.

---

# 70. ADMIN UX REQUIREMENTS

Every admin table should support where relevant: search, filter, sort, pagination, bulk actions, column selection, export, create, edit, view, delete/archive.

Destructive actions require confirmation. Show consequences clearly.

---

# 71. CUSTOMER UX REQUIREMENTS

Customers should never need to understand internal database IDs. Use human-readable license IDs, order numbers, invoice numbers, product names, plan names. Technical IDs may be exposed only where useful for API/debugging.

---

# 72. LICENSE KEY FORMAT

Do not use sequential predictable keys. Example: `HTC-XXXX-XXXX-XXXX-XXXX`. Generation must use secure randomness.

Keys should support: prefix, checksum if useful, key version, secret portion.

Never store plaintext secrets unnecessarily. Prefer secure hashes or encrypted values depending on validation architecture.

---

# 73. LICENSE VALIDATION RESPONSE

```json
{
  "valid": true,
  "license": {
    "id": "lic_xxx",
    "status": "active",
    "product": "HiTechCloud Panel",
    "plan": "Professional",
    "expires_at": "2027-10-07T00:00:00Z"
  },
  "entitlements": {
    "max_servers": 10,
    "max_domains": 500,
    "backup": true,
    "waf": true
  }
}
```

Never return sensitive internal data.

---

# 74. LICENSE SDK

Design a future SDK architecture for: Rust, Go, PHP, Node.js, Python, Java, .NET. Do not implement all SDKs immediately unless required. First define a stable API contract.

---

# 75. CLIENT HEARTBEAT

Floating/concurrent licenses may use heartbeat. Heartbeat should: renew lease, confirm device, confirm license validity, update last-seen, release stale sessions after timeout.

Design for network interruption. Use grace periods where appropriate.

---

# 76. GRACE PERIOD

Support product-level and plan-level grace periods.

```text
Subscription expired → Grace period: 3 days → License restricted → License expired
```

Grace behavior must be configurable.

---

# 77. UPGRADE/DOWNGRADE

Support: Starter → Professional, Professional → Business, Business → Enterprise.

Upgrade may be immediate. Downgrade may happen immediately or at next billing cycle. Entitlement reduction must be handled safely.

---

# 78. PRORATION

If the payment provider supports it, support: unused time credit, remaining period, new plan charge.

Never implement inaccurate financial calculations. Use integer minor units and deterministic rounding.

---

# 79. REFUNDS

Support: full refund, partial refund, payment-provider refund, manual refund.

Refund must update Order, Payment, Invoice, Subscription, License and Entitlements according to business rules.

---

# 80. LICENSE REVOCATION

Revocation must be authoritative.

Reasons: Fraud, Refund, Chargeback, Policy violation, Customer request, Security incident, Administrative action.

Store reason and actor.

---

# 81. FRAUD CONTROLS

Possible signals: excessive activations, excessive device changes, impossible activation patterns, coupon abuse, payment anomalies, reseller abuse.

Do not automatically ban customers solely from weak signals. Provide review workflows.

---

# 82. PRIVACY

Collect only required information. Support: data export, account deletion where legally applicable, retention policies, audit retention, privacy settings. Do not store unnecessary device fingerprint details.

---

# 83. INTERNATIONALIZATION

All user-facing text must be translatable. Never hardcode UI text into business logic.

```text
i18n
 ├── en
 └── vi
Future: ja, ko, zh
```

---

# 84. ACCESSIBILITY

Target: keyboard navigation, semantic HTML where applicable, labels, focus states, contrast, screen-reader-friendly forms, accessible dialogs, accessible tables. Follow WCAG principles appropriate to the stack.

---

# 85. RESPONSIVE DESIGN

Support desktop, laptop, tablet, mobile. Admin may prioritize desktop. Customer and public website must be fully mobile-friendly.

---

# 86. SEARCH

Global search should eventually cover: products, customers, orders, invoices, licenses, subscriptions, devices. Search must respect tenant and RBAC permissions.

---

# 87. COMMAND PALETTE

If compatible with the existing UI: search, create product, create license, find customer, find order, open settings. Shortcut: Cmd/Ctrl + K.

---

# 88. NOTIFICATION CENTER

Unread count, read/unread, priority, timestamp, deep link.

---

# 89. EMPTY STATES

Every empty table/page should explain what the resource is, why it is empty, and what the user can do next.

> No licenses yet.
> Licenses are created automatically after a successful purchase, or you can create one manually.
> **[Create License]**

---

# 90. ERROR UX

Errors must be actionable.

- Bad: *Something went wrong.*
- Better: *Payment could not be completed. The payment provider rejected the transaction. Please try another payment method. Request ID: req_xxx*

---

# 91. ADMIN AUDITABILITY

Every important mutation should show: Who, What, When, Where, Before, After.

---

# 92. DATA RETENTION

Document retention rules for: orders, invoices, payments, audit logs, license activations, usage, webhooks, notifications. Do not silently delete financial records.

---

# 93. ARCHITECTURE QUALITY RULE

Before adding a new module, determine whether the functionality already exists in Keygate. Prefer extending an existing abstraction over creating a duplicate abstraction.

---

# 94. CODE QUALITY

Follow the repository's existing style. Requirements: small focused functions, clear naming, strong validation, explicit errors, no unnecessary abstraction, no dead code, no commented-out legacy code, no duplicated business logic.

---

# 95. NO PREMATURE MICROSERVICES

Do not split into microservices merely because the platform is large. Start with the existing architecture and use clear domain boundaries. Extract services only when justified by scale, deployment independence, security isolation or operational requirements.

---

# 96. MULTI-AGENT DEVELOPMENT

Use multiple agents for parallel work, but maintain one source of truth.

Roles: Agent 1 Repository Auditor, 2 Architecture, 3 Database, 4 License Engine, 5 Subscription, 6 Commerce, 7 Marketplace, 8 API, 9 Security, 10 UI/UX, 11 Testing, 12 Documentation, 13 DevOps/CI.

Agents must NOT independently redesign the architecture. All agents follow this MASTER SPEC.

---

# 97. MULTI-AGENT RULES

Before changing code: read existing code, related modules, migrations, tests, documentation.

- Before creating a new abstraction: search the repository for an existing equivalent.
- Before changing an API: search for all consumers.
- Before changing a database model: search all references.

---

# 98. AGENT COORDINATION

Each agent must report: files inspected, files changed, database changes, API changes, breaking changes, tests added, tests executed, known issues, follow-up required. Agents should avoid overlapping edits.

---

# 99. IMPLEMENTATION PHASES

**PHASE 0 — AUDIT.** Deliver: repository map, architecture map, dependency map, database map, API map, UI map, license compliance, technical debt, risk register. No major feature implementation before Phase 0 is complete.

**PHASE 1 — FOUNDATION.** Tenant, Organization, RBAC, Product, Product version, Plan, Price, Feature, Entitlement, Add-on, Basic license model.

**PHASE 2 — LICENSE ENGINE.** License generation, validation, activation, deactivation, devices, seats, expiration, renewal, revocation, floating, concurrent, offline activation, signing, key rotation.

**PHASE 3 — SUBSCRIPTIONS.** Trial, subscription, renewal, cancellation, upgrade, downgrade, grace period, proration, billing states.

**PHASE 4 — COMMERCE.** Catalog, cart, checkout, order, payment abstraction, invoice, coupon, discount, tax, refund.

**PHASE 5 — CUSTOMER PORTAL.** Dashboard, licenses, activations, devices, subscriptions, orders, invoices, downloads, API keys, webhooks, usage.

**PHASE 6 — MARKETPLACE.** Public catalog, search, product pages, pricing, categories, vendors, releases, downloads.

**PHASE 7 — RESELLER/AFFILIATE.** Reseller accounts, wholesale pricing, customer allocation, license allocation, commission, affiliate tracking, reseller API.

**PHASE 8 — ENTERPRISE.** SSO, SAML, SCIM architecture, advanced RBAC, audit, private deployment support, SLA metadata, enterprise contracts, PO/invoice workflow.

**PHASE 9 — UI/UX.** Polish admin console, customer portal, marketplace, checkout, pricing, product pages, mobile responsiveness, accessibility, i18n.

**PHASE 10 — SECURITY/PERFORMANCE.** Security audit, tenant isolation audit, API audit, load testing, license validation performance testing, database indexing, cache review, rate-limit review.

**PHASE 11 — RELEASE.** All tests pass, migrations verified, backup/restore tested, security review complete, secrets verified, CI green, production build verified, documentation complete, rollback procedure documented.

---

# 100. DEFINITION OF DONE

A feature is NOT complete merely because code compiles. It is complete when:

- Backend implemented
- Database migration implemented
- Validation implemented
- Authorization implemented
- API implemented
- UI implemented where required
- Tests implemented
- Error handling implemented
- Audit implemented where relevant
- Documentation updated
- i18n implemented
- Security reviewed
- Existing functionality verified

---

# 101. GIT WORKFLOW

Use `main` as the stable branch. Feature branches: `feature/license-engine`, `feature/commerce`, `feature/subscriptions`, `feature/marketplace`, `feature/customer-portal`, `feature/reseller`, `feature/ui`.

Do not commit directly to `main` during active development unless explicitly required.

---

# 102. COMMIT CONVENTION

Prefer: `feat:`, `fix:`, `refactor:`, `perf:`, `security:`, `test:`, `docs:`, `chore:`. Example: `feat: add concurrent license activation`.

---

# 103. PULL REQUEST REQUIREMENTS

Every PR should describe: what changed, why, database changes, API changes, UI changes, security impact, tests, migration requirements, rollback considerations.

---

# 104. DO NOT DO

Never:

- Rewrite everything without audit.
- Delete existing functionality without replacement.
- Hardcode plan limits into code.
- Hardcode payment providers into business logic.
- Store money as float.
- Store production secrets in Git.
- Store private license signing keys in clients.
- Trust client-side license validation.
- Trust frontend authorization.
- Allow cross-tenant resource access.
- Log secrets.
- Create duplicate modules unnecessarily.
- Create unnecessary repositories or folders.
- Break upstream compatibility without documenting why.

---

# 105. FINAL TARGET ARCHITECTURE

```text
HiTechCloud Software License & Commerce Platform

├── Identity & Access
│   ├── Authentication
│   ├── Organizations
│   ├── Tenants
│   ├── Users
│   └── RBAC
│
├── Catalog
│   ├── Products
│   ├── Versions
│   ├── Releases
│   ├── Plans
│   ├── Prices
│   ├── Features
│   ├── Entitlements
│   └── Add-ons
│
├── License Engine
│   ├── Keys
│   ├── Validation
│   ├── Activation
│   ├── Devices
│   ├── Seats
│   ├── Floating
│   ├── Concurrent
│   ├── Offline
│   ├── Expiration
│   └── Revocation
│
├── Subscription
│   ├── Trial
│   ├── Renewal
│   ├── Upgrade
│   ├── Downgrade
│   ├── Grace Period
│   └── Cancellation
│
├── Commerce
│   ├── Cart
│   ├── Checkout
│   ├── Orders
│   ├── Payments
│   ├── Invoices
│   ├── Coupons
│   ├── Taxes
│   └── Refunds
│
├── Marketplace
│   ├── Storefront
│   ├── Vendors
│   ├── Categories
│   ├── Product Pages
│   └── Downloads
│
├── Reseller
│   ├── Wholesale
│   ├── Customers
│   ├── License Allocation
│   └── Commissions
│
├── Affiliate
│   ├── Referrals
│   ├── Attribution
│   └── Commissions
│
├── Customer Portal
│   ├── Dashboard
│   ├── Licenses
│   ├── Devices
│   ├── Subscriptions
│   ├── Orders
│   ├── Invoices
│   ├── Downloads
│   └── Usage
│
├── Developer Platform
│   ├── API
│   ├── API Keys
│   ├── Webhooks
│   ├── SDK Contract
│   └── Usage
│
├── Security
│   ├── Audit
│   ├── Rate Limits
│   ├── Key Management
│   └── Tenant Isolation
│
└── Operations
    ├── Jobs
    ├── Notifications
    ├── Reports
    ├── Metrics
    └── Health
```

---

# 106. FIRST TASK FOR CLAUDE CODE / MULTI-AGENT

Do NOT immediately start implementing all modules.

## TASK 0 — FULL REPOSITORY AUDIT

Required output:

1. Repository structure
2. Framework/runtime
3. Backend architecture
4. Frontend architecture
5. Database architecture
6. Authentication
7. Authorization/RBAC
8. Existing license system
9. Existing subscription system
10. Existing API
11. Existing UI components
12. Existing tests
13. Existing CI/CD
14. Existing Docker/deployment
15. Dependency inventory
16. License compliance analysis
17. Existing extension points
18. Conflicts with this specification
19. Recommended implementation order
20. Risk register

Do not make large code changes during the audit. After the audit, create `docs/KEYGATE-AUDIT.md`, then propose the smallest safe implementation sequence.

---

# 107. MASTER AGENT COMMAND

Use this as the instruction to the primary coding agent:

```text
You are the Lead Architect and Engineering Agent for the HiTechCloud
Software License & Commerce Platform.

The existing repository is based on tabloy/keygate.

Your first responsibility is NOT to rewrite the application.
Your first responsibility is to understand the existing system completely.

Follow the MASTER SPECIFICATION in this document.

Rules:

1. Preserve the existing repository structure.
2. Preserve existing working functionality.
3. Search before creating new abstractions.
4. Reuse existing Keygate architecture wherever possible.
5. Keep upstream compatibility in mind.
6. Never make destructive changes without explicit justification.
7. Never hardcode business limits that belong in entitlements.
8. Never trust frontend authorization.
9. Enforce tenant isolation server-side.
10. Never store money as floating point.
11. Never commit secrets.
12. Never expose private license-signing keys.
13. Make mutations idempotent where financial/license duplication is possible.
14. Add migrations for database changes.
15. Add tests for business-critical behavior.
16. Update documentation with architectural changes.
17. Implement English and Vietnamese localization architecture.
18. Keep UI modern, professional and consistent with HiTechCloud.
19. Do not create unnecessary folders or repositories.
20. Do not split into microservices unless there is a concrete technical reason.

Before implementing a major feature:

- inspect existing code
- inspect related database models
- inspect existing APIs
- inspect existing tests
- inspect existing UI
- identify reusable components
- document the implementation plan

Work in small, reviewable phases.

After every phase report:

- files changed
- migrations
- APIs
- UI
- tests
- security considerations
- breaking changes
- remaining work

Do not claim completion without running the relevant tests.
```

---

# 108. MULTI-AGENT MAX CAPACITY COMMAND

When the environment supports multiple coding agents, use parallel execution carefully:

```text
Run the project using maximum safe parallel agent capacity.

Do NOT allow agents to independently redesign the architecture.
Use one Lead Architect as the source of truth.
Parallelize only tasks with clear file/domain boundaries.

Recommended parallel agents:

A1 Repository Audit
A2 Database/Data Model
A3 License Engine
A4 Subscription
A5 Commerce
A6 API
A7 Customer Portal
A8 Marketplace
A9 Reseller/Affiliate
A10 Security
A11 UI/UX
A12 Testing
A13 Documentation
A14 DevOps/CI

Before parallel implementation:

1. Complete repository audit.
2. Establish architecture decision record.
3. Establish database model.
4. Establish API contracts.
5. Establish domain ownership.
6. Establish file ownership boundaries.

Agents must synchronize before changing shared core files.

If two agents need the same core file:
STOP parallel modification and let the Lead Architect coordinate the change.

Never sacrifice correctness for parallelism.
```

---

# 109. SUCCESS CRITERIA

The final platform should demonstrate this complete scenario, eventually end-to-end in automated tests:

```text
Admin creates Product
 → creates Professional Plan
 → adds Entitlements
 → creates Monthly + Yearly Prices
 → publishes Product
Customer visits Marketplace
 → selects Professional
 → Checkout → Payment → Order Paid
 → Subscription Created → License Created
 → Customer receives License
 → Customer activates Device
 → Entitlements become available
 → Application validates license through API
 → Usage is metered
 → Customer upgrades plan
 → Proration is calculated
 → License entitlements update
 → Subscription renews
 → Invoice generated → Payment succeeds
 → License remains active
 → Customer can download new product version
 → Update entitlement is checked
```

---

# 110. FINAL PRINCIPLE

The goal is NOT simply to add more features to Keygate. The goal is to evolve Keygate into a coherent commercial platform where:

```text
Product → Plan → Price → Entitlement → Commerce
→ Subscription / Purchase → License → Activation → Usage → Renewal
```

forms one consistent domain model.

The implementation must remain maintainable, secure, extensible, testable and commercially deployable.

**Build incrementally. Preserve the core. Avoid unnecessary rewrites.**
