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

// fakeAffiliateAdminStore stands in for store.Store so the refusal
// paths — duplicate email, delete with money records, the conversion
// and payout state machines — run without a database. It records what
// the handler wrote and audited.
type fakeAffiliateAdminStore struct {
	affiliates  map[string]*model.Affiliate
	list        []*model.Affiliate
	codes       map[string]*model.ReferralCode
	codeList    map[string][]*model.ReferralCode
	conversions map[string]*model.AffiliateConversion
	convList    []*model.AffiliateConversion
	payouts     map[string]*model.AffiliatePayout
	payoutList  []*model.AffiliatePayout

	createErr       error
	updateErr       error
	deleteErr       error
	createCodeErr   error
	updateCodeErr   error
	deleteCodeErr   error
	setConvErr      error
	createPayoutErr error
	markPaidErr     error
	markFailedErr   error
	accrued         int64
	accruedErr      error

	lastCreateAff  *model.Affiliate
	lastUpdateAff  *model.Affiliate
	lastCreateCode *model.ReferralCode
	lastUpdateCode *model.ReferralCode
	lastDeleteCode [2]string
	lastSetConv    [2]string
	lastPayout     *model.AffiliatePayout
	lastMarkPaidID string
	lastFailedID   string
	lastFailedNote string
	audits         []*model.AuditLog
}

func (f *fakeAffiliateAdminStore) ListAffiliates(_ context.Context, _, _ string, _ store.Page) ([]*model.Affiliate, int, error) {
	return f.list, len(f.list), nil
}

func (f *fakeAffiliateAdminStore) FindAffiliateByID(_ context.Context, id string) (*model.Affiliate, error) {
	if a, ok := f.affiliates[id]; ok {
		// A copy, like a real row read: the handler merges a patch into
		// what it read, and a shared pointer would make a refused patch
		// mutate the fixture behind the test's back.
		cp := *a
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeAffiliateAdminStore) CreateAffiliate(_ context.Context, a *model.Affiliate) error {
	if f.createErr != nil {
		return f.createErr
	}
	a.ID = "aff-new"
	f.lastCreateAff = a
	return nil
}

func (f *fakeAffiliateAdminStore) UpdateAffiliate(_ context.Context, a *model.Affiliate) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.lastUpdateAff = a
	return nil
}

func (f *fakeAffiliateAdminStore) DeleteAffiliate(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.affiliates[id]; !ok {
		return sql.ErrNoRows
	}
	delete(f.affiliates, id)
	return nil
}

func (f *fakeAffiliateAdminStore) CreateReferralCode(_ context.Context, rc *model.ReferralCode) error {
	if f.createCodeErr != nil {
		return f.createCodeErr
	}
	rc.ID = "code-new"
	f.lastCreateCode = rc
	return nil
}

func (f *fakeAffiliateAdminStore) FindReferralCodeByID(_ context.Context, id string) (*model.ReferralCode, error) {
	if rc, ok := f.codes[id]; ok {
		cp := *rc
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeAffiliateAdminStore) ListReferralCodes(_ context.Context, affiliateID string, _ store.Page) ([]*model.ReferralCode, int, error) {
	rows := f.codeList[affiliateID]
	return rows, len(rows), nil
}

func (f *fakeAffiliateAdminStore) UpdateReferralCode(_ context.Context, rc *model.ReferralCode) error {
	if f.updateCodeErr != nil {
		return f.updateCodeErr
	}
	f.lastUpdateCode = rc
	return nil
}

func (f *fakeAffiliateAdminStore) DeleteReferralCode(_ context.Context, affiliateID, codeID string) error {
	if f.deleteCodeErr != nil {
		return f.deleteCodeErr
	}
	f.lastDeleteCode = [2]string{affiliateID, codeID}
	if _, ok := f.codes[codeID]; !ok {
		return sql.ErrNoRows
	}
	delete(f.codes, codeID)
	return nil
}

func (f *fakeAffiliateAdminStore) ListConversions(_ context.Context, _, _ string, _ store.Page) ([]*model.AffiliateConversion, int, error) {
	return f.convList, len(f.convList), nil
}

func (f *fakeAffiliateAdminStore) SetConversionStatus(_ context.Context, id, status string) (*model.AffiliateConversion, error) {
	if f.setConvErr != nil {
		return nil, f.setConvErr
	}
	f.lastSetConv = [2]string{id, status}
	if conv, ok := f.conversions[id]; ok {
		conv.Status = status
		cp := *conv
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeAffiliateAdminStore) SumPendingCommissions(_ context.Context, _ string) (int64, error) {
	if f.accruedErr != nil {
		return 0, f.accruedErr
	}
	return f.accrued, nil
}

func (f *fakeAffiliateAdminStore) CreatePayout(_ context.Context, pay *model.AffiliatePayout) error {
	if f.createPayoutErr != nil {
		return f.createPayoutErr
	}
	pay.ID = "payout-new"
	f.lastPayout = pay
	return nil
}

func (f *fakeAffiliateAdminStore) MarkPayoutPaid(_ context.Context, id string) (*model.AffiliatePayout, error) {
	if f.markPaidErr != nil {
		return nil, f.markPaidErr
	}
	f.lastMarkPaidID = id
	if pay, ok := f.payouts[id]; ok {
		pay.Status = model.AffiliatePayoutStatusPaid
		cp := *pay
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeAffiliateAdminStore) MarkPayoutFailed(_ context.Context, id, notes string) (*model.AffiliatePayout, error) {
	if f.markFailedErr != nil {
		return nil, f.markFailedErr
	}
	f.lastFailedID, f.lastFailedNote = id, notes
	if pay, ok := f.payouts[id]; ok {
		pay.Status = model.AffiliatePayoutStatusFailed
		cp := *pay
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeAffiliateAdminStore) ListAffiliatePayouts(_ context.Context, _ string, _ store.Page) ([]*model.AffiliatePayout, int, error) {
	return f.payoutList, len(f.payoutList), nil
}

func (f *fakeAffiliateAdminStore) Audit(_ context.Context, log *model.AuditLog) {
	f.audits = append(f.audits, log)
}

func affiliateReq(t *testing.T, method, target, body string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return w, c
}

func affiliateErrCode(t *testing.T, w *httptest.ResponseRecorder) string {
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
// trimmed name, folded email, status falling back to active, model
// falling back to percent, both commission numbers kept whatever the
// model.
func TestPrepareAffiliateNormalizesAndDefaults(t *testing.T) {
	a := &model.Affiliate{Name: "  Acme  ", ContactEmail: "  Partner@Example.COM  ", Notes: "  hello  ", PayoutMethod: " PayPal "}
	if err := prepareAffiliate(a); err != nil {
		t.Fatalf("prepareAffiliate: unexpected err %v", err)
	}
	if a.Name != "Acme" {
		t.Errorf("Name = %q, want %q", a.Name, "Acme")
	}
	if a.ContactEmail != "partner@example.com" {
		t.Errorf("ContactEmail = %q, want %q", a.ContactEmail, "partner@example.com")
	}
	if a.Status != model.AffiliateStatusActive {
		t.Errorf("Status = %q, want %q", a.Status, model.AffiliateStatusActive)
	}
	if a.CommissionModel != model.AffiliateCommissionModelPercent {
		t.Errorf("CommissionModel = %q, want %q", a.CommissionModel, model.AffiliateCommissionModelPercent)
	}
	if a.Notes != "hello" || a.PayoutMethod != "PayPal" {
		t.Errorf("Notes/PayoutMethod = %q/%q, want trimmed", a.Notes, a.PayoutMethod)
	}

	fixed := &model.Affiliate{Name: "Flat", ContactEmail: "flat@example.com",
		CommissionModel: model.AffiliateCommissionModelFixed, CommissionMinor: 50000}
	if err := prepareAffiliate(fixed); err != nil {
		t.Fatalf("fixed affiliate: unexpected err %v", err)
	}
	if fixed.CommissionMinor != 50000 {
		t.Errorf("CommissionMinor = %d, want 50000", fixed.CommissionMinor)
	}
}

// What the affiliate refuses: no name, an unusable email, a status or
// model outside the vocabulary, a rate outside 0–10000 bps, a negative
// fixed amount. Every one of these is a 400 the admin can fix, not a
// 500.
func TestPrepareAffiliateRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		aff  model.Affiliate
	}{
		{"empty name", model.Affiliate{ContactEmail: "a@b.co"}},
		{"blank name", model.Affiliate{Name: "   ", ContactEmail: "a@b.co"}},
		{"missing email", model.Affiliate{Name: "Acme"}},
		{"bad email", model.Affiliate{Name: "Acme", ContactEmail: "not-an-address"}},
		{"bad status", model.Affiliate{Name: "Acme", ContactEmail: "a@b.co", Status: "banned"}},
		{"bad model", model.Affiliate{Name: "Acme", ContactEmail: "a@b.co", CommissionModel: "recurring"}},
		{"negative bps", model.Affiliate{Name: "Acme", ContactEmail: "a@b.co", CommissionBPS: -1}},
		{"bps over 10000", model.Affiliate{Name: "Acme", ContactEmail: "a@b.co", CommissionBPS: 10001}},
		{"negative fixed", model.Affiliate{Name: "Acme", ContactEmail: "a@b.co", CommissionMinor: -1}},
	}
	for _, tc := range cases {
		aff := tc.aff
		if err := prepareAffiliate(&aff); err == nil {
			t.Errorf("%s: accepted, want refusal", tc.name)
		} else if err.Status != 400 {
			t.Errorf("%s: status %d, want 400", tc.name, err.Status)
		}
	}
}

// The code handle folds to uppercase alphanumerics, and the landing
// URL must be a full http(s) URL — the open-redirect defence starts at
// write time, because the redirect endpoint navigates only to this
// stored value.
func TestPrepareReferralCode(t *testing.T) {
	rc := &model.ReferralCode{Code: "  Summer-Sale!  ", LandingURL: "  https://example.com/promo  "}
	if err := prepareReferralCode(rc); err != nil {
		t.Fatalf("prepareReferralCode: unexpected err %v", err)
	}
	if rc.Code != "SUMMERSALE" {
		t.Errorf("Code = %q, want %q", rc.Code, "SUMMERSALE")
	}
	if rc.LandingURL != "https://example.com/promo" {
		t.Errorf("LandingURL = %q, want trimmed", rc.LandingURL)
	}

	// No landing URL is fine — the redirect falls back to the platform
	// default.
	if err := prepareReferralCode(&model.ReferralCode{Code: "SAVE20"}); err != nil {
		t.Errorf("empty landing_url refused: %v", err)
	}

	bad := []model.ReferralCode{
		{Code: "ABC"},                 // too short
		{Code: "!!!", LandingURL: ""}, // unfillable handle
		{Code: "SAVE20", LandingURL: "javascript:alert(1)"}, // not http(s)
		{Code: "SAVE20", LandingURL: "//evil.example.com"},  // protocol-relative
		{Code: "SAVE20", LandingURL: "/local/path"},         // not a full URL
		{Code: "SAVE20", LandingURL: "data:text/html,hi"},   // not http(s)
	}
	for _, rc := range bad {
		if err := prepareReferralCode(&rc); err == nil {
			t.Errorf("code %+v accepted, want refusal", rc)
		} else if err.Status != 400 {
			t.Errorf("code %+v: status %d, want 400", rc, err.Status)
		}
	}
}

// The contact email is the unique handle; a duplicate is 409 the admin
// can act on, anything else is 500, and every write is audited.
func TestAffiliateCreateResponses(t *testing.T) {
	fake := &fakeAffiliateAdminStore{createErr: store.ErrAffiliateEmailTaken}
	h := NewAffiliateAdminHandler(nil)
	h.store = fake

	w, c := affiliateReq(t, "POST", "/admin/affiliates", `{"name":"Acme","contact_email":"acme@example.com"}`)
	h.Create(c)
	if w.Code != 409 {
		t.Fatalf("status = %d, want 409; body %s", w.Code, w.Body.String())
	}
	if code := affiliateErrCode(t, w); code != "DUPLICATE" {
		t.Errorf("code = %q, want DUPLICATE", code)
	}

	fake.createErr = errors.New("boom")
	w, c = affiliateReq(t, "POST", "/admin/affiliates", `{"name":"Acme","contact_email":"acme@example.com"}`)
	h.Create(c)
	if w.Code != 500 {
		t.Errorf("other failure status = %d, want 500", w.Code)
	}

	// Missing required fields are 400 before any store call.
	fake.createErr = nil
	w, c = affiliateReq(t, "POST", "/admin/affiliates", `{"name":"Acme"}`)
	h.Create(c)
	if w.Code != 400 {
		t.Errorf("missing email status = %d, want 400", w.Code)
	}

	// Happy path: 201, folded row stored, audited as created.
	w, c = affiliateReq(t, "POST", "/admin/affiliates",
		`{"name":"Acme","contact_email":"Acme@Example.com","commission_bps":1250}`)
	h.Create(c)
	if w.Code != 201 {
		t.Fatalf("status = %d, want 201; body %s", w.Code, w.Body.String())
	}
	if fake.lastCreateAff == nil || fake.lastCreateAff.ContactEmail != "acme@example.com" {
		t.Errorf("stored row = %+v, want folded email", fake.lastCreateAff)
	}
	if fake.lastCreateAff.CommissionBPS != 1250 {
		t.Errorf("stored bps = %d, want 1250", fake.lastCreateAff.CommissionBPS)
	}
	if len(fake.audits) != 1 || fake.audits[0].Entity != "affiliate" || fake.audits[0].Action != "created" {
		t.Errorf("audits = %+v, want one affiliate/created", fake.audits)
	}
}

// Update: 404 on a missing row, the same validation as create on the
// merged row, 409 on a rename onto a taken email, audit on success.
func TestAffiliateUpdateResponses(t *testing.T) {
	fake := &fakeAffiliateAdminStore{affiliates: map[string]*model.Affiliate{
		"a1": {ID: "a1", Name: "Acme", ContactEmail: "acme@example.com", Status: model.AffiliateStatusActive},
	}}
	h := NewAffiliateAdminHandler(nil)
	h.store = fake

	w, c := affiliateReq(t, "PATCH", "/admin/a1", `{"name":"Renamed"}`)
	c.Params = gin.Params{{Key: "id", Value: "missing"}}
	h.Update(c)
	if w.Code != 404 {
		t.Errorf("missing status = %d, want 404", w.Code)
	}
	if code := affiliateErrCode(t, w); code != "AFFILIATE_NOT_FOUND" {
		t.Errorf("code = %q, want AFFILIATE_NOT_FOUND", code)
	}

	w, c = affiliateReq(t, "PATCH", "/admin/a1", `{"commission_bps":10001}`)
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	h.Update(c)
	if w.Code != 400 {
		t.Errorf("invalid patch status = %d, want 400", w.Code)
	}

	fake.updateErr = store.ErrAffiliateEmailTaken
	w, c = affiliateReq(t, "PATCH", "/admin/a1", `{"contact_email":"taken@example.com"}`)
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	h.Update(c)
	if w.Code != 409 || affiliateErrCode(t, w) != "DUPLICATE" {
		t.Errorf("rename-onto-taken = %d %s, want 409 DUPLICATE", w.Code, affiliateErrCode(t, w))
	}

	fake.updateErr = nil
	w, c = affiliateReq(t, "PATCH", "/admin/a1", `{"name":"Renamed","status":"suspended"}`)
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	h.Update(c)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if fake.lastUpdateAff.Name != "Renamed" || fake.lastUpdateAff.Status != model.AffiliateStatusSuspended {
		t.Errorf("merged row = %+v", fake.lastUpdateAff)
	}
	if len(fake.audits) != 1 || fake.audits[0].Action != "updated" {
		t.Errorf("audits = %+v, want one updated", fake.audits)
	}
}

// The delete policy as HTTP: money records refuse with their own 409
// codes (suspend instead), a missing row is 404, the happy path is 204
// and audited.
func TestAffiliateDeleteResponses(t *testing.T) {
	h := NewAffiliateAdminHandler(nil)

	for _, tc := range []struct {
		err  error
		code string
	}{
		{store.ErrAffiliateHasConversions, "AFFILIATE_HAS_CONVERSIONS"},
		{store.ErrAffiliateHasPayouts, "AFFILIATE_HAS_PAYOUTS"},
	} {
		fake := &fakeAffiliateAdminStore{deleteErr: tc.err, affiliates: map[string]*model.Affiliate{"a1": {ID: "a1"}}}
		h.store = fake
		w, c := affiliateReq(t, "DELETE", "/admin/affiliates/a1", "")
		c.Params = gin.Params{{Key: "id", Value: "a1"}}
		h.Delete(c)
		if w.Code != 409 {
			t.Errorf("%s: status = %d, want 409", tc.err, w.Code)
		}
		if code := affiliateErrCode(t, w); code != tc.code {
			t.Errorf("%s: code = %q, want %q", tc.err, code, tc.code)
		}
	}

	fake := &fakeAffiliateAdminStore{deleteErr: sql.ErrNoRows}
	h.store = fake
	w, c := affiliateReq(t, "DELETE", "/admin/affiliates/nope", "")
	c.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.Delete(c)
	if w.Code != 404 {
		t.Errorf("missing delete status = %d, want 404", w.Code)
	}

	fake = &fakeAffiliateAdminStore{affiliates: map[string]*model.Affiliate{"a1": {ID: "a1"}}}
	h.store = fake
	w, c = affiliateReq(t, "DELETE", "/admin/affiliates/a1", "")
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	h.Delete(c)
	c.Writer.WriteHeaderNow() // flush the recorded 204 (NoContent writes no body)
	if w.Code != 204 {
		t.Errorf("delete status = %d, want 204", w.Code)
	}
	if len(fake.audits) != 1 || fake.audits[0].Action != "deleted" {
		t.Errorf("audits = %+v, want one deleted", fake.audits)
	}
}

// Code management: the handle folds, bad handles and non-http(s)
// landing URLs are 400 (never stored — so never redirected to), a
// duplicate handle is 409, deactivation is PATCH, and a code that
// earned conversions is never deleted (409, deactivate instead).
func TestAffiliateCodeManagement(t *testing.T) {
	fake := &fakeAffiliateAdminStore{
		affiliates: map[string]*model.Affiliate{"a1": {ID: "a1"}},
		codes:      map[string]*model.ReferralCode{"c1": {ID: "c1", AffiliateID: "a1", Code: "SAVE20", Active: true}},
	}
	h := NewAffiliateAdminHandler(nil)
	h.store = fake

	// Unknown affiliate is 404 before anything is written.
	w, c := affiliateReq(t, "POST", "/admin/affiliates/x/codes", `{"code":"SAVE20"}`)
	c.Params = gin.Params{{Key: "id", Value: "x"}}
	h.CreateCode(c)
	if w.Code != 404 {
		t.Errorf("unknown affiliate status = %d, want 404", w.Code)
	}

	// A landing_url that is not a full http(s) URL is refused 400 —
	// the open-redirect defence at write time.
	w, c = affiliateReq(t, "POST", "/admin/affiliates/a1/codes",
		`{"code":"EVIL01","landing_url":"javascript:alert(1)"}`)
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	h.CreateCode(c)
	if w.Code != 400 {
		t.Errorf("javascript landing status = %d, want 400", w.Code)
	}

	// Bad handle is 400.
	w, c = affiliateReq(t, "POST", "/admin/affiliates/a1/codes", `{"code":"!!"}`)
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	h.CreateCode(c)
	if w.Code != 400 {
		t.Errorf("bad code status = %d, want 400", w.Code)
	}

	// Duplicate handle is 409 DUPLICATE.
	fake.createCodeErr = store.ErrReferralCodeTaken
	w, c = affiliateReq(t, "POST", "/admin/affiliates/a1/codes", `{"code":"SAVE20"}`)
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	h.CreateCode(c)
	if w.Code != 409 || affiliateErrCode(t, w) != "DUPLICATE" {
		t.Errorf("duplicate code = %d %s, want 409 DUPLICATE", w.Code, affiliateErrCode(t, w))
	}

	// Happy path: folded handle, active on by default, audited.
	fake.createCodeErr = nil
	w, c = affiliateReq(t, "POST", "/admin/affiliates/a1/codes",
		`{"code":"summer-sale!","landing_url":"https://example.com/promo"}`)
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	h.CreateCode(c)
	if w.Code != 201 {
		t.Fatalf("create code status = %d, want 201; body %s", w.Code, w.Body.String())
	}
	if fake.lastCreateCode.Code != "SUMMERSALE" {
		t.Errorf("stored code = %q, want SUMMERSALE", fake.lastCreateCode.Code)
	}
	if !fake.lastCreateCode.Active {
		t.Error("a fresh code must default to active")
	}
	if len(fake.audits) != 1 || fake.audits[0].Action != "code_created" {
		t.Errorf("audits = %+v, want one code_created", fake.audits)
	}

	// PATCH deactivates without deleting history; the route's
	// affiliate must own the code.
	w, c = affiliateReq(t, "PATCH", "/admin/affiliates/a1/codes/c1", `{"active":false}`)
	c.Params = gin.Params{{Key: "id", Value: "a1"}, {Key: "code_id", Value: "c1"}}
	h.UpdateCode(c)
	if w.Code != 200 {
		t.Fatalf("update code status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if fake.lastUpdateCode.Active {
		t.Error("active=false was not written")
	}

	w, c = affiliateReq(t, "PATCH", "/admin/affiliates/other/codes/c1", `{"active":false}`)
	c.Params = gin.Params{{Key: "id", Value: "other"}, {Key: "code_id", Value: "c1"}}
	h.UpdateCode(c)
	if w.Code != 404 {
		t.Errorf("cross-affiliate update status = %d, want 404", w.Code)
	}

	// A code that earned conversions refuses deletion (409) …
	fake.deleteCodeErr = store.ErrReferralCodeHasConversions
	w, c = affiliateReq(t, "DELETE", "/admin/affiliates/a1/codes/c1", "")
	c.Params = gin.Params{{Key: "id", Value: "a1"}, {Key: "code_id", Value: "c1"}}
	h.DeleteCode(c)
	if w.Code != 409 || affiliateErrCode(t, w) != "REFERRAL_CODE_HAS_CONVERSIONS" {
		t.Errorf("earned code delete = %d %s, want 409 REFERRAL_CODE_HAS_CONVERSIONS", w.Code, affiliateErrCode(t, w))
	}

	// …and one that did not deletes (204), audited.
	fake.deleteCodeErr = nil
	w, c = affiliateReq(t, "DELETE", "/admin/affiliates/a1/codes/c1", "")
	c.Params = gin.Params{{Key: "id", Value: "a1"}, {Key: "code_id", Value: "c1"}}
	h.DeleteCode(c)
	c.Writer.WriteHeaderNow() // flush the recorded 204 (NoContent writes no body)
	if w.Code != 204 {
		t.Errorf("delete code status = %d, want 204", w.Code)
	}
	if fake.lastDeleteCode != [2]string{"a1", "c1"} {
		t.Errorf("delete args = %v, want (a1, c1)", fake.lastDeleteCode)
	}
}

// The three review decisions map to the three legal moves; the state
// machine's refusals are their own 409 codes; everything is audited as
// affiliate_conversion.
func TestConversionReviewResponses(t *testing.T) {
	fake := &fakeAffiliateAdminStore{conversions: map[string]*model.AffiliateConversion{
		"cv1": {ID: "cv1", Status: model.AffiliateConversionStatusPending},
	}}
	h := NewAffiliateAdminHandler(nil)
	h.store = fake

	for _, tc := range []struct {
		action string
		want   string
	}{
		{"approve", model.AffiliateConversionStatusApproved},
		{"reject", model.AffiliateConversionStatusRejected},
		{"reverse", model.AffiliateConversionStatusReversed},
	} {
		fake.lastSetConv = [2]string{}
		w, c := affiliateReq(t, "POST", "/admin/conversions/cv1/"+tc.action, "")
		c.Params = gin.Params{{Key: "id", Value: "cv1"}}
		switch tc.action {
		case "approve":
			h.Approve(c)
		case "reject":
			h.Reject(c)
		case "reverse":
			h.Reverse(c)
		}
		if w.Code != 200 {
			t.Fatalf("%s: status = %d, want 200; body %s", tc.action, w.Code, w.Body.String())
		}
		if fake.lastSetConv != [2]string{"cv1", tc.want} {
			t.Errorf("%s: SetConversionStatus(%v), want target %q", tc.action, fake.lastSetConv, tc.want)
		}
	}
	if len(fake.audits) != 3 {
		t.Errorf("audits = %+v, want three", fake.audits)
	}
	for _, log := range fake.audits {
		if log.Entity != "affiliate_conversion" {
			t.Errorf("audit entity = %q, want affiliate_conversion", log.Entity)
		}
	}

	// A move the state machine refuses is 409 CONVERSION_TRANSITION_INVALID.
	fake.setConvErr = store.ErrConversionInvalidTransition
	w, c := affiliateReq(t, "POST", "/admin/conversions/cv1/approve", "")
	c.Params = gin.Params{{Key: "id", Value: "cv1"}}
	h.Approve(c)
	if w.Code != 409 || affiliateErrCode(t, w) != "CONVERSION_TRANSITION_INVALID" {
		t.Errorf("invalid transition = %d %s, want 409 CONVERSION_TRANSITION_INVALID", w.Code, affiliateErrCode(t, w))
	}

	// Frozen by a pending payout is its own 409.
	fake.setConvErr = store.ErrConversionInPayout
	w, c = affiliateReq(t, "POST", "/admin/conversions/cv1/reject", "")
	c.Params = gin.Params{{Key: "id", Value: "cv1"}}
	h.Reject(c)
	if w.Code != 409 || affiliateErrCode(t, w) != "CONVERSION_IN_PAYOUT" {
		t.Errorf("frozen = %d %s, want 409 CONVERSION_IN_PAYOUT", w.Code, affiliateErrCode(t, w))
	}

	// Missing conversion is 404.
	fake.setConvErr = sql.ErrNoRows
	w, c = affiliateReq(t, "POST", "/admin/conversions/nope/approve", "")
	c.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.Approve(c)
	if w.Code != 404 || affiliateErrCode(t, w) != "CONVERSION_NOT_FOUND" {
		t.Errorf("missing = %d %s, want 404 CONVERSION_NOT_FOUND", w.Code, affiliateErrCode(t, w))
	}
}

// Payout creation: the amount is checked against the accrued balance
// before anything is written, nothing accrued is 409, and the settled
// row is what gets audited and returned.
func TestAffiliatePayoutCreateResponses(t *testing.T) {
	fake := &fakeAffiliateAdminStore{
		affiliates: map[string]*model.Affiliate{"a1": {ID: "a1"}},
		accrued:    5000,
	}
	h := NewAffiliateAdminHandler(nil)
	h.store = fake

	w, c := affiliateReq(t, "POST", "/admin/affiliates/a1/payouts", `{"amount_minor":-1}`)
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	h.CreatePayout(c)
	if w.Code != 400 {
		t.Errorf("negative amount status = %d, want 400", w.Code)
	}

	w, c = affiliateReq(t, "POST", "/admin/affiliates/a1/payouts", `{"amount_minor":5001}`)
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	h.CreatePayout(c)
	if w.Code != 400 {
		t.Errorf("over-accrued status = %d, want 400", w.Code)
	}
	if fake.lastPayout != nil {
		t.Error("a refused payout still reached the store")
	}

	fake.createPayoutErr = store.ErrPayoutNothingToPay
	w, c = affiliateReq(t, "POST", "/admin/affiliates/a1/payouts", `{"amount_minor":100}`)
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	h.CreatePayout(c)
	if w.Code != 409 || affiliateErrCode(t, w) != "PAYOUT_NOTHING_TO_PAY" {
		t.Errorf("nothing to pay = %d %s, want 409 PAYOUT_NOTHING_TO_PAY", w.Code, affiliateErrCode(t, w))
	}

	fake.createPayoutErr = nil
	w, c = affiliateReq(t, "POST", "/admin/affiliates/a1/payouts", `{"amount_minor":4000,"notes":"october"}`)
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	h.CreatePayout(c)
	if w.Code != 201 {
		t.Fatalf("status = %d, want 201; body %s", w.Code, w.Body.String())
	}
	if fake.lastPayout.AmountMinor != 4000 || fake.lastPayout.Notes != "october" {
		t.Errorf("payout = %+v, want 4000/october", fake.lastPayout)
	}
	if len(fake.audits) != 1 || fake.audits[0].Entity != "affiliate_payout" || fake.audits[0].Action != "created" {
		t.Errorf("audits = %+v, want one affiliate_payout/created", fake.audits)
	}
}

// Payout settlement: paid stamps and audits, failed carries its notes,
// the state machine's refusal is 409, a missing payout is 404.
func TestAffiliatePayoutSettleResponses(t *testing.T) {
	fake := &fakeAffiliateAdminStore{payouts: map[string]*model.AffiliatePayout{
		"p1": {ID: "p1", Status: model.AffiliatePayoutStatusRequested},
	}}
	h := NewAffiliateAdminHandler(nil)
	h.store = fake

	w, c := affiliateReq(t, "POST", "/admin/payouts/p1/paid", "")
	c.Params = gin.Params{{Key: "id", Value: "p1"}}
	h.MarkPaid(c)
	if w.Code != 200 {
		t.Fatalf("paid status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if fake.lastMarkPaidID != "p1" {
		t.Errorf("MarkPayoutPaid(%q), want p1", fake.lastMarkPaidID)
	}
	if len(fake.audits) != 1 || fake.audits[0].Action != "paid" {
		t.Errorf("audits = %+v, want one paid", fake.audits)
	}

	w, c = affiliateReq(t, "POST", "/admin/payouts/p1/failed", `{"notes":"transfer bounced"}`)
	c.Params = gin.Params{{Key: "id", Value: "p1"}}
	h.MarkFailed(c)
	if w.Code != 200 {
		t.Fatalf("failed status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if fake.lastFailedID != "p1" || fake.lastFailedNote != "transfer bounced" {
		t.Errorf("MarkPayoutFailed(%q, %q), want the notes through", fake.lastFailedID, fake.lastFailedNote)
	}

	fake.markPaidErr = store.ErrPayoutInvalidTransition
	w, c = affiliateReq(t, "POST", "/admin/payouts/p1/paid", "")
	c.Params = gin.Params{{Key: "id", Value: "p1"}}
	h.MarkPaid(c)
	if w.Code != 409 || affiliateErrCode(t, w) != "PAYOUT_TRANSITION_INVALID" {
		t.Errorf("re-pay = %d %s, want 409 PAYOUT_TRANSITION_INVALID", w.Code, affiliateErrCode(t, w))
	}

	fake.markPaidErr = sql.ErrNoRows
	w, c = affiliateReq(t, "POST", "/admin/payouts/nope/paid", "")
	c.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.MarkPaid(c)
	if w.Code != 404 || affiliateErrCode(t, w) != "PAYOUT_NOT_FOUND" {
		t.Errorf("missing = %d %s, want 404 PAYOUT_NOT_FOUND", w.Code, affiliateErrCode(t, w))
	}
}

// The detail view reports the accrued balance beside the account, so
// the payout form knows what a payout would settle.
func TestAffiliateGetIncludesAccrued(t *testing.T) {
	fake := &fakeAffiliateAdminStore{
		affiliates: map[string]*model.Affiliate{"a1": {ID: "a1", Name: "Acme"}},
		accrued:    4200,
	}
	h := NewAffiliateAdminHandler(nil)
	h.store = fake

	w, c := affiliateReq(t, "GET", "/admin/affiliates/a1", "")
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	h.Get(c)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			PendingCommissionMinor int64 `json:"pending_commission_minor"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v; %s", err, w.Body.String())
	}
	if body.Data.PendingCommissionMinor != 4200 {
		t.Errorf("pending_commission_minor = %d, want 4200", body.Data.PendingCommissionMinor)
	}
}

// A status filter outside the vocabulary is refused 400 rather than
// silently returning nothing — on both the account and the conversion
// lists.
func TestAffiliateListStatusFilters(t *testing.T) {
	fake := &fakeAffiliateAdminStore{affiliates: map[string]*model.Affiliate{"a1": {ID: "a1"}}}
	h := NewAffiliateAdminHandler(nil)
	h.store = fake

	w, c := affiliateReq(t, "GET", "/admin/affiliates?status=banned", "")
	h.List(c)
	if w.Code != 400 {
		t.Errorf("bad affiliate status filter = %d, want 400", w.Code)
	}

	w, c = affiliateReq(t, "GET", "/admin/affiliates/a1/conversions?status=settled", "")
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	h.ListConversions(c)
	if w.Code != 400 {
		t.Errorf("bad conversion status filter = %d, want 400", w.Code)
	}

	w, c = affiliateReq(t, "GET", "/admin/affiliates/a1/conversions?status=pending", "")
	c.Params = gin.Params{{Key: "id", Value: "a1"}}
	h.ListConversions(c)
	if w.Code != 200 {
		t.Errorf("good filter = %d, want 200", w.Code)
	}
}
