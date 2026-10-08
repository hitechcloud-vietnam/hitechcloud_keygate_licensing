package sso

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ─── JWT / JWK helpers ───

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// oidcSigningKey is a generated RSA key plus its JWKS document.
type oidcSigningKey struct {
	key  *rsa.PrivateKey
	kid  string
	jwks []byte
}

func makeOIDCKey(t *testing.T, kid string) *oidcSigningKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	e := big.NewInt(int64(key.E)).Bytes()
	doc := map[string]any{
		"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid,
			"n": b64url(key.N.Bytes()), "e": b64url(e),
		}},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return &oidcSigningKey{key: key, kid: kid, jwks: b}
}

// signIDToken builds a compact JWS id_token (RS256 by default) over the
// given header and claim maps.
func signIDToken(t *testing.T, key *rsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	hb, _ := json.Marshal(header)
	cb, _ := json.Marshal(claims)
	signingInput := b64url(hb) + "." + b64url(cb)
	var sig []byte
	var err error
	switch header["alg"] {
	case "RS384":
		h := sha512.New384()
		h.Write([]byte(signingInput))
		sig, err = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA384, h.Sum(nil))
	case "RS512":
		h := sha512.New()
		h.Write([]byte(signingInput))
		sig, err = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA512, h.Sum(nil))
	default: // RS256 (and "none" still signs so the tamper path is real)
		h := sha256.New()
		h.Write([]byte(signingInput))
		sig, err = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, h.Sum(nil))
	}
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return signingInput + "." + b64url(sig)
}

// validClaims is a healthy claim set for the given issuer/client/nonce.
func validClaims(issuer, clientID, nonce string, now time.Time) map[string]any {
	return map[string]any{
		"iss":   issuer,
		"sub":   "user-1",
		"aud":   clientID,
		"exp":   now.Add(time.Hour).Unix(),
		"iat":   now.Add(-time.Minute).Unix(),
		"nonce": nonce,
		"email": "alice@acme.com",
		"name":  "Alice Smith",
	}
}

// serveJWKS stands up an httptest server exposing the key's JWKS and
// returns its URL.
func serveJWKS(t *testing.T, k *oidcSigningKey) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(k.jwks)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/jwks"
}

const testIssuer = "https://idp.example.com"
const testClientID = "client-123"

// ─── discovery ───

func TestDiscoverOIDC(t *testing.T) {
	var srvURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 srvURL,
			"authorization_endpoint": srvURL + "/authorize",
			"token_endpoint":         srvURL + "/token",
			"jwks_uri":               srvURL + "/jwks",
		})
	}))
	defer srv.Close()
	srvURL = srv.URL

	cfg, err := DiscoverOIDC(context.Background(), srv.Client(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthorizationEndpoint != srv.URL+"/authorize" || cfg.TokenEndpoint != srv.URL+"/token" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.JWKSURI != srv.URL+"/jwks" {
		t.Fatalf("jwks_uri = %q", cfg.JWKSURI)
	}
}

func TestDiscoverOIDCRejectsIssuerMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                 "https://attacker.example.com",
			"authorization_endpoint": "https://a/authorize",
			"token_endpoint":         "https://a/token",
		})
	}))
	defer srv.Close()
	if _, err := DiscoverOIDC(context.Background(), srv.Client(), srv.URL); err == nil {
		t.Fatal("issuer mismatch must be refused")
	}
}

func TestDiscoverOIDCEmptyIssuer(t *testing.T) {
	if _, err := DiscoverOIDC(context.Background(), http.DefaultClient, "  "); err == nil {
		t.Fatal("empty issuer must error")
	}
}

// ─── authorize URL ───

func TestBuildAuthorizeURL(t *testing.T) {
	cfg := &OIDCConfig{AuthorizationEndpoint: "https://idp.example.com/authorize"}
	u, err := BuildAuthorizeURL(cfg, testClientID, "https://sp/cb", "", "state-1", "nonce-1")
	if err != nil {
		t.Fatal(err)
	}
	p, _ := url.Parse(u)
	q := p.Query()
	if q.Get("response_type") != "code" {
		t.Fatalf("response_type = %q", q.Get("response_type"))
	}
	if q.Get("client_id") != testClientID || q.Get("redirect_uri") != "https://sp/cb" {
		t.Fatalf("client_id/redirect_uri wrong: %s", u)
	}
	if q.Get("scope") != "openid email profile" {
		t.Fatalf("default scope = %q", q.Get("scope"))
	}
	if q.Get("state") != "state-1" || q.Get("nonce") != "nonce-1" {
		t.Fatalf("state/nonce wrong: %s", u)
	}
}

func TestBuildAuthorizeURLNoEndpoint(t *testing.T) {
	if _, err := BuildAuthorizeURL(&OIDCConfig{}, testClientID, "cb", "", "s", "n"); err == nil {
		t.Fatal("missing authorization endpoint must error")
	}
}

// ─── token exchange ───

func TestExchangeCode(t *testing.T) {
	var gotAuth string
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_ = r.ParseForm()
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "at", "id_token": "RAW.ID.TOKEN", "token_type": "Bearer", "expires_in": 3600,
		})
	}))
	defer srv.Close()

	cfg := &OIDCConfig{TokenEndpoint: srv.URL}
	raw, err := ExchangeCode(context.Background(), srv.Client(), cfg, testClientID, "secret", "code-1", "https://sp/cb")
	if err != nil {
		t.Fatal(err)
	}
	if raw != "RAW.ID.TOKEN" {
		t.Fatalf("id_token = %q", raw)
	}
	if gotForm.Get("grant_type") != "authorization_code" || gotForm.Get("code") != "code-1" {
		t.Fatalf("form = %v", gotForm)
	}
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Fatalf("expected client_secret_basic, got %q", gotAuth)
	}
}

func TestExchangeCodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
	}))
	defer srv.Close()
	if _, err := ExchangeCode(context.Background(), srv.Client(), &OIDCConfig{TokenEndpoint: srv.URL},
		testClientID, "secret", "bad", "cb"); err == nil {
		t.Fatal("token endpoint error must surface")
	}
}

// ─── id_token verification ───

func TestVerifyIDTokenOK(t *testing.T) {
	k := makeOIDCKey(t, "kid-1")
	jwksURL := serveJWKS(t, k)
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)

	header := map[string]any{"alg": "RS256", "kid": k.kid}
	claims := validClaims(testIssuer, testClientID, "nonce-1", now)
	raw := signIDToken(t, k.key, header, claims)

	tok, err := VerifyIDToken(context.Background(), http.DefaultClient, raw, jwksURL,
		testIssuer, testClientID, "nonce-1", now)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if tok.Email != "alice@acme.com" || tok.Subject != "user-1" {
		t.Fatalf("token = %+v", tok)
	}
	if tok.Name != "Alice Smith" {
		t.Fatalf("name = %q", tok.Name)
	}
}

func TestVerifyIDTokenAudienceArray(t *testing.T) {
	k := makeOIDCKey(t, "kid-1")
	jwksURL := serveJWKS(t, k)
	now := time.Now().UTC()
	claims := validClaims(testIssuer, testClientID, "nonce-1", now)
	claims["aud"] = []string{"other", testClientID}
	raw := signIDToken(t, k.key, map[string]any{"alg": "RS256", "kid": k.kid}, claims)
	if _, err := VerifyIDToken(context.Background(), http.DefaultClient, raw, jwksURL,
		testIssuer, testClientID, "nonce-1", now); err != nil {
		t.Fatalf("array aud containing clientID should verify: %v", err)
	}
}

func TestVerifyIDTokenNonceMismatch(t *testing.T) {
	k := makeOIDCKey(t, "kid-1")
	jwksURL := serveJWKS(t, k)
	now := time.Now().UTC()
	raw := signIDToken(t, k.key, map[string]any{"alg": "RS256", "kid": k.kid},
		validClaims(testIssuer, testClientID, "nonce-WRONG", now))
	if _, err := VerifyIDToken(context.Background(), http.DefaultClient, raw, jwksURL,
		testIssuer, testClientID, "nonce-1", now); err == nil {
		t.Fatal("nonce mismatch must be rejected")
	}
}

func TestVerifyIDTokenIssuerMismatch(t *testing.T) {
	k := makeOIDCKey(t, "kid-1")
	jwksURL := serveJWKS(t, k)
	now := time.Now().UTC()
	raw := signIDToken(t, k.key, map[string]any{"alg": "RS256", "kid": k.kid},
		validClaims("https://evil.example.com", testClientID, "nonce-1", now))
	if _, err := VerifyIDToken(context.Background(), http.DefaultClient, raw, jwksURL,
		testIssuer, testClientID, "nonce-1", now); err == nil {
		t.Fatal("issuer mismatch must be rejected")
	}
}

func TestVerifyIDTokenAudienceMismatch(t *testing.T) {
	k := makeOIDCKey(t, "kid-1")
	jwksURL := serveJWKS(t, k)
	now := time.Now().UTC()
	raw := signIDToken(t, k.key, map[string]any{"alg": "RS256", "kid": k.kid},
		validClaims(testIssuer, "other-client", "nonce-1", now))
	if _, err := VerifyIDToken(context.Background(), http.DefaultClient, raw, jwksURL,
		testIssuer, testClientID, "nonce-1", now); err == nil {
		t.Fatal("audience mismatch must be rejected")
	}
}

func TestVerifyIDTokenExpired(t *testing.T) {
	k := makeOIDCKey(t, "kid-1")
	jwksURL := serveJWKS(t, k)
	now := time.Now().UTC()
	claims := validClaims(testIssuer, testClientID, "nonce-1", now)
	claims["exp"] = now.Add(-2 * ClockSkew).Unix() // expired beyond skew
	raw := signIDToken(t, k.key, map[string]any{"alg": "RS256", "kid": k.kid}, claims)
	if _, err := VerifyIDToken(context.Background(), http.DefaultClient, raw, jwksURL,
		testIssuer, testClientID, "nonce-1", now); err == nil {
		t.Fatal("expired token must be rejected")
	}
}

func TestVerifyIDTokenNoEmail(t *testing.T) {
	k := makeOIDCKey(t, "kid-1")
	jwksURL := serveJWKS(t, k)
	now := time.Now().UTC()
	claims := validClaims(testIssuer, testClientID, "nonce-1", now)
	delete(claims, "email")
	raw := signIDToken(t, k.key, map[string]any{"alg": "RS256", "kid": k.kid}, claims)
	if _, err := VerifyIDToken(context.Background(), http.DefaultClient, raw, jwksURL,
		testIssuer, testClientID, "nonce-1", now); err == nil {
		t.Fatal("token with no email must be rejected")
	}
}

func TestVerifyIDTokenAlgNoneRejected(t *testing.T) {
	k := makeOIDCKey(t, "kid-1")
	jwksURL := serveJWKS(t, k)
	now := time.Now().UTC()
	// alg "none" with an empty signature.
	hb, _ := json.Marshal(map[string]any{"alg": "none", "kid": k.kid})
	cb, _ := json.Marshal(validClaims(testIssuer, testClientID, "nonce-1", now))
	raw := b64url(hb) + "." + b64url(cb) + "."
	_, err := VerifyIDToken(context.Background(), http.DefaultClient, raw, jwksURL,
		testIssuer, testClientID, "nonce-1", now)
	if err == nil {
		t.Fatal("alg none must be rejected")
	}
	if !strings.Contains(err.Error(), "none") {
		t.Fatalf("expected alg-none rejection, got %v", err)
	}
}

func TestVerifyIDTokenHSAlgRejected(t *testing.T) {
	k := makeOIDCKey(t, "kid-1")
	jwksURL := serveJWKS(t, k)
	now := time.Now().UTC()
	raw := signIDToken(t, k.key, map[string]any{"alg": "HS256", "kid": k.kid},
		validClaims(testIssuer, testClientID, "nonce-1", now))
	if _, err := VerifyIDToken(context.Background(), http.DefaultClient, raw, jwksURL,
		testIssuer, testClientID, "nonce-1", now); err == nil {
		t.Fatal("HS* alg must be rejected")
	}
}

func TestVerifyIDTokenBadSignature(t *testing.T) {
	k := makeOIDCKey(t, "kid-1")
	jwksURL := serveJWKS(t, k)
	other := makeOIDCKey(t, "kid-1") // different key, same kid
	now := time.Now().UTC()
	// Sign with the WRONG key but serve the RIGHT key's JWKS.
	raw := signIDToken(t, other.key, map[string]any{"alg": "RS256", "kid": k.kid},
		validClaims(testIssuer, testClientID, "nonce-1", now))
	if _, err := VerifyIDToken(context.Background(), http.DefaultClient, raw, jwksURL,
		testIssuer, testClientID, "nonce-1", now); err == nil {
		t.Fatal("signature from the wrong key must be rejected")
	}
}

func TestVerifyIDTokenNotJWS(t *testing.T) {
	if _, err := VerifyIDToken(context.Background(), http.DefaultClient, "not-a-jws",
		"https://jwks", testIssuer, testClientID, "n", time.Now()); err == nil {
		t.Fatal("malformed token must be rejected")
	}
}

func TestVerifyIDTokenMissingNonceOKWhenNoneExpected(t *testing.T) {
	k := makeOIDCKey(t, "kid-1")
	jwksURL := serveJWKS(t, k)
	now := time.Now().UTC()
	claims := validClaims(testIssuer, testClientID, "nonce-1", now)
	raw := signIDToken(t, k.key, map[string]any{"alg": "RS256", "kid": k.kid}, claims)
	// Expected nonce "" skips the check.
	if _, err := VerifyIDToken(context.Background(), http.DefaultClient, raw, jwksURL,
		testIssuer, testClientID, "", now); err != nil {
		t.Fatalf("no-nonce-expectation should verify: %v", err)
	}
}
