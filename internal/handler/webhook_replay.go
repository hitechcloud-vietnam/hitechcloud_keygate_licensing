package handler

// §35 tail: delivery replay + merchant signing-secret rotation.
//
//	POST /api/v1/admin/webhooks/:id/deliveries/:delivery_id/replay
//	POST /api/v1/admin/webhooks/:id/rotate
//
// (the customer-side twin of rotation lives in portal_webhooks.go:
// POST /api/v1/portal/webhooks/:id/rotate)
//
// REPLAY re-dispatches one delivery's stored payload through the SAME
// signing shape a fresh delivery uses — the one HMAC-SHA256 hex scheme
// this system signs everything with (X-HiTechCloud-Event,
// X-HiTechCloud-Signature: sha256=<hex>, X-HiTechCloud-Delivery), the
// same json.Marshal(delivery.Payload) body (byte-identical to the
// original dispatch, so a receiver's idempotency keys keep matching),
// the same insert-first row lifecycle (in-flight hold-off, first attempt
// now, failures fall into the standard retry loop with the 30s·2^n
// backoff), and the CURRENT secret — every read path opens what is
// stored, and a replay must verify like any other delivery, never with a
// frozen historic secret.
//
// One deliberate addition: X-HiTechCloud-Replay: true marks a replay so
// a receiver can tell it apart. It is sent on the FIRST attempt of the
// replay row; if that attempt fails and the standard retry loop re-sends
// the row later, the retried sends carry the same delivery id and the
// same body but NOT the marker (the loop is shared machinery and signs
// only body+secret). Receivers wanting a per-attempt marker must use
// the delivery id — documented trade-off.
//
// ROTATION replaces the merchant signing secret and returns it ONCE.
// Immediate invalidation (chosen): the signing scheme carries one
// secret per delivery and every read path opens only what is stored, so
// there is no dual-verify/dual-sign window to implement — the old
// secret stops signing the moment the write commits. Single documented
// edge: a delivery already in flight when the write commits was signed
// with the secret it read before rotation and still verifies with that
// one until it finishes.
//
// Target policy is enforced again at dispatch (the URL may have been
// saved before a policy tightening, and DNS moves): write-time literal
// check, the strict literal classification shared with the portal path
// (CGNAT/metadata/transition ranges included), the resolver-based check,
// and WebhookService.ValidateTarget on top. The delivery client follows
// NO redirects — a 3xx is the receiver's answer.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/middleware"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/service"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

const (
	// webhookReplayMarkerHeader marks a dispatch as a REPLAY of an
	// earlier delivery. Documented contract: "true" on the first attempt
	// of a replay row (see the file header for the retry caveat).
	webhookReplayMarkerHeader = "X-HiTechCloud-Replay"
	webhookReplayMarkerValue  = "true"

	// webhookDeliveryTimeout mirrors main's webhookHTTPTimeout default.
	webhookDeliveryTimeout = 10 * time.Second
	// webhookReplayInflightHoldOff mirrors service's inflight hold-off
	// (2·httpTimeout + 30s) so a replay and the retry loop never double-
	// send the same window.
	webhookReplayInflightHoldOff = 50 * time.Second
	// webhookReplayRetryUnit mirrors service's scheduleRetry backoff
	// unit: 30s · 2^attempts.
	webhookReplayRetryUnit = 30 * time.Second
	// webhookReplayMaxResponseBody mirrors deliver()'s response bound.
	webhookReplayMaxResponseBody = 4096
)

// webhookReplayStore is the seam the replay/rotate surface needs. The
// production type is *store.Store (asserted below); tests fake it and
// run DB-free.
type webhookReplayStore interface {
	FindWebhookByID(ctx context.Context, id string) (*model.Webhook, error)
	FindWebhookDeliveryByID(ctx context.Context, id string) (*model.WebhookDelivery, error)
	CreateWebhookReplayDelivery(ctx context.Context, d *model.WebhookReplayDelivery) error
	UpdateWebhookDelivery(ctx context.Context, d *model.WebhookDelivery) error
	UpdateWebhook(ctx context.Context, w *model.Webhook) error
	Audit(ctx context.Context, log *model.AuditLog)
}

var _ webhookReplayStore = (*store.Store)(nil)

// webhookReplayDoer is the delivery transport seam.
type webhookReplayDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// WebhookReplayHandler serves the admin replay + rotate endpoints.
type WebhookReplayHandler struct {
	store          webhookReplayStore
	client         webhookReplayDoer
	validateTarget func(string) error
	newSecret      func() string
	lookup         func(ctx context.Context, host string) ([]net.IPAddr, error)
}

// NewWebhookReplayHandler wires replay + rotation to the store and the
// merchant webhook service. svc supplies the write-time target policy
// (WebhookService.ValidateTarget) and the merchant secret format
// (service.GenerateWebhookSecret — 64 lowercase hex chars); everything
// else is the store. The delivery client gets the same timeout as a
// fresh delivery and follows no redirects.
//
// Lead wiring (routes to add beside the existing webhook admin routes):
//
//	admin.POST("/webhooks/:id/deliveries/:delivery_id/replay", replayH.ReplayDelivery)
//	admin.POST("/webhooks/:id/rotate", replayH.RotateSecret)
//	portal.POST("/webhooks/:id/rotate", portalWebhookH.Rotate)
func NewWebhookReplayHandler(s webhookReplayStore, svc *service.WebhookService) *WebhookReplayHandler {
	return &WebhookReplayHandler{
		store: s,
		client: &http.Client{
			Timeout: webhookDeliveryTimeout,
			// No redirects: a 3xx aimed at an internal host is a classic
			// SSRF pivot and every hop would need the target policy re-run.
			// The 3xx is simply the receiver's answer.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		validateTarget: svc.ValidateTarget,
		newSecret:      service.GenerateWebhookSecret,
		lookup:         net.DefaultResolver.LookupIPAddr,
	}
}

// resolveOwnedWebhookDelivery performs the ownership checks every
// endpoint here needs: a missing webhook is 404, and a delivery that
// belongs to ANOTHER webhook id is 404 too — ownership is enforced, and
// the error is indistinguishable from "no such delivery" so probing ids
// gains nothing.
func (h *WebhookReplayHandler) resolveOwnedWebhookDelivery(c *gin.Context) (*model.Webhook, *model.WebhookDelivery, bool) {
	ctx := c.Request.Context()
	wh, err := h.store.FindWebhookByID(ctx, c.Param("id"))
	if err != nil {
		response.NotFound(c, "webhook not found")
		return nil, nil, false
	}
	if !requireKeyProductScope(c, wh.ProductID) {
		return nil, nil, false
	}
	d, err := h.store.FindWebhookDeliveryByID(ctx, c.Param("delivery_id"))
	if err != nil || d.WebhookID != wh.ID {
		response.NotFound(c, "delivery not found")
		return nil, nil, false
	}
	return wh, d, true
}

// guardTarget applies the full delivery-time target policy before a
// replay leaves: the merchant write-time check (service.ValidateTarget),
// then the strict literal + resolved-address classification shared with
// the portal path. Fail closed on anything unclassifiable.
func (h *WebhookReplayHandler) guardTarget(ctx context.Context, rawURL string) error {
	if err := h.validateTarget(rawURL); err != nil {
		return err
	}
	return guardWebhookResolvedTarget(ctx, h.lookup, rawURL)
}

// send performs ONE signed delivery attempt in the system's single
// signing shape (identical to service.deliver and postDelivery) plus the
// replay marker header. Returns the receiver's status code and a bounded
// body sample — errors are transport errors, not deliveries.
func (h *WebhookReplayHandler) send(ctx context.Context, wh *model.Webhook, deliveryID, event string, body []byte) (int, string, error) {
	sig := signCustomerWebhookPayload(body, wh.Secret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HiTechCloud-Event", event)
	req.Header.Set("X-HiTechCloud-Signature", "sha256="+sig)
	req.Header.Set("X-HiTechCloud-Delivery", deliveryID)
	req.Header.Set(webhookReplayMarkerHeader, webhookReplayMarkerValue)
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, webhookReplayMaxResponseBody))
	return resp.StatusCode, string(b), nil
}

// ReplayDelivery answers
// POST /api/v1/admin/webhooks/:id/deliveries/:delivery_id/replay.
//
// Contract:
//   - 201 + the new delivery row (including replay_of = original id) on
//     acceptance; the row's status/attempt fields reflect the first
//     attempt (delivered, or pending with a scheduled retry on failure —
//     identical lifecycle to a fresh delivery).
//   - 404 when the webhook or the delivery is missing OR the delivery
//     belongs to a different webhook (ownership).
//   - 409 NOT_RESENDABLE when the webhook is inactive/deleted — same
//     refusal as resend; re-enable it first.
//   - 400 when the target fails the webhook URL policy.
//   - Signed with the CURRENT secret; marked X-HiTechCloud-Replay: true;
//     new X-HiTechCloud-Delivery id; body byte-identical to the original.
//   - Audited: Entity "webhook_delivery", Action "replayed".
//
// Idempotent-safe: each call mints a NEW delivery row (replay_of the
// same original) and a fresh delivery id — it never mutates the
// original row, so double-clicking replay cannot corrupt history. If a
// receiver wants once-only semantics it keys on the original payload or
// the replay_of lineage, exactly as it would for a re-sent delivery.
func (h *WebhookReplayHandler) ReplayDelivery(c *gin.Context) {
	wh, orig, ok := h.resolveOwnedWebhookDelivery(c)
	if !ok {
		return
	}
	// Same refusal as ResendDelivery: a disabled endpoint cannot accept a
	// delivery. 409 NOT_RESENDABLE keeps the resend contract uniform.
	if !wh.Active {
		response.Err(c, 409, "NOT_RESENDABLE",
			"webhook is deleted or inactive — re-enable it before replaying")
		return
	}
	ctx := c.Request.Context()

	// The stored payload is re-marshalled exactly like deliver() does, so
	// the body is byte-identical to the original dispatch and receiver-
	// side idempotency keys keep matching.
	body, err := json.Marshal(orig.Payload)
	if err != nil {
		response.Internal(c, err)
		return
	}
	if err := h.guardTarget(ctx, wh.URL); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	// Insert-first with the standard in-flight hold-off: the row exists
	// (and blocks the retry sweep) before a byte leaves.
	hold := time.Now().Add(webhookReplayInflightHoldOff)
	fresh := &model.WebhookReplayDelivery{
		WebhookDelivery: model.WebhookDelivery{
			WebhookID: wh.ID,
			Event:     orig.Event,
			Payload:   orig.Payload,
			Status:    "pending",
			NextRetry: &hold,
		},
		ReplayOf: orig.ID,
	}
	if err := h.store.CreateWebhookReplayDelivery(ctx, fresh); err != nil {
		response.Internal(c, err)
		return
	}

	// First attempt synchronous so the admin sees the receiver's answer;
	// failures fall back to the standard retry loop like any delivery.
	code, respBody, sendErr := h.send(ctx, wh, fresh.ID, orig.Event, body)
	d := &fresh.WebhookDelivery
	d.Attempts = 1
	if sendErr != nil {
		d.ResponseBody = sendErr.Error()
	} else {
		d.ResponseCode = code
		d.ResponseBody = respBody
	}
	if sendErr != nil || code < 200 || code >= 300 {
		next := time.Now().Add(time.Duration(1<<uint(d.Attempts)) * webhookReplayRetryUnit) // 60s — service's scheduleRetry shape
		d.NextRetry = &next
		d.Status = "pending"
		middleware.WebhookDeliveries.WithLabelValues("retrying").Inc()
	} else {
		now := time.Now()
		d.Status = "delivered"
		d.DeliveredAt = &now
		middleware.WebhookDeliveries.WithLabelValues("delivered").Inc()
	}
	// Update failures are dropped exactly like service.deliver drops
	// them: the dispatch already happened and must not be reported as a
	// handler error.
	_ = h.store.UpdateWebhookDelivery(ctx, d)

	h.store.Audit(ctx, &model.AuditLog{
		Entity: "webhook_delivery", EntityID: fresh.ID, Action: "replayed",
		ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
		Changes: map[string]any{
			"original_delivery_id": orig.ID,
			"webhook_id":           wh.ID,
			"event":                orig.Event,
		},
	})
	response.Created(c, fresh)
}

// RotateSecret answers POST /api/v1/admin/webhooks/:id/rotate with a
// fresh merchant signing secret (service.GenerateWebhookSecret format —
// 64 lowercase hex chars) returned ONCE, in the exact shape CreateWebhook
// answers with: {id, product_id, url, secret, events, active,
// created_at}. The secret is written through the same sealing path
// creation uses (crypto.Seal inside UpdateWebhook) and never logged.
//
// Semantics: IMMEDIATE invalidation. The old secret stops signing the
// moment the write commits — every read path opens what is stored. The
// single documented edge: a delivery already in flight when the write
// commits was signed with the secret it read before rotation and still
// verifies with that one until it finishes. Future deliveries (and
// replays) verify with the new secret only.
//
// 404 when the webhook is missing (product-scope keys get the usual
// PRODUCT_SCOPE_MISMATCH 403). Audited: Entity "webhook", Action
// "secret_rotated" — the audit carries the URL, never the secret.
func (h *WebhookReplayHandler) RotateSecret(c *gin.Context) {
	ctx := c.Request.Context()
	wh, err := h.store.FindWebhookByID(ctx, c.Param("id"))
	if err != nil {
		response.NotFound(c, "webhook not found")
		return
	}
	if !requireKeyProductScope(c, wh.ProductID) {
		return
	}
	secret := h.newSecret()
	wh.Secret = secret
	if err := h.store.UpdateWebhook(ctx, wh); err != nil {
		response.Internal(c, err)
		return
	}
	h.store.Audit(ctx, &model.AuditLog{
		Entity: "webhook", EntityID: wh.ID, Action: "secret_rotated",
		ActorType: "admin", ActorID: adminID(c), IPAddress: c.ClientIP(),
		Changes: map[string]any{"url": wh.URL},
	})
	response.OK(c, gin.H{
		"id":         wh.ID,
		"product_id": wh.ProductID,
		"url":        wh.URL,
		"secret":     secret,
		"events":     wh.Events,
		"active":     wh.Active,
		"created_at": wh.CreatedAt,
	})
}
