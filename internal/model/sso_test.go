package model

import (
	"strings"
	"testing"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
)

// The domain is the handle a future login routes on, so it is stored
// folded and unique — the fold is what makes the uniqueness mean
// anything. It is also shape-checked, because "shaped like a domain" is
// the one thing a login lookup can rely on before DNS verification
// (future work) proves ownership.
func TestNormalizeSSODomain(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"acme.com", "acme.com", false},
		{"Acme.COM", "acme.com", false},
		{"  sub.acme.co.uk  ", "sub.acme.co.uk", false},
		{"xn--bcher-kva.example", "xn--bcher-kva.example", false},
		// Not shaped like a bare domain.
		{"", "", true},
		{"   ", "", true},
		{"acme", "", true},            // no dot / TLD
		{"acme.", "", true},           // trailing dot, no TLD label
		{"http://acme.com", "", true}, // a URL, not a domain
		{"user@acme.com", "", true},   // an email, not a domain
		{"acme.com/path", "", true},   // a path, not a domain
		{"acme .com", "", true},       // inner space
	}
	for _, tc := range cases {
		got, err := NormalizeSSODomain(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("NormalizeSSODomain(%q) = %q, want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeSSODomain(%q) error = %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("NormalizeSSODomain(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Idempotent: folding a folded domain returns it unchanged.
	folded, err := NormalizeSSODomain("Acme.COM")
	if err != nil {
		t.Fatalf("NormalizeSSODomain: %v", err)
	}
	if again, err := NormalizeSSODomain(folded); err != nil || again != folded {
		t.Errorf("not idempotent: %q -> %q (err %v)", folded, again, err)
	}
}

// The provider vocabulary is closed — saml and oidc and nothing else —
// because provider_type decides which settings are required and which
// handshake a future login route runs.
func TestValidSSOProviderType(t *testing.T) {
	for _, ok := range SSOProviderTypes {
		if !ValidSSOProviderType(ok) {
			t.Errorf("ValidSSOProviderType(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "SAML", "Oidc", "ldap", "saml ", "jwt"} {
		if ValidSSOProviderType(bad) {
			t.Errorf("ValidSSOProviderType(%q) = true, want false", bad)
		}
	}
}

// The PEM check is deliberately loose — only the armour marker — because
// parsing and chain validation belong to the SAML handshake. The goal is
// to catch the obvious mistake (a private key, a URL, raw base64) before
// it is stored.
func TestValidPEMCertificate(t *testing.T) {
	if !ValidPEMCertificate("-----BEGIN CERTIFICATE-----\nMIIB...\n-----END CERTIFICATE-----") {
		t.Error("a PEM-armoured certificate was refused")
	}
	for _, bad := range []string{"", "not a pem", "-----BEGIN PRIVATE KEY-----\n...", "https://idp/cert"} {
		if ValidPEMCertificate(bad) {
			t.Errorf("ValidPEMCertificate(%q) = true, want false", bad)
		}
	}
}

// Validate is the Go twin of the migration's provider-config CHECK: a
// saml connection is refused without its certificate, an oidc one without
// its issuer, and a made-up provider type is refused outright. Each
// refusal names the missing field so the handler can answer a specific
// 400.
func TestSSOConnectionValidate(t *testing.T) {
	pem := "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----"
	s := func(v string) *string { return &v }

	ok := []*SSOConnection{
		{ProviderType: SSOProviderSAML, SAMLEntityID: s("e"), SAMLSSOURL: s("https://idp/sso"), SAMLCertificate: s(pem)},
		{ProviderType: SSOProviderOIDC, OIDCIssuer: s("https://idp"), OIDCClientID: s("cid")},
		// Extra optional fields are fine.
		{ProviderType: SSOProviderOIDC, OIDCIssuer: s("https://idp"), OIDCClientID: s("cid"), OIDCClientSecret: s("sh"), OIDCScopes: s("openid email")},
	}
	for i, conn := range ok {
		if err := conn.Validate(); err != nil {
			t.Errorf("valid config %d refused: %v", i, err)
		}
	}

	bad := []struct {
		name string
		conn *SSOConnection
		want string // a fragment the message must contain
	}{
		{"saml without entity id", &SSOConnection{ProviderType: SSOProviderSAML, SAMLSSOURL: s("u"), SAMLCertificate: s(pem)}, "saml_entity_id"},
		{"saml without sso url", &SSOConnection{ProviderType: SSOProviderSAML, SAMLEntityID: s("e"), SAMLCertificate: s(pem)}, "saml_sso_url"},
		{"saml without certificate", &SSOConnection{ProviderType: SSOProviderSAML, SAMLEntityID: s("e"), SAMLSSOURL: s("u")}, "saml_certificate"},
		{"saml with non-PEM certificate", &SSOConnection{ProviderType: SSOProviderSAML, SAMLEntityID: s("e"), SAMLSSOURL: s("u"), SAMLCertificate: s("garbage")}, "PEM"},
		{"oidc without issuer", &SSOConnection{ProviderType: SSOProviderOIDC, OIDCClientID: s("cid")}, "oidc_issuer"},
		{"oidc without client id", &SSOConnection{ProviderType: SSOProviderOIDC, OIDCIssuer: s("https://idp")}, "oidc_client_id"},
		{"unknown provider type", &SSOConnection{ProviderType: "ldap"}, "provider_type"},
		{"empty provider type", &SSOConnection{}, "provider_type"},
	}
	for _, tc := range bad {
		err := tc.conn.Validate()
		if err == nil {
			t.Errorf("%s: Validate() = nil, want an error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", tc.name, err.Error(), tc.want)
		}
	}
}

// The token IS the credential (shown once at creation, never again), so
// its shape is worth pinning down without a database: the prefix that
// namespaces it, the alphabet that makes it readable, and the length
// that makes it strong.
func TestNewSCIMTokenShape(t *testing.T) {
	plaintext, prefix, err := NewSCIMToken()
	if err != nil {
		t.Fatalf("NewSCIMToken: %v", err)
	}
	if !strings.HasPrefix(plaintext, SCIMTokenPrefix) {
		t.Errorf("token %q does not start with %q", plaintext, SCIMTokenPrefix)
	}
	if want := len(SCIMTokenPrefix) + scimTokenLength; len(plaintext) != want {
		t.Errorf("token length = %d, want %d", len(plaintext), want)
	}
	for i, r := range plaintext[len(SCIMTokenPrefix):] {
		if !strings.ContainsRune(scimTokenAlphabet, r) {
			t.Errorf("token character %d = %q is not in the unambiguous alphabet", i, r)
		}
	}
	// The display prefix is the safe head of the token: the namespace
	// plus a few characters to tell tokens apart, never the whole thing.
	if want := SCIMTokenDisplayPrefixLength; len(prefix) != want {
		t.Errorf("prefix length = %d, want %d", len(prefix), want)
	}
	if prefix != plaintext[:SCIMTokenDisplayPrefixLength] {
		t.Errorf("prefix %q is not the head of the token %q", prefix, plaintext)
	}
	if prefix == plaintext {
		t.Error("prefix must not be the whole token")
	}
	// The namespace must not collide with the other credential prefixes.
	for _, ns := range []string{"htc_sk_", "whsec_", "kg_live_"} {
		if strings.HasPrefix(plaintext, ns) {
			t.Errorf("token collides with the %q namespace", ns)
		}
	}
}

// Two tokens that looked the same would be one credential wearing two
// names. 200 bits of entropy makes a collision fantastically unlikely; a
// missing randomness source would make it certain.
func TestNewSCIMTokensDiffer(t *testing.T) {
	a, _, err := NewSCIMToken()
	if err != nil {
		t.Fatalf("NewSCIMToken: %v", err)
	}
	b, _, err := NewSCIMToken()
	if err != nil {
		t.Fatalf("NewSCIMToken: %v", err)
	}
	if a == b {
		t.Error("two tokens are identical; the randomness source is not being used")
	}
}

// HashSCIMToken is the SHA-256 hex stored at rest — 64 lowercase hex
// chars, the same recipe as HashAPIKey / CustomerAPIKey. The plaintext
// is never recoverable from it, and the same plaintext always hashes the
// same way (so a lookup by hash matches).
func TestHashSCIMToken(t *testing.T) {
	plaintext, _, err := NewSCIMToken()
	if err != nil {
		t.Fatalf("NewSCIMToken: %v", err)
	}
	h := HashSCIMToken(plaintext)
	if len(h) != 64 {
		t.Errorf("hash length = %d, want 64", len(h))
	}
	for _, r := range h {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("hash %q is not lowercase hex", h)
			break
		}
	}
	if HashSCIMToken(plaintext) != h {
		t.Error("the same plaintext hashed two different ways")
	}
	if HashSCIMToken(plaintext+"x") == h {
		t.Error("two different plaintexts hashed the same")
	}
	// The hash must not leak the token.
	if strings.Contains(h, plaintext) {
		t.Error("hash contains the plaintext")
	}
}

// The SAML/OIDC field names lead with an acronym, which is exactly where
// a snake_case inflection is easy to get subtly wrong (SAMLSSOURL could
// become "samlssourl" instead of "saml_sso_url"). The model pins each
// column explicitly; this is the regression pin that the pins took — the
// generated SQL must name the columns the migration created and the
// store's UpdateSSOConnection Column() list writes.
func TestSSOConnectionColumnNames(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	ins := &SSOConnection{ID: "x", Name: "n", ProviderType: SSOProviderOIDC, Domain: "acme.com"}
	raw, err := db.NewInsert().Model(ins).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build insert: %v", err)
	}
	sqlText := string(raw)
	for _, col := range []string{
		"saml_entity_id", "saml_sso_url", "saml_certificate",
		"oidc_issuer", "oidc_client_id", "oidc_client_secret", "oidc_scopes",
	} {
		if !strings.Contains(sqlText, col) {
			t.Errorf("generated INSERT does not name column %q; got:\n%s", col, sqlText)
		}
	}

	tok := &SCIMToken{ID: "x", Name: "n"}
	raw, err = db.NewInsert().Model(tok).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build insert: %v", err)
	}
	sqlText = string(raw)
	for _, col := range []string{"token_hash", "token_prefix", "last_used_at", "revoked_at"} {
		if !strings.Contains(sqlText, col) {
			t.Errorf("generated INSERT does not name column %q; got:\n%s", col, sqlText)
		}
	}
}
