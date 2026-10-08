package handler

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// NotificationCenterHandler serves the customer-facing in-app
// notification center (plan §44 "In-app" channel, §88 NOTIFICATION
// CENTER: unread count, read/unread, priority, timestamp, deep link).
//
//	GET  /portal/notifications             List the caller's inbox (?unread_only&limit&offset) + unread_count
//	GET  /portal/notifications/unread-count The badge number alone
//	POST /portal/notifications/:id/read    Mark one row read (ownership-scoped)
//	POST /portal/notifications/read-all    Mark everything read
//
// Identity: middleware.SessionAuth (the /portal group's auth) puts the
// session user's id into the gin context under "user_id" (see
// portalUserID). Every lookup here is scoped to that id, and a row
// that belongs to someone else answers the same 404 as one that does
// not exist — the endpoint is not a row-existence oracle.
//
// The response rows are exactly
// {id, event, title_key, data, link, priority, read_at, created_at} —
// the model's JSON shape — with NO user_id: the caller is reading
// their own rows, so the owner column is never serialised out.
// title_key is the i18n key (PINNED to the event name); the client
// translates it. No user-facing prose is stored or served.
type NotificationCenterHandler struct {
	store notificationCenterStore
}

// notificationCenterStore is the seam this handler needs from the
// store. *store.Store satisfies it (see the compile-time assertion
// below); tests substitute a fake so the handler can be exercised
// without a database.
type notificationCenterStore interface {
	ListUserNotifications(ctx context.Context, userID string, unreadOnly bool, p store.Page, sort store.Sort) ([]*model.UserNotification, int, error)
	CountUnread(ctx context.Context, userID string) (int, error)
	MarkRead(ctx context.Context, id, userID string) error
	MarkAllRead(ctx context.Context, userID string) (int, error)
}

var _ notificationCenterStore = (*store.Store)(nil)

// NewNotificationCenterHandler wires the handler to the store. The
// caller (main.go) passes the concrete *store.Store.
func NewNotificationCenterHandler(s *store.Store) *NotificationCenterHandler {
	return &NotificationCenterHandler{store: s}
}

// notificationUnreadOnly reads ?unread_only as a flag: its bare
// presence means "yes" (?unread_only), an explicit value is parsed as
// a boolean (?unread_only=true|false|1|0), and anything else is
// refused — a filter silently ignored would show rows the caller
// explicitly asked not to see. The second result is false on a bad
// value.
func notificationUnreadOnly(c *gin.Context) (bool, bool) {
	raw, present := c.GetQuery("unread_only")
	if !present {
		return false, true
	}
	if raw == "" {
		return true, true
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, false
	}
	return v, true
}

// notificationUser resolves the session user, answering 401 when
// there is none. Shared by all four endpoints so the refusal is one
// sentence.
func notificationUser(c *gin.Context) (string, bool) {
	userID := portalUserID(c)
	if userID == "" {
		response.Unauthorized(c, "unauthorized")
		return "", false
	}
	return userID, true
}

// notificationSortColumns is what ?sort= accepts on the notification
// inbox: the columns the rows carry. The expressions are qualified
// with the bun model alias ("user_notification"); "read_at" sorts the
// read receipts, with the unread rows (no receipt) trailing after
// them under NULLS LAST either way. Unknown keys keep the default
// ordering (listSortOrDefault), newest first.
var notificationSortColumns = map[string]sortCol{
	"created_at": {Expr: `"user_notification".created_at`, Desc: true},
	"event":      {Expr: `"user_notification".event`},
	"priority":   {Expr: `"user_notification".priority`, Desc: true},
	"read_at":    {Expr: `"user_notification".read_at`, Desc: true},
}

// List answers GET /portal/notifications: one page of the caller's
// inbox, newest first, with total/limit/offset and the unread badge
// count beside it (one round trip instead of a list plus a badge
// call). ?unread_only narrows the rows but not the badge.
func (h *NotificationCenterHandler) List(c *gin.Context) {
	userID, ok := notificationUser(c)
	if !ok {
		return
	}
	unreadOnly, ok := notificationUnreadOnly(c)
	if !ok {
		response.BadRequest(c, "unread_only must be a boolean")
		return
	}
	page := listPage(c)
	order := listSortOrDefault(c, notificationSortColumns, "created_at")
	rows, total, err := h.store.ListUserNotifications(c.Request.Context(), userID, unreadOnly, page, order)
	if err != nil {
		response.Internal(c, err)
		return
	}
	unread, err := h.store.CountUnread(c.Request.Context(), userID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "notifications", rows, total, page, gin.H{"unread_count": unread})
}

// UnreadCount answers GET /portal/notifications/unread-count: just the
// badge number, for the header poll.
func (h *NotificationCenterHandler) UnreadCount(c *gin.Context) {
	userID, ok := notificationUser(c)
	if !ok {
		return
	}
	unread, err := h.store.CountUnread(c.Request.Context(), userID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, gin.H{"unread_count": unread})
}

// MarkRead answers POST /portal/notifications/:id/read. The write is
// ownership-scoped in the store: someone else's row is the same 404
// as a missing one. Marking an already-read row again is a successful
// no-op that keeps its original read timestamp.
func (h *NotificationCenterHandler) MarkRead(c *gin.Context) {
	userID, ok := notificationUser(c)
	if !ok {
		return
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		response.BadRequest(c, "id is required in the URL path")
		return
	}
	if err := h.store.MarkRead(c.Request.Context(), id, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.NotFound(c, "notification not found")
			return
		}
		response.Internal(c, err)
		return
	}
	response.OK(c, gin.H{"status": "read"})
}

// MarkAllRead answers POST /portal/notifications/read-all: every
// unread row of the caller, nobody else's. Reports how many rows it
// changed so the client can move its badge without re-listing.
func (h *NotificationCenterHandler) MarkAllRead(c *gin.Context) {
	userID, ok := notificationUser(c)
	if !ok {
		return
	}
	updated, err := h.store.MarkAllRead(c.Request.Context(), userID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, gin.H{"status": "read", "updated": updated})
}

// ─── Admin-site helper ───

// licenseEmailLookup is the slice of the store the admin activation
// event site needs (see licenseOwnerEmail). *store.Store satisfies it.
type licenseEmailLookup interface {
	FindLicenseByID(ctx context.Context, id string) (*model.License, error)
}

// licenseOwnerEmail answers "whose inbox is this event for?" at the
// one event site that has a licence id but no loaded licence (the
// admin activation delete). It exists so that site's events.Emit can
// stay a single line — the same "one additive call per site" shape as
// every other wired site. An unknown licence resolves to "" and the
// in-app row is simply skipped (the customer-webhook fan-out at the
// same site is subscription-based and unaffected).
func licenseOwnerEmail(ctx context.Context, s licenseEmailLookup, licenseID string) string {
	if s == nil || licenseID == "" {
		return ""
	}
	lic, err := s.FindLicenseByID(ctx, licenseID)
	if err != nil || lic == nil {
		return ""
	}
	return lic.Email
}
