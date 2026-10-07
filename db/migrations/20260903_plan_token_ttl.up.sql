-- Plans: per-plan check-in interval for signed license tokens.
-- 0 = use the server default (7 days). The token is still clamped to
-- valid_until + grace_days, so this can only shorten the offline
-- window relative to the licence, never extend it.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS token_ttl_days INTEGER NOT NULL DEFAULT 0;
