-- Revert the scope-based idempotency table back to the pre-20261008
-- (key, endpoint)-keyed shape from 20260516_post_bundle_evolution.
--
-- The scoped rows are dropped: they are ephemeral request-dedup state and
-- have no meaning under the old keying.
DROP INDEX IF EXISTS idx_idempotency_keys_expires_at;
DROP TABLE IF EXISTS idempotency_keys;

CREATE TABLE IF NOT EXISTS idempotency_keys (
    key               TEXT    NOT NULL CHECK (length(key) BETWEEN 1 AND 256),
    endpoint          TEXT    NOT NULL CHECK (length(endpoint) BETWEEN 1 AND 256),
    body_hash         TEXT    NOT NULL CHECK (body_hash ~ '^[a-f0-9]{64}$'),
    response_status   INT     NOT NULL DEFAULT 0,
    response_body     TEXT    NOT NULL DEFAULT '',
    response_complete BOOLEAN NOT NULL DEFAULT FALSE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 24h TTL -- Stripe's standard. Cleanup runs on a periodic timer.
    expires_at        TIMESTAMPTZ NOT NULL DEFAULT (now() + interval '24 hours'),
    PRIMARY KEY (key, endpoint)
);

CREATE INDEX IF NOT EXISTS idx_idempotency_keys_expires_at
    ON idempotency_keys(expires_at);
