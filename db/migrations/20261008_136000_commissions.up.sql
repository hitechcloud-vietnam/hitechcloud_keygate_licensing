-- Reseller commerce slice 2 (plan §31 / Phase 7): the commission
-- ledger and the per-reseller wholesale price overrides. Both grow on
-- the reseller rows of 20261008_135000_resellers; the tables they
-- reference (resellers, plans) are untouched here.
--
-- Money discipline throughout: every amount is BIGINT minor units and
-- every percentage is an integer number of basis points (bps,
-- 10000 = 100%). No numeric/float anywhere near a rate or an amount.

-- The commission ledger: one row per (reseller, order) — what this
-- partner earned on this sale.
--
-- Each row is a self-contained commercial record. basis_minor, bps and
-- amount_minor are all snapshotted at accrual time, so the number can
-- be re-derived and audited without consulting the orders table or the
-- reseller's current contract rate. The accrual math is
-- floor(basis_minor * bps / 10000) — rounding DOWN, so a fractional
-- minor unit stays with the house (see model.CommissionAmount).
--
-- order_id is TEXT with NO foreign key, deliberately: this ledger is a
-- financial record that must survive order-retention policies. When an
-- order row is eventually purged, what the partner was paid for it
-- must still be answerable, so the commission must not cascade away
-- with it (compare Order.LicenseID, kept FK-free for the same reason).
-- What is kept instead of the join is the snapshot above.
--
-- Referential actions:
--   * reseller_id ON DELETE RESTRICT — the ledger is never silently
--     erased by deleting its reseller. Combined with the slice-1
--     delete policy (store.DeleteReseller refuses while allocations
--     exist), a reseller with commissions keeps its account; the FK
--     is the backstop for anything that bypasses that check.
CREATE TABLE IF NOT EXISTS commissions (
    id           TEXT PRIMARY KEY,
    reseller_id  TEXT NOT NULL REFERENCES resellers(id) ON DELETE RESTRICT,
    -- Durable reference, not a relation — see the header note.
    order_id     TEXT NOT NULL,
    -- The order amount the commission was computed on (minor units).
    basis_minor  BIGINT NOT NULL
                 CONSTRAINT commissions_basis_check
                 CHECK (basis_minor >= 0),
    -- The rate used, in basis points. Bounded to a real percentage at
    -- the database level, same as resellers.commission_bps.
    bps          INT NOT NULL
                 CONSTRAINT commissions_bps_check
                 CHECK (bps BETWEEN 0 AND 10000),
    -- The exact integer result of the accrual math, rounded down.
    amount_minor BIGINT NOT NULL
                 CONSTRAINT commissions_amount_check
                 CHECK (amount_minor >= 0),
    -- Closed vocabulary, enforced here as well as in the handler: an
    -- invented status would silently vanish from the payout
    -- dashboards and the payout rules that read this column.
    status       TEXT NOT NULL DEFAULT 'accrued'
                 CONSTRAINT commissions_status_check
                 CHECK (status IN ('accrued', 'approved', 'paid', 'cancelled')),
    -- Set when disbursed (status 'paid'); NULL until then.
    paid_at      TIMESTAMPTZ,
    notes        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Idempotency: AT MOST ONE commission per (reseller, order). A retried
-- accrual hits this index and is answered with the original row
-- instead of a second payout. This is the constraint the store's
-- INSERT ... ON CONFLICT (reseller_id, order_id) DO NOTHING names.
CREATE UNIQUE INDEX IF NOT EXISTS idx_commissions_reseller_order
    ON commissions (reseller_id, order_id);
-- The listing walk: every read of this ledger is "one reseller's rows,
-- newest accrual first" (admin page and portal page alike), so the
-- index carries the order and the (created_at, id) tiebreaker makes
-- paging line up. The reseller_id prefix also serves the per-status
-- dashboard sums.
CREATE INDEX IF NOT EXISTS idx_commissions_reseller_created
    ON commissions (reseller_id, created_at DESC, id DESC);

-- Wholesale price overrides: what one reseller pays for one plan,
-- instead of the public (Stripe) price. Configuration, not history —
-- the pair (reseller_id, plan_id) is the whole identity and a changed
-- deal is written over in place. Past money lives in commissions
-- above, not here.
--
-- Referential actions:
--   * plan_id ON DELETE CASCADE — price config for a dead plan means
--     nothing; deleting the plan takes its overrides with it.
--   * reseller_id ON DELETE CASCADE — the same reasoning as
--     reseller_customers (slice 1): this is account configuration, not
--     a commercial record, so it goes with the account. The asymmetry
--     with commissions.reseller_id above is deliberate: RESTRICT
--     protects history, CASCADE cleans up config.
CREATE TABLE IF NOT EXISTS reseller_price_overrides (
    reseller_id       TEXT NOT NULL REFERENCES resellers(id) ON DELETE CASCADE,
    plan_id           TEXT NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    -- The wholesale price in minor units. Zero is legal (a comped
    -- partner); negative is not.
    unit_amount_minor BIGINT NOT NULL
                      CONSTRAINT reseller_price_overrides_amount_check
                      CHECK (unit_amount_minor >= 0),
    -- ISO 4217 code as given by the client. Whether it names a real
    -- currency cannot be checked offline (the plan's Stripe price
    -- currency lives in Stripe); the shape is all that is enforced —
    -- exactly three uppercase letters (model.ValidCurrencyCode).
    currency          CHAR(3) NOT NULL
                      CONSTRAINT reseller_price_overrides_currency_check
                      CHECK (currency ~ '^[A-Z]{3}$'),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (reseller_id, plan_id)
);
-- The primary key leads with reseller_id, so the reseller-side walk
-- ("this partner's wholesale prices") is already covered; no extra
-- index is needed for the reads this slice does.
