-- Reverse: commissions + reseller_price_overrides. Tables drop in
-- dependency order (both reference resellers/plans, so they drop
-- before anything they point at would), and dropping a table drops its
-- indexes, so the explicit index drops below are only there to stay
-- quiet on a database that never had them. The resellers, plans and
-- orders rows themselves are untouched — in particular the orders the
-- ledger recorded are NOT removed: a down migration unwrites schema,
-- not financial history.
DROP INDEX IF EXISTS idx_commissions_reseller_created;
DROP INDEX IF EXISTS idx_commissions_reseller_order;
DROP TABLE IF EXISTS reseller_price_overrides;
DROP TABLE IF EXISTS commissions;
