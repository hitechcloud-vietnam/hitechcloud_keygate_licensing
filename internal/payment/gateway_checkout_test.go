package payment

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
)

// gatewayFakeProvider is an in-memory PaymentProvider for tests — the
// real gateway implementations are being written concurrently and are
// deliberately not depended on here.
type gatewayFakeProvider struct {
	name    string
	enabled bool

	createFn func(ctx context.Context, req CreatePaymentRequest) (*CreatePaymentResult, error)
	verifyFn func(payload []byte) (*WebhookEvent, error)

	got []CreatePaymentRequest
}

func (f *gatewayFakeProvider) Name() string  { return f.name }
func (f *gatewayFakeProvider) Enabled() bool { return f.enabled }

func (f *gatewayFakeProvider) CreatePayment(ctx context.Context, req CreatePaymentRequest) (*CreatePaymentResult, error) {
	f.got = append(f.got, req)
	if f.createFn != nil {
		return f.createFn(ctx, req)
	}
	return &CreatePaymentResult{
		ProviderRef: "ref-" + req.OrderID,
		PayURL:      "https://pay.example/" + req.OrderID,
		QRCode:      "data:image/png;base64,x",
	}, nil
}

func (f *gatewayFakeProvider) CapturePayment(ctx context.Context, ref string) (*PaymentStatusResult, error) {
	return &PaymentStatusResult{ProviderRef: ref, Status: StatusSucceeded, AmountMinor: 0, Currency: "VND"}, nil
}
func (f *gatewayFakeProvider) RefundPayment(ctx context.Context, req RefundRequest) (*RefundResult, error) {
	return nil, ErrNotSupported
}
func (f *gatewayFakeProvider) VoidPayment(ctx context.Context, ref, reason string) error { return nil }
func (f *gatewayFakeProvider) VerifyWebhook(payload []byte) (*WebhookEvent, error) {
	if f.verifyFn != nil {
		return f.verifyFn(payload)
	}
	return nil, ErrWebhookSignatureInvalid
}
func (f *gatewayFakeProvider) GetPaymentStatus(ctx context.Context, ref string) (*PaymentStatusResult, error) {
	return f.CapturePayment(ctx, ref)
}

// gatewayTestDB opens the integration database or skips.
func gatewayTestDB(t *testing.T) *store.Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("skipping integration test: TEST_DATABASE_URL not set")
	}
	s, err := store.New(dsn)
	if err != nil {
		t.Skipf("skipping integration test: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.RunMigrations("../../db/migrations"); err != nil {
		t.Fatalf("migrations: %v", err)
	}
	return s
}

// gatewaySeedPlan creates a product + priced perpetual plan and stubs
// the Stripe price lookup (the catalogue's source of truth for money).
func gatewaySeedPlan(t *testing.T, s *store.Store, ctx context.Context, tag, licenseType, priceID, priceCurrency string, unitAmount int64) *model.Plan {
	t.Helper()
	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/prices/") {
			t.Errorf("unexpected Stripe call: %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"error":{"message":"unexpected"}}`, http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		priceType := "one_time"
		if licenseType != "perpetual" {
			priceType = "recurring"
		}
		fmt.Fprintf(w, `{"id":%q,"object":"price","unit_amount":%d,"currency":%q,"type":%q}`,
			priceID, unitAmount, priceCurrency, priceType)
	})
	prod := &model.Product{Name: "GW " + tag, Slug: "gw-" + tag, Type: "hybrid"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("create product: %v", err)
	}
	plan := &model.Plan{
		ProductID: prod.ID, Name: "GW Plan " + tag, Slug: "gw-plan-" + tag,
		LicenseType: licenseType, LicenseModel: "standard", StripePriceID: priceID, Active: true,
	}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	return plan
}

// TestGatewayCheckoutProviderRefusals pins the provider-layer refusals
// and their sentinels, with no database: the registry is consulted
// first, and a request that cannot even be priced never touches the
// store.
func TestGatewayCheckoutProviderRefusals(t *testing.T) {
	ctx := context.Background()

	_, err := StartGatewayCheckout(ctx, nil, &GatewayCheckoutRequest{
		PlanID: "p", Provider: "ghost", Email: "buyer@example.com",
	})
	if !errors.Is(err, ErrProviderNotFound) {
		t.Fatalf("unknown provider: %v", err)
	}

	RegisterProvider(&gatewayFakeProvider{name: "pay2s", enabled: false})
	_, err = StartGatewayCheckout(ctx, nil, &GatewayCheckoutRequest{
		PlanID: "p", Provider: "pay2s", Email: "buyer@example.com",
	})
	if !errors.Is(err, ErrProviderNotConfigured) {
		t.Fatalf("unconfigured provider: %v", err)
	}

	RegisterProvider(&gatewayFakeProvider{name: "pay2s", enabled: true})
	_, err = StartGatewayCheckout(ctx, nil, &GatewayCheckoutRequest{
		PlanID: "p", Provider: "pay2s",
	})
	var ae *apperr.AppError
	if !errors.As(err, &ae) || ae.Code != "MISSING_CUSTOMER" {
		t.Fatalf("missing email: %v", err)
	}
}

// TestGatewayCheckoutThenIPNFulfils is the happy path end to end:
// create → IPN → fulfilled (order paid, licence exists), and the
// replayed IPN is a no-op.
func TestGatewayCheckoutThenIPNFulfils(t *testing.T) {
	s := gatewayTestDB(t)
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000")
	plan := gatewaySeedPlan(t, s, ctx, "ok"+suffix, "perpetual", "price_gw_"+suffix, "vnd", 220000)
	fake := &gatewayFakeProvider{name: "pay2s", enabled: true}
	RegisterProvider(fake)

	res, err := StartGatewayCheckout(ctx, s, &GatewayCheckoutRequest{
		PlanID: plan.ID, Provider: "pay2s",
		Email:      "gw-ok-" + suffix + "@example.com",
		WebhookURL: "https://example.test/api/v1/webhook/pay2s",
		ReturnURL:  "https://example.test/checkout/gateway-return",
	})
	if err != nil {
		t.Fatalf("start checkout: %v", err)
	}
	if res.Payment.PayURL == "" || res.Payment.ProviderRef == "" {
		t.Fatalf("payment result: %+v", res.Payment)
	}
	if res.Order.Status != model.OrderStatusPending {
		t.Fatalf("order status = %s, want pending", res.Order.Status)
	}
	if len(fake.got) != 1 || fake.got[0].AmountMinor != res.Order.TotalMinor ||
		fake.got[0].Currency != "VND" || fake.got[0].OrderID != res.Order.OrderNumber {
		t.Fatalf("provider request: %+v", fake.got)
	}
	if !strings.Contains(fake.got[0].ReturnURL, "order=") {
		t.Fatalf("return URL does not carry the order number: %q", fake.got[0].ReturnURL)
	}

	row, err := s.GetGatewayPaymentByRef(ctx, "pay2s", res.Payment.ProviderRef)
	if err != nil || row.Status != string(StatusPending) || row.AmountMinor != res.Order.TotalMinor {
		t.Fatalf("payment row: %v %+v", err, row)
	}

	ev := &WebhookEvent{
		Provider: "pay2s", OrderID: res.Order.OrderNumber, ProviderRef: res.Payment.ProviderRef,
		TransID: "trans-1", Status: StatusSucceeded,
		AmountMinor: row.AmountMinor, Currency: "VND", PaidAt: time.Now(),
		Raw: map[string]any{"ok": true},
	}
	if err := GatewayFulfil(ctx, s, nil, ev); err != nil {
		t.Fatalf("fulfil: %v", err)
	}

	paid, err := s.FindOrderByID(ctx, res.Order.ID)
	if err != nil || paid.Status != model.OrderStatusPaid || paid.LicenseID == "" {
		t.Fatalf("paid order: %v %+v", err, paid)
	}
	lic, err := s.FindLicenseByID(ctx, paid.LicenseID)
	if err != nil || lic.Status != model.StatusActive || lic.Email != res.Order.CustomerEmail {
		t.Fatalf("license: %v %+v", err, lic)
	}
	settled, err := s.GetGatewayPaymentByRef(ctx, "pay2s", res.Payment.ProviderRef)
	if err != nil || settled.Status != string(StatusSucceeded) || settled.TransID != "trans-1" {
		t.Fatalf("settled row: %v %+v", err, settled)
	}

	// Replay: a no-op — one licence, order untouched.
	if err := GatewayFulfil(ctx, s, nil, ev); err != nil {
		t.Fatalf("replay: %v", err)
	}
	lics, err := s.ListLicensesByEmail(ctx, res.Order.CustomerEmail)
	if err != nil {
		t.Fatalf("list licenses: %v", err)
	}
	n := 0
	for _, l := range lics {
		if l.ProductID == plan.ProductID {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected 1 licence after replay, got %d", n)
	}
}

// TestGatewayFulfilAmountMismatchNeverFulfils pins the money rule: a
// callback reporting any amount but the stored one is refused and the
// sale stays exactly as it was.
func TestGatewayFulfilAmountMismatchNeverFulfils(t *testing.T) {
	s := gatewayTestDB(t)
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000")
	plan := gatewaySeedPlan(t, s, ctx, "mismatch"+suffix, "perpetual", "price_gwm_"+suffix, "vnd", 50000)
	fake := &gatewayFakeProvider{name: "pay2s", enabled: true}
	RegisterProvider(fake)

	res, err := StartGatewayCheckout(ctx, s, &GatewayCheckoutRequest{
		PlanID: plan.ID, Provider: "pay2s", Email: "gw-mm-" + suffix + "@example.com",
	})
	if err != nil {
		t.Fatalf("start checkout: %v", err)
	}
	row, err := s.GetGatewayPaymentByRef(ctx, "pay2s", res.Payment.ProviderRef)
	if err != nil {
		t.Fatalf("row: %v", err)
	}

	ev := &WebhookEvent{
		Provider: "pay2s", ProviderRef: res.Payment.ProviderRef,
		Status: StatusSucceeded, AmountMinor: row.AmountMinor + 1, Currency: "VND",
	}
	if err := GatewayFulfil(ctx, s, nil, ev); !errors.Is(err, ErrAmountMismatch) {
		t.Fatalf("want ErrAmountMismatch, got %v", err)
	}
	paid, _ := s.FindOrderByID(ctx, res.Order.ID)
	if paid.Status != model.OrderStatusPending || paid.LicenseID != "" {
		t.Fatalf("mismatched IPN must not fulfil: %+v", paid)
	}
	after, _ := s.GetGatewayPaymentByRef(ctx, "pay2s", res.Payment.ProviderRef)
	if after.Status != string(StatusPending) {
		t.Fatalf("row must stay pending: %+v", after)
	}
}

// TestGatewayCheckoutRejectsNonVND: the VN gateways settle VND only.
func TestGatewayCheckoutRejectsNonVND(t *testing.T) {
	s := gatewayTestDB(t)
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000")
	plan := gatewaySeedPlan(t, s, ctx, "usd"+suffix, "perpetual", "price_gwu_"+suffix, "usd", 1999)
	RegisterProvider(&gatewayFakeProvider{name: "pay2s", enabled: true})

	_, err := StartGatewayCheckout(ctx, s, &GatewayCheckoutRequest{
		PlanID: plan.ID, Provider: "pay2s", Email: "gw-usd-" + suffix + "@example.com",
	})
	if !errors.Is(err, ErrCurrencyNotSupported) {
		t.Fatalf("want ErrCurrencyNotSupported, got %v", err)
	}
}

// TestGatewayCheckoutRejectsRecurringPlans: subscriptions belong to
// Stripe; a VN gateway only sells one-time (perpetual) plans.
func TestGatewayCheckoutRejectsRecurringPlans(t *testing.T) {
	s := gatewayTestDB(t)
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000")
	RegisterProvider(&gatewayFakeProvider{name: "pay2s", enabled: true})

	for _, licenseType := range []string{"subscription", "trial"} {
		plan := gatewaySeedPlan(t, s, ctx, licenseType+suffix, licenseType, "price_gws_"+licenseType+suffix, "vnd", 99000)
		_, err := StartGatewayCheckout(ctx, s, &GatewayCheckoutRequest{
			PlanID: plan.ID, Provider: "pay2s", Email: "gw-sub-" + suffix + "@example.com",
		})
		var ae *apperr.AppError
		if !errors.As(err, &ae) || ae.Code != "GATEWAY_PLAN_NOT_SUPPORTED" {
			t.Fatalf("%s plan: want GATEWAY_PLAN_NOT_SUPPORTED, got %v", licenseType, err)
		}
	}
}

// TestGatewayFulfilRecordsFailureNotices: a terminal non-success IPN
// closes a pending row and never creates anything.
func TestGatewayFulfilRecordsFailureNotices(t *testing.T) {
	s := gatewayTestDB(t)
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000")
	plan := gatewaySeedPlan(t, s, ctx, "fail"+suffix, "perpetual", "price_gwf_"+suffix, "vnd", 30000)
	fake := &gatewayFakeProvider{name: "zalopay", enabled: true}
	RegisterProvider(fake)

	res, err := StartGatewayCheckout(ctx, s, &GatewayCheckoutRequest{
		PlanID: plan.ID, Provider: "zalopay", Email: "gw-f-" + suffix + "@example.com",
	})
	if err != nil {
		t.Fatalf("start checkout: %v", err)
	}
	ev := &WebhookEvent{
		Provider: "zalopay", ProviderRef: res.Payment.ProviderRef,
		Status: StatusExpired, AmountMinor: res.Order.TotalMinor, Currency: "VND",
	}
	if err := GatewayFulfil(ctx, s, nil, ev); err != nil {
		t.Fatalf("failure notice: %v", err)
	}
	row, _ := s.GetGatewayPaymentByRef(ctx, "zalopay", res.Payment.ProviderRef)
	if row.Status != string(StatusExpired) {
		t.Fatalf("row status = %s, want expired", row.Status)
	}
	paid, _ := s.FindOrderByID(ctx, res.Order.ID)
	if paid.Status != model.OrderStatusPending || paid.LicenseID != "" {
		t.Fatalf("failure notice must not fulfil: %+v", paid)
	}
}
