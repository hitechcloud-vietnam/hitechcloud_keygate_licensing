-- Reverse: gateway_payments (one-off VN gateway payments).
DROP INDEX IF EXISTS idx_gateway_payments_trans;
DROP INDEX IF EXISTS idx_gateway_payments_order_key;
DROP INDEX IF EXISTS idx_gateway_payments_order;
DROP INDEX IF EXISTS uq_gateway_payments_provider_ref;
DROP TABLE IF EXISTS gateway_payments;
