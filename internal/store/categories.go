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

// ─── Categories (marketplace catalog) ───
//
// Categories are the admin-managed facets the public marketplace
// filters by. Everything here is catalog bookkeeping: no price, no
// licence meaning, so a category change can never alter what a
// customer owns.

// ErrCategorySlugTaken is the refusal the unique index on
// categories.slug gives: a second category with the same handle. The
// two write paths fold it out of the raw driver error so callers can
// answer 409 for it and 500 for everything else without knowing what
// a SQLSTATE is; IsCategorySlugConflict is the matching question.
var ErrCategorySlugTaken = errors.New("category slug already exists")

// IsCategorySlugConflict reports whether err is the slug-taken
// refusal, in either spelling: the sentinel CreateCategory and
// UpdateCategory return, or a raw unique-violation that reached the
// caller without passing through them. Mirrors
// IsTaxRateJurisdictionConflict.
func IsCategorySlugConflict(err error) bool {
	return errors.Is(err, ErrCategorySlugTaken) || isUniqueViolation(err)
}

func (s *Store) CreateCategory(ctx context.Context, cat *model.Category) error {
	if cat.ID == "" {
		cat.ID = newID()
	}
	_, err := s.DB.NewInsert().Model(cat).Exec(ctx)
	if isUniqueViolation(err) {
		return ErrCategorySlugTaken
	}
	return err
}

func (s *Store) FindCategoryByID(ctx context.Context, id string) (*model.Category, error) {
	c := new(model.Category)
	return c, s.DB.NewSelect().Model(c).Where("id = ?", id).Scan(ctx)
}

// FindCategoryBySlug looks a category up by its canonical handle (see
// model.NormalizeCategorySlug). Slugs are folded before they are
// stored, so an exact match is the whole of the lookup.
func (s *Store) FindCategoryBySlug(ctx context.Context, slug string) (*model.Category, error) {
	c := new(model.Category)
	return c, s.DB.NewSelect().Model(c).Where("slug = ?", slug).Scan(ctx)
}

// ListCategories is the admin listing: one page of categories plus how
// many the filter matched. Ordered by position with name and id as
// tiebreakers, so equal positions cannot swap between two pages of one
// listing.
func (s *Store) ListCategories(ctx context.Context, search string, p Page) ([]*model.Category, int, error) {
	var out []*model.Category
	q := s.DB.NewSelect().Model(&out).
		OrderExpr("position ASC, name ASC, id ASC")
	if search != "" {
		q = q.Where("name ILIKE ? OR slug ILIKE ?", "%"+search+"%", "%"+search+"%")
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

// ListCategoriesByPosition is the public catalog's facet list: every
// category, in the order the admin arranged them. Unbounded on
// purpose — this is a small set of rows and the marketplace filter
// menu needs all of it to be worth showing.
func (s *Store) ListCategoriesByPosition(ctx context.Context) ([]*model.Category, error) {
	var out []*model.Category
	err := s.DB.NewSelect().Model(&out).
		OrderExpr("position ASC, name ASC, id ASC").
		Scan(ctx)
	return out, err
}

func (s *Store) UpdateCategory(ctx context.Context, cat *model.Category) error {
	cat.UpdatedAt = time.Now()
	_, err := s.DB.NewUpdate().Model(cat).WherePK().Exec(ctx)
	if isUniqueViolation(err) {
		return ErrCategorySlugTaken
	}
	return err
}

// DeleteCategory removes the category. Its product links go with it —
// the migration declares ON DELETE CASCADE on both join columns, so
// the detach is the database's job and cannot be forgotten here (see
// model.ProductCategory for why cascade and not refuse). The products
// themselves are untouched.
//
// A no-op delete answers sql.ErrNoRows so the caller can say 404 for a
// row that was never there rather than claim a deletion that did not
// happen.
func (s *Store) DeleteCategory(ctx context.Context, id string) error {
	res, err := s.DB.NewDelete().Model((*model.Category)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ReplaceProductCategories sets a product's category links to exactly
// the given set — the whole assignment written in one transaction, so
// a reader never sees a half-moved product and a failed insert cannot
// leave the old links already gone. Unknown category ids are refused
// by the foreign key and roll the whole replacement back.
//
// Duplicate and empty ids are dropped before the write: the pair is
// the primary key, so a repeated id would abort an otherwise valid
// request.
func (s *Store) ReplaceProductCategories(ctx context.Context, productID string, categoryIDs []string) error {
	return RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewDelete().
			Model((*model.ProductCategory)(nil)).
			Where("product_id = ?", productID).
			Exec(ctx); err != nil {
			return err
		}
		seen := make(map[string]bool, len(categoryIDs))
		rows := make([]*model.ProductCategory, 0, len(categoryIDs))
		for _, cid := range categoryIDs {
			cid = strings.TrimSpace(cid)
			if cid == "" || seen[cid] {
				continue
			}
			seen[cid] = true
			rows = append(rows, &model.ProductCategory{ProductID: productID, CategoryID: cid})
		}
		if len(rows) == 0 {
			return nil
		}
		_, err := tx.NewInsert().Model(&rows).Exec(ctx)
		return err
	})
}

// CategoriesForProducts loads the categories of many products in one
// query, keyed by product id and already in catalog order. The
// listing needs every card's facets at once, and one query per product
// would turn a page of twenty into twenty round trips.
//
// The join is written with its own aliases (pc, cat) rather than
// through a bun model: the query spans two tables, and bun's implicit
// model aliases only cover one of them.
func (s *Store) CategoriesForProducts(ctx context.Context, productIDs []string) (map[string][]*model.Category, error) {
	out := make(map[string][]*model.Category, len(productIDs))
	if len(productIDs) == 0 {
		return out, nil
	}
	var rows []struct {
		ProductID   string
		ID          string
		Name        string
		Slug        string
		Description string
		Position    int
	}
	err := s.DB.NewSelect().
		TableExpr("product_categories AS pc").
		ColumnExpr("pc.product_id, cat.id, cat.name, cat.slug, cat.description, cat.position").
		Join("JOIN categories AS cat ON cat.id = pc.category_id").
		Where("pc.product_id IN (?)", bun.List(productIDs)).
		OrderExpr("cat.position ASC, cat.name ASC, cat.id ASC").
		Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ProductID] = append(out[r.ProductID], &model.Category{
			ID:          r.ID,
			Name:        r.Name,
			Slug:        r.Slug,
			Description: r.Description,
			Position:    r.Position,
		})
	}
	return out, nil
}

// ActivePlansForProducts loads the live plans of many products in one
// query, keyed by product id and in the order the admin arranged them
// (sort_order, then newest first — the same order the plan list and
// the pricing page use).
//
// Inactive plans stay out: a plan switched off is not for sale, and
// the catalog must not offer what checkout would refuse. The rows are
// loaded bare — no relations — because the public payload takes only
// the fields it names (see handler.marketplacePlanJSON), never
// entitlements or Stripe identifiers.
func (s *Store) ActivePlansForProducts(ctx context.Context, productIDs []string) (map[string][]*model.Plan, error) {
	out := make(map[string][]*model.Plan, len(productIDs))
	if len(productIDs) == 0 {
		return out, nil
	}
	var plans []*model.Plan
	err := s.DB.NewSelect().Model(&plans).
		Where("product_id IN (?)", bun.List(productIDs)).
		Where("active = true").
		OrderExpr("sort_order ASC, created_at DESC, id DESC").
		Scan(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range plans {
		out[p.ProductID] = append(out[p.ProductID], p)
	}
	return out, nil
}
