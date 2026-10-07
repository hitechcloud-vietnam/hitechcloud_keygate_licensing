package payment

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun/driver/pgdriver"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// TestUpgradePath_LegacyDataAndReplays runs the schema an existing
// install has (everything before the 20260907 migrations) on a fresh
// database, seeds the data shapes such an install holds, and then
// applies the new migrations. It checks what the reviews were about:
//   - a duplicate Stripe price blocks the upgrade with a clear message,
//   - legacy fulfilment claims become done markers, legacy event rows
//     get done markers,
//   - a replayed legacy session is "fulfilled" and produces no second
//     license, a resent legacy event is skipped.
func TestUpgradePath_LegacyDataAndReplays(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	admin := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn)))
	defer admin.Close()
	if err := admin.Ping(); err != nil {
		t.Skipf("ping: %v", err)
	}
	dbName := "keygate_upg_" + time.Now().Format("150405")
	if _, err := admin.Exec("CREATE DATABASE " + dbName); err != nil {
		t.Fatalf("create database: %v", err)
	}
	defer admin.Exec("DROP DATABASE IF EXISTS " + dbName)

	u, _ := url.Parse(dsn)
	u.Path = "/" + dbName
	s, err := store.New(u.String())
	if err != nil {
		t.Fatalf("connect fresh db: %v", err)
	}
	defer s.Close()
	ctx := context.Background()

	// ── the schema an existing install runs ──
	const migrations = "../../db/migrations"
	legacyDir := t.TempDir()
	entries, _ := os.ReadDir(migrations)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "20260907_") {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(migrations, e.Name()))
		os.WriteFile(filepath.Join(legacyDir, e.Name()), b, 0o644)
	}
	if err := s.RunMigrations(legacyDir); err != nil {
		t.Fatalf("legacy migrations: %v", err)
	}

	// ── data shapes such an install holds ──
	prod := &model.Product{Name: "Legacy", Slug: "legacy", Type: "hybrid"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("product: %v", err)
	}
	planA := &model.Plan{ProductID: prod.ID, Name: "A", Slug: "a", LicenseType: "perpetual", LicenseModel: "standard", StripePriceID: "price_shared"}
	planB := &model.Plan{ProductID: prod.ID, Name: "B", Slug: "b", LicenseType: "perpetual", LicenseModel: "standard", StripePriceID: "price_shared"}
	for _, p := range []*model.Plan{planA, planB} {
		if err := s.CreatePlan(ctx, p); err != nil {
			t.Fatalf("plan: %v", err)
		}
	}
	legacySession, legacyEvent := "cs_legacy_1", "evt_legacy_1"
	// Raw insert: the current model carries columns the old schema
	// does not have yet.
	if _, err := s.DB.NewRaw(`INSERT INTO licenses (id, product_id, plan_id, email, license_key, status, payment_provider)
		VALUES (?, ?, ?, 'legacy@example.com', 'KEY-legacy', 'active', 'stripe')`, store.NewID(), prod.ID, planA.ID).Exec(ctx); err != nil {
		t.Fatalf("license: %v", err)
	}
	s.TryRecordProcessedEvent(ctx, "stripe_fulfill", legacySession) // never released by old code
	s.TryRecordProcessedEvent(ctx, "stripe", legacyEvent)           // processed event, old model

	// ── upgrade: refused while the price is ambiguous ──
	err = s.RunMigrations(migrations)
	if err == nil || !strings.Contains(err.Error(), "price_shared") || !strings.Contains(err.Error(), "B (") {
		t.Fatalf("upgrade must refuse the duplicate price and name the plans, got: %v", err)
	}
	if _, err := s.DB.NewUpdate().TableExpr("plans").Set("stripe_price_id = ''").Where("id = ?", planB.ID).Exec(ctx); err != nil {
		t.Fatalf("fix duplicate: %v", err)
	}
	if err := s.RunMigrations(migrations); err != nil {
		t.Fatalf("upgrade after fix: %v", err)
	}

	// ── legacy rows read as done without any conversion (this is
	// what keeps a rolling upgrade safe: rows an old instance writes
	// after the migration look exactly like these) ──
	if !s.IsEventProcessed(ctx, fulfilledSessionProvider, legacySession) || !s.IsEventProcessed(ctx, processedEventDoneProvider, legacyEvent) {
		t.Fatalf("legacy markers must read as done markers")
	}
	// Rolling upgrade: an old instance fulfils a session after the
	// migration — claim-at-start marker, license without session id.
	rollingSession := "cs_rolling_1"
	s.TryRecordProcessedEvent(ctx, "stripe_fulfill", rollingSession)
	if _, err := s.DB.NewRaw(`INSERT INTO licenses (id, product_id, plan_id, email, license_key, status, payment_provider)
		VALUES (?, ?, ?, 'rolling@example.com', 'KEY-rolling', 'active', 'stripe')`, store.NewID(), prod.ID, planA.ID).Exec(ctx); err != nil {
		t.Fatalf("rolling license: %v", err)
	}
	s.DB.NewUpdate().TableExpr("processed_events").Set("created_at = now() - interval '1 hour'").Where("event_id = ?", rollingSession).Exec(ctx)
	var n int
	s.DB.NewRaw("SELECT count(*) FROM pg_indexes WHERE indexname IN ('idx_plans_stripe_price_unique','idx_licenses_stripe_checkout_session','idx_licenses_stripe_payment_intent')").Scan(ctx, &n)
	if n != 3 {
		t.Fatalf("expected the three new indexes, found %d", n)
	}

	// ── behaviour on replays ──
	h := &StripeHandler{Store: s}
	h.SetWebhookSecret("whsec_upg")
	ok, err := h.fulfillCheckout(ctx, "legacy@example.com", "", "", "", map[string]string{"plan_id": planA.ID, "session_id": legacySession}, "test")
	if !ok || err != nil {
		t.Fatalf("legacy session replay: ok=%v err=%v", ok, err)
	}
	lics, _ := s.ListLicensesByEmail(ctx, "legacy@example.com")
	if len(lics) != 1 {
		t.Fatalf("legacy session replay created a second license (%d)", len(lics))
	}
	ok, err = h.fulfillCheckout(ctx, "rolling@example.com", "", "", "", map[string]string{"plan_id": planA.ID, "session_id": rollingSession}, "test")
	if !ok || err != nil {
		t.Fatalf("rolling-upgrade session replay: ok=%v err=%v", ok, err)
	}
	if lics, _ := s.ListLicensesByEmail(ctx, "rolling@example.com"); len(lics) != 1 {
		t.Fatalf("old-instance marker (1h old) was taken over as stale: %d licenses", len(lics))
	}
	code, out := signedWebhook(t, h, "whsec_upg", legacyEvent, "customer.subscription.updated", map[string]any{"id": "sub_x", "object": "subscription", "status": "active"})
	if code != 200 || out["skipped"] != true {
		t.Fatalf("resent legacy event must be skipped, got %d %v", code, out)
	}
	// A new session on the upgraded schema records its id and marker.
	ok, err = h.fulfillCheckout(ctx, "new@example.com", "", "", "pi_new", map[string]string{"plan_id": planA.ID, "session_id": "cs_new_1"}, "test")
	if !ok || err != nil {
		t.Fatalf("new session: ok=%v err=%v", ok, err)
	}
	got, err := s.FindLicenseByStripeCheckoutSession(ctx, "cs_new_1")
	if err != nil || got.StripePaymentIntentID != "pi_new" || !s.IsEventProcessed(ctx, fulfilledSessionProvider, "cs_new_1") {
		t.Fatalf("new license not fully recorded: %v %+v", err, got)
	}
	fmt.Fprintln(os.Stderr, "upgrade path verified on", dbName)
}
