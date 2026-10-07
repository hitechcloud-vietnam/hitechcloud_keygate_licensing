-- A perpetual plan is paid once and never renews, so it carries no
-- billing interval. The admin API now clears the interval for perpetual
-- plans; bring rows created before that rule into line.
UPDATE plans SET billing_interval = '' WHERE license_type = 'perpetual' AND billing_interval <> '';
