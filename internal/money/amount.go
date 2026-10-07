package money

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Amount is a monetary value: an exact integer count of currency minor units
// (for example USD 19.99 is stored as the int64 1999) tagged with a Currency.
// Money is never stored as a floating point number; all arithmetic on Amount is
// exact integer arithmetic with overflow detection. The zero Amount is a valid
// zero value of the zero Currency.
type Amount struct {
	minor int64
	cur   Currency
}

// FromMinor returns the Amount of v minor units in currency c. For example
// FromMinor(1999, USD) is USD 19.99 and FromMinor(1999, VND) is VND 1999.
func FromMinor(v int64, c Currency) Amount {
	return Amount{minor: v, cur: c}
}

// Zero returns the zero Amount in currency c.
func Zero(c Currency) Amount {
	return Amount{cur: c}
}

// FromMajorString parses s as an amount expressed in major units, e.g. "12.34"
// for USD is 1234 minor units and "1234" for VND is 1234 minor units. Parsing
// uses integer arithmetic only. A leading sign is allowed, surrounding
// whitespace is ignored, and digits beyond the currency's minor-unit exponent
// are rounded half up (ties away from zero): "12.345" in USD is 1235 minor
// units, "1234.5" in VND is 1235 minor units, and "-0.005" in USD is -1 minor
// unit. It returns an error wrapping ErrInvalidAmount for anything that is not
// a plain decimal number and ErrOverflow when the value does not fit in int64
// minor units.
func FromMajorString(s string, c Currency) (Amount, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return Amount{}, fmt.Errorf("money: parse %q: %w", s, ErrInvalidAmount)
	}
	neg := false
	switch t[0] {
	case '-':
		neg = true
		t = t[1:]
	case '+':
		t = t[1:]
	}
	intPart, fracPart, hasFrac := strings.Cut(t, ".")
	if !allDigits(intPart) || (hasFrac && !allDigits(fracPart)) {
		return Amount{}, fmt.Errorf("money: parse %q: %w", s, ErrInvalidAmount)
	}

	exp := c.exponent
	mag, err := strconv.ParseUint(intPart, 10, 64)
	if err != nil {
		return Amount{}, fmt.Errorf("money: parse %q: %w", s, ErrOverflow)
	}
	pow := pow10(exp)
	if mag > math.MaxUint64/pow {
		return Amount{}, fmt.Errorf("money: parse %q: %w", s, ErrOverflow)
	}
	mag *= pow

	if hasFrac {
		keep := fracPart
		roundUp := false
		if len(fracPart) > exp {
			keep = fracPart[:exp]
			// Rounding half up on the magnitude: any discarded tail starting
			// with 5 or more rounds the kept fraction up.
			roundUp = fracPart[exp] >= '5'
		}
		var fracMinor uint64
		if keep != "" {
			fracMinor, err = strconv.ParseUint(keep, 10, 64)
			if err != nil {
				return Amount{}, fmt.Errorf("money: parse %q: %w", s, ErrOverflow)
			}
			if len(fracPart) <= exp {
				fracMinor *= pow / pow10(len(fracPart))
			}
		}
		if roundUp {
			fracMinor++
		}
		if fracMinor > math.MaxUint64-mag {
			return Amount{}, fmt.Errorf("money: parse %q: %w", s, ErrOverflow)
		}
		mag += fracMinor
	}

	minor, ok := fromMag(mag, neg)
	if !ok {
		return Amount{}, fmt.Errorf("money: parse %q: %w", s, ErrOverflow)
	}
	return Amount{minor: minor, cur: c}, nil
}

// MustFromMajor is like FromMajorString but panics on error. It is intended for
// constants and tests.
func MustFromMajor(s string, c Currency) Amount {
	a, err := FromMajorString(s, c)
	if err != nil {
		panic(err)
	}
	return a
}

// Minor returns the value in currency minor units (USD 19.99 is 1999).
func (a Amount) Minor() int64 { return a.minor }

// Currency returns the currency of a.
func (a Amount) Currency() Currency { return a.cur }

// Add returns the sum of a and o. It returns an error wrapping
// ErrCurrencyMismatch if the currencies differ and ErrOverflow if the sum does
// not fit in int64 minor units.
func (a Amount) Add(o Amount) (Amount, error) {
	if a.cur.code != o.cur.code {
		return Amount{}, fmt.Errorf("money: add %s and %s: %w", a.Format(), o.Format(), ErrCurrencyMismatch)
	}
	sum, ok := addOverflow(a.minor, o.minor)
	if !ok {
		return Amount{}, fmt.Errorf("money: add %s and %s: %w", a.Format(), o.Format(), ErrOverflow)
	}
	return Amount{minor: sum, cur: a.cur}, nil
}

// Sub returns a minus o. It returns an error wrapping ErrCurrencyMismatch if
// the currencies differ and ErrOverflow if the difference does not fit in int64
// minor units.
func (a Amount) Sub(o Amount) (Amount, error) {
	if a.cur.code != o.cur.code {
		return Amount{}, fmt.Errorf("money: sub %s and %s: %w", a.Format(), o.Format(), ErrCurrencyMismatch)
	}
	diff, ok := subOverflow(a.minor, o.minor)
	if !ok {
		return Amount{}, fmt.Errorf("money: sub %s and %s: %w", a.Format(), o.Format(), ErrOverflow)
	}
	return Amount{minor: diff, cur: a.cur}, nil
}

// MulQty returns a multiplied by the integer quantity qty, keeping the currency
// of a. Zero and negative quantities are allowed. It returns an error wrapping
// ErrOverflow if the product does not fit in int64 minor units.
func (a Amount) MulQty(qty int64) (Amount, error) {
	prod, ok := mulOverflow(a.minor, qty)
	if !ok {
		return Amount{}, fmt.Errorf("money: mul %s by %d: %w", a.Format(), qty, ErrOverflow)
	}
	return Amount{minor: prod, cur: a.cur}, nil
}

// Neg returns the negation of a. As a documented edge case, negating the most
// negative representable amount (math.MinInt64 minor units) is not possible in
// int64; Neg returns a unchanged in that case.
func (a Amount) Neg() Amount {
	if a.minor == math.MinInt64 {
		return a
	}
	return Amount{minor: -a.minor, cur: a.cur}
}

// Abs returns the absolute value of a. Like Neg, it returns a unchanged for the
// most negative representable amount (math.MinInt64 minor units).
func (a Amount) Abs() Amount {
	if a.minor < 0 {
		return a.Neg()
	}
	return a
}

// Equal reports whether a and o have the same currency and the same value.
// Amounts in different currencies are never equal.
func (a Amount) Equal(o Amount) bool {
	return a.cur.code == o.cur.code && a.minor == o.minor
}

// Compare returns -1, 0 or +1 depending on whether a is less than, equal to or
// greater than o. It returns an error wrapping ErrCurrencyMismatch if the
// currencies differ.
func (a Amount) Compare(o Amount) (int, error) {
	if a.cur.code != o.cur.code {
		return 0, fmt.Errorf("money: compare %s and %s: %w", a.Format(), o.Format(), ErrCurrencyMismatch)
	}
	switch {
	case a.minor < o.minor:
		return -1, nil
	case a.minor > o.minor:
		return 1, nil
	default:
		return 0, nil
	}
}

// LessThan reports whether a is less than o. It returns an error wrapping
// ErrCurrencyMismatch if the currencies differ.
func (a Amount) LessThan(o Amount) (bool, error) {
	cmp, err := a.Compare(o)
	if err != nil {
		return false, err
	}
	return cmp < 0, nil
}

// GreaterThan reports whether a is greater than o. It returns an error wrapping
// ErrCurrencyMismatch if the currencies differ.
func (a Amount) GreaterThan(o Amount) (bool, error) {
	cmp, err := a.Compare(o)
	if err != nil {
		return false, err
	}
	return cmp > 0, nil
}

// IsZero reports whether a is exactly zero.
func (a Amount) IsZero() bool { return a.minor == 0 }

// IsNegative reports whether a is below zero.
func (a Amount) IsNegative() bool { return a.minor < 0 }

// IsPositive reports whether a is above zero (zero itself is not positive).
func (a Amount) IsPositive() bool { return a.minor > 0 }

// String formats a in major units with the currency's minor-unit exponent, so
// USD 1234 renders as "12.34", VND 1234 renders as "1234" and USD 5 renders as
// "0.05". It makes Amount a fmt.Stringer.
func (a Amount) String() string {
	neg := a.minor < 0
	s := strconv.FormatUint(absUint(a.minor), 10)
	if exp := a.cur.exponent; exp > 0 {
		for len(s) <= exp {
			s = "0" + s
		}
		s = s[:len(s)-exp] + "." + s[len(s)-exp:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// Format returns the currency code followed by the formatted amount, e.g.
// "USD 12.34" or "VND 1234". For the zero Currency (empty code) it returns the
// bare amount.
func (a Amount) Format() string {
	if a.cur.code == "" {
		return a.String()
	}
	return a.cur.code + " " + a.String()
}

// allDigits reports whether s is non-empty and consists only of ASCII digits.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// pow10 returns 10^n. It is only meaningful for small n (currency exponents).
func pow10(n int) uint64 {
	r := uint64(1)
	for i := 0; i < n; i++ {
		r *= 10
	}
	return r
}

// absUint returns the absolute value of v as a uint64 (correct even for
// math.MinInt64).
func absUint(v int64) uint64 {
	if v < 0 {
		return uint64(-(v + 1)) + 1
	}
	return uint64(v)
}

// fromMag converts an unsigned magnitude and a sign into a signed minor-unit
// value, reporting false when the value is not representable as int64.
func fromMag(mag uint64, neg bool) (int64, bool) {
	if neg {
		if mag > 1<<63 {
			return 0, false
		}
		if mag == 1<<63 {
			return math.MinInt64, true
		}
		return -int64(mag), true
	}
	if mag > math.MaxInt64 {
		return 0, false
	}
	return int64(mag), true
}

// addOverflow returns a+b, reporting false if the result overflows int64.
func addOverflow(a, b int64) (int64, bool) {
	c := a + b
	if (b > 0 && c < a) || (b < 0 && c > a) {
		return 0, false
	}
	return c, true
}

// subOverflow returns a-b, reporting false if the result overflows int64.
func subOverflow(a, b int64) (int64, bool) {
	c := a - b
	if (b < 0 && c < a) || (b > 0 && c > a) {
		return 0, false
	}
	return c, true
}

// mulOverflow returns a*b, reporting false if the result overflows int64.
func mulOverflow(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if (a == math.MinInt64 && b == -1) || (b == math.MinInt64 && a == -1) {
		return 0, false
	}
	c := a * b
	if c/b != a {
		return 0, false
	}
	return c, true
}
