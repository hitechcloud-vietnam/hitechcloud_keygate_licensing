package handler

import (
	"context"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// ─── Public marketplace catalog ───
//
// The anonymous storefront's read side (plan §30 + Phase 6): browse
// categories, discover products, open a product page.
//
//	GET /marketplace/categories        every category, in catalog order
//	GET /marketplace/products          discovery listing (search, category, sort, paging)
//	GET /marketplace/products/:slug    product page (detail, plans, changelog)
//
// Anonymous and read-only. Everything returned is catalog data a
// storefront renders: names, handles, the plans a visitor may buy, and
// the changelog the vendor already publishes in the update feeds. It
// deliberately carries no credentials, no storage or signing keys, no
// Stripe identifiers, and no licence material — the download itself
// stays behind /license/download.
//
// Prices are not in this database: the Stripe Price is the source of
// truth (payment.CheckoutByPlan reads it at checkout). The plan cards
// carry the same field names as /products/:product_slug/plans with
// price and currency null — that endpoint resolves live amounts
// through its own cache, and fanning one Stripe call per plan out of
// every anonymous listing would put a public page on our Stripe rate
// budget (see PublicPlansHandler).

// marketplaceReleasesLimit is how much changelog a product page shows:
// the latest published releases. The feeds list more, but a product
// page is not a download archive.
const marketplaceReleasesLimit = 20

// marketplaceCatalog is the slice of store.Store this handler needs.
// The real constructor takes *store.Store; tests substitute a fake so
// the 404 and the response shapes can be covered without a database.
type marketplaceCatalog interface {
	FindProductBySlug(ctx context.Context, slug string) (*model.Product, error)
	ListCategoriesByPosition(ctx context.Context) ([]*model.Category, error)
	ListMarketplaceProducts(ctx context.Context, f store.MarketplaceProductFilter) ([]*model.Product, int, error)
	CategoriesForProducts(ctx context.Context, productIDs []string) (map[string][]*model.Category, error)
	ActivePlansForProducts(ctx context.Context, productIDs []string) (map[string][]*model.Plan, error)
	ListReleases(ctx context.Context, f store.ReleaseFilter) ([]*model.Release, error)
}

var _ marketplaceCatalog = (*store.Store)(nil)

type MarketplaceHandler struct {
	mk marketplaceCatalog
}

func NewMarketplaceHandler(s *store.Store) *MarketplaceHandler {
	return &MarketplaceHandler{mk: s}
}

// marketplaceSortColumns is the whole of the ?sort= vocabulary the
// discovery listing accepts. It resolves client-facing names to the
// expression listSort lets into the ORDER BY; anything else is
// refused with 400 before a query is built, so nothing a caller typed
// is ever spliced into SQL. "newest" leads because a catalog that
// just gained a product should show it.
var marketplaceSortColumns = map[string]sortCol{
	"name":   {Expr: "product.name"},
	"newest": {Expr: "product.created_at", Desc: true},
}

// ListCategories — GET /marketplace/categories
//
// Every category in the order the admin arranged them: the filter
// menu of the discovery listing, and a small enough set to serve whole.
func (h *MarketplaceHandler) ListCategories(c *gin.Context) {
	cats, err := h.mk.ListCategoriesByPosition(c)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, gin.H{"categories": categoryArray(cats)})
}

// ListProducts — GET /marketplace/products
//
// Query: search (name/slug), category (slug, via the product↔category
// join), sort (name|newest) + order (asc|desc), limit/offset. An
// unknown category is not an error — it simply matches no products.
// Each card carries its categories and its active plans' public
// pricing shape.
func (h *MarketplaceHandler) ListProducts(c *gin.Context) {
	// Sort is resolved before anything touches the store: a name that
	// is not in the allowlist is refused here and cannot reach SQL.
	sortSpec, ok := listSort(c, marketplaceSortColumns, "newest")
	if !ok {
		return
	}
	f := store.MarketplaceProductFilter{
		Search:       strings.TrimSpace(c.Query("search")),
		CategorySlug: strings.ToLower(strings.TrimSpace(c.Query("category"))),
		Sort:         sortSpec,
		Page:         listPage(c),
	}
	prods, total, err := h.mk.ListMarketplaceProducts(c, f)
	if err != nil {
		response.Internal(c, err)
		return
	}
	items, err := h.productCards(c, prods)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "products", items, total, f.Page)
}

// GetProduct — GET /marketplace/products/:slug
//
// The product page: the product's own catalog fields, its categories,
// its active plans, and the latest published releases with their
// changelog. An unknown slug — and a lookup that failed — answer the
// same 404, so slug guesses learn nothing from the difference (the
// PublicPlansHandler convention).
//
// What it deliberately does NOT carry: yanked or draft releases,
// storage keys, signatures and signing key identifiers, Stripe price
// ids, per-licence limits (max_activations and friends), entitlements
// and any licence material. The download itself is license-gated at
// /license/download.
func (h *MarketplaceHandler) GetProduct(c *gin.Context) {
	slug := strings.ToLower(strings.TrimSpace(c.Param("slug")))
	if slug == "" {
		response.BadRequest(c, "slug is required")
		return
	}
	prod, err := h.mk.FindProductBySlug(c, slug)
	if err != nil {
		response.NotFound(c, "product not found")
		return
	}

	catsBy, err := h.mk.CategoriesForProducts(c, []string{prod.ID})
	if err != nil {
		response.Internal(c, err)
		return
	}
	plansBy, err := h.mk.ActivePlansForProducts(c, []string{prod.ID})
	if err != nil {
		response.Internal(c, err)
		return
	}
	rels, err := h.mk.ListReleases(c, store.ReleaseFilter{
		ProductID: prod.ID,
		Status:    model.ReleaseStatusPublished,
		Limit:     marketplaceReleasesLimit,
	})
	if err != nil {
		response.Internal(c, err)
		return
	}

	out := marketplaceProductJSON(prod, catsBy[prod.ID], plansBy[prod.ID])
	out["releases"] = releaseArray(rels)
	response.OK(c, gin.H{"product": out})
}

// productCards decorates a page of products with their categories and
// plans. One batch query per relation, however many cards the page
// carries: the listing is the hottest public endpoint and twenty
// round trips per page load is how a catalog dies.
func (h *MarketplaceHandler) productCards(c *gin.Context, prods []*model.Product) ([]gin.H, error) {
	if len(prods) == 0 {
		return []gin.H{}, nil
	}
	ids := make([]string, 0, len(prods))
	for _, p := range prods {
		ids = append(ids, p.ID)
	}
	catsBy, err := h.mk.CategoriesForProducts(c, ids)
	if err != nil {
		return nil, err
	}
	plansBy, err := h.mk.ActivePlansForProducts(c, ids)
	if err != nil {
		return nil, err
	}
	out := make([]gin.H, 0, len(prods))
	for _, p := range prods {
		out = append(out, marketplaceProductJSON(p, catsBy[p.ID], plansBy[p.ID]))
	}
	return out, nil
}

// marketplaceProductJSON is the product card of the listing and the
// skeleton of the product page.
//
// The field selection is the whole of the leak-prevention on the
// product side: it names what the catalog shows and nothing else, so a
// column added to model.Product tomorrow does not silently become
// public. The §223 catalog fields that have no column yet (description,
// short description, logo, images, documentation/website/repository
// URLs, vendor) are absent until their migration lands; download_url
// and the maintenance floor are the marketing data that exists.
func marketplaceProductJSON(p *model.Product, cats []*model.Category, plans []*model.Plan) gin.H {
	return gin.H{
		"id":                        p.ID,
		"name":                      p.Name,
		"slug":                      p.Slug,
		"type":                      p.Type,
		"download_url":              p.DownloadURL,
		"minimum_supported_version": p.MinimumSupportedVersion,
		"minimum_supported_message": p.MinimumSupportedMessage,
		"created_at":                p.CreatedAt,
		"categories":                categoryArray(cats),
		"plans":                     planArray(plans),
	}
}

// categoryArray renders categories for a public payload. Empty is an
// empty array, never null, so a client iterating the field never has
// to special-case a product with no facets.
func categoryArray(cats []*model.Category) []gin.H {
	out := make([]gin.H, 0, len(cats))
	for _, cat := range cats {
		out = append(out, gin.H{
			"id":          cat.ID,
			"name":        cat.Name,
			"slug":        cat.Slug,
			"description": cat.Description,
			"position":    cat.Position,
		})
	}
	return out
}

// planArray renders plans in the field names
// PublicPlansHandler.ListPlans uses, so a pricing page can treat the
// two sources alike: name/slug/license_type/billing_interval/
// checkout_id plus price and currency. Price and currency are always
// null here — the amounts live on Stripe and are served, cached, by
// GET /products/:product_slug/plans — and license_model (standard |
// floating) is the §223 "license model", which lives on the plan.
// Per-licence limits are deliberately absent.
func planArray(plans []*model.Plan) []gin.H {
	out := make([]gin.H, 0, len(plans))
	for _, p := range plans {
		out = append(out, gin.H{
			"id":               p.ID,
			"name":             p.Name,
			"slug":             p.Slug,
			"license_type":     p.LicenseType,
			"billing_interval": p.BillingInterval,
			"license_model":    p.LicenseModel,
			"checkout_id":      p.CheckoutID,
			"price":            nil,
			"currency":         nil,
		})
	}
	return out
}

// releaseArray renders the changelog: what the vendor already
// publishes in the update feeds, and no more. Artifacts name the
// platforms and checksums of what a licensed download will hand over;
// only uploaded artifacts are listed, since an unfinished one is not
// downloadable. Storage keys, signatures and signing key identifiers
// are not rendered — the field selection, not a scrub, is what keeps
// them out of the payload.
func releaseArray(rels []*model.Release) []gin.H {
	out := make([]gin.H, 0, len(rels))
	for _, r := range rels {
		arts := make([]gin.H, 0, len(r.Artifacts))
		for _, a := range r.Artifacts {
			if !a.IsUploaded() {
				continue
			}
			arts = append(arts, gin.H{
				"platform":     a.Platform,
				"filename":     a.Filename,
				"file_size":    a.FileSize,
				"content_type": a.ContentType,
				"sha256":       a.SHA256,
			})
		}
		out = append(out, gin.H{
			"version":       r.Version,
			"channel":       r.Channel,
			"name":          r.Name,
			"release_notes": r.ReleaseNotes,
			"published_at":  r.PublishedAt,
			"artifacts":     arts,
		})
	}
	return out
}
