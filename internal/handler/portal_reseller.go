package handler

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// PortalResellerHandler is the reseller's self-service view inside the
// customer portal (plan §31 / Phase 7, slice 2).
//
//	GET /portal/reseller/me           Profile + summary numbers
//	GET /portal/reseller/licenses     Licences allocated to the partner (paging)
//	GET /portal/reseller/commissions  Own commission ledger (status, paging)
//	GET /portal/reseller/prices       Own wholesale price overrides
//
// Identity — no new authentication is invented here. The portal
// session (middleware.SessionAuth) leaves the signed-in user's email
// in the context, exactly as for every other /portal route, and the
// session user whose email matches a resellers.contact_email IS that
// reseller: the partner signs in to the portal like any customer and
// the contact address on the account is the handle. A session whose
// email matches no reseller answers 404 (no existence oracle), and
// every read is filtered by the session email's reseller row, so
// cross-reseller reads are impossible by construction — there is no
// request parameter that names a reseller to leak across.
//
// The handler is read-only: a partner sees what they earned, what they
// own and what they pay; changing any of it is the admin API's
// business. Money is int64 minor units and rates are integer basis
// points, the same model serialization the admin answers with, so the
// two views cannot drift.
type PortalResellerHandler struct {
	store portalResellerStore
}

// portalResellerStore is the slice of store.Store this handler needs.
// The real constructor takes *store.Store; tests substitute a fake so
// the scoping and the non-reseller 404 run without a database.
type portalResellerStore interface {
	FindResellerByEmail(ctx context.Context, email string) (*model.Reseller, error)
	CountResellerLicenses(ctx context.Context, resellerID string) (int, error)
	ListResellerLicenses(ctx context.Context, resellerID string, p store.Page) ([]*model.License, int, error)
	ListCommissions(ctx context.Context, resellerID, status string, p store.Page) ([]*model.Commission, int, error)
	SumCommissionsByStatus(ctx context.Context, resellerID string) (map[string]int64, error)
	CountResellerPriceOverrides(ctx context.Context, resellerID string) (int, error)
	ListResellerPriceOverrides(ctx context.Context, resellerID string) ([]*model.ResellerPriceOverride, error)
}

var _ portalResellerStore = (*store.Store)(nil)

// NewPortalResellerHandler wires the handler to the store.
func NewPortalResellerHandler(s *store.Store) *PortalResellerHandler {
	return &PortalResellerHandler{store: s}
}

// resolveReseller maps the session identity onto a reseller account:
// the session email that matches a resellers.contact_email IS that
// reseller. A missing session identity is 401; an email that is not a
// reseller's contact address is the same quiet 404 as an account that
// does not exist, so these endpoints cannot be used to probe which
// addresses are partners. On failure the response is already written.
func (h *PortalResellerHandler) resolveReseller(c *gin.Context) (*model.Reseller, bool) {
	email := emailFromContext(c)
	if email == "" {
		response.Unauthorized(c, "unauthorized")
		return nil, false
	}
	r, err := h.store.FindResellerByEmail(c.Request.Context(), email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.NotFound(c, "reseller not found")
			return nil, false
		}
		response.Internal(c, err)
		return nil, false
	}
	return r, true
}

// Me answers GET /portal/reseller/me — the partner's account beside
// its summary numbers: how many licences it owns, what the commission
// ledger adds up to per status, and how many plans it holds a
// wholesale price for. The status sums come back zero-filled across
// the closed vocabulary so a typed client never has to special-case a
// missing key.
func (h *PortalResellerHandler) Me(c *gin.Context) {
	r, ok := h.resolveReseller(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	licenseCount, err := h.store.CountResellerLicenses(ctx, r.ID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	sums, err := h.store.SumCommissionsByStatus(ctx, r.ID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	priceCount, err := h.store.CountResellerPriceOverrides(ctx, r.ID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	totals := make(map[string]int64, len(model.CommissionStatuses))
	for _, status := range model.CommissionStatuses {
		totals[status] = sums[status]
	}
	c.Header("Cache-Control", "no-store")
	response.OK(c, gin.H{
		"reseller":             r,
		"license_count":        licenseCount,
		"commission_totals":    totals,
		"price_override_count": priceCount,
	})
}

// ListLicenses answers GET /portal/reseller/licenses — one page of the
// licences allocated to the partner, licence rows like the admin
// view. Scoping is by construction: the reseller id comes from the
// session email's account, never from the request.
func (h *PortalResellerHandler) ListLicenses(c *gin.Context) {
	r, ok := h.resolveReseller(c)
	if !ok {
		return
	}
	page := listPage(c)
	licenses, total, err := h.store.ListResellerLicenses(c.Request.Context(), r.ID, page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	listOK(c, "licenses", licenses, total, page)
}

// ListCommissions answers GET /portal/reseller/commissions — one page
// of the caller's own ledger, newest accrual first, optionally
// narrowed to one status of the closed vocabulary. A status filter
// outside it is refused rather than silently served an empty page.
func (h *PortalResellerHandler) ListCommissions(c *gin.Context) {
	r, ok := h.resolveReseller(c)
	if !ok {
		return
	}
	status := c.Query("status")
	if status != "" && !model.ValidCommissionStatus(status) {
		response.BadRequest(c, "status must be one of: "+strings.Join(model.CommissionStatuses, ", "))
		return
	}
	page := listPage(c)
	rows, total, err := h.store.ListCommissions(c.Request.Context(), r.ID, status, page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	listOK(c, "commissions", rows, total, page)
}

// ListPrices answers GET /portal/reseller/prices — the wholesale
// prices this partner pays, per plan. The list is bounded (the plans a
// partner sells), so it answers unpaged under the standard list
// envelope, total = len.
func (h *PortalResellerHandler) ListPrices(c *gin.Context) {
	r, ok := h.resolveReseller(c)
	if !ok {
		return
	}
	rows, err := h.store.ListResellerPriceOverrides(c.Request.Context(), r.ID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	listOK(c, "prices", rows, len(rows), listPage(c))
}
