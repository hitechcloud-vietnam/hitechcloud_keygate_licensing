package handler

// §61 SECURITY hardening — regression suite for the auth and request
// boundaries, DB-free via the handler seams. Existing coverage is NOT
// duplicated (the idempotency 409s live in middleware/idempotency_test.go,
// the merchant SSRF dialer in service/webhook_ssrf_test.go, the SQL
// sort-whitelist in TestMarketplaceSortWhitelist, SSO signatures in
// internal/sso/*_test.go); this file adds the orthogonal vectors:
// IDOR/cross-tenant 404 sweeps, webhook URL policy (incl. cloud
// metadata + CGNAT), delivery-time SSRF on the customer path, signature
// spoofing, and secret/key leakage through responses and JSON.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ── helpers ──────────────────────────────────────────────────────────

// secCall drives a handler with a session context carrying both the
// user id (portal webhooks, notifications) and the email (reviews).
func secCall(fn func(*gin.Context), method, userID, email, path, body string, params gin.Params) (int, string) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	c.Set("user_id", userID)
	c.Set("email", email)
	fn(c)
	return w.Code, w.Body.String()
}

func assertSecQuiet404(t *testing.T, code int, body string) {
	t.Helper()
	if code != http.StatusNotFound || !strings.Contains(body, `"code":"NOT_FOUND"`) {
		t.Errorf("want quiet 404 NOT_FOUND (no existence oracle), got %d\n%s", code, body)
	}
}

func assertSec401(t *testing.T, code int, body string) {
	t.Helper()
	if code != http.StatusUnauthorized || !strings.Contains(body, `"code":"UNAUTHORIZED"`) {
		t.Errorf("want 401 UNAUTHORIZED, got %d\n%s", code, body)
	}
}

// secNotifStore fakes the notification seam with ownership-scoped
// writes: a foreign (id, userID) pair is sql.ErrNoRows, exactly like
// the real MarkRead.
type secNotifStore struct {
	owner    map[string]string // notification id -> owner user id
	attempts []string          // "<id> as <userID>" for every MarkRead call
}

func (f *secNotifStore) ListUserNotifications(_ context.Context, userID string, _ bool, _ store.Page) ([]*model.UserNotification, int, error) {
	var out []*model.UserNotification
	for id, uid := range f.owner {
		if uid == userID {
			out = append(out, &model.UserNotification{ID: id, UserID: uid})
		}
	}
	return out, len(out), nil
}

func (f *secNotifStore) CountUnread(_ context.Context, _ string) (int, error) { return 0, nil }

func (f *secNotifStore) MarkRead(_ context.Context, id, userID string) error {
	f.attempts = append(f.attempts, id+" as "+userID)
	if f.owner[id] != userID {
		return sql.ErrNoRows
	}
	return nil
}

func (f *secNotifStore) MarkAllRead(_ context.Context, _ string) (int, error) { return 0, nil }

// secReviewStore fakes the review seam: a review is only visible to the
// (product, email) pair that wrote it.
type secReviewStore struct {
	review  *model.Review // the one row that exists: alice's on prod_1
	deletes []string
	updates []string
	creates int
}

func (f *secReviewStore) FindProductByID(_ context.Context, id string) (*model.Product, error) {
	if id != "prod_1" {
		return nil, sql.ErrNoRows
	}
	return &model.Product{ID: "prod_1"}, nil
}

func (f *secReviewStore) FindProductBySlug(_ context.Context, _ string) (*model.Product, error) {
	return nil, sql.ErrNoRows
}

func (f *secReviewStore) CreateReview(_ context.Context, _ *model.Review) error {
	f.creates++
	return nil
}

func (f *secReviewStore) FindReviewByProductAndEmail(_ context.Context, productID, email string) (*model.Review, error) {
	if f.review != nil && f.review.ProductID == productID && f.review.CustomerEmail == email {
		return f.review, nil
	}
	return nil, sql.ErrNoRows
}

func (f *secReviewStore) UpdateReviewContent(_ context.Context, id, _, _ string) error {
	f.updates = append(f.updates, id)
	return nil
}

func (f *secReviewStore) DeleteReview(_ context.Context, id string) error {
	f.deletes = append(f.deletes, id)
	return nil
}

// ── IDOR / cross-tenant 404 sweep ────────────────────────────────────

// TestPortalIDOR404Sweep is the ownership boundary across the portal
// surface: EVERY mutating endpoint must answer a cross-user id with the
// same quiet 404 a missing row gets (no existence oracle), and must
// leave no side effect behind. Covers 3+ portal resources.
func TestPortalIDOR404Sweep(t *testing.T) {
	const (
		alice    = "alice_user"
		bob      = "bob_user"
		aliceM   = "alice@example.com"
		bobM     = "bob@example.com"
		webhooks = "portal webhooks"
		notifs   = "portal notifications"
		reviews  = "portal reviews"
	)

	t.Run(webhooks, func(t *testing.T) {
		h, st, doer := newPortalWebhookHarness()
		id, _ := createOne(t, h, alice, `{"url":"https://example.com/hook","events":["license.created"]}`)
		auditsBefore := len(st.audits) // the creation's own audit rows
		updates := []struct {
			name string
			fn   func(*gin.Context)
			body string
		}{
			{"PATCH", h.Update, `{"url":"https://evil.example.com/hook"}`},
			{"DELETE", h.Delete, ""},
			{"test", h.DispatchTest, ""},
			{"rotate", h.Rotate, ""},
		}
		for _, u := range updates {
			code, body := secCall(u.fn, http.MethodPost, bob, bobM, "/x", u.body, gin.Params{{Key: "id", Value: id}})
			assertSecQuiet404(t, code, body)
			code, body = secCall(u.fn, http.MethodPost, "", "", "/x", u.body, gin.Params{{Key: "id", Value: id}})
			assertSec401(t, code, body)
		}
		if len(doer.requests) != 0 {
			t.Error("an attacker's test/rotate must never dispatch a delivery")
		}
		if len(st.rotates) != 0 {
			t.Error("an attacker must never rotate somebody else's secret")
		}
		if len(st.hooks) != 1 || st.hooks[0].URL != "https://example.com/hook" {
			t.Error("an attacker must never mutate somebody else's row")
		}
		if len(st.audits) != auditsBefore {
			t.Error("refused calls must not write audit rows")
		}
	})

	t.Run(notifs, func(t *testing.T) {
		st := &secNotifStore{owner: map[string]string{"notif_1": alice}}
		h := &NotificationCenterHandler{store: st}
		code, body := secCall(h.MarkRead, http.MethodPost, bob, bobM, "/x", "", gin.Params{{Key: "id", Value: "notif_1"}})
		assertSecQuiet404(t, code, body)
		code, body = secCall(h.MarkRead, http.MethodPost, "", "", "/x", "", gin.Params{{Key: "id", Value: "notif_1"}})
		assertSec401(t, code, body)
		if st.owner["notif_1"] != alice {
			t.Error("the victim's notification must be untouched")
		}
	})

	t.Run(reviews, func(t *testing.T) {
		st := &secReviewStore{review: &model.Review{
			ID: "rev_1", ProductID: "prod_1", CustomerEmail: aliceM,
			Rating: 5, Body: "mine", Status: model.ReviewStatusApproved,
		}}
		h := &ReviewPortalHandler{store: st}
		params := gin.Params{{Key: "id", Value: "prod_1"}}
		code, body := secCall(h.Update, http.MethodPatch, bob, bobM, "/x", `{"body":"hijacked"}`, params)
		assertSecQuiet404(t, code, body)
		code, body = secCall(h.Delete, http.MethodDelete, bob, bobM, "/x", "", params)
		assertSecQuiet404(t, code, body)
		code, body = secCall(h.Update, http.MethodPatch, "", "", "/x", `{"body":"hijacked"}`, params)
		assertSec401(t, code, body)
		if len(st.updates) != 0 || len(st.deletes) != 0 {
			t.Error("an attacker must never edit or delete somebody else's review")
		}
		if st.review.Body != "mine" {
			t.Error("the victim's review must be untouched")
		}
	})
}

// ── SSRF: webhook URL policy ─────────────────────────────────────────

// TestWebhookTargetPolicyBlocksInternalAndMetadata pins the WRITE-time
// policy: a webhook may never be created or updated pointing at
// loopback, private space, cloud metadata (169.254.169.254 and the
// CGNAT-hosted 100.100.100.100), multicast, or IPv6-embedding
// transition ranges — the classes the std-lib-only model filter misses.
// The regression this nails shut: http://100.100.100.100/ used to pass.
func TestWebhookTargetPolicyBlocksInternalAndMetadata(t *testing.T) {
	h, _, _ := newPortalWebhookHarness()
	blocked := []string{
		"http://127.0.0.1:8080/hook",
		"http://[::1]/hook",
		"http://localhost/hook",
		"http://10.0.0.5/hook",
		"http://172.16.0.1/hook",
		"http://192.168.1.1/hook",
		"http://169.254.169.254/latest/meta-data/", // cloud metadata (link-local)
		"http://100.100.100.100/latest/meta-data/", // cloud metadata (CGNAT) — the gap fixed here
		"http://100.64.3.9/hook",                   // CGNAT internal
		"http://0.0.0.0/hook",
		"http://[fd12:3456::1]/hook",    // ULA
		"http://[fe80::1]/hook",         // link-local v6
		"http://[::ffff:10.0.0.1]/hook", // v4-mapped private
		"http://224.0.0.1/hook",         // multicast
		"http://[64:ff9b::a00:1]/hook",  // NAT64-embedded 10.0.0.1
		"file:///etc/passwd",
		"ftp://example.com/x",
	}
	for _, u := range blocked {
		body := `{"url":"` + u + `","events":["license.created"]}`
		code, got := secCall(h.Create, http.MethodPost, "alice", "alice@example.com", "/x", body, nil)
		if code != http.StatusBadRequest {
			t.Errorf("create %s: want 400, got %d\n%s", u, code, got)
		}
	}

	// A public endpoint still works (the policy is not a blanket ban).
	id, _ := createOne(t, h, "alice", `{"url":"https://hooks.example.com/x","events":["license.created"]}`)

	// The update path enforces the same policy.
	body := `{"url":"http://100.100.100.100/latest/meta-data/"}`
	code, got := secCall(h.Update, http.MethodPatch, "alice", "alice@example.com", "/x", body, gin.Params{{Key: "id", Value: id}})
	if code != http.StatusBadRequest {
		t.Errorf("update: want 400, got %d\n%s", code, got)
	}
}

// TestCustomerWebhookDispatchRefusesInternalTargets pins the
// DELIVERY-time policy: rows saved before a policy tightening (or
// pointing at a name whose DNS moved) must still refuse to leave. The
// refusal happens BEFORE the transport is touched — a SSRF blind spot
// at dispatch is exactly what a rebinding attack needs.
func TestCustomerWebhookDispatchRefusesInternalTargets(t *testing.T) {
	t.Run("legacy literal metadata row", func(t *testing.T) {
		h, st, doer := newPortalWebhookHarness()
		st.hooks = append(st.hooks, &model.CustomerWebhook{
			ID: "wh_legacy", UserID: "alice",
			URL:    "http://100.100.100.100/latest/meta-data/",
			Events: []string{"order.created"}, Active: true,
			Secret: "whsec_legacy0000", SecretPrefix: "whsec_le",
		})
		code, body := secCall(h.DispatchTest, http.MethodPost, "alice", "alice@example.com", "/x", "",
			gin.Params{{Key: "id", Value: "wh_legacy"}})
		if code != http.StatusOK || !strings.Contains(body, `"accepted":false`) {
			t.Fatalf("want 200 with accepted:false, got %d\n%s", code, body)
		}
		if len(doer.requests) != 0 {
			t.Error("the dispatch must be refused BEFORE the transport is touched")
		}
	})

	t.Run("DNS rebinding: public name resolving internal", func(t *testing.T) {
		h, st, doer := newPortalWebhookHarness()
		h.lookup = func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil
		}
		st.hooks = append(st.hooks, &model.CustomerWebhook{
			ID: "wh_rebind", UserID: "alice",
			URL:    "https://rebind.example.com/hook",
			Events: []string{"order.created"}, Active: true,
			Secret: "whsec_rebind0000", SecretPrefix: "whsec_re",
		})
		code, body := secCall(h.DispatchTest, http.MethodPost, "alice", "alice@example.com", "/x", "",
			gin.Params{{Key: "id", Value: "wh_rebind"}})
		if code != http.StatusOK || !strings.Contains(body, `"accepted":false`) {
			t.Fatalf("want 200 with accepted:false, got %d\n%s", code, body)
		}
		if len(doer.requests) != 0 {
			t.Error("a name that resolves into metadata space must never be dialed")
		}
	})

	t.Run("unresolvable target fails closed", func(t *testing.T) {
		h, st, doer := newPortalWebhookHarness()
		h.lookup = func(context.Context, string) ([]net.IPAddr, error) {
			return nil, sql.ErrNoRows
		}
		st.hooks = append(st.hooks, &model.CustomerWebhook{
			ID: "wh_nx", UserID: "alice",
			URL:    "https://nx.example.com/hook",
			Events: []string{"order.created"}, Active: true,
			Secret: "whsec_nxdomain00", SecretPrefix: "whsec_nx",
		})
		code, body := secCall(h.DispatchTest, http.MethodPost, "alice", "alice@example.com", "/x", "",
			gin.Params{{Key: "id", Value: "wh_nx"}})
		if code != http.StatusOK || !strings.Contains(body, `"accepted":false`) {
			t.Fatalf("want 200 with accepted:false, got %d\n%s", code, body)
		}
		if len(doer.requests) != 0 {
			t.Error("\"could not classify\" must fail closed, never dial")
		}
	})

	t.Run("public name still delivers", func(t *testing.T) {
		h, _, doer := newPortalWebhookHarness()
		h.lookup = func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		}
		id, _ := createOne(t, h, "alice", `{"url":"https://hooks.example.com/x","events":["license.created"]}`)
		code, body := secCall(h.DispatchTest, http.MethodPost, "alice", "alice@example.com", "/x", "",
			gin.Params{{Key: "id", Value: id}})
		if code != http.StatusOK || !strings.Contains(body, `"accepted":true`) {
			t.Fatalf("a public target must still deliver, got %d\n%s", code, body)
		}
		if len(doer.requests) != 1 {
			t.Errorf("want 1 dispatch, got %d", len(doer.requests))
		}
	})
}

// ── webhook spoofing ─────────────────────────────────────────────────

// TestWebhookSignatureCannotBeSpoofed pins the signature contract: only
// the holder of the current secret can produce a valid
// X-HiTechCloud-Signature; a tampered body or a wrong secret never
// verifies; and after rotation the OLD secret is dead immediately.
func TestWebhookSignatureCannotBeSpoofed(t *testing.T) {
	h, _, doer := newPortalWebhookHarness()
	id, secret := createOne(t, h, "alice", `{"url":"https://example.com/hook","events":["license.created"]}`)
	secCall(h.DispatchTest, http.MethodPost, "alice", "alice@example.com", "/x", "",
		gin.Params{{Key: "id", Value: id}})
	if len(doer.requests) != 1 {
		t.Fatalf("want 1 dispatch, got %d", len(doer.requests))
	}
	sig := doer.requests[0].Header.Get("X-HiTechCloud-Signature")
	body := doer.bodies[0]
	verifies := func(b []byte, sec string) bool {
		return sig == "sha256="+hmacSHA256Hex(b, sec)
	}

	if !verifies(body, secret) {
		t.Fatal("the genuine signature must verify with the real secret")
	}
	tampered := append(append([]byte{}, body...), ' ')
	if verifies(tampered, secret) {
		t.Error("a TAMPERED body must not verify against the captured signature")
	}
	if verifies(body, "whsec_not-the-real-secret") {
		t.Error("a WRONG secret must not produce a verifying signature")
	}

	// Rotation kills the old secret on the very next dispatch.
	_, rotBody := secCall(h.Rotate, http.MethodPost, "alice", "alice@example.com", "/x", "",
		gin.Params{{Key: "id", Value: id}})
	rot := replayData(t, rotBody)
	rotSecret, _ := rot["secret"].(string)
	if rotSecret == "" || rotSecret == secret {
		t.Fatal("rotate must return a fresh secret")
	}
	secCall(h.DispatchTest, http.MethodPost, "alice", "alice@example.com", "/x", "",
		gin.Params{{Key: "id", Value: id}})
	if len(doer.requests) != 2 {
		t.Fatalf("want 2 dispatches, got %d", len(doer.requests))
	}
	sig2, body2 := doer.requests[1].Header.Get("X-HiTechCloud-Signature"), doer.bodies[1]
	if sig2 == sig {
		t.Error("the post-rotation dispatch must carry a NEW signature")
	}
	if sig2 != "sha256="+hmacSHA256Hex(body2, rotSecret) {
		t.Error("the post-rotation dispatch must verify with the NEW secret")
	}
	if sig2 == "sha256="+hmacSHA256Hex(body2, secret) {
		t.Error("the SUPERSEDED secret must stop signing immediately")
	}
}

// ── secret / key leakage ─────────────────────────────────────────────

// TestSecretsNeverSerialize is the marshaling leak pin: the secret-
// bearing models must never emit their secret/hash fields through
// json.Marshal, whatever handler forgets to project a view type. This
// is the marketplace "no-leak" pattern applied to every credential
// model in the system.
func TestSecretsNeverSerialize(t *testing.T) {
	oidcSecret := "OIDC_CLIENT_SECRET_VAL"
	samples := map[string]any{
		"Webhook":         model.Webhook{ID: "w", Secret: "MERCHANT_SECRET_VAL"},
		"CustomerWebhook": model.CustomerWebhook{ID: "c", Secret: "CUSTOMER_SECRET_VAL", SecretPrefix: "whsec_CU", UserID: "U_HIDDEN_VAL"},
		"CustomerAPIKey":  model.CustomerAPIKey{ID: "ck", KeyHash: "CK_HASH_VAL"},
		"APIKey":          model.APIKey{ID: "ak", KeyHash: "AK_HASH_VAL"},
		"SSOConnection":   model.SSOConnection{ID: "s", OIDCClientSecret: &oidcSecret},
		"SCIMToken":       model.SCIMToken{TokenHash: "SCIM_HASH_VAL"},
		"UserNotification": model.UserNotification{ID: "n", UserID: "U_HIDDEN_VAL",
			Event: "order.created", TitleKey: "order.created"},
	}
	planted := []string{
		"MERCHANT_SECRET_VAL", "CUSTOMER_SECRET_VAL", "CK_HASH_VAL", "AK_HASH_VAL",
		"OIDC_CLIENT_SECRET_VAL", "SCIM_HASH_VAL", "U_HIDDEN_VAL",
	}
	for name, sample := range samples {
		raw, err := json.Marshal(sample)
		if err != nil {
			t.Fatalf("%s: marshal failed: %v", name, err)
		}
		for _, secret := range planted {
			if strings.Contains(string(raw), secret) {
				t.Errorf("%s leaked a planted secret through json.Marshal: %s", name, raw)
			}
		}
		for _, key := range []string{`"key_hash"`, `"oidc_client_secret"`, `"token_hash"`, `"secret":`} {
			if strings.Contains(string(raw), key) {
				t.Errorf("%s serializes the sensitive key %s: %s", name, key, raw)
			}
		}
	}
}

// TestPortalWebhookResponsesNeverLeakSecretOrSealedValue is the
// endpoint-level leak pin: the customer secret appears ONCE — in the
// create and rotate answers — and in no list, update, or test response;
// and no response ever carries a sealed (enc:v1:) value.
func TestPortalWebhookResponsesNeverLeakSecretOrSealedValue(t *testing.T) {
	h, _, doer := newPortalWebhookHarness()
	id, secret := createOne(t, h, "alice", `{"url":"https://example.com/hook","events":["license.created"]}`)

	_, listBody := secCall(h.List, http.MethodGet, "alice", "alice@example.com", "/x", "", nil)
	_, updateBody := secCall(h.Update, http.MethodPatch, "alice", "alice@example.com", "/x",
		`{"url":"https://example.com/hook2"}`, gin.Params{{Key: "id", Value: id}})
	_, testBody := secCall(h.DispatchTest, http.MethodPost, "alice", "alice@example.com", "/x", "",
		gin.Params{{Key: "id", Value: id}})
	_, rotBody := secCall(h.Rotate, http.MethodPost, "alice", "alice@example.com", "/x", "",
		gin.Params{{Key: "id", Value: id}})
	_, list2Body := secCall(h.List, http.MethodGet, "alice", "alice@example.com", "/x", "", nil)
	_ = doer

	rot := replayData(t, rotBody)
	rotSecret, _ := rot["secret"].(string)
	if rotSecret == "" || rotSecret == secret {
		t.Fatal("rotate must show a fresh secret exactly once")
	}

	for name, body := range map[string]string{"list": listBody, "update": updateBody, "test": testBody, "list after rotate": list2Body} {
		if strings.Contains(body, secret) || strings.Contains(body, rotSecret) {
			t.Errorf("%s response leaked the signing secret: %s", name, body)
		}
		if strings.Contains(body, "enc:v1:") {
			t.Errorf("%s response leaked a sealed value: %s", name, body)
		}
	}
	if strings.Contains(rotBody, secret) {
		t.Errorf("the rotate answer must never show the SUPERSEDED secret: %s", rotBody)
	}
	if !strings.Contains(rotBody, rotSecret) {
		t.Errorf("the rotate answer must show the new secret exactly once: %s", rotBody)
	}
}
