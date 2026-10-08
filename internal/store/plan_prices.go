// Multi-currency plan prices (plan §53).
//
// The catalogue's per-currency price list: one row per (plan,
// currency) with the amount in int64 minor units at that currency's
// ISO-4217 exponent (internal/money holds the exponent table; VND is
// exponent 0, whole dong). When no row exists for a currency the
// existing Stripe-price path stays the source of truth — these rows
// override it per currency, they do not replace it.
//
// Money discipline (plan §51): amount_minor is int64 minor units and
// this layer never computes with it — it only persists and resolves
// what the operator priced.
//
// The Bun alias for PlanPrice is "plan_price" (snake_case of the
// STRUCT name, not the table name) — pinned by plan_prices_test.go,
// same doctrine as bun_alias_test.go. Qualify with "plan_price".x in
// hand-written SQL, never plan_prices.x.
package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// PlanPrice is one (plan, currency) price row. ID is the BIGSERIAL
// row id (int64, same doctrine as GatewayPayment).
type PlanPrice struct {
	bun.BaseModel `bun:"table:plan_prices"`

	ID            int64     `bun:",pk" json:"id"`
	PlanID        string    `bun:",notnull" json:"plan_id"`
	Currency      string    `bun:",notnull" json:"currency"`
	AmountMinor   int64     `bun:",notnull" json:"amount_minor"`
	StripePriceID string    `bun:",notnull,default:''" json:"stripe_price_id,omitempty"`
	IsDefault     bool      `bun:",notnull,default:false" json:"is_default"`
	CreatedAt     time.Time `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt     time.Time `bun:",nullzero,default:now()" json:"updated_at"`
}

// Plan-price resolution refusals. The handler maps
// ErrPlanPriceCurrencyNotSupported to the CURRENCY_NOT_SUPPORTED 400
// the gateway flow already means (one status per code, see
// pkg/response/contract_test.go).
var (
	// ErrPlanPricePlanNotFound: SetPlanPrice named a plan that does
	// not exist (the FK would say the same, this says it before).
	ErrPlanPricePlanNotFound = errors.New("plan not found")
	// ErrPlanPriceCurrencyNotSupported: the plan has no row for the
	// requested currency and no default row to fall back to.
	ErrPlanPriceCurrencyNotSupported = errors.New("currency not supported for this plan")
)

// curFoldCurrency upper-cases and trims a currency code the way every
// writer of a price row must (prefix `cur` — multi-currency domain).
func curFoldCurrency(raw string) string {
	return strings.ToUpper(strings.TrimSpace(raw))
}

// SetPlanPrice upserts the price of one plan in one currency.
//
// The write is the union of the pinned rules:
//
//   - (plan_id, currency) is UNIQUE — setting a currency again
//     re-prices it (INSERT … ON CONFLICT DO UPDATE), never duplicates;
//   - is_default is exclusive per plan: making one row the default
//     demotes the previous default in the same statement's
//     transaction, and at most one can hold the flag (partial unique
//     index in the migration);
//   - the plan must exist (ErrPlanPricePlanNotFound) and the currency
//     must be a shaped ISO code (curValidCurrency).
//
// The model's ID is the BIGSERIAL row id; a caller leaves it zero on
// create and the upsert ignores it (the conflict target decides).
func (s *Store) SetPlanPrice(ctx context.Context, p *PlanPrice) error {
	if p == nil {
		return errors.New("plan price is required")
	}
	p.Currency = curFoldCurrency(p.Currency)
	if !model.ValidCurrencyCode(p.Currency) {
		return errors.New("currency must be a 3-letter ISO 4217 code")
	}
	if p.AmountMinor < 0 {
		return errors.New("amount_minor must not be negative")
	}
	exists, err := s.DB.NewSelect().Model((*model.Plan)(nil)).
		Where("id = ?", strings.TrimSpace(p.PlanID)).Exists(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return ErrPlanPricePlanNotFound
	}

	return s.DB.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if p.IsDefault {
			// Demote whatever holds the flag first — the partial
			// unique index would refuse a second default otherwise.
			if _, err := tx.NewUpdate().Model((*PlanPrice)(nil)).
				Set("is_default = false, updated_at = ?", time.Now()).
				Where("plan_id = ?", p.PlanID).
				Where("is_default = true").
				Where("currency <> ?", p.Currency).
				Exec(ctx); err != nil {
				return err
			}
		}
		_, err := tx.NewInsert().Model(p).
			On("CONFLICT (plan_id, currency) DO UPDATE").
			Set("amount_minor = EXCLUDED.amount_minor, stripe_price_id = EXCLUDED.stripe_price_id, is_default = EXCLUDED.is_default, updated_at = EXCLUDED.updated_at").
			Exec(ctx)
		return err
	})
}

// ListPlanPrices returns every price row of a plan: the default
// first, then the rest by currency code. A plan with no rows priced
// locally is an empty list, not an error.
func (s *Store) ListPlanPrices(ctx context.Context, planID string) ([]*PlanPrice, error) {
	var rows []*PlanPrice
	err := s.DB.NewSelect().Model(&rows).
		Where("plan_id = ?", strings.TrimSpace(planID)).
		OrderExpr("plan_price.is_default DESC, plan_price.currency ASC").
		Scan(ctx)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// ResolvePlanPrice picks the price of a plan in a currency: the exact
// currency row wins, then the plan's default row, then the refusal.
// An empty currency asks for the default directly.
//
// This is the unit-price seam every checkout quotes through
// (payment/cart_checkout.go): when a row exists it prices the sale,
// otherwise the caller falls back to the Stripe Price path.
func (s *Store) ResolvePlanPrice(ctx context.Context, planID, currency string) (*PlanPrice, error) {
	planID = strings.TrimSpace(planID)
	currency = curFoldCurrency(currency)
	row := new(PlanPrice)
	err := s.DB.NewSelect().Model(row).
		Where("plan_id = ?", planID).
		Where("currency = ?", currency).
		Limit(1).
		Scan(ctx)
	if err == nil {
		return row, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if currency != "" {
		// Fall back to the plan's default row.
		row = new(PlanPrice)
		err = s.DB.NewSelect().Model(row).
			Where("plan_id = ?", planID).
			Where("is_default = true").
			Limit(1).
			Scan(ctx)
		if err == nil {
			return row, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return nil, ErrPlanPriceCurrencyNotSupported
}
