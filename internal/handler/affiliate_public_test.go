package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// fakeAffiliatePublicStore stands in for store.Store so the
// open-redirect refusal, the hashed-IP click recording and the
// conversion idempotency run without a database. RecordConversion
// mirrors the real store's idempotency: a repeat for the same order_id
// answers the ORIGINAL row with created=false and writes nothing.
type fakeAffiliatePublicStore struct {
	codes       map[string]*model.ReferralCode // keyed by folded code
	affiliates  map[string]*model.Affiliate
	clicks      []*model.ReferralClick
	conversions []*model.AffiliateConversion
	byOrder     map[string]*model.AffiliateConversion

	clickErr  error
	recordErr error
}

func (f *fakeAffiliatePublicStore) FindReferralCodeByCode(_ context.Context, code string) (*model.ReferralCode, error) {
	if rc, ok := f.codes[model.NormalizeReferralCode(code)]; ok {
		cp := *rc
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeAffiliatePublicStore) FindAffiliateByID(_ context.Context, id string) (*model.Affiliate, error) {
	if a, ok := f.affiliates[id]; ok {
		cp := *a
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeAffiliatePublicStore) RecordClick(_ context.Context, cl *model.ReferralClick) (bool, error) {
	if f.clickErr != nil {
		return false, f.clickErr
	}
	f.clicks = append(f.clicks, cl)
	return true, nil
}

func (f *fakeAffiliatePublicStore) RecordConversion(_ context.Context, conv *model.AffiliateConversion) (*model.AffiliateConversion, bool, error) {
	if f.recordErr != nil {
		return nil, false, f.recordErr
	}
	if existing, ok := f.byOrder[conv.OrderID]; ok {
		return existing, false, nil
	}
	conv.ID = "conv-" + conv.OrderID
	conv.Status = model.AffiliateConversionStatusPending
	f.conversions = append(f.conversions, conv)
	f.byOrder[conv.OrderID] = conv
	return conv, true, nil
}

func newPublicFixture() *fakeAffiliatePublicStore {
	return &fakeAffiliatePublicStore{
		codes: map[string]*model.ReferralCode{
			"SAVE20": {ID: "code-1", AffiliateID: "aff-1", Code: "SAVE20",
				LandingURL: "https://example.com/promo", Active: true},
			"BAREXX": {ID: "code-2", AffiliateID: "aff-1", Code: "BAREXX",
				Active: true}, // no landing_url
			"OFF000": {ID: "code-3", AffiliateID: "aff-1", Code: "OFF000",
				LandingURL: "https://example.com/promo", Active: false},
			"SUSPEN": {ID: "code-4", AffiliateID: "aff-2", Code: "SUSPEN",
				LandingURL: "https://example.com/promo", Active: true},
		},
		affiliates: map[string]*model.Affiliate{
			"aff-1": {ID: "aff-1", Status: model.AffiliateStatusActive,
				CommissionModel: model.AffiliateCommissionModelPercent, CommissionBPS: 750},
			"aff-2": {ID: "aff-2", Status: model.AffiliateStatusSuspended,
				CommissionModel: model.AffiliateCommissionModelPercent, CommissionBPS: 750},
			"aff-3": {ID: "aff-3", Status: model.AffiliateStatusActive,
				CommissionModel: model.AffiliateCommissionModelFixed, CommissionMinor: 500},
		},
		byOrder: map[string]*model.AffiliateConversion{},
	}
}

func publicHandler(f *fakeAffiliatePublicStore) *AffiliatePublicHandler {
	h := NewAffiliatePublicHandler(nil)
	h.store = f
	return h
}

func redirectReq(t *testing.T, code string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/r/"+code, nil)
	c.Request.Header.Set("User-Agent", "Mozilla/5.0 (Test)")
	c.Params = gin.Params{{Key: "code", Value: code}}
	return w, c
}

func convertReq(t *testing.T, body string, cookies ...*http.Cookie) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "/api/v1/affiliates/convert", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	for _, ck := range cookies {
		c.Request.AddCookie(ck)
	}
	return w, c
}

func refCookie(value string) *http.Cookie {
	return &http.Cookie{Name: ReferralCookieName, Value: value}
}

// A live code: 302 to its stored landing_url, a click recorded, and
// the attribution cookie set. The click carries only salted hashes —
// the raw IP and user agent reach neither the store nor the table.
func TestAffiliateRedirectRecordsClickAndCookie(t *testing.T) {
	f := newPublicFixture()
	h := publicHandler(f)

	w, c := redirectReq(t, "save20!") // any spelling folds to SAVE20
	h.Redirect(c)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "https://example.com/promo" {
		t.Errorf("Location = %q, want the stored landing_url", loc)
	}

	if len(f.clicks) != 1 {
		t.Fatalf("clicks = %d, want 1", len(f.clicks))
	}
	click := f.clicks[0]
	if click.CodeID != "code-1" {
		t.Errorf("click code_id = %q, want code-1", click.CodeID)
	}
	// The raw IP (httptest's default remote is 192.0.2.1) and the raw
	// user agent are never what gets recorded — only their hashes.
	const rawIP = "192.0.2.1"
	const rawUA = "Mozilla/5.0 (Test)"
	if click.IPHash == rawIP || strings.Contains(click.IPHash, rawIP) {
		t.Errorf("ip_hash = %q contains the raw IP", click.IPHash)
	}
	if want := model.HashReferralIP("", rawIP); click.IPHash != want {
		t.Errorf("ip_hash = %q, want %q (hash of the IP)", click.IPHash, want)
	}
	if click.UserAgentHash == rawUA || strings.Contains(click.UserAgentHash, "Mozilla") {
		t.Errorf("user_agent_hash = %q contains the raw user agent", click.UserAgentHash)
	}
	if want := model.HashReferralUserAgent("", rawUA); click.UserAgentHash != want {
		t.Errorf("user_agent_hash = %q, want %q", click.UserAgentHash, want)
	}

	// The attribution cookie: 30-day window, HttpOnly (server-side
	// only), SameSite=Lax.
	var got *http.Cookie
	for _, ck := range w.Result().Cookies() {
		if ck.Name == ReferralCookieName {
			got = ck
		}
	}
	if got == nil {
		t.Fatalf("no %s cookie set; cookies %v", ReferralCookieName, w.Result().Cookies())
	}
	if got.Value != "SAVE20" {
		t.Errorf("cookie value = %q, want the folded code", got.Value)
	}
	if got.MaxAge != int(ReferralCookieMaxAge.Seconds()) || got.MaxAge != 2592000 {
		t.Errorf("cookie Max-Age = %d, want 30 days (2592000)", got.MaxAge)
	}
	if !got.HttpOnly {
		t.Error("the attribution cookie must be HttpOnly")
	}
}

// Open-redirect refusal: with no stored landing_url the Location is
// the configured default and nothing else — and the configured default
// itself is clamped to a local path, so no configuration can turn the
// endpoint into a redirector either.
func TestAffiliateRedirectOpenRedirectRefused(t *testing.T) {
	f := newPublicFixture()
	h := publicHandler(f)

	w, c := redirectReq(t, "BAREXX")
	h.Redirect(c)
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want exactly / for a code with no landing_url", loc)
	}

	// A configured default is honoured …
	h.DefaultRedirect = "/welcome"
	w, c = redirectReq(t, "BAREXX")
	h.Redirect(c)
	if loc := w.Header().Get("Location"); loc != "/welcome" {
		t.Errorf("Location = %q, want /welcome", loc)
	}

	// … but only as a local path: an absolute or protocol-relative
	// default falls back to /.
	for _, bad := range []string{"https://evil.example", "//evil.example", "javascript:alert(1)", ""} {
		h.DefaultRedirect = bad
		w, c = redirectReq(t, "BAREXX")
		h.Redirect(c)
		if loc := w.Header().Get("Location"); loc != "/" {
			t.Errorf("DefaultRedirect %q produced Location %q, want /", bad, loc)
		}
	}

	// The request can never influence the target: whatever the query
	// string says, a code with no landing_url goes to the default.
	h.DefaultRedirect = "/"
	rec := httptest.NewRecorder()
	c, _ = gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/r/BAREXX?url=https://evil.example&next=//evil.example", nil)
	c.Params = gin.Params{{Key: "code", Value: "BAREXX"}}
	h.Redirect(c)
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want / despite the query string", loc)
	}
}

// No oracle: unknown, inactive and suspended-affiliate codes answer
// exactly like a live code with no landing_url — 302 to the default,
// no click recorded, no cookie set.
func TestAffiliateRedirectUnknownCodesAreSilent(t *testing.T) {
	cases := []struct {
		name string
		code string
	}{
		{"unknown code", "NOPE99"},
		{"inactive code", "OFF000"},
		{"suspended affiliate's code", "SUSPEN"},
		{"malformed code", "!!"},
		{"empty code", ""},
	}
	for _, tc := range cases {
		f := newPublicFixture()
		h := publicHandler(f)

		w, c := redirectReq(t, tc.code)
		h.Redirect(c)

		if w.Code != http.StatusFound {
			t.Errorf("%s: status = %d, want 302", tc.name, w.Code)
		}
		if loc := w.Header().Get("Location"); loc != "/" {
			t.Errorf("%s: Location = %q, want /", tc.name, loc)
		}
		if len(f.clicks) != 0 {
			t.Errorf("%s: %d clicks recorded, want none", tc.name, len(f.clicks))
		}
		for _, ck := range w.Result().Cookies() {
			if ck.Name == ReferralCookieName {
				t.Errorf("%s: attribution cookie set for a code that is not live", tc.name)
			}
		}
	}
}

// The click is bookkeeping: if it fails the visitor is still
// redirected to what the affiliate earned.
func TestAffiliateRedirectClickFailureStillRedirects(t *testing.T) {
	f := newPublicFixture()
	f.clickErr = sql.ErrConnDone
	h := publicHandler(f)

	w, c := redirectReq(t, "SAVE20")
	h.Redirect(c)
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302 despite the click failure", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "https://example.com/promo" {
		t.Errorf("Location = %q, want the stored landing_url", loc)
	}
}

// The convert helper: commission math for both models, cookie
// attribution, and idempotency per order_id — a double submit is one
// row and the same row both times.
func TestAffiliateConvertCommissionAndIdempotency(t *testing.T) {
	f := newPublicFixture()
	h := publicHandler(f)

	// percent (7.5% of 101 minor units = 7.575, round-half-up -> 8).
	w, c := convertReq(t, `{"order_id":"ORD-1","order_total_minor":101,"code":"SAVE20","user_id":"u1","email":"buyer@example.com"}`)
	h.Convert(c)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	var body struct {
		Data struct {
			Created    bool                       `json:"created"`
			Conversion *model.AffiliateConversion `json:"conversion"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v; %s", err, w.Body.String())
	}
	if !body.Data.Created {
		t.Error("first submit must report created=true")
	}
	if body.Data.Conversion == nil || body.Data.Conversion.CommissionMinor != 8 {
		t.Errorf("commission = %+v, want 8 minor units (7.5%% of 101, half-up)", body.Data.Conversion)
	}
	if body.Data.Conversion.OrderTotalMinor != 101 || body.Data.Conversion.UserID != "u1" {
		t.Errorf("conversion = %+v, want the submitted order facts", body.Data.Conversion)
	}
	if body.Data.Conversion.Status != model.AffiliateConversionStatusPending {
		t.Errorf("status = %q, want pending", body.Data.Conversion.Status)
	}

	// The double submit: same order — one row, same row, created=false.
	w, c = convertReq(t, `{"order_id":"ORD-1","order_total_minor":101,"code":"SAVE20"}`)
	h.Convert(c)
	if w.Code != 200 {
		t.Fatalf("double submit status = %d, want 200 (idempotent)", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body.Data.Created {
		t.Error("double submit reported created=true")
	}
	if body.Data.Conversion.ID != "conv-ORD-1" {
		t.Errorf("double submit id = %q, want the original conv-ORD-1", body.Data.Conversion.ID)
	}
	if len(f.conversions) != 1 {
		t.Errorf("conversion rows = %d, want exactly 1 after a double submit", len(f.conversions))
	}

	// fixed model: the flat minor amount whatever the order total is
	// (fixed affiliate's code routed through its affiliate). The code
	// here belongs to aff-3 in spirit; drive the same body shape with a
	// fixed-model affiliate via the cookie path below.
	f2 := newPublicFixture()
	f2.codes["FIXEDX"] = &model.ReferralCode{ID: "code-5", AffiliateID: "aff-3", Code: "FIXEDX", Active: true}
	h2 := publicHandler(f2)
	w, c = convertReq(t, `{"order_id":"ORD-2","order_total_minor":99999,"code":"FIXEDX"}`)
	h2.Convert(c)
	if w.Code != 200 {
		t.Fatalf("fixed status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body: %v", err)
	}
	if body.Data.Conversion.CommissionMinor != 500 {
		t.Errorf("fixed commission = %d, want 500", body.Data.Conversion.CommissionMinor)
	}
}

// Cookie attribution: no code field, just the htc_ref cookie the
// redirect set — and an explicit code beats the cookie.
func TestAffiliateConvertFromCookie(t *testing.T) {
	f := newPublicFixture()
	h := publicHandler(f)

	// Resolved from the cookie alone.
	w, c := convertReq(t, `{"order_id":"ORD-3","order_total_minor":100}`, refCookie("SAVE20"))
	h.Convert(c)
	if w.Code != 200 {
		t.Fatalf("cookie convert status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if len(f.conversions) != 1 || f.conversions[0].CodeID != "code-1" {
		t.Fatalf("conversion = %+v, want code-1 resolved from the cookie", f.conversions)
	}

	// An explicit code wins over the ambient cookie.
	w, c = convertReq(t, `{"order_id":"ORD-4","order_total_minor":100,"code":"BAREXX"}`, refCookie("SAVE20"))
	h.Convert(c)
	if w.Code != 200 {
		t.Fatalf("explicit-code convert status = %d, want 200", w.Code)
	}
	if f.conversions[1].CodeID != "code-2" {
		t.Errorf("conversion code_id = %q, want code-2 (the explicit code wins)", f.conversions[1].CodeID)
	}

	// No code anywhere: 400, and nothing recorded.
	w, c = convertReq(t, `{"order_id":"ORD-5","order_total_minor":100}`)
	h.Convert(c)
	if w.Code != 400 {
		t.Errorf("codeless convert status = %d, want 400", w.Code)
	}
	if len(f.conversions) != 2 {
		t.Errorf("conversions = %d, want 2 (nothing from the refused submit)", len(f.conversions))
	}
}

// Every bad input is a 400 and writes nothing: missing order_id, a
// total outside the supported range, a malformed email, unknown and
// inactive codes, and a suspended affiliate (the fraud control — a
// suspended affiliate never converts).
func TestAffiliateConvertRefusals(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		cookie *http.Cookie
	}{
		{"missing order_id", `{"order_total_minor":100,"code":"SAVE20"}`, nil},
		{"blank order_id", `{"order_id":"  ","order_total_minor":100,"code":"SAVE20"}`, nil},
		{"negative total", `{"order_id":"ORD-A","order_total_minor":-1,"code":"SAVE20"}`, nil},
		{"total over the bound", `{"order_id":"ORD-B","order_total_minor":999999999999999999,"code":"SAVE20"}`, nil},
		{"bad email", `{"order_id":"ORD-C","order_total_minor":100,"email":"not-an-address","code":"SAVE20"}`, nil},
		{"unknown code", `{"order_id":"ORD-D","order_total_minor":100,"code":"NOPE99"}`, nil},
		{"inactive code", `{"order_id":"ORD-E","order_total_minor":100,"code":"OFF000"}`, nil},
		{"suspended affiliate", `{"order_id":"ORD-F","order_total_minor":100,"code":"SUSPEN"}`, nil},
		{"suspended via cookie", `{"order_id":"ORD-G","order_total_minor":100}`, refCookie("SUSPEN")},
		{"malformed code", `{"order_id":"ORD-H","order_total_minor":100,"code":"!!"}`, nil},
		{"not JSON", `not-json`, nil},
	}
	for _, tc := range cases {
		f := newPublicFixture()
		h := publicHandler(f)

		var w *httptest.ResponseRecorder
		var c *gin.Context
		if tc.cookie != nil {
			w, c = convertReq(t, tc.body, tc.cookie)
		} else {
			w, c = convertReq(t, tc.body)
		}
		h.Convert(c)

		if w.Code != 400 {
			t.Errorf("%s: status = %d, want 400; body %s", tc.name, w.Code, w.Body.String())
		}
		if len(f.conversions) != 0 {
			t.Errorf("%s: %d conversions recorded, want none", tc.name, len(f.conversions))
		}
	}

	// The store's own guards surface as 400 too (the status could have
	// flipped between the handler's checks and the write).
	f := newPublicFixture()
	f.recordErr = store.ErrAffiliateNotActive
	h := publicHandler(f)
	w, c := convertReq(t, `{"order_id":"ORD-Z","order_total_minor":100,"code":"SAVE20"}`)
	h.Convert(c)
	if w.Code != 400 {
		t.Errorf("store-level suspended refusal = %d, want 400", w.Code)
	}

	f = newPublicFixture()
	f.recordErr = store.ErrReferralCodeInactive
	h = publicHandler(f)
	w, c = convertReq(t, `{"order_id":"ORD-Z","order_total_minor":100,"code":"SAVE20"}`)
	h.Convert(c)
	if w.Code != 400 {
		t.Errorf("store-level inactive-code refusal = %d, want 400", w.Code)
	}
}
