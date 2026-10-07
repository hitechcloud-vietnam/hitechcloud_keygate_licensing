package tax

import (
	"fmt"
	"sort"
)

// TaxComponent is one jurisdiction's share of a combined tax, as reported by
// [CalculateMulti].
type TaxComponent struct {
	// Rate is the jurisdiction's rate (label included).
	Rate Rate
	// Tax is this jurisdiction's share of the combined tax, in minor units.
	// The shares always sum to exactly MultiBreakdown.Tax.
	Tax int64
}

// MultiBreakdown is the result of [CalculateMulti]. The fields satisfy
// Net + Tax == Gross exactly, and the component taxes sum to exactly Tax.
type MultiBreakdown struct {
	// Net is the amount before tax.
	Net int64
	// Tax is the combined (rounded) tax across all jurisdictions.
	Tax int64
	// Gross is the amount after tax (net + tax).
	Gross int64
	// Components holds one entry per input rate, in input order.
	Components []TaxComponent
}

// CalculateMulti computes tax for stacked jurisdictions, e.g. state + city.
//
// Rates are ADDITIVE: the effective rate is the arithmetic sum of the
// individual basis points, and one combined tax is computed from it with the
// requested rounding mode. Therefore
//
//	CalculateMulti(a, []Rate{{BasisPoints: 625}, {BasisPoints: 255}}, ...)
//
// is identical to
//
//	Calculate(a, Rate{BasisPoints: 880}, ...)
//
// for the Net/Tax/Gross fields. Compound taxation (each jurisdiction taxing
// the previous jurisdiction's gross) is deliberately NOT implemented.
//
// The combined tax is then split per jurisdiction proportionally to the
// basis points using the largest-remainder method (see [Allocate]), so the
// per-jurisdiction breakdown sums to the combined tax exactly.
//
// Errors are the same as for [Calculate], plus [ErrOverflow] if the summed
// rate itself overflows int64.
func CalculateMulti(amount int64, rates []Rate, inclusive bool, rounding Rounding) (MultiBreakdown, error) {
	totalBps := int64(0)
	for _, rate := range rates {
		if err := Validate(rate); err != nil {
			return MultiBreakdown{}, err
		}
		var ok bool
		totalBps, ok = addInt64OK(totalBps, rate.BasisPoints)
		if !ok {
			return MultiBreakdown{}, fmt.Errorf("%w: combined rate overflows int64", ErrOverflow)
		}
	}

	bd, err := Calculate(amount, Rate{BasisPoints: totalBps}, inclusive, rounding)
	if err != nil {
		return MultiBreakdown{}, err
	}

	weights := make([]int64, len(rates))
	for i, rate := range rates {
		weights[i] = rate.BasisPoints
	}
	shares, err := Allocate(bd.Tax, weights)
	if err != nil {
		return MultiBreakdown{}, err
	}

	components := make([]TaxComponent, len(rates))
	for i, rate := range rates {
		components[i] = TaxComponent{Rate: rate, Tax: shares[i]}
	}
	return MultiBreakdown{
		Net:        bd.Net,
		Tax:        bd.Tax,
		Gross:      bd.Gross,
		Components: components,
	}, nil
}

// Allocate distributes total (a non-negative amount, typically combined tax)
// across weights (non-negative, typically basis points) proportionally, using
// the largest-remainder method:
//
//  1. Each index first receives floor(total * w_i / Σw).
//  2. The still-unallocated remainder (at most len(weights)-1 minor units)
//     is handed out one unit at a time to the largest fractional remainders,
//     ties going to the lower index.
//
// The returned shares are non-negative and always sum to exactly total.
// total == 0 yields all-zero shares. A negative total or weight is rejected
// with [ErrNegativeAmount]; allocating a non-zero total across zero total
// weight is impossible and returns [ErrDivideByZero]. Intermediate products
// use 128-bit precision; results that do not fit in int64 return
// [ErrOverflow]. Note that this split is penny-exact by construction and is
// therefore independent of the [Rounding] mode; the rounding mode governs how
// the combined tax itself is computed (in [Calculate]/[CalculateMulti]).
func Allocate(total int64, weights []int64) ([]int64, error) {
	if total < 0 {
		return nil, fmt.Errorf("%w: total %d", ErrNegativeAmount, total)
	}
	sum := int64(0)
	for _, w := range weights {
		if w < 0 {
			return nil, fmt.Errorf("%w: weight %d", ErrNegativeAmount, w)
		}
		var ok bool
		sum, ok = addInt64OK(sum, w)
		if !ok {
			return nil, fmt.Errorf("%w: weight sum overflows int64", ErrOverflow)
		}
	}

	shares := make([]int64, len(weights))
	if total == 0 {
		return shares, nil
	}
	if sum == 0 {
		return nil, fmt.Errorf("%w: cannot allocate %d across zero total weight", ErrDivideByZero, total)
	}

	type frac struct {
		idx int
		rem int64
	}
	fracs := make([]frac, len(weights))
	allocated := int64(0)
	for i, w := range weights {
		q, r, err := mulDivMod(total, w, sum)
		if err != nil {
			return nil, err
		}
		shares[i] = q
		// Σ floor(total*w_i/sum) <= total, so this cannot overflow.
		allocated += q
		fracs[i] = frac{idx: i, rem: r}
	}

	// Stable sort keeps lower indices first on equal remainders (tie rule).
	sort.SliceStable(fracs, func(a, b int) bool { return fracs[a].rem > fracs[b].rem })
	leftover := total - allocated // 0 <= leftover < len(weights)
	for k := 0; k < int(leftover); k++ {
		shares[fracs[k].idx]++
	}
	return shares, nil
}

// Line is one order line item. Quantity and UnitAmount are non-negative int64
// values; UnitAmount is money in minor units, and the line's base amount is
// Quantity * UnitAmount (also minor units).
type Line struct {
	// Quantity is the number of units; 0 is allowed and yields a zero line.
	Quantity int64
	// UnitAmount is the per-unit amount in minor units (net or gross depending
	// on the pricing mode).
	UnitAmount int64
}

// LineBreakdown pairs a line with its tax breakdown.
type LineBreakdown struct {
	// Line is the input line item.
	Line Line
	// Breakdown is that line's independently rounded tax breakdown.
	Breakdown Breakdown
}

// OrderBreakdown is the result of [CalculateLines]. The totals always equal
// the exact sum of the per-line breakdowns.
type OrderBreakdown struct {
	// Net is the sum of the line nets.
	Net int64
	// Tax is the sum of the (individually rounded) line taxes.
	Tax int64
	// Gross is the sum of the line grosses (net + tax).
	Gross int64
	// Lines holds one entry per input line, in input order.
	Lines []LineBreakdown
}

// CalculateLines computes the tax breakdown for a list of order lines with
// stacked jurisdictions (rates are additive per [CalculateMulti] semantics and
// apply to every line).
//
// Every line is rounded INDEPENDENTLY — each line's tax is rounded per the
// rounding mode on that line's own base — and the rounded line results are
// then summed into the order totals. Consequently Net/Tax/Gross in the result
// equal the exact sums of the per-line fields: there are no lost pennies, and
// the order always reconciles with per-line invoices. Rounding once on the
// summed base could differ by several minor units; that policy is
// deliberately not used here.
//
// Errors: [ErrInvalidRate] for negative basis points, [ErrNegativeAmount] for
// negative quantity or unit amount, [ErrInvalidRounding] for an unknown mode,
// and [ErrOverflow] if a line base, an intermediate tax, or an order total
// overflows int64.
func CalculateLines(lines []Line, rates []Rate, inclusive bool, rounding Rounding) (OrderBreakdown, error) {
	if err := validateRounding(rounding); err != nil {
		return OrderBreakdown{}, err
	}
	totalBps := int64(0)
	for _, rate := range rates {
		if err := Validate(rate); err != nil {
			return OrderBreakdown{}, err
		}
		var ok bool
		totalBps, ok = addInt64OK(totalBps, rate.BasisPoints)
		if !ok {
			return OrderBreakdown{}, fmt.Errorf("%w: combined rate overflows int64", ErrOverflow)
		}
	}
	combined := Rate{BasisPoints: totalBps}

	out := OrderBreakdown{Lines: make([]LineBreakdown, 0, len(lines))}
	for _, line := range lines {
		if line.Quantity < 0 || line.UnitAmount < 0 {
			return OrderBreakdown{}, fmt.Errorf("%w: line %+v", ErrNegativeAmount, line)
		}
		base, ok := mulInt64OK(line.Quantity, line.UnitAmount)
		if !ok {
			return OrderBreakdown{}, fmt.Errorf("%w: %d * %d", ErrOverflow, line.Quantity, line.UnitAmount)
		}
		bd, err := Calculate(base, combined, inclusive, rounding)
		if err != nil {
			return OrderBreakdown{}, err
		}
		out.Lines = append(out.Lines, LineBreakdown{Line: line, Breakdown: bd})

		if out.Net, ok = addInt64OK(out.Net, bd.Net); !ok {
			return OrderBreakdown{}, fmt.Errorf("%w: order net total", ErrOverflow)
		}
		if out.Tax, ok = addInt64OK(out.Tax, bd.Tax); !ok {
			return OrderBreakdown{}, fmt.Errorf("%w: order tax total", ErrOverflow)
		}
		if out.Gross, ok = addInt64OK(out.Gross, bd.Gross); !ok {
			return OrderBreakdown{}, fmt.Errorf("%w: order gross total", ErrOverflow)
		}
	}
	return out, nil
}
