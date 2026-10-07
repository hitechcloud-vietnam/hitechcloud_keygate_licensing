// Package coupon implements a self-contained coupon/discount engine for orders.
//
// It is pure business logic: no database, no HTTP, and no dependency outside
// the Go standard library. It is designed to be called by a later Order/Checkout
// engine that persists coupons and redemption counts.
//
// # Money and percentages
//
// All money values are int64 minor units (e.g. cents, VND xu). Floating point
// is never used. Percentages are int64 basis points where [PercentBase]
// (10000) equals 100%. A fixed-amount coupon stores its value directly in
// minor units and carries the [Coupon.Currency] it was issued in.
//
// # Validation order
//
// [Validate] checks a coupon in this order and returns the first failure
// wrapped (fmt.Errorf with %w) around a sentinel error:
//
//  1. intrinsic sanity ([ErrInvalidCouponType], [ErrInvalidCouponValue])
//  2. active flag ([ErrCouponInactive])
//  3. validity window ([ErrCouponNotStarted], [ErrCouponExpired])
//  4. global redemption limit ([ErrMaxRedemptionsExceeded])
//  5. per-customer redemption limit ([ErrCustomerRedemptionLimit])
//  6. currency ([ErrCurrencyMismatch])
//  7. minimum order amount ([ErrMinimumOrderNotMet], message contains both
//     the required and actual amounts)
//  8. product/plan applicability ([ErrProductNotApplicable])
//
// The validity window bounds are inclusive: a coupon is valid exactly at
// [Coupon.StartsAt] and exactly at [Coupon.EndsAt]; it is expired only after
// [Coupon.EndsAt].
//
// # Currency rules
//
// A fixed-amount coupon must carry a non-empty Coupon.Currency and it must
// equal the order currency (case-insensitive), otherwise [ErrCurrencyMismatch].
// Percent-off and free-shipping coupons may leave Coupon.Currency empty to
// apply in any currency; if set, it must match the order currency.
//
// # Rounding rules
//
// Percent discounts are computed in basis points on the "applicable subtotal":
// the sum of the totals of the order lines the coupon applies to (see
// Coupon.AppliesTo). The percent amount is rounded half up to whole minor
// units using integer arithmetic: (amount*bps + 5000) / 10000. A fractional
// amount of exactly one half rounds away from zero (e.g. 0.5 -> 1).
//
// # Discount caps
//
// A coupon never discounts more than the value of the lines it applies to and
// never more than the order subtotal: the resulting NewSubtotal is never
// negative. A fixed-amount coupon larger than the applicable amount simply
// discounts the whole applicable amount. Discounting is always applied before
// tax, so Result.TaxableSubtotal equals Result.NewSubtotal.
//
// # Per-line allocation (largest remainder)
//
// The discount is allocated across the eligible lines proportionally to their
// line totals using the largest-remainder (Hamilton) method:
//
//   - each line first receives floor(discount * lineTotal / eligibleTotal)
//   - the remaining minor units are handed out one per line, ordered by
//     descending fractional remainder, ties broken by ascending line index
//     (i.e. the original line order is preserved)
//
// This guarantees that the allocations sum exactly to the discount amount:
// no penny is ever lost or created. Lines the coupon does not apply to (and
// zero-value lines) receive a zero allocation. If the computed discount is
// positive but the order has no positive-value line to allocate to,
// [ErrCannotAllocateDiscount] is returned.
//
// # Stacking rules
//
// [ApplyStack] combines several coupons with these rules:
//
//   - If more than one coupon is supplied and any of them is not
//     [Coupon.Stackable], the call is rejected with [ErrCouponNotStackable].
//     There is no silent precedence: a non-stackable coupon must be submitted
//     alone (a single-coupon stack is always accepted, even if the coupon is
//     non-stackable).
//   - Each coupon is validated independently with [Validate]; the first
//     failure aborts the whole stack.
//   - Discounts stack additively: every coupon's discount is computed
//     independently against the original order amounts (a percent coupon on
//     its own applicable subtotal, a fixed coupon for its full value) and the
//     amounts are summed. Order does not change a coupon's individual
//     discount.
//   - The combined discount is capped at the order subtotal and at the total
//     value of the lines eligible for at least one coupon, so no line total is
//     ever netted below zero. When the cap binds, the budget is consumed in
//     the order the coupons were supplied: earlier coupons keep their full
//     discount and later ones are reduced (possibly to zero) first.
//   - The combined discount is allocated across all lines eligible for at
//     least one coupon using the same largest-remainder method.
//
// [Coupon.TimesRedeemed] and [Input.CustomerRedemptions] are supplied by the
// caller; this package performs no persistence. For a stack,
// Input.CustomerRedemptions is checked against every coupon's
// [Coupon.MaxRedemptionsPerCustomer].
//
// # Code matching
//
// Coupon codes are matched case-insensitively: [NormalizeCode] trims
// surrounding whitespace and upper-cases a code, and [MatchCode], [Find] and
// Coupon.AppliesTo matching all compare normalized codes. [GenerateCode]
// creates fresh random codes in a human-friendly unambiguous alphabet.
//
// The package is stateless and safe for concurrent use; all functions are
// pure with respect to their inputs.
package coupon
