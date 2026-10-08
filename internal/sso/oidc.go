package sso

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ─── Enterprise OIDC login (Phase 8, slice 2) ───
//
// The relying-party half of an OpenID Connect authorization-code login:
//
//   1. DiscoverOIDC reads the provider's /.well-known/openid-configuration.
//   2. BuildAuthorizeURL returns the 302 Location for the authorize
//      endpoint (code flow, with state + nonce).
//   3. ExchangeCode trades the code for tokens at the token endpoint.
//   4. VerifyIDToken validates the ID token's SIGNATURE against the
//      provider JWKS and its iss/aud/exp/nonce claims, and extracts email.
//
// Pure protocol logic — std-lib only (crypto/rsa + net/http). No store, no
// handler, no session here; internal/handler/sso_auth.go owns the browser
// redirects and the session cookie.
//
// # HTTP SAFETY
//
// All outbound calls use DefaultHTTPClient: a 10s timeout, and a
// CheckRedirect that REFUSES to follow a redirect to a different host than
// the original request. A token endpoint that tried to bounce our client
// secret to another origin is cut off instead of obeyed. Documented as a
// deliberate hard limit — cross-host redirects are never needed to talk to
// a single OIDC provider.
//
// # ID TOKEN SIGNATURE
//
// The ID token is the source of the login email, so it MUST be signature
// verified before its claims are trusted — otherwise anyone who can reach
// the callback could mint an arbitrary email and take over an account.
// VerifyIDToken fetches the provider JWKS and verifies an RS256/384/512
// signature (the RSA family ID tokens are signed with). `alg: none` and
// symmetric (HS*) algorithms are rejected. Documented limitation: only the
// RSA signature family is supported here; an IdP that signs its ID tokens
// with ECDSA (ES*) is refused rather than accepted unverified.

// DefaultHTTPTimeout bounds every outbound OIDC call.
const DefaultHTTPTimeout = 10 * time.Second

// OIDCCallbackURL is the OIDC redirect_uri — where the IdP returns the
// authorization code. Derived from the install's BaseURL so it always
// matches where the server actually lives.
func OIDCCallbackURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/auth/sso/oidc/callback"
}

// OIDCConfig is the subset of the provider discovery document we consume.
type OIDCConfig struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

// IDToken is a verified ID token's claims.
type IDToken struct {
	Issuer        string
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	GivenName     string
	FamilyName    string
	Audience      []string
	Nonce         string
	ExpiresAt     time.Time
	IssuedAt      time.Time
}

// DefaultHTTPClient is the shared client for OIDC discovery, token and JWKS
// calls. See the HTTP SAFETY note above.
func DefaultHTTPClient() *http.Client {
	return &http.Client{
		Timeout: DefaultHTTPTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("oidc: too many redirects")
			}
			if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
				return fmt.Errorf("oidc: refusing off-host redirect to %q", req.URL.Host)
			}
			return nil
		},
	}
}

// DiscoverOIDC reads the provider's discovery document and validates that
// it advertises the issuer we asked for (a mismatch is a misconfiguration
// or a man-in-the-middle; either way refuse).
func DiscoverOIDC(ctx context.Context, client *http.Client, issuer string) (*OIDCConfig, error) {
	if client == nil {
		client = DefaultHTTPClient()
	}
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	if issuer == "" {
		return nil, errors.New("oidc: issuer is empty")
	}
	u := issuer + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc: discovery failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc: discovery returned %d", resp.StatusCode)
	}
	var cfg OIDCConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("oidc: bad discovery document: %w", err)
	}
	if strings.TrimRight(strings.TrimSpace(cfg.Issuer), "/") != issuer {
		return nil, fmt.Errorf("oidc: discovery issuer %q does not match %q", cfg.Issuer, issuer)
	}
	if cfg.AuthorizationEndpoint == "" || cfg.TokenEndpoint == "" {
		return nil, errors.New("oidc: discovery missing authorization or token endpoint")
	}
	return &cfg, nil
}

// BuildAuthorizeURL assembles the authorization endpoint redirect (code
// flow). scope defaults to "openid email profile" when empty.
func BuildAuthorizeURL(cfg *OIDCConfig, clientID, redirectURI, scope, state, nonce string) (string, error) {
	if cfg == nil || cfg.AuthorizationEndpoint == "" {
		return "", errors.New("oidc: no authorization endpoint")
	}
	if scope == "" {
		scope = "openid email profile"
	}
	u, err := url.Parse(cfg.AuthorizationEndpoint)
	if err != nil {
		return "", fmt.Errorf("oidc: bad authorization endpoint: %w", err)
	}
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("scope", scope)
	q.Set("state", state)
	q.Set("nonce", nonce)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// tokenResponse is the token endpoint's JSON reply.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

// ExchangeCode trades an authorization code for tokens at the token
// endpoint and returns the raw ID token. The client authenticates with
// HTTP Basic (client_secret_basic) when a secret is configured — the
// standard method; a public client (no secret) sends client_id in the body.
func ExchangeCode(ctx context.Context, client *http.Client, cfg *OIDCConfig, clientID, clientSecret, code, redirectURI string) (string, error) {
	if client == nil {
		client = DefaultHTTPClient()
	}
	if cfg == nil || cfg.TokenEndpoint == "" {
		return "", errors.New("oidc: no token endpoint")
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("client_id", clientID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.TokenEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if clientSecret != "" {
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(clientSecret))
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("oidc: token exchange failed: %w", err)
	}
	defer resp.Body.Close()
	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("oidc: bad token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oidc: token endpoint returned %d (%s)", resp.StatusCode, tr.Error)
	}
	if tr.Error != "" {
		return "", fmt.Errorf("oidc: token error %s", tr.Error)
	}
	if tr.IDToken == "" {
		return "", errors.New("oidc: token response has no id_token")
	}
	return tr.IDToken, nil
}

// ─── ID token verification ───

// audienceField tolerates `aud` as either a JSON string or array.
type audienceField []string

func (a *audienceField) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audienceField{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = audienceField(many)
	return nil
}

// idClaims is the ID token payload we read.
type idClaims struct {
	Issuer        string        `json:"iss"`
	Subject       string        `json:"sub"`
	Audience      audienceField `json:"aud"`
	ExpiresAt     int64         `json:"exp"`
	IssuedAt      int64         `json:"iat"`
	Nonce         string        `json:"nonce"`
	Email         string        `json:"email"`
	EmailVerified bool          `json:"email_verified"`
	Name          string        `json:"name"`
	GivenName     string        `json:"given_name"`
	FamilyName    string        `json:"family_name"`
}

// idHeader is the JOSE header we read (alg + kid only).
type idHeader struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
}

// jwks is a JWKS document.
type jwks struct {
	Keys []jwkKey `json:"keys"`
}

// jwkKey is one JWK (RSA public key).
type jwkKey struct {
	KeyType string `json:"kty"`
	KeyID   string `json:"kid"`
	Use     string `json:"use"`
	Alg     string `json:"alg"`
	N       string `json:"n"`
	E       string `json:"e"`
}

// VerifyIDToken validates an ID token: it verifies the RS* signature against
// the provider JWKS, then the iss / aud / exp / nonce claims, and returns
// the verified claims. now is injected for testability.
//
// Failure modes all return an error — the token is never "partly trusted".
// The nonce check ties the token to the login attempt we started (CSRF /
// replay protection) and MUST match the nonce stored in the state cookie.
func VerifyIDToken(ctx context.Context, client *http.Client, raw, jwksURI, expectedIssuer, clientID, expectedNonce string, now time.Time) (*IDToken, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, errors.New("oidc: id_token is not a JWS")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("oidc: bad id_token header")
	}
	var header idHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return nil, errors.New("oidc: bad id_token header")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("oidc: bad id_token signature")
	}

	signingInput := []byte(parts[0] + "." + parts[1])
	key, err := fetchSigningKey(ctx, client, jwksURI, header)
	if err != nil {
		return nil, err
	}
	if err := verifyRSASignature(header.Algorithm, key, signingInput, sig); err != nil {
		return nil, err
	}

	payloadJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("oidc: bad id_token payload")
	}
	var claims idClaims
	if err := json.Unmarshal(payloadJSON, &claims); err != nil {
		return nil, errors.New("oidc: bad id_token claims")
	}

	// Claim validation.
	if strings.TrimRight(claims.Issuer, "/") != strings.TrimRight(expectedIssuer, "/") {
		return nil, fmt.Errorf("oidc: iss %q does not match %q", claims.Issuer, expectedIssuer)
	}
	if !audienceContains(claims.Audience, clientID) {
		return nil, fmt.Errorf("oidc: aud %v does not contain %q", claims.Audience, clientID)
	}
	exp := time.Unix(claims.ExpiresAt, 0)
	if now.Add(ClockSkew).Before(exp.Add(-24 * 365 * time.Hour)) {
		return nil, errors.New("oidc: id_token exp is absurdly far in the future")
	}
	if !now.Add(-ClockSkew).Before(exp) {
		return nil, errors.New("oidc: id_token expired")
	}
	if claims.IssuedAt != 0 {
		iat := time.Unix(claims.IssuedAt, 0)
		if iat.After(now.Add(ClockSkew)) {
			return nil, errors.New("oidc: id_token issued in the future")
		}
	}
	if expectedNonce != "" && claims.Nonce != expectedNonce {
		return nil, errors.New("oidc: nonce mismatch")
	}
	if strings.TrimSpace(claims.Email) == "" {
		return nil, errors.New("oidc: id_token has no email claim")
	}

	return &IDToken{
		Issuer:        claims.Issuer,
		Subject:       claims.Subject,
		Email:         strings.TrimSpace(claims.Email),
		EmailVerified: claims.EmailVerified,
		Name:          claims.Name,
		GivenName:     claims.GivenName,
		FamilyName:    claims.FamilyName,
		Audience:      claims.Audience,
		Nonce:         claims.Nonce,
		ExpiresAt:     exp,
		IssuedAt:      time.Unix(claims.IssuedAt, 0),
	}, nil
}

// fetchSigningKey looks up the JWK for the token's kid (or the first RSA
// signing key when no kid is present).
func fetchSigningKey(ctx context.Context, client *http.Client, jwksURI string, header idHeader) (*rsa.PublicKey, error) {
	if client == nil {
		client = DefaultHTTPClient()
	}
	if jwksURI == "" {
		return nil, errors.New("oidc: no jwks_uri to verify id_token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jwksURI, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oidc: jwks fetch failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("oidc: jwks returned %d", resp.StatusCode)
	}
	var set jwks
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, fmt.Errorf("oidc: bad jwks: %w", err)
	}
	for _, k := range set.Keys {
		if k.KeyType != "RSA" {
			continue
		}
		if header.KeyID != "" && k.KeyID != header.KeyID {
			continue
		}
		pub, err := jwkToRSA(k)
		if err != nil {
			continue
		}
		return pub, nil
	}
	return nil, errors.New("oidc: no matching RSA key in JWKS")
}

// jwkToRSA converts an RSA JWK to a public key.
func jwkToRSA(k jwkKey) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(k.N)
	if err != nil {
		return nil, err
	}
	eb, err := base64.RawURLEncoding.DecodeString(k.E)
	if err != nil {
		return nil, err
	}
	e := 0
	for _, b := range eb {
		e = e<<8 | int(b)
	}
	if e == 0 {
		return nil, errors.New("oidc: bad RSA exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}, nil
}

// verifyRSASignature checks an RSASSA-PKCS1-v1_5 signature for the RS*
// family. `none` and HS* are explicitly rejected — a symmetric or unsigned
// ID token is not proof of anything.
func verifyRSASignature(alg string, key *rsa.PublicKey, signingInput, sig []byte) error {
	var h hash.Hash
	var cryptoHash crypto.Hash
	switch alg {
	case "RS256":
		h, cryptoHash = sha256.New(), crypto.SHA256
	case "RS384":
		h, cryptoHash = sha512.New384(), crypto.SHA384
	case "RS512":
		h, cryptoHash = sha512.New(), crypto.SHA512
	case "none", "":
		return errors.New("oidc: unsigned id_token (alg none) rejected")
	default:
		return fmt.Errorf("oidc: unsupported id_token alg %q (only RS256/384/512)", alg)
	}
	h.Write(signingInput)
	if err := rsa.VerifyPKCS1v15(key, cryptoHash, h.Sum(nil), sig); err != nil {
		return errors.New("oidc: id_token signature invalid")
	}
	return nil
}

// audienceContains reports whether the aud list names the client id.
func audienceContains(aud []string, clientID string) bool {
	for _, a := range aud {
		if strings.TrimSpace(a) == clientID {
			return true
		}
	}
	return false
}
