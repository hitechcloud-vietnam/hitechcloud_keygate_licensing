package payment

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/config"
)

// ─── test fixtures (all pure unit: no network beyond httptest loopback, no DB) ───

// zalopayTestNow is 2026-03-20 13:20:00 UTC = 20:20 Asia/Ho_Chi_Minh,
// so the VN date prefix is "260320". UnixMilli = 1774012800000.
var zalopayTestNow = time.Date(2026, 3, 20, 13, 20, 0, 0, time.UTC)

const (
	zalopayTestKey1        = "zk-key1-demo"
	zalopayTestCallbackKey = "zk-callback-demo"
	zalopayTestAppTimeMs   = int64(1774012800000)
)

// zalopayTestHMAC is an INDEPENDENT lowercase-hex HMAC-SHA256 (does not
// call the provider helper) so the tests cross-check the implementation.
func zalopayTestHMAC(key, raw string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(raw))
	return hex.EncodeToString(m.Sum(nil))
}

// zalopayTestProvider builds a provider with credentials and a frozen
// clock pointed at baseURL ("": no HTTP expected in the test).
func zalopayTestProvider(t *testing.T, baseURL string) *ZaloPayProvider {
	t.Helper()
	p := NewZaloPay(config.ZaloPayConfig{
		AppID:       "2553",
		Key1:        zalopayTestKey1,
		CallbackKey: zalopayTestCallbackKey,
		BaseURL:     baseURL,
	})
	p.zalopayNow = func() time.Time { return zalopayTestNow }
	return p
}

// ─── 1. mac vectors: any signing change must break these ───

func TestZaloPayMacVectors(t *testing.T) {
	// Sanity anchor for every hardcoded timestamp below.
	if got := zalopayTestNow.UnixMilli(); got != zalopayTestAppTimeMs {
		t.Fatalf("frozen clock: got %d, want %d", got, zalopayTestAppTimeMs)
	}

	// Published RFC 4231 HMAC-SHA-256 vectors pin the DIGEST side: any
	// change to the algorithm or the lowercase-hex encoding breaks these.
	for _, tc := range []struct {
		name, key, msg, want string
	}{
		{
			name: "rfc4231-tc1",
			key:  strings.Repeat("\x0b", 20),
			msg:  "Hi There",
			want: "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7",
		},
		{
			name: "rfc4231-tc2",
			key:  "Jefe",
			msg:  "what do ya want for nothing?",
			want: "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843",
		},
	} {
		if got := zalopayHMACHex(tc.key, tc.msg); got != tc.want {
			t.Fatalf("%s: zalopayHMACHex = %s, want %s", tc.name, got, tc.want)
		}
	}

	// Pinned EXACT hmac_input strings: any change to field order,
	// separators or formatting breaks these literals.
	zalopayTestCreateRaw := `2553|260320_DEMO123|demo.user|22000|1774012800000|` +
		`{"redirecturl":"https://example.com/return","order_id":"HTC-1"}|[]`
	if got := zalopayMacCreateInput(2553, "260320_DEMO123", "demo.user", 22000, 1774012800000,
		`{"redirecturl":"https://example.com/return","order_id":"HTC-1"}`, "[]"); got != zalopayTestCreateRaw {
		t.Fatalf("create input:\n got %q\nwant %q", got, zalopayTestCreateRaw)
	}

	zalopayTestQueryRaw := `2553|260320_DEMO123|zk-key1-demo`
	if got := zalopayMacQueryInput(2553, "260320_DEMO123", zalopayTestKey1); got != zalopayTestQueryRaw {
		t.Fatalf("query input: got %q, want %q", got, zalopayTestQueryRaw)
	}

	zalopayTestRefundRaw := `2553|260320000000123|22000|customer request|1774012800000`
	if got := zalopayMacRefundInput(2553, "260320000000123", 22000, "customer request", 1774012800000); got != zalopayTestRefundRaw {
		t.Fatalf("refund input: got %q, want %q", got, zalopayTestRefundRaw)
	}

	zalopayTestQueryRefundRaw := `2553|260320_2553_R1|1774012800000`
	if got := zalopayMacQueryRefundInput(2553, "260320_2553_R1", 1774012800000); got != zalopayTestQueryRefundRaw {
		t.Fatalf("query_refund input: got %q, want %q", got, zalopayTestQueryRefundRaw)
	}

	// Full mac values must equal an independent HMAC over the pinned raw.
	if got, want := zalopayHMACHex(zalopayTestKey1, zalopayTestCreateRaw), zalopayTestHMAC(zalopayTestKey1, zalopayTestCreateRaw); got != want {
		t.Fatalf("create mac: got %s, want %s", got, want)
	}
	if got, want := zalopayHMACHex(zalopayTestCallbackKey, zalopayTestCallbackData), zalopayTestHMAC(zalopayTestCallbackKey, zalopayTestCallbackData); got != want {
		t.Fatalf("callback mac: got %s, want %s", got, want)
	}
	if got, want := zalopayHMACHex(zalopayTestKey1, zalopayTestRefundRaw), zalopayTestHMAC(zalopayTestKey1, zalopayTestRefundRaw); got != want {
		t.Fatalf("refund mac: got %s, want %s", got, want)
	}
}

// zalopayTestCallbackData is the pinned callback "data" JSON string
// (embed_data is itself a JSON string with escaped quotes — exactly as
// ZaloPay sends it).
const zalopayTestCallbackData = `{"app_id":2553,"app_trans_id":"260320_DEMO123","app_time":1774012800000,` +
	`"app_user":"demo.user","amount":22000,` +
	`"embed_data":"{\"redirecturl\":\"https://example.com/return\",\"order_id\":\"HTC-1\"}","item":"[]",` +
	`"zp_trans_id":260320000000123,"server_time":1774012860000,"channel":38,"merchant_user_id":""}`

// ─── 2. app_trans_id generation ───

func TestZaloPayAppTransID(t *testing.T) {
	// VN-date prefix from the frozen clock: 2026-03-20 20:20 ICT.
	if got, want := zalopayAppTransID("HTC-1", zalopayTestNow), "260320_HTC_1"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	// UTC 18:00 is already 01:00 the NEXT day in Vietnam.
	cross := time.Date(2026, 3, 20, 18, 0, 0, 0, time.UTC)
	if got, want := zalopayAppTransID("HTC-1", cross), "260321_HTC_1"; got != want {
		t.Fatalf("VN midnight rollover: got %q, want %q", got, want)
	}

	// Deterministic: a retry the same VN day re-creates the same id.
	if a, b := zalopayAppTransID("HTC-1", zalopayTestNow), zalopayAppTransID("HTC-1", zalopayTestNow.Add(time.Second)); a != b {
		t.Fatalf("not deterministic: %q vs %q", a, b)
	}

	// Sanitization: non-alphanumerics become '_'.
	if got, want := zalopayAppTransID("HTC/1 23", zalopayTestNow), "260320_HTC_1_23"; got != want {
		t.Fatalf("sanitize: got %q, want %q", got, want)
	}

	// Length cap: ≤40 chars even for a monster order id, still prefixed
	// and still deterministic.
	long := zalopayAppTransID(strings.Repeat("A", 60), zalopayTestNow)
	if len(long) > zalopayAppTransIDMaxLen {
		t.Fatalf("too long (%d): %q", len(long), long)
	}
	if !strings.HasPrefix(long, "260320_") {
		t.Fatalf("missing VN-date prefix: %q", long)
	}
	if long != zalopayAppTransID(strings.Repeat("A", 60), zalopayTestNow) {
		t.Fatalf("long id not deterministic: %q", long)
	}
}

// ─── Enabled() credential matrix ───

func TestZaloPayEnabled(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		appID, key1, callbackKey string
		want                     bool
	}{
		{"all set", "2553", "k1", "ck", true},
		{"app id empty", "", "k1", "ck", false},
		{"app id not numeric", "abc", "k1", "ck", false},
		{"app id zero", "0", "k1", "ck", false},
		{"app id negative", "-5", "k1", "ck", false},
		{"key1 missing", "2553", "", "ck", false},
		{"callback key missing", "2553", "k1", "", false},
	} {
		p := NewZaloPay(config.ZaloPayConfig{AppID: tc.appID, Key1: tc.key1, CallbackKey: tc.callbackKey})
		if got := p.Enabled(); got != tc.want {
			t.Fatalf("%s: Enabled() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// ─── 4. CreatePayment over httptest: exact body + response mapping ───

func TestZaloPayCreatePayment(t *testing.T) {
	bodyCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/create" {
			t.Errorf("path = %s, want /v2/create", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		bodyCh <- string(b)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"return_code":1,"return_message":"","sub_return_code":1,"sub_return_message":"",`+
			`"zp_trans_token":"tok","order_token":"otok",`+
			`"order_url":"https://sbgateway.zalopay.vn/checkout/v2/abc","qr_code":"iVBORw0KGgoAAAANS"}`)
	}))
	defer srv.Close()

	p := zalopayTestProvider(t, srv.URL)
	res, err := p.CreatePayment(t.Context(), CreatePaymentRequest{
		OrderID:     "HTC-1",
		AmountMinor: 22000,
		Currency:    "VND",
		Description: "Pro plan 1 month",
		BuyerEmail:  "demo.user@example.com",
		ReturnURL:   "https://example.com/return",
		Extra:       map[string]string{"webhook_url": "https://api.example.com/hooks/zalopay"},
	})
	if err != nil {
		t.Fatalf("CreatePayment: %v", err)
	}

	raw := <-bodyCh
	var body zalopayCreateRequest
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode sent body: %v\n%s", err, raw)
	}

	// Exact body: int app_id / int64 amount (no float formatting), exact
	// embed_data (pins the JSON field order the mac depends on), pinned mac.
	if !strings.Contains(raw, `"app_id":2553`) || !strings.Contains(raw, `"amount":22000`) {
		t.Fatalf("app_id/amount not plain integers:\n%s", raw)
	}
	if body.AppID != 2553 || body.Amount != 22000 {
		t.Fatalf("typed body: app_id=%d amount=%d", body.AppID, body.Amount)
	}
	if body.AppTransID != "260320_HTC_1" || body.AppUser != "demo.user" {
		t.Fatalf("app_trans_id=%q app_user=%q", body.AppTransID, body.AppUser)
	}
	if body.AppTime != zalopayTestAppTimeMs || body.ExpireDurationSecs != zalopayExpireDefaultSecs {
		t.Fatalf("app_time=%d expire=%d", body.AppTime, body.ExpireDurationSecs)
	}
	if body.Description != "Pro plan 1 month" || body.CallbackURL != "https://api.example.com/hooks/zalopay" {
		t.Fatalf("description=%q callback_url=%q", body.Description, body.CallbackURL)
	}
	if body.Item != "[]" || body.BankCode != "" {
		t.Fatalf("item=%q bank_code=%q", body.Item, body.BankCode)
	}
	wantEmbed := `{"redirecturl":"https://example.com/return","order_id":"HTC-1"}`
	if body.EmbedData != wantEmbed {
		t.Fatalf("embed_data:\n got %q\nwant %q", body.EmbedData, wantEmbed)
	}
	wantMac := zalopayTestHMAC(zalopayTestKey1,
		`2553|260320_HTC_1|demo.user|22000|1774012800000|`+wantEmbed+`|[]`)
	if body.Mac != wantMac {
		t.Fatalf("mac:\n got %q\nwant %q", body.Mac, wantMac)
	}

	// Response mapping.
	if res.ProviderRef != "260320_HTC_1" {
		t.Fatalf("ProviderRef = %q", res.ProviderRef)
	}
	if res.PayURL != "https://sbgateway.zalopay.vn/checkout/v2/abc" {
		t.Fatalf("PayURL = %q", res.PayURL)
	}
	if res.QRCode != "iVBORw0KGgoAAAANS" {
		t.Fatalf("QRCode = %q", res.QRCode)
	}
	if want := zalopayTestNow.Add(900 * time.Second); !res.ExpiresAt.Equal(want) {
		t.Fatalf("ExpiresAt = %v, want %v", res.ExpiresAt, want)
	}
}

func TestZaloPayCreatePaymentExpireWindow(t *testing.T) {
	for _, tc := range []struct {
		name      string
		expiresAt time.Time
		wantSecs  int64
	}{
		{"unset → default 900", time.Time{}, 900},
		{"one hour out → 3600", zalopayTestNow.Add(time.Hour), 3600},
		{"ten seconds out → clamped to API floor 300", zalopayTestNow.Add(10 * time.Second), 300},
		{"already expired → clamped to API floor 300", zalopayTestNow.Add(-time.Hour), 300},
		{"over the ceiling → clamped to 2592000", zalopayTestNow.Add(40 * 24 * time.Hour), 2592000},
	} {
		bodyCh := make(chan string, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			bodyCh <- string(b)
			io.WriteString(w, `{"return_code":1,"return_message":"","sub_return_code":1,"sub_return_message":"","order_url":"u","qr_code":""}`)
		}))
		p := zalopayTestProvider(t, srv.URL)
		if _, err := p.CreatePayment(t.Context(), CreatePaymentRequest{
			OrderID: "HTC-1", AmountMinor: 22000, Currency: "VND", ExpiresAt: tc.expiresAt,
		}); err != nil {
			srv.Close()
			t.Fatalf("%s: %v", tc.name, err)
		}
		raw := <-bodyCh
		srv.Close()
		if want := `"expire_duration_seconds":` + strconv.FormatInt(tc.wantSecs, 10); !strings.Contains(raw, want) {
			t.Fatalf("%s: body missing %s:\n%s", tc.name, want, raw)
		}
	}
}

func TestZaloPayCreatePaymentError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"return_code":2,"return_message":"FAIL","sub_return_code":-402,`+
			`"sub_return_message":"ILLEGAL_APP/SIGNATURE_REQUEST"}`)
	}))
	defer srv.Close()

	p := zalopayTestProvider(t, srv.URL)
	_, err := p.CreatePayment(t.Context(), CreatePaymentRequest{OrderID: "HTC-1", AmountMinor: 22000, Currency: "VND"})
	if err == nil {
		t.Fatal("want error on return_code != 1")
	}
	if !strings.Contains(err.Error(), "-402") || !strings.Contains(err.Error(), "ILLEGAL_APP") {
		t.Fatalf("error must carry sub_return_code/message: %v", err)
	}
}

func TestZaloPayCreatePaymentCurrencyGuard(t *testing.T) {
	p := zalopayTestProvider(t, "")
	_, err := p.CreatePayment(t.Context(), CreatePaymentRequest{OrderID: "HTC-1", AmountMinor: 22000, Currency: "USD"})
	if !errors.Is(err, ErrCurrencyNotSupported) {
		t.Fatalf("got %v, want ErrCurrencyNotSupported", err)
	}
}

// ─── 3. VerifyWebhook ───

func zalopayTestEnvelope(t *testing.T, data, mac string, typ int) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{"data": data, "mac": mac, "type": typ})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return b
}

func TestZaloPayVerifyWebhook(t *testing.T) {
	p := zalopayTestProvider(t, "")
	goodMac := zalopayTestHMAC(zalopayTestCallbackKey, zalopayTestCallbackData)

	// Valid → normalized WebhookEvent.
	ev, err := p.VerifyWebhook(zalopayTestEnvelope(t, zalopayTestCallbackData, goodMac, 1))
	if err != nil {
		t.Fatalf("VerifyWebhook: %v", err)
	}
	if ev.Provider != ProviderZaloPay || ev.OrderID != "HTC-1" || ev.ProviderRef != "260320_DEMO123" {
		t.Fatalf("identity fields: %+v", ev)
	}
	if ev.TransID != "260320000000123" {
		t.Fatalf("TransID = %q, want zp_trans_id as decimal string", ev.TransID)
	}
	if ev.Status != StatusSucceeded || ev.AmountMinor != 22000 || ev.Currency != "VND" {
		t.Fatalf("state fields: %+v", ev)
	}
	if want := time.UnixMilli(1774012860000); !ev.PaidAt.Equal(want) {
		t.Fatalf("PaidAt = %v, want %v", ev.PaidAt, want)
	}
	if ev.Raw["amount"] != int64(22000) || ev.Raw["zp_trans_id"] != int64(260320000000123) {
		t.Fatalf("Raw: %+v", ev.Raw)
	}

	// Tampered mac → ErrWebhookSignatureInvalid.
	badMac := "0" + goodMac[1:]
	if goodMac[0] == '0' {
		badMac = "1" + goodMac[1:]
	}
	if _, err := p.VerifyWebhook(zalopayTestEnvelope(t, zalopayTestCallbackData, badMac, 1)); !errors.Is(err, ErrWebhookSignatureInvalid) {
		t.Fatalf("tampered mac: got %v, want ErrWebhookSignatureInvalid", err)
	}

	// Tampered data (mac no longer matches) → ErrWebhookSignatureInvalid.
	tampered := strings.Replace(zalopayTestCallbackData, `"amount":22000`, `"amount":22001`, 1)
	if _, err := p.VerifyWebhook(zalopayTestEnvelope(t, tampered, goodMac, 1)); !errors.Is(err, ErrWebhookSignatureInvalid) {
		t.Fatalf("tampered data: got %v, want ErrWebhookSignatureInvalid", err)
	}

	// Malformed data JSON (valid mac over it) → ErrWebhookPayloadMalformed.
	brokenData := `{"app_id":`
	brokenMac := zalopayTestHMAC(zalopayTestCallbackKey, brokenData)
	if _, err := p.VerifyWebhook(zalopayTestEnvelope(t, brokenData, brokenMac, 1)); !errors.Is(err, ErrWebhookPayloadMalformed) {
		t.Fatalf("broken data: got %v, want ErrWebhookPayloadMalformed", err)
	}

	// type != 1 → error (even with a valid mac).
	if _, err := p.VerifyWebhook(zalopayTestEnvelope(t, zalopayTestCallbackData, goodMac, 2)); !errors.Is(err, ErrWebhookPayloadMalformed) {
		t.Fatalf("type 2: got %v, want ErrWebhookPayloadMalformed", err)
	}

	// Garbage outer payload → ErrWebhookPayloadMalformed.
	if _, err := p.VerifyWebhook([]byte(`{"data":`)); !errors.Is(err, ErrWebhookPayloadMalformed) {
		t.Fatalf("garbage outer: got %v, want ErrWebhookPayloadMalformed", err)
	}

	// embed_data without our order_id (valid mac) → ErrWebhookPayloadMalformed.
	noOrder := `{"app_id":2553,"app_trans_id":"260320_DEMO123","app_time":1774012800000,"app_user":"demo.user",` +
		`"amount":22000,"embed_data":"{}","item":"[]","zp_trans_id":260320000000123,` +
		`"server_time":1774012860000,"channel":38,"merchant_user_id":""}`
	if _, err := p.VerifyWebhook(zalopayTestEnvelope(t, noOrder, zalopayTestHMAC(zalopayTestCallbackKey, noOrder), 1)); !errors.Is(err, ErrWebhookPayloadMalformed) {
		t.Fatalf("missing order_id: got %v, want ErrWebhookPayloadMalformed", err)
	}
}

// ─── 5. GetPaymentStatus: status enum mapping table ───

func TestZaloPayGetPaymentStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
		want     PaymentStatus
		wantErr  bool
	}{
		// Classic "status" enum (defensive branch).
		{"status -1 → failed", `{"return_code":1,"sub_return_code":1,"is_processing":false,"amount":22000,"zp_trans_id":260320000000123,"server_time":1774012860000,"status":-1}`, StatusFailed, false},
		{"status 1 → pending", `{"return_code":1,"sub_return_code":1,"is_processing":true,"amount":22000,"zp_trans_id":260320000000123,"status":1}`, StatusPending, false},
		{"status 2 → succeeded", `{"return_code":1,"sub_return_code":1,"is_processing":false,"amount":22000,"zp_trans_id":260320000000123,"server_time":1774012860000,"status":2}`, StatusSucceeded, false},
		{"status 3 → refunded", `{"return_code":1,"sub_return_code":1,"is_processing":false,"amount":22000,"zp_trans_id":260320000000123,"status":3}`, StatusRefunded, false},
		{"status 7 → fail-safe pending", `{"return_code":1,"sub_return_code":1,"is_processing":false,"amount":22000,"status":7}`, StatusPending, false},

		// Docs-verified path (no "status" field).
		{"is_processing → pending", `{"return_code":1,"sub_return_code":1,"is_processing":true,"amount":22000,"zp_trans_id":260320000000123}`, StatusPending, false},
		{"return_code 3 PROCESSING → pending", `{"return_code":3,"sub_return_code":1,"is_processing":false,"amount":22000}`, StatusPending, false},
		{"return_code 1 → succeeded", `{"return_code":1,"sub_return_code":1,"is_processing":false,"amount":22000,"zp_trans_id":260320000000123,"server_time":1774012860000}`, StatusSucceeded, false},
		{"return_code 2 sub -54 TIME_INVALID → expired", `{"return_code":2,"sub_return_code":-54,"is_processing":false,"amount":22000}`, StatusExpired, false},
		{"return_code 2 sub -217 BANK_ERROR → failed", `{"return_code":2,"sub_return_code":-217,"is_processing":false,"amount":22000}`, StatusFailed, false},
		{"return_code 2 sub -101 ORDER_NOT_EXIST → error", `{"return_code":2,"sub_return_code":-101,"is_processing":false,"amount":0}`, "", true},
		{"unknown return_code → error", `{"return_code":99,"sub_return_code":0,"is_processing":false,"amount":0}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bodyCh := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v2/query" {
					t.Errorf("path = %s, want /v2/query", r.URL.Path)
				}
				b, _ := io.ReadAll(r.Body)
				bodyCh <- string(b)
				io.WriteString(w, tc.response)
			}))
			defer srv.Close()

			p := zalopayTestProvider(t, srv.URL)
			res, err := p.GetPaymentStatus(t.Context(), "260320_DEMO123")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetPaymentStatus: %v", err)
			}
			if res.Status != tc.want {
				t.Fatalf("Status = %q, want %q", res.Status, tc.want)
			}
			if res.ProviderRef != "260320_DEMO123" || res.Currency != "VND" {
				t.Fatalf("result: %+v", res)
			}

			// The query mac input embeds key1 as its third segment
			// (order-query docs) — pin it once from the first response.
			raw := <-bodyCh
			var q zalopayQueryRequest
			if err := json.Unmarshal([]byte(raw), &q); err != nil {
				t.Fatalf("decode query body: %v", err)
			}
			wantMac := zalopayTestHMAC(zalopayTestKey1, `2553|260320_DEMO123|zk-key1-demo`)
			if q.Mac != wantMac {
				t.Fatalf("query mac:\n got %q\nwant %q", q.Mac, wantMac)
			}
		})
	}
}

// ─── 6. Refund: m_refund_id format + pinned mac + async result ───

func TestZaloPayRefund(t *testing.T) {
	bodyCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/refund" {
			t.Errorf("path = %s, want /v2/refund", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		bodyCh <- string(b)
		io.WriteString(w, `{"return_code":1,"return_message":"","sub_return_code":1,"sub_return_message":"","refund_id":260320000000555}`)
	}))
	defer srv.Close()

	p := zalopayTestProvider(t, srv.URL)
	res, err := p.RefundPayment(t.Context(), RefundRequest{
		ProviderRef: "260320_HTC_1",
		TransID:     "260320000000123",
		AmountMinor: 22000,
		Reason:      "customer request",
		RefundID:    "R1",
	})
	if err != nil {
		t.Fatalf("RefundPayment: %v", err)
	}

	// Refunds are ASYNC — never report success from this response alone.
	if res.Status != StatusPending {
		t.Fatalf("Status = %q, want %q", res.Status, StatusPending)
	}
	if res.RefundRef != "260320000000555" {
		t.Fatalf("RefundRef = %q, want the ZaloPay refund_id", res.RefundRef)
	}

	raw := <-bodyCh
	var body zalopayRefundRequest
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("decode refund body: %v\n%s", err, raw)
	}
	if strings.Contains(raw, "refund_fee_amount") {
		t.Fatalf("fee-less path is pinned — refund_fee_amount must be omitted:\n%s", raw)
	}
	if body.MRefundID != "260320_2553_R1" {
		t.Fatalf("m_refund_id = %q, want yymmdd_appid_refundid", body.MRefundID)
	}
	if len(body.MRefundID) > zalopayMRefundIDMaxLen {
		t.Fatalf("m_refund_id too long (%d): %q", len(body.MRefundID), body.MRefundID)
	}
	if body.ZpTransID != "260320000000123" || body.Amount != 22000 {
		t.Fatalf("zp_trans_id=%q amount=%d", body.ZpTransID, body.Amount)
	}
	if body.Timestamp != zalopayTestAppTimeMs || body.Description != "customer request" {
		t.Fatalf("timestamp=%d description=%q", body.Timestamp, body.Description)
	}
	wantMac := zalopayTestHMAC(zalopayTestKey1, `2553|260320000000123|22000|customer request|1774012800000`)
	if body.Mac != wantMac {
		t.Fatalf("refund mac:\n got %q\nwant %q", body.Mac, wantMac)
	}

	// m_refund_id stays ≤45 for a monster RefundID (hashed suffix).
	if got := zalopayMRefundID(strings.Repeat("R", 60), 2553, zalopayTestNow); len(got) > zalopayMRefundIDMaxLen {
		t.Fatalf("m_refund_id too long (%d): %q", len(got), got)
	}
}

func TestZaloPayRefundError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"return_code":2,"return_message":"FAIL","sub_return_code":-14,`+
			`"sub_return_message":"REFUND_AMOUNT_INVALID","refund_id":0}`)
	}))
	defer srv.Close()

	p := zalopayTestProvider(t, srv.URL)
	_, err := p.RefundPayment(t.Context(), RefundRequest{TransID: "260320000000123", AmountMinor: 22000, RefundID: "R1"})
	if err == nil || !strings.Contains(err.Error(), "-14") {
		t.Fatalf("want error carrying sub_return_code, got %v", err)
	}
}

// ─── QueryRefund (admin refund flow backstop) ───

func TestZaloPayQueryRefund(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
		want     PaymentStatus
		wantErr  bool
	}{
		{"sub 1 → succeeded", `{"return_code":1,"sub_return_code":1}`, StatusSucceeded, false},
		{"sub -1 REFUND_PENDING → pending", `{"return_code":1,"sub_return_code":-1}`, StatusPending, false},
		{"sub -16 processing → pending", `{"return_code":1,"sub_return_code":-16}`, StatusPending, false},
		{"sub -13 expired → failed", `{"return_code":2,"sub_return_code":-13}`, StatusFailed, false},
		{"sub -101 not found → error", `{"return_code":2,"sub_return_code":-101}`, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bodyCh := make(chan string, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v2/query_refund" {
					t.Errorf("path = %s, want /v2/query_refund", r.URL.Path)
				}
				b, _ := io.ReadAll(r.Body)
				bodyCh <- string(b)
				io.WriteString(w, tc.response)
			}))
			defer srv.Close()

			p := zalopayTestProvider(t, srv.URL)
			res, err := p.QueryRefund(t.Context(), "260320_2553_R1")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %+v", res)
				}
				return
			}
			if err != nil {
				t.Fatalf("QueryRefund: %v", err)
			}
			if res.Status != tc.want || res.RefundRef != "260320_2553_R1" {
				t.Fatalf("result: %+v, want %q", res, tc.want)
			}

			raw := <-bodyCh
			var body map[string]any
			if err := json.Unmarshal([]byte(raw), &body); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			wantMac := zalopayTestHMAC(zalopayTestKey1, `2553|260320_2553_R1|1774012800000`)
			if body["mac"] != wantMac {
				t.Fatalf("query_refund mac:\n got %v\nwant %s", body["mac"], wantMac)
			}
		})
	}
}

// ─── VoidPayment: no cancel API exists ───

func TestZaloPayVoidPayment(t *testing.T) {
	p := zalopayTestProvider(t, "")
	if err := p.VoidPayment(t.Context(), "260320_HTC_1", "user changed mind"); !errors.Is(err, ErrNotSupported) {
		t.Fatalf("got %v, want ErrNotSupported (ZaloPay has no cancel-order API)", err)
	}
}
