// The §70 sort whitelist, asserted at the seam the store sees.
//
// The handler resolves ?sort= and ?order= through a per-resource
// allowlist and hands the store exactly one store.Sort; the store
// renders exactly that (the statement shape is pinned in
// internal/store/admin_sort_query_test.go). So these fakes capture
// what crossed the seam: a name in the map resolves to its column and
// direction — a caller sorting "name asc" gets rows ordered that way
// — and anything not in the map resolves to the endpoint's default,
// the order the list answered in before sorting existed. Neither is
// ever an error: an unknown sorter still gets its rows.
package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

func TestOrderListSortWhitelist(t *testing.T) {
	for _, tc := range []struct {
		query    string
		wantExpr string
		wantDesc bool
	}{
		// The default: the ledger as it has always read.
		{"", `"order".created_at`, true},
		// Whitelisted names, both directions.
		{"?sort=order_number&order=asc", `"order".order_number`, false},
		{"?sort=customer&order=asc", `lower("order".customer_email)`, false},
		{"?sort=total", `"order".total_minor`, true},
		{"?sort=paid_at&order=asc", `"order".paid_at`, false},
		{"?sort=status&order=desc", `"order".status`, true},
		// Not in the map: the default ordering, still served.
		{"?sort=bogus", `"order".created_at`, true},
		{"?sort=" + url.QueryEscape(`"order".total_minor`), `"order".created_at`, true},
		{"?sort=" + url.QueryEscape("total; DROP TABLE orders"), `"order".created_at`, true},
		// A malformed ?order= is descending, never the caller's
		// spelling.
		{"?sort=status&order=ascending", `"order".status`, true},
	} {
		f := newFakeOrderAdminStore()
		h := &OrderAdminHandler{Store: f}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/admin/orders"+tc.query, nil)
		h.List(c)
		if w.Code != http.StatusOK {
			t.Errorf("List(%q) status = %d, want 200 — an unknown sort is never an error here", tc.query, w.Code)
		}
		if f.lastSort.Expr != tc.wantExpr || f.lastSort.Desc != tc.wantDesc {
			t.Errorf("List(%q) handed the store {Expr:%q Desc:%v}, want {Expr:%q Desc:%v}",
				tc.query, f.lastSort.Expr, f.lastSort.Desc, tc.wantExpr, tc.wantDesc)
		}
	}
}

func TestInvoiceListSortWhitelist(t *testing.T) {
	for _, tc := range []struct {
		query    string
		wantExpr string
		wantDesc bool
	}{
		// The default reads oldest first — the order the documents
		// were drawn in. A column's natural direction is its own, not
		// one endpoint-wide default.
		{"", `invoice.created_at`, false},
		{"?sort=invoice_number&order=desc", `invoice.invoice_number`, true},
		{"?sort=total", `invoice.total_minor`, true},
		{"?sort=due_at&order=asc", `invoice.due_at`, false},
		{"?sort=bogus", `invoice.created_at`, false},
	} {
		f := newFakeOrderAdminStore()
		h := &OrderAdminHandler{Store: f}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/admin/orders/ord-1/invoices"+tc.query, nil)
		c.Params = gin.Params{{Key: "id", Value: "ord-1"}}
		h.ListInvoices(c)
		if w.Code != http.StatusOK {
			t.Errorf("ListInvoices(%q) status = %d, want 200", tc.query, w.Code)
		}
		if f.lastSort.Expr != tc.wantExpr || f.lastSort.Desc != tc.wantDesc {
			t.Errorf("ListInvoices(%q) handed the store {Expr:%q Desc:%v}, want {Expr:%q Desc:%v}",
				tc.query, f.lastSort.Expr, f.lastSort.Desc, tc.wantExpr, tc.wantDesc)
		}
	}
}

func TestRefundListSortWhitelist(t *testing.T) {
	for _, tc := range []struct {
		query    string
		wantExpr string
		wantDesc bool
	}{
		{"", `refund.created_at`, true},
		{"?sort=amount&order=asc", `refund.amount_minor`, false},
		{"?sort=provider", `refund.payment_provider`, false},
		{"?sort=bogus", `refund.created_at`, true},
	} {
		f := newFakeRefundsAdminStore()
		f.seed("ord-1", 1000, model.OrderStatusPaid)
		h := &RefundsAdminHandler{Store: f}
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/admin/orders/ord-1/refunds"+tc.query, nil)
		c.Params = gin.Params{{Key: "id", Value: "ord-1"}}
		h.ListRefunds(c)
		if w.Code != http.StatusOK {
			t.Errorf("ListRefunds(%q) status = %d, want 200", tc.query, w.Code)
		}
		if f.lastSort.Expr != tc.wantExpr || f.lastSort.Desc != tc.wantDesc {
			t.Errorf("ListRefunds(%q) handed the store {Expr:%q Desc:%v}, want {Expr:%q Desc:%v}",
				tc.query, f.lastSort.Expr, f.lastSort.Desc, tc.wantExpr, tc.wantDesc)
		}
	}
}

func TestNotificationListSortWhitelist(t *testing.T) {
	for _, tc := range []struct {
		query    string
		wantExpr string
		wantDesc bool
	}{
		{"", `"user_notification".created_at`, true},
		{"?sort=event&order=asc", `"user_notification".event`, false},
		{"?sort=priority", `"user_notification".priority`, true},
		{"?sort=bogus", `"user_notification".created_at`, true},
	} {
		h, f := newNotificationCenterHarness()
		code, _ := notificationCall(h.List, http.MethodGet, "u-1", "/portal/notifications"+tc.query, nil)
		if code != http.StatusOK {
			t.Errorf("List(%q) status = %d, want 200", tc.query, code)
		}
		if f.lastSort.Expr != tc.wantExpr || f.lastSort.Desc != tc.wantDesc {
			t.Errorf("List(%q) handed the store {Expr:%q Desc:%v}, want {Expr:%q Desc:%v}",
				tc.query, f.lastSort.Expr, f.lastSort.Desc, tc.wantExpr, tc.wantDesc)
		}
	}
}
