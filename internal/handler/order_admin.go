package handler

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/tax"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// OrderAdminHandler exposes admin-only order ledger endpoints.
//
//	GET    /admin/orders                  List orders (search, status)
//	GET    /admin/orders/:id              Get order with items + invoices
//	POST   /admin/orders/preview          Price an order without persisting
//	POST   /admin/orders/:id/refund       Mark a paid order refunded
//	GET    /admin/orders/:id/invoices     List the order's invoices
//	PATCH  /admin/orders/:id/billing      Set the billing block + PO/tax id
//	POST   /admin/orders/:id/invoices/:invoice_id/void
//	POST   /admin/orders/:id/invoices/:invoice_id/mark-uncollectible
//
// Every amount in the request and the response is int64 minor units;
// no endpoint of this handler ever speaks in floating point.

// orderAdminStore is the slice of store.Store this handler needs. The
// real constructor takes *store.Store; tests substitute a fake so the
// refusal paths — invalid billing input, illegal invoice transitions —
// run without a database.
type orderAdminStore interface {
	ListOrders(ctx context.Context, search, status string, p store.Page) ([]*model.Order, int, error)
	FindOrderByID(ctx context.Context, id string) (*model.Order, error)
	UpdateOrderStatus(ctx context.Context, id, status string, paidAt, refundedAt *time.Time) error
	UpdateOrderBilling(ctx context.Context, id string, o *model.Order) error
	ListInvoicesByOrder(ctx context.Context, orderID string) ([]*model.Invoice, error)
	FindInvoiceByID(ctx context.Context, id string) (*model.Invoice, error)
	UpdateInvoiceStatus(ctx context.Context, id, status string, voidedAt, uncollectibleAt *time.Time) error
	Audit(ctx context.Context, log *model.AuditLog)
}

var _ orderAdminStore = (*store.Store)(nil)

type OrderAdminHandler struct {
	Svc   *service.OrderService
	Store orderAdminStore
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

// ─── Billing block (PO / invoice workflow) ───

// billingPatch is the body of PATCH /admin/orders/:id/billing. Every
// field is a pointer so the wire distinguishes three intents — the
// convention the endpoint is documented by:
//
//	absent or null        → the field keeps its current value (merge,
//	                        not replace: one PATCH can touch just
//	                        po_number and leave the address alone)
//	"" (or only spaces)   → the field is CLEARED: stored NULL and
//	                        omitted from the response JSON
//	"some value"          → the field is set, after validation and
//	                        folding (country upper-cased, text trimmed)
type billingPatch struct {
	BillingName         *string `json:"billing_name"`
	BillingCompany      *string `json:"billing_company"`
	BillingAddressLine1 *string `json:"billing_address_line1"`
	BillingAddressLine2 *string `json:"billing_address_line2"`
	BillingCity         *string `json:"billing_city"`
	BillingRegion       *string `json:"billing_region"`
	BillingPostalCode   *string `json:"billing_postal_code"`
	BillingCountry      *string `json:"billing_country"`
	CustomerTaxID       *string `json:"customer_tax_id"`
	PONumber            *string `json:"po_number"`
	BillingEmail        *string `json:"billing_email"`
}

// Length caps for the free-text billing fields. The name/company/
// address/city/region cap matches apperr.ValidateName's 200 — one
// idea of "how long is a human name" across the codebase.
const (
	billingTextMax   = 200 // name, company, address lines, city, region
	billingPostalMax = 32  // billing_postal_code
	billingPOMax     = 64  // po_number
)

// taxIDShape is the loose VAT/tax-id shape: 8–20 characters of
// letters, digits and dashes — what most VAT/GST/TIN schemes share —
// without pretending to validate any country's checksum.
var taxIDShape = regexp.MustCompile(`^[A-Za-z0-9-]{8,20}$`)

// billingText merges one free-text billing field: absent keeps, trimmed
// value replaces, whitespace-only clears. max is the length cap.
func billingText(dst, v *string, max int, field string) error {
	if v == nil {
		return nil
	}
	s := strings.TrimSpace(*v)
	if len(s) > max {
		return fmt.Errorf("%s must be at most %d characters", field, max)
	}
	*dst = s
	return nil
}

// applyBillingPatch merges the patch into the order and validates the
// values it sets. It returns a plain error whose message is safe to
// hand the caller — every one of them is a 400 the admin can fix.
// Shapes: billing_country must be a 2-letter ISO 3166-1 alpha-2 code
// (folded to upper case on write), customer_tax_id 8–20 alphanumerics
// and dashes, billing_email a bare address of at most 254 characters,
// po_number at most 64. All are optional; empty clears.
func applyBillingPatch(o *model.Order, p *billingPatch) error {
	if err := billingText(&o.BillingName, p.BillingName, billingTextMax, "billing_name"); err != nil {
		return err
	}
	if err := billingText(&o.BillingCompany, p.BillingCompany, billingTextMax, "billing_company"); err != nil {
		return err
	}
	if err := billingText(&o.BillingAddressLine1, p.BillingAddressLine1, billingTextMax, "billing_address_line1"); err != nil {
		return err
	}
	if err := billingText(&o.BillingAddressLine2, p.BillingAddressLine2, billingTextMax, "billing_address_line2"); err != nil {
		return err
	}
	if err := billingText(&o.BillingCity, p.BillingCity, billingTextMax, "billing_city"); err != nil {
		return err
	}
	if err := billingText(&o.BillingRegion, p.BillingRegion, billingTextMax, "billing_region"); err != nil {
		return err
	}
	if err := billingText(&o.BillingPostalCode, p.BillingPostalCode, billingPostalMax, "billing_postal_code"); err != nil {
		return err
	}
	if p.BillingCountry != nil {
		s := strings.ToUpper(strings.TrimSpace(*p.BillingCountry))
		if s != "" && (len(s) != 2 || s[0] < 'A' || s[0] > 'Z' || s[1] < 'A' || s[1] > 'Z') {
			return errors.New("billing_country must be a 2-letter ISO 3166-1 alpha-2 code (e.g. VN, US)")
		}
		o.BillingCountry = s
	}
	if p.CustomerTaxID != nil {
		s := strings.TrimSpace(*p.CustomerTaxID)
		if s != "" && !taxIDShape.MatchString(s) {
			return errors.New("customer_tax_id must be 8 to 20 characters of letters, digits, and dashes")
		}
		o.CustomerTaxID = s
	}
	if err := billingText(&o.PONumber, p.PONumber, billingPOMax, "po_number"); err != nil {
		return err
	}
	if p.BillingEmail != nil {
		s := strings.TrimSpace(*p.BillingEmail)
		if s != "" {
			if err := apperr.ValidateEmail(s); err != nil {
				return errors.New(err.Message)
			}
		}
		o.BillingEmail = s
	}
	return nil
}

// UpdateBilling answers PATCH /admin/orders/:id/billing: it sets or
// clears fields of the order's billing block — billing address,
// customer tax id, purchase-order number and invoicing contact. See
// billingPatch for the merge/clear convention. What is stored is
// canonical whatever the admin typed (trimmed text, upper-case
// country), and the response is the updated order so the caller sees
// exactly what the ledger now holds.
func (h *OrderAdminHandler) UpdateBilling(c *gin.Context) {
	id := c.Param("id")
	o, err := h.Store.FindOrderByID(c, id)
	if err != nil {
		response.NotFound(c, "order not found")
		return
	}
	var p billingPatch
	if err := c.ShouldBindJSON(&p); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	if err := applyBillingPatch(o, &p); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := h.Store.UpdateOrderBilling(c, id, o); err != nil {
		response.Internal(c, err)
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "order", EntityID: id, Action: "billing_updated",
		ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
	})
	updated, err := h.Store.FindOrderByID(c, id)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, updated)
}

// ─── Invoice state transitions (PO / invoice workflow) ───

// adminInvoice resolves :invoice_id in the scope of :id — the invoice
// must be drawn on the named order, or the answer is the same 404 as a
// missing invoice, so these endpoints are never an existence oracle
// for other orders' documents.
func (h *OrderAdminHandler) adminInvoice(c *gin.Context) (*model.Invoice, bool) {
	inv, err := h.Store.FindInvoiceByID(c, c.Param("invoice_id"))
	if err != nil || inv.OrderID != c.Param("id") {
		response.NotFound(c, "invoice not found")
		return nil, false
	}
	return inv, true
}

// MarkUncollectible answers
// POST /admin/orders/:id/invoices/:invoice_id/mark-uncollectible: the
// open invoice is declared uncollectible — given up on, neither paid
// nor cancelled — and uncollectible_at is stamped. Only an open
// invoice can get there (model.CanTransitionInvoice); anything else,
// a paid invoice included, is refused 409 INVOICE_TRANSITION_INVALID
// rather than silently ignored. The state is recoverable: the invoice
// can still move back to open or on to paid.
func (h *OrderAdminHandler) MarkUncollectible(c *gin.Context) {
	inv, ok := h.adminInvoice(c)
	if !ok {
		return
	}
	if !model.CanTransitionInvoice(inv.Status, model.InvoiceStatusUncollectible) {
		response.Err(c, 409, "INVOICE_TRANSITION_INVALID",
			"invoice cannot be marked uncollectible from status "+inv.Status)
		return
	}
	now := time.Now()
	if err := h.Store.UpdateInvoiceStatus(c, inv.ID, model.InvoiceStatusUncollectible, nil, &now); err != nil {
		response.Internal(c, err)
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "invoice", EntityID: inv.ID, Action: "marked_uncollectible",
		ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
	})
	updated, err := h.Store.FindInvoiceByID(c, inv.ID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, updated)
}

// Void answers POST /admin/orders/:id/invoices/:invoice_id/void: the
// unpaid invoice is cancelled and voided_at is stamped. A PAID invoice
// is refused with 409 INVOICE_NOT_VOIDABLE — money changed hands, so
// the only way off paid is the refund
// (POST /admin/orders/:id/refund, whose behavior is untouched), never
// a void. Any other illegal transition is 409
// INVOICE_TRANSITION_INVALID. Void is terminal.
func (h *OrderAdminHandler) Void(c *gin.Context) {
	inv, ok := h.adminInvoice(c)
	if !ok {
		return
	}
	if inv.Status == model.InvoiceStatusPaid {
		response.Err(c, 409, "INVOICE_NOT_VOIDABLE",
			"a paid invoice cannot be voided; refund the order first")
		return
	}
	if !model.CanTransitionInvoice(inv.Status, model.InvoiceStatusVoid) {
		response.Err(c, 409, "INVOICE_TRANSITION_INVALID",
			"invoice cannot be voided from status "+inv.Status)
		return
	}
	now := time.Now()
	if err := h.Store.UpdateInvoiceStatus(c, inv.ID, model.InvoiceStatusVoid, &now, nil); err != nil {
		response.Internal(c, err)
		return
	}
	h.Store.Audit(c, &model.AuditLog{
		Entity: "invoice", EntityID: inv.ID, Action: "voided",
		ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
	})
	updated, err := h.Store.FindInvoiceByID(c, inv.ID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, updated)
}
