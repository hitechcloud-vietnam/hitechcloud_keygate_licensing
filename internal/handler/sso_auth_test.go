package handler

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/config"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/sso"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

const testBaseURL = "https://sp.example.com"

// ─── fake store ───

type fakeSSOAuthStore struct {
	conns       map[string]*model.SSOConnection
	connsByDom  map[string]*model.SSOConnection
	users       map[string]*model.User // by email
	idents      map[string]*store.SCIMIdentity
	refreshToks []string
	auditLogs   []*model.AuditLog
	roleSets    map[string]string
	profileUpd  map[string]string
}

func newFakeSSOAuthStore() *fakeSSOAuthStore {
	return &fakeSSOAuthStore{
		conns:      map[string]*model.SSOConnection{},
		connsByDom: map[string]*model.SSOConnection{},
		users:      map[string]*model.User{},
		idents:     map[string]*store.SCIMIdentity{},
		roleSets:   map[string]string{},
		profileUpd: map[string]string{},
	}
}

func (f *fakeSSOAuthStore) FindSSOConnectionByID(_ context.Context, id string) (*model.SSOConnection, error) {
	if c, ok := f.conns[id]; ok {
		return c, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeSSOAuthStore) FindSSOConnectionByDomain(_ context.Context, domain string) (*model.SSOConnection, error) {
	if c, ok := f.connsByDom[strings.ToLower(domain)]; ok {
		return c, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeSSOAuthStore) FindUserByEmail(_ context.Context, email string) (*model.User, error) {
	if u, ok := f.users[email]; ok {
		return u, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeSSOAuthStore) FindUserByID(_ context.Context, id string) (*model.User, error) {
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (f *fakeSSOAuthStore) UpsertUser(_ context.Context, u *model.User) error {
	if u.ID == "" {
		u.ID = "user-" + u.Email
	}
	cpy := *u
	f.users[u.Email] = &cpy
	return nil
}

func (f *fakeSSOAuthStore) UpdateUserProfile(_ context.Context, userID, name string) error {
	for _, u := range f.users {
		if u.ID == userID {
			u.Name = name
			f.profileUpd[userID] = name
		}
	}
	return nil
}

func (f *fakeSSOAuthStore) SetUserRole(_ context.Context, userID, role string) error {
	for _, u := range f.users {
		if u.ID == userID {
			u.Role = role
			f.roleSets[userID] = role
		}
	}
	return nil
}

func (f *fakeSSOAuthStore) CreateRefreshToken(_ context.Context, userID, tokenHash string, _ time.Time) error {
	f.refreshToks = append(f.refreshToks, userID+":"+tokenHash)
	return nil
}

func (f *fakeSSOAuthStore) FindSCIMIdentityByUserID(_ context.Context, userID string) (*store.SCIMIdentity, error) {
	if id, ok := f.idents[userID]; ok {
		return id, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeSSOAuthStore) Audit(_ context.Context, log *model.AuditLog) {
	f.auditLogs = append(f.auditLogs, log)
}

// ─── fixtures ───

func ssoConfig() *config.Config {
	return &config.Config{
		BaseURL:     testBaseURL,
		JWTSecret:   "0123456789abcdef0123456789abcdef",
		AdminEmails: []string{"admin@acme.com"},
	}
}

func strPtr(s string) *string { return &s }

func samlConn() *model.SSOConnection {
	return &model.SSOConnection{
		ID:              "conn-saml",
		Name:            "Acme SAML",
		ProviderType:    model.SSOProviderSAML,
		Domain:          "acme.com",
		Enabled:         true,
		SAMLEntityID:    strPtr("https://idp.example.com"),
		SAMLSSOURL:      strPtr("https://idp.example.com/sso"),
		SAMLCertificate: strPtr("-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----"),
	}
}

// cannedVerifier returns a stub that always yields a valid assertion for
// the given email, ignoring the XML.
func cannedVerifier(email string) sso.VerifySignatureFunc {
	return func([]byte, string) (*sso.SAMLAssertion, error) {
		now := time.Now().UTC()
		nb := now.Add(-time.Minute)
		noa := now.Add(time.Hour)
		return &sso.SAMLAssertion{
			Issuer:       "https://idp.example.com",
			NameID:       email,
			Email:        email,
			NotBefore:    &nb,
			NotOnOrAfter: &noa,
			Audiences:    []string{sso.SPEntityID(testBaseURL)},
		}, nil
	}
}

const minimalSAMLResponse = `<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" ID="_r"></samlp:Response>`

func acsForm(email string) url.Values {
	v := url.Values{}
	v.Set("SAMLResponse", base64.StdEncoding.EncodeToString([]byte(minimalSAMLResponse)))
	v.Set("RelayState", encodeSAMLState(samlState{ConnID: "conn-saml", Return: "/portal", ReqID: "_req1"}))
	return v
}

// ─── state encode/decode ───

func TestSAMLStateRoundTrip(t *testing.T) {
	st := samlState{ConnID: "c1", Return: "/portal", ReqID: "_req1"}
	got, err := decodeSAMLState(encodeSAMLState(st))
	if err != nil {
		t.Fatal(err)
	}
	if *got != st {
		t.Fatalf("round trip = %+v, want %+v", got, st)
	}
}

func TestSAMLStateMissingConnRejected(t *testing.T) {
	if _, err := decodeSAMLState(encodeSAMLState(samlState{Return: "/x"})); err == nil {
		t.Fatal("missing conn must error")
	}
}

func TestOIDCStateRoundTrip(t *testing.T) {
	st := oidcState{State: "s1", Nonce: "n1", ConnID: "c1", Return: "/portal"}
	got, err := decodeOIDCState(encodeOIDCState(st))
	if err != nil {
		t.Fatal(err)
	}
	if *got != st {
		t.Fatalf("round trip = %+v", got)
	}
}

func TestOIDCStateIncompleteRejected(t *testing.T) {
	if _, err := decodeOIDCState(encodeOIDCState(oidcState{State: "s"})); err == nil {
		t.Fatal("missing conn must error")
	}
}

// ─── GET /auth/sso/:id/start ───

func ssoStartRouter(h *SSOAuthHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/auth/sso/:id/start", h.Start)
	r.POST("/auth/sso/saml/acs", h.ACS)
	r.GET("/auth/sso/oidc/callback", h.Callback)
	return r
}

func TestStartSAMLRedirectsToIdP(t *testing.T) {
	f := newFakeSSOAuthStore()
	f.conns["conn-saml"] = samlConn()
	f.connsByDom["acme.com"] = f.conns["conn-saml"]
	h := &SSOAuthHandler{Store: f, Config: ssoConfig()}
	r := ssoStartRouter(h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/auth/sso/conn-saml/start?return=/portal", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://idp.example.com/sso") {
		t.Fatalf("redirect = %q", loc)
	}
	u, _ := url.Parse(loc)
	q := u.Query()
	if q.Get("SAMLRequest") == "" {
		t.Fatal("missing SAMLRequest")
	}
	// RelayState must bind the connection and the return path.
	st, err := decodeSAMLState(q.Get("RelayState"))
	if err != nil {
		t.Fatalf("bad RelayState: %v", err)
	}
	if st.ConnID != "conn-saml" || st.Return != "/portal" {
		t.Fatalf("relay state = %+v", st)
	}
}

func TestStartByDomain(t *testing.T) {
	f := newFakeSSOAuthStore()
	f.connsByDom["acme.com"] = samlConn()
	h := &SSOAuthHandler{Store: f, Config: ssoConfig()}
	r := ssoStartRouter(h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/auth/sso/ignored/start?domain=acme.com", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestStartDisabledOrMissing404(t *testing.T) {
	f := newFakeSSOAuthStore()
	c := samlConn()
	c.Enabled = false
	f.conns["conn-saml"] = c
	h := &SSOAuthHandler{Store: f, Config: ssoConfig()}
	r := ssoStartRouter(h)

	for _, path := range []string{"/auth/sso/conn-saml/start", "/auth/sso/nope/start"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", path, w.Code)
		}
	}
}

// ─── POST /auth/sso/saml/acs ───

func acsReq(r *gin.Engine, form url.Values) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/auth/sso/saml/acs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.ServeHTTP(w, req)
	return w
}

func TestACSHappyPathIssuesSession(t *testing.T) {
	f := newFakeSSOAuthStore()
	f.conns["conn-saml"] = samlConn()
	h := &SSOAuthHandler{Store: f, Config: ssoConfig(), VerifySignature: cannedVerifier("alice@acme.com")}
	r := ssoStartRouter(h)

	w := acsReq(r, acsForm("alice@acme.com"))
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Location"); got != "/portal" {
		t.Fatalf("redirect = %q", got)
	}
	// User provisioned + session cookie set + audit recorded.
	if _, ok := f.users["alice@acme.com"]; !ok {
		t.Fatal("user not provisioned")
	}
	cookies := w.Result().Cookies()
	var hasSession, hasRefresh bool
	for _, ck := range cookies {
		if ck.Name == "session" {
			hasSession = true
		}
		if ck.Name == "refresh_token" {
			hasRefresh = true
		}
	}
	if !hasSession || !hasRefresh {
		t.Fatalf("session cookies not set: %v", cookies)
	}
	if len(f.refreshToks) != 1 {
		t.Fatalf("refresh token not stored: %v", f.refreshToks)
	}
	if len(f.auditLogs) != 1 || f.auditLogs[0].ActorType != "saml" {
		t.Fatalf("audit not recorded: %+v", f.auditLogs)
	}
}

func TestACSDomainGateRejects(t *testing.T) {
	f := newFakeSSOAuthStore()
	f.conns["conn-saml"] = samlConn() // domain acme.com
	h := &SSOAuthHandler{Store: f, Config: ssoConfig(), VerifySignature: cannedVerifier("eve@evil.com")}
	r := ssoStartRouter(h)

	w := acsReq(r, acsForm("eve@evil.com"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if len(f.users) != 0 {
		t.Fatal("rejected assertion must not provision a user")
	}
	if len(f.refreshToks) != 0 {
		t.Fatal("rejected assertion must not issue a session")
	}
}

func TestACSSignatureFailureRejected(t *testing.T) {
	f := newFakeSSOAuthStore()
	f.conns["conn-saml"] = samlConn()
	h := &SSOAuthHandler{Store: f, Config: ssoConfig(), VerifySignature: func([]byte, string) (*sso.SAMLAssertion, error) {
		return nil, errFake
	}}
	r := ssoStartRouter(h)

	w := acsReq(r, acsForm("alice@acme.com"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if len(f.users) != 0 {
		t.Fatal("must not provision on signature failure")
	}
}

func TestACSDeactivatedSCIMRejected(t *testing.T) {
	f := newFakeSSOAuthStore()
	f.conns["conn-saml"] = samlConn()
	// Pre-existing SCIM-managed user, deactivated.
	u := &model.User{ID: "user-alice", Email: "alice@acme.com", Name: "Alice"}
	f.users["alice@acme.com"] = u
	f.idents["user-alice"] = &store.SCIMIdentity{ID: "scim-1", UserID: "user-alice", Active: false}

	h := &SSOAuthHandler{Store: f, Config: ssoConfig(), VerifySignature: cannedVerifier("alice@acme.com")}
	r := ssoStartRouter(h)

	w := acsReq(r, acsForm("alice@acme.com"))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if len(f.refreshToks) != 0 {
		t.Fatal("deactivated account must not get a session")
	}
}

// ─── GET /auth/sso/oidc/callback ───

func TestCallbackStateMismatchRejected(t *testing.T) {
	f := newFakeSSOAuthStore()
	h := &SSOAuthHandler{Store: f, Config: ssoConfig()}
	r := ssoStartRouter(h)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/auth/sso/oidc/callback?code=x&state=WRONG", nil)
	// Cookie holds a DIFFERENT state.
	req.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: encodeOIDCState(oidcState{State: "RIGHT", Nonce: "n", ConnID: "c", Return: "/portal"})})
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestCallbackErrorParamRejected(t *testing.T) {
	f := newFakeSSOAuthStore()
	h := &SSOAuthHandler{Store: f, Config: ssoConfig()}
	r := ssoStartRouter(h)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/auth/sso/oidc/callback?error=access_denied", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestCallbackMissingCodeRejected(t *testing.T) {
	f := newFakeSSOAuthStore()
	h := &SSOAuthHandler{Store: f, Config: ssoConfig()}
	r := ssoStartRouter(h)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/auth/sso/oidc/callback?state=x", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestCallbackMissingCookieRejected(t *testing.T) {
	f := newFakeSSOAuthStore()
	h := &SSOAuthHandler{Store: f, Config: ssoConfig()}
	r := ssoStartRouter(h)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/auth/sso/oidc/callback?code=x&state=y", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// ─── OIDC full happy path against an httptest provider ───

type oidcProvider struct {
	srv    *httptest.Server
	key    *rsa.PrivateKey
	kid    string
	jwks   []byte
	issuer string
	client string
	nonce  string
}

func newOIDCProvider(t *testing.T) *oidcProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &oidcProvider{key: key, kid: "kid-1", client: "client-123"}
	e := big.NewInt(int64(key.E)).Bytes()
	p.jwks, _ = json.Marshal(map[string]any{"keys": []map[string]any{{
		"kty": "RSA", "kid": p.kid, "n": b64u(key.N.Bytes()), "e": b64u(e),
	}}})

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 p.issuer,
			"authorization_endpoint": p.srv.URL + "/authorize",
			"token_endpoint":         p.srv.URL + "/token",
			"jwks_uri":               p.srv.URL + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(p.jwks)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id_token": p.signIDToken(), "token_type": "Bearer"})
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	p.issuer = p.srv.URL
	return p
}

func (p *oidcProvider) signIDToken() string {
	now := time.Now().UTC()
	hb, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": p.kid})
	cb, _ := json.Marshal(map[string]any{
		"iss": p.issuer, "sub": "u1", "aud": p.client,
		"exp": now.Add(time.Hour).Unix(), "iat": now.Add(-time.Minute).Unix(),
		"nonce": p.nonce, "email": "alice@acme.com", "name": "Alice Smith",
	})
	si := b64u(hb) + "." + b64u(cb)
	h := sha256.Sum256([]byte(si))
	sig, _ := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, h[:])
	return si + "." + b64u(sig)
}

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func TestOIDCCallbackFullFlow(t *testing.T) {
	p := newOIDCProvider(t)

	f := newFakeSSOAuthStore()
	conn := &model.SSOConnection{
		ID:           "conn-oidc",
		Name:         "Acme OIDC",
		ProviderType: model.SSOProviderOIDC,
		Domain:       "acme.com",
		Enabled:      true,
		OIDCIssuer:   strPtr(p.issuer),
		OIDCClientID: strPtr(p.client),
	}
	f.conns["conn-oidc"] = conn

	h := &SSOAuthHandler{Store: f, Config: ssoConfig(), HTTPClient: p.srv.Client()}
	r := ssoStartRouter(h)

	p.nonce = "nonce-abc"
	st := encodeOIDCState(oidcState{State: "state-xyz", Nonce: p.nonce, ConnID: "conn-oidc", Return: "/portal"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/auth/sso/oidc/callback?code=goodcode&state=state-xyz", nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: st})
	r.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Location"); got != "/portal" {
		t.Fatalf("redirect = %q", got)
	}
	if _, ok := f.users["alice@acme.com"]; !ok {
		t.Fatal("user not provisioned")
	}
	if len(f.refreshToks) != 1 {
		t.Fatalf("session not issued: %v", f.refreshToks)
	}
}

func TestOIDCCallbackNonceMismatchRejected(t *testing.T) {
	p := newOIDCProvider(t)
	f := newFakeSSOAuthStore()
	f.conns["conn-oidc"] = &model.SSOConnection{
		ID: "conn-oidc", ProviderType: model.SSOProviderOIDC, Domain: "acme.com", Enabled: true,
		OIDCIssuer: strPtr(p.issuer), OIDCClientID: strPtr(p.client),
	}
	h := &SSOAuthHandler{Store: f, Config: ssoConfig(), HTTPClient: p.srv.Client()}
	r := ssoStartRouter(h)

	p.nonce = "nonce-from-idp" // token carries a DIFFERENT nonce than the cookie
	st := encodeOIDCState(oidcState{State: "state-xyz", Nonce: "nonce-expected", ConnID: "conn-oidc", Return: "/portal"})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/auth/sso/oidc/callback?code=goodcode&state=state-xyz", nil)
	req.AddCookie(&http.Cookie{Name: oidcStateCookie, Value: st})
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	if len(f.users) != 0 {
		t.Fatal("nonce mismatch must not provision a user")
	}
}

var errFake = errors.New("fake verification failure")
