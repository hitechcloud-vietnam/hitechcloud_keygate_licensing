package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// The whole of the customer-facing event vocabulary must be storable
// as an inbox row, and the pinned title_key contract must hold for
// every one of them: the client translates the event name, so the key
// is the name — always.
func TestNewUserNotificationAcceptsWholeVocabulary(t *testing.T) {
	if len(CustomerWebhookEvents) != 28 {
		t.Fatalf("CustomerWebhookEvents has %d entries, want 28", len(CustomerWebhookEvents))
	}
	for _, ev := range CustomerWebhookEvents {
		n, err := NewUserNotification("u1", ev, nil, "", "")
		if err != nil {
			t.Fatalf("NewUserNotification(%q): %v", ev, err)
		}
		if n.TitleKey != ev {
			t.Errorf("event %q: title_key = %q, want the event itself", ev, n.TitleKey)
		}
		if n.Priority != NotificationPriorityNormal {
			t.Errorf("event %q: priority = %q, want %q", ev, n.Priority, NotificationPriorityNormal)
		}
		if n.Link != nil {
			t.Errorf("event %q: link = %v, want nil", ev, n.Link)
		}
	}
}

// An event outside the vocabulary is refused — the same closed list
// customer webhooks validate against, and the same list the migration
// CHECKs. "webhook.test" is deliberately NOT storable: it is a
// diagnostic delivery, not something that happened to a customer.
func TestNewUserNotificationRejectsUnknownEvent(t *testing.T) {
	// Whitespace is folded (like FoldCustomerWebhookEvents), so a
	// trailing space is not a distinct event — it is the same name.
	for _, ev := range []string{"", "webhook.test", "License.Activated", "bogus.event", "license.activated extra"} {
		if n, err := NewUserNotification("u1", ev, nil, "", ""); err == nil {
			t.Errorf("event %q: accepted as %+v, want a refusal", ev, n)
		}
	}
}

// title_key is PINNED to the event: prose a caller passes in is
// discarded, never stored.
func TestNormalizeUserNotificationPinsTitleKey(t *testing.T) {
	n := &UserNotification{
		UserID:   "u1",
		Event:    EventLicenseExpired,
		TitleKey: "Your license has expired!",
		Priority: NotificationPriorityHigh,
	}
	if err := NormalizeUserNotification(n); err != nil {
		t.Fatalf("NormalizeUserNotification: %v", err)
	}
	if n.TitleKey != EventLicenseExpired {
		t.Errorf("title_key = %q, want %q (no user-facing prose is ever stored)", n.TitleKey, EventLicenseExpired)
	}
}

// Priority is a closed vocabulary; empty is defaulted, anything else
// refused.
func TestNormalizeUserNotificationPriority(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
		ok   bool
	}{
		{"", NotificationPriorityNormal, true},
		{"normal", NotificationPriorityNormal, true},
		{"high", NotificationPriorityHigh, true},
		{"urgent", "", false},
		{"NORMAL", "", false},
	} {
		n := &UserNotification{UserID: "u1", Event: EventLicenseActivated, Priority: tc.in}
		err := NormalizeUserNotification(n)
		if tc.ok != (err == nil) {
			t.Errorf("priority %q: err = %v, want ok=%v", tc.in, err, tc.ok)
		}
		if tc.ok && n.Priority != tc.want {
			t.Errorf("priority %q: normalized to %q, want %q", tc.in, n.Priority, tc.want)
		}
	}
}

// Link must be a root-relative deep link or absent. A blank link is
// stored as NULL (the column is nullable so "no destination" is not
// confused with "destination /").
func TestNormalizeUserNotificationLink(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string // "" means the row must end up with Link == nil
		ok   bool
	}{
		{"", "", true},
		{"/portal/licenses", "/portal/licenses", true},
		{"/portal/orders", "/portal/orders", true},
		{"  /portal/licenses  ", "/portal/licenses", true},
		{"portal/licenses", "", false},
		{"https://evil.example/portal", "", false},
		{"//evil.example", "", false},
	} {
		n := &UserNotification{UserID: "u1", Event: EventLicenseActivated}
		if tc.in != "" {
			n.Link = &tc.in
		}
		err := NormalizeUserNotification(n)
		if tc.ok != (err == nil) {
			t.Errorf("link %q: err = %v, want ok=%v", tc.in, err, tc.ok)
			continue
		}
		if !tc.ok {
			continue
		}
		switch {
		case tc.want == "" && n.Link != nil:
			t.Errorf("link %q: stored as %q, want NULL", tc.in, *n.Link)
		case tc.want != "" && n.Link == nil:
			t.Errorf("link %q: stored as NULL, want %q", tc.in, tc.want)
		case tc.want != "" && *n.Link != tc.want:
			t.Errorf("link %q: stored as %q, want %q", tc.in, *n.Link, tc.want)
		}
	}
}

// An empty user id is refused — a row with no owner is a row no
// endpoint can ever resolve.
func TestNewUserNotificationRequiresUser(t *testing.T) {
	if _, err := NewUserNotification("", EventLicenseActivated, nil, "", ""); err == nil {
		t.Fatal("empty user id accepted, want a refusal")
	}
}

// The API contract of GET /portal/notifications is exactly
// {id, event, title_key, data, link, priority, read_at, created_at} —
// and never user_id (the caller is reading their own rows).
func TestUserNotificationJSONShape(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	n := &UserNotification{
		ID:        "n1",
		UserID:    "u1",
		Event:     EventLicenseExpired,
		TitleKey:  EventLicenseExpired,
		Data:      map[string]any{"license_id": "lic_1"},
		Priority:  NotificationPriorityHigh,
		CreatedAt: now,
	}
	raw, err := json.Marshal(n)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	want := []string{"id", "event", "title_key", "data", "link", "priority", "read_at", "created_at"}
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("JSON is missing %q; got keys %v", k, mapsKeys(got))
		}
	}
	if _, leaked := got["user_id"]; leaked {
		t.Error("JSON leaked user_id — rows are the caller's own and must not carry the owner column")
	}
	if len(got) != len(want) {
		t.Errorf("JSON has %d keys %v, want exactly %d", len(got), mapsKeys(got), len(want))
	}
	// data carries the render params (it is set in the fixture above)…
	if data, ok := got["data"].(map[string]any); !ok || data["license_id"] != "lic_1" {
		t.Errorf("data = %v, want the render params", got["data"])
	}
	// …while absent members read as null, never as an empty
	// string/object that a client would have to special-case.
	for _, k := range []string{"link", "read_at"} {
		if got[k] != nil {
			t.Errorf("%s = %v, want null when unset", k, got[k])
		}
	}
}

func mapsKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Pin the INSERT column names Bun generates for the model: the table
// carries a `data` JSONB, a nullable `link`, and the owner column is
// user_id (NOT to be confused with the `notifications` email-dedup
// ledger keyed (license_id, tag)).
func TestUserNotificationBunColumns(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	n := &UserNotification{
		ID: "n1", UserID: "u1", Event: EventLicenseActivated,
		TitleKey: EventLicenseActivated, Priority: NotificationPriorityNormal,
		// Non-zero so Bun includes it: fields tagged default are
		// omitted from INSERT at their zero value.
		CreatedAt: time.Now(),
	}
	raw, err := db.NewInsert().Model(n).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build insert: %v", err)
	}
	sqlText := string(raw)
	if !strings.Contains(sqlText, `INSERT INTO "user_notifications"`) {
		t.Errorf("insert does not target user_notifications; got:\n%s", sqlText)
	}
	for _, col := range []string{"id", "user_id", "event", "title_key", "data", "link", "priority", "read_at", "created_at"} {
		if !strings.Contains(sqlText, col) {
			t.Errorf("insert is missing column %q; got:\n%s", col, sqlText)
		}
	}
	if strings.Contains(sqlText, "license_id") {
		t.Errorf("insert carries license_id — this table is keyed by USER, not license; got:\n%s", sqlText)
	}
}
