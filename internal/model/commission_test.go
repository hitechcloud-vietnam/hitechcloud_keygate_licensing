package model

import (
	"math"
	"testing"
)

// The commission status vocabulary is closed — accrued, approved, paid,
// cancelled and nothing else — because status feeds the payout
// dashboards and the payout rules.
func TestValidCommissionStatus(t *testing.T) {
	for _, ok := range []string{
		CommissionStatusAccrued, CommissionStatusApproved,
		CommissionStatusPaid, CommissionStatusCancelled,
	} {
		if !ValidCommissionStatus(ok) {
			t.Errorf("ValidCommissionStatus(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "Accrued", "PAID", "paid ", "refunded", "pending", "void"} {
		if ValidCommissionStatus(bad) {
			t.Errorf("ValidCommissionStatus(%q) = true, want false", bad)
		}
	}

	// The advertised list is exactly the accepted set — the dashboard
	// keys read from it, so it may not be missing a member.
	if len(CommissionStatuses) != 4 {
		t.Fatalf("CommissionStatuses = %v, want the four accepted values", CommissionStatuses)
	}
	for _, s := range CommissionStatuses {
		if !ValidCommissionStatus(s) {
			t.Errorf("CommissionStatuses lists %q which is not valid", s)
		}
	}
}

// A rate is basis points 0..10000 — 100% is 10000, zero is meaningful
// ("no commission on this sale") and must not read as absent.
func TestValidCommissionBPS(t *testing.T) {
	for _, ok := range []int{0, 1, 1000, 5000, 10000} {
		if !ValidCommissionBPS(ok) {
			t.Errorf("ValidCommissionBPS(%d) = false, want true", ok)
		}
	}
	for _, bad := range []int{-1, -10000, 10001, math.MaxInt32} {
		if ValidCommissionBPS(bad) {
			t.Errorf("ValidCommissionBPS(%d) = true, want false", bad)
		}
	}
}

// The accrual math is basis × bps / 10000 rounded DOWN to a whole
// minor unit. The table pins the rounding rule on every shape of
// remainder: a fraction below one minor unit stays with the house
// (1000 at 3bps = 0), a fraction above truncates (1000 at 3333bps =
// 333.3 → 333), and an exact result is exact (1000 at 1000bps = 100).
func TestCommissionAmountRoundsDown(t *testing.T) {
	cases := []struct {
		name       string
		basisMinor int64
		bps        int
		want       int64
	}{
		{"1000 at 1000bps is exactly 100", 1000, 1000, 100},
		{"rounding down: 1003 at 1000bps is 100.3 -> 100", 1003, 1000, 100},
		{"rounding down: 1000 at 3bps is 0.3 -> 0", 1000, 3, 0},
		{"rounding down: 1000 at 3333bps is 333.3 -> 333", 1000, 3333, 333},
		{"zero basis earns nothing", 0, 10000, 0},
		{"zero rate earns nothing", 12345, 0, 0},
		{"100% keeps every minor unit", 123456789, 10000, 123456789},
		// The split multiplication is exact and overflow-free for any
		// int64 basis: the naive basis×bps would wrap at this size.
		{"max int64 basis at 100% is exact", math.MaxInt64, 10000, math.MaxInt64},
		{"max int64 basis at 1bps floors", math.MaxInt64, 1, 922337203685477},
	}
	for _, tc := range cases {
		if got := CommissionAmount(tc.basisMinor, tc.bps); got != tc.want {
			t.Errorf("%s: CommissionAmount(%d, %d) = %d, want %d",
				tc.name, tc.basisMinor, tc.bps, got, tc.want)
		}
	}
}

// A currency override carries the client's ISO code; only the shape is
// checkable offline — exactly three uppercase ASCII letters.
func TestValidCurrencyCode(t *testing.T) {
	for _, ok := range []string{"USD", "EUR", "VND", "GBP", "XXX"} {
		if !ValidCurrencyCode(ok) {
			t.Errorf("ValidCurrencyCode(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "usd", "Usd", "US", "USDD", "US1", "U D", "€UR", "usd ", "123"} {
		if ValidCurrencyCode(bad) {
			t.Errorf("ValidCurrencyCode(%q) = true, want false", bad)
		}
	}
}
