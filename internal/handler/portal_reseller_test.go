package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// fakePortalResellerStore stands in for store.Store so the identity
// mapping and the scoping rules run without a database. The resellers
// are keyed by contact email — the only handle the portal session
// provides — and every list records which reseller id it was asked
// for, so a test can prove the handler never strays from the session's
// own account.
type fakePortalResellerStore struct {
	resellers map[string]*model.Reseller // by folded contact email

	licenses     []*model.License
	licenseCount int
	commissions  []*model.Commission
	commTotals   map[string]int64
	prices       []*model.ResellerPriceOverride
	priceCount   int

	err             error
	lastListLic     string    // reseller id the licence list was asked for
	lastCommissions [2]string // reseller id + status filter
	lastListPrices  string    // reseller id
}

func (f *fakePortalResellerStore) FindResellerByEmail(_ context.Context, email string) (*model.Reseller, error) {
	if f.err != nil {
		return nil, f.err
	}
	if r, ok := f.resellers[model.NormalizeResellerEmail(email)]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakePortalResellerStore) CountResellerLicenses(_ context.Context, resellerID string) (int, error) {
	_ = resellerID
	return f.licenseCount, nil
}

func (f *fakePortalResellerStore) ListResellerLicenses(_ context.Context, resellerID string, _ store.Page) ([]*model.License, int, error) {
	f.lastListLic = resellerID
	return f.licenses, len(f.licenses), nil
}

func (f *fakePortalResellerStore) ListCommissions(_ context.Context, resellerID, status string, _ store.Page) ([]*model.Commission, int, error) {
	f.lastCommissions = [2]string{resellerID, status}
	return f.commissions, len(f.commissions), nil
}

func (f *fakePortalResellerStore) SumCommissionsByStatus(_ context.Context, resellerID string) (map[string]int64, error) {
	_ = resellerID
	return f.commTotals, nil
}

func (f *fakePortalResellerStore) CountResellerPriceOverrides(_ context.Context, resellerID string) (int, error) {
	_ = resellerID
	return f.priceCount, nil
}

func (f *fakePortalResellerStore) ListResellerPriceOverrides(_ context.Context, resellerID string) ([]*model.ResellerPriceOverride, error) {
	f.lastListPrices = resellerID
	return f.prices, nil
}

// portalResellerCtx builds the context a session-authed portal request
// arrives with: middleware.SessionAuth leaves the email claim in the
// context under "email".
func portalResellerCtx(t *testing.T, target, email string, params gin.Params) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	if email != "" {
		c.Set("email", email)
	}
	c.Params = params
	return w, c
}

func portalResellerErrCode(t *testing.T, w *httptest.ResponseRecorder) string {
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

// A session with no email claim is unauthorized; one whose email is
// not a reseller's contact address is a quiet 404 — the endpoints must
// not be an oracle for which addresses are partner accounts.
func TestPortalResellerIdentity(t *testing.T) {
	fake := &fakePortalResellerStore{resellers: map[string]*model.Reseller{}}
	h := NewPortalResellerHandler(nil)
	h.store = fake

	w, c := portalResellerCtx(t, "/portal/reseller/me", "", nil)
	h.Me(c)
	if w.Code != 401 {
		t.Fatalf("no session email status = %d, want 401", w.Code)
	}

	// A signed-in customer who is not a reseller.
	w2, c2 := portalResellerCtx(t, "/portal/reseller/me", "customer@example.com", nil)
	h.Me(c2)
	if w2.Code != 404 {
		t.Fatalf("non-reseller status = %d, want 404", w2.Code)
	}
	if code := portalResellerErrCode(t, w2); code != "NOT_FOUND" {
		t.Errorf("code = %q, want NOT_FOUND (no existence oracle)", code)
	}

	// The same 404 on every endpoint, whatever the path.
	for _, call := range []func(*gin.Context){h.ListLicenses, h.ListCommissions, h.ListPrices} {
		w3, c3 := portalResellerCtx(t, "/portal/reseller/x", "customer@example.com", nil)
		call(c3)
		if w3.Code != 404 {
			t.Errorf("non-reseller endpoint status = %d, want 404", w3.Code)
		}
	}
}

// The identity mapping: the session email that matches a
// resellers.contact_email IS that reseller — folded, so any spelling
// of the address lands on the same account.
func TestPortalResellerIdentityMapping(t *testing.T) {
	fake := &fakePortalResellerStore{
		resellers: map[string]*model.Reseller{
			"partner@example.com": {ID: "r-partner", Name: "Partner", ContactEmail: "partner@example.com"},
		},
	}
	h := NewPortalResellerHandler(nil)
	h.store = fake

	w, c := portalResellerCtx(t, "/portal/reseller/me", "  PARTNER@Example.com ", nil)
	h.Me(c)
	if w.Code != 200 {
		t.Fatalf("reseller session status = %d, want 200; body %s", w.Code, w.Body.String())
	}
}

// Me answers the profile beside the summary numbers, with the
// commission sums zero-filled across the closed status vocabulary so a
// typed client never meets a missing key.
func TestPortalResellerMeSummary(t *testing.T) {
	fake := &fakePortalResellerStore{
		resellers: map[string]*model.Reseller{
			"partner@example.com": {ID: "r1", Name: "Partner", ContactEmail: "partner@example.com", CommissionBPS: 1500},
		},
		licenseCount: 3,
		commTotals:   map[string]int64{model.CommissionStatusPaid: 750},
		priceCount:   2,
	}
	h := NewPortalResellerHandler(nil)
	h.store = fake

	w, c := portalResellerCtx(t, "/portal/reseller/me", "partner@example.com", nil)
	h.Me(c)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Reseller         *model.Reseller  `json:"reseller"`
			LicenseCount     int              `json:"license_count"`
			CommissionTotals map[string]int64 `json:"commission_totals"`
			PriceOverrideCnt int              `json:"price_override_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad body: %v", err)
	}
	if body.Data.Reseller == nil || body.Data.Reseller.ID != "r1" {
		t.Fatalf("reseller = %+v, want r1", body.Data.Reseller)
	}
	if body.Data.LicenseCount != 3 || body.Data.PriceOverrideCnt != 2 {
		t.Errorf("counts = %d licences / %d prices, want 3 / 2", body.Data.LicenseCount, body.Data.PriceOverrideCnt)
	}
	for _, status := range model.CommissionStatuses {
		if _, ok := body.Data.CommissionTotals[status]; !ok {
			t.Errorf("commission_totals missing key %q: %v", status, body.Data.CommissionTotals)
		}
	}
	if body.Data.CommissionTotals[model.CommissionStatusPaid] != 750 {
		t.Errorf("paid total = %d, want 750", body.Data.CommissionTotals[model.CommissionStatusPaid])
	}
	if body.Data.CommissionTotals[model.CommissionStatusAccrued] != 0 {
		t.Errorf("accrued total = %d, want the zero fill", body.Data.CommissionTotals[model.CommissionStatusAccrued])
	}
}

// Every list is scoped to the session's reseller: the id the store
// sees comes from the session email's account, never from the request.
// With two partners in the fake, session A can only ever ask about A.
func TestPortalResellerListsAreScoped(t *testing.T) {
	fake := &fakePortalResellerStore{
		resellers: map[string]*model.Reseller{
			"a@example.com": {ID: "r-a", ContactEmail: "a@example.com"},
			"b@example.com": {ID: "r-b", ContactEmail: "b@example.com"},
		},
		licenses:    []*model.License{{ID: "L1"}},
		commissions: []*model.Commission{{ID: "com-1", ResellerID: "r-a"}},
		prices:      []*model.ResellerPriceOverride{{ResellerID: "r-a", PlanID: "p1"}},
	}
	h := NewPortalResellerHandler(nil)
	h.store = fake

	w, c := portalResellerCtx(t, "/portal/reseller/licenses", "a@example.com", nil)
	h.ListLicenses(c)
	if w.Code != 200 {
		t.Fatalf("licenses status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if fake.lastListLic != "r-a" {
		t.Errorf("licenses asked for %q, want the session's r-a", fake.lastListLic)
	}

	w2, c2 := portalResellerCtx(t, "/portal/reseller/commissions?status=paid", "a@example.com", nil)
	h.ListCommissions(c2)
	if w2.Code != 200 {
		t.Fatalf("commissions status = %d, want 200; body %s", w2.Code, w2.Body.String())
	}
	if fake.lastCommissions != [2]string{"r-a", "paid"} {
		t.Errorf("commissions asked for %v, want [r-a paid]", fake.lastCommissions)
	}

	w3, c3 := portalResellerCtx(t, "/portal/reseller/prices", "a@example.com", nil)
	h.ListPrices(c3)
	if w3.Code != 200 {
		t.Fatalf("prices status = %d, want 200; body %s", w3.Code, w3.Body.String())
	}
	if fake.lastListPrices != "r-a" {
		t.Errorf("prices asked for %q, want the session's r-a", fake.lastListPrices)
	}

	// The list envelope holds the rows under their own name.
	var body struct {
		Data struct {
			Commissions []model.Commission `json:"commissions"`
			Total       int                `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad list body: %v", err)
	}
	if body.Data.Total != 1 || len(body.Data.Commissions) != 1 {
		t.Errorf("commissions list = %d (total %d), want 1", len(body.Data.Commissions), body.Data.Total)
	}
}

// The commission status filter is the closed vocabulary, refused
// rather than silently served an empty page — and refused before the
// store is asked anything.
func TestPortalResellerCommissionStatusFilter(t *testing.T) {
	fake := &fakePortalResellerStore{
		resellers: map[string]*model.Reseller{
			"partner@example.com": {ID: "r1", ContactEmail: "partner@example.com"},
		},
	}
	h := NewPortalResellerHandler(nil)
	h.store = fake

	w, c := portalResellerCtx(t, "/portal/reseller/commissions?status=bogus", "partner@example.com", nil)
	h.ListCommissions(c)
	if w.Code != 400 {
		t.Fatalf("bad status filter = %d, want 400", w.Code)
	}
	if fake.lastCommissions != [2]string{} {
		t.Errorf("a refused filter still asked the store for %v", fake.lastCommissions)
	}

	// Every accepted value passes through.
	for _, status := range model.CommissionStatuses {
		w2, c2 := portalResellerCtx(t, "/portal/reseller/commissions?status="+status, "partner@example.com", nil)
		h.ListCommissions(c2)
		if w2.Code != 200 {
			t.Errorf("status %q: %d, want 200", status, w2.Code)
		}
	}
}
