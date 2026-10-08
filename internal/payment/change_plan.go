// Subscription upgrade / downgrade with proration (plan.md §77/§78).
//
// This file carries the engine behind StripeHandler.ChangePlan (the
// portal route POST /api/v1/portal/subscription/change-plan):
//
//   - an immediate change (the §77 "upgrade may be immediate", and a
//     downgrade asked for right away) switches the subscription item's
//     price with proration_behavior="always_invoice": Stripe computes
//     the unused-time credit, the remaining period and the new plan
//     charge and invoices the difference now. HiTechCloud never
//     computes a proration — every amount reported back is an integer
//     minor-unit field read straight from the Stripe invoice (plan.md
//     §51/§78: no floating point, no hand-rolled financial math);
//
//   - a deferred change (the §77 "downgrade ... at next billing
//     cycle", or a scheduled upgrade) is recorded as a metadata intent
//     on the Stripe subscription itself —
//     pending_plan_id + pending_change_at (= the subscription's
//     current_period_end at request time) — and executed by the
//     renewal webhook path (invoice.paid / customer.subscription.updated
//     → syncFromCurrent → applyPlanIntent below), which switches the
//     price with proration_behavior="none" at the period boundary.
//     The execution is idempotent: the same Update that switches the
//     price also deletes the intent, and an applied-marker keeps the
//     double delivery Stripe sends at renewal (invoice.paid AND
//     customer.subscription.updated) to a single switch, audit line
//     and plan.changed webhook.
//
// Why a metadata intent and not a Subscription Schedule: the intent
// rides on the object whose renewal drives it, so the execution hook
// sits in the same code that already reconciles the licence with the
// subscription at renewal, needs no new API surface or background
// sweeper, survives process restarts, and is trivially replaced
// ("last wins") or dropped (subscription cancellation) with a single
// metadata write. Both of those happen atomically with the other
// change they accompany.
//
// Entitlement safety (§77 "entitlement reduction must be handled
// safely"): a licence's caps resolve through its plan id at check time,
// so the plan write is the entitlement switch. writePlanID performs it
// loudly — a failed write is a failed request (or a retried webhook),
// never a silent success while the old caps linger — and
// reconcilePlanFromBilledPrice heals any drift in the renewal path by
// moving the licence back onto the plan the customer is actually
// billed for. What this file deliberately does NOT do is evict
// existing activations / seats that exceed a smaller plan's caps;
// enforcement refuses new ones the moment the plan id moves, and
// eviction policy belongs to the licence engine (see the Lead notes in
// the delivery report).
package payment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v82"
	stripeprice "github.com/stripe/stripe-go/v82/price"
	"github.com/stripe/stripe-go/v82/subscription"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/pkg/response"
)

const (
	// When a plan change takes effect. "immediate" bills the prorated
	// difference now; "next_period" defers the switch to the period
	// end with no proration at all.
	changeTimingImmediate  = "immediate"
	changeTimingNextPeriod = "next_period"

	// How the target plan's price compares with the one billed now.
	// "unknown" (unfetchable, metered, or a currency mismatch) is
	// treated as upgrade-safe: the safe default is the immediate
	// change the customer asked for, never a silent deferral.
	changeDirectionUpgrade   = "upgrade"
	changeDirectionDowngrade = "downgrade"
	changeDirectionUnknown   = "unknown"

	// Stripe proration behaviors used here. always_invoice makes
	// Stripe compute the proration (unused time credited, the new
	// plan's remainder charged) and invoice the difference at once;
	// none moves the price at the period boundary with no proration.
	prorationAlwaysInvoice = "always_invoice"
	prorationNone          = "none"

	// The deferred-change intent, stamped on the Stripe subscription's
	// metadata. pending_change_at is the subscription's period end at
	// request time — the instant the switch is due — in Unix seconds.
	// An empty value deletes the key on Stripe's side.
	metaPendingPlanID   = "pending_plan_id"
	metaPendingChangeAt = "pending_change_at"
)

// pendingPlanChange is the deferred-change intent carried on the
// subscription's metadata.
type pendingPlanChange struct {
	PlanID   string
	ChangeAt int64 // Unix seconds; 0 when the request carried none
}

// parsePendingPlanChange reads the intent off a subscription's
// metadata. ok is false when there is no intent, including the
// "cleared" form (empty value) a delete leaves behind.
func parsePendingPlanChange(meta map[string]string) (pendingPlanChange, bool) {
	planID := strings.TrimSpace(meta[metaPendingPlanID])
	if planID == "" {
		return pendingPlanChange{}, false
	}
	p := pendingPlanChange{PlanID: planID}
	if raw := strings.TrimSpace(meta[metaPendingChangeAt]); raw != "" {
		if unix, err := strconv.ParseInt(raw, 10, 64); err == nil {
			p.ChangeAt = unix
		}
	}
	return p, true
}

// pendingChangeDue reports whether the intent's moment has come. The
// clock reaching the stamped instant is the primary trigger; a period
// that has already rolled past it (a renewal a moment early, or a
// skewed clock) counts as well. An intent with no instant is never due
// — guessing would move a plan mid-period — and is discarded only if
// its plan goes away (see executePendingPlanChange).
func pendingChangeDue(p pendingPlanChange, now, periodEnd int64) bool {
	if p.ChangeAt <= 0 {
		return false
	}
	return now >= p.ChangeAt || periodEnd > p.ChangeAt
}

// classifyPlanChange compares two unit amounts in integer minor units.
// Either side unknown, or currencies that differ, is "unknown" — never
// a downgrade guess. Per §77 a target at or above the current price is
// upgrade-safe (>= includes an equal-price move).
func classifyPlanChange(curAmount *int64, curCurrency string, tgtAmount *int64, tgtCurrency string) string {
	if curAmount == nil || tgtAmount == nil || curCurrency == "" || tgtCurrency == "" {
		return changeDirectionUnknown
	}
	if !strings.EqualFold(curCurrency, tgtCurrency) {
		return changeDirectionUnknown
	}
	if *tgtAmount >= *curAmount {
		return changeDirectionUpgrade
	}
	return changeDirectionDowngrade
}

// defaultTimingFor is the §77 default: an upgrade lands immediately, a
// downgrade waits for the next billing cycle.
func defaultTimingFor(direction string) string {
	if direction == changeDirectionDowngrade {
		return changeTimingNextPeriod
	}
	return changeTimingImmediate
}

// priceAmount reads a Stripe price in integer minor units. nil when
// the amount cannot be compared: no price, no currency, or a metered
// price whose unit amount is not what the customer pays.
func priceAmount(p *stripe.Price) (*int64, string) {
	if p == nil || p.Currency == "" {
		return nil, ""
	}
	if p.Recurring != nil && string(p.Recurring.UsageType) == "metered" {
		return nil, string(p.Currency)
	}
	amount := p.UnitAmount
	return &amount, string(p.Currency)
}

// subFirstItem is the item HiTechCloud manages: it creates single-item
// subscriptions, and change-plan moves that one item's price.
func subFirstItem(sub *stripe.Subscription) *stripe.SubscriptionItem {
	if sub == nil || sub.Items == nil {
		return nil
	}
	for _, it := range sub.Items.Data {
		if it != nil {
			return it
		}
	}
	return nil
}

// subItemPrice is the price the subscription's first item is billed at,
// or nil when the payload does not carry one.
func subItemPrice(sub *stripe.Subscription) *stripe.Price {
	if it := subFirstItem(sub); it != nil {
		return it.Price
	}
	return nil
}

// subPeriodEnd is the end of the subscription's current billing period:
// the latest item period end (API 2025-03-31+ keeps it on the items),
// or 0 when the payload says none.
func subPeriodEnd(sub *stripe.Subscription) int64 {
	if sub == nil || sub.Items == nil {
		return 0
	}
	var end int64
	for _, it := range sub.Items.Data {
		if it == nil {
			continue
		}
		if it.CurrentPeriodEnd > end {
			end = it.CurrentPeriodEnd
		}
	}
	return end
}

// prorationInvoiceJSON reports the invoice a prorated change raised,
// exactly as Stripe computed it: integer minor units, no rounding or
// arithmetic of ours. nil when the change raised no invoice (the
// response then carries a JSON null).
func prorationInvoiceJSON(sub *stripe.Subscription) gin.H {
	if sub == nil || sub.LatestInvoice == nil || sub.LatestInvoice.ID == "" {
		return nil
	}
	inv := sub.LatestInvoice
	return gin.H{
		"id":                 inv.ID,
		"amount_due":         inv.AmountDue,
		"amount_paid":        inv.AmountPaid,
		"currency":           string(inv.Currency),
		"hosted_invoice_url": inv.HostedInvoiceURL,
	}
}

// fetchSubscriptionForChange reads a subscription with its item prices
// expanded, so the upgrade/downgrade default can be decided from real
// amounts. Everything else (metadata, item ids) the response carries
// either way.
func fetchSubscriptionForChange(id string) (*stripe.Subscription, error) {
	params := &stripe.SubscriptionParams{}
	params.AddExpand("items.data.price")
	sub, err := subscription.Get(id, params)
	if err != nil {
		return nil, err
	}
	return sub, nil
}

// planChangeDirection compares the price billed now with the target
// plan's price. The target price is only fetched when the current one
// is known — an unresolvable comparison is upgrade-safe ("unknown"),
// never a downgrade (and never a second Stripe call for nothing).
func planChangeDirection(sub *stripe.Subscription, target *model.Plan) string {
	curAmount, curCurrency := priceAmount(subItemPrice(sub))
	if curAmount == nil {
		return changeDirectionUnknown
	}
	price, err := stripeprice.Get(target.StripePriceID, nil)
	if err != nil {
		slog.Warn("stripe: target price unreadable, treating the plan change as upgrade-safe",
			"price_id", target.StripePriceID, "error", err)
		return changeDirectionUnknown
	}
	tgtAmount, tgtCurrency := priceAmount(price)
	return classifyPlanChange(curAmount, curCurrency, tgtAmount, tgtCurrency)
}

// resolveChangeTarget maps the request's plan reference to a plan: the
// plan id (preferred), or the legacy new_price_id spelling (the plan
// behind a Stripe price). When both are given they must name the same
// plan. Writes the refusal itself — 404 PLAN_NOT_FOUND for anything
// unresolvable — and returns nil; callers stop there.
func (h *StripeHandler) resolveChangeTarget(c *gin.Context, planID, priceID string) *model.Plan {
	planID = strings.TrimSpace(planID)
	priceID = strings.TrimSpace(priceID)
	if planID == "" && priceID == "" {
		response.BadRequest(c, "plan_id or new_price_id is required")
		return nil
	}
	if planID != "" {
		plan, err := h.Store.FindPlanByID(c, planID)
		if err != nil || plan == nil {
			response.Err(c, http.StatusNotFound, "PLAN_NOT_FOUND", "plan not found")
			return nil
		}
		if priceID != "" && plan.StripePriceID != priceID {
			response.BadRequest(c, "plan_id and new_price_id name different plans")
			return nil
		}
		return plan
	}
	plan, err := h.Store.FindPlanByStripePrice(c, priceID)
	if err != nil || plan == nil {
		response.Err(c, http.StatusNotFound, "PLAN_NOT_FOUND", "plan not found")
		return nil
	}
	return plan
}

// writePlanID is the entitlement switch: the licence (and the
// subscription row behind it) moves to the plan whose caps now apply.
// The write is loud on purpose (§77) — a failure is returned so the
// caller fails the request or lets Stripe retry the webhook, and the
// reconcile hook heals the row if the process dies in between. It is
// idempotent: rewriting the same plan id is a no-op.
func (h *StripeHandler) writePlanID(ctx context.Context, lic *model.License, plan *model.Plan) error {
	oldPlanID := lic.PlanID
	lic.PlanID = plan.ID
	if err := h.Store.UpdateLicense(ctx, lic, "plan_id"); err != nil {
		lic.PlanID = oldPlanID
		slog.Error("stripe: licence plan_id write failed; the subscription.updated webhook will reconcile it",
			"license_id", lic.ID, "plan_id", plan.ID, "error", err)
		return err
	}
	if subRecord, err := h.Store.FindSubscriptionByLicense(ctx, lic.ID); err == nil {
		subRecord.PlanID = plan.ID
		if err := h.Store.UpdateSubscription(ctx, subRecord, "plan_id"); err != nil {
			// Display layer; the licence row is authoritative and is
			// already moved. Loud, not fatal.
			slog.Error("stripe: subscription row plan_id write failed",
				"license_id", lic.ID, "plan_id", plan.ID, "error", err)
		}
	}
	return nil
}

// planChangedEffects is the outside world's notice that a licence moved
// plans: an audit line and the merchant's plan.changed webhook. The
// applied-marker is claimed first so the double delivery Stripe sends
// at renewal collapses into one set.
func (h *StripeHandler) planChangedEffects(ctx context.Context, lic *model.License, oldPlanID string, target *model.Plan, tag, timing, reason string) {
	if h.Store.HasNotification(ctx, lic.ID, tag) {
		return
	}
	h.Store.RecordNotification(ctx, lic.ID, tag)
	changes := map[string]any{
		"old_plan_id": oldPlanID, "new_plan_id": target.ID, "timing": timing,
	}
	if reason != "" {
		changes["reason"] = reason
	}
	h.Store.Audit(ctx, &model.AuditLog{
		Entity: "license", EntityID: lic.ID, Action: "plan_changed",
		ActorType: "webhook", Changes: changes,
	})
	if h.WebhookSvc != nil {
		h.WebhookSvc.Dispatch(ctx, lic.ProductID, "plan.changed", map[string]any{
			"license_id": lic.ID, "old_plan_id": oldPlanID,
			"new_plan_id": target.ID, "timing": timing,
		})
	}
}

// changePlanImmediate moves the licence now: Stripe switches the item's
// price and invoices the prorated difference (always_invoice), the
// licence follows in the same request, and any scheduled change is
// dropped in the same Stripe call (§77/§78, and "a second ChangePlan
// call replaces the pending one" — an immediate change is last).
func (h *StripeHandler) changePlanImmediate(c *gin.Context, lic *model.License, sub *stripe.Subscription, target *model.Plan, direction, proration string) {
	item := subFirstItem(sub)
	if item == nil || item.ID == "" {
		response.Internal(c, fmt.Errorf("stripe subscription %s has no items", sub.ID))
		return
	}
	params := &stripe.SubscriptionParams{
		ProrationBehavior: stripe.String(proration),
		Items: []*stripe.SubscriptionItemsParams{{
			ID:    stripe.String(item.ID),
			Price: stripe.String(target.StripePriceID),
		}},
		// Last wins: the immediate change replaces any scheduled one,
		// atomically with the price switch on Stripe's side.
		Metadata: map[string]string{metaPendingPlanID: "", metaPendingChangeAt: ""},
	}
	params.AddExpand("latest_invoice")
	updated, err := subscription.Update(sub.ID, params)
	if err != nil {
		response.Internal(c, err)
		return
	}

	oldPlanID := lic.PlanID
	// Stripe has already switched what the customer is billed; the
	// entitlements must follow NOW (§77). A failure here is a failed
	// request, never a silent success on the old caps — and the
	// reconcile hook converges the row on the next subscription event.
	if err := h.writePlanID(c, lic, target); err != nil {
		response.Internal(c, err)
		return
	}

	h.Store.Audit(c, &model.AuditLog{
		Entity: "license", EntityID: lic.ID, Action: "plan_changed",
		ActorType: "user",
		Changes: map[string]any{
			"old_plan_id": oldPlanID, "new_plan_id": target.ID,
			"timing": changeTimingImmediate, "direction": direction, "proration": proration,
		},
	})
	if h.WebhookSvc != nil {
		h.WebhookSvc.Dispatch(c, lic.ProductID, "plan.changed", map[string]any{
			"license_id": lic.ID, "old_plan_id": oldPlanID,
			"new_plan_id": target.ID, "timing": changeTimingImmediate,
		})
	}

	out := gin.H{
		"status":            "plan_changed",
		"timing":            changeTimingImmediate,
		"direction":         direction,
		"old_plan_id":       oldPlanID,
		"new_plan_id":       target.ID,
		"new_plan_name":     target.Name,
		"proration":         proration,
		"proration_invoice": nil,
	}
	// The proration invoice exists only when one was asked for; with
	// proration off the subscription's latest invoice is an old one and
	// reporting it would be a lie.
	if proration == prorationAlwaysInvoice {
		out["proration_invoice"] = prorationInvoiceJSON(updated)
	}
	response.OK(c, out)
}

// changePlanDeferred records the change as an intent on the Stripe
// subscription and stops there: the licence keeps the current plan's
// caps — and the current billing — until the renewal webhook path
// executes the switch at the period end (applyPlanIntent below).
func (h *StripeHandler) changePlanDeferred(c *gin.Context, lic *model.License, sub *stripe.Subscription, target *model.Plan, direction string) {
	changeAt := subPeriodEnd(sub)
	if changeAt == 0 {
		response.BadRequest(c, "cannot schedule a plan change: the subscription has no billing period end")
		return
	}
	_, replaced := parsePendingPlanChange(sub.Metadata)
	if _, err := subscription.Update(sub.ID, &stripe.SubscriptionParams{
		Metadata: map[string]string{
			metaPendingPlanID:   target.ID,
			metaPendingChangeAt: strconv.FormatInt(changeAt, 10),
		},
	}); err != nil {
		response.Internal(c, err)
		return
	}

	h.Store.Audit(c, &model.AuditLog{
		Entity: "license", EntityID: lic.ID, Action: "plan_change_scheduled",
		ActorType: "user",
		Changes: map[string]any{
			"old_plan_id": lic.PlanID, "new_plan_id": target.ID,
			"effective_at": changeAt, "replaced_pending": replaced, "direction": direction,
		},
	})

	out := gin.H{
		"status":            "plan_change_scheduled",
		"timing":            changeTimingNextPeriod,
		"direction":         direction,
		"old_plan_id":       lic.PlanID,
		"new_plan_id":       target.ID,
		"new_plan_name":     target.Name,
		"proration":         prorationNone,
		"proration_invoice": nil,
		"effective_at":      time.Unix(changeAt, 0).UTC().Format(time.RFC3339),
		"replaced_pending":  replaced,
	}
	if subscriptionEndsThisPeriod(sub) {
		out["notice"] = "the subscription is set to end at the period end; the scheduled change will not take effect"
	}
	response.OK(c, out)
}

// applyPlanIntent is the renewal-path hook (plan.md §77): what a
// subscription's metadata says about a plan change, and what the
// customer is billed for, applied to the licence. It runs from
// syncFromCurrent — invoice.paid and customer.subscription.updated,
// acting on the subscription as read live — and every step is safe to
// repeat, so an error can simply be returned for Stripe to retry.
func (h *StripeHandler) applyPlanIntent(ctx context.Context, lic *model.License, sub *stripe.Subscription) error {
	if lic == nil || sub == nil || sub.ID == "" || sub.ID != lic.StripeSubscriptionID {
		return nil
	}
	pending, ok := parsePendingPlanChange(sub.Metadata)
	if !ok {
		return h.reconcilePlanFromBilledPrice(ctx, lic, sub)
	}
	if pending.PlanID == lic.PlanID {
		// Already on the target plan (the switch happened and only the
		// intent lingered): the intent is spent. Drop it quietly.
		return h.clearPendingIntent(ctx, sub)
	}
	if !pendingChangeDue(pending, time.Now().Unix(), subPeriodEnd(sub)) {
		return nil // still this period's plan; caps unchanged
	}
	return h.executePendingPlanChange(ctx, lic, sub, pending)
}

// executePendingPlanChange performs a due deferred change at the period
// boundary with proration_behavior="none" (§78: nothing to prorate on a
// period switch) and clears the intent in the same Stripe call — the
// atomic "done" marker. The local applied-marker, written only after
// both the Stripe switch and the licence write, collapses the double
// delivery Stripe sends at renewal into one switch's side effects.
func (h *StripeHandler) executePendingPlanChange(ctx context.Context, lic *model.License, sub *stripe.Subscription, pending pendingPlanChange) error {
	target, err := h.Store.FindPlanByID(ctx, pending.PlanID)
	if errors.Is(err, sql.ErrNoRows) {
		return h.discardPendingIntent(ctx, lic, sub, pending, "the target plan no longer exists")
	}
	if err != nil {
		return err
	}
	if target.ProductID != lic.ProductID || target.LicenseType != "subscription" || target.StripePriceID == "" {
		return h.discardPendingIntent(ctx, lic, sub, pending,
			"the target plan is no longer a subscription plan of this product")
	}
	item := subFirstItem(sub)
	if item == nil || item.ID == "" {
		return h.discardPendingIntent(ctx, lic, sub, pending, "the subscription has no item to switch")
	}
	tag := fmt.Sprintf("plan_change_applied:%s:%d", pending.PlanID, pending.ChangeAt)
	if h.Store.HasNotification(ctx, lic.ID, tag) {
		return nil // already applied — this is a replay on a stale read
	}

	if _, err := subscription.Update(sub.ID, &stripe.SubscriptionParams{
		ProrationBehavior: stripe.String(prorationNone),
		Items: []*stripe.SubscriptionItemsParams{{
			ID:    stripe.String(item.ID),
			Price: stripe.String(target.StripePriceID),
		}},
		Metadata: map[string]string{metaPendingPlanID: "", metaPendingChangeAt: ""},
	}); err != nil {
		return fmt.Errorf("apply scheduled plan change for license %s: %w", lic.ID, err)
	}
	oldPlanID := lic.PlanID
	if err := h.writePlanID(ctx, lic, target); err != nil {
		return err // Stripe is switched and idempotent; the retry re-writes locally
	}
	h.planChangedEffects(ctx, lic, oldPlanID, target, tag, changeTimingNextPeriod, "scheduled_change")
	slog.Info("stripe: scheduled plan change applied",
		"license_id", lic.ID, "old_plan_id", oldPlanID, "new_plan_id", target.ID)
	return nil
}

// discardPendingIntent drops an intent that can never be executed
// (its plan was deleted, retyped, or the subscription lost its item)
// and says so: a quiet disappearance would leave the customer waiting
// for a change that cannot come.
func (h *StripeHandler) discardPendingIntent(ctx context.Context, lic *model.License, sub *stripe.Subscription, pending pendingPlanChange, reason string) error {
	if err := h.clearPendingIntent(ctx, sub); err != nil {
		return err
	}
	slog.Warn("stripe: scheduled plan change discarded",
		"license_id", lic.ID, "pending_plan_id", pending.PlanID, "reason", reason)
	h.Store.Audit(ctx, &model.AuditLog{
		Entity: "license", EntityID: lic.ID, Action: "plan_change_discarded",
		ActorType: "webhook",
		Changes:   map[string]any{"pending_plan_id": pending.PlanID, "reason": reason, "provider": "stripe"},
	})
	return nil
}

// clearPendingIntent deletes the intent from the subscription's
// metadata (an empty value deletes the key). Idempotent.
func (h *StripeHandler) clearPendingIntent(ctx context.Context, sub *stripe.Subscription) error {
	if _, err := subscription.Update(sub.ID, &stripe.SubscriptionParams{
		Metadata: map[string]string{metaPendingPlanID: "", metaPendingChangeAt: ""},
	}); err != nil {
		return fmt.Errorf("clear scheduled plan change for subscription %s: %w", sub.ID, err)
	}
	return nil
}

// reconcilePlanFromBilledPrice keeps a Stripe-billed licence on the
// plan the customer is actually billed for: whenever the item's price
// maps to a plan and the licence sits on another one, the licence
// follows the billing. This is the §77 safety net — entitlements never
// keep granting a plan the customer is no longer on — and the heal for
// a plan write that failed between the Stripe switch and this hook.
//
// Scope: only prices that map to a plan of the same product, of
// subscription type. An unmapped price is no evidence and is left
// alone; the admin endpoint's doctrine (a Stripe-billed licence's plan
// is Stripe's to change) is what makes this the right direction.
func (h *StripeHandler) reconcilePlanFromBilledPrice(ctx context.Context, lic *model.License, sub *stripe.Subscription) error {
	item := subFirstItem(sub)
	if item == nil || item.Price == nil || item.Price.ID == "" {
		return nil // no evidence of what is billed: leave the licence alone
	}
	derived, err := h.planForPrice(ctx, item.Price.ID)
	if err != nil {
		return err
	}
	if derived == nil || derived.ID == lic.PlanID {
		return nil
	}
	if derived.ProductID != lic.ProductID || derived.LicenseType != "subscription" || derived.StripePriceID != item.Price.ID {
		return nil
	}
	oldPlanID := lic.PlanID
	if err := h.writePlanID(ctx, lic, derived); err != nil {
		return err
	}
	tag := fmt.Sprintf("plan_change_reconciled:%s:%d", derived.ID, subPeriodEnd(sub))
	h.planChangedEffects(ctx, lic, oldPlanID, derived, tag, changeTimingImmediate, "billed_price_reconciled")
	slog.Warn("stripe: licence plan reconciled to the billed price",
		"license_id", lic.ID, "old_plan_id", oldPlanID, "new_plan_id", derived.ID,
		"price_id", item.Price.ID)
	return nil
}
