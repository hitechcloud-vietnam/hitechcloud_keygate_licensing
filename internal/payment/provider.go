// Payment gateway abstraction (plan §25 PAYMENT ARCHITECTURE):
//
//	"Do NOT hardcode one payment provider. Create a provider abstraction:
//	 PaymentProvider — createPayment(), capturePayment(), refundPayment(),
//	 voidPayment(), verifyWebhook(), getPaymentStatus().
//	 Provider availability must depend on actual integration capability."
//
// Stripe remains the engine for SUBSCRIPTIONS (recurring billing, metered
// usage, proration — see change_plan.go). The providers behind this
// interface are the Vietnamese one-off payment gateways — Pay2S (bank
// transfer / Napas 247 QR), ZaloPay, payOS (VietQR) — plus future ones.
// They all settle in VND whole-dong amounts and report success
// asynchronously through their IPN/callback/webhook, so the flow is:
//
//  1. checkout calls CreatePayment and redirects the buyer to PayURL;
//  2. the gateway calls back VerifyWebhook → the handler validates the
//     normalized event against the stored order and fulfils;
//  3. GetPaymentStatus is the reconciliation backstop (webhook lost?).
//
// Money discipline (plan §51): every amount is int64 minor units. For VND
// (ISO-4217 exponent 0) minor units ARE whole dong — a 22,000 VND charge
// is AmountMinor = 22000 everywhere in this codebase and in every
// gateway payload. No floats, ever.
package payment

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Provider IDs. Stable wire values — stored on orders, used in URLs.
const (
	ProviderStripe  = "stripe"
	ProviderPay2S   = "pay2s"
	ProviderZaloPay = "zalopay"
	ProviderPayOS   = "payos"
)

// PaymentStatus is the normalized lifecycle of a one-off gateway payment.
type PaymentStatus string

const (
	StatusPending   PaymentStatus = "pending"
	StatusSucceeded PaymentStatus = "succeeded"
	StatusFailed    PaymentStatus = "failed"
	StatusCancelled PaymentStatus = "cancelled"
	StatusExpired   PaymentStatus = "expired"
	StatusRefunded  PaymentStatus = "refunded"
)

// Sentinel errors. Handlers map these to pkg/response codes (one HTTP
// status per code — the repo-wide contract test enforces it).
var (
	// ErrProviderNotFound: no provider registered under that name.
	ErrProviderNotFound = errors.New("payment: provider not found")
	// ErrProviderNotConfigured: registered but its credentials are not
	// all set. Availability depends on actual integration capability.
	ErrProviderNotConfigured = errors.New("payment: provider not configured")
	// ErrNotSupported: the provider has no API for this operation
	// (e.g. payOS has no refund API — refunds are bank transfers).
	ErrNotSupported = errors.New("payment: operation not supported by provider")
	// ErrWebhookSignatureInvalid: signature/mac check failed. Fail closed.
	ErrWebhookSignatureInvalid = errors.New("payment: webhook signature invalid")
	// ErrWebhookPayloadMalformed: unparseable or semantically broken body.
	ErrWebhookPayloadMalformed = errors.New("payment: webhook payload malformed")
	// ErrCurrencyNotSupported: the gateway settles VND only.
	ErrCurrencyNotSupported = errors.New("payment: currency not supported by provider")
	// ErrAmountMismatch: callback amount differs from the stored order.
	ErrAmountMismatch = errors.New("payment: amount mismatch")
)

// CreatePaymentRequest starts a one-off payment for an order we have
// already priced server-side. AmountMinor is authoritative — providers
// must send exactly this amount and the webhook path must re-check it.
type CreatePaymentRequest struct {
	// OrderID is OUR order number (HTC-…). It becomes the gateway's
	// order reference so the IPN can find the row. ≤64 bytes, no secrets.
	OrderID string
	// AmountMinor is the total to charge, int64 minor units (VND: dong).
	AmountMinor int64
	// Currency is the ISO-4217 code of the order. VN gateways: "VND".
	Currency string
	// Description shown to the buyer / bank memo. Gateways restrict
	// charset/length (Pay2S: 10–32 alnum, unique per order — the
	// provider impl enforces its own rules).
	Description string
	// BuyerEmail / BuyerName are hints for the gateway (invoice fields).
	BuyerEmail string
	BuyerName  string
	// ReturnURL / CancelURL: where the buyer's browser lands afterwards.
	ReturnURL string
	CancelURL string
	// ExpiresAt (optional): when the payment link should die.
	ExpiresAt time.Time
	// Extra is provider-specific escape hatch (e.g. Pay2S bankAccounts).
	Extra map[string]string
}

// CreatePaymentResult is what the checkout needs to send the buyer away.
type CreatePaymentResult struct {
	// ProviderRef is the gateway-side handle for later lookup/refund
	// (Pay2S orderId/requestId, ZaloPay app_trans_id, payOS orderCode).
	ProviderRef string
	// PayURL is the redirect target (Pay2S payUrl, ZaloPay order_url,
	// payOS checkoutUrl).
	PayURL string
	// QRCode when the gateway returned one (data-URI PNG or raw VietQR
	// payload). Optional — the UI renders it if non-empty.
	QRCode string
	// ExpiresAt of the payment link, zero when the gateway said nothing.
	ExpiresAt time.Time
}

// PaymentStatusResult is the normalized answer of GetPaymentStatus /
// CapturePayment.
type PaymentStatusResult struct {
	ProviderRef string
	// TransID is the gateway's own transaction id (Pay2S transId,
	// ZaloPay zp_trans_id, payOS reference) — reconciliation key.
	TransID     string
	Status      PaymentStatus
	AmountMinor int64
	Currency    string
	// PaidAt when the gateway reported settlement, zero otherwise.
	PaidAt time.Time
	// Raw keeps the provider payload for the audit trail. Never contains
	// secrets.
	Raw map[string]any
}

// RefundRequest asks the gateway to give money back.
type RefundRequest struct {
	ProviderRef string
	TransID     string
	AmountMinor int64
	Reason      string
	// RefundID is OUR idempotent refund reference (m_refund_id on
	// ZaloPay). The caller reuses it on retry.
	RefundID string
}

// RefundResult reports the refund request outcome. Some gateways (ZaloPay)
// are asynchronous: Status may be StatusPending and the final state comes
// from GetPaymentStatus / the refund-status API.
type RefundResult struct {
	RefundRef string
	Status    PaymentStatus
	Raw       map[string]any
}

// WebhookEvent is one authenticated, normalized callback. Handlers must
// treat it as UNTRUSTED until VerifyWebhook returned it — the verification
// runs first and fails closed.
type WebhookEvent struct {
	// Provider is the producing provider id.
	Provider string
	// OrderID is OUR order number extracted from the callback, when the
	// gateway echoes it (all three VN gateways do).
	OrderID string
	// ProviderRef is the gateway handle stored at CreatePayment time.
	ProviderRef string
	// TransID is the gateway transaction id (dedup key together with
	// ProviderRef — Pay2S may IPN the same transaction 5 times).
	TransID     string
	Status      PaymentStatus
	AmountMinor int64
	Currency    string
	PaidAt      time.Time
	Raw         map[string]any
}

// PaymentProvider is the plan §25 abstraction. Implementations live in
// pay2s.go, zalopay.go, payos.go — one file set per gateway, all
// self-contained (client + signing + parsing + tests).
//
// Contract rules every implementation must honor:
//
//   - money is int64 minor units, never float64;
//   - every signature check is constant-time (crypto/hmac.Equal) and
//     happens BEFORE any field is trusted;
//   - Enabled() is all-or-nothing over the required credentials;
//   - unsupported operations return ErrNotSupported, never a fake success;
//   - network calls carry the ctx and a sane timeout.
type PaymentProvider interface {
	// Name returns the stable provider id (ProviderPay2S …).
	Name() string
	// Enabled reports whether the provider's credentials are configured.
	// Availability depends on actual integration capability (§25).
	Enabled() bool
	// CreatePayment starts a one-off payment and returns the redirect.
	CreatePayment(ctx context.Context, req CreatePaymentRequest) (*CreatePaymentResult, error)
	// CapturePayment confirms/captures an authorized payment when the
	// gateway supports it; VN gateways settle automatically, so their
	// implementations alias GetPaymentStatus (documented, not a stub).
	CapturePayment(ctx context.Context, ref string) (*PaymentStatusResult, error)
	// RefundPayment refunds (part of) a settled payment.
	RefundPayment(ctx context.Context, req RefundRequest) (*RefundResult, error)
	// VoidPayment cancels a not-yet-settled payment / payment link.
	VoidPayment(ctx context.Context, ref, reason string) error
	// VerifyWebhook authenticates a raw callback body and normalizes it.
	// Returns ErrWebhookSignatureInvalid / ErrWebhookPayloadMalformed.
	VerifyWebhook(payload []byte) (*WebhookEvent, error)
	// GetPaymentStatus queries the gateway for the authoritative state.
	GetPaymentStatus(ctx context.Context, ref string) (*PaymentStatusResult, error)
}

// ─── Registry ───

// RegisterProvider adds a provider at boot time (main.go wires the
// configured gateways). Re-registering the same name replaces the entry.
func RegisterProvider(p PaymentProvider) {
	if p == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[p.Name()] = p
}

// Provider returns the registered provider under name.
func Provider(name string) (PaymentProvider, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	p, ok := registry[name]
	if !ok {
		return nil, ErrProviderNotFound
	}
	return p, nil
}

// EnabledProviders lists the ids of configured providers, sorted at the
// call site's discretion (stable map order not guaranteed).
func EnabledProviders() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(registry))
	for name, p := range registry {
		if p.Enabled() {
			out = append(out, name)
		}
	}
	return out
}

var (
	registryMu sync.RWMutex
	registry   = map[string]PaymentProvider{}
)
