// zalopay.go — ZaloPay OpenAPI payment provider (plan §25 PaymentProvider).
//
// Specs (verified 2026-10-08; where the pinned batch spec and the docs
// disagreed the docs won — deviations are called out inline and in the
// agent report):
//
//	create:         https://docs.zalopay.vn/vi/docs/specs/order-create
//	order query:    https://docs.zalopay.vn/vi/docs/specs/order-query
//	callback (IPN): https://docs.zalopay.vn/vi/docs/specs/callback-api
//	refund:         https://docs.zalopay.vn/vi/docs/specs/order-refund
//	refund query:   https://docs.zalopay.vn/vi/docs/specs/order-query-refund
//	status codes:   https://docs.zalopay.vn/vi/docs/developer-tools/knowledge-base/status-codes
//
// Money discipline (plan §51): every amount is int64 VND whole dong
// (ISO-4217 exponent 0). No float64 anywhere in this file.
//
// Compile-collision rule: every unexported symbol in this file carries
// the "zalopay" prefix — other gateways (pay2s.go / payos.go /
// gateway_checkout.go) live in this same package.
package payment

import (
	"bytes"
	"context"
	"crypto/hmac"
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

// Limits from the specs (order-create / order-refund field tables).
const (
	zalopayAppUserMaxLen     = 50
	zalopayDescriptionMaxLen = 256
	zalopayEmbedDataMaxLen   = 2048
	zalopayItemMaxLen        = 2048
	zalopayRefundDescMaxLen  = 100
	zalopayAppTransIDMaxLen  = 40
	zalopayMRefundIDMaxLen   = 45

	zalopayExpireMinSecs     = 300
	zalopayExpireMaxSecs     = 2592000
	zalopayExpireDefaultSecs = 900

	// return_code 1 = SUCCESS (knowledge-base/status-codes: 1 SUCCESS,
	// 2 FAIL, 3 PROCESSING).
	zalopayReturnOK = 1

	// callback-api: type 1 = payment.
	zalopayCallbackTypePayment = 1

	// sub_return_code details (status-codes page):
	zalopaySubTimeInvalid      = -54  // query: "Đơn hàng đã hết thời gian thanh toán"
	zalopaySubIDNotFound       = -101 // query: ORDER_NOT_EXIST / query_refund: M_REFUND_ID_NOT_FOUND
	zalopaySubRefundPending    = -1   // query_refund: REFUND_PENDING (chờ phê duyệt)
	zalopaySubRefundProcessing = -16  // query_refund: INSERT_REFUND_LOG_AR_FAIL (đang xử lý)

	zalopayProdBaseURL = "https://openapi.zalopay.vn"
)

// zalopayVNZone is Asia/Ho_Chi_Minh (GMT+7) — the timezone the docs
// require for app_trans_id dates ("yymmdd phải đúng TimeZone Vietnam
// (GMT+7)"). time.FixedZone on purpose: time.LoadLocation needs a tz
// database which is not guaranteed on Windows, and Vietnam has been
// permanently UTC+7 (no DST) since 1975, so the fixed zone is exact.
var zalopayVNZone = time.FixedZone("GMT+7", 7*60*60)

// ZaloPayProvider implements PaymentProvider over the ZaloPay OpenAPI
// (openapi.zalopay.vn / sb-openapi.zalopay.vn). All amounts are int64
// VND whole dong.
//
// Credentials come from a getter resolved at call time (config-in-DB):
// a corrected AppID or a rotated key takes effect without a restart.
type ZaloPayProvider struct {
	zalopayCreds      func(context.Context) (config.ZaloPayConfig, error)
	zalopayHTTPClient *http.Client
	zalopayNow        func() time.Time // test seam — frozen clock
}

// Compile-time proof we honor the shared contract.
var _ PaymentProvider = (*ZaloPayProvider)(nil)

// NewZaloPay builds the provider from config.ZaloPayConfig — a thin
// wrapper over a constant getter, so every boot-configured caller and
// test keeps working unchanged.
func NewZaloPay(cfg config.ZaloPayConfig) *ZaloPayProvider {
	return NewZaloPayDynamic(func(context.Context) (config.ZaloPayConfig, error) { return cfg, nil })
}

// NewZaloPayDynamic builds the provider with credentials read through
// get at call time (e.g. service.ConfigService.ZaloPayGetter).
func NewZaloPayDynamic(get func(context.Context) (config.ZaloPayConfig, error)) *ZaloPayProvider {
	return &ZaloPayProvider{
		zalopayCreds:      get,
		zalopayHTTPClient: &http.Client{Timeout: 15 * time.Second},
		zalopayNow:        time.Now,
	}
}

// zalopayResolved is one call's credentials, parsed the way the API
// wants them. The numeric AppID is parsed per call and any parse
// failure is remembered and surfaced at call time (CreatePayment etc.
// return it) while Enabled() reports false — availability still
// depends on actual integration capability.
type zalopayResolved struct {
	appID       int
	appIDErr    error // AppID parse failure, surfaced at call time
	key1        string
	callbackKey string
	baseURL     string
}

// zalopayResolveConfig parses one config.ZaloPayConfig.
func zalopayResolveConfig(cfg config.ZaloPayConfig) zalopayResolved {
	base := strings.TrimSuffix(strings.TrimSpace(cfg.BaseURL), "/")
	if base == "" {
		base = zalopayProdBaseURL
	}
	r := zalopayResolved{
		key1:        cfg.Key1,
		callbackKey: cfg.CallbackKey,
		baseURL:     base,
	}
	appID, err := strconv.Atoi(strings.TrimSpace(cfg.AppID))
	if err != nil || appID <= 0 {
		r.appIDErr = fmt.Errorf("zalopay: invalid ZALOPAY_APP_ID %q", cfg.AppID)
	} else {
		r.appID = appID
	}
	return r
}

// zalopayResolve reads the credentials in effect right now.
func (z *ZaloPayProvider) zalopayResolve(ctx context.Context) (zalopayResolved, error) {
	cfg := config.ZaloPayConfig{}
	if z != nil && z.zalopayCreds != nil {
		c, err := z.zalopayCreds(ctx)
		if err != nil {
			return zalopayResolved{}, err
		}
		cfg = c
	}
	return zalopayResolveConfig(cfg), nil
}

// zalopayReady is the pre-flight for every outbound call.
func (r zalopayResolved) zalopayReady() error {
	if r.appIDErr != nil {
		return r.appIDErr
	}
	if r.key1 == "" {
		return fmt.Errorf("zalopay: %w: key1 not configured", ErrProviderNotConfigured)
	}
	return nil
}

// zalopayTime is the clock seam, nil-safe for zero-value providers.
func (z *ZaloPayProvider) zalopayTime() time.Time {
	if z != nil && z.zalopayNow != nil {
		return z.zalopayNow()
	}
	return time.Now()
}

// zalopayClient is the HTTP client seam, nil-safe.
func (z *ZaloPayProvider) zalopayClient() *http.Client {
	if z != nil && z.zalopayHTTPClient != nil {
		return z.zalopayHTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// Name returns the stable provider id.
func (z *ZaloPayProvider) Name() string { return ProviderZaloPay }

// Enabled reports whether the credentials are all present: AppID parses
// to a positive int AND Key1 AND CallbackKey are non-empty.
func (z *ZaloPayProvider) Enabled() bool {
	ctx, cancel := cfgsvcCredsCtx()
	defer cancel()
	r, err := z.zalopayResolve(ctx)
	if err != nil {
		return false
	}
	return r.appIDErr == nil && r.appID > 0 &&
		r.key1 != "" && r.callbackKey != ""
}

// ─── MAC helpers (each documents its EXACT hmac_input from the docs) ───

// zalopayHMACHex is hex_lowercase(HMAC-SHA256(key, raw)).
func zalopayHMACHex(key, raw string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(raw))
	return hex.EncodeToString(m.Sum(nil))
}

// zalopayMacCreateInput is the EXACT hmac_input for /v2/create
// (order-create): app_id|app_trans_id|app_user|amount|app_time|embed_data|item
func zalopayMacCreateInput(appID int, appTransID, appUser string, amount, appTime int64, embedData, item string) string {
	return strconv.Itoa(appID) + "|" + appTransID + "|" + appUser + "|" +
		strconv.FormatInt(amount, 10) + "|" + strconv.FormatInt(appTime, 10) + "|" +
		embedData + "|" + item
}

// zalopayMacQueryInput is the EXACT hmac_input for /v2/query
// (order-query): app_id|app_trans_id|mac key — note the docs append the
// MAC KEY ITSELF (Key1) as the third segment.
func zalopayMacQueryInput(appID int, appTransID, key1 string) string {
	return strconv.Itoa(appID) + "|" + appTransID + "|" + key1
}

// zalopayMacRefundInput is the EXACT hmac_input for /v2/refund on the
// FEE-LESS path (order-refund): app_id|zp_trans_id|amount|description|timestamp
// (the with-fee variant inserts refund_fee_amount between amount and
// description — we never send the fee field, so this is the only path).
func zalopayMacRefundInput(appID int, zpTransID string, amount int64, description string, timestamp int64) string {
	return strconv.Itoa(appID) + "|" + zpTransID + "|" +
		strconv.FormatInt(amount, 10) + "|" + description + "|" +
		strconv.FormatInt(timestamp, 10)
}

// zalopayMacQueryRefundInput is the EXACT hmac_input for /v2/query_refund
// (order-query-refund): app_id|m_refund_id|timestamp
func zalopayMacQueryRefundInput(appID int, mRefundID string, timestamp int64) string {
	return strconv.Itoa(appID) + "|" + mRefundID + "|" + strconv.FormatInt(timestamp, 10)
}

// ─── id / field helpers ───

// zalopayClip truncates s to n characters (runes) — the specs state
// lengths as string(N) character limits.
func zalopayClip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// zalopaySuffix derives a deterministic suffix from id: [A-Za-z0-9] is
// kept, any other rune becomes '_', and an empty/over-budget result is
// replaced by a stable SHA-256 fragment ("H"+hex) so distinct long ids
// cannot collide after truncation. Deterministic ⇒ same-VN-day retries
// re-create the same app_trans_id / m_refund_id (idempotent).
func zalopaySuffix(id string, budget int) string {
	if budget <= 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if s := b.String(); s != "" && len(s) <= budget {
		return s
	}
	sum := sha256.Sum256([]byte(id))
	h := hex.EncodeToString(sum[:])
	if budget < 2 {
		return h[:budget]
	}
	return "H" + h[:budget-1]
}

// zalopayAppTransID builds app_trans_id = "yymmdd_suffix" (≤40 chars):
// yymmdd is TODAY IN VIETNAM TIME (Asia/Ho_Chi_Minh, GMT+7) as the docs
// require (reconciliation runs on VN days), the suffix derives
// deterministically from our order id so a retry the same VN day is
// idempotent (ZaloPay then answers sub_return_code -68
// DUPLICATE_APPS_TRANS_ID — surfaced to the caller, which resolves it
// via GetPaymentStatus).
func zalopayAppTransID(orderID string, now time.Time) string {
	date := now.In(zalopayVNZone).Format("060102")
	return date + "_" + zalopaySuffix(orderID, zalopayAppTransIDMaxLen-len(date)-1)
}

// zalopayMRefundID builds m_refund_id = "yymmdd_appid_refundid" (≤45
// chars, order-refund docs).
func zalopayMRefundID(refundID string, appID int, now time.Time) string {
	prefix := now.In(zalopayVNZone).Format("060102") + "_" + strconv.Itoa(appID) + "_"
	budget := zalopayMRefundIDMaxLen - len(prefix)
	if budget < 0 {
		budget = 0
	}
	return prefix + zalopaySuffix(refundID, budget)
}

// zalopayAppUser maps BuyerEmail to app_user (≤50): the local part of
// the address, else the default "hitechcloud" (the docs forbid an empty
// app_user).
func zalopayAppUser(buyerEmail string) string {
	local := ""
	if i := strings.IndexByte(buyerEmail, '@'); i > 0 {
		local = strings.TrimSpace(buyerEmail[:i])
	}
	if local == "" {
		return "hitechcloud"
	}
	return zalopayClip(local, zalopayAppUserMaxLen)
}

// zalopayExpireSeconds derives expire_duration_seconds (allowed range
// 300..2592000): req.ExpiresAt when set (clamped to the API floor/ceiling),
// else the default 900s.
func zalopayExpireSeconds(expiresAt, now time.Time) int64 {
	if expiresAt.IsZero() {
		return zalopayExpireDefaultSecs
	}
	secs := int64(expiresAt.Sub(now) / time.Second)
	if secs < zalopayExpireMinSecs {
		return zalopayExpireMinSecs
	}
	if secs > zalopayExpireMaxSecs {
		return zalopayExpireMaxSecs
	}
	return secs
}

// ─── wire shapes ───

type zalopayEmbedData struct {
	RedirectURL string `json:"redirecturl"`
	OrderID     string `json:"order_id"`
}

type zalopayCreateRequest struct {
	AppID              int    `json:"app_id"`
	AppUser            string `json:"app_user"`
	AppTransID         string `json:"app_trans_id"`
	AppTime            int64  `json:"app_time"`
	ExpireDurationSecs int64  `json:"expire_duration_seconds"`
	Amount             int64  `json:"amount"`
	Description        string `json:"description"`
	CallbackURL        string `json:"callback_url"`
	Item               string `json:"item"`
	EmbedData          string `json:"embed_data"`
	Mac                string `json:"mac"`
	BankCode           string `json:"bank_code"`
}

type zalopayCreateResponse struct {
	ReturnCode       int    `json:"return_code"`
	ReturnMessage    string `json:"return_message"`
	SubReturnCode    int    `json:"sub_return_code"`
	SubReturnMessage string `json:"sub_return_message"`
	ZpTransToken     string `json:"zp_trans_token"`
	OrderToken       string `json:"order_token"`
	OrderURL         string `json:"order_url"`
	QRCode           string `json:"qr_code"`
}

type zalopayQueryRequest struct {
	AppID      int    `json:"app_id"`
	AppTransID string `json:"app_trans_id"`
	Mac        string `json:"mac"`
}

type zalopayQueryResponse struct {
	ReturnCode       int    `json:"return_code"`
	ReturnMessage    string `json:"return_message"`
	SubReturnCode    int    `json:"sub_return_code"`
	SubReturnMessage string `json:"sub_return_message"`
	IsProcessing     bool   `json:"is_processing"`
	Amount           int64  `json:"amount"`
	ZpTransID        int64  `json:"zp_trans_id"`
	ServerTime       int64  `json:"server_time"`
	DiscountAmount   int64  `json:"discount_amount"`
	// Status is the CLASSIC orderquery enum (-1/1/2/3). The current
	// /v2/query schema does NOT document a "status" field (it documents
	// is_processing + return_code) — we read it defensively when present.
	Status *int `json:"status"`
}

type zalopayRefundRequest struct {
	AppID       int    `json:"app_id"`
	MRefundID   string `json:"m_refund_id"`
	ZpTransID   string `json:"zp_trans_id"` // docs: string
	Amount      int64  `json:"amount"`
	Timestamp   int64  `json:"timestamp"`
	Description string `json:"description"`
	Mac         string `json:"mac"`
	// refund_fee_amount is deliberately absent — fee-less mac path pinned.
}

type zalopayRefundResponse struct {
	ReturnCode       int    `json:"return_code"`
	ReturnMessage    string `json:"return_message"`
	SubReturnCode    int    `json:"sub_return_code"`
	SubReturnMessage string `json:"sub_return_message"`
	RefundID         int64  `json:"refund_id"`
}

type zalopayQueryRefundRequest struct {
	AppID     int    `json:"app_id"`
	MRefundID string `json:"m_refund_id"`
	Timestamp int64  `json:"timestamp"`
	Mac       string `json:"mac"`
}

type zalopayQueryRefundResponse struct {
	ReturnCode       int    `json:"return_code"`
	ReturnMessage    string `json:"return_message"`
	SubReturnCode    int    `json:"sub_return_code"`
	SubReturnMessage string `json:"sub_return_message"`
}

type zalopayCallbackEnvelope struct {
	Data string `json:"data"`
	Mac  string `json:"mac"`
	Type int    `json:"type"`
}

type zalopayCallbackData struct {
	AppID          int64  `json:"app_id"`
	AppTransID     string `json:"app_trans_id"`
	AppTime        int64  `json:"app_time"`
	AppUser        string `json:"app_user"`
	Amount         int64  `json:"amount"`
	EmbedData      string `json:"embed_data"` // JSON string
	Item           string `json:"item"`       // JSON array string
	ZpTransID      int64  `json:"zp_trans_id"`
	ServerTime     int64  `json:"server_time"` // unix ms
	Channel        int64  `json:"channel"`
	MerchantUserID string `json:"merchant_user_id"`
}

// zalopayPostJSON POSTs a JSON body and decodes the JSON answer.
func zalopayPostJSON(ctx context.Context, cli *http.Client, url string, body, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("zalopay: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("zalopay: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := cli.Do(req)
	if err != nil {
		return fmt.Errorf("zalopay: POST %s: %w", url, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("zalopay: POST %s: read response: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("zalopay: POST %s: http %d: %s", url, resp.StatusCode, zalopayClip(string(raw), 200))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("zalopay: POST %s: decode response: %w", url, err)
	}
	return nil
}

// ─── PaymentProvider implementation ───

// CreatePayment starts a one-off payment via POST /v2/create.
func (z *ZaloPayProvider) CreatePayment(ctx context.Context, req CreatePaymentRequest) (*CreatePaymentResult, error) {
	r, err := z.zalopayResolve(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.zalopayReady(); err != nil {
		return nil, err
	}
	if req.Currency != "" && !strings.EqualFold(req.Currency, "VND") {
		return nil, ErrCurrencyNotSupported
	}
	if req.AmountMinor <= 0 {
		return nil, fmt.Errorf("zalopay: amount must be > 0, got %d", req.AmountMinor)
	}
	if req.OrderID == "" {
		return nil, fmt.Errorf("zalopay: order id is required")
	}

	now := z.zalopayTime()
	appTime := now.UnixMilli()
	expire := zalopayExpireSeconds(req.ExpiresAt, now)
	appTransID := zalopayAppTransID(req.OrderID, now)

	embedBytes, err := json.Marshal(zalopayEmbedData{RedirectURL: req.ReturnURL, OrderID: req.OrderID})
	if err != nil {
		return nil, fmt.Errorf("zalopay: encode embed_data: %w", err)
	}
	embedStr := string(embedBytes)
	if len(embedStr) > zalopayEmbedDataMaxLen {
		return nil, fmt.Errorf("zalopay: embed_data longer than %d bytes", zalopayEmbedDataMaxLen)
	}
	item := "[]" // order-create: 'Sử dụng "[]" nếu rỗng'; the description field carries the line-item text

	body := zalopayCreateRequest{
		AppID:              r.appID,
		AppUser:            zalopayAppUser(req.BuyerEmail),
		AppTransID:         appTransID,
		AppTime:            appTime,
		ExpireDurationSecs: expire,
		Amount:             req.AmountMinor,
		Description:        zalopayClip(req.Description, zalopayDescriptionMaxLen),
		CallbackURL:        req.Extra["webhook_url"],
		Item:               item,
		EmbedData:          embedStr,
		BankCode:           "",
	}
	body.Mac = zalopayHMACHex(r.key1,
		zalopayMacCreateInput(body.AppID, body.AppTransID, body.AppUser, body.Amount, body.AppTime, body.EmbedData, body.Item))

	var resp zalopayCreateResponse
	if err := zalopayPostJSON(ctx, z.zalopayClient(), r.baseURL+"/v2/create", &body, &resp); err != nil {
		return nil, err
	}
	if resp.ReturnCode != zalopayReturnOK {
		return nil, fmt.Errorf("zalopay: create order failed: return_code=%d (%s) sub_return_code=%d (%s)",
			resp.ReturnCode, resp.ReturnMessage, resp.SubReturnCode, resp.SubReturnMessage)
	}
	return &CreatePaymentResult{
		ProviderRef: appTransID,
		PayURL:      resp.OrderURL,
		QRCode:      resp.QRCode,
		ExpiresAt:   time.UnixMilli(appTime).Add(time.Duration(expire) * time.Second),
	}, nil
}

// CapturePayment — ZaloPay settles automatically at payment time, so
// this is the documented alias of GetPaymentStatus (provider.go contract:
// "VN gateways settle automatically, so their implementations alias
// GetPaymentStatus (documented, not a stub)").
func (z *ZaloPayProvider) CapturePayment(ctx context.Context, ref string) (*PaymentStatusResult, error) {
	return z.GetPaymentStatus(ctx, ref)
}

// GetPaymentStatus queries POST /v2/query for the authoritative state.
func (z *ZaloPayProvider) GetPaymentStatus(ctx context.Context, ref string) (*PaymentStatusResult, error) {
	r, err := z.zalopayResolve(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.zalopayReady(); err != nil {
		return nil, err
	}
	body := zalopayQueryRequest{
		AppID:      r.appID,
		AppTransID: ref,
		Mac:        zalopayHMACHex(r.key1, zalopayMacQueryInput(r.appID, ref, r.key1)),
	}
	var resp zalopayQueryResponse
	if err := zalopayPostJSON(ctx, z.zalopayClient(), r.baseURL+"/v2/query", &body, &resp); err != nil {
		return nil, err
	}
	status, err := zalopayMapQueryStatus(&resp)
	if err != nil {
		return nil, err
	}
	res := &PaymentStatusResult{
		ProviderRef: ref,
		Status:      status,
		AmountMinor: resp.Amount,
		Currency:    "VND",
		Raw:         zalopayQueryRaw(&resp),
	}
	if resp.ZpTransID != 0 {
		res.TransID = strconv.FormatInt(resp.ZpTransID, 10)
	}
	if status == StatusSucceeded && resp.ServerTime > 0 {
		res.PaidAt = time.UnixMilli(resp.ServerTime)
	}
	return res, nil
}

// zalopayMapQueryStatus normalizes the /v2/query answer.
//
// Docs-verified path (current order-query schema — there is NO "status"
// field; state comes from return_code 1 SUCCESS / 2 FAIL / 3 PROCESSING
// per knowledge-base/status-codes, plus is_processing and sub_return_code
// -54 TIME_INVALID = "Đơn hàng đã hết thời gian thanh toán").
//
// Defensive legacy path (classic orderquery "status" enum, only when the
// field is present): -1 = "Đơn hàng đã bị hủy hoặc giao dịch không thành
// công" → StatusFailed (ZaloPay has no cancel API, so the merged
// cancelled/failed wording is normalized as failed), 1 = pending/processing
// → StatusPending, 2 = success → StatusSucceeded, 3 = "đã được hoàn tiền"
// → StatusRefunded. Unknown values fall back to StatusPending — never mark
// paid on an unknown state.
func zalopayMapQueryStatus(resp *zalopayQueryResponse) (PaymentStatus, error) {
	if resp.Status != nil {
		switch *resp.Status {
		case -1:
			return StatusFailed, nil
		case 1:
			return StatusPending, nil
		case 2:
			return StatusSucceeded, nil
		case 3:
			return StatusRefunded, nil
		default:
			return StatusPending, nil
		}
	}
	switch {
	case resp.IsProcessing || resp.ReturnCode == 3:
		return StatusPending, nil
	case resp.ReturnCode == 1:
		return StatusSucceeded, nil
	case resp.ReturnCode == 2 && resp.SubReturnCode == zalopaySubTimeInvalid:
		return StatusExpired, nil
	case resp.ReturnCode == 2 && resp.SubReturnCode == zalopaySubIDNotFound:
		return "", fmt.Errorf("zalopay: query: ORDER_NOT_EXIST (sub_return_code=%d) for an order we created", resp.SubReturnCode)
	case resp.ReturnCode == 2:
		return StatusFailed, nil
	default:
		return "", fmt.Errorf("zalopay: query: unhandled response return_code=%d sub_return_code=%d",
			resp.ReturnCode, resp.SubReturnCode)
	}
}

// zalopayQueryRaw keeps the provider payload for the audit trail —
// typed fields only, no secrets, int64-exact.
func zalopayQueryRaw(resp *zalopayQueryResponse) map[string]any {
	raw := map[string]any{
		"return_code":     resp.ReturnCode,
		"sub_return_code": resp.SubReturnCode,
		"is_processing":   resp.IsProcessing,
		"amount":          resp.Amount,
		"zp_trans_id":     resp.ZpTransID,
		"server_time":     resp.ServerTime,
		"discount_amount": resp.DiscountAmount,
	}
	if resp.Status != nil {
		raw["status"] = *resp.Status
	}
	return raw
}

// RefundPayment requests a refund via POST /v2/refund.
//
// The fee-less path is pinned: refund_fee_amount is omitted and the mac
// input is app_id|zp_trans_id|amount|description|timestamp.
//
// ASYNC — the docs are explicit ("API Refund chỉ xác nhận yêu cầu hoàn
// tiền đã được tiếp nhận. Merchant không nên sử dụng response của API
// này để kết luận giao dịch đã hoàn tiền thành công"), so the result is
// ALWAYS StatusPending; QueryRefund carries the final state.
func (z *ZaloPayProvider) RefundPayment(ctx context.Context, req RefundRequest) (*RefundResult, error) {
	r, err := z.zalopayResolve(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.zalopayReady(); err != nil {
		return nil, err
	}
	if req.AmountMinor <= 0 {
		return nil, fmt.Errorf("zalopay: refund amount must be > 0, got %d", req.AmountMinor)
	}
	if strings.TrimSpace(req.TransID) == "" {
		return nil, fmt.Errorf("zalopay: refund requires TransID (the ZaloPay zp_trans_id)")
	}
	now := z.zalopayTime()
	desc := zalopayClip(strings.TrimSpace(req.Reason), zalopayRefundDescMaxLen)
	if desc == "" {
		desc = "Hoan tien"
	}
	body := zalopayRefundRequest{
		AppID:       r.appID,
		MRefundID:   zalopayMRefundID(req.RefundID, r.appID, now),
		ZpTransID:   req.TransID,
		Amount:      req.AmountMinor,
		Timestamp:   now.UnixMilli(),
		Description: desc,
	}
	body.Mac = zalopayHMACHex(r.key1,
		zalopayMacRefundInput(body.AppID, body.ZpTransID, body.Amount, body.Description, body.Timestamp))

	var resp zalopayRefundResponse
	if err := zalopayPostJSON(ctx, z.zalopayClient(), r.baseURL+"/v2/refund", &body, &resp); err != nil {
		return nil, err
	}
	if resp.ReturnCode != zalopayReturnOK {
		return nil, fmt.Errorf("zalopay: refund failed: return_code=%d (%s) sub_return_code=%d (%s)",
			resp.ReturnCode, resp.ReturnMessage, resp.SubReturnCode, resp.SubReturnMessage)
	}
	return &RefundResult{
		RefundRef: strconv.FormatInt(resp.RefundID, 10),
		Status:    StatusPending,
		Raw: map[string]any{
			"return_code":     resp.ReturnCode,
			"sub_return_code": resp.SubReturnCode,
			"refund_id":       resp.RefundID,
		},
	}, nil
}

// QueryRefund asks POST /v2/query_refund for the final state of a refund
// requested with mRefundID (mac per order-query-refund:
// app_id|m_refund_id|timestamp). Exported for the admin refund flow:
// RefundPayment only acknowledges the request; this is where the final
// succeeded/failed state comes from.
func (z *ZaloPayProvider) QueryRefund(ctx context.Context, mRefundID string) (*RefundResult, error) {
	r, err := z.zalopayResolve(ctx)
	if err != nil {
		return nil, err
	}
	if err := r.zalopayReady(); err != nil {
		return nil, err
	}
	body := zalopayQueryRefundRequest{
		AppID:     r.appID,
		MRefundID: mRefundID,
		Timestamp: z.zalopayTime().UnixMilli(),
	}
	body.Mac = zalopayHMACHex(r.key1, zalopayMacQueryRefundInput(body.AppID, body.MRefundID, body.Timestamp))

	var resp zalopayQueryRefundResponse
	if err := zalopayPostJSON(ctx, z.zalopayClient(), r.baseURL+"/v2/query_refund", &body, &resp); err != nil {
		return nil, err
	}
	if resp.SubReturnCode == zalopaySubIDNotFound {
		return nil, fmt.Errorf("zalopay: query_refund: M_REFUND_ID_NOT_FOUND for %q", mRefundID)
	}
	return &RefundResult{
		RefundRef: mRefundID,
		Status:    zalopayMapQueryRefundStatus(resp.SubReturnCode),
		Raw: map[string]any{
			"return_code":        resp.ReturnCode,
			"sub_return_code":    resp.SubReturnCode,
			"return_message":     resp.ReturnMessage,
			"sub_return_message": resp.SubReturnMessage,
		},
	}, nil
}

// zalopayMapQueryRefundStatus maps the query_refund sub_return_code
// (status-codes page): 1 = done (the page lists only the failure codes
// plus -1/-16/0; ZaloPay uses sub 1 for success across its APIs),
// -1 = REFUND_PENDING, -16 = refund still processing, anything else is a
// failed refund.
func zalopayMapQueryRefundStatus(subReturnCode int) PaymentStatus {
	switch subReturnCode {
	case 1:
		return StatusSucceeded
	case zalopaySubRefundPending, zalopaySubRefundProcessing:
		return StatusPending
	default:
		return StatusFailed
	}
}

// VoidPayment — ZaloPay publishes NO cancel/hủy-đơn-hàng API: the
// "Đơn hàng" spec group (https://docs.zalopay.vn/vi/docs/specs/order)
// lists only "Tạo đơn hàng" (order-create) and "Truy vấn trạng thái đơn
// hàng" (order-query). Unpaid orders die on their own through
// expire_duration_seconds (900s by default here), so there is nothing to
// cancel. Return ErrNotSupported — never a fake success (provider.go
// contract).
func (z *ZaloPayProvider) VoidPayment(ctx context.Context, ref, reason string) error {
	return ErrNotSupported
}

// zalopayParseCallbackData parses the signed "data" JSON string and
// pulls OUR order id out of its embed_data.
func zalopayParseCallbackData(data string) (*zalopayCallbackData, string, error) {
	var d zalopayCallbackData
	if err := json.Unmarshal([]byte(data), &d); err != nil {
		return nil, "", fmt.Errorf("%w: data: %v", ErrWebhookPayloadMalformed, err)
	}
	var embed struct {
		OrderID string `json:"order_id"`
	}
	if err := json.Unmarshal([]byte(d.EmbedData), &embed); err != nil {
		return nil, "", fmt.Errorf("%w: embed_data: %v", ErrWebhookPayloadMalformed, err)
	}
	if d.AppTransID == "" || embed.OrderID == "" {
		return nil, "", fmt.Errorf("%w: missing app_trans_id / embed_data.order_id", ErrWebhookPayloadMalformed)
	}
	return &d, embed.OrderID, nil
}

// VerifyWebhook authenticates a ZaloPay callback (callback-api):
// {"data": "<json string>", "mac": "<hex>", "type": 1}.
//
// mac = hex_lowercase(HMAC-SHA256(callbackKey, data)) over the RAW data
// string EXACTLY as received (not re-serialized). The check runs FIRST,
// constant-time (crypto/hmac.Equal), before any field is trusted, and
// fails closed. type must be 1 (payment).
func (z *ZaloPayProvider) VerifyWebhook(payload []byte) (*WebhookEvent, error) {
	credsCtx, cancel := cfgsvcCredsCtx()
	defer cancel()
	r, err := z.zalopayResolve(credsCtx)
	if err != nil {
		return nil, err
	}
	if r.callbackKey == "" {
		return nil, fmt.Errorf("zalopay: %w: callback key not configured", ErrProviderNotConfigured)
	}
	var env zalopayCallbackEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrWebhookPayloadMalformed, err)
	}
	want := zalopayHMACHex(r.callbackKey, env.Data)
	if !hmac.Equal([]byte(strings.ToLower(env.Mac)), []byte(want)) {
		return nil, ErrWebhookSignatureInvalid
	}
	if env.Type != zalopayCallbackTypePayment {
		return nil, fmt.Errorf("%w: callback type %d is not a payment callback", ErrWebhookPayloadMalformed, env.Type)
	}
	d, orderID, err := zalopayParseCallbackData(env.Data)
	if err != nil {
		return nil, err
	}
	ev := &WebhookEvent{
		Provider: ProviderZaloPay,
		OrderID:  orderID,
		// ProviderRef is the gateway handle stored at CreatePayment time.
		ProviderRef: d.AppTransID,
		TransID:     strconv.FormatInt(d.ZpTransID, 10),
		// The callback only fires after money moved (callback-api: "Khi và
		// chỉ khi Zalopay nhận tín hiệu khách hàng thành công thì mới thông
		// báo kết quả").
		Status:      StatusSucceeded,
		AmountMinor: d.Amount,
		Currency:    "VND",
		Raw: map[string]any{
			"app_id":           d.AppID,
			"app_trans_id":     d.AppTransID,
			"app_time":         d.AppTime,
			"app_user":         d.AppUser,
			"amount":           d.Amount,
			"embed_data":       d.EmbedData,
			"item":             d.Item,
			"zp_trans_id":      d.ZpTransID,
			"server_time":      d.ServerTime,
			"channel":          d.Channel,
			"merchant_user_id": d.MerchantUserID,
		},
	}
	if d.ServerTime > 0 {
		ev.PaidAt = time.UnixMilli(d.ServerTime)
	}
	return ev, nil
}
