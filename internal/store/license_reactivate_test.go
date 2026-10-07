package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// newLicenseOn creates a licence on a fresh plan of the given type, in the
// given status, with a subscription row the way admin issuance makes one.
func newLicenseOn(t *testing.T, s *store.Store, ctx context.Context, licenseType, status string, validUntil *time.Time) *model.License {
	t.Helper()
	suffix := licenseType + "-" + status + "-" + time.Now().Format("150405.000000")
	product := &model.Product{Name: "Reactivate " + suffix, Slug: "reactivate-" + suffix, Type: "desktop"}
	if err := s.CreateProduct(ctx, product); err != nil {
		t.Fatalf("create product: %v", err)
	}
	plan := &model.Plan{
		ProductID: product.ID, Name: "Plan", Slug: "plan-" + suffix,
		LicenseType: licenseType, LicenseModel: "standard", GraceDays: 7, TrialDays: 14,
	}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	lic := &model.License{
		ProductID: product.ID, PlanID: plan.ID, Email: "reactivate-" + suffix + "@example.com",
		LicenseKey: "KEY-" + suffix, Status: status, ValidUntil: validUntil,
	}
	if err := s.CreateLicense(ctx, lic); err != nil {
		t.Fatalf("create license: %v", err)
	}
	if err := store.SyncLicenseSubscriptionIn(ctx, s.DB, lic.ID, plan, status, validUntil); err != nil {
		t.Fatalf("create subscription: %v", err)
	}
	return lic
}

func licenseState(t *testing.T, s *store.Store, ctx context.Context, id string) (status string, validUntil *time.Time) {
	t.Helper()
	if err := s.DB.NewRaw("SELECT status, valid_until FROM licenses WHERE id = ?", id).Scan(ctx, &status, &validUntil); err != nil {
		t.Fatalf("read license: %v", err)
	}
	return status, validUntil
}

func subscriptionState(t *testing.T, s *store.Store, ctx context.Context, licenseID string) (status string, trialEnd *time.Time) {
	t.Helper()
	if err := s.DB.NewRaw("SELECT status, trial_end FROM subscriptions WHERE license_id = ? ORDER BY created_at DESC LIMIT 1",
		licenseID).Scan(ctx, &status, &trialEnd); err != nil {
		t.Fatalf("read subscription: %v", err)
	}
	return status, trialEnd
}

func sameDay(a *time.Time, b time.Time) bool {
	return a != nil && a.Truncate(time.Second).Equal(b.Truncate(time.Second))
}

// Issue #35: an expired trial given a new date must come back as a trial,
// with its subscription's status and trial window moved in the same write.
func TestSetLicenseValidUntil_ReactivatesExpiredTrial(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	past := time.Now().Add(-24 * time.Hour)
	lic := newLicenseOn(t, s, ctx, "trial", model.StatusExpired, &past)

	future := time.Now().Add(30 * 24 * time.Hour)
	prev, status, err := s.SetLicenseValidUntil(ctx, lic.ID, &future)
	if err != nil {
		t.Fatal(err)
	}
	if prev != model.StatusExpired || status != model.StatusTrialing {
		t.Fatalf("status %s -> %s, want expired -> trialing", prev, status)
	}
	if st, vu := licenseState(t, s, ctx, lic.ID); st != model.StatusTrialing || !sameDay(vu, future) {
		t.Fatalf("license row: status=%s valid_until=%v", st, vu)
	}
	if st, te := subscriptionState(t, s, ctx, lic.ID); st != model.StatusTrialing || !sameDay(te, future) {
		t.Fatalf("subscription row: status=%s trial_end=%v, want trialing and the new date", st, te)
	}
}

func TestSetLicenseValidUntil_ReactivatesExpiredNonTrialAsActive(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	past := time.Now().Add(-24 * time.Hour)
	lic := newLicenseOn(t, s, ctx, "subscription", model.StatusExpired, &past)

	future := time.Now().Add(30 * 24 * time.Hour)
	if _, status, err := s.SetLicenseValidUntil(ctx, lic.ID, &future); err != nil || status != model.StatusActive {
		t.Fatalf("status=%s err=%v, want active", status, err)
	}
	if st, _ := subscriptionState(t, s, ctx, lic.ID); st != model.StatusActive {
		t.Fatalf("subscription status %s, want active", st)
	}
}

// Clearing the date makes the licence perpetual, which is usable too.
func TestSetLicenseValidUntil_PerpetualReactivates(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	past := time.Now().Add(-24 * time.Hour)
	lic := newLicenseOn(t, s, ctx, "perpetual", model.StatusExpired, &past)

	if _, status, err := s.SetLicenseValidUntil(ctx, lic.ID, nil); err != nil || status != model.StatusActive {
		t.Fatalf("status=%s err=%v, want active", status, err)
	}
}

// A date edit must not undo a deliberate decision about the licence.
func TestSetLicenseValidUntil_KeepsDeliberateStatuses(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	for _, st := range []string{model.StatusSuspended, model.StatusRevoked, model.StatusCanceled} {
		past := time.Now().Add(-24 * time.Hour)
		lic := newLicenseOn(t, s, ctx, "trial", st, &past)
		future := time.Now().Add(30 * 24 * time.Hour)
		prev, status, err := s.SetLicenseValidUntil(ctx, lic.ID, &future)
		if err != nil {
			t.Fatal(err)
		}
		if prev != st || status != st {
			t.Fatalf("%s: status became %s", st, status)
		}
		if got, _ := licenseState(t, s, ctx, lic.ID); got != st {
			t.Fatalf("%s: row status became %s", st, got)
		}
	}
}

func TestReinstateLicense_ReturnsPlanStatus(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	future := time.Now().Add(7 * 24 * time.Hour)

	trial := newLicenseOn(t, s, ctx, "trial", model.StatusExpired, &future)
	if status, err := s.ReinstateLicense(ctx, trial.ID); err != nil || status != model.StatusTrialing {
		t.Fatalf("trial: status=%s err=%v, want trialing", status, err)
	}
	if st, _ := subscriptionState(t, s, ctx, trial.ID); st != model.StatusTrialing {
		t.Fatalf("trial subscription status %s, want trialing", st)
	}

	sub := newLicenseOn(t, s, ctx, "subscription", model.StatusSuspended, &future)
	if status, err := s.ReinstateLicense(ctx, sub.ID); err != nil || status != model.StatusActive {
		t.Fatalf("subscription: status=%s err=%v, want active", status, err)
	}

	// Expired with the date already gone: a reinstate would leave it
	// unusable and the sweep would expire it again. Refused; a new
	// expiry date is the way back.
	past := time.Now().Add(-24 * time.Hour)
	lapsed := newLicenseOn(t, s, ctx, "trial", model.StatusExpired, &past)
	if _, err := s.ReinstateLicense(ctx, lapsed.ID); !errors.Is(err, store.ErrReinstateNeedsNewExpiry) {
		t.Fatalf("lapsed: err=%v, want ErrReinstateNeedsNewExpiry", err)
	}
	if st, _ := licenseState(t, s, ctx, lapsed.ID); st != model.StatusExpired {
		t.Fatalf("lapsed license status became %s", st)
	}

	revoked := newLicenseOn(t, s, ctx, "subscription", model.StatusRevoked, &future)
	if _, err := s.ReinstateLicense(ctx, revoked.ID); err == nil {
		t.Fatal("a revoked license must not be reinstatable")
	}
	if st, _ := licenseState(t, s, ctx, revoked.ID); st != model.StatusRevoked {
		t.Fatalf("revoked license status became %s", st)
	}
}

// The sweeps read candidates first and write later. A licence extended in
// between must not be expired by the stale read.
func TestExpireLicenseIf_DoesNotOverwriteAnExtension(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)
	lic := newLicenseOn(t, s, ctx, "trial", model.StatusTrialing, &past)

	// The sweep has read this licence as due; an admin extends it first.
	future := time.Now().Add(30 * 24 * time.Hour)
	if _, _, err := s.SetLicenseValidUntil(ctx, lic.ID, &future); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ok, err := s.ExpireLicenseIf(ctx, lic.ID, []string{model.StatusTrialing}, &now)
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v, want the stale expiry skipped", ok, err)
	}
	if st, _ := licenseState(t, s, ctx, lic.ID); st != model.StatusTrialing {
		t.Fatalf("status became %s", st)
	}
}

func TestExpireLicenseIf_ExpiresWhenStillDue(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	past := time.Now().Add(-time.Hour)
	lic := newLicenseOn(t, s, ctx, "trial", model.StatusTrialing, &past)

	now := time.Now()
	ok, err := s.ExpireLicenseIf(ctx, lic.ID, []string{model.StatusTrialing}, &now)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v, want expired", ok, err)
	}
	if st, _ := licenseState(t, s, ctx, lic.ID); st != model.StatusExpired {
		t.Fatalf("license status %s, want expired", st)
	}
	if st, _ := subscriptionState(t, s, ctx, lic.ID); st != model.StatusExpired {
		t.Fatalf("subscription status %s, want expired", st)
	}

	// Already expired: a second sweep writes nothing.
	if ok, err := s.ExpireLicenseIf(ctx, lic.ID, []string{model.StatusTrialing}, &now); err != nil || ok {
		t.Fatalf("second run ok=%v err=%v, want no-op", ok, err)
	}
}

// The dunning sweep may only expire a licence still past due since before
// the threshold; one that was paid and fell past due again since the sweep
// read it has a fresh dunning clock.
func TestExpireStalePastDueLicenseIf(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	future := time.Now().Add(30 * 24 * time.Hour)
	threshold := time.Now().Add(-30 * 24 * time.Hour)

	stale := newLicenseOn(t, s, ctx, "subscription", model.StatusPastDue, &future)
	if _, err := s.DB.NewRaw("UPDATE licenses SET past_due_at = now() - interval '40 days' WHERE id = ?", stale.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ExpireStalePastDueLicenseIf(ctx, stale.ID, threshold); err != nil || !ok {
		t.Fatalf("stale past_due: ok=%v err=%v, want expired", ok, err)
	}

	fresh := newLicenseOn(t, s, ctx, "subscription", model.StatusPastDue, &future)
	if _, err := s.DB.NewRaw("UPDATE licenses SET past_due_at = now() - interval '1 day' WHERE id = ?", fresh.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.ExpireStalePastDueLicenseIf(ctx, fresh.ID, threshold); err != nil || ok {
		t.Fatalf("fresh past_due: ok=%v err=%v, want left alone", ok, err)
	}
	if st, _ := licenseState(t, s, ctx, fresh.ID); st != model.StatusPastDue {
		t.Fatalf("fresh past_due became %s", st)
	}
}

// A trial plan sold through Stripe goes on to be paid while the plan
// stays "trial". Giving such a licence back — reinstated after a
// suspension, or reactivated by a new expiry date — must not turn a
// paying customer back into a trial: it would meet the trial sweep (no
// grace period) and miss the renewal reminder.
func TestReactivate_StripeBilledTrialPlanIsActive(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	future := time.Now().Add(20 * 24 * time.Hour)
	link := func(lic *model.License) {
		t.Helper()
		if _, err := s.DB.NewRaw("UPDATE licenses SET stripe_subscription_id = ? WHERE id = ?", "sub_paid_"+lic.ID, lic.ID).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}

	// Paid, then suspended by an admin, then reinstated.
	paid := newLicenseOn(t, s, ctx, "trial", model.StatusActive, &future)
	link(paid)
	if err := s.SuspendLicense(ctx, paid.ID); err != nil {
		t.Fatal(err)
	}
	status, err := s.ReinstateLicense(ctx, paid.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status != model.StatusActive {
		t.Fatalf("reinstate: %s, want active", status)
	}
	if st, _ := subscriptionState(t, s, ctx, paid.ID); st != model.StatusActive {
		t.Fatalf("reinstate: subscription row %s, want active", st)
	}

	// Expired, then given a new date: active, and the trial window the
	// customer page shows is left alone.
	past := time.Now().Add(-24 * time.Hour)
	lapsed := newLicenseOn(t, s, ctx, "trial", model.StatusExpired, &past)
	link(lapsed)
	_, trialEndBefore := subscriptionState(t, s, ctx, lapsed.ID)
	_, status, err = s.SetLicenseValidUntil(ctx, lapsed.ID, &future)
	if err != nil {
		t.Fatal(err)
	}
	if status != model.StatusActive {
		t.Fatalf("new date: %s, want active", status)
	}
	if _, te := subscriptionState(t, s, ctx, lapsed.ID); sameDay(te, future) || (trialEndBefore != nil && !sameDay(te, *trialEndBefore)) {
		t.Fatalf("new date moved a Stripe-billed trial window: %v -> %v", trialEndBefore, te)
	}
}
