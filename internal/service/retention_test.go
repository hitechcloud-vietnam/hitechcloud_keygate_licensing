package service

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// fakeRetainStore scripts a retention pass: settings plus one canned
// deletion response per call, per table. The zero value deletes
// nothing.
type fakeRetainStore struct {
	settings map[string]string
	setErr   map[string]error

	// script[table] are the per-call answers; when exhausted the
	// table answers 0. calls[table] counts invocations.
	script map[string][]int64
	calls  map[string]int

	// cutOffs records the cutoff each deletion was asked about.
	cutOffs map[string][]time.Time
}

func newFakeRetainStore() *fakeRetainStore {
	return &fakeRetainStore{
		settings: map[string]string{},
		setErr:   map[string]error{},
		script:   map[string][]int64{},
		calls:    map[string]int{},
		cutOffs:  map[string][]time.Time{},
	}
}

func (f *fakeRetainStore) GetSetting(_ context.Context, key string) (string, error) {
	if err, ok := f.setErr[key]; ok {
		return "", err
	}
	return f.settings[key], nil
}

// delete is the shared deleter: one scripted answer per call.
func (f *fakeRetainStore) delete(table string) func(context.Context, time.Time, int) (int64, error) {
	return func(_ context.Context, before time.Time, _ int) (int64, error) {
		f.calls[table]++
		f.cutOffs[table] = append(f.cutOffs[table], before)
		if len(f.script[table]) == 0 {
			return 0, nil
		}
		n := f.script[table][0]
		f.script[table] = f.script[table][1:]
		return n, nil
	}
}

func (f *fakeRetainStore) DeleteNotificationsBefore(ctx context.Context, before time.Time, batch int) (int64, error) {
	return f.delete(RetentionTableNotifications)(ctx, before, batch)
}
func (f *fakeRetainStore) DeleteProcessedEventsBefore(ctx context.Context, before time.Time, batch int) (int64, error) {
	return f.delete(RetentionTableProcessedEvents)(ctx, before, batch)
}
func (f *fakeRetainStore) DeleteWebhookDeliveriesBefore(ctx context.Context, before time.Time, batch int) (int64, error) {
	return f.delete(RetentionTableWebhookDeliveries)(ctx, before, batch)
}
func (f *fakeRetainStore) DeleteAuditLogsBefore(ctx context.Context, before time.Time, batch int) (int64, error) {
	return f.delete(RetentionTableAuditLogs)(ctx, before, batch)
}

// TestRetainDaysFallbacks pins §92's horizon parsing: a missing,
// empty, non-integer or negative setting falls back to the default; 0
// is a legal "never delete".
func TestRetainDaysFallbacks(t *testing.T) {
	ctx := context.Background()
	f := newFakeRetainStore()

	for _, tc := range []struct {
		raw  string
		key  string
		want int64
	}{
		{"", RetentionKeyNotifications, 90},
		{"   ", RetentionKeyProcessedEvents, 30},
		{"abc", RetentionKeyWebhookDeliveries, 90},
		{"-5", RetentionKeyAuditLogs, 365},
		{"45", RetentionKeyNotifications, 45},
		{"0", RetentionKeyProcessedEvents, 0}, // configured never
	} {
		f.settings[tc.key] = tc.raw
		got, err := retainDays(ctx, f, tc.key)
		if err != nil {
			t.Fatalf("retainDays(%q=%q): %v", tc.key, tc.raw, err)
		}
		if got != tc.want {
			t.Errorf("retainDays(%q=%q) = %d, want %d", tc.key, tc.raw, got, tc.want)
		}
	}

	// A missing row (sql.ErrNoRows) falls back too.
	delete(f.settings, RetentionKeyNotifications)
	got, err := retainDays(ctx, f, RetentionKeyNotifications)
	if err != nil || got != 90 {
		t.Errorf("missing setting = (%d, %v), want (90, nil)", got, err)
	}

	// Any other storage error propagates.
	f.setErr[RetentionKeyAuditLogs] = errors.New("db down")
	if _, err := retainDays(ctx, f, RetentionKeyAuditLogs); err == nil {
		t.Errorf("storage error must propagate")
	}
}

// TestRetainCutOff pins the horizon arithmetic: rows strictly older
// than now − days go; 0 (or negative) means never.
func TestRetainCutOff(t *testing.T) {
	now := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)

	if _, ok := retainCutOff(now, 0); ok {
		t.Errorf("horizon 0 must mean never delete")
	}
	if _, ok := retainCutOff(now, -3); ok {
		t.Errorf("negative horizon must mean never delete")
	}
	cut, ok := retainCutOff(now, 30)
	if !ok {
		t.Fatalf("horizon 30 must delete")
	}
	if want := now.AddDate(0, 0, -30); !cut.Equal(want) {
		t.Errorf("cutoff = %v, want %v", cut, want)
	}
}

// TestRunRetentionPass pins the pass itself: per-table horizons from
// settings, the drain loop running until a batch comes back short, a
// configured 0 skipping its table entirely, and the result always
// carrying all four tables.
func TestRunRetentionPass(t *testing.T) {
	f := newFakeRetainStore()
	f.settings = map[string]string{
		RetentionKeyNotifications:     "10",
		RetentionKeyProcessedEvents:   "0", // never
		RetentionKeyWebhookDeliveries: "",  // default 90
		RetentionKeyAuditLogs:         "7", //
	}
	// Notifications drain: a full batch, then a short one.
	f.script[RetentionTableNotifications] = []int64{1000, 250}
	f.script[RetentionTableWebhookDeliveries] = []int64{3}

	res, err := RunRetention(context.Background(), f)
	if err != nil {
		t.Fatalf("RunRetention: %v", err)
	}
	want := map[string]int64{
		RetentionTableNotifications:     1250,
		RetentionTableProcessedEvents:   0,
		RetentionTableWebhookDeliveries: 3,
		RetentionTableAuditLogs:         0,
	}
	for table, n := range want {
		if res.Deleted[table] != n {
			t.Errorf("deleted[%s] = %d, want %d", table, res.Deleted[table], n)
		}
	}
	if f.calls[RetentionTableProcessedEvents] != 0 {
		t.Errorf("a 0 horizon must never call the deleter, got %d calls", f.calls[RetentionTableProcessedEvents])
	}
	if f.calls[RetentionTableNotifications] != 2 {
		t.Errorf("notifications drained in %d calls, want 2 (full batch + short)", f.calls[RetentionTableNotifications])
	}

	// The cutoffs handed down are now − horizon (calendar days).
	now := time.Now()
	for table, days := range map[string]int{
		RetentionTableNotifications:     10,
		RetentionTableWebhookDeliveries: 90,
		RetentionTableAuditLogs:         7,
	} {
		if len(f.cutOffs[table]) == 0 {
			t.Errorf("%s: no cutoff recorded", table)
			continue
		}
		got := f.cutOffs[table][0]
		if want := now.AddDate(0, 0, -days); got.Sub(want) > 2*time.Second || want.Sub(got) > 2*time.Second {
			t.Errorf("%s cutoff = %v, want ≈ %v", table, got, want)
		}
	}

	// A second pass over an empty script deletes nothing — the pass
	// is idempotent.
	res, err = RunRetention(context.Background(), f)
	if err != nil {
		t.Fatalf("second RunRetention: %v", err)
	}
	for table, n := range res.Deleted {
		if n != 0 {
			t.Errorf("second pass deleted %d from %s, want 0", n, table)
		}
	}
}

// TestRunRetentionErrorSurfaces pins that a storage failure aborts
// the pass with the error (so the scheduler can count it).
func TestRunRetentionErrorSurfaces(t *testing.T) {
	f := newFakeRetainStore()
	f.setErr[RetentionKeyNotifications] = sql.ErrNoRows // falls back — fine
	f.settings[RetentionKeyAuditLogs] = "x"             // falls back — fine
	f.script[RetentionTableNotifications] = []int64{5}

	if _, err := RunRetention(context.Background(), f); err != nil {
		t.Errorf("fall-back-able settings must not fail the pass: %v", err)
	}

	boom := errors.New("boom")
	f2 := newFakeRetainStore()
	f2.setErr[RetentionKeyNotifications] = boom
	if _, err := RunRetention(context.Background(), f2); !errors.Is(err, boom) {
		t.Errorf("storage error = %v, want boom", err)
	}
}

// TestRetentionJobRun pins the scheduler-facing wrapper: Run returns
// the pass outcome and never panics with a nil logger.
func TestRetentionJobRun(t *testing.T) {
	f := newFakeRetainStore()
	if err := NewRetentionJob(f, nil).Run(context.Background()); err != nil {
		t.Errorf("Run: %v", err)
	}
	boom := errors.New("boom")
	f2 := newFakeRetainStore()
	f2.setErr[RetentionKeyNotifications] = boom
	if err := NewRetentionJob(f2, nil).Run(context.Background()); !errors.Is(err, boom) {
		t.Errorf("Run error = %v, want boom", err)
	}
}
