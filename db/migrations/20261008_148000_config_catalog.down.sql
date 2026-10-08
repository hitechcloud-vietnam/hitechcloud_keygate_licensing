-- Reverse of 20261008_148000_config_catalog: remove exactly the rows
-- that migration seeded — and only while they still hold the seeded
-- value.
--
-- The value guard is the point. A plain key-list DELETE would also
-- destroy values an operator wrote through the admin config screen (or
-- the legacy settings UI, for the shared branding rows), which is
-- data, not schema. Rows that were edited are left in place: they are
-- configuration someone chose, and the pre-catalog settings table
-- happily carries keys this migration never heard of.
--
-- Key/value pairs mirror the up migration exactly;
-- internal/config/keys_test.go pins that the two files and the catalog
-- cannot drift apart.

DELETE FROM settings s
USING (VALUES
    ('app.base_url', 'http://localhost:9000'),
    ('app.environment', 'development'),
    ('app.quota_warning_threshold', '8000'),
    ('app.admin_emails', ''),
    ('payment.stripe_secret_key', ''),
    ('payment.stripe_webhook_secret', ''),
    ('payment.stripe_livemode', 'false'),
    ('payment.pay2s.partner_code', ''),
    ('payment.pay2s.partner_name', 'HiTechCloud'),
    ('payment.pay2s.access_key', ''),
    ('payment.pay2s.secret_key', ''),
    ('payment.pay2s.bank_accounts', ''),
    ('payment.pay2s.base_url', 'https://payment.pay2s.vn'),
    ('payment.zalopay.app_id', ''),
    ('payment.zalopay.key1', ''),
    ('payment.zalopay.callback_key', ''),
    ('payment.zalopay.base_url', 'https://openapi.zalopay.vn'),
    ('payment.payos.client_id', ''),
    ('payment.payos.api_key', ''),
    ('payment.payos.checksum_key', ''),
    ('payment.payos.base_url', 'https://api-merchant.payos.vn'),
    ('smtp.host', ''),
    ('smtp.port', '587'),
    ('smtp.username', ''),
    ('smtp.password', ''),
    ('smtp.from', ''),
    ('domain.base', ''),
    ('domain.apex', ''),
    ('domain.payments', ''),
    ('domain.dashboard', ''),
    ('domain.merchant', ''),
    ('domain.customer', ''),
    ('domain.verify', ''),
    ('domain.hooks', ''),
    ('domain.docs', ''),
    ('domain.status', ''),
    ('domain.go', ''),
    ('domain.auth', ''),
    ('domain.cdn', ''),
    ('session_cookie_domain', ''),
    ('ratelimit.api', '60'),
    ('ratelimit.admin', '120'),
    ('ratelimit.auth', '60'),
    ('ratelimit.otp_send', '30'),
    ('ratelimit.bf_max_fails', '5'),
    ('ratelimit.bf_lockout_seconds', '30'),
    ('webhook.max_attempts', '5'),
    ('webhook.retry_interval', '30s'),
    ('webhook.http_timeout', '10s'),
    ('webhook.allow_private', 'false'),
    ('retention.notifications_days', '90'),
    ('retention.processed_events_days', '30'),
    ('retention.webhook_deliveries_days', '90'),
    ('retention.audit_logs_days', '365'),
    ('storage.endpoint', ''),
    ('storage.region', 'auto'),
    ('storage.bucket', ''),
    ('storage.access_key', ''),
    ('storage.secret_key', ''),
    ('storage.public_url', ''),
    ('storage.force_path_style', 'false'),
    ('storage.upload_ttl', '1h'),
    ('storage.download_ttl', '10m'),
    ('storage.feed_url_ttl', '24h'),
    ('storage.max_release_sign_size_mb', '500'),
    ('observability.log_level', 'info'),
    ('site_name', ''),
    ('timezone', ''),
    ('brand_color', ''),
    ('language', ''),
    ('logo_url', '')
) AS seed(key, value)
WHERE s.key = seed.key AND s.value = seed.value;
