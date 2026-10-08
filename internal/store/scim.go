package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── SCIM 2.0 provisioning identities (Phase 8, slice 2) ───
//
// scim_identities is the join between a SCIM /Users resource and the
// platform user it provisions. See the migration for the full rationale.
//
// The model lives HERE rather than in internal/model because it is a
// bookkeeping row owned entirely by this slice — the SCIM endpoints are
// its only reader and writer, and it is not part of the domain model the
// rest of the platform reasons about. This mirrors RefreshToken and
// StripeCancelStateTarget, which are likewise store-local types.
//
// Every column is pinned with an explicit `column:` tag. The names here
// lead with an all-caps acronym (ExternalID, UserID) — exactly the case
// where Bun's snake_case inflection can silently produce the wrong column
// (the SAMLSSOURL lesson in model/sso.go). Pinning keeps the model, the
// migration and the hand-written JOIN in lockstep.

// SCIMIdentity is one provisioning row: "this platform user is managed by
// a directory, here is its externalId and its SCIM active flag".
type SCIMIdentity struct {
	bun.BaseModel `bun:"table:scim_identities"`

	// ID is the SCIM resource id — the opaque handle the provisioning
	// client gets back from POST /Users and later sends to GET/PUT/PATCH/
	// DELETE. It is NOT the platform user id: the SCIM identifier must
	// stay stable across a re-link, and keeping the two apart means a
	// caller can never mistake a user id for a SCIM id.
	ID string `bun:"column:id,pk" json:"id"`
	// ExternalID is the IdP's own identifier. Nullable; omitted from
	// JSON when absent.
	ExternalID *string `bun:"column:external_id" json:"external_id,omitempty"`
	// UserID is the provisioned platform user. UNIQUE — one identity per
	// user (see the migration).
	UserID string `bun:"column:user_id,notnull,unique" json:"user_id"`
	// Active mirrors the SCIM "active" attribute. false means the account
	// was deactivated (SCIM DELETE); the SSO login path refuses it.
	Active bool `bun:"column:active,notnull,default:true" json:"active"`

	CreatedAt time.Time `bun:"column:created_at,nullzero,default:now()" json:"created_at"`
	UpdatedAt time.Time `bun:"column:updated_at,nullzero,default:now()" json:"updated_at"`
}

// SCIMUserRow is the projection a SCIM /Users response is built from: the
// identity joined to the user fields the SCIM payload needs (userName =
// email, name.givenName/familyName derived from the display name).
//
// The query spans two tables, so it is written with EXPLICIT aliases (si /
// u) and explicit column AS names rather than through a bun model — the
// implicit model alias only covers one table and is the "missing
// FROM-clause entry" trap (see bun_alias_test.go). The bun column tags
// here pin the AS names the query selects.
type SCIMUserRow struct {
	SCIMID     string    `bun:"column:scim_id"`
	ExternalID *string   `bun:"column:external_id"`
	Active     bool      `bun:"column:active"`
	CreatedAt  time.Time `bun:"column:created_at"`
	UpdatedAt  time.Time `bun:"column:updated_at"`
	UserID     string    `bun:"column:user_id"`
	Email      string    `bun:"column:email"`
	Name       string    `bun:"column:name"`
}

// Refusals folded out of the raw driver error so callers can answer 409
// for a state conflict without knowing what a SQLSTATE is.

var (
	// ErrSCIMIdentityExists is the unique index on scim_identities.user_id
	// refusing a second identity for one platform user — the row is
	// already provisioned. The handler answers 409 (SCIM "uniqueness").
	ErrSCIMIdentityExists = errors.New("scim identity already exists for this user")

	// ErrSCIMEmailTaken is the unique index on users.email refusing a SCIM
	// userName (email) change onto an address another account already owns.
	// The handler answers 409 (SCIM "uniqueness").
	ErrSCIMEmailTaken = errors.New("user email already exists")
)

const scimIdentityConstraint = "scim_identities_user_id_key"

// IsSCIMIdentityConflict reports whether err is the already-provisioned
// refusal, in either spelling (the sentinel, or a raw unique violation on
// the user_id constraint that reached the caller). Mirrors
// IsSSONameConflict, narrowed to this table's one unique.
func IsSCIMIdentityConflict(err error) bool {
	if errors.Is(err, ErrSCIMIdentityExists) {
		return true
	}
	return isUniqueViolation(err) && strings.Contains(err.Error(), scimIdentityConstraint)
}

// IsSCIMEmailConflict reports whether err is the email-taken refusal, in
// either spelling (the sentinel, or a raw unique violation on the users
// email index).
func IsSCIMEmailConflict(err error) bool {
	if errors.Is(err, ErrSCIMEmailTaken) {
		return true
	}
	return isUniqueViolation(err) && strings.Contains(err.Error(), "email")
}

// UpdateSCIMUserEmail re-points a platform user at a new address — the
// write behind a SCIM userName change (PATCH/PUT). The address is folded
// exactly like UpsertUser folds it. A change onto an address another
// account owns is ErrSCIMEmailTaken (409), never a silent merge.
func (s *Store) UpdateSCIMUserEmail(ctx context.Context, userID, email string) error {
	_, err := s.DB.NewUpdate().Model((*model.User)(nil)).
		Set("email = ?, updated_at = now()", normalizeEmail(email)).
		Where("id = ?", userID).Exec(ctx)
	if isUniqueViolation(err) {
		return ErrSCIMEmailTaken
	}
	return err
}

// scimIdentityRow is a plain scan target for the joined user query's
// column list. It is unexported because the public shape is SCIMUserRow.
func scimUserColumns() string {
	return "si.id AS scim_id, si.external_id AS external_id, si.active AS active, " +
		"si.created_at AS created_at, si.updated_at AS updated_at, si.user_id AS user_id, " +
		"u.email AS email, u.name AS name"
}

// scimUserQuery is the shared FROM/JOIN half of every SCIM user read. The
// aliases are literal (si / u), never a bun model alias, so this is safe
// against the snake_case inflection trap.
func (s *Store) scimUserQuery() *bun.SelectQuery {
	return s.DB.NewSelect().
		TableExpr("scim_identities AS si").
		ColumnExpr(scimUserColumns()).
		Join("INNER JOIN users AS u ON u.id = si.user_id")
}

// CreateSCIMIdentity writes a new provisioning row. The id is allocated
// here when the caller left it empty. A second row for a user that is
// already provisioned is folded into ErrSCIMIdentityExists so the handler
// can answer 409 for the one conflict a client can act on and 500 for the
// rest.
func (s *Store) CreateSCIMIdentity(ctx context.Context, row *SCIMIdentity) error {
	if row.ID == "" {
		row.ID = newID()
	}
	now := time.Now()
	row.CreatedAt = now
	row.UpdatedAt = now
	_, err := s.DB.NewInsert().Model(row).Exec(ctx)
	if isUniqueViolation(err) {
		return ErrSCIMIdentityExists
	}
	return err
}

// FindSCIMIdentityByID returns one identity by its SCIM id. A miss is
// sql.ErrNoRows so the caller can say 404 rather than 500.
func (s *Store) FindSCIMIdentityByID(ctx context.Context, id string) (*SCIMIdentity, error) {
	row := new(SCIMIdentity)
	return row, s.DB.NewSelect().Model(row).Where("id = ?", id).Scan(ctx)
}

// FindSCIMIdentityByUserID returns the identity that provisions one
// platform user — the lookup the SSO login path does after resolving an
// email to a user, to honour a SCIM deactivation. A miss is
// sql.ErrNoRows: a user with no identity simply is not SCIM-managed, which
// is NOT a refusal (the login gate treats "no identity" as allowed).
func (s *Store) FindSCIMIdentityByUserID(ctx context.Context, userID string) (*SCIMIdentity, error) {
	row := new(SCIMIdentity)
	return row, s.DB.NewSelect().Model(row).Where("user_id = ?", userID).Scan(ctx)
}

// UpdateSCIMIdentity writes back the writable identity fields (external_id
// and active — the two the directory owns). user_id and created_at are
// immutable: re-linking a SCIM resource to a different user is not a thing
// this API does, and the creation instant is history.
func (s *Store) UpdateSCIMIdentity(ctx context.Context, row *SCIMIdentity) error {
	row.UpdatedAt = time.Now()
	_, err := s.DB.NewUpdate().Model(row).
		Column("external_id", "active", "updated_at").
		WherePK().Exec(ctx)
	return err
}

// SetSCIMIdentityActive flips the active flag — the write SCIM DELETE
// (deactivate) and PATCH (reactivate / deactivate) do. A missing row is
// sql.ErrNoRows (404).
func (s *Store) SetSCIMIdentityActive(ctx context.Context, id string, active bool) error {
	res, err := s.DB.NewUpdate().Model((*SCIMIdentity)(nil)).
		Set("active = ?", active).
		Set("updated_at = now()").
		Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// DeleteSCIMIdentity hard-deletes the identity row. SCIM itself never uses
// this (its DELETE is a deactivation) — it exists for the admin path and
// for tests that need to un-link a user. A no-op delete answers
// sql.ErrNoRows.
func (s *Store) DeleteSCIMIdentity(ctx context.Context, id string) error {
	res, err := s.DB.NewDelete().Model((*SCIMIdentity)(nil)).
		Where("id = ?", id).Exec(ctx)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// FindSCIMUserBySCIMID resolves one SCIM resource (identity + user) by its
// SCIM id — the read behind GET/PUT/PATCH/DELETE /Users/:id. A miss is
// sql.ErrNoRows.
func (s *Store) FindSCIMUserBySCIMID(ctx context.Context, scimID string) (*SCIMUserRow, error) {
	row := new(SCIMUserRow)
	return row, s.scimUserQuery().Where("si.id = ?", scimID).Scan(ctx)
}

// FindSCIMUserByUserName resolves one SCIM resource by userName, which is
// the user's email. The email is folded exactly like FindUserByEmail folds
// it, so the SCIM `userName eq "..."` filter cannot miss because of case.
// A miss is sql.ErrNoRows.
func (s *Store) FindSCIMUserByUserName(ctx context.Context, email string) (*SCIMUserRow, error) {
	row := new(SCIMUserRow)
	return row, s.scimUserQuery().Where("u.email = ?", normalizeEmail(email)).Scan(ctx)
}

// ListSCIMUsers returns one page of SCIM resources plus the total the
// filter matched, newest first with id as the tiebreaker so equal
// timestamps cannot swap between two pages of one listing.
//
// The count is a second, explicit query rather than ScanAndCount: the
// query spans a JOIN, and a hand-written COUNT over the same FROM/JOIN is
// the predictable shape (ScanAndCount wraps the select and its ORDER BY
// into a subquery, which is exactly where a two-table select is easiest to
// get subtly wrong). The count ignores limit/offset, which is what makes
// it a total.
func (s *Store) ListSCIMUsers(ctx context.Context, p Page) ([]*SCIMUserRow, int, error) {
	var out []*SCIMUserRow
	q := s.scimUserQuery().OrderExpr("si.created_at DESC, si.id DESC")
	total, err := s.DB.NewSelect().
		TableExpr("scim_identities AS si").
		Join("INNER JOIN users AS u ON u.id = si.user_id").
		Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	if p.Limit <= 0 {
		if err := q.Scan(ctx, &out); err != nil {
			return nil, 0, err
		}
		return out, len(out), nil
	}
	if p.Offset > 0 {
		q = q.Offset(p.Offset)
	}
	if err := q.Limit(p.Limit).Scan(ctx, &out); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}
