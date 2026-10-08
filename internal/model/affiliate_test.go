package model

import (
	"regexp"
	"strings"
	"testing"
)

// The stored contact address is canonical whatever the admin typed:
// trimmed and lower-cased, so one address cannot exist under two
// spellings — the same fold the reseller program applies. That fold is
// what the unique index and the FindAffiliateByEmail lookup rely on.
func TestNormalizeAffiliateEmail(t *testing.T) {
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
		if got := NormalizeAffiliateEmail(tc.in); got != tc.want {
			t.Errorf("NormalizeAffiliateEmail(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Idempotent: folding a folded address returns it unchanged.
	folded := NormalizeAffiliateEmail("Partner@Example.com")
	if again := NormalizeAffiliateEmail(folded); again != folded {
		t.Errorf("not idempotent: %q -> %q", folded, again)
	}
}

// The code fold is what makes one code one code: upper-case, letters
// and digits only, so the same handle cannot be spelled several ways
// to split its clicks across rows.
func TestNormalizeReferralCode(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"save20", "SAVE20"},
		{"  Save-20  ", "SAVE20"},
		{"Summer Sale '26!", "SUMMERSALE26"},
		{"SUMMER-SALE-26", "SUMMERSALE26"},
		{"a1b2c3", "A1B2C3"},
		{"", ""},
		{"!!!", ""},
	}
	for _, tc := range cases {
		if got := NormalizeReferralCode(tc.in); got != tc.want {
			t.Errorf("NormalizeReferralCode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Idempotent: folding a folded code returns it unchanged.
	folded := NormalizeReferralCode("Summer Sale '26")
	if again := NormalizeReferralCode(folded); again != folded {
		t.Errorf("not idempotent: %q -> %q", folded, again)
	}
}

// The stored form is 4..32 uppercase alphanumerics and nothing else —
// it travels in a URL path and in a cookie, so the character set is
// part of the safety story, not just tidiness.
func TestValidReferralCode(t *testing.T) {
	valid := []string{"SAVE", "SAVE20", "A1B2C3D4", strings.Repeat("X", 32)}
	for _, code := range valid {
		if !ValidReferralCode(code) {
			t.Errorf("ValidReferralCode(%q) = false, want true", code)
		}
	}

	invalid := []string{
		"", "ABC", // too short
		strings.Repeat("X", 33), // too long
		"save20", "Save20",      // not folded
		"SAVE-20", "SAVE_20", "SAVE 20", // separators are folded away, never stored
		"SAVE!", "été2026", // outside A-Z0-9
	}
	for _, code := range invalid {
		if ValidReferralCode(code) {
			t.Errorf("ValidReferralCode(%q) = true, want false", code)
		}
	}

	// Anything the fold produces that is at least 4 characters is
	// itself valid: the fold and the check agree.
	for _, raw := range []string{"Summer Sale 2026", "promo-code-v2"} {
		folded := NormalizeReferralCode(raw)
		if !ValidReferralCode(folded) {
			t.Errorf("folded %q -> %q is not itself valid", raw, folded)
		}
	}
}

// The lifecycle vocabularies are closed: active|suspended, percent|
// fixed, the five conversion statuses, the three payout statuses.
func TestAffiliateVocabularies(t *testing.T) {
	for _, ok := range []string{AffiliateStatusActive, AffiliateStatusSuspended} {
		if !ValidAffiliateStatus(ok) {
			t.Errorf("ValidAffiliateStatus(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "Active", "deleted", "pending", "banned"} {
		if ValidAffiliateStatus(bad) {
			t.Errorf("ValidAffiliateStatus(%q) = true, want false", bad)
		}
	}
	if len(AffiliateStatuses) != 2 {
		t.Errorf("AffiliateStatuses = %v, want the two accepted values", AffiliateStatuses)
	}

	for _, ok := range AffiliateCommissionModels {
		if !ValidAffiliateCommissionModel(ok) {
			t.Errorf("ValidAffiliateCommissionModel(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "Percent", "recurring", "one-time", "percentage"} {
		if ValidAffiliateCommissionModel(bad) {
			t.Errorf("ValidAffiliateCommissionModel(%q) = true, want false", bad)
		}
	}

	for _, ok := range AffiliateConversionStatuses {
		if !ValidAffiliateConversionStatus(ok) {
			t.Errorf("ValidAffiliateConversionStatus(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "PAID", "settled", "chargeback"} {
		if ValidAffiliateConversionStatus(bad) {
			t.Errorf("ValidAffiliateConversionStatus(%q) = true, want false", bad)
		}
	}
	// The advertised list is exactly the accepted set.
	if len(AffiliateConversionStatuses) != 5 {
		t.Errorf("AffiliateConversionStatuses = %v, want five values", AffiliateConversionStatuses)
	}

	for _, ok := range AffiliatePayoutStatuses {
		if !ValidAffiliatePayoutStatus(ok) {
			t.Errorf("ValidAffiliatePayoutStatus(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "Paid", "settled", "refunded"} {
		if ValidAffiliatePayoutStatus(bad) {
			t.Errorf("ValidAffiliatePayoutStatus(%q) = true, want false", bad)
		}
	}
	if len(AffiliatePayoutStatuses) != 3 {
		t.Errorf("AffiliatePayoutStatuses = %v, want three values", AffiliatePayoutStatuses)
	}
}

// The commission math, for both models. Percent is integer
// round-half-up of total·bps/10000 — a remainder of exactly half a
// minor unit rounds UP (0.5 → 1), everything smaller rounds down.
// Fixed is the flat amount whatever the order total is. No floats
// anywhere.
func TestAffiliateCommissionFor(t *testing.T) {
	percent := func(bps int) *Affiliate {
		return &Affiliate{CommissionModel: AffiliateCommissionModelPercent, CommissionBPS: bps}
	}
	fixed := func(minor int64) *Affiliate {
		return &Affiliate{CommissionModel: AffiliateCommissionModelFixed, CommissionMinor: minor}
	}

	cases := []struct {
		name  string
		aff   *Affiliate
		total int64
		want  int64
	}{
		// percent: exact division needs no rounding.
		{"10% of 10000", percent(1000), 10000, 1000},
		{"100% of 250", percent(10000), 250, 250},
		{"0 bps earns nothing", percent(0), 10000, 0},

		// percent: half a minor unit rounds UP — the documented rule
		// ((total·bps + 5000) / 10000, truncating).
		{"half rounds up: 0.5 -> 1", percent(5000), 1, 1},
		{"half rounds up: 1.5 -> 2", percent(5000), 3, 2},
		{"half rounds up: 2.5 -> 3", percent(5000), 5, 3},

		// percent: everything below half rounds down, everything above
		// rounds up.
		{"just below half: 0.4999 -> 0", percent(4999), 1, 0},
		{"just above half: 0.5001 -> 1", percent(5001), 1, 1},
		{"0.05 rounds down: 5bps of 100", percent(5), 100, 0},
		{"0.75 rounds up: 25bps of 3", percent(2500), 3, 1},

		// percent: real-world rate and total.
		{"3% of 49900 is exact", percent(300), 49900, 1497},
		{"3% of 49999: 1499.97 -> 1500", percent(300), 49999, 1500},
		{"7.5% of 101: 7.575 -> 8", percent(750), 101, 8},

		// fixed: the flat amount, whatever the order total is.
		{"fixed 500 of 10000", fixed(500), 10000, 500},
		{"fixed 500 of 1", fixed(500), 1, 500},
		{"fixed 0", fixed(0), 10000, 0},
	}
	for _, tc := range cases {
		if got := tc.aff.CommissionFor(tc.total); got != tc.want {
			t.Errorf("%s: CommissionFor(%d) = %d, want %d", tc.name, tc.total, got, tc.want)
		}
	}

	// The bound that keeps total·bps inside int64 is what the endpoint
	// enforces before calling; at the bound the math is still exact and
	// does not overflow.
	max := percent(10000).CommissionFor(MaxCommissionableOrderMinor)
	if max != MaxCommissionableOrderMinor {
		t.Errorf("100%% of max total = %d, want %d", max, MaxCommissionableOrderMinor)
	}
	if MaxCommissionableOrderMinor*10000 <= 0 {
		t.Error("MaxCommissionableOrderMinor*10000 overflowed; the bound is wrong")
	}
}

// The conversion review state machine: pending is the queue, approved
// and paid can only be clawed back, rejected and reversed are terminal,
// and paid is never a direct move (a payout settles conversions).
func TestConversionTransitionOK(t *testing.T) {
	allowed := [][2]string{
		{AffiliateConversionStatusPending, AffiliateConversionStatusApproved},
		{AffiliateConversionStatusPending, AffiliateConversionStatusRejected},
		{AffiliateConversionStatusPending, AffiliateConversionStatusReversed},
		{AffiliateConversionStatusApproved, AffiliateConversionStatusReversed},
		{AffiliateConversionStatusPaid, AffiliateConversionStatusReversed},
	}
	for _, mv := range allowed {
		if !ConversionTransitionOK(mv[0], mv[1]) {
			t.Errorf("ConversionTransitionOK(%q, %q) = false, want true", mv[0], mv[1])
		}
	}

	refused := [][2]string{
		{AffiliateConversionStatusPending, AffiliateConversionStatusPending},
		{AffiliateConversionStatusPending, AffiliateConversionStatusPaid},
		{AffiliateConversionStatusApproved, AffiliateConversionStatusApproved},
		{AffiliateConversionStatusApproved, AffiliateConversionStatusPending},
		{AffiliateConversionStatusApproved, AffiliateConversionStatusPaid},
		{AffiliateConversionStatusApproved, AffiliateConversionStatusRejected},
		{AffiliateConversionStatusPaid, AffiliateConversionStatusPaid},
		{AffiliateConversionStatusPaid, AffiliateConversionStatusApproved},
		{AffiliateConversionStatusPaid, AffiliateConversionStatusRejected},
		{AffiliateConversionStatusRejected, AffiliateConversionStatusApproved},
		{AffiliateConversionStatusRejected, AffiliateConversionStatusReversed},
		{AffiliateConversionStatusReversed, AffiliateConversionStatusApproved},
		{AffiliateConversionStatusReversed, AffiliateConversionStatusRejected},
	}
	for _, mv := range refused {
		if ConversionTransitionOK(mv[0], mv[1]) {
			t.Errorf("ConversionTransitionOK(%q, %q) = true, want false", mv[0], mv[1])
		}
	}

	// An invented status is never a legal move, in either position.
	if ConversionTransitionOK("pending", "chargeback") || ConversionTransitionOK("chargeback", "paid") {
		t.Error("an invented status participates in a transition")
	}
}

// The payout state machine: only requested moves, exactly once.
func TestPayoutTransitionOK(t *testing.T) {
	for _, to := range []string{AffiliatePayoutStatusPaid, AffiliatePayoutStatusFailed} {
		if !PayoutTransitionOK(AffiliatePayoutStatusRequested, to) {
			t.Errorf("requested -> %q refused", to)
		}
	}
	terminal := [][2]string{
		{AffiliatePayoutStatusPaid, AffiliatePayoutStatusFailed},
		{AffiliatePayoutStatusPaid, AffiliatePayoutStatusRequested},
		{AffiliatePayoutStatusPaid, AffiliatePayoutStatusPaid},
		{AffiliatePayoutStatusFailed, AffiliatePayoutStatusPaid},
		{AffiliatePayoutStatusFailed, AffiliatePayoutStatusRequested},
		{AffiliatePayoutStatusRequested, AffiliatePayoutStatusRequested},
	}
	for _, mv := range terminal {
		if PayoutTransitionOK(mv[0], mv[1]) {
			t.Errorf("PayoutTransitionOK(%q, %q) = true, want false", mv[0], mv[1])
		}
	}
	if PayoutTransitionOK("requested", "cancelled") || PayoutTransitionOK("cancelled", "paid") {
		t.Error("an invented status participates in a transition")
	}
}

// The click hashes are one-way and salted: the raw IP or user agent is
// recoverable from neither, the output is stable 64-char lowercase
// hex, a different salt gives a different hash for the same input, and
// the two hash spaces do not overlap (an IP hash can never be replayed
// into the user-agent column).
func TestHashReferralValues(t *testing.T) {
	const ip = "203.0.113.7"
	const ua = "Mozilla/5.0 (Test)"

	hexShape := regexp.MustCompile(`^[0-9a-f]{64}$`)

	ipHash := HashReferralIP("salt-1", ip)
	if !hexShape.MatchString(ipHash) {
		t.Errorf("HashReferralIP output %q is not 64 lowercase hex", ipHash)
	}
	if strings.Contains(ipHash, "203.0.113") || strings.Contains(ipHash, ip) {
		t.Error("the IP hash contains the raw address")
	}
	if HashReferralIP("salt-1", ip) != ipHash {
		t.Error("HashReferralIP is not deterministic")
	}
	if HashReferralIP("salt-2", ip) == ipHash {
		t.Error("a different salt produces the same hash")
	}
	if HashReferralIP("salt-1", "203.0.113.8") == ipHash {
		t.Error("a different IP produces the same hash")
	}

	uaHash := HashReferralUserAgent("salt-1", ua)
	if !hexShape.MatchString(uaHash) {
		t.Errorf("HashReferralUserAgent output %q is not 64 lowercase hex", uaHash)
	}
	if strings.Contains(uaHash, "Mozilla") {
		t.Error("the user-agent hash contains the raw string")
	}
	// Domain separation: kind "ip" vs kind "ua" makes the same value
	// hash differently in the two columns.
	if HashReferralIP("salt-1", ua) == uaHash {
		t.Error("the ip and ua hash spaces collide")
	}

	// The empty salt still hashes (no raw value ever stored), it is
	// just weaker against a guessed-input attacker — documented in
	// HashReferralIP.
	if empty := HashReferralIP("", ip); !hexShape.MatchString(empty) || strings.Contains(empty, ip) {
		t.Error("empty-salt hashing is not a hash")
	}
}
