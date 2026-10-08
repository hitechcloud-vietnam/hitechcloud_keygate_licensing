package sso

import (
	"bytes"
	"compress/flate"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"io"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

// ─── test certificate factory ───

// makeSAMLCert mints a self-signed RSA certificate and returns the private
// key, its DER bytes (for goxmldsig signing) and its PEM (as the IdP cert a
// SSO connection would pin).
func makeSAMLCert(t *testing.T) (*rsa.PrivateKey, []byte, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-idp"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	pemStr := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	return key, der, pemStr
}

// samlResponseXML builds a single-line SAML Response carrying an assertion
// for the given email. Times are formatted in the SAML/XSD instant form.
func samlResponseXML(issuer, audience, email, notBefore, notOnOrAfter string) string {
	return `<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="_resp1" Version="2.0" IssueInstant="2026-03-20T10:00:00Z" Destination="https://sp.example.com/auth/sso/saml/acs"><saml:Issuer>` +
		issuer + `</saml:Issuer><samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status><saml:Assertion ID="_a1" Version="2.0" IssueInstant="2026-03-20T10:00:00Z"><saml:Issuer>` +
		issuer + `</saml:Issuer><saml:Subject><saml:NameID Format="urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress">` +
		email + `</saml:NameID></saml:Subject><saml:Conditions NotBefore="` + notBefore + `" NotOnOrAfter="` + notOnOrAfter + `"><saml:AudienceRestriction><saml:Audience>` +
		audience + `</saml:Audience></saml:AudienceRestriction></saml:Conditions><saml:AttributeStatement><saml:Attribute Name="emailaddress"><saml:AttributeValue>` +
		email + `</saml:AttributeValue></saml:Attribute><saml:Attribute Name="givenname"><saml:AttributeValue>Alice</saml:AttributeValue></saml:Attribute><saml:Attribute Name="surname"><saml:AttributeValue>Smith</saml:AttributeValue></saml:Attribute></saml:AttributeStatement></saml:Assertion></samlp:Response>`
}

// signResponse signs the whole <Response> (document-level enveloped
// signature) with the given key and returns the signed XML string.
func signResponse(t *testing.T, xmlStr string, key *rsa.PrivateKey, der []byte) string {
	t.Helper()
	doc := etree.NewDocument()
	if err := doc.ReadFromString(xmlStr); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	ctx, err := dsig.NewSigningContext(key, [][]byte{der})
	if err != nil {
		t.Fatalf("signing context: %v", err)
	}
	signed, err := ctx.SignEnveloped(doc.Root())
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	out := etree.NewDocument()
	out.SetRoot(signed)
	s, err := out.WriteToString()
	if err != nil {
		t.Fatalf("write signed doc: %v", err)
	}
	return s
}

// ─── signature verification (the security seam) ───

func TestVerifySignatureValidDocument(t *testing.T) {
	key, der, pemCert := makeSAMLCert(t)
	now := time.Now().UTC()
	xmlStr := signResponse(t,
		samlResponseXML("https://idp.example.com", "https://sp.example.com/auth/sso/saml",
			"alice@acme.com", FormatSAMLTime(now.Add(-time.Minute)), FormatSAMLTime(now.Add(time.Hour))),
		key, der)

	assertion, err := DefaultSignatureVerifier([]byte(xmlStr), pemCert)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if assertion.Email != "alice@acme.com" {
		t.Fatalf("email = %q", assertion.Email)
	}
	if assertion.Issuer != "https://idp.example.com" {
		t.Fatalf("issuer = %q", assertion.Issuer)
	}
	if assertion.NameID != "alice@acme.com" {
		t.Fatalf("nameID = %q", assertion.NameID)
	}
	if assertion.GivenName != "Alice" || assertion.FamilyName != "Smith" {
		t.Fatalf("name = %q / %q", assertion.GivenName, assertion.FamilyName)
	}
	if len(assertion.Audiences) != 1 || assertion.Audiences[0] != "https://sp.example.com/auth/sso/saml" {
		t.Fatalf("audiences = %v", assertion.Audiences)
	}
	if assertion.NotBefore == nil || assertion.NotOnOrAfter == nil {
		t.Fatalf("conditions bounds not parsed: %+v", assertion)
	}
}

func TestVerifySignatureRejectsUnsigned(t *testing.T) {
	_, _, pemCert := makeSAMLCert(t)
	now := time.Now().UTC()
	xmlStr := samlResponseXML("https://idp.example.com", "https://sp.example.com/auth/sso/saml",
		"alice@acme.com", FormatSAMLTime(now.Add(-time.Minute)), FormatSAMLTime(now.Add(time.Hour)))
	if _, err := DefaultSignatureVerifier([]byte(xmlStr), pemCert); err == nil {
		t.Fatal("unsigned response must be rejected")
	}
}

func TestVerifySignatureRejectsTampered(t *testing.T) {
	key, der, pemCert := makeSAMLCert(t)
	now := time.Now().UTC()
	signed := signResponse(t,
		samlResponseXML("https://idp.example.com", "https://sp.example.com/auth/sso/saml",
			"alice@acme.com", FormatSAMLTime(now.Add(-time.Minute)), FormatSAMLTime(now.Add(time.Hour))),
		key, der)

	tampered := strings.Replace(signed, "alice@acme.com", "mallory@acme.com", 1)
	if tampered == signed {
		t.Fatal("tamper target not found")
	}
	if _, err := DefaultSignatureVerifier([]byte(tampered), pemCert); err == nil {
		t.Fatal("tampered response must be rejected")
	}
}

func TestVerifySignatureRejectsWrongCert(t *testing.T) {
	key, der, _ := makeSAMLCert(t)
	_, _, otherPem := makeSAMLCert(t) // a DIFFERENT pinned cert
	now := time.Now().UTC()
	signed := signResponse(t,
		samlResponseXML("https://idp.example.com", "https://sp.example.com/auth/sso/saml",
			"alice@acme.com", FormatSAMLTime(now.Add(-time.Minute)), FormatSAMLTime(now.Add(time.Hour))),
		key, der)
	if _, err := DefaultSignatureVerifier([]byte(signed), otherPem); err == nil {
		t.Fatal("signature from an unpinned key must be rejected")
	}
}

func TestParseCertsRequiresCert(t *testing.T) {
	if _, err := parseCerts("not a pem"); err == nil {
		t.Fatal("expected error for PEM with no certificate")
	}
}

// ─── SAML Response parsing ───

func TestParseSAMLResponse(t *testing.T) {
	now := time.Now().UTC()
	xmlStr := samlResponseXML("https://idp.example.com", "https://sp.example.com/auth/sso/saml",
		"alice@acme.com", FormatSAMLTime(now), FormatSAMLTime(now.Add(time.Hour)))
	doc, err := ParseSAMLResponse([]byte(xmlStr))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Issuer != "https://idp.example.com" {
		t.Fatalf("issuer = %q", doc.Issuer)
	}
	if doc.StatusCode != samlStatusSuccess {
		t.Fatalf("status = %q", doc.StatusCode)
	}
	if doc.Destination != "https://sp.example.com/auth/sso/saml/acs" {
		t.Fatalf("destination = %q", doc.Destination)
	}
	if doc.ID != "_resp1" {
		t.Fatalf("id = %q", doc.ID)
	}
}

func TestParseSAMLResponseNotXML(t *testing.T) {
	if _, err := ParseSAMLResponse([]byte("garbage")); err == nil {
		t.Fatal("expected error for non-XML")
	}
}

// ─── ValidateSAML ───

func validAssertion(now time.Time) *SAMLAssertion {
	return &SAMLAssertion{
		Issuer:       "https://idp.example.com",
		Email:        "alice@acme.com",
		NotBefore:    timePtr(now.Add(-time.Minute)),
		NotOnOrAfter: timePtr(now.Add(time.Hour)),
		Audiences:    []string{"https://sp.example.com/auth/sso/saml"},
	}
}

func timePtr(t time.Time) *time.Time { return &t }

func TestValidateSAMLOK(t *testing.T) {
	now := time.Now().UTC()
	doc := &SAMLResponseDoc{StatusCode: samlStatusSuccess}
	err := ValidateSAML(doc, validAssertion(now), "https://idp.example.com",
		"https://sp.example.com/auth/sso/saml", now)
	if err != nil {
		t.Fatalf("expected valid, got %v", err)
	}
}

func TestValidateSAMLRejectsNonSuccessStatus(t *testing.T) {
	now := time.Now().UTC()
	doc := &SAMLResponseDoc{StatusCode: "urn:oasis:names:tc:SAML:2.0:status:Requester"}
	err := ValidateSAML(doc, validAssertion(now), "https://idp.example.com",
		"https://sp.example.com/auth/sso/saml", now)
	if err == nil {
		t.Fatal("expected non-Success status to fail")
	}
}

func TestValidateSAMLRejectsIssuerMismatch(t *testing.T) {
	now := time.Now().UTC()
	doc := &SAMLResponseDoc{StatusCode: samlStatusSuccess}
	err := ValidateSAML(doc, validAssertion(now), "https://other-idp.example.com",
		"https://sp.example.com/auth/sso/saml", now)
	if err == nil {
		t.Fatal("expected issuer mismatch to fail")
	}
}

func TestValidateSAMLRejectsNoConditionsWindow(t *testing.T) {
	now := time.Now().UTC()
	a := validAssertion(now)
	a.NotBefore = nil
	a.NotOnOrAfter = nil
	err := ValidateSAML(&SAMLResponseDoc{StatusCode: samlStatusSuccess}, a,
		"https://idp.example.com", "https://sp.example.com/auth/sso/saml", now)
	if err == nil {
		t.Fatal("an unbounded assertion must be refused (fail closed)")
	}
}

func TestValidateSAMLClockSkewBoundaries(t *testing.T) {
	const issuer = "https://idp.example.com"
	const aud = "https://sp.example.com/auth/sso/saml"
	now := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)

	t.Run("NotBefore within skew is accepted", func(t *testing.T) {
		a := validAssertion(now)
		a.NotBefore = timePtr(now.Add(ClockSkew)) // exactly at the skew edge
		if err := ValidateSAML(&SAMLResponseDoc{StatusCode: samlStatusSuccess}, a, issuer, aud, now); err != nil {
			t.Fatalf("within skew should pass: %v", err)
		}
	})
	t.Run("NotBefore beyond skew is rejected", func(t *testing.T) {
		a := validAssertion(now)
		a.NotBefore = timePtr(now.Add(ClockSkew + time.Second))
		if err := ValidateSAML(&SAMLResponseDoc{StatusCode: samlStatusSuccess}, a, issuer, aud, now); err == nil {
			t.Fatal("future NotBefore beyond skew must fail")
		}
	})
	t.Run("NotOnOrAfter within skew is accepted", func(t *testing.T) {
		a := validAssertion(now)
		// Expired by less than the skew allowance: still accepted. (SAML
		// NotOnOrAfter is exclusive, so the edge instant itself is expired.)
		a.NotOnOrAfter = timePtr(now.Add(-ClockSkew + time.Second))
		if err := ValidateSAML(&SAMLResponseDoc{StatusCode: samlStatusSuccess}, a, issuer, aud, now); err != nil {
			t.Fatalf("expired-within-skew should pass: %v", err)
		}
	})
	t.Run("NotOnOrAfter beyond skew is rejected", func(t *testing.T) {
		a := validAssertion(now)
		a.NotOnOrAfter = timePtr(now.Add(-ClockSkew - time.Second))
		if err := ValidateSAML(&SAMLResponseDoc{StatusCode: samlStatusSuccess}, a, issuer, aud, now); err == nil {
			t.Fatal("expired beyond skew must fail")
		}
	})
}

func TestValidateSAMLRejectsAudienceMismatch(t *testing.T) {
	now := time.Now().UTC()
	err := ValidateSAML(&SAMLResponseDoc{StatusCode: samlStatusSuccess}, validAssertion(now),
		"https://idp.example.com", "https://other-sp.example.com/auth/sso/saml", now)
	if err == nil {
		t.Fatal("audience mismatch must fail")
	}
}

func TestValidateSAMLRejectsNoEmail(t *testing.T) {
	now := time.Now().UTC()
	a := validAssertion(now)
	a.Email = ""
	err := ValidateSAML(&SAMLResponseDoc{StatusCode: samlStatusSuccess}, a,
		"https://idp.example.com", "https://sp.example.com/auth/sso/saml", now)
	if err == nil {
		t.Fatal("assertion with no email must fail")
	}
}

// ─── AuthnRequest (outbound) ───

func TestBuildAuthnRedirect(t *testing.T) {
	now := time.Date(2026, 3, 20, 10, 0, 0, 0, time.UTC)
	loc, id, err := BuildAuthnRedirect("https://idp.example.com/sso",
		"https://sp.example.com/auth/sso/saml",
		"https://sp.example.com/auth/sso/saml/acs",
		"/portal", "_req1", now)
	if err != nil {
		t.Fatal(err)
	}
	if id != "_req1" {
		t.Fatalf("id = %q", id)
	}
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if q.Get("RelayState") != "/portal" {
		t.Fatalf("RelayState = %q", q.Get("RelayState"))
	}
	// Decode + inflate the SAMLRequest and check it is our AuthnRequest.
	raw, err := base64.StdEncoding.DecodeString(q.Get("SAMLRequest"))
	if err != nil {
		t.Fatalf("SAMLRequest not base64: %v", err)
	}
	inflated, err := inflate(raw)
	if err != nil {
		t.Fatalf("SAMLRequest not DEFLATE: %v", err)
	}
	s := string(inflated)
	if !strings.Contains(s, "AuthnRequest") || !strings.Contains(s, "_req1") ||
		!strings.Contains(s, "https://sp.example.com/auth/sso/saml") {
		t.Fatalf("inflated AuthnRequest unexpected: %s", s)
	}
}

func TestBuildAuthnRedirectEmptySSOURL(t *testing.T) {
	if _, _, err := BuildAuthnRedirect("", "sp", "acs", "/portal", "", time.Now()); err == nil {
		t.Fatal("empty sso url must error")
	}
}

func TestBuildAuthnRedirectGeneratesID(t *testing.T) {
	_, id, err := BuildAuthnRedirect("https://idp.example.com/sso", "sp", "acs", "/portal", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "_") || len(id) < 5 {
		t.Fatalf("generated id %q looks wrong", id)
	}
}

// ─── shared helpers ───

func TestEmailDomain(t *testing.T) {
	cases := []struct{ in, want string }{
		{"alice@acme.com", "acme.com"},
		{"alice@ACME.com", "acme.com"},
		{"alice@sub.acme.com", "sub.acme.com"},
		{"not-an-email", ""},
		{"@acme.com", ""},
		{"alice@", ""},
	}
	for _, tc := range cases {
		if got := EmailDomain(tc.in); got != tc.want {
			t.Fatalf("EmailDomain(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestEmailMatchesDomain(t *testing.T) {
	cases := []struct {
		email, domain string
		want          bool
	}{
		{"alice@acme.com", "acme.com", true},
		{"alice@ACME.COM", "acme.com", true},
		{"alice@evil.com", "acme.com", false},
		{"alice@acme.com.evil.com", "acme.com", false},
		{"not-an-email", "acme.com", false},
		{"alice@acme.com", "", false},
	}
	for _, tc := range cases {
		if got := EmailMatchesDomain(tc.email, tc.domain); got != tc.want {
			t.Fatalf("EmailMatchesDomain(%q,%q) = %v, want %v", tc.email, tc.domain, got, tc.want)
		}
	}
}

func TestClampReturnPath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/portal", "/portal"},
		{"/admin/users?x=1", "/admin/users?x=1"},
		{"", "/portal"},
		{"  ", "/portal"},
		{"https://evil.com", "/portal"},
		{"//evil.com", "/portal"},
		{"/\\evil", "/portal"},
		{"/path\r\nX", "/portal"},
		{"portal", "/portal"},
	}
	for _, tc := range cases {
		if got := ClampReturnPath(tc.in); got != tc.want {
			t.Fatalf("ClampReturnPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSPEntityIDAndACSURL(t *testing.T) {
	if got := SPEntityID("https://sp.example.com/"); got != "https://sp.example.com/auth/sso/saml" {
		t.Fatalf("SPEntityID = %q", got)
	}
	if got := SAMLACSURL("https://sp.example.com"); got != "https://sp.example.com/auth/sso/saml/acs" {
		t.Fatalf("SAMLACSURL = %q", got)
	}
}

// inflate decompresses the raw-DEFLATE SAMLRequest the build side
// produces, used only to assert the outbound request round-trips.
func inflate(data []byte) ([]byte, error) {
	r := flate.NewReader(bytes.NewReader(data))
	defer r.Close()
	return io.ReadAll(r)
}
