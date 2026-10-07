package middleware

import (
	"testing"
	"time"
)

// A lockout with 59.9 seconds left must not tell the client 59: it
// comes back a moment early, gets another 429, and has learnt nothing.
func TestRetryAfterSeconds(t *testing.T) {
	for _, tc := range []struct {
		left time.Duration
		want int
	}{
		{59900 * time.Millisecond, 60},
		{100 * time.Millisecond, 1},
		{time.Second, 1},
		{1500 * time.Millisecond, 2},
		{0, 1},
		{-time.Second, 1},
		{30 * time.Minute, 1800},
	} {
		if got := RetryAfterSeconds(tc.left); got != tc.want {
			t.Errorf("RetryAfterSeconds(%v) = %d, want %d", tc.left, got, tc.want)
		}
	}
}
