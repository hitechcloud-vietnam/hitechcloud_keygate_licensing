package payment

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// End-to-end tests for the coupon + tax terms of a checkout:
// CheckoutByPlan pricing a session from the plan's Stripe Price with
// the buyer's coupon code and tax country, and recordOrder writing the
// stamped facts into the ledger. Stripe is stubbed per test (the tests
// also prove which Stripe calls are made); the catalog tables are the
// real store.

// payStripeCalls captures what CheckoutByPlan asked Stripe for.
type payStripeCalls struct {
	priceType   string
	prices      int
	coupons     int
	sessions    int
	couponForm  url.Values
	sessionForm url.Values
}

// payStubStripe points stripe-go at a stub that serves the price read,
// the one-time discount coupon and the session create, capturing the
// forms of the two writes.
func payStubStripe(t *testing.T, calls *payStripeCalls) {
	t.Helper()
	if calls.priceType == "" {
		calls.priceType = "one_time"
	}
	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/prices/"):
			calls.prices++
			recurring := ""
			if calls.priceType == "recurring" {
				recurring = `,"recurring":{"interval":"month","interval_count":1}`
			}
			fmt.Fprintf(w, `{"id":%q,"object":"price","unit_amount":2000,"currency":"usd","type":%q%s}`,
				strings.TrimPrefix(r.URL.Path, "/v1/prices/"), calls.priceType, recurring)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/coupons":
			calls.coupons++
			body, _ := io.ReadAll(r.Body)
			calls.couponForm, _ = url.ParseQuery(string(body))
			fmt.Fprint(w, `{"id":"co_pay_1","object":"coupon","duration":"once"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/checkout/sessions":
			calls.sessions++
			body, _ := io.ReadAll(r.Body)
			calls.sessionForm, _ = url.ParseQuery(string(body))
			fmt.Fprint(w, `{"id":"cs_pay_1","object":"checkout.session","url":"https://checkout.stripe.com/c/pay/cs_pay_1"}`)
		default:
			t.Errorf("unexpected Stripe call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// payPlanWithPrice seeds a plan with a Stripe Price id — the one fact
// CheckoutByPlan prices from.
func payPlanWithPrice(t *testing.T, s *store.Store, ctx context.Context, tag, licenseType string) *model.Plan {
	t.Helper()
	plan := seedPlan(t, s, ctx, tag, licenseType)
	plan.StripePriceID = "price_" + plan.Slug
	if err := s.UpdatePlan(ctx, plan); err != nil {
		t.Fatalf("update plan: %v", err)
	}
	return plan
}

// payCheckout runs GET /pay/:checkout_id[?query] through the handler.
func payCheckout(t *testing.T, h *StripeHandler, checkoutID, query string) (int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	target := "/pay/" + checkoutID
	if query != "" {
		target += "?" + query
	}
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	c.Params = gin.Params{{Key: "checkout_id", Value: checkoutID}}
	h.CheckoutByPlan(c)
	return w.Code, w.Body.String()
}

// A usable coupon is validated server-side and priced into the session
// as a real one-time Stripe coupon for exactly the computed discount,
// and the coupon/tax facts are stamped on the session for the ledger.
// The plan's Price stays the line item.
func TestCheckoutByPlan_CouponDiscountOnSession(t *testing.T) {
	s, ctx := openStore(t)
	defer s.Close()
	plan := payPlanWithPrice(t, s, ctx, "payc", "perpetual")
	code := "CPN-" + plan.Slug
	if err := s.CreateCoupon(ctx, &model.Coupon{
		Code: code, Type: model.CouponTypePercentOff, ValueBPS: 1000,
		Stackable: true, Active: true,
	}); err != nil {
		t.Fatalf("create coupon: %v", err)
	}

	calls := &payStripeCalls{}
	payStubStripe(t, calls)
	h := &StripeHandler{Store: s, BaseURL: "https://hitechcloud.example"}

	// The code is matched case-insensitively, like every lookup here.
	codeBody := url.Values{"coupon_code": {strings.ToLower(code)}}.Encode()
	wcode, body := payCheckout(t, h, plan.CheckoutID, codeBody)
	if wcode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307: %s", wcode, body)
	}
	if calls.coupons != 1 || calls.sessions != 1 {
		t.Fatalf("stripe calls: %d coupons, %d sessions, want 1 and 1", calls.coupons, calls.sessions)
	}

	// 10% off the 2000-unit price: the Stripe coupon carries exactly
	// the engine's 200-unit discount, once.
	for k, want := range map[string]string{
		"amount_off": "200", "currency": "usd", "duration": "once", "max_redemptions": "1",
	} {
		if got := calls.couponForm.Get(k); got != want {
			t.Errorf("coupon param %s: got %q want %q", k, got, want)
		}
	}
	// The discount is attached to the session and nothing about the
	// plan's own Price changed.
	for k, want := range map[string]string{
		"mode":                       "payment",
		"line_items[0][price]":       plan.StripePriceID,
		"line_items[0][quantity]":    "1",
		"discounts[0][coupon]":       "co_pay_1",
		"metadata[plan_id]":          plan.ID,
		"metadata[coupon_code]":      strings.ToUpper(code),
		"metadata[coupon_type]":      "percent_off",
		"metadata[coupon_value_bps]": "1000",
		"success_url":                "https://hitechcloud.example/checkout/success?session_id={CHECKOUT_SESSION_ID}",
	} {
		if got := calls.sessionForm.Get(k); got != want {
			t.Errorf("session param %s: got %q want %q", k, got, want)
		}
	}
	// Facts that do not apply are omitted, not stamped as zero: a
	// percent coupon has no coupon_value_minor, and there is no tax.
	for _, k := range []string{"metadata[coupon_value_minor]", "metadata[tax_jurisdiction]",
		"metadata[tax_basis_points]", "metadata[tax_inclusive]",
		"line_items[1][price_data][unit_amount]"} {
		if got := calls.sessionForm.Get(k); got != "" {
			t.Errorf("session param %s: got %q, want unstamped", k, got)
		}
	}
	// A session priced with the operator's coupon records it in
	// metadata; a Stripe promotion code would rewrite that split.
	if got := calls.sessionForm.Get("allow_promotion_codes"); got != "" {
		t.Errorf("allow_promotion_codes = %q, want unset on a stamped session", got)
	}
}

// A coupon that cannot be used is refused before any Stripe object for
// the sale exists — no coupon, no session, no charge to correct.
func TestCheckoutByPlan_UnusableCouponCreatesNoSession(t *testing.T) {
	s, ctx := openStore(t)
	defer s.Close()
	plan := payPlanWithPrice(t, s, ctx, "paybad", "perpetual")
	if err := s.CreateCoupon(ctx, &model.Coupon{
		Code: "DEAD-" + plan.Slug, Type: model.CouponTypePercentOff, ValueBPS: 1000,
		Stackable: true, Active: true,
		EndsAt: timePtr(-3600),
	}); err != nil {
		t.Fatalf("create coupon: %v", err)
	}
	if err := s.CreateCoupon(ctx, &model.Coupon{
		Code: "RICH-" + plan.Slug, Type: model.CouponTypePercentOff, ValueBPS: 1000,
		Stackable: true, Active: true, MinimumOrderMinor: 1_000_000,
	}); err != nil {
		t.Fatalf("create coupon: %v", err)
	}

	calls := &payStripeCalls{}
	payStubStripe(t, calls)
	h := &StripeHandler{Store: s, BaseURL: "https://hitechcloud.example"}

	for _, tc := range []struct {
		name    string
		code    string
		wantMsg string
	}{
		{name: "unknown code", code: "NOSUCH-" + plan.Slug, wantMsg: "coupon not found"},
		{name: "expired code", code: "DEAD-" + plan.Slug, wantMsg: "coupon not usable"},
		{name: "minimum not met", code: "RICH-" + plan.Slug, wantMsg: "coupon not usable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			codeBody := url.Values{"coupon_code": {strings.ToLower(tc.code)}}.Encode()
			status, body := payCheckout(t, h, plan.CheckoutID, codeBody)
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", status, body)
			}
			if !strings.Contains(body, tc.wantMsg) {
				t.Errorf("body = %q, want it to mention %q", body, tc.wantMsg)
			}
		})
	}
	if calls.sessions != 0 || calls.coupons != 0 {
		t.Fatalf("refused coupons still created %d session(s) and %d Stripe coupon(s)",
			calls.sessions, calls.coupons)
	}
}

// The tax country is matched against the operator's rate table —
// exclusive tax is added to the amount charged as its own line, and
// inclusive tax, which already lives in the listed price, is not.
func TestCheckoutByPlan_TaxCountry(t *testing.T) {
	s, ctx := openStore(t)
	defer s.Close()
	plan := payPlanWithPrice(t, s, ctx, "paytax", "perpetual")
	country := strings.ToUpper(plan.Slug)
	if err := s.CreateTaxRate(ctx, &model.TaxRate{
		Jurisdiction: "VAT-" + plan.Slug, BasisPoints: 1000, Country: country, Active: true,
	}); err != nil {
		t.Fatalf("create tax rate: %v", err)
	}
	// A country whose rates are all marked inclusive-of-price: with
	// nothing said on the link, they decide the pricing mode.
	inclCountry := "INC-" + country
	if err := s.CreateTaxRate(ctx, &model.TaxRate{
		Jurisdiction: "VAT-INC-" + plan.Slug, BasisPoints: 1000,
		Country: inclCountry, Inclusive: true, Active: true,
	}); err != nil {
		t.Fatalf("create tax rate: %v", err)
	}

	calls := &payStripeCalls{}
	payStubStripe(t, calls)
	h := &StripeHandler{Store: s, BaseURL: "https://hitechcloud.example"}

	t.Run("exclusive adds the tax to the charge", func(t *testing.T) {
		status, body := payCheckout(t, h, plan.CheckoutID, url.Values{
			"country": {strings.ToLower(country)}, "tax_inclusive": {"false"},
		}.Encode())
		if status != http.StatusTemporaryRedirect {
			t.Fatalf("status = %d, want 307: %s", status, body)
		}
		// 10% on the 2000-unit price: a 200-unit tax line on top.
		for k, want := range map[string]string{
			"line_items[0][price]":                          plan.StripePriceID,
			"line_items[1][price_data][unit_amount]":        "200",
			"line_items[1][price_data][currency]":           "usd",
			"line_items[1][price_data][product_data][name]": "Tax (VAT-" + plan.Slug + ")",
			"metadata[tax_jurisdiction]":                    "VAT-" + plan.Slug,
			"metadata[tax_basis_points]":                    "1000",
			"metadata[tax_inclusive]":                       "false",
		} {
			if got := calls.sessionForm.Get(k); got != want {
				t.Errorf("session param %s: got %q want %q", k, got, want)
			}
		}
		if got := calls.sessionForm.Get("metadata[coupon_code]"); got != "" {
			t.Errorf("coupon metadata = %q, want unstamped without a coupon", got)
		}
	})

	t.Run("inclusive never adds the tax again", func(t *testing.T) {
		status, body := payCheckout(t, h, plan.CheckoutID, url.Values{
			"country": {strings.ToLower(country)}, "tax_inclusive": {"true"},
		}.Encode())
		if status != http.StatusTemporaryRedirect {
			t.Fatalf("status = %d, want 307: %s", status, body)
		}
		if got := calls.sessionForm.Get("line_items[1][price_data][unit_amount]"); got != "" {
			t.Errorf("tax line = %q, want none on an inclusive sale", got)
		}
		if got := calls.sessionForm.Get("metadata[tax_inclusive]"); got != "true" {
			t.Errorf("metadata[tax_inclusive] = %q, want true", got)
		}
		if got := calls.sessionForm.Get("metadata[tax_basis_points]"); got != "1000" {
			t.Errorf("metadata[tax_basis_points] = %q, want 1000", got)
		}
	})

	t.Run("inclusive rate rows decide when the link says nothing", func(t *testing.T) {
		status, body := payCheckout(t, h, plan.CheckoutID,
			url.Values{"country": {strings.ToLower(inclCountry)}}.Encode())
		if status != http.StatusTemporaryRedirect {
			t.Fatalf("status = %d, want 307: %s", status, body)
		}
		if got := calls.sessionForm.Get("line_items[1][price_data][unit_amount]"); got != "" {
			t.Errorf("tax line = %q, want none when the rates are inclusive", got)
		}
		if got := calls.sessionForm.Get("metadata[tax_inclusive]"); got != "true" {
			t.Errorf("metadata[tax_inclusive] = %q, want true", got)
		}
	})

	t.Run("country without rates is untaxed and unpromoted", func(t *testing.T) {
		status, body := payCheckout(t, h, plan.CheckoutID, url.Values{"country": {"fr"}}.Encode())
		if status != http.StatusTemporaryRedirect {
			t.Fatalf("status = %d, want 307: %s", status, body)
		}
		if got := calls.sessionForm.Get("line_items[1][price_data][unit_amount]"); got != "" {
			t.Errorf("tax line = %q, want none without rates", got)
		}
		if got := calls.sessionForm.Get("metadata[tax_basis_points]"); got != "" {
			t.Errorf("tax metadata = %q, want unstamped without rates", got)
		}
		// No terms at all: today's behaviour, promotion codes stay.
		if got := calls.sessionForm.Get("allow_promotion_codes"); got != "true" {
			t.Errorf("allow_promotion_codes = %q, want true on an unstamped session", got)
		}
	})

	t.Run("subscription tax line renews with the plan price", func(t *testing.T) {
		sub := payPlanWithPrice(t, s, ctx, "paytaxsub", "subscription")
		subCountry := strings.ToUpper(sub.Slug)
		if err := s.CreateTaxRate(ctx, &model.TaxRate{
			Jurisdiction: "VAT-" + sub.Slug, BasisPoints: 1000, Country: subCountry, Active: true,
		}); err != nil {
			t.Fatalf("create tax rate: %v", err)
		}
		calls.priceType = "recurring"
		defer func() { calls.priceType = "one_time" }()
		status, body := payCheckout(t, h, sub.CheckoutID, url.Values{
			"country": {strings.ToLower(subCountry)}, "tax_inclusive": {"false"},
		}.Encode())
		if status != http.StatusTemporaryRedirect {
			t.Fatalf("status = %d, want 307: %s", status, body)
		}
		if got := calls.sessionForm.Get("mode"); got != "subscription" {
			t.Errorf("mode = %q, want subscription", got)
		}
		for k, want := range map[string]string{
			"line_items[1][price_data][unit_amount]":               "200",
			"line_items[1][price_data][recurring][interval]":       "month",
			"line_items[1][price_data][recurring][interval_count]": "1",
		} {
			if got := calls.sessionForm.Get(k); got != want {
				t.Errorf("session param %s: got %q want %q", k, got, want)
			}
		}
	})
}

// recordOrder prefers the stamped facts for the ledger's coupon/tax
// columns and splits the charge accordingly; a session without them
// falls back to the totals-derived reconstruction it always used.
// One paid session still yields exactly one order.
func TestRecordOrder_PrefersStampedTerms(t *testing.T) {
	s, ctx := openStore(t)
	defer s.Close()
	plan := payPlanWithPrice(t, s, ctx, "payledg", "perpetual")

	var sessionJSON string
	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/") {
			fmt.Fprint(w, sessionJSON)
			return
		}
		t.Errorf("unexpected Stripe call %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	})
	h := &StripeHandler{Store: s}

	for _, tc := range []struct {
		name string
		id   string
		json string
		want model.Order
	}{
		{
			// 2000 list − 200 coupon + 180 VAT on the 1800 left.
			name: "exclusive stamp",
			id:   "cs_ledg_ex",
			json: `{"id":"cs_ledg_ex","object":"checkout.session","amount_total":1980,"currency":"usd",
				"total_details":{"amount_discount":200},
				"metadata":{"coupon_code":"SAVE10","coupon_type":"percent_off","coupon_value_bps":"1000",
					"tax_jurisdiction":"VAT-VN","tax_basis_points":"1000","tax_inclusive":"false"}}`,
			want: model.Order{
				SubtotalMinor: 2000, DiscountMinor: 200, TaxMinor: 180, TotalMinor: 1980,
				CouponCode: "SAVE10", CouponType: "percent_off", CouponValueBPS: 1000,
				TaxJurisdiction: "VAT-VN", TaxBasisPoints: 1000,
			},
		},
		{
			// 2000 list − 200 coupon, the 164 VAT inside the 1800.
			name: "inclusive stamp",
			id:   "cs_ledg_in",
			json: `{"id":"cs_ledg_in","object":"checkout.session","amount_total":1800,"currency":"usd",
				"total_details":{"amount_discount":200},
				"metadata":{"coupon_code":"SAVE10","coupon_type":"percent_off","coupon_value_bps":"1000",
					"tax_jurisdiction":"VAT-VN","tax_basis_points":"1000","tax_inclusive":"true"}}`,
			want: model.Order{
				SubtotalMinor: 2000, DiscountMinor: 200, TaxMinor: 164, TotalMinor: 1800,
				CouponCode: "SAVE10", CouponType: "percent_off", CouponValueBPS: 1000,
				TaxJurisdiction: "VAT-VN", TaxBasisPoints: 1000, TaxInclusive: true,
			},
		},
		{
			// A fixed coupon stamps its value in minor units and no
			// tax facts at all.
			name: "fixed coupon stamp",
			id:   "cs_ledg_fx",
			json: `{"id":"cs_ledg_fx","object":"checkout.session","amount_total":1500,"currency":"usd",
				"total_details":{"amount_discount":500},
				"metadata":{"coupon_code":"TAKE5","coupon_type":"fixed_amount_off","coupon_value_minor":"500"}}`,
			want: model.Order{
				SubtotalMinor: 2000, DiscountMinor: 500, TaxMinor: 0, TotalMinor: 1500,
				CouponCode: "TAKE5", CouponType: "fixed_amount_off", CouponValueMinor: 500,
			},
		},
		{
			// Nothing stamped: the historical reconstruction from the
			// totals Stripe reports, no coupon/tax columns.
			name: "fallback without a stamp",
			id:   "cs_ledg_plain",
			json: `{"id":"cs_ledg_plain","object":"checkout.session","amount_total":2400,"currency":"usd",
				"total_details":{"amount_discount":200,"amount_tax":150},
				"metadata":{"plan_id":"plan_x"}}`,
			want: model.Order{
				SubtotalMinor: 2450, DiscountMinor: 200, TaxMinor: 150, TotalMinor: 2400,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessionJSON = tc.json
			// Twice: one paid session yields one order.
			for range 2 {
				h.recordOrder(ctx, nil, plan, tc.id, "buyer@example.com", "Product")
			}
			o, err := s.FindOrderByExternalID(ctx, "stripe", tc.id)
			if err != nil || o == nil {
				t.Fatalf("order not recorded: %v", err)
			}
			if o.SubtotalMinor != tc.want.SubtotalMinor || o.DiscountMinor != tc.want.DiscountMinor ||
				o.TaxMinor != tc.want.TaxMinor || o.TotalMinor != tc.want.TotalMinor {
				t.Errorf("money = %d/%d/%d/%d, want %d/%d/%d/%d",
					o.SubtotalMinor, o.DiscountMinor, o.TaxMinor, o.TotalMinor,
					tc.want.SubtotalMinor, tc.want.DiscountMinor, tc.want.TaxMinor, tc.want.TotalMinor)
			}
			if o.CouponCode != tc.want.CouponCode || o.CouponType != tc.want.CouponType ||
				o.CouponValueBPS != tc.want.CouponValueBPS || o.CouponValueMinor != tc.want.CouponValueMinor {
				t.Errorf("coupon columns = %q/%q/%d/%d, want %q/%q/%d/%d",
					o.CouponCode, o.CouponType, o.CouponValueBPS, o.CouponValueMinor,
					tc.want.CouponCode, tc.want.CouponType, tc.want.CouponValueBPS, tc.want.CouponValueMinor)
			}
			if o.TaxJurisdiction != tc.want.TaxJurisdiction || o.TaxBasisPoints != tc.want.TaxBasisPoints ||
				o.TaxInclusive != tc.want.TaxInclusive {
				t.Errorf("tax columns = %q/%d/%v, want %q/%d/%v",
					o.TaxJurisdiction, o.TaxBasisPoints, o.TaxInclusive,
					tc.want.TaxJurisdiction, tc.want.TaxBasisPoints, tc.want.TaxInclusive)
			}
			// The ledger invariant, whatever the pricing mode.
			if tc.want.TaxInclusive {
				if o.SubtotalMinor-o.DiscountMinor != o.TotalMinor {
					t.Errorf("inclusive invariant: %d − %d != %d", o.SubtotalMinor, o.DiscountMinor, o.TotalMinor)
				}
			} else if o.SubtotalMinor-o.DiscountMinor+o.TaxMinor != o.TotalMinor {
				t.Errorf("exclusive invariant: %d − %d + %d != %d",
					o.SubtotalMinor, o.DiscountMinor, o.TaxMinor, o.TotalMinor)
			}
			var n int
			if err := s.DB.NewRaw("SELECT count(*) FROM orders WHERE external_id = ?", tc.id).Scan(ctx, &n); err != nil {
				t.Fatalf("count orders: %v", err)
			}
			if n != 1 {
				t.Errorf("orders for one session = %d, want 1", n)
			}
		})
	}
}

// timePtr is now shifted by seconds, for coupon validity windows.
func timePtr(seconds int) *time.Time {
	t := time.Now().Add(time.Duration(seconds) * time.Second)
	return &t
}
