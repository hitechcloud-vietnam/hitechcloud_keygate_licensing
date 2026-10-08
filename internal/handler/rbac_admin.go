package handler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// ─── Advanced RBAC (custom roles & permissions) ───
//
// Admin API for the plan §8 custom-role layer: named permission
// bundles and their assignment to users. Everything here sits ON TOP
// of users.role / is_admin and never replaces them (see
// model.ExpandBuiltinRole and middleware.RequirePermission).
//
//	GET    /admin/rbac/permissions              the §8 vocabulary (UI dropdowns)
//	GET    /admin/rbac/roles                    List (search, paging)
//	POST   /admin/rbac/roles                    Create (name, description?, permissions?)
//	GET    /admin/rbac/roles/:id                One role + its permissions
//	PATCH  /admin/rbac/roles/:id                Rename / redescribe / replace set
//	DELETE /admin/rbac/roles/:id                Delete (refuses system roles and in-use roles)
//	PUT    /admin/rbac/roles/:id/permissions    Replace the whole permission set
//	GET    /admin/rbac/users/:user_id/roles     Roles held by a user
//	POST   /admin/rbac/users/:user_id/roles     Assign {role_id}
//	DELETE /admin/rbac/users/:user_id/roles     Revoke {role_id}
//
// Every mutation writes an audit row (entities "custom_role" and
// "user_custom_role").
//
// rbacAdminStore is the slice of store.Store this handler needs. The
// real constructor takes *store.Store; tests substitute a fake so
// the refusal paths — unknown permission, duplicate name, system-role
// protection, role in use — run without a database.
type rbacAdminStore interface {
	ListRoles(ctx context.Context, search string, p store.Page) ([]*model.Role, int, error)
	FindRoleByID(ctx context.Context, id string) (*model.Role, error)
	CreateRole(ctx context.Context, r *model.Role, perms []string) error
	UpdateRole(ctx context.Context, r *model.Role, perms []string) error
	DeleteRole(ctx context.Context, id string) error
	SetRolePermissions(ctx context.Context, roleID string, perms []string) error
	PermissionsForRole(ctx context.Context, roleID string) ([]string, error)
	RolesForUser(ctx context.Context, userID string) ([]*model.Role, error)
	FindUserByID(ctx context.Context, id string) (*model.User, error)
	AssignRole(ctx context.Context, userID, roleID string) error
	RevokeRole(ctx context.Context, userID, roleID string) error
	Audit(ctx context.Context, log *model.AuditLog)
}

var _ rbacAdminStore = (*store.Store)(nil)

type RBACAdminHandler struct {
	rbac rbacAdminStore
}

func NewRBACAdminHandler(s *store.Store) *RBACAdminHandler {
	return &RBACAdminHandler{rbac: s}
}

// roleJSON is a role with the permission set it currently carries.
// The set is a separate column family, and an admin form that shows a
// role without its grants cannot edit them; one request answers both.
type roleJSON struct {
	*model.Role
	Permissions []string `json:"permissions"`
}

func newRoleJSON(r *model.Role, perms []string) roleJSON {
	return roleJSON{Role: r, Permissions: response.Array(perms)}
}

// normalizePermissionList trims entries and drops empties and
// duplicates, so one grant cannot arrive twice in a set the store
// would then have to clean again. Order is kept — an admin's list
// comes back the way they sent it.
func normalizePermissionList(perms []string) []string {
	seen := make(map[string]bool, len(perms))
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// prepareRoleName folds a role name into its stored canonical form
// (see model.NormalizeRoleName) and refuses what custom_roles cannot
// store: nothing left after folding, or more than 64 characters. The
// folded name is what is compared against the unique index, so
// "Super Admin" and "super  admin" are refused as one name, not two.
func prepareRoleName(raw string) (string, *apperr.AppError) {
	name := model.NormalizeRoleName(raw)
	if name == "" {
		return "", apperr.BadRequest("name is required")
	}
	if len(name) > 64 {
		return "", apperr.BadRequest("role name must be at most 64 characters")
	}
	return name, nil
}

// checkPermissions refuses any grant outside the closed §8
// vocabulary, naming the offender and where the vocabulary lives.
// The database CHECK would refuse the shape anyway; this turns a 500
// into an actionable 400 before anything is written.
func checkPermissions(perms []string) *apperr.AppError {
	for _, p := range perms {
		if !model.ValidPermission(p) {
			return apperr.BadRequest(fmt.Sprintf(
				"unknown permission %q; see GET /admin/rbac/permissions for the vocabulary", p))
		}
	}
	return nil
}

// writeRoleErr maps the RBAC sentinels to the one status each has,
// uniformly for every endpoint in this file.
func writeRoleErr(c *gin.Context, err error, id string) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeAppErr(c, apperr.NotFound("ROLE", id))
	case store.IsRoleNameConflict(err):
		response.Err(c, 409, "DUPLICATE", "role name already exists")
	case store.IsRoleProtected(err):
		response.Err(c, 409, "SYSTEM_ROLE_PROTECTED", "system roles cannot be renamed, re-permissioned or deleted")
	case store.IsRoleInUseConflict(err):
		response.Err(c, 409, "ROLE_IN_USE", "role is still assigned to users; revoke it first")
	case errors.Is(err, store.ErrUnknownPermission):
		response.BadRequest(c, err.Error())
	default:
		response.Internal(c, err)
	}
}

// Permissions — GET /admin/rbac/permissions
//
// The §8 vocabulary, unchanging and unauthenticated-as-to-content:
// it exists so a permission dropdown never has to hardcode the list.
func (h *RBACAdminHandler) Permissions(c *gin.Context) {
	response.OK(c, gin.H{"permissions": model.AllPermissions})
}

// ListRoles — GET /admin/rbac/roles
func (h *RBACAdminHandler) ListRoles(c *gin.Context) {
	page := listPage(c)
	roles, total, err := h.rbac.ListRoles(c, c.Query("search"), page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "roles", roles, total, page)
}

// GetRole — GET /admin/rbac/roles/:id
func (h *RBACAdminHandler) GetRole(c *gin.Context) {
	id := c.Param("id")
	role, err := h.rbac.FindRoleByID(c, id)
	if err != nil {
		writeRoleErr(c, err, id)
		return
	}
	perms, err := h.rbac.PermissionsForRole(c, id)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, newRoleJSON(role, perms))
}

// CreateRole — POST /admin/rbac/roles
//
// Body: { name, description?, permissions? }. The name is folded;
// every permission must be in the §8 vocabulary (400 otherwise); a
// name already taken — in any spelling — is 409 DUPLICATE. The role
// and its set are written together, so a role never exists half-made.
func (h *RBACAdminHandler) CreateRole(c *gin.Context) {
	var req struct {
		Name        string   `json:"name" binding:"required"`
		Description string   `json:"description"`
		Permissions []string `json:"permissions"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "name is required")
		return
	}
	name, verr := prepareRoleName(req.Name)
	if verr != nil {
		writeAppErr(c, verr)
		return
	}
	perms := normalizePermissionList(req.Permissions)
	if verr := checkPermissions(perms); verr != nil {
		writeAppErr(c, verr)
		return
	}

	role := &model.Role{
		Name:        name,
		Description: strings.TrimSpace(req.Description),
	}
	if err := h.rbac.CreateRole(c, role, perms); err != nil {
		writeRoleErr(c, err, role.ID)
		return
	}
	h.rbac.Audit(c, &model.AuditLog{
		Entity: "custom_role", EntityID: role.ID, Action: "created",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.Created(c, newRoleJSON(role, perms))
}

// UpdateRole — PATCH /admin/rbac/roles/:id
//
// Body: any of { name, description, permissions }. A field the
// request leaves out keeps its stored value — and a permissions field
// that is left out leaves the set alone entirely, so a rename cannot
// accidentally empty a bundle. A named field is re-validated like a
// create, so the two paths cannot drift apart on what a legal role
// is. Seeded bundles refuse renames and re-permissioning (409
// SYSTEM_ROLE_PROTECTED); their descriptions stay editable.
func (h *RBACAdminHandler) UpdateRole(c *gin.Context) {
	id := c.Param("id")
	role, err := h.rbac.FindRoleByID(c, id)
	if err != nil {
		writeRoleErr(c, err, id)
		return
	}

	var req struct {
		Name        *string   `json:"name"`
		Description *string   `json:"description"`
		Permissions *[]string `json:"permissions"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}

	// perms == nil means "keep the stored set"; non-nil (including an
	// empty array) means "replace it with exactly this".
	var perms []string
	if req.Permissions != nil {
		perms = normalizePermissionList(*req.Permissions)
		if verr := checkPermissions(perms); verr != nil {
			writeAppErr(c, verr)
			return
		}
	}
	name := role.Name
	if req.Name != nil {
		newName, verr := prepareRoleName(*req.Name)
		if verr != nil {
			writeAppErr(c, verr)
			return
		}
		name = newName
	}
	if req.Description != nil {
		role.Description = strings.TrimSpace(*req.Description)
	}
	role.Name = name

	if err := h.rbac.UpdateRole(c, role, perms); err != nil {
		writeRoleErr(c, err, id)
		return
	}
	h.rbac.Audit(c, &model.AuditLog{
		Entity: "custom_role", EntityID: id, Action: "updated",
		ActorType: "admin", ActorID: adminID(c),
	})
	// Read the set back rather than trusting the echo: a PATCH that
	// did not name permissions must answer with the set that is
	// actually stored.
	stored, err := h.rbac.PermissionsForRole(c, id)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, newRoleJSON(role, stored))
}

// DeleteRole — DELETE /admin/rbac/roles/:id
//
// A seeded bundle is refused (409 SYSTEM_ROLE_PROTECTED) and a role
// still held by any user is refused (409 ROLE_IN_USE) rather than
// silently stripping their permissions — revoke it first. A role
// that was never there is 404, not a claimed deletion.
func (h *RBACAdminHandler) DeleteRole(c *gin.Context) {
	id := c.Param("id")
	if err := h.rbac.DeleteRole(c, id); err != nil {
		writeRoleErr(c, err, id)
		return
	}
	h.rbac.Audit(c, &model.AuditLog{
		Entity: "custom_role", EntityID: id, Action: "deleted",
		ActorType: "admin", ActorID: adminID(c),
	})
	response.NoContent(c)
}

// SetPermissions — PUT /admin/rbac/roles/:id/permissions
//
// Body: { permissions: [...] }. Replaces the set wholesale — the one
// endpoint that says "this bundle IS exactly this" — so nothing is
// left half-applied and no stale grant survives a cleanup. Every
// entry must be in the §8 vocabulary (400 otherwise).
func (h *RBACAdminHandler) SetPermissions(c *gin.Context) {
	id := c.Param("id")
	role, err := h.rbac.FindRoleByID(c, id)
	if err != nil {
		writeRoleErr(c, err, id)
		return
	}

	var req struct {
		Permissions []string `json:"permissions" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "permissions is required")
		return
	}
	perms := normalizePermissionList(req.Permissions)
	if verr := checkPermissions(perms); verr != nil {
		writeAppErr(c, verr)
		return
	}
	if err := h.rbac.SetRolePermissions(c, id, perms); err != nil {
		writeRoleErr(c, err, id)
		return
	}
	h.rbac.Audit(c, &model.AuditLog{
		Entity: "custom_role", EntityID: id, Action: "permissions_updated",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"permissions": perms},
	})
	response.OK(c, newRoleJSON(role, perms))
}

// ListUserRoles — GET /admin/rbac/users/:user_id/roles
//
// The roles a user holds. The user's EFFECTIVE permissions are the
// union over these (store.PermissionsForUser); this answers "which
// bundles", which is what an admin edits.
func (h *RBACAdminHandler) ListUserRoles(c *gin.Context) {
	userID := c.Param("user_id")
	if _, err := h.rbac.FindUserByID(c, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("USER", userID))
			return
		}
		response.Internal(c, err)
		return
	}
	roles, err := h.rbac.RolesForUser(c, userID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, gin.H{"roles": response.Array(roles), "total": len(roles)})
}

// AssignRole — POST /admin/rbac/users/:user_id/roles
//
// Body: { role_id }. Idempotent: assigning a role the user already
// holds succeeds and stays one assignment — a retry after a lost
// response must not fail. Unknown user or role is 404 before
// anything is written.
func (h *RBACAdminHandler) AssignRole(c *gin.Context) {
	userID := c.Param("user_id")
	var req struct {
		RoleID string `json:"role_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "role_id is required")
		return
	}
	if code := h.resolveAssignment(c, userID, req.RoleID); code != 0 {
		return
	}
	if err := h.rbac.AssignRole(c, userID, req.RoleID); err != nil {
		response.Internal(c, err)
		return
	}
	h.rbac.Audit(c, &model.AuditLog{
		Entity: "user_custom_role", EntityID: userID + ":" + req.RoleID, Action: "assigned",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"user_id": userID, "role_id": req.RoleID},
	})
	response.OK(c, gin.H{"user_id": userID, "role_id": req.RoleID})
}

// RevokeRole — DELETE /admin/rbac/users/:user_id/roles
//
// Body: { role_id }. Idempotent: revoking a role the user does not
// hold succeeds as a no-op — a retry after a lost response must not
// fail. The permissions the user keeps are the union over their
// remaining roles.
func (h *RBACAdminHandler) RevokeRole(c *gin.Context) {
	userID := c.Param("user_id")
	var req struct {
		RoleID string `json:"role_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "role_id is required")
		return
	}
	if code := h.resolveAssignment(c, userID, req.RoleID); code != 0 {
		return
	}
	if err := h.rbac.RevokeRole(c, userID, req.RoleID); err != nil {
		response.Internal(c, err)
		return
	}
	h.rbac.Audit(c, &model.AuditLog{
		Entity: "user_custom_role", EntityID: userID + ":" + req.RoleID, Action: "revoked",
		ActorType: "admin", ActorID: adminID(c),
		Changes: map[string]any{"user_id": userID, "role_id": req.RoleID},
	})
	response.NoContent(c)
}

// resolveAssignment writes the 404s an assignment attempt can earn
// (unknown user, unknown role) and reports whether it answered; the
// callers continue only on 0. Split out so assign and revoke refuse
// identically — a revoke of a nonexistent role would otherwise
// succeed as a no-op and hide the typo.
func (h *RBACAdminHandler) resolveAssignment(c *gin.Context, userID, roleID string) int {
	if _, err := h.rbac.FindUserByID(c, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("USER", userID))
			return 404
		}
		response.Internal(c, err)
		return 500
	}
	if _, err := h.rbac.FindRoleByID(c, roleID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeAppErr(c, apperr.NotFound("ROLE", roleID))
			return 404
		}
		response.Internal(c, err)
		return 500
	}
	return 0
}
