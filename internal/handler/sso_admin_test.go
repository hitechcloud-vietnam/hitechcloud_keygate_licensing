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

// fakeSSOStore stands in for store.Store so the refusal paths — invalid
// provider config, name/domain conflicts, double revoke, the shown-once
// token — run without a database. It records what the handler wrote and
// audited.
type fakeSSOStore struct {
	conns map[string]*model.SSOConnection
	toks  map[string]*model.SCIMToken

	createErr      error
	updateErr      error
	deleteErr      error
	enableErr      error
	revokeErr      error
	tokenDeleteErr error
	tokenCreateErr error

	lastCreate      *model.SSOConnection
	lastUpdate      *model.SSOConnection
	lastEnableID    string
	lastEnableVal   bool
	lastRevokeID    string
	lastTokenCreate *model.SCIMToken
	audits          []*model.AuditLog

	tokenPlaintext string
}

func (f *fakeSSOStore) CreateSSOConnection(_ context.Context, conn *model.SSOConnection) error {
	if f.createErr != nil {
		return f.createErr
	}
	conn.ID = "conn-new"
	f.lastCreate = conn
	return nil
}

func (f *fakeSSOStore) FindSSOConnectionByID(_ context.Context, id string) (*model.SSOConnection, error) {
	if c, ok := f.conns[id]; ok {
		cp := *c // a copy, so a refused patch cannot mutate the fixture
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeSSOStore) ListSSOConnections(_ context.Context, _ store.Page) ([]*model.SSOConnection, int, error) {
	out := make([]*model.SSOConnection, 0, len(f.conns))
	for _, c := range f.conns {
		out = append(out, c)
	}
	return out, len(out), nil
}

func (f *fakeSSOStore) UpdateSSOConnection(_ context.Context, conn *model.SSOConnection) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	f.lastUpdate = conn
	return nil
}

func (f *fakeSSOStore) DeleteSSOConnection(_ context.Context, id string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	if _, ok := f.conns[id]; !ok {
		return sql.ErrNoRows
	}
	delete(f.conns, id)
	return nil
}

func (f *fakeSSOStore) SetSSOEnabled(_ context.Context, id string, enabled bool) error {
	if f.enableErr != nil {
		return f.enableErr
	}
	if _, ok := f.conns[id]; !ok {
		return sql.ErrNoRows
	}
	f.lastEnableID, f.lastEnableVal = id, enabled
	return nil
}

func (f *fakeSSOStore) CreateSCIMToken(_ context.Context, tok *model.SCIMToken) (string, error) {
	if f.tokenCreateErr != nil {
		return "", f.tokenCreateErr
	}
	tok.ID = "tok-new"
	plain := f.tokenPlaintext
	if plain == "" {
		plain = "htc_scim_FAKEPLAINTEXT"
	}
	tok.TokenPrefix = model.SCIMTokenDisplayPrefix(plain)
	tok.TokenHash = model.HashSCIMToken(plain)
	f.lastTokenCreate = tok
	return plain, nil
}

func (f *fakeSSOStore) ListSCIMTokens(_ context.Context, _ store.Page) ([]*model.SCIMToken, int, error) {
	out := make([]*model.SCIMToken, 0, len(f.toks))
	for _, t := range f.toks {
		out = append(out, t)
	}
	return out, len(out), nil
}

func (f *fakeSSOStore) RevokeSCIMToken(_ context.Context, id string) error {
	if f.revokeErr != nil {
		return f.revokeErr
	}
	f.lastRevokeID = id
	return nil
}

func (f *fakeSSOStore) DeleteSCIMToken(_ context.Context, id string) error {
	if f.tokenDeleteErr != nil {
		return f.tokenDeleteErr
	}
	if _, ok := f.toks[id]; !ok {
		return sql.ErrNoRows
	}
	delete(f.toks, id)
	return nil
}

func (f *fakeSSOStore) Audit(_ context.Context, log *model.AuditLog) {
	f.audits = append(f.audits, log)
}

func ssoReq(t *testing.T, method, target, body string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return w, c
}

// ssoErrCode reads the machine-readable code out of the error envelope.
func ssoErrCode(t *testing.T, w *httptest.ResponseRecorder) string {
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

// The provider-config CHECK runs before anything is written: a saml
// connection with no certificate and an oidc one with no issuer are the
// caller's 400 naming the missing field, not a database 500.
func TestSSOCreateProviderConfigEnforced(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // fragment the response must mention
	}{
		{"saml without certificate", `{"name":"A","provider_type":"saml","domain":"a.com","saml_entity_id":"e","saml_sso_url":"https://idp/sso"}`, "saml_certificate"},
		{"saml without entity id", `{"name":"A","provider_type":"saml","domain":"a.com","saml_sso_url":"u","saml_certificate":"-----BEGIN CERTIFICATE-----x"}`, "saml_entity_id"},
		{"oidc without issuer", `{"name":"A","provider_type":"oidc","domain":"a.com","oidc_client_id":"cid"}`, "oidc_issuer"},
		{"oidc without client id", `{"name":"A","provider_type":"oidc","domain":"a.com","oidc_issuer":"https://idp"}`, "oidc_client_id"},
		{"unknown provider type", `{"name":"A","provider_type":"ldap","domain":"a.com"}`, "provider_type"},
	}
	for _, tc := range cases {
		fake := &fakeSSOStore{}
		h := NewSSOAdminHandler(nil)
		h.store = fake
		w, c := ssoReq(t, "POST", "/admin/sso/connections", tc.body)
		h.Create(c)
		if w.Code != 400 {
			t.Errorf("%s: status = %d, want 400; body %s", tc.name, w.Code, w.Body.String())
			continue
		}
		if !strings.Contains(w.Body.String(), tc.want) {
			t.Errorf("%s: body %s does not mention %q", tc.name, w.Body.String(), tc.want)
		}
		if fake.lastCreate != nil {
			t.Errorf("%s: a refused create still wrote %+v", tc.name, fake.lastCreate)
		}
	}
}

// A SAML certificate must be PEM-armoured; pasting a URL or a private key
// is refused before it is stored.
func TestSSOCreateBadPEMIs400(t *testing.T) {
	fake := &fakeSSOStore{}
	h := NewSSOAdminHandler(nil)
	h.store = fake
	w, c := ssoReq(t, "POST", "/admin/sso/connections",
		`{"name":"A","provider_type":"saml","domain":"a.com","saml_entity_id":"e","saml_sso_url":"u","saml_certificate":"https://idp/cert"}`)
	h.Create(c)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400; body %s", w.Code, w.Body.String())
	}
	if fake.lastCreate != nil {
		t.Error("a refused create still wrote")
	}
}

// The domain is the routing handle: folded lowercase on write so one
// domain cannot be spelled two ways, and shape-checked so an email or URL
// is not mistaken for one.
func TestSSOCreateNormalizesDomain(t *testing.T) {
	fake := &fakeSSOStore{}
	h := NewSSOAdminHandler(nil)
	h.store = fake
	w, c := ssoReq(t, "POST", "/admin/sso/connections",
		`{"name":"Acme","provider_type":"oidc","domain":"  Acme.COM  ","oidc_issuer":"https://idp","oidc_client_id":"cid"}`)
	h.Create(c)
	if w.Code != 201 {
		t.Fatalf("status = %d, want 201; body %s", w.Code, w.Body.String())
	}
	if fake.lastCreate == nil || fake.lastCreate.Domain != "acme.com" {
		t.Errorf("stored domain = %+v, want %q (folded)", fake.lastCreate, "acme.com")
	}

	// A domain that is not shaped like one is refused.
	w2, c2 := ssoReq(t, "POST", "/admin/sso/connections",
		`{"name":"B","provider_type":"oidc","domain":"not-a-domain","oidc_issuer":"https://idp","oidc_client_id":"cid"}`)
	h.Create(c2)
	if w2.Code != 400 {
		t.Errorf("bad domain status = %d, want 400; body %s", w2.Code, w2.Body.String())
	}
}

// A name and a domain already taken are distinct 409s an admin can act
// on, not 500s.
func TestSSOCreateConflictsAre409(t *testing.T) {
	fake := &fakeSSOStore{createErr: store.ErrSSONameTaken}
	h := NewSSOAdminHandler(nil)
	h.store = fake
	w, c := ssoReq(t, "POST", "/admin/sso/connections",
		`{"name":"Acme","provider_type":"oidc","domain":"a.com","oidc_issuer":"https://idp","oidc_client_id":"cid"}`)
	h.Create(c)
	if w.Code != 409 {
		t.Fatalf("name conflict status = %d, want 409; body %s", w.Code, w.Body.String())
	}
	if code := ssoErrCode(t, w); code != "SSO_NAME_TAKEN" {
		t.Errorf("code = %q, want SSO_NAME_TAKEN", code)
	}

	fake.createErr = store.ErrSSODomainTaken
	w2, c2 := ssoReq(t, "POST", "/admin/sso/connections",
		`{"name":"Acme","provider_type":"oidc","domain":"a.com","oidc_issuer":"https://idp","oidc_client_id":"cid"}`)
	h.Create(c2)
	if w2.Code != 409 {
		t.Fatalf("domain conflict status = %d, want 409; body %s", w2.Code, w2.Body.String())
	}
	if code := ssoErrCode(t, w2); code != "SSO_DOMAIN_TAKEN" {
		t.Errorf("code = %q, want SSO_DOMAIN_TAKEN", code)
	}
}

// The display name is bounded to the migration's 100 chars, so a longer
// one is the caller's 400 rather than a database 500.
func TestSSOCreateNameTooLongIs400(t *testing.T) {
	fake := &fakeSSOStore{}
	h := NewSSOAdminHandler(nil)
	h.store = fake
	long := strings.Repeat("n", 101)
	w, c := ssoReq(t, "POST", "/admin/sso/connections",
		`{"name":"`+long+`","provider_type":"oidc","domain":"a.com","oidc_issuer":"https://idp","oidc_client_id":"cid"}`)
	h.Create(c)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400; body %s", w.Code, w.Body.String())
	}
}

// The PATCH merge convention mirrors the PO billing patch: an omitted
// field keeps its stored value, "" clears a nullable field to NULL, and a
// value sets it. A patch must not clobber fields it did not name.
func TestSSOUpdateMergeAndClear(t *testing.T) {
	existing := &model.SSOConnection{
		ID: "c1", Name: "Acme", ProviderType: "oidc", Domain: "acme.com", Enabled: true,
		OIDCIssuer:       sspTest("https://idp"),
		OIDCClientID:     sspTest("cid"),
		OIDCClientSecret: sspTest("secret"),
		OIDCScopes:       sspTest("openid email"),
	}
	fake := &fakeSSOStore{conns: map[string]*model.SSOConnection{"c1": existing}}
	h := NewSSOAdminHandler(nil)
	h.store = fake

	// Set issuer, clear scopes, leave client id and secret alone.
	w, c := ssoReq(t, "PATCH", "/admin/sso/connections/c1",
		`{"oidc_issuer":"https://new-idp","oidc_scopes":""}`)
	c.Params = gin.Params{{Key: "id", Value: "c1"}}
	h.Update(c)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	u := fake.lastUpdate
	if u == nil {
		t.Fatal("update was not written")
	}
	if u.OIDCIssuer == nil || *u.OIDCIssuer != "https://new-idp" {
		t.Errorf("issuer = %v, want the new value", u.OIDCIssuer)
	}
	if u.OIDCScopes != nil {
		t.Errorf("scopes = %v, want cleared (nil)", *u.OIDCScopes)
	}
	if u.OIDCClientID == nil || *u.OIDCClientID != "cid" {
		t.Errorf("client id = %v, want kept (cid)", u.OIDCClientID)
	}
	if u.OIDCClientSecret == nil || *u.OIDCClientSecret != "secret" {
		t.Errorf("client secret = %v, want kept (secret)", u.OIDCClientSecret)
	}
}

// A patch that clears a required field leaves a connection that could not
// have been created; that is refused 400 before it is written.
func TestSSOUpdateClearingNameRefused(t *testing.T) {
	existing := &model.SSOConnection{
		ID: "c1", Name: "Acme", ProviderType: "oidc", Domain: "acme.com",
		OIDCIssuer: sspTest("https://idp"), OIDCClientID: sspTest("cid"),
	}
	fake := &fakeSSOStore{conns: map[string]*model.SSOConnection{"c1": existing}}
	h := NewSSOAdminHandler(nil)
	h.store = fake

	w, c := ssoReq(t, "PATCH", "/admin/sso/connections/c1", `{"name":"   "}`)
	c.Params = gin.Params{{Key: "id", Value: "c1"}}
	h.Update(c)
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400; body %s", w.Code, w.Body.String())
	}
	if fake.lastUpdate != nil {
		t.Error("a refused patch still wrote")
	}
}

// The SCIM token plaintext is returned exactly ONCE, in the create
// response's `token` field. The list response must carry neither the
// plaintext nor the stored hash — only the display prefix.
func TestSCIMTokenShownOnceAndListOmitsIt(t *testing.T) {
	fake := &fakeSSOStore{tokenPlaintext: "htc_scim_SECRETSECRETSECRET"}
	h := NewSSOAdminHandler(nil)
	h.store = fake

	w, c := ssoReq(t, "POST", "/admin/sso/tokens", `{"name":"Okta provisioning"}`)
	h.CreateToken(c)
	if w.Code != 201 {
		t.Fatalf("status = %d, want 201; body %s", w.Code, w.Body.String())
	}
	var created struct {
		Data struct {
			Token     string          `json:"token"`
			SCIMToken json.RawMessage `json:"scim_token"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("create body is not the envelope: %v; body %s", err, w.Body.String())
	}
	if created.Data.Token != "htc_scim_SECRETSECRETSECRET" {
		t.Errorf("token = %q, want the plaintext returned once", created.Data.Token)
	}
	// The plaintext must never be audited.
	for _, a := range fake.audits {
		if a.Changes != nil {
			if v, ok := a.Changes["token"]; ok && v == "htc_scim_SECRETSECRETSECRET" {
				t.Error("the audit trail recorded the plaintext token")
			}
		}
	}

	// Now the list: it must carry neither the plaintext nor the hash.
	fake.toks = map[string]*model.SCIMToken{
		"tok-new": fake.lastTokenCreate,
	}
	w2, c2 := ssoReq(t, "GET", "/admin/sso/tokens", "")
	h.ListTokens(c2)
	if w2.Code != 200 {
		t.Fatalf("list status = %d, want 200", w2.Code)
	}
	var listed struct {
		Data struct {
			SCIMTokens []map[string]any `json:"scim_tokens"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &listed); err != nil {
		t.Fatalf("list body is not the envelope: %v; body %s", err, w2.Body.String())
	}
	if len(listed.Data.SCIMTokens) == 0 {
		t.Fatal("list returned no tokens")
	}
	for i, row := range listed.Data.SCIMTokens {
		if _, has := row["token"]; has {
			t.Errorf("row %d exposes a plaintext token field", i)
		}
		if _, has := row["token_hash"]; has {
			t.Errorf("row %d exposes the token hash", i)
		}
		if _, has := row["token_prefix"]; !has {
			t.Errorf("row %d does not expose token_prefix", i)
		}
	}
	if strings.Contains(w2.Body.String(), "htc_scim_SECRETSECRETSECRET") {
		t.Error("the list response contains the plaintext token")
	}
}

// Revoke is soft and refuses to re-revoke (it was already dead — a silent
// no-op that looks like a fresh action would mislead). A missing token is
// 404.
func TestSCIMTokenRevokeSemantics(t *testing.T) {
	fake := &fakeSSOStore{toks: map[string]*model.SCIMToken{"t1": {ID: "t1", Name: "Okta"}}}
	h := NewSSOAdminHandler(nil)
	h.store = fake

	// Clean revoke -> 200 with an audit entry.
	w, c := ssoReq(t, "POST", "/admin/sso/tokens/t1/revoke", "")
	c.Params = gin.Params{{Key: "id", Value: "t1"}}
	h.RevokeToken(c)
	if w.Code != 200 {
		t.Fatalf("revoke status = %d, want 200; body %s", w.Code, w.Body.String())
	}
	if len(fake.audits) != 1 || fake.audits[0].Action != "revoked" {
		t.Errorf("revoke audits = %+v, want one 'revoked'", fake.audits)
	}

	// Already revoked -> 409.
	fake.revokeErr = store.ErrSCIMTokenRevoked
	w2, c2 := ssoReq(t, "POST", "/admin/sso/tokens/t1/revoke", "")
	c2.Params = gin.Params{{Key: "id", Value: "t1"}}
	h.RevokeToken(c2)
	if w2.Code != 409 {
		t.Fatalf("double revoke status = %d, want 409; body %s", w2.Code, w2.Body.String())
	}
	if code := ssoErrCode(t, w2); code != "SCIM_TOKEN_REVOKED" {
		t.Errorf("code = %q, want SCIM_TOKEN_REVOKED", code)
	}

	// Missing -> 404.
	fake.revokeErr = sql.ErrNoRows
	w3, c3 := ssoReq(t, "POST", "/admin/sso/tokens/nope/revoke", "")
	c3.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.RevokeToken(c3)
	if w3.Code != 404 {
		t.Fatalf("revoke missing status = %d, want 404", w3.Code)
	}
}

// Deleting a token is a HARD delete (distinct from revoke), answers 204,
// and a missing token is 404.
func TestSCIMTokenDeleteResponses(t *testing.T) {
	fake := &fakeSSOStore{toks: map[string]*model.SCIMToken{}}
	h := NewSSOAdminHandler(nil)
	h.store = fake

	w, c := ssoReq(t, "DELETE", "/admin/sso/tokens/nope", "")
	c.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.DeleteToken(c)
	if w.Code != 404 {
		t.Fatalf("missing delete status = %d, want 404", w.Code)
	}

	fake.toks["t1"] = &model.SCIMToken{ID: "t1"}
	w2, c2 := ssoReq(t, "DELETE", "/admin/sso/tokens/t1", "")
	c2.Params = gin.Params{{Key: "id", Value: "t1"}}
	h.DeleteToken(c2)
	c2.Writer.WriteHeaderNow()
	if w2.Code != 204 {
		t.Fatalf("clean delete status = %d, want 204", w2.Code)
	}
	if len(fake.audits) != 1 || fake.audits[0].Action != "deleted" {
		t.Errorf("delete audits = %+v, want one 'deleted'", fake.audits)
	}
}

// Enable/disable is the only writer of enabled; a missing row is 404.
func TestSSOEnableDisable(t *testing.T) {
	fake := &fakeSSOStore{conns: map[string]*model.SSOConnection{"c1": {ID: "c1"}}}
	h := NewSSOAdminHandler(nil)
	h.store = fake

	w, c := ssoReq(t, "POST", "/admin/sso/connections/c1/disable", "")
	c.Params = gin.Params{{Key: "id", Value: "c1"}}
	h.Disable(c)
	if w.Code != 200 {
		t.Fatalf("disable status = %d, want 200", w.Code)
	}
	if fake.lastEnableVal {
		t.Error("disable set enabled = true")
	}

	fake.enableErr = sql.ErrNoRows
	w2, c2 := ssoReq(t, "POST", "/admin/sso/connections/nope/enable", "")
	c2.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.Enable(c2)
	if w2.Code != 404 {
		t.Fatalf("enable missing status = %d, want 404", w2.Code)
	}
}

// A delete of a connection that is not there is 404, not a claimed
// deletion.
func TestSSODeleteNotFoundIs404(t *testing.T) {
	fake := &fakeSSOStore{conns: map[string]*model.SSOConnection{}}
	h := NewSSOAdminHandler(nil)
	h.store = fake
	w, c := ssoReq(t, "DELETE", "/admin/sso/connections/nope", "")
	c.Params = gin.Params{{Key: "id", Value: "nope"}}
	h.Delete(c)
	if w.Code != 404 {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

// sspTest is a *string literal helper for the nullable provider settings
// in these fixtures.
func sspTest(v string) *string { return &v }
