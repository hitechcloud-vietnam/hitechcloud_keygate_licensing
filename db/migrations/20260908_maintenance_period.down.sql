-- Rolling back this migration drops what the feature sold: the
-- cutoffs customers paid for, the plan fields that price them, and
-- the renewal ledger - and a binary that predates the feature serves
-- every release to every license. It is refused while any of the
-- three is still in use, so a rollback cannot quietly hand out what
-- was never sold, keep selling a period the old binary ignores, or
-- delete payment history nobody decided to delete. Clearing them is
-- deliberate, in this order: give those licenses updates for life,
-- take the period and renewal price off the plans, export and delete
-- the renewal ledger, and only then roll back.
--
-- Before any of this: switching the maintenance features off stops
-- new bounded licences and new renewal checkouts, but it does not
-- reach the checkout sessions already open in Stripe. One of those
-- paid afterwards still fulfils, and puts a cutoff or a renewal back
-- into the tables this rollback wants empty. Wait them out (a Stripe
-- session expires within 24 hours) or expire them in the dashboard,
-- then do the cleanup the guard below asks for.
--
-- This guards the schema. A binary rolled back on its own, with this
-- migration left applied, cannot be stopped from here — it never runs
-- this file and its feed handlers do not know these columns. Gate
-- that in the deployment: roll back the schema too, or keep every
-- replica on a build that understands update periods.
BEGIN;

-- The checks below decide whether anything is still in use, and the
-- drops act on that answer — so nothing may be written in between. A
-- fulfilment does not consult the maintenance switch, so a Stripe
-- session paid during this migration would otherwise commit a cutoff
-- or a renewal that the next statement destroys. The locks make the
-- count and the drops one decision; they also make any such
-- fulfilment wait, and it then fails on the missing columns rather
-- than losing a paid entitlement quietly.
-- processed_events is in the list for the delayed-payment markers
-- counted below: no licence is written for one of those yet, so the
-- other three locks would not stop a marker appearing between the
-- count and the drops — and that marker is a payment still on its way.
LOCK TABLE licenses, plans, license_renewals, processed_events IN ACCESS EXCLUSIVE MODE;

DO $$
DECLARE cutoffs INT; selling INT; ledger INT; pending INT;
BEGIN
    SELECT count(*) INTO cutoffs FROM licenses WHERE updates_until IS NOT NULL;
    SELECT count(*) INTO selling FROM plans
     WHERE updates_days > 0 OR renewal_days > 0 OR stripe_renewal_price_id <> '';
    SELECT count(*) INTO ledger FROM license_renewals;
    -- A checkout that completed on a delayed payment method (bank
    -- debit, voucher) is not an open session that expires on its own:
    -- the money can arrive days later, and this marker is how it is
    -- found again. Rolling back while one is outstanding means that
    -- payment lands on a build that cannot fulfil it.
    SELECT count(*) INTO pending FROM processed_events WHERE provider = 'stripe_pending_session';
    IF cutoffs > 0 OR selling > 0 OR ledger > 0 OR pending > 0 THEN
        RAISE EXCEPTION 'maintenance_period rollback refused: % license(s) with an update cutoff, % plan(s) still selling an update period or renewals, % renewal(s) on record, % checkout(s) awaiting a delayed payment',
            cutoffs, selling, ledger, pending
            USING HINT = 'this is a deliberate cleanup: settle, cancel or refund the pending checkouts in Stripe (they are listed in processed_events where provider = ''stripe_pending_session''), give those licenses updates for life, take the period and renewal price off the plans, then export and delete license_renewals - the rollback drops that payment history and re-upgrading cannot bring it back';
    END IF;
END $$;

DROP TRIGGER IF EXISTS licenses_require_gated_feed ON licenses;
DROP FUNCTION IF EXISTS licenses_require_gated_feed();
DROP TRIGGER IF EXISTS products_keep_feed_gated ON products;
DROP FUNCTION IF EXISTS products_keep_feed_gated();
DROP TRIGGER IF EXISTS plans_require_gated_feed ON plans;
DROP FUNCTION IF EXISTS plans_require_gated_feed();
DROP FUNCTION IF EXISTS feed_gating_required(TEXT);
DROP FUNCTION IF EXISTS feed_gating_lock(TEXT);
DROP TRIGGER IF EXISTS licenses_init_updates_until ON licenses;
DROP FUNCTION IF EXISTS licenses_init_updates_until();
DROP TRIGGER IF EXISTS plans_record_update_terms ON plans;
DROP FUNCTION IF EXISTS plans_record_update_terms();
DROP TRIGGER IF EXISTS plans_prices_disjoint ON plans;
DROP FUNCTION IF EXISTS plans_prices_disjoint();
DROP TABLE IF EXISTS license_renewals;
ALTER TABLE products DROP COLUMN IF EXISTS feed_gated_at;
ALTER TABLE products DROP COLUMN IF EXISTS feed_license_required;
DROP INDEX IF EXISTS idx_licenses_updates_until;
ALTER TABLE licenses DROP COLUMN IF EXISTS updates_terms_set;
ALTER TABLE licenses DROP COLUMN IF EXISTS updates_until;
DROP TABLE IF EXISTS plan_update_terms;
ALTER TABLE plans DROP COLUMN IF EXISTS stripe_renewal_price_id;
ALTER TABLE plans DROP COLUMN IF EXISTS renewal_days;
ALTER TABLE plans DROP COLUMN IF EXISTS updates_days;
-- A row with sent_at NULL is an open claim: the reminder was leased
-- but never queued, so nothing reached the customer. Restoring the
-- NOT NULL by filling sent_at would tell the older reminder code the
-- mail went out and the notice would be lost for good. Drop those
-- rows instead; the old code then re-sends, which is what it does
-- after any crash in its own send.
ALTER TABLE email_queue DROP COLUMN IF EXISTS claim_token;
ALTER TABLE email_queue DROP COLUMN IF EXISTS notification_id;
DELETE FROM notifications WHERE sent_at IS NULL;
ALTER TABLE notifications ALTER COLUMN sent_at SET NOT NULL;
ALTER TABLE notifications DROP COLUMN IF EXISTS claimed_at;

COMMIT;
