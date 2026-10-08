package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/uptrace/bun"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Product reviews ───
//
// The persistence half of the marketplace review queue: one row per
// (product, customer), written pending and published by moderation.
// Reads split by audience — the storefront sees approved rows only
// (the status filter is the whole of that gate), the admin sees the
// queue. Ratings and their aggregates are read here too, computed
// from SUM/COUNT in SQL and rounded by the one model implementation
// (model.RatingAggregateFromSum), so the number a listing renders and
// the number a review page renders cannot drift.

// ErrReviewAlreadyExists is the refusal the unique (product_id,
// customer_email) pair gives: one customer, one review per product.
// The write path folds the raw driver error into this sentinel so
// callers can answer 409 for it and 500 for everything else without
// knowing what a SQLSTATE is — the pattern of ErrCategorySlugTaken.
var ErrReviewAlreadyExists = errors.New("review already exists")

// IsReviewConflict reports whether err is the already-reviewed
// refusal, in either spelling: the sentinel CreateReview returns, or a
// raw unique violation that reached the caller without passing through
// it (a future writer that skips the fold must still not answer 500
// for this). Mirrors IsCategorySlugConflict.
func IsReviewConflict(err error) bool {
	return errors.Is(err, ErrReviewAlreadyExists) || isUniqueViolation(err)
}

// CreateReview inserts a review. The row starts pending — always; the
// parameter's status is ignored, so a submission can never smuggle
// itself onto the storefront — and the author's address is folded so
// the unique pair cannot be evaded by spelling. A second review of the
// same product by the same customer (any spelling) folds into
// ErrReviewAlreadyExists.
func (s *Store) CreateReview(ctx context.Context, r *model.Review) error {
	if r.ID == "" {
		r.ID = newID()
	}
	r.CustomerEmail = model.NormalizeReviewEmail(r.CustomerEmail)
	r.Status = model.ReviewStatusPending
	now := time.Now()
	r.CreatedAt, r.UpdatedAt = now, now
	_, err := s.DB.NewInsert().Model(r).Exec(ctx)
	if isUniqueViolation(err) {
		return ErrReviewAlreadyExists
	}
	return err
}

// FindReviewByID looks one review up. A miss is sql.ErrNoRows so the
// caller can answer 404.
func (s *Store) FindReviewByID(ctx context.Context, id string) (*model.Review, error) {
	r := new(model.Review)
	return r, s.DB.NewSelect().Model(r).Where("id = ?", id).Scan(ctx)
}

// FindReviewByProductAndEmail is the portal ownership lookup: the one
// review this customer wrote for this product (the pair is unique, so
// there is at most one). The email is folded here too, so ownership
// survives any spelling of the session address — and a customer
// looking for somebody else's review simply gets sql.ErrNoRows, which
// is exactly the quiet 404 the portal endpoints answer with.
func (s *Store) FindReviewByProductAndEmail(ctx context.Context, productID, email string) (*model.Review, error) {
	r := new(model.Review)
	return r, s.DB.NewSelect().Model(r).
		Where("product_id = ? AND customer_email = ?", productID, model.NormalizeReviewEmail(email)).
		Scan(ctx)
}

// UpdateReviewStatus stamps a new moderation status. It writes
// blindly — deciding whether the move is legal is
// model.CanTransitionReview's job in the handler — and answers
// sql.ErrNoRows for an id that is not there, so a raced-away row
// cannot be claimed as moderated.
func (s *Store) UpdateReviewStatus(ctx context.Context, id, status string) error {
	res, err := s.DB.NewUpdate().Model((*model.Review)(nil)).
		Set("status = ?", status).
		Set("updated_at = ?", time.Now()).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpdateReviewReply writes (or clears) the vendor's answer under a
// review. An empty reply stores NULL — "no answer" is null, not empty
// string, so the nullable column means what it says and the two shapes
// cannot both exist for the same fact.
func (s *Store) UpdateReviewReply(ctx context.Context, id, reply string) error {
	var v any
	if reply != "" {
		v = reply
	}
	res, err := s.DB.NewUpdate().Model((*model.Review)(nil)).
		Set("admin_reply = ?", v).
		Set("updated_at = ?", time.Now()).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpdateReviewContent rewrites the author's own words (title and
// body). Narrow on purpose: this can move no status and no reply, so
// an edit from the portal cannot race moderation into publishing
// something else. An empty title stores NULL (title is optional).
func (s *Store) UpdateReviewContent(ctx context.Context, id, title, body string) error {
	var t any
	if title != "" {
		t = title
	}
	res, err := s.DB.NewUpdate().Model((*model.Review)(nil)).
		Set("title = ?", t).
		Set("body = ?", body).
		Set("updated_at = ?", time.Now()).
		Where("id = ?", id).
		Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteReview removes a review — the admin's takedown and the
// author's own withdrawal go through the same door. A no-op delete
// answers sql.ErrNoRows so the caller says 404 rather than claim a
// deletion that did not happen.
func (s *Store) DeleteReview(ctx context.Context, id string) error {
	res, err := s.DB.NewDelete().Model((*model.Review)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListReviewsByProduct is one page of a product's reviews, newest
// first, optionally narrowed to one status. The public storefront
// passes model.ReviewStatusApproved and can therefore never see the
// queue; the admin passes whatever it is filtering by (empty = all).
// Ordering matches the (product_id, status, created_at DESC, id DESC)
// index, and the id tiebreak is what makes OFFSET/LIMIT paging line up.
func (s *Store) ListReviewsByProduct(ctx context.Context, productID, status string, p Page) ([]*model.Review, int, error) {
	var out []*model.Review
	q := s.DB.NewSelect().Model(&out).
		Where("product_id = ?", productID).
		OrderExpr("created_at DESC, id DESC")
	if status != "" {
		q = q.Where("status = ?", status)
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

// ListAllReviews is the admin moderation queue across the whole
// catalog: one page of reviews, newest first, optionally narrowed to
// one status. Same ordering and paging contract as the per-product
// list — the queue is "every product's rows" and nothing else changes.
func (s *Store) ListAllReviews(ctx context.Context, status string, p Page) ([]*model.Review, int, error) {
	var out []*model.Review
	q := s.DB.NewSelect().Model(&out).
		OrderExpr("created_at DESC, id DESC")
	if status != "" {
		q = q.Where("status = ?", status)
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

// RatingSummaryForProduct is the approved-only aggregate of one
// product: how many published reviews it has and their average stars
// in bps. Unapproved rows are excluded by the WHERE — the summary a
// listing renders is exactly the summary of what the listing shows.
// A product with no approved reviews reads as the zero value.
func (s *Store) RatingSummaryForProduct(ctx context.Context, productID string) (model.RatingAggregate, error) {
	byID, err := s.RatingSummariesForProducts(ctx, map[string]struct{}{productID: {}})
	if err != nil {
		return model.RatingAggregate{}, err
	}
	return byID[productID], nil
}

// RatingSummariesForProducts is the batch form for listings: the same
// aggregate for many products in ONE query, keyed by product id. The
// listing page is the hottest read in the marketplace and one summary
// query per card is how a catalog dies (the lesson of
// CategoriesForProducts). Products with no approved reviews are
// simply absent from the map — the zero value is their summary.
//
// The query is raw table SQL (no bun model), so it is immune to the
// model-alias pitfall; it returns SUM/COUNT and the rounding happens
// in model.RatingAggregateFromSum, the one implementation of the math.
func (s *Store) RatingSummariesForProducts(ctx context.Context, ids map[string]struct{}) (map[string]model.RatingAggregate, error) {
	out := make(map[string]model.RatingAggregate, len(ids))
	list := make([]string, 0, len(ids))
	for id := range ids {
		if id != "" {
			list = append(list, id)
		}
	}
	if len(list) == 0 {
		return out, nil
	}
	var rows []struct {
		ProductID   string
		ReviewCount int64
		StarSum     int64
	}
	err := s.DB.NewSelect().
		TableExpr("product_reviews").
		ColumnExpr("product_id").
		ColumnExpr("COUNT(*) AS review_count").
		ColumnExpr("COALESCE(SUM(rating), 0) AS star_sum").
		Where("status = ?", model.ReviewStatusApproved).
		Where("product_id IN (?)", bun.List(list)).
		GroupExpr("product_id").
		Scan(ctx, &rows)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out[r.ProductID] = model.RatingAggregateFromSum(r.StarSum, r.ReviewCount)
	}
	return out, nil
}
