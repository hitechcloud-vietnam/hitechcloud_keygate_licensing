-- Affiliate accounts, referral codes, click attribution, conversions
-- and payouts (plan §32). One affiliate promotes the product with a
-- referral code; a click on /r/<code> is recorded; a signup within the
-- cookie window converts; the conversion earns a commission that is
-- reviewed and eventually settled by a payout.
--
-- Money discipline throughout: a percentage is an integer number of
-- basis points (bps) and an amount is an integer number of minor
-- units, never a float.
--
-- Privacy discipline throughout: referral_clicks stores salted SHA-256
-- hashes of the IP and user agent, NEVER the raw values. The hash
-- carries the two things it is for — the click dedup window and (later)
-- click-to-signup matching — without the table becoming a location
-- history of every visitor. The salt must come from deployment config
-- (see model.HashReferralIP; a config field is a shared-core follow-up
-- for the Lead).
--
-- Fraud controls in this schema: UNIQUE(order_id) makes one order
-- convert at most once; CHECK constraints pin every status vocabulary;
-- and the referential actions below keep money records (conversions,
-- payouts) from ever being silently deleted.

CREATE TABLE IF NOT EXISTS affiliates (
    id               TEXT PRIMARY KEY,
    name             TEXT NOT NULL,
    -- The payout correspondence address and the handle the admin list
    -- searches by. Stored folded (trimmed + lower-cased, see
    -- model.NormalizeAffiliateEmail) so one address cannot exist under
    -- two spellings. Unique: a duplicate answers 409
    -- (store.IsAffiliateEmailConflict).
    contact_email    TEXT NOT NULL UNIQUE,
    -- Closed vocabulary, enforced here as well as in the handler: an
    -- invented status would be silently ignored by the conversion
    -- rules that read it. suspended is the fraud kill-switch — the
    -- account and its history stay, but it never converts again.
    status           TEXT NOT NULL DEFAULT 'active'
                     CONSTRAINT affiliates_status_check
                     CHECK (status IN ('active', 'suspended')),
    -- Which commission field pays: percent uses commission_bps, fixed
    -- uses commission_minor. Both fields are always stored so
    -- switching the model is an edit, not a migration.
    commission_model TEXT NOT NULL DEFAULT 'percent'
                     CONSTRAINT affiliates_commission_model_check
                     CHECK (commission_model IN ('percent', 'fixed')),
    -- The percent rate in basis points (10000 = 100%), integer only;
    -- the CHECK bounds it to a real percentage at the database level.
    commission_bps   INT NOT NULL DEFAULT 0
                     CONSTRAINT affiliates_commission_bps_check
                     CHECK (commission_bps BETWEEN 0 AND 10000),
    -- The fixed-model flat amount in minor units, integer only.
    commission_minor BIGINT NOT NULL DEFAULT 0
                     CONSTRAINT affiliates_commission_minor_check
                     CHECK (commission_minor >= 0),
    payout_method    TEXT NOT NULL DEFAULT '',
    notes            TEXT NOT NULL DEFAULT '',
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- The admin listing filters by status and orders by name; this covers
-- that walk (status, then name/id tiebreak).
CREATE INDEX IF NOT EXISTS idx_affiliates_status ON affiliates (status, name, id);

-- One row per shareable handle — the thing that goes in /r/<code> and
-- in the htc_ref cookie.
CREATE TABLE IF NOT EXISTS referral_codes (
    id           TEXT PRIMARY KEY,
    -- A code belongs to one affiliate and never outlives it: deleting
    -- the account takes its codes (and their clicks) with it.
    affiliate_id TEXT NOT NULL REFERENCES affiliates(id) ON DELETE CASCADE,
    -- The handle, stored canonical: uppercase [A-Z0-9]{4,32} (see
    -- model.NormalizeReferralCode) so one code cannot be spelled
    -- several ways to split its clicks. UNIQUE — that constraint is
    -- the index the /r/<code> lookup resolves against (one row per
    -- handle, one handle per row); no separate index is needed.
    code         TEXT NOT NULL UNIQUE,
    -- Where /r/<code> sends the visitor. NULL = the platform default.
    -- This stored URL is the ONLY external redirect target the
    -- endpoint ever uses (open-redirect discipline), and it is
    -- validated as a full http(s) URL when written, so a javascript:
    -- or data: URL can never end up here.
    landing_url  TEXT,
    -- The per-code kill-switch. Deactivating keeps the row and its
    -- history; deleting is only for codes that never earned anything.
    active       BOOLEAN NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- The affiliate-side walk ("what codes does this partner have"). The
-- unique constraint on code covers the handle side.
CREATE INDEX IF NOT EXISTS idx_referral_codes_affiliate ON referral_codes (affiliate_id);

-- One row per recorded visit through a referral code: the attribution
-- evidence behind a conversion. Hashes only — see the privacy note at
-- the top.
CREATE TABLE IF NOT EXISTS referral_clicks (
    id                TEXT PRIMARY KEY,
    -- A click means nothing without its code: it cascades so the row
    -- can never orphan.
    code_id           TEXT NOT NULL REFERENCES referral_codes(id) ON DELETE CASCADE,
    clicked_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- SHA-256 hex of the client IP with a deployment salt — NEVER a
    -- raw address (see model.HashReferralIP).
    ip_hash           TEXT NOT NULL,
    -- SHA-256 hex of the user agent with the same salt — NEVER the raw
    -- string (see model.HashReferralUserAgent).
    user_agent_hash   TEXT NOT NULL DEFAULT '',
    -- The account that later converted, once a future attribution
    -- slice matches clicks to signups; NULL until then. No FK — the
    -- click record must outlive the account.
    converted_user_id TEXT
);
-- The dedup window and the per-code timeline both walk
-- (code_id, clicked_at); this index is that walk.
CREATE INDEX IF NOT EXISTS idx_referral_clicks_code_time ON referral_clicks (code_id, clicked_at);

-- One row per attributed sale. UNIQUE(order_id) is the idempotency
-- key: one order converts at most once, so a retried checkout callback
-- can never double a commission.
CREATE TABLE IF NOT EXISTS affiliate_conversions (
    id                TEXT PRIMARY KEY,
    -- The money records below RESTRICT on both owners: a conversion is
    -- a commercial record (who earned what) and must not vanish with
    -- its affiliate or its code. store.DeleteAffiliate /
    -- store.DeleteReferralCode refuse first with typed errors; the
    -- RESTRICT backs that up if a check is bypassed.
    affiliate_id      TEXT NOT NULL REFERENCES affiliates(id) ON DELETE RESTRICT,
    code_id           TEXT NOT NULL REFERENCES referral_codes(id) ON DELETE RESTRICT,
    -- Deliberately NO foreign key: the conversion must outlive any
    -- ledger cleanup, and the checkout may report it for an order
    -- written in the same breath. The unique index above is the only
    -- constraint — and the only one the idempotency rule needs.
    order_id          TEXT NOT NULL UNIQUE,
    -- Caller-reported, deliberately no FK (same reasoning as
    -- order_id): a deleted account cannot retroactively erase
    -- attribution.
    user_id           TEXT,
    -- Integer minor units, non-negative by CHECK. The total the
    -- commission was computed on, and the commission itself
    -- (model.Affiliate.CommissionFor: percent rounds half up of
    -- total·bps/10000; fixed is the flat amount).
    order_total_minor BIGINT NOT NULL
                      CONSTRAINT affiliate_conversions_total_check
                      CHECK (order_total_minor >= 0),
    commission_minor  BIGINT NOT NULL
                      CONSTRAINT affiliate_conversions_commission_check
                      CHECK (commission_minor >= 0),
    -- Closed vocabulary (see model.ConversionTransitionOK): pending is
    -- the review queue; approved and paid can only be reversed
    -- (clawback); rejected and reversed are terminal. paid is applied
    -- by a payout's settlement, never directly.
    status            TEXT NOT NULL DEFAULT 'pending'
                      CONSTRAINT affiliate_conversions_status_check
                      CHECK (status IN ('pending', 'approved', 'paid', 'rejected', 'reversed')),
    -- The payout that claimed (and settled) this conversion; NULL
    -- while it is still accrued. Deliberately no FK: the two tables'
    -- lifecycles stay independent (a failed payout leaves this
    -- readable) and payouts are never deleted.
    payout_id         TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- The review listing walks one affiliate's conversions newest-first
-- (created_at, then id); the prefix on affiliate_id serves both this
-- and SumPendingCommissions.
CREATE INDEX IF NOT EXISTS idx_affiliate_conversions_affiliate
    ON affiliate_conversions (affiliate_id, created_at, id);
-- The payout side of the claim ("what did this payout settle") and the
-- accrual filter (payout_id IS NULL) both walk this column.
CREATE INDEX IF NOT EXISTS idx_affiliate_conversions_payout ON affiliate_conversions (payout_id);

-- One row per movement of money to an affiliate, settling whole
-- conversions. amount_minor records the sum actually settled (a
-- conversion is settled whole or not at all — see store.CreatePayout),
-- which keeps payout.amount == Σ settled conversions true per row.
CREATE TABLE IF NOT EXISTS affiliate_payouts (
    -- RESTRICT like conversions: a payout is a record of money that
    -- moved or was requested and never vanishes with its affiliate.
    id           TEXT PRIMARY KEY,
    affiliate_id TEXT NOT NULL REFERENCES affiliates(id) ON DELETE RESTRICT,
    -- Integer minor units, non-negative by CHECK (money discipline).
    amount_minor BIGINT NOT NULL
                 CONSTRAINT affiliate_payouts_amount_check
                 CHECK (amount_minor >= 0),
    -- Closed vocabulary (see model.PayoutTransitionOK): only requested
    -- moves, exactly once, to paid or failed.
    status       TEXT NOT NULL DEFAULT 'requested'
                 CONSTRAINT affiliate_payouts_status_check
                 CHECK (status IN ('requested', 'paid', 'failed')),
    -- Stamped when the money actually moved (the paid transition);
    -- NULL for requested and failed.
    paid_at      TIMESTAMPTZ,
    notes        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- The per-affiliate payout history, newest-first.
CREATE INDEX IF NOT EXISTS idx_affiliate_payouts_affiliate
    ON affiliate_payouts (affiliate_id, created_at, id);

-- FUTURE WORK, deliberately out of this slice (documented, not
-- forgotten):
--   * commission caps (a maximum commission per conversion or per
--     period) — a fraud control the review flow will want.
--   * click-to-signup matching that stamps
--     referral_clicks.converted_user_id.
--   * recurring commission models (plan §32 lists Percentage, Fixed,
--     Recurring, One-time; this slice implements percent and fixed —
--     the two that need no subscription-renewal hook).
