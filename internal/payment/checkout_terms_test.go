package payment

import (
	"context"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/coupon"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/tax"
)

// These tests exercise the checkout-terms core with no database and no
// Stripe: the pricing is the shared engines' arithmetic, and the
// metadata/split round-trips are pure functions. The end-to-end path
// through CheckoutByPlan and recordOrder is covered by the DB-backed
// tests in checkout_by_plan_test.go.

func termsTestPlan() *model.Plan {
	return &model.Plan{
		ID: "plan_terms", ProductID: "prod_terms", Name: "Terms Plan", Slug: "terms-plan",
		LicenseType: "perpetual", LicenseModel: "standard",
	}
}

// The sale is priced exactly as the quote endpoint prices it: the
// coupon engine's discount on the Stripe Price's unit amount, then the
// tax engine on what is left — added on top when exclusive, already
// inside when inclusive. The ledger invariants hold in every case.
func TestPriceCheckoutMatchesTheQuoteMath(t *testing.T) {
	plan := termsTestPlan()
	save10 := &coupon.Coupon{Code: "SAVE10", Type: coupon.TypePercentOff, Value: 1000, Active: true}
	fixed := &coupon.Coupon{Code: "TAKE5", Type: coupon.TypeFixedAmountOff, Value: 500, Currency: "USD", Active: true}
	vat := []tax.Rate{{Jurisdiction: "VAT-VN", BasisPoints: 1000}}

	for _, tc := range []struct {
		name      string
		unit      int64
		cpn       *coupon.Coupon
		rates     []tax.Rate
		inclusive bool
		subtotal  int64
		discount  int64
		taxMinor  int64
		total     int64
	}{
		{name: "no terms", unit: 2000, subtotal: 2000, total: 2000},
		{name: "percent", unit: 2000, cpn: save10, subtotal: 2000, discount: 200, total: 1800},
		{name: "fixed", unit: 2000, cpn: fixed, subtotal: 2000, discount: 500, total: 1500},
		{name: "exclusive tax", unit: 2000, rates: vat, subtotal: 2000, taxMinor: 200, total: 2200},
		{name: "coupon and exclusive tax", unit: 2000, cpn: save10, rates: vat,
			subtotal: 2000, discount: 200, taxMinor: 180, total: 1980},
		{name: "coupon and inclusive tax", unit: 2000, cpn: save10, rates: vat, inclusive: true,
			subtotal: 2000, discount: 200, taxMinor: 164, total: 1800},
		{name: "inclusive tax alone", unit: 2000, rates: vat, inclusive: true,
			subtotal: 2000, taxMinor: 182, total: 2000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := priceCheckout(context.Background(), plan, tc.unit, "USD", tc.cpn, tc.rates, tc.inclusive)
			if err != nil {
				t.Fatalf("priceCheckout: %v", err)
			}
			if res.SubtotalMinor != tc.subtotal || res.DiscountMinor != tc.discount ||
				res.TaxMinor != tc.taxMinor || res.TotalMinor != tc.total {
				t.Errorf("money = %d/%d/%d/%d, want %d/%d/%d/%d",
					res.SubtotalMinor, res.DiscountMinor, res.TaxMinor, res.TotalMinor,
					tc.subtotal, tc.discount, tc.taxMinor, tc.total)
			}
			if tc.inclusive {
				if res.SubtotalMinor-res.DiscountMinor != res.TotalMinor {
					t.Errorf("inclusive invariant: %d − %d != %d",
						res.SubtotalMinor, res.DiscountMinor, res.TotalMinor)
				}
			} else if res.SubtotalMinor-res.DiscountMinor+res.TaxMinor != res.TotalMinor {
				t.Errorf("exclusive invariant: %d − %d + %d != %d",
					res.SubtotalMinor, res.DiscountMinor, res.TaxMinor, res.TotalMinor)
			}
			if len(res.LineResults) != 1 || res.LineResults[0].LineTotalMinor != res.TotalMinor {
				t.Errorf("lines = %+v, want one line summing to the total", res.LineResults)
			}
		})
	}
}

// An unusable coupon is refused by the pricing — the same refusal the
// quote endpoint answers COUPON_INVALID with — and nothing is priced.
func TestPriceCheckoutRefusesUnusableCoupons(t *testing.T) {
	plan := termsTestPlan()
	past := time.Now().Add(-time.Hour)
	for _, tc := range []struct {
		name string
		cpn  *coupon.Coupon
	}{
		{name: "inactive", cpn: &coupon.Coupon{Code: "OFF", Type: coupon.TypePercentOff, Value: 1000}},
		{name: "expired", cpn: &coupon.Coupon{Code: "OLD", Type: coupon.TypePercentOff, Value: 1000, Active: true, EndsAt: past}},
		{name: "minimum not met", cpn: &coupon.Coupon{Code: "BIG", Type: coupon.TypePercentOff, Value: 1000, Active: true, MinimumOrderAmount: 100_000}},
		{name: "currency mismatch", cpn: &coupon.Coupon{Code: "EUR", Type: coupon.TypeFixedAmountOff, Value: 500, Currency: "EUR", Active: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, err := priceCheckout(context.Background(), plan, 2000, "USD", tc.cpn, nil, false)
			if err == nil {
				t.Fatalf("unusable coupon priced anyway: %+v", res)
			}
		})
	}
}

// The metadata carries the facts the ledger records, in the units the
// Order columns use: basis points for a percent coupon, minor units
// for a fixed one, neither for free shipping, and the tax keys only
// when the sale was matched to rates at all.
func TestCheckoutTermsMetadata(t *testing.T) {
	plan := termsTestPlan()
	rates := []tax.Rate{{Jurisdiction: "VAT-VN", BasisPoints: 900}, {Jurisdiction: "US-CA-SF", BasisPoints: 100}}

	meta := func(t *testing.T, cpn *coupon.Coupon, rs []tax.Rate, inclusive bool) map[string]string {
		t.Helper()
		res, err := priceCheckout(context.Background(), plan, 2000, "USD", cpn, rs, inclusive)
		if err != nil {
			t.Fatalf("priceCheckout: %v", err)
		}
		return checkoutTermsMetadata(res, rs, inclusive)
	}

	t.Run("percent stamps basis points only", func(t *testing.T) {
		md := meta(t, &coupon.Coupon{Code: "save10", Type: coupon.TypePercentOff, Value: 1000, Active: true}, nil, false)
		if md[metaCouponCode] != "SAVE10" || md[metaCouponType] != "percent_off" || md[metaCouponValueBPS] != "1000" {
			t.Errorf("coupon keys = %v, want SAVE10 percent_off 1000", md)
		}
		if _, ok := md[metaCouponValueMinor]; ok {
			t.Errorf("coupon_value_minor must not apply to a percent coupon: %v", md)
		}
		if len(md) != 3 {
			t.Errorf("metadata = %v, want exactly the three applicable coupon keys", md)
		}
	})

	t.Run("fixed stamps minor units only", func(t *testing.T) {
		md := meta(t, &coupon.Coupon{Code: "TAKE5", Type: coupon.TypeFixedAmountOff, Value: 500, Currency: "USD", Active: true}, nil, false)
		if md[metaCouponCode] != "TAKE5" || md[metaCouponType] != "fixed_amount_off" || md[metaCouponValueMinor] != "500" {
			t.Errorf("coupon keys = %v, want TAKE5 fixed_amount_off 500", md)
		}
		if _, ok := md[metaCouponValueBPS]; ok {
			t.Errorf("coupon_value_bps must not apply to a fixed coupon: %v", md)
		}
	})

	t.Run("free shipping stamps neither value", func(t *testing.T) {
		md := meta(t, &coupon.Coupon{Code: "SHIP", Type: coupon.TypeFreeShipping, Active: true}, nil, false)
		if md[metaCouponCode] != "SHIP" || md[metaCouponType] != "free_shipping" {
			t.Errorf("coupon keys = %v, want SHIP free_shipping", md)
		}
		if _, ok := md[metaCouponValueBPS]; ok {
			t.Errorf("coupon_value_bps must not apply to free shipping: %v", md)
		}
		if _, ok := md[metaCouponValueMinor]; ok {
			t.Errorf("coupon_value_minor must not apply to free shipping: %v", md)
		}
	})

	t.Run("tax keys come from the matched rates", func(t *testing.T) {
		md := meta(t, nil, rates, true)
		if md[metaTaxJurisdiction] != "VAT-VN+US-CA-SF" || md[metaTaxBasisPoints] != "1000" || md[metaTaxInclusive] != "true" {
			t.Errorf("tax keys = %v, want VAT-VN+US-CA-SF 1000 true", md)
		}
		if _, ok := md[metaCouponCode]; ok {
			t.Errorf("coupon keys must be omitted without a coupon: %v", md)
		}
	})

	t.Run("exclusive flag and no rates", func(t *testing.T) {
		if md := meta(t, nil, rates, false); md[metaTaxInclusive] != "false" {
			t.Errorf("tax_inclusive = %q, want false", md[metaTaxInclusive])
		}
		if md := meta(t, nil, nil, false); len(md) != 0 {
			t.Errorf("metadata = %v, want empty when no coupon and no rates", md)
		}
	})
}

// The stamped facts read back to the same terms they came from, with
// unstamped keys at their zero values — exactly what the Order columns
// hold for facts that do not apply.
func TestLedgerTermsRoundTrip(t *testing.T) {
	plan := termsTestPlan()
	cpn := &coupon.Coupon{Code: "SAVE10", Type: coupon.TypePercentOff, Value: 1000, Active: true}
	rates := []tax.Rate{{Jurisdiction: "VAT-VN", BasisPoints: 1000}}
	res, err := priceCheckout(context.Background(), plan, 2000, "USD", cpn, rates, true)
	if err != nil {
		t.Fatalf("priceCheckout: %v", err)
	}
	md := checkoutTermsMetadata(res, rates, true)
	md[metaCouponValueMinor] = "17" // a stray key must be honoured too

	terms := ledgerTermsFromMetadata(md)
	if !terms.hasCoupon || !terms.hasTax {
		t.Fatalf("terms = %+v, want both groups present", terms)
	}
	if terms.couponCode != "SAVE10" || terms.couponType != "percent_off" ||
		terms.couponValueBPS != 1000 || terms.couponValueMinor != 17 {
		t.Errorf("coupon terms = %+v", terms)
	}
	if terms.taxJurisdiction != "VAT-VN" || terms.taxBasisPoints != 1000 || !terms.taxInclusive {
		t.Errorf("tax terms = %+v", terms)
	}

	empty := ledgerTermsFromMetadata(map[string]string{"plan_id": "plan_x"})
	if empty.hasCoupon || empty.hasTax {
		t.Errorf("terms = %+v, want nothing stamped", empty)
	}
}

// The money split recovers the tax from the charge itself, for both
// pricing modes — the extraction is the exact inverse of how the
// session was charged, so the ledger's columns name the right things
// and the invariants hold to the minor unit.
func TestLedgerMoneySplitsTheCharge(t *testing.T) {
	exclusive := ledgerTerms{hasTax: true, taxBasisPoints: 1000}
	inclusive := ledgerTerms{hasTax: true, taxBasisPoints: 1000, taxInclusive: true}

	for _, tc := range []struct {
		name                       string
		total, detailsDiscount     int64
		detailsTax                 int64
		terms                      ledgerTerms
		subtotal, discount, taxMin int64
	}{
		// No stamp: the historical reconstruction from totals alone.
		{name: "fallback", total: 2400, detailsDiscount: 200, detailsTax: 150,
			subtotal: 2450, discount: 200, taxMin: 150},
		// 2000 list − 200 coupon + 180 VAT on 1800.
		{name: "exclusive", total: 1980, detailsDiscount: 200, terms: exclusive,
			subtotal: 2000, discount: 200, taxMin: 180},
		// 2000 list − 200 coupon, VAT of 164 inside the 1800 gross.
		{name: "inclusive", total: 1800, detailsDiscount: 200, terms: inclusive,
			subtotal: 2000, discount: 200, taxMin: 164},
		// Tax alone on an undiscounted 2000: charge 2200 with 200 VAT.
		{name: "exclusive without coupon", total: 2200, terms: exclusive,
			subtotal: 2000, taxMin: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			subtotal, discount, taxMin := ledgerMoney(tc.total, tc.detailsDiscount, tc.detailsTax, tc.terms)
			if subtotal != tc.subtotal || discount != tc.discount || taxMin != tc.taxMin {
				t.Errorf("split = %d/%d/%d, want %d/%d/%d",
					subtotal, discount, taxMin, tc.subtotal, tc.discount, tc.taxMin)
			}
			if tc.terms.taxInclusive {
				if subtotal-discount != tc.total {
					t.Errorf("inclusive invariant: %d − %d != %d", subtotal, discount, tc.total)
				}
			} else if subtotal-discount+taxMin != tc.total {
				t.Errorf("exclusive invariant: %d − %d + %d != %d", subtotal, discount, taxMin, tc.total)
			}
		})
	}
}

// TaxExtractedFrom is the exact inverse of the engine's exclusive
// computation: round a tax onto a net, charge the gross, and the
// extraction gives the same tax back. Inclusive amounts simply
// re-run the engine's own extraction.
func TestTaxExtractedFromIsExact(t *testing.T) {
	for _, net := range []int64{0, 1, 7, 99, 100, 999, 1000, 1800, 3333, 12345, 100_000} {
		for _, bps := range []int64{0, 1, 250, 887, 1000, 3333, 5000, 9999, 10_000, 20_000} {
			bd, err := tax.Calculate(net, tax.Rate{BasisPoints: bps}, false, tax.RoundingHalfUp)
			if err != nil {
				t.Fatalf("Calculate(%d, %d): %v", net, bps, err)
			}
			if got := taxExtractedFrom(bd.Gross, bps); got != bd.Tax {
				t.Errorf("exclusive round trip net=%d bps=%d: extracted %d, charged %d",
					net, bps, got, bd.Tax)
			}
			bdIn, err := tax.Calculate(net, tax.Rate{BasisPoints: bps}, true, tax.RoundingHalfUp)
			if err != nil {
				t.Fatalf("Calculate inclusive(%d, %d): %v", net, bps, err)
			}
			if got := taxExtractedFrom(net, bps); got != bdIn.Tax {
				t.Errorf("inclusive extraction net=%d bps=%d: got %d, want %d",
					net, bps, got, bdIn.Tax)
			}
		}
	}
}

func TestCheckoutTermsFlags(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "0": false, "no": false, "false": false,
		"1": true, "true": true, "TRUE": true, "yes": true, " Yes ": true, "on": false} {
		if got := queryFlag(raw); got != want {
			t.Errorf("queryFlag(%q) = %v, want %v", raw, got, want)
		}
	}
	inc := &model.TaxRate{Inclusive: true}
	exc := &model.TaxRate{}
	if allRatesInclusive(nil) {
		t.Error("no rates must not read as inclusive")
	}
	if !allRatesInclusive([]*model.TaxRate{inc, inc}) {
		t.Error("all-inclusive rows must read as inclusive")
	}
	if allRatesInclusive([]*model.TaxRate{inc, exc}) {
		t.Error("a single exclusive row must make the sale exclusive")
	}
}
