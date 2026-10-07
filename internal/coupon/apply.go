package coupon

import (
	"fmt"
	"sort"
	"time"
)

// Apply computes the discount produced by a single coupon against order in at
// time now. It runs [Validate] first and returns its error unchanged. On
// success it returns a [Result] whose DiscountAmount is:
//
//   - TypePercentOff: round-half-up of the applicable subtotal (the sum of the
//     totals of the lines the coupon applies to) times Value basis points,
//     i.e. (subtotal*bps + 5000) / 10000 in integer arithmetic
//   - TypeFixedAmountOff: Value minor units
//   - TypeFreeShipping: 0 (with [Result.FreeShipping] set)
//
// The discount is capped at both the order subtotal and the value of the lines
// the coupon applies to, so NewSubtotal is never negative and no line total is
// netted below zero. It is allocated across the eligible lines with the
// largest-remainder method so the allocations sum exactly to DiscountAmount.
// If the discount is positive but no positive-value line exists,
// [ErrCannotAllocateDiscount] is returned.
func Apply(c Coupon, in Input, now time.Time) (Result, error) {
	if err := Validate(c, in, now); err != nil {
		return Result{}, err
	}
	weights := eligibleWeights(c.AppliesTo, in.Lines)
	base := weightTotal(weights)
	discount, rateBps, freeShip := couponDiscount(c, base)
	discount = min(discount, capFor(in, base))

	res, err := buildResult(in, discount, weights)
	if err != nil {
		return Result{}, err
	}
	res.Applied = []AppliedCoupon{{
		Code:           NormalizeCode(c.Code),
		Type:           c.Type,
		DiscountAmount: discount,
		RateBps:        rateBps,
	}}
	res.AppliedRateBps = rateBps
	res.FreeShipping = freeShip
	return res, nil
}

// ApplyStack computes the combined discount of several coupons against order
// in at time now. Rules (see the package documentation for the full
// discussion):
//
//   - An empty coupon slice returns [ErrNoCoupons].
//   - If more than one coupon is supplied and any is not [Coupon.Stackable],
//     the call returns [ErrCouponNotStackable]. A single-coupon stack is
//     always allowed. This check runs before validation.
//   - Every coupon is validated independently with [Validate]; the first
//     failure aborts the whole stack.
//   - Discounts are additive: each coupon's discount is computed against the
//     original order amounts, then the sum is capped at the order subtotal and
//     at the value of the lines eligible for at least one coupon. When the cap
//     binds, the budget is consumed in the order given: earlier coupons keep
//     their full discount and later ones are reduced first.
//   - The combined discount is allocated across all lines eligible for at
//     least one coupon with the largest-remainder method, so the allocations
//     sum exactly to DiscountAmount.
//
// On error a zero [Result] is returned.
func ApplyStack(coupons []Coupon, in Input, now time.Time) (Result, error) {
	if len(coupons) == 0 {
		return Result{}, fmt.Errorf("ApplyStack: %w", ErrNoCoupons)
	}
	if len(coupons) > 1 {
		for _, c := range coupons {
			if !c.Stackable {
				return Result{}, fmt.Errorf("coupon %q is not stackable and cannot be combined with %d other coupon(s): %w",
					c.Code, len(coupons)-1, ErrCouponNotStackable)
			}
		}
	}
	for _, c := range coupons {
		if err := Validate(c, in, now); err != nil {
			return Result{}, err
		}
	}

	// Weights cover lines eligible for at least one coupon in the stack.
	weights := make([]int64, len(in.Lines))
	for i, l := range in.Lines {
		for _, c := range coupons {
			if matchesAppliesTo(c.AppliesTo, l) {
				weights[i] = l.Total()
				break
			}
		}
	}
	budget := capFor(in, weightTotal(weights))

	var total, rateBps int64
	freeShip := false
	applied := make([]AppliedCoupon, 0, len(coupons))
	for _, c := range coupons {
		base := weightTotal(eligibleWeights(c.AppliesTo, in.Lines))
		discount, rate, free := couponDiscount(c, base)
		if free {
			freeShip = true
		}
		rateBps += rate
		take := min(discount, budget)
		budget -= take
		total += take
		applied = append(applied, AppliedCoupon{
			Code:           NormalizeCode(c.Code),
			Type:           c.Type,
			DiscountAmount: take,
			RateBps:        rate,
		})
	}
	if rateBps > PercentBase {
		rateBps = PercentBase
	}

	res, err := buildResult(in, total, weights)
	if err != nil {
		return Result{}, err
	}
	res.Applied = applied
	res.AppliedRateBps = rateBps
	res.FreeShipping = freeShip
	return res, nil
}

// couponDiscount computes a single coupon's uncapped discount from the value
// of the lines it applies to, plus its percent rate and free-shipping flag.
func couponDiscount(c Coupon, applicableSubtotal int64) (discount int64, rateBps int64, freeShipping bool) {
	switch c.Type {
	case TypePercentOff:
		return percentOf(applicableSubtotal, c.Value), c.Value, false
	case TypeFixedAmountOff:
		return c.Value, 0, false
	case TypeFreeShipping:
		return 0, 0, true
	default:
		// Unreachable: Validate rejects unknown types first.
		return 0, 0, false
	}
}

// percentOf returns amount * bps basis points rounded half up to whole minor
// units using integer arithmetic: (amount*bps + 5000) / 10000. Amounts are
// expected to stay within sane order magnitudes (below ~9e14 minor units) so
// the intermediate product cannot overflow int64.
func percentOf(amount, bps int64) int64 {
	if amount <= 0 || bps <= 0 {
		return 0
	}
	return (amount*bps + PercentBase/2) / PercentBase
}

// capFor returns the maximum discount allowed for an order: at most the order
// subtotal, and at most the value of the lines the coupon applies to when that
// value is positive and smaller. Negative subtotals are treated as 0.
func capFor(in Input, applicableSubtotal int64) int64 {
	cap := in.Subtotal
	if cap < 0 {
		cap = 0
	}
	if applicableSubtotal > 0 && applicableSubtotal < cap {
		cap = applicableSubtotal
	}
	return cap
}

// eligibleWeights returns one weight per input line (aligned with in.Lines):
// the line total for lines matching codes, 0 otherwise.
func eligibleWeights(codes []string, lines []Line) []int64 {
	weights := make([]int64, len(lines))
	for i, l := range lines {
		if matchesAppliesTo(codes, l) {
			weights[i] = l.Total()
		}
	}
	return weights
}

// weightTotal sums the positive weights.
func weightTotal(weights []int64) int64 {
	var total int64
	for _, w := range weights {
		if w > 0 {
			total += w
		}
	}
	return total
}

// buildResult allocates discount across the lines described by weights using
// the largest-remainder method and fills in the resulting totals.
func buildResult(in Input, discount int64, weights []int64) (Result, error) {
	allocs, err := allocate(discount, weights)
	if err != nil {
		return Result{}, err
	}
	res := Result{
		DiscountAmount:  discount,
		NewSubtotal:     in.Subtotal - discount,
		TaxableSubtotal: in.Subtotal - discount,
		LineAllocations: make([]LineAllocation, 0, len(in.Lines)),
	}
	for i, l := range in.Lines {
		total := l.Total()
		res.LineAllocations = append(res.LineAllocations, LineAllocation{
			LineIndex:              i,
			SKU:                    l.SKU,
			LineTotal:              total,
			Discount:               allocs[i],
			LineTotalAfterDiscount: total - allocs[i],
		})
	}
	return res, nil
}

// allocate distributes discount across lines proportionally to their weights
// using the largest-remainder (Hamilton) method: every line first gets
// floor(discount * weight / totalWeight), then the leftover minor units are
// distributed one per line by descending fractional remainder, ties broken by
// ascending index. The result sums exactly to discount (no lost pennies) and,
// because discount never exceeds the total weight, never exceeds a line's
// weight. Zero and negative weights always get 0. If discount is positive but
// no positive weight exists, [ErrCannotAllocateDiscount] is returned.
func allocate(discount int64, weights []int64) ([]int64, error) {
	out := make([]int64, len(weights))
	if discount <= 0 {
		return out, nil
	}
	total := weightTotal(weights)
	if total == 0 {
		return nil, fmt.Errorf("coupon: cannot allocate %d minor units of discount because the order has no positive-value line item: %w",
			discount, ErrCannotAllocateDiscount)
	}

	type remainder struct {
		idx int
		rem int64
	}
	rems := make([]remainder, 0, len(weights))
	var allocated int64
	for i, w := range weights {
		if w <= 0 {
			continue
		}
		base := discount * w / total
		out[i] = base
		allocated += base
		rems = append(rems, remainder{idx: i, rem: discount*w - base*total})
	}
	// Largest-remainder property: leftover < number of positive weights.
	leftover := discount - allocated
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].rem > rems[b].rem })
	for k := 0; leftover > 0 && k < len(rems); k++ {
		out[rems[k].idx]++
		leftover--
	}
	return out, nil
}
