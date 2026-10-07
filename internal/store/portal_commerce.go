package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Customer portal commerce (read-only) ───
//
// The queries behind the customer portal's orders, invoices and
// downloads pages. Everything here is a lookup over the EXISTING
// commerce ledger (orders, order_items, invoices), the licence table
// and the release tables — nothing is written and no table is owned
// by this file.
//
// Ownership is one rule everywhere: an order belongs to the customer
// whose session email matches orders.customer_email case-insensitively
// (the portal session carries the lower-cased sign-in address, while a
// licence or an order keeps the address as it was typed). A fetch that
// would cross that line answers exactly like "no such row" — sql.
// ErrNoRows — so the endpoints can never be used to probe which ids
// exist for somebody else.
//
// Bun aliases a model by the snake_case of its struct name, so the
// orders table is addressed as "order" here — quoted, because ORDER
// is a SQL reserved word and the alias cannot be referenced unquoted.

// ListOrdersByEmail returns one page of the customer's orders and how
// many the filter matched, newest first. An optional status narrows it
// to one lifecycle state.
func (s *Store) ListOrdersByEmail(ctx context.Context, email, status string, p Page) ([]*model.Order, int, error) {
	e := normalizeEmail(email)
	if e == "" {
		return nil, 0, nil
	}
	var out []*model.Order
	q := s.DB.NewSelect().Model(&out).
		Where(`lower("order".customer_email) = ?`, e).
		OrderExpr(`"order".created_at DESC, "order".id DESC`)
	if status != "" {
		q = q.Where(`"order".status = ?`, status)
	}
	total, err := scanPage(ctx, q, p)
	if err != nil {
		return nil, 0, err
	}
	if p.Limit <= 0 {
		total = len(out)
	}
	return out, total, nil
}

// CountOrdersByEmail counts the customer's orders without fetching
// them — the number a list response's total is worked out from when
// the rows themselves are not needed.
func (s *Store) CountOrdersByEmail(ctx context.Context, email, status string) (int, error) {
	e := normalizeEmail(email)
	if e == "" {
		return 0, nil
	}
	q := s.DB.NewSelect().Model((*model.Order)(nil)).
		Where(`lower("order".customer_email) = ?`, e)
	if status != "" {
		q = q.Where(`"order".status = ?`, status)
	}
	return q.Count(ctx)
}

// FindOrderByIdAndEmail returns the order with its line items, but
// only when the order belongs to this customer. An id that does not
// exist and an id that belongs to somebody else are the same answer —
// sql.ErrNoRows — so a portal session cannot tell the two apart.
func (s *Store) FindOrderByIdAndEmail(ctx context.Context, id, email string) (*model.Order, error) {
	e := normalizeEmail(email)
	if e == "" {
		return nil, sql.ErrNoRows
	}
	o := new(model.Order)
	return o, s.DB.NewSelect().Model(o).Relation("Items").
		Where(`"order".id = ?`, id).
		Where(`lower("order".customer_email) = ?`, e).
		Scan(ctx)
}

// FindInvoiceForEmail returns one invoice, but only when the order it
// was drawn against belongs to this customer. The ownership test rides
// on the join, so the invoice row itself is never looked at first —
// a cross-customer id fails the same way a missing one does.
func (s *Store) FindInvoiceForEmail(ctx context.Context, invoiceID, email string) (*model.Invoice, error) {
	e := normalizeEmail(email)
	if e == "" {
		return nil, sql.ErrNoRows
	}
	inv := new(model.Invoice)
	return inv, s.DB.NewSelect().Model(inv).
		Join("JOIN orders ON orders.id = invoice.order_id").
		Where("invoice.id = ?", invoiceID).
		Where("lower(orders.customer_email) = ?", e).
		Scan(ctx)
}

// ListPortalOwnedLicenses returns the licences this customer owns —
// matched on the licence's own email, not on seats: the downloads page
// is entitled by what the customer bought. Plan and product ride along
// because the download gate needs both (grace days, product type).
func (s *Store) ListPortalOwnedLicenses(ctx context.Context, email string) ([]*model.License, error) {
	e := normalizeEmail(email)
	if e == "" {
		return nil, nil
	}
	var out []*model.License
	err := s.DB.NewSelect().Model(&out).
		Relation("Plan").Relation("Product").
		Where("lower(license.email) = ?", e).
		OrderExpr("license.created_at DESC, license.id DESC").
		Scan(ctx)
	return out, err
}

// ListPortalDownloadReleases returns published, non-yanked releases of
// a product that carry at least one uploaded artifact, newest first,
// with their artifacts preloaded — the release set a licensed customer
// may download from.
//
// publishedBefore is the licence's maintenance cutoff: a release
// published after the period ended is not theirs to install, and the
// predicate belongs in the query (same reasoning as
// ListPublishedReleasesForFeed) so the newest rows being too new can
// never hide the ones that are entitled. A NULL published_at never
// satisfies it, matching the feed gate exactly.
//
// The uploaded-artifact predicates mirror model.ReleaseArtifact.
// IsUploaded — a release whose binaries never finished their upload
// cycle offers nothing to download.
func (s *Store) ListPortalDownloadReleases(ctx context.Context, productID string, publishedBefore *time.Time, channel, platform string, limit int) ([]*model.Release, error) {
	if productID == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	const uploaded = `ra.file_key <> '' AND ra.sha256 <> ''`
	var out []*model.Release
	q := s.DB.NewSelect().Model(&out).
		Relation("Artifacts").
		Where("release.product_id = ?", productID).
		Where("release.status = ?", model.ReleaseStatusPublished).
		Where(`EXISTS (SELECT 1 FROM release_artifacts ra
			WHERE ra.release_id = release.id AND ` + uploaded + `)`)
	if channel != "" {
		q = q.Where("release.channel = ?", channel)
	}
	if platform != "" {
		q = q.Where(`EXISTS (SELECT 1 FROM release_artifacts ra
			WHERE ra.release_id = release.id AND ra.platform = ? AND `+uploaded+`)`, platform)
	}
	if publishedBefore != nil {
		q = q.Where("release.published_at <= ?", *publishedBefore)
	}
	err := q.OrderExpr("release.published_at DESC, release.id DESC").
		Limit(limit).
		Scan(ctx)
	return out, err
}
