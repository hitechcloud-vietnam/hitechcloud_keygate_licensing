package model

import (
	"slices"
	"strings"
	"time"

	"github.com/uptrace/bun"
)

// ─── Advanced RBAC (plan §8) ───
//
// Custom roles are named bundles of granular permissions, layered ON
// TOP of the existing users.role ('owner' | 'admin' | 'user') and
// is_admin — never replacing them. A user's effective permission set
// is the UNION of every custom role they hold; a session whose
// is_admin is true is additionally treated as holding every
// permission (the backward-compat wildcard — see
// middleware.RequirePermission).
//
// The permission vocabulary is CLOSED: only the constants below are
// storeable, and ValidPermission is the single gate. The database
// CHECK pins the SHAPE ("family.action"); this file pins the
// MEANING.
//
// ─── Built-in role → permission mapping ───
//
// The 12 plan §8 roles, as named bundles. ExpandBuiltinRole is the
// canonical implementation; db/migrations/20261008_143000_rbac.up.sql
// seeds the same bundles as is_system rows and a store test pins the
// two together. "all" is the full vocabulary (23 permissions).
//
//	owner            → all
//	super admin      → all
//	admin            → all minus orders.refund
//	finance          → orders.read, payments.read, payments.manage, reports.read
//	product manager  → products.*, plans.*, reports.read
//	license manager  → licenses.*, products.read, plans.read, customers.read
//	support          → products.read, plans.read, licenses.read,
//	                   licenses.activate, licenses.deactivate,
//	                   orders.read, customers.read, customers.manage
//	developer        → products.read, plans.read, licenses.read,
//	                   licenses.create, licenses.activate, licenses.deactivate
//	reseller         → products.read, plans.read, licenses.read,
//	                   licenses.create, orders.read, orders.create, customers.read
//	affiliate        → products.read, plans.read, orders.read, reports.read
//	customer         → products.read, plans.read, licenses.read, orders.read
//	viewer           → every *.read (products.read, plans.read, licenses.read,
//	                   orders.read, payments.read, customers.read,
//	                   reports.read, audit.read)
//
// Two rules the table above follows, so it can be checked at a
// glance: refund authority (orders.refund) is reserved for owner,
// super admin and admin; and there is no invoices.* family in the
// §8 vocabulary, so no bundle grants one ("finance gets the
// money surface that exists").

// The §8 permission vocabulary. One constant per grant; the verb is
// always the last dot-segment.
const (
	PermProductsRead   = "products.read"
	PermProductsCreate = "products.create"
	PermProductsUpdate = "products.update"
	PermProductsDelete = "products.delete"

	PermPlansRead   = "plans.read"
	PermPlansCreate = "plans.create"
	PermPlansUpdate = "plans.update"
	PermPlansDelete = "plans.delete"

	PermLicensesRead       = "licenses.read"
	PermLicensesCreate     = "licenses.create"
	PermLicensesActivate   = "licenses.activate"
	PermLicensesDeactivate = "licenses.deactivate"
	PermLicensesRevoke     = "licenses.revoke"

	PermOrdersRead   = "orders.read"
	PermOrdersCreate = "orders.create"
	PermOrdersRefund = "orders.refund"

	PermPaymentsRead   = "payments.read"
	PermPaymentsManage = "payments.manage"

	PermCustomersRead   = "customers.read"
	PermCustomersManage = "customers.manage"

	PermReportsRead    = "reports.read"
	PermAuditRead      = "audit.read"
	PermSettingsManage = "settings.manage"
)

// WildcardPermission is the reserved grant that stands for every
// permission. It is honored wherever a permission set is consulted
// (middleware.RequirePermission, PermissionsForUser unions) but is
// NOT part of AllPermissions and NOT accepted by ValidPermission:
// the database CHECK cannot store it and no built-in bundle emits
// it. It exists so a future backend can hand out one grant for
// "everything" without a vocabulary change.
const WildcardPermission = "*.*"

// AllPermissions is the whole §8 vocabulary, in plan order. It is
// the closed set every permission check, dropdown and seed draws
// from.
var AllPermissions = []string{
	PermProductsRead, PermProductsCreate, PermProductsUpdate, PermProductsDelete,
	PermPlansRead, PermPlansCreate, PermPlansUpdate, PermPlansDelete,
	PermLicensesRead, PermLicensesCreate, PermLicensesActivate, PermLicensesDeactivate, PermLicensesRevoke,
	PermOrdersRead, PermOrdersCreate, PermOrdersRefund,
	PermPaymentsRead, PermPaymentsManage,
	PermCustomersRead, PermCustomersManage,
	PermReportsRead, PermAuditRead, PermSettingsManage,
}

// BuiltinRoleNames are the 12 plan §8 roles, in plan order, in their
// folded (stored) form. Every one is seeded with is_system = true and
// protected from mutation; see the mapping table at the top of this
// file.
var BuiltinRoleNames = []string{
	"owner", "super admin", "admin", "finance", "product manager",
	"license manager", "support", "developer", "reseller", "affiliate",
	"customer", "viewer",
}

// ValidPermission reports whether p is a member of the closed §8
// vocabulary. The wildcard is deliberately not a member (see
// WildcardPermission); this is the gate every write path runs before
// a permission reaches the database.
func ValidPermission(p string) bool {
	return slices.Contains(AllPermissions, p)
}

// NormalizeRoleName folds a role name into its stored canonical
// form: trimmed, lower-cased, every run of whitespace collapsed to a
// single space. Folding rather than refusing means one role cannot
// exist under several spellings ("Super Admin", "super  admin" and
// "SUPER ADMIN" are one role), which is what the unique index on
// custom_roles.name relies on. The result is validated separately
// (1..64 chars) — this only folds.
func NormalizeRoleName(name string) string {
	return strings.Join(strings.Fields(strings.ToLower(name)), " ")
}

// ExpandBuiltinRole maps one of the 12 plan §8 role names to its
// permission set (see the mapping table at the top of this file).
// The name is folded first, so " Super ADMIN " finds "super admin".
// The result is a fresh slice every call — mutating it cannot corrupt
// later expansions. An unknown name returns nil: the built-in
// vocabulary is closed, and anything else is a custom role whose
// permissions live in the database, not here.
func ExpandBuiltinRole(name string) []string {
	switch NormalizeRoleName(name) {
	case "owner", "super admin":
		return slices.Clone(AllPermissions)
	case "admin":
		// All but orders.refund: refund authority is reserved for
		// owner / super admin.
		return slices.DeleteFunc(slices.Clone(AllPermissions), func(p string) bool {
			return p == PermOrdersRefund
		})
	case "finance":
		return []string{PermOrdersRead, PermPaymentsRead, PermPaymentsManage, PermReportsRead}
	case "product manager":
		return []string{
			PermProductsRead, PermProductsCreate, PermProductsUpdate, PermProductsDelete,
			PermPlansRead, PermPlansCreate, PermPlansUpdate, PermPlansDelete,
			PermReportsRead,
		}
	case "license manager":
		return []string{
			PermLicensesRead, PermLicensesCreate, PermLicensesActivate, PermLicensesDeactivate, PermLicensesRevoke,
			PermProductsRead, PermPlansRead, PermCustomersRead,
		}
	case "support":
		return []string{
			PermProductsRead, PermPlansRead,
			PermLicensesRead, PermLicensesActivate, PermLicensesDeactivate,
			PermOrdersRead, PermCustomersRead, PermCustomersManage,
		}
	case "developer":
		return []string{
			PermProductsRead, PermPlansRead,
			PermLicensesRead, PermLicensesCreate, PermLicensesActivate, PermLicensesDeactivate,
		}
	case "reseller":
		return []string{
			PermProductsRead, PermPlansRead,
			PermLicensesRead, PermLicensesCreate,
			PermOrdersRead, PermOrdersCreate, PermCustomersRead,
		}
	case "affiliate":
		return []string{PermProductsRead, PermPlansRead, PermOrdersRead, PermReportsRead}
	case "customer":
		return []string{PermProductsRead, PermPlansRead, PermLicensesRead, PermOrdersRead}
	case "viewer":
		return []string{
			PermProductsRead, PermPlansRead, PermLicensesRead, PermOrdersRead,
			PermPaymentsRead, PermCustomersRead, PermReportsRead, PermAuditRead,
		}
	}
	return nil
}

// ─── Tables ───

// Role is a custom role: a named bundle of permissions
// (custom_roles). IsSystem marks the 12 seeded plan §8 bundles —
// those are protected: they cannot be deleted, renamed or have their
// permission set replaced (store.ErrSystemRoleProtected). Name is
// stored folded, see NormalizeRoleName.
type Role struct {
	bun.BaseModel `bun:"table:custom_roles"`

	ID          string    `bun:",pk" json:"id"`
	Name        string    `bun:",notnull,unique" json:"name"`
	Description string    `bun:",nullzero" json:"description"`
	IsSystem    bool      `bun:",notnull,default:false" json:"is_system"`
	CreatedAt   time.Time `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt   time.Time `bun:",nullzero,default:now()" json:"updated_at"`
}

// RolePermission is one (role, permission) grant
// (custom_role_permissions). The composite PRIMARY KEY is the unique
// pair; both columns are the whole row. The role side cascades on
// delete — dropping a role drops its grants.
type RolePermission struct {
	bun.BaseModel `bun:"table:custom_role_permissions"`

	RoleID     string `bun:",pk" json:"role_id"`
	Permission string `bun:",pk" json:"permission"`
}

// UserCustomRole is one (user, role) assignment
// (user_custom_roles). A user may hold several roles; the effective
// permission set is the union over all of them. Both sides cascade
// on delete — the assignments never outlive either side and never
// block a deletion. The pair is the primary key, so assigning a role
// twice is one row.
type UserCustomRole struct {
	bun.BaseModel `bun:"table:user_custom_roles"`

	UserID    string    `bun:",pk" json:"user_id"`
	RoleID    string    `bun:",pk" json:"role_id"`
	CreatedAt time.Time `bun:",nullzero,default:now()" json:"created_at"`
}
