# Marketplace

The public storefront: product discovery, product pages, categories, reviews
and release downloads. Backend: `internal/handler/marketplace_public.go`,
`review_public.go`, `category_admin.go`, `internal/store/marketplace.go`,
`reviews.go`, `categories.go`. UI: `web/src/pages/marketplace*.tsx`.

## Catalog

- **Products** carry catalog fields (plan §223): `description`,
  `short_description`, `logo_url`, `images[]`, `documentation_url`,
  `website_url`, `repository_url`, `vendor` — all URLs validated http(s) on
  write and never fetched server-side.
- **Categories** (`categories` + `product_categories` join): admin CRUD at
  `/api/v1/admin/categories`; product tagging via
  `PUT /api/v1/admin/products/:id/categories` (`category_ids`).
  Deleting a category detaches products (cascade join) — never blocks.
- **Plans** are exposed as public plan cards (name/slug/license type/
  interval); prices stay Stripe-side or in `plan_prices` — the marketplace
  shows what the checkout would charge only through the quote endpoint.

## Public API (surface `verify`, own rate-limit bucket)

| Endpoint | Notes |
|---|---|
| `GET /api/v1/marketplace/categories` | Flat list. |
| `GET /api/v1/marketplace/products` | `?search&category&sort&order&limit&offset`; search = name+slug ILIKE; sort whitelist `name\|newest`. |
| `GET /api/v1/marketplace/products/:slug` | Detail: catalog fields, categories, active plans, latest 20 **published** releases with artifacts (`platform`, `filename`, `file_size`, `content_type`, `sha256` only — never `file_key`, signatures or signing-key ids). |
| `GET /api/v1/marketplace/products/:slug/reviews` | Approved reviews + rating aggregate. |
| `GET /api/v1/marketplace/products/:slug/related` | Same-category products (≤8, excludes self). |

No-leak guarantees are pinned by tests in `marketplace_public_test.go`:
Stripe ids, entitlement internals and artifact credentials never appear in
public payloads.

## Reviews & ratings

- One review per `(product, customer_email)` — uniqueness folds case.
  Statuses: `pending → approved | rejected` (`CanTransitionReview`;
  self-transitions refused → `REVIEW_TRANSITION_INVALID` 409).
- Rating 1–5 (`model.ValidReviewRating`); title ≤ 120, body ≤ 4000 chars.
- Aggregate is exact: average in bps via round-half-up
  (`model.RatingAggregateFromSum`); the web `StarRating` renders bps → stars.
- Customer writes: `POST|PATCH|DELETE /api/v1/portal/products/:id/reviews`
  (session email identity; cross-user access answers quiet 404). A second
  review for the same product answers `DUPLICATE` 409 — the UI flips to edit
  mode. Admin moderation: `/api/v1/admin/reviews*` (approve/reject/reply/
  delete; admin reply is a separate field, cleared by `""`).
- "Verified purchase" is a documented future flag — not enforced today.

## Releases & downloads

Release bundles (`releases` + `release_artifacts`, 20260515 model) mirror
GitHub Releases: one release, many platform artifacts
(`platform`/`architecture`, checksum, optional ed25519 signature). Public
auto-update feeds per product:

- Sparkle: `GET /api/v1/releases/:product_slug/feed.xml`
- Velopack: `GET /api/v1/releases/:product_slug/feed.json` + `/velopack/*`
- Tauri: `GET /api/v1/releases/:product_slug/upgrade.json`

Feeds are public by design — trust comes from the artifact's ed25519
signature, not URL secrecy. Products with `feed_license_required` gate the
feed on a license key (the updater sends it) so a maintenance period cannot
be bypassed via the public feed; enforcement waits out cached feeds
(`FeedPublicMaxAge` + presigned-URL TTL bound).

Download entitlement for portal users: usable licenses +
`CapReleases` product type + `updates_until` cutoff + published releases
(`GET /api/v1/portal/downloads`).

## Product page data

`plans_public` companion endpoint (`GET /api/v1/products/:product_slug/plans`)
serves the anonymous pricing table; the marketplace product detail embeds the
same plan card shape for its Buy buttons (`/checkout/:checkout_id`).
