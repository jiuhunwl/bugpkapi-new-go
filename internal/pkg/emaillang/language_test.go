package emaillang

import "testing"

func TestNormalize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "simplified Chinese", value: "zh-CN", want: Chinese},
		{name: "traditional Chinese", value: "zh-TW", want: Chinese},
		{name: "Hong Kong Chinese", value: "zh-HK", want: Chinese},
		{name: "traditional Chinese script", value: "zh-Hant-TW", want: Chinese},
		{name: "English", value: "en-US", want: English},
		{name: "underscore and case", value: "EN_us", want: English},
		{name: "weighted Chinese", value: "en-US;q=0.7, zh-CN;q=0.9", want: Chinese},
		{name: "weighted English", value: "zh-TW;q=0.4,en-US;q=0.8", want: English},
		{name: "parameter without q keeps default quality", value: "en-US;level=1,zh-CN;q=0.8", want: English},
		{name: "malformed q falls through", value: "en-US;q=NaN,zh-HK;q=0.8", want: Chinese},
		{name: "duplicate q is invalid", value: "en-US;q=0.9;q=0.8,zh-CN;q=0.7", want: Chinese},
		{name: "unsupported", value: "fr-FR", want: ""},
		{name: "invalid", value: "not a locale", want: ""},
		{name: "disabled", value: "en-US;q=0", want: ""},
		{name: "missing", value: "", want: ""},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := Normalize(test.value); got != test.want {
				t.Fatalf("Normalize(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestResolvePriorityAndFallback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                      string
		page, target, browser, site, wantLanguage string
	}{
		{name: "page wins", page: "zh-CN", target: "en-US", browser: "en-US", site: "en", wantLanguage: Chinese},
		{name: "target wins", page: "invalid", target: "en-US", browser: "zh-CN", site: "zh", wantLanguage: English},
		{name: "browser wins", target: "fr", browser: "en-US", site: "zh", wantLanguage: English},
		{name: "site fallback", browser: "invalid", site: "en-US", wantLanguage: English},
		{name: "Chinese default", page: "", target: "", browser: "", site: "", wantLanguage: Chinese},
		{name: "invalid values do not mask browser", page: "xx", target: "broken", browser: "zh-TW", site: "en", wantLanguage: Chinese},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := Resolve(test.page, test.target, test.browser, test.site); got != test.wantLanguage {
				t.Fatalf("Resolve() = %q, want %q", got, test.wantLanguage)
			}
		})
	}
}
