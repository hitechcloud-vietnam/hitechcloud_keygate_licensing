package money

import (
	"fmt"
	"strings"
)

// Currency is an ISO 4217 currency: a three-letter code plus the minor-unit
// exponent, i.e. the number of decimal digits that make up one major unit. USD
// and EUR have exponent 2 (1 USD = 100 cents) while JPY and VND have exponent 0
// (the major unit is already the minor unit).
//
// The zero Currency is the empty code with exponent 0. It behaves as a valid
// anonymous 0-exponent currency; prefer NewCurrency for real currencies.
type Currency struct {
	code     string
	exponent int
}

// currencyExponents maps the supported ISO 4217 codes to their minor-unit
// exponent.
var currencyExponents = map[string]int{
	"AUD": 2,
	"CAD": 2,
	"CHF": 2,
	"CNY": 2,
	"EUR": 2,
	"GBP": 2,
	"IDR": 0,
	"INR": 2,
	"JPY": 0,
	"KRW": 0,
	"PHP": 2,
	"SGD": 2,
	"THB": 2,
	"USD": 2,
	"VND": 0,
}

// NewCurrency returns the Currency for the given ISO 4217 code. The code is
// case-insensitive and surrounding whitespace is ignored ("usd" and " EUR "
// are accepted). It returns an error wrapping ErrUnknownCurrency for codes
// outside the supported set.
func NewCurrency(code string) (Currency, error) {
	c := strings.ToUpper(strings.TrimSpace(code))
	exp, ok := currencyExponents[c]
	if !ok {
		return Currency{}, fmt.Errorf("money: currency %q: %w", code, ErrUnknownCurrency)
	}
	return Currency{code: c, exponent: exp}, nil
}

// MustCurrency is like NewCurrency but panics on error. It is intended for
// package-level constants and tests.
func MustCurrency(code string) Currency {
	c, err := NewCurrency(code)
	if err != nil {
		panic(err)
	}
	return c
}

// Code returns the ISO 4217 code, e.g. "USD". The zero Currency has code "".
func (c Currency) Code() string { return c.code }

// Exponent returns the minor-unit exponent: the number of decimal digits that
// make up one major unit (2 for USD, 0 for JPY and VND).
func (c Currency) Exponent() int { return c.exponent }

// String returns the currency code, e.g. "USD", making Currency a fmt.Stringer.
func (c Currency) String() string { return c.code }
