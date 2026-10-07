package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v82"
	stripeprice "github.com/stripe/stripe-go/v82/price"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/tax"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// CheckoutQuoteHandler is the public pricing preview of a checkout.
//
//	POST /checkout/quote    Price items + coupon + tax before payment
//
// It is the public twin of the admin preview (POST /admin/orders/quotes):
// the same OrderService.Calculate and the same response shape — but the
// unit prices are resolved server-side from the plan catalogue instead
// of being read from the request body. A quote is a preview and nothing
// else: no order, licence, invoice or payment session is created here,
// and the answer carries nothing beyond money and catalogue fields the
// pricing page already shows.
//
// Every amount is int64 minor units in the currency of the plan's Stripe
// Price — the same source of truth payment.CheckoutByPlan charges from,
// so what is quoted is what will be charged. There is no request field
// for an amount and any a client sends is ignored.
type CheckoutQuoteHandler struct {
	svc    *service.OrderService
	plans  quotePlanCatalog
	taxes  quoteTaxRates
	prices quoteUnitPrice
}

// quotePlanCatalog resolves a plan by id or by its 8-character
// checkout id. *store.Store implements it; the seam exists so the
// quote can be priced in tests without a database. The checkout-id
// lookup is the one payment.CheckoutByPlan resolves /pay/:checkout_id
// through, so a quoted item and the payment link behind it can never
// name different plans.
type quotePlanCatalog interface {
	FindPlanByID(ctx context.Context, id string) (*model.Plan, error)
	FindPlanByCheckoutID(ctx context.Context, checkoutID string) (*model.Plan, error)
}

// quoteTaxRates resolves the active tax rates for a sale in a country
// and region. *store.Store implements it.
type quoteTaxRates interface {
	ListActiveTaxRatesForCountry(ctx context.Context, country, region string) ([]*model.TaxRate, error)
}

// quoteUnitPrice resolves a Stripe Price to its per-item amount in minor
// units and its ISO 4217 currency. This is the whole of where money
// enters the quote: the client never names an amount.
type quoteUnitPrice interface {
	UnitAmount(ctx context.Context, priceID string) (amountMinor int64, currency string, err error)
}

var (
	_ quotePlanCatalog = (*store.Store)(nil)
	_ quoteTaxRates    = (*store.Store)(nil)
	_ quoteUnitPrice   = stripeUnitPrice{}
)

// NewCheckoutQuoteHandler wires the handler. svc prices quotes through
// its injected CouponLookup (the Lead wires the coupon store at
// bootstrap); s is the plan and tax-rate catalogue.
func NewCheckoutQuoteHandler(svc *service.OrderService, s *store.Store) *CheckoutQuoteHandler {
	return &CheckoutQuoteHandler{
		svc:    svc,
		plans:  s,
		taxes:  s,
		prices: stripeUnitPrice{},
	}
}

// checkoutQuoteMaxItems bounds one quote. Each item costs a catalogue
// lookup and a Stripe price call, and a quote is a public endpoint: a
// cart is small by nature, so anything past this is abuse, not shopping.
const checkoutQuoteMaxItems = 50

// stripeQuoteWait bounds one price lookup. The Stripe SDK's own default
// is 80s, which on a public endpoint would park request goroutines on a
// slow Stripe instead of answering.
const stripeQuoteWait = 3 * time.Second

// stripeUnitPrice reads the unit amount of a Stripe Price. The Stripe
// Price is the source of truth for what a purchase costs — there is no
// local price column to go stale — and it is the very price
// payment.CheckoutByPlan hands to the checkout session.
type stripeUnitPrice struct{}

func (stripeUnitPrice) UnitAmount(ctx context.Context, priceID string) (int64, string, error) {
	ctx, cancel := context.WithTimeout(ctx, stripeQuoteWait)
	defer cancel()
	sp, err := stripeprice.Get(priceID, &stripe.PriceParams{Params: stripe.Params{Context: ctx}})
	if err != nil {
		return 0, "", err
	}
	if sp == nil || strings.TrimSpace(string(sp.Currency)) == "" {
		return 0, "", fmt.Errorf("stripe price %q has no currency", priceID)
	}
	return sp.UnitAmount, strings.ToUpper(string(sp.Currency)), nil
}

// Quote answers POST /checkout/quote.
//
// Body: { items: [{plan_id | checkout_id, quantity}], coupon_code?,
// country?, region?, tax_inclusive? }
//
// Each item names its plan either by plan_id or by the checkout_id of
// the payment link (the same lookup CheckoutByPlan pays through) —
// exactly one of the two.
//
// The unit amount of every line comes from the plan's Stripe Price, so
// the request cannot spoof one: there is no amount field to spoof it
// through. The pricing itself is service.OrderService.Calculate — the
// same calculation CreateOrder would persist — so the preview is exact.
func (h *CheckoutQuoteHandler) Quote(c *gin.Context) {
	var req struct {
		Items []struct {
			PlanID     string `json:"plan_id"`
			CheckoutID string `json:"checkout_id"`
			Quantity   int64  `json:"quantity"`
		} `json:"items"`
		CouponCode   string `json:"coupon_code"`
		Country      string `json:"country"`
		Region       string `json:"region"`
		TaxInclusive bool   `json:"tax_inclusive"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	if len(req.Items) == 0 {
		response.BadRequest(c, "items must not be empty")
		return
	}
	if len(req.Items) > checkoutQuoteMaxItems {
		response.BadRequest(c, fmt.Sprintf("items must not exceed %d entries", checkoutQuoteMaxItems))
		return
	}

	lines := make([]service.Line, 0, len(req.Items))
	currency := ""
	for i, it := range req.Items {
		planID, checkoutID := strings.TrimSpace(it.PlanID), strings.TrimSpace(it.CheckoutID)
		if planID == "" && checkoutID == "" {
			response.BadRequest(c, fmt.Sprintf("items[%d]: plan_id is required unless checkout_id is set", i))
			return
		}
		if planID != "" && checkoutID != "" {
			response.BadRequest(c, fmt.Sprintf("items[%d]: plan_id and checkout_id cannot both be set", i))
			return
		}
		if it.Quantity < 1 {
			response.BadRequest(c, fmt.Sprintf("items[%d]: quantity must be at least 1", i))
			return
		}
		var plan *model.Plan
		var err error
		if planID != "" {
			plan, err = h.plans.FindPlanByID(c, planID)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					response.BadRequest(c, fmt.Sprintf("items[%d]: unknown plan_id %q", i, planID))
					return
				}
				response.Internal(c, err)
				return
			}
		} else {
			// The checkout id of the payment link, resolved through the
			// same lookup CheckoutByPlan pays through.
			plan, err = h.plans.FindPlanByCheckoutID(c, checkoutID)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					response.BadRequest(c, fmt.Sprintf("items[%d]: unknown checkout_id %q", i, checkoutID))
					return
				}
				response.Internal(c, err)
				return
			}
		}
		if !plan.Active {
			response.BadRequest(c, fmt.Sprintf("items[%d]: plan %q is not available", i, it.PlanID))
			return
		}
		if plan.StripePriceID == "" {
			response.BadRequest(c, fmt.Sprintf("items[%d]: plan %q has no price", i, it.PlanID))
			return
		}
		// The unit amount is read here and nowhere else. A cart is
		// priced in one currency, so every line must agree on one.
		unit, cur, err := h.prices.UnitAmount(c, plan.StripePriceID)
		if err != nil {
			writeAppErr(c, apperr.Wrap(502, "PRICE_UNAVAILABLE",
				"price is temporarily unavailable", err))
			return
		}
		cur = strings.ToUpper(strings.TrimSpace(cur))
		if currency == "" {
			currency = cur
		} else if currency != cur {
			response.BadRequest(c, "all items must be priced in the same currency")
			return
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

	// Tax rates are configuration the operator owns, never something
	// the request supplies. Without a country there is no sale to match
	// a rate to, and quoting with every rate in the table would be
	// worse than quoting untaxed — so no country means no lookup.
	country := strings.ToUpper(strings.TrimSpace(req.Country))
	region := strings.ToUpper(strings.TrimSpace(req.Region))
	var rates []tax.Rate
	if country != "" {
		rows, err := h.taxes.ListActiveTaxRatesForCountry(c, country, region)
		if err != nil {
			response.Internal(c, err)
			return
		}
		for _, r := range rows {
			rates = append(rates, r.ToEngine())
		}
	}

	res, err := h.svc.Calculate(c, service.CalcInput{
		Lines:        lines,
		Currency:     currency,
		CouponCode:   req.CouponCode,
		TaxRates:     rates,
		TaxInclusive: req.TaxInclusive,
	})
	if err != nil {
		writeAppErr(c, checkoutQuoteErr(err))
		return
	}

	taxApplied := make([]gin.H, 0, len(rates))
	for _, r := range rates {
		taxApplied = append(taxApplied, gin.H{
			"jurisdiction": r.Jurisdiction,
			"basis_points": r.BasisPoints,
		})
	}
	body := gin.H{
		"currency":       currency,
		"tax_inclusive":  req.TaxInclusive,
		"subtotal_minor": res.SubtotalMinor,
		"discount_minor": res.DiscountMinor,
		"tax_minor":      res.TaxMinor,
		"total_minor":    res.TotalMinor,
		"lines":          res.LineResults,
		"applied_coupon": nil,
		"tax_rates":      taxApplied,
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

// checkoutQuoteErr re-maps the calculation's coupon refusals onto the
// checkout contract: whatever is wrong with the code — unknown, expired,
// exhausted, below its minimum — the quote answers 400 with the
// engine's precise message, so the checkout UI can put it next to the
// coupon field instead of treating an unknown code like a missing page.
//
// The calculation answers an unknown code 404 COUPON_NOT_FOUND, and an
// error code means exactly one status across the API (the contract in
// pkg/response), so a 400 cannot carry that code; it reuses
// COUPON_INVALID, whose meaning — "this coupon cannot be applied" — is
// the same refusal. Everything else passes through untouched.
func checkoutQuoteErr(err error) error {
	var ae *apperr.AppError
	if errors.As(err, &ae) && ae.Code == "COUPON_NOT_FOUND" {
		return apperr.Wrap(400, "COUPON_INVALID", ae.Message, err)
	}
	return err
}
