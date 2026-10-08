package model

import (
	"regexp"
	"slices"
	"testing"
)

// The §8 vocabulary, spelled out independently of the constants: a
// constant renamed or dropped must fail here, not in production.
var wantPermissions = []string{
	"products.read", "products.create", "products.update", "products.delete",
	"plans.read", "plans.create", "plans.update", "plans.delete",
	"licenses.read", "licenses.create", "licenses.activate", "licenses.deactivate", "licenses.revoke",
	"orders.read", "orders.create", "orders.refund",
	"payments.read", "payments.manage",
	"customers.read", "customers.manage",
	"reports.read", "audit.read", "settings.manage",
}

// The permission vocabulary is closed and exactly the plan §8 set:
// 23 grants, no duplicates, each shaped family.action (the same
// shape the database CHECK pins), and each accepted by
// ValidPermission.
func TestAllPermissionsMatchesVocabulary(t *testing.T) {
	if len(AllPermissions) != 23 {
		t.Errorf("AllPermissions has %d entries, want 23: %v", len(AllPermissions), AllPermissions)
	}
	shape := regexp.MustCompile(`^[a-z_]+\.[a-z_]+$`)
	for _, want := range wantPermissions {
		if !slices.Contains(AllPermissions, want) {
			t.Errorf("AllPermissions is missing %q", want)
		}
		if !ValidPermission(want) {
			t.Errorf("ValidPermission(%q) = false, want true", want)
		}
		if !shape.MatchString(want) {
			t.Errorf("%q does not match %s (the database CHECK shape)", want, shape)
		}
	}
	for _, p := range AllPermissions {
		if !slices.Contains(wantPermissions, p) {
			t.Errorf("AllPermissions has unexpected entry %q", p)
		}
	}
	seen := map[string]bool{}
	for _, p := range AllPermissions {
		if seen[p] {
			t.Errorf("AllPermissions lists %q twice", p)
		}
		seen[p] = true
	}
}

// ValidPermission is the gate every write path runs: the vocabulary
// and nothing else. Notably the wildcard is NOT a storable grant
// (the database CHECK cannot hold it) even though the middleware
// honors it in a set.
func TestValidPermission(t *testing.T) {
	for _, p := range []string{
		"products.destroy", "products.read ", "PRODUCTS.READ", "",
		"products", "products.read.extra", "invoices.read", "*.*", "nonsense",
	} {
		if ValidPermission(p) {
			t.Errorf("ValidPermission(%q) = true, want false", p)
		}
	}
	if ValidPermission(WildcardPermission) {
		t.Error("ValidPermission(WildcardPermission) = true, want false: the wildcard is honored but not storable")
	}
}

// Every one of the 12 plan §8 roles expands to a non-empty set drawn
// from the vocabulary, and only the full-access bundles (owner,
// super admin) hold everything.
func TestExpandBuiltinRoleCoversPlanRoles(t *testing.T) {
	wantNames := []string{
		"owner", "super admin", "admin", "finance", "product manager",
		"license manager", "support", "developer", "reseller", "affiliate",
		"customer", "viewer",
	}
	if !slices.Equal(BuiltinRoleNames, wantNames) {
		t.Fatalf("BuiltinRoleNames = %v, want %v", BuiltinRoleNames, wantNames)
	}

	for _, name := range BuiltinRoleNames {
		perms := ExpandBuiltinRole(name)
		if len(perms) == 0 {
			t.Errorf("ExpandBuiltinRole(%q) is empty", name)
			continue
		}
		for _, p := range perms {
			if !ValidPermission(p) {
				t.Errorf("ExpandBuiltinRole(%q) grants non-vocabulary %q", name, p)
			}
		}
	}

	// Owner and Super Admin ⊇ everything.
	for _, name := range []string{"owner", "super admin"} {
		perms := ExpandBuiltinRole(name)
		for _, p := range AllPermissions {
			if !slices.Contains(perms, p) {
				t.Errorf("ExpandBuiltinRole(%q) is missing %q; owner/super admin must hold everything", name, p)
			}
		}
	}

	// Admin holds everything except orders.refund: refund authority
	// is reserved for owner / super admin.
	admin := ExpandBuiltinRole("admin")
	if slices.Contains(admin, PermOrdersRefund) {
		t.Error("admin holds orders.refund; it is reserved for owner / super admin")
	}
	if len(admin) != len(AllPermissions)-1 {
		t.Errorf("admin holds %d permissions, want %d (all minus orders.refund)", len(admin), len(AllPermissions)-1)
	}

	// Viewer is exactly the *.read surface.
	viewer := ExpandBuiltinRole("viewer")
	for _, p := range AllPermissions {
		want := len(p) > 5 && p[len(p)-5:] == ".read"
		if got := slices.Contains(viewer, p); got != want {
			t.Errorf("viewer holds %q = %v, want %v (viewer = all *.read)", p, got, want)
		}
	}
}

// The exact bundle for each role is the documented contract — pin
// every one of them, so a re-bundle cannot happen silently.
func TestExpandBuiltinRoleExactSets(t *testing.T) {
	cases := []struct {
		role  string
		perms []string
	}{
		{"finance", []string{
			"orders.read", "payments.read", "payments.manage", "reports.read",
		}},
		{"product manager", []string{
			"products.read", "products.create", "products.update", "products.delete",
			"plans.read", "plans.create", "plans.update", "plans.delete",
			"reports.read",
		}},
		{"license manager", []string{
			"licenses.read", "licenses.create", "licenses.activate", "licenses.deactivate", "licenses.revoke",
			"products.read", "plans.read", "customers.read",
		}},
		{"support", []string{
			"products.read", "plans.read",
			"licenses.read", "licenses.activate", "licenses.deactivate",
			"orders.read", "customers.read", "customers.manage",
		}},
		{"developer", []string{
			"products.read", "plans.read",
			"licenses.read", "licenses.create", "licenses.activate", "licenses.deactivate",
		}},
		{"reseller", []string{
			"products.read", "plans.read",
			"licenses.read", "licenses.create",
			"orders.read", "orders.create", "customers.read",
		}},
		{"affiliate", []string{
			"products.read", "plans.read", "orders.read", "reports.read",
		}},
		{"customer", []string{
			"products.read", "plans.read", "licenses.read", "orders.read",
		}},
		{"viewer", []string{
			"products.read", "plans.read", "licenses.read", "orders.read",
			"payments.read", "customers.read", "reports.read", "audit.read",
		}},
	}
	for _, tc := range cases {
		got := ExpandBuiltinRole(tc.role)
		if !slices.Equal(got, tc.perms) {
			t.Errorf("ExpandBuiltinRole(%q) = %v, want %v", tc.role, got, tc.perms)
		}
	}
}

// Lookup folds the name first — " Super ADMIN " is "super admin" —
// and an unknown name is nil, not an empty bundle that reads like
// "a role with no permissions". Results are copies: mutating one
// must not corrupt the next expansion.
func TestExpandBuiltinRoleLookupAndCopies(t *testing.T) {
	if got := ExpandBuiltinRole(" Super ADMIN "); !slices.Equal(got, ExpandBuiltinRole("super admin")) {
		t.Errorf("folded lookup = %v, want the super admin set", got)
	}
	if got := ExpandBuiltinRole("no such role"); got != nil {
		t.Errorf("ExpandBuiltinRole(unknown) = %v, want nil", got)
	}

	mine := ExpandBuiltinRole("customer")
	mine[0] = "products.delete"
	again := ExpandBuiltinRole("customer")
	if again[0] != "products.read" {
		t.Errorf("mutating a returned set changed the mapping: %v", again)
	}

	owner := ExpandBuiltinRole("owner")
	owner[0] = "corrupted"
	if ExpandBuiltinRole("owner")[0] == "corrupted" {
		t.Error("mutating the owner set changed AllPermissions backing storage")
	}
}

// Role names fold to one spelling: trimmed, lower-cased, whitespace
// collapsed. The unique index and every lookup rely on it.
func TestNormalizeRoleName(t *testing.T) {
	cases := map[string]string{
		"Super Admin":     "super admin",
		"  super  admin":  "super admin",
		"SUPER ADMIN":     "super admin",
		"Owner":           "owner",
		"Product Manager": "product manager",
		"\t Finance \n":   "finance",
		"":                "",
		"   ":             "",
	}
	for in, want := range cases {
		if got := NormalizeRoleName(in); got != want {
			t.Errorf("NormalizeRoleName(%q) = %q, want %q", in, got, want)
		}
	}
}
