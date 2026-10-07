package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// The Velopack route answers {base}/{file} where the base URL may end in
// a platform. These requests are rejected before any lookup, so a bare
// handler is enough to check how the path is split.
func TestFeedVelopackIndexPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/releases/:product_slug/velopack/*path", (&ReleasePublicHandler{}).FeedVelopackIndex)

	for _, tc := range []struct {
		path string
		want int
	}{
		// The Rust core sends no rid, so without a platform in the path
		// or the channel name there is nothing to serve.
		{"/releases/app/velopack/releases.osx.json?localVersion=1.0.0&id=App&stagingId=x", http.StatusBadRequest},
		{"/releases/app/velopack/solaris-x64/releases.osx.json", http.StatusBadRequest},
		{"/releases/app/velopack/releases.osx.json?rid=freebsd-x64", http.StatusBadRequest},
		{"/releases/app/velopack/notes.txt", http.StatusNotFound},
		{"/releases/app/velopack/darwin-arm64/extra/releases.osx.json", http.StatusNotFound},
		{"/releases/app/velopack/", http.StatusNotFound},
		{"/releases/app/velopack/darwin-arm64/not-a-package.nupkg", http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.want {
			t.Errorf("GET %s = %d, want %d: %s", tc.path, w.Code, tc.want, w.Body.String())
		}
	}
}
