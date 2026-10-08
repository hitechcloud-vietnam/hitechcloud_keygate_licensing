-- Customer self-service webhooks (plan §29 portal "Webhooks", §35).
--
-- Customers register their own endpoints and receive signed event
-- deliveries. This is the customer-scoped twin of the operator/product
-- `webhooks` table (20260321_saas_extension): that one is scoped to a
-- product and minted by an admin; this one is scoped to a portal user
-- and minted by the customer. Two webhook systems, two tables.
--
-- The signing secret (secret) is kept in plaintext so the delivery
-- signer can compute the HMAC-SHA256 over each payload at dispatch
-- time. It is shown to the customer exactly once, at creation, and is
-- never returned again (only secret_prefix is ever exposed, and only
-- ever as a display hint). It is NEVER logged. See the model doc for
-- the encryption-at-rest follow-up.
CREATE TABLE IF NOT EXISTS customer_webhooks (
    id               TEXT PRIMARY KEY,
    user_id          TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    url              TEXT NOT NULL,
    events           TEXT[] NOT NULL DEFAULT '{}',
    active           BOOLEAN NOT NULL DEFAULT TRUE,
    -- HMAC-SHA256 signing secret. Needed in plaintext at dispatch time.
    secret           TEXT NOT NULL,
    secret_prefix    TEXT NOT NULL,
    last_delivery_at TIMESTAMPTZ NULL,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- The portal answers "my webhooks" on every visit.
CREATE INDEX IF NOT EXISTS idx_customer_webhooks_user ON customer_webhooks (user_id);
-- The async dispatch path fans an event out to every active endpoint
-- subscribed to it, across all users.
CREATE INDEX IF NOT EXISTS idx_customer_webhooks_active ON customer_webhooks (active);
