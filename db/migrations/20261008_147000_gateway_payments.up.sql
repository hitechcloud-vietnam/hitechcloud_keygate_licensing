-- Gateway payments (plan §25 provider abstraction): one row per
-- one-off VND payment handed to a buyer through a Vietnamese gateway
-- (Pay2S bank transfer / Napas 247 QR, ZaloPay, payOS VietQR).
-- Subscriptions stay on Stripe — only one-time purchases land here.
--
-- Lifecycle: the checkout writes the row as 'pending' alongside its
-- pending order; the gateway's IPN/webhook settles it to 'succeeded'
-- and fulfilment turns the order into a paid ledger entry with a
-- licence. The IPN is the only writer of the terminal states.
--
-- provider_ref is the gateway-side handle (Pay2S orderId/requestId,
-- ZaloPay app_trans_id, payOS orderCode) — UNIQUE per provider so an
-- IPN finds exactly its payment and a replay cannot mint a second
-- row. order_key is OUR order number (HTC-…), the value the gateways
-- echo in their callbacks.
--
-- Money discipline (plan §51): amount_minor is int64 minor units; for
-- VND (ISO-4217 exponent 0) those are whole dong. No floats anywhere
-- near this table.
CREATE TABLE IF NOT EXISTS gateway_payments (
    id           BIGSERIAL PRIMARY KEY,
    -- orders.id is TEXT (uuid). Deliberately NOT BIGINT: this column
    -- must reference the orders table as it actually is. Plain
    -- reference — the ledger row outlives the payment record.
    order_id     TEXT NOT NULL REFERENCES orders(id),
    provider     TEXT NOT NULL CHECK (provider IN ('pay2s', 'zalopay', 'payos')),
    provider_ref TEXT NOT NULL,
    order_key    TEXT NOT NULL,
    amount_minor BIGINT NOT NULL,
    currency     TEXT NOT NULL DEFAULT 'VND',
    status       TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'succeeded', 'failed', 'cancelled', 'expired', 'refunded')),
    -- The gateway's own transaction id (Pay2S transId, ZaloPay
    -- zp_trans_id, payOS reference): the reconciliation key, filled
    -- at settlement. '' until then.
    trans_id     TEXT NOT NULL DEFAULT '',
    -- The provider's normalized callback payload, for the audit
    -- trail. Never contains secrets (the provider parsers redact).
    raw          JSONB NOT NULL DEFAULT '{}',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One payment per gateway handle: the IPN lookup key and the
-- idempotency guard against a checkout retried at the gateway.
CREATE UNIQUE INDEX IF NOT EXISTS uq_gateway_payments_provider_ref
    ON gateway_payments (provider, provider_ref);
-- The status-poll endpoint and the IPN's fallback lookup.
CREATE INDEX IF NOT EXISTS idx_gateway_payments_order
    ON gateway_payments (order_id);
CREATE INDEX IF NOT EXISTS idx_gateway_payments_order_key
    ON gateway_payments (provider, order_key);
-- Reconciliation by gateway transaction id (refunds, disputes).
CREATE INDEX IF NOT EXISTS idx_gateway_payments_trans
    ON gateway_payments (provider, trans_id)
    WHERE trans_id <> '';
