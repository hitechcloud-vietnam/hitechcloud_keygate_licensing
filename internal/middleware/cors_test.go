package middleware

// §CORS for the domain split — the SPA now calls the API cross-origin
// (customer.example.com → verify.example.com). The contract being
// pinned:
//
//   - A configured surface host, the base domain, the BaseURL host and
//     the request's own host are allowed; credentials come along and
//     the origin is echoed verbatim (never "*").
//   - Any other origin gets no CORS headers on real requests and a
//     403 FORBIDDEN envelope on preflight — the inline block's
//     production behavior, kept.
//   - Host matching is port-stripped and case-insensitive.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/surface"
)

func surfCORSEngine(cfg surface.Config, baseURL string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	mw := CORS(func() surface.Config { return cfg }, baseURL)
	ok := func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) }
	r.GET("/api/v1/config", mw, ok)
	r.OPTIONS("/api/v1/config", mw, ok)
	return r
}

func surfCORSDo(r *gin.Engine, method, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://app.internal:9000/api/v1/config", nil)
	req.Host = "app.internal:9000"
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCORSAllowsSurfaceHostsWithCredentials(t *testing.T) {
	cfg := surface.FromSettings(func(key string) string {
		if key == surface.SettingDomainBase {
			return "example.com"
		}
		return ""
	})
	r := surfCORSEngine(cfg, "https://example.com")

	for _, origin := range []string{
		"https://customer.example.com",
		"https://verify.example.com",
		"https://example.com",               // apex / base domain
		"https://CUSTOMER.example.com:8443", // case + port stripped
		"http://localhost:5173",             // vite dev server
		"http://127.0.0.1:5173",
	} {
		w := surfCORSDo(r, "GET", origin)
		if w.Code != http.StatusOK {
			t.Errorf("%s: want 200, got %d", origin, w.Code)
		}
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != origin {
			t.Errorf("%s: Allow-Origin = %q, want the origin echoed verbatim", origin, got)
		}
		if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("%s: Allow-Credentials = %q, want true (session cookie rides along)", origin, got)
		}
		if v := w.Header().Values("Vary"); !surfContainsString(v, "Origin") {
			t.Errorf("%s: Vary must include Origin, got %v", origin, v)
		}
	}

	// Preflight answers 204 with methods/headers the SPA may use.
	w := surfCORSDo(r, "OPTIONS", "https://customer.example.com")
	if w.Code != http.StatusNoContent {
		t.Errorf("preflight: want 204, got %d", w.Code)
	}
	for _, want := range []string{"GET,POST,PUT,PATCH,DELETE,OPTIONS", "Authorization,Content-Type,Idempotency-Key"} {
		if !strings.Contains(w.Header().Get("Access-Control-Allow-Methods")+w.Header().Get("Access-Control-Allow-Headers"), want) {
			t.Errorf("preflight missing %q", want)
		}
	}
	if w.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Error("preflight must allow credentials")
	}
}

func TestCORSAllowsBaseURLHostForSingleHostInstalls(t *testing.T) {
	// No surface config at all — today's install: the BaseURL origin
	// keeps working exactly like the inline block did.
	r := surfCORSEngine(surface.Config{}, "https://hitechcloud.example.com")
	w := surfCORSDo(r, "GET", "https://hitechcloud.example.com")
	if w.Header().Get("Access-Control-Allow-Origin") != "https://hitechcloud.example.com" {
		t.Errorf("BaseURL origin must stay allowed, got headers %v", w.Header())
	}
}

func TestCORSRefusesForeignOrigins(t *testing.T) {
	cfg := surface.FromSettings(func(key string) string {
		if key == surface.SettingDomainBase {
			return "example.com"
		}
		return ""
	})
	r := surfCORSEngine(cfg, "https://example.com")

	for _, origin := range []string{"https://evil.com", "https://example.com.evil.com", "null", "https://notexample.com"} {
		w := surfCORSDo(r, "GET", origin)
		if w.Code != http.StatusOK {
			t.Errorf("%s: a real request passes through for the 404/200 decision, got %d", origin, w.Code)
		}
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s: must get no Allow-Origin, got %q", origin, got)
		}
	}

	// Preflight is refused with the house envelope.
	w := surfCORSDo(r, "OPTIONS", "https://evil.com")
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"code":"FORBIDDEN"`) {
		t.Errorf("foreign preflight: want 403 FORBIDDEN envelope, got %d\n%s", w.Code, w.Body.String())
	}
}

func TestCORSNoOriginHeaderIsUntouched(t *testing.T) {
	r := surfCORSEngine(surface.Config{}, "https://example.com")
	w := surfCORSDo(r, "GET", "")
	if w.Code != http.StatusOK {
		t.Errorf("want 200, got %d", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("no Origin must emit no CORS headers, got %q", got)
	}
}

func surfContainsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
