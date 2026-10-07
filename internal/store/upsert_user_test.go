package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// Login upserts the user with nothing but an email — OTP has no name to
// offer, and neither does the Stripe checkout path. The upsert used to
// write that blank straight over the display name its owner had set in
// the portal, so every login wiped it.
//
// The rule this pins: a blank incoming value means "the caller doesn't
// know", never "clear it".
func TestUpsertUserKeepsDisplayName(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	email := "upsert-" + time.Now().Format("150405.000000") + "@test.com"
	defer func() {
		_, _ = s.DB.NewRaw("DELETE FROM users WHERE email = ?", email).Exec(ctx)
	}()

	// First login creates the account, with no name to go on.
	if err := s.UpsertUser(ctx, &model.User{Email: email}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// The owner names themselves in the portal.
	created, err := s.FindUserByEmail(ctx, email)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if err := s.UpdateUserProfile(ctx, created.ID, "Sheck Andar"); err != nil {
		t.Fatalf("set name: %v", err)
	}

	// Two more logins, still with nothing but an email.
	for i := range 2 {
		if err := s.UpsertUser(ctx, &model.User{Email: email}); err != nil {
			t.Fatalf("login %d: %v", i+1, err)
		}
		u, err := s.FindUserByEmail(ctx, email)
		if err != nil {
			t.Fatalf("find after login %d: %v", i+1, err)
		}
		if u.Name != "Sheck Andar" {
			t.Fatalf("login %d cleared the display name: got %q", i+1, u.Name)
		}
		if u.ID != created.ID {
			t.Fatalf("login %d created a second row: %s != %s", i+1, u.ID, created.ID)
		}
	}

	// A caller that does know a name still gets to set it — OAuth passes
	// the one the provider reports.
	if err := s.UpsertUser(ctx, &model.User{Email: email, Name: "From Provider"}); err != nil {
		t.Fatalf("named upsert: %v", err)
	}
	u, err := s.FindUserByEmail(ctx, email)
	if err != nil {
		t.Fatalf("find after named upsert: %v", err)
	}
	if u.Name != "From Provider" {
		t.Errorf("a non-blank name should still win: got %q", u.Name)
	}
}
