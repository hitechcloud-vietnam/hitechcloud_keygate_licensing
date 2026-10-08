// Refunds (plan §79) and revocation provenance (plan §80).
//
// The refund row is a financial record: written for every refund —
// full, partial or manual — and never deleted (retention excludes the
// money tables, docs/DATA-RETENTION.md). This layer persists; it does
// not decide. The eligibility rules (a paid order, an amount within
// what is left) live in payment.RefundOrder, and the fulfilment
// effects (order status, invoice, licence, clawbacks) are driven from
// there through the methods here.
//
// Money discipline (plan §51): every amount is int64 minor units.
//
// The Bun alias for model.Refund is "refund" (snake_case of the
// STRUCT name) — pinned by refunds_test.go. Qualify with "refund".x,
// never refunds.x (bun_alias_test.go).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// Refund-layer sentinels.
var (
	// ErrRefundNotFound: FindRefundByID named no row.
	ErrRefundNotFound = errors.New("refund not found")
	// ErrInvalidRevokeReason: RevokeLicenseWithReason was handed a
	// reason outside model.RevokeReasons(). Callers fold an empty
	// reason to model.DefaultRevokeReason first; this is the store
	// refusing to persist something the vocabulary does not know.
	ErrInvalidRevokeReason = errors.New("invalid revocation reason")
)

// refundSelectByOrder builds the per-order refund list. Extracted as
// a builder so the alias and the order are pinnable without a
// database (refunds_test.go). An empty sort keeps the list's own
// order, newest first (created_at then id — the same total order
// every admin list uses).
func refundSelectByOrder(db bun.IDB, orderID string, sort Sort) *bun.SelectQuery {
	if sort.Expr == "" {
		sort = Sort{Expr: "refund.created_at", Desc: true}
	}
	q := db.NewSelect().Model((*model.Refund)(nil)).
		Where("refund.order_id = ?", orderID)
	return applySort(q, sort, "refund.id")
}

// CreateRefund writes one refund row. The row's status comes from the
// caller — a gateway refund of an asynchronous gateway is 'pending'
// until reconciled, a manual one is 'succeeded' the moment it is
// written — and the money fields are stored exactly as given.
func (s *Store) CreateRefund(ctx context.Context, r *model.Refund) error {
	if r == nil {
		return errors.New("refund is required")
	}
	_, err := s.DB.NewInsert().Model(r).Returning("id").Exec(ctx)
	return err
}

// FindRefundByID returns one refund row. A miss is ErrRefundNotFound.
func (s *Store) FindRefundByID(ctx context.Context, id int64) (*model.Refund, error) {
	r := new(model.Refund)
	err := s.DB.NewSelect().Model(r).Where("refund.id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrRefundNotFound
	}
	return r, err
}

// ListRefundsByOrder returns every refund recorded against an order,
// newest first (or in the validated order the caller asked for). The
// list is the audit trail of the money that went back and is never
// filtered — pending and failed rows are history too.
func (s *Store) ListRefundsByOrder(ctx context.Context, orderID string, sort Sort) ([]*model.Refund, error) {
	var rows []*model.Refund
	if err := refundSelectByOrder(s.DB, orderID, sort).Scan(ctx, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// RefundSumsByOrder returns the two sums a refund decision needs:
//
//	committed  the money already committed to going back — succeeded
//	           refunds plus pending ones whose gateway has not
//	           settled yet. This is what caps a new refund: pending
//	           amounts are reserved or two async refunds could both
//	           fit under the bar.
//	succeeded  the money actually returned. This is what
//	           model.Order.RefundedMinor mirrors and what decides
//	           refunded vs partially_refunded.
//
// A failed refund counts toward neither — its amount freed up again.
func (s *Store) RefundSumsByOrder(ctx context.Context, orderID string) (committed, succeeded int64, err error) {
	var row struct {
		Committed int64 `bun:"committed"`
		Succeeded int64 `bun:"succeeded"`
	}
	err = s.DB.NewSelect().Model((*model.Refund)(nil)).
		ColumnExpr(`COALESCE(SUM(refund.amount_minor) FILTER (WHERE refund.status IN ('pending', 'succeeded')), 0) AS committed`).
		ColumnExpr(`COALESCE(SUM(refund.amount_minor) FILTER (WHERE refund.status = 'succeeded'), 0) AS succeeded`).
		Where("refund.order_id = ?", orderID).
		Scan(ctx, &row)
	if err != nil {
		return 0, 0, err
	}
	return row.Committed, row.Succeeded, nil
}

// UpdateRefundStatus settles a pending refund row — the hook a refund
// reconciliation job uses when an asynchronous gateway (ZaloPay)
// reports the final state. providerRef and transID are stored when
// non-empty (the gateway may hand them out only at settlement).
func (s *Store) UpdateRefundStatus(ctx context.Context, id int64, status, providerRef, transID string) error {
	if !model.ValidRefundStatus(status) {
		return fmt.Errorf("invalid refund status %q", status)
	}
	q := s.DB.NewUpdate().Model((*model.Refund)(nil)).
		Set("status = ?", status).
		Set("updated_at = ?", time.Now()).
		Where("refund.id = ?", id)
	if providerRef != "" {
		q = q.Set("provider_ref = ?", providerRef)
	}
	if transID != "" {
		q = q.Set("trans_id = ?", transID)
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRefundNotFound
	}
	return nil
}

// refundSyncOrderIn re-derives the order's refund progress from the
// ledger and writes it: refunded_minor is the SUM of succeeded
// refunds (never hand-maintained, so it cannot drift), and the status
// follows the pinned rules —
//
//	succeeded == 0        → the order's status is untouched (a
//	                       pending async refund changes nothing yet)
//	succeeded <  total    → partially_refunded (RefundedAt stays nil:
//	                       the order is still open business)
//	succeeded >= total    → refunded, with refunded_at stamped NOW
//
// Historical totals are never recalculated: total_minor is untouched.
// Shared by RecordRefund (after its insert) and SyncOrderRefundState
// (after an async refund settles) — one derivation, one truth.
func refundSyncOrderIn(ctx context.Context, tx bun.IDB, orderID string) (*model.Order, error) {
	var sums struct {
		Succeeded int64 `bun:"succeeded"`
	}
	if err := tx.NewSelect().Model((*model.Refund)(nil)).
		ColumnExpr(`COALESCE(SUM(refund.amount_minor) FILTER (WHERE refund.status = 'succeeded'), 0) AS succeeded`).
		Where("refund.order_id = ?", orderID).
		Scan(ctx, &sums); err != nil {
		return nil, err
	}
	o := new(model.Order)
	if err := tx.NewSelect().Model(o).Where(`"order".id = ?`, orderID).Scan(ctx); err != nil {
		return nil, err
	}
	o.RefundedMinor = sums.Succeeded
	q := tx.NewUpdate().Model((*model.Order)(nil)).
		Set("refunded_minor = ?", sums.Succeeded).
		Set("updated_at = ?", time.Now()).
		Where(`"order".id = ?`, orderID)
	switch {
	case sums.Succeeded == 0:
		// Nothing has actually come back: only the sum is synced and
		// the status is left exactly as it was.
	case sums.Succeeded >= o.TotalMinor:
		now := time.Now()
		o.Status = model.OrderStatusRefunded
		o.RefundedAt = &now
		q = q.Set("status = ?", model.OrderStatusRefunded).Set("refunded_at = ?", now)
	default:
		o.Status = model.OrderStatusPartiallyRefunded
		q = q.Set("status = ?", model.OrderStatusPartiallyRefunded)
	}
	if _, err := q.Exec(ctx); err != nil {
		return nil, err
	}
	return o, nil
}

// RecordRefund writes a refund row AND the order's refund progress in
// one transaction, then returns the order as it now stands. See
// refundSyncOrderIn for the derivation rules.
func (s *Store) RecordRefund(ctx context.Context, r *model.Refund) (*model.Order, error) {
	if r == nil {
		return nil, errors.New("refund is required")
	}
	var out *model.Order
	err := RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewInsert().Model(r).Exec(ctx); err != nil {
			return err
		}
		o, err := refundSyncOrderIn(ctx, tx, r.OrderID)
		if err != nil {
			return err
		}
		out = o
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SyncOrderRefundState re-derives and writes an order's refund
// progress from the refunds ledger. It inserts nothing — it is the
// settle half of the async flow (ReconcileRefund) and the self-heal
// for any path that changed refund rows outside RecordRefund.
func (s *Store) SyncOrderRefundState(ctx context.Context, orderID string) (*model.Order, error) {
	var out *model.Order
	err := RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		o, err := refundSyncOrderIn(ctx, tx, orderID)
		if err != nil {
			return err
		}
		out = o
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RevokeLicenseWithReason revokes a licence and records why (§80):
// the reason from the closed vocabulary, the actor who asked, and the
// stamp — beside the status flip RevokeLicense already performs
// (activations dropped, subscriptions marked revoked).
//
// The reason is validated here, not only at the HTTP edge: what
// reaches licenses.revoke_reason is always a member of
// model.RevokeReasons(). An empty reason folds to
// model.DefaultRevokeReason; anything else unknown is
// ErrInvalidRevokeReason.
func (s *Store) RevokeLicenseWithReason(ctx context.Context, id, reason, actor string) error {
	if reason == "" {
		reason = model.DefaultRevokeReason
	}
	if !model.ValidRevokeReason(reason) {
		return ErrInvalidRevokeReason
	}
	now := time.Now()
	_, _ = s.DB.NewDelete().Model((*model.Activation)(nil)).Where("license_id = ?", id).Exec(ctx)
	res, err := s.DB.NewUpdate().Model((*model.License)(nil)).
		Set("status = ?", model.StatusRevoked).
		Set("revoke_reason = ?", reason).
		Set("revoked_by = ?", actor).
		Set("revoked_at = ?", now).
		Set("updated_at = ?", now).
		Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("license not found")
	}
	_, _ = s.DB.NewRaw(`UPDATE subscriptions SET status = ?, updated_at = now() WHERE license_id = ?`,
		model.StatusRevoked, id).Exec(ctx)
	return nil
}

// CancelCommissionForOrder claws back the reseller commission
// recorded for an order (the refund's fulfilment hook).
//
// The clawback only moves money that has not moved yet: accrued and
// approved commissions become cancelled, and a commission already
// marked PAID is left alone — the money is out the door and taking it
// back is a conversation, not a database write. Returns whether a
// commission was cancelled, so the caller can audit the outcome.
//
// Deliberately narrow: one order's commission, found by its unique
// (reseller_id, order_id) pair. A miss is not an error — an order no
// partner brought has nothing to claw back.
func (s *Store) CancelCommissionForOrder(ctx context.Context, resellerID, orderID string) (bool, error) {
	if resellerID == "" || orderID == "" {
		return false, nil
	}
	res, err := s.DB.NewUpdate().Model((*model.Commission)(nil)).
		Set("status = ?", model.CommissionStatusCancelled).
		Set("updated_at = ?", time.Now()).
		Where("reseller_id = ?", resellerID).
		Where("order_id = ?", orderID).
		Where("status IN (?)", bun.In([]string{
			model.CommissionStatusAccrued, model.CommissionStatusApproved,
		})).
		Exec(ctx)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
