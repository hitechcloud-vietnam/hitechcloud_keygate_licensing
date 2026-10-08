package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/uptrace/bun"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Gateway payments ───
//
// One row per one-off VND payment handed to a buyer through a
// Vietnamese gateway (Pay2S, ZaloPay, payOS — see
// payment/provider.go). The checkout writes it 'pending' alongside its
// pending order; the gateway's IPN settles it and fulfilment turns the
// order into a paid ledger entry with a licence.
//
// Money is int64 minor units (VND: whole dong) end to end; this layer
// never computes with it, it only persists what the checkout priced
// and what the IPN reported.
//
// The Bun alias for this model is "gateway_payment" (snake_case of the
// STRUCT name, not the table name) — pinned by
// gateway_payments_test.go, same doctrine as bun_alias_test.go.
type GatewayPayment struct {
	bun.BaseModel `bun:"table:gateway_payments"`

	ID          int64          `bun:",pk" json:"id"`
	OrderID     string         `bun:",notnull" json:"order_id"`
	Provider    string         `bun:",notnull" json:"provider"`
	ProviderRef string         `bun:",notnull" json:"provider_ref"`
	OrderKey    string         `bun:",notnull" json:"order_key"`
	AmountMinor int64          `bun:",notnull" json:"amount_minor"`
	Currency    string         `bun:",notnull,default:'VND'" json:"currency"`
	Status      string         `bun:",notnull,default:'pending'" json:"status"`
	TransID     string         `bun:",notnull,default:''" json:"trans_id,omitempty"`
	Raw         map[string]any `bun:"type:jsonb,default:'{}'" json:"raw,omitempty"`
	CreatedAt   time.Time      `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt   time.Time      `bun:",nullzero,default:now()" json:"updated_at"`
}

// gatewayRawJSON marshals the callback payload for a JSONB column.
// A nil map means "leave the column alone": the caller passes nil on
// paths (like a claim release) that rewrite state but not evidence.
func gatewayRawJSON(raw map[string]any) (string, bool, error) {
	if raw == nil {
		return "", false, nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

// gatewayPaymentRefQuery is the IPN's primary lookup: the gateway's
// own handle, unique per provider.
func gatewayPaymentRefQuery(db bun.IDB, provider, providerRef string, dest *GatewayPayment) *bun.SelectQuery {
	return db.NewSelect().Model(dest).
		Where("provider = ? AND provider_ref = ?", provider, providerRef)
}

// gatewayPaymentOrderKeyQuery is the fallback lookup by OUR order
// number — the value every VN gateway echoes in its callback. The
// newest row wins (a checkout retried at the gateway may have left
// more than one handle behind). The qualifier is the model alias
// ("gateway_payment"), never the table name.
func gatewayPaymentOrderKeyQuery(db bun.IDB, provider, orderKey string, dest *GatewayPayment) *bun.SelectQuery {
	return db.NewSelect().Model(dest).
		Where("provider = ? AND order_key = ?", provider, orderKey).
		OrderExpr("gateway_payment.id DESC").
		Limit(1)
}

// gatewayPaymentStatusUpdate builds the status write every variant
// shares. transID != "" records the gateway's transaction id; raw
// != nil replaces the stored payload.
func gatewayPaymentStatusUpdate(db bun.IDB, id int64, status, transID, rawJSON string, setRaw bool) *bun.UpdateQuery {
	q := db.NewUpdate().Model((*GatewayPayment)(nil)).
		Set("status = ?", status).
		Set("updated_at = now()").
		Where("id = ?", id)
	if transID != "" {
		q = q.Set("trans_id = ?", transID)
	}
	if setRaw {
		q = q.Set("raw = ?::jsonb", rawJSON)
	}
	return q
}

// CreateGatewayPayment writes the payment row the checkout opened.
// (provider, provider_ref) is UNIQUE: a gateway-side retry of the same
// handle surfaces as a unique violation, never as a second payment.
func (s *Store) CreateGatewayPayment(ctx context.Context, gp *GatewayPayment) error {
	_, err := s.DB.NewInsert().Model(gp).Returning("id").Exec(ctx)
	return err
}

// GetGatewayPaymentByRef finds the payment a gateway IPN is talking
// about, by the handle the gateway gave us at CreatePayment time.
func (s *Store) GetGatewayPaymentByRef(ctx context.Context, provider, providerRef string) (*GatewayPayment, error) {
	gp := new(GatewayPayment)
	return gp, gatewayPaymentRefQuery(s.DB, provider, providerRef, gp).Scan(ctx)
}

// GetGatewayPaymentByOrderKey finds a payment by OUR order number
// (HTC-…), the value the gateways echo in their callbacks. Used by the
// IPN fallback and by the browser return page's status poll.
func (s *Store) GetGatewayPaymentByOrderKey(ctx context.Context, provider, orderKey string) (*GatewayPayment, error) {
	gp := new(GatewayPayment)
	return gp, gatewayPaymentOrderKeyQuery(s.DB, provider, orderKey, gp).Scan(ctx)
}

// UpdateGatewayPaymentStatus writes the payment's state. Unconditional
// — the caller owns the row's lifecycle (IPN settlement, refunds).
func (s *Store) UpdateGatewayPaymentStatus(ctx context.Context, id int64, status, transID string, raw map[string]any) error {
	rawJSON, setRaw, err := gatewayRawJSON(raw)
	if err != nil {
		return err
	}
	_, err = gatewayPaymentStatusUpdate(s.DB, id, status, transID, rawJSON, setRaw).Exec(ctx)
	return err
}

// UpdateGatewayPaymentStatusIfPending is the settlement claim: the
// conditional UPDATE (WHERE status = 'pending') that lets exactly one
// of two simultaneous IPNs fulfil. claimed is false when the row was
// no longer pending — someone else settled it (or it was recorded
// failed/cancelled/expired meanwhile); err reports a database failure,
// which callers must never read as "already settled".
func (s *Store) UpdateGatewayPaymentStatusIfPending(ctx context.Context, id int64, status, transID string, raw map[string]any) (bool, error) {
	rawJSON, setRaw, err := gatewayRawJSON(raw)
	if err != nil {
		return false, err
	}
	res, err := gatewayPaymentStatusUpdate(s.DB, id, status, transID, rawJSON, setRaw).
		Where("status = 'pending'").Exec(ctx)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// gatewayOrderPaidUpdate builds the order flip a settled gateway
// payment performs: the pending order becomes the paid ledger entry,
// linked to the licence fulfilment just created. Conditional on
// status = 'pending' so the flip happens exactly once even if two
// fulfilments race; the loser sees 0 rows and skips its hooks.
func gatewayOrderPaidUpdate(db bun.IDB, orderID, licenseID, externalID, idempotencyKey string, paidAt time.Time) *bun.UpdateQuery {
	return db.NewUpdate().Model((*model.Order)(nil)).
		Set("status = ?", model.OrderStatusPaid).
		Set("paid_at = ?", paidAt).
		Set("license_id = ?", licenseID).
		Set("external_id = ?", externalID).
		Set("idempotency_key = ?", idempotencyKey).
		Set("updated_at = now()").
		Where("id = ? AND status = 'pending'", orderID)
}

// MarkGatewayOrderPaid settles the checkout's pending order after the
// IPN proved the money: status → paid, paid_at stamped, and the order
// linked to the licence (and to the gateway's handle as its external
// id / idempotency key, mirroring how recordOrder stamps a Stripe
// session). flipped is false when the order was not pending — a
// concurrent fulfilment got there first.
func (s *Store) MarkGatewayOrderPaid(ctx context.Context, orderID, licenseID, externalID, idempotencyKey string, paidAt time.Time) (bool, error) {
	res, err := gatewayOrderPaidUpdate(s.DB, orderID, licenseID, externalID, idempotencyKey, paidAt).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
