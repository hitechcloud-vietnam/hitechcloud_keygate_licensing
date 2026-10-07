package money

import (
	"errors"
	"fmt"
	"math"
	"testing"
)

// minors returns the minor-unit values of the given amounts.
func minors(as []Amount) []int64 {
	out := make([]int64, len(as))
	for i, a := range as {
		out[i] = a.Minor()
	}
	return out
}

func equalInt64s(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSplit(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		cur  Currency
		n    int
		want []int64
	}{
		// Documented rule: the remainder goes to the earliest parts, so
		// 100 minor units split three ways is 34, 33, 33.
		{"100 into 3", 100, usd, 3, []int64{34, 33, 33}},
		{"10 into 3", 10, usd, 3, []int64{4, 3, 3}},
		{"3 into 5", 3, usd, 5, []int64{1, 1, 1, 0, 0}},
		{"negative 100 into 3", -100, usd, 3, []int64{-34, -33, -33}},
		{"100 into 1", 100, usd, 1, []int64{100}},
		{"100 into 4", 100, usd, 4, []int64{25, 25, 25, 25}},
		{"zero into 3", 0, usd, 3, []int64{0, 0, 0}},
		{"min int64 into 1", math.MinInt64, usd, 1, []int64{math.MinInt64}},
		{"min int64 into 2", math.MinInt64, usd, 2, []int64{math.MinInt64 / 2, math.MinInt64 / 2}},
		{"vnd currency kept", 100, vnd, 3, []int64{34, 33, 33}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FromMinor(tt.in, tt.cur).Split(tt.n)
			if err != nil {
				t.Fatalf("Split(%d) unexpected err %v", tt.n, err)
			}
			if m := minors(got); !equalInt64s(m, tt.want) {
				t.Fatalf("Split(%d) = %v, want %v", tt.n, m, tt.want)
			}
			for _, p := range got {
				if p.Currency() != tt.cur {
					t.Fatalf("Split part currency = %v, want %v", p.Currency(), tt.cur)
				}
			}
		})
	}
}

func TestSplitErrors(t *testing.T) {
	a := FromMinor(100, usd)
	if _, err := a.Split(0); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("Split(0) err = %v, want ErrInvalidAmount", err)
	}
	if _, err := a.Split(-3); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("Split(-3) err = %v, want ErrInvalidAmount", err)
	}
}

func TestAllocate(t *testing.T) {
	tests := []struct {
		name    string
		in      int64
		cur     Currency
		weights []int64
		want    []int64
	}{
		// Documented rule: largest fractional remainder first, lowest index on
		// ties, so equal thirds of 100 is 34, 33, 33.
		{"equal thirds", 100, usd, []int64{1, 1, 1}, []int64{34, 33, 33}},
		{"30/70 split", 100, usd, []int64{3, 7}, []int64{30, 70}},
		{"largest remainder wins", 101, usd, []int64{2, 3, 5}, []int64{20, 30, 51}},
		{"negative amount", -100, usd, []int64{1, 1, 1}, []int64{-34, -33, -33}},
		{"zero weight", 100, usd, []int64{0, 5}, []int64{0, 100}},
		{"single weight", 999, usd, []int64{1}, []int64{999}},
		{"zero amount", 0, usd, []int64{1, 2}, []int64{0, 0}},
		{"huge weights do not overflow", 100, usd, []int64{math.MaxInt64, math.MaxInt64}, []int64{50, 50}},
		{"tiny amount", 1, vnd, []int64{1, 1, 1, 1}, []int64{1, 0, 0, 0}},
		{"remainder to second part", 2, usd, []int64{1, 1, 1}, []int64{1, 1, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FromMinor(tt.in, tt.cur).Allocate(tt.weights...)
			if err != nil {
				t.Fatalf("Allocate(%v) unexpected err %v", tt.weights, err)
			}
			if m := minors(got); !equalInt64s(m, tt.want) {
				t.Fatalf("Allocate(%v) = %v, want %v", tt.weights, m, tt.want)
			}
		})
	}
}

func TestAllocateErrors(t *testing.T) {
	a := FromMinor(100, usd)
	if _, err := a.Allocate(); !errors.Is(err, ErrInvalidAmount) {
		t.Errorf("Allocate() err = %v, want ErrInvalidAmount", err)
	}
	if _, err := a.Allocate(1, -1); !errors.Is(err, ErrNegativeNotAllowed) {
		t.Errorf("Allocate(1, -1) err = %v, want ErrNegativeNotAllowed", err)
	}
	if _, err := a.Allocate(0, 0); !errors.Is(err, ErrDivideByZero) {
		t.Errorf("Allocate(0, 0) err = %v, want ErrDivideByZero", err)
	}
}

// TestSplitMatchesAllocate verifies that Split(n) is exactly Allocate with n
// equal weights of 1.
func TestSplitMatchesAllocate(t *testing.T) {
	amounts := []int64{0, 1, -1, 7, 100, -100, 999999, math.MinInt64, math.MaxInt64}
	for _, m := range amounts {
		for n := 1; n <= 5; n++ {
			t.Run(fmt.Sprintf("%d/%d", m, n), func(t *testing.T) {
				a := FromMinor(m, usd)
				split, err := a.Split(n)
				if err != nil {
					t.Fatalf("Split unexpected err %v", err)
				}
				weights := make([]int64, n)
				for i := range weights {
					weights[i] = 1
				}
				alloc, err := a.Allocate(weights...)
				if err != nil {
					t.Fatalf("Allocate unexpected err %v", err)
				}
				if !equalInt64s(minors(split), minors(alloc)) {
					t.Fatalf("Split = %v, Allocate = %v, want identical", minors(split), minors(alloc))
				}
			})
		}
	}
}

// TestSplitAllocatePreserveTotal verifies the core invariant: no minor unit is
// ever lost or created.
func TestSplitAllocatePreserveTotal(t *testing.T) {
	amounts := []int64{0, 1, -1, 2, 99, 100, -100, 101, 12345, math.MinInt64, math.MaxInt64}
	weightSets := [][]int64{
		{1, 1, 1},
		{3, 7},
		{2, 3, 5},
		{1},
		{0, 5},
		{5, 0},
		{1, 0, 1, 0, 1},
	}
	for _, m := range amounts {
		a := FromMinor(m, usd)
		for n := 1; n <= 7; n++ {
			parts, err := a.Split(n)
			if err != nil {
				t.Fatalf("Split(%d) unexpected err %v", n, err)
			}
			sum := int64(0)
			for _, p := range parts {
				sum += p.Minor()
			}
			if sum != m {
				t.Fatalf("Split(%d) of %d sums to %d, want %d", n, m, sum, m)
			}
		}
		for _, w := range weightSets {
			parts, err := a.Allocate(w...)
			if err != nil {
				t.Fatalf("Allocate(%v) unexpected err %v", w, err)
			}
			sum := int64(0)
			for _, p := range parts {
				sum += p.Minor()
			}
			if sum != m {
				t.Fatalf("Allocate(%v) of %d sums to %d, want %d", w, m, sum, m)
			}
		}
	}
}
