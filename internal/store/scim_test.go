package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Bun alias + column pins (DB-free) ───
//
// Regression pin for the snake_case-of-STRUCT-name alias trap (see
// bun_alias_test.go). SCIMIdentity maps to the scim_identities table but
// Bun ALIASES it "scim_identity" — hand-written qualifiers must use that,
// not "scim_identities".

func TestSCIMIdentityBunAlias(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	raw, err := db.NewSelect().Model((*SCIMIdentity)(nil)).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)
	if !strings.Contains(sqlText, `FROM "scim_identities" AS "scim_identity"`) &&
		!strings.Contains(sqlText, `AS "scim_identity"`) {
		t.Errorf("expected alias \"scim_identity\"; got:\n%s", sqlText)
	}
}

// bunColumnFor reads the explicit `column:` name from a struct field's bun
// tag, or "" when the column is inferred.
func bunColumnFor(t reflect.Type, field string) string {
	f, ok := t.FieldByName(field)
	if !ok {
		return ""
	}
	for _, part := range strings.Split(f.Tag.Get("bun"), ",") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "column:") {
			return strings.TrimPrefix(part, "column:")
		}
	}
	return ""
}

func TestSCIMIdentityColumnMapping(t *testing.T) {
	typ := reflect.TypeOf(SCIMIdentity{})
	want := map[string]string{
		"ID":         "id",
		"ExternalID": "external_id",
		"UserID":     "user_id",
		"Active":     "active",
		"CreatedAt":  "created_at",
		"UpdatedAt":  "updated_at",
	}
	for field, col := range want {
		if got := bunColumnFor(typ, field); got != col {
			t.Errorf("SCIMIdentity.%s -> column %q, want %q", field, got, col)
		}
	}
}

func TestSCIMUserRowColumnMapping(t *testing.T) {
	typ := reflect.TypeOf(SCIMUserRow{})
	want := map[string]string{
		"SCIMID":     "scim_id",
		"ExternalID": "external_id",
		"Active":     "active",
		"CreatedAt":  "created_at",
		"UpdatedAt":  "updated_at",
		"UserID":     "user_id",
		"Email":      "email",
		"Name":       "name",
	}
	for field, col := range want {
		if got := bunColumnFor(typ, field); got != col {
			t.Errorf("SCIMUserRow.%s -> column %q, want %q", field, got, col)
		}
	}
}

// TestSCIMUserColumns pins the joined SELECT list's AS aliases — they must
// line up with SCIMUserRow's column tags or the scan silently mismatches.
func TestSCIMUserColumns(t *testing.T) {
	cols := scimUserColumns()
	for _, as := range []string{
		"si.id AS scim_id", "si.external_id AS external_id", "si.active AS active",
		"si.created_at AS created_at", "si.updated_at AS updated_at", "si.user_id AS user_id",
		"u.email AS email", "u.name AS name",
	} {
		if !strings.Contains(cols, as) {
			t.Errorf("scimUserColumns() missing %q; got %s", as, cols)
		}
	}
}

// ─── conflict sentinels (DB-free) ───

func TestSCIMConflictSentinels(t *testing.T) {
	if !IsSCIMIdentityConflict(ErrSCIMIdentityExists) {
		t.Error("ErrSCIMIdentityExists should be an identity conflict")
	}
	if !IsSCIMIdentityConflict(fmt.Errorf("wrap: %w", ErrSCIMIdentityExists)) {
		t.Error("wrapped ErrSCIMIdentityExists should still be a conflict")
	}
	if IsSCIMIdentityConflict(ErrSCIMEmailTaken) {
		t.Error("email-taken is not an identity conflict")
	}
	if IsSCIMIdentityConflict(errors.New("boom")) {
		t.Error("unrelated error is not an identity conflict")
	}

	if !IsSCIMEmailConflict(ErrSCIMEmailTaken) {
		t.Error("ErrSCIMEmailTaken should be an email conflict")
	}
	if !IsSCIMEmailConflict(fmt.Errorf("wrap: %w", ErrSCIMEmailTaken)) {
		t.Error("wrapped ErrSCIMEmailTaken should still be a conflict")
	}
	if IsSCIMEmailConflict(ErrSCIMIdentityExists) {
		t.Error("identity-exists is not an email conflict")
	}
	if IsSCIMEmailConflict(nil) {
		t.Error("nil is not an email conflict")
	}
}

// ─── DB-backed round-trip: skipped without TEST_DATABASE_URL ───

// scimTestDB mirrors integration_test.go's setupTestDB for an in-package
// test. Skips when no database is configured.
func scimTestDB(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := New(dsn)
	if err != nil {
		t.Skipf("skipping integration test: %v", err)
	}
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations failed: %v", err)
	}
	return s
}

func TestSCIMStoreRoundTrip(t *testing.T) {
	s := scimTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := time.Now().Format("150405.000")
	email := "scim-" + suffix + "@acme.com"

	// Provision a user + identity.
	u := &model.User{Email: email, Name: "Alice Smith"}
	if err := s.UpsertUser(ctx, u); err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	created, err := s.FindUserByEmail(ctx, email)
	if err != nil {
		t.Fatalf("find user: %v", err)
	}

	ident := &SCIMIdentity{UserID: created.ID, Active: true}
	if err := s.CreateSCIMIdentity(ctx, ident); err != nil {
		t.Fatalf("create identity: %v", err)
	}
	if ident.ID == "" {
		t.Fatal("identity id not allocated")
	}

	// A second identity for the same user is a conflict.
	dup := &SCIMIdentity{UserID: created.ID, Active: true}
	if err := s.CreateSCIMIdentity(ctx, dup); !IsSCIMIdentityConflict(err) {
		t.Fatalf("duplicate identity err = %v, want conflict", err)
	}

	// Reads.
	if _, err := s.FindSCIMIdentityByID(ctx, ident.ID); err != nil {
		t.Fatalf("find by id: %v", err)
	}
	if _, err := s.FindSCIMIdentityByUserID(ctx, created.ID); err != nil {
		t.Fatalf("find by user id: %v", err)
	}
	row, err := s.FindSCIMUserBySCIMID(ctx, ident.ID)
	if err != nil {
		t.Fatalf("find scim user: %v", err)
	}
	if row.Email != email || row.Name != "Alice Smith" {
		t.Fatalf("joined row = %+v", row)
	}
	if _, err := s.FindSCIMUserByUserName(ctx, email); err != nil {
		t.Fatalf("find by userName: %v", err)
	}
	if _, _, err := s.ListSCIMUsers(ctx, Page{Limit: 100, Offset: 0}); err != nil {
		t.Fatalf("list: %v", err)
	}

	// Active toggle.
	if err := s.SetSCIMIdentityActive(ctx, ident.ID, false); err != nil {
		t.Fatalf("set active: %v", err)
	}
	got, _ := s.FindSCIMIdentityByID(ctx, ident.ID)
	if got.Active {
		t.Fatal("active should be false")
	}

	// Email change + conflict.
	newEmail := "scim2-" + suffix + "@acme.com"
	if err := s.UpdateSCIMUserEmail(ctx, created.ID, newEmail); err != nil {
		t.Fatalf("update email: %v", err)
	}
	// Change onto another account's address -> conflict.
	other := &model.User{Email: "taken-" + suffix + "@acme.com", Name: "Taken"}
	if err := s.UpsertUser(ctx, other); err != nil {
		t.Fatalf("upsert other: %v", err)
	}
	if err := s.UpdateSCIMUserEmail(ctx, created.ID, other.Email); !IsSCIMEmailConflict(err) {
		t.Fatalf("email conflict err = %v", err)
	}

	// Delete.
	if err := s.DeleteSCIMIdentity(ctx, ident.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.FindSCIMIdentityByID(ctx, ident.ID); err == nil {
		t.Fatal("identity should be gone after delete")
	}
}
