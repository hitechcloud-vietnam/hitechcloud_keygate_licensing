package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/middleware"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// Developer API tests use a fake store on purpose: what must hold is
// the handler CONTRACT — 401 without auth, owner-scoped queries only,
// and payload shapes that cannot carry a credential — and all of that
// is visible at the interfaces the handler consumes. No test database
// is needed, so these always run.

// fakeDeveloperAPIStore serves both the handler's read interface and
// (for the end-to-end test) the auth middleware's lookup interface.
type fakeDeveloperAPIStore struct {
	// middleware.CustomerAPIKeyStore
	keysByHash map[string]*model.CustomerAPIKey
	users      map[string]*model.User
	touches    int

	// developerAPIStore — rows keyed by the email the handler passed,
	// which is the whole point: a query scoped to the wrong email
	// finds nothing here.
	ordersByEmail   map[string][]*model.Order
	licensesByEmail map[string][]*model.License

	orderEmails   []string // every email ListOrdersByEmail was asked for
	licenseEmails []string // every email ListLicensesByEmail was asked for
	lastStatus    string
}

func newFakeDeveloperAPIStore() *fakeDeveloperAPIStore {
	return &fakeDeveloperAPIStore{
		keysByHash:      map[string]*model.CustomerAPIKey{},
		users:           map[string]*model.User{},
		ordersByEmail:   map[string][]*model.Order{},
		licensesByEmail: map[string][]*model.License{},
	}
}

func (f *fakeDeveloperAPIStore) FindCustomerAPIKeyByHash(_ context.Context, keyHash string) (*model.CustomerAPIKey, error) {
	k, ok := f.keysByHash[keyHash]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return k, nil
}

func (f *fakeDeveloperAPIStore) TouchCustomerAPIKeyLastUsed(_ context.Context, _ string) error {
	f.touches++
	return nil
}

func (f *fakeDeveloperAPIStore) FindUserByID(_ context.Context, id string) (*model.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, sql.ErrNoRows
	}
	return u, nil
}

func (f *fakeDeveloperAPIStore) ListOrdersByEmail(_ context.Context, email, status string, p store.Page) ([]*model.Order, int, error) {
	f.orderEmails = append(f.orderEmails, email)
	f.lastStatus = status
	rows := append([]*model.Order(nil), f.ordersByEmail[email]...)
	if status != "" {
		var filtered []*model.Order
		for _, o := range rows {
			if o.Status == status {
				filtered = append(filtered, o)
			}
		}
		rows = filtered
	}
	total := len(rows)
	if p.Limit > 0 && p.Offset < total {
		end := min(p.Offset+p.Limit, total)
		rows = rows[p.Offset:end]
	} else if p.Limit > 0 {
		rows = nil
	}
	return rows, total, nil
}

func (f *fakeDeveloperAPIStore) ListLicensesByEmail(_ context.Context, email string) ([]*model.License, error) {
	f.licenseEmails = append(f.licenseEmails, email)
	return append([]*model.License(nil), f.licensesByEmail[email]...), nil
}

var (
	_ middleware.CustomerAPIKeyStore = (*fakeDeveloperAPIStore)(nil)
	_ developerAPIStore              = (*fakeDeveloperAPIStore)(nil)
)

// devAPIRequest builds the request context exactly as
// middleware.CustomerAPIKeyAuth leaves it for the handlers.
func devAPIRequest(t *testing.T, target string, key *model.CustomerAPIKey, email string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	if key != nil {
		c.Set("customer_api_key", key)
		c.Set("customer_scopes", key.ScopeList())
	}
	if email != "" {
		c.Set("user_id", "u-"+email)
		c.Set("email", email)
	}
	return w, c
}

type devMeBody struct {
	Success bool `json:"success"`
	Data    struct {
		ID         string     `json:"id"`
		Name       string     `json:"name"`
		KeyPrefix  string     `json:"key_prefix"`
		Scopes     []string   `json:"scopes"`
		ExpiresAt  *time.Time `json:"expires_at"`
		LastUsedAt *time.Time `json:"last_used_at"`
		CreatedAt  time.Time  `json:"created_at"`
		OwnerEmail string     `json:"owner_email"`
	} `json:"data"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

type devOrdersBody struct {
	Success bool `json:"success"`
	Data    struct {
		Orders []struct {
			ID          string `json:"id"`
			OrderNumber string `json:"order_number"`
			TotalMinor  int64  `json:"total_minor"`
			Status      string `json:"status"`
		} `json:"orders"`
		Total  int `json:"total"`
		Limit  int `json:"limit"`
		Offset int `json:"offset"`
	} `json:"data"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

type devLicensesBody struct {
	Success bool `json:"success"`
	Data    struct {
		Licenses []developerLicenseInfo `json:"licenses"`
	} `json:"data"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

func decodeAs[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var body T
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return body
}

// Without an authenticated identity every endpoint answers 401 — the
// routes sit behind middleware.CustomerAPIKeyAuth, and this is the
// belt to those suspenders (same discipline as the portal handlers).
func TestDeveloperAPI_UnauthenticatedIs401(t *testing.T) {
	h := NewDeveloperAPIHandler(nil)
	h.store = newFakeDeveloperAPIStore()

	for _, tc := range []struct {
		name string
		run  func(*gin.Context)
	}{
		{"me", h.Me},
		{"orders", h.ListOrders},
		{"licenses", h.ListLicenses},
	} {
		w, c := devAPIRequest(t, "/v1/"+tc.name, nil, "")
		tc.run(c)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", tc.name, w.Code)
		}
		body := decodeAs[devMeBody](t, w)
		if body.Error == nil || body.Error.Code != "UNAUTHORIZED" {
			t.Errorf("%s: body = %s, want UNAUTHORIZED", tc.name, w.Body.String())
		}
	}
}

// GET /v1/me answers with the key's self-description and nothing that
// could authenticate anyone: no secret (never stored) and no key hash.
func TestDeveloperAPI_MeShape(t *testing.T) {
	h := NewDeveloperAPIHandler(nil)
	h.store = newFakeDeveloperAPIStore()

	expires := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	lastUsed := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	key := &model.CustomerAPIKey{
		ID: "k-1", UserID: "u-1", Name: "CI runner",
		KeyPrefix: "htc_sk_AbCd",
		KeyHash:   "cafebabe01234567",
		Scopes:    "orders:read, licenses:read",
		ExpiresAt: &expires, LastUsedAt: &lastUsed,
	}

	w, c := devAPIRequest(t, "/v1/me", key, "alice@example.com")
	h.Me(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := decodeAs[devMeBody](t, w)
	d := body.Data
	if d.ID != "k-1" || d.Name != "CI runner" || d.KeyPrefix != "htc_sk_AbCd" {
		t.Errorf("identity = %+v, want k-1 / CI runner / htc_sk_AbCd", d)
	}
	if len(d.Scopes) != 2 || d.Scopes[0] != "orders:read" || d.Scopes[1] != "licenses:read" {
		t.Errorf("scopes = %v, want [orders:read licenses:read]", d.Scopes)
	}
	if d.OwnerEmail != "alice@example.com" {
		t.Errorf("owner_email = %q, want alice@example.com", d.OwnerEmail)
	}
	if d.ExpiresAt == nil || !d.ExpiresAt.Equal(expires) {
		t.Errorf("expires_at = %v, want %v", d.ExpiresAt, expires)
	}
	if d.LastUsedAt == nil || !d.LastUsedAt.Equal(lastUsed) {
		t.Errorf("last_used_at = %v, want %v", d.LastUsedAt, lastUsed)
	}
	for _, banned := range []string{key.KeyHash, "key_hash", "secret"} {
		if strings.Contains(w.Body.String(), banned) {
			t.Errorf("/v1/me leaks %q: %s", banned, w.Body.String())
		}
	}
}

// A key with no scopes still introspects (Scopes is [], never null) —
// it just cannot pass a RequireCustomerScope gate.
func TestDeveloperAPI_MeEmptyScopesIsArray(t *testing.T) {
	h := NewDeveloperAPIHandler(nil)
	h.store = newFakeDeveloperAPIStore()

	key := &model.CustomerAPIKey{ID: "k-1", UserID: "u-1", Name: "bare"}
	w, c := devAPIRequest(t, "/v1/me", key, "alice@example.com")
	h.Me(c)

	body := decodeAs[devMeBody](t, w)
	if body.Data.Scopes == nil || len(body.Data.Scopes) != 0 {
		t.Errorf("scopes = %v, want empty non-nil array", body.Data.Scopes)
	}
	if !strings.Contains(w.Body.String(), `"scopes":[]`) {
		t.Errorf("body = %s, want scopes encoded as []", w.Body.String())
	}
}

func devTestOrder(id, email, status string, total int64) *model.Order {
	return &model.Order{
		ID: id, OrderNumber: "HTC-" + id, CustomerEmail: email,
		Currency: "USD", TotalMinor: total, Status: status,
	}
}

// GET /v1/orders queries exactly the key owner's email — never
// anything the request carried — and pages like the portal.
func TestDeveloperAPI_OrdersScopedToKeyOwner(t *testing.T) {
	f := newFakeDeveloperAPIStore()
	f.ordersByEmail["alice@example.com"] = []*model.Order{
		devTestOrder("o-a1", "alice@example.com", "paid", 1500),
		devTestOrder("o-a2", "alice@example.com", "paid", 2500),
	}
	f.ordersByEmail["bob@example.com"] = []*model.Order{
		devTestOrder("o-b1", "bob@example.com", "paid", 999),
	}
	h := NewDeveloperAPIHandler(nil)
	h.store = f

	// ?email= must not widen the scope: the identity comes from the
	// auth context only.
	w, c := devAPIRequest(t, "/v1/orders?limit=1&email=bob@example.com",
		&model.CustomerAPIKey{ID: "k-1"}, "alice@example.com")
	h.ListOrders(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if len(f.orderEmails) != 1 || f.orderEmails[0] != "alice@example.com" {
		t.Fatalf("store asked for %v, want only alice@example.com", f.orderEmails)
	}
	body := decodeAs[devOrdersBody](t, w)
	if body.Data.Total != 2 || len(body.Data.Orders) != 1 {
		t.Errorf("page = %d rows of %d, want 1 of 2", len(body.Data.Orders), body.Data.Total)
	}
	if len(body.Data.Orders) == 1 && body.Data.Orders[0].ID != "o-a1" {
		t.Errorf("order = %s, want o-a1 (alice's first)", body.Data.Orders[0].ID)
	}
	if body.Data.Limit != 1 || body.Data.Offset != 0 {
		t.Errorf("paging = limit %d offset %d, want 1 / 0", body.Data.Limit, body.Data.Offset)
	}
	for _, banned := range []string{"o-b1", "bob@example.com"} {
		if strings.Contains(w.Body.String(), banned) {
			t.Errorf("alice's page leaks %q: %s", banned, w.Body.String())
		}
	}

	// Bob's key sees Bob's rows and only his.
	w, c = devAPIRequest(t, "/v1/orders", &model.CustomerAPIKey{ID: "k-2"}, "bob@example.com")
	h.ListOrders(c)
	body = decodeAs[devOrdersBody](t, w)
	if len(body.Data.Orders) != 1 || body.Data.Orders[0].ID != "o-b1" {
		t.Errorf("bob's page = %+v, want only o-b1", body.Data.Orders)
	}
	if strings.Contains(w.Body.String(), "o-a1") {
		t.Errorf("bob's page leaks alice's order: %s", w.Body.String())
	}
}

// ?status= is the same closed vocabulary the portal accepts; a typo is
// refused, not silently widened to "all".
func TestDeveloperAPI_OrdersStatusFilter(t *testing.T) {
	f := newFakeDeveloperAPIStore()
	f.ordersByEmail["alice@example.com"] = []*model.Order{
		devTestOrder("o-a1", "alice@example.com", "paid", 1500),
		devTestOrder("o-a2", "alice@example.com", "refunded", 2500),
	}
	h := NewDeveloperAPIHandler(nil)
	h.store = f

	w, c := devAPIRequest(t, "/v1/orders?status=refunded",
		&model.CustomerAPIKey{ID: "k-1"}, "alice@example.com")
	h.ListOrders(c)
	body := decodeAs[devOrdersBody](t, w)
	if f.lastStatus != "refunded" || len(body.Data.Orders) != 1 || body.Data.Orders[0].ID != "o-a2" {
		t.Errorf("filter = %q, rows = %+v, want refunded / o-a2", f.lastStatus, body.Data.Orders)
	}

	w, c = devAPIRequest(t, "/v1/orders?status=settled",
		&model.CustomerAPIKey{ID: "k-1"}, "alice@example.com")
	h.ListOrders(c)
	if w.Code != http.StatusBadRequest {
		t.Errorf("bad status: status = %d, want 400 (body %s)", w.Code, w.Body.String())
	}
}

// GET /v1/licenses reports metadata and ONLY metadata. The fixture
// arms model.License with every credential and internal field it
// carries; none of them may appear in the payload — the DTO, not the
// model, is what gets serialized.
func TestDeveloperAPI_LicensesMetadataOnly(t *testing.T) {
	validUntil := time.Now().Add(30 * 24 * time.Hour)
	f := newFakeDeveloperAPIStore()
	f.licensesByEmail["alice@example.com"] = []*model.License{
		{
			ID: "lic-1", ProductID: "p-1", PlanID: "pl-1",
			Email: "alice@example.com",
			// Credential material + internal notes — must never leak.
			LicenseKey:          "htc_lic_S3CRETKEY",
			KeyHash:             "deadbeefcafebabe",
			LicenseKeyEncrypted: []byte{0xde, 0xad, 0xbe, 0xef},
			Notes:               "internal note xyzzy",
			OrgName:             "ACME internal org",
			ExternalCustomerID:  "ext-999",
			StripeCustomerID:    "cus_999",
			PaymentProvider:     "stripe",
			Status:              model.StatusActive,
			ValidFrom:           time.Now().Add(-24 * time.Hour),
			ValidUntil:          &validUntil,
			ActivationCount:     2,
			ActiveSessionCount:  1,
			Product:             &model.Product{ID: "p-1", Name: "Cloud App"},
			Plan:                &model.Plan{ID: "pl-1", Name: "Pro", MaxActivations: 3},
			Seats:               []*model.Seat{{ID: "s-1", Email: "seat-user@example.com"}},
		},
	}
	h := NewDeveloperAPIHandler(nil)
	h.store = f

	w, c := devAPIRequest(t, "/v1/licenses", &model.CustomerAPIKey{ID: "k-1"}, "alice@example.com")
	h.ListLicenses(c)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	body := decodeAs[devLicensesBody](t, w)
	if len(body.Data.Licenses) != 1 {
		t.Fatalf("licenses = %+v, want 1 row", body.Data.Licenses)
	}
	got := body.Data.Licenses[0]
	if got.ID != "lic-1" || got.ProductName != "Cloud App" || got.PlanName != "Pro" ||
		got.Status != model.StatusActive {
		t.Errorf("metadata = %+v, want lic-1 / Cloud App / Pro / active", got)
	}
	if got.ActivationCount != 2 || got.ActiveSessionCount != 1 || got.MaxActivations != 3 {
		t.Errorf("activation metadata = %+v, want 2 / 1 / 3", got)
	}
	if got.ProductID != "p-1" || got.PlanID != "pl-1" {
		t.Errorf("ids = %s/%s, want p-1/pl-1", got.ProductID, got.PlanID)
	}
	if got.ValidUntil == nil || !got.ValidUntil.Equal(validUntil) {
		t.Errorf("valid_until = %v, want %v", got.ValidUntil, validUntil)
	}

	for _, banned := range []string{
		"htc_lic_S3CRETKEY",       // licence key (plaintext)
		"deadbeefcafebabe",        // key hash
		"3q2+7w==",                // encrypted key bytes (base64 JSON encoding)
		"internal note xyzzy",     // notes
		"ACME internal org",       // org name
		"ext-999",                 // external customer id
		"cus_999",                 // stripe customer id
		"seat-user@example.com",   // seat rows
		"license_key", "key_hash", // the field names themselves
	} {
		if strings.Contains(w.Body.String(), banned) {
			t.Errorf("/v1/licenses leaks %q: %s", banned, w.Body.String())
		}
	}
}

// Ownership rides on the queried email: each key only ever receives
// the rows of the user who minted it.
func TestDeveloperAPI_LicensesCrossUserIsolation(t *testing.T) {
	f := newFakeDeveloperAPIStore()
	f.licensesByEmail["alice@example.com"] = []*model.License{
		{ID: "lic-a", ProductID: "p-1", PlanID: "pl-1", Status: model.StatusActive},
	}
	f.licensesByEmail["bob@example.com"] = []*model.License{
		{ID: "lic-b", ProductID: "p-2", PlanID: "pl-2", Status: model.StatusActive},
	}
	h := NewDeveloperAPIHandler(nil)
	h.store = f

	w, c := devAPIRequest(t, "/v1/licenses?email=bob@example.com",
		&model.CustomerAPIKey{ID: "k-1"}, "alice@example.com")
	h.ListLicenses(c)

	if len(f.licenseEmails) != 1 || f.licenseEmails[0] != "alice@example.com" {
		t.Fatalf("store asked for %v, want only alice@example.com", f.licenseEmails)
	}
	body := decodeAs[devLicensesBody](t, w)
	if len(body.Data.Licenses) != 1 || body.Data.Licenses[0].ID != "lic-a" {
		t.Errorf("alice's page = %+v, want only lic-a", body.Data.Licenses)
	}
	if strings.Contains(w.Body.String(), "lic-b") {
		t.Errorf("alice's page leaks bob's licence: %s", w.Body.String())
	}
}

// End to end: the real auth middleware in front of the real handlers,
// over one gin engine wired the way the Lead will wire it. A presented
// `htc_sk_…` secret authenticates, the scope gate opens for it, and
// the answer is the owner's rows.
func TestDeveloperAPI_EndToEnd(t *testing.T) {
	f := newFakeDeveloperAPIStore()
	f.users["u-1"] = &model.User{ID: "u-1", Email: "alice@example.com", Name: "Alice"}
	secret, err := model.NewCustomerAPIKeySecret()
	if err != nil {
		t.Fatalf("NewCustomerAPIKeySecret: %v", err)
	}
	key := &model.CustomerAPIKey{
		ID: "k-1", UserID: "u-1", Name: "CI runner",
		KeyPrefix: model.CustomerAPIKeyDisplayPrefix(secret),
		Scopes:    "orders:read",
	}
	f.keysByHash[store.HashAPIKey(secret)] = key
	f.ordersByEmail["alice@example.com"] = []*model.Order{
		devTestOrder("o-a1", "alice@example.com", "paid", 1500),
	}

	h := NewDeveloperAPIHandler(nil)
	h.store = f

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/me", middleware.CustomerAPIKeyAuth(f), h.Me)
	r.GET("/v1/orders", middleware.CustomerAPIKeyAuth(f),
		middleware.RequireCustomerScope(ScopeOrdersRead), h.ListOrders)
	r.GET("/v1/licenses", middleware.CustomerAPIKeyAuth(f),
		middleware.RequireCustomerScope(ScopeLicensesRead), h.ListLicenses)

	get := func(target string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer "+secret)
		r.ServeHTTP(w, req)
		return w
	}

	w := get("/v1/me")
	if w.Code != http.StatusOK {
		t.Fatalf("/v1/me: status = %d, body = %s", w.Code, w.Body.String())
	}
	if me := decodeAs[devMeBody](t, w); me.Data.OwnerEmail != "alice@example.com" {
		t.Errorf("owner_email = %q, want alice@example.com", me.Data.OwnerEmail)
	}

	w = get("/v1/orders")
	if w.Code != http.StatusOK {
		t.Fatalf("/v1/orders: status = %d, body = %s", w.Code, w.Body.String())
	}

	// The key has orders:read only — licenses is refused by the gate,
	// and the refusal never echoes the secret.
	w = get("/v1/licenses")
	if w.Code != http.StatusForbidden {
		t.Errorf("/v1/licenses: status = %d, want 403", w.Code)
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Error("403 body echoes the presented secret")
	}
}
