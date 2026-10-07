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

// ─── Categories ───
//
// Admin CRUD for the marketplace catalog's categories. The rows are
// discovery facets only (see model.Category): they decide how the
// public catalog is browsed, never what a licence may do.
//
//	GET    /admin/categories            List (search, paging)
//	POST   /admin/categories            Create
//	GET    /admin/categories/:id        Get one
//	PATCH  /admin/categories/:id        Update (partial)
//	DELETE /admin/categories/:id        Delete (detaches its products)
//
// categoryAdminStore is the slice of store.Store this handler needs.
// The real constructor takes *store.Store; tests substitute a fake so
// the refusal paths — duplicate slug, missing row — run without a
// database.
type categoryAdminStore interface {
	ListCategories(ctx context.Context, search string, p store.Page) ([]*model.Category, int, error)
	FindCategoryByID(ctx context.Context, id string) (*model.Category, error)
	CreateCategory(ctx context.Context, cat *model.Category) error
	UpdateCategory(ctx context.Context, cat *model.Category) error
	DeleteCategory(ctx context.Context, id string) error
	Audit(ctx context.Context, log *model.AuditLog)
}

var _ categoryAdminStore = (*store.Store)(nil)

type CategoryAdminHandler struct {
	cat categoryAdminStore
}

func NewCategoryAdminHandler(s *store.Store) *CategoryAdminHandler {
	return &CategoryAdminHandler{cat: s}
}

// prepareCategory folds a row into its stored form — trimmed name,
// canonical slug (derived from the name when the request did not send
// one) — and refuses what the catalog cannot store: an empty name, a
// slug that will not validate even after folding, a negative position.
// Create and update both run it, so a value refused at creation cannot
// be put on the same category a moment later. It returns apperr so
// writeAppErr writes it.
func prepareCategory(cat *model.Category) *apperr.AppError {
	cat.Name = strings.TrimSpace(cat.Name)
	if err := apperr.ValidateName("name", cat.Name); err != nil {
		return err
	}

	// A slug the caller sent is folded, not merely checked: the
	// handle is stored in exactly one spelling whatever the admin
	// typed (see model.NormalizeCategorySlug). When none was sent the
	// name derives one. Folded-empty means the input had no usable
	// ASCII character at all — say that, rather than "slug is
	// required" for a slug that was provided.
	if raw := strings.TrimSpace(cat.Slug); raw != "" {
		cat.Slug = model.NormalizeCategorySlug(raw)
		if cat.Slug == "" {
			return apperr.BadRequest("slug must contain at least one letter or number")
		}
	} else {
		cat.Slug = model.NormalizeCategorySlug(cat.Name)
	}
	if err := apperr.ValidateSlug(cat.Slug); err != nil {
		return err
	}

	cat.Description = strings.TrimSpace(cat.Description)
	if cat.Position < 0 {
		return apperr.BadRequest("position must not be negative")
	}
	return nil
}

// List — GET /admin/categories
func (h *CategoryAdminHandler) List(c *gin.Context) {
	page := listPage(c)
	cats, total, err := h.cat.ListCategories(c, c.Query("search"), page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "categories", cats, total, page)
}

// Get — GET /admin/categories/:id
func (h *CategoryAdminHandler) Get(c *gin.Context) {
	id := c.Param("id")
	cat, err := h.cat.FindCategoryByID(c, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("CATEGORY", id))
			return
		}
		response.Internal(c, err)
		return
	}
	response.OK(c, cat)
}

// Create — POST /admin/categories
//
// Body: { name, slug?, description?, position? }. A missing slug is
// derived from the name.
func (h *CategoryAdminHandler) Create(c *gin.Context) {
	var req struct {
		Name        string `json:"name" binding:"required"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		Position    int    `json:"position"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "name is required")
		return
	}

	cat := &model.Category{
		Name:        req.Name,
		Slug:        req.Slug,
		Description: req.Description,
		Position:    req.Position,
	}
	if verr := prepareCategory(cat); verr != nil {
		writeAppErr(c, verr)
		return
	}
	if err := h.cat.CreateCategory(c, cat); err != nil {
		// The slug is the category's handle and the column is unique;
		// an insert refused here is almost always that handle taken,
		// which the admin can act on. Anything else is not their doing.
		if store.IsCategorySlugConflict(err) {
			response.Err(c, 409, "DUPLICATE", "category slug already exists")
			return
		}
		response.Internal(c, err)
		return
	}
	h.cat.Audit(c, &model.AuditLog{
		Entity: "category", EntityID: cat.ID, Action: "created",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.Created(c, cat)
}

// Update — PATCH /admin/categories/:id
//
// Body: any of { name, slug, description, position }. A field the
// request leaves out keeps its stored value; a field it names is
// re-validated like a create, so the two paths cannot drift apart on
// what a legal category is.
func (h *CategoryAdminHandler) Update(c *gin.Context) {
	id := c.Param("id")
	cat, err := h.cat.FindCategoryByID(c, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("CATEGORY", id))
			return
		}
		response.Internal(c, err)
		return
	}

	var req struct {
		Name        *string `json:"name"`
		Slug        *string `json:"slug"`
		Description *string `json:"description"`
		Position    *int    `json:"position"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	if req.Name != nil {
		cat.Name = *req.Name
	}
	if req.Slug != nil {
		cat.Slug = *req.Slug
	}
	if req.Description != nil {
		cat.Description = *req.Description
	}
	if req.Position != nil {
		cat.Position = *req.Position
	}
	if verr := prepareCategory(cat); verr != nil {
		writeAppErr(c, verr)
		return
	}
	if err := h.cat.UpdateCategory(c, cat); err != nil {
		if store.IsCategorySlugConflict(err) {
			response.Err(c, 409, "DUPLICATE", "category slug already exists")
			return
		}
		response.Internal(c, err)
		return
	}
	h.cat.Audit(c, &model.AuditLog{
		Entity: "category", EntityID: cat.ID, Action: "updated",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.OK(c, cat)
}

// Delete — DELETE /admin/categories/:id
//
// The category's product links are dropped with it (ON DELETE
// CASCADE): deleting a category detaches its products from that facet
// of the catalog and does nothing else — no product, plan, licence or
// release is touched. This is the documented "cascade the join rows"
// choice over refusing while products are attached; see
// model.ProductCategory.
func (h *CategoryAdminHandler) Delete(c *gin.Context) {
	id := c.Param("id")
	if err := h.cat.DeleteCategory(c, id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("CATEGORY", id))
			return
		}
		response.Internal(c, err)
		return
	}
	h.cat.Audit(c, &model.AuditLog{
		Entity: "category", EntityID: id, Action: "deleted",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.NoContent(c)
}
