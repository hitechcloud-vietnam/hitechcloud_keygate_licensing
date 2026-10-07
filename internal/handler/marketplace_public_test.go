package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// fakeCatalog stands in for store.Store so the marketplace endpoints
// can be covered without a database. It records the filter the handler
// resolved — which is how the sort whitelist is pinned: whatever the
// caller sent, the store must only ever see an expression from our
// map.
type fakeCatalog struct {
	product    *model.Product
	productErr error

	categories []*model.Category
	products   []*model.Product
	total      int
	catsBy     map[string][]*model.Category
	plansBy    map[string][]*model.Plan
	releases   []*model.Release

	listFilter store.MarketplaceProductFilter
	listCalls  int
	gotIDs     []string
	gotRelease store.ReleaseFilter
}

func (f *fakeCatalog) FindProductBySlug(_ context.Context, _ string) (*model.Product, error) {
	return f.product, f.productErr
}

func (f *fakeCatalog) ListCategoriesByPosition(_ context.Context) ([]*model.Category, error) {
	return f.categories, nil
}

func (f *fakeCatalog) ListMarketplaceProducts(_ context.Context, filter store.MarketplaceProductFilter) ([]*model.Product, int, error) {
	f.listCalls++
	f.listFilter = filter
	return f.products, f.total, nil
}

func (f *fakeCatalog) CategoriesForProducts(_ context.Context, productIDs []string) (map[string][]*model.Category, error) {
	f.gotIDs = productIDs
	return f.catsBy, nil
}

func (f *fakeCatalog) ActivePlansForProducts(_ context.Context, _ []string) (map[string][]*model.Plan, error) {
	return f.plansBy, nil
}

func (f *fakeCatalog) ListReleases(_ context.Context, filter store.ReleaseFilter) ([]*model.Release, error) {
	f.gotRelease = filter
	return f.releases, nil
}

func (f *fakeCatalog) handler() *MarketplaceHandler {
	return &MarketplaceHandler{mk: f}
}

func mktGet(h *MarketplaceHandler, target string, route string, params gin.Params) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", target, nil)
	c.Params = params
	switch route {
	case "list":
		h.ListProducts(c)
	case "detail":
		h.GetProduct(c)
	case "categories":
		h.ListCategories(c)
	}
	return w
}

// The allowlist is the security boundary: anything not named in
// marketplaceSortColumns is refused with 400 before the store is
// touched at all. A malicious sort literally cannot reach SQL because
// the query is never built.
func TestMarketplaceSortWhitelist(t *testing.T) {
	for _, tc := range []struct {
		query string
		code  int
	}{
		{"", 200},
		{"?sort=name", 200},
		{"?sort=newest", 200},
		{"?sort=name&order=desc", 200},
		{"?sort=nope", 400},
		{"?sort=product.created_at", 400},
		// The shapes an injection attempt takes. Escaped the way a
		// client would have to send them (a raw space would not even
		// survive the request line).
		{"?sort=" + url.QueryEscape("name; DROP TABLE products"), 400},
		{"?sort=" + url.QueryEscape("(SELECT 1)"), 400},
		{"?sort=name&order=" + url.QueryEscape("asc; DELETE FROM products"), 400},
	} {
		fake := &fakeCatalog{products: []*model.Product{}, total: 0}
		w := mktGet(fake.handler(), "/marketplace/products"+tc.query, "list", nil)
		if w.Code != tc.code {
			t.Errorf("ListProducts(%q) status = %d, want %d; body %s",
				tc.query, w.Code, tc.code, w.Body.String())
		}
		if tc.code == 400 {
			if fake.listCalls != 0 {
				t.Errorf("ListProducts(%q) reached the store %d times, want 0", tc.query, fake.listCalls)
			}
			if !strings.Contains(w.Body.String(), "INVALID_SORT") && !strings.Contains(w.Body.String(), "INVALID_ORDER") {
				t.Errorf("ListProducts(%q) refusal does not name the contract code: %s", tc.query, w.Body.String())
			}
		}
	}
}

// Whatever survives the whitelist lands in the filter as the
// expression our map resolved it to — never as the caller's word for
// it — and the text/category filters arrive trimmed and folded.
func TestMarketplaceListFilterPassthrough(t *testing.T) {
	fake := &fakeCatalog{products: []*model.Product{}, total: 0}
	w := mktGet(fake.handler(),
		"/marketplace/products?search=%20photo%20&category=Dev-Tools&sort=name&limit=5",
		"list", nil)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	f := fake.listFilter
	if f.Search != "photo" {
		t.Errorf("Search = %q, want %q", f.Search, "photo")
	}
	if f.CategorySlug != "dev-tools" {
		t.Errorf("CategorySlug = %q, want %q", f.CategorySlug, "dev-tools")
	}
	if f.Sort.Expr != "product.name" || f.Sort.Desc {
		t.Errorf("Sort = %+v, want product.name ASC", f.Sort)
	}
	if f.Page.Limit != 5 {
		t.Errorf("Limit = %d, want 5", f.Page.Limit)
	}
}

// The sortable expressions are ours, not the caller's: each one is a
// table-qualified column of the products alias and nothing else.
func TestMarketplaceSortColumnsAreQualified(t *testing.T) {
	if len(marketplaceSortColumns) == 0 {
		t.Fatal("no sortable columns declared for the marketplace listing")
	}
	for name, col := range marketplaceSortColumns {
		if !strings.Contains(col.Expr, ".") {
			t.Errorf("sort column %q resolves to %q, which is not table-qualified", name, col.Expr)
		}
		for _, r := range col.Expr {
			if r != '.' && r != '_' && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') {
				t.Errorf("sort column %q expression %q contains unexpected character %q", name, col.Expr, r)
			}
		}
	}
	if _, ok := marketplaceSortColumns["newest"]; !ok {
		t.Error("the listing's default sort column is not in its own allowlist")
	}
}

// An unknown slug answers 404 — and so does a lookup that failed, so
// slug guesses learn nothing from the difference.
func TestMarketplaceDetail404(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"unknown slug", sql.ErrNoRows},
		{"lookup failure", errors.New("connection reset")},
	} {
		fake := &fakeCatalog{productErr: tc.err}
		w := mktGet(fake.handler(), "/marketplace/products/nope", "detail",
			gin.Params{{Key: "slug", Value: "nope"}})
		if w.Code != 404 {
			t.Errorf("%s: status = %d, want 404; body %s", tc.name, w.Code, w.Body.String())
		}
	}
}

// The product page is catalog data and nothing else: the §223 fields
// that exist on the row, the categories, the active plans in the
// pricing shape /products/:product_slug/plans uses, and the published
// changelog. The artifact's storage key, signature and signing key id
// are on the model but must not reach the payload.
func TestMarketplaceDetailShape(t *testing.T) {
	fake := &fakeCatalog{
		product: &model.Product{
			ID: "p1", Name: "Photo Suite", Slug: "photo-suite", Type: "desktop",
			DownloadURL:             "https://example.com/download",
			MinimumSupportedVersion: "2.0.0",
			MinimumSupportedMessage: "please upgrade",
		},
		catsBy: map[string][]*model.Category{
			"p1": {{ID: "c1", Name: "Photo", Slug: "photo", Description: "editors", Position: 1}},
		},
		plansBy: map[string][]*model.Plan{
			"p1": {{
				ID: "pl1", Name: "Pro", Slug: "pro", CheckoutID: "chk_1",
				LicenseType: "subscription", BillingInterval: "month", LicenseModel: "standard",
				StripePriceID: "price_SECRET", MaxActivations: 5,
			}},
		},
		releases: []*model.Release{{
			Version: "2.1.0", Channel: "stable", Name: "Spring", ReleaseNotes: "faster",
			Artifacts: []*model.ReleaseArtifact{
				{
					Platform: "windows-x64", Filename: "setup.exe", FileSize: 123,
					ContentType: "application/octet-stream", SHA256: "abc",
					FileKey: "storage/SECRET-KEY", Ed25519Sig: "SIG-SECRET",
					SigningKeyID: "key-SECRET",
				},
				// Not uploaded yet: nothing to show, nothing to leak.
				{Platform: "linux-x64", FileKey: "", SHA256: ""},
			},
		}},
	}
	w := mktGet(fake.handler(), "/marketplace/products/photo-suite", "detail",
		gin.Params{{Key: "slug", Value: "photo-suite"}})
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	for _, want := range []string{
		"photo-suite", "Photo Suite", "download_url", "minimum_supported_version",
		"categories", "photo", "plans", "checkout_id", "license_model",
		"releases", "2.1.0", "faster", "windows-x64", "sha256",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail body is missing %q: %s", want, body)
		}
	}
	for _, leak := range []string{
		"SECRET", "file_key", "ed25519", "signing_key", "stripe_price_id",
		"max_activations", "license_key", "linux-x64",
	} {
		if strings.Contains(body, leak) {
			t.Errorf("detail body leaks %q: %s", leak, body)
		}
	}

	// The plan card keeps the pricing shape's field names with the
	// amounts null: the catalog does not fan out to Stripe, and
	// /products/:product_slug/plans is where live prices come from.
	var envelope struct {
		Data struct {
			Product struct {
				Plans []map[string]any `json:"plans"`
			} `json:"product"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("body is not the envelope: %v", err)
	}
	if len(envelope.Data.Product.Plans) != 1 {
		t.Fatalf("plans = %d, want 1", len(envelope.Data.Product.Plans))
	}
	plan := envelope.Data.Product.Plans[0]
	for _, key := range []string{"name", "slug", "license_type", "checkout_id", "price", "currency"} {
		if _, ok := plan[key]; !ok {
			t.Errorf("plan card is missing %q", key)
		}
	}
	if plan["price"] != nil || plan["currency"] != nil {
		t.Errorf("plan card carries a price: %+v", plan)
	}

	// The changelog shows the published releases the feed already
	// publishes, and only the artifacts a licensed download could
	// actually hand over.
	if fake.gotRelease.Status != model.ReleaseStatusPublished || fake.gotRelease.Limit != marketplaceReleasesLimit {
		t.Errorf("release filter = %+v, want published x%d", fake.gotRelease, marketplaceReleasesLimit)
	}
}

// A product page batches its facets for the one card; the listing
// batches them for the whole page and answers in the shared list
// shape.
func TestMarketplaceListShapeAndBatching(t *testing.T) {
	fake := &fakeCatalog{
		products: []*model.Product{{ID: "p1", Name: "Photo Suite", Slug: "photo-suite", Type: "desktop"}},
		total:    1,
		catsBy:   map[string][]*model.Category{"p1": {{ID: "c1", Name: "Photo", Slug: "photo"}}},
		plansBy:  map[string][]*model.Plan{},
	}
	w := mktGet(fake.handler(), "/marketplace/products", "list", nil)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if len(fake.gotIDs) != 1 || fake.gotIDs[0] != "p1" {
		t.Errorf("batched ids = %v, want [p1]", fake.gotIDs)
	}
	var envelope struct {
		Data struct {
			Products []map[string]any `json:"products"`
			Total    int              `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("body is not the envelope: %v", err)
	}
	if envelope.Data.Total != 1 || len(envelope.Data.Products) != 1 {
		t.Fatalf("listing = %d rows, total %d, want 1", len(envelope.Data.Products), envelope.Data.Total)
	}
	card := envelope.Data.Products[0]
	if _, ok := card["categories"]; !ok {
		t.Error("card has no categories")
	}
	if _, ok := card["plans"]; !ok {
		t.Error("card has no plans")
	}
	// An empty plan set renders as [], not null.
	if got, _ := json.Marshal(card["plans"]); string(got) != "[]" {
		t.Errorf("plans = %s, want []", got)
	}
}

// The facet list is the filter menu: every category, in the order the
// store serves them, as an array however few there are.
func TestMarketplaceCategories(t *testing.T) {
	fake := &fakeCatalog{categories: []*model.Category{
		{ID: "c1", Name: "Photo", Slug: "photo", Position: 1},
		{ID: "c2", Name: "Audio", Slug: "audio", Position: 2},
	}}
	w := mktGet(fake.handler(), "/marketplace/categories", "categories", nil)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	for _, want := range []string{"photo", "audio", "categories"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("categories body is missing %q: %s", want, w.Body.String())
		}
	}
}
