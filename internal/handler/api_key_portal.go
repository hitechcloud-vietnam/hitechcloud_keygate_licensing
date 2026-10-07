package handler

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// APIKeyPortalHandler exposes customer API key self-service (plan §29
// "API Keys", §34 key management).
//
//	GET    /api/v1/portal/api-keys      List the caller's own keys
//	POST   /api/v1/portal/api-keys      Mint a key (secret returned ONCE)
//	GET    /api/v1/portal/api-keys/:id  Get one of the caller's own keys
//	DELETE /api/v1/portal/api-keys/:id  Revoke (soft: sets revoked_at)
//
// The plaintext secret is returned exactly once, in the Create answer,
// and is never recoverable afterwards: the row keeps only key_prefix
// (display) and key_hash (SHA-256 hex). Get/List never echo it.
//
// Identity: middleware.SessionAuth (the /portal group's auth) puts the
// session user's id into the gin context under "user_id" — the same
// key the portal profile route and adminID read. Every lookup here is
// scoped to that id, and a key that belongs to someone else answers
// the same 404 as a key that does not exist, so these endpoints are
// not an existence oracle (no IDOR).
type APIKeyPortalHandler struct {
	store *store.Store
}

// NewAPIKeyPortalHandler wires the handler to the store.
func NewAPIKeyPortalHandler(s *store.Store) *APIKeyPortalHandler {
	return &APIKeyPortalHandler{store: s}
}

// portalUserID reads the session identity SessionAuth left in the
// context ("user_id"). Empty means no authenticated user reached the
// handler (routes are meant to sit behind SessionAuth; this is the
// belt to that suspenders).
func portalUserID(c *gin.Context) string {
	v, _ := c.Get("user_id")
	s, _ := v.(string)
	return s
}

// resolveOwnedKey loads the key at :id and verifies the session user
// owns it. Returns nil-without-writing on success; on failure the
// response is already written. "Not yours" and "does not exist" are
// deliberately the same 404.
func (h *APIKeyPortalHandler) resolveOwnedKey(c *gin.Context) (*model.CustomerAPIKey, bool) {
	userID := portalUserID(c)
	if userID == "" {
		response.Unauthorized(c, "unauthorized")
		return nil, false
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		response.BadRequest(c, "id is required in the URL path")
		return nil, false
	}
	k, err := h.store.FindCustomerAPIKeyByID(c.Request.Context(), id)
	if err != nil || k.UserID != userID {
		response.NotFound(c, "api key not found")
		return nil, false
	}
	return k, true
}

// normalizeCustomerAPIKey folds what the portal form sent into the
// shape that gets stored — the one place create validates, so the row
// and the refusal cannot drift apart. Both create and any future edit
// go through here.
func normalizeCustomerAPIKey(k *model.CustomerAPIKey, now time.Time) error {
	k.Name = strings.TrimSpace(k.Name)
	if err := apperr.ValidateName("name", k.Name); err != nil {
		return err
	}
	// Scopes fold to a clean comma list: trimmed entries, empties
	// dropped. " a , b ,, " and "a,b" must not become two spellings of
	// the same permission set. (ScopeList is computed from the old
	// value before the assignment lands.)
	k.Scopes = strings.Join(k.ScopeList(), ",")
	if k.ExpiresAt != nil && !k.ExpiresAt.After(now) {
		return apperr.BadRequest("expires_at must be in the future")
	}
	return nil
}

// List answers GET /api/v1/portal/api-keys: the caller's own keys,
// newest first. The JSON of a row never carries the secret or its
// hash (see the model's json:"-" field) — only key_prefix to tell the
// rows apart.
func (h *APIKeyPortalHandler) List(c *gin.Context) {
	userID := portalUserID(c)
	if userID == "" {
		response.Unauthorized(c, "unauthorized")
		return
	}
	page := listPage(c)
	keys, total, err := h.store.ListCustomerAPIKeysByUser(c.Request.Context(), userID, page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "api_keys", keys, total, page)
}

// Create answers POST /api/v1/portal/api-keys: a fresh secret is
// generated, only its prefix and SHA-256 hash are stored, and the
// secret is returned in this one response. There is no endpoint,
// admin or otherwise, that can show it again.
//
// Body: { name (required), scopes? (comma-separated), expires_at? }
func (h *APIKeyPortalHandler) Create(c *gin.Context) {
	userID := portalUserID(c)
	if userID == "" {
		response.Unauthorized(c, "unauthorized")
		return
	}
	var req struct {
		Name      string     `json:"name" binding:"required"`
		Scopes    string     `json:"scopes"`
		ExpiresAt *time.Time `json:"expires_at"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "name is required")
		return
	}
	ak := &model.CustomerAPIKey{
		UserID:    userID,
		Name:      req.Name,
		Scopes:    req.Scopes,
		ExpiresAt: req.ExpiresAt,
	}
	if err := normalizeCustomerAPIKey(ak, time.Now()); err != nil {
		writeAppErr(c, err)
		return
	}
	secret, err := model.NewCustomerAPIKeySecret()
	if err != nil {
		response.Internal(c, err)
		return
	}
	if err := h.store.CreateCustomerAPIKey(c.Request.Context(), ak, secret); err != nil {
		response.Internal(c, err)
		return
	}
	// API key creation/revocation is a security-sensitive operation
	// (plan §37) and always audited. The secret is never in Changes.
	h.store.Audit(c.Request.Context(), &model.AuditLog{
		Entity: "api_key", EntityID: ak.ID, Action: "created",
		ActorType: "portal_user", ActorID: userID, IPAddress: c.ClientIP(),
		Changes: map[string]any{"name": ak.Name, "scopes": ak.Scopes},
	})
	response.Created(c, gin.H{
		"api_key": ak,
		"secret":  secret,
		"note":    "store this secret now — it cannot be shown again",
	})
}

// Get answers GET /api/v1/portal/api-keys/:id — one of the caller's
// own keys, secret-free.
func (h *APIKeyPortalHandler) Get(c *gin.Context) {
	k, ok := h.resolveOwnedKey(c)
	if !ok {
		return
	}
	response.OK(c, k)
}

// Revoke answers DELETE /api/v1/portal/api-keys/:id: a SOFT revoke —
// revoked_at is stamped, the row stays so the audit trail survives
// (created/last-used/revoked), and the key stops authenticating at
// once. Idempotent: revoking an already-revoked key answers 200 with
// the same row.
func (h *APIKeyPortalHandler) Revoke(c *gin.Context) {
	k, ok := h.resolveOwnedKey(c)
	if !ok {
		return
	}
	if k.RevokedAt == nil {
		if err := h.store.RevokeCustomerAPIKey(c.Request.Context(), k.ID); err != nil {
			response.Internal(c, err)
			return
		}
		h.store.Audit(c.Request.Context(), &model.AuditLog{
			Entity: "api_key", EntityID: k.ID, Action: "revoked",
			ActorType: "portal_user", ActorID: k.UserID, IPAddress: c.ClientIP(),
		})
	}
	updated, err := h.store.FindCustomerAPIKeyByID(c.Request.Context(), k.ID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, updated)
}
