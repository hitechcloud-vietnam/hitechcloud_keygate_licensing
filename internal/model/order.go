package model

import (
	"time"

	"github.com/uptrace/bun"
)

// ─── Order ───
//
// The commerce ledger. One Order is one checkout: the priced snapshot
// of what was bought, what it cost after discount and tax, and how it
// was (or will be) paid. The money fields are int64 minor units stored
// at order time — a historical order keeps the numbers it was paid at
// and is never recalculated from current prices.

// Order status values.
const (
	OrderStatusPending  = "pending"
	OrderStatusPaid     = "paid"
	OrderStatusFailed   = "failed"
	OrderStatusRefunded = "refunded"
)

// Invoice status values.
const (
	InvoiceStatusDraft = "draft"
	InvoiceStatusOpen  = "open"
	InvoiceStatusPaid  = "paid"
	InvoiceStatusVoid  = "void"
)

type Order struct {
	bun.BaseModel `bun:"table:orders"`

	ID string `bun:",pk" json:"id"`
	// OrderNumber is the human-facing identifier ("HTC-XXXXXXX"). The
	// uuid stays internal; this is what a customer quotes in a ticket.
	OrderNumber   string `bun:",notnull,unique" json:"order_number"`
	CustomerEmail string `bun:",notnull" json:"customer_email"`
	CustomerName  string `bun:",notnull,default:''" json:"customer_name,omitempty"`
	// LicenseID is the license this order created or extended. It is
	// nullable — a checkout has none until fulfilment links one — and
	// deliberately carries no FK: the ledger must survive a license
	// deleted long after the sale.
	LicenseID        string `bun:",nullzero" json:"license_id,omitempty"`
	Currency         string `bun:",notnull" json:"currency"`
	SubtotalMinor    int64  `bun:",notnull" json:"subtotal_minor"`
	DiscountMinor    int64  `bun:",notnull" json:"discount_minor"`
	TaxMinor         int64  `bun:",notnull" json:"tax_minor"`
	TotalMinor       int64  `bun:",notnull" json:"total_minor"`
	CouponCode       string `bun:",notnull,default:''" json:"coupon_code,omitempty"`
	CouponType       string `bun:",notnull,default:''" json:"coupon_type,omitempty"`
	CouponValueBPS   int64  `bun:",notnull,default:0" json:"coupon_value_bps,omitempty"`
	CouponValueMinor int64  `bun:",notnull,default:0" json:"coupon_value_minor,omitempty"`
	// TaxJurisdiction is the joined jurisdiction labels of the rates
	// applied ("US-CA+US-CA-SF") and TaxBasisPoints their summed
	// rate; both are recorded so an old invoice can be explained
	// without the tax tables of the day.
	TaxJurisdiction string `bun:",notnull,default:''" json:"tax_jurisdiction,omitempty"`
	TaxBasisPoints  int64  `bun:",notnull,default:0" json:"tax_basis_points,omitempty"`
	TaxInclusive    bool   `bun:",notnull,default:false" json:"tax_inclusive"`
	Status          string `bun:",notnull,default:'pending'" json:"status"`
	PaymentProvider string `bun:",notnull,default:''" json:"payment_provider,omitempty"`
	ExternalID      string `bun:",notnull,default:''" json:"external_id,omitempty"`
	// IdempotencyKey is the client's key for a retried checkout: the
	// unique index turns a retry into "return the order that was
	// created" instead of a second charge.
	IdempotencyKey string     `bun:",nullzero,unique" json:"idempotency_key,omitempty"`
	PaidAt         *time.Time `json:"paid_at,omitempty"`
	RefundedAt     *time.Time `json:"refunded_at,omitempty"`
	CreatedAt      time.Time  `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt      time.Time  `bun:",nullzero,default:now()" json:"updated_at"`

	Items []*OrderItem `bun:"rel:has-many,join:id=order_id" json:"items,omitempty"`
}

// ─── Order Item ───
//
// One priced line of an order. Like the order totals, the per-line
// money is the snapshot taken at purchase time: quantity, unit price
// and the line's share of discount and tax all add up to LineTotalMinor
// exactly, and the line totals add up to the order total exactly.

type OrderItem struct {
	bun.BaseModel `bun:"table:order_items"`

	ID                string    `bun:",pk" json:"id"`
	OrderID           string    `bun:",notnull" json:"order_id"`
	SKU               string    `bun:",notnull,default:''" json:"sku,omitempty"`
	ProductID         string    `bun:",notnull,default:''" json:"product_id,omitempty"`
	PlanID            string    `bun:",notnull,default:''" json:"plan_id,omitempty"`
	Description       string    `bun:",notnull,default:''" json:"description,omitempty"`
	Quantity          int64     `bun:",notnull" json:"quantity"`
	UnitAmountMinor   int64     `bun:",notnull" json:"unit_amount_minor"`
	LineSubtotalMinor int64     `bun:",notnull" json:"line_subtotal_minor"`
	LineDiscountMinor int64     `bun:",notnull" json:"line_discount_minor"`
	LineTaxMinor      int64     `bun:",notnull" json:"line_tax_minor"`
	LineTotalMinor    int64     `bun:",notnull" json:"line_total_minor"`
	CreatedAt         time.Time `bun:",nullzero,default:now()" json:"created_at"`
}

// ─── Invoice ───
//
// The billing document derived from an order: same money, numbered for
// a customer. Kept simple on purpose — one document per order, drawn
// at creation as a draft and issued (open/paid/void) by the billing
// flow. Its amounts are copied from the order when it is created and
// never recalculated.

type Invoice struct {
	bun.BaseModel `bun:"table:invoices"`

	ID            string     `bun:",pk" json:"id"`
	OrderID       string     `bun:",notnull" json:"order_id"`
	InvoiceNumber string     `bun:",notnull,unique" json:"invoice_number"`
	Status        string     `bun:",notnull,default:'draft'" json:"status"`
	Currency      string     `bun:",notnull" json:"currency"`
	SubtotalMinor int64      `bun:",notnull" json:"subtotal_minor"`
	DiscountMinor int64      `bun:",notnull" json:"discount_minor"`
	TaxMinor      int64      `bun:",notnull" json:"tax_minor"`
	TotalMinor    int64      `bun:",notnull" json:"total_minor"`
	IssuedAt      *time.Time `json:"issued_at,omitempty"`
	DueAt         *time.Time `json:"due_at,omitempty"`
	PaidAt        *time.Time `json:"paid_at,omitempty"`
	CreatedAt     time.Time  `bun:",nullzero,default:now()" json:"created_at"`
}
