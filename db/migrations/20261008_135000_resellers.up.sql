-- Reseller accounts and licence allocation (plan §31 / Phase 7). A
-- reseller is a partner that owns (has sold) a set of licences. This
-- migration lays down the account and that ownership record; wholesale
-- pricing, commissions and the reseller-facing API grow on these rows
-- later, so the shapes are deliberately open to extension.
--
-- Money discipline throughout: a percentage is an integer number of
-- basis points (bps), never a float. commission_bps is the reseller's
-- earnings share in bps (10000 = 100%).

CREATE TABLE IF NOT EXISTS resellers (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL,
    -- The address a partner is reached at and the handle the future
    -- reseller API addresses it by. Stored folded (trimmed + lower-
    -- cased, see model.NormalizeResellerEmail) so one address cannot
    -- exist under two spellings. Unique: two resellers sharing one
    -- address would make it ambiguous; a duplicate answers 409
    -- (store.IsResellerEmailConflict).
    contact_email  TEXT NOT NULL UNIQUE,
    -- Closed vocabulary, enforced here as well as in the handler: an
    -- invented status would be silently ignored by the allocation and
    -- commission rules that read it.
    status         TEXT NOT NULL DEFAULT 'active'
                   CONSTRAINT resellers_status_check
                   CHECK (status IN ('active', 'suspended')),
    -- Earnings share in basis points (10000 = 100%), integer only. The
    -- CHECK bounds it to a real percentage and keeps the money
    -- discipline at the database level.
    commission_bps INT NOT NULL DEFAULT 0
                   CONSTRAINT resellers_commission_bps_check
                   CHECK (commission_bps BETWEEN 0 AND 10000),
    notes          TEXT NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- The admin listing filters by status and orders by name; this covers
-- that walk (status, then name/id tiebreak).
CREATE INDEX IF NOT EXISTS idx_resellers_status ON resellers (status, name, id);

-- Join table: one row per allocation — this reseller owns this licence.
-- The composite PRIMARY KEY is the unique pair.
--
-- Cardinality, decided:
--   * reseller -> licences is 1:N (a partner sells many licences), so
--     the pair is the key and the reseller-side lookup walks it.
--   * licence -> reseller is at most 1:1. A licence is owned by AT MOST
--     ONE reseller, enforced by the separate UNIQUE index on license_id
--     below — one row per license_id, so a second reseller can never
--     claim a licence already allocated. This is the commercial truth
--     the commission slice depends on: a sale is attributed to one
--     partner, never split across two.
--
-- Referential actions:
--   * license_id ON DELETE CASCADE  — a licence deleted has its
--     allocation go with it; the join row can never orphan.
--   * reseller_id ON DELETE RESTRICT — a reseller cannot be deleted
--     while it still owns licences. This is the documented delete
--     policy (store.DeleteReseller refuses and answers 409); the FK
--     makes the rule hold even if that check is bypassed.
CREATE TABLE IF NOT EXISTS reseller_licenses (
    reseller_id  TEXT NOT NULL REFERENCES resellers(id) ON DELETE RESTRICT,
    license_id   TEXT NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    allocated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (reseller_id, license_id)
);
-- The exclusivity above, and the licence-side lookup ("which reseller
-- owns this licence"). The primary key's (reseller_id, …) prefix serves
-- the reseller side; this serves the licence side and enforces one
-- reseller per licence.
CREATE UNIQUE INDEX IF NOT EXISTS idx_reseller_licenses_license ON reseller_licenses (license_id);

-- Forward-looking (Phase 7): the customer-referral link — this reseller
-- brought this customer. Defined now so later slices grow onto it
-- without another migration touching these tables. Slice 1 writes no
-- store or handler method for it.
--
-- The identity is the pair (reseller_id, customer_email). customer_email
-- is the durable handle (a customer may predate any user account);
-- user_id is an optional enrichment linking to users(id) once the
-- customer has one, and is set NULL (not dropped) if that account is
-- ever removed — the email stays the identity. Unlike a licence, "unique
-- pair" is the whole of the constraint; tightening a customer to one
-- reseller is a later decision.
CREATE TABLE IF NOT EXISTS reseller_customers (
    reseller_id    TEXT NOT NULL REFERENCES resellers(id) ON DELETE CASCADE,
    customer_email TEXT NOT NULL,
    user_id        TEXT REFERENCES users(id) ON DELETE SET NULL,
    allocated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (reseller_id, customer_email)
);
-- The pair's key leads with reseller_id, so looking a referral up by
-- the customer needs its own index.
CREATE INDEX IF NOT EXISTS idx_reseller_customers_customer ON reseller_customers (customer_email);
