package handler

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/coupon"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// ─── Coupons ───
//
// Admin CRUD for discount coupons. The rows are the persisted half of
// the pure engine in internal/coupon: everything here is about
// storing a definition the engine will later be asked to redeem, and
// both create and update go through the same normalizer so the two
// paths cannot drift apart.

type CouponAdminHandler struct {
	Store *store.Store
}

func NewCouponAdminHandler(s *store.Store) *CouponAdminHandler {
	return &CouponAdminHandler{Store: s}
}

// couponSortColumns is what ?sort= accepts on the promotion-code
// list: the columns the table shows. The expressions are qualified
// with the bun model alias ("coupon"); "expires_at" is the ends_at
// column — the moment the code stops redeeming — and "redemptions"
// is the counter. Unknown keys keep the default ordering, newest
// first (listSortOrDefault).
var couponSortColumns = map[string]sortCol{
	"created_at":  {Expr: "coupon.created_at", Desc: true},
	"code":        {Expr: "coupon.code"},
	"type":        {Expr: "coupon.type"},
	"expires_at":  {Expr: "coupon.ends_at"},
	"redemptions": {Expr: "coupon.times_redeemed", Desc: true},
	"active":      {Expr: "coupon.is_active", Desc: true},
}

func (h *CouponAdminHandler) List(c *gin.Context) {
	page := listPage(c)
	order := listSortOrDefault(c, couponSortColumns, "created_at")
	coupons, total, err := h.Store.ListCoupons(c, c.Query("search"), page, order)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "coupons", coupons, total, page)
}

func (h *CouponAdminHandler) Get(c *gin.Context) {
	cpn, err := h.Store.FindCouponByID(c, c.Param("id"))
	if err != nil {
		response.NotFound(c, "coupon not found")
		return
	}
	response.OK(c, cpn)
}

func (h *CouponAdminHandler) Create(c *gin.Context) {
	var req struct {
		Code                      string     `json:"code" binding:"required"`
		Type                      string     `json:"type" binding:"required"`
		ValueBPS                  int64      `json:"value_bps"`
		ValueMinor                int64      `json:"value_minor"`
		Currency                  string     `json:"currency"`
		StartsAt                  *time.Time `json:"starts_at"`
		EndsAt                    *time.Time `json:"ends_at"`
		MaxRedemptions            int        `json:"max_redemptions"`
		MaxRedemptionsPerCustomer int        `json:"max_redemptions_per_customer"`
		MinimumOrderMinor         int64      `json:"minimum_order_minor"`
		AppliesTo                 string     `json:"applies_to"`
		Stackable                 *bool      `json:"stackable"`
		Active                    *bool      `json:"active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "code and type are required")
		return
	}
	// Stackable and Active are on unless the request turns them off;
	// pointers, because a JSON false is deliberate and must not be
	// confused with an omitted field.
	stackable, active := true, true
	if req.Stackable != nil {
		stackable = *req.Stackable
	}
	if req.Active != nil {
		active = *req.Active
	}

	cpn := &model.Coupon{
		Code:                      req.Code,
		Type:                      req.Type,
		ValueBPS:                  req.ValueBPS,
		ValueMinor:                req.ValueMinor,
		Currency:                  req.Currency,
		StartsAt:                  req.StartsAt,
		EndsAt:                    req.EndsAt,
		MaxRedemptions:            req.MaxRedemptions,
		MaxRedemptionsPerCustomer: req.MaxRedemptionsPerCustomer,
		MinimumOrderMinor:         req.MinimumOrderMinor,
		AppliesTo:                 req.AppliesTo,
		Stackable:                 stackable,
		Active:                    active,
	}
	if err := normalizeCoupon(cpn); err != nil {
		writeAppErr(c, err)
		return
	}
	if err := h.Store.CreateCoupon(c, cpn); err != nil {
		// The code is the coupon's handle and the column is unique;
		// an insert that fails here is almost always that handle
		// taken, which the admin can act on. Anything else is not
		// their doing.
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			response.Err(c, 409, "DUPLICATE", "coupon code already exists")
			return
		}
		response.Internal(c, err)
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "coupon", EntityID: cpn.ID, Action: "created",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.Created(c, cpn)
}

func (h *CouponAdminHandler) Update(c *gin.Context) {
	cpn, err := h.Store.FindCouponByID(c, c.Param("id"))
	if err != nil {
		response.NotFound(c, "coupon not found")
		return
	}

	var req struct {
		Code                      *string    `json:"code"`
		Type                      *string    `json:"type"`
		ValueBPS                  *int64     `json:"value_bps"`
		ValueMinor                *int64     `json:"value_minor"`
		Currency                  *string    `json:"currency"`
		StartsAt                  *time.Time `json:"starts_at"`
		EndsAt                    *time.Time `json:"ends_at"`
		MaxRedemptions            *int       `json:"max_redemptions"`
		MaxRedemptionsPerCustomer *int       `json:"max_redemptions_per_customer"`
		MinimumOrderMinor         *int64     `json:"minimum_order_minor"`
		AppliesTo                 *string    `json:"applies_to"`
		Stackable                 *bool      `json:"stackable"`
		Active                    *bool      `json:"active"`
		// TimesRedeemed is deliberately absent: the counter moves on
		// redemption (store.IncrementCouponRedemptions), never by
		// hand, or a save could erase checkouts that landed while the
		// form was open.
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	if req.Code != nil {
		cpn.Code = *req.Code
	}
	if req.Type != nil {
		cpn.Type = *req.Type
	}
	if req.ValueBPS != nil {
		cpn.ValueBPS = *req.ValueBPS
	}
	if req.ValueMinor != nil {
		cpn.ValueMinor = *req.ValueMinor
	}
	if req.Currency != nil {
		cpn.Currency = *req.Currency
	}
	if req.StartsAt != nil {
		cpn.StartsAt = req.StartsAt
	}
	if req.EndsAt != nil {
		cpn.EndsAt = req.EndsAt
	}
	if req.MaxRedemptions != nil {
		cpn.MaxRedemptions = *req.MaxRedemptions
	}
	if req.MaxRedemptionsPerCustomer != nil {
		cpn.MaxRedemptionsPerCustomer = *req.MaxRedemptionsPerCustomer
	}
	if req.MinimumOrderMinor != nil {
		cpn.MinimumOrderMinor = *req.MinimumOrderMinor
	}
	if req.AppliesTo != nil {
		cpn.AppliesTo = *req.AppliesTo
	}
	if req.Stackable != nil {
		cpn.Stackable = *req.Stackable
	}
	if req.Active != nil {
		cpn.Active = *req.Active
	}
	// The create path checks all of this; an update goes through the
	// same normalizer, so a value refused at creation cannot be put
	// on the same coupon a moment later.
	if err := normalizeCoupon(cpn); err != nil {
		writeAppErr(c, err)
		return
	}
	if err := h.Store.UpdateCoupon(c, cpn); err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			response.Err(c, 409, "DUPLICATE", "coupon code already exists")
			return
		}
		response.Internal(c, err)
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "coupon", EntityID: cpn.ID, Action: "updated",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.OK(c, cpn)
}

func (h *CouponAdminHandler) Delete(c *gin.Context) {
	if err := h.Store.DeleteCoupon(c, c.Param("id")); err != nil {
		response.Internal(c, err)
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "coupon", EntityID: c.Param("id"), Action: "deleted",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.NoContent(c)
}

// normalizeCoupon folds a coupon into its stored form — code upper-
// cased and trimmed through the engine's NormalizeCode, currency
// trimmed and upper-cased — and refuses anything the engine would
// refuse to redeem: an unknown type, a value outside its type's range,
// negative limits or counters, a validity window that ends before it
// starts. Create and update both call it, so the two entry points
// answer the same request the same way. It returns errors built with
// apperr for writeAppErr to write.
func normalizeCoupon(cpn *model.Coupon) error {
	cpn.Code = coupon.NormalizeCode(cpn.Code)
	if err := apperr.ValidateName("code", cpn.Code); err != nil {
		return err
	}
	cpn.Currency = strings.ToUpper(strings.TrimSpace(cpn.Currency))

	switch cpn.Type {
	case model.CouponTypePercentOff, model.CouponTypeFixedOff:
	default:
		return apperr.BadRequest("type must be percent_off or fixed_off")
	}
	if cpn.ValueBPS < 0 {
		return apperr.BadRequest("value_bps must not be negative")
	}
	if cpn.ValueMinor < 0 {
		return apperr.BadRequest("value_minor must not be negative")
	}
	if cpn.MinimumOrderMinor < 0 {
		return apperr.BadRequest("minimum_order_minor must not be negative")
	}
	if cpn.MaxRedemptions < 0 {
		return apperr.BadRequest("max_redemptions must not be negative")
	}
	if cpn.MaxRedemptionsPerCustomer < 0 {
		return apperr.BadRequest("max_redemptions_per_customer must not be negative")
	}
	if cpn.TimesRedeemed < 0 {
		return apperr.BadRequest("times_redeemed must not be negative")
	}

	switch cpn.Type {
	case model.CouponTypePercentOff:
		// Basis points: (0, 10000], so a percent coupon discounts
		// something and never more than everything.
		if cpn.ValueBPS <= 0 || cpn.ValueBPS > coupon.PercentBase {
			return apperr.BadRequest("value_bps must be between 1 and 10000 (10000 = 100%)")
		}
	case model.CouponTypeFixedOff:
		// A fixed coupon is denominated in a currency, so it cannot
		// discount an amount that was never named.
		if cpn.ValueMinor <= 0 {
			return apperr.BadRequest("value_minor must be greater than 0 for fixed_off coupons")
		}
		if cpn.Currency == "" {
			return apperr.BadRequest("currency is required for fixed_off coupons")
		}
	}
	if cpn.StartsAt != nil && cpn.EndsAt != nil && !cpn.EndsAt.After(*cpn.StartsAt) {
		return apperr.BadRequest("ends_at must be after starts_at")
	}
	return nil
}
