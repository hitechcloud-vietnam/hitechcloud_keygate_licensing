-- Reverse: resellers. Tables drop in dependency order (the join tables
-- first — their foreign keys point at resellers/users/licenses), and
-- dropping a table drops its indexes, so the explicit index drops below
-- are only there to stay quiet on a database that never had them. The
-- resellers, licenses and users rows themselves are untouched.
DROP INDEX IF EXISTS idx_reseller_customers_customer;
DROP INDEX IF EXISTS idx_reseller_licenses_license;
DROP INDEX IF EXISTS idx_resellers_status;
DROP TABLE IF EXISTS reseller_customers;
DROP TABLE IF EXISTS reseller_licenses;
DROP TABLE IF EXISTS resellers;
