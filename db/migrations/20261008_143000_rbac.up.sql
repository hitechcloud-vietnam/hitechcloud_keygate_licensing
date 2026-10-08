-- Advanced RBAC (plan §8 / Phase 8): custom roles as named
-- permission bundles, layered ON TOP of the existing users.role
-- ('owner' | 'admin' | 'user') and is_admin — never replacing them.
-- A user keeps their users.role row exactly as it is; a custom role
-- only ever ADDS granular permissions on top (the middleware grants
-- an is_admin session every permission as a backward-compat wildcard).
--
-- Three tables:
--   custom_roles           — the role definitions (named bundles)
--   custom_role_permissions — one row per (role, permission) grant
--   user_custom_roles      — one row per (user, role) assignment
--
-- Effective permissions of a user = UNION over all assigned roles.

CREATE TABLE IF NOT EXISTS custom_roles (
    id          TEXT PRIMARY KEY,
    -- The role's identity. Stored folded (trimmed, lower-cased,
    -- whitespace collapsed — see model.NormalizeRoleName) so one
    -- role cannot exist under several spellings ("Super Admin",
    -- "super  admin" and "SUPER ADMIN" are one role). UNIQUE: the
    -- name is how admins address a role, and 1..64 chars after
    -- folding (see model.NormalizeRoleName).
    name        TEXT NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 64),
    description TEXT,
    -- Seeded bundles (the 12 plan §8 roles below) are protected:
    -- they cannot be deleted, renamed or have their permission set
    -- replaced. Custom roles created by an admin are not protected.
    is_system   BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS custom_role_permissions (
    role_id    TEXT NOT NULL REFERENCES custom_roles(id) ON DELETE CASCADE,
    -- The §8 permission vocabulary, e.g. 'products.read'. The CHECK
    -- mirrors the shape every permission has (validated in Go by
    -- model.ValidPermission against the closed vocabulary — the
    -- database pins the shape, the application pins the meaning).
    permission TEXT NOT NULL CHECK (permission ~ '^[a-z_]+\.[a-z_]+$'),
    PRIMARY KEY (role_id, permission)
);

CREATE TABLE IF NOT EXISTS user_custom_roles (
    -- CASCADE both sides: removing a user or a role drops its
    -- assignments; a deletion can never be blocked by an assignment
    -- and an assignment can never outlive either side.
    user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role_id    TEXT NOT NULL REFERENCES custom_roles(id) ON DELETE CASCADE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, role_id)
);
-- The primary key's (user_id, …) prefix serves "roles of a user";
-- the role-side lookup ("who holds this role", and the in-use check
-- before a delete) gets nothing from it — mirror of the
-- product_categories note.
CREATE INDEX IF NOT EXISTS idx_user_custom_roles_role ON user_custom_roles (role_id);

-- ─── Seeded built-in bundles (plan §8) ───
--
-- The 12 plan roles, as named permission bundles with is_system =
-- TRUE. The canonical mapping is model.ExpandBuiltinRole (documented
-- there as a table); a store test pins these rows against it, so the
-- two cannot drift. Ids are fixed and readable on purpose: ops can
-- find them without a lookup, and ON CONFLICT DO NOTHING keeps the
-- whole seed idempotent.

INSERT INTO custom_roles (id, name, description, is_system) VALUES
    ('rbac-owner',           'owner',           'Full control of everything (plan §8). Mirrors users.role = owner.', TRUE),
    ('rbac-super-admin',     'super admin',     'Full control of everything (plan §8). Mirrors users.role = admin + is_admin.', TRUE),
    ('rbac-admin',           'admin',           'Everything except orders.refund (plan §8).', TRUE),
    ('rbac-finance',         'finance',         'Money surface: orders read, payments management, reports (plan §8).', TRUE),
    ('rbac-product-manager', 'product manager', 'Products and plans, plus reports (plan §8).', TRUE),
    ('rbac-license-manager', 'license manager', 'Full license lifecycle, with read-only context (plan §8).', TRUE),
    ('rbac-support',         'support',         'Customer and license support actions (plan §8).', TRUE),
    ('rbac-developer',       'developer',       'Integration surface: catalog reads and license lifecycle (plan §8).', TRUE),
    ('rbac-reseller',        'reseller',        'Resells: reads the catalog, creates orders and licenses (plan §8).', TRUE),
    ('rbac-affiliate',       'affiliate',       'Tracks referred orders and reports (plan §8).', TRUE),
    ('rbac-customer',        'customer',        'End-customer reads (plan §8).', TRUE),
    ('rbac-viewer',          'viewer',          'Read-only across the whole surface (plan §8).', TRUE)
ON CONFLICT (id) DO NOTHING;

-- owner / super admin — the entire vocabulary (23 permissions).
INSERT INTO custom_role_permissions (role_id, permission)
SELECT 'rbac-owner', u FROM unnest(ARRAY[
    'products.read', 'products.create', 'products.update', 'products.delete',
    'plans.read', 'plans.create', 'plans.update', 'plans.delete',
    'licenses.read', 'licenses.create', 'licenses.activate', 'licenses.deactivate', 'licenses.revoke',
    'orders.read', 'orders.create', 'orders.refund',
    'payments.read', 'payments.manage',
    'customers.read', 'customers.manage',
    'reports.read', 'audit.read', 'settings.manage'
]::text[]) AS u
ON CONFLICT DO NOTHING;

INSERT INTO custom_role_permissions (role_id, permission)
SELECT 'rbac-super-admin', u FROM unnest(ARRAY[
    'products.read', 'products.create', 'products.update', 'products.delete',
    'plans.read', 'plans.create', 'plans.update', 'plans.delete',
    'licenses.read', 'licenses.create', 'licenses.activate', 'licenses.deactivate', 'licenses.revoke',
    'orders.read', 'orders.create', 'orders.refund',
    'payments.read', 'payments.manage',
    'customers.read', 'customers.manage',
    'reports.read', 'audit.read', 'settings.manage'
]::text[]) AS u
ON CONFLICT DO NOTHING;

-- admin — everything except orders.refund: refund authority stays
-- with owner / super admin (and the finance bundle carries payments,
-- not refunds).
INSERT INTO custom_role_permissions (role_id, permission)
SELECT 'rbac-admin', u FROM unnest(ARRAY[
    'products.read', 'products.create', 'products.update', 'products.delete',
    'plans.read', 'plans.create', 'plans.update', 'plans.delete',
    'licenses.read', 'licenses.create', 'licenses.activate', 'licenses.deactivate', 'licenses.revoke',
    'orders.read', 'orders.create',
    'payments.read', 'payments.manage',
    'customers.read', 'customers.manage',
    'reports.read', 'audit.read', 'settings.manage'
]::text[]) AS u
ON CONFLICT DO NOTHING;

-- finance — orders.read / payments.* / reports.read, and nothing
-- outside the §8 vocabulary (there is no invoices.* family to grant).
INSERT INTO custom_role_permissions (role_id, permission)
SELECT 'rbac-finance', u FROM unnest(ARRAY[
    'orders.read', 'payments.read', 'payments.manage', 'reports.read'
]::text[]) AS u
ON CONFLICT DO NOTHING;

-- product manager — the products and plans families in full, plus
-- reports.
INSERT INTO custom_role_permissions (role_id, permission)
SELECT 'rbac-product-manager', u FROM unnest(ARRAY[
    'products.read', 'products.create', 'products.update', 'products.delete',
    'plans.read', 'plans.create', 'plans.update', 'plans.delete',
    'reports.read'
]::text[]) AS u
ON CONFLICT DO NOTHING;

-- license manager — the licenses family in full, with read-only
-- context (products, plans, customers) around it.
INSERT INTO custom_role_permissions (role_id, permission)
SELECT 'rbac-license-manager', u FROM unnest(ARRAY[
    'licenses.read', 'licenses.create', 'licenses.activate', 'licenses.deactivate', 'licenses.revoke',
    'products.read', 'plans.read', 'customers.read'
]::text[]) AS u
ON CONFLICT DO NOTHING;

-- support — customer help: read the catalog and licenses, activate /
-- deactivate (never revoke), and manage customers.
INSERT INTO custom_role_permissions (role_id, permission)
SELECT 'rbac-support', u FROM unnest(ARRAY[
    'products.read', 'plans.read',
    'licenses.read', 'licenses.activate', 'licenses.deactivate',
    'orders.read', 'customers.read', 'customers.manage'
]::text[]) AS u
ON CONFLICT DO NOTHING;

-- developer — integration surface: catalog reads and the license
-- lifecycle (no revoke).
INSERT INTO custom_role_permissions (role_id, permission)
SELECT 'rbac-developer', u FROM unnest(ARRAY[
    'products.read', 'plans.read',
    'licenses.read', 'licenses.create', 'licenses.activate', 'licenses.deactivate'
]::text[]) AS u
ON CONFLICT DO NOTHING;

-- reseller — reads the catalog, creates orders and licenses for
-- their own customers, reads customers.
INSERT INTO custom_role_permissions (role_id, permission)
SELECT 'rbac-reseller', u FROM unnest(ARRAY[
    'products.read', 'plans.read',
    'licenses.read', 'licenses.create',
    'orders.read', 'orders.create', 'customers.read'
]::text[]) AS u
ON CONFLICT DO NOTHING;

-- affiliate — reads the catalog and the orders / reports they are
-- measured on.
INSERT INTO custom_role_permissions (role_id, permission)
SELECT 'rbac-affiliate', u FROM unnest(ARRAY[
    'products.read', 'plans.read', 'orders.read', 'reports.read'
]::text[]) AS u
ON CONFLICT DO NOTHING;

-- customer — end-customer reads.
INSERT INTO custom_role_permissions (role_id, permission)
SELECT 'rbac-customer', u FROM unnest(ARRAY[
    'products.read', 'plans.read', 'licenses.read', 'orders.read'
]::text[]) AS u
ON CONFLICT DO NOTHING;

-- viewer — every *.read permission in the vocabulary.
INSERT INTO custom_role_permissions (role_id, permission)
SELECT 'rbac-viewer', u FROM unnest(ARRAY[
    'products.read', 'plans.read', 'licenses.read', 'orders.read',
    'payments.read', 'customers.read', 'reports.read', 'audit.read'
]::text[]) AS u
ON CONFLICT DO NOTHING;
