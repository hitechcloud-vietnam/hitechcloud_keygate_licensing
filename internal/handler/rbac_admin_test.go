package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// fakeRBACStore stands in for store.Store so the refusal paths —
// unknown permission, duplicate name, system-role protection, role
// in use — run without a database. It records what the handler
// wrote and what it audited.
type fakeRBACStore struct {
	roles     map[string]*model.Role
	rolePerms map[string][]string
	users     map[string]*model.User
	assigned  map[string]map[string]bool // userID -> roleID -> held

	createErr  error
	updateErr  error
	deleteErr  error
	setPermErr error
	assignErr  error

	lastCreateRole *model.Role
	lastCreatePerm []string
	lastUpdateRole *model.Role
	lastUpdatePerm []string
	lastUpdateKeep bool // the PATCH passed perms == nil ("keep the set")
	lastSetPermID  string
	lastSetPerm    []string
	lastAssign     [2]string

	audits []*model.AuditLog
}

func newFakeRBACStore() *fakeRBACStore {
	return &fakeRBACStore{
		roles:     map[string]*model.Role{},
		rolePerms: map[string][]string{},
		users:     map[string]*model.User{},
		assigned:  map[string]map[string]bool{},
	}
}

func (f *fakeRBACStore) ListRoles(_ context.Context, _ string, _ store.Page) ([]*model.Role, int, error) {
	out := make([]*model.Role, 0, len(f.roles))
	for _, r := range f.roles {
		cp := *r
		out = append(out, &cp)
	}
	return out, len(out), nil
}

func (f *fakeRBACStore) FindRoleByID(_ context.Context, id string) (*model.Role, error) {
	if r, ok := f.roles[id]; ok {
		cp := *r // a copy, like a real row read
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeRBACStore) CreateRole(_ context.Context, r *model.Role, perms []string) error {
	if f.createErr != nil {
		return f.createErr
	}
	r.ID = "role-new"
	for _, other := range f.roles {
		if other.Name == r.Name {
			return store.ErrRoleNameTaken
		}
	}
	cp := *r
	f.roles[r.ID] = &cp
	f.rolePerms[r.ID] = append([]string(nil), perms...)
	f.lastCreateRole, f.lastCreatePerm = r, perms
	return nil
}

func (f *fakeRBACStore) UpdateRole(_ context.Context, r *model.Role, perms []string) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	existing, ok := f.roles[r.ID]
	if !ok {
		return sql.ErrNoRows
	}
	if existing.IsSystem && (r.Name != existing.Name || perms != nil) {
		return store.ErrSystemRoleProtected
	}
	cp := *r
	f.roles[r.ID] = &cp
	f.lastUpdateRole, f.lastUpdatePerm = r, perms
	f.lastUpdateKeep = perms == nil
	if perms != nil {
		f.rolePerms[r.ID] = append([]string(nil), perms...)
	}
	return nil
}

func (f *fakeRBACStore) DeleteRole(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	role, ok := f.roles[id]
	if !ok {
		return sql.ErrNoRows
	}
	if role.IsSystem {
		return store.ErrSystemRoleProtected
	}
	for _, held := range f.assigned {
		if held[id] {
			return store.ErrRoleInUse
		}
	}
	delete(f.roles, id)
	delete(f.rolePerms, id)
	return nil
}

func (f *fakeRBACStore) SetRolePermissions(_ context.Context, roleID string, perms []string) error {
	if f.setPermErr != nil {
		return f.setPermErr
	}
	role, ok := f.roles[roleID]
	if !ok {
		return sql.ErrNoRows
	}
	if role.IsSystem {
		return store.ErrSystemRoleProtected
	}
	f.rolePerms[roleID] = append([]string(nil), perms...)
	f.lastSetPermID, f.lastSetPerm = roleID, perms
	return nil
}

func (f *fakeRBACStore) PermissionsForRole(_ context.Context, roleID string) ([]string, error) {
	return append([]string(nil), f.rolePerms[roleID]...), nil
}

func (f *fakeRBACStore) RolesForUser(_ context.Context, userID string) ([]*model.Role, error) {
	// Held roles are stored user -> role.
	var out []*model.Role
	for rid := range f.assigned[userID] {
		if r, ok := f.roles[rid]; ok {
			cp := *r
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeRBACStore) FindUserByID(_ context.Context, id string) (*model.User, error) {
	if u, ok := f.users[id]; ok {
		cp := *u
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeRBACStore) AssignRole(_ context.Context, userID, roleID string) error {
	if f.assignErr != nil {
		return f.assignErr
	}
	if f.assigned[userID] == nil {
		f.assigned[userID] = map[string]bool{}
	}
	f.assigned[userID][roleID] = true // idempotent: set semantics
	f.lastAssign = [2]string{userID, roleID}
	return nil
}

func (f *fakeRBACStore) RevokeRole(_ context.Context, userID, roleID string) error {
	delete(f.assigned[userID], roleID) // idempotent: absent is a no-op
	return nil
}

func (f *fakeRBACStore) Audit(_ context.Context, log *model.AuditLog) {
	f.audits = append(f.audits, log)
}

func rbacAdminReq(t *testing.T, method, target, body string, params ...gin.Param) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", "admin-1")
	c.Params = params
	return w, c
}

func decodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, body)
	}
	return m
}

func seededRBACStore() *fakeRBACStore {
	f := newFakeRBACStore()
	f.roles["role-finance"] = &model.Role{ID: "role-finance", Name: "finance", IsSystem: true}
	f.rolePerms["role-finance"] = []string{model.PermOrdersRead}
	f.roles["role-custom"] = &model.Role{ID: "role-custom", Name: "support tier 2"}
	f.rolePerms["role-custom"] = []string{model.PermCustomersRead}
	f.users["u1"] = &model.User{ID: "u1", Email: "u1@example.com", Name: "U One"}
	return f
}

// GET /admin/rbac/permissions is the vocabulary endpoint behind
// every permission dropdown — it must list the closed §8 set.
func TestRBACPermissionsEndpoint(t *testing.T) {
	fake := newFakeRBACStore()
	h := NewRBACAdminHandler(nil)
	h.rbac = fake
	w, c := rbacAdminReq(t, "GET", "/admin/rbac/permissions", "")

	h.Permissions(c)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	body := decodeBody(t, w.Body.Bytes())
	data := body["data"].(map[string]any)
	perms, ok := data["permissions"].([]any)
	if !ok || len(perms) != 23 {
		t.Fatalf("permissions = %v (len %d), want 23 entries", data["permissions"], len(perms))
	}
	found := false
	for _, p := range perms {
		if p == model.PermSettingsManage {
			found = true
		}
	}
	if !found {
		t.Error("vocabulary is missing settings.manage")
	}
}

// Unknown permissions are refused with 400 before anything is
// written or audited — the vocabulary is closed.
func TestRBACCreateUnknownPermissionIs400(t *testing.T) {
	fake := newFakeRBACStore()
	h := NewRBACAdminHandler(nil)
	h.rbac = fake
	w, c := rbacAdminReq(t, "POST", "/admin/rbac/roles",
		`{"name":"ops","permissions":["products.read","products.explode"]}`)

	h.CreateRole(c)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body.String())
	}
	if fake.lastCreateRole != nil {
		t.Error("refused create still wrote a role")
	}
	if len(fake.audits) != 0 {
		t.Errorf("refused create left %d audit entries", len(fake.audits))
	}
}

// A name that folds to nothing, or grows past 64 chars, is a 400 the
// admin can fix.
func TestRBACCreateBadNameIs400(t *testing.T) {
	for _, body := range []string{
		`{"name":"   "}`,
		`{"name":"` + strings.Repeat("n", 65) + `"}`,
	} {
		fake := newFakeRBACStore()
		h := NewRBACAdminHandler(nil)
		h.rbac = fake
		w, c := rbacAdminReq(t, "POST", "/admin/rbac/roles", body)
		h.CreateRole(c)
		if w.Code != 400 {
			t.Errorf("body %q: status = %d, want 400", body, w.Code)
		}
	}
}

// The name is folded before it is stored: one role cannot exist
// under several spellings.
func TestRBACCreateFoldsNameAndAudits(t *testing.T) {
	fake := newFakeRBACStore()
	h := NewRBACAdminHandler(nil)
	h.rbac = fake
	w, c := rbacAdminReq(t, "POST", "/admin/rbac/roles",
		`{"name":"  Super  Analyst ","description":" d ","permissions":["products.read","products.read"]}`)

	h.CreateRole(c)

	if w.Code != 201 {
		t.Fatalf("status = %d, want 201 (%s)", w.Code, w.Body.String())
	}
	if fake.lastCreateRole == nil || fake.lastCreateRole.Name != "super analyst" {
		t.Fatalf("stored name = %q, want %q", fake.lastCreateRole.Name, "super analyst")
	}
	if len(fake.lastCreatePerm) != 1 || fake.lastCreatePerm[0] != model.PermProductsRead {
		t.Errorf("stored permissions = %v, want one products.read (dupe dropped)", fake.lastCreatePerm)
	}
	if len(fake.audits) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(fake.audits))
	}
	a := fake.audits[0]
	if a.Entity != "custom_role" || a.Action != "created" || a.ActorType != "admin" || a.ActorID != "admin-1" {
		t.Errorf("audit = %+v, want custom_role/created by admin-1", a)
	}
}

// A duplicate name is a state conflict the admin can act on — 409
// DUPLICATE, in the house spelling.
func TestRBACCreateDuplicateNameIs409(t *testing.T) {
	fake := seededRBACStore()
	h := NewRBACAdminHandler(nil)
	h.rbac = fake
	w, c := rbacAdminReq(t, "POST", "/admin/rbac/roles",
		`{"name":"Finance"}`) // folds to "finance", already seeded

	h.CreateRole(c)

	if w.Code != 409 {
		t.Fatalf("status = %d, want 409 (%s)", w.Code, w.Body.String())
	}
	body := decodeBody(t, w.Body.Bytes())
	errObj := body["error"].(map[string]any)
	if errObj["code"] != "DUPLICATE" {
		t.Errorf("error code = %v, want DUPLICATE", errObj["code"])
	}
}

// PATCH without a permissions field leaves the stored set alone —
// a rename cannot empty a bundle.
func TestRBACUpdateRenameKeepsPermissionSet(t *testing.T) {
	fake := seededRBACStore()
	h := NewRBACAdminHandler(nil)
	h.rbac = fake
	w, c := rbacAdminReq(t, "PATCH", "/admin/rbac/roles/role-custom",
		`{"name":"support tier 3"}`, gin.Param{Key: "id", Value: "role-custom"})

	h.UpdateRole(c)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if !fake.lastUpdateKeep {
		t.Error("PATCH without permissions replaced the permission set")
	}
	if fake.rolePerms["role-custom"][0] != model.PermCustomersRead {
		t.Errorf("stored set = %v, want the original kept", fake.rolePerms["role-custom"])
	}
}

// Seeded bundles refuse renames — their names are the documented
// vocabulary.
func TestRBACUpdateRenameSystemRoleIs409(t *testing.T) {
	fake := seededRBACStore()
	h := NewRBACAdminHandler(nil)
	h.rbac = fake
	w, c := rbacAdminReq(t, "PATCH", "/admin/rbac/roles/role-finance",
		`{"name":"money"}`, gin.Param{Key: "id", Value: "role-finance"})

	h.UpdateRole(c)

	if w.Code != 409 {
		t.Fatalf("status = %d, want 409 (%s)", w.Code, w.Body.String())
	}
	body := decodeBody(t, w.Body.Bytes())
	if body["error"].(map[string]any)["code"] != "SYSTEM_ROLE_PROTECTED" {
		t.Errorf("error code = %v, want SYSTEM_ROLE_PROTECTED", body["error"].(map[string]any)["code"])
	}
	if fake.roles["role-finance"].Name != "finance" {
		t.Error("refused rename still changed the row")
	}
}

// DELETE refusals, each with its own status: missing is 404, a
// seeded bundle is 409 SYSTEM_ROLE_PROTECTED, a role still held is
// 409 ROLE_IN_USE. None of them audit a deletion that did not
// happen.
func TestRBACDeleteRefusals(t *testing.T) {
	fake := seededRBACStore()
	fake.assigned["u1"] = map[string]bool{"role-custom": true}
	h := NewRBACAdminHandler(nil)
	h.rbac = fake

	w, c := rbacAdminReq(t, "DELETE", "/admin/rbac/roles/nope", "", gin.Param{Key: "id", Value: "nope"})
	h.DeleteRole(c)
	if w.Code != 404 {
		t.Errorf("missing: status = %d, want 404", w.Code)
	}

	w, c = rbacAdminReq(t, "DELETE", "/admin/rbac/roles/role-finance", "", gin.Param{Key: "id", Value: "role-finance"})
	h.DeleteRole(c)
	if w.Code != 409 {
		t.Errorf("system: status = %d, want 409", w.Code)
	} else if code := decodeBody(t, w.Body.Bytes())["error"].(map[string]any)["code"]; code != "SYSTEM_ROLE_PROTECTED" {
		t.Errorf("system: error code = %v, want SYSTEM_ROLE_PROTECTED", code)
	}

	w, c = rbacAdminReq(t, "DELETE", "/admin/rbac/roles/role-custom", "", gin.Param{Key: "id", Value: "role-custom"})
	h.DeleteRole(c)
	if w.Code != 409 {
		t.Errorf("in use: status = %d, want 409", w.Code)
	} else if code := decodeBody(t, w.Body.Bytes())["error"].(map[string]any)["code"]; code != "ROLE_IN_USE" {
		t.Errorf("in use: error code = %v, want ROLE_IN_USE", code)
	}

	if len(fake.audits) != 0 {
		t.Errorf("refused deletes left %d audit entries", len(fake.audits))
	}
}

// A successful delete audits the deletion and answers 204.
func TestRBACDeleteAudits(t *testing.T) {
	fake := seededRBACStore()
	h := NewRBACAdminHandler(nil)
	h.rbac = fake
	w, c := rbacAdminReq(t, "DELETE", "/admin/rbac/roles/role-custom", "", gin.Param{Key: "id", Value: "role-custom"})

	h.DeleteRole(c)
	// A 204 writes no body, so gin never flushes the status to the
	// recorder on its own — flush before reading w.Code.
	c.Writer.WriteHeaderNow()

	if w.Code != 204 {
		t.Fatalf("status = %d, want 204 (%s)", w.Code, w.Body.String())
	}
	if len(fake.audits) != 1 || fake.audits[0].Entity != "custom_role" || fake.audits[0].Action != "deleted" {
		t.Errorf("audits = %+v, want one custom_role/deleted", fake.audits)
	}
}

// PUT replaces the set wholesale and audits the new set; unknown
// permissions are 400 before anything is written.
func TestRBACSetPermissions(t *testing.T) {
	fake := seededRBACStore()
	h := NewRBACAdminHandler(nil)
	h.rbac = fake

	w, c := rbacAdminReq(t, "PUT", "/admin/rbac/roles/role-custom/permissions",
		`{"permissions":["orders.refund","invoices.read"]}`, gin.Param{Key: "id", Value: "role-custom"})
	h.SetPermissions(c)
	if w.Code != 400 {
		t.Fatalf("unknown permission: status = %d, want 400 (%s)", w.Code, w.Body.String())
	}
	if fake.lastSetPermID != "" {
		t.Error("refused PUT still wrote the set")
	}

	w, c = rbacAdminReq(t, "PUT", "/admin/rbac/roles/role-finance/permissions",
		`{"permissions":["orders.read"]}`, gin.Param{Key: "id", Value: "role-finance"})
	h.SetPermissions(c)
	if w.Code != 409 {
		t.Fatalf("system role: status = %d, want 409 (%s)", w.Code, w.Body.String())
	}

	w, c = rbacAdminReq(t, "PUT", "/admin/rbac/roles/role-custom/permissions",
		`{"permissions":["orders.refund"]}`, gin.Param{Key: "id", Value: "role-custom"})
	h.SetPermissions(c)
	if w.Code != 200 {
		t.Fatalf("replace: status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	if len(fake.lastSetPerm) != 1 || fake.lastSetPerm[0] != model.PermOrdersRefund {
		t.Errorf("stored set = %v, want exactly [orders.refund]", fake.lastSetPerm)
	}
	a := fake.audits[len(fake.audits)-1]
	if a.Action != "permissions_updated" || a.Entity != "custom_role" {
		t.Errorf("audit = %+v, want custom_role/permissions_updated", a)
	}
}

// Assign and revoke: unknown user or role is 404 before anything is
// written; the pair is audited as "user_custom_role"; a repeat
// assign is not an error (idempotent-ish).
func TestRBACAssignRevoke(t *testing.T) {
	fake := seededRBACStore()
	h := NewRBACAdminHandler(nil)
	h.rbac = fake

	w, c := rbacAdminReq(t, "POST", "/admin/rbac/users/ghost/roles",
		`{"role_id":"role-custom"}`, gin.Param{Key: "user_id", Value: "ghost"})
	h.AssignRole(c)
	if w.Code != 404 {
		t.Errorf("unknown user: status = %d, want 404", w.Code)
	}

	w, c = rbacAdminReq(t, "POST", "/admin/rbac/users/u1/roles",
		`{"role_id":"nope"}`, gin.Param{Key: "user_id", Value: "u1"})
	h.AssignRole(c)
	if w.Code != 404 {
		t.Errorf("unknown role: status = %d, want 404", w.Code)
	}
	if len(fake.audits) != 0 {
		t.Errorf("refused assigns left %d audit entries", len(fake.audits))
	}

	for i := 0; i < 2; i++ { // the second assign is a repeat, not a failure
		w, c = rbacAdminReq(t, "POST", "/admin/rbac/users/u1/roles",
			`{"role_id":"role-custom"}`, gin.Param{Key: "user_id", Value: "u1"})
		h.AssignRole(c)
		if w.Code != 200 {
			t.Fatalf("assign #%d: status = %d, want 200 (%s)", i+1, w.Code, w.Body.String())
		}
	}
	if !fake.assigned["u1"]["role-custom"] {
		t.Error("role was not assigned")
	}
	a := fake.audits[len(fake.audits)-1]
	if a.Entity != "user_custom_role" || a.Action != "assigned" || a.EntityID != "u1:role-custom" {
		t.Errorf("audit = %+v, want user_custom_role/assigned u1:role-custom", a)
	}

	// Revoking twice is a no-op the second time, never an error.
	for i := 0; i < 2; i++ {
		w, c = rbacAdminReq(t, "DELETE", "/admin/rbac/users/u1/roles",
			`{"role_id":"role-custom"}`, gin.Param{Key: "user_id", Value: "u1"})
		h.RevokeRole(c)
		c.Writer.WriteHeaderNow() // 204: no body to flush the status
		if w.Code != 204 {
			t.Fatalf("revoke #%d: status = %d, want 204 (%s)", i+1, w.Code, w.Body.String())
		}
	}
	if fake.assigned["u1"]["role-custom"] {
		t.Error("role is still assigned after revoke")
	}
	a = fake.audits[len(fake.audits)-1]
	if a.Entity != "user_custom_role" || a.Action != "revoked" {
		t.Errorf("audit = %+v, want user_custom_role/revoked", a)
	}
}

// GET /admin/rbac/users/:user_id/roles answers the held bundles.
func TestRBACListUserRoles(t *testing.T) {
	fake := seededRBACStore()
	fake.assigned["u1"] = map[string]bool{"role-custom": true}
	h := NewRBACAdminHandler(nil)
	h.rbac = fake
	w, c := rbacAdminReq(t, "GET", "/admin/rbac/users/u1/roles", "",
		gin.Param{Key: "user_id", Value: "u1"})

	h.ListUserRoles(c)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	body := decodeBody(t, w.Body.Bytes())
	data := body["data"].(map[string]any)
	roles, ok := data["roles"].([]any)
	if !ok || len(roles) != 1 {
		t.Fatalf("roles = %v, want exactly the one held role", data["roles"])
	}

	w, c = rbacAdminReq(t, "GET", "/admin/rbac/users/ghost/roles", "",
		gin.Param{Key: "user_id", Value: "ghost"})
	h.ListUserRoles(c)
	if w.Code != 404 {
		t.Errorf("unknown user: status = %d, want 404", w.Code)
	}
}
