package payment

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ─── Checkout attribution + wholesale pricing (Phase 7) ───
//
// Who brought the sale, priced and recorded at checkout time:
//
//   - ?reseller_code= names the reseller the order is attributed to.
//     Resellers have no code field, so the handle IS the reseller's
//     contact_email, folded (model.NormalizeResellerEmail) — the same
//     one unique index FindResellerByEmail matches on. Attribution
//     only: it never changes what is charged.
//
//   - ?ref= is the affiliate referral code the buyer arrived with; a
//     missing ?ref falls back to the first-party htc_ref cookie the
//     /r/<code> redirect sets (the cookie name mirrors
//     handler.ReferralCookieName). Snapshotted on the order in its
//     canonical folded form (model.NormalizeReferralCode).
//
//   - ?email= is the buyer's address. It is the WHOLESALE AUTHORITY:
//     when the buyer email matches a reseller that holds a wholesale
//     price (reseller_price_overrides) for the plan, that price is
//     what the sale charges. reseller_code is never consulted for
//     pricing — buyer email match is the only trigger — and a
//     reseller_code without a buyer-email match never discounts
//     anything.
//
// The resolved facts are stamped on the Stripe session's metadata
// (attributionMetadata) and read back by recordOrder when the payment
// settles — the same snapshot pattern the coupon/tax terms follow
// (checkout_terms.go): what is written to the ledger is what was true
// when the sale was made, never a later re-derivation from partner
// tables that may have moved.
//
// Wholesale pricing is applied like the buyer's own coupon: the sale
// is priced at the override (so coupon caps and tax compute on what
// is actually paid) and the cut from the list price rides to Stripe
// as a one-time fixed-amount discount coupon labelled "wholesale".
// The plan's own Stripe Price stays the line item, so renewals and
// plan resolution from line items keep working.
//
// Money discipline: every amount here is an int64 of minor units and
// every rate an integer number of basis points. Never a float.

// Checkout-terms metadata: the attribution facts a paid checkout
// carries into the commerce ledger. The four order-column keys mirror
// those columns one-to-one (the convention the coupon/tax keys
// follow); the wholesale pair records that the charge was priced
// under a reseller's wholesale deal and at what price. A fact that
// does not apply is omitted, exactly like the coupon/tax keys.
const (
	metaResellerID    = "reseller_id"
	metaResellerEmail = "reseller_email"
	metaReferralCode  = "referral_code"
	metaAffiliateID   = "affiliate_id"

	metaWholesaleOverride      = "wholesale_override"
	metaWholesaleOverridePrice = "wholesale_override_price_minor"
)

// wholesaleCouponName labels the one-time fixed-amount Stripe coupon
// that carries the wholesale cut on the receipt, so a partner can see
// their deal in the charge.
const wholesaleCouponName = "wholesale"

// referralCookieName is the first-party attribution cookie the
// /r/<code> redirect sets. It mirrors
// handler.ReferralCookieName (internal/handler/affiliate_public.go) —
// the two packages spell the same protocol name, and the affiliates
// migration documents the cookie too. It is repeated here rather than
// imported so this package keeps no dependency on the handler layer.
const referralCookieName = "htc_ref"

// attributionStore is the seam the attribution logic needs from the
// store: partner lookups at checkout time, and the two idempotent
// partner ledgers at fulfilment time (store.AccrueCommission and
// store.RecordConversion own their idempotency — one row per
// (reseller, order) and per order). *store.Store satisfies it; tests
// substitute a fake so this file's rules are pinned without a
// database.
type attributionStore interface {
	FindResellerByEmail(ctx context.Context, email string) (*model.Reseller, error)
	FindResellerByID(ctx context.Context, id string) (*model.Reseller, error)
	FindResellerPriceOverride(ctx context.Context, resellerID, planID string) (*model.ResellerPriceOverride, error)
	FindReferralCodeByCode(ctx context.Context, code string) (*model.ReferralCode, error)
	FindAffiliateByID(ctx context.Context, id string) (*model.Affiliate, error)
	AccrueCommission(ctx context.Context, cm *model.Commission) (*model.Commission, bool, error)
	RecordConversion(ctx context.Context, conv *model.AffiliateConversion) (*model.AffiliateConversion, bool, error)
}

var _ attributionStore = (*store.Store)(nil)

// attributionQuery is what the checkout link carried: the three
// optional inputs, already folded into their stored canonical forms
// so every lookup downstream compares like with like. An input that
// was absent (or folded to nothing — a junk cookie is not a
// referral code) is the empty string, never a sentinel.
type attributionQuery struct {
	// resellerCode is the reseller's folded contact email
	// (?reseller_code=). Attribution handle.
	resellerCode string
	// referralCode is the folded affiliate handle (?ref=, falling
	// back to the htc_ref cookie). Snapshot + conversion handle.
	referralCode string
	// buyerEmail is the folded buyer address (?email=). The wholesale
	// pricing authority.
	buyerEmail string
}

// attributionFromContext reads the attribution inputs off the
// checkout request. The ?ref= parameter wins over the htc_ref cookie
// when both are present — an explicit code on the link is a deliberate
// choice, the cookie is only the memory of where the buyer first
// arrived from. Both are folded with the referral-code fold, so a
// cookie of "Summer-Sale '26" and a link of ?ref=SUMMERSALE26 name
// the same code; a value that folds to nothing (or to something that
// is not code-shaped) is dropped rather than stamped on an order.
func attributionFromContext(c *gin.Context) attributionQuery {
	q := attributionQuery{
		resellerCode: model.NormalizeResellerEmail(c.Query("reseller_code")),
		referralCode: model.NormalizeReferralCode(c.Query("ref")),
		buyerEmail:   strings.ToLower(strings.TrimSpace(c.Query("email"))),
	}
	if q.referralCode == "" {
		if ck, err := c.Cookie(referralCookieName); err == nil {
			q.referralCode = model.NormalizeReferralCode(ck)
		}
	}
	if !model.ValidReferralCode(q.referralCode) {
		// Junk in the link or the cookie (too short, too long, or
		// nothing at all) is not attribution — silently absent.
		q.referralCode = ""
	}
	return q
}

// wholesaleDeal is a wholesale price that applies to this sale: the
// partner's override for the plan, and the cut from the list price it
// takes. Cut is strictly positive by construction — a deal that would
// not change the charge is not a deal (see wholesaleDealFor).
type wholesaleDeal struct {
	resellerID string
	// overridePriceMinor is what the partner pays per item.
	overridePriceMinor int64
	// cutMinor is catalog − override: what the one-time Stripe
	// discount coupon must take off the list price.
	cutMinor int64
}

// checkoutAttribution is everything one checkout resolved about its
// partners. The snapshot fields travel to the session metadata and
// then to the ledger; wholesale, when present, also shapes the price.
type checkoutAttribution struct {
	// resellerID/resellerEmail: the attributed reseller and its
	// contact address at sale time. Filled by the reseller_code
	// resolution (the attribution handle), or — when no code named a
	// partner — by the buyer-email match.
	resellerID    string
	resellerEmail string
	// referralCode is the canonical folded code; affiliateID the
	// affiliate that code belonged to when it resolved (an
	// unresolvable code is still snapshotted — it is what the buyer
	// arrived with — but names no affiliate).
	referralCode string
	affiliateID  string
	// wholesale is non-nil when the buyer pays a wholesale price.
	wholesale *wholesaleDeal
}

// resolveCheckoutAttribution turns the request's inputs into the
// facts to stamp and the price to charge. Policy, in full:
//
//   - reseller_code resolves a reseller → attributed (its id + the
//     row's contact email snapshot). It never prices anything.
//   - buyer email resolves a reseller → the wholesale authority: a
//     valid override for the plan becomes the sale's price. When no
//     code named a partner, the matched reseller is also the
//     attributed one (their own purchase is their order).
//   - both resolve and name DIFFERENT partners: reseller_code keeps
//     the attribution (that is its whole job — "attribution only"
//     means the code is where attribution comes from), and the buyer
//     match still governs pricing. In every sane link they are the
//     same partner.
//   - the referral code is stamped in canonical form and resolved to
//     its affiliate when it has one. Active/suspended is NOT decided
//     here — recordOrder re-checks at conversion time, because the
//     state at fulfilment is what counts.
//
// Wholesale edge cases, all silently "no deal" (the sale charges the
// list price):
//
//   - the override's currency differs from the plan's price currency:
//     there is no offline exchange rate, so a mismatched override is
//     ignored rather than mis-charged;
//   - the override is not below the list price (it can only discount,
//     never raise — an override at or above catalog charges catalog);
//   - no override row, negative override (impossible through the
//     CHECK, defended anyway), or a non-positive catalog price.
//
// Errors: a failure to READ pricing-relevant state (the buyer's
// reseller row or the override row) is returned — charging the list
// price to a partner whose deal we could not read would be a wrong
// charge, so the checkout refuses instead. Attribution-only lookups
// (the code, the referral handle) fail soft: an unattributable order
// is bookkeeping noise, not a wrong charge.
func resolveCheckoutAttribution(ctx context.Context, st attributionStore, q attributionQuery, planID, currency string, catalogMinor int64) (checkoutAttribution, error) {
	var out checkoutAttribution
	out.referralCode = q.referralCode

	if q.referralCode != "" {
		rc, err := st.FindReferralCodeByCode(ctx, q.referralCode)
		switch {
		case err == nil && rc != nil:
			out.referralCode = rc.Code // canonical stored form
			out.affiliateID = rc.AffiliateID
		case errors.Is(err, sql.ErrNoRows):
			// Unresolvable code: snapshotted, names no affiliate.
		default:
			slog.Warn("checkout attribution: referral code lookup failed",
				"ref", q.referralCode, "error", err)
		}
	}

	if q.resellerCode != "" {
		r, err := st.FindResellerByEmail(ctx, q.resellerCode)
		switch {
		case err == nil && r != nil:
			out.resellerID, out.resellerEmail = r.ID, r.ContactEmail
		case errors.Is(err, sql.ErrNoRows):
			// Not a reseller's handle: no attribution, no error.
		default:
			slog.Warn("checkout attribution: reseller code lookup failed",
				"reseller_code", q.resellerCode, "error", err)
		}
	}

	if q.buyerEmail != "" {
		w, err := st.FindResellerByEmail(ctx, q.buyerEmail)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			// Pricing authority unreadable: refuse rather than
			// silently charge list price to a partner.
			return out, err
		}
		if w != nil {
			if out.resellerID == "" {
				out.resellerID, out.resellerEmail = w.ID, w.ContactEmail
			}
			deal, derr := wholesaleDealFor(ctx, st, w.ID, planID, currency, catalogMinor)
			if derr != nil {
				return out, derr
			}
			out.wholesale = deal
		}
	}
	return out, nil
}

// wholesaleDealFor resolves one reseller's wholesale price for the
// plan and turns it into the discount the sale takes. Returns nil
// (with no error) for every "no deal" case in
// resolveCheckoutAttribution's doc block; returns the error only when
// the override row could not be read at all.
func wholesaleDealFor(ctx context.Context, st attributionStore, resellerID, planID, currency string, catalogMinor int64) (*wholesaleDeal, error) {
	if catalogMinor <= 0 || planID == "" {
		return nil, nil
	}
	ov, err := st.FindResellerPriceOverride(ctx, resellerID, planID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if ov == nil {
		return nil, nil
	}
	// The override carries its own currency and there is no offline
	// exchange rate: a mismatch is ignored, never converted.
	if strings.ToUpper(strings.TrimSpace(ov.Currency)) != strings.ToUpper(strings.TrimSpace(currency)) {
		return nil, nil
	}
	if ov.UnitAmountMinor < 0 || ov.UnitAmountMinor >= catalogMinor {
		// A wholesale price can only discount. At or above the list
		// price the public number is at least as good, and that is
		// what the sale charges.
		return nil, nil
	}
	return &wholesaleDeal{
		resellerID:         resellerID,
		overridePriceMinor: ov.UnitAmountMinor,
		cutMinor:           catalogMinor - ov.UnitAmountMinor,
	}, nil
}

// metadata stamps the resolved attribution onto the session, using
// the same key names the Order columns carry (the convention
// checkoutTermsMetadata follows). The wholesale pair is stamped only
// when a deal actually priced the sale.
func (a checkoutAttribution) metadata() map[string]string {
	md := make(map[string]string)
	if a.resellerID != "" {
		md[metaResellerID] = a.resellerID
		md[metaResellerEmail] = a.resellerEmail
	}
	if a.referralCode != "" {
		md[metaReferralCode] = a.referralCode
	}
	if a.affiliateID != "" {
		md[metaAffiliateID] = a.affiliateID
	}
	if a.wholesale != nil {
		md[metaWholesaleOverride] = "true"
		md[metaWholesaleOverridePrice] = strconv.FormatInt(a.wholesale.overridePriceMinor, 10)
	}
	return md
}

// ledgerAttribution is the stamped attribution of one session, read
// back at ledger time — the partner facts as they were when the sale
// was made. Mirrors ledgerTerms.
type ledgerAttribution struct {
	hasReseller   bool
	resellerID    string
	resellerEmail string
	referralCode  string
	affiliateID   string

	hasWholesale   bool
	wholesalePrice int64
}

// ledgerAttributionFromMetadata reads the stamped facts back. A key
// that was never stamped reads as its zero value — the same the Order
// column holds for a fact that does not apply.
func ledgerAttributionFromMetadata(md map[string]string) ledgerAttribution {
	var a ledgerAttribution
	if id := strings.TrimSpace(md[metaResellerID]); id != "" {
		a.hasReseller = true
		a.resellerID = id
		a.resellerEmail = strings.TrimSpace(md[metaResellerEmail])
	}
	a.referralCode = strings.TrimSpace(md[metaReferralCode])
	a.affiliateID = strings.TrimSpace(md[metaAffiliateID])
	if v, ok := md[metaWholesaleOverride]; ok && strings.EqualFold(strings.TrimSpace(v), "true") {
		a.hasWholesale = true
		a.wholesalePrice, _ = strconv.ParseInt(md[metaWholesaleOverridePrice], 10, 64)
	}
	return a
}

// applyTo copies the stamped facts onto the ledger row's attribution
// columns, preferring the metadata over anything re-derived — these
// are the facts as they were when the sale was made.
func (a ledgerAttribution) applyTo(o *model.Order) {
	if a.hasReseller {
		o.ResellerID = a.resellerID
		o.ResellerEmail = a.resellerEmail
	}
	if a.referralCode != "" {
		o.ReferralCode = a.referralCode
	}
	if a.affiliateID != "" {
		o.AffiliateID = a.affiliateID
	}
}

// fillFallbacks re-derives what the session did NOT stamp, so a
// ledger row is never left unattributed when the buyer is visibly a
// partner. The buyer email is the same authority it is at checkout
// time (a reseller's own purchase is that reseller's order), and the
// referral code is resolved to its affiliate when it has one. Stamped
// facts always win — this only fills what is missing — and every
// failure here is swallowed: enrichment of a ledger row must never
// hold up or fail fulfilment.
func (a *ledgerAttribution) fillFallbacks(ctx context.Context, st attributionStore, buyerEmail string) {
	if !a.hasReseller && buyerEmail != "" {
		if r, err := st.FindResellerByEmail(ctx, buyerEmail); err == nil && r != nil {
			a.hasReseller = true
			a.resellerID, a.resellerEmail = r.ID, r.ContactEmail
		}
	}
	if a.referralCode != "" && a.affiliateID == "" {
		if rc, err := st.FindReferralCodeByCode(ctx, a.referralCode); err == nil && rc != nil {
			a.referralCode = rc.Code
			a.affiliateID = rc.AffiliateID
		}
	}
}

// recordAttributionEffects runs the two partner ledgers one settled
// order feeds: the reseller commission accrual and the affiliate
// conversion. Called by recordOrder on every pass — including
// replays — because both stores are idempotent (one row per
// (reseller, order) / per order), which heals a first pass that died
// between the ledger insert and these hooks and makes a repeat a
// no-op. Best-effort: neither hook ever returns anything, so nothing
// here can fail fulfilment.
func recordAttributionEffects(ctx context.Context, st attributionStore, o *model.Order) {
	if o == nil {
		return
	}
	accrueResellerCommission(ctx, st, o)
	recordAffiliateConversion(ctx, st, o)
}

// accrueResellerCommission records what the attributed reseller
// earned on this order: basis = the order total as charged, rate =
// the reseller's contractual bps at accrual time (the commission row
// snapshots both, per the ledger's doctrine). Zero bps is meaningful
// ("no commission") and still records a row — the sale is the
// partner's and the ledger says what it earned, which is zero.
//
// Best-effort and replay-safe: the store's unique (reseller_id,
// order_id) answers a repeat with the original row (created=false),
// a missing reseller (deleted since the sale — the order's snapshot
// still names them) is skipped quietly, and any error is logged and
// swallowed. Fulfilment never waits on bookkeeping.
func accrueResellerCommission(ctx context.Context, st attributionStore, o *model.Order) {
	if o.ResellerID == "" {
		return
	}
	r, err := st.FindResellerByID(ctx, o.ResellerID)
	if err != nil || r == nil {
		// sql.ErrNoRows (reseller gone) is the common case here and is
		// not worth a warning; anything else is.
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("stripe order: commission accrual lookup failed",
				"order_id", o.ID, "reseller_id", o.ResellerID, "error", err)
		}
		return
	}
	row, created, err := st.AccrueCommission(ctx, &model.Commission{
		ResellerID: r.ID,
		OrderID:    o.ID,
		BasisMinor: o.TotalMinor,
		BPS:        r.CommissionBPS,
	})
	if err != nil {
		slog.Warn("stripe order: commission accrual failed",
			"order_id", o.ID, "reseller_id", r.ID, "error", err)
		return
	}
	if created {
		slog.Info("stripe order: reseller commission accrued",
			"order_id", o.ID, "reseller_id", r.ID,
			"basis_minor", o.TotalMinor, "bps", r.CommissionBPS,
			"amount_minor", row.AmountMinor)
	}
}

// recordAffiliateConversion records the sale the affiliate referral
// code brought, at the affiliate's own commission model
// (model.Affiliate.CommissionFor: round-half-up percent or fixed).
// Status is pending — the review queue the affiliate admin approves
// or rejects from, never auto-approved.
//
// The code is resolved at fulfilment time (not trusted from the
// stamp): a code deleted or deactivated between checkout and payment,
// or an affiliate suspended in between, silently skips the
// conversion — a suspended account never converts, which is the same
// fraud rule store.RecordConversion enforces as the last word. An
// order total outside the percent math's contract
// (model.MaxCommissionableOrderMinor) is skipped too rather than
// computed into a wrong number.
//
// Best-effort and replay-safe: the store's UNIQUE(order_id) answers a
// repeat with the original row (created=false), so a replayed
// fulfilment yields exactly one conversion. Errors are logged and
// swallowed.
func recordAffiliateConversion(ctx context.Context, st attributionStore, o *model.Order) {
	if o.ReferralCode == "" {
		return
	}
	rc, err := st.FindReferralCodeByCode(ctx, o.ReferralCode)
	if err != nil || rc == nil {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("stripe order: referral code lookup failed",
				"order_id", o.ID, "referral_code", o.ReferralCode, "error", err)
		}
		return
	}
	if !rc.Active {
		// Deactivated code: history stays, new conversions stop.
		return
	}
	aff, err := st.FindAffiliateByID(ctx, rc.AffiliateID)
	if err != nil || aff == nil {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("stripe order: affiliate lookup failed",
				"order_id", o.ID, "affiliate_id", rc.AffiliateID, "error", err)
		}
		return
	}
	if aff.Status != model.AffiliateStatusActive {
		// Suspended is the fraud kill-switch: silently no conversion.
		return
	}
	if o.TotalMinor < 0 || o.TotalMinor > model.MaxCommissionableOrderMinor {
		slog.Warn("stripe order: order total outside the commissionable range, conversion skipped",
			"order_id", o.ID, "total_minor", o.TotalMinor)
		return
	}
	conv, created, err := st.RecordConversion(ctx, &model.AffiliateConversion{
		AffiliateID:     aff.ID,
		CodeID:          rc.ID,
		OrderID:         o.ID,
		OrderTotalMinor: o.TotalMinor,
		CommissionMinor: aff.CommissionFor(o.TotalMinor),
		Status:          model.AffiliateConversionStatusPending,
	})
	if err != nil {
		slog.Warn("stripe order: affiliate conversion failed",
			"order_id", o.ID, "affiliate_id", aff.ID, "error", err)
		return
	}
	if created {
		slog.Info("stripe order: affiliate conversion recorded",
			"order_id", o.ID, "affiliate_id", aff.ID, "code_id", rc.ID,
			"order_total_minor", o.TotalMinor, "commission_minor", conv.CommissionMinor)
	}
}
