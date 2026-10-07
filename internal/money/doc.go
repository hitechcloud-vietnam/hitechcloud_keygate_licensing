// Package money provides the shared money type for the HiTechCloud license and
// commerce platform.
//
// # Storage model
//
// A monetary value is always an exact integer count of currency minor units
// (for example USD 19.99 is stored as the int64 1999) together with a
// Currency. Money is never stored or computed as a floating point number:
// every calculation in this package is exact integer arithmetic, and any
// result that would fall between two minor units (for example a 33.33% tax) is
// resolved with an explicit, deterministic RoundMode.
//
// # Rounding
//
// Sub-minor-unit remainders are resolved on the magnitude of the exact result,
// so results are symmetric around zero: RoundUp always moves away from zero and
// RoundDown always truncates toward zero. The default mode, RoundHalfUp, breaks
// ties away from zero; RoundHalfEven (banker's rounding) breaks ties toward the
// even neighbour.
//
// # Splitting
//
// Split and Allocate never lose or create minor units: the parts always sum
// exactly back to the original amount. Any remainder is distributed one minor
// unit at a time to the parts with the largest fractional remainder,
// tie-breaking on the lowest index, so 100 minor units split three ways is 34,
// 33, 33.
//
// # Errors
//
// Operations that cannot be performed return errors wrapping one of the
// sentinel values defined in this package (ErrCurrencyMismatch,
// ErrUnknownCurrency, ErrOverflow, ErrDivideByZero, ErrInvalidAmount,
// ErrNegativeNotAllowed). Use errors.Is to test for them.
package money
