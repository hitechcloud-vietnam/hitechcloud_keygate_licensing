package service

import (
	"context"
	"errors"
	"testing"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/config"
)

// TestApplyBootOverlay pins the boot overlay: the settings table wins
// over env for the restart-scoped catalog keys, and the two derived/
// legacy exceptions (stripe livemode derivation, quota legacy float)
// behave as documented.
func TestApplyBootOverlay(t *testing.T) {
	ctx := context.Background()

	t.Run("db values land on config fields", func(t *testing.T) {
		svc, f, _ := cfgsvcTestService(t)
		f.rows["app.base_url"] = "https://db.example.com"
		f.rows["app.environment"] = "production"
		f.rows["ratelimit.api"] = "999"
		f.rows["ratelimit.bf_max_fails"] = "9"
		f.rows["webhook.max_attempts"] = "7"
		f.rows["webhook.retry_interval"] = "45s"
		f.rows["webhook.allow_private"] = "true"
		f.rows["smtp.host"] = "smtp.db.example.com"
		f.rows["smtp.port"] = "465"
		f.rows["storage.bucket"] = "db-bucket"
		f.rows["storage.upload_ttl"] = "2h"
		f.rows["storage.max_release_sign_size_mb"] = "250"
		f.rows["observability.log_level"] = "debug"
		f.rows["app.admin_emails"] = "Admin@Example.com, second@example.com"
		f.rows["app.quota_warning_threshold"] = "5000"
		f.secrets["smtp.password"] = "hunter2"

		// env is the loser everywhere it overlaps.
		t.Setenv("RATE_LIMIT_API", "60")

		cfg := &config.Config{RateLimitAPI: 60, QuotaWarningThreshold: 0.8}
		svc.ApplyBootOverlay(ctx, cfg)

		if cfg.BaseURL != "https://db.example.com" {
			t.Errorf("BaseURL = %q", cfg.BaseURL)
		}
		if cfg.Environment != "production" {
			t.Errorf("Environment = %q", cfg.Environment)
		}
		if cfg.RateLimitAPI != 999 {
			t.Errorf("RateLimitAPI = %d, want 999", cfg.RateLimitAPI)
		}
		if cfg.BFMaxFails != 9 {
			t.Errorf("BFMaxFails = %d, want 9", cfg.BFMaxFails)
		}
		if cfg.WebhookMaxAttempts != 7 {
			t.Errorf("WebhookMaxAttempts = %d, want 7", cfg.WebhookMaxAttempts)
		}
		if cfg.WebhookRetryInterval != "45s" {
			t.Errorf("WebhookRetryInterval = %q", cfg.WebhookRetryInterval)
		}
		if !cfg.WebhookAllowPrivate {
			t.Error("WebhookAllowPrivate = false, want true")
		}
		if cfg.SMTPHost != "smtp.db.example.com" || cfg.SMTPPort != "465" {
			t.Errorf("smtp = %q:%q", cfg.SMTPHost, cfg.SMTPPort)
		}
		if cfg.SMTPPassword != "hunter2" {
			t.Errorf("SMTPPassword = %q", cfg.SMTPPassword)
		}
		if cfg.StorageBucket != "db-bucket" {
			t.Errorf("StorageBucket = %q", cfg.StorageBucket)
		}
		if cfg.StorageUploadTTL != "2h" {
			t.Errorf("StorageUploadTTL = %q", cfg.StorageUploadTTL)
		}
		if cfg.MaxReleaseSignSize != 250*1024*1024 {
			t.Errorf("MaxReleaseSignSize = %d", cfg.MaxReleaseSignSize)
		}
		if cfg.LogLevel != "debug" {
			t.Errorf("LogLevel = %q", cfg.LogLevel)
		}
		if len(cfg.AdminEmails) != 2 || cfg.AdminEmails[0] != "admin@example.com" || cfg.AdminEmails[1] != "second@example.com" {
			t.Errorf("AdminEmails = %v", cfg.AdminEmails)
		}
		if cfg.QuotaWarningThreshold != 0.5 {
			t.Errorf("QuotaWarningThreshold = %v, want 0.5 (5000bps)", cfg.QuotaWarningThreshold)
		}
	})

	t.Run("quota legacy float survives when catalog key unset", func(t *testing.T) {
		svc, _, _ := cfgsvcTestService(t)
		cfg := &config.Config{QuotaWarningThreshold: 0.42}
		svc.ApplyBootOverlay(ctx, cfg)
		if cfg.QuotaWarningThreshold != 0.42 {
			t.Errorf("QuotaWarningThreshold = %v, want legacy 0.42 preserved", cfg.QuotaWarningThreshold)
		}
	})

	t.Run("stripe livemode keeps sk_live derivation", func(t *testing.T) {
		svc, f, _ := cfgsvcTestService(t)
		f.secrets["payment.stripe_secret_key"] = "sk_live_abc"
		cfg := &config.Config{}
		svc.ApplyBootOverlay(ctx, cfg)
		if !cfg.StripeLivemode {
			t.Error("StripeLivemode = false, want derived true from sk_live_")
		}
	})

	t.Run("empty settings degrade to env-loaded config", func(t *testing.T) {
		svc, _, _ := cfgsvcTestService(t)
		cfg := &config.Config{
			BaseURL:            "http://env:9000",
			RateLimitAPI:       60,
			WebhookHTTPTimeout: "10s",
		}
		svc.ApplyBootOverlay(ctx, cfg)
		if cfg.BaseURL != "http://env:9000" {
			t.Errorf("BaseURL = %q, want env value kept", cfg.BaseURL)
		}
		if cfg.RateLimitAPI != 60 {
			t.Errorf("RateLimitAPI = %d, want default 60", cfg.RateLimitAPI)
		}
		if cfg.WebhookHTTPTimeout != "10s" {
			t.Errorf("WebhookHTTPTimeout = %q", cfg.WebhookHTTPTimeout)
		}
	})

	t.Run("read errors never wipe env values", func(t *testing.T) {
		svc, f, _ := cfgsvcTestService(t)
		f.getErr = errors.New("boom")
		cfg := &config.Config{BaseURL: "http://env:9000", RateLimitAPI: 60}
		svc.ApplyBootOverlay(ctx, cfg)
		if cfg.BaseURL != "http://env:9000" || cfg.RateLimitAPI != 60 {
			t.Errorf("overlay wiped config on read error: %+v", cfg)
		}
	})

	t.Run("nil config is a no-op", func(t *testing.T) {
		svc, _, _ := cfgsvcTestService(t)
		svc.ApplyBootOverlay(ctx, nil)
	})
}
