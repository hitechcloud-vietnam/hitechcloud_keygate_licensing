package store

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Marketplace (public catalog reads) ───
//
// The read side of the anonymous storefront: discovery listings and
// the product page. Everything here is SELECT-only and answers with
// catalog data alone — no prices beyond what the plan rows carry, no
// credentials, no storage keys. The prices themselves live on Stripe
// (see handler.PublicPlansHandler), not in this database.

// MarketplaceProductFilter narrows ListMarketplaceProducts.
//
// Sort is a resolved ordering (see Sort): the expression is one the
// handler picked out of its allowlist, never a string the caller sent.
// An empty Sort falls back to newest-first inside the query builder,
// so the store is safe to call without going through the handler.
type MarketplaceProductFilter struct {
	Search       string
	CategorySlug string
	Sort         Sort
	Page         Page
}

// ListMarketplaceProducts is the public discovery listing: products
// matching a text search and/or carrying a category, one page at a
// time with the total the filter matched.
//
// The search covers name and slug. There is no description column on
// products to search — plan §223 lists one for the future catalog
// schema; when it lands the match grows into it here.
func (s *Store) ListMarketplaceProducts(ctx context.Context, f MarketplaceProductFilter) ([]*model.Product, int, error) {
	var out []*model.Product
	q := marketplaceProductsQuery(s.DB, f, &out)
	total, err := scanPage(ctx, q, f.Page)
	if err != nil {
		return nil, 0, err
	}
	if f.Page.Limit <= 0 {
		total = len(out)
	}
	return out, total, nil
}

// marketplaceProductsQuery builds the listing query on its own so the
// shape of it can be pinned without a database (see the tests): which
// alias the qualifiers use, and that the only caller-supplied values
// in the statement are bind parameters.
//
// The category filter is an EXISTS rather than a JOIN: the join would
// duplicate a product that carries the category twice (it cannot — the
// pair is the primary key) or require DISTINCT over every column, and
// EXISTS keeps the outer query single-table so unqualified columns can
// never become ambiguous with the joined ones.
//
// The correlation into the outer table uses the bun alias "product"
// (model.Product → FROM "products" AS "product"); a bare products.id
// would be a missing FROM-clause entry, the bug class pinned in
// bun_alias_test.go.
func marketplaceProductsQuery(db bun.IDB, f MarketplaceProductFilter, dest *[]*model.Product) *bun.SelectQuery {
	q := db.NewSelect().Model(dest)
	if f.Search != "" {
		like := "%" + f.Search + "%"
		q = q.Where("product.name ILIKE ? OR product.slug ILIKE ?", like, like)
	}
	if f.CategorySlug != "" {
		q = q.Where(`EXISTS (
			SELECT 1 FROM product_categories pc
			JOIN categories cat ON cat.id = pc.category_id
			WHERE pc.product_id = product.id AND cat.slug = ?)`, f.CategorySlug)
	}
	sort := f.Sort
	if sort.Expr == "" {
		// Newest first is what a discovery listing reads as when the
		// caller has no preference; the id tiebreak applySort appends
		// keeps paging stable either way.
		sort = Sort{Expr: "product.created_at", Desc: true}
	}
	return applySort(q, sort, "product.id")
}
