// HTTP glue for one-off VND payments through the Vietnamese gateways
// (Pay2S, ZaloPay, payOS). Four endpoints:
//
//	POST /api/v1/checkout/gateway-pay            start a checkout (public)
//	GET  /api/v1/checkout/gateway-pay/status     poll the payment state (public)
//	POST /api/v1/webhook/pay2s                   Pay2S IPN (public)
//	POST /api/v1/webhook/zalopay                 ZaloPay callback (public)
//	POST /api/v1/webhook/payos                   payOS IPN (public)
//
// The IPN response contracts are strict — the gateways retry until
// they see their own success shape:
//
//	pay2s:   200 {"success": true} on ack; 400 for a bad
//	         signature/body/amount; 5xx while the fulfilment is
//	         transiently failing (its retry ladder: 5min/15min/1h/24h).
//	zalopay: 200 {"return_code": 1, "return_message": "success"};
//	         200 {"return_code": 2, "return_message": "<reason>"}
//	         when the delivery is invalid (ZaloPay reads the body,
//	         not the HTTP status); 503 + return_code 2 while
//	         transiently failing so it comes back.
//	payos:   200 {"code": "00", "desc": "success", "success": true,
//	         "data": null}; 400 with the same envelope and
//	         success:false when invalid; 5xx while transiently
//	         failing (it retries anything non-2xx).
//
// Duplicate deliveries of an already-settled payment ack success
// without re-fulfilling. Every IPN body is capped at 1 MiB and no
// input can panic the handler.
package handler

import (
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/payment"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// PaymentGatewayHandler serves the gateway checkout and IPN routes.
type PaymentGatewayHandler struct {
	store    *store.Store
	baseURL  string
	email    *service.EmailService
	webhooks *service.WebhookService
}

// NewPaymentGatewayHandler wires the handler. baseURL is the install's
// public BaseURL (cfg.BaseURL) — the webhook URL handed to the gateway
// and the buyer's return page are derived from it.
func NewPaymentGatewayHandler(s *store.Store, baseURL string, email *service.EmailService, wh *service.WebhookService) *PaymentGatewayHandler {
	return &PaymentGatewayHandler{store: s, baseURL: strings.TrimRight(baseURL, "/"), email: email, webhooks: wh}
}

// gwMaxBody is the IPN body cap. Gateways send small JSON; 1 MiB is
// generous and keeps a hostile flood out of memory.
const gwMaxBody = 1 << 20

// GatewayMethods answers GET /api/v1/checkout/gateway-pay/methods —
// which gateways this install can actually charge through right now.
// Availability depends on real integration capability (plan §25): a
// gateway appears only when its credentials are configured. The web
// checkout uses this to decide whether to show the payment-method
// picker at all.
func (h *PaymentGatewayHandler) GatewayMethods(c *gin.Context) {
	type method struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	methods := []method{}
	for _, id := range []string{payment.ProviderPay2S, payment.ProviderZaloPay, payment.ProviderPayOS} {
		p, err := payment.Provider(id)
		if err != nil || !p.Enabled() {
			continue
		}
		methods = append(methods, method{ID: id, Name: gwDisplayName(id)})
	}
	response.OK(c, gin.H{"methods": methods})
}

// gwDisplayName is the buyer-facing gateway name. Brand names, not
// translated — they read the same in every locale.
func gwDisplayName(id string) string {
	switch id {
	case payment.ProviderPay2S:
		return "Pay2S"
	case payment.ProviderZaloPay:
		return "ZaloPay"
	case payment.ProviderPayOS:
		return "payOS"
	default:
		return id
	}
}

// gwAckStyle selects the per-gateway IPN response contract.
type gwAckStyle int

const (
	gwAckPay2S gwAckStyle = iota
	gwAckZaloPay
	gwAckPayOS
)

// GatewayPay answers POST /api/v1/checkout/gateway-pay.
//
// Body: {plan_id, provider, coupon_code?, country?, email?}. Optional
// attribution rides the query string (?reseller_code=&ref=) like the
// Stripe checkout link. The response is
// {pay_url, qr_code?, provider_ref, order_number, expires_at?}.
func (h *PaymentGatewayHandler) GatewayPay(c *gin.Context) {
	var req struct {
		PlanID     string `json:"plan_id" binding:"required"`
		Provider   string `json:"provider" binding:"required"`
		CouponCode string `json:"coupon_code"`
		Country    string `json:"country"`
		Email      string `json:"email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "plan_id, provider and a JSON body are required")
		return
	}

	res, err := payment.StartGatewayCheckout(c.Request.Context(), h.store, &payment.GatewayCheckoutRequest{
		PlanID:       strings.TrimSpace(req.PlanID),
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
		h.gatewayPayError(c, err)
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

// gatewayPayError maps a checkout refusal to the response contract:
// the provider sentinels first, then any *apperr.AppError forwarded
// verbatim (its code already means exactly one status — see
// pkg/response/contract_test.go).
func (h *PaymentGatewayHandler) gatewayPayError(c *gin.Context, err error) {
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

// GatewayPayStatus answers GET /api/v1/checkout/gateway-pay/status?order=…
// — the browser return page polls it. {status, provider} from the
// payment row; 404 for an order (or payment) it cannot find.
func (h *PaymentGatewayHandler) GatewayPayStatus(c *gin.Context) {
	orderNumber := strings.TrimSpace(c.Query("order"))
	if orderNumber == "" {
		response.BadRequest(c, "order is required")
		return
	}
	order, err := h.store.FindOrderByNumber(c.Request.Context(), orderNumber)
	if err != nil || order == nil {
		response.NotFound(c, "unknown order")
		return
	}
	row, err := h.store.GetGatewayPaymentByOrderKey(c.Request.Context(), order.PaymentProvider, order.OrderNumber)
	if err != nil || row == nil {
		response.NotFound(c, "no payment for this order")
		return
	}
	response.OK(c, gin.H{"status": row.Status, "provider": row.Provider})
}

// WebhookPay2S handles POST /api/v1/webhook/pay2s.
func (h *PaymentGatewayHandler) WebhookPay2S(c *gin.Context) {
	h.gatewayIPN(c, payment.ProviderPay2S, gwAckPay2S)
}

// WebhookZaloPay handles POST /api/v1/webhook/zalopay.
func (h *PaymentGatewayHandler) WebhookZaloPay(c *gin.Context) {
	h.gatewayIPN(c, payment.ProviderZaloPay, gwAckZaloPay)
}

// WebhookPayOS handles POST /api/v1/webhook/payos.
func (h *PaymentGatewayHandler) WebhookPayOS(c *gin.Context) {
	h.gatewayIPN(c, payment.ProviderPayOS, gwAckPayOS)
}

// gatewayIPN is the shared IPN pipeline: bounded raw body →
// provider.VerifyWebhook (fails closed) → payment.GatewayFulfil →
// the per-gateway ack. Verification runs before any field is trusted,
// and every refusal is logged with the X-Forwarded-For-aware client
// IP the house uses (c.ClientIP).
func (h *PaymentGatewayHandler) gatewayIPN(c *gin.Context, providerName string, style gwAckStyle) {
	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, gwMaxBody))
	if err != nil {
		slog.Warn("gateway IPN refused: body unreadable or too large",
			"provider", providerName, "ip", c.ClientIP(), "error", err)
		h.gatewayRefuse(c, style, "request body unreadable")
		return
	}

	p, err := payment.Provider(providerName)
	if err != nil || !p.Enabled() {
		// Transient: the operator may still be configuring the
		// gateway. Ask it to come back rather than dropping the IPN.
		slog.Error("gateway IPN received for a provider that is not configured",
			"provider", providerName, "ip", c.ClientIP())
		h.gatewayTemporary(c, style, "payment provider not configured")
		return
	}

	ev, err := p.VerifyWebhook(body)
	if err != nil {
		// Security event: an unauthenticated or unparseable delivery.
		slog.Warn("gateway IPN refused: verification failed",
			"provider", providerName, "ip", c.ClientIP(), "error", err)
		h.gatewayRefuse(c, style, "verification failed")
		return
	}

	ctx := c.Request.Context()
	switch err := payment.GatewayFulfil(ctx, h.store, &payment.GatewayFulfilDeps{
		Email: h.email, Webhooks: h.webhooks,
	}, ev); {
	case err == nil:
		h.gatewayAck(c, style)
	case errors.Is(err, payment.ErrAmountMismatch):
		// Never fulfil a mismatched amount — audit AND security log.
		slog.Error("gateway IPN refused: amount mismatch",
			"provider", providerName, "ip", c.ClientIP(),
			"ref", ev.ProviderRef, "order", ev.OrderID, "error", err)
		if h.store != nil {
			h.store.Audit(ctx, &model.AuditLog{
				Entity:    "gateway_payment",
				EntityID:  providerName + ":" + ev.ProviderRef,
				Action:    "ipn_amount_mismatch",
				ActorType: "gateway_ipn", ActorID: providerName, IPAddress: c.ClientIP(),
				Changes: map[string]any{"order": ev.OrderID, "error": err.Error()},
			})
		}
		h.gatewayRefuse(c, style, "amount mismatch")
	case errors.Is(err, payment.ErrGatewayPaymentNotFound),
		errors.Is(err, payment.ErrGatewayOrderNotPending),
		errors.Is(err, payment.ErrGatewayUnfulfillable):
		// Permanent: retrying cannot fix these. Refuse loudly; the
		// payment (if real) stays for manual reconciliation.
		slog.Error("gateway IPN refused: payment cannot be fulfilled",
			"provider", providerName, "ip", c.ClientIP(),
			"ref", ev.ProviderRef, "order", ev.OrderID, "error", err)
		h.gatewayRefuse(c, style, "payment cannot be fulfilled")
	default:
		// Transient (database down, licence write failed, another
		// delivery mid-flight): ask the gateway to retry.
		slog.Error("gateway IPN transient failure, asking the gateway to retry",
			"provider", providerName, "ip", c.ClientIP(),
			"ref", ev.ProviderRef, "order", ev.OrderID, "error", err)
		h.gatewayTemporary(c, style, "temporary failure")
	}
}

// gwAck answers the settled case in the gateway's own success shape.
func gwAck(c *gin.Context, style gwAckStyle) {
	switch style {
	case gwAckPay2S:
		c.JSON(http.StatusOK, gin.H{"success": true})
	case gwAckZaloPay:
		c.JSON(http.StatusOK, gin.H{"return_code": 1, "return_message": "success"})
	case gwAckPayOS:
		c.JSON(http.StatusOK, gin.H{"code": "00", "desc": "success", "success": true, "data": nil})
	}
}

// gwRefuse answers a permanently refused delivery. ZaloPay wants its
// answer in the body (return_code), the others in the HTTP status.
func gwRefuse(c *gin.Context, style gwAckStyle, reason string) {
	switch style {
	case gwAckPay2S:
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "error": reason})
	case gwAckZaloPay:
		c.JSON(http.StatusOK, gin.H{"return_code": 2, "return_message": reason})
	case gwAckPayOS:
		c.JSON(http.StatusBadRequest, gin.H{"code": "01", "desc": reason, "success": false, "data": nil})
	}
}

// gwTemporary answers a transient failure: non-2xx so every gateway's
// retry ladder kicks in (ZaloPay also sees return_code != 1).
func gwTemporary(c *gin.Context, style gwAckStyle, reason string) {
	switch style {
	case gwAckPay2S:
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "error": reason})
	case gwAckZaloPay:
		c.JSON(http.StatusServiceUnavailable, gin.H{"return_code": 2, "return_message": reason})
	case gwAckPayOS:
		c.JSON(http.StatusServiceUnavailable, gin.H{"code": "01", "desc": reason, "success": false, "data": nil})
	}
}

// gatewayAck/gatewayRefuse/gatewayTemporary are the method-typed
// dispatchers the IPN pipeline uses (kept as methods so the handler's
// dependency on the helpers reads top-down).
func (h *PaymentGatewayHandler) gatewayAck(c *gin.Context, style gwAckStyle) { gwAck(c, style) }
func (h *PaymentGatewayHandler) gatewayRefuse(c *gin.Context, style gwAckStyle, reason string) {
	gwRefuse(c, style, reason)
}
func (h *PaymentGatewayHandler) gatewayTemporary(c *gin.Context, style gwAckStyle, reason string) {
	gwTemporary(c, style, reason)
}
