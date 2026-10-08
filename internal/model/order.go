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

// Invoice status values added by the purchase-order / invoice
// workflow. They extend — never replace — the four above: the strings
// live in invoices.status, so existing rows keep scanning and the
// database CHECK was widened (20261008_138000_po_workflow), not
// rewritten.
const (
	// InvoiceStatusUncollectible is an issued (open) invoice the
	// billing flow has given up collecting. Recovery is still
	// possible: back to open when collection resumes, to paid when
	// the debt settles late.
	InvoiceStatusUncollectible = "uncollectible"
	// InvoiceStatusRefunded is a paid invoice whose money went back
	// to the customer. Terminal.
	InvoiceStatusRefunded = "refunded"
)

// CanTransitionInvoice reports whether an invoice may move from
// status from to status to. The machine, in full:
//
//	draft         -> open (issue)        | void (cancel before issue)
//	open          -> paid (settle)       | void (cancel)
//	              | uncollectible (give up collection)
//	uncollectible -> open (resume)       | paid (settles late)
//	paid          -> refunded (money returned)
//	void, refunded are terminal.
//
// Two refusals are load-bearing for the API built on this:
//
//   - paid -> void is refused: money changed hands, so the only way
//     off paid is the refund the order's Refund endpoint performs
//     (paid -> refunded). Voiding a paid invoice would erase a real
//     charge from the books.
//   - paid -> uncollectible is refused: an invoice that was PAID is
//     collected by definition.
//
// Self-transitions are refused too — re-running a transition should
// surface, not silently succeed.
func CanTransitionInvoice(from, to string) bool {
	switch from {
	case InvoiceStatusDraft:
		return to == InvoiceStatusOpen || to == InvoiceStatusVoid
	case InvoiceStatusOpen:
		return to == InvoiceStatusPaid || to == InvoiceStatusVoid ||
			to == InvoiceStatusUncollectible
	case InvoiceStatusUncollectible:
		return to == InvoiceStatusOpen || to == InvoiceStatusPaid
	case InvoiceStatusPaid:
		return to == InvoiceStatusRefunded
	}
	// void and refunded are terminal; anything unknown never moves.
	return false
}

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

	// Billing block (purchase-order / invoice workflow): who the
	// invoice is drawn for and the commercial references that travel
	// with it. Nullable and additive — an order created before this
	// block existed (or by a flow that never sets it, like the payment
	// ledger) has every one of these NULL. An empty string is stored
	// as NULL (bun nullzero) and omitted from JSON, so "no value" has
	// exactly one representation in the database and on the wire.
	//
	// Values are validated and folded on write by the admin billing
	// endpoint (PATCH /admin/orders/:id/billing), not here.
	BillingName         string `bun:",nullzero" json:"billing_name,omitempty"`
	BillingCompany      string `bun:",nullzero" json:"billing_company,omitempty"`
	BillingAddressLine1 string `bun:",nullzero" json:"billing_address_line1,omitempty"`
	BillingAddressLine2 string `bun:",nullzero" json:"billing_address_line2,omitempty"`
	BillingCity         string `bun:",nullzero" json:"billing_city,omitempty"`
	BillingRegion       string `bun:",nullzero" json:"billing_region,omitempty"`
	BillingPostalCode   string `bun:",nullzero" json:"billing_postal_code,omitempty"`
	// BillingCountry is an ISO 3166-1 alpha-2 code ("VN", "US"),
	// folded to upper case on write.
	BillingCountry string `bun:",nullzero" json:"billing_country,omitempty"`
	// CustomerTaxID is the customer's VAT / tax identifier, validated
	// loosely on write: 8–20 characters of letters, digits and
	// dashes (the shape most VAT numbers share, without pretending to
	// validate any country's scheme).
	CustomerTaxID string `bun:",nullzero" json:"customer_tax_id,omitempty"`
	// PONumber is the customer's purchase-order reference, quoted on
	// the invoice so their AP can match it (max 64 chars).
	PONumber string `bun:",nullzero" json:"po_number,omitempty"`
	// BillingEmail is the invoicing contact when it differs from
	// CustomerEmail.
	BillingEmail string `bun:",nullzero" json:"billing_email,omitempty"`

	// ── Checkout attribution (Phase 7, checkout slice) ──
	//
	// Who brought this sale: the reseller it is attributed to, the
	// affiliate referral code it arrived through, and the affiliate
	// account behind that code. Stamped at checkout time from the
	// attribution the buyer arrived with (payment/attribution.go) and
	// read back into the ledger when the payment settles — snapshots,
	// exactly like the coupon/tax columns: never re-derived from the
	// live partner tables, which may have moved or been deleted since.
	//
	// All four are nullable TEXT with deliberately NO foreign keys,
	// same doctrine as commissions.order_id and
	// affiliate_conversions.order_id: these are commercial records of
	// a sale and must survive the partner account being deleted (and
	// any order-retention purge). Empty is stored as NULL (bun
	// nullzero) and omitted from JSON.
	ResellerID string `bun:",nullzero" json:"reseller_id,omitempty"`
	// ResellerEmail is the partner's contact address at sale time —
	// the human-readable half of the attribution, kept so the order
	// still names the partner after the account is gone.
	ResellerEmail string `bun:",nullzero" json:"reseller_email,omitempty"`
	// ReferralCode is the affiliate handle the buyer arrived with
	// (?ref= or the htc_ref cookie), stored in its canonical folded
	// form (model.NormalizeReferralCode).
	ReferralCode string `bun:",nullzero" json:"referral_code,omitempty"`
	// AffiliateID is the affiliate that code belonged to, when it
	// resolved at stamping time.
	AffiliateID string `bun:",nullzero" json:"affiliate_id,omitempty"`

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

	// VoidedAt and UncollectibleAt are stamped by the state
	// transitions the PO/invoice workflow added (void,
	// uncollectible). Nullable and additive: a row predating them
	// scans fine with both nil. The transition they belong to is
	// decided by CanTransitionInvoice.
	VoidedAt        *time.Time `json:"voided_at,omitempty"`
	UncollectibleAt *time.Time `json:"uncollectible_at,omitempty"`
}
