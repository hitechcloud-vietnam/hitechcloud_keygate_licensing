package handler

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// The trigger keeps renewal prices and purchase prices disjoint even
// for writes that bypass the admin handler, and its violation reads
// as a price conflict so the API answers 409.
func TestRenewalPriceDisjointTrigger(t *testing.T) {
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
	prod := &model.Product{Name: "PriceDisjoint", Slug: "pdis-" + suffix, Type: "desktop", FeedLicenseRequired: true}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatal(err)
	}
	buy, renew := "price_buy_"+suffix, "price_renew_"+suffix
	a := &model.Plan{ProductID: prod.ID, Name: "A", Slug: "a-" + suffix, LicenseType: "perpetual", LicenseModel: "standard",
		StripePriceID: buy, RenewalDays: 365, StripeRenewalPriceID: renew}
	if err := s.CreatePlan(ctx, a); err != nil {
		t.Fatalf("plan a: %v", err)
	}
	cases := map[string]*model.Plan{
		"renewal price equal to another plan's purchase price": {ProductID: prod.ID, Name: "B", Slug: "b-" + suffix, LicenseType: "perpetual", LicenseModel: "standard", RenewalDays: 30, StripeRenewalPriceID: buy},
		"purchase price equal to another plan's renewal price": {ProductID: prod.ID, Name: "C", Slug: "c-" + suffix, LicenseType: "perpetual", LicenseModel: "standard", StripePriceID: renew},
		"renewal price equal to own purchase price":            {ProductID: prod.ID, Name: "D", Slug: "d-" + suffix, LicenseType: "perpetual", LicenseModel: "standard", StripePriceID: "price_own_" + suffix, RenewalDays: 30, StripeRenewalPriceID: "price_own_" + suffix},
	}
	for name, p := range cases {
		err := s.CreatePlan(ctx, p)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !isStripePriceConflict(err) {
			t.Errorf("%s: not reported as a price conflict: %v", name, err)
		}
	}
	// Two plans may share a renewal price: the session names the license.
	e := &model.Plan{ProductID: prod.ID, Name: "E", Slug: "e-" + suffix, LicenseType: "perpetual", LicenseModel: "standard", RenewalDays: 30, StripeRenewalPriceID: renew}
	if err := s.CreatePlan(ctx, e); err != nil {
		t.Fatalf("shared renewal price refused: %v", err)
	}
	// Updates are checked too.
	a.StripePriceID = renew
	if err := s.UpdatePlan(ctx, a); err == nil || !isStripePriceConflict(err) {
		t.Fatalf("update overlapping prices: %v", err)
	}
}

// Two transactions assigning one value as a purchase price and as a
// renewal price cannot both commit: the trigger locks the price for
// the transaction, so the second writer waits for the first and then
// sees its row.
func TestRenewalPriceDisjointTrigger_Serialises(t *testing.T) {
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
	prod := &model.Product{Name: "PriceRace", Slug: "prace-" + suffix, Type: "desktop", FeedLicenseRequired: true}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatal(err)
	}
	a := &model.Plan{ProductID: prod.ID, Name: "A", Slug: "ra-" + suffix, LicenseType: "perpetual", LicenseModel: "standard"}
	b := &model.Plan{ProductID: prod.ID, Name: "B", Slug: "rb-" + suffix, LicenseType: "perpetual", LicenseModel: "standard"}
	for _, p := range []*model.Plan{a, b} {
		if err := s.CreatePlan(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	price := "price_race_" + suffix

	tx1, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx1.Rollback()
	if _, err := tx1.NewRaw("UPDATE plans SET stripe_price_id = ? WHERE id = ?", price, a.ID).Exec(ctx); err != nil {
		t.Fatalf("tx1 update: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		tx2, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			done <- err
			return
		}
		defer tx2.Rollback()
		if _, err := tx2.NewRaw("UPDATE plans SET renewal_days = 30, stripe_renewal_price_id = ? WHERE id = ?", price, b.ID).Exec(ctx); err != nil {
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
		if err == nil || !isStripePriceConflict(err) {
			t.Fatalf("second writer must fail with a price conflict after the first commits: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second writer still blocked after the first committed")
	}

	// The reverse order: the renewal price is written first and the
	// purchase-price writer waits; it must fail once the first commits.
	price2 := "price_race2_" + suffix
	tx3, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx3.Rollback()
	if _, err := tx3.NewRaw("UPDATE plans SET renewal_days = 30, stripe_renewal_price_id = ? WHERE id = ?", price2, b.ID).Exec(ctx); err != nil {
		t.Fatalf("tx3 renewal price: %v", err)
	}
	done2 := make(chan error, 1)
	go func() {
		tx4, err := s.DB.BeginTx(ctx, nil)
		if err != nil {
			done2 <- err
			return
		}
		defer tx4.Rollback()
		if _, err := tx4.NewRaw("UPDATE plans SET stripe_price_id = ? WHERE id = ?", price2, a.ID).Exec(ctx); err != nil {
			done2 <- err
			return
		}
		done2 <- tx4.Commit()
	}()
	select {
	case err := <-done2:
		t.Fatalf("purchase-price writer did not wait: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx3.Commit(); err != nil {
		t.Fatalf("tx3 commit: %v", err)
	}
	select {
	case err := <-done2:
		if err == nil || !isStripePriceConflict(err) {
			t.Fatalf("purchase price committed against a renewal price committed after its statement began: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("purchase-price writer still blocked")
	}
}
