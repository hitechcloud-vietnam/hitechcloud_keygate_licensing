package store

import (
	"context"
	"strings"

	"github.com/uptrace/bun"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
)

// ─── Privacy: data export + account anonymization (plan §82) ───
//
// Two operations, one subject: the person behind a users row.
//
//   - Export gathers everything the platform holds about one user so
//     they (or an admin acting for them) can take a copy: profile,
//     seats/invites, licenses, orders, invoices, subscriptions, inbox
//     rows, API key METADATA (the model hides key_hash and every
//     secret behind json:"-"; nothing here re-attaches them), webhook
//     metadata and affiliate/referral records.
//
//   - AnonymizeUser ERASES THE IDENTITY, NOT THE LEDGER. The user row
//     keeps its id (orders, licences and audit rows reference it) but
//     loses every piece of personal data: email is rewritten to the
//     deleted-<id>@example.invalid placeholder (RFC 2606 .invalid —
//     a domain that can never receive mail, so the account can never
//     be signed back into), name becomes "Deleted User", avatar_url is
//     cleared, and every credential and inbox row of the account is
//     deleted. FINANCIAL AND LEGAL RECORDS ARE PRESERVED ON PURPOSE:
//     orders, order items, invoices, refunds, licences and audit_logs
//     stay exactly as they are — invoices must remain reconstructable
//     for accounting and tax retention, and the audit trail is the
//     evidence of what happened. Their customer_email / email columns
//     are commercial records of a sale (snapshots taken at purchase
//     time), same doctrine as the order attribution columns.
//
// There is no users.deleted_at / disabled column (see the SCIM note:
// "the platform user row has no disabled flag"), so the placeholder
// email IS the deleted marker: deterministic, unique per user, and
// impossible to log in with. A future migration adding a proper flag
// would slot in here.
//
// Bun aliases used below follow the snake_case-of-STRUCT-name rule
// (bun_alias_test.go): "license", "order" (quoted — reserved word),
// "invoice", "subscription", "seat", "customer_api_key",
// "user_notification", "customer_webhook", "referral_code",
// "affiliate_conversion", "user" — and, counter-intuitively,
// "o_auth_account" for model.OAuthAccount (bun folds the struct name
// on every capital, so OAuthAccount → o_auth_account; pinned in
// privacy_test.go).
//
// Every list here is scoped by (user_id, email): a licence or order
// belongs to the export when it is linked by user_id OR addressed to
// the account's email, which is how the portal already defines
// ownership (see ListLicensesByEmail / ListOrdersByEmail).

// AnonymizedEmail is the placeholder address an anonymized account's
// email is rewritten to. Built from the user id so it is unique (the
// column has a UNIQUE constraint) and greppable. The .invalid TLD is
// reserved (RFC 2606) and can never receive mail: no OTP, no password
// reset, no newsletter — the account is unreachable by construction.
func AnonymizedEmail(userID string) string {
	return "deleted-" + userID + "@example.invalid"
}

// AnonymizedName is the display name every anonymized account shares.
const AnonymizedName = "Deleted User"

// privacyListOrder is the listing order every export collection uses:
// newest first with id as the stable tiebreaker.
const privacyListOrder = "created_at DESC, id DESC"

// ─── Export queries ───

// privacyLicensesQuery is the export's licence scope: rows linked by
// user_id OR addressed to the account's email. Split out so its shape
// can be pinned without a database (privacy_test.go).
func privacyLicensesQuery(db *bun.DB, userID, email string, dest *[]*model.License) *bun.SelectQuery {
	return db.NewSelect().Model(dest).
		Where(`"license".user_id = ? OR lower("license".email) = lower(?)`, userID, email).
		OrderExpr(`"license".created_at DESC, "license".id DESC`)
}

// ListLicensesForUser returns every licence the account owns or is
// named on. The key is NOT attached here — model.License hides it
// behind json:"-"; the handler re-attaches it explicitly via
// DecryptLicenseKey, exactly like the portal licence listing does.
func (s *Store) ListLicensesForUser(ctx context.Context, userID, email string) ([]*model.License, error) {
	var out []*model.License
	if err := privacyLicensesQuery(s.DB, userID, email, &out).Scan(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// privacySeatsQuery is the seat/invite scope: rows where the account
// is the linked user OR the invited address (a pending invite has
// user_id NULL and only the email).
func privacySeatsQuery(db *bun.DB, userID, email string, dest *[]*model.Seat) *bun.SelectQuery {
	return db.NewSelect().Model(dest).
		Where(`"seat".user_id = ? OR lower("seat".email) = lower(?)`, userID, email).
		OrderExpr(`"seat".created_at DESC, "seat".id DESC`)
}

// ListSeatsForUser returns the account's seats and invites across all
// licences. InviteTokenHash stays hidden (json:"-") — a hash is not
// the user's data and re-attaching invite tokens would mint live
// credentials into a download.
func (s *Store) ListSeatsForUser(ctx context.Context, userID, email string) ([]*model.Seat, error) {
	var out []*model.Seat
	if err := privacySeatsQuery(s.DB, userID, email, &out).Scan(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// privacyOrdersQuery is the order scope. Orders have no user_id —
// the ledger is keyed by the address the buyer typed at checkout — so
// the match is on customer_email, case-insensitively like everywhere
// else (see ListOrdersByEmail). Items ride along (rel:has-many).
func privacyOrdersQuery(db *bun.DB, email string, dest *[]*model.Order) *bun.SelectQuery {
	return db.NewSelect().Model(dest).
		Relation("Items").
		Where(`lower("order".customer_email) = lower(?)`, email).
		OrderExpr(`"order".created_at DESC, "order".id DESC`)
}

// ListOrdersForUser returns the account's orders with their lines.
func (s *Store) ListOrdersForUser(ctx context.Context, email string) ([]*model.Order, error) {
	var out []*model.Order
	if err := privacyOrdersQuery(s.DB, email, &out).Scan(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// privacyInvoicesQuery joins invoices to their order to scope by the
// buyer's address. `"order"` is quoted — it is a reserved word and the
// alias bun gives model.Order (snake_case of the struct name).
func privacyInvoicesQuery(db *bun.DB, email string, dest *[]*model.Invoice) *bun.SelectQuery {
	return db.NewSelect().Model(dest).
		Join(`JOIN "order" ON "order".id = "invoice".order_id`).
		Where(`lower("order".customer_email) = lower(?)`, email).
		OrderExpr(`"invoice".created_at DESC, "invoice".id DESC`)
}

// ListInvoicesForUser returns every invoice drawn on the account's
// orders.
func (s *Store) ListInvoicesForUser(ctx context.Context, email string) ([]*model.Invoice, error) {
	var out []*model.Invoice
	if err := privacyInvoicesQuery(s.DB, email, &out).Scan(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// privacySubscriptionsQuery scopes subscriptions by direct user link
// OR by the licence they hang off (which may be linked by user_id or
// by address). The inner subquery is unambiguous on its own; the outer
// columns are qualified with the subscription alias.
func privacySubscriptionsQuery(db *bun.DB, userID, email string, dest *[]*model.Subscription) *bun.SelectQuery {
	return db.NewSelect().Model(dest).
		Where(`"subscription".user_id = ? OR "subscription".license_id IN
			(SELECT id FROM licenses WHERE user_id = ? OR lower(email) = lower(?))`, userID, userID, email).
		OrderExpr(`"subscription".created_at DESC, "subscription".id DESC`)
}

// ListSubscriptionsForUser returns every subscription the account
// holds, directly or through a licence.
func (s *Store) ListSubscriptionsForUser(ctx context.Context, userID, email string) ([]*model.Subscription, error) {
	var out []*model.Subscription
	if err := privacySubscriptionsQuery(s.DB, userID, email, &out).Scan(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// privacyNotificationsQuery is the whole inbox of one user, newest
// first. Same alias and tiebreaker as the notification center's
// listing, without the unread filter or the page.
func privacyNotificationsQuery(db *bun.DB, userID string, dest *[]*model.UserNotification) *bun.SelectQuery {
	return db.NewSelect().Model(dest).
		Where(`"user_notification".user_id = ?`, userID).
		OrderExpr(`"user_notification".created_at DESC, "user_notification".id DESC`)
}

// ListAllUserNotifications returns the account's complete in-app
// inbox (the export is not paginated — a copy of "everything I have"
// that silently stops at page one is not a copy).
func (s *Store) ListAllUserNotifications(ctx context.Context, userID string) ([]*model.UserNotification, error) {
	var out []*model.UserNotification
	if err := privacyNotificationsQuery(s.DB, userID, &out).Scan(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// privacyAPIKeysQuery is the account's own customer API keys. The
// model hides KeyHash behind json:"-" — what leaves through the
// export is name, prefix, scopes and timestamps: metadata, never
// secret material.
func privacyAPIKeysQuery(db *bun.DB, userID string, dest *[]*model.CustomerAPIKey) *bun.SelectQuery {
	return db.NewSelect().Model(dest).
		Where(`"customer_api_key".user_id = ?`, userID).
		OrderExpr(`"customer_api_key".created_at DESC, "customer_api_key".id DESC`)
}

// ListCustomerAPIKeysForUser returns every API key row of the
// account, including revoked ones — the export is a record, not a
// live keyring.
func (s *Store) ListCustomerAPIKeysForUser(ctx context.Context, userID string) ([]*model.CustomerAPIKey, error) {
	var out []*model.CustomerAPIKey
	if err := privacyAPIKeysQuery(s.DB, userID, &out).Scan(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// privacyWebhooksQuery is the account's webhook endpoints. The
// signing secret is hidden by the model (json:"-"); only the display
// prefix travels.
func privacyWebhooksQuery(db *bun.DB, userID string, dest *[]*model.CustomerWebhook) *bun.SelectQuery {
	return db.NewSelect().Model(dest).
		Where(`"customer_webhook".user_id = ?`, userID).
		OrderExpr(`"customer_webhook".created_at DESC, "customer_webhook".id DESC`)
}

// ListAllCustomerWebhooksByUser returns the account's webhook
// endpoints unpaged (see ListAllUserNotifications for why).
func (s *Store) ListAllCustomerWebhooksByUser(ctx context.Context, userID string) ([]*model.CustomerWebhook, error) {
	var out []*model.CustomerWebhook
	if err := privacyWebhooksQuery(s.DB, userID, &out).Scan(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// privacyOAuthQuery is the account's linked sign-in identities.
func privacyOAuthQuery(db *bun.DB, userID string, dest *[]*model.OAuthAccount) *bun.SelectQuery {
	return db.NewSelect().Model(dest).
		Where(`"o_auth_account".user_id = ?`, userID).
		OrderExpr(`"o_auth_account".created_at DESC, "o_auth_account".id DESC`)
}

// ListOAuthAccountsForUser returns the OAuth identities linked to the
// account (provider + provider account id + the address the provider
// asserted).
func (s *Store) ListOAuthAccountsForUser(ctx context.Context, userID string) ([]*model.OAuthAccount, error) {
	var out []*model.OAuthAccount
	if err := privacyOAuthQuery(s.DB, userID, &out).Scan(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// privacyReferralCodesQuery is the affiliate's referral codes.
func privacyReferralCodesQuery(db *bun.DB, affiliateID string, dest *[]*model.ReferralCode) *bun.SelectQuery {
	return db.NewSelect().Model(dest).
		Where(`"referral_code".affiliate_id = ?`, affiliateID).
		OrderExpr(`"referral_code".created_at DESC, "referral_code".id DESC`)
}

// ListAllReferralCodesByAffiliate returns every referral code of one
// affiliate, unpaged.
func (s *Store) ListAllReferralCodesByAffiliate(ctx context.Context, affiliateID string) ([]*model.ReferralCode, error) {
	var out []*model.ReferralCode
	if err := privacyReferralCodesQuery(s.DB, affiliateID, &out).Scan(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// privacyConversionsQuery is the affiliate-side attribution rows that
// name the account: conversions through the account's own affiliate
// account, plus conversions recorded against the account as the
// referred buyer.
func privacyConversionsQuery(db *bun.DB, userID, affiliateID string, dest *[]*model.AffiliateConversion) *bun.SelectQuery {
	q := db.NewSelect().Model(dest)
	if affiliateID != "" {
		// The account as referred buyer, or the account as the
		// affiliate that earned the conversion.
		q = q.Where(`"affiliate_conversion".user_id = ? OR "affiliate_conversion".affiliate_id = ?`, userID, affiliateID)
	} else {
		q = q.Where(`"affiliate_conversion".user_id = ?`, userID)
	}
	return q.OrderExpr(`"affiliate_conversion".created_at DESC, "affiliate_conversion".id DESC`)
}

// ListAffiliateConversionsForUser returns the referral conversions
// that name the account, as buyer (user_id) or, when the account IS
// an affiliate, through its affiliate id. An empty affiliateID only
// matches the buyer side.
func (s *Store) ListAffiliateConversionsForUser(ctx context.Context, userID, affiliateID string) ([]*model.AffiliateConversion, error) {
	var out []*model.AffiliateConversion
	if err := privacyConversionsQuery(s.DB, userID, affiliateID, &out).Scan(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// ─── Anonymization ───

// PrivacyRevocations counts the credential and inbox rows an
// anonymization removed. Reported back so the caller can say exactly
// what was revoked, and so an admin-initiated run has a receipt.
type PrivacyRevocations struct {
	RefreshTokens    int64 `json:"refresh_tokens"`
	APIKeys          int64 `json:"api_keys"`
	OAuthAccounts    int64 `json:"oauth_accounts"`
	OTPCodes         int64 `json:"otp_codes"`
	Notifications    int64 `json:"notifications"`
	CustomerWebhooks int64 `json:"customer_webhooks"`
	RoleAssignments  int64 `json:"role_assignments"`
	SCIMIdentities   int64 `json:"scim_identities"`
}

// AnonymizeOutcome is what AnonymizeUser did: the placeholder address
// the identity now wears, whether the role was demoted, and the
// revocation receipt.
type AnonymizeOutcome struct {
	UserID          string             `json:"user_id"`
	AnonymizedEmail string             `json:"anonymized_email"`
	RoleDemoted     bool               `json:"role_demoted"`
	Revoked         PrivacyRevocations `json:"revoked"`
}

// AnonymizeUser erases the identity behind u in ONE transaction and
// revokes every credential the account holds. What it does, in order:
//
//  1. Rewrites the users row: email → AnonymizedEmail(u.ID), name →
//     AnonymizedName, avatar_url cleared. The id is kept — orders,
//     licences and audit rows reference it and are preserved.
//  2. Demotes role to "user" WHEN SAFE — i.e. unless that would erase
//     the install's last owner. The first-run setup flow re-opens when
//     no owner exists (setupNeeded), so demoting the last owner would
//     turn "delete my account" into "hand the install to the next
//     visitor": the role is kept for a last owner (ErrLastOwner
//     doctrine, same as DemoteOwnerAtomic — the owner rows are locked
//     first so two concurrent anonymizations cannot both keep). For
//     every other account the demotion is immediate effect: SessionAuth
//     reads the role from the database per request, so an admin being
//     anonymized loses admin on the very next call, not at JWT expiry.
//  3. Deletes every credential and inbox row of the account:
//     refresh_tokens (kills session renewal; an already-issued JWT is
//     short-lived — 24h — and cannot be recalled, which is accepted),
//     customer_api_keys, oauth_accounts (or the owner could simply
//     sign back in through their provider), otp_codes for the OLD
//     address (captured before the rewrite), user_notifications,
//     customer_webhooks (their secrets are credentials and their
//     deliveries would keep leaking account events after deletion),
//     user_custom_roles (privileges) and scim_identities (the IdP
//     link).
//
// WHAT IS PRESERVED, DELIBERATELY: orders, order items, invoices,
// refunds, commissions, licences, activations and audit_logs. They are
// legal and accounting records of money that moved — invoices must
// stay reconstructable for tax retention and chargeback evidence —
// and their email columns are purchase-time snapshots, not live PII
// references. This is the documented trade-off of plan §82.
func (s *Store) AnonymizeUser(ctx context.Context, u *model.User) (*AnonymizeOutcome, error) {
	out := &AnonymizeOutcome{
		UserID:          u.ID,
		AnonymizedEmail: AnonymizedEmail(u.ID),
	}
	oldEmail := u.Email
	err := RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		// ── 1+2: the users row ──
		newRole := model.RoleUser
		keepRole := false
		if u.Role == model.RoleOwner {
			// Lock every owner row, then recount: with the locks held,
			// "how many owners are left once this one is demoted" is a
			// fact, not a race (mirror of DemoteOwnerAtomic).
			var owners []string
			if err := tx.NewSelect().Model((*model.User)(nil)).
				Column("id").
				Where("role = ?", model.RoleOwner).
				For("UPDATE").
				Scan(ctx, &owners); err != nil {
				return err
			}
			remaining := len(owners)
			for _, id := range owners {
				if id == u.ID {
					remaining--
					break
				}
			}
			keepRole = remaining < 1
		}
		if keepRole {
			newRole = u.Role
		} else {
			out.RoleDemoted = true
		}
		if _, err := tx.NewUpdate().Model((*model.User)(nil)).
			Set("email = ?", out.AnonymizedEmail).
			Set("name = ?", AnonymizedName).
			Set("avatar_url = ''").
			Set("role = ?", newRole).
			Set("updated_at = now()").
			Where("id = ?", u.ID).
			Exec(ctx); err != nil {
			return err
		}

		// ── 3: credentials and inbox rows ──
		if n, err := privacyDelete(ctx, tx, (*RefreshToken)(nil), "user_id = ?", u.ID); err != nil {
			return err
		} else {
			out.Revoked.RefreshTokens = n
		}
		if n, err := privacyDelete(ctx, tx, (*model.CustomerAPIKey)(nil), "user_id = ?", u.ID); err != nil {
			return err
		} else {
			out.Revoked.APIKeys = n
		}
		if n, err := privacyDelete(ctx, tx, (*model.OAuthAccount)(nil), "user_id = ?", u.ID); err != nil {
			return err
		} else {
			out.Revoked.OAuthAccounts = n
		}
		// OTP codes are keyed by address, not by user id — and the
		// address is about to stop being this account's. Match the old
		// one case-insensitively; codes for OTHER addresses are other
		// people's rows and stay.
		if n, err := privacyDelete(ctx, tx, (*model.OTPCode)(nil), "lower(email) = lower(?)", oldEmail); err != nil {
			return err
		} else {
			out.Revoked.OTPCodes = n
		}
		if n, err := privacyDelete(ctx, tx, (*model.UserNotification)(nil), "user_id = ?", u.ID); err != nil {
			return err
		} else {
			out.Revoked.Notifications = n
		}
		if n, err := privacyDelete(ctx, tx, (*model.CustomerWebhook)(nil), "user_id = ?", u.ID); err != nil {
			return err
		} else {
			out.Revoked.CustomerWebhooks = n
		}
		if n, err := privacyDelete(ctx, tx, (*model.UserCustomRole)(nil), "user_id = ?", u.ID); err != nil {
			return err
		} else {
			out.Revoked.RoleAssignments = n
		}
		if n, err := privacyDelete(ctx, tx, (*SCIMIdentity)(nil), "user_id = ?", u.ID); err != nil {
			return err
		} else {
			out.Revoked.SCIMIdentities = n
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Keep the in-memory copy consistent with the row for callers that
	// read u afterwards (audit payloads).
	u.Email = out.AnonymizedEmail
	u.Name = AnonymizedName
	u.AvatarURL = ""
	if out.RoleDemoted {
		u.Role = model.RoleUser
	}
	return out, nil
}

// privacyDelete removes rows of one model and reports how many went.
// One helper so every count in the revocation receipt comes from the
// same place — a receipt whose numbers are hand-assembled is a
// receipt nobody can trust.
func privacyDelete(ctx context.Context, tx bun.Tx, tbl interface{}, where string, args ...any) (int64, error) {
	res, err := tx.NewDelete().Model(tbl).Where(where, args...).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// IsAnonymizedEmail reports whether an address is one this package
// minted. Deliberately dumb (prefix + TLD) — it answers "was this
// anonymized", not "does this row exist".
func IsAnonymizedEmail(email string) bool {
	return strings.HasPrefix(email, "deleted-") && strings.HasSuffix(email, "@example.invalid")
}
