-- Checkout attribution (Phase 7, checkout slice): who brought each
-- sale — the reseller the order is attributed to and the affiliate
-- referral code (and its owning affiliate) it arrived through.
--
-- Everything here is additive and nullable. An order created before
-- this migration — or written by a flow with no attribution (the
-- normal public checkout) — keeps working with every new column NULL.
-- The columns are nullable TEXT because "no partner brought this
-- sale" is the normal state of most orders and NULL is the one
-- representation of it (bun writes empty as NULL, the API serializes
-- NULL away).
--
-- NO foreign keys, deliberately — the same doctrine as
-- commissions.order_id and affiliate_conversions.order_id: these are
-- snapshots in a commercial record and must survive the partner
-- account being deleted (and any order-retention purge). The
-- reseller_email column keeps the human-readable attribution exactly
-- as it was at sale time for the same reason.
--
-- reseller_id is indexed: "this partner's orders" is the listing the
-- reseller commission reconciliation reads.

ALTER TABLE orders ADD COLUMN IF NOT EXISTS reseller_id    TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS reseller_email TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS referral_code  TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS affiliate_id   TEXT;

CREATE INDEX IF NOT EXISTS idx_orders_reseller_id ON orders (reseller_id);
