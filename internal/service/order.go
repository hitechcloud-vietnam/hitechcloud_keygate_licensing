package service

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/coupon"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/money"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/tax"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
)

// CouponLookup resolves a coupon code to the coupon engine value.
// Concrete impl (backed by the coupons store) is injected at bootstrap
// by the Lead. A nil lookup is legal and means "no coupons": a code
// asked for then simply does not discount anything.
type CouponLookup interface {
	FindCouponByCode(ctx context.Context, code string) (*coupon.Coupon, error) // returns engine type
}

// OrderService prices orders and persists them as the commerce ledger.
//
// Pricing is a composition of the three pure engines — money for
// integer minor-unit arithmetic, coupon for discounts, tax for
// exclusive/inclusive rates — and is deliberately free of database
// access: Calculate works with nothing but its input and the injected
// CouponLookup, which is what makes it testable and what makes the
// preview endpoint honest (it computes exactly what CreateOrder would
// store). Every amount is int64 minor units; no float ever touches
// this file.
type OrderService struct {
	store        *store.Store
	couponLookup CouponLookup
	// now is time.Now except in tests that need a fixed clock for
	// coupon validity windows.
	now func() time.Time
}

// NewOrderService wires an OrderService. s may be nil for pure
// calculation (previews, tests); coupons may be nil for "no coupons".
func NewOrderService(s *store.Store, coupons CouponLookup) *OrderService {
	return &OrderService{store: s, couponLookup: coupons, now: time.Now}
}

// Line is one order line being priced. Quantity is in items and
// UnitAmountMinor is a per-item price in minor units.
type Line struct {
	SKU             string
	ProductID       string
	PlanID          string
	Description     string
	Quantity        int64
	UnitAmountMinor int64
}

// CalcInput is everything Calculate needs: the lines, the currency,
// an optional coupon, and the tax configuration. TaxRates are stacked
// jurisdictions (additive, see the tax engine) applied to every line.
type CalcInput struct {
	Lines      []Line
	Currency   string
	CouponCode string
	// Coupon, when set, is a pre-resolved coupon used as-is and
	// CouponCode is ignored for resolution (it is still recorded on
	// the order). The checkout flow uses this when it has already
	// looked the coupon up.
	Coupon       *coupon.Coupon
	TaxRates     []tax.Rate
	TaxInclusive bool
}

// LineResult is one priced line: the input echoed back plus the four
// money columns an OrderItem stores. The lines always sum exactly to
// the CalcResult totals — allocation uses the largest-remainder
// method (money.Split/Allocate semantics and the coupon engine's own
// per-line allocation), so no minor unit is lost or invented.
//
// With TaxInclusive the line total is the gross amount charged (tax is
// extracted from inside it); otherwise tax is added on top.
type LineResult struct {
	SKU               string `json:"sku,omitempty"`
	ProductID         string `json:"product_id,omitempty"`
	PlanID            string `json:"plan_id,omitempty"`
	Description       string `json:"description,omitempty"`
	Quantity          int64  `json:"quantity"`
	UnitAmountMinor   int64  `json:"unit_amount_minor"`
	LineSubtotalMinor int64  `json:"line_subtotal_minor"`
	LineDiscountMinor int64  `json:"line_discount_minor"`
	LineTaxMinor      int64  `json:"line_tax_minor"`
	LineTotalMinor    int64  `json:"line_total_minor"`
}

// CalcResult is the priced order. Invariants, both exact:
//
//   - exclusive tax: Subtotal - Discount + Tax == Total, and each line
//     satisfies LineSubtotal - LineDiscount + LineTax == LineTotal
//   - inclusive tax: Subtotal - Discount == Total (the tax lives
//     inside the total), and each line satisfies
//     LineSubtotal - LineDiscount == LineTotal
//
// LineResults sum field-by-field to the totals in both modes.
type CalcResult struct {
	SubtotalMinor int64        `json:"subtotal_minor"`
	DiscountMinor int64        `json:"discount_minor"`
	TaxMinor      int64        `json:"tax_minor"`
	TotalMinor    int64        `json:"total_minor"`
	LineResults   []LineResult `json:"lines,omitempty"`
	// AppliedCoupon is the coupon that produced DiscountMinor, or nil
	// when none was applied.
	AppliedCoupon *coupon.Coupon `json:"-"`
	// Err mirrors the error Calculate returned, so a caller holding
	// only the result can tell a priced order from a refusal. It is
	// nil on success.
	Err error `json:"-"`
}

// OrderMeta is who and how: the commercial context an order cannot be
// priced without but that no engine cares about.
type OrderMeta struct {
	CustomerEmail   string
	CustomerName    string
	LicenseID       string
	PaymentProvider string
	ExternalID      string
	IdempotencyKey  string
}

// Numbering: human-facing, unambiguous (no 0/O, 1/I/L), 8 characters
// of cryptographic randomness behind a short prefix. Uniqueness is
// ultimately the database's (UNIQUE indexes), and CreateOrder retries
// on the astronomically unlikely collision.
const (
	orderNumberPrefix   = "HTC-"
	invoiceNumberPrefix = "INV-"
	refLength           = 8
	maxNumberAttempts   = 3
	refAlphabet         = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
)

// Calculate prices an order without persisting anything.
//
// Subtotal is the sum of quantity × unit price computed with the money
// engine. The coupon (if any) is resolved through the injected
// CouponLookup and applied with the coupon engine, whose per-line
// largest-remainder allocation decides each line's discount. Tax is
// then computed per line on the discounted amount with the tax engine,
// which rounds every line independently so the order totals are the
// exact sum of its lines.
//
// Robustness: a nil CouponLookup skips the coupon entirely, and empty
// TaxRates mean zero tax. On error the returned result is non-nil with
// Err set alongside the error.
func (s *OrderService) Calculate(ctx context.Context, in CalcInput) (*CalcResult, error) {
	res, err := s.calculate(ctx, in)
	if res == nil {
		res = &CalcResult{}
	}
	res.Err = err
	return res, err
}

func (s *OrderService) calculate(ctx context.Context, in CalcInput) (*CalcResult, error) {
	if len(in.Lines) == 0 {
		return nil, apperr.New(400, "NO_LINES", "at least one line item is required")
	}
	cur, err := money.NewCurrency(in.Currency)
	if err != nil {
		return nil, apperr.New(400, "INVALID_CURRENCY", fmt.Sprintf("unsupported currency %q", in.Currency))
	}

	// Line subtotals and the order subtotal, all through the money
	// engine so overflow is caught rather than wrapped around.
	lineSubtotals := make([]int64, len(in.Lines))
	subtotal := money.Zero(cur)
	for i, l := range in.Lines {
		if l.Quantity <= 0 {
			return nil, apperr.New(400, "INVALID_QUANTITY",
				fmt.Sprintf("line %d: quantity must be at least 1", i+1))
		}
		if int64(int(l.Quantity)) != l.Quantity {
			return nil, apperr.New(400, "INVALID_QUANTITY",
				fmt.Sprintf("line %d: quantity too large", i+1))
		}
		if l.UnitAmountMinor < 0 {
			return nil, apperr.New(400, "INVALID_UNIT_AMOUNT",
				fmt.Sprintf("line %d: unit amount must not be negative", i+1))
		}
		lineAmt, err := money.FromMinor(l.UnitAmountMinor, cur).MulQty(l.Quantity)
		if err != nil {
			return nil, apperr.New(400, "AMOUNT_OVERFLOW",
				fmt.Sprintf("line %d: amount out of range", i+1))
		}
		lineSubtotals[i] = lineAmt.Minor()
		if subtotal, err = subtotal.Add(lineAmt); err != nil {
			return nil, apperr.New(400, "AMOUNT_OVERFLOW", "order subtotal out of range")
		}
	}

	// Discount. A pre-resolved coupon wins; otherwise the code goes
	// through the injected lookup. With no lookup wired (or no code)
	// nothing is discounted — the calculation is still complete and
	// exact, which is what lets preview run before the coupon tables
	// exist.
	cp := in.Coupon
	if cp == nil {
		if code := coupon.NormalizeCode(in.CouponCode); code != "" && s.couponLookup != nil {
			found, lerr := s.couponLookup.FindCouponByCode(ctx, code)
			if lerr != nil {
				var ae *apperr.AppError
				if errors.As(lerr, &ae) {
					return nil, lerr
				}
				if errors.Is(lerr, coupon.ErrCouponNotFound) {
					return nil, apperr.Wrap(404, "COUPON_NOT_FOUND", "coupon not found", lerr)
				}
				return nil, apperr.Wrap(400, "COUPON_INVALID", lerr.Error(), lerr)
			}
			cp = found
		}
	}

	lineDiscounts := make([]int64, len(in.Lines))
	var discountMinor int64
	if cp != nil {
		cLines := make([]coupon.Line, len(in.Lines))
		for i, l := range in.Lines {
			cLines[i] = coupon.Line{
				SKU:         l.SKU,
				ProductCode: l.ProductID,
				PlanCode:    l.PlanID,
				Quantity:    int(l.Quantity),
				UnitAmount:  l.UnitAmountMinor,
			}
		}
		cres, cerr := coupon.Apply(*cp, coupon.Input{
			Lines:      cLines,
			Subtotal:   subtotal.Minor(),
			Currency:   cur.Code(),
			CustomerID: "",
		}, s.now())
		if cerr != nil {
			return nil, apperr.Wrap(400, "COUPON_INVALID", cerr.Error(), cerr)
		}
		discountMinor = cres.DiscountAmount
		// The engine's allocation is largest-remainder: the per-line
		// discounts sum to DiscountAmount exactly.
		for _, alloc := range cres.LineAllocations {
			if alloc.LineIndex >= 0 && alloc.LineIndex < len(lineDiscounts) {
				lineDiscounts[alloc.LineIndex] = alloc.Discount
			}
		}
	}

	// Tax on the discounted amount of each line. The line is fed to
	// the tax engine as quantity 1 at the discounted line total: the
	// discount may not divide evenly across the units, and per-unit
	// rounding would lose pennies. The engine rounds each line
	// independently and sums, so order tax is exactly the sum of line
	// taxes in both modes. Empty TaxRates yield zero tax.
	taxLines := make([]tax.Line, len(in.Lines))
	for i := range in.Lines {
		taxLines[i] = tax.Line{Quantity: 1, UnitAmount: lineSubtotals[i] - lineDiscounts[i]}
	}
	ob, terr := tax.CalculateLines(taxLines, in.TaxRates, in.TaxInclusive, tax.RoundingHalfUp)
	if terr != nil {
		return nil, apperr.Wrap(400, "TAX_CALCULATION_FAILED", terr.Error(), terr)
	}

	lineResults := make([]LineResult, len(in.Lines))
	var taxMinor, totalMinor int64
	for i, l := range in.Lines {
		base := lineSubtotals[i] - lineDiscounts[i]
		lineTax := ob.Lines[i].Breakdown.Tax
		// Exclusive: tax is added on top. Inclusive: the base is the
		// gross already, and the tax lives inside it.
		lineTotal := base
		if !in.TaxInclusive {
			lineTotal += lineTax
		}
		lineResults[i] = LineResult{
			SKU:               l.SKU,
			ProductID:         l.ProductID,
			PlanID:            l.PlanID,
			Description:       l.Description,
			Quantity:          l.Quantity,
			UnitAmountMinor:   l.UnitAmountMinor,
			LineSubtotalMinor: lineSubtotals[i],
			LineDiscountMinor: lineDiscounts[i],
			LineTaxMinor:      lineTax,
			LineTotalMinor:    lineTotal,
		}
		taxMinor += lineTax
		totalMinor += lineTotal
	}

	// Totals are the exact sums of the lines above, so the per-line
	// and order-level identities hold by construction.
	return &CalcResult{
		SubtotalMinor: subtotal.Minor(),
		DiscountMinor: discountMinor,
		TaxMinor:      taxMinor,
		TotalMinor:    totalMinor,
		LineResults:   lineResults,
		AppliedCoupon: cp,
	}, nil
}

// CreateOrder prices the order and writes the ledger: the order, its
// line items, and the draft invoice derived from it. Numbers are
// allocated here (HTC-… / INV-…).
//
// With an IdempotencyKey a retry returns the order the first attempt
// created instead of creating a second one.
func (s *OrderService) CreateOrder(ctx context.Context, in CalcInput, meta OrderMeta) (*model.Order, error) {
	email := strings.TrimSpace(meta.CustomerEmail)
	if email == "" {
		return nil, apperr.New(400, "MISSING_CUSTOMER", "customer_email is required")
	}
	if s.store == nil {
		return nil, apperr.Internal(errors.New("order service has no store attached"))
	}
	key := strings.TrimSpace(meta.IdempotencyKey)
	if key != "" {
		// A miss is the normal case (first attempt) and indistinct
		// from a lookup hiccup; the create below fails loudly if the
		// database is actually unreachable.
		if existing, err := s.store.FindOrderByIdempotencyKey(ctx, key); err == nil {
			return existing, nil
		}
	}

	res, err := s.Calculate(ctx, in)
	if err != nil {
		return nil, err
	}

	o := &model.Order{
		CustomerEmail:   email,
		CustomerName:    strings.TrimSpace(meta.CustomerName),
		LicenseID:       strings.TrimSpace(meta.LicenseID),
		Currency:        strings.ToUpper(strings.TrimSpace(in.Currency)),
		SubtotalMinor:   res.SubtotalMinor,
		DiscountMinor:   res.DiscountMinor,
		TaxMinor:        res.TaxMinor,
		TotalMinor:      res.TotalMinor,
		TaxInclusive:    in.TaxInclusive,
		Status:          model.OrderStatusPending,
		PaymentProvider: strings.TrimSpace(meta.PaymentProvider),
		ExternalID:      strings.TrimSpace(meta.ExternalID),
		IdempotencyKey:  key,
	}
	if res.AppliedCoupon != nil {
		o.CouponCode = coupon.NormalizeCode(res.AppliedCoupon.Code)
		o.CouponType = string(res.AppliedCoupon.Type)
		// The coupon's configured value, in the unit its type means:
		// basis points for percent, minor units for fixed.
		switch res.AppliedCoupon.Type {
		case coupon.TypePercentOff:
			o.CouponValueBPS = res.AppliedCoupon.Value
		case coupon.TypeFixedAmountOff:
			o.CouponValueMinor = res.AppliedCoupon.Value
		}
	}
	o.TaxJurisdiction, o.TaxBasisPoints = taxLabel(in.TaxRates)
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

	if err := s.insertOrderWithNumber(ctx, o); err != nil {
		return nil, err
	}

	// The invoice copies the order's money — a billing document, not a
	// second calculation.
	inv := &model.Invoice{
		OrderID:       o.ID,
		Status:        model.InvoiceStatusDraft,
		Currency:      o.Currency,
		SubtotalMinor: o.SubtotalMinor,
		DiscountMinor: o.DiscountMinor,
		TaxMinor:      o.TaxMinor,
		TotalMinor:    o.TotalMinor,
	}
	if err := s.insertInvoiceWithNumber(ctx, inv); err != nil {
		// The order is already written and is the chargeable record;
		// a missing draft invoice is an operational repair, not a
		// reason to pretend the sale did not happen.
		return o, err
	}
	return o, nil
}

// insertOrderWithNumber writes the order, retrying with a fresh order
// number if the generated one collided (unique index). ID and
// idempotency-key collisions are unique violations too but retrying
// cannot fix them; the attempts run out and the error surfaces.
func (s *OrderService) insertOrderWithNumber(ctx context.Context, o *model.Order) error {
	var err error
	for attempt := 0; attempt < maxNumberAttempts; attempt++ {
		num, genErr := randomRef()
		if genErr != nil {
			return apperr.Internal(genErr)
		}
		o.OrderNumber = orderNumberPrefix + num
		if err = s.store.CreateOrder(ctx, o); err == nil {
			return nil
		}
		if !isUniqueViolation(err) {
			break
		}
	}
	return apperr.Internal(err)
}

// insertInvoiceWithNumber is insertOrderWithNumber for invoices.
func (s *OrderService) insertInvoiceWithNumber(ctx context.Context, inv *model.Invoice) error {
	var err error
	for attempt := 0; attempt < maxNumberAttempts; attempt++ {
		num, genErr := randomRef()
		if genErr != nil {
			return apperr.Internal(genErr)
		}
		inv.InvoiceNumber = invoiceNumberPrefix + num
		if err = s.store.CreateInvoice(ctx, inv); err == nil {
			return nil
		}
		if !isUniqueViolation(err) {
			break
		}
	}
	return apperr.Internal(err)
}

// taxLabel summarises the rates an order was taxed with: the joined
// jurisdiction labels and the summed basis points.
func taxLabel(rates []tax.Rate) (string, int64) {
	var parts []string
	var bps int64
	for _, r := range rates {
		bps += r.BasisPoints
		if r.Jurisdiction != "" {
			parts = append(parts, r.Jurisdiction)
		}
	}
	return strings.Join(parts, "+"), bps
}

// randomRef draws refLength characters from the unambiguous alphabet.
func randomRef() (string, error) {
	max := big.NewInt(int64(len(refAlphabet)))
	out := make([]byte, refLength)
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("order: generate reference: %w", err)
		}
		out[i] = refAlphabet[n.Int64()]
	}
	return string(out), nil
}

// isUniqueViolation reports whether err is a unique/duplicate key
// refusal — the shape a generated-number collision takes.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate") || strings.Contains(msg, "unique")
}
