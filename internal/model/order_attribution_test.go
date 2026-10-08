package model

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// The attribution columns name exactly what the 20261008_141000
// migration created and what the session metadata keys say
// (payment/attribution.go mirrors these column names one-to-one), so
// the generated SQL is pinned here the way the SSO acronym fields
// are (TestSSOConnectionColumnNames): a rename on either side must
// fail a test, not an INSERT.
func TestOrderAttributionColumnNames(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	ins := &Order{ID: "x", OrderNumber: "n", CustomerEmail: "e", Currency: "USD"}
	raw, err := db.NewInsert().Model(ins).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build insert: %v", err)
	}
	sqlText := string(raw)
	for _, col := range []string{"reseller_id", "reseller_email", "referral_code", "affiliate_id"} {
		if !strings.Contains(sqlText, col) {
			t.Errorf("generated INSERT does not name column %q; got:\n%s", col, sqlText)
		}
	}
}

// On the wire the attribution is exactly the four snake_case keys,
// and "no attribution" is their absence (omitempty on the empty
// string, the one representation NULL has in the database).
func TestOrderAttributionJSONShape(t *testing.T) {
	raw, err := json.Marshal(&Order{})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"reseller_id", "reseller_email", "referral_code", "affiliate_id"} {
		if strings.Contains(string(raw), `"`+key+`"`) {
			t.Errorf("empty attribution leaked key %q into JSON: %s", key, raw)
		}
	}

	raw, err = json.Marshal(&Order{
		ResellerID: "res_1", ResellerEmail: "partner@example.com",
		ReferralCode: "SUMMER26", AffiliateID: "aff_1",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, key := range []string{"reseller_id", "reseller_email", "referral_code", "affiliate_id"} {
		if !strings.Contains(string(raw), `"`+key+`"`) {
			t.Errorf("attribution JSON is missing key %q: %s", key, raw)
		}
	}
}
