// Refunds (plan §79) and the licence revocation-reason vocabulary
// (plan §80).
//
// A Refund is one money-back event against an order: full, partial or
// manual. Money is int64 minor units at the order's currency
// (plan §51 — never float64), and a refund row is a financial record:
// written for every refund, never deleted, never recalculated.
package model

import (
	"time"

	"github.com/uptrace/bun"
)

// Refund status values. The lifecycle is small on purpose:
//
//	pending    accepted by an asynchronous gateway (ZaloPay refunds
//	           settle later) — the amount is already reserved against
//	           further refunds
//	succeeded  the money is back (or was never moved by a gateway:
//	           manual refunds settle immediately)
//	failed     the gateway refused; the amount frees up again
const (
	RefundStatusPending   = "pending"
	RefundStatusSucceeded = "succeeded"
	RefundStatusFailed    = "failed"
)

// RefundStatuses lists the vocabulary in lifecycle order. It is the
// single source the validation and the DB CHECK (migration
// 20261008_149000) mirror.
var RefundStatuses = []string{
	RefundStatusPending,
	RefundStatusSucceeded,
	RefundStatusFailed,
}

// ValidRefundStatus reports whether status is one of the three.
func ValidRefundStatus(status string) bool {
	switch status {
	case RefundStatusPending, RefundStatusSucceeded, RefundStatusFailed:
		return true
	}
	return false
}

// Refund payment providers: who moved (or was asked to move) the
// money. Same vocabulary as orders.payment_provider plus 'manual' —
// the record for an operator-issued refund of a gateway that has no
// refund API (pay2s, payos), or of an order that was never
// gateway-paid at all.
const (
	RefundProviderStripe = "stripe"
	RefundProviderPay2S  = "pay2s"
	RefundProviderZalo   = "zalopay"
	RefundProviderPayOS  = "payos"
	RefundProviderManual = "manual"
)

// ─── Refund ───
//
// One row per refund issued against an order. The Bun alias for this
// model is "refund" (snake_case of the STRUCT name) — pinned by
// refunds_test.go, same doctrine as bun_alias_test.go. Qualify with
// "refund".id in hand-written SQL, never refunds.id.

type Refund struct {
	bun.BaseModel `bun:"table:refunds"`

	// ID is the BIGSERIAL row id — the model keeps int64 (like
	// GatewayPayment) because the ledger's own ids stay uuid TEXT.
	ID      int64  `bun:",pk,autoincrement" json:"id"`
	OrderID string `bun:",notnull" json:"order_id"`
	// PaymentProvider is one of the RefundProvider* values.
	PaymentProvider string `bun:",notnull,default:''" json:"payment_provider"`
	// ProviderRef is the gateway handle of the payment refunded
	// (Stripe payment intent, ZaloPay app_trans_id, …); empty for
	// manual refunds.
	ProviderRef string `bun:",notnull,default:''" json:"provider_ref,omitempty"`
	// TransID is the gateway's transaction id when one exists — the
	// reconciliation key.
	TransID string `bun:",notnull,default:''" json:"trans_id,omitempty"`
	// AmountMinor is the refunded money in int64 minor units
	// (plan §51). Always > 0 (DB CHECK); "the rest of the order" is
	// resolved to an amount before a Refund row is ever written.
	AmountMinor int64  `bun:",notnull" json:"amount_minor"`
	Currency    string `bun:",notnull" json:"currency"`
	Reason      string `bun:",notnull,default:''" json:"reason,omitempty"`
	// Status is one of the RefundStatus* values. A pending refund
	// reserves its amount against further refunds even though the
	// money has not landed yet.
	Status string `bun:",notnull,default:'pending'" json:"status"`
	// RefundedBy is who asked for the refund: the admin user id, or a
	// system actor. Free text on purpose.
	RefundedBy string    `bun:",notnull,default:''" json:"refunded_by,omitempty"`
	CreatedAt  time.Time `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt  time.Time `bun:",nullzero,default:now()" json:"updated_at"`
}

// Counts reports whether this refund already removed money from the
// order's remaining refundable amount. Pending counts: the money is
// committed to going back and must not be refunded twice while the
// gateway settles. Failed does not count — the amount freed up again.
func (r *Refund) Counts() bool {
	return r != nil && (r.Status == RefundStatusSucceeded || r.Status == RefundStatusPending)
}

// ─── Revocation reasons (§80) ───
//
// Why a licence was revoked. A closed vocabulary so the audit trail
// can be grouped and reported: an operator picks a reason, an
// integration sends one, and "administrative_action" is the default
// when nobody says. The strings live in licenses.revoke_reason.
const (
	RevokeReasonFraud            = "fraud"
	RevokeReasonRefund           = "refund"
	RevokeReasonChargeback       = "chargeback"
	RevokeReasonPolicyViolation  = "policy_violation"
	RevokeReasonCustomerRequest  = "customer_request"
	RevokeReasonSecurityIncident = "security_incident"
	RevokeReasonAdministrative   = "administrative_action"
)

// DefaultRevokeReason is what a revoke call falls back to when its
// request carries no reason.
const DefaultRevokeReason = RevokeReasonAdministrative

// RevokeReasons lists the vocabulary in display order. It is the
// single source the revoke endpoints validate against.
func RevokeReasons() []string {
	return []string{
		RevokeReasonFraud,
		RevokeReasonRefund,
		RevokeReasonChargeback,
		RevokeReasonPolicyViolation,
		RevokeReasonCustomerRequest,
		RevokeReasonSecurityIncident,
		RevokeReasonAdministrative,
	}
}

// ValidRevokeReason reports whether reason is one of the seven. An
// empty reason is NOT valid here — callers substitute
// DefaultRevokeReason first, so what reaches storage is always a real
// member of the vocabulary.
func ValidRevokeReason(reason string) bool {
	switch reason {
	case RevokeReasonFraud, RevokeReasonRefund, RevokeReasonChargeback,
		RevokeReasonPolicyViolation, RevokeReasonCustomerRequest,
		RevokeReasonSecurityIncident, RevokeReasonAdministrative:
		return true
	}
	return false
}
