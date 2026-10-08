package handler

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/config"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// configsvcFakeSettings is the ConfigServiceStore for handler tests.
type configsvcFakeSettings struct {
	rows    map[string]string
	secrets map[string]string
	setErr  error
}

func configsvcNewFakeSettings() *configsvcFakeSettings {
	return &configsvcFakeSettings{rows: map[string]string{}, secrets: map[string]string{}}
}

func (f *configsvcFakeSettings) GetSettings(ctx context.Context) (map[string]string, error) {
	out := make(map[string]string, len(f.rows))
	for k, v := range f.rows {
		out[k] = v
	}
	return out, nil
}

func (f *configsvcFakeSettings) GetSecretSetting(ctx context.Context, key string) (string, error) {
	return f.secrets[key], nil
}

func (f *configsvcFakeSettings) SetSettings(ctx context.Context, settings map[string]string) error {
	if f.setErr != nil {
		return f.setErr
	}
	for k, v := range settings {
		if store.IsSecretSettingKey(k) {
			f.secrets[k] = v
		} else {
			f.rows[k] = v
		}
	}
	return nil
}

func (f *configsvcFakeSettings) DeleteSetting(ctx context.Context, key string) error {
	delete(f.rows, key)
	delete(f.secrets, key)
	return nil
}

// configsvcFakeAuditor captures the audit trail.
type configsvcFakeAuditor struct {
	logs []model.AuditLog
}

func (f *configsvcFakeAuditor) Audit(_ context.Context, log *model.AuditLog) {
	f.logs = append(f.logs, *log)
}

// configsvcHandler builds the handler under test with its fakes.
func configsvcHandler(t *testing.T) (*ConfigAdminHandler, *configsvcFakeSettings, *configsvcFakeAuditor) {
	t.Helper()
	fs := configsvcNewFakeSettings()
	aud := &configsvcFakeAuditor{}
	h := NewConfigAdminHandler(nil, service.NewConfigService(fs))
	h.audit = aud
	return h, fs, aud
}

func configsvcCtx(t *testing.T, method, target, body string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var r *strings.Reader
	if body == "" {
		r = strings.NewReader("")
	} else {
		r = strings.NewReader(body)
	}
	c.Request = httptest.NewRequest(method, target, r)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("user_id", "admin-1")
	return w, c
}

type configsvcEnvelope struct {
	Success bool            `json:"success"`
	Data    json.RawMessage `json:"data"`
	Error   *struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details"`
	} `json:"error"`
}

func configsvcDecode(t *testing.T, w *httptest.ResponseRecorder) configsvcEnvelope {
	t.Helper()
	var env configsvcEnvelope
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	return env
}

// TestConfigAdminResetKey pins the POST /:key/reset contract: same
// {reset: key} body as DELETE.
func TestConfigAdminResetKey(t *testing.T) {
	h, _, aud := configsvcHandler(t)
	w, c := configsvcCtx(t, "POST", "/api/v1/admin/config/site_name/reset", "")
	c.Params = gin.Params{{Key: "key", Value: "site_name"}}
	h.ResetConfigKey(c)
	if w.Code != 200 {
		t.Fatalf("POST reset failed: %s", w.Body.String())
	}
	var data map[string]any
	if err := json.Unmarshal(configsvcDecode(t, w).Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["reset"] != "site_name" {
		t.Fatalf("reset = %v, want {reset: site_name}", data)
	}
	if len(aud.logs) != 1 || aud.logs[0].Action != "config.reset" {
		t.Fatalf("audit = %+v, want one config.reset", aud.logs)
	}
}

// TestConfigAdminGetShape pins the GET contract the web UI builds
// against: categories → keys, and secrets ALWAYS masked.
func TestConfigAdminGetShape(t *testing.T) {
	h, fs, _ := configsvcHandler(t)
	fs.rows["smtp.port"] = "2525"
	fs.rows["ratelimit.api"] = "60" // == default → not "set"
	fs.secrets["payment.payos.checksum_key"] = "shh"

	w, c := configsvcCtx(t, "GET", "/api/v1/admin/config", "")
	h.GetConfig(c)

	env := configsvcDecode(t, w)
	if !env.Success {
		t.Fatalf("GET failed: %s", w.Body.String())
	}
	var data struct {
		Categories []struct {
			ID   string             `json:"id"`
			Name string             `json:"name"`
			Keys []configsvcKeyView `json:"keys"`
		} `json:"categories"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if len(data.Categories) != len(config.CategoryOrder()) {
		t.Fatalf("categories = %d, want %d", len(data.Categories), len(config.CategoryOrder()))
	}

	byKey := map[string]configsvcKeyView{}
	for _, cat := range data.Categories {
		if cat.Name == "" {
			t.Errorf("category %q has no name", cat.ID)
		}
		for _, k := range cat.Keys {
			if _, dup := byKey[k.Key]; dup {
				t.Errorf("key %q listed twice", k.Key)
			}
			byKey[k.Key] = k
		}
	}
	for _, e := range config.Catalog() {
		got, ok := byKey[e.Key]
		if !ok {
			t.Errorf("catalog key %q missing from GET", e.Key)
			continue
		}
		if got.Type != e.Type || got.EnvVar != e.EnvVar || got.Default != e.Default ||
			got.Description != e.Description || got.RestartRequired != e.RestartRequired {
			t.Errorf("%s: metadata mismatch: %+v", e.Key, got)
		}
		if config.IsSecretEntry(e) {
			if got.Value != "" {
				t.Errorf("secret %s returned a value: %q", e.Key, got.Value)
			}
			if !got.Secret {
				t.Errorf("%s must be flagged secret", e.Key)
			}
		}
	}

	if v := byKey["smtp.port"]; v.Value != "2525" || !v.Set {
		t.Errorf("smtp.port = %+v, want value 2525 set=true", v)
	}
	if v := byKey["ratelimit.api"]; v.Value != "60" || v.Set {
		t.Errorf("ratelimit.api = %+v, want value 60 (default) set=false", v)
	}
	if v := byKey["payment.payos.checksum_key"]; !v.Set {
		t.Errorf("configured secret must report set=true: %+v", v)
	}
}

// TestConfigAdminUpdateValid pins the PUT contract and its audit
// trail: one row per changed key, secrets masked.
func TestConfigAdminUpdateValid(t *testing.T) {
	h, fs, aud := configsvcHandler(t)

	w, c := configsvcCtx(t, "PUT", "/api/v1/admin/config",
		`{"values":{"smtp.port":"2525","site_name":"Acme"},`+
			`"secrets":{"payment.payos.checksum_key":"shh-new"}}`)
	h.UpdateConfig(c)

	env := configsvcDecode(t, w)
	if !env.Success {
		t.Fatalf("PUT failed: %s", w.Body.String())
	}
	var data struct {
		Updated           []string `json:"updated"`
		RestartedRequired []string `json:"restarted_required"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if len(data.Updated) != 3 {
		t.Fatalf("updated = %v, want 3 keys", data.Updated)
	}
	if fs.rows["smtp.port"] != "2525" || fs.rows["site_name"] != "Acme" ||
		fs.secrets["payment.payos.checksum_key"] != "shh-new" {
		t.Fatalf("stored: rows=%v secrets=%v", fs.rows, fs.secrets)
	}
	// smtp.port/site_name take effect at once; none of these is
	// restart-required — the flag list must reflect the catalog.
	for _, k := range data.RestartedRequired {
		if k == "smtp.port" || k == "site_name" {
			t.Errorf("%s must not be restart_required", k)
		}
	}

	if len(aud.logs) != 3 {
		t.Fatalf("audit rows = %d, want 3", len(aud.logs))
	}
	for _, lg := range aud.logs {
		if lg.Entity != "settings" || lg.Action != "config.update" ||
			lg.ActorType != "admin" || lg.ActorID != "admin-1" {
			t.Errorf("audit row wrong: %+v", lg)
		}
	}
	// The secret must never reach the audit log in any form.
	for _, lg := range aud.logs {
		if lg.EntityID == "payment.payos.checksum_key" {
			for _, v := range lg.Changes {
				if s, ok := v.(string); ok && strings.Contains(s, "shh-new") {
					t.Fatalf("audit row leaks the secret: %+v", lg.Changes)
				}
			}
			if lg.Changes["after"] != configsvcMask {
				t.Errorf("secret after = %v, want masked", lg.Changes["after"])
			}
		}
		if lg.EntityID == "site_name" && lg.Changes["after"] != "Acme" {
			t.Errorf("plain change not recorded: %+v", lg.Changes)
		}
	}
}

// TestConfigAdminUpdateRestartFlag pins restarted_required for a key
// the running server only reads at boot.
func TestConfigAdminUpdateRestartFlag(t *testing.T) {
	h, _, _ := configsvcHandler(t)
	w, c := configsvcCtx(t, "PUT", "/api/v1/admin/config",
		`{"values":{"ratelimit.api":"10","site_name":"x"}}`)
	h.UpdateConfig(c)

	env := configsvcDecode(t, w)
	if !env.Success {
		t.Fatalf("PUT failed: %s", w.Body.String())
	}
	var data struct {
		RestartedRequired []string `json:"restarted_required"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if len(data.RestartedRequired) != 1 || data.RestartedRequired[0] != "ratelimit.api" {
		t.Fatalf("restarted_required = %v, want [ratelimit.api]", data.RestartedRequired)
	}
}

// TestConfigAdminUpdateRejects pins the 400 contract: BAD_REQUEST with
// per-key details, and NOTHING written.
func TestConfigAdminUpdateRejects(t *testing.T) {
	h, fs, aud := configsvcHandler(t)

	w, c := configsvcCtx(t, "PUT", "/api/v1/admin/config",
		`{"values":{"ratelimit.api":"many","payment.pay2s.secret_key":"oops"},"secrets":{"smtp.port":"x"}}`)
	h.UpdateConfig(c)

	env := configsvcDecode(t, w)
	if env.Success || w.Code != 400 {
		t.Fatalf("status = %d body = %s, want 400", w.Code, w.Body.String())
	}
	if env.Error == nil || env.Error.Code != "BAD_REQUEST" {
		t.Fatalf("error = %+v, want BAD_REQUEST", env.Error)
	}
	var details map[string]string
	if err := json.Unmarshal(env.Error.Details, &details); err != nil {
		t.Fatalf("decode details: %v", err)
	}
	for _, key := range []string{"ratelimit.api", "payment.pay2s.secret_key", "smtp.port"} {
		if details[key] == "" {
			t.Errorf("missing detail for %q: %v", key, details)
		}
	}
	if len(fs.rows) != 0 || len(fs.secrets) != 0 {
		t.Errorf("refused batch must write nothing: %v %v", fs.rows, fs.secrets)
	}
	if len(aud.logs) != 0 {
		t.Errorf("refused batch must not audit: %v", aud.logs)
	}
}

// TestConfigAdminUpdateUnknownKey: an unknown key is a validation
// problem like any other (same 400, its own detail line).
func TestConfigAdminUpdateUnknownKey(t *testing.T) {
	h, _, _ := configsvcHandler(t)
	w, c := configsvcCtx(t, "PUT", "/api/v1/admin/config", `{"values":{"not.a.key":"1"}}`)
	h.UpdateConfig(c)
	env := configsvcDecode(t, w)
	if env.Success || w.Code != 400 || env.Error.Code != "BAD_REQUEST" {
		t.Fatalf("status = %d body = %s, want 400 BAD_REQUEST", w.Code, w.Body.String())
	}
}

// TestConfigAdminUpdateEncryptionMissing pins the 503 mapping when a
// secret has nowhere safe to go (no encryption key configured).
func TestConfigAdminUpdateEncryptionMissing(t *testing.T) {
	h, fs, _ := configsvcHandler(t)
	fs.setErr = store.ErrSecretEncryptionUnavailable
	w, c := configsvcCtx(t, "PUT", "/api/v1/admin/config",
		`{"secrets":{"smtp.password":"p"}}`)
	h.UpdateConfig(c)
	env := configsvcDecode(t, w)
	if env.Success || w.Code != 503 || env.Error.Code != "ENCRYPTION_NOT_CONFIGURED" {
		t.Fatalf("status = %d body = %s, want 503 ENCRYPTION_NOT_CONFIGURED", w.Code, w.Body.String())
	}
}

// TestConfigAdminDelete pins the reset contract: {reset: key}, an
// audit row, and the value back on the env/default fallback.
func TestConfigAdminDelete(t *testing.T) {
	h, fs, aud := configsvcHandler(t)
	fs.rows["site_name"] = "Something"

	w, c := configsvcCtx(t, "DELETE", "/api/v1/admin/config/site_name", "")
	c.Params = gin.Params{{Key: "key", Value: "site_name"}}
	h.DeleteConfig(c)

	env := configsvcDecode(t, w)
	if !env.Success {
		t.Fatalf("DELETE failed: %s", w.Body.String())
	}
	var data map[string]string
	if err := json.Unmarshal(env.Data, &data); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if data["reset"] != "site_name" {
		t.Fatalf("reset = %v, want {reset: site_name}", data)
	}
	if _, ok := fs.rows["site_name"]; ok {
		t.Fatal("row must be gone after reset")
	}
	if len(aud.logs) != 1 || aud.logs[0].Action != "config.reset" ||
		aud.logs[0].Entity != "settings" || aud.logs[0].EntityID != "site_name" {
		t.Fatalf("audit = %+v", aud.logs)
	}
	if aud.logs[0].Changes["before"] != "Something" {
		t.Errorf("before = %v", aud.logs[0].Changes["before"])
	}
}

// TestConfigAdminDeleteUnknown: no such key → 404, nothing audited.
func TestConfigAdminDeleteUnknown(t *testing.T) {
	h, _, aud := configsvcHandler(t)
	w, c := configsvcCtx(t, "DELETE", "/api/v1/admin/config/not.a.key", "")
	c.Params = gin.Params{{Key: "key", Value: "not.a.key"}}
	h.DeleteConfig(c)
	env := configsvcDecode(t, w)
	if env.Success || w.Code != 404 {
		t.Fatalf("status = %d body = %s, want 404", w.Code, w.Body.String())
	}
	if len(aud.logs) != 0 {
		t.Errorf("unknown key must not audit: %v", aud.logs)
	}
}

// TestConfigAdminDeleteSecretAuditsMasked: a reset secret is logged
// as presence, never as a value.
func TestConfigAdminDeleteSecretAuditsMasked(t *testing.T) {
	h, fs, aud := configsvcHandler(t)
	fs.secrets["smtp.password"] = "super-secret"

	w, c := configsvcCtx(t, "DELETE", "/api/v1/admin/config/smtp.password", "")
	c.Params = gin.Params{{Key: "key", Value: "smtp.password"}}
	h.DeleteConfig(c)
	if !configsvcDecode(t, w).Success {
		t.Fatalf("DELETE failed: %s", w.Body.String())
	}
	if len(aud.logs) != 1 {
		t.Fatalf("audit rows = %d", len(aud.logs))
	}
	for _, v := range aud.logs[0].Changes {
		if s, ok := v.(string); ok && strings.Contains(s, "super-secret") {
			t.Fatalf("audit leaks the secret: %+v", aud.logs[0].Changes)
		}
	}
	if aud.logs[0].Changes["before"] != configsvcMask {
		t.Errorf("before = %v, want masked", aud.logs[0].Changes["before"])
	}
}
