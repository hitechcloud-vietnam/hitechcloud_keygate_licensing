// Multi-item (cart) checkout, backend half (plan §23) + the
// currency-aware unit-price resolution the quote engine gained
// (plan §53).
//
// Two flows, one pricing core:
//
//	cartStartGatewayCheckout   N items, ONE order with N order
//	                           lines, ONE gateway payment (VND
//	                           one-off gateways); wired from
//	                           StartGatewayCheckout when the request
//	                           carries Items
//	CreateCartCheckoutSession  N items, ONE Stripe Checkout Session
//	                           with SERVER-PRICED lines (the request
//	                           never carries an amount — there is no
//	                           field for one)
//
// Prices are resolved per plan and currency: a plan_prices row for
// the requested currency wins, then the plan's default row when no
// currency was asked for, else the existing Stripe-price path
// (Plan.StripePriceID — the catalogue's long-standing source of
// truth). Client amounts are ignored — cartPriceSale never reads one.
//
// Money discipline (plan §51): int64 minor units end to end, no
// floats anywhere near a total. The money engines (coupon
// largest-remainder allocation, per-line tax) keep every line summing
// exactly to the totals.
//
// Wholesale note (Phase 7 attribution): a multi-item cart stamps who
// brought the sale (reseller/referral/affiliate) but does NOT apply a
// reseller wholesale override — the override is defined per plan
// price and a cart priced at mixed overrides would need per-line cuts
// Stripe's one-coupon model cannot express. Single-item carts keep
// the full behaviour.
package payment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	stripe "github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/checkout/session"
	stripecoupon "github.com/stripe/stripe-go/v82/coupon"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/coupon"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/tax"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
)

// Cart limits: a cart is a human's shopping list, not a bulk order.
const (
	// cartMaxLines caps how many distinct plans one cart may carry.
	cartMaxLines = 50
	// cartMaxQuantity caps one line's quantity.
	cartMaxQuantity = 999
)

// CartCheckoutItem is one line of a cart: a plan and how many of it.
// Deliberately has NO amount field — the sale is always priced
// server-side (cartPriceSale).
type CartCheckoutItem struct {
	PlanID   string `json:"plan_id"`
	Quantity int64  `json:"quantity"`
}

// CartStore is the slice of store.Store the cart flows need: the
// plan catalogue, the §53 price list, the coupon and tax tables and
// the maintenance switch. The real wiring passes *store.Store; tests
// substitute a fake so the pricing core runs without a database.
type CartStore interface {
	FindPlanByID(ctx context.Context, id string) (*model.Plan, error)
	ListPlanPrices(ctx context.Context, planID string) ([]*store.PlanPrice, error)
	MaintenanceFeaturesEnabled(ctx context.Context) (bool, error)
	FindCouponByCode(ctx context.Context, code string) (*model.Coupon, error)
	ListActiveTaxRatesForCountry(ctx context.Context, country, region string) ([]*model.TaxRate, error)
}

var _ CartStore = (*store.Store)(nil)

// cartNormalizeItems validates a cart and folds it to its canonical
// shape: at least one line, no more than cartMaxLines, every plan id
// present, every quantity in 1..cartMaxQuantity, and no plan twice —
// a cart line IS (plan, quantity), so two lines of one plan are one
// line with the quantities added, which is refused here rather than
// silently merged (the client's intent should be visible).
func cartNormalizeItems(items []CartCheckoutItem) ([]CartCheckoutItem, error) {
	if len(items) == 0 {
		return nil, apperr.BadRequest("items must not be empty")
	}
	if len(items) > cartMaxLines {
		return nil, apperr.BadRequest(fmt.Sprintf("a cart may carry at most %d lines", cartMaxLines))
	}
	out := make([]CartCheckoutItem, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, it := range items {
		id := strings.TrimSpace(it.PlanID)
		if id == "" {
			return nil, apperr.BadRequest("each item needs a plan_id")
		}
		if it.Quantity <= 0 || it.Quantity > cartMaxQuantity {
			return nil, apperr.BadRequest(fmt.Sprintf("quantity must be between 1 and %d", cartMaxQuantity))
		}
		if _, dup := seen[id]; dup {
			return nil, apperr.BadRequest("a cart may carry each plan only once")
		}
		seen[id] = struct{}{}
		out = append(out, CartCheckoutItem{PlanID: id, Quantity: it.Quantity})
	}
	return out, nil
}

// cartUnitPrice resolves one plan's per-unit price in money minor
// units (plan §53): a plan_prices row for the requested currency,
// else the plan's default row when no currency was asked for, else
// the existing Stripe-price path. The returned currency is the one
// the amount is expressed in — the sale's currency comes from the
// first line and every later line must agree.
func cartUnitPrice(ctx context.Context, s CartStore, plan *model.Plan, wantCurrency string) (int64, string, error) {
	rows, err := s.ListPlanPrices(ctx, plan.ID)
	if err != nil {
		return 0, "", fmt.Errorf("read plan prices: %w", err)
	}
	if wantCurrency != "" {
		for _, r := range rows {
			if r.Currency == wantCurrency {
				return r.AmountMinor, r.Currency, nil
			}
		}
		// No row for the requested currency: fall through to the
		// Stripe price path (the pinned "else existing Stripe-price
		// path" rule).
	} else {
		for _, r := range rows {
			if r.IsDefault {
				return r.AmountMinor, r.Currency, nil
			}
		}
	}
	if strings.TrimSpace(plan.StripePriceID) == "" {
		return 0, "", apperr.New(503, "PRICING_UNAVAILABLE", "payment not configured for this plan")
	}
	sp, err := gatewayPlanUnitPrice(ctx, plan.StripePriceID)
	if err != nil || sp == nil {
		slog.Error("cart: cannot read the plan price", "plan_id", plan.ID, "price_id", plan.StripePriceID, "error", err)
		return 0, "", apperr.New(503, "PRICING_UNAVAILABLE", "cannot read the plan price")
	}
	return sp.UnitAmount, strings.ToUpper(string(sp.Currency)), nil
}

// cartPriceSale prices a whole cart exactly the way the single-plan
// quote prices one sale: server-side unit prices, the resolved
// coupon, the matched tax rates — through the same
// service.OrderService.Calculate the quote and checkout paths use, so
// every line allocates discount and tax with the same
// largest-remainder rules and the lines sum exactly to the totals.
//
// wantCurrency asks for a currency ("" lets the plans' defaults
// decide). A cart whose lines resolve to different currencies is
// refused — one order has one currency.
func cartPriceSale(ctx context.Context, s CartStore, items []CartCheckoutItem, wantCurrency string, cpn *coupon.Coupon, rates []tax.Rate, taxInclusive bool) ([]service.Line, *service.CalcResult, string, error) {
	items, err := cartNormalizeItems(items)
	if err != nil {
		return nil, nil, "", err
	}
	wantCurrency = strings.ToUpper(strings.TrimSpace(wantCurrency))
	lines := make([]service.Line, 0, len(items))
	saleCurrency := wantCurrency
	for _, it := range items {
		plan, err := s.FindPlanByID(ctx, it.PlanID)
		if err != nil || plan == nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, nil, "", apperr.NotFound("PLAN", it.PlanID)
			}
			if err != nil {
				return nil, nil, "", fmt.Errorf("find plan: %w", err)
			}
			return nil, nil, "", apperr.NotFound("PLAN", it.PlanID)
		}
		if !plan.Active {
			return nil, nil, "", apperr.New(404, "PLAN_NOT_FOUND", "this plan is no longer available")
		}
		// A bounded update period is only sellable while every
		// replica can enforce it — same gate as every other sale.
		if plan.InitialUpdatesUntil(time.Now()) != nil {
			on, err := s.MaintenanceFeaturesEnabled(ctx)
			if err != nil {
				return nil, nil, "", fmt.Errorf("read the maintenance switch: %w", err)
			}
			if !on {
				return nil, nil, "", apperr.New(503, "PLAN_UNAVAILABLE", "this plan is temporarily unavailable")
			}
		}
		unit, currency, err := cartUnitPrice(ctx, s, plan, wantCurrency)
		if err != nil {
			return nil, nil, "", err
		}
		if saleCurrency == "" {
			saleCurrency = currency
		} else if currency != saleCurrency {
			return nil, nil, "", apperr.New(400, "CURRENCY_NOT_SUPPORTED",
				"every line of a cart must be priced in one currency")
		}
		lines = append(lines, service.Line{
			SKU:             plan.Slug,
			ProductID:       plan.ProductID,
			PlanID:          plan.ID,
			Description:     plan.Name,
			Quantity:        it.Quantity,
			UnitAmountMinor: unit,
		})
	}

	res, err := service.NewOrderService(nil, nil).Calculate(ctx, service.CalcInput{
		Lines:        lines,
		Currency:     saleCurrency,
		Coupon:       cpn,
		TaxRates:     rates,
		TaxInclusive: taxInclusive,
	})
	if err != nil {
		var ae *apperr.AppError
		if errors.As(err, &ae) {
			return nil, nil, "", err // COUPON_INVALID etc. — one code, one status
		}
		return nil, nil, "", err
	}
	return lines, res, saleCurrency, nil
}

// ─── Gateway (VN one-off) cart checkout ───
//
// cartStartGatewayCheckout is the multi-item half of
// StartGatewayCheckout: one pending order carrying N order lines, one
// gateway payment for the total, settled by the same IPN fulfilment.
// It mirrors the single-plan flow's refusals and stamping exactly —
// the only difference is that the priced sale has N lines and the
// wholesale override is not applied (see the package comment).
func cartStartGatewayCheckout(ctx context.Context, s *store.Store, req *GatewayCheckoutRequest) (*GatewayCheckoutResult, error) {
	if req == nil {
		return nil, apperr.BadRequest("invalid request")
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
		return nil, apperr.New(400, "MISSING_CUSTOMER", "customer_email is required")
	}
	items, err := cartNormalizeItems(req.Items)
	if err != nil {
		return nil, err
	}

	// One-time purchases only — Stripe owns subscriptions (same gate
	// as the single-plan flow, now per line).
	for _, it := range items {
		plan, err := s.FindPlanByID(ctx, it.PlanID)
		if err != nil || plan == nil {
			return nil, apperr.NotFound("PLAN", it.PlanID)
		}
		if plan.LicenseType != "perpetual" {
			return nil, apperr.New(400, "GATEWAY_PLAN_NOT_SUPPORTED",
				"this plan is billed as a "+plan.LicenseType+"; gateway payments only sell one-time (perpetual) plans — subscriptions are billed through Stripe")
		}
	}

	// The buyer's coupon, resolved through the coupon table exactly
	// as the quote resolves it; an unknown code is its own refusal.
	var cpn *coupon.Coupon
	if code := coupon.NormalizeCode(req.CouponCode); code != "" {
		row, err := s.FindCouponByCode(ctx, code)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, apperr.Wrap(404, "COUPON_NOT_FOUND", "coupon not found", err)
			}
			return nil, fmt.Errorf("look up coupon %s: %w", code, err)
		}
		eng := row.ToEngine()
		cpn = &eng
	}

	// Tax rates are the operator's configuration matched against the
	// buyer's country.
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

	// Price the whole cart server-side. Gateways settle VND only.
	_, res, currency, err := cartPriceSale(ctx, s, items, "VND", cpn, rates, taxInclusive)
	if err != nil {
		var ae *apperr.AppError
		if errors.As(err, &ae) {
			return nil, err
		}
		slog.Error("cart gateway checkout: cannot price the sale", "error", err)
		return nil, apperr.New(503, "PRICING_UNAVAILABLE", "cannot price the sale")
	}
	if currency != "VND" {
		return nil, fmt.Errorf("gateway checkout: cart priced in %s: %w", currency, ErrCurrencyNotSupported)
	}
	if res.TotalMinor <= 0 {
		return nil, apperr.BadRequest("the order total must be positive")
	}

	// Attribution stamps (attribution.go). A multi-item cart records
	// who brought the sale but does not apply a wholesale override —
	// see the package comment.
	attr, err := resolveCheckoutAttribution(ctx, s, attributionQuery{
		resellerCode: model.NormalizeResellerEmail(req.ResellerCode),
		referralCode: gatewayReferralCode(req.Ref),
		buyerEmail:   email,
	}, items[0].PlanID, currency, 0)
	if err != nil {
		slog.Error("cart gateway checkout: cannot resolve partner pricing", "error", err)
		return nil, apperr.New(503, "PRICING_UNAVAILABLE", "cannot resolve partner pricing")
	}

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

	// ONE gateway payment for the cart total.
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
		slog.Error("cart gateway checkout: provider refused to start the payment",
			"provider", provider, "order", o.OrderNumber, "error", err)
		return nil, apperr.Wrap(502, "PAYMENT_GATEWAY_ERROR",
			"the payment provider could not start the payment", err)
	}
	if pres == nil || strings.TrimSpace(pres.ProviderRef) == "" || pres.PayURL == "" {
		return nil, apperr.New(502, "PAYMENT_GATEWAY_ERROR",
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
		slog.Error("cart gateway checkout: failed to record the payment row",
			"provider", provider, "order", o.OrderNumber, "ref", pres.ProviderRef, "error", err)
		if verr := p.VoidPayment(ctx, pres.ProviderRef, "payment record could not be created"); verr != nil {
			slog.Warn("cart gateway checkout: void after record failure also failed",
				"provider", provider, "ref", pres.ProviderRef, "error", verr)
		}
		return nil, fmt.Errorf("record gateway payment: %w", err)
	}

	slog.Info("cart gateway checkout started",
		"provider", provider, "order", o.OrderNumber, "ref", pres.ProviderRef,
		"amount_minor", res.TotalMinor, "currency", currency, "lines", len(items))
	return &GatewayCheckoutResult{Payment: pres, Order: o}, nil
}

// ─── Stripe cart checkout ───
//
// CartCheckoutRequest is one buyer's attempt to pay a cart through
// Stripe. Amounts are NEVER taken from it — every line is priced
// server-side (cartPriceSale) and written to the session as
// server-priced PriceData lines.
type CartCheckoutRequest struct {
	Items        []CartCheckoutItem
	CouponCode   string
	Country      string
	Email        string
	Currency     string // optional price preference ("" = plan defaults)
	TaxInclusive *bool

	// Attribution inputs (attribution.go), query-parameter shaped.
	ResellerCode string
	Ref          string

	// Where the buyer lands afterwards. Built by the handler from the
	// install's BaseURL; {CHECKOUT_SESSION_ID} is substituted by
	// Stripe on SuccessURL.
	SuccessURL string
	CancelURL  string
}

// CartCheckoutSession is the redirect the HTTP layer turns into
// {checkout_url, checkout_id}. CheckoutID is the Stripe Checkout
// Session id.
type CartCheckoutSession struct {
	URL        string
	CheckoutID string
	TotalMinor int64
	Currency   string
	LineCount  int
}

// CreateCartCheckoutSession prices a cart server-side and creates ONE
// Stripe Checkout Session for it — one order's worth of money, N
// lines on the receipt. The session-creation pattern is the one
// CheckoutByPlan uses (payment mode for one-time carts, subscription
// mode for recurring ones, the computed discount as a one-time fixed
// Stripe coupon, exclusive tax as its own line, money facts stamped
// into metadata) — mirrored here for N lines.
//
// KNOWN LIMIT, documented: fulfilment of a MULTI-PLAN session is not
// wired yet (fulfillCheckout creates one licence per session from its
// plan metadata). A cart of a single plan fulfils today's way; a
// multi-plan session needs the fulfilment side extended to mint one
// licence per line — tracked as the follow-up to this slice.
func CreateCartCheckoutSession(ctx context.Context, s CartStore, req *CartCheckoutRequest) (*CartCheckoutSession, error) {
	if req == nil {
		return nil, apperr.BadRequest("invalid request")
	}
	items, err := cartNormalizeItems(req.Items)
	if err != nil {
		return nil, err
	}
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if email == "" {
		return nil, apperr.New(400, "MISSING_CUSTOMER", "customer_email is required")
	}

	// The buyer's coupon — same resolution as every other sale.
	var cpn *coupon.Coupon
	if code := coupon.NormalizeCode(req.CouponCode); code != "" {
		row, err := s.FindCouponByCode(ctx, code)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, apperr.Wrap(404, "COUPON_NOT_FOUND", "coupon not found", err)
			}
			return nil, fmt.Errorf("look up coupon %s: %w", code, err)
		}
		eng := row.ToEngine()
		cpn = &eng
	}

	// Tax rates matched against the buyer's country.
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

	_, res, currency, err := cartPriceSale(ctx, s, items, req.Currency, cpn, rates, taxInclusive)
	if err != nil {
		var ae *apperr.AppError
		if errors.As(err, &ae) {
			return nil, err
		}
		slog.Error("cart checkout: cannot price the sale", "error", err)
		return nil, apperr.New(503, "PRICING_UNAVAILABLE", "cannot price the sale")
	}
	if res.TotalMinor <= 0 {
		return nil, apperr.BadRequest("the order total must be positive")
	}

	// Mode from the plans themselves (perpetual = one-time payment;
	// subscription/trial = recurring). A cart cannot mix the two — a
	// Stripe session is one mode. The first recurring plan's billing
	// interval is what a recurring tax line copies.
	mode := stripe.CheckoutSessionModePayment
	recurringInterval := ""
	firstLine := true
	for _, it := range items {
		plan, err := s.FindPlanByID(ctx, it.PlanID)
		if err != nil || plan == nil {
			return nil, apperr.NotFound("PLAN", it.PlanID)
		}
		recurring := plan.LicenseType != "perpetual"
		if firstLine {
			if recurring {
				mode = stripe.CheckoutSessionModeSubscription
				recurringInterval = plan.BillingInterval
			}
			firstLine = false
			continue
		}
		if recurring != (mode == stripe.CheckoutSessionModeSubscription) {
			return nil, apperr.BadRequest("a cart cannot mix one-time and subscription plans")
		}
		if recurring && recurringInterval == "" {
			recurringInterval = plan.BillingInterval
		}
	}

	params := &stripe.CheckoutSessionParams{
		Mode:                stripe.String(string(mode)),
		CustomerEmail:       stripe.String(email),
		SuccessURL:          stripe.String(req.SuccessURL),
		CancelURL:           stripe.String(req.CancelURL),
		AllowPromotionCodes: stripe.Bool(true),
	}
	for _, lr := range res.LineResults {
		params.LineItems = append(params.LineItems, &stripe.CheckoutSessionLineItemParams{
			PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
				Currency:   stripe.String(strings.ToLower(currency)),
				UnitAmount: stripe.Int64(lr.UnitAmountMinor),
				ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
					Name: stripe.String(lr.Description),
				},
			},
			Quantity: stripe.Int64(lr.Quantity),
		})
	}

	// The discount becomes a one-time fixed-amount Stripe coupon for
	// exactly the computed amount — same doctrine as CheckoutByPlan.
	if res.DiscountMinor > 0 && res.AppliedCoupon != nil {
		sc, err := newCartDiscountCoupon(ctx, res.DiscountMinor, currency, coupon.NormalizeCode(res.AppliedCoupon.Code))
		if err != nil {
			return nil, err
		}
		params.Discounts = []*stripe.CheckoutSessionDiscountParams{{Coupon: stripe.String(sc.ID)}}
	}

	// Exclusive tax is added as its own named line; inclusive tax
	// already lives inside the listed prices.
	if !taxInclusive && res.TaxMinor > 0 {
		item := &stripe.CheckoutSessionLineItemParams{
			PriceData: &stripe.CheckoutSessionLineItemPriceDataParams{
				Currency:   stripe.String(strings.ToLower(currency)),
				UnitAmount: stripe.Int64(res.TaxMinor),
				ProductData: &stripe.CheckoutSessionLineItemPriceDataProductDataParams{
					Name: stripe.String(taxLineLabel(rates)),
				},
			},
			Quantity: stripe.Int64(1),
		}
		if mode == stripe.CheckoutSessionModeSubscription {
			interval := stripe.PriceRecurringIntervalMonth
			count := int64(1)
			switch recurringInterval {
			case "year":
				interval = stripe.PriceRecurringIntervalYear
			}
			item.PriceData.Recurring = &stripe.CheckoutSessionLineItemPriceDataRecurringParams{
				Interval:      stripe.String(string(interval)),
				IntervalCount: stripe.Int64(count),
			}
		}
		params.LineItems = append(params.LineItems, item)
	}

	params.Metadata = map[string]string{
		"cart_count": strconv.Itoa(len(items)),
	}
	if raw, err := json.Marshal(items); err == nil {
		params.Metadata["cart_items"] = string(raw)
	}
	terms := checkoutTermsMetadata(res, rates, taxInclusive)
	for k, v := range terms {
		params.Metadata[k] = v
	}
	if len(terms) > 0 {
		// The stamped facts account for every minor unit of the
		// charge; a Stripe promotion code would rewrite the split.
		params.AllowPromotionCodes = nil
	}

	sess, err := session.New(params)
	if err != nil {
		slog.Error("cart checkout: stripe refused the session", "error", err)
		return nil, apperr.New(503, "PRICING_UNAVAILABLE", "cannot start the checkout")
	}
	return &CartCheckoutSession{
		URL:        sess.URL,
		CheckoutID: sess.ID,
		TotalMinor: res.TotalMinor,
		Currency:   currency,
		LineCount:  len(items),
	}, nil
}

// newCartDiscountCoupon mints the one-time fixed-amount Stripe coupon
// that carries a computed discount to the session (the CheckoutByPlan
// pattern): fixed, not percent, so Stripe cannot re-round the amount
// the engine computed; once-only, so it cannot leak to another sale.
func newCartDiscountCoupon(ctx context.Context, amountMinor int64, currency, code string) (*stripe.Coupon, error) {
	sc, err := stripecoupon.New(&stripe.CouponParams{
		AmountOff:      stripe.Int64(amountMinor),
		Currency:       stripe.String(strings.ToLower(currency)),
		Duration:       stripe.String(string(stripe.CouponDurationOnce)),
		MaxRedemptions: stripe.Int64(1),
		Name:           stripe.String(code),
		Params:         stripe.Params{Context: ctx},
	})
	if err != nil {
		slog.Error("cart checkout: failed to create the discount coupon", "coupon", code, "error", err)
		return nil, apperr.New(503, "PRICING_UNAVAILABLE", "cannot start the checkout")
	}
	return sc, nil
}
