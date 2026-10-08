package payment

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/config"
)

// Pay2S (payment.pay2s.vn) — bank-transfer / Napas 247 QR gateway.
//
// API reference (docs.pay2s.vn): "Tích hợp kỹ thuật" (create), "Hủy đơn
// hàng" (cancel), "Thanh toán thành công" / "Instant Payment Notification"
// (IPN), "Chữ ký" (signature). Where this file deviates from the original
// brief the DOCUMENTATION wins — see the FINAL REPORT.
//
// Signing (HMAC-SHA256, lowercase hex, key = SecretKey):
//   - create: canonical string over all request fields, "bankAccounts"
//     contributed as the literal "Array";
//   - IPN:    canonical string over the 13 m2signature fields;
//   - cancel: canonical string over accessKey/orderId/partnerCode/
//     requestId plus requestType=cancel.

const (
	pay2sProductionBaseURL = "https://payment.pay2s.vn"
	pay2sCreatePath        = "/v1/gateway/api/create"
	pay2sCancelPath        = "/v1/gateway/api/cancel"
	pay2sOrderType         = "pay2s"
	pay2sRequestTypeCreate = "pay2s"
	pay2sRequestTypeCancel = "cancel"
	// pay2sMaxRequestIDLen: V1 documents requestId as String(50); V2
	// allows ≤80 of [A-Za-z0-9._:-]. We honor the stricter bound.
	pay2sMaxRequestIDLen = 50
)

// pay2sICT is the Pay2S timestamp timezone (responseTime "20060102150405"
// is ICT, UTC+7).
var pay2sICT = time.FixedZone("ICT", 7*3600)

// pay2sHTTPClient is shared so connection pooling applies; every request
// carries the caller's ctx and this client adds a hard timeout.
var pay2sHTTPClient = &http.Client{Timeout: 15 * time.Second}

// pay2sBankAccount is one entry of PAY2S_BANK_ACCOUNTS
// ("bankId|accountNumber|accountName|bankName").
type pay2sBankAccount struct {
	BankID        string
	AccountNumber string
	AccountName   string
	BankName      string
}

// Pay2SProvider implements PaymentProvider for Pay2S.
//
// Credentials come from a getter resolved at call time, so an operator
// can complete or rotate them in the admin config (config-in-DB)
// without restarting the server. NewPay2S wraps a constant getter for
// callers that configure once at boot.
type Pay2SProvider struct {
	pay2sCreds func(context.Context) (config.Pay2SConfig, error)
}

var _ PaymentProvider = (*Pay2SProvider)(nil)

// cfgsvcCredsTimeout bounds credential reads on paths that have no
// request context of their own (Enabled, webhook verification): the
// getter may reach the database and must not hang a status probe.
const cfgsvcCredsTimeout = 2 * time.Second

// cfgsvcCredsCtx returns the bounded context for such a read.
func cfgsvcCredsCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), cfgsvcCredsTimeout)
}

// NewPay2S builds a provider from static configuration — a thin
// wrapper over a constant getter, so every boot-configured caller and
// test keeps working unchanged.
func NewPay2S(cfg config.Pay2SConfig) *Pay2SProvider {
	return NewPay2SDynamic(func(context.Context) (config.Pay2SConfig, error) { return cfg, nil })
}

// NewPay2SDynamic builds a provider whose credentials are read
// through get at call time (e.g. service.ConfigService.Pay2SGetter —
// DB over env over default).
func NewPay2SDynamic(get func(context.Context) (config.Pay2SConfig, error)) *Pay2SProvider {
	return &Pay2SProvider{pay2sCreds: get}
}

// Name returns the stable provider id.
func (p *Pay2SProvider) Name() string { return ProviderPay2S }

// pay2sConfig resolves the credentials in effect right now, together
// with their parsed bank accounts — both derived from ONE read, so
// Enabled() and a request can never disagree.
func (p *Pay2SProvider) pay2sConfig(ctx context.Context) (config.Pay2SConfig, []pay2sBankAccount, error) {
	cfg := config.Pay2SConfig{}
	if p != nil && p.pay2sCreds != nil {
		c, err := p.pay2sCreds(ctx)
		if err != nil {
			return cfg, nil, err
		}
		cfg = c
	}
	if strings.TrimSpace(cfg.BaseURL) == "" {
		cfg.BaseURL = pay2sProductionBaseURL
	}
	return cfg, pay2sParseBankAccounts(cfg.BankAccounts), nil
}

// Enabled is all-or-nothing over the credentials the API actually needs:
// partner code, access key (signing + auth), secret key (HMAC) and at
// least one bank account (the create API requires bankAccounts).
func (p *Pay2SProvider) Enabled() bool {
	ctx, cancel := cfgsvcCredsCtx()
	defer cancel()
	cfg, banks, err := p.pay2sConfig(ctx)
	if err != nil {
		return false
	}
	return cfg.PartnerCode != "" &&
		cfg.AccessKey != "" &&
		cfg.SecretKey != "" &&
		len(banks) > 0
}

// pay2sParseBankAccounts parses repeated entries of the form
// "bankId|accountNumber|accountName|bankName", separated by ',' or ';'.
// Garbage entries are skipped (a malformed optional entry must not take
// the whole provider down); entries missing bankId or accountNumber are
// not usable and are dropped.
func pay2sParseBankAccounts(raw string) []pay2sBankAccount {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	normalized := strings.NewReplacer(",", "\n", ";", "\n").Replace(raw)
	var out []pay2sBankAccount
	for _, line := range strings.Split(normalized, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) < 2 {
			continue
		}
		acct := pay2sBankAccount{
			BankID:        strings.TrimSpace(fields[0]),
			AccountNumber: strings.TrimSpace(fields[1]),
		}
		if acct.BankID == "" || acct.AccountNumber == "" {
			continue
		}
		if len(fields) > 2 {
			acct.AccountName = strings.TrimSpace(fields[2])
		}
		if len(fields) > 3 {
			acct.BankName = strings.TrimSpace(fields[3])
		}
		out = append(out, acct)
	}
	return out
}

// pay2sHMACHex returns the lowercase hex HMAC-SHA256 of raw under secret.
func pay2sHMACHex(secret, raw string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

// pay2sCreateSignatureRaw builds the canonical create string exactly as
// documented: keys in fixed (alphabetical-ish) order, "bankAccounts"
// contributed as the literal "Array", every key present even when empty.
func pay2sCreateSignatureRaw(accessKey, amount, ipnURL, orderID, orderInfo, partnerCode, redirectURL, requestID, requestType string) string {
	return "accessKey=" + accessKey +
		"&amount=" + amount +
		"&bankAccounts=Array" +
		"&ipnUrl=" + ipnURL +
		"&orderId=" + orderID +
		"&orderInfo=" + orderInfo +
		"&partnerCode=" + partnerCode +
		"&redirectUrl=" + redirectURL +
		"&requestId=" + requestID +
		"&requestType=" + requestType
}

// pay2sIPNSignatureRaw builds the canonical string over the 13 fields of
// the IPN m2signature. Empty fields still contribute "key=" — the set of
// keys is always complete.
func pay2sIPNSignatureRaw(accessKey string, ipn *pay2sIPN) string {
	return "accessKey=" + accessKey +
		"&amount=" + ipn.Amount.s +
		"&extraData=" + ipn.ExtraData.s +
		"&message=" + ipn.Message.s +
		"&orderId=" + ipn.OrderID.s +
		"&orderInfo=" + ipn.OrderInfo.s +
		"&orderType=" + ipn.OrderType.s +
		"&partnerCode=" + ipn.PartnerCode.s +
		"&payType=" + ipn.PayType.s +
		"&requestId=" + ipn.RequestID.s +
		"&responseTime=" + ipn.ResponseTime.s +
		"&resultCode=" + ipn.ResultCode.s +
		"&transId=" + ipn.TransID.s
}

// pay2sCancelSignatureRaw builds the canonical cancel string.
func pay2sCancelSignatureRaw(accessKey, orderID, partnerCode, requestID string) string {
	return "accessKey=" + accessKey +
		"&orderId=" + orderID +
		"&partnerCode=" + partnerCode +
		"&requestId=" + requestID +
		"&requestType=" + pay2sRequestTypeCancel
}

// pay2sOrderInfo derives the Pay2S orderInfo from OUR order id:
// "HTCPAY" + 16 uppercase hex chars (first 8 bytes of SHA-256), i.e. 22
// alphanumeric chars — inside the documented 10–32 range. Deterministic,
// so a retried create for the same order reuses the same value, and
// unique per order (a free-form Description is not guaranteed unique).
func pay2sOrderInfo(orderID string) string {
	sum := sha256.Sum256([]byte(orderID))
	return "HTCPAY" + strings.ToUpper(hex.EncodeToString(sum[:8]))
}

// pay2sNewRequestID builds a fresh requestId: sanitized seed plus an
// 8-hex random suffix, capped at pay2sMaxRequestIDLen bytes, restricted
// to [A-Za-z0-9._:-]. Callers pass a per-operation seed (order id for
// create, "CANCEL-<order id>" for cancel — cancel requires a FRESH id,
// not the create requestId).
func pay2sNewRequestID(seed string) string {
	var b strings.Builder
	for _, r := range seed {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '.', r == '_', r == ':', r == '-':
			b.WriteRune(r)
		}
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix) // crypto/rand.Read never fails
	id := b.String() + "-" + hex.EncodeToString(suffix)
	if len(id) > pay2sMaxRequestIDLen {
		id = id[:pay2sMaxRequestIDLen]
	}
	return id
}

// pay2sLit captures the WIRE LITERAL of a JSON field so the exact string
// Pay2S signed can be reproduced: JSON strings decode to their value,
// numbers keep their literal token (the create response shows amount as a
// string while the IPN sends numbers), null/missing stay "". Pay2S signs
// what it sent, not what we parsed.
type pay2sLit struct {
	s string
}

func (l *pay2sLit) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		l.s = ""
		return nil
	}
	if data[0] == '"' {
		var v string
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		l.s = v
		return nil
	}
	l.s = string(data)
	return nil
}

// pay2sIPN is the raw IPN body ("m2signature" authenticates it).
type pay2sIPN struct {
	PartnerCode  pay2sLit `json:"partnerCode"`
	OrderID      pay2sLit `json:"orderId"`
	RequestID    pay2sLit `json:"requestId"`
	Amount       pay2sLit `json:"amount"`
	OrderInfo    pay2sLit `json:"orderInfo"`
	OrderType    pay2sLit `json:"orderType"`
	TransID      pay2sLit `json:"transId"`
	ResultCode   pay2sLit `json:"resultCode"`
	Message      pay2sLit `json:"message"`
	PayType      pay2sLit `json:"payType"`
	ResponseTime pay2sLit `json:"responseTime"`
	ExtraData    pay2sLit `json:"extraData"`
	M2Signature  pay2sLit `json:"m2signature"`
}

// pay2sBankAccountsItem is the wire shape of one bankAccounts entry.
type pay2sBankAccountsItem struct {
	AccountNumber string `json:"account_number"`
	BankID        string `json:"bank_id"`
}

// pay2sCreateRequest is the POST body of /v1/gateway/api/create.
// Field order below is the JSON emission order (Go marshals in
// declaration order).
type pay2sCreateRequest struct {
	AccessKey    string                  `json:"accessKey"`
	PartnerCode  string                  `json:"partnerCode"`
	PartnerName  string                  `json:"partnerName"`
	RequestID    string                  `json:"requestId"`
	Amount       int64                   `json:"amount"` // int64 dong, JSON number
	OrderID      string                  `json:"orderId"`
	OrderInfo    string                  `json:"orderInfo"`
	OrderType    string                  `json:"orderType"`
	BankAccounts []pay2sBankAccountsItem `json:"bankAccounts"`
	RedirectURL  string                  `json:"redirectUrl"`
	IPNURL       string                  `json:"ipnUrl"`
	RequestType  string                  `json:"requestType"`
	Signature    string                  `json:"signature"`
}

// pay2sQRItem is one qrList entry. qrCode arrives as a ready data-URI —
// we pass it through untouched.
type pay2sQRItem struct {
	BankID        string `json:"bank_id"`
	AccountNumber string `json:"account_number"`
	AccountName   string `json:"account_name"`
	QRCode        string `json:"qrCode"`
	QRURL         string `json:"qrUrl"`
}

// pay2sCreateResponse is the create API response. amount is a STRING in
// the documented samples and resultCode may also vary in type — both go
// through pay2sLit so the wire literal is preserved.
type pay2sCreateResponse struct {
	PartnerCode pay2sLit      `json:"partnerCode"`
	RequestID   pay2sLit      `json:"requestId"`
	OrderID     pay2sLit      `json:"orderId"`
	OrderInfo   pay2sLit      `json:"orderInfo"`
	Amount      pay2sLit      `json:"amount"`
	Message     pay2sLit      `json:"message"`
	Lang        pay2sLit      `json:"lang"`
	ResultCode  pay2sLit      `json:"resultCode"`
	QRList      []pay2sQRItem `json:"qrList"`
	PayURL      string        `json:"payUrl"`
}

// pay2sCancelRequest is the POST body of /v1/gateway/api/cancel —
// exactly these six keys.
type pay2sCancelRequest struct {
	AccessKey   string `json:"accessKey"`
	PartnerCode string `json:"partnerCode"`
	OrderID     string `json:"orderId"`
	RequestID   string `json:"requestId"`
	RequestType string `json:"requestType"`
	Signature   string `json:"signature"`
}

// pay2sCancelResponse: success is HTTP 200 {"resultCode":0,"status":
// "cancelled",...} while errors are {"status":false,"message":...} —
// status is polymorphic, hence pay2sLit.
type pay2sCancelResponse struct {
	Status     pay2sLit `json:"status"`
	ResultCode pay2sLit `json:"resultCode"`
	Message    pay2sLit `json:"message"`
}

// pay2sPostJSON POSTs body as UTF-8 JSON and returns the HTTP status plus
// a capped response body (1 MiB).
func pay2sPostJSON(ctx context.Context, url string, body any) (int, []byte, error) {
	buf, err := json.Marshal(body)
	if err != nil {
		return 0, nil, fmt.Errorf("pay2s: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return 0, nil, fmt.Errorf("pay2s: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	resp, err := pay2sHTTPClient.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("pay2s: post: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("pay2s: read response: %w", err)
	}
	return resp.StatusCode, data, nil
}

func pay2sCreateURL(base string) string {
	return strings.TrimRight(base, "/") + pay2sCreatePath
}

func pay2sCancelURL(base string) string {
	return strings.TrimRight(base, "/") + pay2sCancelPath
}

// CreatePayment starts a Pay2S collection-link payment. ProviderRef is
// OUR orderId verbatim — the IPN identifies the order by orderId ("Hệ
// thống đối tác nên nhận diện đơn bằng orderId"); the IPN requestId is an
// internal id and unusable as a handle.
func (p *Pay2SProvider) CreatePayment(ctx context.Context, req CreatePaymentRequest) (*CreatePaymentResult, error) {
	cfg, banks, err := p.pay2sConfig(ctx)
	if err != nil {
		return nil, err
	}
	if cfg.PartnerCode == "" || cfg.AccessKey == "" || cfg.SecretKey == "" || len(banks) == 0 {
		return nil, ErrProviderNotConfigured
	}
	if req.Currency != "VND" {
		return nil, ErrCurrencyNotSupported
	}
	if req.AmountMinor <= 0 {
		return nil, fmt.Errorf("pay2s: amount must be positive")
	}
	if req.OrderID == "" || len(req.OrderID) > 64 {
		return nil, fmt.Errorf("pay2s: order id must be 1..64 bytes")
	}
	if req.ReturnURL == "" {
		return nil, fmt.Errorf("pay2s: return url is required")
	}
	// ipnUrl is required by the create API; the platform's webhook URL
	// arrives through the Extra escape hatch.
	ipnURL := ""
	if req.Extra != nil {
		ipnURL = req.Extra["webhook_url"]
	}
	if ipnURL == "" {
		return nil, fmt.Errorf("pay2s: Extra[\"webhook_url\"] (ipnUrl) is required")
	}

	orderInfo := pay2sOrderInfo(req.OrderID)
	requestID := pay2sNewRequestID(req.OrderID)
	amountStr := strconv.FormatInt(req.AmountMinor, 10) // int64 dong
	signature := pay2sHMACHex(cfg.SecretKey, pay2sCreateSignatureRaw(
		cfg.AccessKey, amountStr, ipnURL, req.OrderID, orderInfo,
		cfg.PartnerCode, req.ReturnURL, requestID, pay2sRequestTypeCreate,
	))

	bankAccounts := make([]pay2sBankAccountsItem, 0, len(banks))
	for _, a := range banks {
		bankAccounts = append(bankAccounts, pay2sBankAccountsItem{
			AccountNumber: a.AccountNumber,
			BankID:        a.BankID,
		})
	}
	body := pay2sCreateRequest{
		AccessKey:    cfg.AccessKey,
		PartnerCode:  cfg.PartnerCode,
		PartnerName:  cfg.PartnerName,
		RequestID:    requestID,
		Amount:       req.AmountMinor,
		OrderID:      req.OrderID,
		OrderInfo:    orderInfo,
		OrderType:    pay2sOrderType,
		BankAccounts: bankAccounts,
		RedirectURL:  req.ReturnURL,
		IPNURL:       ipnURL,
		RequestType:  pay2sRequestTypeCreate,
		Signature:    signature,
	}

	status, data, err := pay2sPostJSON(ctx, pay2sCreateURL(cfg.BaseURL), body)
	if err != nil {
		return nil, err
	}
	var resp pay2sCreateResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("pay2s: decode create response: %w", err)
	}
	if status >= 400 || resp.ResultCode.s != "0" {
		return nil, fmt.Errorf("pay2s: create payment failed: resultCode=%s message=%q",
			resp.ResultCode.s, resp.Message.s)
	}
	qrCode := ""
	if len(resp.QRList) > 0 {
		qrCode = resp.QRList[0].QRCode
	}
	// Pay2S reports no link expiry → ExpiresAt stays zero (contract:
	// "zero when the gateway said nothing").
	return &CreatePaymentResult{
		ProviderRef: req.OrderID,
		PayURL:      resp.PayURL,
		QRCode:      qrCode,
	}, nil
}

// VerifyWebhook authenticates an IPN body and normalizes it. Fail closed:
// the constant-time HMAC check runs BEFORE any field is trusted; only
// then are numeric fields parsed and mapped.
//
// resultCode mapping (documented result codes): 0 = success, 9000 =
// pending, anything else = failed.
func (p *Pay2SProvider) VerifyWebhook(payload []byte) (*WebhookEvent, error) {
	credsCtx, cancel := cfgsvcCredsCtx()
	defer cancel()
	cfg, _, err := p.pay2sConfig(credsCtx)
	if err != nil {
		return nil, err
	}
	var ipn pay2sIPN
	if err := json.Unmarshal(payload, &ipn); err != nil {
		return nil, ErrWebhookPayloadMalformed
	}
	want := pay2sHMACHex(cfg.SecretKey, pay2sIPNSignatureRaw(cfg.AccessKey, &ipn))
	if !hmac.Equal([]byte(ipn.M2Signature.s), []byte(want)) {
		return nil, ErrWebhookSignatureInvalid
	}
	// MAC verified — the payload is authentic, but it may still be for
	// ANOTHER partner; that is a signature-level failure, not ours.
	if ipn.PartnerCode.s != cfg.PartnerCode {
		return nil, ErrWebhookSignatureInvalid
	}
	amount, err := strconv.ParseInt(ipn.Amount.s, 10, 64)
	if err != nil {
		return nil, ErrWebhookPayloadMalformed
	}
	resultCode, err := strconv.ParseInt(ipn.ResultCode.s, 10, 64)
	if err != nil {
		return nil, ErrWebhookPayloadMalformed
	}

	var status PaymentStatus
	switch resultCode {
	case 0:
		status = StatusSucceeded
	case 9000:
		status = StatusPending
	default:
		status = StatusFailed
	}

	var paidAt time.Time
	if status == StatusSucceeded {
		if t, err := time.ParseInLocation("20060102150405", ipn.ResponseTime.s, pay2sICT); err == nil {
			paidAt = t
		}
	}

	return &WebhookEvent{
		Provider:    ProviderPay2S,
		OrderID:     ipn.OrderID.s,
		ProviderRef: ipn.OrderID.s, // ProviderRef convention: orderId
		TransID:     ipn.TransID.s,
		Status:      status,
		AmountMinor: amount,
		Currency:    "VND",
		PaidAt:      paidAt,
		Raw: map[string]any{
			"partnerCode":  ipn.PartnerCode.s,
			"orderId":      ipn.OrderID.s,
			"requestId":    ipn.RequestID.s,
			"amount":       amount,
			"orderInfo":    ipn.OrderInfo.s,
			"orderType":    ipn.OrderType.s,
			"transId":      ipn.TransID.s,
			"resultCode":   resultCode,
			"message":      ipn.Message.s,
			"payType":      ipn.PayType.s,
			"responseTime": ipn.ResponseTime.s,
			"extraData":    ipn.ExtraData.s,
			"m2signature":  ipn.M2Signature.s,
		},
	}, nil
}

// VoidPayment cancels a pending Pay2S payment (documented cancel order:
// only pending → cancelled). ref is the ProviderRef from CreatePayment —
// i.e. our orderId. Pay2S requires a FRESH requestId per cancel call (not
// the create requestId). reason is accepted for the PaymentProvider
// contract but NOT transmitted — the cancel API has no reason field.
func (p *Pay2SProvider) VoidPayment(ctx context.Context, ref, reason string) error {
	cfg, _, err := p.pay2sConfig(ctx)
	if err != nil {
		return err
	}
	if cfg.PartnerCode == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return ErrProviderNotConfigured
	}
	if ref == "" {
		return fmt.Errorf("pay2s: order id is required")
	}
	requestID := pay2sNewRequestID("CANCEL-" + ref)
	signature := pay2sHMACHex(cfg.SecretKey,
		pay2sCancelSignatureRaw(cfg.AccessKey, ref, cfg.PartnerCode, requestID))
	body := pay2sCancelRequest{
		AccessKey:   cfg.AccessKey,
		PartnerCode: cfg.PartnerCode,
		OrderID:     ref,
		RequestID:   requestID,
		RequestType: pay2sRequestTypeCancel,
		Signature:   signature,
	}
	status, data, err := pay2sPostJSON(ctx, pay2sCancelURL(cfg.BaseURL), body)
	if err != nil {
		return err
	}
	var resp pay2sCancelResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("pay2s: decode cancel response: %w", err)
	}
	// Success: HTTP 200 with resultCode 0 (and status "cancelled");
	// cancelling an already-cancelled order returns the same success
	// shape. Errors: {"status":false,...} with HTTP 400/401/404/409/
	// 429/500.
	if status < 400 && resp.ResultCode.s == "0" {
		return nil
	}
	return fmt.Errorf("pay2s: cancel failed: http=%d resultCode=%s status=%q message=%q",
		status, resp.ResultCode.s, resp.Status.s, resp.Message.s)
}

// GetPaymentStatus returns ErrNotSupported: the public Pay2S docs expose
// NO status query for Collection Link payments. The History API
// (https://api.pay2s.vn/userapi/transactions, pay2s-token auth) lists
// bank transactions, not payment-link state; OneQR query is a separate
// product; simulate-payment is demo-only. Correct state arrives via the
// IPN (VerifyWebhook).
func (p *Pay2SProvider) GetPaymentStatus(ctx context.Context, ref string) (*PaymentStatusResult, error) {
	return nil, ErrNotSupported
}

// CapturePayment is an alias of GetPaymentStatus per the contract for VN
// gateways (they settle automatically) — documented, not a stub. Pay2S
// settles at the bank, so this is ErrNotSupported like GetPaymentStatus.
func (p *Pay2SProvider) CapturePayment(ctx context.Context, ref string) (*PaymentStatusResult, error) {
	return p.GetPaymentStatus(ctx, ref)
}

// RefundPayment returns ErrNotSupported: the cancel API only moves
// pending → cancelled and never refunds; Pay2S refunds are performed as
// manual bank transfers outside the API.
func (p *Pay2SProvider) RefundPayment(ctx context.Context, req RefundRequest) (*RefundResult, error) {
	return nil, ErrNotSupported
}
