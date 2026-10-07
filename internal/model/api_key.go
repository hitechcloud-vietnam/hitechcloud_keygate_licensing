package model

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/uptrace/bun"
)

// ─── Customer API Key (portal self-service credential) ───
//
// A customer mints these in the portal (plan §29 "API Keys", §34 key
// management) to authenticate machine access to their own resources.
// They are a DIFFERENT credential from the operator/server-to-server
// APIKey in model.go: that one is minted by an admin for a product and
// speaks scopes over admin routes; this one is minted by the customer
// and speaks only for their own account. Two credentials, two
// lifecycles — hence the customer_api_keys table (the name api_keys
// has been taken since 20260320_init).
//
// The secret is returned exactly once, at creation, and is NOT
// recoverable afterwards. Only KeyPrefix (safe to display) and KeyHash
// (SHA-256 hex of the full secret — the same recipe as
// store.HashAPIKey and license.HashKey) are stored. Plaintext secrets
// never touch the database.
const (
	// CustomerAPIKeySecretPrefix marks the credential. It deliberately
	// does NOT collide with the kg_live_ prefix the product-key auth
	// middleware routes on, so one namespace cannot be mistaken for
	// the other at a glance or in a log.
	CustomerAPIKeySecretPrefix = "htc_sk_"
	// customerAPIKeySecretLength is the length of the random part:
	// 40 characters of a 32-character alphabet = 200 bits of entropy.
	customerAPIKeySecretLength = 40
	// customerAPIKeyAlphabet excludes 0/O and 1/I/L: a customer who
	// reads a key aloud or retypes it from a password manager must
	// never have to guess which glyph they saw.
	customerAPIKeyAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	// CustomerAPIKeyPrefixLength is how much of the secret is kept for
	// display ("htc_sk_" + 4 random characters): enough to tell two
	// keys apart in a list, useless to anyone who does not hold the
	// full secret.
	CustomerAPIKeyPrefixLength = 12
)

// CustomerAPIKey is one customer-minted portal credential.
//
// Scopes is a plain comma-separated text column (may be empty — an
// empty scope list is the default and is validated as "carries no
// extra permission"), not a Postgres array: the column is read by
// humans in the portal and written by one writer only, and a text
// column keeps the row printable in any SQL client.
//
// Revocation is soft (RevokedAt) so the row keeps its audit trail:
// who created it, when it was last used, when it died. Hard deletion
// exists (store.DeleteCustomerAPIKey) but is not the portal path.
type CustomerAPIKey struct {
	bun.BaseModel `bun:"table:customer_api_keys"`

	ID     string `bun:",pk" json:"id"`
	UserID string `bun:",notnull" json:"user_id"`
	// Name is the customer's own label ("CI runner", "Laptop CLI").
	Name string `bun:",notnull" json:"name"`
	// KeyPrefix is the first CustomerAPIKeyPrefixLength characters of
	// the secret. Displayable; useless for authentication.
	KeyPrefix string `bun:",notnull" json:"key_prefix"`
	// KeyHash is the SHA-256 hex of the full secret. Never exposed.
	KeyHash string `bun:",notnull,unique" json:"-"`
	// Scopes is the comma-separated permission list; empty is allowed.
	Scopes string `bun:",notnull,default:''" json:"scopes,omitempty"`
	// ExpiresAt bounds the key's life. Nil means it never expires.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	// LastUsedAt is stamped by store.TouchCustomerAPIKeyLastUsed on
	// successful authentication.
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	// RevokedAt is the soft-revoke stamp. A revoked key is dead even
	// before any expiry it may carry.
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	CreatedAt time.Time  `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt time.Time  `bun:",nullzero,default:now()" json:"updated_at"`
}

// NewCustomerAPIKeySecret draws a fresh secret:
// CustomerAPIKeySecretPrefix + 40 characters from the unambiguous
// alphabet, every draw from crypto/rand (no math/rand, no clock, no
// counters — the secret IS the credential).
func NewCustomerAPIKeySecret() (string, error) {
	max := big.NewInt(int64(len(customerAPIKeyAlphabet)))
	out := make([]byte, customerAPIKeySecretLength)
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("api key: generate secret: %w", err)
		}
		out[i] = customerAPIKeyAlphabet[n.Int64()]
	}
	return CustomerAPIKeySecretPrefix + string(out), nil
}

// CustomerAPIKeyDisplayPrefix returns the part of the secret that is
// safe to keep and show: its first CustomerAPIKeyPrefixLength
// characters. A secret shorter than that (only possible if a caller
// hands in something that was not made by NewCustomerAPIKeySecret) is
// returned unchanged rather than panicked on.
func CustomerAPIKeyDisplayPrefix(secret string) string {
	if len(secret) > CustomerAPIKeyPrefixLength {
		return secret[:CustomerAPIKeyPrefixLength]
	}
	return secret
}

// ScopeList splits the stored comma-separated scopes into clean
// entries: whitespace-trimmed, empties dropped. The input is stored
// folded (see the portal handler's normalizer) so this is mostly a
// reader for auth checks and the UI.
func (k *CustomerAPIKey) ScopeList() []string {
	if k == nil || k.Scopes == "" {
		return nil
	}
	parts := strings.Split(k.Scopes, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// HasScope reports whether the key carries the given scope.
func (k *CustomerAPIKey) HasScope(scope string) bool {
	for _, s := range k.ScopeList() {
		if s == scope {
			return true
		}
	}
	return false
}

// IsRevoked reports whether the key was revoked (soft). A nil key is
// not "revoked" — it is nothing at all; UsableAt is the validation
// gate and answers false for it.
func (k *CustomerAPIKey) IsRevoked() bool {
	return k != nil && k.RevokedAt != nil
}

// UsableAt reports whether the key validates at time t: not revoked,
// and not past its expiry. An expiry exactly AT t is already past — a
// key that dies at midnight does not work at midnight. A nil key is
// never usable, so a failed lookup cannot authenticate by accident.
func (k *CustomerAPIKey) UsableAt(t time.Time) bool {
	if k == nil || k.RevokedAt != nil {
		return false
	}
	if k.ExpiresAt != nil && !k.ExpiresAt.After(t) {
		return false
	}
	return true
}
