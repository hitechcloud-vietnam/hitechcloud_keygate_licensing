-- Product reviews (Phase 6 leftovers): customer reviews with a
-- moderation queue, feeding the aggregate star rating the marketplace
-- listings and product page render. A review is catalog-adjacent
-- content only — it carries no price, no entitlement and no license
-- meaning, so approving, rejecting or deleting a review can never
-- change what a customer's license may do.
--
-- One review per (product, customer): customer_email is the account
-- handle the portal session provides, folded to lowercase on write
-- (model.NormalizeReviewEmail) and CHECKed folded here, so one address
-- cannot hold two rows under different spellings — the unique pair
-- below is only meaningful because of that fold.
--
-- Referential actions:
--   * product_id ON DELETE CASCADE — reviews are content hung on the
--     product. Deleting the product takes its reviews with it (the
--     aggregates disappear with their rows); nothing here blocks a
--     product deletion and nothing outlives it. The alternative —
--     RESTRICT — would make a review immortal and a product undeletable.
CREATE TABLE IF NOT EXISTS product_reviews (
    id             TEXT PRIMARY KEY,
    product_id     TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    -- Folded lowercase account address; the fold is enforced, not
    -- trusted (see the header note).
    customer_email TEXT NOT NULL
                   CONSTRAINT product_reviews_email_folded_check
                   CHECK (customer_email = lower(customer_email)),
    -- Display name beside the review. Nullable: the account may not
    -- carry one, and the storefront renders a review without it.
    customer_name  TEXT,
    -- Star rating. Closed range at the database level as well as in
    -- model.ValidReviewRating — an invented rating would poison the
    -- aggregate the listings render.
    rating         INT NOT NULL
                   CONSTRAINT product_reviews_rating_check
                   CHECK (rating BETWEEN 1 AND 5),
    -- Optional headline. NULL, not empty string, when absent.
    title          TEXT
                   CONSTRAINT product_reviews_title_check
                   CHECK (title IS NULL OR char_length(title) <= 120),
    -- The review text itself. Bounded so one post cannot be a novel.
    body           TEXT NOT NULL
                   CONSTRAINT product_reviews_body_check
                   CHECK (char_length(body) <= 4000),
    -- Moderation state, closed vocabulary (model.ReviewStatuses). The
    -- public storefront reads approved rows only; pending is the
    -- review queue and rejected rows are kept for the audit trail.
    status         TEXT NOT NULL DEFAULT 'pending'
                   CONSTRAINT product_reviews_status_check
                   CHECK (status IN ('pending', 'approved', 'rejected')),
    -- The vendor's public answer under the review. Nullable and
    -- bounded like the body; NULL when unanswered.
    admin_reply    TEXT
                   CONSTRAINT product_reviews_reply_check
                   CHECK (admin_reply IS NULL OR char_length(admin_reply) <= 4000),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Idempotency of authorship: AT MOST ONE review per (product,
-- customer). A retried submission hits this index and is answered with
-- the existing row (409) instead of a second review. This is the
-- constraint the store's unique-violation fold names
-- (store.ErrReviewAlreadyExists).
CREATE UNIQUE INDEX IF NOT EXISTS idx_product_reviews_product_email
    ON product_reviews (product_id, customer_email);

-- The public listing walk: "this product's approved reviews, newest
-- first" — the index leads with product_id and status (the approved
-- filter) and carries the ordering, with id as the tiebreaker so
-- OFFSET/LIMIT paging lines up across pages.
CREATE INDEX IF NOT EXISTS idx_product_reviews_product_status_created
    ON product_reviews (product_id, status, created_at DESC, id DESC);

-- The moderation queue: "pending reviews across the catalog, newest
-- first" (GET /admin/reviews?status=pending), ordered like the index
-- above for the same paging-stability reason.
CREATE INDEX IF NOT EXISTS idx_product_reviews_status_created
    ON product_reviews (status, created_at DESC, id DESC);
