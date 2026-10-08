// Admin refund, metrics and retention endpoints (plan §79, §42, §92).
//
//	POST /admin/orders/:id/refund      Refund an order (full/partial/manual)
//	GET  /admin/orders/:id/refunds     The order's refund ledger
//	GET  /admin/metrics/mrr            MRR/ARR at an instant
//	GET  /admin/metrics/mrr-series     MRR/ARR series (day|week|month)
//	POST /admin/retention/run          Run one data-retention pass
//
// Route guards are the route's business (cmd/server/main.go wires
// middleware.RequirePermission): model.PermOrdersRefund for the
// refund pair, model.PermReportsRead for the metrics pair,
// model.PermSettingsManage for the retention run.
//
// Every amount is int64 minor units — this handler never speaks in
// floating point. Error codes are the pinned ones and each maps to
// exactly one HTTP status (pkg/response/contract_test.go):
//
//	ORDER_NOT_FOUND        404   unknown order
//	ORDER_NOT_REFUNDABLE   409   not paid / not partially refunded
//	REFUND_AMOUNT_INVALID  400   amount ≤ 0
//	REFUND_EXCEEDS_PAID    400   more than what is left
//	PAYMENT_GATEWAY_ERROR  502   the gateway refused or failed
//	IDEMPOTENCY_KEY_REUSED     409  (key reuse, same as the middleware)
//	IDEMPOTENCY_IN_PROGRESS    409  (key still in flight)
package handler

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/payment"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// refundsAdminStore is the slice of store.Store the refund endpoints
// need: everything the refund flow touches (payment.RefundStore) plus
// the refund list. The real constructor takes *store.Store; tests
// substitute a fake so every refusal path runs without a database.
type refundsAdminStore interface {
	payment.RefundStore
	ListRefundsByOrder(ctx context.Context, orderID string, sort store.Sort) ([]*model.Refund, error)
}

var _ refundsAdminStore = (*store.Store)(nil)

// RefundsAdminHandler exposes the §79 refund endpoints.
type RefundsAdminHandler struct {
	Store refundsAdminStore
	// Webhook dispatches order.refunded when a refund lands (plan
	// §35). Optional and nil-guarded at the call site, the same
	// pattern as AdminHandler.Webhook and OrderAdminHandler.Webhook.
	Webhook *service.WebhookService
}

// NewRefundsAdminHandler wires the handler to the store.
func NewRefundsAdminHandler(s *store.Store) *RefundsAdminHandler {
	return &RefundsAdminHandler{Store: s}
}

// SetWebhook wires the merchant webhook dispatcher into this handler.
func (h *RefundsAdminHandler) SetWebhook(w *service.WebhookService) {
	h.Webhook = w
}

// Refund answers POST /admin/orders/:id/refund.
//
// Body: {amount_minor?: int64, reason: string}. amount_minor omitted
// means the FULL REMAINDER (total − already refunded); an explicit
// amount must be > 0 and within what is left. The refund is issued
// through payment.RefundOrderKey — gateway dispatch, ledger row,
// fulfilment effects and idempotency included. An Idempotency-Key
// header pins the request: the same key never refunds twice (and the
// same amount+reason replays even without one).
//
// Answers {refund: {...}, order_status} — the refund row as recorded
// and the order's status after it.
func (h *RefundsAdminHandler) Refund(c *gin.Context) {
	id := c.Param("id")
	var req struct {
		AmountMinor *int64 `json:"amount_minor"`
		Reason      string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "a JSON body with a reason is required")
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		response.BadRequest(c, "reason is required")
		return
	}
	order, err := h.Store.FindOrderByID(c, id)
	if err != nil || order == nil {
		response.Err(c, 404, "ORDER_NOT_FOUND", "order not found")
		return
	}

	// "Omit = full remaining" is resolved here so the flow's
	// validation stays the same three rules for every caller.
	amount := int64(0)
	if req.AmountMinor != nil {
		amount = *req.AmountMinor
	} else {
		committed, _, err := h.Store.RefundSumsByOrder(c, order.ID)
		if err != nil {
			response.Internal(c, err)
			return
		}
		remaining := order.TotalMinor - committed
		if remaining <= 0 {
			response.Err(c, 409, "ORDER_NOT_REFUNDABLE", "nothing left to refund on this order")
			return
		}
		amount = remaining
	}

	idemKey := strings.TrimSpace(c.GetHeader("Idempotency-Key"))
	if idemKey != "" && !store.ValidIdempotencyKey(idemKey) {
		response.Err(c, 400, "INVALID_IDEMPOTENCY_KEY", "invalid Idempotency-Key header")
		return
	}

	row, err := payment.RefundOrderKey(c, h.Store, order.ID, amount, req.Reason, adminID(c), idemKey)
	if err != nil {
		writeAppErr(c, err)
		return
	}
	status := ""
	if after, err := h.Store.FindOrderByID(c, order.ID); err == nil && after != nil {
		status = after.Status
	}
	// order.refunded (plan §35) — but only once the order has
	// actually reached refunded. A partial refund leaves it paid, and
	// this plan's vocabulary has no partial-refund event (deferred,
	// see the report). The amount is the refund row's, not the
	// order's, so a receiver can tell a part from the whole.
	// Best-effort: the money has already gone back.
	if h.Webhook != nil && status == model.OrderStatusRefunded {
		if pid := orderProductID(order); pid != "" {
			h.Webhook.Dispatch(c, pid, model.EventOrderRefunded, map[string]any{
				"order_id": order.ID, "order_number": order.OrderNumber,
				"refund_id": row.ID, "amount_minor": row.AmountMinor,
				"currency": order.Currency, "reason": row.Reason,
			})
		}
	}
	response.OK(c, gin.H{"refund": row, "order_status": status})
}

// refundSortColumns is what ?sort= accepts on the refund ledger: the
// columns the table shows, qualified with the bun model alias
// ("refund"). "amount" is the amount_minor column. This orders the
// READ — the ledger's own append order is immutable (see
// refunds.go); an unknown key keeps the default, newest first
// (listSortOrDefault).
var refundSortColumns = map[string]sortCol{
	"created_at": {Expr: "refund.created_at", Desc: true},
	"amount":     {Expr: "refund.amount_minor", Desc: true},
	"status":     {Expr: "refund.status"},
	"provider":   {Expr: "refund.payment_provider"},
}

// ListRefunds answers GET /admin/orders/:id/refunds — the order's
// whole refund ledger, newest first by default (pending and failed
// rows are history too and are never filtered out; ?sort= may read it
// in another order).
func (h *RefundsAdminHandler) ListRefunds(c *gin.Context) {
	id := c.Param("id")
	order, err := h.Store.FindOrderByID(c, id)
	if err != nil || order == nil {
		response.Err(c, 404, "ORDER_NOT_FOUND", "order not found")
		return
	}
	sortOrder := listSortOrDefault(c, refundSortColumns, "created_at")
	rows, err := h.Store.ListRefundsByOrder(c, order.ID, sortOrder)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, gin.H{"refunds": response.Array(rows)})
}

// ─── MRR / ARR metrics (plan §42) ───

// metricsStore is the slice of store.Store the metrics endpoints
// need. Tests substitute a fake.
type metricsStore interface {
	MRR(ctx context.Context, at time.Time) (*store.MRRSnapshot, error)
	MRRSeries(ctx context.Context, from, to time.Time, grain string) ([]store.MRRSeriesPoint, error)
}

var _ metricsStore = (*store.Store)(nil)

// AdminMetricsHandler exposes the §42 MRR/ARR endpoints.
//
//	GET /admin/metrics/mrr?at=YYYY-MM-DD        (default: now)
//	GET /admin/metrics/mrr-series?from=&to=&grain=day|week|month
//
// Both answer {mrr_minor, arr_minor, currency, series:[{ts,
// mrr_minor, arr_minor}]} — the instant endpoint with an empty
// series, the series endpoint with one point per bucket (and its
// totals left zero). Money is int64 minor units.
type AdminMetricsHandler struct {
	Store metricsStore
}

// NewAdminMetricsHandler wires the handler to the store.
func NewAdminMetricsHandler(s *store.Store) *AdminMetricsHandler {
	return &AdminMetricsHandler{Store: s}
}

// MRR answers GET /admin/metrics/mrr — MRR and ARR (MRR × 12) at an
// instant. ?at= takes a UTC day (YYYY-MM-DD) or an RFC3339 stamp;
// omitted means now.
func (h *AdminMetricsHandler) MRR(c *gin.Context) {
	at := time.Now()
	if raw := strings.TrimSpace(c.Query("at")); raw != "" {
		parsed, ok := mrrDateParam(raw)
		if !ok {
			response.BadRequest(c, "at must be YYYY-MM-DD or RFC3339")
			return
		}
		at = parsed
	}
	snap, err := h.Store.MRR(c, at)
	if err != nil {
		response.Internal(c, err)
		return
	}
	response.OK(c, gin.H{
		"mrr_minor": snap.MRRMinor,
		"arr_minor": snap.ARRMinor,
		"currency":  snap.Currency,
		"series":    []store.MRRSeriesPoint{},
	})
}

// Series answers GET /admin/metrics/mrr-series?from&to&grain= — the
// MRR at each bucket start over the window. The window rules are the
// report ones: from ≤ to as inclusive UTC days, span ≤ 3 years, and
// no gap-fill.
func (h *AdminMetricsHandler) Series(c *gin.Context) {
	from, ok := reportDateParam(c, "from")
	if !ok {
		return
	}
	to, ok := reportDateParam(c, "to")
	if !ok {
		return
	}
	grain := strings.TrimSpace(c.Query("grain"))
	if grain == "" {
		grain = store.ReportGroupByDay
	}
	if !store.ValidReportGroupBy(grain) {
		response.BadRequest(c, "grain must be day, week, or month")
		return
	}
	if from.After(to) {
		response.BadRequest(c, "from must be on or before to")
		return
	}
	if to.After(from.AddDate(store.ReportMaxSpanYears, 0, 0)) {
		response.BadRequest(c, "mrr series range must not exceed "+strconv.Itoa(store.ReportMaxSpanYears)+" years")
		return
	}
	series, err := h.Store.MRRSeries(c, from, to, grain)
	if err != nil {
		// The store re-checks the window; a validation refusal is a
		// 400, anything else is ours.
		if errors.Is(err, store.ErrInvalidGroupBy) || errors.Is(err, store.ErrInvalidReportRange) {
			response.BadRequest(c, "invalid mrr series parameters")
			return
		}
		response.Internal(c, err)
		return
	}
	response.OK(c, gin.H{
		"from":   from.Format("2006-01-02"),
		"to":     to.Format("2006-01-02"),
		"grain":  grain,
		"series": response.Array(series),
	})
}

// mrrDateParam parses one query parameter as a UTC day (YYYY-MM-DD)
// or an RFC3339 instant. (reportDateParam requires presence; ?at= is
// optional, hence this sibling — prefix `mrr`.)
func mrrDateParam(raw string) (time.Time, bool) {
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// ─── Data retention (plan §92) ───

// retentionStore is the slice of store.Store a retention pass runs
// against (the service layer owns the pass; this is only its typing
// at the seam).
type retentionStore interface {
	GetSetting(ctx context.Context, key string) (string, error)
	DeleteNotificationsBefore(ctx context.Context, before time.Time, batch int) (int64, error)
	DeleteProcessedEventsBefore(ctx context.Context, before time.Time, batch int) (int64, error)
	DeleteWebhookDeliveriesBefore(ctx context.Context, before time.Time, batch int) (int64, error)
	DeleteAuditLogsBefore(ctx context.Context, before time.Time, batch int) (int64, error)
}

var _ retentionStore = (*store.Store)(nil)

// AdminRetentionHandler exposes the §92 manual retention run.
type AdminRetentionHandler struct {
	Store retentionStore
	Audit interface {
		Audit(ctx context.Context, log *model.AuditLog)
	}
}

// NewRetentionAdminHandler wires the handler to the store.
func NewRetentionAdminHandler(s *store.Store) *AdminRetentionHandler {
	return &AdminRetentionHandler{Store: s, Audit: s}
}

// Run answers POST /admin/retention/run — one idempotent pass over
// the four operational tables (never financial records), answering
// {deleted: {notifications, processed_events, webhook_deliveries,
// audit_logs}}.
func (h *AdminRetentionHandler) Run(c *gin.Context) {
	res, err := service.RunRetention(c, h.Store)
	if err != nil {
		response.Internal(c, err)
		return
	}
	if h.Audit != nil {
		h.Audit.Audit(c, &model.AuditLog{
			Entity: "settings", EntityID: "retention", Action: "retention_run",
			ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
			Changes: map[string]any{"deleted": res.Deleted},
		})
	}
	response.OK(c, gin.H{"deleted": res.Deleted})
}
