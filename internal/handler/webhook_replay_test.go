package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
)

// ── fakes ────────────────────────────────────────────────────────────

type fakeReplayStore struct {
	wh        *model.Webhook
	whErr     error
	orig      *model.WebhookDelivery
	origErr   error
	created   []*model.WebhookReplayDelivery
	updated   []*model.WebhookDelivery
	updatedWh []*model.Webhook
	audits    []*model.AuditLog
	nextID    int
}

func (f *fakeReplayStore) FindWebhookByID(_ context.Context, id string) (*model.Webhook, error) {
	if f.whErr != nil {
		return nil, f.whErr
	}
	if f.wh == nil || f.wh.ID != id {
		return nil, sql.ErrNoRows
	}
	return f.wh, nil
}

func (f *fakeReplayStore) FindWebhookDeliveryByID(_ context.Context, id string) (*model.WebhookDelivery, error) {
	if f.origErr != nil {
		return nil, f.origErr
	}
	if f.orig == nil || f.orig.ID != id {
		return nil, sql.ErrNoRows
	}
	return f.orig, nil
}

func (f *fakeReplayStore) CreateWebhookReplayDelivery(_ context.Context, d *model.WebhookReplayDelivery) error {
	if d.ID == "" {
		f.nextID++
		d.ID = fmt.Sprintf("dlv_fresh_%d", f.nextID)
	}
	f.created = append(f.created, d)
	return nil
}

func (f *fakeReplayStore) UpdateWebhookDelivery(_ context.Context, d *model.WebhookDelivery) error {
	f.updated = append(f.updated, d)
	return nil
}

func (f *fakeReplayStore) UpdateWebhook(_ context.Context, w *model.Webhook) error {
	f.updatedWh = append(f.updatedWh, w)
	return nil
}

func (f *fakeReplayStore) Audit(_ context.Context, log *model.AuditLog) {
	f.audits = append(f.audits, log)
}

type fakeReplayDoer struct {
	status   int
	err      error
	requests []*http.Request
	bodies   [][]byte
}

func (d *fakeReplayDoer) Do(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	d.requests = append(d.requests, req)
	d.bodies = append(d.bodies, body)
	if d.err != nil {
		return nil, d.err
	}
	return &http.Response{
		StatusCode: d.status,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
		Header:     make(http.Header),
	}, nil
}

func newReplayHarness() (*WebhookReplayHandler, *fakeReplayStore, *fakeReplayDoer) {
	st := &fakeReplayStore{
		wh: &model.Webhook{
			ID:        "wh_1",
			ProductID: "prod_1",
			URL:       "https://hooks.example.com/x",
			Secret:    "s3cret-current",
			Events:    []string{"order.created"},
			Active:    true,
		},
		orig: &model.WebhookDelivery{
			ID:        "dlv_orig_1",
			WebhookID: "wh_1",
			Event:     "order.created",
			Payload:   map[string]any{"event": "order.created", "timestamp": "2026-10-08T00:00:00Z", "data": map[string]any{"order_id": "o_1"}},
			Status:    "delivered",
		},
	}
	doer := &fakeReplayDoer{status: 200}
	svc := service.NewWebhookService(nil, slog.Default(), time.Second, 1, false)
	h := NewWebhookReplayHandler(st, svc)
	h.client = doer
	h.lookup = nil // literal classification only, unless a test wires a resolver
	return h, st, doer
}

func replayDeliveryCall(h *WebhookReplayHandler, params gin.Params) (int, string) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	c.Params = params
	c.Set("user_id", "admin_1")
	c.Set("is_admin", true)
	h.ReplayDelivery(c)
	return w.Code, w.Body.String()
}

func rotateCall(h *WebhookReplayHandler, params gin.Params) (int, string) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	c.Params = params
	c.Set("user_id", "admin_1")
	c.Set("is_admin", true)
	h.RotateSecret(c)
	return w.Code, w.Body.String()
}

func replayParams(webhookID, deliveryID string) gin.Params {
	return gin.Params{{Key: "id", Value: webhookID}, {Key: "delivery_id", Value: deliveryID}}
}

func replayData(t *testing.T, body string) map[string]any {
	t.Helper()
	var envelope struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("response is not the standard envelope: %v\n%s", err, body)
	}
	if !envelope.Success {
		t.Fatalf("expected success envelope, got %s", body)
	}
	return envelope.Data
}

func hmacSHA256Hex(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// ── replay ───────────────────────────────────────────────────────────

func TestWebhookReplaySignsAndMarks(t *testing.T) {
	h, st, doer := newReplayHarness()

	code, body := replayDeliveryCall(h, replayParams("wh_1", "dlv_orig_1"))
	if code != http.StatusCreated {
		t.Fatalf("want 201 CREATED, got %d\n%s", code, body)
	}
	data := replayData(t, body)
	if data["replay_of"] != "dlv_orig_1" {
		t.Errorf("replay_of must name the original delivery, got %v", data["replay_of"])
	}
	if len(st.created) != 1 {
		t.Fatalf("want exactly 1 new delivery row, got %d", len(st.created))
	}
	fresh := st.created[0]
	if fresh.ID == "dlv_orig_1" || fresh.ID == "" {
		t.Errorf("the replay must mint a FRESH delivery id, got %q", fresh.ID)
	}
	if fresh.ReplayOf != "dlv_orig_1" || fresh.WebhookID != "wh_1" || fresh.Event != "order.created" {
		t.Errorf("replay row misfiled: %+v", fresh)
	}

	if len(doer.requests) != 1 {
		t.Fatalf("want exactly 1 dispatch, got %d", len(doer.requests))
	}
	req, sent := doer.requests[0], doer.bodies[0]

	// Same signing shape as every other delivery in the system.
	if got := req.Header.Get("X-HiTechCloud-Event"); got != "order.created" {
		t.Errorf("event header = %q", got)
	}
	if got := req.Header.Get("X-HiTechCloud-Delivery"); got != fresh.ID || got == "dlv_orig_1" {
		t.Errorf("delivery header must be the FRESH id, got %q", got)
	}
	wantSig := "sha256=" + hmacSHA256Hex(sent, "s3cret-current")
	if got := req.Header.Get("X-HiTechCloud-Signature"); got != wantSig {
		t.Errorf("signature = %q, want %q", got, wantSig)
	}
	if got := req.Header.Get(webhookReplayMarkerHeader); got != webhookReplayMarkerValue {
		t.Errorf("replay marker header = %q, want %q", got, webhookReplayMarkerValue)
	}

	// Body byte-identical to the original dispatch payload.
	wantBody, _ := json.Marshal(st.orig.Payload)
	if !bytes.Equal(sent, wantBody) {
		t.Errorf("replay body differs from the original payload bytes:\nsent  %s\nwant %s", sent, wantBody)
	}

	// First attempt lifecycle: delivered, attempts=1.
	if fresh.Attempts != 1 || fresh.Status != "delivered" || fresh.DeliveredAt == nil {
		t.Errorf("first attempt not recorded as delivered: %+v", fresh.WebhookDelivery)
	}

	// Audit: Entity "webhook_delivery", Action "replayed".
	if len(st.audits) != 1 {
		t.Fatalf("want 1 audit row, got %d", len(st.audits))
	}
	a := st.audits[0]
	if a.Entity != "webhook_delivery" || a.Action != "replayed" || a.EntityID != fresh.ID {
		t.Errorf("audit misfiled: %+v", a)
	}
	if a.Changes["original_delivery_id"] != "dlv_orig_1" {
		t.Errorf("audit must name the original delivery: %+v", a.Changes)
	}
}

func TestWebhookReplaySignsWithCurrentSecret(t *testing.T) {
	h, st, doer := newReplayHarness()
	// The original was sent with some older secret; the CURRENT one is
	// what a replay must sign with (rotation's whole point).
	st.wh.Secret = "rotated-current-secret"

	if code, body := replayDeliveryCall(h, replayParams("wh_1", "dlv_orig_1")); code != http.StatusCreated {
		t.Fatalf("want 201, got %d\n%s", code, body)
	}
	sent := doer.bodies[0]
	got := doer.requests[0].Header.Get("X-HiTechCloud-Signature")
	if want := "sha256=" + hmacSHA256Hex(sent, "rotated-current-secret"); got != want {
		t.Errorf("signature = %q, want %q (signed with the CURRENT secret)", got, want)
	}
	if old := "sha256=" + hmacSHA256Hex(sent, "s3cret-current"); got == old {
		t.Error("replay must NOT sign with the superseded secret")
	}
}

func TestWebhookReplayOwnership404s(t *testing.T) {
	t.Run("missing webhook", func(t *testing.T) {
		h, st, doer := newReplayHarness()
		st.wh = nil
		code, body := replayDeliveryCall(h, replayParams("wh_gone", "dlv_orig_1"))
		assertSecNotFound(t, code, body, "webhook not found", st, doer)
	})
	t.Run("missing delivery", func(t *testing.T) {
		h, st, doer := newReplayHarness()
		st.orig = nil
		code, body := replayDeliveryCall(h, replayParams("wh_1", "dlv_gone"))
		assertSecNotFound(t, code, body, "delivery not found", st, doer)
	})
	t.Run("delivery of ANOTHER webhook is 404 (ownership)", func(t *testing.T) {
		h, st, doer := newReplayHarness()
		st.orig.WebhookID = "wh_other"
		code, body := replayDeliveryCall(h, replayParams("wh_1", "dlv_orig_1"))
		assertSecNotFound(t, code, body, "delivery not found", st, doer)
	})
}

func assertSecNotFound(t *testing.T, code int, body string, wantMsg string, st *fakeReplayStore, doer *fakeReplayDoer) {
	t.Helper()
	if code != http.StatusNotFound {
		t.Errorf("want 404, got %d\n%s", code, body)
	}
	if !strings.Contains(body, wantMsg) || !strings.Contains(body, `"code":"NOT_FOUND"`) {
		t.Errorf("404 body must be the quiet %q NOT_FOUND envelope, got %s", wantMsg, body)
	}
	if len(doer.requests) != 0 || len(st.created) != 0 || len(st.audits) != 0 {
		t.Errorf("a refused replay must leave no side effects: %d sends, %d rows, %d audits",
			len(doer.requests), len(st.created), len(st.audits))
	}
}

func TestWebhookReplayInactiveIs409(t *testing.T) {
	h, st, doer := newReplayHarness()
	st.wh.Active = false

	code, body := replayDeliveryCall(h, replayParams("wh_1", "dlv_orig_1"))
	if code != http.StatusConflict || !strings.Contains(body, `"code":"NOT_RESENDABLE"`) {
		t.Fatalf("want 409 NOT_RESENDABLE (same contract as resend), got %d\n%s", code, body)
	}
	if len(doer.requests) != 0 || len(st.created) != 0 || len(st.audits) != 0 {
		t.Errorf("nothing may be dispatched, stored or audited on refusal")
	}
}

func TestWebhookReplayFailureSchedulesRetry(t *testing.T) {
	h, st, doer := newReplayHarness()
	doer.status = 500

	code, body := replayDeliveryCall(h, replayParams("wh_1", "dlv_orig_1"))
	if code != http.StatusCreated {
		t.Fatalf("the replay row is created even when the receiver rejects it, want 201, got %d\n%s", code, body)
	}
	data := replayData(t, body)
	if data["status"] != "pending" {
		t.Errorf("failed first attempt must stay pending for the retry loop, got %v", data["status"])
	}
	fresh := st.created[0]
	if fresh.Attempts != 1 {
		t.Errorf("attempts = %d, want 1", fresh.Attempts)
	}
	if fresh.NextRetry == nil {
		t.Fatal("failed attempt must schedule a retry")
	}
	if wait := time.Until(*fresh.NextRetry); wait < 55*time.Second || wait > 65*time.Second {
		t.Errorf("retry in %s, want the standard 2^1·30s = 60s", wait)
	}
	if fresh.Status != "pending" || fresh.DeliveredAt != nil {
		t.Errorf("row state wrong: %+v", fresh.WebhookDelivery)
	}
}

// ── rotation ─────────────────────────────────────────────────────────

func TestWebhookRotateSecretShownOnce(t *testing.T) {
	h, st, doer := newReplayHarness()
	_ = doer

	code, body := rotateCall(h, gin.Params{{Key: "id", Value: "wh_1"}})
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d\n%s", code, body)
	}
	data := replayData(t, body)

	// Response shape mirrors CreateWebhook exactly.
	want := map[string]bool{"id": true, "product_id": true, "url": true, "secret": true, "events": true, "active": true, "created_at": true}
	if len(data) != len(want) {
		t.Errorf("rotate response keys = %v, want exactly the CreateWebhook keys", keysOf(data))
	}
	for k := range want {
		if _, ok := data[k]; !ok {
			t.Errorf("rotate response missing %q", k)
		}
	}

	secret, _ := data["secret"].(string)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(secret) {
		t.Errorf("merchant secret format broken: %q (want 64 lowercase hex)", secret)
	}
	if secret == "s3cret-current" {
		t.Error("rotate must return a NEW secret")
	}

	// Stored through the same update/seal path creation uses.
	if len(st.updatedWh) != 1 || st.updatedWh[0].Secret != secret {
		t.Errorf("store must receive the new secret exactly once: %+v", st.updatedWh)
	}
	// Audited — and never with the secret itself.
	if len(st.audits) != 1 {
		t.Fatalf("want 1 audit, got %d", len(st.audits))
	}
	a := st.audits[0]
	if a.Entity != "webhook" || a.Action != "secret_rotated" {
		t.Errorf("audit misfiled: %+v", a)
	}
	if auditBody, _ := json.Marshal(a.Changes); strings.Contains(string(auditBody), secret) {
		t.Error("the audit trail must never contain the secret")
	}

	// Second rotation yields a different secret again.
	_, body2 := rotateCall(h, gin.Params{{Key: "id", Value: "wh_1"}})
	data2 := replayData(t, body2)
	if data2["secret"] == secret {
		t.Error("two rotations must never repeat a secret")
	}
}

func TestWebhookRotateSecretOwnership404(t *testing.T) {
	h, st, doer := newReplayHarness()
	st.wh = nil
	_ = doer

	code, body := rotateCall(h, gin.Params{{Key: "id", Value: "wh_gone"}})
	if code != http.StatusNotFound || !strings.Contains(body, `"code":"NOT_FOUND"`) {
		t.Fatalf("want quiet 404 NOT_FOUND, got %d\n%s", code, body)
	}
	if len(st.updatedWh) != 0 || len(st.audits) != 0 {
		t.Error("a refused rotation must change and audit nothing")
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
