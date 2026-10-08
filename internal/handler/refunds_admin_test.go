package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ─── fake refundsAdminStore ───────────────────────────────────────
//
// A compact in-memory implementation of the whole seam so the
// endpoint contracts run without a database. RecordRefund mirrors the
// store-side order derivation (pinned in internal/store and
// internal/payment tests).

type fakeRefundsAdminStore struct {
	orders  map[string]*model.Order
	refunds map[int64]*model.Refund
	nextID  int64
	slots   map[string]*store.IdempotencyRecord
	done    map[int64]bool
	audits  []*model.AuditLog
	revokes []string
}

func newFakeRefundsAdminStore() *fakeRefundsAdminStore {
	return &fakeRefundsAdminStore{
		orders:  map[string]*model.Order{},
		refunds: map[int64]*model.Refund{},
		slots:   map[string]*store.IdempotencyRecord{},
		done:    map[int64]bool{},
		nextID:  1,
	}
}

func (f *fakeRefundsAdminStore) seed(id string, total int64, status string) *model.Order {
	o := &model.Order{ID: id, OrderNumber: "HTC-" + id, Currency: "VND",
		SubtotalMinor: total, TotalMinor: total, Status: status, PaymentProvider: "manual"}
	f.orders[id] = o
	return o
}

func (f *fakeRefundsAdminStore) FindOrderByID(_ context.Context, id string) (*model.Order, error) {
	o, ok := f.orders[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return o, nil
}

func (f *fakeRefundsAdminStore) FindLicenseByID(context.Context, string) (*model.License, error) {
	return nil, errors.New("not found")
}
func (f *fakeRefundsAdminStore) InvoiceByOrder(context.Context, string) (*model.Invoice, error) {
	return nil, errors.New("not found")
}
func (f *fakeRefundsAdminStore) UpdateInvoiceStatus(context.Context, string, string, *time.Time, *time.Time) error {
	return nil
}

func (f *fakeRefundsAdminStore) RefundSumsByOrder(_ context.Context, orderID string) (int64, int64, error) {
	var committed, succeeded int64
	for _, r := range f.refunds {
		if r.OrderID != orderID || !r.Counts() {
			continue
		}
		committed += r.AmountMinor
		if r.Status == model.RefundStatusSucceeded {
			succeeded += r.AmountMinor
		}
	}
	return committed, succeeded, nil
}

func (f *fakeRefundsAdminStore) RecordRefund(_ context.Context, r *model.Refund) (*model.Order, error) {
	cp := *r
	cp.ID = f.nextID
	f.nextID++
	f.refunds[cp.ID] = &cp
	r.ID = cp.ID

	o := f.orders[r.OrderID]
	_, succeeded, _ := f.RefundSumsByOrder(context.Background(), o.ID)
	o.RefundedMinor = succeeded
	switch {
	case succeeded == 0:
	case succeeded >= o.TotalMinor:
		now := time.Now()
		o.Status = model.OrderStatusRefunded
		o.RefundedAt = &now
	default:
		o.Status = model.OrderStatusPartiallyRefunded
	}
	return o, nil
}

func (f *fakeRefundsAdminStore) FindRefundByID(_ context.Context, id int64) (*model.Refund, error) {
	r, ok := f.refunds[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return r, nil
}

func (f *fakeRefundsAdminStore) UpdateRefundStatus(_ context.Context, id int64, status, _, _ string) error {
	f.refunds[id].Status = status
	return nil
}

func (f *fakeRefundsAdminStore) SyncOrderRefundState(_ context.Context, orderID string) (*model.Order, error) {
	return f.orders[orderID], nil
}

func (f *fakeRefundsAdminStore) RevokeLicenseWithReason(_ context.Context, id, reason, actor string) error {
	f.revokes = append(f.revokes, id+"|"+reason+"|"+actor)
	return nil
}

func (f *fakeRefundsAdminStore) CancelCommissionForOrder(context.Context, string, string) (bool, error) {
	return true, nil
}

func (f *fakeRefundsAdminStore) FindConversionByOrderID(context.Context, string) (*model.AffiliateConversion, error) {
	return nil, errors.New("not found")
}

func (f *fakeRefundsAdminStore) SetConversionStatus(_ context.Context, id, status string) (*model.AffiliateConversion, error) {
	return &model.AffiliateConversion{ID: id, Status: status}, nil
}

func (f *fakeRefundsAdminStore) BeginIdempotent(_ context.Context, scope, key, reqHash string) (*store.IdempotencyRecord, bool, error) {
	k := scope + "|" + key
	rec, ok := f.slots[k]
	if !ok {
		rec = &store.IdempotencyRecord{ID: f.nextID + 1000, Scope: scope, IdempotencyKey: key, RequestHash: reqHash}
		f.slots[k] = rec
		return rec, true, nil
	}
	if rec.RequestHash != reqHash {
		return nil, false, store.ErrIdempotencyKeyReused
	}
	if !f.done[rec.ID] {
		return nil, false, store.ErrIdempotencyInProgress
	}
	return rec, false, nil
}

func (f *fakeRefundsAdminStore) FinishIdempotent(_ context.Context, id int64, status int, body []byte) error {
	f.done[id] = true
	for _, rec := range f.slots {
		if rec.ID == id {
			rec.ResponseStatus = status
			rec.ResponseBody = body
		}
	}
	return nil
}

func (f *fakeRefundsAdminStore) ReleaseIdempotent(_ context.Context, id int64) error {
	for k, rec := range f.slots {
		if rec.ID == id {
			delete(f.slots, k)
		}
	}
	return nil
}

func (f *fakeRefundsAdminStore) Audit(_ context.Context, log *model.AuditLog) {
	f.audits = append(f.audits, log)
}

func (f *fakeRefundsAdminStore) ListRefundsByOrder(_ context.Context, orderID string) ([]*model.Refund, error) {
	var out []*model.Refund
	for _, r := range f.refunds {
		if r.OrderID == orderID {
			out = append(out, r)
		}
	}
	return out, nil
}

// ─── POST /admin/orders/:id/refund ────────────────────────────────

// TestAdminRefundEndpoint pins the request/response contract:
// body {amount_minor?, reason}, omit-amount = the full remainder,
// reason required, and {refund, order_status} out.
func TestAdminRefundEndpoint(t *testing.T) {
	t.Run("omit amount refunds the full remainder", func(t *testing.T) {
		f := newFakeRefundsAdminStore()
		f.seed("ord-1", 1000, model.OrderStatusPaid)
		h := &RefundsAdminHandler{Store: f}

		w, c := poAdminCtx(t, http.MethodPost, "/admin/orders/ord-1/refund",
			`{"reason":"goodwill"}`, params("id", "ord-1"))
		h.Refund(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		data := poData(t, w)
		refund := data["refund"].(map[string]any)
		if refund["amount_minor"] != float64(1000) {
			t.Errorf("amount = %v, want the full 1000", refund["amount_minor"])
		}
		if data["order_status"] != model.OrderStatusRefunded {
			t.Errorf("order_status = %v, want refunded", data["order_status"])
		}
	})

	t.Run("explicit amount partial-refunds", func(t *testing.T) {
		f := newFakeRefundsAdminStore()
		o := f.seed("ord-2", 1000, model.OrderStatusPaid)
		h := &RefundsAdminHandler{Store: f}

		w, c := poAdminCtx(t, http.MethodPost, "/admin/orders/ord-2/refund",
			`{"amount_minor":400,"reason":"goodwill"}`, params("id", "ord-2"))
		h.Refund(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		data := poData(t, w)
		if data["order_status"] != model.OrderStatusPartiallyRefunded {
			t.Errorf("order_status = %v, want partially_refunded", data["order_status"])
		}
		if o.RefundedMinor != 400 {
			t.Errorf("refunded_minor = %d, want 400", o.RefundedMinor)
		}
	})

	t.Run("reason is required", func(t *testing.T) {
		f := newFakeRefundsAdminStore()
		f.seed("ord-3", 1000, model.OrderStatusPaid)
		h := &RefundsAdminHandler{Store: f}
		w, c := poAdminCtx(t, http.MethodPost, "/admin/orders/ord-3/refund",
			`{"amount_minor":100}`, params("id", "ord-3"))
		h.Refund(c)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("unknown order is 404", func(t *testing.T) {
		f := newFakeRefundsAdminStore()
		h := &RefundsAdminHandler{Store: f}
		w, c := poAdminCtx(t, http.MethodPost, "/admin/orders/ghost/refund",
			`{"reason":"x"}`, params("id", "ghost"))
		h.Refund(c)
		if w.Code != http.StatusNotFound || !poHasErrorCode(t, w, "ORDER_NOT_FOUND") {
			t.Errorf("status = %d body=%s, want 404 ORDER_NOT_FOUND", w.Code, w.Body.String())
		}
	})

	t.Run("non-refundable order is 409", func(t *testing.T) {
		f := newFakeRefundsAdminStore()
		f.seed("ord-4", 1000, model.OrderStatusPending)
		h := &RefundsAdminHandler{Store: f}
		w, c := poAdminCtx(t, http.MethodPost, "/admin/orders/ord-4/refund",
			`{"amount_minor":100,"reason":"x"}`, params("id", "ord-4"))
		h.Refund(c)
		if w.Code != http.StatusConflict || !poHasErrorCode(t, w, "ORDER_NOT_REFUNDABLE") {
			t.Errorf("status = %d body=%s, want 409 ORDER_NOT_REFUNDABLE", w.Code, w.Body.String())
		}
	})

	t.Run("over-refund is 400", func(t *testing.T) {
		f := newFakeRefundsAdminStore()
		f.seed("ord-5", 1000, model.OrderStatusPaid)
		h := &RefundsAdminHandler{Store: f}
		w, c := poAdminCtx(t, http.MethodPost, "/admin/orders/ord-5/refund",
			`{"amount_minor":2000,"reason":"x"}`, params("id", "ord-5"))
		h.Refund(c)
		if w.Code != http.StatusBadRequest || !poHasErrorCode(t, w, "REFUND_EXCEEDS_PAID") {
			t.Errorf("status = %d body=%s, want 400 REFUND_EXCEEDS_PAID", w.Code, w.Body.String())
		}
	})

	t.Run("nothing left is 409", func(t *testing.T) {
		f := newFakeRefundsAdminStore()
		o := f.seed("ord-6", 1000, model.OrderStatusRefunded)
		o.RefundedMinor = 1000
		if _, err := f.RecordRefund(context.Background(), &model.Refund{
			OrderID: o.ID, AmountMinor: 1000, Currency: "VND", Status: model.RefundStatusSucceeded}); err != nil {
			t.Fatal(err)
		}
		h := &RefundsAdminHandler{Store: f}
		w, c := poAdminCtx(t, http.MethodPost, "/admin/orders/ord-6/refund",
			`{"reason":"x"}`, params("id", "ord-6"))
		h.Refund(c)
		if w.Code != http.StatusConflict || !poHasErrorCode(t, w, "ORDER_NOT_REFUNDABLE") {
			t.Errorf("status = %d body=%s, want 409 ORDER_NOT_REFUNDABLE", w.Code, w.Body.String())
		}
	})

	t.Run("malformed Idempotency-Key is 400", func(t *testing.T) {
		f := newFakeRefundsAdminStore()
		f.seed("ord-7", 1000, model.OrderStatusPaid)
		h := &RefundsAdminHandler{Store: f}
		w, c := poAdminCtx(t, http.MethodPost, "/admin/orders/ord-7/refund",
			`{"amount_minor":100,"reason":"x"}`, params("id", "ord-7"))
		c.Request.Header.Set("Idempotency-Key", strings.Repeat("k", 256)) // over the column's 255
		h.Refund(c)
		if w.Code != http.StatusBadRequest || !poHasErrorCode(t, w, "INVALID_IDEMPOTENCY_KEY") {
			t.Errorf("status = %d body=%s, want 400 INVALID_IDEMPOTENCY_KEY", w.Code, w.Body.String())
		}
	})

	t.Run("idempotent replay returns the same refund", func(t *testing.T) {
		f := newFakeRefundsAdminStore()
		f.seed("ord-8", 1000, model.OrderStatusPaid)
		h := &RefundsAdminHandler{Store: f}

		w, c := poAdminCtx(t, http.MethodPost, "/admin/orders/ord-8/refund",
			`{"amount_minor":300,"reason":"x"}`, params("id", "ord-8"))
		c.Request.Header.Set("Idempotency-Key", "key-abc-123")
		h.Refund(c)
		first := poData(t, w)["refund"].(map[string]any)

		w, c = poAdminCtx(t, http.MethodPost, "/admin/orders/ord-8/refund",
			`{"amount_minor":300,"reason":"x"}`, params("id", "ord-8"))
		c.Request.Header.Set("Idempotency-Key", "key-abc-123")
		h.Refund(c)
		second := poData(t, w)["refund"].(map[string]any)

		if first["id"] != second["id"] {
			t.Errorf("replay returned refund %v then %v", first["id"], second["id"])
		}
		if len(f.refunds) != 1 {
			t.Errorf("rows = %d, want 1 (the replay recorded nothing)", len(f.refunds))
		}
	})
}

// TestAdminListRefundsEndpoint pins the ledger endpoint: {refunds:
// [...]} for the order, ORDER_NOT_FOUND for an unknown one.
func TestAdminListRefundsEndpoint(t *testing.T) {
	f := newFakeRefundsAdminStore()
	o := f.seed("ord-1", 1000, model.OrderStatusPaid)
	if _, err := f.RecordRefund(context.Background(), &model.Refund{
		OrderID: o.ID, AmountMinor: 400, Currency: "VND", Status: model.RefundStatusSucceeded}); err != nil {
		t.Fatal(err)
	}
	h := &RefundsAdminHandler{Store: f}

	w, c := poAdminCtx(t, http.MethodGet, "/admin/orders/ord-1/refunds", "", params("id", "ord-1"))
	h.ListRefunds(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	rows := poData(t, w)["refunds"].([]any)
	if len(rows) != 1 {
		t.Errorf("refunds = %d rows, want 1", len(rows))
	}

	w, c = poAdminCtx(t, http.MethodGet, "/admin/orders/ghost/refunds", "", params("id", "ghost"))
	h.ListRefunds(c)
	if w.Code != http.StatusNotFound || !poHasErrorCode(t, w, "ORDER_NOT_FOUND") {
		t.Errorf("status = %d body=%s, want 404 ORDER_NOT_FOUND", w.Code, w.Body.String())
	}
}

// ─── metrics ──────────────────────────────────────────────────────

type fakeMetricsStore struct {
	snap      *store.MRRSnapshot
	series    []store.MRRSeriesPoint
	lastAt    time.Time
	lastFrom  time.Time
	lastTo    time.Time
	lastGrain string
	err       error
}

func (f *fakeMetricsStore) MRR(_ context.Context, at time.Time) (*store.MRRSnapshot, error) {
	f.lastAt = at
	if f.err != nil {
		return nil, f.err
	}
	return f.snap, nil
}

func (f *fakeMetricsStore) MRRSeries(_ context.Context, from, to time.Time, grain string) ([]store.MRRSeriesPoint, error) {
	f.lastFrom, f.lastTo, f.lastGrain = from, to, grain
	if f.err != nil {
		return nil, f.err
	}
	return f.series, nil
}

// TestAdminMetricsEndpoints pins the §42 response shape and the
// window validation: money as int64 minor units, ?at= parsing, and
// the report window rules (grain, from ≤ to, ≤ 3 years).
func TestAdminMetricsEndpoints(t *testing.T) {
	t.Run("snapshot shape", func(t *testing.T) {
		f := &fakeMetricsStore{snap: &store.MRRSnapshot{MRRMinor: 12345, ARRMinor: 148140, Currency: "VND"}}
		h := &AdminMetricsHandler{Store: f}
		w, c := poAdminCtx(t, http.MethodGet, "/admin/metrics/mrr?at=2026-03-01", "", nil)
		h.MRR(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		data := poData(t, w)
		if data["mrr_minor"] != float64(12345) || data["arr_minor"] != float64(148140) || data["currency"] != "VND" {
			t.Errorf("snapshot = %v", data)
		}
		if _, ok := data["series"].([]any); !ok {
			t.Errorf("series must be present (empty) — %v", data)
		}
		if f.lastAt.Format("2006-01-02") != "2026-03-01" {
			t.Errorf("at = %v, want 2026-03-01", f.lastAt)
		}
	})

	t.Run("bad at is 400", func(t *testing.T) {
		h := &AdminMetricsHandler{Store: &fakeMetricsStore{snap: &store.MRRSnapshot{}}}
		w, c := poAdminCtx(t, http.MethodGet, "/admin/metrics/mrr?at=not-a-date", "", nil)
		h.MRR(c)
		if w.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", w.Code)
		}
	})

	t.Run("series shape and window", func(t *testing.T) {
		f := &fakeMetricsStore{series: []store.MRRSeriesPoint{
			{Ts: "2026-03-01", MRRMinor: 100, ARRMinor: 1200},
			{Ts: "2026-03-02", MRRMinor: 200, ARRMinor: 2400},
		}}
		h := &AdminMetricsHandler{Store: f}
		w, c := poAdminCtx(t, http.MethodGet,
			"/admin/metrics/mrr-series?from=2026-03-01&to=2026-03-02&grain=day", "", nil)
		h.Series(c)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
		}
		data := poData(t, w)
		if data["grain"] != "day" || data["from"] != "2026-03-01" || data["to"] != "2026-03-02" {
			t.Errorf("series envelope = %v", data)
		}
		if pts := data["series"].([]any); len(pts) != 2 {
			t.Errorf("series = %d points, want 2", len(pts))
		}
		if f.lastGrain != "day" {
			t.Errorf("grain = %q, want day", f.lastGrain)
		}
	})

	t.Run("window refusals", func(t *testing.T) {
		h := &AdminMetricsHandler{Store: &fakeMetricsStore{}}
		for _, target := range []string{
			"/admin/metrics/mrr-series?from=2026-03-02&to=2026-03-01",            // from > to
			"/admin/metrics/mrr-series?from=2020-01-01&to=2026-01-01&grain=day",  // > 3y
			"/admin/metrics/mrr-series?from=2026-03-01&to=2026-03-02&grain=hour", // bad grain
			"/admin/metrics/mrr-series?to=2026-03-02",                            // from missing
		} {
			w, c := poAdminCtx(t, http.MethodGet, target, "", nil)
			h.Series(c)
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", target, w.Code)
			}
		}
	})
}

// ─── retention ────────────────────────────────────────────────────

type fakeRetentionStore struct {
	settings map[string]string
	deleted  map[string]int64
	audits   []*model.AuditLog
}

func (f *fakeRetentionStore) GetSetting(_ context.Context, key string) (string, error) {
	return f.settings[key], nil
}

func (f *fakeRetentionStore) delete(table string) func(context.Context, time.Time, int) (int64, error) {
	return func(_ context.Context, _ time.Time, _ int) (int64, error) {
		return f.deleted[table], nil
	}
}

func (f *fakeRetentionStore) DeleteNotificationsBefore(ctx context.Context, before time.Time, batch int) (int64, error) {
	return f.delete("notifications")(ctx, before, batch)
}
func (f *fakeRetentionStore) DeleteProcessedEventsBefore(ctx context.Context, before time.Time, batch int) (int64, error) {
	return f.delete("processed_events")(ctx, before, batch)
}
func (f *fakeRetentionStore) DeleteWebhookDeliveriesBefore(ctx context.Context, before time.Time, batch int) (int64, error) {
	return f.delete("webhook_deliveries")(ctx, before, batch)
}
func (f *fakeRetentionStore) DeleteAuditLogsBefore(ctx context.Context, before time.Time, batch int) (int64, error) {
	return f.delete("audit_logs")(ctx, before, batch)
}

func (f *fakeRetentionStore) Audit(_ context.Context, log *model.AuditLog) {
	f.audits = append(f.audits, log)
}

// TestAdminRetentionRun pins the §92 manual pass endpoint:
// {deleted: {...}} with all four tables and an audit entry.
func TestAdminRetentionRun(t *testing.T) {
	f := &fakeRetentionStore{
		settings: map[string]string{"retention.notifications_days": "10"},
		deleted:  map[string]int64{"notifications": 5},
	}
	h := &AdminRetentionHandler{Store: f, Audit: f}

	w, c := poAdminCtx(t, http.MethodPost, "/admin/retention/run", "", nil)
	h.Run(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	data := poData(t, w)["deleted"].(map[string]any)
	for _, table := range []string{"notifications", "processed_events", "webhook_deliveries", "audit_logs"} {
		if _, ok := data[table]; !ok {
			t.Errorf("deleted map missing %q: %v", table, data)
		}
	}
	if data["notifications"] != float64(5) {
		t.Errorf("notifications = %v, want 5", data["notifications"])
	}
	if len(f.audits) != 1 || f.audits[0].Action != "retention_run" {
		t.Errorf("audits = %v, want one retention_run entry", f.audits)
	}
}

// params builds gin params from pairs.
func params(kv ...string) gin.Params {
	out := make(gin.Params, 0, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		out = append(out, gin.Param{Key: kv[i], Value: kv[i+1]})
	}
	return out
}

// poHasErrorCode reports whether the error envelope carries code.
func poHasErrorCode(t *testing.T, w *httptest.ResponseRecorder, code string) bool {
	t.Helper()
	var env struct {
		Success bool `json:"success"`
		Error   *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v\nbody: %s", err, w.Body.String())
	}
	return env.Error != nil && env.Error.Code == code
}
