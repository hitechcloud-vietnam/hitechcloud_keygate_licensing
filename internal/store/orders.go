package store

import (
	"context"
	"errors"
	"time"

	"github.com/uptrace/bun"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Orders ───
//
// The commerce ledger: an order with its line items, and the invoice
// derived from it. Money is int64 minor units end to end; this layer
// never computes with it, it only persists what the order service
// calculated.

var (
	// ErrInvoiceNotFound is returned by InvoiceByOrder when the order
	// has no invoice yet.
	ErrInvoiceNotFound = errors.New("invoice not found")

	// ErrInvoiceNotUnique is the InvoiceByOrder guard: one order is
	// meant to carry one invoice, so a lookup that matches more than
	// one refuses to pick rather than silently returning whichever
	// row the database felt like serving.
	ErrInvoiceNotUnique = errors.New("order has more than one invoice")
)

// CreateOrder writes the order and, when it carries items, its line
// items — in one transaction, because items without their order (or an
// order half of whose items went missing) would break every total the
// ledger promises. IDs are allocated here for any row still missing
// one, and each item is pinned to the order's id.
func (s *Store) CreateOrder(ctx context.Context, o *model.Order) error {
	if o.ID == "" {
		o.ID = newID()
	}
	for _, it := range o.Items {
		if it.ID == "" {
			it.ID = newID()
		}
		it.OrderID = o.ID
	}
	return RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewInsert().Model(o).Exec(ctx); err != nil {
			return err
		}
		if len(o.Items) > 0 {
			if _, err := tx.NewInsert().Model(&o.Items).Exec(ctx); err != nil {
				return err
			}
		}
		return nil
	})
}

// FindOrderByID returns the order and its line items. The items ride
// along because an order without them cannot be reconciled against
// anything.
func (s *Store) FindOrderByID(ctx context.Context, id string) (*model.Order, error) {
	o := new(model.Order)
	return o, s.DB.NewSelect().Model(o).Relation("Items").
		Where(`"order".id = ?`, id).Scan(ctx)
}

// FindOrderByNumber looks an order up by its human-facing number.
func (s *Store) FindOrderByNumber(ctx context.Context, num string) (*model.Order, error) {
	o := new(model.Order)
	return o, s.DB.NewSelect().Model(o).Relation("Items").
		Where(`"order".order_number = ?`, num).Scan(ctx)
}

// FindOrderByExternalID finds the order a payment provider is talking
// about. The provider is part of the key: two providers may hand out
// the same identifier, and a webhook for one must never land on the
// other's order.
func (s *Store) FindOrderByExternalID(ctx context.Context, provider, externalID string) (*model.Order, error) {
	o := new(model.Order)
	q := s.DB.NewSelect().Model(o)
	if provider != "" {
		q = q.Where("payment_provider = ?", provider)
	}
	return o, q.Where("external_id = ?", externalID).Scan(ctx)
}

// FindOrderByIdempotencyKey returns the order a previous attempt with
// this client key created, so a retried checkout replays instead of
// charging twice.
func (s *Store) FindOrderByIdempotencyKey(ctx context.Context, key string) (*model.Order, error) {
	o := new(model.Order)
	return o, s.DB.NewSelect().Model(o).Where("idempotency_key = ?", key).Scan(ctx)
}

// ordersListQuery builds the admin order ledger listing. Bun aliases
// the model by the snake_case of the STRUCT name (FROM "orders" AS
// "order"), so qualifiers must use "order" — quoted, it is a
// reserved word. The sort is the handler's validated ordering; an
// empty one takes the ledger's own default, newest first.
func ordersListQuery(db bun.IDB, search, status string, sort Sort, dest *[]*model.Order) *bun.SelectQuery {
	if sort.Expr == "" {
		sort = Sort{Expr: `"order".created_at`, Desc: true}
	}
	q := db.NewSelect().Model(dest)
	if status != "" {
		q = q.Where(`"order".status = ?`, status)
	}
	if search != "" {
		q = q.Where(`"order".customer_email ILIKE ? OR "order".order_number ILIKE ?`,
			"%"+search+"%", "%"+search+"%")
	}
	return applySort(q, sort, `"order".id`)
}

// ListOrders returns one page of orders and how many the filter
// matched. The search covers what an admin actually types: a customer
// email or an order number. sort is the validated ordering the caller
// asked for; the zero value takes the default, newest first.
func (s *Store) ListOrders(ctx context.Context, search, status string, p Page, sort Sort) ([]*model.Order, int, error) {
	var out []*model.Order
	q := ordersListQuery(s.DB, search, status, sort, &out)
	total, err := scanPage(ctx, q, p)
	if err != nil {
		return nil, 0, err
	}
	if p.Limit <= 0 {
		total = len(out)
	}
	return out, total, nil
}

// UpdateOrderStatus moves an order through its lifecycle. The two
// instants are optional because only some transitions set them: a
// refund stamps refunded_at and must not rewrite the moment the order
// was paid, which is history.
func (s *Store) UpdateOrderStatus(ctx context.Context, id, status string, paidAt, refundedAt *time.Time) error {
	q := s.DB.NewUpdate().Model((*model.Order)(nil)).
		Set("status = ?", status).
		Set("updated_at = now()").
		Where("id = ?", id)
	if paidAt != nil {
		q = q.Set("paid_at = ?", *paidAt)
	}
	if refundedAt != nil {
		q = q.Set("refunded_at = ?", *refundedAt)
	}
	_, err := q.Exec(ctx)
	return err
}

// ─── Invoices ───

// CreateInvoice writes the invoice. The number is generated by the
// caller (the order service), so a collision surfaces as a unique
// violation there and is retried with a fresh number.
func (s *Store) CreateInvoice(ctx context.Context, inv *model.Invoice) error {
	if inv.ID == "" {
		inv.ID = newID()
	}
	_, err := s.DB.NewInsert().Model(inv).Exec(ctx)
	return err
}

// FindInvoiceByID returns one invoice by its id.
func (s *Store) FindInvoiceByID(ctx context.Context, id string) (*model.Invoice, error) {
	inv := new(model.Invoice)
	return inv, s.DB.NewSelect().Model(inv).Where("id = ?", id).Scan(ctx)
}

// invoicesByOrderQuery builds the per-order invoice listing. An
// empty sort keeps the order the ledger has always read in — oldest
// first, the order the documents were drawn in.
func invoicesByOrderQuery(db bun.IDB, orderID string, sort Sort, dest *[]*model.Invoice) *bun.SelectQuery {
	if sort.Expr == "" {
		sort = Sort{Expr: "invoice.created_at"}
	}
	q := db.NewSelect().Model(dest).Where("order_id = ?", orderID)
	return applySort(q, sort, "invoice.id")
}

// ListInvoicesByOrder returns every invoice drawn against an order,
// oldest first by default (or in the validated order the caller asked
// for).
func (s *Store) ListInvoicesByOrder(ctx context.Context, orderID string, sort Sort) ([]*model.Invoice, error) {
	var out []*model.Invoice
	if err := invoicesByOrderQuery(s.DB, orderID, sort, &out).Scan(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// InvoiceByOrder returns the order's invoice with a uniqueness guard:
// zero rows is ErrInvoiceNotFound and more than one is
// ErrInvoiceNotUnique, so a caller that means "the invoice of this
// order" can never be handed an arbitrary pick from a corrupted
// ledger. Read at most two rows — enough to tell one from many.
func (s *Store) InvoiceByOrder(ctx context.Context, orderID string) (*model.Invoice, error) {
	var out []*model.Invoice
	err := s.DB.NewSelect().Model(&out).
		Where("order_id = ?", orderID).
		OrderExpr("created_at ASC, id ASC").
		Limit(2).Scan(ctx)
	if err != nil {
		return nil, err
	}
	switch len(out) {
	case 1:
		return out[0], nil
	case 0:
		return nil, ErrInvoiceNotFound
	default:
		return nil, ErrInvoiceNotUnique
	}
}

// ─── Billing / invoice state (PO workflow) ───

// UpdateOrderBilling replaces the order's billing block — the billing
// address, tax id, purchase-order number and invoicing contact. The
// caller (the admin PATCH endpoint) has already validated and folded
// the values; this only writes them. An empty value is written as NULL
// (NULLIF), so "cleared" has exactly one representation in the ledger
// and the nullable columns carry it. Only the billing columns and
// updated_at move — the money, status and history of the order are not
// this method's business.
func (s *Store) UpdateOrderBilling(ctx context.Context, id string, o *model.Order) error {
	q := s.DB.NewUpdate().Model((*model.Order)(nil)).
		Set("billing_name = NULLIF(?, '')", o.BillingName).
		Set("billing_company = NULLIF(?, '')", o.BillingCompany).
		Set("billing_address_line1 = NULLIF(?, '')", o.BillingAddressLine1).
		Set("billing_address_line2 = NULLIF(?, '')", o.BillingAddressLine2).
		Set("billing_city = NULLIF(?, '')", o.BillingCity).
		Set("billing_region = NULLIF(?, '')", o.BillingRegion).
		Set("billing_postal_code = NULLIF(?, '')", o.BillingPostalCode).
		Set("billing_country = NULLIF(?, '')", o.BillingCountry).
		Set("customer_tax_id = NULLIF(?, '')", o.CustomerTaxID).
		Set("po_number = NULLIF(?, '')", o.PONumber).
		Set("billing_email = NULLIF(?, '')", o.BillingEmail).
		Set("updated_at = now()").
		Where("id = ?", id)
	_, err := q.Exec(ctx)
	return err
}

// UpdateInvoiceStatus moves an invoice to a new status and stamps the
// instant that move implies — voided_at for a void, uncollectible_at
// for a give-up. Like UpdateOrderStatus it writes what it is told: the
// transition guard (model.CanTransitionInvoice) is the caller's
// business, and the optional instants are optional because only some
// transitions stamp one.
func (s *Store) UpdateInvoiceStatus(ctx context.Context, id, status string, voidedAt, uncollectibleAt *time.Time) error {
	q := s.DB.NewUpdate().Model((*model.Invoice)(nil)).
		Set("status = ?", status).
		Where("id = ?", id)
	if voidedAt != nil {
		q = q.Set("voided_at = ?", *voidedAt)
	}
	if uncollectibleAt != nil {
		q = q.Set("uncollectible_at = ?", *uncollectibleAt)
	}
	_, err := q.Exec(ctx)
	return err
}
