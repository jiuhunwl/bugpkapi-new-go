package payment

import (
	"strings"
	"testing"
)

func TestPayGoodsNameUsesConfiguredNameAndFallsBackToSubject(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		subject    string
		want       string
	}{
		{name: "configured_wins", configured: "API 开放平台技术服务费", subject: "余额充值-50元", want: "API 开放平台技术服务费"},
		{name: "configured_trimmed", configured: "  数据接口服务费  ", subject: "点数套餐-入门", want: "数据接口服务费"},
		// 留空必须回到明细名：清掉配置即恢复 1.2.89 之前的行为，不留半个改名状态。
		{name: "empty_keeps_subject", configured: "", subject: "接口套餐-抖音解析-标准版", want: "接口套餐-抖音解析-标准版"},
		{name: "blank_keeps_subject", configured: "   ", subject: "全站套餐-月度", want: "全站套餐-月度"},
		{name: "subject_trimmed", configured: "", subject: "  余额充值-10元 ", want: "余额充值-10元"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := payGoodsName(tc.configured, tc.subject); got != tc.want {
				t.Fatalf("payGoodsName(%q, %q) = %q, want %q", tc.configured, tc.subject, got, tc.want)
			}
		})
	}
}

func TestNormalizeSavePaymentCurrencyRequestValidatesGoodsName(t *testing.T) {
	t.Run("trimmed_and_kept", func(t *testing.T) {
		cfg, err := normalizeSavePaymentCurrencyRequest(SavePaymentCurrencyConfigRequest{
			Currency:  "CNY",
			GoodsName: "  API 开放平台技术服务费 ",
		})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GoodsName != "API 开放平台技术服务费" {
			t.Fatalf("GoodsName = %q", cfg.GoodsName)
		}
	})
	t.Run("omitted_means_detailed_subject", func(t *testing.T) {
		cfg, err := normalizeSavePaymentCurrencyRequest(SavePaymentCurrencyConfigRequest{Currency: "CNY"})
		if err != nil {
			t.Fatal(err)
		}
		if cfg.GoodsName != "" {
			t.Fatalf("GoodsName = %q, want empty so the order subject stays", cfg.GoodsName)
		}
	})
	t.Run("byte_limit_rejects_43_cjk", func(t *testing.T) {
		// 虎皮椒 title 上限 127 字节：43 个汉字是 129 字节，必须存不进去。
		if _, err := normalizeSavePaymentCurrencyRequest(SavePaymentCurrencyConfigRequest{
			Currency:  "CNY",
			GoodsName: strings.Repeat("服", 43),
		}); err == nil {
			t.Fatal("expected a name over 127 bytes to be rejected")
		}
	})
	t.Run("byte_limit_allows_42_cjk", func(t *testing.T) {
		cfg, err := normalizeSavePaymentCurrencyRequest(SavePaymentCurrencyConfigRequest{
			Currency:  "CNY",
			GoodsName: strings.Repeat("服", 42),
		})
		if err != nil {
			t.Fatal(err)
		}
		// 42 个汉字是 126 字节，仍在虎皮椒 127 字节上限内。
		if len(cfg.GoodsName) != 126 || len(cfg.GoodsName) > maxPayGoodsNameBytes {
			t.Fatalf("GoodsName bytes = %d, want 126 and <= %d", len(cfg.GoodsName), maxPayGoodsNameBytes)
		}
	})
	t.Run("invalid_utf8_rejected", func(t *testing.T) {
		if _, err := normalizeSavePaymentCurrencyRequest(SavePaymentCurrencyConfigRequest{
			Currency:  "CNY",
			GoodsName: "坏\x80字节",
		}); err == nil {
			t.Fatal("expected invalid utf8 to be rejected")
		}
	})
}

func TestNormalizePaymentCurrencyConfigTrimsGoodsName(t *testing.T) {
	cfg := normalizePaymentCurrencyConfig(PaymentCurrencyConfig{GoodsName: "  数据接口服务费  "})
	if cfg.GoodsName != "数据接口服务费" {
		t.Fatalf("GoodsName = %q", cfg.GoodsName)
	}
}
