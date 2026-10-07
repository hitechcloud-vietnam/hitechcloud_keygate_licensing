package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// TestCategoryTableAliases pins the aliases Bun generates for the
// catalog models, the same bug class TestBunDefaultTableAliases pins
// for the commerce ones: Bun aliases a model by the snake_case of the
// STRUCT name, so hand-written qualifiers must use "category" and
// "product_category", never the table names "categories" /
// "product_categories".
func TestCategoryTableAliases(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	cases := []struct {
		model     interface{}
		wantAlias string
	}{
		{(*model.Category)(nil), `"category"`},
		{(*model.ProductCategory)(nil), `"product_category"`},
	}
	for _, tc := range cases {
		raw, err := db.NewSelect().Model(tc.model).AppendQuery(db.QueryGen(), nil)
		if err != nil {
			t.Fatalf("build query for %T: %v", tc.model, err)
		}
		sqlText := string(raw)
		if as := "AS " + tc.wantAlias; !strings.Contains(sqlText, as) {
			t.Errorf("generated SQL for %T does not contain %q; got:\n%s", tc.model, as, sqlText)
		}
	}
}

// The conflict detector must answer on exactly the two spellings of a
// duplicate slug — the sentinel the write paths fold the driver error
// into, and a raw unique violation that never passed through them —
// and on nothing else, or a genuine failure would be answered 409.
func TestIsCategorySlugConflict(t *testing.T) {
	if IsCategorySlugConflict(nil) {
		t.Error("nil reads as a slug conflict")
	}
	if IsCategorySlugConflict(errors.New("connection reset")) {
		t.Error("an arbitrary error reads as a slug conflict")
	}
	if !IsCategorySlugConflict(ErrCategorySlugTaken) {
		t.Error("ErrCategorySlugTaken is not recognised")
	}
	wrapped := fmt.Errorf("create category: %w", ErrCategorySlugTaken)
	if !IsCategorySlugConflict(wrapped) {
		t.Error("a wrapped ErrCategorySlugTaken is not recognised")
	}
}

// TestMarketplaceProductsQueryShape pins the listing statement itself,
// without a database: the bun alias the qualifiers correlate against,
// and that the category filter is a plain EXISTS whose tables are the
// real ones (its own little SQL, not subject to bun's model aliasing).
func TestMarketplaceProductsQueryShape(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	var dest []*model.Product
	q := marketplaceProductsQuery(db, MarketplaceProductFilter{
		Search:       "photo",
		CategorySlug: "dev-tools",
		Sort:         Sort{Expr: "product.name"},
	}, &dest)
	raw, err := q.AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)

	for _, want := range []string{
		`FROM "products" AS "product"`, // the alias qualifiers must match
		"product.name ILIKE",           // search stays on the outer table
		"product_categories pc",        // the join is raw SQL: real table names
		"JOIN categories cat",
		"pc.product_id = product.id", // correlated via the bun alias
		"cat.slug =",
		"product.name ASC NULLS LAST", // the resolved sort lands in ORDER BY
		"product.id DESC",             // applySort's stable tiebreak
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("listing SQL does not contain %q; got:\n%s", want, sqlText)
		}
	}
}

// An empty Sort is not an ORDER BY syntax error waiting to happen: the
// store falls back to newest-first on its own, so it is safe to call
// without the handler's resolution step.
func TestMarketplaceProductsQueryDefaultSort(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	var dest []*model.Product
	q := marketplaceProductsQuery(db, MarketplaceProductFilter{}, &dest)
	raw, err := q.AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)
	if !strings.Contains(sqlText, "product.created_at DESC NULLS LAST") {
		t.Errorf("default sort is not newest-first; got:\n%s", sqlText)
	}
	// A bare filter must not drag the category subquery along.
	if strings.Contains(sqlText, "product_categories") {
		t.Errorf("unfiltered listing still joins product_categories; got:\n%s", sqlText)
	}
}
