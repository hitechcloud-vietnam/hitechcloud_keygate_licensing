package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/sso"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ─── SCIM 2.0 bearer-token auth (Phase 8, slice 2) ───
//
// SCIMTokenAuth guards the SCIM /Users provisioning endpoints with the
// `htc_scim_…` bearer token a directory (Okta, Entra ID, …) presents. The
// token is looked up by SHA-256 hash — the exact recipe model.
// HashSCIMToken and the admin mint path use — via store.FindSCIMTokenByHash,
// whose SQL already excludes revoked rows, so a dead token fails exactly
// like a wrong one and the endpoint is not a state oracle. RevokedAt is
// re-checked here anyway: it costs nothing and a lookup that one day
// returned more than it should still cannot authenticate.
//
// # ONE ANSWER FOR EVERY FAILURE
//
// Missing header, wrong namespace (a JWT, a kg_live_ key, anything else),
// unknown token, and revoked token all answer the SAME 401 with a
// byte-identical SCIM error body. There is deliberately no "invalid" vs
// "revoked" vs "unknown" distinction — that would let a caller probe which
// tokens exist. last_used_at is stamped (best-effort) only on success.
//
// # SCIM ERROR SHAPE
//
// The refusal body is the SCIM error schema, not pkg/response — this
// middleware only ever sits in front of SCIM endpoints, and a SCIM client
// parses errors by that schema (see the note in internal/sso/scim_user.go).

// SCIMTokenStore is the slice of store.Store this middleware needs. The
// production implementation is *store.Store; tests substitute a fake.
type SCIMTokenStore interface {
	// FindSCIMTokenByHash resolves a presented token's hash to a USABLE
	// token (revoked rows excluded), or sql.ErrNoRows.
	FindSCIMTokenByHash(ctx context.Context, keyHash string) (*model.SCIMToken, error)
	// TouchSCIMTokenLastUsed stamps last_used_at on a usable token.
	TouchSCIMTokenLastUsed(ctx context.Context, id string) error
}

// *store.Store is the production implementation. Asserted here so wiring
// cannot compile against a drifted interface.
var _ SCIMTokenStore = (*store.Store)(nil)

// scimUnauthorized writes the single, identical 401 every refusal shares
// and aborts the chain. Detail is a fixed string — the token never appears
// in a log line or a message.
func scimUnauthorized(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, sso.NewSCIMError(http.StatusUnauthorized, "unauthorized"))
}

// SCIMTokenAuth authenticates a SCIM request. Wire it in front of the
// /scim/v2 group.
//
// Accepted presentation (only this):
//
//	Authorization: Bearer htc_scim_…
//
// On success the token row is available to handlers as "scim_token" (and
// its id as "scim_token_id"). No identity context ("user_id"/"email") and
// no "is_admin" are set — a SCIM token is a machine credential for the
// provisioning API and must never pass AdminOnly() or look like a user
// session.
func SCIMTokenAuth(s SCIMTokenStore) gin.HandlerFunc {
	return func(c *gin.Context) {
		raw := extractSCIMBearer(c)
		if raw == "" {
			scimUnauthorized(c)
			return
		}
		// Wrong namespace is just another way to present a credential that
		// does not authenticate here — same 401 as an unknown secret.
		if !strings.HasPrefix(raw, model.SCIMTokenPrefix) {
			scimUnauthorized(c)
			return
		}
		tok, err := s.FindSCIMTokenByHash(c.Request.Context(), model.HashSCIMToken(raw))
		if err != nil || tok == nil || tok.RevokedAt != nil {
			scimUnauthorized(c)
			return
		}
		// Stamp last_used_at. Best-effort: a failed stamp must never fail a
		// request that already authenticated.
		_ = s.TouchSCIMTokenLastUsed(c.Request.Context(), tok.ID)

		c.Set("scim_token", tok)
		c.Set("scim_token_id", tok.ID)
		c.Next()
	}
}

// extractSCIMBearer pulls the bearer secret from the Authorization header.
// Only `Bearer` is honoured (SCIM mandates it); no cookie, no X-API-Key
// fallback, so a session cookie can never authenticate as a SCIM token.
func extractSCIMBearer(c *gin.Context) string {
	h := c.GetHeader("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
