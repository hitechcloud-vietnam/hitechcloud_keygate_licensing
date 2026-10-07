package model

import (
	"strings"
	"testing"
	"time"
)

// The secret IS the credential (plan §34: never display it again after
// creation), so its shape is worth pinning down without a database:
// the prefix that namespaces it, the alphabet that makes it readable,
// and the length that makes it strong.

func TestNewCustomerAPIKeySecretShape(t *testing.T) {
	secret, err := NewCustomerAPIKeySecret()
	if err != nil {
		t.Fatalf("NewCustomerAPIKeySecret: %v", err)
	}
	if !strings.HasPrefix(secret, CustomerAPIKeySecretPrefix) {
		t.Errorf("secret %q does not start with %q", secret, CustomerAPIKeySecretPrefix)
	}
	if want := len(CustomerAPIKeySecretPrefix) + customerAPIKeySecretLength; len(secret) != want {
		t.Errorf("secret length = %d, want %d", len(secret), want)
	}
	for i, r := range secret[len(CustomerAPIKeySecretPrefix):] {
		if !strings.ContainsRune(customerAPIKeyAlphabet, r) {
			t.Errorf("secret character %d = %q is not in the unambiguous alphabet", i, r)
		}
	}
	// The namespace must not collide with the product-key prefix the
	// auth middleware routes on (kg_live_).
	if strings.HasPrefix(secret, "kg_live_") {
		t.Error("secret collides with the product API key namespace")
	}
}

// Two keys that looked the same would be one credential wearing two
// names. 200 bits of entropy makes a collision fantastically
// unlikely; a missing randomness source would make it certain.
func TestNewCustomerAPIKeySecretsDiffer(t *testing.T) {
	a, err := NewCustomerAPIKeySecret()
	if err != nil {
		t.Fatalf("NewCustomerAPIKeySecret: %v", err)
	}
	b, err := NewCustomerAPIKeySecret()
	if err != nil {
		t.Fatalf("NewCustomerAPIKeySecret: %v", err)
	}
	if a == b {
		t.Errorf("two draws produced the same secret %q", a)
	}
}

// The display prefix must be enough to tell keys apart and useless for
// authenticating as one.
func TestCustomerAPIKeyDisplayPrefix(t *testing.T) {
	secret, err := NewCustomerAPIKeySecret()
	if err != nil {
		t.Fatalf("NewCustomerAPIKeySecret: %v", err)
	}
	prefix := CustomerAPIKeyDisplayPrefix(secret)
	if len(prefix) != CustomerAPIKeyPrefixLength {
		t.Errorf("prefix length = %d, want %d", len(prefix), CustomerAPIKeyPrefixLength)
	}
	if prefix != secret[:CustomerAPIKeyPrefixLength] {
		t.Errorf("prefix %q is not the first %d characters of the secret", prefix, CustomerAPIKeyPrefixLength)
	}
	if !strings.HasPrefix(prefix, CustomerAPIKeySecretPrefix) {
		t.Errorf("prefix %q should still carry the namespace", prefix)
	}
	// A short input is returned unchanged rather than panicked on.
	if got := CustomerAPIKeyDisplayPrefix("tiny"); got != "tiny" {
		t.Errorf("short secret prefix = %q, want it unchanged", got)
	}
}

func TestCustomerAPIKeyScopeList(t *testing.T) {
	for _, tc := range []struct {
		name   string
		scopes string
		want   []string
	}{
		{"empty", "", nil},
		{"only separators", " , , ", nil},
		{"one", "orders:read", []string{"orders:read"}},
		{"needs folding", " a , b ,,c ", []string{"a", "b", "c"}},
		{"already folded", "a,b", []string{"a", "b"}},
	} {
		k := &CustomerAPIKey{Scopes: tc.scopes}
		got := k.ScopeList()
		if len(got) != len(tc.want) {
			t.Errorf("%s: ScopeList() = %v, want %v", tc.name, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%s: ScopeList()[%d] = %q, want %q", tc.name, i, got[i], tc.want[i])
			}
		}
	}
}

func TestCustomerAPIKeyHasScope(t *testing.T) {
	k := &CustomerAPIKey{Scopes: "orders:read,usage:read"}
	if !k.HasScope("orders:read") {
		t.Error("HasScope(orders:read) = false, want true")
	}
	if k.HasScope("orders:write") {
		t.Error("HasScope(orders:write) = true, want false")
	}
	empty := &CustomerAPIKey{}
	if empty.HasScope("orders:read") {
		t.Error("a key with no scopes must carry no scope")
	}
}

// Revoked and expired keys must not validate. An expiry exactly at the
// moment of the check is already past: a key that dies at midnight
// does not work at midnight.
func TestCustomerAPIKeyUsableAt(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	for _, tc := range []struct {
		name string
		key  *CustomerAPIKey
		want bool
	}{
		{"nil key", nil, false},
		{"fresh key", &CustomerAPIKey{}, true},
		{"revoked", &CustomerAPIKey{RevokedAt: &past}, false},
		{"expired", &CustomerAPIKey{ExpiresAt: &past}, false},
		{"expiring right now", &CustomerAPIKey{ExpiresAt: &now}, false},
		{"expiring later", &CustomerAPIKey{ExpiresAt: &future}, true},
		{"revoked AND expiring later", &CustomerAPIKey{RevokedAt: &past, ExpiresAt: &future}, false},
	} {
		if got := tc.key.UsableAt(now); got != tc.want {
			t.Errorf("%s: UsableAt = %v, want %v", tc.name, got, tc.want)
		}
		if got := tc.key.IsRevoked(); got != (tc.key != nil && tc.key.RevokedAt != nil) {
			t.Errorf("%s: IsRevoked = %v, want %v", tc.name, got, tc.key != nil && tc.key.RevokedAt != nil)
		}
	}
}
