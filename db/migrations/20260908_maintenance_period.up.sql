-- Maintenance period for perpetual plans: the license never expires,
-- but only releases published before updates_until may be installed.
--
-- Rollout order matters. Replicas that predate this migration neither
-- gate feeds nor know about renewals, and leave no trace by which the
-- new version could detect them. So the features that depend on every
-- replica running this version — bounded update periods, renewals,
-- feed_license_required, manual cutoffs — stay refused by the admin
-- API until an admin turns on the maintenance_features_enabled
-- setting, which is the operator's confirmation that the rollout is
-- complete. The triggers below cover what a legacy replica writes.
-- updates_days is the period a purchase includes (0 = updates for
-- life); renewal_days and stripe_renewal_price_id describe the
-- one-time renewal a customer can buy from the portal.
ALTER TABLE plans ADD COLUMN IF NOT EXISTS updates_days INT NOT NULL DEFAULT 0;
ALTER TABLE plans ADD COLUMN IF NOT EXISTS renewal_days INT NOT NULL DEFAULT 0;
ALTER TABLE plans ADD COLUMN IF NOT EXISTS stripe_renewal_price_id TEXT NOT NULL DEFAULT '';
-- Every period a plan has sold, with the instant it took effect.
-- Keygate's own checkout freezes the period in the session, but a
-- Stripe Payment Link the merchant made carries no terms; for one of
-- those this is what says which period the buyer was shown, however
-- many times the plan has been edited since. One row per plan is
-- seeded from its current value so a plan that predates this
-- migration answers too.
-- effective_from is a whole second. For a change it is the second
-- *after* the edit; for a plan's first row it is the second the plan
-- was created in, since there is no earlier row to fall back to and a
-- checkout stamped in that second must still find the terms it was
-- created with.
-- the only stamp a Stripe session carries is its created time in
-- whole seconds, so a change made during second S cannot be told
-- apart from a checkout opened during second S. Taking effect at
-- S + 1 makes that unambiguous in the one direction that is always
-- wrong otherwise — a checkout is never charged terms that did not
-- exist when Stripe stamped it. recorded_at keeps the real instant,
-- and orders two edits made inside the same second.
CREATE TABLE IF NOT EXISTS plan_update_terms (
    id             TEXT PRIMARY KEY,
    plan_id        TEXT NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    updates_days   INT NOT NULL,
    -- The kind of licence the plan sold then. A Stripe Payment Link
    -- carries no terms at all, and the session's own shape only tells
    -- a one-off purchase from a recurring one — subscription and
    -- trial look alike. This is what says which of the two a checkout
    -- was opened against.
    license_type   TEXT NOT NULL DEFAULT '',
    effective_from TIMESTAMPTZ NOT NULL DEFAULT date_trunc('second', now()) + interval '1 second',
    recorded_at    TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
ALTER TABLE plan_update_terms ADD COLUMN IF NOT EXISTS recorded_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp();
ALTER TABLE plan_update_terms ADD COLUMN IF NOT EXISTS license_type TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_plan_update_terms_lookup
    ON plan_update_terms(plan_id, effective_from DESC, recorded_at DESC);
INSERT INTO plan_update_terms (id, plan_id, updates_days, license_type, effective_from, recorded_at)
    SELECT 'seed-' || id, id, updates_days, license_type, date_trunc('second', created_at), created_at FROM plans
    ON CONFLICT (id) DO NOTHING;

-- The history is appended by the database, not by the application:
-- an older replica, an operator at the psql prompt or an old admin
-- tool changing updates_days or license_type would otherwise leave it
-- saying something the plan no longer sells, and a Payment Link
-- session — which has no terms of its own — is authorised against
-- exactly this table.
--
-- A change takes effect from the next whole second, so a checkout
-- Stripe stamped in the second it was made still reads the terms it
-- was created with (a Stripe session's created time has no finer
-- resolution). A plan's first row has no earlier one to fall back to,
-- so it takes effect from the second the plan was created in.
CREATE OR REPLACE FUNCTION plans_record_update_terms() RETURNS trigger AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        INSERT INTO plan_update_terms (id, plan_id, updates_days, license_type, effective_from, recorded_at)
        VALUES (gen_random_uuid()::TEXT, NEW.id, NEW.updates_days, NEW.license_type,
                date_trunc('second', clock_timestamp()), clock_timestamp());
        RETURN NEW;
    END IF;
    IF NEW.updates_days IS DISTINCT FROM OLD.updates_days
       OR NEW.license_type IS DISTINCT FROM OLD.license_type THEN
        INSERT INTO plan_update_terms (id, plan_id, updates_days, license_type, effective_from, recorded_at)
        VALUES (gen_random_uuid()::TEXT, NEW.id, NEW.updates_days, NEW.license_type,
                date_trunc('second', clock_timestamp()) + interval '1 second', clock_timestamp());
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql VOLATILE;
DROP TRIGGER IF EXISTS plans_record_update_terms ON plans;
CREATE TRIGGER plans_record_update_terms
    AFTER INSERT OR UPDATE OF updates_days, license_type ON plans
    FOR EACH ROW EXECUTE FUNCTION plans_record_update_terms();

-- Products that sell maintenance periods need their update feeds to
-- identify the license: without a key the feed would be the public
-- list and the period could be skipped by dropping the key. Off by
-- default so existing installs keep public feeds until the updater
-- ships with the key.
ALTER TABLE products ADD COLUMN IF NOT EXISTS feed_license_required BOOLEAN NOT NULL DEFAULT false;
-- When the gate was last switched on. Shared caches may still hold
-- the public feed for its max-age after that instant, so bounded
-- periods wait until it has drained (see feedPublicMaxAge).
ALTER TABLE products ADD COLUMN IF NOT EXISTS feed_gated_at TIMESTAMPTZ;

-- A renewal price must not double as any plan's purchase price. A
-- replica that predates renewals resolves a session by its line-item
-- price alone: it would mint a new license for that plan instead of
-- extending the intended one, and the session would then read as
-- fulfilled. The admin API refuses the overlap; this trigger is the
-- backstop for writes that bypass it.
--
-- Two transactions assigning one value as a purchase price here and a
-- renewal price there cannot see each other's uncommitted row, so the
-- EXISTS checks alone would let both commit. Each price written is
-- first locked for the transaction (in sorted order, so two writers of
-- both prices cannot deadlock); the second writer then waits and sees
-- the first one's committed row. That visibility relies on the
-- functions being VOLATILE (the default, stated explicitly below):
-- in READ COMMITTED a volatile function takes a fresh snapshot for
-- every query it runs, so the checks after the lock see rows
-- committed after the outer statement began. STABLE would pin the
-- statement's snapshot and break this.
CREATE OR REPLACE FUNCTION plans_prices_disjoint() RETURNS trigger AS $$
DECLARE price TEXT;
BEGIN
    FOR price IN
        SELECT p FROM unnest(ARRAY[NEW.stripe_price_id, NEW.stripe_renewal_price_id]) AS p
         WHERE p <> '' ORDER BY p
    LOOP
        PERFORM pg_advisory_xact_lock(hashtext('stripe_price:' || price));
    END LOOP;
    IF NEW.stripe_renewal_price_id <> '' AND (
        NEW.stripe_renewal_price_id = NEW.stripe_price_id
        OR EXISTS (SELECT 1 FROM plans WHERE stripe_price_id = NEW.stripe_renewal_price_id AND id <> NEW.id)
    ) THEN
        RAISE EXCEPTION 'plans_renewal_price_disjoint: stripe_renewal_price_id % is a purchase price of a plan', NEW.stripe_renewal_price_id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'plans_renewal_price_disjoint';
    END IF;
    IF NEW.stripe_price_id <> '' AND EXISTS (
        SELECT 1 FROM plans WHERE stripe_renewal_price_id = NEW.stripe_price_id AND id <> NEW.id
    ) THEN
        RAISE EXCEPTION 'plans_renewal_price_disjoint: stripe_price_id % is a renewal price of a plan', NEW.stripe_price_id
            USING ERRCODE = 'check_violation', CONSTRAINT = 'plans_renewal_price_disjoint';
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql VOLATILE;
DROP TRIGGER IF EXISTS plans_prices_disjoint ON plans;
CREATE TRIGGER plans_prices_disjoint
    BEFORE INSERT OR UPDATE OF stripe_price_id, stripe_renewal_price_id ON plans
    FOR EACH ROW EXECUTE FUNCTION plans_prices_disjoint();

-- The update period compares release publish dates with the cutoff.
-- Publishing sets published_at, but rows written before that rule or
-- outside the API may lack it; without a date a release cannot be
-- shown to predate any cutoff and would be refused to bounded
-- licenses. Give such published rows their best-known date.
UPDATE releases SET published_at = COALESCE(published_at, updated_at, created_at)
 WHERE status = 'published' AND published_at IS NULL;

-- A period left on a license whose plan is not perpetual is stale by
-- definition — an older replica moved the plan without knowing the
-- column. It reports nothing to the customer, but it does block the
-- product's feed gate from coming off and this migration from rolling
-- back, so it goes here and the trigger below keeps it from coming
-- back.
-- (Runs after the columns exist; see the UPDATE further down.)
-- NULL means the license follows valid_until (subscriptions, trials)
-- or includes updates for life (perpetual plans with updates_days 0).
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS updates_until TIMESTAMPTZ;
-- Set by any writer that decided the update period itself, including
-- deciding there is none. A replica that predates the column leaves
-- it false, which is what the trigger below keys on: it must fill a
-- missing period for those writers, and must not second-guess a
-- deliberate "updates for life" from this one.
ALTER TABLE licenses ADD COLUMN IF NOT EXISTS updates_terms_set BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX IF NOT EXISTS idx_licenses_updates_until
    ON licenses(updates_until) WHERE updates_until IS NOT NULL;
UPDATE licenses l SET updates_until = NULL, updates_terms_set = true
 WHERE l.updates_until IS NOT NULL
   AND EXISTS (SELECT 1 FROM plans p WHERE p.id = l.plan_id AND p.license_type <> 'perpetual');

-- The initial period is set by the database, not only by the
-- application: during a rolling deployment a replica that predates
-- this column inserts licenses without it, and a NULL here reads as
-- updates for life. On insert, and when a license enters a perpetual
-- plan from another type, a missing period is filled from the plan.
-- Moving between perpetual plans keeps whatever the license has.
CREATE OR REPLACE FUNCTION licenses_init_updates_until() RETURNS trigger AS $$
DECLARE new_type TEXT; new_days INT; old_type TEXT;
BEGIN
    SELECT license_type, updates_days INTO new_type, new_days FROM plans WHERE id = NEW.plan_id;
    -- A plan change across the perpetual boundary decides the period
    -- again, whoever writes it. A replica that predates this column
    -- moves plan_id alone: leaving a perpetual plan would strand the
    -- old cutoff on a subscription (blocking the feed gate from ever
    -- coming off, and the migration from rolling back), and entering
    -- one again would keep that stale date instead of the new plan's
    -- period. The rule is the same one the application applies, so a
    -- current replica writing both columns agrees with it.
    IF TG_OP = 'UPDATE' AND OLD.plan_id IS DISTINCT FROM NEW.plan_id THEN
        SELECT license_type INTO old_type FROM plans WHERE id = OLD.plan_id;
        IF new_type IS DISTINCT FROM 'perpetual' THEN
            -- Leaving perpetual: the period belonged to that plan;
            -- subscriptions follow valid_until. The renewals bought
            -- into it are closed with it, as the application does: a
            -- refund of one of them must not later be subtracted from
            -- a period this licence earned somewhere else.
            NEW.updates_until := NULL;
            NEW.updates_terms_set := true;
            UPDATE license_renewals SET superseded_at = now()
             WHERE license_id = NEW.id AND superseded_at IS NULL;
            RETURN NEW;
        END IF;
        IF old_type IS DISTINCT FROM 'perpetual' THEN
            -- Entering perpetual: the new plan's period, whatever the
            -- row happened to carry before, and a ledger that belongs
            -- to no period this licence still has.
            IF COALESCE(new_days, 0) > 0 THEN
                NEW.updates_until := now() + make_interval(days => new_days);
            ELSE
                NEW.updates_until := NULL;
            END IF;
            NEW.updates_terms_set := true;
            UPDATE license_renewals SET superseded_at = now()
             WHERE license_id = NEW.id AND superseded_at IS NULL;
            RETURN NEW;
        END IF;
        -- Between two perpetual plans the customer keeps what they have.
        RETURN NEW;
    END IF;
    IF NEW.updates_until IS NOT NULL OR NEW.updates_terms_set THEN
        RETURN NEW;
    END IF;
    IF new_type IS DISTINCT FROM 'perpetual' OR COALESCE(new_days, 0) <= 0 THEN
        RETURN NEW;
    END IF;
    NEW.updates_until := now() + make_interval(days => new_days);
    RETURN NEW;
END $$ LANGUAGE plpgsql VOLATILE;
DROP TRIGGER IF EXISTS licenses_init_updates_until ON licenses;
CREATE TRIGGER licenses_init_updates_until
    BEFORE INSERT OR UPDATE OF plan_id ON licenses
    FOR EACH ROW EXECUTE FUNCTION licenses_init_updates_until();

CREATE OR REPLACE FUNCTION feed_gating_lock(pid TEXT) RETURNS void AS $$
BEGIN
    PERFORM pg_advisory_xact_lock(hashtext('feed_gating:' || pid));
END $$ LANGUAGE plpgsql;

CREATE OR REPLACE FUNCTION feed_gating_required(pid TEXT) RETURNS boolean AS $$
DECLARE gated BOOLEAN; ptype TEXT;
BEGIN
    SELECT feed_license_required, type INTO gated, ptype FROM products WHERE id = pid;
    RETURN ptype IN ('desktop', 'hybrid') AND NOT COALESCE(gated, false);
END $$ LANGUAGE plpgsql VOLATILE;

-- A finite cutoff needs gated feeds as much as a bounded plan does.
-- Runs after licenses_init_updates_until (BEFORE triggers fire in
-- name order), so it also sees the period that trigger filled in.
CREATE OR REPLACE FUNCTION licenses_require_gated_feed() RETURNS trigger AS $$
BEGIN
    IF NEW.updates_until IS NOT NULL AND (TG_OP = 'INSERT' OR OLD.updates_until IS DISTINCT FROM NEW.updates_until OR OLD.product_id <> NEW.product_id) THEN
        PERFORM feed_gating_lock(NEW.product_id);
        IF feed_gating_required(NEW.product_id) THEN
            RAISE EXCEPTION 'licenses_feed_not_gated: license with an update cutoff on product % whose feeds are public', NEW.product_id
                USING ERRCODE = 'check_violation', CONSTRAINT = 'licenses_feed_not_gated';
        END IF;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql VOLATILE;
DROP TRIGGER IF EXISTS licenses_require_gated_feed ON licenses;
CREATE TRIGGER licenses_require_gated_feed
    BEFORE INSERT OR UPDATE OF updates_until, plan_id, product_id ON licenses
    FOR EACH ROW EXECUTE FUNCTION licenses_require_gated_feed();

-- The update period is enforced on the feeds only when the product
-- requires the license key there (feed_license_required); on a public
-- feed a lapsed customer would simply omit the key. So a bounded plan,
-- renewals, or a finite license cutoff may exist only on a product
-- whose feeds are gated, and a product cannot ungate its feeds while
-- any of those exist. Products that ship no releases (saas) have no
-- feed to gate. The admin API refuses these with a message; the
-- triggers make the invariant hold under concurrent writes too, all
-- serialised per product by a transaction-scoped advisory lock.
CREATE OR REPLACE FUNCTION plans_require_gated_feed() RETURNS trigger AS $$
BEGIN
    IF NEW.updates_days > 0 OR NEW.stripe_renewal_price_id <> '' THEN
        PERFORM feed_gating_lock(NEW.product_id);
        IF feed_gating_required(NEW.product_id) THEN
            RAISE EXCEPTION 'plans_feed_not_gated: plan with an update period or renewals on product % whose feeds are public', NEW.product_id
                USING ERRCODE = 'check_violation', CONSTRAINT = 'plans_feed_not_gated';
        END IF;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql VOLATILE;
DROP TRIGGER IF EXISTS plans_require_gated_feed ON plans;
CREATE TRIGGER plans_require_gated_feed
    BEFORE INSERT OR UPDATE OF updates_days, stripe_renewal_price_id, product_id ON plans
    FOR EACH ROW EXECUTE FUNCTION plans_require_gated_feed();

-- Fires when the product ends up release-capable with public feeds,
-- whether by switching the gate off or by changing a saas product
-- (no feed, no gate needed) into a desktop or hybrid one.
CREATE OR REPLACE FUNCTION products_keep_feed_gated() RETURNS trigger AS $$
BEGIN
    -- The instant the gate went up is what the wait before an update
    -- period may be sold is measured from, and a NULL there reads as
    -- "nothing to wait out". The application stamps it; a direct
    -- UPDATE by an operator or an older tool would not, so it is
    -- stamped here too — from the database clock, in the same
    -- statement that switches the gate on.
    IF TG_OP = 'INSERT' THEN
        IF NEW.feed_license_required AND NEW.feed_gated_at IS NULL THEN
            NEW.feed_gated_at := clock_timestamp();
        END IF;
        RETURN NEW;
    END IF;
    IF NEW.feed_license_required AND NOT OLD.feed_license_required
       AND NEW.feed_gated_at IS NOT DISTINCT FROM OLD.feed_gated_at THEN
        NEW.feed_gated_at := clock_timestamp();
    END IF;
    IF NEW.type IN ('desktop', 'hybrid') AND NOT NEW.feed_license_required
       AND (OLD.feed_license_required OR OLD.type NOT IN ('desktop', 'hybrid')) THEN
        PERFORM feed_gating_lock(NEW.id);
        IF EXISTS (SELECT 1 FROM plans WHERE product_id = NEW.id AND (updates_days > 0 OR stripe_renewal_price_id <> ''))
           OR EXISTS (SELECT 1 FROM licenses WHERE product_id = NEW.id AND updates_until IS NOT NULL) THEN
            RAISE EXCEPTION 'products_feed_gate_in_use: product % has plans or licenses with an update period', NEW.id
                USING ERRCODE = 'check_violation', CONSTRAINT = 'products_feed_gate_in_use';
        END IF;
    END IF;
    RETURN NEW;
END $$ LANGUAGE plpgsql VOLATILE;
DROP TRIGGER IF EXISTS products_keep_feed_gated ON products;
CREATE TRIGGER products_keep_feed_gated
    BEFORE INSERT OR UPDATE OF feed_license_required, type ON products
    FOR EACH ROW EXECUTE FUNCTION products_keep_feed_gated();

-- One row per paid renewal: the ledger the license's updates_until is
-- derived from. The checkout session makes fulfilment idempotent; the
-- payment intent lets a refund find the renewal it reverses. A NULL
-- updates_until is a renewal that changed nothing (the license had
-- updates for life when it was applied, or was refunded first).
CREATE TABLE IF NOT EXISTS license_renewals (
    id                         TEXT PRIMARY KEY,
    license_id                 TEXT NOT NULL REFERENCES licenses(id) ON DELETE CASCADE,
    stripe_checkout_session_id TEXT NOT NULL DEFAULT '',
    stripe_payment_intent_id   TEXT NOT NULL DEFAULT '',
    days                       INT NOT NULL,
    previous_updates_until     TIMESTAMPTZ,
    updates_until              TIMESTAMPTZ,
    refunded_at                TIMESTAMPTZ,
    -- Set when the license's period was reset outside the ledger (the
    -- license left or re-entered a perpetual plan, or was granted
    -- updates for life). A superseded renewal belongs to the previous
    -- period: its refund is recorded but no longer moves anything.
    superseded_at              TIMESTAMPTZ,
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_license_renewals_session
    ON license_renewals(stripe_checkout_session_id) WHERE stripe_checkout_session_id != '';
CREATE INDEX IF NOT EXISTS idx_license_renewals_payment_intent
    ON license_renewals(stripe_payment_intent_id) WHERE stripe_payment_intent_id != '';
CREATE INDEX IF NOT EXISTS idx_license_renewals_license ON license_renewals(license_id);

-- Reminder claims are leases, not records. The maintenance reminder
-- claims its (license, tag) row before sending; sent_at is set once
-- the mail went out. A claim whose sender died before that (crash,
-- forced restart) is taken over after the lease expires, so the
-- customer's only notice is not lost. Rows written by the older
-- reminders keep sent_at = the time they were recorded.
ALTER TABLE notifications ADD COLUMN IF NOT EXISTS claimed_at TIMESTAMPTZ;
ALTER TABLE notifications ALTER COLUMN sent_at DROP NOT NULL;
-- The queued mail points back at the reminder it settles. A mail that
-- exhausts its retries is where a reminder is actually lost: the
-- claim was closed when it was queued, so without this link the
-- hourly pass would skip that license for good, even after the mail
-- server came back.
ALTER TABLE email_queue ADD COLUMN IF NOT EXISTS notification_id TEXT NOT NULL DEFAULT '';
-- Who holds the mail right now. The claim is a lease, so a processor
-- that stalled past it finds the mail taken over when it comes back;
-- without a token its "sent" or "failed" would still land on the row
-- the new holder is working on, and the mail would go out twice.
ALTER TABLE email_queue ADD COLUMN IF NOT EXISTS claim_token TEXT NOT NULL DEFAULT '';
