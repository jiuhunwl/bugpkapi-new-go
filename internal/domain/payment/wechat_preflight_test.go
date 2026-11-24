package payment

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"bugpk-api/internal/pkg/testdb"
)

func TestWechatMalformedCreateInputHasNoPersistenceSideEffects(t *testing.T) {
	cases := []struct {
		name   string
		change func(*WechatConfig, *OrderSeed)
	}{
		{"whitespace JSAPI payer", func(_ *WechatConfig, s *OrderSeed) { s.TradeType = TradeTypeJSAPI; s.OpenID = " \t " }},
		{"oversized MINI payer", func(_ *WechatConfig, s *OrderSeed) { s.TradeType = TradeTypeMini; s.OpenID = strings.Repeat("x", 129) }},
		{"missing JSAPI app", func(c *WechatConfig, s *OrderSeed) { s.TradeType = TradeTypeJSAPI; c.AppID, c.OAAppID = "", "" }},
		{"missing MINI app", func(c *WechatConfig, s *OrderSeed) { s.TradeType = TradeTypeMini; c.AppID, c.MiniAppID = "", "" }},
		{"service merchant without sub merchant", func(c *WechatConfig, _ *OrderSeed) { c.SpMchid = "1900000001" }},
		{"sub merchant without service merchant", func(c *WechatConfig, _ *OrderSeed) { c.SubMchid = "1900000002" }},
		{"partner missing app", func(c *WechatConfig, _ *OrderSeed) { c.SpMchid, c.SubMchid = "1900000001", "1900000002" }},
		{"H5 hostname", func(_ *WechatConfig, s *OrderSeed) { s.TradeType = TradeTypeH5; s.ClientIP = "client.example" }},
		{"H5 IP with port", func(_ *WechatConfig, s *OrderSeed) { s.TradeType = TradeTypeH5; s.ClientIP = "203.0.113.42:4321" }},
		{"H5 unspecified IPv4", func(_ *WechatConfig, s *OrderSeed) { s.TradeType = TradeTypeH5; s.ClientIP = "0.0.0.0" }},
		{"H5 unspecified IPv6", func(_ *WechatConfig, s *OrderSeed) { s.TradeType = TradeTypeH5; s.ClientIP = "::" }},
		{"H5 multicast IPv4", func(_ *WechatConfig, s *OrderSeed) { s.TradeType = TradeTypeH5; s.ClientIP = "224.0.0.1" }},
		{"H5 multicast IPv6", func(_ *WechatConfig, s *OrderSeed) { s.TradeType = TradeTypeH5; s.ClientIP = "ff02::1" }},
		{"zero amount", func(_ *WechatConfig, s *OrderSeed) { s.PayAmount = "0.0000" }},
		{"negative amount", func(_ *WechatConfig, s *OrderSeed) { s.PayAmount = "-1.0000" }},
		{"fractional cent", func(_ *WechatConfig, s *OrderSeed) { s.PayAmount = "10.0001" }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			s, config, _ := verifierConfigFixture(t)
			seed := OrderSeed{OrderNo: "BP_MALFORMED", OrderType: "RECHARGE", UserID: 1, PayChannel: ChannelWechat, PayAmount: "10.0000", TradeType: TradeTypeNative, OpenID: "fixture-payer", ClientIP: "203.0.113.42"}
			tt.change(&config.stored.WechatConfig, &seed)
			begins := 0
			s.repo = NewRepository(testdb.OpenStub(t, testdb.SQLStub{Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) {
				begins++
				return nil, errors.New("unexpected order persistence")
			}}))
			_, err := s.createOrderWechat(context.Background(), seed)
			if err == nil || begins != 0 {
				t.Fatalf("invalid create input reached persistence: err=%v begins=%d", err, begins)
			}
		})
	}
}

func TestWechatCreateAmountMustBeRepresentableInWholeCents(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  int64
		valid bool
	}{
		{"0.01", 1, true}, {"0.0100", 1, true}, {"10.0000", 1000, true}, {"10.0100", 1001, true}, {"99999999.9900", 9999999999, true},
		{"0.0101", 0, false}, {"0.0150", 0, false}, {"0.0199", 0, false}, {"10.0001", 0, false}, {"99999999.9999", 0, false},
	} {
		t.Run(tt.value, func(t *testing.T) {
			got, err := amountToFen(tt.value)
			if (err == nil) != tt.valid || (tt.valid && got != tt.want) {
				t.Fatalf("price was rounded instead of exactly converted: got=%d err=%v", got, err)
			}
		})
	}
}
