// Package events is the in-process hub that fans one lifecycle event
// out to every customer-facing sink.
//
// The platform already tells customers things happened through two
// channels: merchant/product webhooks + email (service.WebhookService,
// service.EmailService — dispatched INLINE at the call sites), and
// customer-registered webhooks (handler.PortalWebhookHandler.
// DispatchCustomerEvent — the fan-out helper that was documented as
// not yet wired). This package is the seam that wires the second one
// AND adds the third channel, the in-app notification center
// (plan §44 "In-app", §88): both subscribe to the hub and receive the
// same Event.
//
// Design rules, all of them deliberate:
//
//   - Emit is ADDITIVE. The call sites keep their existing merchant
//     webhook dispatch and email exactly as they were; the hub line is
//     one extra statement beside them. Nothing pre-existing routes
//     through here.
//   - Emit is best-effort and NEVER panics the caller: each sink runs
//     in its own goroutine with panic recovery, sink failures are
//     logged and dropped, and the wait for the fan-out is bounded
//     (emitWait) so a slow receiver cannot stall a request or the
//     expiry sweep. A notification is an outcome to observe, never a
//     transaction to fail.
//   - This package imports model and nothing else from the
//     codebase — in particular NOT handler (the Lead adapts
//     DispatchCustomerEvent into a WebhookSink at boot, which keeps
//     handler free to import events without a cycle) and not store
//     (the in-app sink talks to a two-method seam instead).
package events

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// Event is one thing that happened that a customer-facing surface may
// care about. Name is one of model.CustomerWebhookEvents (the same
// closed vocabulary customer webhooks subscribe to and the in-app
// rows validate against).
//
// UserEmail / UserID say WHO this happened to: the in-app sink needs
// an owner for the row (UserID wins when both are set; UserEmail is
// resolved through the users table otherwise). The customer-webhook
// sink ignores them — that fan-out is subscription-based, not
// recipient-based, exactly like DispatchCustomerEvent.
//
// Data is small render/dispatch context (license_id, order_number,
// amount_minor, a reason …) and must NEVER contain secrets or keys.
// Sinks must treat it as read-only.
//
// Link is a root-relative deep link ("/portal/licenses") for the
// in-app row; Priority is model.NotificationPriorityNormal/High
// (empty means normal).
type Event struct {
	Name      string
	UserEmail string
	UserID    string
	Data      map[string]any
	Link      string
	Priority  string
}

// Sink is one consumer of the hub. Deliver must be safe to call
// concurrently and must not panic (a panicking sink is still isolated
// by Emit — it just becomes a logged error instead of a crash).
type Sink interface {
	Deliver(ctx context.Context, n Event)
}

// DispatchFunc is the shape of
// handler.PortalWebhookHandler.DispatchCustomerEvent —
// fan one event out to every ACTIVE customer webhook subscribed to it.
// The Lead wraps the concrete handler in a closure with this signature
// and hands it to WebhookSink at boot, which is what keeps this
// package free of any handler import (and of any import cycle).
type DispatchFunc func(ctx context.Context, event string, data map[string]any) error

var (
	sinksMu sync.RWMutex
	sinks   []Sink

	// emitWait bounds how long Emit waits for the fan-out. Delivery
	// itself keeps running past it (best-effort, logged) — the bound
	// is on the CALLER's wait, so a wedged receiver cannot stall the
	// lifecycle path that merely reports the event. A variable, not a
	// const, so tests can tighten it.
	emitWait = 5 * time.Second
)

// Register adds a sink at boot time. Registration after the process
// starts serving is not the intended use (a sink added mid-flight may
// miss earlier events) but is safe: the sink list is mutex-guarded and
// Emit works on a copy. A nil sink is ignored.
func Register(s Sink) {
	if s == nil {
		return
	}
	sinksMu.Lock()
	defer sinksMu.Unlock()
	sinks = append(sinks, s)
}

// Emit fans ev out to every registered sink and returns without
// waiting longer than emitWait (or ctx, whichever comes first).
//
//   - no sinks registered → no-op;
//   - each sink runs in its own goroutine, so one slow sink cannot
//     serialize the others;
//   - a panicking sink is recovered and logged — the other sinks, the
//     caller and the process are unaffected;
//   - nothing a sink does can return an error to the caller. Events
//     are reported, not transacted.
func Emit(ctx context.Context, ev Event) {
	if ctx == nil {
		ctx = context.Background()
	}
	sinksMu.RLock()
	current := make([]Sink, len(sinks))
	copy(current, sinks)
	sinksMu.RUnlock()
	if len(current) == 0 {
		return
	}

	var wg sync.WaitGroup
	for _, s := range current {
		if s == nil {
			continue
		}
		wg.Add(1)
		go func(s Sink) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					slog.Error("events: sink panicked", "event", ev.Name, "panic", r)
				}
			}()
			s.Deliver(ctx, ev)
		}(s)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(emitWait):
		slog.Warn("events: sink fan-out exceeded wait budget", "event", ev.Name)
	case <-ctx.Done():
	}
}

// ─── In-app notification sink ───

// NotificationStore is the seam the in-app sink needs from the store.
// *store.Store satisfies it as-is (FindUserByEmail already exists;
// CreateUserNotification lives in store/notifications_center.go), and
// tests substitute a fake.
type NotificationStore interface {
	FindUserByEmail(ctx context.Context, email string) (*model.User, error)
	CreateUserNotification(ctx context.Context, n *model.UserNotification) error
}

// NotificationSink is the in-app notification center sink (plan §44
// "In-app", §88): every event becomes at most one user_notifications
// row.
//
// The owner is Event.UserID, or — when that is empty — the user
// Event.UserEmail resolves to. An event with neither, an email with no
// account behind it (an admin acting on someone's licence, a guest
// checkout not yet signed in), or an event name outside the vocabulary
// is quietly dropped: best-effort means an undeliverable notification
// is never an error and never a retry queue.
func NotificationSink(store NotificationStore) Sink {
	return &notificationSink{store: store}
}

type notificationSink struct {
	store NotificationStore
}

func (s *notificationSink) Deliver(ctx context.Context, ev Event) {
	if s == nil || s.store == nil {
		return
	}
	// The table's CHECK is the same closed list; dropping unknown
	// names here keeps the failure a log line instead of a SQL error.
	if !model.IsCustomerWebhookEvent(ev.Name) {
		slog.Warn("events: unknown event dropped by notification sink", "event", ev.Name)
		return
	}
	userID := ev.UserID
	if userID == "" {
		email := ev.UserEmail
		if email == "" {
			return
		}
		u, err := s.store.FindUserByEmail(ctx, email)
		if err != nil || u == nil || u.ID == "" {
			// No account for this address (or a lookup hiccup): there is
			// nobody to notify. Best-effort — dropped, not retried.
			return
		}
		userID = u.ID
	}
	n, err := model.NewUserNotification(userID, ev.Name, ev.Data, ev.Link, ev.Priority)
	if err != nil {
		// Only reachable on a bug at the call site (bad link/priority).
		slog.Warn("events: notification sink refused event", "event", ev.Name, "error", err)
		return
	}
	if err := s.store.CreateUserNotification(ctx, n); err != nil {
		slog.Warn("events: notification sink could not store row", "event", ev.Name, "error", err)
	}
}

// ─── Customer webhook sink ───

// WebhookSink adapts the customer-webhook fan-out
// (DispatchCustomerEvent) to the hub. The Lead constructs it at boot:
//
//	events.Register(events.WebhookSink(portalWebhooks.DispatchCustomerEvent))
//
// The event name and Data are delivered verbatim to every ACTIVE
// customer webhook subscribed to that event, signed exactly like the
// handler's own test-fire path. Failures are the delivery layer's to
// count and log; here they are a warning, never an error upstream.
func WebhookSink(dispatch DispatchFunc) Sink {
	return &webhookSink{dispatch: dispatch}
}

type webhookSink struct {
	dispatch DispatchFunc
}

func (s *webhookSink) Deliver(ctx context.Context, ev Event) {
	if s == nil || s.dispatch == nil || ev.Name == "" {
		return
	}
	if err := s.dispatch(ctx, ev.Name, ev.Data); err != nil {
		slog.Warn("events: customer webhook fan-out reported failure",
			"event", ev.Name, "error", err)
	}
}
