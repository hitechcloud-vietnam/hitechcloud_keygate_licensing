-- The checkout session that created the license. Fulfilment is
-- idempotent per session: retries look for this row, and the unique
-- index is the last guard when two workers race for one session.
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS stripe_checkout_session_id TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_licenses_stripe_checkout_session
    ON licenses(stripe_checkout_session_id) WHERE stripe_checkout_session_id != '';

-- One Stripe price maps to one plan: Payment Link sessions resolve
-- their plan by price alone, so a duplicate would hand out an
-- arbitrary plan. Refuse to upgrade until the operator decides which
-- plan keeps the price; the message lists the conflicts.
DO $$
DECLARE dups TEXT;
BEGIN
    SELECT string_agg(stripe_price_id || ' -> ' || names, '; ')
      INTO dups
      FROM (SELECT stripe_price_id, string_agg(name || ' (' || id || ')', ', ' ORDER BY created_at) AS names
              FROM plans WHERE stripe_price_id <> ''
             GROUP BY stripe_price_id HAVING count(*) > 1) d;
    IF dups IS NOT NULL THEN
        RAISE EXCEPTION 'plans.stripe_price_id must be unique; clear or change the price on all but one plan: %', dups;
    END IF;
END $$;
CREATE UNIQUE INDEX IF NOT EXISTS idx_plans_stripe_price_unique
    ON plans(stripe_price_id) WHERE stripe_price_id != '';
