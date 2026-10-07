package model

import (
	"testing"
	"time"
)

func TestInitialUpdatesUntil(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	if got := (&Plan{LicenseType: "perpetual", UpdatesDays: 365}).InitialUpdatesUntil(now); got == nil || !got.Equal(now.AddDate(0, 0, 365)) {
		t.Fatalf("perpetual with 365 days: got %v", got)
	}
	if got := (&Plan{LicenseType: "perpetual", UpdatesDays: 0}).InitialUpdatesUntil(now); got != nil {
		t.Fatalf("perpetual with updates for life must have no limit, got %v", got)
	}
	if got := (&Plan{LicenseType: "subscription", UpdatesDays: 365}).InitialUpdatesUntil(now); got != nil {
		t.Fatalf("subscription must follow valid_until, got %v", got)
	}
}

func TestOffersRenewal(t *testing.T) {
	cases := []struct {
		p    Plan
		want bool
	}{
		{Plan{Active: true, LicenseType: "perpetual", RenewalDays: 365, StripeRenewalPriceID: "price_1"}, true},
		{Plan{LicenseType: "perpetual", RenewalDays: 365, StripeRenewalPriceID: "price_1"}, false}, // deactivated
		{Plan{Active: true, LicenseType: "perpetual", RenewalDays: 365}, false},
		{Plan{Active: true, LicenseType: "perpetual", StripeRenewalPriceID: "price_1"}, false},
		{Plan{Active: true, LicenseType: "subscription", RenewalDays: 365, StripeRenewalPriceID: "price_1"}, false},
	}
	for _, c := range cases {
		if got := c.p.OffersRenewal(); got != c.want {
			t.Errorf("%+v: got %v want %v", c.p, got, c.want)
		}
	}
}

// A renewal bought before the period ends continues from the current
// end; one bought after it starts from now. Lapsed time is neither
// sold twice nor given away.
func TestRenewedUpdatesUntil(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	future := now.AddDate(0, 0, 100)
	past := now.AddDate(0, 0, -30)

	if got := RenewedUpdatesUntil(&future, now, 365); !got.Equal(future.AddDate(0, 0, 365)) {
		t.Fatalf("early renewal: got %v want %v", got, future.AddDate(0, 0, 365))
	}
	if got := RenewedUpdatesUntil(&past, now, 365); !got.Equal(now.AddDate(0, 0, 365)) {
		t.Fatalf("late renewal: got %v want %v", got, now.AddDate(0, 0, 365))
	}
	if got := RenewedUpdatesUntil(nil, now, 365); !got.Equal(now.AddDate(0, 0, 365)) {
		t.Fatalf("no current end: got %v want %v", got, now.AddDate(0, 0, 365))
	}
}

// The ledger replay is what refunds derive the period from. Refunds
// in any order must land on the dates a customer who never bought the
// refunded renewals would have had.
func TestReplayRenewals(t *testing.T) {
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	expired := now.AddDate(0, 0, -30)
	a := &LicenseRenewal{ID: "a", PreviousUpdatesUntil: &expired, Days: 365, CreatedAt: now}
	aEnd := now.AddDate(0, 0, 365)
	a.UpdatesUntil = &aEnd
	b := &LicenseRenewal{ID: "b", PreviousUpdatesUntil: &aEnd, Days: 100, CreatedAt: now.Add(time.Hour)}
	bEnd := aEnd.AddDate(0, 0, 100)
	b.UpdatesUntil = &bEnd

	if got := ReplayRenewals([]*LicenseRenewal{a, b}); got == nil || !got.Equal(bEnd) {
		t.Fatalf("full ledger: got %v want %v", got, bEnd)
	}
	a.RefundedAt = &now
	// Without A, B started from its own purchase time on a lapsed period.
	if got := ReplayRenewals([]*LicenseRenewal{a, b}); got == nil || !got.Equal(now.Add(time.Hour).AddDate(0, 0, 100)) {
		t.Fatalf("A refunded: got %v", got)
	}
	b.RefundedAt = &now
	// Everything refunded: back to the original, already lapsed, end.
	if got := ReplayRenewals([]*LicenseRenewal{a, b}); got == nil || !got.Equal(expired) {
		t.Fatalf("all refunded: got %v want %v", got, expired)
	}
	// A renewal applied to a lifetime license never counts.
	life := &LicenseRenewal{ID: "l", Days: 365, CreatedAt: now}
	if got := ReplayRenewals([]*LicenseRenewal{life}); got != nil {
		t.Fatalf("lifetime renewal produced an end: %v", got)
	}
	if ReplayRenewals(nil) != nil {
		t.Fatal("empty ledger must have no end")
	}

	// A no-effect row (lifetime at the time), then an admin sets a
	// lapsed cutoff by hand, then a renewal is bought and refunded:
	// the hand-set cutoff comes back, not lifetime updates.
	cutoff := now.AddDate(0, 0, -10)
	y := &LicenseRenewal{ID: "y", PreviousUpdatesUntil: &cutoff, Days: 365, CreatedAt: now.Add(2 * time.Hour)}
	yEnd := now.Add(2*time.Hour).AddDate(0, 0, 365)
	y.UpdatesUntil = &yEnd
	if got := ReplayRenewals([]*LicenseRenewal{life, y}); got == nil || !got.Equal(yEnd) {
		t.Fatalf("after y: got %v want %v", got, yEnd)
	}
	y.RefundedAt = &now
	if got := ReplayRenewals([]*LicenseRenewal{life, y}); got == nil || !got.Equal(cutoff) {
		t.Fatalf("y refunded: got %v want the hand-set cutoff %v", got, cutoff)
	}

	// An admin edit between two renewals is an offset, not a new
	// baseline. With both renewals live the license stands where the
	// edit plus the later renewal put it; refunding the earlier one
	// removes exactly its days and keeps the edit and the later one.
	a2 := &LicenseRenewal{ID: "a2", PreviousUpdatesUntil: &expired, Days: 365, CreatedAt: now}
	a2.UpdatesUntil = &aEnd
	edited := now.AddDate(0, 0, 500)
	z := &LicenseRenewal{ID: "z", PreviousUpdatesUntil: &edited, Days: 100, CreatedAt: now.Add(3 * time.Hour)}
	zEnd := edited.AddDate(0, 0, 100)
	z.UpdatesUntil = &zEnd
	if got := ReplayRenewals([]*LicenseRenewal{a2, z}); got == nil || !got.Equal(zEnd) {
		t.Fatalf("both live: got %v want %v", got, zEnd)
	}
	a2.RefundedAt = &now
	// expired + the admin's shift (edited - aEnd) + z's 100 days.
	want := expired.Add(edited.Sub(aEnd)).AddDate(0, 0, 100)
	if got := ReplayRenewals([]*LicenseRenewal{a2, z}); got == nil || !got.Equal(want) {
		t.Fatalf("refunding the earlier renewal must remove its days only: got %v want %v", got, want)
	}
}

func TestEffectiveUpdatesUntil(t *testing.T) {
	d := time.Now()
	perp := &License{UpdatesUntil: &d, Plan: &Plan{LicenseType: "perpetual"}}
	sub := &License{UpdatesUntil: &d, Plan: &Plan{LicenseType: "subscription"}}
	noPlan := &License{UpdatesUntil: &d}
	if perp.EffectiveUpdatesUntil() == nil || sub.EffectiveUpdatesUntil() != nil || noPlan.EffectiveUpdatesUntil() == nil {
		t.Fatal("effective period must apply on perpetual plans only")
	}
}
