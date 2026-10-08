package store_test

import (
	"context"
	"testing"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// The §223 catalog columns round-trip through bun: the text array
// survives a write and read, a column-limited update writes only what
// it names, and the nullable columns of a row written before the
// migration (NULL) read as zero values — not a scan error.
func TestProductCatalogColumnsRoundTrip(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := uniqueSuffix()

	prod := &model.Product{
		Name: "Catalog Fields " + suffix, Slug: "catalog-fields-" + suffix, Type: "desktop",
		Description: "Long storefront copy.", ShortDescription: "One liner.",
		LogoURL:          "https://example.com/logo.png",
		Images:           []string{"https://example.com/a.png", "https://example.com/b.png"},
		DocumentationURL: "https://docs.example.com",
		WebsiteURL:       "https://example.com",
		RepositoryURL:    "https://github.com/example/app",
		Vendor:           "Acme",
	}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("create product: %v", err)
	}
	defer s.DeleteProduct(ctx, prod.ID)

	got, err := s.FindProductByID(ctx, prod.ID)
	if err != nil {
		t.Fatalf("find product: %v", err)
	}
	if got.Description != prod.Description || got.ShortDescription != prod.ShortDescription ||
		got.LogoURL != prod.LogoURL || got.DocumentationURL != prod.DocumentationURL ||
		got.WebsiteURL != prod.WebsiteURL || got.RepositoryURL != prod.RepositoryURL ||
		got.Vendor != prod.Vendor {
		t.Errorf("round trip lost fields: %+v", got)
	}
	if len(got.Images) != 2 || got.Images[0] != prod.Images[0] || got.Images[1] != prod.Images[1] {
		t.Errorf("images = %v, want %v", got.Images, prod.Images)
	}

	// Column-limited update: only the named columns are written, so
	// the fields this write does not name keep their stored values.
	got.Description = ""
	got.Images = []string{}
	got.Vendor = "Acme GmbH"
	if err := store.UpdateProductIn(ctx, s.DB, got, "description", "images", "vendor"); err != nil {
		t.Fatalf("update product: %v", err)
	}
	got, err = s.FindProductByID(ctx, prod.ID)
	if err != nil {
		t.Fatalf("re-find product: %v", err)
	}
	if got.Description != "" || len(got.Images) != 0 || got.Vendor != "Acme GmbH" {
		t.Errorf("after clear = %q %v %q, want \"\" [] \"Acme GmbH\"", got.Description, got.Images, got.Vendor)
	}
	if got.ShortDescription != prod.ShortDescription || got.LogoURL != prod.LogoURL {
		t.Errorf("untouched fields moved: %q %q", got.ShortDescription, got.LogoURL)
	}

	// The pre-migration row shape: every catalog column NULL. It must
	// read as zero values, not fail the scan.
	if _, err := s.DB.NewRaw(`UPDATE products SET
			description = NULL, short_description = NULL, logo_url = NULL, images = NULL,
			documentation_url = NULL, website_url = NULL, repository_url = NULL, vendor = NULL
		WHERE id = ?`, prod.ID).Exec(ctx); err != nil {
		t.Fatalf("null the columns: %v", err)
	}
	got, err = s.FindProductByID(ctx, prod.ID)
	if err != nil {
		t.Fatalf("find product with NULL catalog columns: %v", err)
	}
	if got.Description != "" || got.ShortDescription != "" || got.LogoURL != "" ||
		got.DocumentationURL != "" || got.WebsiteURL != "" || got.RepositoryURL != "" ||
		got.Vendor != "" {
		t.Errorf("NULL columns read as %+v, want empty values", got)
	}
	if len(got.Images) != 0 {
		t.Errorf("NULL images read as %v, want empty", got.Images)
	}
}
