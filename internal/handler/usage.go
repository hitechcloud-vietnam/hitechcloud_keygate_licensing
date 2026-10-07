package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

type UsageHandler struct {
	svc *service.UsageService
}

func NewUsageHandler(svc *service.UsageService) *UsageHandler {
	return &UsageHandler{svc: svc}
}

func (h *UsageHandler) RecordUsage(c *gin.Context) {
	var req struct {
		LicenseKey string         `json:"license_key" binding:"required"`
		Feature    string         `json:"feature" binding:"required"`
		Quantity   int64          `json:"quantity"`
		Metadata   map[string]any `json:"metadata"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "license_key and feature are required")
		return
	}
	// 0 (omitted) means one unit; a negative number is a client bug,
	// not a refund — silently counting it as 1 would hide that.
	if req.Quantity < 0 {
		response.BadRequest(c, "quantity cannot be negative")
		return
	}

	productID, _ := c.Get("product_id")
	result, err := h.svc.RecordUsage(c.Request.Context(), service.RecordUsageInput{
		LicenseKey: req.LicenseKey,
		Feature:    req.Feature,
		Quantity:   req.Quantity,
		Metadata:   req.Metadata,
		ProductID:  str(productID),
		IPAddress:  c.ClientIP(),
	})
	if err != nil {
		writeAppErr(c, err)
		return
	}
	response.OK(c, result)
}

func (h *UsageHandler) GetQuotaStatus(c *gin.Context) {
	var req struct {
		LicenseKey string `json:"license_key" binding:"required"`
		Feature    string `json:"feature" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "license_key and feature are required")
		return
	}

	productID, _ := c.Get("product_id")
	result, err := h.svc.GetQuotaStatus(c.Request.Context(), req.LicenseKey, req.Feature, str(productID))
	if err != nil {
		writeAppErr(c, err)
		return
	}
	response.OK(c, result)
}
