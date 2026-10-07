package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/coupon"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// The quote runs against the real money/coupon/tax engines and fakes
// for the three catalogue seams (plans, tax rates, Stripe prices): no
// database, no network, nothing that could make these tests depend on
// the world. The integration test at the bottom trades the fakes for a
// real store when TEST_DATABASE_URL is set.

type quoteFakeCatalog struct {
	plans     map[string]*model.Plan
	checkouts map[string]string // checkout id → plan id
}

func (f quoteFakeCatalog) FindPlanByID(_ context.Context, id string) (*model.Plan, error) {
	p, ok := f.plans[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return p, nil
}

// FindPlanByCheckoutID mirrors the store's checkout-id lookup — the
// one payment.CheckoutByPlan pays through.
func (f quoteFakeCatalog) FindPlanByCheckoutID(_ context.Context, checkoutID string) (*model.Plan, error) {
	id, ok := f.checkouts[checkoutID]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return f.FindPlanByID(nil, id)
}

// quoteFakeTaxes mirrors the store's matching rules (country equal,
// region equal or empty, active only) so a fixture that should not
// apply to a country really does not.
type quoteFakeTaxes struct {
	rates                 []*model.TaxRate
	calls                 int
	gotCountry, gotRegion string
}

func (f *quoteFakeTaxes) ListActiveTaxRatesForCountry(_ context.Context, country, region string) ([]*model.TaxRate, error) {
	f.calls++
	f.gotCountry, f.gotRegion = country, region
	var out []*model.TaxRate
	for _, r := range f.rates {
		if !r.Active {
			continue
		}
		if r.Country != "" && r.Country != country {
			continue
		}
		if r.Region != "" && r.Region != region {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

type quoteFakePrices struct {
	amounts    map[string]int64
	currencies map[string]string
	calls      []string
}

func (f *quoteFakePrices) UnitAmount(_ context.Context, priceID string) (int64, string, error) {
	f.calls = append(f.calls, priceID)
	amount, ok := f.amounts[priceID]
	if !ok {
		return 0, "", fmt.Errorf("price %q not in the fake catalogue", priceID)
	}
	cur := f.currencies[priceID]
	if cur == "" {
		cur = "USD"
	}
	return amount, cur, nil
}

type quoteFakeLookup struct{ coupons map[string]coupon.Coupon }

func (f *quoteFakeLookup) FindCouponByCode(_ context.Context, code string) (*coupon.Coupon, error) {
	c, ok := f.coupons[coupon.NormalizeCode(code)]
	if !ok {
		return nil, fmt.Errorf("coupon %q: %w", code, coupon.ErrCouponNotFound)
	}
	return &c, nil
}

type quoteFixture struct {
	handler *CheckoutQuoteHandler
	taxes   *quoteFakeTaxes
	prices  *quoteFakePrices
}

// quoteFixture builds the standard catalogue: two priced plans in USD,
// one in EUR (for the mixed-currency refusal), one inactive, one with
// no price at all, and a SAVE10 coupon the tests may point at.
func newQuoteFixture() quoteFixture {
	catalog := &quoteFakeCatalog{plans: map[string]*model.Plan{
		"plan_basic": {ID: "plan_basic", ProductID: "prod_1", Name: "Basic", Slug: "basic",
			StripePriceID: "price_basic", Active: true},
		"plan_pro": {ID: "plan_pro", ProductID: "prod_1", Name: "Pro", Slug: "pro",
			StripePriceID: "price_pro", Active: true},
		"plan_eur": {ID: "plan_eur", ProductID: "prod_1", Name: "Euro", Slug: "euro",
			StripePriceID: "price_eur", Active: true},
		"plan_gone": {ID: "plan_gone", ProductID: "prod_1", Name: "Retired", Slug: "retired",
			StripePriceID: "price_gone", Active: false},
		"plan_free": {ID: "plan_free", ProductID: "prod_1", Name: "Unpriced", Slug: "unpriced",
			Active: true},
	},
		checkouts: map[string]string{
			"chk_basic": "plan_basic",
			"chk_pro":   "plan_pro",
			"chk_gone":  "plan_gone",
			"chk_free":  "plan_free",
		},
	}
	prices := &quoteFakePrices{
		amounts:    map[string]int64{"price_basic": 1999, "price_pro": 2000, "price_eur": 1500, "price_gone": 999},
		currencies: map[string]string{"price_eur": "EUR"},
	}
	taxes := &quoteFakeTaxes{}
	lookup := &quoteFakeLookup{coupons: map[string]coupon.Coupon{
		"SAVE10": {Code: "SAVE10", Type: coupon.TypePercentOff, Value: 1000, Active: true},
	}}
	return quoteFixture{
		handler: &CheckoutQuoteHandler{
			svc:    service.NewOrderService(nil, lookup),
			plans:  catalog,
			taxes:  taxes,
			prices: prices,
		},
		taxes:  taxes,
		prices: prices,
	}
}

func quoteServe(t *testing.T, h *CheckoutQuoteHandler, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/checkout/quote", h.Quote)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/checkout/quote", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

type quoteLine struct {
	PlanID            string `json:"plan_id"`
	Quantity          int64  `json:"quantity"`
	UnitAmountMinor   int64  `json:"unit_amount_minor"`
	LineSubtotalMinor int64  `json:"line_subtotal_minor"`
	LineDiscountMinor int64  `json:"line_discount_minor"`
	LineTaxMinor      int64  `json:"line_tax_minor"`
	LineTotalMinor    int64  `json:"line_total_minor"`
}

type quoteBody struct {
	Success bool `json:"success"`
	Data    struct {
		Currency      string      `json:"currency"`
		TaxInclusive  bool        `json:"tax_inclusive"`
		SubtotalMinor int64       `json:"subtotal_minor"`
		DiscountMinor int64       `json:"discount_minor"`
		TaxMinor      int64       `json:"tax_minor"`
		TotalMinor    int64       `json:"total_minor"`
		Lines         []quoteLine `json:"lines"`
		AppliedCoupon *struct {
			Code     string `json:"code"`
			Type     string `json:"type"`
			Value    int64  `json:"value"`
			Currency string `json:"currency"`
		} `json:"applied_coupon"`
		TaxRates []struct {
			Jurisdiction string `json:"jurisdiction"`
			BasisPoints  int64  `json:"basis_points"`
		} `json:"tax_rates"`
	} `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func quoteDecode(t *testing.T, w *httptest.ResponseRecorder) quoteBody {
	t.Helper()
	var b quoteBody
	if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode response %s: %v", w.Body.String(), err)
	}
	return b
}

// A request without a coupon must always work, coupon table or not:
// the fixture's SAVE10 must sit there unapplied.
func TestCheckoutQuoteNoCoupon(t *testing.T) {
	fx := newQuoteFixture()
	w := quoteServe(t, fx.handler, `{"items":[{"plan_id":"plan_basic","quantity":2}]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	b := quoteDecode(t, w)
	if !b.Success {
		t.Fatalf("success = false: %s", w.Body.String())
	}
	if b.Data.Currency != "USD" {
		t.Errorf("currency = %q, want USD", b.Data.Currency)
	}
	if b.Data.SubtotalMinor != 3998 || b.Data.DiscountMinor != 0 ||
		b.Data.TaxMinor != 0 || b.Data.TotalMinor != 3998 {
		t.Errorf("totals = %d/%d/%d/%d, want 3998/0/0/3998",
			b.Data.SubtotalMinor, b.Data.DiscountMinor, b.Data.TaxMinor, b.Data.TotalMinor)
	}
	if b.Data.AppliedCoupon != nil {
		t.Errorf("applied_coupon = %+v, want none", b.Data.AppliedCoupon)
	}
	if len(b.Data.TaxRates) != 0 {
		t.Errorf("tax_rates = %+v, want empty", b.Data.TaxRates)
	}
	if len(b.Data.Lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(b.Data.Lines))
	}
	l := b.Data.Lines[0]
	if l.PlanID != "plan_basic" || l.Quantity != 2 || l.UnitAmountMinor != 1999 {
		t.Errorf("line = %+v, want plan_basic ×2 at 1999", l)
	}
	if l.LineSubtotalMinor != 3998 || l.LineDiscountMinor != 0 ||
		l.LineTaxMinor != 0 || l.LineTotalMinor != 3998 {
		t.Errorf("line money = %d/%d/%d/%d, want 3998/0/0/3998",
			l.LineSubtotalMinor, l.LineDiscountMinor, l.LineTaxMinor, l.LineTotalMinor)
	}
}

// A percent coupon discounts the catalogue price — 10% of 2 × 2000 —
// and the response says which coupon produced the discount. The code
// is matched case-insensitively, like every other lookup in the system.
func TestCheckoutQuotePercentCoupon(t *testing.T) {
	fx := newQuoteFixture()
	fx.prices.amounts["price_basic"] = 2000
	w := quoteServe(t, fx.handler,
		`{"items":[{"plan_id":"plan_basic","quantity":2}],"coupon_code":"save10"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	b := quoteDecode(t, w)
	if b.Data.SubtotalMinor != 4000 || b.Data.DiscountMinor != 400 ||
		b.Data.TaxMinor != 0 || b.Data.TotalMinor != 3600 {
		t.Errorf("totals = %d/%d/%d/%d, want 4000/400/0/3600",
			b.Data.SubtotalMinor, b.Data.DiscountMinor, b.Data.TaxMinor, b.Data.TotalMinor)
	}
	if len(b.Data.Lines) != 1 || b.Data.Lines[0].LineDiscountMinor != 400 {
		t.Errorf("lines = %+v, want one line discounted 400", b.Data.Lines)
	}
	if b.Data.AppliedCoupon == nil {
		t.Fatalf("applied_coupon = nil, want the coupon that discounted")
	}
	if b.Data.AppliedCoupon.Code != "SAVE10" ||
		b.Data.AppliedCoupon.Type != "percent_off" ||
		b.Data.AppliedCoupon.Value != 1000 {
		t.Errorf("applied_coupon = %+v, want SAVE10 percent_off 1000", *b.Data.AppliedCoupon)
	}
}

// Every coupon the operator can misconfigure is a 400 with the
// engine's own message — precise enough for the checkout UI to put
// next to the coupon field. The unknown code is the odd one: the
// calculation answers it 404, and the checkout contract folds it into
// COUPON_INVALID at 400 (see checkoutQuoteErr).
func TestCheckoutQuoteCouponRefusals(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	for _, tc := range []struct {
		name    string
		coupon  coupon.Coupon
		body    string
		wantMsg string
	}{
		{
			name:    "unknown code",
			body:    `{"items":[{"plan_id":"plan_basic","quantity":1}],"coupon_code":"NOPE"}`,
			wantMsg: "coupon not found",
		},
		{
			name:    "expired code",
			coupon:  coupon.Coupon{Code: "OLD", Type: coupon.TypePercentOff, Value: 1000, Active: true, EndsAt: past},
			body:    `{"items":[{"plan_id":"plan_basic","quantity":1}],"coupon_code":"OLD"}`,
			wantMsg: "expired",
		},
		{
			name:    "inactive code",
			coupon:  coupon.Coupon{Code: "OFF", Type: coupon.TypePercentOff, Value: 1000},
			body:    `{"items":[{"plan_id":"plan_basic","quantity":1}],"coupon_code":"OFF"}`,
			wantMsg: "inactive",
		},
		{
			name:    "minimum not met",
			coupon:  coupon.Coupon{Code: "BIG", Type: coupon.TypePercentOff, Value: 1000, Active: true, MinimumOrderAmount: 100_000},
			body:    `{"items":[{"plan_id":"plan_basic","quantity":1}],"coupon_code":"BIG"}`,
			wantMsg: "minimum",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newQuoteFixture()
			if tc.coupon.Code != "" {
				fx.handler.svc = service.NewOrderService(nil, &quoteFakeLookup{
					coupons: map[string]coupon.Coupon{coupon.NormalizeCode(tc.coupon.Code): tc.coupon},
				})
			}
			w := quoteServe(t, fx.handler, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
			b := quoteDecode(t, w)
			if b.Success || b.Error == nil {
				t.Fatalf("response = %s, want an error envelope", w.Body.String())
			}
			if b.Error.Code != "COUPON_INVALID" {
				t.Errorf("error.code = %q, want COUPON_INVALID", b.Error.Code)
			}
			if !strings.Contains(b.Error.Message, tc.wantMsg) {
				t.Errorf("error.message = %q, want it to mention %q", b.Error.Message, tc.wantMsg)
			}
		})
	}
}

// Tax comes from the operator's rate table matched against the
// customer's country — never from the request. Exclusive tax is added
// on top, inclusive tax lives inside the total, and the response names
// the jurisdiction and rate it used.
func TestCheckoutQuoteTaxRates(t *testing.T) {
	newFX := func() quoteFixture {
		fx := newQuoteFixture()
		fx.prices.amounts["price_basic"] = 1000
		fx.taxes.rates = []*model.TaxRate{
			{Jurisdiction: "VAT-VN", BasisPoints: 1000, Country: "VN", Active: true},
		}
		return fx
	}

	t.Run("exclusive", func(t *testing.T) {
		fx := newFX()
		w := quoteServe(t, fx.handler,
			`{"items":[{"plan_id":"plan_basic","quantity":1}],"country":"vn"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		b := quoteDecode(t, w)
		if b.Data.SubtotalMinor != 1000 || b.Data.TaxMinor != 100 || b.Data.TotalMinor != 1100 {
			t.Errorf("totals = %d/%d/%d, want 1000/100/1100",
				b.Data.SubtotalMinor, b.Data.TaxMinor, b.Data.TotalMinor)
		}
		if fx.taxes.gotCountry != "VN" || fx.taxes.gotRegion != "" {
			t.Errorf("lookup got (%q, %q), want (VN, %q)", fx.taxes.gotCountry, fx.taxes.gotRegion, "")
		}
		if len(b.Data.TaxRates) != 1 ||
			b.Data.TaxRates[0].Jurisdiction != "VAT-VN" || b.Data.TaxRates[0].BasisPoints != 1000 {
			t.Errorf("tax_rates = %+v, want VAT-VN at 1000 bps", b.Data.TaxRates)
		}
	})

	t.Run("inclusive", func(t *testing.T) {
		fx := newFX()
		w := quoteServe(t, fx.handler,
			`{"items":[{"plan_id":"plan_basic","quantity":1}],"country":"vn","tax_inclusive":true}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		b := quoteDecode(t, w)
		// 1000 gross at 10% contains round(1000×1000/11000) = 91 of
		// tax; the customer still pays the gross 1000.
		if b.Data.SubtotalMinor != 1000 || b.Data.TaxMinor != 91 || b.Data.TotalMinor != 1000 {
			t.Errorf("totals = %d/%d/%d, want 1000/91/1000",
				b.Data.SubtotalMinor, b.Data.TaxMinor, b.Data.TotalMinor)
		}
	})

	t.Run("country without rates is untaxed", func(t *testing.T) {
		fx := newFX()
		w := quoteServe(t, fx.handler,
			`{"items":[{"plan_id":"plan_basic","quantity":1}],"country":"fr"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		b := quoteDecode(t, w)
		if b.Data.TaxMinor != 0 || b.Data.TotalMinor != 1000 || len(b.Data.TaxRates) != 0 {
			t.Errorf("tax = %d, total = %d, rates = %+v, want untaxed",
				b.Data.TaxMinor, b.Data.TotalMinor, b.Data.TaxRates)
		}
	})

	t.Run("no country means no lookup and no tax", func(t *testing.T) {
		fx := newFX()
		w := quoteServe(t, fx.handler,
			`{"items":[{"plan_id":"plan_basic","quantity":1}]}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		b := quoteDecode(t, w)
		if b.Data.TaxMinor != 0 || b.Data.TotalMinor != 1000 {
			t.Errorf("tax = %d, total = %d, want untaxed 1000", b.Data.TaxMinor, b.Data.TotalMinor)
		}
		if fx.taxes.calls != 0 {
			t.Errorf("tax lookup ran %d times without a country, want 0", fx.taxes.calls)
		}
	})
}

// The whole point of the endpoint: amounts come from the catalogue,
// not from the request. The client may send unit prices, totals and a
// currency — every one of them is ignored, and the response is priced
// entirely from the plan's Stripe Price.
func TestCheckoutQuoteIgnoresClientAmounts(t *testing.T) {
	fx := newQuoteFixture()
	w := quoteServe(t, fx.handler, `{
		"items": [{
			"plan_id": "plan_basic",
			"quantity": 1,
			"unit_amount_minor": 1,
			"price_minor": 1,
			"amount": 1,
			"total_minor": 1
		}],
		"currency": "JPY",
		"subtotal_minor": 1,
		"total_minor": 1
	}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	b := quoteDecode(t, w)
	if b.Data.Currency != "USD" {
		t.Errorf("currency = %q, want the catalogue's USD (JPY was spoofed)", b.Data.Currency)
	}
	if b.Data.SubtotalMinor != 1999 || b.Data.TotalMinor != 1999 {
		t.Errorf("totals = %d/%d, want 1999/1999 from the catalogue", b.Data.SubtotalMinor, b.Data.TotalMinor)
	}
	if len(b.Data.Lines) != 1 || b.Data.Lines[0].UnitAmountMinor != 1999 {
		t.Errorf("lines = %+v, want one line at 1999", b.Data.Lines)
	}
}

// Refusals before any pricing: a body that names no purchasable plan
// gets a 400 that says which item and why.
func TestCheckoutQuoteRequestRefusals(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantMsg string
	}{
		{"empty items", `{"items":[]}`, "items must not be empty"},
		{"invalid json", `{"items":`, "invalid request"},
		{"missing plan_id", `{"items":[{"quantity":1}]}`, "plan_id is required"},
		{"zero quantity", `{"items":[{"plan_id":"plan_basic","quantity":0}]}`, "quantity must be at least 1"},
		{"unknown plan", `{"items":[{"plan_id":"plan_nope","quantity":1}]}`, `unknown plan_id "plan_nope"`},
		{"inactive plan", `{"items":[{"plan_id":"plan_gone","quantity":1}]}`, "is not available"},
		{"unpriced plan", `{"items":[{"plan_id":"plan_free","quantity":1}]}`, "has no price"},
		{
			"mixed currencies",
			`{"items":[{"plan_id":"plan_basic","quantity":1},{"plan_id":"plan_eur","quantity":1}]}`,
			"same currency",
		},
		{
			"too many items",
			`{"items":[` + strings.Repeat(`{"plan_id":"plan_basic","quantity":1},`, checkoutQuoteMaxItems) +
				`{"plan_id":"plan_basic","quantity":1}]}`,
			"must not exceed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newQuoteFixture()
			w := quoteServe(t, fx.handler, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
			}
			b := quoteDecode(t, w)
			if b.Error == nil {
				t.Fatalf("response = %s, want an error envelope", w.Body.String())
			}
			if b.Error.Code != "BAD_REQUEST" {
				t.Errorf("error.code = %q, want BAD_REQUEST", b.Error.Code)
			}
			if !strings.Contains(b.Error.Message, tc.wantMsg) {
				t.Errorf("error.message = %q, want it to mention %q", b.Error.Message, tc.wantMsg)
			}
		})
	}
}

// An item may name its plan by the checkout_id of the payment link
// instead of a plan_id — the same lookup CheckoutByPlan pays through —
// and prices it identically. A checkout_id that names nothing, or one
// sent alongside a plan_id, is a 400 that says which item and why.
func TestCheckoutQuoteCheckoutIDItems(t *testing.T) {
	t.Run("resolves the plan behind the payment link", func(t *testing.T) {
		fx := newQuoteFixture()
		fx.prices.amounts["price_basic"] = 2000
		w := quoteServe(t, fx.handler,
			`{"items":[{"checkout_id":"chk_basic","quantity":2}],"coupon_code":"SAVE10"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		b := quoteDecode(t, w)
		if b.Data.SubtotalMinor != 4000 || b.Data.DiscountMinor != 400 || b.Data.TotalMinor != 3600 {
			t.Errorf("totals = %d/%d/%d, want 4000/400/3600",
				b.Data.SubtotalMinor, b.Data.DiscountMinor, b.Data.TotalMinor)
		}
		if len(b.Data.Lines) != 1 || b.Data.Lines[0].PlanID != "plan_basic" || b.Data.Lines[0].Quantity != 2 {
			t.Errorf("lines = %+v, want plan_basic ×2", b.Data.Lines)
		}
	})

	t.Run("takes coupon and tax like a plan_id item", func(t *testing.T) {
		fx := newQuoteFixture()
		fx.prices.amounts["price_basic"] = 2000
		fx.taxes.rates = []*model.TaxRate{
			{Jurisdiction: "VAT-VN", BasisPoints: 1000, Country: "VN", Active: true},
		}
		w := quoteServe(t, fx.handler,
			`{"items":[{"checkout_id":"chk_basic","quantity":1}],"coupon_code":"SAVE10","country":"vn"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
		}
		b := quoteDecode(t, w)
		// 2000 − 200 + 10% VAT on the 1800 left.
		if b.Data.SubtotalMinor != 2000 || b.Data.DiscountMinor != 200 ||
			b.Data.TaxMinor != 180 || b.Data.TotalMinor != 1980 {
			t.Errorf("totals = %d/%d/%d/%d, want 2000/200/180/1980",
				b.Data.SubtotalMinor, b.Data.DiscountMinor, b.Data.TaxMinor, b.Data.TotalMinor)
		}
	})

	t.Run("unknown checkout_id", func(t *testing.T) {
		fx := newQuoteFixture()
		w := quoteServe(t, fx.handler, `{"items":[{"checkout_id":"chk_nope","quantity":1}]}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
		}
		b := quoteDecode(t, w)
		if b.Error == nil || b.Error.Code != "BAD_REQUEST" ||
			!strings.Contains(b.Error.Message, `unknown checkout_id "chk_nope"`) {
			t.Errorf("error = %+v, want BAD_REQUEST naming the checkout_id", b.Error)
		}
	})

	t.Run("plan_id and checkout_id together", func(t *testing.T) {
		fx := newQuoteFixture()
		w := quoteServe(t, fx.handler,
			`{"items":[{"plan_id":"plan_basic","checkout_id":"chk_basic","quantity":1}]}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
		}
		b := quoteDecode(t, w)
		if b.Error == nil || !strings.Contains(b.Error.Message, "cannot both be set") {
			t.Errorf("error = %+v, want the both-set refusal", b.Error)
		}
	})

	t.Run("inactive plan behind the link", func(t *testing.T) {
		fx := newQuoteFixture()
		w := quoteServe(t, fx.handler, `{"items":[{"checkout_id":"chk_gone","quantity":1}]}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
		}
		b := quoteDecode(t, w)
		if b.Error == nil || !strings.Contains(b.Error.Message, "is not available") {
			t.Errorf("error = %+v, want the not-available refusal", b.Error)
		}
	})

	t.Run("unpriced plan behind the link", func(t *testing.T) {
		fx := newQuoteFixture()
		w := quoteServe(t, fx.handler, `{"items":[{"checkout_id":"chk_free","quantity":1}]}`)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
		}
		b := quoteDecode(t, w)
		if b.Error == nil || !strings.Contains(b.Error.Message, "has no price") {
			t.Errorf("error = %+v, want the no-price refusal", b.Error)
		}
	})
}

// ─── store-backed integration ───

// quoteStoreLookup is the adapter the Lead wires at bootstrap, written
// here against the real store: a missing coupon row must surface as the
// coupon engine's ErrCouponNotFound, not as a raw sql error — that
// mapping is what gives the checkout UI "coupon not found" instead of
// a database message.
type quoteStoreLookup struct{ s *store.Store }

func (l quoteStoreLookup) FindCouponByCode(ctx context.Context, code string) (*coupon.Coupon, error) {
	mc, err := l.s.FindCouponByCode(ctx, code)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("coupon %q: %w", code, coupon.ErrCouponNotFound)
		}
		return nil, err
	}
	eng := mc.ToEngine()
	return &eng, nil
}

// TestCheckoutQuoteStoreIntegration prices a quote through the real
// store: the plan row, the coupon row and the tax rate row are the
// operator's, and only the Stripe price is faked (a real Stripe is not
// a test dependency). Everything else runs the production path.
func TestCheckoutQuoteStoreIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := store.New(dsn)
	if err != nil {
		t.Skipf("skipping integration test: %v", err)
	}
	defer s.Close()
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	ctx := context.Background()
	suffix := time.Now().Format("150405.000")

	prod := &model.Product{Name: "Quote", Slug: "quote-" + suffix, Type: "hybrid"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("create product: %v", err)
	}
	priceID := "price_quote_" + suffix
	plan := &model.Plan{
		ProductID: prod.ID, Name: "Quote Plan", Slug: "quote-plan-" + suffix,
		LicenseType: "perpetual", LicenseModel: "standard", StripePriceID: priceID,
	}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	code := "QT" + suffix
	if err := s.CreateCoupon(ctx, &model.Coupon{
		Code: code, Type: model.CouponTypePercentOff, ValueBPS: 500,
		Stackable: true, Active: true,
	}); err != nil {
		t.Fatalf("create coupon: %v", err)
	}
	country := "QT" + suffix
	if err := s.CreateTaxRate(ctx, &model.TaxRate{
		Jurisdiction: "VAT-" + country, BasisPoints: 1000, Country: country, Active: true,
	}); err != nil {
		t.Fatalf("create tax rate: %v", err)
	}

	h := &CheckoutQuoteHandler{
		svc:    service.NewOrderService(s, quoteStoreLookup{s: s}),
		plans:  s,
		taxes:  s,
		prices: &quoteFakePrices{amounts: map[string]int64{priceID: 20000}},
	}
	w := quoteServe(t, h, fmt.Sprintf(
		`{"items":[{"plan_id":%q,"quantity":1}],"coupon_code":%q,"country":%q}`,
		plan.ID, strings.ToLower(code), strings.ToLower(country)))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	b := quoteDecode(t, w)
	// 5% off 20000 = 1000 discount; 10% VAT on the 19000 left = 1900.
	if b.Data.SubtotalMinor != 20000 || b.Data.DiscountMinor != 1000 ||
		b.Data.TaxMinor != 1900 || b.Data.TotalMinor != 20900 {
		t.Errorf("totals = %d/%d/%d/%d, want 20000/1000/1900/20900",
			b.Data.SubtotalMinor, b.Data.DiscountMinor, b.Data.TaxMinor, b.Data.TotalMinor)
	}
	if b.Data.AppliedCoupon == nil || b.Data.AppliedCoupon.Code != code {
		t.Errorf("applied_coupon = %+v, want %s", b.Data.AppliedCoupon, code)
	}
	if len(b.Data.TaxRates) != 1 || b.Data.TaxRates[0].Jurisdiction != "VAT-"+country {
		t.Errorf("tax_rates = %+v, want VAT-%s", b.Data.TaxRates, country)
	}
}
