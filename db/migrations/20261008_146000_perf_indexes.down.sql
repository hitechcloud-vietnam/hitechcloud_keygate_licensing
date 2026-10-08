-- Reverse of 20261008_146000_perf_indexes: only the indexes and the
-- one column that migration created — nothing pre-existing is touched.

DROP INDEX IF EXISTS idx_refresh_tokens_expires_at;
DROP INDEX IF EXISTS idx_audit_logs_entity_created_at;
DROP INDEX IF EXISTS idx_audit_logs_actor_id_created_at;
DROP INDEX IF EXISTS idx_webhook_deliveries_webhook_status_created;
DROP INDEX IF EXISTS idx_orders_customer_email_lower;

ALTER TABLE webhook_deliveries DROP COLUMN IF EXISTS replay_of;
