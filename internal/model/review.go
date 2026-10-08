package model

import (
	"strings"
	"time"

	"github.com/uptrace/bun"
)

// ─── Product reviews (marketplace, Phase 6 leftovers) ───
//
// A review is a customer's star rating and written opinion of one
// product, published on the marketplace after moderation. It is
// catalog-adjacent content: it changes what the storefront SHOWS and
// never what a licence may DO, so moderation can never reach a
// customer's entitlements.
//
// The row carries no purchase proof. "Verified purchase" is a
// documented future flag (the order ledger can answer it); today the
// review is attributed to the portal account that wrote it and
// moderated before publication.
//
// Ratings are integers of stars (1..5) and the aggregate the listings
// render is integer basis points — the same no-float discipline as the
// money and rate fields elsewhere: an average that drifts by a
// floating-point ulp is an average two clients can disagree about.

// Review status values (closed vocabulary — the migration's CHECK and
// ValidReviewStatus both refuse anything else).
//
// The moderation machine:
//
//	pending ──► approved ──► rejected (unpublish)
//	    │                        │
//	    └────► rejected ──► approved (republish)
//
// pending  — submitted, waiting for moderation. Never public.
// approved — published on the storefront.
// rejected — withheld (or taken down again). Kept for the audit trail.
const (
	ReviewStatusPending  = "pending"
	ReviewStatusApproved = "approved"
	ReviewStatusRejected = "rejected"
)

// ReviewStatuses lists the vocabulary in display order — the single
// source the validation and the dashboard keys read from.
var ReviewStatuses = []string{
	ReviewStatusPending,
	ReviewStatusApproved,
	ReviewStatusRejected,
}

// Review content limits, mirrored by the migration's CHECKs. The
// handler validates these before a write so the refusal is a 400, not
// a constraint violation; the database keeps the same bound for
// anything that skips the handler.
const (
	MaxReviewTitleLen = 120
	MaxReviewBodyLen  = 4000
)

// Star rating bounds. A rating is whole stars only — no halves — so
// the aggregate's input set is exactly {1,2,3,4,5} and the average of
// n reviews is always sum·10000/n in bps.
const (
	MinReviewRating = 1
	MaxReviewRating = 5
)

// Review is one customer's review of one product. Unique per
// (product, customer email) — one review per customer per product,
// folded so one address cannot hold two rows under two spellings.
type Review struct {
	bun.BaseModel `bun:"table:product_reviews"`

	ID string `bun:",pk" json:"id"`
	// ProductID is the product under review. ON DELETE CASCADE in the
	// migration: reviews are content hung on the product and go with
	// it, so a deletion can neither be blocked nor leave orphans.
	ProductID string `bun:",notnull" json:"product_id"`
	// CustomerEmail is the folded (lowercase) portal account address
	// of the author — the identity the review is attributed to and
	// the ownership handle the portal edit/delete endpoints match on.
	// It is deliberately NOT rendered on the public storefront (see
	// the handlers' field selection).
	CustomerEmail string `bun:",notnull" json:"customer_email"`
	// CustomerName is the display name beside the review. Nullable:
	// the account may not carry one.
	CustomerName string `bun:",nullzero" json:"customer_name,omitempty"`
	// Rating is whole stars, 1..5 (ValidReviewRating).
	Rating int `bun:",notnull" json:"rating"`
	// Title is an optional headline (≤ MaxReviewTitleLen). Nullable.
	Title string `bun:",nullzero" json:"title,omitempty"`
	// Body is the review text (≤ MaxReviewBodyLen).
	Body string `bun:",notnull" json:"body"`
	// Status is one of the ReviewStatus* values; a fresh review is
	// always pending (store.CreateReview enforces it).
	Status string `bun:",notnull,default:'pending'" json:"status"`
	// AdminReply is the vendor's public answer under the review.
	// Nullable, bounded like the body.
	AdminReply string `bun:",nullzero" json:"admin_reply,omitempty"`

	CreatedAt time.Time `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt time.Time `bun:",nullzero,default:now()" json:"updated_at"`
}

// ValidReviewRating reports whether rating is a legal star rating:
// whole stars, 1..5. Zero is NOT "unrated" — a review always carries
// a rating, and a 0 would silently drag the aggregate down.
func ValidReviewRating(rating int) bool {
	return rating >= MinReviewRating && rating <= MaxReviewRating
}

// ValidReviewStatus reports whether status is one of the three values
// a review row may carry.
func ValidReviewStatus(status string) bool {
	switch status {
	case ReviewStatusPending, ReviewStatusApproved, ReviewStatusRejected:
		return true
	}
	return false
}

// NormalizeReviewEmail folds a customer address into its stored
// canonical form: trimmed and lower-cased — the same fold as
// model.NormalizeResellerEmail, for the same reason. Folding rather
// than refusing means one address cannot hold two reviews of one
// product under different spellings ("A@Example.com" and
// "a@example.com" are one customer), which is what the unique
// (product_id, customer_email) pair and the portal ownership lookup
// both rely on. Idempotent: folding a folded address returns it
// unchanged. This only canonicalizes — it never decides what is
// acceptable.
func NormalizeReviewEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// RatingAggregate is the star summary the listings render beside a
// product: how many approved reviews it is drawn from, and their
// average in basis points (10000 = 1 star, 50000 = 5 stars).
//
// Bps rather than a float — see the money discipline. The zero value
// is the honest "no approved reviews yet" answer: count 0, average 0.
type RatingAggregate struct {
	Count      int `json:"count"`
	AverageBPS int `json:"average_bps"`
}

// AggregateRatings reduces a set of star ratings to the count and the
// average in basis points:
//
//	average_bps = round_half_up(sum(ratings) · 10000 / n)
//
// Integer arithmetic throughout. (2·sum·10000 + n) / (2·n) is exactly
// round-half-up: writing sum·10000 = q·n + r, the formula floors to
// q + 1 precisely when 2r ≥ n — a remainder of exactly half a unit
// rounds up (0.5 → 1), every smaller remainder rounds down. Empty
// input returns the zero value ("no reviews yet"), never a division by
// zero.
//
// The inputs are ratings of whole stars 1..5, so n ≤ sum ≤ 5n and the
// products stay far inside int64.
func AggregateRatings(ratings []int) RatingAggregate {
	if len(ratings) == 0 {
		return RatingAggregate{}
	}
	var sum int64
	for _, r := range ratings {
		sum += int64(r)
	}
	return RatingAggregateFromSum(sum, int64(len(ratings)))
}

// RatingAggregateFromSum is the same math on its SQL-friendly shape:
// a star sum and a count, which is what a GROUP BY can produce
// without hauling every rating out of the database (see
// store.RatingSummariesForProducts). AggregateRatings delegates here,
// so the rounding rule has exactly one implementation.
func RatingAggregateFromSum(starSum, count int64) RatingAggregate {
	if count <= 0 {
		return RatingAggregate{}
	}
	return RatingAggregate{
		Count:      int(count),
		AverageBPS: int((2*starSum*10000 + count) / (2 * count)),
	}
}

// CanTransitionReview reports whether a review may move from status
// from to status to. The machine, in full:
//
//	pending  -> approved (publish)   | rejected (withhold)
//	approved -> rejected (unpublish)
//	rejected -> approved (republish)
//
// Self-transitions are refused — approving an approved review should
// surface (409 REVIEW_TRANSITION_INVALID), not silently succeed and
// blur who published what and when. pending is never re-entered: an
// edited review keeps its status (editing content is the author's
// right; re-moderation on edit is a documented future flag), and the
// two post-publication moves above are the whole of the admin machine.
func CanTransitionReview(from, to string) bool {
	switch from {
	case ReviewStatusPending:
		return to == ReviewStatusApproved || to == ReviewStatusRejected
	case ReviewStatusApproved:
		return to == ReviewStatusRejected
	case ReviewStatusRejected:
		return to == ReviewStatusApproved
	}
	// Unknown statuses never move.
	return false
}
