package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// fakeIdemStore is an in-memory IdempotencyStore so the middleware's
// claim / replay / refusal logic is covered without a database. It
// mirrors the real store's BeginIdempotent semantics exactly.
type fakeIdemStore struct {
	mu   sync.Mutex
	rows map[string]*store.IdempotencyRecord
	next int64
}

func newFakeIdemStore() *fakeIdemStore {
	return &fakeIdemStore{rows: map[string]*store.IdempotencyRecord{}}
}

func (f *fakeIdemStore) BeginIdempotent(ctx context.Context, scope, key, reqHash string) (*store.IdempotencyRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := scope + "\x00" + key
	rec, ok := f.rows[k]
	if !ok {
		f.next++
		nr := &store.IdempotencyRecord{ID: f.next, Scope: scope, IdempotencyKey: key, RequestHash: reqHash, State: "in_progress"}
		f.rows[k] = nr
		return nr, true, nil
	}
	if rec.RequestHash != reqHash {
		return nil, false, store.ErrIdempotencyKeyReused
	}
	if rec.State == "done" {
		return rec, false, nil
	}
	return nil, false, store.ErrIdempotencyInProgress
}

func (f *fakeIdemStore) FinishIdempotent(ctx context.Context, id int64, status int, body []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.ID == id {
			r.State = "done"
			r.ResponseStatus = status
			r.ResponseBody = append([]byte(nil), body...)
		}
	}
	return nil
}

func (f *fakeIdemStore) ReleaseIdempotent(ctx context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, r := range f.rows {
		if r.ID == id && r.State == "in_progress" {
			delete(f.rows, k)
		}
	}
	return nil
}

// makeApp wires the Idempotency middleware in front of `handler` on
// POST /test with a fixed scope, mirroring production registration.
func makeApp(s IdempotencyStore, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/test", Idempotency(func(c *gin.Context) string { return "user:1" }, s), handler)
	return r
}

func postJSON(r *gin.Engine, key, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	r.ServeHTTP(w, req)
	return w
}

// TestIdempotency_NoHeaderPassthrough — an opt-in layer: without an
// Idempotency-Key header the request passes straight through, the
// handler runs, and nothing is claimed or cached.
func TestIdempotency_NoHeaderPassthrough(t *testing.T) {
	f := newFakeIdemStore()
	var ran atomic.Bool
	r := makeApp(f, func(c *gin.Context) {
		ran.Store(true)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := postJSON(r, "", `{"x":1}`)
	if w.Code != 200 {
		t.Fatalf("code = %d, want 200", w.Code)
	}
	if !ran.Load() {
		t.Fatal("handler must run when there is no Idempotency-Key")
	}
	if len(f.rows) != 0 {
		t.Errorf("no slot may be claimed without a header; got %d rows", len(f.rows))
	}
}

// TestIdempotency_Replay — a retry with the same key AND same body gets
// the original response replayed (exact status + body) with the
// Idempotent-Replay: true header, and the handler runs only once.
func TestIdempotency_Replay(t *testing.T) {
	f := newFakeIdemStore()
	var attempts atomic.Int32
	r := makeApp(f, func(c *gin.Context) {
		attempts.Add(1)
		c.JSON(http.StatusCreated, gin.H{"id": "abc"})
	})

	body := `{"x":1}`
	w1 := postJSON(r, "k1", body)
	if w1.Code != 201 {
		t.Fatalf("first call: code = %d, want 201", w1.Code)
	}
	if w1.Header().Get("Idempotent-Replay") != "" {
		t.Error("first (non-replay) response must not carry Idempotent-Replay")
	}

	w2 := postJSON(r, "k1", body)
	if w2.Code != 201 {
		t.Fatalf("replay: code = %d, want 201", w2.Code)
	}
	if got := w2.Header().Get("Idempotent-Replay"); got != "true" {
		t.Errorf("replay header = %q, want %q", got, "true")
	}
	if w2.Body.String() != w1.Body.String() {
		t.Errorf("replay body = %q, want %q", w2.Body.String(), w1.Body.String())
	}
	if a := attempts.Load(); a != 1 {
		t.Errorf("handler ran %d times, want 1 (replay must not re-run)", a)
	}
}

// TestIdempotency_ReusedKey409 — the same key with a DIFFERENT body is a
// client bug and is refused 409 IDEMPOTENCY_KEY_REUSED (a single-status
// code the SDK can branch on).
func TestIdempotency_ReusedKey409(t *testing.T) {
	f := newFakeIdemStore()
	r := makeApp(f, func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	if w := postJSON(r, "k2", `{"a":1}`); w.Code != 200 {
		t.Fatalf("first call: code = %d", w.Code)
	}
	w2 := postJSON(r, "k2", `{"a":2}`) // different body, same key
	if w2.Code != 409 {
		t.Fatalf("reused key: code = %d, want 409", w2.Code)
	}
	if !strings.Contains(w2.Body.String(), "IDEMPOTENCY_KEY_REUSED") {
		t.Errorf("409 body missing IDEMPOTENCY_KEY_REUSED: %s", w2.Body.String())
	}
}

// TestIdempotency_InProgress409 — a second request with the same key and
// body while the first is still running (not yet finished) is refused
// 409 IDEMPOTENCY_IN_PROGRESS with a Retry-After hint.
func TestIdempotency_InProgress409(t *testing.T) {
	f := newFakeIdemStore()
	// Seed a slot that an earlier request claimed but never finished.
	h := requestHash(http.MethodPost, "/test", []byte(`{"x":4}`))
	f.rows["user:1\x00k4"] = &store.IdempotencyRecord{ID: 99, Scope: "user:1", IdempotencyKey: "k4", RequestHash: h, State: "in_progress"}

	var ran atomic.Bool
	r := makeApp(f, func(c *gin.Context) {
		ran.Store(true)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := postJSON(r, "k4", `{"x":4}`)
	if w.Code != 409 {
		t.Fatalf("code = %d, want 409", w.Code)
	}
	if !strings.Contains(w.Body.String(), "IDEMPOTENCY_IN_PROGRESS") {
		t.Errorf("409 body missing IDEMPOTENCY_IN_PROGRESS: %s", w.Body.String())
	}
	if got := w.Header().Get("Retry-After"); got == "" {
		t.Error("in-progress 409 should carry a Retry-After hint")
	}
	if ran.Load() {
		t.Error("handler must not run while a prior attempt is in progress")
	}
}

// TestIdempotency_5xxNotCached — a 5xx is transient, so it must NOT be
// cached: the slot is released and a retry runs the handler again. (A 4xx
// IS cached — it is a deterministic outcome — which is why the
// non-5xx path calls FinishIdempotent.)
func TestIdempotency_5xxNotCached(t *testing.T) {
	f := newFakeIdemStore()
	var attempts atomic.Int32
	r := makeApp(f, func(c *gin.Context) {
		attempts.Add(1)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "transient"})
	})

	body := `{"x":5}`
	if w := postJSON(r, "k5", body); w.Code != 500 {
		t.Fatalf("first call: code = %d, want 500", w.Code)
	}
	if len(f.rows) != 0 {
		t.Errorf("after 5xx the slot must be released; got %d rows", len(f.rows))
	}
	if w := postJSON(r, "k5", body); w.Code != 500 {
		t.Fatalf("retry: code = %d, want 500", w.Code)
	}
	if a := attempts.Load(); a != 2 {
		t.Errorf("handler ran %d times, want 2 (5xx must not be cached)", a)
	}
}

// TestIdempotency_FreshStoresResponse — a fresh claim runs the handler and
// stores its response (status + body) so a later retry replays it, and the
// first response carries no Idempotent-Replay header.
func TestIdempotency_FreshStoresResponse(t *testing.T) {
	f := newFakeIdemStore()
	r := makeApp(f, func(c *gin.Context) {
		c.JSON(http.StatusAccepted, gin.H{"queued": true})
	})

	w := postJSON(r, "k6", `{"x":6}`)
	if w.Code != 202 {
		t.Fatalf("code = %d, want 202", w.Code)
	}
	if w.Header().Get("Idempotent-Replay") != "" {
		t.Error("a fresh response must not carry Idempotent-Replay")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	rec := f.rows["user:1\x00k6"]
	if rec == nil || rec.State != "done" {
		t.Fatalf("slot not stored as done: %+v", rec)
	}
	if rec.ResponseStatus != 202 {
		t.Errorf("stored status = %d, want 202", rec.ResponseStatus)
	}
	if string(rec.ResponseBody) != w.Body.String() {
		t.Errorf("stored body = %q, want %q", rec.ResponseBody, w.Body.String())
	}
}

// TestIdempotency_PanicReleasesSlot — if the handler panics, the deferred
// cleanup releases the in_progress slot so a retry can claim fresh instead
// of being stuck 409 until the TTL.
func TestIdempotency_PanicReleasesSlot(t *testing.T) {
	f := newFakeIdemStore()
	r := gin.New()
	r.Use(gin.Recovery())
	r.POST("/test", Idempotency(func(c *gin.Context) string { return "user:1" }, f), func(c *gin.Context) {
		panic("boom")
	})

	w := postJSON(r, "k7", `{"x":7}`)
	if w.Code != 500 {
		t.Fatalf("code = %d, want 500 from recovered panic", w.Code)
	}
	if len(f.rows) != 0 {
		t.Errorf("after panic the slot must be released; got %d rows", len(f.rows))
	}
}
