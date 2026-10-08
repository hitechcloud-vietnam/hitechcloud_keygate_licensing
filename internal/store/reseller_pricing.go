package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Wholesale price overrides (plan §31 / Phase 7, slice 2) ───
//
// What one reseller pays for one plan instead of the public (Stripe)
// price. Configuration keyed by (reseller_id, plan_id) — a changed
// deal is written over in place, there is no price history here (past
// money lives in the commission ledger). The currency is the client's
// ISO 4217 code, shape-validated by the handler
// (model.ValidCurrencyCode) and CHECKed by the migration; whether it
// matches the plan's Stripe price is deliberately not decided here —
// that fact lives in Stripe and cannot be checked offline.

// ErrPriceOverridePlanNotFound is SetResellerPriceOverride refusing a
// plan_id that names no plan. Both sides of the pair are checked
// before the write so the refusals are the typed ones a handler can
// map, not a raw foreign-key violation (see AllocateLicense). The
// caller sent an id we cannot price; that is a bad request, not a
// missing reseller.
var ErrPriceOverridePlanNotFound = errors.New("plan not found")

// SetResellerPriceOverride writes the wholesale price for one
// (reseller, plan) pair — create on first write, replace on a repeat
// (PUT semantics: the pair is the whole identity and a new deal
// overwrites the old in place; updated_at is bumped, created_at
// stands). The reseller must exist (sql.ErrNoRows) and the plan must
// exist (ErrPriceOverridePlanNotFound).
//
// The currency is stored as given — the handler already validated its
// shape and the migration's CHECK backs that up — and the amount is
// stored verbatim too: it is the contract the admin is asserting, not
// a computation. (Contrast the commission accrual, where the store
// owns the math: here there IS no math, only the stated price.)
func (s *Store) SetResellerPriceOverride(ctx context.Context, o *model.ResellerPriceOverride) error {
	n, err := s.DB.NewSelect().Model((*model.Reseller)(nil)).
		Where("id = ?", o.ResellerID).Count(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	n, err = s.DB.NewSelect().Model((*model.Plan)(nil)).
		Where("id = ?", o.PlanID).Count(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrPriceOverridePlanNotFound
	}
	o.UpdatedAt = time.Now()
	_, err = s.DB.NewInsert().Model(o).
		On("CONFLICT (reseller_id, plan_id) DO UPDATE").
		Set("unit_amount_minor = EXCLUDED.unit_amount_minor, currency = EXCLUDED.currency, updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	return err
}

// FindResellerPriceOverride returns the wholesale price one reseller
// pays for one plan. The lookup is scoped by the pair (never by plan
// alone) because the pair is the row's identity. A miss is
// sql.ErrNoRows so the caller can say 404 rather than 500.
func (s *Store) FindResellerPriceOverride(ctx context.Context, resellerID, planID string) (*model.ResellerPriceOverride, error) {
	o := new(model.ResellerPriceOverride)
	return o, s.DB.NewSelect().Model(o).
		Where("reseller_id = ? AND plan_id = ?", resellerID, planID).
		Scan(ctx)
}

// ListResellerPriceOverrides is one reseller's whole wholesale price
// list, plan_id order (the pair is unique, so the order is total and
// pages — or a full listing — line up). Deliberately unpaged: the
// collection is bounded by the plans a partner sells, and the reads
// this slice serves ("show me my prices") want all of them.
func (s *Store) ListResellerPriceOverrides(ctx context.Context, resellerID string) ([]*model.ResellerPriceOverride, error) {
	var out []*model.ResellerPriceOverride
	err := s.DB.NewSelect().Model(&out).
		Where("reseller_id = ?", resellerID).
		OrderExpr("plan_id ASC").
		Scan(ctx)
	return out, err
}

// CountResellerPriceOverrides is how many plans a reseller holds a
// wholesale price for — the summary number the portal dashboard shows
// beside the account.
func (s *Store) CountResellerPriceOverrides(ctx context.Context, resellerID string) (int, error) {
	return s.DB.NewSelect().Model((*model.ResellerPriceOverride)(nil)).
		Where("reseller_id = ?", resellerID).Count(ctx)
}

// DeleteResellerPriceOverride drops the override on one (reseller,
// plan) pair — scoped by the pair so a delete can never reach another
// partner's price. A pair with no override is a no-op that answers
// sql.ErrNoRows, so the caller says 404 for a deletion that was not
// there rather than 204 for nothing.
func (s *Store) DeleteResellerPriceOverride(ctx context.Context, resellerID, planID string) error {
	res, err := s.DB.NewDelete().Model((*model.ResellerPriceOverride)(nil)).
		Where("reseller_id = ? AND plan_id = ?", resellerID, planID).Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
