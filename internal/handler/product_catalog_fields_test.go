package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// The §223 catalog URL fields are sanity-checked on write: garbage is
// refused with 400 before any store is touched — javascript: and
// friends are not links a storefront may show, and nothing here
// fetches a URL to find out. The handler runs with no store at all: a
// regression that reached the write path would panic this test rather
// than pass it.
func TestCreateProductRejectsBadCatalogFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := `"name":"App","slug":"catalog-app","type":"desktop"`
	for _, tc := range []struct{ name, extra string }{
		{"logo_url not a url", `"logo_url":"not a url"`},
		{"logo_url script scheme", `"logo_url":"javascript:alert(1)"`},
		{"documentation_url no host", `"documentation_url":"https://"`},
		{"website_url ftp", `"website_url":"ftp://example.com"`},
		{"repository_url bare host", `"repository_url":"example.com/repo"`},
		{"images garbage entry", `"images":["https://example.com/a.png","relative.png"]`},
		{"images empty entry", `"images":[""]`},
		{"images too many", `"images":[` + strings.Repeat(`"https://example.com/x.png",`, maxProductImages) + `"https://example.com/y.png"]`},
		{"url too long", `"website_url":"https://example.com/` + strings.Repeat("a", 2048) + `"`},
		{"description too long", `"description":"` + strings.Repeat("a", maxProductDescription+1) + `"`},
		{"short_description too long", `"short_description":"` + strings.Repeat("a", maxProductShortDescription+1) + `"`},
		{"vendor too long", `"vendor":"` + strings.Repeat("a", maxProductVendor+1) + `"`},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/admin/products",
			strings.NewReader(`{`+base+`,`+tc.extra+`}`))
		c.Request.Header.Set("Content-Type", "application/json")
		(&AdminHandler{}).CreateProduct(c)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400; body %s", tc.name, w.Code, w.Body.String())
		}
	}
}

// The gallery folds to its stored form: entries trimmed and kept in
// order, and nil survives as nil — the sentinel an update reads as
// "keep the stored value", distinct from `[]` which clears.
func TestNormalizeImagesFoldsEntries(t *testing.T) {
	got, err := normalizeImages([]string{" https://example.com/a.png ", "https://example.com/b.png"})
	if err != nil {
		t.Fatalf("normalizeImages: %v", err)
	}
	if len(got) != 2 || got[0] != "https://example.com/a.png" || got[1] != "https://example.com/b.png" {
		t.Errorf("folded = %v, want trimmed in order", got)
	}
	if got, err := normalizeImages(nil); err != nil || got != nil {
		t.Errorf("nil = %v, %v; want nil, nil (omitted)", got, err)
	}
	if got, err := normalizeImages([]string{}); err != nil || got == nil || len(got) != 0 {
		t.Errorf("[] = %v, %v; want empty non-nil (clear)", got, err)
	}
	if _, err := normalizeImages([]string{"https://example.com/ok.png", "::::"}); err == nil {
		t.Error("a garbage entry was accepted")
	}
}

// category_ids fold the way ReplaceProductCategories folds them:
// trimmed, empties dropped, duplicates collapsed. nil is "not sent"
// (keep the links) and `[]` is the clear.
func TestNormalizeCategoryIDsFoldsEntries(t *testing.T) {
	got := normalizeCategoryIDs([]string{" c1 ", "", "c1", "c2"})
	if len(got) != 2 || got[0] != "c1" || got[1] != "c2" {
		t.Errorf("folded = %v, want [c1 c2]", got)
	}
	if got := normalizeCategoryIDs(nil); got != nil {
		t.Errorf("nil = %v, want nil (omitted)", got)
	}
	if got := normalizeCategoryIDs([]string{}); got == nil || len(got) != 0 {
		t.Errorf("[] = %v, want empty non-nil (clear)", got)
	}
}

// setupCatalogTest opens the shared test database (skipped without
// TEST_DATABASE_URL) and an admin handler on it — the product_floor
// pattern.
func setupCatalogTest(t *testing.T) (*store.Store, *AdminHandler) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := store.New(dsn)
	if err != nil {
		t.Skipf("skipping integration test: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	gin.SetMode(gin.TestMode)
	return s, &AdminHandler{Store: s, FeedURLTTL: time.Hour}
}

// productAdmin drives the product CRUD routes the way main wires
// them, with the route named rather than rebuilt per call.
func productAdmin(t *testing.T, h *AdminHandler, route, method, target string, params gin.Params, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	c.Request = req
	c.Params = params
	switch route {
	case "create":
		h.CreateProduct(c)
	case "update":
		h.UpdateProduct(c)
	case "get":
		h.GetProduct(c)
	case "list":
		h.ListProducts(c)
	case "categories":
		h.SetProductCategories(c)
	}
	return w
}

// catalogProduct is the admin product DTO as a client reads it.
type catalogProduct struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	ShortDescription string   `json:"short_description"`
	LogoURL          string   `json:"logo_url"`
	Images           []string `json:"images"`
	DocumentationURL string   `json:"documentation_url"`
	WebsiteURL       string   `json:"website_url"`
	RepositoryURL    string   `json:"repository_url"`
	Vendor           string   `json:"vendor"`
	Categories       []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
	} `json:"categories"`
}

func decodeProduct(t *testing.T, w *httptest.ResponseRecorder) catalogProduct {
	t.Helper()
	var out struct {
		Data catalogProduct `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v; body %s", err, w.Body.String())
	}
	return out.Data
}

// The catalog fields and category links round-trip through the admin
// API with the one write convention: omitted keeps the stored value,
// "" clears it (an explicit [] clears images and category_ids), and
// the answers carry the fields plus the facets the edit form
// prefills.
func TestAdminProductCatalogFieldsRoundTrip(t *testing.T) {
	s, h := setupCatalogTest(t)
	ctx := context.Background()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	cat1 := &model.Category{Name: "Editors " + suffix, Slug: "editors-" + suffix, Position: 1}
	cat2 := &model.Category{Name: "Photo " + suffix, Slug: "photo-" + suffix, Position: 2}
	for _, cat := range []*model.Category{cat1, cat2} {
		if err := s.CreateCategory(ctx, cat); err != nil {
			t.Fatalf("create category: %v", err)
		}
		defer s.DeleteCategory(ctx, cat.ID)
	}

	body := `{"name":"Suite ` + suffix + `","slug":"suite-` + suffix + `","type":"desktop",` +
		`"description":"Edit photos with confidence","short_description":"Photo editing made easy",` +
		`"logo_url":"https://example.com/logo.png",` +
		`"images":["https://example.com/a.png","https://example.com/b.png"],` +
		`"documentation_url":"https://docs.example.com/photo",` +
		`"website_url":"https://example.com/photo",` +
		`"repository_url":"https://github.com/example/photo",` +
		`"vendor":"Acme",` +
		`"category_ids":["` + cat1.ID + `","` + cat2.ID + `"]}`
	w := productAdmin(t, h, "create", http.MethodPost, "/admin/products", nil, body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: status %d, want 201; body %s", w.Code, w.Body.String())
	}

	// The response DTO names every field — and the facets — even when
	// one is empty, so a form can rely on the keys existing.
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, key := range []string{
		"description", "short_description", "logo_url", "images",
		"documentation_url", "website_url", "repository_url", "vendor", "categories",
	} {
		if _, ok := envelope.Data[key]; !ok {
			t.Errorf("create response is missing %q: %s", key, w.Body.String())
		}
	}

	p := decodeProduct(t, w)
	if p.Description != "Edit photos with confidence" || p.ShortDescription != "Photo editing made easy" ||
		p.LogoURL != "https://example.com/logo.png" || p.Vendor != "Acme" ||
		p.DocumentationURL != "https://docs.example.com/photo" ||
		p.WebsiteURL != "https://example.com/photo" ||
		p.RepositoryURL != "https://github.com/example/photo" {
		t.Errorf("create round trip lost fields: %+v", p)
	}
	if len(p.Images) != 2 || p.Images[0] != "https://example.com/a.png" {
		t.Errorf("images = %v, want the two sent", p.Images)
	}
	if len(p.Categories) != 2 {
		t.Errorf("categories = %+v, want both sent", p.Categories)
	}

	// Omitted fields keep their stored value; the empty string clears.
	w = productAdmin(t, h, "update", http.MethodPut, "/admin/products/"+p.ID,
		gin.Params{{Key: "id", Value: p.ID}}, `{"description":"","vendor":"Acme GmbH"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update clear: status %d; body %s", w.Code, w.Body.String())
	}
	p = decodeProduct(t, w)
	if p.Description != "" {
		t.Errorf("description = %q, want cleared", p.Description)
	}
	if p.Vendor != "Acme GmbH" {
		t.Errorf("vendor = %q, want %q", p.Vendor, "Acme GmbH")
	}
	if p.ShortDescription != "Photo editing made easy" {
		t.Errorf("short_description = %q, want the omitted field kept", p.ShortDescription)
	}

	// images: an explicit [] clears the gallery; the answer renders []
	// and never null.
	w = productAdmin(t, h, "update", http.MethodPut, "/admin/products/"+p.ID,
		gin.Params{{Key: "id", Value: p.ID}}, `{"images":[]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update images clear: status %d; body %s", w.Code, w.Body.String())
	}
	p = decodeProduct(t, w)
	if len(p.Images) != 0 {
		t.Errorf("images = %v, want cleared", p.Images)
	}
	if !strings.Contains(w.Body.String(), `"images":[]`) {
		t.Errorf("cleared gallery is not rendered as []: %s", w.Body.String())
	}

	// A refused URL write changes nothing that is stored.
	w = productAdmin(t, h, "update", http.MethodPut, "/admin/products/"+p.ID,
		gin.Params{{Key: "id", Value: p.ID}}, `{"website_url":"notaurl"}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("update bad url: status %d, want 400; body %s", w.Code, w.Body.String())
	}
	got := decodeProduct(t, productAdmin(t, h, "get", http.MethodGet, "/admin/products/"+p.ID,
		gin.Params{{Key: "id", Value: p.ID}}, ""))
	if got.WebsiteURL != "https://example.com/photo" {
		t.Errorf("website_url = %q after refused write, want the stored value", got.WebsiteURL)
	}

	// category_ids: replace, untouched when omitted, clear on [].
	w = productAdmin(t, h, "update", http.MethodPut, "/admin/products/"+p.ID,
		gin.Params{{Key: "id", Value: p.ID}}, `{"category_ids":["`+cat2.ID+`"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update categories: status %d; body %s", w.Code, w.Body.String())
	}
	p = decodeProduct(t, w)
	if len(p.Categories) != 1 || p.Categories[0].ID != cat2.ID {
		t.Errorf("categories = %+v, want just %s", p.Categories, cat2.ID)
	}

	w = productAdmin(t, h, "update", http.MethodPut, "/admin/products/"+p.ID,
		gin.Params{{Key: "id", Value: p.ID}}, `{"name":"Renamed `+suffix+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update name: status %d; body %s", w.Code, w.Body.String())
	}
	p = decodeProduct(t, w)
	if len(p.Categories) != 1 || p.Categories[0].ID != cat2.ID {
		t.Errorf("categories = %+v after a write that omitted them, want untouched [%s]", p.Categories, cat2.ID)
	}

	// An unknown id is the caller's 400 before anything is written —
	// and the links stay as they were.
	w = productAdmin(t, h, "update", http.MethodPut, "/admin/products/"+p.ID,
		gin.Params{{Key: "id", Value: p.ID}}, `{"category_ids":["no-such-category"]}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("update unknown category: status %d, want 400; body %s", w.Code, w.Body.String())
	}
	p = decodeProduct(t, productAdmin(t, h, "get", http.MethodGet, "/admin/products/"+p.ID,
		gin.Params{{Key: "id", Value: p.ID}}, ""))
	if len(p.Categories) != 1 || p.Categories[0].ID != cat2.ID {
		t.Errorf("categories = %+v after refused write, want untouched [%s]", p.Categories, cat2.ID)
	}

	w = productAdmin(t, h, "update", http.MethodPut, "/admin/products/"+p.ID,
		gin.Params{{Key: "id", Value: p.ID}}, `{"category_ids":[]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("update categories clear: status %d; body %s", w.Code, w.Body.String())
	}
	p = decodeProduct(t, w)
	if len(p.Categories) != 0 {
		t.Errorf("categories = %+v, want cleared", p.Categories)
	}

	// The listing carries the same fields and facets per row.
	w = productAdmin(t, h, "list", http.MethodGet, "/admin/products?search=Renamed+"+suffix, nil, "")
	if w.Code != http.StatusOK {
		t.Fatalf("list: status %d; body %s", w.Code, w.Body.String())
	}
	var listOut struct {
		Data struct {
			Products []catalogProduct `json:"products"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listOut); err != nil {
		t.Fatalf("decode list: %v; body %s", err, w.Body.String())
	}
	if len(listOut.Data.Products) != 1 {
		t.Fatalf("list = %d rows, want 1; body %s", len(listOut.Data.Products), w.Body.String())
	}
	row := listOut.Data.Products[0]
	if row.Vendor != "Acme GmbH" || row.Images == nil || row.Categories == nil {
		t.Errorf("list row = %+v, want the catalog fields and facets present", row)
	}
}

// The categories write route replaces the links wholesale — the
// multi-select's own save — and answers with the product's new facets.
func TestSetProductCategoriesReplacesWholesale(t *testing.T) {
	s, h := setupCatalogTest(t)
	ctx := context.Background()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	prod := &model.Product{Name: "Routed " + suffix, Slug: "routed-" + suffix, Type: "desktop"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("create product: %v", err)
	}
	defer s.DeleteProduct(ctx, prod.ID)
	cat1 := &model.Category{Name: "One " + suffix, Slug: "one-" + suffix, Position: 1}
	cat2 := &model.Category{Name: "Two " + suffix, Slug: "two-" + suffix, Position: 2}
	for _, cat := range []*model.Category{cat1, cat2} {
		if err := s.CreateCategory(ctx, cat); err != nil {
			t.Fatalf("create category: %v", err)
		}
		defer s.DeleteCategory(ctx, cat.ID)
	}

	target := "/admin/products/" + prod.ID + "/categories"
	params := gin.Params{{Key: "id", Value: prod.ID}}

	// The key is required: there is no "half a save" here, and a
	// missing key must not read as the clear.
	w := productAdmin(t, h, "categories", http.MethodPut, target, params, `{}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("missing category_ids: status %d, want 400; body %s", w.Code, w.Body.String())
	}

	w = productAdmin(t, h, "categories", http.MethodPut, target, params,
		`{"category_ids":["`+cat1.ID+`","`+cat2.ID+`"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("set categories: status %d; body %s", w.Code, w.Body.String())
	}
	p := decodeProduct(t, w)
	if len(p.Categories) != 2 {
		t.Errorf("categories = %+v, want both", p.Categories)
	}

	w = productAdmin(t, h, "categories", http.MethodPut, target, params,
		`{"category_ids":["`+cat2.ID+`"]}`)
	p = decodeProduct(t, w)
	if len(p.Categories) != 1 || p.Categories[0].ID != cat2.ID {
		t.Errorf("categories = %+v, want just %s", p.Categories, cat2.ID)
	}

	w = productAdmin(t, h, "categories", http.MethodPut, target, params, `{"category_ids":[]}`)
	p = decodeProduct(t, w)
	if len(p.Categories) != 0 {
		t.Errorf("categories = %+v, want cleared", p.Categories)
	}

	w = productAdmin(t, h, "categories", http.MethodPut, target, params,
		`{"category_ids":["no-such-category"]}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("unknown category: status %d, want 400; body %s", w.Code, w.Body.String())
	}
}

// The pricing shape carries the product blurb above its table — the
// §223 fields only, no Stripe material and no licence data.
func TestPublicPlansCarriesProductContext(t *testing.T) {
	s, _ := setupCatalogTest(t)
	ctx := context.Background()
	suffix := strconv.FormatInt(time.Now().UnixNano(), 10)

	prod := &model.Product{
		Name: "Priced " + suffix, Slug: "priced-" + suffix, Type: "desktop",
		ShortDescription: "One liner.", Description: "Long copy.",
		LogoURL: "https://example.com/logo.png",
	}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("create product: %v", err)
	}
	defer s.DeleteProduct(ctx, prod.ID)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/products/"+prod.Slug+"/plans", nil)
	c.Params = gin.Params{{Key: "product_slug", Value: prod.Slug}}
	NewPublicPlansHandler(s, nil).ListPlans(c)
	if w.Code != http.StatusOK {
		t.Fatalf("plans: status %d; body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`"product"`, `"short_description":"One liner."`, `"description":"Long copy."`,
		`"logo_url":"https://example.com/logo.png"`, `"plans"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plans body is missing %s: %s", want, body)
		}
	}
}
