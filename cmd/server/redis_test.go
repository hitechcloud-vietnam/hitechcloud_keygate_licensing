package main

import (
	"strings"
	"testing"
)

func TestParseRedisURL(t *testing.T) {
	cases := []struct {
		in, addr string
		db       int
		tls      bool
	}{
		{"redis://localhost:6379/2", "localhost:6379", 2, false},
		{"localhost:6380", "localhost:6380", 0, false},
		{"rediss://cache.example.com:6380/0", "cache.example.com:6380", 0, true},
	}
	for _, c := range cases {
		opt, err := parseRedisURL(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if opt.Addr != c.addr || opt.DB != c.db || (opt.TLSConfig != nil) != c.tls {
			t.Fatalf("%q: got addr=%s db=%d tls=%v", c.in, opt.Addr, opt.DB, opt.TLSConfig != nil)
		}
	}
	if _, err := parseRedisURL("http://localhost:6379"); err == nil {
		t.Fatal("a non-redis scheme must be rejected")
	}
}

func TestRedisNamespaceIsStablePerBaseURL(t *testing.T) {
	a, b := redisNamespace("https://a.example.com"), redisNamespace("https://b.example.com")
	if a == b || a != redisNamespace("https://a.example.com") || !strings.HasSuffix(a, ":") {
		t.Fatalf("namespaces: %q %q", a, b)
	}
	// Replicas whose BASE_URL differs only by case or a trailing slash
	// must still share one namespace, or they silently stop sharing limits.
	if a != redisNamespace("https://A.example.com/") {
		t.Fatal("trailing slash / case changed the namespace")
	}
}

// A malformed URL's error is logged, so it must not contain the password.
func TestParseRedisURLErrorHidesPassword(t *testing.T) {
	for _, u := range []string{
		"redis://:s3cretPW@host:notaport/0",
		"redis://user:s3cretPW@host:6379/abc",
		"redis://user:s3cretPW@[::1/0",
	} {
		_, err := parseRedisURL(u)
		if err == nil {
			t.Fatalf("%q: expected an error", u)
		}
		if strings.Contains(err.Error(), "s3cretPW") {
			t.Fatalf("%q: error leaks the password: %v", u, err)
		}
	}
}
