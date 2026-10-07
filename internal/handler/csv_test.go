package handler

import "testing"

func TestCSVSafeRow(t *testing.T) {
	in := []string{"lic_1", "+foo@x.com", "=SUM(A1)", "-1", "@bar", "\tx", "Pro", "", "2026-01-01T00:00:00Z"}
	want := []string{"lic_1", "'+foo@x.com", "'=SUM(A1)", "'-1", "'@bar", "'\tx", "Pro", "", "2026-01-01T00:00:00Z"}
	got := csvSafeRow(in)
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cell %d: got %q, want %q", i, got[i], want[i])
		}
	}
}
