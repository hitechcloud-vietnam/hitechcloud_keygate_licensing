package handler

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/coupon"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// The coupon rows are the only thing standing between an operator's
// form and a definition the engine will later be asked to redeem, so
// the normalizer both paths share and the conversion into the engine's
// own value are worth testing on their own — none of it needs a
// database.

var couponStart = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
var couponEnd = time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)

// A coupon everything accepts, so each refusal below can name the one
// field it is about.
func validCoupon() *model.Coupon {
	return &model.Coupon{
		Code:      "SAVE10",
		Type:      model.CouponTypePercentOff,
		ValueBPS:  1000,
		Stackable: true,
		Active:    true,
	}
}

// What gets stored is the folded form: the engine matches codes
// case-insensitively and currencies the same way, but a code spelled
// three ways in three rows is three coupons to everyone but the
// engine.
func TestNormalizeCouponFoldsCodeAndCurrency(t *testing.T) {
	cpn := validCoupon()
	cpn.Code = "  save10  "
	cpn.Currency = " usd "
	if err := normalizeCoupon(cpn); err != nil {
		t.Fatalf("normalizeCoupon: %v", err)
	}
	if cpn.Code != "SAVE10" {
		t.Errorf("code stored as %q, want %q", cpn.Code, "SAVE10")
	}
	if cpn.Currency != "USD" {
		t.Errorf("currency stored as %q, want %q", cpn.Currency, "USD")
	}
}

// The edges of each range are the boundaries an operator will aim at:
// 100% is the whole order and is allowed, 100% + 1 basis point is not.
func TestNormalizeCouponAcceptsValidRanges(t *testing.T) {
	for _, tc := range []struct {
		name   string
		coupon model.Coupon
	}{
		{"percent at one basis point", model.Coupon{Code: "P1", Type: model.CouponTypePercentOff, ValueBPS: 1}},
		{"percent at one hundred percent", model.Coupon{Code: "P2", Type: model.CouponTypePercentOff, ValueBPS: 10000}},
		{"fixed at one minor unit", model.Coupon{Code: "F1", Type: model.CouponTypeFixedOff, ValueMinor: 1, Currency: "eur"}},
		{"no validity window", model.Coupon{Code: "P3", Type: model.CouponTypePercentOff, ValueBPS: 1000}},
		{"window in the future", model.Coupon{Code: "P4", Type: model.CouponTypePercentOff, ValueBPS: 1000, StartsAt: &couponStart, EndsAt: &couponEnd}},
		{"unlimited everything", model.Coupon{Code: "P5", Type: model.CouponTypePercentOff, ValueBPS: 1000, MaxRedemptions: 0, MaxRedemptionsPerCustomer: 0, MinimumOrderMinor: 0}},
	} {
		cpn := tc.coupon
		if err := normalizeCoupon(&cpn); err != nil {
			t.Errorf("normalizeCoupon(%s) = %v, want nil", tc.name, err)
		}
	}
}

// Every refusal a create and an update must agree on. The message is
// checked because it is the whole of what the admin sees: these are
// 400s written straight to the form.
func TestNormalizeCouponRejectsBadInput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		coupon  model.Coupon
		wantErr string
	}{
		{"code missing", model.Coupon{Code: "   ", Type: model.CouponTypePercentOff, ValueBPS: 1000}, "code is required"},
		{"unknown type", model.Coupon{Code: "X1", Type: "free_shipping"}, "type must be percent_off or fixed_off"},
		{"type spelled as the engine does", model.Coupon{Code: "X2", Type: "fixed_amount_off"}, "type must be percent_off or fixed_off"},
		{"percent without a value", model.Coupon{Code: "P1", Type: model.CouponTypePercentOff, ValueBPS: 0}, "value_bps must be between 1 and 10000"},
		{"percent over one hundred", model.Coupon{Code: "P2", Type: model.CouponTypePercentOff, ValueBPS: 10001}, "value_bps must be between 1 and 10000"},
		{"negative percent value", model.Coupon{Code: "P3", Type: model.CouponTypePercentOff, ValueBPS: -1}, "value_bps must not be negative"},
		{"fixed without a value", model.Coupon{Code: "F1", Type: model.CouponTypeFixedOff, ValueMinor: 0, Currency: "USD"}, "value_minor must be greater than 0"},
		{"negative fixed value", model.Coupon{Code: "F2", Type: model.CouponTypeFixedOff, ValueMinor: -50, Currency: "USD"}, "value_minor must not be negative"},
		{"fixed without a currency", model.Coupon{Code: "F3", Type: model.CouponTypeFixedOff, ValueMinor: 500}, "currency is required"},
		{"window ends before it starts", model.Coupon{Code: "P4", Type: model.CouponTypePercentOff, ValueBPS: 1000, StartsAt: &couponEnd, EndsAt: &couponStart}, "ends_at must be after starts_at"},
		{"window with equal bounds", model.Coupon{Code: "P5", Type: model.CouponTypePercentOff, ValueBPS: 1000, StartsAt: &couponStart, EndsAt: &couponStart}, "ends_at must be after starts_at"},
		{"negative minimum order", model.Coupon{Code: "P6", Type: model.CouponTypePercentOff, ValueBPS: 1000, MinimumOrderMinor: -1}, "minimum_order_minor must not be negative"},
		{"negative max redemptions", model.Coupon{Code: "P7", Type: model.CouponTypePercentOff, ValueBPS: 1000, MaxRedemptions: -1}, "max_redemptions must not be negative"},
		{"negative per-customer cap", model.Coupon{Code: "P8", Type: model.CouponTypePercentOff, ValueBPS: 1000, MaxRedemptionsPerCustomer: -1}, "max_redemptions_per_customer must not be negative"},
		{"negative redemption count", model.Coupon{Code: "P9", Type: model.CouponTypePercentOff, ValueBPS: 1000, TimesRedeemed: -1}, "times_redeemed must not be negative"},
	} {
		cpn := tc.coupon
		err := normalizeCoupon(&cpn)
		if err == nil {
			t.Errorf("normalizeCoupon(%s) = nil, want a refusal", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("normalizeCoupon(%s) = %q, want it to mention %q", tc.name, err.Error(), tc.wantErr)
		}
	}
}

// The row and the engine spell the fixed-amount type differently and
// keep the value in different fields; this mapping is the only place
// that translation happens.
func TestCouponToEngine(t *testing.T) {
	cpn := &model.Coupon{
		Code:                      "SAVE10",
		Type:                      model.CouponTypePercentOff,
		ValueBPS:                  1000,
		ValueMinor:                777, // ignored for percent_off
		Currency:                  "USD",
		StartsAt:                  &couponStart,
		EndsAt:                    &couponEnd,
		MaxRedemptions:            5,
		TimesRedeemed:             2,
		MaxRedemptionsPerCustomer: 1,
		MinimumOrderMinor:         5000,
		AppliesTo:                 "plan-a, plan-b,,PLAN-C",
		Stackable:                 true,
		Active:                    true,
	}
	got := cpn.ToEngine()
	if got.Code != "SAVE10" || got.Type != coupon.TypePercentOff || got.Value != 1000 {
		t.Errorf("percent mapping = (%q, %q, %d), want (%q, %q, %d)",
			got.Code, got.Type, got.Value, "SAVE10", coupon.TypePercentOff, 1000)
	}
	if !got.StartsAt.Equal(couponStart) || !got.EndsAt.Equal(couponEnd) {
		t.Errorf("window mapped to %v..%v, want %v..%v", got.StartsAt, got.EndsAt, couponStart, couponEnd)
	}
	if got.MaxRedemptions != 5 || got.TimesRedeemed != 2 || got.MaxRedemptionsPerCustomer != 1 {
		t.Errorf("limits mapped to (%d, %d, %d), want (5, 2, 1)",
			got.MaxRedemptions, got.TimesRedeemed, got.MaxRedemptionsPerCustomer)
	}
	if got.MinimumOrderAmount != 5000 {
		t.Errorf("minimum order mapped to %d, want 5000", got.MinimumOrderAmount)
	}
	// Split and folded, and the hole a trailing comma would leave is
	// not a code that matches nothing.
	wantCodes := []string{"PLAN-A", "PLAN-B", "PLAN-C"}
	if len(got.AppliesTo) != len(wantCodes) {
		t.Fatalf("applies_to mapped to %v, want %v", got.AppliesTo, wantCodes)
	}
	for i, code := range wantCodes {
		if got.AppliesTo[i] != code {
			t.Errorf("applies_to[%d] = %q, want %q", i, got.AppliesTo[i], code)
		}
	}

	fixed := &model.Coupon{
		Code:              "MINUS15",
		Type:              model.CouponTypeFixedOff,
		ValueMinor:        1500,
		ValueBPS:          2500, // ignored for fixed_off
		Currency:          "USD",
		Stackable:         true,
		Active:            true,
		MinimumOrderMinor: 0,
	}
	got = fixed.ToEngine()
	if got.Type != coupon.TypeFixedAmountOff || got.Value != 1500 {
		t.Errorf("fixed mapping = (%q, %d), want (%q, %d)",
			got.Type, got.Value, coupon.TypeFixedAmountOff, 1500)
	}
	if !got.StartsAt.IsZero() || !got.EndsAt.IsZero() {
		t.Errorf("nil bounds mapped to %v..%v, want zero times", got.StartsAt, got.EndsAt)
	}
	if len(got.AppliesTo) != 0 {
		t.Errorf("empty applies_to mapped to %v, want no codes", got.AppliesTo)
	}

	// A type neither constant names maps to the zero Type, which the
	// engine refuses — never to a wrong discount.
	bad := &model.Coupon{Code: "X", Type: "free_shipping", ValueBPS: 1000}
	got = bad.ToEngine()
	if got.Type != "" || got.Value != 0 {
		t.Errorf("unknown type mapped to (%q, %d), want the zero type", got.Type, got.Value)
	}
}

// The conversion exists so the engine can be fed a stored row; the
// proof is that the engine computes with it — and refuses it — as
// every rule says it should.
func TestCouponToEngineDrivesTheEngine(t *testing.T) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	in := coupon.Input{
		Lines:    []coupon.Line{{SKU: "SKU-1", Quantity: 1, UnitAmount: 10000}},
		Subtotal: 10000,
		Currency: "USD",
	}

	t.Run("percent off", func(t *testing.T) {
		cpn := validCoupon()
		cpn.Code = "SAVE25"
		cpn.ValueBPS = 2500
		res, err := coupon.Apply(cpn.ToEngine(), in, now)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if res.DiscountAmount != 2500 || res.NewSubtotal != 7500 {
			t.Errorf("discount = %d, new subtotal = %d, want 2500 / 7500",
				res.DiscountAmount, res.NewSubtotal)
		}
		if res.AppliedRateBps != 2500 {
			t.Errorf("applied rate = %d bps, want 2500", res.AppliedRateBps)
		}
	})

	t.Run("fixed amount off", func(t *testing.T) {
		cpn := &model.Coupon{
			Code: "MINUS15", Type: model.CouponTypeFixedOff,
			ValueMinor: 1500, Currency: "USD",
			Stackable: true, Active: true,
		}
		res, err := coupon.Apply(cpn.ToEngine(), in, now)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if res.DiscountAmount != 1500 || res.NewSubtotal != 8500 {
			t.Errorf("discount = %d, new subtotal = %d, want 1500 / 8500",
				res.DiscountAmount, res.NewSubtotal)
		}
	})

	t.Run("inactive coupon is refused", func(t *testing.T) {
		cpn := validCoupon()
		cpn.Active = false
		_, err := coupon.Apply(cpn.ToEngine(), in, now)
		if !errors.Is(err, coupon.ErrCouponInactive) {
			t.Errorf("Apply of an inactive coupon = %v, want ErrCouponInactive", err)
		}
	})

	t.Run("spent coupon is refused", func(t *testing.T) {
		cpn := validCoupon()
		cpn.MaxRedemptions = 3
		cpn.TimesRedeemed = 3
		_, err := coupon.Apply(cpn.ToEngine(), in, now)
		if !errors.Is(err, coupon.ErrMaxRedemptionsExceeded) {
			t.Errorf("Apply of a spent coupon = %v, want ErrMaxRedemptionsExceeded", err)
		}
	})
}
