package money

import (
	"errors"
	"math"
	"testing"
)

// Currencies shared by the tables below.
var (
	usd = MustCurrency("USD")
	eur = MustCurrency("EUR")
	vnd = MustCurrency("VND")
	jpy = MustCurrency("JPY")
)

func TestNewCurrency(t *testing.T) {
	tests := []struct {
		name     string
		code     string
		wantCode string
		wantExp  int
		wantErr  error
	}{
		{name: "USD", code: "USD", wantCode: "USD", wantExp: 2},
		{name: "EUR", code: "EUR", wantCode: "EUR", wantExp: 2},
		{name: "GBP", code: "GBP", wantCode: "GBP", wantExp: 2},
		{name: "JPY exponent 0", code: "JPY", wantCode: "JPY", wantExp: 0},
		{name: "VND exponent 0", code: "VND", wantCode: "VND", wantExp: 0},
		{name: "CAD", code: "CAD", wantCode: "CAD", wantExp: 2},
		{name: "AUD", code: "AUD", wantCode: "AUD", wantExp: 2},
		{name: "SGD", code: "SGD", wantCode: "SGD", wantExp: 2},
		{name: "THB", code: "THB", wantCode: "THB", wantExp: 2},
		{name: "IDR exponent 0", code: "IDR", wantCode: "IDR", wantExp: 0},
		{name: "PHP", code: "PHP", wantCode: "PHP", wantExp: 2},
		{name: "CNY", code: "CNY", wantCode: "CNY", wantExp: 2},
		{name: "KRW exponent 0", code: "KRW", wantCode: "KRW", wantExp: 0},
		{name: "CHF", code: "CHF", wantCode: "CHF", wantExp: 2},
		{name: "INR", code: "INR", wantCode: "INR", wantExp: 2},
		{name: "lowercase accepted", code: "usd", wantCode: "USD", wantExp: 2},
		{name: "whitespace trimmed", code: " eur ", wantCode: "EUR", wantExp: 2},
		{name: "unknown code", code: "XYZ", wantErr: ErrUnknownCurrency},
		{name: "empty code", code: "", wantErr: ErrUnknownCurrency},
		{name: "too short", code: "US", wantErr: ErrUnknownCurrency},
		{name: "too long", code: "USDD", wantErr: ErrUnknownCurrency},
		{name: "digits not letters", code: "123", wantErr: ErrUnknownCurrency},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := NewCurrency(tt.code)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("NewCurrency(%q) err = %v, want %v", tt.code, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewCurrency(%q) unexpected err %v", tt.code, err)
			}
			if c.Code() != tt.wantCode {
				t.Errorf("Code() = %q, want %q", c.Code(), tt.wantCode)
			}
			if c.Exponent() != tt.wantExp {
				t.Errorf("Exponent() = %d, want %d", c.Exponent(), tt.wantExp)
			}
			if c.String() != tt.wantCode {
				t.Errorf("String() = %q, want %q", c.String(), tt.wantCode)
			}
		})
	}
}

func TestCurrencyZeroValue(t *testing.T) {
	var c Currency
	if c.Code() != "" {
		t.Errorf("zero Currency.Code() = %q, want empty", c.Code())
	}
	if c.Exponent() != 0 {
		t.Errorf("zero Currency.Exponent() = %d, want 0", c.Exponent())
	}
	if c.String() != "" {
		t.Errorf("zero Currency.String() = %q, want empty", c.String())
	}
}

func TestMustCurrencyPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustCurrency(\"NOPE\") did not panic")
		}
	}()
	MustCurrency("NOPE")
}

func TestFromMajorString(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		cur     Currency
		want    int64
		wantErr error
	}{
		// USD (exponent 2).
		{"usd 12.34", "12.34", usd, 1234, nil},
		{"usd 0.07", "0.07", usd, 7, nil},
		{"usd whole units", "1234", usd, 123400, nil},
		{"usd 12", "12", usd, 1200, nil},
		{"usd zero", "0", usd, 0, nil},
		{"usd 0.00", "0.00", usd, 0, nil},
		{"usd negative", "-12.34", usd, -1234, nil},
		{"usd explicit plus", "+5.00", usd, 500, nil},
		{"usd surrounding whitespace", "  12.34  ", usd, 1234, nil},
		{"usd short fraction", "12.3", usd, 1230, nil},
		{"usd leading zeros", "00012.34", usd, 1234, nil},
		{"usd round half up", "12.345", usd, 1235, nil},
		{"usd round down", "12.344", usd, 1234, nil},
		{"usd round above half", "12.3451", usd, 1235, nil},
		{"usd round carry", "12.355", usd, 1236, nil},
		{"usd 1.999 rounds to 2", "1.999", usd, 200, nil},
		{"usd tiny half up", "0.005", usd, 1, nil},
		{"usd negative tiny half up", "-0.005", usd, -1, nil},
		{"usd max int64", "92233720368547758.07", usd, math.MaxInt64, nil},
		{"usd min int64", "-92233720368547758.08", usd, math.MinInt64, nil},

		// VND / JPY (exponent 0).
		{"vnd 1234", "1234", vnd, 1234, nil},
		{"vnd rounds down", "1234.4", vnd, 1234, nil},
		{"vnd rounds half up", "1234.5", vnd, 1235, nil},
		{"vnd negative half up", "-1234.5", vnd, -1235, nil},
		{"jpy 500", "500", jpy, 500, nil},

		// Errors.
		{"empty", "", usd, 0, ErrInvalidAmount},
		{"blank", "   ", usd, 0, ErrInvalidAmount},
		{"letters", "abc", usd, 0, ErrInvalidAmount},
		{"two dots", "1.2.3", usd, 0, ErrInvalidAmount},
		{"double dot", "12..34", usd, 0, ErrInvalidAmount},
		{"currency symbol", "$12.34", usd, 0, ErrInvalidAmount},
		{"grouping comma", "1,234.56", usd, 0, ErrInvalidAmount},
		{"exponent notation", "1e3", usd, 0, ErrInvalidAmount},
		{"trailing dot", "12.", usd, 0, ErrInvalidAmount},
		{"leading dot", ".5", usd, 0, ErrInvalidAmount},
		{"sign only", "-", usd, 0, ErrInvalidAmount},
		{"interior space", "1 2", usd, 0, ErrInvalidAmount},
		{"bad fraction digit", "12.3a", usd, 0, ErrInvalidAmount},
		{"double sign", "--1", usd, 0, ErrInvalidAmount},
		{"huge integer part", "99999999999999999999", usd, 0, ErrOverflow},
		{"overflow by one cent", "92233720368547758.08", usd, 0, ErrOverflow},
		{"vnd overflow", "9223372036854775808", vnd, 0, ErrOverflow},
		{"beyond uint64 magnitude", "18446744073709551616", usd, 0, ErrOverflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FromMajorString(tt.in, tt.cur)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("FromMajorString(%q) err = %v, want %v", tt.in, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("FromMajorString(%q) unexpected err %v", tt.in, err)
			}
			if got.Minor() != tt.want {
				t.Fatalf("FromMajorString(%q).Minor() = %d, want %d", tt.in, got.Minor(), tt.want)
			}
			if got.Currency() != tt.cur {
				t.Fatalf("FromMajorString(%q) currency = %v, want %v", tt.in, got.Currency(), tt.cur)
			}
		})
	}
}

func TestFromMinorAndZero(t *testing.T) {
	a := FromMinor(1999, usd)
	if a.Minor() != 1999 {
		t.Errorf("FromMinor(1999, USD).Minor() = %d, want 1999", a.Minor())
	}
	if a.Currency() != usd {
		t.Errorf("FromMinor(1999, USD).Currency() = %v, want USD", a.Currency())
	}
	z := Zero(vnd)
	if !z.IsZero() {
		t.Errorf("Zero(VND).IsZero() = false, want true")
	}
	if z.Minor() != 0 || z.Currency() != vnd {
		t.Errorf("Zero(VND) = %d %v, want 0 VND", z.Minor(), z.Currency())
	}
}

func TestMustFromMajor(t *testing.T) {
	if got := MustFromMajor("12.34", usd); got.Minor() != 1234 {
		t.Fatalf("MustFromMajor(\"12.34\", USD).Minor() = %d, want 1234", got.Minor())
	}
}

func TestMustFromMajorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustFromMajor(\"nope\") did not panic")
		}
	}()
	MustFromMajor("nope", usd)
}

func TestAddSub(t *testing.T) {
	tests := []struct {
		name    string
		x, y    Amount
		wantAdd int64
		wantSub int64
	}{
		{"usd simple", FromMinor(1234, usd), FromMinor(250, usd), 1484, 984},
		{"usd negatives", FromMinor(-500, usd), FromMinor(200, usd), -300, -700},
		{"usd cancel out", FromMinor(250, usd), FromMinor(-250, usd), 0, 500},
		{"vnd large", FromMinor(1000000, vnd), FromMinor(1, vnd), 1000001, 999999},
		{"max plus zero", FromMinor(math.MaxInt64, usd), FromMinor(0, usd), math.MaxInt64, math.MaxInt64},
		{"min plus zero", FromMinor(math.MinInt64, usd), FromMinor(0, usd), math.MinInt64, math.MinInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sum, err := tt.x.Add(tt.y)
			if err != nil {
				t.Fatalf("Add unexpected err %v", err)
			}
			if sum.Minor() != tt.wantAdd {
				t.Fatalf("Add minor = %d, want %d", sum.Minor(), tt.wantAdd)
			}
			if sum.Currency() != tt.x.Currency() {
				t.Fatalf("Add currency = %v, want %v", sum.Currency(), tt.x.Currency())
			}
			diff, err := tt.x.Sub(tt.y)
			if err != nil {
				t.Fatalf("Sub unexpected err %v", err)
			}
			if diff.Minor() != tt.wantSub {
				t.Fatalf("Sub minor = %d, want %d", diff.Minor(), tt.wantSub)
			}
		})
	}
}

func TestAddSubErrors(t *testing.T) {
	if _, err := FromMinor(1, usd).Add(FromMinor(1, eur)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("USD.Add(EUR) err = %v, want ErrCurrencyMismatch", err)
	}
	if _, err := FromMinor(1, usd).Sub(FromMinor(1, eur)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("USD.Sub(EUR) err = %v, want ErrCurrencyMismatch", err)
	}
	if _, err := FromMinor(1, Currency{}).Add(FromMinor(1, usd)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("zero-currency.Add(USD) err = %v, want ErrCurrencyMismatch", err)
	}
	if _, err := FromMinor(math.MaxInt64, usd).Add(FromMinor(1, usd)); !errors.Is(err, ErrOverflow) {
		t.Errorf("MaxInt64.Add(1) err = %v, want ErrOverflow", err)
	}
	if _, err := FromMinor(math.MinInt64, usd).Add(FromMinor(-1, usd)); !errors.Is(err, ErrOverflow) {
		t.Errorf("MinInt64.Add(-1) err = %v, want ErrOverflow", err)
	}
	if _, err := FromMinor(math.MinInt64, usd).Sub(FromMinor(1, usd)); !errors.Is(err, ErrOverflow) {
		t.Errorf("MinInt64.Sub(1) err = %v, want ErrOverflow", err)
	}
	if _, err := FromMinor(math.MaxInt64, usd).Sub(FromMinor(-1, usd)); !errors.Is(err, ErrOverflow) {
		t.Errorf("MaxInt64.Sub(-1) err = %v, want ErrOverflow", err)
	}
	if _, err := FromMinor(math.MinInt64, usd).Add(FromMinor(math.MinInt64, usd)); !errors.Is(err, ErrOverflow) {
		t.Errorf("MinInt64.Add(MinInt64) err = %v, want ErrOverflow", err)
	}
	if _, err := FromMinor(math.MaxInt64, usd).Sub(FromMinor(math.MinInt64, usd)); !errors.Is(err, ErrOverflow) {
		t.Errorf("MaxInt64.Sub(MinInt64) err = %v, want ErrOverflow", err)
	}
	if got, err := FromMinor(math.MinInt64, usd).Sub(FromMinor(math.MinInt64, usd)); err != nil || got.Minor() != 0 {
		t.Errorf("MinInt64.Sub(MinInt64) = %d, %v, want 0, nil", got.Minor(), err)
	}
}

func TestMulQty(t *testing.T) {
	tests := []struct {
		name    string
		a       Amount
		qty     int64
		want    int64
		wantErr error
	}{
		{"simple", FromMinor(1234, usd), 3, 3702, nil},
		{"zero qty", FromMinor(1234, usd), 0, 0, nil},
		{"negative qty", FromMinor(-500, usd), 4, -2000, nil},
		{"double negative", FromMinor(-500, usd), -3, 1500, nil},
		{"times one keeps min int64", FromMinor(math.MinInt64, usd), 1, math.MinInt64, nil},
		{"negate max int64", FromMinor(math.MaxInt64, usd), -1, -math.MaxInt64, nil},
		{"overflow", FromMinor(math.MaxInt64, usd), 2, 0, ErrOverflow},
		{"overflow negating min int64", FromMinor(math.MinInt64, usd), -1, 0, ErrOverflow},
		{"overflow min times two", FromMinor(math.MinInt64, usd), 2, 0, ErrOverflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.a.MulQty(tt.qty)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("MulQty(%d) err = %v, want %v", tt.qty, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("MulQty(%d) unexpected err %v", tt.qty, err)
			}
			if got.Minor() != tt.want {
				t.Fatalf("MulQty(%d).Minor() = %d, want %d", tt.qty, got.Minor(), tt.want)
			}
			if got.Currency() != tt.a.Currency() {
				t.Fatalf("MulQty currency = %v, want %v", got.Currency(), tt.a.Currency())
			}
		})
	}
}

func TestNegAbs(t *testing.T) {
	tests := []struct {
		name    string
		in      Amount
		wantNeg int64
		wantAbs int64
	}{
		{"positive", FromMinor(1234, usd), -1234, 1234},
		{"negative", FromMinor(-1234, usd), 1234, 1234},
		{"zero", FromMinor(0, usd), 0, 0},
		// Documented edge case: math.MinInt64 cannot be negated in int64, so
		// Neg and Abs return the value unchanged.
		{"min int64 wrap", FromMinor(math.MinInt64, usd), math.MinInt64, math.MinInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.Neg().Minor(); got != tt.wantNeg {
				t.Errorf("Neg().Minor() = %d, want %d", got, tt.wantNeg)
			}
			if got := tt.in.Abs().Minor(); got != tt.wantAbs {
				t.Errorf("Abs().Minor() = %d, want %d", got, tt.wantAbs)
			}
			if got := tt.in.Neg().Currency(); got != tt.in.Currency() {
				t.Errorf("Neg() changed currency to %v", got)
			}
		})
	}
}

func TestPercentOf(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		bps  int64
		want int64
	}{
		{"quarter of 100.00", 10000, 2500, 2500},
		{"full amount", 10000, 10000, 10000},
		{"zero bps", 12345, 0, 0},
		{"zero amount", 0, 2500, 0},
		{"0.5 percent exact", 200, 50, 1},
		{"33.33 percent truncates", 100, 3333, 33},
		{"default tie rounds half up", 1, 5000, 1},
		{"1.5 rounds up", 3, 5000, 2},
		{"negative amount", -3, 5000, -2},
		{"negative bps is a discount", 10000, -2500, -2500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FromMinor(tt.in, usd).PercentOf(tt.bps)
			if err != nil {
				t.Fatalf("PercentOf(%d) unexpected err %v", tt.bps, err)
			}
			if got.Minor() != tt.want {
				t.Fatalf("PercentOf(%d) = %d, want %d", tt.bps, got.Minor(), tt.want)
			}
			if got.Currency() != usd {
				t.Fatalf("PercentOf currency = %v, want USD", got.Currency())
			}
		})
	}
}

func TestApplyBpsRoundingModes(t *testing.T) {
	tests := []struct {
		name    string
		in      int64
		bps     int64
		mode    RoundMode
		want    int64
		wantErr error
	}{
		{"quarter of 100.00", 10000, 2500, RoundHalfUp, 2500, nil},
		{"100 percent identity", 12345, 10000, RoundHalfUp, 12345, nil},
		{"zero bps", 12345, 0, RoundHalfUp, 0, nil},
		{"zero amount", 0, 2500, RoundHalfUp, 0, nil},
		{"below half truncates", 100, 3333, RoundHalfUp, 33, nil},
		{"negative rate", 10000, -2500, RoundHalfUp, -2500, nil},

		// Exact tie of half a minor unit: 1 * 5000/10000 = 0.5.
		{"tie half up", 1, 5000, RoundHalfUp, 1, nil},
		{"tie half even to zero", 1, 5000, RoundHalfEven, 0, nil},
		{"tie half down to zero", 1, 5000, RoundHalfDown, 0, nil},
		{"tie up", 1, 5000, RoundUp, 1, nil},
		{"tie down", 1, 5000, RoundDown, 0, nil},

		// 3 * 5000/10000 = 1.5.
		{"1.5 half up", 3, 5000, RoundHalfUp, 2, nil},
		{"1.5 half even to even", 3, 5000, RoundHalfEven, 2, nil},
		{"1.5 half down", 3, 5000, RoundHalfDown, 1, nil},
		{"1.5 up", 3, 5000, RoundUp, 2, nil},
		{"1.5 down", 3, 5000, RoundDown, 1, nil},
		{"exact 1.0 all modes", 2, 5000, RoundHalfEven, 1, nil},

		// Negatives round on magnitude (symmetric around zero).
		{"-0.5 half up", -1, 5000, RoundHalfUp, -1, nil},
		{"-0.5 half even", -1, 5000, RoundHalfEven, 0, nil},
		{"-0.5 half down", -1, 5000, RoundHalfDown, 0, nil},
		{"-0.5 up", -1, 5000, RoundUp, -1, nil},
		{"-0.5 down", -1, 5000, RoundDown, 0, nil},
		{"-1.5 half up", -3, 5000, RoundHalfUp, -2, nil},
		{"-1.5 half even", -3, 5000, RoundHalfEven, -2, nil},
		{"-1.5 half down", -3, 5000, RoundHalfDown, -1, nil},

		// Product exceeds int64 but the result still fits: computed exactly.
		{"huge product half up", math.MaxInt64, 5000, RoundHalfUp, 4611686018427387904, nil},
		{"huge product half down", math.MaxInt64, 5000, RoundHalfDown, 4611686018427387903, nil},
		{"min int64 times 100 percent", math.MinInt64, 10000, RoundHalfUp, math.MinInt64, nil},
		{"result overflow", math.MaxInt64, 20000, RoundHalfUp, 0, ErrOverflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FromMinor(tt.in, usd).ApplyBps(tt.bps, tt.mode)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ApplyBps(%d, %v) err = %v, want %v", tt.bps, tt.mode, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ApplyBps(%d, %v) unexpected err %v", tt.bps, tt.mode, err)
			}
			if got.Minor() != tt.want {
				t.Fatalf("ApplyBps(%d, %v) = %d, want %d", tt.bps, tt.mode, got.Minor(), tt.want)
			}
		})
	}
}

func TestMulRateMatchesApplyBps(t *testing.T) {
	a := FromMinor(9999, usd)
	want, err := a.ApplyBps(875, RoundHalfEven)
	if err != nil {
		t.Fatalf("ApplyBps unexpected err %v", err)
	}
	got, err := a.MulRate(875, RoundHalfEven)
	if err != nil {
		t.Fatalf("MulRate unexpected err %v", err)
	}
	if !got.Equal(want) {
		t.Fatalf("MulRate = %v, ApplyBps = %v, want identical", got, want)
	}
}

func TestRound(t *testing.T) {
	tests := []struct {
		name string
		in   int64
		cur  Currency
		mode RoundMode
		want int64
	}{
		{"12.34 half up", 1234, usd, RoundHalfUp, 1200},
		{"12.34 half even", 1234, usd, RoundHalfEven, 1200},
		{"12.34 up", 1234, usd, RoundUp, 1300},
		{"12.34 down", 1234, usd, RoundDown, 1200},
		{"12.50 half up", 1250, usd, RoundHalfUp, 1300},
		{"12.50 half even to 12", 1250, usd, RoundHalfEven, 1200},
		{"13.50 half even to 14", 1350, usd, RoundHalfEven, 1400},
		{"12.50 half down", 1250, usd, RoundHalfDown, 1200},
		{"12.51 half down", 1251, usd, RoundHalfDown, 1300},
		{"12.49 half up", 1249, usd, RoundHalfUp, 1200},
		{"-12.50 half up away", -1250, usd, RoundHalfUp, -1300},
		{"-12.50 half even", -1250, usd, RoundHalfEven, -1200},
		{"-12.50 up", -1250, usd, RoundUp, -1300},
		{"-12.50 down", -1250, usd, RoundDown, -1200},
		{"vnd is a no-op", 1234, vnd, RoundHalfUp, 1234},
		{"vnd no-op down", 1234, vnd, RoundDown, 1234},
		{"zero", 0, usd, RoundHalfUp, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FromMinor(tt.in, tt.cur).Round(tt.mode)
			if got.Minor() != tt.want {
				t.Fatalf("Round(%v) = %d, want %d", tt.mode, got.Minor(), tt.want)
			}
			if got.Currency() != tt.cur {
				t.Fatalf("Round currency = %v, want %v", got.Currency(), tt.cur)
			}
		})
	}
}

func TestRoundTo(t *testing.T) {
	tests := []struct {
		name      string
		in        int64
		cur       Currency
		increment int64
		mode      RoundMode
		want      int64
	}{
		{"vnd 12500 to 1000 half up", 12500, vnd, 1000, RoundHalfUp, 13000},
		{"vnd 12500 to 1000 half even", 12500, vnd, 1000, RoundHalfEven, 12000},
		{"vnd 12500 to 1000 half down", 12500, vnd, 1000, RoundHalfDown, 12000},
		{"vnd 12500 to 1000 up", 12500, vnd, 1000, RoundUp, 13000},
		{"vnd 12500 to 1000 down", 12500, vnd, 1000, RoundDown, 12000},
		{"vnd 12501 to 1000 half down", 12501, vnd, 1000, RoundHalfDown, 13000},
		{"negative vnd to 1000 half up", -12500, vnd, 1000, RoundHalfUp, -13000},
		{"usd 1234 to 5 half up", 1234, usd, 5, RoundHalfUp, 1235},
		{"usd 1232 to 5 half up", 1232, usd, 5, RoundHalfUp, 1230},
		{"increment 1 is identity", 1234, usd, 1, RoundHalfUp, 1234},
		{"min int64 to 1 down", math.MinInt64, usd, 1, RoundDown, math.MinInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := FromMinor(tt.in, tt.cur).RoundTo(tt.increment, tt.mode)
			if err != nil {
				t.Fatalf("RoundTo(%d, %v) unexpected err %v", tt.increment, tt.mode, err)
			}
			if got.Minor() != tt.want {
				t.Fatalf("RoundTo(%d, %v) = %d, want %d", tt.increment, tt.mode, got.Minor(), tt.want)
			}
		})
	}
}

func TestRoundToErrors(t *testing.T) {
	a := FromMinor(1234, usd)
	if _, err := a.RoundTo(0, RoundHalfUp); !errors.Is(err, ErrDivideByZero) {
		t.Errorf("RoundTo(0) err = %v, want ErrDivideByZero", err)
	}
	if _, err := a.RoundTo(-5, RoundHalfUp); !errors.Is(err, ErrNegativeNotAllowed) {
		t.Errorf("RoundTo(-5) err = %v, want ErrNegativeNotAllowed", err)
	}
}

func TestCompare(t *testing.T) {
	tests := []struct {
		name string
		x, y Amount
		want int
	}{
		{"less", FromMinor(100, usd), FromMinor(200, usd), -1},
		{"equal", FromMinor(200, usd), FromMinor(200, usd), 0},
		{"greater", FromMinor(300, usd), FromMinor(200, usd), 1},
		{"negative less", FromMinor(-300, usd), FromMinor(-200, usd), -1},
		{"zero equals zero", Zero(usd), FromMinor(0, usd), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmp, err := tt.x.Compare(tt.y)
			if err != nil {
				t.Fatalf("Compare unexpected err %v", err)
			}
			if cmp != tt.want {
				t.Fatalf("Compare = %d, want %d", cmp, tt.want)
			}
			lt, err := tt.x.LessThan(tt.y)
			if err != nil {
				t.Fatalf("LessThan unexpected err %v", err)
			}
			gt, err := tt.x.GreaterThan(tt.y)
			if err != nil {
				t.Fatalf("GreaterThan unexpected err %v", err)
			}
			if lt != (tt.want < 0) {
				t.Errorf("LessThan = %v, want %v", lt, tt.want < 0)
			}
			if gt != (tt.want > 0) {
				t.Errorf("GreaterThan = %v, want %v", gt, tt.want > 0)
			}
			if eq := tt.x.Equal(tt.y); eq != (tt.want == 0) {
				t.Errorf("Equal = %v, want %v", eq, tt.want == 0)
			}
		})
	}
}

func TestCompareCurrencyMismatch(t *testing.T) {
	x := FromMinor(100, usd)
	y := FromMinor(100, eur)
	if _, err := x.Compare(y); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("Compare across currencies err = %v, want ErrCurrencyMismatch", err)
	}
	if _, err := x.LessThan(y); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("LessThan across currencies err = %v, want ErrCurrencyMismatch", err)
	}
	if _, err := x.GreaterThan(y); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("GreaterThan across currencies err = %v, want ErrCurrencyMismatch", err)
	}
	if x.Equal(y) {
		t.Error("Equal across currencies = true, want false")
	}
	if x.Equal(FromMinor(100, usd)) != true {
		t.Error("Equal same value = false, want true")
	}
}

func TestPredicates(t *testing.T) {
	tests := []struct {
		name   string
		in     int64
		isZero bool
		isNeg  bool
		isPos  bool
	}{
		{"zero", 0, true, false, false},
		{"positive", 1, false, false, true},
		{"negative", -1, false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := FromMinor(tt.in, usd)
			if a.IsZero() != tt.isZero {
				t.Errorf("IsZero = %v, want %v", a.IsZero(), tt.isZero)
			}
			if a.IsNegative() != tt.isNeg {
				t.Errorf("IsNegative = %v, want %v", a.IsNegative(), tt.isNeg)
			}
			if a.IsPositive() != tt.isPos {
				t.Errorf("IsPositive = %v, want %v", a.IsPositive(), tt.isPos)
			}
		})
	}
}

func TestStringAndFormat(t *testing.T) {
	tests := []struct {
		name       string
		in         int64
		cur        Currency
		wantStr    string
		wantFormat string
	}{
		{"usd 12.34", 1234, usd, "12.34", "USD 12.34"},
		{"usd negative", -1234, usd, "-12.34", "USD -12.34"},
		{"usd few cents", 5, usd, "0.05", "USD 0.05"},
		{"usd zero", 0, usd, "0.00", "USD 0.00"},
		{"vnd 1234", 1234, vnd, "1234", "VND 1234"},
		{"vnd negative", -7, vnd, "-7", "VND -7"},
		{"jpy 100", 100, jpy, "100", "JPY 100"},
		{"usd max int64", math.MaxInt64, usd, "92233720368547758.07", "USD 92233720368547758.07"},
		{"usd min int64", math.MinInt64, usd, "-92233720368547758.08", "USD -92233720368547758.08"},
		{"vnd min int64", math.MinInt64, vnd, "-9223372036854775808", "VND -9223372036854775808"},
		{"zero currency", 5, Currency{}, "5", "5"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := FromMinor(tt.in, tt.cur)
			if got := a.String(); got != tt.wantStr {
				t.Errorf("String() = %q, want %q", got, tt.wantStr)
			}
			if got := a.Format(); got != tt.wantFormat {
				t.Errorf("Format() = %q, want %q", got, tt.wantFormat)
			}
		})
	}
}

func TestZeroValueAmount(t *testing.T) {
	var z Amount
	if !z.IsZero() {
		t.Error("zero Amount.IsZero() = false, want true")
	}
	if z.Minor() != 0 {
		t.Errorf("zero Amount.Minor() = %d, want 0", z.Minor())
	}
	if z.String() != "0" {
		t.Errorf("zero Amount.String() = %q, want \"0\"", z.String())
	}
	if z.Format() != "0" {
		t.Errorf("zero Amount.Format() = %q, want \"0\"", z.Format())
	}
	if z.Currency().Code() != "" || z.Currency().Exponent() != 0 {
		t.Errorf("zero Amount.Currency() = %q/%d, want \"\"/0", z.Currency().Code(), z.Currency().Exponent())
	}
	sum, err := z.Add(z)
	if err != nil || !sum.IsZero() {
		t.Errorf("zero.Add(zero) = %v, %v, want 0, nil", sum, err)
	}
	if _, err := z.Add(FromMinor(1, usd)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("zero.Add(USD) err = %v, want ErrCurrencyMismatch", err)
	}
}

func TestStringRoundTrip(t *testing.T) {
	minors := []int64{0, 1, -1, 5, 99, 1234, -1234, 123456789, math.MaxInt64, math.MinInt64}
	curs := []Currency{usd, vnd, jpy}
	for _, c := range curs {
		for _, m := range minors {
			a := FromMinor(m, c)
			got, err := FromMajorString(a.String(), c)
			if err != nil {
				t.Fatalf("FromMajorString(%q, %v) unexpected err %v", a.String(), c, err)
			}
			if !got.Equal(a) {
				t.Fatalf("round trip %v: got %d, want %d", c, got.Minor(), m)
			}
		}
	}
}

func TestRoundModeString(t *testing.T) {
	tests := []struct {
		mode RoundMode
		want string
	}{
		{RoundHalfUp, "HalfUp"},
		{RoundHalfEven, "HalfEven"},
		{RoundHalfDown, "HalfDown"},
		{RoundUp, "Up"},
		{RoundDown, "Down"},
		{RoundMode(99), "RoundMode(99)"},
	}
	for _, tt := range tests {
		if got := tt.mode.String(); got != tt.want {
			t.Errorf("RoundMode(%d).String() = %q, want %q", int(tt.mode), got, tt.want)
		}
	}
}
