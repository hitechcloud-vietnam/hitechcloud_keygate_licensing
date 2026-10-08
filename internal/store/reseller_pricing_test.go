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

// TestResellerPriceOverrideTableAlias pins the alias Bun generates for
// the ResellerPriceOverride model — snake_case of the STRUCT name
// ("reseller_price_override"), never the table name. The column
// spellings are pinned with it: unit_amount_minor is money and a Bun
// naming surprise would surface only at runtime.
func TestResellerPriceOverrideTableAlias(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	raw, err := db.NewSelect().Model((*model.ResellerPriceOverride)(nil)).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)
	for _, want := range []string{
		`FROM "reseller_price_overrides" AS "reseller_price_override"`,
		`"reseller_price_override"."reseller_id"`,
		`"reseller_price_override"."plan_id"`,
		`"reseller_price_override"."unit_amount_minor"`,
		`"reseller_price_override"."currency"`,
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("pricing SQL does not contain %q; got:\n%s", want, sqlText)
		}
	}
}

// newPricingTestPlan makes a plan backed by its own product, without a
// licence attached — so a test may delete it (the cascade test) without
// tripping over licence rows.
func newPricingTestPlan(t *testing.T, s *store.Store, ctx context.Context) *model.Plan {
	t.Helper()
	uniq := store.NewID()
	product := &model.Product{Name: "Pricing Test", Slug: "pricing-test-" + uniq, Type: "saas"}
	if err := s.CreateProduct(ctx, product); err != nil {
		t.Fatalf("create product: %v", err)
	}
	plan := &model.Plan{
		ProductID:    product.ID,
		Name:         "Pricing Plan",
		Slug:         "pricing-plan-" + uniq,
		LicenseType:  "subscription",
		LicenseModel: "standard",
		GraceDays:    7,
	}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	return plan
}

// The pair is the whole identity: the first write creates, a repeat
// writes over it in place (PUT semantics) — no second row, no history.
// Deleting is scoped to the pair and answers sql.ErrNoRows when there
// is nothing to delete.
func TestResellerPriceOverrideUpsertAndDelete(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	r := newCommissionTestReseller(t, s, ctx)
	defer cleanupCommissionTestReseller(t, s, ctx, r)
	plan := newPricingTestPlan(t, s, ctx)

	o := &model.ResellerPriceOverride{
		ResellerID:      r.ID,
		PlanID:          plan.ID,
		UnitAmountMinor: 50000,
		Currency:        "USD",
	}
	if err := s.SetResellerPriceOverride(ctx, o); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, err := s.FindResellerPriceOverride(ctx, r.ID, plan.ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.UnitAmountMinor != 50000 || got.Currency != "USD" {
		t.Errorf("row = %+v, want 50000 USD", got)
	}

	// A repeat replaces the deal in place.
	o2 := &model.ResellerPriceOverride{
		ResellerID:      r.ID,
		PlanID:          plan.ID,
		UnitAmountMinor: 45000,
		Currency:        "EUR",
	}
	if err := s.SetResellerPriceOverride(ctx, o2); err != nil {
		t.Fatalf("reset: %v", err)
	}
	got, err = s.FindResellerPriceOverride(ctx, r.ID, plan.ID)
	if err != nil {
		t.Fatalf("find after reset: %v", err)
	}
	if got.UnitAmountMinor != 45000 || got.Currency != "EUR" {
		t.Errorf("row = %+v, want the replacement 45000 EUR", got)
	}

	// Exactly one row for the pair, listed under its reseller.
	rows, err := s.ListResellerPriceOverrides(ctx, r.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("list = %d rows, want 1", len(rows))
	}
	if n, err := s.CountResellerPriceOverrides(ctx, r.ID); err != nil || n != 1 {
		t.Errorf("count = %d (err %v), want 1", n, err)
	}

	// Delete is scoped to the pair and idempotent-by-refusal: a second
	// delete is sql.ErrNoRows, not a silent success.
	if err := s.DeleteResellerPriceOverride(ctx, r.ID, plan.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.FindResellerPriceOverride(ctx, r.ID, plan.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("find-after-delete err = %v, want sql.ErrNoRows", err)
	}
	if err := s.DeleteResellerPriceOverride(ctx, r.ID, plan.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("second delete err = %v, want sql.ErrNoRows", err)
	}
}

// Both sides of the pair are checked before the write, so the refusals
// are the typed ones a handler can map: a missing reseller is
// sql.ErrNoRows, a missing plan is ErrPriceOverridePlanNotFound. And
// the plan's delete takes its overrides with it (CASCADE) — price
// config for a dead plan means nothing.
func TestResellerPriceOverrideReferences(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	r := newCommissionTestReseller(t, s, ctx)
	defer cleanupCommissionTestReseller(t, s, ctx, r)
	plan := newPricingTestPlan(t, s, ctx)

	if err := s.SetResellerPriceOverride(ctx, &model.ResellerPriceOverride{
		ResellerID: "no-such-reseller", PlanID: plan.ID,
		UnitAmountMinor: 100, Currency: "USD",
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("unknown reseller err = %v, want sql.ErrNoRows", err)
	}
	if err := s.SetResellerPriceOverride(ctx, &model.ResellerPriceOverride{
		ResellerID: r.ID, PlanID: "no-such-plan",
		UnitAmountMinor: 100, Currency: "USD",
	}); !errors.Is(err, store.ErrPriceOverridePlanNotFound) {
		t.Errorf("unknown plan err = %v, want ErrPriceOverridePlanNotFound", err)
	}

	if err := s.SetResellerPriceOverride(ctx, &model.ResellerPriceOverride{
		ResellerID: r.ID, PlanID: plan.ID,
		UnitAmountMinor: 100, Currency: "USD",
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := s.DB.NewRaw("DELETE FROM plans WHERE id = ?", plan.ID).Exec(ctx); err != nil {
		t.Fatalf("delete plan: %v", err)
	}
	if _, err := s.FindResellerPriceOverride(ctx, r.ID, plan.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("override survived its plan: err = %v, want sql.ErrNoRows", err)
	}
}

// The currency shape is enforced at the database too: whatever skips
// the handler's model.ValidCurrencyCode still cannot store a malformed
// ISO code. (Whether "XXX" is a REAL currency is deliberately not
// checked offline — see model.ValidCurrencyCode.)
func TestResellerPriceOverrideCurrencyShapeEnforced(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	r := newCommissionTestReseller(t, s, ctx)
	defer cleanupCommissionTestReseller(t, s, ctx, r)
	plan := newPricingTestPlan(t, s, ctx)

	for _, bad := range []string{"usd", "US", "US1"} {
		err := s.SetResellerPriceOverride(ctx, &model.ResellerPriceOverride{
			ResellerID: r.ID, PlanID: plan.ID,
			UnitAmountMinor: 100, Currency: bad,
		})
		if err == nil {
			t.Errorf("currency %q was stored, want the CHECK to refuse it", bad)
			_ = s.DeleteResellerPriceOverride(ctx, r.ID, plan.ID)
		}
	}
}
