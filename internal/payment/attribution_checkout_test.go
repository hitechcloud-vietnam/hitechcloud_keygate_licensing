package payment

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// End-to-end tests for checkout attribution + wholesale pricing:
// CheckoutByPlan stamping the partner facts on the session (and
// pricing a reseller's wholesale deal into it), and recordOrder
// reading them back into the ledger and feeding the two partner
// ledgers. Stripe is stubbed per test; the partner tables are the
// real store. Skipped without TEST_DATABASE_URL, like the rest of the
// payment suite's integration tests.

// partnerStripeCalls captures what the checkout asked Stripe for —
// every discount coupon created (a wholesale sale may stack the
// buyer's own coupon on top), and the session form.
type partnerStripeCalls struct {
	priceType   string
	prices      int
	coupons     int
	sessions    int
	couponForms []url.Values
	sessionForm url.Values
	// sessionJSON is served for GET /v1/checkout/sessions/<id> — what
	// recordOrder reads the charge back from.
	sessionJSON string
}

// partnerStubStripe points stripe-go at a stub serving the price read
// (the plan catalog price is 2000 in every currency the stub sells),
// the one-time discount coupon(s) and the session create, plus the
// session read recordOrder reconciles from.
func partnerStubStripe(t *testing.T, calls *partnerStripeCalls) {
	t.Helper()
	if calls.priceType == "" {
		calls.priceType = "one_time"
	}
	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/checkout/sessions/"):
			if calls.sessionJSON == "" {
				t.Errorf("unexpected session read %s", r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
			fmt.Fprint(w, calls.sessionJSON)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/prices/"):
			calls.prices++
			fmt.Fprintf(w, `{"id":%q,"object":"price","unit_amount":2000,"currency":"usd","type":%q}`,
				strings.TrimPrefix(r.URL.Path, "/v1/prices/"), calls.priceType)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/coupons":
			calls.coupons++
			body, _ := io.ReadAll(r.Body)
			form, _ := url.ParseQuery(string(body))
			calls.couponForms = append(calls.couponForms, form)
			fmt.Fprintf(w, `{"id":"co_partner_%d","object":"coupon","duration":"once"}`, len(calls.couponForms))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/checkout/sessions":
			calls.sessions++
			body, _ := io.ReadAll(r.Body)
			calls.sessionForm, _ = url.ParseQuery(string(body))
			fmt.Fprint(w, `{"id":"cs_partner_1","object":"checkout.session","url":"https://checkout.stripe.com/c/pay/cs_partner_1"}`)
		default:
			t.Errorf("unexpected Stripe call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// seedPartnerWorld creates one reseller (with a wholesale price on the
// plan) and one affiliate with an active referral code, all under a
// unique suffix so a shared test database cannot collide.
func seedPartnerWorld(t *testing.T, s *store.Store, ctx context.Context, suffix string, plan *model.Plan) (*model.Reseller, *model.Affiliate, *model.ReferralCode) {
	t.Helper()
	res := &model.Reseller{
		Name: "Partner " + suffix, ContactEmail: "partner-" + suffix + "@example.com",
		Status: model.ResellerStatusActive, CommissionBPS: 1000,
	}
	if err := s.CreateReseller(ctx, res); err != nil {
		t.Fatalf("create reseller: %v", err)
	}
	if err := s.SetResellerPriceOverride(ctx, &model.ResellerPriceOverride{
		ResellerID: res.ID, PlanID: plan.ID, UnitAmountMinor: 1500, Currency: "USD",
	}); err != nil {
		t.Fatalf("set price override: %v", err)
	}
	aff := &model.Affiliate{
		Name: "Affiliate " + suffix, ContactEmail: "aff-" + suffix + "@example.com",
		Status:          model.AffiliateStatusActive,
		CommissionModel: model.AffiliateCommissionModelPercent, CommissionBPS: 500,
	}
	if err := s.CreateAffiliate(ctx, aff); err != nil {
		t.Fatalf("create affiliate: %v", err)
	}
	code := &model.ReferralCode{
		AffiliateID: aff.ID, Code: "RF" + time.Now().Format("150405000"), Active: true,
	}
	if err := s.CreateReferralCode(ctx, code); err != nil {
		t.Fatalf("create referral code: %v", err)
	}
	return res, aff, code
}

// A reseller buying under their own handle is charged their wholesale
// price: the sale is priced at the override (catalog 2000, override
// 1500 → a 500-unit one-time coupon labelled "wholesale"), and every
// attribution fact is stamped on the session for the ledger. A
// currency-mismatched or non-discounting override is ignored and the
// catalog price is charged — attribution stamps either way.
func TestCheckoutByPlan_PartnerAttributionAndWholesale(t *testing.T) {
	s, ctx := openStore(t)
	defer s.Close()
	suffix := time.Now().Format("150405.000")
	plan := payPlanWithPrice(t, s, ctx, "attr"+suffix, "perpetual")
	res, aff, code := seedPartnerWorld(t, s, ctx, suffix, plan)

	calls := &partnerStripeCalls{}
	partnerStubStripe(t, calls)
	h := &StripeHandler{Store: s, BaseURL: "https://hitechcloud.example"}

	t.Run("wholesale deal prices the sale and stamps the facts", func(t *testing.T) {
		status, body := payCheckout(t, h, plan.CheckoutID, url.Values{
			"reseller_code": {res.ContactEmail},
			"ref":           {code.Code},
			"email":         {res.ContactEmail},
		}.Encode())
		if status != http.StatusTemporaryRedirect {
			t.Fatalf("status = %d, want 307: %s", status, body)
		}
		// 2000 catalog − 1500 override: exactly one 500-unit coupon,
		// labelled wholesale and good for one redemption.
		if calls.coupons != 1 {
			t.Fatalf("stripe coupons = %d, want 1", calls.coupons)
		}
		for k, want := range map[string]string{
			"amount_off": "500", "currency": "usd", "duration": "once",
			"max_redemptions": "1", "name": "wholesale",
		} {
			if got := calls.couponForms[0].Get(k); got != want {
				t.Errorf("wholesale coupon %s = %q, want %q", k, got, want)
			}
		}
		for k, want := range map[string]string{
			"line_items[0][price]":                     plan.StripePriceID,
			"line_items[0][quantity]":                  "1",
			"discounts[0][coupon]":                     "co_partner_1",
			"metadata[reseller_id]":                    res.ID,
			"metadata[reseller_email]":                 res.ContactEmail,
			"metadata[referral_code]":                  code.Code,
			"metadata[affiliate_id]":                   aff.ID,
			"metadata[wholesale_override]":             "true",
			"metadata[wholesale_override_price_minor]": "1500",
		} {
			if got := calls.sessionForm.Get(k); got != want {
				t.Errorf("session %s = %q, want %q", k, got, want)
			}
		}
		// A stamped money fact leaves no room for a Stripe promotion
		// code to rewrite the split.
		if got := calls.sessionForm.Get("allow_promotion_codes"); got != "" {
			t.Errorf("allow_promotion_codes = %q, want unset on a wholesale session", got)
		}
	})

	t.Run("currency-mismatched override is ignored", func(t *testing.T) {
		if err := s.SetResellerPriceOverride(ctx, &model.ResellerPriceOverride{
			ResellerID: res.ID, PlanID: plan.ID, UnitAmountMinor: 1500, Currency: "EUR",
		}); err != nil {
			t.Fatalf("update price override: %v", err)
		}
		defer func() {
			_ = s.SetResellerPriceOverride(ctx, &model.ResellerPriceOverride{
				ResellerID: res.ID, PlanID: plan.ID, UnitAmountMinor: 1500, Currency: "USD",
			})
		}()
		couponsBefore := calls.coupons
		status, body := payCheckout(t, h, plan.CheckoutID, url.Values{
			"email": {res.ContactEmail},
		}.Encode())
		if status != http.StatusTemporaryRedirect {
			t.Fatalf("status = %d, want 307: %s", status, body)
		}
		if calls.coupons != couponsBefore {
			t.Errorf("stripe coupons = %d, want unchanged %d (no deal, no discount)",
				calls.coupons, couponsBefore)
		}
		if got := calls.sessionForm.Get("metadata[wholesale_override]"); got != "" {
			t.Errorf("wholesale_override = %q, want unstamped", got)
		}
		// Attribution is stamped regardless: the buyer IS the partner.
		if got := calls.sessionForm.Get("metadata[reseller_id]"); got != res.ID {
			t.Errorf("reseller_id = %q, want %q", got, res.ID)
		}
	})

	t.Run("override at or above catalog charges catalog", func(t *testing.T) {
		if err := s.SetResellerPriceOverride(ctx, &model.ResellerPriceOverride{
			ResellerID: res.ID, PlanID: plan.ID, UnitAmountMinor: 2500, Currency: "USD",
		}); err != nil {
			t.Fatalf("update price override: %v", err)
		}
		defer func() {
			_ = s.SetResellerPriceOverride(ctx, &model.ResellerPriceOverride{
				ResellerID: res.ID, PlanID: plan.ID, UnitAmountMinor: 1500, Currency: "USD",
			})
		}()
		couponsBefore := calls.coupons
		if status, body := payCheckout(t, h, plan.CheckoutID, url.Values{
			"email": {res.ContactEmail},
		}.Encode()); status != http.StatusTemporaryRedirect {
			t.Fatalf("status = %d, want 307: %s", status, body)
		}
		if calls.coupons != couponsBefore {
			t.Errorf("stripe coupons = %d, want unchanged %d (a wholesale deal can only discount)",
				calls.coupons, couponsBefore)
		}
		if got := calls.sessionForm.Get("metadata[wholesale_override]"); got != "" {
			t.Errorf("wholesale_override = %q, want unstamped", got)
		}
	})
}

// recordOrder reads the stamped attribution back into the ledger
// (stamped facts win over any buyer-email re-derivation) and feeds the
// two partner ledgers: a reseller commission accrued on the order
// total at the reseller's rate, and one pending affiliate conversion
// at the affiliate's model. A replayed fulfilment is still one order,
// one commission, one conversion.
func TestRecordOrder_PartnerAttributionAndLedgers(t *testing.T) {
	s, ctx := openStore(t)
	defer s.Close()
	suffix := time.Now().Format("150405.000")
	plan := payPlanWithPrice(t, s, ctx, "attrledg"+suffix, "perpetual")
	res, aff, code := seedPartnerWorld(t, s, ctx, suffix, plan)
	// A second reseller is the buyer email on one session, to prove
	// the stamped attribution wins over the buyer-email fallback.
	other := &model.Reseller{
		Name: "Buyer " + suffix, ContactEmail: "buyer-" + suffix + "@example.com",
		Status: model.ResellerStatusActive, CommissionBPS: 500,
	}
	if err := s.CreateReseller(ctx, other); err != nil {
		t.Fatalf("create second reseller: %v", err)
	}

	calls := &partnerStripeCalls{}
	partnerStubStripe(t, calls)
	h := &StripeHandler{Store: s}

	t.Run("stamped attribution feeds both ledgers", func(t *testing.T) {
		sessionID := "cs_attr_ledg_" + suffix
		calls.sessionJSON = fmt.Sprintf(`{"id":%q,"object":"checkout.session","amount_total":2000,"currency":"usd",
			"total_details":{"amount_discount":500},
			"metadata":{"reseller_id":%q,"reseller_email":%q,"referral_code":%q,"affiliate_id":%q,
				"wholesale_override":"true","wholesale_override_price_minor":"1500"}}`,
			sessionID, res.ID, res.ContactEmail, code.Code, aff.ID)
		// Twice: one paid session yields one order — and one row in
		// each partner ledger.
		for range 2 {
			h.recordOrder(ctx, nil, plan, sessionID, other.ContactEmail, "Product")
		}

		o, err := s.FindOrderByExternalID(ctx, "stripe", sessionID)
		if err != nil || o == nil {
			t.Fatalf("order not recorded: %v", err)
		}
		if o.ResellerID != res.ID || o.ResellerEmail != res.ContactEmail {
			t.Errorf("reseller attribution = %q/%q, want the stamped %q/%q (not the buyer match %q)",
				o.ResellerID, o.ResellerEmail, res.ID, res.ContactEmail, other.ID)
		}
		if o.ReferralCode != code.Code || o.AffiliateID != aff.ID {
			t.Errorf("affiliate attribution = %q/%q, want %q/%q",
				o.ReferralCode, o.AffiliateID, code.Code, aff.ID)
		}

		cm, err := s.FindCommissionByOrder(ctx, res.ID, o.ID)
		if err != nil {
			t.Fatalf("commission not accrued: %v", err)
		}
		if cm.BasisMinor != o.TotalMinor || cm.BPS != 1000 {
			t.Errorf("commission = basis %d bps %d, want basis %d bps %d (the reseller's rate)",
				cm.BasisMinor, cm.BPS, o.TotalMinor, 1000)
		}
		if want := model.CommissionAmount(o.TotalMinor, 1000); cm.AmountMinor != want {
			t.Errorf("commission amount = %d, want %d (store-computed floor)", cm.AmountMinor, want)
		}

		conv, err := s.FindConversionByOrderID(ctx, o.ID)
		if err != nil {
			t.Fatalf("conversion not recorded: %v", err)
		}
		if conv.AffiliateID != aff.ID || conv.CodeID != code.ID {
			t.Errorf("conversion = %q/%q, want %q/%q", conv.AffiliateID, conv.CodeID, aff.ID, code.ID)
		}
		if conv.OrderTotalMinor != o.TotalMinor {
			t.Errorf("conversion total = %d, want %d", conv.OrderTotalMinor, o.TotalMinor)
		}
		if want := aff.CommissionFor(o.TotalMinor); conv.CommissionMinor != want {
			t.Errorf("conversion commission = %d, want %d (the affiliate's model)",
				conv.CommissionMinor, want)
		}
		if conv.Status != model.AffiliateConversionStatusPending {
			t.Errorf("conversion status = %q, want pending", conv.Status)
		}

		// Replay: the idempotent ledgers answer with the same rows.
		var n int
		if err := s.DB.NewRaw("SELECT count(*) FROM commissions WHERE order_id = ?", o.ID).Scan(ctx, &n); err != nil {
			t.Fatalf("count commissions: %v", err)
		}
		if n != 1 {
			t.Errorf("commissions for one order = %d, want 1", n)
		}
		if err := s.DB.NewRaw("SELECT count(*) FROM affiliate_conversions WHERE order_id = ?", o.ID).Scan(ctx, &n); err != nil {
			t.Fatalf("count conversions: %v", err)
		}
		if n != 1 {
			t.Errorf("conversions for one order = %d, want 1", n)
		}
	})

	t.Run("unstamped attribution falls back to the buyer-email match", func(t *testing.T) {
		sessionID := "cs_attr_fb_" + suffix
		calls.sessionJSON = fmt.Sprintf(`{"id":%q,"object":"checkout.session","amount_total":2000,"currency":"usd",
			"total_details":{},"metadata":{}}`, sessionID)
		h.recordOrder(ctx, nil, plan, sessionID, other.ContactEmail, "Product")

		o, err := s.FindOrderByExternalID(ctx, "stripe", sessionID)
		if err != nil || o == nil {
			t.Fatalf("order not recorded: %v", err)
		}
		if o.ResellerID != other.ID || o.ResellerEmail != other.ContactEmail {
			t.Errorf("reseller attribution = %q/%q, want the buyer match %q/%q",
				o.ResellerID, o.ResellerEmail, other.ID, other.ContactEmail)
		}
		if o.ReferralCode != "" || o.AffiliateID != "" {
			t.Errorf("affiliate attribution = %q/%q, want none", o.ReferralCode, o.AffiliateID)
		}
		if _, err := s.FindCommissionByOrder(ctx, other.ID, o.ID); err != nil {
			t.Errorf("buyer-match commission not accrued: %v", err)
		}
	})
}
