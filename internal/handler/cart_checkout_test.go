package handler

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/payment"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// fakeCartStore implements payment.CartStore in memory so the
// checkout endpoint's request contract runs without a database.
type fakeCartStore struct {
	plans  map[string]*model.Plan
	prices map[string][]*store.PlanPrice
}

func newFakeCartStore() *fakeCartStore {
	return &fakeCartStore{
		plans:  map[string]*model.Plan{},
		prices: map[string][]*store.PlanPrice{},
	}
}

func (f *fakeCartStore) FindPlanByID(_ context.Context, id string) (*model.Plan, error) {
	p, ok := f.plans[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return p, nil
}

func (f *fakeCartStore) ListPlanPrices(_ context.Context, planID string) ([]*store.PlanPrice, error) {
	return f.prices[planID], nil
}

func (f *fakeCartStore) MaintenanceFeaturesEnabled(context.Context) (bool, error) { return true, nil }
func (f *fakeCartStore) FindCouponByCode(context.Context, string) (*model.Coupon, error) {
	return nil, sql.ErrNoRows
}
func (f *fakeCartStore) ListActiveTaxRatesForCountry(context.Context, string, string) ([]*model.TaxRate, error) {
	return nil, nil
}

func (f *fakeCartStore) seedPlan(id string, unit int64) {
	f.plans[id] = &model.Plan{
		ID: id, ProductID: "prod-" + id, Name: "Plan " + id, Slug: "plan-" + id,
		LicenseType: "perpetual", LicenseModel: "standard", Active: true,
	}
	f.prices[id] = []*store.PlanPrice{{Currency: "VND", AmountMinor: unit, IsDefault: true}}
}

// TestCartCheckoutEndpoint pins POST /api/v1/checkout/cart: 201 with
// {checkout_url, checkout_id, amount_minor, currency} where the money
// is the server-priced sum of the lines.
func TestCartCheckoutEndpoint(t *testing.T) {
	f := newFakeCartStore()
	f.seedPlan("a", 100000)
	f.seedPlan("b", 50000)

	var sessions int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessions++
		if !strings.HasSuffix(r.URL.Path, "/v1/checkout/sessions") {
			t.Errorf("unexpected stripe call %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_cart","object":"checkout.session","url":"https://stripe.example/pay"}`))
	}))
	t.Cleanup(srv.Close)
	gwSwapStripeBackend(t, srv.URL)

	h := &CartCheckoutHandler{carts: f, store: nil, baseURL: "https://app.test"}
	w, c := poAdminCtx(t, http.MethodPost, "/api/v1/checkout/cart",
		`{"items":[{"plan_id":"a","quantity":2},{"plan_id":"b","quantity":1}],"email":"buyer@example.com"}`,
		nil)
	h.Checkout(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	data := poData(t, w)
	if data["checkout_url"] != "https://stripe.example/pay" || data["checkout_id"] != "cs_cart" {
		t.Errorf("checkout = %v", data)
	}
	// 2×100000 + 1×50000 — server prices, client amounts nowhere.
	if data["amount_minor"] != float64(250000) || data["currency"] != "VND" {
		t.Errorf("money = %v, want 250000 VND", data)
	}
	if sessions != 1 {
		t.Errorf("stripe sessions = %d, want exactly 1 for the whole cart", sessions)
	}
}

// TestCartCheckoutRefusals pins the endpoint's request refusals: no
// items and no email.
func TestCartCheckoutRefusals(t *testing.T) {
	f := newFakeCartStore()
	f.seedPlan("a", 100000)
	h := &CartCheckoutHandler{carts: f, baseURL: "https://app.test"}

	w, c := poAdminCtx(t, http.MethodPost, "/api/v1/checkout/cart",
		`{"email":"buyer@example.com"}`, nil)
	h.Checkout(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("no items: status = %d, want 400", w.Code)
	}

	w, c = poAdminCtx(t, http.MethodPost, "/api/v1/checkout/cart",
		`{"items":[{"plan_id":"a","quantity":1}]}`, nil)
	h.Checkout(c)
	if w.Code != http.StatusBadRequest || !poHasErrorCode(t, w, "MISSING_CUSTOMER") {
		t.Errorf("no email: status = %d body=%s, want 400 MISSING_CUSTOMER", w.Code, w.Body.String())
	}
}

// TestGatewayPayCartShapeRefusals pins the drop-in endpoint's shape
// rules without a database: provider is required and exactly one of
// plan_id or items must be present.
func TestGatewayPayCartShapeRefusals(t *testing.T) {
	h := &CartCheckoutHandler{baseURL: "https://app.test"}

	w, c := poAdminCtx(t, http.MethodPost, "/api/v1/checkout/gateway-pay",
		`{"items":[{"plan_id":"a","quantity":1}]}`, nil)
	h.GatewayPay(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("no provider: status = %d, want 400", w.Code)
	}

	w, c = poAdminCtx(t, http.MethodPost, "/api/v1/checkout/gateway-pay",
		`{"provider":"payos"}`, nil)
	h.GatewayPay(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("no plan_id and no items: status = %d, want 400", w.Code)
	}
}

// TestGatewayPayMultiItemContract is the end-to-end multi-item
// gateway contract (DB-gated): ONE order carrying N lines, ONE
// gateway payment, and amount_minor = the sum of the server-priced
// lines × quantities.
func TestGatewayPayMultiItemContract(t *testing.T) {
	s := gwTestDB(t)
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000")

	// Stub the catalogue price lookup (VND, one-off).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"price_cart_%s","object":"price","unit_amount":220000,"currency":"vnd","type":"one_time"}`, suffix)
	}))
	t.Cleanup(srv.Close)
	gwSwapStripeBackend(t, srv.URL)

	prod := &model.Product{Name: "CART " + suffix, Slug: "cart-" + suffix, Type: "hybrid"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatal(err)
	}
	plan := &model.Plan{
		ProductID: prod.ID, Name: "Cart Plan " + suffix, Slug: "cart-plan-" + suffix,
		LicenseType: "perpetual", LicenseModel: "standard",
		StripePriceID: "price_cart_" + suffix, Active: true,
	}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	payment.RegisterProvider(&gwFakeProvider{name: "payos", enabled: true})

	h := &CartCheckoutHandler{carts: s, store: s, baseURL: "https://example.test"}
	r := gin.New()
	r.POST("/api/v1/checkout/gateway-pay", h.GatewayPay)

	body := fmt.Sprintf(`{"items":[{"plan_id":%q,"quantity":2}],"provider":"payos","email":"cart-%s@example.com"}`, plan.ID, suffix)
	w := gwDo(r, http.MethodPost, "/api/v1/checkout/gateway-pay", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("cart checkout: %d %s", w.Code, w.Body.String())
	}
	data := gwDecode(t, w)["data"].(map[string]any)
	if data["amount_minor"] != float64(440000) { // 2 × 220000 server-priced
		t.Errorf("amount = %v, want 440000 (sum of server-priced lines)", data["amount_minor"])
	}
	if data["currency"] != "VND" {
		t.Errorf("currency = %v, want VND", data["currency"])
	}
	for _, k := range []string{"pay_url", "provider_ref", "order_number"} {
		if v, _ := data[k].(string); v == "" {
			t.Fatalf("response missing %s: %v", k, data)
		}
	}
}
