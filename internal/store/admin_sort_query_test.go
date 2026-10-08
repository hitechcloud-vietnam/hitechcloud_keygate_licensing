// Query-shape pins for the §70 admin listings that take a sort.
//
// The handler's allowlist decides WHICH column and direction; these
// tests assert what that decision renders as, against the generated
// statement (bun with a nil connection inlines the args, so no
// database is needed). Three things must hold for every listing:
// the default ordering is what the endpoint answered with before
// sorting existed, a caller's column leads while the unique
// tiebreaker stays last (the guarantee behind TestApplySortAlways-
// AppendsTheTiebreaker), and nothing renders as a $n placeholder —
// bun v1.2.18 splices args inline and a stray "$1" would go to
// Postgres as literal text.
package store

import (
	"strings"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// wantShape asserts the statement contains every want and renders no
// $n placeholder.
func wantShape(t *testing.T, name, sqlText string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(sqlText, want) {
			t.Errorf("%s is missing %q; got:\n%s", name, want, sqlText)
		}
	}
	if strings.Contains(sqlText, "$1") || strings.Contains(sqlText, "$2") {
		t.Errorf("%s renders a $n placeholder; args must be inline; got:\n%s", name, sqlText)
	}
}

// wantTiebreakLast asserts the tiebreaker trails the chosen column.
func wantTiebreakLast(t *testing.T, name, sqlText, col, tiebreak string) {
	t.Helper()
	iCol := strings.Index(sqlText, col)
	iTie := strings.Index(sqlText, tiebreak)
	if iCol < 0 || iTie < 0 {
		return // wantShape reports the missing half
	}
	if iTie < iCol {
		t.Errorf("%s puts the tiebreaker %q before the sort column %q; got:\n%s", name, tiebreak, col, sqlText)
	}
}

func TestOrdersListQuerySort(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	var dest []*model.Order
	raw, err := ordersListQuery(db, "acme", "paid", Sort{}, &dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)
	// The ledger's default: newest first, total order under id. The
	// search and status args land inline.
	wantShape(t, "orders default", sqlText,
		`"order".created_at DESC NULLS LAST`, `"order".id DESC`, "acme", "paid")

	dest = nil
	raw, err = ordersListQuery(db, "", "", Sort{Expr: `"order".order_number`}, &dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText = string(raw)
	wantShape(t, "orders by number", sqlText,
		`"order".order_number ASC NULLS LAST`, `"order".id DESC`)
	wantTiebreakLast(t, "orders by number", sqlText, `"order".order_number`, `"order".id DESC`)

	dest = nil
	raw, err = ordersListQuery(db, "", "", Sort{Expr: `"order".total_minor`, Desc: true}, &dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	wantShape(t, "orders by total", string(raw), `"order".total_minor DESC NULLS LAST`)
}

func TestInvoicesByOrderQuerySort(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	var dest []*model.Invoice
	raw, err := invoicesByOrderQuery(db, "ord-1", Sort{}, &dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)
	// Oldest first by default — the order the documents were drawn in,
	// which this endpoint has always answered in.
	wantShape(t, "invoices default", sqlText,
		`invoice.created_at ASC NULLS LAST`, `invoice.id DESC`, "ord-1")

	dest = nil
	raw, err = invoicesByOrderQuery(db, "ord-1", Sort{Expr: "invoice.paid_at", Desc: true}, &dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	wantShape(t, "invoices by paid_at", string(raw), `invoice.paid_at DESC NULLS LAST`, `invoice.id DESC`)
}

func TestProductsAdminQuerySort(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	var dest []*model.Product
	raw, err := productsAdminQuery(db, "", nil, Sort{}, &dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)
	wantShape(t, "products default", sqlText,
		`product.created_at DESC NULLS LAST`, `product.id DESC`)

	dest = nil
	raw, err = productsAdminQuery(db, "kit", []string{"desktop"}, Sort{Expr: "product.name"}, &dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText = string(raw)
	wantShape(t, "products by name", sqlText,
		`product.name ASC NULLS LAST`, `product.id DESC`, "kit", "desktop")
	wantTiebreakLast(t, "products by name", sqlText, `product.name`, `product.id DESC`)
}

func TestUsersAdminQuerySort(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	var dest []*model.User
	raw, err := usersAdminQuery(db, "", Sort{}, &dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)
	// "user" is a reserved word: the alias is quoted and every
	// qualifier carries the quotes.
	wantShape(t, "users default", sqlText,
		`"user".created_at DESC NULLS LAST`, `"user".id DESC`)

	dest = nil
	raw, err = usersAdminQuery(db, "ann", Sort{Expr: `lower("user".email)`}, &dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText = string(raw)
	wantShape(t, "users by email", sqlText,
		`lower("user".email) ASC NULLS LAST`, `"user".id DESC`, "ann")
}

func TestAuditLogsQuerySort(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	var dest []*model.AuditLog
	raw, err := auditLogsQuery(db, "license", "lic-1", "", Sort{}, &dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)
	wantShape(t, "audit default", sqlText,
		`audit_log.created_at DESC NULLS LAST`, `audit_log.id DESC`, "lic-1")

	dest = nil
	raw, err = auditLogsQuery(db, "", "", "prod-1", Sort{Expr: "audit_log.action"}, &dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText = string(raw)
	wantShape(t, "audit by action", sqlText,
		`audit_log.action ASC NULLS LAST`, `audit_log.id DESC`)
}

func TestCouponsListQuerySort(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	var dest []*model.Coupon
	raw, err := couponsListQuery(db, "SAVE", Sort{}, &dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)
	wantShape(t, "coupons default", sqlText,
		`coupon.created_at DESC NULLS LAST`, `coupon.id DESC`, "SAVE")

	dest = nil
	raw, err = couponsListQuery(db, "", Sort{Expr: "coupon.code"}, &dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText = string(raw)
	wantShape(t, "coupons by code", sqlText,
		`coupon.code ASC NULLS LAST`, `coupon.id DESC`)
	wantTiebreakLast(t, "coupons by code", sqlText, `coupon.code`, `coupon.id DESC`)
}

func TestRefundSelectByOrderSort(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	raw, err := refundSelectByOrder(db, "ord-1", Sort{}).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)
	// The refund ledger reads newest first by default; its own
	// append order is immutable (TestRefundTableAlias) and this is
	// the read order on top of it.
	wantShape(t, "refunds default", sqlText,
		`refund.created_at DESC NULLS LAST`, `refund.id DESC`, "ord-1")

	raw, err = refundSelectByOrder(db, "ord-1", Sort{Expr: "refund.amount_minor", Desc: true}).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	wantShape(t, "refunds by amount", string(raw), `refund.amount_minor DESC NULLS LAST`, `refund.id DESC`)
}

func TestUserNotificationsListQueryCustomSort(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	var dest []*model.UserNotification
	raw, err := userNotificationsListQuery(db, "u-1", false, Sort{Expr: `"user_notification".event`}, &dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)
	wantShape(t, "inbox by event", sqlText,
		`"user_notification".event ASC NULLS LAST`, `"user_notification".id DESC`)
	wantTiebreakLast(t, "inbox by event", sqlText, `"user_notification".event`, `"user_notification".id DESC`)
}
