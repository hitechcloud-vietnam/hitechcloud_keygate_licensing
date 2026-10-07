-- Customer API keys: portal self-service credentials (plan §29/§34).
--
-- The table is customer_api_keys, NOT api_keys: that name has been
-- taken since 20260320_init by the operator/product server-to-server
-- keys (model.APIKey, minted in the admin panel). Two different
-- credentials with two lifecycles get two tables.
--
-- The secret is shown to the customer exactly once, at creation. Only
-- key_prefix (first 12 chars, safe to display) and key_hash (SHA-256
-- hex of the full secret) are stored — the secret itself is never
-- recoverable and never touches the database.
CREATE TABLE IF NOT EXISTS customer_api_keys (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    key_prefix   TEXT NOT NULL,
    key_hash     TEXT NOT NULL,
    -- Comma-separated permission list; '' = no extra scope.
    scopes       TEXT NOT NULL DEFAULT '',
    expires_at   TIMESTAMPTZ NULL,
    last_used_at TIMESTAMPTZ NULL,
    revoked_at   TIMESTAMPTZ NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- The portal answers "my keys" on every visit.
CREATE INDEX IF NOT EXISTS idx_customer_apikeys_user ON customer_api_keys (user_id);
-- Authentication looks a key up by the hash of the presented secret;
-- uniqueness keeps two secrets from ever collapsing into one identity.
CREATE UNIQUE INDEX IF NOT EXISTS idx_customer_apikeys_hash ON customer_api_keys (key_hash);
