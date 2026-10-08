package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// TestGatewayPaymentTableAlias pins the alias Bun generates for
// GatewayPayment: snake_case of the STRUCT name → "gateway_payment",
// NOT the table name. Same doctrine as TestBunDefaultTableAliases.
func TestGatewayPaymentTableAlias(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	raw, err := db.NewSelect().Model((*GatewayPayment)(nil)).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	if sqlText := string(raw); !strings.Contains(sqlText, `AS "gateway_payment"`) {
		t.Errorf("generated SQL does not contain `AS \"gateway_payment\"`; got:\n%s", sqlText)
	}
}

// TestGatewayPaymentQueryShape pins the IPN lookup statements without
// a database: the ref lookup is keyed (provider, provider_ref) on the
// bun alias with bound parameters ($n under pgdialect), and the
// settlement claim is a conditional UPDATE gated on
// status = 'pending'.
func TestGatewayPaymentQueryShape(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	dest := new(GatewayPayment)
	raw, err := gatewayPaymentRefQuery(db, "pay2s", "ref-1", dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build ref query: %v", err)
	}
	// NOTE: bun v1.2.18 renders query args CLIENT-SIDE, quoted inline
	// (tooling-notes.md): there are no $n bind placeholders to assert.
	// The pins below assert the same (column, value) pairings the $n
	// numbering would have expressed.
	sqlText := string(raw)
	for _, want := range []string{
		`FROM "gateway_payments" AS "gateway_payment"`,
		"provider = 'pay2s'",
		"provider_ref = 'ref-1'",
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("ref lookup SQL missing %q; got:\n%s", want, sqlText)
		}
	}

	raw, err = gatewayPaymentOrderKeyQuery(db, "pay2s", "HTC-1", dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build order-key query: %v", err)
	}
	sqlText = string(raw)
	for _, want := range []string{
		`FROM "gateway_payments" AS "gateway_payment"`,
		"provider = 'pay2s'",
		"order_key = 'HTC-1'",
		"gateway_payment.id DESC",
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("order-key lookup SQL missing %q; got:\n%s", want, sqlText)
		}
	}

	raw, err = gatewayPaymentStatusUpdate(db, 7, "succeeded", "t-1", `{"a":1}`, true).
		Where("status = 'pending'").AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build claim query: %v", err)
	}
	sqlText = string(raw)
	for _, want := range []string{
		`UPDATE "gateway_payments"`,
		"status = 'succeeded'",
		"updated_at = now()",
		"trans_id = 't-1'",
		"raw = '{\"a\":1}'::jsonb",
		"id = 7",
		"status = 'pending'",
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("settlement-claim SQL missing %q; got:\n%s", want, sqlText)
		}
	}
}

// TestGatewayOrderPaidQueryShape pins the order flip: it writes the
// "order" model (table "orders", alias "order"), and it only fires on
// a still-pending order.
func TestGatewayOrderPaidQueryShape(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	raw, err := gatewayOrderPaidUpdate(db, "ord-1", "lic-1", "ref-1", "gateway:pay2s:ref-1", time.Now()).
		AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)
	for _, want := range []string{
		`UPDATE "orders"`,
		"status = 'paid'",
		"paid_at = '",
		"license_id = 'lic-1'",
		"external_id = 'ref-1'",
		"idempotency_key = 'gateway:pay2s:ref-1'",
		"status = 'pending'",
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("order-paid SQL missing %q; got:\n%s", want, sqlText)
		}
	}
}

// TestGatewayPaymentRoundTrip exercises the row lifecycle against a
// real database when one is configured.
func TestGatewayPaymentRoundTrip(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := New(dsn)
	if err != nil {
		t.Skipf("skipping integration test: %v", err)
	}
	defer s.Close()
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	ctx := context.Background()
	uniq := time.Now().Format("150405.000000")

	order := &model.Order{
		OrderNumber:   "HTC-GW" + uniq,
		CustomerEmail: "gw-" + uniq + "@example.com",
		Currency:      "VND",
		TotalMinor:    22000,
		Status:        model.OrderStatusPending,
		Items: []*model.OrderItem{{
			Description:     "Gateway round trip",
			Quantity:        1,
			UnitAmountMinor: 22000,
			LineTotalMinor:  22000,
		}},
	}
	if err := s.CreateOrder(ctx, order); err != nil {
		t.Fatalf("create order: %v", err)
	}

	gp := &GatewayPayment{
		OrderID: order.ID, Provider: "pay2s", ProviderRef: "ref-" + uniq,
		OrderKey: order.OrderNumber, AmountMinor: 22000, Currency: "VND",
		Status: "pending",
	}
	if err := s.CreateGatewayPayment(ctx, gp); err != nil {
		t.Fatalf("create gateway payment: %v", err)
	}
	if gp.ID == 0 {
		t.Fatal("CreateGatewayPayment did not return the generated id")
	}

	byRef, err := s.GetGatewayPaymentByRef(ctx, "pay2s", gp.ProviderRef)
	if err != nil || byRef.OrderID != order.ID {
		t.Fatalf("by ref: %v %+v", err, byRef)
	}
	byKey, err := s.GetGatewayPaymentByOrderKey(ctx, "pay2s", order.OrderNumber)
	if err != nil || byKey.ID != gp.ID {
		t.Fatalf("by order key: %v %+v", err, byKey)
	}

	claimed, err := s.UpdateGatewayPaymentStatusIfPending(ctx, gp.ID, "succeeded", "t-9", map[string]any{"ok": true})
	if err != nil || !claimed {
		t.Fatalf("first claim: claimed=%v err=%v", claimed, err)
	}
	claimed, err = s.UpdateGatewayPaymentStatusIfPending(ctx, gp.ID, "succeeded", "t-10", nil)
	if err != nil || claimed {
		t.Fatalf("second claim must be refused: claimed=%v err=%v", claimed, err)
	}
	settled, err := s.GetGatewayPaymentByRef(ctx, "pay2s", gp.ProviderRef)
	if err != nil || settled.Status != "succeeded" || settled.TransID != "t-9" {
		t.Fatalf("settled row: %v %+v", err, settled)
	}

	flipped, err := s.MarkGatewayOrderPaid(ctx, order.ID, "lic-gw-test", gp.ProviderRef, "gateway:pay2s:"+gp.ProviderRef, time.Now())
	if err != nil || !flipped {
		t.Fatalf("mark paid: flipped=%v err=%v", flipped, err)
	}
	flipped, err = s.MarkGatewayOrderPaid(ctx, order.ID, "lic-gw-test", gp.ProviderRef, "gateway:pay2s:"+gp.ProviderRef, time.Now())
	if err != nil || flipped {
		t.Fatalf("second mark paid must be a no-op: flipped=%v err=%v", flipped, err)
	}
	paid, err := s.FindOrderByID(ctx, order.ID)
	if err != nil || paid.Status != model.OrderStatusPaid || paid.LicenseID != "lic-gw-test" {
		t.Fatalf("paid order: %v %+v", err, paid)
	}
}
