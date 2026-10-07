package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// portalCommerceTestDB opens the shared integration store, skipping
// when no test database is configured (the house pattern: these tests
// must pass with go test on a machine with no Postgres).
func portalCommerceTestDB(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := store.New(dsn)
	if err != nil {
		t.Skipf("skipping integration test: %v", err)
	}
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations failed: %v", err)
	}
	return s
}

// portalCommerceCtx builds the gin context a session-authed portal
// request arrives with: middleware.SessionAuth leaves the email claim
// in the context under "email".
func portalCommerceCtx(t *testing.T, target, email string, params gin.Params) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	if email != "" {
		c.Set("email", email)
	}
	c.Params = params
	return w, c
}

// portalCommerceBody is the response envelope the portal commerce
// endpoints answer with.
type portalCommerceBody struct {
	Success bool `json:"success"`
	Data    struct {
		Orders []struct {
			ID string `json:"id"`
		} `json:"orders"`
		Total     int                  `json:"total"`
		Limit     int                  `json:"limit"`
		Offset    int                  `json:"offset"`
		Order     *model.Order         `json:"order"`
		Invoices  []model.Invoice      `json:"invoices"`
		Invoice   *model.Invoice       `json:"invoice"`
		Downloads []portalDownloadView `json:"downloads"`
	} `json:"data"`
	Error *struct {
		Code string `json:"code"`
	} `json:"error"`
}

func portalCommerceDecode(t *testing.T, w *httptest.ResponseRecorder) portalCommerceBody {
	t.Helper()
	var body portalCommerceBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response %q: %v", w.Body.String(), err)
	}
	return body
}

// The status filter is a closed vocabulary shared with the admin list;
// a typo is refused, not silently widened to "all".
func TestPortalCommerceOrderStatusOK(t *testing.T) {
	for _, ok := range []string{"", model.OrderStatusPending, model.OrderStatusPaid,
		model.OrderStatusFailed, model.OrderStatusRefunded} {
		if !portalOrderStatusOK(ok) {
			t.Errorf("status %q must be accepted", ok)
		}
	}
	for _, bad := range []string{"PAID", "settled", "refunded ", "x"} {
		if portalOrderStatusOK(bad) {
			t.Errorf("status %q must be refused", bad)
		}
	}
}

// Entitlement mirrors the /license/download gate: usable licence,
// product that ships installable releases, maintenance cutoff honoured
// — and per product the most generous of the customer's licences wins.
func TestPortalCommerceEntitlements(t *testing.T) {
	now := time.Now()
	past := now.Add(-24 * time.Hour)
	future := now.Add(24 * time.Hour)
	longAgo := now.Add(-720 * time.Hour)

	perpetual := &model.Plan{ID: "pl-perp", LicenseType: "perpetual", GraceDays: 7}
	subPlan := &model.Plan{ID: "pl-sub", LicenseType: "subscription", GraceDays: 7}
	desktop := &model.Product{ID: "p-desktop", Name: "App", Slug: "app", Type: "desktop"}
	saas := &model.Product{ID: "p-saas", Name: "Cloud", Slug: "cloud", Type: "saas"}
	other := &model.Product{ID: "p-other", Name: "Tools", Slug: "tools", Type: "hybrid"}

	lic := func(id string, prod *model.Product, plan *model.Plan, status string, validUntil, updatesUntil *time.Time) *model.License {
		return &model.License{
			ID: id, ProductID: prod.ID, Product: prod,
			PlanID: plan.ID, Plan: plan,
			Status: status, ValidUntil: validUntil, UpdatesUntil: updatesUntil,
		}
	}

	lics := []*model.License{
		lic("l-revoked", desktop, perpetual, model.StatusRevoked, nil, nil),      // unusable
		lic("l-saas", saas, subPlan, model.StatusActive, nil, nil),               // no installable releases
		lic("l-dead", desktop, perpetual, model.StatusActive, &longAgo, nil),     // expired past grace
		lic("l-old-cut", desktop, perpetual, model.StatusActive, nil, &past),     // cutoff yesterday
		lic("l-unlimited", desktop, perpetual, model.StatusActive, nil, nil),     // updates for life
		lic("l-later-cut", desktop, perpetual, model.StatusActive, nil, &future), // cutoff tomorrow
		lic("l-sub", other, subPlan, model.StatusActive, nil, &future),           // subscription follows valid_until
	}

	ents := portalEntitlements(lics)
	if len(ents) != 2 {
		t.Fatalf("entitlements: %d entries, want 2 (desktop + hybrid): %+v", len(ents), ents)
	}
	byProduct := map[string]portalEntitlement{}
	for _, e := range ents {
		byProduct[e.Product.ID] = e
	}

	d, ok := byProduct[desktop.ID]
	if !ok {
		t.Fatalf("desktop product missing from entitlements")
	}
	// The most generous of three desktop licences wins: updates for
	// life, and the licence id travels with it.
	if d.LicenseID != "l-unlimited" {
		t.Errorf("desktop entitling licence = %s, want l-unlimited", d.LicenseID)
	}
	if d.UpdatesUntil != nil {
		t.Errorf("desktop cutoff = %v, want nil (updates for life)", d.UpdatesUntil)
	}

	o, ok := byProduct[other.ID]
	if !ok {
		t.Fatalf("hybrid product missing from entitlements")
	}
	if o.LicenseID != "l-sub" || o.UpdatesUntil != nil {
		t.Errorf("subscription licence: id=%s cutoff=%v, want l-sub with nil cutoff",
			o.LicenseID, o.UpdatesUntil)
	}
}

// A later-limited licence must not drag an unlimited one down: only
// when every licence carries a cutoff does the latest apply.
func TestPortalCommerceEntitlements_LatestCutoffWins(t *testing.T) {
	past := time.Now().Add(-24 * time.Hour)
	future := time.Now().Add(24 * time.Hour)
	plan := &model.Plan{ID: "pl", LicenseType: "perpetual", GraceDays: 7}
	prod := &model.Product{ID: "p", Name: "App", Slug: "app", Type: "desktop"}
	mk := func(id string, until *time.Time) *model.License {
		return &model.License{
			ID: id, ProductID: prod.ID, Product: prod, PlanID: plan.ID, Plan: plan,
			Status: model.StatusActive, UpdatesUntil: until,
		}
	}
	ents := portalEntitlements([]*model.License{mk("l-early", &past), mk("l-late", &future)})
	if len(ents) != 1 || ents[0].LicenseID != "l-late" || ents[0].UpdatesUntil == nil || !ents[0].UpdatesUntil.Equal(future) {
		t.Fatalf("cutoff merge: %+v, want l-late with the future cutoff", ents)
	}
}

// Download rows carry metadata and pointers into the EXISTING download
// flow — never a credential, never a freshly minted URL.
func TestPortalCommerceDownloadRows(t *testing.T) {
	pub := time.Now().Add(-time.Hour)
	prod := &model.Product{ID: "p1", Name: "App", Slug: "app", DownloadURL: "https://example.com/download"}
	ent := portalEntitlement{LicenseID: "lic1", Product: prod}
	rel := &model.Release{
		Version: "1.2.3", Channel: model.ReleaseChannelStable, PublishedAt: &pub,
		Artifacts: []*model.ReleaseArtifact{
			{Platform: "windows-x64", Filename: "app.exe", FileKey: "k1", FileSize: 10, SHA256: "s1"},
			{Platform: "darwin-arm64", Filename: "", FileKey: "k2", FileSize: 20, SHA256: "s2"},
			{Platform: "linux-x64", Filename: "app.AppImage"}, // upload never finished
		},
	}

	rows := portalDownloadRows(ent, []*model.Release{rel}, "")
	if len(rows) != 2 {
		t.Fatalf("rows: %d, want 2 (unfinished upload skipped): %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.ProductName != "App" || r.Version != "1.2.3" {
			t.Errorf("row %q/%q, want App/1.2.3", r.ProductName, r.Version)
		}
		if r.DownloadURL != "https://example.com/download" {
			t.Errorf("download_url = %q, want the product's stable page", r.DownloadURL)
		}
		if r.LicenseID != "lic1" {
			t.Errorf("license_id = %q, want lic1", r.LicenseID)
		}
		if r.Filename == "" {
			t.Errorf("row %s must carry a filename", r.Platform)
		}
	}
	if rows[0].Filename != "app.exe" {
		t.Errorf("uploaded filename = %q, want app.exe", rows[0].Filename)
	}

	filtered := portalDownloadRows(ent, []*model.Release{rel}, "windows-x64")
	if len(filtered) != 1 || filtered[0].Platform != "windows-x64" {
		t.Fatalf("platform filter: %+v, want only windows-x64", filtered)
	}
}

// Auth and validation answer before any database is touched — a
// session with no email is a 401, a bad status filter a 400.
func TestPortalCommerceHandlers_Guards(t *testing.T) {
	h := NewPortalCommerceHandler(nil)

	w, c := portalCommerceCtx(t, "/portal/orders", "", nil)
	h.ListOrders(c)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no session email: %d, want 401", w.Code)
	}

	w, c = portalCommerceCtx(t, "/portal/orders?status=bogus", "a@example.com", nil)
	h.ListOrders(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad status: %d, want 400", w.Code)
	}

	w, c = portalCommerceCtx(t, "/portal/downloads?channel=nightly", "a@example.com", nil)
	h.ListDownloads(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad channel: %d, want 400", w.Code)
	}

	w, c = portalCommerceCtx(t, "/portal/downloads?platform=vms", "a@example.com", nil)
	h.ListDownloads(c)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad platform: %d, want 400", w.Code)
	}
}

func portalCommerceSeedOrder(t *testing.T, s *store.Store, ctx context.Context, email, suffix, status string) (*model.Order, *model.Invoice) {
	t.Helper()
	o := &model.Order{
		OrderNumber:   "HTC-PH" + suffix,
		CustomerEmail: email,
		Currency:      "USD",
		SubtotalMinor: 5000,
		TotalMinor:    5000,
		Status:        status,
		Items: []*model.OrderItem{{
			SKU: "SKU-" + suffix, Quantity: 1,
			UnitAmountMinor: 5000, LineSubtotalMinor: 5000, LineTotalMinor: 5000,
		}},
	}
	if err := s.CreateOrder(ctx, o); err != nil {
		t.Fatalf("create order: %v", err)
	}
	var inv *model.Invoice
	if status == model.OrderStatusPaid {
		inv = &model.Invoice{
			OrderID: o.ID, InvoiceNumber: "INV-PH" + suffix,
			Status: model.InvoiceStatusPaid, Currency: "USD",
			SubtotalMinor: 5000, TotalMinor: 5000,
		}
		if err := s.CreateInvoice(ctx, inv); err != nil {
			t.Fatalf("create invoice: %v", err)
		}
	}
	return o, inv
}

// The full ownership surface: the portal lists and reads only the
// session customer's orders and invoices, and a cross-customer id is
// indistinguishable from a missing one — 404 either way, no leak.
func TestPortalCommerceHandlers_OrdersOwnership(t *testing.T) {
	s := portalCommerceTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := time.Now().Format("150405.000")
	owner := "portal-howner-" + suffix + "@example.com"
	stranger := "portal-hother-" + suffix + "@example.com"

	mine, myInv := portalCommerceSeedOrder(t, s, ctx, owner, suffix+"A", model.OrderStatusPaid)
	mine2, _ := portalCommerceSeedOrder(t, s, ctx, owner, suffix+"B", model.OrderStatusPending)
	theirs, _ := portalCommerceSeedOrder(t, s, ctx, stranger, suffix+"C", model.OrderStatusPaid)
	defer func() {
		for _, id := range []string{mine.ID, mine2.ID, theirs.ID} {
			_, _ = s.DB.NewRaw("DELETE FROM invoices WHERE order_id = ?", id).Exec(ctx)
			_, _ = s.DB.NewRaw("DELETE FROM order_items WHERE order_id = ?", id).Exec(ctx)
			_, _ = s.DB.NewRaw("DELETE FROM orders WHERE id = ?", id).Exec(ctx)
		}
	}()

	h := NewPortalCommerceHandler(s)
	p := gin.Params{{Key: "id", Value: mine.ID}}

	// Own order: 200 with items and its invoice, and the shape the
	// admin endpoint answers with ({order, invoices}).
	w, c := portalCommerceCtx(t, "/portal/orders/"+mine.ID, strings.ToUpper(owner), p)
	h.GetOrder(c)
	body := portalCommerceDecode(t, w)
	if w.Code != http.StatusOK || !body.Success {
		t.Fatalf("own GetOrder: %d %s", w.Code, w.Body.String())
	}
	if body.Data.Order == nil || body.Data.Order.ID != mine.ID || len(body.Data.Order.Items) != 1 {
		t.Fatalf("own GetOrder payload: %+v", body.Data.Order)
	}
	if len(body.Data.Invoices) != 1 || body.Data.Invoices[0].ID != myInv.ID {
		t.Fatalf("own GetOrder invoices: %+v", body.Data.Invoices)
	}

	// Cross-customer: the SAME 404 as a missing id.
	w, c = portalCommerceCtx(t, "/portal/orders/"+mine.ID, stranger, p)
	h.GetOrder(c)
	body = portalCommerceDecode(t, w)
	if w.Code != http.StatusNotFound || body.Error == nil || body.Error.Code != "NOT_FOUND" {
		t.Fatalf("cross GetOrder: %d %s, want 404 NOT_FOUND", w.Code, w.Body.String())
	}

	w, c = portalCommerceCtx(t, "/portal/orders/no-such-order", owner, gin.Params{{Key: "id", Value: "no-such-order"}})
	h.GetOrder(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing GetOrder: %d, want 404", w.Code)
	}

	// List: own orders only, paginated like every other list endpoint.
	w, c = portalCommerceCtx(t, "/portal/orders?limit=1", owner, nil)
	h.ListOrders(c)
	body = portalCommerceDecode(t, w)
	if w.Code != http.StatusOK || body.Data.Total != 2 || len(body.Data.Orders) != 1 {
		t.Fatalf("ListOrders limit=1: total=%d rows=%d, want 2/1", body.Data.Total, len(body.Data.Orders))
	}
	firstID := body.Data.Orders[0].ID
	if firstID == theirs.ID {
		t.Fatalf("ListOrders leaked a stranger's order")
	}
	w, c = portalCommerceCtx(t, "/portal/orders?limit=1&offset=1", owner, nil)
	h.ListOrders(c)
	body = portalCommerceDecode(t, w)
	if body.Data.Total != 2 || len(body.Data.Orders) != 1 || body.Data.Orders[0].ID == firstID {
		t.Fatalf("ListOrders page 2: %+v, want the other order", body.Data.Orders)
	}

	// Status filter.
	w, c = portalCommerceCtx(t, "/portal/orders?status=pending", owner, nil)
	h.ListOrders(c)
	body = portalCommerceDecode(t, w)
	if body.Data.Total != 1 || body.Data.Orders[0].ID != mine2.ID {
		t.Fatalf("ListOrders status=pending: %+v total=%d, want only the pending one", body.Data.Orders, body.Data.Total)
	}

	// Invoices of one own order — and the same 404 across customers.
	w, c = portalCommerceCtx(t, "/portal/orders/"+mine.ID+"/invoices", owner, p)
	h.ListInvoices(c)
	body = portalCommerceDecode(t, w)
	if w.Code != http.StatusOK || len(body.Data.Invoices) != 1 || body.Data.Invoices[0].ID != myInv.ID {
		t.Fatalf("own ListInvoices: %d %+v", w.Code, body.Data.Invoices)
	}
	w, c = portalCommerceCtx(t, "/portal/orders/"+mine.ID+"/invoices", stranger, p)
	h.ListInvoices(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross ListInvoices: %d, want 404", w.Code)
	}

	// Invoice detail carries the owning order's line items.
	ip := gin.Params{{Key: "id", Value: myInv.ID}}
	w, c = portalCommerceCtx(t, "/portal/invoices/"+myInv.ID, strings.ToUpper(owner), ip)
	h.GetInvoice(c)
	body = portalCommerceDecode(t, w)
	if w.Code != http.StatusOK || body.Data.Invoice == nil || body.Data.Invoice.ID != myInv.ID {
		t.Fatalf("own GetInvoice: %d %+v", w.Code, body.Data.Invoice)
	}
	if body.Data.Order == nil || body.Data.Order.ID != mine.ID || len(body.Data.Order.Items) != 1 {
		t.Fatalf("own GetInvoice order: %+v, want %s with 1 item", body.Data.Order, mine.ID)
	}
	w, c = portalCommerceCtx(t, "/portal/invoices/"+myInv.ID, stranger, ip)
	h.GetInvoice(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross GetInvoice: %d, want 404", w.Code)
	}
	w, c = portalCommerceCtx(t, "/portal/invoices/no-such-invoice", owner, gin.Params{{Key: "id", Value: "no-such-invoice"}})
	h.GetInvoice(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing GetInvoice: %d, want 404", w.Code)
	}
}

// Downloads are entitled by the customer's OWN licences — nobody sees
// artifacts their licences do not cover — and the rows carry product
// name, version and the product's stable download page.
func TestPortalCommerceHandlers_Downloads(t *testing.T) {
	s := portalCommerceTestDB(t)
	defer s.Close()
	ctx := context.Background()
	suffix := time.Now().Format("150405.000")

	prod := &model.Product{
		Name: "Portal DL App", Slug: "portal-dlh-" + suffix, Type: "desktop",
		DownloadURL: "https://example.com/portal-dl",
	}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("create product: %v", err)
	}
	plan := &model.Plan{
		ProductID: prod.ID, Name: "Perpetual", Slug: "perp-" + suffix,
		LicenseType: "perpetual", LicenseModel: "standard",
	}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	owner := "portal-dlowner-" + suffix + "@example.com"
	lic := &model.License{
		ProductID: prod.ID, PlanID: plan.ID,
		Email: owner, LicenseKey: "KEY-PH-" + suffix, Status: model.StatusActive,
	}
	if err := s.CreateLicense(ctx, lic); err != nil {
		t.Fatalf("create license: %v", err)
	}

	rel := &model.Release{ProductID: prod.ID, Version: "3.1.4", Channel: model.ReleaseChannelStable, Name: "v3.1.4"}
	if err := s.CreateRelease(ctx, rel); err != nil {
		t.Fatalf("create release: %v", err)
	}
	art := &model.ReleaseArtifact{ReleaseID: rel.ID, Platform: "windows-x64", Filename: "app.exe"}
	if err := s.CreateArtifact(ctx, art); err != nil {
		t.Fatalf("create artifact: %v", err)
	}
	if err := s.UpdateArtifactFile(ctx, art.ID, "k-ph", 42, "sha-ph", "application/octet-stream"); err != nil {
		t.Fatalf("upload artifact: %v", err)
	}
	if err := s.PublishRelease(ctx, rel.ID, false, ""); err != nil {
		t.Fatalf("publish release: %v", err)
	}

	h := NewPortalCommerceHandler(s)

	w, c := portalCommerceCtx(t, "/portal/downloads", strings.ToUpper(owner), nil)
	h.ListDownloads(c)
	body := portalCommerceDecode(t, w)
	if w.Code != http.StatusOK || !body.Success {
		t.Fatalf("ListDownloads: %d %s", w.Code, w.Body.String())
	}
	if len(body.Data.Downloads) != 1 {
		t.Fatalf("ListDownloads rows: %+v, want 1", body.Data.Downloads)
	}
	row := body.Data.Downloads[0]
	if row.ProductName != "Portal DL App" || row.Version != "3.1.4" || row.Platform != "windows-x64" {
		t.Fatalf("download row: %+v", row)
	}
	if row.DownloadURL != "https://example.com/portal-dl" {
		t.Fatalf("download_url = %q, want the product's stable page", row.DownloadURL)
	}
	if row.LicenseID != lic.ID {
		t.Fatalf("license_id = %q, want %s", row.LicenseID, lic.ID)
	}
	if row.SHA256 != "sha-ph" || row.FileSize != 42 || row.Filename != "app.exe" {
		t.Fatalf("artifact metadata: %+v", row)
	}

	// Platform narrowing: this release ships no macOS build.
	w, c = portalCommerceCtx(t, "/portal/downloads?platform=darwin-arm64", owner, nil)
	h.ListDownloads(c)
	body = portalCommerceDecode(t, w)
	if len(body.Data.Downloads) != 0 {
		t.Fatalf("darwin filter: %+v, want none", body.Data.Downloads)
	}

	// A customer with no licence for the product downloads nothing.
	w, c = portalCommerceCtx(t, "/portal/downloads", "portal-dlnone-"+suffix+"@example.com", nil)
	h.ListDownloads(c)
	body = portalCommerceDecode(t, w)
	if w.Code != http.StatusOK || len(body.Data.Downloads) != 0 {
		t.Fatalf("unentitled customer: %d %+v, want none", w.Code, body.Data.Downloads)
	}
}
