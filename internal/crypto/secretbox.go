package crypto

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
)

// SealedPrefix marks a value that has been sealed at rest by the secret
// box. The wire format is:
//
//	enc:v1:<base64( nonce || ciphertext || tag )>
//
// where the inner blob is exactly what AESGCM.Encrypt returns (a fresh
// 12-byte nonce prepended to the AES-256-GCM ciphertext, the GCM tag
// included at the end). The "v1" gives us a place to change the
// construction later without orphaning rows: a future "enc:v2:" can be
// detected and upgraded while "enc:v1:" keeps opening.
//
// A value WITHOUT this prefix is legacy plaintext written before
// encryption-at-rest was switched on. See Open for why those stay
// readable forever.
const SealedPrefix = "enc:v1:"

// secretBox is the process-wide AEAD used to seal secrets at rest. It is
// optional: a nil box means "dev mode", where Seal is the identity and
// secrets are stored in the clear (see SecretBoxConfigured).
//
// atomic.Pointer because Seal/Open may run on any request goroutine
// while ConfigureSecretBox runs once at boot; the load/store are
// cheap and the AESGCM value is immutable once built.
var secretBox atomic.Pointer[AESGCM]

// ConfigureSecretBox derives the AES-256-GCM key used to seal secrets at
// rest and installs it process-wide. The key is derived from master via
// DeriveSubkey with the purpose "secrets-at-rest-v1" — a NEW purpose
// string, so ciphertexts produced here can never be replayed under the
// license-key or release-signing subkeys (and vice versa). The shared
// purposePrefix inside DeriveSubkey is untouched.
//
// master is the raw key material (it is fed to HKDF as bytes, NOT
// hex-decoded). Use a stable, high-entropy string of at least 16
// characters — e.g. `openssl rand -base64 32`. The exact same string
// must be supplied on every boot: changing it orphans every previously
// sealed value (they can no longer be opened). Keep it out of the repo.
//
// Passing an empty master clears the box and puts the secret box into
// dev mode: Seal becomes the identity, so secrets are stored in the
// clear. This is deliberate for local development and is exactly what a
// missing SECRET_ENCRYPTION_KEY produces; production installs that store
// live credentials must set one. Use SecretBoxConfigured to warn at boot.
//
// An error is returned only when a non-empty master is unusable (shorter
// than DeriveSubkey's minimum), so a misconfiguration is loud at startup
// rather than a mystery at the first read.
func ConfigureSecretBox(master string) error {
	master = strings.TrimSpace(master)
	if master == "" {
		secretBox.Store(nil) // dev mode
		return nil
	}
	if len(master) < 16 {
		return fmt.Errorf("crypto: secret encryption key too short: need at least 16 characters, got %d", len(master))
	}
	sub, err := DeriveSubkey([]byte(master), "secrets-at-rest-v1")
	if err != nil {
		return err
	}
	box, err := NewAESGCM(sub)
	if err != nil {
		return err
	}
	secretBox.Store(box)
	return nil
}

// SecretBoxConfigured reports whether a master key has been installed. It
// exists so main() can emit a startup warning when secrets will be stored
// in the clear (dev mode) instead of silently shipping a plaintext
// credential into the database.
func SecretBoxConfigured() bool {
	return secretBox.Load() != nil
}

// IsSealed reports whether a stored value carries the SealedPrefix and so
// must be opened before use. A false result means either legacy plaintext
// or an empty value — both are passed through unchanged by Open.
func IsSealed(value string) bool {
	return strings.HasPrefix(value, SealedPrefix)
}

// Seal encrypts a secret for storage. The result is
// SealedPrefix + base64(nonce || ciphertext || tag).
//
//   - Empty plaintext is returned empty: there is nothing to protect and
//     the empty string is how "unset" is spelled across the settings
//     layer, so it must round-trip as empty.
//   - In dev mode (no master key) Seal is the identity and returns the
//     plaintext unchanged. This keeps local development working with no
//     configuration; production must set SECRET_ENCRYPTION_KEY (see
//     SecretBoxConfigured).
//   - A plaintext secret is never returned to the caller as its own
//     ciphertext: when a key IS configured the value is always sealed.
//
// Seal cannot fail in a way the caller can act on (the only failure mode
// is a broken system CSPRNG, which would already have compromised every
// other secret), so rather than silently degrade to storing plaintext —
// the exact leak sealing exists to prevent — a derivation/RNG failure
// panics loudly.
func Seal(plaintext string) string {
	if plaintext == "" {
		return ""
	}
	box := secretBox.Load()
	if box == nil {
		return plaintext // dev mode: identity
	}
	ct, err := box.Encrypt([]byte(plaintext), nil)
	if err != nil {
		panic("crypto: secretbox seal: " + err.Error())
	}
	return SealedPrefix + base64.StdEncoding.EncodeToString(ct)
}

// Open reverses Seal and is the read-side trust boundary: it returns the
// plaintext that a sealed value hides.
//
// Mixed-dormant migration contract: a value WITHOUT the SealedPrefix is
// legacy plaintext written before encryption was switched on, and is
// returned UNCHANGED. Old rows therefore stay readable forever; only new
// writes are sealed. This is what makes switching encryption on a
// non-event for existing data.
//
// A sealed value is decrypted with the current key. If it cannot be —
// tampered with, or sealed under a different key (e.g. after rotating
// SECRET_ENCRYPTION_KEY) — Open returns an error rather than handing back
// a ciphertext that a caller would mistake for a live credential.
func Open(value string) (string, error) {
	if !IsSealed(value) {
		return value, nil // legacy plaintext (or empty): passthrough
	}
	box := secretBox.Load()
	if box == nil {
		return "", errors.New("crypto: value is sealed but the secret box is not configured (set SECRET_ENCRYPTION_KEY)")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, SealedPrefix))
	if err != nil {
		return "", errors.New("crypto: sealed value is not valid base64")
	}
	pt, err := box.Decrypt(raw, nil)
	if err != nil {
		// Tampering, wrong key, or wrong construction — don't reveal
		// which. The caller treats every case as "unreadable secret".
		return "", errors.New("crypto: sealed value could not be opened (wrong key or corrupt)")
	}
	return string(pt), nil
}
