-- Purchase-order / invoice workflow (plan §26 Invoices / Phase 8):
-- billing details on orders and the richer invoice states.
--
-- Everything here is additive and nullable. An order created before
-- this migration — or written by a flow that never sets billing
-- details, like the Stripe ledger — keeps working with every new
-- column NULL. The columns are nullable TEXT (not NOT NULL DEFAULT '')
-- because "no billing details" is the normal state of most orders and
-- NULL is the one representation of it: the store writes an empty
-- value as NULL (orders.UpdateOrderBilling), and the API serializes
-- NULL away.

ALTER TABLE orders ADD COLUMN IF NOT EXISTS billing_name          TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS billing_company       TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS billing_address_line1 TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS billing_address_line2 TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS billing_city          TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS billing_region        TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS billing_postal_code   TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS billing_country       TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS customer_tax_id       TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS po_number             TEXT;
ALTER TABLE orders ADD COLUMN IF NOT EXISTS billing_email         TEXT;

-- The shapes the PATCH /admin/orders/:id/billing endpoint validates
-- on write are backed here too, in the style of the resellers
-- migration: a country is an ISO 3166-1 alpha-2 code, a tax id is
-- 8–20 alphanumerics/dashes. A column never set stays NULL and passes
-- both. Lengths for the free-text fields are enforced on write only —
-- they are presentation limits, not ledger invariants.
ALTER TABLE orders ADD CONSTRAINT orders_billing_country_check
    CHECK (billing_country IS NULL OR billing_country ~ '^[A-Za-z]{2}$');
ALTER TABLE orders ADD CONSTRAINT orders_customer_tax_id_check
    CHECK (customer_tax_id IS NULL OR customer_tax_id ~ '^[A-Za-z0-9-]{8,20}$');

-- Invoice state enrichment: the instants the two added states stamp
-- and the widened status vocabulary. The existing status STRINGS are
-- untouched — the CHECK below is widened, never rewritten, so rows
-- written before this migration keep scanning unchanged. 'draft',
-- 'open', 'paid', 'void' mean exactly what they meant; 'uncollectible'
-- (open given up on, recoverable) and 'refunded' (paid, money
-- returned) extend the vocabulary the plan calls for. The transition
-- rules live in code (model.CanTransitionInvoice) — the CHECK only
-- pins the vocabulary.
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS voided_at        TIMESTAMPTZ;
ALTER TABLE invoices ADD COLUMN IF NOT EXISTS uncollectible_at TIMESTAMPTZ;

ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_status_check;
ALTER TABLE invoices ADD CONSTRAINT invoices_status_check
    CHECK (status IN ('draft', 'open', 'paid', 'void', 'uncollectible', 'refunded'));
