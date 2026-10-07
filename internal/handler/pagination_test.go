package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// What a list endpoint does with the window a caller asks for. The
// clamp is the safety here: no query string may talk the server into
// reading an unbounded slice of a table, and nothing a caller sends
// turns into an error they have to handle.
func TestListPage(t *testing.T) {
	for _, tc := range []struct {
		query      string
		wantLimit  int
		wantOffset int
	}{
		{"", defaultListLimit, 0},
		{"?limit=10&offset=20", 10, 20},
		{"?limit=200", 200, 0},
		{"?limit=201", maxListLimit, 0},
		{"?limit=100000", maxListLimit, 0},
		// Zero is not "no limit": that page exists only for callers
		// inside the process.
		{"?limit=0", defaultListLimit, 0},
		{"?limit=-5", defaultListLimit, 0},
		{"?limit=abc", defaultListLimit, 0},
		{"?offset=-1", defaultListLimit, 0},
		{"?offset=abc", defaultListLimit, 0},
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/admin/products"+tc.query, nil)
		got := listPage(c)
		if got.Limit != tc.wantLimit || got.Offset != tc.wantOffset {
			t.Errorf("listPage(%q) = {Limit:%d Offset:%d}, want {Limit:%d Offset:%d}",
				tc.query, got.Limit, got.Offset, tc.wantLimit, tc.wantOffset)
		}
	}
}

// The rows travel under their own name, the three numbers beside them,
// and an empty list is an empty array rather than null.
func TestListOKShape(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/admin/products?limit=2&offset=4", nil)
	listOK(c, "products", []string(nil), 9, listPage(c))
	const want = `{"success":true,"data":{"limit":2,"offset":4,"products":[],"total":9}}`
	if got := w.Body.String(); got != want {
		t.Errorf("listOK wrote %s, want %s", got, want)
	}
}
