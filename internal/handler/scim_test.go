package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/sso"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ─── in-memory fake store ───

type fakeScimStore struct {
	users        map[string]*model.User         // id -> user
	usersByEmail map[string]*model.User         // email -> user
	idents       map[string]*store.SCIMIdentity // scimID -> identity
	identByUser  map[string]*store.SCIMIdentity // userID -> identity
	seq          int

	// call recorders
	createdIdents int
	emailUpdates  map[string]string // userID -> new email
	nameUpdates   map[string]string // userID -> new name
	activeSets    map[string]bool   // scimID -> active
}

func newFakeScimStore() *fakeScimStore {
	return &fakeScimStore{
		users:        map[string]*model.User{},
		usersByEmail: map[string]*model.User{},
		idents:       map[string]*store.SCIMIdentity{},
		identByUser:  map[string]*store.SCIMIdentity{},
		emailUpdates: map[string]string{},
		nameUpdates:  map[string]string{},
		activeSets:   map[string]bool{},
	}
}

func (f *fakeScimStore) row(ident *store.SCIMIdentity) *store.SCIMUserRow {
	u := f.users[ident.UserID]
	r := &store.SCIMUserRow{
		SCIMID:     ident.ID,
		ExternalID: ident.ExternalID,
		Active:     ident.Active,
		CreatedAt:  ident.CreatedAt,
		UpdatedAt:  ident.UpdatedAt,
		UserID:     ident.UserID,
	}
	if u != nil {
		r.Email = u.Email
		r.Name = u.Name
	}
	return r
}

func (f *fakeScimStore) FindSCIMUserBySCIMID(_ context.Context, scimID string) (*store.SCIMUserRow, error) {
	if ident, ok := f.idents[scimID]; ok {
		return f.row(ident), nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeScimStore) FindSCIMUserByUserName(_ context.Context, email string) (*store.SCIMUserRow, error) {
	u, ok := f.usersByEmail[email]
	if !ok {
		return nil, sql.ErrNoRows
	}
	ident, ok := f.identByUser[u.ID]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return f.row(ident), nil
}

func (f *fakeScimStore) ListSCIMUsers(_ context.Context, p store.Page) ([]*store.SCIMUserRow, int, error) {
	var rows []*store.SCIMUserRow
	for _, ident := range f.idents {
		rows = append(rows, f.row(ident))
	}
	return rows, len(rows), nil
}

func (f *fakeScimStore) FindSCIMIdentityByID(_ context.Context, id string) (*store.SCIMIdentity, error) {
	if ident, ok := f.idents[id]; ok {
		cpy := *ident
		return &cpy, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeScimStore) CreateSCIMIdentity(_ context.Context, row *store.SCIMIdentity) error {
	if _, exists := f.identByUser[row.UserID]; exists {
		return store.ErrSCIMIdentityExists
	}
	f.seq++
	row.ID = fmt.Sprintf("scim-%d", f.seq)
	row.CreatedAt = time.Now()
	row.UpdatedAt = row.CreatedAt
	cpy := *row
	f.idents[row.ID] = &cpy
	f.identByUser[row.UserID] = &cpy
	f.createdIdents++
	return nil
}

func (f *fakeScimStore) UpdateSCIMIdentity(_ context.Context, row *store.SCIMIdentity) error {
	cpy := *row
	f.idents[row.ID] = &cpy
	f.identByUser[row.UserID] = &cpy
	return nil
}

func (f *fakeScimStore) SetSCIMIdentityActive(_ context.Context, id string, active bool) error {
	if ident, ok := f.idents[id]; ok {
		ident.Active = active
		f.activeSets[id] = active
		return nil
	}
	return sql.ErrNoRows
}

func (f *fakeScimStore) FindUserByEmail(_ context.Context, email string) (*model.User, error) {
	if u, ok := f.usersByEmail[email]; ok {
		return u, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeScimStore) UpsertUser(_ context.Context, u *model.User) error {
	f.seq++
	u.ID = fmt.Sprintf("user-%d", f.seq)
	cpy := *u
	f.users[u.ID] = &cpy
	f.usersByEmail[u.Email] = &cpy
	return nil
}

func (f *fakeScimStore) UpdateUserProfile(_ context.Context, userID, name string) error {
	if u, ok := f.users[userID]; ok {
		u.Name = name
		f.nameUpdates[userID] = name
	}
	return nil
}

func (f *fakeScimStore) UpdateSCIMUserEmail(_ context.Context, userID, email string) error {
	u, ok := f.users[userID]
	if !ok {
		return sql.ErrNoRows
	}
	if other, exists := f.usersByEmail[email]; exists && other.ID != userID {
		return store.ErrSCIMEmailTaken
	}
	delete(f.usersByEmail, u.Email)
	u.Email = email
	f.usersByEmail[email] = u
	f.emailUpdates[userID] = email
	return nil
}

// ─── router ───

func scimTestRouter(f *fakeScimStore) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := &SCIMHandler{store: f, baseURL: "https://sp.example.com"}
	r := gin.New()
	r.GET("/scim/v2/Users", h.ListUsers)
	r.POST("/scim/v2/Users", h.CreateUser)
	r.GET("/scim/v2/Users/:id", h.GetUser)
	r.PUT("/scim/v2/Users/:id", h.ReplaceUser)
	r.PATCH("/scim/v2/Users/:id", h.PatchUser)
	r.DELETE("/scim/v2/Users/:id", h.DeleteUser)
	return r
}

func scimReq(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/scim+json")
	}
	r.ServeHTTP(w, req)
	return w
}

func decodeList(t *testing.T, body []byte) sso.SCIMListResponse {
	t.Helper()
	var l sso.SCIMListResponse
	if err := json.Unmarshal(body, &l); err != nil {
		t.Fatalf("bad list body %s: %v", body, err)
	}
	return l
}

func decodeUser(t *testing.T, body []byte) sso.SCIMUser {
	t.Helper()
	var u sso.SCIMUser
	if err := json.Unmarshal(body, &u); err != nil {
		t.Fatalf("bad user body %s: %v", body, err)
	}
	return u
}

func assertSCIMErr(t *testing.T, w *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	if w.Code != wantStatus {
		t.Fatalf("status = %d, want %d, body %s", w.Code, wantStatus, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "application/scim+json") {
		t.Fatalf("error content-type = %q, want application/scim+json", ct)
	}
	var e sso.SCIMError
	if err := json.Unmarshal(w.Body.Bytes(), &e); err != nil {
		t.Fatalf("error body is not SCIM shape: %s", w.Body.String())
	}
	if e.Status != fmt.Sprint(wantStatus) {
		t.Fatalf("SCIM error status = %q, want %d", e.Status, wantStatus)
	}
}

// seedUser registers a user + active identity and returns the SCIM id.
func seedUser(f *fakeScimStore, email, name string) (scimID, userID string) {
	u := &model.User{ID: fmt.Sprintf("user-seed-%s", email), Email: email, Name: name}
	f.users[u.ID] = u
	f.usersByEmail[email] = u
	ident := &store.SCIMIdentity{ID: fmt.Sprintf("scim-seed-%s", email), UserID: u.ID, Active: true}
	f.idents[ident.ID] = ident
	f.identByUser[u.ID] = ident
	return ident.ID, u.ID
}

// ─── GET /Users ───

func TestSCIMListUsers(t *testing.T) {
	f := newFakeScimStore()
	seedUser(f, "alice@acme.com", "Alice Smith")
	seedUser(f, "bob@acme.com", "Bob Jones")
	r := scimTestRouter(f)

	w := scimReq(r, "GET", "/scim/v2/Users", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	l := decodeList(t, w.Body.Bytes())
	if l.TotalResults != 2 {
		t.Fatalf("totalResults = %d", l.TotalResults)
	}
	if len(l.Resources) != 2 {
		t.Fatalf("resources = %d", len(l.Resources))
	}
}

func TestSCIMListUsersFilterHit(t *testing.T) {
	f := newFakeScimStore()
	seedUser(f, "alice@acme.com", "Alice Smith")
	seedUser(f, "bob@acme.com", "Bob Jones")
	r := scimTestRouter(f)

	w := scimReq(r, "GET", `/scim/v2/Users?filter=`+urlQueryEscape(`userName eq "alice@acme.com"`), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	l := decodeList(t, w.Body.Bytes())
	if l.TotalResults != 1 || len(l.Resources) != 1 {
		t.Fatalf("want 1 result, got %+v", l)
	}
	if l.Resources[0].UserName != "alice@acme.com" {
		t.Fatalf("filtered wrong user: %s", l.Resources[0].UserName)
	}
}

func TestSCIMListUsersFilterMissIsEmptyList(t *testing.T) {
	f := newFakeScimStore()
	seedUser(f, "alice@acme.com", "Alice Smith")
	r := scimTestRouter(f)

	w := scimReq(r, "GET", `/scim/v2/Users?filter=`+urlQueryEscape(`userName eq "nobody@acme.com"`), "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	l := decodeList(t, w.Body.Bytes())
	if l.TotalResults != 0 || len(l.Resources) != 0 {
		t.Fatalf("filter miss should be an empty list, got %+v", l)
	}
}

func TestSCIMListUsersBadFilter(t *testing.T) {
	f := newFakeScimStore()
	r := scimTestRouter(f)
	w := scimReq(r, "GET", `/scim/v2/Users?filter=`+urlQueryEscape(`active eq "true"`), "")
	assertSCIMErr(t, w, http.StatusBadRequest)
}

// ─── GET /Users/:id ───

func TestSCIMGetUser(t *testing.T) {
	f := newFakeScimStore()
	id, _ := seedUser(f, "alice@acme.com", "Alice Smith")
	r := scimTestRouter(f)

	w := scimReq(r, "GET", "/scim/v2/Users/"+id, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	u := decodeUser(t, w.Body.Bytes())
	if u.ID != id || u.UserName != "alice@acme.com" {
		t.Fatalf("user = %+v", u)
	}
	if u.Name == nil || u.Name.GivenName != "Alice" || u.Name.FamilyName != "Smith" {
		t.Fatalf("name split wrong: %+v", u.Name)
	}
	if u.Meta == nil || u.Meta.Location != "https://sp.example.com/scim/v2/Users/"+id {
		t.Fatalf("meta.location wrong: %+v", u.Meta)
	}
}

func TestSCIMGetUserNotFound(t *testing.T) {
	f := newFakeScimStore()
	r := scimTestRouter(f)
	w := scimReq(r, "GET", "/scim/v2/Users/nope", "")
	assertSCIMErr(t, w, http.StatusNotFound)
}

// ─── POST /Users ───

func TestSCIMCreateUserNew(t *testing.T) {
	f := newFakeScimStore()
	r := scimTestRouter(f)
	body := `{"schemas":["urn:ietf:params:scim:schemas:core:2.0:User"],` +
		`"userName":"carol@acme.com","name":{"givenName":"Carol","familyName":"Jones"},` +
		`"externalId":"ext-carol"}`

	w := scimReq(r, "POST", "/scim/v2/Users", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	u := decodeUser(t, w.Body.Bytes())
	if u.UserName != "carol@acme.com" || u.ExternalID != "ext-carol" {
		t.Fatalf("user = %+v", u)
	}
	if !u.Active {
		t.Fatal("new user should default to active")
	}
	if loc := w.Header().Get("Location"); loc != "https://sp.example.com/scim/v2/Users/"+u.ID {
		t.Fatalf("Location = %q", loc)
	}
	if f.createdIdents != 1 {
		t.Fatalf("identity not created: %d", f.createdIdents)
	}
}

func TestSCIMCreateUserLinksExisting(t *testing.T) {
	f := newFakeScimStore()
	// A platform user already exists (e.g. via OTP) with no SCIM identity.
	existing := &model.User{ID: "user-existing", Email: "dave@acme.com", Name: "Dave"}
	f.users[existing.ID] = existing
	f.usersByEmail[existing.Email] = existing

	r := scimTestRouter(f)
	body := `{"userName":"dave@acme.com"}`
	w := scimReq(r, "POST", "/scim/v2/Users", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	u := decodeUser(t, w.Body.Bytes())
	// Should link, not create a second user.
	if u.ID == "" {
		t.Fatal("no id")
	}
	if len(f.users) != 1 {
		t.Fatalf("expected link (no duplicate user), got %d users", len(f.users))
	}
	if f.idents[u.ID].UserID != existing.ID {
		t.Fatalf("identity not linked to existing user")
	}
}

func TestSCIMCreateUserDuplicate(t *testing.T) {
	f := newFakeScimStore()
	id, _ := seedUser(f, "alice@acme.com", "Alice Smith")
	_ = id
	r := scimTestRouter(f)
	w := scimReq(r, "POST", "/scim/v2/Users", `{"userName":"alice@acme.com"}`)
	assertSCIMErr(t, w, http.StatusConflict)
}

func TestSCIMCreateUserNoUserName(t *testing.T) {
	f := newFakeScimStore()
	r := scimTestRouter(f)
	w := scimReq(r, "POST", "/scim/v2/Users", `{"name":{"givenName":"X"}}`)
	assertSCIMErr(t, w, http.StatusBadRequest)
}

func TestSCIMCreateUserBadJSON(t *testing.T) {
	f := newFakeScimStore()
	r := scimTestRouter(f)
	w := scimReq(r, "POST", "/scim/v2/Users", `{not json`)
	assertSCIMErr(t, w, http.StatusBadRequest)
}

func TestSCIMCreateUserActiveFalse(t *testing.T) {
	f := newFakeScimStore()
	r := scimTestRouter(f)
	w := scimReq(r, "POST", "/scim/v2/Users", `{"userName":"eve@acme.com","active":false}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d", w.Code)
	}
	u := decodeUser(t, w.Body.Bytes())
	if u.Active {
		t.Fatal("active:false should provision inactive")
	}
}

// ─── PUT /Users/:id ───

func TestSCIMReplaceUser(t *testing.T) {
	f := newFakeScimStore()
	id, userID := seedUser(f, "alice@acme.com", "Alice Smith")
	r := scimTestRouter(f)
	body := `{"userName":"alice@acme.com","name":{"givenName":"Alicia","familyName":"Smith"},"active":false,"externalId":"ext-2"}`
	w := scimReq(r, "PUT", "/scim/v2/Users/"+id, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	u := decodeUser(t, w.Body.Bytes())
	if u.Name.GivenName != "Alicia" {
		t.Fatalf("name not updated: %+v", u.Name)
	}
	if u.Active {
		t.Fatal("active should be false after replace")
	}
	if f.nameUpdates[userID] != "Alicia Smith" {
		t.Fatalf("name not persisted: %v", f.nameUpdates)
	}
	if f.activeSets[id] != false {
		t.Fatalf("active not persisted: %v", f.activeSets)
	}
}

// ─── PATCH /Users/:id ───

func TestSCIMPatchDeactivate(t *testing.T) {
	f := newFakeScimStore()
	id, _ := seedUser(f, "alice@acme.com", "Alice Smith")
	r := scimTestRouter(f)
	body := `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],` +
		`"Operations":[{"op":"replace","path":"active","value":false}]}`
	w := scimReq(r, "PATCH", "/scim/v2/Users/"+id, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	u := decodeUser(t, w.Body.Bytes())
	if u.Active {
		t.Fatal("active should be false")
	}
	if f.activeSets[id] != false {
		t.Fatalf("SetSCIMIdentityActive not called: %v", f.activeSets)
	}
}

func TestSCIMPatchChangeUserName(t *testing.T) {
	f := newFakeScimStore()
	id, userID := seedUser(f, "alice@acme.com", "Alice Smith")
	r := scimTestRouter(f)
	body := `{"Operations":[{"op":"replace","path":"userName","value":"alicia@acme.com"}]}`
	w := scimReq(r, "PATCH", "/scim/v2/Users/"+id, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if f.emailUpdates[userID] != "alicia@acme.com" {
		t.Fatalf("email not persisted: %v", f.emailUpdates)
	}
}

func TestSCIMPatchUserNameConflict(t *testing.T) {
	f := newFakeScimStore()
	id, _ := seedUser(f, "alice@acme.com", "Alice Smith")
	seedUser(f, "taken@acme.com", "Taken")
	r := scimTestRouter(f)
	body := `{"Operations":[{"op":"replace","path":"userName","value":"taken@acme.com"}]}`
	w := scimReq(r, "PATCH", "/scim/v2/Users/"+id, body)
	assertSCIMErr(t, w, http.StatusConflict)
}

func TestSCIMPatchChangeName(t *testing.T) {
	f := newFakeScimStore()
	id, userID := seedUser(f, "alice@acme.com", "Alice Smith")
	r := scimTestRouter(f)
	body := `{"Operations":[{"op":"replace","path":"name.givenName","value":"Alicia"}]}`
	w := scimReq(r, "PATCH", "/scim/v2/Users/"+id, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if f.nameUpdates[userID] != "Alicia Smith" {
		t.Fatalf("name not persisted: %v", f.nameUpdates)
	}
}

func TestSCIMPatchBadOp(t *testing.T) {
	f := newFakeScimStore()
	id, _ := seedUser(f, "alice@acme.com", "Alice Smith")
	r := scimTestRouter(f)
	w := scimReq(r, "PATCH", "/scim/v2/Users/"+id, `{"Operations":[{"op":"insert","path":"active","value":true}]}`)
	assertSCIMErr(t, w, http.StatusBadRequest)
}

// ─── DELETE /Users/:id ───

func TestSCIMDeleteDeactivates(t *testing.T) {
	f := newFakeScimStore()
	id, _ := seedUser(f, "alice@acme.com", "Alice Smith")
	r := scimTestRouter(f)
	w := scimReq(r, "DELETE", "/scim/v2/Users/"+id, "")
	if w.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if f.activeSets[id] != false {
		t.Fatalf("delete should deactivate: %v", f.activeSets)
	}
	// The user row is preserved (only the identity deactivates).
	if len(f.users) != 1 {
		t.Fatalf("user row must be preserved, got %d users", len(f.users))
	}
}

func TestSCIMDeleteNotFound(t *testing.T) {
	f := newFakeScimStore()
	r := scimTestRouter(f)
	w := scimReq(r, "DELETE", "/scim/v2/Users/nope", "")
	assertSCIMErr(t, w, http.StatusNotFound)
}

// urlQueryEscape is a tiny wrapper so the test reads cleanly.
func urlQueryEscape(s string) string { return url.QueryEscape(s) }
