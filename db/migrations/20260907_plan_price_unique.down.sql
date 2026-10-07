DROP INDEX IF EXISTS idx_plans_stripe_price_unique;
DROP INDEX IF EXISTS idx_licenses_stripe_checkout_session;
ALTER TABLE licenses DROP COLUMN IF EXISTS stripe_checkout_session_id;
-- In-flight claims written by this version; older binaries do not
-- know these providers.
DELETE FROM processed_events WHERE provider IN ('stripe_claim', 'stripe_fulfill_claim');
