package handler

import "testing"

// A perpetual plan is paid once, so it never carries an interval; a
// subscription keeps whatever valid interval it was given.
func TestNormalizeBillingInterval(t *testing.T) {
	cases := []struct {
		licenseType, in, want string
		wantErr               bool
	}{
		{"subscription", "month", "month", false},
		{"subscription", "year", "year", false},
		{"subscription", "", "", false},
		{"perpetual", "month", "", false},
		{"perpetual", "", "", false},
		{"trial", "year", "year", false},
		{"subscription", "week", "", true},
		{"perpetual", "daily", "", true},
	}
	for _, tc := range cases {
		got, err := normalizeBillingInterval(tc.licenseType, tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s/%q: err=%v wantErr=%v", tc.licenseType, tc.in, err, tc.wantErr)
			continue
		}
		if got != tc.want {
			t.Errorf("%s/%q: got %q want %q", tc.licenseType, tc.in, got, tc.want)
		}
	}
}
