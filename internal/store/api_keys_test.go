package store_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// Customer API key store tests (integration: skip without a database,
// same guard as every other store test — see setupTestDB).

func createAPIKeyTestUser(t *testing.T, s *store.Store) string {
	t.Helper()
	id := store.NewID()
	if _, err := s.DB.NewRaw(
		"INSERT INTO users (id, email, name) VALUES (?, ?, ?)",
		id, "apikeys-"+id+"@example.com", "API Key Tester",
	).Exec(context.Background()); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}

func deleteAPIKeyTestData(t *testing.T, s *store.Store, userIDs ...string) {
	t.Helper()
	for _, id := range userIDs {
		if _, err := s.DB.NewRaw("DELETE FROM customer_api_keys WHERE user_id = ?", id).Exec(context.Background()); err != nil {
			t.Fatalf("cleanup keys: %v", err)
		}
		if _, err := s.DB.NewRaw("DELETE FROM users WHERE id = ?", id).Exec(context.Background()); err != nil {
			t.Fatalf("cleanup user: %v", err)
		}
	}
}

// The whole life of a key: mint (hash + prefix derived, plaintext not
// stored), find by id and by hash, stamp last-used, soft-revoke (the
// hash stops resolving), revoke again without error, hard-delete.
func TestCustomerAPIKeyLifecycle(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	userID := createAPIKeyTestUser(t, s)
	defer deleteAPIKeyTestData(t, s, userID)

	secret, err := model.NewCustomerAPIKeySecret()
	if err != nil {
		t.Fatalf("NewCustomerAPIKeySecret: %v", err)
	}
	ak := &model.CustomerAPIKey{UserID: userID, Name: "CI runner", Scopes: "orders:read, usage:read"}
	if err := s.CreateCustomerAPIKey(ctx, ak, secret); err != nil {
		t.Fatalf("CreateCustomerAPIKey: %v", err)
	}
	if ak.ID == "" {
		t.Fatal("CreateCustomerAPIKey left the ID empty")
	}
	// At rest: SHA-256 hex of the full secret and its display prefix —
	// never the plaintext.
	if want := store.HashAPIKey(secret); ak.KeyHash != want {
		t.Errorf("KeyHash = %q, want %q", ak.KeyHash, want)
	}
	if want := model.CustomerAPIKeyDisplayPrefix(secret); ak.KeyPrefix != want {
		t.Errorf("KeyPrefix = %q, want %q", ak.KeyPrefix, want)
	}

	got, err := s.FindCustomerAPIKeyByID(ctx, ak.ID)
	if err != nil {
		t.Fatalf("FindCustomerAPIKeyByID: %v", err)
	}
	if got.UserID != userID || got.Name != "CI runner" || got.Scopes != "orders:read,usage:read" {
		t.Errorf("round-trip = %+v, want user %s, name CI runner, scopes folded", got, userID)
	}
	if got.LastUsedAt != nil || got.RevokedAt != nil {
		t.Errorf("a fresh key must have no last_used_at/revoked_at: %+v", got)
	}

	byHash, err := s.FindCustomerAPIKeyByHash(ctx, store.HashAPIKey(secret))
	if err != nil {
		t.Fatalf("FindCustomerAPIKeyByHash: %v", err)
	}
	if byHash.ID != ak.ID {
		t.Errorf("hash lookup returned %q, want %q", byHash.ID, ak.ID)
	}
	if _, err := s.FindCustomerAPIKeyByHash(ctx, store.HashAPIKey("htc_sk_SOMETHING_ELSE")); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("wrong hash lookup error = %v, want sql.ErrNoRows", err)
	}

	if err := s.TouchCustomerAPIKeyLastUsed(ctx, ak.ID); err != nil {
		t.Fatalf("TouchCustomerAPIKeyLastUsed: %v", err)
	}
	if got, _ := s.FindCustomerAPIKeyByID(ctx, ak.ID); got.LastUsedAt == nil {
		t.Error("last_used_at was not stamped")
	}

	if err := s.RevokeCustomerAPIKey(ctx, ak.ID); err != nil {
		t.Fatalf("RevokeCustomerAPIKey: %v", err)
	}
	revoked, err := s.FindCustomerAPIKeyByID(ctx, ak.ID)
	if err != nil {
		t.Fatalf("FindCustomerAPIKeyByID after revoke: %v", err)
	}
	if revoked.RevokedAt == nil {
		t.Fatal("revoked_at was not stamped")
	}
	// A revoked key must not validate.
	if _, err := s.FindCustomerAPIKeyByHash(ctx, store.HashAPIKey(secret)); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("revoked key still resolves: %v", err)
	}
	// Idempotent: revoking again is not an error and does not move the
	// original revocation moment.
	if err := s.RevokeCustomerAPIKey(ctx, ak.ID); err != nil {
		t.Errorf("second RevokeCustomerAPIKey: %v", err)
	}
	if again, _ := s.FindCustomerAPIKeyByID(ctx, ak.ID); !again.RevokedAt.Equal(*revoked.RevokedAt) {
		t.Errorf("second revoke moved revoked_at: %v -> %v", revoked.RevokedAt, again.RevokedAt)
	}

	if err := s.DeleteCustomerAPIKey(ctx, ak.ID); err != nil {
		t.Fatalf("DeleteCustomerAPIKey: %v", err)
	}
	if _, err := s.FindCustomerAPIKeyByID(ctx, ak.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("deleted key still resolves: %v", err)
	}
}

// Expiry is enforced at the lookup, not just in the UI: an expired key
// does not validate and is not stamped as used.
func TestCustomerAPIKeyExpiry(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	userID := createAPIKeyTestUser(t, s)
	defer deleteAPIKeyTestData(t, s, userID)

	past := time.Now().Add(-time.Hour)
	expired := &model.CustomerAPIKey{UserID: userID, Name: "expired", ExpiresAt: &past}
	secretExpired, err := model.NewCustomerAPIKeySecret()
	if err != nil {
		t.Fatalf("NewCustomerAPIKeySecret: %v", err)
	}
	if err := s.CreateCustomerAPIKey(ctx, expired, secretExpired); err != nil {
		t.Fatalf("CreateCustomerAPIKey: %v", err)
	}

	future := time.Now().Add(time.Hour)
	live := &model.CustomerAPIKey{UserID: userID, Name: "live", ExpiresAt: &future}
	secretLive, err := model.NewCustomerAPIKeySecret()
	if err != nil {
		t.Fatalf("NewCustomerAPIKeySecret: %v", err)
	}
	if err := s.CreateCustomerAPIKey(ctx, live, secretLive); err != nil {
		t.Fatalf("CreateCustomerAPIKey: %v", err)
	}

	if _, err := s.FindCustomerAPIKeyByHash(ctx, store.HashAPIKey(secretExpired)); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expired key resolved: %v", err)
	}
	if _, err := s.FindCustomerAPIKeyByHash(ctx, store.HashAPIKey(secretLive)); err != nil {
		t.Errorf("unexpired key did not resolve: %v", err)
	}

	// Touching an expired key must not stamp it: a dead credential
	// must not gain a fresh "last used" mark.
	if err := s.TouchCustomerAPIKeyLastUsed(ctx, expired.ID); err != nil {
		t.Fatalf("TouchCustomerAPIKeyLastUsed: %v", err)
	}
	if got, _ := s.FindCustomerAPIKeyByID(ctx, expired.ID); got.LastUsedAt != nil {
		t.Error("an expired key was stamped last_used_at")
	}
}

// The portal lists "my keys": one user's keys must never show up under
// another user, and the total is the owner's count, not the table's.
func TestListCustomerAPIKeysByUserIsolation(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	alice := createAPIKeyTestUser(t, s)
	bob := createAPIKeyTestUser(t, s)
	defer deleteAPIKeyTestData(t, s, alice, bob)

	names := map[string][]string{
		alice: {"alice-1", "alice-2"},
		bob:   {"bob-1"},
	}
	for uid, ns := range names {
		for _, n := range ns {
			secret, err := model.NewCustomerAPIKeySecret()
			if err != nil {
				t.Fatalf("NewCustomerAPIKeySecret: %v", err)
			}
			if err := s.CreateCustomerAPIKey(ctx, &model.CustomerAPIKey{UserID: uid, Name: n}, secret); err != nil {
				t.Fatalf("CreateCustomerAPIKey(%s): %v", n, err)
			}
		}
	}

	aliceKeys, total, err := s.ListCustomerAPIKeysByUser(ctx, alice, store.Page{Limit: 50})
	if err != nil {
		t.Fatalf("ListCustomerAPIKeysByUser: %v", err)
	}
	if total != 2 || len(aliceKeys) != 2 {
		t.Fatalf("alice list = %d rows (total %d), want 2/2", len(aliceKeys), total)
	}
	for _, k := range aliceKeys {
		if k.UserID != alice {
			t.Errorf("alice's list leaked key %s of user %s", k.ID, k.UserID)
		}
	}

	bobKeys, total, err := s.ListCustomerAPIKeysByUser(ctx, bob, store.Page{Limit: 50})
	if err != nil {
		t.Fatalf("ListCustomerAPIKeysByUser: %v", err)
	}
	if total != 1 || len(bobKeys) != 1 {
		t.Fatalf("bob list = %d rows (total %d), want 1/1", len(bobKeys), total)
	}

	// Paging: one row on the page, two in the count.
	page, total, err := s.ListCustomerAPIKeysByUser(ctx, alice, store.Page{Limit: 1})
	if err != nil {
		t.Fatalf("ListCustomerAPIKeysByUser (paged): %v", err)
	}
	if len(page) != 1 || total != 2 {
		t.Errorf("paged list = %d rows (total %d), want 1/2", len(page), total)
	}
}
