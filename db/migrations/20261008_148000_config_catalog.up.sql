-- Config catalog (config-in-DB): seed one settings row per catalog
-- key (internal/config/keys.go) with its DEFAULT value as plain text,
-- so the admin config screen shows the full editable list from the
-- first boot and every consumer can read a row through
-- store.GetSetting without missing-row special cases.
--
-- ON CONFLICT (key) DO NOTHING: installs that already set a value —
-- including the branding rows (site_name, timezone, brand_color,
-- language, logo_url) the legacy settings UI has always written — keep
-- what they have. Nothing here may overwrite operator data.
--
-- Secrets seed EMPTY, never a real value: a seeded secret would be a
-- lie (the migration knows no credentials) and would also read as
-- "configured" in the admin UI.
--
-- Precedence at read time (service.ConfigService) is
-- stored row > env var > this default, with one refinement: a row that
-- still holds the default counted here does NOT shadow its env var, so
-- an install upgrading with PAY2S_* / SMTP_* / STRIPE_* still in the
-- environment keeps working unchanged (those env vars are deprecated
-- and logged at boot, not silently ignored).
--
-- The down migration deletes EXACTLY these seeded rows — and only
-- while they still hold the seeded value — so a rollback never
-- destroys a value an operator wrote.

INSERT INTO settings (key, value) VALUES
    -- app
    ('app.base_url', 'http://localhost:9000'),
    ('app.environment', 'development'),
    ('app.quota_warning_threshold', '8000'),
    ('app.admin_emails', ''),
    -- payment: Stripe
    ('payment.stripe_secret_key', ''),
    ('payment.stripe_webhook_secret', ''),
    ('payment.stripe_livemode', 'false'),
    -- payment: Pay2S
    ('payment.pay2s.partner_code', ''),
    ('payment.pay2s.partner_name', 'HiTechCloud'),
    ('payment.pay2s.access_key', ''),
    ('payment.pay2s.secret_key', ''),
    ('payment.pay2s.bank_accounts', ''),
    ('payment.pay2s.base_url', 'https://payment.pay2s.vn'),
    -- payment: ZaloPay
    ('payment.zalopay.app_id', ''),
    ('payment.zalopay.key1', ''),
    ('payment.zalopay.callback_key', ''),
    ('payment.zalopay.base_url', 'https://openapi.zalopay.vn'),
    -- payment: payOS
    ('payment.payos.client_id', ''),
    ('payment.payos.api_key', ''),
    ('payment.payos.checksum_key', ''),
    ('payment.payos.base_url', 'https://api-merchant.payos.vn'),
    -- smtp
    ('smtp.host', ''),
    ('smtp.port', '587'),
    ('smtp.username', ''),
    ('smtp.password', ''),
    ('smtp.from', ''),
    -- domains
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
    -- ratelimit
    ('ratelimit.api', '60'),
    ('ratelimit.admin', '120'),
    ('ratelimit.auth', '60'),
    ('ratelimit.otp_send', '30'),
    ('ratelimit.bf_max_fails', '5'),
    ('ratelimit.bf_lockout_seconds', '30'),
    -- webhook
    ('webhook.max_attempts', '5'),
    ('webhook.retry_interval', '30s'),
    ('webhook.http_timeout', '10s'),
    ('webhook.allow_private', 'false'),
    -- retention (the retention job reads these keys; 0 = keep forever)
    ('retention.notifications_days', '90'),
    ('retention.processed_events_days', '30'),
    ('retention.webhook_deliveries_days', '90'),
    ('retention.audit_logs_days', '365'),
    -- storage
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
    -- observability
    ('observability.log_level', 'info'),
    -- branding (rows shared with the legacy settings UI)
    ('site_name', ''),
    ('timezone', ''),
    ('brand_color', ''),
    ('language', ''),
    ('logo_url', '')
ON CONFLICT (key) DO NOTHING;
