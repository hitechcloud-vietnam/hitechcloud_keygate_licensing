package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// TestMrrNormalizeMonthly pins the §42 normalization math — integer
// only, never float64 (money rule §51):
//
//	month → as-is
//	year  → (amount + 6) / 12   = amount/12 rounded half-up
//	anything else → excluded (ok=false), never counted at 0
//
// The .5 case is pinned exactly: 1206/12 = 100.5 must round UP to
// 101; 1205/12 = 100.4167 must stay 100.
func TestMrrNormalizeMonthly(t *testing.T) {
	for _, tc := range []struct {
		amount   int64
		interval string
		want     int64
		ok       bool
	}{
		{1000, "month", 1000, true},
		{0, "month", 0, true},
		{1200, "year", 100, true},
		{1206, "year", 101, true}, // 100.5 rounds half-up → 101
		{1205, "year", 100, true}, // 100.4167 rounds down → 100
		{6, "year", 1, true},      // 0.5 rounds half-up → 1
		{1, "year", 0, true},
		{1200, "quarter", 0, false},
		{1200, "week", 0, false},
		{1200, "", 0, false},
		{1200, "metered", 0, false},
	} {
		got, ok := mrrNormalizeMonthly(tc.amount, tc.interval)
		if got != tc.want || ok != tc.ok {
			t.Errorf("mrrNormalizeMonthly(%d, %q) = (%d, %v), want (%d, %v)",
				tc.amount, tc.interval, got, ok, tc.want, tc.ok)
		}
	}
}

// TestMrrArrIsTimesTwelve pins ARR = MRR × 12, integer math.
func TestMrrArrIsTimesTwelve(t *testing.T) {
	for _, tc := range []struct{ mrr, want int64 }{
		{0, 0}, {100, 1200}, {1234567, 14814804},
	} {
		if got := mrrArr(tc.mrr); got != tc.want {
			t.Errorf("mrrArr(%d) = %d, want %d", tc.mrr, got, tc.want)
		}
	}
}

// TestMrrValidateSeries pins the window rules to the reports ones:
// grain day|week|month, from ≤ to, span ≤ 3 years.
func TestMrrValidateSeries(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)

	if err := mrrValidateSeries(from, to, "day"); err != nil {
		t.Errorf("valid window: %v", err)
	}
	if err := mrrValidateSeries(from, to, "hour"); !errors.Is(err, ErrInvalidGroupBy) {
		t.Errorf("bad grain: err = %v, want ErrInvalidGroupBy", err)
	}
	if err := mrrValidateSeries(to, from, "day"); !errors.Is(err, ErrInvalidReportRange) {
		t.Errorf("from after to: err = %v, want ErrInvalidReportRange", err)
	}
	long := from.AddDate(ReportMaxSpanYears, 0, 1)
	if err := mrrValidateSeries(from, long, "day"); !errors.Is(err, ErrInvalidReportRange) {
		t.Errorf("span over cap: err = %v, want ErrInvalidReportRange", err)
	}
}

// TestMrrCurrencyLabel pins the snapshot currency collapse.
func TestMrrCurrencyLabel(t *testing.T) {
	if got := mrrCurrencyLabel(map[string]struct{}{}); got != "" {
		t.Errorf("empty = %q, want \"\"", got)
	}
	if got := mrrCurrencyLabel(map[string]struct{}{"VND": {}}); got != "VND" {
		t.Errorf("single = %q, want VND", got)
	}
	if got := mrrCurrencyLabel(map[string]struct{}{"VND": {}, "USD": {}}); got != "mixed" {
		t.Errorf("plural = %q, want mixed", got)
	}
}

// TestMrrQueryShape pins the SQL the two metrics run without needing
// a database (query-shape doctrine, bun_alias_test.go): the price
// source prefers the §53 plan_prices row (default first) and falls
// back to the last PAID order, the active-at-instant predicate is the
// documented one, and the series query reads the bucket-start column
// (b.ts) rather than binding an instant.
func TestMrrQueryShape(t *testing.T) {
	_ = bun.NewDB(nil, pgdialect.New())

	point := fmtSprintfPrice("?")
	for _, want := range []string{
		"plan_prices pp2", "pp2.is_default DESC", "pp2.plan_id = p.id",
		"FROM orders o", "o.license_id = s.license_id",
		"o.status IN ('paid', 'partially_refunded')",
		"COALESCE(o.paid_at, o.created_at) <= ?",
	} {
		if !strings.Contains(point, want) {
			t.Errorf("price source missing %q in:\n%s", want, point)
		}
	}

	series := fmtSprintfPrice("b.ts")
	if !strings.Contains(series, "COALESCE(o.paid_at, o.created_at) <= b.ts") {
		t.Errorf("series price source must read the bucket start, got:\n%s", series)
	}

	for _, want := range []string{
		"s.status IN ('active', 'past_due')",
		"s.created_at <= ?",
		"s.canceled_at IS NULL OR s.canceled_at > ?",
		"p.billing_interval IN ('month', 'year')",
		"price.amount_minor IS NOT NULL",
	} {
		if !strings.Contains(mrrActiveWhere, want) {
			t.Errorf("active predicate missing %q in:\n%s", want, mrrActiveWhere)
		}
	}

	// The point query binds the instant exactly three times (price
	// instant + the two active-at checks).
	if n := strings.Count(point+mrrActiveWhere, "?"); n != 3 {
		t.Errorf("point query binds %d placeholders, want 3", n)
	}
}

// fmtSprintfPrice renders the shared price lateral with the given
// snapshot-instant fragment exactly as the metrics do.
func fmtSprintfPrice(instant string) string {
	return fmt.Sprintf(mrrPriceLateral, instant)
}

// TestMrrEmptySnapshot pins the degenerate answer: no recurring
// subscriptions → 0/0 and an empty currency label, and ARR always
// equals MRR × 12.
func TestMrrEmptySnapshot(t *testing.T) {
	s := reviewsTestDB(t)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()

	snap, err := s.MRR(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("MRR: %v", err)
	}
	if snap.MRRMinor != 0 || snap.ARRMinor != 0 || snap.Currency != "" {
		t.Errorf("empty snapshot = %+v, want 0/0/\"\"", snap)
	}
	if snap.ARRMinor != snap.MRRMinor*12 {
		t.Errorf("ARR %d != MRR %d × 12", snap.ARRMinor, snap.MRRMinor)
	}
}
