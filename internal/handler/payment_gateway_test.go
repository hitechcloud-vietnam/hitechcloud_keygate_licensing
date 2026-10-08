package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v82"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/payment"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// gwFakeProvider is the in-memory PaymentProvider the handler tests
// register — the real gateway implementations are being written
// concurrently and are deliberately not depended on here.
type gwFakeProvider struct {
	name     string
	enabled  bool
	verifyFn func(payload []byte) (*payment.WebhookEvent, error)
}

func (f *gwFakeProvider) Name() string  { return f.name }
func (f *gwFakeProvider) Enabled() bool { return f.enabled }
func (f *gwFakeProvider) CreatePayment(ctx context.Context, req payment.CreatePaymentRequest) (*payment.CreatePaymentResult, error) {
	return &payment.CreatePaymentResult{
		ProviderRef: "ref-" + req.OrderID,
		PayURL:      "https://pay.example/" + req.OrderID,
	}, nil
}
func (f *gwFakeProvider) CapturePayment(ctx context.Context, ref string) (*payment.PaymentStatusResult, error) {
	return &payment.PaymentStatusResult{ProviderRef: ref, Status: payment.StatusSucceeded, Currency: "VND"}, nil
}
func (f *gwFakeProvider) RefundPayment(ctx context.Context, req payment.RefundRequest) (*payment.RefundResult, error) {
	return nil, payment.ErrNotSupported
}
func (f *gwFakeProvider) VoidPayment(ctx context.Context, ref, reason string) error { return nil }
func (f *gwFakeProvider) VerifyWebhook(payload []byte) (*payment.WebhookEvent, error) {
	if f.verifyFn != nil {
		return f.verifyFn(payload)
	}
	return nil, payment.ErrWebhookSignatureInvalid
}
func (f *gwFakeProvider) GetPaymentStatus(ctx context.Context, ref string) (*payment.PaymentStatusResult, error) {
	return f.CapturePayment(ctx, ref)
}

func gwRouter(h *PaymentGatewayHandler) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/checkout/gateway-pay", h.GatewayPay)
	r.GET("/api/v1/checkout/gateway-pay/status", h.GatewayPayStatus)
	r.POST("/api/v1/webhook/pay2s", h.WebhookPay2S)
	r.POST("/api/v1/webhook/zalopay", h.WebhookZaloPay)
	r.POST("/api/v1/webhook/payos", h.WebhookPayOS)
	return r
}

func gwDo(r *gin.Engine, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func gwDecode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode response %s: %v", w.Body.String(), err)
	}
	return m
}

func gwTestDB(t *testing.T) *store.Store {
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

// gwSeedPayment creates the pending order + payment row a settled IPN
// needs, without going through a real gateway. The order carries a
// real product + perpetual plan: fulfilment resolves the licence from
// order.Items[0].PlanID, so a fabricated plan id would (rightly) be
// refused as unfulfillable.
func gwSeedPayment(t *testing.T, s *store.Store, ctx context.Context, provider, suffix string, amount int64) (*model.Order, *store.GatewayPayment) {
	t.Helper()
	slug := strings.ToLower(strings.ReplaceAll(suffix, ".", ""))
	prod := &model.Product{Name: "GWP " + suffix, Slug: "gwp-" + slug, Type: "hybrid"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("create product: %v", err)
	}
	plan := &model.Plan{
		ProductID: prod.ID, Name: "GWP Plan " + suffix, Slug: "gwp-plan-" + slug,
		LicenseType: "perpetual", LicenseModel: "standard", Active: true,
	}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatalf("create plan: %v", err)
	}
	order := &model.Order{
		OrderNumber:     "HTC-GWP" + suffix,
		CustomerEmail:   "gwp-" + suffix + "@example.com",
		Currency:        "VND",
		TotalMinor:      amount,
		Status:          model.OrderStatusPending,
		PaymentProvider: provider,
		Items: []*model.OrderItem{{
			PlanID:            plan.ID,
			Description:       "Gateway handler test",
			Quantity:          1,
			UnitAmountMinor:   amount,
			LineTotalMinor:    amount,
			LineSubtotalMinor: amount,
		}},
	}
	if err := s.CreateOrder(ctx, order); err != nil {
		t.Fatalf("create order: %v", err)
	}
	gp := &store.GatewayPayment{
		OrderID: order.ID, Provider: provider, ProviderRef: "ref-" + suffix,
		OrderKey: order.OrderNumber, AmountMinor: amount, Currency: "VND",
		Status: string(payment.StatusPending),
	}
	if err := s.CreateGatewayPayment(ctx, gp); err != nil {
		t.Fatalf("create payment: %v", err)
	}
	return order, gp
}

// TestGatewayPayRequestRefusals: the JSON contract and the provider
// error codes, with no database (the registry is consulted first).
func TestGatewayPayRequestRefusals(t *testing.T) {
	h := NewPaymentGatewayHandler(nil, "https://example.test", nil, nil)
	r := gwRouter(h)

	w := gwDo(r, http.MethodPost, "/api/v1/checkout/gateway-pay", `{}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d %s", w.Code, w.Body.String())
	}

	w = gwDo(r, http.MethodPost, "/api/v1/checkout/gateway-pay",
		`{"plan_id":"p","provider":"ghost","email":"a@b.co"}`)
	if w.Code != http.StatusNotFound || gwDecode(t, w)["error"].(map[string]any)["code"] != "PROVIDER_NOT_FOUND" {
		t.Fatalf("unknown provider: %d %s", w.Code, w.Body.String())
	}

	payment.RegisterProvider(&gwFakeProvider{name: "pay2s", enabled: false})
	w = gwDo(r, http.MethodPost, "/api/v1/checkout/gateway-pay",
		`{"plan_id":"p","provider":"pay2s","email":"a@b.co"}`)
	if w.Code != http.StatusServiceUnavailable || gwDecode(t, w)["error"].(map[string]any)["code"] != "PROVIDER_NOT_CONFIGURED" {
		t.Fatalf("unconfigured provider: %d %s", w.Code, w.Body.String())
	}
}

// TestGatewayIPNSignatureRefusalShapes pins the STRICT per-gateway
// response shapes for a bad signature / malformed body.
func TestGatewayIPNSignatureRefusalShapes(t *testing.T) {
	for _, tc := range []struct {
		provider string
		route    string
		verify   error
		wantCode int
		check    func(t *testing.T, m map[string]any)
	}{
		{"pay2s", "/api/v1/webhook/pay2s", payment.ErrWebhookSignatureInvalid, 400,
			func(t *testing.T, m map[string]any) {
				if m["success"] != false {
					t.Errorf("pay2s refusal body: %v", m)
				}
			}},
		{"pay2s", "/api/v1/webhook/pay2s", payment.ErrWebhookPayloadMalformed, 400,
			func(t *testing.T, m map[string]any) { _ = m }},
		{"zalopay", "/api/v1/webhook/zalopay", payment.ErrWebhookSignatureInvalid, 200,
			func(t *testing.T, m map[string]any) {
				if m["return_code"] != float64(2) || m["return_message"] == nil {
					t.Errorf("zalopay refusal body: %v", m)
				}
			}},
		{"payos", "/api/v1/webhook/payos", payment.ErrWebhookSignatureInvalid, 400,
			func(t *testing.T, m map[string]any) {
				if m["success"] != false || m["code"] != "01" || m["data"] != nil {
					t.Errorf("payos refusal body: %v", m)
				}
			}},
	} {
		payment.RegisterProvider(&gwFakeProvider{name: tc.provider, enabled: true,
			verifyFn: func([]byte) (*payment.WebhookEvent, error) { return nil, tc.verify }})
		h := NewPaymentGatewayHandler(nil, "https://example.test", nil, nil)
		r := gwRouter(h)
		w := gwDo(r, http.MethodPost, tc.route, `{"garbage":`)
		if w.Code != tc.wantCode {
			t.Errorf("%s: code %d, want %d — %s", tc.route, w.Code, tc.wantCode, w.Body.String())
		}
		tc.check(t, gwDecode(t, w))
	}
}

// TestGatewayIPNBodyLimit: a hostile flood is refused before any
// parsing, in the gateway's refusal shape.
func TestGatewayIPNBodyLimit(t *testing.T) {
	payment.RegisterProvider(&gwFakeProvider{name: "pay2s", enabled: true,
		verifyFn: func([]byte) (*payment.WebhookEvent, error) {
			t.Error("verify must not run on an oversized body")
			return nil, nil
		}})
	h := NewPaymentGatewayHandler(nil, "https://example.test", nil, nil)
	r := gwRouter(h)
	w := gwDo(r, http.MethodPost, "/api/v1/webhook/pay2s", strings.Repeat("x", (1<<20)+10))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized body: %d %s", w.Code, w.Body.String())
	}
}

// TestGatewayIPNHappyPathShapes: create → IPN → acknowledged in each
// gateway's success shape; the duplicate delivery acks success again
// and re-fulfils nothing. Full fulfilment needs the database (it
// creates the licence and settles the order).
func TestGatewayIPNHappyPathShapes(t *testing.T) {
	s := gwTestDB(t)
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000")

	for _, tc := range []struct {
		provider string
		route    string
		check    func(t *testing.T, m map[string]any)
	}{
		{"pay2s", "/api/v1/webhook/pay2s", func(t *testing.T, m map[string]any) {
			if m["success"] != true {
				t.Errorf("pay2s ack: %v", m)
			}
		}},
		{"zalopay", "/api/v1/webhook/zalopay", func(t *testing.T, m map[string]any) {
			if m["return_code"] != float64(1) || m["return_message"] != "success" {
				t.Errorf("zalopay ack: %v", m)
			}
		}},
		{"payos", "/api/v1/webhook/payos", func(t *testing.T, m map[string]any) {
			if m["code"] != "00" || m["success"] != true || m["data"] != nil {
				t.Errorf("payos ack: %v", m)
			}
		}},
	} {
		order, gp := gwSeedPayment(t, s, ctx, tc.provider, tc.provider+suffix, 22000)
		payment.RegisterProvider(&gwFakeProvider{name: tc.provider, enabled: true,
			verifyFn: func([]byte) (*payment.WebhookEvent, error) {
				return &payment.WebhookEvent{
					Provider: tc.provider, OrderID: order.OrderNumber, ProviderRef: gp.ProviderRef,
					TransID: "t-" + tc.provider, Status: payment.StatusSucceeded,
					AmountMinor: gp.AmountMinor, Currency: "VND", PaidAt: time.Now(),
				}, nil
			}})
		h := NewPaymentGatewayHandler(s, "https://example.test", nil, nil)
		r := gwRouter(h)

		w := gwDo(r, http.MethodPost, tc.route, `{"x":1}`)
		if w.Code != http.StatusOK {
			t.Fatalf("%s first delivery: %d %s", tc.route, w.Code, w.Body.String())
		}
		tc.check(t, gwDecode(t, w))

		// The sale settled: order paid with a licence, row succeeded.
		paid, err := s.FindOrderByID(ctx, order.ID)
		if err != nil || paid.Status != model.OrderStatusPaid || paid.LicenseID == "" {
			t.Fatalf("%s fulfilment: %v %+v", tc.route, err, paid)
		}
		if _, err := s.FindLicenseByID(ctx, paid.LicenseID); err != nil {
			t.Fatalf("%s licence: %v", tc.route, err)
		}

		// Duplicate delivery: ack success, nothing re-fulfilled.
		w = gwDo(r, http.MethodPost, tc.route, `{"x":1}`)
		if w.Code != http.StatusOK {
			t.Fatalf("%s duplicate: %d %s", tc.route, w.Code, w.Body.String())
		}
		tc.check(t, gwDecode(t, w))
		again, _ := s.FindOrderByID(ctx, order.ID)
		if again.LicenseID != paid.LicenseID {
			t.Fatalf("%s duplicate minted a second licence: %s != %s", tc.route, again.LicenseID, paid.LicenseID)
		}
	}
}

// TestGatewayIPNAmountMismatchShapes: a mismatched amount is refused
// in the per-gateway shape and the sale is never fulfilled.
func TestGatewayIPNAmountMismatchShapes(t *testing.T) {
	s := gwTestDB(t)
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000")

	for _, tc := range []struct {
		provider string
		route    string
		wantCode int
	}{
		{"pay2s", "/api/v1/webhook/pay2s", 400},
		{"zalopay", "/api/v1/webhook/zalopay", 200},
		{"payos", "/api/v1/webhook/payos", 400},
	} {
		order, gp := gwSeedPayment(t, s, ctx, tc.provider, "mm"+tc.provider+suffix, 22000)
		payment.RegisterProvider(&gwFakeProvider{name: tc.provider, enabled: true,
			verifyFn: func([]byte) (*payment.WebhookEvent, error) {
				return &payment.WebhookEvent{
					Provider: tc.provider, ProviderRef: gp.ProviderRef, OrderID: order.OrderNumber,
					Status: payment.StatusSucceeded, AmountMinor: gp.AmountMinor + 1, Currency: "VND",
				}, nil
			}})
		h := NewPaymentGatewayHandler(s, "https://example.test", nil, nil)
		r := gwRouter(h)

		w := gwDo(r, http.MethodPost, tc.route, `{"x":1}`)
		if w.Code != tc.wantCode {
			t.Fatalf("%s mismatch: %d %s", tc.route, w.Code, w.Body.String())
		}
		paid, _ := s.FindOrderByID(ctx, order.ID)
		if paid.Status != model.OrderStatusPending || paid.LicenseID != "" {
			t.Fatalf("%s mismatched IPN must not fulfil: %+v", tc.route, paid)
		}
	}
}

// TestGatewayPayStatusEndpoint: the browser return page's poll.
func TestGatewayPayStatusEndpoint(t *testing.T) {
	s := gwTestDB(t)
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000")
	order, _ := gwSeedPayment(t, s, ctx, "pay2s", "st"+suffix, 123000)

	h := NewPaymentGatewayHandler(s, "https://example.test", nil, nil)
	r := gwRouter(h)

	w := gwDo(r, http.MethodGet, "/api/v1/checkout/gateway-pay/status?order="+order.OrderNumber, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status: %d %s", w.Code, w.Body.String())
	}
	data := gwDecode(t, w)["data"].(map[string]any)
	if data["status"] != "pending" || data["provider"] != "pay2s" {
		t.Fatalf("status payload: %v", data)
	}

	w = gwDo(r, http.MethodGet, "/api/v1/checkout/gateway-pay/status?order=HTC-NOPE", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown order: %d %s", w.Code, w.Body.String())
	}
}

// TestGatewayPayCheckoutContract: the create endpoint's response
// contract (pay_url / provider_ref / order_number) against a stubbed
// price and a fake gateway.
func TestGatewayPayCheckoutContract(t *testing.T) {
	s := gwTestDB(t)
	ctx := context.Background()
	suffix := time.Now().Format("150405.000000")

	// Stub the catalogue price lookup (VND, one-off).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"price_gwc_%s","object":"price","unit_amount":220000,"currency":"vnd","type":"one_time"}`, suffix)
	}))
	t.Cleanup(srv.Close)
	gwSwapStripeBackend(t, srv.URL)

	prod := &model.Product{Name: "GWC " + suffix, Slug: "gwc-" + suffix, Type: "hybrid"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatal(err)
	}
	plan := &model.Plan{
		ProductID: prod.ID, Name: "GWC Plan " + suffix, Slug: "gwc-plan-" + suffix,
		LicenseType: "perpetual", LicenseModel: "standard",
		StripePriceID: "price_gwc_" + suffix, Active: true,
	}
	if err := s.CreatePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	payment.RegisterProvider(&gwFakeProvider{name: "payos", enabled: true})

	h := NewPaymentGatewayHandler(s, "https://example.test", nil, nil)
	r := gwRouter(h)
	body := fmt.Sprintf(`{"plan_id":%q,"provider":"payos","email":"gwc-%s@example.com"}`, plan.ID, suffix)
	w := gwDo(r, http.MethodPost, "/api/v1/checkout/gateway-pay", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("checkout: %d %s", w.Code, w.Body.String())
	}
	data := gwDecode(t, w)["data"].(map[string]any)
	for _, k := range []string{"pay_url", "provider_ref", "order_number"} {
		if s, _ := data[k].(string); s == "" {
			t.Fatalf("response missing %s: %v", k, data)
		}
	}
	if data["amount_minor"] != float64(220000) || data["currency"] != "VND" {
		t.Fatalf("money in response: %v", data)
	}
}

// gwSwapStripeBackend points stripe-go at the stub server for the
// duration of the test (mirrors payment.stubStripe, which lives in
// another package's test binary and cannot be shared).
func gwSwapStripeBackend(t *testing.T, rawURL string) {
	t.Helper()
	prevKey := stripe.Key
	stripe.Key = "sk_test_stub"
	stripe.SetBackend(stripe.APIBackend, stripe.GetBackendWithConfig(stripe.APIBackend, &stripe.BackendConfig{
		URL:           stripe.String(rawURL),
		LeveledLogger: &stripe.LeveledLogger{Level: stripe.LevelNull},
	}))
	t.Cleanup(func() {
		stripe.Key = prevKey
		stripe.SetBackend(stripe.APIBackend, nil)
	})
}
