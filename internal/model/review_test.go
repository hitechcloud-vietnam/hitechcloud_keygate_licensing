package model

import (
	"testing"
)

// The star vocabulary is closed: 1..5 whole stars, and 0 or 6 are
// refusals — a 0 would silently drag every aggregate down.
func TestValidReviewRating(t *testing.T) {
	for _, tc := range []struct {
		rating int
		want   bool
	}{
		{-1, false}, {0, false},
		{1, true}, {2, true}, {3, true}, {4, true}, {5, true},
		{6, false}, {100, false},
	} {
		if got := ValidReviewRating(tc.rating); got != tc.want {
			t.Errorf("ValidReviewRating(%d) = %v, want %v", tc.rating, got, tc.want)
		}
	}
}

func TestValidReviewStatus(t *testing.T) {
	if len(ReviewStatuses) != 3 {
		t.Fatalf("ReviewStatuses = %v, want the three accepted values", ReviewStatuses)
	}
	for _, s := range ReviewStatuses {
		if !ValidReviewStatus(s) {
			t.Errorf("ValidReviewStatus(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "Pending", "published", "deleted", "spam"} {
		if ValidReviewStatus(s) {
			t.Errorf("ValidReviewStatus(%q) = true, want false", s)
		}
	}
}

// One address is one customer: the fold makes the (product, email)
// uniqueness mean something. Idempotent, too.
func TestNormalizeReviewEmail(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"  USER@Example.COM ", "user@example.com"},
		{"user@example.com", "user@example.com"},
		{"", ""},
		{"   ", ""},
	} {
		if got := NormalizeReviewEmail(tc.in); got != tc.want {
			t.Errorf("NormalizeReviewEmail(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if once := NormalizeReviewEmail("A@B.com"); NormalizeReviewEmail(once) != once {
		t.Error("NormalizeReviewEmail is not idempotent")
	}
}

// The aggregate math. 4+5 stars is 4.5 stars = 45000 bps exactly —
// the case that catches a sloppy average (a float 4.4999…, or an
// integer division truncated to 44999). Empty input is the zero
// value, never a division by zero.
func TestAggregateRatings(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ratings []int
		want    RatingAggregate
	}{
		{"empty", nil, RatingAggregate{}},
		{"single 5", []int{5}, RatingAggregate{Count: 1, AverageBPS: 50000}},
		{"single 1", []int{1}, RatingAggregate{Count: 1, AverageBPS: 10000}},
		{"4 and 5", []int{4, 5}, RatingAggregate{Count: 2, AverageBPS: 45000}},
		{"1 and 5", []int{1, 5}, RatingAggregate{Count: 2, AverageBPS: 30000}},
		{"all five", []int{1, 2, 3, 4, 5}, RatingAggregate{Count: 5, AverageBPS: 30000}},
		{"1 and 2", []int{1, 2}, RatingAggregate{Count: 2, AverageBPS: 15000}},
		{"three 5s", []int{5, 5, 5}, RatingAggregate{Count: 3, AverageBPS: 50000}},
	} {
		if got := AggregateRatings(tc.ratings); got != tc.want {
			t.Errorf("%s: AggregateRatings(%v) = %+v, want %+v", tc.name, tc.ratings, got, tc.want)
		}
	}
}

// Round-half-up, pinned at the exact half. Real star inputs can never
// produce a fractional half-bps (n | 20000·sum is even either way),
// so the boundary is exercised through the SQL shape directly:
// 1 star summed over 20000 rows is 0.5 bps and must round UP to 1,
// while 1 over 40000 rows is 0.25 bps and must round DOWN to 0.
func TestRatingAggregateRounding(t *testing.T) {
	if got := RatingAggregateFromSum(1, 20000); got != (RatingAggregate{Count: 20000, AverageBPS: 1}) {
		t.Errorf("exact half: RatingAggregateFromSum(1, 20000) = %+v, want {20000 1}", got)
	}
	if got := RatingAggregateFromSum(1, 40000); got != (RatingAggregate{Count: 40000, AverageBPS: 0}) {
		t.Errorf("below half: RatingAggregateFromSum(1, 40000) = %+v, want {40000 0}", got)
	}
	// The two math shapes agree: the batch form is the same rounding
	// rule as the ratings-in-memory form, not a second one.
	if got, want := RatingAggregateFromSum(9, 2), AggregateRatings([]int{4, 5}); got != want {
		t.Errorf("shapes disagree: from-sum %+v vs from-ratings %+v", got, want)
	}
	// No reviews is the zero value on both paths.
	if got := RatingAggregateFromSum(0, 0); got != (RatingAggregate{}) {
		t.Errorf("RatingAggregateFromSum(0, 0) = %+v, want zero value", got)
	}
	if got := RatingAggregateFromSum(0, -1); got != (RatingAggregate{}) {
		t.Errorf("negative count = %+v, want zero value", got)
	}
}

// The moderation machine. Self-transitions (approve twice) are
// refused on purpose — they surface as 409 in the API — and pending is
// never re-entered.
func TestCanTransitionReview(t *testing.T) {
	for _, tc := range []struct {
		from, to string
		want     bool
	}{
		{ReviewStatusPending, ReviewStatusApproved, true},
		{ReviewStatusPending, ReviewStatusRejected, true},
		{ReviewStatusApproved, ReviewStatusRejected, true}, // unpublish
		{ReviewStatusRejected, ReviewStatusApproved, true}, // republish

		{ReviewStatusPending, ReviewStatusPending, false},
		{ReviewStatusApproved, ReviewStatusApproved, false}, // approve twice
		{ReviewStatusRejected, ReviewStatusRejected, false}, // reject twice
		{ReviewStatusApproved, ReviewStatusPending, false},
		{ReviewStatusRejected, ReviewStatusPending, false},
		{"", ReviewStatusApproved, false},
		{ReviewStatusApproved, "", false},
		{"published", ReviewStatusApproved, false},
		{ReviewStatusApproved, "published", false},
	} {
		if got := CanTransitionReview(tc.from, tc.to); got != tc.want {
			t.Errorf("CanTransitionReview(%q, %q) = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}
