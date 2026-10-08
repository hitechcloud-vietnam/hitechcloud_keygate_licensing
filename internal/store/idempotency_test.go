package store_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// TestBeginIdempotent_ConcurrentClaim verifies that under concurrent
// claims of the same (scope, key, request hash), exactly ONE goroutine
// wins the claim (created=true) and every other sees the in-progress
// refusal. This is the core safety property: if it regresses, two retries
// can both run a mutation and double-apply its side effect.
func TestBeginIdempotent_ConcurrentClaim(t *testing.T) {
	s := setupTestDB(t)
	ctx := context.Background()

	const concurrency = 32
	scope := "concurrent-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	const key = "atomic-claim-test"
	const reqHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	defer func() {
		_, _ = s.DB.NewRaw(`DELETE FROM idempotency_keys WHERE scope = ? AND idempotency_key = ?`, scope, key).Exec(ctx)
	}()
	_, _ = s.DB.NewRaw(`DELETE FROM idempotency_keys WHERE scope = ? AND idempotency_key = ?`, scope, key).Exec(ctx)

	var winners atomic.Int32
	var inprogress atomic.Int32

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for range concurrency {
		go func() {
			defer wg.Done()
			<-start
			rec, created, err := s.BeginIdempotent(ctx, scope, key, reqHash)
			switch {
			case err != nil:
				if errors.Is(err, store.ErrIdempotencyInProgress) {
					inprogress.Add(1)
				} else {
					t.Errorf("unexpected err: %v", err)
				}
			case created:
				winners.Add(1)
				if rec == nil {
					t.Error("a created claim must return the record (for its id)")
				}
			default:
				t.Errorf("no goroutine should see a replay on a fresh slot (rec=%v)", rec)
			}
		}()
	}
	close(start)
	wg.Wait()

	if w := winners.Load(); w != 1 {
		t.Fatalf("exactly one goroutine must claim the slot; got %d winners", w)
	}
	if i := inprogress.Load(); i != concurrency-1 {
		t.Fatalf("all other goroutines must see in-progress; got %d (expected %d)", i, concurrency-1)
	}
}

// TestBeginIdempotent_Replay stores a response and verifies a retry with
// the same (scope, key, request hash) returns the stored status+body for
// replay instead of re-running the handler.
func TestBeginIdempotent_Replay(t *testing.T) {
	s := setupTestDB(t)
	ctx := context.Background()
	scope := "replay-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	const key = "replay-test"
	const reqHash = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	defer func() {
		_, _ = s.DB.NewRaw(`DELETE FROM idempotency_keys WHERE scope = ? AND idempotency_key = ?`, scope, key).Exec(ctx)
	}()

	rec, created, err := s.BeginIdempotent(ctx, scope, key, reqHash)
	if err != nil || !created {
		t.Fatalf("claim: rec=%v created=%v err=%v", rec, created, err)
	}
	if err := s.FinishIdempotent(ctx, rec.ID, 200, []byte(`{"success":true}`)); err != nil {
		t.Fatalf("finish: %v", err)
	}

	replay, created, err := s.BeginIdempotent(ctx, scope, key, reqHash)
	if err != nil {
		t.Fatalf("replay claim: %v", err)
	}
	if created {
		t.Fatal("replay must NOT report created; the slot is done")
	}
	if replay == nil {
		t.Fatal("replay must return the stored record")
	}
	if replay.State != "done" {
		t.Fatalf("replay state = %q, want done", replay.State)
	}
	if replay.ResponseStatus != 200 || string(replay.ResponseBody) != `{"success":true}` {
		t.Fatalf("cached response wrong: status=%d body=%q", replay.ResponseStatus, replay.ResponseBody)
	}
}

// TestBeginIdempotent_ReusedKey verifies that reusing a key with a
// different request hash returns ErrIdempotencyKeyReused — never silently
// accepts the second request, never overwrites the first.
func TestBeginIdempotent_ReusedKey(t *testing.T) {
	s := setupTestDB(t)
	ctx := context.Background()
	scope := "reused-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	const key = "reused-test"
	const hashA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const hashB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	defer func() {
		_, _ = s.DB.NewRaw(`DELETE FROM idempotency_keys WHERE scope = ? AND idempotency_key = ?`, scope, key).Exec(ctx)
	}()

	if _, created, err := s.BeginIdempotent(ctx, scope, key, hashA); err != nil || !created {
		t.Fatalf("first claim: created=%v err=%v", created, err)
	}
	if _, _, err := s.BeginIdempotent(ctx, scope, key, hashB); !errors.Is(err, store.ErrIdempotencyKeyReused) {
		t.Fatalf("expected ErrIdempotencyKeyReused, got err=%v", err)
	}
}

// TestBeginIdempotent_InProgress verifies that a second claim while the
// first is still running (not yet finished) returns
// ErrIdempotencyInProgress so the client retries with backoff.
func TestBeginIdempotent_InProgress(t *testing.T) {
	s := setupTestDB(t)
	ctx := context.Background()
	scope := "inprog-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	const key = "inprogress-test"
	const reqHash = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	defer func() {
		_, _ = s.DB.NewRaw(`DELETE FROM idempotency_keys WHERE scope = ? AND idempotency_key = ?`, scope, key).Exec(ctx)
	}()

	if _, created, err := s.BeginIdempotent(ctx, scope, key, reqHash); err != nil || !created {
		t.Fatalf("claim: created=%v err=%v", created, err)
	}
	if _, _, err := s.BeginIdempotent(ctx, scope, key, reqHash); !errors.Is(err, store.ErrIdempotencyInProgress) {
		t.Fatalf("expected ErrIdempotencyInProgress, got err=%v", err)
	}
}

// TestReleaseIdempotent_FreesSlot verifies that releasing an unfinished
// slot (handler failed / 5xx) lets the next claim succeed fresh instead
// of being stuck in-progress until the TTL.
func TestReleaseIdempotent_FreesSlot(t *testing.T) {
	s := setupTestDB(t)
	ctx := context.Background()
	scope := "release-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	const key = "release-test"
	const reqHash = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	defer func() {
		_, _ = s.DB.NewRaw(`DELETE FROM idempotency_keys WHERE scope = ? AND idempotency_key = ?`, scope, key).Exec(ctx)
	}()

	rec, created, err := s.BeginIdempotent(ctx, scope, key, reqHash)
	if err != nil || !created {
		t.Fatalf("claim: created=%v err=%v", created, err)
	}
	if err := s.ReleaseIdempotent(ctx, rec.ID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, created, err := s.BeginIdempotent(ctx, scope, key, reqHash); err != nil || !created {
		t.Fatalf("re-claim after release should succeed fresh; created=%v err=%v", created, err)
	}
}

// TestExpireIdempotencyKeys drops rows past their expiry so the table
// does not grow without bound.
func TestExpireIdempotencyKeys(t *testing.T) {
	s := setupTestDB(t)
	ctx := context.Background()
	scope := "expire-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	const key = "expire-test"
	const reqHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	defer func() {
		_, _ = s.DB.NewRaw(`DELETE FROM idempotency_keys WHERE scope = ? AND idempotency_key = ?`, scope, key).Exec(ctx)
	}()

	rec, _, err := s.BeginIdempotent(ctx, scope, key, reqHash)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := s.DB.NewRaw(`UPDATE idempotency_keys SET expires_at = now() - interval '1 hour' WHERE id = ?`, rec.ID).Exec(ctx); err != nil {
		t.Fatalf("force expiry: %v", err)
	}
	if n, err := s.ExpireIdempotencyKeys(ctx, time.Now()); err != nil || n < 1 {
		t.Fatalf("ExpireIdempotencyKeys: n=%d err=%v (want >= 1)", n, err)
	}
	if _, created, err := s.BeginIdempotent(ctx, scope, key, reqHash); err != nil || !created {
		t.Fatalf("claim after expiry should succeed fresh; created=%v err=%v", created, err)
	}
}
