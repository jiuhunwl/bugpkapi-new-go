package systemsettings

import (
	"strings"
	"testing"
)

func TestNormalizeCaptchaConfigRejectsUnknownScene(t *testing.T) {
	_, err := normalizeCaptchaConfig(SaveCaptchaConfigRequest{Scene: "unknown", Provider: captchaProviderPOW})
	if err == nil || !strings.Contains(err.Error(), "场景") {
		t.Fatalf("expected scene validation error, got: %v", err)
	}
}

func TestNormalizeCaptchaConfigRejectsUnknownProvider(t *testing.T) {
	_, err := normalizeCaptchaConfig(SaveCaptchaConfigRequest{Scene: "login", Provider: "wechat"})
	if err == nil || !strings.Contains(err.Error(), "Provider") {
		t.Fatalf("expected provider validation error, got: %v", err)
	}
}

func TestNormalizeCaptchaSceneSelectionDeduplicatesAndKeepsStableOrder(t *testing.T) {
	got, err := normalizeCaptchaSceneSelection([]string{
		" login ", "register", "login", "default",
	})
	if err != nil {
		t.Fatalf("normalizeCaptchaSceneSelection() error = %v", err)
	}
	want := []string{"default", "login", "register"}
	if len(got) != len(want) {
		t.Fatalf("normalized scenes = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalized scenes = %#v, want %#v", got, want)
		}
	}
}

func TestNormalizeCaptchaSceneSelectionAllowsDisablingEveryScene(t *testing.T) {
	got, err := normalizeCaptchaSceneSelection(nil)
	if err != nil {
		t.Fatalf("normalizeCaptchaSceneSelection(nil) error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("normalized empty selection = %#v, want empty", got)
	}
}

func TestNormalizeCaptchaSceneSelectionRejectsUnknownScene(t *testing.T) {
	if _, err := normalizeCaptchaSceneSelection([]string{"login", "admin"}); err == nil {
		t.Fatal("unknown scene was accepted")
	}
}

func TestNormalizeCaptchaConfigRequiresGeeTest4KeysWhenEnabled(t *testing.T) {
	_, err := normalizeCaptchaConfig(SaveCaptchaConfigRequest{
		Scene: "login", Provider: captchaProviderGeeTest4, Enabled: 1,
	})
	if err == nil {
		t.Fatal("expected geetest4 key validation error")
	}
	if !strings.Contains(err.Error(), "captchaId") || !strings.Contains(err.Error(), "apiKey") {
		t.Fatalf("geetest4 error must mention missing keys, got: %v", err)
	}
}

func TestNormalizeCaptchaConfigAcceptsGeeTest4Keys(t *testing.T) {
	cfg, err := normalizeCaptchaConfig(SaveCaptchaConfigRequest{
		Scene: "login", Provider: captchaProviderGeeTest4, Enabled: 1,
		CaptchaID: "captcha-id", APIKey: "api-key", Difficulty: 4,
	})
	if err != nil {
		t.Fatalf("normalizeCaptchaConfig() error = %v", err)
	}
	if cfg.Provider != captchaProviderGeeTest4 || cfg.CaptchaID != "captcha-id" || cfg.APIKey != "api-key" {
		t.Fatalf("normalized geetest4 config = %+v", cfg)
	}
	if cfg.Difficulty != 4 {
		t.Fatalf("difficulty = %d, want 4", cfg.Difficulty)
	}
	if cfg.Product != "float" {
		t.Fatalf("default GeeTest product = %q, want float", cfg.Product)
	}
}

func TestNormalizeCaptchaConfigRequiresCloudflareKeysWhenEnabled(t *testing.T) {
	_, err := normalizeCaptchaConfig(SaveCaptchaConfigRequest{
		Scene: "login", Provider: captchaProviderCloudflare, Enabled: 1,
	})
	if err == nil {
		t.Fatal("expected cloudflare key validation error")
	}
	if !strings.Contains(err.Error(), "siteKey") || !strings.Contains(err.Error(), "secretKey") {
		t.Fatalf("cloudflare error must mention missing keys, got: %v", err)
	}
}

func TestNormalizeCaptchaConfigAllowsDisabledThirdPartyWithoutKeys(t *testing.T) {
	cfg, err := normalizeCaptchaConfig(SaveCaptchaConfigRequest{
		Scene: "login", Provider: captchaProviderCloudflare, Enabled: 0,
	})
	if err != nil {
		t.Fatalf("normalizeCaptchaConfig() error = %v", err)
	}
	if cfg.Enabled != 0 || cfg.Provider != captchaProviderCloudflare {
		t.Fatalf("normalized config = %+v", cfg)
	}
}

func TestEnabledCaptchaRejectsMaskedSecretWithoutStoredValue(t *testing.T) {
	cfg := CaptchaSceneConfig{
		Scene: "login", Provider: captchaProviderCloudflare, Enabled: 1,
		SiteKey: "site-key", SecretKey: preservedSecret(secretMask, ""),
	}
	if err := validateEnabledCaptchaCredentials(cfg); err == nil {
		t.Fatal("masked secret without a stored value must not enable Turnstile")
	}
}

func TestCurrentCaptchaSecretFollowsDefaultFallback(t *testing.T) {
	rows := []CaptchaProviderRow{
		{ProviderKey: captchaProviderCloudflare, Scene: "default", Enabled: 1, SecretRef: "default-secret"},
		{ProviderKey: captchaProviderCloudflare, Scene: "login", Enabled: 0, SecretRef: "stale-scene-secret"},
	}
	if got := currentCaptchaSecret(rows, "login", "secretKey"); got != "default-secret" {
		t.Fatalf("currentCaptchaSecret() = %q, want active default secret", got)
	}
}

func TestNormalizeCaptchaConfigClampsDifficultyAndPolicy(t *testing.T) {
	cfg, err := normalizeCaptchaConfig(SaveCaptchaConfigRequest{
		Scene: "login", Provider: captchaProviderPOW, Enabled: 1,
		Difficulty: 99, FailurePolicy: "allow",
	})
	if err != nil {
		t.Fatalf("normalizeCaptchaConfig() error = %v", err)
	}
	if cfg.Difficulty != defaultCaptchaDifficulty {
		t.Fatalf("difficulty = %d, want fallback %d", cfg.Difficulty, defaultCaptchaDifficulty)
	}
	if cfg.FailurePolicy != "deny" {
		t.Fatalf("failure policy = %q, want deny", cfg.FailurePolicy)
	}
}

func TestNormalizeCaptchaConfigValidatesCloudflareBindings(t *testing.T) {
	cfg, err := normalizeCaptchaConfig(SaveCaptchaConfigRequest{
		Scene: "login", Provider: captchaProviderCloudflare, Enabled: 1,
		SiteKey: "site-key", SecretKey: "secret-key",
		Action: "login_action", Hostname: "API.Example.COM.", CData: "bugpk",
	})
	if err != nil {
		t.Fatalf("normalizeCaptchaConfig() error = %v", err)
	}
	if cfg.Action != "login_action" || cfg.Hostname != "api.example.com" || cfg.CData != "bugpk" {
		t.Fatalf("normalized Cloudflare bindings = %+v", cfg)
	}

	for _, req := range []SaveCaptchaConfigRequest{
		{Scene: "login", Provider: captchaProviderCloudflare, Action: "invalid action"},
		{Scene: "login", Provider: captchaProviderCloudflare, CData: "invalid/data"},
		{Scene: "login", Provider: captchaProviderCloudflare, Hostname: "https://example.com"},
	} {
		if _, err := normalizeCaptchaConfig(req); err == nil {
			t.Fatalf("expected invalid Cloudflare binding to fail: %+v", req)
		}
	}
}

func TestActiveSceneConfigPrefersSceneRowOverDefault(t *testing.T) {
	service := &Service{}
	rows := []CaptchaProviderRow{
		{ProviderKey: captchaProviderPOW, Scene: "default", Enabled: 1, SceneEnabled: 1, ExtraConfig: `{"difficulty":3}`},
		{ProviderKey: captchaProviderGeeTest4, Scene: "login", Enabled: 1, SceneEnabled: 1, ExtraConfig: `{"captchaId":"id"}`},
	}
	cfg := service.activeSceneConfig(rows, "login")
	if cfg.Provider != captchaProviderGeeTest4 {
		t.Fatalf("active provider = %q, want geetest4", cfg.Provider)
	}
}

func TestActiveSceneConfigPreservesExplicitDisabledScene(t *testing.T) {
	service := &Service{}
	rows := []CaptchaProviderRow{
		{ProviderKey: captchaProviderPOW, Scene: "default", Enabled: 1, SceneEnabled: 1, ExtraConfig: `{"difficulty":3}`},
		{ProviderKey: captchaProviderPOW, Scene: "login", Enabled: 1, SceneEnabled: 0, ExtraConfig: `{"difficulty":4}`},
	}
	cfg := service.activeSceneConfig(rows, "login")
	if cfg.Enabled != 0 || cfg.Difficulty != 4 {
		t.Fatalf("disabled scene config = %+v, want disabled scene-specific config", cfg)
	}
}

func TestActiveSceneConfigFallsBackToDefaultThenPOW(t *testing.T) {
	service := &Service{}
	rows := []CaptchaProviderRow{
		{ProviderKey: captchaProviderPOW, Scene: "default", Enabled: 1, SceneEnabled: 1, ExtraConfig: `{"difficulty":4}`},
	}
	cfg := service.activeSceneConfig(rows, "register_email")
	if cfg.Provider != captchaProviderPOW || cfg.Difficulty != 4 {
		t.Fatalf("fallback config = %+v", cfg)
	}

	empty := service.activeSceneConfig(nil, "register")
	if empty.Provider != captchaProviderPOW || empty.Enabled != 1 || empty.Difficulty != defaultCaptchaDifficulty {
		t.Fatalf("empty fallback config = %+v", empty)
	}
}

func TestActiveSceneConfigInheritsDefaultOverSceneSeedRow(t *testing.T) {
	// The seed rows (explicit = 0) must not shadow a provider the
	// administrator chose for the `default` scene.
	service := &Service{}
	rows := []CaptchaProviderRow{
		{ProviderKey: captchaProviderPOW, Scene: "default", Enabled: 0, SceneEnabled: 1},
		{ProviderKey: captchaProviderGeeTest4, Scene: "default", Enabled: 1, SceneEnabled: 1, Explicit: 1, SecretRef: "geetest-secret", ExtraConfig: `{"captchaId":"geetest-id"}`},
		{ProviderKey: captchaProviderPOW, Scene: "login", Enabled: 1, SceneEnabled: 1, ExtraConfig: `{"difficulty":3}`},
	}
	cfg := service.activeSceneConfig(rows, "login")
	if cfg.Provider != captchaProviderGeeTest4 {
		t.Fatalf("active provider = %q, want geetest4", cfg.Provider)
	}
	if !cfg.Inherited {
		t.Fatalf("inherited scene must be marked inherited, got %+v", cfg)
	}
	if cfg.Enabled != 1 {
		t.Fatalf("inherited scene switch = %d, want the scene's own enabled flag", cfg.Enabled)
	}
	if cfg.CaptchaID != "geetest-id" || cfg.APIKey != secretMask {
		t.Fatalf("inherited credentials = %+v", cfg)
	}
}

func TestActiveSceneConfigInheritedKeepsSceneSwitchOff(t *testing.T) {
	service := &Service{}
	rows := []CaptchaProviderRow{
		{ProviderKey: captchaProviderCloudflare, Scene: "default", Enabled: 1, SceneEnabled: 1, Explicit: 1, ExtraConfig: `{"siteKey":"site-key"}`},
		{ProviderKey: captchaProviderPOW, Scene: "login", Enabled: 1, SceneEnabled: 0},
	}
	cfg := service.activeSceneConfig(rows, "login")
	if cfg.Provider != captchaProviderCloudflare {
		t.Fatalf("active provider = %q, want cloudflare", cfg.Provider)
	}
	if cfg.Enabled != 0 {
		t.Fatalf("checked-off scene must stay disabled, got %+v", cfg)
	}
}

func TestSelectCaptchaProviderRowExplicitSceneBeatsInheritedDefault(t *testing.T) {
	rows := []CaptchaProviderRow{
		{ProviderKey: captchaProviderGeeTest4, Scene: "default", Enabled: 1, Explicit: 1},
		{ProviderKey: captchaProviderPOW, Scene: "login", Enabled: 1, Explicit: 1, ExtraConfig: `{"difficulty":5}`},
	}
	picked, ok, inherited := selectCaptchaProviderRow(rows, "login")
	if !ok || inherited {
		t.Fatalf("picked = %+v ok=%v inherited=%v, want an explicit scene row", picked, ok, inherited)
	}
	if picked.ProviderKey != captchaProviderPOW || picked.Scene != "login" {
		t.Fatalf("picked = %+v", picked)
	}
}

func TestSelectCaptchaProviderRowKeepsExplicitDisabledScene(t *testing.T) {
	rows := []CaptchaProviderRow{
		{ProviderKey: captchaProviderGeeTest4, Scene: "default", Enabled: 1, Explicit: 1},
		{ProviderKey: captchaProviderPOW, Scene: "login", Enabled: 1, SceneEnabled: 0, Explicit: 1},
	}
	picked, ok, inherited := selectCaptchaProviderRow(rows, "login")
	if !ok || inherited || picked.SceneEnabled != 0 {
		t.Fatalf("picked = %+v ok=%v inherited=%v", picked, ok, inherited)
	}
}

func TestSelectCaptchaProviderRowIgnoresImplicitDefaultRow(t *testing.T) {
	// A `default` row the administrator never chose stays a seed row and must
	// not shadow the scene's own seed row.
	rows := []CaptchaProviderRow{
		{ProviderKey: captchaProviderCloudflare, Scene: "default", Enabled: 1, Explicit: 0},
		{ProviderKey: captchaProviderPOW, Scene: "register", Enabled: 1, Explicit: 0},
	}
	picked, ok, inherited := selectCaptchaProviderRow(rows, "register")
	if !ok || inherited {
		t.Fatalf("picked = %+v ok=%v inherited=%v", picked, ok, inherited)
	}
	if picked.ProviderKey != captchaProviderPOW || picked.Scene != "register" {
		t.Fatalf("picked = %+v", picked)
	}
}

func TestSceneSwitchValueUsesTheSceneRow(t *testing.T) {
	rows := []CaptchaProviderRow{
		{ProviderKey: captchaProviderGeeTest4, Scene: "default", Enabled: 1, SceneEnabled: 1, Explicit: 1},
		{ProviderKey: captchaProviderPOW, Scene: "login", Enabled: 1, SceneEnabled: 0},
	}
	if value, ok := sceneSwitchValue(rows, "login"); !ok || value != 0 {
		t.Fatalf("scene switch = %d ok=%v, want 0 true", value, ok)
	}
	if value, ok := sceneSwitchValue(rows, "change_password"); ok || value != 0 {
		t.Fatalf("missing scene switch = %d ok=%v, want 0 false", value, ok)
	}
}
