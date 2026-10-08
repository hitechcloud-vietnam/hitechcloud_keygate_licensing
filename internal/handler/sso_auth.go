package handler

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/config"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/middleware"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/sso"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// ─── Enterprise SSO login (Phase 8, slice 2) ───
//
// The browser-facing SAML + OIDC login endpoints. On success a user lands
// in a NORMAL portal session — the exact same JWT + refresh-token cookie
// shape the OTP login issues (see issueSession, which mirrors
// AuthHandler.issueSession primitive-for-primitive).
//
//	GET  /auth/sso/:id/start            start login (?domain=… OR :id)
//	POST /auth/sso/saml/acs             SAML Assertion Consumer Service
//	GET  /auth/sso/oidc/callback        OIDC code-flow callback
//
// Route wiring is the Lead's (cmd/server/main.go). This handler owns the
// redirects, the user provisioning seam and the session issuance.
//
// # DOMAIN GATE (security)
//
// A login is honoured ONLY when the asserted email's domain equals the
// connection's domain (sso.EmailMatchesDomain). An IdP configured for
// acme.com cannot mint a session for eve@evil.com. This is enforced AFTER
// the signature/token verification and BEFORE any user is created, so a
// rejected assertion never provisions an account.
//
// # SAML connection binding
//
// The connection is resolved from RelayState (see samlState below), NOT
// from a cookie: the IdP POSTs the SAMLResponse to the ACS as a cross-site
// form post, and a SameSite=Lax cookie is not sent on a cross-site POST.
// The return path inside RelayState is clamped to a local path (sso.
// ClampReturnPath) exactly as specified — so RelayState can never become an
// open redirect, and forging it to select a different connection still
// fails the signature check against that connection's certificate.

// ssoAuthStore is the slice of store.Store this handler needs. The real
// constructor takes *store.Store; tests substitute a fake.
type ssoAuthStore interface {
	FindSSOConnectionByID(ctx context.Context, id string) (*model.SSOConnection, error)
	FindSSOConnectionByDomain(ctx context.Context, domain string) (*model.SSOConnection, error)
	FindUserByEmail(ctx context.Context, email string) (*model.User, error)
	FindUserByID(ctx context.Context, id string) (*model.User, error)
	UpsertUser(ctx context.Context, u *model.User) error
	UpdateUserProfile(ctx context.Context, userID, name string) error
	SetUserRole(ctx context.Context, userID, role string) error
	CreateRefreshToken(ctx context.Context, userID, tokenHash string, expiresAt time.Time) error
	FindSCIMIdentityByUserID(ctx context.Context, userID string) (*store.SCIMIdentity, error)
	Audit(ctx context.Context, log *model.AuditLog)
}

var _ ssoAuthStore = (*store.Store)(nil)

// SSOAuthHandler wires the login endpoints to the store and the protocol
// logic.
type SSOAuthHandler struct {
	Store  ssoAuthStore
	Config *config.Config
	// HTTPClient talks to the OIDC provider (discovery, token, JWKS).
	// Nil means sso.DefaultHTTPClient.
	HTTPClient *http.Client
	// VerifySignature is the SAML signature seam; nil means
	// sso.DefaultSignatureVerifier (goxmldsig).
	VerifySignature sso.VerifySignatureFunc
	// Now is the clock seam for tests; nil means time.Now.
	Now func() time.Time
}

// NewSSOAuthHandler builds the handler against the real store.
func NewSSOAuthHandler(s *store.Store, cfg *config.Config) *SSOAuthHandler {
	return &SSOAuthHandler{Store: s, Config: cfg}
}

func (h *SSOAuthHandler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *SSOAuthHandler) httpClient() *http.Client {
	if h.HTTPClient != nil {
		return h.HTTPClient
	}
	return sso.DefaultHTTPClient()
}

func (h *SSOAuthHandler) verifier() sso.VerifySignatureFunc {
	if h.VerifySignature != nil {
		return h.VerifySignature
	}
	return sso.DefaultSignatureVerifier
}

// requestIsHTTPS reuses AuthHandler's TLS/proxy detection verbatim so the
// session cookies get the identical Secure attribute the OTP login sets.
func (h *SSOAuthHandler) requestIsHTTPS(c *gin.Context) bool {
	return (&AuthHandler{Config: h.Config}).requestIsHTTPS(c)
}

// ─── RelayState / OIDC state encoding ───

// samlState is what travels in the SAML RelayState. It carries the return
// path (clamped to a local path) plus the connection binding and the
// AuthnRequest id (for the optional InResponseTo correlation). See the
// "SAML connection binding" note above for why this is not a cookie.
type samlState struct {
	ConnID string `json:"c"`
	Return string `json:"r"`
	ReqID  string `json:"q"`
}

func encodeSAMLState(st samlState) string {
	b, _ := json.Marshal(st)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeSAMLState(s string) (*samlState, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	var st samlState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	if st.ConnID == "" {
		return nil, errors.New("sso: relay state missing connection")
	}
	return &st, nil
}

// oidcState is what travels in the short-lived HttpOnly OIDC state cookie.
type oidcState struct {
	State  string `json:"s"`
	Nonce  string `json:"n"`
	ConnID string `json:"c"`
	Return string `json:"r"`
}

const oidcStateCookie = "sso_oidc_state"

func encodeOIDCState(st oidcState) string {
	b, _ := json.Marshal(st)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeOIDCState(s string) (*oidcState, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	var st oidcState
	if err := json.Unmarshal(b, &st); err != nil {
		return nil, err
	}
	if st.State == "" || st.ConnID == "" {
		return nil, errors.New("sso: oidc state incomplete")
	}
	return &st, nil
}

// ─── GET /auth/sso/:id/start ───

// Start begins an SSO login. The connection is resolved by ?domain= when
// present, else by the :id path parameter. It dispatches on the
// connection's provider_type to the SAML or OIDC leg and 302s the browser
// to the identity provider.
//
// A missing, disabled or misconfigured connection answers 404 (folded, so
// the endpoint is not an oracle for which domains are configured).
func (h *SSOAuthHandler) Start(c *gin.Context) {
	conn, ok := h.resolveConnection(c)
	if !ok {
		return
	}
	ret := sso.ClampReturnPath(c.Query("return"))

	switch conn.ProviderType {
	case model.SSOProviderSAML:
		h.startSAML(c, conn, ret)
	case model.SSOProviderOIDC:
		h.startOIDC(c, conn, ret)
	default:
		response.BadRequest(c, "unsupported sso provider type")
	}
}

// resolveConnection finds the connection for a login, folding "not found",
// "disabled" and "invalid config" into a quiet 404.
func (h *SSOAuthHandler) resolveConnection(c *gin.Context) (*model.SSOConnection, bool) {
	var (
		conn *model.SSOConnection
		err  error
	)
	if domain := strings.TrimSpace(c.Query("domain")); domain != "" {
		conn, err = h.Store.FindSSOConnectionByDomain(c, domain)
	} else {
		conn, err = h.Store.FindSSOConnectionByID(c, c.Param("id"))
	}
	if err != nil || conn == nil || !conn.Enabled {
		response.NotFound(c, "not found")
		return nil, false
	}
	if err := conn.Validate(); err != nil {
		response.BadRequest(c, "sso connection is not configured for login")
		return nil, false
	}
	return conn, true
}

func (h *SSOAuthHandler) startSAML(c *gin.Context, conn *model.SSOConnection, ret string) {
	reqID := sso.NewSAMLID()
	relay := encodeSAMLState(samlState{ConnID: conn.ID, Return: ret, ReqID: reqID})
	spEntityID := sso.SPEntityID(h.Config.BaseURL)
	acsURL := sso.SAMLACSURL(h.Config.BaseURL)

	redirectURL, _, err := sso.BuildAuthnRedirect(
		deref(conn.SAMLSSOURL), spEntityID, acsURL, relay, reqID, h.now())
	if err != nil {
		response.Internal(c, err)
		return
	}
	c.Redirect(http.StatusFound, redirectURL)
}

func (h *SSOAuthHandler) startOIDC(c *gin.Context, conn *model.SSOConnection, ret string) {
	if conn.OIDCIssuer == nil || conn.OIDCClientID == nil {
		response.BadRequest(c, "sso connection is not configured for login")
		return
	}
	ctx := c.Request.Context()
	cfg, err := sso.DiscoverOIDC(ctx, h.httpClient(), *conn.OIDCIssuer)
	if err != nil {
		response.Internal(c, err)
		return
	}

	state := randomHex(16)
	nonce := randomHex(16)
	redirectURI := sso.OIDCCallbackURL(h.Config.BaseURL)
	scopes := ""
	if conn.OIDCScopes != nil {
		scopes = *conn.OIDCScopes
	}
	authURL, err := sso.BuildAuthorizeURL(cfg, *conn.OIDCClientID, redirectURI, scopes, state, nonce)
	if err != nil {
		response.Internal(c, err)
		return
	}

	// state + nonce in a short-lived HttpOnly cookie, bound to this
	// connection and the return path. Cleared-by-overwrite on callback.
	payload := encodeOIDCState(oidcState{State: state, Nonce: nonce, ConnID: conn.ID, Return: ret})
	setSecureCookie(c, oidcStateCookie, payload, 600, "/auth/sso", h.requestIsHTTPS(c), true)

	c.Redirect(http.StatusFound, authURL)
}

// ─── POST /auth/sso/saml/acs ───

// ACS is the SAML Assertion Consumer Service. It verifies the response,
// applies the domain gate, provisions/links the user and issues a portal
// session, then 302s to the clamped return path. Unsigned, tampered,
// wrong-issuer, wrong-audience and out-of-window assertions are REJECTED
// with 401.
func (h *SSOAuthHandler) ACS(c *gin.Context) {
	respB64 := strings.TrimSpace(c.PostForm("SAMLResponse"))
	if respB64 == "" {
		response.BadRequest(c, "missing SAMLResponse")
		return
	}
	state, err := decodeSAMLState(strings.TrimSpace(c.PostForm("RelayState")))
	if err != nil {
		response.BadRequest(c, "invalid RelayState")
		return
	}
	rawXML, err := base64.StdEncoding.DecodeString(respB64)
	if err != nil {
		response.BadRequest(c, "SAMLResponse is not valid base64")
		return
	}

	conn, err := h.Store.FindSSOConnectionByID(c, state.ConnID)
	if err != nil || conn == nil || !conn.Enabled || conn.ProviderType != model.SSOProviderSAML {
		response.NotFound(c, "not found")
		return
	}

	doc, err := sso.ParseSAMLResponse(rawXML)
	if err != nil {
		response.BadRequest(c, "invalid SAMLResponse")
		return
	}
	assertion, err := h.verifier()(rawXML, deref(conn.SAMLCertificate))
	if err != nil {
		response.Unauthorized(c, "saml signature verification failed")
		return
	}
	if err := sso.ValidateSAML(doc, assertion, deref(conn.SAMLEntityID),
		sso.SPEntityID(h.Config.BaseURL), h.now()); err != nil {
		response.Unauthorized(c, "saml assertion rejected")
		return
	}
	// InResponseTo correlation: if the assertion answers a request, it must
	// be the one this RelayState started.
	if doc.InResponseTo != "" && doc.InResponseTo != state.ReqID {
		response.Unauthorized(c, "saml response does not match the request")
		return
	}

	if !h.completeLogin(c, conn, assertion.Email, ssoDisplayName(assertion.GivenName, assertion.FamilyName), "saml") {
		return
	}
	c.Redirect(http.StatusFound, sso.ClampReturnPath(state.Return))
}

// ─── GET /auth/sso/oidc/callback ───

// Callback finishes the OIDC code flow: validates state, exchanges the code,
// verifies the ID token, applies the domain gate, provisions/links the user
// and issues a portal session, then 302s to the clamped return path.
func (h *SSOAuthHandler) Callback(c *gin.Context) {
	if e := c.Query("error"); e != "" {
		response.Unauthorized(c, "sso login failed")
		return
	}
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		response.BadRequest(c, "missing authorization code")
		return
	}
	cookie, err := c.Cookie(oidcStateCookie)
	if err != nil || cookie == "" {
		response.Unauthorized(c, "invalid sso state")
		return
	}
	state, err := decodeOIDCState(cookie)
	if err != nil {
		response.Unauthorized(c, "invalid sso state")
		return
	}
	// CSRF: the state echoed by the IdP must equal the one we stored.
	if subtle.ConstantTimeCompare([]byte(state.State), []byte(c.Query("state"))) != 1 {
		response.Unauthorized(c, "invalid sso state")
		return
	}

	conn, err := h.Store.FindSSOConnectionByID(c, state.ConnID)
	if err != nil || conn == nil || !conn.Enabled || conn.ProviderType != model.SSOProviderOIDC {
		response.NotFound(c, "not found")
		return
	}
	if conn.OIDCIssuer == nil || conn.OIDCClientID == nil {
		response.BadRequest(c, "sso connection is not configured for login")
		return
	}

	ctx := c.Request.Context()
	cfg, err := sso.DiscoverOIDC(ctx, h.httpClient(), *conn.OIDCIssuer)
	if err != nil {
		response.Internal(c, err)
		return
	}
	redirectURI := sso.OIDCCallbackURL(h.Config.BaseURL)
	secret := ""
	if conn.OIDCClientSecret != nil {
		secret = *conn.OIDCClientSecret
	}
	idRaw, err := sso.ExchangeCode(ctx, h.httpClient(), cfg, *conn.OIDCClientID, secret, code, redirectURI)
	if err != nil {
		response.Unauthorized(c, "sso token exchange failed")
		return
	}
	idTok, err := sso.VerifyIDToken(ctx, h.httpClient(), idRaw, cfg.JWKSURI,
		*conn.OIDCIssuer, *conn.OIDCClientID, state.Nonce, h.now())
	if err != nil {
		response.Unauthorized(c, "sso id token rejected")
		return
	}

	name := ssoDisplayName(idTok.GivenName, idTok.FamilyName)
	if name == "" {
		name = idTok.Name
	}
	if !h.completeLogin(c, conn, idTok.Email, name, "oidc") {
		return
	}
	// One-time: clear the state cookie.
	setSecureCookie(c, oidcStateCookie, "", -1, "/auth/sso", h.requestIsHTTPS(c), true)
	c.Redirect(http.StatusFound, sso.ClampReturnPath(state.Return))
}

// ─── shared login completion ───

// completeLogin runs the domain gate, find-or-create, SCIM deactivation
// gate, admin-email auto-promote, session issuance and audit. It answers
// the HTTP error itself and reports whether the caller may proceed to the
// success redirect.
func (h *SSOAuthHandler) completeLogin(c *gin.Context, conn *model.SSOConnection, email, name, actorType string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" || !sso.EmailMatchesDomain(email, conn.Domain) {
		response.Unauthorized(c, "email domain is not permitted for this connection")
		return false
	}

	user, err := h.Store.FindUserByEmail(c, email)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			response.Internal(c, err)
			return false
		}
		// First login for this email: create the account.
		u := &model.User{Email: email, Name: name}
		if uerr := h.Store.UpsertUser(c, u); uerr != nil {
			response.Internal(c, uerr)
			return false
		}
		if user, err = h.Store.FindUserByEmail(c, email); err != nil {
			response.Internal(c, err)
			return false
		}
	} else if name != "" && strings.TrimSpace(user.Name) == "" {
		// Fill in a blank display name on first SSO login; never overwrite
		// a name the user already set.
		_ = h.Store.UpdateUserProfile(c, user.ID, name)
		user.Name = name
	}

	// SCIM deactivation gate: a directory-deactivated identity may not sign
	// in. A user with NO scim_identity is not SCIM-managed and is allowed.
	if ident, ierr := h.Store.FindSCIMIdentityByUserID(c, user.ID); ierr == nil && ident != nil && !ident.Active {
		response.Unauthorized(c, "account is deactivated")
		return false
	}

	// Parity with the OTP login: an ADMIN_EMAILS address is promoted on the
	// way in.
	if h.Config.IsAdminEmail(user.Email) && user.Role == model.RoleUser {
		_ = h.Store.SetUserRole(c, user.ID, model.RoleAdmin)
		user.Role = model.RoleAdmin
	}

	h.issueSession(c, user)
	h.Store.Audit(c, &model.AuditLog{
		Entity: "session", EntityID: user.ID, Action: "login",
		ActorType: actorType, ActorID: user.ID, IPAddress: c.ClientIP(),
		Changes: map[string]any{"email": user.Email},
	})
	return true
}

// issueSession issues the NORMAL portal session: a 24-hour JWT in the
// `session` cookie plus a 30-day rotating refresh token in `refresh_token`,
// exactly mirroring AuthHandler.issueSession's primitives (IssueJWT +
// setSecureCookie + CreateRefreshToken + hashToken) so an SSO login and an
// OTP login are indistinguishable to every downstream handler.
func (h *SSOAuthHandler) issueSession(c *gin.Context, user *model.User) {
	token, _ := middleware.IssueJWT(
		h.Config.JWTSecret, user.ID, user.Email, user.Name,
		user.IsAdmin(), 24*time.Hour,
	)
	// Same session_cookie_domain setting as the OTP/password login so
	// an SSO session can roam the site's subdomains identically. The
	// store is an interface here — fakes without GetSetting keep the
	// host-only behaviour.
	dom := ""
	if gs, ok := h.Store.(interface {
		GetSetting(context.Context, string) (string, error)
	}); ok {
		dom = surfReadCookieDomain(c.Request.Context(), gs.GetSetting)
	}
	surfSetCookieDomain(c, "session", token, 24*3600, "/", h.requestIsHTTPS(c), true, dom)
	rawRefresh := randomHex(32)
	expiresAt := time.Now().Add(refreshTokenTTL)
	_ = h.Store.CreateRefreshToken(c, user.ID, hashToken(rawRefresh), expiresAt)
	surfSetCookieDomain(c, "refresh_token", rawRefresh,
		int(time.Until(expiresAt).Seconds()), "/api/v1/auth/refresh", h.requestIsHTTPS(c), true, dom)
}

// ssoDisplayName joins given + family name for the user's display name.
func ssoDisplayName(given, family string) string {
	return sso.JoinName(given, family)
}

// deref flattens an optional connection field to its value or "".
func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
