package model

import (
	"strings"
	"time"

	"github.com/uptrace/bun"
)

// ─── Category (marketplace catalog) ───
//
// A category is an admin-managed facet of discovery: the public
// marketplace browses and filters products by it (plan §30). It is
// presentation-only — a category carries no price, no entitlement and
// no license meaning, so moving a product between categories can never
// change what a customer's license may do.
//
// Position orders the catalog listing. It is plain ordering data with
// no uniqueness: two categories may share a position and the listing
// falls back to name, then id, so the order is total either way.
type Category struct {
	bun.BaseModel `bun:"table:categories"`

	ID   string `bun:",pk" json:"id"`
	Name string `bun:",notnull" json:"name"`
	// Slug is the URL handle the public filter speaks
	// (?category=<slug>), stored canonical via NormalizeCategorySlug.
	// Unique: it is how the catalog addresses a category, and a second
	// spelling of one handle would make the filter answer for two.
	Slug        string `bun:",notnull,unique" json:"slug"`
	Description string `bun:",notnull,default:''" json:"description"`
	// Position has no bun `default:N` on purpose (see the note on
	// Plan): 0 is the top of the catalog, a meaningful zero the
	// handler must be able to write. The column keeps its CREATE-TABLE
	// default for plain SQL only.
	Position  int       `bun:",notnull" json:"position"`
	CreatedAt time.Time `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt time.Time `bun:",nullzero,default:now()" json:"updated_at"`
}

// ProductCategory is the product↔category join row. It lives in this
// file rather than model.go so the shared core needs no change for the
// marketplace round (model.go is contested ground under parallel
// agents); the join table is catalog-only and has no identity of its
// own beyond the pair.
//
// Both columns cascade on delete (see the migration): removing a
// category detaches it from its products, and removing a product drops
// its links — the join rows never outlive either side and never block
// a deletion. Deleting a category can therefore not strand or delete
// anything else: products, plans, licences and releases are untouched.
type ProductCategory struct {
	bun.BaseModel `bun:"table:product_categories"`

	ProductID  string `bun:",pk" json:"product_id"`
	CategoryID string `bun:",pk" json:"category_id"`
}

// NormalizeCategorySlug folds a category handle into its stored
// canonical form: lower-cased, every run of characters outside
// [a-z0-9] collapsed to a single hyphen, no leading or trailing
// hyphen. Folding rather than refusing means the one handle cannot
// exist in several spellings ("Dev Tools", "dev tools" and "DEV-TOOLS"
// are one category), which is what the unique index and the public
// ?category= filter both rely on.
//
// The fold is ASCII-only because apperr.ValidateSlug accepts only
// a-z0-9-: a name without those (a non-Latin name) folds to hyphen
// noise or to empty, and the caller then has to supply an explicit
// ASCII slug. The result is validated separately — this only
// canonicalizes, it never decides what is acceptable.
//
// Idempotent: folding a folded slug returns it unchanged.
func NormalizeCategorySlug(raw string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			hyphen = false
			continue
		}
		if b.Len() > 0 && !hyphen {
			b.WriteRune('-')
			hyphen = true
		}
	}
	// Runs collapse, so at most one trailing hyphen can be pending.
	return strings.TrimSuffix(b.String(), "-")
}
