package model

import (
	"strings"
	"testing"
)

func TestNewCustomerWebhookSecret(t *testing.T) {
	s1, err := NewCustomerWebhookSecret()
	if err != nil {
		t.Fatalf("NewCustomerWebhookSecret: %v", err)
	}
	if !strings.HasPrefix(s1, CustomerWebhookSecretPrefix) {
		t.Errorf("secret %q does not carry the whsec_ prefix", s1)
	}
	if want := len(CustomerWebhookSecretPrefix) + customerWebhookSecretLength; len(s1) != want {
		t.Errorf("secret length = %d, want %d", len(s1), want)
	}
	for _, ch := range s1[len(CustomerWebhookSecretPrefix):] {
		if !strings.ContainsRune(customerWebhookAlphabet, ch) {
			t.Fatalf("secret contains %q outside the unambiguous alphabet", ch)
		}
	}
	// Two draws must not collide — the secret IS the credential.
	s2, _ := NewCustomerWebhookSecret()
	if s1 == s2 {
		t.Error("two secrets generated identically")
	}
}

func TestCustomerWebhookDisplayPrefix(t *testing.T) {
	secret, err := NewCustomerWebhookSecret()
	if err != nil {
		t.Fatalf("NewCustomerWebhookSecret: %v", err)
	}
	p := CustomerWebhookDisplayPrefix(secret)
	if len(p) != CustomerWebhookSecretPrefixLength {
		t.Errorf("display prefix length = %d, want %d", len(p), CustomerWebhookSecretPrefixLength)
	}
	if !strings.HasPrefix(secret, p) {
		t.Errorf("display prefix %q is not a prefix of the secret", p)
	}
	if p == secret {
		t.Error("display prefix must not be the whole secret")
	}
	// A secret shorter than the prefix length is returned unchanged.
	if got := CustomerWebhookDisplayPrefix("abc"); got != "abc" {
		t.Errorf("short secret prefix = %q, want %q", got, "abc")
	}
}

func TestFoldCustomerWebhookEvents(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []string
		want   []string
	}{
		{"clean", []string{"license.created", "quota.exceeded"}, []string{"license.created", "quota.exceeded"}},
		{"trims and drops blanks", []string{" license.created ", "", "  ", "seat.added"}, []string{"license.created", "seat.added"}},
		{"dedupes first wins", []string{"license.created", "seat.added", "license.created"}, []string{"license.created", "seat.added"}},
	} {
		got, err := FoldCustomerWebhookEvents(tc.events)
		if err != nil {
			t.Errorf("%s: FoldCustomerWebhookEvents = %v, want nil", tc.name, err)
			continue
		}
		if len(got) != len(tc.want) {
			t.Errorf("%s: folded = %v, want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: folded[%d] = %q, want %q", tc.name, i, got[i], tc.want[i])
			}
		}
	}

	for _, tc := range []struct {
		name   string
		events []string
	}{
		{"empty", nil},
		{"all blank", []string{"", "  "}},
		{"unknown event", []string{"license.created", "bogus.event"}},
		{"test event is not subscribable", []string{"webhook.test"}},
	} {
		if got, err := FoldCustomerWebhookEvents(tc.events); err == nil {
			t.Errorf("%s: FoldCustomerWebhookEvents = %v, want a refusal", tc.name, got)
		}
	}
}

func TestValidateCustomerWebhookURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
	}{
		{"https public", "https://example.com/webhooks"},
		{"http public", "http://hooks.example.com/a/b?x=1"},
		{"public literal ip", "https://203.0.114.10/hook"}, // just outside TEST-NET-3
		{"public v6", "https://[2606:4700::1111]/hook"},
	} {
		if err := ValidateCustomerWebhookURL(tc.url); err != nil {
			t.Errorf("%s: ValidateCustomerWebhookURL(%q) = %v, want nil", tc.name, tc.url, err)
		}
	}

	for _, tc := range []struct {
		name string
		url  string
	}{
		{"empty", ""},
		{"blank", "   "},
		{"not a url", "::::"},
		{"no scheme", "example.com/hook"},
		{"ftp scheme", "ftp://example.com/hook"},
		{"localhost", "http://localhost/hook"},
		{"localhost case", "https://LocalHost:8080/hook"},
		{"loopback ip", "http://127.0.0.1/hook"},
		{"private 10", "http://10.0.0.5/hook"},
		{"private 192.168", "http://192.168.1.1/hook"},
		{"link local 169.254", "http://169.254.169.254/latest/meta-data"},
		{"unspecified", "http://0.0.0.0/hook"},
		{"this network 0/8", "http://0.1.2.3/hook"},
		{"cgnat metadata", "http://100.100.100.100/hook"}, // Alibaba metadata via CGNAT
		{"cgnat range", "http://100.64.0.1/hook"},
		{"test-net-3", "http://203.0.113.10/hook"},
		{"multicast", "http://224.0.0.1/hook"},
		{"limited broadcast", "http://255.255.255.255/hook"},
		{"nat64", "http://[64:ff9b::7f00:1]/hook"},
		{"teredo", "http://[2001::1]/hook"},
		{"6to4", "http://[2002:c000:0204::1]/hook"},
		{"loopback v6", "http://[::1]/hook"},
		{"too long", "https://example.com/" + strings.Repeat("a", 3000)},
	} {
		if err := ValidateCustomerWebhookURL(tc.url); err == nil {
			t.Errorf("%s: ValidateCustomerWebhookURL(%q) = nil, want a refusal", tc.name, tc.url)
		}
	}
}

func TestIsCustomerWebhookEvent(t *testing.T) {
	if !IsCustomerWebhookEvent(EventLicenseCreated) {
		t.Error("license.created must be in the vocabulary")
	}
	if IsCustomerWebhookEvent("webhook.test") {
		t.Error("webhook.test is a test event, not subscribable")
	}
	if IsCustomerWebhookEvent("") {
		t.Error("empty string is not an event")
	}
	// Every vocabulary entry validates, and the list is exactly the
	// model.Event* set — the full dispatched vocabulary (28 events,
	// including the names some call sites still send as literals).
	if len(CustomerWebhookEvents) != 28 {
		t.Errorf("CustomerWebhookEvents has %d entries, want 28", len(CustomerWebhookEvents))
	}
	for _, name := range []string{
		EventLicenseActivated,
		EventLicenseExpired,
		EventLicensePaymentFailed,
		EventReleasePublished,
	} {
		if !IsCustomerWebhookEvent(name) {
			t.Errorf("IsCustomerWebhookEvent(%q) = false, want true", name)
		}
	}
	for _, e := range CustomerWebhookEvents {
		if !IsCustomerWebhookEvent(e) {
			t.Errorf("IsCustomerWebhookEvent(%q) = false, want true", e)
		}
	}
}
