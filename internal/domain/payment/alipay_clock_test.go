package payment

import (
	"context"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testhost"
)

func TestMain(m *testing.M) { testhost.Main(m) }
func TestAlipayRequestTimestampUsesBusinessClockAcrossHosts(t *testing.T) {
	testhost.Run(t, "TestAlipayRequestClockChild")
}

func TestAlipayRequestClockChild(t *testing.T) {
	if !testhost.IsChild() {
		t.Skip("run by host harness")
	}
	client := newTestAlipayClient(t)
	before := time.Now()
	params, err := client.commonParams("alipay.trade.page.pay", `{"out_trade_no":"clock-test"}`, false, false)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", params.Get("timestamp"), time.FixedZone("expected +08", 8*60*60))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Before(before.Add(-time.Second)) || parsed.After(time.Now().Add(time.Second)) {
		t.Errorf("outgoing timestamp is not the current business instant: %s", params.Get("timestamp"))
	}
	if err := client.verify(params, params.Get("sign")); err != nil {
		t.Fatalf("generated signature invalid: %v", err)
	}
	params.Set("timestamp", "2000-01-01 00:00:00")
	if err := client.verify(params, params.Get("sign")); err == nil {
		t.Fatal("timestamp was not protected by signature")
	}
	form, err := client.Create(context.Background(), PaymentOrder{OrderNo: "BP_CLOCK_TEST", Product: "PAGE", Subject: "clock test", PayAmount: "1.00"})
	if err != nil || form.PayForm == "" {
		t.Fatalf("local PAGE form generation failed: %v", err)
	}
}
