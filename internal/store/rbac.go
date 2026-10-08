package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── RBAC (custom roles & permissions) ───
//
// Custom roles are named permission bundles (plan §8) layered on top
// of the existing users.role / is_admin, never replacing them. A
// user's effective permission set is the UNION of every role they
// hold (PermissionsForUser). The built-in bundles are seeded as
// is_system rows by the migration and are protected from mutation
// here.
//
// Bun alias reminder (see bun_alias_test.go): a model is aliased by
// the snake_case of the STRUCT name, so hand-written qualifiers on
// these models are "role", "role_permission" and "user_custom_role"
// — never the table names. The queries below avoid qualifiers
// entirely and use raw subqueries against real table names, which
// sidesteps the class of bug the alias pin exists for.

// ErrRoleNameTaken is the refusal the unique index on
// custom_roles.name gives: a second role with the same folded name.
// CreateRole and UpdateRole fold it out of the raw driver error so
// callers can answer 409 for it and 500 for everything else without
// knowing what a SQLSTATE is; IsRoleNameConflict is the matching
// question.
var ErrRoleNameTaken = errors.New("role name already exists")

// ErrSystemRoleProtected is the refusal every mutation of a seeded
// built-in bundle returns: the 12 plan §8 roles are the documented
// vocabulary and may not be deleted, renamed or re-permissioned.
// Custom roles are not affected. IsRoleProtected is the matching
// question.
var ErrSystemRoleProtected = errors.New("system role is protected")

// ErrRoleInUse is DeleteRole's refusal while any user still holds the
// role: a delete would silently strip permissions from live users,
// which is the admin's decision to make explicitly (revoke first).
// The assignments themselves cascade — this check exists precisely
// so that cascade cannot take anyone's permissions by surprise.
// IsRoleInUseConflict is the matching question.
var ErrRoleInUse = errors.New("role is assigned to one or more users")

// ErrUnknownPermission is the refusal of any grant outside the
// closed §8 vocabulary (model.ValidPermission). Callers who validate
// first (the admin handler) answer 400 before this can fire; it is
// the store's backstop so an invalid grant can never reach the
// database through a path that forgot to check.
var ErrUnknownPermission = errors.New("unknown permission")

// IsRoleNameConflict reports whether err is the name-taken refusal,
// in either spelling: the sentinel CreateRole and UpdateRole return,
// or a raw unique-violation that reached the caller without passing
// through them. Mirrors IsCategorySlugConflict.
func IsRoleNameConflict(err error) bool {
	return errors.Is(err, ErrRoleNameTaken) || isUniqueViolation(err)
}

// IsRoleProtected reports whether err is the system-role refusal.
func IsRoleProtected(err error) bool {
	return errors.Is(err, ErrSystemRoleProtected)
}

// IsRoleInUseConflict reports whether err is the role-in-use
// refusal.
func IsRoleInUseConflict(err error) bool {
	return errors.Is(err, ErrRoleInUse)
}

// cleanPermissions validates a permission set against the closed §8
// vocabulary and drops duplicates (the pair is the primary key, so a
// repeated permission would abort an otherwise valid write). Order is
// kept, so an admin's list comes back the way they sent it.
func cleanPermissions(perms []string) ([]string, error) {
	seen := make(map[string]bool, len(perms))
	out := make([]string, 0, len(perms))
	for _, p := range perms {
		p = strings.TrimSpace(p)
		if !model.ValidPermission(p) {
			return nil, fmt.Errorf("%w: %q", ErrUnknownPermission, p)
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out, nil
}

// insertRolePermissionsIn writes a role's whole permission set. The
// rows are the persisted half of a bundle; ON CONFLICT DO NOTHING
// makes a repeated write idempotent instead of aborting the set.
func insertRolePermissionsIn(ctx context.Context, db bun.IDB, roleID string, perms []string) error {
	if len(perms) == 0 {
		return nil
	}
	rows := make([]*model.RolePermission, 0, len(perms))
	for _, p := range perms {
		rows = append(rows, &model.RolePermission{RoleID: roleID, Permission: p})
	}
	_, err := db.NewInsert().Model(&rows).
		On("CONFLICT (role_id, permission) DO NOTHING").
		Exec(ctx)
	return err
}

// replaceRolePermissionsIn drops and rewrites a role's permission
// set in one transaction, so a reader never sees a half-replaced
// bundle.
func replaceRolePermissionsIn(ctx context.Context, tx bun.Tx, roleID string, perms []string) error {
	if _, err := tx.NewDelete().
		Model((*model.RolePermission)(nil)).
		Where("role_id = ?", roleID).
		Exec(ctx); err != nil {
		return err
	}
	return insertRolePermissionsIn(ctx, tx, roleID, perms)
}

// CreateRole writes a new role and its permission set in one
// transaction: a role that exists without the permissions it was
// created with would be a bundle nobody asked for. The name is
// folded (see model.NormalizeRoleName) so uniqueness holds across
// spellings. perms may be empty — a role with no grants is a legal
// (if useless) starting point. A duplicate name is
// ErrRoleNameTaken.
func (s *Store) CreateRole(ctx context.Context, r *model.Role, perms []string) error {
	cleaned, err := cleanPermissions(perms)
	if err != nil {
		return err
	}
	return RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		if r.ID == "" {
			r.ID = newID()
		}
		r.Name = model.NormalizeRoleName(r.Name)
		if _, err := tx.NewInsert().Model(r).Exec(ctx); err != nil {
			if isUniqueViolation(err) {
				return ErrRoleNameTaken
			}
			return err
		}
		return insertRolePermissionsIn(ctx, tx, r.ID, cleaned)
	})
}

// FindRoleByID reads one role row.
func (s *Store) FindRoleByID(ctx context.Context, id string) (*model.Role, error) {
	r := new(model.Role)
	return r, s.DB.NewSelect().Model(r).Where("id = ?", id).Scan(ctx)
}

// FindRoleByName reads one role by its folded name (see
// model.NormalizeRoleName). Names are folded before they are stored,
// so folding the query is the whole of the lookup.
func (s *Store) FindRoleByName(ctx context.Context, name string) (*model.Role, error) {
	r := new(model.Role)
	return r, s.DB.NewSelect().Model(r).
		Where("name = ?", model.NormalizeRoleName(name)).
		Scan(ctx)
}

// ListRoles is the admin listing: one page of roles plus how many
// the filter matched. Ordered by name with id as the tiebreaker, so
// equal names cannot swap between two pages of one listing.
func (s *Store) ListRoles(ctx context.Context, search string, p Page) ([]*model.Role, int, error) {
	var out []*model.Role
	q := s.DB.NewSelect().Model(&out).
		OrderExpr("name ASC, id ASC")
	if search != "" {
		q = q.Where("name ILIKE ? OR description ILIKE ?", "%"+search+"%", "%"+search+"%")
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

// UpdateRole writes a role's name and description, and — when perms
// is non-nil — replaces its permission set in the same transaction.
// A nil perms means "leave the set alone", so a rename cannot
// accidentally empty a bundle; an empty-but-non-nil perms is a
// deliberate clear. The name is folded like a create, so a value
// refused at creation cannot be put on the same role a moment later.
// A duplicate name is ErrRoleNameTaken; a rename or re-permission of
// a seeded bundle is ErrSystemRoleProtected.
func (s *Store) UpdateRole(ctx context.Context, r *model.Role, perms []string) error {
	var cleaned []string
	if perms != nil {
		var err error
		if cleaned, err = cleanPermissions(perms); err != nil {
			return err
		}
	}
	return RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		existing := new(model.Role)
		if err := tx.NewSelect().Model(existing).Where("id = ?", r.ID).Scan(ctx); err != nil {
			return err // sql.ErrNoRows bubbles up: the caller answers 404
		}
		r.Name = model.NormalizeRoleName(r.Name)
		if existing.IsSystem && (r.Name != existing.Name || perms != nil) {
			return ErrSystemRoleProtected
		}
		r.UpdatedAt = time.Now()
		if _, err := tx.NewUpdate().Model(r).Column("name", "description", "updated_at").
			WherePK().Exec(ctx); err != nil {
			if isUniqueViolation(err) {
				return ErrRoleNameTaken
			}
			return err
		}
		if perms != nil {
			return replaceRolePermissionsIn(ctx, tx, r.ID, cleaned)
		}
		return nil
	})
}

// SetRolePermissions replaces a role's permission set wholesale —
// the whole set written in one transaction. Seeded bundles are
// protected (ErrSystemRoleProtected): their sets ARE the documented
// vocabulary. A role that does not exist is sql.ErrNoRows.
func (s *Store) SetRolePermissions(ctx context.Context, roleID string, perms []string) error {
	cleaned, err := cleanPermissions(perms)
	if err != nil {
		return err
	}
	return RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		role := new(model.Role)
		if err := tx.NewSelect().Model(role).Where("id = ?", roleID).Scan(ctx); err != nil {
			return err
		}
		if role.IsSystem {
			return ErrSystemRoleProtected
		}
		return replaceRolePermissionsIn(ctx, tx, roleID, cleaned)
	})
}

// DeleteRole removes a role and (by cascade) its permission grants.
// Seeded bundles are protected (ErrSystemRoleProtected), and a role
// still held by any user is refused (ErrRoleInUse) rather than
// silently stripping their permissions — revoke first. A no-op
// delete answers sql.ErrNoRows so the caller can say 404 for a row
// that was never there rather than claim a deletion that did not
// happen.
func (s *Store) DeleteRole(ctx context.Context, id string) error {
	return RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		role := new(model.Role)
		if err := tx.NewSelect().Model(role).Where("id = ?", id).Scan(ctx); err != nil {
			return err
		}
		if role.IsSystem {
			return ErrSystemRoleProtected
		}
		assigned, err := tx.NewSelect().Model((*model.UserCustomRole)(nil)).
			Where("role_id = ?", id).Exists(ctx)
		if err != nil {
			return err
		}
		if assigned {
			return ErrRoleInUse
		}
		if _, err := tx.NewDelete().
			Model((*model.RolePermission)(nil)).
			Where("role_id = ?", id).
			Exec(ctx); err != nil {
			return err
		}
		_, err = tx.NewDelete().Model(role).WherePK().Exec(ctx)
		return err
	})
}

// PermissionsForRole reads one role's permission set.
func (s *Store) PermissionsForRole(ctx context.Context, roleID string) ([]string, error) {
	var rows []*model.RolePermission
	if err := s.DB.NewSelect().Model(&rows).
		Column("permission").
		Where("role_id = ?", roleID).
		OrderExpr("permission ASC").
		Scan(ctx); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Permission)
	}
	return out, nil
}

// PermissionsForUser returns the user's effective permission set:
// the UNION of every permission granted by every role they hold (a
// user may hold several; see model.UserCustomRole). A user with no
// roles gets an empty map, never nil, so callers can look up without
// a nil check. The wildcard (model.WildcardPermission) is honored by
// the callers of this map, not added to it.
func (s *Store) PermissionsForUser(ctx context.Context, userID string) (map[string]bool, error) {
	var rows []*model.RolePermission
	if err := s.DB.NewSelect().Model(&rows).
		ColumnExpr("DISTINCT permission").
		Where("role_id IN (SELECT role_id FROM user_custom_roles WHERE user_id = ?)", userID).
		Scan(ctx); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(rows))
	for _, r := range rows {
		out[r.Permission] = true
	}
	return out, nil
}

// RolesForUser lists the roles a user holds, by name.
func (s *Store) RolesForUser(ctx context.Context, userID string) ([]*model.Role, error) {
	var out []*model.Role
	err := s.DB.NewSelect().Model(&out).
		Where("id IN (SELECT role_id FROM user_custom_roles WHERE user_id = ?)", userID).
		OrderExpr("name ASC, id ASC").
		Scan(ctx)
	return out, err
}

// UsersWithRole lists the users holding a role — one page plus how
// many the filter matched.
func (s *Store) UsersWithRole(ctx context.Context, roleID string, p Page) ([]*model.User, int, error) {
	var out []*model.User
	q := s.DB.NewSelect().Model(&out).
		Where("id IN (SELECT user_id FROM user_custom_roles WHERE role_id = ?)", roleID).
		OrderExpr("name ASC, id ASC")
	total, err := scanPage(ctx, q, p)
	if err != nil {
		return nil, 0, err
	}
	if p.Limit <= 0 {
		total = len(out)
	}
	return out, total, nil
}

// AssignRole grants a user a role. Idempotent by construction: the
// pair is the primary key and the insert is ON CONFLICT DO NOTHING,
// so assigning a role twice is one row and no error — a retry after a
// lost response must not fail. Unknown user or role is refused by
// the foreign keys.
func (s *Store) AssignRole(ctx context.Context, userID, roleID string) error {
	_, err := s.DB.NewInsert().
		Model(&model.UserCustomRole{UserID: userID, RoleID: roleID}).
		On("CONFLICT (user_id, role_id) DO NOTHING").
		Exec(ctx)
	return err
}

// RevokeRole revokes a role from a user. Idempotent: revoking a role
// that is not held is a no-op and no error, so a retry after a lost
// response must not fail. The permissions the user keeps are the
// union over their remaining roles.
func (s *Store) RevokeRole(ctx context.Context, userID, roleID string) error {
	_, err := s.DB.NewDelete().
		Model((*model.UserCustomRole)(nil)).
		Where("user_id = ? AND role_id = ?", userID, roleID).
		Exec(ctx)
	return err
}
