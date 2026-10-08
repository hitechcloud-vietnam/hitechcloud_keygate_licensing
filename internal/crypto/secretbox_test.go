package crypto

import (
	"strings"
	"testing"
)

// withSecretBox installs a box for the duration of one test and restores
// dev mode afterwards, so the process-wide key never leaks into another
// test. Tests in this file must not use t.Parallel() — they share the
// global.
func withSecretBox(t *testing.T, master string) {
	t.Helper()
	if err := ConfigureSecretBox(master); err != nil {
		t.Fatalf("ConfigureSecretBox(%q): %v", master, err)
	}
	t.Cleanup(func() { _ = ConfigureSecretBox("") })
}

const testMaster = "test-secret-box-master-key-32ch"

func TestSecretBoxRoundTrip(t *testing.T) {
	withSecretBox(t, testMaster)

	if !SecretBoxConfigured() {
		t.Fatal("SecretBoxConfigured() = false after configure")
	}
	for _, pt := range []string{"whsec_abcdefghijklmnopqrstuvwxyz0123456789abcd", "hunter2", "a", strings.Repeat("x", 5000)} {
		sealed := Seal(pt)
		if sealed == pt {
			t.Errorf("Seal(%q) returned plaintext; must be sealed when configured", pt)
		}
		if !IsSealed(sealed) {
			t.Errorf("Seal(%q) = %q, want IsSealed", pt, sealed)
		}
		if !strings.HasPrefix(sealed, SealedPrefix) {
			t.Errorf("Seal(%q) = %q, want %q prefix", pt, sealed, SealedPrefix)
		}
		got, err := Open(sealed)
		if err != nil {
			t.Errorf("Open(%q): %v", sealed, err)
			continue
		}
		if got != pt {
			t.Errorf("Open(Seal(%q)) = %q", pt, got)
		}
	}
}

// Two seals of the same plaintext must differ (fresh nonce per call) —
// otherwise an attacker learns equality of two secrets from the rows.
func TestSecretBoxSealIsRandomised(t *testing.T) {
	withSecretBox(t, testMaster)
	if Seal("same") == Seal("same") {
		t.Error("two seals of the same plaintext are identical; nonce is being reused")
	}
}

func TestSecretBoxEmptyAndUnconfigured(t *testing.T) {
	// Empty plaintext always seals to empty, configured or not.
	withSecretBox(t, testMaster)
	if got := Seal(""); got != "" {
		t.Errorf("Seal(%q) = %q, want empty", "", got)
	}
	if got, err := Open(""); err != nil || got != "" {
		t.Errorf("Open(%q) = %q, %v, want empty, nil", "", got, err)
	}

	// Dev mode: no key -> Seal is the identity, nothing is sealed.
	if err := ConfigureSecretBox(""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if SecretBoxConfigured() {
		t.Error("SecretBoxConfigured() = true after clear")
	}
	if got := Seal("plain-secret"); got != "plain-secret" {
		t.Errorf("Seal in dev mode = %q, want identity %q", got, "plain-secret")
	}
	if IsSealed(Seal("plain-secret")) {
		t.Error("dev-mode Seal must not mark the value sealed")
	}
	// A sealed value cannot be opened without a key: better a hard error
	// than the ciphertext returned as if it were the secret.
	if _, err := Open(SealedPrefix + "AAAA"); err == nil {
		t.Error("Open of a sealed value with no key must error")
	}
}

func TestSecretBoxLegacyPlaintextPassthrough(t *testing.T) {
	withSecretBox(t, testMaster)
	// A value stored before encryption was switched on has no prefix and
	// must come back byte-for-byte, for ever.
	for _, legacy := range []string{"old-plaintext-secret", "whsec_legacy0000000000000000000000000000000000"} {
		got, err := Open(legacy)
		if err != nil {
			t.Errorf("Open(%q): %v (legacy plaintext must pass through)", legacy, err)
			continue
		}
		if got != legacy {
			t.Errorf("Open(%q) = %q", legacy, got)
		}
	}
}

func TestSecretBoxTamperAndWrongKey(t *testing.T) {
	withSecretBox(t, testMaster)
	sealed := Seal("top-secret")

	// Flip a character inside the base64 payload -> must not open.
	broken := sealed[:len(sealed)-1] + string(flipped(sealed[len(sealed)-1]))
	if _, err := Open(broken); err == nil {
		t.Error("Open of a tampered value must error, never return plaintext")
	}

	// Truncated payload -> must not open.
	if _, err := Open(SealedPrefix + sealed[len(SealedPrefix):len(sealed)/2]); err == nil {
		t.Error("Open of a truncated value must error")
	}

	// Not base64 after the prefix -> must not open.
	if _, err := Open(SealedPrefix + "!!!not-base64!!!"); err == nil {
		t.Error("Open of a non-base64 value must error")
	}

	// Sealed under one key, opened under another -> must not open.
	if err := ConfigureSecretBox("a-completely-different-master-key"); err != nil {
		t.Fatalf("reconfigure: %v", err)
	}
	if _, err := Open(sealed); err == nil {
		t.Error("Open under a different key must error")
	}
}

func TestSecretBoxRejectsShortMaster(t *testing.T) {
	// Start from a known-empty state so the assertions don't depend on
	// which tests ran before.
	if err := ConfigureSecretBox(""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := ConfigureSecretBox("short"); err == nil {
		t.Error("ConfigureSecretBox with a too-short key must error")
	}
	if SecretBoxConfigured() {
		t.Error("a rejected key must not leave a box installed")
	}
}

// flipped returns a different base64 character for c (or 'A' if c is not
// one), so the payload changes without producing an invalid base64 byte.
func flipped(c byte) byte {
	if c == 'A' {
		return 'B'
	}
	return 'A'
}
