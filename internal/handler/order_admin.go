package handler

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/tax"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// OrderAdminHandler exposes admin-only order ledger endpoints.
//
//	GET    /admin/orders                  List orders (search, status)
//	GET    /admin/orders/:id              Get order with items + invoices
//	POST   /admin/orders/preview          Price an order without persisting
//	POST   /admin/orders/:id/refund       Mark a paid order refunded
//	GET    /admin/orders/:id/invoices     List the order's invoices
//
// Every amount in the request and the response is int64 minor units;
// no endpoint of this handler ever speaks in floating point.
type OrderAdminHandler struct {
	Svc   *service.OrderService
	Store *store.Store
}

// NewOrderAdminHandler wires the handler. svc prices and persists
// orders; s is the ledger for lookups and audit.
func NewOrderAdminHandler(svc *service.OrderService, s *store.Store) *OrderAdminHandler {
	return &OrderAdminHandler{Svc: svc, Store: s}
}

// List answers GET /admin/orders.
func (h *OrderAdminHandler) List(c *gin.Context) {
	page := listPage(c)
	status := c.Query("status")
	switch status {
	case "", model.OrderStatusPending, model.OrderStatusPaid,
		model.OrderStatusFailed, model.OrderStatusRefunded:
	default:
		response.BadRequest(c, "status must be pending, paid, failed, or refunded")
		return
	}
	orders, total, err := h.Store.ListOrders(c, c.Query("search"), status, page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "orders", orders, total, page)
}

// Get answers GET /admin/orders/:id — the order with its line items
// (loaded by the store) and every invoice drawn against it.
func (h *OrderAdminHandler) Get(c *gin.Context) {
	o, err := h.Store.FindOrderByID(c, c.Param("id"))
	if err != nil {
		response.NotFound(c, "order not found")
		return
	}
	invoices, err := h.Store.ListInvoicesByOrder(c, o.ID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, gin.H{"order": o, "invoices": response.Array(invoices)})
}

// Preview answers POST /admin/orders/preview: it runs exactly the
// calculation CreateOrder would run and returns the totals — and the
// per-line breakdown — without writing anything. What is quoted is
// what will be charged.
//
// Body: { lines: [{sku?, product_id?, plan_id?, description?,
// quantity, unit_amount_minor}], currency, coupon_code?, tax_rates?:
// [{basis_points, jurisdiction}], tax_inclusive? }
func (h *OrderAdminHandler) Preview(c *gin.Context) {
	var req struct {
		Lines []struct {
			SKU             string `json:"sku"`
			ProductID       string `json:"product_id"`
			PlanID          string `json:"plan_id"`
			Description     string `json:"description"`
			Quantity        int64  `json:"quantity"`
			UnitAmountMinor int64  `json:"unit_amount_minor"`
		} `json:"lines"`
		Currency     string `json:"currency"`
		CouponCode   string `json:"coupon_code"`
		TaxInclusive bool   `json:"tax_inclusive"`
		TaxRates     []struct {
			BasisPoints  int64  `json:"basis_points"`
			Jurisdiction string `json:"jurisdiction"`
		} `json:"tax_rates"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	if len(req.Lines) == 0 {
		response.BadRequest(c, "lines must not be empty")
		return
	}
	lines := make([]service.Line, len(req.Lines))
	for i, l := range req.Lines {
		if l.Quantity <= 0 {
			response.BadRequest(c, "each line quantity must be greater than zero")
			return
		}
		if l.UnitAmountMinor < 0 {
			response.BadRequest(c, "each line unit_amount_minor must not be negative")
			return
		}
		lines[i] = service.Line{
			SKU:             l.SKU,
			ProductID:       l.ProductID,
			PlanID:          l.PlanID,
			Description:     l.Description,
			Quantity:        l.Quantity,
			UnitAmountMinor: l.UnitAmountMinor,
		}
	}
	rates := make([]tax.Rate, len(req.TaxRates))
	for i, r := range req.TaxRates {
		rates[i] = tax.Rate{BasisPoints: r.BasisPoints, Jurisdiction: r.Jurisdiction}
	}

	res, err := h.Svc.Calculate(c, service.CalcInput{
		Lines:        lines,
		Currency:     req.Currency,
		CouponCode:   req.CouponCode,
		TaxRates:     rates,
		TaxInclusive: req.TaxInclusive,
	})
	if err != nil {
		writeAppErr(c, err)
		return
	}

	body := gin.H{
		"currency":       req.Currency,
		"tax_inclusive":  req.TaxInclusive,
		"subtotal_minor": res.SubtotalMinor,
		"discount_minor": res.DiscountMinor,
		"tax_minor":      res.TaxMinor,
		"total_minor":    res.TotalMinor,
		"lines":          res.LineResults,
		"applied_coupon": nil,
	}
	if res.AppliedCoupon != nil {
		body["applied_coupon"] = gin.H{
			"code":     res.AppliedCoupon.Code,
			"type":     string(res.AppliedCoupon.Type),
			"value":    res.AppliedCoupon.Value,
			"currency": res.AppliedCoupon.Currency,
		}
	}
	response.OK(c, body)
}

// Refund answers POST /admin/orders/:id/refund: the paid order is
// marked refunded and refunded_at is stamped. Paid_at is left alone —
// the moment the money arrived is history. Only a paid order can be
// refunded; a pending one has taken no money and a refunded one is
// done.
func (h *OrderAdminHandler) Refund(c *gin.Context) {
	id := c.Param("id")
	o, err := h.Store.FindOrderByID(c, id)
	if err != nil {
		response.NotFound(c, "order not found")
		return
	}
	if o.RefundedAt != nil || o.Status == model.OrderStatusRefunded {
		response.Err(c, 409, "ALREADY_REFUNDED", "order is already refunded")
		return
	}
	if o.Status != model.OrderStatusPaid {
		response.Err(c, 409, "NOT_REFUNDABLE", "only a paid order can be refunded")
		return
	}
	now := time.Now()
	if err := h.Store.UpdateOrderStatus(c, id, model.OrderStatusRefunded, nil, &now); err != nil {
		response.Internal(c, err)
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "order", EntityID: id, Action: "refunded",
		ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
	})
	updated, err := h.Store.FindOrderByID(c, id)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, updated)
}

// ListInvoices answers GET /admin/orders/:id/invoices (and
// GET /admin/invoices?order_id=…) with every invoice drawn against
// the order.
func (h *OrderAdminHandler) ListInvoices(c *gin.Context) {
	orderID := c.Param("id")
	if orderID == "" {
		orderID = c.Query("order_id")
	}
	if orderID == "" {
		response.BadRequest(c, "order id is required")
		return
	}
	invoices, err := h.Store.ListInvoicesByOrder(c, orderID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "invoices", invoices, len(invoices), listPage(c))
}
