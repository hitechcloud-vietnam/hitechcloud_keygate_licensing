package model

import "testing"

// TestCanTransitionInvoice pins the invoice state machine the PO/
// invoice workflow exposes: draft→open→paid is the happy path, void
// and uncollectible branch off open, uncollectible can recover, and
// paid can only leave through a refund. The two refusals the admin API
// leans on hardest — paid→void and paid→uncollectible — are cases in
// the "refused" table below, not special-cases in a handler.
func TestCanTransitionInvoice(t *testing.T) {
	allowed := [][2]string{
		{InvoiceStatusDraft, InvoiceStatusOpen},
		{InvoiceStatusDraft, InvoiceStatusVoid},
		{InvoiceStatusOpen, InvoiceStatusPaid},
		{InvoiceStatusOpen, InvoiceStatusVoid},
		{InvoiceStatusOpen, InvoiceStatusUncollectible},
		{InvoiceStatusPaid, InvoiceStatusRefunded},
		{InvoiceStatusUncollectible, InvoiceStatusOpen},
		{InvoiceStatusUncollectible, InvoiceStatusPaid},
	}
	for _, tc := range allowed {
		if !CanTransitionInvoice(tc[0], tc[1]) {
			t.Errorf("CanTransitionInvoice(%q, %q) = false, want true", tc[0], tc[1])
		}
	}

	refused := [][2]string{
		// The two load-bearing refusals: a paid invoice must be
		// refunded before anything else, and is collected by
		// definition — never uncollectible.
		{InvoiceStatusPaid, InvoiceStatusVoid},
		{InvoiceStatusPaid, InvoiceStatusUncollectible},
		// Skip-the-line: an invoice is issued (open) before it is
		// settled or given up on.
		{InvoiceStatusDraft, InvoiceStatusPaid},
		{InvoiceStatusDraft, InvoiceStatusUncollectible},
		// Terminal states.
		{InvoiceStatusVoid, InvoiceStatusOpen},
		{InvoiceStatusVoid, InvoiceStatusPaid},
		{InvoiceStatusRefunded, InvoiceStatusVoid},
		{InvoiceStatusRefunded, InvoiceStatusOpen},
		// A refund is not a re-pay.
		{InvoiceStatusRefunded, InvoiceStatusPaid},
		// Self-transitions surface instead of silently succeeding.
		{InvoiceStatusDraft, InvoiceStatusDraft},
		{InvoiceStatusOpen, InvoiceStatusOpen},
		{InvoiceStatusPaid, InvoiceStatusPaid},
		// Unknown values never move — neither side of the arrow.
		{"", InvoiceStatusOpen},
		{InvoiceStatusOpen, "bogus"},
		{"bogus", InvoiceStatusOpen},
	}
	for _, tc := range refused {
		if CanTransitionInvoice(tc[0], tc[1]) {
			t.Errorf("CanTransitionInvoice(%q, %q) = true, want false", tc[0], tc[1])
		}
	}
}

// The added states keep the exact strings the migration's widened
// CHECK expects: a rename here would orphan every row already written.
func TestInvoiceStatusStringsAreStable(t *testing.T) {
	for _, tc := range []struct{ got, want string }{
		{InvoiceStatusDraft, "draft"},
		{InvoiceStatusOpen, "open"},
		{InvoiceStatusPaid, "paid"},
		{InvoiceStatusVoid, "void"},
		{InvoiceStatusUncollectible, "uncollectible"},
		{InvoiceStatusRefunded, "refunded"},
	} {
		if tc.got != tc.want {
			t.Errorf("invoice status constant = %q, want %q", tc.got, tc.want)
		}
	}
}
