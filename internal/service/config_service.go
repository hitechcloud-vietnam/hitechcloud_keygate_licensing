package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/config"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ConfigService resolves platform configuration from the settings
// table with the environment as a fallback and the catalog default as
// the floor (config-in-DB, per internal/config/keys.go):
//
//	stored settings row  >  env var  >  catalog default
//
// A stored row that still equals the catalog default counts as unset,
// so the rows seeded by the config-catalog migration never shadow an
// env var an upgrading install still relies on. Empty values fall back
// the same way (an empty secret is "not configured", not "configured
// as blank").
//
// Non-secret values are cached for 30 seconds and the cache is dropped
// on every write, so the admin API's read-your-own-write holds while
// hot paths (payment credential reads, domain resolution) stay cheap.
// Secrets are never cached: each read decrypts through the settings
// layer, so a rotated encryption key or a cleared row cannot be masked
// by a stale copy.
type ConfigService struct {
	st ConfigServiceStore

	// now is the clock seam for the cache TTL in tests.
	now func() time.Time

	mu         sync.Mutex
	cache      map[string]string
	cacheUntil time.Time
}

// ConfigServiceStore is the slice of *store.Store the config service
// needs. Declared as an interface so tests can drive precedence and
// validation without a database.
type ConfigServiceStore interface {
	GetSettings(ctx context.Context) (map[string]string, error)
	GetSecretSetting(ctx context.Context, key string) (string, error)
	SetSettings(ctx context.Context, settings map[string]string) error
	DeleteSetting(ctx context.Context, key string) error
}

var _ ConfigServiceStore = (*store.Store)(nil)

// cfgsvcCacheTTL is how long a resolved settings snapshot may be
// reused before the table is read again. Short enough that a manual
// row edit is picked up quickly even when it bypassed Set, long enough
// to keep per-request credential reads off the database.
const cfgsvcCacheTTL = 30 * time.Second

// ErrConfigKeyUnknown is returned for keys that are not in the
// catalog. The admin API maps it to 404; Set reports it as a
// validation problem instead.
var ErrConfigKeyUnknown = errors.New("unknown configuration key")

// cfgsvcErrSecretValue guards the secret values: they are only
// reachable through GetSecret, never through the plain getters.
var cfgsvcErrSecretValue = errors.New("config: key holds a secret; read it with GetSecret")

// cfgsvcErrNotSecret is the mirror misuse: GetSecret on a plain key.
var cfgsvcErrNotSecret = errors.New("config: key is not a secret")

// ConfigValidationError collects per-key problems from Set. The admin
// API turns Problems into the details of a single 400, so one bad
// value in a form submit names every field that needs fixing at once.
type ConfigValidationError struct {
	Problems map[string]string
}

func (e *ConfigValidationError) Error() string {
	keys := make([]string, 0, len(e.Problems))
	for k := range e.Problems {
		keys = append(keys, k)
	}
	// Deterministic message; the map itself travels as details.
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+e.Problems[k])
	}
	return "invalid configuration: " + strings.Join(parts, "; ")
}

// NewConfigService builds the config service over the settings store.
func NewConfigService(st ConfigServiceStore) *ConfigService {
	return &ConfigService{st: st, now: time.Now}
}

// ─── Reads ───

// rows returns the raw settings rows, through the 30s cache.
func (s *ConfigService) rows(ctx context.Context) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache != nil && s.now().Before(s.cacheUntil) {
		return s.cache, nil
	}
	m, err := s.st.GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	s.cache = m
	s.cacheUntil = s.now().Add(cfgsvcCacheTTL)
	return m, nil
}

// invalidate drops the cache after a write.
func (s *ConfigService) invalidate() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cache = nil
	s.cacheUntil = time.Time{}
}

// cfgsvcEffective applies the precedence rule to one catalog entry and
// its stored row. The bool reports whether an explicit value is in
// effect (stored row that is not merely the catalog default, or env
// var) — i.e. what the admin UI shows as "set".
func cfgsvcEffective(e config.Entry, row string) (string, bool) {
	if row != "" {
		if rn, ok := cfgsvcNormalize(e.Type, row); !ok || rn != cfgsvcNormalizeOrRaw(e.Type, e.Default) {
			// Explicit value (or one that no longer parses — treat it
			// as what the operator set, never silently replace it).
			return row, true
		}
	}
	if v := strings.TrimSpace(os.Getenv(e.EnvVar)); v != "" {
		return v, true
	}
	return e.Default, false
}

// GetString returns the effective value of a non-secret key.
func (s *ConfigService) GetString(ctx context.Context, key string) (string, error) {
	e, err := cfgsvcPlainEntry(key)
	if err != nil {
		return "", err
	}
	rows, err := s.rows(ctx)
	if err != nil {
		return "", err
	}
	v, set := cfgsvcEffective(e, rows[key])
	return s.applyStripeLivemodeDerivation(ctx, key, set, v)
}

// cfgsvcDerivedKey is the one catalog key whose value when UNSET is
// derived rather than its literal default: payment.stripe_livemode
// keeps the legacy rule that live mode follows the secret key prefix
// (sk_live_ → live) unless the flag says otherwise. An explicit value
// that merely equals the default still counts as unset here — with a
// sk_live_ key the install IS live, and the flag is a refusal to
// believe so, not a downgrade.
const cfgsvcDerivedKey = "payment.stripe_livemode"

// applyStripeLivemodeDerivation substitutes the derived value for the
// one key above when nothing explicit is in effect.
func (s *ConfigService) applyStripeLivemodeDerivation(ctx context.Context, key string, set bool, v string) (string, error) {
	if key != cfgsvcDerivedKey || set {
		return v, nil
	}
	sk, err := s.GetSecret(ctx, "payment.stripe_secret_key")
	if err != nil {
		return "", err
	}
	return strconv.FormatBool(config.DeriveStripeLivemode(sk)), nil
}

// GetInt returns the effective value of an int key.
func (s *ConfigService) GetInt(ctx context.Context, key string) (int, error) {
	v, err := s.GetString(ctx, key)
	if err != nil {
		return 0, err
	}
	n, perr := strconv.Atoi(strings.TrimSpace(v))
	if perr != nil {
		return 0, fmt.Errorf("config %s is not a whole number (%q)", key, v)
	}
	return n, nil
}

// GetBool returns the effective value of a bool key ("true"/"false",
// plus the strconv.ParseBool spellings).
func (s *ConfigService) GetBool(ctx context.Context, key string) (bool, error) {
	v, err := s.GetString(ctx, key)
	if err != nil {
		return false, err
	}
	b, perr := strconv.ParseBool(strings.TrimSpace(v))
	if perr != nil {
		return false, fmt.Errorf("config %s is not a boolean (%q)", key, v)
	}
	return b, nil
}

// GetDuration returns the effective value of a duration key
// (Go duration syntax, e.g. "30s", "1h30m").
func (s *ConfigService) GetDuration(ctx context.Context, key string) (time.Duration, error) {
	v, err := s.GetString(ctx, key)
	if err != nil {
		return 0, err
	}
	d, perr := time.ParseDuration(strings.TrimSpace(v))
	if perr != nil {
		return 0, fmt.Errorf("config %s is not a duration (%q)", key, v)
	}
	return d, nil
}

// GetSecret returns the decrypted value of a secret key. Precedence is
// the same as for plain values: the stored secret wins, an empty
// (unset) one falls back to the env var. Never cached.
func (s *ConfigService) GetSecret(ctx context.Context, key string) (string, error) {
	e, ok := config.FindEntry(key)
	if !ok {
		return "", ErrConfigKeyUnknown
	}
	if e.Type != config.TypeSecret {
		return "", cfgsvcErrNotSecret
	}
	v, err := s.st.GetSecretSetting(ctx, key)
	if err != nil {
		return "", err
	}
	if v == "" {
		v = strings.TrimSpace(os.Getenv(e.EnvVar))
	}
	return v, nil
}

// IsSet reports whether an explicit value is in effect for key (stored
// row beyond the catalog default, or env var). For secrets it reports
// whether one is configured at all — the admin UI's "set" badge, since
// the value itself must never travel.
func (s *ConfigService) IsSet(ctx context.Context, key string) (bool, error) {
	e, ok := config.FindEntry(key)
	if !ok {
		return false, ErrConfigKeyUnknown
	}
	if e.Type == config.TypeSecret {
		v, err := s.st.GetSecretSetting(ctx, key)
		if err != nil {
			return false, err
		}
		return v != "" || strings.TrimSpace(os.Getenv(e.EnvVar)) != "", nil
	}
	rows, err := s.rows(ctx)
	if err != nil {
		return false, err
	}
	_, set := cfgsvcEffective(e, rows[key])
	return set, nil
}

// Resolved is a point-in-time snapshot of every catalog key, for
// consumers that read many values at once (payment credential bundles,
// the admin config screen, the public config blob).
type Resolved struct {
	values     map[string]string // non-secret keys → effective value
	set        map[string]bool   // every key → explicit value in effect?
	secretsSet map[string]bool   // secret keys → configured?
}

// Values returns the non-secret effective values (a copy). Secret
// values are never included — only their presence, via SecretSet.
func (r Resolved) Values() map[string]string {
	out := make(map[string]string, len(r.values))
	for k, v := range r.values {
		out[k] = v
	}
	return out
}

// String returns the effective value of a non-secret key ("" when
// absent).
func (r Resolved) String(key string) string { return r.values[key] }

// IsSet reports whether key has an explicit value in effect.
func (r Resolved) IsSet(key string) bool { return r.set[key] }

// SecretSet reports whether a secret key is configured. The value
// itself is deliberately not part of the snapshot.
func (r Resolved) SecretSet(key string) bool { return r.secretsSet[key] }

// Int returns the key's value as an int, or 0 when it does not parse.
// Typed errors live on ConfigService.GetInt; this is the convenience
// accessor over the snapshot.
func (r Resolved) Int(key string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(r.values[key]))
	return n
}

// Bool returns the key's value as a bool, or false when it does not
// parse.
func (r Resolved) Bool(key string) bool {
	b, _ := strconv.ParseBool(strings.TrimSpace(r.values[key]))
	return b
}

// Duration returns the key's value as a duration, or 0 when it does
// not parse.
func (r Resolved) Duration(key string) time.Duration {
	d, _ := time.ParseDuration(strings.TrimSpace(r.values[key]))
	return d
}

// Resolve snapshots every catalog key at once. Non-secret keys carry
// their effective value; secret keys appear only as a presence flag.
func (s *ConfigService) Resolve(ctx context.Context) (Resolved, error) {
	rows, err := s.rows(ctx)
	if err != nil {
		return Resolved{}, err
	}
	out := Resolved{
		values:     make(map[string]string, len(cfgsvcAllKeys())),
		set:        map[string]bool{},
		secretsSet: map[string]bool{},
	}
	for _, e := range config.Catalog() {
		if e.Type == config.TypeSecret {
			v, err := s.st.GetSecretSetting(ctx, e.Key)
			if err != nil {
				return Resolved{}, err
			}
			set := v != "" || strings.TrimSpace(os.Getenv(e.EnvVar)) != ""
			out.secretsSet[e.Key] = set
			out.set[e.Key] = set
			continue
		}
		v, set := cfgsvcEffective(e, rows[e.Key])
		if v, err = s.applyStripeLivemodeDerivation(ctx, e.Key, set, v); err != nil {
			return Resolved{}, err
		}
		out.values[e.Key] = v
		out.set[e.Key] = set
	}
	return out, nil
}

// PublicSnapshot returns the non-secret configuration the web client
// needs without authentication: the domain map and the branding keys.
// Secret keys are excluded by construction — a snapshot of this must
// never be able to leak one.
func (s *ConfigService) PublicSnapshot(ctx context.Context) (map[string]string, error) {
	rows, err := s.rows(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, e := range config.Catalog() {
		if e.Type == config.TypeSecret {
			continue
		}
		if e.Category != config.CategoryDomains && e.Category != config.CategoryBranding {
			continue
		}
		v, _ := cfgsvcEffective(e, rows[e.Key])
		out[e.Key] = v
	}
	return out, nil
}

// ─── Writes ───

// Set validates and stores the given values in one transaction.
// values carries non-secret keys, secrets carries secret keys (they
// are sealed at rest by the settings layer). Unknown keys, type
// mismatches and wrong-map entries are refused as a batch with one
// problem per key; nothing is written when any key is refused.
//
// An empty secret value means "leave the stored one alone" (the admin
// form can never show it, so a blank field must not wipe it) — use
// Unset to remove one. An empty non-secret value is stored as-is and
// therefore resolves back to the env var / default.
func (s *ConfigService) Set(ctx context.Context, values, secrets map[string]string) error {
	problems := map[string]string{}
	writes := map[string]string{}

	for key, value := range values {
		e, ok := config.FindEntry(key)
		if !ok {
			problems[key] = "unknown configuration key"
			continue
		}
		if e.Type == config.TypeSecret {
			problems[key] = "secret keys must be sent in the secrets map"
			continue
		}
		nv, err := cfgsvcNormalizeStrict(e.Type, value)
		if err != nil {
			problems[key] = err.Error()
			continue
		}
		if e.Category == config.CategoryRetention {
			if n, _ := strconv.Atoi(nv); n < 0 {
				problems[key] = "must be 0 or more days (0 = keep forever)"
				continue
			}
		}
		writes[key] = nv
	}

	for key, value := range secrets {
		e, ok := config.FindEntry(key)
		if !ok {
			problems[key] = "unknown configuration key"
			continue
		}
		if e.Type != config.TypeSecret {
			problems[key] = "key is not a secret; send it in the values map"
			continue
		}
		if value == "" {
			continue // blank secret = unchanged
		}
		// Stored verbatim: the settings layer seals it (see
		// store.IsSecretSettingKey) and refuses to write it at all
		// when no encryption key is configured.
		writes[key] = value
	}

	if len(problems) > 0 {
		return &ConfigValidationError{Problems: problems}
	}
	if len(writes) == 0 {
		return nil
	}
	if err := s.st.SetSettings(ctx, writes); err != nil {
		return err
	}
	s.invalidate()
	return nil
}

// Unset removes a key's stored row so it resolves back to the env var
// and then the catalog default. Unknown keys are refused.
func (s *ConfigService) Unset(ctx context.Context, key string) error {
	if _, ok := config.FindEntry(key); !ok {
		return ErrConfigKeyUnknown
	}
	if err := s.st.DeleteSetting(ctx, key); err != nil {
		return err
	}
	s.invalidate()
	return nil
}

// Reset is the API-facing name for Unset: drop the stored row so the
// key resolves back to its env var and then the catalog default.
func (s *ConfigService) Reset(ctx context.Context, key string) error {
	return s.Unset(ctx, key)
}

// LogEnvDeprecations logs one line per non-bootstrap env var that is
// still set. Env is bootstrap-only (PORT, DATABASE_URL, JWT_SECRET,
// LICENSE_SIGNING_KEY, SECRET_ENCRYPTION_KEY,
// RELEASE_KEY_ENCRYPTION_KEY, REFERRAL_HASH_SALT, REDIS_URL);
// everything else belongs in the settings table.
func (s *ConfigService) LogEnvDeprecations(logger *slog.Logger) {
	for _, name := range config.DeprecatedEnvVars() {
		logger.Warn("env var is deprecated config; move it into the database (admin → Settings → Config)",
			"env", name)
	}
}

// ─── Typed credential bundles ───

// PaymentCreds assembles every payment provider's configuration from
// the resolved values, ready for the dynamic providers.
func (s *ConfigService) PaymentCreds(ctx context.Context) (config.PaymentCreds, error) {
	var out config.PaymentCreds
	var err error
	if out.Stripe, err = s.StripeCreds(ctx); err != nil {
		return config.PaymentCreds{}, err
	}
	if out.Pay2S, err = s.Pay2SCreds(ctx); err != nil {
		return config.PaymentCreds{}, err
	}
	if out.ZaloPay, err = s.ZaloPayCreds(ctx); err != nil {
		return config.PaymentCreds{}, err
	}
	if out.PayOS, err = s.PayOSCreds(ctx); err != nil {
		return config.PaymentCreds{}, err
	}
	return out, nil
}

// StripeCreds returns the typed Stripe configuration. Livemode is the
// resolved flag: explicit values win, and an unset flag derives from
// the secret key prefix (sk_live_ → live) exactly as before
// config-in-DB.
func (s *ConfigService) StripeCreds(ctx context.Context) (config.StripeConfig, error) {
	sk, err := s.GetSecret(ctx, "payment.stripe_secret_key")
	if err != nil {
		return config.StripeConfig{}, err
	}
	wh, err := s.GetSecret(ctx, "payment.stripe_webhook_secret")
	if err != nil {
		return config.StripeConfig{}, err
	}
	live, err := s.GetBool(ctx, "payment.stripe_livemode")
	if err != nil {
		return config.StripeConfig{}, err
	}
	return config.StripeConfig{SecretKey: sk, WebhookSecret: wh, Livemode: live}, nil
}

// Pay2SCreds returns the typed Pay2S configuration.
func (s *ConfigService) Pay2SCreds(ctx context.Context) (config.Pay2SConfig, error) {
	cfg := config.Pay2SConfig{}
	for _, f := range []struct {
		key string
		dst *string
	}{
		{"payment.pay2s.partner_code", &cfg.PartnerCode},
		{"payment.pay2s.partner_name", &cfg.PartnerName},
		{"payment.pay2s.access_key", &cfg.AccessKey},
		{"payment.pay2s.bank_accounts", &cfg.BankAccounts},
		{"payment.pay2s.base_url", &cfg.BaseURL},
	} {
		v, err := s.GetString(ctx, f.key)
		if err != nil {
			return config.Pay2SConfig{}, err
		}
		*f.dst = v
	}
	secret, err := s.GetSecret(ctx, "payment.pay2s.secret_key")
	if err != nil {
		return config.Pay2SConfig{}, err
	}
	cfg.SecretKey = secret
	return cfg, nil
}

// ZaloPayCreds returns the typed ZaloPay configuration.
func (s *ConfigService) ZaloPayCreds(ctx context.Context) (config.ZaloPayConfig, error) {
	cfg := config.ZaloPayConfig{}
	for _, f := range []struct {
		key string
		dst *string
	}{
		{"payment.zalopay.app_id", &cfg.AppID},
		{"payment.zalopay.base_url", &cfg.BaseURL},
	} {
		v, err := s.GetString(ctx, f.key)
		if err != nil {
			return config.ZaloPayConfig{}, err
		}
		*f.dst = v
	}
	key1, err := s.GetSecret(ctx, "payment.zalopay.key1")
	if err != nil {
		return config.ZaloPayConfig{}, err
	}
	cb, err := s.GetSecret(ctx, "payment.zalopay.callback_key")
	if err != nil {
		return config.ZaloPayConfig{}, err
	}
	cfg.Key1, cfg.CallbackKey = key1, cb
	return cfg, nil
}

// PayOSCreds returns the typed payOS configuration.
func (s *ConfigService) PayOSCreds(ctx context.Context) (config.PayOSConfig, error) {
	cfg := config.PayOSConfig{}
	for _, f := range []struct {
		key string
		dst *string
	}{
		{"payment.payos.client_id", &cfg.ClientID},
		{"payment.payos.base_url", &cfg.BaseURL},
	} {
		v, err := s.GetString(ctx, f.key)
		if err != nil {
			return config.PayOSConfig{}, err
		}
		*f.dst = v
	}
	apiKey, err := s.GetSecret(ctx, "payment.payos.api_key")
	if err != nil {
		return config.PayOSConfig{}, err
	}
	checksum, err := s.GetSecret(ctx, "payment.payos.checksum_key")
	if err != nil {
		return config.PayOSConfig{}, err
	}
	cfg.APIKey, cfg.ChecksumKey = apiKey, checksum
	return cfg, nil
}

// The *Getter adapters are what main.go hands to the dynamic payment
// providers (payment.NewPay2SDynamic etc.), so credentials are read at
// call time from the database instead of being frozen at boot.
func (s *ConfigService) Pay2SGetter() func(context.Context) (config.Pay2SConfig, error) {
	return s.Pay2SCreds
}

func (s *ConfigService) ZaloPayGetter() func(context.Context) (config.ZaloPayConfig, error) {
	return s.ZaloPayCreds
}

func (s *ConfigService) PayOSGetter() func(context.Context) (config.PayOSConfig, error) {
	return s.PayOSCreds
}

func (s *ConfigService) StripeGetter() func(context.Context) (config.StripeConfig, error) {
	return s.StripeCreds
}

// ─── Internal helpers ───

// cfgsvcAllKeys is a cheap size hint source for the snapshot maps.
func cfgsvcAllKeys() []string {
	cat := config.Catalog()
	out := make([]string, len(cat))
	for i, e := range cat {
		out[i] = e.Key
	}
	return out
}

// cfgsvcPlainEntry resolves key to its catalog entry and refuses
// secrets and unknown keys for the plain getters.
func cfgsvcPlainEntry(key string) (config.Entry, error) {
	e, ok := config.FindEntry(key)
	if !ok {
		return config.Entry{}, ErrConfigKeyUnknown
	}
	if e.Type == config.TypeSecret {
		return config.Entry{}, cfgsvcErrSecretValue
	}
	return e, nil
}

// cfgsvcNormalize canonicalizes value for the given type so equal
// values compare equal ("1h" == "1h0m0s", "TRUE" == "true"). The bool
// reports whether the value parses at all.
func cfgsvcNormalize(typ, value string) (string, bool) {
	v := strings.TrimSpace(value)
	switch typ {
	case config.TypeInt:
		n, err := strconv.Atoi(v)
		if err != nil {
			return value, false
		}
		return strconv.Itoa(n), true
	case config.TypeBool:
		b, err := strconv.ParseBool(v)
		if err != nil {
			return value, false
		}
		if b {
			return "true", true
		}
		return "false", true
	case config.TypeDuration:
		d, err := time.ParseDuration(v)
		if err != nil {
			return value, false
		}
		return d.String(), true
	default: // string, secret
		return value, true
	}
}

// cfgsvcNormalizeOrRaw is cfgsvcNormalize with the raw value returned
// when it does not parse (catalog defaults always parse; this keeps
// the comparison total).
func cfgsvcNormalizeOrRaw(typ, value string) string {
	n, ok := cfgsvcNormalize(typ, value)
	if !ok {
		return value
	}
	return n
}

// cfgsvcNormalizeStrict canonicalizes for storage and reports why a
// value does not fit its type. Strings are trimmed (a stray space in
// a URL is a typo); secrets are never touched here (Set stores them
// verbatim).
func cfgsvcNormalizeStrict(typ, value string) (string, error) {
	if typ == config.TypeSecret {
		return value, nil
	}
	v := strings.TrimSpace(value)
	n, ok := cfgsvcNormalize(typ, v)
	if !ok {
		switch typ {
		case config.TypeInt:
			return "", fmt.Errorf("must be a whole number, got %q", value)
		case config.TypeBool:
			return "", fmt.Errorf("must be true or false, got %q", value)
		case config.TypeDuration:
			return "", fmt.Errorf("must be a Go duration such as 30s or 2m, got %q", value)
		}
		return "", fmt.Errorf("invalid value %q", value)
	}
	if typ == config.TypeString {
		return v, nil
	}
	return n, nil
}

// ApplyBootOverlay copies the effective value of every catalog key
// that has a runtime home in *config.Config onto cfg, so the settings
// table wins over the environment for the restart-scoped keys. The
// live-scoped keys (payment gateway credentials, Stripe secret,
// domains, session cookie domain, SMTP, retention, branding) are
// re-read per use by their own seams and never need this.
//
// Precedence is the catalog's own: DB row > env var > catalog default.
// A key that is explicit in NEITHER the database nor the environment
// leaves cfg's already-loaded value alone — config.Load has applied
// the same defaults, so nothing is gained by stomping. The one
// derived exception is payment.stripe_livemode (sk_live_-prefix
// derivation when unset), applied unconditionally through GetString.
//
// Every accessor falls back to cfg's current (env-loaded) value on a
// read error, so a transient settings failure degrades to today's env
// configuration instead of wiping it.
//
// Call at boot, after the database is reachable and BEFORE the
// services that consume cfg are constructed.
func (s *ConfigService) ApplyBootOverlay(ctx context.Context, cfg *config.Config) {
	if cfg == nil {
		return
	}
	// getSet returns the effective value of key only when something
	// explicit is in effect (DB row beyond the default, or env var);
	// otherwise — or on a read error — the caller's fallback stands.
	getStr := func(key, fallback string) string {
		if set, err := s.IsSet(ctx, key); err != nil || !set {
			return fallback
		}
		v, err := s.GetString(ctx, key)
		if err != nil {
			return fallback
		}
		return v
	}
	getSecret := func(key, fallback string) string {
		if set, err := s.IsSet(ctx, key); err != nil || !set {
			return fallback
		}
		v, err := s.GetSecret(ctx, key)
		if err != nil {
			return fallback
		}
		return v
	}
	getInt := func(key string, fallback int) int {
		if set, err := s.IsSet(ctx, key); err != nil || !set {
			return fallback
		}
		n, err := s.GetInt(ctx, key)
		if err != nil {
			return fallback
		}
		return n
	}
	// getDur keeps the operator's raw spelling ("2h" stays "2h" —
	// these cfg fields are strings re-parsed at their use sites).
	getDur := func(key, fallback string) string {
		if set, err := s.IsSet(ctx, key); err != nil || !set {
			return fallback
		}
		v, err := s.GetString(ctx, key)
		if err != nil {
			return fallback
		}
		return v
	}

	// ─── app ───
	cfg.BaseURL = getStr("app.base_url", cfg.BaseURL)
	cfg.Environment = getStr("app.environment", cfg.Environment)
	cfg.LogLevel = getStr("observability.log_level", cfg.LogLevel)
	// Quota threshold: bps in the catalog vs the legacy float env var
	// QUOTA_WARNING_THRESHOLD that config.Load reads. Same set-only
	// rule keeps the legacy variable working untouched.
	if bps := getInt("app.quota_warning_threshold", 0); bps > 0 {
		cfg.QuotaWarningThreshold = float64(bps) / 10000
	}
	if v := getStr("app.admin_emails", ""); v != "" {
		var emails []string
		for e := range strings.SplitSeq(v, ",") {
			if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
				emails = append(emails, e)
			}
		}
		cfg.AdminEmails = emails
	}

	// ─── payment: Stripe ─── (secret keys; livemode is the one
	// unconditional read — GetString derives it from the sk_ prefix
	// when unset, which IS the documented effective value)
	cfg.StripeSecretKey = getSecret("payment.stripe_secret_key", cfg.StripeSecretKey)
	cfg.StripeWebhookSecret = getSecret("payment.stripe_webhook_secret", cfg.StripeWebhookSecret)
	if b, err := s.GetBool(ctx, "payment.stripe_livemode"); err == nil {
		cfg.StripeLivemode = b
	}

	// ─── payment: Vietnamese gateways (typed bundles, same
	// precedence internally) ───
	if c, err := s.Pay2SCreds(ctx); err == nil {
		cfg.Pay2S = c
	}
	if c, err := s.ZaloPayCreds(ctx); err == nil {
		cfg.ZaloPay = c
	}
	if c, err := s.PayOSCreds(ctx); err == nil {
		cfg.PayOS = c
	}

	// ─── smtp ───
	cfg.SMTPHost = getStr("smtp.host", cfg.SMTPHost)
	cfg.SMTPPort = getStr("smtp.port", cfg.SMTPPort)
	cfg.SMTPUsername = getStr("smtp.username", cfg.SMTPUsername)
	cfg.SMTPPassword = getSecret("smtp.password", cfg.SMTPPassword)
	cfg.SMTPFrom = getStr("smtp.from", cfg.SMTPFrom)

	// ─── ratelimit ───
	cfg.RateLimitAPI = getInt("ratelimit.api", cfg.RateLimitAPI)
	cfg.RateLimitAdmin = getInt("ratelimit.admin", cfg.RateLimitAdmin)
	cfg.RateLimitAuth = getInt("ratelimit.auth", cfg.RateLimitAuth)
	cfg.RateLimitOTPSend = getInt("ratelimit.otp_send", cfg.RateLimitOTPSend)
	cfg.BFMaxFails = getInt("ratelimit.bf_max_fails", cfg.BFMaxFails)
	cfg.BFLockoutSeconds = getInt("ratelimit.bf_lockout_seconds", cfg.BFLockoutSeconds)

	// ─── webhook ───
	cfg.WebhookMaxAttempts = getInt("webhook.max_attempts", cfg.WebhookMaxAttempts)
	cfg.WebhookRetryInterval = getDur("webhook.retry_interval", cfg.WebhookRetryInterval)
	cfg.WebhookHTTPTimeout = getDur("webhook.http_timeout", cfg.WebhookHTTPTimeout)
	if set, err := s.IsSet(ctx, "webhook.allow_private"); err == nil && set {
		if b, err := s.GetBool(ctx, "webhook.allow_private"); err == nil {
			cfg.WebhookAllowPrivate = b
		}
	}

	// ─── storage ───
	cfg.StorageEndpoint = getStr("storage.endpoint", cfg.StorageEndpoint)
	cfg.StorageRegion = getStr("storage.region", cfg.StorageRegion)
	cfg.StorageBucket = getStr("storage.bucket", cfg.StorageBucket)
	cfg.StorageAccessKey = getStr("storage.access_key", cfg.StorageAccessKey)
	cfg.StorageSecretKey = getSecret("storage.secret_key", cfg.StorageSecretKey)
	cfg.StoragePublicURL = getStr("storage.public_url", cfg.StoragePublicURL)
	if set, err := s.IsSet(ctx, "storage.force_path_style"); err == nil && set {
		if b, err := s.GetBool(ctx, "storage.force_path_style"); err == nil {
			cfg.StorageForcePathStyle = b
		}
	}
	cfg.StorageUploadTTL = getDur("storage.upload_ttl", cfg.StorageUploadTTL)
	cfg.StorageDownloadTTL = getDur("storage.download_ttl", cfg.StorageDownloadTTL)
	cfg.StorageFeedURLTTL = getDur("storage.feed_url_ttl", cfg.StorageFeedURLTTL)
	if mb := getInt("storage.max_release_sign_size_mb", 0); mb > 0 {
		cfg.MaxReleaseSignSize = int64(mb) * 1024 * 1024
	}
}
