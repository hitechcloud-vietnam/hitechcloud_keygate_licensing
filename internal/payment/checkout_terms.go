package payment

import (
	"context"
	"strconv"
	"strings"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/coupon"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/tax"
)

// Checkout-terms metadata: the coupon and tax facts a paid checkout
// carries into the commerce ledger. CheckoutByPlan stamps them on the
// Stripe session when it creates it; recordOrder reads them back when
// the payment settles and writes them to the Order's coupon/tax
// columns — the keys mirror those columns one-to-one. A fact that does
// not apply (no coupon, or no tax) is omitted, and the column it
// would have filled stays at its zero value, exactly as a sale run
// through OrderService.CreateOrder records it.
//
// The money split itself is still read from Stripe (the session's
// amount total is what was charged); these keys say how that charge
// adds up, so the ledger's Subtotal/Discount/Tax columns name the
// right things and the Order invariants hold exactly:
//
//	Subtotal − Discount + Tax == Total   (exclusive tax)
//	Subtotal − Discount        == Total  (inclusive tax)
const (
	metaCouponCode       = "coupon_code"
	metaCouponType       = "coupon_type"
	metaCouponValueBPS   = "coupon_value_bps"
	metaCouponValueMinor = "coupon_value_minor"
	metaTaxJurisdiction  = "tax_jurisdiction"
	metaTaxBasisPoints   = "tax_basis_points"
	metaTaxInclusive     = "tax_inclusive"
)

// priceCheckout prices one plan sale exactly the way the public quote
// endpoint (POST /checkout/quote) prices it: the same
// service.OrderService.Calculate over the same money, coupon and tax
// engines, with the coupon already resolved and the unit amount read
// from the plan's Stripe Price. What is quoted is therefore what is
// charged, to the minor unit. Nothing here reads an amount from the
// client — there is no field for one.
func priceCheckout(ctx context.Context, plan *model.Plan, unitAmount int64, currency string, cpn *coupon.Coupon, rates []tax.Rate, taxInclusive bool) (*service.CalcResult, error) {
	return service.NewOrderService(nil, nil).Calculate(ctx, service.CalcInput{
		Lines: []service.Line{{
			SKU:             plan.Slug,
			ProductID:       plan.ProductID,
			PlanID:          plan.ID,
			Description:     plan.Name,
			Quantity:        1,
			UnitAmountMinor: unitAmount,
		}},
		Currency:     currency,
		Coupon:       cpn,
		TaxRates:     rates,
		TaxInclusive: taxInclusive,
	})
}

// checkoutTermsMetadata stamps the coupon and tax facts of a priced
// sale onto session metadata. The coupon keys appear when a coupon
// was applied — only the value unit its type means is stamped (basis
// points for percent, minor units for fixed, neither for free
// shipping), because the other one does not apply and its Order
// column is zero for the same coupons. The tax keys appear when the
// sale was matched to at least one active tax rate.
func checkoutTermsMetadata(res *service.CalcResult, rates []tax.Rate, taxInclusive bool) map[string]string {
	md := make(map[string]string)
	if cp := res.AppliedCoupon; cp != nil {
		md[metaCouponCode] = coupon.NormalizeCode(cp.Code)
		md[metaCouponType] = string(cp.Type)
		switch cp.Type {
		case coupon.TypePercentOff:
			md[metaCouponValueBPS] = strconv.FormatInt(cp.Value, 10)
		case coupon.TypeFixedAmountOff:
			md[metaCouponValueMinor] = strconv.FormatInt(cp.Value, 10)
		}
	}
	if len(rates) > 0 {
		md[metaTaxJurisdiction] = taxJurisdictionLabel(rates)
		md[metaTaxBasisPoints] = strconv.FormatInt(taxBasisPoints(rates), 10)
		md[metaTaxInclusive] = strconv.FormatBool(taxInclusive)
	}
	return md
}

// taxJurisdictionLabel summarises the rates a sale was taxed with the
// way the ledger does: the non-empty jurisdiction labels joined by
// "+" (service.taxLabel spells the same summary on Order rows).
func taxJurisdictionLabel(rates []tax.Rate) string {
	parts := make([]string, 0, len(rates))
	for _, r := range rates {
		if r.Jurisdiction != "" {
			parts = append(parts, r.Jurisdiction)
		}
	}
	return strings.Join(parts, "+")
}

// taxBasisPoints is the combined rate of stacked jurisdictions. The
// tax engine adds them up before computing (tax.CalculateMulti), so
// the sum is the rate the charged amount was taxed at.
func taxBasisPoints(rates []tax.Rate) int64 {
	var bps int64
	for _, r := range rates {
		bps += r.BasisPoints
	}
	return bps
}

// taxLineLabel names the extra line item that carries exclusive tax
// on the Stripe receipt, e.g. "Tax (VAT-VN)".
func taxLineLabel(rates []tax.Rate) string {
	if label := taxJurisdictionLabel(rates); label != "" {
		return "Tax (" + label + ")"
	}
	return "Tax"
}

// queryFlag reads an optional boolean query parameter: "1", "true" or
// "yes", in any case. Anything absent or unrecognised is false.
func queryFlag(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// allRatesInclusive reports whether every resolved rate row is marked
// inclusive-of-price. With no rates there is nothing to be inclusive
// of, so the answer is false.
func allRatesInclusive(rows []*model.TaxRate) bool {
	if len(rows) == 0 {
		return false
	}
	for _, r := range rows {
		if !r.Inclusive {
			return false
		}
	}
	return true
}

// ledgerTerms are the stamped coupon/tax facts of one session, read
// back at ledger time. hasCoupon/hasTax say which groups were stamped
// at all; the value fields then carry the facts and nothing else.
type ledgerTerms struct {
	hasCoupon        bool
	couponCode       string
	couponType       string
	couponValueBPS   int64
	couponValueMinor int64

	hasTax          bool
	taxJurisdiction string
	taxBasisPoints  int64
	taxInclusive    bool
}

// ledgerTermsFromMetadata reads the stamped facts back. A key that was
// never stamped reads as its zero value — the same the Order column
// holds for a fact that does not apply.
func ledgerTermsFromMetadata(md map[string]string) ledgerTerms {
	var t ledgerTerms
	if code := strings.TrimSpace(md[metaCouponCode]); code != "" {
		t.hasCoupon = true
		t.couponCode = code
		t.couponType = strings.TrimSpace(md[metaCouponType])
		t.couponValueBPS, _ = strconv.ParseInt(md[metaCouponValueBPS], 10, 64)
		t.couponValueMinor, _ = strconv.ParseInt(md[metaCouponValueMinor], 10, 64)
	}
	if _, ok := md[metaTaxBasisPoints]; ok {
		t.hasTax = true
		t.taxJurisdiction = md[metaTaxJurisdiction]
		t.taxBasisPoints, _ = strconv.ParseInt(md[metaTaxBasisPoints], 10, 64)
		t.taxInclusive, _ = strconv.ParseBool(md[metaTaxInclusive])
	}
	return t
}

// applyTo copies the stamped facts onto the ledger row's coupon/tax
// columns, preferring the metadata over anything re-derived later —
// these are the facts as they were when the sale was made, and the
// tax tables or coupon rows may have moved since.
func (t ledgerTerms) applyTo(o *model.Order) {
	if t.hasCoupon {
		o.CouponCode = t.couponCode
		o.CouponType = t.couponType
		o.CouponValueBPS = t.couponValueBPS
		o.CouponValueMinor = t.couponValueMinor
	}
	if t.hasTax {
		o.TaxJurisdiction = t.taxJurisdiction
		o.TaxBasisPoints = t.taxBasisPoints
		o.TaxInclusive = t.taxInclusive
	}
}

// ledgerMoney splits what Stripe charged into the ledger's money
// columns. total is the session's amount total (authoritative: it is
// the charge); detailsDiscount/detailsTax are the session's own
// total_details amounts; terms are the stamped facts.
//
// Without stamped tax facts the split is the historical one: the
// discount and tax Stripe reports, and the pre-discount subtotal
// rebuilt so Subtotal − Discount + Tax == Total holds — Stripe's
// AmountSubtotal is net of discounts, so it is not that number.
//
// With stamped tax facts the tax is recovered from the charge itself
// (see taxExtractedFrom) and the subtotal follows from the pricing
// mode:
//
//   - exclusive: the charge is net + tax, so
//     Subtotal = Total − Tax + Discount and the invariant
//     Subtotal − Discount + Tax == Total holds exactly;
//   - inclusive: the tax lives inside the charge, so
//     Subtotal = Total + Discount and Subtotal − Discount == Total
//     holds exactly — the tax is recorded but never added again.
func ledgerMoney(total, detailsDiscount, detailsTax int64, terms ledgerTerms) (subtotal, discount, taxMinor int64) {
	discount, taxMinor = detailsDiscount, detailsTax
	subtotal = total - taxMinor + discount
	if !terms.hasTax {
		return subtotal, discount, taxMinor
	}
	taxMinor = taxExtractedFrom(total, terms.taxBasisPoints)
	if terms.taxInclusive {
		subtotal = total + discount
	} else {
		subtotal = total - taxMinor + discount
	}
	return subtotal, discount, taxMinor
}

// taxExtractedFrom recovers the tax inside an amount the way the tax
// engine extracts it (round-half-up of amount·bps/(10000+bps)). It is
// the exact inverse of how a checkout session is charged, for both
// pricing modes:
//
//   - inclusive: the charged total is the gross, and extracting from
//     it is by definition the engine's inclusive computation — the
//     same call priceCheckout made on the same amount.
//   - exclusive: the charged total is net + tax with
//     tax = round(net·bps/10000). Writing the charge as C = N + T,
//     the extraction formula yields T + δ with
//     δ = (N·bps − 10000·T)/(10000+bps); since rounding T put
//     |N·bps − 10000·T| ≤ 5000, |δ| < 1/2 strictly, and half-up
//     rounding lands back on exactly T.
//
// So one formula recovers the tax minor unit-exactly in both modes,
// with no stored tax amount to trust or drift.
func taxExtractedFrom(total, taxBps int64) int64 {
	if total <= 0 || taxBps <= 0 {
		return 0
	}
	bd, err := tax.Calculate(total, tax.Rate{BasisPoints: taxBps}, true, tax.RoundingHalfUp)
	if err != nil {
		return 0
	}
	return bd.Tax
}
