package handler

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// PrivacyHandler serves plan §82 — the two privacy rights the
// platform owes its users:
//
//	GET  /api/v1/portal/export               Download everything the
//	                                         platform holds about the
//	                                         caller (JSON).
//	POST /api/v1/portal/delete-account       Anonymize the caller's own
//	                                         account (confirmed).
//	GET  /api/v1/admin/users/:id/export      Same download, any user,
//	                                         admin-initiated.
//	POST /api/v1/admin/users/:id/anonymize   Same anonymization, any
//	                                         user, admin-initiated.
//
// Identity: the /portal routes run behind middleware.SessionAuth,
// which puts the session user's id in the context under "user_id"
// (portalUserID). The /admin routes sit on the standard admin chain
// (SessionOrAPIKey + RequireScope(admin) + RequirePermission) and the
// actor is adminID(c).
//
// Deletion is ANONYMIZATION, not a hard delete — see
// store.AnonymizeUser for the full doctrine. In one line: the identity
// is erased (email → deleted-<id>@example.invalid, name → "Deleted
// User", every credential revoked) while financial and legal records
// (orders, invoices, refunds, licences, audit logs) are PRESERVED for
// accounting, tax retention and chargeback evidence. The response
// says so in both `retained` and `note`, and the store code documents
// each column it touches. There is no users.deleted_at / disabled
// column yet (the SCIM code has the same note), so the placeholder
// email is the deleted marker; a future migration would replace it.
//
// Revocation is immediate for everything the server can recall:
// refresh tokens, customer API keys, OAuth links, OTP codes, the
// in-app inbox, webhook endpoints, custom-role assignments and SCIM
// links. The one thing it cannot recall is an already-issued session
// JWT — those are short-lived (24h) and stateless; that residual
// window is accepted (plan §82).
type PrivacyHandler struct {
	store privacyStore
}

// privacyStore is the seam this handler needs from the store.
// *store.Store satisfies it (compile-time assertion below); tests
// substitute a fake so the handler can be exercised without a
// database.
type privacyStore interface {
	FindUserByID(ctx context.Context, id string) (*model.User, error)
	FindAffiliateByEmail(ctx context.Context, email string) (*model.Affiliate, error)
	DecryptLicenseKey(l *model.License) string
	ListLicensesForUser(ctx context.Context, userID, email string) ([]*model.License, error)
	ListSeatsForUser(ctx context.Context, userID, email string) ([]*model.Seat, error)
	ListOrdersForUser(ctx context.Context, email string) ([]*model.Order, error)
	ListInvoicesForUser(ctx context.Context, email string) ([]*model.Invoice, error)
	ListSubscriptionsForUser(ctx context.Context, userID, email string) ([]*model.Subscription, error)
	ListAllUserNotifications(ctx context.Context, userID string) ([]*model.UserNotification, error)
	ListCustomerAPIKeysForUser(ctx context.Context, userID string) ([]*model.CustomerAPIKey, error)
	ListAllCustomerWebhooksByUser(ctx context.Context, userID string) ([]*model.CustomerWebhook, error)
	ListOAuthAccountsForUser(ctx context.Context, userID string) ([]*model.OAuthAccount, error)
	ListAllReferralCodesByAffiliate(ctx context.Context, affiliateID string) ([]*model.ReferralCode, error)
	ListAffiliateConversionsForUser(ctx context.Context, userID, affiliateID string) ([]*model.AffiliateConversion, error)
	AnonymizeUser(ctx context.Context, u *model.User) (*store.AnonymizeOutcome, error)
	Audit(ctx context.Context, log *model.AuditLog)
}

var _ privacyStore = (*store.Store)(nil)

// NewPrivacyHandler wires the handler to the store. The caller
// (main.go) passes the concrete *store.Store.
func NewPrivacyHandler(s *store.Store) *PrivacyHandler {
	return &PrivacyHandler{store: s}
}

// DeleteAccountPhrase is the exact phrase a delete-account request may
// carry instead of the account email. It is matched verbatim — no
// folding, no trimming beyond the surrounding whitespace — so the
// confirmation is a deliberate act, not a typo that happened to pass.
const DeleteAccountPhrase = "DELETE MY ACCOUNT"

// privacyConfirmMatches reports whether the typed confirmation proves
// intent: the account's own address (case-insensitive — addresses are
// compared folded everywhere else in this codebase) or the exact
// phrase. Anything else is refused with 400
// PRIVACY_CONFIRMATION_MISMATCH; the endpoint never hints which one it
// wanted, and an empty body is a plain 400 before this is consulted.
func privacyConfirmMatches(confirm, email string) bool {
	confirm = strings.TrimSpace(confirm)
	if confirm == "" {
		return false
	}
	return confirm == DeleteAccountPhrase || strings.EqualFold(confirm, email)
}

// ─── Export payload ───

// privacyLicenseExport re-attaches the license key the model hides.
// Same move as the portal licence listing: the export is the
// customer's own data and the key IS their credential — it is theirs
// to take — so it rides along explicitly instead of leaking from the
// model's JSON by accident. DecryptLicenseKey handles the encrypted
// and legacy-plaintext rows alike.
type privacyLicenseExport struct {
	*model.License
	LicenseKey string `json:"license_key"`
}

// privacyExportNotes spells out, in the download itself, what the
// file does and does not contain — a copy that silently omits the
// half the requester cares about is worse than no copy.
type privacyExportNotes struct {
	SecretMaterial string `json:"secret_material"`
	Deletion       string `json:"deletion"`
}

// privacyExport is the whole download: one JSON document per person.
// Every collection is a non-nil slice (response.Array) so the shape is
// stable — a client should never meet a null where a list is.
type privacyExport struct {
	Format               string                       `json:"format"`
	ExportedAt           time.Time                    `json:"exported_at"`
	User                 *model.User                  `json:"user"`
	OAuthAccounts        []*model.OAuthAccount        `json:"oauth_accounts"`
	APIKeys              []*model.CustomerAPIKey      `json:"api_keys"`
	CustomerWebhooks     []*model.CustomerWebhook     `json:"customer_webhooks"`
	Seats                []*model.Seat                `json:"seats"`
	Licenses             []privacyLicenseExport       `json:"licenses"`
	Orders               []*model.Order               `json:"orders"`
	Invoices             []*model.Invoice             `json:"invoices"`
	Subscriptions        []*model.Subscription        `json:"subscriptions"`
	Notifications        []*model.UserNotification    `json:"notifications"`
	Affiliate            *model.Affiliate             `json:"affiliate"`
	ReferralCodes        []*model.ReferralCode        `json:"referral_codes"`
	AffiliateConversions []*model.AffiliateConversion `json:"affiliate_conversions"`
	Notes                privacyExportNotes           `json:"notes"`
}

// privacyExportFormat is the payload's self-description so a future
// reader (human or tool) can tell which shape they are holding.
const privacyExportFormat = "keygate-privacy-export-v1"

// buildExport assembles the download for one user. Shared by the
// portal self-service route and the admin route so the two can never
// drift apart — an admin emailing a customer their data must send the
// same file the customer could have downloaded themselves.
func (h *PrivacyHandler) buildExport(ctx context.Context, u *model.User) (*privacyExport, error) {
	licenses, err := h.store.ListLicensesForUser(ctx, u.ID, u.Email)
	if err != nil {
		return nil, err
	}
	licOut := make([]privacyLicenseExport, 0, len(licenses))
	for _, l := range licenses {
		licOut = append(licOut, privacyLicenseExport{License: l, LicenseKey: h.store.DecryptLicenseKey(l)})
	}
	seats, err := h.store.ListSeatsForUser(ctx, u.ID, u.Email)
	if err != nil {
		return nil, err
	}
	orders, err := h.store.ListOrdersForUser(ctx, u.Email)
	if err != nil {
		return nil, err
	}
	invoices, err := h.store.ListInvoicesForUser(ctx, u.Email)
	if err != nil {
		return nil, err
	}
	subs, err := h.store.ListSubscriptionsForUser(ctx, u.ID, u.Email)
	if err != nil {
		return nil, err
	}
	notifications, err := h.store.ListAllUserNotifications(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	apiKeys, err := h.store.ListCustomerAPIKeysForUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	webhooks, err := h.store.ListAllCustomerWebhooksByUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}
	oauthAccounts, err := h.store.ListOAuthAccountsForUser(ctx, u.ID)
	if err != nil {
		return nil, err
	}

	// Affiliate/referral data "if any": the account may be an
	// affiliate (keyed by its contact address) and may also appear as
	// a referred buyer. A missing affiliate is the normal case, not an
	// error.
	var affiliate *model.Affiliate
	if a, err := h.store.FindAffiliateByEmail(ctx, u.Email); err == nil {
		affiliate = a
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	var referralCodes []*model.ReferralCode
	var conversions []*model.AffiliateConversion
	if affiliate != nil {
		referralCodes, err = h.store.ListAllReferralCodesByAffiliate(ctx, affiliate.ID)
		if err != nil {
			return nil, err
		}
	}
	conversions, err = h.store.ListAffiliateConversionsForUser(ctx, u.ID, affiliateIDOrEmpty(affiliate))
	if err != nil {
		return nil, err
	}

	return &privacyExport{
		Format:               privacyExportFormat,
		ExportedAt:           time.Now().UTC(),
		User:                 u,
		OAuthAccounts:        response.Array(oauthAccounts),
		APIKeys:              response.Array(apiKeys),
		CustomerWebhooks:     response.Array(webhooks),
		Seats:                response.Array(seats),
		Licenses:             licOut,
		Orders:               response.Array(orders),
		Invoices:             response.Array(invoices),
		Subscriptions:        response.Array(subs),
		Notifications:        response.Array(notifications),
		Affiliate:            affiliate,
		ReferralCodes:        response.Array(referralCodes),
		AffiliateConversions: response.Array(conversions),
		Notes: privacyExportNotes{
			SecretMaterial: "API key and webhook secrets are never included — only names, prefixes, scopes and timestamps. License keys are included: they are the account's own credentials.",
			Deletion:       "Account deletion anonymizes the profile and revokes credentials; orders, invoices, refunds, licenses and audit logs are preserved for legal and accounting reasons.",
		},
	}, nil
}

func affiliateIDOrEmpty(a *model.Affiliate) string {
	if a == nil {
		return ""
	}
	return a.ID
}

// ─── Portal: self-service ───

// Export answers GET /api/v1/portal/export: the caller's own data, as
// one JSON document (the web client wraps it in a Blob for download).
// Served with Cache-Control: no-store — a personal-data dump must not
// sit in a shared cache — and audited as privacy.export.
func (h *PrivacyHandler) Export(c *gin.Context) {
	userID := portalUserID(c)
	if userID == "" {
		response.Unauthorized(c, "unauthorized")
		return
	}
	u, err := h.store.FindUserByID(c.Request.Context(), userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.NotFound(c, "user not found")
			return
		}
		response.Internal(c, err)
		return
	}
	payload, err := h.buildExport(c.Request.Context(), u)
	if err != nil {
		response.Internal(c, err)
		return
	}
	h.store.Audit(c.Request.Context(), &model.AuditLog{
		Entity: "user", EntityID: u.ID, Action: "privacy.export",
		ActorType: "portal_user", ActorID: u.ID, IPAddress: c.ClientIP(),
	})
	c.Header("Cache-Control", "no-store")
	response.OK(c, payload)
}

// DeleteAccount answers POST /api/v1/portal/delete-account. The body
// must carry {"confirm": "<account email | \"DELETE MY ACCOUNT\">"};
// anything else is 400 PRIVACY_CONFIRMATION_MISMATCH before a single
// row changes. On success the account is anonymized (see
// store.AnonymizeUser), every credential is revoked, and the answer
// spells out both what was revoked and what was preserved and why.
// The action is audited as privacy.delete.
func (h *PrivacyHandler) DeleteAccount(c *gin.Context) {
	userID := portalUserID(c)
	if userID == "" {
		response.Unauthorized(c, "unauthorized")
		return
	}
	u, err := h.store.FindUserByID(c.Request.Context(), userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.NotFound(c, "user not found")
			return
		}
		response.Internal(c, err)
		return
	}
	var req struct {
		Confirm string `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, `body must be {"confirm": "<your account email>"}`)
		return
	}
	if !privacyConfirmMatches(req.Confirm, u.Email) {
		response.Err(c, http.StatusBadRequest, "PRIVACY_CONFIRMATION_MISMATCH",
			"confirm must be your account email")
		return
	}
	out, err := h.store.AnonymizeUser(c.Request.Context(), u)
	if err != nil {
		response.Internal(c, err)
		return
	}
	h.store.Audit(c.Request.Context(), &model.AuditLog{
		Entity: "user", EntityID: out.UserID, Action: "privacy.delete",
		ActorType: "portal_user", ActorID: out.UserID, IPAddress: c.ClientIP(),
		Changes: map[string]any{
			"anonymized_email": out.AnonymizedEmail,
			"role_demoted":     out.RoleDemoted,
			"revoked":          out.Revoked,
		},
	})
	response.OK(c, privacyAnonymizeResult("deleted", out))
}

// ─── Admin: export + anonymize on behalf of a user ───

// AdminExport answers GET /api/v1/admin/users/:id/export: byte-for-
// byte the same document the user could download themselves, for the
// user named in the path. Audited as privacy.export — an admin
// walking away with someone's data is exactly the kind of act the
// audit trail exists for.
func (h *PrivacyHandler) AdminExport(c *gin.Context) {
	target, ok := h.privacyAdminTarget(c)
	if !ok {
		return
	}
	payload, err := h.buildExport(c.Request.Context(), target)
	if err != nil {
		response.Internal(c, err)
		return
	}
	h.store.Audit(c.Request.Context(), &model.AuditLog{
		Entity: "user", EntityID: target.ID, Action: "privacy.export",
		ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
	})
	c.Header("Cache-Control", "no-store")
	response.OK(c, payload)
}

// AdminAnonymize answers POST /api/v1/admin/users/:id/anonymize: the
// same anonymization as delete-account, without the typed
// confirmation (the permission gate is the confirmation) — but fully
// audited as privacy.anonymize, with the admin as actor.
func (h *PrivacyHandler) AdminAnonymize(c *gin.Context) {
	target, ok := h.privacyAdminTarget(c)
	if !ok {
		return
	}
	out, err := h.store.AnonymizeUser(c.Request.Context(), target)
	if err != nil {
		response.Internal(c, err)
		return
	}
	h.store.Audit(c.Request.Context(), &model.AuditLog{
		Entity: "user", EntityID: out.UserID, Action: "privacy.anonymize",
		ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
		Changes: map[string]any{
			"anonymized_email": out.AnonymizedEmail,
			"role_demoted":     out.RoleDemoted,
			"revoked":          out.Revoked,
		},
	})
	response.OK(c, privacyAnonymizeResult("anonymized", out))
}

// privacyAdminTarget resolves :id to a user for the admin routes. A
// missing id is a 400 (the route cannot match without one, but the
// belt is here); an unknown user is the same 404 whether it never
// existed or the id was mistyped — the endpoint is not a row-existence
// oracle. On failure the response is already written.
func (h *PrivacyHandler) privacyAdminTarget(c *gin.Context) (*model.User, bool) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		response.BadRequest(c, "id is required in the URL path")
		return nil, false
	}
	u, err := h.store.FindUserByID(c.Request.Context(), id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.NotFound(c, "user not found")
			return nil, false
		}
		response.Internal(c, err)
		return nil, false
	}
	return u, true
}

// privacyAnonymizeResult is the one answer shape both anonymization
// routes return. `retained` and `note` are not decoration: the
// requester is owed a plain statement that financial and legal
// records survive the deletion and why — it is the honest answer to
// "did you delete everything?".
func privacyAnonymizeResult(status string, out *store.AnonymizeOutcome) gin.H {
	return gin.H{
		"status":           status,
		"user_id":          out.UserID,
		"anonymized_email": out.AnonymizedEmail,
		"role_demoted":     out.RoleDemoted,
		"revoked":          out.Revoked,
		"retained":         []string{"orders", "invoices", "refunds", "commissions", "licenses", "activations", "audit_logs"},
		"note": "The account identity has been erased and all credentials revoked. " +
			"Orders, invoices, refunds, licenses and audit logs are preserved for legal, tax and accounting reasons. " +
			"A session token already issued stays valid for at most 24 hours; refresh tokens and API keys are revoked immediately.",
	}
}
