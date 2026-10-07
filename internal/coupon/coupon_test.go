package coupon

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// testNow is a fixed "now" so validity-window tests are deterministic.
var testNow = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)

// defaultCoupon returns a valid, stackable 10%-off coupon.
func defaultCoupon() Coupon {
	return Coupon{
		Code:      "SAVE10",
		Type:      TypePercentOff,
		Value:     1000,
		Stackable: true,
		Active:    true,
	}
}

// fixedCoupon returns a valid, stackable fixed-amount coupon for USD.
func fixedCoupon(value int64) Coupon {
	return Coupon{
		Code:      "FLAT",
		Type:      TypeFixedAmountOff,
		Value:     value,
		Currency:  "USD",
		Stackable: true,
		Active:    true,
	}
}

// shippingCoupon returns a valid, stackable free-shipping coupon.
func shippingCoupon() Coupon {
	return Coupon{
		Code:      "FREESHIP",
		Type:      TypeFreeShipping,
		Stackable: true,
		Active:    true,
	}
}

// makeInput builds a USD input with the given lines and subtotal.
func makeInput(subtotal int64, lines ...Line) Input {
	return Input{
		Lines:      lines,
		Subtotal:   subtotal,
		Currency:   "USD",
		CustomerID: "cust-1",
	}
}

// qtyLine builds a line with shared product/plan/SKU codes.
func qtyLine(qty int, unit int64) Line {
	return Line{SKU: "SKU-1", ProductCode: "prod-1", PlanCode: "plan-1", Quantity: qty, UnitAmount: unit}
}

// codedLine builds a line with the given codes.
func codedLine(sku, product, plan string, qty int, unit int64) Line {
	return Line{SKU: sku, ProductCode: product, PlanCode: plan, Quantity: qty, UnitAmount: unit}
}

func TestNormalizeCode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "SAVE10", "SAVE10"},
		{"lowercase", "save10", "SAVE10"},
		{"mixed", "SaVe10", "SAVE10"},
		{"surrounding spaces", "  save10  ", "SAVE10"},
		{"tabs and newline", "\tcode\n", "CODE"},
		{"empty", "", ""},
		{"only spaces", "   ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeCode(tt.in); got != tt.want {
				t.Fatalf("NormalizeCode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMatchCode(t *testing.T) {
	c := Coupon{Code: "Save10"}
	tests := []struct {
		name string
		code string
		want bool
	}{
		{"exact", "Save10", true},
		{"lowercase", "save10", true},
		{"uppercase", "SAVE10", true},
		{"padded", "  SAVE10  ", true},
		{"different", "SAVE20", false},
		{"empty query", "", false},
		{"prefix", "SAVE", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchCode(c, tt.code); got != tt.want {
				t.Fatalf("MatchCode(%q) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
	if MatchCode(Coupon{Code: ""}, "") {
		t.Fatal("empty codes must never match")
	}
}

func TestFind(t *testing.T) {
	coupons := []Coupon{
		{Code: "SAVE10", Active: true},
		{Code: "  Big Deal ", Active: true},
	}
	c, err := Find(coupons, " save10 ")
	if err != nil {
		t.Fatalf("Find(save10) unexpected error: %v", err)
	}
	if c.Code != "SAVE10" {
		t.Fatalf("Find(save10) returned code %q", c.Code)
	}
	if _, err := Find(coupons, "BIG DEAL"); err != nil {
		t.Fatalf("Find(BIG DEAL) should match padded code, got %v", err)
	}
	_, err = Find(coupons, "nope")
	if !errors.Is(err, ErrCouponNotFound) {
		t.Fatalf("Find(nope) error = %v, want ErrCouponNotFound", err)
	}
}

func TestGenerateCode(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		code, err := GenerateCode()
		if err != nil {
			t.Fatalf("GenerateCode() error: %v", err)
		}
		if len(code) != codeLength {
			t.Fatalf("GenerateCode() = %q (len %d), want length %d", code, len(code), codeLength)
		}
		for _, r := range code {
			if !strings.ContainsRune(codeAlphabet, r) {
				t.Fatalf("GenerateCode() = %q contains %q outside alphabet %q", code, r, codeAlphabet)
			}
		}
		seen[code] = true
	}
	if len(seen) != 50 {
		t.Fatalf("expected 50 distinct codes, got %d", len(seen))
	}
}

func TestLineTotal(t *testing.T) {
	tests := []struct {
		name string
		line Line
		want int64
	}{
		{"basic", qtyLine(3, 100), 300},
		{"one unit", qtyLine(1, 250), 250},
		{"zero quantity", qtyLine(0, 250), 0},
		{"negative quantity", qtyLine(-2, 250), 0},
		{"zero amount", qtyLine(3, 0), 0},
		{"negative amount", qtyLine(3, -250), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.line.Total(); got != tt.want {
				t.Fatalf("Total() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Coupon, *Input)
		wantErr error
	}{
		{
			name:    "valid percent coupon",
			mutate:  nil,
			wantErr: nil,
		}, {
			name:    "valid fixed coupon",
			mutate:  func(c *Coupon, in *Input) { *c = fixedCoupon(500) },
			wantErr: nil,
		}, {
			name:    "valid free shipping coupon",
			mutate:  func(c *Coupon, in *Input) { *c = shippingCoupon() },
			wantErr: nil,
		}, {
			name:    "inactive",
			mutate:  func(c *Coupon, in *Input) { c.Active = false },
			wantErr: ErrCouponInactive,
		}, {
			name:    "expired",
			mutate:  func(c *Coupon, in *Input) { c.EndsAt = testNow.Add(-time.Hour) },
			wantErr: ErrCouponExpired,
		}, {
			name:    "expiry boundary is inclusive",
			mutate:  func(c *Coupon, in *Input) { c.EndsAt = testNow },
			wantErr: nil,
		}, {
			name:    "not started",
			mutate:  func(c *Coupon, in *Input) { c.StartsAt = testNow.Add(time.Hour) },
			wantErr: ErrCouponNotStarted,
		}, {
			name:    "start boundary is inclusive",
			mutate:  func(c *Coupon, in *Input) { c.StartsAt = testNow },
			wantErr: nil,
		}, {
			name:    "inside validity window",
			mutate:  func(c *Coupon, in *Input) { c.StartsAt = testNow.Add(-time.Hour); c.EndsAt = testNow.Add(time.Hour) },
			wantErr: nil,
		}, {
			name:    "minimum order not met",
			mutate:  func(c *Coupon, in *Input) { c.MinimumOrderAmount = 20000 },
			wantErr: ErrMinimumOrderNotMet,
		}, {
			name:    "minimum order exactly met",
			mutate:  func(c *Coupon, in *Input) { c.MinimumOrderAmount = 10000 },
			wantErr: nil,
		}, {
			name:    "minimum order exceeded",
			mutate:  func(c *Coupon, in *Input) { c.MinimumOrderAmount = 9999 },
			wantErr: nil,
		}, {
			name:    "max redemptions exceeded",
			mutate:  func(c *Coupon, in *Input) { c.MaxRedemptions = 5; c.TimesRedeemed = 5 },
			wantErr: ErrMaxRedemptionsExceeded,
		}, {
			name:    "max redemptions one left",
			mutate:  func(c *Coupon, in *Input) { c.MaxRedemptions = 5; c.TimesRedeemed = 4 },
			wantErr: nil,
		}, {
			name:    "max redemptions unlimited",
			mutate:  func(c *Coupon, in *Input) { c.MaxRedemptions = 0; c.TimesRedeemed = 999 },
			wantErr: nil,
		}, {
			name:    "customer redemption limit reached",
			mutate:  func(c *Coupon, in *Input) { c.MaxRedemptionsPerCustomer = 2; in.CustomerRedemptions = 2 },
			wantErr: ErrCustomerRedemptionLimit,
		}, {
			name:    "customer redemption limit one left",
			mutate:  func(c *Coupon, in *Input) { c.MaxRedemptionsPerCustomer = 2; in.CustomerRedemptions = 1 },
			wantErr: nil,
		}, {
			name:    "customer redemption limit unlimited",
			mutate:  func(c *Coupon, in *Input) { c.MaxRedemptionsPerCustomer = 0; in.CustomerRedemptions = 50 },
			wantErr: nil,
		}, {
			name:    "fixed currency mismatch",
			mutate:  func(c *Coupon, in *Input) { *c = fixedCoupon(500); c.Currency = "EUR" },
			wantErr: ErrCurrencyMismatch,
		}, {
			name:    "fixed coupon missing currency",
			mutate:  func(c *Coupon, in *Input) { *c = fixedCoupon(500); c.Currency = "" },
			wantErr: ErrCurrencyMismatch,
		}, {
			name:    "currency match is case-insensitive",
			mutate:  func(c *Coupon, in *Input) { *c = fixedCoupon(500); c.Currency = "usd" },
			wantErr: nil,
		}, {
			name:    "percent coupon currency mismatch",
			mutate:  func(c *Coupon, in *Input) { c.Currency = "EUR" },
			wantErr: ErrCurrencyMismatch,
		}, {
			name:    "percent coupon without currency is wildcard",
			mutate:  func(c *Coupon, in *Input) { c.Currency = "" },
			wantErr: nil,
		}, {
			name:    "product not applicable",
			mutate:  func(c *Coupon, in *Input) { c.AppliesTo = []string{"prod-x"} },
			wantErr: ErrProductNotApplicable,
		}, {
			name:    "product code match",
			mutate:  func(c *Coupon, in *Input) { c.AppliesTo = []string{"prod-a"} },
			wantErr: nil,
		}, {
			name:    "plan code match",
			mutate:  func(c *Coupon, in *Input) { c.AppliesTo = []string{"plan-b"} },
			wantErr: nil,
		}, {
			name:    "sku match",
			mutate:  func(c *Coupon, in *Input) { c.AppliesTo = []string{"SKU-A"} },
			wantErr: nil,
		}, {
			name:    "match is case-insensitive",
			mutate:  func(c *Coupon, in *Input) { c.AppliesTo = []string{"PROD-A"} },
			wantErr: nil,
		}, {
			name:    "one of many codes matches",
			mutate:  func(c *Coupon, in *Input) { c.AppliesTo = []string{"prod-x", "prod-a"} },
			wantErr: nil,
		}, {
			name:    "percent value above 100 percent",
			mutate:  func(c *Coupon, in *Input) { c.Value = PercentBase + 1 },
			wantErr: ErrInvalidCouponValue,
		}, {
			name:    "percent value zero",
			mutate:  func(c *Coupon, in *Input) { c.Value = 0 },
			wantErr: ErrInvalidCouponValue,
		}, {
			name:    "negative value",
			mutate:  func(c *Coupon, in *Input) { c.Value = -1 },
			wantErr: ErrInvalidCouponValue,
		}, {
			name:    "unknown type",
			mutate:  func(c *Coupon, in *Input) { c.Type = "bogus" },
			wantErr: ErrInvalidCouponType,
		}, {
			name:    "negative minimum order",
			mutate:  func(c *Coupon, in *Input) { c.MinimumOrderAmount = -1 },
			wantErr: ErrInvalidCouponValue,
		}, {
			name:    "negative max redemptions",
			mutate:  func(c *Coupon, in *Input) { c.MaxRedemptions = -1 },
			wantErr: ErrInvalidCouponValue,
		}, {
			name:    "negative per-customer max",
			mutate:  func(c *Coupon, in *Input) { c.MaxRedemptionsPerCustomer = -1 },
			wantErr: ErrInvalidCouponValue,
		}, {
			name:    "negative times redeemed",
			mutate:  func(c *Coupon, in *Input) { c.TimesRedeemed = -1 },
			wantErr: ErrInvalidCouponValue,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := defaultCoupon()
			in := makeInput(10000,
				codedLine("SKU-A", "prod-a", "plan-a", 1, 5000),
				codedLine("SKU-B", "prod-b", "plan-b", 1, 5000),
			)
			if tt.mutate != nil {
				tt.mutate(&c, &in)
			}
			err := Validate(c, in, testNow)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateMinimumOrderMessageHasAmounts(t *testing.T) {
	c := defaultCoupon()
	c.MinimumOrderAmount = 25000
	in := makeInput(10000, qtyLine(1, 10000))
	err := Validate(c, in, testNow)
	if !errors.Is(err, ErrMinimumOrderNotMet) {
		t.Fatalf("Validate() = %v, want ErrMinimumOrderNotMet", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "25000") || !strings.Contains(msg, "10000") {
		t.Fatalf("error %q must contain both the required (25000) and actual (10000) amounts", msg)
	}
}

func TestApplyPercentMath(t *testing.T) {
	tests := []struct {
		name     string
		bps      int64
		subtotal int64
		unit     int64
		wantDisc int64
		wantNew  int64
		wantRate int64
	}{
		{name: "ten percent of 10000", bps: 1000, subtotal: 10000, unit: 10000, wantDisc: 1000, wantNew: 9000, wantRate: 1000},
		{name: "half up of 499.5", bps: 5000, subtotal: 999, unit: 999, wantDisc: 500, wantNew: 499, wantRate: 5000},
		{name: "half up of 0.5", bps: 5000, subtotal: 1, unit: 1, wantDisc: 1, wantNew: 0, wantRate: 5000},
		{name: "half up of 0.5 on 5", bps: 1000, subtotal: 5, unit: 5, wantDisc: 1, wantNew: 4, wantRate: 1000},
		{name: "0.75 rounds up", bps: 2500, subtotal: 3, unit: 3, wantDisc: 1, wantNew: 2, wantRate: 2500},
		{name: "0.25 rounds down", bps: 2500, subtotal: 1, unit: 1, wantDisc: 0, wantNew: 1, wantRate: 2500},
		{name: "one hundred percent off", bps: PercentBase, subtotal: 10000, unit: 10000, wantDisc: 10000, wantNew: 0, wantRate: PercentBase},
		{name: "awkward rate and amount", bps: 1234, subtotal: 7777, unit: 7777, wantDisc: 960, wantNew: 6817, wantRate: 1234},
		{name: "zero subtotal", bps: 1000, subtotal: 0, unit: 0, wantDisc: 0, wantNew: 0, wantRate: 1000},
		{name: "fifty percent of one hundred", bps: 5000, subtotal: 100, unit: 100, wantDisc: 50, wantNew: 50, wantRate: 5000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := defaultCoupon()
			c.Value = tt.bps
			in := makeInput(tt.subtotal, qtyLine(1, tt.unit))
			res, err := Apply(c, in, testNow)
			if err != nil {
				t.Fatalf("Apply() error: %v", err)
			}
			checkTotals(t, res, tt.wantDisc, tt.wantNew)
			if res.AppliedRateBps != tt.wantRate {
				t.Fatalf("AppliedRateBps = %d, want %d", res.AppliedRateBps, tt.wantRate)
			}
			checkAllocation(t, in, res)
		})
	}
}

func TestApplyFixedMath(t *testing.T) {
	tests := []struct {
		name     string
		value    int64
		subtotal int64
		lines    []Line
		wantDisc int64
		wantNew  int64
	}{
		{
			name:     "simple subtraction",
			value:    2500,
			subtotal: 10000,
			lines:    []Line{qtyLine(2, 5000)},
			wantDisc: 2500,
			wantNew:  7500,
		}, {
			name:     "fixed equals subtotal",
			value:    10000,
			subtotal: 10000,
			lines:    []Line{qtyLine(1, 10000)},
			wantDisc: 10000,
			wantNew:  0,
		}, {
			name:     "fixed greater than subtotal is capped",
			value:    15000,
			subtotal: 10000,
			lines:    []Line{qtyLine(1, 10000)},
			wantDisc: 10000,
			wantNew:  0,
		}, {
			name:     "fixed greater than line value is capped",
			value:    1000,
			subtotal: 2000,
			lines:    []Line{codedLine("SKU-A", "prod-a", "plan-a", 1, 500), codedLine("SKU-B", "prod-b", "plan-b", 1, 1500)},
			wantDisc: 500, // appliesTo prod-a only: never more than the lines it applies to
			wantNew:  1500,
		}, {
			name:     "zero subtotal yields zero discount",
			value:    100,
			subtotal: 0,
			lines:    []Line{qtyLine(1, 0)},
			wantDisc: 0,
			wantNew:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fixedCoupon(tt.value)
			if tt.name == "fixed greater than line value is capped" {
				c.AppliesTo = []string{"prod-a"}
			}
			in := makeInput(tt.subtotal, tt.lines...)
			res, err := Apply(c, in, testNow)
			if err != nil {
				t.Fatalf("Apply() error: %v", err)
			}
			checkTotals(t, res, tt.wantDisc, tt.wantNew)
			checkAllocation(t, in, res)
		})
	}
}

func TestApplyFreeShipping(t *testing.T) {
	in := makeInput(10000, qtyLine(1, 10000))
	res, err := Apply(shippingCoupon(), in, testNow)
	if err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	checkTotals(t, res, 0, 10000)
	if !res.FreeShipping {
		t.Fatal("FreeShipping must be set")
	}
	if res.AppliedRateBps != 0 {
		t.Fatalf("AppliedRateBps = %d, want 0", res.AppliedRateBps)
	}
	checkAllocation(t, in, res)
}

func TestApplyReturnsValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Coupon, *Input)
		wantErr error
	}{
		{"inactive", func(c *Coupon, in *Input) { c.Active = false }, ErrCouponInactive},
		{"expired", func(c *Coupon, in *Input) { c.EndsAt = testNow.Add(-time.Minute) }, ErrCouponExpired},
		{"not started", func(c *Coupon, in *Input) { c.StartsAt = testNow.Add(time.Minute) }, ErrCouponNotStarted},
		{"min order", func(c *Coupon, in *Input) { c.MinimumOrderAmount = 20000 }, ErrMinimumOrderNotMet},
		{"max redemptions", func(c *Coupon, in *Input) { c.MaxRedemptions = 1; c.TimesRedeemed = 1 }, ErrMaxRedemptionsExceeded},
		{"customer limit", func(c *Coupon, in *Input) { c.MaxRedemptionsPerCustomer = 1; in.CustomerRedemptions = 1 }, ErrCustomerRedemptionLimit},
		{"currency", func(c *Coupon, in *Input) { *c = fixedCoupon(100); c.Currency = "EUR" }, ErrCurrencyMismatch},
		{"product", func(c *Coupon, in *Input) { c.AppliesTo = []string{"prod-x"} }, ErrProductNotApplicable},
		{"invalid value", func(c *Coupon, in *Input) { c.Value = 0 }, ErrInvalidCouponValue},
		{"unknown type", func(c *Coupon, in *Input) { c.Type = "nope" }, ErrInvalidCouponType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := defaultCoupon()
			in := makeInput(10000, qtyLine(1, 10000))
			tt.mutate(&c, &in)
			res, err := Apply(c, in, testNow)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Apply() error = %v, want %v", err, tt.wantErr)
			}
			if res.DiscountAmount != 0 || res.Applied != nil {
				t.Fatalf("Apply() must return a zero Result on error, got %+v", res)
			}
		})
	}
}

func TestApplyLineAllocation(t *testing.T) {
	tests := []struct {
		name       string
		coupon     func() Coupon
		in         Input
		wantDisc   int64
		wantAllocs []int64
	}{
		{
			name:       "equal lines largest remainder tie broken by order",
			coupon:     func() Coupon { return fixedCoupon(100) },
			in:         makeInput(300, qtyLine(1, 100), qtyLine(1, 100), qtyLine(1, 100)),
			wantDisc:   100,
			wantAllocs: []int64{34, 33, 33},
		}, {
			name:       "proportional with largest remainder boost",
			coupon:     func() Coupon { return fixedCoupon(100) },
			in:         makeInput(600, qtyLine(1, 100), qtyLine(1, 200), qtyLine(1, 300)),
			wantDisc:   100,
			wantAllocs: []int64{17, 33, 50},
		}, {
			name:       "percent over odd line totals",
			coupon:     func() Coupon { c := defaultCoupon(); c.Value = 3333; return c },
			in:         makeInput(303, qtyLine(1, 101), qtyLine(1, 101), qtyLine(1, 101)),
			wantDisc:   101,
			wantAllocs: []int64{34, 34, 33},
		}, {
			name:       "zero-value line gets nothing",
			coupon:     func() Coupon { return fixedCoupon(30) },
			in:         makeInput(300, qtyLine(1, 100), qtyLine(1, 0), qtyLine(1, 200)),
			wantDisc:   30,
			wantAllocs: []int64{10, 0, 20},
		}, {
			name: "only eligible lines are discounted",
			coupon: func() Coupon {
				c := defaultCoupon() // 10% off prod-a only
				c.AppliesTo = []string{"prod-a"}
				return c
			},
			in: makeInput(2000,
				codedLine("SKU-A", "prod-a", "plan-a", 1, 500),
				codedLine("SKU-B", "prod-b", "plan-b", 1, 1500),
			),
			wantDisc:   50,
			wantAllocs: []int64{50, 0},
		}, {
			name:       "full discount across lines",
			coupon:     func() Coupon { c := defaultCoupon(); c.Value = PercentBase; return c },
			in:         makeInput(250, qtyLine(1, 100), qtyLine(1, 150)),
			wantDisc:   250,
			wantAllocs: []int64{100, 150},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := Apply(tt.coupon(), tt.in, testNow)
			if err != nil {
				t.Fatalf("Apply() error: %v", err)
			}
			if res.DiscountAmount != tt.wantDisc {
				t.Fatalf("DiscountAmount = %d, want %d", res.DiscountAmount, tt.wantDisc)
			}
			if len(res.LineAllocations) != len(tt.wantAllocs) {
				t.Fatalf("got %d allocations, want %d", len(res.LineAllocations), len(tt.wantAllocs))
			}
			for i, want := range tt.wantAllocs {
				if got := res.LineAllocations[i].Discount; got != want {
					t.Fatalf("allocation[%d] = %d, want %d (all: %+v)", i, got, want, res.LineAllocations)
				}
			}
			checkAllocation(t, tt.in, res)
		})
	}
}

func TestApplyStackCombinesStackableCoupons(t *testing.T) {
	c1 := defaultCoupon() // 10% off
	c2 := fixedCoupon(500)
	in := makeInput(10000, qtyLine(1, 5000), qtyLine(1, 5000))

	res, err := ApplyStack([]Coupon{c1, c2}, in, testNow)
	if err != nil {
		t.Fatalf("ApplyStack() error: %v", err)
	}
	checkTotals(t, res, 1500, 8500)
	if res.AppliedRateBps != 1000 {
		t.Fatalf("AppliedRateBps = %d, want 1000", res.AppliedRateBps)
	}
	if len(res.Applied) != 2 {
		t.Fatalf("got %d applied coupons, want 2", len(res.Applied))
	}
	if res.Applied[0].DiscountAmount != 1000 || res.Applied[1].DiscountAmount != 500 {
		t.Fatalf("applied amounts = %+v, want [1000 500]", res.Applied)
	}
	checkAllocation(t, in, res)
}

func TestApplyStackNonStackableRejected(t *testing.T) {
	in := makeInput(10000, qtyLine(1, 10000))
	nonStackable := fixedCoupon(500)
	nonStackable.Stackable = false

	for _, order := range [][]Coupon{
		{nonStackable, defaultCoupon()},
		{defaultCoupon(), nonStackable},
		{nonStackable, nonStackable},
	} {
		_, err := ApplyStack(order, in, testNow)
		if !errors.Is(err, ErrCouponNotStackable) {
			t.Fatalf("ApplyStack(%v) error = %v, want ErrCouponNotStackable", codesOf(order), err)
		}
	}

	// A single non-stackable coupon is fine.
	res, err := ApplyStack([]Coupon{nonStackable}, in, testNow)
	if err != nil {
		t.Fatalf("ApplyStack(single non-stackable) error: %v", err)
	}
	checkTotals(t, res, 500, 9500)
}

func TestApplyStackEmpty(t *testing.T) {
	_, err := ApplyStack(nil, makeInput(10000, qtyLine(1, 10000)), testNow)
	if !errors.Is(err, ErrNoCoupons) {
		t.Fatalf("ApplyStack(nil) error = %v, want ErrNoCoupons", err)
	}
}

func TestApplyStackCappedAtSubtotal(t *testing.T) {
	c1 := defaultCoupon()
	c1.Value = 6000 // 60%
	c2 := defaultCoupon()
	c2.Code = "SAVE70"
	c2.Value = 7000 // 70%
	in := makeInput(10000, qtyLine(1, 10000))

	res, err := ApplyStack([]Coupon{c1, c2}, in, testNow)
	if err != nil {
		t.Fatalf("ApplyStack() error: %v", err)
	}
	checkTotals(t, res, 10000, 0)
	// Budget is consumed in order: first coupon full 6000, second reduced to 4000.
	if len(res.Applied) != 2 || res.Applied[0].DiscountAmount != 6000 || res.Applied[1].DiscountAmount != 4000 {
		t.Fatalf("applied amounts = %+v, want [6000 4000]", res.Applied)
	}
	if res.AppliedRateBps != PercentBase {
		t.Fatalf("AppliedRateBps = %d, want %d (capped)", res.AppliedRateBps, PercentBase)
	}
	checkAllocation(t, in, res)
}

func TestApplyStackMixedApplicabilityAllocation(t *testing.T) {
	c1 := defaultCoupon() // 10% off prod-a
	c1.AppliesTo = []string{"prod-a"}
	c2 := fixedCoupon(600)
	c2.AppliesTo = []string{"prod-b"}
	in := makeInput(4000,
		codedLine("SKU-A", "prod-a", "plan-a", 1, 1000),
		codedLine("SKU-B", "prod-b", "plan-b", 1, 3000),
	)

	res, err := ApplyStack([]Coupon{c1, c2}, in, testNow)
	if err != nil {
		t.Fatalf("ApplyStack() error: %v", err)
	}
	checkTotals(t, res, 700, 3300) // 100 (10% of 1000) + 600
	want := []int64{175, 525}
	for i, w := range want {
		if got := res.LineAllocations[i].Discount; got != w {
			t.Fatalf("allocation[%d] = %d, want %d (all: %+v)", i, got, w, res.LineAllocations)
		}
	}
	checkAllocation(t, in, res)
}

func TestApplyStackFreeShippingWithPercent(t *testing.T) {
	in := makeInput(10000, qtyLine(1, 10000))
	res, err := ApplyStack([]Coupon{shippingCoupon(), defaultCoupon()}, in, testNow)
	if err != nil {
		t.Fatalf("ApplyStack() error: %v", err)
	}
	checkTotals(t, res, 1000, 9000)
	if !res.FreeShipping {
		t.Fatal("FreeShipping must be set for a stack containing a free-shipping coupon")
	}
}

func TestApplyStackValidatesEveryCoupon(t *testing.T) {
	in := makeInput(10000, qtyLine(1, 10000))
	expired := fixedCoupon(500)
	expired.EndsAt = testNow.Add(-time.Hour)

	_, err := ApplyStack([]Coupon{defaultCoupon(), expired}, in, testNow)
	if !errors.Is(err, ErrCouponExpired) {
		t.Fatalf("ApplyStack() error = %v, want ErrCouponExpired", err)
	}

	_, err = ApplyStack([]Coupon{expired, defaultCoupon()}, in, testNow)
	if !errors.Is(err, ErrCouponExpired) {
		t.Fatalf("ApplyStack() error = %v, want ErrCouponExpired", err)
	}

	// The stacking check runs before validation.
	nonStackable := defaultCoupon()
	nonStackable.Stackable = false
	_, err = ApplyStack([]Coupon{nonStackable, expired}, in, testNow)
	if !errors.Is(err, ErrCouponNotStackable) {
		t.Fatalf("ApplyStack() error = %v, want ErrCouponNotStackable (stacking checked first)", err)
	}
}

func TestApplyStackCannotAllocate(t *testing.T) {
	in := Input{Subtotal: 5000, Currency: "USD", CustomerID: "cust-1"} // no lines
	_, err := ApplyStack([]Coupon{fixedCoupon(1000), fixedCoupon(500)}, in, testNow)
	if !errors.Is(err, ErrCannotAllocateDiscount) {
		t.Fatalf("ApplyStack() error = %v, want ErrCannotAllocateDiscount", err)
	}
}

func TestApplyEdgeCases(t *testing.T) {
	t.Run("fixed greater than subtotal never goes negative", func(t *testing.T) {
		in := makeInput(300, qtyLine(1, 300))
		res, err := Apply(fixedCoupon(999999), in, testNow)
		if err != nil {
			t.Fatalf("Apply() error: %v", err)
		}
		checkTotals(t, res, 300, 0)
		checkAllocation(t, in, res)
	})

	t.Run("one hundred percent off zeroes the order", func(t *testing.T) {
		c := defaultCoupon()
		c.Value = PercentBase
		in := makeInput(12345, qtyLine(3, 4115))
		res, err := Apply(c, in, testNow)
		if err != nil {
			t.Fatalf("Apply() error: %v", err)
		}
		checkTotals(t, res, 12345, 0)
		checkAllocation(t, in, res)
	})

	t.Run("zero subtotal yields zero discount", func(t *testing.T) {
		in := makeInput(0, qtyLine(1, 0))
		res, err := Apply(defaultCoupon(), in, testNow)
		if err != nil {
			t.Fatalf("Apply() error: %v", err)
		}
		checkTotals(t, res, 0, 0)
		checkAllocation(t, in, res)
	})

	t.Run("positive discount without positive lines is rejected", func(t *testing.T) {
		in := Input{Subtotal: 5000, Currency: "USD", CustomerID: "cust-1"}
		_, err := Apply(fixedCoupon(1000), in, testNow)
		if !errors.Is(err, ErrCannotAllocateDiscount) {
			t.Fatalf("Apply() error = %v, want ErrCannotAllocateDiscount", err)
		}
	})

	t.Run("per-customer limit is checked per coupon in a stack", func(t *testing.T) {
		c := defaultCoupon()
		c.MaxRedemptionsPerCustomer = 1
		in := makeInput(10000, qtyLine(1, 10000))
		in.CustomerRedemptions = 1
		_, err := ApplyStack([]Coupon{c, fixedCoupon(100)}, in, testNow)
		if !errors.Is(err, ErrCustomerRedemptionLimit) {
			t.Fatalf("ApplyStack() error = %v, want ErrCustomerRedemptionLimit", err)
		}
	})
}

func TestCaseInsensitiveCodeMatching(t *testing.T) {
	c := defaultCoupon()
	c.Code = "Save10"
	in := makeInput(10000, qtyLine(1, 10000))

	if _, err := Find([]Coupon{c}, "  save10 "); err != nil {
		t.Fatalf("Find with different case failed: %v", err)
	}

	// AppliesTo matching is case-insensitive too.
	c.AppliesTo = []string{"PROD-1"}
	if err := Validate(c, in, testNow); err != nil {
		t.Fatalf("Validate with case-different AppliesTo failed: %v", err)
	}

	// Currency matching is case-insensitive.
	fx := fixedCoupon(100)
	fx.Currency = "usd"
	if err := Validate(fx, in, testNow); err != nil {
		t.Fatalf("Validate with case-different currency failed: %v", err)
	}
}

// checkTotals asserts the headline result numbers and internal consistency.
func checkTotals(t *testing.T, res Result, wantDisc, wantNew int64) {
	t.Helper()
	if res.DiscountAmount != wantDisc {
		t.Fatalf("DiscountAmount = %d, want %d", res.DiscountAmount, wantDisc)
	}
	if res.NewSubtotal != wantNew {
		t.Fatalf("NewSubtotal = %d, want %d", res.NewSubtotal, wantNew)
	}
	if res.TaxableSubtotal != wantNew {
		t.Fatalf("TaxableSubtotal = %d, want %d", res.TaxableSubtotal, wantNew)
	}
	if wantNew < 0 {
		t.Fatalf("NewSubtotal must never be negative, got %d", wantNew)
	}
}

// checkAllocation asserts that per-line allocations sum exactly to the
// discount (no lost pennies) and never exceed their line total.
func checkAllocation(t *testing.T, in Input, res Result) {
	t.Helper()
	if len(res.LineAllocations) != len(in.Lines) {
		t.Fatalf("got %d allocations for %d lines", len(res.LineAllocations), len(in.Lines))
	}
	var sum int64
	for _, a := range res.LineAllocations {
		sum += a.Discount
		if a.Discount < 0 {
			t.Fatalf("negative allocation: %+v", a)
		}
		if a.Discount > a.LineTotal {
			t.Fatalf("allocation %+v exceeds its line total", a)
		}
		if a.LineTotalAfterDiscount != a.LineTotal-a.Discount {
			t.Fatalf("LineTotalAfterDiscount mismatch: %+v", a)
		}
	}
	if sum != res.DiscountAmount {
		t.Fatalf("allocations sum to %d, want DiscountAmount %d (lost pennies!)", sum, res.DiscountAmount)
	}
}

// codesOf renders coupon codes for failure messages.
func codesOf(coupons []Coupon) []string {
	out := make([]string, len(coupons))
	for i, c := range coupons {
		out[i] = c.Code
	}
	return out
}
