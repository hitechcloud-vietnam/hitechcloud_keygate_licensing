package coupon

import "errors"

// Sentinel errors returned by this package. Validation and application
// functions wrap them with fmt.Errorf("%w") so callers can test membership
// with errors.Is while still getting descriptive messages (amounts, codes,
// limits) in the error text.
var (
	// ErrCouponNotFound is returned by [Find] when no coupon matches the
	// requested code.
	ErrCouponNotFound = errors.New("coupon not found")

	// ErrCouponInactive is returned when [Coupon.Active] is false.
	ErrCouponInactive = errors.New("coupon is inactive")

	// ErrCouponNotStarted is returned when now is before [Coupon.StartsAt].
	ErrCouponNotStarted = errors.New("coupon has not started yet")

	// ErrCouponExpired is returned when now is after [Coupon.EndsAt].
	ErrCouponExpired = errors.New("coupon has expired")

	// ErrMinimumOrderNotMet is returned when the order subtotal is below
	// [Coupon.MinimumOrderAmount]. Wrapped messages include both amounts.
	ErrMinimumOrderNotMet = errors.New("minimum order amount not met")

	// ErrMaxRedemptionsExceeded is returned when redeeming the coupon would
	// exceed [Coupon.MaxRedemptions] (checked against [Coupon.TimesRedeemed]).
	ErrMaxRedemptionsExceeded = errors.New("maximum redemptions exceeded")

	// ErrCustomerRedemptionLimit is returned when the customer has already
	// redeemed the coupon [Coupon.MaxRedemptionsPerCustomer] times.
	ErrCustomerRedemptionLimit = errors.New("per-customer redemption limit reached")

	// ErrCurrencyMismatch is returned when the coupon currency and the order
	// currency are incompatible (see the currency rules in the package
	// documentation), including a fixed-amount coupon with no currency.
	ErrCurrencyMismatch = errors.New("coupon currency does not match order currency")

	// ErrProductNotApplicable is returned when [Coupon.AppliesTo] is non-empty
	// and no order line matches any of its product/plan/SKU codes.
	ErrProductNotApplicable = errors.New("coupon does not apply to any product or plan in the order")

	// ErrCouponNotStackable is returned by [ApplyStack] when more than one
	// coupon is supplied and at least one of them is not [Coupon.Stackable].
	ErrCouponNotStackable = errors.New("coupon cannot be combined with other coupons")

	// ErrInvalidCouponType is returned when [Coupon.Type] is not a supported
	// [Type] constant.
	ErrInvalidCouponType = errors.New("unknown coupon type")

	// ErrInvalidCouponValue is returned when coupon fields are intrinsically
	// invalid (non-positive or out-of-range value, negative limits, etc).
	ErrInvalidCouponValue = errors.New("invalid coupon value")

	// ErrNoCoupons is returned by [ApplyStack] when the coupon slice is empty.
	ErrNoCoupons = errors.New("no coupons supplied")

	// ErrCannotAllocateDiscount is returned when a positive discount cannot be
	// allocated because the order has no positive-value line item.
	ErrCannotAllocateDiscount = errors.New("discount cannot be allocated to any line item")
)
