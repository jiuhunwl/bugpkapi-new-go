package payment

import (
	"strings"
	"testing"
)

func TestConvertCNYToUSD(t *testing.T) {
	cases := []struct {
		name      string
		originCNY string
		rate      string
		want      string
		wantErr   bool
	}{
		{name: "normal", originCNY: "50.0000", rate: "7.2000", want: "6.9444"},
		{name: "rounding_half_away", originCNY: "1.0000", rate: "7.2000", want: "0.1389"},
		{name: "tiny_amount_floors_to_one_cent", originCNY: "0.0700", rate: "7.2000", want: "0.0100"},
		{name: "zero_origin_stays_zero", originCNY: "0.0000", rate: "7.2000", want: "0.0000"},
		{name: "zero_rate_rejected", originCNY: "50.0000", rate: "0.0000", wantErr: true},
		{name: "negative_origin_rejected", originCNY: "-1.0000", rate: "7.2000", wantErr: true},
		{name: "garbage_origin_rejected", originCNY: "abc", rate: "7.2000", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := convertCNYToUSD(tc.originCNY, tc.rate)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("convertCNYToUSD(%q, %q) expected error, got %q", tc.originCNY, tc.rate, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("convertCNYToUSD(%q, %q) unexpected error: %v", tc.originCNY, tc.rate, err)
			}
			if got != tc.want {
				t.Fatalf("convertCNYToUSD(%q, %q) = %q, want %q", tc.originCNY, tc.rate, got, tc.want)
			}
		})
	}
}

func TestRechargeCreditBase(t *testing.T) {
	cases := []struct {
		name     string
		currency string
		origin   string
		pay      string
		want     string
	}{
		{name: "cny uses pay amount", currency: "CNY", pay: "50.0000", want: "50.0000"},
		{name: "cny ignores origin", currency: "CNY", pay: "50.0000", origin: "55.0000", want: "50.0000"},
		{name: "usd uses cny origin", currency: "USD", pay: "6.9444", origin: "50.0000", want: "50.0000"},
		{name: "usd normalizes casing and spaces", currency: " usd ", pay: "6.9444", origin: " 50.0000 ", want: "50.0000"},
		{name: "usd without origin falls back", currency: "USD", pay: "6.9444", origin: "", want: "6.9444"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := rechargeCreditBase(tc.currency, tc.pay, tc.origin); got != tc.want {
				t.Fatalf("rechargeCreditBase(%q, %q, %q) = %q, want %q", tc.currency, tc.pay, tc.origin, got, tc.want)
			}
		})
	}
}

func TestNormalizeSavePaymentCurrencyRequest(t *testing.T) {
	t.Run("cny_defaults_rate", func(t *testing.T) {
		cfg, err := normalizeSavePaymentCurrencyRequest(SavePaymentCurrencyConfigRequest{Currency: " cny "})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Currency != CurrencyCNY {
			t.Fatalf("currency = %q, want CNY", cfg.Currency)
		}
		if cfg.USDCNYRate != defaultUSDCNYRate {
			t.Fatalf("rate = %q, want default %q", cfg.USDCNYRate, defaultUSDCNYRate)
		}
	})
	t.Run("usd_keeps_rate", func(t *testing.T) {
		cfg, err := normalizeSavePaymentCurrencyRequest(SavePaymentCurrencyConfigRequest{Currency: "USD", USDCNYRate: "7.15"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.USDCNYRate != "7.1500" {
			t.Fatalf("rate = %q, want 7.1500", cfg.USDCNYRate)
		}
	})
	t.Run("unsupported_currency_rejected", func(t *testing.T) {
		if _, err := normalizeSavePaymentCurrencyRequest(SavePaymentCurrencyConfigRequest{Currency: "EUR"}); err == nil {
			t.Fatal("expected error for unsupported currency")
		}
	})
	t.Run("zero_rate_rejected", func(t *testing.T) {
		if _, err := normalizeSavePaymentCurrencyRequest(SavePaymentCurrencyConfigRequest{Currency: "USD", USDCNYRate: "0"}); err == nil {
			t.Fatal("expected error for zero rate")
		}
	})
	t.Run("display_name_trimmed", func(t *testing.T) {
		cfg, err := normalizeSavePaymentCurrencyRequest(SavePaymentCurrencyConfigRequest{Currency: "CNY", DisplayName: "  赞助额度  "})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.DisplayName != "赞助额度" {
			t.Fatalf("displayName = %q, want 赞助额度", cfg.DisplayName)
		}
	})
	t.Run("display_name_empty_allowed", func(t *testing.T) {
		cfg, err := normalizeSavePaymentCurrencyRequest(SavePaymentCurrencyConfigRequest{Currency: "CNY", DisplayName: "   "})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.DisplayName != "" {
			t.Fatalf("displayName = %q, want empty", cfg.DisplayName)
		}
	})
	t.Run("display_name_too_long_rejected", func(t *testing.T) {
		long := strings.Repeat("额", maxCurrencyDisplayNameRunes+1)
		if _, err := normalizeSavePaymentCurrencyRequest(SavePaymentCurrencyConfigRequest{Currency: "CNY", DisplayName: long}); err == nil {
			t.Fatal("expected error for too long display name")
		}
	})
	t.Run("display_name_control_chars_rejected", func(t *testing.T) {
		if _, err := normalizeSavePaymentCurrencyRequest(SavePaymentCurrencyConfigRequest{Currency: "CNY", DisplayName: "积分\n额度"}); err == nil {
			t.Fatal("expected error for control characters")
		}
	})
}

func TestNormalizePaymentCurrencyConfig(t *testing.T) {
	t.Run("row_missing_fields_fall_back", func(t *testing.T) {
		cfg := normalizePaymentCurrencyConfig(PaymentCurrencyConfig{Currency: "junk", USDCNYRate: ""})
		if cfg.Currency != CurrencyCNY || cfg.USDCNYRate != defaultUSDCNYRate {
			t.Fatalf("cfg = %+v, want CNY with default rate", cfg)
		}
	})
	t.Run("usd_kept", func(t *testing.T) {
		cfg := normalizePaymentCurrencyConfig(PaymentCurrencyConfig{Currency: "usd", USDCNYRate: "7.1000"})
		if cfg.Currency != CurrencyUSD || cfg.USDCNYRate != "7.1000" {
			t.Fatalf("cfg = %+v, want USD 7.1000", cfg)
		}
	})
	t.Run("display_name_trimmed", func(t *testing.T) {
		cfg := normalizePaymentCurrencyConfig(PaymentCurrencyConfig{Currency: "CNY", DisplayName: "  积分 "})
		if cfg.DisplayName != "积分" {
			t.Fatalf("displayName = %q, want 积分", cfg.DisplayName)
		}
	})
}
