package middleware

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── RBAC permission gate ───
//
// RequirePermission is the granular half of authorization, layered
// on top of — never instead of — the existing SessionAuth / AdminOnly
// machinery. It reads ONLY the session context that SessionAuth
// wrote ("user_id", "is_admin"); API-key requests are out of scope
// here BY DESIGN — their route groups keep using RequireScope /
// RequireCustomerScope, and the two must not be mixed (see below).

// PermissionStore is the slice of store.Store this gate needs: the
// union of a user's permissions over every custom role they hold.
// The real constructor takes *store.Store; tests substitute a fake.
type PermissionStore interface {
	PermissionsForUser(ctx context.Context, userID string) (map[string]bool, error)
}

// RequirePermission passes the request on to the route only if the
// session user holds the granular permission `perm` (a member of the
// closed model.AllPermissions vocabulary). It refuses with 403
// FORBIDDEN — the house code from pkg/response — otherwise.
//
// Two paths pass WITHOUT consulting the permission set:
//
//  1. is_admin == true. *** BACKWARD-COMPATIBILITY WILDCARD — DO NOT
//     NARROW WITHOUT A MIGRATION PLAN. *** The platform has always
//     had admins and owners (users.role 'owner'|'admin' + is_admin)
//     with full run of the admin API. If this gate denied an
//     is_admin session that holds no custom role, every existing
//     admin would break the moment a route gained a permission
//     check. So an is_admin session is treated as holding every
//     permission, forever, until the built-in bundles are assigned
//     as a deliberate migration and this wildcard is revisited.
//     Non-admin users (users.role 'user') get their permissions
//     exclusively from their custom roles.
//
//  2. API-key requests (auth_type == "api_key"). This gate is for
//     interactive sessions; a kg_live_ key is scoped per route by
//     RequireScope / RequireCustomerScope instead. PINNED: do NOT
//     extend this middleware to resolve permissions for API keys —
//     mixing session RBAC with key scopes in one gate is how the two
//     models silently leak into each other. The no-op is a pass-
//     through, not a grant: whatever the route's own API-key
//     middleware decided still stands, and routes that accept keys
//     must carry that middleware.
//
// The effective set also honors the reserved wildcard "*.*"
// (model.WildcardPermission) if a backend ever emits it; the current
// vocabulary never does.
//
// A request without a session identity (no "user_id" — SessionAuth
// not run) is 401, matching every other session-only gate.
// Everything else fails closed: an unknown permission, a store error,
// a nil store — none of them pass.
func RequirePermission(perm string, perms PermissionStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		v, exists := c.Get("user_id")
		if !exists {
			abortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
			return
		}

		// API-key requests: not ours to judge — RequireScope /
		// RequireCustomerScope owns them (see the doc comment).
		if t, _ := c.Get("auth_type"); t == "api_key" {
			c.Next()
			return
		}

		// *** BACKWARD-COMPAT WILDCARD — see doc comment. ***
		// is_admin means the pre-RBAC full-access world; narrowing
		// this would break every existing admin overnight.
		if admin, _ := c.Get("is_admin"); admin == true {
			c.Next()
			return
		}

		uid, _ := v.(string)
		// A nil store is a wiring mistake; it fails closed like a
		// missing permission rather than panicking on the call.
		if perms == nil {
			abortWithError(c, http.StatusForbidden, "FORBIDDEN", "missing permission "+perm)
			return
		}
		set, err := perms.PermissionsForUser(c.Request.Context(), uid)
		if err != nil {
			abortInternal(c, err)
			return
		}
		if set[perm] || set[model.WildcardPermission] {
			c.Next()
			return
		}
		abortWithError(c, http.StatusForbidden, "FORBIDDEN", "missing permission "+perm)
	}
}
