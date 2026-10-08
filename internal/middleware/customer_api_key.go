package middleware

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// CustomerAPIKeyAuth authenticates the versioned developer API (/v1/…)
// with a customer-minted portal credential: the `htc_sk_…` secret the
// portal hands out exactly once (plan §33 Developer API, §34 key
// management). This is the customer half of the two-credential world —
// APIKeyAuth/SessionOrAPIKey speak `kg_live_` operator keys over admin
// routes, and deliberately never see these; one namespace cannot be
// mistaken for the other (see model.CustomerAPIKeySecretPrefix).
//
// The presented secret is looked up by SHA-256 hash — the exact recipe
// store.HashAPIKey and the creation path use — via
// FindCustomerAPIKeyByHash, whose SQL already excludes revoked and
// expired rows, so a dead key fails exactly like a wrong one and the
// endpoint is not a state oracle. UsableAt is re-checked here anyway:
// it costs nothing and a lookup that one day returns more than it
// should still cannot authenticate. A key whose owner no longer exists
// is refused the same way — one answer for every failure.
//
// On success the request carries the same identity context shape a
// session-authenticated request does ("user_id", "email", "name" — the
// keys SessionAuth uses) plus the credential itself ("customer_api_key",
// "customer_scopes") and a "auth_type" tag ("customer_api_key") so
// downstream code can tell the path apart without re-reading headers.
// "is_admin" is deliberately NOT set — a customer key must never pass
// AdminOnly(), and setting it "for symmetry" is how that bug arrives.
//
// The secret never enters a log line or an error message: every refusal
// answers with a fixed string, and nothing here logs at all.
type CustomerAPIKeyStore interface {
	// FindCustomerAPIKeyByHash resolves a presented secret's hash to a
	// USABLE key (revoked/expired rows excluded), or sql.ErrNoRows.
	FindCustomerAPIKeyByHash(ctx context.Context, keyHash string) (*model.CustomerAPIKey, error)
	// TouchCustomerAPIKeyLastUsed stamps last_used_at on a usable key.
	TouchCustomerAPIKeyLastUsed(ctx context.Context, id string) error
	// FindUserByID resolves the key's owner for the identity context.
	FindUserByID(ctx context.Context, id string) (*model.User, error)
}

// *store.Store is the production implementation. Asserted here so the
// wiring in main.go cannot compile against a drifted interface.
var _ CustomerAPIKeyStore = (*store.Store)(nil)

// CustomerScopeWildcard is the customer-key analogue of the admin
// system's `admin` wildcard scope (model.ScopeAdmin): a key that
// carries "*" passes every RequireCustomerScope gate. The operator
// name is not reused on purpose — "admin" means nothing a customer
// should hold, and the two scope vocabularies must not blur.
const CustomerScopeWildcard = "*"

// customerAPIKeyTouchInterval is how often a key is stamped
// last_used_at at most. The stamp is a write, and a credential used on
// every call of a polling client is a hot path; once a minute per key
// answers "when was this last used?" to any human resolution while
// keeping the write volume at one UPDATE per key per minute instead of
// one per request.
const customerAPIKeyTouchInterval = time.Minute

// customerAPIKeyTouchPurgeAt bounds the throttle map. When it grows
// past this many keys, entries whose window has passed are dropped —
// they can never suppress a touch again, so keeping them would only
// hold memory for credentials that stopped calling.
const customerAPIKeyTouchPurgeAt = 4096

// customerAPIKeyTouchThrottle decides whether a key is due for a
// last_used_at stamp. One instance lives in each middleware closure;
// several replicas throttle independently, which only makes the stamp
// slightly coarser — last_used_at is an operational hint, not a ledger.
type customerAPIKeyTouchThrottle struct {
	mu   sync.Mutex
	last map[string]time.Time
}

// due reports whether id should be stamped at now, and records the
// decision window if so. The first use of a key is always due.
func (t *customerAPIKeyTouchThrottle) due(id string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.last == nil {
		t.last = map[string]time.Time{}
	}
	if prev, ok := t.last[id]; ok && now.Sub(prev) < customerAPIKeyTouchInterval {
		return false
	}
	if len(t.last) >= customerAPIKeyTouchPurgeAt {
		for k, v := range t.last {
			if now.Sub(v) >= customerAPIKeyTouchInterval {
				delete(t.last, k)
			}
		}
	}
	t.last[id] = now
	return true
}

// CustomerAPIKeyAuth builds the /v1 authentication middleware. Wire it
// once per engine — the last-used throttle lives in the closure.
//
// Accepted presentations (first wins):
//
//	Authorization: Bearer htc_sk_…
//	X-API-Key: htc_sk_…            (tolerated for simple clients)
//
// Refusals: no credential presented → 401 "missing api key"; anything
// else that fails (wrong namespace, unknown, revoked, expired, owner
// gone) → 401 "invalid api key", byte-identical across causes so the
// response cannot be used to probe why a credential stopped working.
func CustomerAPIKeyAuth(s CustomerAPIKeyStore) gin.HandlerFunc {
	var touch customerAPIKeyTouchThrottle
	return func(c *gin.Context) {
		raw := extractCustomerAPIKeySecret(c)
		if raw == "" {
			abortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "missing api key")
			return
		}
		// Wrong namespace (a JWT, a kg_live_ operator key, anything
		// else) is just another way to present a credential that does
		// not authenticate here — same 401 as an unknown secret.
		if !strings.HasPrefix(raw, model.CustomerAPIKeySecretPrefix) {
			abortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "invalid api key")
			return
		}
		k, err := s.FindCustomerAPIKeyByHash(c.Request.Context(), store.HashAPIKey(raw))
		if err != nil || !k.UsableAt(time.Now()) {
			abortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "invalid api key")
			return
		}
		owner, err := s.FindUserByID(c.Request.Context(), k.UserID)
		if err != nil {
			abortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "invalid api key")
			return
		}
		// Stamp last_used_at at most once per key per window. The write
		// is best-effort: a failed stamp must never fail a request that
		// already authenticated, so the error is dropped on purpose.
		if touch.due(k.ID, time.Now()) {
			_ = s.TouchCustomerAPIKeyLastUsed(c.Request.Context(), k.ID)
		}

		// Identity context, same keys SessionAuth sets, so handlers
		// read one shape whatever authenticated the request.
		c.Set("user_id", owner.ID)
		c.Set("email", owner.Email)
		c.Set("name", owner.Name)
		c.Set("customer_api_key", k)
		c.Set("customer_scopes", k.ScopeList())
		c.Set("auth_type", "customer_api_key")
		c.Next()
	}
}

// extractCustomerAPIKeySecret reads the presented secret from the
// Authorization Bearer header, falling back to X-API-Key. Whitespace
// around the value is dropped (the secret alphabet cannot contain it);
// the value itself is never logged or echoed.
func extractCustomerAPIKeySecret(c *gin.Context) string {
	if raw := strings.TrimSpace(extractBearer(c)); raw != "" {
		return raw
	}
	return strings.TrimSpace(c.GetHeader("X-API-Key"))
}

// RequireCustomerScope gates one /v1 route on one scope. Behavior
// mirrors the admin RequireScope, minus the session branch (a customer
// key is the only credential this layer knows):
//
//   - key carries "*" (CustomerScopeWildcard) → pass.
//   - key carries the exact scope → pass.
//   - authenticated but missing the scope → 403 INSUFFICIENT_SCOPE.
//   - no customer key in context (middleware not run) → 401.
//
// Scope semantics for customer keys, decided here once:
//
//   - An EMPTY scope list is FAIL-CLOSED and grants nothing beyond
//     self-introspection: the same convention the operator keys carry
//     ("Empty scopes = the key can do nothing", model.APIKey) and the
//     one the CustomerAPIKey model documents ("carries no extra
//     permission"). A minted key does not silently become all-powerful
//     because its owner skipped the scopes field.
//   - Matching is an exact string match on one listed scope (the
//     caller passes the single scope the route needs).
//   - "*" is the wildcard, the customer-key mirror of the admin
//     system's `admin` wildcard.
//
// GET /v1/me is intentionally NOT gated with this: any authenticated
// key may introspect itself, which is how a customer checks what a
// key can do before granting it anything.
func RequireCustomerScope(scope string) gin.HandlerFunc {
	return func(c *gin.Context) {
		v, ok := c.Get("customer_api_key")
		if !ok {
			abortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
			return
		}
		k, ok := v.(*model.CustomerAPIKey)
		if !ok || k == nil {
			abortWithError(c, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
			return
		}
		if k.HasScope(CustomerScopeWildcard) || k.HasScope(scope) {
			c.Next()
			return
		}
		abortWithError(c, http.StatusForbidden, "INSUFFICIENT_SCOPE",
			"api_key is missing a required scope")
	}
}
