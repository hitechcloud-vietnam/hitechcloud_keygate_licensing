package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// ─── Affiliate attribution (public) ───
//
// The public half of the affiliate program (plan §32): the referral
// link that starts attribution and the conversion helper the checkout
// flow will call to finish it.
//
//	GET  /r/:code                     attribution redirect (rate-limited by the Lead)
//	POST /api/v1/affiliates/convert   INTERNAL: record a conversion (rate-limited by the Lead)
//
// Attribution flow: a visitor opens /r/<code>; if the code is live the
// click is recorded (salted hashes only — never a raw IP or user
// agent) and a first-party cookie htc_ref=<code> is set. When the
// visitor later signs up and orders, the checkout calls
// /api/v1/affiliates/convert — which resolves the code from the
// caller's htc_ref cookie (or an explicit code field) and records the
// conversion against the affiliate. The cookie is the bridge between
// the anonymous click and the signed-up order.

const (
	// ReferralCookieName is the first-party attribution cookie set by
	// the redirect and read by the convert helper.
	ReferralCookieName = "htc_ref"

	// ReferralCookieMaxAge is the attribution window: a signup within
	// 30 days of the click carries the cookie and converts. A later
	// click on another affiliate's code overwrites it — last click
	// wins, which is the attribution rule this slice implements.
	ReferralCookieMaxAge = 30 * 24 * time.Hour
)

// affiliatePublicStore is the slice of store.Store this handler needs.
// The real constructor takes *store.Store; tests substitute a fake so
// the open-redirect refusal, the hashed-IP click recording and the
// conversion idempotency run without a database.
type affiliatePublicStore interface {
	FindReferralCodeByCode(ctx context.Context, code string) (*model.ReferralCode, error)
	FindAffiliateByID(ctx context.Context, id string) (*model.Affiliate, error)
	RecordClick(ctx context.Context, cl *model.ReferralClick) (bool, error)
	RecordConversion(ctx context.Context, conv *model.AffiliateConversion) (*model.AffiliateConversion, bool, error)
}

var _ affiliatePublicStore = (*store.Store)(nil)

type AffiliatePublicHandler struct {
	store affiliatePublicStore

	// DefaultRedirect is where a visitor goes when the code has no
	// landing_url — and where an unknown or inactive code goes. One
	// default for both keeps the endpoint from telling a prober which
	// codes exist (no oracle): the response shape is identical for a
	// live code with no landing_url and a code that is not live.
	DefaultRedirect string

	// IPSalt salts the click hashes (model.HashReferralIP). It MUST
	// come from deployment configuration once the Lead adds a config
	// field (e.g. REFERRAL_HASH_SALT) — config is shared core and this
	// slice does not touch it. Until then it defaults to empty, which
	// still never stores a raw IP (see model.HashReferralIP for what
	// the salt defends against).
	IPSalt string
}

func NewAffiliatePublicHandler(s *store.Store) *AffiliatePublicHandler {
	return &AffiliatePublicHandler{store: s, DefaultRedirect: "/"}
}

// defaultRedirect is the configured fallback, clamped to a local path:
// this endpoint's only two redirect targets are the stored landing_url
// and this default, so even a misconfigured default cannot turn it
// into an open redirector.
func (h *AffiliatePublicHandler) defaultRedirect() string {
	if h.DefaultRedirect == "" || !strings.HasPrefix(h.DefaultRedirect, "/") || strings.HasPrefix(h.DefaultRedirect, "//") {
		return "/"
	}
	return h.DefaultRedirect
}

// Redirect — GET /r/:code
//
// The attribution entry point. For a live code (active code on an
// active affiliate) it records the click — salted SHA-256 of the IP
// and user agent, never the raw values — sets the htc_ref cookie (30
// days, HttpOnly, first-party, last click wins) and 302s to the code's
// stored landing_url or, when there is none, the configured default.
//
// Open-redirect discipline: the Location header is ONLY ever the
// landing_url stored at code-creation time (validated as http(s) when
// written by the admin handler) or the configured default path. The
// request cannot supply, override or amend a target in any way.
//
// No oracle: an unknown code, an inactive code and a suspended
// affiliate's code all answer exactly like a live code with no
// landing_url — 302 to the default, no click recorded, no cookie. The
// endpoint never 404s and never reveals whether a code exists.
//
// The click is recorded best-effort: a bookkeeping failure is logged
// (response.LogInternal) and the visitor is redirected anyway. A
// redirect that 500s would cost the affiliate the visitor it earned.
func (h *AffiliatePublicHandler) Redirect(c *gin.Context) {
	target := h.defaultRedirect()

	code := model.NormalizeReferralCode(c.Param("code"))
	if !model.ValidReferralCode(code) {
		c.Redirect(http.StatusFound, target)
		return
	}
	rc, err := h.store.FindReferralCodeByCode(c, code)
	if err != nil || !rc.Active {
		c.Redirect(http.StatusFound, target)
		return
	}
	aff, err := h.store.FindAffiliateByID(c, rc.AffiliateID)
	if err != nil || aff.Status != model.AffiliateStatusActive {
		c.Redirect(http.StatusFound, target)
		return
	}

	// A live code: record the click (fraud control: the store folds
	// repeats from one ip_hash inside its dedup window into one row).
	_, err = h.store.RecordClick(c, &model.ReferralClick{
		CodeID:        rc.ID,
		ClickedAt:     time.Now(),
		IPHash:        model.HashReferralIP(h.IPSalt, c.ClientIP()),
		UserAgentHash: model.HashReferralUserAgent(h.IPSalt, c.Request.UserAgent()),
	})
	if err != nil {
		response.LogInternal(c, err)
	}

	// The attribution cookie. HttpOnly: only the server ever reads it
	// (the convert helper), so a script on the page cannot steal or
	// forge someone's attribution. Secure when the request arrived
	// over TLS (directly or through a TLS-terminating proxy, which is
	// what X-Forwarded-Proto records) — it stays off on plain-HTTP dev
	// so local work is not broken. SameSite=Lax: sent on the top-level
	// navigations a signup happens through, never on cross-site
	// subrequests.
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     ReferralCookieName,
		Value:    rc.Code,
		Path:     "/",
		MaxAge:   int(ReferralCookieMaxAge.Seconds()),
		HttpOnly: true,
		Secure:   c.Request.TLS != nil || c.Request.Header.Get("X-Forwarded-Proto") == "https",
		SameSite: http.SameSiteLaxMode,
	})

	// The only external target this endpoint ever navigates to: the
	// URL stored on the code when an admin wrote it.
	dest := target
	if rc.LandingURL != "" {
		dest = rc.LandingURL
	}
	c.Redirect(http.StatusFound, dest)
}

// Convert — POST /api/v1/affiliates/convert
//
// INTERNAL helper the checkout flow will call after a paid order to
// attribute it (the Lead has not wired it; this route is safe to leave
// mounted — it validates everything and only ever writes through the
// store's idempotent recorder).
//
// Body: { order_id, order_total_minor, user_id?, email?, code? }. The
// caller must present the htc_ref cookie OR a code field; an explicit
// code wins over the cookie (the checkout may know better than the
// ambient state). The affiliate is resolved through the code and the
// commission computed under its model (percent: round-half-up of
// total·bps/10000; fixed: the flat minor amount — see
// model.Affiliate.CommissionFor).
//
// Idempotent per order_id (fraud control): one order converts at most
// once. A repeat submit answers 200 with created=false and the row
// that is already there — never a second commission. Suspended
// affiliates and inactive codes never convert (400).
//
// email is accepted and validated but deliberately NOT stored in this
// slice (the conversions table has no email column): it is validated so
// a bad request is caught where it arrives, and the checkout's
// user_id is the identity the row keeps.
func (h *AffiliatePublicHandler) Convert(c *gin.Context) {
	var req struct {
		OrderID         string `json:"order_id" binding:"required"`
		OrderTotalMinor int64  `json:"order_total_minor"`
		UserID          string `json:"user_id"`
		Email           string `json:"email"`
		Code            string `json:"code"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "order_id is required")
		return
	}

	req.OrderID = strings.TrimSpace(req.OrderID)
	if req.OrderID == "" || len(req.OrderID) > 200 {
		response.BadRequest(c, "order_id is required and must be at most 200 characters")
		return
	}
	// The bound that keeps the percent commission math inside int64
	// (see model.MaxCommissionableOrderMinor) — refused here rather
	// than overflowing into a wrong commission down there.
	if req.OrderTotalMinor < 0 || req.OrderTotalMinor > model.MaxCommissionableOrderMinor {
		response.BadRequest(c, "order_total_minor must be between 0 and the maximum supported order total")
		return
	}
	userID := strings.TrimSpace(req.UserID)
	if len(userID) > 64 {
		response.BadRequest(c, "user_id must be at most 64 characters")
		return
	}
	if req.Email != "" {
		if err := apperr.ValidateEmail(model.NormalizeAffiliateEmail(req.Email)); err != nil {
			writeAppErr(c, err)
			return
		}
	}

	// The code: an explicit field beats the ambient cookie; the cookie
	// is what makes "signed up later" work at all.
	code := strings.TrimSpace(req.Code)
	if code == "" {
		if cookie, err := c.Cookie(ReferralCookieName); err == nil {
			code = cookie
		}
	}
	code = model.NormalizeReferralCode(code)
	if !model.ValidReferralCode(code) {
		response.BadRequest(c, "no referral code: present the "+ReferralCookieName+" cookie or a code field")
		return
	}
	rc, err := h.store.FindReferralCodeByCode(c, code)
	if err != nil || !rc.Active {
		response.BadRequest(c, "referral code is not active")
		return
	}
	aff, err := h.store.FindAffiliateByID(c, rc.AffiliateID)
	if err != nil {
		response.BadRequest(c, "referral code is not active")
		return
	}
	// Fraud control: a suspended affiliate never converts. Checked
	// here for the clean 400 and again in the store, which is the layer
	// that writes the money.
	if aff.Status != model.AffiliateStatusActive {
		response.BadRequest(c, "affiliate is not active")
		return
	}

	conv := &model.AffiliateConversion{
		AffiliateID:     aff.ID,
		CodeID:          rc.ID,
		OrderID:         req.OrderID,
		UserID:          userID,
		OrderTotalMinor: req.OrderTotalMinor,
		CommissionMinor: aff.CommissionFor(req.OrderTotalMinor),
	}
	stored, created, err := h.store.RecordConversion(c, conv)
	if err != nil {
		// The store re-checks the two guards (a status could flip
		// between the checks above and the write); a refusal there is
		// still a 400 about the request's referral state, not a 500.
		if errors.Is(err, store.ErrAffiliateNotActive) {
			response.BadRequest(c, "affiliate is not active")
			return
		}
		if errors.Is(err, store.ErrReferralCodeInactive) {
			response.BadRequest(c, "referral code is not active")
			return
		}
		response.Internal(c, err)
		return
	}
	// 200 whether this submit created the row or found the one an
	// earlier submit already recorded — the idempotent contract. The
	// created flag says which happened.
	response.OK(c, gin.H{"conversion": stored, "created": created})
}
