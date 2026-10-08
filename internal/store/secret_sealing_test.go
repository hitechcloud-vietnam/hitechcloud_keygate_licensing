package store_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/crypto"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// Encryption-at-rest proofs for the store's secret columns. These verify
// the trust boundary: the COLUMN holds sealed ciphertext while every read
// the signer / token exchange uses returns the plaintext, and legacy
// plaintext rows (written before encryption) keep reading back as-is.
//
// All of these need a database (same guard as every store test — see
// setupTestDB). The identity behaviour with no key is covered by a
// DB-free test at the bottom.

const sealingMasterKey = "unit-test-sealing-master-key-0123456789"

// withSecretBox configures the secret box for a test and clears it again
// on cleanup, so tests that rely on the license-key AEAD or on identity
// sealing are unaffected by whichever test ran first.
func withSecretBox(t *testing.T, master string) {
	t.Helper()
	if err := crypto.ConfigureSecretBox(master); err != nil {
		t.Fatalf("ConfigureSecretBox: %v", err)
	}
	t.Cleanup(func() { _ = crypto.ConfigureSecretBox("") })
}

// A customer webhook's signing secret must be SEALED in the column but
// returned as plaintext on every read the dispatch signer uses.
func TestCustomerWebhookSecretSealedAtRest(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	withSecretBox(t, sealingMasterKey)
	ctx := context.Background()
	user := createWebhookTestUser(t, s)
	defer deleteWebhookTestData(t, s, user)

	secret, err := model.NewCustomerWebhookSecret()
	if err != nil {
		t.Fatalf("NewCustomerWebhookSecret: %v", err)
	}
	wh := &model.CustomerWebhook{
		UserID: user,
		URL:    "https://example.com/hook",
		Events: []string{"license.created"},
		Active: true,
	}
	if err := s.CreateCustomerWebhook(ctx, wh, secret); err != nil {
		t.Fatalf("CreateCustomerWebhook: %v", err)
	}
	// The caller keeps the plaintext (shown to the customer once).
	if wh.Secret != secret {
		t.Fatalf("create must hand back the plaintext, got %q", wh.Secret)
	}

	// The COLUMN must hold sealed ciphertext, never the plaintext.
	var raw string
	if err := s.DB.NewRaw(
		"SELECT secret FROM customer_webhooks WHERE id = ?", wh.ID,
	).Scan(ctx, &raw); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if raw == secret {
		t.Fatal("customer_webhook secret stored in plaintext")
	}
	if !crypto.IsSealed(raw) {
		t.Fatalf("stored customer_webhook secret is not sealed: %q", raw)
	}

	// Every read path hands the signer the plaintext.
	got, err := s.FindCustomerWebhookByID(ctx, wh.ID)
	if err != nil {
		t.Fatalf("FindCustomerWebhookByID: %v", err)
	}
	if got.Secret != secret {
		t.Errorf("FindCustomerWebhookByID secret = %q, want plaintext %q", got.Secret, secret)
	}

	forEvent, err := s.FindCustomerWebhooksForEvent(ctx, "license.created")
	if err != nil {
		t.Fatalf("FindCustomerWebhooksForEvent: %v", err)
	}
	found := false
	for _, w := range forEvent {
		if w.ID == wh.ID {
			found = true
			if w.Secret != secret {
				t.Errorf("FindCustomerWebhooksForEvent secret = %q, want plaintext %q", w.Secret, secret)
			}
		}
	}
	if !found {
		t.Error("created webhook not returned by FindCustomerWebhooksForEvent")
	}
}

// The SSO OIDC client secret is a *string (nil = "cleared"). It must be
// sealed at rest and returned as plaintext to the token exchange.
func TestSSOClientSecretSealedAtRest(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	withSecretBox(t, sealingMasterKey)
	ctx := context.Background()

	suffix := store.NewID()
	issuer := "https://issuer.example.com"
	clientID := "client-" + suffix
	clientSecret := "sso-secret-" + suffix
	conn := &model.SSOConnection{
		Name:             "seal-test-" + suffix,
		ProviderType:     model.SSOProviderOIDC,
		Domain:           "seal-test-" + suffix + ".example.com",
		OIDCIssuer:       &issuer,
		OIDCClientID:     &clientID,
		OIDCClientSecret: &clientSecret,
	}
	if err := s.CreateSSOConnection(ctx, conn); err != nil {
		t.Fatalf("CreateSSOConnection: %v", err)
	}
	defer func() { _ = s.DeleteSSOConnection(ctx, conn.ID) }()

	// The caller keeps the plaintext pointer.
	if conn.OIDCClientSecret == nil || *conn.OIDCClientSecret != clientSecret {
		t.Fatalf("create must hand back the plaintext secret, got %v", conn.OIDCClientSecret)
	}

	// The COLUMN must hold sealed ciphertext.
	var raw sql.NullString
	if err := s.DB.NewRaw(
		"SELECT oidc_client_secret FROM sso_connections WHERE id = ?", conn.ID,
	).Scan(ctx, &raw); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if !raw.Valid {
		t.Fatal("oidc_client_secret unexpectedly NULL")
	}
	if raw.String == clientSecret {
		t.Fatal("sso oidc_client_secret stored in plaintext")
	}
	if !crypto.IsSealed(raw.String) {
		t.Fatalf("stored sso secret is not sealed: %q", raw.String)
	}

	// Read returns the plaintext.
	got, err := s.FindSSOConnectionByID(ctx, conn.ID)
	if err != nil {
		t.Fatalf("FindSSOConnectionByID: %v", err)
	}
	if got.OIDCClientSecret == nil || *got.OIDCClientSecret != clientSecret {
		t.Errorf("FindSSOConnectionByID secret = %v, want plaintext %q", got.OIDCClientSecret, clientSecret)
	}
}

// A row written before encryption existed (plaintext in the column) must
// keep reading back as that plaintext forever — the store opens legacy
// values by passing them through, never stranding existing config.
func TestLegacyPlaintextSecretStillReadable(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	// Start with NO secret box so the row is written as plaintext, exactly
	// like a row created before encryption was ever enabled.
	_ = crypto.ConfigureSecretBox("")
	ctx := context.Background()
	user := createWebhookTestUser(t, s)
	defer deleteWebhookTestData(t, s, user)

	const legacy = "legacy-plaintext-secret-value"
	wh := &model.CustomerWebhook{
		UserID: user,
		URL:    "https://legacy.example.com/h",
		Events: []string{"license.created"},
		Active: true,
	}
	if err := s.CreateCustomerWebhook(ctx, wh, legacy); err != nil {
		t.Fatalf("CreateCustomerWebhook (legacy plaintext): %v", err)
	}
	var raw string
	if err := s.DB.NewRaw(
		"SELECT secret FROM customer_webhooks WHERE id = ?", wh.ID,
	).Scan(ctx, &raw); err != nil {
		t.Fatalf("raw read: %v", err)
	}
	if raw != legacy {
		t.Fatalf("unconfigured seal should store plaintext, got %q", raw)
	}

	// Now enable the secret box. The plaintext row must still read back
	// as-is forever.
	withSecretBox(t, sealingMasterKey)
	got, err := s.FindCustomerWebhookByID(ctx, wh.ID)
	if err != nil {
		t.Fatalf("FindCustomerWebhookByID: %v", err)
	}
	if got.Secret != legacy {
		t.Errorf("legacy plaintext secret = %q, want %q", got.Secret, legacy)
	}
}

// With no secret box configured, sealing is the identity: the value is
// stored as-is and Open passes it through. This is the behaviour the rest
// of the store's tests rely on when they don't configure a key. DB-free.
func TestUnconfiguredSecretBoxIsIdentity(t *testing.T) {
	_ = crypto.ConfigureSecretBox("") // ensure cleared
	if crypto.SecretBoxConfigured() {
		t.Fatal("SecretBoxConfigured() must be false with no master key")
	}
	const v = "some-plaintext-secret"
	if got := crypto.Seal(v); got != v {
		t.Errorf("Seal(unconfigured) = %q, want identity %q", got, v)
	}
	got, err := crypto.Open(v)
	if err != nil || got != v {
		t.Errorf("Open(unconfigured) = %q, %v; want identity %q, nil", got, err, v)
	}
}
