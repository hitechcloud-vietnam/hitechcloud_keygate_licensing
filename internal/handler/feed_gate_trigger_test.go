package handler

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// The feed-gating invariant holds in the database, not only in the
// handlers: a finite cutoff or a bounded plan cannot appear on a
// product with public feeds, a product cannot ungate while they exist,
// and two such writes racing are serialised per product.
func TestFeedGateTriggers(t *testing.T) {
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
	prod := &model.Product{Name: "FeedGate", Slug: "fgate-" + suffix, Type: "desktop"} // feeds public
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatal(err)
	}
	plain := &model.Plan{ProductID: prod.ID, Name: "Plain", Slug: "fg-plain-" + suffix, LicenseType: "perpetual", LicenseModel: "standard"}
	if err := s.CreatePlan(ctx, plain); err != nil {
		t.Fatal(err)
	}
	lic := &model.License{ProductID: prod.ID, PlanID: plain.ID, Email: "fg-" + suffix + "@example.com", LicenseKey: "KEY-fg-" + suffix, Status: model.StatusActive}
	if err := s.CreateLicense(ctx, lic); err != nil {
		t.Fatal(err)
	}
	violates := func(err error, constraint string) bool {
		return err != nil && strings.Contains(err.Error(), constraint)
	}

	// Public feeds: no bounded plan, no renewals, no finite cutoff.
	bounded := &model.Plan{ProductID: prod.ID, Name: "B", Slug: "fg-b-" + suffix, LicenseType: "perpetual", LicenseModel: "standard", UpdatesDays: 365}
	if err := s.CreatePlan(ctx, bounded); !violates(err, "plans_feed_not_gated") {
		t.Fatalf("bounded plan on public feeds: %v", err)
	}
	plain.RenewalDays, plain.StripeRenewalPriceID = 30, "price_fg_"+suffix
	if err := s.UpdatePlan(ctx, plain); !violates(err, "plans_feed_not_gated") {
		t.Fatalf("renewals on public feeds: %v", err)
	}
	plain.RenewalDays, plain.StripeRenewalPriceID = 0, ""
	if _, err := s.DB.NewRaw("UPDATE licenses SET updates_until = now() WHERE id = ?", lic.ID).Exec(ctx); !violates(err, "licenses_feed_not_gated") {
		t.Fatalf("finite cutoff on public feeds: %v", err)
	}
	// A saas product has no feed to gate.
	saas := &model.Product{Name: "Saas", Slug: "fg-saas-" + suffix, Type: "saas"}
	if err := s.CreateProduct(ctx, saas); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePlan(ctx, &model.Plan{ProductID: saas.ID, Name: "S", Slug: "fg-s-" + suffix, LicenseType: "perpetual", LicenseModel: "standard", UpdatesDays: 365}); err != nil {
		t.Fatalf("bounded plan on a saas product refused: %v", err)
	}

	// Gated: everything above is allowed; ungating is then refused
	// while a bounded plan exists, and still while a finite license
	// cutoff remains after the plan setting is removed.
	prod.FeedLicenseRequired = true
	if err := s.UpdateProduct(ctx, prod); err != nil {
		t.Fatal(err)
	}
	if err := s.CreatePlan(ctx, bounded); err != nil {
		t.Fatalf("bounded plan on gated feeds: %v", err)
	}
	if _, err := s.DB.NewRaw("UPDATE licenses SET updates_until = now() WHERE id = ?", lic.ID).Exec(ctx); err != nil {
		t.Fatalf("finite cutoff on gated feeds: %v", err)
	}
	prod.FeedLicenseRequired = false
	if err := s.UpdateProduct(ctx, prod); !violates(err, "products_feed_gate_in_use") {
		t.Fatalf("ungating with a bounded plan: %v", err)
	}
	bounded.UpdatesDays = 0
	if err := s.UpdatePlan(ctx, bounded); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateProduct(ctx, prod); !violates(err, "products_feed_gate_in_use") {
		t.Fatalf("ungating with a finite license cutoff: %v", err)
	}
	if _, err := s.DB.NewRaw("UPDATE licenses SET updates_until = NULL WHERE id = ?", lic.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateProduct(ctx, prod); err != nil {
		t.Fatalf("ungating with nothing bounded left: %v", err)
	}

	// A saas product may hold a bounded plan with public feeds (no
	// feed exists), but cannot become desktop or hybrid like that.
	if _, err := s.DB.NewRaw("UPDATE products SET type = 'desktop' WHERE id = ?", saas.ID).Exec(ctx); !violates(err, "products_feed_gate_in_use") {
		t.Fatalf("saas with a bounded plan turned into desktop with public feeds: %v", err)
	}
	if _, err := s.DB.NewRaw("UPDATE products SET type = 'desktop', feed_license_required = true WHERE id = ?", saas.ID).Exec(ctx); err != nil {
		t.Fatalf("type change with gating on: %v", err)
	}

	// Race: one transaction ungates, another adds a bounded plan; the
	// per-product lock makes the second wait and then fail.
	prod.FeedLicenseRequired = true
	if err := s.UpdateProduct(ctx, prod); err != nil {
		t.Fatal(err)
	}
	tx1, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx1.Rollback()
	if _, err := tx1.NewRaw("UPDATE products SET feed_license_required = false WHERE id = ?", prod.ID).Exec(ctx); err != nil {
		t.Fatalf("tx1 ungate: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		tx2, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			done <- err
			return
		}
		defer tx2.Rollback()
		if _, err := tx2.NewRaw("UPDATE plans SET updates_days = 365 WHERE id = ?", bounded.ID).Exec(ctx); err != nil {
			done <- err
			return
		}
		done <- tx2.Commit()
	}()
	select {
	case err := <-done:
		t.Fatalf("second writer did not wait for the first: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx1.Commit(); err != nil {
		t.Fatalf("tx1 commit: %v", err)
	}
	select {
	case err := <-done:
		if !violates(err, "plans_feed_not_gated") {
			t.Fatalf("bounded plan committed against an ungated product: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second writer still blocked")
	}

	// The reverse: a bounded plan is being added while an ungate
	// waits; after the plan commits the ungate must see it and fail.
	prod.FeedLicenseRequired = true
	if err := s.UpdateProduct(ctx, prod); err != nil {
		t.Fatal(err)
	}
	tx3, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx3.Rollback()
	if _, err := tx3.NewRaw("UPDATE plans SET updates_days = 365 WHERE id = ?", bounded.ID).Exec(ctx); err != nil {
		t.Fatalf("tx3 bounded plan: %v", err)
	}
	done2 := make(chan error, 1)
	go func() {
		tx4, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			done2 <- err
			return
		}
		defer tx4.Rollback()
		if _, err := tx4.NewRaw("UPDATE products SET feed_license_required = false WHERE id = ?", prod.ID).Exec(ctx); err != nil {
			done2 <- err
			return
		}
		done2 <- tx4.Commit()
	}()
	select {
	case err := <-done2:
		t.Fatalf("ungate did not wait for the plan writer: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx3.Commit(); err != nil {
		t.Fatalf("tx3 commit: %v", err)
	}
	select {
	case err := <-done2:
		if !violates(err, "products_feed_gate_in_use") {
			t.Fatalf("ungate committed against a bounded plan committed after its statement began: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ungate still blocked")
	}
}
