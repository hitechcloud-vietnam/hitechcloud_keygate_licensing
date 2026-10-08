package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// TestAffiliateTableAliases pins the aliases Bun generates for the
// affiliate models — the same bug class TestBunDefaultTableAliases
// pins for the commerce ones. Bun aliases a model by the snake_case of
// the STRUCT name (table affiliates, model.Affiliate -> AS "affiliate"),
// so hand-written qualifiers must use "affiliate", "referral_code",
// "referral_click", "affiliate_conversion" and "affiliate_payout",
// never the table names.
func TestAffiliateTableAliases(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())

	cases := []struct {
		model     interface{}
		wantAlias string
	}{
		{(*model.Affiliate)(nil), `"affiliate"`},
		{(*model.ReferralCode)(nil), `"referral_code"`},
		{(*model.ReferralClick)(nil), `"referral_click"`},
		{(*model.AffiliateConversion)(nil), `"affiliate_conversion"`},
		{(*model.AffiliatePayout)(nil), `"affiliate_payout"`},
	}
	for _, tc := range cases {
		raw, err := db.NewSelect().Model(tc.model).AppendQuery(db.QueryGen(), nil)
		if err != nil {
			t.Fatalf("build query for %T: %v", tc.model, err)
		}
		sqlText := string(raw)
		if as := "AS " + tc.wantAlias; !strings.Contains(sqlText, as) {
			t.Errorf("generated SQL for %T does not contain %q; got:\n%s", tc.model, as, sqlText)
		}
	}
}

// The conflict detectors must answer on exactly the two spellings of a
// refusal — the sentinel the write paths fold the driver error into,
// and a raw unique violation that never passed through them — and on
// nothing else, or a genuine failure would be answered 409.
func TestIsAffiliateEmailConflict(t *testing.T) {
	if store.IsAffiliateEmailConflict(nil) {
		t.Error("nil reads as an email conflict")
	}
	if store.IsAffiliateEmailConflict(errors.New("connection reset")) {
		t.Error("an arbitrary error reads as an email conflict")
	}
	if !store.IsAffiliateEmailConflict(store.ErrAffiliateEmailTaken) {
		t.Error("ErrAffiliateEmailTaken is not recognised")
	}
	wrapped := fmt.Errorf("create affiliate: %w", store.ErrAffiliateEmailTaken)
	if !store.IsAffiliateEmailConflict(wrapped) {
		t.Error("a wrapped ErrAffiliateEmailTaken is not recognised")
	}
}

func TestIsReferralCodeConflict(t *testing.T) {
	if store.IsReferralCodeConflict(nil) {
		t.Error("nil reads as a code conflict")
	}
	if store.IsReferralCodeConflict(errors.New("connection reset")) {
		t.Error("an arbitrary error reads as a code conflict")
	}
	if !store.IsReferralCodeConflict(store.ErrReferralCodeTaken) {
		t.Error("ErrReferralCodeTaken is not recognised")
	}
	wrapped := fmt.Errorf("create code: %w", store.ErrReferralCodeTaken)
	if !store.IsReferralCodeConflict(wrapped) {
		t.Error("a wrapped ErrReferralCodeTaken is not recognised")
	}
}

// ── DB-backed tests: skipped without TEST_DATABASE_URL ──

func newAffiliateTest(t *testing.T, s *store.Store, ctx context.Context, name, email string) *model.Affiliate {
	t.Helper()
	a := &model.Affiliate{
		Name: name, ContactEmail: email,
		Status:          model.AffiliateStatusActive,
		CommissionModel: model.AffiliateCommissionModelPercent,
		CommissionBPS:   1000,
	}
	if err := s.CreateAffiliate(ctx, a); err != nil {
		t.Fatalf("create affiliate %s: %v", name, err)
	}
	return a
}

func newCodeTest(t *testing.T, s *store.Store, ctx context.Context, affiliateID, code string) *model.ReferralCode {
	t.Helper()
	rc := &model.ReferralCode{AffiliateID: affiliateID, Code: code, LandingURL: "https://example.com/promo", Active: true}
	if err := s.CreateReferralCode(ctx, rc); err != nil {
		t.Fatalf("create code %s: %v", code, err)
	}
	return rc
}

// The contact email is folded on the way in, so two spellings of one
// address collide on the unique index and answer the typed refusal.
func TestAffiliateEmailConflict(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	newAffiliateTest(t, s, ctx, "Acme", "acme@example.com")

	dup := &model.Affiliate{Name: "Acme Two", ContactEmail: "ACME@Example.com"}
	err := s.CreateAffiliate(ctx, dup)
	if !store.IsAffiliateEmailConflict(err) {
		t.Fatalf("duplicate create err = %v, want an email conflict", err)
	}
}

// The finders fold their query keys and answer sql.ErrNoRows on a
// miss; the list filters and pages like every other admin list.
func TestAffiliateFindersAndList(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	a := newAffiliateTest(t, s, ctx, "Acme", "acme@example.com")
	newAffiliateTest(t, s, ctx, "Beta", "beta@example.com")

	got, err := s.FindAffiliateByID(ctx, a.ID)
	if err != nil {
		t.Fatalf("FindAffiliateByID: %v", err)
	}
	if got.Name != "Acme" || got.CommissionBPS != 1000 {
		t.Errorf("by-id row = %+v", got)
	}

	byEmail, err := s.FindAffiliateByEmail(ctx, "  ACME@example.COM ")
	if err != nil {
		t.Fatalf("FindAffiliateByEmail: %v", err)
	}
	if byEmail.ID != a.ID {
		t.Errorf("by-email id = %q, want %q", byEmail.ID, a.ID)
	}

	if _, err := s.FindAffiliateByID(ctx, "nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing id err = %v, want sql.ErrNoRows", err)
	}

	found, total, err := s.ListAffiliates(ctx, "bet", "", store.All)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if total != 1 || len(found) != 1 || found[0].Name != "Beta" {
		t.Errorf("search 'bet' = %v (total %d), want just Beta", found, total)
	}
}

// Delete refuses while money records exist — conversions or payouts —
// and cascades the account's codes and clicks when only those remain.
func TestAffiliateDeletePolicy(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	a := newAffiliateTest(t, s, ctx, "Acme", "acme@example.com")
	rc := newCodeTest(t, s, ctx, a.ID, "SAVE20")

	// Codes and clicks alone never block a delete.
	if _, err := s.RecordClick(ctx, &model.ReferralClick{
		CodeID: rc.ID, ClickedAt: time.Now(),
		IPHash: model.HashReferralIP("salt", "203.0.113.7"),
	}); err != nil {
		t.Fatalf("record click: %v", err)
	}

	withConv := newAffiliateTest(t, s, ctx, "Conv", "conv@example.com")
	convCode := newCodeTest(t, s, ctx, withConv.ID, "CONV20")
	if _, _, err := s.RecordConversion(ctx, &model.AffiliateConversion{
		AffiliateID: withConv.ID, CodeID: convCode.ID, OrderID: "ORD-DEL-1",
		OrderTotalMinor: 10000, CommissionMinor: 1000,
	}); err != nil {
		t.Fatalf("record conversion: %v", err)
	}
	if err := s.DeleteAffiliate(ctx, withConv.ID); !errors.Is(err, store.ErrAffiliateHasConversions) {
		t.Errorf("delete with conversions err = %v, want ErrAffiliateHasConversions", err)
	}

	withPayout := newAffiliateTest(t, s, ctx, "Pay", "pay@example.com")
	payCode := newCodeTest(t, s, ctx, withPayout.ID, "PAY20X")
	if _, _, err := s.RecordConversion(ctx, &model.AffiliateConversion{
		AffiliateID: withPayout.ID, CodeID: payCode.ID, OrderID: "ORD-DEL-2",
		OrderTotalMinor: 10000, CommissionMinor: 1000,
	}); err != nil {
		t.Fatalf("record conversion: %v", err)
	}
	if err := s.CreatePayout(ctx, &model.AffiliatePayout{AffiliateID: withPayout.ID, AmountMinor: 1000}); err != nil {
		t.Fatalf("create payout: %v", err)
	}
	if err := s.DeleteAffiliate(ctx, withPayout.ID); !errors.Is(err, store.ErrAffiliateHasPayouts) {
		t.Errorf("delete with payouts err = %v, want ErrAffiliateHasPayouts", err)
	}

	// The affiliate with only codes and clicks deletes fine.
	if err := s.DeleteAffiliate(ctx, a.ID); err != nil {
		t.Fatalf("delete with only codes/clicks: %v", err)
	}
	// Its codes went with it (ON DELETE CASCADE).
	if _, err := s.FindReferralCodeByID(ctx, rc.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("cascaded code lookup err = %v, want sql.ErrNoRows", err)
	}
	// A delete that was not there is a 404, not a claimed deletion.
	if err := s.DeleteAffiliate(ctx, a.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("second delete err = %v, want sql.ErrNoRows", err)
	}
}

// Codes are folded on the way in and unique after the fold; the lookup
// folds too, so any spelling resolves. A code that earned conversions
// is deactivated, never deleted.
func TestReferralCodeFoldingConflictAndDelete(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	a := newAffiliateTest(t, s, ctx, "Acme", "acme@example.com")
	rc := newCodeTest(t, s, ctx, a.ID, "Summer-Sale!")

	if rc.Code != "SUMMERSALE" {
		t.Errorf("stored code = %q, want %q", rc.Code, "SUMMERSALE")
	}
	got, err := s.FindReferralCodeByCode(ctx, " summer sale! ")
	if err != nil {
		t.Fatalf("FindReferralCodeByCode: %v", err)
	}
	if got.ID != rc.ID {
		t.Errorf("resolved id = %q, want %q", got.ID, rc.ID)
	}
	if _, err := s.FindReferralCodeByCode(ctx, "nope99"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing code err = %v, want sql.ErrNoRows", err)
	}

	dup := &model.ReferralCode{AffiliateID: a.ID, Code: "summer_sale", Active: true}
	if err := s.CreateReferralCode(ctx, dup); !store.IsReferralCodeConflict(err) {
		t.Errorf("duplicate code err = %v, want a code conflict", err)
	}

	// A code with conversions cannot be deleted…
	conv := &model.AffiliateConversion{
		AffiliateID: a.ID, CodeID: rc.ID, OrderID: "ORD-CODE-1",
		OrderTotalMinor: 5000, CommissionMinor: 500,
	}
	if _, _, err := s.RecordConversion(ctx, conv); err != nil {
		t.Fatalf("record conversion: %v", err)
	}
	if err := s.DeleteReferralCode(ctx, a.ID, rc.ID); !errors.Is(err, store.ErrReferralCodeHasConversions) {
		t.Errorf("delete earned code err = %v, want ErrReferralCodeHasConversions", err)
	}

	// …but one that only ever collected clicks can be, and takes its
	// clicks with it.
	other := newCodeTest(t, s, ctx, a.ID, "PLAIN99")
	if _, err := s.RecordClick(ctx, &model.ReferralClick{
		CodeID: other.ID, ClickedAt: time.Now(),
		IPHash: model.HashReferralIP("salt", "203.0.113.9"),
	}); err != nil {
		t.Fatalf("record click: %v", err)
	}
	if err := s.DeleteReferralCode(ctx, a.ID, other.ID); err != nil {
		t.Fatalf("delete click-only code: %v", err)
	}

	// Route scoping: another affiliate's id cannot delete this code.
	b := newAffiliateTest(t, s, ctx, "Other", "other@example.com")
	if err := s.DeleteReferralCode(ctx, b.ID, rc.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("cross-affiliate delete err = %v, want sql.ErrNoRows", err)
	}
	if err := s.DeleteReferralCode(ctx, a.ID, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing code delete err = %v, want sql.ErrNoRows", err)
	}
}

// Click recording: hashed inputs only, one row per (code, ip_hash)
// inside the dedup window, and a fresh row outside it.
func TestRecordClickDedupAndHashing(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	a := newAffiliateTest(t, s, ctx, "Acme", "acme@example.com")
	rc := newCodeTest(t, s, ctx, a.ID, "CLICK01")
	const rawIP = "203.0.113.7"
	const rawUA = "Mozilla/5.0 (Test)"
	ipHash := model.HashReferralIP("salt", rawIP)

	first := &model.ReferralClick{
		CodeID: rc.ID, ClickedAt: time.Now(),
		IPHash: ipHash, UserAgentHash: model.HashReferralUserAgent("salt", rawUA),
	}
	created, err := s.RecordClick(ctx, first)
	if err != nil || !created {
		t.Fatalf("first click: created=%v err=%v", created, err)
	}

	// The stored row is the hash and nothing but the hash: the raw IP
	// and user agent are never written.
	var stored model.ReferralClick
	if err := s.DB.NewSelect().Model(&stored).Where("id = ?", first.ID).Scan(ctx); err != nil {
		t.Fatalf("read click: %v", err)
	}
	if stored.IPHash != ipHash {
		t.Errorf("stored ip_hash = %q, want the hash %q", stored.IPHash, ipHash)
	}
	if strings.Contains(stored.IPHash, rawIP) || stored.IPHash == rawIP {
		t.Error("the raw IP was stored")
	}
	if strings.Contains(stored.UserAgentHash, rawUA) || len(stored.UserAgentHash) != 64 {
		t.Errorf("user_agent_hash = %q, want a 64-hex hash", stored.UserAgentHash)
	}

	// Same code + same ip_hash inside the window: one click row.
	created, err = s.RecordClick(ctx, &model.ReferralClick{
		CodeID: rc.ID, ClickedAt: time.Now(), IPHash: ipHash,
	})
	if err != nil {
		t.Fatalf("dedup click: %v", err)
	}
	if created {
		t.Error("a duplicate click inside the window was recorded")
	}
	n, err := s.DB.NewSelect().Model((*model.ReferralClick)(nil)).Where("code_id = ?", rc.ID).Count(ctx)
	if err != nil {
		t.Fatalf("count clicks: %v", err)
	}
	if n != 1 {
		t.Errorf("click rows = %d, want 1", n)
	}

	// A different ip_hash is a different visitor.
	created, err = s.RecordClick(ctx, &model.ReferralClick{
		CodeID: rc.ID, ClickedAt: time.Now(),
		IPHash: model.HashReferralIP("salt", "203.0.113.8"),
	})
	if err != nil || !created {
		t.Errorf("other-IP click: created=%v err=%v", created, err)
	}

	// Outside the window it is a new visit, and counts again. The
	// window is measured against the click being recorded, so a
	// backdated first click makes the second one a fresh visit.
	old := &model.ReferralClick{
		CodeID: rc.ID, ClickedAt: time.Now().Add(-store.ClickDedupWindow - time.Minute),
		IPHash: model.HashReferralIP("salt", "203.0.113.10"),
	}
	if created, err := s.RecordClick(ctx, old); err != nil || !created {
		t.Fatalf("old click: created=%v err=%v", created, err)
	}
	fresh := &model.ReferralClick{
		CodeID: rc.ID, ClickedAt: time.Now(), IPHash: old.IPHash,
	}
	if created, err := s.RecordClick(ctx, fresh); err != nil || !created {
		t.Errorf("click after the window: created=%v err=%v", created, err)
	}
}

// Conversion recording: idempotent per order_id (fraud control — one
// order converts at most once), and a suspended affiliate or an
// inactive code never converts.
func TestRecordConversionIdempotencyAndGuards(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	a := newAffiliateTest(t, s, ctx, "Acme", "acme@example.com")
	rc := newCodeTest(t, s, ctx, a.ID, "CONV01")

	conv := &model.AffiliateConversion{
		AffiliateID: a.ID, CodeID: rc.ID, OrderID: "ORD-IDEM-1",
		UserID: "user-1", OrderTotalMinor: 10000, CommissionMinor: 1000,
	}
	first, created, err := s.RecordConversion(ctx, conv)
	if err != nil || !created {
		t.Fatalf("first conversion: created=%v err=%v", created, err)
	}
	if first.Status != model.AffiliateConversionStatusPending {
		t.Errorf("initial status = %q, want pending", first.Status)
	}

	// A double submit — same order, same everything — is one row and
	// the same authoritative row both times.
	again, created, err := s.RecordConversion(ctx, &model.AffiliateConversion{
		AffiliateID: a.ID, CodeID: rc.ID, OrderID: "ORD-IDEM-1",
		OrderTotalMinor: 10000, CommissionMinor: 1000,
	})
	if err != nil {
		t.Fatalf("double submit: %v", err)
	}
	if created {
		t.Error("double submit created a second row")
	}
	if again.ID != first.ID {
		t.Errorf("double submit returned id %q, want %q", again.ID, first.ID)
	}
	n, err := s.DB.NewSelect().Model((*model.AffiliateConversion)(nil)).
		Where("order_id = ?", "ORD-IDEM-1").Count(ctx)
	if err != nil {
		t.Fatalf("count conversions: %v", err)
	}
	if n != 1 {
		t.Errorf("conversion rows = %d, want 1", n)
	}

	// Suspended affiliates never convert (fraud control), even with a
	// valid code.
	a.Status = model.AffiliateStatusSuspended
	if err := s.UpdateAffiliate(ctx, a); err != nil {
		t.Fatalf("suspend: %v", err)
	}
	_, _, err = s.RecordConversion(ctx, &model.AffiliateConversion{
		AffiliateID: a.ID, CodeID: rc.ID, OrderID: "ORD-IDEM-2",
		OrderTotalMinor: 10000, CommissionMinor: 1000,
	})
	if !errors.Is(err, store.ErrAffiliateNotActive) {
		t.Errorf("suspended conversion err = %v, want ErrAffiliateNotActive", err)
	}
	a.Status = model.AffiliateStatusActive
	if err := s.UpdateAffiliate(ctx, a); err != nil {
		t.Fatalf("reactivate: %v", err)
	}

	// An inactive code stops converting without losing its history.
	rc.Active = false
	if err := s.UpdateReferralCode(ctx, rc); err != nil {
		t.Fatalf("deactivate code: %v", err)
	}
	_, _, err = s.RecordConversion(ctx, &model.AffiliateConversion{
		AffiliateID: a.ID, CodeID: rc.ID, OrderID: "ORD-IDEM-3",
		OrderTotalMinor: 10000, CommissionMinor: 1000,
	})
	if !errors.Is(err, store.ErrReferralCodeInactive) {
		t.Errorf("inactive-code conversion err = %v, want ErrReferralCodeInactive", err)
	}
}

// The review state machine, including the payout freeze: a conversion
// claimed by a still-requested payout cannot change underneath it.
func TestSetConversionStatusTransitions(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	a := newAffiliateTest(t, s, ctx, "Acme", "acme@example.com")
	rc := newCodeTest(t, s, ctx, a.ID, "STAT01")
	newConv := func(order string) *model.AffiliateConversion {
		_, _, err := s.RecordConversion(ctx, &model.AffiliateConversion{
			AffiliateID: a.ID, CodeID: rc.ID, OrderID: order,
			OrderTotalMinor: 10000, CommissionMinor: 1000,
		})
		if err != nil {
			t.Fatalf("record conversion %s: %v", order, err)
		}
		conv, err := s.FindConversionByOrderID(ctx, order)
		if err != nil {
			t.Fatalf("find %s: %v", order, err)
		}
		return conv
	}

	// pending -> approved -> reversed (the clawback).
	c1 := newConv("ORD-STAT-1")
	got, err := s.SetConversionStatus(ctx, c1.ID, model.AffiliateConversionStatusApproved)
	if err != nil || got.Status != model.AffiliateConversionStatusApproved {
		t.Fatalf("approve: status=%q err=%v", got.Status, err)
	}
	got, err = s.SetConversionStatus(ctx, c1.ID, model.AffiliateConversionStatusReversed)
	if err != nil || got.Status != model.AffiliateConversionStatusReversed {
		t.Fatalf("reverse: status=%q err=%v", got.Status, err)
	}
	// Terminal: reversed cannot be approved again.
	if _, err := s.SetConversionStatus(ctx, c1.ID, model.AffiliateConversionStatusApproved); !errors.Is(err, store.ErrConversionInvalidTransition) {
		t.Errorf("un-reverse err = %v, want ErrConversionInvalidTransition", err)
	}

	// pending -> rejected is terminal too.
	c2 := newConv("ORD-STAT-2")
	if _, err := s.SetConversionStatus(ctx, c2.ID, model.AffiliateConversionStatusRejected); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if _, err := s.SetConversionStatus(ctx, c2.ID, model.AffiliateConversionStatusApproved); !errors.Is(err, store.ErrConversionInvalidTransition) {
		t.Errorf("un-reject err = %v, want ErrConversionInvalidTransition", err)
	}

	// paid is never a direct move.
	c3 := newConv("ORD-STAT-3")
	if _, err := s.SetConversionStatus(ctx, c3.ID, model.AffiliateConversionStatusPaid); !errors.Is(err, store.ErrConversionInvalidTransition) {
		t.Errorf("direct pay err = %v, want ErrConversionInvalidTransition", err)
	}

	// A missing conversion is a 404-shaped miss.
	if _, err := s.SetConversionStatus(ctx, "missing", model.AffiliateConversionStatusApproved); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing conversion err = %v, want sql.ErrNoRows", err)
	}

	// The freeze: claimed by a requested payout -> refused until the
	// payout resolves. (Amount 0 settles the whole balance: whatever is
	// still accrued, including c3 and c4.)
	c4 := newConv("ORD-STAT-4")
	if err := s.CreatePayout(ctx, &model.AffiliatePayout{AffiliateID: a.ID}); err != nil {
		t.Fatalf("create payout: %v", err)
	}
	c4r, err := s.FindConversionByID(ctx, c4.ID)
	if err != nil || c4r.PayoutID == "" {
		t.Fatalf("conversion not claimed: %+v err=%v", c4r, err)
	}
	if _, err := s.SetConversionStatus(ctx, c4.ID, model.AffiliateConversionStatusRejected); !errors.Is(err, store.ErrConversionInPayout) {
		t.Errorf("frozen conversion err = %v, want ErrConversionInPayout", err)
	}
	// Once the payout failed and released it, review resumes.
	if _, err := s.MarkPayoutFailed(ctx, c4r.PayoutID, "bank details wrong"); err != nil {
		t.Fatalf("fail payout: %v", err)
	}
	if _, err := s.SetConversionStatus(ctx, c4.ID, model.AffiliateConversionStatusApproved); err != nil {
		t.Errorf("review after release: %v", err)
	}
}

// The accrued balance counts pending and approved, unclaimed,
// commission — and nothing else.
func TestSumPendingCommissions(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	a := newAffiliateTest(t, s, ctx, "Acme", "acme@example.com")
	rc := newCodeTest(t, s, ctx, a.ID, "SUMS01")

	add := func(order string, minor int64, status string) *model.AffiliateConversion {
		conv, _, err := s.RecordConversion(ctx, &model.AffiliateConversion{
			AffiliateID: a.ID, CodeID: rc.ID, OrderID: order,
			OrderTotalMinor: minor * 10, CommissionMinor: minor,
		})
		if err != nil {
			t.Fatalf("record %s: %v", order, err)
		}
		if status != model.AffiliateConversionStatusPending {
			if _, err := s.SetConversionStatus(ctx, conv.ID, status); err != nil {
				t.Fatalf("move %s to %s: %v", order, status, err)
			}
		}
		return conv
	}

	add("ORD-SUM-1", 1000, model.AffiliateConversionStatusPending)  // counts
	add("ORD-SUM-2", 2000, model.AffiliateConversionStatusApproved) // counts
	add("ORD-SUM-3", 4000, model.AffiliateConversionStatusRejected) // earns nothing
	add("ORD-SUM-4", 8000, model.AffiliateConversionStatusReversed) // clawed back

	total, err := s.SumPendingCommissions(ctx, a.ID)
	if err != nil {
		t.Fatalf("SumPendingCommissions: %v", err)
	}
	if total != 3000 {
		t.Errorf("accrued = %d, want 3000", total)
	}

	// A payout claims the balance; it is then neither accrued nor
	// double-countable.
	if err := s.CreatePayout(ctx, &model.AffiliatePayout{AffiliateID: a.ID}); err != nil {
		t.Fatalf("create payout: %v", err)
	}
	total, err = s.SumPendingCommissions(ctx, a.ID)
	if err != nil {
		t.Fatalf("after payout: %v", err)
	}
	if total != 0 {
		t.Errorf("accrued after payout = %d, want 0", total)
	}
}

// The payout state machine: greedy whole-conversion claiming with the
// settled sum recorded, paid settles its claims, failed releases them,
// and only requested ever moves.
func TestPayoutStateMachine(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	a := newAffiliateTest(t, s, ctx, "Acme", "acme@example.com")
	rc := newCodeTest(t, s, ctx, a.ID, "PAY01")

	add := func(order string, minor int64) *model.AffiliateConversion {
		conv, _, err := s.RecordConversion(ctx, &model.AffiliateConversion{
			AffiliateID: a.ID, CodeID: rc.ID, OrderID: order,
			OrderTotalMinor: minor * 10, CommissionMinor: minor,
		})
		if err != nil {
			t.Fatalf("record %s: %v", order, err)
		}
		return conv
	}
	c1 := add("ORD-PAY-1", 600)
	c2 := add("ORD-PAY-2", 600)
	c3 := add("ORD-PAY-3", 600)

	// Asking for 1500 settles whole conversions oldest-first and stops
	// at the first overshoot: 600 + 600 = 1200, never 1500.
	pay := &model.AffiliatePayout{AffiliateID: a.ID, AmountMinor: 1500, Notes: "october"}
	if err := s.CreatePayout(ctx, pay); err != nil {
		t.Fatalf("create payout: %v", err)
	}
	if pay.AmountMinor != 1200 {
		t.Errorf("payout amount = %d, want the settled sum 1200", pay.AmountMinor)
	}
	if pay.Status != model.AffiliatePayoutStatusRequested {
		t.Errorf("initial status = %q, want requested", pay.Status)
	}
	claimed := 0
	for _, id := range []string{c1.ID, c2.ID, c3.ID} {
		conv, err := s.FindConversionByID(ctx, id)
		if err != nil {
			t.Fatalf("read conversion: %v", err)
		}
		if conv.PayoutID == pay.ID {
			claimed++
		}
	}
	if claimed != 2 {
		t.Errorf("claimed conversions = %d, want the two oldest", claimed)
	}

	// The unclaimed remainder is still accrued and pays out next
	// (amount 0 = the whole balance).
	pay2 := &model.AffiliatePayout{AffiliateID: a.ID}
	if err := s.CreatePayout(ctx, pay2); err != nil {
		t.Fatalf("second create: %v", err)
	}
	if pay2.AmountMinor != 600 {
		t.Errorf("second payout amount = %d, want the remaining 600", pay2.AmountMinor)
	}

	// Everything is claimed now: nothing left to pay.
	if err := s.CreatePayout(ctx, &model.AffiliatePayout{AffiliateID: a.ID}); !errors.Is(err, store.ErrPayoutNothingToPay) {
		t.Errorf("third payout err = %v, want ErrPayoutNothingToPay", err)
	}

	// Paid: the payout stamps paid_at and its claims become paid.
	got, err := s.MarkPayoutPaid(ctx, pay.ID)
	if err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if got.Status != model.AffiliatePayoutStatusPaid || got.PaidAt == nil {
		t.Errorf("paid payout = %+v, want status paid with paid_at", got)
	}
	for _, id := range []string{c1.ID, c2.ID} {
		conv, err := s.FindConversionByID(ctx, id)
		if err != nil {
			t.Fatalf("read conversion: %v", err)
		}
		if conv.Status != model.AffiliateConversionStatusPaid {
			t.Errorf("claimed conversion %s status = %q, want paid", id, conv.Status)
		}
	}

	// Terminal: a paid payout never moves again.
	if _, err := s.MarkPayoutPaid(ctx, pay.ID); !errors.Is(err, store.ErrPayoutInvalidTransition) {
		t.Errorf("re-pay err = %v, want ErrPayoutInvalidTransition", err)
	}
	if _, err := s.MarkPayoutFailed(ctx, pay.ID, ""); !errors.Is(err, store.ErrPayoutInvalidTransition) {
		t.Errorf("fail-after-pay err = %v, want ErrPayoutInvalidTransition", err)
	}

	// Failed: the claims are released and the money owed again.
	if _, err := s.MarkPayoutFailed(ctx, pay2.ID, "transfer bounced"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	conv3, err := s.FindConversionByID(ctx, c3.ID)
	if err != nil {
		t.Fatalf("read released conversion: %v", err)
	}
	if conv3.PayoutID != "" {
		t.Errorf("released conversion still claims payout %q", conv3.PayoutID)
	}
	if conv3.Status != model.AffiliateConversionStatusPending {
		t.Errorf("released conversion status = %q, want pending", conv3.Status)
	}
	total, err := s.SumPendingCommissions(ctx, a.ID)
	if err != nil {
		t.Fatalf("accrued after release: %v", err)
	}
	if total != 600 {
		t.Errorf("accrued after failed payout = %d, want 600 back", total)
	}
	// And a failed payout never moves again either.
	if _, err := s.MarkPayoutPaid(ctx, pay2.ID); !errors.Is(err, store.ErrPayoutInvalidTransition) {
		t.Errorf("pay-after-fail err = %v, want ErrPayoutInvalidTransition", err)
	}

	// A missing payout is a 404-shaped miss on both doors.
	if _, err := s.MarkPayoutPaid(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing payout err = %v, want sql.ErrNoRows", err)
	}
	if _, err := s.MarkPayoutFailed(ctx, "missing", ""); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing payout fail err = %v, want sql.ErrNoRows", err)
	}
}
