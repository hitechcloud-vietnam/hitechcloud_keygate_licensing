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

// refundsTestDB is the TEST_DATABASE_URL-gated store for the refund
// suite — kept local because this file also pins query shapes, which
// needs the unexported builders (package store, not store_test).
func refundsTestDB(t *testing.T) *Store {
	t.Helper()
	s := reviewsTestDB(t)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func refundsSuffix() string { return time.Now().Format("150405.000000") }

// TestRefundTableAlias pins the alias Bun generates for model.Refund
// and the shape of the per-order list query — the bug class
// TestBunDefaultTableAliases pins for the commerce models: the alias
// is the snake_case of the STRUCT name ("refund"), so hand-written
// qualifiers must say "refund".x, never refunds.x.
func TestRefundTableAlias(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	raw, err := db.NewSelect().Model((*model.Refund)(nil)).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	if !strings.Contains(string(raw), `FROM "refunds" AS "refund"`) {
		t.Errorf("model.Refund must alias to \"refund\"; got:\n%s", raw)
	}

	q := refundSelectByOrder(db, "ord_1")
	raw, err = q.AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build list query: %v", err)
	}
	sqlText := string(raw)
	if !strings.Contains(sqlText, `refund.order_id`) {
		t.Errorf("list query must qualify with the struct alias (refund.order_id); got:\n%s", sqlText)
	}
	if strings.Contains(sqlText, `refunds.order_id`) {
		t.Errorf("list query must NEVER qualify with the table name; got:\n%s", sqlText)
	}
	if !strings.Contains(sqlText, `refund.created_at DESC`) {
		t.Errorf("list query must order newest-first via the alias; got:\n%s", sqlText)
	}
}

// refundSeedOrder writes a paid order worth totalMinor.
func refundSeedOrder(t *testing.T, s *Store, ctx context.Context, suffix string, totalMinor int64) *model.Order {
	t.Helper()
	o := &model.Order{
		OrderNumber:     "HTC-RF" + suffix,
		CustomerEmail:   "rf-" + suffix + "@example.com",
		Currency:        "VND",
		SubtotalMinor:   totalMinor,
		TotalMinor:      totalMinor,
		Status:          model.OrderStatusPaid,
		PaymentProvider: "stripe",
	}
	if err := s.CreateOrder(ctx, o); err != nil {
		t.Fatalf("create order: %v", err)
	}
	return o
}

// TestRefundLedgerSync pins the order/refund derivation — the pinned
// business matrix on the store side:
//
//	partial succeeded        → partially_refunded, refunded_minor
//	                           accumulated, refunded_at NIL
//	succeeded reaching total → refunded, refunded_at stamped
//	pending                  → reserves its amount (committed) but
//	                           moves neither refunded_minor nor the
//	                           status; failed frees its amount again.
func TestRefundLedgerSync(t *testing.T) {
	s := refundsTestDB(t)
	ctx := context.Background()
	suffix := refundsSuffix()
	o := refundSeedOrder(t, s, ctx, suffix, 1000)

	// Partial: 400 back.
	r1 := &model.Refund{OrderID: o.ID, PaymentProvider: "stripe", AmountMinor: 400,
		Currency: o.Currency, Reason: "goodwill", Status: model.RefundStatusSucceeded, RefundedBy: "admin"}
	updated, err := s.RecordRefund(ctx, r1)
	if err != nil {
		t.Fatalf("record partial: %v", err)
	}
	if updated.Status != model.OrderStatusPartiallyRefunded {
		t.Errorf("after partial: status = %q, want partially_refunded", updated.Status)
	}
	if updated.RefundedMinor != 400 {
		t.Errorf("after partial: refunded_minor = %d, want 400", updated.RefundedMinor)
	}
	if updated.RefundedAt != nil {
		t.Errorf("after partial: refunded_at must stay nil, got %v", updated.RefundedAt)
	}

	// A pending refund reserves its amount but changes nothing yet.
	r2 := &model.Refund{OrderID: o.ID, PaymentProvider: "zalopay", AmountMinor: 300,
		Currency: o.Currency, Status: model.RefundStatusPending}
	if _, err := s.RecordRefund(ctx, r2); err != nil {
		t.Fatalf("record pending: %v", err)
	}
	committed, succeeded, err := s.RefundSumsByOrder(ctx, o.ID)
	if err != nil {
		t.Fatalf("sums: %v", err)
	}
	if committed != 700 || succeeded != 400 {
		t.Errorf("sums = (%d, %d), want committed 700 / succeeded 400", committed, succeeded)
	}
	got, err := s.FindOrderByID(ctx, o.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != model.OrderStatusPartiallyRefunded || got.RefundedMinor != 400 {
		t.Errorf("pending must not move the order: %+v", got)
	}

	// Reconcile the pending one to failed: its amount frees again.
	if err := s.UpdateRefundStatus(ctx, r2.ID, model.RefundStatusFailed, "", ""); err != nil {
		t.Fatalf("settle failed: %v", err)
	}
	committed, succeeded, err = s.RefundSumsByOrder(ctx, o.ID)
	if err != nil {
		t.Fatalf("sums: %v", err)
	}
	if committed != 400 || succeeded != 400 {
		t.Errorf("after failed: sums = (%d, %d), want (400, 400)", committed, succeeded)
	}

	// Full: the remaining 600 back completes the order.
	r3 := &model.Refund{OrderID: o.ID, PaymentProvider: "manual", AmountMinor: 600,
		Currency: o.Currency, Reason: "refund", Status: model.RefundStatusSucceeded}
	updated, err = s.RecordRefund(ctx, r3)
	if err != nil {
		t.Fatalf("record full: %v", err)
	}
	if updated.Status != model.OrderStatusRefunded {
		t.Errorf("after full: status = %q, want refunded", updated.Status)
	}
	if updated.RefundedMinor != 1000 {
		t.Errorf("after full: refunded_minor = %d, want 1000", updated.RefundedMinor)
	}
	if updated.RefundedAt == nil {
		t.Errorf("after full: refunded_at must be stamped")
	}

	// The list is newest-first and total: three rows.
	rows, err := s.ListRefundsByOrder(ctx, o.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 3 {
		t.Errorf("list has %d rows, want 3", len(rows))
	}
	// A second SyncOrderRefundState changes nothing (idempotent).
	again, err := s.SyncOrderRefundState(ctx, o.ID)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if again.RefundedMinor != 1000 || again.Status != model.OrderStatusRefunded {
		t.Errorf("re-sync drifted: %+v", again)
	}
}

// TestRefundNotSucceededDoesNotComplete pins that only SUCCEEDED
// refunds complete an order: a full-amount PENDING refund leaves the
// order paid (the money may still come back).
func TestRefundNotSucceededDoesNotComplete(t *testing.T) {
	s := refundsTestDB(t)
	ctx := context.Background()
	suffix := refundsSuffix()
	o := refundSeedOrder(t, s, ctx, suffix, 500)

	if _, err := s.RecordRefund(ctx, &model.Refund{OrderID: o.ID, AmountMinor: 500,
		Currency: o.Currency, Status: model.RefundStatusPending}); err != nil {
		t.Fatalf("record pending full: %v", err)
	}
	got, err := s.FindOrderByID(ctx, o.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got.Status != model.OrderStatusPaid {
		t.Errorf("status = %q, want paid (a pending refund completes nothing)", got.Status)
	}
	if got.RefundedMinor != 0 {
		t.Errorf("refunded_minor = %d, want 0", got.RefundedMinor)
	}
}

// TestRevokeLicenseWithReason pins the §80 persistence: the reason is
// validated against the closed vocabulary (an unknown one is refused,
// an empty one folds to the default) and the actor + stamp land on
// the row.
func TestRevokeLicenseWithReason(t *testing.T) {
	s := refundsTestDB(t)
	ctx := context.Background()
	suffix := refundsSuffix()

	prod := &model.Product{Name: "RFL " + suffix, Slug: "rfl-" + suffix, Type: "hybrid"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("product: %v", err)
	}
	plan := &model.Plan{ProductID: prod.ID, Name: "RFL Plan " + suffix, Slug: "rfl-plan-" + suffix,
		LicenseType: "perpetual", LicenseModel: "standard", Active: true}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("plan: %v", err)
	}
	lic := &model.License{ProductID: prod.ID, PlanID: plan.ID,
		Email: "rfl-" + suffix + "@example.com", LicenseKey: "RFLKEY" + suffix, Status: model.StatusActive}
	if _, err := s.DB.NewInsert().Model(lic).Exec(ctx); err != nil {
		t.Fatalf("license: %v", err)
	}

	if err := s.RevokeLicenseWithReason(ctx, lic.ID, "not_a_reason", "admin"); !errors.Is(err, ErrInvalidRevokeReason) {
		t.Fatalf("unknown reason: err = %v, want ErrInvalidRevokeReason", err)
	}

	// Empty reason folds to the default.
	if err := s.RevokeLicenseWithReason(ctx, lic.ID, "", "admin-2"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	got, err := s.FindLicenseByID(ctx, lic.ID)
	if err != nil {
		t.Fatalf("reload license: %v", err)
	}
	if got.Status != model.StatusRevoked {
		t.Errorf("status = %q, want revoked", got.Status)
	}
	if got.RevokeReason != model.DefaultRevokeReason {
		t.Errorf("revoke_reason = %q, want %q", got.RevokeReason, model.DefaultRevokeReason)
	}
	if got.RevokedBy != "admin-2" {
		t.Errorf("revoked_by = %q, want admin-2", got.RevokedBy)
	}
	if got.RevokedAt == nil {
		t.Errorf("revoked_at must be stamped")
	}

	// The vocabulary reason round-trips too.
	if err := s.RevokeLicenseWithReason(ctx, lic.ID, model.RevokeReasonFraud, "security"); err != nil {
		t.Fatalf("revoke with reason: %v", err)
	}
	got, err = s.FindLicenseByID(ctx, lic.ID)
	if err != nil {
		t.Fatalf("reload license: %v", err)
	}
	if got.RevokeReason != model.RevokeReasonFraud || got.RevokedBy != "security" {
		t.Errorf("revoke provenance = (%q, %q), want (fraud, security)", got.RevokeReason, got.RevokedBy)
	}
}
