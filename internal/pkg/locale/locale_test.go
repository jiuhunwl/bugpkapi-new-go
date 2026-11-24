package locale

import (
	"reflect"
	"testing"
)

func TestNormalizeTag(t *testing.T) {
	tests := map[string]string{
		"zh-CN":    "zh-CN",
		"zh_cn":    "zh-CN",
		"EN-us":    "en-US",
		"en":       "en",
		" zh-Hant": "zh-Hant",
		"en-001":   "en-001",
		"x":        "",
		"en-":      "",
		"123":      "",
		"zh-CN-":   "",
		"*":        "",
	}
	for input, want := range tests {
		if got := NormalizeTag(input); got != want {
			t.Errorf("NormalizeTag(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestParseAcceptLanguageOrdersByQuality(t *testing.T) {
	got := ParseAcceptLanguage("en-US;q=0.6, zh-CN;q=0.9, en;q=0.6, *;q=0.2, en-US;q=0.1")
	want := []string{"zh-CN", "en-US", "en"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseAcceptLanguage() = %v, want %v", got, want)
	}
}

func TestParseAcceptLanguageDropsDisabledAndMalformed(t *testing.T) {
	got := ParseAcceptLanguage("fr-FR;q=0, de-DE;q=abc, ja-JP")
	want := []string{"ja-JP"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseAcceptLanguage() = %v, want %v", got, want)
	}
}

func TestPrimaryLanguage(t *testing.T) {
	if got := PrimaryLanguage("zh-Hant-TW"); got != "zh" {
		t.Fatalf("PrimaryLanguage() = %q, want zh", got)
	}
	if got := PrimaryLanguage("en"); got != "en" {
		t.Fatalf("PrimaryLanguage() = %q, want en", got)
	}
}
