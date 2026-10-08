package handler

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

var testSortColumns = map[string]sortCol{
	"created_at": {Expr: "license.created_at", Desc: true},
	"email":      {Expr: "license.email"},
	"status":     {Expr: "license.status"},
}

// What ?sort= and ?order= resolve to. The allowlist is the security
// boundary: the expression that reaches the ORDER BY has to be one of
// ours no matter what the caller sent.
func TestListSort(t *testing.T) {
	for _, tc := range []struct {
		query    string
		wantExpr string
		wantDesc bool
	}{
		// Nothing asked for: the endpoint's default column, in the
		// direction that column reads in.
		{"", "license.created_at", true},
		{"?sort=created_at", "license.created_at", true},
		// A name column defaults the other way round. One default per
		// endpoint would have sent this back Z to A.
		{"?sort=email", "license.email", false},
		{"?sort=status", "license.status", false},
		// An explicit order wins over the column's own default, both ways.
		{"?sort=created_at&order=asc", "license.created_at", false},
		{"?sort=email&order=desc", "license.email", true},
		{"?order=asc", "license.created_at", false},
		// An empty ?sort= reads as "not asked for", not as an unknown
		// column: a client that clears its own state still gets a list.
		{"?sort=", "license.created_at", true},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/admin/licenses"+tc.query, nil)
		got, ok := listSort(c, testSortColumns, "created_at")
		if !ok {
			t.Errorf("listSort(%q) refused, want accepted (%d)", tc.query, w.Code)
			continue
		}
		if got.Expr != tc.wantExpr || got.Desc != tc.wantDesc {
			t.Errorf("listSort(%q) = {Expr:%q Desc:%v}, want {Expr:%q Desc:%v}",
				tc.query, got.Expr, got.Desc, tc.wantExpr, tc.wantDesc)
		}
	}
}

// Anything not in the allowlist is refused rather than quietly served
// in the default order, and refused before it can reach a query.
func TestListSortRejects(t *testing.T) {
	for _, tc := range []struct {
		query    string
		wantCode string
	}{
		{"?sort=nope", "INVALID_SORT"},
		{"?sort=license.created_at", "INVALID_SORT"},
		{"?sort=id", "INVALID_SORT"},
		// The shapes an injection attempt takes, spelled the way a
		// client would have to send them. None is a key in the map,
		// so none gets past this function.
		{"?sort=" + url.QueryEscape("email; DROP TABLE licenses"), "INVALID_SORT"},
		{"?sort=" + url.QueryEscape("email--"), "INVALID_SORT"},
		{"?sort=" + url.QueryEscape("(SELECT 1)"), "INVALID_SORT"},
		{"?sort=email&order=" + url.QueryEscape("asc; DELETE FROM licenses"), "INVALID_ORDER"},
		// Case matters: "ASC" is not the spelling the contract names.
		{"?sort=email&order=ASC", "INVALID_ORDER"},
		{"?sort=email&order=ascending", "INVALID_ORDER"},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/admin/licenses"+tc.query, nil)
		got, ok := listSort(c, testSortColumns, "created_at")
		if ok {
			t.Errorf("listSort(%q) accepted and returned %q, want refused", tc.query, got.Expr)
			continue
		}
		if got.Expr != "" {
			t.Errorf("listSort(%q) refused but still returned Expr %q", tc.query, got.Expr)
		}
		if w.Code != 400 {
			t.Errorf("listSort(%q) status = %d, want 400", tc.query, w.Code)
		}
		var body struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("listSort(%q) body is not the error envelope: %v", tc.query, err)
		}
		if body.Error.Code != tc.wantCode {
			t.Errorf("listSort(%q) code = %q, want %q", tc.query, body.Error.Code, tc.wantCode)
		}
	}
}

// The refusal has to say what would have worked, in a stable order, or
// it is a 400 the caller can only fix by reading the source.
func TestListSortErrorNamesTheColumns(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/admin/licenses?sort=nope", nil)
	listSort(c, testSortColumns, "created_at")

	msg := w.Body.String()
	for name := range testSortColumns {
		if !strings.Contains(msg, name) {
			t.Errorf("refusal does not mention sortable column %q: %s", name, msg)
		}
	}
	if !strings.Contains(msg, "created_at, email, status") {
		t.Errorf("column list is not in a stable order: %s", msg)
	}
}

// Every column the license list advertises has to be one the query can
// actually order by, and every expression has to be qualified: an
// unqualified "name" is ambiguous once product and plan are joined.
func TestLicenseSortColumnsAreQualified(t *testing.T) {
	if len(licenseSortColumns) == 0 {
		t.Fatal("no sortable columns declared for the license list")
	}
	for name, col := range licenseSortColumns {
		if col.Expr == "" {
			t.Errorf("sort column %q has no expression", name)
		}
		if !strings.Contains(col.Expr, ".") {
			t.Errorf("sort column %q resolves to %q, which is not table-qualified", name, col.Expr)
		}
		// Whatever the map says, it must not be something a caller
		// could have injected: no spaces, no punctuation beyond the
		// table qualifier and the parentheses of a function call such
		// as lower(license.email).
		for _, r := range col.Expr {
			if r != '.' && r != '_' && r != '(' && r != ')' && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') {
				t.Errorf("sort column %q expression %q contains unexpected character %q", name, col.Expr, r)
			}
		}
	}
	if _, ok := licenseSortColumns["created_at"]; !ok {
		t.Error("the license list's default sort column is not in its own allowlist")
	}
}

// The refusal quotes back what was asked for, so what was asked for
// has to be bounded. A query string can carry a great deal of it, and
// echoing it whole makes a 400 an amplifier: what goes in comes back,
// and goes into every log line that records the error.
func TestListSortRefusalDoesNotEchoTheWholeQuery(t *testing.T) {
	huge := strings.Repeat("a", 50000)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/admin/licenses?sort="+url.QueryEscape(huge), nil)

	if _, ok := listSort(c, testSortColumns, "created_at"); ok {
		t.Fatal("a 50k column name was accepted")
	}
	if w.Code != 400 {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if n := w.Body.Len(); n > 1000 {
		t.Errorf("refusal is %d bytes for a %d byte input; it should not scale with the query", n, len(huge))
	}
	// It still has to say enough to be useful.
	if !strings.Contains(w.Body.String(), "aaaa") {
		t.Error("the refusal no longer shows any of what was asked for")
	}
	for name := range testSortColumns {
		if !strings.Contains(w.Body.String(), name) {
			t.Errorf("the refusal stopped naming the valid column %q", name)
		}
	}
}

// The lenient twin: the §70 admin ledger lists treat ?sort= as a
// nicety, not a contract. An unknown sorter — a hand-written script, a
// client clearing its own state — must still get its rows, in the
// order the endpoint always answered in. What does NOT relax is the
// security boundary: only an allowlisted expression can reach the
// ORDER BY, and an unknown name resolves to the default column
// rather than to whatever was sent.
func TestListSortOrDefault(t *testing.T) {
	for _, tc := range []struct {
		query    string
		wantExpr string
		wantDesc bool
	}{
		// Nothing asked for: the endpoint's default, in the direction
		// that column reads in.
		{"", "license.created_at", true},
		{"?sort=", "license.created_at", true},
		{"?sort=created_at", "license.created_at", true},
		// A named column, both directions.
		{"?sort=email", "license.email", false},
		{"?sort=email&order=asc", "license.email", false},
		{"?sort=email&order=desc", "license.email", true},
		{"?sort=created_at&order=asc", "license.created_at", false},
		// Unknown column: today's default ordering, not a 400.
		{"?sort=nope", "license.created_at", true},
		{"?sort=license.created_at", "license.created_at", true},
		{"?sort=" + url.QueryEscape("email; DROP TABLE licenses"), "license.created_at", true},
		// ...and ?order= still applies to whatever column resolved.
		{"?sort=nope&order=asc", "license.created_at", false},
		// A malformed ?order= is not a direction the contract names:
		// it takes descending, never the caller's spelling. "ASC" is
		// malformed exactly as it is for the strict listSort.
		{"?sort=email&order=bogus", "license.email", true},
		{"?sort=email&order=ASC", "license.email", true},
		{"?sort=email&order=" + url.QueryEscape("asc; DELETE FROM licenses"), "license.email", true},
	} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/admin/orders"+tc.query, nil)
		got := listSortOrDefault(c, testSortColumns, "created_at")
		if got.Expr != tc.wantExpr || got.Desc != tc.wantDesc {
			t.Errorf("listSortOrDefault(%q) = {Expr:%q Desc:%v}, want {Expr:%q Desc:%v}",
				tc.query, got.Expr, got.Desc, tc.wantExpr, tc.wantDesc)
		}
	}
}

// Lenient means silent: no error envelope, no status of its own. The
// list that called this answers 200 with rows whatever the caller
// sent in ?sort= and ?order=.
func TestListSortOrDefaultWritesNothing(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/admin/orders?sort=nope&order=nope", nil)
	listSortOrDefault(c, testSortColumns, "created_at")
	if w.Code != 200 {
		t.Errorf("status = %d, want the untouched 200", w.Code)
	}
	if w.Body.Len() != 0 {
		t.Errorf("listSortOrDefault wrote %d bytes; it must not answer at all", w.Body.Len())
	}
}

// Every §70 allowlist, under the same rule the license list is held
// to: qualified expressions only (a quoted alias for a reserved-word
// table counts as qualified — "order".created_at carries its own
// qualifier inside the quotes), each map naming its own default. The
// character set is widened by the quote for the reserved-word
// aliases; anything else in an expression is still a bug.
func TestAdminSortColumnMapsAreQualified(t *testing.T) {
	maps := map[string]map[string]sortCol{
		"products":      productSortColumns,
		"users":         userSortColumns,
		"audit_logs":    auditSortColumns,
		"orders":        orderSortColumns,
		"invoices":      invoiceSortColumns,
		"coupons":       couponSortColumns,
		"releases":      releaseSortColumns,
		"notifications": notificationSortColumns,
		"refunds":       refundSortColumns,
	}
	for res, m := range maps {
		if len(m) == 0 {
			t.Errorf("%s has no sortable columns declared", res)
			continue
		}
		if _, ok := m["created_at"]; !ok {
			t.Errorf("%s allowlist does not name its own default column created_at", res)
		}
		for name, col := range m {
			if col.Expr == "" {
				t.Errorf("%s sort column %q has no expression", res, name)
			}
			if !strings.Contains(col.Expr, ".") {
				t.Errorf("%s sort column %q resolves to %q, which is not table-qualified", res, name, col.Expr)
			}
			for _, r := range col.Expr {
				if r != '.' && r != '_' && r != '(' && r != ')' && r != '"' &&
					!(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z') {
					t.Errorf("%s sort column %q expression %q contains unexpected character %q", res, name, col.Expr, r)
				}
			}
		}
	}
}
