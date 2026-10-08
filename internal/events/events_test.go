package events

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ── test doubles ─────────────────────────────────────────────────────

// sinkFunc adapts a plain function to Sink so a test can register a
// closure without declaring a type per scenario.
type sinkFunc func(ctx context.Context, ev Event)

func (f sinkFunc) Deliver(ctx context.Context, ev Event) { f(ctx, ev) }

type captureSink struct {
	mu     sync.Mutex
	events []Event
}

func (c *captureSink) Deliver(_ context.Context, ev Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
}

func (c *captureSink) snapshot() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Event(nil), c.events...)
}

type panicSink struct{}

func (panicSink) Deliver(context.Context, Event) { panic("sink exploded") }

type fakeNotificationStore struct {
	mu        sync.Mutex
	users     map[string]string // folded email → user id
	created   []*model.UserNotification
	findErr   error
	createErr error
	lookups   int
}

func (f *fakeNotificationStore) FindUserByEmail(_ context.Context, email string) (*model.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lookups++
	if f.findErr != nil {
		return nil, f.findErr
	}
	if id, ok := f.users[strings.ToLower(strings.TrimSpace(email))]; ok {
		return &model.User{ID: id, Email: email}, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeNotificationStore) CreateUserNotification(_ context.Context, n *model.UserNotification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	f.created = append(f.created, n)
	return nil
}

func (f *fakeNotificationStore) rows() []*model.UserNotification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*model.UserNotification(nil), f.created...)
}

// resetSinks isolates each test from whatever the hub holds and from
// the tests that ran before it.
func resetSinks(t *testing.T) {
	t.Helper()
	sinksMu.Lock()
	old := sinks
	sinks = nil
	sinksMu.Unlock()
	t.Cleanup(func() {
		sinksMu.Lock()
		sinks = old
		sinksMu.Unlock()
	})
}

// ── hub ──────────────────────────────────────────────────────────────

// Every registered sink sees the event, with its fields intact.
func TestEmitFansOutToAllSinks(t *testing.T) {
	resetSinks(t)
	a, b, c := &captureSink{}, &captureSink{}, &captureSink{}
	Register(a)
	Register(b)
	Register(c)

	ev := Event{
		Name:      model.EventLicenseActivated,
		UserEmail: "alice@example.test",
		Data:      map[string]any{"license_id": "lic_1"},
		Link:      "/portal/licenses",
		Priority:  model.NotificationPriorityNormal,
	}
	Emit(context.Background(), ev)

	for name, s := range map[string]*captureSink{"a": a, "b": b, "c": c} {
		got := s.snapshot()
		if len(got) != 1 {
			t.Fatalf("sink %s got %d events, want 1", name, len(got))
		}
		if got[0].Name != ev.Name || got[0].UserEmail != ev.UserEmail ||
			got[0].Link != ev.Link || got[0].Priority != ev.Priority ||
			got[0].Data["license_id"] != "lic_1" {
			t.Errorf("sink %s got %+v, want %+v", name, got[0], ev)
		}
	}
}

// A sink that panics is isolated: the panic never reaches the caller
// and never stops the other sinks from receiving the event.
func TestEmitPanicIsolation(t *testing.T) {
	resetSinks(t)
	Register(panicSink{})
	ok := &captureSink{}
	Register(ok)

	Emit(context.Background(), Event{Name: model.EventLicenseExpired, UserEmail: "alice@example.test"})

	if got := ok.snapshot(); len(got) != 1 {
		t.Fatalf("healthy sink got %d events, want 1 (a panicking peer must not eat the fan-out)", len(got))
	}
}

// A wedged sink cannot stall the caller: the wait is bounded even
// though the sink never returns. This is the "sink error never
// blocks" half of the contract — slow is indistinguishable from stuck,
// and neither may block the lifecycle path that reports the event.
func TestEmitBoundedWaitOnWedgedSink(t *testing.T) {
	resetSinks(t)
	release := make(chan struct{})
	defer close(release)
	Register(sinkFunc(func(context.Context, Event) { <-release }))

	old := emitWait
	emitWait = 50 * time.Millisecond
	defer func() { emitWait = old }()

	start := time.Now()
	Emit(context.Background(), Event{Name: model.EventLicenseExpired})
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("Emit waited %v on a wedged sink, want the bounded %v", elapsed, emitWait)
	}
}

// No sinks is a no-op, a nil sink is ignored, and a nil context is
// tolerated — Emit is called from lifecycle code that may be shutting
// down, and none of those may panic.
func TestEmitEdgeCases(t *testing.T) {
	resetSinks(t)
	Emit(context.Background(), Event{Name: model.EventLicenseActivated}) // no sinks

	Register(nil)
	Register(sinkFunc(func(context.Context, Event) {}))
	Emit(nil, Event{Name: model.EventLicenseActivated}) // nil ctx with a sink registered
}

// ── webhook sink ─────────────────────────────────────────────────────

// The adapter hands the fan-out exactly the event name and data — no
// recipient fields leak into a webhook payload.
func TestWebhookSinkDispatchesNameAndData(t *testing.T) {
	var gotName string
	var gotData map[string]any
	s := WebhookSink(func(_ context.Context, event string, data map[string]any) error {
		gotName, gotData = event, data
		return nil
	})
	s.Deliver(context.Background(), Event{
		Name:      model.EventLicenseDeactivated,
		UserEmail: "alice@example.test",
		Data:      map[string]any{"license_id": "lic_1"},
		Link:      "/portal/licenses",
	})
	if gotName != model.EventLicenseDeactivated {
		t.Errorf("dispatched %q, want %q", gotName, model.EventLicenseDeactivated)
	}
	if gotData["license_id"] != "lic_1" {
		t.Errorf("dispatched data %v, want license_id lic_1", gotData)
	}
}

// A dispatch that reports failure (or a nil adapter) is swallowed:
// the sink never turns one receiver's trouble into the caller's.
func TestWebhookSinkSwallowsErrors(t *testing.T) {
	failing := WebhookSink(func(context.Context, string, map[string]any) error {
		return errors.New("4 of 4 customer webhook deliveries failed")
	})
	failing.Deliver(context.Background(), Event{Name: model.EventLicenseExpired, Data: map[string]any{}}) // must not panic

	WebhookSink(nil).Deliver(context.Background(), Event{Name: model.EventLicenseExpired})
}

// ── notification sink ────────────────────────────────────────────────

// With no UserID, the row's owner is the account behind UserEmail;
// the row is normalized (title_key = event, priority defaulted).
func TestNotificationSinkResolvesUserByEmail(t *testing.T) {
	st := &fakeNotificationStore{users: map[string]string{"alice@example.test": "u-alice"}}
	s := NotificationSink(st)
	s.Deliver(context.Background(), Event{
		Name:      model.EventLicenseActivated,
		UserEmail: "alice@example.test",
		Data:      map[string]any{"license_id": "lic_1"},
		Link:      "/portal/licenses",
	})
	rows := st.rows()
	if len(rows) != 1 {
		t.Fatalf("created %d rows, want 1", len(rows))
	}
	if rows[0].UserID != "u-alice" {
		t.Errorf("row owner = %q, want u-alice (resolved from the event email)", rows[0].UserID)
	}
	if rows[0].TitleKey != model.EventLicenseActivated {
		t.Errorf("title_key = %q, want the event name", rows[0].TitleKey)
	}
	if rows[0].Priority != model.NotificationPriorityNormal {
		t.Errorf("priority = %q, want %q", rows[0].Priority, model.NotificationPriorityNormal)
	}
	if rows[0].Link == nil || *rows[0].Link != "/portal/licenses" {
		t.Errorf("link = %v, want /portal/licenses", rows[0].Link)
	}
}

// A UserID on the event skips the email lookup entirely — sites that
// already know the user never round-trip through users.
func TestNotificationSinkPrefersUserID(t *testing.T) {
	st := &fakeNotificationStore{}
	NotificationSink(st).Deliver(context.Background(), Event{
		Name:     model.EventSeatAdded,
		UserID:   "u-7",
		Priority: model.NotificationPriorityHigh,
		Data:     map[string]any{"license_id": "lic_2"},
	})
	rows := st.rows()
	if len(rows) != 1 {
		t.Fatalf("created %d rows, want 1", len(rows))
	}
	if rows[0].UserID != "u-7" || rows[0].Priority != model.NotificationPriorityHigh {
		t.Errorf("row = %+v, want owner u-7 priority high", rows[0])
	}
	if st.lookups != 0 {
		t.Errorf("made %d email lookups with a UserID on the event, want 0", st.lookups)
	}
}

// Everything undeliverable is dropped quietly, and a storage failure
// is a log line, never a panic: best-effort all the way down.
func TestNotificationSinkDropsUndeliverable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		store *fakeNotificationStore
		ev    Event
	}{
		{"unknown event", &fakeNotificationStore{}, Event{Name: "webhook.test", UserEmail: "alice@example.test"}},
		{"no identity", &fakeNotificationStore{}, Event{Name: model.EventLicenseExpired}},
		{"email with no account", &fakeNotificationStore{findErr: sql.ErrNoRows}, Event{Name: model.EventLicenseExpired, UserEmail: "ghost@example.test"}},
		{"bad link", &fakeNotificationStore{users: map[string]string{"alice@example.test": "u1"}}, Event{Name: model.EventLicenseExpired, UserEmail: "alice@example.test", Link: "https://evil.example"}},
		{"create failure", &fakeNotificationStore{users: map[string]string{"alice@example.test": "u1"}, createErr: errors.New("db down")}, Event{Name: model.EventLicenseExpired, UserEmail: "alice@example.test"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			NotificationSink(tc.store).Deliver(context.Background(), tc.ev) // must not panic
			if rows := tc.store.rows(); len(rows) != 0 {
				t.Fatalf("created %d rows (%+v), want none", len(rows), rows)
			}
		})
	}
}
