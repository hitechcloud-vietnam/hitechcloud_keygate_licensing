package money

import (
	"fmt"
	"math/big"
	"sort"
)

// Split divides a into n parts of equal size and returns them in order. It
// never loses or creates minor units: the returned parts always sum exactly to
// a. Any remainder is spread one minor unit at a time over the parts with the
// largest fractional remainder, tie-breaking on the lowest index, so
// FromMinor(100, USD).Split(3) yields 34, 33, 33 and -100 split three ways
// yields -34, -33, -33. Split returns an error wrapping ErrInvalidAmount if
// n <= 0.
func (a Amount) Split(n int) ([]Amount, error) {
	if n <= 0 {
		return nil, fmt.Errorf("money: split %s into %d parts: %w", a.Format(), n, ErrInvalidAmount)
	}
	mag := absUint(a.minor)
	neg := a.minor < 0
	base := mag / uint64(n)
	rem := mag % uint64(n)
	out := make([]Amount, n)
	for i := range out {
		part := base
		if uint64(i) < rem {
			part++
		}
		minor, ok := fromMag(part, neg)
		if !ok {
			return nil, fmt.Errorf("money: split %s into %d parts: %w", a.Format(), n, ErrOverflow)
		}
		out[i] = Amount{minor: minor, cur: a.cur}
	}
	return out, nil
}

// Allocate distributes a across the given non-negative integer weights using
// the largest-remainder method: every part receives the truncated share of
// |a|*w/total (with total the sum of the weights), and the leftover minor units
// are handed out one at a time to the parts with the largest fractional
// remainder, tie-breaking on the lowest index. The parts always sum exactly to
// a and keep its sign, e.g. FromMinor(101, USD).Allocate(2, 3, 5) yields
// 20, 30, 51. Allocate returns an error wrapping ErrInvalidAmount when no
// weights are given, ErrNegativeNotAllowed for a negative weight and
// ErrDivideByZero when all weights are zero.
func (a Amount) Allocate(weights ...int64) ([]Amount, error) {
	if len(weights) == 0 {
		return nil, fmt.Errorf("money: allocate %s without weights: %w", a.Format(), ErrInvalidAmount)
	}
	total := new(big.Int)
	for i, w := range weights {
		if w < 0 {
			return nil, fmt.Errorf("money: allocate %s: weight[%d]=%d: %w", a.Format(), i, w, ErrNegativeNotAllowed)
		}
		total.Add(total, big.NewInt(w))
	}
	if total.Sign() == 0 {
		return nil, fmt.Errorf("money: allocate %s over zero total weight: %w", a.Format(), ErrDivideByZero)
	}

	absAmt := new(big.Int).SetUint64(absUint(a.minor))
	exact := make([]*big.Int, len(weights))
	rems := make([]*big.Int, len(weights))
	used := new(big.Int)
	for i, w := range weights {
		prod := new(big.Int).Mul(absAmt, big.NewInt(w))
		q, r := new(big.Int).QuoRem(prod, total, new(big.Int))
		exact[i], rems[i] = q, r
		used.Add(used, q)
	}
	// 0 <= leftover < len(weights): the sum of the truncated shares falls short
	// of the amount by exactly this many minor units.
	leftover := new(big.Int).Sub(absAmt, used).Int64()

	order := make([]int, len(weights))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		if cmp := rems[order[i]].Cmp(rems[order[j]]); cmp != 0 {
			return cmp > 0
		}
		return order[i] < order[j]
	})
	for k := int64(0); k < leftover; k++ {
		exact[order[k]].Add(exact[order[k]], big.NewInt(1))
	}

	neg := a.minor < 0
	out := make([]Amount, len(weights))
	for i := range out {
		v := exact[i]
		if neg {
			v = new(big.Int).Neg(v)
		}
		if !v.IsInt64() {
			return nil, fmt.Errorf("money: allocate %s: %w", a.Format(), ErrOverflow)
		}
		out[i] = Amount{minor: v.Int64(), cur: a.cur}
	}
	return out, nil
}
