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

// RelatedProductsLimit is how many neighbours a product page's
// "related products" rail shows. It is a rail, not a listing: capped
// hard at 8 so one page cannot ask the store for a second catalog,
// and the cap is the store's — whatever a request sent, this is the
// most it can get.
const RelatedProductsLimit = 8

// clampRelatedLimit folds a requested size onto the rail's contract:
// 8 at most, and "the rail's size" when the caller did not ask (or
// asked for something nonsensical). The store clamps even though the
// handler clamps too — a limit is cheap to enforce here and expensive
// to forget somewhere else.
func clampRelatedLimit(limit int) int {
	if limit <= 0 {
		return RelatedProductsLimit
	}
	if limit > RelatedProductsLimit {
		return RelatedProductsLimit
	}
	return limit
}

// RelatedProducts answers "what else is like this one": products
// sharing at least one category with the given product, the product
// itself excluded, newest first, at most limit (≤ RelatedProductsLimit)
// of them.
//
// Same-category is the whole of the relation — there is no
// purchase-affinity data to draw on and inventing one would be worse
// than the honest category neighbour. A product with no categories has
// no relatives and reads as an empty list. A product sharing several
// categories with one neighbour still appears once: the relation is an
// EXISTS, not a join, so no duplicate rows can be produced to page
// around.
func (s *Store) RelatedProducts(ctx context.Context, productID string, limit int) ([]*model.Product, error) {
	var out []*model.Product
	q := relatedProductsQuery(s.DB, productID, clampRelatedLimit(limit), &out)
	return out, q.Scan(ctx)
}

// relatedProductsQuery builds the related-rail query on its own so
// its shape can be pinned without a database (see reviews_test.go):
// which alias the qualifiers correlate against, and that the
// neighbour relation is a plain EXISTS over the real join table.
//
// The correlation into the outer products table uses the bun alias
// "product" (model.Product → FROM "products" AS "product"); a bare
// products.id in the subquery would be a missing FROM-clause entry,
// the bug class pinned in bun_alias_test.go. The subquery's own tables
// (product_categories, twice: once for this product's facets, once for
// the candidate's) are real table names — its little SQL, not subject
// to bun's model aliasing.
func relatedProductsQuery(db bun.IDB, productID string, limit int, dest *[]*model.Product) *bun.SelectQuery {
	q := db.NewSelect().Model(dest).
		Where("product.id <> ?", productID).
		Where(`EXISTS (
			SELECT 1 FROM product_categories mine
			JOIN product_categories theirs ON theirs.category_id = mine.category_id
			WHERE mine.product_id = ? AND theirs.product_id = product.id)`, productID)
	// Newest first, with applySort's unique tiebreak so the rail
	// cannot reshuffle between two renders of the same page.
	return applySort(q, Sort{Expr: "product.created_at", Desc: true}, "product.id").Limit(limit)
}
