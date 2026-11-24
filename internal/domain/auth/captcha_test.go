package auth

import (
	"context"
	"testing"
	"time"

	"bugpk-api/internal/config"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type disabledCaptchaConfigRepository struct{}

func (disabledCaptchaConfigRepository) CaptchaGlobalEnabled(context.Context) (bool, error) {
	return true, nil
}

func (disabledCaptchaConfigRepository) CaptchaProviderConfig(_ context.Context, scene, fallback string) (CaptchaProviderConfig, error) {
	return CaptchaProviderConfig{
		ProviderKey: fallback,
		Scene:       scene,
		Enabled:     false,
	}, nil
}

type enabledCaptchaConfigRepository struct{}

func (enabledCaptchaConfigRepository) CaptchaGlobalEnabled(context.Context) (bool, error) {
	return true, nil
}

func (enabledCaptchaConfigRepository) CaptchaProviderConfig(_ context.Context, scene, fallback string) (CaptchaProviderConfig, error) {
	return CaptchaProviderConfig{
		ProviderKey: fallback,
		Scene:       scene,
		Enabled:     true,
	}, nil
}

func TestDisabledCaptchaSceneReturnsNoChallengeAndSkipsConsume(t *testing.T) {
	service := &CaptchaService{
		cfg:  config.CaptchaConfig{DefaultProvider: ProviderPOW},
		repo: disabledCaptchaConfigRepository{},
	}

	challenge, err := service.Create(context.Background(), "login", "binding")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if challenge.Enabled || challenge.Scene != "login" || challenge.CaptchaID != "" {
		t.Fatalf("disabled challenge = %+v", challenge)
	}
	if err := service.Consume(context.Background(), "", "login", "binding"); err != nil {
		t.Fatalf("Consume() for disabled scene error = %v", err)
	}
}

type globallyDisabledCaptchaConfigRepository struct {
	enabledCaptchaConfigRepository
}

func (globallyDisabledCaptchaConfigRepository) CaptchaGlobalEnabled(context.Context) (bool, error) {
	return false, nil
}

func TestGlobalCaptchaSwitchBypassesCreateVerifyAndConsume(t *testing.T) {
	service := &CaptchaService{
		cfg:  config.CaptchaConfig{DefaultProvider: ProviderPOW},
		repo: globallyDisabledCaptchaConfigRepository{},
	}

	challenge, err := service.Create(context.Background(), "login", "binding")
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if challenge.Enabled || challenge.Scene != "login" || challenge.CaptchaID != "" {
		t.Fatalf("global-disabled challenge = %+v", challenge)
	}
	if err := service.Verify(context.Background(), "", CaptchaVerifyRequest{}, "binding"); err != nil {
		t.Fatalf("Verify() global-disabled error = %v", err)
	}
	if err := service.Consume(context.Background(), "", "login", "binding"); err != nil {
		t.Fatalf("Consume() global-disabled error = %v", err)
	}
}

func TestEnabledCaptchaSceneRejectsMissingCaptchaID(t *testing.T) {
	service := &CaptchaService{
		cfg:  config.CaptchaConfig{DefaultProvider: ProviderPOW},
		repo: enabledCaptchaConfigRepository{},
	}

	err := service.Consume(context.Background(), "", "login", "binding")
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusBadRequest {
		t.Fatalf("enabled scene consume error = %v, want HTTP 400", err)
	}
}

func TestValidPowProof(t *testing.T) {
	stored := storedCaptcha{
		Challenge:  "abcdef",
		Difficulty: 2,
	}
	timeText := "1.2"
	proof := ""
	for i := 0; i < 500000; i++ {
		candidate := base36(i)
		if validPowProof("captcha-id", stored, timeText, candidate) {
			proof = candidate
			break
		}
	}
	if proof == "" {
		t.Fatal("failed to find proof")
	}
	if !validPowProof("captcha-id", stored, timeText, proof) {
		t.Fatal("expected proof to be valid")
	}
	if validPowProof("captcha-id", stored, timeText, proof+"x") {
		t.Fatal("expected modified proof to be invalid")
	}
}

func TestCaptchaBindingStable(t *testing.T) {
	a := CaptchaBinding("ua", "zh-CN")
	b := CaptchaBinding("ua", "zh-CN")
	c := CaptchaBinding("ua2", "zh-CN")
	if a == "" || a != b {
		t.Fatal("expected stable non-empty binding")
	}
	if a == c {
		t.Fatal("expected user-agent change to change binding")
	}
}

func TestValidElapsedAcceptsFastDragAndRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{name: "instant drag", value: "0.0", want: true},
		{name: "ordinary drag", value: "1.2", want: true},
		{name: "paused browser", value: "30.1", want: true},
		{name: "maximum", value: "600", want: true},
		{name: "negative", value: "-0.1", want: false},
		{name: "too large", value: "600.1", want: false},
		{name: "nan", value: "NaN", want: false},
		{name: "infinity", value: "+Inf", want: false},
		{name: "empty", value: "", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validElapsed(test.value); got != test.want {
				t.Fatalf("validElapsed(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestCaptchaTTLAlwaysExpires(t *testing.T) {
	tests := []struct {
		configured time.Duration
		want       time.Duration
	}{
		{configured: 0, want: defaultCaptchaTTL},
		{configured: -time.Second, want: defaultCaptchaTTL},
		{configured: 10 * time.Second, want: defaultCaptchaTTL},
		{configured: time.Hour, want: defaultCaptchaTTL},
		{configured: time.Minute, want: time.Minute},
	}
	for _, test := range tests {
		service := &CaptchaService{cfg: config.CaptchaConfig{TTL: test.configured}}
		if got := service.captchaTTL(); got != test.want {
			t.Fatalf("captchaTTL(%v) = %v, want %v", test.configured, got, test.want)
		}
	}
}

func TestSanitizeCaptchaMetaAllowsOnlyPublicProviderFields(t *testing.T) {
	geeTest, err := sanitizeCaptchaMeta(ProviderGeeTest4, map[string]any{
		"apiKey": "must-not-leak", "captchaId": "captcha-id",
		"language": "eng", "product": "float",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := geeTest["apiKey"]; leaked {
		t.Fatal("GeeTest apiKey leaked from sanitized metadata")
	}

	turnstile, err := sanitizeCaptchaMeta(ProviderCloudflare, map[string]any{
		"secretKey": "must-not-leak", "siteKey": "site-key",
		"action": "login", "cData": "internal123",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := turnstile["secretKey"]; leaked {
		t.Fatal("Turnstile secretKey leaked from sanitized metadata")
	}
}

func base36(value int) string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	if value == 0 {
		return "0"
	}
	var out []byte
	for value > 0 {
		out = append([]byte{digits[value%36]}, out...)
		value /= 36
	}
	return string(out)
}
