-- Product catalog fields (plan §223): the marketing half of a product
-- row — description, short description, logo, gallery images, and the
-- documentation / website / repository links plus the vendor name.
--
-- All nullable and left unfilled: rows written before this migration
-- read as empty values (bun scans NULL into the zero value) and no
-- backfill is needed. The URLs are validated on write by the admin
-- API and nothing here fetches them. images is a Postgres text array,
-- the display-ordered gallery, and stays NULL when never set.
ALTER TABLE products
    ADD COLUMN IF NOT EXISTS description       TEXT,
    ADD COLUMN IF NOT EXISTS short_description TEXT,
    ADD COLUMN IF NOT EXISTS logo_url          TEXT,
    ADD COLUMN IF NOT EXISTS images            TEXT[],
    ADD COLUMN IF NOT EXISTS documentation_url TEXT,
    ADD COLUMN IF NOT EXISTS website_url       TEXT,
    ADD COLUMN IF NOT EXISTS repository_url    TEXT,
    ADD COLUMN IF NOT EXISTS vendor            TEXT;
