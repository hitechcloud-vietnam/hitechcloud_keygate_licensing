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

// ─── Affiliates ───
//
// Admin surface for the affiliate program (plan §32): affiliate
// accounts and their commission terms, referral codes, the review of
// conversions and the payouts that settle them.
//
//	GET    /admin/affiliates                       List (search, status, paging)
//	POST   /admin/affiliates                       Create
//	GET    /admin/affiliates/:id                   Get one (with accrued commission)
//	PATCH  /admin/affiliates/:id                   Update (partial)
//	DELETE /admin/affiliates/:id                   Delete (refuses while payouts/conversions exist)
//	GET    /admin/affiliates/:id/codes             List referral codes
//	POST   /admin/affiliates/:id/codes             Create a referral code
//	PATCH  /admin/affiliates/:id/codes/:code_id    Update a code (landing_url, active)
//	DELETE /admin/affiliates/:id/codes/:code_id    Delete a code (refuses while conversions exist)
//	GET    /admin/affiliates/:id/conversions       List conversions (status filter, paging)
//	POST   /admin/conversions/:id/approve          Approve a conversion
//	POST   /admin/conversions/:id/reject           Reject a conversion
//	POST   /admin/conversions/:id/reverse          Reverse a conversion (clawback)
//	GET    /admin/affiliates/:id/payouts           List payouts
//	POST   /admin/affiliates/:id/payouts           Create a payout (claims conversions)
//	POST   /admin/payouts/:id/paid                 Mark a payout paid
//	POST   /admin/payouts/:id/failed               Mark a payout failed (releases its claims)
//
// The PATCH code route is not in the original sketch; it exists
// because the active flag needs a way to change without deleting the
// row (deactivation is the fraud response that keeps history).
//
// affiliateAdminStore is the slice of store.Store this handler needs.
// The real constructor takes *store.Store; tests substitute a fake so
// the refusal paths — duplicate email, delete with money records, the
// conversion and payout state machines — run without a database.
type affiliateAdminStore interface {
	ListAffiliates(ctx context.Context, search, status string, p store.Page) ([]*model.Affiliate, int, error)
	FindAffiliateByID(ctx context.Context, id string) (*model.Affiliate, error)
	CreateAffiliate(ctx context.Context, a *model.Affiliate) error
	UpdateAffiliate(ctx context.Context, a *model.Affiliate) error
	DeleteAffiliate(ctx context.Context, id string) error
	CreateReferralCode(ctx context.Context, rc *model.ReferralCode) error
	FindReferralCodeByID(ctx context.Context, id string) (*model.ReferralCode, error)
	ListReferralCodes(ctx context.Context, affiliateID string, p store.Page) ([]*model.ReferralCode, int, error)
	UpdateReferralCode(ctx context.Context, rc *model.ReferralCode) error
	DeleteReferralCode(ctx context.Context, affiliateID, codeID string) error
	ListConversions(ctx context.Context, affiliateID, status string, p store.Page) ([]*model.AffiliateConversion, int, error)
	SetConversionStatus(ctx context.Context, id, status string) (*model.AffiliateConversion, error)
	SumPendingCommissions(ctx context.Context, affiliateID string) (int64, error)
	CreatePayout(ctx context.Context, pay *model.AffiliatePayout) error
	MarkPayoutPaid(ctx context.Context, id string) (*model.AffiliatePayout, error)
	MarkPayoutFailed(ctx context.Context, id, notes string) (*model.AffiliatePayout, error)
	ListAffiliatePayouts(ctx context.Context, affiliateID string, p store.Page) ([]*model.AffiliatePayout, int, error)
	Audit(ctx context.Context, log *model.AuditLog)
}

var _ affiliateAdminStore = (*store.Store)(nil)

type AffiliateAdminHandler struct {
	store affiliateAdminStore
}

func NewAffiliateAdminHandler(s *store.Store) *AffiliateAdminHandler {
	return &AffiliateAdminHandler{store: s}
}

// prepareAffiliate folds a row into its stored form — trimmed name, a
// folded contact address, defaulted and validated status and
// commission model, a checked commission pair — and refuses what an
// affiliate cannot carry: an empty name, an unusable email, a status
// or model outside the vocabulary, a rate outside 0–10000 bps, a
// negative fixed amount. Create and update both run it, so a value
// refused at creation cannot be put on the same affiliate a moment
// later. It returns apperr so writeAppErr writes it.
func prepareAffiliate(a *model.Affiliate) *apperr.AppError {
	a.Name = strings.TrimSpace(a.Name)
	if err := apperr.ValidateName("name", a.Name); err != nil {
		return err
	}

	// The contact address is stored folded so one address cannot exist
	// under several spellings — the unique index and
	// FindAffiliateByEmail both rely on it. Validate the folded value:
	// it is what gets stored.
	a.ContactEmail = model.NormalizeAffiliateEmail(a.ContactEmail)
	if err := apperr.ValidateEmail(a.ContactEmail); err != nil {
		return err
	}

	// Status is a closed vocabulary (active|suspended). Empty defaults
	// to active; anything outside the two is refused rather than
	// silently stored, because status gates conversion recording.
	if a.Status == "" {
		a.Status = model.AffiliateStatusActive
	} else if !model.ValidAffiliateStatus(a.Status) {
		return apperr.BadRequest("status must be one of: " + strings.Join(model.AffiliateStatuses, ", "))
	}

	// Commission model is a closed vocabulary (percent|fixed) and
	// selects which of the two commission fields pays.
	if a.CommissionModel == "" {
		a.CommissionModel = model.AffiliateCommissionModelPercent
	} else if !model.ValidAffiliateCommissionModel(a.CommissionModel) {
		return apperr.BadRequest("commission_model must be one of: " + strings.Join(model.AffiliateCommissionModels, ", "))
	}

	// Money discipline: the rate is integer bps bounded to a real
	// percentage; the fixed amount is integer minor units and never
	// negative. Both are written whatever the model, so switching the
	// model later does not lose either number.
	if a.CommissionBPS < 0 || a.CommissionBPS > 10000 {
		return apperr.BadRequest("commission_bps must be between 0 and 10000")
	}
	if a.CommissionMinor < 0 {
		return apperr.BadRequest("commission_minor must not be negative")
	}

	a.PayoutMethod = strings.TrimSpace(a.PayoutMethod)
	a.Notes = strings.TrimSpace(a.Notes)
	return nil
}

// prepareReferralCode folds a code row into its stored form — the
// handle through model.NormalizeReferralCode, the landing URL
// trimmed — and refuses what a code cannot carry: a handle that is not
// 4..32 uppercase alphanumerics after folding, or a landing URL that
// is not a full http(s) URL. The URL check is half of the open-redirect
// defence: the redirect endpoint only ever navigates to this stored
// value, so the value must have been a web URL since the moment it was
// written (a javascript: or data: URL can never be stored).
func prepareReferralCode(rc *model.ReferralCode) *apperr.AppError {
	rc.Code = model.NormalizeReferralCode(rc.Code)
	if !model.ValidReferralCode(rc.Code) {
		return apperr.BadRequest("code must be 4 to 32 letters and digits")
	}
	rc.LandingURL = strings.TrimSpace(rc.LandingURL)
	if rc.LandingURL != "" {
		if err := apperr.ValidateHTTPURL("landing_url", rc.LandingURL); err != nil {
			return err
		}
	}
	return nil
}

// List — GET /admin/affiliates
//
// Query: search (name/email), status (active|suspended), limit/offset.
// An unrecognised status filter is refused rather than silently
// returning nothing, so a typo is visible instead of an empty page.
func (h *AffiliateAdminHandler) List(c *gin.Context) {
	page := listPage(c)
	status := c.Query("status")
	if status != "" && !model.ValidAffiliateStatus(status) {
		response.BadRequest(c, "status must be one of: "+strings.Join(model.AffiliateStatuses, ", "))
		return
	}
	affiliates, total, err := h.store.ListAffiliates(c, c.Query("search"), status, page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "affiliates", affiliates, total, page)
}

// Get — GET /admin/affiliates/:id
//
// Answers the account beside its accrued (unpaid, unclaimed)
// commission, so the detail view can show the balance a payout would
// settle without a second call.
func (h *AffiliateAdminHandler) Get(c *gin.Context) {
	id := c.Param("id")
	a, err := h.store.FindAffiliateByID(c, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("AFFILIATE", id))
			return
		}
		response.Internal(c, err)
		return
	}
	accrued, err := h.store.SumPendingCommissions(c, id)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, gin.H{"affiliate": a, "pending_commission_minor": accrued})
}

// Create — POST /admin/affiliates
//
// Body: { name, contact_email, status?, commission_model?,
// commission_bps?, commission_minor?, payout_method?, notes? }.
// Missing status/model default to active/percent. A contact_email
// already claimed by another affiliate is a 409 the admin can act on.
func (h *AffiliateAdminHandler) Create(c *gin.Context) {
	var req struct {
		Name            string `json:"name" binding:"required"`
		ContactEmail    string `json:"contact_email" binding:"required"`
		Status          string `json:"status"`
		CommissionModel string `json:"commission_model"`
		CommissionBPS   int    `json:"commission_bps"`
		CommissionMinor int64  `json:"commission_minor"`
		PayoutMethod    string `json:"payout_method"`
		Notes           string `json:"notes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "name and contact_email are required")
		return
	}

	a := &model.Affiliate{
		Name:            req.Name,
		ContactEmail:    req.ContactEmail,
		Status:          req.Status,
		CommissionModel: req.CommissionModel,
		CommissionBPS:   req.CommissionBPS,
		CommissionMinor: req.CommissionMinor,
		PayoutMethod:    req.PayoutMethod,
		Notes:           req.Notes,
	}
	if verr := prepareAffiliate(a); verr != nil {
		writeAppErr(c, verr)
		return
	}
	if err := h.store.CreateAffiliate(c, a); err != nil {
		if store.IsAffiliateEmailConflict(err) {
			response.Err(c, 409, "DUPLICATE", "affiliate contact email already exists")
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "affiliate", EntityID: a.ID, Action: "created",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.Created(c, a)
}

// Update — PATCH /admin/affiliates/:id
//
// Body: any of { name, contact_email, status, commission_model,
// commission_bps, commission_minor, payout_method, notes }. A field the
// request leaves out keeps its stored value; a field it names is
// re-validated like a create, so the two paths cannot drift apart on
// what a legal affiliate is.
func (h *AffiliateAdminHandler) Update(c *gin.Context) {
	id := c.Param("id")
	a, err := h.store.FindAffiliateByID(c, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("AFFILIATE", id))
			return
		}
		response.Internal(c, err)
		return
	}

	var req struct {
		Name            *string `json:"name"`
		ContactEmail    *string `json:"contact_email"`
		Status          *string `json:"status"`
		CommissionModel *string `json:"commission_model"`
		CommissionBPS   *int    `json:"commission_bps"`
		CommissionMinor *int64  `json:"commission_minor"`
		PayoutMethod    *string `json:"payout_method"`
		Notes           *string `json:"notes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	if req.Name != nil {
		a.Name = *req.Name
	}
	if req.ContactEmail != nil {
		a.ContactEmail = *req.ContactEmail
	}
	if req.Status != nil {
		a.Status = *req.Status
	}
	if req.CommissionModel != nil {
		a.CommissionModel = *req.CommissionModel
	}
	if req.CommissionBPS != nil {
		a.CommissionBPS = *req.CommissionBPS
	}
	if req.CommissionMinor != nil {
		a.CommissionMinor = *req.CommissionMinor
	}
	if req.PayoutMethod != nil {
		a.PayoutMethod = *req.PayoutMethod
	}
	if req.Notes != nil {
		a.Notes = *req.Notes
	}
	if verr := prepareAffiliate(a); verr != nil {
		writeAppErr(c, verr)
		return
	}
	if err := h.store.UpdateAffiliate(c, a); err != nil {
		if store.IsAffiliateEmailConflict(err) {
			response.Err(c, 409, "DUPLICATE", "affiliate contact email already exists")
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "affiliate", EntityID: a.ID, Action: "updated",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.OK(c, a)
}

// Delete — DELETE /admin/affiliates/:id
//
// The documented delete policy: an affiliate with payouts or
// conversions is a money record and refuses with 409 (suspend the
// account instead — that stops it converting while keeping every
// record). Codes and clicks cascade with the account.
func (h *AffiliateAdminHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	if err := h.store.DeleteAffiliate(c, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("AFFILIATE", id))
			return
		}
		if errors.Is(err, store.ErrAffiliateHasConversions) {
			response.Err(c, 409, "AFFILIATE_HAS_CONVERSIONS",
				"affiliate has conversion records; suspend it instead of deleting")
			return
		}
		if errors.Is(err, store.ErrAffiliateHasPayouts) {
			response.Err(c, 409, "AFFILIATE_HAS_PAYOUTS",
				"affiliate has payout records; suspend it instead of deleting")
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "affiliate", EntityID: id, Action: "deleted",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.NoContent(c)
}

// ListCodes — GET /admin/affiliates/:id/codes
func (h *AffiliateAdminHandler) ListCodes(c *gin.Context) {
	affiliateID := c.Param("id")
	if _, err := h.store.FindAffiliateByID(c, affiliateID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("AFFILIATE", affiliateID))
			return
		}
		response.Internal(c, err)
		return
	}
	page := listPage(c)
	codes, total, err := h.store.ListReferralCodes(c, affiliateID, page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "codes", codes, total, page)
}

// CreateCode — POST /admin/affiliates/:id/codes
//
// Body: { code, landing_url?, active? }. The code is folded to its
// stored form (uppercase alphanumerics) and a duplicate of an existing
// handle is 409. active is on unless the request turns it off — a
// pointer, because a JSON false is deliberate. landing_url is
// validated as a full http(s) URL at write time (the open-redirect
// defence: the redirect endpoint navigates only to this stored value).
func (h *AffiliateAdminHandler) CreateCode(c *gin.Context) {
	affiliateID := c.Param("id")
	if _, err := h.store.FindAffiliateByID(c, affiliateID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("AFFILIATE", affiliateID))
			return
		}
		response.Internal(c, err)
		return
	}

	var req struct {
		Code       string `json:"code" binding:"required"`
		LandingURL string `json:"landing_url"`
		Active     *bool  `json:"active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "code is required")
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	rc := &model.ReferralCode{
		AffiliateID: affiliateID,
		Code:        req.Code,
		LandingURL:  req.LandingURL,
		Active:      active,
	}
	if verr := prepareReferralCode(rc); verr != nil {
		writeAppErr(c, verr)
		return
	}
	if err := h.store.CreateReferralCode(c, rc); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("AFFILIATE", affiliateID))
			return
		}
		if store.IsReferralCodeConflict(err) {
			response.Err(c, 409, "DUPLICATE", "referral code already exists")
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "affiliate", EntityID: affiliateID, Action: "code_created",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"code_id": rc.ID, "code": rc.Code},
	})
	response.Created(c, rc)
}

// UpdateCode — PATCH /admin/affiliates/:id/codes/:code_id
//
// Body: { landing_url?, active? }. The handle and the owning affiliate
// are immutable (renaming a code orphans every link already shared);
// this changes only where the code lands and whether it still
// converts. Deactivating is the fraud response that keeps history.
func (h *AffiliateAdminHandler) UpdateCode(c *gin.Context) {
	affiliateID, codeID := c.Param("id"), c.Param("code_id")
	rc, err := h.store.FindReferralCodeByID(c, codeID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("REFERRAL_CODE", codeID))
			return
		}
		response.Internal(c, err)
		return
	}
	// Route scoping: the code must belong to the affiliate in the path.
	if rc.AffiliateID != affiliateID {
		writeAppErr(c, apperr.NotFound("REFERRAL_CODE", codeID))
		return
	}

	var req struct {
		LandingURL *string `json:"landing_url"`
		Active     *bool   `json:"active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	if req.LandingURL != nil {
		rc.LandingURL = *req.LandingURL
	}
	if req.Active != nil {
		rc.Active = *req.Active
	}
	if verr := prepareReferralCode(rc); verr != nil {
		writeAppErr(c, verr)
		return
	}
	if err := h.store.UpdateReferralCode(c, rc); err != nil {
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "affiliate", EntityID: affiliateID, Action: "code_updated",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"code_id": rc.ID, "code": rc.Code, "active": rc.Active},
	})
	response.OK(c, rc)
}

// DeleteCode — DELETE /admin/affiliates/:id/codes/:code_id
//
// Refuses with 409 while conversions point at the code — those are
// money records whose handle must stay readable; deactivate instead.
// Clicks cascade. Both ids come from the route, so an admin cannot
// delete another affiliate's code through a mismatched path.
func (h *AffiliateAdminHandler) DeleteCode(c *gin.Context) {
	affiliateID, codeID := c.Param("id"), c.Param("code_id")
	if err := h.store.DeleteReferralCode(c, affiliateID, codeID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("REFERRAL_CODE", codeID))
			return
		}
		if errors.Is(err, store.ErrReferralCodeHasConversions) {
			response.Err(c, 409, "REFERRAL_CODE_HAS_CONVERSIONS",
				"referral code has conversion records; deactivate it instead of deleting")
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "affiliate", EntityID: affiliateID, Action: "code_deleted",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"code_id": codeID},
	})
	response.NoContent(c)
}

// ListConversions — GET /admin/affiliates/:id/conversions
//
// Query: status (pending|approved|paid|rejected|reversed), limit/
// offset. An unrecognised status filter is refused rather than
// silently returning nothing.
func (h *AffiliateAdminHandler) ListConversions(c *gin.Context) {
	affiliateID := c.Param("id")
	if _, err := h.store.FindAffiliateByID(c, affiliateID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("AFFILIATE", affiliateID))
			return
		}
		response.Internal(c, err)
		return
	}
	status := c.Query("status")
	if status != "" && !model.ValidAffiliateConversionStatus(status) {
		response.BadRequest(c, "status must be one of: "+strings.Join(model.AffiliateConversionStatuses, ", "))
		return
	}
	page := listPage(c)
	convs, total, err := h.store.ListConversions(c, affiliateID, status, page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "conversions", convs, total, page)
}

// Approve / Reject / Reverse are the three review decisions, one
// route each — POST /admin/conversions/:id/approve|reject|reverse.
// All three run the same state machine in the store (see
// model.ConversionTransitionOK): a move the matrix does not allow is
// 409 CONVERSION_TRANSITION_INVALID, and a conversion a still-requested
// payout has claimed is frozen (409 CONVERSION_IN_PAYOUT — fail that
// payout first). The paid state is never settable here: a payout's
// settlement applies it.

// Approve — POST /admin/conversions/:id/approve
func (h *AffiliateAdminHandler) Approve(c *gin.Context) {
	h.moveConversion(c, model.AffiliateConversionStatusApproved, "approved")
}

// Reject — POST /admin/conversions/:id/reject
func (h *AffiliateAdminHandler) Reject(c *gin.Context) {
	h.moveConversion(c, model.AffiliateConversionStatusRejected, "rejected")
}

// Reverse — POST /admin/conversions/:id/reverse
func (h *AffiliateAdminHandler) Reverse(c *gin.Context) {
	h.moveConversion(c, model.AffiliateConversionStatusReversed, "reversed")
}

func (h *AffiliateAdminHandler) moveConversion(c *gin.Context, to, action string) {
	id := c.Param("id")
	conv, err := h.store.SetConversionStatus(c, id, to)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("CONVERSION", id))
			return
		}
		if errors.Is(err, store.ErrConversionInvalidTransition) {
			response.Err(c, 409, "CONVERSION_TRANSITION_INVALID",
				"conversion cannot move to "+to+" from its current status")
			return
		}
		if errors.Is(err, store.ErrConversionInPayout) {
			response.Err(c, 409, "CONVERSION_IN_PAYOUT",
				"conversion is claimed by a pending payout; fail that payout first")
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "affiliate_conversion", EntityID: conv.ID, Action: action,
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"status": conv.Status},
	})
	response.OK(c, conv)
}

// ListPayouts — GET /admin/affiliates/:id/payouts
func (h *AffiliateAdminHandler) ListPayouts(c *gin.Context) {
	affiliateID := c.Param("id")
	if _, err := h.store.FindAffiliateByID(c, affiliateID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("AFFILIATE", affiliateID))
			return
		}
		response.Internal(c, err)
		return
	}
	page := listPage(c)
	payouts, total, err := h.store.ListAffiliatePayouts(c, affiliateID, page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "payouts", payouts, total, page)
}

// CreatePayout — POST /admin/affiliates/:id/payouts
//
// Body: { amount_minor?, notes? }. amount_minor is an upper bound on
// what to settle — omitted or 0 means the whole accrued balance. The
// store settles whole conversions oldest-first up to that bound and
// records the settled sum (which may be a little less than asked — a
// conversion is never split), so an amount above the accrued balance
// is refused 400 before that, and nothing accrued is 409
// PAYOUT_NOTHING_TO_PAY. The response carries the payout as recorded.
func (h *AffiliateAdminHandler) CreatePayout(c *gin.Context) {
	affiliateID := c.Param("id")
	if _, err := h.store.FindAffiliateByID(c, affiliateID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("AFFILIATE", affiliateID))
			return
		}
		response.Internal(c, err)
		return
	}

	var req struct {
		AmountMinor int64  `json:"amount_minor"`
		Notes       string `json:"notes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	if req.AmountMinor < 0 {
		response.BadRequest(c, "amount_minor must not be negative")
		return
	}
	accrued, err := h.store.SumPendingCommissions(c, affiliateID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	if req.AmountMinor > accrued {
		response.BadRequest(c, "amount_minor exceeds the accrued commission balance")
		return
	}

	pay := &model.AffiliatePayout{
		AffiliateID: affiliateID,
		AmountMinor: req.AmountMinor,
		Notes:       strings.TrimSpace(req.Notes),
	}
	if err := h.store.CreatePayout(c, pay); err != nil {
		if errors.Is(err, store.ErrPayoutNothingToPay) {
			response.Err(c, 409, "PAYOUT_NOTHING_TO_PAY",
				"no accrued commission to pay out")
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "affiliate_payout", EntityID: pay.ID, Action: "created",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"affiliate_id": affiliateID, "amount_minor": pay.AmountMinor},
	})
	response.Created(c, pay)
}

// MarkPaid — POST /admin/payouts/:id/paid
//
// The money has moved: the payout stamps paid_at and its claimed
// conversions become paid with it. Only a requested payout can pay —
// 409 PAYOUT_TRANSITION_INVALID otherwise (what happened to the money
// is recorded once and never rewritten).
func (h *AffiliateAdminHandler) MarkPaid(c *gin.Context) {
	h.finishPayout(c, model.AffiliatePayoutStatusPaid, "paid", "")
}

// MarkFailed — POST /admin/payouts/:id/failed
//
// Body: { notes? }. The transfer did not happen: the payout becomes
// failed and the conversions it claimed return to the accrued pool for
// a later payout. Only a requested payout can fail.
func (h *AffiliateAdminHandler) MarkFailed(c *gin.Context) {
	var req struct {
		Notes string `json:"notes"`
	}
	// A body is optional here; an empty one is a valid failure record.
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			response.BadRequest(c, "invalid request")
			return
		}
	}
	h.finishPayout(c, model.AffiliatePayoutStatusFailed, "failed", strings.TrimSpace(req.Notes))
}

func (h *AffiliateAdminHandler) finishPayout(c *gin.Context, to, action, notes string) {
	id := c.Param("id")
	var (
		pay *model.AffiliatePayout
		err error
	)
	if to == model.AffiliatePayoutStatusPaid {
		pay, err = h.store.MarkPayoutPaid(c, id)
	} else {
		pay, err = h.store.MarkPayoutFailed(c, id, notes)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("PAYOUT", id))
			return
		}
		if errors.Is(err, store.ErrPayoutInvalidTransition) {
			response.Err(c, 409, "PAYOUT_TRANSITION_INVALID",
				"only a requested payout can be marked "+action)
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c, &model.AuditLog{
		Entity: "affiliate_payout", EntityID: pay.ID, Action: action,
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"status": pay.Status},
	})
	response.OK(c, pay)
}
