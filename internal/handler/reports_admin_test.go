package handler

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// fakeReportsStore stands in for store.Store so every validation,
// mapping and rendering path runs without a database. It records the
// arguments the handler passed and what it audited.
type fakeReportsStore struct {
	result   *store.ReportResult
	err      error
	gotType  string
	gotFrom  time.Time
	gotTo    time.Time
	gotGroup string
	calls    int
	audits   []*model.AuditLog
}

func (f *fakeReportsStore) RunReport(_ context.Context, reportType string, from, to time.Time, groupBy string) (*store.ReportResult, error) {
	f.calls++
	f.gotType, f.gotFrom, f.gotTo, f.gotGroup = reportType, from, to, groupBy
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

func (f *fakeReportsStore) Audit(_ context.Context, log *model.AuditLog) {
	f.audits = append(f.audits, log)
}

func reportsReq(t *testing.T, target, typ string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", target, nil)
	c.Params = gin.Params{{Key: "type", Value: typ}}
	return w, c
}

// reportsData unwraps the {success, data} envelope every response.OK
// answers with and hands back the data object.
func reportsData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not the response envelope: %v; %s", err, w.Body.String())
	}
	if !env.Success {
		t.Fatalf("success = false; body %s", w.Body.String())
	}
	var data map[string]any
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("data is not an object: %v; %s", err, env.Data)
	}
	return data
}

// revenueResult is a canned run with a money value past 2^53, so a
// float anywhere in the pipeline would show up as a wrong number.
func revenueResult() *store.ReportResult {
	return &store.ReportResult{
		ReportType: store.ReportTypeRevenue,
		GroupBy:    "day",
		From:       "2026-10-01",
		To:         "2026-10-08",
		Series: []store.ReportRow{
			{Period: "2026-10-01", Metrics: map[string]int64{"revenue_minor": 1999, "order_count": 1}},
			{Period: "2026-10-03", Metrics: map[string]int64{"revenue_minor": 9007199254740993, "order_count": 2}},
		},
		Totals: map[string]int64{"revenue_minor": 9007199254740995, "order_count": 3},
	}
}

// The vocabulary endpoint is the UI's contract: exactly the fourteen
// plan §43 reports, the accepted group_by values and export formats.
func TestReportsListVocabulary(t *testing.T) {
	h := NewReportsAdminHandler(nil)
	w, c := reportsReq(t, "/admin/reports", "")
	h.List(c)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			ReportTypes   []string            `json:"report_types"`
			GroupBy       []string            `json:"group_by"`
			DefaultGroup  string              `json:"default_group_by"`
			ExportFormats []string            `json:"export_formats"`
			Metrics       map[string][]string `json:"metrics"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not the vocabulary: %v", err)
	}
	body := env.Data
	if len(body.ReportTypes) != 14 {
		t.Fatalf("report_types = %v, want exactly the 14 plan reports", body.ReportTypes)
	}
	for i, rt := range store.ReportTypes {
		if body.ReportTypes[i] != rt {
			t.Errorf("report_types[%d] = %q, want %q", i, body.ReportTypes[i], rt)
		}
	}
	if len(body.GroupBy) != 3 || body.GroupBy[0] != "day" || body.GroupBy[1] != "week" || body.GroupBy[2] != "month" {
		t.Errorf("group_by = %v, want [day week month]", body.GroupBy)
	}
	if body.DefaultGroup != "day" {
		t.Errorf("default_group_by = %q, want day", body.DefaultGroup)
	}
	if len(body.ExportFormats) != 2 || body.ExportFormats[0] != "csv" || body.ExportFormats[1] != "json" {
		t.Errorf("export_formats = %v, want [csv json] (PDF deliberately absent)", body.ExportFormats)
	}
	if len(body.Metrics) != 14 {
		t.Errorf("metrics covers %d reports, want 14", len(body.Metrics))
	}
}

// A report type outside the closed vocabulary is a missing resource —
// 404, and nothing runs.
func TestReportsUnknownTypeIs404(t *testing.T) {
	fake := &fakeReportsStore{result: revenueResult()}
	h := &ReportsAdminHandler{store: fake}

	w, c := reportsReq(t, "/admin/reports/nope", "nope")
	h.Get(c)
	if w.Code != 404 {
		t.Errorf("Get status = %d, want 404", w.Code)
	}
	w2, c2 := reportsReq(t, "/admin/reports/nope/export?format=csv", "nope")
	h.Export(c2)
	if w2.Code != 404 {
		t.Errorf("Export status = %d, want 404", w2.Code)
	}
	if fake.calls != 0 {
		t.Errorf("RunReport called %d times on unknown types, want 0", fake.calls)
	}
}

// Every malformed parameter is the caller's 400 and never reaches the
// store: inverted dates, the 3-year span cap, unparseable dates,
// missing bounds, and a group_by outside the whitelist.
func TestReportsBadParamsAre400(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{"from after to", "/admin/reports/revenue?from=2026-10-08&to=2026-10-01"},
		{"span over three years", "/admin/reports/revenue?from=2020-01-01&to=2023-01-02"},
		{"bad from format", "/admin/reports/revenue?from=08-10-2026&to=2026-10-08"},
		{"bad to format", "/admin/reports/revenue?from=2026-10-01&to=soon"},
		{"missing from", "/admin/reports/revenue?to=2026-10-08"},
		{"missing to", "/admin/reports/revenue?from=2026-10-01"},
		{"unknown group_by", "/admin/reports/revenue?from=2026-10-01&to=2026-10-08&group_by=year"},
	}
	for _, tc := range cases {
		fake := &fakeReportsStore{result: revenueResult()}
		h := &ReportsAdminHandler{store: fake}
		w, c := reportsReq(t, tc.target, "revenue")
		h.Get(c)
		if w.Code != 400 {
			t.Errorf("%s: status = %d, want 400; body %s", tc.name, w.Code, w.Body.String())
		}
		if fake.calls != 0 {
			t.Errorf("%s: RunReport called, want the request refused first", tc.name)
		}
	}
}

// A good request runs with UTC day bounds and the default bucketing;
// the answer keeps the pinned {series: [{period, ...metrics}], totals}
// shape — metrics flattened beside the period.
func TestReportsGetOK(t *testing.T) {
	fake := &fakeReportsStore{result: revenueResult()}
	h := &ReportsAdminHandler{store: fake}
	w, c := reportsReq(t, "/admin/reports/revenue?from=2026-10-01&to=2026-10-08", "revenue")
	h.Get(c)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if fake.gotType != "revenue" || fake.gotGroup != "day" {
		t.Errorf("RunReport got type=%q group=%q, want revenue/day", fake.gotType, fake.gotGroup)
	}
	if !fake.gotFrom.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("from = %v, want 2026-10-01 UTC", fake.gotFrom)
	}
	if !fake.gotTo.Equal(time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("to = %v, want 2026-10-08 UTC", fake.gotTo)
	}

	var body map[string]any = reportsData(t, w)
	if body["report_type"] != "revenue" || body["from"] != "2026-10-01" || body["to"] != "2026-10-08" {
		t.Errorf("metadata = %v", body)
	}
	series, _ := body["series"].([]any)
	if len(series) != 2 {
		t.Fatalf("series = %v, want 2 rows", body["series"])
	}
	row := series[0].(map[string]any)
	if row["period"] != "2026-10-01" {
		t.Errorf("row period = %v", row["period"])
	}
	if _, nested := row["metrics"]; nested {
		t.Errorf("metrics are nested, want flattened: %v", row)
	}
	if row["revenue_minor"] == nil || row["order_count"] == nil {
		t.Errorf("row metrics missing: %v", row)
	}
	if _, ok := body["totals"].(map[string]any); !ok {
		t.Errorf("totals missing or not an object: %v", body["totals"])
	}
	if fake.calls != 1 {
		t.Errorf("RunReport calls = %d, want 1", fake.calls)
	}
}

// group_by=day|week|month passes through; anything else is a 400.
func TestReportsGroupByWhitelist(t *testing.T) {
	for _, g := range []string{"day", "week", "month"} {
		fake := &fakeReportsStore{result: revenueResult()}
		h := &ReportsAdminHandler{store: fake}
		w, c := reportsReq(t, "/admin/reports/revenue?from=2026-10-01&to=2026-10-08&group_by="+g, "revenue")
		h.Get(c)
		if w.Code != 200 || fake.gotGroup != g {
			t.Errorf("group_by=%s: status %d group %q, want 200/%s", g, w.Code, fake.gotGroup, g)
		}
	}
}

var csvIntegerCell = regexp.MustCompile(`^-?[0-9]+$`)

// The CSV contract (the web agent renders exactly this):
//   - header row: period|reseller_id|affiliate_id + metric names in
//     canonical order; money columns end _minor
//   - one row per series row; every metric cell is an integer string
//   - a final TOTAL row whose first cell is literally "TOTAL"
//   - text/csv with an attachment filename
//
// and every export is audited as report/exported.
func TestReportsExportCSV(t *testing.T) {
	fake := &fakeReportsStore{result: revenueResult()}
	h := &ReportsAdminHandler{store: fake}
	w, c := reportsReq(t, "/admin/reports/revenue/export?format=csv&from=2026-10-01&to=2026-10-08", "revenue")
	h.Export(c)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "text/csv; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/csv; charset=utf-8", ct)
	}
	cd := w.Header().Get("Content-Disposition")
	if !strings.Contains(cd, `filename="report-revenue-2026-10-01_2026-10-08.csv"`) {
		t.Errorf("Content-Disposition = %q, want the report filename", cd)
	}

	rows, err := csv.NewReader(w.Body).ReadAll()
	if err != nil {
		t.Fatalf("body is not CSV: %v", err)
	}
	if len(rows) != 4 { // header + 2 series rows + TOTAL
		t.Fatalf("rows = %d, want 4; %v", len(rows), rows)
	}
	wantHeader := []string{"period", "revenue_minor", "order_count"}
	for i, want := range wantHeader {
		if rows[0][i] != want {
			t.Errorf("header[%d] = %q, want %q", i, rows[0][i], want)
		}
	}
	for _, name := range rows[0][1:] {
		if strings.HasSuffix(name, "_minor") {
			continue // money column, named as the contract says
		}
		if !strings.HasSuffix(name, "_count") {
			t.Errorf("metric column %q must end in _minor or _count", name)
		}
	}
	if rows[1][0] != "2026-10-01" || rows[2][0] != "2026-10-03" {
		t.Errorf("series keys = %v, %v", rows[1][0], rows[2][0])
	}
	// Money stays integer minor units: no decimal point, no rounding.
	for _, r := range rows[1:] {
		for i := 1; i < len(r); i++ {
			if !csvIntegerCell.MatchString(r[i]) {
				t.Errorf("cell %q is not an integer string", r[i])
			}
		}
	}
	if rows[2][1] != "9007199254740993" {
		t.Errorf("big money cell = %q, want 9007199254740993 exactly (no float rounding)", rows[2][1])
	}
	total := rows[3]
	if total[0] != "TOTAL" || total[1] != "9007199254740995" || total[2] != "3" {
		t.Errorf("TOTAL row = %v, want [TOTAL 9007199254740995 3]", total)
	}

	if len(fake.audits) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(fake.audits))
	}
	a := fake.audits[0]
	if a.Entity != "report" || a.EntityID != "revenue" || a.Action != "exported" || a.ActorType != "admin" {
		t.Errorf("audit = %+v, want report/revenue exported by admin", a)
	}
	if a.Changes["format"] != "csv" || a.Changes["from"] != "2026-10-01" || a.Changes["to"] != "2026-10-08" {
		t.Errorf("audit changes = %v", a.Changes)
	}
}

// List-mode reports key their CSV on the partner id, not a period.
func TestReportsExportCSVListMode(t *testing.T) {
	res := &store.ReportResult{
		ReportType: store.ReportTypeResellers,
		From:       "2026-10-01",
		To:         "2026-10-08",
		Series: []store.ReportRow{
			{ResellerID: "res-1", Metrics: map[string]int64{
				"order_count": 2, "gross_minor": 300,
				"commission_accrued_minor": 30, "commission_paid_minor": 10,
			}},
		},
		Totals: map[string]int64{
			"order_count": 2, "gross_minor": 300,
			"commission_accrued_minor": 30, "commission_paid_minor": 10,
		},
	}
	fake := &fakeReportsStore{result: res}
	h := &ReportsAdminHandler{store: fake}
	w, c := reportsReq(t, "/admin/reports/resellers/export?format=csv&from=2026-10-01&to=2026-10-08", "resellers")
	h.Export(c)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	rows, err := csv.NewReader(w.Body).ReadAll()
	if err != nil {
		t.Fatalf("body is not CSV: %v", err)
	}
	wantHeader := []string{"reseller_id", "order_count", "gross_minor", "commission_accrued_minor", "commission_paid_minor"}
	for i, want := range wantHeader {
		if rows[0][i] != want {
			t.Errorf("header[%d] = %q, want %q", i, rows[0][i], want)
		}
	}
	if rows[1][0] != "res-1" || rows[1][2] != "300" {
		t.Errorf("series row = %v", rows[1])
	}
	if rows[2][0] != "TOTAL" || rows[2][2] != "300" {
		t.Errorf("TOTAL row = %v", rows[2])
	}
}

// JSON export answers the same result object as the report endpoint —
// full result, flattened rows — and is audited like any export.
func TestReportsExportJSON(t *testing.T) {
	fake := &fakeReportsStore{result: revenueResult()}
	h := &ReportsAdminHandler{store: fake}
	w, c := reportsReq(t, "/admin/reports/revenue/export?format=json&from=2026-10-01&to=2026-10-08", "revenue")
	h.Export(c)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any = reportsData(t, w)
	if body["report_type"] != "revenue" {
		t.Errorf("report_type = %v", body["report_type"])
	}
	series, _ := body["series"].([]any)
	if len(series) != 2 {
		t.Fatalf("series = %v", body["series"])
	}
	if series[0].(map[string]any)["period"] != "2026-10-01" {
		t.Errorf("series row = %v", series[0])
	}
	if len(fake.audits) != 1 {
		t.Errorf("audit entries = %d, want 1", len(fake.audits))
	}
}

// format is required and closed: csv|json only. PDF is deliberately
// not offered (plan §43: only formats supported reliably).
func TestReportsExportFormatValidation(t *testing.T) {
	for _, target := range []string{
		"/admin/reports/revenue/export?from=2026-10-01&to=2026-10-08",
		"/admin/reports/revenue/export?format=pdf&from=2026-10-01&to=2026-10-08",
		"/admin/reports/revenue/export?format=xml&from=2026-10-01&to=2026-10-08",
	} {
		fake := &fakeReportsStore{result: revenueResult()}
		h := &ReportsAdminHandler{store: fake}
		w, c := reportsReq(t, target, "revenue")
		h.Export(c)
		if w.Code != 400 {
			t.Errorf("%s: status = %d, want 400", target, w.Code)
		}
		if fake.calls != 0 {
			t.Errorf("%s: RunReport called on a refused format", target)
		}
	}
}

// A store refusal is still the caller's 400/404 when it carries one of
// the validation sentinels, and a plain failure is a quiet 500.
func TestReportsStoreErrorMapping(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"range sentinel", fmt.Errorf("wrap: %w", store.ErrInvalidReportRange), 400},
		{"groupBy sentinel", fmt.Errorf("wrap: %w", store.ErrInvalidGroupBy), 400},
		{"type sentinel", fmt.Errorf("wrap: %w", store.ErrInvalidReportType), 404},
		{"plain failure", errors.New("connection reset"), 500},
	}
	for _, tc := range cases {
		fake := &fakeReportsStore{err: tc.err}
		h := &ReportsAdminHandler{store: fake}
		w, c := reportsReq(t, "/admin/reports/revenue?from=2026-10-01&to=2026-10-08", "revenue")
		h.Get(c)
		if w.Code != tc.want {
			t.Errorf("%s: status = %d, want %d; body %s", tc.name, w.Code, tc.want, w.Body.String())
		}
	}
}
