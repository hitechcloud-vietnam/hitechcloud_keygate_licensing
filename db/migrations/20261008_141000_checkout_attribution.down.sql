-- Reverse of the checkout attribution slice: the four snapshot
-- columns go (the index drops with its column; the explicit drop is
-- kept for clarity). No rows are rewritten and no partner ledger rows
-- are touched — commissions and conversions carry their own snapshots
-- and outlive this schema, by design.

DROP INDEX IF EXISTS idx_orders_reseller_id;

ALTER TABLE orders DROP COLUMN IF EXISTS reseller_id;
ALTER TABLE orders DROP COLUMN IF EXISTS reseller_email;
ALTER TABLE orders DROP COLUMN IF EXISTS referral_code;
ALTER TABLE orders DROP COLUMN IF EXISTS affiliate_id;
