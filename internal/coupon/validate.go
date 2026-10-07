package coupon

import (
	"fmt"
	"strings"
	"time"
)

// Validate reports whether coupon c may be redeemed against order in at time
// now. It checks, in order: intrinsic coupon sanity, the Active flag, the
// validity window (bounds inclusive), the global and per-customer redemption
// limits, currency compatibility, the minimum order amount, and product/plan
// applicability. The returned error wraps one of the package sentinel errors
// with %w and carries descriptive context (codes, amounts, limits). See the
// package documentation for the exact rules.
func Validate(c Coupon, in Input, now time.Time) error {
	if err := validateCoupon(c); err != nil {
		return err
	}
	if !c.Active {
		return fmt.Errorf("coupon %q: %w", c.Code, ErrCouponInactive)
	}
	if !c.StartsAt.IsZero() && now.Before(c.StartsAt) {
		return fmt.Errorf("coupon %q: not valid before %s: %w",
			c.Code, c.StartsAt.Format(time.RFC3339), ErrCouponNotStarted)
	}
	if !c.EndsAt.IsZero() && now.After(c.EndsAt) {
		return fmt.Errorf("coupon %q: expired at %s: %w",
			c.Code, c.EndsAt.Format(time.RFC3339), ErrCouponExpired)
	}
	if c.MaxRedemptions > 0 && c.TimesRedeemed >= c.MaxRedemptions {
		return fmt.Errorf("coupon %q: already redeemed %d times (max %d): %w",
			c.Code, c.TimesRedeemed, c.MaxRedemptions, ErrMaxRedemptionsExceeded)
	}
	if c.MaxRedemptionsPerCustomer > 0 && in.CustomerRedemptions >= c.MaxRedemptionsPerCustomer {
		return fmt.Errorf("coupon %q: customer %q already redeemed this coupon %d times (max %d): %w",
			c.Code, in.CustomerID, in.CustomerRedemptions, c.MaxRedemptionsPerCustomer, ErrCustomerRedemptionLimit)
	}
	if err := checkCurrency(c, in); err != nil {
		return err
	}
	if c.MinimumOrderAmount > 0 && in.Subtotal < c.MinimumOrderAmount {
		return fmt.Errorf("coupon %q: minimum order amount is %d minor units but order subtotal is %d minor units: %w",
			c.Code, c.MinimumOrderAmount, in.Subtotal, ErrMinimumOrderNotMet)
	}
	if len(c.AppliesTo) > 0 && !appliesToAnyLine(c.AppliesTo, in.Lines) {
		return fmt.Errorf("coupon %q: no order line matches product/plan codes %v: %w",
			c.Code, c.AppliesTo, ErrProductNotApplicable)
	}
	return nil
}

// validateCoupon checks the intrinsic sanity of the coupon fields, independent
// of any order: type, value range, and non-negative limits/counters.
func validateCoupon(c Coupon) error {
	switch c.Type {
	case TypePercentOff:
		if c.Value <= 0 || c.Value > PercentBase {
			return fmt.Errorf("coupon %q: percent-off value must be in [1, %d] basis points, got %d: %w",
				c.Code, PercentBase, c.Value, ErrInvalidCouponValue)
		}
	case TypeFixedAmountOff:
		if c.Value <= 0 {
			return fmt.Errorf("coupon %q: fixed-amount value must be positive minor units, got %d: %w",
				c.Code, c.Value, ErrInvalidCouponValue)
		}
	case TypeFreeShipping:
		// Value is unused for free shipping; nothing to range-check.
	default:
		return fmt.Errorf("coupon %q: unsupported coupon type %q: %w", c.Code, c.Type, ErrInvalidCouponType)
	}
	if c.MinimumOrderAmount < 0 {
		return fmt.Errorf("coupon %q: minimum order amount must not be negative, got %d: %w",
			c.Code, c.MinimumOrderAmount, ErrInvalidCouponValue)
	}
	if c.MaxRedemptions < 0 || c.MaxRedemptionsPerCustomer < 0 || c.TimesRedeemed < 0 {
		return fmt.Errorf("coupon %q: redemption counters must not be negative (max %d, per-customer max %d, redeemed %d): %w",
			c.Code, c.MaxRedemptions, c.MaxRedemptionsPerCustomer, c.TimesRedeemed, ErrInvalidCouponValue)
	}
	return nil
}

// checkCurrency enforces the currency rules: fixed-amount coupons must name a
// currency equal to the order currency; other coupon types only need to match
// when they name one. Comparisons are case-insensitive.
func checkCurrency(c Coupon, in Input) error {
	if c.Type == TypeFixedAmountOff && c.Currency == "" {
		return fmt.Errorf("coupon %q: fixed-amount coupon has no currency (order currency %q): %w",
			c.Code, in.Currency, ErrCurrencyMismatch)
	}
	if c.Currency != "" && !strings.EqualFold(c.Currency, in.Currency) {
		return fmt.Errorf("coupon %q: coupon currency %q does not match order currency %q: %w",
			c.Code, c.Currency, in.Currency, ErrCurrencyMismatch)
	}
	return nil
}

// appliesToAnyLine reports whether at least one line matches codes.
func appliesToAnyLine(codes []string, lines []Line) bool {
	for _, l := range lines {
		if matchesAppliesTo(codes, l) {
			return true
		}
	}
	return false
}

// matchesAppliesTo reports whether line l is eligible for the coupon whose
// AppliesTo list is codes. An empty list means every line is eligible;
// otherwise the normalized line ProductCode, PlanCode or SKU must match one of
// the normalized codes.
func matchesAppliesTo(codes []string, l Line) bool {
	if len(codes) == 0 {
		return true
	}
	for _, code := range codes {
		want := NormalizeCode(code)
		if want == "" {
			continue
		}
		if want == NormalizeCode(l.ProductCode) ||
			want == NormalizeCode(l.PlanCode) ||
			want == NormalizeCode(l.SKU) {
			return true
		}
	}
	return false
}
