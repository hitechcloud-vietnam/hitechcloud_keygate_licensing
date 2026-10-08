package handler

import (
	"context"
	"database/sql"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/config"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// The Secure attribute has to follow the connection the browser
// actually used, not the environment name. A Secure cookie handed out
// over plain HTTP is dropped by the browser without a word: the login
// answers 200 and the very next request 401s (issue #25). The reverse
// matters too — an HTTPS install must keep the attribute whatever
// ENVIRONMENT says.
func TestSessionCookieSecureFollowsTheRequestScheme(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name       string
		env        string
		baseURL    string
		tls        bool
		forwarded  string
		wantSecure bool
	}{
		{"plain http, production", "production", "http://box.local:9000", false, "", false},
		{"plain http, development", "development", "http://localhost:9000", false, "", false},
		{"https, production", "production", "https://hitechcloud.example.com", true, "", true},
		{"https, staging", "staging", "https://hitechcloud.example.com", true, "", true},
		{"behind a TLS proxy", "production", "http://hitechcloud.internal:9000", false, "https", true},
		{"proxy chain, browser used TLS", "production", "http://x:9000", false, "https, http", true},
		{"proxy chain, browser used http", "production", "http://x:9000", false, "http, https", false},
		{"https BASE_URL, silent proxy", "production", "https://hitechcloud.example.com", false, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &AuthHandler{Config: &config.Config{Environment: tc.env, BaseURL: tc.baseURL}}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			scheme := "http://"
			if tc.tls {
				scheme = "https://"
			}
			c.Request = httptest.NewRequest("POST", scheme+"host/api/v1/auth/otp/verify", nil)
			if tc.forwarded != "" {
				c.Request.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			setSecureCookie(c, "session", "jwt", 3600, "/", h.requestIsHTTPS(c), true)
			got := w.Header().Get("Set-Cookie")
			if secure := strings.Contains(got, "; Secure"); secure != tc.wantSecure {
				t.Fatalf("Secure=%v, want %v: %s", secure, tc.wantSecure, got)
			}
			// The rest of the cookie is not up for negotiation.
			for _, want := range []string{"HttpOnly", "SameSite=Lax", "Path=/"} {
				if !strings.Contains(got, want) {
					t.Fatalf("cookie lost %s: %s", want, got)
				}
			}
		})
	}
}

// Session sharing across subdomains (SSO): the session and refresh
// cookies roam the site's subdomains only when the session_cookie_domain
// setting says so. Unset — or unreadable, or garbage no browser would
// accept — must reproduce today's host-only cookies EXACTLY (a cookie
// silently dropped for a malformed Domain is a login that answers 200
// and a session that never sticks). The Domain is attached to both the
// set and the clear: a host-only clear cannot delete a cookie stored
// for .example.com.
func TestSessionCookieDomainSetAndUnset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name       string
		setting    string
		readErr    error
		wantDomain string
	}{
		{"set: leading dot", ".example.com", nil, "example.com"}, // Go drops the legacy leading dot on the wire
		{"set: bare domain", "example.com", nil, "example.com"},
		{"set: case and padding", " .Example.COM ", nil, "example.com"},
		{"unset: empty value", "", nil, ""},
		{"unset: missing row", ".example.com", sql.ErrNoRows, ""},
		{"unset: read failure", ".example.com", sql.ErrConnDone, ""},
		{"unset: garbage value", "not a domain; drop=it", nil, ""},
		{"unset: single label", "localhost", nil, ""},
		{"unset: double dot", "a..example.com", nil, ""},
		{"unset: trailing dot", "example.com.", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := &AuthHandler{
				Config: &config.Config{Environment: "development", BaseURL: "http://localhost:9000"},
				surfGetSetting: func(_ context.Context, key string) (string, error) {
					if key != surfCookieDomainSetting {
						t.Errorf("read unexpected setting key %q", key)
					}
					return tc.setting, tc.readErr
				},
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest("POST", "http://customer.example.com/api/v1/auth/otp/verify", nil)

			h.issueSessionCookie(c, &model.User{ID: "user_1", Email: "u@example.com", Name: "U"})
			h.setRefreshCookie(c, "raw-refresh", time.Now().Add(time.Hour))

			cookies := w.Header().Values("Set-Cookie")
			if len(cookies) != 2 {
				t.Fatalf("want session + refresh cookies, got %v", cookies)
			}
			for i, want := range []string{"Path=/;", "Path=/api/v1/auth/refresh;"} {
				if !strings.Contains(cookies[i], want) {
					t.Errorf("cookie %d lost %s: %s", i, want, cookies[i])
				}
			}
			for _, ck := range cookies {
				if tc.wantDomain == "" {
					if strings.Contains(ck, "Domain=") {
						t.Errorf("setting %q must stay host-only (no Domain): %s", tc.setting, ck)
					}
				} else if !strings.Contains(ck, "Domain="+tc.wantDomain) {
					t.Errorf("setting %q: want Domain=%s: %s", tc.setting, tc.wantDomain, ck)
				}
				// Secure/HttpOnly/SameSite are not up for negotiation —
				// the domain never changes them.
				for _, want := range []string{"HttpOnly", "SameSite=Lax"} {
					if !strings.Contains(ck, want) {
						t.Errorf("cookie lost %s: %s", want, ck)
					}
				}
			}
		})
	}
}

// Without the setting the cookie is byte-for-byte today's behavior:
// no Domain attribute at all, even when the Store is not wired
// (dev/tests) — the read is best-effort and fails open to host-only.
func TestSessionCookieDomainWithoutSettingOrStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AuthHandler{Config: &config.Config{Environment: "development", BaseURL: "http://localhost:9000"}}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "http://customer.example.com/api/v1/auth/otp/verify", nil)

	h.issueSessionCookie(c, &model.User{ID: "user_1", Email: "u@example.com", Name: "U"})
	got := w.Header().Get("Set-Cookie")
	if strings.Contains(got, "Domain=") {
		t.Errorf("no Store/no setting must stay host-only: %s", got)
	}
	for _, want := range []string{"HttpOnly", "SameSite=Lax", "Path=/"} {
		if !strings.Contains(got, want) {
			t.Errorf("cookie lost %s: %s", want, got)
		}
	}
}
