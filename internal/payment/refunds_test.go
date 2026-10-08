package payment

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
)

// ─── fake RefundStore ─────────────────────────────────────────────
//
// refundsFakeStore implements the whole RefundStore seam in memory so
// the validation matrix, the effects and the idempotency semantics
// run without a database. RecordRefund mirrors the store-side
// derivation exactly (pinned for real in
// internal/store/refunds_test.go: only SUCCEEDED amounts move the
// order; the first one leaves it 'partially_refunded', one reaching
// the total makes it 'refunded' and stamps refunded_at).

type refundsFakeSlot struct {
	rec  *store.IdempotencyRecord
	done bool
}

type refundsFakeStore struct {
	orders      map[string]*model.Order
	licenses    map[string]*model.License
	invoices    map[string]*model.Invoice
	refunds     map[int64]*model.Refund
	conversions map[string]*model.AffiliateConversion

	nextRefund int64
	nextSlot   int64
	slots      map[string]*refundsFakeSlot

	// failure injection
	sumsErr error

	// observation
	audits        []*model.AuditLog
	revocations   []string // "license|reason|actor"
	commissionFor []string
	invoiceSets   []string
	conversionSet []string
}

func newRefundsFakeStore() *refundsFakeStore {
	return &refundsFakeStore{
		orders:      map[string]*model.Order{},
		licenses:    map[string]*model.License{},
		invoices:    map[string]*model.Invoice{},
		refunds:     map[int64]*model.Refund{},
		conversions: map[string]*model.AffiliateConversion{},
		slots:       map[string]*refundsFakeSlot{},
		nextRefund:  1,
		nextSlot:    1,
	}
}

func (f *refundsFakeStore) FindOrderByID(_ context.Context, id string) (*model.Order, error) {
	o, ok := f.orders[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return o, nil
}

func (f *refundsFakeStore) FindLicenseByID(_ context.Context, id string) (*model.License, error) {
	l, ok := f.licenses[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return l, nil
}

func (f *refundsFakeStore) InvoiceByOrder(_ context.Context, orderID string) (*model.Invoice, error) {
	inv, ok := f.invoices[orderID]
	if !ok {
		return nil, errors.New("not found")
	}
	return inv, nil
}

func (f *refundsFakeStore) UpdateInvoiceStatus(_ context.Context, id, status string, _, _ *time.Time) error {
	for _, inv := range f.invoices {
		if inv.ID == id {
			inv.Status = status
			f.invoiceSets = append(f.invoiceSets, id+"|"+status)
			return nil
		}
	}
	return errors.New("not found")
}

func (f *refundsFakeStore) RefundSumsByOrder(_ context.Context, orderID string) (int64, int64, error) {
	if f.sumsErr != nil {
		return 0, 0, f.sumsErr
	}
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

// RecordRefund mirrors store.RecordRefund's order derivation.
func (f *refundsFakeStore) RecordRefund(_ context.Context, r *model.Refund) (*model.Order, error) {
	cp := *r
	cp.ID = f.nextRefund
	f.nextRefund++
	f.refunds[cp.ID] = &cp
	r.ID = cp.ID

	o := f.orders[r.OrderID]
	if o == nil {
		return nil, errors.New("not found")
	}
	_, succeeded, _ := f.RefundSumsByOrder(context.Background(), o.ID)
	o.RefundedMinor = succeeded
	switch {
	case succeeded == 0:
		// nothing has actually left — the status is untouched
	case succeeded >= o.TotalMinor:
		now := time.Now()
		o.Status = model.OrderStatusRefunded
		o.RefundedAt = &now
	default:
		o.Status = model.OrderStatusPartiallyRefunded
	}
	return o, nil
}

func (f *refundsFakeStore) FindRefundByID(_ context.Context, id int64) (*model.Refund, error) {
	r, ok := f.refunds[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return r, nil
}

func (f *refundsFakeStore) UpdateRefundStatus(_ context.Context, id int64, status, providerRef, transID string) error {
	r, ok := f.refunds[id]
	if !ok {
		return errors.New("not found")
	}
	r.Status = status
	if providerRef != "" {
		r.ProviderRef = providerRef
	}
	if transID != "" {
		r.TransID = transID
	}
	return nil
}

func (f *refundsFakeStore) SyncOrderRefundState(_ context.Context, orderID string) (*model.Order, error) {
	o := f.orders[orderID]
	if o == nil {
		return nil, errors.New("not found")
	}
	_, succeeded, _ := f.RefundSumsByOrder(context.Background(), orderID)
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

func (f *refundsFakeStore) RevokeLicenseWithReason(_ context.Context, id, reason, actor string) error {
	if reason == "" {
		reason = model.DefaultRevokeReason
	}
	if !model.ValidRevokeReason(reason) {
		return store.ErrInvalidRevokeReason
	}
	l, ok := f.licenses[id]
	if !ok {
		return errors.New("not found")
	}
	now := time.Now()
	l.Status = model.StatusRevoked
	l.RevokeReason = reason
	l.RevokedBy = actor
	l.RevokedAt = &now
	f.revocations = append(f.revocations, id+"|"+reason+"|"+actor)
	return nil
}

func (f *refundsFakeStore) CancelCommissionForOrder(_ context.Context, resellerID, orderID string) (bool, error) {
	f.commissionFor = append(f.commissionFor, resellerID+"|"+orderID)
	return true, nil
}

func (f *refundsFakeStore) FindConversionByOrderID(_ context.Context, orderID string) (*model.AffiliateConversion, error) {
	c, ok := f.conversions[orderID]
	if !ok {
		return nil, errors.New("not found")
	}
	return c, nil
}

func (f *refundsFakeStore) SetConversionStatus(_ context.Context, id, status string) (*model.AffiliateConversion, error) {
	for _, c := range f.conversions {
		if c.ID == id {
			c.Status = status
			f.conversionSet = append(f.conversionSet, id+"|"+status)
			return c, nil
		}
	}
	return nil, errors.New("not found")
}

func (f *refundsFakeStore) BeginIdempotent(_ context.Context, scope, key, reqHash string) (*store.IdempotencyRecord, bool, error) {
	k := scope + "|" + key
	slot, ok := f.slots[k]
	if !ok {
		rec := &store.IdempotencyRecord{ID: f.nextSlot, Scope: scope, IdempotencyKey: key, RequestHash: reqHash}
		f.nextSlot++
		f.slots[k] = &refundsFakeSlot{rec: rec}
		return rec, true, nil
	}
	if slot.rec.RequestHash != reqHash {
		return nil, false, store.ErrIdempotencyKeyReused
	}
	if !slot.done {
		return nil, false, store.ErrIdempotencyInProgress
	}
	return slot.rec, false, nil
}

func (f *refundsFakeStore) FinishIdempotent(_ context.Context, id int64, status int, body []byte) error {
	for _, slot := range f.slots {
		if slot.rec.ID == id {
			slot.rec.ResponseStatus = status
			slot.rec.ResponseBody = body
			slot.done = true
			return nil
		}
	}
	return errors.New("not found")
}

func (f *refundsFakeStore) ReleaseIdempotent(_ context.Context, id int64) error {
	for k, slot := range f.slots {
		if slot.rec.ID == id {
			delete(f.slots, k)
			return nil
		}
	}
	return nil
}

func (f *refundsFakeStore) Audit(_ context.Context, log *model.AuditLog) {
	f.audits = append(f.audits, log)
}

// refundsFakeProvider is a registry fake: its RefundPayment answers
// the scripted result/error once per scripted entry.
type refundsFakeProvider struct {
	name    string
	enabled bool

	results []RefundResult
	errs    []error
	calls   int
}

func (f *refundsFakeProvider) Name() string  { return f.name }
func (f *refundsFakeProvider) Enabled() bool { return f.enabled }

func (f *refundsFakeProvider) CreatePayment(context.Context, CreatePaymentRequest) (*CreatePaymentResult, error) {
	return nil, ErrNotSupported
}
func (f *refundsFakeProvider) CapturePayment(context.Context, string) (*PaymentStatusResult, error) {
	return nil, ErrNotSupported
}
func (f *refundsFakeProvider) RefundPayment(_ context.Context, req RefundRequest) (*RefundResult, error) {
	i := f.calls
	f.calls++
	if i < len(f.errs) && f.errs[i] != nil {
		return nil, f.errs[i]
	}
	if i < len(f.results) {
		res := f.results[i]
		return &res, nil
	}
	return &RefundResult{RefundRef: "auto", Status: StatusSucceeded}, nil
}
func (f *refundsFakeProvider) VoidPayment(context.Context, string, string) error {
	return ErrNotSupported
}
func (f *refundsFakeProvider) VerifyWebhook([]byte) (*WebhookEvent, error) {
	return nil, ErrNotSupported
}
func (f *refundsFakeProvider) GetPaymentStatus(context.Context, string) (*PaymentStatusResult, error) {
	return nil, ErrNotSupported
}

// refundsSeed writes a paid order with an optional licence + invoice.
func refundsSeed(f *refundsFakeStore, id string, total int64, provider string) *model.Order {
	o := &model.Order{
		ID: id, OrderNumber: "HTC-" + id, CustomerEmail: id + "@example.com",
		Currency: "VND", SubtotalMinor: total, TotalMinor: total,
		Status: model.OrderStatusPaid, PaymentProvider: provider,
	}
	if provider == "stripe" {
		o.LicenseID = "lic-" + id
		f.licenses[o.LicenseID] = &model.License{
			ID: o.LicenseID, Status: model.StatusActive,
			StripePaymentIntentID: "pi_" + id,
		}
	}
	if provider != "no-invoice" {
		f.invoices[id] = &model.Invoice{ID: "inv-" + id, OrderID: id, Status: model.InvoiceStatusPaid}
	}
	f.orders[id] = o
	return o
}

// appErrCode asserts err is the pinned app error and returns it.
func appErrCode(t *testing.T, err error, code string, status int) *apperr.AppError {
	t.Helper()
	var ae *apperr.AppError
	if !errors.As(err, &ae) {
		t.Fatalf("err = %v (%T), want *apperr.AppError", err, err)
	}
	if ae.Code != code || ae.Status != status {
		t.Fatalf("err = (%s, %d), want (%s, %d)", ae.Code, ae.Status, code, status)
	}
	return ae
}

// ─── validation matrix ────────────────────────────────────────────

// TestRefundValidateMatrix pins the eligibility rules as pure logic:
// which order states can be refunded, and which amounts are legal.
func TestRefundValidateMatrix(t *testing.T) {
	order := &model.Order{ID: "o1", TotalMinor: 1000, Status: model.OrderStatusPaid}

	for _, tc := range []struct {
		name      string
		status    string
		amount    int64
		committed int64
		code      string
	}{
		{"full amount on a paid order", model.OrderStatusPaid, 1000, 0, ""},
		{"partial amount", model.OrderStatusPaid, 400, 0, ""},
		{"remaining after a partial", model.OrderStatusPartiallyRefunded, 400, 600, ""},
		{"zero amount", model.OrderStatusPaid, 0, 0, "REFUND_AMOUNT_INVALID"},
		{"negative amount", model.OrderStatusPaid, -1, 0, "REFUND_AMOUNT_INVALID"},
		{"over the total", model.OrderStatusPaid, 1001, 0, "REFUND_EXCEEDS_PAID"},
		{"over what is left", model.OrderStatusPaid, 401, 600, "REFUND_EXCEEDS_PAID"},
		{"pending order", model.OrderStatusPending, 100, 0, "ORDER_NOT_REFUNDABLE"},
		{"failed order", model.OrderStatusFailed, 100, 0, "ORDER_NOT_REFUNDABLE"},
		{"already fully refunded", model.OrderStatusRefunded, 100, 1000, "ORDER_NOT_REFUNDABLE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			order.Status = tc.status
			err := refundValidate(order, tc.amount, tc.committed)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("want ok, got %v", err)
				}
				return
			}
			appErrCode(t, err, tc.code, map[string]int{
				"ORDER_NOT_REFUNDABLE": 409, "REFUND_AMOUNT_INVALID": 400, "REFUND_EXCEEDS_PAID": 400,
			}[tc.code])
		})
	}
}

// TestRefundOrderValidationMatrix pins the same matrix end-to-end
// through RefundOrder: the exact error codes, their statuses, and
// that nothing is recorded when the request is refused.
func TestRefundOrderValidationMatrix(t *testing.T) {
	ctx := context.Background()

	t.Run("unknown order is 404", func(t *testing.T) {
		f := newRefundsFakeStore()
		_, err := RefundOrder(ctx, f, "nope", 100, "goodwill", "admin")
		appErrCode(t, err, "ORDER_NOT_FOUND", 404)
	})

	t.Run("non-paid order is 409", func(t *testing.T) {
		f := newRefundsFakeStore()
		o := refundsSeed(f, "o-pending", 1000, "manual")
		o.Status = model.OrderStatusPending
		_, err := RefundOrder(ctx, f, o.ID, 100, "goodwill", "admin")
		appErrCode(t, err, "ORDER_NOT_REFUNDABLE", 409)
		if len(f.refunds) != 0 {
			t.Errorf("a refused refund must record nothing, got %d rows", len(f.refunds))
		}
	})

	t.Run("double refund is refused", func(t *testing.T) {
		f := newRefundsFakeStore()
		o := refundsSeed(f, "o-double", 1000, "manual")
		if _, err := RefundOrder(ctx, f, o.ID, 1000, "goodwill", "admin"); err != nil {
			t.Fatalf("first refund: %v", err)
		}
		_, err := RefundOrder(ctx, f, o.ID, 1, "again", "admin")
		appErrCode(t, err, "ORDER_NOT_REFUNDABLE", 409) // fully refunded now
	})

	t.Run("over-refund is refused", func(t *testing.T) {
		f := newRefundsFakeStore()
		o := refundsSeed(f, "o-over", 1000, "manual")
		if _, err := RefundOrder(ctx, f, o.ID, 600, "goodwill", "admin"); err != nil {
			t.Fatalf("partial: %v", err)
		}
		_, err := RefundOrder(ctx, f, o.ID, 500, "more", "admin") // only 400 left
		appErrCode(t, err, "REFUND_EXCEEDS_PAID", 400)

		_, err = RefundOrder(ctx, f, o.ID, 0, "zero", "admin")
		appErrCode(t, err, "REFUND_AMOUNT_INVALID", 400)
	})

	t.Run("pending refunds reserve their amount", func(t *testing.T) {
		f := newRefundsFakeStore()
		o := refundsSeed(f, "o-reserve", 1000, "manual")
		if _, err := f.RecordRefund(ctx, &model.Refund{OrderID: o.ID, AmountMinor: 700,
			Currency: o.Currency, Status: model.RefundStatusPending}); err != nil {
			t.Fatalf("seed pending: %v", err)
		}
		// Only 300 is left once a 700 refund is on its way out.
		_, err := RefundOrder(ctx, f, o.ID, 400, "more", "admin")
		appErrCode(t, err, "REFUND_EXCEEDS_PAID", 400)
	})
}

// ─── effects ──────────────────────────────────────────────────────

// TestRefundPartialThenFullEffects pins the business-rule matrix:
//
//	partial → order 'partially_refunded', the LICENCE STAYS ACTIVE,
//	          the invoice stays paid, no clawback
//	full    → order 'refunded' (refunded_at stamped), the invoice
//	          follows the money, the licence is revoked with reason
//	          `refund` and the actor, partner money is clawed back
func TestRefundPartialThenFullEffects(t *testing.T) {
	ctx := context.Background()
	f := newRefundsFakeStore()
	o := refundsSeed(f, "o-effects", 1000, "manual")
	o.ResellerID = "res-1"
	o.LicenseID = "lic-effects"
	f.licenses[o.LicenseID] = &model.License{ID: o.LicenseID, Status: model.StatusActive}
	f.conversions[o.ID] = &model.AffiliateConversion{ID: "conv-1", Status: model.AffiliateConversionStatusApproved}

	// Partial: 400 back.
	row, err := RefundOrder(ctx, f, o.ID, 400, "goodwill", "admin-1")
	if err != nil {
		t.Fatalf("partial refund: %v", err)
	}
	if row.Status != model.RefundStatusSucceeded || row.AmountMinor != 400 {
		t.Errorf("row = %+v, want a succeeded 400 refund", row)
	}
	if o.Status != model.OrderStatusPartiallyRefunded || o.RefundedMinor != 400 || o.RefundedAt != nil {
		t.Errorf("after partial: order = %+v, want partially_refunded/400/no stamp", o)
	}
	if f.licenses[o.LicenseID].Status != model.StatusActive {
		t.Errorf("a partial refund must NOT revoke the licence, got %q", f.licenses[o.LicenseID].Status)
	}
	if len(f.invoiceSets) != 0 {
		t.Errorf("a partial refund must NOT touch the invoice, got %v", f.invoiceSets)
	}
	if len(f.commissionFor) != 0 || len(f.conversionSet) != 0 {
		t.Errorf("a partial refund must NOT claw back partners, got %v / %v", f.commissionFor, f.conversionSet)
	}

	// Full: the remaining 600 back completes the order.
	if _, err := RefundOrder(ctx, f, o.ID, 600, "refund", "admin-2"); err != nil {
		t.Fatalf("full refund: %v", err)
	}
	if o.Status != model.OrderStatusRefunded || o.RefundedMinor != 1000 || o.RefundedAt == nil {
		t.Errorf("after full: order = %+v, want refunded/1000/stamped", o)
	}
	if got := f.invoiceSets; len(got) != 1 || !strings.Contains(got[0], model.InvoiceStatusRefunded) {
		t.Errorf("invoice updates = %v, want the invoice marked refunded", got)
	}
	if len(f.revocations) != 1 {
		t.Fatalf("revocations = %v, want exactly one", f.revocations)
	}
	if want := o.LicenseID + "|" + model.RevokeReasonRefund + "|admin-2"; f.revocations[0] != want {
		t.Errorf("revocation = %q, want %q", f.revocations[0], want)
	}
	if got := f.licenses[o.LicenseID]; got.Status != model.StatusRevoked ||
		got.RevokeReason != model.RevokeReasonRefund || got.RevokedBy != "admin-2" || got.RevokedAt == nil {
		t.Errorf("licence = %+v, want revoked with reason refund by admin-2", got)
	}
	if len(f.commissionFor) != 1 || f.commissionFor[0] != "res-1|"+o.ID {
		t.Errorf("commission clawback = %v, want [res-1|%s]", f.commissionFor, o.ID)
	}
	if len(f.conversionSet) != 1 || !strings.HasSuffix(f.conversionSet[0], model.AffiliateConversionStatusReversed) {
		t.Errorf("conversion clawback = %v, want the conversion reversed", f.conversionSet)
	}

	// The audit trail recorded both refunds and the revocation.
	actions := map[string]int{}
	for _, a := range f.audits {
		actions[a.Action]++
	}
	if actions["refund_recorded"] != 2 || actions["revoked"] != 1 || actions["commission_clawed_back"] != 1 {
		t.Errorf("audit actions = %v, want 2 refunds + 1 revocation + 1 clawback", actions)
	}
}

// TestRefundManualPathForVNGateways pins the by-contract manual
// record for pay2s / payos / no gateway at all: NO provider call, the
// row lands as a 'manual' succeeded ledger entry immediately.
func TestRefundManualPathForVNGateways(t *testing.T) {
	ctx := context.Background()
	for _, provider := range []string{"pay2s", "payos", "manual", ""} {
		f := newRefundsFakeStore()
		o := refundsSeed(f, "o-"+provider, 500, provider)
		row, err := RefundOrder(ctx, f, o.ID, 500, "refund", "admin")
		if err != nil {
			t.Fatalf("provider %q: %v", provider, err)
		}
		if row.PaymentProvider != model.RefundProviderManual || row.Status != model.RefundStatusSucceeded {
			t.Errorf("provider %q: row = (%q, %q), want (manual, succeeded)", provider, row.PaymentProvider, row.Status)
		}
		if o.Status != model.OrderStatusRefunded {
			t.Errorf("provider %q: order = %q, want refunded", provider, o.Status)
		}
	}
}

// TestRefundStripePath pins the synchronous gateway path: the
// licence's payment intent is refunded through the SDK and the row
// records the Stripe refund id.
func TestRefundStripePath(t *testing.T) {
	ctx := context.Background()
	f := newRefundsFakeStore()
	o := refundsSeed(f, "o-stripe", 2000, "stripe")

	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/refunds") {
			t.Errorf("unexpected stripe call %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"re_stub","object":"refund","status":"succeeded"}`))
	})

	row, err := RefundOrder(ctx, f, o.ID, 2000, "customer request", "admin")
	if err != nil {
		t.Fatalf("stripe refund: %v", err)
	}
	if row.PaymentProvider != "stripe" || row.Status != model.RefundStatusSucceeded {
		t.Errorf("row = (%q, %q), want (stripe, succeeded)", row.PaymentProvider, row.Status)
	}
	if row.ProviderRef != "re_stub" || row.TransID != "pi_o-stripe" {
		t.Errorf("row refs = (%q, %q), want (re_stub, pi_o-stripe)", row.ProviderRef, row.TransID)
	}
	if o.Status != model.OrderStatusRefunded {
		t.Errorf("order = %q, want refunded", o.Status)
	}
}

// TestRefundZaloPayAsyncLifecycle pins the asynchronous path: the
// refund is accepted as pending (its amount reserves), nothing is
// revoked yet, and ReconcileRefund runs the fulfilment effects when
// the gateway settles it — exactly once.
func TestRefundZaloPayAsyncLifecycle(t *testing.T) {
	ctx := context.Background()
	fake := &refundsFakeProvider{
		name: "zalopay", enabled: true,
		results: []RefundResult{{RefundRef: "zr-1", Status: StatusPending}},
	}
	RegisterProvider(fake)

	f := newRefundsFakeStore()
	o := refundsSeed(f, "o-zalo", 800, "zalopay")
	o.LicenseID = "lic-zalo"
	f.licenses[o.LicenseID] = &model.License{ID: o.LicenseID, Status: model.StatusActive}

	row, err := RefundOrder(ctx, f, o.ID, 800, "refund", "admin")
	if err != nil {
		t.Fatalf("zalopay refund: %v", err)
	}
	if row.Status != model.RefundStatusPending || row.ProviderRef != "zr-1" {
		t.Errorf("row = (%q, %q), want (pending, zr-1)", row.Status, row.ProviderRef)
	}
	if fake.calls != 1 {
		t.Errorf("provider calls = %d, want 1", fake.calls)
	}
	// A pending refund moves nothing yet.
	if o.Status != model.OrderStatusPaid || len(f.revocations) != 0 {
		t.Errorf("pending must not fulfil: order = %q, revocations = %v", o.Status, f.revocations)
	}

	// The gateway settles it.
	if _, err := ReconcileRefund(ctx, f, row.ID, model.RefundStatusSucceeded, "zr-1", "t-1", "ops"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if o.Status != model.OrderStatusRefunded {
		t.Errorf("after reconcile: order = %q, want refunded", o.Status)
	}
	if len(f.revocations) != 1 {
		t.Errorf("revocations = %v, want one", f.revocations)
	}

	// Reconciliation is not a rewrite: a second pass changes nothing.
	if _, err := ReconcileRefund(ctx, f, row.ID, model.RefundStatusFailed, "", "", "ops"); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if got := f.refunds[row.ID].Status; got != model.RefundStatusSucceeded || len(f.revocations) != 1 {
		t.Errorf("settled row rewritten: status = %q, revocations = %v", got, f.revocations)
	}
}

// TestRefundGatewayFailureSurfaces502 pins the failure contract: a
// provider that errors yields PAYMENT_GATEWAY_ERROR (502), the failed
// attempt is recorded (its amount freed again), and the idempotency
// slot is released so the operator can retry.
func TestRefundGatewayFailureSurfaces502(t *testing.T) {
	ctx := context.Background()
	fake := &refundsFakeProvider{
		name: "zalopay", enabled: true,
		errs:    []error{errors.New("gateway down"), nil},
		results: []RefundResult{{}, {RefundRef: "zr-2", Status: StatusPending}},
	}
	RegisterProvider(fake)

	f := newRefundsFakeStore()
	o := refundsSeed(f, "o-fail", 700, "zalopay")

	_, err := RefundOrder(ctx, f, o.ID, 700, "refund", "admin")
	appErrCode(t, err, "PAYMENT_GATEWAY_ERROR", 502)

	// The failed attempt is on the ledger and reserves nothing.
	if len(f.refunds) != 1 {
		t.Fatalf("refunds = %d, want the failed attempt recorded", len(f.refunds))
	}
	for _, r := range f.refunds {
		if r.Status != model.RefundStatusFailed {
			t.Errorf("attempt = %q, want failed", r.Status)
		}
	}
	if o.Status != model.OrderStatusPaid {
		t.Errorf("order = %q, want paid", o.Status)
	}

	// The retry works — the slot was released, not wedged.
	row, err := RefundOrder(ctx, f, o.ID, 700, "refund", "admin")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if row.Status != model.RefundStatusPending {
		t.Errorf("retry row = %q, want pending", row.Status)
	}
}

// TestRefundIdempotency pins the replay contract: one refund per
// (order, Idempotency-Key); a replay returns the recorded row and
// records nothing new; a key reused for a different refund is
// IDEMPOTENCY_KEY_REUSED (409); an in-flight key is
// IDEMPOTENCY_IN_PROGRESS (409).
func TestRefundIdempotency(t *testing.T) {
	ctx := context.Background()
	f := newRefundsFakeStore()
	o := refundsSeed(f, "o-idem", 1000, "manual")

	row, err := RefundOrderKey(ctx, f, o.ID, 300, "goodwill", "admin", "key-1")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	replay, err := RefundOrderKey(ctx, f, o.ID, 300, "goodwill", "admin", "key-1")
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if replay.ID != row.ID {
		t.Errorf("replay returned refund %d, want the recorded %d", replay.ID, row.ID)
	}
	if len(f.refunds) != 1 {
		t.Errorf("replay must record nothing new, got %d rows", len(f.refunds))
	}

	// Same key, different refund → refused.
	_, err = RefundOrderKey(ctx, f, o.ID, 400, "other", "admin", "key-1")
	appErrCode(t, err, "IDEMPOTENCY_KEY_REUSED", 409)

	// A fresh key refunds again (within the remaining cap).
	if _, err := RefundOrderKey(ctx, f, o.ID, 300, "goodwill", "admin", "key-2"); err != nil {
		t.Fatalf("second key: %v", err)
	}

	// The empty-key path fingerprints (order, amount, reason): the
	// same logical refund replays, a different one does not.
	again, err := RefundOrder(ctx, f, o.ID, 100, "auto", "admin")
	if err != nil {
		t.Fatalf("auto-key first: %v", err)
	}
	replay, err = RefundOrder(ctx, f, o.ID, 100, "auto", "admin")
	if err != nil {
		t.Fatalf("auto-key replay: %v", err)
	}
	if replay.ID != again.ID || len(f.refunds) != 3 {
		t.Errorf("auto-key replay = refund %d (rows=%d), want %d with 3 rows", replay.ID, len(f.refunds), again.ID)
	}

	// An in-flight key is refused until it settles (same request hash
	// as the retry will bring — the slot is claimed, not done).
	inFlightHash := refundAutoKey(o.ID, 50, "in flight")
	if _, _, err := f.BeginIdempotent(ctx, "refund:"+o.ID, "key-3", inFlightHash); err != nil {
		t.Fatalf("claim: %v", err)
	}
	_, err = RefundOrderKey(ctx, f, o.ID, 50, "in flight", "admin", "key-3")
	appErrCode(t, err, "IDEMPOTENCY_IN_PROGRESS", 409)
}

// TestRefundGatewayRefundIDShape pins the idempotent gateway refund
// reference: R + 16 uppercase hex of the same fingerprint the local
// idempotency key uses (so a retry reuses it and the gateway dedupes).
func TestRefundGatewayRefundIDShape(t *testing.T) {
	got := refundGatewayRefundID("ord_1", 500, "refund")
	if !strings.HasPrefix(got, "R") || len(got) != 17 {
		t.Fatalf("refund id %q, want R + 16 hex", got)
	}
	if got[1:] != strings.ToUpper(got[1:]) {
		t.Errorf("refund id %q must be uppercase", got)
	}
	if got != refundGatewayRefundID("ord_1", 500, "refund") {
		t.Errorf("refund id must be deterministic")
	}
	if got == refundGatewayRefundID("ord_1", 501, "refund") {
		t.Errorf("different amount must give a different refund id")
	}
}

// TestRefundAutoKeyIsFingerprint pins the deterministic request
// fingerprint: same inputs → same key, any input changing → a new one.
func TestRefundAutoKeyIsFingerprint(t *testing.T) {
	base := refundAutoKey("ord_1", 500, "refund")
	if base != refundAutoKey("ord_1", 500, "refund") {
		t.Errorf("auto key must be deterministic")
	}
	for _, other := range []string{
		refundAutoKey("ord_2", 500, "refund"),
		refundAutoKey("ord_1", 501, "refund"),
		refundAutoKey("ord_1", 500, "fraud"),
	} {
		if other == base {
			t.Errorf("a changed input must change the key")
		}
	}
	if len(base) != 64 {
		t.Errorf("auto key = %d chars, want sha-256 hex (64)", len(base))
	}
}
