package auth

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRegistryResolvesRegisteredProvidersAndDefaultsToPOW(t *testing.T) {
	registry := NewRegistry(nil, time.Minute, 3)
	for _, kind := range []string{ProviderPOW, ProviderGeeTest4, ProviderCloudflare} {
		provider, ok := registry.Provider(kind)
		if !ok {
			t.Fatalf("provider %q not registered", kind)
		}
		if provider.Type() != kind {
			t.Fatalf("provider.Type() = %q, want %q", provider.Type(), kind)
		}
	}
	if registry.Default() == nil || registry.Default().Type() != ProviderPOW {
		t.Fatal("Default() must resolve to the POW provider")
	}
	if got := registry.Resolve("unknown-provider"); got == nil || got.Type() != ProviderPOW {
		t.Fatal("Resolve(unknown) must fall back to POW")
	}
}

func TestRegistryTypesListsSupportedProviders(t *testing.T) {
	types := NewRegistry(nil, time.Minute, 3).Types()
	if len(types) != 3 {
		t.Fatalf("registry types = %v, want 3 entries", types)
	}
	seen := map[string]bool{}
	for _, kind := range types {
		seen[kind] = true
	}
	for _, kind := range []string{ProviderPOW, ProviderGeeTest4, ProviderCloudflare} {
		if !seen[kind] {
			t.Fatalf("registry types missing %q: %v", kind, types)
		}
	}
}

func TestGeeTest4ProviderIssueReturnsClearErrorWhenUnconfigured(t *testing.T) {
	provider := NewGeeTest4Provider()
	_, err := provider.Issue(context.Background(), "login", ProviderConfig{}, "")
	if err == nil {
		t.Fatal("expected unconfigured geetest4 Issue to fail")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "geetest") {
		t.Fatalf("geetest4 error must name the provider clearly, got: %v", err)
	}
}

func TestGeeTest4ProviderIssueReturnsPublicSDKConfiguration(t *testing.T) {
	provider := NewGeeTest4Provider()
	cfg := ProviderConfig{
		SiteKey:   "captcha-id-placeholder",
		SecretRef: "api-key-placeholder",
		Extra: map[string]any{
			"language": "eng",
			"product":  "popup",
		},
	}
	challenge, err := provider.Issue(context.Background(), "login", cfg, "")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if challenge.Provider != ProviderGeeTest4 || challenge.CaptchaID == "" || challenge.Scene != "login" {
		t.Fatalf("invalid GeeTest challenge: %+v", challenge)
	}
	if challenge.Meta["captchaId"] != "captcha-id-placeholder" || challenge.Meta["product"] != "popup" || challenge.Meta["language"] != "eng" {
		t.Fatalf("invalid GeeTest public metadata: %#v", challenge.Meta)
	}
	if _, leaked := challenge.Meta["apiKey"]; leaked {
		t.Fatal("GeeTest apiKey must never be exposed in challenge metadata")
	}
}

func TestGeeTest4ProviderVerifyFailsClosedWhenUnconfigured(t *testing.T) {
	provider := NewGeeTest4Provider()
	ok, err := provider.Verify(context.Background(), StoredChallenge{Provider: ProviderGeeTest4}, ProviderConfig{}, ChallengeSolution{})
	if ok {
		t.Fatal("unconfigured geetest4 Verify must return false")
	}
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "geetest") {
		t.Fatalf("geetest4 Verify must return a clear configuration error, got: %v", err)
	}
}

func TestCloudflareProviderIssueReturnsClearErrorWhenUnconfigured(t *testing.T) {
	provider := NewCloudflareTurnstileProvider()
	_, err := provider.Issue(context.Background(), "login", ProviderConfig{}, "")
	if err == nil {
		t.Fatal("expected unconfigured cloudflare Issue to fail")
	}
	if !strings.Contains(err.Error(), "Cloudflare") && !strings.Contains(err.Error(), "siteKey") {
		t.Fatalf("cloudflare error must name the provider clearly, got: %v", err)
	}
}

func TestCloudflareProviderIssueReturnsBoundPublicSDKConfiguration(t *testing.T) {
	provider := NewCloudflareTurnstileProvider()
	cfg := ProviderConfig{
		SiteKey:   "site-key-placeholder",
		SecretRef: "secret-key-placeholder",
		Extra: map[string]any{
			"cdata": "bugpk",
		},
	}
	challenge, err := provider.Issue(context.Background(), "login", cfg, "")
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if challenge.Provider != ProviderCloudflare || challenge.CaptchaID == "" || challenge.Scene != "login" {
		t.Fatalf("invalid Turnstile challenge: %+v", challenge)
	}
	if challenge.Meta["siteKey"] != "site-key-placeholder" || challenge.Meta["action"] != "login" || challenge.Meta["cData"] != "bugpk_"+challenge.CaptchaID {
		t.Fatalf("invalid Turnstile public metadata: %#v", challenge.Meta)
	}
	if _, leaked := challenge.Meta["secretKey"]; leaked {
		t.Fatal("Turnstile secretKey must never be exposed in challenge metadata")
	}
}

func TestCloudflareProviderVerifyFailsClosedWhenUnconfigured(t *testing.T) {
	provider := NewCloudflareTurnstileProvider()
	ok, err := provider.Verify(context.Background(), StoredChallenge{Provider: ProviderCloudflare}, ProviderConfig{}, ChallengeSolution{})
	if ok {
		t.Fatal("unconfigured cloudflare Verify must return false")
	}
	if err == nil || !strings.Contains(err.Error(), "Cloudflare") {
		t.Fatalf("cloudflare Verify must return a clear configuration error, got: %v", err)
	}
}

func TestPOWProviderVerifyUsesUnchangedAlgorithm(t *testing.T) {
	provider := NewPOWProvider(nil, time.Minute, 2)
	stored := StoredChallenge{
		CaptchaID:  "captcha-id",
		Provider:   ProviderPOW,
		Challenge:  "abcdef",
		Difficulty: 2,
	}
	proof := ""
	for value := 0; value < 500_000; value++ {
		candidate := base36(value)
		if validPowProof(stored.CaptchaID, storedCaptcha{Challenge: stored.Challenge, Difficulty: stored.Difficulty}, "1.2", candidate) {
			proof = candidate
			break
		}
	}
	if proof == "" {
		t.Fatal("failed to find proof")
	}
	ok, err := provider.Verify(context.Background(), stored, ProviderConfig{}, ChallengeSolution{Proof: proof, Time: "1.2"})
	if err != nil || !ok {
		t.Fatalf("expected valid proof to verify, ok=%v err=%v", ok, err)
	}
	ok, err = provider.Verify(context.Background(), stored, ProviderConfig{}, ChallengeSolution{Proof: proof + "x", Time: "1.2"})
	if err == nil || ok {
		t.Fatalf("expected tampered proof to fail, ok=%v err=%v", ok, err)
	}
}

func TestParseProviderConfigReadsDifficultyFromExtra(t *testing.T) {
	cfg := ParseProviderConfig(CaptchaProviderConfig{
		ProviderKey: ProviderPOW,
		Enabled:     true,
		ExtraConfig: `{"difficulty": 5}`,
	})
	if cfg.Difficulty() != 5 {
		t.Fatalf("difficulty = %d, want 5", cfg.Difficulty())
	}
	if cfg.CaptchaID() != "" || cfg.SiteKeyValue() != "" || cfg.APIKey() != "" {
		t.Fatal("pow config must not expose third-party fields")
	}
}

func TestParseProviderConfigReadsThirdPartyFields(t *testing.T) {
	cfg := ParseProviderConfig(CaptchaProviderConfig{
		ProviderKey: ProviderGeeTest4,
		SiteKey:     "captcha-id",
		SecretRef:   "encrypted-api-key",
		ExtraConfig: `{"captchaId": "captcha-id"}`,
	})
	if cfg.CaptchaID() != "captcha-id" {
		t.Fatalf("captchaId = %q, want captcha-id", cfg.CaptchaID())
	}
	if cfg.APIKey() != "encrypted-api-key" {
		t.Fatalf("apiKey = %q, want encrypted-api-key", cfg.APIKey())
	}

	cf := ParseProviderConfig(CaptchaProviderConfig{
		ProviderKey: ProviderCloudflare,
		SiteKey:     "site-key",
		SecretRef:   "encrypted-secret-key",
		ExtraConfig: `{"siteKey": "site-key"}`,
	})
	if cf.SiteKeyValue() != "site-key" {
		t.Fatalf("siteKey = %q, want site-key", cf.SiteKeyValue())
	}
	if cf.SecretKey() != "encrypted-secret-key" {
		t.Fatalf("secretKey = %q, want encrypted-secret-key", cf.SecretKey())
	}
}
