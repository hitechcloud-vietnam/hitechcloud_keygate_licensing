-- Marketplace catalog: product categories and the product↔category
-- links (plan §30). Categories are admin-managed facets of discovery
-- for the public catalog; products gain no new columns here.
CREATE TABLE IF NOT EXISTS categories (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    -- The public filter's handle (?category=<slug>), stored canonical
    -- (see model.NormalizeCategorySlug). Unique: a second spelling of
    -- one handle would make the filter answer for two categories.
    slug        TEXT NOT NULL UNIQUE,
    description TEXT NOT NULL DEFAULT '',
    -- Position orders the catalog. Plain ordering data: two
    -- categories may share one, and the listing breaks ties by name,
    -- then id.
    position    INT NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_categories_position ON categories (position, name, id);

-- Join table: one row per (product, category) assignment. The
-- composite PRIMARY KEY is the unique pair.
--
-- Both sides cascade on delete: removing a category detaches it from
-- its products, removing a product drops its links. The join rows
-- never outlive either side and never block a deletion — deleting a
-- category touches nothing but its own row and these links (products,
-- plans, licences and releases are untouched).
CREATE TABLE IF NOT EXISTS product_categories (
    product_id  TEXT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    category_id TEXT NOT NULL REFERENCES categories(id) ON DELETE CASCADE,
    PRIMARY KEY (product_id, category_id)
);
-- The category-side lookup ("products in this category") gets nothing
-- from the primary key's (product_id, …) prefix.
CREATE INDEX IF NOT EXISTS idx_product_categories_category ON product_categories (category_id);
