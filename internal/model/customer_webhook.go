package model

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/uptrace/bun"
)

// ─── Customer Webhook (portal self-service endpoint) ───
//
// A customer registers their own webhook endpoints in the portal
// (plan §29 "Webhooks", §35 Webhooks) and receives signed event
// deliveries. This is the customer-scoped twin of the operator/product
// Webhook in model.go: that one is scoped to a product and minted by an
// admin; this one is scoped to a portal user and minted by the customer.
// Two webhook systems, two tables (customer_webhooks vs webhooks).
//
// # SIGNING SECRET STORAGE DECISION
//
// The delivery signer must hold the secret in plaintext to compute the
// HMAC-SHA256 over each payload at dispatch time, so the row keeps the
// full secret in Secret. This mirrors the merchant Webhook.Secret,
// which is likewise stored plaintext (json:"-") — it is a shared
// symmetric signing key, not a login credential. It is NOT hashed
// (unlike a CustomerAPIKey, which is a bearer credential and is stored
// as a SHA-256 hash precisely because nothing ever needs the plaintext
// back).
//
// The secret is returned exactly once, at creation, and never again:
// Secret carries json:"-" so it is never serialised into any API
// response, and only SecretPrefix (a short display hint) is exposed.
// It must NEVER be logged.
//
// Encryption at rest is possible and desirable as a follow-up: derive a
// purpose-specific subkey (crypto.DeriveSubkey(master,
// "customer-webhook-secret")) into a new Store field, and wrap the
// insert/read the way LicenseKeyAEAD wraps a licence key
// (prepareLicenseForInsert / DecryptLicenseKey). That wiring lives in
// shared core (internal/store/store.go + cmd/server/main.go), which is
// out of scope for this file set, so it is documented here instead of
// half-wired.
type CustomerWebhook struct {
	bun.BaseModel `bun:"table:customer_webhooks"`

	ID     string `bun:",pk" json:"id"`
	UserID string `bun:",notnull" json:"-"`
	// URL is where deliveries are POSTed. Validated http(s) + no
	// loopback/private literal at write time (ValidateCustomerWebhookURL).
	URL string `bun:",notnull" json:"url"`
	// Events is the subscription list, drawn from CustomerWebhookEvents.
	Events []string `bun:",array" json:"events"`
	// Active gates real deliveries (the async dispatch path skips
	// inactive rows). A test fire is allowed regardless: testing an
	// endpoint is a diagnostic the customer triggers on purpose.
	Active bool `bun:",notnull,default:true" json:"active"`
	// Secret is the HMAC-SHA256 signing secret, in plaintext so the
	// signer can use it. json:"-" keeps it out of every response. NEVER
	// log it, NEVER return it after creation.
	Secret string `bun:",notnull" json:"-"`
	// SecretPrefix is the first CustomerWebhookSecretPrefixLength chars
	// of the secret — displayable, useless for signing. Never the secret.
	SecretPrefix string `bun:",notnull" json:"secret_prefix"`
	// LastDeliveryAt is stamped after a delivery attempt (test or real).
	LastDeliveryAt *time.Time `json:"last_delivery_at"`
	CreatedAt      time.Time  `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt      time.Time  `bun:",nullzero,default:now()" json:"updated_at"`
}

const (
	// CustomerWebhookSecretPrefix marks the credential, in the same
	// "whsec_" style used across the industry for webhook signing
	// secrets (and distinct from the htc_sk_ API-key prefix and the
	// kg_live_ product-key prefix so no namespace is mistaken for
	// another at a glance or in a log).
	CustomerWebhookSecretPrefix = "whsec_"
	// customerWebhookSecretLength is the length of the random part:
	// 40 characters of a 32-character alphabet = 200 bits of entropy.
	customerWebhookSecretLength = 40
	// customerWebhookAlphabet excludes 0/O and 1/I/L so a secret read
	// aloud or retyped is never ambiguous.
	customerWebhookAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	// CustomerWebhookSecretPrefixLength is how much of the secret is
	// kept for display. It is a weak display hint (telling rows apart
	// in a list — the URL is the real discriminator), never a security
	// boundary: the full secret carries 200 bits and is never exposed.
	CustomerWebhookSecretPrefixLength = 8
)

// NewCustomerWebhookSecret draws a fresh signing secret:
// CustomerWebhookSecretPrefix + 40 characters from the unambiguous
// alphabet, every draw from crypto/rand (no math/rand, no clock, no
// counters — the secret IS the credential).
func NewCustomerWebhookSecret() (string, error) {
	max := big.NewInt(int64(len(customerWebhookAlphabet)))
	out := make([]byte, customerWebhookSecretLength)
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", fmt.Errorf("customer webhook: generate secret: %w", err)
		}
		out[i] = customerWebhookAlphabet[n.Int64()]
	}
	return CustomerWebhookSecretPrefix + string(out), nil
}

// CustomerWebhookDisplayPrefix returns the part of the secret that is
// safe to keep and show: its first CustomerWebhookSecretPrefixLength
// characters. A secret shorter than that (only possible if a caller
// hands in something NewCustomerWebhookSecret did not make) is returned
// unchanged rather than panicked on.
func CustomerWebhookDisplayPrefix(secret string) string {
	if len(secret) > CustomerWebhookSecretPrefixLength {
		return secret[:CustomerWebhookSecretPrefixLength]
	}
	return secret
}

// CustomerWebhookEvents is the subscription vocabulary: EXACTLY the
// event names the platform dispatches (the model.Event* constants in
// model.go — including the ones several call sites still send as
// literals). A customer may subscribe to any of these and nothing
// else; writes are validated against this list. The names are shared
// with the merchant webhook system so a receiver handles one event
// vocabulary regardless of which system delivers.
var CustomerWebhookEvents = []string{
	EventLicenseCreated,
	EventLicenseActivated,
	EventLicenseDeactivated,
	EventLicenseExpiryChanged,
	EventLicenseExpired,
	EventLicenseCanceled,
	EventLicenseSuspended,
	EventLicenseReinstated,
	EventLicenseRevoked,
	EventLicensePaymentFailed,
	EventLicensePaymentRecovered,
	EventQuotaWarning,
	EventQuotaExceeded,
	EventSeatAdded,
	EventSeatRemoved,
	EventPlanChanged,
	EventReleasePublished,
	EventReleaseYanked,
	EventReleaseUnyanked,
	EventProductCreated,
	EventProductUpdated,
	EventProductDeleted,
	EventOrderFailed,
	EventOrderRefunded,
	EventInvoicePaid,
	EventInvoiceVoided,
	EventSubscriptionRenewed,
	EventUsageThresholdReached,
}

// IsCustomerWebhookEvent reports whether name is in the subscription
// vocabulary.
func IsCustomerWebhookEvent(name string) bool {
	for _, e := range CustomerWebhookEvents {
		if e == name {
			return true
		}
	}
	return false
}

// FoldCustomerWebhookEvents folds what a request sent into the shape
// that gets stored: each entry trimmed, blanks dropped, duplicates
// removed (first occurrence wins, order preserved). It refuses an empty
// result (a webhook subscribed to nothing is a row that never fires) and
// any name outside the vocabulary, naming the valid set so a client can
// fix the request.
func FoldCustomerWebhookEvents(events []string) ([]string, error) {
	seen := make(map[string]bool, len(events))
	out := make([]string, 0, len(events))
	for _, raw := range events {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		if !IsCustomerWebhookEvent(name) {
			return nil, fmt.Errorf("unknown event %q, expected one of: %s",
				name, strings.Join(CustomerWebhookEvents, ", "))
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, errors.New("at least one event is required")
	}
	return out, nil
}

// ValidateCustomerWebhookURL enforces the write-time URL policy:
//
//   - a full http:// or https:// URL with a host, at most 2048 chars;
//   - no loopback / private / link-local / unspecified target when it is
//     written as a literal IP or "localhost".
//
// The literal check is the cheap, always-on guard against a customer
// pointing a webhook at the metadata service or an internal host. DNS
// names are NOT resolved here: DNS can change between save and delivery,
// so the authoritative guard is the delivery-time resolved-IP check the
// real dispatch path performs (the merchant WebhookService dialer). This
// documents exactly what is and is not enforced at write time.
func ValidateCustomerWebhookURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("url is required")
	}
	if len(raw) > 2048 {
		return errors.New("url is too long (max 2048 characters)")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("url must be a full http(s) URL, such as https://example.com/webhooks")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("url must start with https:// or http://")
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return errors.New("url must not point at loopback or private addresses")
	}
	if ip := net.ParseIP(host); ip != nil && isNonPublicCustomerWebhookIP(ip) {
		return errors.New("url must not point at loopback or private addresses")
	}
	return nil
}

// isNonPublicCustomerWebhookIP is the cheap write-time target check: it
// rejects the literal addresses a webhook must never be delivered to.
// It is deliberately smaller than the delivery-time guard (no DNS
// resolution) — it is a fast filter at save time, not the authoritative
// SSRF defence (see ValidateCustomerWebhookURL).
//
// Beyond the std-lib predicates it covers the reserved ranges those
// predicates miss: CGNAT 100.64/10 (which contains the Alibaba metadata
// service at 100.100.100.100), 0.0.0.0/8 ("this network" —
// IsUnspecified matches only 0.0.0.0 itself), the documentation and
// benchmarking ranges, and the IPv6 transition ranges that can carry a
// tunneled IPv4 target (NAT64, Teredo, 6to4). Multicast and the limited
// broadcast are refused as well — none of them are legitimate webhook
// targets.
func isNonPublicCustomerWebhookIP(ip net.IP) bool {
	if ip == nil {
		return true // unparseable: fail closed
	}
	if ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast() {
		return true
	}
	for _, n := range nonPublicCustomerWebhookNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// nonPublicCustomerWebhookNets is the fixed table of reserved ranges the
// std-lib IP predicates do not cover. Parsed once; a typo in a CIDR
// literal panics at init (fail closed) rather than admitting the range.
var nonPublicCustomerWebhookNets = mustParseCIDRs(
	"0.0.0.0/8",       // "this network"
	"100.64.0.0/10",   // CGNAT (RFC 6598) — Alibaba metadata 100.100.100.100
	"192.0.0.0/24",    // IETF protocol assignments
	"192.0.2.0/24",    // TEST-NET-1
	"198.18.0.0/15",   // benchmarking (RFC 2544)
	"198.51.100.0/24", // TEST-NET-2
	"203.0.113.0/24",  // TEST-NET-3
	"240.0.0.0/4",     // reserved + limited broadcast 255.255.255.255
	"64:ff9b::/96",    // NAT64 (RFC 6052)
	"2001::/32",       // Teredo (RFC 4380)
	"2002::/16",       // 6to4 (RFC 3056)
)

func mustParseCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("model: bad webhook IP filter CIDR " + c + ": " + err.Error())
		}
		out = append(out, n)
	}
	return out
}
