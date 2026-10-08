package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ─── Reports (plan §43 REPORTING) ───
//
// The built-in business reports: read-only aggregations over the
// commerce ledger, licence/activation tables, usage metering and the
// partner ledgers. A report is a time series — one row per period
// bucket — plus a totals object; the reseller and affiliate reports
// are the documented exception (a top list, not a time series).
//
// Money discipline: every money metric is int64 minor units summed
// across its rows and is named *_minor; counts are int64 and named
// *_count. Three pinned metric names predate that naming rule and are
// kept exactly as plan §43 pinned them: subscriptions.active_end
// (a snapshot count), usage.usage_events and usage.usage_units. No
// metric is ever a float.
//
// Aggregation decisions, all deliberate:
//
//   - UTC bucketing. Every date_trunc runs on (col AT TIME ZONE 'UTC'),
//     so a bucket is a UTC day/week/month wherever the server lives.
//     The period key is the bucket start as YYYY-MM-DD (month buckets
//     read as YYYY-MM-01, week buckets as the Monday).
//
//   - NO gap filling. Periods with no events are absent from the
//     series — the client charts sparse points. Totals always cover
//     the whole window regardless.
//
//   - Inclusive date bounds. from and to are calendar days; the window
//     is [from 00:00 UTC, to+1day 00:00 UTC).
//
//   - Renewals (renewal_count / renewal_minor). Orders carry no
//     renewal tag and Stripe subscription cycles (invoice.paid) leave
//     no per-cycle row, so the one durable renewal record is the
//     license_renewals ledger ("one row per paid renewal"). The series
//     unions two disjoint sources: (1) license_renewals rows applied
//     in the window with refunded_at IS NULL (a refunded renewal is a
//     clawback, see model.LicenseRenewal.Counts), whose money is the
//     total of the paid order the renewal checkout recorded in the
//     order ledger (orders.external_id = stripe_checkout_session_id)
//     — typically 0 today, because the Stripe renewal fulfilment
//     records no order; and (2) "extension orders": paid orders that
//     charge a licence created by a different checkout
//     (orders.external_id IS DISTINCT FROM
//     licenses.stripe_checkout_session_id) and are not themselves a
//     renewal checkout. An order with no external id cannot be told
//     apart from the sale that created its licence and counts as a
//     sale, not a renewal.
//
//   - Churn (canceled_count / churned_revenue_minor). A churn is a
//     subscription cancellation: subscriptions.canceled_at inside the
//     window. churned_revenue_minor is the total of the most recent
//     paid order on the churned subscription's licence at or before
//     cancellation — the last recurring charge that will not repeat.
//     That is a documented proxy: subscription prices live in Stripe,
//     not locally, and a subscription whose charges never landed in
//     the order ledger churns at 0 revenue.
//
//   - Resellers / affiliates are top lists, not time series: one row
//     per partner with activity in the window, ordered by the money
//     descending. The reseller window bounds orders by sale time,
//     commission accruals by accrual time and commission payouts by
//     paid time; the affiliate window bounds conversions by recording
//     time and payouts by paid time.
//
//   - Customers are keyed on orders.customer_email (the durable
//     customer handle): new_customers_count counts addresses whose
//     first paid order falls in the window, buying_customers_count
//     counts distinct paying addresses per period (its total is a
//     distinct count over the window, not a sum — hence the dedicated
//     totals query, shared with the subscriptions.active_end
//     snapshot).
//
//   - Multi-currency caveat: money is summed across currencies as
//     minor units (one USD minor unit and one VND minor unit add).
//     A per-currency split is a future refinement; nothing here
//     converts.
//
// Every SQL string below is a constant template: the only values that
// reach it are ? bind parameters plus the bucket unit, which is
// resolved from the groupBy whitelist map (reportTruncUnit /
// reportBucketEnd) — never interpolated from caller input.

// Report type vocabulary. Closed — RunReport refuses anything not in
// ReportTypes — because the type selects the SQL, and a made-up type
// would otherwise have to be answered with something.
const (
	ReportTypeRevenue        = "revenue"
	ReportTypeSales          = "sales"
	ReportTypeSubscriptions  = "subscriptions"
	ReportTypeRenewals       = "renewals"
	ReportTypeChurn          = "churn"
	ReportTypeLicenses       = "licenses"
	ReportTypeActivations    = "activations"
	ReportTypeDevices        = "devices"
	ReportTypeUsage          = "usage"
	ReportTypeCustomers      = "customers"
	ReportTypeResellers      = "resellers"
	ReportTypeAffiliates     = "affiliates"
	ReportTypeRefunds        = "refunds"
	ReportTypeFailedPayments = "failed_payments"
)

// ReportTypes lists the fourteen plan §43 reports in plan order. It is
// the single source the validation and the admin vocabulary endpoint
// read from, so they cannot drift apart.
var ReportTypes = []string{
	ReportTypeRevenue,
	ReportTypeSales,
	ReportTypeSubscriptions,
	ReportTypeRenewals,
	ReportTypeChurn,
	ReportTypeLicenses,
	ReportTypeActivations,
	ReportTypeDevices,
	ReportTypeUsage,
	ReportTypeCustomers,
	ReportTypeResellers,
	ReportTypeAffiliates,
	ReportTypeRefunds,
	ReportTypeFailedPayments,
}

// ValidReportType reports whether reportType is one of the fourteen.
func ValidReportType(reportType string) bool {
	for _, t := range ReportTypes {
		if t == reportType {
			return true
		}
	}
	return false
}

// Bucket granularity vocabulary for time-series reports.
const (
	ReportGroupByDay   = "day"
	ReportGroupByWeek  = "week"
	ReportGroupByMonth = "month"
)

// ReportGroupBys lists the accepted ?group_by= values in the order the
// admin UI offers them.
var ReportGroupBys = []string{ReportGroupByDay, ReportGroupByWeek, ReportGroupByMonth}

// ValidReportGroupBy reports whether groupBy is one of the three
// bucket granularities. An empty groupBy is accepted and means day —
// the handler says so in its vocabulary endpoint.
func ValidReportGroupBy(groupBy string) bool {
	switch groupBy {
	case ReportGroupByDay, ReportGroupByWeek, ReportGroupByMonth:
		return true
	}
	return false
}

// reportTruncUnit maps the validated groupBy word to the date_trunc
// unit. Identity on purpose: the map exists so the SQL builders read
// their unit from a fixed whitelist instead of touching caller input.
var reportTruncUnit = map[string]string{
	ReportGroupByDay:   "day",
	ReportGroupByWeek:  "week",
	ReportGroupByMonth: "month",
}

// reportBucketEnd maps the bucket granularity to the interval from a
// bucket start to the next one — what "period end" means for the one
// report that takes a snapshot there (subscriptions.active_end).
var reportBucketEnd = map[string]string{
	ReportGroupByDay:   "1 day",
	ReportGroupByWeek:  "7 days",
	ReportGroupByMonth: "1 month",
}

// ReportMetricNames gives each report's metrics in canonical column
// order — the CSV header order and the m1..m6 mapping of the queries.
// Every name is *_count or *_minor except three pinned plan §43 names
// (active_end, usage_events, usage_units), which are kept as pinned.
var ReportMetricNames = map[string][]string{
	ReportTypeRevenue:        {"revenue_minor", "order_count"},
	ReportTypeSales:          {"order_count", "gross_minor", "discount_minor", "tax_minor", "net_minor"},
	ReportTypeSubscriptions:  {"active_end", "new_count", "canceled_count"},
	ReportTypeRenewals:       {"renewal_count", "renewal_minor"},
	ReportTypeChurn:          {"canceled_count", "churned_revenue_minor"},
	ReportTypeLicenses:       {"created_count", "activated_count", "expired_count", "revoked_count"},
	ReportTypeActivations:    {"activation_count"},
	ReportTypeDevices:        {"device_count"},
	ReportTypeUsage:          {"usage_events", "usage_units"},
	ReportTypeCustomers:      {"new_customers_count", "buying_customers_count"},
	ReportTypeResellers:      {"order_count", "gross_minor", "commission_accrued_minor", "commission_paid_minor"},
	ReportTypeAffiliates:     {"conversion_count", "commission_minor", "payout_minor"},
	ReportTypeRefunds:        {"refund_count", "refund_minor"},
	ReportTypeFailedPayments: {"failed_count", "failed_minor"},
}

// ReportMaxSpanYears is the widest window one report run may cover.
// Three years of daily buckets is ~1100 rows — small enough to answer
// in one request, wide enough for any dashboard the admin UI draws.
const ReportMaxSpanYears = 3

// Report parameter refusals. The handler maps these to 404 / 400;
// RunReport checks them too so a bad call cannot reach the database.
var (
	ErrInvalidReportType  = errors.New("invalid report type")
	ErrInvalidGroupBy     = errors.New("invalid group_by")
	ErrInvalidReportRange = errors.New("invalid report date range")
)

// ValidateReportParams checks the three RunReport inputs. An empty
// groupBy is accepted and means day. The range rules are the pinned
// ones: from must be on or before to (equal days are one day wide),
// and the window must not exceed ReportMaxSpanYears.
func ValidateReportParams(reportType, groupBy string, from, to time.Time) error {
	if !ValidReportType(reportType) {
		return fmt.Errorf("%w: %q", ErrInvalidReportType, reportType)
	}
	if groupBy != "" && !ValidReportGroupBy(groupBy) {
		return fmt.Errorf("%w: %q", ErrInvalidGroupBy, groupBy)
	}
	if from.IsZero() || to.IsZero() {
		return fmt.Errorf("%w: from and to are required", ErrInvalidReportRange)
	}
	if from.After(to) {
		return fmt.Errorf("%w: from must be on or before to", ErrInvalidReportRange)
	}
	if to.After(from.AddDate(ReportMaxSpanYears, 0, 0)) {
		return fmt.Errorf("%w: range must not exceed %d years", ErrInvalidReportRange, ReportMaxSpanYears)
	}
	return nil
}

// ReportRow is one series row: the group key (a period bucket for time
// series, a partner id for the two list reports) plus that report's
// metrics. Metrics are always int64 — counts and minor-unit money.
type ReportRow struct {
	// Period is the UTC bucket start as YYYY-MM-DD. Empty in list mode.
	Period string `json:"period,omitempty"`
	// ResellerID / AffiliateID are the list-mode group keys.
	ResellerID  string `json:"reseller_id,omitempty"`
	AffiliateID string `json:"affiliate_id,omitempty"`
	// Metrics maps the report's metric names to int64 values. JSON
	// flattens them beside the key (see MarshalJSON) so a row reads
	// {"period": "...", "revenue_minor": 0, ...} exactly as the
	// {series: [{period, ...metrics}]} contract specifies.
	Metrics map[string]int64 `json:"-"`
}

// MarshalJSON flattens the metrics beside the group key, producing
// {"period": "...", metric: value, ...} (or reseller_id/affiliate_id
// in list mode). Values stay JSON integers — money is never a float.
func (r ReportRow) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, len(r.Metrics)+1)
	if r.Period != "" {
		m["period"] = r.Period
	}
	if r.ResellerID != "" {
		m["reseller_id"] = r.ResellerID
	}
	if r.AffiliateID != "" {
		m["affiliate_id"] = r.AffiliateID
	}
	for k, v := range r.Metrics {
		m[k] = v
	}
	return json.Marshal(m)
}

// ReportResult is what one report run answers: the metadata the client
// echoes, the series, and the totals over the whole window.
type ReportResult struct {
	ReportType string           `json:"report_type"`
	GroupBy    string           `json:"group_by,omitempty"`
	From       string           `json:"from"`
	To         string           `json:"to"`
	Series     []ReportRow      `json:"series"`
	Totals     map[string]int64 `json:"totals"`
}

// reportScanRow is the one shape every report query returns: the
// series key plus up to six aggregate columns aliased m1..m6. The
// per-report metric names in ReportMetricNames say which name each
// alias carries, so one scanner serves all fourteen reports.
type reportScanRow struct {
	Key string `bun:"series_key"`
	M1  int64  `bun:"m1"`
	M2  int64  `bun:"m2"`
	M3  int64  `bun:"m3"`
	M4  int64  `bun:"m4"`
	M5  int64  `bun:"m5"`
	M6  int64  `bun:"m6"`
}

// vals hands the six alias slots back in order.
func (r reportScanRow) vals() []int64 {
	return []int64{r.M1, r.M2, r.M3, r.M4, r.M5, r.M6}
}

// reportTrunc renders the bucket key of column col: truncated in UTC
// at the given unit and formatted as the bucket start, YYYY-MM-DD.
// unit comes only from reportTruncUnit; col is a code constant.
func reportTrunc(unit, col string) string {
	return fmt.Sprintf("to_char(date_trunc('%s', %s AT TIME ZONE 'UTC'), 'YYYY-MM-DD')", unit, col)
}

// reportSeriesQuery returns the series SQL for a report and its bind
// arguments, in textual placeholder order. Every caller-visible value
// is a bind; only the whitelisted unit strings are composed in.
func reportSeriesQuery(reportType, unit string) (string, []any) {
	switch reportType {
	case ReportTypeRevenue:
		return fmt.Sprintf(`
SELECT %s AS series_key,
       COALESCE(SUM(t.total_minor), 0)::bigint AS m1,
       COUNT(*)::bigint AS m2
FROM (
    SELECT COALESCE(o.paid_at, o.created_at) AS ev, o.total_minor
    FROM orders o
    WHERE o.status = 'paid'
      AND COALESCE(o.paid_at, o.created_at) >= ?
      AND COALESCE(o.paid_at, o.created_at) < ?
) t
GROUP BY 1
ORDER BY 1`, reportTrunc(unit, "t.ev")), nil

	case ReportTypeSales:
		return fmt.Sprintf(`
SELECT %s AS series_key,
       COUNT(*)::bigint AS m1,
       COALESCE(SUM(t.subtotal_minor), 0)::bigint AS m2,
       COALESCE(SUM(t.discount_minor), 0)::bigint AS m3,
       COALESCE(SUM(t.tax_minor), 0)::bigint AS m4,
       COALESCE(SUM(t.total_minor), 0)::bigint AS m5
FROM (
    SELECT o.created_at AS ev, o.subtotal_minor, o.discount_minor, o.tax_minor, o.total_minor
    FROM orders o
    WHERE o.status <> 'failed'
      AND o.created_at >= ?
      AND o.created_at < ?
) t
GROUP BY 1
ORDER BY 1`, reportTrunc(unit, "t.ev")), nil

	case ReportTypeSubscriptions:
		// active_end is a snapshot at the bucket END (not additive),
		// so each series row carries its own correlated count. Buckets
		// come from the event streams only — no gap filling.
		bucket := fmt.Sprintf("date_trunc('%s', ev.at AT TIME ZONE 'UTC')", unit)
		end := bucket + " + INTERVAL '" + reportBucketEnd[groupByUnitName(unit)] + "'"
		return fmt.Sprintf(`
WITH ev AS (
    SELECT s.created_at AS at, 1 AS new_c, 0 AS canceled_c
    FROM subscriptions s
    WHERE s.created_at >= ? AND s.created_at < ?
    UNION ALL
    SELECT s.canceled_at AS at, 0 AS new_c, 1 AS canceled_c
    FROM subscriptions s
    WHERE s.canceled_at >= ? AND s.canceled_at < ?
)
SELECT to_char(%s, 'YYYY-MM-DD') AS series_key,
       (SELECT COUNT(*)::bigint FROM subscriptions s2
        WHERE s2.created_at < %s
          AND (s2.canceled_at IS NULL OR s2.canceled_at >= %s)) AS m1,
       COALESCE(SUM(ev.new_c), 0)::bigint AS m2,
       COALESCE(SUM(ev.canceled_c), 0)::bigint AS m3
FROM ev
GROUP BY %s
ORDER BY 1`, bucket, end, end, bucket), nil

	case ReportTypeRenewals:
		return fmt.Sprintf(`
SELECT %s AS series_key,
       COUNT(*)::bigint AS m1,
       COALESCE(SUM(t.amount_minor), 0)::bigint AS m2
FROM (
    SELECT lr.created_at AS ev,
           COALESCE((SELECT o.total_minor FROM orders o
                     WHERE o.external_id = lr.stripe_checkout_session_id
                       AND o.status = 'paid'
                     ORDER BY o.id LIMIT 1), 0) AS amount_minor
    FROM license_renewals lr
    WHERE lr.refunded_at IS NULL
      AND lr.created_at >= ? AND lr.created_at < ?
    UNION ALL
    SELECT COALESCE(o.paid_at, o.created_at) AS ev, o.total_minor AS amount_minor
    FROM orders o
    JOIN licenses l ON l.id = o.license_id
    WHERE o.status = 'paid'
      AND COALESCE(o.paid_at, o.created_at) >= ? AND COALESCE(o.paid_at, o.created_at) < ?
      AND o.external_id IS DISTINCT FROM l.stripe_checkout_session_id
      AND NOT EXISTS (SELECT 1 FROM license_renewals lr2
                      WHERE lr2.stripe_checkout_session_id = o.external_id)
) t
GROUP BY 1
ORDER BY 1`, reportTrunc(unit, "t.ev")), nil

	case ReportTypeChurn:
		return fmt.Sprintf(`
SELECT %s AS series_key,
       COUNT(*)::bigint AS m1,
       COALESCE(SUM(lo.total_minor), 0)::bigint AS m2
FROM subscriptions s
LEFT JOIN LATERAL (
    SELECT o.total_minor
    FROM orders o
    WHERE o.license_id = s.license_id
      AND o.status = 'paid'
      AND COALESCE(o.paid_at, o.created_at) <= s.canceled_at
    ORDER BY COALESCE(o.paid_at, o.created_at) DESC, o.id DESC
    LIMIT 1
) lo ON true
WHERE s.canceled_at >= ? AND s.canceled_at < ?
GROUP BY 1
ORDER BY 1`, reportTrunc(unit, "s.canceled_at")), nil

	case ReportTypeLicenses:
		// Four event streams — creation, first activation, expiry,
		// revocation — unioned as flag columns and summed per bucket.
		// Revocation has no stamped column (verified on the model), so
		// it is read as "status revoked, updated_at in the window":
		// the best available clock, documented as approximate.
		return fmt.Sprintf(`
SELECT %s AS series_key,
       COALESCE(SUM(t.created_c), 0)::bigint AS m1,
       COALESCE(SUM(t.activated_c), 0)::bigint AS m2,
       COALESCE(SUM(t.expired_c), 0)::bigint AS m3,
       COALESCE(SUM(t.revoked_c), 0)::bigint AS m4
FROM (
    SELECT l.created_at AS ev, 1 AS created_c, 0 AS activated_c, 0 AS expired_c, 0 AS revoked_c
    FROM licenses l
    WHERE l.created_at >= ? AND l.created_at < ?
    UNION ALL
    SELECT fa.ev, 0, 1, 0, 0
    FROM (SELECT MIN(a.created_at) AS ev FROM activations a GROUP BY a.license_id) fa
    WHERE fa.ev >= ? AND fa.ev < ?
    UNION ALL
    SELECT l.valid_until AS ev, 0, 0, 1, 0
    FROM licenses l
    WHERE l.valid_until >= ? AND l.valid_until < ?
    UNION ALL
    SELECT l.updated_at AS ev, 0, 0, 0, 1
    FROM licenses l
    WHERE l.status = 'revoked' AND l.updated_at >= ? AND l.updated_at < ?
) t
GROUP BY 1
ORDER BY 1`, reportTrunc(unit, "t.ev")), nil

	case ReportTypeActivations:
		return fmt.Sprintf(`
SELECT %s AS series_key,
       COUNT(*)::bigint AS m1
FROM activations a
WHERE a.created_at >= ? AND a.created_at < ?
GROUP BY 1
ORDER BY 1`, reportTrunc(unit, "a.created_at")), nil

	case ReportTypeDevices:
		// A device is a distinct device identifier (the schema keeps
		// one activation row per (license, identifier) and re-activation
		// only bumps last_verified, so rows are not device arrivals).
		// This report dedupes across licences and counts only
		// device-type identifiers: a device that activates two licences
		// is one device, and a user seat is not a device. The bucket is
		// the identifier's first sighting anywhere.
		return fmt.Sprintf(`
SELECT %s AS series_key,
       COUNT(DISTINCT d.identifier)::bigint AS m1
FROM (
    SELECT a.identifier, MIN(a.created_at) AS ev
    FROM activations a
    WHERE a.identifier_type = 'device'
    GROUP BY a.identifier
) d
WHERE d.ev >= ? AND d.ev < ?
GROUP BY 1
ORDER BY 1`, reportTrunc(unit, "d.ev")), nil

	case ReportTypeUsage:
		return fmt.Sprintf(`
SELECT %s AS series_key,
       COUNT(*)::bigint AS m1,
       COALESCE(SUM(u.quantity), 0)::bigint AS m2
FROM usage_events u
WHERE u.recorded_at >= ? AND u.recorded_at < ?
GROUP BY 1
ORDER BY 1`, reportTrunc(unit, "u.recorded_at")), nil

	case ReportTypeCustomers:
		// first_ev is the address's first paid order EVER (the window
		// is applied outside the window function), so an old customer
		// buying again is not a new one.
		return fmt.Sprintf(`
SELECT %s AS series_key,
       COUNT(DISTINCT CASE WHEN x.ev = x.first_ev THEN x.email END)::bigint AS m1,
       COUNT(DISTINCT x.email)::bigint AS m2
FROM (
    SELECT o.customer_email AS email,
           COALESCE(o.paid_at, o.created_at) AS ev,
           MIN(COALESCE(o.paid_at, o.created_at)) OVER (PARTITION BY o.customer_email) AS first_ev
    FROM orders o
    WHERE o.status = 'paid'
) x
WHERE x.ev >= ? AND x.ev < ?
GROUP BY 1
ORDER BY 1`, reportTrunc(unit, "x.ev")), nil

	case ReportTypeResellers:
		// Top list, not a time series: the key is the reseller, the
		// window bounds the orders, the accruals and the payouts that
		// feed each metric. FULL OUTER JOIN keeps partners active in
		// only one of the two ledgers visible.
		return `
WITH ord AS (
    SELECT o.reseller_id AS rid,
           COUNT(*)::bigint AS order_count,
           COALESCE(SUM(o.total_minor), 0)::bigint AS gross_minor
    FROM orders o
    WHERE o.reseller_id IS NOT NULL
      AND o.status = 'paid'
      AND COALESCE(o.paid_at, o.created_at) >= ? AND COALESCE(o.paid_at, o.created_at) < ?
    GROUP BY o.reseller_id
), com AS (
    SELECT c.reseller_id AS rid,
           COALESCE(SUM(c.amount_minor) FILTER (
               WHERE c.status <> 'cancelled' AND c.created_at >= ? AND c.created_at < ?), 0)::bigint AS accrued_minor,
           COALESCE(SUM(c.amount_minor) FILTER (
               WHERE c.status = 'paid' AND c.paid_at >= ? AND c.paid_at < ?), 0)::bigint AS paid_minor
    FROM commissions c
    WHERE (c.created_at >= ? AND c.created_at < ?)
       OR (c.status = 'paid' AND c.paid_at >= ? AND c.paid_at < ?)
    GROUP BY c.reseller_id
)
SELECT COALESCE(ord.rid, com.rid) AS series_key,
       COALESCE(ord.order_count, 0)::bigint AS m1,
       COALESCE(ord.gross_minor, 0)::bigint AS m2,
       COALESCE(com.accrued_minor, 0)::bigint AS m3,
       COALESCE(com.paid_minor, 0)::bigint AS m4
FROM ord
FULL OUTER JOIN com ON ord.rid = com.rid
ORDER BY m2 DESC, 1 ASC`, nil

	case ReportTypeAffiliates:
		// Top list, same shape as resellers. conversion_count and
		// commission_minor cover conversions recorded in the window
		// that earned (rejected and reversed ones earned nothing);
		// payout_minor is money that actually moved in the window.
		return `
WITH conv AS (
    SELECT ac.affiliate_id AS rid,
           COUNT(*)::bigint AS conversion_count,
           COALESCE(SUM(ac.commission_minor), 0)::bigint AS commission_minor
    FROM affiliate_conversions ac
    WHERE ac.created_at >= ? AND ac.created_at < ?
      AND ac.status NOT IN ('rejected', 'reversed')
    GROUP BY ac.affiliate_id
), pay AS (
    SELECT ap.affiliate_id AS rid,
           COALESCE(SUM(ap.amount_minor), 0)::bigint AS payout_minor
    FROM affiliate_payouts ap
    WHERE ap.status = 'paid' AND ap.paid_at >= ? AND ap.paid_at < ?
    GROUP BY ap.affiliate_id
)
SELECT COALESCE(conv.rid, pay.rid) AS series_key,
       COALESCE(conv.conversion_count, 0)::bigint AS m1,
       COALESCE(conv.commission_minor, 0)::bigint AS m2,
       COALESCE(pay.payout_minor, 0)::bigint AS m3
FROM conv
FULL OUTER JOIN pay ON conv.rid = pay.rid
ORDER BY m2 DESC, 1 ASC`, nil

	case ReportTypeRefunds:
		// refunded_at is the refund clock; the amount is the full
		// order total (the ledger's refund endpoint refunds whole
		// orders). Orders refunded outside any status flip still land
		// here — the timestamp is the signal.
		return fmt.Sprintf(`
SELECT %s AS series_key,
       COUNT(*)::bigint AS m1,
       COALESCE(SUM(o.total_minor), 0)::bigint AS m2
FROM orders o
WHERE o.refunded_at >= ? AND o.refunded_at < ?
GROUP BY 1
ORDER BY 1`, reportTrunc(unit, "o.refunded_at")), nil

	case ReportTypeFailedPayments:
		// No failed_at column exists on orders (verified on the
		// schema), so a failed payment is timestamped by the order it
		// was attempted on; failed_minor is the amount that did not
		// land.
		return fmt.Sprintf(`
SELECT %s AS series_key,
       COUNT(*)::bigint AS m1,
       COALESCE(SUM(o.total_minor), 0)::bigint AS m2
FROM orders o
WHERE o.status = 'failed' AND o.created_at >= ? AND o.created_at < ?
GROUP BY 1
ORDER BY 1`, reportTrunc(unit, "o.created_at")), nil
	}
	return "", nil
}

// groupByUnitName maps the trunc unit back to the groupBy word, for
// the one query that also needs the bucket-end interval. The two maps
// have the same keys; this keeps the SQL builder honest about which
// one it is reading.
func groupByUnitName(unit string) string {
	for g, u := range reportTruncUnit {
		if u == unit {
			return g
		}
	}
	return ReportGroupByDay
}

// reportWindowArgs returns the (from, toExclusive) binds every report
// window uses; the handler hands in inclusive calendar days, the SQL
// works in [from, to+1day).
func reportWindowArgs(from, to time.Time) []any {
	return []any{from, to.AddDate(0, 0, 1)}
}

// repeatWindows returns n window bind pairs in a row, for the reports
// whose SQL is a union of several windowed event streams (the pairs
// are read in textual placeholder order).
func repeatWindows(from, to time.Time, n int) []any {
	win := reportWindowArgs(from, to)
	out := make([]any, 0, len(win)*n)
	for i := 0; i < n; i++ {
		out = append(out, win...)
	}
	return out
}

// RunReport executes one of the fourteen plan §43 reports.
//
// groupBy ("day" | "week" | "month") buckets the time-series reports
// and is ignored by the two list reports (resellers, affiliates),
// whose rows are grouped by partner instead. The returned series has
// no gap filling; Totals covers the whole window and always carries
// every metric name of the report (zero when there were no rows).
func (s *Store) RunReport(ctx context.Context, reportType string, from, to time.Time, groupBy string) (*ReportResult, error) {
	if groupBy == "" {
		groupBy = ReportGroupByDay
	}
	if err := ValidateReportParams(reportType, groupBy, from, to); err != nil {
		return nil, err
	}
	unit := reportTruncUnit[groupBy]
	sqlText, queryArgs := reportSeriesQuery(reportType, unit)

	win := reportWindowArgs(from, to)
	switch reportType {
	case ReportTypeRenewals, ReportTypeSubscriptions, ReportTypeAffiliates:
		// Two event streams, each with its own window binds.
		queryArgs = repeatWindows(from, to, 2)
	case ReportTypeLicenses:
		// Four event streams: creation, first activation, expiry,
		// revocation.
		queryArgs = repeatWindows(from, to, 4)
	case ReportTypeResellers:
		// Order window, then the commission binds: accrual window in
		// both FILTERs and in the prefilter, payout window likewise.
		queryArgs = repeatWindows(from, to, 5)
	default:
		queryArgs = win
	}

	var scanRows []reportScanRow
	if err := s.DB.NewRaw(sqlText, queryArgs...).Scan(ctx, &scanRows); err != nil {
		return nil, fmt.Errorf("run %s report: %w", reportType, err)
	}

	names := ReportMetricNames[reportType]
	series := make([]ReportRow, 0, len(scanRows))
	for _, r := range scanRows {
		row := ReportRow{Metrics: make(map[string]int64, len(names))}
		switch reportType {
		case ReportTypeResellers:
			row.ResellerID = r.Key
		case ReportTypeAffiliates:
			row.AffiliateID = r.Key
		default:
			row.Period = r.Key
		}
		vals := r.vals()
		for i, name := range names {
			row.Metrics[name] = vals[i]
		}
		series = append(series, row)
	}

	totals := make(map[string]int64, len(names))
	for _, name := range names {
		totals[name] = 0
	}
	switch reportType {
	case ReportTypeSubscriptions, ReportTypeCustomers:
		// Two metrics are not additive — active_end is a snapshot and
		// buying_customers_count is a distinct count — so these two
		// reports get their totals recomputed over the whole window
		// instead of summed from the series.
		var trow reportScanRow
		tSQL, tArgs := reportTotalsQuery(reportType, from, to)
		if err := s.DB.NewRaw(tSQL, tArgs...).Scan(ctx, &trow); err != nil {
			return nil, fmt.Errorf("run %s report totals: %w", reportType, err)
		}
		vals := trow.vals()
		for i, name := range names {
			totals[name] = vals[i]
		}
	default:
		// Every other report's metrics are additive over disjoint
		// buckets, so the series sums are exactly the window totals.
		for _, row := range series {
			for name, v := range row.Metrics {
				totals[name] += v
			}
		}
	}

	return &ReportResult{
		ReportType: reportType,
		GroupBy:    groupBy,
		From:       from.Format("2006-01-02"),
		To:         to.Format("2006-01-02"),
		Series:     series,
		Totals:     totals,
	}, nil
}

// reportTotalsQuery is the whole-window aggregate for the two reports
// with non-additive metrics. Same aliases as the series queries, so
// the same scanner reads it.
func reportTotalsQuery(reportType string, from, to time.Time) (string, []any) {
	toExclusive := to.AddDate(0, 0, 1)
	if reportType == ReportTypeSubscriptions {
		// active_end is the snapshot at the window end (both binds of
		// the first subquery); the other two are window sums.
		return `
SELECT (SELECT COUNT(*)::bigint FROM subscriptions s
        WHERE s.created_at < ? AND (s.canceled_at IS NULL OR s.canceled_at >= ?)) AS m1,
       (SELECT COUNT(*)::bigint FROM subscriptions s
        WHERE s.created_at >= ? AND s.created_at < ?) AS m2,
       (SELECT COUNT(*)::bigint FROM subscriptions s
        WHERE s.canceled_at >= ? AND s.canceled_at < ?) AS m3`,
			[]any{toExclusive, toExclusive, from, toExclusive, from, toExclusive}
	}
	return `
SELECT COUNT(DISTINCT CASE WHEN x.ev = x.first_ev THEN x.email END)::bigint AS m1,
       COUNT(DISTINCT x.email)::bigint AS m2
FROM (
    SELECT o.customer_email AS email,
           COALESCE(o.paid_at, o.created_at) AS ev,
           MIN(COALESCE(o.paid_at, o.created_at)) OVER (PARTITION BY o.customer_email) AS first_ev
    FROM orders o
    WHERE o.status = 'paid'
) x
WHERE x.ev >= ? AND x.ev < ?`, []any{from, toExclusive}
}
