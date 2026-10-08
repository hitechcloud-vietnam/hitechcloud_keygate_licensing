// Checkout + IPN fulfilment glue for the Vietnamese one-off payment
// gateways (Pay2S, ZaloPay, payOS) behind the payment.PaymentProvider
// abstraction (plan §25). Stripe keeps subscriptions; these gateways
// settle VND whole-dong one-time purchases asynchronously through
// their IPN, so the flow is:
//
//  1. StartGatewayCheckout prices the sale exactly the way the quote
//     endpoint prices it (checkout_terms.go — server-side, nothing is
//     read from the client), records the order as PENDING and calls
//     provider.CreatePayment;
//  2. GatewayFulfil authenticates the normalized callback and settles
//     the payment exactly once: order → paid, invoice, licence, and
//     the recordOrder-style ledger + hooks.
//
// Money discipline (plan §51): every amount is int64 minor units
// (VND: whole dong). No floats, ever.
package payment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	stripe "github.com/stripe/stripe-go/v82"
	stripeprice "github.com/stripe/stripe-go/v82/price"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/coupon"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/license"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/tax"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
)

// Fulfilment sentinels. Handlers map these to the per-gateway IPN
// response contracts: a permanent refusal stops the gateway's retry
// ladder, a transient one (and anything unclassified) asks it to come
// back.
var (
	// ErrGatewayPaymentNotFound: an authenticated callback for a
	// payment this install has no row (and no pending order) for.
	ErrGatewayPaymentNotFound = errors.New("payment: gateway payment not found")
	// ErrGatewayOrderNotPending: the order behind the payment is no
	// longer awaiting settlement (failed, refunded, cancelled…).
	ErrGatewayOrderNotPending = errors.New("payment: gateway order is not pending")
	// ErrGatewayFulfilInFlight: another delivery of this payment is
	// being fulfilled right now. Transient — the retry finds it done.
	ErrGatewayFulfilInFlight = errors.New("payment: gateway fulfilment in flight")
	// ErrGatewayUnfulfillable: paid, but the sale cannot produce a
	// licence (no plan, no email, no line items). Permanent — needs a
	// human and a refund, not a retry.
	ErrGatewayUnfulfillable = errors.New("payment: gateway sale cannot be fulfilled")
)

// GatewayCheckoutRequest is one buyer's attempt to pay a plan through
// a VN gateway. Amounts are NEVER taken from here — the sale is priced
// server-side from the plan's price (Stripe Price is the catalogue's
// source of truth) through the same path the quote endpoint uses.
type GatewayCheckoutRequest struct {
	PlanID     string
	Provider   string // payment.ProviderPay2S | ProviderZaloPay | ProviderPayOS
	CouponCode string
	Country    string // matched against the operator's active tax rates
	Email      string // the buyer — required: the licence is delivered to it

	// Items is the multi-item (cart) shape (plan §23): one order with
	// N order lines and ONE gateway payment for the total. When set,
	// PlanID is ignored and the sale is priced through the cart
	// engine (cart_checkout.go), which resolves each line's unit
	// price from plan_prices (§53) or the Stripe Price fallback.
	// The single-PlanID shape above stays exactly as it was.
	Items []CartCheckoutItem

	// Attribution inputs (see attribution.go): who brought the sale.
	// Optional, query-parameter shaped — the JSON body stays
	// {plan_id, provider, coupon_code?, country?, email?}.
	ResellerCode string
	Ref          string

	// TaxInclusive is the buyer's pricing mode; nil lets the matched
	// rate rows decide (all-inclusive rows price inclusive).
	TaxInclusive *bool

	// Where the gateway should post its IPN and where the buyer's
	// browser lands afterwards. Built by the caller from the install's
	// BaseURL: WebhookURL is <base>/api/v1/webhook/<provider>,
	// ReturnURL the browser return page (the helper appends
	// ?order=<order_number>), CancelURL the pricing page.
	WebhookURL string
	ReturnURL  string
	CancelURL  string
}

// GatewayCheckoutResult is what the HTTP layer turns into
// {pay_url, qr_code?, provider_ref, order_number, expires_at?}.
type GatewayCheckoutResult struct {
	Payment *CreatePaymentResult
	Order   *model.Order
}

// GatewayFulfilDeps are the fulfilment side-effect services. Both are
// optional: without an EmailService the licence mail falls back to a
// built-in template, without a WebhookService no merchant webhook
// fires. A nil *GatewayFulfilDeps is valid.
type GatewayFulfilDeps struct {
	Email    *service.EmailService
	Webhooks *service.WebhookService
}

const (
	// gatewayFulfilClaimProvider keys the processed_events row that
	// guards the crash-window heal path (see GatewayFulfil).
	gatewayFulfilClaimProvider = "gateway_fulfil"
	// gatewayFulfilStaleAge is how long a heal claim may sit before a
	// later delivery takes it over — the heal's owner died mid-way.
	// Well under Pay2S's first retry (5 min), so a crashed heal is
	// picked up on the next delivery.
	gatewayFulfilStaleAge = 2 * time.Minute
	// gatewayPriceWait bounds the catalogue price lookup on a public
	// endpoint (mirrors the quote handler's bound).
	gatewayPriceWait = 5 * time.Second
)

// gatewayPlanUnitPrice reads the plan's price: the unit amount and ISO
// currency the catalogue sells it at. This is the ONLY place money
// enters a gateway checkout — there is no request field for an amount.
func gatewayPlanUnitPrice(ctx context.Context, priceID string) (*stripe.Price, error) {
	ctx, cancel := context.WithTimeout(ctx, gatewayPriceWait)
	defer cancel()
	return stripeprice.Get(priceID, &stripe.PriceParams{Params: stripe.Params{Context: ctx}})
}

// gatewayInsertOrder writes the order, retrying with a fresh order
// number on the (astronomically unlikely) generated-number collision
// — same contract as service.OrderService.insertOrderWithNumber.
func gatewayInsertOrder(ctx context.Context, s *store.Store, o *model.Order) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = s.CreateOrder(ctx, o); err == nil {
			return nil
		}
		if !isUniqueRef(err) {
			return err
		}
		o.OrderNumber = service.NewOrderNumber()
	}
	return err
}

// StartGatewayCheckout prices one plan sale server-side, records it as
// a PENDING order plus a pending gateway_payments row, and starts the
// payment at the gateway. Returns the redirect/QR the buyer needs and
// the order it belongs to.
//
// Refusals: provider unknown (payment.ErrProviderNotFound) or not
// configured (ErrProviderNotConfigured); non-VND catalogue price
// (ErrCurrencyNotSupported); subscription/trial or recurring-price
// plans (apperr GATEWAY_PLAN_NOT_SUPPORTED — Stripe owns
// subscriptions); everything pricing-shaped as the quote path reports
// it (apperr, forwarded verbatim by the handler).
func StartGatewayCheckout(ctx context.Context, s *store.Store, req *GatewayCheckoutRequest) (*GatewayCheckoutResult, error) {
	if req == nil {
		return nil, apperr.BadRequest("invalid request")
	}
	// Multi-item (cart) shape: the whole flow lives in
	// cart_checkout.go — same refusals, same stamping, N priced lines
	// on one order and one gateway payment (plan §23).
	if len(req.Items) > 0 {
		return cartStartGatewayCheckout(ctx, s, req)
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	p, err := Provider(provider)
	if err != nil {
		return nil, fmt.Errorf("gateway checkout: %w", ErrProviderNotFound)
	}
	if !p.Enabled() {
		return nil, fmt.Errorf("gateway checkout: %s: %w", provider, ErrProviderNotConfigured)
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		return nil, apperr.New(http.StatusBadRequest, "MISSING_CUSTOMER", "customer_email is required")
	}

	plan, err := s.FindPlanByID(ctx, strings.TrimSpace(req.PlanID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.NotFound("PLAN", req.PlanID)
		}
		return nil, fmt.Errorf("find plan: %w", err)
	}
	if !plan.Active {
		return nil, apperr.New(http.StatusNotFound, "PLAN_NOT_FOUND", "this plan is no longer available")
	}
	// One-time purchases only. A subscription or trial plan is
	// recurring billing — Stripe owns that (and the proration,
	// dunning and cancel states that come with it); a VN gateway
	// cannot deliver it, so the sale is refused before anything is
	// created.
	if plan.LicenseType != "perpetual" {
		return nil, apperr.New(http.StatusBadRequest, "GATEWAY_PLAN_NOT_SUPPORTED",
			"this plan is billed as a "+plan.LicenseType+"; gateway payments only sell one-time (perpetual) plans — subscriptions are billed through Stripe")
	}
	// A bounded update period is only sellable while every replica can
	// enforce it — same gate as the Stripe checkout.
	if plan.InitialUpdatesUntil(time.Now()) != nil {
		on, err := s.MaintenanceFeaturesEnabled(ctx)
		if err != nil {
			return nil, fmt.Errorf("read the maintenance switch: %w", err)
		}
		if !on {
			return nil, apperr.New(http.StatusServiceUnavailable, "PLAN_UNAVAILABLE", "this plan is temporarily unavailable")
		}
	}

	if strings.TrimSpace(plan.StripePriceID) == "" {
		return nil, apperr.New(http.StatusServiceUnavailable, "PRICING_UNAVAILABLE", "payment not configured for this plan")
	}
	sp, err := gatewayPlanUnitPrice(ctx, plan.StripePriceID)
	if err != nil || sp == nil {
		slog.Error("gateway checkout: cannot read the plan price",
			"plan_id", plan.ID, "price_id", plan.StripePriceID, "error", err)
		return nil, apperr.New(http.StatusServiceUnavailable, "PRICING_UNAVAILABLE", "cannot read the plan price")
	}
	currency := strings.ToUpper(string(sp.Currency))
	if currency != "VND" {
		return nil, fmt.Errorf("gateway checkout: plan price is %s: %w", currency, ErrCurrencyNotSupported)
	}
	if sp.Type != "one_time" || sp.Recurring != nil {
		return nil, apperr.New(http.StatusBadRequest, "GATEWAY_PLAN_NOT_SUPPORTED",
			"the plan's price is recurring; subscriptions are billed through Stripe")
	}

	// The buyer's coupon is resolved through the coupon table and
	// validated by the coupon engine as part of the pricing; an
	// unknown or unusable code is refused before any sale exists.
	var cpn *coupon.Coupon
	if code := coupon.NormalizeCode(req.CouponCode); code != "" {
		row, err := s.FindCouponByCode(ctx, code)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, apperr.Wrap(http.StatusNotFound, "COUPON_NOT_FOUND", "coupon not found", err)
		}
		if err != nil {
			return nil, fmt.Errorf("look up coupon %s: %w", code, err)
		}
		eng := row.ToEngine()
		cpn = &eng
	}

	// Tax rates are the operator's configuration matched against the
	// buyer's country — never something the request supplies.
	country := strings.ToUpper(strings.TrimSpace(req.Country))
	var rates []tax.Rate
	taxInclusive := req.TaxInclusive != nil && *req.TaxInclusive
	if country != "" {
		rows, err := s.ListActiveTaxRatesForCountry(ctx, country, "")
		if err != nil {
			return nil, fmt.Errorf("look up tax rates: %w", err)
		}
		for _, r := range rows {
			rates = append(rates, r.ToEngine())
		}
		if req.TaxInclusive == nil {
			taxInclusive = allRatesInclusive(rows)
		}
	}

	// Attribution + wholesale pricing (attribution.go) — the same
	// authority the Stripe checkout uses. The buyer email is the
	// wholesale authority; reseller_code/ref only stamp who brought
	// the sale and never change what is charged, except through a
	// valid wholesale override.
	attr, err := resolveCheckoutAttribution(ctx, s, attributionQuery{
		resellerCode: model.NormalizeResellerEmail(req.ResellerCode),
		referralCode: gatewayReferralCode(req.Ref),
		buyerEmail:   email,
	}, plan.ID, currency, sp.UnitAmount)
	if err != nil {
		slog.Error("gateway checkout: cannot resolve partner pricing", "plan_id", plan.ID, "error", err)
		return nil, apperr.New(http.StatusServiceUnavailable, "PRICING_UNAVAILABLE", "cannot resolve partner pricing")
	}
	pricedUnit := sp.UnitAmount
	if attr.wholesale != nil {
		pricedUnit = attr.wholesale.overridePriceMinor
	}

	// Price the sale exactly as the quote endpoint prices it: one line
	// at the catalogue price, the resolved coupon, the matched rates.
	res, err := priceCheckout(ctx, plan, pricedUnit, currency, cpn, rates, taxInclusive)
	if err != nil {
		var ae *apperr.AppError
		if errors.As(err, &ae) {
			return nil, err // COUPON_INVALID etc. — already one code, one status
		}
		slog.Error("gateway checkout: cannot price the sale", "plan_id", plan.ID, "error", err)
		return nil, apperr.New(http.StatusServiceUnavailable, "PRICING_UNAVAILABLE", "cannot price the sale")
	}
	if res.TotalMinor <= 0 {
		return nil, apperr.BadRequest("the order total must be positive")
	}

	// The pending order: the priced snapshot the IPN later settles.
	// Its money, coupon/tax and attribution columns are stamped NOW,
	// exactly like service.OrderService.CreateOrder would — never
	// re-derived from tables that may move before the money arrives.
	o := &model.Order{
		OrderNumber:     service.NewOrderNumber(),
		CustomerEmail:   email,
		Currency:        currency,
		SubtotalMinor:   res.SubtotalMinor,
		DiscountMinor:   res.DiscountMinor,
		TaxMinor:        res.TaxMinor,
		TotalMinor:      res.TotalMinor,
		TaxInclusive:    taxInclusive,
		Status:          model.OrderStatusPending,
		PaymentProvider: provider,
	}
	if res.AppliedCoupon != nil {
		o.CouponCode = coupon.NormalizeCode(res.AppliedCoupon.Code)
		o.CouponType = string(res.AppliedCoupon.Type)
		switch res.AppliedCoupon.Type {
		case coupon.TypePercentOff:
			o.CouponValueBPS = res.AppliedCoupon.Value
		case coupon.TypeFixedAmountOff:
			o.CouponValueMinor = res.AppliedCoupon.Value
		}
	}
	o.TaxJurisdiction, o.TaxBasisPoints = taxJurisdictionLabel(rates), taxBasisPoints(rates)
	for _, lr := range res.LineResults {
		o.Items = append(o.Items, &model.OrderItem{
			SKU:               lr.SKU,
			ProductID:         lr.ProductID,
			PlanID:            lr.PlanID,
			Description:       lr.Description,
			Quantity:          lr.Quantity,
			UnitAmountMinor:   lr.UnitAmountMinor,
			LineSubtotalMinor: lr.LineSubtotalMinor,
			LineDiscountMinor: lr.LineDiscountMinor,
			LineTaxMinor:      lr.LineTaxMinor,
			LineTotalMinor:    lr.LineTotalMinor,
		})
	}
	o.ResellerID, o.ResellerEmail = attr.resellerID, attr.resellerEmail
	o.ReferralCode, o.AffiliateID = attr.referralCode, attr.affiliateID

	if err := gatewayInsertOrder(ctx, s, o); err != nil {
		return nil, fmt.Errorf("create order: %w", err)
	}

	// Start the payment. AmountMinor is authoritative — the IPN path
	// re-checks it against the stored row and never fulfils a mismatch.
	cr := CreatePaymentRequest{
		OrderID:     o.OrderNumber,
		AmountMinor: res.TotalMinor,
		Currency:    currency,
		Description: o.OrderNumber,
		BuyerEmail:  email,
		BuyerName:   o.CustomerName,
		ReturnURL:   gatewayReturnURL(req.ReturnURL, o.OrderNumber),
		CancelURL:   req.CancelURL,
	}
	if req.WebhookURL != "" {
		cr.Extra = map[string]string{"webhook_url": req.WebhookURL}
	}
	pres, err := p.CreatePayment(ctx, cr)
	if err != nil {
		slog.Error("gateway checkout: provider refused to start the payment",
			"provider", provider, "order", o.OrderNumber, "error", err)
		return nil, apperr.Wrap(http.StatusBadGateway, "PAYMENT_GATEWAY_ERROR",
			"the payment provider could not start the payment", err)
	}
	if pres == nil || strings.TrimSpace(pres.ProviderRef) == "" || pres.PayURL == "" {
		return nil, apperr.New(http.StatusBadGateway, "PAYMENT_GATEWAY_ERROR",
			"the payment provider returned no payment handle")
	}

	gp := &store.GatewayPayment{
		OrderID:     o.ID,
		Provider:    provider,
		ProviderRef: pres.ProviderRef,
		OrderKey:    o.OrderNumber,
		AmountMinor: res.TotalMinor,
		Currency:    currency,
		Status:      string(StatusPending),
	}
	if err := s.CreateGatewayPayment(ctx, gp); err != nil {
		// The order is in and the gateway holds a payment for it; the
		// IPN's order-key fallback can still settle it, but do not
		// leave the buyer mid-air silently: try to void the link and
		// surface the failure either way.
		slog.Error("gateway checkout: failed to record the payment row",
			"provider", provider, "order", o.OrderNumber, "ref", pres.ProviderRef, "error", err)
		if verr := p.VoidPayment(ctx, pres.ProviderRef, "payment record could not be created"); verr != nil {
			slog.Warn("gateway checkout: void after record failure also failed",
				"provider", provider, "ref", pres.ProviderRef, "error", verr)
		}
		return nil, fmt.Errorf("record gateway payment: %w", err)
	}

	slog.Info("gateway checkout started",
		"provider", provider, "order", o.OrderNumber, "ref", pres.ProviderRef,
		"amount_minor", res.TotalMinor, "currency", currency)
	return &GatewayCheckoutResult{Payment: pres, Order: o}, nil
}

// gatewayReferralCode folds and sanity-checks a referral handle the
// way attributionFromContext does for the Stripe link: junk that folds
// to nothing is absent, never a sentinel.
func gatewayReferralCode(raw string) string {
	code := model.NormalizeReferralCode(raw)
	if !model.ValidReferralCode(code) {
		return ""
	}
	return code
}

// gatewayReturnURL appends the order number to the browser return
// page so the page can poll GET /checkout/gateway-pay/status?order=…
// without trusting anything the gateway puts in the URL.
func gatewayReturnURL(base, orderNumber string) string {
	if base == "" {
		return ""
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "order=" + url.QueryEscape(orderNumber)
}

// ─── IPN fulfilment ───

// gatewayLicenseID derives the licence id from the payment row, so a
// retried fulfilment inserts the SAME id and the unique violation
// reads as "already created" instead of minting a second licence.
// Deterministic convergence is what makes re-running safe even where
// the two fulfilment gates (below) were bypassed by a crash.
func gatewayLicenseID(row *store.GatewayPayment) string {
	return "licgw-" + row.Provider + "-" + strconv.FormatInt(row.ID, 10)
}

// gatewayFirstNonEmpty returns the first argument that is not "".
func gatewayFirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// gatewayOrderProductID is the product an order's lifecycle events fan
// out to: the product of its first line item that names one — the same
// rule the handler-side order events follow (handler.orderProductID;
// the packages cannot share it without an import cycle). An order with
// no product line has nowhere to send them.
func gatewayOrderProductID(o *model.Order) string {
	for _, it := range o.Items {
		if it.ProductID != "" {
			return it.ProductID
		}
	}
	return ""
}

// gatewayFindPayment locates the payment row an authenticated event is
// about: first by the gateway's own handle, then — all three VN
// gateways echo OUR order number — by (provider, order_key). The last
// resort heals the checkout that died between the gateway call and the
// row insert: the pending order exists and matches the event, so the
// row is created from it.
func gatewayFindPayment(ctx context.Context, s *store.Store, ev *WebhookEvent) (*store.GatewayPayment, error) {
	ref := strings.TrimSpace(ev.ProviderRef)
	if ref != "" {
		row, err := s.GetGatewayPaymentByRef(ctx, ev.Provider, ref)
		if err == nil {
			return row, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	key := strings.TrimSpace(ev.OrderID)
	if key != "" {
		row, err := s.GetGatewayPaymentByOrderKey(ctx, ev.Provider, key)
		if err == nil {
			return row, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}

		order, err := s.FindOrderByNumber(ctx, key)
		if err != nil || order == nil {
			return nil, nil
		}
		if order.PaymentProvider != ev.Provider || order.Status != model.OrderStatusPending {
			return nil, nil
		}
		if ev.AmountMinor != order.TotalMinor {
			// Do not invent a row for an event that does not match the
			// order it names — fall through to "not found".
			return nil, nil
		}
		row = &store.GatewayPayment{
			OrderID:     order.ID,
			Provider:    ev.Provider,
			ProviderRef: gatewayFirstNonEmpty(ref, key),
			OrderKey:    key,
			AmountMinor: order.TotalMinor,
			Currency:    order.Currency,
			Status:      string(StatusPending),
		}
		if err := s.CreateGatewayPayment(ctx, row); err == nil {
			slog.Warn("gateway fulfil: payment row missing, healed from the pending order",
				"provider", ev.Provider, "order", key, "ref", row.ProviderRef)
			return row, nil
		} else if !isUniqueRef(err) {
			return nil, err
		}
		// A concurrent delivery created it: re-read.
		if ref != "" {
			if row, err := s.GetGatewayPaymentByRef(ctx, ev.Provider, ref); err == nil {
				return row, nil
			}
		}
		return s.GetGatewayPaymentByOrderKey(ctx, ev.Provider, key)
	}
	return nil, nil
}

// GatewayFulfil settles one authenticated gateway callback. It is
// idempotent and safe under concurrent deliveries:
//
//   - the amount (and currency) must match the stored row exactly — a
//     mismatch returns ErrAmountMismatch and NOTHING is fulfilled;
//   - a duplicate of an already-settled payment is a no-op (the order
//     is paid and linked to its licence);
//   - two simultaneous IPNs fulfil exactly once: the winner is the
//     one whose conditional UPDATE flips the row out of 'pending';
//   - a row settled by a run that crashed before finishing is healed
//     by a later delivery under a processed_events claim.
//
// On success the side effects mirror fulfilCheckout + recordOrder:
// order → paid (linked to the licence), licence created, ledger
// invoice, partner ledgers, user link, licence email, audit line and
// the merchant "license.created" webhook.
func GatewayFulfil(ctx context.Context, s *store.Store, deps *GatewayFulfilDeps, ev *WebhookEvent) error {
	if ev == nil {
		return ErrWebhookPayloadMalformed
	}
	if ev.Status != StatusSucceeded {
		// A failure notice (failed/cancelled/expired/refunded): record
		// it on a still-pending row and stop. Refund automation is not
		// part of this flow — a refund IPN is logged and recorded, the
		// money-back bookkeeping stays with the admin refund path.
		if ev.Status == StatusFailed || ev.Status == StatusCancelled ||
			ev.Status == StatusExpired || ev.Status == StatusRefunded {
			if row, err := gatewayFindPayment(ctx, s, ev); err == nil && row != nil && row.Status == string(StatusPending) {
				if uerr := s.UpdateGatewayPaymentStatus(ctx, row.ID, string(ev.Status), ev.TransID, ev.Raw); uerr != nil {
					return fmt.Errorf("record gateway payment status: %w", uerr)
				}
				slog.Info("gateway payment closed without settlement",
					"provider", ev.Provider, "ref", row.ProviderRef, "status", ev.Status)
				// order.failed (plan §35): the sale died on the wire —
				// failed, cancelled or expired before settlement. The row
				// just left pending, so this fires once per payment. A
				// refund notice above sends nothing: the money went BACK,
				// which is a different event and the admin refund path's
				// to send. Best-effort: the payment row is already closed.
				if ev.Status != StatusRefunded && deps != nil && deps.Webhooks != nil {
					payload := map[string]any{
						"order_id": row.OrderID, "payment_provider": row.Provider,
						"provider_ref": row.ProviderRef, "reason": string(ev.Status),
						"amount_minor": row.AmountMinor, "currency": row.Currency,
					}
					if o, oerr := s.FindOrderByID(ctx, row.OrderID); oerr == nil && o != nil {
						payload["order_number"] = o.OrderNumber
						if pid := gatewayOrderProductID(o); pid != "" {
							deps.Webhooks.Dispatch(ctx, pid, model.EventOrderFailed, payload)
						}
					}
				}
			}
		}
		return nil
	}

	row, err := gatewayFindPayment(ctx, s, ev)
	if err != nil {
		return err
	}
	if row == nil {
		return fmt.Errorf("%w: %s %s", ErrGatewayPaymentNotFound, ev.Provider, gatewayFirstNonEmpty(ev.ProviderRef, ev.OrderID))
	}
	// The stored amount is what the checkout priced; the event must
	// report exactly it. Nothing is fulfilled on a mismatch.
	if ev.AmountMinor != row.AmountMinor {
		return fmt.Errorf("%w: event %d != stored %d", ErrAmountMismatch, ev.AmountMinor, row.AmountMinor)
	}
	if ev.Currency != "" && !strings.EqualFold(ev.Currency, row.Currency) {
		return fmt.Errorf("%w: event currency %s != stored %s", ErrAmountMismatch, ev.Currency, row.Currency)
	}

	order, err := s.FindOrderByID(ctx, row.OrderID)
	if err != nil {
		return fmt.Errorf("find order for gateway payment %d: %w", row.ID, err)
	}
	if order.LicenseID != "" && order.Status == model.OrderStatusPaid {
		return nil // replay: already fulfilled
	}
	if order.Status != model.OrderStatusPending {
		return fmt.Errorf("%w: %s", ErrGatewayOrderNotPending, order.Status)
	}

	heal := false
	won := false
	switch row.Status {
	case string(StatusPending):
		// The settlement claim: exactly one of two simultaneous IPNs
		// gets past this conditional UPDATE.
		won, err = s.UpdateGatewayPaymentStatusIfPending(ctx, row.ID, string(StatusSucceeded), ev.TransID, ev.Raw)
		if err != nil {
			return fmt.Errorf("claim gateway payment %d: %w", row.ID, err)
		}
		if !won {
			return ErrGatewayFulfilInFlight
		}
	default:
		// The row settled (or was closed) earlier but the order never
		// became paid — a run crashed mid-fulfilment, or a closed link
		// was actually paid. Money is authoritative: record success and
		// heal, guarded by a processed_events claim so two concurrent
		// healers cannot both run the side effects.
		heal = true
		if uerr := s.UpdateGatewayPaymentStatus(ctx, row.ID, string(StatusSucceeded), ev.TransID, ev.Raw); uerr != nil {
			return fmt.Errorf("record gateway payment status: %w", uerr)
		}
		claimID := row.Provider + ":" + row.ProviderRef
		claimed, cerr := s.ClaimProcessedEvent(ctx, gatewayFulfilClaimProvider, claimID)
		if cerr != nil {
			return fmt.Errorf("claim heal: %w", cerr)
		}
		if !claimed {
			released, rerr := s.ReleaseStaleProcessedEvent(ctx, gatewayFulfilClaimProvider, claimID, gatewayFulfilStaleAge)
			if rerr != nil {
				return fmt.Errorf("release stale heal claim: %w", rerr)
			}
			if !released {
				return ErrGatewayFulfilInFlight
			}
			claimed, cerr = s.ClaimProcessedEvent(ctx, gatewayFulfilClaimProvider, claimID)
			if cerr != nil {
				return fmt.Errorf("claim heal: %w", cerr)
			}
			if !claimed {
				return ErrGatewayFulfilInFlight
			}
		}
		defer func() {
			// The order's paid state is the durable done marker; the
			// claim only exists to serialise healers, so it goes either
			// way — on failure a later delivery must be able to heal.
			_ = s.DeleteProcessedEvent(ctx, gatewayFulfilClaimProvider, claimID)
		}()
	}

	if err := gatewayFulfilOrder(ctx, s, deps, row, order, ev); err != nil {
		if won {
			// Release the settlement claim so the gateway's retry can
			// take it again — the money is in, the licence must come.
			if uerr := s.UpdateGatewayPaymentStatus(ctx, row.ID, string(StatusPending), "", nil); uerr != nil {
				slog.Error("gateway fulfil: failed to release the settlement claim",
					"provider", row.Provider, "ref", row.ProviderRef, "error", uerr)
			}
		}
		return err
	}
	if heal {
		slog.Info("gateway fulfil: healed a settlement whose fulfilment had stalled",
			"provider", row.Provider, "ref", row.ProviderRef, "order", order.OrderNumber)
	}
	return nil
}

// gatewayFulfilOrder performs the fulfilment side effects, mirroring
// StripeHandler.fulfillCheckout + recordOrder for a one-off purchase:
// licence, paid order, invoice, partner ledgers, user link, licence
// email, audit and merchant webhook. Idempotent by construction — the
// licence id is derived from the payment row, so a re-run collides on
// the primary key and adopts the licence it finds instead of minting
// a second one.
func gatewayFulfilOrder(ctx context.Context, s *store.Store, deps *GatewayFulfilDeps, row *store.GatewayPayment, order *model.Order, ev *WebhookEvent) error {
	if len(order.Items) == 0 {
		slog.Error("gateway fulfil: order has no line items; sale cannot be fulfilled",
			"provider", row.Provider, "order", order.OrderNumber)
		return fmt.Errorf("%w: order %s has no line items", ErrGatewayUnfulfillable, order.OrderNumber)
	}
	planID := order.Items[0].PlanID
	plan, err := s.FindPlanByID(ctx, planID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			slog.Error("gateway fulfil: plan is gone; sale cannot be fulfilled",
				"provider", row.Provider, "order", order.OrderNumber, "plan_id", planID)
			return fmt.Errorf("%w: plan %s no longer exists", ErrGatewayUnfulfillable, planID)
		}
		return fmt.Errorf("find plan %s: %w", planID, err)
	}
	email := strings.TrimSpace(order.CustomerEmail)
	if email == "" {
		slog.Error("gateway fulfil: order carries no customer email; sale cannot be fulfilled",
			"provider", row.Provider, "order", order.OrderNumber)
		return fmt.Errorf("%w: order %s has no customer email", ErrGatewayUnfulfillable, order.OrderNumber)
	}

	now := time.Now()
	paidAt := ev.PaidAt
	if paidAt.IsZero() {
		paidAt = now
	}

	// Only perpetual plans reach here (the checkout refuses the rest),
	// so the licence is active with the plan's update period.
	lic := &model.License{
		ID:              gatewayLicenseID(row),
		ProductID:       plan.ProductID,
		PlanID:          plan.ID,
		Email:           email,
		LicenseKey:      license.GenerateKey(""),
		Status:          model.StatusActive,
		PaymentProvider: row.Provider,
		// The gateway's own identifiers ride the generic external
		// payment columns (their names are Stripe-flavoured for
		// historical reasons; the docs above them describe exactly
		// this job): fulfilment idempotency and refund matching.
		StripePaymentIntentID:   gatewayFirstNonEmpty(ev.TransID, row.ProviderRef),
		StripeCheckoutSessionID: row.Provider + ":" + row.ProviderRef,
	}
	lic.UpdatesUntil, lic.UpdatesTermsSet = model.UpdatesUntilFor(plan.LicenseType, plan.UpdatesDays, now), true

	_ = s.UpsertUser(ctx, &model.User{Email: email})

	err = s.CreateLicenseWithSubscription(ctx, lic, plan)
	if err != nil {
		if isUniqueRef(err) {
			// A previous attempt of THIS fulfilment already wrote the
			// licence (the deterministic id collided). Adopt it — it
			// holds the key that was (or will be) delivered; the key
			// generated above must never leave this process.
			if existing, lerr := s.FindLicenseByID(ctx, lic.ID); lerr == nil && existing != nil {
				lic = existing
			} else {
				return fmt.Errorf("adopt existing license %s: %w", lic.ID, lerr)
			}
		} else {
			// Includes store.ErrPlanChanged / ErrUpdatePeriodNotEnforceable:
			// transient — the retry rebuilds from the plan as it reads then,
			// exactly like the Stripe fulfilment.
			slog.Error("gateway fulfil: failed to create license",
				"provider", row.Provider, "order", order.OrderNumber, "error", err)
			return fmt.Errorf("create license: %w", err)
		}
	}

	// The pending order becomes the paid ledger entry. Conditional on
	// pending: the flip happens exactly once.
	flipped, err := s.MarkGatewayOrderPaid(ctx, order.ID, lic.ID, row.ProviderRef,
		"gateway:"+row.Provider+":"+row.ProviderRef, paidAt)
	if err != nil {
		return fmt.Errorf("mark order paid: %w", err)
	}
	if !flipped {
		// A concurrent fulfilment settled the order. Its hooks (and
		// ours converge on the same licence id) have run or will run.
		slog.Info("gateway fulfil: order settled concurrently", "order", order.OrderNumber)
		return nil
	}

	// ── Best-effort from here (mirror fulfilCheckout's tail and
	// recordOrder's doctrine): a bookkeeping hiccup is logged and never
	// fails fulfilment — the licence must never be held up by it. ──
	if u, uerr := s.FindUserByEmail(ctx, email); uerr == nil && u != nil {
		_ = s.UpdateLicenseUser(ctx, lic.ID, u.ID)
	}

	productName, downloadURL := "Your Software", ""
	if p, perr := s.FindProductByID(ctx, plan.ProductID); perr == nil && p != nil {
		if p.Name != "" {
			productName = p.Name
		}
		downloadURL = p.DownloadURL
	}

	subject := "Your license for " + productName
	var body string
	if deps != nil && deps.Email != nil {
		subject, body = deps.Email.RenderLicenseCreated(productName, plan.Name, s.DecryptLicenseKey(lic), downloadURL)
	} else {
		displayKey := s.DecryptLicenseKey(lic)
		body = fmt.Sprintf(`<!DOCTYPE html>
<html><body style="font-family: -apple-system, sans-serif; max-width: 600px; margin: 0 auto; padding: 20px;">
<h2 style="color: #111;">Your %s License</h2>
<p>Your <strong>%s</strong> license is ready.</p>
<div style="background: #f4f4f5; border-radius: 8px; padding: 16px; margin: 16px 0; font-family: monospace; font-size: 18px; text-align: center; letter-spacing: 2px;">%s</div>
<p style="color: #666; font-size: 14px;">Keep this key safe. You'll need it to activate your software.</p>
</body></html>`, html.EscapeString(productName), html.EscapeString(plan.Name), html.EscapeString(displayKey))
	}
	_ = s.EnqueueEmail(ctx, email, subject, body)

	s.Audit(ctx, &model.AuditLog{
		Entity: "license", EntityID: lic.ID, Action: "created",
		ActorType: "webhook",
		Changes:   map[string]any{"provider": row.Provider, "email": email, "plan": plan.Name},
	})

	if deps != nil && deps.Webhooks != nil {
		deps.Webhooks.Dispatch(ctx, lic.ProductID, "license.created", map[string]any{
			"license_id": lic.ID, "email": lic.Email, "plan_id": lic.PlanID,
		})
	}

	// The partner ledgers (reseller commission + affiliate conversion)
	// on the stamped attribution — idempotent at the store, best-effort
	// here, exactly like recordOrder runs them.
	recordAttributionEffects(ctx, s, order)

	// The invoice copies the order's money — a billing document, not a
	// second calculation (recordOrder's rule).
	inv := &model.Invoice{
		OrderID:       order.ID,
		InvoiceNumber: service.NewInvoiceNumber(),
		Status:        model.InvoiceStatusPaid,
		Currency:      order.Currency,
		SubtotalMinor: order.SubtotalMinor,
		DiscountMinor: order.DiscountMinor,
		TaxMinor:      order.TaxMinor,
		TotalMinor:    order.TotalMinor,
		IssuedAt:      &paidAt,
		PaidAt:        &paidAt,
	}
	if ierr := s.CreateInvoice(ctx, inv); ierr != nil {
		slog.Error("gateway fulfil: failed to record invoice",
			"order", order.OrderNumber, "error", ierr)
	} else if deps != nil && deps.Webhooks != nil {
		// invoice.paid (plan §35), the gateway twin of recordOrder's
		// dispatch: the money settled and the invoice is paid. Only
		// when the invoice row actually landed — above it is merely
		// logged — and best-effort: a receiver being down never re-runs
		// the sale.
		deps.Webhooks.Dispatch(ctx, plan.ProductID, model.EventInvoicePaid, map[string]any{
			"invoice_id": inv.ID, "order_id": order.ID, "order_number": order.OrderNumber,
			"invoice_number": inv.InvoiceNumber, "amount_minor": inv.TotalMinor,
			"currency": inv.Currency, "payment_provider": row.Provider,
		})
	}

	slog.Info("gateway payment fulfilled",
		"provider", row.Provider, "ref", row.ProviderRef, "order", order.OrderNumber,
		"license_id", lic.ID, "amount_minor", row.AmountMinor, "currency", row.Currency)
	return nil
}
