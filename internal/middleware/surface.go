package middleware

import (
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/surface"
)

// RequireSurface keeps each route group on its own public host. The
// platform is splitting into per-surface domains (payments.example.com
// for checkout, customer.example.com for the portal, verify.example.com
// for the integration API), and a request for one surface's routes
// arriving on another surface's host is either a lost user (redirect
// them to the canonical host, preserving path and query) or a machine
// client on the wrong endpoint (404 — a JSON envelope, never HTML).
//
// allowed lists the surfaces the group is served on (a route group may
// live on several: /health on all of them). The canonical redirect
// target is the first entry.
//
// Enforcement is active only when the config has at least one domain.
// When it is inactive — or the request host resolves to nothing at all
// (localhost, 127.0.0.1, a foreign Host header) — the request passes
// through untouched: single-host installs and every local dev setup
// keep behaving exactly as before. Host matching is case-insensitive
// and port-stripped.
//
// X-Forwarded-Host is honored only when a trusted proxy forwarded the
// request — the same trust boundary gin applies to X-Forwarded-For
// (c.ClientIP() only departs from the peer address when a trusted
// proxy says so). A client forging it on a direct connection is
// ignored, so no caller can steer itself onto — or off — a surface.
func RequireSurface(cfgProvider func() surface.Config, allowed ...surface.Surface) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cfgProvider == nil || len(allowed) == 0 {
			c.Next()
			return
		}
		cfg := cfgProvider()
		if !surface.Enforced(cfg) {
			c.Next()
			return
		}
		host := surfRequestHost(c)
		s, ok := surface.Resolve(host, cfg)
		if !ok {
			// Dev hosts and foreign domains: not ours to enforce.
			c.Next()
			return
		}
		for _, a := range allowed {
			if a == s {
				c.Next()
				return
			}
		}

		if surfIsAPIPath(c.Request.URL.Path) {
			// A machine client on the wrong host gets the same answer
			// an unknown path would: a 404 envelope, no hint of what
			// lives where.
			slog.Warn("surface: refusing request on wrong host",
				"host", host, "surface", string(s), "path", c.Request.URL.Path)
			abortWithError(c, http.StatusNotFound, "NOT_FOUND", "not found")
			return
		}

		target := surface.HostFor(allowed[0], cfg)
		if target == "" || surface.NormalizeHost(target) == host {
			// Nowhere canonical to send them (or already there): serve
			// here rather than bounce the browser in a loop.
			c.Next()
			return
		}
		scheme := "http"
		if surfRequestIsHTTPS(c) {
			scheme = "https"
		}
		slog.Info("surface: redirecting to canonical host",
			"host", host, "surface", string(s), "target", target, "path", c.Request.URL.Path)
		c.Redirect(http.StatusFound, scheme+"://"+target+c.Request.URL.RequestURI())
		c.Abort()
	}
}

// surfRequestHost is the host the client reached: the Host header with
// the port stripped, or the first X-Forwarded-Host entry when a
// trusted proxy rewrote it. The first entry is the host the browser
// used — proxy chains append, exactly like X-Forwarded-Proto.
func surfRequestHost(c *gin.Context) string {
	h := c.Request.Host
	if surfBehindTrustedProxy(c) {
		if xfh := c.GetHeader("X-Forwarded-Host"); xfh != "" {
			if first, _, _ := strings.Cut(xfh, ","); strings.TrimSpace(first) != "" {
				h = first
			}
		}
	}
	return surface.NormalizeHost(h)
}

// surfBehindTrustedProxy reports whether a proxy this install trusts
// forwarded the request. It is the same test gin applies to decide
// whether X-Forwarded-For names the client: c.ClientIP() only differs
// from the peer address when the peer is inside the trusted proxy set
// AND forwarded something. A forged header from a direct client never
// passes it. (IP.Equal, not string compare: ::ffff:10.0.0.5 and
// 10.0.0.5 are the same peer.)
func surfBehindTrustedProxy(c *gin.Context) bool {
	remote := c.RemoteIP()
	client := c.ClientIP()
	if remote == "" || client == "" {
		return false
	}
	rp, cp := net.ParseIP(remote), net.ParseIP(client)
	if rp == nil || cp == nil {
		return client != remote
	}
	return !rp.Equal(cp)
}

// surfRequestIsHTTPS reports whether the browser reached this request
// over TLS — directly or through a proxy that terminated it. It picks
// the redirect scheme only; the Secure cookie attribute is decided by
// requestIsHTTPS in the handler layer, which additionally consults
// BASE_URL for proxies that forward nothing.
func surfRequestIsHTTPS(c *gin.Context) bool {
	if c.Request.TLS != nil {
		return true
	}
	proto, _, _ := strings.Cut(c.GetHeader("X-Forwarded-Proto"), ",")
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// surfIsAPIPath reports whether the path is machine-facing: API and
// SCIM endpoints plus webhook/IPN receivers. Those get a 404 envelope
// on the wrong host; everything else (browser pages, the SPA) gets a
// redirect.
func surfIsAPIPath(p string) bool {
	for _, prefix := range []string{"/api/", "/scim/", "/webhook/", "/webhooks/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	switch p {
	case "/api", "/scim", "/webhook", "/webhooks":
		return true
	}
	return false
}
