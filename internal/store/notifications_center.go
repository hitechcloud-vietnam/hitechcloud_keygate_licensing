package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/uptrace/bun"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── User Notifications (in-app notification center) ───
//
// The persistence half of the in-app inbox (plan §44 "In-app" channel,
// §88). Table user_notifications, model.UserNotification — a per-USER
// inbox row ("something happened, show it in the portal").
//
// NOT the `notifications` ledger: that table (keyed (license_id, tag),
// written by the reminder loops via ClaimNotification/HasNotification/
// RecordNotification) exists so an expiry or dunning EMAIL is sent at
// most once per episode. Nothing here touches it, and nothing there
// touches these rows — the two only share a prefix of a name.
//
// Hand-written qualifiers use the Bun alias "user_notification"
// (snake_case of the STRUCT name — see bun_alias_test.go for the
// pitfall), never the table name "user_notifications".

// CreateUserNotification files one inbox row. The ID is minted here
// when the caller has not set one (house pattern: newID). The row is
// expected to have been built by model.NewUserNotification /
// NormalizeUserNotification, so the CHECKs hold by construction; a
// violation reaching the database is a bug and surfaces as an error.
func (s *Store) CreateUserNotification(ctx context.Context, n *model.UserNotification) error {
	if n.ID == "" {
		n.ID = newID()
	}
	_, err := s.DB.NewInsert().Model(n).Exec(ctx)
	return err
}

// userNotificationsListQuery builds the inbox listing: this user's
// rows (created_at DESC with id DESC as the stable tiebreaker so
// OFFSET/LIMIT pages cannot repeat or skip a row — applySort's
// default), and optionally only the unread ones. Split out as a
// builder so its shape can be pinned without a database
// (notifications_center_test.go). An empty sort keeps the default,
// newest first.
func userNotificationsListQuery(db *bun.DB, userID string, unreadOnly bool, sort Sort, dest *[]*model.UserNotification) *bun.SelectQuery {
	if sort.Expr == "" {
		sort = Sort{Expr: `"user_notification".created_at`, Desc: true}
	}
	q := db.NewSelect().Model(dest).
		Where(`"user_notification".user_id = ?`, userID)
	if unreadOnly {
		q = q.Where(`"user_notification".read_at IS NULL`)
	}
	return applySort(q, sort, `"user_notification".id`)
}

// ListUserNotifications answers the inbox listing: one page of the
// user's rows (newest first, or the validated order the caller asked
// for) plus how many rows the filter matched in total. Ownership is
// in the WHERE clause — the query cannot see anyone else's rows even
// if the caller asked.
func (s *Store) ListUserNotifications(ctx context.Context, userID string, unreadOnly bool, p Page, sort Sort) ([]*model.UserNotification, int, error) {
	var out []*model.UserNotification
	q := userNotificationsListQuery(s.DB, userID, unreadOnly, sort, &out)
	total, err := scanPage(ctx, q, p)
	if err != nil {
		return nil, 0, err
	}
	if p.Limit <= 0 {
		total = len(out)
	}
	return out, total, nil
}

// CountUnread is the badge number: how many of this user's rows are
// still unread. Owned by the notification center; it never sees rows
// of other users and never touches the `notifications` email ledger.
func (s *Store) CountUnread(ctx context.Context, userID string) (int, error) {
	return s.DB.NewSelect().Model((*model.UserNotification)(nil)).
		Where("user_id = ? AND read_at IS NULL", userID).
		Count(ctx)
}

// MarkRead stamps one row as read — but only its owner's. The write is
// scoped by (id, user_id) in a single statement, so "belongs to
// someone else" and "does not exist" are the same sql.ErrNoRows and a
// caller cannot use the endpoint as a row-existence oracle (no IDOR).
//
// The stamp is COALESCEd: marking an already-read row again is a
// successful no-op that KEEPS the original read timestamp, so the
// "when did I read this" fact cannot be rewritten by a double click.
func (s *Store) MarkRead(ctx context.Context, id, userID string) error {
	res, err := s.DB.NewUpdate().Model((*model.UserNotification)(nil)).
		Set("read_at = COALESCE(read_at, now())").
		Where("id = ? AND user_id = ?", id, userID).
		Exec(ctx)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// MarkAllRead stamps every unread row of this user and reports how
// many it changed. Already-read rows keep their original read_at (the
// filter is read_at IS NULL), and rows of other users are untouched.
func (s *Store) MarkAllRead(ctx context.Context, userID string) (int, error) {
	res, err := s.DB.NewUpdate().Model((*model.UserNotification)(nil)).
		Set("read_at = now()").
		Where("user_id = ? AND read_at IS NULL", userID).
		Exec(ctx)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// PruneUserNotifications is the retention helper (plan §101 —
// documented retention rules; "do not silently delete financial
// records", and these are NOT financial records): it deletes inbox
// rows created before the cutoff and reports how many. Not wired to a
// schedule yet; the sweep that will call it lives with the other
// retention jobs, and that call site picks the policy (the rows
// themselves are pure UI state — the audit trail of what happened is
// in audit_logs and outlives them).
func (s *Store) PruneUserNotifications(ctx context.Context, before time.Time) (int, error) {
	res, err := s.DB.NewDelete().Model((*model.UserNotification)(nil)).
		Where("created_at < ?", before).
		Exec(ctx)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
