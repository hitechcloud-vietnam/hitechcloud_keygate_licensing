package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// TestRetentionBatchDeleteSemantics pins §92's deletion mechanics on
// the real table shape:
//
//	one batch deletes at most RetentionBatchSize rows
//	the drain loop converges and is idempotent (second run → 0)
//	rows newer than the cutoff are never touched
func TestRetentionBatchDeleteSemantics(t *testing.T) {
	s := reviewsTestDB(t)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	suffix := refundsSuffix()
	now := time.Now().UTC()
	cutoff := now.Add(-24 * time.Hour)

	// 1500 old rows + 2 fresh ones: two full batches and a leftover.
	var vals []string
	var args []any
	for i := 0; i < 1500; i++ {
		vals = append(vals, fmt.Sprintf("('pe-%s-%d', 'stripe', ?, ?)", suffix, i))
		args = append(args, fmt.Sprintf("evt-%s-%d", suffix, i), now.Add(-48*time.Hour))
	}
	for i := 0; i < 2; i++ {
		vals = append(vals, fmt.Sprintf("('pe-new-%s-%d', 'stripe', ?, ?)", suffix, i))
		args = append(args, fmt.Sprintf("evt-new-%s-%d", suffix, i), now)
	}
	if _, err := s.DB.ExecContext(ctx,
		`INSERT INTO processed_events (id, provider, event_id, created_at) VALUES `+strings.Join(vals, ","),
		args...); err != nil {
		t.Fatalf("seed processed_events: %v", err)
	}

	// The drain loop the retention job runs: batches until one comes
	// back short.
	total := int64(0)
	for {
		n, err := s.DeleteProcessedEventsBefore(ctx, cutoff, RetentionBatchSize)
		if err != nil {
			t.Fatalf("delete batch: %v", err)
		}
		total += n
		if n < RetentionBatchSize {
			break
		}
	}
	if total != 1500 {
		t.Errorf("drained %d rows, want exactly the 1500 old ones", total)
	}

	// Idempotent: a second sweep finds nothing old.
	n, err := s.DeleteProcessedEventsBefore(ctx, cutoff, RetentionBatchSize)
	if err != nil {
		t.Fatalf("second sweep: %v", err)
	}
	if n != 0 {
		t.Errorf("second sweep deleted %d rows, want 0", n)
	}

	// The fresh rows survived.
	var left int
	if err := s.DB.NewRaw(
		`SELECT count(*) FROM processed_events WHERE provider = 'stripe' AND event_id LIKE ?`,
		"evt-new-"+suffix+"-%").Scan(ctx, &left); err != nil {
		t.Fatalf("count survivors: %v", err)
	}
	if left != 2 {
		t.Errorf("fresh rows left = %d, want 2", left)
	}
}

// TestRetentionNeverDeletesFinancialRecords pins the §92 hard rule:
// a retention sweep of every expiring table leaves the financial
// record set — orders and their refunds — completely untouched.
func TestRetentionNeverDeletesFinancialRecords(t *testing.T) {
	s := refundsTestDB(t)
	ctx := context.Background()
	suffix := refundsSuffix()
	now := time.Now().UTC()
	o := refundSeedOrder(t, s, ctx, suffix, 900)
	if _, err := s.RecordRefund(ctx, &model.Refund{OrderID: o.ID, AmountMinor: 900,
		Currency: o.Currency, Status: model.RefundStatusSucceeded}); err != nil {
		t.Fatalf("record refund: %v", err)
	}

	// Seed one expiring row in every swept table (notifications and
	// webhook_deliveries hang off a licence / a webhook).
	prod := &model.Product{Name: "RET " + suffix, Slug: "ret-" + suffix, Type: "hybrid"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("product: %v", err)
	}
	plan := &model.Plan{ProductID: prod.ID, Name: "RET Plan " + suffix, Slug: "ret-plan-" + suffix,
		LicenseType: "perpetual", LicenseModel: "standard", Active: true}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("plan: %v", err)
	}
	lic := &model.License{ProductID: prod.ID, PlanID: plan.ID,
		Email: "ret-" + suffix + "@example.com", LicenseKey: "RETKEY" + suffix, Status: model.StatusActive}
	if _, err := s.DB.NewInsert().Model(lic).Exec(ctx); err != nil {
		t.Fatalf("license: %v", err)
	}

	old := now.Add(-72 * time.Hour)
	seeds := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO notifications (id, license_id, tag, sent_at) VALUES (?, ?, 'retention', ?)`,
			[]any{"nt-" + suffix, lic.ID, old}},
		{`INSERT INTO processed_events (id, provider, event_id, created_at) VALUES (?, 'stripe', ?, ?)`,
			[]any{"pe-" + suffix, "evt-" + suffix, old}},
		{`INSERT INTO webhooks (id, product_id, url, secret) VALUES (?, ?, 'https://example.test/hook', 'sh')`,
			[]any{"wh-" + suffix, prod.ID}},
		{`INSERT INTO webhook_deliveries (id, webhook_id, event, created_at) VALUES (?, ?, 'order.created', ?)`,
			[]any{"wd-" + suffix, "wh-" + suffix, old}},
		{`INSERT INTO audit_logs (id, entity, entity_id, action, created_at) VALUES (?, 'order', ?, 'x', ?)`,
			[]any{"al-" + suffix, o.ID, old}},
	}
	for _, seed := range seeds {
		if _, err := s.DB.ExecContext(ctx, seed.query, seed.args...); err != nil {
			t.Fatalf("seed %q: %v", seed.query, err)
		}
	}

	// Sweep EVERYTHING: a cutoff in the future expires every row in
	// the four log tables.
	future := now.Add(time.Hour)
	for _, del := range []struct {
		name string
		fn   func(context.Context, time.Time, int) (int64, error)
	}{
		{"notifications", s.DeleteNotificationsBefore},
		{"processed_events", s.DeleteProcessedEventsBefore},
		{"webhook_deliveries", s.DeleteWebhookDeliveriesBefore},
		{"audit_logs", s.DeleteAuditLogsBefore},
	} {
		n, err := del.fn(ctx, future, RetentionBatchSize)
		if err != nil {
			t.Fatalf("sweep %s: %v", del.name, err)
		}
		if n == 0 {
			t.Errorf("sweep %s deleted nothing — the seeded row should have expired", del.name)
		}
	}

	// The financial records are still there, unchanged.
	got, err := s.FindOrderByID(ctx, o.ID)
	if err != nil {
		t.Fatalf("order must survive the retention sweep: %v", err)
	}
	if got.Status != model.OrderStatusRefunded || got.RefundedMinor != 900 {
		t.Errorf("order changed by the sweep: %+v", got)
	}
	refs, err := s.ListRefundsByOrder(ctx, o.ID)
	if err != nil {
		t.Fatalf("refunds must survive the retention sweep: %v", err)
	}
	if len(refs) != 1 {
		t.Errorf("refunds left = %d, want 1", len(refs))
	}
}
