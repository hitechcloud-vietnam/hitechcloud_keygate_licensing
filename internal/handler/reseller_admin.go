package handler

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"strings"
	"time"

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
// Slice 2 (commission ledger + wholesale pricing):
//
//	GET    /admin/resellers/:id/commissions     List the ledger (status, paging)
//	POST   /admin/resellers/:id/commissions     Accrue {order_id, basis_minor, bps?}
//	POST   /admin/resellers/:id/commissions/:commission_id/paid   Mark paid
//	GET    /admin/resellers/:id/prices          List wholesale price overrides
//	PUT    /admin/resellers/:id/prices/:plan_id Set one {unit_amount_minor, currency}
//	DELETE /admin/resellers/:id/prices/:plan_id Delete one
//
// Money is int64 minor units and rates are integer basis points —
// never float. The accrual is idempotent per (reseller, order): a
// retry answers the original row, never a second payout.
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

	// Commission ledger (slice 2).
	AccrueCommission(ctx context.Context, cm *model.Commission) (*model.Commission, bool, error)
	FindCommissionByID(ctx context.Context, id string) (*model.Commission, error)
	ListCommissions(ctx context.Context, resellerID, status string, p store.Page) ([]*model.Commission, int, error)
	MarkCommissionPaid(ctx context.Context, id string, paidAt time.Time) (*model.Commission, error)

	// Wholesale price overrides (slice 2).
	FindResellerPriceOverride(ctx context.Context, resellerID, planID string) (*model.ResellerPriceOverride, error)
	ListResellerPriceOverrides(ctx context.Context, resellerID string) ([]*model.ResellerPriceOverride, error)
	SetResellerPriceOverride(ctx context.Context, o *model.ResellerPriceOverride) error
	DeleteResellerPriceOverride(ctx context.Context, resellerID, planID string) error

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
		if errors.Is(err, store.ErrResellerHasCommissions) {
			response.Err(c, 409, "RESSELLER_HAS_COMMISSIONS", "reseller still has commission records; settle or cancel them before deleting")
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

// ─── Commission ledger ───
//
// The record of what a partner earned per sale. Every row is
// self-contained (basis, rate and the exact amount are snapshotted at
// accrual), and the accrual is idempotent per (reseller, order) — a
// retried POST answers the original row, never a second payout.
// Money is int64 minor units, rates are integer basis points.

// ListCommissions — GET /admin/resellers/:id/commissions
//
// Query: status (accrued|approved|paid|cancelled), limit/offset. The
// reseller is checked first so a missing account is a 404 rather than
// an empty page that reads like "earned nothing"; an unrecognised
// status filter is refused rather than silently returning nothing.
func (h *ResellerAdminHandler) ListCommissions(c *gin.Context) {
	resellerID := c.Param("id")
	if _, err := h.store.FindResellerByID(c, resellerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("RESELLER", resellerID))
			return
		}
		response.Internal(c, err)
		return
	}
	status := c.Query("status")
	if status != "" && !model.ValidCommissionStatus(status) {
		response.BadRequest(c, "status must be one of: "+strings.Join(model.CommissionStatuses, ", "))
		return
	}
	page := listPage(c)
	rows, total, err := h.store.ListCommissions(c, resellerID, status, page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "commissions", rows, total, page)
}

// AccrueCommission — POST /admin/resellers/:id/commissions
//
// Body: { order_id (required), basis_minor (required, >= 0),
// bps? (0..10000, defaults to the reseller's contract rate) }. The
// order amount is the admin's assertion of what the sale was worth and
// the rate is the contractual share; the store computes the exact
// amount (rounding down) so the ledger never trusts its writer on the
// money.
//
// Idempotent per (reseller, order): the first accrual answers 201 with
// the row it created, a repeat answers 200 with the ORIGINAL row and
// writes nothing — the retried webhook or double-clicked button can
// never double-pay. Corrections are a future edit path; the first
// writer wins.
func (h *ResellerAdminHandler) AccrueCommission(c *gin.Context) {
	resellerID := c.Param("id")
	r, err := h.store.FindResellerByID(c, resellerID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("RESELLER", resellerID))
			return
		}
		response.Internal(c, err)
		return
	}

	var req struct {
		OrderID    string `json:"order_id"`
		BasisMinor *int64 `json:"basis_minor"`
		BPS        *int   `json:"bps"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	orderID := strings.TrimSpace(req.OrderID)
	if orderID == "" {
		response.BadRequest(c, "order_id is required")
		return
	}
	if req.BasisMinor == nil {
		response.BadRequest(c, "basis_minor is required")
		return
	}
	if *req.BasisMinor < 0 {
		response.BadRequest(c, "basis_minor must be >= 0")
		return
	}
	// bps defaults to the reseller's contract rate when the body leaves
	// it out; a stated rate is bounded to a real percentage. Integer
	// basis points only — money discipline.
	bps := r.CommissionBPS
	if req.BPS != nil {
		if !model.ValidCommissionBPS(*req.BPS) {
			response.BadRequest(c, "bps must be between 0 and 10000")
			return
		}
		bps = *req.BPS
	}

	row, created, err := h.store.AccrueCommission(c, &model.Commission{
		ResellerID: resellerID,
		OrderID:    orderID,
		BasisMinor: *req.BasisMinor,
		BPS:        bps,
	})
	if err != nil {
		response.Internal(c, err)
		return
	}
	if !created {
		// A replay is not a new event: the ledger did not change, so
		// there is nothing to audit. Answer the original row.
		response.OK(c, row)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "commission", EntityID: row.ID, Action: "accrued",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.Created(c, row)
}

// MarkCommissionPaid — POST /admin/resellers/:id/commissions/:commission_id/paid
//
// Body (optional): { paid_at? } — the disbursement date (RFC 3339,
// backdating a payout run is legitimate); absent means now. The
// commission must belong to the :id reseller: a commission under
// another partner answers exactly like a missing one (404), so this
// is never an existence oracle. A cancelled commission is refused
// (409) — cancelled is a closed state; paying one would undo a
// deliberate void.
func (h *ResellerAdminHandler) MarkCommissionPaid(c *gin.Context) {
	resellerID := c.Param("id")
	commissionID := strings.TrimSpace(c.Param("commission_id"))
	if commissionID == "" {
		response.BadRequest(c, "commission_id is required in the URL path")
		return
	}
	if _, err := h.store.FindResellerByID(c, resellerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("RESELLER", resellerID))
			return
		}
		response.Internal(c, err)
		return
	}
	cm, err := h.store.FindCommissionByID(c, commissionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("COMMISSION", commissionID))
			return
		}
		response.Internal(c, err)
		return
	}
	// Ownership: the route's reseller must own the row. "Not yours" is
	// deliberately the same 404 as "does not exist".
	if cm.ResellerID != resellerID {
		writeAppErr(c, apperr.NotFound("COMMISSION", commissionID))
		return
	}

	// The body is optional (no body = "paid now"); a malformed one is
	// still a 400.
	var req struct {
		PaidAt *time.Time `json:"paid_at"`
	}
	if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
		response.BadRequest(c, "invalid request")
		return
	}
	paidAt := time.Now()
	if req.PaidAt != nil {
		paidAt = *req.PaidAt
	}

	row, err := h.store.MarkCommissionPaid(c, commissionID, paidAt)
	if err != nil {
		if errors.Is(err, store.ErrCommissionCancelled) {
			response.Err(c, 409, "COMMISSION_CANCELLED", "a cancelled commission cannot be marked paid")
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "commission", EntityID: commissionID, Action: "paid",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.OK(c, row)
}

// ─── Wholesale price overrides ───
//
// What this partner pays per plan instead of the public (Stripe)
// price. The currency is the client's ISO 4217 code — only its shape
// is validated offline (three uppercase letters); whether it matches
// the plan's Stripe price is deliberately not decided here.

// ListPriceOverrides — GET /admin/resellers/:id/prices
//
// The reseller's whole wholesale price list (it is bounded by the
// plans a partner sells, so the listing is unpaged). A missing account
// is a 404, not an empty list.
func (h *ResellerAdminHandler) ListPriceOverrides(c *gin.Context) {
	resellerID := c.Param("id")
	if _, err := h.store.FindResellerByID(c, resellerID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("RESELLER", resellerID))
			return
		}
		response.Internal(c, err)
		return
	}
	rows, err := h.store.ListResellerPriceOverrides(c, resellerID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "prices", rows, len(rows), listPage(c))
}

// SetPriceOverride — PUT /admin/resellers/:id/prices/:plan_id
//
// Body: { unit_amount_minor (required, >= 0), currency (required,
// three uppercase letters) }. PUT semantics: the (reseller, plan) pair
// is the whole identity, so a repeat writes over the old deal in
// place. The plan must exist (a price on a dead plan is a 404 the
// admin can fix) and zero is a legal wholesale price (a comped
// partner); negative is refused.
func (h *ResellerAdminHandler) SetPriceOverride(c *gin.Context) {
	resellerID := c.Param("id")
	planID := strings.TrimSpace(c.Param("plan_id"))
	if planID == "" {
		response.BadRequest(c, "plan_id is required in the URL path")
		return
	}
	var req struct {
		UnitAmountMinor *int64 `json:"unit_amount_minor"`
		Currency        string `json:"currency"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	if req.UnitAmountMinor == nil {
		response.BadRequest(c, "unit_amount_minor is required")
		return
	}
	if *req.UnitAmountMinor < 0 {
		response.BadRequest(c, "unit_amount_minor must be >= 0")
		return
	}
	if !model.ValidCurrencyCode(req.Currency) {
		response.BadRequest(c, "currency must be 3 uppercase letters (ISO 4217)")
		return
	}
	o := &model.ResellerPriceOverride{
		ResellerID:      resellerID,
		PlanID:          planID,
		UnitAmountMinor: *req.UnitAmountMinor,
		Currency:        req.Currency,
	}
	if err := h.store.SetResellerPriceOverride(c, o); err != nil {
		if errors.Is(err, store.ErrPriceOverridePlanNotFound) {
			writeAppErr(c, apperr.NotFound("PLAN", planID))
			return
		}
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("RESELLER", resellerID))
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "reseller_price", EntityID: planID, Action: "set",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"reseller_id": resellerID},
	})
	// Answer the stored row (with its timestamps), not the request.
	stored, err := h.store.FindResellerPriceOverride(c, resellerID, planID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, stored)
}

// DeletePriceOverride — DELETE /admin/resellers/:id/prices/:plan_id
//
// Scoped by the (reseller, plan) pair, so a delete can never reach
// another partner's price. A pair with no override is a 404, not a
// 204 for nothing.
func (h *ResellerAdminHandler) DeletePriceOverride(c *gin.Context) {
	resellerID := c.Param("id")
	planID := strings.TrimSpace(c.Param("plan_id"))
	if planID == "" {
		response.BadRequest(c, "plan_id is required in the URL path")
		return
	}
	if err := h.store.DeleteResellerPriceOverride(c, resellerID, planID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("PRICE_OVERRIDE", planID))
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "reseller_price", EntityID: planID, Action: "deleted",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"reseller_id": resellerID},
	})
	response.NoContent(c)
}
