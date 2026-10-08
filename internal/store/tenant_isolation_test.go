package store_test

// §61 SECURITY — tenant isolation, the store half of the boundary.
// DB-backed on purpose: the handler fakes prove the QUIET-404 contract,
// but only a real schema+queries run proves that one tenant's rows can
// never surface in another tenant's reads. TEST_DATABASE_URL-gated like
// every integration test here (setupTestDB skips otherwise).
//
// Per-resource ownership tests exist already (TestPortalOrdersByEmail_
// OwnershipAndPaging, TestPortalFindInvoiceForEmail_Ownership,
// TestCustomerWebhookLifecycle's scoping, the inbox ownership suite,
// api_key_portal_test); this is the ACROSS-RESOURCE sweep in one place:
// two tenants, every portal-facing resource, every scoped read.

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

func TestTenantIsolationAcrossPortalResources(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := time.Now().Format("150405.000")

	// Two tenants. The account rows and the commerce rows are separate
	// on purpose: orders/reviews key on customer_email text, webhooks/
	// keys/notifications key on user id — both identity shapes are
	// probed below.
	alice := createWebhookTestUser(t, s)
	bob := createWebhookTestUser(t, s)
	defer deleteWebhookTestData(t, s, alice, bob)
	aliceEmail := "ti-alice-" + suffix + "@example.com"
	bobEmail := "ti-bob-" + suffix + "@example.com"

	// ── orders + invoices (email-keyed) ────────────────────────────
	aOrder := portalTestOrder(t, s, ctx, aliceEmail, suffix+"A", model.OrderStatusPaid)
	bOrder := portalTestOrder(t, s, ctx, bobEmail, suffix+"B", model.OrderStatusPaid)
	aInv := &model.Invoice{
		OrderID:       aOrder.ID,
		InvoiceNumber: "INV-TI" + suffix,
		Status:        model.InvoiceStatusPaid,
		Currency:      "USD",
		SubtotalMinor: 1000,
		TotalMinor:    1000,
	}
	if err := s.CreateInvoice(ctx, aInv); err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	defer func() {
		for _, id := range []string{aOrder.ID, bOrder.ID} {
			_, _ = s.DB.NewRaw("DELETE FROM order_items WHERE order_id = ?", id).Exec(ctx)
			_, _ = s.DB.NewRaw("DELETE FROM invoices WHERE order_id = ?", id).Exec(ctx)
			_, _ = s.DB.NewRaw("DELETE FROM orders WHERE id = ?", id).Exec(ctx)
		}
	}()

	// ── customer webhooks (user-keyed) ─────────────────────────────
	aSecret, _ := model.NewCustomerWebhookSecret()
	bSecret, _ := model.NewCustomerWebhookSecret()
	aHook := &model.CustomerWebhook{UserID: alice, URL: "https://alice.example.com/h", Events: []string{"order.created"}, Active: true}
	if err := s.CreateCustomerWebhook(ctx, aHook, aSecret); err != nil {
		t.Fatalf("create alice webhook: %v", err)
	}
	bHook := &model.CustomerWebhook{UserID: bob, URL: "https://bob.example.com/h", Events: []string{"order.created"}, Active: true}
	if err := s.CreateCustomerWebhook(ctx, bHook, bSecret); err != nil {
		t.Fatalf("create bob webhook: %v", err)
	}

	// ── customer API keys (user-keyed) ─────────────────────────────
	aKeySecret, _ := model.NewCustomerAPIKeySecret()
	aKey := &model.CustomerAPIKey{UserID: alice, Name: "ti alice", Scopes: "orders:read"}
	if err := s.CreateCustomerAPIKey(ctx, aKey, aKeySecret); err != nil {
		t.Fatalf("create alice key: %v", err)
	}
	bKeySecret, _ := model.NewCustomerAPIKeySecret()
	bKey := &model.CustomerAPIKey{UserID: bob, Name: "ti bob", Scopes: "orders:read"}
	if err := s.CreateCustomerAPIKey(ctx, bKey, bKeySecret); err != nil {
		t.Fatalf("create bob key: %v", err)
	}

	// ── user notifications (user-keyed) ────────────────────────────
	aNotif, err := model.NewUserNotification(alice, "order.created", map[string]any{"x": "1"}, "/portal/orders", "")
	if err != nil {
		t.Fatalf("NewUserNotification: %v", err)
	}
	if err := s.CreateUserNotification(ctx, aNotif); err != nil {
		t.Fatalf("create alice notification: %v", err)
	}

	// ── reviews (product + email keyed) ────────────────────────────
	prod := &model.Product{Name: "Tenant Isolation " + suffix, Slug: "ti-" + suffix, Type: "desktop"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("create product: %v", err)
	}
	aReview := &model.Review{ProductID: prod.ID, CustomerEmail: aliceEmail, Rating: 5, Body: "alice's review"}
	if err := s.CreateReview(ctx, aReview); err != nil {
		t.Fatalf("create alice review: %v", err)
	}
	bReview := &model.Review{ProductID: prod.ID, CustomerEmail: bobEmail, Rating: 1, Body: "bob's review"}
	if err := s.CreateReview(ctx, bReview); err != nil {
		t.Fatalf("create bob review: %v", err)
	}
	defer func() {
		_, _ = s.DB.NewRaw("DELETE FROM reviews WHERE product_id = ?", prod.ID).Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM products WHERE id = ?", prod.ID).Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM customer_webhooks WHERE user_id IN (?, ?)", alice, bob).Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM customer_api_keys WHERE user_id IN (?, ?)", alice, bob).Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM user_notifications WHERE user_id IN (?, ?)", alice, bob).Exec(ctx)
	}()

	// ── cross-tenant reads: identical to missing ───────────────────
	if _, err := s.FindOrderByIdAndEmail(ctx, aOrder.ID, bobEmail); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("bob fetched alice's order: err=%v, want sql.ErrNoRows", err)
	}
	if _, err := s.FindInvoiceForEmail(ctx, aInv.ID, bobEmail); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("bob fetched alice's invoice: err=%v, want sql.ErrNoRows", err)
	}
	if got, err := s.FindOrderByIdAndEmail(ctx, aOrder.ID, aliceEmail); err != nil || got.ID != aOrder.ID {
		t.Errorf("the OWNER must still reach her own order: %v %v", got, err)
	}

	// ── scoped lists never contain the other tenant's rows ─────────
	page := store.Page{Limit: 50}
	orders, _, err := s.ListOrdersByEmail(ctx, aliceEmail, "", page)
	if err != nil || len(orders) != 1 || orders[0].ID != aOrder.ID {
		t.Errorf("alice's order list = %d rows %+v err=%v, want only hers", len(orders), orders, err)
	}
	hooks, total, err := s.ListCustomerWebhooksByUser(ctx, alice, page)
	if err != nil || total != 1 || len(hooks) != 1 || hooks[0].ID != aHook.ID {
		t.Errorf("alice's webhook list = %d/%d rows err=%v, want only hers", len(hooks), total, err)
	}
	if hooks[0].UserID != alice {
		t.Error("alice's webhook list must not carry bob's row")
	}
	keys, total, err := s.ListCustomerAPIKeysByUser(ctx, alice, page)
	if err != nil || total != 1 || len(keys) != 1 || keys[0].ID != aKey.ID {
		t.Errorf("alice's key list = %d/%d rows err=%v, want only hers", len(keys), total, err)
	}
	notifs, total, err := s.ListUserNotifications(ctx, alice, false, page)
	if err != nil || total != 1 || len(notifs) != 1 || notifs[0].ID != aNotif.ID {
		t.Errorf("alice's inbox = %d/%d rows err=%v, want only hers", len(notifs), total, err)
	}

	// ── ownership-scoped write: someone else's row is a miss ───────
	if err := s.MarkRead(ctx, aNotif.ID, bob); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("bob marked alice's notification: err=%v, want sql.ErrNoRows", err)
	}
	var readAt *time.Time
	if err := s.DB.NewRaw("SELECT read_at FROM user_notifications WHERE id = ?", aNotif.ID).Scan(ctx, &readAt); err != nil {
		t.Fatalf("read_at check: %v", err)
	}
	if readAt != nil {
		t.Error("the victim's notification must still be unread")
	}

	// ── reviews: the lookup is by (product, caller email) ──────────
	mine, err := s.FindReviewByProductAndEmail(ctx, prod.ID, aliceEmail)
	if err != nil || mine.ID != aReview.ID {
		t.Errorf("alice's own review lookup = %v err=%v", mine, err)
	}
	if _, err := s.FindReviewByProductAndEmail(ctx, prod.ID, "nobody-"+suffix+"@example.com"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("a stranger read alice's review: err=%v, want sql.ErrNoRows", err)
	}
}
