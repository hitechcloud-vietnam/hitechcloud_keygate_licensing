package payment

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// These tests pin the checkout-attribution rules with no database and
// no Stripe: the seam (attributionStore) is faked with the same
// semantics the real store guarantees — misses are sql.ErrNoRows, the
// two partner ledgers are idempotent per (reseller, order) / per
// order, and RecordConversion re-checks the active guards. The
// end-to-end path through CheckoutByPlan and recordOrder is covered
// by the DB-backed tests in attribution_checkout_test.go.

// fakeAttrStore is the attribution seam backed by maps.
type fakeAttrStore struct {
	resellers  map[string]*model.Reseller              // by folded contact email
	byID       map[string]*model.Reseller              // by id
	overrides  map[string]*model.ResellerPriceOverride // "resellerID/planID"
	codes      map[string]*model.ReferralCode          // by folded code
	codesByID  map[string]*model.ReferralCode
	affiliates map[string]*model.Affiliate // by id

	commissions map[string]*model.Commission          // "resellerID/orderID"
	conversions map[string]*model.AffiliateConversion // by order id

	accrueCalls  []*model.Commission
	convertCalls []*model.AffiliateConversion

	accrueErr      error
	convertErr     error
	emailLookupErr error // injected failure of FindResellerByEmail
	overrideErr    error // injected failure of FindResellerPriceOverride
}

func newFakeAttrStore() *fakeAttrStore {
	return &fakeAttrStore{
		resellers:   map[string]*model.Reseller{},
		byID:        map[string]*model.Reseller{},
		overrides:   map[string]*model.ResellerPriceOverride{},
		codes:       map[string]*model.ReferralCode{},
		codesByID:   map[string]*model.ReferralCode{},
		affiliates:  map[string]*model.Affiliate{},
		commissions: map[string]*model.Commission{},
		conversions: map[string]*model.AffiliateConversion{},
	}
}

func (f *fakeAttrStore) addReseller(r *model.Reseller) {
	f.resellers[model.NormalizeResellerEmail(r.ContactEmail)] = r
	f.byID[r.ID] = r
}

func (f *fakeAttrStore) addCode(rc *model.ReferralCode) {
	f.codes[model.NormalizeReferralCode(rc.Code)] = rc
	f.codesByID[rc.ID] = rc
}

func (f *fakeAttrStore) FindResellerByEmail(_ context.Context, email string) (*model.Reseller, error) {
	if f.emailLookupErr != nil {
		return nil, f.emailLookupErr
	}
	if r, ok := f.resellers[model.NormalizeResellerEmail(email)]; ok {
		return r, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeAttrStore) FindResellerByID(_ context.Context, id string) (*model.Reseller, error) {
	if r, ok := f.byID[id]; ok {
		return r, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeAttrStore) FindResellerPriceOverride(_ context.Context, resellerID, planID string) (*model.ResellerPriceOverride, error) {
	if f.overrideErr != nil {
		return nil, f.overrideErr
	}
	if o, ok := f.overrides[resellerID+"/"+planID]; ok {
		return o, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeAttrStore) FindReferralCodeByCode(_ context.Context, code string) (*model.ReferralCode, error) {
	if rc, ok := f.codes[model.NormalizeReferralCode(code)]; ok {
		return rc, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeAttrStore) FindAffiliateByID(_ context.Context, id string) (*model.Affiliate, error) {
	if a, ok := f.affiliates[id]; ok {
		return a, nil
	}
	return nil, sql.ErrNoRows
}

// AccrueCommission mirrors store.AccrueCommission: the store computes
// the amount, forces accrued, and answers a replay with the ORIGINAL
// row (created=false).
func (f *fakeAttrStore) AccrueCommission(_ context.Context, cm *model.Commission) (*model.Commission, bool, error) {
	f.accrueCalls = append(f.accrueCalls, cm)
	if f.accrueErr != nil {
		return nil, false, f.accrueErr
	}
	if _, ok := f.byID[cm.ResellerID]; !ok {
		return nil, false, sql.ErrNoRows
	}
	key := cm.ResellerID + "/" + cm.OrderID
	if row, ok := f.commissions[key]; ok {
		return row, false, nil
	}
	row := *cm
	row.ID = "cm_" + key
	row.AmountMinor = model.CommissionAmount(cm.BasisMinor, cm.BPS)
	row.Status = model.CommissionStatusAccrued
	row.PaidAt = nil
	f.commissions[key] = &row
	return &row, true, nil
}

// RecordConversion mirrors store.RecordConversion: the active guards
// are re-checked and one order converts at most once.
func (f *fakeAttrStore) RecordConversion(_ context.Context, conv *model.AffiliateConversion) (*model.AffiliateConversion, bool, error) {
	f.convertCalls = append(f.convertCalls, conv)
	if f.convertErr != nil {
		return nil, false, f.convertErr
	}
	aff, ok := f.affiliates[conv.AffiliateID]
	if !ok {
		return nil, false, sql.ErrNoRows
	}
	if aff.Status != model.AffiliateStatusActive {
		return nil, false, store.ErrAffiliateNotActive
	}
	rc, ok := f.codesByID[conv.CodeID]
	if !ok {
		return nil, false, sql.ErrNoRows
	}
	if !rc.Active {
		return nil, false, store.ErrReferralCodeInactive
	}
	if row, ok := f.conversions[conv.OrderID]; ok {
		return row, false, nil
	}
	row := *conv
	row.ID = "conv_" + conv.OrderID
	row.Status = model.AffiliateConversionStatusPending
	f.conversions[conv.OrderID] = &row
	return &row, true, nil
}

// attrReq builds a gin context for one GET with optional cookies.
func attrReq(t *testing.T, target string, cookies ...*http.Cookie) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	for _, ck := range cookies {
		c.Request.AddCookie(ck)
	}
	return c
}

// The three inputs are read folded into their stored canonical forms:
// the reseller handle as a folded contact email, the referral code
// under its alphanumerics-only fold, the buyer email trimmed and
// lowered. ?ref= beats the htc_ref cookie; the cookie is the fallback
// when the link says nothing. A value that is not code-shaped is
// dropped rather than stamped.
func TestAttributionFromContext_ReadsQueryAndCookie(t *testing.T) {
	for _, tc := range []struct {
		name     string
		target   string
		cookies  []*http.Cookie
		reseller string
		ref      string
		buyer    string
	}{
		{
			name:     "all three inputs fold",
			target:   "/pay/abc12345?reseller_code=Partner@Example.com&ref=summer-sale&email=Buyer@Example.com",
			reseller: "partner@example.com", ref: "SUMMERSALE", buyer: "buyer@example.com",
		},
		{
			name:    "cookie is the ref fallback",
			target:  "/pay/abc12345",
			cookies: []*http.Cookie{{Name: referralCookieName, Value: "cookiecode"}},
			ref:     "COOKIECODE",
		},
		{
			name:    "explicit ref beats the cookie",
			target:  "/pay/abc12345?ref=WINNER1",
			cookies: []*http.Cookie{{Name: referralCookieName, Value: "LOSER1"}},
			ref:     "WINNER1",
		},
		{
			name:    "junk cookie is not attribution",
			target:  "/pay/abc12345",
			cookies: []*http.Cookie{{Name: referralCookieName, Value: "!!!"}},
		},
		{
			name:   "code-shaped junk that is too long is dropped",
			target: "/pay/abc12345?ref=ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789",
		},
		{
			name:     "unrelated cookie is ignored",
			target:   "/pay/abc12345?reseller_code=only%40example.com",
			cookies:  []*http.Cookie{{Name: "session", Value: "xyz"}},
			reseller: "only@example.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := attributionFromContext(attrReq(t, tc.target, tc.cookies...))
			if got.resellerCode != tc.reseller {
				t.Errorf("resellerCode = %q, want %q", got.resellerCode, tc.reseller)
			}
			if got.referralCode != tc.ref {
				t.Errorf("referralCode = %q, want %q", got.referralCode, tc.ref)
			}
			if got.buyerEmail != tc.buyer {
				t.Errorf("buyerEmail = %q, want %q", got.buyerEmail, tc.buyer)
			}
		})
	}
}

// attrFixture builds a world with two resellers (one holding a
// wholesale deal on plan_1), one affiliate and one active referral
// code. The pricing numbers are the pinned ones: catalog 10000,
// override 7500 → cut 2500.
func attrFixture() *fakeAttrStore {
	f := newFakeAttrStore()
	f.addReseller(&model.Reseller{ID: "res_a", Name: "Partner A", ContactEmail: "partner@example.com",
		Status: model.ResellerStatusActive, CommissionBPS: 1000})
	f.addReseller(&model.Reseller{ID: "res_b", Name: "Partner B", ContactEmail: "other@example.com",
		Status: model.ResellerStatusActive, CommissionBPS: 500})
	f.overrides["res_a/plan_1"] = &model.ResellerPriceOverride{
		ResellerID: "res_a", PlanID: "plan_1", UnitAmountMinor: 7500, Currency: "USD",
	}
	f.affiliates["aff_1"] = &model.Affiliate{ID: "aff_1", Name: "Ada",
		ContactEmail: "ada@example.com", Status: model.AffiliateStatusActive,
		CommissionModel: model.AffiliateCommissionModelPercent, CommissionBPS: 1500}
	f.addCode(&model.ReferralCode{ID: "code_1", AffiliateID: "aff_1", Code: "SUMMER26", Active: true})
	return f
}

// Wholesale pricing: the buyer email is the only authority. Catalog
// 10000 and override 7500 cut exactly 2500 — a one-time fixed-amount
// discount, never a percentage and never a float.
func TestResolveCheckoutAttribution_WholesaleCut(t *testing.T) {
	f := attrFixture()
	attr, err := resolveCheckoutAttribution(context.Background(), f,
		attributionQuery{buyerEmail: "partner@example.com"}, "plan_1", "USD", 10000)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if attr.wholesale == nil {
		t.Fatalf("no wholesale deal for a reseller holding an override")
	}
	if attr.wholesale.cutMinor != 2500 {
		t.Errorf("cut = %d, want 2500 (catalog 10000 − override 7500)", attr.wholesale.cutMinor)
	}
	if attr.wholesale.overridePriceMinor != 7500 {
		t.Errorf("override price = %d, want 7500", attr.wholesale.overridePriceMinor)
	}
	// The matched reseller is also the attributed one (their own
	// purchase is their order) when no code named a partner.
	if attr.resellerID != "res_a" || attr.resellerEmail != "partner@example.com" {
		t.Errorf("attribution = %q/%q, want res_a/partner@example.com", attr.resellerID, attr.resellerEmail)
	}
}

// Every "no deal" edge charges the list price without failing the
// checkout: a currency-mismatched override is ignored (no offline
// exchange rate), an override at or above catalog can only discount
// so it does not, and a buyer with no override has no deal.
func TestResolveCheckoutAttribution_WholesaleEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   func(f *fakeAttrStore)
		query    attributionQuery
		currency string
		catalog  int64
		wantDeal bool
	}{
		{
			name: "currency mismatch is ignored",
			mutate: func(f *fakeAttrStore) {
				f.overrides["res_a/plan_1"].Currency = "EUR"
			},
			query: attributionQuery{buyerEmail: "partner@example.com"}, currency: "USD", catalog: 10000,
		},
		{
			name: "override at catalog charges catalog",
			mutate: func(f *fakeAttrStore) {
				f.overrides["res_a/plan_1"].UnitAmountMinor = 10000
			},
			query: attributionQuery{buyerEmail: "partner@example.com"}, currency: "USD", catalog: 10000,
		},
		{
			name: "override above catalog charges catalog",
			mutate: func(f *fakeAttrStore) {
				f.overrides["res_a/plan_1"].UnitAmountMinor = 12000
			},
			query: attributionQuery{buyerEmail: "partner@example.com"}, currency: "USD", catalog: 10000,
		},
		{
			name: "negative override is no deal",
			mutate: func(f *fakeAttrStore) {
				f.overrides["res_a/plan_1"].UnitAmountMinor = -1
			},
			query: attributionQuery{buyerEmail: "partner@example.com"}, currency: "USD", catalog: 10000,
		},
		{
			name: "no override row is no deal",
			mutate: func(f *fakeAttrStore) {
				delete(f.overrides, "res_a/plan_1")
			},
			query: attributionQuery{buyerEmail: "partner@example.com"}, currency: "USD", catalog: 10000,
		},
		{
			name:  "buyer who is not a reseller gets no deal",
			query: attributionQuery{buyerEmail: "nobody@example.com"}, currency: "USD", catalog: 10000,
		},
		{
			name:  "no buyer email means no deal even with a reseller code",
			query: attributionQuery{resellerCode: "partner@example.com"}, currency: "USD", catalog: 10000,
		},
		{
			name:  "zero catalog price is no deal",
			query: attributionQuery{buyerEmail: "partner@example.com"}, currency: "USD", catalog: 0,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := attrFixture()
			if tc.mutate != nil {
				tc.mutate(f)
			}
			attr, err := resolveCheckoutAttribution(context.Background(), f, tc.query, "plan_1", tc.currency, tc.catalog)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if tc.wantDeal != (attr.wholesale != nil) {
				t.Fatalf("wholesale = %+v, want deal = %v", attr.wholesale, tc.wantDeal)
			}
		})
	}
}

// Attribution rules: reseller_code names the partner the order is
// attributed to (and prices nothing), the referral code is snapshotted
// in canonical form and resolved to its affiliate, and when the code
// and the buyer name different partners the code keeps the
// attribution while the buyer match still governs pricing.
func TestResolveCheckoutAttribution_AttributionRules(t *testing.T) {
	f := attrFixture()

	t.Run("reseller code attributes and never prices", func(t *testing.T) {
		attr, err := resolveCheckoutAttribution(context.Background(), f,
			attributionQuery{resellerCode: "Partner@Example.com"}, "plan_1", "USD", 10000)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if attr.resellerID != "res_a" || attr.resellerEmail != "partner@example.com" {
			t.Errorf("attribution = %q/%q, want res_a/partner@example.com", attr.resellerID, attr.resellerEmail)
		}
		if attr.wholesale != nil {
			t.Errorf("reseller_code priced the sale: %+v", attr.wholesale)
		}
	})

	t.Run("unresolvable reseller code attributes nothing", func(t *testing.T) {
		attr, err := resolveCheckoutAttribution(context.Background(), f,
			attributionQuery{resellerCode: "ghost@example.com"}, "plan_1", "USD", 10000)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if attr.resellerID != "" || attr.wholesale != nil {
			t.Errorf("attribution = %+v, want none", attr)
		}
	})

	t.Run("code keeps attribution, buyer keeps pricing, when they differ", func(t *testing.T) {
		attr, err := resolveCheckoutAttribution(context.Background(), f, attributionQuery{
			resellerCode: "other@example.com", buyerEmail: "partner@example.com",
		}, "plan_1", "USD", 10000)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if attr.resellerID != "res_b" {
			t.Errorf("attribution = %q, want res_b (the code's partner)", attr.resellerID)
		}
		if attr.wholesale == nil || attr.wholesale.resellerID != "res_a" || attr.wholesale.cutMinor != 2500 {
			t.Errorf("pricing = %+v, want res_a's deal cutting 2500", attr.wholesale)
		}
	})

	t.Run("referral code resolves to its affiliate and canonical form", func(t *testing.T) {
		attr, err := resolveCheckoutAttribution(context.Background(), f,
			attributionQuery{referralCode: "summer26"}, "plan_1", "USD", 10000)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if attr.referralCode != "SUMMER26" || attr.affiliateID != "aff_1" {
			t.Errorf("referral = %q/%q, want SUMMER26/aff_1", attr.referralCode, attr.affiliateID)
		}
	})

	t.Run("unresolvable referral code is still snapshotted, names no affiliate", func(t *testing.T) {
		attr, err := resolveCheckoutAttribution(context.Background(), f,
			attributionQuery{referralCode: "GHOST99"}, "plan_1", "USD", 10000)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		if attr.referralCode != "GHOST99" || attr.affiliateID != "" {
			t.Errorf("referral = %q/%q, want GHOST99/", attr.referralCode, attr.affiliateID)
		}
	})

	t.Run("attribution lookup failures are soft", func(t *testing.T) {
		fb := attrFixture()
		fb.emailLookupErr = errors.New("db down")
		attr, err := resolveCheckoutAttribution(context.Background(), fb,
			attributionQuery{resellerCode: "partner@example.com", referralCode: "SUMMER26"}, "plan_1", "USD", 10000)
		if err != nil {
			t.Fatalf("attribution-only failure must not fail the checkout: %v", err)
		}
		if attr.wholesale != nil {
			t.Errorf("wholesale = %+v, want none", attr.wholesale)
		}
	})

	t.Run("pricing lookup failures fail loudly", func(t *testing.T) {
		fb := attrFixture()
		fb.emailLookupErr = errors.New("db down")
		if _, err := resolveCheckoutAttribution(context.Background(), fb,
			attributionQuery{buyerEmail: "partner@example.com"}, "plan_1", "USD", 10000); err == nil {
			t.Fatalf("unreadable buyer pricing state must refuse the checkout, not charge list price")
		}
		fb = attrFixture()
		fb.overrideErr = errors.New("db down")
		if _, err := resolveCheckoutAttribution(context.Background(), fb,
			attributionQuery{buyerEmail: "partner@example.com"}, "plan_1", "USD", 10000); err == nil {
			t.Fatalf("unreadable override must refuse the checkout, not charge list price")
		}
	})
}

// The resolved facts stamp onto the session under the Order column
// key names and read straight back into the ledger row — the snapshot
// round-trip that keeps an order answerable after the partner tables
// move.
func TestCheckoutAttribution_MetadataRoundTrip(t *testing.T) {
	f := attrFixture()
	attr, err := resolveCheckoutAttribution(context.Background(), f, attributionQuery{
		resellerCode: "partner@example.com", referralCode: "summer26", buyerEmail: "partner@example.com",
	}, "plan_1", "USD", 10000)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	md := attr.metadata()
	for k, want := range map[string]string{
		metaResellerID:             "res_a",
		metaResellerEmail:          "partner@example.com",
		metaReferralCode:           "SUMMER26",
		metaAffiliateID:            "aff_1",
		metaWholesaleOverride:      "true",
		metaWholesaleOverridePrice: "7500",
	} {
		if got := md[k]; got != want {
			t.Errorf("metadata[%s] = %q, want %q", k, got, want)
		}
	}

	back := ledgerAttributionFromMetadata(md)
	o := &model.Order{}
	back.applyTo(o)
	if o.ResellerID != "res_a" || o.ResellerEmail != "partner@example.com" ||
		o.ReferralCode != "SUMMER26" || o.AffiliateID != "aff_1" {
		t.Errorf("order attribution = %+v, want the stamped facts", o)
	}
	if !back.hasWholesale || back.wholesalePrice != 7500 {
		t.Errorf("wholesale read-back = %v/%d, want true/7500", back.hasWholesale, back.wholesalePrice)
	}

	// Nothing stamped reads back as the zero Order columns.
	plain := ledgerAttributionFromMetadata(map[string]string{"plan_id": "plan_1"})
	empty := &model.Order{}
	plain.applyTo(empty)
	if empty.ResellerID != "" || empty.ResellerEmail != "" || empty.ReferralCode != "" || empty.AffiliateID != "" {
		t.Errorf("unstamped attribution filled columns: %+v", empty)
	}
}

// Read-back preference: stamped facts are what was true at sale time
// and win over any later re-derivation. The buyer-email fallback only
// fills what is missing — including the affiliate behind a stamped
// code the session could not resolve.
func TestLedgerAttribution_PrefersStampedFacts(t *testing.T) {
	f := attrFixture()

	t.Run("stamped facts beat the buyer-email fallback", func(t *testing.T) {
		a := ledgerAttributionFromMetadata(map[string]string{
			metaResellerID: "res_b", metaResellerEmail: "other@example.com",
		})
		a.fillFallbacks(context.Background(), f, "partner@example.com") // matches res_a
		o := &model.Order{}
		a.applyTo(o)
		if o.ResellerID != "res_b" || o.ResellerEmail != "other@example.com" {
			t.Errorf("attribution = %+v, want the stamped res_b", o)
		}
	})

	t.Run("fallback fills what the session did not stamp", func(t *testing.T) {
		var a ledgerAttribution
		a.fillFallbacks(context.Background(), f, "partner@example.com")
		o := &model.Order{}
		a.applyTo(o)
		if o.ResellerID != "res_a" || o.ResellerEmail != "partner@example.com" {
			t.Errorf("attribution = %+v, want res_a from the buyer match", o)
		}
	})

	t.Run("fallback resolves the affiliate behind a stamped code", func(t *testing.T) {
		a := ledgerAttributionFromMetadata(map[string]string{metaReferralCode: "SUMMER26"})
		a.fillFallbacks(context.Background(), f, "buyer@example.com")
		o := &model.Order{}
		a.applyTo(o)
		if o.ReferralCode != "SUMMER26" || o.AffiliateID != "aff_1" {
			t.Errorf("attribution = %+v, want SUMMER26/aff_1", o)
		}
	})

	t.Run("fallback failures are swallowed", func(t *testing.T) {
		fb := attrFixture()
		fb.emailLookupErr = errors.New("db down")
		var a ledgerAttribution
		a.fillFallbacks(context.Background(), fb, "partner@example.com")
		o := &model.Order{}
		a.applyTo(o)
		if o.ResellerID != "" {
			t.Errorf("attribution = %+v, want none after a failed lookup", o)
		}
	})
}

// The commission accrual: basis is the order total as charged, the
// rate is the reseller's contractual bps — the store computes the
// amount and owns the idempotency. A repeat accrual is one ledger
// row, and no failure ever escapes the hook.
func TestAccrueResellerCommission(t *testing.T) {
	t.Run("basis and bps reach the ledger", func(t *testing.T) {
		f := attrFixture()
		o := &model.Order{ID: "ord_1", ResellerID: "res_a", TotalMinor: 10000}
		accrueResellerCommission(context.Background(), f, o)
		if len(f.accrueCalls) != 1 {
			t.Fatalf("accrual calls = %d, want 1", len(f.accrueCalls))
		}
		cm := f.accrueCalls[0]
		if cm.ResellerID != "res_a" || cm.OrderID != "ord_1" {
			t.Errorf("accrual keys = %q/%q, want res_a/ord_1", cm.ResellerID, cm.OrderID)
		}
		if cm.BasisMinor != 10000 {
			t.Errorf("basis = %d, want the order total 10000", cm.BasisMinor)
		}
		if cm.BPS != 1000 {
			t.Errorf("bps = %d, want the reseller's contractual 1000", cm.BPS)
		}
		if row := f.commissions["res_a/ord_1"]; row == nil || row.AmountMinor != model.CommissionAmount(10000, 1000) {
			t.Errorf("stored amount = %+v, want %d (store-computed floor)",
				row, model.CommissionAmount(10000, 1000))
		}
	})

	t.Run("replay accrual is one row", func(t *testing.T) {
		f := attrFixture()
		o := &model.Order{ID: "ord_1", ResellerID: "res_a", TotalMinor: 10000}
		accrueResellerCommission(context.Background(), f, o)
		accrueResellerCommission(context.Background(), f, o)
		if len(f.commissions) != 1 {
			t.Fatalf("commission rows = %d, want 1", len(f.commissions))
		}
	})

	t.Run("no reseller, or a vanished one, accrues nothing", func(t *testing.T) {
		f := attrFixture()
		accrueResellerCommission(context.Background(), f, &model.Order{ID: "ord_2", TotalMinor: 10000})
		accrueResellerCommission(context.Background(), f, &model.Order{ID: "ord_3", ResellerID: "res_gone", TotalMinor: 10000})
		if len(f.accrueCalls) != 0 {
			t.Fatalf("accrual calls = %d, want 0", len(f.accrueCalls))
		}
	})

	t.Run("accrual errors are swallowed", func(t *testing.T) {
		f := attrFixture()
		f.accrueErr = errors.New("ledger unavailable")
		accrueResellerCommission(context.Background(), f,
			&model.Order{ID: "ord_4", ResellerID: "res_a", TotalMinor: 10000})
		// The hook returned normally — there is nothing to assert but
		// the absence of a panic/return value, and the call that was
		// attempted.
		if len(f.accrueCalls) != 1 {
			t.Fatalf("accrual calls = %d, want 1", len(f.accrueCalls))
		}
	})
}

// The affiliate conversion: commission under the affiliate's own
// model, status pending, one row per order however many times
// fulfilment replays — and a code or account that is not allowed to
// convert silently records nothing.
func TestRecordAffiliateConversion(t *testing.T) {
	t.Run("percent commission under the affiliate model", func(t *testing.T) {
		f := attrFixture()
		o := &model.Order{ID: "ord_1", ReferralCode: "SUMMER26", TotalMinor: 10000}
		recordAffiliateConversion(context.Background(), f, o)
		row := f.conversions["ord_1"]
		if row == nil {
			t.Fatalf("no conversion recorded")
		}
		if row.AffiliateID != "aff_1" || row.CodeID != "code_1" || row.OrderID != "ord_1" {
			t.Errorf("conversion = %+v, want aff_1/code_1/ord_1", row)
		}
		if row.OrderTotalMinor != 10000 {
			t.Errorf("order total = %d, want 10000", row.OrderTotalMinor)
		}
		if want := (int64(10000)*1500 + 5000) / 10000; row.CommissionMinor != want {
			t.Errorf("commission = %d, want %d (round-half-up percent)", row.CommissionMinor, want)
		}
		if row.Status != model.AffiliateConversionStatusPending {
			t.Errorf("status = %q, want pending", row.Status)
		}
	})

	t.Run("fixed commission under the affiliate model", func(t *testing.T) {
		f := attrFixture()
		f.affiliates["aff_1"].CommissionModel = model.AffiliateCommissionModelFixed
		f.affiliates["aff_1"].CommissionMinor = 250
		o := &model.Order{ID: "ord_2", ReferralCode: "SUMMER26", TotalMinor: 999}
		recordAffiliateConversion(context.Background(), f, o)
		if row := f.conversions["ord_2"]; row == nil || row.CommissionMinor != 250 {
			t.Errorf("conversion = %+v, want a fixed 250", row)
		}
	})

	t.Run("replayed fulfilment is one conversion", func(t *testing.T) {
		f := attrFixture()
		o := &model.Order{ID: "ord_3", ReferralCode: "SUMMER26", TotalMinor: 10000}
		for range 3 {
			recordAffiliateConversion(context.Background(), f, o)
		}
		if len(f.conversions) != 1 {
			t.Fatalf("conversion rows = %d, want 1", len(f.conversions))
		}
	})

	t.Run("unresolvable code, inactive code, suspended affiliate all skip", func(t *testing.T) {
		f := attrFixture()
		recordAffiliateConversion(context.Background(), f,
			&model.Order{ID: "ord_4", ReferralCode: "GHOST99", TotalMinor: 10000})

		f.codes["SUMMER26"].Active = false
		recordAffiliateConversion(context.Background(), f,
			&model.Order{ID: "ord_5", ReferralCode: "SUMMER26", TotalMinor: 10000})
		f.codes["SUMMER26"].Active = true

		f.affiliates["aff_1"].Status = model.AffiliateStatusSuspended
		recordAffiliateConversion(context.Background(), f,
			&model.Order{ID: "ord_6", ReferralCode: "SUMMER26", TotalMinor: 10000})
		f.affiliates["aff_1"].Status = model.AffiliateStatusActive

		if len(f.convertCalls) != 0 || len(f.conversions) != 0 {
			t.Fatalf("conversions = %d calls / %d rows, want 0/0", len(f.convertCalls), len(f.conversions))
		}
	})

	t.Run("no referral code means no conversion", func(t *testing.T) {
		f := attrFixture()
		recordAffiliateConversion(context.Background(), f,
			&model.Order{ID: "ord_7", TotalMinor: 10000})
		if len(f.convertCalls) != 0 {
			t.Fatalf("conversion calls = %d, want 0", len(f.convertCalls))
		}
	})

	t.Run("an uncommissionable total is skipped, not computed wrong", func(t *testing.T) {
		f := attrFixture()
		recordAffiliateConversion(context.Background(), f,
			&model.Order{ID: "ord_8", ReferralCode: "SUMMER26", TotalMinor: model.MaxCommissionableOrderMinor + 1})
		if len(f.convertCalls) != 0 {
			t.Fatalf("conversion calls = %d, want 0", len(f.convertCalls))
		}
	})

	t.Run("conversion errors are swallowed", func(t *testing.T) {
		f := attrFixture()
		f.convertErr = errors.New("ledger unavailable")
		recordAffiliateConversion(context.Background(), f,
			&model.Order{ID: "ord_9", ReferralCode: "SUMMER26", TotalMinor: 10000})
		if len(f.convertCalls) != 1 {
			t.Fatalf("conversion calls = %d, want 1", len(f.convertCalls))
		}
	})
}

// Fulfilment discipline: the two partner ledgers are independent
// side-effects. A commission accrual that errors must not stop the
// affiliate conversion (or vice versa) — and the whole effect set
// never returns anything a fulfilment could fail on.
func TestRecordAttributionEffects_ErrorsNeverBlock(t *testing.T) {
	f := attrFixture()
	f.accrueErr = errors.New("commission ledger unavailable")
	f.convertErr = errors.New("conversion ledger unavailable")
	recordAttributionEffects(context.Background(), f, &model.Order{
		ID: "ord_1", ResellerID: "res_a", ReferralCode: "SUMMER26", TotalMinor: 10000,
	})
	if len(f.accrueCalls) != 1 || len(f.convertCalls) != 1 {
		t.Fatalf("calls = %d accruals / %d conversions, want 1/1", len(f.accrueCalls), len(f.convertCalls))
	}
	// And a nil order is a no-op, not a panic.
	recordAttributionEffects(context.Background(), f, nil)
}
