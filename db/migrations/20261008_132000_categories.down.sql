-- Reverse: categories. The join table goes first (its foreign key
-- points at categories); product rows are untouched. Dropping a table
-- drops its indexes, and the explicit drops before that stay quiet on
-- a database that never had them.
DROP INDEX IF EXISTS idx_product_categories_category;
DROP INDEX IF EXISTS idx_categories_position;
DROP TABLE IF EXISTS product_categories;
DROP TABLE IF EXISTS categories;
