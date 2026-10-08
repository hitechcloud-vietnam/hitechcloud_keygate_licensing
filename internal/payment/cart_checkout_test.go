package payment

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ─── fake CartStore ───────────────────────────────────────────────

type cartFakeStore struct {
	plans       map[string]*model.Plan
	prices      map[string][]*store.PlanPrice
	coupons     map[string]*model.Coupon
	tax         map[string][]*model.TaxRate
	maintenance bool
	maintErr    error
	planLookups int
}

func newCartFakeStore() *cartFakeStore {
	return &cartFakeStore{
		plans:   map[string]*model.Plan{},
		prices:  map[string][]*store.PlanPrice{},
		coupons: map[string]*model.Coupon{},
		tax:     map[string][]*model.TaxRate{},
	}
}

func (f *cartFakeStore) FindPlanByID(_ context.Context, id string) (*model.Plan, error) {
	f.planLookups++
	p, ok := f.plans[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return p, nil
}

func (f *cartFakeStore) ListPlanPrices(_ context.Context, planID string) ([]*store.PlanPrice, error) {
	return f.prices[planID], nil
}

func (f *cartFakeStore) MaintenanceFeaturesEnabled(_ context.Context) (bool, error) {
	return f.maintenance, f.maintErr
}

func (f *cartFakeStore) FindCouponByCode(_ context.Context, code string) (*model.Coupon, error) {
	c, ok := f.coupons[code]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return c, nil
}

func (f *cartFakeStore) ListActiveTaxRatesForCountry(_ context.Context, country, _ string) ([]*model.TaxRate, error) {
	return f.tax[country], nil
}

// cartSeedPlan registers a sellable plan with its §53 price rows.
func cartSeedPlan(f *cartFakeStore, id, slug, licenseType string, prices ...*store.PlanPrice) *model.Plan {
	p := &model.Plan{
		ID: id, ProductID: "prod-" + id, Name: "Plan " + id, Slug: slug,
		LicenseType: licenseType, LicenseModel: "standard", Active: true,
		BillingInterval: "month",
	}
	f.plans[id] = p
	f.prices[id] = prices
	return p
}

// ─── normalization ────────────────────────────────────────────────

// TestCartNormalizeItems pins the cart-shape rules: non-empty, at
// most cartMaxLines lines, every plan present, every quantity in
// 1..cartMaxQuantity, and no plan twice.
func TestCartNormalizeItems(t *testing.T) {
	tooMany := make([]CartCheckoutItem, cartMaxLines+1)
	for i := range tooMany {
		tooMany[i] = CartCheckoutItem{PlanID: fmt.Sprintf("p%d", i), Quantity: 1}
	}

	for _, tc := range []struct {
		name  string
		items []CartCheckoutItem
		ok    bool
	}{
		{"one line", []CartCheckoutItem{{PlanID: "p1", Quantity: 1}}, true},
		{"max quantity", []CartCheckoutItem{{PlanID: "p1", Quantity: cartMaxQuantity}}, true},
		{"empty", nil, false},
		{"blank plan id", []CartCheckoutItem{{PlanID: "  ", Quantity: 1}}, false},
		{"zero quantity", []CartCheckoutItem{{PlanID: "p1", Quantity: 0}}, false},
		{"quantity over the cap", []CartCheckoutItem{{PlanID: "p1", Quantity: cartMaxQuantity + 1}}, false},
		{"duplicate plan", []CartCheckoutItem{{PlanID: "p1", Quantity: 1}, {PlanID: "p1", Quantity: 2}}, false},
		{"too many lines", tooMany, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cartNormalizeItems(tc.items)
			if tc.ok {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				if len(got) != len(tc.items) {
					t.Errorf("normalized to %d lines, want %d", len(got), len(tc.items))
				}
				return
			}
			if err == nil {
				t.Fatalf("want a refusal, got %v", got)
			}
			appErrCode(t, err, "BAD_REQUEST", 400)
		})
	}
}

// ─── §53 price resolution ─────────────────────────────────────────

// TestCartUnitPriceResolveMatrix pins the per-line price source:
// the plan_prices row for the requested currency, else the default
// row, else the existing Stripe-price path, else PRICING_UNAVAILABLE.
func TestCartUnitPriceResolveMatrix(t *testing.T) {
	ctx := context.Background()
	f := newCartFakeStore()

	cartSeedPlan(f, "p-rows", "p-rows", "perpetual",
		&store.PlanPrice{Currency: "USD", AmountMinor: 1000, IsDefault: true},
		&store.PlanPrice{Currency: "VND", AmountMinor: 25000000},
	)
	cartSeedPlan(f, "p-stripe", "p-stripe", "perpetual").StripePriceID = "price_live"
	cartSeedPlan(f, "p-none", "p-none", "perpetual")

	// Exact row wins.
	unit, cur, err := cartUnitPrice(ctx, f, f.plans["p-rows"], "VND")
	if err != nil || unit != 25000000 || cur != "VND" {
		t.Errorf("exact row = (%d, %q, %v), want (25000000, VND, nil)", unit, cur, err)
	}

	// No currency asked: the default row.
	unit, cur, err = cartUnitPrice(ctx, f, f.plans["p-rows"], "")
	if err != nil || unit != 1000 || cur != "USD" {
		t.Errorf("default row = (%d, %q, %v), want (1000, USD, nil)", unit, cur, err)
	}

	// Requested currency without a row falls through to the Stripe
	// price (the pinned "else existing Stripe-price path").
	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"price_live","object":"price","unit_amount":220000,"currency":"vnd","type":"one_time"}`))
	})
	unit, cur, err = cartUnitPrice(ctx, f, f.plans["p-stripe"], "JPY")
	if err != nil || unit != 220000 || cur != "VND" {
		t.Errorf("stripe fallback = (%d, %q, %v), want (220000, VND, nil)", unit, cur, err)
	}

	// No rows and no stripe price: PRICING_UNAVAILABLE (503).
	_, _, err = cartUnitPrice(ctx, f, f.plans["p-none"], "VND")
	appErrCode(t, err, "PRICING_UNAVAILABLE", 503)
}

// ─── sale pricing ─────────────────────────────────────────────────

// TestCartPriceSaleSumsServerPrices pins §23's core: the cart total
// is the sum of the SERVER-priced lines × quantities. CartCheckoutItem
// has no amount field by construction — a client cannot express a
// price, so nothing here can read one.
func TestCartPriceSaleSumsServerPrices(t *testing.T) {
	ctx := context.Background()
	f := newCartFakeStore()
	cartSeedPlan(f, "a", "plan-a", "perpetual",
		&store.PlanPrice{Currency: "VND", AmountMinor: 100000, IsDefault: true})
	cartSeedPlan(f, "b", "plan-b", "perpetual",
		&store.PlanPrice{Currency: "VND", AmountMinor: 50000, IsDefault: true})

	lines, res, cur, err := cartPriceSale(ctx, f, []CartCheckoutItem{
		{PlanID: "a", Quantity: 2},
		{PlanID: "b", Quantity: 3},
	}, "", nil, nil, false)
	if err != nil {
		t.Fatalf("price cart: %v", err)
	}
	if cur != "VND" {
		t.Errorf("currency = %q, want VND", cur)
	}
	// 2×100000 + 3×50000 = 350000.
	if res.TotalMinor != 350000 {
		t.Errorf("total = %d, want 350000 (server prices × quantities)", res.TotalMinor)
	}
	if len(lines) != 2 || lines[0].UnitAmountMinor != 100000 || lines[1].UnitAmountMinor != 50000 {
		t.Errorf("lines = %+v, want the server unit prices", lines)
	}

	// One currency per cart: lines resolving differently are refused.
	cartSeedPlan(f, "usd", "plan-usd", "perpetual",
		&store.PlanPrice{Currency: "USD", AmountMinor: 10, IsDefault: true})
	_, _, _, err = cartPriceSale(ctx, f, []CartCheckoutItem{
		{PlanID: "usd", Quantity: 1}, {PlanID: "a", Quantity: 1},
	}, "", nil, nil, false)
	appErrCode(t, err, "CURRENCY_NOT_SUPPORTED", 400)

	// Unknown plan is PLAN_NOT_FOUND (404).
	_, _, _, err = cartPriceSale(ctx, f, []CartCheckoutItem{{PlanID: "ghost", Quantity: 1}}, "", nil, nil, false)
	appErrCode(t, err, "PLAN_NOT_FOUND", 404)

	// An inactive plan is refused the same way.
	f.plans["a"].Active = false
	_, _, _, err = cartPriceSale(ctx, f, []CartCheckoutItem{{PlanID: "a", Quantity: 1}}, "", nil, nil, false)
	appErrCode(t, err, "PLAN_NOT_FOUND", 404)
}

// ─── Stripe session ───────────────────────────────────────────────

// TestCartCheckoutSessionOneStripeSession pins that a whole cart
// becomes ONE Stripe Checkout Session whose line amounts are the
// server prices — the client's view of money is nowhere in the
// request or the wire format.
func TestCartCheckoutSessionOneStripeSession(t *testing.T) {
	ctx := context.Background()
	f := newCartFakeStore()
	cartSeedPlan(f, "a", "plan-a", "perpetual",
		&store.PlanPrice{Currency: "VND", AmountMinor: 100000, IsDefault: true})
	cartSeedPlan(f, "b", "plan-b", "perpetual",
		&store.PlanPrice{Currency: "VND", AmountMinor: 50000, IsDefault: true})

	var captured map[string]string
	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/checkout/sessions") {
			t.Errorf("unexpected stripe call %s %s", r.Method, r.URL.Path)
		}
		_ = r.ParseForm()
		captured = map[string]string{}
		for k, vs := range r.PostForm {
			if len(vs) > 0 {
				captured[k] = vs[0]
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"cs_cart","object":"checkout.session","url":"https://stripe.example/pay"}`))
	})

	res, err := CreateCartCheckoutSession(ctx, f, &CartCheckoutRequest{
		Items:      []CartCheckoutItem{{PlanID: "a", Quantity: 2}, {PlanID: "b", Quantity: 1}},
		Email:      "buyer@example.com",
		SuccessURL: "https://app.test/ok", CancelURL: "https://app.test/cancel",
	})
	if err != nil {
		t.Fatalf("cart session: %v", err)
	}
	if res.CheckoutID != "cs_cart" || res.URL != "https://stripe.example/pay" {
		t.Errorf("session = %+v, want cs_cart / the stripe URL", res)
	}
	if res.TotalMinor != 250000 { // 2×100000 + 1×50000
		t.Errorf("total = %d, want 250000", res.TotalMinor)
	}
	if res.Currency != "VND" || res.LineCount != 2 {
		t.Errorf("session = %+v, want VND with 2 lines", res)
	}

	// The wire format carries the SERVER unit amounts and quantities.
	if captured["mode"] != "payment" {
		t.Errorf("mode = %q, want payment", captured["mode"])
	}
	if captured["customer_email"] != "buyer@example.com" {
		t.Errorf("customer_email = %q", captured["customer_email"])
	}
	for k, want := range map[string]string{
		"line_items[0][quantity]":                "2",
		"line_items[0][price_data][unit_amount]": "100000",
		"line_items[0][price_data][currency]":    "vnd",
		"line_items[1][quantity]":                "1",
		"line_items[1][price_data][unit_amount]": "50000",
	} {
		if captured[k] != want {
			t.Errorf("stripe form %s = %q, want %q (full form: %v)", k, captured[k], want, captured)
		}
	}
}

// TestCartCheckoutSessionRefusals pins the request refusals: no
// email is MISSING_CUSTOMER (400) and a cart mixing one-time and
// subscription plans is refused (a Stripe session is one mode).
func TestCartCheckoutSessionRefusals(t *testing.T) {
	ctx := context.Background()
	f := newCartFakeStore()
	cartSeedPlan(f, "once", "plan-once", "perpetual",
		&store.PlanPrice{Currency: "VND", AmountMinor: 100000, IsDefault: true})
	cartSeedPlan(f, "sub", "plan-sub", "subscription",
		&store.PlanPrice{Currency: "VND", AmountMinor: 100000, IsDefault: true})

	_, err := CreateCartCheckoutSession(ctx, f, &CartCheckoutRequest{
		Items: []CartCheckoutItem{{PlanID: "once", Quantity: 1}},
	})
	appErrCode(t, err, "MISSING_CUSTOMER", 400)

	// Mixed modes only fail AFTER pricing (the mode check runs on
	// the priced sale), so this one never reaches Stripe.
	_, err = CreateCartCheckoutSession(ctx, f, &CartCheckoutRequest{
		Items: []CartCheckoutItem{{PlanID: "once", Quantity: 1}, {PlanID: "sub", Quantity: 1}},
		Email: "buyer@example.com",
	})
	appErrCode(t, err, "BAD_REQUEST", 400)
}

// TestCartCheckoutSessionMissingCoupon pins the coupon lookup's
// error contract: an unknown code is COUPON_NOT_FOUND (404).
func TestCartCheckoutSessionMissingCoupon(t *testing.T) {
	ctx := context.Background()
	f := newCartFakeStore()
	cartSeedPlan(f, "once", "plan-once", "perpetual",
		&store.PlanPrice{Currency: "VND", AmountMinor: 100000, IsDefault: true})

	_, err := CreateCartCheckoutSession(ctx, f, &CartCheckoutRequest{
		Items:      []CartCheckoutItem{{PlanID: "once", Quantity: 1}},
		Email:      "buyer@example.com",
		CouponCode: "NOPE",
	})
	appErrCode(t, err, "COUPON_NOT_FOUND", 404)
}

// TestCartCheckoutItemHasNoAmount pins the structural guarantee of
// §23: a cart line carries a plan and a quantity, never money — the
// server's price is the only price there is. The JSON wire shape
// proves it: the marshalled item has exactly plan_id and quantity.
func TestCartCheckoutItemHasNoAmount(t *testing.T) {
	raw, err := json.Marshal(CartCheckoutItem{PlanID: "p", Quantity: 2})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 {
		t.Errorf("item wire shape = %s, want exactly plan_id + quantity", raw)
	}
	for _, k := range []string{"plan_id", "quantity"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("item wire shape %s missing %q", raw, k)
		}
	}
	if _, ok := keys["amount"]; ok {
		t.Errorf("a cart line must never carry money: %s", raw)
	}
}
