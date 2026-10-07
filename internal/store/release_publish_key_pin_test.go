package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// Two publishes overlapping a key rotation must not produce a shipped
// release with mixed signatures: the gate pins one key, and once a
// release is published its signatures cannot be rewritten.
func TestPublishRelease_PinsSigningKeyAndFreezesSignatures(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	relID, artID := setupSignedDraft(t, s, ctx, true)
	var keyID, sigBefore string
	if err := s.DB.NewRaw("SELECT signing_key_id, ed25519_sig FROM release_artifacts WHERE id = ?", artID).Scan(ctx, &keyID, &sigBefore); err != nil {
		t.Fatalf("read artifact: %v", err)
	}

	// Signed with key A, but the publisher pinned key B → not publishable
	// as-is; the caller retries and re-signs with one key.
	if err := s.PublishRelease(ctx, relID, true, "some-other-key"); !errors.Is(err, store.ErrReleaseArtifactsNotSigned) {
		t.Fatalf("publish with a different pinned key: got %v, want ErrReleaseArtifactsNotSigned", err)
	}
	if err := s.PublishRelease(ctx, relID, true, keyID); err != nil {
		t.Fatalf("publish with the matching key: %v", err)
	}

	// The slower publisher now tries to write its signature onto the
	// shipped release.
	if err := s.UpdateArtifactSignature(ctx, artID, "late-sig", keyID, ""); !errors.Is(err, store.ErrReleaseNotPublishable) {
		t.Fatalf("signature write on a published release: got %v, want ErrReleaseNotPublishable", err)
	}
	var sigAfter string
	if err := s.DB.NewRaw("SELECT ed25519_sig FROM release_artifacts WHERE id = ?", artID).Scan(ctx, &sigAfter); err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if sigAfter != sigBefore {
		t.Fatalf("shipped signature changed: %q -> %q", sigBefore, sigAfter)
	}
	if err := s.UpdateArtifactSignature(ctx, "no-such-artifact", "x", keyID, ""); !errors.Is(err, store.ErrArtifactNotFound) {
		t.Fatalf("unknown artifact: got %v, want ErrArtifactNotFound", err)
	}
}

func TestClearReleaseSignatures_OnlyOnDrafts(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	relID, artID := setupSignedDraft(t, s, ctx, true)
	if err := s.ClearReleaseSignatures(ctx, relID); err != nil {
		t.Fatalf("clear on draft: %v", err)
	}
	var sig string
	var keyID *string
	if err := s.DB.NewRaw("SELECT ed25519_sig, signing_key_id FROM release_artifacts WHERE id = ?", artID).Scan(ctx, &sig, &keyID); err != nil {
		t.Fatalf("read: %v", err)
	}
	if sig != "" || keyID != nil {
		t.Fatalf("draft signatures not cleared: sig=%q key=%v", sig, keyID)
	}

	// Publish unsigned, then a clear must be a no-op on the shipped row.
	if err := s.PublishRelease(ctx, relID, false, ""); err != nil {
		t.Fatalf("publish unsigned: %v", err)
	}
	relID2, artID2 := setupSignedDraft(t, s, ctx, true)
	if err := s.PublishRelease(ctx, relID2, true, ""); err != nil {
		t.Fatalf("publish signed: %v", err)
	}
	if err := s.ClearReleaseSignatures(ctx, relID2); !errors.Is(err, store.ErrReleaseNotPublishable) {
		t.Fatalf("clear on published: got %v, want ErrReleaseNotPublishable", err)
	}
	if err := s.DB.NewRaw("SELECT ed25519_sig FROM release_artifacts WHERE id = ?", artID2).Scan(ctx, &sig); err != nil {
		t.Fatalf("read: %v", err)
	}
	if sig == "" {
		t.Fatal("clear touched a published release's signature")
	}
}

// A signature write that starts while a publish holds the release row
// must wait for the publish and then see "published", not slip through
// on a snapshot taken before the commit.
func TestUpdateArtifactSignature_WaitsForInFlightPublish(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	relID, artID := setupSignedDraft(t, s, ctx, true)
	var keyID string
	if err := s.DB.NewRaw("SELECT signing_key_id FROM release_artifacts WHERE id = ?", artID).Scan(ctx, &keyID); err != nil {
		t.Fatalf("read key: %v", err)
	}

	// Simulate a publish in flight: lock the release row in a tx.
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	var status string
	if err := tx.NewRaw("SELECT status FROM releases WHERE id = ? FOR UPDATE", relID).Scan(ctx, &status); err != nil {
		t.Fatalf("lock: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- s.UpdateArtifactSignature(ctx, artID, "late-sig", keyID, "") }()
	select {
	case err := <-done:
		t.Fatalf("signature write did not wait for the publish lock (returned %v)", err)
	case <-time.After(300 * time.Millisecond):
	}

	// The "publish" commits inside the locked tx.
	if _, err := tx.NewRaw("UPDATE releases SET status = 'published', published_at = now() WHERE id = ?", relID).Exec(ctx); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, store.ErrReleaseNotPublishable) {
			t.Fatalf("late signature write: got %v, want ErrReleaseNotPublishable", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("signature write never returned after the publish committed")
	}
	var sig string
	if err := s.DB.NewRaw("SELECT ed25519_sig FROM release_artifacts WHERE id = ?", artID).Scan(ctx, &sig); err != nil {
		t.Fatalf("read: %v", err)
	}
	if sig == "late-sig" {
		t.Fatal("published release's signature was rewritten")
	}
}
