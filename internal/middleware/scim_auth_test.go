package middleware

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// fakeSCIMStore resolves tokens by their presented hash. A hash absent
// from the map answers sql.ErrNoRows (unknown token); a nil value answers
// (nil, nil) so the middleware's nil-token guard is exercised.
type fakeSCIMStore struct {
	byHash  map[string]*model.SCIMToken
	touched []string
}

func (f *fakeSCIMStore) FindSCIMTokenByHash(_ context.Context, keyHash string) (*model.SCIMToken, error) {
	if tok, ok := f.byHash[keyHash]; ok {
		return tok, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeSCIMStore) TouchSCIMTokenLastUsed(_ context.Context, id string) error {
	f.touched = append(f.touched, id)
	return nil
}

// scimRouter builds a router with the SCIM auth middleware in front of a
// sentinel handler that reports whether it was reached and what it saw.
func scimRouter(store SCIMTokenStore) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(SCIMTokenAuth(store))
	r.GET("/scim/v2/Users", func(c *gin.Context) {
		id, _ := c.Get("scim_token_id")
		_, hasToken := c.Get("scim_token")
		_, hasAdmin := c.Get("is_admin")
		c.JSON(http.StatusOK, gin.H{"reached": true, "id": id, "hasToken": hasToken, "hasAdmin": hasAdmin})
	})
	return r
}

func doSCIM(t *testing.T, r *gin.Engine, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	r.ServeHTTP(w, req)
	return w
}

// goodToken registers a valid token and returns its plaintext.
func goodToken(t *testing.T, f *fakeSCIMStore) string {
	t.Helper()
	plain, _, err := model.NewSCIMToken()
	if err != nil {
		t.Fatal(err)
	}
	f.byHash[model.HashSCIMToken(plain)] = &model.SCIMToken{ID: "tok-ok"}
	return plain
}

func TestSCIMTokenAuthSuccess(t *testing.T) {
	f := &fakeSCIMStore{byHash: map[string]*model.SCIMToken{}}
	plain := goodToken(t, f)
	r := scimRouter(f)

	w := doSCIM(t, r, "Bearer "+plain)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !contains(body, `"reached":true`) {
		t.Fatalf("handler not reached: %s", body)
	}
	if !contains(body, `"id":"tok-ok"`) {
		t.Fatalf("scim_token_id not set: %s", body)
	}
	if !contains(body, `"hasToken":true`) {
		t.Fatalf("scim_token not set: %s", body)
	}
	// A SCIM token must NOT look like an admin/user session.
	if contains(body, `"hasAdmin":true`) {
		t.Fatalf("is_admin must not be set for a SCIM token: %s", body)
	}
	if len(f.touched) != 1 || f.touched[0] != "tok-ok" {
		t.Fatalf("last_used_at not stamped: %v", f.touched)
	}
}

func TestSCIMTokenAuthAllRefusalsIdentical(t *testing.T) {
	f := &fakeSCIMStore{byHash: map[string]*model.SCIMToken{}}
	good := goodToken(t, f)
	_ = good

	// A token that IS in the map but revoked.
	revokedPlain, _, err := model.NewSCIMToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	f.byHash[model.HashSCIMToken(revokedPlain)] = &model.SCIMToken{ID: "tok-rev", RevokedAt: &now}

	// An unknown-but-correctly-shaped token.
	unknownPlain, _, err := model.NewSCIMToken()
	if err != nil {
		t.Fatal(err)
	}

	r := scimRouter(f)

	cases := []struct {
		name   string
		header string
	}{
		{"missing header", ""},
		{"wrong namespace", "Bearer kg_live_notascimtoken"},
		{"bearer of empty", "Bearer "},
		{"not bearer", "Basic dXNlcjpwYXNz"},
		{"unknown token", "Bearer " + unknownPlain},
		{"revoked token", "Bearer " + revokedPlain},
	}

	var first string
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := doSCIM(t, r, tc.header)
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
			body := w.Body.String()
			if tc.name == "missing header" {
				first = body
			} else if body != first {
				t.Fatalf("refusal body differs:\n%s\nvs\n%s", first, body)
			}
		})
	}
}

func TestSCIMTokenAuthIsSCIMErrorShape(t *testing.T) {
	f := &fakeSCIMStore{byHash: map[string]*model.SCIMToken{}}
	r := scimRouter(f)
	w := doSCIM(t, r, "")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !contains(body, "urn:ietf:params:scim:api:messages:2.0:Error") {
		t.Fatalf("refusal is not SCIM error shape: %s", body)
	}
	if !contains(body, `"status":"401"`) {
		t.Fatalf("SCIM error status should be the string \"401\": %s", body)
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
