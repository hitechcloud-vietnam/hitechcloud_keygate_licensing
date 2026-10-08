package handler

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// ─── Resellers ───
//
// Admin CRUD for reseller accounts and the allocation of licences to
// them (plan §31 / Phase 7). A reseller is a partner that owns (has
// sold) a set of licences; this slice keeps the account and that
// ownership record. Wholesale pricing, commissions and a reseller API
// build on these rows later.
//
//	GET    /admin/resellers                     List (search, status, paging)
//	POST   /admin/resellers                     Create
//	GET    /admin/resellers/:id                 Get one (with allocated-licence count)
//	PATCH  /admin/resellers/:id                 Update (partial)
//	DELETE /admin/resellers/:id                 Delete (refuses while licences allocated)
//	POST   /admin/resellers/:id/licenses        Allocate a licence {license_id}
//	GET    /admin/resellers/:id/licenses        List allocated licences (paging)
//	DELETE /admin/resellers/:id/licenses/:license_id   Deallocate one licence
//
// resellerAdminStore is the slice of store.Store this handler needs.
// The real constructor takes *store.Store; tests substitute a fake so
// the refusal paths — duplicate email, double allocation, delete with
// allocations — run without a database.
type resellerAdminStore interface {
	ListResellers(ctx context.Context, search, status string, p store.Page) ([]*model.Reseller, int, error)
	FindResellerByID(ctx context.Context, id string) (*model.Reseller, error)
	CreateReseller(ctx context.Context, r *model.Reseller) error
	UpdateReseller(ctx context.Context, r *model.Reseller) error
	DeleteReseller(ctx context.Context, id string) error
	CountResellerLicenses(ctx context.Context, resellerID string) (int, error)
	AllocateLicense(ctx context.Context, resellerID, licenseID string) error
	DeallocateLicense(ctx context.Context, licenseID string) error
	ListResellerLicenses(ctx context.Context, resellerID string, p store.Page) ([]*model.License, int, error)
	Audit(ctx context.Context, log *model.AuditLog)
}

var _ resellerAdminStore = (*store.Store)(nil)

type ResellerAdminHandler struct {
	store resellerAdminStore
}

func NewResellerAdminHandler(s *store.Store) *ResellerAdminHandler {
	return &ResellerAdminHandler{store: s}
}

// prepareReseller folds a row into its stored form — trimmed name, a
// folded contact address, a defaulted and validated status, a
// trimmed notes field — and refuses what a reseller cannot carry: an
// empty name, an unusable email, a status outside the vocabulary, or a
// commission rate outside 0–10000 bps. Create and update both run it,
// so a value refused at creation cannot be put on the same reseller a
// moment later. It returns apperr so writeAppErr writes it.
func prepareReseller(r *model.Reseller) *apperr.AppError {
	r.Name = strings.TrimSpace(r.Name)
	if err := apperr.ValidateName("name", r.Name); err != nil {
		return err
	}

	// The contact address is stored folded (trimmed + lower-cased) so
	// one address cannot exist under two spellings — the unique index
	// and FindResellerByEmail both rely on it. Validate the folded
	// value: it is what gets stored, so it is what must be acceptable.
	r.ContactEmail = model.NormalizeResellerEmail(r.ContactEmail)
	if err := apperr.ValidateEmail(r.ContactEmail); err != nil {
		return err
	}

	// Status is a closed vocabulary (active|suspended). Empty defaults
	// to active; anything outside the two is refused rather than
	// silently stored, because status feeds allocation rules and, later,
	// commission eligibility.
	if r.Status == "" {
		r.Status = model.ResellerStatusActive
	} else if !model.ValidResellerStatus(r.Status) {
		return apperr.BadRequest("status must be one of: " + strings.Join(model.ResellerStatuses, ", "))
	}

	// commission_bps is basis points (10000 = 100%), integer only — the
	// money discipline: percentages are bps, never floats. The rate is
	// bounded to a real percentage.
	if r.CommissionBPS < 0 || r.CommissionBPS > 10000 {
		return apperr.BadRequest("commission_bps must be between 0 and 10000")
	}

	r.Notes = strings.TrimSpace(r.Notes)
	return nil
}

// List — GET /admin/resellers
//
// Query: search (name/email), status (active|suspended), limit/offset.
// An unrecognised status filter is refused rather than silently
// returning nothing, so a typo is visible instead of an empty page.
func (h *ResellerAdminHandler) List(c *gin.Context) {
	page := listPage(c)
	status := c.Query("status")
	if status != "" && !model.ValidResellerStatus(status) {
		response.BadRequest(c, "status must be one of: "+strings.Join(model.ResellerStatuses, ", "))
		return
	}
	resellers, total, err := h.store.ListResellers(c, c.Query("search"), status, page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "resellers", resellers, total, page)
}

// Get — GET /admin/resellers/:id
//
// Answers the account beside how many licences it currently owns, so
// the detail view can show the allocation size without a second call.
func (h *ResellerAdminHandler) Get(c *gin.Context) {
	id := c.Param("id")
	r, err := h.store.FindResellerByID(c, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("RESELLER", id))
			return
		}
		response.Internal(c, err)
		return
	}
	count, err := h.store.CountResellerLicenses(c, id)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, gin.H{"reseller": r, "license_count": count})
}

// Create — POST /admin/resellers
//
// Body: { name, contact_email, status?, commission_bps?, notes? }. A
// missing status defaults to active. A contact_email already claimed
// by another reseller is a 409 the admin can act on.
func (h *ResellerAdminHandler) Create(c *gin.Context) {
	var req struct {
		Name          string `json:"name" binding:"required"`
		ContactEmail  string `json:"contact_email" binding:"required"`
		Status        string `json:"status"`
		CommissionBPS int    `json:"commission_bps"`
		Notes         string `json:"notes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "name and contact_email are required")
		return
	}

	r := &model.Reseller{
		Name:          req.Name,
		ContactEmail:  req.ContactEmail,
		Status:        req.Status,
		CommissionBPS: req.CommissionBPS,
		Notes:         req.Notes,
	}
	if verr := prepareReseller(r); verr != nil {
		writeAppErr(c, verr)
		return
	}
	if err := h.store.CreateReseller(c, r); err != nil {
		// The contact email is the reseller's handle and the column is
		// unique; an insert refused here is almost always that address
		// taken, which the admin can act on. Anything else is not their
		// doing.
		if store.IsResellerEmailConflict(err) {
			response.Err(c, 409, "DUPLICATE", "reseller contact email already exists")
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "reseller", EntityID: r.ID, Action: "created",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.Created(c, r)
}

// Update — PATCH /admin/resellers/:id
//
// Body: any of { name, contact_email, status, commission_bps, notes }.
// A field the request leaves out keeps its stored value; a field it
// names is re-validated like a create, so the two paths cannot drift
// apart on what a legal reseller is.
func (h *ResellerAdminHandler) Update(c *gin.Context) {
	id := c.Param("id")
	r, err := h.store.FindResellerByID(c, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("RESELLER", id))
			return
		}
		response.Internal(c, err)
		return
	}

	var req struct {
		Name          *string `json:"name"`
		ContactEmail  *string `json:"contact_email"`
		Status        *string `json:"status"`
		CommissionBPS *int    `json:"commission_bps"`
		Notes         *string `json:"notes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	if req.Name != nil {
		r.Name = *req.Name
	}
	if req.ContactEmail != nil {
		r.ContactEmail = *req.ContactEmail
	}
	if req.Status != nil {
		r.Status = *req.Status
	}
	if req.CommissionBPS != nil {
		r.CommissionBPS = *req.CommissionBPS
	}
	if req.Notes != nil {
		r.Notes = *req.Notes
	}
	if verr := prepareReseller(r); verr != nil {
		writeAppErr(c, verr)
		return
	}
	if err := h.store.UpdateReseller(c, r); err != nil {
		if store.IsResellerEmailConflict(err) {
			response.Err(c, 409, "DUPLICATE", "reseller contact email already exists")
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "reseller", EntityID: r.ID, Action: "updated",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.OK(c, r)
}

// Delete — DELETE /admin/resellers/:id
//
// The documented delete policy: a reseller that still owns licences is
// REFUSED (409), not silently emptied. Those allocations are the
// commercial record of what the partner sold — the commission slice
// will read them — so the admin deallocates (or suspends the account)
// first. Only a reseller with no allocations can be deleted, and its
// own row is all that is then removed. See store.DeleteReseller and
// model.ResellerLicense.
func (h *ResellerAdminHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	if err := h.store.DeleteReseller(c, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("RESELLER", id))
			return
		}
		if errors.Is(err, store.ErrResellerHasAllocations) {
			response.Err(c, 409, "RESSELLER_HAS_ALLOCATIONS", "reseller still owns licenses; deallocate them before deleting")
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "reseller", EntityID: id, Action: "deleted",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.NoContent(c)
}

// AllocateLicense — POST /admin/resellers/:id/licenses
//
// Body: { license_id }. The licence must exist and must not already be
// owned: a licence belongs to at most one reseller (see
// model.ResellerLicense), so a second claim is a 409, and a licence_id
// naming nothing is a 400 the caller can fix.
func (h *ResellerAdminHandler) AllocateLicense(c *gin.Context) {
	resellerID := c.Param("id")
	var req struct {
		LicenseID string `json:"license_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "license_id is required")
		return
	}
	licenseID := strings.TrimSpace(req.LicenseID)
	if licenseID == "" {
		response.BadRequest(c, "license_id is required")
		return
	}
	if err := h.store.AllocateLicense(c, resellerID, licenseID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("RESELLER", resellerID))
			return
		}
		if errors.Is(err, store.ErrAllocationLicenseNotFound) {
			// LICENSE_NOT_FOUND is 404 everywhere in this codebase
			// (one status per error code — pkg/response contract).
			response.Err(c, 404, "LICENSE_NOT_FOUND", "license_id must name an existing license")
			return
		}
		if store.IsResellerLicenseConflict(err) {
			response.Err(c, 409, "ALREADY_ALLOCATED", "license is already allocated to a reseller")
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "reseller_license", EntityID: licenseID, Action: "allocated",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.Created(c, gin.H{"reseller_id": resellerID, "license_id": licenseID})
}

// ListLicenses — GET /admin/resellers/:id/licenses
//
// One page of the licences this reseller owns, as licence rows. The
// reseller is checked first so a missing account is a 404 rather than
// an empty page that reads like "owns nothing".
func (h *ResellerAdminHandler) ListLicenses(c *gin.Context) {
	resellerID := c.Param("id")
	if _, err := h.store.FindResellerByID(c, resellerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("RESELLER", resellerID))
			return
		}
		response.Internal(c, err)
		return
	}
	page := listPage(c)
	licenses, total, err := h.store.ListResellerLicenses(c, resellerID, page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "licenses", licenses, total, page)
}

// DeallocateLicense — DELETE /admin/resellers/:id/licenses/:license_id
//
// Keyed on the licence alone (store.DeallocateLicense(licenseID)): a
// licence is owned by at most one reseller, so the licence id names
// exactly one allocation and the removal is unambiguous — the route's
// :id is the account context, not needed to locate the row. A licence
// with no allocation is a 404, not a 204 for nothing.
func (h *ResellerAdminHandler) DeallocateLicense(c *gin.Context) {
	licenseID := c.Param("license_id")
	if err := h.store.DeallocateLicense(c, licenseID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.Err(c, 404, "NOT_ALLOCATED", "license is not allocated to a reseller")
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "reseller_license", EntityID: licenseID, Action: "deallocated",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.NoContent(c)
}
