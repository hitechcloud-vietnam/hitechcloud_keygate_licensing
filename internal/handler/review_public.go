package handler

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// ─── Product reviews, customer side (portal) ───
//
// The signed-in customer's review of a product, on the portal:
//
//	POST   /portal/products/:id/reviews   write one (starts pending)
//	PATCH  /portal/products/:id/reviews   edit own title/body
//	DELETE /portal/products/:id/reviews   take own review down
//
// Identity — no new authentication is invented here. The portal
// session (middleware.SessionAuth) leaves the signed-in user's email
// in the context, exactly as for every other /portal route, and the
// review is attributed to that address (folded). Ownership is the
// (product, session email) pair — one review per customer per product
// — so there is no request parameter that names a review to reach
// across: a customer can only ever see and touch their OWN row, and a
// lookup that finds nothing (including somebody else's review) is the
// same quiet 404.
//
// The review starts pending and stays moderated: submissions never
// publish themselves (store.CreateReview forces the status), and the
// public storefront reads approved rows only. Whether the author ever
// BUUGHT the product is not checked here — "verified purchase" is a
// documented future flag (the order ledger can answer it); today the
// review is attributed to the account that wrote it and a human
// moderates before publication.
//
// Editing rewrites the author's own words and nothing else: title and
// body only, no status and no rating move (a rating is what it was
// when moderated — see model.CanTransitionReview). Re-moderation on
// edit is the same documented future flag.
type ReviewPortalHandler struct {
	store reviewPortalStore
}

// productRefLookup is the product lookup the review surfaces share.
// The storefront's own detail route calls the segment :slug while the
// review routes call it :id; accepting either spelling — the id
// first, then the folded slug — means the endpoints answer whichever
// way the Lead mounts them (and a caller who pastes a slug for an id
// still lands on the product).
type productRefLookup interface {
	FindProductByID(ctx context.Context, id string) (*model.Product, error)
	FindProductBySlug(ctx context.Context, slug string) (*model.Product, error)
}

// findProductByRef resolves the product a review route addresses. A
// ref that is neither — and a lookup that failed — read the same, so
// the endpoint is never an existence oracle for ids or slugs (the
// PublicPlansHandler convention).
func findProductByRef(ctx context.Context, lu productRefLookup, ref string) (*model.Product, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, sql.ErrNoRows
	}
	p, err := lu.FindProductByID(ctx, ref)
	if errors.Is(err, sql.ErrNoRows) {
		return lu.FindProductBySlug(ctx, strings.ToLower(ref))
	}
	return p, err
}

// reviewRefParam reads the product segment whatever the route called
// it: :id on the review routes, :slug if the Lead mounts them under
// the existing marketplace wildcard (gin requires ONE name per
// segment, so the mounting decides which this sees).
func reviewRefParam(c *gin.Context) string {
	if v := c.Param("id"); v != "" {
		return v
	}
	return c.Param("slug")
}

// reviewPortalStore is the slice of store.Store this handler needs.
// The real constructor takes *store.Store; tests substitute a fake so
// the ownership rules run without a database.
type reviewPortalStore interface {
	productRefLookup
	CreateReview(ctx context.Context, r *model.Review) error
	FindReviewByProductAndEmail(ctx context.Context, productID, email string) (*model.Review, error)
	UpdateReviewContent(ctx context.Context, id, title, body string) error
	DeleteReview(ctx context.Context, id string) error
}

var _ reviewPortalStore = (*store.Store)(nil)

// NewReviewPortalHandler wires the handler to the store.
func NewReviewPortalHandler(s *store.Store) *ReviewPortalHandler {
	return &ReviewPortalHandler{store: s}
}

// ownReview resolves the review the session user wrote for the
// addressed product. The lookup is BY the session identity, so it
// cannot name anybody else's review — cross-user reach is impossible
// by construction, and "no review of yours here" is the same quiet 404
// as a product that has none. On failure the response is written.
func (h *ReviewPortalHandler) ownReview(c *gin.Context, productID, email string) (*model.Review, bool) {
	r, err := h.store.FindReviewByProductAndEmail(c.Request.Context(), productID, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.NotFound(c, "review not found")
			return nil, false
		}
		response.Internal(c, err)
		return nil, false
	}
	return r, true
}

// reviewProduct resolves the addressed product, or writes its 404.
func (h *ReviewPortalHandler) reviewProduct(c *gin.Context) (*model.Product, bool) {
	p, err := findProductByRef(c.Request.Context(), h.store, reviewRefParam(c))
	if err != nil {
		response.NotFound(c, "product not found")
		return nil, false
	}
	return p, true
}

// validateReviewContent trims and bounds the author's words — the
// same limits the migration CHECKs (model.MaxReviewTitleLen /
// MaxReviewBodyLen), refused here as a 400 before anything is stored.
// A title may be empty (it is optional); the body may not, which the
// callers enforce on the trimmed result.
func validateReviewContent(c *gin.Context, title, body string) (string, string, bool) {
	title = strings.TrimSpace(title)
	if len(title) > model.MaxReviewTitleLen {
		response.BadRequest(c, "title must be at most 120 characters")
		return "", "", false
	}
	body = strings.TrimSpace(body)
	if len(body) > model.MaxReviewBodyLen {
		response.BadRequest(c, "body must be at most 4000 characters")
		return "", "", false
	}
	return title, body, true
}

// Create — POST /portal/products/:id/reviews
//
// Body: { rating 1..5, title?, body }. One review per (product,
// customer); the row starts pending and is published by moderation,
// never by this request. The display name beside the review comes
// from the session's name claim — not from the body, which cannot be
// trusted to name its own author.
func (h *ReviewPortalHandler) Create(c *gin.Context) {
	email := emailFromContext(c)
	if email == "" {
		response.Unauthorized(c, "unauthorized")
		return
	}
	product, ok := h.reviewProduct(c)
	if !ok {
		return
	}
	var req struct {
		Rating int    `json:"rating"`
		Title  string `json:"title"`
		Body   string `json:"body"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "rating and body are required")
		return
	}
	if !model.ValidReviewRating(req.Rating) {
		response.BadRequest(c, "rating must be between 1 and 5")
		return
	}
	title, body, ok := validateReviewContent(c, req.Title, req.Body)
	if !ok {
		return
	}
	if body == "" {
		response.BadRequest(c, "body is required")
		return
	}

	name, _ := c.Get("name")
	r := &model.Review{
		ProductID:     product.ID,
		CustomerEmail: email,
		CustomerName:  str(name),
		Rating:        req.Rating,
		Title:         title,
		Body:          body,
	}
	if err := h.store.CreateReview(c.Request.Context(), r); err != nil {
		if store.IsReviewConflict(err) {
			response.Err(c, 409, "DUPLICATE", "you have already reviewed this product")
			return
		}
		response.Internal(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	response.Created(c, r)
}

// Update — PATCH /portal/products/:id/reviews
//
// Body: any of { title?, body }. The merge is the convention every
// partial update in this API follows: a field the request leaves out
// keeps its stored value, a field it names is re-validated whole. The
// body must stay non-empty — a review with no words is just a rating,
// which the row shape does not model.
func (h *ReviewPortalHandler) Update(c *gin.Context) {
	email := emailFromContext(c)
	if email == "" {
		response.Unauthorized(c, "unauthorized")
		return
	}
	product, ok := h.reviewProduct(c)
	if !ok {
		return
	}
	review, ok := h.ownReview(c, product.ID, email)
	if !ok {
		return
	}
	var req struct {
		Title *string `json:"title"`
		Body  *string `json:"body"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	if req.Title == nil && req.Body == nil {
		response.BadRequest(c, "title or body is required")
		return
	}
	title, body := review.Title, review.Body
	if req.Title != nil {
		title = *req.Title
	}
	if req.Body != nil {
		body = *req.Body
	}
	title, body, ok = validateReviewContent(c, title, body)
	if !ok {
		return
	}
	if body == "" {
		response.BadRequest(c, "body is required")
		return
	}
	if err := h.store.UpdateReviewContent(c.Request.Context(), review.ID, title, body); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.NotFound(c, "review not found")
			return
		}
		response.Internal(c, err)
		return
	}
	review.Title, review.Body = title, body
	c.Header("Cache-Control", "no-store")
	response.OK(c, review)
}

// Delete — DELETE /portal/products/:id/reviews
//
// The author takes their own review down. Same quiet 404 as a review
// that was never written — the endpoint cannot confirm another
// customer's row exists.
func (h *ReviewPortalHandler) Delete(c *gin.Context) {
	email := emailFromContext(c)
	if email == "" {
		response.Unauthorized(c, "unauthorized")
		return
	}
	product, ok := h.reviewProduct(c)
	if !ok {
		return
	}
	review, ok := h.ownReview(c, product.ID, email)
	if !ok {
		return
	}
	if err := h.store.DeleteReview(c.Request.Context(), review.ID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.NotFound(c, "review not found")
			return
		}
		response.Internal(c, err)
		return
	}
	response.NoContent(c)
}

// reviewPublicJSON renders one review for the anonymous storefront.
// Field selection is the whole of the privacy boundary: the author's
// email NEVER appears here — the display name is what a storefront
// shows beside a review, and the address behind it stays between the
// account and the admin. The moderation status is not rendered
// either: everything on this surface is approved by construction (the
// store filter), so the key would say "approved" on every row and
// mean nothing.
func reviewPublicJSON(r *model.Review) gin.H {
	return gin.H{
		"id":            r.ID,
		"customer_name": r.CustomerName,
		"rating":        r.Rating,
		"title":         r.Title,
		"body":          r.Body,
		"admin_reply":   r.AdminReply,
		"created_at":    r.CreatedAt,
	}
}

// reviewPublicArray renders a page of reviews for the public payload.
// Empty is an empty array, never null — the same contract every list
// field in these payloads keeps.
func reviewPublicArray(revs []*model.Review) []gin.H {
	out := make([]gin.H, 0, len(revs))
	for _, r := range revs {
		out = append(out, reviewPublicJSON(r))
	}
	return out
}
