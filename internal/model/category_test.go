package model

import (
	"encoding/json"
	"strings"
	"testing"
)

// The fold exists so one handle has one stored spelling. Whatever the
// admin types, the same category name keeps resolving to the same slug
// — and folding a folded slug must not move it again.
func TestNormalizeCategorySlug(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"dev-tools", "dev-tools"},
		{"Dev Tools", "dev-tools"},
		{"  dev   tools  ", "dev-tools"},
		{"DEV-TOOLS", "dev-tools"},
		{"Data_Science", "data-science"},
		{"tools--2026!", "tools-2026"},
		{"-edge-", "edge"},
		{"2026", "2026"},
		{"", ""},
		{"   ", ""},
		{"!!!", ""},
		// ASCII-only fold: non-letters become hyphens rather than
		// being transliterated. Validating the result is the caller's
		// job (apperr.ValidateSlug), not this fold's.
		{"café", "caf"},
	}
	for _, tc := range cases {
		if got := NormalizeCategorySlug(tc.in); got != tc.want {
			t.Errorf("NormalizeCategorySlug(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Idempotence: a canonical slug is a fixed point.
	for _, slug := range []string{"dev-tools", "a1", "tools-2026", "data-science"} {
		if got := NormalizeCategorySlug(slug); got != slug {
			t.Errorf("NormalizeCategorySlug(%q) = %q, fold is not idempotent", slug, got)
		}
	}
}

// The canonical form must fit apperr.ValidateSlug — otherwise the
// handler could fold a name into something it then refuses, and the
// "derive the slug from the name" path would never work.
func TestNormalizeCategorySlugProducesValidSlugs(t *testing.T) {
	for _, name := range []string{"Dev Tools", "AI & ML", "Data-Science 101", "x y z"} {
		slug := NormalizeCategorySlug(name)
		if slug == "" {
			t.Errorf("NormalizeCategorySlug(%q) is empty", name)
			continue
		}
		if slug[0] == '-' || slug[len(slug)-1] == '-' {
			t.Errorf("NormalizeCategorySlug(%q) = %q has an edge hyphen", name, slug)
		}
		if strings.Contains(slug, "--") {
			t.Errorf("NormalizeCategorySlug(%q) = %q has a doubled hyphen", name, slug)
		}
		for _, r := range slug {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-') {
				t.Errorf("NormalizeCategorySlug(%q) = %q contains %q", name, slug, r)
			}
		}
	}
}

// The join row is a pair of ids and nothing else — the public catalog
// must never gain a field here that leaks into a payload later.
func TestProductCategoryJSONIsJustThePair(t *testing.T) {
	raw, err := json.Marshal(&ProductCategory{ProductID: "p1", CategoryID: "c1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 || got["product_id"] != "p1" || got["category_id"] != "c1" {
		t.Errorf("ProductCategory JSON = %s, want exactly product_id + category_id", raw)
	}
}

// The admin API hands the row straight to JSON; these are the fields a
// category is allowed to carry to any client.
func TestCategoryJSONShape(t *testing.T) {
	raw, err := json.Marshal(&Category{ID: "c1", Name: "Dev Tools", Slug: "dev-tools"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"id", "name", "slug", "description", "position", "created_at", "updated_at"} {
		if _, ok := got[key]; !ok {
			t.Errorf("Category JSON is missing %q: %s", key, raw)
		}
	}
}
