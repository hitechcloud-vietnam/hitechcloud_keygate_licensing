package store_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// TestRBACTableAliases pins the table aliases Bun generates for the
// RBAC models. Bun aliases a model by the snake_case of the STRUCT
// name, NOT by the table name (custom_roles table, model.Role ->
// AS "role"). Hand-written WHERE/ORDER BY qualifiers must use the
// struct-name alias. Same regression pin as
// TestBunDefaultTableAliases, for the RBAC slice.
func TestRBACTableAliases(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	cases := []struct {
		model     interface{}
		wantAlias string
	}{
		{(*model.Role)(nil), `"role"`},
		{(*model.RolePermission)(nil), `"role_permission"`},
		{(*model.UserCustomRole)(nil), `"user_custom_role"`},
	}
	for _, tc := range cases {
		raw, err := db.NewSelect().Model(tc.model).AppendQuery(db.QueryGen(), nil)
		if err != nil {
			t.Fatalf("build query for %T: %v", tc.model, err)
		}
		if sqlText := string(raw); !strings.Contains(sqlText, "AS "+tc.wantAlias) {
			t.Errorf("generated SQL for %T does not contain %q; got:\n%s", tc.model, "AS "+tc.wantAlias, sqlText)
		}
	}
}

// The sentinels answer their questions in either spelling: the
// sentinel the store returns, or a raw driver error that reached the
// caller without passing through it.
func TestRBACSentinels(t *testing.T) {
	if !store.IsRoleNameConflict(store.ErrRoleNameTaken) {
		t.Error("IsRoleNameConflict(ErrRoleNameTaken) = false")
	}
	if store.IsRoleNameConflict(errors.New("connection reset")) {
		t.Error("IsRoleNameConflict(other) = true")
	}
	if store.IsRoleNameConflict(nil) {
		t.Error("IsRoleNameConflict(nil) = true")
	}
	if !store.IsRoleProtected(store.ErrSystemRoleProtected) {
		t.Error("IsRoleProtected(ErrSystemRoleProtected) = false")
	}
	if store.IsRoleProtected(store.ErrRoleInUse) {
		t.Error("IsRoleProtected(ErrRoleInUse) = true")
	}
	if !store.IsRoleInUseConflict(store.ErrRoleInUse) {
		t.Error("IsRoleInUseConflict(ErrRoleInUse) = false")
	}
	if store.IsRoleInUseConflict(store.ErrSystemRoleProtected) {
		t.Error("IsRoleInUseConflict(ErrSystemRoleProtected) = true")
	}
}

// newRBACTestUser creates a throwaway user for assignment tests.
func newRBACTestUser(t *testing.T, s *store.Store, ctx context.Context, tag string) *model.User {
	t.Helper()
	u := &model.User{Email: "rbac-" + tag + "@example.com", Name: "RBAC " + tag}
	if err := s.UpsertUser(ctx, u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.DB.NewRaw("DELETE FROM user_custom_roles WHERE user_id = ?", u.ID).Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM users WHERE id = ?", u.ID).Exec(ctx)
	})
	return u
}

// A role round-trips: folded name, deduped permission set, rename
// and re-permission through UpdateRole, nil-perms keeping the set.
func TestStoreRBACRoleLifecycle(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	role := &model.Role{Name: "  Ops  Team ", Description: "day to day"}
	t.Cleanup(func() { _ = s.DeleteRole(ctx, role.ID) })
	if err := s.CreateRole(ctx, role, []string{
		model.PermProductsRead, model.PermProductsRead, model.PermProductsCreate,
	}); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if role.ID == "" {
		t.Fatal("CreateRole did not set the id")
	}
	if role.Name != "ops team" {
		t.Errorf("Name = %q, want the folded %q", role.Name, "ops team")
	}

	perms, err := s.PermissionsForRole(ctx, role.ID)
	if err != nil {
		t.Fatalf("PermissionsForRole: %v", err)
	}
	if len(perms) != 2 || perms[0] != model.PermProductsCreate || perms[1] != model.PermProductsRead {
		t.Errorf("permissions = %v, want the two grants, deduped and sorted", perms)
	}

	byName, err := s.FindRoleByName(ctx, " OPS   TEAM ") // lookup folds too
	if err != nil || byName.ID != role.ID {
		t.Fatalf("FindRoleByName = %+v, %v; want the created role", byName, err)
	}

	// Rename + replace the set in one call.
	role.Name = "Ops Tier 1"
	role.Description = "renamed"
	if err := s.UpdateRole(ctx, role, []string{model.PermOrdersRead}); err != nil {
		t.Fatalf("UpdateRole: %v", err)
	}
	perms, _ = s.PermissionsForRole(ctx, role.ID)
	if len(perms) != 1 || perms[0] != model.PermOrdersRead {
		t.Errorf("permissions after replace = %v, want [orders.read]", perms)
	}

	// A nil set leaves the stored set alone.
	role.Name = "ops tier 1"
	if err := s.UpdateRole(ctx, role, nil); err != nil {
		t.Fatalf("UpdateRole (nil perms): %v", err)
	}
	perms, _ = s.PermissionsForRole(ctx, role.ID)
	if len(perms) != 1 || perms[0] != model.PermOrdersRead {
		t.Errorf("permissions after rename-only = %v, want the set kept", perms)
	}

	// Wholly unknown ids are 404 material: sql.ErrNoRows.
	if _, err := s.FindRoleByID(ctx, "no-such-role"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("FindRoleByID(unknown) err = %v, want sql.ErrNoRows", err)
	}
	if err := s.DeleteRole(ctx, "no-such-role"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("DeleteRole(unknown) err = %v, want sql.ErrNoRows", err)
	}

	if err := s.DeleteRole(ctx, role.ID); err != nil {
		t.Fatalf("DeleteRole: %v", err)
	}
	if n, _ := s.PermissionsForRole(ctx, role.ID); len(n) != 0 {
		t.Errorf("permissions survive the role: %v", n) // cascade
	}
}

// The folded name is unique across spellings, and grants outside the
// closed §8 vocabulary never reach the database.
func TestStoreRBACDuplicateNameAndUnknownPermission(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	role := &model.Role{Name: "Billing Ops"}
	t.Cleanup(func() { _ = s.DeleteRole(ctx, role.ID) })
	if err := s.CreateRole(ctx, role, nil); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	dup := &model.Role{Name: "billing  OPS"}
	if err := s.CreateRole(ctx, dup, nil); !store.IsRoleNameConflict(err) {
		t.Errorf("duplicate CreateRole err = %v, want a name conflict", err)
	} else if err := s.CreateRole(ctx, &model.Role{Name: "billing ops"}, nil); err == nil {
		t.Error("duplicate create succeeded")
	}

	bad := &model.Role{Name: "Sloppy"}
	err := s.CreateRole(ctx, bad, []string{"products.explode"})
	if !errors.Is(err, store.ErrUnknownPermission) {
		t.Errorf("CreateRole(unknown permission) err = %v, want ErrUnknownPermission", err)
	}
	if _, err := s.FindRoleByName(ctx, "sloppy"); !errors.Is(err, sql.ErrNoRows) {
		t.Error("a refused create still wrote the role row")
	}

	if err := s.SetRolePermissions(ctx, role.ID, []string{"invoices.read"}); !errors.Is(err, store.ErrUnknownPermission) {
		t.Errorf("SetRolePermissions(unknown permission) err = %v, want ErrUnknownPermission", err)
	}
}

// The 12 seeded bundles are protected: no delete, no rename, no
// re-permission. A description touch is fine — it changes nothing
// about what the bundle MEANS.
func TestStoreRBACSystemRoleProtection(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	role, err := s.FindRoleByName(ctx, " Finance ") // folded lookup finds the seed
	if err != nil {
		t.Fatalf("FindRoleByName(finance): %v (migration seed missing?)", err)
	}
	if !role.IsSystem {
		t.Fatal("seeded bundle is not marked is_system")
	}

	if err := s.DeleteRole(ctx, role.ID); !store.IsRoleProtected(err) {
		t.Errorf("DeleteRole(system) err = %v, want ErrSystemRoleProtected", err)
	}
	if err := s.SetRolePermissions(ctx, role.ID, []string{model.PermOrdersRead}); !store.IsRoleProtected(err) {
		t.Errorf("SetRolePermissions(system) err = %v, want ErrSystemRoleProtected", err)
	}
	role.Name = "money"
	if err := s.UpdateRole(ctx, role, nil); !store.IsRoleProtected(err) {
		t.Errorf("UpdateRole rename (system) err = %v, want ErrSystemRoleProtected", err)
	}
	role.Name = "finance"
	role.Description = "still the finance bundle"
	if err := s.UpdateRole(ctx, role, nil); err != nil {
		t.Errorf("UpdateRole description-only (system) err = %v, want nil", err)
	}
}

// DeleteRole refuses while the role is assigned — revoking first is
// the admin's explicit decision — and the assignments cascade away
// with their user and role.
func TestStoreRBACDeleteRoleInUse(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	u := newRBACTestUser(t, s, ctx, "inuse")
	role := &model.Role{Name: "Temp Bundle"}
	t.Cleanup(func() { _ = s.DeleteRole(ctx, role.ID) })
	if err := s.CreateRole(ctx, role, []string{model.PermReportsRead}); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}
	if err := s.AssignRole(ctx, u.ID, role.ID); err != nil {
		t.Fatalf("AssignRole: %v", err)
	}

	if err := s.DeleteRole(ctx, role.ID); !store.IsRoleInUseConflict(err) {
		t.Fatalf("DeleteRole(in use) err = %v, want ErrRoleInUse", err)
	}
	if err := s.RevokeRole(ctx, u.ID, role.ID); err != nil {
		t.Fatalf("RevokeRole: %v", err)
	}
	if err := s.DeleteRole(ctx, role.ID); err != nil {
		t.Errorf("DeleteRole(after revoke) err = %v, want nil", err)
	}
}

// The effective set is the UNION over every held role; revoking one
// role leaves exactly the others' grants.
func TestStoreRBACPermissionsForUserUnion(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	u := newRBACTestUser(t, s, ctx, "union")
	r1 := &model.Role{Name: "Union One"}
	r2 := &model.Role{Name: "Union Two"}
	t.Cleanup(func() {
		_ = s.DeleteRole(ctx, r1.ID)
		_ = s.DeleteRole(ctx, r2.ID)
	})
	if err := s.CreateRole(ctx, r1, []string{model.PermProductsRead, model.PermOrdersRead}); err != nil {
		t.Fatalf("CreateRole r1: %v", err)
	}
	if err := s.CreateRole(ctx, r2, []string{model.PermOrdersRead, model.PermReportsRead}); err != nil {
		t.Fatalf("CreateRole r2: %v", err)
	}

	// No roles yet: an empty map, never nil.
	set, err := s.PermissionsForUser(ctx, u.ID)
	if err != nil || len(set) != 0 {
		t.Fatalf("PermissionsForUser(none) = %v, %v; want empty map", set, err)
	}

	if err := s.AssignRole(ctx, u.ID, r1.ID); err != nil {
		t.Fatalf("AssignRole r1: %v", err)
	}
	if err := s.AssignRole(ctx, u.ID, r2.ID); err != nil {
		t.Fatalf("AssignRole r2: %v", err)
	}
	set, err = s.PermissionsForUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("PermissionsForUser: %v", err)
	}
	for _, p := range []string{model.PermProductsRead, model.PermOrdersRead, model.PermReportsRead} {
		if !set[p] {
			t.Errorf("union is missing %q (got %v)", p, set)
		}
	}
	if len(set) != 3 {
		t.Errorf("union = %v, want exactly 3 grants (orders.read shared, not doubled)", set)
	}

	if err := s.RevokeRole(ctx, u.ID, r1.ID); err != nil {
		t.Fatalf("RevokeRole r1: %v", err)
	}
	set, _ = s.PermissionsForUser(ctx, u.ID)
	if set[model.PermProductsRead] {
		t.Error("products.read survived revoking its only role")
	}
	if !set[model.PermReportsRead] || !set[model.PermOrdersRead] {
		t.Errorf("remaining union = %v, want union two's grants", set)
	}
}

// Assign and revoke are idempotent: repeating either is a no-op, not
// a failure, and the pair stays exactly one row.
func TestStoreRBACAssignRevokeIdempotent(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	u := newRBACTestUser(t, s, ctx, "idem")
	role := &model.Role{Name: "Idem Bundle"}
	t.Cleanup(func() { _ = s.DeleteRole(ctx, role.ID) })
	if err := s.CreateRole(ctx, role, nil); err != nil {
		t.Fatalf("CreateRole: %v", err)
	}

	for i := 0; i < 2; i++ {
		if err := s.AssignRole(ctx, u.ID, role.ID); err != nil {
			t.Fatalf("AssignRole #%d: %v", i+1, err)
		}
	}
	users, total, err := s.UsersWithRole(ctx, role.ID, store.All)
	if err != nil || total != 1 || len(users) != 1 || users[0].ID != u.ID {
		t.Fatalf("UsersWithRole = %d rows %+v (%v), want exactly the one user", total, users, err)
	}

	for i := 0; i < 2; i++ {
		if err := s.RevokeRole(ctx, u.ID, role.ID); err != nil {
			t.Fatalf("RevokeRole #%d: %v", i+1, err)
		}
	}
	_, total, _ = s.UsersWithRole(ctx, role.ID, store.All)
	if total != 0 {
		t.Errorf("UsersWithRole after revoke = %d, want 0", total)
	}
}

// The migration's seeded bundles and model.ExpandBuiltinRole are the
// same mapping, checked row by row — the two cannot drift without
// this failing.
func TestStoreRBACBuiltinSeedMatchesExpand(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	for _, name := range model.BuiltinRoleNames {
		role, err := s.FindRoleByName(ctx, name)
		if err != nil {
			t.Errorf("seeded role %q missing: %v", name, err)
			continue
		}
		if !role.IsSystem {
			t.Errorf("seeded role %q is not is_system", name)
		}
		stored, err := s.PermissionsForRole(ctx, role.ID)
		if err != nil {
			t.Errorf("PermissionsForRole(%q): %v", name, err)
			continue
		}
		want := model.ExpandBuiltinRole(name)
		if len(stored) != len(want) {
			t.Errorf("role %q has %d permissions, want %d", name, len(stored), len(want))
			continue
		}
		have := map[string]bool{}
		for _, p := range stored {
			have[p] = true
		}
		for _, p := range want {
			if !have[p] {
				t.Errorf("role %q is missing %q in the database (Go mapping says it should hold it)", name, p)
			}
		}
	}
}
