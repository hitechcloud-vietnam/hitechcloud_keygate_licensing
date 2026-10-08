package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// fakeOrderAdminStore stands in for store.Store so the PO/invoice
// workflow's refusal paths — invalid billing input, illegal invoice
// transitions — run without a database. It records what the handler
// wrote and what it audited.
type fakeOrderAdminStore struct {
	orders      map[string]*model.Order
	invoices    map[string]*model.Invoice
	billingErr  error
	invoiceErr  error
	billingWise int
	lastBilling *model.Order
	audits      []*model.AuditLog
	lastSort    store.Sort
}

func newFakeOrderAdminStore() *fakeOrderAdminStore {
	return &fakeOrderAdminStore{
		orders:   map[string]*model.Order{},
		invoices: map[string]*model.Invoice{},
	}
}

func (f *fakeOrderAdminStore) ListOrders(_ context.Context, _, _ string, _ store.Page, sort store.Sort) ([]*model.Order, int, error) {
	f.lastSort = sort
	var out []*model.Order
	for _, o := range f.orders {
		out = append(out, o)
	}
	return out, len(out), nil
}

func (f *fakeOrderAdminStore) FindOrderByID(_ context.Context, id string) (*model.Order, error) {
	if o, ok := f.orders[id]; ok {
		cp := *o // a copy, like a real row read
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeOrderAdminStore) UpdateOrderStatus(_ context.Context, id, status string, paidAt, refundedAt *time.Time) error {
	o, ok := f.orders[id]
	if !ok {
		return sql.ErrNoRows
	}
	o.Status = status
	if paidAt != nil {
		o.PaidAt = paidAt
	}
	if refundedAt != nil {
		o.RefundedAt = refundedAt
	}
	return nil
}

func (f *fakeOrderAdminStore) UpdateOrderBilling(_ context.Context, id string, o *model.Order) error {
	if f.billingErr != nil {
		return f.billingErr
	}
	stored, ok := f.orders[id]
	if !ok {
		return sql.ErrNoRows
	}
	f.billingWise++
	f.lastBilling = o
	stored.BillingName = o.BillingName
	stored.BillingCompany = o.BillingCompany
	stored.BillingAddressLine1 = o.BillingAddressLine1
	stored.BillingAddressLine2 = o.BillingAddressLine2
	stored.BillingCity = o.BillingCity
	stored.BillingRegion = o.BillingRegion
	stored.BillingPostalCode = o.BillingPostalCode
	stored.BillingCountry = o.BillingCountry
	stored.CustomerTaxID = o.CustomerTaxID
	stored.PONumber = o.PONumber
	stored.BillingEmail = o.BillingEmail
	return nil
}

func (f *fakeOrderAdminStore) ListInvoicesByOrder(_ context.Context, orderID string, sort store.Sort) ([]*model.Invoice, error) {
	f.lastSort = sort
	var out []*model.Invoice
	for _, inv := range f.invoices {
		if inv.OrderID == orderID {
			out = append(out, inv)
		}
	}
	return out, nil
}

func (f *fakeOrderAdminStore) FindInvoiceByID(_ context.Context, id string) (*model.Invoice, error) {
	if inv, ok := f.invoices[id]; ok {
		cp := *inv
		return &cp, nil
	}
	return nil, sql.ErrNoRows
}

func (f *fakeOrderAdminStore) UpdateInvoiceStatus(_ context.Context, id, status string, voidedAt, uncollectibleAt *time.Time) error {
	if f.invoiceErr != nil {
		return f.invoiceErr
	}
	inv, ok := f.invoices[id]
	if !ok {
		return sql.ErrNoRows
	}
	inv.Status = status
	if voidedAt != nil {
		inv.VoidedAt = voidedAt
	}
	if uncollectibleAt != nil {
		inv.UncollectibleAt = uncollectibleAt
	}
	return nil
}

func (f *fakeOrderAdminStore) Audit(_ context.Context, log *model.AuditLog) {
	f.audits = append(f.audits, log)
}

func poAdminCtx(t *testing.T, method, target, body string, params gin.Params) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, target, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = params
	return w, c
}

// poData decodes a success envelope down to the data object.
func poData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var env struct {
		Success bool            `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v\nbody: %s", err, w.Body.String())
	}
	if !env.Success {
		t.Fatalf("expected success, got error %+v\nbody: %s", env.Error, w.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(env.Data, &m); err != nil {
		t.Fatalf("decode data: %v\nbody: %s", err, w.Body.String())
	}
	return m
}

// poErr decodes a failure envelope to its error code.
func poErr(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Success bool `json:"success"`
		Error   *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v\nbody: %s", err, w.Body.String())
	}
	if env.Success || env.Error == nil {
		t.Fatalf("expected an error envelope, got: %s", w.Body.String())
	}
	return env.Error.Code
}

func sp(s string) *string { return &s }

// ─── Billing patch: fold, clear, merge ───

// What is stored is canonical whatever the admin typed: trimmed text,
// an upper-cased country. A whitespace-only value clears, and a field
// the body omits keeps its current value.
func TestApplyBillingPatch_FoldsClearsAndMerges(t *testing.T) {
	o := &model.Order{
		BillingName:    "Old Name",
		BillingCity:    "Old City",
		PONumber:       "PO-KEEP",
		BillingCountry: "US",
	}
	p := &billingPatch{
		BillingName:    sp("  Ada Lovelace  "),
		BillingCountry: sp("vn"),
		BillingCity:    sp("   "), // whitespace-only: clear
		BillingEmail:   sp("ap@example.com"),
	}
	if err := applyBillingPatch(o, p); err != nil {
		t.Fatalf("applyBillingPatch: unexpected err %v", err)
	}
	if o.BillingName != "Ada Lovelace" {
		t.Errorf("BillingName = %q, want %q", o.BillingName, "Ada Lovelace")
	}
	if o.BillingCountry != "VN" {
		t.Errorf("BillingCountry = %q, want %q", o.BillingCountry, "VN")
	}
	if o.BillingCity != "" {
		t.Errorf("BillingCity = %q, want cleared", o.BillingCity)
	}
	if o.BillingEmail != "ap@example.com" {
		t.Errorf("BillingEmail = %q, want %q", o.BillingEmail, "ap@example.com")
	}
	// Absent (nil) fields keep their value — this is a merge.
	if o.PONumber != "PO-KEEP" {
		t.Errorf("PONumber = %q, want %q (absent field must be left alone)", o.PONumber, "PO-KEEP")
	}
}

// What the endpoint refuses: every shape and length rule in one table.
// All of these are 400s an admin can fix, never a 500.
func TestApplyBillingPatch_Validation(t *testing.T) {
	cases := []struct {
		name    string
		p       billingPatch
		wantErr bool
	}{
		{"country 3 letters", billingPatch{BillingCountry: sp("USA")}, true},
		{"country digit", billingPatch{BillingCountry: sp("V1")}, true},
		{"country space", billingPatch{BillingCountry: sp("V N")}, true},
		{"country lowercase ok (folded)", billingPatch{BillingCountry: sp("vn")}, false},
		{"tax id 7 chars", billingPatch{CustomerTaxID: sp("AB12345")}, true},
		{"tax id 21 chars", billingPatch{CustomerTaxID: sp(strings.Repeat("A", 21))}, true},
		{"tax id spaces", billingPatch{CustomerTaxID: sp("VN 123 456")}, true},
		{"tax id underscore", billingPatch{CustomerTaxID: sp("VN1234_5678")}, true},
		{"tax id 8 chars ok", billingPatch{CustomerTaxID: sp("AB123456")}, false},
		{"tax id 20 chars ok", billingPatch{CustomerTaxID: sp(strings.Repeat("A", 20))}, false},
		{"po 65 chars", billingPatch{PONumber: sp(strings.Repeat("P", 65))}, true},
		{"po 64 chars ok", billingPatch{PONumber: sp(strings.Repeat("P", 64))}, false},
		{"name 201 chars", billingPatch{BillingName: sp(strings.Repeat("N", 201))}, true},
		{"postal 33 chars", billingPatch{BillingPostalCode: sp(strings.Repeat("1", 33))}, true},
		{"email no at", billingPatch{BillingEmail: sp("not-an-email")}, true},
		{"email with spaces", billingPatch{BillingEmail: sp("a b@example.com")}, true},
		{"email display name", billingPatch{BillingEmail: sp("AP <ap@example.com>")}, true},
		{"email ok", billingPatch{BillingEmail: sp("ap@example.com")}, false},
		{"clears are always legal", billingPatch{
			BillingName: sp(""), BillingCountry: sp(""),
			CustomerTaxID: sp(""), BillingEmail: sp(""),
		}, false},
	}
	for _, tc := range cases {
		err := applyBillingPatch(&model.Order{}, &tc.p)
		if tc.wantErr && err == nil {
			t.Errorf("%s: accepted, want refusal", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: refused with %v, want accepted", tc.name, err)
		}
	}
}

// ─── PATCH /admin/orders/:id/billing ───

func poSeedOrder(f *fakeOrderAdminStore, id string) *model.Order {
	o := &model.Order{
		ID:            id,
		OrderNumber:   "HTC-" + id,
		CustomerEmail: "buyer@example.com",
		Currency:      "USD",
		Status:        model.OrderStatusPaid,
	}
	f.orders[id] = o
	return o
}

// The happy path: the whole block goes in, the response carries the
// snake_case keys the rest of the ledger uses, and an audit row names
// the change.
func TestAdminUpdateBilling_SetsBlockAndAnswersNewKeys(t *testing.T) {
	f := newFakeOrderAdminStore()
	poSeedOrder(f, "ord-1")
	h := &OrderAdminHandler{Store: f}
	w, c := poAdminCtx(t, "PATCH", "/admin/orders/ord-1/billing", `{
		"billing_name": "Ada Lovelace",
		"billing_company": "Analytical Engines Ltd",
		"billing_address_line1": "123 Computation St",
		"billing_address_line2": "Suite 4",
		"billing_city": "Ho Chi Minh City",
		"billing_region": "Southern Vietnam",
		"billing_postal_code": "70000",
		"billing_country": "vn",
		"customer_tax_id": "VN1234-5678",
		"po_number": "PO-2026-0042",
		"billing_email": "ap@example.com"
	}`, gin.Params{{Key: "id", Value: "ord-1"}})
	h.UpdateBilling(c)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	data := poData(t, w)
	want := map[string]string{
		"billing_name":          "Ada Lovelace",
		"billing_company":       "Analytical Engines Ltd",
		"billing_address_line1": "123 Computation St",
		"billing_address_line2": "Suite 4",
		"billing_city":          "Ho Chi Minh City",
		"billing_region":        "Southern Vietnam",
		"billing_postal_code":   "70000",
		"billing_country":       "VN",
		"customer_tax_id":       "VN1234-5678",
		"po_number":             "PO-2026-0042",
		"billing_email":         "ap@example.com",
	}
	for key, val := range want {
		got, ok := data[key]
		if !ok {
			t.Errorf("response JSON is missing key %q", key)
			continue
		}
		if got != val {
			t.Errorf("response JSON %s = %v, want %q", key, got, val)
		}
	}
	if len(f.audits) != 1 || f.audits[0].Action != "billing_updated" || f.audits[0].EntityID != "ord-1" {
		t.Errorf("audits = %+v, want one billing_updated for ord-1", f.audits)
	}
}

// The clear convention in the wire form: an explicit empty string (or
// whitespace) clears the field — its key vanishes from the response
// JSON — while fields the body omits keep their value.
func TestAdminUpdateBilling_ClearConvention(t *testing.T) {
	f := newFakeOrderAdminStore()
	o := poSeedOrder(f, "ord-1")
	o.BillingName = "Ada Lovelace"
	o.BillingCity = "Ho Chi Minh City"
	o.PONumber = "PO-KEEP"
	h := &OrderAdminHandler{Store: f}
	w, c := poAdminCtx(t, "PATCH", "/admin/orders/ord-1/billing",
		`{"billing_name": "", "billing_city": "   ", "po_number": "PO-9"}`,
		gin.Params{{Key: "id", Value: "ord-1"}})
	h.UpdateBilling(c)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	data := poData(t, w)
	if _, ok := data["billing_name"]; ok {
		t.Errorf("cleared billing_name is still in the response JSON: %v", data["billing_name"])
	}
	if _, ok := data["billing_city"]; ok {
		t.Errorf("cleared billing_city is still in the response JSON: %v", data["billing_city"])
	}
	if got := data["po_number"]; got != "PO-9" {
		t.Errorf("po_number = %v, want %q", got, "PO-9")
	}
	if f.orders["ord-1"].BillingName != "" || f.orders["ord-1"].BillingCity != "" {
		t.Errorf("cleared fields stored non-empty: %+v", f.orders["ord-1"])
	}
}

// Bad input is refused before anything is written: 400, and the store
// never hears about it.
func TestAdminUpdateBilling_RefusesInvalidInput(t *testing.T) {
	cases := []struct{ name, body string }{
		{"bad country", `{"billing_country": "USA"}`},
		{"bad tax id", `{"customer_tax_id": "AB12345"}`},
		{"po too long", `{"po_number": "` + strings.Repeat("P", 65) + `"}`},
		{"bad email", `{"billing_email": "not-an-email"}`},
		{"not json", `{"billing_name":`},
	}
	for _, tc := range cases {
		f := newFakeOrderAdminStore()
		poSeedOrder(f, "ord-1")
		h := &OrderAdminHandler{Store: f}
		w, c := poAdminCtx(t, "PATCH", "/admin/orders/ord-1/billing", tc.body,
			gin.Params{{Key: "id", Value: "ord-1"}})
		h.UpdateBilling(c)

		if w.Code != 400 {
			t.Errorf("%s: status = %d, want 400 (body %s)", tc.name, w.Code, w.Body.String())
			continue
		}
		if code := poErr(t, w); code != "BAD_REQUEST" {
			t.Errorf("%s: error code = %s, want BAD_REQUEST", tc.name, code)
		}
		if f.billingWise != 0 {
			t.Errorf("%s: store was written despite the refusal", tc.name)
		}
	}
}

func TestAdminUpdateBilling_OrderNotFound(t *testing.T) {
	f := newFakeOrderAdminStore()
	h := &OrderAdminHandler{Store: f}
	w, c := poAdminCtx(t, "PATCH", "/admin/orders/no-such-order/billing",
		`{"po_number": "PO-1"}`, gin.Params{{Key: "id", Value: "no-such-order"}})
	h.UpdateBilling(c)

	if w.Code != 404 {
		t.Fatalf("status = %d, want 404 (body %s)", w.Code, w.Body.String())
	}
}

// ─── Invoice state transitions ───

func poSeedInvoice(f *fakeOrderAdminStore, id, orderID, status string) *model.Invoice {
	inv := &model.Invoice{
		ID:            id,
		OrderID:       orderID,
		InvoiceNumber: "INV-" + id,
		Status:        status,
		Currency:      "USD",
	}
	f.invoices[id] = inv
	return inv
}

func TestAdminInvoiceVoid_Guarded(t *testing.T) {
	cases := []struct {
		name       string
		from       string
		wantStatus int
		wantCode   string
	}{
		{"draft can be voided", model.InvoiceStatusDraft, 200, ""},
		{"open can be voided", model.InvoiceStatusOpen, 200, ""},
		// The load-bearing refusal: money changed hands, so the way
		// off paid is the refund, never a void.
		{"paid cannot be voided", model.InvoiceStatusPaid, 409, "INVOICE_NOT_VOIDABLE"},
		{"refunded cannot be voided", model.InvoiceStatusRefunded, 409, "INVOICE_TRANSITION_INVALID"},
		{"uncollectible cannot be voided", model.InvoiceStatusUncollectible, 409, "INVOICE_TRANSITION_INVALID"},
		{"void is terminal", model.InvoiceStatusVoid, 409, "INVOICE_TRANSITION_INVALID"},
	}
	for _, tc := range cases {
		f := newFakeOrderAdminStore()
		poSeedOrder(f, "ord-1")
		poSeedInvoice(f, "inv-1", "ord-1", tc.from)
		h := &OrderAdminHandler{Store: f}
		w, c := poAdminCtx(t, "POST", "/admin/orders/ord-1/invoices/inv-1/void", "",
			gin.Params{{Key: "id", Value: "ord-1"}, {Key: "invoice_id", Value: "inv-1"}})
		h.Void(c)

		if w.Code != tc.wantStatus {
			t.Errorf("%s: status = %d, want %d (body %s)", tc.name, w.Code, tc.wantStatus, w.Body.String())
			continue
		}
		if tc.wantStatus == 200 {
			data := poData(t, w)
			if got := data["status"]; got != model.InvoiceStatusVoid {
				t.Errorf("%s: status = %v, want %q", tc.name, got, model.InvoiceStatusVoid)
			}
			if _, ok := data["voided_at"]; !ok {
				t.Errorf("%s: voided_at missing from the response JSON", tc.name)
			}
			if f.invoices["inv-1"].VoidedAt == nil {
				t.Errorf("%s: voided_at not stamped in the store", tc.name)
			}
			continue
		}
		if code := poErr(t, w); code != tc.wantCode {
			t.Errorf("%s: error code = %s, want %s", tc.name, code, tc.wantCode)
		}
		if f.invoices["inv-1"].Status != tc.from {
			t.Errorf("%s: invoice was written despite the refusal", tc.name)
		}
	}
}

func TestAdminInvoiceMarkUncollectible_Guarded(t *testing.T) {
	cases := []struct {
		name       string
		from       string
		wantStatus int
		wantCode   string
	}{
		{"open can be given up on", model.InvoiceStatusOpen, 200, ""},
		{"draft is not collectible yet", model.InvoiceStatusDraft, 409, "INVOICE_TRANSITION_INVALID"},
		// Collected by definition: a paid invoice is never
		// uncollectible.
		{"paid cannot be uncollectible", model.InvoiceStatusPaid, 409, "INVOICE_TRANSITION_INVALID"},
		{"void is terminal", model.InvoiceStatusVoid, 409, "INVOICE_TRANSITION_INVALID"},
		{"refunded is terminal", model.InvoiceStatusRefunded, 409, "INVOICE_TRANSITION_INVALID"},
	}
	for _, tc := range cases {
		f := newFakeOrderAdminStore()
		poSeedOrder(f, "ord-1")
		poSeedInvoice(f, "inv-1", "ord-1", tc.from)
		h := &OrderAdminHandler{Store: f}
		w, c := poAdminCtx(t, "POST", "/admin/orders/ord-1/invoices/inv-1/mark-uncollectible", "",
			gin.Params{{Key: "id", Value: "ord-1"}, {Key: "invoice_id", Value: "inv-1"}})
		h.MarkUncollectible(c)

		if w.Code != tc.wantStatus {
			t.Errorf("%s: status = %d, want %d (body %s)", tc.name, w.Code, tc.wantStatus, w.Body.String())
			continue
		}
		if tc.wantStatus == 200 {
			data := poData(t, w)
			if got := data["status"]; got != model.InvoiceStatusUncollectible {
				t.Errorf("%s: status = %v, want %q", tc.name, got, model.InvoiceStatusUncollectible)
			}
			if _, ok := data["uncollectible_at"]; !ok {
				t.Errorf("%s: uncollectible_at missing from the response JSON", tc.name)
			}
			continue
		}
		if code := poErr(t, w); code != tc.wantCode {
			t.Errorf("%s: error code = %s, want %s", tc.name, code, tc.wantCode)
		}
	}
}

// An invoice must be drawn on the order the URL names — anything else
// is the same 404 as a missing invoice.
func TestAdminInvoiceTransitions_ScopedToOrder(t *testing.T) {
	f := newFakeOrderAdminStore()
	poSeedOrder(f, "ord-1")
	poSeedOrder(f, "ord-2")
	poSeedInvoice(f, "inv-other", "ord-2", model.InvoiceStatusOpen)
	h := &OrderAdminHandler{Store: f}

	for _, ep := range []string{"void", "mark-uncollectible"} {
		w, c := poAdminCtx(t, "POST", "/admin/orders/ord-1/invoices/inv-other/"+ep, "",
			gin.Params{{Key: "id", Value: "ord-1"}, {Key: "invoice_id", Value: "inv-other"}})
		if ep == "void" {
			h.Void(c)
		} else {
			h.MarkUncollectible(c)
		}
		if w.Code != 404 {
			t.Errorf("%s: foreign invoice answered %d, want 404", ep, w.Code)
		}
	}

	w, c := poAdminCtx(t, "POST", "/admin/orders/ord-1/invoices/no-such/void", "",
		gin.Params{{Key: "id", Value: "ord-1"}, {Key: "invoice_id", Value: "no-such"}})
	h.Void(c)
	if w.Code != 404 {
		t.Errorf("missing invoice answered %d, want 404", w.Code)
	}
}

// ─── List/Get carry the new fields automatically ───

// The existing List/Get responses serialize model.Order / model.Invoice
// directly, so the new keys ride along — this pins their names and
// their presence wherever the ledger has a value.
func TestAdminOrderListAndGet_CarryBillingAndInvoiceKeys(t *testing.T) {
	f := newFakeOrderAdminStore()
	o := poSeedOrder(f, "ord-1")
	o.PONumber = "PO-2026-0042"
	o.CustomerTaxID = "VN1234-5678"
	o.BillingEmail = "ap@example.com"
	o.BillingName = "Ada Lovelace"
	uncollectibleAt := time.Now()
	poSeedInvoice(f, "inv-1", "ord-1", model.InvoiceStatusUncollectible)
	f.invoices["inv-1"].UncollectibleAt = &uncollectibleAt
	h := &OrderAdminHandler{Store: f}

	w, c := poAdminCtx(t, "GET", "/admin/orders/ord-1", "",
		gin.Params{{Key: "id", Value: "ord-1"}})
	h.Get(c)
	if w.Code != 200 {
		t.Fatalf("get status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	data := poData(t, w)
	order, ok := data["order"].(map[string]any)
	if !ok {
		t.Fatalf("get data has no order object: %v", data)
	}
	for _, key := range []string{"billing_name", "po_number", "customer_tax_id", "billing_email"} {
		if _, ok := order[key]; !ok {
			t.Errorf("get order JSON is missing key %q", key)
		}
	}
	invoices, ok := data["invoices"].([]any)
	if !ok || len(invoices) != 1 {
		t.Fatalf("get data invoices = %v, want one invoice", data["invoices"])
	}
	inv := invoices[0].(map[string]any)
	if _, ok := inv["uncollectible_at"]; !ok {
		t.Errorf("get invoice JSON is missing key %q", "uncollectible_at")
	}

	w, c = poAdminCtx(t, "GET", "/admin/orders", "", nil)
	h.List(c)
	if w.Code != 200 {
		t.Fatalf("list status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	list := poData(t, w)
	rows, ok := list["orders"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("list orders = %v, want one row", list["orders"])
	}
	row := rows[0].(map[string]any)
	if got := row["po_number"]; got != "PO-2026-0042" {
		t.Errorf("list row po_number = %v, want %q", got, "PO-2026-0042")
	}
	if _, ok := row["customer_tax_id"]; !ok {
		t.Errorf("list row JSON is missing key %q", "customer_tax_id")
	}
}
