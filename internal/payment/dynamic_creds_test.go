package payment

// Dynamic-credential rotation tests (config-in-DB, plan §67): the
// *Dynamic constructors read credentials through a getter on every
// call, so an admin editing gateway credentials in the database takes
// effect without a restart. Enabled() must track the CURRENT
// credentials, never a boot-time snapshot.

import (
	"context"
	"testing"

	"github.com/hitechcloud-vietnam/hitechcloud_keygate_licensing/internal/config"
)

func TestPay2SDynamicCredsRotate(t *testing.T) {
	full := config.Pay2SConfig{
		PartnerCode:  "PC",
		PartnerName:  "HiTechCloud",
		AccessKey:    "AK",
		SecretKey:    "SK",
		BankAccounts: "970422|92568686|NGUYEN VAN A|MB Bank",
		BaseURL:      "https://sandbox-payment.pay2s.vn",
	}
	current := full
	p := NewPay2SDynamic(func(context.Context) (config.Pay2SConfig, error) { return current, nil })
	if !p.Enabled() {
		t.Fatal("full creds via getter must be enabled")
	}
	current = config.Pay2SConfig{}
	if p.Enabled() {
		t.Fatal("Enabled must flip to false when creds are cleared")
	}
	current = full
	if !p.Enabled() {
		t.Fatal("Enabled must flip back when creds return")
	}
}

func TestZaloPayDynamicCredsRotate(t *testing.T) {
	full := config.ZaloPayConfig{
		AppID:       "2553",
		Key1:        "key1",
		CallbackKey: "cb",
		BaseURL:     "https://sb-openapi.zalopay.vn",
	}
	current := full
	p := NewZaloPayDynamic(func(context.Context) (config.ZaloPayConfig, error) { return current, nil })
	if !p.Enabled() {
		t.Fatal("full creds via getter must be enabled")
	}
	current = config.ZaloPayConfig{}
	if p.Enabled() {
		t.Fatal("Enabled must flip to false when creds are cleared")
	}
	current = full
	if !p.Enabled() {
		t.Fatal("Enabled must flip back when creds return")
	}
}

func TestPayOSDynamicCredsRotate(t *testing.T) {
	full := config.PayOSConfig{
		ClientID:    "cid",
		APIKey:      "key",
		ChecksumKey: "sum",
		BaseURL:     "https://api-merchant.payos.vn",
	}
	current := full
	p := NewPayOSDynamic(func(context.Context) (config.PayOSConfig, error) { return current, nil })
	if !p.Enabled() {
		t.Fatal("full creds via getter must be enabled")
	}
	current = config.PayOSConfig{}
	if p.Enabled() {
		t.Fatal("Enabled must flip to false when creds are cleared")
	}
	current = full
	if !p.Enabled() {
		t.Fatal("Enabled must flip back when creds return")
	}
}
