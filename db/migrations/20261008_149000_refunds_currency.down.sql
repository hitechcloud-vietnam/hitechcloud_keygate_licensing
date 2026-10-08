-- Reverse of 20261008_149000_refunds_currency. Tables drop first
-- (their indexes go with them), then the order/licence columns, then
-- the order status vocabulary is narrowed back to its original four
-- strings.
--
-- The narrowing refuses to run while any order sits in
-- 'partially_refunded' — reconcile those rows first (or accept that
-- this down migration needs that cleanup). No rows are rewritten
-- here: a down migration reverses schema, it does not invent history.
-- refunds is dropped, not retained: it is this migration that created
-- it, and its history has no table to return to. Export before
-- rolling back.

DROP TABLE IF EXISTS plan_prices;

ALTER TABLE licenses DROP COLUMN IF EXISTS revoke_reason;
ALTER TABLE licenses DROP COLUMN IF EXISTS revoked_by;
ALTER TABLE licenses DROP COLUMN IF EXISTS revoked_at;

ALTER TABLE orders DROP COLUMN IF EXISTS refunded_minor;

ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check
    CHECK (status IN ('pending', 'paid', 'failed', 'refunded'));

DROP TABLE IF EXISTS refunds;
