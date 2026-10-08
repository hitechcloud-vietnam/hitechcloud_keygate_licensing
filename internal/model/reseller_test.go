package model

import "testing"

// The stored contact address is canonical whatever the admin typed:
// trimmed and lower-cased, so one address cannot exist under two
// spellings. That fold is what the unique index and the
// FindResellerByEmail lookup both rely on.
func TestNormalizeResellerEmail(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"Partner@Example.com", "partner@example.com"},
		{"  partner@example.com  ", "partner@example.com"},
		{"PARTNER@EXAMPLE.COM", "partner@example.com"},
		{"partner@example.com", "partner@example.com"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := NormalizeResellerEmail(tc.in); got != tc.want {
			t.Errorf("NormalizeResellerEmail(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Idempotent: folding a folded address returns it unchanged.
	folded := NormalizeResellerEmail("Partner@Example.com")
	if again := NormalizeResellerEmail(folded); again != folded {
		t.Errorf("not idempotent: %q -> %q", folded, again)
	}
}

// The status vocabulary is closed — active and suspended and nothing
// else — because status feeds the allocation rules and, later,
// commission eligibility.
func TestValidResellerStatus(t *testing.T) {
	for _, ok := range []string{ResellerStatusActive, ResellerStatusSuspended} {
		if !ValidResellerStatus(ok) {
			t.Errorf("ValidResellerStatus(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "active ", "Active", "deleted", "pending", "banned"} {
		if ValidResellerStatus(bad) {
			t.Errorf("ValidResellerStatus(%q) = true, want false", bad)
		}
	}

	// The advertised list is exactly the accepted set.
	if len(ResellerStatuses) != 2 {
		t.Fatalf("ResellerStatuses = %v, want the two accepted values", ResellerStatuses)
	}
	for _, s := range ResellerStatuses {
		if !ValidResellerStatus(s) {
			t.Errorf("ResellerStatuses lists %q which is not valid", s)
		}
	}
}
