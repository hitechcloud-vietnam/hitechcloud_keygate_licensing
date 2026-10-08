package model

import (
	"time"

	"github.com/uptrace/bun"
)

// ─── Reseller commerce, slice 2 (plan §31 / Phase 7) ───
//
// Two models grow on the reseller rows of slice 1
// (internal/model/reseller.go):
//
//   - Commission — the ledger of what a reseller earned on each sale.
//     A commission row is a self-contained commercial record: the
//     basis it was computed on, the rate used and the exact amount,
//     snapshotted at accrual time so no later price or contract change
//     can rewrite history.
//
//   - ResellerPriceOverride — the wholesale price one reseller pays
//     for one plan, overriding the public (Stripe) price for that
//     partner. Configuration, not history: it is keyed by
//     (reseller, plan) and overwritten in place when the deal changes.
//
// Both live in this file because the reseller round extends
// reseller.go via its own files (see the note there on contested
// shared-core files); the pairing is the slice's subject matter.
//
// Money discipline, twice as loud here because this IS the money:
// every amount is an int64 of minor units and every percentage is an
// integer number of basis points (bps). Never a float — a float in a
// ledger is drift waiting to happen.

// Commission lifecycle values. The vocabulary is closed — the CHECK in
// the migration and ValidCommissionStatus both refuse anything else —
// because the status feeds the payout dashboards and the payout rules,
// and a made-up status would silently vanish from both.
//
// The lifecycle the vocabulary models:
//
//	accrued ──► approved ──► paid
//	    │            │
//	    └────────────┴──► cancelled
//
// accrued   — recorded, not yet confirmed for payout.
// approved  — confirmed for the next payout run.
// paid      — disbursed; paid_at carries when.
// cancelled — void (refunded order, broken deal); never payable.
//
// Slice 2's API writes only accrued (accrual) and paid (the payout
// mark); approved and cancelled are written by the slices that grow
// the approval and payout workflows. The vocabulary is complete now so
// those slices need no migration.
const (
	CommissionStatusAccrued   = "accrued"
	CommissionStatusApproved  = "approved"
	CommissionStatusPaid      = "paid"
	CommissionStatusCancelled = "cancelled"
)

// CommissionStatuses lists the vocabulary in display order. It is the
// single source the validation and the dashboard keys read from, so
// the two cannot drift apart.
var CommissionStatuses = []string{
	CommissionStatusAccrued,
	CommissionStatusApproved,
	CommissionStatusPaid,
	CommissionStatusCancelled,
}

// ValidCommissionStatus reports whether status is one of the four
// values a commission row may carry.
func ValidCommissionStatus(status string) bool {
	switch status {
	case CommissionStatusAccrued, CommissionStatusApproved,
		CommissionStatusPaid, CommissionStatusCancelled:
		return true
	}
	return false
}

// ValidCommissionBPS reports whether bps is a legal commission rate:
// 0..10000 basis points, where 10000 is 100%. Zero is legal and
// meaningful ("no commission on this sale"), so callers must not read
// it as absent. Integer only — see the money discipline above.
func ValidCommissionBPS(bps int) bool {
	return bps >= 0 && bps <= 10000
}

// CommissionAmount is the accrual math: the commission earned on a
// basis of basisMinor minor units at a rate of bps basis points,
//
//	amount = basis_minor × bps / 10000
//
// rounded DOWN to a whole minor unit. Integer division truncates, and
// the truncation is toward zero; since the basis is CHECKed
// non-negative and the rate is bounded to 0..10000, that is floor.
//
// Rounding down is the documented choice, not an accident of integer
// division: a fractional minor unit does not exist, and the fraction
// stays with the house rather than being paid out. The ledger records
// the exact integer that was accrued — basis, rate and amount — so
// anyone can re-derive the number and see the same rounding rule.
//
// The multiplication is split (q·bps + (r·bps)/10000 for
// basis = 10000·q + r) so it is exact and cannot overflow for ANY
// non-negative int64 basis with bps ≤ 10000: naive basis×bps overflows
// silently past ~9.2e14 minor units, and a ledger that quietly wraps
// is worse than one that is wrong loudly.
func CommissionAmount(basisMinor int64, bps int) int64 {
	if basisMinor <= 0 || bps <= 0 {
		return 0
	}
	b := int64(bps)
	return (basisMinor/10000)*b + (basisMinor%10000)*b/10000
}

// Commission is one ledger row: what this reseller earned on this
// order. It is append-mostly — accrual creates it, the payout mark
// stamps it — because a commission that changes after the fact is a
// dispute, and disputes are resolved by cancelling and re-accruing,
// not by editing history.
type Commission struct {
	bun.BaseModel `bun:"table:commissions"`

	ID         string `bun:",pk" json:"id"`
	ResellerID string `bun:",notnull" json:"reseller_id"`
	// OrderID is the sale this commission is owed on. It is TEXT with
	// NO foreign key on purpose: the commission ledger is a financial
	// record that must outlive order-retention purges — when an order
	// row is eventually deleted, what the partner was paid for it must
	// still be answerable. The row is self-contained (basis, rate and
	// amount are all snapshotted here), so it never needs to join back
	// to the order to mean anything. Uniqueness with reseller_id is
	// what makes accrual idempotent: at most one commission per
	// (reseller, order).
	OrderID string `bun:",notnull" json:"order_id"`
	// BasisMinor is the order amount the commission was computed on,
	// in minor units of Currency-free integer money. Snapshotted: a
	// later refund is handled by cancelling this row, not by mutating
	// the basis under it.
	BasisMinor int64 `bun:",notnull" json:"basis_minor"`
	// BPS is the rate actually used for this accrual, in basis points
	// (10000 = 100%). Snapshot again: the reseller's contract rate can
	// change later and must not rewrite what this sale owed. Integer
	// only — money discipline.
	BPS int `bun:",notnull" json:"bps"`
	// AmountMinor is the exact int64 result of CommissionAmount at
	// accrual time — never re-derived from live rates, so the ledger
	// cannot disagree with itself.
	AmountMinor int64 `bun:",notnull" json:"amount_minor"`
	// Status is one of the CommissionStatus* values (closed
	// vocabulary; see the constants for the lifecycle).
	Status string `bun:",notnull,default:'accrued'" json:"status"`
	// PaidAt is set when the commission is disbursed (status paid).
	PaidAt *time.Time `json:"paid_at,omitempty"`
	Notes  string     `bun:",notnull,default:''" json:"notes"`

	CreatedAt time.Time `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt time.Time `bun:",nullzero,default:now()" json:"updated_at"`
}

// ValidCurrencyCode reports whether code has the shape of an ISO 4217
// currency code: exactly three uppercase ASCII letters.
//
// This validates SHAPE only. Whether "XYZ" is a real currency is a
// fact about the outside world (and specifically about the Stripe
// price the plan sells at) that cannot be checked offline, so the
// client's ISO code is accepted as given once it is well-formed — the
// same trust boundary every currency-tagged money field draws. The
// migration enforces the same shape with a CHECK, so a value that
// skips this validation still cannot be stored malformed.
func ValidCurrencyCode(code string) bool {
	if len(code) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if code[i] < 'A' || code[i] > 'Z' {
			return false
		}
	}
	return true
}

// ResellerPriceOverride is the wholesale price one reseller pays for
// one plan: what costs the public the Stripe price costs this partner
// UnitAmountMinor. It is configuration keyed by (reseller, plan) —
// the pair is the whole identity — and is written over in place when
// the deal changes; there is no price history here, the commission
// ledger is where past money is remembered.
//
// The currency is the client's ISO 4217 code (shape-validated, see
// ValidCurrencyCode). It cannot be checked against the plan's Stripe
// price offline, so the override carries its own currency and any
// checkout that reads it must convert or refuse deliberately.
type ResellerPriceOverride struct {
	bun.BaseModel `bun:"table:reseller_price_overrides"`

	ResellerID string `bun:",pk" json:"reseller_id"`
	// PlanID references plans(id) ON DELETE CASCADE: a plan deleted
	// takes its overrides with it (price config for a dead plan means
	// nothing), while the commission ledger above deliberately
	// survives its referents.
	PlanID string `bun:",pk" json:"plan_id"`
	// UnitAmountMinor is the wholesale price in minor units. Money is
	// int64 minor units — never float. Zero is a legal wholesale price
	// (a comped partner) and the CHECK bounds it non-negative.
	UnitAmountMinor int64  `bun:",notnull" json:"unit_amount_minor"`
	Currency        string `bun:",notnull" json:"currency"`

	CreatedAt time.Time `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt time.Time `bun:",nullzero,default:now()" json:"updated_at"`
}
