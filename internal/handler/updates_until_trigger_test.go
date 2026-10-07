package handler

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// The database initialises a missing period itself, so a replica that
// predates the column cannot mint updates-for-life licenses on a
// bounded plan during a rolling deployment.
func TestUpdatesUntilInitTrigger(t *testing.T) {
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
	prod := &model.Product{Name: "InitTrig", Slug: "initrig-" + suffix, Type: "desktop", FeedLicenseRequired: true}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatal(err)
	}
	bounded := &model.Plan{ProductID: prod.ID, Name: "B", Slug: "tb-" + suffix, LicenseType: "perpetual", LicenseModel: "standard", UpdatesDays: 365}
	life := &model.Plan{ProductID: prod.ID, Name: "L", Slug: "tl-" + suffix, LicenseType: "perpetual", LicenseModel: "standard"}
	sub := &model.Plan{ProductID: prod.ID, Name: "S", Slug: "ts-" + suffix, LicenseType: "subscription", LicenseModel: "standard"}
	for _, p := range []*model.Plan{bounded, life, sub} {
		if err := s.CreatePlan(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	// A legacy insert: no updates_until column in the statement.
	legacyInsert := func(id string, plan *model.Plan) {
		_, err := s.DB.NewRaw(`INSERT INTO licenses (id, product_id, plan_id, email, license_key, key_hash, status)
			VALUES (?, ?, ?, ?, ?, ?, 'active')`, id, prod.ID, plan.ID, id+"@example.com", "KEY-"+id, id).Exec(ctx)
		if err != nil {
			t.Fatalf("legacy insert: %v", err)
		}
	}
	until := func(id string) *time.Time {
		var u *time.Time
		if err := s.DB.NewRaw("SELECT updates_until FROM licenses WHERE id = ?", id).Scan(ctx, &u); err != nil {
			t.Fatal(err)
		}
		return u
	}
	legacyInsert("trig-b-"+suffix, bounded)
	if u := until("trig-b-" + suffix); u == nil || u.Sub(time.Now().Add(365*24*time.Hour)).Abs() > time.Minute {
		t.Fatalf("bounded plan insert without period: got %v", u)
	}
	legacyInsert("trig-l-"+suffix, life)
	if u := until("trig-l-" + suffix); u != nil {
		t.Fatalf("updates-for-life plan got a period: %v", u)
	}
	legacyInsert("trig-s-"+suffix, sub)
	if u := until("trig-s-" + suffix); u != nil {
		t.Fatalf("subscription got a period: %v", u)
	}
	// Legacy plan change into a bounded plan (plan_id only).
	if _, err := s.DB.NewRaw("UPDATE licenses SET plan_id = ? WHERE id = ?", bounded.ID, "trig-s-"+suffix).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if u := until("trig-s-" + suffix); u == nil {
		t.Fatal("entering a bounded plan left no period")
	}
	// Perpetual to perpetual keeps a lifetime grant.
	if _, err := s.DB.NewRaw("UPDATE licenses SET plan_id = ? WHERE id = ?", bounded.ID, "trig-l-"+suffix).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if u := until("trig-l-" + suffix); u != nil {
		t.Fatalf("perpetual-to-perpetual move replaced a lifetime grant: %v", u)
	}
	// A replica that predates the column moves plan_id alone. Leaving
	// a perpetual plan must not strand the cutoff on a subscription:
	// it would report nothing to the customer but would keep the feed
	// gate from ever coming off, and the migration from rolling back.
	legacyInsert("trig-m-"+suffix, bounded)
	if u := until("trig-m-" + suffix); u == nil {
		t.Fatal("bounded plan left no period")
	}
	if _, err := s.DB.NewRaw("UPDATE licenses SET plan_id = ? WHERE id = ?", sub.ID, "trig-m-"+suffix).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if u := until("trig-m-" + suffix); u != nil {
		t.Fatalf("leaving a perpetual plan kept the cutoff: %v", u)
	}
	// Entering one again takes the new plan's period, not whatever
	// the row carried before.
	if _, err := s.DB.NewRaw("UPDATE licenses SET updates_until = now() + interval '3650 days' WHERE id = ?", "trig-m-"+suffix).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.NewRaw("UPDATE licenses SET plan_id = ? WHERE id = ?", bounded.ID, "trig-m-"+suffix).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if u := until("trig-m-" + suffix); u == nil || u.Sub(time.Now().AddDate(0, 0, 365)).Abs() > time.Minute {
		t.Fatalf("re-entering a bounded plan kept a stale period: %v", u)
	}

	// The renewals bought into the old period are closed with it: a
	// refund of one of them must not later be subtracted from a
	// period the licence earned on another plan.
	if _, err := s.DB.NewRaw(
		`INSERT INTO license_renewals (id, license_id, days, previous_updates_until, updates_until, stripe_checkout_session_id, stripe_payment_intent_id)
		 VALUES (?, ?, 365, now(), now() + interval '365 days', ?, ?)`,
		"r-trig-"+suffix, "trig-m-"+suffix, "cs-trig-"+suffix, "pi-trig-"+suffix,
	).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.NewRaw("UPDATE licenses SET plan_id = ? WHERE id = ?", sub.ID, "trig-m-"+suffix).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	var open int
	if err := s.DB.NewRaw("SELECT count(*) FROM license_renewals WHERE license_id = ? AND superseded_at IS NULL", "trig-m-"+suffix).Scan(ctx, &open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Fatalf("leaving a perpetual plan left %d renewal(s) open", open)
	}

	// An explicit value is never overridden.
	past := time.Now().Add(-24 * time.Hour)
	if _, err := s.DB.NewRaw("INSERT INTO licenses (id, product_id, plan_id, email, license_key, key_hash, status, updates_until) VALUES (?, ?, ?, ?, ?, ?, 'active', ?)",
		"trig-x-"+suffix, prod.ID, bounded.ID, "x@example.com", "KEY-trig-x-"+suffix, "trig-x-"+suffix, past).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if u := until("trig-x-" + suffix); u == nil || u.Sub(past).Abs() > time.Second {
		t.Fatalf("explicit period overridden: %v", u)
	}
}

// Granting updates for life is a decision, and it has to survive the
// next write that carries plan_id. Without the mark the cleared
// cutoff reads as "nobody decided yet" — what a replica predating the
// column leaves behind — and the trigger would fill the plan's period
// back in.
func TestLifetimeGrantSurvivesALaterPlanWrite(t *testing.T) {
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
	prod := &model.Product{Name: "Life", Slug: "life-" + suffix, Type: "desktop", FeedLicenseRequired: true}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatal(err)
	}
	bounded := &model.Plan{ProductID: prod.ID, Name: "B", Slug: "lgb-" + suffix, LicenseType: "perpetual", LicenseModel: "standard", UpdatesDays: 365}
	if err := s.CreatePlan(ctx, bounded); err != nil {
		t.Fatal(err)
	}
	// A row as a replica predating the column leaves it: no
	// updates_terms_set, the period filled in by the trigger.
	id := "life-" + suffix
	if _, err := s.DB.NewRaw(`INSERT INTO licenses (id, product_id, plan_id, email, license_key, key_hash, status)
		VALUES (?, ?, ?, ?, ?, ?, 'active')`, id, prod.ID, bounded.ID, id+"@example.com", "KEY-"+id, id).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	current, err := s.FindLicenseByID(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if current.UpdatesUntil == nil {
		t.Fatal("the trigger did not fill the period")
	}
	// The admin grants updates for life.
	if applied, err := s.SetLicenseUpdatesUntil(ctx, id, current.UpdatesUntil, nil, true); err != nil || !applied {
		t.Fatalf("grant: applied=%v err=%v", applied, err)
	}
	// A later write carrying plan_id — the same plan — must leave it.
	if _, err := s.DB.NewRaw("UPDATE licenses SET plan_id = ? WHERE id = ?", bounded.ID, id).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	var until *time.Time
	if err := s.DB.NewRaw("SELECT updates_until FROM licenses WHERE id = ?", id).Scan(ctx, &until); err != nil {
		t.Fatal(err)
	}
	if until != nil {
		t.Fatalf("the lifetime grant was undone: %v", until)
	}
}
