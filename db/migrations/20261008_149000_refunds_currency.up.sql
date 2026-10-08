-- Refunds, revocation reasons and multi-currency plan prices
-- (plan §79 partial + manual refunds, §80 revocation reasons,
-- §53 multi-currency prices).
--
-- Everything here is additive: the refunds and plan_prices tables are
-- new, and the orders / licenses changes are new columns plus one
-- WIDENED status vocabulary (the invoice-status widening of
-- 20261008_138000_po_workflow is the precedent — the existing status
-- strings are untouched, rows written before this migration keep
-- scanning unchanged).
--
-- Money discipline (plan §51): every money column is BIGINT minor
-- units — never floating point. VND is ISO-4217 exponent 0 (whole
-- dong).

-- ─── Refunds (§79) ───
--
-- One row per refund issued against an order — full, partial or
-- manual. The row is written for every refund the platform records,
-- whether the money moved through a gateway (stripe, zalopay — which
-- refunds asynchronously and settles this row later) or by hand
-- (pay2s and payos have no refund API, so their refunds are recorded
-- as 'manual' with no gateway call and settle immediately).
--
-- order_id carries a plain reference (no CASCADE): a refund is a
-- financial record and must survive the ledger around it being
-- cleaned up; the FK still stops refunds for orders that never
-- existed. Financial records are never deleted by retention
-- (docs/DATA-RETENTION.md).
CREATE TABLE IF NOT EXISTS refunds (
    id               BIGSERIAL PRIMARY KEY,
    -- orders.id is TEXT (uuid), same doctrine as gateway_payments.
    order_id         TEXT NOT NULL REFERENCES orders(id),
    -- Who moved (or was asked to move) the money: the gateway id, or
    -- 'manual' for an operator-recorded refund with no gateway call.
    payment_provider TEXT NOT NULL DEFAULT ''
                     CHECK (payment_provider IN ('', 'stripe', 'pay2s', 'zalopay', 'payos', 'manual')),
    -- The gateway's handle for the payment being refunded (Stripe
    -- payment intent / charge, Pay2S orderId, ZaloPay app_trans_id,
    -- payOS orderCode). '' for manual refunds.
    provider_ref     TEXT NOT NULL DEFAULT '',
    -- The gateway's own transaction id when one exists (ZaloPay
    -- zp_trans_id, payOS reference) — the reconciliation key.
    trans_id         TEXT NOT NULL DEFAULT '',
    amount_minor     BIGINT NOT NULL CHECK (amount_minor > 0),
    currency         TEXT NOT NULL,
    reason           TEXT NOT NULL DEFAULT '',
    -- pending = accepted by an asynchronous gateway (ZaloPay) and not
    -- yet reconciled; succeeded = money is back; failed = refused.
    -- A pending row still reserves its amount against further refunds.
    status           TEXT NOT NULL DEFAULT 'pending'
                     CHECK (status IN ('pending', 'succeeded', 'failed')),
    -- Who asked for it (admin user id, or a system actor like
    -- 'refund' automation). Free text: the actor may predate users.
    refunded_by      TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The order's refund list and the "already refunded" sum that caps a
-- new refund. Order-only index: every read of this table is per order.
CREATE INDEX IF NOT EXISTS idx_refunds_order ON refunds (order_id);

-- ─── Orders: partial refunds (§79) ───
--
-- 'partially_refunded' extends the order status vocabulary: money has
-- gone back but not all of it, so the order is still open business.
-- The CHECK is widened, never rewritten. refunded_minor accumulates
-- the money actually returned (succeeded refunds only) so the
-- remaining refundable amount is total_minor − refunded_minor without
-- re-summing the ledger; historical totals are never recalculated.
ALTER TABLE orders ADD COLUMN IF NOT EXISTS refunded_minor BIGINT NOT NULL DEFAULT 0
    CHECK (refunded_minor >= 0);

ALTER TABLE orders DROP CONSTRAINT IF EXISTS orders_status_check;
ALTER TABLE orders ADD CONSTRAINT orders_status_check
    CHECK (status IN ('pending', 'paid', 'failed', 'refunded', 'partially_refunded'));

-- ─── Licence revocation reasons (§80) ───
--
-- Why a licence was revoked, by whom, and when. Nullable and
-- additive: a licence revoked before this migration has all three
-- NULL and keeps scanning. The vocabulary lives in code
-- (model.RevokeReasons); the columns just persist the answer.
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS revoke_reason TEXT;
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS revoked_by    TEXT;
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS revoked_at    TIMESTAMPTZ;

-- ─── Multi-currency plan prices (§53) ───
--
-- The catalogue's per-currency price list. Stripe's Price remains the
-- source of truth for the currency a checkout actually charges when
-- no row exists here — these rows override it per currency. Money is
-- int64 minor units at the currency's ISO-4217 exponent
-- (internal/money holds the exponent table; VND exponent 0).
CREATE TABLE IF NOT EXISTS plan_prices (
    id              BIGSERIAL PRIMARY KEY,
    -- CASCADE: prices are configuration of a plan, not history.
    plan_id         TEXT NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    -- ISO 4217, upper-case — shape only, like reseller_price_overrides.
    currency        TEXT NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    amount_minor    BIGINT NOT NULL CHECK (amount_minor >= 0),
    -- The Stripe Price that carries this amount for this currency,
    -- when the merchant also sells it through Stripe. '' = the row is
    -- local pricing only (VN gateway checkouts).
    stripe_price_id TEXT NOT NULL DEFAULT '',
    is_default      BOOLEAN NOT NULL DEFAULT false,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- One price per (plan, currency): setting a currency re-prices it.
    UNIQUE (plan_id, currency)
);

-- At most one default row per plan — the currency a checkout falls
-- back to when the buyer asked for none (or an unsupported one).
CREATE UNIQUE INDEX IF NOT EXISTS uq_plan_prices_default
    ON plan_prices (plan_id) WHERE is_default;
