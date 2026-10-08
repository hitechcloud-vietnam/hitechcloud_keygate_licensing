package handler

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// ─── Enterprise SSO + SCIM groundwork (Phase 8, slice 1) ───
//
// Admin CRUD for SSO connection configuration (SAML + OIDC) and SCIM
// provisioning tokens. This slice is CONFIGURATION + TOKENS ONLY — the
// SAML handshake, the OIDC authorization-code flow and the SCIM 2.0
// /Users + /Groups sync that will READ these rows are future work and
// deliberately have NO routes here (see model/sso.go for the seams).
//
//	GET    /admin/sso/connections             List connections (paging)
//	POST   /admin/sso/connections             Create one
//	GET    /admin/sso/connections/:id         Get one
//	PATCH  /admin/sso/connections/:id         Update (partial; merge/clear)
//	DELETE /admin/sso/connections/:id         Delete (hard)
//	POST   /admin/sso/connections/:id/enable  Enable
//	POST   /admin/sso/connections/:id/disable Disable
//
//	GET    /admin/sso/tokens                  List SCIM tokens (paging)
//	POST   /admin/sso/tokens                  Mint one (plaintext ONCE)
//	POST   /admin/sso/tokens/:id/revoke       Soft-revoke
//	DELETE /admin/sso/tokens/:id              Delete (hard)
//
// TOKEN SHOWN ONCE: POST /admin/sso/tokens returns the plaintext in the
// `token` field of this one response and never again. Every other read
// serialises the row, which carries only token_prefix + timestamps — the
// hash is json:"-" on the model and the plaintext is not a field at all.
// The plaintext is NEVER audited and NEVER logged.
//
// Identity: these sit behind the admin middleware (wired in main.go),
// which puts the acting admin's id where adminID reads it. There is no
// per-row ownership here — every connection/token is global config an
// admin manages.

// ssoAdminStore is the slice of store.Store this handler needs. The real
// constructor takes *store.Store; tests substitute a fake so the refusal
// paths — invalid provider config, name/domain conflicts, double revoke
// — run without a database.
type ssoAdminStore interface {
	CreateSSOConnection(ctx context.Context, conn *model.SSOConnection) error
	FindSSOConnectionByID(ctx context.Context, id string) (*model.SSOConnection, error)
	ListSSOConnections(ctx context.Context, p store.Page) ([]*model.SSOConnection, int, error)
	UpdateSSOConnection(ctx context.Context, conn *model.SSOConnection) error
	DeleteSSOConnection(ctx context.Context, id string) error
	SetSSOEnabled(ctx context.Context, id string, enabled bool) error

	CreateSCIMToken(ctx context.Context, tok *model.SCIMToken) (string, error)
	ListSCIMTokens(ctx context.Context, p store.Page) ([]*model.SCIMToken, int, error)
	RevokeSCIMToken(ctx context.Context, id string) error
	DeleteSCIMToken(ctx context.Context, id string) error

	Audit(ctx context.Context, log *model.AuditLog)
}

var _ ssoAdminStore = (*store.Store)(nil)

// SSOAdminHandler wires the admin endpoints to the store.
type SSOAdminHandler struct {
	store ssoAdminStore
}

// NewSSOAdminHandler wires the handler to the store. The caller
// (main.go) passes the concrete *store.Store; tests build the struct
// with a fake (see sso_admin_test.go).
func NewSSOAdminHandler(s *store.Store) *SSOAdminHandler {
	return &SSOAdminHandler{store: s}
}

// ssoNameMax is the length bound on a connection/token display name. It
// matches the migration's VARCHAR(100); enforced here so a too-long name
// is the caller's 400 rather than a database 500.
const ssoNameMax = 100

// validateSSOName checks a connection or token display name: trimmed,
// non-empty, at most ssoNameMax characters. It is apperr.ValidateName's
// job narrowed to the tighter 100-char bound these tables carry (the
// shared helper allows 200).
func validateSSOName(field, value string) *apperr.AppError {
	v := strings.TrimSpace(value)
	if v == "" {
		return apperr.BadRequest(field + " is required")
	}
	if len(v) > ssoNameMax {
		return apperr.BadRequest(field + " must be at most " + strconv.Itoa(ssoNameMax) + " characters")
	}
	return nil
}

// mergeSSOStr folds one optional provider setting into the row, applying
// the admin PATCH convention (mirrors applyBillingPatch's billingText):
//
//	absent (nil)      → keep the stored value (merge, not replace)
//	"" (or spaces)    → CLEAR: stored NULL
//	"a value"         → set, after trimming
//
// On create the destination starts nil for every field, so the same
// helper reads "not sent" and "sent empty" both as "not configured".
func mergeSSOStr(dst **string, v *string) {
	if v == nil {
		return
	}
	s := strings.TrimSpace(*v)
	if s == "" {
		*dst = nil
		return
	}
	*dst = &s
}

// writeSSOWriteErr answers the typed refusals a connection write can
// earn — a name or domain already taken (409), or a config the database
// would reject (400) — and reports whether it wrote the response. A raw
// unique violation is folded into the same 409s the sentinels answer, so
// a concurrent create that slipped past any read still reports the
// conflict rather than a 500.
func writeSSOWriteErr(c *gin.Context, err error) bool {
	switch {
	case store.IsSSONameConflict(err):
		response.Err(c, 409, "SSO_NAME_TAKEN", "sso connection name already exists")
	case store.IsSSODomainConflict(err):
		response.Err(c, 409, "SSO_DOMAIN_TAKEN", "sso connection domain already exists")
	case errors.Is(err, store.ErrSSOInvalidConfig):
		response.BadRequest(c, "invalid sso connection configuration")
	default:
		return false
	}
	return true
}

// List — GET /admin/sso/connections
//
// One page of connections, name order. A row never carries the OIDC
// client secret (model.OIDCClientSecret is json:"-").
func (h *SSOAdminHandler) List(c *gin.Context) {
	page := listPage(c)
	conns, total, err := h.store.ListSSOConnections(c.Request.Context(), page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "sso_connections", conns, total, page)
}

// Get — GET /admin/sso/connections/:id
func (h *SSOAdminHandler) Get(c *gin.Context) {
	conn, err := h.store.FindSSOConnectionByID(c.Request.Context(), strings.TrimSpace(c.Param("id")))
	if err != nil {
		response.NotFound(c, "sso connection not found")
		return
	}
	response.OK(c, conn)
}

// Create — POST /admin/sso/connections
//
// Body: name, provider_type (saml|oidc), domain, enabled?, and the
// provider's settings (saml_* for a saml connection, oidc_* for an oidc
// one). The domain is folded lowercase, the config validated against the
// provider type (a saml connection without a certificate is refused),
// and a duplicate name or domain answers 409.
func (h *SSOAdminHandler) Create(c *gin.Context) {
	var req struct {
		Name             string  `json:"name" binding:"required"`
		ProviderType     string  `json:"provider_type" binding:"required"`
		Domain           string  `json:"domain" binding:"required"`
		Enabled          *bool   `json:"enabled"`
		SAMLEntityID     *string `json:"saml_entity_id"`
		SAMLSSOURL       *string `json:"saml_sso_url"`
		SAMLCertificate  *string `json:"saml_certificate"`
		OIDCIssuer       *string `json:"oidc_issuer"`
		OIDCClientID     *string `json:"oidc_client_id"`
		OIDCClientSecret *string `json:"oidc_client_secret"`
		OIDCScopes       *string `json:"oidc_scopes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "name, provider_type, and domain are required")
		return
	}

	conn := &model.SSOConnection{
		Name:         strings.TrimSpace(req.Name),
		ProviderType: strings.TrimSpace(req.ProviderType),
		Domain:       strings.TrimSpace(req.Domain),
		// Enabled is on unless the request turns it off; a pointer,
		// because a JSON false is deliberate and must not be confused
		// with an omitted field.
		Enabled: true,
	}
	if req.Enabled != nil {
		conn.Enabled = *req.Enabled
	}
	// Provider settings: on create every destination starts nil, so
	// "not sent" and "sent empty" both read as "not configured".
	mergeSSOStr(&conn.SAMLEntityID, req.SAMLEntityID)
	mergeSSOStr(&conn.SAMLSSOURL, req.SAMLSSOURL)
	mergeSSOStr(&conn.SAMLCertificate, req.SAMLCertificate)
	mergeSSOStr(&conn.OIDCIssuer, req.OIDCIssuer)
	mergeSSOStr(&conn.OIDCClientID, req.OIDCClientID)
	mergeSSOStr(&conn.OIDCClientSecret, req.OIDCClientSecret)
	mergeSSOStr(&conn.OIDCScopes, req.OIDCScopes)

	if err := validateSSOName("name", conn.Name); err != nil {
		writeAppErr(c, err)
		return
	}
	domain, err := model.NormalizeSSODomain(conn.Domain)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	conn.Domain = domain
	// The provider-config rule (a saml connection needs a certificate,
	// an oidc one an issuer, …) — the same rule the migration's CHECK
	// holds. Refused here with the specific message so a 400 names the
	// missing field.
	if err := conn.Validate(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	if err := h.store.CreateSSOConnection(c.Request.Context(), conn); err != nil {
		if !writeSSOWriteErr(c, err) {
			response.Internal(c, err)
		}
		return
	}
	// SSO configuration is security-sensitive (it decides how people
	// sign in) and always audited. The OIDC client secret is never in
	// Changes.
	h.store.Audit(c.Request.Context(), &model.AuditLog{
		Entity: "sso_connection", EntityID: conn.ID, Action: "created",
		ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
		Changes: map[string]any{
			"name": conn.Name, "provider_type": conn.ProviderType, "domain": conn.Domain,
		},
	})
	response.Created(c, conn)
}

// ssoPatch is the body of PATCH /admin/sso/connections/:id. Every field
// is a pointer so the wire distinguishes three intents — absent (keep),
// "" (clear a nullable field), a value (set). name/domain/provider_type
// are required-in-practice: clearing any of them leaves a connection that
// could not have been created, so the validation below refuses that. The
// enabled toggle is deliberately NOT here — it has its own endpoints.
type ssoPatch struct {
	Name             *string `json:"name"`
	ProviderType     *string `json:"provider_type"`
	Domain           *string `json:"domain"`
	SAMLEntityID     *string `json:"saml_entity_id"`
	SAMLSSOURL       *string `json:"saml_sso_url"`
	SAMLCertificate  *string `json:"saml_certificate"`
	OIDCIssuer       *string `json:"oidc_issuer"`
	OIDCClientID     *string `json:"oidc_client_id"`
	OIDCClientSecret *string `json:"oidc_client_secret"`
	OIDCScopes       *string `json:"oidc_scopes"`
}

// applySSOPatch merges the patch into the row and validates what it sets.
// It returns a plain error whose message is safe to hand the caller — each
// is a 400 the admin can fix. The merge convention is mergeSSOStr's (and
// the required name/domain/provider_type refuse being cleared).
func applySSOPatch(conn *model.SSOConnection, p *ssoPatch) error {
	if p.Name != nil {
		if err := validateSSOName("name", *p.Name); err != nil {
			return errors.New(err.Message)
		}
		conn.Name = strings.TrimSpace(*p.Name)
	}
	if p.ProviderType != nil {
		conn.ProviderType = strings.TrimSpace(*p.ProviderType)
	}
	if p.Domain != nil {
		conn.Domain = strings.TrimSpace(*p.Domain)
	}
	mergeSSOStr(&conn.SAMLEntityID, p.SAMLEntityID)
	mergeSSOStr(&conn.SAMLSSOURL, p.SAMLSSOURL)
	mergeSSOStr(&conn.SAMLCertificate, p.SAMLCertificate)
	mergeSSOStr(&conn.OIDCIssuer, p.OIDCIssuer)
	mergeSSOStr(&conn.OIDCClientID, p.OIDCClientID)
	mergeSSOStr(&conn.OIDCClientSecret, p.OIDCClientSecret)
	mergeSSOStr(&conn.OIDCScopes, p.OIDCScopes)
	return nil
}

// Update — PATCH /admin/sso/connections/:id
//
// Merges the patch into the row read first (absent keeps, "" clears a
// nullable field, a value sets) and writes back the config columns. The
// domain is re-folded and the merged config re-validated, so an edit
// cannot put a connection into a state that could not have been created.
// A rename onto a taken name or domain answers 409.
func (h *SSOAdminHandler) Update(c *gin.Context) {
	conn, err := h.store.FindSSOConnectionByID(c.Request.Context(), strings.TrimSpace(c.Param("id")))
	if err != nil {
		response.NotFound(c, "sso connection not found")
		return
	}
	var p ssoPatch
	if err := c.ShouldBindJSON(&p); err != nil {
		response.BadRequest(c, "invalid request")
		return
	}
	if err := applySSOPatch(conn, &p); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	domain, err := model.NormalizeSSODomain(conn.Domain)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	conn.Domain = domain
	if err := conn.Validate(); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err := h.store.UpdateSSOConnection(c.Request.Context(), conn); err != nil {
		if !writeSSOWriteErr(c, err) {
			response.Internal(c, err)
		}
		return
	}
	h.store.Audit(c.Request.Context(), &model.AuditLog{
		Entity: "sso_connection", EntityID: conn.ID, Action: "updated",
		ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
	})
	response.OK(c, conn)
}

// Delete — DELETE /admin/sso/connections/:id (hard delete).
func (h *SSOAdminHandler) Delete(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if err := h.store.DeleteSSOConnection(c.Request.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.NotFound(c, "sso connection not found")
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c.Request.Context(), &model.AuditLog{
		Entity: "sso_connection", EntityID: id, Action: "deleted",
		ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
	})
	response.NoContent(c)
}

// Enable / Disable — POST /admin/sso/connections/:id/enable|disable
//
// The ONLY way to flip enabled (the general update path never writes that
// column), so a config edit cannot clobber a concurrent toggle. The two
// share one implementation; the flag is what differs.
func (h *SSOAdminHandler) Enable(c *gin.Context)  { h.setEnabled(c, true) }
func (h *SSOAdminHandler) Disable(c *gin.Context) { h.setEnabled(c, false) }

func (h *SSOAdminHandler) setEnabled(c *gin.Context, enabled bool) {
	id := strings.TrimSpace(c.Param("id"))
	if err := h.store.SetSSOEnabled(c.Request.Context(), id, enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.NotFound(c, "sso connection not found")
			return
		}
		response.Internal(c, err)
		return
	}
	action := "enabled"
	if !enabled {
		action = "disabled"
	}
	h.store.Audit(c.Request.Context(), &model.AuditLog{
		Entity: "sso_connection", EntityID: id, Action: action,
		ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
	})
	response.OK(c, gin.H{"id": id, "enabled": enabled})
}

// ─── SCIM tokens ───

// ListTokens — GET /admin/sso/tokens
//
// One page of tokens newest first. A row never carries the hash or the
// plaintext — only token_prefix to tell the rows apart (see the model's
// json tags).
func (h *SSOAdminHandler) ListTokens(c *gin.Context) {
	page := listPage(c)
	toks, total, err := h.store.ListSCIMTokens(c.Request.Context(), page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "scim_tokens", toks, total, page)
}

// CreateToken — POST /admin/sso/tokens
//
// Mints a fresh token: the plaintext is generated inside the store, only
// its prefix and SHA-256 hash are stored, and the plaintext is returned
// in this one response's `token` field. There is no endpoint, admin or
// otherwise, that can show it again.
//
// Body: { name (required) }
func (h *SSOAdminHandler) CreateToken(c *gin.Context) {
	var req struct {
		Name string `json:"name" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "name is required")
		return
	}
	tok := &model.SCIMToken{Name: strings.TrimSpace(req.Name)}
	if err := validateSSOName("name", tok.Name); err != nil {
		writeAppErr(c, err)
		return
	}
	plaintext, err := h.store.CreateSCIMToken(c.Request.Context(), tok)
	if err != nil {
		response.Internal(c, err)
		return
	}
	// Token creation is a security-sensitive operation and always
	// audited. The plaintext is NEVER in Changes — only the name and the
	// display prefix.
	h.store.Audit(c.Request.Context(), &model.AuditLog{
		Entity: "scim_token", EntityID: tok.ID, Action: "created",
		ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
		Changes: map[string]any{"name": tok.Name, "token_prefix": tok.TokenPrefix},
	})
	response.Created(c, gin.H{"scim_token": tok, "token": plaintext})
}

// RevokeToken — POST /admin/sso/tokens/:id/revoke
//
// Soft-revokes (revoked_at = now()), so the row keeps its audit trail.
// Revoking an already-revoked token is refused 409 (it was already dead,
// and a silent no-op that looks like a fresh action would mislead).
func (h *SSOAdminHandler) RevokeToken(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if err := h.store.RevokeSCIMToken(c.Request.Context(), id); err != nil {
		switch {
		case errors.Is(err, sql.ErrNoRows):
			response.NotFound(c, "scim token not found")
		case errors.Is(err, store.ErrSCIMTokenRevoked):
			response.Err(c, 409, "SCIM_TOKEN_REVOKED", "scim token is already revoked")
		default:
			response.Internal(c, err)
		}
		return
	}
	h.store.Audit(c.Request.Context(), &model.AuditLog{
		Entity: "scim_token", EntityID: id, Action: "revoked",
		ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
	})
	response.OK(c, gin.H{"id": id, "revoked": true})
}

// DeleteToken — DELETE /admin/sso/tokens/:id (hard delete).
//
// Hard delete, NOT soft revoke (that is RevokeToken): the two are
// distinct operations with distinct audit actions ("deleted" vs
// "revoked"). Delete drops the row and its audit trail in this table
// entirely; revoke keeps it.
func (h *SSOAdminHandler) DeleteToken(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if err := h.store.DeleteSCIMToken(c.Request.Context(), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.NotFound(c, "scim token not found")
			return
		}
		response.Internal(c, err)
		return
	}
	h.store.Audit(c.Request.Context(), &model.AuditLog{
		Entity: "scim_token", EntityID: id, Action: "deleted",
		ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
	})
	response.NoContent(c)
}
