package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ── fake store ───────────────────────────────────────────────────────

type fakeNotificationCenterStore struct {
	rows               []*model.UserNotification
	total              int
	unread             int
	listErr            error
	countErr           error
	markErr            error
	markAllErr         error
	markAllN           int
	lastListUser       string
	lastListUnreadOnly bool
	lastSort           store.Sort
	markCalls          [][2]string // (id, userID) pairs MarkRead was asked about
}

func (f *fakeNotificationCenterStore) ListUserNotifications(_ context.Context, userID string, unreadOnly bool, p store.Page, sort store.Sort) ([]*model.UserNotification, int, error) {
	f.lastSort = sort
	f.lastListUser, f.lastListUnreadOnly = userID, unreadOnly
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	total := f.total
	if total == 0 {
		total = len(f.rows)
	}
	out := f.rows
	if p.Limit > 0 && p.Limit < len(out) {
		out = out[:p.Limit]
	}
	return out, total, nil
}

func (f *fakeNotificationCenterStore) CountUnread(context.Context, string) (int, error) {
	return f.unread, f.countErr
}

func (f *fakeNotificationCenterStore) MarkRead(_ context.Context, id, userID string) error {
	f.markCalls = append(f.markCalls, [2]string{id, userID})
	return f.markErr
}

func (f *fakeNotificationCenterStore) MarkAllRead(context.Context, string) (int, error) {
	return f.markAllN, f.markAllErr
}

// ── harness ──────────────────────────────────────────────────────────

func newNotificationCenterHarness() (*NotificationCenterHandler, *fakeNotificationCenterStore) {
	gin.SetMode(gin.TestMode)
	st := &fakeNotificationCenterStore{}
	return &NotificationCenterHandler{store: st}, st
}

func notificationCall(fn func(*gin.Context), method, userID, path string, params gin.Params) (int, string) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, nil)
	c.Params = params
	if userID != "" {
		c.Set("user_id", userID)
	}
	fn(c)
	return w.Code, w.Body.String()
}

func notificationRows() []*model.UserNotification {
	link := "/portal/licenses"
	return []*model.UserNotification{
		{
			ID: "n1", UserID: "u1", Event: model.EventLicenseExpired,
			TitleKey: model.EventLicenseExpired,
			Data:     map[string]any{"license_id": "lic_1"},
			Link:     &link,
			Priority: model.NotificationPriorityHigh,
		},
		{
			ID: "n2", UserID: "u1", Event: model.EventLicenseActivated,
			TitleKey: model.EventLicenseActivated,
			Priority: model.NotificationPriorityNormal,
		},
	}
}

// ── list ─────────────────────────────────────────────────────────────

// The listing answers exactly the API contract: the rows under
// "notifications" with total/limit/offset, the unread badge beside
// them, and no user_id anywhere in the payload.
func TestNotificationCenterList(t *testing.T) {
	h, st := newNotificationCenterHarness()
	st.rows = notificationRows()
	st.unread = 1

	code, body := notificationCall(h.List, http.MethodGet, "u1", "/api/v1/portal/notifications?limit=50&offset=0", nil)
	if code != http.StatusOK {
		t.Fatalf("list = %d %s", code, body)
	}
	var parsed struct {
		Data struct {
			Notifications []map[string]any `json:"notifications"`
			Total         int              `json:"total"`
			Limit         int              `json:"limit"`
			Offset        int              `json:"offset"`
			UnreadCount   int              `json:"unread_count"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("parse list: %v (%s)", err, body)
	}
	if parsed.Data.Total != 2 || len(parsed.Data.Notifications) != 2 {
		t.Fatalf("list has %d rows / total %d, want 2 / 2", len(parsed.Data.Notifications), parsed.Data.Total)
	}
	if parsed.Data.Limit != 50 || parsed.Data.Offset != 0 {
		t.Errorf("paging echo = %d/%d, want 50/0", parsed.Data.Limit, parsed.Data.Offset)
	}
	if parsed.Data.UnreadCount != 1 {
		t.Errorf("unread_count = %d, want 1", parsed.Data.UnreadCount)
	}
	for _, row := range parsed.Data.Notifications {
		for _, k := range []string{"id", "event", "title_key", "data", "link", "priority", "read_at", "created_at"} {
			if _, ok := row[k]; !ok {
				t.Errorf("row %v is missing %q", row["id"], k)
			}
		}
		if _, leaked := row["user_id"]; leaked {
			t.Errorf("row %v leaked user_id", row["id"])
		}
	}
	if st.lastListUser != "u1" {
		t.Errorf("store listed for %q, want the session user u1", st.lastListUser)
	}
}

// ?unread_only is a flag: bare presence means yes, explicit booleans
// are honoured, and anything else is refused rather than silently
// ignored.
func TestNotificationCenterUnreadOnlyFlag(t *testing.T) {
	for _, tc := range []struct {
		query  string
		want   bool
		status int
	}{
		{"", false, http.StatusOK},
		{"?unread_only", true, http.StatusOK},
		{"?unread_only=true", true, http.StatusOK},
		{"?unread_only=1", true, http.StatusOK},
		{"?unread_only=false", false, http.StatusOK},
		{"?unread_only=0", false, http.StatusOK},
		{"?unread_only=banana", false, http.StatusBadRequest},
	} {
		h, st := newNotificationCenterHarness()
		code, body := notificationCall(h.List, http.MethodGet, "u1", "/api/v1/portal/notifications"+tc.query, nil)
		if code != tc.status {
			t.Errorf("query %q: status = %d %s, want %d", tc.query, code, body, tc.status)
			continue
		}
		if code == http.StatusOK && st.lastListUnreadOnly != tc.want {
			t.Errorf("query %q: unreadOnly = %v, want %v", tc.query, st.lastListUnreadOnly, tc.want)
		}
	}
}

// ── unread badge ─────────────────────────────────────────────────────

func TestNotificationCenterUnreadCount(t *testing.T) {
	h, st := newNotificationCenterHarness()
	st.unread = 7
	code, body := notificationCall(h.UnreadCount, http.MethodGet, "u1", "/api/v1/portal/notifications/unread-count", nil)
	if code != http.StatusOK {
		t.Fatalf("unread-count = %d %s", code, body)
	}
	if !strings.Contains(body, `"unread_count":7`) {
		t.Errorf("unread-count body = %s, want unread_count 7", body)
	}
}

// ── read ─────────────────────────────────────────────────────────────

// Marking one row read is ownership-scoped: the store is asked about
// (id, session user), and a row it reports as someone else's (or
// missing — the same sql.ErrNoRows) is a 404, not a 500 and not a 200.
func TestNotificationCenterMarkRead(t *testing.T) {
	h, st := newNotificationCenterHarness()
	code, body := notificationCall(h.MarkRead, http.MethodPost, "u1",
		"/api/v1/portal/notifications/n1/read", gin.Params{{Key: "id", Value: "n1"}})
	if code != http.StatusOK {
		t.Fatalf("mark read = %d %s", code, body)
	}
	if !strings.Contains(body, `"status":"read"`) {
		t.Errorf("mark read body = %s", body)
	}
	if len(st.markCalls) != 1 || st.markCalls[0] != [2]string{"n1", "u1"} {
		t.Errorf("MarkRead asked about %v, want [(n1 u1)]", st.markCalls)
	}

	// Cross-user (or missing) row: quiet 404, no IDOR oracle.
	st.markCalls, st.markErr = nil, sql.ErrNoRows
	code, body = notificationCall(h.MarkRead, http.MethodPost, "bob",
		"/api/v1/portal/notifications/n1/read", gin.Params{{Key: "id", Value: "n1"}})
	if code != http.StatusNotFound {
		t.Errorf("cross-user mark read = %d %s, want 404", code, body)
	}
	if !strings.Contains(body, "notification not found") {
		t.Errorf("cross-user mark read body = %s, want a plain not-found", body)
	}
	if len(st.markCalls) != 1 || st.markCalls[0] != [2]string{"n1", "bob"} {
		t.Errorf("MarkRead asked about %v, want the CALLER's id pair [(n1 bob)] — scoping is the store's", st.markCalls)
	}

	// A real failure is a 500, not a 404.
	st.markErr = context.DeadlineExceeded
	code, _ = notificationCall(h.MarkRead, http.MethodPost, "u1",
		"/api/v1/portal/notifications/n1/read", gin.Params{{Key: "id", Value: "n1"}})
	if code != http.StatusInternalServerError {
		t.Errorf("failing MarkRead = %d, want 500", code)
	}
}

func TestNotificationCenterMarkAllRead(t *testing.T) {
	h, st := newNotificationCenterHarness()
	st.markAllN = 3
	code, body := notificationCall(h.MarkAllRead, http.MethodPost, "u1",
		"/api/v1/portal/notifications/read-all", nil)
	if code != http.StatusOK {
		t.Fatalf("mark all = %d %s", code, body)
	}
	if !strings.Contains(body, `"updated":3`) {
		t.Errorf("mark all body = %s, want updated 3", body)
	}
}

// ── session gate ─────────────────────────────────────────────────────

// No session, no inbox: all four endpoints answer the same 401.
func TestNotificationCenterRequiresSession(t *testing.T) {
	h, _ := newNotificationCenterHarness()
	for _, tc := range []struct {
		name string
		fn   func(*gin.Context)
	}{
		{"list", h.List},
		{"unread-count", h.UnreadCount},
		{"read", h.MarkRead},
		{"read-all", h.MarkAllRead},
	} {
		code, body := notificationCall(tc.fn, http.MethodPost, "", "/api/v1/portal/notifications", nil)
		if code != http.StatusUnauthorized {
			t.Errorf("%s without session = %d %s, want 401", tc.name, code, body)
		}
	}
}

// ─── Site wiring pins (admin.go + portal_activations.go) ───
//
// The events.Emit call at each lifecycle site is ADDITIVE: the
// pre-existing merchant-webhook dispatch keeps its exact event
// literal, and each dispatch of a wired event carries exactly one
// hub call referencing the shared model constant. These pins are the
// regression guard for both halves — the customer-webhook behaviour
// must not change, and the hub wiring must not drift from the
// constants (or silently double up).

// TestAdminAndPortalActivationEventWiring pins the two handler files
// this round wired (admin.go: expiry_changed + deactivated;
// portal_activations.go: deactivated).
func TestAdminAndPortalActivationEventWiring(t *testing.T) {
	// The constants the Emit lines use are exactly the literals the
	// pre-existing dispatches at the same sites send.
	for constant, literal := range map[string]string{
		model.EventLicenseExpiryChanged: "license.expiry_changed",
		model.EventLicenseDeactivated:   "license.deactivated",
	} {
		if constant != literal {
			t.Errorf("constant %q != dispatched literal %q — the hub and the webhook would disagree", constant, literal)
		}
	}

	cases := []struct {
		file     string
		emits    int
		literals []string // pre-existing Dispatch literals that must remain
	}{
		{"admin.go", 2, []string{`"license.expiry_changed"`, `"license.deactivated"`}},
		{"portal_activations.go", 1, []string{`"license.deactivated"`}},
	}
	for _, tc := range cases {
		src, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		text := string(src)
		if got := strings.Count(text, "events.Emit("); got != tc.emits {
			t.Errorf("%s has %d events.Emit call(s), want exactly %d (one per wired dispatch site)", tc.file, got, tc.emits)
		}
		for _, lit := range tc.literals {
			if !strings.Contains(text, lit) {
				t.Errorf("%s no longer contains the dispatched literal %s — pre-existing behaviour changed", tc.file, lit)
			}
		}
		if !strings.Contains(text, "model.EventLicense") {
			t.Errorf("%s wires the hub without a model.Event* constant — bare literals drift", tc.file)
		}
	}
}
