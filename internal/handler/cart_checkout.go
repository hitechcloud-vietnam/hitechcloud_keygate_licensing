// Multi-item (cart) checkout endpoints, backend half (plan §23).
//
//	POST /api/v1/checkout/cart       Stripe: ONE checkout session for
//	                                 a priced server-side cart
//	POST /api/v1/checkout/gateway-pay  VN gateways: one order with N
//	                                 lines, one gateway payment.
//	                                 Accepts the cart shape
//	                                 {items:[{plan_id,quantity}]}
//	                                 AND the original single-plan
//	                                 {plan_id} — both work.
//
// The gateway endpoint is a drop-in replacement for
// PaymentGatewayHandler.GatewayPay (same route, same response shape,
// same error codes): Lead swaps the route target to enable carts.
// Nothing here ever reads an amount from the client — the request has
// no field for one and every line is priced server-side
// (payment/cart_checkout.go).
//
// Error codes (each maps to exactly one status,
// pkg/response/contract_test.go):
//
//	PROVIDER_NOT_FOUND        404
//	PROVIDER_NOT_CONFIGURED   503
//	CURRENCY_NOT_SUPPORTED    400
//	PAYMENT_GATEWAY_ERROR     502
//	MISSING_CUSTOMER          400
//	PLAN_NOT_FOUND            404
//	COUPON_NOT_FOUND          404
//	PRICING_UNAVAILABLE       503
//	…plus whatever *apperr.AppError the pricing flow already means
//	 (forwarded verbatim).
package handler

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/payment"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// CartCheckoutHandler exposes the two cart checkout endpoints.
//
// carts prices and starts the Stripe cart (a seam so tests run
// without a database); store is the concrete store the gateway flow
// takes (payment.StartGatewayCheckout's signature); baseURL is the
// install's public URL for the buyer's return trip.
type CartCheckoutHandler struct {
	carts   payment.CartStore
	store   *store.Store
	baseURL string
}

// NewCartCheckoutHandler wires the handler.
func NewCartCheckoutHandler(s *store.Store, baseURL string) *CartCheckoutHandler {
	return &CartCheckoutHandler{carts: s, store: s, baseURL: baseURL}
}

// cartCheckoutBody is the body of POST /api/v1/checkout/cart.
type cartCheckoutBody struct {
	Items        []payment.CartCheckoutItem `json:"items"`
	CouponCode   string                     `json:"coupon_code"`
	Country      string                     `json:"country"`
	Email        string                     `json:"email"`
	Currency     string                     `json:"currency"`
	TaxInclusive *bool                      `json:"tax_inclusive"`
}

// Checkout answers POST /api/v1/checkout/cart: one Stripe Checkout
// Session for the whole cart, priced entirely server-side. The
// response is {checkout_url, checkout_id, amount_minor, currency} —
// checkout_id is the Stripe Checkout Session id.
//
// Attribution rides the query string (?reseller_code=&ref=) exactly
// like the single-plan checkout link.
func (h *CartCheckoutHandler) Checkout(c *gin.Context) {
	var req cartCheckoutBody
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "a JSON body with items is required")
		return
	}
	res, err := payment.CreateCartCheckoutSession(c.Request.Context(), h.carts, &payment.CartCheckoutRequest{
		Items:        req.Items,
		CouponCode:   req.CouponCode,
		Country:      req.Country,
		Email:        req.Email,
		Currency:     req.Currency,
		TaxInclusive: req.TaxInclusive,
		ResellerCode: c.Query("reseller_code"),
		Ref:          c.Query("ref"),
		SuccessURL:   h.baseURL + "/checkout/success?session_id={CHECKOUT_SESSION_ID}",
		CancelURL:    h.baseURL + "/pricing",
	})
	if err != nil {
		writeAppErr(c, err)
		return
	}
	response.Created(c, gin.H{
		"checkout_url": res.URL,
		"checkout_id":  res.CheckoutID,
		"amount_minor": res.TotalMinor,
		"currency":     res.Currency,
	})
}

// gatewayPayBody is the body of POST /api/v1/checkout/gateway-pay:
// the original single-plan shape ({plan_id, provider, …}) and the
// cart shape ({items, provider, …}) — exactly one of plan_id or
// items.
type gatewayPayBody struct {
	PlanID     string                     `json:"plan_id"`
	Items      []payment.CartCheckoutItem `json:"items"`
	Provider   string                     `json:"provider" binding:"required"`
	CouponCode string                     `json:"coupon_code"`
	Country    string                     `json:"country"`
	Email      string                     `json:"email"`
}

// GatewayPay answers POST /api/v1/checkout/gateway-pay (drop-in
// replacement for PaymentGatewayHandler.GatewayPay): {items:
// [{plan_id, quantity}], provider, coupon_code?, country?, email?} —
// one order, N order lines, one gateway payment. The single
// {plan_id} shape still works unchanged.
//
// The response is the same contract the single-plan flow answers:
// {pay_url, qr_code?, provider_ref, order_number, amount_minor,
// currency, expires_at?} (201).
func (h *CartCheckoutHandler) GatewayPay(c *gin.Context) {
	var req gatewayPayBody
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "provider and a JSON body are required")
		return
	}
	if strings.TrimSpace(req.PlanID) == "" && len(req.Items) == 0 {
		response.BadRequest(c, "plan_id or items is required")
		return
	}

	res, err := payment.StartGatewayCheckout(c.Request.Context(), h.store, &payment.GatewayCheckoutRequest{
		PlanID:       strings.TrimSpace(req.PlanID),
		Items:        req.Items,
		Provider:     req.Provider,
		CouponCode:   req.CouponCode,
		Country:      req.Country,
		Email:        req.Email,
		ResellerCode: c.Query("reseller_code"),
		Ref:          c.Query("ref"),
		WebhookURL:   h.baseURL + "/api/v1/webhook/" + strings.ToLower(strings.TrimSpace(req.Provider)),
		ReturnURL:    h.baseURL + "/checkout/gateway-return",
		CancelURL:    h.baseURL + "/pricing",
	})
	if err != nil {
		cartPayError(c, err)
		return
	}

	out := gin.H{
		"pay_url":      res.Payment.PayURL,
		"provider_ref": res.Payment.ProviderRef,
		"order_number": res.Order.OrderNumber,
		"amount_minor": res.Order.TotalMinor,
		"currency":     res.Order.Currency,
	}
	if res.Payment.QRCode != "" {
		out["qr_code"] = res.Payment.QRCode
	}
	if !res.Payment.ExpiresAt.IsZero() {
		out["expires_at"] = res.Payment.ExpiresAt.Format(time.RFC3339)
	}
	response.Created(c, out)
}

// cartPayError maps a checkout refusal to the response contract: the
// provider sentinels first (one status each), then any
// *apperr.AppError forwarded verbatim (its code already means exactly
// one status — pkg/response/contract_test.go).
func cartPayError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, payment.ErrProviderNotFound):
		response.Err(c, http.StatusNotFound, "PROVIDER_NOT_FOUND", "unknown payment provider")
	case errors.Is(err, payment.ErrProviderNotConfigured):
		response.Err(c, http.StatusServiceUnavailable, "PROVIDER_NOT_CONFIGURED", "this payment provider is not configured")
	case errors.Is(err, payment.ErrCurrencyNotSupported):
		response.Err(c, http.StatusBadRequest, "CURRENCY_NOT_SUPPORTED", "this gateway only settles VND")
	default:
		var ae *apperr.AppError
		if errors.As(err, &ae) {
			response.Err(c, ae.Status, ae.Code, ae.Message)
			return
		}
		response.Internal(c, err)
	}
}
