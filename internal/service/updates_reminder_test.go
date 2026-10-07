package service

import (
	"context"
	"log/slog"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// The reminder claims its (license, tag) row, hands the mail to the
// durable queue and closes the claim. It never waits on SMTP: the
// expiry loop runs serially over every due license.
func TestSendUpdatesEndingReminders_QueuesAndClosesTheClaim(t *testing.T) {
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
	prod := &model.Product{Name: "Remind", Slug: "remind-" + suffix, Type: "desktop", FeedLicenseRequired: true}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatal(err)
	}
	plan := &model.Plan{ProductID: prod.ID, Name: "P", Slug: "remind-" + suffix, LicenseType: "perpetual", LicenseModel: "standard", RenewalDays: 365, StripeRenewalPriceID: "price_remind_" + suffix}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	soon := time.Now().Add(5 * 24 * time.Hour)
	email := "remind-" + suffix + "@example.com"
	lic := &model.License{ProductID: prod.ID, PlanID: plan.ID, Email: email, LicenseKey: "KEY-remind-" + suffix, Status: model.StatusActive, UpdatesUntil: &soon}
	if err := s.CreateLicense(ctx, lic); err != nil {
		t.Fatal(err)
	}
	tag := "updates_14d_" + soon.UTC().Format("2006-01-02")

	// An SMTP host that refuses connections: the run must not care,
	// because nothing is sent from inside the loop.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	c := NewExpiryChecker(s, NewEmailService("127.0.0.1", strconv.Itoa(port), "", "", "keygate@test.local", slog.Default(), s), nil, slog.Default())

	start := time.Now()
	c.SendUpdatesEndingReminders(ctx)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("the reminder pass waited on SMTP: %s", elapsed)
	}
	queued := func() int {
		var n int
		if err := s.DB.NewRaw("SELECT count(*) FROM email_queue WHERE to_addr = ?", email).Scan(ctx, &n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if queued() != 1 {
		t.Fatalf("queued mails after the first run: %d", queued())
	}
	var sent bool
	if err := s.DB.NewRaw("SELECT sent_at IS NOT NULL FROM notifications WHERE license_id = ? AND tag = ?", lic.ID, tag).Scan(ctx, &sent); err != nil || !sent {
		t.Fatalf("queuing must close the claim: sent=%v err=%v", sent, err)
	}
	// A second run has nothing to do.
	c.SendUpdatesEndingReminders(ctx)
	if queued() != 1 {
		t.Fatalf("a second run queued the reminder again: %d", queued())
	}
	if _, err := s.DB.NewRaw("DELETE FROM email_queue WHERE to_addr = ?", email).Exec(ctx); err != nil {
		t.Fatal(err)
	}
}
