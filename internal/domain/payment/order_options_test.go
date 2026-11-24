package payment

import "testing"

func TestNormalizeOnlineOrderRequestKeepsChannelProductContracts(t *testing.T) {
	cases := []struct {
		name, channel, product, trade, wantProduct, wantTrade string
		valid                                                 bool
	}{
		{"wechat default", ChannelWechat, "", "", TradeTypeNative, TradeTypeNative, true},
		{"wechat native", ChannelWechat, " native ", "", TradeTypeNative, TradeTypeNative, true},
		{"wechat h5", ChannelWechat, "h5", " H5 ", TradeTypeH5, TradeTypeH5, true},
		{"wechat jsapi", ChannelWechat, "", TradeTypeJSAPI, TradeTypeJSAPI, TradeTypeJSAPI, true},
		{"wechat mini", ChannelWechat, TradeTypeMini, "", TradeTypeMini, TradeTypeMini, true},
		{"wechat app", ChannelWechat, TradeTypeAPP, TradeTypeAPP, TradeTypeAPP, TradeTypeAPP, true},
		{"legacy page explicit mini", ChannelWechat, "PAGE", TradeTypeMini, TradeTypeMini, TradeTypeMini, true},
		{"legacy wap default", ChannelWechat, "WAP", "", TradeTypeNative, TradeTypeNative, true},
		{"legacy face explicit app", ChannelWechat, "FACE", TradeTypeAPP, TradeTypeAPP, TradeTypeAPP, true},
		{"hupi default", ChannelHupijiaoWechat, "", "", TradeTypeNative, TradeTypeNative, true},
		{"hupi native", ChannelHupijiaoWechat, TradeTypeNative, "", TradeTypeNative, TradeTypeNative, true},
		{"hupi h5", ChannelHupijiaoWechat, TradeTypeH5, "", TradeTypeH5, TradeTypeH5, true},
		{"hupi jsapi", ChannelHupijiaoWechat, "", TradeTypeJSAPI, TradeTypeJSAPI, TradeTypeJSAPI, true},
		{"wechat mismatch", ChannelWechat, TradeTypeNative, TradeTypeH5, "", "", false},
		{"wechat unknown trade", ChannelWechat, "", "OTHER", "", "", false},
		{"wechat unknown product", ChannelWechat, "OTHER", "", "", "", false},
		{"hupi mini unsupported", ChannelHupijiaoWechat, TradeTypeMini, "", "", "", false},
		{"hupi app unsupported", ChannelHupijiaoWechat, "", TradeTypeAPP, "", "", false},
		{"hupi mismatch", ChannelHupijiaoWechat, TradeTypeH5, TradeTypeNative, "", "", false},
		{"alipay default", ChannelAlipay, "", "", "PAGE", "", true},
		{"alipay page", ChannelAlipay, "PAGE", "", "PAGE", "", true},
		{"alipay wap", ChannelAlipay, "wap", "", "WAP", "", true},
		{"alipay face", ChannelAlipay, " FACE ", "", "FACE", "", true},
		{"hupi alipay page", ChannelHupijiaoAlipay, "PAGE", "", "PAGE", "", true},
		{"hupi alipay wap", ChannelHupijiaoAlipay, "WAP", "", "WAP", "", true},
		{"hupi alipay face", ChannelHupijiaoAlipay, "FACE", "", "FACE", "", true},
		{"alipay native unsupported", ChannelAlipay, TradeTypeNative, "", "", "", false},
		{"hupi alipay h5 unsupported", ChannelHupijiaoAlipay, TradeTypeH5, "", "", "", false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := normalizeOnlineOrderRequest(tt.channel, CreateOrderRequest{Product: tt.product, TradeType: tt.trade, OpenID: " fixture-payer ", ClientIP: "203.0.113.42"})
			if (err == nil) != tt.valid {
				t.Fatalf("wrong channel validation: got=%+v err=%v", got, err)
			}
			if !tt.valid {
				return
			}
			if got.Product != tt.wantProduct || got.TradeType != tt.wantTrade || got.ClientIP != "203.0.113.42" {
				t.Fatalf("wrong normalized request: %+v", got)
			}
			if (tt.channel == ChannelWechat || tt.channel == ChannelHupijiaoWechat) && got.OpenID != "fixture-payer" {
				t.Fatalf("openid was not normalized: %q", got.OpenID)
			}
		})
	}
}
