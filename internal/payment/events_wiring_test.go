package payment

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
// stripe.go (notifyPaymentRecovered: license.payment_recovered;
// onPaymentFailed: license.payment_failed). The dispatches, their
// literals and their payloads are untouched — the recovery/payment-
// failure bookkeeping (email once per past_due episode, audit line,
// merchant webhook) must be exactly what it was, and the payment suite
// (recovered_test.go, invoice_paid_test.go — TEST_DATABASE_URL) is the
// end-to-end proof of that when a database is available.
//
// These pins guard both halves: the pre-existing dispatch literals
// remain, and each wired site carries exactly ONE events.Emit
// referencing the shared model constant (no bare literals — they
// drift).
func TestStripeEventSiteWiring(t *testing.T) {
	// The constants the Emit lines use are exactly the literals the
	// pre-existing dispatches at the same sites send.
	for constant, literal := range map[string]string{
		model.EventLicensePaymentFailed:    "license.payment_failed",
		model.EventLicensePaymentRecovered: "license.payment_recovered",
	} {
		if constant != literal {
			t.Errorf("constant %q != dispatched literal %q — the hub and the webhooks would disagree", constant, literal)
		}
	}

	src, err := os.ReadFile("stripe.go")
	if err != nil {
		t.Fatalf("read stripe.go: %v", err)
	}
	text := string(src)
	if got := strings.Count(text, "events.Emit("); got != 2 {
		t.Errorf("stripe.go has %d events.Emit call(s), want exactly 2 (recovered + failed)", got)
	}
	for _, lit := range []string{`"license.payment_recovered"`, `"license.payment_failed"`} {
		if !strings.Contains(text, lit) {
			t.Errorf("stripe.go no longer contains the dispatched literal %s — pre-existing behaviour changed", lit)
		}
	}
	if !strings.Contains(text, "model.EventLicensePayment") {
		t.Error("stripe.go wires the hub without a model.Event* constant — bare literals drift")
	}
}
