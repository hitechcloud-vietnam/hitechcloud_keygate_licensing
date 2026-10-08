package store_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ─── Reports (plan §43) — vocabulary and validation, database-free ───

// The plan §43 list is closed and exact: fourteen reports, plan order.
// A rename or a drop here is a contract change for the admin UI and
// the export columns, so it must be a deliberate edit of this list.
func TestReportTypesMatchPlanVocabulary(t *testing.T) {
	want := []string{
		"revenue", "sales", "subscriptions", "renewals", "churn",
		"licenses", "activations", "devices", "usage", "customers",
		"resellers", "affiliates", "refunds", "failed_payments",
	}
	if len(store.ReportTypes) != len(want) {
		t.Fatalf("ReportTypes = %v, want exactly %v", store.ReportTypes, want)
	}
	for i, w := range want {
		if store.ReportTypes[i] != w {
			t.Errorf("ReportTypes[%d] = %q, want %q", i, store.ReportTypes[i], w)
		}
		if !store.ValidReportType(w) {
			t.Errorf("ValidReportType(%q) = false, want true", w)
		}
	}
	for _, bad := range []string{"", "Revenue", "revenue ", "refunds2", "payouts"} {
		if store.ValidReportType(bad) {
			t.Errorf("ValidReportType(%q) = true, want false", bad)
		}
	}
}

func TestValidReportGroupBy(t *testing.T) {
	for _, g := range []string{"day", "week", "month"} {
		if !store.ValidReportGroupBy(g) {
			t.Errorf("ValidReportGroupBy(%q) = false, want true", g)
		}
	}
	for _, g := range []string{"", "year", "Day", "hour"} {
		if store.ValidReportGroupBy(g) {
			t.Errorf("ValidReportGroupBy(%q) = true, want false", g)
		}
	}
}

// Metric names are *_count or *_minor — except three pinned plan §43
// names (active_end, usage_events, usage_units) that predate the rule
// and are kept exactly as pinned. Any NEW name must follow the rule.
func TestReportMetricNameDiscipline(t *testing.T) {
	exempt := map[string]bool{"active_end": true, "usage_events": true, "usage_units": true}
	seenExempt := map[string]bool{}

	if len(store.ReportMetricNames) != len(store.ReportTypes) {
		t.Fatalf("ReportMetricNames covers %d reports, want %d", len(store.ReportMetricNames), len(store.ReportTypes))
	}
	for _, rt := range store.ReportTypes {
		names := store.ReportMetricNames[rt]
		if len(names) == 0 {
			t.Errorf("%s: no metrics", rt)
		}
		seen := map[string]bool{}
		for _, n := range names {
			if seen[n] {
				t.Errorf("%s: duplicate metric %q", rt, n)
			}
			seen[n] = true
			if exempt[n] {
				seenExempt[n] = true
				continue
			}
			if len(n) < 6 || (n[len(n)-6:] != "_count" && n[len(n)-6:] != "_minor") {
				t.Errorf("%s: metric %q must end in _count or _minor", rt, n)
			}
		}
	}
	for n := range exempt {
		if !seenExempt[n] {
			t.Errorf("pinned metric %q disappeared from every report", n)
		}
	}
}

func TestValidateReportParams(t *testing.T) {
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

	cases := []struct {
		name      string
		report    string
		groupBy   string
		from, to  time.Time
		wantError error
	}{
		{"valid day", "revenue", "day", day(2026, 6, 1), day(2026, 6, 30), nil},
		{"valid empty groupBy", "sales", "", day(2026, 6, 1), day(2026, 6, 30), nil},
		{"valid month", "usage", "month", day(2026, 1, 1), day(2026, 12, 31), nil},
		{"from equals to is one day", "revenue", "day", day(2026, 6, 1), day(2026, 6, 1), nil},
		{"span exactly three years", "revenue", "day", day(2023, 6, 1), day(2026, 6, 1), nil},
		{"list report ignores groupBy value only after whitelist", "resellers", "week", day(2026, 6, 1), day(2026, 6, 2), nil},

		{"unknown type", "revenue2", "day", day(2026, 6, 1), day(2026, 6, 2), store.ErrInvalidReportType},
		{"unknown groupBy", "revenue", "year", day(2026, 6, 1), day(2026, 6, 2), store.ErrInvalidGroupBy},
		{"from after to", "revenue", "day", day(2026, 6, 3), day(2026, 6, 2), store.ErrInvalidReportRange},
		{"span over three years", "revenue", "day", day(2023, 5, 31), day(2026, 6, 1), store.ErrInvalidReportRange},
		{"zero from", "revenue", "day", time.Time{}, day(2026, 6, 2), store.ErrInvalidReportRange},
		{"zero to", "revenue", "day", day(2026, 6, 1), time.Time{}, store.ErrInvalidReportRange},
	}
	for _, tc := range cases {
		err := store.ValidateReportParams(tc.report, tc.groupBy, tc.from, tc.to)
		if tc.wantError == nil {
			if err != nil {
				t.Errorf("%s: unexpected err %v", tc.name, err)
			}
			continue
		}
		if !errors.Is(err, tc.wantError) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.wantError)
		}
	}
}

// RunReport refuses bad input before it ever touches the database —
// this runs on a store with no DB at all.
func TestRunReportValidatesBeforeTouchingDB(t *testing.T) {
	s := &store.Store{}
	ctx := context.Background()
	from := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC)

	if _, err := s.RunReport(ctx, "nope", from, to, "day"); !errors.Is(err, store.ErrInvalidReportType) {
		t.Errorf("unknown type err = %v, want ErrInvalidReportType", err)
	}
	if _, err := s.RunReport(ctx, "revenue", from, to, "year"); !errors.Is(err, store.ErrInvalidGroupBy) {
		t.Errorf("bad groupBy err = %v, want ErrInvalidGroupBy", err)
	}
	if _, err := s.RunReport(ctx, "revenue", to, from, "day"); !errors.Is(err, store.ErrInvalidReportRange) {
		t.Errorf("inverted range err = %v, want ErrInvalidReportRange", err)
	}
}

// The JSON shape is {series: [{period, ...metrics}], totals}: metrics
// flatten beside the key and stay JSON integers — even past 2^53,
// which is where a float would silently lie about the money.
func TestReportRowJSONFlattensMetrics(t *testing.T) {
	const big = int64(9007199254740993) // 2^53 + 1
	row := store.ReportRow{
		Period:  "2026-06-01",
		Metrics: map[string]int64{"revenue_minor": big, "order_count": 2},
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // numbers as literals — float64 would round 2^53+1
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("unmarshal row: %v", err)
	}
	if _, nested := m["metrics"]; nested {
		t.Errorf("row carries a nested metrics object: %s", raw)
	}
	if m["period"] != "2026-06-01" {
		t.Errorf("period = %v, want 2026-06-01", m["period"])
	}
	if got, ok := m["revenue_minor"].(json.Number); !ok || got.String() != "9007199254740993" {
		t.Errorf("revenue_minor = %v, want 9007199254740993 (integer, exact)", m["revenue_minor"])
	}
	if got, ok := m["order_count"].(json.Number); !ok || got.String() != "2" {
		t.Errorf("order_count = %v, want 2", m["order_count"])
	}

	list := store.ReportRow{ResellerID: "r1", Metrics: map[string]int64{"gross_minor": 5}}
	raw, err = json.Marshal(list)
	if err != nil {
		t.Fatalf("marshal list row: %v", err)
	}
	dec = json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var lm map[string]any
	if err := dec.Decode(&lm); err != nil {
		t.Fatalf("unmarshal list row: %v", err)
	}
	if lm["reseller_id"] != "r1" {
		t.Errorf("list row = %s, want reseller_id r1", raw)
	}
	if got, ok := lm["gross_minor"].(json.Number); !ok || got.String() != "5" {
		t.Errorf("list row = %s, want gross_minor 5", raw)
	}
}

// ─── DB-backed report runs: skipped without TEST_DATABASE_URL ───
//
// Each test stamps its rows into its own fixed past month so the
// now()-stamped rows other test files leave behind can never leak
// into a window, and deletes what it created.

func reportsWindow(y int, m time.Month) (time.Time, time.Time) {
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC),
		time.Date(y, m, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, -1)
}

func at(y int, m time.Month, d, hour int) time.Time {
	return time.Date(y, m, d, hour, 0, 0, 0, time.UTC)
}

// reportsSeedOrder writes one order at a known instant. created is the
// order clock; a paid order is paid then, a refunded one was paid an
// hour earlier and refunded then.
func reportsSeedOrder(t *testing.T, s *store.Store, ctx context.Context, tag, status string, created time.Time, total int64) *model.Order {
	t.Helper()
	o := &model.Order{
		OrderNumber:   "RPT-" + tag,
		CustomerEmail: "rpt-" + tag + "@example.com",
		Currency:      "USD",
		SubtotalMinor: total,
		DiscountMinor: 0,
		TaxMinor:      0,
		TotalMinor:    total,
		Status:        status,
	}
	switch status {
	case model.OrderStatusPaid:
		paid := created
		o.PaidAt = &paid
	case model.OrderStatusRefunded:
		paid := created.Add(-time.Hour)
		ref := created
		o.PaidAt, o.RefundedAt = &paid, &ref
	}
	if err := s.CreateOrder(ctx, o); err != nil {
		t.Fatalf("seed order %s: %v", tag, err)
	}
	if _, err := s.DB.NewRaw("UPDATE orders SET created_at = ? WHERE id = ?", created, o.ID).Exec(ctx); err != nil {
		t.Fatalf("stamp order %s: %v", tag, err)
	}
	return o
}

func reportsRow(t *testing.T, res *store.ReportResult, key string) store.ReportRow {
	t.Helper()
	for _, r := range res.Series {
		if r.Period == key || r.ResellerID == key || r.AffiliateID == key {
			return r
		}
	}
	t.Fatalf("%s: no series row %q in %+v", res.ReportType, key, res.Series)
	return store.ReportRow{}
}

func reportsAssertMetric(t *testing.T, res *store.ReportResult, row store.ReportRow, name string, want int64) {
	t.Helper()
	if got := row.Metrics[name]; got != want {
		t.Errorf("%s row metric %s = %d, want %d", res.ReportType, name, got, want)
	}
}

func reportsAssertTotal(t *testing.T, res *store.ReportResult, name string, want int64) {
	t.Helper()
	if got := res.Totals[name]; got != want {
		t.Errorf("%s total %s = %d, want %d", res.ReportType, name, got, want)
	}
}

// revenue / sales / refunds / failed_payments over one seeded month.
// Revenue recognizes at paid_at and counts paid orders only; sales
// counts what was sold (pending + paid + refunded, never failed);
// refunds and failed payments key off refunded_at and the failed
// status respectively.
func TestRunReportOrdersReports(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	tag := "ord-" + store.NewID()
	defer func() {
		_, _ = s.DB.NewRaw("DELETE FROM orders WHERE order_number LIKE ?", tag+"%").Exec(ctx)
	}()

	from, to := reportsWindow(2025, time.June)
	reportsSeedOrder(t, s, ctx, tag+"-1", model.OrderStatusPaid, at(2025, 6, 2, 10), 1000)
	reportsSeedOrder(t, s, ctx, tag+"-2", model.OrderStatusPaid, at(2025, 6, 3, 10), 2500)
	reportsSeedOrder(t, s, ctx, tag+"-3", model.OrderStatusRefunded, at(2025, 6, 4, 10), 500)
	reportsSeedOrder(t, s, ctx, tag+"-4", model.OrderStatusFailed, at(2025, 6, 5, 10), 700)
	reportsSeedOrder(t, s, ctx, tag+"-5", model.OrderStatusPending, at(2025, 6, 6, 10), 300)
	// Outside the window entirely.
	reportsSeedOrder(t, s, ctx, tag+"-6", model.OrderStatusPaid, at(2025, 5, 15, 10), 9999)

	rev, err := s.RunReport(ctx, store.ReportTypeRevenue, from, to, "day")
	if err != nil {
		t.Fatalf("revenue: %v", err)
	}
	if len(rev.Series) != 2 {
		t.Fatalf("revenue series = %+v, want 2 days", rev.Series)
	}
	reportsAssertMetric(t, rev, reportsRow(t, rev, "2025-06-02"), "revenue_minor", 1000)
	reportsAssertMetric(t, rev, reportsRow(t, rev, "2025-06-02"), "order_count", 1)
	reportsAssertMetric(t, rev, reportsRow(t, rev, "2025-06-03"), "revenue_minor", 2500)
	reportsAssertTotal(t, rev, "revenue_minor", 3500)
	reportsAssertTotal(t, rev, "order_count", 2)

	sales, err := s.RunReport(ctx, store.ReportTypeSales, from, to, "month")
	if err != nil {
		t.Fatalf("sales: %v", err)
	}
	row := reportsRow(t, sales, "2025-06-01")
	reportsAssertMetric(t, sales, row, "order_count", 4) // paid x2 + refunded + pending
	reportsAssertMetric(t, sales, row, "gross_minor", 4300)
	reportsAssertMetric(t, sales, row, "discount_minor", 0)
	reportsAssertMetric(t, sales, row, "tax_minor", 0)
	reportsAssertMetric(t, sales, row, "net_minor", 4300)

	refunds, err := s.RunReport(ctx, store.ReportTypeRefunds, from, to, "day")
	if err != nil {
		t.Fatalf("refunds: %v", err)
	}
	reportsAssertMetric(t, refunds, reportsRow(t, refunds, "2025-06-04"), "refund_count", 1)
	reportsAssertMetric(t, refunds, reportsRow(t, refunds, "2025-06-04"), "refund_minor", 500)

	failed, err := s.RunReport(ctx, store.ReportTypeFailedPayments, from, to, "day")
	if err != nil {
		t.Fatalf("failed_payments: %v", err)
	}
	reportsAssertMetric(t, failed, reportsRow(t, failed, "2025-06-05"), "failed_count", 1)
	reportsAssertMetric(t, failed, reportsRow(t, failed, "2025-06-05"), "failed_minor", 700)
	reportsAssertTotal(t, failed, "failed_minor", 700)
}

// subscriptions: active_end is a snapshot at the period end (not a
// sum), new/canceled are event counts. churn: a cancellation in the
// window is a churn, and churned_revenue_minor is the last paid order
// on that subscription's licence before it cancelled.
func TestRunReportSubscriptionsAndChurn(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	tag := "sub-" + store.NewID()

	lic1 := createTestLicense(t, s, ctx)
	lic2 := createTestLicense(t, s, ctx)
	lic3 := createTestLicense(t, s, ctx) // the churn with no charges at all
	defer func() {
		_, _ = s.DB.NewRaw("DELETE FROM subscriptions WHERE id LIKE ?", tag+"%").Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM orders WHERE order_number LIKE ?", "RPT-"+tag+"%").Exec(ctx)
	}()

	seedSub := func(name string, licenseID string, created time.Time, canceled *time.Time) {
		id := tag + "-" + name
		if _, err := s.DB.NewRaw(
			`INSERT INTO subscriptions (id, license_id, plan_id, status, created_at, canceled_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, licenseID, lic1.PlanID, "canceled", created, canceled, created,
		).Exec(ctx); err != nil {
			t.Fatalf("seed subscription %s: %v", name, err)
		}
	}
	canceled1 := at(2025, 7, 10, 12)
	canceled3 := at(2025, 7, 15, 12)
	seedSub("a", lic1.ID, at(2025, 7, 1, 9), &canceled1)
	seedSub("b", lic2.ID, at(2025, 7, 5, 9), nil)
	seedSub("c", lic3.ID, at(2025, 5, 1, 9), &canceled3)

	// The last charge that will not repeat: the churned revenue proxy.
	reportsSeedOrder(t, s, ctx, tag+"-last", model.OrderStatusPaid, at(2025, 7, 2, 10), 1200)
	// Same licence as the churned sub "a": link its order.
	if _, err := s.DB.NewRaw("UPDATE orders SET license_id = ? WHERE order_number = ?", lic1.ID, "RPT-"+tag+"-last").Exec(ctx); err != nil {
		t.Fatalf("link churn order: %v", err)
	}

	from, to := reportsWindow(2025, time.July)
	subs, err := s.RunReport(ctx, store.ReportTypeSubscriptions, from, to, "day")
	if err != nil {
		t.Fatalf("subscriptions: %v", err)
	}
	reportsAssertTotal(t, subs, "new_count", 2)      // a + b
	reportsAssertTotal(t, subs, "canceled_count", 2) // a + c
	reportsAssertTotal(t, subs, "active_end", 1)     // only b still active at Aug 1
	reportsAssertMetric(t, subs, reportsRow(t, subs, "2025-07-01"), "new_count", 1)
	// At the end of 2025-07-01: a and c active (c predates the window).
	reportsAssertMetric(t, subs, reportsRow(t, subs, "2025-07-01"), "active_end", 2)
	reportsAssertMetric(t, subs, reportsRow(t, subs, "2025-07-10"), "canceled_count", 1)

	churn, err := s.RunReport(ctx, store.ReportTypeChurn, from, to, "day")
	if err != nil {
		t.Fatalf("churn: %v", err)
	}
	if len(churn.Series) != 2 {
		t.Fatalf("churn series = %+v, want 2 buckets (the two cancellations)", churn.Series)
	}
	reportsAssertMetric(t, churn, reportsRow(t, churn, "2025-07-10"), "canceled_count", 1)
	reportsAssertMetric(t, churn, reportsRow(t, churn, "2025-07-10"), "churned_revenue_minor", 1200)
	reportsAssertMetric(t, churn, reportsRow(t, churn, "2025-07-15"), "churned_revenue_minor", 0) // no charges ever
	reportsAssertTotal(t, churn, "canceled_count", 2)
	reportsAssertTotal(t, churn, "churned_revenue_minor", 1200)
}

// customers: new = first paid order EVER in the window, buying =
// distinct paying addresses per period. The buying total is a distinct
// count over the window — a repeat buyer must not double-count.
func TestRunReportCustomers(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	tag := "cust-" + store.NewID()
	defer func() {
		_, _ = s.DB.NewRaw("DELETE FROM orders WHERE order_number LIKE ?", tag+"%").Exec(ctx)
	}()

	// Two orders from one address (first one makes it "new"), one
	// order from a second address, both inside the window.
	first := reportsSeedOrder(t, s, ctx, tag+"-1", model.OrderStatusPaid, at(2025, 8, 2, 10), 1000)
	reportsSeedOrder(t, s, ctx, tag+"-2", model.OrderStatusPaid, at(2025, 8, 3, 10), 1000)
	reportsSeedOrder(t, s, ctx, tag+"-3", model.OrderStatusPaid, at(2025, 8, 3, 11), 1000)
	// Collapse 1 and 2 into one customer identity.
	shared := "rpt-" + tag + "-shared@example.com"
	if _, err := s.DB.NewRaw("UPDATE orders SET customer_email = ? WHERE order_number IN (?, ?)",
		shared, first.OrderNumber, tag+"-2").Exec(ctx); err != nil {
		t.Fatalf("share customer identity: %v", err)
	}

	from, to := reportsWindow(2025, time.August)
	res, err := s.RunReport(ctx, store.ReportTypeCustomers, from, to, "day")
	if err != nil {
		t.Fatalf("customers: %v", err)
	}
	reportsAssertMetric(t, res, reportsRow(t, res, "2025-08-02"), "new_customers_count", 1)
	reportsAssertMetric(t, res, reportsRow(t, res, "2025-08-03"), "new_customers_count", 1) // second address only
	reportsAssertMetric(t, res, reportsRow(t, res, "2025-08-03"), "buying_customers_count", 2)
	reportsAssertTotal(t, res, "new_customers_count", 2)
	// Distinct over the window (2), NOT the sum of daily buyers (3).
	reportsAssertTotal(t, res, "buying_customers_count", 2)
}

// renewals: the license_renewals ledger (minus refunds) unions with
// extension orders; an order that created its licence is a sale, and
// an order that is itself a renewal checkout is counted once.
func TestRunReportRenewals(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	tag := "ren-" + store.NewID()

	lic := createTestLicense(t, s, ctx)
	defer func() {
		_, _ = s.DB.NewRaw("DELETE FROM license_renewals WHERE id LIKE ?", tag+"%").Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM orders WHERE order_number LIKE ?", "RPT-"+tag+"%").Exec(ctx)
	}()

	// A ledger renewal with its checkout recorded in the order ledger.
	if _, err := s.DB.NewRaw(
		`INSERT INTO license_renewals (id, license_id, days, stripe_checkout_session_id, created_at)
		 VALUES (?, ?, 365, ?, ?)`, tag+"-r1", lic.ID, "cs-"+tag, at(2025, 9, 3, 10),
	).Exec(ctx); err != nil {
		t.Fatalf("seed renewal: %v", err)
	}
	reportsSeedOrder(t, s, ctx, tag+"-paid", model.OrderStatusPaid, at(2025, 9, 3, 10), 900)
	if _, err := s.DB.NewRaw("UPDATE orders SET external_id = ?, license_id = ? WHERE order_number = ?",
		"cs-"+tag, lic.ID, "RPT-"+tag+"-paid").Exec(ctx); err != nil {
		t.Fatalf("link renewal order: %v", err)
	}
	// A refunded renewal does not count.
	if _, err := s.DB.NewRaw(
		`INSERT INTO license_renewals (id, license_id, days, stripe_checkout_session_id, refunded_at, created_at)
		 VALUES (?, ?, 365, ?, ?, ?)`, tag+"-r2", lic.ID, "cs2-"+tag, at(2025, 9, 5, 10), at(2025, 9, 5, 10),
	).Exec(ctx); err != nil {
		t.Fatalf("seed refunded renewal: %v", err)
	}
	// The order that CREATED the licence (same external id as the
	// licence's own checkout session) is a sale, not a renewal.
	reportsSeedOrder(t, s, ctx, tag+"-orig", model.OrderStatusPaid, at(2025, 9, 7, 10), 500)
	if _, err := s.DB.NewRaw("UPDATE orders SET external_id = ?, license_id = ? WHERE order_number = ?",
		"cs-orig-"+tag, lic.ID, "RPT-"+tag+"-orig").Exec(ctx); err != nil {
		t.Fatalf("link original order: %v", err)
	}
	if _, err := s.DB.NewRaw("UPDATE licenses SET stripe_checkout_session_id = ? WHERE id = ?",
		"cs-orig-"+tag, lic.ID).Exec(ctx); err != nil {
		t.Fatalf("stamp licence session: %v", err)
	}
	// An extension order with no matching ledger row still counts.
	ext := reportsSeedOrder(t, s, ctx, tag+"-ext", model.OrderStatusPaid, at(2025, 9, 10, 10), 400)
	if _, err := s.DB.NewRaw("UPDATE orders SET external_id = ?, license_id = ? WHERE id = ?",
		"cs-ext-"+tag, lic.ID, ext.ID).Exec(ctx); err != nil {
		t.Fatalf("link extension order: %v", err)
	}

	from, to := reportsWindow(2025, time.September)
	res, err := s.RunReport(ctx, store.ReportTypeRenewals, from, to, "day")
	if err != nil {
		t.Fatalf("renewals: %v", err)
	}
	if len(res.Series) != 2 {
		t.Fatalf("renewal series = %+v, want 2 buckets (ledger renewal + extension order)", res.Series)
	}
	reportsAssertMetric(t, res, reportsRow(t, res, "2025-09-03"), "renewal_count", 1)
	reportsAssertMetric(t, res, reportsRow(t, res, "2025-09-03"), "renewal_minor", 900) // matched order
	reportsAssertMetric(t, res, reportsRow(t, res, "2025-09-10"), "renewal_count", 1)
	reportsAssertMetric(t, res, reportsRow(t, res, "2025-09-10"), "renewal_minor", 400)
	reportsAssertTotal(t, res, "renewal_count", 2)
	reportsAssertTotal(t, res, "renewal_minor", 1300)
}

// licenses: created at creation, activated at FIRST activation, expired
// at valid_until, revoked at the updated_at of a revoked row (the
// schema stamps no revoked_at — documented approximation). Devices
// dedupe identifiers across licences; activations do not.
func TestRunReportLicensesActivationsDevices(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	tag := "dev-" + store.NewID()

	lic := createTestLicense(t, s, ctx)
	lic2 := createTestLicense(t, s, ctx)
	expired := at(2025, 10, 20, 0)
	defer func() {
		_, _ = s.DB.NewRaw("DELETE FROM activations WHERE license_id IN (?, ?)", lic.ID, lic2.ID).Exec(ctx)
	}()

	if _, err := s.DB.NewRaw("UPDATE licenses SET created_at = ?, valid_until = ? WHERE id = ?",
		at(2025, 10, 5, 0), expired, lic.ID).Exec(ctx); err != nil {
		t.Fatalf("stamp license: %v", err)
	}
	// A revoked licence whose update lands in the window.
	if _, err := s.DB.NewRaw("UPDATE licenses SET status = 'revoked', updated_at = ? WHERE id = ?",
		at(2025, 10, 25, 0), lic2.ID).Exec(ctx); err != nil {
		t.Fatalf("revoke license: %v", err)
	}
	// Same device on two licences (one device, two activation rows of
	// which only the first-seen row counts per report), plus a seat.
	seedAct := func(name, licenseID, identifier, idType string, created time.Time) {
		if _, err := s.DB.NewRaw(
			`INSERT INTO activations (id, license_id, identifier, identifier_type, created_at)
			 VALUES (?, ?, ?, ?, ?)`, tag+"-"+name, licenseID, identifier, idType, created,
		).Exec(ctx); err != nil {
			t.Fatalf("seed activation %s: %v", name, err)
		}
	}
	seedAct("a1", lic.ID, "device-"+tag, "device", at(2025, 10, 10, 9))
	seedAct("a2", lic2.ID, "device-"+tag, "device", at(2025, 10, 11, 9))
	seedAct("a3", lic.ID, "user-"+tag, "user", at(2025, 10, 12, 9))

	from, to := reportsWindow(2025, time.October)
	lics, err := s.RunReport(ctx, store.ReportTypeLicenses, from, to, "day")
	if err != nil {
		t.Fatalf("licenses: %v", err)
	}
	reportsAssertTotal(t, lics, "created_count", 1)   // lic stamped into the window
	reportsAssertTotal(t, lics, "activated_count", 2) // both licences first activated in window
	reportsAssertTotal(t, lics, "expired_count", 1)   // valid_until in window
	reportsAssertTotal(t, lics, "revoked_count", 1)
	reportsAssertMetric(t, lics, reportsRow(t, lics, "2025-10-20"), "expired_count", 1)

	acts, err := s.RunReport(ctx, store.ReportTypeActivations, from, to, "day")
	if err != nil {
		t.Fatalf("activations: %v", err)
	}
	reportsAssertTotal(t, acts, "activation_count", 3) // all rows created in window

	devs, err := s.RunReport(ctx, store.ReportTypeDevices, from, to, "day")
	if err != nil {
		t.Fatalf("devices: %v", err)
	}
	// One distinct device identifier across two licences; the user
	// seat is not a device.
	reportsAssertTotal(t, devs, "device_count", 1)
	reportsAssertMetric(t, devs, reportsRow(t, devs, "2025-10-10"), "device_count", 1)
}

func TestRunReportUsage(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	tag := "use-" + store.NewID()

	lic := createTestLicense(t, s, ctx)
	defer func() {
		_, _ = s.DB.NewRaw("DELETE FROM usage_events WHERE license_id = ?", lic.ID).Exec(ctx)
	}()
	seedUsage := func(name string, qty int64, recorded time.Time) {
		if _, err := s.DB.NewRaw(
			`INSERT INTO usage_events (id, license_id, feature, quantity, recorded_at)
			 VALUES (?, ?, 'api_calls', ?, ?)`, tag+"-"+name, lic.ID, qty, recorded,
		).Exec(ctx); err != nil {
			t.Fatalf("seed usage %s: %v", name, err)
		}
	}
	seedUsage("u1", 3, at(2025, 10, 2, 9))
	seedUsage("u2", 7, at(2025, 10, 2, 15))
	seedUsage("u3", 5, at(2025, 10, 3, 9))

	from, to := reportsWindow(2025, time.October)
	res, err := s.RunReport(ctx, store.ReportTypeUsage, from, to, "day")
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	reportsAssertMetric(t, res, reportsRow(t, res, "2025-10-02"), "usage_events", 2)
	reportsAssertMetric(t, res, reportsRow(t, res, "2025-10-02"), "usage_units", 10)
	reportsAssertTotal(t, res, "usage_events", 3)
	reportsAssertTotal(t, res, "usage_units", 15)
}

// resellers / affiliates: top lists keyed by partner, money from the
// partner ledgers with each metric windowed on its own event clock.
func TestRunReportPartners(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	tag := "ptr-" + store.NewID()

	r := newCommissionTestReseller(t, s, ctx)
	defer cleanupCommissionTestReseller(t, s, ctx, r)
	aff := newAffiliateTest(t, s, ctx, "Reports "+tag, "reports-"+tag+"@example.com")
	// A stored-form referral code: [A-Z0-9], 4..32 (see
	// model.ValidReferralCode); the uuid folds into that shape.
	code := newCodeTest(t, s, ctx, aff.ID, "RPT"+model.NormalizeReferralCode(store.NewID())[:12])
	defer func() {
		_, _ = s.DB.NewRaw("DELETE FROM affiliate_conversions WHERE affiliate_id = ?", aff.ID).Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM affiliate_payouts WHERE affiliate_id = ?", aff.ID).Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM referral_codes WHERE affiliate_id = ?", aff.ID).Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM affiliates WHERE id = ?", aff.ID).Exec(ctx)
	}()

	from, to := reportsWindow(2025, time.November)

	// Attributed sales for the reseller.
	o := reportsSeedOrder(t, s, ctx, tag+"-1", model.OrderStatusPaid, at(2025, 11, 5, 10), 10000)
	if _, err := s.DB.NewRaw("UPDATE orders SET reseller_id = ? WHERE id = ?", r.ID, o.ID).Exec(ctx); err != nil {
		t.Fatalf("attribute order: %v", err)
	}
	// Commission accrued in the window, later marked paid (paid_at in
	// the window), and one accrued OUTSIDE the window that pays inside
	// it: accrual and payout are windowed on their own clocks.
	cm, _, err := s.AccrueCommission(ctx, &model.Commission{ResellerID: r.ID, OrderID: o.ID, BasisMinor: 10000, BPS: 1000})
	if err != nil {
		t.Fatalf("accrue: %v", err)
	}
	if _, err := s.DB.NewRaw("UPDATE commissions SET created_at = ? WHERE id = ?", at(2025, 11, 5, 10), cm.ID).Exec(ctx); err != nil {
		t.Fatalf("stamp commission: %v", err)
	}
	if _, err := s.MarkCommissionPaid(ctx, cm.ID, at(2025, 11, 20, 10)); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	cm2, _, err := s.AccrueCommission(ctx, &model.Commission{ResellerID: r.ID, OrderID: o.ID + "-old", BasisMinor: 5000, BPS: 1000})
	if err != nil {
		t.Fatalf("accrue 2: %v", err)
	}
	if _, err := s.DB.NewRaw("UPDATE commissions SET created_at = ? WHERE id = ?", at(2025, 8, 5, 10), cm2.ID).Exec(ctx); err != nil {
		t.Fatalf("stamp commission 2: %v", err)
	}
	if _, err := s.MarkCommissionPaid(ctx, cm2.ID, at(2025, 11, 21, 10)); err != nil {
		t.Fatalf("mark paid 2: %v", err)
	}

	conv, _, err := s.RecordConversion(ctx, &model.AffiliateConversion{
		AffiliateID: aff.ID, CodeID: code.ID, OrderID: o.ID,
		OrderTotalMinor: 10000, CommissionMinor: 500,
	})
	if err != nil {
		t.Fatalf("record conversion: %v", err)
	}
	if _, err := s.DB.NewRaw("UPDATE affiliate_conversions SET created_at = ? WHERE id = ?",
		at(2025, 11, 5, 10), conv.ID).Exec(ctx); err != nil {
		t.Fatalf("stamp conversion: %v", err)
	}
	pay := &model.AffiliatePayout{AffiliateID: aff.ID, AmountMinor: 500}
	if err := s.CreatePayout(ctx, pay); err != nil {
		t.Fatalf("create payout: %v", err)
	}
	// CreatePayout writes a request; the payout run then stamps paid.
	// Stamped directly here so the paid_at lands in the window.
	if _, err := s.DB.NewRaw("UPDATE affiliate_payouts SET status = 'paid', paid_at = ? WHERE id = ?",
		at(2025, 11, 25, 10), pay.ID).Exec(ctx); err != nil {
		t.Fatalf("mark payout paid: %v", err)
	}

	res, err := s.RunReport(ctx, store.ReportTypeResellers, from, to, "day")
	if err != nil {
		t.Fatalf("resellers: %v", err)
	}
	row := reportsRow(t, res, r.ID)
	reportsAssertMetric(t, res, row, "order_count", 1)
	reportsAssertMetric(t, res, row, "gross_minor", 10000)
	reportsAssertMetric(t, res, row, "commission_accrued_minor", 1000) // the November accrual only
	reportsAssertMetric(t, res, row, "commission_paid_minor", 1500)    // both payouts landed in November
	reportsAssertTotal(t, res, "gross_minor", 10000)

	affRes, err := s.RunReport(ctx, store.ReportTypeAffiliates, from, to, "day")
	if err != nil {
		t.Fatalf("affiliates: %v", err)
	}
	arow := reportsRow(t, affRes, aff.ID)
	reportsAssertMetric(t, affRes, arow, "conversion_count", 1)
	reportsAssertMetric(t, affRes, arow, "commission_minor", 500)
	reportsAssertMetric(t, affRes, arow, "payout_minor", 500)
	reportsAssertTotal(t, affRes, "payout_minor", 500)
}
