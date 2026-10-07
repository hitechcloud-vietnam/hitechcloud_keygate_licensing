-- Refunds arrive as charge.refunded with a payment_intent. Licenses
-- used to be matched to a refund by Stripe customer (newest wins),
-- which picks the wrong license once a customer holds more than one.
-- Record the checkout session's payment intent so a refund lands on
-- the license it paid for.
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS stripe_payment_intent_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_licenses_stripe_payment_intent
    ON licenses(stripe_payment_intent_id) WHERE stripe_payment_intent_id != '';
