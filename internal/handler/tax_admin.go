package handler

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/tax"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// TaxAdminHandler exposes admin CRUD for the tax rate table — the
// persistent half of internal/tax: the engine computes, this stores
// which rates are in force.
//
//	GET    /admin/tax-rates            List (search, paging)
//	POST   /admin/tax-rates            Create
//	GET    /admin/tax-rates/:id        Get one
//	PATCH  /admin/tax-rates/:id        Update (partial)
//	DELETE /admin/tax-rates/:id        Delete
type TaxAdminHandler struct {
	Store *store.Store
}

func NewTaxAdminHandler(s *store.Store) *TaxAdminHandler {
	return &TaxAdminHandler{Store: s}
}

// maxTaxBasisPoints caps a stored rate at 1000%. The engine accepts any
// non-negative rate; the cap is this table's guard against the typo
// that charges ten times over — 100000 where 10000 was meant.
const maxTaxBasisPoints int64 = 100_000

// normalizeTaxRateJurisdiction trims and upper-cases a jurisdiction
// label (" us-ca " → "US-CA") and refuses an empty or over-long one.
// The stored form is canonical so the unique index and the checkout
// lookup see one spelling of a jurisdiction, not several.
func normalizeTaxRateJurisdiction(raw string) (string, *apperr.AppError) {
	j := strings.ToUpper(strings.TrimSpace(raw))
	if err := apperr.ValidateName("jurisdiction", j); err != nil {
		return "", err
	}
	if len(j) > 64 {
		return "", apperr.BadRequest("jurisdiction must be at most 64 characters")
	}
	return j, nil
}

// validateTaxRateBasisPoints refuses a rate the engine would not carry
// (negative, via tax.Validate — the engine is the one place that knows
// what a valid rate is) and the over-large typo above.
func validateTaxRateBasisPoints(bps int64) *apperr.AppError {
	if err := tax.Validate(tax.Rate{BasisPoints: bps}); err != nil {
		return apperr.BadRequest("basis_points must not be negative")
	}
	if bps > maxTaxBasisPoints {
		return apperr.BadRequest("basis_points must be at most 100000 (1000%)")
	}
	return nil
}

// prepareTaxRate validates a row and normalizes it in place, so both
// write paths — and only those two — ask the same questions of the
// same fields. The create path could not put a value in that the
// update path would then refuse (or silently keep un-normalized).
func prepareTaxRate(r *model.TaxRate) *apperr.AppError {
	j, err := normalizeTaxRateJurisdiction(r.Jurisdiction)
	if err != nil {
		return err
	}
	r.Jurisdiction = j
	r.Country = strings.TrimSpace(r.Country)
	r.Region = strings.TrimSpace(r.Region)
	return validateTaxRateBasisPoints(r.BasisPoints)
}

// List — GET /admin/tax-rates
func (h *TaxAdminHandler) List(c *gin.Context) {
	page := listPage(c)
	rates, total, err := h.Store.ListTaxRates(c, c.Query("search"), page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "tax_rates", rates, total, page)
}

// Get — GET /admin/tax-rates/:id
func (h *TaxAdminHandler) Get(c *gin.Context) {
	id := c.Param("id")
	r, err := h.Store.FindTaxRateByID(c, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("TAX_RATE", id))
			return
		}
		response.Internal(c, err)
		return
	}
	response.OK(c, r)
}

// Create — POST /admin/tax-rates
//
// Body: { jurisdiction, basis_points?, inclusive?, country?, region?, description? }
func (h *TaxAdminHandler) Create(c *gin.Context) {
	var req struct {
		Jurisdiction string `json:"jurisdiction" binding:"required"`
		BasisPoints  int64  `json:"basis_points"`
		Inclusive    bool   `json:"inclusive"`
		Country      string `json:"country"`
		Region       string `json:"region"`
		Description  string `json:"description"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "jurisdiction is required")
		return
	}

	r := &model.TaxRate{
		Jurisdiction: req.Jurisdiction,
		BasisPoints:  req.BasisPoints,
		Inclusive:    req.Inclusive,
		Country:      req.Country,
		Region:       req.Region,
		Description:  req.Description,
		Active:       true,
	}
	if verr := prepareTaxRate(r); verr != nil {
		writeAppErr(c, verr)
		return
	}
	if err := h.Store.CreateTaxRate(c, r); err != nil {
		if store.IsTaxRateJurisdictionConflict(err) {
			response.Err(c, 409, "DUPLICATE", "a tax rate for this jurisdiction already exists")
			return
		}
		response.Internal(c, err)
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "tax_rate", EntityID: r.ID, Action: "created",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.Created(c, r)
}

// Update — PATCH /admin/tax-rates/:id
//
// Body: any of { jurisdiction, basis_points, inclusive, country, region, description, active }.
// A field the request leaves out keeps its stored value; a field it
// names is re-validated like a create, so the two paths cannot drift
// apart on what a legal rate is.
func (h *TaxAdminHandler) Update(c *gin.Context) {
	id := c.Param("id")
	r, err := h.Store.FindTaxRateByID(c, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("TAX_RATE", id))
			return
		}
		response.Internal(c, err)
		return
	}

	var req struct {
		Jurisdiction *string `json:"jurisdiction"`
		BasisPoints  *int64  `json:"basis_points"`
		Inclusive    *bool   `json:"inclusive"`
		Country      *string `json:"country"`
		Region       *string `json:"region"`
		Description  *string `json:"description"`
		Active       *bool   `json:"active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	if req.Jurisdiction != nil {
		r.Jurisdiction = *req.Jurisdiction
	}
	if req.BasisPoints != nil {
		r.BasisPoints = *req.BasisPoints
	}
	if req.Inclusive != nil {
		r.Inclusive = *req.Inclusive
	}
	if req.Country != nil {
		r.Country = *req.Country
	}
	if req.Region != nil {
		r.Region = *req.Region
	}
	if req.Description != nil {
		r.Description = *req.Description
	}
	if req.Active != nil {
		r.Active = *req.Active
	}
	if verr := prepareTaxRate(r); verr != nil {
		writeAppErr(c, verr)
		return
	}
	if err := h.Store.UpdateTaxRate(c, r); err != nil {
		if store.IsTaxRateJurisdictionConflict(err) {
			response.Err(c, 409, "DUPLICATE", "a tax rate for this jurisdiction already exists")
			return
		}
		response.Internal(c, err)
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "tax_rate", EntityID: r.ID, Action: "updated",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.OK(c, r)
}

// Delete — DELETE /admin/tax-rates/:id
func (h *TaxAdminHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	if err := h.Store.DeleteTaxRate(c, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("TAX_RATE", id))
			return
		}
		response.Internal(c, err)
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "tax_rate", EntityID: id, Action: "deleted",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.NoContent(c)
}
