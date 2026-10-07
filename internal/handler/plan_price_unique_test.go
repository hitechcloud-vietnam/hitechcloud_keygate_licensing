package handler

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// TestStripePriceUniqueIndex: the partial unique index is what holds
// the one-price-one-plan invariant under concurrent writes, and the
// handler must recognise its violation to answer 409 instead of 500.
func TestStripePriceUniqueIndex(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := store.New(dsn)
	if err != nil {
		t.Skipf("skipping integration test: %v", err)
	}
	defer s.Close()
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	ctx := context.Background()
	suffix := time.Now().Format("150405.000")
	prod := &model.Product{Name: "PriceIdx", Slug: "priceidx-" + suffix, Type: "hybrid"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("create product: %v", err)
	}
	price := "price_idx_" + suffix
	a := &model.Plan{ProductID: prod.ID, Name: "A", Slug: "a-" + suffix, LicenseType: "perpetual", LicenseModel: "standard", StripePriceID: price}
	if err := s.CreatePlan(ctx, a); err != nil {
		t.Fatalf("create plan a: %v", err)
	}
	b := &model.Plan{ProductID: prod.ID, Name: "B", Slug: "b-" + suffix, LicenseType: "perpetual", LicenseModel: "standard", StripePriceID: price}
	err = s.CreatePlan(ctx, b)
	if err == nil {
		t.Fatalf("second plan with the same stripe price was accepted by the database")
	}
	if !isStripePriceConflict(err) {
		t.Fatalf("violation not recognised as a price conflict: %v", err)
	}
	// Empty prices are not part of the invariant.
	c := &model.Plan{ProductID: prod.ID, Name: "C", Slug: "c-" + suffix, LicenseType: "perpetual", LicenseModel: "standard"}
	d := &model.Plan{ProductID: prod.ID, Name: "D", Slug: "d-" + suffix, LicenseType: "perpetual", LicenseModel: "standard"}
	if err := s.CreatePlan(ctx, c); err != nil {
		t.Fatalf("create plan c: %v", err)
	}
	if err := s.CreatePlan(ctx, d); err != nil {
		t.Fatalf("create plan d (second empty price): %v", err)
	}
}
