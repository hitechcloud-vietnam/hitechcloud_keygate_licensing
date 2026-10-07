package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/coupon"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/tax"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
)

// ─── fakes & helpers ───
//
// The tests drive Calculate against the real money/coupon/tax engines
// and a fake coupon lookup: no database, no store, nothing that could
// make `go test ./internal/service/ -run Order` depend on the world.

type orderFakeLookup struct {
	coupons map[string]coupon.Coupon
	err     error // returned before any lookup, when set
}

func (f *orderFakeLookup) FindCouponByCode(_ context.Context, code string) (*coupon.Coupon, error) {
	if f.err != nil {
		return nil, f.err
	}
	c, ok := f.coupons[coupon.NormalizeCode(code)]
	if !ok {
		return nil, fmt.Errorf("coupon %q: %w", code, coupon.ErrCouponNotFound)
	}
	return &c, nil
}

func orderSvcWith(lookup CouponLookup) *OrderService {
	return NewOrderService(nil, lookup)
}

func orderPercent(code string, bps int64) coupon.Coupon {
	return coupon.Coupon{Code: code, Type: coupon.TypePercentOff, Value: bps, Active: true}
}

func orderFixed(code string, minor int64, currency string) coupon.Coupon {
	return coupon.Coupon{Code: code, Type: coupon.TypeFixedAmountOff, Value: minor, Currency: currency, Active: true}
}

func orderLine(qty, unit int64) Line {
	return Line{SKU: "SKU", Quantity: qty, UnitAmountMinor: unit}
}

func orderLookupWith(cs ...coupon.Coupon) *orderFakeLookup {
	m := make(map[string]coupon.Coupon, len(cs))
	for _, c := range cs {
		m[coupon.NormalizeCode(c.Code)] = c
	}
	return &orderFakeLookup{coupons: m}
}

// orderCheckSum asserts the result's totals are exactly the sum of its
// lines and that each line satisfies its own money identity.
func orderCheckSum(t *testing.T, res *CalcResult, inclusive bool) {
	t.Helper()
	var sub, disc, taxSum, tot int64
	for i, lr := range res.LineResults {
		if want := lr.Quantity * lr.UnitAmountMinor; lr.LineSubtotalMinor != want {
			t.Errorf("line %d: subtotal %d, want qty*unit %d", i, lr.LineSubtotalMinor, want)
		}
		if inclusive {
			if got, want := lr.LineSubtotalMinor-lr.LineDiscountMinor, lr.LineTotalMinor; got != want {
				t.Errorf("line %d: subtotal-discount = %d, want inclusive total %d", i, got, want)
			}
		} else {
			if got, want := lr.LineSubtotalMinor-lr.LineDiscountMinor+lr.LineTaxMinor, lr.LineTotalMinor; got != want {
				t.Errorf("line %d: subtotal-discount+tax = %d, want total %d", i, got, want)
			}
		}
		sub += lr.LineSubtotalMinor
		disc += lr.LineDiscountMinor
		taxSum += lr.LineTaxMinor
		tot += lr.LineTotalMinor
	}
	if sub != res.SubtotalMinor {
		t.Errorf("sum of line subtotals %d != order subtotal %d", sub, res.SubtotalMinor)
	}
	if disc != res.DiscountMinor {
		t.Errorf("sum of line discounts %d != order discount %d", disc, res.DiscountMinor)
	}
	if taxSum != res.TaxMinor {
		t.Errorf("sum of line taxes %d != order tax %d", taxSum, res.TaxMinor)
	}
	if tot != res.TotalMinor {
		t.Errorf("sum of line totals %d != order total %d", tot, res.TotalMinor)
	}
}

func orderMustCalc(t *testing.T, s *OrderService, in CalcInput) *CalcResult {
	t.Helper()
	res, err := s.Calculate(context.Background(), in)
	if err != nil {
		t.Fatalf("Calculate() error = %v", err)
	}
	if res.Err != nil {
		t.Fatalf("Calculate() result.Err = %v, want nil", res.Err)
	}
	return res
}

// ─── tests ───

func TestOrderCalculateBasicTotals(t *testing.T) {
	svc := orderSvcWith(nil)
	res := orderMustCalc(t, svc, CalcInput{
		Currency: "USD",
		Lines:    []Line{orderLine(2, 1000), orderLine(1, 500)},
	})
	if res.SubtotalMinor != 2500 || res.DiscountMinor != 0 || res.TaxMinor != 0 || res.TotalMinor != 2500 {
		t.Fatalf("totals = %+v, want subtotal 2500, discount 0, tax 0, total 2500", res)
	}
	if res.AppliedCoupon != nil {
		t.Errorf("AppliedCoupon = %+v, want nil", res.AppliedCoupon)
	}
	if len(res.LineResults) != 2 {
		t.Fatalf("len(LineResults) = %d, want 2", len(res.LineResults))
	}
	if res.LineResults[0].LineTotalMinor != 2000 || res.LineResults[1].LineTotalMinor != 500 {
		t.Errorf("line totals = %d, %d, want 2000, 500",
			res.LineResults[0].LineTotalMinor, res.LineResults[1].LineTotalMinor)
	}
	// The invariant, on a plain order.
	if res.SubtotalMinor-res.DiscountMinor+res.TaxMinor != res.TotalMinor {
		t.Errorf("subtotal-discount+tax != total: %+v", res)
	}
	orderCheckSum(t, res, false)
}

func TestOrderCalculatePercentCouponExclusiveTax(t *testing.T) {
	svc := orderSvcWith(orderLookupWith(orderPercent("SAVE10", 1000)))
	res := orderMustCalc(t, svc, CalcInput{
		Currency:   "USD",
		CouponCode: "save10", // matching is case-insensitive
		Lines:      []Line{orderLine(1, 10000)},
		TaxRates:   []tax.Rate{{BasisPoints: 825, Jurisdiction: "US-CA"}},
	})
	// subtotal 10000, 10% off = 1000, taxable base 9000,
	// tax = round(9000*825/10000) = 743, total = 9743.
	if res.SubtotalMinor != 10000 || res.DiscountMinor != 1000 ||
		res.TaxMinor != 743 || res.TotalMinor != 9743 {
		t.Fatalf("totals = %+v, want 10000/1000/743/9743", res)
	}
	if res.AppliedCoupon == nil || res.AppliedCoupon.Code != "SAVE10" {
		t.Fatalf("AppliedCoupon = %+v, want SAVE10", res.AppliedCoupon)
	}
	lr := res.LineResults[0]
	if lr.LineSubtotalMinor != 10000 || lr.LineDiscountMinor != 1000 ||
		lr.LineTaxMinor != 743 || lr.LineTotalMinor != 9743 {
		t.Errorf("line = %+v, want 10000/1000/743/9743", lr)
	}
	if res.SubtotalMinor-res.DiscountMinor+res.TaxMinor != res.TotalMinor {
		t.Errorf("subtotal-discount+tax != total: %+v", res)
	}
	orderCheckSum(t, res, false)
}

func TestOrderCalculateFixedCouponAllocation(t *testing.T) {
	svc := orderSvcWith(orderLookupWith(orderFixed("TAKE100", 100, "USD")))
	res := orderMustCalc(t, svc, CalcInput{
		Currency:   "USD",
		CouponCode: "TAKE100",
		Lines:      []Line{orderLine(1, 100), orderLine(1, 100), orderLine(1, 100)},
	})
	// Subtotal 300, fixed 100 off, allocated largest-remainder over
	// three equal lines: 34, 33, 33 — never 33.33.
	if res.DiscountMinor != 100 {
		t.Fatalf("discount = %d, want 100", res.DiscountMinor)
	}
	want := []int64{34, 33, 33}
	for i, w := range want {
		if res.LineResults[i].LineDiscountMinor != w {
			t.Errorf("line %d discount = %d, want %d", i, res.LineResults[i].LineDiscountMinor, w)
		}
		if res.LineResults[i].LineTotalMinor != 100-w {
			t.Errorf("line %d total = %d, want %d", i, res.LineResults[i].LineTotalMinor, 100-w)
		}
	}
	if res.TotalMinor != 200 {
		t.Errorf("total = %d, want 200", res.TotalMinor)
	}
	orderCheckSum(t, res, false)
}

func TestOrderCalculateHundredPercentCoupon(t *testing.T) {
	svc := orderSvcWith(orderLookupWith(orderPercent("FREE", coupon.PercentBase)))
	res := orderMustCalc(t, svc, CalcInput{
		Currency:   "USD",
		CouponCode: "FREE",
		Lines:      []Line{orderLine(3, 333), orderLine(1, 1)},
		TaxRates:   []tax.Rate{{BasisPoints: 1000, Jurisdiction: "US-CA"}},
	})
	// 100% off: discount = subtotal = 1000, taxable base 0, tax 0.
	if res.SubtotalMinor != 1000 || res.DiscountMinor != 1000 ||
		res.TaxMinor != 0 || res.TotalMinor != 0 {
		t.Fatalf("totals = %+v, want 1000/1000/0/0", res)
	}
	for i, lr := range res.LineResults {
		if lr.LineTotalMinor != 0 || lr.LineTaxMinor != 0 {
			t.Errorf("line %d = %+v, want zero total and tax", i, lr)
		}
	}
	orderCheckSum(t, res, false)
}

func TestOrderCalculateEmptyCouponNoDiscount(t *testing.T) {
	svc := orderSvcWith(orderLookupWith(orderPercent("SAVE10", 1000)))
	res := orderMustCalc(t, svc, CalcInput{
		Currency: "USD",
		Lines:    []Line{orderLine(1, 10000)},
		TaxRates: []tax.Rate{{BasisPoints: 1000}},
	})
	if res.DiscountMinor != 0 || res.AppliedCoupon != nil {
		t.Fatalf("discount = %d, coupon = %+v, want 0 and nil", res.DiscountMinor, res.AppliedCoupon)
	}
	if res.TaxMinor != 1000 || res.TotalMinor != 11000 {
		t.Errorf("totals = %+v, want tax 1000 and total 11000", res)
	}
	orderCheckSum(t, res, false)
}

func TestOrderCalculateNoLookupSkipsCoupon(t *testing.T) {
	// No lookup wired: a code asked for must not fail the quote, it
	// simply does not discount anything.
	svc := orderSvcWith(nil)
	res := orderMustCalc(t, svc, CalcInput{
		Currency:   "USD",
		CouponCode: "SAVE10",
		Lines:      []Line{orderLine(1, 10000)},
	})
	if res.DiscountMinor != 0 || res.AppliedCoupon != nil || res.TotalMinor != 10000 {
		t.Fatalf("result = %+v, want undiscounted 10000", res)
	}
}

func TestOrderCalculateNoTaxRatesZeroTax(t *testing.T) {
	svc := orderSvcWith(orderLookupWith(orderPercent("SAVE10", 1000)))
	res := orderMustCalc(t, svc, CalcInput{
		Currency:     "USD",
		CouponCode:   "SAVE10",
		Lines:        []Line{orderLine(2, 2500)},
		TaxInclusive: true, // inclusive with no rates is still no tax
	})
	if res.TaxMinor != 0 {
		t.Fatalf("tax = %d, want 0", res.TaxMinor)
	}
	if res.SubtotalMinor != 5000 || res.DiscountMinor != 500 || res.TotalMinor != 4500 {
		t.Fatalf("totals = %+v, want 5000/500/0/4500", res)
	}
	orderCheckSum(t, res, true)
}

func TestOrderCalculateInclusiveTax(t *testing.T) {
	svc := orderSvcWith(orderLookupWith(orderPercent("SAVE10", 1000)))
	res := orderMustCalc(t, svc, CalcInput{
		Currency:     "USD",
		CouponCode:   "SAVE10",
		Lines:        []Line{orderLine(1, 10000)},
		TaxRates:     []tax.Rate{{BasisPoints: 1000, Jurisdiction: "VAT-VN"}},
		TaxInclusive: true,
	})
	// Prices include tax: 10% off leaves a gross of 9000, the customer
	// pays 9000, and round(9000*1000/11000) = 818 of it is tax.
	if res.SubtotalMinor != 10000 || res.DiscountMinor != 1000 ||
		res.TaxMinor != 818 || res.TotalMinor != 9000 {
		t.Fatalf("totals = %+v, want 10000/1000/818/9000", res)
	}
	lr := res.LineResults[0]
	if lr.LineTaxMinor != 818 || lr.LineTotalMinor != 9000 {
		t.Errorf("line = %+v, want tax 818 and total 9000", lr)
	}
	// Inclusive identity: the tax lives inside the total.
	if res.SubtotalMinor-res.DiscountMinor != res.TotalMinor {
		t.Errorf("subtotal-discount = %d, want inclusive total %d",
			res.SubtotalMinor-res.DiscountMinor, res.TotalMinor)
	}
	if res.TotalMinor-res.TaxMinor != 8182 {
		t.Errorf("net of tax = %d, want 8182", res.TotalMinor-res.TaxMinor)
	}
	orderCheckSum(t, res, true)
}

func TestOrderCalculateAllocationSumsExactly(t *testing.T) {
	// Awkward numbers chosen so neither the 33% discount nor the 7%
	// tax divides evenly across the lines: whatever the engines do
	// with the fractions, nothing may be lost or invented.
	svc := orderSvcWith(orderLookupWith(orderPercent("THIRTYTHREE", 3300)))
	res := orderMustCalc(t, svc, CalcInput{
		Currency:   "USD",
		CouponCode: "THIRTYTHREE",
		Lines:      []Line{orderLine(3, 333), orderLine(7, 7), orderLine(1, 1234)},
		TaxRates:   []tax.Rate{{BasisPoints: 700, Jurisdiction: "US-CA"}},
	})
	// subtotal 999+49+1234 = 2282, discount = round(2282*33%) = 753
	// allocated 330/16/407, bases 669/33/827, tax 47/2/58,
	// totals 716/35/885.
	wantSub := []int64{999, 49, 1234}
	wantDisc := []int64{330, 16, 407}
	wantTax := []int64{47, 2, 58}
	wantTot := []int64{716, 35, 885}
	for i := range wantSub {
		lr := res.LineResults[i]
		if lr.LineSubtotalMinor != wantSub[i] || lr.LineDiscountMinor != wantDisc[i] ||
			lr.LineTaxMinor != wantTax[i] || lr.LineTotalMinor != wantTot[i] {
			t.Errorf("line %d = %+v, want %d/%d/%d/%d", i, lr,
				wantSub[i], wantDisc[i], wantTax[i], wantTot[i])
		}
	}
	if res.SubtotalMinor != 2282 || res.DiscountMinor != 753 ||
		res.TaxMinor != 107 || res.TotalMinor != 1636 {
		t.Fatalf("totals = %+v, want 2282/753/107/1636", res)
	}
	if res.SubtotalMinor-res.DiscountMinor+res.TaxMinor != res.TotalMinor {
		t.Errorf("subtotal-discount+tax != total: %+v", res)
	}
	orderCheckSum(t, res, false)
}

func TestOrderCalculateTotalsInvariant(t *testing.T) {
	rates := []tax.Rate{{BasisPoints: 625, Jurisdiction: "US-CA"}, {BasisPoints: 255, Jurisdiction: "US-CA-SF"}}
	scenarios := []struct {
		name   string
		in     CalcInput
		lookup *orderFakeLookup
	}{
		{
			name:   "no coupon no tax",
			in:     CalcInput{Currency: "USD", Lines: []Line{orderLine(2, 1000), orderLine(1, 500)}},
			lookup: orderSvcNil(),
		},
		{
			name: "percent coupon exclusive multi-jurisdiction tax",
			in: CalcInput{Currency: "USD", CouponCode: "SAVE15",
				Lines:    []Line{orderLine(3, 333), orderLine(1, 1234), orderLine(2, 7)},
				TaxRates: rates},
			lookup: orderLookupWith(orderPercent("SAVE15", 1500)),
		},
		{
			name: "fixed coupon inclusive tax",
			in: CalcInput{Currency: "USD", CouponCode: "TAKE50",
				Lines:    []Line{orderLine(1, 10000), orderLine(2, 499)},
				TaxRates: []tax.Rate{{BasisPoints: 880, Jurisdiction: "VAT-VN"}}, TaxInclusive: true},
			lookup: orderLookupWith(orderFixed("TAKE50", 50, "USD")),
		},
		{
			name: "capped fixed coupon on a small order",
			in: CalcInput{Currency: "USD", CouponCode: "BIG",
				Lines: []Line{orderLine(1, 30)}, TaxRates: rates, TaxInclusive: true},
			lookup: orderLookupWith(orderFixed("BIG", 500, "USD")),
		},
	}
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			svc := orderSvcWith(sc.lookup)
			res := orderMustCalc(t, svc, sc.in)
			orderCheckSum(t, res, sc.in.TaxInclusive)
			if sc.in.TaxInclusive {
				if res.SubtotalMinor-res.DiscountMinor != res.TotalMinor {
					t.Errorf("inclusive: subtotal-discount = %d, want total %d",
						res.SubtotalMinor-res.DiscountMinor, res.TotalMinor)
				}
			} else {
				if res.SubtotalMinor-res.DiscountMinor+res.TaxMinor != res.TotalMinor {
					t.Errorf("exclusive: subtotal-discount+tax = %d, want total %d",
						res.SubtotalMinor-res.DiscountMinor+res.TaxMinor, res.TotalMinor)
				}
			}
			if res.DiscountMinor > res.SubtotalMinor {
				t.Errorf("discount %d exceeds subtotal %d", res.DiscountMinor, res.SubtotalMinor)
			}
		})
	}
}

// orderSvcNil names the nil-lookup case inside the invariant table.
func orderSvcNil() *orderFakeLookup { return &orderFakeLookup{coupons: map[string]coupon.Coupon{}} }

func TestOrderCalculateValidationErrors(t *testing.T) {
	svc := orderSvcWith(nil)
	cases := []struct {
		name string
		in   CalcInput
	}{
		{"no lines", CalcInput{Currency: "USD"}},
		{"zero quantity", CalcInput{Currency: "USD", Lines: []Line{orderLine(0, 100)}}},
		{"negative quantity", CalcInput{Currency: "USD", Lines: []Line{orderLine(-1, 100)}}},
		{"negative unit amount", CalcInput{Currency: "USD", Lines: []Line{{
			Quantity: 1, UnitAmountMinor: -1}}}},
		{"unknown currency", CalcInput{Currency: "XYZ", Lines: []Line{orderLine(1, 100)}}},
		{"empty currency", CalcInput{Currency: "", Lines: []Line{orderLine(1, 100)}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := svc.Calculate(context.Background(), tc.in)
			if err == nil {
				t.Fatalf("Calculate() error = nil, want a refusal")
			}
			if res == nil || res.Err != err {
				t.Errorf("result.Err = %v, want the returned error %v", res, err)
			}
			var ae *apperr.AppError
			if !errors.As(err, &ae) {
				t.Errorf("error %v is not an AppError", err)
			}
		})
	}
}

func TestOrderCalculateCouponLookupError(t *testing.T) {
	t.Run("unknown code", func(t *testing.T) {
		svc := orderSvcWith(orderLookupWith())
		_, err := svc.Calculate(context.Background(), CalcInput{
			Currency: "USD", CouponCode: "NOPE", Lines: []Line{orderLine(1, 100)},
		})
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != "COUPON_NOT_FOUND" || ae.Status != 404 {
			t.Fatalf("error = %v, want 404 COUPON_NOT_FOUND", err)
		}
	})
	t.Run("lookup failure", func(t *testing.T) {
		svc := orderSvcWith(&orderFakeLookup{err: errors.New("coupons table is down")})
		_, err := svc.Calculate(context.Background(), CalcInput{
			Currency: "USD", CouponCode: "SAVE10", Lines: []Line{orderLine(1, 100)},
		})
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != "COUPON_INVALID" {
			t.Fatalf("error = %v, want COUPON_INVALID", err)
		}
	})
	t.Run("invalid coupon", func(t *testing.T) {
		// Inactive coupons never apply.
		svc := orderSvcWith(orderLookupWith(coupon.Coupon{
			Code: "DEAD", Type: coupon.TypePercentOff, Value: 1000, Active: false,
		}))
		_, err := svc.Calculate(context.Background(), CalcInput{
			Currency: "USD", CouponCode: "DEAD", Lines: []Line{orderLine(1, 100)},
		})
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != "COUPON_INVALID" {
			t.Fatalf("error = %v, want COUPON_INVALID", err)
		}
	})
}

func TestOrderCalculatePreResolvedCoupon(t *testing.T) {
	// The checkout may hand over a coupon it already resolved; no
	// lookup is consulted.
	svc := orderSvcWith(nil)
	cp := orderFixed("TAKE50", 50, "USD")
	res := orderMustCalc(t, svc, CalcInput{
		Currency: "USD", Coupon: &cp, Lines: []Line{orderLine(1, 200)},
	})
	if res.DiscountMinor != 50 || res.TotalMinor != 150 {
		t.Fatalf("result = %+v, want discount 50 and total 150", res)
	}
	if res.AppliedCoupon == nil || res.AppliedCoupon.Code != "TAKE50" {
		t.Fatalf("AppliedCoupon = %+v, want TAKE50", res.AppliedCoupon)
	}
	orderCheckSum(t, res, false)
}

func TestOrderCalculateFreeShippingCoupon(t *testing.T) {
	// Free shipping changes no money: discount 0, but the coupon is
	// recorded as applied.
	svc := orderSvcWith(orderLookupWith(coupon.Coupon{
		Code: "SHIP", Type: coupon.TypeFreeShipping, Active: true,
	}))
	res := orderMustCalc(t, svc, CalcInput{
		Currency: "USD", CouponCode: "SHIP", Lines: []Line{orderLine(1, 1200)},
	})
	if res.DiscountMinor != 0 || res.TotalMinor != 1200 {
		t.Fatalf("result = %+v, want discount 0 and total 1200", res)
	}
	if res.AppliedCoupon == nil || res.AppliedCoupon.Code != "SHIP" {
		t.Fatalf("AppliedCoupon = %+v, want SHIP", res.AppliedCoupon)
	}
	orderCheckSum(t, res, false)
}

func TestOrderCalculateMultiJurisdictionTax(t *testing.T) {
	// Stacked jurisdictions are additive: 625 + 255 = 880 bps.
	svc := orderSvcWith(nil)
	res := orderMustCalc(t, svc, CalcInput{
		Currency: "USD",
		Lines:    []Line{orderLine(1, 10000)},
		TaxRates: []tax.Rate{{BasisPoints: 625, Jurisdiction: "US-CA"}, {BasisPoints: 255, Jurisdiction: "US-CA-SF"}},
	})
	if res.TaxMinor != 880 || res.TotalMinor != 10880 {
		t.Fatalf("totals = %+v, want tax 880 and total 10880", res)
	}
	if res.SubtotalMinor-res.DiscountMinor+res.TaxMinor != res.TotalMinor {
		t.Errorf("subtotal-discount+tax != total: %+v", res)
	}
	orderCheckSum(t, res, false)
}
