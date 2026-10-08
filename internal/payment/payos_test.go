package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/config"
)

// ─── helpers (payos-prefixed: compile-collision rule) ───

func payosTestProvider(baseURL string) *PayOSProvider {
	return NewPayOS(config.PayOSConfig{
		ClientID:    "test-client-id",
		APIKey:      "test-api-key",
		ChecksumKey: "test_checksum_key",
		BaseURL:     baseURL,
	})
}

func payosTestData(t *testing.T, dataRaw string) map[string]any {
	t.Helper()
	env, err := payosParseEnvelope([]byte(`{"data":` + dataRaw + `}`))
	if err != nil || env.Data == nil {
		t.Fatalf("payosTestData(%s): %v", dataRaw, err)
	}
	return env.Data
}

func payosTestWebhookEnvelope(t *testing.T, key, dataRaw string, success bool, topCode string) []byte {
	t.Helper()
	data := payosTestData(t, dataRaw)
	sig := payosSignData(key, data)
	return []byte(fmt.Sprintf(`{"code":%q,"desc":"test","success":%v,"data":%s,"signature":"%s"}`,
		topCode, success, dataRaw, sig))
}

// ─── 1. Signature vectors (official payOSHQ/payos-lib-* testCases.json,
//
//	checksum key "test_checksum_key") + exact encoding pins ───
func TestPayOSCreateSignatureVectors(t *testing.T) {
	// "create payment link full fields" — only the five canonical fields sign.
	got := payosSignCreate("test_checksum_key", 3300, 0, "http://localhost", "ABC456", "http://localhost")
	const want = "189864813370cdf819974dc3f63c3a93a7e7af0a0d10b21344b14e4f7358999b"
	if got != want {
		t.Fatalf("create signature (ABC456) = %s, want %s", got, want)
	}

	// "create payment link with Vietnamese description".
	got = payosSignCreate("test_checksum_key", 2000, 0, "http://localhost", "Thanh toán đơn hàng", "http://localhost")
	const wantVN = "d8b7b0e5d19edfe7b1f53dedeb8d1c7c2b62fabd7eb320dbc514c58f13e9e2c5"
	if got != wantVN {
		t.Fatalf("create signature (VN) = %s, want %s", got, wantVN)
	}

	// The documented formula cross-check (exact raw string).
	raw := "amount=22000&cancelUrl=https://shop/cancel&description=Thanh toán đơn hàng&orderCode=123456789&returnUrl=https://shop/return"
	if a, b := payosHMACHex("k", raw), payosSignCreate("k", 22000, 123456789, "https://shop/cancel", "Thanh toán đơn hàng", "https://shop/return"); a != b {
		t.Fatalf("payosSignCreate deviates from documented raw string")
	}
}

func TestPayOSDataEncodeRules(t *testing.T) {
	// Exact encoding pin: null→"" , "undefined" string→"", bool→true,
	// number literal, raw unicode, arrays keep order but sort element keys.
	data := payosTestData(t, `{"amount":3000,"desc":"Thành công","note":null,"objs":[{"z":1,"a":"x"}],"orderCode":42,"tags":["b","a"],"flag":true,"undef":"undefined"}`)
	want := `amount=3000&desc=Thành công&flag=true&note=&objs=[{"a":"x","z":1}]&orderCode=42&tags=["b","a"]&undef=`
	if got := payosEncodeData(data); got != want {
		t.Fatalf("payosEncodeData:\n got %q\nwant %q", got, want)
	}

	// "null" string quirk (JS SDK parity) and empty object.
	if got := payosEncodeData(payosTestData(t, `{"k":"null"}`)); got != "k=" {
		t.Fatalf("null-string quirk: got %q", got)
	}
	if got := payosEncodeData(map[string]any{}); got != "" {
		t.Fatalf("empty data: got %q", got)
	}
}

func TestPayOSDataSignatureVectors(t *testing.T) {
	// "webhook" — the real IPN data shape (null-free, empty strings kept).
	webhookData := `{"accountNumber":"0123456789","amount":20000,"description":"thanh toan","reference":"FT-REFERENCE","transactionDateTime":"2025-12-12 09:00:00","virtualAccountNumber":"","counterAccountBankId":"01202001","counterAccountBankName":"","counterAccountName":"NGUYEN VAN A","counterAccountNumber":"9876543210","virtualAccountName":"","currency":"VND","orderCode":0,"paymentLinkId":"payment-link-id","code":"00","desc":"success"}`
	wantEncoded := `accountNumber=0123456789&amount=20000&code=00&counterAccountBankId=01202001&counterAccountBankName=&counterAccountName=NGUYEN VAN A&counterAccountNumber=9876543210&currency=VND&desc=success&description=thanh toan&orderCode=0&paymentLinkId=payment-link-id&reference=FT-REFERENCE&transactionDateTime=2025-12-12 09:00:00&virtualAccountName=&virtualAccountNumber=`
	data := payosTestData(t, webhookData)
	if got := payosEncodeData(data); got != wantEncoded {
		t.Fatalf("webhook encoded:\n got %q\nwant %q", got, wantEncoded)
	}
	const wantSig = "302b3becca1672dff99daafae2965f40e48ea3ca39453e4bf37fbcc26807a0e8"
	if got := payosSignData("test_checksum_key", data); got != wantSig {
		t.Fatalf("webhook signature = %s, want %s", got, wantSig)
	}

	// "paid payment link with 1 transaction" — array + nulls inside elements.
	txnData := `{"id":"payment-link-id","orderCode":0,"amount":2000,"amountPaid":2000,"amountRemaining":0,"status":"PAID","createdAt":"2025-12-12T09:00:00+07:00","transactions":[{"accountNumber":"0123456789","amount":2000,"counterAccountBankId":"01202001","counterAccountBankName":null,"counterAccountName":"NGUYEN VAN A","counterAccountNumber":"9876543210","description":"TRANSACTION DESCRIPTION","reference":"FT-REFERENCE","transactionDateTime":"2025-12-12T09:00:00+07:00","virtualAccountName":null,"virtualAccountNumber":null}],"canceledAt":null,"cancellationReason":null}`
	wantTxnEncoded := `amount=2000&amountPaid=2000&amountRemaining=0&canceledAt=&cancellationReason=&createdAt=2025-12-12T09:00:00+07:00&id=payment-link-id&orderCode=0&status=PAID&transactions=[{"accountNumber":"0123456789","amount":2000,"counterAccountBankId":"01202001","counterAccountBankName":null,"counterAccountName":"NGUYEN VAN A","counterAccountNumber":"9876543210","description":"TRANSACTION DESCRIPTION","reference":"FT-REFERENCE","transactionDateTime":"2025-12-12T09:00:00+07:00","virtualAccountName":null,"virtualAccountNumber":null}]`
	if got := payosEncodeData(payosTestData(t, txnData)); got != wantTxnEncoded {
		t.Fatalf("txn encoded:\n got %q\nwant %q", got, wantTxnEncoded)
	}
	if got := payosSignData("test_checksum_key", payosTestData(t, txnData)); got != "6af5e2c9a28256c140169ed624114b43295915d7b6e2fa6278b17a7c43aadefd" {
		t.Fatalf("txn signature = %s", got)
	}

	// "invoice information with many invoices" — nulls inside array elements
	// stay JSON null (only TOP-LEVEL nulls collapse to "").
	invData := `{"invoices":[{"invoiceId":"invoice-id","invoiceNumber":"invoiceNo","issuedTimestamp":1765504800,"issuedDatetime":"2025-12-12T09:00:00+07:00","transactionId":"transactionId","reservationCode":"reservationCode","codeOfTax":"codeOfTax"},{"invoiceId":"invoice-id","invoiceNumber":null,"issuedTimestamp":null,"issuedDatetime":null,"transactionId":null,"reservationCode":null,"codeOfTax":null}]}`
	if got := payosSignData("test_checksum_key", payosTestData(t, invData)); got != "8ceca4558787d5ec58b24caaf5aaa7693df83242efbb28e7b70cf3391f3a1138" {
		t.Fatalf("invoices signature = %s", got)
	}

	// "body type empty string".
	if got := payosSignData("test_checksum_key", map[string]any{}); got != "d9dd60ea06e1ee2dc960267c7a798f0c70307a4783ac0460d015772412c37938" {
		t.Fatalf("empty-object signature = %s", got)
	}
}

// ─── 2. orderCode derivation ───

func TestPayOSOrderCode(t *testing.T) {
	// Golden pin: SHA-256("abc") = ba7816bf8f01cfea… → first 53 bits.
	if got := payosOrderCode("abc"); got != 6560798095827001 {
		t.Fatalf(`payosOrderCode("abc") = %d, want 6560798095827001`, got)
	}
	// Deterministic + range: positive and < 2^53 (JS-safe, payOS limit).
	for _, id := range []string{"", "HTC-1", "HTC-2", "HTC-20261008-ABCDEF", strings.Repeat("x", 64)} {
		a := payosOrderCode(id)
		if b := payosOrderCode(id); a != b {
			t.Fatalf("payosOrderCode(%q) not deterministic: %d vs %d", id, a, b)
		}
		if a <= 0 || a >= 1<<53 {
			t.Fatalf("payosOrderCode(%q) = %d, want 0 < v < 2^53", id, a)
		}
	}
	if payosOrderCode("HTC-1") == payosOrderCode("HTC-2") {
		t.Fatalf("distinct order ids must derive distinct order codes")
	}
}

// ─── 3. VerifyWebhook ───

func TestPayOSVerifyWebhook(t *testing.T) {
	// Official vector end-to-end: signature 302b… over the official data.
	dataRaw := `{"accountNumber":"0123456789","amount":20000,"description":"thanh toan","reference":"FT-REFERENCE","transactionDateTime":"2025-12-12 09:00:00","virtualAccountNumber":"","counterAccountBankId":"01202001","counterAccountBankName":"","counterAccountName":"NGUYEN VAN A","counterAccountNumber":"9876543210","virtualAccountName":"","currency":"VND","orderCode":0,"paymentLinkId":"payment-link-id","code":"00","desc":"success"}`
	payload := []byte(`{"code":"00","desc":"success","success":true,"data":` + dataRaw +
		`,"signature":"302b3becca1672dff99daafae2965f40e48ea3ca39453e4bf37fbcc26807a0e8"}`)

	ev, err := payosTestProvider("http://payos.invalid").VerifyWebhook(payload)
	if err != nil {
		t.Fatalf("VerifyWebhook: %v", err)
	}
	if ev.Provider != ProviderPayOS || ev.OrderID != "" || ev.ProviderRef != "0" ||
		ev.TransID != "FT-REFERENCE" || ev.Status != StatusSucceeded ||
		ev.AmountMinor != 20000 || ev.Currency != "VND" {
		t.Fatalf("event mismatch: %+v", ev)
	}
	if payosStr(ev.Raw["paymentLinkId"]) != "payment-link-id" {
		t.Fatalf("Raw must keep the data map: %+v", ev.Raw)
	}
	if ev.PaidAt.IsZero() {
		t.Fatalf("succeeded event should parse transactionDateTime, got zero PaidAt")
	}

	// Self-signed realistic event: orderCode → ProviderRef round-trip.
	data := `{"orderCode":8123456789012,"amount":22000,"description":"Thanh toán đơn hàng","accountNumber":"113366668888","reference":"FT123","transactionDateTime":"2026-10-08 10:00:00","currency":"VND","paymentLinkId":"plink_1","code":"00","desc":"Thành công","counterAccountBankId":null,"counterAccountBankName":null,"counterAccountName":null,"counterAccountNumber":null,"virtualAccountName":"","virtualAccountNumber":""}`
	ev, err = payosTestProvider("http://payos.invalid").VerifyWebhook(
		payosTestWebhookEnvelope(t, "test_checksum_key", data, true, "00"))
	if err != nil {
		t.Fatalf("VerifyWebhook self-signed: %v", err)
	}
	if ev.ProviderRef != "8123456789012" || ev.AmountMinor != 22000 || ev.Status != StatusSucceeded {
		t.Fatalf("self-signed event mismatch: %+v", ev)
	}
}

func TestPayOSVerifyWebhookRejects(t *testing.T) {
	p := payosTestProvider("http://payos.invalid")
	const data = `{"orderCode":1,"amount":1000,"description":"d","accountNumber":"1","reference":"r","transactionDateTime":"2026-10-08 10:00:00","currency":"VND","paymentLinkId":"p","code":"00","desc":"ok"}`

	// Valid signature, TAMPERED data → must fail closed.
	tampered := strings.Replace(data, `"amount":1000`, `"amount":1001`, 1)
	bad := payosTestWebhookEnvelope(t, "test_checksum_key", tampered, true, "00")
	bad = []byte(strings.Replace(string(bad), `"amount":1001`, `"amount":1000`, 1)) // data tampered AFTER signing
	if _, err := p.VerifyWebhook(bad); !errors.Is(err, ErrWebhookSignatureInvalid) {
		t.Fatalf("tampered data: err = %v, want ErrWebhookSignatureInvalid", err)
	}

	// Tampered signature.
	bad = payosTestWebhookEnvelope(t, "test_checksum_key", data, true, "00")
	bad = []byte(strings.Replace(string(bad), `"signature":"`, `"signature":"0`, 1))
	if _, err := p.VerifyWebhook(bad); !errors.Is(err, ErrWebhookSignatureInvalid) {
		t.Fatalf("tampered signature: err = %v, want ErrWebhookSignatureInvalid", err)
	}

	// Unsigned data with valid JSON: signature must be checked BEFORE field use.
	unsigned := []byte(`{"code":"00","desc":"success","success":true,"data":` + data + `}`)
	if _, err := p.VerifyWebhook(unsigned); !errors.Is(err, ErrWebhookSignatureInvalid) {
		t.Fatalf("unsigned data: err = %v, want ErrWebhookSignatureInvalid", err)
	}

	// Missing data / broken JSON / wrong key.
	if _, err := p.VerifyWebhook([]byte(`{"code":"00","success":true,"signature":"aa"}`)); !errors.Is(err, ErrWebhookPayloadMalformed) {
		t.Fatalf("missing data: err = %v, want ErrWebhookPayloadMalformed", err)
	}
	if _, err := p.VerifyWebhook([]byte(`{not json`)); !errors.Is(err, ErrWebhookPayloadMalformed) {
		t.Fatalf("broken JSON: err = %v, want ErrWebhookPayloadMalformed", err)
	}
	if _, err := payosTestProvider("http://payos.invalid").VerifyWebhook(
		payosTestWebhookEnvelope(t, "wrong-key", data, true, "00")); !errors.Is(err, ErrWebhookSignatureInvalid) {
		t.Fatalf("wrong key: err = %v, want ErrWebhookSignatureInvalid", err)
	}

	// No checksum key configured → fail closed.
	if _, err := (&PayOSProvider{}).VerifyWebhook(
		payosTestWebhookEnvelope(t, "test_checksum_key", data, true, "00")); !errors.Is(err, ErrProviderNotConfigured) {
		t.Fatalf("unconfigured: err = %v, want ErrProviderNotConfigured", err)
	}
}

func TestPayOSVerifyWebhookStatusAndFloatTrap(t *testing.T) {
	p := payosTestProvider("http://payos.invalid")

	// success=false (or data.code != "00") → StatusFailed. NOTE: the top-level
	// success flag is NOT signed — failure events are advisory (reconcile via
	// GetPaymentStatus before acting).
	data := `{"orderCode":7,"amount":5000,"description":"d","code":"00","desc":"ok"}`
	ev, err := p.VerifyWebhook(payosTestWebhookEnvelope(t, "test_checksum_key", data, false, "00"))
	if err != nil || ev.Status != StatusFailed {
		t.Fatalf("success=false: ev=%+v err=%v, want StatusFailed", ev, err)
	}
	// SIGNED data.code != "00" → StatusFailed (the pinned map is
	// success && data.code=="00" → succeeded, everything else → failed).
	badCode := `{"orderCode":7,"amount":5000,"description":"d","code":"01","desc":"rejected"}`
	ev, err = p.VerifyWebhook(payosTestWebhookEnvelope(t, "test_checksum_key", badCode, true, "00"))
	if err != nil || ev.Status != StatusFailed {
		t.Fatalf("data code 01: ev=%+v err=%v, want StatusFailed", ev, err)
	}
	// The top-level code is NOT covered by the signature — it must NOT
	// downgrade a signed success (advisory only; reconcile via GetPaymentStatus).
	ev, err = p.VerifyWebhook(payosTestWebhookEnvelope(t, "test_checksum_key", data, true, "01"))
	if err != nil || ev.Status != StatusSucceeded {
		t.Fatalf("unsigned top code must not downgrade: ev=%+v err=%v, want StatusSucceeded", ev, err)
	}

	// Money is int64, never float64: 2^53+1 must survive EXACTLY.
	big := `{"orderCode":7,"amount":9007199254740993,"description":"d","code":"00","desc":"ok"}`
	ev, err = p.VerifyWebhook(payosTestWebhookEnvelope(t, "test_checksum_key", big, true, "00"))
	if err != nil {
		t.Fatalf("float trap: %v", err)
	}
	if ev.AmountMinor != 9007199254740993 {
		t.Fatalf("float trap: AmountMinor = %d, want 9007199254740993", ev.AmountMinor)
	}
}

// ─── 4. CreatePayment (httptest) ───

func TestPayOSCreatePayment(t *testing.T) {
	var gotMethod, gotPath, gotClientID, gotAPIKey, gotCT string
	var gotBody map[string]any
	var bodyErr error
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotClientID = r.Header.Get("x-client-id")
		gotAPIKey = r.Header.Get("x-api-key")
		gotCT = r.Header.Get("Content-Type")
		dec := json.NewDecoder(r.Body)
		dec.UseNumber()
		gotBody = map[string]any{}
		if err := dec.Decode(&gotBody); err != nil {
			bodyErr = err
		}
		oc := payosOrderCode("HTC-1001")
		fmt.Fprintf(w, `{"code":"00","desc":"success","data":{"id":"plink_1","orderCode":%d,"amount":22000,"description":"d","status":"PENDING","checkoutUrl":"https://pay.payos.vn/web/plink_1","qrCode":"QRDATA","paymentLinkId":"plink_1"},"signature":""}`, oc)
	}))
	defer srv.Close()

	p := payosTestProvider(srv.URL)
	res, err := p.CreatePayment(context.Background(), CreatePaymentRequest{
		OrderID:     "HTC-1001",
		AmountMinor: 22000,
		Currency:    "VND",
		Description: "Thanh toán đơn hàng",
		BuyerName:   "Nguyen Van A",
		BuyerEmail:  "buyer@example.com",
		ReturnURL:   "https://shop.example/return",
		CancelURL:   "https://shop.example/cancel",
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if bodyErr != nil {
		t.Fatalf("request body: %v", bodyErr)
	}
	if gotMethod != http.MethodPost || gotPath != "/v2/payment-requests" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if gotClientID != "test-client-id" || gotAPIKey != "test-api-key" || gotCT != "application/json" {
		t.Fatalf("headers: x-client-id=%q x-api-key=%q ct=%q", gotClientID, gotAPIKey, gotCT)
	}
	oc := payosOrderCode("HTC-1001")
	if payosStr(gotBody["orderCode"]) != strconv.FormatInt(oc, 10) || payosStr(gotBody["amount"]) != "22000" ||
		payosStr(gotBody["description"]) != "Thanh toán đơn hàng" ||
		payosStr(gotBody["cancelUrl"]) != "https://shop.example/cancel" ||
		payosStr(gotBody["returnUrl"]) != "https://shop.example/return" ||
		payosStr(gotBody["buyerName"]) != "Nguyen Van A" {
		t.Fatalf("request body mismatch: %+v", gotBody)
	}
	// expiredAt: Unix seconds ≈ now + 15min (Int32).
	if at, ok := payosInt(gotBody["expiredAt"]); !ok || at < time.Now().Unix()+13*60 || at > time.Now().Unix()+17*60 {
		t.Fatalf("expiredAt = %v, want now+15min", gotBody["expiredAt"])
	}
	// Exact signature over the five canonical fields as sent.
	wantSig := payosSignCreate("test_checksum_key", 22000, oc,
		"https://shop.example/cancel", "Thanh toán đơn hàng", "https://shop.example/return")
	if payosStr(gotBody["signature"]) != wantSig {
		t.Fatalf("body signature = %q, want %q", payosStr(gotBody["signature"]), wantSig)
	}
	// Response mapping.
	if res.ProviderRef != strconv.FormatInt(oc, 10) || res.PayURL != "https://pay.payos.vn/web/plink_1" ||
		res.QRCode != "QRDATA" || res.ExpiresAt.IsZero() {
		t.Fatalf("result mismatch: %+v", res)
	}
}

func TestPayOSCreatePaymentErrorsAndShapes(t *testing.T) {
	var lastBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dec := json.NewDecoder(r.Body)
		dec.UseNumber()
		lastBody = map[string]any{}
		dec.Decode(&lastBody)
		fmt.Fprint(w, `{"code":"01","desc":"invalid params","data":null,"signature":""}`)
	}))
	defer srv.Close()
	p := payosTestProvider(srv.URL)
	base := CreatePaymentRequest{OrderID: "HTC-2", AmountMinor: 1000, Currency: "VND",
		Description: "d", ReturnURL: "https://shop/return", CancelURL: "https://shop/cancel"}

	// code != "00" → error carrying code + desc.
	if _, err := p.CreatePayment(context.Background(), base); err == nil || !strings.Contains(err.Error(), "invalid params") {
		t.Fatalf("code 01: err = %v", err)
	}
	// Currency discipline.
	eu := base
	eu.Currency = "EUR"
	if _, err := p.CreatePayment(context.Background(), eu); !errors.Is(err, ErrCurrencyNotSupported) {
		t.Fatalf("EUR: err = %v, want ErrCurrencyNotSupported", err)
	}
	// returnUrl is REQUIRED by the API.
	nr := base
	nr.ReturnURL = ""
	if _, err := p.CreatePayment(context.Background(), nr); err == nil {
		t.Fatalf("missing return URL must fail")
	}
	// Unconfigured provider.
	if _, err := NewPayOS(config.PayOSConfig{}).CreatePayment(context.Background(), base); !errors.Is(err, ErrProviderNotConfigured) {
		t.Fatalf("unconfigured: err = %v", err)
	}

	// cancelUrl falls back to returnUrl; empty description falls back;
	// Extra["items"] feeds the optional items array; both before signing.
	ok := base
	ok.CancelURL = ""
	ok.Description = ""
	ok.Extra = map[string]string{"items": `[{"name":"Pro","quantity":1,"price":1000}]`}
	// Handler above always errors on code 01 — that is fine: the captured body
	// is what these asserts exercise.
	_, _ = p.CreatePayment(context.Background(), ok)
	if payosStr(lastBody["cancelUrl"]) != "https://shop/return" {
		t.Fatalf("cancelUrl fallback: %+v", lastBody)
	}
	if payosStr(lastBody["description"]) != "Thanh toan don hang HTC-2" {
		t.Fatalf("description fallback: %+v", lastBody)
	}
	items, _ := lastBody["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items: %+v", lastBody["items"])
	}
	bad := base
	bad.Extra = map[string]string{"items": `not json`}
	if _, err := p.CreatePayment(context.Background(), bad); err == nil {
		t.Fatalf("bad items JSON must fail")
	}
}

func TestPayOSCreatePaymentDescriptionTruncation(t *testing.T) {
	var gotDesc string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dec := json.NewDecoder(r.Body)
		dec.UseNumber()
		body := map[string]any{}
		dec.Decode(&body)
		gotDesc = payosStr(body["description"])
		fmt.Fprint(w, `{"code":"00","desc":"ok","data":{"checkoutUrl":"u"},"signature":""}`)
	}))
	defer srv.Close()
	long := strings.Repeat("ế", 300) // 300 runes, 600 bytes
	if _, err := payosTestProvider(srv.URL).CreatePayment(context.Background(), CreatePaymentRequest{
		OrderID: "HTC-3", AmountMinor: 1, Currency: "VND", Description: long,
		ReturnURL: "https://shop/return",
	}); err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}
	if n := len([]rune(gotDesc)); n != 256 {
		t.Fatalf("description runes = %d, want 256", n)
	}
}

// ─── 5. GetPaymentStatus / Capture / Void / Refund / ConfirmWebhook ───

func TestPayOSGetPaymentStatusEnumMapping(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   PaymentStatus
	}{
		{"PENDING", StatusPending},
		{"PROCESSING", StatusPending},
		{"UNDERPAID", StatusPending}, // partial — never succeeded
		{"PAID", StatusSucceeded},
		{"EXPIRED", StatusExpired},
		{"CANCELLED", StatusCancelled},
		{"FAILED", StatusFailed},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"code":"00","desc":"success","data":{"id":"plink_1","orderCode":42,"amount":9007199254740993,"amountPaid":0,"amountRemaining":9007199254740993,"status":"%s","createdAt":"2025-12-12T09:00:00+07:00","transactions":[{"reference":"FT-1","transactionDateTime":"2025-12-12T09:00:00+07:00","amount":9007199254740993}]},"signature":""}`, tc.status)
		}))
		res, err := payosTestProvider(srv.URL).GetPaymentStatus(context.Background(), "42")
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", tc.status, err)
		}
		if res.Status != tc.want {
			t.Fatalf("%s → %s, want %s", tc.status, res.Status, tc.want)
		}
		if res.TransID != "FT-1" || res.AmountMinor != 9007199254740993 || res.Currency != "VND" {
			t.Fatalf("%s result mismatch: %+v", tc.status, res)
		}
		if res.Status == StatusSucceeded && res.PaidAt.IsZero() {
			t.Fatalf("PAID must parse PaidAt from transactions")
		}
	}

	// Unknown status fails closed (never silently "paid").
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":"00","desc":"success","data":{"status":"WEIRD","amount":1},"signature":""}`)
	}))
	defer srv.Close()
	if _, err := payosTestProvider(srv.URL).GetPaymentStatus(context.Background(), "42"); err == nil {
		t.Fatalf("unknown status must error")
	}

	// Top-level data.reference wins when present (webhook-shaped payload).
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":"00","desc":"success","data":{"status":"PAID","amount":5,"reference":"TOP-REF"},"signature":""}`)
	}))
	defer srv2.Close()
	res, err := payosTestProvider(srv2.URL).GetPaymentStatus(context.Background(), "42")
	if err != nil || res.TransID != "TOP-REF" {
		t.Fatalf("top-level reference: res=%+v err=%v", res, err)
	}
}

func TestPayOSCaptureAliasesStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v2/payment-requests/77" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		fmt.Fprint(w, `{"code":"00","desc":"success","data":{"status":"PAID","amount":900,"reference":"FT-9"},"signature":""}`)
	}))
	defer srv.Close()
	res, err := payosTestProvider(srv.URL).CapturePayment(context.Background(), "77")
	if err != nil || res.Status != StatusSucceeded || res.TransID != "FT-9" {
		t.Fatalf("CapturePayment: res=%+v err=%v", res, err)
	}
}

func TestPayOSVoidPayment(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	cancelCode := "00"
	statusJSON := `{"code":"00","desc":"success","data":{"status":"PENDING","amount":1},"signature":""}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		if r.Method == http.MethodPost {
			dec := json.NewDecoder(r.Body)
			dec.UseNumber()
			gotBody = map[string]any{}
			dec.Decode(&gotBody)
			fmt.Fprintf(w, `{"code":"%s","desc":"d","data":null,"signature":""}`, cancelCode)
			return
		}
		fmt.Fprint(w, statusJSON)
	}))
	defer srv.Close()
	p := payosTestProvider(srv.URL)

	// Happy path: exact request shape.
	if err := p.VoidPayment(context.Background(), "12345", "buyer changed mind"); err != nil {
		t.Fatalf("VoidPayment: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v2/payment-requests/12345/cancel" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if len(gotBody) != 1 || payosStr(gotBody["cancellationReason"]) != "buyer changed mind" {
		t.Fatalf("cancel body = %+v", gotBody)
	}

	// Idempotent: cancel refuses, but the link is already CANCELLED → nil.
	cancelCode = "01"
	statusJSON = `{"code":"00","desc":"success","data":{"status":"CANCELLED","amount":1},"signature":""}`
	if err := p.VoidPayment(context.Background(), "12345", "retry"); err != nil {
		t.Fatalf("idempotent void: %v", err)
	}
	// Real failure: cancel refuses and the link is still PENDING.
	statusJSON = `{"code":"00","desc":"success","data":{"status":"PENDING","amount":1},"signature":""}`
	err := p.VoidPayment(context.Background(), "12345", "retry")
	var apiErr payosAPIError
	if !errors.As(err, &apiErr) || apiErr.Code != "01" {
		t.Fatalf("void failure: err = %v, want payosAPIError code 01", err)
	}
}

func TestPayOSRefundNotSupported(t *testing.T) {
	if _, err := payosTestProvider("http://payos.invalid").RefundPayment(
		context.Background(), RefundRequest{ProviderRef: "1", AmountMinor: 1000}); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("err = %v, want ErrNotSupported", err)
	}
}

func TestPayOSConfirmWebhook(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]any
	reply := `{"code":"00","desc":"success","data":{"webhookUrl":"https://shop.example/ipn/payos"},"signature":""}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		dec := json.NewDecoder(r.Body)
		dec.UseNumber()
		gotBody = map[string]any{}
		dec.Decode(&gotBody)
		fmt.Fprint(w, reply)
	}))
	defer srv.Close()
	p := payosTestProvider(srv.URL)

	// POST /confirm-webhook (root path — NOT /v2/payment-requests/…).
	if err := p.ConfirmWebhook(context.Background(), "https://shop.example/ipn/payos"); err != nil {
		t.Fatalf("ConfirmWebhook: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/confirm-webhook" {
		t.Fatalf("request = %s %s, want POST /confirm-webhook", gotMethod, gotPath)
	}
	if payosStr(gotBody["webhookUrl"]) != "https://shop.example/ipn/payos" {
		t.Fatalf("body = %+v", gotBody)
	}
	reply = `{"code":"01","desc":"webhook url invalid","data":null,"signature":""}`
	if err := p.ConfirmWebhook(context.Background(), "https://bad"); err == nil {
		t.Fatalf("code 01 must error")
	}
	if err := NewPayOS(config.PayOSConfig{}).ConfirmWebhook(context.Background(), "https://x"); !errors.Is(err, ErrProviderNotConfigured) {
		t.Fatalf("unconfigured: %v", err)
	}
}

func TestPayOSNameAndEnabled(t *testing.T) {
	if got := NewPayOS(config.PayOSConfig{}).Name(); got != ProviderPayOS {
		t.Fatalf("Name() = %q", got)
	}
	full := NewPayOS(config.PayOSConfig{ClientID: "a", APIKey: "b", ChecksumKey: "c", BaseURL: "https://x/"})
	if !full.Enabled() {
		t.Fatalf("all credentials set must be enabled")
	}
	for _, cfg := range []config.PayOSConfig{
		{APIKey: "b", ChecksumKey: "c"},
		{ClientID: "a", ChecksumKey: "c"},
		{ClientID: "a", APIKey: "b"},
	} {
		if NewPayOS(cfg).Enabled() {
			t.Fatalf("partial credentials must NOT be enabled: %+v", cfg)
		}
	}
	if !strings.HasSuffix(full.payosBaseURL, "https://x") {
		t.Fatalf("BaseURL trailing slash must be trimmed: %q", full.payosBaseURL)
	}
}
