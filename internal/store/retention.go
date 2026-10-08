// Data retention (plan §92): batched deletion of the operational
// rows that grow without bound.
//
// FOUR tables are ever deleted from here — notifications (the
// email-dedup ledger), processed_events (IPN/webhook idempotency
// claims), webhook_deliveries (merchant webhook outbox) and
// audit_logs — and only past their configured horizon.
//
// FINANCIAL RECORDS ARE NEVER DELETED. orders, order_items, invoices,
// refunds, gateway_payments, license_renewals, commissions and
// affiliate_conversions are the money trail and deliberately have NO
// delete method in this file (docs/DATA-RETENTION.md is the rules
// table). A retention run cannot reach them.
//
// Every method deletes at most one bounded batch and reports what it
// deleted; the caller loops. Re-running is idempotent: there is no
// state to corrupt, and a second pass over an already-clean table
// deletes 0 rows. Each batch is a single statement keyed on id, so a
// cancelled run leaves the table consistent.
//
// The Bun alias rules do not apply here — these are raw SQL
// statements with explicit table names, no model queries.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"
)

// RetentionBatchSize is the default deletion batch: small enough to
// keep each statement's row locks short on a busy table, large enough
// that a horizon's worth of rows drains in a handful of statements.
const RetentionBatchSize = 1000

// retainDeleteBatch deletes one batch of rows older than before from
// table, judged on clock column. table and clock are code constants
// from the four exported wrappers — never caller input (prefix
// `retain` — data-retention domain).
func retainDeleteBatch(ctx context.Context, db bun.IDB, table, clock string, before time.Time, batch int) (int64, error) {
	if batch <= 0 {
		batch = RetentionBatchSize
	}
	res, err := db.NewRaw(fmt.Sprintf(
		`DELETE FROM %s WHERE id IN (SELECT id FROM %s WHERE %s < ? LIMIT ?)`,
		table, table, clock), before, batch).Exec(ctx)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// DeleteNotificationsBefore removes one batch of the email-dedup
// notifications ledger. The clock is sent_at — the one time column
// this table has (it stamps when the mail was sent).
func (s *Store) DeleteNotificationsBefore(ctx context.Context, before time.Time, batch int) (int64, error) {
	return retainDeleteBatch(ctx, s.DB, "notifications", "sent_at", before, batch)
}

// DeleteProcessedEventsBefore removes one batch of processed
// IPN/webhook event claims. These guard replay windows of hours —
// the retention horizon for them is short by design.
func (s *Store) DeleteProcessedEventsBefore(ctx context.Context, before time.Time, batch int) (int64, error) {
	return retainDeleteBatch(ctx, s.DB, "processed_events", "created_at", before, batch)
}

// DeleteWebhookDeliveriesBefore removes one batch of merchant webhook
// deliveries (the outbox, payload included). customer_webhooks and
// their secrets are configuration, not history — never touched here.
func (s *Store) DeleteWebhookDeliveriesBefore(ctx context.Context, before time.Time, batch int) (int64, error) {
	return retainDeleteBatch(ctx, s.DB, "webhook_deliveries", "created_at", before, batch)
}

// DeleteAuditLogsBefore removes one batch of audit-log rows past the
// horizon. The longest horizon of the four (a year by default): the
// audit trail is the first thing an investigator asks for.
func (s *Store) DeleteAuditLogsBefore(ctx context.Context, before time.Time, batch int) (int64, error) {
	return retainDeleteBatch(ctx, s.DB, "audit_logs", "created_at", before, batch)
}
