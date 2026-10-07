DROP INDEX IF EXISTS idx_plans_checkout_id;
ALTER TABLE plans DROP COLUMN IF EXISTS checkout_id;
