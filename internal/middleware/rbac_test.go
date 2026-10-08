package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// fakePermissionStore stands in for store.Store so the gate's
// decision table runs without a database. It records whether it was
// consulted at all — several paths must pass WITHOUT asking it.
type fakePermissionStore struct {
	set      map[string]bool
	err      error
	calls    int
	lastUser string
}

func (f *fakePermissionStore) PermissionsForUser(_ context.Context, userID string) (map[string]bool, error) {
	f.calls++
	f.lastUser = userID
	if f.err != nil {
		return nil, f.err
	}
	return f.set, nil
}

// runGate drives one middleware invocation on a real router and
// reports whether the request reached the handler behind the gate.
// The seeding middleware stands in for SessionAuth: it writes the
// same context keys, so the gate sees exactly what production sees.
func runGate(t *testing.T, perm string, perms PermissionStore, userID, authType string, isAdmin bool) (*httptest.ResponseRecorder, bool) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	reached := false
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if userID != "" {
			c.Set("user_id", userID)
			c.Set("email", userID+"@example.com")
		}
		if authType != "" {
			c.Set("auth_type", authType)
		}
		c.Set("is_admin", isAdmin)
	})
	router.Use(RequirePermission(perm, perms))
	router.GET("/admin/rbac/roles", func(c *gin.Context) { reached = true })
	router.ServeHTTP(w, httptest.NewRequest("GET", "/admin/rbac/roles", nil))
	return w, reached
}

// A session user who holds the permission passes; the store is asked
// for THAT user's set.
func TestRequirePermissionPassesWithPermission(t *testing.T) {
	fake := &fakePermissionStore{set: map[string]bool{model.PermProductsRead: true}}
	w, reached := runGate(t, model.PermProductsRead, fake, "u1", "session", false)
	if !reached {
		t.Fatalf("handler not reached; status %d body %s", w.Code, w.Body.String())
	}
	if fake.calls != 1 || fake.lastUser != "u1" {
		t.Errorf("store calls = %d (user %q), want 1 call for u1", fake.calls, fake.lastUser)
	}
}

// The reserved wildcard "*.*" in an effective set stands for every
// permission.
func TestRequirePermissionWildcardPasses(t *testing.T) {
	fake := &fakePermissionStore{set: map[string]bool{model.WildcardPermission: true}}
	w, reached := runGate(t, model.PermSettingsManage, fake, "u1", "session", false)
	if !reached {
		t.Fatalf("wildcard did not pass; status %d body %s", w.Code, w.Body.String())
	}
}

// *** The backward-compat wildcard: an is_admin session passes
// WITHOUT a permission set and WITHOUT consulting the store. This is
// what keeps every pre-RBAC admin working — if this test ever needs
// changing, that is a breaking migration, not a refactor.
func TestRequirePermissionIsAdminWildcardPassthrough(t *testing.T) {
	fake := &fakePermissionStore{set: map[string]bool{}} // holds nothing
	w, reached := runGate(t, model.PermSettingsManage, fake, "admin-1", "session", true)
	if !reached {
		t.Fatalf("is_admin was refused; status %d body %s", w.Code, w.Body.String())
	}
	if fake.calls != 0 {
		t.Errorf("store consulted %d times for an is_admin session, want 0", fake.calls)
	}
}

// A missing permission is 403 FORBIDDEN with the standard envelope —
// one code, the house one from pkg/response.
func TestRequirePermissionMissingIs403BodyShape(t *testing.T) {
	fake := &fakePermissionStore{set: map[string]bool{model.PermProductsRead: true}}
	w, reached := runGate(t, model.PermOrdersRefund, fake, "u1", "session", false)
	if reached {
		t.Fatal("handler reached despite missing permission")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	var body struct {
		Success bool `json:"success"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, w.Body.String())
	}
	if body.Success {
		t.Error("success = true, want false")
	}
	if body.Error == nil || body.Error.Code != "FORBIDDEN" {
		t.Errorf("error = %+v, want code FORBIDDEN", body.Error)
	}
	if body.Error != nil && body.Error.Message == "" {
		t.Error("error message is empty")
	}
}

// A request with no session identity at all is 401, matching every
// other session-only gate.
func TestRequirePermissionNoSessionIs401(t *testing.T) {
	fake := &fakePermissionStore{set: map[string]bool{}}
	w, reached := runGate(t, model.PermProductsRead, fake, "", "session", false)
	if reached {
		t.Fatal("handler reached without a session")
	}
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if fake.calls != 0 {
		t.Errorf("store consulted %d times with no session, want 0", fake.calls)
	}
}

// PINNED: API-key requests are not this gate's business. It no-ops
// (passes through) and NEVER resolves permissions for them —
// RequireScope / RequireCustomerScope own the key path and the two
// models must not be mixed.
func TestRequirePermissionAPIKeyNoOp(t *testing.T) {
	fake := &fakePermissionStore{set: map[string]bool{}} // holds nothing
	w, reached := runGate(t, model.PermSettingsManage, fake, "apikey:k1", "api_key", false)
	if !reached {
		t.Fatalf("API-key request was refused by the session gate; status %d body %s", w.Code, w.Body.String())
	}
	if fake.calls != 0 {
		t.Errorf("store consulted %d times for an API-key request, want 0", fake.calls)
	}
}

// A store failure is a 500 the client can do nothing about — never a
// silent pass and never a 403 that would blame the caller.
func TestRequirePermissionStoreErrorIs500(t *testing.T) {
	fake := &fakePermissionStore{err: errors.New("connection reset")}
	w, reached := runGate(t, model.PermProductsRead, fake, "u1", "session", false)
	if reached {
		t.Fatal("handler reached despite store error")
	}
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
}

// A nil store is a wiring mistake and fails closed with 403 — it
// must not panic and must not pass.
func TestRequirePermissionNilStoreFailsClosed(t *testing.T) {
	w, reached := runGate(t, model.PermProductsRead, nil, "u1", "session", false)
	if reached {
		t.Fatal("handler reached with nil PermissionStore")
	}
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
}
