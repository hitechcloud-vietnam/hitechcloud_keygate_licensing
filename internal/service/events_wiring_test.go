package service

import (
	"os"
	"strings"
	"testing"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Events-hub site wiring pins ───
//
// This round added exactly one ADDITIVE events.Emit call beside each
// pre-existing merchant-webhook dispatch of a customer-facing event in
// this package (license.go: activated + deactivated; expiry.go: expired
// ×2). The dispatches themselves, their literals and their payloads
// are untouched — the customer-webhook and email behaviour must be
// byte-for-byte what it was.
//
// These pins guard both halves:
//
//  1. the pre-existing Dispatch calls still send their historical
//     literals (existing behaviour unchanged — the package's own suite
//     proves the behaviour end to end);
//  2. each wired site carries exactly ONE events.Emit, referencing the
//     shared model constant rather than a bare literal, so the in-app
//     row and the webhook delivery can never disagree about what
//     happened.
func TestServiceEventSiteWiring(t *testing.T) {
	// The constants the Emit lines use are exactly the literals the
	// pre-existing dispatches at the same sites send.
	for constant, literal := range map[string]string{
		model.EventLicenseActivated:   "license.activated",
		model.EventLicenseDeactivated: "license.deactivated",
		model.EventLicenseExpired:     "license.expired",
	} {
		if constant != literal {
			t.Errorf("constant %q != dispatched literal %q — the hub and the webhooks would disagree", constant, literal)
		}
	}

	cases := []struct {
		file     string
		emits    int
		literals []string // pre-existing Dispatch literals that must remain
	}{
		{"license.go", 2, []string{`"license.activated"`, `"license.deactivated"`}},
		{"expiry.go", 2, []string{`"license.expired"`}},
	}
	for _, tc := range cases {
		src, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		text := string(src)
		if got := strings.Count(text, "events.Emit("); got != tc.emits {
			t.Errorf("%s has %d events.Emit call(s), want exactly %d (one per wired dispatch site)", tc.file, got, tc.emits)
		}
		for _, lit := range tc.literals {
			if !strings.Contains(text, lit) {
				t.Errorf("%s no longer contains the dispatched literal %s — pre-existing behaviour changed", tc.file, lit)
			}
		}
		if !strings.Contains(text, "model.EventLicense") {
			t.Errorf("%s wires the hub without a model.Event* constant — bare literals drift", tc.file)
		}
	}
}
