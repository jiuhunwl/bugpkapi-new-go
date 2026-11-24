package auth

import "testing"

func TestImplementedCaptchaProvidersAdvertisesAllWorkingProviders(t *testing.T) {
	providers := implementedCaptchaProviders()
	want := []string{ProviderPOW, ProviderGeeTest4, ProviderCloudflare}
	if len(providers) != len(want) {
		t.Fatalf("implemented captcha providers = %v, want %v", providers, want)
	}
	for index, provider := range want {
		if providers[index] != provider {
			t.Fatalf("implemented captcha providers = %v, want %v", providers, want)
		}
	}
}
