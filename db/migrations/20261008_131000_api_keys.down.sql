-- Reverse: customer_api_keys (portal self-service credentials).
DROP INDEX IF EXISTS idx_customer_apikeys_hash;
DROP INDEX IF EXISTS idx_customer_apikeys_user;
DROP TABLE IF EXISTS customer_api_keys;
