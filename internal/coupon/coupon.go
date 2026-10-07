package coupon

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"
)

// PercentBase is the number of basis points in 100%: a coupon value of
// PercentBase discounts the applicable amount in full.
const PercentBase int64 = 10000

// Type identifies how a [Coupon] reduces an order.
type Type string

const (
	// TypePercentOff discounts the applicable subtotal by [Coupon.Value]
	// basis points (PercentBase = 100%).
	TypePercentOff Type = "percent_off"

	// TypeFixedAmountOff subtracts [Coupon.Value] minor units of the
	// coupon's [Coupon.Currency] from the applicable subtotal.
	TypeFixedAmountOff Type = "fixed_amount_off"

	// TypeFreeShipping grants free shipping without changing the order
	// subtotal; it sets [Result.FreeShipping].
	TypeFreeShipping Type = "free_shipping"
)

// Coupon is a discount definition. Codes are matched case-insensitively via
// [NormalizeCode]; comparisons in this package never require exact case.
type Coupon struct {
	// Code is the human-facing coupon code (e.g. "SAVE10"). Matching is
	// case-insensitive and ignores surrounding whitespace.
	Code string

	// Type selects the discount semantics (percent, fixed amount, or free
	// shipping).
	Type Type

	// Value is the discount size: for TypePercentOff it is basis points where
	// PercentBase (10000) = 100%; for TypeFixedAmountOff it is minor units of
	// Currency. It is ignored for TypeFreeShipping.
	Value int64

	// Currency is the currency of Value. Required (non-empty and equal to the
	// order currency, case-insensitive) for TypeFixedAmountOff; optional for
	// other types, where empty means "any currency".
	Currency string

	// StartsAt is the beginning of the validity window (inclusive). The zero
	// time means "no start bound".
	StartsAt time.Time

	// EndsAt is the end of the validity window (inclusive). The zero time
	// means "no end bound".
	EndsAt time.Time

	// MaxRedemptions is the maximum total number of redemptions allowed;
	// 0 means unlimited. Checked against TimesRedeemed.
	MaxRedemptions int

	// TimesRedeemed is how many times the coupon has already been redeemed.
	// The caller maintains this counter; this package only reads it.
	TimesRedeemed int

	// MaxRedemptionsPerCustomer is the maximum number of redemptions allowed
	// per customer; 0 means unlimited. Checked against
	// [Input.CustomerRedemptions].
	MaxRedemptionsPerCustomer int

	// MinimumOrderAmount is the smallest order subtotal (minor units) the
	// coupon may be applied to; 0 means no minimum.
	MinimumOrderAmount int64

	// AppliesTo optionally restricts the coupon to lines whose ProductCode,
	// PlanCode or SKU matches one of these codes (case-insensitive). An empty
	// slice means the coupon applies to every line.
	AppliesTo []string

	// Stackable reports whether the coupon may be combined with other coupons
	// in [ApplyStack]. A non-stackable coupon must be applied alone.
	Stackable bool

	// Active is the master switch: an inactive coupon never validates.
	Active bool
}

// Line is one order line being discounted. Quantity is in items and UnitAmount
// is a per-item price in minor units; negative values are treated as zero.
type Line struct {
	// SKU is the stock-keeping unit code of the line item.
	SKU string

	// ProductCode is the product code of the line item.
	ProductCode string

	// PlanCode is the plan code of the line item (for subscription plans).
	PlanCode string

	// Quantity is the number of units on the line.
	Quantity int

	// UnitAmount is the per-unit price in minor units.
	UnitAmount int64
}

// Total returns the line total (Quantity * UnitAmount) in minor units.
// Non-positive quantities or amounts yield 0.
func (l Line) Total() int64 {
	if l.Quantity <= 0 || l.UnitAmount <= 0 {
		return 0
	}
	return int64(l.Quantity) * l.UnitAmount
}

// Input is the order being discounted.
type Input struct {
	// Lines are the order lines the discount can be allocated to. Subtotal
	// should normally equal the sum of the line totals.
	Lines []Line

	// Subtotal is the order subtotal in minor units, before discount. It is
	// authoritative for validation (minimum order) and for the discount cap.
	Subtotal int64

	// Currency is the ISO 4217 code of the order (compared case-insensitively
	// against [Coupon.Currency]).
	Currency string

	// CustomerID identifies the customer placing the order (used in error
	// messages and for per-customer limits).
	CustomerID string

	// CustomerRedemptions is the number of times this customer has already
	// redeemed the coupon being applied. For [ApplyStack] it is checked
	// against every coupon in the stack.
	CustomerRedemptions int
}

// LineAllocation is the share of the discount allocated to one input line.
// The Discount values over all lines always sum exactly to
// [Result.DiscountAmount].
type LineAllocation struct {
	// LineIndex is the index of the line in [Input.Lines].
	LineIndex int

	// SKU echoes the SKU of the allocated line for convenience.
	SKU string

	// LineTotal is the total of the allocated line before discount.
	LineTotal int64

	// Discount is this line's share of the discount, in minor units.
	Discount int64

	// LineTotalAfterDiscount is LineTotal minus Discount.
	LineTotalAfterDiscount int64
}

// AppliedCoupon records one coupon's contribution to a [Result]. In a stack
// whose total was capped, later coupons are reduced first, so the
// DiscountAmount values always sum exactly to [Result.DiscountAmount].
type AppliedCoupon struct {
	// Code is the normalized ([NormalizeCode]) coupon code.
	Code string

	// Type is the coupon type that produced the discount.
	Type Type

	// DiscountAmount is the coupon's contribution in minor units (0 for
	// TypeFreeShipping, or when the stack budget was exhausted).
	DiscountAmount int64

	// RateBps is the coupon's percent rate in basis points; 0 for non-percent
	// types.
	RateBps int64
}

// Result is the outcome of applying one or more coupons to an order.
type Result struct {
	// Applied lists the coupons that were applied, in application order.
	Applied []AppliedCoupon

	// DiscountAmount is the total discount in minor units, capped at the
	// order subtotal (never negative).
	DiscountAmount int64

	// NewSubtotal is the order subtotal after discount (Subtotal minus
	// DiscountAmount), never negative.
	NewSubtotal int64

	// TaxableSubtotal is the base amount for tax computation. Discounts are
	// applied before tax, so it equals NewSubtotal.
	TaxableSubtotal int64

	// AppliedRateBps is the effective percent-off rate in basis points: the
	// coupon rate for a single percent coupon, or the sum of the percent
	// rates in a stack, capped at PercentBase. It is informational and 0 when
	// only fixed-amount or free-shipping coupons were applied.
	AppliedRateBps int64

	// FreeShipping reports whether a TypeFreeShipping coupon was applied.
	FreeShipping bool

	// LineAllocations holds one entry per input line (same order as
	// [Input.Lines]) with that line's share of DiscountAmount.
	LineAllocations []LineAllocation
}

// codeAlphabet is the unambiguous alphabet used by [GenerateCode]
// (no 0/O, 1/I/L, or other look-alike characters).
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"

// codeLength is the length of codes produced by [GenerateCode].
const codeLength = 12

// NormalizeCode returns code with surrounding whitespace removed and all
// letters upper-cased, so coupon code matching is case-insensitive.
func NormalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// MatchCode reports whether code identifies coupon c, comparing normalized
// codes case-insensitively. An empty code never matches.
func MatchCode(c Coupon, code string) bool {
	want := NormalizeCode(code)
	return want != "" && want == NormalizeCode(c.Code)
}

// Find returns the first coupon in coupons whose code matches code
// case-insensitively ([MatchCode]). If none matches it returns a zero coupon
// and an error wrapping [ErrCouponNotFound].
func Find(coupons []Coupon, code string) (Coupon, error) {
	for _, c := range coupons {
		if MatchCode(c, code) {
			return c, nil
		}
	}
	return Coupon{}, fmt.Errorf("coupon %q: %w", NormalizeCode(code), ErrCouponNotFound)
}

// GenerateCode returns a new random coupon code of 12 characters drawn from a
// 31-character unambiguous alphabet using a cryptographically secure source.
// Codes are not guaranteed unique; callers persisting codes should retry on
// collision.
func GenerateCode() (string, error) {
	return randomCode(rand.Reader, codeLength)
}

// randomCode builds a code of the given length from the unambiguous alphabet
// using r as its entropy source.
func randomCode(r io.Reader, length int) (string, error) {
	max := big.NewInt(int64(len(codeAlphabet)))
	out := make([]byte, length)
	for i := range out {
		n, err := rand.Int(r, max)
		if err != nil {
			return "", fmt.Errorf("coupon: generate code: %w", err)
		}
		out[i] = codeAlphabet[n.Int64()]
	}
	return string(out), nil
}
