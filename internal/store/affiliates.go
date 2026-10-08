package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/uptrace/bun"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Affiliates (plan §32) ───
//
// Affiliate accounts, their referral codes, the click attribution
// behind every conversion, the conversions themselves and the payouts
// that settle them. Money here is integer minor units and integer bps,
// never floats — see model/affiliate.go.
//
// Fraud controls implemented at this layer:
//
//   - one conversion per order: the UNIQUE index on order_id is the
//     idempotency key; a retried RecordConversion returns the row that
//     is already there instead of doubling the commission.
//   - click dedup: the same code + ip_hash within ClickDedupWindow
//     records one click, so a refresh loop cannot inflate an
//     affiliate's click counts.
//   - suspended affiliates never convert: RecordConversion re-checks
//     the affiliate's status (and the code's active flag) even though
//     the handler checks first, because this is the layer that writes
//     the money.
//
// Commission caps (a maximum commission per conversion or per period)
// are deliberately NOT in this slice — documented as future work in
// the migration and the plan.

// ClickDedupWindow is the click dedup window: a second click on the
// same code from the same ip_hash within this window is not recorded —
// it is the same visit (a refresh, a double-click, a link preview).
// 30 minutes is long enough to collapse accidental repeats and short
// enough that two genuine visits the same afternoon both count. The
// window check is a read-then-insert (no partial index can express a
// moving window), so two exactly-simultaneous clicks could both land;
// the consequence is one extra click row, never extra money.
const ClickDedupWindow = 30 * time.Minute

// Refusals the unique indexes and the state machines give, folded out
// of the raw driver error or raised directly so callers can answer 409
// for a state conflict and 500 for anything else without knowing what
// a SQLSTATE is. Each has a matching Is… question where a raw unique
// violation is an alternative spelling; both mirror the reseller
// precedents.
var (
	// ErrAffiliateEmailTaken is the unique index on
	// affiliates.contact_email refusing a second affiliate for one
	// address.
	ErrAffiliateEmailTaken = errors.New("affiliate contact email already exists")

	// ErrReferralCodeTaken is the unique index on referral_codes.code
	// refusing a second row for one folded code.
	ErrReferralCodeTaken = errors.New("referral code already exists")

	// ErrAffiliateHasConversions is DeleteAffiliate's refusal: the
	// account has earned commissions, and those conversions are
	// commercial records (who earned what) the payout history reads.
	// Deleting the affiliate must not silently erase them, so the admin
	// suspends instead. Backed by ON DELETE RESTRICT.
	ErrAffiliateHasConversions = errors.New("affiliate has conversion records")

	// ErrAffiliateHasPayouts is DeleteAffiliate's refusal: the account
	// has payout rows, and those are records of money that moved (or
	// was requested). Same policy as conversions — suspend, do not
	// delete. Backed by ON DELETE RESTRICT.
	ErrAffiliateHasPayouts = errors.New("affiliate has payout records")

	// ErrReferralCodeHasConversions is DeleteReferralCode's refusal: the
	// code earned commissions and is the handle those conversions point
	// at. Deactivate it (active=false) instead of deleting history.
	// Backed by ON DELETE RESTRICT.
	ErrReferralCodeHasConversions = errors.New("referral code has conversion records")

	// ErrAffiliateNotActive is RecordConversion's guard: the affiliate
	// is suspended (or gone), and a suspended affiliate never converts.
	ErrAffiliateNotActive = errors.New("affiliate is not active")

	// ErrReferralCodeInactive is RecordConversion's guard: the code is
	// deactivated (or gone). An inactive code stops converting without
	// losing its history.
	ErrReferralCodeInactive = errors.New("referral code is not active")

	// ErrConversionInvalidTransition is SetConversionStatus refusing a
	// move the review state machine does not allow (see
	// model.ConversionTransitionOK) — approving an already-paid
	// conversion, un-rejecting, anything out of a terminal state.
	ErrConversionInvalidTransition = errors.New("conversion status transition not allowed")

	// ErrConversionInPayout is SetConversionStatus refusing to touch a
	// conversion that a still-requested payout has claimed. The payout
	// settled an exact set of conversions for an exact sum; letting a
	// review action change one of them underneath it would make the
	// payout pay a different sum than it records. Fail the payout first
	// (which returns the conversions to the pool), then review.
	ErrConversionInPayout = errors.New("conversion is claimed by a pending payout")

	// ErrPayoutInvalidTransition is the payout state machine refusing a
	// move: only requested moves, and it moves exactly once (see
	// model.PayoutTransitionOK).
	ErrPayoutInvalidTransition = errors.New("payout status transition not allowed")

	// ErrPayoutNothingToPay is CreatePayout's refusal: the affiliate has
	// no accrued (unpaid, unclaimed) commission for this payout to
	// settle. Paying zero would be a payout row that says nothing
	// happened.
	ErrPayoutNothingToPay = errors.New("no accrued commission to pay out")
)

// IsAffiliateEmailConflict reports whether err is the email-taken
// refusal, in either spelling: the sentinel the write paths return, or
// a raw unique violation that reached the caller without passing
// through them. Mirrors IsResellerEmailConflict.
func IsAffiliateEmailConflict(err error) bool {
	return errors.Is(err, ErrAffiliateEmailTaken) || isUniqueViolation(err)
}

// IsReferralCodeConflict reports whether err is the code-taken
// refusal, in either spelling (sentinel or raw unique violation).
func IsReferralCodeConflict(err error) bool {
	return errors.Is(err, ErrReferralCodeTaken) || isUniqueViolation(err)
}

// CreateAffiliate writes a new affiliate account. The id is allocated
// here when the caller left it empty. The contact address is folded
// (see model.NormalizeAffiliateEmail) so the row is stored canonical
// whatever the caller passed — the store must not trust its caller to
// have folded it. A duplicate contact_email is folded into
// ErrAffiliateEmailTaken so the handler can answer 409 for the one
// conflict an admin can act on and 500 for the rest.
func (s *Store) CreateAffiliate(ctx context.Context, a *model.Affiliate) error {
	if a.ID == "" {
		a.ID = newID()
	}
	a.ContactEmail = model.NormalizeAffiliateEmail(a.ContactEmail)
	_, err := s.DB.NewInsert().Model(a).Exec(ctx)
	if isUniqueViolation(err) {
		return ErrAffiliateEmailTaken
	}
	return err
}

// FindAffiliateByID returns one affiliate by primary key. A miss is
// sql.ErrNoRows so the caller can say 404 rather than 500.
func (s *Store) FindAffiliateByID(ctx context.Context, id string) (*model.Affiliate, error) {
	a := new(model.Affiliate)
	return a, s.DB.NewSelect().Model(a).Where("id = ?", id).Scan(ctx)
}

// FindAffiliateByEmail looks an affiliate up by contact address. The
// query folds the address first (see model.NormalizeAffiliateEmail),
// matching how it was stored, so the lookup cannot miss because of
// case or padding.
func (s *Store) FindAffiliateByEmail(ctx context.Context, email string) (*model.Affiliate, error) {
	a := new(model.Affiliate)
	return a, s.DB.NewSelect().Model(a).
		Where("contact_email = ?", model.NormalizeAffiliateEmail(email)).
		Scan(ctx)
}

// ListAffiliates is the admin listing: one page of affiliates plus how
// many the filter matched. search covers what an admin actually types
// — a name or a contact address — and status narrows to one lifecycle
// value. Ordered by name with id as the tiebreaker so equal names
// cannot swap between two pages of one listing.
func (s *Store) ListAffiliates(ctx context.Context, search, status string, p Page) ([]*model.Affiliate, int, error) {
	var out []*model.Affiliate
	q := s.DB.NewSelect().Model(&out).
		OrderExpr("name ASC, id ASC")
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if search != "" {
		q = q.Where("name ILIKE ? OR contact_email ILIKE ?", "%"+search+"%", "%"+search+"%")
	}
	total, err := scanPage(ctx, q, p)
	if err != nil {
		return nil, 0, err
	}
	if p.Limit <= 0 {
		total = len(out)
	}
	return out, total, nil
}

// UpdateAffiliate writes back a whole affiliate row (the handler merged
// the patch into what it read first). The contact address is folded on
// the way in, exactly as on create, so a renamed affiliate cannot take
// on a second spelling of an address. A rename onto a taken email is
// the same ErrAffiliateEmailTaken the create path answers.
func (s *Store) UpdateAffiliate(ctx context.Context, a *model.Affiliate) error {
	a.ContactEmail = model.NormalizeAffiliateEmail(a.ContactEmail)
	a.UpdatedAt = time.Now()
	_, err := s.DB.NewUpdate().Model(a).WherePK().Exec(ctx)
	if isUniqueViolation(err) {
		return ErrAffiliateEmailTaken
	}
	return err
}

// DeleteAffiliate removes the account — but only while it has neither
// payouts nor conversions. Both are money records the reconciliation
// of the program reads, so this refuses with the typed sentinel rather
// than silently dropping them (the documented choice over cascading;
// the migration backs the check with ON DELETE RESTRICT). Payouts are
// checked first: a payout is money that moved, the strongest reason
// an account cannot vanish. Codes and clicks are NOT a reason to
// refuse — they cascade with the account, because an account that only
// ever collected clicks is safe to remove.
//
// A no-op delete answers sql.ErrNoRows so the caller can say 404 for a
// row that was never there rather than claim a deletion that did not
// happen.
func (s *Store) DeleteAffiliate(ctx context.Context, id string) error {
	n, err := s.DB.NewSelect().Model((*model.AffiliatePayout)(nil)).
		Where("affiliate_id = ?", id).Count(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrAffiliateHasPayouts
	}
	n, err = s.DB.NewSelect().Model((*model.AffiliateConversion)(nil)).
		Where("affiliate_id = ?", id).Count(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrAffiliateHasConversions
	}
	res, err := s.DB.NewDelete().Model((*model.Affiliate)(nil)).
		Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// CreateReferralCode writes a new referral code for an affiliate. The
// id is allocated here when empty, and the code is folded (see
// model.NormalizeReferralCode) so the row is stored canonical whatever
// the caller passed. The affiliate is checked first so a missing one
// is sql.ErrNoRows (404) rather than a raw foreign-key violation; a
// duplicate code folds into ErrReferralCodeTaken (409).
func (s *Store) CreateReferralCode(ctx context.Context, rc *model.ReferralCode) error {
	n, err := s.DB.NewSelect().Model((*model.Affiliate)(nil)).
		Where("id = ?", rc.AffiliateID).Count(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	if rc.ID == "" {
		rc.ID = newID()
	}
	rc.Code = model.NormalizeReferralCode(rc.Code)
	_, err = s.DB.NewInsert().Model(rc).Exec(ctx)
	if isUniqueViolation(err) {
		return ErrReferralCodeTaken
	}
	return err
}

// FindReferralCodeByID returns one code row by primary key. A miss is
// sql.ErrNoRows.
func (s *Store) FindReferralCodeByID(ctx context.Context, id string) (*model.ReferralCode, error) {
	rc := new(model.ReferralCode)
	return rc, s.DB.NewSelect().Model(rc).Where("id = ?", id).Scan(ctx)
}

// FindReferralCodeByCode resolves the handle the public /r/<code>
// redirect and the convert helper speak. The query folds the code
// first (see model.NormalizeReferralCode), matching how it was stored,
// so any spelling of the handle finds its row. A miss is
// sql.ErrNoRows — the callers turn that into a silent default redirect
// or a 400, never a 404 oracle.
func (s *Store) FindReferralCodeByCode(ctx context.Context, code string) (*model.ReferralCode, error) {
	rc := new(model.ReferralCode)
	return rc, s.DB.NewSelect().Model(rc).
		Where("code = ?", model.NormalizeReferralCode(code)).
		Scan(ctx)
}

// ListReferralCodes is one page of an affiliate's codes plus how many
// it has in total, ordered by the code itself (a name-like handle) with
// id as the tiebreaker.
func (s *Store) ListReferralCodes(ctx context.Context, affiliateID string, p Page) ([]*model.ReferralCode, int, error) {
	var out []*model.ReferralCode
	q := s.DB.NewSelect().Model(&out).
		Where("affiliate_id = ?", affiliateID).
		OrderExpr("code ASC, id ASC")
	total, err := scanPage(ctx, q, p)
	if err != nil {
		return nil, 0, err
	}
	if p.Limit <= 0 {
		total = len(out)
	}
	return out, total, nil
}

// UpdateReferralCode writes back the editable halves of a code row —
// the landing URL and the active flag. The code handle itself and the
// owning affiliate are immutable: renaming a code would orphan every
// link already shared, and moving it between affiliates would rewrite
// who earned the clicks. Those are create-a-new-code decisions.
func (s *Store) UpdateReferralCode(ctx context.Context, rc *model.ReferralCode) error {
	_, err := s.DB.NewUpdate().Model(rc).
		Column("landing_url", "active").
		WherePK().Exec(ctx)
	return err
}

// DeleteReferralCode removes one code of one affiliate (both ids from
// the route, so an admin cannot delete another affiliate's code through
// a mismatched path). It refuses with ErrReferralCodeHasConversions
// while conversions point at the code — those are money records whose
// handle must stay readable; deactivate the code instead. Clicks
// cascade (they mean nothing without their code).
//
// A no-op delete answers sql.ErrNoRows.
func (s *Store) DeleteReferralCode(ctx context.Context, affiliateID, codeID string) error {
	n, err := s.DB.NewSelect().Model((*model.AffiliateConversion)(nil)).
		Where("code_id = ?", codeID).Count(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrReferralCodeHasConversions
	}
	res, err := s.DB.NewDelete().Model((*model.ReferralCode)(nil)).
		Where("id = ? AND affiliate_id = ?", codeID, affiliateID).Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RecordClick stores one visit through a referral code.
//
// Dedup (fraud control): if the same code already has a click from the
// same ip_hash within ClickDedupWindow, nothing is written and created
// is false — one visit, one row, however many refreshes. The check
// compares the hash the caller computed (see model.HashReferralIP); the
// store never sees, wants or stores a raw address.
//
// The window is measured against the click being recorded, not against
// "now", so a caller can backdate a click without breaking the rule.
func (s *Store) RecordClick(ctx context.Context, cl *model.ReferralClick) (bool, error) {
	if cl.ID == "" {
		cl.ID = newID()
	}
	if cl.ClickedAt.IsZero() {
		cl.ClickedAt = time.Now()
	}
	cutoff := cl.ClickedAt.Add(-ClickDedupWindow)
	n, err := s.DB.NewSelect().Model((*model.ReferralClick)(nil)).
		Where("code_id = ? AND ip_hash = ? AND clicked_at >= ?", cl.CodeID, cl.IPHash, cutoff).
		Count(ctx)
	if err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	if _, err := s.DB.NewInsert().Model(cl).Exec(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// RecordConversion records one attributed sale, idempotently.
//
// Idempotency (fraud control): the UNIQUE index on order_id makes one
// order convert at most once. When the insert loses that race — or a
// checkout simply retries — the row that is already there is returned
// with created=false, so a double submit can never double a
// commission. The caller gets the authoritative row either way.
//
// Guards (fraud control): the affiliate must be active and the code
// must be active — a suspended affiliate never converts, and a
// deactivated code stops converting while keeping its history. Both
// are re-checked here even though the handler checks first, because
// this is the layer that writes the money.
//
// The row is stored as given: the caller resolved the affiliate and
// code together and computed commission_minor under the affiliate's
// model (model.Affiliate.CommissionFor). The store validates none of
// the arithmetic — the amount is decided at exactly one place and is
// visible there.
func (s *Store) RecordConversion(ctx context.Context, conv *model.AffiliateConversion) (*model.AffiliateConversion, bool, error) {
	aff := new(model.Affiliate)
	if err := s.DB.NewSelect().Model(aff).Where("id = ?", conv.AffiliateID).Scan(ctx); err != nil {
		return nil, false, err
	}
	if aff.Status != model.AffiliateStatusActive {
		return nil, false, ErrAffiliateNotActive
	}
	rc := new(model.ReferralCode)
	if err := s.DB.NewSelect().Model(rc).Where("id = ?", conv.CodeID).Scan(ctx); err != nil {
		return nil, false, err
	}
	if !rc.Active {
		return nil, false, ErrReferralCodeInactive
	}

	if conv.ID == "" {
		conv.ID = newID()
	}
	if conv.Status == "" {
		conv.Status = model.AffiliateConversionStatusPending
	}
	now := time.Now()
	conv.CreatedAt = now
	conv.UpdatedAt = now

	_, err := s.DB.NewInsert().Model(conv).Exec(ctx)
	if err != nil {
		if isUniqueViolation(err) {
			existing, ferr := s.FindConversionByOrderID(ctx, conv.OrderID)
			if ferr == nil {
				return existing, false, nil
			}
			// The lookup lost the race too; surface the original
			// refusal rather than the secondary miss.
		}
		return nil, false, err
	}
	return conv, true, nil
}

// FindConversionByID returns one conversion by primary key. A miss is
// sql.ErrNoRows.
func (s *Store) FindConversionByID(ctx context.Context, id string) (*model.AffiliateConversion, error) {
	conv := new(model.AffiliateConversion)
	return conv, s.DB.NewSelect().Model(conv).Where("id = ?", id).Scan(ctx)
}

// FindConversionByOrderID is the idempotency lookup behind
// RecordConversion: the row an order already converted into, if any.
// A miss is sql.ErrNoRows.
func (s *Store) FindConversionByOrderID(ctx context.Context, orderID string) (*model.AffiliateConversion, error) {
	conv := new(model.AffiliateConversion)
	return conv, s.DB.NewSelect().Model(conv).Where("order_id = ?", orderID).Scan(ctx)
}

// ListConversions is one page of an affiliate's conversions plus how
// many the filter matched, newest first (created_at, then id) so the
// order is total and pages line up. status narrows to one lifecycle
// value; empty means all of them.
func (s *Store) ListConversions(ctx context.Context, affiliateID, status string, p Page) ([]*model.AffiliateConversion, int, error) {
	var out []*model.AffiliateConversion
	q := s.DB.NewSelect().Model(&out).
		Where("affiliate_id = ?", affiliateID).
		OrderExpr("created_at DESC, id DESC")
	if status != "" {
		q = q.Where("status = ?", status)
	}
	total, err := scanPage(ctx, q, p)
	if err != nil {
		return nil, 0, err
	}
	if p.Limit <= 0 {
		total = len(out)
	}
	return out, total, nil
}

// SetConversionStatus moves one conversion through the review state
// machine (see model.ConversionTransitionOK) and returns the row as it
// now stands.
//
// A move the matrix does not allow is ErrConversionInvalidTransition
// (an operator cannot approve an already-paid conversion or un-reject
// one). A conversion claimed by a still-requested payout is frozen —
// ErrConversionInPayout — because the payout settled an exact set for
// an exact sum; fail the payout first and the conversion returns to
// the pool for review. A conversion already settled by a paid payout
// can still be reversed (the clawback records a loss on money that
// already moved).
//
// The paid state is NOT reachable here: it is applied in bulk by
// MarkPayoutPaid when the money moves.
func (s *Store) SetConversionStatus(ctx context.Context, id, status string) (*model.AffiliateConversion, error) {
	conv := new(model.AffiliateConversion)
	if err := s.DB.NewSelect().Model(conv).Where("id = ?", id).Scan(ctx); err != nil {
		return nil, err
	}
	if !model.ConversionTransitionOK(conv.Status, status) {
		return nil, ErrConversionInvalidTransition
	}
	if conv.PayoutID != "" {
		pay := new(model.AffiliatePayout)
		err := s.DB.NewSelect().Model(pay).Where("id = ?", conv.PayoutID).Scan(ctx)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if err == nil && pay.Status == model.AffiliatePayoutStatusRequested {
			return nil, ErrConversionInPayout
		}
	}
	conv.Status = status
	conv.UpdatedAt = time.Now()
	if _, err := s.DB.NewUpdate().Model(conv).
		Column("status", "updated_at").WherePK().Exec(ctx); err != nil {
		return nil, err
	}
	return conv, nil
}

// SumPendingCommissions is the accrued, unpaid balance of an affiliate:
// the sum of commissions on conversions that are still pending or
// approved and not yet claimed by a payout. That is exactly the money a
// payout may settle, which is what the admin create-payout call checks
// its amount against. Rejected and reversed conversions earn nothing
// and are excluded; paid ones are already out.
func (s *Store) SumPendingCommissions(ctx context.Context, affiliateID string) (int64, error) {
	var total int64
	err := s.DB.NewSelect().Model((*model.AffiliateConversion)(nil)).
		ColumnExpr("COALESCE(SUM(commission_minor), 0)").
		Where("affiliate_id = ? AND payout_id IS NULL AND status IN (?, ?)",
			affiliateID,
			model.AffiliateConversionStatusPending,
			model.AffiliateConversionStatusApproved).
		Scan(ctx, &total)
	return total, err
}

// CreatePayout records a payout request and claims the conversions it
// settles, in one transaction.
//
// Claiming is greedy and whole: the affiliate's accrued conversions
// (pending or approved, unclaimed) are walked oldest-first, and each is
// claimed while the running total stays within pay.AmountMinor —
// stopping at the first conversion that would overshoot. A conversion
// is settled whole or not at all, so pay.AmountMinor is then set to
// the claimed sum: the row records the money that will actually move,
// which may be a little less than the operator asked for. A
// pay.AmountMinor of 0 or less means "the whole accrued balance".
//
// Nothing claimable is ErrPayoutNothingToPay. Concurrent CreatePayout
// calls serialize on the row locks (SELECT … FOR UPDATE), so two
// payouts can never claim the same conversion twice.
func (s *Store) CreatePayout(ctx context.Context, pay *model.AffiliatePayout) error {
	if pay.ID == "" {
		pay.ID = newID()
	}
	pay.Status = model.AffiliatePayoutStatusRequested
	return s.RunInTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		var eligible []*model.AffiliateConversion
		err := tx.NewSelect().Model(&eligible).
			Where("affiliate_id = ? AND payout_id IS NULL AND status IN (?, ?)",
				pay.AffiliateID,
				model.AffiliateConversionStatusPending,
				model.AffiliateConversionStatusApproved).
			OrderExpr("created_at ASC, id ASC").
			For("UPDATE").
			Scan(ctx)
		if err != nil {
			return err
		}

		limit := pay.AmountMinor
		var claimedSum int64
		var claimedIDs []string
		for _, conv := range eligible {
			if limit > 0 && claimedSum+conv.CommissionMinor > limit {
				break // oldest-first prefix: stop at the first overshoot
			}
			claimedSum += conv.CommissionMinor
			claimedIDs = append(claimedIDs, conv.ID)
		}
		if claimedSum == 0 {
			return ErrPayoutNothingToPay
		}
		pay.AmountMinor = claimedSum

		pay.CreatedAt = time.Now()
		if _, err := tx.NewInsert().Model(pay).Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.NewUpdate().Model((*model.AffiliateConversion)(nil)).
			Set("payout_id = ?, updated_at = ?", pay.ID, time.Now()).
			Where("id IN (?)", bun.In(claimedIDs)).
			Exec(ctx); err != nil {
			return err
		}
		return nil
	})
}

// MarkPayoutPaid settles a payout: the money has moved. The payout
// becomes paid and is stamped paid_at, and every conversion it claimed
// becomes paid with it (pending and approved claimed rows — the exact
// set the payout's amount is the sum of).
//
// Only a requested payout can pay (model.PayoutTransitionOK): paying a
// paid payout again, or a failed one, is ErrPayoutInvalidTransition —
// what happened to the money is recorded once and never rewritten.
func (s *Store) MarkPayoutPaid(ctx context.Context, id string) (*model.AffiliatePayout, error) {
	return s.finishPayout(ctx, id, model.AffiliatePayoutStatusPaid, "")
}

// MarkPayoutFailed records that a requested payout did not happen. The
// payout becomes failed (notes kept if given), and the conversions it
// claimed are returned to the accrued pool (payout_id cleared) so a
// later payout can settle them — a failed transfer owes the money
// again, it does not erase it.
//
// Only a requested payout can fail. Paid payouts reverse per
// conversion (the clawback is about specific sales), never as a payout.
func (s *Store) MarkPayoutFailed(ctx context.Context, id, notes string) (*model.AffiliatePayout, error) {
	return s.finishPayout(ctx, id, model.AffiliatePayoutStatusFailed, notes)
}

// finishPayout is the shared half of MarkPayoutPaid / MarkPayoutFailed:
// validate the transition, write the terminal state, and settle or
// release the claimed conversions — all in one transaction so the
// payout row and its conversions can never disagree.
func (s *Store) finishPayout(ctx context.Context, id, to, notes string) (*model.AffiliatePayout, error) {
	pay := new(model.AffiliatePayout)
	err := s.RunInTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if err := tx.NewSelect().Model(pay).Where("id = ?", id).For("UPDATE").Scan(ctx); err != nil {
			return err
		}
		if !model.PayoutTransitionOK(pay.Status, to) {
			return ErrPayoutInvalidTransition
		}
		now := time.Now()
		pay.Status = to
		if notes != "" {
			pay.Notes = notes
		}
		cols := []string{"status"}
		if to == model.AffiliatePayoutStatusPaid {
			pay.PaidAt = &now
			cols = append(cols, "paid_at", "notes")
		} else {
			cols = append(cols, "notes")
		}
		if _, err := tx.NewUpdate().Model(pay).Column(cols...).WherePK().Exec(ctx); err != nil {
			return err
		}
		if to == model.AffiliatePayoutStatusPaid {
			_, err := tx.NewUpdate().Model((*model.AffiliateConversion)(nil)).
				Set("status = ?, updated_at = ?", model.AffiliateConversionStatusPaid, now).
				Where("payout_id = ? AND status IN (?, ?)",
					id,
					model.AffiliateConversionStatusPending,
					model.AffiliateConversionStatusApproved).
				Exec(ctx)
			return err
		}
		// Failed: the claimed conversions go back to the accrued pool,
		// still pending or approved (nothing else could have been
		// claimed, and the freeze kept them unchanged while the payout
		// was open).
		_, err := tx.NewUpdate().Model((*model.AffiliateConversion)(nil)).
			Set("payout_id = NULL, updated_at = ?", now).
			Where("payout_id = ?", id).
			Exec(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	return pay, nil
}

// ListAffiliatePayouts is one page of an affiliate's payouts plus how
// many it has in total, newest first (created_at, then id) so the order
// is total and pages line up.
func (s *Store) ListAffiliatePayouts(ctx context.Context, affiliateID string, p Page) ([]*model.AffiliatePayout, int, error) {
	var out []*model.AffiliatePayout
	q := s.DB.NewSelect().Model(&out).
		Where("affiliate_id = ?", affiliateID).
		OrderExpr("created_at DESC, id DESC")
	total, err := scanPage(ctx, q, p)
	if err != nil {
		return nil, 0, err
	}
	if p.Limit <= 0 {
		total = len(out)
	}
	return out, total, nil
}
