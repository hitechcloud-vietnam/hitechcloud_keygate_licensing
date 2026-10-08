// Package surface answers one question: which public hostname serves
// which surface of the platform. The install is being split across
// several domains — payments.example.com for checkout,
// customer.example.com for the portal, verify.example.com for the
// external-integration API — and the edge needs a plain answer to
// "what surface is this host" and "where does this surface live"
// before a request reaches any handler.
//
// The model: one host maps to ONE surface; a route group may be served
// on several surfaces (/health on all of them). Hosts are matched on
// the Host header with the port stripped, case-insensitively.
//
// Configuration arrives through a plain key→value getter (the settings
// store's shape), so this package imports nothing internal — no store,
// no service, no handler. Settings keys are domain.base plus one
// domain.<surface> per surface ("domain.payments"), values bare
// hostnames ("payments.example.com"). Unset keys fall back to the
// DNS-ish defaults derived from domain.base; domain.base empty and no
// domain.* set means NO enforcement at all — today's single-host
// install keeps working untouched.
package surface

import (
	"net"
	"strings"
)

// Surface is the stable id of one public surface. The ids are part of
// the site-config API response (keyed exactly like this) and of the
// settings keys, so they never change spelling.
type Surface string

const (
	// SurfaceApex is the marketing/marketplace HTML, /r/:code, /health,
	// /ready and the static SPA — the "front door".
	SurfaceApex Surface = "apex"
	// SurfacePayments is /pay/*, /checkout/*, /api/v1/checkout/* and
	// /api/v1/webhook/* (Stripe + gateway IPNs): everything a buyer
	// touches while money moves.
	SurfacePayments Surface = "payments"
	// SurfaceDashboard is /admin/*, /setup and /api/v1/admin/*.
	SurfaceDashboard Surface = "dashboard"
	// SurfaceMerchant is the reseller/affiliate portal pages + APIs.
	SurfaceMerchant Surface = "merchant"
	// SurfaceCustomer is the customer portal: /portal/*, /auth/*,
	// /invites/*.
	SurfaceCustomer Surface = "customer"
	// SurfaceVerify is the external-integration API: /api/v1/license/*,
	// /api/v1/marketplace/*, the customer API-key routes, and SCIM.
	SurfaceVerify Surface = "verify"

	// Optional extras. They only exist when their domain.* setting is
	// filled in — unlike the six above they have no default host.

	// SurfaceHooks isolates inbound webhooks/IPNs from user traffic.
	SurfaceHooks Surface = "hooks"
	// SurfaceDocs serves API docs/openapi.
	SurfaceDocs Surface = "docs"
	// SurfaceStatus serves the health/status page.
	SurfaceStatus Surface = "status"
	// SurfaceGo serves short referral redirects (/r/:code).
	SurfaceGo Surface = "go"
	// SurfaceAuth is central login/SSO.
	SurfaceAuth Surface = "auth"
	// SurfaceCDN serves release artifacts.
	SurfaceCDN Surface = "cdn"
)

// AllSurfaces lists every surface id in presentation order.
var AllSurfaces = []Surface{
	SurfaceApex,
	SurfacePayments,
	SurfaceDashboard,
	SurfaceMerchant,
	SurfaceCustomer,
	SurfaceVerify,
	SurfaceHooks,
	SurfaceDocs,
	SurfaceStatus,
	SurfaceGo,
	SurfaceAuth,
	SurfaceCDN,
}

// surfDerived lists the surfaces that get a DNS-ish default host under
// Base ("<surface>.<base>"), apex aside (apex defaults to the base
// domain itself). The optional extras are deliberate opt-ins: no
// operator has promised hooks.example.com exists, so nothing resolves
// or redirects there until domain.hooks says so.
var surfDerived = []Surface{
	SurfacePayments,
	SurfaceDashboard,
	SurfaceMerchant,
	SurfaceCustomer,
	SurfaceVerify,
}

// SettingDomainBase is the settings key holding the base domain
// ("example.com"). Every surface with no explicit domain.* key derives
// its default host from it, and any unknown host under it resolves to
// apex.
const SettingDomainBase = "domain.base"

// DomainKey returns the settings key holding the hostname override for
// s: DomainKey(SurfacePayments) == "domain.payments".
func DomainKey(s Surface) string { return "domain." + string(s) }

// Config is the runtime host map: which hostname serves which surface,
// plus the base domain the defaults and the apex fallback derive from.
// Build it with FromSettings (runtime settings) or DeriveFromBase
// (static defaults) so the entries are normalized; Resolve and HostFor
// also tolerate hand-built maps.
type Config struct {
	// Hosts maps a hostname (lowercase, no port) to the surface it
	// serves. One host = one surface.
	Hosts map[string]Surface
	// Base is the base domain ("example.com"), empty when unset.
	Base string
}

// Enforced reports whether host enforcement is active at all: at least
// one domain configured. With everything empty this is a single-host
// install — no request is ever refused or redirected, and every
// existing test and local dev setup keeps behaving exactly as before.
func Enforced(cfg Config) bool {
	return len(cfg.Hosts) > 0 || cfg.Base != ""
}

// NormalizeHost lowercases a host and strips any port and trailing dot,
// so "PAYMENTS.Example.com:8443" and "payments.example.com." both
// compare as "payments.example.com". IP literals survive untouched
// apart from the same normalization.
func NormalizeHost(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return ""
	}
	if strings.HasPrefix(h, "[") {
		// Bracketed IPv6, port optional: "[::1]:8080" → "::1".
		if i := strings.Index(h, "]"); i >= 0 {
			h = h[1:i]
		}
	} else if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	h = strings.TrimSuffix(h, ".")
	return strings.ToLower(h)
}

// Resolve maps a request host (with or without port, any case) to its
// surface.
//
// A host with no explicit entry that sits under Base resolves to apex:
// the marketing surface is what an unknown subdomain of the install's
// own domain serves. A core surface's derived default resolves too,
// unless that surface has an explicit host elsewhere — then it moved
// and its old default name falls to apex via the suffix rule.
//
// A host that resolves to nothing — localhost, 127.0.0.1, unrelated
// domains — returns ok=false and is never enforced: that is what keeps
// dev setups and foreign Host headers working.
func Resolve(host string, cfg Config) (Surface, bool) {
	h := NormalizeHost(host)
	if h == "" {
		return "", false
	}
	if s, ok := surfLookup(cfg.Hosts, h); ok {
		return s, true
	}
	base := NormalizeHost(cfg.Base)
	if base == "" {
		return "", false
	}
	if d, ok := surfLookup(DeriveFromBase(base).Hosts, h); ok && !surfHasSurface(cfg.Hosts, d) {
		return d, true
	}
	if h == base || strings.HasSuffix(h, "."+base) {
		return SurfaceApex, true
	}
	return "", false
}

// HostFor returns the canonical hostname for s — the one a redirect to
// this surface should aim at. Deterministic when several hosts map to
// the same surface (lexicographically smallest). When nothing is
// configured for s, the DNS-ish default under Base is returned for
// apex and the five derived surfaces; the optional extras have no
// default and return "" — nothing may redirect to a host nobody
// promised exists.
func HostFor(s Surface, cfg Config) string {
	best := ""
	for host, surf := range cfg.Hosts {
		if surf != s {
			continue
		}
		if best == "" || host < best {
			best = host
		}
	}
	if best != "" {
		return best
	}
	base := NormalizeHost(cfg.Base)
	if base == "" {
		return ""
	}
	if s == SurfaceApex {
		return base
	}
	for _, d := range surfDerived {
		if d == s {
			return string(s) + "." + base
		}
	}
	return ""
}

// DeriveFromBase builds the default host map for a base domain:
// "example.com" → apex on example.com and the five derived surfaces on
// <surface>.example.com. The optional extras get no default host. An
// empty base yields the empty config — no enforcement.
func DeriveFromBase(base string) Config {
	base = NormalizeHost(base)
	cfg := Config{Base: base, Hosts: map[string]Surface{}}
	if base == "" {
		return cfg
	}
	cfg.Hosts[base] = SurfaceApex
	for _, s := range surfDerived {
		cfg.Hosts[string(s)+"."+base] = s
	}
	return cfg
}

// FromSettings builds the runtime Config from the settings store via
// get (a plain key→value getter; empty string for unset or unknown).
// Explicit domain.* entries win; a surface with no explicit entry falls
// back to its DeriveFromBase default when domain.base is set. With
// domain.base empty and no domain.* set the result is the empty config
// — no enforcement, single-host behavior.
func FromSettings(get func(key string) string) Config {
	base := NormalizeHost(get(SettingDomainBase))
	cfg := Config{Base: base, Hosts: map[string]Surface{}}
	covered := map[Surface]bool{}
	for _, s := range AllSurfaces {
		h := NormalizeHost(get(DomainKey(s)))
		if h == "" {
			continue
		}
		cfg.Hosts[h] = s
		covered[s] = true
	}
	if base != "" {
		for h, s := range DeriveFromBase(base).Hosts {
			if covered[s] {
				continue
			}
			cfg.Hosts[h] = s
		}
	}
	return cfg
}

// PublicMap returns surface id → hostname for the site-config API
// ("surfaces": {"payments": "payments.example.com", …}), omitting every
// surface with no host. This is the map the SPA uses to reach each
// surface, so it only ever contains hosts that actually resolve.
func PublicMap(cfg Config) map[string]string {
	out := make(map[string]string, len(cfg.Hosts))
	for _, s := range AllSurfaces {
		if h := HostFor(s, cfg); h != "" {
			out[string(s)] = h
		}
	}
	return out
}

// surfLookup finds h in hosts, tolerating non-normalized keys in
// hand-built maps.
func surfLookup(hosts map[string]Surface, h string) (Surface, bool) {
	if s, ok := hosts[h]; ok {
		return s, true
	}
	for k, s := range hosts {
		if NormalizeHost(k) == h {
			return s, true
		}
	}
	return "", false
}

// surfHasSurface reports whether any host already serves s.
func surfHasSurface(hosts map[string]Surface, s Surface) bool {
	for _, v := range hosts {
		if v == s {
			return true
		}
	}
	return false
}
