-- Reverse: product catalog fields. What the storefront wrote into
-- them is dropped with the columns; the product's identity, gating,
-- pricing and download columns are untouched, and the product↔category
-- links (a different migration) are untouched too.
ALTER TABLE products
    DROP COLUMN IF EXISTS vendor,
    DROP COLUMN IF EXISTS repository_url,
    DROP COLUMN IF EXISTS website_url,
    DROP COLUMN IF EXISTS documentation_url,
    DROP COLUMN IF EXISTS images,
    DROP COLUMN IF EXISTS logo_url,
    DROP COLUMN IF EXISTS short_description,
    DROP COLUMN IF EXISTS description;
