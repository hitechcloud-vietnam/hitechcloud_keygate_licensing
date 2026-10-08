-- Reverse of the PO / invoice workflow. The order billing columns
-- drop with their CHECK constraints; the invoice vocabulary is
-- narrowed back to the original four strings and the two state
-- timestamps go with their states.
--
-- The narrowing refuses to run while any invoice sits in one of the
-- added states ('uncollectible', 'refunded') — reconcile those rows
-- first (or accept that this down migration needs that cleanup). No
-- rows are rewritten here: a down migration reverses schema, it does
-- not invent history.

ALTER TABLE orders DROP COLUMN IF EXISTS billing_name;
ALTER TABLE orders DROP COLUMN IF EXISTS billing_company;
ALTER TABLE orders DROP COLUMN IF EXISTS billing_address_line1;
ALTER TABLE orders DROP COLUMN IF EXISTS billing_address_line2;
ALTER TABLE orders DROP COLUMN IF EXISTS billing_city;
ALTER TABLE orders DROP COLUMN IF EXISTS billing_region;
ALTER TABLE orders DROP COLUMN IF EXISTS billing_postal_code;
ALTER TABLE orders DROP COLUMN IF EXISTS billing_country;
ALTER TABLE orders DROP COLUMN IF EXISTS customer_tax_id;
ALTER TABLE orders DROP COLUMN IF EXISTS po_number;
ALTER TABLE orders DROP COLUMN IF EXISTS billing_email;

ALTER TABLE invoices DROP COLUMN IF EXISTS voided_at;
ALTER TABLE invoices DROP COLUMN IF EXISTS uncollectible_at;

ALTER TABLE invoices DROP CONSTRAINT IF EXISTS invoices_status_check;
ALTER TABLE invoices ADD CONSTRAINT invoices_status_check
    CHECK (status IN ('draft', 'open', 'paid', 'void'));
