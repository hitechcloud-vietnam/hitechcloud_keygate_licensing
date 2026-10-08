package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// notificationsCenterTestDB is the TEST_DATABASE_URL-gated store for
// the inbox suite. Same contract as reviewsTestDB — no database, no
// test — kept local because this file also pins query shapes, which
// needs the unexported builders (package store, not store_test).
func notificationsCenterTestDB(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := New(dsn)
	if err != nil {
		t.Skipf("skipping integration test: %v", err)
	}
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations failed: %v", err)
	}
	return s
}

// The alias pin: Bun aliases model.UserNotification as
// "user_notification" (snake_case of the STRUCT name), NOT as the
// table name "user_notifications". Hand-written qualifiers in
// notifications_center.go must use that alias or Postgres answers
// "missing FROM-clause entry". Same bug class as
// TestBunDefaultTableAliases and TestReviewTableAliases.
func TestUserNotificationTableAlias(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	raw, err := db.NewSelect().Model((*model.UserNotification)(nil)).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)
	if !strings.Contains(sqlText, `FROM "user_notifications" AS "user_notification"`) &&
		!strings.Contains(sqlText, `AS "user_notification"`) {
		t.Errorf("generated SQL does not alias the model as \"user_notification\"; got:\n%s", sqlText)
	}
	if strings.Contains(sqlText, `AS "user_notifications"`) {
		t.Errorf("model aliased by TABLE name — that is the bug this pin exists for; got:\n%s", sqlText)
	}
}

// The listing statement without a database: scoped to the owner via
// the struct-name alias, newest first with the stable tiebreaker, and
// the unread filter present only when asked for.
func TestUserNotificationsListQueryShape(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	var dest []*model.UserNotification
	raw, err := userNotificationsListQuery(db, "u-1", true, Sort{}, &dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)
	for _, want := range []string{
		`AS "user_notification"`,
		`"user_notification".user_id = `,
		`"user_notification".read_at IS NULL`,
		`"user_notification".created_at DESC`,
		`"user_notification".id DESC`,
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("unread listing is missing %q; got:\n%s", want, sqlText)
		}
	}

	dest = nil
	raw, err = userNotificationsListQuery(db, "u-1", false, Sort{}, &dest).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	if sqlText := string(raw); strings.Contains(sqlText, "read_at IS NULL") {
		t.Errorf("plain listing filters on read_at; got:\n%s", sqlText)
	}
}

// ── DB-backed tests: skipped without TEST_DATABASE_URL ──

// inboxUser makes a throwaway account. Deleting it at cleanup cascades
// its inbox rows (migration: user_id ON DELETE CASCADE), so the suite
// leaves nothing behind.
func inboxUser(t *testing.T, s *Store, tag string) *model.User {
	t.Helper()
	ctx := context.Background()
	u := &model.User{
		ID:    newID(),
		Email: "inbox-" + tag + "-" + newID() + "@example.test",
		Name:  "Inbox " + tag,
	}
	if _, err := s.DB.NewInsert().Model(u).Exec(ctx); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.DB.NewDelete().Model((*model.User)(nil)).Where("id = ?", u.ID).Exec(context.Background())
	})
	return u
}

func mustNotify(t *testing.T, s *Store, userID, event, link string) *model.UserNotification {
	t.Helper()
	n, err := model.NewUserNotification(userID, event, map[string]any{"license_id": "lic_x"}, link, "")
	if err != nil {
		t.Fatalf("NewUserNotification: %v", err)
	}
	if err := s.CreateUserNotification(context.Background(), n); err != nil {
		t.Fatalf("CreateUserNotification: %v", err)
	}
	return n
}

// Ownership is enforced in the queries themselves: each of the read,
// count and list paths sees exactly one user's rows, and MarkRead on
// someone else's row is the same sql.ErrNoRows as a missing row.
func TestUserNotificationsOwnership(t *testing.T) {
	s := notificationsCenterTestDB(t)
	ctx := context.Background()

	alice := inboxUser(t, s, "alice")
	bob := inboxUser(t, s, "bob")

	aliceRow := mustNotify(t, s, alice.ID, model.EventLicenseActivated, "/portal/licenses")
	mustNotify(t, s, bob.ID, model.EventLicenseActivated, "/portal/licenses")

	// Count: only the owner's unread rows count.
	if n, err := s.CountUnread(ctx, alice.ID); err != nil || n != 1 {
		t.Fatalf("CountUnread(alice) = %d, %v; want 1, nil", n, err)
	}

	// List: only the owner's rows are visible.
	rows, total, err := s.ListUserNotifications(ctx, alice.ID, false, Page{Limit: 50}, Sort{})
	if err != nil || total != 1 || len(rows) != 1 {
		t.Fatalf("ListUserNotifications(alice) = %d rows, total %d, %v; want 1, 1, nil", len(rows), total, err)
	}
	if rows[0].ID != aliceRow.ID {
		t.Fatalf("alice listed row %q, want %q", rows[0].ID, aliceRow.ID)
	}

	// MarkRead cross-user: same answer as "no such row".
	if err := s.MarkRead(ctx, aliceRow.ID, bob.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("MarkRead(alice's row, bob) = %v, want sql.ErrNoRows", err)
	}
	// …and the row is still unread for its owner.
	if n, _ := s.CountUnread(ctx, alice.ID); n != 1 {
		t.Fatalf("cross-user MarkRead changed the badge: unread = %d, want 1", n)
	}
	// A row that does not exist at all is the same answer (no oracle).
	if err := s.MarkRead(ctx, "no-such-row", alice.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("MarkRead(missing) = %v, want sql.ErrNoRows", err)
	}

	// MarkAllRead only touches the caller's rows.
	changed, err := s.MarkAllRead(ctx, alice.ID)
	if err != nil || changed != 1 {
		t.Fatalf("MarkAllRead(alice) = %d, %v; want 1, nil", changed, err)
	}
	if n, _ := s.CountUnread(ctx, bob.ID); n != 1 {
		t.Fatalf("MarkAllRead(alice) moved bob's badge: unread = %d, want 1", n)
	}
}

// The read stamp is written once and kept: a second MarkRead is a
// successful no-op that preserves the original timestamp.
func TestUserNotificationsMarkReadKeepsTimestamp(t *testing.T) {
	s := notificationsCenterTestDB(t)
	ctx := context.Background()
	alice := inboxUser(t, s, "keep")
	row := mustNotify(t, s, alice.ID, model.EventLicenseExpired, "")

	if err := s.MarkRead(ctx, row.ID, alice.ID); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	first := new(model.UserNotification)
	if err := s.DB.NewSelect().Model(first).Where("id = ?", row.ID).Scan(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if first.ReadAt == nil {
		t.Fatal("read_at still NULL after MarkRead")
	}

	time.Sleep(10 * time.Millisecond)
	if err := s.MarkRead(ctx, row.ID, alice.ID); err != nil {
		t.Fatalf("second MarkRead: %v", err)
	}
	second := new(model.UserNotification)
	if err := s.DB.NewSelect().Model(second).Where("id = ?", row.ID).Scan(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if !second.ReadAt.Equal(*first.ReadAt) {
		t.Fatalf("second MarkRead rewrote read_at: %v -> %v", first.ReadAt, second.ReadAt)
	}
}

// The badge number and the unread_only filter agree with read_at.
func TestUserNotificationsUnreadOnly(t *testing.T) {
	s := notificationsCenterTestDB(t)
	ctx := context.Background()
	alice := inboxUser(t, s, "unread")
	mustNotify(t, s, alice.ID, model.EventSeatAdded, "")
	b := mustNotify(t, s, alice.ID, model.EventSeatRemoved, "")
	mustNotify(t, s, alice.ID, model.EventPlanChanged, "")

	if err := s.MarkRead(ctx, b.ID, alice.ID); err != nil {
		t.Fatalf("MarkRead: %v", err)
	}
	if n, err := s.CountUnread(ctx, alice.ID); err != nil || n != 2 {
		t.Fatalf("CountUnread = %d, %v; want 2, nil", n, err)
	}
	rows, total, err := s.ListUserNotifications(ctx, alice.ID, true, Page{Limit: 50}, Sort{})
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("unread listing = %d rows, total %d, %v; want 2, 2, nil", len(rows), total, err)
	}
	for _, r := range rows {
		if r.ReadAt != nil {
			t.Errorf("unread listing returned a read row %q", r.ID)
		}
	}
}

// Newest first, with the id tiebreaker keeping pages stable. Explicit
// timestamps, so the assertion cannot flake on two inserts landing in
// the same clock tick.
func TestUserNotificationsListOrder(t *testing.T) {
	s := notificationsCenterTestDB(t)
	ctx := context.Background()
	alice := inboxUser(t, s, "order")

	first := &model.UserNotification{
		UserID: alice.ID, Event: model.EventLicenseActivated,
		TitleKey: model.EventLicenseActivated, Priority: model.NotificationPriorityNormal,
		CreatedAt: time.Now().Add(-2 * time.Hour),
	}
	second := &model.UserNotification{
		UserID: alice.ID, Event: model.EventLicenseDeactivated,
		TitleKey: model.EventLicenseDeactivated, Priority: model.NotificationPriorityNormal,
		CreatedAt: time.Now().Add(-1 * time.Hour),
	}
	for _, n := range []*model.UserNotification{second, first} { // insert out of order
		if err := s.CreateUserNotification(ctx, n); err != nil {
			t.Fatalf("create row: %v", err)
		}
	}

	rows, total, err := s.ListUserNotifications(ctx, alice.ID, false, Page{Limit: 10}, Sort{})
	if err != nil || total != 2 {
		t.Fatalf("list = total %d, %v; want 2, nil", total, err)
	}
	if rows[0].ID != second.ID || rows[1].ID != first.ID {
		t.Fatalf("list order = [%q %q], want newest first [%q %q]", rows[0].ID, rows[1].ID, second.ID, first.ID)
	}
}

// Retention helper: deletes only rows strictly older than the cutoff
// and reports how many.
func TestPruneUserNotifications(t *testing.T) {
	s := notificationsCenterTestDB(t)
	ctx := context.Background()
	alice := inboxUser(t, s, "prune")

	old := &model.UserNotification{
		UserID: alice.ID, Event: model.EventLicenseExpired,
		TitleKey: model.EventLicenseExpired, Priority: model.NotificationPriorityNormal,
		CreatedAt: time.Now().Add(-90 * 24 * time.Hour),
	}
	if err := s.CreateUserNotification(ctx, old); err != nil {
		t.Fatalf("create old row: %v", err)
	}
	fresh := mustNotify(t, s, alice.ID, model.EventLicenseActivated, "")

	if _, err := s.PruneUserNotifications(ctx, time.Now().Add(-30*24*time.Hour)); err != nil {
		t.Fatalf("PruneUserNotifications: %v", err)
	}
	// The old row is gone…
	if err := s.DB.NewSelect().Model((*model.UserNotification)(nil)).Where("id = ?", old.ID).Scan(ctx); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("old row survived the prune: %v", err)
	}
	// …and the fresh one did not.
	if err := s.DB.NewSelect().Model((*model.UserNotification)(nil)).Where("id = ?", fresh.ID).Scan(ctx); err != nil {
		t.Fatalf("prune deleted a row newer than the cutoff: %v", err)
	}
}
