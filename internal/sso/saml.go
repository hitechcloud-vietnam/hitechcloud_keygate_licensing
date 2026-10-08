package sso

import (
	"bytes"
	"compress/flate"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
)

// ─── Enterprise SAML 2.0 login (Phase 8, slice 2) ───
//
// The service-provider half of a SAML Web Browser SSO login:
//
//   1. BuildAuthnRedirect crafts a <samlp:AuthnRequest>, DEFLATE+base64
//      encodes it and returns the 302 Location for the IdP's SSO endpoint.
//   2. ParseSAMLResponse reads the base64-decoded <samlp:Response> the IdP
//      posts back to our Assertion Consumer Service.
//   3. VerifySignature (the security seam) proves the assertion was signed
//      by the configured IdP certificate and extracts the identity FROM THE
//      SIGNED CONTENT.
//   4. ValidateSAML enforces issuer, the Conditions validity window and the
//      AudienceRestriction.
//
// Everything here is pure protocol logic: no store, no HTTP handler, no
// session. The handler (internal/handler/sso_auth.go) owns the browser
// redirects, the user provisioning and the session cookie.
//
// # SIGNATURE VERIFICATION — the one non-stdlib dependency
//
// XML-DSig is not something to hand-roll. VerifySignature uses
// github.com/russellhaering/goxmldsig (the library the reference Go SAML
// stacks build on) to validate the enveloped signature over either the
// whole <Response> (document-level) or the <Assertion> (assertion-level)
// against the PEM certificate pinned on the SSO connection.
//
// The security property that matters: identity is extracted ONLY from the
// element goxmldsig actually validated. goxmldsig's Validate returns the
// exact element its digest covered; we read NameID / email / Conditions /
// Audience out of THAT subtree and nothing else. So a forged or unsigned
// assertion injected next to a signed one is never trusted — the bytes we
// read are the bytes the IdP signed. An unsigned or tampered response is
// REJECTED (401 at the handler); it is never accepted on the "hope the
// caller checks" plan.
//
// Known limitation (documented, not hidden): goxmldsig performs exclusive
// canonicalization (exc-c14n) as the SAML profiles require, and verifies
// RSA-SHA1/256/384/512 (and ECDSA) signature methods. It does NOT do full
// XML schema validation of the assertion, and it trusts exactly the single
// certificate it is given (no chain building, no CRL/OCSP). That is the
// correct trust model for a pinned IdP signing cert.

// SAML namespaces. The semantic parser matches these by URI (prefix
// independent — an IdP may use saml:/samlp:/default, the URIs are fixed),
// while the signature verifier is namespace-strict via goxmldsig.
const (
	samlProtocolNS  = "urn:oasis:names:tc:SAML:2.0:protocol"
	samlAssertionNS = "urn:oasis:names:tc:SAML:2.0:assertion"
	// samlStatusSuccess is the only StatusCode we accept.
	samlStatusSuccess = "urn:oasis:names:tc:SAML:2.0:status:Success"
	// samlNameIDEmail is the NameID Format most IdPs use for a login name.
	samlNameIDEmail = "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress"
	// samlBindingHTTPRedirect / samlBindingHTTPPost are the two bindings
	// this SP speaks (Redirect for the request, POST for the response).
	samlBindingHTTPRedirect = "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect"
	samlBindingHTTPPost     = "urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST"
)

// ClockSkew is the tolerance applied to the Conditions NotBefore /
// NotOnOrAfter window and to OIDC exp/iat. Two minutes covers the normal
// clock drift between this server and an identity provider without
// accepting a stale assertion.
const ClockSkew = 2 * time.Minute

// SAMLAssertion is the identity and validity a SAML assertion carries. It
// is produced by VerifySignature from the SIGNED content — every field is
// one the IdP cryptographically attested.
type SAMLAssertion struct {
	// ID is the assertion's ID attribute (used for correlation checks).
	ID string
	// Issuer is the asserting party (saml:Issuer) — checked against the
	// connection's saml_entity_id.
	Issuer string
	// NameID is the <NameID> subject identifier (often the email).
	NameID string
	// Email is the asserted login email (from an email attribute, or the
	// NameID when it is shaped like an address).
	Email string
	// GivenName / FamilyName are best-effort display-name parts.
	GivenName  string
	FamilyName string
	// NotBefore / NotOnOrAfter are the Conditions validity bounds. Nil
	// when the assertion omitted that bound.
	NotBefore    *time.Time
	NotOnOrAfter *time.Time
	// Audiences lists the AudienceRestriction audience values.
	Audiences []string
}

// SAMLResponseDoc is the envelope of a SAML Response: the protocol-level
// facts (status, destination, the response's own issuer) that frame the
// assertion. Only the assertion's contents are security-critical; these
// are used for the status gate and for diagnostics.
type SAMLResponseDoc struct {
	ID           string
	Issuer       string
	Destination  string
	StatusCode   string
	InResponseTo string
}

// VerifySignatureFunc is the security seam. Given the raw (already
// base64-decoded) SAML Response XML and the IdP's PEM signing certificate,
// it proves a valid XML-DSig signature covers the assertion and returns the
// identity extracted from the verified content. A response with no valid
// signature, or one whose signature does not cover the asserted identity,
// MUST return an error — never a partially-trusted assertion.
//
// The default implementation is verifySignatureGoXMLDSig. Tests substitute
// a stub to exercise the login logic, and separately pin the real verifier
// with a generated key.
type VerifySignatureFunc func(rawXML []byte, pemCert string) (*SAMLAssertion, error)

// DefaultSignatureVerifier is the production VerifySignature. It is a var
// only so tests can observe it is wired; the handler passes an explicit
// verifier and never reads this at request time.
var DefaultSignatureVerifier VerifySignatureFunc = verifySignatureGoXMLDSig

// ─── Shared SSO helpers (used by both the SAML and OIDC flows) ───

// EmailDomain returns the lowercased domain part of an email address, or
// "" when the address is not shaped like one. It is the input to the SSO
// domain gate: a login is only honoured when this equals the connection's
// domain.
func EmailDomain(email string) string {
	e := strings.TrimSpace(email)
	at := strings.LastIndex(e, "@")
	if at < 0 || at == len(e)-1 || at == 0 {
		return ""
	}
	return strings.ToLower(e[at+1:])
}

// EmailMatchesDomain reports whether the asserted email belongs to the
// connection's email domain. The comparison is case-insensitive and the
// connection domain is expected to already be folded (model.
// NormalizeSSODomain). An email with no domain never matches — the gate
// fails closed.
func EmailMatchesDomain(email, domain string) bool {
	d := EmailDomain(email)
	if d == "" {
		return false
	}
	return d == strings.ToLower(strings.TrimSpace(domain))
}

// ClampReturnPath forces a return target to a local path so a RelayState or
// a `return` query parameter can never become an open redirect. Anything
// that is not a single-slash-prefixed local path collapses to "/portal".
// Backslashes, control characters and protocol-relative "//" are refused.
func ClampReturnPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/portal"
	}
	if !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return "/portal"
	}
	if strings.ContainsAny(p, "\\\r\n\x00") {
		return "/portal"
	}
	return p
}

// ─── SAML AuthnRequest (the outbound leg) ───

// SPEntityID derives the service-provider entity id from the install's
// BaseURL. It is the audience the assertion must name and the Issuer we
// put on our AuthnRequest. Derived (rather than a separate config value)
// so the SP id can never drift from where the server actually lives.
func SPEntityID(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/auth/sso/saml"
}

// SAMLACSURL is the Assertion Consumer Service URL — where the IdP posts
// the SAMLResponse.
func SAMLACSURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/auth/sso/saml/acs"
}

// BuildAuthnRedirect crafts an unsigned AuthnRequest and returns the 302
// Location for the IdP's SSO endpoint, along with the generated request ID.
//
// Request signing is deliberately OUT OF SCOPE: the request is sent
// unsigned. Most SAML identity providers accept unsigned AuthnRequests (the
// response is what carries the signature that matters). Signing the request
// would require an SP signing key pair, which this slice does not provision
// — documented here rather than half-wired.
//
// The SAMLRequest parameter is DEFLATE-compressed (RFC 1951 raw DEFLATE,
// per the SAML HTTP-Redirect binding) then base64-encoded and URL-escaped
// into the query. RelayState carries the (already clamped) return path.
func BuildAuthnRedirect(ssoURL, spEntityID, acsURL, relayState, returnID string, now time.Time) (string, string, error) {
	if strings.TrimSpace(ssoURL) == "" {
		return "", "", errors.New("saml: sso url is empty")
	}
	id := returnID
	if id == "" {
		id = NewSAMLID()
	}
	reqXML := fmt.Sprintf(
		`<samlp:AuthnRequest xmlns:samlp="%s" xmlns:saml="%s" ID="%s" Version="2.0" IssueInstant="%s" Destination="%s" AssertionConsumerServiceURL="%s" ProtocolBinding="%s"><saml:Issuer>%s</saml:Issuer><samlp:NameIDPolicy AllowCreate="true" Format="%s"/></samlp:AuthnRequest>`,
		samlProtocolNS, samlAssertionNS, id, FormatSAMLTime(now),
		xmlEscape(ssoURL), xmlEscape(acsURL), samlBindingHTTPPost,
		xmlEscape(spEntityID), samlNameIDEmail,
	)

	compressed, err := deflate([]byte(reqXML))
	if err != nil {
		return "", "", err
	}
	encoded := base64.StdEncoding.EncodeToString(compressed)

	u, err := url.Parse(ssoURL)
	if err != nil {
		return "", "", fmt.Errorf("saml: bad sso url: %w", err)
	}
	q := u.Query()
	q.Set("SAMLRequest", encoded)
	q.Set("RelayState", relayState)
	u.RawQuery = q.Encode()
	return u.String(), id, nil
}

// NewSAMLID mints a SAML identifier: a leading underscore plus random hex,
// matching the ID/NameID shape SAML profiles expect (NCName). It uses
// crypto/rand via the shared id helper — never a clock or counter, because
// the ID is what ties a response back to a request.
func NewSAMLID() string { return "_" + randomHex(8) }

// randomHex returns n random bytes as a lowercase hex string. It is the one
// entropy source for SAML IDs and OIDC state/nonce; a crypto/rand failure
// is an error, never a predictable fallback.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read only fails if the OS entropy source is broken;
		// rather than emit a guessable value, panic — a login flow must not
		// proceed on predictable state.
		panic("sso: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// FormatSAMLTime renders an instant in the SAML/XSD UTC form.
func FormatSAMLTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// ParseSAMLTime reads the SAML/XSD instant form (…Z or with an offset). A
// blank value yields (nil, nil) so a missing Conditions bound is
// distinguishable from a malformed one.
func ParseSAMLTime(s string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	for _, layout := range []string{
		time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			u := t.UTC()
			return &u, nil
		}
	}
	return nil, fmt.Errorf("saml: bad time %q", s)
}

// deflate compresses raw (no zlib wrapper) per the SAML redirect binding.
func deflate(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// xmlEscape escapes text for an XML text/attribute node.
func xmlEscape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return s
	}
	return b.String()
}

// ─── SAML Response parsing (the inbound leg) ───

// Semantic parse structs. Matched by LOCAL NAME (prefix/namespace
// independent — an IdP may use saml:/samlp:/default, and encoding/xml's
// single-token tag matches any namespace). These fields are the
// protocol envelope only; the security-critical assertion fields are NOT
// read here but by VerifySignature from the signed content — see the note
// at the top.
//
// The Response's direct-field Issuer resolves to the response-level
// <Issuer> (the assertion's <Issuer> is a grandchild nested inside
// <Assertion>, so it is never ambiguous here).

type xmlResponse struct {
	XMLName      xml.Name  `xml:"Response"`
	ID           string    `xml:"ID,attr"`
	Destination  string    `xml:"Destination,attr"`
	Issuer       string    `xml:"Issuer"`
	Status       xmlStatus `xml:"Status"`
	InResponseTo string    `xml:"InResponseTo,attr"`
}

type xmlStatus struct {
	StatusCode xmlStatusCode `xml:"StatusCode"`
}

type xmlStatusCode struct {
	Value string `xml:"Value,attr"`
}

// ParseSAMLResponse reads the protocol envelope of a SAML Response from its
// raw (base64-decoded) XML: the status code, destination and the response's
// own issuer. It does NOT read or trust the assertion — that is
// VerifySignature's job.
func ParseSAMLResponse(rawXML []byte) (*SAMLResponseDoc, error) {
	var resp xmlResponse
	if err := xml.Unmarshal(rawXML, &resp); err != nil {
		return nil, fmt.Errorf("saml: not a SAML Response: %w", err)
	}
	return &SAMLResponseDoc{
		ID:           resp.ID,
		Issuer:       strings.TrimSpace(resp.Issuer),
		Destination:  resp.Destination,
		StatusCode:   resp.Status.StatusCode.Value,
		InResponseTo: resp.InResponseTo,
	}, nil
}

// ValidateSAML enforces the assertion-level trust rules after the signature
// has been verified:
//
//   - the Response status must be Success;
//   - the assertion Issuer must equal the connection's saml_entity_id;
//   - the Conditions window (NotBefore ≤ now ≤ NotOnOrAfter) must hold
//     within ClockSkew — an assertion with no bounds at all is refused
//     (fail closed: an unbounded assertion is not a login);
//   - the AudienceRestriction must contain the SP entity id derived from
//     BaseURL, so an assertion minted for a different service provider is
//     rejected.
//
// now is injected for testability. expectedIssuer / expectedAudience come
// from the SSO connection and the install config.
func ValidateSAML(doc *SAMLResponseDoc, assertion *SAMLAssertion, expectedIssuer, expectedAudience string, now time.Time) error {
	if doc != nil && doc.StatusCode != "" && doc.StatusCode != samlStatusSuccess {
		return fmt.Errorf("saml: response status %q is not Success", doc.StatusCode)
	}
	if strings.TrimSpace(assertion.Issuer) != strings.TrimSpace(expectedIssuer) {
		return fmt.Errorf("saml: assertion issuer %q does not match expected %q", assertion.Issuer, expectedIssuer)
	}
	// Conditions window — fail closed when no bound is present.
	if assertion.NotBefore == nil && assertion.NotOnOrAfter == nil {
		return errors.New("saml: assertion has no Conditions validity window")
	}
	if assertion.NotBefore != nil && now.Add(ClockSkew).Before(*assertion.NotBefore) {
		return errors.New("saml: assertion not yet valid (NotBefore)")
	}
	if assertion.NotOnOrAfter != nil && !now.Add(-ClockSkew).Before(*assertion.NotOnOrAfter) {
		return errors.New("saml: assertion expired (NotOnOrAfter)")
	}
	if expectedAudience != "" {
		ok := false
		for _, aud := range assertion.Audiences {
			if strings.TrimSpace(aud) == expectedAudience {
				ok = true
				break
			}
		}
		if !ok {
			return fmt.Errorf("saml: audience %v does not contain %q", assertion.Audiences, expectedAudience)
		}
	}
	if strings.TrimSpace(assertion.Email) == "" {
		return errors.New("saml: assertion carries no email")
	}
	return nil
}

// ─── Signature verification (the goxmldsig-backed seam) ───

// verifySignatureGoXMLDSig is the production VerifySignature: it validates
// the enveloped XML-DSig signature against the pinned PEM certificate and
// returns the identity read out of the VERIFIED element.
//
// It first tries a document-level signature (covers the whole Response and
// therefore every assertion inside it); failing that, it tries each
// assertion's own signature. Only an element goxmldsig actually validated
// is read from. An unsigned or tampered document yields an error.
func verifySignatureGoXMLDSig(rawXML []byte, pemCert string) (*SAMLAssertion, error) {
	certs, err := parseCerts(pemCert)
	if err != nil {
		return nil, err
	}
	doc := etree.NewDocument()
	if err := doc.ReadFromString(string(rawXML)); err != nil {
		return nil, fmt.Errorf("saml: cannot parse XML: %w", err)
	}
	root := doc.Root()
	if root == nil {
		return nil, errors.New("saml: empty XML document")
	}

	// 1) Document-level: a signature over the <Response> covers everything
	//    beneath it, including the assertion.
	if verified, err := validateElementSignature(root, certs); err == nil {
		return extractAssertion(verified)
	}

	// 2) Assertion-level: the <Assertion> itself is signed.
	for _, a := range elementsByTag(root, "Assertion") {
		if verified, err := validateElementSignature(a, certs); err == nil {
			return extractAssertion(verified)
		}
	}

	return nil, errors.New("saml: no valid signature covering the assertion (unsigned or untrusted certificate)")
}

// validateElementSignature runs goxmldsig's enveloped-signature validation
// over el and returns the element its digest verified (which for an
// enveloped signature is el itself). The certificate set is pinned to the
// connection's PEM — nothing else can make this pass.
func validateElementSignature(el *etree.Element, certs []*x509.Certificate) (*etree.Element, error) {
	ctx := dsig.NewDefaultValidationContext(&dsig.MemoryX509CertificateStore{Roots: certs})
	ctx.IdAttribute = "ID" // SAML uses the ID attribute
	return ctx.Validate(el)
}

// parseCerts extracts the X.509 certificates from a PEM bundle. At least
// one CERTIFICATE block is required.
func parseCerts(pemText string) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	rest := []byte(pemText)
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("saml: bad certificate: %w", err)
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		return nil, errors.New("saml: no certificates in PEM")
	}
	return certs, nil
}

// extractAssertion reads the SAML identity out of a VERIFIED element. If the
// verified element is the <Response>, the identity lives in its <Assertion>
// child; if it is the <Assertion>, it is read directly. Every lookup is by
// local tag name (prefix-independent) and only within the verified subtree —
// this is what makes the extraction trustworthy.
func extractAssertion(verified *etree.Element) (*SAMLAssertion, error) {
	assertEl := verified
	if verified.Tag != "Assertion" {
		assertEl = firstByTag(verified, "Assertion")
		if assertEl == nil {
			return nil, errors.New("saml: verified response contains no assertion")
		}
	}

	a := &SAMLAssertion{ID: assertEl.SelectAttrValue("ID", "")}
	a.Issuer = strings.TrimSpace(firstTextByTag(assertEl, "Issuer"))
	a.NameID = strings.TrimSpace(firstTextByTag(assertEl, "NameID"))

	// Conditions bounds.
	if cond := firstByTag(assertEl, "Conditions"); cond != nil {
		if nb, err := ParseSAMLTime(cond.SelectAttrValue("NotBefore", "")); err == nil {
			a.NotBefore = nb
		}
		if noa, err := ParseSAMLTime(cond.SelectAttrValue("NotOnOrAfter", "")); err == nil {
			a.NotOnOrAfter = noa
		}
	}

	// AudienceRestriction audiences.
	for _, aud := range elementsByTag(assertEl, "Audience") {
		if v := strings.TrimSpace(aud.Text()); v != "" {
			a.Audiences = append(a.Audiences, v)
		}
	}

	// AttributeStatement: email + name parts.
	attrs := map[string]string{}
	for _, attr := range elementsByTag(assertEl, "Attribute") {
		name := strings.ToLower(strings.TrimSpace(attr.SelectAttrValue("Name", "")))
		val := firstTextByTag(attr, "AttributeValue")
		if name == "" || val == "" {
			continue
		}
		if _, seen := attrs[name]; !seen {
			attrs[name] = val
		}
	}
	a.Email = pickEmail(attrs, a.NameID)
	a.GivenName = pickAttr(attrs, "givenname", "firstname", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/givenname")
	a.FamilyName = pickAttr(attrs, "surname", "lastname", "familyname", "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/surname")

	return a, nil
}

// pickEmail resolves the asserted email: prefer a dedicated email attribute
// (several common spellings), then the NameID when it is shaped like an
// address. Returns "" when neither yields an address.
func pickEmail(attrs map[string]string, nameID string) string {
	if e := pickAttr(attrs,
		"email", "emailaddress", "mail", "userprincipalname", "upn",
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress",
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/upn",
		"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name",
	); e != "" {
		return strings.TrimSpace(e)
	}
	// Fall back to the NameID only if it is actually an address.
	if strings.Count(nameID, "@") == 1 && EmailDomain(nameID) != "" {
		return strings.TrimSpace(nameID)
	}
	return ""
}

// pickAttr returns the first non-empty attribute value among the given
// (lowercased) names.
func pickAttr(attrs map[string]string, names ...string) string {
	for _, n := range names {
		if v, ok := attrs[strings.ToLower(n)]; ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// firstByTag returns the first element with the given local tag at or below
// root (prefix-independent — etree.Tag is the local name).
func firstByTag(root *etree.Element, tag string) *etree.Element {
	if root.Tag == tag {
		return root
	}
	for _, child := range root.ChildElements() {
		if found := firstByTag(child, tag); found != nil {
			return found
		}
	}
	return nil
}

// elementsByTag returns every element with the given local tag at or below
// root (depth-first), prefix-independent.
func elementsByTag(root *etree.Element, tag string) []*etree.Element {
	var out []*etree.Element
	if root.Tag == tag {
		out = append(out, root)
	}
	for _, child := range root.ChildElements() {
		out = append(out, elementsByTag(child, tag)...)
	}
	return out
}

// firstTextByTag returns the trimmed text of the first matching element.
func firstTextByTag(root *etree.Element, tag string) string {
	if el := firstByTag(root, tag); el != nil {
		return strings.TrimSpace(el.Text())
	}
	return ""
}
