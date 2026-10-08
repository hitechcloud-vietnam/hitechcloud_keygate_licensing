package store

import (
	"context"
	"errors"
	"strings"

	"github.com/uptrace/bun"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/license"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Admin search (plan §86) ───
//
// One lookup across the catalog, commerce and partner tables, behind
// GET /api/v1/admin/search?q=&types=&limit=. The vocabulary is the
// closed set below; every type maps to the name/title/number columns
// an operator actually searches by, ILIKE-substring, ordered so the
// most recently written rows lead where that means anything.
//
// Two rules the whole file follows:
//
//   - A licence is searched by its KEY HASH only. The key is a
//     credential (model.License.LicenseKey is JSON-hidden for the same
//     reason), so the query matches `key_hash = <sha256 of the typed
//     key)>` and the hit carries the licence's email/id — never any
//     key material. The plaintext column is deliberately not
//     consulted: one less query that touches a secret, and the
//     BackfillKeyHashes backfill is what makes hash lookup complete.
//
//   - Every hand-written qualifier uses the Bun STRUCT-name alias
//     (model.Order -> `"order"`, model.License -> `license`), never
//     the table name. See bun_alias_test.go for the bug class.

// The searchable result types. Closed vocabulary — a request naming
// anything else is refused at the handler boundary before it reaches
// a query.
const (
	SearchTypeProduct      = "product"
	SearchTypeCustomer     = "customer"
	SearchTypeOrder        = "order"
	SearchTypeInvoice      = "invoice"
	SearchTypeLicense      = "license"
	SearchTypeSubscription = "subscription"
	SearchTypeDevice       = "device"
	SearchTypeReseller     = "reseller"
	SearchTypeAffiliate    = "affiliate"
)

// SearchTypes is every searchable type in canonical result order —
// the order a merged answer groups in when no ?types= filter narrows
// it.
var SearchTypes = []string{
	SearchTypeProduct,
	SearchTypeCustomer,
	SearchTypeOrder,
	SearchTypeInvoice,
	SearchTypeLicense,
	SearchTypeSubscription,
	SearchTypeDevice,
	SearchTypeReseller,
	SearchTypeAffiliate,
}

// ErrUnknownSearchType names a ?types= member outside the closed
// vocabulary. The handler refuses those with 400; the store refuses
// them again so a future caller cannot silently widen the search.
var ErrUnknownSearchType = errors.New("unknown search type")

// SearchHit is one row of a search answer.
//
// Title and Subtitle are display text for the result line; Ref is the
// secondary identifier the handler turns into the SPA link (the order
// an invoice belongs to, the licence a device activated). None of the
// three may carry a credential: a licence hit shows the customer
// email and nothing that could activate a product.
type SearchHit struct {
	Type     string
	ID       string
	Title    string
	Subtitle string
	Ref      string
}

// SearchOptions is one admin search request. Types empty means every
// type in SearchTypes order; Limit is a PER-TYPE cap (the handler
// merges and truncates the combined list to the request's limit).
type SearchOptions struct {
	Query string
	Types []string
	Limit int
}

const (
	searchDefaultLimit = 20
	searchMaxLimit     = 50
)

// searchLike wraps a term for the substring match every type uses.
// The term is a bound parameter everywhere it appears, so % and _ are
// never SQL — at worst they widen the match, which is what a search
// box that accepts them should do anyway.
func searchLike(term string) string {
	return "%" + term + "%"
}

// searchHitRow is the shared shape every per-type statement selects
// into: the four columns of SearchHit minus the type, which the
// caller stamps from the query it ran. Plain struct scanning, the
// same convention as reportScanRow in reports.go.
type searchHitRow struct {
	ID       string `bun:"id"`
	Title    string `bun:"title"`
	Subtitle string `bun:"subtitle"`
	Ref      string `bun:"link_ref"`
}

// searchProductQuery matches products by name or slug.
func searchProductQuery(db *bun.DB, term string, limit int) *bun.SelectQuery {
	like := searchLike(term)
	return db.NewSelect().Model((*model.Product)(nil)).
		ColumnExpr("product.id AS id").
		ColumnExpr("product.name AS title").
		ColumnExpr("product.slug AS subtitle").
		ColumnExpr("product.id AS link_ref").
		Where("product.name ILIKE ? OR product.slug ILIKE ?", like, like).
		OrderExpr("product.name ASC").
		Limit(limit)
}

// searchCustomerQuery matches the account rows behind customers: name
// or email. The email is a fine subtitle — the caller is an admin and
// the admin list shows it already.
func searchCustomerQuery(db *bun.DB, term string, limit int) *bun.SelectQuery {
	like := searchLike(term)
	return db.NewSelect().Model((*model.User)(nil)).
		ColumnExpr(`"user".id AS id`).
		ColumnExpr(`COALESCE(NULLIF("user".name, ''), "user".email) AS title`).
		ColumnExpr(`"user".email AS subtitle`).
		ColumnExpr(`"user".email AS link_ref`).
		Where(`"user".name ILIKE ? OR "user".email ILIKE ?`, like, like).
		OrderExpr(`"user".created_at DESC`).
		Limit(limit)
}

// searchOrderQuery matches orders by number, customer email or
// customer name. The number is what a customer quotes in a ticket, so
// it leads.
func searchOrderQuery(db *bun.DB, term string, limit int) *bun.SelectQuery {
	like := searchLike(term)
	return db.NewSelect().Model((*model.Order)(nil)).
		ColumnExpr(`"order".id AS id`).
		ColumnExpr(`"order".order_number AS title`).
		ColumnExpr(`"order".customer_email AS subtitle`).
		ColumnExpr(`"order".id AS link_ref`).
		Where(`"order".order_number ILIKE ? OR "order".customer_email ILIKE ? OR "order".customer_name ILIKE ?`,
			like, like, like).
		OrderExpr(`"order".created_at DESC`).
		Limit(limit)
}

// searchInvoiceQuery matches invoices by their own number OR by the
// number of the order they were drawn for — "find the invoice for
// HTC-123" is the question this answers. The order row is joined
// under the struct-name alias `"order"` so its number can subtitle
// the hit and its id can link to the order page the invoice lives on.
func searchInvoiceQuery(db *bun.DB, term string, limit int) *bun.SelectQuery {
	like := searchLike(term)
	return db.NewSelect().Model((*model.Invoice)(nil)).
		ColumnExpr("invoice.id AS id").
		ColumnExpr("invoice.invoice_number AS title").
		ColumnExpr(`"order".order_number AS subtitle`).
		ColumnExpr(`"order".id AS link_ref`).
		Join(`LEFT JOIN orders AS "order" ON "order".id = invoice.order_id`).
		Where("invoice.invoice_number ILIKE ? OR \"order\".order_number ILIKE ?", like, like).
		OrderExpr("invoice.created_at DESC").
		Limit(limit)
}

// searchLicenseQuery matches licences by customer email, org name or
// external customer id — and by KEY HASH when the term is a full
// licence key. The key itself is never selected, never compared in
// plaintext and never echoed: the hit is the licence's identity, not
// its credential.
func searchLicenseQuery(db *bun.DB, term string, limit int) *bun.SelectQuery {
	like := searchLike(term)
	return db.NewSelect().Model((*model.License)(nil)).
		ColumnExpr("license.id AS id").
		ColumnExpr("license.email AS title").
		ColumnExpr("COALESCE(NULLIF(license.org_name, ''), license.status) AS subtitle").
		ColumnExpr("license.id AS link_ref").
		Where("license.email ILIKE ? OR license.org_name ILIKE ? OR license.external_customer_id ILIKE ? OR license.key_hash = ?",
			like, like, like, license.HashKey(term)).
		OrderExpr("license.created_at DESC").
		Limit(limit)
}

// searchSubscriptionQuery matches subscriptions by external id or id,
// and by the email of the licence they bill. A subscription has no
// name of its own, so its title is the external id when the merchant
// mapped one and the row id otherwise.
func searchSubscriptionQuery(db *bun.DB, term string, limit int) *bun.SelectQuery {
	like := searchLike(term)
	return db.NewSelect().Model((*model.Subscription)(nil)).
		ColumnExpr("subscription.id AS id").
		ColumnExpr("COALESCE(NULLIF(subscription.external_id, ''), subscription.id) AS title").
		ColumnExpr("license.email AS subtitle").
		ColumnExpr("subscription.license_id AS link_ref").
		Join("LEFT JOIN licenses AS license ON license.id = subscription.license_id").
		Where("subscription.external_id ILIKE ? OR subscription.id ILIKE ? OR license.email ILIKE ?",
			like, like, like).
		OrderExpr("subscription.created_at DESC").
		Limit(limit)
}

// searchDeviceQuery matches device activations by identifier, label
// or the email of the licence that activated them. The identifier is
// a machine fingerprint the admin screens already show — not a
// credential — so it may lead the hit when no label was set.
func searchDeviceQuery(db *bun.DB, term string, limit int) *bun.SelectQuery {
	like := searchLike(term)
	return db.NewSelect().Model((*model.Activation)(nil)).
		ColumnExpr("activation.id AS id").
		ColumnExpr("COALESCE(NULLIF(activation.label, ''), activation.identifier) AS title").
		ColumnExpr("license.email AS subtitle").
		ColumnExpr("activation.license_id AS link_ref").
		Join("LEFT JOIN licenses AS license ON license.id = activation.license_id").
		Where("activation.identifier ILIKE ? OR activation.label ILIKE ? OR license.email ILIKE ?",
			like, like, like).
		OrderExpr("activation.created_at DESC").
		Limit(limit)
}

// searchResellerQuery matches partner accounts by name or contact
// email — the folded handle the admin list searches by.
func searchResellerQuery(db *bun.DB, term string, limit int) *bun.SelectQuery {
	like := searchLike(term)
	return db.NewSelect().Model((*model.Reseller)(nil)).
		ColumnExpr("reseller.id AS id").
		ColumnExpr("reseller.name AS title").
		ColumnExpr("reseller.contact_email AS subtitle").
		ColumnExpr("reseller.id AS link_ref").
		Where("reseller.name ILIKE ? OR reseller.contact_email ILIKE ?", like, like).
		OrderExpr("reseller.name ASC").
		Limit(limit)
}

// searchAffiliateQuery matches promoter accounts by name or contact
// email. Referral codes are deliberately not searched here: a code is
// a public handle that redirects, and the palette has no business
// turning one into an affiliate id.
func searchAffiliateQuery(db *bun.DB, term string, limit int) *bun.SelectQuery {
	like := searchLike(term)
	return db.NewSelect().Model((*model.Affiliate)(nil)).
		ColumnExpr("affiliate.id AS id").
		ColumnExpr("affiliate.name AS title").
		ColumnExpr("affiliate.contact_email AS subtitle").
		ColumnExpr("affiliate.id AS link_ref").
		Where("affiliate.name ILIKE ? OR affiliate.contact_email ILIKE ?", like, like).
		OrderExpr("affiliate.name ASC").
		Limit(limit)
}

// searchTypeQuery resolves a type to its statement. Unknown types
// answer nil, and the caller refuses them — nothing user-typed ever
// reaches a switch that could build SQL out of it.
func searchTypeQuery(db *bun.DB, typ, term string, limit int) *bun.SelectQuery {
	switch typ {
	case SearchTypeProduct:
		return searchProductQuery(db, term, limit)
	case SearchTypeCustomer:
		return searchCustomerQuery(db, term, limit)
	case SearchTypeOrder:
		return searchOrderQuery(db, term, limit)
	case SearchTypeInvoice:
		return searchInvoiceQuery(db, term, limit)
	case SearchTypeLicense:
		return searchLicenseQuery(db, term, limit)
	case SearchTypeSubscription:
		return searchSubscriptionQuery(db, term, limit)
	case SearchTypeDevice:
		return searchDeviceQuery(db, term, limit)
	case SearchTypeReseller:
		return searchResellerQuery(db, term, limit)
	case SearchTypeAffiliate:
		return searchAffiliateQuery(db, term, limit)
	}
	return nil
}

// ValidSearchType reports whether typ is a member of the closed
// vocabulary.
func ValidSearchType(typ string) bool {
	for _, t := range SearchTypes {
		if t == typ {
			return true
		}
	}
	return false
}

// Search runs one lookup per requested type (every type when none is
// named) and answers the hits grouped in request order. The LIMIT is
// per type: the handler merges the groups and truncates the combined
// list to the request's own limit, so one busy type cannot starve the
// others out of the first page.
//
// An empty term answers an empty list without touching the database —
// "everything" is not a search.
func (s *Store) Search(ctx context.Context, opts SearchOptions) ([]SearchHit, error) {
	term := strings.TrimSpace(opts.Query)
	if term == "" {
		return []SearchHit{}, nil
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = searchDefaultLimit
	}
	if limit > searchMaxLimit {
		limit = searchMaxLimit
	}
	types := opts.Types
	if len(types) == 0 {
		types = SearchTypes
	}
	// Every type is validated up front: one bad member must refuse
	// the request before any statement runs, not after some of them
	// have already touched the database.
	for _, typ := range types {
		if !ValidSearchType(typ) {
			return nil, ErrUnknownSearchType
		}
	}

	out := []SearchHit{}
	for _, typ := range types {
		q := searchTypeQuery(s.DB, typ, term, limit)
		if q == nil {
			return nil, ErrUnknownSearchType
		}
		var rows []searchHitRow
		if err := q.Scan(ctx, &rows); err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, SearchHit{
				Type:     typ,
				ID:       r.ID,
				Title:    r.Title,
				Subtitle: r.Subtitle,
				Ref:      r.Ref,
			})
		}
	}
	return out, nil
}
