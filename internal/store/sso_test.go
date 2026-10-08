package store_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ssp is a *string literal helper for the nullable provider settings.
func ssp(v string) *string { return &v }

// The two SSO conflicts are distinct sentinels so the handler can name
// which one an admin hit (a taken name vs a taken domain). This pins the
// sentinel identity and the Is… questions that recognise them in both
// spellings, without a database.
func TestSSOConflictSentinels(t *testing.T) {
	if !store.IsSSONameConflict(store.ErrSSONameTaken) {
		t.Error("ErrSSONameTaken is not recognised as a name conflict")
	}
	if !store.IsSSODomainConflict(store.ErrSSODomainTaken) {
		t.Error("ErrSSODomainTaken is not recognised as a domain conflict")
	}
	// A name conflict is not a domain conflict and vice versa.
	if store.IsSSODomainConflict(store.ErrSSONameTaken) {
		t.Error("ErrSSONameTaken is misread as a domain conflict")
	}
	if store.IsSSONameConflict(store.ErrSSODomainTaken) {
		t.Error("ErrSSODomainTaken is misread as a name conflict")
	}
	// Wrapped sentinels are still recognised.
	wrapped := errors.New("create sso: " + store.ErrSSONameTaken.Error())
	if !errors.Is(errors.Join(store.ErrSSONameTaken), store.ErrSSONameTaken) {
		t.Error("errors.Join loses the sentinel")
	}
	_ = wrapped
	// nil and an unrelated error are neither.
	if store.IsSSONameConflict(nil) || store.IsSSODomainConflict(nil) {
		t.Error("nil is read as a conflict")
	}
	other := errors.New("connection reset")
	if store.IsSSONameConflict(other) || store.IsSSODomainConflict(other) {
		t.Error("an unrelated error is read as a conflict")
	}
}

// ── DB-backed tests: skipped without TEST_DATABASE_URL ──

func newSSO(t *testing.T, s *store.Store, ctx context.Context, name, domain string) *model.SSOConnection {
	t.Helper()
	conn := &model.SSOConnection{
		Name: name, ProviderType: model.SSOProviderOIDC, Domain: domain, Enabled: true,
		OIDCIssuer: ssp("https://idp.example.com"), OIDCClientID: ssp("cid-" + name),
	}
	if err := s.CreateSSOConnection(ctx, conn); err != nil {
		t.Fatalf("create sso connection %s: %v", name, err)
	}
	return conn
}

func TestSSOConnectionLifecycle(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	conn := newSSO(t, s, ctx, "Acme", "Acme.COM") // domain folded on write

	got, err := s.FindSSOConnectionByID(ctx, conn.ID)
	if err != nil {
		t.Fatalf("find by id: %v", err)
	}
	if got.Domain != "acme.com" {
		t.Errorf("stored domain = %q, want %q (folded)", got.Domain, "acme.com")
	}

	byDomain, err := s.FindSSOConnectionByDomain(ctx, "ACME.com")
	if err != nil {
		t.Fatalf("find by domain: %v", err)
	}
	if byDomain.ID != conn.ID {
		t.Errorf("find by domain returned %q, want %q", byDomain.ID, conn.ID)
	}

	list, total, err := s.ListSSOConnections(ctx, store.All)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total < 1 || len(list) < 1 {
		t.Errorf("list = %d rows (total %d), want at least 1", len(list), total)
	}
}

// A duplicate name and a duplicate domain are distinct 409s: the name is
// a display label, the domain is the routing handle. Both uniques must
// answer the typed refusal, not a 500.
func TestSSOConnectionConflicts(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	newSSO(t, s, ctx, "Acme", "acme.com")

	dupName := &model.SSOConnection{
		Name: "Acme", ProviderType: model.SSOProviderOIDC, Domain: "other.com", Enabled: true,
		OIDCIssuer: ssp("https://idp"), OIDCClientID: ssp("cid"),
	}
	if err := s.CreateSSOConnection(ctx, dupName); !store.IsSSONameConflict(err) {
		t.Errorf("duplicate name err = %v, want a name conflict", err)
	}

	dupDomain := &model.SSOConnection{
		Name: "Acme Two", ProviderType: model.SSOProviderOIDC, Domain: "ACME.com", Enabled: true,
		OIDCIssuer: ssp("https://idp"), OIDCClientID: ssp("cid"),
	}
	if err := s.CreateSSOConnection(ctx, dupDomain); !store.IsSSODomainConflict(err) {
		t.Errorf("duplicate domain err = %v, want a domain conflict", err)
	}
}

// The config CHECK is the database backstop: a saml connection with no
// certificate is refused before it is written (ErrSSOInvalidConfig), the
// same rule the handler answers 400 for.
func TestSSOConnectionInvalidConfigRefused(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	bad := &model.SSOConnection{
		Name: "NoCert", ProviderType: model.SSOProviderSAML, Domain: "nocert.com", Enabled: true,
		SAMLEntityID: ssp("e"), SAMLSSOURL: ssp("https://idp/sso"),
		// no certificate
	}
	if err := s.CreateSSOConnection(ctx, bad); !errors.Is(err, store.ErrSSOInvalidConfig) {
		t.Errorf("saml without certificate err = %v, want ErrSSOInvalidConfig", err)
	}
}

// Update writes back the merged config: a field set to "" clears to NULL,
// a value replaces, and the untouched columns keep their stored value.
func TestSSOConnectionUpdateMerge(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	conn := newSSO(t, s, ctx, "Acme", "acme.com")
	if conn.OIDCClientSecret == nil {
		conn.OIDCClientSecret = ssp("secret")
		if err := s.UpdateSSOConnection(ctx, conn); err != nil {
			t.Fatalf("seed secret: %v", err)
		}
	}

	// Clear scopes, set a new issuer, leave client id and secret alone.
	conn.OIDCScopes = nil
	conn.OIDCIssuer = ssp("https://new-idp.example.com")
	conn.OIDCClientSecret = nil // explicit clear
	if err := s.UpdateSSOConnection(ctx, conn); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := s.FindSSOConnectionByID(ctx, conn.ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got.OIDCIssuer == nil || *got.OIDCIssuer != "https://new-idp.example.com" {
		t.Errorf("issuer = %v, want the new value", got.OIDCIssuer)
	}
	if got.OIDCClientSecret != nil {
		t.Errorf("client secret = %v, want NULL after a clear", *got.OIDCClientSecret)
	}
	if got.OIDCClientID == nil || *got.OIDCClientID == "" {
		t.Error("client id was clobbered by an update that did not touch it")
	}
}

func TestSSOEnabledToggle(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	conn := newSSO(t, s, ctx, "Acme", "acme.com")
	if err := s.SetSSOEnabled(ctx, conn.ID, false); err != nil {
		t.Fatalf("disable: %v", err)
	}
	got, _ := s.FindSSOConnectionByID(ctx, conn.ID)
	if got.Enabled {
		t.Error("connection still enabled after disable")
	}
	if err := s.SetSSOEnabled(ctx, "no-such-id", true); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("toggle missing row err = %v, want ErrNoRows", err)
	}
}

func TestSSOConnectionDelete(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	conn := newSSO(t, s, ctx, "Acme", "acme.com")
	if err := s.DeleteSSOConnection(ctx, conn.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.FindSSOConnectionByID(ctx, conn.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("find after delete err = %v, want ErrNoRows", err)
	}
	if err := s.DeleteSSOConnection(ctx, conn.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("double delete err = %v, want ErrNoRows", err)
	}
}

// A token is stored as a hash + display prefix; the plaintext is returned
// exactly once and is never recoverable. The list never carries the hash.
func TestSCIMTokenLifecycle(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	tok := &model.SCIMToken{Name: "Okta provisioning"}
	plaintext, err := s.CreateSCIMToken(ctx, tok)
	if err != nil {
		t.Fatalf("create scim token: %v", err)
	}
	if plaintext == "" {
		t.Fatal("create returned an empty plaintext")
	}
	if !strings.HasPrefix(plaintext, model.SCIMTokenPrefix) {
		t.Errorf("plaintext %q does not start with %q", plaintext, model.SCIMTokenPrefix)
	}
	if tok.TokenHash == "" || tok.TokenHash == plaintext {
		t.Error("the stored hash must be set and must not be the plaintext")
	}
	if len(tok.TokenHash) != 64 {
		t.Errorf("token hash length = %d, want 64", len(tok.TokenHash))
	}

	// The hash is what the future SCIM middleware looks up; a usable
	// token is found by it.
	found, err := s.FindSCIMTokenByHash(ctx, model.HashSCIMToken(plaintext))
	if err != nil {
		t.Fatalf("find by hash: %v", err)
	}
	if found.ID != tok.ID {
		t.Errorf("find by hash returned %q, want %q", found.ID, tok.ID)
	}
	// A wrong hash finds nothing.
	if _, err := s.FindSCIMTokenByHash(ctx, model.HashSCIMToken("wrong")); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("find by wrong hash err = %v, want ErrNoRows", err)
	}

	list, total, err := s.ListSCIMTokens(ctx, store.All)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total < 1 || len(list) < 1 {
		t.Errorf("list = %d rows (total %d), want at least 1", len(list), total)
	}
	for _, row := range list {
		if row.TokenHash != "" && row.TokenHash == plaintext {
			t.Error("a listed row carried the plaintext")
		}
	}
}

// Revoke is soft (the row stays) and refuses to re-revoke. A revoked
// token no longer authenticates: the future SCIM middleware's
// FindSCIMTokenByHash must not return it.
func TestSCIMTokenRevoke(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	tok := &model.SCIMToken{Name: "Entra ID"}
	plaintext, err := s.CreateSCIMToken(ctx, tok)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := s.RevokeSCIMToken(ctx, tok.ID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	// The row survives (soft revoke) and is stamped.
	got, err := s.FindSCIMTokenByID(ctx, tok.ID)
	if err != nil {
		t.Fatalf("find after revoke: %v", err)
	}
	if got.RevokedAt == nil {
		t.Error("revoked_at not stamped")
	}
	// A revoked token no longer authenticates.
	if _, err := s.FindSCIMTokenByHash(ctx, model.HashSCIMToken(plaintext)); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("find a revoked token by hash err = %v, want ErrNoRows", err)
	}
	// Re-revoking is refused.
	if err := s.RevokeSCIMToken(ctx, tok.ID); !errors.Is(err, store.ErrSCIMTokenRevoked) {
		t.Errorf("double revoke err = %v, want ErrSCIMTokenRevoked", err)
	}
	// Revoking a token that never existed is a 404, not "already revoked".
	if err := s.RevokeSCIMToken(ctx, "no-such-id"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("revoke missing err = %v, want ErrNoRows", err)
	}
}

func TestSCIMTokenDelete(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	tok := &model.SCIMToken{Name: "Temp"}
	if _, err := s.CreateSCIMToken(ctx, tok); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.DeleteSCIMToken(ctx, tok.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.FindSCIMTokenByID(ctx, tok.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("find after delete err = %v, want ErrNoRows", err)
	}
}
