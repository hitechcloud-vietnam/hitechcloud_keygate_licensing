package handler

import (
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// PortalCommerceHandler exposes the customer-portal commerce API:
// the logged-in customer's orders, invoices and downloads.
//
//	GET    /portal/orders                  List own orders (status, paging)
//	GET    /portal/orders/:id              Order with items + invoices
//	GET    /portal/orders/:id/invoices     Invoices of one own order
//	GET    /portal/invoices/:id            Invoice with its order's lines
//	GET    /portal/downloads               Release artifacts entitled by own licences
//
// Mounted behind SessionAuth (like the rest of /portal); the session
// identity is the email claim the middleware leaves in the context.
// Ownership is email matching, case-insensitive, against the order's
// customer_email — and every cross-customer lookup answers exactly like
// a missing row (404), so the endpoints are never an existence oracle.
//
// Money is int64 minor units end to end; the DTO shapes are the same
// model.Order / model.Invoice serialization the admin ledger answers
// with, so the portal and the admin views of an order cannot drift.
// This handler is read-only — it writes nothing and audits nothing.
type PortalCommerceHandler struct {
	store *store.Store
}

// NewPortalCommerceHandler wires the handler to the commerce ledger.
func NewPortalCommerceHandler(s *store.Store) *PortalCommerceHandler {
	return &PortalCommerceHandler{store: s}
}

// portalCommerceEmail returns the session user's email, or writes the
// 401 and reports false when the session did not carry one.
func portalCommerceEmail(c *gin.Context) (string, bool) {
	email := emailFromContext(c)
	if email == "" {
		response.Unauthorized(c, "unauthorized")
		return "", false
	}
	return email, true
}

// portalOrderStatusOK reports whether status is a filter this endpoint
// accepts. The same closed set the admin list accepts — one vocabulary
// per ledger — with empty meaning "all of them".
func portalOrderStatusOK(status string) bool {
	switch status {
	case "", model.OrderStatusPending, model.OrderStatusPaid,
		model.OrderStatusFailed, model.OrderStatusRefunded:
		return true
	}
	return false
}

// ListOrders answers GET /portal/orders — one page of the customer's
// own orders, newest first, optionally narrowed to one status.
func (h *PortalCommerceHandler) ListOrders(c *gin.Context) {
	email, ok := portalCommerceEmail(c)
	if !ok {
		return
	}
	status := c.Query("status")
	if !portalOrderStatusOK(status) {
		response.BadRequest(c, "status must be pending, paid, failed, or refunded")
		return
	}
	page := listPage(c)
	orders, total, err := h.store.ListOrdersByEmail(c.Request.Context(), email, status, page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	listOK(c, "orders", orders, total, page)
}

// GetOrder answers GET /portal/orders/:id — the order with its line
// items and every invoice drawn against it. An order that exists but
// belongs to somebody else is answered exactly like a missing one.
func (h *PortalCommerceHandler) GetOrder(c *gin.Context) {
	email, ok := portalCommerceEmail(c)
	if !ok {
		return
	}
	o, err := h.store.FindOrderByIdAndEmail(c.Request.Context(), c.Param("id"), email)
	if err != nil {
		portalWriteLookupErr(c, err, "order not found")
		return
	}
	invoices, err := h.store.ListInvoicesByOrder(c.Request.Context(), o.ID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.OK(c, gin.H{"order": o, "invoices": response.Array(invoices)})
}

// ListInvoices answers GET /portal/orders/:id/invoices — every invoice
// drawn against one of the customer's own orders.
func (h *PortalCommerceHandler) ListInvoices(c *gin.Context) {
	email, ok := portalCommerceEmail(c)
	if !ok {
		return
	}
	// Ownership first, then the invoices: the invoice list of an order
	// that is not yours must be the same 404 the order itself is.
	o, err := h.store.FindOrderByIdAndEmail(c.Request.Context(), c.Param("id"), email)
	if err != nil {
		portalWriteLookupErr(c, err, "order not found")
		return
	}
	invoices, err := h.store.ListInvoicesByOrder(c.Request.Context(), o.ID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	listOK(c, "invoices", invoices, len(invoices), listPage(c))
}

// GetInvoice answers GET /portal/invoices/:id — the invoice and the
// owning order with its line items, so one page can render a receipt.
func (h *PortalCommerceHandler) GetInvoice(c *gin.Context) {
	email, ok := portalCommerceEmail(c)
	if !ok {
		return
	}
	inv, err := h.store.FindInvoiceForEmail(c.Request.Context(), c.Param("id"), email)
	if err != nil {
		portalWriteLookupErr(c, err, "invoice not found")
		return
	}
	o, err := h.store.FindOrderByIdAndEmail(c.Request.Context(), inv.OrderID, email)
	if err != nil {
		portalWriteLookupErr(c, err, "invoice not found")
		return
	}
	c.Header("Cache-Control", "no-store")
	response.OK(c, gin.H{"invoice": inv, "order": o})
}

// portalWriteLookupErr turns an ownership-checked lookup into its
// response: a miss (which is also "not yours") is a quiet 404, anything
// else is an internal error. One status for both miss and not-owned is
// deliberate — see the store queries, which cannot tell them apart.
func portalWriteLookupErr(c *gin.Context, err error, msg string) {
	if errors.Is(err, sql.ErrNoRows) {
		response.NotFound(c, msg)
		return
	}
	response.Internal(c, err)
}

// ─── Downloads ───
//
// The downloads page lists release artifacts the customer is entitled
// to. Entitlement is derived from the licences they OWN — the same gate
// the license-gated download endpoint applies (service.IsLicenseUsable
// + the licence's maintenance cutoff + only published releases with
// uploaded artifacts), just answered as a list instead of per request.
//
// No new download authorization is invented here: rows carry the
// metadata of the EXISTING license-gated POST /license/download flow
// (license_id to pair with the key the portal already hands its owner,
// platform + version of the artifact) plus the product's stable
// download page. No presigned links are minted by this handler, and no
// license keys or signing keys ever appear in a row.

// portalEntitlement is one product the customer may download from,
// with the maintenance cutoff of the licence that entitles them. Nil
// cutoff means the licence includes updates for life (or follows the
// subscription).
type portalEntitlement struct {
	LicenseID    string
	Product      *model.Product
	UpdatesUntil *time.Time
}

// portalEntitlements derives download entitlement from the customer's
// licences. Mirrors the /license/download gate: a licence must be
// usable right now and its product must ship installable releases.
// Where several licences entitle the same product, the most generous
// maintenance period wins — unlimited beats any date, a later date
// beats an earlier one.
func portalEntitlements(lics []*model.License) []portalEntitlement {
	byProduct := map[string]portalEntitlement{}
	var order []string
	for _, l := range lics {
		if l == nil || l.Product == nil {
			continue
		}
		if !model.ProductSupports(l.Product.Type, model.CapReleases) {
			continue
		}
		if !service.IsLicenseUsable(l) {
			continue
		}
		cut := l.EffectiveUpdatesUntil()
		cur, seen := byProduct[l.ProductID]
		if !seen {
			byProduct[l.ProductID] = portalEntitlement{
				LicenseID: l.ID, Product: l.Product, UpdatesUntil: cut,
			}
			order = append(order, l.ProductID)
			continue
		}
		// Keep the more generous cutoff; the licence id stays with it
		// so the UI can pair the row with the right key.
		if cur.UpdatesUntil != nil && (cut == nil || cut.After(*cur.UpdatesUntil)) {
			byProduct[l.ProductID] = portalEntitlement{
				LicenseID: l.ID, Product: l.Product, UpdatesUntil: cut,
			}
		}
	}
	out := make([]portalEntitlement, 0, len(order))
	for _, id := range order {
		out = append(out, byProduct[id])
	}
	return out
}

// portalDownloadView is one downloadable artifact row. Deliberately
// slim: metadata and pointers into the EXISTING download flow, never a
// credential — no license key, no signing key, no freshly minted URL.
type portalDownloadView struct {
	LicenseID   string     `json:"license_id"`
	ProductID   string     `json:"product_id"`
	ProductName string     `json:"product_name"`
	ProductSlug string     `json:"product_slug"`
	Version     string     `json:"version"`
	Channel     string     `json:"channel"`
	Platform    string     `json:"platform"`
	Filename    string     `json:"filename"`
	FileSize    int64      `json:"file_size"`
	SHA256      string     `json:"sha256"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	// DownloadURL is the product's stable download page — the same
	// {{.DownloadURL}} the delivery emails link to. A page, never a
	// signed file link: those expire, can be forwarded, and skip the
	// license check. Empty when the vendor has not configured one.
	DownloadURL string `json:"download_url"`
}

// portalDownloadRows flattens the entitled product's releases into one
// row per downloadable artifact. Only uploads that finished are listed
// (model.ReleaseArtifact.IsUploaded), and platform — when the request
// asked for one — narrows each release to its matching artifact.
func portalDownloadRows(ent portalEntitlement, releases []*model.Release, platform string) []portalDownloadView {
	var out []portalDownloadView
	for _, rel := range releases {
		if rel == nil {
			continue
		}
		for _, a := range rel.Artifacts {
			if !a.IsUploaded() {
				continue
			}
			if platform != "" && a.Platform != platform {
				continue
			}
			name := a.Filename
			if name == "" {
				name = service.DownloadFilename(rel, a)
			}
			out = append(out, portalDownloadView{
				LicenseID:   ent.LicenseID,
				ProductID:   ent.Product.ID,
				ProductName: ent.Product.Name,
				ProductSlug: ent.Product.Slug,
				Version:     rel.Version,
				Channel:     rel.Channel,
				Platform:    a.Platform,
				Filename:    name,
				FileSize:    a.FileSize,
				SHA256:      a.SHA256,
				PublishedAt: rel.PublishedAt,
				DownloadURL: ent.Product.DownloadURL,
			})
		}
	}
	return out
}

// portalDownloadReleaseLimit caps the release window fetched per
// product. The downloads page shows what is current, not a full
// version archive; 100 releases per product is years of shipping.
const portalDownloadReleaseLimit = 100

// ListDownloads answers GET /portal/downloads — the release artifacts
// the customer's own licences entitle them to, newest first across all
// their products. Optional ?channel= and ?platform= narrow the list to
// the client the portal user is downloading for.
func (h *PortalCommerceHandler) ListDownloads(c *gin.Context) {
	email, ok := portalCommerceEmail(c)
	if !ok {
		return
	}
	channel := strings.TrimSpace(c.Query("channel"))
	if channel != "" && !model.IsValidReleaseChannel(channel) {
		response.BadRequest(c, "channel must be stable, beta, alpha, or dev")
		return
	}
	platform := service.NormalizePlatform(strings.TrimSpace(c.Query("platform")))
	if platform != "" && !service.IsValidPlatform(platform) {
		response.BadRequest(c,
			"platform must be one of: "+strings.Join(service.AllowedPlatforms(), ", "))
		return
	}

	lics, err := h.store.ListPortalOwnedLicenses(c.Request.Context(), email)
	if err != nil {
		response.Internal(c, err)
		return
	}
	out := []portalDownloadView{}
	for _, ent := range portalEntitlements(lics) {
		rels, err := h.store.ListPortalDownloadReleases(c.Request.Context(),
			ent.Product.ID, ent.UpdatesUntil, channel, platform, portalDownloadReleaseLimit)
		if err != nil {
			response.Internal(c, err)
			return
		}
		out = append(out, portalDownloadRows(ent, rels, platform)...)
	}
	// Newest first wherever the row came from. The tiebreaks make the
	// order total: two products may publish in the same second, and a
	// list that reorders between requests is impossible to page.
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := out[i].PublishedAt, out[j].PublishedAt
		switch {
		case pi == nil && pj == nil:
		case pi == nil:
			return false
		case pj == nil:
			return true
		case !pi.Equal(*pj):
			return pi.After(*pj)
		}
		if out[i].ProductName != out[j].ProductName {
			return out[i].ProductName < out[j].ProductName
		}
		return out[i].Version > out[j].Version
	})
	c.Header("Cache-Control", "no-store")
	response.OK(c, gin.H{"downloads": response.Array(out)})
}
