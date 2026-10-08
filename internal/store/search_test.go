package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// searchTestDB is the TEST_DATABASE_URL-gated store for the search
// suite. Same contract as notificationsCenterTestDB — no database, no
// test — kept local because this file also pins query shapes, which
// needs the unexported builders (package store, not store_test).
func searchTestDB(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := New(dsn)
	if err != nil {
		t.Skipf("skipping integration test: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations failed: %v", err)
	}
	return s
}

// The alias pin: every model the search statements touch is aliased
// by the snake_case of its STRUCT name, not its table name, and the
// hand-written qualifiers must say the same thing. Same bug class as
// TestBunDefaultTableAliases and TestUserNotificationTableAlias.
func TestSearchModelAliases(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	cases := []struct {
		model     interface{}
		wantAlias string
	}{
		{(*model.Product)(nil), `AS "product"`},
		{(*model.User)(nil), `AS "user"`},
		{(*model.Order)(nil), `AS "order"`},
		{(*model.Invoice)(nil), `AS "invoice"`},
		{(*model.License)(nil), `AS "license"`},
		{(*model.Subscription)(nil), `AS "subscription"`},
		{(*model.Activation)(nil), `AS "activation"`},
		{(*model.Reseller)(nil), `AS "reseller"`},
		{(*model.Affiliate)(nil), `AS "affiliate"`},
	}
	for _, tc := range cases {
		raw, err := db.NewSelect().Model(tc.model).AppendQuery(db.QueryGen(), nil)
		if err != nil {
			t.Fatalf("build query for %T: %v", tc.model, err)
		}
		if sqlText := string(raw); !strings.Contains(sqlText, tc.wantAlias) {
			t.Errorf("generated SQL for %T does not contain %q; got:\n%s", tc.model, tc.wantAlias, sqlText)
		}
	}
}

// searchSQL builds one per-type statement and renders it with the
// args inlined (bun v1.2.18 renders client-side, no $n placeholders).
func searchSQL(t *testing.T, typ string) string {
	t.Helper()
	db := bun.NewDB(nil, pgdialect.New())
	q := searchTypeQuery(db, typ, "acme", 20)
	if q == nil {
		t.Fatalf("no query for type %q", typ)
	}
	raw, err := q.AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build %s query: %v", typ, err)
	}
	return string(raw)
}

// Every statement is scoped to its struct-name alias, selects the
// shared four-column shape, carries the term as a bound LIKE and caps
// the page. The "bare table name" assertions are the regression pin
// for the missing-FROM-clause bug: products.name compiles fine in
// Go and fails on the database.
func TestSearchQueryShapes(t *testing.T) {
	cases := []struct {
		typ       string
		alias     string
		wants     []string
		forbids   []string
		extraLike int // how many ILIKE terms this type matches on
	}{
		{
			typ: SearchTypeProduct, alias: "product", extraLike: 2,
			wants:   []string{`AS "product"`, "product.name ILIKE '%acme%'", "product.slug ILIKE '%acme%'", "LIMIT 20"},
			forbids: []string{"products.name", "products.slug"},
		},
		{
			typ: SearchTypeCustomer, alias: "user", extraLike: 2,
			wants:   []string{`AS "user"`, `"user".name ILIKE '%acme%'`, `"user".email ILIKE '%acme%'`, "LIMIT 20"},
			forbids: []string{"users.name", "users.email"},
		},
		{
			typ: SearchTypeOrder, alias: "order", extraLike: 3,
			wants: []string{
				`AS "order"`, `"order".order_number ILIKE '%acme%'`,
				`"order".customer_email ILIKE '%acme%'`, `"order".customer_name ILIKE '%acme%'`, "LIMIT 20",
			},
			forbids: []string{"orders.order_number", "orders.customer_email"},
		},
		{
			typ: SearchTypeInvoice, alias: "invoice", extraLike: 2,
			wants: []string{
				`AS "invoice"`, `LEFT JOIN orders AS "order" ON "order".id = invoice.order_id`,
				`invoice.invoice_number ILIKE '%acme%'`, `"order".order_number ILIKE '%acme%'`,
				`"order".id AS link_ref`, "LIMIT 20",
			},
			forbids: []string{"invoices.invoice_number", "orders.id = invoices"},
		},
		{
			typ: SearchTypeLicense, alias: "license", extraLike: 3,
			wants: []string{
				`AS "license"`, "license.email ILIKE '%acme%'", "license.org_name ILIKE '%acme%'",
				"license.external_customer_id ILIKE '%acme%'", "license.key_hash = ", "LIMIT 20",
			},
			// The credential pin: a licence is found by its key HASH
			// and the plaintext key column appears NOWHERE — not in the
			// projection, not in the WHERE, not at all.
			forbids: []string{"license_key", "licenses.email"},
		},
		{
			typ: SearchTypeSubscription, alias: "subscription", extraLike: 3,
			wants: []string{
				`AS "subscription"`, "LEFT JOIN licenses AS license ON license.id = subscription.license_id",
				"subscription.external_id ILIKE '%acme%'", "subscription.id ILIKE '%acme%'",
				"license.email ILIKE '%acme%'", "subscription.license_id AS link_ref", "LIMIT 20",
			},
			forbids: []string{"subscriptions.external_id", "licenses.email ILIKE"},
		},
		{
			typ: SearchTypeDevice, alias: "activation", extraLike: 3,
			wants: []string{
				`AS "activation"`, "LEFT JOIN licenses AS license ON license.id = activation.license_id",
				"activation.identifier ILIKE '%acme%'", "activation.label ILIKE '%acme%'",
				"activation.license_id AS link_ref", "LIMIT 20",
			},
			forbids: []string{"activations.identifier"},
		},
		{
			typ: SearchTypeReseller, alias: "reseller", extraLike: 2,
			wants:   []string{`AS "reseller"`, "reseller.name ILIKE '%acme%'", "reseller.contact_email ILIKE '%acme%'", "LIMIT 20"},
			forbids: []string{"resellers.name", "resellers.contact_email"},
		},
		{
			typ: SearchTypeAffiliate, alias: "affiliate", extraLike: 2,
			wants:   []string{`AS "affiliate"`, "affiliate.name ILIKE '%acme%'", "affiliate.contact_email ILIKE '%acme%'", "LIMIT 20"},
			forbids: []string{"affiliates.name", "affiliates.contact_email"},
		},
	}

	for _, tc := range cases {
		sqlText := searchSQL(t, tc.typ)
		for _, want := range tc.wants {
			if !strings.Contains(sqlText, want) {
				t.Errorf("%s query is missing %q; got:\n%s", tc.typ, want, sqlText)
			}
		}
		for _, bad := range tc.forbids {
			if strings.Contains(sqlText, bad) {
				t.Errorf("%s query contains %q (alias bug or credential leak); got:\n%s", tc.typ, bad, sqlText)
			}
		}
		// The shared projection — every title/subtitle/link_ref alias
		// must be there or the scan has nothing to read.
		for _, col := range []string{"AS id", "AS title", "AS subtitle", "AS link_ref"} {
			if !strings.Contains(sqlText, col) {
				t.Errorf("%s query is missing column %q; got:\n%s", tc.typ, col, sqlText)
			}
		}
	}
}

// The LIMIT is the query's own per-type cap, applied to every type.
func TestSearchQueryLimitIsApplied(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	q := searchTypeQuery(db, SearchTypeProduct, "acme", 7)
	raw, err := q.AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if sqlText := string(raw); !strings.Contains(sqlText, "LIMIT 7") {
		t.Errorf("per-type LIMIT missing; got:\n%s", sqlText)
	}
}

// An empty term never reaches the database: "everything" is not a
// search. The store answers before touching its (here nil) handle.
func TestSearchEmptyQueryHitsNoRows(t *testing.T) {
	s := &Store{}
	for _, q := range []string{"", "   ", "\t"} {
		hits, err := s.Search(context.Background(), SearchOptions{Query: q, Limit: 20})
		if err != nil {
			t.Fatalf("Search(%q): %v", q, err)
		}
		if len(hits) != 0 {
			t.Errorf("Search(%q) = %d hits, want none", q, len(hits))
		}
	}
}

// A ?types= member outside the closed vocabulary is refused before
// any statement runs — the handler refuses it first, this is the
// store's own backstop.
func TestSearchUnknownTypeRefused(t *testing.T) {
	s := &Store{}
	_, err := s.Search(context.Background(), SearchOptions{
		Query: "acme",
		Types: []string{SearchTypeProduct, "bogus"},
		Limit: 20,
	})
	if !errors.Is(err, ErrUnknownSearchType) {
		t.Fatalf("Search with unknown type = %v, want ErrUnknownSearchType", err)
	}
}

// ── DB-backed round trip: skipped without TEST_DATABASE_URL ──

// The licence half of the contract, end to end: a licence is found by
// its FULL KEY (matched against the hash), and the hit carries the
// identity only — no key material in any field.
func TestSearchLicenseByKeyHash(t *testing.T) {
	s := searchTestDB(t)
	ctx := context.Background()
	suffix := time.Now().Format("150405.000")

	prod := &model.Product{Name: "SearchCo " + suffix, Slug: "searchco-" + suffix, Type: "desktop"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("create product: %v", err)
	}
	plan := &model.Plan{ProductID: prod.ID, Name: "Std", Slug: "std-" + suffix, LicenseType: "perpetual", LicenseModel: "standard"}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	key := "KEY-SEARCH-" + suffix
	lic := &model.License{
		ProductID: prod.ID, PlanID: plan.ID,
		Email:   "search-buyer-" + suffix + "@example.com",
		OrgName: "SearchOrg " + suffix,
		// Distinct from both the email and the org name, so the only
		// way the query can find this row is the key hash.
		LicenseKey: key,
		Status:     model.StatusActive,
	}
	if err := s.CreateLicense(ctx, lic); err != nil {
		t.Fatalf("create license: %v", err)
	}

	hits, err := s.Search(ctx, SearchOptions{Query: key, Types: []string{SearchTypeLicense}, Limit: 20})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	var mine *SearchHit
	for i := range hits {
		if hits[i].ID == lic.ID {
			mine = &hits[i]
		}
	}
	if mine == nil {
		t.Fatalf("key search found no hit for %s (hits: %+v)", lic.ID, hits)
	}
	if mine.Title != lic.Email {
		t.Errorf("hit title = %q, want the customer email %q", mine.Title, lic.Email)
	}
	for _, field := range []string{mine.Title, mine.Subtitle, mine.Ref, mine.ID} {
		if strings.Contains(strings.ToUpper(field), "KEY-SEARCH") {
			t.Errorf("hit leaked key material: %+v", mine)
		}
	}

	// And the plain-text columns still match what an admin types.
	hits, err = s.Search(ctx, SearchOptions{
		Query: "search-buyer-" + suffix,
		Types: []string{SearchTypeLicense},
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("email search: %v", err)
	}
	if len(hits) == 0 {
		t.Errorf("email search found nothing for %s", lic.Email)
	}
}
