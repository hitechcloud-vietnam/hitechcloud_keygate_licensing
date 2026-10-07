-- SMTP was never read from the settings table (the mailer is env-only),
-- so these rows were dead — and smtp_password was a secret sitting in
-- the database for nothing. The API no longer accepts the keys.
DELETE FROM settings WHERE key IN ('smtp_host', 'smtp_port', 'smtp_username', 'smtp_password', 'smtp_from');
