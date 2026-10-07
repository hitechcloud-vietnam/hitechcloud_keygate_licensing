// Package tax implements pure tax calculation for the license & commerce
// platform. It is intentionally self-contained: standard library only, no
// database, no HTTP, and no other repository packages, so it compiles and
// tests standalone.
//
// Money is represented as int64 minor units (for example cents). The package
// never uses floating-point arithmetic: every computation is exact integer
// arithmetic with explicit overflow detection (ErrOverflow).
//
// # Rates
//
// A [Rate] is an integer number of basis points (10_000 bps = 100%) plus a
// jurisdiction label such as "US-CA" or "VAT-VN". A zero rate is valid and
// simply produces zero tax. Negative rates are rejected with [ErrInvalidRate].
//
// # Pricing modes
//
// Exclusive (tax added on top of a net amount):
//
//	tax   = round(net  * bps / 10_000)
//	gross = net + tax
//
// Inclusive (tax already contained in the gross price):
//
//	tax = round(gross * bps / (10_000 + bps))
//	net = gross - tax
//
// The invariant Net + Tax == Gross holds exactly in both modes: the non-rounded
// component (net for exclusive, gross for inclusive) is carried through
// unchanged and the remaining values are derived from it, so no rounding can
// break the identity.
//
// # Rounding
//
// Only the tax amount is rounded; see [Rounding] for the supported modes.
// Negative amounts are rejected ([ErrNegativeAmount]), so tie-breaking is
// unambiguous:
//
//   - [RoundingHalfUp]: ties (exactly .5) round up (away from zero).
//   - [RoundingHalfEven]: ties round to the even neighbor (banker's rounding).
//   - [RoundingDown]: truncation toward zero (floor for non-negative values).
//
// # Multiple jurisdictions
//
// [CalculateMulti] combines stacked jurisdictions (e.g. state + city)
// additively: the effective rate is the arithmetic SUM of the individual basis
// points and a single combined tax is computed with the requested rounding
// mode. (Compound taxation, where each jurisdiction taxes the previous gross,
// is deliberately not implemented.) The combined tax is then split across
// jurisdictions proportionally to their rates with the largest-remainder
// method, so the per-jurisdiction components always sum to the combined tax
// exactly — never losing or inventing pennies.
//
// # Multiple line items
//
// [CalculateLines] rounds every line independently and sums the rounded line
// results into the order totals. Order totals are therefore always the exact
// sum of their lines and reconcile with per-line invoices; rounding once on the
// summed base could differ by several minor units and lose pennies.
package tax

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strconv"
)

// BasisPointsDenominator is the scaling factor for rates: 10_000 bps = 100%.
const BasisPointsDenominator int64 = 10_000

// Sentinel errors returned by this package. All errors produced here wrap one
// of these with %w so callers can test with errors.Is.
var (
	// ErrOverflow indicates that an intermediate or final integer result does
	// not fit in int64 minor units. No partial result is returned alongside it.
	ErrOverflow = errors.New("tax: integer overflow")

	// ErrNegativeAmount indicates a negative amount, quantity, unit amount or
	// allocation weight. Money in this package is never negative.
	ErrNegativeAmount = errors.New("tax: negative amount")

	// ErrInvalidRate indicates a rate with negative basis points.
	ErrInvalidRate = errors.New("tax: invalid rate")

	// ErrDivideByZero indicates a division by a zero denominator, e.g.
	// [Allocate] of a non-zero total across zero total weight.
	ErrDivideByZero = errors.New("tax: division by zero")

	// ErrInvalidRounding indicates an unknown [Rounding] mode value.
	ErrInvalidRounding = errors.New("tax: invalid rounding mode")
)

// Rate is a tax rate for a single jurisdiction.
type Rate struct {
	// BasisPoints is the rate in basis points: 10_000 = 100%, 0 = zero-rated.
	// Only whole basis points are representable; a fractional rate such as
	// 8.875% (= 887.5 bps) must be expressed as a whole number of basis points
	// (e.g. 887 or 888) by the caller.
	BasisPoints int64

	// Jurisdiction labels the taxing authority, e.g. "US-CA" or "VAT-VN".
	// It is carried through for reporting and is not interpreted.
	Jurisdiction string
}

// Validate reports whether the rate is usable. It returns nil for any rate
// with BasisPoints >= 0, and a wrapped [ErrInvalidRate] otherwise.
func Validate(rate Rate) error {
	if rate.BasisPoints < 0 {
		return fmt.Errorf("%w: %s basis points %d must be >= 0",
			ErrInvalidRate, rate.Jurisdiction, rate.BasisPoints)
	}
	return nil
}

// Validate is the method form of [Validate].
func (r Rate) Validate() error { return Validate(r) }

// Rounding selects how the fractional part of the tax amount is resolved.
// Only the tax amount is rounded; net and gross follow from the identity
// Net + Tax == Gross.
type Rounding int

const (
	// RoundingHalfUp rounds ties (an exact fraction of one half) up, i.e. away
	// from zero. Since amounts are non-negative this is ordinary "round half up".
	RoundingHalfUp Rounding = iota

	// RoundingHalfEven rounds ties to the even neighbor (banker's rounding,
	// IEEE 754 default), which avoids systematic upward bias over many lines.
	RoundingHalfEven

	// RoundingDown truncates the fraction toward zero (floor for non-negative
	// values), i.e. the tax is never rounded up.
	RoundingDown
)

// String returns a human-readable name of the rounding mode.
func (m Rounding) String() string {
	switch m {
	case RoundingHalfUp:
		return "HalfUp"
	case RoundingHalfEven:
		return "HalfEven"
	case RoundingDown:
		return "Down"
	default:
		return "Rounding(" + strconv.FormatInt(int64(m), 10) + ")"
	}
}

// Breakdown is the result of a single tax calculation. The fields always
// satisfy Net + Tax == Gross exactly.
type Breakdown struct {
	// Net is the amount before tax (the tax-exclusive base).
	Net int64
	// Tax is the rounded tax amount.
	Tax int64
	// Gross is the amount after tax (net + tax).
	Gross int64
}

// Calculate computes the tax breakdown for one amount and one rate.
//
// amount is the NET amount when inclusive is false (tax is added on top:
// tax = round(amount * bps / 10_000), gross = amount + tax) and the GROSS
// amount when inclusive is true (tax is extracted from inside the price:
// tax = round(amount * bps / (10_000 + bps)), net = amount - tax).
//
// The rounding mode applies to the tax amount only; the invariant
// Net + Tax == Gross holds exactly for both modes.
//
// Errors (all wrapped with %w):
//   - [ErrInvalidRate] if rate.BasisPoints < 0,
//   - [ErrNegativeAmount] if amount < 0,
//   - [ErrInvalidRounding] if rounding is not a known mode,
//   - [ErrOverflow] if any intermediate product, quotient, or the gross sum
//     does not fit in int64.
//
// A zero rate or a zero amount yields a zero tax and net == gross.
func Calculate(amount int64, rate Rate, inclusive bool, rounding Rounding) (Breakdown, error) {
	if err := Validate(rate); err != nil {
		return Breakdown{}, err
	}
	if amount < 0 {
		return Breakdown{}, fmt.Errorf("%w: amount %d", ErrNegativeAmount, amount)
	}
	if err := validateRounding(rounding); err != nil {
		return Breakdown{}, err
	}
	if amount == 0 || rate.BasisPoints == 0 {
		// Zero rate: tax is zero and net equals gross. Zero amount: all zero.
		return Breakdown{Net: amount, Tax: 0, Gross: amount}, nil
	}

	if !inclusive {
		// Exclusive: tax = round(net * bps / 10_000), gross = net + tax.
		tax, err := mulDivRound(amount, rate.BasisPoints, BasisPointsDenominator, rounding)
		if err != nil {
			return Breakdown{}, err
		}
		gross, err := addInt64(amount, tax)
		if err != nil {
			return Breakdown{}, err
		}
		return Breakdown{Net: amount, Tax: tax, Gross: gross}, nil
	}

	// Inclusive: tax = round(gross * bps / (10_000 + bps)), net = gross - tax.
	den, ok := addInt64OK(BasisPointsDenominator, rate.BasisPoints)
	if !ok {
		return Breakdown{}, fmt.Errorf("%w: 10000 + %d", ErrOverflow, rate.BasisPoints)
	}
	tax, err := mulDivRound(amount, rate.BasisPoints, den, rounding)
	if err != nil {
		return Breakdown{}, err
	}
	// tax <= amount always holds: bps/(10000+bps) < 1 strictly, so the exact
	// quotient is below amount and rounding lifts it by at most one unit to
	// amount itself. Hence net = amount - tax is never negative.
	net := amount - tax
	return Breakdown{Net: net, Tax: tax, Gross: amount}, nil
}

// validateRounding reports whether m is a known rounding mode.
func validateRounding(m Rounding) error {
	switch m {
	case RoundingHalfUp, RoundingHalfEven, RoundingDown:
		return nil
	}
	return fmt.Errorf("%w: %d", ErrInvalidRounding, m)
}

// mulDivRound computes round(a * b / d) for non-negative a, b and positive d
// using 128-bit intermediate arithmetic, so large operands are handled
// exactly and overflow is reported rather than silently wrapping.
func mulDivRound(a, b, d int64, mode Rounding) (int64, error) {
	q, r, err := mulDivMod(a, b, d)
	if err != nil {
		return 0, err
	}
	return roundQuotient(q, r, d, mode)
}

// mulDivMod returns floor(a*b/d) and (a*b) mod d for non-negative a, b and
// positive d. The product is computed at 128-bit precision. ErrOverflow is
// returned when the quotient does not fit in int64; ErrDivideByZero when d==0.
func mulDivMod(a, b, d int64) (q, r int64, err error) {
	if d == 0 {
		return 0, 0, fmt.Errorf("%w: denominator is zero", ErrDivideByZero)
	}
	if a < 0 || b < 0 || d < 0 {
		return 0, 0, fmt.Errorf("%w: mulDivMod operand", ErrNegativeAmount)
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	// hi >= d implies quotient >= 2^64, which cannot fit in 64 bits.
	if hi >= uint64(d) {
		return 0, 0, fmt.Errorf("%w: %d * %d / %d exceeds int64", ErrOverflow, a, b, d)
	}
	qq, rr := bits.Div64(hi, lo, uint64(d))
	if qq > math.MaxInt64 {
		return 0, 0, fmt.Errorf("%w: quotient of %d * %d / %d exceeds int64", ErrOverflow, a, b, d)
	}
	return int64(qq), int64(rr), nil
}

// roundQuotient rounds the exact rational q + r/d (0 <= r < d, q >= 0, d > 0)
// to an int64 according to mode. Because all package amounts are
// non-negative, HalfUp means "ties away from zero" == "ties up", HalfEven is
// IEEE 754 banker's rounding, and Down truncates toward zero.
func roundQuotient(q, r, d int64, mode Rounding) (int64, error) {
	// Compare r against d-r instead of 2*r against d to avoid overflow:
	// 2*r >= d  <=>  r >= d-r  (d-r > 0 because r < d).
	var roundUp bool
	switch mode {
	case RoundingDown:
		roundUp = false
	case RoundingHalfUp:
		roundUp = r >= d-r
	case RoundingHalfEven:
		roundUp = r > d-r || (r == d-r && q%2 == 1)
	default:
		return 0, fmt.Errorf("%w: %d", ErrInvalidRounding, mode)
	}
	if !roundUp {
		return q, nil
	}
	if q == math.MaxInt64 {
		return 0, fmt.Errorf("%w: rounding up from %d", ErrOverflow, q)
	}
	return q + 1, nil
}

// addInt64 returns a+b or a wrapped [ErrOverflow] if the sum leaves int64.
func addInt64(a, b int64) (int64, error) {
	s, ok := addInt64OK(a, b)
	if !ok {
		return 0, fmt.Errorf("%w: %d + %d", ErrOverflow, a, b)
	}
	return s, nil
}

// addInt64OK returns a+b and whether the sum fits in int64.
func addInt64OK(a, b int64) (int64, bool) {
	s := a + b
	if (b > 0 && s < a) || (b < 0 && s > a) {
		return 0, false
	}
	return s, true
}

// mulInt64OK returns a*b and whether the product fits in int64.
func mulInt64OK(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	if hi != 0 || lo > math.MaxInt64 {
		return 0, false
	}
	return int64(lo), true
}
