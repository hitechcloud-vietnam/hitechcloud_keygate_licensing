package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// pricesTestDB is the TEST_DATABASE_URL-gated store for the §53
// price suite (query shapes need package store, hence local).
func pricesTestDB(t *testing.T) *Store {
	t.Helper()
	s := reviewsTestDB(t)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func pricesSuffix() string { return time.Now().Format("150405.000000") }

// TestPlanPriceTableAlias pins PlanPrice's Bun alias and columns: the
// alias is the snake_case of the STRUCT name ("plan_price"), so
// hand-written qualifiers must say "plan_price".x, never plan_prices.x
// (bun_alias_test.go doctrine).
func TestPlanPriceTableAlias(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	ins := &PlanPrice{PlanID: "p1", Currency: "USD", AmountMinor: 100}
	raw, err := db.NewInsert().Model(ins).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build insert: %v", err)
	}
	for _, col := range []string{"plan_id", "currency", "amount_minor", "stripe_price_id", "is_default"} {
		if !strings.Contains(string(raw), col) {
			t.Errorf("generated INSERT does not name column %q; got:\n%s", col, raw)
		}
	}

	raw, err = db.NewSelect().Model((*PlanPrice)(nil)).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	if !strings.Contains(string(raw), `FROM "plan_prices" AS "plan_price"`) {
		t.Errorf("PlanPrice must alias to \"plan_price\"; got:\n%s", raw)
	}
}

// pricesSeedPlan writes a product + plan to hang prices on.
func pricesSeedPlan(t *testing.T, s *Store, ctx context.Context, suffix string) *model.Plan {
	t.Helper()
	prod := &model.Product{Name: "PR " + suffix, Slug: "pr-" + suffix, Type: "hybrid"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("product: %v", err)
	}
	plan := &model.Plan{ProductID: prod.ID, Name: "PR Plan " + suffix, Slug: "pr-plan-" + suffix,
		LicenseType: "perpetual", LicenseModel: "standard", Active: true}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("plan: %v", err)
	}
	return plan
}

// TestPlanPriceResolveMatrix pins the §53 resolution matrix:
//
//	exact currency row        → that row
//	no exact row, a default   → the default row
//	no rows at all            → ErrPlanPriceCurrencyNotSupported
//
// plus the write rules: currency folds to upper, an unknown plan is
// ErrPlanPricePlanNotFound, re-pricing a currency upserts (never
// duplicates) and only one row per plan may hold is_default.
func TestPlanPriceResolveMatrix(t *testing.T) {
	s := pricesTestDB(t)
	ctx := context.Background()
	suffix := pricesSuffix()
	plan := pricesSeedPlan(t, s, ctx, suffix)

	// Nothing priced yet: every currency is refused.
	if _, err := s.ResolvePlanPrice(ctx, plan.ID, "USD"); !errors.Is(err, ErrPlanPriceCurrencyNotSupported) {
		t.Fatalf("empty price list: err = %v, want ErrPlanPriceCurrencyNotSupported", err)
	}

	// Unknown plan is refused before anything is written.
	if err := s.SetPlanPrice(ctx, &PlanPrice{PlanID: "nope", Currency: "USD", AmountMinor: 1}); !errors.Is(err, ErrPlanPricePlanNotFound) {
		t.Fatalf("unknown plan: err = %v, want ErrPlanPricePlanNotFound", err)
	}

	// Write a default USD price and a VND one; "usd" folds to "USD".
	if err := s.SetPlanPrice(ctx, &PlanPrice{PlanID: plan.ID, Currency: "usd", AmountMinor: 1000, IsDefault: true}); err != nil {
		t.Fatalf("set usd: %v", err)
	}
	if err := s.SetPlanPrice(ctx, &PlanPrice{PlanID: plan.ID, Currency: "VND", AmountMinor: 25000000}); err != nil {
		t.Fatalf("set vnd: %v", err)
	}

	// Exact match wins.
	got, err := s.ResolvePlanPrice(ctx, plan.ID, "VND")
	if err != nil {
		t.Fatalf("resolve VND: %v", err)
	}
	if got.Currency != "VND" || got.AmountMinor != 25000000 {
		t.Errorf("resolve VND = %q %d, want VND 25000000", got.Currency, got.AmountMinor)
	}

	// No exact row → the default row.
	got, err = s.ResolvePlanPrice(ctx, plan.ID, "JPY")
	if err != nil {
		t.Fatalf("resolve JPY (default fallback): %v", err)
	}
	if got.Currency != "USD" || got.AmountMinor != 1000 {
		t.Errorf("fallback = %q %d, want the USD default 1000", got.Currency, got.AmountMinor)
	}

	// Empty currency asks for the default directly.
	got, err = s.ResolvePlanPrice(ctx, plan.ID, "")
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if got.Currency != "USD" {
		t.Errorf("default resolve = %q, want USD", got.Currency)
	}

	// Re-pricing upserts: one row per (plan, currency).
	if err := s.SetPlanPrice(ctx, &PlanPrice{PlanID: plan.ID, Currency: "VND", AmountMinor: 26000000}); err != nil {
		t.Fatalf("re-price vnd: %v", err)
	}
	rows, err := s.ListPlanPrices(ctx, plan.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("list has %d rows, want 2 (upsert, never duplicate)", len(rows))
	}
	got, err = s.ResolvePlanPrice(ctx, plan.ID, "VND")
	if err != nil {
		t.Fatalf("resolve VND: %v", err)
	}
	if got.AmountMinor != 26000000 {
		t.Errorf("re-priced VND = %d, want 26000000", got.AmountMinor)
	}

	// The default flag is exclusive: making VND the default demotes USD.
	if err := s.SetPlanPrice(ctx, &PlanPrice{PlanID: plan.ID, Currency: "VND", AmountMinor: 26000000, IsDefault: true}); err != nil {
		t.Fatalf("default vnd: %v", err)
	}
	rows, err = s.ListPlanPrices(ctx, plan.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	def := 0
	for _, r := range rows {
		if r.IsDefault {
			def++
		}
	}
	if def != 1 {
		t.Errorf("%d rows hold is_default, want exactly 1", def)
	}

	// A plan with only non-default rows still refuses an unknown
	// currency — there is no default to fall back to.
	other := pricesSeedPlan(t, s, ctx, "nd"+suffix)
	if err := s.SetPlanPrice(ctx, &PlanPrice{PlanID: other.ID, Currency: "VND", AmountMinor: 1}); err != nil {
		t.Fatalf("set non-default: %v", err)
	}
	if _, err := s.ResolvePlanPrice(ctx, other.ID, "JPY"); !errors.Is(err, ErrPlanPriceCurrencyNotSupported) {
		t.Errorf("no default row: err = %v, want ErrPlanPriceCurrencyNotSupported", err)
	}
}
