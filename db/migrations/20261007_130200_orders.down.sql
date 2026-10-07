-- Reverse: invoices
DROP INDEX IF EXISTS idx_invoices_order;
DROP TABLE IF EXISTS invoices;

-- Reverse: order items
DROP INDEX IF EXISTS idx_order_items_order;
DROP TABLE IF EXISTS order_items;

-- Reverse: orders
DROP INDEX IF EXISTS idx_orders_external_id;
DROP INDEX IF EXISTS idx_orders_status;
DROP INDEX IF EXISTS idx_orders_customer_email;
DROP TABLE IF EXISTS orders;
