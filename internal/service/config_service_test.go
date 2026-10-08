package service

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/config"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/crypto"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// cfgsvcFakeStore is an in-memory ConfigServiceStore. It mirrors the
// settings layer's secret handling (secrets land in a separate map,
// keyed through store.IsSecretSettingKey) so tests can assert both the
// precedence rule and that secret writes never touch the plain rows.
type cfgsvcFakeStore struct {
	rows    map[string]string
	secrets map[string]string

	gets    int
	lastSet map[string]string
	deleted []string

	getErr error
	setErr error
}

func cfgsvcNewFake() *cfgsvcFakeStore {
	return &cfgsvcFakeStore{rows: map[string]string{}, secrets: map[string]string{}}
}

func (f *cfgsvcFakeStore) GetSettings(ctx context.Context) (map[string]string, error) {
	f.gets++
	if f.getErr != nil {
		return nil, f.getErr
	}
	out := make(map[string]string, len(f.rows))
	for k, v := range f.rows {
		out[k] = v
	}
	return out, nil
}

func (f *cfgsvcFakeStore) GetSecretSetting(ctx context.Context, key string) (string, error) {
	if f.getErr != nil {
		return "", f.getErr
	}
	return f.secrets[key], nil
}

func (f *cfgsvcFakeStore) SetSettings(ctx context.Context, settings map[string]string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.lastSet = map[string]string{}
	for k, v := range settings {
		f.lastSet[k] = v
		if store.IsSecretSettingKey(k) {
			f.secrets[k] = v
		} else {
			f.rows[k] = v
		}
	}
	return nil
}

func (f *cfgsvcFakeStore) DeleteSetting(ctx context.Context, key string) error {
	f.deleted = append(f.deleted, key)
	delete(f.rows, key)
	delete(f.secrets, key)
	return nil
}

// cfgsvcTestService builds a service over the fake with a frozen clock.
func cfgsvcTestService(t *testing.T) (*ConfigService, *cfgsvcFakeStore, *func(time.Time)) {
	t.Helper()
	f := cfgsvcNewFake()
	svc := NewConfigService(f)
	now := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	setNow := func(t2 time.Time) { now = t2 }
	svc.now = func() time.Time { return now }
	return svc, f, &setNow
}

// ─── Precedence: DB row > env var > catalog default ───

func TestConfigServicePrecedence(t *testing.T) {
	ctx := context.Background()

	t.Run("db wins over env", func(t *testing.T) {
		t.Setenv("SMTP_PORT", "465")
		svc, f, _ := cfgsvcTestService(t)
		f.rows["smtp.port"] = "2525"
		if got, err := svc.GetString(ctx, "smtp.port"); err != nil || got != "2525" {
			t.Fatalf("GetString = %q, %v; want 2525", got, err)
		}
	})

	t.Run("env wins over default", func(t *testing.T) {
		t.Setenv("SMTP_PORT", "465")
		svc, _, _ := cfgsvcTestService(t)
		if got, err := svc.GetString(ctx, "smtp.port"); err != nil || got != "465" {
			t.Fatalf("GetString = %q, %v; want 465", got, err)
		}
	})

	t.Run("default when nothing is set", func(t *testing.T) {
		t.Setenv("SMTP_PORT", "")
		svc, _, _ := cfgsvcTestService(t)
		if got, err := svc.GetString(ctx, "smtp.port"); err != nil || got != "587" {
			t.Fatalf("GetString = %q, %v; want 587", got, err)
		}
	})

	t.Run("seeded default row is transparent to env", func(t *testing.T) {
		// The migration seeds every key with its default. That row
		// must NOT shadow an env var an upgrading install still uses.
		t.Setenv("SMTP_PORT", "465")
		svc, f, _ := cfgsvcTestService(t)
		f.rows["smtp.port"] = "587" // == catalog default
		if got, err := svc.GetString(ctx, "smtp.port"); err != nil || got != "465" {
			t.Fatalf("GetString = %q, %v; want 465 (seeded default must not shadow env)", got, err)
		}
	})

	t.Run("empty row falls through", func(t *testing.T) {
		t.Setenv("RATE_LIMIT_API", "300")
		svc, f, _ := cfgsvcTestService(t)
		f.rows["ratelimit.api"] = ""
		if got, err := svc.GetInt(ctx, "ratelimit.api"); err != nil || got != 300 {
			t.Fatalf("GetInt = %d, %v; want 300", got, err)
		}
	})

	t.Run("typed getters read the effective value", func(t *testing.T) {
		svc, f, _ := cfgsvcTestService(t)
		f.rows["webhook.retry_interval"] = "2m"
		f.rows["webhook.allow_private"] = "TRUE" // canonicalized on write; tolerated on read
		f.rows["ratelimit.admin"] = "240"
		if d, err := svc.GetDuration(ctx, "webhook.retry_interval"); err != nil || d != 2*time.Minute {
			t.Fatalf("GetDuration = %v, %v; want 2m", d, err)
		}
		if b, err := svc.GetBool(ctx, "webhook.allow_private"); err != nil || !b {
			t.Fatalf("GetBool = %v, %v; want true", b, err)
		}
		if n, err := svc.GetInt(ctx, "ratelimit.admin"); err != nil || n != 240 {
			t.Fatalf("GetInt = %d, %v; want 240", n, err)
		}
	})

	t.Run("unparseable stored value is an error, never a silent default", func(t *testing.T) {
		svc, f, _ := cfgsvcTestService(t)
		f.rows["ratelimit.api"] = "many"
		if _, err := svc.GetInt(ctx, "ratelimit.api"); err == nil {
			t.Fatal("GetInt must refuse a non-numeric stored value")
		}
	})

	t.Run("unknown keys and secrets are refused on the plain getters", func(t *testing.T) {
		svc, _, _ := cfgsvcTestService(t)
		if _, err := svc.GetString(ctx, "nope.not_here"); !errors.Is(err, ErrConfigKeyUnknown) {
			t.Fatalf("unknown key err = %v; want ErrConfigKeyUnknown", err)
		}
		if _, err := svc.GetString(ctx, "payment.pay2s.secret_key"); err == nil {
			t.Fatal("GetString must refuse secret keys")
		}
		if _, err := svc.GetSecret(ctx, "smtp.port"); err == nil {
			t.Fatal("GetSecret must refuse non-secret keys")
		}
	})
}

func TestConfigServiceSecretPrecedence(t *testing.T) {
	ctx := context.Background()

	t.Run("stored secret wins over env", func(t *testing.T) {
		t.Setenv("SMTP_PASSWORD", "envpass")
		svc, f, _ := cfgsvcTestService(t)
		f.secrets["smtp.password"] = "storedpass"
		if got, err := svc.GetSecret(ctx, "smtp.password"); err != nil || got != "storedpass" {
			t.Fatalf("GetSecret = %q, %v; want storedpass", got, err)
		}
	})

	t.Run("empty secret falls back to env", func(t *testing.T) {
		t.Setenv("PAYOS_API_KEY", "envkey")
		svc, f, _ := cfgsvcTestService(t)
		f.secrets["payment.payos.api_key"] = ""
		if got, err := svc.GetSecret(ctx, "payment.payos.api_key"); err != nil || got != "envkey" {
			t.Fatalf("GetSecret = %q, %v; want envkey", got, err)
		}
	})

	t.Run("unset secret is empty", func(t *testing.T) {
		t.Setenv("PAYOS_API_KEY", "")
		svc, _, _ := cfgsvcTestService(t)
		if got, err := svc.GetSecret(ctx, "payment.payos.api_key"); err != nil || got != "" {
			t.Fatalf("GetSecret = %q, %v; want empty", got, err)
		}
	})
}

// ─── Set / Unset ───

func TestConfigServiceSetValidation(t *testing.T) {
	ctx := context.Background()
	svc, f, _ := cfgsvcTestService(t)

	err := svc.Set(ctx,
		map[string]string{
			"nope.unknown":              "1",
			"ratelimit.api":             "many",
			"webhook.allow_private":     "yes",
			"webhook.http_timeout":      "5 parsecs",
			"retention.audit_logs_days": "-3",
			"payment.pay2s.secret_key":  "oops-in-values",
			"smtp.port":                 "2525",
		},
		map[string]string{
			"smtp.host":     "not-a-secret",
			"unknown.key":   "x",
			"smtp.password": "",
		})
	var verr *ConfigValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("Set err = %v; want ConfigValidationError", err)
	}
	for _, key := range []string{
		"nope.unknown", "ratelimit.api", "webhook.allow_private",
		"webhook.http_timeout", "retention.audit_logs_days",
		"payment.pay2s.secret_key", "smtp.host", "unknown.key",
	} {
		if verr.Problems[key] == "" {
			t.Errorf("missing problem for %q (got %v)", key, verr.Problems)
		}
	}
	if _, bad := verr.Problems["smtp.port"]; bad {
		t.Errorf("smtp.port=2525 is valid, got problem %q", verr.Problems["smtp.port"])
	}
	// A refused batch writes NOTHING — no half-applied config.
	if len(f.lastSet) != 0 {
		t.Errorf("validation failure must not write anything, wrote %v", f.lastSet)
	}
}

func TestConfigServiceSetCanonicalizes(t *testing.T) {
	ctx := context.Background()
	svc, f, _ := cfgsvcTestService(t)

	if err := svc.Set(ctx, map[string]string{
		"webhook.allow_private":  "TRUE",
		"webhook.retry_interval": "1h",
		"ratelimit.api":          "0060",
		"smtp.from":              "  Acme <a@b.co>  ",
	}, map[string]string{
		"payment.payos.checksum_key": "  raw-secret  ", // secrets stored verbatim
	}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	want := map[string]string{
		"webhook.allow_private":  "true",
		"webhook.retry_interval": "1h0m0s",
		"ratelimit.api":          "60",
		"smtp.from":              "Acme <a@b.co>",
	}
	for k, v := range want {
		if f.lastSet[k] != v {
			t.Errorf("stored %s = %q, want %q", k, f.lastSet[k], v)
		}
	}
	if f.lastSet["payment.payos.checksum_key"] != "  raw-secret  " {
		t.Errorf("secrets must be stored verbatim, got %q", f.lastSet["payment.payos.checksum_key"])
	}
	if f.rows["payment.payos.checksum_key"] != "" {
		t.Error("secret must not land in the plain rows")
	}
	if f.secrets["payment.payos.checksum_key"] != "  raw-secret  " {
		t.Error("secret must land in the sealed path")
	}
}

func TestConfigServiceSetSkipsBlankSecrets(t *testing.T) {
	ctx := context.Background()
	svc, f, _ := cfgsvcTestService(t)
	f.secrets["smtp.password"] = "keepme"

	// A form that cannot display a secret sends it blank on save; that
	// must mean "unchanged", never "cleared".
	if err := svc.Set(ctx, nil, map[string]string{"smtp.password": ""}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if len(f.lastSet) != 0 {
		t.Errorf("blank secret must not be written, wrote %v", f.lastSet)
	}
	if f.secrets["smtp.password"] != "keepme" {
		t.Fatalf("stored secret = %q, want keepme", f.secrets["smtp.password"])
	}
}

func TestConfigServiceCacheInvalidation(t *testing.T) {
	ctx := context.Background()
	svc, f, setNow := cfgsvcTestService(t)
	base := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	(*setNow)(base)

	f.rows["site_name"] = "First"
	if v, _ := svc.GetString(ctx, "site_name"); v != "First" {
		t.Fatalf("got %q", v)
	}
	if v, _ := svc.GetString(ctx, "site_name"); v != "First" {
		t.Fatalf("got %q", v)
	}
	if f.gets != 1 {
		t.Fatalf("second read within TTL hit the store %d times, want 1", f.gets)
	}

	// Set drops the cache: read-your-own-write must hold.
	if err := svc.Set(ctx, map[string]string{"site_name": "Second"}, nil); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, _ := svc.GetString(ctx, "site_name"); v != "Second" {
		t.Fatalf("after Set got %q, want Second", v)
	}

	// The TTL alone expires the snapshot too.
	(*setNow)(base.Add(cfgsvcCacheTTL + time.Second))
	if v, _ := svc.GetString(ctx, "site_name"); v != "Second" {
		t.Fatalf("after TTL got %q", v)
	}
	if f.gets < 3 {
		t.Fatalf("store reads = %d, want one per generation", f.gets)
	}
}

func TestConfigServiceUnset(t *testing.T) {
	ctx := context.Background()
	svc, f, _ := cfgsvcTestService(t)
	f.rows["site_name"] = "Something"

	if err := svc.Unset(ctx, "nope.unknown"); !errors.Is(err, ErrConfigKeyUnknown) {
		t.Fatalf("Unset unknown = %v; want ErrConfigKeyUnknown", err)
	}
	if err := svc.Unset(ctx, "site_name"); err != nil {
		t.Fatalf("Unset: %v", err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "site_name" {
		t.Fatalf("deleted = %v", f.deleted)
	}
	// Resolved back to env/default after the reset.
	if v, _ := svc.GetString(ctx, "site_name"); v != "" {
		t.Fatalf("after Unset got %q, want the empty default", v)
	}
}

// ─── PublicSnapshot ───

func TestConfigServicePublicSnapshot(t *testing.T) {
	ctx := context.Background()
	t.Setenv("DOMAIN_BASE", "https://from-env.example")
	svc, f, _ := cfgsvcTestService(t)
	f.rows["site_name"] = "HiTechCloud"
	f.rows["payment.pay2s.secret_key"] = "LEAK-CHECK"

	snap, err := svc.PublicSnapshot(ctx)
	if err != nil {
		t.Fatalf("PublicSnapshot: %v", err)
	}
	if snap["site_name"] != "HiTechCloud" {
		t.Errorf("site_name = %q", snap["site_name"])
	}
	if snap["domain.base"] != "https://from-env.example" {
		t.Errorf("domain.base = %q, want env fallback", snap["domain.base"])
	}
	// NOT the secret assertion anyone can pass by accident: no key
	// NAME of a secret may appear either.
	for _, secretKey := range config.SortedSecretKeys() {
		if _, ok := snap[secretKey]; ok {
			t.Errorf("PublicSnapshot leaks secret key %q", secretKey)
		}
	}
	for k, v := range snap {
		if v == "LEAK-CHECK" {
			t.Errorf("PublicSnapshot leaks a secret value under %q", k)
		}
		e, _ := config.FindEntry(k)
		if e.Category != config.CategoryDomains && e.Category != config.CategoryBranding {
			t.Errorf("PublicSnapshot carries %q from category %q", k, e.Category)
		}
	}
}

// ─── Payment credential bundles ───

func TestConfigServicePaymentCreds(t *testing.T) {
	ctx := context.Background()
	t.Setenv("PAYOS_CHECKSUM_KEY", "")
	svc, f, _ := cfgsvcTestService(t)
	f.rows["payment.pay2s.partner_code"] = "PC1"
	f.rows["payment.pay2s.partner_name"] = "Acme"
	f.rows["payment.pay2s.access_key"] = "AK1"
	f.rows["payment.pay2s.bank_accounts"] = "970422|92568686|MB Bank|MB"
	f.rows["payment.pay2s.base_url"] = "https://sandbox-payment.pay2s.vn"
	f.secrets["payment.pay2s.secret_key"] = "SK1"
	f.rows["payment.zalopay.app_id"] = "2553"
	f.secrets["payment.zalopay.key1"] = "k1"
	f.secrets["payment.zalopay.callback_key"] = "ck"
	f.rows["payment.payos.client_id"] = "cid"
	f.secrets["payment.payos.api_key"] = "akey"
	f.secrets["payment.payos.checksum_key"] = "ckey"

	creds, err := svc.PaymentCreds(ctx)
	if err != nil {
		t.Fatalf("PaymentCreds: %v", err)
	}
	if creds.Pay2S.PartnerCode != "PC1" || creds.Pay2S.PartnerName != "Acme" ||
		creds.Pay2S.AccessKey != "AK1" || creds.Pay2S.SecretKey != "SK1" ||
		creds.Pay2S.BaseURL != "https://sandbox-payment.pay2s.vn" ||
		!strings.Contains(creds.Pay2S.BankAccounts, "92568686") {
		t.Errorf("Pay2S creds wrong: %+v", creds.Pay2S)
	}
	if creds.ZaloPay.AppID != "2553" || creds.ZaloPay.Key1 != "k1" || creds.ZaloPay.CallbackKey != "ck" {
		t.Errorf("ZaloPay creds wrong: %+v", creds.ZaloPay)
	}
	if creds.PayOS.ClientID != "cid" || creds.PayOS.APIKey != "akey" || creds.PayOS.ChecksumKey != "ckey" {
		t.Errorf("PayOS creds wrong: %+v", creds.PayOS)
	}

	// The getter adapters are the seams main.go hands to the dynamic
	// providers — pin they delegate to the same resolution.
	got, err := svc.Pay2SGetter()(ctx)
	if err != nil || got.PartnerCode != "PC1" {
		t.Errorf("Pay2SGetter() = %+v, %v", got, err)
	}
}

func TestConfigServiceStripeLivemode(t *testing.T) {
	ctx := context.Background()
	t.Setenv("STRIPE_LIVEMODE", "")

	t.Run("derived from live key prefix when unset", func(t *testing.T) {
		svc, f, _ := cfgsvcTestService(t)
		f.secrets["payment.stripe_secret_key"] = "sk_live_abc"
		c, err := svc.StripeCreds(ctx)
		if err != nil || !c.Livemode {
			t.Fatalf("Livemode = %v, %v; want true (derived from sk_live_)", c.Livemode, err)
		}
		// The reader and the UI see the same truth.
		if v, _ := svc.GetBool(ctx, "payment.stripe_livemode"); !v {
			t.Fatal("GetBool must reflect the derived value")
		}
	})

	t.Run("test key derives false", func(t *testing.T) {
		svc, f, _ := cfgsvcTestService(t)
		f.secrets["payment.stripe_secret_key"] = "sk_test_abc"
		c, err := svc.StripeCreds(ctx)
		if err != nil || c.Livemode {
			t.Fatalf("Livemode = %v, %v; want false", c.Livemode, err)
		}
	})

	t.Run("explicit true wins over derivation", func(t *testing.T) {
		svc, f, _ := cfgsvcTestService(t)
		f.secrets["payment.stripe_secret_key"] = "sk_test_abc"
		f.rows["payment.stripe_livemode"] = "true"
		c, err := svc.StripeCreds(ctx)
		if err != nil || !c.Livemode {
			t.Fatalf("Livemode = %v, %v; want true (explicit)", c.Livemode, err)
		}
	})
}

// ─── Secret sealing round-trip (DB-gated) ───

// TestConfigServiceSecretSealingRoundTrip runs against a real settings
// table: a secret written through Set must land SEALED (never
// plaintext) and read back through GetSecret unchanged.
func TestConfigServiceSecretSealingRoundTrip(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	st, err := store.New(dsn)
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	// The settings layer seals with the secret box when configured and
	// falls back to the license-key AEAD otherwise. The AEAD is set
	// here so the test is deterministic without mutating the process
	// global secret box.
	if st.LicenseKeyAEAD == nil {
		st.LicenseKeyAEAD, err = crypto.NewAESGCM(make([]byte, 32))
		if err != nil {
			t.Fatalf("NewAESGCM: %v", err)
		}
	}
	svc := NewConfigService(st)
	ctx := context.Background()
	const key = "payment.payos.checksum_key"
	defer func() { _ = svc.Unset(ctx, key) }()

	if err := svc.Set(ctx, nil, map[string]string{key: "ck-round-trip"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	raw, err := st.GetSetting(ctx, key)
	if err != nil {
		t.Fatalf("GetSetting: %v", err)
	}
	if raw == "ck-round-trip" {
		t.Fatal("secret stored in PLAINTEXT")
	}
	if !strings.HasPrefix(raw, "enc:v1:") {
		t.Fatalf("stored secret not sealed: %q", raw)
	}
	got, err := svc.GetSecret(ctx, key)
	if err != nil || got != "ck-round-trip" {
		t.Fatalf("GetSecret = %q, %v; want ck-round-trip", got, err)
	}
}
