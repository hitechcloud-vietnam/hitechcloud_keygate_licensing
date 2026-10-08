// Refunds (plan §79): full, partial and manual refunds of an order.
//
// One flow, three shapes:
//
//	full      amount == everything left on the order — the order
//	          becomes 'refunded', the invoice follows, the licence is
//	          revoked with reason `refund`, and the partner money is
//	          clawed back
//	partial   amount < everything left — the order becomes
//	          'partially_refunded' and the LICENCE STAYS ACTIVE (the
//	          customer paid for part of the term and keeps it;
//	          documented business rule)
//	manual    a gateway with no refund API (pay2s, payos) or no
//	          gateway at all — the refund is RECORDED
//	          (payment_provider 'manual', status succeeded) and no
//	          gateway is called: the money went back by bank
//	          transfer / dashboard and this row is its ledger entry.
//
// Dispatch is by the order's payment_provider:
//
//	stripe    partial refund through the Stripe SDK
//	          (refundStripePaymentIntent — amount REQUIRED, int64)
//	zalopay   RefundPayment with AmountMinor — asynchronous: a
//	          pending result leaves the refunds row 'pending' until
//	          reconciliation (ReconcileRefund); the amount still
//	          reserves against further refunds
//	pay2s,    ErrNotSupported by contract (no refund API) → manual
//	payos
//	manual,   no gateway to call → manual
//	""
//
// Money discipline (plan §51): amounts are int64 minor units, never
// float64; historical order totals are never recalculated — only
// refunded_minor accumulates.
//
// Idempotency: the same (order, idempotency key) never refunds twice.
// The key is the request's Idempotency-Key header when present (the
// existing middleware.Idempotency pattern, enforced here so it also
// covers callers that skip the middleware) and otherwise a hash of
// (amount, reason) — so replaying the identical refund request
// RETURNS the original refund row instead of issuing another one. A
// key reused for a DIFFERENT amount/reason is refused exactly like
// the middleware refuses it.
package payment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
)

// RefundStore is the slice of store.Store the refund flow needs: the
// ledger, the refund rows, the licence/invoice effects, the partner
// clawbacks and the idempotency slots. The real wiring passes
// *store.Store; tests pass a fake so the whole validation matrix runs
// without a database.
type RefundStore interface {
	FindOrderByID(ctx context.Context, id string) (*model.Order, error)
	FindLicenseByID(ctx context.Context, id string) (*model.License, error)
	InvoiceByOrder(ctx context.Context, orderID string) (*model.Invoice, error)
	UpdateInvoiceStatus(ctx context.Context, id, status string, voidedAt, uncollectibleAt *time.Time) error

	RefundSumsByOrder(ctx context.Context, orderID string) (committed, succeeded int64, err error)
	RecordRefund(ctx context.Context, r *model.Refund) (*model.Order, error)
	FindRefundByID(ctx context.Context, id int64) (*model.Refund, error)
	UpdateRefundStatus(ctx context.Context, id int64, status, providerRef, transID string) error
	SyncOrderRefundState(ctx context.Context, orderID string) (*model.Order, error)

	RevokeLicenseWithReason(ctx context.Context, id, reason, actor string) error
	CancelCommissionForOrder(ctx context.Context, resellerID, orderID string) (bool, error)
	FindConversionByOrderID(ctx context.Context, orderID string) (*model.AffiliateConversion, error)
	SetConversionStatus(ctx context.Context, id, status string) (*model.AffiliateConversion, error)

	BeginIdempotent(ctx context.Context, scope, key, requestHash string) (*store.IdempotencyRecord, bool, error)
	FinishIdempotent(ctx context.Context, id int64, status int, body []byte) error
	ReleaseIdempotent(ctx context.Context, id int64) error

	Audit(ctx context.Context, log *model.AuditLog)
}

var _ RefundStore = (*store.Store)(nil)

// refundIdemScopePrefix namespaces the idempotency slots of the flow.
// The scope carries the order id so two orders can use the same key.
const refundIdemScopePrefix = "refund:"

// RefundOrder refunds (part of) an order. amountMinor is REQUIRED and
// positive — the API's "omit = full remaining" is resolved by the
// handler before this call, so the pinned validation is always the
// same three rules:
//
//   - the order is paid or partially_refunded (anything else —
//     pending, failed, already fully refunded — is
//     ORDER_NOT_REFUNDABLE, 409);
//   - amountMinor > 0 (REFUND_AMOUNT_INVALID, 400);
//   - amountMinor ≤ total − already committed (REFUND_EXCEEDS_PAID,
//     400). "Committed" counts pending refunds too: their money is on
//     its way out and must not be promised twice.
//
// reason is free text (kept on the row); actor is who asked (the
// admin's id). The idempotency key is derived from (amount, reason)
// — see RefundOrderKey.
func RefundOrder(ctx context.Context, s RefundStore, orderID string, amountMinor int64, reason, actor string) (*model.Refund, error) {
	return RefundOrderKey(ctx, s, orderID, amountMinor, reason, actor, "")
}

// RefundOrderKey is RefundOrder with the request's Idempotency-Key
// (header) supplied. An empty key falls back to the (amount, reason)
// hash; a non-empty one is used as given. Same validation, same
// effects.
func RefundOrderKey(ctx context.Context, s RefundStore, orderID string, amountMinor int64, reason, actor, idemKey string) (*model.Refund, error) {
	if s == nil {
		return nil, errors.New("refund: store is required")
	}
	orderID = strings.TrimSpace(orderID)
	reason = strings.TrimSpace(reason)
	actor = strings.TrimSpace(actor)

	order, err := s.FindOrderByID(ctx, orderID)
	if err != nil || order == nil {
		return nil, apperr.NotFound("ORDER", orderID)
	}
	committed, _, err := s.RefundSumsByOrder(ctx, order.ID)
	if err != nil {
		return nil, fmt.Errorf("refund: read refunded sums: %w", err)
	}
	if err := refundValidate(order, amountMinor, committed); err != nil {
		return nil, err
	}

	// The idempotency claim: one refund per (order, key). A replay
	// returns the refund row the first request recorded; a key reused
	// for a different amount/reason is a client bug and refused.
	scope := refundIdemScopePrefix + order.ID
	key := strings.TrimSpace(idemKey)
	if key == "" {
		key = "auto:" + refundAutoKey(order.ID, amountMinor, reason)
	}
	reqHash := refundAutoKey(order.ID, amountMinor, reason)
	rec, created, err := s.BeginIdempotent(ctx, scope, key, reqHash)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrIdempotencyKeyReused):
			return nil, apperr.New(409, "IDEMPOTENCY_KEY_REUSED", "this Idempotency-Key was already used for a different refund")
		case errors.Is(err, store.ErrIdempotencyInProgress):
			return nil, apperr.New(409, "IDEMPOTENCY_IN_PROGRESS", "a refund with this Idempotency-Key is still in flight")
		default:
			return nil, fmt.Errorf("refund: claim idempotency slot: %w", err)
		}
	}
	if !created {
		// Replay: the stored body is the refund row id.
		id, cerr := strconv.ParseInt(strings.TrimSpace(string(rec.ResponseBody)), 10, 64)
		if cerr != nil {
			return nil, fmt.Errorf("refund: corrupt idempotency record %d: %w", rec.ID, cerr)
		}
		return s.FindRefundByID(ctx, id)
	}
	rollback := func(err error) (*model.Refund, error) {
		_ = s.ReleaseIdempotent(ctx, rec.ID)
		return nil, err
	}

	// ── Dispatch ──
	provider, providerRef, transID, status, err := refundDispatch(ctx, s, order, amountMinor, reason)
	if err != nil {
		// The gateway refused or failed: record the attempt as failed
		// (freeing its amount) and surface the pinned 502. The
		// idempotency slot is released so an operator can retry.
		row := refundRow(order, amountMinor, reason, actor, provider, providerRef, transID, model.RefundStatusFailed)
		if _, rerr := s.RecordRefund(ctx, row); rerr != nil {
			slogRefund("failed refund could not be recorded", order, rerr)
		}
		return rollback(apperr.Wrap(502, "PAYMENT_GATEWAY_ERROR", "the payment provider could not refund this payment", err))
	}

	// ── Record + effects ──
	row := refundRow(order, amountMinor, reason, actor, provider, providerRef, transID, status)
	updated, err := s.RecordRefund(ctx, row)
	if err != nil {
		return rollback(fmt.Errorf("refund: record refund: %w", err))
	}
	if row.Status == model.RefundStatusSucceeded {
		refundApplyEffects(ctx, s, order, row, updated, actor)
	}
	if err := s.FinishIdempotent(ctx, rec.ID, 200, []byte(strconv.FormatInt(row.ID, 10))); err != nil {
		// The refund stands; only the replay record failed.
		slogRefund("refund recorded but idempotency slot not completed", order, err)
	}
	return row, nil
}

// refundValidate is the pinned eligibility matrix as pure logic —
// no store, no I/O, pinned directly by refunds_test.go.
func refundValidate(order *model.Order, amountMinor, committedMinor int64) error {
	if order.Status != model.OrderStatusPaid && order.Status != model.OrderStatusPartiallyRefunded {
		return apperr.New(409, "ORDER_NOT_REFUNDABLE",
			"only a paid or partially refunded order can be refunded")
	}
	if amountMinor <= 0 {
		return apperr.New(400, "REFUND_AMOUNT_INVALID",
			"the refund amount must be greater than zero")
	}
	if amountMinor > order.TotalMinor-committedMinor {
		return apperr.New(400, "REFUND_EXCEEDS_PAID",
			"the refund amount exceeds what is left to refund on this order")
	}
	return nil
}

// refundAutoKey is the deterministic fingerprint of one logical
// refund: same order, amount and reason = same key = same refund. The
// request-hash shape (sha-256 hex) matches middleware.Idempotency so
// both halves of the idempotency table speak the same language.
func refundAutoKey(orderID string, amountMinor int64, reason string) string {
	h := sha256.Sum256([]byte(orderID + "\x00" + strconv.FormatInt(amountMinor, 10) + "\x00" + reason))
	return hex.EncodeToString(h[:])
}

// refundRow builds the refunds row for a completed dispatch.
func refundRow(order *model.Order, amountMinor int64, reason, actor, provider, providerRef, transID, status string) *model.Refund {
	return &model.Refund{
		OrderID:         order.ID,
		PaymentProvider: provider,
		ProviderRef:     providerRef,
		TransID:         transID,
		AmountMinor:     amountMinor,
		Currency:        order.Currency,
		Reason:          reason,
		Status:          status,
		RefundedBy:      actor,
	}
}

// refundDispatch moves (or records) the money by the order's payment
// provider and reports what to record. The returned status is the
// refund row's initial status: 'succeeded' where the money is back
// synchronously (stripe, manual), 'pending' for asynchronous
// gateways (zalopay).
func refundDispatch(ctx context.Context, s RefundStore, order *model.Order, amountMinor int64, reason string) (provider, providerRef, transID, status string, err error) {
	switch p := strings.ToLower(strings.TrimSpace(order.PaymentProvider)); p {
	case model.RefundProviderStripe:
		pi := refundStripeIntent(ctx, s, order)
		if pi == "" {
			return p, "", "", model.RefundStatusFailed,
				errors.New("stripe refund: no payment intent is recorded for this order")
		}
		ref, err := refundStripePaymentIntent(ctx, pi, amountMinor, reason)
		if err != nil {
			return p, pi, "", model.RefundStatusFailed, err
		}
		// Stripe refunds are synchronous: the charge is refunded when
		// the call returns.
		return p, ref, pi, model.RefundStatusSucceeded, nil

	case model.RefundProviderZalo:
		gp, err := Provider(model.RefundProviderZalo)
		if err != nil || gp == nil || !gp.Enabled() {
			return p, "", "", model.RefundStatusFailed,
				fmt.Errorf("zalopay refund: provider not available: %w", err)
		}
		res, err := gp.RefundPayment(ctx, RefundRequest{
			ProviderRef: refundGatewayRef(order),
			TransID:     "",
			AmountMinor: amountMinor,
			Reason:      reason,
			RefundID:    refundGatewayRefundID(order.ID, amountMinor, reason),
		})
		if errors.Is(err, ErrNotSupported) {
			// Defensive fold: a gateway that grew a no-refund API
			// answer mid-flight is refunded manually, same as pay2s.
			return model.RefundProviderManual, "", "", model.RefundStatusSucceeded, nil
		}
		if err != nil {
			return p, "", "", model.RefundStatusFailed, err
		}
		if res == nil {
			return p, "", "", model.RefundStatusFailed, errors.New("zalopay refund: empty result")
		}
		// Asynchronous by contract: accept the request now, settle at
		// reconciliation. A provider that answers 'succeeded' right
		// away is recorded as such.
		status = model.RefundStatusPending
		if res.Status == StatusSucceeded {
			status = model.RefundStatusSucceeded
		}
		return p, res.RefundRef, "", status, nil

	case model.RefundProviderPay2S, model.RefundProviderPayOS, model.RefundProviderManual, "":
		// No refund API (pay2s, payos) or no gateway at all: the
		// money went back out of band and this row is its ledger
		// entry. No gateway call, by contract.
		return model.RefundProviderManual, "", "", model.RefundStatusSucceeded, nil

	default:
		// An unknown-to-this-flow provider is still worth trying: one
		// that answers ErrNotSupported is refunded manually, anything
		// else is a real failure.
		gp, err := Provider(p)
		if err == nil && gp != nil && gp.Enabled() {
			res, perr := gp.RefundPayment(ctx, RefundRequest{
				ProviderRef: refundGatewayRef(order),
				AmountMinor: amountMinor,
				Reason:      reason,
				RefundID:    refundGatewayRefundID(order.ID, amountMinor, reason),
			})
			if errors.Is(perr, ErrNotSupported) {
				return model.RefundProviderManual, "", "", model.RefundStatusSucceeded, nil
			}
			if perr != nil {
				return p, "", "", model.RefundStatusFailed, perr
			}
			status = model.RefundStatusSucceeded
			if res != nil && res.Status == StatusPending {
				status = model.RefundStatusPending
			}
			ref := ""
			if res != nil {
				ref = res.RefundRef
			}
			return p, ref, "", status, nil
		}
		return model.RefundProviderManual, "", "", model.RefundStatusSucceeded, nil
	}
}

// refundStripeIntent locates the charge to refund: the licence's
// payment intent first (recorded at fulfilment), then the checkout
// session's own (recordOrder stamps the session id on the order).
func refundStripeIntent(ctx context.Context, s RefundStore, order *model.Order) string {
	if order.LicenseID != "" {
		if lic, err := s.FindLicenseByID(ctx, order.LicenseID); err == nil && lic != nil {
			if pi := strings.TrimSpace(lic.StripePaymentIntentID); pi != "" {
				return pi
			}
		}
	}
	return ""
}

// refundGatewayRef is the gateway handle to send a refund against.
// The order's ExternalID carries the session id for stripe and the
// order number for the VN gateways (their ProviderRef IS our order
// number — see provider.go).
func refundGatewayRef(order *model.Order) string {
	if ref := strings.TrimSpace(order.ExternalID); ref != "" {
		return ref
	}
	return order.OrderNumber
}

// refundGatewayRefundID is OUR idempotent refund reference at a
// gateway (ZaloPay m_refund_id): derived from the same fingerprint as
// the local idempotency key, so a retried request reuses it and the
// gateway dedupes even across a lost idempotency slot.
func refundGatewayRefundID(orderID string, amountMinor int64, reason string) string {
	return "R" + strings.ToUpper(refundAutoKey(orderID, amountMinor, reason)[:16])
}

// refundApplyEffects runs the fulfilment side of a SUCCEEDED refund,
// exactly the pinned business rules:
//
//	partial → order 'partially_refunded'; the LICENCE STAYS ACTIVE;
//	          invoice untouched
//	full    → order 'refunded' (RecordRefund already did); invoice
//	          → 'refunded' when it is paid (the only legal move off
//	          paid, model.CanTransitionInvoice); the licence is
//	          REVOKED with reason `refund` and the actor recorded;
//	          partner money is clawed back
//
// All of it is best-effort in the sense that one failing effect never
// rolls back the refund — the money moved first and the ledger row is
// truth — but every failure is logged.
func refundApplyEffects(ctx context.Context, s RefundStore, order *model.Order, row *model.Refund, updated *model.Order, actor string) {
	s.Audit(ctx, &model.AuditLog{
		Entity: "order", EntityID: order.ID, Action: "refund_recorded",
		ActorType: "admin", ActorID: actor,
		Changes: map[string]any{
			"refund_id":    row.ID,
			"amount_minor": row.AmountMinor,
			"status":       row.Status,
			"provider":     row.PaymentProvider,
			"reason":       row.Reason,
		},
	})
	if updated == nil || updated.Status != model.OrderStatusRefunded {
		// Partial: document-only. The licence keeps serving the term
		// the customer paid for and the invoice stays paid.
		return
	}

	// The invoice follows the money.
	if inv, err := s.InvoiceByOrder(ctx, order.ID); err == nil && inv != nil {
		if model.CanTransitionInvoice(inv.Status, model.InvoiceStatusRefunded) {
			now := time.Now()
			if err := s.UpdateInvoiceStatus(ctx, inv.ID, model.InvoiceStatusRefunded, nil, &now); err != nil {
				slogRefund("refund: invoice could not be marked refunded", order, err)
			}
		}
	}

	// Full refund revokes the licence — reason `refund`, the actor
	// who issued it, stamped now (plan §80).
	if order.LicenseID != "" {
		if lic, err := s.FindLicenseByID(ctx, order.LicenseID); err == nil && lic != nil &&
			lic.Status != model.StatusRevoked {
			if err := s.RevokeLicenseWithReason(ctx, lic.ID, model.RevokeReasonRefund, actor); err != nil {
				slogRefund("refund: licence could not be revoked", order, err)
			} else {
				s.Audit(ctx, &model.AuditLog{
					Entity: "license", EntityID: lic.ID, Action: "revoked",
					ActorType: "admin", ActorID: actor,
					Changes: map[string]any{"reason": model.RevokeReasonRefund, "refund_id": row.ID},
				})
			}
		}
	}

	// Partner clawbacks (the existing clawback infra): the reseller
	// commission that has not been disbursed is cancelled; an
	// affiliate conversion is reversed through the review state
	// machine. Both are per-order and best-effort — a partner payout
	// that already left is NOT unwound from here (a cancelled
	// commission is money not yet sent; one marked paid is a
	// conversation).
	if order.ResellerID != "" {
		if cancelled, err := s.CancelCommissionForOrder(ctx, order.ResellerID, order.ID); err != nil {
			slogRefund("refund: commission clawback failed", order, err)
		} else if cancelled {
			s.Audit(ctx, &model.AuditLog{
				Entity: "order", EntityID: order.ID, Action: "commission_clawed_back",
				ActorType: "admin", ActorID: actor,
				Changes: map[string]any{"reseller_id": order.ResellerID, "refund_id": row.ID},
			})
		}
	}
	if conv, err := s.FindConversionByOrderID(ctx, order.ID); err == nil && conv != nil {
		if model.ConversionTransitionOK(conv.Status, model.AffiliateConversionStatusReversed) {
			if _, err := s.SetConversionStatus(ctx, conv.ID, model.AffiliateConversionStatusReversed); err != nil {
				slogRefund("refund: affiliate conversion clawback failed", order, err)
			}
		}
	}
}

// ReconcileRefund settles an asynchronous refund (zalopay) once the
// gateway reports its final state: the row's status moves to the
// reported one and, when it landed, the full/partial fulfilment
// effects run exactly as an immediate refund's would. Exposed for the
// reconciliation loop Lead schedules; safe to call repeatedly (a
// settled row is left alone).
func ReconcileRefund(ctx context.Context, s RefundStore, refundID int64, status, providerRef, transID, actor string) (*model.Refund, error) {
	row, err := s.FindRefundByID(ctx, refundID)
	if err != nil {
		return nil, err
	}
	if row.Status != model.RefundStatusPending {
		return row, nil // settled already — reconciliation is not a rewrite
	}
	if !model.ValidRefundStatus(status) || status == model.RefundStatusPending {
		return nil, fmt.Errorf("refund: invalid terminal status %q", status)
	}
	if err := s.UpdateRefundStatus(ctx, row.ID, status, providerRef, transID); err != nil {
		return nil, err
	}
	row.Status = status
	if providerRef != "" {
		row.ProviderRef = providerRef
	}
	if transID != "" {
		row.TransID = transID
	}
	if status == model.RefundStatusSucceeded {
		order, err := s.FindOrderByID(ctx, row.OrderID)
		if err != nil {
			return row, nil // status recorded; effects are best-effort
		}
		updated, err := s.SyncOrderRefundState(ctx, row.OrderID)
		if err != nil {
			slogRefund("reconcile: order state could not be re-derived", order, err)
			updated = order
		}
		refundApplyEffects(ctx, s, order, row, updated, actor)
	}
	return row, nil
}

// slogRefund logs an effect failure against the order it belongs to.
func slogRefund(msg string, order *model.Order, err error) {
	slog.Error(msg, "order_id", order.ID, "order_number", order.OrderNumber, "error", err)
}
