package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/uptrace/bun"
)

// ─── User Notification (in-app notification center) ───
//
// One row in a customer's in-app inbox (plan §44 "In-app" channel,
// §88 NOTIFICATION CENTER: unread count, read/unread, priority,
// timestamp, deep link). This is the third notification surface and
// the third table:
//
//   - `notifications`       — the email-dedup LEASE ledger, keyed
//     (license_id, tag); "this reminder mail already went out".
//   - `webhook_deliveries`  — merchant webhook fan-out bookkeeping.
//   - `user_notifications`  — THIS model: a per-user inbox row, "this
//     happened, show it in the portal".
//
// # NO USER-FACING PROSE IS STORED
//
// TitleKey is PINNED to equal Event. The client translates the event
// name through i18n (plan §83 — all user-facing text translatable), so
// the row carries an i18n key, never a sentence: rewording a
// notification is a locale-file edit, and no translation is ever baked
// into the data. NormalizeUserNotification enforces this, and the
// migration repeats it as a CHECK (title_key = event).
//
// Data carries small client-side params (license_id, order_number,
// amount_minor, a reason string …) so the translated template can fill
// in values. It must NEVER hold secrets or credentials: no license
// keys, no signing secrets, no API keys, no tokens. Link is a
// root-relative deep link (must start with "/"), never an absolute URL
// — a notification can only ever point inside the app.
//
// The JSON shape of a row is exactly the API contract of
// GET /portal/notifications: {id, event, title_key, data, link,
// priority, read_at, created_at}. UserID is json:"-" — a caller only
// ever reads their own rows, so the column is never serialised out.

// NotificationPriorities is the closed priority vocabulary of an inbox
// row (§88 "priority").
var NotificationPriorities = []string{
	NotificationPriorityNormal,
	NotificationPriorityHigh,
}

const (
	// NotificationPriorityNormal is the default: informational, the
	// badge counts it but nothing is wrong.
	NotificationPriorityNormal = "normal"
	// NotificationPriorityHigh is for rows the customer should act on
	// (a license that expired, a payment that failed).
	NotificationPriorityHigh = "high"
)

// UserNotification is one in-app inbox row. Table user_notifications
// (the Bun alias is therefore "user_notification" — snake_case of the
// STRUCT name, see store/bun_alias_test.go for the pitfall).
type UserNotification struct {
	bun.BaseModel `bun:"table:user_notifications"`

	ID string `bun:",pk" json:"id"`
	// UserID is the inbox owner (FK users, ON DELETE CASCADE).
	// json:"-": never serialised — the caller reads their own rows.
	UserID string `bun:",notnull" json:"-"`
	// Event is one of CustomerWebhookEvents — the same closed
	// vocabulary customer webhooks subscribe to.
	Event string `bun:",notnull" json:"event"`
	// TitleKey is the i18n key; PINNED to equal Event (see header).
	TitleKey string `bun:",notnull" json:"title_key"`
	// Data is small render context, nullable. NEVER secrets/keys.
	Data map[string]any `bun:"type:jsonb,nullzero" json:"data"`
	// Link is a root-relative deep link path or NULL.
	Link *string `bun:",nullzero" json:"link"`
	// Priority is one of NotificationPriorities.
	Priority string `bun:",notnull,default:'normal'" json:"priority"`
	// ReadAt is NULL while the row is unread.
	ReadAt *time.Time `json:"read_at"`
	// CreatedAt is the "timestamp" of §88; newest-first listing order.
	CreatedAt time.Time `bun:",nullzero,default:now()" json:"created_at"`
}

// ValidNotificationPriority reports whether p is in the closed
// priority vocabulary. The empty string is NOT valid on a row — it is
// defaulted to NotificationPriorityNormal before validation.
func ValidNotificationPriority(p string) bool {
	return p == NotificationPriorityNormal || p == NotificationPriorityHigh
}

// NewUserNotification builds a normalized inbox row for userID. It is
// the one constructor the events hub uses: the event is validated
// against the customer-webhook vocabulary, TitleKey is forced to the
// event name, an empty priority becomes "normal", and a link must be
// root-relative ("/…" ) or absent. An empty userID, an unknown event,
// a bad priority or a bad link is refused — the table's CHECKs are the
// same rules and would refuse at insert time anyway.
func NewUserNotification(userID, event string, data map[string]any, link, priority string) (*UserNotification, error) {
	if strings.TrimSpace(userID) == "" {
		return nil, fmt.Errorf("user notification: user id is required")
	}
	n := &UserNotification{
		UserID:   strings.TrimSpace(userID),
		Event:    event,
		Data:     data,
		Priority: priority,
	}
	if link != "" {
		n.Link = &link
	}
	if err := NormalizeUserNotification(n); err != nil {
		return nil, err
	}
	return n, nil
}

// NormalizeUserNotification folds a freshly built row into the shape
// that gets stored — the one place the write rules live, so the row
// and the refusal cannot drift apart from the migration's CHECKs.
//
//   - Event is trimmed and must be in CustomerWebhookEvents.
//   - TitleKey is FORCED to Event: the pinned i18n contract. Any prose
//     a caller passed in is discarded, not stored.
//   - Priority: empty defaults to "normal"; anything outside the
//     vocabulary is refused.
//   - Link: absent/blank becomes NULL; anything present must start
//     with "/" (root-relative deep link; never an absolute URL).
func NormalizeUserNotification(n *UserNotification) error {
	if n == nil {
		return fmt.Errorf("user notification: nil row")
	}
	n.Event = strings.TrimSpace(n.Event)
	if !IsCustomerWebhookEvent(n.Event) {
		return fmt.Errorf("unknown event %q, expected one of: %s",
			n.Event, strings.Join(CustomerWebhookEvents, ", "))
	}
	n.TitleKey = n.Event
	if strings.TrimSpace(n.Priority) == "" {
		n.Priority = NotificationPriorityNormal
	}
	if !ValidNotificationPriority(n.Priority) {
		return fmt.Errorf("invalid priority %q, expected one of: %s",
			n.Priority, strings.Join(NotificationPriorities, ", "))
	}
	if n.Link != nil {
		link := strings.TrimSpace(*n.Link)
		switch {
		case link == "":
			n.Link = nil
		case !strings.HasPrefix(link, "/") || strings.HasPrefix(link, "//"):
			// Root-relative, not merely slash-prefixed: "//evil.example"
			// is a scheme-relative URL that would take the customer
			// off-site, which is exactly what the deep-link rule is for.
			return fmt.Errorf("link %q must be a root-relative path starting with /", link)
		default:
			n.Link = &link
		}
	}
	return nil
}
