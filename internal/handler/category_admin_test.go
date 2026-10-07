package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// fakeCategoryStore stands in for store.Store so the refusal paths —
// duplicate slug, missing row — run without a database. It records
// what the handler wrote and what it audited.
type fakeCategoryStore struct {
	cats       map[string]*model.Category
	list       []*model.Category
	createErr  error
	updateErr  error
	deleteErr  error
	lastCreate *model.Category
	lastUpdate *model.Category
	audits     []*model.AuditLog
}

func (f *fakeCategoryStore) ListCategories(_ context.Context, _ string, _ store.Page) ([]*model.Category, int, error) {
	return f.list, len(f.list), nil
}

func (f *fakeCategoryStore) FindCategoryByID(_ context.Context, id string) (*model.Category, error) {
	if cat, ok := f.cats[id]; ok {
		// A copy, like a real row read: the handler merges a patch
		// into what it read, and a shared pointer would make a
		// refused patch mutate the fixture behind the test's back.
		cp := *cat
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeCategoryStore) CreateCategory(_ context.Context, cat *model.Category) error {
	if f.createErr != nil {
		return f.createErr
	}
	cat.ID = "cat-new"
	f.lastCreate = cat
	return nil
}

func (f *fakeCategoryStore) UpdateCategory(_ context.Context, cat *model.Category) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.lastUpdate = cat
	return nil
}

func (f *fakeCategoryStore) DeleteCategory(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.cats[id]; !ok {
		return sql.ErrNoRows
	}
	delete(f.cats, id)
	return nil
}

func (f *fakeCategoryStore) Audit(_ context.Context, log *model.AuditLog) {
	f.audits = append(f.audits, log)
}

func categoryReq(t *testing.T, target string, body string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return w, c
}

// The stored form is canonical whatever the admin typed, and the slug
// derives from the name when none is sent — so one category cannot
// exist under several handles.
func TestPrepareCategoryNormalizesInPlace(t *testing.T) {
	cat := &model.Category{Name: " Dev Tools ", Slug: " Dev_Tools! ", Description: " tools ", Position: 3}
	if err := prepareCategory(cat); err != nil {
		t.Fatalf("prepareCategory: unexpected err %v", err)
	}
	if cat.Name != "Dev Tools" {
		t.Errorf("Name = %q, want %q", cat.Name, "Dev Tools")
	}
	if cat.Slug != "dev-tools" {
		t.Errorf("Slug = %q, want %q", cat.Slug, "dev-tools")
	}
	if cat.Description != "tools" {
		t.Errorf("Description = %q, want %q", cat.Description, "tools")
	}

	derived := &model.Category{Name: "Data Science 101"}
	if err := prepareCategory(derived); err != nil {
		t.Fatalf("derived slug: unexpected err %v", err)
	}
	if derived.Slug != "data-science-101" {
		t.Errorf("derived Slug = %q, want %q", derived.Slug, "data-science-101")
	}
}

// What the catalog refuses: no name, no slug left after folding, a
// slug the policy will not take, a negative position. Every one of
// these is a 400 the admin can fix, not a 500.
func TestPrepareCategoryRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		cat  model.Category
	}{
		{"empty name", model.Category{}},
		{"blank name", model.Category{Name: "   "}},
		{"unfillable slug", model.Category{Name: "Tools", Slug: "!!!"}},
		{"short slug", model.Category{Name: "Tools", Slug: "x"}},
		{"negative position", model.Category{Name: "Tools", Position: -1}},
	}
	for _, tc := range cases {
		cat := tc.cat
		if err := prepareCategory(&cat); err == nil {
			t.Errorf("%s: accepted, want refusal", tc.name)
		} else if err.Status != 400 {
			t.Errorf("%s: status %d, want 400", tc.name, err.Status)
		}
	}
}

// The slug handle is unique; a second category claiming it is a state
// conflict the admin can act on, not a server error.
func TestCategoryCreateDuplicateSlugIs409(t *testing.T) {
	fake := &fakeCategoryStore{createErr: store.ErrCategorySlugTaken}
	h := NewCategoryAdminHandler(nil)
	h.cat = fake

	w, c := categoryReq(t, "/admin/categories", `{"name":"Dev Tools","slug":"dev-tools"}`)
	h.Create(c)

	if w.Code != 409 {
		t.Fatalf("status = %d, want 409; body %s", w.Code, w.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the error envelope: %v", err)
	}
	if body.Error.Code != "DUPLICATE" {
		t.Errorf("code = %q, want DUPLICATE", body.Error.Code)
	}
}

// Any other write failure is not the admin's doing and must not be
// dressed up as a conflict.
func TestCategoryCreateOtherErrorIs500(t *testing.T) {
	fake := &fakeCategoryStore{createErr: errors.New("connection reset")}
	h := NewCategoryAdminHandler(nil)
	h.cat = fake

	w, c := categoryReq(t, "/admin/categories", `{"name":"Dev Tools"}`)
	h.Create(c)

	if w.Code != 500 {
		t.Fatalf("status = %d, want 500; body %s", w.Code, w.Body.String())
	}
}

// A successful create writes the row through the shared normalizer and
// leaves an audit entry naming the category and the admin.
func TestCategoryCreateAudits(t *testing.T) {
	fake := &fakeCategoryStore{}
	h := NewCategoryAdminHandler(nil)
	h.cat = fake

	w, c := categoryReq(t, "/admin/categories", `{"name":" Dev Tools ","position":2}`)
	h.Create(c)

	if w.Code != 201 {
		t.Fatalf("status = %d, want 201; body %s", w.Code, w.Body.String())
	}
	if fake.lastCreate == nil || fake.lastCreate.Slug != "dev-tools" || fake.lastCreate.Position != 2 {
		t.Fatalf("stored row = %+v, want slug dev-tools at position 2", fake.lastCreate)
	}
	if len(fake.audits) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(fake.audits))
	}
	a := fake.audits[0]
	if a.Entity != "category" || a.EntityID != "cat-new" || a.Action != "created" || a.ActorType != "admin" {
		t.Errorf("audit = %+v, want entity category/cat-new created by admin", a)
	}
}

// Updating a name without a slug keeps the stored handle: a partial
// update must not re-derive it and silently break every category URL.
func TestCategoryUpdateKeepsSlugAndRevalidates(t *testing.T) {
	fake := &fakeCategoryStore{cats: map[string]*model.Category{
		"c1": {ID: "c1", Name: "Old Name", Slug: "old-slug", Position: 1},
	}}
	h := NewCategoryAdminHandler(nil)
	h.cat = fake

	w, c := categoryReq(t, "/admin/categories/c1", `{"name":"New Name"}`)
	c.Params = gin.Params{{Key: "id", Value: "c1"}}
	h.Update(c)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if fake.lastUpdate == nil || fake.lastUpdate.Slug != "old-slug" || fake.lastUpdate.Name != "New Name" {
		t.Fatalf("updated row = %+v, want the stored slug kept", fake.lastUpdate)
	}

	// A patch that renames onto a refused slug is refused like a
	// create, before it reaches the store.
	w2, c2 := categoryReq(t, "/admin/categories/c1", `{"slug":"x"}`)
	c2.Params = gin.Params{{Key: "id", Value: "c1"}}
	h.Update(c2)
	if w2.Code != 400 {
		t.Fatalf("bad patch status = %d, want 400", w2.Code)
	}
	if fake.lastUpdate.Slug != "old-slug" {
		t.Errorf("refused patch still changed the slug to %q", fake.lastUpdate.Slug)
	}
}

// Deleting an unknown category says 404, not 204 for a row that was
// never there.
func TestCategoryDeleteMissingIs404(t *testing.T) {
	fake := &fakeCategoryStore{cats: map[string]*model.Category{}}
	h := NewCategoryAdminHandler(nil)
	h.cat = fake

	w, c := categoryReq(t, "/admin/categories/nope", "")
	c.Request.Method = "DELETE"
	c.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.Delete(c)

	if w.Code != 404 {
		t.Fatalf("status = %d, want 404; body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CATEGORY_NOT_FOUND") {
		t.Errorf("body does not name CATEGORY_NOT_FOUND: %s", w.Body.String())
	}
	if len(fake.audits) != 0 {
		t.Errorf("a failed delete left %d audit entries", len(fake.audits))
	}
}

// The update path answers the same duplicate conflict as the create
// path: a rename onto a taken handle is 409 from either door.
func TestCategoryUpdateDuplicateSlugIs409(t *testing.T) {
	fake := &fakeCategoryStore{
		cats:      map[string]*model.Category{"c1": {ID: "c1", Name: "One", Slug: "one"}},
		updateErr: store.ErrCategorySlugTaken,
	}
	h := NewCategoryAdminHandler(nil)
	h.cat = fake

	w, c := categoryReq(t, "/admin/categories/c1", `{"slug":"two"}`)
	c.Params = gin.Params{{Key: "id", Value: "c1"}}
	h.Update(c)

	if w.Code != 409 {
		t.Fatalf("status = %d, want 409; body %s", w.Code, w.Body.String())
	}
}
