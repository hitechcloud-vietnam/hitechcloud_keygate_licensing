package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// privacyTestDB is the TEST_DATABASE_URL-gated store for the
// anonymization round-trip. Same contract as notificationsCenterTestDB
// — no database, no test — kept local because this file also pins
// query shapes, which needs the unexported builders (package store).
func privacyTestDB(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := New(dsn)
	if err != nil {
		t.Skipf("skipping integration test: %v", err)
	}
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations failed: %v", err)
	}
	return s
}

// TestPrivacyTableAliases pins the Bun aliases every hand-written
// qualifier in privacy.go relies on: snake_case of the STRUCT name
// (bun_alias_test.go), never the table name. In particular model.Order
// aliases to "order" — a reserved word, so the qualifier must be
// QUOTED in the SQL text.
func TestPrivacyTableAliases(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	cases := []struct {
		name  string
		model interface{}
		want  string
	}{
		{"license", (*model.License)(nil), `AS "license"`},
		{"order", (*model.Order)(nil), `AS "order"`},
		{"invoice", (*model.Invoice)(nil), `AS "invoice"`},
		{"subscription", (*model.Subscription)(nil), `AS "subscription"`},
		{"seat", (*model.Seat)(nil), `AS "seat"`},
		{"customer_api_key", (*model.CustomerAPIKey)(nil), `AS "customer_api_key"`},
		{"user_notification", (*model.UserNotification)(nil), `AS "user_notification"`},
		{"customer_webhook", (*model.CustomerWebhook)(nil), `AS "customer_webhook"`},
		// OAuthAccount folds to "o_auth_account" (capital-boundary
		// snake_case) — the one alias a reader will not guess.
		{"oauth_account", (*model.OAuthAccount)(nil), `AS "o_auth_account"`},
		{"referral_code", (*model.ReferralCode)(nil), `AS "referral_code"`},
		{"affiliate_conversion", (*model.AffiliateConversion)(nil), `AS "affiliate_conversion"`},
	}
	for _, tc := range cases {
		raw, err := db.NewSelect().Model(tc.model).AppendQuery(db.QueryGen(), nil)
		if err != nil {
			t.Fatalf("%s: build query: %v", tc.name, err)
		}
		if sqlText := string(raw); !strings.Contains(sqlText, tc.want) {
			t.Errorf("%s: SQL does not contain %s; got:\n%s", tc.name, tc.want, sqlText)
		}
	}
}

// privacySQL renders one query builder to SQL text without a database.
func privacySQL(t *testing.T, q *bun.SelectQuery) string {
	t.Helper()
	raw, err := q.AppendQuery(bun.NewDB(nil, pgdialect.New()).QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	return string(raw)
}

// TestPrivacyQueryShapes pins every export query without a database:
// scoped to the owner, newest first with the stable tiebreaker, and —
// bun v1.2.18 renders query args CLIENT-SIDE, quoted inline
// (gateway_payments_test.go) — the (column, value) pairings asserted
// as literals.
func TestPrivacyQueryShapes(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	var licenses []*model.License
	sqlText := privacySQL(t, privacyLicensesQuery(db, "u-1", "Me@Example.Test", &licenses))
	for _, want := range []string{
		`AS "license"`,
		`"license".user_id = 'u-1'`,
		`lower("license".email) = lower('Me@Example.Test')`,
		`"license".created_at DESC`,
		`"license".id DESC`,
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("licenses query missing %q; got:\n%s", want, sqlText)
		}
	}

	var seats []*model.Seat
	sqlText = privacySQL(t, privacySeatsQuery(db, "u-1", "me@example.test", &seats))
	for _, want := range []string{
		`AS "seat"`,
		`"seat".user_id = 'u-1'`,
		`lower("seat".email) = lower('me@example.test')`,
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("seats query missing %q; got:\n%s", want, sqlText)
		}
	}

	var orders []*model.Order
	sqlText = privacySQL(t, privacyOrdersQuery(db, "me@example.test", &orders))
	for _, want := range []string{
		`FROM "orders" AS "order"`,
		`lower("order".customer_email) = lower('me@example.test')`,
		`"order".created_at DESC`,
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("orders query missing %q; got:\n%s", want, sqlText)
		}
	}
	// The alias is the reserved word "order"; an unquoted or
	// table-name alias would not survive Postgres.
	if strings.Contains(sqlText, `AS "orders"`) {
		t.Errorf("orders query aliases by TABLE name; got:\n%s", sqlText)
	}

	var invoices []*model.Invoice
	sqlText = privacySQL(t, privacyInvoicesQuery(db, "me@example.test", &invoices))
	for _, want := range []string{
		`AS "invoice"`,
		`JOIN "order" ON "order".id = "invoice".order_id`,
		`lower("order".customer_email) = lower('me@example.test')`,
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("invoices query missing %q; got:\n%s", want, sqlText)
		}
	}

	var subs []*model.Subscription
	sqlText = privacySQL(t, privacySubscriptionsQuery(db, "u-1", "me@example.test", &subs))
	for _, want := range []string{
		`AS "subscription"`,
		`"subscription".user_id = 'u-1'`,
		`SELECT id FROM licenses WHERE user_id = 'u-1'`,
		`lower(email) = lower('me@example.test')`,
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("subscriptions query missing %q; got:\n%s", want, sqlText)
		}
	}

	var notes []*model.UserNotification
	sqlText = privacySQL(t, privacyNotificationsQuery(db, "u-1", &notes))
	for _, want := range []string{
		`AS "user_notification"`,
		`"user_notification".user_id = 'u-1'`,
		`"user_notification".created_at DESC`,
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("notifications query missing %q; got:\n%s", want, sqlText)
		}
	}

	var keys []*model.CustomerAPIKey
	sqlText = privacySQL(t, privacyAPIKeysQuery(db, "u-1", &keys))
	for _, want := range []string{
		`AS "customer_api_key"`,
		`"customer_api_key".user_id = 'u-1'`,
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("api keys query missing %q; got:\n%s", want, sqlText)
		}
	}

	var hooks []*model.CustomerWebhook
	sqlText = privacySQL(t, privacyWebhooksQuery(db, "u-1", &hooks))
	if !strings.Contains(sqlText, `"customer_webhook".user_id = 'u-1'`) {
		t.Errorf("webhooks query not owner-scoped; got:\n%s", sqlText)
	}

	var oauths []*model.OAuthAccount
	sqlText = privacySQL(t, privacyOAuthQuery(db, "u-1", &oauths))
	if !strings.Contains(sqlText, `"o_auth_account".user_id = 'u-1'`) {
		t.Errorf("oauth query not owner-scoped (or wrong alias — want o_auth_account); got:\n%s", sqlText)
	}

	var codes []*model.ReferralCode
	sqlText = privacySQL(t, privacyReferralCodesQuery(db, "aff-1", &codes))
	for _, want := range []string{
		`AS "referral_code"`,
		`"referral_code".affiliate_id = 'aff-1'`,
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("referral codes query missing %q; got:\n%s", want, sqlText)
		}
	}
}

// TestPrivacyConversionsQueryShapes pins both scopes of the conversion
// listing: buyer-only when the account is not an affiliate, and buyer
// OR affiliate when it is. The OR must widen to the affiliate id —
// never to everything (the classic `OR TRUE` slip).
func TestPrivacyConversionsQueryShapes(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	var convs []*model.AffiliateConversion
	sqlText := privacySQL(t, privacyConversionsQuery(db, "u-1", "", &convs))
	for _, want := range []string{
		`AS "affiliate_conversion"`,
		`"affiliate_conversion".user_id = 'u-1'`,
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("buyer-only conversions missing %q; got:\n%s", want, sqlText)
		}
	}
	if strings.Contains(sqlText, "affiliate_id =") {
		t.Errorf("buyer-only conversions filter on affiliate_id; got:\n%s", sqlText)
	}

	sqlText = privacySQL(t, privacyConversionsQuery(db, "u-1", "aff-1", &convs))
	for _, want := range []string{
		`"affiliate_conversion".user_id = 'u-1'`,
		`"affiliate_conversion".affiliate_id = 'aff-1'`,
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("affiliate conversions missing %q; got:\n%s", want, sqlText)
		}
	}
	if strings.Contains(sqlText, "OR TRUE") {
		t.Errorf("conversions query has an OR TRUE wildcard; got:\n%s", sqlText)
	}
}

// TestAnonymizedEmail pins the placeholder: unique per user, on the
// RFC 2606 .invalid TLD so it can never receive mail, and recognised
// by IsAnonymizedEmail.
func TestAnonymizedEmail(t *testing.T) {
	got := AnonymizedEmail("u-1")
	if got != "deleted-u-1@example.invalid" {
		t.Errorf("AnonymizedEmail = %q, want deleted-u-1@example.invalid", got)
	}
	if !IsAnonymizedEmail(got) {
		t.Errorf("IsAnonymizedEmail(%q) = false", got)
	}
	if IsAnonymizedEmail("someone@example.com") {
		t.Error("IsAnonymizedEmail claims a live address")
	}
}

// ── DB-backed tests: skipped without TEST_DATABASE_URL ──

// privacyUser makes a throwaway account with the given role and wipes
// it at cleanup (FK cascades take the credential rows with it).
func privacyUser(t *testing.T, s *Store, tag, role string) *model.User {
	t.Helper()
	ctx := context.Background()
	u := &model.User{
		ID:        newID(),
		Email:     "privacy-" + tag + "-" + newID() + "@example.test",
		Name:      "Privacy " + tag,
		AvatarURL: "https://example.test/avatar.png",
		Role:      role,
	}
	if _, err := s.DB.NewInsert().Model(u).Exec(ctx); err != nil {
		t.Fatalf("create user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.DB.NewDelete().Model((*model.User)(nil)).Where("id = ?", u.ID).Exec(context.Background())
	})
	return u
}

// TestAnonymizeUserRewritesAndRevokes is the full round-trip: one
// account with a credential of every kind gets anonymized; the row is
// rewritten, every credential row is gone, and a second account's rows
// are untouched.
func TestAnonymizeUserRewritesAndRevokes(t *testing.T) {
	s := privacyTestDB(t)
	ctx := context.Background()

	u := privacyUser(t, s, "victim", model.RoleAdmin)
	other := privacyUser(t, s, "bystander", model.RoleUser)

	if _, err := s.DB.NewInsert().Model(&model.OAuthAccount{
		ID: newID(), UserID: u.ID, Provider: "google", ProviderID: "g-" + u.ID,
	}).Exec(ctx); err != nil {
		t.Fatalf("seed oauth: %v", err)
	}
	if err := s.CreateRefreshToken(ctx, u.ID, "hash-"+u.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("seed refresh token: %v", err)
	}
	if err := s.CreateRefreshToken(ctx, other.ID, "hash-"+other.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("seed bystander token: %v", err)
	}
	ak := &model.CustomerAPIKey{UserID: u.ID, Name: "seed"}
	if err := s.CreateCustomerAPIKey(ctx, ak, "htc_sk_seed"+u.ID); err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	if err := s.CreateOTPCode(ctx, &model.OTPCode{Email: u.Email, CodeHash: "x", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("seed otp: %v", err)
	}
	if err := s.CreateUserNotification(ctx, mustPrivacyNotification(t, u.ID)); err != nil {
		t.Fatalf("seed notification: %v", err)
	}

	out, err := s.AnonymizeUser(ctx, u)
	if err != nil {
		t.Fatalf("anonymize: %v", err)
	}

	// The row survives with a rewritten identity.
	got, err := s.FindUserByID(ctx, u.ID)
	if err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if got.Email != AnonymizedEmail(u.ID) {
		t.Errorf("email = %q, want %q", got.Email, AnonymizedEmail(u.ID))
	}
	if got.Name != AnonymizedName || got.AvatarURL != "" {
		t.Errorf("name/avatar = %q/%q, want %q/empty", got.Name, got.AvatarURL, AnonymizedName)
	}
	// An admin is demoted immediately (SessionAuth reads the role per
	// request); the receipt says so.
	if got.Role != model.RoleUser || !out.RoleDemoted {
		t.Errorf("role = %q demoted=%v, want demoted to user", got.Role, out.RoleDemoted)
	}
	if out.Revoked.RefreshTokens != 1 || out.Revoked.APIKeys != 1 ||
		out.Revoked.OAuthAccounts != 1 || out.Revoked.OTPCodes != 1 ||
		out.Revoked.Notifications != 1 {
		t.Errorf("revocation receipt = %+v, want one of each", out.Revoked)
	}

	// Credentials of the account are gone; the bystander's are not.
	if n, _ := s.DB.NewSelect().Model((*RefreshToken)(nil)).Where("user_id = ?", u.ID).Count(ctx); n != 0 {
		t.Errorf("%d refresh tokens survived anonymization", n)
	}
	if n, _ := s.DB.NewSelect().Model((*model.CustomerAPIKey)(nil)).Where("user_id = ?", u.ID).Count(ctx); n != 0 {
		t.Errorf("%d api keys survived anonymization", n)
	}
	if n, _ := s.DB.NewSelect().Model((*model.OAuthAccount)(nil)).Where("user_id = ?", u.ID).Count(ctx); n != 0 {
		t.Errorf("%d oauth accounts survived anonymization", n)
	}
	if n, _ := s.DB.NewSelect().Model((*model.OTPCode)(nil)).Where("email = ?", u.Email).Count(ctx); n != 0 {
		t.Errorf("%d otp codes survived anonymization", n)
	}
	if n, _ := s.DB.NewSelect().Model((*model.UserNotification)(nil)).Where("user_id = ?", u.ID).Count(ctx); n != 0 {
		t.Errorf("%d notifications survived anonymization", n)
	}
	if n, _ := s.DB.NewSelect().Model((*RefreshToken)(nil)).Where("user_id = ?", other.ID).Count(ctx); n != 1 {
		t.Errorf("bystander has %d refresh tokens, want 1 untouched", n)
	}
}

// TestAnonymizeUserKeepsLastOwner pins the guard that keeps the
// install owned: the last owner is NEVER demoted — the first-run setup
// flow re-opens when no owner exists, and "delete my account" must not
// hand the install to the next visitor. A second owner present makes
// the demotion safe and it happens.
func TestAnonymizeUserKeepsLastOwner(t *testing.T) {
	s := privacyTestDB(t)
	ctx := context.Background()

	owner := privacyUser(t, s, "sole-owner", model.RoleOwner)
	out, err := s.AnonymizeUser(ctx, owner)
	if err != nil {
		t.Fatalf("anonymize sole owner: %v", err)
	}
	if out.RoleDemoted {
		t.Error("sole owner was demoted — setup flow would re-open")
	}
	if got, _ := s.FindUserByID(ctx, owner.ID); got.Role != model.RoleOwner {
		t.Errorf("sole owner role = %q, want owner kept", got.Role)
	}

	owner2 := privacyUser(t, s, "owner-2", model.RoleOwner)
	owner3 := privacyUser(t, s, "owner-3", model.RoleOwner)
	out, err = s.AnonymizeUser(ctx, owner2)
	if err != nil {
		t.Fatalf("anonymize second owner: %v", err)
	}
	if !out.RoleDemoted {
		t.Error("owner with a remaining co-owner was not demoted")
	}
	if got, _ := s.FindUserByID(ctx, owner2.ID); got.Role != model.RoleUser {
		t.Errorf("anonymized owner role = %q, want user", got.Role)
	}
	if got, _ := s.FindUserByID(ctx, owner3.ID); got.Role != model.RoleOwner {
		t.Errorf("co-owner role = %q, want owner untouched", got.Role)
	}
}

// mustPrivacyNotification builds a valid inbox row for the given user.
func mustPrivacyNotification(t *testing.T, userID string) *model.UserNotification {
	t.Helper()
	n, err := model.NewUserNotification(userID, model.EventLicenseExpired, nil, "/portal/licenses", "")
	if err != nil {
		t.Fatalf("build notification: %v", err)
	}
	return n
}
