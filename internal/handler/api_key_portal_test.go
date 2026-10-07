package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// The normalizer is the one place create validates; it needs no
// database and every refusal below is a 400 the portal form acts on.
func TestNormalizeCustomerAPIKey(t *testing.T) {
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	for _, tc := range []struct {
		name string
		key  model.CustomerAPIKey
		want string // normalized scopes
	}{
		{"plain", model.CustomerAPIKey{Name: "key", Scopes: "orders:read"}, "orders:read"},
		{"name trimmed", model.CustomerAPIKey{Name: "  CI  ", Scopes: "a"}, "a"},
		{"scopes folded", model.CustomerAPIKey{Name: "key", Scopes: " a , b ,, "}, "a,b"},
		{"no scopes", model.CustomerAPIKey{Name: "key"}, ""},
		{"future expiry", model.CustomerAPIKey{Name: "key", ExpiresAt: &future}, ""},
	} {
		k := tc.key
		if err := normalizeCustomerAPIKey(&k, now); err != nil {
			t.Errorf("%s: normalizeCustomerAPIKey = %v, want nil", tc.name, err)
			continue
		}
		if k.Scopes != tc.want {
			t.Errorf("%s: scopes stored as %q, want %q", tc.name, k.Scopes, tc.want)
		}
	}
	if k := (model.CustomerAPIKey{Name: "  CI  "}); func() bool {
		_ = normalizeCustomerAPIKey(&k, now)
		return k.Name != "CI"
	}() {
		t.Errorf("name not trimmed: %q", k.Name)
	}

	for _, tc := range []struct {
		name    string
		key     model.CustomerAPIKey
		wantErr string
	}{
		{"name missing", model.CustomerAPIKey{Scopes: "a"}, "name is required"},
		{"name blank", model.CustomerAPIKey{Name: "   "}, "name is required"},
		{"name too long", model.CustomerAPIKey{Name: strings.Repeat("x", 201)}, "at most 200 characters"},
		{"expiry in the past", model.CustomerAPIKey{Name: "key", ExpiresAt: &past}, "expires_at must be in the future"},
		{"expiry exactly now", model.CustomerAPIKey{Name: "key", ExpiresAt: &now}, "expires_at must be in the future"},
	} {
		k := tc.key
		err := normalizeCustomerAPIKey(&k, now)
		if err == nil {
			t.Errorf("%s: normalizeCustomerAPIKey = nil, want a refusal", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: error = %q, want it to mention %q", tc.name, err.Error(), tc.wantErr)
		}
	}
}

// The full portal flow against a real database: create (secret shown
// exactly once), list scoped to the session user, get/revoke with
// cross-user access answering the same 404 as a missing key (no
// IDOR), and an unauthenticated call answered 401.
func TestAPIKeyPortalHandlerFlow(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := store.New(dsn)
	if err != nil {
		t.Skipf("skipping integration test: %v", err)
	}
	defer s.Close()
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	gin.SetMode(gin.TestMode)

	ctx := context.Background()
	mkUser := func() string {
		id := store.NewID()
		if _, err := s.DB.NewRaw(
			"INSERT INTO users (id, email, name) VALUES (?, ?, ?)",
			id, "portalkeys-"+id+"@example.com", "Portal Keys",
		).Exec(ctx); err != nil {
			t.Fatalf("create user: %v", err)
		}
		return id
	}
	alice, bob := mkUser(), mkUser()
	defer func() {
		for _, id := range []string{alice, bob} {
			_, _ = s.DB.NewRaw("DELETE FROM customer_api_keys WHERE user_id = ?", id).Exec(ctx)
			_, _ = s.DB.NewRaw("DELETE FROM users WHERE id = ?", id).Exec(ctx)
		}
	}()

	h := NewAPIKeyPortalHandler(s)

	call := func(fn func(*gin.Context), method, userID, path, body string, params gin.Params) (int, string) {
		t.Helper()
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
	create := func(userID, body string) (int, string) {
		return call(h.Create, http.MethodPost, userID, "/api/v1/portal/api-keys", body, nil)
	}
	list := func(userID string) (int, string) {
		return call(h.List, http.MethodGet, userID, "/api/v1/portal/api-keys", "", nil)
	}
	get := func(userID, id string) (int, string) {
		return call(h.Get, http.MethodGet, userID, "/api/v1/portal/api-keys/"+id, "", gin.Params{{Key: "id", Value: id}})
	}
	revoke := func(userID, id string) (int, string) {
		return call(h.Revoke, http.MethodDelete, userID, "/api/v1/portal/api-keys/"+id, "", gin.Params{{Key: "id", Value: id}})
	}

	// ── create: the secret comes back exactly once ──
	code, body := create(alice, `{"name":"CI runner","scopes":" orders:read , usage:read ","expires_at":null}`)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, body)
	}
	var created struct {
		Success bool `json:"success"`
		Data    struct {
			APIKey *struct {
				ID        string  `json:"id"`
				UserID    string  `json:"user_id"`
				Name      string  `json:"name"`
				KeyPrefix string  `json:"key_prefix"`
				Scopes    string  `json:"scopes"`
				RevokedAt *string `json:"revoked_at"`
			} `json:"api_key"`
			Secret string `json:"secret"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("parse create answer: %v (%s)", err, body)
	}
	if created.Data.APIKey == nil || created.Data.APIKey.ID == "" {
		t.Fatalf("create returned no key: %s", body)
	}
	key := created.Data.APIKey
	secret := created.Data.Secret
	if !strings.HasPrefix(secret, model.CustomerAPIKeySecretPrefix) {
		t.Errorf("secret %q does not carry the htc_sk_ prefix", secret)
	}
	if key.UserID != alice || key.Name != "CI runner" || key.Scopes != "orders:read,usage:read" {
		t.Errorf("stored key = %+v, want alice / CI runner / folded scopes", key)
	}
	if len(key.KeyPrefix) != model.CustomerAPIKeyPrefixLength || !strings.HasPrefix(secret, key.KeyPrefix) {
		t.Errorf("key_prefix %q is not the display prefix of the secret", key.KeyPrefix)
	}
	// The hash must never ride along, even in the one answer that
	// carries the secret.
	if strings.Contains(body, store.HashAPIKey(secret)) {
		t.Error("the create answer leaked the key hash")
	}

	// ── validation refusals ──
	if code, _ := create(alice, `{"scopes":"a"}`); code != http.StatusBadRequest {
		t.Errorf("create without name = %d, want 400", code)
	}
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if code, _ := create(alice, `{"name":"old","expires_at":"`+past+`"}`); code != http.StatusBadRequest {
		t.Errorf("create with a past expiry = %d, want 400", code)
	}
	// Unauthenticated: the identity gate, whichever way the request
	// reached the handler.
	if code, _ := create("", `{"name":"anon"}`); code != http.StatusUnauthorized {
		t.Errorf("create without a session = %d, want 401", code)
	}

	// ── list: only the caller's own keys ──
	code, body = create(bob, `{"name":"bob key"}`)
	if code != http.StatusCreated {
		t.Fatalf("create for bob = %d %s", code, body)
	}
	code, body = list(alice)
	if code != http.StatusOK {
		t.Fatalf("list = %d %s", code, body)
	}
	var listed struct {
		Data struct {
			APIKeys []struct {
				ID     string `json:"id"`
				UserID string `json:"user_id"`
			} `json:"api_keys"`
			Total int `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &listed); err != nil {
		t.Fatalf("parse list answer: %v (%s)", err, body)
	}
	if listed.Data.Total != 1 || len(listed.Data.APIKeys) != 1 {
		t.Fatalf("alice list = %d keys (total %d), want 1/1: %s", len(listed.Data.APIKeys), listed.Data.Total, body)
	}
	if listed.Data.APIKeys[0].ID != key.ID || listed.Data.APIKeys[0].UserID != alice {
		t.Errorf("alice's list holds %s of %s, want %s of %s", listed.Data.APIKeys[0].ID, listed.Data.APIKeys[0].UserID, key.ID, alice)
	}

	// ── get: own key yes, someone else's is a 404 like a missing one ──
	code, body = get(alice, key.ID)
	if code != http.StatusOK {
		t.Fatalf("get own = %d %s", code, body)
	}
	if strings.Contains(body, secret) {
		t.Error("the get answer leaked the secret — it must be shown exactly once")
	}
	if code, _ := get(bob, key.ID); code != http.StatusNotFound {
		t.Errorf("get someone else's key = %d, want 404", code)
	}
	if code, _ := get(alice, "no-such-key"); code != http.StatusNotFound {
		t.Errorf("get a missing key = %d, want 404", code)
	}
	if code, _ := get("", key.ID); code != http.StatusUnauthorized {
		t.Errorf("get without a session = %d, want 401", code)
	}

	// ── revoke: soft, idempotent, and never someone else's ──
	code, body = revoke(bob, key.ID)
	if code != http.StatusNotFound {
		t.Errorf("revoke someone else's key = %d, want 404", code)
	}
	code, body = revoke(alice, key.ID)
	if code != http.StatusOK {
		t.Fatalf("revoke = %d %s", code, body)
	}
	var revoked struct {
		Data struct {
			ID        string  `json:"id"`
			RevokedAt *string `json:"revoked_at"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &revoked); err != nil {
		t.Fatalf("parse revoke answer: %v (%s)", err, body)
	}
	if revoked.Data.ID != key.ID || revoked.Data.RevokedAt == nil {
		t.Errorf("revoke did not stamp revoked_at: %s", body)
	}
	// Idempotent second revoke still answers 200 with the same row.
	if code, _ = revoke(alice, key.ID); code != http.StatusOK {
		t.Errorf("second revoke = %d, want 200", code)
	}
	// The revoked key stops authenticating at once.
	if _, err := s.FindCustomerAPIKeyByHash(ctx, store.HashAPIKey(secret)); err == nil {
		t.Error("a revoked key still validates")
	}
}
