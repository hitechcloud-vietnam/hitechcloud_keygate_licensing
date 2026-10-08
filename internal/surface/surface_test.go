package surface

// The host map is the root of every routing decision the edge makes,
// so its rules are pinned here rather than discovered in production:
//
//   - Resolve: exact host (case-insensitive, port stripped), a core
//     surface's derived default, then anything under Base → apex; a
//     dev host or foreign domain resolves to nothing and is never
//     enforced.
//   - HostFor is Resolve's mirror (a redirect can never aim at a host
//     that resolves to a different surface) and stays deterministic.
//   - FromSettings: explicit domain.* wins, domain.base derives the
//     rest, and NOTHING configured means no enforcement at all.

import (
	"testing"
)

func TestNormalizeHostStripsPortCaseAndDot(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"payments.example.com", "payments.example.com"},
		{"PAYMENTS.Example.COM", "payments.example.com"},
		{"payments.example.com:8443", "payments.example.com"},
		{" payments.example.com. ", "payments.example.com"},
		{"[::1]:8080", "::1"},
		{"[::1]", "::1"},
		{"127.0.0.1:9000", "127.0.0.1"},
		{"", ""},
		{"   ", ""},
	} {
		if got := NormalizeHost(tc.in); got != tc.want {
			t.Errorf("NormalizeHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResolveTable(t *testing.T) {
	full := FromSettings(func(key string) string {
		switch key {
		case SettingDomainBase:
			return "example.com"
		case DomainKey(SurfacePayments):
			return "pay.example.com" // explicit override of payments.example.com
		}
		return ""
	})

	for _, tc := range []struct {
		name   string
		host   string
		cfg    Config
		want   Surface
		wantOK bool
	}{
		{"explicit host", "pay.example.com", full, SurfacePayments, true},
		{"explicit host, case + port", "Pay.Example.Com:8443", full, SurfacePayments, true},
		{"derived default", "dashboard.example.com", full, SurfaceDashboard, true},
		{"derived default, case", "CUSTOMER.EXAMPLE.COM", full, SurfaceCustomer, true},
		{"base itself is apex", "example.com", full, SurfaceApex, true},
		{"unknown subdomain is apex", "anything.example.com", full, SurfaceApex, true},
		{"overridden default name falls to apex", "payments.example.com", full, SurfaceApex, true},
		{"suffix must be a label boundary", "notexample.com", full, "", false},
		{"suffix must match the whole tail", "example.com.evil.com", full, "", false},
		{"dev host passes through", "localhost:9000", full, "", false},
		{"dev host passes through (loopback)", "127.0.0.1:9000", full, "", false},
		{"foreign domain passes through", "app.other.com", full, "", false},
		{"empty host", "", full, "", false},
		{"empty config resolves nothing", "payments.example.com", Config{}, "", false},
		{"base only: core default", "payments.example.com", DeriveFromBase("example.com"), SurfacePayments, true},
		{"base only: apex", "www.example.com", DeriveFromBase("example.com"), SurfaceApex, true},
		{"hand-built map, unnormalized key", "payments.example.com", Config{Base: "example.com", Hosts: map[string]Surface{"Payments.Example.com": SurfacePayments}}, SurfacePayments, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Resolve(tc.host, tc.cfg)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("Resolve(%q) = (%q, %v), want (%q, %v)", tc.host, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

func TestDeriveFromBaseDefaults(t *testing.T) {
	cfg := DeriveFromBase("Example.COM")
	if cfg.Base != "example.com" {
		t.Errorf("Base = %q", cfg.Base)
	}
	want := map[string]Surface{
		"example.com":           SurfaceApex,
		"payments.example.com":  SurfacePayments,
		"dashboard.example.com": SurfaceDashboard,
		"merchant.example.com":  SurfaceMerchant,
		"customer.example.com":  SurfaceCustomer,
		"verify.example.com":    SurfaceVerify,
	}
	if len(cfg.Hosts) != len(want) {
		t.Errorf("derived %d hosts, want %d: %v", len(cfg.Hosts), len(want), cfg.Hosts)
	}
	for h, s := range want {
		if cfg.Hosts[h] != s {
			t.Errorf("Hosts[%q] = %q, want %q", h, cfg.Hosts[h], s)
		}
	}
	// The optional extras are opt-ins: no default host, ever.
	for _, s := range []Surface{SurfaceHooks, SurfaceDocs, SurfaceStatus, SurfaceGo, SurfaceAuth, SurfaceCDN} {
		if HostFor(s, cfg) != "" {
			t.Errorf("HostFor(%s) = %q, want \"\" (opt-in only)", s, HostFor(s, cfg))
		}
	}
	// Empty base → no enforcement at all.
	if Enforced(DeriveFromBase("")) {
		t.Error("DeriveFromBase(\"\") must not enforce")
	}
}

func TestHostForMirrorsResolve(t *testing.T) {
	cfg := FromSettings(func(key string) string {
		switch key {
		case SettingDomainBase:
			return "example.com"
		case DomainKey(SurfaceApex):
			return "www.example.com"
		case DomainKey(SurfaceHooks):
			return "hooks.example.com"
		}
		return ""
	})
	// Every host HostFor hands out must resolve back to the same
	// surface — otherwise a redirect can aim at a host that serves
	// something else (or loops).
	for _, s := range AllSurfaces {
		h := HostFor(s, cfg)
		if h == "" {
			continue
		}
		got, ok := Resolve(h, cfg)
		if !ok || got != s {
			t.Errorf("HostFor(%s) = %q but Resolve = (%q, %v)", s, h, got, ok)
		}
	}
	if got := HostFor(SurfaceApex, cfg); got != "www.example.com" {
		t.Errorf("explicit apex host = %q, want www.example.com", got)
	}
	if got := HostFor(SurfaceHooks, cfg); got != "hooks.example.com" {
		t.Errorf("explicit hooks host = %q, want hooks.example.com", got)
	}
	// No config at all → nothing to aim at.
	if got := HostFor(SurfacePayments, Config{}); got != "" {
		t.Errorf("HostFor on empty config = %q, want \"\"", got)
	}
}

func TestFromSettingsExplicitWinsAndBaseDerives(t *testing.T) {
	vals := map[string]string{
		SettingDomainBase:          "example.com ",
		DomainKey(SurfacePayments): "Pay.Example.Com:443",
		DomainKey(SurfaceGo):       "go.example.com",
		"unrelated_key":            "ignored",
	}
	cfg := FromSettings(func(key string) string { return vals[key] })

	if got := HostFor(SurfacePayments, cfg); got != "pay.example.com" {
		t.Errorf("explicit payments host = %q, want pay.example.com", got)
	}
	// The derived name of an explicitly-hosted surface is gone.
	for h, s := range cfg.Hosts {
		if s == SurfacePayments && h != "pay.example.com" {
			t.Errorf("stray payments host %q", h)
		}
	}
	if got := HostFor(SurfaceDashboard, cfg); got != "dashboard.example.com" {
		t.Errorf("derived dashboard host = %q, want dashboard.example.com", got)
	}
	// Opt-in extras only appear when configured — but may.
	if got := HostFor(SurfaceGo, cfg); got != "go.example.com" {
		t.Errorf("configured go host = %q, want go.example.com", got)
	}
	if got := HostFor(SurfaceDocs, cfg); got != "" {
		t.Errorf("unconfigured docs host = %q, want \"\"", got)
	}
	if !Enforced(cfg) {
		t.Error("config with domains must enforce")
	}

	// Nothing configured → single-host install, today's behavior.
	empty := FromSettings(func(string) string { return "" })
	if Enforced(empty) || len(empty.Hosts) != 0 {
		t.Errorf("empty settings must yield the empty config, got %+v", empty)
	}
}

func TestPublicMapOmitsEmptyHosts(t *testing.T) {
	cfg := FromSettings(func(key string) string {
		switch key {
		case SettingDomainBase:
			return "example.com"
		case DomainKey(SurfacePayments):
			return "pay.example.com"
		case DomainKey(SurfaceCDN):
			return "cdn.example.com"
		}
		return ""
	})
	want := map[string]string{
		"apex":      "example.com",
		"payments":  "pay.example.com",
		"dashboard": "dashboard.example.com",
		"merchant":  "merchant.example.com",
		"customer":  "customer.example.com",
		"verify":    "verify.example.com",
		"cdn":       "cdn.example.com",
	}
	got := PublicMap(cfg)
	if len(got) != len(want) {
		t.Errorf("PublicMap = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("PublicMap[%q] = %q, want %q", k, got[k], v)
		}
	}
	// Unconfigured opt-ins are omitted, never blank.
	for _, s := range []Surface{SurfaceHooks, SurfaceDocs, SurfaceStatus, SurfaceGo, SurfaceAuth} {
		if h, ok := got[string(s)]; ok {
			t.Errorf("PublicMap contains unconfigured %s = %q", s, h)
		}
	}
}
