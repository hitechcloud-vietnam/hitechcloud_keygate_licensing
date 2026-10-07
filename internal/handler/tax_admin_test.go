package handler

import (
	"math"
	"strings"
	"testing"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/tax"
)

// The stored spelling is canonical so the unique index and the
// checkout lookup meet one form of a jurisdiction, not several.
func TestTaxRateJurisdictionNormalization(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"US-CA", "US-CA", false},
		{" us-ca ", "US-CA", false},
		{"vat-vn", "VAT-VN", false},
		{"  vat-vn  ", "VAT-VN", false},
		{"", "", true},
		{"   ", "", true},
		{strings.Repeat("J", 65), "", true},
	}
	for _, tc := range cases {
		got, err := normalizeTaxRateJurisdiction(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("normalizeTaxRateJurisdiction(%q): err=%v wantErr=%v", tc.in, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("normalizeTaxRateJurisdiction(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Zero is a legal rate (zero-rated); negative is the engine's refusal,
// and anything past 1000% is this table's own cap.
func TestTaxRateBasisPointsValidation(t *testing.T) {
	cases := []struct {
		bps     int64
		wantErr bool
	}{
		{0, false},
		{888, false},
		{10_000, false},
		{100_000, false},
		{100_001, true},
		{-1, true},
		{math.MinInt64, true},
	}
	for _, tc := range cases {
		err := validateTaxRateBasisPoints(tc.bps)
		if (err != nil) != tc.wantErr {
			t.Errorf("validateTaxRateBasisPoints(%d): err=%v wantErr=%v", tc.bps, err, tc.wantErr)
		}
	}
}

// Both write paths run the row through prepareTaxRate, which
// normalizes in place; the stored values are what it leaves behind.
func TestTaxRatePrepareNormalizesInPlace(t *testing.T) {
	r := &model.TaxRate{
		Jurisdiction: " vat-vn ",
		BasisPoints:  1_000,
		Country:      " VN ",
		Region:       "  ",
		Description:  "value added tax",
	}
	if err := prepareTaxRate(r); err != nil {
		t.Fatalf("prepareTaxRate: unexpected err %v", err)
	}
	if r.Jurisdiction != "VAT-VN" {
		t.Errorf("Jurisdiction = %q, want %q", r.Jurisdiction, "VAT-VN")
	}
	if r.Country != "VN" {
		t.Errorf("Country = %q, want %q", r.Country, "VN")
	}
	if r.Region != "" {
		t.Errorf("Region = %q, want empty", r.Region)
	}

	bad := &model.TaxRate{Jurisdiction: "", BasisPoints: 100}
	if err := prepareTaxRate(bad); err == nil {
		t.Error("prepareTaxRate: empty jurisdiction accepted")
	}
	bad = &model.TaxRate{Jurisdiction: "US-CA", BasisPoints: -5}
	if err := prepareTaxRate(bad); err == nil {
		t.Error("prepareTaxRate: negative basis_points accepted")
	}
}

// The row is configuration and the engine is the arithmetic; ToEngine
// is the whole of the bridge between them, so what it hands over had
// better compute.
func TestTaxRateToEngine(t *testing.T) {
	r := &model.TaxRate{Jurisdiction: "US-CA", BasisPoints: 888}
	got := r.ToEngine()
	want := tax.Rate{BasisPoints: 888, Jurisdiction: "US-CA"}
	if got != want {
		t.Fatalf("ToEngine() = %+v, want %+v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("engine refuses the converted rate: %v", err)
	}

	// 8.88% exclusive on 100.00: tax 8.88, gross 108.88.
	bd, err := tax.Calculate(10_000, got, false, tax.RoundingHalfUp)
	if err != nil {
		t.Fatalf("Calculate: %v", err)
	}
	if bd.Net != 10_000 || bd.Tax != 888 || bd.Gross != 10_888 {
		t.Errorf("Calculate = %+v, want {Net:10000 Tax:888 Gross:10888}", bd)
	}

	// The same rate inclusive of a 100.00 gross price.
	bd, err = tax.Calculate(10_000, got, true, tax.RoundingHalfUp)
	if err != nil {
		t.Fatalf("Calculate inclusive: %v", err)
	}
	if bd.Net+bd.Tax != bd.Gross || bd.Gross != 10_000 {
		t.Errorf("Calculate inclusive = %+v, want gross 10000 with Net+Tax == Gross", bd)
	}
}

// CRUD over HTTP needs a PostgreSQL test database (store.RunMigrations);
// the validation and conversion those handlers share is covered above.
func TestTaxAdminCRUDAgainstDatabase(t *testing.T) {
	t.Skip("requires a PostgreSQL test database; run with the integration suite")
}
