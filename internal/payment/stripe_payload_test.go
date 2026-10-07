package payment

import (
	"encoding/json"
	"testing"
)

// Webhook payload shapes differ by the endpoint's API version. Both
// must resolve to the same subscription and period end.
func TestInvoiceEvent_SubscriptionID_BothShapes(t *testing.T) {
	legacy := []byte(`{"id":"in_1","subscription":"sub_legacy","period_end":1700000000}`)
	basil := []byte(`{"id":"in_1","period_end":1700000000,"parent":{"type":"subscription_details","subscription_details":{"subscription":"sub_basil"}}}`)
	oneOff := []byte(`{"id":"in_1","period_end":1700000000,"parent":null}`)

	var e invoiceEvent
	if json.Unmarshal(legacy, &e) != nil || e.SubscriptionID() != "sub_legacy" {
		t.Fatalf("legacy shape: got %q", e.SubscriptionID())
	}
	e = invoiceEvent{}
	if json.Unmarshal(basil, &e) != nil || e.SubscriptionID() != "sub_basil" {
		t.Fatalf("basil shape: got %q", e.SubscriptionID())
	}
	e = invoiceEvent{}
	if json.Unmarshal(oneOff, &e) != nil || e.SubscriptionID() != "" {
		t.Fatalf("one-off invoice: got %q, want empty", e.SubscriptionID())
	}
}

func TestSubscriptionEvent_PeriodEnd_BothShapes(t *testing.T) {
	legacy := []byte(`{"id":"sub_1","status":"active","current_period_end":1700000000,"items":{"data":[{"current_period_end":1600000000}]}}`)
	basil := []byte(`{"id":"sub_1","status":"active","items":{"data":[{"current_period_end":1700000000},{"current_period_end":1700005000}]}}`)
	none := []byte(`{"id":"sub_1","status":"canceled","items":{"data":[]}}`)

	var e subscriptionEvent
	if json.Unmarshal(legacy, &e) != nil || e.PeriodEnd() != 1700000000 {
		t.Fatalf("legacy shape: got %d", e.PeriodEnd())
	}
	e = subscriptionEvent{}
	if json.Unmarshal(basil, &e) != nil || e.PeriodEnd() != 1700005000 {
		t.Fatalf("basil shape: got %d, want latest item period end", e.PeriodEnd())
	}
	e = subscriptionEvent{}
	if json.Unmarshal(none, &e) != nil || e.PeriodEnd() != 0 {
		t.Fatalf("no period: got %d, want 0", e.PeriodEnd())
	}
}

// ServiceEnd reads the billing period from the lines that bill the
// subscription — its recurring charge and prorations, in either API
// shape — and ignores other invoice items, whose service period (say a
// year of support) is no licence period. Shapes as Stripe sends them.
func TestInvoiceServiceEnd(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int64
	}{
		{"legacy: subscription line, one-off year ignored", `{"period_end":100,"lines":{"data":[
			{"type":"invoiceitem","proration":false,"period":{"end":36500}},
			{"type":"subscription","period":{"end":3100}}]}}`, 3100},
		{"basil: subscription_item_details, one-off year ignored", `{"period_end":100,"lines":{"data":[
			{"parent":{"type":"invoice_item_details","invoice_item_details":{"proration":false}},"period":{"end":36500}},
			{"parent":{"type":"subscription_item_details"},"period":{"end":3100}}]}}`, 3100},
		{"legacy proration lines (type invoiceitem)", `{"period_end":100,"lines":{"data":[
			{"type":"invoiceitem","proration":true,"period":{"end":3100}}]}}`, 3100},
		{"pending proration invoice item (basil)", `{"period_end":100,"lines":{"data":[
			{"parent":{"type":"invoice_item_details","invoice_item_details":{"proration":true}},"period":{"end":3100}}]}}`, 3100},
		{"yearly → monthly: the unused-year credit is not paid service", `{"period_end":100,"lines":{"data":[
			{"amount":-9000,"type":"invoiceitem","proration":true,"parent":{"type":"subscription_item_details"},"period":{"end":36500}},
			{"amount":900,"type":"subscription","parent":{"type":"subscription_item_details"},"period":{"end":3100}}]}}`, 3100},
		{"a zero line (100% coupon) still counts", `{"period_end":100,"lines":{"data":[
			{"amount":0,"type":"subscription","period":{"end":3100}}]}}`, 3100},
		{"only one-off items: the invoice period_end", `{"period_end":100,"lines":{"data":[
			{"type":"invoiceitem","period":{"end":36500}}]}}`, 100},
		{"no lines: the invoice period_end", `{"period_end":100}`, 100},
		{"nothing set", `{}`, 0},
	} {
		var e invoiceEvent
		if err := json.Unmarshal([]byte(tc.body), &e); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := e.ServiceEnd(); got != tc.want {
			t.Errorf("%s: ServiceEnd = %d, want %d", tc.name, got, tc.want)
		}
	}
}
