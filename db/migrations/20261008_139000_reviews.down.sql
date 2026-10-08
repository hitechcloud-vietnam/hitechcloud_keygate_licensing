-- Reverse: product_reviews. Dropping the table drops its indexes with
-- it, so the explicit index drops below only keep the migration quiet
-- on a database that never had them. The products and customers the
-- reviews spoke about are untouched — a down migration unwrites
-- schema, not content history.
DROP INDEX IF EXISTS idx_product_reviews_status_created;
DROP INDEX IF EXISTS idx_product_reviews_product_status_created;
DROP INDEX IF EXISTS idx_product_reviews_product_email;
DROP TABLE IF EXISTS product_reviews;
