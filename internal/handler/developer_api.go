package handler

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// DeveloperAPIHandler serves the versioned customer developer API
// (plan §33): the machine-facing mirror of the customer portal, spoken
// with a customer-minted `htc_sk_…` key (plan §34) that
// middleware.CustomerAPIKeyAuth has already resolved to its owner.
//
//	GET /v1/me        Key introspection (name, prefix, scopes, dates)
//	GET /v1/orders    The key owner's orders (paged, ?status=)
//	GET /v1/licenses  The owner's licences, METADATA ONLY
//
// Every answer is owner-scoped through the identity the auth
// middleware left in the context — never through anything the caller
// sent. A key can only ever read the rows of the user who minted it;
// there is no parameter anywhere below that can widen that.
//
// This handler is read-only and writes nothing. It shares the response
// envelope, pagination and error codes of every other surface; it is
// "developer" in audience, not in exception.
//
// Licence secrecy is structural here, not incidental: /v1/licenses
// serializes an explicit metadata DTO and never model.License, because
// that struct carries the licence key, its hash, its ciphertext, notes
// and external identifiers. A future field added to model.License with
// a non-hidden JSON tag therefore CANNOT leak through this endpoint —
// the DTO simply does not have it.
type DeveloperAPIHandler struct {
	store developerAPIStore
}

// developerAPIStore is the slice of the store this handler reads
// through. The methods are the existing exported queries (the
// portal's, reused rather than re-derived so ownership semantics
// cannot drift): orders are matched to the owner's email
// case-insensitively by ListOrdersByEmail, and licences ride on
// ListLicensesByEmail's owned-or-seated match with Plan + Product and
// the computed activation counts loaded.
type developerAPIStore interface {
	ListOrdersByEmail(ctx context.Context, email, status string, p store.Page) ([]*model.Order, int, error)
	ListLicensesByEmail(ctx context.Context, email string) ([]*model.License, error)
}

// *store.Store is the production implementation.
var _ developerAPIStore = (*store.Store)(nil)

// NewDeveloperAPIHandler wires the handler to the store. The Lead
// mounts the routes with middleware.CustomerAPIKeyAuth in front — this
// handler refuses unauthenticated calls itself but does not authenticate.
func NewDeveloperAPIHandler(s *store.Store) *DeveloperAPIHandler {
	return &DeveloperAPIHandler{store: s}
}

// Scope vocabulary for customer API keys, the customer half of
// model.ScopeAdmin etc. Kept here rather than in the model because
// these names only mean anything to /v1 routes; RequireCustomerScope
// enforces them per route.
const (
	// ScopeOrdersRead lets a key read its owner's orders.
	ScopeOrdersRead = "orders:read"
	// ScopeLicensesRead lets a key read its owner's licence metadata.
	ScopeLicensesRead = "licenses:read"
)

// developerAPIIdentity is the one place the authenticated identity is
// read: the key row and its owner's email, both set by
// middleware.CustomerAPIKeyAuth. Missing either (routes reached without
// the middleware) answers 401 — the same "unauthorized" the rest of the
// API gives an unauthenticated caller.
func developerAPIIdentity(c *gin.Context) (*model.CustomerAPIKey, string, bool) {
	email := emailFromContext(c)
	v, _ := c.Get("customer_api_key")
	k, _ := v.(*model.CustomerAPIKey)
	if k == nil || email == "" {
		response.Unauthorized(c, "unauthorized")
		return nil, "", false
	}
	return k, email, true
}

// developerKeyInfo is GET /v1/me — what the key says about itself.
// Deliberately secret-free: the display prefix is the only part of the
// secret that survives creation, and key_hash never leaves the database
// layer. last_used_at is the stored stamp as of this request's
// authentication (the stamp for THIS call may not have landed yet —
// last-used writes are throttled, see the auth middleware).
type developerKeyInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	KeyPrefix string `json:"key_prefix"`
	// Scopes is always a JSON array, empty when the key carries none.
	Scopes     []string   `json:"scopes"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	OwnerEmail string     `json:"owner_email"`
}

// Me answers GET /v1/me: key introspection. It proves the credential
// works and says what it may do — name, display prefix, scopes, expiry,
// last use, and the owner it speaks for. No scope gate: any valid key
// may describe itself (see RequireCustomerScope).
func (h *DeveloperAPIHandler) Me(c *gin.Context) {
	k, email, ok := developerAPIIdentity(c)
	if !ok {
		return
	}
	response.OK(c, developerKeyInfo{
		ID:         k.ID,
		Name:       k.Name,
		KeyPrefix:  k.KeyPrefix,
		Scopes:     response.Array(k.ScopeList()),
		ExpiresAt:  k.ExpiresAt,
		LastUsedAt: k.LastUsedAt,
		CreatedAt:  k.CreatedAt,
		OwnerEmail: email,
	})
}

// ListOrders answers GET /v1/orders — one page of the key owner's own
// orders, newest first, optionally narrowed by ?status=. Paged and
// filtered exactly like the portal's /portal/orders (same query, same
// closed status vocabulary, same envelope fields), so an SDK written
// against one works against the other. Money is int64 minor units in
// every field, as stored at purchase time.
//
// Ownership rides on the store query: ListOrdersByEmail matches
// lower(customer_email) to the owner's address and returns nothing for
// anybody else. `Cache-Control: no-store` marks the page as one
// customer's private ledger.
func (h *DeveloperAPIHandler) ListOrders(c *gin.Context) {
	_, email, ok := developerAPIIdentity(c)
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

// developerLicenseInfo is one licence as /v1/licenses reports it:
// METADATA ONLY. It exists so no serialization path can carry a
// credential out of this endpoint — no licence key (plaintext or
// encrypted), no key hash, no notes, no seat/activation identifiers,
// no payment or external identifiers. If model.License grows another
// secret tomorrow, this struct simply does not grow it.
type developerLicenseInfo struct {
	ID          string `json:"id"`
	ProductID   string `json:"product_id"`
	ProductName string `json:"product_name"`
	PlanID      string `json:"plan_id"`
	PlanName    string `json:"plan_name"`
	Status      string `json:"status"`
	// Activations: how many devices/sessions hold the licence now
	// (activation_count + active_session_count for floating plans),
	// and the plan's cap (0 = unlimited) so "2 of 3" reads out.
	ActivationCount    int `json:"activation_count"`
	ActiveSessionCount int `json:"active_session_count"`
	MaxActivations     int `json:"max_activations"`

	ValidFrom    time.Time  `json:"valid_from"`
	ValidUntil   *time.Time `json:"valid_until,omitempty"`
	UpdatesUntil *time.Time `json:"updates_until,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
}

// developerLicenseRows projects loaded licences onto the metadata
// DTO. Nil relations degrade to empty names — a licence whose product
// row is gone still reports its ids, status and dates.
func developerLicenseRows(lics []*model.License) []developerLicenseInfo {
	out := make([]developerLicenseInfo, 0, len(lics))
	for _, l := range lics {
		if l == nil {
			continue
		}
		row := developerLicenseInfo{
			ID:                 l.ID,
			ProductID:          l.ProductID,
			PlanID:             l.PlanID,
			Status:             l.Status,
			ActivationCount:    l.ActivationCount,
			ActiveSessionCount: l.ActiveSessionCount,
			ValidFrom:          l.ValidFrom,
			ValidUntil:         l.ValidUntil,
			UpdatesUntil:       l.UpdatesUntil,
			CreatedAt:          l.CreatedAt,
		}
		if l.Product != nil {
			row.ProductName = l.Product.Name
		}
		if l.Plan != nil {
			row.PlanName = l.Plan.Name
			row.MaxActivations = l.Plan.MaxActivations
		}
		out = append(out, row)
	}
	return out
}

// ListLicenses answers GET /v1/licenses — the licences the key owner
// holds, as metadata only (see developerLicenseInfo for exactly what
// that excludes). The set is the same one the portal's /portal/licenses
// shows the customer — owned licences plus licences shared with them
// by seat — matched by email inside ListLicensesByEmail, so one key can
// never see another user's rows. Not paged: a customer's licence count
// is small and bounded by their purchases, and the portal page this
// mirrors answers with the full set too.
func (h *DeveloperAPIHandler) ListLicenses(c *gin.Context) {
	_, email, ok := developerAPIIdentity(c)
	if !ok {
		return
	}
	lics, err := h.store.ListLicensesByEmail(c.Request.Context(), email)
	if err != nil {
		response.Internal(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.OK(c, gin.H{"licenses": developerLicenseRows(lics)})
}
