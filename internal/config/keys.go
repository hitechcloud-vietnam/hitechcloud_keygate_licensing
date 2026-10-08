package config

import (
	"os"
	"sort"
	"strings"
)

// ─── Configuration catalog (config-in-DB) ───
//
// The platform keeps its configuration in the `settings` table and
// treats the environment as a bootstrap fallback only. This file is
// the single static catalog of everything that may be configured: the
// key name, its type, the legacy environment variable it replaces, its
// default, and whether the running server has to restart to pick up a
// change.
//
// Precedence (enforced by service.ConfigService, documented here so the
// catalog and the reader cannot drift):
//
//	stored settings row  >  env var  >  catalog default
//
// with one deliberate refinement: a stored row that still holds the
// catalog default counts as UNSET, so the seeded rows this catalog's
// migration writes never shadow an env var an upgrading install still
// relies on (PAY2S_*, SMTP_*, STRIPE_*, …). An operator who wants a
// value equal to the default to win over an env var clears the env var
// — env-supplied config is deprecated and logged at boot.
//
// Bootstrap stays in env forever and is NOT in this catalog: PORT,
// DATABASE_URL, JWT_SECRET, LICENSE_SIGNING_KEY,
// SECRET_ENCRYPTION_KEY, RELEASE_KEY_ENCRYPTION_KEY,
// REFERRAL_HASH_SALT, REDIS_URL (see BootstrapEnvVars).

// Entry is one configurable setting.
type Entry struct {
	// Key is the settings-table key, e.g. "payment.pay2s.secret_key".
	Key string
	// Category groups the key for the admin UI (see the Category*
	// constants and CategoryLabel).
	Category string
	// Type is one of TypeString, TypeInt, TypeBool, TypeDuration,
	// TypeSecret. TypeSecret values are sealed at rest by the settings
	// layer and never returned by any API.
	Type string
	// EnvVar is the legacy environment variable consulted when no
	// stored value is in effect. Always set: an entry with no env
	// fallback would have nowhere to read from before its row exists.
	EnvVar string
	// Default is the value in effect when neither a stored row nor the
	// env var says otherwise. Secrets always default to empty.
	Default string
	// Description is operator-facing copy for the admin UI.
	Description string
	// RestartRequired marks keys the running server only reads at
	// boot. Changing one over the API is accepted and audited but
	// takes effect on the next restart; the admin UI shows the badge.
	RestartRequired bool
}

// Setting types.
const (
	TypeString   = "string"
	TypeInt      = "int"
	TypeBool     = "bool"
	TypeDuration = "duration"
	TypeSecret   = "secret"
)

// Categories, in the order the admin config UI lists them.
const (
	CategoryApp           = "app"
	CategoryPayment       = "payment"
	CategorySMTP          = "smtp"
	CategoryDomains       = "domains"
	CategoryRateLimit     = "ratelimit"
	CategoryWebhook       = "webhook"
	CategoryRetention     = "retention"
	CategoryStorage       = "storage"
	CategoryObservability = "observability"
	CategoryBranding      = "branding"
)

// cfgsvcCategoryLabels are the human names for the admin UI. The web
// client may translate by id; these are the English fallbacks.
var cfgsvcCategoryLabels = map[string]string{
	CategoryApp:           "Application",
	CategoryPayment:       "Payment Gateways",
	CategorySMTP:          "Email (SMTP)",
	CategoryDomains:       "Domains",
	CategoryRateLimit:     "Rate Limiting",
	CategoryWebhook:       "Webhooks",
	CategoryRetention:     "Data Retention",
	CategoryStorage:       "Storage & Releases",
	CategoryObservability: "Observability",
	CategoryBranding:      "Branding",
}

// cfgsvcCategoryOrder is the display order of the categories.
var cfgsvcCategoryOrder = []string{
	CategoryApp, CategoryPayment, CategorySMTP, CategoryDomains,
	CategoryRateLimit, CategoryWebhook, CategoryRetention,
	CategoryStorage, CategoryObservability, CategoryBranding,
}

// CategoryLabel returns the human name of a category id.
func CategoryLabel(id string) string {
	if l, ok := cfgsvcCategoryLabels[id]; ok {
		return l
	}
	return id
}

// CategoryOrder returns the category ids in display order.
func CategoryOrder() []string {
	return append([]string(nil), cfgsvcCategoryOrder...)
}

// cfgsvcCatalog is the pinned configuration catalog. Keys are named in
// the dotted form the admin API and the settings table use; the env
// fallbacks are the pre-config-in-DB variables from .env.example.
var cfgsvcCatalog = []Entry{
	// ─── app ───
	{Key: "app.base_url", Category: CategoryApp, Type: TypeString, EnvVar: "BASE_URL",
		Default:     "http://localhost:9000",
		Description: "Public base URL of this install (links in emails, payment return URLs, absolute redirects).",
		// Boot validates it for TLS/cookies and stamps absolute URLs
		// from it, so a change is a restart.
		RestartRequired: true},
	{Key: "app.environment", Category: CategoryApp, Type: TypeString, EnvVar: "ENVIRONMENT",
		Default:         "development",
		Description:     "Deployment environment: development, staging, or production (gates dev-login and security warnings).",
		RestartRequired: true},
	{Key: "app.quota_warning_threshold", Category: CategoryApp, Type: TypeInt, EnvVar: "QUOTA_WARNING_THRESHOLD_BPS",
		Default: "8000",
		Description: "Quota usage (in basis points, 10000 = 100%) that triggers the quota-warning email. " +
			"8000 = 80%. Replaces the legacy QUOTA_WARNING_THRESHOLD float fraction.",
		RestartRequired: true},
	{Key: "app.admin_emails", Category: CategoryApp, Type: TypeString, EnvVar: "ADMIN_EMAILS",
		Default: "",
		Description: "Comma-separated emails granted the admin role at boot (bootstrap only; normal admin status " +
			"comes from the user's role in the database).",
		RestartRequired: true},

	// ─── payment: Stripe (subscriptions) ───
	{Key: "payment.stripe_secret_key", Category: CategoryPayment, Type: TypeSecret, EnvVar: "STRIPE_SECRET_KEY",
		Default: "", Description: "Stripe API secret key (sk_… / rk_…). Leave empty to disable Stripe."},
	{Key: "payment.stripe_webhook_secret", Category: CategoryPayment, Type: TypeSecret, EnvVar: "STRIPE_WEBHOOK_SECRET",
		Default:     "",
		Description: "Stripe webhook signing secret (whsec_…). Auto-configured at boot when left empty.",
		// The StripeHandler captures it at construction; a rotated
		// secret reaches the handler on restart.
		RestartRequired: true},
	{Key: "payment.stripe_livemode", Category: CategoryPayment, Type: TypeBool, EnvVar: "STRIPE_LIVEMODE",
		Default: "false",
		Description: "Trust live-mode Stripe events. When unset, derived from the secret key prefix " +
			"(sk_live_ → live, sk_test_ → test).",
		RestartRequired: true},

	// ─── payment: Pay2S (bank transfer / Napas 247 QR) ───
	{Key: "payment.pay2s.partner_code", Category: CategoryPayment, Type: TypeString, EnvVar: "PAY2S_PARTNER_CODE",
		Default: "", Description: "Pay2S partner code. All Pay2S credentials must be set or the gateway stays disabled."},
	{Key: "payment.pay2s.partner_name", Category: CategoryPayment, Type: TypeString, EnvVar: "PAY2S_PARTNER_NAME",
		Default: "HiTechCloud", Description: "Pay2S partner display name sent on create."},
	{Key: "payment.pay2s.access_key", Category: CategoryPayment, Type: TypeString, EnvVar: "PAY2S_ACCESS_KEY",
		Default: "", Description: "Pay2S access key (request auth + signature input)."},
	{Key: "payment.pay2s.secret_key", Category: CategoryPayment, Type: TypeSecret, EnvVar: "PAY2S_SECRET_KEY",
		Default: "", Description: "Pay2S HMAC-SHA256 secret key (create/IPN/cancel signatures)."},
	{Key: "payment.pay2s.bank_accounts", Category: CategoryPayment, Type: TypeString, EnvVar: "PAY2S_BANK_ACCOUNTS",
		Default: "",
		Description: "Buyer transfer accounts, repeated \"bankId|accountNumber|accountName|bankName\" entries " +
			"separated by commas. At least one entry is required."},
	{Key: "payment.pay2s.base_url", Category: CategoryPayment, Type: TypeString, EnvVar: "PAY2S_BASE_URL",
		Default:     "https://payment.pay2s.vn",
		Description: "Pay2S API base URL (sandbox: https://sandbox-payment.pay2s.vn)."},

	// ─── payment: ZaloPay ───
	{Key: "payment.zalopay.app_id", Category: CategoryPayment, Type: TypeString, EnvVar: "ZALOPAY_APP_ID",
		Default: "", Description: "ZaloPay app id (positive integer). All ZaloPay credentials must be set or the gateway stays disabled."},
	{Key: "payment.zalopay.key1", Category: CategoryPayment, Type: TypeSecret, EnvVar: "ZALOPAY_KEY1",
		Default: "", Description: "ZaloPay HMAC key for outbound request macs."},
	{Key: "payment.zalopay.callback_key", Category: CategoryPayment, Type: TypeSecret, EnvVar: "ZALOPAY_CALLBACK_KEY",
		Default: "", Description: "ZaloPay HMAC key for verifying inbound payment callbacks."},
	{Key: "payment.zalopay.base_url", Category: CategoryPayment, Type: TypeString, EnvVar: "ZALOPAY_BASE_URL",
		Default:     "https://openapi.zalopay.vn",
		Description: "ZaloPay OpenAPI base URL (sandbox: https://sb-openapi.zalopay.vn)."},

	// ─── payment: payOS (VietQR) ───
	{Key: "payment.payos.client_id", Category: CategoryPayment, Type: TypeString, EnvVar: "PAYOS_CLIENT_ID",
		Default: "", Description: "payOS client id (x-client-id). All payOS credentials must be set or the gateway stays disabled."},
	{Key: "payment.payos.api_key", Category: CategoryPayment, Type: TypeSecret, EnvVar: "PAYOS_API_KEY",
		Default: "", Description: "payOS API key (x-api-key)."},
	{Key: "payment.payos.checksum_key", Category: CategoryPayment, Type: TypeSecret, EnvVar: "PAYOS_CHECKSUM_KEY",
		Default: "", Description: "payOS HMAC-SHA256 checksum key (create signatures + webhook verification)."},
	{Key: "payment.payos.base_url", Category: CategoryPayment, Type: TypeString, EnvVar: "PAYOS_BASE_URL",
		Default:     "https://api-merchant.payos.vn",
		Description: "payOS merchant API base URL."},

	// ─── smtp ───
	{Key: "smtp.host", Category: CategorySMTP, Type: TypeString, EnvVar: "SMTP_HOST",
		Default: "", Description: "SMTP server host. All SMTP fields together configure outbound mail."},
	{Key: "smtp.port", Category: CategorySMTP, Type: TypeInt, EnvVar: "SMTP_PORT",
		Default: "587", Description: "SMTP server port (465 = implicit TLS, otherwise STARTTLS)."},
	{Key: "smtp.username", Category: CategorySMTP, Type: TypeString, EnvVar: "SMTP_USERNAME",
		Default: "", Description: "SMTP username."},
	{Key: "smtp.password", Category: CategorySMTP, Type: TypeSecret, EnvVar: "SMTP_PASSWORD",
		Default: "", Description: "SMTP password (encrypted at rest, never returned by any API)."},
	{Key: "smtp.from", Category: CategorySMTP, Type: TypeString, EnvVar: "SMTP_FROM",
		Default: "", Description: "From address for outgoing mail, e.g. \"Acme Licensing <noreply@example.com>\"."},

	// ─── domains (consumed by the domain surface; exact names pinned) ───
	{Key: "domain.base", Category: CategoryDomains, Type: TypeString, EnvVar: "DOMAIN_BASE",
		Default: "", Description: "Base domain of the install (derived public URLs hang off this)."},
	{Key: "domain.apex", Category: CategoryDomains, Type: TypeString, EnvVar: "DOMAIN_APEX",
		Default: "", Description: "Apex / marketing site domain."},
	{Key: "domain.payments", Category: CategoryDomains, Type: TypeString, EnvVar: "DOMAIN_PAYMENTS",
		Default: "", Description: "Payments / checkout surface domain."},
	{Key: "domain.dashboard", Category: CategoryDomains, Type: TypeString, EnvVar: "DOMAIN_DASHBOARD",
		Default: "", Description: "Admin dashboard surface domain."},
	{Key: "domain.merchant", Category: CategoryDomains, Type: TypeString, EnvVar: "DOMAIN_MERCHANT",
		Default: "", Description: "Merchant surface domain."},
	{Key: "domain.customer", Category: CategoryDomains, Type: TypeString, EnvVar: "DOMAIN_CUSTOMER",
		Default: "", Description: "Customer portal surface domain."},
	{Key: "domain.verify", Category: CategoryDomains, Type: TypeString, EnvVar: "DOMAIN_VERIFY",
		Default: "", Description: "Domain-verification surface domain."},
	{Key: "domain.hooks", Category: CategoryDomains, Type: TypeString, EnvVar: "DOMAIN_HOOKS",
		Default: "", Description: "Webhook / IPN receiver domain."},
	{Key: "domain.docs", Category: CategoryDomains, Type: TypeString, EnvVar: "DOMAIN_DOCS",
		Default: "", Description: "Developer documentation surface domain."},
	{Key: "domain.status", Category: CategoryDomains, Type: TypeString, EnvVar: "DOMAIN_STATUS",
		Default: "", Description: "Status page surface domain."},
	{Key: "domain.go", Category: CategoryDomains, Type: TypeString, EnvVar: "DOMAIN_GO",
		Default: "", Description: "Short-link / go surface domain."},
	{Key: "domain.auth", Category: CategoryDomains, Type: TypeString, EnvVar: "DOMAIN_AUTH",
		Default: "", Description: "Auth surface domain."},
	{Key: "domain.cdn", Category: CategoryDomains, Type: TypeString, EnvVar: "DOMAIN_CDN",
		Default: "", Description: "CDN / static asset domain."},
	{Key: "session_cookie_domain", Category: CategoryDomains, Type: TypeString, EnvVar: "SESSION_COOKIE_DOMAIN",
		Default: "", Description: "Domain attribute for session cookies (empty = host-only).",
		// Cookies are minted from the boot-captured config.
		RestartRequired: true},

	// ─── ratelimit ───
	{Key: "ratelimit.api", Category: CategoryRateLimit, Type: TypeInt, EnvVar: "RATE_LIMIT_API",
		Default: "60", Description: "General API rate limit per IP per minute.",
		// Rate-limit middleware is built at boot.
		RestartRequired: true},
	{Key: "ratelimit.admin", Category: CategoryRateLimit, Type: TypeInt, EnvVar: "RATE_LIMIT_ADMIN",
		Default: "120", Description: "Admin API rate limit per IP per minute.", RestartRequired: true},
	{Key: "ratelimit.auth", Category: CategoryRateLimit, Type: TypeInt, EnvVar: "RATE_LIMIT_AUTH",
		Default: "60", Description: "Auth endpoints rate limit per IP per minute.", RestartRequired: true},
	{Key: "ratelimit.otp_send", Category: CategoryRateLimit, Type: TypeInt, EnvVar: "RATE_LIMIT_OTP_SEND",
		Default:         "30",
		Description:     "OTP send cap per IP per hour. Tight on purpose: the endpoint mails addresses the caller supplies.",
		RestartRequired: true},
	{Key: "ratelimit.bf_max_fails", Category: CategoryRateLimit, Type: TypeInt, EnvVar: "BF_MAX_FAILS",
		Default: "5", Description: "Failed license-key attempts per IP before the lockout on /license/*.",
		RestartRequired: true},
	{Key: "ratelimit.bf_lockout_seconds", Category: CategoryRateLimit, Type: TypeInt, EnvVar: "BF_LOCKOUT_SECONDS",
		Default: "30", Description: "Lockout duration in seconds after too many failed license-key attempts.",
		RestartRequired: true},

	// ─── webhook ───
	{Key: "webhook.max_attempts", Category: CategoryWebhook, Type: TypeInt, EnvVar: "WEBHOOK_MAX_ATTEMPTS",
		Default: "5", Description: "Delivery attempts per webhook event before it is marked failed.",
		// The webhook service is constructed at boot.
		RestartRequired: true},
	{Key: "webhook.retry_interval", Category: CategoryWebhook, Type: TypeDuration, EnvVar: "WEBHOOK_RETRY_INTERVAL",
		Default: "30s", Description: "Delay between webhook delivery retries (Go duration, e.g. 30s, 2m).",
		RestartRequired: true},
	{Key: "webhook.http_timeout", Category: CategoryWebhook, Type: TypeDuration, EnvVar: "WEBHOOK_HTTP_TIMEOUT",
		Default: "10s", Description: "HTTP timeout for each webhook delivery attempt.", RestartRequired: true},
	{Key: "webhook.allow_private", Category: CategoryWebhook, Type: TypeBool, EnvVar: "WEBHOOK_ALLOW_PRIVATE",
		Default:         "false",
		Description:     "Allow webhook deliveries to loopback/private/link-local addresses (SSRF guard off). Off by default.",
		RestartRequired: true},

	// ─── retention (consumed by the retention job; 0 = keep forever) ───
	// Defaults match service.RetentionDefaults: financial records are
	// NEVER deleted regardless of these windows.
	{Key: "retention.notifications_days", Category: CategoryRetention, Type: TypeInt, EnvVar: "RETENTION_NOTIFICATIONS_DAYS",
		Default: "90", Description: "Delete in-app notification rows older than this many days. 0 = keep forever."},
	{Key: "retention.processed_events_days", Category: CategoryRetention, Type: TypeInt, EnvVar: "RETENTION_PROCESSED_EVENTS_DAYS",
		Default: "30", Description: "Delete processed webhook-event dedup rows older than this many days. 0 = keep forever."},
	{Key: "retention.webhook_deliveries_days", Category: CategoryRetention, Type: TypeInt, EnvVar: "RETENTION_WEBHOOK_DELIVERIES_DAYS",
		Default: "90", Description: "Delete webhook delivery history older than this many days. 0 = keep forever."},
	{Key: "retention.audit_logs_days", Category: CategoryRetention, Type: TypeInt, EnvVar: "RETENTION_AUDIT_LOGS_DAYS",
		Default: "365", Description: "Delete audit-log rows older than this many days. 0 = keep forever."},

	// ─── storage (release artifacts) ───
	{Key: "storage.endpoint", Category: CategoryStorage, Type: TypeString, EnvVar: "STORAGE_ENDPOINT",
		Default: "", Description: "S3-compatible endpoint (e.g. R2). Empty = AWS S3. Empty bucket disables storage.",
		RestartRequired: true},
	{Key: "storage.region", Category: CategoryStorage, Type: TypeString, EnvVar: "STORAGE_REGION",
		Default: "auto", Description: "Storage region (R2 uses \"auto\").", RestartRequired: true},
	{Key: "storage.bucket", Category: CategoryStorage, Type: TypeString, EnvVar: "STORAGE_BUCKET",
		Default: "", Description: "Artifact bucket. With credentials present, enables release storage.",
		RestartRequired: true},
	{Key: "storage.access_key", Category: CategoryStorage, Type: TypeString, EnvVar: "STORAGE_ACCESS_KEY",
		Default: "", Description: "Storage access key id.", RestartRequired: true},
	{Key: "storage.secret_key", Category: CategoryStorage, Type: TypeSecret, EnvVar: "STORAGE_SECRET_KEY",
		Default: "", Description: "Storage secret access key (encrypted at rest, never returned by any API).",
		RestartRequired: true},
	{Key: "storage.public_url", Category: CategoryStorage, Type: TypeString, EnvVar: "STORAGE_PUBLIC_URL",
		Default: "", Description: "Optional CDN URL prefix for public artifact reads.", RestartRequired: true},
	{Key: "storage.force_path_style", Category: CategoryStorage, Type: TypeBool, EnvVar: "STORAGE_FORCE_PATH_STYLE",
		Default: "false", Description: "Path-style S3 addressing (MinIO and some self-hosted gateways need it).",
		RestartRequired: true},
	{Key: "storage.upload_ttl", Category: CategoryStorage, Type: TypeDuration, EnvVar: "STORAGE_UPLOAD_TTL",
		Default: "1h", Description: "Lifetime of presigned upload URLs.", RestartRequired: true},
	{Key: "storage.download_ttl", Category: CategoryStorage, Type: TypeDuration, EnvVar: "STORAGE_DOWNLOAD_TTL",
		Default: "10m", Description: "Lifetime of presigned download URLs.", RestartRequired: true},
	{Key: "storage.feed_url_ttl", Category: CategoryStorage, Type: TypeDuration, EnvVar: "STORAGE_FEED_URL_TTL",
		Default: "24h", Description: "Lifetime of artifact URLs embedded in public update feeds.",
		RestartRequired: true},
	{Key: "storage.max_release_sign_size_mb", Category: CategoryStorage, Type: TypeInt, EnvVar: "MAX_RELEASE_SIGN_SIZE_MB",
		Default: "500", Description: "Largest artifact (MB) signed server-side; larger ones must ship unsigned.",
		RestartRequired: true},

	// ─── observability ───
	{Key: "observability.log_level", Category: CategoryObservability, Type: TypeString, EnvVar: "LOG_LEVEL",
		Default:         "info",
		Description:     "Boot log level: debug, info, warn, or error.",
		RestartRequired: true},

	// ─── branding (public; shared rows with the legacy settings UI) ───
	{Key: "site_name", Category: CategoryBranding, Type: TypeString, EnvVar: "SITE_NAME",
		Default: "", Description: "Public site/product name shown to customers."},
	{Key: "timezone", Category: CategoryBranding, Type: TypeString, EnvVar: "TIMEZONE",
		Default: "", Description: "Display timezone for the admin UI (IANA name, e.g. Asia/Ho_Chi_Minh)."},
	{Key: "brand_color", Category: CategoryBranding, Type: TypeString, EnvVar: "BRAND_COLOR",
		Default: "", Description: "Brand accent color (CSS color, e.g. #2563eb)."},
	{Key: "language", Category: CategoryBranding, Type: TypeString, EnvVar: "LANGUAGE",
		Default: "", Description: "Default UI language (en, vi, zh)."},
	{Key: "logo_url", Category: CategoryBranding, Type: TypeString, EnvVar: "LOGO_URL",
		Default: "", Description: "Public logo URL."},
}

// cfgsvcIndex keys the catalog by Key for lookups.
var cfgsvcIndex = func() map[string]Entry {
	m := make(map[string]Entry, len(cfgsvcCatalog))
	for _, e := range cfgsvcCatalog {
		m[e.Key] = e
	}
	return m
}()

// Catalog returns a copy of the full catalog in declaration order.
func Catalog() []Entry {
	return append([]Entry(nil), cfgsvcCatalog...)
}

// FindEntry returns the catalog entry for key.
func FindEntry(key string) (Entry, bool) {
	e, ok := cfgsvcIndex[key]
	return e, ok
}

// EntriesInCategory returns the catalog entries of a category in
// declaration order.
func EntriesInCategory(category string) []Entry {
	var out []Entry
	for _, e := range cfgsvcCatalog {
		if e.Category == category {
			out = append(out, e)
		}
	}
	return out
}

// IsSecretEntry reports whether the entry holds a credential. Secret
// values are sealed at rest by the settings layer and are never
// returned by any API — the admin UI shows only whether one is set.
func IsSecretEntry(e Entry) bool { return e.Type == TypeSecret }

// BootstrapEnvVars are the environment variables that stay in the
// environment forever. They carry key material and infrastructure
// addresses the server needs BEFORE any database is reachable, so they
// are deliberately absent from the catalog.
var BootstrapEnvVars = []string{
	"PORT",
	"DATABASE_URL",
	"JWT_SECRET",
	"LICENSE_SIGNING_KEY",
	"SECRET_ENCRYPTION_KEY",
	"RELEASE_KEY_ENCRYPTION_KEY",
	"REFERRAL_HASH_SALT",
	"REDIS_URL",
}

// IsBootstrapEnvVar reports whether name is a bootstrap variable that
// must never move into the database.
func IsBootstrapEnvVar(name string) bool {
	for _, b := range BootstrapEnvVars {
		if b == name {
			return true
		}
	}
	return false
}

// DeprecatedEnvVars returns the catalog env vars that are supplied in
// the environment right now. Boot logs one deprecation line per entry:
// these values belong in the settings table, and the env var is only a
// fallback for installs that predate config-in-DB. Returned in catalog
// order for stable log output.
func DeprecatedEnvVars() []string {
	var out []string
	for _, e := range cfgsvcCatalog {
		if e.EnvVar == "" || IsBootstrapEnvVar(e.EnvVar) {
			continue
		}
		if strings.TrimSpace(os.Getenv(e.EnvVar)) != "" {
			out = append(out, e.EnvVar)
		}
	}
	return out
}

// SortedSecretKeys returns every catalog key of type secret, sorted.
// The settings layer seals exactly these at rest (see
// store.IsSecretSettingKey).
func SortedSecretKeys() []string {
	var out []string
	for _, e := range cfgsvcCatalog {
		if e.Type == TypeSecret {
			out = append(out, e.Key)
		}
	}
	sort.Strings(out)
	return out
}

// ─── Typed credential bundles ───

// StripeConfig is the typed Stripe configuration assembled from the
// payment.stripe_* keys.
type StripeConfig struct {
	SecretKey     string
	WebhookSecret string
	Livemode      bool
}

// PaymentCreds is the typed bundle handed to the payment providers.
type PaymentCreds struct {
	Stripe  StripeConfig
	Pay2S   Pay2SConfig
	ZaloPay ZaloPayConfig
	PayOS   PayOSConfig
}

// DeriveStripeLivemode reproduces the legacy deriveLivemode rule for
// installs that never set STRIPE_LIVEMODE / payment.stripe_livemode
// explicitly: live keys imply live mode, everything else is test.
// Operators must opt INTO live mode rather than fall into it.
func DeriveStripeLivemode(secretKey string) bool {
	return strings.HasPrefix(secretKey, "sk_live_") ||
		strings.HasPrefix(secretKey, "rk_live_")
}
