package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// TestResellerTableAliases pins the aliases Bun generates for the
// reseller models — the same bug class TestBunDefaultTableAliases
// pins for the commerce ones. Bun aliases a model by the snake_case of
// the STRUCT name, so hand-written qualifiers must use "reseller",
// "reseller_license" and "reseller_customer", never the table names.
func TestResellerTableAliases(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	cases := []struct {
		model     interface{}
		wantAlias string
	}{
		{(*model.Reseller)(nil), `"reseller"`},
		{(*model.ResellerLicense)(nil), `"reseller_license"`},
		{(*model.ResellerCustomer)(nil), `"reseller_customer"`},
	}
	for _, tc := range cases {
		raw, err := db.NewSelect().Model(tc.model).AppendQuery(db.QueryGen(), nil)
		if err != nil {
			t.Fatalf("build query for %T: %v", tc.model, err)
		}
		sqlText := string(raw)
		if as := "AS " + tc.wantAlias; !strings.Contains(sqlText, as) {
			t.Errorf("generated SQL for %T does not contain %q; got:\n%s", tc.model, as, sqlText)
		}
	}
}

// The conflict detectors must answer on exactly the two spellings of a
// refusal — the sentinel the write paths fold the driver error into,
// and a raw unique violation that never passed through them — and on
// nothing else, or a genuine failure would be answered 409.
func TestIsResellerEmailConflict(t *testing.T) {
	if store.IsResellerEmailConflict(nil) {
		t.Error("nil reads as an email conflict")
	}
	if store.IsResellerEmailConflict(errors.New("connection reset")) {
		t.Error("an arbitrary error reads as an email conflict")
	}
	if !store.IsResellerEmailConflict(store.ErrResellerEmailTaken) {
		t.Error("ErrResellerEmailTaken is not recognised")
	}
	wrapped := fmt.Errorf("create reseller: %w", store.ErrResellerEmailTaken)
	if !store.IsResellerEmailConflict(wrapped) {
		t.Error("a wrapped ErrResellerEmailTaken is not recognised")
	}
}

func TestIsResellerLicenseConflict(t *testing.T) {
	if store.IsResellerLicenseConflict(nil) {
		t.Error("nil reads as an allocation conflict")
	}
	if store.IsResellerLicenseConflict(errors.New("connection reset")) {
		t.Error("an arbitrary error reads as an allocation conflict")
	}
	if !store.IsResellerLicenseConflict(store.ErrLicenseAlreadyAllocated) {
		t.Error("ErrLicenseAlreadyAllocated is not recognised")
	}
	wrapped := fmt.Errorf("allocate: %w", store.ErrLicenseAlreadyAllocated)
	if !store.IsResellerLicenseConflict(wrapped) {
		t.Error("a wrapped ErrLicenseAlreadyAllocated is not recognised")
	}
}

// TestResellerLicensesJoinShape pins the join correlation in
// ListResellerLicenses without a database: the outer model is License,
// so it is aliased "license" (the snake_case of the struct name) and
// the join must correlate on "license".id, never "licenses".id — the
// exact bug class TestBunDefaultTableAliases guards. This mirrors the
// query ListResellerLicenses builds.
func TestResellerLicensesJoinShape(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	var dest []*model.License
	q := db.NewSelect().Model(&dest).
		Join(`JOIN reseller_licenses rl ON rl.license_id = "license".id`).
		Where("rl.reseller_id = ?", "res1").
		OrderExpr(`"license".created_at DESC, "license".id DESC`)
	raw, err := q.AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)
	for _, want := range []string{
		`FROM "licenses" AS "license"`, // the alias the qualifiers must match
		`JOIN reseller_licenses rl ON rl.license_id = "license".id`,
		`"license".created_at DESC`,
		`"license".id DESC`,
		"rl.reseller_id = ",
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("licences SQL does not contain %q; got:\n%s", want, sqlText)
		}
	}
}

// newResellerTestLicense makes a licence backed by its own product and
// plan, keyed by a fresh UUID so several can be created in one test
// without a slug/identifier collision.
func newResellerTestLicense(t *testing.T, s *store.Store, ctx context.Context) *model.License {
	t.Helper()
	uniq := store.NewID()
	product := &model.Product{Name: "Reseller Test", Slug: "reseller-test-" + uniq, Type: "saas"}
	if err := s.CreateProduct(ctx, product); err != nil {
		t.Fatalf("create product: %v", err)
	}
	plan := &model.Plan{
		ProductID:    product.ID,
		Name:         "Test Plan",
		Slug:         "test-plan-" + uniq,
		LicenseType:  "subscription",
		LicenseModel: "standard",
		GraceDays:    7,
	}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	lic := &model.License{
		ProductID:  product.ID,
		PlanID:     plan.ID,
		Email:      "reseller-test-" + uniq + "@example.com",
		LicenseKey: "KEY-" + uniq,
		Status:     "active",
	}
	if err := s.CreateLicense(ctx, lic); err != nil {
		t.Fatalf("create license: %v", err)
	}
	return lic
}

func newResellerTest(t *testing.T, s *store.Store, ctx context.Context, name, email string) *model.Reseller {
	t.Helper()
	r := &model.Reseller{Name: name, ContactEmail: email, Status: "active", CommissionBPS: 1000}
	if err := s.CreateReseller(ctx, r); err != nil {
		t.Fatalf("create reseller %s: %v", name, err)
	}
	return r
}

// The contact email is folded on the way in, so two spellings of one
// address collide on the unique index and answer the typed refusal.
func TestResellerEmailConflict(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	newResellerTest(t, s, ctx, "Acme", "acme@example.com")

	dup := &model.Reseller{Name: "Acme Two", ContactEmail: "ACME@Example.com"}
	err := s.CreateReseller(ctx, dup)
	if !store.IsResellerEmailConflict(err) {
		t.Fatalf("duplicate create err = %v, want an email conflict", err)
	}
}

func TestResellerFinders(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	r := newResellerTest(t, s, ctx, "Acme", "acme@example.com")

	got, err := s.FindResellerByID(ctx, r.ID)
	if err != nil {
		t.Fatalf("FindResellerByID: %v", err)
	}
	if got.Name != "Acme" || got.ContactEmail != "acme@example.com" {
		t.Errorf("by-id row = %+v", got)
	}

	// The lookup folds the query address, so any spelling finds it.
	byEmail, err := s.FindResellerByEmail(ctx, "  ACME@example.COM ")
	if err != nil {
		t.Fatalf("FindResellerByEmail: %v", err)
	}
	if byEmail.ID != r.ID {
		t.Errorf("by-email id = %q, want %q", byEmail.ID, r.ID)
	}

	if _, err := s.FindResellerByID(ctx, "nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing id err = %v, want sql.ErrNoRows", err)
	}
}

func TestResellerListFiltersAndPaging(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	newResellerTest(t, s, ctx, "Alpha", "alpha@example.com")
	newResellerTest(t, s, ctx, "Beta", "beta@example.com")
	b := newResellerTest(t, s, ctx, "Gamma", "gamma@example.com")
	b.Status = model.ResellerStatusSuspended
	if err := s.UpdateReseller(ctx, b); err != nil {
		t.Fatalf("suspend: %v", err)
	}

	// status filter narrows to one lifecycle value.
	suspended, total, err := s.ListResellers(ctx, "", model.ResellerStatusSuspended, store.All)
	if err != nil {
		t.Fatalf("list suspended: %v", err)
	}
	if total != 1 || len(suspended) != 1 || suspended[0].Name != "Gamma" {
		t.Errorf("suspended list = %v (total %d), want just Gamma", suspended, total)
	}

	// search covers name and contact address.
	found, total, err := s.ListResellers(ctx, "bet", "", store.All)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total != 1 || len(found) != 1 || found[0].Name != "Beta" {
		t.Errorf("search 'bet' = %v (total %d), want just Beta", found, total)
	}

	// paging: two per window over three rows ordered by name.
	page, total, err := s.ListResellers(ctx, "", "", store.Page{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(page) != 2 || page[0].Name != "Alpha" || page[1].Name != "Beta" {
		t.Errorf("first page = %v, want Alpha then Beta", page)
	}
	next, _, err := s.ListResellers(ctx, "", "", store.Page{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatalf("next page: %v", err)
	}
	if len(next) != 1 || next[0].Name != "Gamma" {
		t.Errorf("second page = %v, want just Gamma", next)
	}
}

func TestResellerUpdate(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	r := newResellerTest(t, s, ctx, "Acme", "acme@example.com")
	r.Name = "Acme Renamed"
	r.Notes = "vip partner"
	if err := s.UpdateReseller(ctx, r); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, err := s.FindResellerByID(ctx, r.ID)
	if err != nil {
		t.Fatalf("find after update: %v", err)
	}
	if got.Name != "Acme Renamed" || got.Notes != "vip partner" {
		t.Errorf("updated row = %+v", got)
	}

	// Renaming onto another reseller's address is the same conflict.
	newResellerTest(t, s, ctx, "Other", "other@example.com")
	other := newResellerTest(t, s, ctx, "Solo", "solo@example.com")
	other.ContactEmail = "acme@example.com"
	if err := s.UpdateReseller(ctx, other); !store.IsResellerEmailConflict(err) {
		t.Errorf("rename onto a taken email err = %v, want a conflict", err)
	}
}

// The delete policy: a reseller that still owns licences is refused
// (the allocations are commercial records), and only deletable once
// they are gone.
func TestResellerDeletePolicy(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	r := newResellerTest(t, s, ctx, "Acme", "acme@example.com")
	lic := newResellerTestLicense(t, s, ctx)
	if err := s.AllocateLicense(ctx, r.ID, lic.ID); err != nil {
		t.Fatalf("allocate: %v", err)
	}

	// Refused while it owns a licence.
	if err := s.DeleteReseller(ctx, r.ID); !errors.Is(err, store.ErrResellerHasAllocations) {
		t.Fatalf("delete with allocations err = %v, want ErrResellerHasAllocations", err)
	}

	// Once deallocated, the account can be removed.
	if err := s.DeallocateLicense(ctx, lic.ID); err != nil {
		t.Fatalf("deallocate: %v", err)
	}
	if err := s.DeleteReseller(ctx, r.ID); err != nil {
		t.Fatalf("delete after deallocate: %v", err)
	}
	if _, err := s.FindResellerByID(ctx, r.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("deleted reseller still resolves: %v", err)
	}

	// A delete of a row that was never there is a no-op 404.
	if err := s.DeleteReseller(ctx, "nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("delete missing err = %v, want sql.ErrNoRows", err)
	}
}

func TestResellerAllocateLicenseConflicts(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	r := newResellerTest(t, s, ctx, "Acme", "acme@example.com")
	other := newResellerTest(t, s, ctx, "Other", "other@example.com")
	lic := newResellerTestLicense(t, s, ctx)

	if err := s.AllocateLicense(ctx, r.ID, lic.ID); err != nil {
		t.Fatalf("allocate: %v", err)
	}
	// A repeat to the same reseller is "already allocated".
	if err := s.AllocateLicense(ctx, r.ID, lic.ID); !errors.Is(err, store.ErrLicenseAlreadyAllocated) {
		t.Errorf("double allocate err = %v, want ErrLicenseAlreadyAllocated", err)
	}
	// A claim by a second reseller is the same refusal — one owner per licence.
	if err := s.AllocateLicense(ctx, other.ID, lic.ID); !errors.Is(err, store.ErrLicenseAlreadyAllocated) {
		t.Errorf("cross-reseller allocate err = %v, want ErrLicenseAlreadyAllocated", err)
	}
	// An unknown licence is a typed refusal, not a foreign-key crash.
	if err := s.AllocateLicense(ctx, r.ID, "no-such-license"); !errors.Is(err, store.ErrAllocationLicenseNotFound) {
		t.Errorf("allocate unknown licence err = %v, want ErrAllocationLicenseNotFound", err)
	}
	// An unknown reseller is a no-row refusal.
	if err := s.AllocateLicense(ctx, "no-such-reseller", lic.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("allocate to unknown reseller err = %v, want sql.ErrNoRows", err)
	}
}

func TestResellerDeallocateLicense(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	r := newResellerTest(t, s, ctx, "Acme", "acme@example.com")
	lic := newResellerTestLicense(t, s, ctx)
	if err := s.AllocateLicense(ctx, r.ID, lic.ID); err != nil {
		t.Fatalf("allocate: %v", err)
	}

	if err := s.DeallocateLicense(ctx, lic.ID); err != nil {
		t.Fatalf("deallocate: %v", err)
	}
	// A deallocation of something not allocated is a no-op 404.
	if err := s.DeallocateLicense(ctx, lic.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("second deallocate err = %v, want sql.ErrNoRows", err)
	}
}

// The allocation list returns the licence rows a reseller owns, as
// licences (joined to reseller_licenses), newest first, paged.
func TestResellerListLicenses(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	r := newResellerTest(t, s, ctx, "Acme", "acme@example.com")
	unowned := newResellerTestLicense(t, s, ctx)
	lic1 := newResellerTestLicense(t, s, ctx)
	lic2 := newResellerTestLicense(t, s, ctx)
	for _, id := range []string{lic1.ID, lic2.ID} {
		if err := s.AllocateLicense(ctx, r.ID, id); err != nil {
			t.Fatalf("allocate %s: %v", id, err)
		}
	}

	rows, total, err := s.ListResellerLicenses(ctx, r.ID, store.All)
	if err != nil {
		t.Fatalf("list licences: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("allocated rows = %d (total %d), want 2", len(rows), total)
	}
	seen := map[string]bool{}
	for _, l := range rows {
		seen[l.ID] = true
	}
	if !seen[lic1.ID] || !seen[lic2.ID] {
		t.Errorf("rows = %v, want the two allocated licences", rows)
	}
	if seen[unowned.ID] {
		t.Errorf("unallocated licence %s appeared in the list", unowned.ID)
	}

	// Paging halves the window but keeps the total.
	page, total, err := s.ListResellerLicenses(ctx, r.ID, store.Page{Limit: 1, Offset: 0})
	if err != nil {
		t.Fatalf("paged licences: %v", err)
	}
	if total != 2 || len(page) != 1 {
		t.Errorf("paged rows = %d (total %d), want 1 of 2", len(page), total)
	}

	// A reseller with none owns an empty set.
	empty, total, err := s.ListResellerLicenses(ctx, "no-such-reseller", store.All)
	if err != nil {
		t.Fatalf("empty list: %v", err)
	}
	if total != 0 || len(empty) != 0 {
		t.Errorf("empty list = %v (total %d), want none", empty, total)
	}
}
