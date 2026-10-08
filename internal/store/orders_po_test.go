package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// poTestOrder writes one order whose billing block is entirely unset —
// the backward-compatibility case: every flow that predates the PO/
// workflow (service.OrderService.CreateOrder, payment.recordOrder)
// constructs model.Order literals that leave all the new fields at
// their zero value, and that shape must keep writing and scanning.
func poTestOrder(t *testing.T, s *store.Store, ctx context.Context, suffix string) *model.Order {
	t.Helper()
	o := &model.Order{
		OrderNumber:   "HTC-PO" + suffix,
		CustomerEmail: "po-" + suffix + "@example.com",
		Currency:      "USD",
		SubtotalMinor: 1000,
		TotalMinor:    1000,
		Status:        model.OrderStatusPending,
		Items: []*model.OrderItem{{
			SKU:               "SKU-PO" + suffix,
			Quantity:          1,
			UnitAmountMinor:   1000,
			LineSubtotalMinor: 1000,
			LineTotalMinor:    1000,
		}},
	}
	if err := s.CreateOrder(ctx, o); err != nil {
		t.Fatalf("create order: %v", err)
	}
	return o
}

// The billing block round-trips: a zero-valued insert scans back empty,
// UpdateOrderBilling writes the set fields, and an explicitly cleared
// field goes back to NULL — the one representation "no value" has.
func TestOrderBillingUpdate_RoundTrip(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := time.Now().Format("150405.000")
	o := poTestOrder(t, s, ctx, suffix)
	defer func() {
		_, _ = s.DB.NewRaw("DELETE FROM order_items WHERE order_id = ?", o.ID).Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM invoices WHERE order_id = ?", o.ID).Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM orders WHERE id = ?", o.ID).Exec(ctx)
	}()

	got, err := s.FindOrderByID(ctx, o.ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.BillingName != "" || got.PONumber != "" || got.BillingCountry != "" ||
		got.CustomerTaxID != "" || got.BillingEmail != "" {
		t.Fatalf("zero-valued billing block scanned back non-empty: %+v", got)
	}

	o.BillingName = "Ada Lovelace"
	o.BillingCompany = "Analytical Engines Ltd"
	o.BillingAddressLine1 = "123 Computation St"
	o.BillingAddressLine2 = "Suite 4"
	o.BillingCity = "Ho Chi Minh City"
	o.BillingRegion = "Southern Vietnam"
	o.BillingPostalCode = "70000"
	o.BillingCountry = "VN"
	o.CustomerTaxID = "VN1234-5678"
	o.PONumber = "PO-2026-0042"
	o.BillingEmail = "ap@example.com"
	if err := s.UpdateOrderBilling(ctx, o.ID, o); err != nil {
		t.Fatalf("update billing: %v", err)
	}

	got, err = s.FindOrderByID(ctx, o.ID)
	if err != nil {
		t.Fatalf("find after update: %v", err)
	}
	for _, tc := range []struct{ got, want, field string }{
		{got.BillingName, "Ada Lovelace", "billing_name"},
		{got.BillingCompany, "Analytical Engines Ltd", "billing_company"},
		{got.BillingAddressLine1, "123 Computation St", "billing_address_line1"},
		{got.BillingAddressLine2, "Suite 4", "billing_address_line2"},
		{got.BillingCity, "Ho Chi Minh City", "billing_city"},
		{got.BillingRegion, "Southern Vietnam", "billing_region"},
		{got.BillingPostalCode, "70000", "billing_postal_code"},
		{got.BillingCountry, "VN", "billing_country"},
		{got.CustomerTaxID, "VN1234-5678", "customer_tax_id"},
		{got.PONumber, "PO-2026-0042", "po_number"},
		{got.BillingEmail, "ap@example.com", "billing_email"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %q, want %q", tc.field, tc.got, tc.want)
		}
	}

	// Clear one field explicitly: empty goes back to NULL in the
	// ledger, and scanning NULL still reads as the empty string.
	o.BillingName = ""
	if err := s.UpdateOrderBilling(ctx, o.ID, o); err != nil {
		t.Fatalf("clear billing_name: %v", err)
	}
	got, err = s.FindOrderByID(ctx, o.ID)
	if err != nil {
		t.Fatalf("find after clear: %v", err)
	}
	if got.BillingName != "" {
		t.Errorf("cleared billing_name scanned back as %q, want empty", got.BillingName)
	}
	var isNull bool
	if err := s.DB.NewRaw("SELECT billing_name IS NULL FROM orders WHERE id = ?", o.ID).
		Scan(ctx, &isNull); err != nil {
		t.Fatalf("check NULL: %v", err)
	}
	if !isNull {
		t.Errorf("cleared billing_name is stored as a value, want NULL")
	}
	if got.PONumber != "PO-2026-0042" {
		t.Errorf("po_number after clear = %q, want it untouched", got.PONumber)
	}
}

// UpdateInvoiceStatus writes the status and stamps the instant the
// transition implies; the instants it does not pass stay untouched.
func TestInvoiceStatusUpdate_StampsTransitionInstants(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := time.Now().Format("150405.000")
	o := poTestOrder(t, s, ctx, suffix+"A")
	inv := &model.Invoice{
		OrderID:       o.ID,
		InvoiceNumber: "INV-PO" + suffix,
		Status:        model.InvoiceStatusDraft,
		Currency:      "USD",
		SubtotalMinor: 1000,
		TotalMinor:    1000,
	}
	if err := s.CreateInvoice(ctx, inv); err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	defer func() {
		_, _ = s.DB.NewRaw("DELETE FROM invoices WHERE id = ?", inv.ID).Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM order_items WHERE order_id = ?", o.ID).Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM orders WHERE id = ?", o.ID).Exec(ctx)
	}()

	// A zero-valued invoice — exactly what the pre-existing writers
	// construct — scans back with both new instants nil.
	got, err := s.FindInvoiceByID(ctx, inv.ID)
	if err != nil {
		t.Fatalf("find invoice: %v", err)
	}
	if got.VoidedAt != nil || got.UncollectibleAt != nil {
		t.Fatalf("new instants scanned back set on a fresh draft: %+v", got)
	}

	now := time.Now()
	if err := s.UpdateInvoiceStatus(ctx, inv.ID, model.InvoiceStatusUncollectible, nil, &now); err != nil {
		t.Fatalf("mark uncollectible: %v", err)
	}
	got, err = s.FindInvoiceByID(ctx, inv.ID)
	if err != nil {
		t.Fatalf("find after uncollectible: %v", err)
	}
	if got.Status != model.InvoiceStatusUncollectible {
		t.Errorf("status = %q, want %q", got.Status, model.InvoiceStatusUncollectible)
	}
	if got.UncollectibleAt == nil {
		t.Errorf("uncollectible_at not stamped")
	}
	if got.VoidedAt != nil {
		t.Errorf("voided_at stamped by a transition that does not set it")
	}

	// A second transition stamps its own instant and leaves the first
	// one alone — the instants are history.
	if err := s.UpdateInvoiceStatus(ctx, inv.ID, model.InvoiceStatusVoid, &now, nil); err != nil {
		t.Fatalf("void: %v", err)
	}
	got, err = s.FindInvoiceByID(ctx, inv.ID)
	if err != nil {
		t.Fatalf("find after void: %v", err)
	}
	if got.Status != model.InvoiceStatusVoid || got.VoidedAt == nil {
		t.Errorf("after void: status=%q voided_at=%v, want void stamped", got.Status, got.VoidedAt)
	}
	if got.UncollectibleAt == nil {
		t.Errorf("uncollectible_at lost by a later transition")
	}
}
