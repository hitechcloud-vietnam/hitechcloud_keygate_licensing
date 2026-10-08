// payOS (VietQR — api-merchant.payos.vn) one-off payment provider.
//
// Wire contract verified against the payOS API reference
// (https://payos.vn/docs/api/ … /webhook/ … /payment-link/, fetched 2026-10-08)
// and the official SDKs (payOSHQ/payos-lib-{node,golang,python,php,java,dotnet}),
// whose tests/crypto/testCases.json pins the exact signature encodings:
//
//   - CreatePayment    → POST /v2/payment-requests
//   - GetPaymentStatus → GET  /v2/payment-requests/{id}   (id = orderCode OR
//     paymentLinkId; we always hand out the decimal orderCode as ProviderRef)
//   - VoidPayment      → POST /v2/payment-requests/{id}/cancel
//     body {"cancellationReason": reason}
//   - ConfirmWebhook   → POST /confirm-webhook body {"webhookUrl": ...}
//     NOTE (deviation from the internal brief's guess): the path is the ROOT
//     /confirm-webhook, NOT /v2/payment-requests/confirm-webhook — confirmed on
//     the API reference and in every official SDK.
//   - RefundPayment    → ErrNotSupported: payOS v2 has NO merchant refund API
//     (refunds settle as manual bank transfers; the separate "payouts"/chi hộ
//     product is not a refund of a payment link).
//
// Signature rules (docs + SDK source, pinned byte-for-byte by tests against the
// official vectors):
//
//   - Create ("create-payment-link"): HMAC_SHA256(checksumKey,
//     "amount=$amount&cancelUrl=$cancelUrl&description=$description&orderCode=$orderCode&returnUrl=$returnUrl")
//     — fixed key order, raw interpolation (no URL-encoding), hex lowercase.
//   - Webhook `data` and response `body`: only the `data` OBJECT is signed;
//     keys sorted alphabetically joined "key=value&key=value…" (no
//     URL-encoding). Value rules (from the official signer
//     convertObjToQueryStr / PHP sample): null → "" (the strings "null" /
//     "undefined" also → "" — JS parity quirk, pinned); booleans/numbers/
//     strings → their string form; ARRAYS → JSON with element ORDER kept and
//     each object element's keys sorted, unicode unescaped (JSON_UNESCAPED_
//     UNICODE, no HTML escaping); nested objects (defensive only) → same JSON.
//     The top-level code/desc/success are NOT covered by the signature.
//
// orderCode derivation (deterministic, pinned in tests): payOS needs a unique
// positive per-merchant integer ≤ 2^53−1 (JS-safe; the official SDK validates
// exactly that range). Our OrderID (HTC-…) is hashed, never parsed:
//
//	orderCode = int64(be64(sha256(orderID)[0:8]) >> 11);  0 → 1
//
// so the same OrderID always yields the same orderCode in [1, 2^53).
// CreatePaymentResult.ProviderRef is that orderCode as a decimal string —
// the SAME key WebhookEvent.ProviderRef reports on IPN, so the handler glue
// must look orders up by ProviderRef (WebhookEvent.OrderID is always "" —
// payOS never echoes our order id).
//
// Money discipline (plan §51): all amounts are int64 VND whole dong. JSON
// numbers are decoded with UseNumber and re-encoded as literals — float64
// appears nowhere in this file.

package payment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/config"
)

const (
	payosHTTPTimeout      = 15 * time.Second
	payosDefaultLinkTTL   = 15 * time.Minute
	payosDescriptionMax   = 256 // runes; payOS caps description at 256 chars
	payosMaxResponseBytes = 1 << 20
	payosMinInt32         = -1 << 31 // expiredAt is Unix seconds as Int32
	payosMaxInt32         = 1<<31 - 1
	payosCreatePath       = "/v2/payment-requests"
	payosConfirmWebhook   = "/confirm-webhook" // root path — see file comment
	payosCodeOK           = "00"
)

// PayOSProvider implements PaymentProvider for payOS (VietQR). Immutable
// after construction — safe for concurrent use.
type PayOSProvider struct {
	payosClientID    string
	payosAPIKey      string
	payosChecksumKey string
	payosBaseURL     string
	payosHTTPClient  *http.Client
}

// NewPayOS builds a provider from PAYOS_* config. Credentials may be empty —
// Enabled() reports whether the integration is actually available (§25).
func NewPayOS(cfg config.PayOSConfig) *PayOSProvider {
	return &PayOSProvider{
		payosClientID:    cfg.ClientID,
		payosAPIKey:      cfg.APIKey,
		payosChecksumKey: cfg.ChecksumKey,
		payosBaseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		payosHTTPClient:  &http.Client{Timeout: payosHTTPTimeout},
	}
}

// Name returns the stable provider id.
func (p *PayOSProvider) Name() string { return ProviderPayOS }

// Enabled is all-or-nothing over the three required credentials.
func (p *PayOSProvider) Enabled() bool {
	return p != nil &&
		p.payosClientID != "" &&
		p.payosAPIKey != "" &&
		p.payosChecksumKey != ""
}

// payosCreateItem is one line item on the payment link.
type payosCreateItem struct {
	Name     string `json:"name"`
	Quantity int64  `json:"quantity"`
	Price    int64  `json:"price"` // int64 VND dong, never float
}

// payosCreateRequest is the POST /v2/payment-requests body.
type payosCreateRequest struct {
	OrderCode   int64             `json:"orderCode"`
	Amount      int64             `json:"amount"`
	Description string            `json:"description"`
	BuyerName   string            `json:"buyerName,omitempty"`
	BuyerEmail  string            `json:"buyerEmail,omitempty"`
	Items       []payosCreateItem `json:"items,omitempty"`
	CancelURL   string            `json:"cancelUrl"`
	ReturnURL   string            `json:"returnUrl"`
	ExpiredAt   int64             `json:"expiredAt"`
	Signature   string            `json:"signature"`
}

// payosEnvelope is the {code, desc, success, data, signature} shape shared by
// every payOS response and the inbound webhook.
type payosEnvelope struct {
	Code      string         `json:"code"`
	Desc      string         `json:"desc"`
	Success   bool           `json:"success"`
	Data      map[string]any `json:"data"`
	Signature string         `json:"signature"`
}

// payosAPIError is a business-level payOS error (HTTP 200, code != "00").
type payosAPIError struct {
	Code string
	Desc string
}

func (e payosAPIError) Error() string {
	return "payos: API error code=" + e.Code + " desc=" + e.Desc
}

// ─── PaymentProvider implementation ───

// CreatePayment creates a payOS payment link and returns the redirect.
// ProviderRef is the derived orderCode as a decimal string (see file comment).
func (p *PayOSProvider) CreatePayment(ctx context.Context, req CreatePaymentRequest) (*CreatePaymentResult, error) {
	if !p.Enabled() {
		return nil, ErrProviderNotConfigured
	}
	if req.Currency != "" && !strings.EqualFold(req.Currency, "VND") {
		return nil, ErrCurrencyNotSupported
	}
	if strings.TrimSpace(req.OrderID) == "" {
		return nil, fmt.Errorf("payos: order id is required")
	}
	if req.AmountMinor <= 0 {
		return nil, fmt.Errorf("payos: amount must be a positive int64 of VND dong")
	}
	returnURL := strings.TrimSpace(req.ReturnURL)
	if returnURL == "" {
		// The payOS API marks returnUrl (and cancelUrl) required.
		return nil, fmt.Errorf("payos: return URL is required by the payOS API")
	}
	cancelURL := strings.TrimSpace(req.CancelURL)
	if cancelURL == "" {
		cancelURL = returnURL // documented fallback: API marks both required
	}
	description := payosDescription(req.Description, req.OrderID)
	orderCode := payosOrderCode(req.OrderID)

	expiry := req.ExpiresAt
	if expiry.IsZero() {
		expiry = time.Now().Add(payosDefaultLinkTTL)
	}
	expiredAt := expiry.Unix()
	if expiredAt > payosMaxInt32 {
		expiredAt = payosMaxInt32 // API wants Unix seconds as Int32
	}
	if expiredAt < payosMinInt32 {
		expiredAt = payosMinInt32
	}

	items, err := payosItemsFromExtra(req.Extra)
	if err != nil {
		return nil, err
	}

	// Sign over EXACTLY the five canonical fields (docs-pinned order).
	signature := payosSignCreate(p.payosChecksumKey, req.AmountMinor, orderCode, cancelURL, description, returnURL)

	body := payosCreateRequest{
		OrderCode:   orderCode,
		Amount:      req.AmountMinor,
		Description: description,
		BuyerName:   req.BuyerName,
		BuyerEmail:  req.BuyerEmail,
		Items:       items,
		CancelURL:   cancelURL,
		ReturnURL:   returnURL,
		ExpiredAt:   expiredAt,
		Signature:   signature,
	}
	env, err := p.payosCall(ctx, http.MethodPost, payosCreatePath, body)
	if err != nil {
		return nil, err
	}
	if env.Code != payosCodeOK {
		return nil, payosAPIError{Code: env.Code, Desc: env.Desc}
	}
	data := env.Data
	if echo, ok := payosInt(data["orderCode"]); ok && echo != orderCode {
		return nil, fmt.Errorf("payos: response orderCode %d does not match derived %d", echo, orderCode)
	}
	payURL := payosStr(data["checkoutUrl"])
	if payURL == "" {
		return nil, fmt.Errorf("payos: create response missing checkoutUrl")
	}
	result := &CreatePaymentResult{
		ProviderRef: strconv.FormatInt(orderCode, 10),
		PayURL:      payURL,
		QRCode:      payosStr(data["qrCode"]),
		ExpiresAt:   expiry,
	}
	if at, ok := payosInt(data["expiredAt"]); ok && at > 0 {
		result.ExpiresAt = time.Unix(at, 0)
	}
	return result, nil
}

// CapturePayment aliases GetPaymentStatus (documented, not a stub): payOS
// settles automatically at the bank — there is nothing to capture.
func (p *PayOSProvider) CapturePayment(ctx context.Context, ref string) (*PaymentStatusResult, error) {
	return p.GetPaymentStatus(ctx, ref)
}

// RefundPayment: payOS v2 exposes NO merchant refund API — refunds happen as
// manual bank transfers outside the platform (and the separate "payouts"/chi hộ
// API is a payout product with its own account+signature, not a refund of a
// payment link). Returning ErrNotSupported instead of a fake success is the
// provider.go contract.
func (p *PayOSProvider) RefundPayment(ctx context.Context, req RefundRequest) (*RefundResult, error) {
	return nil, ErrNotSupported
}

// VoidPayment cancels a not-yet-settled payment link. Idempotent: a link that
// is already CANCELLED or EXPIRED counts as voided (nil).
func (p *PayOSProvider) VoidPayment(ctx context.Context, ref, reason string) error {
	if !p.Enabled() {
		return ErrProviderNotConfigured
	}
	if strings.TrimSpace(ref) == "" {
		return fmt.Errorf("payos: payment reference is required")
	}
	env, err := p.payosCall(ctx, http.MethodPost,
		payosCreatePath+"/"+url.PathEscape(ref)+"/cancel",
		map[string]any{"cancellationReason": reason})
	if err != nil {
		return err
	}
	if env.Code == payosCodeOK {
		return nil
	}
	// Already cancelled/expired → idempotent success (verified, not guessed).
	if st, gerr := p.GetPaymentStatus(ctx, ref); gerr == nil &&
		(st.Status == StatusCancelled || st.Status == StatusExpired) {
		return nil
	}
	return payosAPIError{Code: env.Code, Desc: env.Desc}
}

// GetPaymentStatus queries the authoritative payment-link state. ref may be
// the decimal orderCode (what we hand out) or a payOS paymentLinkId.
func (p *PayOSProvider) GetPaymentStatus(ctx context.Context, ref string) (*PaymentStatusResult, error) {
	if !p.Enabled() {
		return nil, ErrProviderNotConfigured
	}
	if strings.TrimSpace(ref) == "" {
		return nil, fmt.Errorf("payos: payment reference is required")
	}
	env, err := p.payosCall(ctx, http.MethodGet, payosCreatePath+"/"+url.PathEscape(ref), nil)
	if err != nil {
		return nil, err
	}
	if env.Code != payosCodeOK {
		return nil, payosAPIError{Code: env.Code, Desc: env.Desc}
	}
	data := env.Data
	status, err := payosMapStatus(payosStr(data["status"]))
	if err != nil {
		return nil, err
	}
	amount, _ := payosInt(data["amount"])
	transID := payosStr(data["reference"])
	var paidAt time.Time
	if txn := payosFirstTransaction(data); txn != nil {
		if transID == "" {
			transID = payosStr(txn["reference"])
		}
		if status == StatusSucceeded {
			paidAt = payosParsePayOSTime(payosStr(txn["transactionDateTime"]))
		}
	}
	currency := payosStr(data["currency"])
	if currency == "" {
		currency = "VND" // payOS settles VND only; GET has no currency field
	}
	return &PaymentStatusResult{
		ProviderRef: ref,
		TransID:     transID,
		Status:      status,
		AmountMinor: amount,
		Currency:    currency,
		PaidAt:      paidAt,
		Raw:         data,
	}, nil
}

// VerifyWebhook authenticates a raw payOS webhook body and normalizes it.
//
// Only the `data` object is signed (top-level code/desc/success are NOT), so
// the MAC is verified over `data` with hmac.Equal BEFORE any field is read —
// fail closed on mismatch. Status mapping: success==true AND data.code=="00"
// → StatusSucceeded, everything else → StatusFailed (the top-level `success`
// flag is unsigned; treat failure events as advisory and reconcile with
// GetPaymentStatus before cancelling anything).
//
// OrderID is always "": payOS does not echo our order id — the routing key is
// ProviderRef (the decimal orderCode stored at CreatePayment time).
func (p *PayOSProvider) VerifyWebhook(payload []byte) (*WebhookEvent, error) {
	if p == nil || p.payosChecksumKey == "" {
		return nil, ErrProviderNotConfigured
	}
	env, err := payosParseEnvelope(payload)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWebhookPayloadMalformed, err)
	}
	if env.Data == nil {
		return nil, ErrWebhookPayloadMalformed
	}
	if env.Signature == "" {
		return nil, ErrWebhookSignatureInvalid
	}
	want := payosSignData(p.payosChecksumKey, env.Data)
	got := strings.ToLower(env.Signature)
	if len(got) != len(want) || !hmac.Equal([]byte(got), []byte(want)) {
		return nil, ErrWebhookSignatureInvalid
	}
	// Signature valid — only now the fields may be trusted.
	orderCode, ok := payosInt(env.Data["orderCode"])
	if !ok {
		return nil, fmt.Errorf("%w: missing orderCode", ErrWebhookPayloadMalformed)
	}
	amount, ok := payosInt(env.Data["amount"])
	if !ok {
		return nil, fmt.Errorf("%w: missing amount", ErrWebhookPayloadMalformed)
	}
	status := StatusFailed
	if env.Success && payosStr(env.Data["code"]) == payosCodeOK {
		status = StatusSucceeded
	}
	currency := payosStr(env.Data["currency"])
	if currency == "" {
		currency = "VND"
	}
	event := &WebhookEvent{
		Provider:    ProviderPayOS,
		OrderID:     "", // payOS never echoes our OrderID — route on ProviderRef
		ProviderRef: strconv.FormatInt(orderCode, 10),
		TransID:     payosStr(env.Data["reference"]),
		Status:      status,
		AmountMinor: amount,
		Currency:    currency,
		Raw:         env.Data,
	}
	if status == StatusSucceeded {
		event.PaidAt = payosParsePayOSTime(payosStr(env.Data["transactionDateTime"]))
	}
	return event, nil
}

// ConfirmWebhook registers/validates the IPN URL with payOS
// (POST /confirm-webhook body {"webhookUrl": ...}). payOS fires a TEST webhook
// at the URL first — the handler must ACK it (HTTP 2xx) even though the sample
// orderCode matches no order. Extra method, not part of PaymentProvider.
func (p *PayOSProvider) ConfirmWebhook(ctx context.Context, webhookURL string) error {
	if !p.Enabled() {
		return ErrProviderNotConfigured
	}
	if strings.TrimSpace(webhookURL) == "" {
		return fmt.Errorf("payos: webhook URL is required")
	}
	env, err := p.payosCall(ctx, http.MethodPost, payosConfirmWebhook, map[string]any{"webhookUrl": webhookURL})
	if err != nil {
		return err
	}
	if env.Code != payosCodeOK {
		return payosAPIError{Code: env.Code, Desc: env.Desc}
	}
	return nil
}

// ─── HTTP plumbing ───

// payosCall performs an authenticated JSON request and decodes the envelope.
// Every request carries x-client-id, x-api-key and Content-Type (docs).
func (p *PayOSProvider) payosCall(ctx context.Context, method, path string, body any) (*payosEnvelope, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("payos: encode request: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.payosBaseURL+path, reader)
	if err != nil {
		return nil, fmt.Errorf("payos: build request: %w", err)
	}
	req.Header.Set("x-client-id", p.payosClientID)
	req.Header.Set("x-api-key", p.payosAPIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.payosHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("payos: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, payosMaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("payos: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("payos: HTTP %d from %s: %s", resp.StatusCode, path, payosSnippet(raw))
	}
	env, err := payosParseEnvelope(raw)
	if err != nil {
		return nil, fmt.Errorf("payos: bad response envelope: %w", err)
	}
	return env, nil
}

func payosParseEnvelope(payload []byte) (*payosEnvelope, error) {
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber() // int64-exact money: numbers stay json.Number, never float64
	var env payosEnvelope
	if err := dec.Decode(&env); err != nil {
		return nil, err
	}
	return &env, nil
}

func payosSnippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 256 {
		s = s[:256] + "…"
	}
	return s
}

// ─── Signing & encoding (pinned against the official vectors) ───

// payosOrderCode derives the payOS orderCode from OUR OrderID:
//
//	orderCode = int64(be64(sha256(orderID)[0:8]) >> 11);  0 → 1
//
// Deterministic, positive, < 2^53 (JS-safe — payOS rejects 0 and anything
// above 2^53−1). Golden pin: payosOrderCode("abc") == 6560798095827001
// (derived from the NIST SHA-256("abc") vector, verified twice by hand).
func payosOrderCode(orderID string) int64 {
	sum := sha256.Sum256([]byte(orderID))
	v := int64(binary.BigEndian.Uint64(sum[:8]) >> 11)
	if v == 0 {
		v = 1 // payOS rejects orderCode 0
	}
	return v
}

// payosHMACHex is hex_lowercase(HMAC_SHA256(key, msg)).
func payosHMACHex(key, msg string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

// payosSignCreate signs the five canonical create-payment-link fields in the
// exact docs-pinned order (raw interpolation, no URL-encoding).
func payosSignCreate(key string, amount, orderCode int64, cancelURL, description, returnURL string) string {
	raw := fmt.Sprintf("amount=%d&cancelUrl=%s&description=%s&orderCode=%d&returnUrl=%s",
		amount, cancelURL, description, orderCode, returnURL)
	return payosHMACHex(key, raw)
}

// payosSignData signs the webhook/response `data` object.
func payosSignData(key string, data map[string]any) string {
	return payosHMACHex(key, payosEncodeData(data))
}

// payosEncodeData renders `data` as "key=value&…" for signing: keys sorted
// alphabetically; null (and the strings "null"/"undefined" — JS quirk) → "";
// arrays → JSON with element order preserved and each object element's keys
// sorted, unicode unescaped; scalars → their string form. Matches the official
// signer (sortObjDataByKey + convertObjToQueryStr) byte for byte — the tests
// pin this with the official HMAC vectors and exact encoded strings.
func payosEncodeData(data map[string]any) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys) // byte order == JS default sort for ASCII field names
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		var s string
		switch t := data[k].(type) {
		case nil:
			s = ""
		case []any:
			s = payosMarshalSorted(t)
		case map[string]any:
			s = payosMarshalSorted(t) // defensive: docs never nest objects here
		case string:
			if t == "null" || t == "undefined" { // JS SDK parity quirk
				s = ""
			} else {
				s = t
			}
		default:
			s = payosStr(data[k]) // numbers → literal, bools → true|false
		}
		parts = append(parts, k+"="+s)
	}
	return strings.Join(parts, "&")
}

// payosMarshalSorted JSON-encodes a value with object keys sorted recursively
// and strings escaped exactly like JS JSON.stringify (unicode raw, HTML raw,
// \b \f \n \r \t shorthands) — i.e. JSON_UNESCAPED_UNICODE parity with the
// PHP/Node reference signers.
func payosMarshalSorted(v any) string {
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		if t {
			return "true"
		}
		return "false"
	case json.Number:
		return t.String() // literal as sent — matches String(number) for ints
	case string:
		return payosJSONString(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			parts = append(parts, payosMarshalSorted(e))
		}
		return "[" + strings.Join(parts, ",") + "]"
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(payosJSONString(k))
			b.WriteByte(':')
			b.WriteString(payosMarshalSorted(t[k]))
		}
		b.WriteByte('}')
		return b.String()
	default:
		// Unreachable from UseNumber decoding; refuse to guess.
		return "null"
	}
}

// payosJSONString quotes a string exactly like JS JSON.stringify.
func payosJSONString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
			} else {
				b.WriteRune(r) // raw UTF-8 — JS leaves unicode + U+2028/2029 raw
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// ─── Value helpers (int64-exact, float64-free) ───

// payosStr renders a decoded JSON scalar as its string form.
func payosStr(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case json.Number:
		return t.String()
	case bool:
		if t {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// payosInt parses a decoded JSON scalar as int64 without float64.
func payosInt(v any) (int64, bool) {
	switch t := v.(type) {
	case json.Number:
		i, err := strconv.ParseInt(t.String(), 10, 64)
		if err != nil {
			return 0, false
		}
		return i, true
	case string:
		i, err := strconv.ParseInt(t, 10, 64)
		if err != nil {
			return 0, false
		}
		return i, true
	case int:
		return int64(t), true
	case int64:
		return t, true
	default:
		return 0, false
	}
}

// payosMapStatus maps the payOS PaymentLinkStatus enum — all 7 values from the
// official SDKs (PENDING, PROCESSING, UNDERPAID, PAID, EXPIRED, CANCELLED,
// FAILED) — onto the normalized lifecycle. UNDERPAID/PROCESSING are in-flight
// (never succeeded: the caller must re-check AmountMinor). Unknown values fail
// closed so a new payOS status can never silently pass for paid.
func payosMapStatus(raw string) (PaymentStatus, error) {
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "PENDING", "PROCESSING", "UNDERPAID":
		return StatusPending, nil
	case "PAID":
		return StatusSucceeded, nil
	case "EXPIRED":
		return StatusExpired, nil
	case "CANCELLED":
		return StatusCancelled, nil
	case "FAILED":
		return StatusFailed, nil
	default:
		return "", fmt.Errorf("payos: unknown payment status %q (update payosMapStatus)", raw)
	}
}

func payosFirstTransaction(data map[string]any) map[string]any {
	txns, _ := data["transactions"].([]any)
	for _, raw := range txns {
		if m, ok := raw.(map[string]any); ok {
			return m
		}
	}
	return nil
}

func payosParsePayOSTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts
		}
	}
	return time.Time{}
}

// payosDescription normalizes the buyer-visible description: fallback text
// (payOS rejects empty descriptions), rune-safe truncation at 256 chars — all
// BEFORE signing so the signature covers exactly what is sent.
func payosDescription(desc, orderID string) string {
	d := strings.TrimSpace(desc)
	if d == "" {
		d = "Thanh toan don hang " + orderID
	}
	if runes := []rune(d); len(runes) > payosDescriptionMax {
		d = string(runes[:payosDescriptionMax])
	}
	return d
}

// payosItemsFromExtra reads optional line items from the provider-specific
// escape hatch (Extra["items"] = JSON [{"name","quantity","price"}]) — the
// shared CreatePaymentRequest has no structured items field. Items are NOT
// part of the signature (docs: only the five canonical fields are).
func payosItemsFromExtra(extra map[string]string) ([]payosCreateItem, error) {
	raw, ok := extra["items"]
	if !ok || raw == "" {
		return nil, nil
	}
	var items []payosCreateItem
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("payos: invalid items JSON in Extra: %w", err)
	}
	for _, it := range items {
		if it.Name == "" || it.Quantity <= 0 || it.Price < 0 {
			return nil, fmt.Errorf("payos: items need name, quantity > 0 and price >= 0 (VND dong)")
		}
	}
	return items, nil
}
