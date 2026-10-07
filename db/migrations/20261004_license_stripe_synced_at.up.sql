-- The newest read of a licence's Stripe subscription whose answer has
-- been applied to it. Two webhooks for one subscription can overlap, and
-- the one that asked Stripe first may write last; the licence write is
-- made only while this read is not older than the one already applied
-- (store.UpdateLicenseFromSubscriptionRead). NULL: none applied yet.
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS stripe_synced_at TIMESTAMPTZ;
