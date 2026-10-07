package store_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// portalTestOrder writes one order (with a single line item) for the
// customer-portal queries to find — or to refuse to find.
func portalTestOrder(t *testing.T, s *store.Store, ctx context.Context, email, suffix, status string) *model.Order {
	t.Helper()
	o := &model.Order{
		OrderNumber:     "HTC-PT" + suffix,
		CustomerEmail:   email,
		Currency:        "USD",
		SubtotalMinor:   1000,
		TotalMinor:      1000,
		Status:          status,
		PaymentProvider: "test",
		Items: []*model.OrderItem{{
			SKU:               "SKU-" + suffix,
			Quantity:          1,
			UnitAmountMinor:   1000,
			LineSubtotalMinor: 1000,
			LineTotalMinor:    1000,
		}},
	}
	if err := s.CreateOrder(ctx, o); err != nil {
		t.Fatalf("create order: %v", err)
	}
	return o
}

// TestPortalOrdersByEmail_OwnershipAndPaging proves the portal list is
// scoped to the caller's own orders (case-insensitive email match) and
// pages the way the admin list does.
func TestPortalOrdersByEmail_OwnershipAndPaging(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := time.Now().Format("150405.000")
	owner := "portal-owner-" + suffix + "@example.com"
	stranger := "portal-other-" + suffix + "@example.com"

	mine1 := portalTestOrder(t, s, ctx, owner, suffix+"A", model.OrderStatusPaid)
	mine2 := portalTestOrder(t, s, ctx, owner, suffix+"B", model.OrderStatusPending)
	theirs := portalTestOrder(t, s, ctx, stranger, suffix+"C", model.OrderStatusPaid)
	defer func() {
		for _, id := range []string{mine1.ID, mine2.ID, theirs.ID} {
			_, _ = s.DB.NewRaw("DELETE FROM order_items WHERE order_id = ?", id).Exec(ctx)
			_, _ = s.DB.NewRaw("DELETE FROM invoices WHERE order_id = ?", id).Exec(ctx)
			_, _ = s.DB.NewRaw("DELETE FROM orders WHERE id = ?", id).Exec(ctx)
		}
	}()

	ids := func(rows []*model.Order) map[string]bool {
		m := map[string]bool{}
		for _, r := range rows {
			m[r.ID] = true
		}
		return m
	}

	rows, total, err := s.ListOrdersByEmail(ctx, owner, "", store.Page{Limit: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("owner list: %d rows (total %d), want 2", len(rows), total)
	}
	got := ids(rows)
	if !got[mine1.ID] || !got[mine2.ID] || got[theirs.ID] {
		t.Fatalf("owner list leaked or lost orders: %v", got)
	}

	// Case-insensitive: the session email may be folded differently
	// from how the order stored it.
	rows, total, err = s.ListOrdersByEmail(ctx, strings.ToUpper(owner), "", store.Page{Limit: 10})
	if err != nil || total != 2 || len(rows) != 2 {
		t.Fatalf("upper-case owner list: %d rows (total %d) err=%v, want 2/2", len(rows), total, err)
	}

	// The stranger sees only their own.
	rows, total, err = s.ListOrdersByEmail(ctx, stranger, "", store.Page{Limit: 10})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != theirs.ID {
		t.Fatalf("stranger list: %d rows (total %d) err=%v, want only theirs", len(rows), total, err)
	}

	// Status filter.
	rows, total, err = s.ListOrdersByEmail(ctx, owner, model.OrderStatusPaid, store.Page{Limit: 10})
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != mine1.ID {
		t.Fatalf("paid filter: %d rows (total %d) err=%v, want only the paid one", len(rows), total, err)
	}

	// Paging: limit 1 serves one row but reports the real total; the
	// next page serves the other one.
	first, total, err := s.ListOrdersByEmail(ctx, owner, "", store.Page{Limit: 1})
	if err != nil || total != 2 || len(first) != 1 {
		t.Fatalf("page 1: %d rows (total %d) err=%v, want 1/2", len(first), total, err)
	}
	second, total, err := s.ListOrdersByEmail(ctx, owner, "", store.Page{Limit: 1, Offset: 1})
	if err != nil || total != 2 || len(second) != 1 {
		t.Fatalf("page 2: %d rows (total %d) err=%v, want 1/2", len(second), total, err)
	}
	if first[0].ID == second[0].ID {
		t.Fatalf("paging served the same row twice: %s", first[0].ID)
	}

	n, err := s.CountOrdersByEmail(ctx, owner, "")
	if err != nil || n != 2 {
		t.Fatalf("count: %d err=%v, want 2", n, err)
	}

	// An empty email must match nothing rather than orders that
	// somehow carry an empty address.
	rows, total, err = s.ListOrdersByEmail(ctx, "   ", "", store.Page{Limit: 10})
	if err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("empty email: %d rows (total %d) err=%v, want none", len(rows), total, err)
	}
}

// TestPortalFindOrderByIdAndEmail_Ownership proves the ownership-checked
// fetch: a cross-customer id answers exactly like a missing one, so the
// portal cannot be used to probe which order ids exist.
func TestPortalFindOrderByIdAndEmail_Ownership(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := time.Now().Format("150405.000")
	owner := "portal-fowner-" + suffix + "@example.com"
	stranger := "portal-fother-" + suffix + "@example.com"

	o := portalTestOrder(t, s, ctx, owner, suffix+"A", model.OrderStatusPaid)
	defer func() {
		_, _ = s.DB.NewRaw("DELETE FROM order_items WHERE order_id = ?", o.ID).Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM orders WHERE id = ?", o.ID).Exec(ctx)
	}()

	got, err := s.FindOrderByIdAndEmail(ctx, o.ID, strings.ToUpper(owner))
	if err != nil {
		t.Fatalf("owner fetch: %v", err)
	}
	if got.ID != o.ID || len(got.Items) != 1 {
		t.Fatalf("owner fetch: id=%s items=%d, want %s with 1 item", got.ID, len(got.Items), o.ID)
	}

	if _, err := s.FindOrderByIdAndEmail(ctx, o.ID, stranger); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-customer fetch: err=%v, want sql.ErrNoRows", err)
	}
	if _, err := s.FindOrderByIdAndEmail(ctx, "no-such-order", owner); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing fetch: err=%v, want sql.ErrNoRows", err)
	}
	if _, err := s.FindOrderByIdAndEmail(ctx, o.ID, ""); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty-email fetch: err=%v, want sql.ErrNoRows", err)
	}
}

// TestPortalFindInvoiceForEmail_Ownership proves the invoice lookup is
// gated by the owning order's customer — the invoice row alone grants
// nothing.
func TestPortalFindInvoiceForEmail_Ownership(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := time.Now().Format("150405.000")
	owner := "portal-iowner-" + suffix + "@example.com"
	stranger := "portal-iother-" + suffix + "@example.com"

	o := portalTestOrder(t, s, ctx, owner, suffix+"A", model.OrderStatusPaid)
	inv := &model.Invoice{
		OrderID:       o.ID,
		InvoiceNumber: "INV-PT" + suffix,
		Status:        model.InvoiceStatusPaid,
		Currency:      "USD",
		SubtotalMinor: 1000,
		TotalMinor:    1000,
	}
	if err := s.CreateInvoice(ctx, inv); err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	defer func() {
		_, _ = s.DB.NewRaw("DELETE FROM invoices WHERE id = ?", inv.ID).Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM order_items WHERE order_id = ?", o.ID).Exec(ctx)
		_, _ = s.DB.NewRaw("DELETE FROM orders WHERE id = ?", o.ID).Exec(ctx)
	}()

	got, err := s.FindInvoiceForEmail(ctx, inv.ID, strings.ToUpper(owner))
	if err != nil {
		t.Fatalf("owner fetch: %v", err)
	}
	if got.ID != inv.ID || got.OrderID != o.ID || got.TotalMinor != 1000 {
		t.Fatalf("owner fetch: %+v, want invoice %s of order %s", got, inv.ID, o.ID)
	}

	if _, err := s.FindInvoiceForEmail(ctx, inv.ID, stranger); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-customer fetch: err=%v, want sql.ErrNoRows", err)
	}
	if _, err := s.FindInvoiceForEmail(ctx, "no-such-invoice", owner); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing fetch: err=%v, want sql.ErrNoRows", err)
	}
}

// TestPortalDownloadReleases_Gating proves the downloads query serves
// exactly what the license-gated download would: published, non-yanked
// releases with at least one finished upload, honouring the licence's
// maintenance cutoff in the query itself.
func TestPortalDownloadReleases_Gating(t *testing.T) {
	s := setupTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := time.Now().Format("150405.000")

	prod := &model.Product{Name: "Portal Downloads", Slug: "portal-dl-" + suffix, Type: "desktop"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("create product: %v", err)
	}

	// Old release: uploaded windows build, published two days ago.
	oldRel := &model.Release{ProductID: prod.ID, Version: "1.0.0", Channel: model.ReleaseChannelStable, Name: "v1.0.0"}
	if err := s.CreateRelease(ctx, oldRel); err != nil {
		t.Fatalf("create old release: %v", err)
	}
	oldArt := &model.ReleaseArtifact{ReleaseID: oldRel.ID, Platform: "windows-x64", Filename: "old.exe"}
	if err := s.CreateArtifact(ctx, oldArt); err != nil {
		t.Fatalf("create old artifact: %v", err)
	}
	if err := s.UpdateArtifactFile(ctx, oldArt.ID, "k-old", 100, "sha-old", "application/octet-stream"); err != nil {
		t.Fatalf("upload old artifact: %v", err)
	}
	if err := s.PublishRelease(ctx, oldRel.ID, false, ""); err != nil {
		t.Fatalf("publish old release: %v", err)
	}
	oldWhen := time.Now().Add(-48 * time.Hour)
	if _, err := s.DB.NewRaw("UPDATE releases SET published_at = ? WHERE id = ?", oldWhen, oldRel.ID).Exec(ctx); err != nil {
		t.Fatalf("backdate old release: %v", err)
	}

	// New beta release: uploaded builds for two platforms, published now.
	newRel := &model.Release{ProductID: prod.ID, Version: "1.1.0-beta.1", Channel: model.ReleaseChannelBeta, Name: "v1.1.0-beta.1"}
	if err := s.CreateRelease(ctx, newRel); err != nil {
		t.Fatalf("create new release: %v", err)
	}
	for _, plat := range []string{"windows-x64", "darwin-arm64"} {
		a := &model.ReleaseArtifact{ReleaseID: newRel.ID, Platform: plat, Filename: plat + ".zip"}
		if err := s.CreateArtifact(ctx, a); err != nil {
			t.Fatalf("create %s artifact: %v", plat, err)
		}
		if err := s.UpdateArtifactFile(ctx, a.ID, "k-"+plat, 200, "sha-"+plat, "application/octet-stream"); err != nil {
			t.Fatalf("upload %s artifact: %v", plat, err)
		}
	}
	if err := s.PublishRelease(ctx, newRel.ID, false, ""); err != nil {
		t.Fatalf("publish new release: %v", err)
	}

	// Draft release with an upload: never downloadable.
	draftRel := &model.Release{ProductID: prod.ID, Version: "2.0.0", Channel: model.ReleaseChannelStable}
	if err := s.CreateRelease(ctx, draftRel); err != nil {
		t.Fatalf("create draft release: %v", err)
	}
	draftArt := &model.ReleaseArtifact{ReleaseID: draftRel.ID, Platform: "windows-x64"}
	if err := s.CreateArtifact(ctx, draftArt); err != nil {
		t.Fatalf("create draft artifact: %v", err)
	}
	if err := s.UpdateArtifactFile(ctx, draftArt.ID, "k-draft", 300, "sha-draft", "application/octet-stream"); err != nil {
		t.Fatalf("upload draft artifact: %v", err)
	}

	// Published but every upload unfinished: offers nothing.
	emptyRel := &model.Release{ProductID: prod.ID, Version: "2.1.0", Channel: model.ReleaseChannelStable}
	if err := s.CreateRelease(ctx, emptyRel); err != nil {
		t.Fatalf("create empty release: %v", err)
	}
	if err := s.CreateArtifact(ctx, &model.ReleaseArtifact{ReleaseID: emptyRel.ID, Platform: "windows-x64"}); err != nil {
		t.Fatalf("create empty artifact: %v", err)
	}
	// Straight to published: the publish gate forbids this state, so
	// write it the way a hand-edited database would.
	if _, err := s.DB.NewRaw("UPDATE releases SET status = ?, published_at = now() WHERE id = ?",
		model.ReleaseStatusPublished, emptyRel.ID).Exec(ctx); err != nil {
		t.Fatalf("force-publish empty release: %v", err)
	}

	// Yanked release: pulled from everything a customer sees.
	yankedRel := &model.Release{ProductID: prod.ID, Version: "2.2.0", Channel: model.ReleaseChannelStable}
	if err := s.CreateRelease(ctx, yankedRel); err != nil {
		t.Fatalf("create yanked release: %v", err)
	}
	yArt := &model.ReleaseArtifact{ReleaseID: yankedRel.ID, Platform: "windows-x64"}
	if err := s.CreateArtifact(ctx, yArt); err != nil {
		t.Fatalf("create yanked artifact: %v", err)
	}
	if err := s.UpdateArtifactFile(ctx, yArt.ID, "k-yank", 400, "sha-yank", "application/octet-stream"); err != nil {
		t.Fatalf("upload yanked artifact: %v", err)
	}
	if err := s.PublishRelease(ctx, yankedRel.ID, false, ""); err != nil {
		t.Fatalf("publish yanked release: %v", err)
	}
	if err := s.YankRelease(ctx, yankedRel.ID, "test"); err != nil {
		t.Fatalf("yank release: %v", err)
	}

	defer func() {
		for _, id := range []string{oldRel.ID, newRel.ID, draftRel.ID, emptyRel.ID, yankedRel.ID} {
			_, _ = s.DB.NewRaw("DELETE FROM release_artifacts WHERE release_id = ?", id).Exec(ctx)
			_, _ = s.DB.NewRaw("DELETE FROM releases WHERE id = ?", id).Exec(ctx)
		}
		_, _ = s.DB.NewRaw("DELETE FROM products WHERE id = ?", prod.ID).Exec(ctx)
	}()

	// Everything entitled: the two real releases, newest first.
	rows, err := s.ListPortalDownloadReleases(ctx, prod.ID, nil, "", "", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("list: %d releases, want 2 (got %+v)", len(rows), releaseVersions(rows))
	}
	if rows[0].ID != newRel.ID || rows[1].ID != oldRel.ID {
		t.Fatalf("list order: %v, want [1.1.0-beta.1 1.0.0]", releaseVersions(rows))
	}
	if len(rows[1].Artifacts) != 1 || !rows[1].Artifacts[0].IsUploaded() {
		t.Fatalf("old release artifacts not preloaded: %+v", rows[1].Artifacts)
	}

	// Maintenance cutoff in the past hides the newer release — and it
	// hides it in SQL, not after the fact.
	cut := time.Now().Add(-24 * time.Hour)
	rows, err = s.ListPortalDownloadReleases(ctx, prod.ID, &cut, "", "", 10)
	if err != nil {
		t.Fatalf("cutoff list: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != oldRel.ID {
		t.Fatalf("cutoff list: %v, want only 1.0.0", releaseVersions(rows))
	}

	// Platform filter selects releases that ship for it.
	rows, err = s.ListPortalDownloadReleases(ctx, prod.ID, nil, "", "darwin-arm64", 10)
	if err != nil {
		t.Fatalf("platform list: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != newRel.ID {
		t.Fatalf("platform list: %v, want only 1.1.0-beta.1", releaseVersions(rows))
	}

	// Channel filter.
	rows, err = s.ListPortalDownloadReleases(ctx, prod.ID, nil, model.ReleaseChannelBeta, "", 10)
	if err != nil {
		t.Fatalf("channel list: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != newRel.ID {
		t.Fatalf("channel list: %v, want only the beta", releaseVersions(rows))
	}

	// A product with nothing entitled answers empty, not an error.
	rows, err = s.ListPortalDownloadReleases(ctx, "no-such-product", nil, "", "", 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("unknown product: %d rows err=%v, want none", len(rows), err)
	}
}

func releaseVersions(rows []*model.Release) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.Version
	}
	return out
}
