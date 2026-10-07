-- Reverse: tax rates
DROP INDEX IF EXISTS idx_tax_rates_country_region;
DROP INDEX IF EXISTS idx_tax_rates_active;
DROP TABLE IF EXISTS tax_rates;
