package model

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"

	"github.com/uptrace/bun"
)

// ─── Enterprise SSO + SCIM groundwork (Phase 8, slice 1) ───
//
// Admin-managed SSO connection configuration (SAML + OIDC) and SCIM
// provisioning tokens. This slice is CONFIGURATION + TOKENS ONLY.
//
// What lives here is the pair of tables an operator needs BEFORE any
// single sign-on handshake or directory sync can run:
//
//   - sso_connections — one row per identity-provider connection: which
//     email domain it serves, whether it speaks SAML or OIDC, and the
//     provider-specific settings (entity id / SSO URL / certificate, or
//     issuer / client id / client secret / scopes) that the handshake
//     will later read.
//
//   - scim_tokens — the bearer tokens a directory (Okta, Entra ID, …)
//     will present to a future SCIM 2.0 provisioning endpoint. This
//     slice mints and manages those tokens; the /Users and /Groups sync
//     endpoints that will consume them are explicitly future work.
//
// # WHAT IS DELIBERATELY *NOT* HERE (documented future seams)
//
// None of the runtime half of enterprise SSO is in this slice. The
// seams are named so the next slice knows exactly where each plugs in,
// but none of them is stubbed as a route:
//
//   - SAML: the AuthnRequest issuer and the ACS (Assertion Consumer
//     Service) callback that will read sso_connections.saml_* to build
//     and validate a SAML Response. There is no /sso/saml/... route.
//
//   - OIDC: the authorization-code flow (redirect to oidc_issuer,
//     exchange the code using oidc_client_id/secret, verify the ID
//     token). There is no /sso/oidc/... route.
//
//   - SCIM 2.0: /scim/v2/Users and /scim/v2 Groups create/update/deprovision
//     sync, authenticated by a scim_tokens bearer token. There is no
//     /scim/... route. store.FindSCIMTokenByHash is the seam the future
//     token-auth middleware will call: hash the presented bearer token,
//     look it up, and refuse anything revoked or unknown.
//
//   - Domain verification: proving the operator owns sso_connections.domain
//     (a DNS TXT challenge) before the connection may be enabled.
//
// # OIDC CLIENT SECRET — STORAGE DECISION
//
// oidc_client_secret is stored in PLAINTEXT, exactly like the merchant
// Webhook.Secret and the CustomerWebhook.Secret: it is a shared
// symmetric credential the OIDC token exchange must present back to the
// IdP verbatim, so it cannot be hashed (nothing can recover it from a
// hash) and there is no signing step that could use a derived key. It
// is json:"-" so it is NEVER serialised into any API response — the
// admin sets it on create/update and never reads it back.
//
// Encryption at rest is possible and desirable as a follow-up, on the
// same plan as the webhook secrets: derive a purpose-specific subkey
// (crypto.DeriveSubkey(master, "sso-oidc-client-secret")) and wrap the
// insert/read the way LicenseKeyAEAD wraps a licence key. That wiring
// lives in shared core (internal/store/store.go + cmd/server/main.go),
// which is out of scope for this file set, so it is documented here
// rather than half-wired.

// SSO provider-type vocabulary. Closed — the handler refuses anything
// outside these two — because provider_type decides which settings are
// required (see SSOConnection.Validate) and which handshake a future
// login route will run. A made-up type would be silently ignored by
// both.
const (
	SSOProviderSAML = "saml"
	SSOProviderOIDC = "oidc"
)

// SSOProviderTypes lists the vocabulary in the order the admin UI
// offers it. It is the single source the validation and any select box
// read from, so the two cannot drift apart.
var SSOProviderTypes = []string{SSOProviderSAML, SSOProviderOIDC}

// ValidSSOProviderType reports whether t is one of the two providers a
// connection may be configured for.
func ValidSSOProviderType(t string) bool {
	return t == SSOProviderSAML || t == SSOProviderOIDC
}

// SSOConnection is one identity-provider connection.
//
// The provider-specific fields are pointers, not plain strings, because
// they are genuinely nullable in the database and the admin API's PATCH
// convention needs to tell three intents apart — absent (keep), "" (clear
// to NULL), a value (set). See handler/sso_admin.go's merge and the
// migration's provider-config CHECK, which is the database backstop for
// the same rule Validate enforces in Go.
//
// The SAML fields are only meaningful when ProviderType == "saml" and
// the OIDC fields only when it is "oidc". The other provider's fields
// are left NULL; the CHECK only constrains the ACTIVE provider's fields
// to be present, so an unused field is free to be NULL.
type SSOConnection struct {
	bun.BaseModel `bun:"table:sso_connections"`

	ID   string `bun:",pk" json:"id"`
	Name string `bun:",notnull" json:"name"`
	// ProviderType is one of the SSOProviderType* values.
	ProviderType string `bun:",notnull" json:"provider_type"`
	// Domain is the email domain this connection serves (e.g.
	// "acme.com"). Stored folded lowercase (see NormalizeSSODomain) and
	// unique: one domain maps to exactly one connection, so a login for
	// alice@acme.com cannot be routed ambiguously. The fold means one
	// domain cannot be spelled two ways to slip past the index.
	Domain string `bun:",notnull,unique" json:"domain"`
	// Enabled gates whether the connection may be used for sign-in. A
	// disabled connection keeps its config but refuses logins; the
	// handler always writes an explicit value (there is no bun
	// `default:` tag — the column keeps its CREATE-TABLE DEFAULT true
	// for plain SQL). Toggled only via SetSSOEnabled / the enable|disable
	// endpoints, never by the general PATCH.
	Enabled bool `bun:",notnull" json:"enabled"`

	// ── SAML settings (ProviderType == "saml") ──
	// The column names are pinned explicitly (column:...) because these
	// field names lead with the SAML/OIDC acronym, and acronyms are where
	// a snake_case inflection is easy to get subtly wrong; the pin keeps
	// the model, the migration and store.UpdateSSOConnection's Column()
	// list in lockstep.
	//
	// SAMLEntityID is the IdP's entity id (the Audience).
	SAMLEntityID *string `bun:"column:saml_entity_id" json:"saml_entity_id,omitempty"`
	// SAMLSSOURL is the IdP's HTTP-Redirect SSO endpoint.
	SAMLSSOURL *string `bun:"column:saml_sso_url" json:"saml_sso_url,omitempty"`
	// SAMLCertificate is the IdP's X.509 signing certificate as PEM
	// (BEGIN CERTIFICATE …). Public; safe to return in responses.
	SAMLCertificate *string `bun:"column:saml_certificate" json:"saml_certificate,omitempty"`

	// ── OIDC settings (ProviderType == "oidc") ──
	// OIDCIssuer is the OIDC discovery issuer URL.
	OIDCIssuer *string `bun:"column:oidc_issuer" json:"oidc_issuer,omitempty"`
	// OIDCClientID is the OAuth client id.
	OIDCClientID *string `bun:"column:oidc_client_id" json:"oidc_client_id,omitempty"`
	// OIDCClientSecret is the OAuth client secret. Stored plaintext (see
	// the header note) and json:"-" so it is never echoed back — it is
	// write-only through the admin API.
	OIDCClientSecret *string `bun:"column:oidc_client_secret" json:"-"`
	// OIDCScopes is the space-separated scope list to request (optional;
	// empty means the provider default).
	OIDCScopes *string `bun:"column:oidc_scopes" json:"oidc_scopes,omitempty"`

	CreatedAt time.Time `bun:",nullzero,default:now()" json:"created_at"`
	UpdatedAt time.Time `bun:",nullzero,default:now()" json:"updated_at"`
}

// ssoStr dereferences an optional string field, answering "" for nil.
// It is the one place the nil-vs-empty distinction is flattened for the
// checks that only care whether something was configured at all.
func ssoStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ssoDomainShape is the loose domain shape a connection's domain must
// have: labels of letters/digits/dots/hyphens ending in a dot and a
// two-or-more-letter TLD. It is deliberately BASIC — it does not prove
// the domain exists or is reachable, only that it is shaped like one
// (the DNS-TXT verification is future work). Anchored, so an email, a
// URL, or a path is not mistaken for a domain.
var ssoDomainShape = regexp.MustCompile(`^[a-z0-9.-]+\.[a-z]{2,}$`)

// NormalizeSSODomain folds a connection domain into its stored
// canonical form — trimmed and lower-cased — and refuses anything that
// is not shaped like a bare domain.
//
// Folding rather than refusing a case variant means one domain cannot
// exist under several spellings ("Acme.COM" and "acme.com" are one
// connection), which is what the unique index on domain and the
// future FindSSOConnectionByDomain login lookup both rely on. The fold
// is what makes the uniqueness mean anything.
//
// Unlike model.NormalizeResellerEmail (which only folds and defers
// validation to apperr.ValidateEmail), this both folds AND checks the
// shape, because there is no separate "is this a domain" validator in
// the house and the shape rule belongs with the fold that makes it
// canonical. It returns an error — never a silently-mangled value — so
// a bad domain is the caller's 400, not a row that no lookup will ever
// match.
//
// Idempotent: folding a folded domain returns it unchanged.
func NormalizeSSODomain(raw string) (string, error) {
	d := strings.ToLower(strings.TrimSpace(raw))
	if d == "" {
		return "", errors.New("domain is required")
	}
	if !ssoDomainShape.MatchString(d) {
		return "", errors.New("domain must be a bare domain such as acme.com")
	}
	return d, nil
}

// ValidPEMCertificate reports whether pem looks like an X.509
// certificate in PEM armour. The check is deliberately LOOSE — it only
// requires the BEGIN CERTIFICATE marker — because parsing and chain
// validation belong to the SAML handshake that will consume it (future
// work); here the goal is to catch the obvious mistake of pasting a
// private key, a URL, or base64 with no armour, before it is stored.
func ValidPEMCertificate(pem string) bool {
	return strings.Contains(pem, "BEGIN CERTIFICATE")
}

// Validate reports whether the connection carries a complete, coherent
// provider configuration: a recognised provider type, and for that
// type every required setting present (and, for SAML, a certificate
// that is actually PEM-armoured). It returns the first problem as a
// plain error whose message is safe to hand a caller.
//
// It is the Go twin of the migration's sso_connections_provider_config_check
// CHECK — both refuse a saml connection with no certificate and an oidc
// connection with no issuer. The handler runs it to answer 400 with a
// human message; the store runs it as a backstop (ErrSSOInvalidConfig)
// so a caller that skipped the handler cannot insert a row the database
// would reject. Name and domain are NOT checked here — they are the
// handler's and the unique indexes' concern (see NormalizeSSODomain and
// the name bound in handler/sso_admin.go).
func (c *SSOConnection) Validate() error {
	switch c.ProviderType {
	case SSOProviderSAML:
		if ssoStr(c.SAMLEntityID) == "" {
			return errors.New("saml_entity_id is required for a saml connection")
		}
		if ssoStr(c.SAMLSSOURL) == "" {
			return errors.New("saml_sso_url is required for a saml connection")
		}
		pem := ssoStr(c.SAMLCertificate)
		if pem == "" {
			return errors.New("saml_certificate is required for a saml connection")
		}
		if !ValidPEMCertificate(pem) {
			return errors.New("saml_certificate must be a PEM certificate (BEGIN CERTIFICATE)")
		}
	case SSOProviderOIDC:
		if ssoStr(c.OIDCIssuer) == "" {
			return errors.New("oidc_issuer is required for an oidc connection")
		}
		if ssoStr(c.OIDCClientID) == "" {
			return errors.New("oidc_client_id is required for an oidc connection")
		}
	default:
		return errors.New("provider_type must be one of: " + strings.Join(SSOProviderTypes, ", "))
	}
	return nil
}

// ─── SCIM provisioning token ───
//
// A bearer token a directory will present to a future SCIM 2.0
// provisioning endpoint. It is a LOGIN CREDENTIAL, so — exactly like a
// CustomerAPIKey — it is stored as a SHA-256 hash and the plaintext is
// returned exactly once, at creation, and never recoverable afterwards.
// Only TokenPrefix (a short display hint) and the hash are kept.
//
// Revocation is soft (RevokedAt) so the row keeps its audit trail: who
// created it, when it was last used, when it died. Hard deletion exists
// (store.DeleteSCIMToken) for when the trail is not wanted.

const (
	// SCIMTokenPrefix marks the credential. It deliberately does NOT
	// collide with htc_sk_ (customer API keys), whsec_ (webhook secrets)
	// or kg_live_ (product keys), so no namespace is mistaken for
	// another at a glance or in a log.
	SCIMTokenPrefix = "htc_scim_"
	// scimTokenLength is the length of the random part: 40 characters
	// of a 32-character alphabet = 200 bits of entropy.
	scimTokenLength = 40
	// scimTokenAlphabet excludes 0/O and 1/I/L so a token read aloud or
	// retyped from a password manager is never ambiguous.
	scimTokenAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	// SCIMTokenDisplayPrefixLength is how much of the token is kept for
	// display ("htc_scim_" + 4 random characters): enough to tell two
	// tokens apart in a list, useless to anyone who does not hold the
	// full token. It is a weak display hint, never a security boundary —
	// the full token carries 200 bits and is never exposed.
	SCIMTokenDisplayPrefixLength = 13
)

// SCIMToken is one provisioning credential.
//
// The plaintext is never a field on this struct: the row keeps only
// TokenHash (SHA-256 hex) and TokenPrefix (display). The one time the
// plaintext exists is the return value of store.CreateSCIMToken, which
// the handler shows once and drops.
type SCIMToken struct {
	bun.BaseModel `bun:"table:scim_tokens"`

	ID   string `bun:",pk" json:"id"`
	Name string `bun:",notnull" json:"name"`
	// TokenHash is the SHA-256 hex of the full token. json:"-" keeps it
	// out of every response. NEVER exposed, NEVER logged.
	TokenHash string `bun:"column:token_hash,notnull,unique" json:"-"`
	// TokenPrefix is the first SCIMTokenDisplayPrefixLength characters
	// of the token. Displayable; useless for authentication.
	TokenPrefix string `bun:"column:token_prefix,notnull" json:"token_prefix"`
	// LastUsedAt is stamped by store.TouchSCIMTokenLastUsed (future SCIM
	// middleware) on a successful authentication.
	LastUsedAt *time.Time `bun:"column:last_used_at" json:"last_used_at,omitempty"`
	// RevokedAt is the soft-revoke stamp. A revoked token is dead.
	RevokedAt *time.Time `bun:"column:revoked_at" json:"revoked_at,omitempty"`
	CreatedAt time.Time  `bun:",nullzero,default:now()" json:"created_at"`
}

// NewSCIMToken draws a fresh provisioning token: SCIMTokenPrefix + 40
// characters from the unambiguous alphabet, every draw from crypto/rand
// (no math/rand, no clock, no counters — the token IS the credential).
// It returns the plaintext and its safe display prefix.
//
// The signature carries an error because the entropy source does: a
// crypto/rand failure must never be swallowed to produce a predictable
// token. This mirrors NewCustomerWebhookSecret and
// NewCustomerAPIKeySecret exactly; the named returns (plaintext, prefix)
// are the two things a caller needs to store-and-show-once around it.
func NewSCIMToken() (plaintext, prefix string, err error) {
	max := big.NewInt(int64(len(scimTokenAlphabet)))
	out := make([]byte, scimTokenLength)
	for i := range out {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", "", fmt.Errorf("scim token: generate token: %w", err)
		}
		out[i] = scimTokenAlphabet[n.Int64()]
	}
	plaintext = SCIMTokenPrefix + string(out)
	return plaintext, SCIMTokenDisplayPrefix(plaintext), nil
}

// SCIMTokenDisplayPrefix returns the part of the token that is safe to
// keep and show: its first SCIMTokenDisplayPrefixLength characters. A
// token shorter than that (only possible if a caller handed in something
// NewSCIMToken did not make) is returned unchanged rather than panicked
// on.
func SCIMTokenDisplayPrefix(token string) string {
	if len(token) > SCIMTokenDisplayPrefixLength {
		return token[:SCIMTokenDisplayPrefixLength]
	}
	return token
}

// HashSCIMToken reduces a plaintext token to the SHA-256 hex stored at
// rest. It is the exact recipe store.HashAPIKey and
// model.CustomerAPIKey use — one hashing scheme across every bearer
// credential in the codebase. A future SCIM middleware hashes the
// presented token with this and looks the result up in
// store.FindSCIMTokenByHash.
func HashSCIMToken(plain string) string {
	h := sha256.Sum256([]byte(plain))
	return hex.EncodeToString(h[:])
}
