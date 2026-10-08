package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// The refund status vocabulary is closed and pinned: the DB CHECK
// (migration 20261008_149000) and the refund dispatcher both read
// these strings, so a rename must fail here, not at INSERT time.
func TestRefundStatusVocabulary(t *testing.T) {
	want := []string{RefundStatusPending, RefundStatusSucceeded, RefundStatusFailed}
	if len(RefundStatuses) != len(want) {
		t.Fatalf("RefundStatuses = %v, want %v", RefundStatuses, want)
	}
	for i, s := range want {
		if RefundStatuses[i] != s {
			t.Errorf("RefundStatuses[%d] = %q, want %q", i, RefundStatuses[i], s)
		}
	}
	for _, s := range want {
		if !ValidRefundStatus(s) {
			t.Errorf("ValidRefundStatus(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "PENDING", "settled", "refunded", "partial"} {
		if ValidRefundStatus(s) {
			t.Errorf("ValidRefundStatus(%q) = true, want false", s)
		}
	}
}

// Counts is the refund's claim on the order's refundable amount:
// succeeded money is gone and pending money is committed to going
// (the gateway settles asynchronously) — both reserve their amount.
// Failed freed its amount again, and there is no refund at all
// without a row.
func TestRefundCounts(t *testing.T) {
	cases := []struct {
		name string
		r    *Refund
		want bool
	}{
		{"succeeded", &Refund{Status: RefundStatusSucceeded}, true},
		{"pending", &Refund{Status: RefundStatusPending}, true},
		{"failed", &Refund{Status: RefundStatusFailed}, false},
		{"nil row", nil, false},
	}
	for _, tc := range cases {
		if got := tc.r.Counts(); got != tc.want {
			t.Errorf("%s: Counts() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// §80: seven revocation reasons, closed. The default must itself be a
// member — a revoke call with no reason falls back to it, and what
// reaches licenses.revoke_reason is always a real reason.
func TestRevokeReasonVocabulary(t *testing.T) {
	got := RevokeReasons()
	want := []string{
		RevokeReasonFraud,
		RevokeReasonRefund,
		RevokeReasonChargeback,
		RevokeReasonPolicyViolation,
		RevokeReasonCustomerRequest,
		RevokeReasonSecurityIncident,
		RevokeReasonAdministrative,
	}
	if len(got) != 7 {
		t.Fatalf("RevokeReasons() = %v, want the seven plan §80 reasons", got)
	}
	seen := map[string]bool{}
	for i, r := range want {
		if got[i] != r {
			t.Errorf("RevokeReasons()[%d] = %q, want %q", i, got[i], r)
		}
		if seen[got[i]] {
			t.Errorf("RevokeReasons() repeats %q", got[i])
		}
		seen[got[i]] = true
		if !ValidRevokeReason(r) {
			t.Errorf("ValidRevokeReason(%q) = false, want true", r)
		}
	}
	if !ValidRevokeReason(DefaultRevokeReason) {
		t.Errorf("DefaultRevokeReason %q is not itself valid", DefaultRevokeReason)
	}
	// An empty reason never reaches storage: callers fold it to the
	// default first, so validation deliberately refuses it.
	for _, r := range []string{"", "Refund", "REFUND", "none", "other"} {
		if ValidRevokeReason(r) {
			t.Errorf("ValidRevokeReason(%q) = true, want false", r)
		}
	}
}

// The refund columns are exactly what the 20261008_149000 migration
// created — pinned through generated SQL so a rename fails a test,
// not an INSERT (same doctrine as TestOrderAttributionColumnNames).
func TestRefundColumnNames(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	ins := &Refund{OrderID: "o1", AmountMinor: 1, Currency: "USD"}
	raw, err := db.NewInsert().Model(ins).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build insert: %v", err)
	}
	sqlText := string(raw)
	for _, col := range []string{
		"order_id", "payment_provider", "provider_ref", "trans_id",
		"amount_minor", "currency", "reason", "status", "refunded_by",
	} {
		if !strings.Contains(sqlText, col) {
			t.Errorf("generated INSERT does not name column %q; got:\n%s", col, sqlText)
		}
	}
}

// §80's licence columns are additive and nullable: an empty revoke
// provenance omits from JSON exactly like every other nullzero field,
// and a stamped one appears under the three snake_case keys.
func TestLicenseRevokeColumnsJSONShape(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	ins := &License{ID: "l1", ProductID: "p1", PlanID: "pl1", Email: "e@example.com"}
	raw, err := db.NewInsert().Model(ins).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build insert: %v", err)
	}
	for _, col := range []string{"revoke_reason", "revoked_by", "revoked_at"} {
		if !strings.Contains(string(raw), col) {
			t.Errorf("generated INSERT does not name column %q; got:\n%s", col, raw)
		}
	}

	out, err := json.Marshal(&License{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"revoke_reason", "revoked_by", "revoked_at"} {
		if strings.Contains(string(out), `"`+key+`"`) {
			t.Errorf("empty revoke provenance leaked key %q into JSON: %s", key, out)
		}
	}
}

// Money discipline (plan §51): AmountMinor is int64 minor units and
// must survive float64's 2^53 mantissa limit byte-exact on the wire.
func TestRefundMoneyIsExactInteger(t *testing.T) {
	const big = int64(1)<<53 + 1
	out, err := json.Marshal(&Refund{AmountMinor: big, Currency: "VND"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(out), `"amount_minor":9007199254740993`) {
		t.Errorf("amount_minor did not round-trip exactly as an integer: %s", out)
	}
}
