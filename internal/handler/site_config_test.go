package handler

// GET /api/v1/site-config extends the public /api/v1/config contract
// rather than replacing it: the branding keys the SPA already reads
// keep their names, the attribution fields (AGPL v3 Section 7(b) —
// legally required output) are always present, and the two new shapes
// — surfaces (id → host) and payment_methods ({id, name}) — are added
// beside them. No DB is needed: the store getters are faked.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/branding"
)

func surfSiteConfigHandler(vals map[string]string, public map[string]string, publicErr error, providers []string) *SiteConfigHandler {
	return &SiteConfigHandler{
		surfGetPublic: func(context.Context) (map[string]string, error) {
			return public, publicErr
		},
		surfGetSetting: func(_ context.Context, key string) (string, error) {
			v, ok := vals[key]
			if !ok {
				return "", sql.ErrNoRows
			}
			return v, nil
		},
		surfListProviders: func() []string { return providers },
		surfBaseURL:       "https://example.com",
	}
}

func surfGetSiteConfig(t *testing.T, h *SiteConfigHandler) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/site-config", h.Get)
	req := httptest.NewRequest("GET", "/api/v1/site-config", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not the envelope: %v\n%s", err, w.Body.String())
	}
	if !env.Success {
		t.Fatalf("want success envelope, got %s", w.Body.String())
	}
	data := map[string]any{}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("data is not an object: %v", err)
	}
	return w.Code, data
}

func TestSiteConfigExtendsThePublicContract(t *testing.T) {
	h := surfSiteConfigHandler(
		map[string]string{
			"domain.base":     "example.com",
			"domain.payments": "pay.example.com",
			"domain.cdn":      "cdn.example.com",
		},
		map[string]string{
			"site_name":   "Acme",
			"brand_color": "#123456",
			"logo_url":    "/logo.svg",
			"timezone":    "Asia/Ho_Chi_Minh",
			"language":    "en",
		},
		nil,
		[]string{"payos", "pay2s"}, // registry order is not stable; the response sorts
	)
	code, data := surfGetSiteConfig(t, h)
	if code != http.StatusOK {
		t.Fatalf("want 200, got %d", code)
	}

	// The existing /api/v1/config keys keep their names and meanings.
	for key, want := range map[string]string{
		"site_name":   "Acme",
		"brand_color": "#123456",
		"logo_url":    "/logo.svg",
		"timezone":    "Asia/Ho_Chi_Minh",
		"language":    "en",
		"base_url":    "https://example.com",
	} {
		if got, _ := data[key].(string); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	// Attribution is legally required output (AGPL v3 Section 7(b)).
	if got, _ := data["attribution_text"].(string); got != branding.Tagline || got != "Powered by Keygate" {
		t.Errorf("attribution_text = %q, want %q", got, branding.Tagline)
	}
	if got, _ := data["attribution_url"].(string); got != branding.URL {
		t.Errorf("attribution_url = %q, want %q", got, branding.URL)
	}

	// The domain map: explicit override + derived defaults, opt-ins
	// only when configured.
	wantSurfaces := map[string]any{
		"apex":      "example.com",
		"payments":  "pay.example.com",
		"dashboard": "dashboard.example.com",
		"merchant":  "merchant.example.com",
		"customer":  "customer.example.com",
		"verify":    "verify.example.com",
		"cdn":       "cdn.example.com",
	}
	surfaces, _ := data["surfaces"].(map[string]any)
	if len(surfaces) != len(wantSurfaces) {
		t.Errorf("surfaces = %v, want %v", surfaces, wantSurfaces)
	}
	for k, v := range wantSurfaces {
		if surfaces[k] != v {
			t.Errorf("surfaces[%q] = %v, want %v", k, surfaces[k], v)
		}
	}

	// Payment methods carry the same display names as the gateway
	// checkout, sorted for a stable contract.
	raw, _ := json.Marshal(data["payment_methods"])
	var methods []surfPaymentMethod
	if err := json.Unmarshal(raw, &methods); err != nil {
		t.Fatalf("payment_methods: %v", err)
	}
	wantMethods := []surfPaymentMethod{{ID: "pay2s", Name: "Pay2S"}, {ID: "payos", Name: "payOS"}}
	if len(methods) != len(wantMethods) || methods[0] != wantMethods[0] || methods[1] != wantMethods[1] {
		t.Errorf("payment_methods = %+v, want %+v", methods, wantMethods)
	}
}

func TestSiteConfigAttributionSurvivesAFailedSettingsRead(t *testing.T) {
	// The branding read failing must not drop the legal notice — the
	// page still renders and the attribution is still there.
	h := surfSiteConfigHandler(nil, nil, sql.ErrConnDone, nil)
	code, data := surfGetSiteConfig(t, h)
	if code != http.StatusOK {
		t.Fatalf("want 200 even when the settings read fails, got %d", code)
	}
	for _, key := range []string{"attribution_text", "attribution_url"} {
		if v, _ := data[key].(string); v == "" {
			t.Errorf("%s must never be omitted", key)
		}
	}
	// No providers configured → empty list, not null.
	raw, _ := json.Marshal(data["payment_methods"])
	if string(raw) != "[]" {
		t.Errorf("payment_methods = %s, want []", raw)
	}
}
