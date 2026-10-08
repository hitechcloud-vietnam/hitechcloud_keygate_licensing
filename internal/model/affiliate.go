package model

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/uptrace/bun"
)

// ─── Affiliate (plan §32) ───
//
// The affiliate program: an affiliate promotes the product with a
// referral code, a click on /r/<code> records the attribution, a later
// signup/order with the referral cookie converts, and the conversion
// earns a commission that is reviewed and eventually paid out.
//
// Money discipline: every percentage is an integer number of basis
// points (bps), every amount is an integer number of minor units, and
// the commission computation is integer arithmetic with a documented
// rounding rule — never a float (plan §51). A float that cannot be
// represented exactly would make the ledger disagree with the payout,
// cent by cent, until nobody trusts either.
//
// Privacy discipline: a click never stores a raw IP address or user
// agent. It stores salted SHA-256 hashes (see HashReferralIP), so the
// attribution and the click-dedup rule can work without the table ever
// becoming a location history of the visitors.

// Affiliate lifecycle values. The vocabulary is closed — the handler
// refuses anything outside these two — because status gates conversion
// recording: a suspended affiliate never converts (fraud control).
const (
	AffiliateStatusActive    = "active"
	AffiliateStatusSuspended = "suspended"
)

// AffiliateStatuses lists the vocabulary in the order the admin UI
// offers it. It is the single source the validation and any select box
// read from, so the two cannot drift apart.
var AffiliateStatuses = []string{AffiliateStatusActive, AffiliateStatusSuspended}

// ValidAffiliateStatus reports whether status is one of the two values
// an affiliate may carry.
func ValidAffiliateStatus(status string) bool {
	return status == AffiliateStatusActive || status == AffiliateStatusSuspended
}

// Commission models. percent earns a share of the order total (rate in
// bps); fixed earns a flat minor-unit amount per order. The two fields
// are both stored and the model says which one speaks — a row carries
// both numbers so an admin can switch the model without losing either.
const (
	AffiliateCommissionModelPercent = "percent"
	AffiliateCommissionModelFixed   = "fixed"
)

// AffiliateCommissionModels lists the vocabulary in admin-UI order.
var AffiliateCommissionModels = []string{AffiliateCommissionModelPercent, AffiliateCommissionModelFixed}

// ValidAffiliateCommissionModel reports whether model is one of the two
// commission models.
func ValidAffiliateCommissionModel(model string) bool {
	return model == AffiliateCommissionModelPercent || model == AffiliateCommissionModelFixed
}

// Conversion lifecycle values. A conversion is recorded pending (it has
// not been reviewed), then approved, rejected or reversed; approved
// conversions become paid when a payout settles them. Reversed is the
// clawback: the commission was earned and then taken back (a refund, a
// fraud finding). rejected and reversed are terminal — an operator who
// changed their mind records the opposite decision on the order's next
// conversion, never by silently rewriting history.
const (
	AffiliateConversionStatusPending  = "pending"
	AffiliateConversionStatusApproved = "approved"
	AffiliateConversionStatusPaid     = "paid"
	AffiliateConversionStatusRejected = "rejected"
	AffiliateConversionStatusReversed = "reversed"
)

// AffiliateConversionStatuses lists the vocabulary in review-flow
// order.
var AffiliateConversionStatuses = []string{
	AffiliateConversionStatusPending,
	AffiliateConversionStatusApproved,
	AffiliateConversionStatusPaid,
	AffiliateConversionStatusRejected,
	AffiliateConversionStatusReversed,
}

// ValidAffiliateConversionStatus reports whether status is one of the
// values a conversion may carry.
func ValidAffiliateConversionStatus(status string) bool {
	switch status {
	case AffiliateConversionStatusPending, AffiliateConversionStatusApproved,
		AffiliateConversionStatusPaid, AffiliateConversionStatusRejected,
		AffiliateConversionStatusReversed:
		return true
	}
	return false
}

// Payout lifecycle values. A payout is requested (money owed, not yet
// moved), then either paid or failed. paid and failed are terminal; a
// failed payout returns its claimed conversions to the accrued pool so
// a later payout can settle them (see store.MarkPayoutFailed).
const (
	AffiliatePayoutStatusRequested = "requested"
	AffiliatePayoutStatusPaid      = "paid"
	AffiliatePayoutStatusFailed    = "failed"
)

// AffiliatePayoutStatuses lists the vocabulary in lifecycle order.
var AffiliatePayoutStatuses = []string{
	AffiliatePayoutStatusRequested,
	AffiliatePayoutStatusPaid,
	AffiliatePayoutStatusFailed,
}

// ValidAffiliatePayoutStatus reports whether status is one of the three
// values a payout may carry.
func ValidAffiliatePayoutStatus(status string) bool {
	return status == AffiliatePayoutStatusRequested ||
		status == AffiliatePayoutStatusPaid ||
		status == AffiliatePayoutStatusFailed
}

// Affiliate is a promoter account: it earns a commission on every
// order attributed to one of its referral codes.
//
// CommissionModel says which of the two commission fields speaks:
// percent uses CommissionBPS (a share of the order total in bps),
// fixed uses CommissionMinor (a flat amount in minor units). Both are
// stored so switching the model is an edit, not a migration, and both
// are integers — money discipline, see the package note.
type Affiliate struct {
	bun.BaseModel `bun:"table:affiliates"`

	ID   string `bun:",pk" json:"id"`
	Name string `bun:",notnull" json:"name"`
	// ContactEmail is the payout correspondence address and the handle
	// the admin list searches by. Stored folded (see
	// NormalizeAffiliateEmail) and unique: two affiliates sharing one
	// address would make that address ambiguous, and the fold means one
	// address cannot be spelled two ways to slip past the index. A
	// duplicate answers 409 (store.IsAffiliateEmailConflict).
	ContactEmail string `bun:",notnull,unique" json:"contact_email"`
	// Status is one of the AffiliateStatus* values. Suspended is the
	// fraud kill-switch: the account and its history stay, but no click
	// on its codes ever converts again.
	Status string `bun:",notnull,default:'active'" json:"status"`
	// CommissionModel is percent|fixed and selects which commission
	// field pays. Closed vocabulary, defaulted in the handler and
	// enforced by the column CHECK.
	CommissionModel string `bun:",notnull,default:'percent'" json:"commission_model"`
	// CommissionBPS is the percent rate in basis points (10000 = 100%).
	// Zero is a meaningful "no commission yet" and is written as zero,
	// so there is no bun `default:` tag here (see the note on
	// Category.Position): the column keeps its CREATE-TABLE DEFAULT 0
	// for plain SQL, and the handler always writes an explicit value.
	CommissionBPS int `bun:",notnull" json:"commission_bps"`
	// CommissionMinor is the fixed-model flat amount in minor units.
	// Same zero note as CommissionBPS. Ignored while the model is
	// percent, but never dropped — switching models must not need a
	// migration to re-enter the number.
	CommissionMinor int64 `bun:",notnull" json:"commission_minor"`
	// PayoutMethod is free text (bank transfer, PayPal, …): how money
	// reaches this affiliate. Recorded, never interpreted.
	PayoutMethod string `bun:",notnull,default:''" json:"payout_method"`
	Notes        string `bun:",notnull,default:''" json:"notes"`

	CreatedAt time.Time `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt time.Time `bun:",nullzero,default:now()" json:"updated_at"`
}

// NormalizeAffiliateEmail folds an affiliate contact address into its
// stored canonical form: trimmed and lower-cased. Exactly the fold
// model.NormalizeResellerEmail applies — the two partner programs are
// one family and their handles behave the same way.
//
// Folding rather than refusing means one address cannot exist under
// several spellings ("Partner@Example.com" and "partner@example.com"
// are one affiliate), which is what the unique index on contact_email
// and the FindAffiliateByEmail lookup both rely on. Idempotent; this
// only canonicalizes — it never decides what is acceptable, which is
// apperr.ValidateEmail's job on the folded value.
func NormalizeAffiliateEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// ReferralCode is one shareable handle — the thing that goes in
// /r/<code> and in the htc_ref cookie — belonging to one affiliate.
//
// Code is stored canonical (see NormalizeReferralCode) and unique: the
// public redirect resolves exactly one row per spelling, and the fold
// means one code cannot exist under several spellings to split its
// clicks. Active is the per-code kill-switch (suspend the whole
// account with Affiliate.Status, or just this handle); deleted codes
// take their clicks with them (ON DELETE CASCADE) but keep conversions
// (ON DELETE RESTRICT — a code that earned money is a commercial
// record's handle and is deactivated, never deleted).
type ReferralCode struct {
	bun.BaseModel `bun:"table:referral_codes"`

	ID          string `bun:",pk" json:"id"`
	AffiliateID string `bun:",notnull" json:"affiliate_id"`
	Code        string `bun:",notnull,unique" json:"code"`
	// LandingURL is where /r/<code> sends the visitor. Nullable: empty
	// means "the platform default" (the handler's configured default,
	// normally /). This exact stored URL is the ONLY external redirect
	// target the endpoint ever uses — the request cannot supply or
	// amend a target, which is what keeps the redirect from becoming an
	// open redirector. Validated http(s) at write time.
	LandingURL string `bun:",nullzero" json:"landing_url,omitempty"`
	// Active has no bun `default:` tag on purpose (see the note on
	// Category.Position): false is a deliberate "this code is off" that
	// the handler must be able to write. The column keeps its
	// CREATE-TABLE DEFAULT true for plain SQL only.
	Active    bool      `bun:",notnull" json:"active"`
	CreatedAt time.Time `bun:",nullzero,default:now()" json:"created_at"`
}

// NormalizeReferralCode folds a referral code into its stored canonical
// form: upper-cased, with every character outside [A-Z0-9] removed.
// "Summer Sale '26" and "SUMMER-SALE-26" both fold to SUMMERSALE26 —
// one code, one spelling, which is what the unique index and the
// /r/<code> lookup both rely on. The fold is also why codes are safe
// in a URL path segment and in a cookie value: the stored form is
// always plain uppercase alphanumerics.
//
// The result is validated separately (ValidReferralCode) — this only
// canonicalizes, it never decides what is acceptable. Idempotent.
func NormalizeReferralCode(raw string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(raw)) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ValidReferralCode reports whether code is already a stored-form
// referral code: 4..32 characters of [A-Z0-9], nothing else. It is
// checked on the folded value (code == NormalizeReferralCode(code)),
// so an unfolded spelling is refused rather than silently rewritten
// into something the caller did not send.
func ValidReferralCode(code string) bool {
	if len(code) < 4 || len(code) > 32 {
		return false
	}
	return code == NormalizeReferralCode(code)
}

// ReferralClick is one recorded visit through /r/<code>: the
// attribution evidence behind a conversion.
//
// Privacy: IP and user agent are stored only as salted hashes (see
// HashReferralIP / HashReferralUserAgent) — a raw address is never
// written to this table. The hash is still enough to run the click
// dedup rule (one click per code + ip_hash per window) without the
// table turning into a movement history of every visitor.
//
// ConvertedUserID is the optional link to the account that later
// converted. It is defined now so a later attribution slice can stamp
// it without a migration touching this table, but nothing writes it in
// this slice — matching a click to a signup is that later slice's
// decision (the conversion row itself already carries the user).
type ReferralClick struct {
	bun.BaseModel `bun:"table:referral_clicks"`

	ID            string    `bun:",pk" json:"id"`
	CodeID        string    `bun:",notnull" json:"code_id"`
	ClickedAt     time.Time `bun:",nullzero,default:now()" json:"clicked_at"`
	IPHash        string    `bun:",notnull" json:"ip_hash"`
	UserAgentHash string    `bun:",nullzero" json:"user_agent_hash,omitempty"`
	// ConvertedUserID is nullable and caller-reported; deliberately no
	// FK to users — the click record must outlive the account.
	ConvertedUserID string `bun:",nullzero" json:"converted_user_id,omitempty"`
}

// AffiliateConversion is one attributed sale: this order was brought
// by this affiliate through this code, and earned this commission.
//
// Exactly one row per order — the unique index on order_id is the
// idempotency key of the conversion recording: a retried checkout
// callback, a double-submitted helper call and a replayed request all
// land on the same row instead of doubling the affiliate's earnings
// (fraud control).
//
// OrderID has deliberately no FK to orders: the conversion is a
// commercial record (who earned what on which sale) and must outlive
// any ledger cleanup, and the checkout that reports it may report it
// for an order written in the same breath. user_id is the same story —
// caller-reported, no FK, validated for shape only — so a deleted
// account cannot retroactively erase attribution.
type AffiliateConversion struct {
	bun.BaseModel `bun:"table:affiliate_conversions"`

	ID          string `bun:",pk" json:"id"`
	AffiliateID string `bun:",notnull" json:"affiliate_id"`
	CodeID      string `bun:",notnull" json:"code_id"`
	OrderID     string `bun:",notnull,unique" json:"order_id"`
	UserID      string `bun:",nullzero" json:"user_id,omitempty"`
	// The money, in integer minor units — the order total the
	// commission was computed on, and the commission itself (see
	// Affiliate.CommissionFor for the rounding rule). Both non-negative
	// by CHECK.
	OrderTotalMinor int64 `bun:",notnull" json:"order_total_minor"`
	CommissionMinor int64 `bun:",notnull" json:"commission_minor"`
	// Status is one of the AffiliateConversionStatus* values; see the
	// transition matrix in ConversionTransitionOK.
	Status string `bun:",notnull,default:'pending'" json:"status"`
	// PayoutID names the payout that settled (or claimed) this
	// conversion; empty while it is still accrued. Deliberately no FK
	// to affiliate_payouts: the two tables' lifecycles stay independent
	// (a failed payout leaves the record readable) and payouts are
	// never deleted.
	PayoutID  string    `bun:",nullzero" json:"payout_id,omitempty"`
	CreatedAt time.Time `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt time.Time `bun:",nullzero,default:now()" json:"updated_at"`
}

// AffiliatePayout is one movement of money from us to an affiliate,
// settling whole conversions.
//
// AmountMinor is the sum of the commissions it settles — the exact
// total of the conversions claimed when it was created, which may be a
// little less than the operator asked for: a conversion is settled
// whole or not at all (see store.CreatePayout). Recording the settled
// sum rather than the requested sum is what keeps
// payout.amount == Σ settled conversions true for every row.
type AffiliatePayout struct {
	bun.BaseModel `bun:"table:affiliate_payouts"`

	ID          string `bun:",pk" json:"id"`
	AffiliateID string `bun:",notnull" json:"affiliate_id"`
	// AmountMinor is integer minor units, non-negative by CHECK. Money
	// discipline: never a float.
	AmountMinor int64 `bun:",notnull" json:"amount_minor"`
	// Status is one of the AffiliatePayoutStatus* values; see
	// PayoutTransitionOK. Only the requested state moves.
	Status string `bun:",notnull,default:'requested'" json:"status"`
	// PaidAt is stamped when the money actually moved (the paid
	// transition), and stays NULL for requested and failed.
	PaidAt *time.Time `bun:",nullzero" json:"paid_at,omitempty"`
	Notes  string     `bun:",notnull,default:''" json:"notes"`

	CreatedAt time.Time `bun:",nullzero,default:now()" json:"created_at"`
}

// MaxCommissionableOrderMinor is the largest order total the percent
// commission math accepts. The computation multiplies the total by the
// rate in bps (at most 10000), and that product must stay inside
// int64: 9e14 minor units · 10000 = 9e18 < 2^63-1 ≈ 9.22e18. Anything
// larger is refused at the endpoint as a bad request rather than
// silently overflowing into a wrong commission.
const MaxCommissionableOrderMinor int64 = 900_000_000_000_000

// CommissionFor returns the commission an order of orderTotalMinor
// minor units earns under this affiliate's model.
//
//	percent — round-half-up of order_total · commission_bps / 10000:
//	          (total·bps + 5000) / 10000 in integer arithmetic. A
//	          remainder of exactly half a minor unit rounds up (0.5 →
//	          1), so the ledger is never a cent short of what the
//	          contract says; every smaller remainder rounds down.
//	fixed   — commission_minor exactly, whatever the order total is.
//
// The caller validates 0 ≤ orderTotalMinor ≤ MaxCommissionableOrderMinor
// before calling; the math is undefined outside that range.
func (a *Affiliate) CommissionFor(orderTotalMinor int64) int64 {
	if a.CommissionModel == AffiliateCommissionModelFixed {
		return a.CommissionMinor
	}
	// percent: +5000 before the truncating divide is the documented
	// round-half-up. All quantities are non-negative here, so the
	// integer formula is exact.
	return (orderTotalMinor*int64(a.CommissionBPS) + 5000) / 10000
}

// conversionTransitions is the review state machine: which status a
// conversion may move to from each status it holds. pending is the
// review queue; approved and paid can only be clawed back (reversed);
// rejected and reversed are terminal. paid is reached only through a
// payout settling the conversion (store.MarkPayoutPaid), never by an
// admin action directly.
var conversionTransitions = map[string][]string{
	AffiliateConversionStatusPending: {
		AffiliateConversionStatusApproved,
		AffiliateConversionStatusRejected,
		AffiliateConversionStatusReversed,
	},
	AffiliateConversionStatusApproved: {AffiliateConversionStatusReversed},
	AffiliateConversionStatusPaid:     {AffiliateConversionStatusReversed},
	AffiliateConversionStatusRejected: {},
	AffiliateConversionStatusReversed: {},
}

// ConversionTransitionOK reports whether a conversion may move from
// status from to status to. Both must be known statuses: an invented
// one is never a legal move.
func ConversionTransitionOK(from, to string) bool {
	if !ValidAffiliateConversionStatus(from) || !ValidAffiliateConversionStatus(to) {
		return false
	}
	for _, allowed := range conversionTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// payoutTransitions is the payout state machine: only a requested
// payout moves, and it moves exactly once — to paid or to failed. A
// payout that moved is a record of what happened to the money and is
// never rewritten.
var payoutTransitions = map[string][]string{
	AffiliatePayoutStatusRequested: {
		AffiliatePayoutStatusPaid,
		AffiliatePayoutStatusFailed,
	},
	AffiliatePayoutStatusPaid:   {},
	AffiliatePayoutStatusFailed: {},
}

// PayoutTransitionOK reports whether a payout may move from status
// from to status to.
func PayoutTransitionOK(from, to string) bool {
	if !ValidAffiliatePayoutStatus(from) || !ValidAffiliatePayoutStatus(to) {
		return false
	}
	for _, allowed := range payoutTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// HashReferralIP returns the salted SHA-256 hex of a client IP, the
// form referral_clicks.ip_hash stores. The raw address is never
// written anywhere (privacy discipline, see the package note); the
// hash is only ever compared to another hash — for the click dedup
// window.
//
// SALT: the salt MUST come from deployment configuration (a future
// config field fed by an env var such as REFERRAL_HASH_SALT, wired by
// the Lead — config is shared core). An empty salt still never stores
// a raw IP, but it would let an attacker with a copy of the table
// confirm a guessed address by hashing it; a per-deployment secret
// salt makes that a brute-force of the salt instead. The "ip" kind
// prefix separates this hash space from user-agent hashes, so one
// value cannot be replayed into the other column.
func HashReferralIP(salt, ip string) string {
	return hashSalted("ip", salt, ip)
}

// HashReferralUserAgent returns the salted SHA-256 hex of a user agent
// string, the form referral_clicks.user_agent_hash stores. Same salt
// discipline as HashReferralIP.
func HashReferralUserAgent(salt, userAgent string) string {
	return hashSalted("ua", salt, userAgent)
}

// hashSalted is SHA-256 over kind\0salt\0value, hex-encoded: 64
// lowercase hex characters out, no raw input recoverable from the
// column. The NUL separators make the encoding unambiguous (a kind of
// "ip" with salt "a" and value "b" cannot collide with kind "ipa",
// salt "", value "b").
func hashSalted(kind, salt, value string) string {
	h := sha256.Sum256([]byte(kind + "\x00" + salt + "\x00" + value))
	return hex.EncodeToString(h[:])
}
