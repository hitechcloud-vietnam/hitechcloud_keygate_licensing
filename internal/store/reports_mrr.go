// MRR / ARR metrics (plan §42).
//
// MRR(at) is the normalized monthly value of the subscriptions ACTIVE
// at that instant; ARR(at) is MRR × 12. The normalization is integer
// math (plan §51 — money is int64 minor units, never float64):
//
//	monthly billing interval → the amount as-is
//	yearly  billing interval → (amount + 6) / 12
//
// which is amount/12 rounded half-up for every non-negative amount
// (Postgres and Go integer division both truncate toward zero, and
// adding half the divisor before dividing is the exact half-up form
// for non-negative values). The rounding is pinned by
// reports_mrr_test.go.
//
// Where the amount comes from, in order (there is no local
// subscription_items table — prices live on Stripe per
// plan.StripePriceID):
//
//  1. the plan's plan_prices row (§53): the default row, else the
//     oldest row, else
//  2. the licence's most recent PAID order at or before the instant
//     — what the customer was actually billed (the same source the
//     churn proxy in reports.go uses), else
//  3. the subscription is EXCLUDED: no local amount, no invented one.
//
// Excluded from MRR by design, each documented: perpetual plans
// (billing_interval is ” — a one-time sale is not recurring
// revenue), trials (status 'trialing' is not yet paying), metered /
// usage-based components (Stripe metered events never reach the order
// ledger, so there is no local amount — never estimated), and
// subscriptions canceled / expired / revoked / suspended at the
// instant. Counted: status 'active' AND 'past_due' (dunning is still
// expected revenue).
//
// Series conventions match internal/store/reports.go exactly: buckets
// are date_trunc at day|week|month in UTC, the window is inclusive
// calendar days [from, to] with the snapshot taken at each bucket
// START, the span is capped at ReportMaxSpanYears, and there is NO
// gap-fill — a bucket where nothing was recurring simply has no row.
//
// Multi-currency caveat (same as reports.go): amounts are summed
// across currencies as minor units and nothing here converts; the
// snapshot's Currency field reports the currency when every
// contributing row agreed on one, "mixed" when they did not.
package store

import (
	"context"
	"fmt"
	"time"
)

// MRRSnapshot is one MRR/ARR answer: the two money figures (int64
// minor units) and the currency label described above.
type MRRSnapshot struct {
	MRRMinor int64  `json:"mrr_minor"`
	ARRMinor int64  `json:"arr_minor"`
	Currency string `json:"currency"`
}

// MRRSeriesPoint is one bucket of the MRR series. Ts is the UTC
// bucket start as YYYY-MM-DD — the same key format reports.go uses.
type MRRSeriesPoint struct {
	Ts       string `json:"ts"`
	MRRMinor int64  `json:"mrr_minor"`
	ARRMinor int64  `json:"arr_minor"`
}

// mrrNormalizeMonthly converts one recurring amount to its monthly
// value: as-is for a monthly price, amount/12 rounded half-up for a
// yearly one. ok is false for an interval that is not recurring —
// the caller excludes the subscription rather than counting it at 0.
func mrrNormalizeMonthly(amountMinor int64, billingInterval string) (int64, bool) {
	switch billingInterval {
	case "month":
		return amountMinor, true
	case "year":
		return (amountMinor + 6) / 12, true
	}
	return 0, false
}

// mrrArr multiplies a monthly figure into its yearly one. Integer
// math end to end.
func mrrArr(mrrMinor int64) int64 { return mrrMinor * 12 }

// mrrValidateSeries pins the MRRSeries window rules to the report
// ones: grain is day|week|month, from ≤ to, span ≤ 3 years.
func mrrValidateSeries(from, to time.Time, grain string) error {
	if !ValidReportGroupBy(grain) {
		return fmt.Errorf("%w: %q", ErrInvalidGroupBy, grain)
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

// mrrCurrencyLabel collapses the currencies one snapshot summed: the
// code when every row agreed, "mixed" when they did not, "" when
// nothing contributed.
func mrrCurrencyLabel(currencies map[string]struct{}) string {
	switch len(currencies) {
	case 0:
		return ""
	case 1:
		for c := range currencies {
			return c
		}
	}
	return "mixed"
}

// mrrScanRow is the one shape both MRR queries return: the bucket key
// (empty for the point-in-time query), the recurring amount of one
// subscription, its billing interval and its currency. The
// normalization happens in Go — one math implementation
// (mrrNormalizeMonthly) for the point and the series alike.
type mrrScanRow struct {
	SeriesKey       string `bun:"series_key"`
	AmountMinor     int64  `bun:"amount_minor"`
	Currency        string `bun:"currency"`
	BillingInterval string `bun:"billing_interval"`
}

// mrrPriceLateral is the per-subscription price source shared by both
// queries: the plan's plan_prices row (default first) else the
// licence's most recent paid order at or before the query instant.
// The snapshot instant is rendered as %s so the point query binds ?
// and the series query reads the bucket-start column.
const mrrPriceLateral = `
    JOIN LATERAL (
        SELECT COALESCE(pp.amount_minor, lo.total_minor) AS amount_minor,
               COALESCE(pp.currency, lo.currency, '')    AS currency
        FROM (SELECT 1) AS seed
        LEFT JOIN LATERAL (
            SELECT pp2.amount_minor, pp2.currency
            FROM plan_prices pp2
            WHERE pp2.plan_id = p.id
            ORDER BY pp2.is_default DESC, pp2.id ASC
            LIMIT 1
        ) pp ON true
        LEFT JOIN LATERAL (
            SELECT o.total_minor, o.currency
            FROM orders o
            WHERE o.license_id = s.license_id
              AND o.status IN ('paid', 'partially_refunded')
              AND COALESCE(o.paid_at, o.created_at) <= %s
            ORDER BY COALESCE(o.paid_at, o.created_at) DESC, o.id DESC
            LIMIT 1
        ) lo ON true
    ) price ON true`

// mrrActiveWhere is the "active at the instant" predicate shared by
// both queries (second ?): created by then, not canceled before it,
// and in a status that still bills.
const mrrActiveWhere = `
    WHERE s.status IN ('active', 'past_due')
      AND s.created_at <= ?
      AND (s.canceled_at IS NULL OR s.canceled_at > ?)
      AND p.billing_interval IN ('month', 'year')
      AND price.amount_minor IS NOT NULL`

// MRR returns the normalized monthly recurring value of the
// subscriptions active at at, with ARR as MRR × 12. See the package
// comment for the normalization, the price source and the exclusions.
func (s *Store) MRR(ctx context.Context, at time.Time) (*MRRSnapshot, error) {
	if at.IsZero() {
		at = time.Now()
	}
	sqlText := `
SELECT price.amount_minor AS amount_minor,
       price.currency     AS currency,
       p.billing_interval AS billing_interval
FROM subscriptions s
JOIN plans p ON p.id = s.plan_id` + fmt.Sprintf(mrrPriceLateral, "?") + mrrActiveWhere

	var rows []mrrScanRow
	if err := s.DB.NewRaw(sqlText, at, at, at).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("compute MRR: %w", err)
	}

	var mrr int64
	currencies := map[string]struct{}{}
	for _, r := range rows {
		monthly, ok := mrrNormalizeMonthly(r.AmountMinor, r.BillingInterval)
		if !ok {
			continue // not a recurring interval — excluded, not zeroed
		}
		mrr += monthly
		if r.Currency != "" {
			currencies[r.Currency] = struct{}{}
		}
	}
	return &MRRSnapshot{
		MRRMinor: mrr,
		ARRMinor: mrrArr(mrr),
		Currency: mrrCurrencyLabel(currencies),
	}, nil
}

// ARR is the yearly figure: MRR × 12 at the same instant. It shares
// MRR's snapshot so the two can never disagree.
func (s *Store) ARR(ctx context.Context, at time.Time) (*MRRSnapshot, error) {
	return s.MRR(ctx, at)
}

// MRRSeries walks [from, to] in UTC buckets of grain (day|week|month)
// and reports the MRR at each bucket START. Conventions are the
// reports.go ones: inclusive calendar days, a ReportMaxSpanYears
// span cap, and no gap-fill — buckets where nothing was recurring
// have no row. Each point carries ARR = MRR × 12.
func (s *Store) MRRSeries(ctx context.Context, from, to time.Time, grain string) ([]MRRSeriesPoint, error) {
	if grain == "" {
		grain = ReportGroupByDay
	}
	if err := mrrValidateSeries(from, to, grain); err != nil {
		return nil, err
	}
	unit := reportTruncUnit[grain]

	// The lateral's snapshot instant reads the bucket start directly
	// (b.ts); the only binds are generate_series's own.
	sqlText := fmt.Sprintf(`
SELECT to_char(date_trunc('%s', b.ts AT TIME ZONE 'UTC'), 'YYYY-MM-DD') AS series_key,
       price.amount_minor AS amount_minor,
       price.currency     AS currency,
       p.billing_interval AS billing_interval
FROM generate_series(?::timestamptz, ?::timestamptz, ?::interval) AS b(ts)
JOIN subscriptions s ON s.created_at <= b.ts
                    AND (s.canceled_at IS NULL OR s.canceled_at > b.ts)
JOIN plans p ON p.id = s.plan_id
            AND p.billing_interval IN ('month', 'year')`+
		fmt.Sprintf(mrrPriceLateral, "b.ts")+
		`
WHERE s.status IN ('active', 'past_due')
  AND price.amount_minor IS NOT NULL
ORDER BY 1`, unit)

	var rows []mrrScanRow
	if err := s.DB.NewRaw(sqlText, from, to, reportBucketEnd[grain]).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("compute MRR series: %w", err)
	}

	// One point per bucket: normalize and sum the rows of each key.
	// The query ordered by the key, so equal keys are contiguous.
	out := make([]MRRSeriesPoint, 0, len(rows))
	for i := 0; i < len(rows); {
		key := rows[i].SeriesKey
		var mrr int64
		for i < len(rows) && rows[i].SeriesKey == key {
			if monthly, ok := mrrNormalizeMonthly(rows[i].AmountMinor, rows[i].BillingInterval); ok {
				mrr += monthly
			}
			i++
		}
		out = append(out, MRRSeriesPoint{Ts: key, MRRMinor: mrr, ARRMinor: mrrArr(mrr)})
	}
	return out, nil
}
