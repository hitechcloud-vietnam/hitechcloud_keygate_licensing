package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// public_key must look like a base64 ed25519 key (length 32..128).
func fakePublicKey(tag string) string {
	return tag + "-" + strings.Repeat("A", 40)
}

// The column has a CHECK on octet_length (60..256): a real AEAD blob
// is nonce + 32-byte seed + tag. The test only needs the shape.
func fakeEncryptedSeed(fill byte) []byte {
	b := make([]byte, 72)
	for i := range b {
		b[i] = fill
	}
	return b
}

// Rotation runs under a per-product advisory lock. The lock statement
// used a "$1" placeholder that Bun does not substitute, so every
// rotation failed before touching a row.
func TestRotateSigningKey_Atomic(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	lic := createTestLicense(t, s, ctx)
	defer func() {
		_, _ = s.DB.NewRaw("DELETE FROM release_signing_keys WHERE product_id = ?", lic.ProductID).Exec(ctx)
	}()

	k1 := &model.ReleaseSigningKey{ProductID: lic.ProductID, PublicKey: fakePublicKey("pk1"), PrivateKeyEncrypted: fakeEncryptedSeed(1)}
	if err := s.RotateSigningKey(ctx, k1, ""); err != nil {
		t.Fatalf("first rotate (no prior key): %v", err)
	}
	k2 := &model.ReleaseSigningKey{ProductID: lic.ProductID, PublicKey: fakePublicKey("pk2"), PrivateKeyEncrypted: fakeEncryptedSeed(2)}
	if err := s.RotateSigningKey(ctx, k2, "compromised laptop"); err != nil {
		t.Fatalf("second rotate: %v", err)
	}

	var active, note string
	if err := s.DB.NewRaw("SELECT public_key FROM release_signing_keys WHERE product_id = ? AND active", lic.ProductID).Scan(ctx, &active); err != nil {
		t.Fatalf("active key: %v", err)
	}
	if active != fakePublicKey("pk2") {
		t.Errorf("active key = %q, want pk2", active)
	}
	if err := s.DB.NewRaw("SELECT note FROM release_signing_keys WHERE public_key = ?", fakePublicKey("pk1")).Scan(ctx, &note); err != nil {
		t.Fatalf("old key: %v", err)
	}
	if note != "compromised laptop" {
		t.Errorf("rotation note should land on the retired key, got %q", note)
	}
}
