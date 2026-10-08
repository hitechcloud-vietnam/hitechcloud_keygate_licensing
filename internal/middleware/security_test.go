package middleware

// §61 SECURITY hardening — the middleware boundary. The contract being
// pinned is the ACTUAL behavior of this codebase:
//
//   - AdminOnly: no session identity → 401 UNAUTHORIZED; a session that
//     is not an admin → 403 FORBIDDEN "admin required" (even when the
//     JWT CLAIM says otherwise — the DB check is authoritative).
//   - Rate limiting: a client cannot rotate rate-limit buckets by
//     forging X-Forwarded-For; the header only counts from a trusted
//     proxy (the exact proxy set cmd/server/main.go configures).
//   - Idempotency: a cached response is scoped — one tenant's replay
//     can never be served to another.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

const secTestSecret = "sec-test-secret"

func secAdminEngine(check AdminChecker) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/admin/ping",
		SessionAuth(secTestSecret, check),
		AdminOnly(),
		func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"pong": true}) },
	)
	return r
}

func secGet(r *gin.Engine, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", "/admin/ping", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestAdminOnly_NoSessionIs401 pins the unauthenticated contract on
// /admin/*: 401 UNAUTHORIZED, never a silent 200 and never a 403 (a
// 403 would confirm the route exists to an anonymous prober).
func TestAdminOnly_NoSessionIs401(t *testing.T) {
	r := secAdminEngine(func(context.Context, string) bool { return false })
	w := secGet(r, "")
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"code":"UNAUTHORIZED"`) {
		t.Errorf("want 401 UNAUTHORIZED, got %d\n%s", w.Code, w.Body.String())
	}
}

// TestAdminOnly_NonAdminSessionIs403Forbidden pins the privilege-
// escalation boundary: a VALID non-admin session on /admin/* gets
// 403 FORBIDDEN "admin required" — authenticated but unprivileged is
// 403, not 401 and not a bare 404.
func TestAdminOnly_NonAdminSessionIs403Forbidden(t *testing.T) {
	r := secAdminEngine(func(context.Context, string) bool { return false })
	token, err := IssueJWT(secTestSecret, "user_1", "user@example.com", "User", false, time.Minute)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	w := secGet(r, token)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"FORBIDDEN"`) {
		t.Errorf("want 403 FORBIDDEN, got %d\n%s", w.Code, w.Body.String())
	}
}

// TestAdminOnly_ForgedAdminClaimWithoutDBAdminIs403 pins that admin
// state is DB-authoritative: a token CLAIMING is_admin — forged, stale,
// or from before a demotion — must NOT open /admin/* when the database
// says the user is not an admin. The JWT claim is a display fallback,
// never the authorization decision.
func TestAdminOnly_ForgedAdminClaimWithoutDBAdminIs403(t *testing.T) {
	r := secAdminEngine(func(context.Context, string) bool { return false })
	token, err := IssueJWT(secTestSecret, "user_1", "user@example.com", "User", true /* forged admin claim */, time.Minute)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	w := secGet(r, token)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"FORBIDDEN"`) {
		t.Errorf("a forged admin claim must not escalate: got %d\n%s", w.Code, w.Body.String())
	}

	// Control: the same shape with a DB admin is allowed through, so the
	// 403 above is the privilege check and not a blanket block.
	rOK := secAdminEngine(func(context.Context, string) bool { return true })
	wOK := secGet(rOK, token)
	if wOK.Code != http.StatusOK {
		t.Errorf("a real admin must pass, got %d\n%s", wOK.Code, wOK.Body.String())
	}
}

// TestRateLimitXForwardedForCannotRotateBuckets pins the rate-limit
// bypass boundary. X-Forwarded-For is a client-controlled header: from
// an UNTRUSTED peer it must be ignored entirely (otherwise every client
// gets a fresh bucket per request), and from a trusted proxy it must
// name the client to its right — never the proxy's own peers.
//
// The trusted-proxy set below mirrors cmd/server/main.go's
// SetTrustedProxies exactly (RFC1918 + ULA); the pin documents the
// production dependency: widen the proxy set and these semantics hold,
// trust the world and bucket-rotation is back.
func TestRateLimitXForwardedForCannotRotateBuckets(t *testing.T) {
	SetRateLimitBackend(NewMemoryBackend())
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := r.SetTrustedProxies([]string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"}); err != nil {
		t.Fatalf("SetTrustedProxies: %v", err)
	}
	ok := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) }
	r.GET("/rl/direct", RateLimitByIPScoped("sec_xff_direct", 2, time.Minute), ok)
	r.GET("/rl/proxy", RateLimitByIPScoped("sec_xff_proxy", 2, time.Minute), ok)

	hit := func(path, remoteAddr, xff string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.RemoteAddr = remoteAddr
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// Direct client (untrusted peer): three DIFFERENT forged X-Forwarded-
	// For values must all land in the SAME bucket — the third hit is
	// refused. Rotating the header is not a rate-limit reset.
	for i, spoof := range []string{"1.2.3.4", "5.6.7.8", "9.9.9.9"} {
		w := hit("/rl/direct", "203.0.113.9:5555", spoof)
		if i < 2 && w.Code != http.StatusOK {
			t.Fatalf("hit %d with spoofed XFF %s: want 200, got %d", i+1, spoof, w.Code)
		}
		if i == 2 {
			if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), `"code":"RATE_LIMITED"`) {
				t.Errorf("3rd hit must be 429 RATE_LIMITED despite a fresh XFF, got %d\n%s", w.Code, w.Body.String())
			}
		}
	}

	// A different REAL client keeps its own allowance — the spoofing war
	// must not lock everyone into one shared bucket.
	if w := hit("/rl/direct", "203.0.113.10:1", "1.2.3.4"); w.Code != http.StatusOK {
		t.Errorf("a second real client must keep its own bucket, got %d", w.Code)
	}

	// Behind a TRUSTED proxy the X-Forwarded-For client is the bucket:
	// same client 3 hits → 429; a different client through the same
	// proxy → its own bucket.
	for i := 0; i < 3; i++ {
		w := hit("/rl/proxy", "10.0.0.5:443", "203.0.113.77")
		if i < 2 && w.Code != http.StatusOK {
			t.Fatalf("proxied hit %d: want 200, got %d", i+1, w.Code)
		}
		if i == 2 && w.Code != http.StatusTooManyRequests {
			t.Errorf("proxied 3rd hit must be 429, got %d", w.Code)
		}
	}
	if w := hit("/rl/proxy", "10.0.0.5:443", "203.0.113.78"); w.Code != http.StatusOK {
		t.Errorf("a second proxied client must keep its own bucket, got %d", w.Code)
	}
}

// TestIdempotencyCrossScopeReplayIsolation pins the replay boundary of
// the idempotency cache: a stored response is keyed by (scope, key) —
// so the same Idempotency-Key + body under a DIFFERENT scope (tenant,
// user, session — whatever the scope function resolves) must RUN the
// handler again instead of receiving another scope's cached response.
// A cross-scope replay would hand one tenant's signed response to
// another. (The 409 reuse/in-progress paths are already pinned in
// idempotency_test.go — this is the orthogonal axis.)
func TestIdempotencyCrossScopeReplayIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	st := newFakeIdemStore()
	runs := 0
	r := gin.New()
	r.POST("/t",
		Idempotency(func(c *gin.Context) string { return c.GetHeader("X-Scope") }, st),
		func(c *gin.Context) {
			runs++
			c.JSON(http.StatusOK, gin.H{"run": runs})
		},
	)

	post := func(scope, key, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/t", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("X-Scope", scope)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	w1 := post("tenant-a", "k1", `{"x":1}`)
	w2 := post("tenant-a", "k1", `{"x":1}`)
	w3 := post("tenant-b", "k1", `{"x":1}`)

	if w1.Code != http.StatusOK || !strings.Contains(w1.Body.String(), `"run":1`) {
		t.Fatalf("first call must run the handler: %d %s", w1.Code, w1.Body.String())
	}
	if w2.Header().Get("Idempotent-Replay") != "true" || !strings.Contains(w2.Body.String(), `"run":1`) {
		t.Errorf("same scope must replay the cached response: %s", w2.Body.String())
	}
	// The security property: tenant-b does NOT get tenant-a's cached
	// response — its request runs.
	if w3.Code != http.StatusOK || !strings.Contains(w3.Body.String(), `"run":2`) {
		t.Errorf("cross-scope must NOT serve another scope's cached response: %d %s", w3.Code, w3.Body.String())
	}
	if w3.Header().Get("Idempotent-Replay") == "true" {
		t.Error("a cross-scope response must not be marked as a replay")
	}
}
