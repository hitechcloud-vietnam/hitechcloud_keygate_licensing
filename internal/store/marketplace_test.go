package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// uniqueSuffix keeps rows from this suite apart from every other test
// sharing the database, and from earlier runs of itself.
func uniqueSuffix() string { return time.Now().Format("150405.000000") }

// The slug is the category's public handle and the column is unique:
// both write paths must fold a duplicate into ErrCategorySlugTaken,
// and the detector must recognise the raw driver error too — a future
// writer that skips the fold must not answer 500 for this.
func TestCategorySlugConflict(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := uniqueSuffix()
	slug := "dev-tools-" + suffix

	first := &model.Category{Name: "Dev Tools", Slug: slug, Position: 1}
	if err := s.CreateCategory(ctx, first); err != nil {
		t.Fatalf("create first category: %v", err)
	}
	defer s.DeleteCategory(ctx, first.ID)

	dup := &model.Category{Name: "Dev Tools Again", Slug: slug}
	err := s.CreateCategory(ctx, dup)
	if err == nil {
		t.Fatal("second category with the same slug was accepted")
	}
	if !store.IsCategorySlugConflict(err) {
		t.Errorf("CreateCategory duplicate: IsCategorySlugConflict(%v) = false", err)
	}

	// The update path answers the same way: renaming a category onto
	// an existing handle is the same conflict.
	other := &model.Category{Name: "Other", Slug: "other-" + suffix}
	if err := s.CreateCategory(ctx, other); err != nil {
		t.Fatalf("create other category: %v", err)
	}
	defer s.DeleteCategory(ctx, other.ID)
	other.Slug = slug
	if err := s.UpdateCategory(ctx, other); err == nil {
		t.Error("update onto an existing slug was accepted")
	} else if !store.IsCategorySlugConflict(err) {
		t.Errorf("UpdateCategory duplicate: IsCategorySlugConflict(%v) = false", err)
	}

	// The raw spelling: an insert that never passed through
	// CreateCategory still reads as the same conflict.
	raw := &model.Category{ID: store.NewID(), Name: "Raw", Slug: slug}
	if _, err := s.DB.NewInsert().Model(raw).Exec(ctx); err == nil {
		t.Fatal("raw duplicate insert was accepted")
	} else if !store.IsCategorySlugConflict(err) {
		t.Errorf("raw duplicate: IsCategorySlugConflict(%v) = false", err)
	}

	// A delete of a row that was never there is ErrNoRows, not a
	// silent success the caller would answer 204 with.
	if err := s.DeleteCategory(ctx, "no-such-category"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("DeleteCategory(missing) = %v, want sql.ErrNoRows", err)
	}
}

// The product↔category assignment is replace-semantics and cascades
// with its parents: the round trip must survive re-assignment, and
// deleting a category must detach exactly its own links and leave the
// product with its other facets — the documented cascade choice.
func TestProductCategoriesJoinRoundTrip(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := uniqueSuffix()

	product := &model.Product{Name: "Catalog Product " + suffix, Slug: "catalog-product-" + suffix, Type: "desktop"}
	if err := s.CreateProduct(ctx, product); err != nil {
		t.Fatalf("create product: %v", err)
	}
	defer s.DeleteProduct(ctx, product.ID)

	// Positions run against catalog order: c2 sorts first.
	c1 := &model.Category{Name: "Second " + suffix, Slug: "second-" + suffix, Position: 2}
	c2 := &model.Category{Name: "First " + suffix, Slug: "first-" + suffix, Position: 1}
	for _, cat := range []*model.Category{c1, c2} {
		if err := s.CreateCategory(ctx, cat); err != nil {
			t.Fatalf("create category %q: %v", cat.Slug, err)
		}
		defer s.DeleteCategory(ctx, cat.ID)
	}

	if err := s.ReplaceProductCategories(ctx, product.ID, []string{c1.ID, c2.ID, c1.ID, "  "}); err != nil {
		t.Fatalf("assign categories: %v", err)
	}
	got := mustCategories(t, s, ctx, product.ID)
	if len(got) != 2 || got[0].ID != c2.ID || got[1].ID != c1.ID {
		t.Fatalf("assigned categories = %+v, want [%s %s] in catalog order", ids(got), c2.ID, c1.ID)
	}

	// Replace is whole-set: the second write must not append.
	if err := s.ReplaceProductCategories(ctx, product.ID, []string{c2.ID}); err != nil {
		t.Fatalf("re-assign categories: %v", err)
	}
	got = mustCategories(t, s, ctx, product.ID)
	if len(got) != 1 || got[0].ID != c2.ID {
		t.Fatalf("after replace = %+v, want just %s", ids(got), c2.ID)
	}

	// An unknown category refuses the whole replacement — and the
	// previous assignment survives, because the swap is one
	// transaction.
	if err := s.ReplaceProductCategories(ctx, product.ID, []string{"no-such-category"}); err == nil {
		t.Error("assignment of an unknown category was accepted")
	}
	got = mustCategories(t, s, ctx, product.ID)
	if len(got) != 1 || got[0].ID != c2.ID {
		t.Fatalf("after refused replace = %+v, want the old assignment [%s]", ids(got), c2.ID)
	}

	// Cascade: deleting a category detaches its links and nothing
	// else. Re-attach both, drop c1, and only c1's link may go.
	if err := s.ReplaceProductCategories(ctx, product.ID, []string{c1.ID, c2.ID}); err != nil {
		t.Fatalf("re-assign both: %v", err)
	}
	if err := s.DeleteCategory(ctx, c1.ID); err != nil {
		t.Fatalf("delete category: %v", err)
	}
	got = mustCategories(t, s, ctx, product.ID)
	if len(got) != 1 || got[0].ID != c2.ID {
		t.Fatalf("after category delete = %+v, want [%s]", ids(got), c2.ID)
	}
}

// The discovery listing end to end: the search matches name and slug
// (there is no description column on products to match), the category
// filter goes through the join, and the plans of the cards carry only
// the active ones in admin order.
func TestMarketplaceListingAndPlans(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := uniqueSuffix()

	cat := &model.Category{Name: "Searchable " + suffix, Slug: "searchable-" + suffix}
	if err := s.CreateCategory(ctx, cat); err != nil {
		t.Fatalf("create category: %v", err)
	}
	defer s.DeleteCategory(ctx, cat.ID)

	alpha := &model.Product{Name: "MktSearch Alpha " + suffix, Slug: "mktsearch-alpha-" + suffix, Type: "desktop"}
	beta := &model.Product{Name: "MktSearch Beta " + suffix, Slug: "mktsearch-beta-" + suffix, Type: "desktop"}
	for _, p := range []*model.Product{alpha, beta} {
		if err := s.CreateProduct(ctx, p); err != nil {
			t.Fatalf("create product: %v", err)
		}
		defer s.DeleteProduct(ctx, p.ID)
	}
	if err := s.ReplaceProductCategories(ctx, alpha.ID, []string{cat.ID}); err != nil {
		t.Fatalf("categorize alpha: %v", err)
	}

	// name ASC: alpha before beta whatever the insert order was.
	byName := store.MarketplaceProductFilter{
		Search: "MktSearch",
		Sort:   store.Sort{Expr: "product.name"},
		Page:   store.Page{Limit: 50},
	}
	prods, total, err := s.ListMarketplaceProducts(ctx, byName)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(prods) != 2 || prods[0].ID != alpha.ID || prods[1].ID != beta.ID {
		t.Fatalf("by-name listing = %d rows, total %d, want alpha then beta", len(prods), total)
	}

	// The category filter answers through the join, and nothing else
	// matches it.
	filtered, total, err := s.ListMarketplaceProducts(ctx, store.MarketplaceProductFilter{
		CategorySlug: cat.Slug,
		Sort:         store.Sort{Expr: "product.created_at", Desc: true},
		Page:         store.Page{Limit: 50},
	})
	if err != nil {
		t.Fatalf("list by category: %v", err)
	}
	if total != 1 || len(filtered) != 1 || filtered[0].ID != alpha.ID {
		t.Fatalf("category listing = %d rows, total %d, want just alpha", len(filtered), total)
	}
	if _, total, err := s.ListMarketplaceProducts(ctx, store.MarketplaceProductFilter{
		CategorySlug: "no-such-category",
		Page:         store.Page{Limit: 50},
	}); err != nil || total != 0 {
		t.Fatalf("unknown category filter = total %d, err %v, want 0 rows", total, err)
	}

	// Plans: inactive ones stay out of the catalog, active ones come
	// back in admin order (sort_order).
	second := &model.Plan{ProductID: alpha.ID, Name: "Pro", Slug: "pro-" + suffix,
		CheckoutID: "chk-pro-" + suffix, LicenseType: "subscription", LicenseModel: "standard",
		Active: true, SortOrder: 2}
	first := &model.Plan{ProductID: alpha.ID, Name: "Basic", Slug: "basic-" + suffix,
		CheckoutID: "chk-basic-" + suffix, LicenseType: "subscription", LicenseModel: "standard",
		Active: true, SortOrder: 1}
	off := &model.Plan{ProductID: alpha.ID, Name: "Retired", Slug: "retired-" + suffix,
		CheckoutID: "chk-off-" + suffix, LicenseType: "subscription", LicenseModel: "standard",
		Active: false, SortOrder: 0}
	for _, p := range []*model.Plan{second, first, off} {
		if err := s.CreatePlan(ctx, p); err != nil {
			t.Fatalf("create plan %q: %v", p.Slug, err)
		}
		defer s.DeletePlan(ctx, p.ID)
	}
	plansBy, err := s.ActivePlansForProducts(ctx, []string{alpha.ID, beta.ID})
	if err != nil {
		t.Fatalf("active plans: %v", err)
	}
	alphaPlans := plansBy[alpha.ID]
	if len(alphaPlans) != 2 || alphaPlans[0].ID != first.ID || alphaPlans[1].ID != second.ID {
		t.Fatalf("active plans = %d rows, want [basic pro] by sort_order", len(alphaPlans))
	}
	if got := plansBy[beta.ID]; len(got) != 0 {
		t.Fatalf("beta gained %d plans out of nowhere", len(got))
	}

	// The facet list arrives in catalog order for every card at once.
	catsBy, err := s.CategoriesForProducts(ctx, []string{alpha.ID, beta.ID})
	if err != nil {
		t.Fatalf("categories for products: %v", err)
	}
	if got := catsBy[alpha.ID]; len(got) != 1 || got[0].ID != cat.ID {
		t.Fatalf("alpha categories = %+v, want [%s]", ids(got), cat.ID)
	}
	if got := catsBy[beta.ID]; len(got) != 0 {
		t.Fatalf("beta categories = %+v, want none", ids(got))
	}

	// The public facet list is the whole set in position order.
	all, err := s.ListCategoriesByPosition(ctx)
	if err != nil {
		t.Fatalf("list categories: %v", err)
	}
	found := false
	for _, c := range all {
		if c.ID == cat.ID {
			found = true
		}
	}
	if !found {
		t.Error("ListCategoriesByPosition did not return the created category")
	}
}

func mustCategories(t *testing.T, s *store.Store, ctx context.Context, productID string) []*model.Category {
	t.Helper()
	byProduct, err := s.CategoriesForProducts(ctx, []string{productID})
	if err != nil {
		t.Fatalf("CategoriesForProducts: %v", err)
	}
	return byProduct[productID]
}

func ids(cats []*model.Category) []string {
	out := make([]string, 0, len(cats))
	for _, c := range cats {
		out = append(out, c.ID)
	}
	return out
}
