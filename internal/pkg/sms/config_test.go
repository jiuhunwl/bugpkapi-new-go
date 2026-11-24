package sms

import (
	"errors"
	"testing"
	"time"
)

func TestParseConfig(t *testing.T) {
	raw := `{
		"accessKeyId":" LTAI-test ",
		"accessKeySecret":"secret-value",
		"signName":"BugPk",
		"templateId":"SMS_1001",
		"templateIdEn":"SMS_2001",
		"endpoint":"https://dysmsapi.aliyuncs.com",
		"region":"cn-hangzhou",
		"timeoutMs":4000,
		"officialSdkOnly":true
	}`
	cfg, err := ParseConfig(ProviderAliyun, raw)
	if err != nil {
		t.Fatalf("ParseConfig error: %v", err)
	}
	if cfg.ProviderKey != ProviderAliyun {
		t.Fatalf("provider key = %q", cfg.ProviderKey)
	}
	if cfg.AccessKeyID != "LTAI-test" || cfg.AccessKeySecret != "secret-value" {
		t.Fatalf("credentials = %q / %q", cfg.AccessKeyID, cfg.AccessKeySecret)
	}
	if cfg.TemplateID != "SMS_1001" {
		t.Fatalf("template = %q", cfg.TemplateID)
	}
	// templateIdEn 必须落到 en 模板位：老配置只有一个英文模板字段名。
	if got := cfg.TemplatesByLocale["en"]; got != "SMS_2001" {
		t.Fatalf("en template = %q", got)
	}
	if cfg.Timeout != 4*time.Second {
		t.Fatalf("timeout = %v", cfg.Timeout)
	}
	// 未知字段（officialSdkOnly）必须被忽略，否则线上配置一改就起不来。
}

func TestParseConfigEmptyAndBroken(t *testing.T) {
	cfg, err := ParseConfig(ProviderTencent, "")
	if err != nil {
		t.Fatalf("empty config must not error, got %v", err)
	}
	if cfg.ProviderKey != ProviderTencent || cfg.Timeout != defaultTimeout {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if _, err := ParseConfig(ProviderAliyun, "{not json"); err == nil {
		t.Fatal("broken json must error")
	}
}

func TestParseConfigAppIDAliases(t *testing.T) {
	preferred, err := ParseConfig(ProviderTencent, `{"appId":"1400000001","smsSdkAppId":"1400000002"}`)
	if err != nil {
		t.Fatalf("ParseConfig error: %v", err)
	}
	// 两个字段同义时以 smsSdkAppId 为准，与腾讯云 API 的命名对齐。
	if preferred.AppID != "1400000002" {
		t.Fatalf("app id = %q", preferred.AppID)
	}
	fallback, err := ParseConfig(ProviderTencent, `{"appId":"1400000003"}`)
	if err != nil {
		t.Fatalf("ParseConfig error: %v", err)
	}
	if fallback.AppID != "1400000003" {
		t.Fatalf("app id fallback = %q", fallback.AppID)
	}
}

func TestConfigValidate(t *testing.T) {
	base := Config{
		ProviderKey:     ProviderAliyun,
		AccessKeyID:     "LTAI-test",
		AccessKeySecret: "secret-value",
		SignName:        "BugPk",
		TemplateID:      "SMS_1001",
	}

	good := base
	if err := good.Validate(); err != nil {
		t.Fatalf("Validate error: %v", err)
	}
	// 默认 endpoint 必须是短信服务 dysmsapi，不是号码认证服务的 dypnsapi。
	if good.Endpoint != aliyunEndpoint {
		t.Fatalf("endpoint = %q, want %q", good.Endpoint, aliyunEndpoint)
	}
	if good.Region != "cn-hangzhou" {
		t.Fatalf("region = %q", good.Region)
	}
	if good.Timeout != defaultTimeout {
		t.Fatalf("timeout = %v", good.Timeout)
	}

	tencent := base
	tencent.ProviderKey = ProviderTencent
	if err := tencent.Validate(); err == nil {
		t.Fatal("tencent must require an app id")
	}
	tencent.AppID = "1400000000"
	if err := tencent.Validate(); err != nil {
		t.Fatalf("tencent Validate error: %v", err)
	}
	if tencent.Endpoint != tencentEndpoint || tencent.Region != "ap-guangzhou" {
		t.Fatalf("tencent defaults = %q / %q", tencent.Endpoint, tencent.Region)
	}

	missingSecret := base
	missingSecret.AccessKeySecret = ""
	if err := missingSecret.Validate(); !errors.Is(err, ErrIncompleteConfig) {
		t.Fatalf("missing secret err = %v, want ErrIncompleteConfig", err)
	}

	unknown := base
	unknown.ProviderKey = "twilio"
	if err := unknown.Validate(); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("unknown provider err = %v, want ErrNotImplemented", err)
	}

	userInfo := base
	userInfo.Endpoint = "https://admin:pass@dysmsapi.aliyuncs.com"
	if err := userInfo.Validate(); err == nil {
		t.Fatal("endpoint with userinfo must be rejected")
	}

	fileScheme := base
	fileScheme.Endpoint = "file:///etc/passwd"
	if err := fileScheme.Validate(); err == nil {
		t.Fatal("non-http endpoint scheme must be rejected")
	}

	oversized := base
	oversized.SignName = string(make([]byte, maxSignNameLen+1))
	if err := oversized.Validate(); err == nil {
		t.Fatal("oversized sign name must be rejected")
	}
}

func TestClampTimeout(t *testing.T) {
	if got := clampTimeout(0); got != defaultTimeout {
		t.Fatalf("zero timeout = %v", got)
	}
	if got := clampTimeout(-5); got != defaultTimeout {
		t.Fatalf("negative timeout = %v", got)
	}
	if got := clampTimeout(1); got != minTimeout {
		t.Fatalf("tiny timeout = %v, want %v", got, minTimeout)
	}
	if got := clampTimeout(600_000); got != maxTimeout {
		t.Fatalf("huge timeout = %v, want %v", got, maxTimeout)
	}
}

func TestRegistry(t *testing.T) {
	got := Implemented()
	want := []string{ProviderAliyun, ProviderTencent}
	if len(got) != len(want) {
		t.Fatalf("Implemented = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Implemented = %v, want %v", got, want)
		}
	}
	if !Supports("ALIYUN") || !Supports(" tencent ") {
		t.Fatal("Supports must be case and space insensitive")
	}
	if Supports("huawei") || Supports("") {
		t.Fatal("Supports must reject unimplemented providers")
	}

	provider, err := New(Config{
		ProviderKey:     ProviderAliyun,
		AccessKeyID:     "LTAI-test",
		AccessKeySecret: "secret-value",
		SignName:        "BugPk",
		TemplateID:      "SMS_1001",
	})
	if err != nil {
		t.Fatalf("New error: %v", err)
	}
	if provider.Key() != ProviderAliyun {
		t.Fatalf("provider key = %q", provider.Key())
	}
	if provider.Capabilities().NumberAuth {
		t.Fatal("本期不接一键登录，能力位必须为 false")
	}
	if _, err := New(Config{ProviderKey: ProviderAliyun}); !errors.Is(err, ErrIncompleteConfig) {
		t.Fatalf("New with empty config = %v", err)
	}
}

func TestSelectTemplate(t *testing.T) {
	byLocale := map[string]string{"en": "SMS_en", "zh": "SMS_zh"}
	tests := []struct {
		locale string
		want   string
	}{
		{locale: "", want: "SMS_zh"},
		{locale: "zh", want: "SMS_zh"},
		{locale: "en", want: "SMS_en"},
		{locale: "en-US", want: "SMS_en"},
		{locale: "en_US", want: "SMS_en"},
		{locale: "fr", want: "SMS_zh"}, // 未配置语言回退默认模板
		{locale: "  ", want: "SMS_zh"},
	}
	for _, tt := range tests {
		got, ok := selectTemplate("SMS_zh", byLocale, tt.locale)
		if !ok || got != tt.want {
			t.Fatalf("selectTemplate(%q) = (%q, %v), want %q", tt.locale, got, ok, tt.want)
		}
	}
	if _, ok := selectTemplate("", map[string]string{"en": " "}, "fr"); ok {
		t.Fatal("no template at all must report not configurable")
	}
}
