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

// renew presents hash, asking for a new token with the given hash.
func renew(t *testing.T, s *store.Store, hash, next string) (*store.RefreshRenewal, error) {
	t.Helper()
	return s.RenewRefreshToken(context.Background(), hash, next, time.Now().Add(30*24*time.Hour))
}

func activeTokens(t *testing.T, s *store.Store, userID string) int {
	t.Helper()
	var n int
	if err := s.DB.NewRaw("SELECT count(*) FROM refresh_tokens WHERE user_id = ? AND revoked_at IS NULL", userID).Scan(context.Background(), &n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A rotated refresh token is honoured for RefreshReuseGrace (several tabs
// renewing at once, a retried request) and treated as theft after it. A
// grace renewal issues a token too, capped at the presented one's expiry.
func TestRenewRefreshToken_GraceThenReuse(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	u := &model.User{Email: "grace-" + time.Now().Format("150405.000000") + "@example.com"}
	if err := s.UpsertUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	hash := "grace-hash-" + u.ID
	expiry := time.Now().Add(time.Hour).Truncate(time.Second)
	if err := s.CreateRefreshToken(ctx, u.ID, hash, expiry); err != nil {
		t.Fatal(err)
	}

	first, err := renew(t, s, hash, hash+"-1")
	if err != nil || first.Grace || first.User.ID != u.ID {
		t.Fatalf("first renewal: %+v %v", first, err)
	}
	second, err := renew(t, s, hash, hash+"-2")
	if err != nil || !second.Grace {
		t.Fatalf("immediate second use: %+v %v, want a grace renewal", second, err)
	}
	if !second.ExpiresAt.Equal(expiry) {
		t.Fatalf("grace token expires %v, want the presented token's %v", second.ExpiresAt, expiry)
	}
	if n := activeTokens(t, s, u.ID); n != 2 {
		t.Fatalf("active tokens after a grace renewal: %d, want 2", n)
	}

	if _, err := s.DB.NewRaw("UPDATE refresh_tokens SET revoked_at = now() - interval '1 minute' WHERE token_hash = ?", hash).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	ren, err := renew(t, s, hash, hash+"-3")
	if !errors.Is(err, store.ErrRefreshTokenReused) {
		t.Fatalf("use after the grace window: err=%v, want ErrRefreshTokenReused", err)
	}
	if ren == nil || ren.Token.UserID != u.ID {
		t.Fatal("reuse must report the user so the caller can revoke the chain")
	}
	var issued int
	if err := s.DB.NewRaw("SELECT count(*) FROM refresh_tokens WHERE token_hash = ?", hash+"-3").Scan(ctx, &issued); err != nil || issued != 0 {
		t.Fatalf("reuse issued a token: %d %v", issued, err)
	}
}

// A renewal that cannot finish leaves the presented token as it was, so
// a retry after the outage still works. Here the new token cannot be
// written (its hash is taken), which fails the transaction after the
// rotation and the user read.
func TestRenewRefreshToken_FailureConsumesNothing(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	u := &model.User{Email: "renew-fail-" + time.Now().Format("150405.000000") + "@example.com"}
	if err := s.UpsertUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	hash := "renew-fail-hash-" + u.ID
	taken := "renew-fail-taken-" + u.ID
	for _, h := range []string{hash, taken} {
		if err := s.CreateRefreshToken(ctx, u.ID, h, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := renew(t, s, hash, taken); err == nil {
		t.Skip("refresh_tokens.token_hash is not unique here; cannot force a write failure")
	}
	var revoked bool
	if err := s.DB.NewRaw("SELECT revoked_at IS NOT NULL FROM refresh_tokens WHERE token_hash = ?", hash).Scan(ctx, &revoked); err != nil {
		t.Fatal(err)
	}
	if revoked {
		t.Fatal("failed renewal consumed the presented token")
	}
	if ren, err := renew(t, s, hash, hash+"-retry"); err != nil || ren.Grace {
		t.Fatalf("retry after the failure: %+v %v, want a normal renewal", ren, err)
	}
}

// A deleted user's token gets a plain refusal and is not consumed.
func TestRenewRefreshToken_UserGone(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	u := &model.User{Email: "renew-gone-" + time.Now().Format("150405.000000") + "@example.com"}
	if err := s.UpsertUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	hash := "renew-gone-hash-" + u.ID
	if err := s.CreateRefreshToken(ctx, u.ID, hash, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.NewRaw("DELETE FROM users WHERE id = ?", u.ID).Exec(ctx); err != nil {
		t.Skipf("cannot delete the user here: %v", err)
	}
	if _, err := renew(t, s, hash, hash+"-1"); !errors.Is(err, store.ErrRefreshUserNotFound) && !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted user: err=%v, want ErrRefreshUserNotFound (or no token, if it went with the user)", err)
	}
}

// Logout deletes the token, so no grace window can bring it back.
func TestRenewRefreshToken_LoggedOutTokenStaysDead(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	u := &model.User{Email: "logout-grace-" + time.Now().Format("150405.000000") + "@example.com"}
	if err := s.UpsertUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	hash := "logout-grace-hash-" + u.ID
	if err := s.CreateRefreshToken(ctx, u.ID, hash, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := renew(t, s, hash, hash+"-1"); err != nil {
		t.Fatal(err)
	}
	s.DeleteUserRefreshTokens(ctx, u.ID)
	if _, err := renew(t, s, hash, hash+"-2"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("after logout: err=%v, want sql.ErrNoRows", err)
	}
}

// Checkout hands the address over as typed; the user row is keyed in
// lower case so it is the same account the buyer signs in to.
func TestUpsertUser_NormalizesEmail(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	suffix := time.Now().Format("150405.000000")
	lower := "mixed-" + suffix + "@example.com"
	mixed := "  MIXED-" + suffix + "@Example.COM "
	if err := s.UpsertUser(ctx, &model.User{Email: lower}); err != nil {
		t.Fatal(err)
	}
	u := &model.User{Email: mixed}
	if err := s.UpsertUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if u.Email != lower {
		t.Fatalf("upserted email %q, want %q", u.Email, lower)
	}
	var n int
	if err := s.DB.NewRaw("SELECT count(*) FROM users WHERE lower(email) = ?", lower).Scan(ctx, &n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d user rows for one address, want 1", n)
	}
	if got, err := s.FindUserByEmail(ctx, mixed); err != nil || got.Email != lower {
		t.Fatalf("FindUserByEmail mixed-case: user=%v err=%v", got, err)
	}
}

// The portal session carries the lower-cased sign-in address; a licence
// keeps the address as typed. They must still match.
func TestListLicensesByEmail_IgnoresCase(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	lic := createTestLicense(t, s, ctx)
	mixed := "Case-" + lic.ID[:8] + "@Example.COM"
	if _, err := s.DB.NewRaw("UPDATE licenses SET email = ? WHERE id = ?", mixed, lic.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListLicensesByEmail(ctx, "case-"+lic.ID[:8]+"@example.com")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, l := range got {
		found = found || l.ID == lic.ID
	}
	if !found {
		t.Fatal("license with a mixed-case address is invisible to its lower-cased owner")
	}
	var stored string
	_ = s.DB.NewRaw("SELECT email FROM licenses WHERE id = ?", lic.ID).Scan(ctx, &stored)
	if stored != mixed {
		t.Fatalf("license address changed to %q; it must stay as typed", stored)
	}
}
