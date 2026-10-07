package tax

import (
	"errors"
	"math"
	"testing"
)

// Note on rates: basis points are whole integers (10_000 = 100%). The common
// US-NY combined rate 8.875% equals 887.5 bps and is therefore not exactly
// representable; its whole-bps neighbors 887 (8.87%) and 888 (8.88%) are used
// below to bracket that case.

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		rate    Rate
		wantErr error
	}{
		{"zero rate is valid", Rate{BasisPoints: 0, Jurisdiction: "ZERO"}, nil},
		{"ordinary rate is valid", Rate{BasisPoints: 888, Jurisdiction: "US-NY"}, nil},
		{"100% rate is valid", Rate{BasisPoints: 10_000, Jurisdiction: "VAT-VN"}, nil},
		{"empty rate is valid", Rate{}, nil},
		{"negative bps rejected", Rate{BasisPoints: -1, Jurisdiction: "BAD"}, ErrInvalidRate},
		{"large negative bps rejected", Rate{BasisPoints: math.MinInt64, Jurisdiction: "BAD"}, ErrInvalidRate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.rate)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate(%+v) = %v, want nil", tt.rate, err)
				}
				// Method form must agree.
				if err := tt.rate.Validate(); err != nil {
					t.Fatalf("rate.Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate(%+v) = %v, want %v", tt.rate, err, tt.wantErr)
			}
		})
	}
}

func TestCalculateExclusive(t *testing.T) {
	tests := []struct {
		name   string
		amount int64 // net amount
		bps    int64
		round  Rounding
		want   Breakdown
	}{
		{"zero rate", 12_345, 0, RoundingHalfUp, Breakdown{Net: 12_345, Tax: 0, Gross: 12_345}},
		{"zero amount", 0, 1000, RoundingHalfUp, Breakdown{Net: 0, Tax: 0, Gross: 0}},
		{"10% exact", 100_000, 1000, RoundingHalfUp, Breakdown{Net: 100_000, Tax: 10_000, Gross: 110_000}},
		{"8.87% exact", 100_000, 887, RoundingHalfUp, Breakdown{Net: 100_000, Tax: 8870, Gross: 108_870}},
		{"8.88% exact", 100_000, 888, RoundingHalfUp, Breakdown{Net: 100_000, Tax: 8880, Gross: 108_880}},
		{"20% exact", 12_345, 2000, RoundingHalfUp, Breakdown{Net: 12_345, Tax: 2469, Gross: 14_814}},
		{"100% rate", 5000, 10_000, RoundingHalfUp, Breakdown{Net: 5000, Tax: 5000, Gross: 10_000}},
		{"100% on one unit", 1, 10_000, RoundingHalfUp, Breakdown{Net: 1, Tax: 1, Gross: 2}},
		{"tie half-up", 12_345, 1000, RoundingHalfUp, Breakdown{Net: 12_345, Tax: 1235, Gross: 13_580}},
		{"tie half-even", 12_345, 1000, RoundingHalfEven, Breakdown{Net: 12_345, Tax: 1234, Gross: 13_579}},
		{"tie down", 12_345, 1000, RoundingDown, Breakdown{Net: 12_345, Tax: 1234, Gross: 13_579}},
		{"sub-unit down", 100, 50, RoundingDown, Breakdown{Net: 100, Tax: 0, Gross: 100}},
		{"sub-unit half-up", 100, 50, RoundingHalfUp, Breakdown{Net: 100, Tax: 1, Gross: 101}},
		{"sub-unit half-even", 100, 50, RoundingHalfEven, Breakdown{Net: 100, Tax: 0, Gross: 100}},
		{"8.88% on 333 rounds up", 333, 888, RoundingHalfUp, Breakdown{Net: 333, Tax: 30, Gross: 363}},
		{"8.88% on 333 rounds down", 333, 888, RoundingDown, Breakdown{Net: 333, Tax: 29, Gross: 362}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Calculate(tt.amount, Rate{BasisPoints: tt.bps}, false, tt.round)
			if err != nil {
				t.Fatalf("Calculate(%d, %d bps, exclusive, %v) error: %v", tt.amount, tt.bps, tt.round, err)
			}
			if got != tt.want {
				t.Fatalf("Calculate(%d, %d bps, exclusive, %v) = %+v, want %+v",
					tt.amount, tt.bps, tt.round, got, tt.want)
			}
			if got.Net+got.Tax != got.Gross {
				t.Fatalf("invariant broken: %+v", got)
			}
		})
	}
}

func TestCalculateInclusive(t *testing.T) {
	tests := []struct {
		name   string
		amount int64 // gross
		bps    int64
		round  Rounding
		want   Breakdown
	}{
		{"zero rate", 12_345, 0, RoundingHalfUp, Breakdown{Net: 12_345, Tax: 0, Gross: 12_345}},
		{"zero amount", 0, 1000, RoundingHalfUp, Breakdown{Net: 0, Tax: 0, Gross: 0}},
		{"10% exact", 11_000, 1000, RoundingHalfUp, Breakdown{Net: 10_000, Tax: 1000, Gross: 11_000}},
		{"8.87% exact", 108_870, 887, RoundingHalfUp, Breakdown{Net: 100_000, Tax: 8870, Gross: 108_870}},
		{"8.88% exact", 108_880, 888, RoundingHalfUp, Breakdown{Net: 100_000, Tax: 8880, Gross: 108_880}},
		{"8.88% inexact extraction", 100_000, 888, RoundingHalfUp, Breakdown{Net: 91_844, Tax: 8156, Gross: 100_000}},
		{"20% exact", 12_000, 2000, RoundingHalfUp, Breakdown{Net: 10_000, Tax: 2000, Gross: 12_000}},
		{"100% rate", 1000, 10_000, RoundingHalfUp, Breakdown{Net: 500, Tax: 500, Gross: 1000}},
		{"odd gross 100% half-up", 5, 10_000, RoundingHalfUp, Breakdown{Net: 2, Tax: 3, Gross: 5}},
		{"odd gross 100% half-even", 5, 10_000, RoundingHalfEven, Breakdown{Net: 3, Tax: 2, Gross: 5}},
		{"odd gross 100% down", 5, 10_000, RoundingDown, Breakdown{Net: 3, Tax: 2, Gross: 5}},
		{"10% inexact", 2000, 1000, RoundingHalfUp, Breakdown{Net: 1818, Tax: 182, Gross: 2000}},
		{"50% tiny", 3, 5000, RoundingHalfUp, Breakdown{Net: 2, Tax: 1, Gross: 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Calculate(tt.amount, Rate{BasisPoints: tt.bps}, true, tt.round)
			if err != nil {
				t.Fatalf("Calculate(%d, %d bps, inclusive, %v) error: %v", tt.amount, tt.bps, tt.round, err)
			}
			if got != tt.want {
				t.Fatalf("Calculate(%d, %d bps, inclusive, %v) = %+v, want %+v",
					tt.amount, tt.bps, tt.round, got, tt.want)
			}
			if got.Net+got.Tax != got.Gross {
				t.Fatalf("invariant broken: %+v", got)
			}
		})
	}
}

// TestBreakdownInvariant checks Net+Tax==Gross (and non-negativity) across a
// wide matrix of amounts, rates, pricing modes and rounding modes.
func TestBreakdownInvariant(t *testing.T) {
	amounts := []int64{0, 1, 7, 99, 100, 333, 999, 12_345, 100_000, 999_999_999}
	bpsList := []int64{0, 1, 50, 887, 888, 1000, 2000, 5000, 10_000, 20_000}
	roundings := []Rounding{RoundingHalfUp, RoundingHalfEven, RoundingDown}

	for _, amount := range amounts {
		for _, bps := range bpsList {
			for _, inclusive := range []bool{false, true} {
				for _, round := range roundings {
					got, err := Calculate(amount, Rate{BasisPoints: bps}, inclusive, round)
					if err != nil {
						t.Fatalf("Calculate(%d, %d, %v, %v) error: %v", amount, bps, inclusive, round, err)
					}
					if got.Net < 0 || got.Tax < 0 || got.Gross < 0 {
						t.Fatalf("negative component in %+v (amount=%d bps=%d incl=%v round=%v)",
							got, amount, bps, inclusive, round)
					}
					if got.Net+got.Tax != got.Gross {
						t.Fatalf("Net+Tax != Gross: %+v (amount=%d bps=%d incl=%v round=%v)",
							got, amount, bps, inclusive, round)
					}
					if inclusive && got.Gross != amount {
						t.Fatalf("inclusive gross changed: %+v (amount=%d)", got, amount)
					}
					if !inclusive && got.Net != amount {
						t.Fatalf("exclusive net changed: %+v (amount=%d)", got, amount)
					}
				}
			}
		}
	}
}

// TestInclusiveExclusiveRoundTrip extracts tax inclusively, then recomputes
// the tax exclusively from the extracted net and confirms the same tax and
// the original gross. (Exact round-tripping is guaranteed for amounts where
// the extraction is exact or unambiguous; near-tie fractions on very small
// amounts can legitimately shift a unit between net and tax.)
func TestInclusiveExclusiveRoundTrip(t *testing.T) {
	tests := []struct {
		gross int64
		bps   int64
	}{
		{11_000, 1000},
		{108_880, 888},
		{108_870, 887},
		{12_000, 2000},
		{1000, 10_000},
		{2000, 1000},
		{99_999, 888},
		{50_000, 2000},
		{100_000, 887},
		{123_456, 888},
	}
	for _, tt := range tests {
		incl, err := Calculate(tt.gross, Rate{BasisPoints: tt.bps}, true, RoundingHalfUp)
		if err != nil {
			t.Fatalf("inclusive Calculate(%d, %d): %v", tt.gross, tt.bps, err)
		}
		excl, err := Calculate(incl.Net, Rate{BasisPoints: tt.bps}, false, RoundingHalfUp)
		if err != nil {
			t.Fatalf("exclusive Calculate(%d, %d): %v", incl.Net, tt.bps, err)
		}
		if excl.Tax != incl.Tax {
			t.Errorf("round-trip tax mismatch: gross=%d bps=%d inclusive tax=%d exclusive tax=%d",
				tt.gross, tt.bps, incl.Tax, excl.Tax)
		}
		if excl.Gross != tt.gross {
			t.Errorf("round-trip gross mismatch: gross=%d bps=%d reconstructed=%d",
				tt.gross, tt.bps, excl.Gross)
		}
	}
}

// TestRoundingModesDiffer pins the tie-breaking behavior of the three modes
// on values that round differently under each.
func TestRoundingModesDiffer(t *testing.T) {
	tests := []struct {
		name     string
		amount   int64 // net
		bps      int64
		halfUp   int64 // expected tax
		halfEven int64
		down     int64
	}{
		{"exact 2.5 rounds up/even/down", 500, 50, 3, 2, 2},
		{"exact 1.5 rounds up+even/down", 300, 50, 2, 2, 1},
		{"exact 0.5 rounds up only", 100, 50, 1, 0, 0},
		{"exact 3.5 rounds up+even/down", 700, 50, 4, 4, 3},
		{"exact 1234.5 at 10%", 12_345, 1000, 1235, 1234, 1234},
		{"no fraction: all agree", 2500, 1000, 250, 250, 250},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cases := []struct {
				round Rounding
				want  int64
			}{
				{RoundingHalfUp, tt.halfUp},
				{RoundingHalfEven, tt.halfEven},
				{RoundingDown, tt.down},
			}
			for _, c := range cases {
				got, err := Calculate(tt.amount, Rate{BasisPoints: tt.bps}, false, c.round)
				if err != nil {
					t.Fatalf("Calculate(%d, %d, %v): %v", tt.amount, tt.bps, c.round, err)
				}
				if got.Tax != c.want {
					t.Errorf("tax with %v = %d, want %d (net=%d bps=%d)", c.round, got.Tax, c.want, tt.amount, tt.bps)
				}
				if got.Net+got.Tax != got.Gross {
					t.Errorf("invariant broken with %v: %+v", c.round, got)
				}
			}
		})
	}
}

func TestCalculateMulti(t *testing.T) {
	tests := []struct {
		name      string
		amount    int64
		rates     []Rate
		inclusive bool
		round     Rounding
		want      MultiBreakdown
	}{
		{
			name:   "state + city exclusive additive",
			amount: 100_000,
			rates: []Rate{
				{BasisPoints: 625, Jurisdiction: "US-CA"},
				{BasisPoints: 255, Jurisdiction: "US-CA-LA"},
			},
			round: RoundingHalfUp,
			want: MultiBreakdown{
				Net: 100_000, Tax: 8800, Gross: 108_800,
				Components: []TaxComponent{
					{Rate: Rate{BasisPoints: 625, Jurisdiction: "US-CA"}, Tax: 6250},
					{Rate: Rate{BasisPoints: 255, Jurisdiction: "US-CA-LA"}, Tax: 2550},
				},
			},
		},
		{
			name:   "residual penny goes to largest fractional remainder",
			amount: 100,
			rates: []Rate{
				{BasisPoints: 333, Jurisdiction: "A"},
				{BasisPoints: 333, Jurisdiction: "B"},
				{BasisPoints: 334, Jurisdiction: "C"},
			},
			round: RoundingHalfUp,
			want: MultiBreakdown{
				Net: 100, Tax: 10, Gross: 110,
				Components: []TaxComponent{
					{Rate: Rate{BasisPoints: 333, Jurisdiction: "A"}, Tax: 3},
					{Rate: Rate{BasisPoints: 333, Jurisdiction: "B"}, Tax: 3},
					{Rate: Rate{BasisPoints: 334, Jurisdiction: "C"}, Tax: 4},
				},
			},
		},
		{
			name:   "even split has no residual",
			amount: 100,
			rates: []Rate{
				{BasisPoints: 500, Jurisdiction: "A"},
				{BasisPoints: 500, Jurisdiction: "B"},
			},
			round: RoundingHalfUp,
			want: MultiBreakdown{
				Net: 100, Tax: 10, Gross: 110,
				Components: []TaxComponent{
					{Rate: Rate{BasisPoints: 500, Jurisdiction: "A"}, Tax: 5},
					{Rate: Rate{BasisPoints: 500, Jurisdiction: "B"}, Tax: 5},
				},
			},
		},
		{
			name:   "inclusive extraction split",
			amount: 108_800,
			rates: []Rate{
				{BasisPoints: 625, Jurisdiction: "US-CA"},
				{BasisPoints: 255, Jurisdiction: "US-CA-LA"},
			},
			inclusive: true,
			round:     RoundingHalfUp,
			want: MultiBreakdown{
				Net: 100_000, Tax: 8800, Gross: 108_800,
				Components: []TaxComponent{
					{Rate: Rate{BasisPoints: 625, Jurisdiction: "US-CA"}, Tax: 6250},
					{Rate: Rate{BasisPoints: 255, Jurisdiction: "US-CA-LA"}, Tax: 2550},
				},
			},
		},
		{
			name:   "empty rates yield zero tax",
			amount: 100,
			rates:  nil,
			round:  RoundingHalfUp,
			want:   MultiBreakdown{Net: 100, Tax: 0, Gross: 100, Components: []TaxComponent{}},
		},
		{
			name:   "all-zero rates yield zero tax components",
			amount: 100,
			rates: []Rate{
				{BasisPoints: 0, Jurisdiction: "A"},
				{BasisPoints: 0, Jurisdiction: "B"},
			},
			round: RoundingHalfUp,
			want: MultiBreakdown{
				Net: 100, Tax: 0, Gross: 100,
				Components: []TaxComponent{
					{Rate: Rate{BasisPoints: 0, Jurisdiction: "A"}, Tax: 0},
					{Rate: Rate{BasisPoints: 0, Jurisdiction: "B"}, Tax: 0},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CalculateMulti(tt.amount, tt.rates, tt.inclusive, tt.round)
			if err != nil {
				t.Fatalf("CalculateMulti error: %v", err)
			}
			if got.Net != tt.want.Net || got.Tax != tt.want.Tax || got.Gross != tt.want.Gross {
				t.Fatalf("totals = %+v, want %+v", got, tt.want)
			}
			if len(got.Components) != len(tt.want.Components) {
				t.Fatalf("components = %+v, want %+v", got.Components, tt.want.Components)
			}
			sum := int64(0)
			for i, c := range got.Components {
				if c != tt.want.Components[i] {
					t.Errorf("component[%d] = %+v, want %+v", i, c, tt.want.Components[i])
				}
				sum += c.Tax
			}
			if sum != got.Tax {
				t.Errorf("component tax sum %d != combined tax %d", sum, got.Tax)
			}
			if got.Net+got.Tax != got.Gross {
				t.Errorf("invariant broken: %+v", got)
			}

			// Additive rule: the combined result must equal a single rate of
			// the summed basis points.
			totalBps := int64(0)
			for _, r := range tt.rates {
				totalBps += r.BasisPoints
			}
			single, err := Calculate(tt.amount, Rate{BasisPoints: totalBps}, tt.inclusive, tt.round)
			if err != nil {
				t.Fatalf("single-rate Calculate: %v", err)
			}
			if single.Net != got.Net || single.Tax != got.Tax || single.Gross != got.Gross {
				t.Errorf("additive mismatch: multi=%+v single=%+v", got, single)
			}
		})
	}
}

func TestAllocate(t *testing.T) {
	tests := []struct {
		name    string
		total   int64
		weights []int64
		want    []int64
		wantErr error
	}{
		{"exact proportional split", 100, []int64{3, 2, 1}, []int64{50, 33, 17}, nil},
		{"tie goes to lower index", 10, []int64{1, 1, 1}, []int64{4, 3, 3}, nil},
		{"three-way penny split", 10, []int64{333, 333, 334}, []int64{3, 3, 4}, nil},
		{"zero weights absorb nothing", 10, []int64{0, 20}, []int64{0, 10}, nil},
		{"zero total all zero", 0, []int64{1, 2}, []int64{0, 0}, nil},
		{"single weight takes all", 7, []int64{1}, []int64{7}, nil},
		{"zero total with zero weights", 0, []int64{0, 0}, []int64{0, 0}, nil},
		{"non-zero total zero weights", 10, []int64{0, 0}, nil, ErrDivideByZero},
		{"non-zero total no weights", 10, nil, nil, ErrDivideByZero},
		{"negative total", -1, []int64{1}, nil, ErrNegativeAmount},
		{"negative weight", 5, []int64{-1}, nil, ErrNegativeAmount},
		{"weight sum overflow", 5, []int64{math.MaxInt64, 1}, nil, ErrOverflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Allocate(tt.total, tt.weights)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Allocate(%d, %v) error = %v, want %v", tt.total, tt.weights, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Allocate(%d, %v) error: %v", tt.total, tt.weights, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("Allocate(%d, %v) = %v, want %v", tt.total, tt.weights, got, tt.want)
			}
			sum := int64(0)
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("Allocate(%d, %v) = %v, want %v", tt.total, tt.weights, got, tt.want)
				}
				if got[i] < 0 {
					t.Fatalf("negative share %d", got[i])
				}
				sum += got[i]
			}
			if sum != tt.total {
				t.Fatalf("shares sum %d != total %d", sum, tt.total)
			}
		})
	}
}

func TestCalculateLinesNoLostPennies(t *testing.T) {
	t.Run("per-line rounding sums exactly", func(t *testing.T) {
		// Three lines of 50 at 5%: each tax is exactly 2.5 -> HalfUp 3.
		lines := []Line{{Quantity: 1, UnitAmount: 50}, {Quantity: 1, UnitAmount: 50}, {Quantity: 1, UnitAmount: 50}}
		rates := []Rate{{BasisPoints: 500, Jurisdiction: "XX"}}
		got, err := CalculateLines(lines, rates, false, RoundingHalfUp)
		if err != nil {
			t.Fatalf("CalculateLines: %v", err)
		}
		want := OrderBreakdown{Net: 150, Tax: 9, Gross: 159}
		if got.Net != want.Net || got.Tax != want.Tax || got.Gross != want.Gross {
			t.Fatalf("order totals = %+v, want %+v", got, want)
		}
		for i, lb := range got.Lines {
			if lb.Breakdown != (Breakdown{Net: 50, Tax: 3, Gross: 53}) {
				t.Fatalf("line[%d] = %+v, want {50 3 53}", i, lb.Breakdown)
			}
		}
		// Documented contrast: rounding once on the summed base gives 8, not 9.
		once, err := Calculate(150, rates[0], false, RoundingHalfUp)
		if err != nil {
			t.Fatalf("Calculate: %v", err)
		}
		if once.Tax != 8 || once.Tax == got.Tax {
			t.Fatalf("expected order-level rounding to differ (once=%d, per-line=%d)", once.Tax, got.Tax)
		}
		// No lost pennies: totals are exactly the sum of the lines.
		net, tax, gross := int64(0), int64(0), int64(0)
		for _, lb := range got.Lines {
			net += lb.Breakdown.Net
			tax += lb.Breakdown.Tax
			gross += lb.Breakdown.Gross
		}
		if net != got.Net || tax != got.Tax || gross != got.Gross {
			t.Fatalf("totals %+v do not equal line sums (net=%d tax=%d gross=%d)", got, net, tax, gross)
		}
	})

	t.Run("quantity times unit amount", func(t *testing.T) {
		lines := []Line{{Quantity: 3, UnitAmount: 333}}
		rates := []Rate{{BasisPoints: 1000, Jurisdiction: "XX"}}
		got, err := CalculateLines(lines, rates, false, RoundingHalfUp)
		if err != nil {
			t.Fatalf("CalculateLines: %v", err)
		}
		want := OrderBreakdown{Net: 999, Tax: 100, Gross: 1099} // 99.9 -> 100
		if got.Net != want.Net || got.Tax != want.Tax || got.Gross != want.Gross {
			t.Fatalf("order totals = %+v, want %+v", got, want)
		}
	})

	t.Run("stacked jurisdictions per line", func(t *testing.T) {
		lines := []Line{{Quantity: 2, UnitAmount: 500}}
		rates := []Rate{
			{BasisPoints: 625, Jurisdiction: "US-CA"},
			{BasisPoints: 255, Jurisdiction: "US-CA-LA"},
		}
		got, err := CalculateLines(lines, rates, false, RoundingHalfUp)
		if err != nil {
			t.Fatalf("CalculateLines: %v", err)
		}
		// base 1000 at combined 880 bps -> 88.0 exact.
		want := OrderBreakdown{Net: 1000, Tax: 88, Gross: 1088}
		if got.Net != want.Net || got.Tax != want.Tax || got.Gross != want.Gross {
			t.Fatalf("order totals = %+v, want %+v", got, want)
		}
	})

	t.Run("inclusive lines", func(t *testing.T) {
		lines := []Line{{Quantity: 1, UnitAmount: 1100}}
		rates := []Rate{{BasisPoints: 1000, Jurisdiction: "XX"}}
		got, err := CalculateLines(lines, rates, true, RoundingHalfUp)
		if err != nil {
			t.Fatalf("CalculateLines: %v", err)
		}
		want := OrderBreakdown{Net: 1000, Tax: 100, Gross: 1100}
		if got.Net != want.Net || got.Tax != want.Tax || got.Gross != want.Gross {
			t.Fatalf("order totals = %+v, want %+v", got, want)
		}
	})

	t.Run("empty lines and zero quantity", func(t *testing.T) {
		got, err := CalculateLines(nil, []Rate{{BasisPoints: 1000}}, false, RoundingHalfUp)
		if err != nil {
			t.Fatalf("CalculateLines(nil): %v", err)
		}
		if got.Net != 0 || got.Tax != 0 || got.Gross != 0 || len(got.Lines) != 0 {
			t.Fatalf("empty order = %+v, want all-zero", got)
		}
		got, err = CalculateLines([]Line{{Quantity: 0, UnitAmount: 123}}, nil, false, RoundingHalfUp)
		if err != nil {
			t.Fatalf("CalculateLines zero-qty: %v", err)
		}
		if got.Net != 0 || got.Tax != 0 || got.Gross != 0 {
			t.Fatalf("zero-quantity order = %+v, want all-zero", got)
		}
	})
}

func TestEdgeCases(t *testing.T) {
	t.Run("zero rate keeps net and gross", func(t *testing.T) {
		for _, inclusive := range []bool{false, true} {
			got, err := Calculate(42, Rate{BasisPoints: 0, Jurisdiction: "ZERO"}, inclusive, RoundingHalfUp)
			if err != nil {
				t.Fatalf("inclusive=%v: %v", inclusive, err)
			}
			want := Breakdown{Net: 42, Tax: 0, Gross: 42}
			if got != want {
				t.Fatalf("inclusive=%v got %+v, want %+v", inclusive, got, want)
			}
		}
	})

	t.Run("zero amount is all zero", func(t *testing.T) {
		for _, inclusive := range []bool{false, true} {
			got, err := Calculate(0, Rate{BasisPoints: 1234}, inclusive, RoundingHalfEven)
			if err != nil {
				t.Fatalf("inclusive=%v: %v", inclusive, err)
			}
			if got != (Breakdown{}) {
				t.Fatalf("inclusive=%v got %+v, want zero breakdown", inclusive, got)
			}
		}
	})

	t.Run("100 percent rate", func(t *testing.T) {
		excl, err := Calculate(1000, Rate{BasisPoints: 10_000}, false, RoundingHalfUp)
		if err != nil {
			t.Fatal(err)
		}
		if excl != (Breakdown{Net: 1000, Tax: 1000, Gross: 2000}) {
			t.Fatalf("exclusive 100%% = %+v", excl)
		}
		incl, err := Calculate(1000, Rate{BasisPoints: 10_000}, true, RoundingHalfUp)
		if err != nil {
			t.Fatal(err)
		}
		if incl != (Breakdown{Net: 500, Tax: 500, Gross: 1000}) {
			t.Fatalf("inclusive 100%% = %+v", incl)
		}
	})

	t.Run("rate above 100 percent", func(t *testing.T) {
		got, err := Calculate(100, Rate{BasisPoints: 20_000}, false, RoundingHalfUp)
		if err != nil {
			t.Fatal(err)
		}
		if got != (Breakdown{Net: 100, Tax: 200, Gross: 300}) {
			t.Fatalf("200%% = %+v", got)
		}
	})
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name    string
		fn      func() error
		wantErr error
	}{
		{
			name:    "negative exclusive amount",
			fn:      func() error { _, err := Calculate(-1, Rate{BasisPoints: 1000}, false, RoundingHalfUp); return err },
			wantErr: ErrNegativeAmount,
		},
		{
			name:    "negative inclusive amount",
			fn:      func() error { _, err := Calculate(-1, Rate{BasisPoints: 1000}, true, RoundingHalfUp); return err },
			wantErr: ErrNegativeAmount,
		},
		{
			name:    "negative rate",
			fn:      func() error { _, err := Calculate(100, Rate{BasisPoints: -5}, false, RoundingHalfUp); return err },
			wantErr: ErrInvalidRate,
		},
		{
			name:    "invalid rounding mode",
			fn:      func() error { _, err := Calculate(100, Rate{BasisPoints: 1000}, false, Rounding(99)); return err },
			wantErr: ErrInvalidRounding,
		},
		{
			name: "overflow: gross sum",
			fn: func() error {
				_, err := Calculate(math.MaxInt64, Rate{BasisPoints: 10_000}, false, RoundingHalfUp)
				return err
			},
			wantErr: ErrOverflow,
		},
		{
			name: "overflow: tax quotient",
			fn: func() error {
				_, err := Calculate(math.MaxInt64, Rate{BasisPoints: 20_000}, false, RoundingHalfUp)
				return err
			},
			wantErr: ErrOverflow,
		},
		{
			name: "overflow: 128-bit product",
			fn: func() error {
				_, err := Calculate(math.MaxInt64, Rate{BasisPoints: math.MaxInt64}, false, RoundingHalfUp)
				return err
			},
			wantErr: ErrOverflow,
		},
		{
			name: "overflow: inclusive denominator",
			fn: func() error {
				_, err := Calculate(100, Rate{BasisPoints: math.MaxInt64}, true, RoundingHalfUp)
				return err
			},
			wantErr: ErrOverflow,
		},
		{
			name: "multi: negative rate",
			fn: func() error {
				_, err := CalculateMulti(100, []Rate{{BasisPoints: 5}, {BasisPoints: -5}}, false, RoundingHalfUp)
				return err
			},
			wantErr: ErrInvalidRate,
		},
		{
			name: "multi: rate sum overflow",
			fn: func() error {
				_, err := CalculateMulti(100, []Rate{{BasisPoints: math.MaxInt64}, {BasisPoints: math.MaxInt64}}, false, RoundingHalfUp)
				return err
			},
			wantErr: ErrOverflow,
		},
		{
			name: "lines: negative unit amount",
			fn: func() error {
				_, err := CalculateLines([]Line{{Quantity: 1, UnitAmount: -1}}, nil, false, RoundingHalfUp)
				return err
			},
			wantErr: ErrNegativeAmount,
		},
		{
			name: "lines: negative quantity",
			fn: func() error {
				_, err := CalculateLines([]Line{{Quantity: -2, UnitAmount: 1}}, nil, false, RoundingHalfUp)
				return err
			},
			wantErr: ErrNegativeAmount,
		},
		{
			name: "lines: quantity overflow",
			fn: func() error {
				_, err := CalculateLines([]Line{{Quantity: math.MaxInt64, UnitAmount: 2}}, nil, false, RoundingHalfUp)
				return err
			},
			wantErr: ErrOverflow,
		},
		{
			name: "lines: order total overflow",
			fn: func() error {
				lines := []Line{
					{Quantity: math.MaxInt64/2 + 1, UnitAmount: 1},
					{Quantity: math.MaxInt64/2 + 1, UnitAmount: 1},
				}
				_, err := CalculateLines(lines, nil, false, RoundingHalfUp)
				return err
			},
			wantErr: ErrOverflow,
		},
		{
			name:    "lines: invalid rounding",
			fn:      func() error { _, err := CalculateLines(nil, nil, false, Rounding(-1)); return err },
			wantErr: ErrInvalidRounding,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.fn()
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want errors.Is(err, %v)", err, tt.wantErr)
			}
		})
	}
}
