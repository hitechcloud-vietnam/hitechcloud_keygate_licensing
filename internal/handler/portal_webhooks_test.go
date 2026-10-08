package handler

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ── fakes ────────────────────────────────────────────────────────────
//
// The handler talks to a customerWebhookStore seam and a customerWebhook
// Doer seam, so these tests run the whole portal surface WITHOUT a
// database or the network.

type fakeCustomerWebhookStore struct {
	hooks  []*model.CustomerWebhook
	nextID int
	audits []*model.AuditLog
}

func (f *fakeCustomerWebhookStore) CreateCustomerWebhook(_ context.Context, w *model.CustomerWebhook, rawSecret string) error {
	f.nextID++
	w.ID = fmt.Sprintf("wh_%d", f.nextID)
	w.Secret = rawSecret
	w.SecretPrefix = model.CustomerWebhookDisplayPrefix(rawSecret)
	f.hooks = append(f.hooks, w)
	return nil
}

func (f *fakeCustomerWebhookStore) ListCustomerWebhooksByUser(_ context.Context, userID string, p store.Page) ([]*model.CustomerWebhook, int, error) {
	var out []*model.CustomerWebhook
	for _, h := range f.hooks {
		if h.UserID == userID {
			out = append(out, h)
		}
	}
	total := len(out)
	if p.Offset > 0 {
		if p.Offset >= len(out) {
			out = nil
		} else {
			out = out[p.Offset:]
		}
	}
	if p.Limit > 0 && len(out) > p.Limit {
		out = out[:p.Limit]
	}
	return out, total, nil
}

func (f *fakeCustomerWebhookStore) FindCustomerWebhookByID(_ context.Context, id string) (*model.CustomerWebhook, error) {
	for _, h := range f.hooks {
		if h.ID == id {
			return h, nil
		}
	}
	return nil, sql.ErrNoRows
}

func (f *fakeCustomerWebhookStore) UpdateCustomerWebhook(_ context.Context, w *model.CustomerWebhook) error {
	for i, h := range f.hooks {
		if h.ID == w.ID {
			f.hooks[i] = w
			return nil
		}
	}
	return sql.ErrNoRows
}

func (f *fakeCustomerWebhookStore) DeleteCustomerWebhook(_ context.Context, id string) error {
	for i, h := range f.hooks {
		if h.ID == id {
			f.hooks = append(f.hooks[:i], f.hooks[i+1:]...)
			return nil
		}
	}
	return sql.ErrNoRows
}

func (f *fakeCustomerWebhookStore) CountCustomerWebhooksByUser(_ context.Context, userID string) (int, error) {
	n := 0
	for _, h := range f.hooks {
		if h.UserID == userID {
			n++
		}
	}
	return n, nil
}

func (f *fakeCustomerWebhookStore) FindCustomerWebhooksForEvent(_ context.Context, event string) ([]*model.CustomerWebhook, error) {
	var out []*model.CustomerWebhook
	for _, h := range f.hooks {
		if !h.Active {
			continue
		}
		for _, e := range h.Events {
			if e == event {
				out = append(out, h)
				break
			}
		}
	}
	return out, nil
}

func (f *fakeCustomerWebhookStore) TouchCustomerWebhookLastDelivery(_ context.Context, id string) error {
	for _, h := range f.hooks {
		if h.ID == id {
			now := time.Now()
			h.LastDeliveryAt = &now
		}
	}
	return nil
}

func (f *fakeCustomerWebhookStore) Audit(_ context.Context, log *model.AuditLog) {
	f.audits = append(f.audits, log)
}

type fakeDoer struct {
	requests []*http.Request
	bodies   [][]byte
	status   int
	err      error
}

func (f *fakeDoer) Do(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	f.requests = append(f.requests, req)
	f.bodies = append(f.bodies, body)
	if f.err != nil {
		return nil, f.err
	}
	status := f.status
	if status == 0 {
		status = 200
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}, nil
}

// ── harness ──────────────────────────────────────────────────────────

func newPortalWebhookHarness() (*PortalWebhookHandler, *fakeCustomerWebhookStore, *fakeDoer) {
	gin.SetMode(gin.TestMode)
	st := &fakeCustomerWebhookStore{}
	doer := &fakeDoer{status: 200}
	h := &PortalWebhookHandler{store: st, client: doer}
	return h, st, doer
}

func portalWebhookCall(fn func(*gin.Context), method, userID, path, body string, params gin.Params) (int, string) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	if userID != "" {
		c.Set("user_id", userID)
	}
	fn(c)
	return w.Code, w.Body.String()
}

// createOne registers a webhook for userID and returns (id, secret).
func createOne(t *testing.T, h *PortalWebhookHandler, userID, body string) (string, string) {
	t.Helper()
	code, resp := portalWebhookCall(h.Create, http.MethodPost, userID, "/api/v1/portal/webhooks", body, nil)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, resp)
	}
	var parsed struct {
		Data struct {
			Webhook struct {
				ID string `json:"id"`
			} `json:"webhook"`
			Secret string `json:"secret"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
		t.Fatalf("parse create: %v (%s)", err, resp)
	}
	return parsed.Data.Webhook.ID, parsed.Data.Secret
}

// ── tests ────────────────────────────────────────────────────────────

// The secret comes back exactly once (data.secret), and the row object
// never carries it — only secret_prefix. Every later read (list) is
// secret-free.
func TestPortalWebhookSecretShownOnce(t *testing.T) {
	h, _, _ := newPortalWebhookHarness()
	code, resp := portalWebhookCall(h.Create, http.MethodPost, "alice", "/api/v1/portal/webhooks",
		`{"url":"https://example.com/hook","events":["license.created"]}`, nil)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, resp)
	}
	var parsed struct {
		Data struct {
			Webhook map[string]any `json:"webhook"`
			Secret  string         `json:"secret"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(resp), &parsed); err != nil {
		t.Fatalf("parse create: %v (%s)", err, resp)
	}
	secret := parsed.Data.Secret
	if !strings.HasPrefix(secret, model.CustomerWebhookSecretPrefix) {
		t.Errorf("secret %q lacks the whsec_ prefix", secret)
	}
	// The row object must NOT contain the raw secret — only the prefix.
	if _, leaked := parsed.Data.Webhook["secret"]; leaked {
		t.Error("the webhook object leaked a secret field")
	}
	if _, leaked := parsed.Data.Webhook["key_hash"]; leaked {
		t.Error("the webhook object leaked a key_hash field")
	}
	prefix, _ := parsed.Data.Webhook["secret_prefix"].(string)
	if prefix == "" || prefix != secret[:model.CustomerWebhookSecretPrefixLength] {
		t.Errorf("secret_prefix = %q, want first %d chars of the secret", prefix, model.CustomerWebhookSecretPrefixLength)
	}

	// List never returns the secret.
	code, list := portalWebhookCall(h.List, http.MethodGet, "alice", "/api/v1/portal/webhooks", "", nil)
	if code != http.StatusOK {
		t.Fatalf("list = %d %s", code, list)
	}
	if strings.Contains(list, secret) {
		t.Error("list leaked the signing secret")
	}
	if !strings.Contains(list, `"secret_prefix"`) {
		t.Error("list is missing secret_prefix")
	}
}

// A body without required fields is refused; unauthenticated is 401.
func TestPortalWebhookCreateValidation(t *testing.T) {
	h, _, _ := newPortalWebhookHarness()
	for _, tc := range []struct {
		name string
		body string
	}{
		{"missing url", `{"events":["license.created"]}`},
		{"missing events", `{"url":"https://example.com/hook"}`},
		{"empty events", `{"url":"https://example.com/hook","events":[]}`},
		{"unknown event", `{"url":"https://example.com/hook","events":["bogus.event"]}`},
		{"ftp scheme", `{"url":"ftp://example.com/hook","events":["license.created"]}`},
		{"localhost", `{"url":"http://localhost/hook","events":["license.created"]}`},
		{"loopback ip", `{"url":"http://127.0.0.1/hook","events":["license.created"]}`},
		{"private ip", `{"url":"http://10.0.0.5/hook","events":["license.created"]}`},
	} {
		if code, body := portalWebhookCall(h.Create, http.MethodPost, "alice", "/api/v1/portal/webhooks", tc.body, nil); code != http.StatusBadRequest {
			t.Errorf("%s: create = %d %s, want 400", tc.name, code, body)
		}
	}
	if code, _ := portalWebhookCall(h.Create, http.MethodPost, "", "/api/v1/portal/webhooks",
		`{"url":"https://example.com/hook","events":["license.created"]}`, nil); code != http.StatusUnauthorized {
		t.Errorf("create without a session = %d, want 401", code)
	}
}

// Cross-user access answers the same 404 as a missing endpoint.
func TestPortalWebhookOwnership404(t *testing.T) {
	h, _, doer := newPortalWebhookHarness()
	id, _ := createOne(t, h, "alice", `{"url":"https://example.com/hook","events":["license.created"]}`)
	params := gin.Params{{Key: "id", Value: id}}

	// Bob (and anonymous) cannot see, edit, delete, or test it.
	for _, tc := range []struct {
		name string
		fn   func(*gin.Context)
		meth string
	}{
		{"patch", h.Update, http.MethodPatch},
		{"delete", h.Delete, http.MethodDelete},
		{"test", h.DispatchTest, http.MethodPost},
	} {
		if code, body := portalWebhookCall(tc.fn, tc.meth, "bob", "/api/v1/portal/webhooks/"+id, `{}`, params); code != http.StatusNotFound {
			t.Errorf("bob %s = %d %s, want 404", tc.name, code, body)
		}
		if code, _ := portalWebhookCall(tc.fn, tc.meth, "", "/api/v1/portal/webhooks/"+id, `{}`, params); code != http.StatusUnauthorized {
			t.Errorf("anon %s = %d, want 401", tc.name, code)
		}
	}
	// A missing id is a 404 too — no existence oracle.
	if code, _ := portalWebhookCall(h.Delete, http.MethodDelete, "alice", "/api/v1/portal/webhooks/nope",
		`{}`, gin.Params{{Key: "id", Value: "nope"}}); code != http.StatusNotFound {
		t.Errorf("delete missing = %d, want 404", code)
	}
	if len(doer.requests) != 0 {
		t.Error("cross-user access must not trigger any delivery")
	}
}

// PATCH applies only the fields present.
func TestPortalWebhookPatchPartial(t *testing.T) {
	h, _, _ := newPortalWebhookHarness()
	id, _ := createOne(t, h, "alice", `{"url":"https://example.com/hook","events":["license.created"],"active":true}`)
	params := gin.Params{{Key: "id", Value: id}}

	// Flip active only; url and events must survive.
	code, resp := portalWebhookCall(h.Update, http.MethodPatch, "alice", "/api/v1/portal/webhooks/"+id,
		`{"active":false}`, params)
	if code != http.StatusOK {
		t.Fatalf("patch = %d %s", code, resp)
	}
	var row struct {
		Data struct {
			Webhook struct {
				URL    string   `json:"url"`
				Events []string `json:"events"`
				Active bool     `json:"active"`
			} `json:"webhook"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(resp), &row); err != nil {
		t.Fatalf("parse patch: %v (%s)", err, resp)
	}
	got := row.Data.Webhook
	if got.URL != "https://example.com/hook" || len(got.Events) != 1 || got.Events[0] != "license.created" || got.Active {
		t.Errorf("after active-only patch = %+v, want url+events preserved, active=false", got)
	}

	// Change events only; url and active must survive.
	code, resp = portalWebhookCall(h.Update, http.MethodPatch, "alice", "/api/v1/portal/webhooks/"+id,
		`{"events":["seat.added","seat.removed"]}`, params)
	if code != http.StatusOK {
		t.Fatalf("patch = %d %s", code, resp)
	}
	if err := json.Unmarshal([]byte(resp), &row); err != nil {
		t.Fatalf("parse patch: %v (%s)", err, resp)
	}
	got = row.Data.Webhook
	if got.URL != "https://example.com/hook" || len(got.Events) != 2 || got.Active {
		t.Errorf("after events-only patch = %+v, want url+active preserved, 2 events", got)
	}

	// Patch validation: an empty events array is refused.
	if code, _ := portalWebhookCall(h.Update, http.MethodPatch, "alice", "/api/v1/portal/webhooks/"+id,
		`{"events":[]}`, params); code != http.StatusBadRequest {
		t.Error("patch with empty events = not 400")
	}
}

// The test fire signs the body with the endpoint's own secret and sends
// the merchant-identical headers.
func TestPortalWebhookTestDispatchSigningShape(t *testing.T) {
	h, st, doer := newPortalWebhookHarness()
	id, secret := createOne(t, h, "alice", `{"url":"https://example.com/hook","events":["license.created"]}`)

	code, resp := portalWebhookCall(h.DispatchTest, http.MethodPost, "alice",
		"/api/v1/portal/webhooks/"+id+"/test", "", gin.Params{{Key: "id", Value: id}})
	if code != http.StatusOK {
		t.Fatalf("test = %d %s", code, resp)
	}
	if !strings.Contains(resp, `"accepted":true`) {
		t.Errorf("test response = %s, want accepted:true", resp)
	}
	if strings.Contains(resp, secret) {
		t.Error("test response leaked the signing secret")
	}
	if len(doer.requests) != 1 {
		t.Fatalf("expected 1 delivery, got %d", len(doer.requests))
	}
	req := doer.requests[0]
	body := doer.bodies[0]

	if req.Method != http.MethodPost {
		t.Errorf("method = %s, want POST", req.Method)
	}
	if got := req.Header.Get("X-HiTechCloud-Event"); got != "webhook.test" {
		t.Errorf("event header = %q, want webhook.test", got)
	}
	if req.Header.Get("X-HiTechCloud-Delivery") == "" {
		t.Error("missing X-HiTechCloud-Delivery")
	}

	// Signature is "sha256=" + hex(HMAC-SHA256(body, secret)).
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got := req.Header.Get("X-HiTechCloud-Signature"); got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}

	// Body is {"event","timestamp","data"}.
	var payload struct {
		Event     string         `json:"event"`
		Timestamp string         `json:"timestamp"`
		Data      map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("parse delivery body: %v (%s)", err, body)
	}
	if payload.Event != "webhook.test" || payload.Timestamp == "" || payload.Data == nil {
		t.Errorf("payload = %+v, want event/timestamp/data", payload)
	}
	if payload.Data["webhook_id"] != id {
		t.Errorf("payload data.webhook_id = %v, want %s", payload.Data["webhook_id"], id)
	}

	// last_delivery_at was stamped.
	if st.hooks[0].LastDeliveryAt == nil {
		t.Error("test dispatch must stamp last_delivery_at")
	}
}

// A failed test fire reports accepted:false (and still never leaks the
// secret).
func TestPortalWebhookTestDispatchRejected(t *testing.T) {
	h, _, doer := newPortalWebhookHarness()
	doer.status = 500
	id, secret := createOne(t, h, "alice", `{"url":"https://example.com/hook","events":["license.created"]}`)
	_, resp := portalWebhookCall(h.DispatchTest, http.MethodPost, "alice",
		"/api/v1/portal/webhooks/"+id+"/test", "", gin.Params{{Key: "id", Value: id}})
	if !strings.Contains(resp, `"accepted":false`) {
		t.Errorf("test response = %s, want accepted:false", resp)
	}
	if strings.Contains(resp, secret) {
		t.Error("test response leaked the signing secret")
	}
}

// DispatchCustomerEvent fans out to active subscribers and signs each
// with that endpoint's own secret.
func TestPortalWebhookDispatchCustomerEvent(t *testing.T) {
	h, _, doer := newPortalWebhookHarness()
	_, secret := createOne(t, h, "alice", `{"url":"https://example.com/a","events":["license.created"]}`)
	createOne(t, h, "bob", `{"url":"https://example.com/b","events":["license.created"]}`)
	// An endpoint not subscribed, and an inactive one, must be skipped.
	createOne(t, h, "carol", `{"url":"https://example.com/c","events":["seat.added"]}`)
	createOne(t, h, "dave", `{"url":"https://example.com/d","events":["license.created"],"active":false}`)

	if err := h.DispatchCustomerEvent(context.Background(), "license.created", map[string]any{"license_id": "L1"}); err != nil {
		t.Fatalf("DispatchCustomerEvent: %v", err)
	}
	if len(doer.requests) != 2 {
		t.Fatalf("expected 2 deliveries, got %d", len(doer.requests))
	}
	// The first endpoint is signed with alice's secret.
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(doer.bodies[0])
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if got := doer.requests[0].Header.Get("X-HiTechCloud-Signature"); got != want {
		t.Errorf("signature = %q, want %q", got, want)
	}
	// The payload wraps data under "data".
	var payload struct {
		Event string         `json:"event"`
		Data  map[string]any `json:"data"`
	}
	_ = json.Unmarshal(doer.bodies[0], &payload)
	if payload.Event != "license.created" || payload.Data["license_id"] != "L1" {
		t.Errorf("payload = %+v, want license.created / license_id=L1", payload)
	}
}
