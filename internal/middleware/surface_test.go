package middleware

// §SURFACE host enforcement — the routing boundary between the
// platform's public domains. The contract being pinned:
//
//   - Wrong host on an API-ish path (/api/, /scim/, webhook paths) is
//     a 404 NOT_FOUND envelope, and the handler never runs.
//   - Wrong host on a browser path is a 302 to the SAME path + query
//     on the canonical host of the first allowed surface, https when
//     the browser used TLS (directly or via X-Forwarded-Proto).
//   - The right host, an inactive config, and dev/unresolved hosts
//     (localhost, 127.0.0.1:9000) pass through untouched — local dev
//     and every single-host install keeps working.
//   - X-Forwarded-Host counts only from a trusted proxy (the exact
//     proxy set cmd/server/main.go configures); a direct client
//     forging it stays on the host it actually used.

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/surface"
)

// surfTestEngine registers the route shapes the real router has — an
// API path, a SCIM path, a browser path — behind one RequireSurface.
func surfTestEngine(cfg surface.Config, allowed []surface.Surface, downstream gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	if err := r.SetTrustedProxies([]string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"}); err != nil {
		panic(err)
	}
	mw := RequireSurface(func() surface.Config { return cfg }, allowed...)
	r.GET("/api/v1/checkout/quote", mw, downstream)
	r.GET("/scim/v2/Users", mw, downstream)
	r.GET("/pay/:id", mw, downstream)
	r.GET("/portal/licenses", mw, downstream)
	return r
}

func surfDownstream(reached *bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		*reached = true
		c.JSON(http.StatusOK, gin.H{"ok": true})
	}
}

func TestRequireSurfaceWrongHostAPIPathIs404Envelope(t *testing.T) {
	cfg := surface.FromSettings(func(key string) string {
		if key == surface.SettingDomainBase {
			return "example.com"
		}
		return ""
	})
	reached := false
	r := surfTestEngine(cfg, []surface.Surface{surface.SurfacePayments}, surfDownstream(&reached))

	for _, path := range []string{"/api/v1/checkout/quote", "/scim/v2/Users"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = "customer.example.com"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), `"code":"NOT_FOUND"`) {
			t.Errorf("%s on customer host: want 404 NOT_FOUND envelope, got %d\n%s", path, w.Code, w.Body.String())
		}
		if reached {
			t.Errorf("%s: handler must not run on the wrong host", path)
		}
		if loc := w.Header().Get("Location"); loc != "" {
			t.Errorf("%s: API paths must not redirect, got Location %q", path, loc)
		}
	}
}

func TestRequireSurfaceWrongHostBrowserRedirectsToCanonical(t *testing.T) {
	cfg := surface.FromSettings(func(key string) string {
		if key == surface.SettingDomainBase {
			return "example.com"
		}
		return ""
	})
	reached := false
	r := surfTestEngine(cfg, []surface.Surface{surface.SurfacePayments}, surfDownstream(&reached))

	for _, tc := range []struct {
		name    string
		target  string
		forward string
		tls     bool
		wantLoc string
	}{
		{"plain request", "/pay/abc?x=1&y=2", "", false, "http://payments.example.com/pay/abc?x=1&y=2"},
		{"TLS proxy", "/pay/abc?x=1", "https", false, "https://payments.example.com/pay/abc?x=1"},
		{"proxy chain, browser used TLS", "/pay/abc", "https, http", false, "https://payments.example.com/pay/abc"},
		{"direct TLS", "/pay/abc?q=a%20b", "", true, "https://payments.example.com/pay/abc?q=a%20b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.target, nil)
			req.Host = "customer.example.com"
			if tc.forward != "" {
				req.Header.Set("X-Forwarded-Proto", tc.forward)
			}
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusFound {
				t.Fatalf("want 302, got %d\n%s", w.Code, w.Body.String())
			}
			if loc := w.Header().Get("Location"); loc != tc.wantLoc {
				t.Errorf("Location = %q, want %q", loc, tc.wantLoc)
			}
			if reached {
				t.Error("handler must not run before the redirect")
			}
		})
	}
}

func TestRequireSurfaceRedirectAimsAtConfiguredHost(t *testing.T) {
	cfg := surface.FromSettings(func(key string) string {
		switch key {
		case surface.SettingDomainBase:
			return "example.com"
		case surface.DomainKey(surface.SurfacePayments):
			return "pay.example.com"
		}
		return ""
	})
	reached := false
	r := surfTestEngine(cfg, []surface.Surface{surface.SurfacePayments}, surfDownstream(&reached))

	req := httptest.NewRequest("GET", "/pay/abc?order=7", nil)
	req.Host = "example.com"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Fatalf("want 302, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "http://pay.example.com/pay/abc?order=7" {
		t.Errorf("Location = %q, want the configured host preserved with path+query", loc)
	}
}

func TestRequireSurfaceRightHostAndDevHostsPassThrough(t *testing.T) {
	cfg := surface.FromSettings(func(key string) string {
		if key == surface.SettingDomainBase {
			return "example.com"
		}
		return ""
	})
	for _, tc := range []struct {
		name string
		host string
		path string
	}{
		{"right host", "payments.example.com", "/pay/abc"},
		{"right host, case + port", "PAYMENTS.Example.COM:8443", "/api/v1/checkout/quote"},
		{"localhost dev", "localhost:9000", "/api/v1/checkout/quote"},
		{"loopback dev", "127.0.0.1:9000", "/pay/abc"},
		{"foreign host", "app.other.com", "/api/v1/checkout/quote"},
		{"empty host", "", "/pay/abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			r := surfTestEngine(cfg, []surface.Surface{surface.SurfacePayments}, surfDownstream(&reached))
			req := httptest.NewRequest("GET", tc.path, nil)
			req.Host = tc.host // httptest defaults Host to example.com; pin it explicitly ("" included)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != http.StatusOK || !reached {
				t.Errorf("want pass-through 200, got %d (reached=%v)\n%s", w.Code, reached, w.Body.String())
			}
		})
	}
}

func TestRequireSurfaceInactiveConfigNeverEnforces(t *testing.T) {
	reached := false
	r := surfTestEngine(surface.Config{}, []surface.Surface{surface.SurfacePayments}, surfDownstream(&reached))
	for _, path := range []string{"/api/v1/checkout/quote", "/pay/abc"} {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = "customer.example.com"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK || !reached {
			t.Errorf("inactive config: %s must pass through, got %d", path, w.Code)
		}
	}
}

func TestRequireSurfaceRedirectNeverLoops(t *testing.T) {
	// A hand-built config whose canonical host resolves to another
	// surface: redirecting there would bounce forever. Serve instead.
	cfg := surface.Config{Base: "example.com", Hosts: map[string]surface.Surface{}}
	reached := false
	r := surfTestEngine(cfg, []surface.Surface{surface.SurfacePayments}, surfDownstream(&reached))

	req := httptest.NewRequest("GET", "/pay/abc", nil)
	req.Host = "payments.example.com" // resolves to apex (suffix rule)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !reached {
		t.Errorf("canonical-host-on-self must pass through, got %d (reached=%v)", w.Code, reached)
	}
}

func TestRequireSurfaceXForwardedHostOnlyFromTrustedProxy(t *testing.T) {
	cfg := surface.FromSettings(func(key string) string {
		if key == surface.SettingDomainBase {
			return "example.com"
		}
		return ""
	})
	reached := false
	r := surfTestEngine(cfg, []surface.Surface{surface.SurfacePayments}, surfDownstream(&reached))

	// Behind a TRUSTED proxy (the proxy set from main.go) the forwarded
	// host is the one the browser used: a payments request lands on the
	// payments surface even though the proxy dialed the customer host.
	req := httptest.NewRequest("GET", "/api/v1/checkout/quote", nil)
	req.Host = "customer.example.com"
	req.RemoteAddr = "10.0.0.5:443"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("X-Forwarded-Host", "payments.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !reached {
		t.Errorf("trusted proxy X-Forwarded-Host must decide the surface, got %d\n%s", w.Code, w.Body.String())
	}

	// A DIRECT client forging the same headers stays on the host it
	// actually used — and gets the 404 it deserves.
	reached = false
	req = httptest.NewRequest("GET", "/api/v1/checkout/quote", nil)
	req.Host = "customer.example.com"
	req.RemoteAddr = "203.0.113.9:5555"
	req.Header.Set("X-Forwarded-For", "1.2.3.4")
	req.Header.Set("X-Forwarded-Host", "payments.example.com")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound || reached {
		t.Errorf("forged X-Forwarded-Host must be ignored, got %d (reached=%v)", w.Code, reached)
	}
}
