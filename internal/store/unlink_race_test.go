package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// A Stripe event that was already on its way when an admin unlinked
// the licence must not put the subscription's state back.
//
// The sequence is the one a busy install hits for real: the webhook
// resolves the licence by subscription id, the admin unlinks (having
// had Stripe confirm the subscription is over), and only then does the
// webhook get to its write. By primary key alone that write would
// restore the status and the period the subscription last dictated,
// on a licence that is now managed locally.
func TestUpdateFromSubscriptionIgnoresUnlinkedLicense(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	lic := createTestLicense(t, s, ctx)
	subID := "sub_unlink_race_" + time.Now().Format("150405.000")
	linkSubscription(t, s, ctx, lic.ID, subID)

	// The webhook's read, before the unlink.
	inFlight, err := s.FindLicenseByStripeSubscription(ctx, subID)
	if err != nil {
		t.Fatalf("find by subscription: %v", err)
	}

	// The admin unlinks, with the id Stripe answered about.
	cleared, err := s.ClearStripeSubscription(ctx, lic.ID, subID)
	if err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if !cleared {
		t.Fatal("unlink reported no change, want cleared")
	}

	// The webhook finally writes.
	inFlight.Status = model.StatusPastDue
	until := time.Now().Add(720 * time.Hour)
	inFlight.ValidUntil = &until
	err = s.UpdateLicenseFromSubscription(ctx, inFlight, "status", "valid_until")
	if !errors.Is(err, store.ErrSubscriptionUnlinked) {
		t.Fatalf("stale event write = %v, want ErrSubscriptionUnlinked", err)
	}

	after, err := s.FindLicenseByID(ctx, lic.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Status != model.StatusActive {
		t.Errorf("status = %q, want it untouched (%q)", after.Status, model.StatusActive)
	}
	if after.ValidUntil != nil {
		t.Errorf("valid_until = %v, want it untouched (nil)", after.ValidUntil)
	}
	if after.StripeSubscriptionID != "" {
		t.Errorf("stripe_subscription_id = %q, want it to stay unlinked", after.StripeSubscriptionID)
	}
}

// Unlinking names the subscription it confirmed. If the licence has
// moved to another one in between — a fresh checkout — the unlink does
// nothing rather than cutting a subscription nobody asked about.
func TestClearStripeSubscriptionOnlyTheConfirmedOne(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	lic := createTestLicense(t, s, ctx)
	stamp := time.Now().Format("150405.000")
	current := "sub_current_" + stamp
	linkSubscription(t, s, ctx, lic.ID, current)

	cleared, err := s.ClearStripeSubscription(ctx, lic.ID, "sub_stale_"+stamp)
	if err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if cleared {
		t.Fatal("unlink cleared a subscription it was not asked about")
	}
	after, err := s.FindLicenseByID(ctx, lic.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.StripeSubscriptionID != current {
		t.Errorf("stripe_subscription_id = %q, want %q", after.StripeSubscriptionID, current)
	}

	// Naming the one it is actually on works.
	cleared, err = s.ClearStripeSubscription(ctx, lic.ID, current)
	if err != nil || !cleared {
		t.Fatalf("unlink of the current subscription = (%v, %v), want (true, nil)", cleared, err)
	}
}

// linkSubscription puts a licence on a subscription the way a paid
// checkout does, without going through Stripe.
func linkSubscription(t *testing.T, s *store.Store, ctx context.Context, licenseID, subID string) {
	t.Helper()
	if _, err := s.DB.NewRaw(
		"UPDATE licenses SET stripe_subscription_id = ? WHERE id = ?", subID, licenseID,
	).Exec(ctx); err != nil {
		t.Fatalf("link subscription: %v", err)
	}
}
