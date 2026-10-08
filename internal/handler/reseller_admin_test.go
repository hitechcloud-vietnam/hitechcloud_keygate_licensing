package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// fakeResellerStore stands in for store.Store so the refusal paths —
// duplicate email, double allocation, delete with allocations — run
// without a database. It records what the handler wrote and audited.
type fakeResellerStore struct {
	resellers map[string]*model.Reseller
	list      []*model.Reseller
	licenses  map[string][]*model.License

	createErr     error
	updateErr     error
	deleteErr     error
	allocateErr   error
	deallocateErr error
	listLicErr    error
	countErr      error

	lastCreate   *model.Reseller
	lastUpdate   *model.Reseller
	lastAllocate [2]string
	lastDealloc  string
	audits       []*model.AuditLog

	// Commission ledger (slice 2).
	commissions     map[string]*model.Commission
	byOrder         map[[2]string]*model.Commission
	commissionList  []*model.Commission
	accrueErr       error
	markPaidErr     error
	listCommissions [2]string // last (reseller_id, status) filter
	lastAccrue      *model.Commission
	lastMarkPaidID  string
	lastPaidAt      time.Time

	// Wholesale price overrides (slice 2).
	prices          map[[2]string]*model.ResellerPriceOverride
	priceList       []*model.ResellerPriceOverride
	priceErr        error
	priceDeleteErr  error
	lastPrice       *model.ResellerPriceOverride
	lastPriceDelete [2]string
}

func (f *fakeResellerStore) ListResellers(_ context.Context, _, _ string, _ store.Page) ([]*model.Reseller, int, error) {
	return f.list, len(f.list), nil
}

func (f *fakeResellerStore) FindResellerByID(_ context.Context, id string) (*model.Reseller, error) {
	if r, ok := f.resellers[id]; ok {
		// A copy, like a real row read: the handler merges a patch into
		// what it read, and a shared pointer would make a refused patch
		// mutate the fixture behind the test's back.
		cp := *r
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeResellerStore) CreateReseller(_ context.Context, r *model.Reseller) error {
	if f.createErr != nil {
		return f.createErr
	}
	r.ID = "res-new"
	f.lastCreate = r
	return nil
}

func (f *fakeResellerStore) UpdateReseller(_ context.Context, r *model.Reseller) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.lastUpdate = r
	return nil
}

func (f *fakeResellerStore) DeleteReseller(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.resellers[id]; !ok {
		return sql.ErrNoRows
	}
	delete(f.resellers, id)
	return nil
}

func (f *fakeResellerStore) CountResellerLicenses(_ context.Context, resellerID string) (int, error) {
	if f.countErr != nil {
		return 0, f.countErr
	}
	return len(f.licenses[resellerID]), nil
}

func (f *fakeResellerStore) AllocateLicense(_ context.Context, resellerID, licenseID string) error {
	if f.allocateErr != nil {
		return f.allocateErr
	}
	f.lastAllocate = [2]string{resellerID, licenseID}
	return nil
}

func (f *fakeResellerStore) DeallocateLicense(_ context.Context, licenseID string) error {
	if f.deallocateErr != nil {
		return f.deallocateErr
	}
	f.lastDealloc = licenseID
	return nil
}

func (f *fakeResellerStore) ListResellerLicenses(_ context.Context, resellerID string, _ store.Page) ([]*model.License, int, error) {
	if f.listLicErr != nil {
		return nil, 0, f.listLicErr
	}
	rows := f.licenses[resellerID]
	return rows, len(rows), nil
}

func (f *fakeResellerStore) Audit(_ context.Context, log *model.AuditLog) {
	f.audits = append(f.audits, log)
}

// AccrueCommission mirrors the real store's idempotency: a repeat for
// the same (reseller, order) answers the ORIGINAL row with created=false
// and writes nothing.
func (f *fakeResellerStore) AccrueCommission(_ context.Context, cm *model.Commission) (*model.Commission, bool, error) {
	if f.accrueErr != nil {
		return nil, false, f.accrueErr
	}
	if f.byOrder == nil {
		f.byOrder = map[[2]string]*model.Commission{}
	}
	if f.commissions == nil {
		f.commissions = map[string]*model.Commission{}
	}
	key := [2]string{cm.ResellerID, cm.OrderID}
	if existing, ok := f.byOrder[key]; ok {
		cp := *existing
		return &cp, false, nil
	}
	f.lastAccrue = cm
	row := *cm
	row.ID = "com-new"
	row.Status = model.CommissionStatusAccrued
	row.AmountMinor = model.CommissionAmount(cm.BasisMinor, cm.BPS)
	f.byOrder[key] = &row
	f.commissions[row.ID] = &row
	cp := row
	return &cp, true, nil
}

func (f *fakeResellerStore) FindCommissionByID(_ context.Context, id string) (*model.Commission, error) {
	if cm, ok := f.commissions[id]; ok {
		cp := *cm
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeResellerStore) ListCommissions(_ context.Context, resellerID, status string, _ store.Page) ([]*model.Commission, int, error) {
	f.listCommissions = [2]string{resellerID, status}
	return f.commissionList, len(f.commissionList), nil
}

func (f *fakeResellerStore) MarkCommissionPaid(_ context.Context, id string, paidAt time.Time) (*model.Commission, error) {
	if f.markPaidErr != nil {
		return nil, f.markPaidErr
	}
	cm, ok := f.commissions[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	f.lastMarkPaidID = id
	f.lastPaidAt = paidAt
	cm.Status = model.CommissionStatusPaid
	cm.PaidAt = &paidAt
	cp := *cm
	return &cp, nil
}

func (f *fakeResellerStore) FindResellerPriceOverride(_ context.Context, resellerID, planID string) (*model.ResellerPriceOverride, error) {
	if o, ok := f.prices[[2]string{resellerID, planID}]; ok {
		cp := *o
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeResellerStore) ListResellerPriceOverrides(_ context.Context, resellerID string) ([]*model.ResellerPriceOverride, error) {
	if f.priceErr != nil {
		return nil, f.priceErr
	}
	rows := f.priceList
	if rows == nil {
		rows = []*model.ResellerPriceOverride{}
	}
	_ = resellerID
	return rows, nil
}

func (f *fakeResellerStore) SetResellerPriceOverride(_ context.Context, o *model.ResellerPriceOverride) error {
	if f.priceErr != nil {
		return f.priceErr
	}
	if f.prices == nil {
		f.prices = map[[2]string]*model.ResellerPriceOverride{}
	}
	f.lastPrice = o
	cp := *o
	f.prices[[2]string{o.ResellerID, o.PlanID}] = &cp
	return nil
}

func (f *fakeResellerStore) DeleteResellerPriceOverride(_ context.Context, resellerID, planID string) error {
	if f.priceDeleteErr != nil {
		return f.priceDeleteErr
	}
	key := [2]string{resellerID, planID}
	if _, ok := f.prices[key]; !ok {
		return sql.ErrNoRows
	}
	delete(f.prices, key)
	f.lastPriceDelete = key
	return nil
}

func resellerReq(t *testing.T, method, target, body string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return w, c
}

func resellerErrCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not the error envelope: %v; body %s", err, w.Body.String())
	}
	return body.Error.Code
}

// The stored form is canonical and defaulted whatever the admin typed:
// trimmed name, folded email, status falling back to active, and a
// commission rate bounded to a real percentage.
func TestPrepareResellerNormalizesAndDefaults(t *testing.T) {
	r := &model.Reseller{Name: "  Acme  ", ContactEmail: "  Partner@Example.COM  ", Notes: "  hello  "}
	if err := prepareReseller(r); err != nil {
		t.Fatalf("prepareReseller: unexpected err %v", err)
	}
	if r.Name != "Acme" {
		t.Errorf("Name = %q, want %q", r.Name, "Acme")
	}
	if r.ContactEmail != "partner@example.com" {
		t.Errorf("ContactEmail = %q, want %q", r.ContactEmail, "partner@example.com")
	}
	if r.Status != model.ResellerStatusActive {
		t.Errorf("Status = %q, want %q", r.Status, model.ResellerStatusActive)
	}
	if r.Notes != "hello" {
		t.Errorf("Notes = %q, want %q", r.Notes, "hello")
	}
}

// What a reseller cannot carry: no name, an unusable email, a status
// outside the vocabulary, a commission rate outside 0–10000 bps. Every
// one is a 400 the admin can fix, not a 500.
func TestPrepareResellerRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		r    model.Reseller
	}{
		{"empty name", model.Reseller{ContactEmail: "a@b.co"}},
		{"blank name", model.Reseller{Name: "   ", ContactEmail: "a@b.co"}},
		{"missing email", model.Reseller{Name: "Acme"}},
		{"bad email", model.Reseller{Name: "Acme", ContactEmail: "not-an-email"}},
		{"display-name email", model.Reseller{Name: "Acme", ContactEmail: "Acme <a@b.co>"}},
		{"bad status", model.Reseller{Name: "Acme", ContactEmail: "a@b.co", Status: "deleted"}},
		{"negative commission", model.Reseller{Name: "Acme", ContactEmail: "a@b.co", CommissionBPS: -1}},
		{"commission over 100%", model.Reseller{Name: "Acme", ContactEmail: "a@b.co", CommissionBPS: 10001}},
	}
	for _, tc := range cases {
		r := tc.r
		if err := prepareReseller(&r); err == nil {
			t.Errorf("%s: accepted, want refusal", tc.name)
		} else if err.Status != 400 {
			t.Errorf("%s: status %d, want 400", tc.name, err.Status)
		}
	}
}

// The contact email is the reseller's handle and the column is unique;
// a second reseller claiming it is a state conflict the admin can act
// on, not a server error.
func TestResellerCreateDuplicateEmailIs409(t *testing.T) {
	fake := &fakeResellerStore{createErr: store.ErrResellerEmailTaken}
	h := NewResellerAdminHandler(nil)
	h.store = fake

	w, c := resellerReq(t, "POST", "/admin/resellers", `{"name":"Acme","contact_email":"acme@example.com"}`)
	h.Create(c)

	if w.Code != 409 {
		t.Fatalf("status = %d, want 409; body %s", w.Code, w.Body.String())
	}
	if code := resellerErrCode(t, w); code != "DUPLICATE" {
		t.Errorf("code = %q, want DUPLICATE", code)
	}
}

// Any other write failure is not the admin's doing and must not be
// dressed up as a conflict.
func TestResellerCreateOtherErrorIs500(t *testing.T) {
	fake := &fakeResellerStore{createErr: errors.New("connection reset")}
	h := NewResellerAdminHandler(nil)
	h.store = fake

	w, c := resellerReq(t, "POST", "/admin/resellers", `{"name":"Acme","contact_email":"acme@example.com"}`)
	h.Create(c)

	if w.Code != 500 {
		t.Fatalf("status = %d, want 500; body %s", w.Code, w.Body.String())
	}
}

// A successful create writes the row through the shared normalizer and
// leaves an audit entry naming the reseller and the admin.
func TestResellerCreateAudits(t *testing.T) {
	fake := &fakeResellerStore{}
	h := NewResellerAdminHandler(nil)
	h.store = fake

	w, c := resellerReq(t, "POST", "/admin/resellers", `{"name":"  Acme  ","contact_email":"acme@example.com"}`)
	h.Create(c)

	if w.Code != 201 {
		t.Fatalf("status = %d, want 201; body %s", w.Code, w.Body.String())
	}
	if fake.lastCreate == nil || fake.lastCreate.Name != "Acme" {
		t.Fatalf("stored row = %+v, want trimmed name", fake.lastCreate)
	}
	if len(fake.audits) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(fake.audits))
	}
	a := fake.audits[0]
	if a.Entity != "reseller" || a.EntityID != "res-new" || a.Action != "created" || a.ActorType != "admin" {
		t.Errorf("audit = %+v, want entity reseller/res-new created by admin", a)
	}
}

// The update path answers the same duplicate conflict as the create
// path, and a patch to a missing row is 404 before it ever writes.
func TestResellerUpdateResponses(t *testing.T) {
	fake := &fakeResellerStore{
		resellers: map[string]*model.Reseller{
			"r1": {ID: "r1", Name: "Acme", ContactEmail: "acme@example.com", Status: "active"},
		},
		updateErr: store.ErrResellerEmailTaken,
	}
	h := NewResellerAdminHandler(nil)
	h.store = fake

	w, c := resellerReq(t, "PATCH", "/admin/resellers/r1", `{"contact_email":"other@example.com"}`)
	c.Params = gin.Params{{Key: "id", Value: "r1"}}
	h.Update(c)
	if w.Code != 409 {
		t.Fatalf("rename-onto-taken status = %d, want 409; body %s", w.Code, w.Body.String())
	}

	w2, c2 := resellerReq(t, "PATCH", "/admin/resellers/nope", `{"name":"X"}`)
	c2.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.Update(c2)
	if w2.Code != 404 {
		t.Fatalf("missing update status = %d, want 404", w2.Code)
	}

	// A patch that puts a bad status on a real reseller is refused
	// before it reaches the store.
	fake.updateErr = nil
	w3, c3 := resellerReq(t, "PATCH", "/admin/resellers/r1", `{"status":"bogus"}`)
	c3.Params = gin.Params{{Key: "id", Value: "r1"}}
	h.Update(c3)
	if w3.Code != 400 {
		t.Fatalf("bad status patch = %d, want 400", w3.Code)
	}
	if fake.lastUpdate != nil {
		t.Errorf("a refused patch still wrote %+v", fake.lastUpdate)
	}
}

// Deleting an unknown reseller says 404; a reseller that still owns
// licences is refused 409 (the documented delete policy); a clean
// delete is 204 with an audit entry.
func TestResellerDeleteResponses(t *testing.T) {
	// Missing -> 404, no audit.
	fake := &fakeResellerStore{resellers: map[string]*model.Reseller{}}
	h := NewResellerAdminHandler(nil)
	h.store = fake
	w, c := resellerReq(t, "DELETE", "/admin/resellers/nope", "")
	c.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.Delete(c)
	if w.Code != 404 {
		t.Fatalf("missing delete status = %d, want 404", w.Code)
	}
	if len(fake.audits) != 0 {
		t.Errorf("a failed delete left %d audit entries", len(fake.audits))
	}

	// Has allocations -> 409 with a machine-readable code.
	fake = &fakeResellerStore{
		resellers: map[string]*model.Reseller{"r1": {ID: "r1"}},
		deleteErr: store.ErrResellerHasAllocations,
	}
	h.store = fake
	w2, c2 := resellerReq(t, "DELETE", "/admin/resellers/r1", "")
	c2.Params = gin.Params{{Key: "id", Value: "r1"}}
	h.Delete(c2)
	if w2.Code != 409 {
		t.Fatalf("delete-with-allocations status = %d, want 409", w2.Code)
	}
	if code := resellerErrCode(t, w2); code != "RESSELLER_HAS_ALLOCATIONS" {
		t.Errorf("code = %q, want RESSELLER_HAS_ALLOCATIONS", code)
	}

	// Clean delete -> 204 with an audit entry.
	fake = &fakeResellerStore{resellers: map[string]*model.Reseller{"r1": {ID: "r1"}}}
	h.store = fake
	w3, c3 := resellerReq(t, "DELETE", "/admin/resellers/r1", "")
	c3.Params = gin.Params{{Key: "id", Value: "r1"}}
	h.Delete(c3)
	// NoContent only records the status; the engine flushes it after the
	// handler in a real request. Flush here so w.Code surfaces 204.
	c3.Writer.WriteHeaderNow()
	if w3.Code != 204 {
		t.Fatalf("clean delete status = %d, want 204", w3.Code)
	}
	if len(fake.audits) != 1 || fake.audits[0].Action != "deleted" {
		t.Errorf("clean delete audits = %+v, want one 'deleted'", fake.audits)
	}
}

// Allocation refusals map to distinct statuses: a missing reseller is
// 404, an unknown licence a 404 (LICENSE_NOT_FOUND keeps its single
// status repo-wide), an already-owned licence a 409, and a body
// without license_id a 400.
func TestResellerAllocateLicenseResponses(t *testing.T) {
	// Missing reseller -> 404.
	fake := &fakeResellerStore{allocateErr: sql.ErrNoRows}
	h := NewResellerAdminHandler(nil)
	h.store = fake
	w, c := resellerReq(t, "POST", "/admin/resellers/nope/licenses", `{"license_id":"L1"}`)
	c.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.AllocateLicense(c)
	if w.Code != 404 {
		t.Fatalf("missing reseller status = %d, want 404", w.Code)
	}

	// Unknown licence -> 404.
	fake = &fakeResellerStore{allocateErr: store.ErrAllocationLicenseNotFound}
	h.store = fake
	w2, c2 := resellerReq(t, "POST", "/admin/resellers/r1/licenses", `{"license_id":"nope"}`)
	c2.Params = gin.Params{{Key: "id", Value: "r1"}}
	h.AllocateLicense(c2)
	if w2.Code != 404 {
		t.Fatalf("unknown licence status = %d, want 404", w2.Code)
	}
	if code := resellerErrCode(t, w2); code != "LICENSE_NOT_FOUND" {
		t.Errorf("code = %q, want LICENSE_NOT_FOUND", code)
	}

	// Already allocated -> 409.
	fake = &fakeResellerStore{allocateErr: store.ErrLicenseAlreadyAllocated}
	h.store = fake
	w3, c3 := resellerReq(t, "POST", "/admin/resellers/r1/licenses", `{"license_id":"L1"}`)
	c3.Params = gin.Params{{Key: "id", Value: "r1"}}
	h.AllocateLicense(c3)
	if w3.Code != 409 {
		t.Fatalf("already-allocated status = %d, want 409", w3.Code)
	}
	if code := resellerErrCode(t, w3); code != "ALREADY_ALLOCATED" {
		t.Errorf("code = %q, want ALREADY_ALLOCATED", code)
	}

	// Body without license_id -> 400, before any store call.
	fake = &fakeResellerStore{}
	h.store = fake
	w4, c4 := resellerReq(t, "POST", "/admin/resellers/r1/licenses", `{"other":"x"}`)
	c4.Params = gin.Params{{Key: "id", Value: "r1"}}
	h.AllocateLicense(c4)
	if w4.Code != 400 {
		t.Fatalf("missing license_id status = %d, want 400", w4.Code)
	}
}

// A successful allocation records the pair and leaves an audit entry on
// the reseller_license entity.
func TestResellerAllocateLicenseAudits(t *testing.T) {
	fake := &fakeResellerStore{}
	h := NewResellerAdminHandler(nil)
	h.store = fake

	w, c := resellerReq(t, "POST", "/admin/resellers/r1/licenses", `{"license_id":"L1"}`)
	c.Params = gin.Params{{Key: "id", Value: "r1"}}
	h.AllocateLicense(c)

	if w.Code != 201 {
		t.Fatalf("status = %d, want 201; body %s", w.Code, w.Body.String())
	}
	if fake.lastAllocate != [2]string{"r1", "L1"} {
		t.Errorf("allocated pair = %v, want [r1 L1]", fake.lastAllocate)
	}
	if len(fake.audits) != 1 {
		t.Fatalf("audit entries = %d, want 1", len(fake.audits))
	}
	a := fake.audits[0]
	if a.Entity != "reseller_license" || a.EntityID != "L1" || a.Action != "allocated" || a.ActorType != "admin" {
		t.Errorf("audit = %+v, want reseller_license/L1 allocated by admin", a)
	}
}

// Deallocating something not allocated is a 404; a clean deallocation
// is 204 with an audit entry.
func TestResellerDeallocateResponses(t *testing.T) {
	fake := &fakeResellerStore{deallocateErr: sql.ErrNoRows}
	h := NewResellerAdminHandler(nil)
	h.store = fake
	w, c := resellerReq(t, "DELETE", "/admin/resellers/r1/licenses/L1", "")
	c.Params = gin.Params{{Key: "license_id", Value: "L1"}}
	h.DeallocateLicense(c)
	if w.Code != 404 {
		t.Fatalf("not-allocated status = %d, want 404", w.Code)
	}

	fake = &fakeResellerStore{}
	h.store = fake
	w2, c2 := resellerReq(t, "DELETE", "/admin/resellers/r1/licenses/L1", "")
	c2.Params = gin.Params{{Key: "license_id", Value: "L1"}}
	h.DeallocateLicense(c2)
	c2.Writer.WriteHeaderNow() // flush the recorded 204 (see above)
	if w2.Code != 204 {
		t.Fatalf("clean deallocate status = %d, want 204", w2.Code)
	}
	if fake.lastDealloc != "L1" {
		t.Errorf("deallocated licence = %q, want L1", fake.lastDealloc)
	}
	if len(fake.audits) != 1 || fake.audits[0].Action != "deallocated" {
		t.Errorf("deallocate audits = %+v, want one 'deallocated'", fake.audits)
	}
}

// The allocation list checks the reseller first (a missing account is a
// 404, not an empty page), then answers the licence rows under the
// standard list envelope.
func TestResellerListLicenses(t *testing.T) {
	fake := &fakeResellerStore{resellers: map[string]*model.Reseller{}}
	h := NewResellerAdminHandler(nil)
	h.store = fake

	w, c := resellerReq(t, "GET", "/admin/resellers/nope/licenses", "")
	c.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.ListLicenses(c)
	if w.Code != 404 {
		t.Fatalf("missing reseller status = %d, want 404", w.Code)
	}

	fake = &fakeResellerStore{
		resellers: map[string]*model.Reseller{"r1": {ID: "r1"}},
		licenses:  map[string][]*model.License{"r1": {{ID: "L1"}, {ID: "L2"}}},
	}
	h.store = fake
	w2, c2 := resellerReq(t, "GET", "/admin/resellers/r1/licenses", "")
	c2.Params = gin.Params{{Key: "id", Value: "r1"}}
	h.ListLicenses(c2)
	if w2.Code != 200 {
		t.Fatalf("list status = %d, want 200; body %s", w2.Code, w2.Body.String())
	}
	var body struct {
		Data struct {
			Licenses []model.License `json:"licenses"`
			Total    int             `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad list body: %v", err)
	}
	if body.Data.Total != 2 || len(body.Data.Licenses) != 2 {
		t.Errorf("list = %d (total %d), want 2", len(body.Data.Licenses), body.Data.Total)
	}
}

// The status filter is a closed vocabulary; an unrecognised value is
// refused rather than silently served an empty page.
func TestResellerListStatusFilterValidation(t *testing.T) {
	fake := &fakeResellerStore{}
	h := NewResellerAdminHandler(nil)
	h.store = fake

	w, c := resellerReq(t, "GET", "/admin/resellers?status=bogus", "")
	h.List(c)
	if w.Code != 400 {
		t.Fatalf("bad status filter = %d, want 400", w.Code)
	}

	w2, c2 := resellerReq(t, "GET", "/admin/resellers?status=suspended", "")
	h.List(c2)
	if w2.Code != 200 {
		t.Fatalf("good status filter = %d, want 200", w2.Code)
	}
}

// ─── Commission ledger ───

// What an accrual cannot carry: a missing or blank order id, a missing
// or negative basis, a rate outside 0–10000 bps. Every one is a 400
// the admin can fix, and none of them reaches the store.
func TestAccrueCommissionValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"missing order_id", `{"basis_minor":1000,"bps":1000}`},
		{"blank order_id", `{"order_id":"  ","basis_minor":1000,"bps":1000}`},
		{"missing basis_minor", `{"order_id":"ord-1","bps":1000}`},
		{"negative basis_minor", `{"order_id":"ord-1","basis_minor":-1,"bps":1000}`},
		{"negative bps", `{"order_id":"ord-1","basis_minor":1000,"bps":-1}`},
		{"bps over 100%", `{"order_id":"ord-1","basis_minor":1000,"bps":10001}`},
		{"garbage body", `not-json`},
	}
	for _, tc := range cases {
		fake := &fakeResellerStore{
			resellers: map[string]*model.Reseller{"r1": {ID: "r1", CommissionBPS: 1000}},
		}
		h := NewResellerAdminHandler(nil)
		h.store = fake

		w, c := resellerReq(t, "POST", "/admin/resellers/r1/commissions", tc.body)
		c.Params = gin.Params{{Key: "id", Value: "r1"}}
		h.AccrueCommission(c)
		if w.Code != 400 {
			t.Errorf("%s: status = %d, want 400; body %s", tc.name, w.Code, w.Body.String())
		}
		if fake.lastAccrue != nil {
			t.Errorf("%s: a refused accrual still wrote %+v", tc.name, fake.lastAccrue)
		}
	}
}

// bps is optional and defaults to the reseller's contract rate; a
// stated rate is used verbatim once it is inside 0–10000.
func TestAccrueCommissionBPSHandling(t *testing.T) {
	fake := &fakeResellerStore{
		resellers: map[string]*model.Reseller{"r1": {ID: "r1", CommissionBPS: 2500}},
	}
	h := NewResellerAdminHandler(nil)
	h.store = fake

	w, c := resellerReq(t, "POST", "/admin/resellers/r1/commissions", `{"order_id":"ord-1","basis_minor":1000}`)
	c.Params = gin.Params{{Key: "id", Value: "r1"}}
	h.AccrueCommission(c)
	if w.Code != 201 {
		t.Fatalf("defaulted bps status = %d, want 201; body %s", w.Code, w.Body.String())
	}
	if fake.lastAccrue == nil || fake.lastAccrue.BPS != 2500 {
		t.Fatalf("defaulted bps = %+v, want the reseller's 2500", fake.lastAccrue)
	}

	w2, c2 := resellerReq(t, "POST", "/admin/resellers/r1/commissions", `{"order_id":"ord-2","basis_minor":1000,"bps":0}`)
	c2.Params = gin.Params{{Key: "id", Value: "r1"}}
	h.AccrueCommission(c2)
	if w2.Code != 201 {
		t.Fatalf("explicit zero bps status = %d, want 201 (zero is meaningful)", w2.Code)
	}
	if fake.lastAccrue.BPS != 0 {
		t.Errorf("explicit bps = %d, want 0", fake.lastAccrue.BPS)
	}
}

// The accrual is idempotent per (reseller, order): the first POST
// creates and audits, a retry answers the ORIGINAL row (200, same id)
// and writes nothing. A missing reseller is a 404 before anything else.
func TestAccrueCommissionIdempotency(t *testing.T) {
	fake := &fakeResellerStore{
		resellers: map[string]*model.Reseller{"r1": {ID: "r1", CommissionBPS: 1000}},
	}
	h := NewResellerAdminHandler(nil)
	h.store = fake

	w, c := resellerReq(t, "POST", "/admin/resellers/r1/commissions", `{"order_id":"ord-1","basis_minor":1000,"bps":1000}`)
	c.Params = gin.Params{{Key: "id", Value: "r1"}}
	h.AccrueCommission(c)
	if w.Code != 201 {
		t.Fatalf("first accrual status = %d, want 201; body %s", w.Code, w.Body.String())
	}
	if len(fake.audits) != 1 || fake.audits[0].Entity != "commission" || fake.audits[0].Action != "accrued" {
		t.Fatalf("first accrual audits = %+v, want one commission 'accrued'", fake.audits)
	}

	// Replay with different numbers: the first writer wins.
	w2, c2 := resellerReq(t, "POST", "/admin/resellers/r1/commissions", `{"order_id":"ord-1","basis_minor":999999,"bps":10000}`)
	c2.Params = gin.Params{{Key: "id", Value: "r1"}}
	h.AccrueCommission(c2)
	if w2.Code != 200 {
		t.Fatalf("replay status = %d, want 200 (the original row)", w2.Code)
	}
	var body struct {
		Data model.Commission `json:"data"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad replay body: %v", err)
	}
	if body.Data.ID != "com-new" || body.Data.BasisMinor != 1000 || body.Data.AmountMinor != 100 {
		t.Errorf("replay row = %+v, want the original com-new at basis 1000", body.Data)
	}
	if len(fake.audits) != 1 {
		t.Errorf("a replay audited again: %+v", fake.audits)
	}

	// Unknown reseller -> 404, nothing written.
	w3, c3 := resellerReq(t, "POST", "/admin/resellers/nope/commissions", `{"order_id":"ord-9","basis_minor":1000}`)
	c3.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.AccrueCommission(c3)
	if w3.Code != 404 {
		t.Fatalf("unknown reseller status = %d, want 404", w3.Code)
	}
}

// Marking paid: a missing commission is 404, one that belongs to
// another reseller is the SAME 404 (no existence oracle), a cancelled
// one is refused 409, and a clean mark is 200 with the disbursement
// date recorded and audited.
func TestMarkCommissionPaidResponses(t *testing.T) {
	// Missing -> 404 COMMISSION_NOT_FOUND.
	fake := &fakeResellerStore{resellers: map[string]*model.Reseller{"r1": {ID: "r1"}}}
	h := NewResellerAdminHandler(nil)
	h.store = fake
	w, c := resellerReq(t, "POST", "/admin/resellers/r1/commissions/nope/paid", "")
	c.Params = gin.Params{{Key: "id", Value: "r1"}, {Key: "commission_id", Value: "nope"}}
	h.MarkCommissionPaid(c)
	if w.Code != 404 {
		t.Fatalf("missing commission status = %d, want 404", w.Code)
	}
	if code := resellerErrCode(t, w); code != "COMMISSION_NOT_FOUND" {
		t.Errorf("code = %q, want COMMISSION_NOT_FOUND", code)
	}

	// Someone else's commission -> the same 404.
	fake = &fakeResellerStore{
		resellers:   map[string]*model.Reseller{"r1": {ID: "r1"}},
		commissions: map[string]*model.Commission{"com-x": {ID: "com-x", ResellerID: "r2"}},
	}
	h.store = fake
	w2, c2 := resellerReq(t, "POST", "/admin/resellers/r1/commissions/com-x/paid", "")
	c2.Params = gin.Params{{Key: "id", Value: "r1"}, {Key: "commission_id", Value: "com-x"}}
	h.MarkCommissionPaid(c2)
	if w2.Code != 404 {
		t.Fatalf("cross-reseller status = %d, want 404", w2.Code)
	}
	if len(fake.audits) != 0 {
		t.Errorf("a refused mark left %d audit entries", len(fake.audits))
	}

	// Cancelled -> 409, machine-readable.
	fake = &fakeResellerStore{
		resellers:   map[string]*model.Reseller{"r1": {ID: "r1"}},
		commissions: map[string]*model.Commission{"com-c": {ID: "com-c", ResellerID: "r1", Status: model.CommissionStatusCancelled}},
		markPaidErr: store.ErrCommissionCancelled,
	}
	h.store = fake
	w3, c3 := resellerReq(t, "POST", "/admin/resellers/r1/commissions/com-c/paid", "")
	c3.Params = gin.Params{{Key: "id", Value: "r1"}, {Key: "commission_id", Value: "com-c"}}
	h.MarkCommissionPaid(c3)
	if w3.Code != 409 {
		t.Fatalf("cancelled mark status = %d, want 409", w3.Code)
	}
	if code := resellerErrCode(t, w3); code != "COMMISSION_CANCELLED" {
		t.Errorf("code = %q, want COMMISSION_CANCELLED", code)
	}

	// Clean mark with a stated date -> 200, that date recorded.
	fake = &fakeResellerStore{
		resellers:   map[string]*model.Reseller{"r1": {ID: "r1"}},
		commissions: map[string]*model.Commission{"com-1": {ID: "com-1", ResellerID: "r1", Status: model.CommissionStatusApproved}},
	}
	h.store = fake
	w4, c4 := resellerReq(t, "POST", "/admin/resellers/r1/commissions/com-1/paid", `{"paid_at":"2026-10-08T12:00:00Z"}`)
	c4.Params = gin.Params{{Key: "id", Value: "r1"}, {Key: "commission_id", Value: "com-1"}}
	h.MarkCommissionPaid(c4)
	if w4.Code != 200 {
		t.Fatalf("clean mark status = %d, want 200; body %s", w4.Code, w4.Body.String())
	}
	want := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	if fake.lastMarkPaidID != "com-1" || !fake.lastPaidAt.Equal(want) {
		t.Errorf("marked = (%q, %v), want (com-1, %v)", fake.lastMarkPaidID, fake.lastPaidAt, want)
	}
	if len(fake.audits) != 1 || fake.audits[0].Entity != "commission" || fake.audits[0].Action != "paid" {
		t.Errorf("clean mark audits = %+v, want one commission 'paid'", fake.audits)
	}
}

// The ledger list checks the reseller first (404, not an empty page)
// and refuses a status filter outside the closed vocabulary.
func TestListCommissionsResponses(t *testing.T) {
	fake := &fakeResellerStore{resellers: map[string]*model.Reseller{}}
	h := NewResellerAdminHandler(nil)
	h.store = fake
	w, c := resellerReq(t, "GET", "/admin/resellers/nope/commissions", "")
	c.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.ListCommissions(c)
	if w.Code != 404 {
		t.Fatalf("missing reseller status = %d, want 404", w.Code)
	}

	fake = &fakeResellerStore{resellers: map[string]*model.Reseller{"r1": {ID: "r1"}}}
	h.store = fake
	w2, c2 := resellerReq(t, "GET", "/admin/resellers/r1/commissions?status=bogus", "")
	c2.Params = gin.Params{{Key: "id", Value: "r1"}}
	h.ListCommissions(c2)
	if w2.Code != 400 {
		t.Fatalf("bad status filter = %d, want 400", w2.Code)
	}
	if fake.listCommissions != [2]string{} {
		t.Errorf("a refused filter still asked the store for %v", fake.listCommissions)
	}

	fake = &fakeResellerStore{
		resellers:      map[string]*model.Reseller{"r1": {ID: "r1"}},
		commissionList: []*model.Commission{{ID: "com-1", ResellerID: "r1", Status: model.CommissionStatusPaid}},
	}
	h.store = fake
	w3, c3 := resellerReq(t, "GET", "/admin/resellers/r1/commissions?status=paid", "")
	c3.Params = gin.Params{{Key: "id", Value: "r1"}}
	h.ListCommissions(c3)
	if w3.Code != 200 {
		t.Fatalf("list status = %d, want 200; body %s", w3.Code, w3.Body.String())
	}
	if fake.listCommissions != [2]string{"r1", "paid"} {
		t.Errorf("store filter = %v, want [r1 paid]", fake.listCommissions)
	}
}

// ─── Wholesale price overrides ───

// What an override cannot carry: a negative amount, a missing amount,
// a currency whose shape is not three uppercase letters. Shape is all
// that is checkable offline — "USD" passes, "usd" does not.
func TestSetPriceOverrideValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"lowercase currency", `{"unit_amount_minor":100,"currency":"usd"}`},
		{"short currency", `{"unit_amount_minor":100,"currency":"US"}`},
		{"digit currency", `{"unit_amount_minor":100,"currency":"US1"}`},
		{"missing currency", `{"unit_amount_minor":100}`},
		{"negative amount", `{"unit_amount_minor":-1,"currency":"USD"}`},
		{"missing amount", `{"currency":"USD"}`},
		{"garbage body", `not-json`},
	}
	for _, tc := range cases {
		fake := &fakeResellerStore{resellers: map[string]*model.Reseller{"r1": {ID: "r1"}}}
		h := NewResellerAdminHandler(nil)
		h.store = fake

		w, c := resellerReq(t, "PUT", "/admin/resellers/r1/prices/plan-1", tc.body)
		c.Params = gin.Params{{Key: "id", Value: "r1"}, {Key: "plan_id", Value: "plan-1"}}
		h.SetPriceOverride(c)
		if w.Code != 400 {
			t.Errorf("%s: status = %d, want 400; body %s", tc.name, w.Code, w.Body.String())
		}
		if fake.lastPrice != nil {
			t.Errorf("%s: a refused write still reached the store: %+v", tc.name, fake.lastPrice)
		}
	}
}

// A reseller with commission ledger rows is history, not clutter:
// DeleteReseller refuses and the handler says 409 with its own code,
// distinct from the allocations refusal.
func TestDeleteRefusedWhileCommissionsExist(t *testing.T) {
	fake := &fakeResellerStore{deleteErr: store.ErrResellerHasCommissions}
	h := NewResellerAdminHandler(nil)
	h.store = fake

	w, c := resellerReq(t, "DELETE", "/admin/resellers/r1", "")
	c.Params = gin.Params{{Key: "id", Value: "r1"}}
	h.Delete(c)
	if w.Code != 409 {
		t.Fatalf("status = %d, want 409; body %s", w.Code, w.Body.String())
	}
	if code := resellerErrCode(t, w); code != "RESSELLER_HAS_COMMISSIONS" {
		t.Errorf("code = %q, want RESSELLER_HAS_COMMISSIONS", code)
	}
}

// The refusals that are state, not syntax: a plan_id naming nothing is
// 404 (PLAN_NOT_FOUND), a missing reseller 404, and a clean set is 200
// answering the stored row with an audit on the reseller_price entity.
func TestSetPriceOverrideResponses(t *testing.T) {
	fake := &fakeResellerStore{
		resellers: map[string]*model.Reseller{"r1": {ID: "r1"}},
		priceErr:  store.ErrPriceOverridePlanNotFound,
	}
	h := NewResellerAdminHandler(nil)
	h.store = fake
	w, c := resellerReq(t, "PUT", "/admin/resellers/r1/prices/nope", `{"unit_amount_minor":100,"currency":"USD"}`)
	c.Params = gin.Params{{Key: "id", Value: "r1"}, {Key: "plan_id", Value: "nope"}}
	h.SetPriceOverride(c)
	if w.Code != 404 {
		t.Fatalf("unknown plan status = %d, want 404", w.Code)
	}
	if code := resellerErrCode(t, w); code != "PLAN_NOT_FOUND" {
		t.Errorf("code = %q, want PLAN_NOT_FOUND", code)
	}

	fake = &fakeResellerStore{
		resellers: map[string]*model.Reseller{"r1": {ID: "r1"}},
		priceErr:  sql.ErrNoRows,
	}
	h.store = fake
	w2, c2 := resellerReq(t, "PUT", "/admin/resellers/nope/prices/plan-1", `{"unit_amount_minor":100,"currency":"USD"}`)
	c2.Params = gin.Params{{Key: "id", Value: "nope"}, {Key: "plan_id", Value: "plan-1"}}
	h.SetPriceOverride(c2)
	if w2.Code != 404 {
		t.Fatalf("unknown reseller status = %d, want 404", w2.Code)
	}

	fake = &fakeResellerStore{resellers: map[string]*model.Reseller{"r1": {ID: "r1"}}}
	h.store = fake
	w3, c3 := resellerReq(t, "PUT", "/admin/resellers/r1/prices/plan-1", `{"unit_amount_minor":0,"currency":"VND"}`)
	c3.Params = gin.Params{{Key: "id", Value: "r1"}, {Key: "plan_id", Value: "plan-1"}}
	h.SetPriceOverride(c3)
	if w3.Code != 200 {
		t.Fatalf("clean set status = %d, want 200; body %s", w3.Code, w3.Body.String())
	}
	if fake.lastPrice == nil || fake.lastPrice.UnitAmountMinor != 0 || fake.lastPrice.Currency != "VND" {
		t.Errorf("stored override = %+v, want zero VND (zero is a legal wholesale price)", fake.lastPrice)
	}
	if len(fake.audits) != 1 || fake.audits[0].Entity != "reseller_price" || fake.audits[0].Action != "set" {
		t.Errorf("clean set audits = %+v, want one reseller_price 'set'", fake.audits)
	}
}

// Deleting an override: a pair with no override is 404, a clean delete
// is 204 with an audit. The pair scopes the delete — a wrong reseller
// never reaches another partner's row.
func TestDeletePriceOverrideResponses(t *testing.T) {
	fake := &fakeResellerStore{prices: map[[2]string]*model.ResellerPriceOverride{}}
	h := NewResellerAdminHandler(nil)
	h.store = fake
	w, c := resellerReq(t, "DELETE", "/admin/resellers/r1/prices/plan-1", "")
	c.Params = gin.Params{{Key: "id", Value: "r1"}, {Key: "plan_id", Value: "plan-1"}}
	h.DeletePriceOverride(c)
	if w.Code != 404 {
		t.Fatalf("missing override status = %d, want 404", w.Code)
	}
	if code := resellerErrCode(t, w); code != "PRICE_OVERRIDE_NOT_FOUND" {
		t.Errorf("code = %q, want PRICE_OVERRIDE_NOT_FOUND", code)
	}

	fake = &fakeResellerStore{
		prices: map[[2]string]*model.ResellerPriceOverride{
			{"r1", "plan-1"}: {ResellerID: "r1", PlanID: "plan-1"},
		},
	}
	h.store = fake
	w2, c2 := resellerReq(t, "DELETE", "/admin/resellers/r1/prices/plan-1", "")
	c2.Params = gin.Params{{Key: "id", Value: "r1"}, {Key: "plan_id", Value: "plan-1"}}
	h.DeletePriceOverride(c2)
	c2.Writer.WriteHeaderNow() // flush the recorded 204 (see above)
	if w2.Code != 204 {
		t.Fatalf("clean delete status = %d, want 204", w2.Code)
	}
	if fake.lastPriceDelete != [2]string{"r1", "plan-1"} {
		t.Errorf("deleted pair = %v, want [r1 plan-1]", fake.lastPriceDelete)
	}
	if len(fake.audits) != 1 || fake.audits[0].Action != "deleted" {
		t.Errorf("clean delete audits = %+v, want one 'deleted'", fake.audits)
	}
}
