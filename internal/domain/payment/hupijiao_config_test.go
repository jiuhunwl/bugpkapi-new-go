package payment

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type hupijiaoConfigRepositoryStub struct {
	configs map[string]storedHupijiaoConfig
	methods []PaymentMethod

	savedConfig     HupijiaoConfig
	savedCipher     string
	updateAppSecret bool
	savedMethods    []PaymentMethod
	loadConfigErr   error
	loadMethodsErr  error
	saveConfigErr   error
	saveMethodsErr  error
}

func (s *hupijiaoConfigRepositoryStub) HupijiaoConfig(_ context.Context, method string) (storedHupijiaoConfig, error) {
	if s.loadConfigErr != nil {
		return storedHupijiaoConfig{}, s.loadConfigErr
	}
	return s.configs[method], nil
}

func (s *hupijiaoConfigRepositoryStub) HupijiaoConfigs(context.Context) ([]storedHupijiaoConfig, error) {
	if s.loadConfigErr != nil {
		return nil, s.loadConfigErr
	}
	return []storedHupijiaoConfig{s.configs["ALIPAY"], s.configs["WECHAT"]}, nil
}

func (s *hupijiaoConfigRepositoryStub) SaveHupijiaoConfig(_ context.Context, cfg HupijiaoConfig, cipher string, updateAppSecret bool) error {
	s.savedConfig = cfg
	s.savedCipher = cipher
	s.updateAppSecret = updateAppSecret
	return s.saveConfigErr
}

func (s *hupijiaoConfigRepositoryStub) PaymentMethods(context.Context) ([]PaymentMethod, error) {
	if s.loadMethodsErr != nil {
		return nil, s.loadMethodsErr
	}
	return append([]PaymentMethod(nil), s.methods...), nil
}

func (s *hupijiaoConfigRepositoryStub) SavePaymentMethods(_ context.Context, methods []PaymentMethod) error {
	s.savedMethods = append([]PaymentMethod(nil), methods...)
	return s.saveMethodsErr
}

func TestSaveHupijiaoConfigKeepsMaskedSecretForEachMethod(t *testing.T) {
	cipher := NewConfigCipher("payment-test-secret")
	encrypted, err := cipher.Encrypt("wechat-secret")
	if err != nil {
		t.Fatal(err)
	}
	repo := &hupijiaoConfigRepositoryStub{configs: map[string]storedHupijiaoConfig{
		"WECHAT": {
			HupijiaoConfig:  HupijiaoConfig{Method: "WECHAT", AppID: "wechat-app"},
			AppSecretCipher: encrypted,
		},
	}}
	service := &Service{hupijiaoConfigRepo: repo, cipher: cipher}

	err = service.saveHupijiaoConfig(context.Background(), SaveHupijiaoConfigRequest{
		Method:    "WECHAT",
		Enabled:   true,
		AppID:     "wechat-app",
		AppSecret: maskedPaymentSecret,
		NotifyURL: "https://api.example.com/system-api/payment/hupijiao/notify/wechat",
		WAPURL:    "https://www.example.com",
		WAPName:   "BugPk API",
	})
	if err != nil {
		t.Fatalf("saveHupijiaoConfig() error = %v", err)
	}
	if repo.updateAppSecret || repo.savedCipher != encrypted {
		t.Fatalf("masked secret was overwritten: update=%v cipherChanged=%v", repo.updateAppSecret, repo.savedCipher != encrypted)
	}
	if repo.savedConfig.Method != "WECHAT" || repo.savedConfig.AppID != "wechat-app" {
		t.Fatalf("wrong channel configuration saved: %#v", repo.savedConfig)
	}
}

func TestSaveHupijiaoConfigRejectsEnabledChannelWithoutSecret(t *testing.T) {
	repo := &hupijiaoConfigRepositoryStub{configs: map[string]storedHupijiaoConfig{"ALIPAY": {}}}
	service := &Service{hupijiaoConfigRepo: repo, cipher: NewConfigCipher("payment-test-secret")}

	err := service.saveHupijiaoConfig(context.Background(), SaveHupijiaoConfigRequest{
		Method:    "ALIPAY",
		Enabled:   true,
		AppID:     "alipay-app",
		NotifyURL: "https://api.example.com/system-api/payment/hupijiao/notify/alipay",
	})
	if err == nil {
		t.Fatal("enabled Hupijiao channel without appsecret must be rejected")
	}
}

func TestNormalizePaymentMethodsRequiresKnownUniqueMethods(t *testing.T) {
	valid, err := normalizePaymentMethods(SavePaymentMethodsRequest{Methods: []PaymentMethod{
		{Code: ChannelHupijiaoWechat, Enabled: true, SortOrder: 5},
		{Code: ChannelAlipay, Enabled: true, SortOrder: 1},
	}})
	if err != nil {
		t.Fatalf("normalizePaymentMethods() error = %v", err)
	}
	if len(valid) != 2 || valid[0].Code != ChannelAlipay || valid[1].Code != ChannelHupijiaoWechat {
		t.Fatalf("methods were not normalized and sorted: %#v", valid)
	}

	for _, methods := range [][]PaymentMethod{
		{{Code: "UNKNOWN", Enabled: true}},
		{{Code: ChannelAlipay}, {Code: ChannelAlipay}},
	} {
		if _, err := normalizePaymentMethods(SavePaymentMethodsRequest{Methods: methods}); err == nil {
			t.Fatalf("invalid payment methods accepted: %#v", methods)
		}
	}
}

func TestSavePaymentMethodsStopsWhenRepositoryFails(t *testing.T) {
	want := errors.New("database unavailable")
	repo := &hupijiaoConfigRepositoryStub{saveMethodsErr: want}
	service := &Service{hupijiaoConfigRepo: repo}
	err := service.savePaymentMethods(context.Background(), SavePaymentMethodsRequest{Methods: []PaymentMethod{{Code: ChannelAlipay, Enabled: true}}})
	if !errors.Is(err, want) {
		t.Fatalf("savePaymentMethods() error = %v, want %v", err, want)
	}
}

func TestIsPaymentMethodEnabledRequiresConfiguredEnabledMethod(t *testing.T) {
	repo := &hupijiaoConfigRepositoryStub{methods: []PaymentMethod{
		{Code: ChannelAlipay, Enabled: true},
		{Code: ChannelHupijiaoWechat, Enabled: false},
	}}
	service := &Service{hupijiaoConfigRepo: repo}

	enabled, err := service.isPaymentMethodEnabled(context.Background(), ChannelAlipay)
	if err != nil || !enabled {
		t.Fatalf("enabled method = (%v, %v), want true,nil", enabled, err)
	}
	disabled, err := service.isPaymentMethodEnabled(context.Background(), ChannelHupijiaoWechat)
	if err != nil || disabled {
		t.Fatalf("disabled method = (%v, %v), want false,nil", disabled, err)
	}
	missing, err := service.isPaymentMethodEnabled(context.Background(), ChannelHupijiaoAlipay)
	if err != nil || missing {
		t.Fatalf("missing method = (%v, %v), want false,nil", missing, err)
	}
}

func TestNormalizeEnabledPayChannelUsesFirstConfiguredOnlineMethod(t *testing.T) {
	repo := &hupijiaoConfigRepositoryStub{methods: []PaymentMethod{
		{Code: ChannelBalance, Enabled: true, SortOrder: 10},
		{Code: ChannelAlipay, Enabled: true, SortOrder: 20},
	}}
	service := &Service{
		configRepo:         &alipayConfigRepositoryStub{stored: storedAlipayConfig{AlipayConfig: AlipayConfig{Enabled: true}}},
		hupijiaoConfigRepo: repo,
	}

	channel, err := service.normalizeEnabledPayChannel(context.Background(), "")
	if err != nil || channel != ChannelAlipay {
		t.Fatalf("normalizeEnabledPayChannel() = (%q, %v), want ALIPAY,nil", channel, err)
	}
}

func TestNormalizeEnabledPayChannelRejectsMethodWhenProviderIsDisabled(t *testing.T) {
	repo := &hupijiaoConfigRepositoryStub{methods: []PaymentMethod{{Code: ChannelAlipay, Enabled: true}}}
	service := &Service{
		configRepo:         &alipayConfigRepositoryStub{stored: storedAlipayConfig{AlipayConfig: AlipayConfig{Enabled: false}}},
		hupijiaoConfigRepo: repo,
	}

	if _, err := service.normalizeEnabledPayChannel(context.Background(), ChannelAlipay); err == nil {
		t.Fatal("disabled provider must reject order creation even when the payment-method switch is enabled")
	}
}

func TestNormalizeHupijiaoAmountRoundsFourDecimalOrderAmount(t *testing.T) {
	for input, want := range map[string]string{
		"0.0049":  "0.00",
		"0.0050":  "0.01",
		"12.3449": "12.34",
		"12.3450": "12.35",
		"99.9999": "100.00",
	} {
		got, err := normalizeHupijiaoAmount(input)
		if want == "0.00" {
			if err == nil {
				t.Fatalf("normalizeHupijiaoAmount(%q) = %q, want rejection after rounding to zero", input, got)
			}
			continue
		}
		if err != nil || got != want {
			t.Fatalf("normalizeHupijiaoAmount(%q) = (%q, %v), want %q,nil", input, got, err, want)
		}
	}
}

func TestNormalizeHupijiaoConfigAllowsOnlyOfficialProviderEndpoints(t *testing.T) {
	for _, endpoint := range []string{
		"https://api.xunhupay.com/payment/do.html",
		"https://api.dpweixin.com/payment/do.html",
	} {
		if _, err := normalizeHupijiaoConfig(SaveHupijiaoConfigRequest{
			Method:    "ALIPAY",
			CreateURL: endpoint,
			QueryURL:  strings.Replace(endpoint, "do.html", "query.html", 1),
		}); err != nil {
			t.Fatalf("official Hupijiao endpoint %q rejected: %v", endpoint, err)
		}
	}
	if _, err := normalizeHupijiaoConfig(SaveHupijiaoConfigRequest{
		Method:    "ALIPAY",
		CreateURL: "https://evil.example/payment/do.html",
		QueryURL:  "https://evil.example/payment/query.html",
	}); err == nil {
		t.Fatal("arbitrary Hupijiao endpoint was accepted")
	}
}
