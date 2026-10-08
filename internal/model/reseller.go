package model

import (
	"strings"
	"time"

	"github.com/uptrace/bun"
)

// ─── Reseller (plan §31 / Phase 7) ───
//
// A reseller is a partner account that sells our licences to its own
// customers. Slice 1 keeps only the account itself and the record of
// which licences it owns (has sold); wholesale pricing, commissions
// and a reseller-facing API grow on top of these tables later, so the
// rows here are deliberately just identity + lifecycle + the money
// rate that those later slices will read.
//
// Money discipline: every percentage in this domain is an integer
// number of basis points (bps), never a float. commission_bps is the
// share of a sale the reseller earns, in bps (10000 bps = 100%), so
// arithmetic on it stays exact and no rounding drift can appear
// between what a contract says and what a payout computes.

// Reseller lifecycle values. The vocabulary is closed — the handler
// refuses anything outside these two — because status feeds the
// allocation rules and, later, commission eligibility, and a made-up
// status would be silently ignored by both.
const (
	ResellerStatusActive    = "active"
	ResellerStatusSuspended = "suspended"
)

// ResellerStatuses lists the vocabulary in the order the admin UI
// offers it. It is the single source the validation and any select
// box read from, so the two cannot drift apart.
var ResellerStatuses = []string{ResellerStatusActive, ResellerStatusSuspended}

// ValidResellerStatus reports whether status is one of the two values
// a reseller may carry.
func ValidResellerStatus(status string) bool {
	return status == ResellerStatusActive || status == ResellerStatusSuspended
}

type Reseller struct {
	bun.BaseModel `bun:"table:resellers"`

	ID   string `bun:",pk" json:"id"`
	Name string `bun:",notnull" json:"name"`
	// ContactEmail is the address the reseller is reached at and, more
	// importantly, the handle the future reseller API and the wholesale
	// correspondence address a partner by. Stored folded (see
	// NormalizeResellerEmail) and unique: two resellers sharing one
	// address would make that address ambiguous, and the fold means one
	// address cannot be spelled two ways to slip past the index. A
	// duplicate answers 409 (store.IsResellerEmailConflict).
	ContactEmail string `bun:",notnull,unique" json:"contact_email"`
	// Status is one of the ResellerStatus* values. Suspended is a soft
	// stop: the account stays and its allocations are kept, but it is
	// not an active partner. Defaulted in the handler and by the
	// column, never accepted raw.
	Status string `bun:",notnull,default:'active'" json:"status"`
	// CommissionBPS is the reseller's earnings share in basis points
	// (10000 = 100%). Integer only — money discipline: percentages are
	// bps, never floats. Zero is a meaningful "no commission yet" and
	// is written as zero, so there is no bun `default:` tag here (see
	// the same note on Category.Position): the column keeps its
	// CREATE-TABLE DEFAULT 0 for plain SQL, and the handler always
	// writes an explicit value.
	CommissionBPS int    `bun:",notnull" json:"commission_bps"`
	Notes         string `bun:",notnull,default:''" json:"notes"`

	CreatedAt time.Time `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt time.Time `bun:",nullzero,default:now()" json:"updated_at"`
}

// NormalizeResellerEmail folds a reseller contact address into its
// stored canonical form: trimmed and lower-cased.
//
// Folding rather than refusing means one address cannot exist under
// several spellings ("Partner@Example.com" and "partner@example.com"
// are one reseller), which is what the unique index on contact_email
// and the FindResellerByEmail lookup both rely on. The trade is
// deliberate: strictly, the local part of an address may be
// case-sensitive, but a partner contact used as a handle is far more
// useful case-insensitive than it is RFC-precise, and the fold is what
// makes the uniqueness mean anything.
//
// Idempotent: folding a folded address returns it unchanged. This only
// canonicalizes — it never decides what is acceptable, which is
// apperr.ValidateEmail's job on the folded value.
func NormalizeResellerEmail(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// ResellerLicense is one allocation: this reseller owns (sold) this
// licence. It lives in this file rather than model.go so the shared
// core needs no change for the reseller round (model.go is contested
// ground under parallel agents); the join has no identity of its own
// beyond the pair.
//
// Cardinality, decided and enforced in the migration:
//
//   - reseller → licences is 1:N. A reseller sells many licences, so
//     the pair (reseller_id, license_id) is the primary key and the
//     reseller-side lookup ("what does this reseller own") walks it.
//
//   - licence → reseller is at most 1:1. A licence is owned by AT MOST
//     ONE reseller, enforced by a UNIQUE index on license_id alone —
//     one row per license_id, so a second reseller can never claim a
//     licence already allocated. This is the commercial truth the
//     later commission slice depends on: a sale is attributed to one
//     partner, never split across two.
//
// Referential actions: license_id cascades (a licence deleted has its
// allocation go with it — the row cannot orphan), while reseller_id
// restricts (a reseller cannot be deleted while it still owns
// licences; see the delete policy in store.DeleteReseller).
type ResellerLicense struct {
	bun.BaseModel `bun:"table:reseller_licenses"`

	// Both columns are the primary key: the pair is the row's whole
	// identity. The licence-side exclusivity (one reseller per licence)
	// is the separate UNIQUE index on license_id in the migration.
	ResellerID  string    `bun:",pk" json:"reseller_id"`
	LicenseID   string    `bun:",pk" json:"license_id"`
	AllocatedAt time.Time `bun:",nullzero,default:now()" json:"allocated_at"`
}

// ResellerCustomer is the customer-referral link: this reseller
// brought this customer. It is defined now so Phase 7 can grow onto it
// without another migration touching these tables, but slice 1 writes
// no store or handler method for it — allocation of licences is the
// slice's scope, allocation of customers is a later one.
//
// The identity is the pair (reseller_id, customer_email), keyed like
// reseller_licenses. customer_email is the durable handle (a customer
// may predate any user account); user_id is an optional enrichment
// linking to users(id) once the customer has an account. Unlike a
// licence, "unique pair" is the whole of the constraint here — a
// customer is not yet pinned to a single reseller the way a licence
// is, and tightening that is a decision for the slice that reasons
// about it.
type ResellerCustomer struct {
	bun.BaseModel `bun:"table:reseller_customers"`

	ResellerID    string `bun:",pk" json:"reseller_id"`
	CustomerEmail string `bun:",pk" json:"customer_email"`
	// UserID is nullable: the customer may not have an account yet, and
	// if the account is ever removed the referral link survives (the
	// email is the durable identity). See the migration's ON DELETE
	// SET NULL.
	UserID      string    `bun:",nullzero" json:"user_id,omitempty"`
	AllocatedAt time.Time `bun:",nullzero,default:now()" json:"allocated_at"`
}
