package payment

import (
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// A subscription event that arrives after the licence was unlinked
// must change nothing and say so, because everything the caller does
// next — the audit line, the license.canceled webhook, the dunning
// mail — describes a write that did not happen. Telling a downstream
// system a licence was cancelled while the row stays active is worse
// than silence: it revokes access the database still grants.
func TestUnlinkedSubscriptionEventIsNotApplied(t *testing.T) {
	s, ctx := openStore(t)
	defer s.Close()
	plan := seedPlan(t, s, ctx, "unlinked", "subscription")
	subID := "sub_unlinked_" + plan.Slug
	lic := &model.License{
		ProductID: plan.ProductID, PlanID: plan.ID,
		Email:      "unlinked-" + plan.Slug + "@example.com",
		LicenseKey: "KG-UNLINK-" + plan.Slug,
		Status:     model.StatusActive,
	}
	if err := s.CreateLicense(ctx, lic); err != nil {
		t.Fatalf("create license: %v", err)
	}
	if _, err := s.DB.NewRaw("UPDATE licenses SET stripe_subscription_id = ? WHERE id = ?", subID, lic.ID).Exec(ctx); err != nil {
		t.Fatalf("link subscription: %v", err)
	}

	// What a webhook holds: the licence as it was when the event
	// resolved it.
	inFlight, err := s.FindLicenseByStripeSubscription(ctx, subID)
	if err != nil {
		t.Fatalf("find by subscription: %v", err)
	}

	// The admin unlinks in the window before the write.
	if cleared, err := s.ClearStripeSubscription(ctx, lic.ID, subID); err != nil || !cleared {
		t.Fatalf("unlink = (%v, %v), want (true, nil)", cleared, err)
	}

	h := &StripeHandler{Store: s}
	canceled := time.Now()
	inFlight.Status = model.StatusCanceled
	inFlight.CanceledAt = &canceled
	if h.applyLicenseFromSubscription(ctx, inFlight, "customer.subscription.deleted", nil, "status", "canceled_at") {
		t.Fatal("stale event reported as applied; the caller would then audit and dispatch a cancellation that never happened")
	}

	after, err := s.FindLicenseByID(ctx, lic.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.Status != model.StatusActive {
		t.Errorf("status = %q, want %q", after.Status, model.StatusActive)
	}
	if after.CanceledAt != nil {
		t.Errorf("canceled_at = %v, want nil", after.CanceledAt)
	}

	// A licence still on its subscription is written as before.
	lic2 := &model.License{
		ProductID: plan.ProductID, PlanID: plan.ID,
		Email:      "linked-" + plan.Slug + "@example.com",
		LicenseKey: "KG-LINKED-" + plan.Slug,
		Status:     model.StatusActive,
	}
	if err := s.CreateLicense(ctx, lic2); err != nil {
		t.Fatalf("create second license: %v", err)
	}
	if _, err := s.DB.NewRaw("UPDATE licenses SET stripe_subscription_id = ? WHERE id = ?", subID+"-live", lic2.ID).Exec(ctx); err != nil {
		t.Fatalf("link second subscription: %v", err)
	}
	live, err := s.FindLicenseByStripeSubscription(ctx, subID+"-live")
	if err != nil {
		t.Fatalf("find second: %v", err)
	}
	live.Status = model.StatusCanceled
	if !h.applyLicenseFromSubscription(ctx, live, "customer.subscription.deleted", nil, "status") {
		t.Fatal("a licence still on its subscription was not written")
	}
	if reloaded, err := s.FindLicenseByID(ctx, lic2.ID); err != nil || reloaded.Status != model.StatusCanceled {
		t.Fatalf("second license status = %v (err %v), want canceled", reloaded.Status, err)
	}
}

// Every caller of applyLicenseFromSubscription has to act on its
// answer. Ignoring it is how the side effects got out of step with the
// database in the first place, and the compiler will not say a word
// about a dropped bool.
func TestEveryCallerChecksTheApplyResult(t *testing.T) {
	src, err := os.ReadFile("stripe.go")
	if err != nil {
		t.Fatalf("read stripe.go: %v", err)
	}
	calls := regexp.MustCompile(`(?m)^(\s*)(.*)h\.(?:applyLicenseFromSubscription|endLicenseFromSubscription)\(`).FindAllStringSubmatch(string(src), -1)
	if len(calls) == 0 {
		t.Fatal("no call sites found — did the helper get renamed?")
	}
	for _, c := range calls {
		if prefix := c[2]; prefix != "if !" {
			t.Errorf("call site ignores the result (prefix %q); it must read `if !h.applyLicenseFromSubscription(...) { return }`", prefix)
		}
	}
	if len(calls) < 6 {
		t.Errorf("found %d call sites, expected the six subscription webhooks", len(calls))
	}
}
