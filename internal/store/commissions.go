package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Commission ledger (plan §31 / Phase 7, slice 2) ───
//
// One row per (reseller, order): what this partner earned on this
// sale. The store owns the money — the amount is computed here from
// the basis and rate (model.CommissionAmount, rounding down), never
// taken from a caller — and the idempotency — a retried accrual
// answers with the original row instead of writing a second payout.
// See model.Commission and the migration for why order_id has no
// foreign key and why the basis/rate/amount are snapshotted.

// ErrCommissionCancelled is MarkCommissionPaid's refusal: the row was
// voided (refunded order, broken deal) and a cancelled commission is
// never payable. Stamping one "paid" would resurrect a row the ledger
// deliberately closed, so the state change is refused and the handler
// answers 409.
var ErrCommissionCancelled = errors.New("commission is cancelled")

// AccrueCommission records what a reseller earned on one order.
//
// Idempotent per (reseller_id, order_id), enforced by the unique index
// those two columns carry: a second accrual for the same pair writes
// nothing and answers the ORIGINAL row with created=false, so a
// retried request can never double-pay. The insert is
// INSERT ... ON CONFLICT DO NOTHING plus a read-back — atomic at the
// SQL layer either way, and the read-back is what makes the answer
// carry the stored timestamps rather than the caller's struct.
//
// The store computes amount_minor = model.CommissionAmount(basis, bps)
// — rounding down, as documented there — and forces the row's shape:
// status accrued (later transitions are MarkCommissionPaid's business)
// and paid_at NULL. The caller's AmountMinor is ignored on purpose:
// a ledger row must not trust its writer on the money. The basis and
// the rate are the caller's (the admin asserts the sale amount and
// the contractual rate at accrual time), and both are snapshotted so
// later contract changes cannot rewrite history.
//
// A reseller_id that names no reseller is sql.ErrNoRows (the FK would
// say the same less politely).
func (s *Store) AccrueCommission(ctx context.Context, cm *model.Commission) (*model.Commission, bool, error) {
	if cm.ID == "" {
		cm.ID = newID()
	}
	n, err := s.DB.NewSelect().Model((*model.Reseller)(nil)).
		Where("id = ?", cm.ResellerID).Count(ctx)
	if err != nil {
		return nil, false, err
	}
	if n == 0 {
		return nil, false, sql.ErrNoRows
	}

	cm.AmountMinor = model.CommissionAmount(cm.BasisMinor, cm.BPS)
	cm.Status = model.CommissionStatusAccrued
	cm.PaidAt = nil

	res, err := s.DB.NewInsert().Model(cm).
		On("CONFLICT (reseller_id, order_id) DO NOTHING").
		Exec(ctx)
	if err != nil {
		return nil, false, err
	}
	created := false
	if n, err := res.RowsAffected(); err == nil && n > 0 {
		created = true
	}
	// Read back either way: the winner of a race, or the pre-existing
	// row on a replay. Both answer the ledger's copy, not the input.
	row, err := s.FindCommissionByOrder(ctx, cm.ResellerID, cm.OrderID)
	if err != nil {
		return nil, false, err
	}
	return row, created, nil
}

// FindCommissionByID returns one ledger row by primary key. A miss is
// sql.ErrNoRows so the caller can say 404 rather than 500.
func (s *Store) FindCommissionByID(ctx context.Context, id string) (*model.Commission, error) {
	cm := new(model.Commission)
	return cm, s.DB.NewSelect().Model(cm).Where("id = ?", id).Scan(ctx)
}

// FindCommissionByOrder returns the commission accrued on one order
// for one reseller — the lookup the idempotent accrual answers a
// replay from. Scoped by the pair because the uniqueness is the pair:
// the same order could in principle be accrued for a different
// reseller (a transferred sale), and those are different rows. A miss
// is sql.ErrNoRows.
func (s *Store) FindCommissionByOrder(ctx context.Context, resellerID, orderID string) (*model.Commission, error) {
	cm := new(model.Commission)
	return cm, s.DB.NewSelect().Model(cm).
		Where("reseller_id = ? AND order_id = ?", resellerID, orderID).
		Scan(ctx)
}

// ListCommissions is the ledger listing: one page of a reseller's
// commissions plus how many the filter matched. status narrows to one
// lifecycle value (already validated against the closed vocabulary by
// the handler); empty means all of them. Newest accrual first with id
// as the tiebreaker so equal timestamps cannot swap between two pages
// of one listing.
func (s *Store) ListCommissions(ctx context.Context, resellerID, status string, p Page) ([]*model.Commission, int, error) {
	var out []*model.Commission
	q := s.DB.NewSelect().Model(&out).
		Where("reseller_id = ?", resellerID).
		OrderExpr("created_at DESC, id DESC")
	if status != "" {
		q = q.Where("status = ?", status)
	}
	total, err := scanPage(ctx, q, p)
	if err != nil {
		return nil, 0, err
	}
	if p.Limit <= 0 {
		total = len(out)
	}
	return out, total, nil
}

// MarkCommissionPaid stamps a commission disbursed: status becomes
// paid and paid_at records when. The paid_at is the caller's (a payout
// run backdates to the transfer date); a zero paid_at means "now".
//
// Re-marking a paid commission re-stamps it rather than refusing —
// the payout happened, only the record of when is being corrected.
// A CANCELLED commission is the one refusal (ErrCommissionCancelled):
// cancelled is a closed state, and paying one would silently undo a
// deliberate void.
//
// A missing id is sql.ErrNoRows. The read-modify-write is not
// serializable across concurrent markers, but every path converges on
// the same terminal values (status paid), so the worst race is a
// re-stamped paid_at.
func (s *Store) MarkCommissionPaid(ctx context.Context, id string, paidAt time.Time) (*model.Commission, error) {
	existing, err := s.FindCommissionByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if existing.Status == model.CommissionStatusCancelled {
		return nil, ErrCommissionCancelled
	}
	if paidAt.IsZero() {
		paidAt = time.Now()
	}
	if _, err := s.DB.NewUpdate().Model((*model.Commission)(nil)).
		Set("status = ?, paid_at = ?, updated_at = ?", model.CommissionStatusPaid, paidAt, time.Now()).
		Where("id = ?", id).
		Exec(ctx); err != nil {
		return nil, err
	}
	return s.FindCommissionByID(ctx, id)
}

// SumCommissionsByStatus is the dashboard number: for one reseller,
// the sum of amount_minor per status. Statuses with no rows are simply
// absent from the map (the handler zero-fills the closed vocabulary
// for display). Money is int64 minor units — the sums are exact.
func (s *Store) SumCommissionsByStatus(ctx context.Context, resellerID string) (map[string]int64, error) {
	var rows []struct {
		Status string
		Total  int64
	}
	if err := s.DB.NewSelect().Model((*model.Commission)(nil)).
		ColumnExpr("status, COALESCE(SUM(amount_minor), 0) AS total").
		Where("reseller_id = ?", resellerID).
		GroupExpr("status").
		Scan(ctx, &rows); err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.Status] = r.Total
	}
	return out, nil
}
