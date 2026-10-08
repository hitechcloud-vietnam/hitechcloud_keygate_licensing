package handler

import (
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// The product page carries the §223 catalog fields — and still
// nothing that must not leak. The fixture puts secrets on the models
// (a storage key, a signature, a signing key id, a Stripe price id)
// so the assertion is about the payload, never the row.
func TestMarketplaceCarriesProductCatalogFields(t *testing.T) {
	fake := &fakeCatalog{
		product: &model.Product{
			ID: "p1", Name: "Photo Suite", Slug: "photo-suite", Type: "desktop",
			Vendor:           "Acme",
			Description:      "Edit photos with confidence",
			ShortDescription: "Photo editing made easy",
			LogoURL:          "https://example.com/logo.png",
			Images:           []string{"https://example.com/a.png", "https://example.com/b.png"},
			DocumentationURL: "https://docs.example.com/photo",
			WebsiteURL:       "https://example.com/photo",
			RepositoryURL:    "https://github.com/example/photo",
			DownloadURL:      "https://example.com/download",
		},
		catsBy:  map[string][]*model.Category{},
		plansBy: map[string][]*model.Plan{},
		releases: []*model.Release{{
			Version: "2.1.0", Channel: "stable",
			Artifacts: []*model.ReleaseArtifact{{
				Platform: "windows-x64", Filename: "setup.exe",
				FileKey: "storage/SECRET-KEY", Ed25519Sig: "SIG-SECRET", SigningKeyID: "key-SECRET",
				SHA256: "abc", ContentType: "application/octet-stream",
			}},
		}},
	}
	w := mktGet(fake.handler(), "/marketplace/products/photo-suite", "detail",
		gin.Params{{Key: "slug", Value: "photo-suite"}})
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{
		`"description":"Edit photos with confidence"`,
		`"short_description":"Photo editing made easy"`,
		`"logo_url":"https://example.com/logo.png"`,
		`"images":["https://example.com/a.png","https://example.com/b.png"]`,
		`"documentation_url":"https://docs.example.com/photo"`,
		`"website_url":"https://example.com/photo"`,
		`"repository_url":"https://github.com/example/photo"`,
		`"vendor":"Acme"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("detail body is missing %s: %s", want, body)
		}
	}
	// The leak guard is field selection, and it still holds now that
	// more of the row is rendered.
	for _, leak := range []string{
		"SECRET", "file_key", "ed25519", "signing_key", "stripe_price_id",
		"max_activations", "license_key",
	} {
		if strings.Contains(body, leak) {
			t.Errorf("detail body leaks %q: %s", leak, body)
		}
	}
}

// A gallery that was never filled renders as [], never null — the one
// non-string of the catalog fields, on cards and on the product page
// alike.
func TestMarketplaceEmptyImagesRenderAsArray(t *testing.T) {
	fake := &fakeCatalog{
		product:  &model.Product{ID: "p1", Name: "Bare", Slug: "bare", Type: "saas"},
		products: []*model.Product{{ID: "p1", Name: "Bare", Slug: "bare", Type: "saas"}},
		total:    1,
		catsBy:   map[string][]*model.Category{},
		plansBy:  map[string][]*model.Plan{},
	}
	w := mktGet(fake.handler(), "/marketplace/products/bare", "detail",
		gin.Params{{Key: "slug", Value: "bare"}})
	if w.Code != 200 {
		t.Fatalf("detail status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"images":[]`) {
		t.Errorf("detail images is not rendered as []: %s", w.Body.String())
	}
	w = mktGet(fake.handler(), "/marketplace/products", "list", nil)
	if !strings.Contains(w.Body.String(), `"images":[]`) {
		t.Errorf("card images is not rendered as []: %s", w.Body.String())
	}
}

// Listing cards are cheap to enrich and carry what a card renders:
// the blurb and the logo. The plans and facets shape is unchanged.
func TestMarketplaceListCardsCarryBlurb(t *testing.T) {
	fake := &fakeCatalog{
		products: []*model.Product{{
			ID: "p1", Name: "Photo Suite", Slug: "photo-suite", Type: "desktop",
			ShortDescription: "Photo editing made easy",
			LogoURL:          "https://example.com/logo.png",
		}},
		total:   1,
		catsBy:  map[string][]*model.Category{},
		plansBy: map[string][]*model.Plan{},
	}
	w := mktGet(fake.handler(), "/marketplace/products", "list", nil)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	for _, want := range []string{
		`"short_description":"Photo editing made easy"`,
		`"logo_url":"https://example.com/logo.png"`,
	} {
		if !strings.Contains(w.Body.String(), want) {
			t.Errorf("card body is missing %s: %s", want, w.Body.String())
		}
	}
}
