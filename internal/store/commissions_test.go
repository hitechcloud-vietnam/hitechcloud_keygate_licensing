package store_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// TestCommissionTableAlias pins the alias Bun generates for the
// Commission model — the same bug class TestBunDefaultTableAliases
// pins for the commerce ones. Bun aliases a model by the snake_case of
// the STRUCT name, so the table is correlated as "commission", never
// as "commissions". The column spellings are pinned too: the money
// columns are hand-checked in the accrual math, and a Bun naming
// surprise on "bps" would otherwise surface only at runtime.
func TestCommissionTableAlias(t *testing.T) {
	db := bun.NewDB(nil, pgdialect.New())
	raw, err := db.NewSelect().Model((*model.Commission)(nil)).AppendQuery(db.QueryGen(), nil)
	if err != nil {
		t.Fatalf("build query: %v", err)
	}
	sqlText := string(raw)
	for _, want := range []string{
		`FROM "commissions" AS "commission"`,
		`"commission"."order_id"`,
		`"commission"."basis_minor"`,
		`"commission"."bps"`,
		`"commission"."amount_minor"`,
		`"commission"."paid_at"`,
	} {
		if !strings.Contains(sqlText, want) {
			t.Errorf("commissions SQL does not contain %q; got:\n%s", want, sqlText)
		}
	}
}

// newCommissionTestReseller makes a reseller with a fresh identity so
// several tests can share one database without colliding on the unique
// contact address.
func newCommissionTestReseller(t *testing.T, s *store.Store, ctx context.Context) *model.Reseller {
	t.Helper()
	uniq := store.NewID()
	r := &model.Reseller{
		Name:          "Commission Test " + uniq,
		ContactEmail:  "commission-" + uniq + "@example.com",
		Status:        model.ResellerStatusActive,
		CommissionBPS: 1000,
	}
	if err := s.CreateReseller(ctx, r); err != nil {
		t.Fatalf("create reseller: %v", err)
	}
	return r
}

// cleanupCommissionTestReseller removes what a test made: the ledger
// and the price config first (the ledger's FK restricts the account's
// deletion), then the account.
func cleanupCommissionTestReseller(t *testing.T, s *store.Store, ctx context.Context, r *model.Reseller) {
	t.Helper()
	_, _ = s.DB.NewRaw("DELETE FROM commissions WHERE reseller_id = ?", r.ID).Exec(ctx)
	_, _ = s.DB.NewRaw("DELETE FROM reseller_price_overrides WHERE reseller_id = ?", r.ID).Exec(ctx)
	_ = s.DeleteReseller(ctx, r.ID)
}

// Accrual is idempotent per (reseller, order): the second call for the
// same pair writes nothing and answers the ORIGINAL row — different
// numbers on the retry do not rewrite what the first writer recorded.
// The amount the store computes is the documented rounding-down math
// (1000 at 1000bps = exactly 100).
func TestAccrueCommissionIdempotent(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	r := newCommissionTestReseller(t, s, ctx)
	defer cleanupCommissionTestReseller(t, s, ctx, r)

	row, created, err := s.AccrueCommission(ctx, &model.Commission{
		ResellerID: r.ID,
		OrderID:    "ord-1",
		BasisMinor: 1000,
		BPS:        1000,
	})
	if err != nil {
		t.Fatalf("first accrue: %v", err)
	}
	if !created {
		t.Fatalf("first accrue reported created=false")
	}
	if row.AmountMinor != 100 {
		t.Errorf("amount = %d, want 100 (1000 at 1000bps)", row.AmountMinor)
	}
	if row.Status != model.CommissionStatusAccrued {
		t.Errorf("status = %q, want accrued", row.Status)
	}
	if row.PaidAt != nil {
		t.Errorf("paid_at = %v, want nil on accrual", row.PaidAt)
	}

	// Replay with different numbers: the original row comes back.
	again, created, err := s.AccrueCommission(ctx, &model.Commission{
		ResellerID: r.ID,
		OrderID:    "ord-1",
		BasisMinor: 999999,
		BPS:        10000,
	})
	if err != nil {
		t.Fatalf("replay accrue: %v", err)
	}
	if created {
		t.Errorf("replay reported created=true, want the original row")
	}
	if again.ID != row.ID || again.AmountMinor != 100 || again.BasisMinor != 1000 {
		t.Errorf("replay row = %+v, want the original (%s at basis 1000, amount 100)", again, row.ID)
	}

	// And the ledger holds exactly one row for the pair.
	rows, total, err := s.ListCommissions(ctx, r.ID, "", store.All)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Errorf("ledger = %d rows (total %d), want exactly 1", len(rows), total)
	}
}

// A reseller_id that names no reseller is sql.ErrNoRows — a typed 404,
// not a raw foreign-key violation.
func TestAccrueCommissionUnknownReseller(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	_, _, err := s.AccrueCommission(ctx, &model.Commission{
		ResellerID: "no-such-reseller",
		OrderID:    "ord-1",
		BasisMinor: 1000,
		BPS:        1000,
	})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("accrue err = %v, want sql.ErrNoRows", err)
	}
}

// The finders answer sql.ErrNoRows on a miss, and the order lookup is
// scoped by the (reseller, order) pair.
func TestCommissionFinders(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	r := newCommissionTestReseller(t, s, ctx)
	defer cleanupCommissionTestReseller(t, s, ctx, r)

	row, _, err := s.AccrueCommission(ctx, &model.Commission{
		ResellerID: r.ID, OrderID: "ord-1", BasisMinor: 1000, BPS: 1000,
	})
	if err != nil {
		t.Fatalf("accrue: %v", err)
	}

	byID, err := s.FindCommissionByID(ctx, row.ID)
	if err != nil {
		t.Fatalf("FindCommissionByID: %v", err)
	}
	if byID.OrderID != "ord-1" || byID.ResellerID != r.ID {
		t.Errorf("by-id row = %+v", byID)
	}

	byOrder, err := s.FindCommissionByOrder(ctx, r.ID, "ord-1")
	if err != nil {
		t.Fatalf("FindCommissionByOrder: %v", err)
	}
	if byOrder.ID != row.ID {
		t.Errorf("by-order id = %q, want %q", byOrder.ID, row.ID)
	}

	if _, err := s.FindCommissionByID(ctx, "nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing id err = %v, want sql.ErrNoRows", err)
	}
	if _, err := s.FindCommissionByOrder(ctx, r.ID, "nope"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("missing order err = %v, want sql.ErrNoRows", err)
	}
	// The pair is the identity: the right order under the wrong
	// reseller is a miss.
	if _, err := s.FindCommissionByOrder(ctx, "other-reseller", "ord-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("cross-reseller order err = %v, want sql.ErrNoRows", err)
	}
}

// Marking paid stamps the row and records when. Re-marking is a
// correction (re-stamp), while a cancelled commission is the one
// refusal — cancelled is a closed state.
func TestMarkCommissionPaid(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	r := newCommissionTestReseller(t, s, ctx)
	defer cleanupCommissionTestReseller(t, s, ctx, r)

	row, _, err := s.AccrueCommission(ctx, &model.Commission{
		ResellerID: r.ID, OrderID: "ord-1", BasisMinor: 1000, BPS: 1000,
	})
	if err != nil {
		t.Fatalf("accrue: %v", err)
	}

	paidAt := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	paid, err := s.MarkCommissionPaid(ctx, row.ID, paidAt)
	if err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if paid.Status != model.CommissionStatusPaid {
		t.Errorf("status = %q, want paid", paid.Status)
	}
	if paid.PaidAt == nil || !paid.PaidAt.Equal(paidAt) {
		t.Errorf("paid_at = %v, want %v", paid.PaidAt, paidAt)
	}

	// A zero paid_at means now.
	again, err := s.MarkCommissionPaid(ctx, row.ID, time.Time{})
	if err != nil {
		t.Fatalf("re-mark: %v", err)
	}
	if again.PaidAt == nil || time.Since(*again.PaidAt) > time.Minute {
		t.Errorf("re-mark paid_at = %v, want ~now", again.PaidAt)
	}

	// Cancelled is closed: the raw status flip is what the future
	// cancellation slice will do; the mark must refuse it.
	if _, err := s.DB.NewRaw(
		"UPDATE commissions SET status = ? WHERE id = ?", model.CommissionStatusCancelled, row.ID,
	).Exec(ctx); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := s.MarkCommissionPaid(ctx, row.ID, time.Now()); !errors.Is(err, store.ErrCommissionCancelled) {
		t.Errorf("mark-cancelled err = %v, want ErrCommissionCancelled", err)
	}

	// A missing id is sql.ErrNoRows.
	if _, err := s.MarkCommissionPaid(ctx, "nope", time.Now()); !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("mark-missing err = %v, want sql.ErrNoRows", err)
	}
}

// The listing narrows to one status of the closed vocabulary and pages
// newest-accrual-first with a total for the pager.
func TestListCommissionsFilterAndPaging(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	r := newCommissionTestReseller(t, s, ctx)
	defer cleanupCommissionTestReseller(t, s, ctx, r)

	for _, order := range []string{"ord-1", "ord-2", "ord-3"} {
		if _, _, err := s.AccrueCommission(ctx, &model.Commission{
			ResellerID: r.ID, OrderID: order, BasisMinor: 1000, BPS: 1000,
		}); err != nil {
			t.Fatalf("accrue %s: %v", order, err)
		}
	}
	// Pay one: it must drop out of the accrued filter.
	paid, err := s.FindCommissionByOrder(ctx, r.ID, "ord-1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if _, err := s.MarkCommissionPaid(ctx, paid.ID, time.Now()); err != nil {
		t.Fatalf("mark paid: %v", err)
	}

	accrued, total, err := s.ListCommissions(ctx, r.ID, model.CommissionStatusAccrued, store.All)
	if err != nil {
		t.Fatalf("list accrued: %v", err)
	}
	if total != 2 || len(accrued) != 2 {
		t.Errorf("accrued = %d rows (total %d), want 2", len(accrued), total)
	}

	paidRows, total, err := s.ListCommissions(ctx, r.ID, model.CommissionStatusPaid, store.All)
	if err != nil {
		t.Fatalf("list paid: %v", err)
	}
	if total != 1 || len(paidRows) != 1 || paidRows[0].OrderID != "ord-1" {
		t.Errorf("paid = %v (total %d), want just ord-1", paidRows, total)
	}

	// Two per window over three rows; the total ignores the window.
	page, total, err := s.ListCommissions(ctx, r.ID, "", store.Page{Limit: 2, Offset: 0})
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if len(page) != 2 {
		t.Errorf("page size = %d, want 2", len(page))
	}

	// Another reseller's ledger is untouched and its own.
	other := newCommissionTestReseller(t, s, ctx)
	defer cleanupCommissionTestReseller(t, s, ctx, other)
	rows, total, err := s.ListCommissions(ctx, other.ID, "", store.All)
	if err != nil {
		t.Fatalf("list other: %v", err)
	}
	if total != 0 || len(rows) != 0 {
		t.Errorf("other's ledger = %d rows (total %d), want 0", len(rows), total)
	}
}

// The dashboard sums are per status and exact in minor units.
func TestSumCommissionsByStatus(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()

	r := newCommissionTestReseller(t, s, ctx)
	defer cleanupCommissionTestReseller(t, s, ctx, r)

	for _, tc := range []struct {
		order string
		basis int64
		bps   int
	}{
		{"ord-1", 1000, 1000}, // 100
		{"ord-2", 3000, 1000}, // 300
		{"ord-3", 1003, 1000}, // 100 (100.3 rounds down)
	} {
		if _, _, err := s.AccrueCommission(ctx, &model.Commission{
			ResellerID: r.ID, OrderID: tc.order, BasisMinor: tc.basis, BPS: tc.bps,
		}); err != nil {
			t.Fatalf("accrue %s: %v", tc.order, err)
		}
	}
	paid, err := s.FindCommissionByOrder(ctx, r.ID, "ord-1")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if _, err := s.MarkCommissionPaid(ctx, paid.ID, time.Now()); err != nil {
		t.Fatalf("mark paid: %v", err)
	}

	sums, err := s.SumCommissionsByStatus(ctx, r.ID)
	if err != nil {
		t.Fatalf("sums: %v", err)
	}
	if sums[model.CommissionStatusAccrued] != 400 {
		t.Errorf("accrued sum = %d, want 400 (300 + 100)", sums[model.CommissionStatusAccrued])
	}
	if sums[model.CommissionStatusPaid] != 100 {
		t.Errorf("paid sum = %d, want 100", sums[model.CommissionStatusPaid])
	}
	if _, ok := sums[model.CommissionStatusCancelled]; ok {
		t.Errorf("cancelled sum present with no rows: %v", sums)
	}
}
