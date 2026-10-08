package payment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/config"
)

// Test constants — all prefixed pay2sTest* (package collision rule).
const (
	pay2sTestPartnerCode = "PC"
	pay2sTestAccessKey   = "AK"
	pay2sTestSecretKey   = "key"
	pay2sTestBankAccount = "970422|92568686|MB Bank|Nguyen Van A"

	// Pinned canonical signature strings (exact literals — any change to
	// the signing format breaks these). Vector values: accessKey=AK,
	// amount=2000, ipnUrl=U, orderId=ORD1, orderInfo=OI1,
	// partnerCode=PC, redirectUrl=R, requestId=REQ1/CANCEL1,
	// requestType=pay2s|cancel, transId=99, resultCode=0, message=ok,
	// payType=qr, responseTime=20261008120000, extraData empty.
	pay2sTestCreateRaw = "accessKey=AK&amount=2000&bankAccounts=Array&ipnUrl=U&orderId=ORD1&orderInfo=OI1&partnerCode=PC&redirectUrl=R&requestId=REQ1&requestType=pay2s"
	pay2sTestIPNRaw    = "accessKey=AK&amount=2000&extraData=&message=ok&orderId=ORD1&orderInfo=OI1&orderType=pay2s&partnerCode=PC&payType=qr&requestId=REQ1&responseTime=20261008120000&resultCode=0&transId=99"
	pay2sTestCancelRaw = "accessKey=AK&orderId=ORD1&partnerCode=PC&requestId=CANCEL1&requestType=cancel"

	// pay2sTestIPNJSON is the wire form of the IPN vector above; %s is
	// the m2signature.
	pay2sTestIPNJSON = `{"partnerCode":"PC","orderId":"ORD1","requestId":"REQ1","amount":2000,"orderInfo":"OI1","orderType":"pay2s","transId":99,"resultCode":0,"message":"ok","payType":"qr","responseTime":"20261008120000","extraData":"","m2signature":"%s"}`
)

func pay2sTestConfig() config.Pay2SConfig {
	return config.Pay2SConfig{
		PartnerCode:  pay2sTestPartnerCode,
		PartnerName:  "HiTechCloud",
		AccessKey:    pay2sTestAccessKey,
		SecretKey:    pay2sTestSecretKey,
		BankAccounts: pay2sTestBankAccount,
	}
}

// pay2sTestValidRequestID checks the documented requestId charset and
// length bound ([A-Za-z0-9._:-], ≤50).
func pay2sTestValidRequestID(s string) bool {
	if s == "" || len(s) > pay2sMaxRequestIDLen {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '.', r == '_', r == ':', r == '-':
		default:
			return false
		}
	}
	return true
}

// pay2sTestValidOrderInfo checks the documented orderInfo constraints
// (10–32 alphanumeric).
func pay2sTestValidOrderInfo(s string) bool {
	if len(s) < 10 || len(s) > 32 {
		return false
	}
	for _, r := range s {
		if !(r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

// TestPay2SHMACKnownAnswers pins pay2sHMACHex against PUBLISHED HMAC-SHA256
// vectors (RFC 4231 cases 1–2 and the vector from Pay2S' own signature
// doc). This freezes the digest construction itself; the tests below pin
// the canonical strings, which together pin the complete signature.
func TestPay2SHMACKnownAnswers(t *testing.T) {
	cases := []struct{ key, msg, want string }{
		{strings.Repeat("\x0b", 20), "Hi There",
			"b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7"},
		{"Jefe", "what do ya want for nothing?",
			"5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843"},
		{"key", "The quick brown fox jumps over the lazy dog",
			"f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"},
	}
	for _, tc := range cases {
		mac := hmac.New(sha256.New, []byte(tc.key))
		mac.Write([]byte(tc.msg))
		if got := hex.EncodeToString(mac.Sum(nil)); got != tc.want {
			t.Errorf("independent HMAC(%q,%q) = %s, want %s", tc.key, tc.msg, got, tc.want)
		}
		if got := pay2sHMACHex(tc.key, tc.msg); got != tc.want {
			t.Errorf("pay2sHMACHex(%q,%q) = %s, want %s", tc.key, tc.msg, got, tc.want)
		}
	}
}

// TestPay2SSignatureRawPinned pins the EXACT canonical strings for the
// three operations and chain-checks the signing path: the signature the
// builders produce must equal HMAC-SHA256(secret, pinned-literal), so any
// change to the signing format breaks this test.
func TestPay2SSignatureRawPinned(t *testing.T) {
	gotCreate := pay2sCreateSignatureRaw("AK", "2000", "U", "ORD1", "OI1", "PC", "R", "REQ1", "pay2s")
	if gotCreate != pay2sTestCreateRaw {
		t.Errorf("create raw mismatch:\n got  %q\n want %q", gotCreate, pay2sTestCreateRaw)
	}

	ipn := pay2sIPN{
		PartnerCode:  pay2sLit{s: "PC"},
		OrderID:      pay2sLit{s: "ORD1"},
		RequestID:    pay2sLit{s: "REQ1"},
		Amount:       pay2sLit{s: "2000"},
		OrderInfo:    pay2sLit{s: "OI1"},
		OrderType:    pay2sLit{s: "pay2s"},
		TransID:      pay2sLit{s: "99"},
		ResultCode:   pay2sLit{s: "0"},
		Message:      pay2sLit{s: "ok"},
		PayType:      pay2sLit{s: "qr"},
		ResponseTime: pay2sLit{s: "20261008120000"},
	}
	gotIPN := pay2sIPNSignatureRaw("AK", &ipn)
	if gotIPN != pay2sTestIPNRaw {
		t.Errorf("IPN raw mismatch:\n got  %q\n want %q", gotIPN, pay2sTestIPNRaw)
	}

	gotCancel := pay2sCancelSignatureRaw("AK", "ORD1", "PC", "CANCEL1")
	if gotCancel != pay2sTestCancelRaw {
		t.Errorf("cancel raw mismatch:\n got  %q\n want %q", gotCancel, pay2sTestCancelRaw)
	}

	// Chain check: signatures equal HMAC over the pinned literals.
	if s := pay2sHMACHex(pay2sTestSecretKey, gotCreate); s != pay2sHMACHex(pay2sTestSecretKey, pay2sTestCreateRaw) {
		t.Errorf("create signature chain mismatch: %s", s)
	}
	if s := pay2sHMACHex(pay2sTestSecretKey, gotIPN); s != pay2sHMACHex(pay2sTestSecretKey, pay2sTestIPNRaw) {
		t.Errorf("IPN signature chain mismatch: %s", s)
	}
	if s := pay2sHMACHex(pay2sTestSecretKey, gotCancel); s != pay2sHMACHex(pay2sTestSecretKey, pay2sTestCancelRaw) {
		t.Errorf("cancel signature chain mismatch: %s", s)
	}
}

func TestPay2SVerifyWebhook(t *testing.T) {
	sig := pay2sHMACHex(pay2sTestSecretKey, pay2sTestIPNRaw)
	p := NewPay2S(pay2sTestConfig())

	t.Run("valid", func(t *testing.T) {
		ev, err := p.VerifyWebhook([]byte(fmt.Sprintf(pay2sTestIPNJSON, sig)))
		if err != nil {
			t.Fatalf("VerifyWebhook: %v", err)
		}
		if ev.Provider != ProviderPay2S {
			t.Errorf("Provider = %q, want %q", ev.Provider, ProviderPay2S)
		}
		if ev.OrderID != "ORD1" || ev.ProviderRef != "ORD1" {
			t.Errorf("OrderID/ProviderRef = %q/%q, want ORD1/ORD1", ev.OrderID, ev.ProviderRef)
		}
		if ev.TransID != "99" {
			t.Errorf("TransID = %q, want 99", ev.TransID)
		}
		if ev.Status != StatusSucceeded {
			t.Errorf("Status = %q, want %q", ev.Status, StatusSucceeded)
		}
		if ev.AmountMinor != 2000 {
			t.Errorf("AmountMinor = %d, want 2000", ev.AmountMinor)
		}
		if ev.Currency != "VND" {
			t.Errorf("Currency = %q, want VND", ev.Currency)
		}
		wantPaid := time.Date(2026, 10, 8, 12, 0, 0, 0, time.FixedZone("ICT", 7*3600))
		if !ev.PaidAt.Equal(wantPaid) {
			t.Errorf("PaidAt = %v, want %v", ev.PaidAt, wantPaid)
		}
		if ev.Raw["transId"] != "99" || ev.Raw["amount"] != int64(2000) {
			t.Errorf("Raw = %v", ev.Raw)
		}
	})

	t.Run("string amount and transId tolerated", func(t *testing.T) {
		payload := fmt.Sprintf(pay2sTestIPNJSON, sig)
		payload = strings.Replace(payload, `"amount":2000`, `"amount":"2000"`, 1)
		payload = strings.Replace(payload, `"transId":99`, `"transId":"99"`, 1)
		payload = strings.Replace(payload, `"resultCode":0`, `"resultCode":"0"`, 1)
		ev, err := p.VerifyWebhook([]byte(payload))
		if err != nil {
			t.Fatalf("VerifyWebhook: %v", err)
		}
		if ev.AmountMinor != 2000 || ev.TransID != "99" || ev.Status != StatusSucceeded {
			t.Errorf("ev = %+v", ev)
		}
	})

	t.Run("tampered amount rejected", func(t *testing.T) {
		payload := strings.Replace(fmt.Sprintf(pay2sTestIPNJSON, sig), `"amount":2000`, `"amount":2001`, 1)
		if _, err := p.VerifyWebhook([]byte(payload)); !errors.Is(err, ErrWebhookSignatureInvalid) {
			t.Errorf("err = %v, want ErrWebhookSignatureInvalid", err)
		}
	})

	t.Run("tampered signature rejected", func(t *testing.T) {
		bad := strings.Repeat("0", 64)
		if _, err := p.VerifyWebhook([]byte(fmt.Sprintf(pay2sTestIPNJSON, bad))); !errors.Is(err, ErrWebhookSignatureInvalid) {
			t.Errorf("err = %v, want ErrWebhookSignatureInvalid", err)
		}
	})

	t.Run("missing signature rejected", func(t *testing.T) {
		payload := fmt.Sprintf(pay2sTestIPNJSON, "")
		if _, err := p.VerifyWebhook([]byte(payload)); !errors.Is(err, ErrWebhookSignatureInvalid) {
			t.Errorf("err = %v, want ErrWebhookSignatureInvalid", err)
		}
	})

	t.Run("broken JSON malformed", func(t *testing.T) {
		if _, err := p.VerifyWebhook([]byte(`{"partnerCode":`)); !errors.Is(err, ErrWebhookPayloadMalformed) {
			t.Errorf("err = %v, want ErrWebhookPayloadMalformed", err)
		}
	})

	t.Run("valid JSON bad MAC fails closed", func(t *testing.T) {
		// Well-formed body whose fields are all attacker-controlled:
		// nothing may be trusted before the MAC check passes.
		payload := `{"partnerCode":"PC","orderId":"ORD1","amount":999999,"resultCode":0,"m2signature":"00"}`
		if _, err := p.VerifyWebhook([]byte(payload)); !errors.Is(err, ErrWebhookSignatureInvalid) {
			t.Errorf("err = %v, want ErrWebhookSignatureInvalid", err)
		}
	})

	t.Run("other partner rejected after MAC", func(t *testing.T) {
		// Signature recomputed over the same fields with partnerCode=PX
		// passes the MAC but must still be rejected.
		pxRaw := strings.Replace(pay2sTestIPNRaw, "partnerCode=PC", "partnerCode=PX", 1)
		pxSig := pay2sHMACHex(pay2sTestSecretKey, pxRaw)
		payload := strings.Replace(fmt.Sprintf(pay2sTestIPNJSON, pxSig), `"partnerCode":"PC"`, `"partnerCode":"PX"`, 1)
		if _, err := p.VerifyWebhook([]byte(payload)); !errors.Is(err, ErrWebhookSignatureInvalid) {
			t.Errorf("err = %v, want ErrWebhookSignatureInvalid", err)
		}
	})

	t.Run("resultCode 9000 is pending", func(t *testing.T) {
		raw := strings.Replace(pay2sTestIPNRaw, "resultCode=0", "resultCode=9000", 1)
		payload := strings.Replace(fmt.Sprintf(pay2sTestIPNJSON, pay2sHMACHex(pay2sTestSecretKey, raw)),
			`"resultCode":0`, `"resultCode":9000`, 1)
		ev, err := p.VerifyWebhook([]byte(payload))
		if err != nil {
			t.Fatalf("VerifyWebhook: %v", err)
		}
		if ev.Status != StatusPending {
			t.Errorf("Status = %q, want %q", ev.Status, StatusPending)
		}
		if !ev.PaidAt.IsZero() {
			t.Errorf("PaidAt = %v, want zero", ev.PaidAt)
		}
	})

	t.Run("other resultCode is failed", func(t *testing.T) {
		raw := strings.Replace(pay2sTestIPNRaw, "resultCode=0", "resultCode=2", 1)
		payload := strings.Replace(fmt.Sprintf(pay2sTestIPNJSON, pay2sHMACHex(pay2sTestSecretKey, raw)),
			`"resultCode":0`, `"resultCode":2`, 1)
		ev, err := p.VerifyWebhook([]byte(payload))
		if err != nil {
			t.Fatalf("VerifyWebhook: %v", err)
		}
		if ev.Status != StatusFailed {
			t.Errorf("Status = %q, want %q", ev.Status, StatusFailed)
		}
	})

	t.Run("unparseable amount malformed after MAC", func(t *testing.T) {
		raw := strings.Replace(pay2sTestIPNRaw, "amount=2000", "amount=x", 1)
		payload := strings.Replace(fmt.Sprintf(pay2sTestIPNJSON, pay2sHMACHex(pay2sTestSecretKey, raw)),
			`"amount":2000`, `"amount":"x"`, 1)
		if _, err := p.VerifyWebhook([]byte(payload)); !errors.Is(err, ErrWebhookPayloadMalformed) {
			t.Errorf("err = %v, want ErrWebhookPayloadMalformed", err)
		}
	})
}

func TestPay2SCreatePayment(t *testing.T) {
	var gotBody pay2sCreateRequest
	var gotRawBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != pay2sCreatePath {
			t.Errorf("path = %s, want %s", r.URL.Path, pay2sCreatePath)
		}
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q", ct)
		}
		data, _ := io.ReadAll(r.Body)
		gotRawBody = string(data)
		if err := json.Unmarshal(data, &gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"partnerCode":"PC","requestId":"REQ1","orderId":"ORD1","orderInfo":"OI1","amount":"2000","message":"ok","lang":"vi","resultCode":0,"qrList":[{"bank_id":"970422","account_number":"92568686","account_name":"MB","qrCode":"data:image/png;base64,AAA","qrUrl":"https://qr.example/1"}],"payUrl":"https://pay2s.vn/pay/1"}`)
	}))
	defer srv.Close()

	cfg := pay2sTestConfig()
	cfg.BaseURL = srv.URL
	p := NewPay2S(cfg)

	res, err := p.CreatePayment(context.Background(), CreatePaymentRequest{
		OrderID:     "ORD1",
		AmountMinor: 2000,
		Currency:    "VND",
		ReturnURL:   "https://shop.example/return",
		Extra:       map[string]string{"webhook_url": "https://shop.example/ipn"},
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}

	// Request body shape.
	if gotBody.AccessKey != pay2sTestAccessKey || gotBody.PartnerCode != pay2sTestPartnerCode {
		t.Errorf("credentials in body: %+v", gotBody)
	}
	if gotBody.OrderType != "pay2s" || gotBody.RequestType != "pay2s" {
		t.Errorf("orderType/requestType = %q/%q, want pay2s/pay2s", gotBody.OrderType, gotBody.RequestType)
	}
	if gotBody.Amount != 2000 {
		t.Errorf("Amount = %d, want 2000", gotBody.Amount)
	}
	if !strings.Contains(gotRawBody, `"amount":2000`) {
		t.Errorf("amount must be a JSON number, body: %s", gotRawBody)
	}
	if len(gotBody.BankAccounts) != 1 || gotBody.BankAccounts[0].BankID != "970422" || gotBody.BankAccounts[0].AccountNumber != "92568686" {
		t.Errorf("bankAccounts = %+v", gotBody.BankAccounts)
	}
	if !pay2sTestValidOrderInfo(gotBody.OrderInfo) {
		t.Errorf("orderInfo %q violates 10-32 alnum", gotBody.OrderInfo)
	}
	if !pay2sTestValidRequestID(gotBody.RequestID) {
		t.Errorf("requestId %q violates charset/length", gotBody.RequestID)
	}
	// Signature: canonical raw built from the received fields must
	// reproduce the sent signature (pins "bankAccounts=Array" + format).
	raw := pay2sCreateSignatureRaw(gotBody.AccessKey, strconv.FormatInt(gotBody.Amount, 10),
		gotBody.IPNURL, gotBody.OrderID, gotBody.OrderInfo, gotBody.PartnerCode,
		gotBody.RedirectURL, gotBody.RequestID, gotBody.RequestType)
	if gotBody.Signature != pay2sHMACHex(pay2sTestSecretKey, raw) {
		t.Errorf("signature mismatch: body=%q recomputed raw=%q", gotBody.Signature, raw)
	}

	// Result mapping.
	if res.ProviderRef != "ORD1" {
		t.Errorf("ProviderRef = %q, want ORD1", res.ProviderRef)
	}
	if res.PayURL != "https://pay2s.vn/pay/1" {
		t.Errorf("PayURL = %q", res.PayURL)
	}
	if res.QRCode != "data:image/png;base64,AAA" {
		t.Errorf("QRCode = %q", res.QRCode)
	}
	if !res.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt = %v, want zero (gateway reports none)", res.ExpiresAt)
	}

	// Determinism: retried create for the same order reuses orderInfo.
	if _, err := p.CreatePayment(context.Background(), CreatePaymentRequest{
		OrderID:     "ORD1",
		AmountMinor: 2000,
		Currency:    "VND",
		ReturnURL:   "https://shop.example/return",
		Extra:       map[string]string{"webhook_url": "https://shop.example/ipn"},
	}); err != nil {
		t.Fatalf("CreatePayment retry: %v", err)
	}
	if gotBody.OrderInfo != pay2sOrderInfo("ORD1") || !pay2sTestValidRequestID(gotBody.RequestID) {
		t.Errorf("retry produced orderInfo=%q requestId=%q", gotBody.OrderInfo, gotBody.RequestID)
	}
}

func TestPay2SCreatePaymentErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"resultCode":1001,"message":"invalid request"}`)
	}))
	defer srv.Close()
	cfg := pay2sTestConfig()
	cfg.BaseURL = srv.URL
	p := NewPay2S(cfg)

	_, err := p.CreatePayment(context.Background(), CreatePaymentRequest{
		OrderID:     "ORD1",
		AmountMinor: 2000,
		Currency:    "VND",
		ReturnURL:   "https://shop.example/return",
		Extra:       map[string]string{"webhook_url": "https://shop.example/ipn"},
	})
	if err == nil {
		t.Fatal("want error for resultCode != 0")
	}

	// Input validation.
	valid := CreatePaymentRequest{
		OrderID:     "ORD1",
		AmountMinor: 2000,
		Currency:    "VND",
		ReturnURL:   "https://shop.example/return",
		Extra:       map[string]string{"webhook_url": "https://shop.example/ipn"},
	}

	// "not configured" is a PROVIDER state, not a request state: the very
	// same request must fail against a provider without credentials.
	if _, err := NewPay2S(config.Pay2SConfig{}).CreatePayment(context.Background(), valid); !errors.Is(err, ErrProviderNotConfigured) {
		t.Errorf("not configured: err = %v, want %v", err, ErrProviderNotConfigured)
	}

	cases := []struct {
		name string
		mut  func(*CreatePaymentRequest)
		want error
	}{
		{"currency", func(r *CreatePaymentRequest) { r.Currency = "USD" }, ErrCurrencyNotSupported},
		{"amount", func(r *CreatePaymentRequest) { r.AmountMinor = 0 }, nil},
		{"order id", func(r *CreatePaymentRequest) { r.OrderID = "" }, nil},
		{"return url", func(r *CreatePaymentRequest) { r.ReturnURL = "" }, nil},
		{"ipn url", func(r *CreatePaymentRequest) { r.Extra = map[string]string{} }, nil},
	}
	for _, tc := range cases {
		req := valid
		tc.mut(&req)
		_, err := p.CreatePayment(context.Background(), req)
		if tc.want != nil {
			if !errors.Is(err, tc.want) {
				t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
			}
		} else if err == nil {
			t.Errorf("%s: want error", tc.name)
		}
	}
}

func TestPay2SBankAccountsParsing(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{"970422|92568686|MB Bank|A;970415|12345678|VCB|B", 2},
		{"970422|92568686", 1},
		{"  970422 | 92568686 | MB  ", 1},
		{"garbage", 0},
		{"970422", 0},
		{"|92568686", 0},
		{"970422|", 0},
		{"", 0},
		{"bad;970422|92568686|x|y", 1},
	}
	for _, tc := range cases {
		if got := len(pay2sParseBankAccounts(tc.raw)); got != tc.want {
			t.Errorf("pay2sParseBankAccounts(%q) = %d entries, want %d", tc.raw, got, tc.want)
		}
	}

	// Enabled truth table (all-or-nothing).
	full := pay2sTestConfig()
	enabled := map[string]bool{
		"full":          NewPay2S(full).Enabled(),
		"no partner":    NewPay2S(func() config.Pay2SConfig { c := full; c.PartnerCode = ""; return c }()).Enabled(),
		"no access":     NewPay2S(func() config.Pay2SConfig { c := full; c.AccessKey = ""; return c }()).Enabled(),
		"no secret":     NewPay2S(func() config.Pay2SConfig { c := full; c.SecretKey = ""; return c }()).Enabled(),
		"no banks":      NewPay2S(func() config.Pay2SConfig { c := full; c.BankAccounts = ""; return c }()).Enabled(),
		"garbage banks": NewPay2S(func() config.Pay2SConfig { c := full; c.BankAccounts = "zzz"; return c }()).Enabled(),
	}
	want := map[string]bool{
		"full": true, "no partner": false, "no access": false,
		"no secret": false, "no banks": false, "garbage banks": false,
	}
	for k, w := range want {
		if enabled[k] != w {
			t.Errorf("Enabled[%s] = %v, want %v", k, enabled[k], w)
		}
	}
	if NewPay2S(full).Name() != ProviderPay2S {
		t.Errorf("Name() = %q, want %q", NewPay2S(full).Name(), ProviderPay2S)
	}
}

func TestPay2SVoidPayment(t *testing.T) {
	type call struct {
		path, rawBody string
		body          pay2sCancelRequest
	}
	var calls []call
	fail := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		var body pay2sCancelRequest
		_ = json.Unmarshal(data, &body)
		calls = append(calls, call{path: r.URL.Path, rawBody: string(data), body: body})
		w.Header().Set("Content-Type", "application/json")
		if fail {
			w.WriteHeader(http.StatusConflict)
			io.WriteString(w, `{"status":false,"message":"order not pending"}`)
			return
		}
		io.WriteString(w, `{"resultCode":0,"status":"cancelled","message":"ok"}`)
	}))
	defer srv.Close()

	cfg := pay2sTestConfig()
	cfg.BaseURL = srv.URL
	p := NewPay2S(cfg)

	if err := p.VoidPayment(context.Background(), "ORD1", "customer changed mind"); err != nil {
		t.Fatalf("VoidPayment: %v", err)
	}
	if err := p.VoidPayment(context.Background(), "ORD1", "retry"); err != nil {
		t.Fatalf("VoidPayment retry: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	c := calls[0]
	if c.path != pay2sCancelPath {
		t.Errorf("path = %q, want %q", c.path, pay2sCancelPath)
	}
	// Exactly the six documented keys.
	var generic map[string]any
	if err := json.Unmarshal([]byte(c.rawBody), &generic); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(generic) != 6 {
		t.Errorf("body has %d keys, want 6: %s", len(generic), c.rawBody)
	}
	if c.body.OrderID != "ORD1" || c.body.AccessKey != pay2sTestAccessKey || c.body.PartnerCode != pay2sTestPartnerCode {
		t.Errorf("cancel body = %+v", c.body)
	}
	if c.body.RequestType != "cancel" {
		t.Errorf("requestType = %q, want cancel", c.body.RequestType)
	}
	if !pay2sTestValidRequestID(c.body.RequestID) {
		t.Errorf("requestId %q violates charset/length", c.body.RequestID)
	}
	if c.body.RequestID == calls[1].body.RequestID {
		t.Errorf("cancel requestId must be fresh per call, got %q twice", c.body.RequestID)
	}
	// Cancel signature pinned by literal raw built inline.
	raw := "accessKey=" + c.body.AccessKey + "&orderId=" + c.body.OrderID +
		"&partnerCode=" + c.body.PartnerCode + "&requestId=" + c.body.RequestID +
		"&requestType=cancel"
	if c.body.Signature != pay2sHMACHex(pay2sTestSecretKey, raw) {
		t.Errorf("cancel signature mismatch: %q over %q", c.body.Signature, raw)
	}

	// Error shape {"status":false,...} → error.
	fail = true
	if err := p.VoidPayment(context.Background(), "ORD1", "again"); err == nil {
		t.Error("want error for cancel failure")
	}

	if err := p.VoidPayment(context.Background(), "", "x"); err == nil {
		t.Error("want error for empty ref")
	}
}

func TestPay2SOrderInfo(t *testing.T) {
	a := pay2sOrderInfo("HTC-1")
	b := pay2sOrderInfo("HTC-1")
	c := pay2sOrderInfo("HTC-2")
	if a != b {
		t.Errorf("orderInfo not deterministic: %q vs %q", a, b)
	}
	if a == c {
		t.Errorf("orderInfo collides for different orders: %q", a)
	}
	if !pay2sTestValidOrderInfo(a) {
		t.Errorf("orderInfo %q violates 10-32 alnum", a)
	}
	if !strings.HasPrefix(a, "HTCPAY") {
		t.Errorf("orderInfo %q missing HTCPAY prefix", a)
	}
}

func TestPay2SUnsupported(t *testing.T) {
	p := NewPay2S(config.Pay2SConfig{})
	ctx := context.Background()
	if _, err := p.GetPaymentStatus(ctx, "ORD1"); !errors.Is(err, ErrNotSupported) {
		t.Errorf("GetPaymentStatus err = %v, want ErrNotSupported", err)
	}
	if _, err := p.CapturePayment(ctx, "ORD1"); !errors.Is(err, ErrNotSupported) {
		t.Errorf("CapturePayment err = %v, want ErrNotSupported (alias of GetPaymentStatus)", err)
	}
	if _, err := p.RefundPayment(ctx, RefundRequest{ProviderRef: "ORD1"}); !errors.Is(err, ErrNotSupported) {
		t.Errorf("RefundPayment err = %v, want ErrNotSupported", err)
	}
}
