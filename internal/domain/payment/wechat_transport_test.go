package payment

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

func TestWechatClientUsesConfiguredHTTPClient(t *testing.T) {
	f := newWechatMessageFixture(t, false)
	stopped := errors.New("controlled local transport")
	calls := 0
	f.cfg.HTTPClient = &http.Client{Transport: paymentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, stopped
	})}
	client, err := NewWechatClient(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Never let a missing injection contact the actual payment gateway.
	if client.httpClient != f.cfg.HTTPClient {
		t.Fatal("configured HTTP client was ignored; refusing to call the live gateway")
	}
	_, err = client.Create(context.Background(), PaymentOrder{OrderNo: "BP_LOCAL_TRANSPORT", PayAmount: "10.0000", TradeType: TradeTypeNative})
	if !errors.Is(err, stopped) || calls != 1 {
		t.Fatalf("create bypassed the configured transport: err=%v calls=%d", err, calls)
	}
}
