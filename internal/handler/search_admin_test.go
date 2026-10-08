package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ── fake store ───────────────────────────────────────────────────────

type fakeSearchAdminStore struct {
	hits       []store.SearchHit
	searchErr  error
	perms      map[string]bool
	permsErr   error
	lastOpts   store.SearchOptions
	searchCall int
	permsCalls int
}

func (f *fakeSearchAdminStore) Search(_ context.Context, opts store.SearchOptions) ([]store.SearchHit, error) {
	f.searchCall++
	f.lastOpts = opts
	return f.hits, f.searchErr
}

func (f *fakeSearchAdminStore) PermissionsForUser(context.Context, string) (map[string]bool, error) {
	f.permsCalls++
	return f.perms, f.permsErr
}

// ── harness ──────────────────────────────────────────────────────────

func newAdminSearchHarness() (*AdminSearchHandler, *fakeSearchAdminStore) {
	gin.SetMode(gin.TestMode)
	st := &fakeSearchAdminStore{}
	return &AdminSearchHandler{store: st}, st
}

// adminSearchCall performs one GET with the given query string and
// session context values. Missing keys are simply not set — a request
// with no user_id at all is its own test case.
func adminSearchCall(h *AdminSearchHandler, query string, session map[string]any) (int, string) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/search?"+query, nil)
	for k, v := range session {
		c.Set(k, v)
	}
	h.Search(c)
	return w.Code, w.Body.String()
}

func adminSession() map[string]any {
	return map[string]any{"user_id": "u1", "auth_type": "session", "is_admin": true}
}

// ── result shape ─────────────────────────────────────────────────────

// The answer is exactly the pinned contract: {results: [{type, id,
// title, subtitle, url}]} — nothing more on a row, and never a
// credential.
func TestAdminSearchResultShape(t *testing.T) {
	h, st := newAdminSearchHarness()
	st.hits = []store.SearchHit{
		{Type: store.SearchTypeProduct, ID: "p1", Title: "Acme", Subtitle: "acme"},
		{Type: store.SearchTypeOrder, ID: "o1", Title: "HTC-1234567", Subtitle: "buyer@example.com"},
	}

	code, body := adminSearchCall(h, "q=acme", adminSession())
	if code != http.StatusOK {
		t.Fatalf("search = %d %s", code, body)
	}
	var parsed struct {
		Success bool `json:"success"`
		Data    struct {
			Results []map[string]any `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("parse: %v (%s)", err, body)
	}
	if !parsed.Success {
		t.Errorf("success = false, want true (%s)", body)
	}
	if len(parsed.Data.Results) != 2 {
		t.Fatalf("got %d results, want 2 (%s)", len(parsed.Data.Results), body)
	}
	for i, row := range parsed.Data.Results {
		want := map[string]bool{"type": true, "id": true, "title": true, "subtitle": true, "url": true}
		for k := range row {
			if !want[k] {
				t.Errorf("result[%d] has unexpected field %q", i, k)
			}
		}
		for k := range want {
			if _, ok := row[k]; !ok {
				t.Errorf("result[%d] is missing field %q", i, k)
			}
		}
	}
	if got := parsed.Data.Results[0]["url"]; got != "/admin/products?search=Acme" {
		t.Errorf("product url = %v", got)
	}
	if got := parsed.Data.Results[1]["url"]; got != "/admin/orders/o1" {
		t.Errorf("order url = %v", got)
	}
}

// The URL a hit links to, per type. Detail screens get the id; list
// screens get ?search= on the column the list filters by; devices and
// subscriptions reach the licence list through their licence's email.
func TestAdminSearchResultURLs(t *testing.T) {
	cases := []struct {
		hit  store.SearchHit
		want string
	}{
		{store.SearchHit{Type: store.SearchTypeProduct, ID: "p1", Title: "Acme Suite"}, "/admin/products?search=Acme+Suite"},
		{store.SearchHit{Type: store.SearchTypeCustomer, ID: "u1", Subtitle: "a@b.c"}, "/admin/customers?search=a%40b.c"},
		{store.SearchHit{Type: store.SearchTypeOrder, ID: "o1"}, "/admin/orders/o1"},
		{store.SearchHit{Type: store.SearchTypeInvoice, ID: "i1", Ref: "o9"}, "/admin/orders/o9"},
		{store.SearchHit{Type: store.SearchTypeLicense, ID: "l1", Title: "buyer@example.com"}, "/admin/licenses?search=buyer%40example.com"},
		{store.SearchHit{Type: store.SearchTypeSubscription, ID: "s1", Subtitle: "buyer@example.com"}, "/admin/licenses?search=buyer%40example.com"},
		{store.SearchHit{Type: store.SearchTypeDevice, ID: "d1", Subtitle: "buyer@example.com"}, "/admin/licenses?search=buyer%40example.com"},
		{store.SearchHit{Type: store.SearchTypeReseller, ID: "r1"}, "/admin/resellers/r1"},
		{store.SearchHit{Type: store.SearchTypeAffiliate, ID: "a1"}, "/admin/affiliates/a1"},
	}
	for _, tc := range cases {
		if got := searchResultURL(tc.hit); got != tc.want {
			t.Errorf("searchResultURL(%s) = %q, want %q", tc.hit.Type, got, tc.want)
		}
	}
	// Space encoding is checked above via Acme+Suite; make sure the
	// exact form is what url.QueryEscape produces (no %20 surprise).
	if got := url.QueryEscape("Acme Suite"); got != "Acme+Suite" {
		t.Logf("QueryEscape form changed: %q", got)
	}
}

// ── limit clamp ──────────────────────────────────────────────────────

// The limit clamps rather than refuses: over the cap gets the cap,
// absent or junk gets the default, and a sane value passes through.
func TestAdminSearchLimitClamp(t *testing.T) {
	cases := []struct {
		query string
		want  int
	}{
		{"q=x&limit=500", searchMaxLimit},
		{"q=x&limit=50", searchMaxLimit},
		{"q=x&limit=51", searchMaxLimit},
		{"q=x&limit=7", 7},
		{"q=x&limit=0", searchDefaultLimit},
		{"q=x&limit=-3", searchDefaultLimit},
		{"q=x&limit=abc", searchDefaultLimit},
		{"q=x", searchDefaultLimit},
	}
	for _, tc := range cases {
		h, st := newAdminSearchHarness()
		code, body := adminSearchCall(h, tc.query, adminSession())
		if code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.query, code, body)
		}
		if st.lastOpts.Limit != tc.want {
			t.Errorf("%s: store saw limit %d, want %d", tc.query, st.lastOpts.Limit, tc.want)
		}
	}
}

// ── empty query ──────────────────────────────────────────────────────

// An empty or whitespace q answers an empty LIST (never null) and
// never reaches the store: "everything" is not a search.
func TestAdminSearchEmptyQuery(t *testing.T) {
	for _, q := range []string{"", "q=", "q=%20%20"} {
		h, st := newAdminSearchHarness()
		code, body := adminSearchCall(h, q, adminSession())
		if code != http.StatusOK {
			t.Fatalf("%q: %d %s", q, code, body)
		}
		if st.searchCall != 0 {
			t.Errorf("%q: store was called %d times, want 0", q, st.searchCall)
		}
		var parsed struct {
			Data struct {
				Results []json.RawMessage `json:"results"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &parsed); err != nil {
			t.Fatalf("%q: parse: %v (%s)", q, err, body)
		}
		if string(mustJSON(t, parsed.Data.Results)) != "[]" {
			t.Errorf("%q: results = %s, want []", q, mustJSON(t, parsed.Data.Results))
		}
	}
}

// ── types filter ─────────────────────────────────────────────────────

// A ?types= member outside the closed vocabulary is refused with 400
// — nothing user-typed reaches a query — and the store is not called.
func TestAdminSearchUnknownTypeRefused(t *testing.T) {
	h, st := newAdminSearchHarness()
	code, body := adminSearchCall(h, "q=x&types=product,bogus", adminSession())
	if code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400 (%s)", code, body)
	}
	if st.searchCall != 0 {
		t.Errorf("store was called %d times, want 0", st.searchCall)
	}
}

// types passes through to the store in the caller's order, minus
// duplicates and empty segments. Absent types means every type — the
// store gets no filter and searches the whole vocabulary.
func TestAdminSearchTypesPassThrough(t *testing.T) {
	h, st := newAdminSearchHarness()
	code, body := adminSearchCall(h, "q=x&types=order,,reseller,order", adminSession())
	if code != http.StatusOK {
		t.Fatalf("code = %d %s", code, body)
	}
	want := []string{store.SearchTypeOrder, store.SearchTypeReseller}
	if len(st.lastOpts.Types) != len(want) {
		t.Fatalf("store types = %v, want %v", st.lastOpts.Types, want)
	}
	for i := range want {
		if st.lastOpts.Types[i] != want[i] {
			t.Errorf("store types = %v, want %v", st.lastOpts.Types, want)
		}
	}

	h2, st2 := newAdminSearchHarness()
	if code, body := adminSearchCall(h2, "q=x", adminSession()); code != http.StatusOK {
		t.Fatalf("code = %d %s", code, body)
	}
	if st2.lastOpts.Types != nil {
		t.Errorf("absent types sent a filter: %v", st2.lastOpts.Types)
	}
}

// ── RBAC sieve ──────────────────────────────────────────────────────

// The sieve mirrors middleware.RequirePermission: is_admin and
// api-key requests pass unfiltered, anyone else keeps only the types
// their permission set covers. An empty filtered set answers an empty
// list WITHOUT a lookup.
func TestAdminSearchRBACFilter(t *testing.T) {
	t.Run("is_admin sees everything", func(t *testing.T) {
		h, st := newAdminSearchHarness()
		code, body := adminSearchCall(h, "q=x&types=product,order", adminSession())
		if code != http.StatusOK {
			t.Fatalf("%d %s", code, body)
		}
		if len(st.lastOpts.Types) != 2 {
			t.Errorf("types = %v, want both", st.lastOpts.Types)
		}
	})

	t.Run("api_key passes unfiltered", func(t *testing.T) {
		h, st := newAdminSearchHarness()
		sess := map[string]any{"user_id": "apikey:k1", "auth_type": "api_key"}
		code, body := adminSearchCall(h, "q=x&types=product,order", sess)
		if code != http.StatusOK {
			t.Fatalf("%d %s", code, body)
		}
		if len(st.lastOpts.Types) != 2 {
			t.Errorf("types = %v, want both", st.lastOpts.Types)
		}
	})

	t.Run("custom roles keep only granted types", func(t *testing.T) {
		h, st := newAdminSearchHarness()
		st.perms = map[string]bool{"products.read": true}
		sess := map[string]any{"user_id": "u2", "auth_type": "session"}
		code, body := adminSearchCall(h, "q=x&types=product,customer,license", sess)
		if code != http.StatusOK {
			t.Fatalf("%d %s", code, body)
		}
		want := []string{store.SearchTypeProduct}
		if len(st.lastOpts.Types) != 1 || st.lastOpts.Types[0] != want[0] {
			t.Errorf("types = %v, want %v", st.lastOpts.Types, want)
		}
	})

	t.Run("no grants answers empty without a lookup", func(t *testing.T) {
		h, st := newAdminSearchHarness()
		st.perms = map[string]bool{}
		sess := map[string]any{"user_id": "u2", "auth_type": "session"}
		code, body := adminSearchCall(h, "q=x&types=product", sess)
		if code != http.StatusOK {
			t.Fatalf("%d %s", code, body)
		}
		if st.searchCall != 0 {
			t.Errorf("store was called %d times, want 0", st.searchCall)
		}
		var parsed struct {
			Data struct {
				Results []json.RawMessage `json:"results"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &parsed); err != nil {
			t.Fatalf("parse: %v (%s)", err, body)
		}
		if string(mustJSON(t, parsed.Data.Results)) != "[]" {
			t.Errorf("results = %s, want []", mustJSON(t, parsed.Data.Results))
		}
	})

	t.Run("wildcard grants everything", func(t *testing.T) {
		h, st := newAdminSearchHarness()
		st.perms = map[string]bool{"*.*": true}
		sess := map[string]any{"user_id": "u2", "auth_type": "session"}
		code, body := adminSearchCall(h, "q=x&types=product,reseller", sess)
		if code != http.StatusOK {
			t.Fatalf("%d %s", code, body)
		}
		if len(st.lastOpts.Types) != 2 {
			t.Errorf("types = %v, want both", st.lastOpts.Types)
		}
	})

	t.Run("permission lookup failure is 500", func(t *testing.T) {
		h, st := newAdminSearchHarness()
		st.permsErr = errors.New("db down")
		sess := map[string]any{"user_id": "u2", "auth_type": "session"}
		code, _ := adminSearchCall(h, "q=x", sess)
		if code != http.StatusInternalServerError {
			t.Errorf("code = %d, want 500", code)
		}
	})

	t.Run("no session identity is 401", func(t *testing.T) {
		h, st := newAdminSearchHarness()
		code, _ := adminSearchCall(h, "q=x", nil)
		if code != http.StatusUnauthorized {
			t.Errorf("code = %d, want 401", code)
		}
		if st.searchCall != 0 {
			t.Errorf("store was called %d times, want 0", st.searchCall)
		}
	})
}

// ── merge ────────────────────────────────────────────────────────────

// Per-type groups interleave round-robin so one busy type cannot fill
// the page, and the combined list truncates to the request's limit.
func TestAdminSearchMergeInterleavesAndTruncates(t *testing.T) {
	h, st := newAdminSearchHarness()
	var hits []store.SearchHit
	for i := 1; i <= 3; i++ {
		hits = append(hits, store.SearchHit{Type: store.SearchTypeProduct, ID: "p" + string(rune('0'+i)), Title: "P"})
	}
	for i := 1; i <= 3; i++ {
		hits = append(hits, store.SearchHit{Type: store.SearchTypeOrder, ID: "o" + string(rune('0'+i)), Title: "O"})
	}
	st.hits = hits

	code, body := adminSearchCall(h, "q=x&limit=4", adminSession())
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var parsed struct {
		Data struct {
			Results []struct {
				Type string `json:"type"`
				ID   string `json:"id"`
			} `json:"results"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("parse: %v (%s)", err, body)
	}
	if len(parsed.Data.Results) != 4 {
		t.Fatalf("got %d results, want 4 (%s)", len(parsed.Data.Results), body)
	}
	wantTypes := []string{store.SearchTypeProduct, store.SearchTypeOrder, store.SearchTypeProduct, store.SearchTypeOrder}
	wantIDs := []string{"p1", "o1", "p2", "o2"}
	for i, row := range parsed.Data.Results {
		if row.Type != wantTypes[i] || row.ID != wantIDs[i] {
			t.Errorf("result[%d] = %s/%s, want %s/%s", i, row.Type, row.ID, wantTypes[i], wantIDs[i])
		}
	}
}

// mustJSON renders for the [] vs null assertions above.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
