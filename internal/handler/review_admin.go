package handler

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// ─── Product reviews, moderation ───
//
// Admin moderation for the marketplace review queue:
//
//	GET    /admin/reviews                List (product_id, status, paging)
//	GET    /admin/reviews/:id            Get one
//	POST   /admin/reviews/:id/approve    Publish
//	POST   /admin/reviews/:id/reject    Withhold (or unpublish)
//	PUT    /admin/reviews/:id/reply      Answer under the review
//	DELETE /admin/reviews/:id            Take down and remove
//
// The state machine is model.CanTransitionReview and nothing else
// moves: pending → approved | rejected, approved → rejected
// (unpublish), rejected → approved (republish). An illegal move —
// approving an approved review, rejecting a rejected one — answers 409
// REVIEW_TRANSITION_INVALID naming both statuses, so a double-click
// surfaces instead of silently "succeeding" and blurring who published
// what. Every moderation action is audited (Entity "review").
//
// The handler deliberately carries no business rule of its own: it
// validates the request, asks the model whether the move is legal,
// writes through the store and audits. Ratings and bodies are the
// author's; status, reply and deletion are the admin's.
type ReviewAdminHandler struct {
	rev reviewAdminStore
}

// reviewAdminStore is the slice of store.Store this handler needs.
// The real constructor takes *store.Store; tests substitute a fake so
// the transition refusals run without a database.
type reviewAdminStore interface {
	FindReviewByID(ctx context.Context, id string) (*model.Review, error)
	ListReviewsByProduct(ctx context.Context, productID, status string, p store.Page) ([]*model.Review, int, error)
	ListAllReviews(ctx context.Context, status string, p store.Page) ([]*model.Review, int, error)
	UpdateReviewStatus(ctx context.Context, id, status string) error
	UpdateReviewReply(ctx context.Context, id, reply string) error
	DeleteReview(ctx context.Context, id string) error
	Audit(ctx context.Context, log *model.AuditLog)
}

var _ reviewAdminStore = (*store.Store)(nil)

// NewReviewAdminHandler wires the handler to the store.
func NewReviewAdminHandler(s *store.Store) *ReviewAdminHandler {
	return &ReviewAdminHandler{rev: s}
}

// Get — GET /admin/reviews/:id
func (h *ReviewAdminHandler) Get(c *gin.Context) {
	r, ok := h.mustFind(c, c.Param("id"))
	if !ok {
		return
	}
	response.OK(c, r)
}

// List — GET /admin/reviews
//
// Query: product_id (narrow to one product), status (one of the
// closed vocabulary), limit/offset. A status filter outside the
// vocabulary is refused rather than silently served an empty page.
func (h *ReviewAdminHandler) List(c *gin.Context) {
	status := c.Query("status")
	if status != "" && !model.ValidReviewStatus(status) {
		response.BadRequest(c, "status must be pending, approved, or rejected")
		return
	}
	page := listPage(c)
	productID := strings.TrimSpace(c.Query("product_id"))

	var (
		reviews []*model.Review
		total   int
		err     error
	)
	if productID != "" {
		reviews, total, err = h.rev.ListReviewsByProduct(c, productID, status, page)
	} else {
		reviews, total, err = h.rev.ListAllReviews(c, status, page)
	}
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "reviews", reviews, total, page)
}

// Approve — POST /admin/reviews/:id/approve
//
// Publishes the review onto the storefront (pending → approved, or
// rejected → approved to republish). Approving an already-approved
// review is a 409, not a no-op.
func (h *ReviewAdminHandler) Approve(c *gin.Context) {
	h.moderate(c, model.ReviewStatusApproved)
}

// Reject — POST /admin/reviews/:id/reject
//
// Withholds the review (pending → rejected) or takes a published one
// down again (approved → rejected, unpublish). The row is kept — the
// audit trail needs it — and the aggregate stops counting it the
// moment its status moves (the summaries read approved rows only).
func (h *ReviewAdminHandler) Reject(c *gin.Context) {
	h.moderate(c, model.ReviewStatusRejected)
}

// moderate runs one admin status move end to end: find, ask the model
// whether the move is legal, write it, audit it. The audit action is
// the target status itself — "approved" / "rejected" read as what
// happened, in the same past tense as "created" and "deleted".
func (h *ReviewAdminHandler) moderate(c *gin.Context, target string) {
	id := c.Param("id")
	r, ok := h.mustFind(c, id)
	if !ok {
		return
	}
	if !model.CanTransitionReview(r.Status, target) {
		response.Err(c, 409, "REVIEW_TRANSITION_INVALID",
			"review status "+r.Status+" cannot move to "+target)
		return
	}
	if err := h.rev.UpdateReviewStatus(c, id, target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("REVIEW", id))
			return
		}
		response.Internal(c, err)
		return
	}
	h.rev.Audit(c, &model.AuditLog{
		Entity: "review", EntityID: id, Action: target,
		ActorType: "admin", ActorID: adminID(c),
	})
	r.Status = target
	response.OK(c, r)
}

// Reply — PUT /admin/reviews/:id/reply
//
// Body: { admin_reply }. Writes — or, with an empty value, clears —
// the vendor's public answer under the review. The reply is bounded
// like the review body and, unlike the review itself, can be written
// at any moderation status: answering a pending review before it is
// published is legitimate.
func (h *ReviewAdminHandler) Reply(c *gin.Context) {
	id := c.Param("id")
	r, ok := h.mustFind(c, id)
	if !ok {
		return
	}
	var req struct {
		AdminReply *string `json:"admin_reply"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.AdminReply == nil {
		response.BadRequest(c, "admin_reply is required")
		return
	}
	reply := strings.TrimSpace(*req.AdminReply)
	if len(reply) > model.MaxReviewBodyLen {
		response.BadRequest(c, "admin_reply must be at most 4000 characters")
		return
	}
	if err := h.rev.UpdateReviewReply(c, id, reply); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("REVIEW", id))
			return
		}
		response.Internal(c, err)
		return
	}
	h.rev.Audit(c, &model.AuditLog{
		Entity: "review", EntityID: id, Action: "replied",
		ActorType: "admin", ActorID: adminID(c),
	})
	r.AdminReply = reply
	response.OK(c, r)
}

// Delete — DELETE /admin/reviews/:id
//
// Removes the review entirely — harsher than reject, which keeps the
// row for the audit trail. Use it for spam and abuse, where the
// content itself should not persist.
func (h *ReviewAdminHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	if err := h.rev.DeleteReview(c, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("REVIEW", id))
			return
		}
		response.Internal(c, err)
		return
	}
	h.rev.Audit(c, &model.AuditLog{
		Entity: "review", EntityID: id, Action: "deleted",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.NoContent(c)
}

// mustFind loads the review a moderation action addresses, writing
// the 404 REVIEW_NOT_FOUND when it is not there. A lookup that failed
// for any other reason is an internal error — the two are not blurred
// together on the admin surface, where the operator needs to tell
// "gone" from "broken".
func (h *ReviewAdminHandler) mustFind(c *gin.Context, id string) (*model.Review, bool) {
	r, err := h.rev.FindReviewByID(c, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("REVIEW", id))
			return nil, false
		}
		response.Internal(c, err)
		return nil, false
	}
	return r, true
}
