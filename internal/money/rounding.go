package money

import (
	"fmt"
	"math/big"
	"strconv"
)

// RoundMode selects how the remainder of a division between two minor-unit
// values is resolved. Rounding is applied to the magnitude of the exact result,
// so results are symmetric around zero: RoundUp always moves away from zero and
// RoundDown always truncates toward zero. The zero value is RoundHalfUp.
type RoundMode int

// Supported rounding modes.
const (
	// RoundHalfUp rounds to the nearest neighbour, breaking ties away from
	// zero (the common "round half up").
	RoundHalfUp RoundMode = iota
	// RoundHalfEven (banker's rounding) rounds to the nearest neighbour,
	// breaking ties toward the even neighbour of the exact quotient.
	RoundHalfEven
	// RoundHalfDown rounds to the nearest neighbour, breaking ties toward zero.
	RoundHalfDown
	// RoundUp always rounds away from zero.
	RoundUp
	// RoundDown always truncates toward zero.
	RoundDown
)

// roundModeNames holds the names of the rounding modes.
var roundModeNames = [...]string{"HalfUp", "HalfEven", "HalfDown", "Up", "Down"}

// String returns the name of the rounding mode, e.g. "HalfUp".
func (m RoundMode) String() string {
	if m < 0 || int(m) >= len(roundModeNames) {
		return "RoundMode(" + strconv.Itoa(int(m)) + ")"
	}
	return roundModeNames[m]
}

// ApplyBps returns a scaled by bps basis points (10000 basis points = 100%),
// i.e. a.Minor()*bps/10000, resolved with the given rounding mode. Negative
// basis points (discounts) and negative amounts are allowed and the result
// keeps the currency of a. The product is computed with arbitrary precision
// integers so the intermediate result can never overflow; only a final result
// that does not fit in int64 minor units returns an error wrapping ErrOverflow.
func (a Amount) ApplyBps(bps int64, mode RoundMode) (Amount, error) {
	prod := new(big.Int).Mul(big.NewInt(a.minor), big.NewInt(bps))
	q := roundQuoRem(prod, big.NewInt(10000), mode)
	if !q.IsInt64() {
		return Amount{}, fmt.Errorf("money: apply %d bps to %s: %w", bps, a.Format(), ErrOverflow)
	}
	return Amount{minor: q.Int64(), cur: a.cur}, nil
}

// MulRate is an alias for ApplyBps: it multiplies a by a rate expressed in
// basis points (10000 = 100%) with the given rounding mode.
func (a Amount) MulRate(bps int64, mode RoundMode) (Amount, error) {
	return a.ApplyBps(bps, mode)
}

// PercentOf returns bps basis points of a, i.e. a.Minor()*bps/10000, rounded
// half up (the default for derived prices). For example USD 100.00
// .PercentOf(2500) is USD 25.00. Use ApplyBps to pick a different rounding
// mode.
func (a Amount) PercentOf(bps int64) (Amount, error) {
	return a.ApplyBps(bps, RoundHalfUp)
}

// Round rounds a to whole major units with the given rounding mode: USD 12.50
// rounded RoundHalfUp is USD 13.00 and rounded RoundHalfEven is USD 12.00. For
// a 0-exponent currency (JPY, VND) it is a no-op. Use RoundTo to round to a
// different increment.
func (a Amount) Round(mode RoundMode) Amount {
	v, err := a.RoundTo(int64(pow10(a.cur.exponent)), mode)
	if err != nil {
		return a // unreachable: pow10 is always >= 1
	}
	return v
}

// RoundTo rounds a to the nearest multiple of increment minor units with the
// given rounding mode, e.g. cash rounding of VND 12500 to a 1000-dong increment
// is VND 13000 with RoundHalfUp and VND 12000 with RoundHalfEven. It returns an
// error wrapping ErrDivideByZero for a zero increment and ErrNegativeNotAllowed
// for a negative increment.
func (a Amount) RoundTo(increment int64, mode RoundMode) (Amount, error) {
	if increment == 0 {
		return Amount{}, fmt.Errorf("money: round %s: %w", a.Format(), ErrDivideByZero)
	}
	if increment < 0 {
		return Amount{}, fmt.Errorf("money: round %s to increment %d: %w", a.Format(), increment, ErrNegativeNotAllowed)
	}
	// roundQuoRem yields the rounded multiple count; scale it back to minor
	// units by the increment so the result is a multiple of increment.
	q := roundQuoRem(big.NewInt(a.minor), big.NewInt(increment), mode)
	q.Mul(q, big.NewInt(increment))
	if !q.IsInt64() {
		return Amount{}, fmt.Errorf("money: round %s to increment %d: %w", a.Format(), increment, ErrOverflow)
	}
	return Amount{minor: q.Int64(), cur: a.cur}, nil
}

// roundQuoRem divides num by den (which must be non-zero) truncating toward
// zero, then resolves the remainder according to mode. The rounding decision is
// made on the magnitude of the quotient so the result is symmetric around zero.
func roundQuoRem(num, den *big.Int, mode RoundMode) *big.Int {
	q, r := new(big.Int).QuoRem(num, den, new(big.Int))
	if r.Sign() == 0 {
		return q
	}
	absQ := new(big.Int).Abs(q)
	absR := new(big.Int).Abs(r)
	// cmp compares 2*|r| against den: >0 means more than half, ==0 an exact
	// tie. Computing it this way avoids any overflow.
	cmp := new(big.Int).Lsh(absR, 1).Cmp(den)
	inc := false
	switch mode {
	case RoundDown:
		inc = false
	case RoundUp:
		inc = true
	case RoundHalfDown:
		inc = cmp > 0
	case RoundHalfEven:
		inc = cmp > 0 || (cmp == 0 && absQ.Bit(0) == 1)
	default: // RoundHalfUp, and any unknown mode
		inc = cmp >= 0
	}
	if inc {
		absQ.Add(absQ, big.NewInt(1))
	}
	if num.Sign() < 0 {
		absQ.Neg(absQ)
	}
	return absQ
}
