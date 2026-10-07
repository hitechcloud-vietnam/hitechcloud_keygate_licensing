package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Tax Rates ───

func (s *Store) CreateTaxRate(ctx context.Context, r *model.TaxRate) error {
	if r.ID == "" {
		r.ID = newID()
	}
	_, err := s.DB.NewInsert().Model(r).Exec(ctx)
	return err
}

func (s *Store) FindTaxRateByID(ctx context.Context, id string) (*model.TaxRate, error) {
	r := new(model.TaxRate)
	return r, s.DB.NewSelect().Model(r).Where("id = ?", id).Scan(ctx)
}

func (s *Store) FindTaxRateByJurisdiction(ctx context.Context, jurisdiction string) (*model.TaxRate, error) {
	r := new(model.TaxRate)
	return r, s.DB.NewSelect().Model(r).Where("jurisdiction = ?", jurisdiction).Scan(ctx)
}

func (s *Store) ListTaxRates(ctx context.Context, search string, p Page) ([]*model.TaxRate, int, error) {
	var out []*model.TaxRate
	// jurisdiction is unique, so ordering by it alone is total and a
	// row cannot move between two pages of one listing. Bun aliases
	// the model by the snake_case of the STRUCT name
	// (FROM "tax_rates" AS "tax_rate"), so qualifiers use tax_rate.
	q := s.DB.NewSelect().Model(&out).OrderExpr("tax_rate.jurisdiction ASC")
	if search != "" {
		q = q.Where("tax_rate.jurisdiction ILIKE ? OR tax_rate.country ILIKE ?",
			"%"+search+"%", "%"+search+"%")
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

func (s *Store) UpdateTaxRate(ctx context.Context, r *model.TaxRate) error {
	r.UpdatedAt = time.Now()
	_, err := s.DB.NewUpdate().Model(r).WherePK().Exec(ctx)
	return err
}

func (s *Store) DeleteTaxRate(ctx context.Context, id string) error {
	res, err := s.DB.NewDelete().Model((*model.TaxRate)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	// Nothing was deleted, so the id names no rate; say so rather than
	// let the caller answer 204 for a row that was never there.
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListActiveTaxRatesForCountry returns the active rates that apply to a
// sale in the given country and region — the input to the checkout tax
// computation (TaxRate.ToEngine → tax.CalculateMulti).
//
// A rate applies when its country matches and its region is either the
// buyer's region or empty: a country-wide rate covers every region in
// it, and a regional rate stacks on top of it additively in the engine.
// With both arguments empty every active rate comes back — no sale to
// match, just the whole set of live rates.
func (s *Store) ListActiveTaxRatesForCountry(ctx context.Context, country, region string) ([]*model.TaxRate, error) {
	var out []*model.TaxRate
	q := s.DB.NewSelect().Model(&out).Where("tax_rate.active = true")
	if country != "" {
		q = q.Where("tax_rate.country = ?", country)
	}
	if region != "" {
		q = q.Where("(tax_rate.region = ? OR tax_rate.region = '')", region)
	}
	err := q.OrderExpr("tax_rate.jurisdiction ASC").Scan(ctx)
	return out, err
}

// IsTaxRateJurisdictionConflict recognises the unique index on
// tax_rates.jurisdiction — a second rate for one jurisdiction. The two
// write paths answer 409 for it and 500 for everything else.
func IsTaxRateJurisdictionConflict(err error) bool {
	return isUniqueViolation(err)
}
