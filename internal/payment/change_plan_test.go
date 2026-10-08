package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v82"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/model"
	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/store"
)

// ─── pure: upgrade/downgrade rules (plan.md §77/§78) ───

// The comparison must be integer minor units (§51/§78). 2^53+1 and
// 2^53+3 are EQUAL as float64 — a float comparison would call this
// move upgrade-safe instead of the downgrade it is.
func TestClassifyPlanChange_IntegerMinorUnits(t *testing.T) {
	big := int64(1<<53 + 1)
	bigPlus := int64(1<<53 + 3)
	amount := func(v int64) *int64 { return &v }

	for _, tc := range []struct {
		name        string
		cur         *int64
		curCurrency string
		tgt         *int64
		tgtCurrency string
		want        string
	}{
		{"equal price is upgrade-safe", amount(5000), "usd", amount(5000), "usd", changeDirectionUpgrade},
		{"more expensive target is an upgrade", amount(2000), "usd", amount(5000), "usd", changeDirectionUpgrade},
		{"cheaper target is a downgrade", amount(5000), "usd", amount(2000), "usd", changeDirectionDowngrade},
		{"beyond float precision", amount(bigPlus), "usd", amount(big), "usd", changeDirectionDowngrade},
		{"current price unknown", nil, "", amount(5000), "usd", changeDirectionUnknown},
		{"target price unknown", amount(5000), "usd", nil, "usd", changeDirectionUnknown},
		{"currency mismatch", amount(5000), "usd", amount(2000), "eur", changeDirectionUnknown},
		{"currency comparison is folded", amount(5000), "USD", amount(2000), "usd", changeDirectionDowngrade},
		{"missing currency", amount(5000), "", amount(5000), "usd", changeDirectionUnknown},
		{"free target", amount(5000), "usd", amount(0), "usd", changeDirectionDowngrade},
	} {
		if got := classifyPlanChange(tc.cur, tc.curCurrency, tc.tgt, tc.tgtCurrency); got != tc.want {
			t.Errorf("%s: classify = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestDefaultTimingFor(t *testing.T) {
	for direction, want := range map[string]string{
		changeDirectionUpgrade:   changeTimingImmediate,
		changeDirectionUnknown:   changeTimingImmediate,
		changeDirectionDowngrade: changeTimingNextPeriod,
	} {
		if got := defaultTimingFor(direction); got != want {
			t.Errorf("defaultTimingFor(%q) = %q, want %q", direction, got, want)
		}
	}
}

// A metered price's unit amount is not what anyone pays; comparing it
// would manufacture a "downgrade" out of thin air.
func TestPriceAmount_MeteredIsUnknown(t *testing.T) {
	if amt, _ := priceAmount(&stripe.Price{Currency: "usd", UnitAmount: 0,
		Recurring: &stripe.PriceRecurring{UsageType: "metered"}}); amt != nil {
		t.Fatalf("metered price compared as amount %d, want unknown", *amt)
	}
	if amt, _ := priceAmount(&stripe.Price{Currency: "usd", UnitAmount: 0}); amt == nil || *amt != 0 {
		t.Fatalf("a free price is a known 0, got %v", amt)
	}
	if amt, _ := priceAmount(nil); amt != nil {
		t.Fatal("no price must be unknown")
	}
}

func TestParsePendingPlanChange(t *testing.T) {
	if _, ok := parsePendingPlanChange(nil); ok {
		t.Fatal("no metadata parsed as an intent")
	}
	if _, ok := parsePendingPlanChange(map[string]string{metaPendingPlanID: ""}); ok {
		t.Fatal("a cleared key parsed as an intent")
	}
	p, ok := parsePendingPlanChange(map[string]string{
		metaPendingPlanID: "  plan_b ", metaPendingChangeAt: "1700000000",
	})
	if !ok || p.PlanID != "plan_b" || p.ChangeAt != 1700000000 {
		t.Fatalf("intent = %+v ok=%v", p, ok)
	}
	// A corrupt instant must not become "due now".
	p, ok = parsePendingPlanChange(map[string]string{
		metaPendingPlanID: "plan_b", metaPendingChangeAt: "soon",
	})
	if !ok || p.PlanID != "plan_b" || p.ChangeAt != 0 {
		t.Fatalf("garbage change_at = %+v ok=%v", p, ok)
	}
}

func TestPendingChangeDue(t *testing.T) {
	const at = int64(1700000000)
	for _, tc := range []struct {
		name           string
		changeAt       int64
		now, periodEnd int64
		want           bool
	}{
		{"still the old period", at, at - 1, at, false},
		{"clock reached the instant", at, at, at, true},
		{"after the instant", at, at + 5, at + 5, true},
		{"period rolled past the instant", at, at - 5, at + 1, true},
		{"no instant is never due (no mid-period guessing)", 0, at + 999, at + 999, false},
	} {
		got := pendingChangeDue(pendingPlanChange{PlanID: "p", ChangeAt: tc.changeAt}, tc.now, tc.periodEnd)
		if got != tc.want {
			t.Errorf("%s: due = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestSubFirstItemAndPeriodEnd(t *testing.T) {
	if subFirstItem(nil) != nil || subPeriodEnd(nil) != 0 {
		t.Fatal("nil subscription must read empty")
	}
	sub := &stripe.Subscription{Items: &stripe.SubscriptionItemList{Data: []*stripe.SubscriptionItem{
		nil,
		{ID: "si_1", CurrentPeriodEnd: 100},
		{ID: "si_2", CurrentPeriodEnd: 250},
	}}}
	if it := subFirstItem(sub); it == nil || it.ID != "si_1" {
		t.Fatalf("first item = %+v", it)
	}
	if end := subPeriodEnd(sub); end != 250 {
		t.Fatalf("period end = %d, want the latest item end 250", end)
	}
	if subItemPrice(sub) != nil {
		t.Fatal("items without prices must read as no price")
	}
}

// The proration amounts are Stripe's integer minor units, verbatim.
func TestProrationInvoiceJSONKeepsIntegerMinorUnits(t *testing.T) {
	out := prorationInvoiceJSON(&stripe.Subscription{LatestInvoice: &stripe.Invoice{
		ID: "in_1", AmountDue: 4321, AmountPaid: 4000, Currency: "usd",
		HostedInvoiceURL: "https://billing.stripe.com/invoices/in_1",
	}})
	for _, k := range []string{"amount_due", "amount_paid"} {
		if v, ok := out[k].(int64); !ok {
			t.Fatalf("%s is %T, want int64 minor units", k, out[k])
		} else if k == "amount_due" && v != 4321 {
			t.Fatalf("amount_due = %d, want 4321", v)
		}
	}
	if prorationInvoiceJSON(&stripe.Subscription{}) != nil {
		t.Fatal("no invoice must report null, not an empty object")
	}
}

// ─── integration (TEST_DATABASE_URL): the fake-Stripe harness ───

type planChangeFixture struct {
	s         *store.Store
	ctx       context.Context
	h         *StripeHandler
	oldPlan   *model.Plan
	newPlan   *model.Plan
	lic       *model.License
	subID     string
	oldPrice  string
	newPrice  string
	amounts   map[string]int64
	periodEnd int64
}

// newPlanChangeFixture seeds a product, two subscription plans whose
// Stripe prices differ, and a licence billed at oldAmount on the old
// plan's price (newAmount is the target).
func newPlanChangeFixture(t *testing.T, oldAmount, newAmount int64) *planChangeFixture {
	t.Helper()
	s, ctx := openStore(t)
	t.Cleanup(func() { _ = s.Close() })
	suffix := time.Now().Format("150405.000000")
	prod := &model.Product{Name: "ChangePlan " + suffix, Slug: "cp-" + suffix, Type: "saas"}
	if err := s.CreateProduct(ctx, prod); err != nil {
		t.Fatalf("product: %v", err)
	}
	oldPrice := "price_cp_old_" + suffix
	newPrice := "price_cp_new_" + suffix
	f := &planChangeFixture{
		s: s, ctx: ctx, h: &StripeHandler{Store: s},
		subID:     "sub_cp_" + suffix,
		oldPrice:  oldPrice,
		newPrice:  newPrice,
		amounts:   map[string]int64{oldPrice: oldAmount, newPrice: newAmount},
		periodEnd: time.Now().Add(30 * 24 * time.Hour).Truncate(time.Second).Unix(),
	}
	f.oldPlan = f.addPlan(t, "Old", oldPrice)
	f.newPlan = f.addPlan(t, "New", newPrice)
	f.lic = &model.License{
		ProductID: f.oldPlan.ProductID, PlanID: f.oldPlan.ID, Email: "cp-" + suffix + "@example.com",
		LicenseKey: "KEY-CP-" + suffix, Status: model.StatusActive,
		PaymentProvider: "stripe", StripeCustomerID: "cus_cp_" + suffix, StripeSubscriptionID: f.subID,
	}
	if err := s.CreateLicense(ctx, f.lic); err != nil {
		t.Fatalf("license: %v", err)
	}
	if err := store.SyncLicenseSubscriptionIn(ctx, s.DB, f.lic.ID, f.oldPlan, model.StatusActive, nil); err != nil {
		t.Fatalf("subscription row: %v", err)
	}
	return f
}

// addPlan creates one more plan of the fixture's product and prices it.
func (f *planChangeFixture) addPlan(t *testing.T, name, price string) *model.Plan {
	t.Helper()
	suffix := time.Now().Format("150405.000000") + "-" + name
	p := &model.Plan{
		ProductID: f.oldPlan.ProductID, Name: name, Slug: "cp-" + strings.ToLower(name) + "-" + suffix,
		LicenseType: "subscription", LicenseModel: "standard", BillingInterval: "month",
		StripePriceID: price, Active: true,
	}
	if err := f.s.CreatePlan(f.ctx, p); err != nil {
		t.Fatalf("plan %s: %v", name, err)
	}
	return p
}

// fakeSubState is the Stripe side of a subscription: its metadata, the
// price its item bills, and every update request that arrived.
type fakeSubState struct {
	mu            sync.Mutex
	meta          map[string]string
	priceNow      string
	updates       []url.Values
	updateInvoice string // returned as latest_invoice on updates
}

func (st *fakeSubState) updateForms() []url.Values {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]url.Values, len(st.updates))
	copy(out, st.updates)
	return out
}

func (f *planChangeFixture) subJSON(st *fakeSubState, withInvoice bool) string {
	meta := "{}"
	if len(st.meta) > 0 {
		if b, err := json.Marshal(st.meta); err == nil {
			meta = string(b)
		}
	}
	inv := ""
	if withInvoice && st.updateInvoice != "" {
		inv = fmt.Sprintf(`,"latest_invoice":%s`, st.updateInvoice)
	}
	return fmt.Sprintf(`{"id":%q,"object":"subscription","status":"active","metadata":%s,"cancel_at_period_end":false,
		"items":{"object":"list","data":[{"id":"si_cp_1","current_period_end":%d,
		"price":{"id":%q,"object":"price","unit_amount":%d,"currency":"usd"}}]}%s}`,
		f.subID, meta, f.periodEnd, st.priceNow, f.amounts[st.priceNow], inv)
}

// stubSubscription answers Stripe like the API would for this
// subscription: metadata values delete on empty, an items[0][price]
// switches the billed price, and every update is recorded.
func (f *planChangeFixture) stubSubscription(t *testing.T, st *fakeSubState) {
	t.Helper()
	if st.meta == nil {
		st.meta = map[string]string{}
	}
	if st.priceNow == "" {
		st.priceNow = f.oldPrice
	}
	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/subscriptions/"+f.subID:
			st.mu.Lock()
			defer st.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodPost {
				_ = r.ParseForm()
				st.updates = append(st.updates, r.PostForm)
				for _, k := range []string{metaPendingPlanID, metaPendingChangeAt} {
					if vs, ok := r.PostForm["metadata["+k+"]"]; ok {
						if vs[0] == "" {
							delete(st.meta, k)
						} else {
							st.meta[k] = vs[0]
						}
					}
				}
				if p := r.PostForm.Get("items[0][price]"); p != "" {
					st.priceNow = p
				}
				_, _ = io.WriteString(w, f.subJSON(st, true))
				return
			}
			_, _ = io.WriteString(w, f.subJSON(st, false))
		case strings.HasPrefix(r.URL.Path, "/v1/prices/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/prices/")
			amount, ok := f.amounts[id]
			if !ok {
				t.Errorf("unexpected price lookup %s", id)
				http.Error(w, `{"error":{"message":"unknown price"}}`, http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"id":%q,"object":"price","unit_amount":%d,"currency":"usd"}`, id, amount)
		default:
			t.Errorf("unexpected Stripe call %s %s", r.Method, r.URL.Path)
			http.Error(w, `{"error":{"message":"unexpected"}}`, http.StatusNotFound)
		}
	})
}

// stubNoStripe fails the test on any Stripe call — for the refusals
// that must happen before anything is fetched or written.
func stubNoStripe(t *testing.T) {
	t.Helper()
	stubStripe(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected Stripe call %s %s", r.Method, r.URL.Path)
		http.Error(w, `{"error":{"message":"unexpected"}}`, http.StatusNotFound)
	})
}

func (f *planChangeFixture) changePlan(t *testing.T, body string) (int, []byte) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/portal/subscription/change-plan", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("email", f.lic.Email)
	f.h.ChangePlan(c)
	return w.Code, w.Body.Bytes()
}

func decodeResp(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // money discipline: numbers must survive as integers
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("decode response %s: %v", raw, err)
	}
	return out
}

func respData(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	data, ok := decodeResp(t, raw)["data"].(map[string]any)
	if !ok {
		t.Fatalf("response has no data: %s", raw)
	}
	return data
}

func respErrCode(t *testing.T, raw []byte) string {
	t.Helper()
	errObj, ok := decodeResp(t, raw)["error"].(map[string]any)
	if !ok {
		t.Fatalf("response has no error: %s", raw)
	}
	code, _ := errObj["code"].(string)
	return code
}

// requireJSONInt is the integer-minor-unit pin: a money amount must
// arrive as a JSON integer, never a float.
func requireJSONInt(t *testing.T, v any, path string) int64 {
	t.Helper()
	n, ok := v.(json.Number)
	if !ok {
		t.Fatalf("%s: got %T (%v), want a JSON integer", path, v, v)
	}
	i, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil {
		t.Fatalf("%s: %q is not an integer minor-unit amount", path, n)
	}
	return i
}

func (f *planChangeFixture) reloadLicense(t *testing.T) *model.License {
	t.Helper()
	lic, err := f.s.FindLicenseByID(f.ctx, f.lic.ID)
	if err != nil {
		t.Fatalf("reload license: %v", err)
	}
	return lic
}

// planChangeAudits collects one kind of audit line on the licence.
func (f *planChangeFixture) planChangeAudits(t *testing.T, action string) []map[string]any {
	t.Helper()
	logs, _, err := f.s.ListAuditLogs(f.ctx, "license", f.lic.ID, "", 0, 100, store.Sort{})
	if err != nil {
		t.Fatalf("audit logs: %v", err)
	}
	var out []map[string]any
	for _, l := range logs {
		if l.Action == action {
			out = append(out, l.Changes)
		}
	}
	return out
}

// An upgrade moves immediately: always_invoice proration, the item's
// price switched, the licence (entitlements) following in the same
// request, and the proration invoice reported as Stripe computed it.
func TestChangePlan_UpgradeImmediateProratesAndInvoices(t *testing.T) {
	f := newPlanChangeFixture(t, 2000, 5000)
	st := &fakeSubState{
		updateInvoice: `{"id":"in_cp_1","object":"invoice","amount_due":4321,"amount_paid":4321,"currency":"usd","hosted_invoice_url":"https://billing.stripe.com/i/in_cp_1"}`,
	}
	f.stubSubscription(t, st)

	code, raw := f.changePlan(t, fmt.Sprintf(`{"license_id":%q,"plan_id":%q}`, f.lic.ID, f.newPlan.ID))
	if code != http.StatusOK {
		t.Fatalf("change plan = %d: %s", code, raw)
	}
	data := respData(t, raw)
	if data["status"] != "plan_changed" || data["timing"] != "immediate" || data["direction"] != "upgrade" {
		t.Fatalf("response = %v, want plan_changed/immediate/upgrade", data)
	}
	if data["old_plan_id"] != f.oldPlan.ID || data["new_plan_id"] != f.newPlan.ID || data["new_plan_name"] != f.newPlan.Name {
		t.Fatalf("plan ids = %v", data)
	}
	if data["proration"] != "always_invoice" {
		t.Fatalf("proration = %v, want always_invoice", data["proration"])
	}
	inv, ok := data["proration_invoice"].(map[string]any)
	if !ok {
		t.Fatalf("proration_invoice = %v, want the invoice Stripe returned", data["proration_invoice"])
	}
	if inv["id"] != "in_cp_1" || inv["currency"] != "usd" {
		t.Fatalf("proration invoice = %v", inv)
	}
	if got := requireJSONInt(t, inv["amount_due"], "proration_invoice.amount_due"); got != 4321 {
		t.Fatalf("amount_due = %d, want 4321 (Stripe's integer minor units, verbatim)", got)
	}
	requireJSONInt(t, inv["amount_paid"], "proration_invoice.amount_paid")

	// The request the fake Stripe received: prorated NOW, item switched,
	// any scheduled change replaced (metadata cleared in the same call).
	forms := st.updateForms()
	if len(forms) != 1 {
		t.Fatalf("%d Stripe updates, want 1", len(forms))
	}
	form := forms[0]
	if form.Get("proration_behavior") != "always_invoice" {
		t.Fatalf("proration_behavior = %q, want always_invoice", form.Get("proration_behavior"))
	}
	if form.Get("items[0][id]") != "si_cp_1" || form.Get("items[0][price]") != f.newPrice {
		t.Fatalf("item switch = %v", form)
	}
	if v, ok := form["metadata[pending_plan_id]"]; !ok || v[0] != "" {
		t.Fatalf("immediate change must clear any pending intent, form = %v", form)
	}

	// Entitlements follow NOW: the licence and the subscription row are
	// on the new plan's caps.
	if lic := f.reloadLicense(t); lic.PlanID != f.newPlan.ID {
		t.Fatalf("license plan = %s, want %s", lic.PlanID, f.newPlan.ID)
	}
	if subRow, err := f.s.FindSubscriptionByLicense(f.ctx, f.lic.ID); err != nil || subRow.PlanID != f.newPlan.ID {
		t.Fatalf("subscription row plan = %v (%v)", subRow, err)
	}
	if audits := f.planChangeAudits(t, "plan_changed"); len(audits) != 1 {
		t.Fatalf("%d plan_changed audit lines, want 1", len(audits))
	}
}

// A downgrade defaults to next_period (§77): a metadata intent on the
// Stripe subscription, no price switch and no proration now, and the
// licence keeps the old plan's caps until the change executes.
func TestChangePlan_DowngradeDefersToPeriodEndViaMetadata(t *testing.T) {
	f := newPlanChangeFixture(t, 5000, 2000)
	st := &fakeSubState{}
	f.stubSubscription(t, st)

	code, raw := f.changePlan(t, fmt.Sprintf(`{"license_id":%q,"plan_id":%q}`, f.lic.ID, f.newPlan.ID))
	if code != http.StatusOK {
		t.Fatalf("change plan = %d: %s", code, raw)
	}
	data := respData(t, raw)
	if data["status"] != "plan_change_scheduled" || data["timing"] != "next_period" || data["direction"] != "downgrade" {
		t.Fatalf("response = %v, want plan_change_scheduled/next_period/downgrade", data)
	}
	if data["proration"] != "none" || data["proration_invoice"] != nil {
		t.Fatalf("a deferred change must report no proration invoice: %v", data)
	}
	if want := time.Unix(f.periodEnd, 0).UTC().Format(time.RFC3339); data["effective_at"] != want {
		t.Fatalf("effective_at = %v, want %s", data["effective_at"], want)
	}
	if data["replaced_pending"] != false {
		t.Fatalf("replaced_pending = %v, want false (no intent was pending)", data["replaced_pending"])
	}

	forms := st.updateForms()
	if len(forms) != 1 {
		t.Fatalf("%d Stripe updates, want 1", len(forms))
	}
	form := forms[0]
	// The chosen mechanism: a metadata intent (pending_plan_id +
	// pending_change_at = current_period_end), nothing else.
	if form.Get("metadata["+metaPendingPlanID+"]") != f.newPlan.ID {
		t.Fatalf("pending_plan_id = %q, want %q", form.Get("metadata["+metaPendingPlanID+"]"), f.newPlan.ID)
	}
	if form.Get("metadata["+metaPendingChangeAt+"]") != strconv.FormatInt(f.periodEnd, 10) {
		t.Fatalf("pending_change_at = %q, want the period end %d", form.Get("metadata["+metaPendingChangeAt+"]"), f.periodEnd)
	}
	if _, switched := form["items[0][price]"]; switched {
		t.Fatalf("a deferred change must not touch the item: %v", form)
	}
	if form.Get("proration_behavior") != "" {
		t.Fatalf("a metadata-only update must not prorate: %v", form)
	}

	// No entitlement change before its time.
	if lic := f.reloadLicense(t); lic.PlanID != f.oldPlan.ID {
		t.Fatalf("license moved early: %s", lic.PlanID)
	}
	if audits := f.planChangeAudits(t, "plan_change_scheduled"); len(audits) != 1 {
		t.Fatalf("%d plan_change_scheduled audit lines, want 1", len(audits))
	}
	if audits := f.planChangeAudits(t, "plan_changed"); len(audits) != 0 {
		t.Fatalf("a scheduled change reported plan_changed early: %v", audits)
	}
}

// "Downgrade may happen immediately" (§77): timing=immediate on a
// cheaper plan switches at once with the unused-time credit invoiced.
func TestChangePlan_DowngradeImmediate(t *testing.T) {
	f := newPlanChangeFixture(t, 5000, 2000)
	st := &fakeSubState{updateInvoice: `{"id":"in_cp_down","object":"invoice","amount_due":0,"amount_paid":0,"currency":"usd"}`}
	f.stubSubscription(t, st)

	code, raw := f.changePlan(t, fmt.Sprintf(`{"license_id":%q,"plan_id":%q,"timing":"immediate"}`, f.lic.ID, f.newPlan.ID))
	if code != http.StatusOK {
		t.Fatalf("change plan = %d: %s", code, raw)
	}
	data := respData(t, raw)
	if data["status"] != "plan_changed" || data["timing"] != "immediate" || data["direction"] != "downgrade" {
		t.Fatalf("response = %v", data)
	}
	forms := st.updateForms()
	if len(forms) != 1 || forms[0].Get("proration_behavior") != "always_invoice" {
		t.Fatalf("immediate downgrade must invoice the proration: %v", forms)
	}
	if lic := f.reloadLicense(t); lic.PlanID != f.newPlan.ID {
		t.Fatalf("license plan = %s, want %s", lic.PlanID, f.newPlan.ID)
	}
}

// A scheduled upgrade is allowed (§77): timing=next_period defers an
// upgrade too, with no proration at execution.
func TestChangePlan_ScheduledUpgrade(t *testing.T) {
	f := newPlanChangeFixture(t, 2000, 5000)
	st := &fakeSubState{}
	f.stubSubscription(t, st)

	code, raw := f.changePlan(t, fmt.Sprintf(`{"license_id":%q,"plan_id":%q,"timing":"next_period"}`, f.lic.ID, f.newPlan.ID))
	if code != http.StatusOK {
		t.Fatalf("change plan = %d: %s", code, raw)
	}
	data := respData(t, raw)
	if data["status"] != "plan_change_scheduled" || data["direction"] != "upgrade" {
		t.Fatalf("response = %v", data)
	}
	forms := st.updateForms()
	if len(forms) != 1 || forms[0].Get("metadata["+metaPendingPlanID+"]") != f.newPlan.ID {
		t.Fatalf("scheduled upgrade must stamp the intent: %v", forms)
	}
	if lic := f.reloadLicense(t); lic.PlanID != f.oldPlan.ID {
		t.Fatalf("license moved early: %s", lic.PlanID)
	}
}

// The legacy body (new_price_id, prorate=false) keeps its old meaning:
// move immediately, without a proration.
func TestChangePlan_LegacyBodyStillMovesImmediately(t *testing.T) {
	f := newPlanChangeFixture(t, 5000, 2000) // a downgrade under the new defaults
	st := &fakeSubState{}
	f.stubSubscription(t, st)

	code, raw := f.changePlan(t, fmt.Sprintf(`{"license_id":%q,"new_price_id":%q,"prorate":false}`, f.lic.ID, f.newPrice))
	if code != http.StatusOK {
		t.Fatalf("change plan = %d: %s", code, raw)
	}
	data := respData(t, raw)
	if data["status"] != "plan_changed" || data["timing"] != "immediate" || data["proration"] != "none" {
		t.Fatalf("legacy call = %v, want immediate/none", data)
	}
	if data["proration_invoice"] != nil {
		t.Fatalf("prorate:false must report no proration invoice: %v", data)
	}
	forms := st.updateForms()
	if len(forms) != 1 || forms[0].Get("proration_behavior") != "none" {
		t.Fatalf("prorate:false must send proration_behavior=none: %v", forms)
	}
	if lic := f.reloadLicense(t); lic.PlanID != f.newPlan.ID {
		t.Fatalf("license plan = %s, want the legacy immediate move to %s", lic.PlanID, f.newPlan.ID)
	}
}

// A no-op call is refused with its own code before anything is touched.
func TestChangePlan_SamePlanUnchanged(t *testing.T) {
	f := newPlanChangeFixture(t, 2000, 5000)
	stubNoStripe(t)

	code, raw := f.changePlan(t, fmt.Sprintf(`{"license_id":%q,"plan_id":%q}`, f.lic.ID, f.oldPlan.ID))
	if code != http.StatusBadRequest || respErrCode(t, raw) != "PLAN_UNCHANGED" {
		t.Fatalf("same plan = %d %s, want 400 PLAN_UNCHANGED", code, raw)
	}
	code, raw = f.changePlan(t, fmt.Sprintf(`{"license_id":%q,"new_price_id":%q}`, f.lic.ID, f.oldPrice))
	if code != http.StatusBadRequest || respErrCode(t, raw) != "PLAN_UNCHANGED" {
		t.Fatalf("same plan by price = %d %s, want 400 PLAN_UNCHANGED", code, raw)
	}
}

func TestChangePlan_UnknownPlanNotFound(t *testing.T) {
	f := newPlanChangeFixture(t, 2000, 5000)
	stubNoStripe(t)

	for _, body := range []string{
		fmt.Sprintf(`{"license_id":%q,"plan_id":"pln_missing"}`, f.lic.ID),
		fmt.Sprintf(`{"license_id":%q,"new_price_id":"price_missing"}`, f.lic.ID),
	} {
		code, raw := f.changePlan(t, body)
		if code != http.StatusNotFound || respErrCode(t, raw) != "PLAN_NOT_FOUND" {
			t.Fatalf("unknown plan %s = %d %s, want 404 PLAN_NOT_FOUND", body, code, raw)
		}
	}
	// Two references that disagree are a bad request, not a missing plan.
	code, raw := f.changePlan(t, fmt.Sprintf(`{"license_id":%q,"plan_id":%q,"new_price_id":"price_other"}`, f.lic.ID, f.newPlan.ID))
	if code != http.StatusBadRequest || respErrCode(t, raw) != "BAD_REQUEST" {
		t.Fatalf("conflicting refs = %d %s, want 400 BAD_REQUEST", code, raw)
	}
}

// The refusals that say "this is not a subscription move at all".
func TestChangePlan_NonSubscriptionRefusals(t *testing.T) {
	f := newPlanChangeFixture(t, 2000, 5000)
	stubNoStripe(t)

	// (a) A licence with no Stripe subscription (a non-subscription
	// customer) cannot change plans here at all.
	plain := &model.License{
		ProductID: f.oldPlan.ProductID, PlanID: f.oldPlan.ID,
		Email: "cp-plain-" + f.lic.Email, LicenseKey: f.lic.LicenseKey + "-plain",
		Status: model.StatusActive, PaymentProvider: "stripe",
	}
	if err := f.s.CreateLicense(f.ctx, plain); err != nil {
		t.Fatalf("plain license: %v", err)
	}
	gin.SetMode(gin.TestMode)
	call := func(body string) (int, []byte) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/portal/subscription/change-plan", strings.NewReader(body))
		c.Request.Header.Set("Content-Type", "application/json")
		c.Set("email", plain.Email)
		f.h.ChangePlan(c)
		return w.Code, w.Body.Bytes()
	}
	code, raw := call(fmt.Sprintf(`{"license_id":%q,"plan_id":%q}`, plain.ID, f.newPlan.ID))
	if code != http.StatusBadRequest {
		t.Fatalf("no subscription = %d %s, want 400", code, raw)
	}

	// (b) A perpetual target plan is not subscription-billable.
	perp := f.addPlan(t, "Perp", "price_cp_perp_"+f.subID)
	if _, err := f.s.DB.NewUpdate().TableExpr("plans").Set("license_type = 'perpetual'").Where("id = ?", perp.ID).Exec(f.ctx); err != nil {
		t.Fatalf("make perpetual: %v", err)
	}
	code, raw = f.changePlan(t, fmt.Sprintf(`{"license_id":%q,"plan_id":%q}`, f.lic.ID, perp.ID))
	if code != http.StatusBadRequest || respErrCode(t, raw) != "NOT_SUBSCRIPTION_PLAN" {
		t.Fatalf("perpetual target = %d %s, want 400 NOT_SUBSCRIPTION_PLAN", code, raw)
	}

	// (c) An inactive target plan reopens a discontinued tier.
	off := f.addPlan(t, "Off", "price_cp_off_"+f.subID)
	if _, err := f.s.DB.NewUpdate().TableExpr("plans").Set("active = false").Where("id = ?", off.ID).Exec(f.ctx); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	code, raw = f.changePlan(t, fmt.Sprintf(`{"license_id":%q,"plan_id":%q}`, f.lic.ID, off.ID))
	if code != http.StatusBadRequest || respErrCode(t, raw) != "PLAN_INACTIVE" {
		t.Fatalf("inactive target = %d %s, want 400 PLAN_INACTIVE", code, raw)
	}

	// (d) An unknown timing is a bad request, before any Stripe call.
	code, raw = f.changePlan(t, fmt.Sprintf(`{"license_id":%q,"plan_id":%q,"timing":"later"}`, f.lic.ID, f.newPlan.ID))
	if code != http.StatusBadRequest {
		t.Fatalf("bad timing = %d %s, want 400", code, raw)
	}
}

// Requirement: a second ChangePlan call replaces the pending one —
// last wins.
func TestChangePlan_ReplacesPendingIntent(t *testing.T) {
	f := newPlanChangeFixture(t, 5000, 2000)
	other := f.addPlan(t, "Other", "price_cp_other_"+f.subID)
	f.amounts[other.StripePriceID] = 3000
	st := &fakeSubState{meta: map[string]string{
		metaPendingPlanID: other.ID, metaPendingChangeAt: "123",
	}}
	f.stubSubscription(t, st)

	code, raw := f.changePlan(t, fmt.Sprintf(`{"license_id":%q,"plan_id":%q}`, f.lic.ID, f.newPlan.ID))
	if code != http.StatusOK {
		t.Fatalf("change plan = %d: %s", code, raw)
	}
	data := respData(t, raw)
	if data["replaced_pending"] != true {
		t.Fatalf("replaced_pending = %v, want true", data["replaced_pending"])
	}
	forms := st.updateForms()
	if len(forms) != 1 || forms[0].Get("metadata["+metaPendingPlanID+"]") != f.newPlan.ID {
		t.Fatalf("the second call's target must win: %v", forms)
	}
}

// The execution hook: a due intent runs in the renewal webhook path
// (invoice.paid / customer.subscription.updated → syncFromCurrent),
// switches the price with proration none, clears the intent in the same
// Stripe call, moves the entitlements — and runs exactly once even
// though Stripe delivers BOTH events at renewal and replays carry a
// pre-execution snapshot.
func TestPendingPlanChange_ExecutesOnceAtRenewal(t *testing.T) {
	f := newPlanChangeFixture(t, 5000, 2000)
	dueAt := time.Now().Add(-time.Hour).Unix()
	st := &fakeSubState{meta: map[string]string{
		metaPendingPlanID: f.newPlan.ID, metaPendingChangeAt: strconv.FormatInt(dueAt, 10),
	}}
	f.stubSubscription(t, st)

	// Delivery 1: the renewal invoice.
	if err := f.h.onInvoicePaid(f.ctx, []byte(fmt.Sprintf(
		`{"subscription":%q,"amount_paid":2000,"period_end":%d}`, f.subID, time.Now().Unix()))); err != nil {
		t.Fatalf("invoice.paid: %v", err)
	}
	forms := st.updateForms()
	if len(forms) != 1 {
		t.Fatalf("%d Stripe updates after invoice.paid, want 1", len(forms))
	}
	form := forms[0]
	if form.Get("proration_behavior") != "none" {
		t.Fatalf("a period switch must not prorate: %v", form)
	}
	if form.Get("items[0][id]") != "si_cp_1" || form.Get("items[0][price]") != f.newPrice {
		t.Fatalf("execution must switch the item to %s: %v", f.newPrice, form)
	}
	if v, ok := form["metadata[pending_plan_id]"]; !ok || v[0] != "" {
		t.Fatalf("execution must clear the intent in the same call: %v", form)
	}
	if lic := f.reloadLicense(t); lic.PlanID != f.newPlan.ID {
		t.Fatalf("license plan = %s, want %s", lic.PlanID, f.newPlan.ID)
	}

	// Delivery 2: customer.subscription.updated, which Stripe sends for
	// the same renewal. The intent is gone and the billed price matches
	// — nothing more may happen.
	if err := f.h.onSubscriptionUpdated(f.ctx, []byte(fmt.Sprintf(
		`{"id":%q,"status":"active","current_period_end":%d}`, f.subID, f.periodEnd))); err != nil {
		t.Fatalf("subscription.updated: %v", err)
	}
	if got := len(st.updateForms()); got != 1 {
		t.Fatalf("%d Stripe updates after the paired delivery, want exactly 1", got)
	}

	// A replay holding the pre-execution snapshot (metadata still set)
	// is stopped by the applied-marker: idempotent execution.
	stale := &stripe.Subscription{
		ID:       f.subID,
		Metadata: map[string]string{metaPendingPlanID: f.newPlan.ID, metaPendingChangeAt: strconv.FormatInt(dueAt, 10)},
		Items: &stripe.SubscriptionItemList{Data: []*stripe.SubscriptionItem{
			{ID: "si_cp_1", Price: &stripe.Price{ID: f.oldPrice, Currency: "usd", UnitAmount: 5000}},
		}},
	}
	if err := f.h.applyPlanIntent(f.ctx, f.lic, stale); err != nil {
		t.Fatalf("stale replay: %v", err)
	}
	if got := len(st.updateForms()); got != 1 {
		t.Fatalf("%d Stripe updates after the stale replay, want exactly 1", got)
	}

	// One switch, one audit line — the outside world heard about it once.
	if audits := f.planChangeAudits(t, "plan_changed"); len(audits) != 1 {
		t.Fatalf("%d plan_changed audit lines, want exactly 1: %v", len(audits), audits)
	}
}

// An intent that is not due yet must not move the plan mid-period.
func TestPendingPlanChange_NotDueIsLeftAlone(t *testing.T) {
	f := newPlanChangeFixture(t, 5000, 2000)
	st := &fakeSubState{meta: map[string]string{
		metaPendingPlanID:   f.newPlan.ID,
		metaPendingChangeAt: strconv.FormatInt(f.periodEnd, 10), // == period end: not yet
	}}
	f.stubSubscription(t, st)

	if err := f.h.onInvoicePaid(f.ctx, []byte(fmt.Sprintf(
		`{"subscription":%q,"amount_paid":5000,"period_end":%d}`, f.subID, time.Now().Unix()))); err != nil {
		t.Fatalf("invoice.paid: %v", err)
	}
	if got := len(st.updateForms()); got != 0 {
		t.Fatalf("%d Stripe updates before the due instant, want 0", got)
	}
	if lic := f.reloadLicense(t); lic.PlanID != f.oldPlan.ID {
		t.Fatalf("license moved before the period end: %s", lic.PlanID)
	}
}

// Cancelling the subscription clears any pending intent — a plan change
// must not fire on a subscription that is ending (requirement 3).
func TestCancelSubscription_ClearsPendingIntent(t *testing.T) {
	f := newPlanChangeFixture(t, 5000, 2000)
	st := &fakeSubState{meta: map[string]string{
		metaPendingPlanID: f.newPlan.ID, metaPendingChangeAt: "123",
	}}
	f.stubSubscription(t, st)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/portal/subscription/cancel",
		strings.NewReader(fmt.Sprintf(`{"license_id":%q}`, f.lic.ID)))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("email", f.lic.Email)
	f.h.CancelSubscription(c)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel = %d: %s", w.Code, w.Body.String())
	}
	forms := st.updateForms()
	if len(forms) != 1 {
		t.Fatalf("%d Stripe updates, want 1", len(forms))
	}
	form := forms[0]
	if form.Get("cancel_at_period_end") != "true" {
		t.Fatalf("cancel form = %v", form)
	}
	for _, k := range []string{metaPendingPlanID, metaPendingChangeAt} {
		if v, ok := form["metadata["+k+"]"]; !ok || v[0] != "" {
			t.Fatalf("cancel must clear %s, form = %v", k, form)
		}
	}
	if _, ok := parsePendingPlanChange(st.meta); ok {
		t.Fatalf("intent survived the cancellation: %v", st.meta)
	}
}

// Entitlement safety (§77): a Stripe-billed licence whose plan drifted
// from the price it is billed for follows the billing in the renewal
// path — never over-trusting the old caps — and converges quietly.
func TestReconcile_LicenseFollowsBilledPrice(t *testing.T) {
	f := newPlanChangeFixture(t, 5000, 2000)
	// No intent anywhere; the customer is billed at the NEW plan's
	// price (say a plan write died between the Stripe switch and here).
	st := &fakeSubState{priceNow: f.newPrice}
	f.stubSubscription(t, st)

	send := func() {
		t.Helper()
		if err := f.h.onSubscriptionUpdated(f.ctx, []byte(fmt.Sprintf(
			`{"id":%q,"status":"active","current_period_end":%d}`, f.subID, f.periodEnd))); err != nil {
			t.Fatalf("subscription.updated: %v", err)
		}
	}
	send()
	if lic := f.reloadLicense(t); lic.PlanID != f.newPlan.ID {
		t.Fatalf("license plan = %s, want the billed plan %s", lic.PlanID, f.newPlan.ID)
	}
	if got := len(st.updateForms()); got != 0 {
		t.Fatalf("reconciliation is a local heal, not a Stripe write: %d updates", got)
	}
	if audits := f.planChangeAudits(t, "plan_changed"); len(audits) != 1 {
		t.Fatalf("%d plan_changed audit lines, want 1", len(audits))
	}

	// Converged: the next event is a no-op.
	send()
	if audits := f.planChangeAudits(t, "plan_changed"); len(audits) != 1 {
		t.Fatalf("converged licence re-reported: %d audit lines", len(audits))
	}
}
