-- Reversible: drop the inbox table and everything hung on it
-- (the index goes with the table). The `notifications` email-dedup
-- ledger is untouched by this migration in either direction.
DROP TABLE IF EXISTS user_notifications;
