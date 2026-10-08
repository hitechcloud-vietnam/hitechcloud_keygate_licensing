-- Reverse: customer_webhooks (customer self-service endpoints).
DROP INDEX IF EXISTS idx_customer_webhooks_active;
DROP INDEX IF EXISTS idx_customer_webhooks_user;
DROP TABLE IF EXISTS customer_webhooks;
