package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ── fake store ───────────────────────────────────────────────────────

type fakePrivacyStore struct {
	user    *model.User
	userErr error

	licenses      []*model.License
	seats         []*model.Seat
	orders        []*model.Order
	invoices      []*model.Invoice
	subs          []*model.Subscription
	notifications []*model.UserNotification
	apiKeys       []*model.CustomerAPIKey
	webhooks      []*model.CustomerWebhook
	oauthAccounts []*model.OAuthAccount
	affiliate     *model.Affiliate
	affiliateErr  error
	refCodes      []*model.ReferralCode
	conversions   []*model.AffiliateConversion
	exportErr     error

	outcome      *store.AnonymizeOutcome
	anonymizeErr error
	lastDeleted  *model.User

	audits []*model.AuditLog
}

func (f *fakePrivacyStore) FindUserByID(_ context.Context, id string) (*model.User, error) {
	if f.userErr != nil {
		return nil, f.userErr
	}
	if f.user == nil || f.user.ID != id {
		return nil, sql.ErrNoRows
	}
	return f.user, nil
}

func (f *fakePrivacyStore) FindAffiliateByEmail(_ context.Context, _ string) (*model.Affiliate, error) {
	if f.affiliateErr != nil {
		return nil, f.affiliateErr
	}
	if f.affiliate == nil {
		return nil, sql.ErrNoRows
	}
	return f.affiliate, nil
}

func (f *fakePrivacyStore) DecryptLicenseKey(l *model.License) string { return l.LicenseKey }

func (f *fakePrivacyStore) ListLicensesForUser(_ context.Context, _, _ string) ([]*model.License, error) {
	return f.licenses, f.exportErr
}
func (f *fakePrivacyStore) ListSeatsForUser(_ context.Context, _, _ string) ([]*model.Seat, error) {
	return f.seats, f.exportErr
}
func (f *fakePrivacyStore) ListOrdersForUser(_ context.Context, _ string) ([]*model.Order, error) {
	return f.orders, f.exportErr
}
func (f *fakePrivacyStore) ListInvoicesForUser(_ context.Context, _ string) ([]*model.Invoice, error) {
	return f.invoices, f.exportErr
}
func (f *fakePrivacyStore) ListSubscriptionsForUser(_ context.Context, _, _ string) ([]*model.Subscription, error) {
	return f.subs, f.exportErr
}
func (f *fakePrivacyStore) ListAllUserNotifications(_ context.Context, _ string) ([]*model.UserNotification, error) {
	return f.notifications, f.exportErr
}
func (f *fakePrivacyStore) ListCustomerAPIKeysForUser(_ context.Context, _ string) ([]*model.CustomerAPIKey, error) {
	return f.apiKeys, f.exportErr
}
func (f *fakePrivacyStore) ListAllCustomerWebhooksByUser(_ context.Context, _ string) ([]*model.CustomerWebhook, error) {
	return f.webhooks, f.exportErr
}
func (f *fakePrivacyStore) ListOAuthAccountsForUser(_ context.Context, _ string) ([]*model.OAuthAccount, error) {
	return f.oauthAccounts, f.exportErr
}
func (f *fakePrivacyStore) ListAllReferralCodesByAffiliate(_ context.Context, _ string) ([]*model.ReferralCode, error) {
	return f.refCodes, f.exportErr
}
func (f *fakePrivacyStore) ListAffiliateConversionsForUser(_ context.Context, _, _ string) ([]*model.AffiliateConversion, error) {
	return f.conversions, f.exportErr
}

func (f *fakePrivacyStore) AnonymizeUser(_ context.Context, u *model.User) (*store.AnonymizeOutcome, error) {
	f.lastDeleted = u
	if f.anonymizeErr != nil {
		return nil, f.anonymizeErr
	}
	if f.outcome != nil {
		return f.outcome, nil
	}
	return &store.AnonymizeOutcome{
		UserID:          u.ID,
		AnonymizedEmail: store.AnonymizedEmail(u.ID),
		RoleDemoted:     u.Role != model.RoleOwner,
		Revoked:         store.PrivacyRevocations{RefreshTokens: 2, APIKeys: 1},
	}, nil
}

func (f *fakePrivacyStore) Audit(_ context.Context, log *model.AuditLog) {
	f.audits = append(f.audits, log)
}

// ── harness ──────────────────────────────────────────────────────────

func newPrivacyHarness() (*PrivacyHandler, *fakePrivacyStore) {
	gin.SetMode(gin.TestMode)
	st := &fakePrivacyStore{user: &model.User{
		ID: "u1", Email: "me@example.test", Name: "Me", Role: model.RoleUser,
	}}
	return &PrivacyHandler{store: st}, st
}

func privacyCall(fn func(*gin.Context), method, userID, path, body string, params gin.Params) (int, string, http.Header) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	c.Request = httptest.NewRequest(method, path, reader)
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	if userID != "" {
		c.Set("user_id", userID)
		c.Set("email", "me@example.test")
	}
	fn(c)
	return w.Code, w.Body.String(), w.Header()
}

// privacySeedExport fills the fake with one row of everything, each
// carrying secret material that must NOT reach the download.
func privacySeedExport(st *fakePrivacyStore) {
	st.licenses = []*model.License{{ID: "lic1", Email: "me@example.test", LicenseKey: "KG-REAL-KEY"}}
	st.seats = []*model.Seat{{ID: "seat1", Email: "me@example.test", InviteTokenHash: "INVITE-HASH"}}
	st.orders = []*model.Order{{ID: "o1", CustomerEmail: "me@example.test", TotalMinor: 1000}}
	st.invoices = []*model.Invoice{{ID: "inv1", OrderID: "o1", TotalMinor: 1000}}
	st.subs = []*model.Subscription{{ID: "s1", UserID: "u1"}}
	st.notifications = []*model.UserNotification{{ID: "n1", UserID: "u1", Event: model.EventLicenseExpired}}
	st.apiKeys = []*model.CustomerAPIKey{{ID: "k1", UserID: "u1", Name: "CI", KeyPrefix: "htc_sk_ABCD", KeyHash: "API-KEY-HASH"}}
	st.webhooks = []*model.CustomerWebhook{{ID: "w1", UserID: "u1", URL: "https://hook.test/x", Secret: "whsec-REAL-SECRET", SecretPrefix: "whsec_"}}
	st.oauthAccounts = []*model.OAuthAccount{{ID: "oa1", UserID: "u1", Provider: "google", ProviderID: "g-1"}}
	st.affiliate = &model.Affiliate{ID: "aff1", ContactEmail: "me@example.test", Name: "Me"}
	st.refCodes = []*model.ReferralCode{{ID: "rc1", AffiliateID: "aff1", Code: "me-ref"}}
	st.conversions = []*model.AffiliateConversion{{ID: "ac1", AffiliateID: "aff1", UserID: "u1", OrderTotalMinor: 1000}}
}

func privacyErrCode(t *testing.T, body string) string {
	t.Helper()
	var parsed struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("parse error envelope: %v (%s)", err, body)
	}
	return parsed.Error.Code
}

// ── export ───────────────────────────────────────────────────────────

// The export without a session is a plain 401 — the route sits behind
// SessionAuth but the belt is here too.
func TestPrivacyExportRequiresSession(t *testing.T) {
	h, _ := newPrivacyHarness()
	code, body, _ := privacyCall(h.Export, http.MethodGet, "", "/api/v1/portal/export", "", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("export without session = %d %s, want 401", code, body)
	}
}

// The export answers the pinned document shape: every section present,
// licence keys included (the customer's own credential) but no other
// secret material anywhere in the bytes — not the API key hash, not
// the webhook secret, not the invite token hash.
func TestPrivacyExportShapeAndSecrets(t *testing.T) {
	h, st := newPrivacyHarness()
	privacySeedExport(st)

	code, body, hdr := privacyCall(h.Export, http.MethodGet, "u1", "/api/v1/portal/export", "", nil)
	if code != http.StatusOK {
		t.Fatalf("export = %d %s", code, body)
	}
	if got := hdr.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	for _, section := range []string{
		`"format":"keygate-privacy-export-v1"`,
		`"user"`, `"oauth_accounts"`, `"api_keys"`, `"customer_webhooks"`,
		`"seats"`, `"licenses"`, `"orders"`, `"invoices"`, `"subscriptions"`,
		`"notifications"`, `"affiliate"`, `"referral_codes"`,
		`"affiliate_conversions"`, `"notes"`,
	} {
		if !strings.Contains(body, section) {
			t.Errorf("export is missing %s; got:\n%s", section, body)
		}
	}
	// Their own licence key IS theirs; everything else secret is not
	// in the file at all.
	if !strings.Contains(body, "KG-REAL-KEY") {
		t.Error("export is missing the license key the portal already shows its owner")
	}
	for _, secret := range []string{"API-KEY-HASH", "whsec-REAL-SECRET", "INVITE-HASH"} {
		if strings.Contains(body, secret) {
			t.Errorf("export leaked secret material %q", secret)
		}
	}
	// The preservation note travels with the download.
	if !strings.Contains(body, "preserved for legal") {
		t.Error("export notes do not document deletion retention")
	}
	// Audited, as the portal user themselves.
	if len(st.audits) != 1 || st.audits[0].Action != "privacy.export" ||
		st.audits[0].ActorType != "portal_user" || st.audits[0].EntityID != "u1" {
		t.Errorf("audits = %+v, want one privacy.export by portal_user", st.audits)
	}
}

// An unknown session user is a 404, and a store failure is a 500 that
// does not pretend to be a download.
func TestPrivacyExportFailures(t *testing.T) {
	h, st := newPrivacyHarness()
	st.userErr = sql.ErrNoRows
	code, body, _ := privacyCall(h.Export, http.MethodGet, "gone", "/api/v1/portal/export", "", nil)
	if code != http.StatusNotFound {
		t.Errorf("export of unknown user = %d %s, want 404", code, body)
	}

	h, st = newPrivacyHarness()
	st.exportErr = errors.New("db down")
	code, body, _ = privacyCall(h.Export, http.MethodGet, "u1", "/api/v1/portal/export", "", nil)
	if code != http.StatusInternalServerError {
		t.Errorf("export with store failure = %d %s, want 500", code, body)
	}
}

// The admin export is the same document, audited with the admin as
// the actor — walking away with someone's data is exactly what the
// audit trail is for.
func TestPrivacyAdminExport(t *testing.T) {
	h, st := newPrivacyHarness()
	privacySeedExport(st)

	code, body, _ := privacyCall(h.AdminExport, http.MethodGet, "u1", "/api/v1/admin/users/u1/export", "",
		gin.Params{{Key: "id", Value: "u1"}})
	if code != http.StatusOK {
		t.Fatalf("admin export = %d %s", code, body)
	}
	if !strings.Contains(body, "keygate-privacy-export-v1") {
		t.Errorf("admin export is not the pinned document; got:\n%s", body)
	}
	if len(st.audits) != 1 || st.audits[0].Action != "privacy.export" || st.audits[0].ActorType != "admin" {
		t.Errorf("audits = %+v, want one privacy.export by admin", st.audits)
	}

	code, body, _ = privacyCall(h.AdminExport, http.MethodGet, "nope", "/api/v1/admin/users/nope/export", "",
		gin.Params{{Key: "id", Value: "nope"}})
	if code != http.StatusNotFound {
		t.Errorf("admin export of unknown user = %d %s, want 404", code, body)
	}
}

// ── delete-account ───────────────────────────────────────────────────

// The confirmation gate: a body that is not JSON, an empty confirm and
// a wrong confirm all answer 400 — the mismatch with its own code so
// the UI can put the message next to the field — and none of them
// touches a single row.
func TestPrivacyDeleteAccountConfirmation(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		wantCode int
		wantErr  string
	}{
		{"not json", "not json at all", http.StatusBadRequest, "BAD_REQUEST"},
		{"empty confirm", `{"confirm": ""}`, http.StatusBadRequest, "PRIVACY_CONFIRMATION_MISMATCH"},
		{"wrong confirm", `{"confirm": "something else"}`, http.StatusBadRequest, "PRIVACY_CONFIRMATION_MISMATCH"},
	}
	for _, tc := range cases {
		h, st := newPrivacyHarness()
		code, body, _ := privacyCall(h.DeleteAccount, http.MethodPost, "u1", "/api/v1/portal/delete-account", tc.body, nil)
		if code != tc.wantCode {
			t.Errorf("%s: status = %d %s, want %d", tc.name, code, body, tc.wantCode)
		}
		if got := privacyErrCode(t, body); got != tc.wantErr {
			t.Errorf("%s: error code = %s, want %s", tc.name, got, tc.wantErr)
		}
		if st.lastDeleted != nil {
			t.Errorf("%s: anonymization ran on a refused confirmation", tc.name)
		}
		if len(st.audits) != 0 {
			t.Errorf("%s: a refused confirmation was audited as done", tc.name)
		}
	}
}

// A matching confirmation — the account email (folded) or the exact
// phrase — anonymizes the account, audits privacy.delete and answers
// with the revocation receipt plus the retention statement.
func TestPrivacyDeleteAccountConfirmed(t *testing.T) {
	for _, confirm := range []string{"me@example.test", "ME@Example.Test", "  me@example.test  ", DeleteAccountPhrase} {
		h, st := newPrivacyHarness()
		code, body, _ := privacyCall(h.DeleteAccount, http.MethodPost, "u1", "/api/v1/portal/delete-account",
			`{"confirm": "`+confirm+`"}`, nil)
		if code != http.StatusOK {
			t.Fatalf("confirm %q: status = %d %s", confirm, code, body)
		}
		if st.lastDeleted == nil || st.lastDeleted.ID != "u1" {
			t.Fatalf("confirm %q: anonymization did not run for u1", confirm)
		}
		if !strings.Contains(body, `"status":"deleted"`) ||
			!strings.Contains(body, `"anonymized_email":"deleted-u1@example.invalid"`) ||
			!strings.Contains(body, `"revoked"`) ||
			!strings.Contains(body, "preserved for legal") {
			t.Errorf("confirm %q: answer is missing the receipt/retention statement; got:\n%s", confirm, body)
		}
		if len(st.audits) != 1 || st.audits[0].Action != "privacy.delete" ||
			st.audits[0].ActorType != "portal_user" || st.audits[0].ActorID != "u1" {
			t.Errorf("confirm %q: audits = %+v, want one privacy.delete by the user", confirm, st.audits)
		}
	}

	// The phrase must match VERBATIM — a lower-case spelling of it is
	// not the deliberate act the gate asks for.
	h, st := newPrivacyHarness()
	code, _, _ := privacyCall(h.DeleteAccount, http.MethodPost, "u1", "/api/v1/portal/delete-account",
		`{"confirm": "delete my account"}`, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("lower-case phrase accepted: %d", code)
	}
	if st.lastDeleted != nil {
		t.Error("lower-case phrase anonymized the account")
	}
}

// The session user is who gets deleted — never a param, never the body.
func TestPrivacyDeleteAccountTargetsSelf(t *testing.T) {
	h, st := newPrivacyHarness()
	privacyCall(h.DeleteAccount, http.MethodPost, "u1", "/api/v1/portal/delete-account",
		`{"confirm": "me@example.test"}`, nil)
	if st.lastDeleted == nil || st.lastDeleted.ID != "u1" {
		t.Fatalf("deleted %v, want u1", st.lastDeleted)
	}
}

// ── admin anonymize ──────────────────────────────────────────────────

// The admin variant runs the same anonymization without the typed
// confirmation (the permission gate is the confirmation) and is
// audited as privacy.anonymize with the admin as actor.
func TestPrivacyAdminAnonymize(t *testing.T) {
	h, st := newPrivacyHarness()
	code, body, _ := privacyCall(h.AdminAnonymize, http.MethodPost, "admin1", "/api/v1/admin/users/u1/anonymize", "",
		gin.Params{{Key: "id", Value: "u1"}})
	if code != http.StatusOK {
		t.Fatalf("admin anonymize = %d %s", code, body)
	}
	if !strings.Contains(body, `"status":"anonymized"`) ||
		!strings.Contains(body, `"anonymized_email":"deleted-u1@example.invalid"`) {
		t.Errorf("answer is missing the receipt; got:\n%s", body)
	}
	if !strings.Contains(body, `"retained"`) || !strings.Contains(body, "preserved for legal") {
		t.Errorf("answer does not document preserved financial records; got:\n%s", body)
	}
	if len(st.audits) != 1 || st.audits[0].Action != "privacy.anonymize" ||
		st.audits[0].ActorType != "admin" || st.audits[0].ActorID != "admin1" {
		t.Errorf("audits = %+v, want one privacy.anonymize by admin1", st.audits)
	}

	code, body, _ = privacyCall(h.AdminAnonymize, http.MethodPost, "admin1", "/api/v1/admin/users/nope/anonymize", "",
		gin.Params{{Key: "id", Value: "nope"}})
	if code != http.StatusNotFound {
		t.Errorf("anonymize unknown user = %d %s, want 404", code, body)
	}
}

// The confirmation helper itself, pinned: folded email match, exact
// phrase, nothing else.
func TestPrivacyConfirmMatches(t *testing.T) {
	cases := []struct {
		confirm, email string
		want           bool
	}{
		{"me@example.test", "me@example.test", true},
		{"ME@Example.Test", "me@example.test", true},
		{" me@example.test ", "me@example.test", true},
		{DeleteAccountPhrase, "me@example.test", true},
		{"delete my account", "me@example.test", false},
		{"", "me@example.test", false},
		{"   ", "me@example.test", false},
		{"other@example.test", "me@example.test", false},
	}
	for _, tc := range cases {
		if got := privacyConfirmMatches(tc.confirm, tc.email); got != tc.want {
			t.Errorf("privacyConfirmMatches(%q, %q) = %v, want %v", tc.confirm, tc.email, got, tc.want)
		}
	}
}
