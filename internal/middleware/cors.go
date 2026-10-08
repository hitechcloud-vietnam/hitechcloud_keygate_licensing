package middleware

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/surface"
)

// CORS answers the cross-origin browser calls the domain split
// creates: the SPA on one surface host (customer.example.com) calling
// the API served on another (verify.example.com). It replaces the
// inline block in cmd/server/main.go, which only knows one origin
// (cfg.BaseURL) and would refuse every cross-surface call.
//
// The allowlist is the platform's own hosts: every configured surface
// host (surface.PublicMap — apex included), the base domain, the
// install's BaseURL host (today's single-host behavior), and the host
// of the request itself. Loopback origins (localhost, 127.0.0.1, ::1)
// are allowed too, for dev servers like vite on :5173. Everything
// else is refused exactly like before: no CORS headers on real
// requests (the browser blocks the read) and a 403 envelope on
// preflights. Credentials are allowed — the session cookie must ride
// along cross-origin, which is also why no wildcard origin is ever
// emitted.
func CORS(cfgProvider func() surface.Config, baseURL string) gin.HandlerFunc {
	baseHost := surfBaseHost(baseURL)
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin == "" {
			c.Next()
			return
		}
		if !surfOriginAllowed(surfOriginHost(origin), c.Request.Host, cfgProvider, baseHost) {
			if c.Request.Method == http.MethodOptions {
				// Envelope, not an empty 403 — a browser never reads a
				// preflight body, but anything else that gets here does,
				// and every other refusal in the API is shaped this way.
				abortWithError(c, http.StatusForbidden, "FORBIDDEN", "origin not allowed")
				return
			}
			// Proceed without CORS headers: the browser refuses the
			// read, same as the inline block's production branch.
			c.Next()
			return
		}
		c.Header("Access-Control-Allow-Origin", origin) // echo the origin — credentials forbid "*"
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Authorization,Content-Type,Idempotency-Key")
		c.Header("Access-Control-Expose-Headers", "X-Request-Id")
		c.Writer.Header().Add("Vary", "Origin")
		if c.Request.Method == http.MethodOptions {
			c.Header("Access-Control-Max-Age", "600")
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// surfOriginHost is the host of an Origin header — "" when it parses
// to nothing usable ("null", "file://", garbage).
func surfOriginHost(origin string) string {
	u, err := url.Parse(strings.TrimSpace(origin))
	if err != nil {
		return ""
	}
	return surface.NormalizeHost(u.Host)
}

// surfBaseHost is the host of the install's public BaseURL.
func surfBaseHost(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ""
	}
	return surface.NormalizeHost(u.Host)
}

// surfOriginAllowed is the allowlist: the platform's own surface hosts
// and base domain, the BaseURL host (legacy single-host installs), the
// request's own host (same-origin), and loopback for dev servers. Any
// other host is cross-origin to the platform and stays refused.
func surfOriginAllowed(originHost, reqHost string, cfgProvider func() surface.Config, baseHost string) bool {
	if originHost == "" {
		return false
	}
	switch originHost {
	case "localhost", "127.0.0.1", "::1":
		return true // dev servers; a real deployment has no pages on loopback
	}
	if originHost == baseHost || originHost == surface.NormalizeHost(reqHost) {
		return true
	}
	if cfgProvider == nil {
		return false
	}
	cfg := cfgProvider()
	if surface.NormalizeHost(cfg.Base) == originHost {
		return true
	}
	for _, host := range surface.PublicMap(cfg) {
		if surface.NormalizeHost(host) == originHost {
			return true
		}
	}
	return false
}
