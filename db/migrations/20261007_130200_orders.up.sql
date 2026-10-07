-- Commerce order ledger: orders, order line items, invoices.
-- All money columns are BIGINT minor units — never floating point.
CREATE TABLE IF NOT EXISTS orders (
    id                  TEXT PRIMARY KEY,
    order_number        TEXT NOT NULL UNIQUE,
    customer_email      TEXT NOT NULL,
    customer_name       TEXT NOT NULL DEFAULT '',
    -- Nullable on purpose and without FK: the ledger must outlive the
    -- license it sold (see the Order model).
    license_id          TEXT,
    currency            TEXT NOT NULL,
    subtotal_minor      BIGINT NOT NULL DEFAULT 0,
    discount_minor      BIGINT NOT NULL DEFAULT 0,
    tax_minor           BIGINT NOT NULL DEFAULT 0,
    total_minor         BIGINT NOT NULL DEFAULT 0,
    coupon_code         TEXT NOT NULL DEFAULT '',
    coupon_type         TEXT NOT NULL DEFAULT '',
    coupon_value_bps    BIGINT NOT NULL DEFAULT 0,
    coupon_value_minor  BIGINT NOT NULL DEFAULT 0,
    tax_jurisdiction    TEXT NOT NULL DEFAULT '',
    tax_basis_points    BIGINT NOT NULL DEFAULT 0,
    tax_inclusive       BOOLEAN NOT NULL DEFAULT false,
    status              TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'paid', 'failed', 'refunded')),
    payment_provider    TEXT NOT NULL DEFAULT '',
    external_id         TEXT NOT NULL DEFAULT '',
    -- NULL when the client sent none; the unique index only guards
    -- keys that were actually used (Postgres allows repeat NULLs).
    idempotency_key     TEXT UNIQUE,
    paid_at             TIMESTAMPTZ,
    refunded_at         TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_orders_customer_email ON orders(customer_email);
CREATE INDEX IF NOT EXISTS idx_orders_status ON orders(status);
CREATE INDEX IF NOT EXISTS idx_orders_external_id ON orders(external_id);

-- Order line items: the priced snapshot of one SKU on the order.
CREATE TABLE IF NOT EXISTS order_items (
    id                  TEXT PRIMARY KEY,
    order_id            TEXT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    sku                 TEXT NOT NULL DEFAULT '',
    product_id          TEXT NOT NULL DEFAULT '',
    plan_id             TEXT NOT NULL DEFAULT '',
    description         TEXT NOT NULL DEFAULT '',
    quantity            BIGINT NOT NULL DEFAULT 1,
    unit_amount_minor   BIGINT NOT NULL DEFAULT 0,
    line_subtotal_minor BIGINT NOT NULL DEFAULT 0,
    line_discount_minor BIGINT NOT NULL DEFAULT 0,
    line_tax_minor      BIGINT NOT NULL DEFAULT 0,
    line_total_minor    BIGINT NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_order_items_order ON order_items(order_id);

-- Invoices: the billing document derived from an order.
CREATE TABLE IF NOT EXISTS invoices (
    id              TEXT PRIMARY KEY,
    order_id        TEXT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    invoice_number  TEXT NOT NULL UNIQUE,
    status          TEXT NOT NULL DEFAULT 'draft'
        CHECK (status IN ('draft', 'open', 'paid', 'void')),
    currency        TEXT NOT NULL,
    subtotal_minor  BIGINT NOT NULL DEFAULT 0,
    discount_minor  BIGINT NOT NULL DEFAULT 0,
    tax_minor       BIGINT NOT NULL DEFAULT 0,
    total_minor     BIGINT NOT NULL DEFAULT 0,
    issued_at       TIMESTAMPTZ,
    due_at          TIMESTAMPTZ,
    paid_at         TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_invoices_order ON invoices(order_id);
