package translation

import "testing"

func TestSnapshotResolvesAndMergesFallbacks(t *testing.T) {
	locales := []Locale{
		{Code: "zh-CN", Enabled: true, IsDefault: true},
		{Code: "en-US", Enabled: true, FallbackCode: "zh-CN"},
		{Code: "en-GB", Enabled: true, FallbackCode: "en-US"},
	}
	entries := []entry{
		{ResourceType: "interface", ResourceKey: "42", Field: "name", Locale: "en-US", Value: "Weather API"},
		{ResourceType: "interface", ResourceKey: "42", Field: "remark", Locale: "en-US", Value: "Global weather"},
	}
	snapshot := newSnapshot(locales, entries)

	if got := snapshot.Localize("en-US", "interface", "42")["name"]; got != "Weather API" {
		t.Fatalf("en-US name = %q", got)
	}
	if got := snapshot.Localize("en-GB", "interface", "42")["remark"]; got != "Global weather" {
		t.Fatalf("en-GB fallback remark = %q", got)
	}
	if got := snapshot.Localize("zh-CN", "interface", "42")["name"]; got != "" {
		t.Fatalf("default locale must stay on base data, got %q", got)
	}
}

func TestSnapshotResolvePrecedence(t *testing.T) {
	locales := []Locale{
		{Code: "zh-CN", Enabled: true, IsDefault: true},
		{Code: "en-US", Enabled: true, FallbackCode: "zh-CN"},
	}
	snapshot := newSnapshot(locales, nil)

	tests := []struct {
		name      string
		explicit  string
		appLocale string
		accept    string
		want      string
	}{
		{name: "explicit wins", explicit: "en-US", appLocale: "zh-CN", accept: "zh-CN", want: "en-US"},
		{name: "unsupported explicit falls through", explicit: "fr-FR", appLocale: "en-US", accept: "zh-CN", want: "en-US"},
		{name: "regional variant maps to primary", explicit: "en-GB", want: "en-US"},
		{name: "traditional chinese falls back to default", explicit: "zh-TW", want: "zh-CN"},
		{name: "accept quality", accept: "fr-FR;q=0.9, en-US;q=0.8, zh-CN;q=0.7", want: "en-US"},
		{name: "default fallback", accept: "de-DE", want: "zh-CN"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := snapshot.resolve(test.explicit, test.appLocale, test.accept); got != test.want {
				t.Fatalf("resolve() = %q, want %q", got, test.want)
			}
		})
	}
}
