// Data retention job (plan §92): deletes operational rows past their
// configured horizon, in bounded batches, and never touches financial
// records (store/retention.go holds the four delete methods and the
// never-delete list; docs/DATA-RETENTION.md is the rules table).
//
// The horizons are operator settings read at run time, in DAYS:
//
//	retention.notifications_days       default 90
//	retention.processed_events_days    default 30
//	retention.webhook_deliveries_days  default 90
//	retention.audit_logs_days          default 365
//
// A missing or unparseable setting falls back to its default; 0 means
// NEVER delete that table. Negative values are unparseable intent and
// fall back too — the safe direction is always the default, never
// "delete everything".
//
// A run is idempotent: it deletes only rows strictly older than
// now − horizon, so running twice deletes the same nothing the second
// time. RunRetention is the one pass the admin endpoint reports on;
// RetentionJob wraps the same pass for the background loops
// (cmd/server/main.go schedules it, exactly like the hourly cleanup
// loop).
package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// Retention setting keys — EXACT strings, shared with the settings
// seed another agent writes. Renaming one silently resets that
// horizon to its default.
const (
	RetentionKeyNotifications     = "retention.notifications_days"
	RetentionKeyProcessedEvents   = "retention.processed_events_days"
	RetentionKeyWebhookDeliveries = "retention.webhook_deliveries_days"
	RetentionKeyAuditLogs         = "retention.audit_logs_days"
)

// RetentionDefaults are the horizons a missing (or broken) setting
// falls back to, in days. 0 = never delete.
var RetentionDefaults = map[string]int64{
	RetentionKeyNotifications:     90,
	RetentionKeyProcessedEvents:   30,
	RetentionKeyWebhookDeliveries: 90,
	RetentionKeyAuditLogs:         365,
}

// Retention tables, in the order a run drains them — cheap and hot
// first, the audit trail last.
const (
	RetentionTableNotifications     = "notifications"
	RetentionTableProcessedEvents   = "processed_events"
	RetentionTableWebhookDeliveries = "webhook_deliveries"
	RetentionTableAuditLogs         = "audit_logs"
)

// RetentionResult is one pass: how many rows went from each table.
// The map always carries all four table names (0 when nothing went).
type RetentionResult struct {
	Deleted map[string]int64 `json:"deleted"`
}

// retainStore is the slice of store.Store a retention pass needs —
// the settings reader and the four batch deleters (prefix `retain`).
// The real wiring passes *store.Store; tests pass a fake.
type retainStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	DeleteNotificationsBefore(ctx context.Context, before time.Time, batch int) (int64, error)
	DeleteProcessedEventsBefore(ctx context.Context, before time.Time, batch int) (int64, error)
	DeleteWebhookDeliveriesBefore(ctx context.Context, before time.Time, batch int) (int64, error)
	DeleteAuditLogsBefore(ctx context.Context, before time.Time, batch int) (int64, error)
}

// retainBatch is how many rows one deletion statement takes. Small
// enough to keep row locks short, big enough that a year of audit
// rows drains quickly.
const retainBatch = 1000

// retainDays reads one horizon setting in days. sql.ErrNoRows, an
// empty value, a non-integer and a negative number all fall back to
// the key's default; 0 is returned as-is and means "never delete".
func retainDays(ctx context.Context, s retainStore, key string) (int64, error) {
	raw, err := s.GetSetting(ctx, key)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		raw = ""
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return RetentionDefaults[key], nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return RetentionDefaults[key], nil
	}
	return n, nil
}

// retainCutOff is the instant rows must predate to go: now minus the
// horizon, in calendar days. A zero horizon returns ok=false — the
// table is never deleted.
func retainCutOff(now time.Time, days int64) (time.Time, bool) {
	if days <= 0 {
		return time.Time{}, false
	}
	return now.AddDate(0, 0, -int(days)), true
}

// RunRetention is one retention pass: each table's horizon is read
// from settings, and rows strictly older than now − horizon are
// deleted in batches until the table is clean past the line. It
// returns the per-table counts and never touches financial records.
func RunRetention(ctx context.Context, s retainStore) (*RetentionResult, error) {
	now := time.Now()
	out := &RetentionResult{Deleted: map[string]int64{}}

	drain := func(table, key string, del func(context.Context, time.Time, int) (int64, error)) error {
		days, err := retainDays(ctx, s, key)
		if err != nil {
			return err
		}
		cut, ok := retainCutOff(now, days)
		if !ok {
			out.Deleted[table] = 0 // configured never — explicitly, not by omission
			return nil
		}
		var total int64
		for {
			n, err := del(ctx, cut, retainBatch)
			if err != nil {
				return err
			}
			total += n
			if n < retainBatch {
				break // the table is clean past the line
			}
		}
		out.Deleted[table] = total
		return nil
	}

	if err := drain(RetentionTableNotifications, RetentionKeyNotifications, s.DeleteNotificationsBefore); err != nil {
		return nil, err
	}
	if err := drain(RetentionTableProcessedEvents, RetentionKeyProcessedEvents, s.DeleteProcessedEventsBefore); err != nil {
		return nil, err
	}
	if err := drain(RetentionTableWebhookDeliveries, RetentionKeyWebhookDeliveries, s.DeleteWebhookDeliveriesBefore); err != nil {
		return nil, err
	}
	if err := drain(RetentionTableAuditLogs, RetentionKeyAuditLogs, s.DeleteAuditLogsBefore); err != nil {
		return nil, err
	}
	return out, nil
}

// RetentionJob is the background-loop shape of one retention pass.
// Lead schedules it exactly like the hourly cleanup loop in
// cmd/server/main.go (a ticker feeding Run). It holds no state and a
// missed tick loses nothing — the pass is idempotent.
type RetentionJob struct {
	store  retainStore
	logger *slog.Logger
}

// NewRetentionJob wires the job. logger may be nil.
func NewRetentionJob(s retainStore, logger *slog.Logger) *RetentionJob {
	return &RetentionJob{store: s, logger: logger}
}

// Run performs one pass and logs its outcome. The error is returned
// so a scheduler can count failures; a partial pass still reports the
// counts it managed (the map is filled as it goes).
func (j *RetentionJob) Run(ctx context.Context) error {
	res, err := RunRetention(ctx, j.store)
	if err != nil {
		if j.logger != nil {
			j.logger.Error("retention pass failed", "error", err)
		}
		return err
	}
	if j.logger != nil {
		j.logger.Info("retention pass done",
			"notifications", res.Deleted[RetentionTableNotifications],
			"processed_events", res.Deleted[RetentionTableProcessedEvents],
			"webhook_deliveries", res.Deleted[RetentionTableWebhookDeliveries],
			"audit_logs", res.Deleted[RetentionTableAuditLogs],
		)
	}
	return nil
}
