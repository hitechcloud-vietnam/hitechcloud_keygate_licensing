package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// attrTestOrder writes one order, optionally carrying the checkout
// attribution block. Like the billing block (orders_po_test.go), the
// attribution is additive and nullable: a flow that never sets it —
// the ordinary public checkout — must keep writing and scanning.
func attrTestOrder(t *testing.T, s *store.Store, ctx context.Context, suffix string, attr bool) *model.Order {
	t.Helper()
	o := &model.Order{
		OrderNumber:   "HTC-AT" + suffix,
		CustomerEmail: "attr-" + suffix + "@example.com",
		Currency:      "USD",
		SubtotalMinor: 1000,
		TotalMinor:    1000,
		Status:        model.OrderStatusPending,
		Items: []*model.OrderItem{{
			SKU:               "SKU-AT" + suffix,
			Quantity:          1,
			UnitAmountMinor:   1000,
			LineSubtotalMinor: 1000,
			LineTotalMinor:    1000,
		}},
	}
	if attr {
		o.ResellerID = "res_attr_" + suffix
		o.ResellerEmail = "partner-" + suffix + "@example.com"
		o.ReferralCode = "REF" + suffix
		o.AffiliateID = "aff_attr_" + suffix
		o.PaymentProvider = "stripe"
		o.ExternalID = "cs_attr_" + suffix
	}
	if err := s.CreateOrder(ctx, o); err != nil {
		t.Fatalf("create order: %v", err)
	}
	return o
}

// The attribution block round-trips through the ledger: a zero-valued
// insert scans back empty (NULL is "no partner brought this sale"),
// and stamped snapshots come back byte-for-byte — no FK join, no
// re-derivation, which is the whole point of the columns.
func TestOrderAttribution_RoundTrip(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := time.Now().Format("150405.000")
	withAttr := attrTestOrder(t, s, ctx, suffix, true)
	withoutAttr := attrTestOrder(t, s, ctx, "plain"+suffix, false)
	for _, o := range []*model.Order{withAttr, withoutAttr} {
		defer func(id string) {
			_, _ = s.DB.NewRaw("DELETE FROM order_items WHERE order_id = ?", id).Exec(ctx)
			_, _ = s.DB.NewRaw("DELETE FROM invoices WHERE order_id = ?", id).Exec(ctx)
			_, _ = s.DB.NewRaw("DELETE FROM orders WHERE id = ?", id).Exec(ctx)
		}(o.ID)
	}

	got, err := s.FindOrderByID(ctx, withAttr.ID)
	if err != nil {
		t.Fatalf("find attributed: %v", err)
	}
	if got.ResellerID != withAttr.ResellerID || got.ResellerEmail != withAttr.ResellerEmail ||
		got.ReferralCode != withAttr.ReferralCode || got.AffiliateID != withAttr.AffiliateID {
		t.Errorf("attribution = %+v, want the stamped snapshots %+v", got, withAttr)
	}

	// The provider-id lookup recordOrder replays from carries the
	// attribution too — that is where the healing re-read gets it.
	got, err = s.FindOrderByExternalID(ctx, "stripe", "cs_attr_"+suffix)
	if err != nil {
		t.Fatalf("find by external id: %v", err)
	}
	if got.ResellerID != withAttr.ResellerID || got.ReferralCode != withAttr.ReferralCode {
		t.Errorf("external-id lookup lost the attribution: %+v", got)
	}

	plain, err := s.FindOrderByID(ctx, withoutAttr.ID)
	if err != nil {
		t.Fatalf("find plain: %v", err)
	}
	if plain.ResellerID != "" || plain.ResellerEmail != "" || plain.ReferralCode != "" || plain.AffiliateID != "" {
		t.Errorf("zero-valued attribution scanned back non-empty: %+v", plain)
	}

	// The list search stays scoped on customer_email / order_number —
	// attribution never leaked into it — and the new column is simply
	// present on the rows it returns.
	rows, total, err := s.ListOrders(ctx, withAttr.CustomerEmail, "", store.Page{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ResellerID != withAttr.ResellerID {
		t.Errorf("list = %d/%d rows %+v, want one attributed row", total, len(rows), rows)
	}
}
