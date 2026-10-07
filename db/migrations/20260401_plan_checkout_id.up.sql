ALTER TABLE plans ADD COLUMN IF NOT EXISTS checkout_id TEXT;

-- Generate short IDs for existing plans (8-char random hex)
UPDATE plans SET checkout_id = substr(md5(id || random()::text), 1, 8) WHERE checkout_id IS NULL OR checkout_id = '';

ALTER TABLE plans ALTER COLUMN checkout_id SET NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_plans_checkout_id ON plans (checkout_id);
