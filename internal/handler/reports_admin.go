package handler

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// ReportsAdminHandler exposes the admin reporting engine (plan §43).
//
//	GET /admin/reports               Vocabulary + group_by + export formats
//	GET /admin/reports/:type         One report as JSON ({series, totals})
//	GET /admin/reports/:type/export  The same report as CSV or JSON download
//
// Query parameters for :type and :type/export:
//
//	from=YYYY-MM-DD&to=YYYY-MM-DD   inclusive UTC days, required
//	                                (from <= to, span <= 3 years)
//	group_by=day|week|month         time-series bucket, default day
//
// Export adds ?format=csv|json. PDF is deliberately not offered: no
// PDF renderer this stack can depend on reliably exists here (plan §43
// allows implementing only the formats supported reliably), so CSV and
// JSON are the contract.
//
// Every money column is int64 minor units — the CSV money headers end
// _minor and their cells are integer strings, never decimal.
//
// reportsAdminStore is the slice of store.Store this handler needs.
// The real constructor takes *store.Store; tests substitute a fake so
// every validation and rendering path runs without a database.
type reportsAdminStore interface {
	RunReport(ctx context.Context, reportType string, from, to time.Time, groupBy string) (*store.ReportResult, error)
	Audit(ctx context.Context, log *model.AuditLog)
}

var _ reportsAdminStore = (*store.Store)(nil)

type ReportsAdminHandler struct {
	store reportsAdminStore
}

// NewReportsAdminHandler wires the handler to the store.
func NewReportsAdminHandler(s *store.Store) *ReportsAdminHandler {
	return &ReportsAdminHandler{store: s}
}

// List answers GET /admin/reports: the closed report vocabulary, the
// accepted bucket granularities and the export formats — everything
// the admin UI needs to draw its pickers without hard-coding a word of
// it.
func (h *ReportsAdminHandler) List(c *gin.Context) {
	response.OK(c, gin.H{
		"report_types":     store.ReportTypes,
		"group_by":         store.ReportGroupBys,
		"default_group_by": store.ReportGroupByDay,
		"export_formats":   []string{"csv", "json"},
		"metrics":          store.ReportMetricNames,
	})
}

// params reads and validates :type plus the shared query parameters.
// Unknown report type answers 404 (the vocabulary is closed, so a
// wrong type is a missing resource); every malformed parameter answers
// 400 and runs nothing.
func (h *ReportsAdminHandler) params(c *gin.Context) (reportType string, from, to time.Time, groupBy string, ok bool) {
	reportType = c.Param("type")
	if !store.ValidReportType(reportType) {
		response.NotFound(c, "unknown report type "+strconv.Quote(clipForMessage(reportType)))
		return "", time.Time{}, time.Time{}, "", false
	}
	from, ok = reportDateParam(c, "from")
	if !ok {
		return "", time.Time{}, time.Time{}, "", false
	}
	to, ok = reportDateParam(c, "to")
	if !ok {
		return "", time.Time{}, time.Time{}, "", false
	}
	groupBy = strings.TrimSpace(c.Query("group_by"))
	if groupBy == "" {
		groupBy = store.ReportGroupByDay
	}
	if !store.ValidReportGroupBy(groupBy) {
		response.BadRequest(c, "group_by must be day, week, or month")
		return "", time.Time{}, time.Time{}, "", false
	}
	if from.After(to) {
		response.BadRequest(c, "from must be on or before to")
		return "", time.Time{}, time.Time{}, "", false
	}
	if to.After(from.AddDate(store.ReportMaxSpanYears, 0, 0)) {
		response.BadRequest(c, fmt.Sprintf("report range must not exceed %d years", store.ReportMaxSpanYears))
		return "", time.Time{}, time.Time{}, "", false
	}
	return reportType, from, to, groupBy, true
}

// reportDateParam reads one YYYY-MM-DD query parameter as a UTC day.
func reportDateParam(c *gin.Context, name string) (time.Time, bool) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		response.BadRequest(c, name+" is required (YYYY-MM-DD)")
		return time.Time{}, false
	}
	v, err := time.ParseInLocation("2006-01-02", raw, time.UTC)
	if err != nil {
		response.BadRequest(c, name+" must be a date in YYYY-MM-DD format")
		return time.Time{}, false
	}
	return v, true
}

// writeRunErr maps a store refusal back onto the API contract. The
// store re-validates defensively; anything it refuses is still the
// caller's 400/404, never a 500.
func (h *ReportsAdminHandler) writeRunErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrInvalidReportType):
		response.NotFound(c, "unknown report type")
	case errors.Is(err, store.ErrInvalidGroupBy):
		response.BadRequest(c, "group_by must be day, week, or month")
	case errors.Is(err, store.ErrInvalidReportRange):
		response.BadRequest(c, "invalid report date range")
	default:
		response.Internal(c, err)
	}
}

// Get answers GET /admin/reports/:type — the full result: {series:
// [{period, ...metrics}], totals: {...}} plus the echoed report_type,
// from, to and group_by. Periods are sparse by design (no gap
// filling); the client charts the points it is given.
func (h *ReportsAdminHandler) Get(c *gin.Context) {
	reportType, from, to, groupBy, ok := h.params(c)
	if !ok {
		return
	}
	res, err := h.store.RunReport(c, reportType, from, to, groupBy)
	if err != nil {
		h.writeRunErr(c, err)
		return
	}
	response.OK(c, res)
}

// Export answers GET /admin/reports/:type/export — the same run
// rendered as a CSV download or echoed as JSON. Every export is
// audited: a report is business data, and "who pulled the numbers,
// when" is answerable from the audit log.
func (h *ReportsAdminHandler) Export(c *gin.Context) {
	reportType, from, to, groupBy, ok := h.params(c)
	if !ok {
		return
	}
	format := strings.ToLower(strings.TrimSpace(c.Query("format")))
	if format != "csv" && format != "json" {
		response.BadRequest(c, "format must be csv or json")
		return
	}
	res, err := h.store.RunReport(c, reportType, from, to, groupBy)
	if err != nil {
		h.writeRunErr(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "report", EntityID: reportType, Action: "exported",
		ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
		Changes: map[string]any{
			"format": format, "from": res.From, "to": res.To, "group_by": res.GroupBy,
		},
	})
	if format == "json" {
		response.OK(c, res)
		return
	}
	writeReportCSV(c, res)
}

// reportCSVKeyColumn is the first CSV column of a report: the period
// bucket for time series, the partner id for the two list reports.
func reportCSVKeyColumn(reportType string) string {
	switch reportType {
	case store.ReportTypeResellers:
		return "reseller_id"
	case store.ReportTypeAffiliates:
		return "affiliate_id"
	}
	return "period"
}

// writeReportCSV streams the result as text/csv:
//
//	header row          period|reseller_id|affiliate_id + metric names
//	                    (canonical order; money columns end _minor)
//	one row per series row, integers only
//	a final TOTAL row   first cell literally "TOTAL", then the totals
//
// The values are integer minor units / counts — no decimals, no
// thousands separators, no currency decoration — so a spreadsheet
// reads them as numbers and the web UI can re-aggregate losslessly.
func writeReportCSV(c *gin.Context, res *store.ReportResult) {
	names := store.ReportMetricNames[res.ReportType]
	keyCol := reportCSVKeyColumn(res.ReportType)

	filename := fmt.Sprintf("report-%s-%s_%s.csv", res.ReportType, res.From, res.To)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
	c.Status(http.StatusOK)

	w := csv.NewWriter(c.Writer)
	header := make([]string, 0, len(names)+1)
	header = append(header, keyCol)
	header = append(header, names...)
	_ = w.Write(header)

	for _, row := range res.Series {
		rec := make([]string, 0, len(names)+1)
		switch keyCol {
		case "reseller_id":
			rec = append(rec, row.ResellerID)
		case "affiliate_id":
			rec = append(rec, row.AffiliateID)
		default:
			rec = append(rec, row.Period)
		}
		for _, name := range names {
			rec = append(rec, strconv.FormatInt(row.Metrics[name], 10))
		}
		_ = w.Write(rec)
	}

	total := make([]string, 0, len(names)+1)
	total = append(total, "TOTAL")
	for _, name := range names {
		total = append(total, strconv.FormatInt(res.Totals[name], 10))
	}
	_ = w.Write(total)
	w.Flush()
	if err := w.Error(); err != nil {
		// The status line is already on the wire; all that is left is
		// to say so in the log.
		response.LogInternal(c, err)
	}
}
