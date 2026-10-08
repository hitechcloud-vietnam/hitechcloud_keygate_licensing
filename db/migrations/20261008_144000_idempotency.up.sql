-- Idempotency-Key support (plan §36): a scope-aware request-dedup store.
--
-- Replaces the earlier (key, endpoint)-keyed table from
-- 20260516_post_bundle_evolution with a scope-based design:
--
--   * scope           -- caller identity (e.g. "user:123", "api_key:abc"),
--                        so two callers reusing the same key never collide.
--   * idempotency_key -- the client's Idempotency-Key header (1..255 chars).
--   * request_hash    -- SHA-256(method+path+body) as 64-char hex, so a
--                        reused key carrying a DIFFERENT body is detectable.
--   * response_status -- nullable; set once the reply is cached.
--   * response_body   -- nullable BYTEA; the cached reply to replay.
--   * state           -- 'in_progress' while the handler runs, 'done' once
--                        the reply is cached.
--
-- UNIQUE(scope, idempotency_key) is both the claim target for
-- INSERT ... ON CONFLICT DO NOTHING and the lookup key for replay.
--
-- The old table is dropped here: in-flight idempotency state is ephemeral
-- and safe to discard across a deploy.
DROP INDEX IF EXISTS idx_idempotency_keys_expires_at;
DROP TABLE IF EXISTS idempotency_keys;

CREATE TABLE idempotency_keys (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    scope           TEXT     NOT NULL CHECK (length(scope) BETWEEN 1 AND 255),
    idempotency_key TEXT     NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 255),
    request_hash    CHAR(64) NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
    response_status INT,
    response_body   BYTEA,
    state           TEXT     NOT NULL CHECK (state IN ('in_progress', 'done')),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 24h TTL -- Stripe's standard. ExpireIdempotencyKeys runs on a
    -- periodic timer to delete rows past expires_at.
    expires_at      TIMESTAMPTZ NOT NULL,
    UNIQUE (scope, idempotency_key)
);

CREATE INDEX idx_idempotency_keys_expires_at ON idempotency_keys(expires_at);
