package payment

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/checkout/session"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
)

// recordOrder writes one paid checkout into the commerce ledger as a
// paid Order (with its line item) and a paid Invoice. It runs after the
// licence has already been created and is strictly best-effort: any
// failure here is logged and never fails fulfilment — the ledger can be
// reconciled from Stripe later, but a licence must never be held up by
// bookkeeping.
//
// The money is what Stripe actually charged (the checkout session's
// amount total), read back from Stripe rather than recomputed, so a
// receipt always matches the charge regardless of how prices, coupons
// or tax have moved since.
//
// The coupon and tax facts stamped on the session when it was created
// (see checkout_terms.go) are preferred for the Order's coupon/tax
// columns and for splitting the charge into Subtotal/Discount/Tax;
// sessions that carry no such stamp fall back to the totals-derived
// reconstruction this has always done. Either way the ledger
// invariants hold exactly:
//
//	Subtotal − Discount + Tax == Total   (exclusive tax)
//	Subtotal − Discount        == Total  (inclusive tax)
//
// The attribution stamped alongside those terms (see
// attribution.go) lands on the Order's reseller/affiliate snapshot
// columns the same way — stamped facts preferred, buyer-email
// resolution only filling what is missing — and then feeds the two
// partner ledgers (reseller commission accrual + affiliate
// conversion), which are idempotent at the store and best-effort
// here.
//
// Idempotent per Stripe session: the session id is the order's external
// id and idempotency key, so one paid session yields one order — the
// same rule the licence fulfilment already follows.
func (h *StripeHandler) recordOrder(ctx context.Context, lic *model.License, plan *model.Plan, sessionID, email, productName string) {
	if sessionID == "" {
		return
	}
	// Already recorded? The lookup distinguishes "absent" from "cannot
	// tell"; on the latter, skip rather than risk a duplicate ledger row.
	existing, err := h.Store.FindOrderByExternalID(ctx, "stripe", sessionID)
	if err == nil && existing != nil && existing.ID != "" {
		// A previous pass recorded the order. The partner ledgers are
		// idempotent (one commission per (reseller, order), one
		// conversion per order), so the attribution effects run again:
		// a first pass that died between the insert and the hooks is
		// healed, and a complete one is a no-op.
		recordAttributionEffects(ctx, h.Store, existing)
		return
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		slog.Warn("stripe order: cannot confirm ledger state, skipping entry",
			"session_id", sessionID, "error", err)
		return
	}

	sess, serr := session.Get(sessionID, &stripe.CheckoutSessionParams{})
	if serr != nil || sess == nil {
		slog.Warn("stripe order: cannot read session amount, skipping entry",
			"session_id", sessionID, "error", serr)
		return
	}
	total := sess.AmountTotal
	currency := strings.ToUpper(string(sess.Currency))
	var detailsDiscount, detailsTax int64
	if sess.TotalDetails != nil {
		detailsDiscount = sess.TotalDetails.AmountDiscount
		detailsTax = sess.TotalDetails.AmountTax
	}
	// The stamped coupon/tax facts say how the charge adds up; without
	// them the split is rebuilt from the totals alone, which keeps the
	// invariant Subtotal − Discount + Tax == Total because Stripe's
	// AmountSubtotal is net of discounts, so it is not that number.
	terms := ledgerTermsFromMetadata(sess.Metadata)
	// The stamped attribution says who brought the sale. What the
	// session did not stamp is filled from the same authority checkout
	// pricing uses (the buyer email) — stamped facts win, this only
	// fills gaps.
	attr := ledgerAttributionFromMetadata(sess.Metadata)
	attr.fillFallbacks(ctx, h.Store, email)
	subtotal, discount, taxMinor := ledgerMoney(total, detailsDiscount, detailsTax, terms)

	now := time.Now()
	order := &model.Order{
		OrderNumber:     service.NewOrderNumber(),
		CustomerEmail:   email,
		Currency:        currency,
		SubtotalMinor:   subtotal,
		DiscountMinor:   discount,
		TaxMinor:        taxMinor,
		TotalMinor:      total,
		Status:          model.OrderStatusPaid,
		PaymentProvider: "stripe",
		ExternalID:      sessionID,
		IdempotencyKey:  "stripe:" + sessionID,
		PaidAt:          &now,
		Items: []*model.OrderItem{{
			ProductID:         plan.ProductID,
			PlanID:            plan.ID,
			Description:       strings.TrimSpace(productName + " — " + plan.Name),
			Quantity:          1,
			UnitAmountMinor:   total,
			LineSubtotalMinor: subtotal,
			LineDiscountMinor: discount,
			LineTaxMinor:      taxMinor,
			LineTotalMinor:    total,
		}},
	}
	if lic != nil {
		order.LicenseID = lic.ID
	}
	// The coupon/tax columns are the sale's facts as they were when the
	// session was created, never re-read from the coupon or tax tables,
	// which may have moved since. Same for the attribution columns.
	terms.applyTo(order)
	attr.applyTo(order)
	if err := h.Store.CreateOrder(ctx, order); err != nil {
		if isUniqueRef(err) {
			// A concurrent writer got there first; its order stands.
			// Its partner ledgers are idempotent too, so running them
			// on the winner's row is safe either way.
			slog.Info("stripe order: ledger entry already recorded concurrently",
				"session_id", sessionID)
			if winner, werr := h.Store.FindOrderByExternalID(ctx, "stripe", sessionID); werr == nil && winner != nil {
				recordAttributionEffects(ctx, h.Store, winner)
			}
		} else {
			slog.Error("stripe order: failed to record order",
				"session_id", sessionID, "error", err)
		}
		return
	}

	// The partner ledgers (reseller commission + affiliate conversion)
	// before the invoice: the order row is what they key on, and a
	// bookkeeping hiccup down the line must not cost a partner their
	// commission. Both are idempotent at the store and best-effort
	// here — see attribution.go.
	recordAttributionEffects(ctx, h.Store, order)

	inv := &model.Invoice{
		OrderID:       order.ID,
		InvoiceNumber: service.NewInvoiceNumber(),
		Status:        model.InvoiceStatusPaid,
		Currency:      currency,
		SubtotalMinor: subtotal,
		DiscountMinor: discount,
		TaxMinor:      taxMinor,
		TotalMinor:    total,
		IssuedAt:      &now,
		PaidAt:        &now,
	}
	if err := h.Store.CreateInvoice(ctx, inv); err != nil {
		// The order is in; the invoice can be re-drawn from it later.
		slog.Error("stripe order: failed to record invoice",
			"session_id", sessionID, "order_id", order.ID, "error", err)
		return
	}

	// invoice.paid (plan §35): the money for this order was collected
	// and the invoice is paid — the Stripe checkout's ledger close.
	// Best-effort: the ledger row is in, and nothing downstream waits
	// on a receiver being up. The unique-ref path above sends nothing:
	// that order's invoice was paid once, and the event belongs to the
	// pass that wrote it.
	if h.WebhookSvc != nil {
		h.WebhookSvc.Dispatch(ctx, plan.ProductID, model.EventInvoicePaid, map[string]any{
			"invoice_id": inv.ID, "order_id": order.ID, "order_number": order.OrderNumber,
			"invoice_number": inv.InvoiceNumber, "amount_minor": inv.TotalMinor,
			"currency": inv.Currency, "payment_provider": "stripe",
		})
	}

	slog.Info("stripe order: ledger entry recorded",
		"order_number", order.OrderNumber, "session_id", sessionID,
		"email", email, "total", total, "currency", currency,
		"reseller_id", order.ResellerID, "referral_code", order.ReferralCode,
		"wholesale_override", attr.hasWholesale)
}

// isUniqueRef reports whether err is a unique-key violation — a
// generated reference or idempotency key that already exists — which
// means the ledger row is (or was being) written by someone else.
func isUniqueRef(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "duplicate") || strings.Contains(s, "unique")
}
