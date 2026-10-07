package money

import "errors"

// Sentinel errors returned by this package. Every error returned by this
// package wraps exactly one of these values, plus context about the operation
// and the values involved. Use errors.Is to test for them.
var (
	// ErrCurrencyMismatch is returned when an operation combines amounts with
	// different currencies.
	ErrCurrencyMismatch = errors.New("money: currency mismatch")

	// ErrUnknownCurrency is returned by NewCurrency for ISO 4217 codes outside
	// the supported set.
	ErrUnknownCurrency = errors.New("money: unknown currency")

	// ErrOverflow is returned when a calculation or a parse result cannot be
	// represented as int64 minor units.
	ErrOverflow = errors.New("money: integer overflow")

	// ErrDivideByZero is returned when a value would be divided by zero (for
	// example a rounding increment of 0 or allocation weights summing to 0).
	ErrDivideByZero = errors.New("money: division by zero")

	// ErrInvalidAmount is returned for malformed input, such as a major-unit
	// string that is not a plain decimal number or a non-positive part count.
	ErrInvalidAmount = errors.New("money: invalid amount")

	// ErrNegativeNotAllowed is returned where negative values are not
	// meaningful: negative allocation weights and negative rounding
	// increments.
	ErrNegativeNotAllowed = errors.New("money: negative value not allowed")
)
