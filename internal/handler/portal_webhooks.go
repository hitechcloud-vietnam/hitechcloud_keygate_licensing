package handler

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/apperr"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

// PortalWebhookHandler exposes customer webhook self-service (plan §29
// portal "Webhooks", §35 Webhooks).
//
//	GET    /portal/webhooks              List the caller's own endpoints
//	POST   /portal/webhooks              Register one (secret returned ONCE)
//	PATCH  /portal/webhooks/:id          Partial update (url?/events?/active?)
//	DELETE /portal/webhooks/:id          Hard delete
//	POST   /portal/webhooks/:id/rotate   Fresh signing secret, shown once
//	POST   /portal/webhooks/:id/test     Fire a test delivery, report accepted/failed
//
// The signing secret is returned exactly once, in the Create and Rotate
// answers, and is never recoverable afterwards. Every response's row
// carries only secret_prefix (a display hint); the full secret is
// json:"-" on the model and NEVER logged.
//
// Identity: middleware.SessionAuth (the /portal group's auth) puts the
// session user's id into the gin context under "user_id". Every lookup
// here is scoped to that id, and an endpoint that belongs to someone
// else answers the same 404 as one that does not exist (no IDOR).
type PortalWebhookHandler struct {
	store  customerWebhookStore
	client customerWebhookDoer
	// lookup resolves a delivery target's host so a name that now points
	// into loopback / RFC 1918 / CGNAT / metadata space is refused even
	// when the literal saved at write time was public (DNS changes between
	// save and delivery). nil means "classify literal addresses only" —
	// tests that fake the transport leave it unset; the production
	// constructor always wires the real resolver.
	lookup func(ctx context.Context, host string) ([]net.IPAddr, error)
}

// customerWebhookStore is the seam this handler needs from the store.
// *store.Store satisfies it (see the compile-time assertion below); tests
// substitute a fake so the handler can be exercised without a database.
type customerWebhookStore interface {
	CreateCustomerWebhook(ctx context.Context, w *model.CustomerWebhook, rawSecret string) error
	ListCustomerWebhooksByUser(ctx context.Context, userID string, p store.Page) ([]*model.CustomerWebhook, int, error)
	FindCustomerWebhookByID(ctx context.Context, id string) (*model.CustomerWebhook, error)
	UpdateCustomerWebhook(ctx context.Context, w *model.CustomerWebhook) error
	DeleteCustomerWebhook(ctx context.Context, id string) error
	CountCustomerWebhooksByUser(ctx context.Context, userID string) (int, error)
	FindCustomerWebhooksForEvent(ctx context.Context, event string) ([]*model.CustomerWebhook, error)
	TouchCustomerWebhookLastDelivery(ctx context.Context, id string) error
	RotateCustomerWebhookSecret(ctx context.Context, id, rawSecret string) error
	Audit(ctx context.Context, log *model.AuditLog)
}

// customerWebhookDoer is the HTTP seam for deliveries. *http.Client
// satisfies it; tests inject a fake to assert the signing shape without
// touching the network.
type customerWebhookDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

var (
	_ customerWebhookStore = (*store.Store)(nil)
	_ customerWebhookDoer  = (*http.Client)(nil)
)

// maxCustomerWebhooksPerUser caps how many endpoints one customer may
// register. A pre-insert count is the cheap guard; a race between two
// concurrent creates can slip one past the cap, which is acceptable for
// a UX limit (not a security boundary).
const maxCustomerWebhooksPerUser = 10

// customerWebhookTestEvent is the event name a test delivery carries. It
// mirrors service.DeliverTest's "webhook.test" exactly, so a customer
// endpoint sees one event name and one signed format for a test ping no
// matter which webhook system delivers it. It is NOT part of the
// subscription vocabulary (a customer cannot subscribe to it).
const customerWebhookTestEvent = "webhook.test"

// NewPortalWebhookHandler wires the handler to the store. The caller
// (main.go) passes the concrete *store.Store; tests pass a fake.
func NewPortalWebhookHandler(s customerWebhookStore) *PortalWebhookHandler {
	return &PortalWebhookHandler{
		store:  s,
		client: defaultCustomerWebhookClient(),
		lookup: net.DefaultResolver.LookupIPAddr,
	}
}

// defaultCustomerWebhookClient is the delivery client: short timeout so
// a test fire never blocks a request for long, and redirects never
// followed (a 3xx pointed at an internal host is a classic SSRF pivot,
// and each hop would need the same target check as the first request —
// so a 3xx is simply the receiver's non-2xx answer).
func defaultCustomerWebhookClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// resolveOwnedWebhook loads the endpoint at :id and verifies the session
// user owns it. "Not yours" and "does not exist" are deliberately the
// same 404.
func (h *PortalWebhookHandler) resolveOwnedWebhook(c *gin.Context) (*model.CustomerWebhook, bool) {
	userID := portalUserID(c)
	if userID == "" {
		response.Unauthorized(c, "unauthorized")
		return nil, false
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		response.BadRequest(c, "id is required in the URL path")
		return nil, false
	}
	wh, err := h.store.FindCustomerWebhookByID(c.Request.Context(), id)
	if err != nil || wh.UserID != userID {
		response.NotFound(c, "webhook not found")
		return nil, false
	}
	return wh, true
}

// normalizeCustomerWebhook folds what the portal form sent into the
// shape that gets stored — the one place create and update validate, so
// the row and the refusal cannot drift apart. Events are trimmed,
// deduped and validated against the vocabulary; the URL is checked
// against the write-time target policy. On success w.Events is folded.
func normalizeCustomerWebhook(w *model.CustomerWebhook) error {
	if err := model.ValidateCustomerWebhookURL(w.URL); err != nil {
		return apperr.BadRequest(err.Error())
	}
	// §61 hardening on top of the model's fast filter: that check is
	// std-lib-only and misses CGNAT (100.64/10 — where cloud metadata
	// services like 100.100.100.100 live), the protocol/documentation
	// blocks, multicast and the IPv4-embedding IPv6 transition ranges.
	// The write path refuses those too; the message is the model's, so
	// one policy reads as one policy.
	if err := guardWebhookLiteralTarget(w.URL); err != nil {
		return apperr.BadRequest(err.Error())
	}
	events, err := model.FoldCustomerWebhookEvents(w.Events)
	if err != nil {
		return apperr.BadRequest(err.Error())
	}
	w.Events = events
	return nil
}

// errWebhookPrivateTarget is the one refusal shared by every webhook
// target check in this package. Same wording as the model's, so create,
// update, and the delivery-time guard all read as one policy.
var errWebhookPrivateTarget = errors.New("url must not point at loopback or private addresses")

// webhookInternalNets is everything a webhook must never be delivered
// to. It mirrors service.nonPublicNets (the merchant delivery guard)
// exactly: loopback, RFC 1918, CGNAT (100.64/10 — cloud metadata
// services live there too), link-local, the IETF/benchmark/documentation
// blocks, class E, broadcast, and the IPv6 equivalents including the
// ranges that embed an IPv4 address (NAT64, 6to4). One list, one
// doctrine — a target refused by the merchant system is refused by the
// customer system and vice versa.
var webhookInternalNets = func() []*net.IPNet {
	cidrs := []string{
		"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
		"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24",
		"192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24",
		"224.0.0.0/4", "240.0.0.0/4", "255.255.255.255/32",
		"::/128", "::1/128", "64:ff9b::/96", "100::/64", "2001::/32",
		"2001:db8::/32", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8",
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("webhook: bad cidr " + c)
		}
		nets = append(nets, n)
	}
	return nets
}()

// isNonPublicWebhookIP reports whether ip is a target no webhook may be
// delivered to (see webhookInternalNets). A v4-mapped v6 address is
// checked as its v4 self. Nil/unparseable fails closed.
func isNonPublicWebhookIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range webhookInternalNets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// guardWebhookLiteralTarget refuses a webhook URL whose HOST is a
// literal non-public address (or "localhost"). Hostnames pass here and
// are classified at delivery time — DNS can change between save and
// send, so the literal check alone is not the authoritative guard.
func guardWebhookLiteralTarget(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host := u.Hostname()
	if strings.EqualFold(host, "localhost") {
		return errWebhookPrivateTarget
	}
	if ip := net.ParseIP(host); ip != nil && isNonPublicWebhookIP(ip) {
		return errWebhookPrivateTarget
	}
	return nil
}

// guardWebhookResolvedTarget is the delivery-time half: literals are
// re-classified (a row saved before a policy tightening stays refused),
// and when a resolver is wired the host's ADDRESSES are classified too
// — closing the DNS-rebinding window between save and send. An
// unresolvable or address-less name fails closed: "we could not check"
// is not "safe" (an NXDOMAIN for us can be the metadata service for
// whatever resolves next).
func guardWebhookResolvedTarget(ctx context.Context, lookup func(context.Context, string) ([]net.IPAddr, error), raw string) error {
	if err := guardWebhookLiteralTarget(raw); err != nil {
		return err
	}
	if lookup == nil {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	host := u.Hostname()
	if net.ParseIP(host) != nil {
		return nil // literal already classified above
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		return errWebhookPrivateTarget
	}
	if len(addrs) == 0 {
		return errWebhookPrivateTarget
	}
	for _, a := range addrs {
		if isNonPublicWebhookIP(a.IP) {
			return errWebhookPrivateTarget
		}
	}
	return nil
}

// List answers GET /portal/webhooks: the caller's own endpoints, newest
// first. A row never carries the secret or its hash — only secret_prefix
// (see the model's json tags).
func (h *PortalWebhookHandler) List(c *gin.Context) {
	userID := portalUserID(c)
	if userID == "" {
		response.Unauthorized(c, "unauthorized")
		return
	}
	page := listPage(c)
	webhooks, total, err := h.store.ListCustomerWebhooksByUser(c.Request.Context(), userID, page)
	if err != nil {
		response.Internal(c, err)
		return
	}
	listOK(c, "webhooks", webhooks, total, page)
}

// Create answers POST /portal/webhooks: a fresh signing secret is
// generated and returned in this one response (data.{webhook, secret});
// there is no endpoint that can show it again. Body:
// { url (required), events (required, non-empty), active? (default true) }.
func (h *PortalWebhookHandler) Create(c *gin.Context) {
	userID := portalUserID(c)
	if userID == "" {
		response.Unauthorized(c, "unauthorized")
		return
	}
	var req struct {
		URL    string   `json:"url"`
		Events []string `json:"events"`
		Active *bool    `json:"active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body: url and events are required")
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}
	wh := &model.CustomerWebhook{
		UserID: userID,
		URL:    req.URL,
		Events: req.Events,
		Active: active,
	}
	if err := normalizeCustomerWebhook(wh); err != nil {
		writeAppErr(c, err)
		return
	}
	n, err := h.store.CountCustomerWebhooksByUser(c.Request.Context(), userID)
	if err != nil {
		response.Internal(c, err)
		return
	}
	if n >= maxCustomerWebhooksPerUser {
		response.BadRequest(c, fmt.Sprintf("webhook limit reached (maximum %d endpoints)", maxCustomerWebhooksPerUser))
		return
	}
	secret, err := model.NewCustomerWebhookSecret()
	if err != nil {
		response.Internal(c, err)
		return
	}
	if err := h.store.CreateCustomerWebhook(c.Request.Context(), wh, secret); err != nil {
		response.Internal(c, err)
		return
	}
	h.store.Audit(c.Request.Context(), &model.AuditLog{
		Entity: "customer_webhook", EntityID: wh.ID, Action: "created",
		ActorType: "portal_user", ActorID: userID, IPAddress: c.ClientIP(),
		Changes: map[string]any{"url": wh.URL, "events": wh.Events, "active": wh.Active},
	})
	response.Created(c, gin.H{"webhook": wh, "secret": secret})
}

// Update answers PATCH /portal/webhooks/:id with partial semantics: each
// of url / events / active is applied only when present. An empty body
// is a no-op that returns the current row. The secret is never in the
// write path (see UpdateCustomerWebhook) and is never returned.
func (h *PortalWebhookHandler) Update(c *gin.Context) {
	wh, ok := h.resolveOwnedWebhook(c)
	if !ok {
		return
	}
	var req struct {
		URL    *string   `json:"url"`
		Events *[]string `json:"events"`
		Active *bool     `json:"active"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "invalid request body")
		return
	}
	if req.URL != nil {
		wh.URL = *req.URL
	}
	if req.Events != nil {
		wh.Events = *req.Events
	}
	if req.Active != nil {
		wh.Active = *req.Active
	}
	if err := normalizeCustomerWebhook(wh); err != nil {
		writeAppErr(c, err)
		return
	}
	if err := h.store.UpdateCustomerWebhook(c.Request.Context(), wh); err != nil {
		response.Internal(c, err)
		return
	}
	h.store.Audit(c.Request.Context(), &model.AuditLog{
		Entity: "customer_webhook", EntityID: wh.ID, Action: "updated",
		ActorType: "portal_user", ActorID: portalUserID(c), IPAddress: c.ClientIP(),
		Changes: map[string]any{"url": wh.URL, "events": wh.Events, "active": wh.Active},
	})
	response.OK(c, gin.H{"webhook": wh})
}

// Delete answers DELETE /portal/webhooks/:id — a HARD delete (the API
// contract specifies hard delete). The audit trail lives in audit_logs.
func (h *PortalWebhookHandler) Delete(c *gin.Context) {
	wh, ok := h.resolveOwnedWebhook(c)
	if !ok {
		return
	}
	if err := h.store.DeleteCustomerWebhook(c.Request.Context(), wh.ID); err != nil {
		response.Internal(c, err)
		return
	}
	h.store.Audit(c.Request.Context(), &model.AuditLog{
		Entity: "customer_webhook", EntityID: wh.ID, Action: "deleted",
		ActorType: "portal_user", ActorID: portalUserID(c), IPAddress: c.ClientIP(),
		Changes: map[string]any{"url": wh.URL},
	})
	response.NoContent(c)
}

// Rotate answers POST /portal/webhooks/:id/rotate with a FRESH signing
// secret, in the same shape Create answers with
// ({"webhook": <row>, "secret": "whsec_…"} — 200 instead of 201). The
// secret is shown ONCE here and is unrecoverable afterwards; the row
// carries only secret_prefix.
//
// Storage goes through the same sealing as creation
// (crypto.Seal inside store.RotateCustomerWebhookSecret), and the old
// secret is invalid the moment the write commits: every read path opens
// what is stored, so the next delivery is signed with the new secret —
// immediate invalidation, with the single documented edge that a
// delivery already in flight when the write commits was signed with the
// secret it read before rotation.
//
// Ownership is the same quiet 404 as every other portal write: a
// cross-user id is indistinguishable from a missing one, so probing
// someone else's webhook id gains nothing. Never logs the secret; the
// audit trail records only its display prefix.
func (h *PortalWebhookHandler) Rotate(c *gin.Context) {
	wh, ok := h.resolveOwnedWebhook(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	secret, err := model.NewCustomerWebhookSecret()
	if err != nil {
		response.Internal(c, err)
		return
	}
	if err := h.store.RotateCustomerWebhookSecret(ctx, wh.ID, secret); err != nil {
		response.Internal(c, err)
		return
	}
	wh.Secret = secret
	wh.SecretPrefix = model.CustomerWebhookDisplayPrefix(secret)
	h.store.Audit(ctx, &model.AuditLog{
		Entity: "customer_webhook", EntityID: wh.ID, Action: "secret_rotated",
		ActorType: "portal_user", ActorID: portalUserID(c), IPAddress: c.ClientIP(),
		Changes: map[string]any{"secret_prefix": wh.SecretPrefix},
	})
	response.OK(c, gin.H{"webhook": wh, "secret": secret})
}

// DispatchTest answers POST /portal/webhooks/:id/test: it fires a signed
// test delivery at exactly this endpoint and reports whether the
// endpoint accepted it (2xx). It is synchronous with a short timeout, so
// a slow receiver cannot hang the request. Testing works on any owned
// endpoint, active or not — testing is a diagnostic the customer triggers
// on purpose (diverging from service.DeliverTest, which refuses inactive
// webhooks). The secret is used to sign and never echoed or logged.
func (h *PortalWebhookHandler) DispatchTest(c *gin.Context) {
	wh, ok := h.resolveOwnedWebhook(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	status, err := h.postDelivery(ctx, wh, customerWebhookTestEvent, map[string]any{
		"webhook_id": wh.ID,
		"message":    "This is a test delivery from HiTechCloud.",
	})
	// Stamp last_delivery_at regardless of accept/fail: the customer
	// asked for a delivery and one was attempted.
	_ = h.store.TouchCustomerWebhookLastDelivery(ctx, wh.ID)
	if err != nil {
		// Transport-level failure: the endpoint could not be reached. The
		// cause may name the target URL; it is not a secret but is an
		// operator concern, so it is logged, not echoed to the client.
		// The signing secret and signature are never in it.
		slog.Warn("customer webhook test delivery failed", "webhook_id", wh.ID, "error", err)
		response.OK(c, gin.H{"accepted": false, "status_code": 0, "event": customerWebhookTestEvent})
		return
	}
	h.store.Audit(ctx, &model.AuditLog{
		Entity: "customer_webhook", EntityID: wh.ID, Action: "tested",
		ActorType: "portal_user", ActorID: portalUserID(c), IPAddress: c.ClientIP(),
		Changes: map[string]any{"status_code": status},
	})
	response.OK(c, gin.H{
		"accepted":    status >= 200 && status < 300,
		"status_code": status,
		"event":       customerWebhookTestEvent,
	})
}

// DispatchCustomerEvent fans an event out to every ACTIVE customer
// webhook subscribed to it and returns an error if any delivery could not
// be completed. It is the entry point the rest of the codebase calls when
// something happens that a customer may be listening for (e.g.
// model.EventLicenseCreated). It is NOT wired into other packages yet —
// the call sites land later; this is the seam they will use.
//
// The payload is wrapped exactly like the merchant webhook service:
// {"event":…, "timestamp":…, "data":…} and signed HMAC-SHA256 with each
// endpoint's own secret. Callers wanting fire-and-forget can run it in a
// goroutine; it is synchronous here so it is deterministic to test.
//
// It never logs or returns a signing secret.
func (h *PortalWebhookHandler) DispatchCustomerEvent(ctx context.Context, event string, data map[string]any) error {
	hooks, err := h.store.FindCustomerWebhooksForEvent(ctx, event)
	if err != nil {
		return fmt.Errorf("find customer webhooks: %w", err)
	}
	failed := 0
	for _, wh := range hooks {
		if _, err := h.postDelivery(ctx, wh, event, data); err != nil {
			failed++
			continue
		}
		_ = h.store.TouchCustomerWebhookLastDelivery(ctx, wh.ID)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d customer webhook deliveries failed", failed, len(hooks))
	}
	return nil
}

// postDelivery signs and POSTs one payload to a single endpoint,
// mirroring the merchant deliver() header format exactly:
//
//	Content-Type: application/json
//	X-HiTechCloud-Event:      <event>
//	X-HiTechCloud-Signature:  sha256=<hex HMAC-SHA256 of the body>
//	X-HiTechCloud-Delivery:   <fresh id>
//
// A fresh delivery id per attempt gives the receiver an idempotency /
// dedup key, as on the merchant path. Returns the HTTP status on a
// completed exchange (any status, including non-2xx) and an error only on
// a transport failure.
func (h *PortalWebhookHandler) postDelivery(ctx context.Context, wh *model.CustomerWebhook, event string, data map[string]any) (int, error) {
	// §61 delivery-time target guard (SSRF): the write-time policy only
	// saw the URL as saved — a legacy row, or a name whose DNS has moved
	// since, must not turn this dispatch into a request to the metadata
	// service or an internal host. Literals are always classified;
	// resolved addresses too whenever a resolver is wired (production
	// always has one — NewPortalWebhookHandler sets it).
	if err := guardWebhookResolvedTarget(ctx, h.lookup, wh.URL); err != nil {
		return 0, err
	}
	payload := map[string]any{
		"event":     event,
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"data":      data,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal payload: %w", err)
	}
	sig := signCustomerWebhookPayload(body, wh.Secret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-HiTechCloud-Event", event)
	req.Header.Set("X-HiTechCloud-Signature", "sha256="+sig)
	req.Header.Set("X-HiTechCloud-Delivery", store.NewID())
	resp, err := h.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// Drain a bounded amount so the connection can be reused, without
	// letting a chatty receiver stream unbounded data at us.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, nil
}

// signCustomerWebhookPayload is the HMAC-SHA256 signature in the same
// shape the merchant webhook service produces (service.signPayload):
// lowercase hex of HMAC-SHA256(payload, secret), sent as "sha256=<hex>".
// One signing scheme across both systems, so a receiver verifies one way.
func signCustomerWebhookPayload(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return hex.EncodeToString(mac.Sum(nil))
}
