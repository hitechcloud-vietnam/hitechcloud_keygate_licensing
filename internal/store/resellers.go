package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Resellers (plan §31 / Phase 7) ───
//
// Reseller accounts and the record of which licences each one owns
// (has sold). Slice 1 is account + allocation bookkeeping only;
// wholesale pricing, commissions and the reseller-facing API grow on
// top of these rows later. Money here is basis points and integer
// minor units, never floats — see model.Reseller.

// Refusals the unique indexes give, folded out of the raw driver error
// so callers can answer 409 for a state conflict and 500 for anything
// else without knowing what a SQLSTATE is. Each has a matching Is…
// question; both mirror the category/tax precedents.

var (
	// ErrResellerEmailTaken is the unique index on resellers.contact_email
	// refusing a second reseller for one address.
	ErrResellerEmailTaken = errors.New("reseller contact email already exists")

	// ErrResellerHasAllocations is DeleteReseller's refusal: the account
	// still owns licences, and those allocations are commercial records
	// (who sold what) that the commission slice will read. Deleting the
	// reseller must not silently erase them, so the admin deallocates —
	// or suspends — first. See model.ResellerLicense for the 1:1 rule
	// that makes an allocation meaningful.
	ErrResellerHasAllocations = errors.New("reseller has allocated licenses")

	// ErrResellerHasCommissions is DeleteReseller's second refusal: a
	// reseller with commission ledger rows is financial history and must
	// be kept (settle or cancel the rows first, then delete). The
	// migration's ON DELETE RESTRICT on commissions is the backstop.
	ErrResellerHasCommissions = errors.New("reseller has commission records")

	// ErrAllocationLicenseNotFound is AllocateLicense refusing a
	// licence_id that names no licence. The caller sent an id we cannot
	// allocate; that is a bad request, not a missing reseller.
	ErrAllocationLicenseNotFound = errors.New("license not found")

	// ErrLicenseAlreadyAllocated is AllocateLicense refusing a licence
	// that already has an owner. Raised by the unique index on
	// reseller_licenses.license_id (one reseller per licence) and by the
	// (reseller_id, license_id) primary key on a repeat to the same
	// reseller — both are "already allocated" to the admin.
	ErrLicenseAlreadyAllocated = errors.New("license is already allocated to a reseller")
)

// IsResellerEmailConflict reports whether err is the email-taken
// refusal, in either spelling: the sentinel the write paths return, or
// a raw unique violation that reached the caller without passing
// through them. Mirrors IsCategorySlugConflict.
func IsResellerEmailConflict(err error) bool {
	return errors.Is(err, ErrResellerEmailTaken) || isUniqueViolation(err)
}

// IsResellerLicenseConflict reports whether err is the already-allocated
// refusal, in either spelling (sentinel or raw unique violation). A
// repeat allocation and a cross-reseller claim both land here.
func IsResellerLicenseConflict(err error) bool {
	return errors.Is(err, ErrLicenseAlreadyAllocated) || isUniqueViolation(err)
}

// CreateReseller writes a new reseller account. The id is allocated
// here when the caller left it empty. The contact address is folded
// (see model.NormalizeResellerEmail) so the row is stored canonical
// whatever the caller passed — the unique index and FindResellerByEmail
// both depend on there being exactly one spelling per address, and the
// store must not trust its caller to have folded it. A duplicate
// contact_email is folded into ErrResellerEmailTaken so the handler can
// answer 409 for the one conflict an admin can act on and 500 for the
// rest.
func (s *Store) CreateReseller(ctx context.Context, r *model.Reseller) error {
	if r.ID == "" {
		r.ID = newID()
	}
	r.ContactEmail = model.NormalizeResellerEmail(r.ContactEmail)
	_, err := s.DB.NewInsert().Model(r).Exec(ctx)
	if isUniqueViolation(err) {
		return ErrResellerEmailTaken
	}
	return err
}

// FindResellerByID returns one reseller by primary key. A miss is
// sql.ErrNoRows so the caller can say 404 rather than 500.
func (s *Store) FindResellerByID(ctx context.Context, id string) (*model.Reseller, error) {
	r := new(model.Reseller)
	return r, s.DB.NewSelect().Model(r).Where("id = ?", id).Scan(ctx)
}

// FindResellerByEmail looks a reseller up by contact address. The
// query folds the address first (see model.NormalizeResellerEmail),
// matching how it was stored, so the lookup cannot miss because of
// case or padding.
func (s *Store) FindResellerByEmail(ctx context.Context, email string) (*model.Reseller, error) {
	r := new(model.Reseller)
	return r, s.DB.NewSelect().Model(r).
		Where("contact_email = ?", model.NormalizeResellerEmail(email)).
		Scan(ctx)
}

// ListResellers is the admin listing: one page of resellers plus how
// many the filter matched. search covers what an admin actually types
// — a name or a contact address — and status narrows to one lifecycle
// value. Ordered by name with id as the tiebreaker so equal names
// cannot swap between two pages of one listing.
func (s *Store) ListResellers(ctx context.Context, search, status string, p Page) ([]*model.Reseller, int, error) {
	var out []*model.Reseller
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

// UpdateReseller writes back a whole reseller row (the handler merged
// the patch into what it read first). The contact address is folded on
// the way in, exactly as on create, so a renamed reseller cannot take
// on a second spelling of an address. A rename onto a taken email is
// the same ErrResellerEmailTaken the create path answers, so both
// doors report the conflict as 409.
func (s *Store) UpdateReseller(ctx context.Context, r *model.Reseller) error {
	r.ContactEmail = model.NormalizeResellerEmail(r.ContactEmail)
	r.UpdatedAt = time.Now()
	_, err := s.DB.NewUpdate().Model(r).WherePK().Exec(ctx)
	if isUniqueViolation(err) {
		return ErrResellerEmailTaken
	}
	return err
}

// DeleteReseller removes the account — but only while it owns no
// licences and no commission ledger rows. The allocations are
// commercial records the commission slice reads, and the commission
// rows are financial history, so this refuses with
// ErrResellerHasAllocations / ErrResellerHasCommissions rather than
// silently dropping them (the documented choice over cascading; see
// model.ResellerLicense). The migration backs both checks with
// ON DELETE RESTRICT, so a bypassed check still cannot orphan the
// records.
//
// A no-op delete answers sql.ErrNoRows so the caller can say 404 for a
// row that was never there rather than claim a deletion that did not
// happen.
func (s *Store) DeleteReseller(ctx context.Context, id string) error {
	n, err := s.CountResellerLicenses(ctx, id)
	if err != nil {
		return err
	}
	if n > 0 {
		return ErrResellerHasAllocations
	}
	cn, err := s.CountResellerCommissions(ctx, id)
	if err != nil {
		return err
	}
	if cn > 0 {
		return ErrResellerHasCommissions
	}
	res, err := s.DB.NewDelete().Model((*model.Reseller)(nil)).
		Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// CountResellerLicenses is how many licences a reseller currently
// owns. The Get endpoint reports it beside the account; DeleteReseller
// uses it as the guard above.
func (s *Store) CountResellerLicenses(ctx context.Context, resellerID string) (int, error) {
	return s.DB.NewSelect().Model((*model.ResellerLicense)(nil)).
		Where("reseller_id = ?", resellerID).Count(ctx)
}

// CountResellerCommissions is how many commission ledger rows name a
// reseller. DeleteReseller uses it as the ledger guard above.
func (s *Store) CountResellerCommissions(ctx context.Context, resellerID string) (int, error) {
	return s.DB.NewSelect().Model((*model.Commission)(nil)).
		Where("reseller_id = ?", resellerID).Count(ctx)
}

// AllocateLicense records that a reseller owns a licence.
//
// Both sides are checked before the insert so the refusals are the
// typed ones a handler can map, not raw foreign-key violations: a
// missing reseller is sql.ErrNoRows (404), a missing licence is
// ErrAllocationLicenseNotFound (400). An allocation that already
// exists — a repeat to this reseller (primary key) or a claim on a
// licence another reseller owns (the unique license_id) — is
// ErrLicenseAlreadyAllocated (409).
func (s *Store) AllocateLicense(ctx context.Context, resellerID, licenseID string) error {
	n, err := s.DB.NewSelect().Model((*model.Reseller)(nil)).
		Where("id = ?", resellerID).Count(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	n, err = s.DB.NewSelect().Model((*model.License)(nil)).
		Where("id = ?", licenseID).Count(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrAllocationLicenseNotFound
	}
	if _, err := s.DB.NewInsert().Model(&model.ResellerLicense{
		ResellerID: resellerID,
		LicenseID:  licenseID,
	}).Exec(ctx); err != nil {
		if isUniqueViolation(err) {
			return ErrLicenseAlreadyAllocated
		}
		return err
	}
	return nil
}

// DeallocateLicense drops the allocation on a licence. It is keyed on
// the licence alone, not the (reseller, licence) pair: a licence is
// owned by at most one reseller (the unique license_id), so the licence
// id names exactly one allocation row and the removal is unambiguous —
// the route's reseller id is not needed to locate it (see
// model.ResellerLicense).
//
// A licence with no allocation is a no-op that answers sql.ErrNoRows,
// so the caller says 404 for a deallocation that was not there rather
// than 204 for nothing.
func (s *Store) DeallocateLicense(ctx context.Context, licenseID string) error {
	res, err := s.DB.NewDelete().Model((*model.ResellerLicense)(nil)).
		Where("license_id = ?", licenseID).Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// ListResellerLicenses is the allocation list: one page of the licences
// a reseller owns, as licence rows, plus how many the reseller has in
// total.
//
// The join is written against the bun model alias (see
// bun_alias_test.go): the outer model is License, so it is correlated
// as "license", never as "licenses". The allocation table is a plain
// join target with its own alias (rl); its columns are the real table's
// and are not subject to bun's model aliasing. Newest allocation first
// (created_at, then id) so the order is total and pages line up.
func (s *Store) ListResellerLicenses(ctx context.Context, resellerID string, p Page) ([]*model.License, int, error) {
	var out []*model.License
	q := s.DB.NewSelect().Model(&out).
		Join(`JOIN reseller_licenses rl ON rl.license_id = "license".id`).
		Where("rl.reseller_id = ?", resellerID).
		OrderExpr(`"license".created_at DESC, "license".id DESC`)
	total, err := scanPage(ctx, q, p)
	if err != nil {
		return nil, 0, err
	}
	if p.Limit <= 0 {
		total = len(out)
	}
	return out, total, nil
}
