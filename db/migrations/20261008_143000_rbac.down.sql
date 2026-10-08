-- Reverse: advanced RBAC. Assignments first (their foreign keys
-- point at users and custom_roles), then permissions (pointing at
-- custom_roles), then the roles themselves — which also takes the
-- seeded built-in bundles with it. Dropping a table drops its
-- indexes; the explicit drops stay quiet on a database that never had
-- them. users.role and is_admin are untouched: this migration only
-- ever added a layer on top of them.
DROP INDEX IF EXISTS idx_user_custom_roles_role;
DROP TABLE IF EXISTS user_custom_roles;
DROP TABLE IF EXISTS custom_role_permissions;
DROP TABLE IF EXISTS custom_roles;
