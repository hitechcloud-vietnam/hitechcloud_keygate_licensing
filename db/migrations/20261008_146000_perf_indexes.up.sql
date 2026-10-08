-- 20261008_146000_perf_indexes — hot-path indexes (plan §62
-- PERFORMANCE, §64 MIGRATIONS) plus the one additive column the §35
-- delivery-replay marker needs.
--
-- Every index below serves a query that exists today in
-- internal/store/*.go (annotated beside it). Nothing existing is
-- touched or dropped, everything is IF NOT EXISTS, and the .down
-- migration reverses all of it.
--
--   idx_orders_customer_email_lower
--       store/portal_commerce.go — ListOrdersByEmail,
--       CountOrdersByEmail, FindOrderByIdAndEmail and
--       FindInvoiceForEmail filter on
--       lower("order".customer_email) = ? / lower(orders.customer_email) = ?.
--       An expression predicate cannot use the plain
--       idx_orders_customer_email; mirrors idx_licenses_email_lower
--       (20261001) on the licences table.
--
--   idx_webhook_deliveries_webhook_status_created
--       store/webhook.go — ListWebhookDeliveries:
--       WHERE webhook_id = ? [AND status = ?] [AND event = ?]
--       ORDER BY created_at DESC, id DESC (webhookDeliveryOrder) —
--       the admin delivery log + the "failed only" drill-down. The
--       trailing id keeps pages stable on tied timestamps.
--
--   idx_audit_logs_actor_id_created_at
--       store/analytics.go — GetUserDetail:
--       WHERE actor_id = ? ORDER BY created_at DESC LIMIT 20.
--
--   idx_audit_logs_entity_created_at
--       store/admin.go — ListAuditLogs:
--       WHERE entity = ? [AND entity_id = ?]
--       ORDER BY created_at DESC, id DESC.
--
--   idx_refresh_tokens_expires_at
--       store/store.go — the prune sweep
--       DELETE FROM refresh_tokens WHERE expires_at < now().
--
-- Column: webhook_deliveries.replay_of (plan §35 replay) — nullable,
-- no default, no constraint. It names the delivery a replay row
-- re-fires (model.WebhookReplayDelivery). Plain model.WebhookDelivery
-- reads and writes never select or update it.

ALTER TABLE webhook_deliveries ADD COLUMN IF NOT EXISTS replay_of TEXT;

CREATE INDEX IF NOT EXISTS idx_orders_customer_email_lower
    ON orders (lower(customer_email));
CREATE INDEX IF NOT EXISTS idx_webhook_deliveries_webhook_status_created
    ON webhook_deliveries (webhook_id, status, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_actor_id_created_at
    ON audit_logs (actor_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_logs_entity_created_at
    ON audit_logs (entity, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_refresh_tokens_expires_at
    ON refresh_tokens (expires_at);
