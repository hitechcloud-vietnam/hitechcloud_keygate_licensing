package middleware

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// Customer API-key auth tests run against a FAKE store on purpose:
// the middleware's contract is "what does it refuse, what does it put
// in the context, when does it write last_used_at", and every one of
// those is observable at the interface it consumes. No test database
// is needed, so these always run.

type fakeCustomerKeyStore struct {
	keysByHash map[string]*model.CustomerAPIKey
	users      map[string]*model.User
	touches    map[string]int
	// permissive makes FindCustomerAPIKeyByHash return rows whatever
	// their state — the real store filters revoked/expired in SQL, and
	// this stands in for a lookup that (against its own contract)
	// hands the middleware a dead row anyway. The middleware must
	// still refuse it.
	permissive bool
}

func newFakeCustomerKeyStore() *fakeCustomerKeyStore {
	return &fakeCustomerKeyStore{
		keysByHash: map[string]*model.CustomerAPIKey{},
		users:      map[string]*model.User{},
		touches:    map[string]int{},
	}
}

// find mirrors store.FindCustomerAPIKeyByHash's contract: only usable
// rows resolve; revoked and expired rows are sql.ErrNoRows, exactly
// like unknown hashes.
func (f *fakeCustomerKeyStore) FindCustomerAPIKeyByHash(_ context.Context, keyHash string) (*model.CustomerAPIKey, error) {
	k, ok := f.keysByHash[keyHash]
	if !ok || (!f.permissive && !k.UsableAt(time.Now())) {
		return nil, sql.ErrNoRows
	}
	return k, nil
}

func (f *fakeCustomerKeyStore) TouchCustomerAPIKeyLastUsed(_ context.Context, id string) error {
	f.touches[id]++
	return nil
}

func (f *fakeCustomerKeyStore) FindUserByID(_ context.Context, id string) (*model.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return u, nil
}

var _ CustomerAPIKeyStore = (*fakeCustomerKeyStore)(nil)

// makeKey mints a secret and registers its hash like the creation path
// stores it (SHA-256 of the raw secret via store.HashAPIKey).
func makeKey(t *testing.T, f *fakeCustomerKeyStore, k *model.CustomerAPIKey) string {
	t.Helper()
	secret, err := model.NewCustomerAPIKeySecret()
	if err != nil {
		t.Fatalf("NewCustomerAPIKeySecret: %v", err)
	}
	f.keysByHash[store.HashAPIKey(secret)] = k
	return secret
}

func registerUser(f *fakeCustomerKeyStore, id, email string) {
	f.users[id] = &model.User{ID: id, Email: email, Name: "Test Owner"}
}

// customerKeyApp mounts the auth middleware (and an optional scope
// gate) in front of a handler that records whether it ran.
func customerKeyApp(s CustomerAPIKeyStore, scope string, reached *bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	mws := []gin.HandlerFunc{CustomerAPIKeyAuth(s)}
	if scope != "" {
		mws = append(mws, RequireCustomerScope(scope))
	}
	r.GET("/v1/test", append(mws, func(c *gin.Context) {
		*reached = true
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})...)
	return r
}

func customerKeyGet(r *gin.Engine, target string, header http.Header) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	r.ServeHTTP(w, req)
	return w
}

func bearer(secret string) http.Header {
	h := http.Header{}
	h.Set("Authorization", "Bearer "+secret)
	return h
}

type errEnvelope struct {
	Success bool `json:"success"`
	Error   *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeErr(t *testing.T, w *httptest.ResponseRecorder) errEnvelope {
	t.Helper()
	var body errEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return body
}

// Every refusal of a presented-but-unusable credential must be the
// SAME 401: wrong namespace, unknown hash, revoked, expired, owner
// gone. Anything distinguishable turns the endpoint into a probe for
// which credentials exist and in what state.
func TestCustomerAPIKeyAuth_FailuresAreOneAnswer(t *testing.T) {
	now := time.Now()
	past := now.Add(-time.Hour)

	f := newFakeCustomerKeyStore()
	registerUser(f, "u-1", "alice@example.com")

	unknown := makeKey(t, f, &model.CustomerAPIKey{ID: "k-unknown", UserID: "u-1", Name: "x"})
	revoked := makeKey(t, f, &model.CustomerAPIKey{ID: "k-revoked", UserID: "u-1", Name: "x", RevokedAt: &past})
	expired := makeKey(t, f, &model.CustomerAPIKey{ID: "k-expired", UserID: "u-1", Name: "x", ExpiresAt: &past})
	orphan := makeKey(t, f, &model.CustomerAPIKey{ID: "k-orphan", UserID: "u-gone", Name: "x"})

	// The unknown case is a secret no row carries: register then drop.
	delete(f.keysByHash, store.HashAPIKey(unknown))

	cases := []struct {
		name   string
		header http.Header
		secret string
	}{
		{"wrong namespace", bearer("kg_live_notAcustomerKey"), "kg_live_notAcustomerKey"},
		{"bare garbage", bearer("nope"), "nope"},
		{"unknown key", bearer(unknown), unknown},
		{"revoked key", bearer(revoked), revoked},
		{"expired key", bearer(expired), expired},
		{"owner gone", bearer(orphan), orphan},
	}

	var first string
	for _, tc := range cases {
		reached := false
		w := customerKeyGet(customerKeyApp(f, "", &reached), "/v1/test", tc.header)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", tc.name, w.Code)
		}
		if reached {
			t.Errorf("%s: handler ran for a refused credential", tc.name)
		}
		body := decodeErr(t, w)
		if body.Success || body.Error == nil || body.Error.Code != "UNAUTHORIZED" {
			t.Errorf("%s: body = %s, want {success:false, error.code:UNAUTHORIZED}", tc.name, w.Body.String())
		}
		if strings.Contains(w.Body.String(), tc.secret) {
			t.Errorf("%s: response echoes the presented secret", tc.name)
		}
		if first == "" {
			first = w.Body.String()
		} else if w.Body.String() != first {
			t.Errorf("%s: refusal %q differs from first refusal %q — "+
				"unknown/revoked/expired must not be distinguishable",
				tc.name, w.Body.String(), first)
		}
	}
}

// Even a lookup that hands back a revoked or expired row must not
// authenticate: UsableAt is the gate here too, not only in SQL.
func TestCustomerAPIKeyAuth_UnusableRowsStillRefused(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	f := newFakeCustomerKeyStore()
	f.permissive = true // store contract violated on purpose
	registerUser(f, "u-1", "alice@example.com")

	secrets := []string{
		makeKey(t, f, &model.CustomerAPIKey{ID: "k-revoked", UserID: "u-1", Name: "x", RevokedAt: &past}),
		makeKey(t, f, &model.CustomerAPIKey{ID: "k-expired", UserID: "u-1", Name: "x", ExpiresAt: &past}),
	}
	for _, s := range secrets {
		reached := false
		w := customerKeyGet(customerKeyApp(f, "", &reached), "/v1/test", bearer(s))
		if w.Code != http.StatusUnauthorized || reached {
			t.Errorf("unusable row authenticated: status = %d, reached = %v", w.Code, reached)
		}
		if strings.Contains(w.Body.String(), s) {
			t.Error("refusal echoes the presented secret")
		}
	}
}

// No credential presented at all is its own (harmless) answer: nothing
// was proven wrong about any credential, so the message says what is
// missing instead of pretending one was rejected.
func TestCustomerAPIKeyAuth_MissingCredential(t *testing.T) {
	f := newFakeCustomerKeyStore()
	reached := false
	r := customerKeyApp(f, "", &reached)

	for _, h := range []http.Header{{}, {"X-API-Key": []string{""}}} {
		w := customerKeyGet(r, "/v1/test", h)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", w.Code)
		}
		body := decodeErr(t, w)
		if body.Error == nil || body.Error.Code != "UNAUTHORIZED" || body.Error.Message != "missing api key" {
			t.Errorf("body = %s, want missing api key", w.Body.String())
		}
	}
	if reached {
		t.Error("handler ran with no credential")
	}
}

// A valid key authenticates and hands the handler the owner identity
// in the repo's context-key shape, plus the credential's scopes.
func TestCustomerAPIKeyAuth_SuccessSetsContext(t *testing.T) {
	f := newFakeCustomerKeyStore()
	registerUser(f, "u-1", "alice@example.com")
	expires := time.Now().Add(24 * time.Hour)
	secret := makeKey(t, f, &model.CustomerAPIKey{
		ID: "k-1", UserID: "u-1", Name: "CI runner",
		Scopes: "orders:read, licenses:read", ExpiresAt: &expires,
	})

	var gotUserID, gotEmail, gotName, gotAuthType string
	var gotScopes []string
	var gotKey *model.CustomerAPIKey
	var isAdminSet bool
	reached := false
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/test", CustomerAPIKeyAuth(f), func(c *gin.Context) {
		reached = true
		gotUserID = c.GetString("user_id")
		gotEmail = c.GetString("email")
		gotName = c.GetString("name")
		gotAuthType = c.GetString("auth_type")
		v, _ := c.Get("customer_scopes")
		gotScopes, _ = v.([]string)
		kv, _ := c.Get("customer_api_key")
		gotKey, _ = kv.(*model.CustomerAPIKey)
		_, isAdminSet = c.Get("is_admin")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := customerKeyGet(r, "/v1/test", bearer(secret))
	if w.Code != http.StatusOK || !reached {
		t.Fatalf("status = %d, reached = %v, want 200 + handler", w.Code, reached)
	}
	if gotUserID != "u-1" || gotEmail != "alice@example.com" || gotName != "Test Owner" {
		t.Errorf("identity = (%q, %q, %q), want (u-1, alice@example.com, Test Owner)",
			gotUserID, gotEmail, gotName)
	}
	if gotAuthType != "customer_api_key" {
		t.Errorf("auth_type = %q, want customer_api_key", gotAuthType)
	}
	if len(gotScopes) != 2 || gotScopes[0] != "orders:read" || gotScopes[1] != "licenses:read" {
		t.Errorf("scopes = %v, want [orders:read licenses:read]", gotScopes)
	}
	if gotKey == nil || gotKey.ID != "k-1" {
		t.Errorf("customer_api_key = %+v, want the k-1 row", gotKey)
	}
	if isAdminSet {
		t.Error("is_admin must never be set by customer key auth")
	}
}

// X-API-Key is tolerated as an alternative presentation.
func TestCustomerAPIKeyAuth_XAPIKeyHeader(t *testing.T) {
	f := newFakeCustomerKeyStore()
	registerUser(f, "u-1", "alice@example.com")
	secret := makeKey(t, f, &model.CustomerAPIKey{ID: "k-1", UserID: "u-1", Name: "cli"})

	reached := false
	w := customerKeyGet(customerKeyApp(f, "", &reached), "/v1/test",
		http.Header{"X-API-Key": []string{secret}})
	if w.Code != http.StatusOK || !reached {
		t.Fatalf("X-API-Key auth failed: status = %d, reached = %v, body = %s",
			w.Code, reached, w.Body.String())
	}
}

// last_used_at is stamped on successful authentication — but at most
// once per key per throttle window, so a polling client cannot turn
// every request into a write.
func TestCustomerAPIKeyAuth_TouchThrottled(t *testing.T) {
	f := newFakeCustomerKeyStore()
	registerUser(f, "u-1", "alice@example.com")
	secret := makeKey(t, f, &model.CustomerAPIKey{ID: "k-touch", UserID: "u-1", Name: "poller"})

	reached := false
	r := customerKeyApp(f, "", &reached)
	for i := 0; i < 5; i++ {
		if w := customerKeyGet(r, "/v1/test", bearer(secret)); w.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d", i, w.Code)
		}
	}
	if got := f.touches["k-touch"]; got != 1 {
		t.Errorf("touches = %d after 5 requests, want 1 (throttled)", got)
	}

	// A different key has its own window and is stamped at first use.
	secret2 := makeKey(t, f, &model.CustomerAPIKey{ID: "k-touch-2", UserID: "u-1", Name: "other"})
	if w := customerKeyGet(r, "/v1/test", bearer(secret2)); w.Code != http.StatusOK {
		t.Fatalf("second key: status = %d", w.Code)
	}
	if got := f.touches["k-touch-2"]; got != 1 {
		t.Errorf("second key touches = %d, want 1", got)
	}
	if got := f.touches["k-touch"]; got != 1 {
		t.Errorf("first key was stamped again: %d", got)
	}
}

// A failed touch must never fail the request: the credential is valid,
// the stamp is an operational hint.
func TestCustomerAPIKeyAuth_TouchFailureIsIgnored(t *testing.T) {
	f := newFakeCustomerKeyStore()
	registerUser(f, "u-1", "alice@example.com")
	secret := makeKey(t, f, &model.CustomerAPIKey{ID: "k-1", UserID: "u-1", Name: "cli"})

	// Break the touch by removing the id lookup path: wrap the fake.
	broken := &touchBrokenStore{fakeCustomerKeyStore: f}
	reached := false
	w := customerKeyGet(customerKeyApp(broken, "", &reached), "/v1/test", bearer(secret))
	if w.Code != http.StatusOK || !reached {
		t.Fatalf("touch failure poisoned the request: status = %d", w.Code)
	}
}

type touchBrokenStore struct{ *fakeCustomerKeyStore }

func (t *touchBrokenStore) TouchCustomerAPIKeyLastUsed(context.Context, string) error {
	return errors.New("database exploded")
}

// RequireCustomerScope mirrors the admin RequireScope: wildcard wins,
// exact match wins, missing scope is 403 INSUFFICIENT_SCOPE, and no
// credential at all is 401. Empty scope lists are fail-closed — a key
// with no scopes reaches no gated route.
func TestRequireCustomerScope(t *testing.T) {
	mk := func(id, scopes string) *model.CustomerAPIKey {
		return &model.CustomerAPIKey{ID: id, UserID: "u-1", Name: "k", Scopes: scopes}
	}

	cases := []struct {
		name  string
		key   *model.CustomerAPIKey // nil = no auth context
		scope string
		want  int
	}{
		{"exact match passes", mk("k1", "orders:read"), "orders:read", http.StatusOK},
		{"comma list matches", mk("k2", "orders:read, licenses:read"), "licenses:read", http.StatusOK},
		{"wildcard passes", mk("k3", "*"), "orders:read", http.StatusOK},
		{"wildcard inside list", mk("k4", "orders:read,*"), "licenses:read", http.StatusOK},
		{"wrong scope is 403", mk("k5", "licenses:read"), "orders:read", http.StatusForbidden},
		{"empty scopes are fail-closed", mk("k6", ""), "orders:read", http.StatusForbidden},
		{"empty list entries only", mk("k7", " , "), "orders:read", http.StatusForbidden},
		{"no credential is 401", nil, "orders:read", http.StatusUnauthorized},
	}

	for _, tc := range cases {
		reached := false
		gin.SetMode(gin.TestMode)
		r := gin.New()
		r.GET("/v1/test", func(c *gin.Context) {
			if tc.key != nil {
				c.Set("customer_api_key", tc.key)
			}
		}, RequireCustomerScope(tc.scope), func(c *gin.Context) {
			reached = true
			c.JSON(http.StatusOK, gin.H{"ok": true})
		})
		w := customerKeyGet(r, "/v1/test", nil)
		if w.Code != tc.want {
			t.Errorf("%s: status = %d, want %d (body %s)", tc.name, w.Code, tc.want, w.Body.String())
		}
		if reached != (tc.want == http.StatusOK) {
			t.Errorf("%s: reached = %v at status %d", tc.name, reached, w.Code)
		}
		if tc.want == http.StatusForbidden {
			body := decodeErr(t, w)
			if body.Error == nil || body.Error.Code != "INSUFFICIENT_SCOPE" {
				t.Errorf("%s: body = %s, want INSUFFICIENT_SCOPE", tc.name, w.Body.String())
			}
		}
	}
}

// The full shape: auth + scope gate on one route, proving the two
// middlewares compose in the order the Lead will wire them.
func TestCustomerAPIKeyAuth_WithScopeGate(t *testing.T) {
	f := newFakeCustomerKeyStore()
	registerUser(f, "u-1", "alice@example.com")
	secret := makeKey(t, f, &model.CustomerAPIKey{
		ID: "k-1", UserID: "u-1", Name: "ci", Scopes: "orders:read",
	})

	reached := false
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/orders", CustomerAPIKeyAuth(f), RequireCustomerScope("orders:read"), func(c *gin.Context) {
		reached = true
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
	r.GET("/v1/licenses", CustomerAPIKeyAuth(f), RequireCustomerScope("licenses:read"), func(c *gin.Context) {
		t.Error("licenses route ran with an orders-only key")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	if w := customerKeyGet(r, "/v1/orders", bearer(secret)); w.Code != http.StatusOK || !reached {
		t.Fatalf("orders route: status = %d, reached = %v, body = %s", w.Code, reached, w.Body.String())
	}
	w := customerKeyGet(r, "/v1/licenses", bearer(secret))
	if w.Code != http.StatusForbidden {
		t.Errorf("licenses route: status = %d, want 403 (body %s)", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Error("403 response echoes the presented secret")
	}
}
