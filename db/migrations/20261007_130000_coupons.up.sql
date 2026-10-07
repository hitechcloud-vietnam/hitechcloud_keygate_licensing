-- Discount coupons (admin-managed codes, redeemed at checkout)
-- Money is minor units (value_minor / minimum_order_minor) or basis
-- points (value_bps, 10000 = 100%) — never floats.
CREATE TABLE IF NOT EXISTS coupons (
    id                           TEXT PRIMARY KEY,
    code                         TEXT NOT NULL UNIQUE,
    type                         TEXT NOT NULL,
    value_bps                    BIGINT NOT NULL DEFAULT 0,
    value_minor                  BIGINT NOT NULL DEFAULT 0,
    currency                     TEXT NOT NULL DEFAULT '',
    starts_at                    TIMESTAMPTZ NULL,
    ends_at                      TIMESTAMPTZ NULL,
    max_redemptions              INT NOT NULL DEFAULT 0,
    times_redeemed               INT NOT NULL DEFAULT 0,
    max_redemptions_per_customer INT NOT NULL DEFAULT 0,
    minimum_order_minor          BIGINT NOT NULL DEFAULT 0,
    applies_to                   TEXT NOT NULL DEFAULT '',
    stackable                    BOOLEAN NOT NULL DEFAULT true,
    active                       BOOLEAN NOT NULL DEFAULT true,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at                   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Codes are matched case-insensitively (lower(code) on both sides);
-- the UNIQUE constraint on code cannot serve that lookup.
CREATE INDEX IF NOT EXISTS idx_coupons_code_lower ON coupons (lower(code));
CREATE INDEX IF NOT EXISTS idx_coupons_active ON coupons (active);
