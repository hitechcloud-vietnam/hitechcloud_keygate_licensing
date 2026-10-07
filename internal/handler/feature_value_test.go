package handler

import (
	"strconv"
	"strings"
	"testing"
)

// A feature value is read back without a second check anywhere: a
// quota that is not a number parses as 0, and 0 means "no limit", so
// a typo in the admin form would hand out the feature uncapped. A
// bool that is not exactly "true" reads as false. Both are refused
// at the door instead.
func TestNormalizeFeatureValue(t *testing.T) {
	for _, tc := range []struct {
		valueType, value, period string
		ok                       bool
	}{
		// A period on a feature that is not a quota is cleared, the
		// way a plan's update terms are cleared when it is not
		// perpetual — an addon that once was a quota has to be able
		// to become something else in one request.
		{"bool", "true", "", true},
		{"bool", "false", "", true},
		{"bool", "yes", "", false},
		{"bool", "", "", false},
		{"flag", "true", "", true},
		{"flag", "1", "", false},
		{"int", "10", "", true},
		{"int", " 10 ", "", true},
		{"int", "10k", "", false},
		{"int", "1_000", "", false},
		{"int", "-1", "", false},
		{"quota", "1000", "monthly", true},
		{"quota", "0", "daily", true}, // 0 is the documented "no limit"
		{"quota", "abc", "monthly", false},
		{"quota", "10.5", "monthly", false},
		{"quota", "1000", "", true}, // empty period reads as monthly
		{"quota", "1000", "weekly", false},
		{"quota", "1000", "Monthly", false},
		{"string", "enterprise", "", true},
		{"string", "anything at all", "", true},
		{"string", "x", "monthly", true}, // the period is dropped, not refused
		{"bool", "true", "daily", true},
	} {
		got, period, err := normalizeFeatureValue(tc.valueType, tc.value, tc.period)
		if (err == nil) != tc.ok {
			t.Errorf("normalizeFeatureValue(%q, %q, %q) = %v, want ok=%v",
				tc.valueType, tc.value, tc.period, err, tc.ok)
			continue
		}
		// What comes back is what gets stored, and the readers do not
		// trim: strconv.ParseInt(" 10 ") fails the same way "ten"
		// does, and a bool is compared to "true" exactly.
		if err == nil && got != strings.TrimSpace(tc.value) {
			t.Errorf("normalizeFeatureValue(%q, %q, %q) returned %q, want it stored trimmed",
				tc.valueType, tc.value, tc.period, got)
		}
		// The period belongs to a quota and to nothing else.
		if err == nil && tc.valueType != "quota" && period != "" {
			t.Errorf("normalizeFeatureValue(%q, %q, %q) kept the period %q",
				tc.valueType, tc.value, tc.period, period)
		}
	}
}

// The value that comes back is the one written to the database, so a
// quota typed with spaces must still parse where it is read.
func TestNormalizedQuotaSurvivesTheReader(t *testing.T) {
	got, period, err := normalizeFeatureValue("quota", "  1000  ", "monthly")
	if err != nil {
		t.Fatalf("a padded quota should be accepted: %v", err)
	}
	n, perr := strconv.ParseInt(got, 10, 64)
	if perr != nil || n != 1000 {
		t.Fatalf("service/usage.go would read %q as %d (%v), want 1000", got, n, perr)
	}
	if period != "monthly" {
		t.Fatalf("the quota's period was lost: %q", period)
	}
}
