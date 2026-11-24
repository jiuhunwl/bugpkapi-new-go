package systemsettings

import (
	"strings"
	"testing"
)

func TestNormalizeCustomCode(t *testing.T) {
	config, err := normalizeCustomCode(CustomCodeConfig{Enabled: 1, CSSCode: "<style>body{color:red}</style>", JSCode: "<script>console.log('ok')</script>"})
	if err != nil {
		t.Fatalf("normalizeCustomCode() error=%v", err)
	}
	if config.Enabled != 1 || config.CSSCode == "" || config.JSCode == "" {
		t.Fatalf("unexpected config: %#v", config)
	}
}

func TestNormalizeCustomCodeRequiresCompleteTags(t *testing.T) {
	for _, request := range []CustomCodeConfig{
		{Enabled: 1, CSSCode: "body{color:red}"},
		{Enabled: 1, CSSCode: "<style>body{}"},
		{Enabled: 1, JSCode: "console.log('missing tag')"},
		{Enabled: 1, JSCode: "<script>console.log('missing close')"},
	} {
		if _, err := normalizeCustomCode(request); err == nil {
			t.Fatalf("incomplete custom code was accepted: %#v", request)
		}
	}
	if _, err := normalizeCustomCode(CustomCodeConfig{
		Enabled: 1,
		CSSCode: "<style media=\"screen\">body{color:red}</style>\n<style>.x{display:none}</style>",
		JSCode:  "<script src=\"https://example.com/a.js\"></script>\n<script>window.ready=true</script>",
	}); err != nil {
		t.Fatalf("complete tag snippets were rejected: %v", err)
	}
}

func TestNormalizeCustomCodeRejectsOversizeAndNUL(t *testing.T) {
	if _, err := normalizeCustomCode(CustomCodeConfig{Enabled: 1, CSSCode: strings.Repeat("x", maxCustomCodeBytes+1)}); err == nil {
		t.Fatal("oversized CSS unexpectedly passed")
	}
	if _, err := normalizeCustomCode(CustomCodeConfig{Enabled: 1, JSCode: "a\x00b"}); err == nil {
		t.Fatal("NUL JavaScript unexpectedly passed")
	}
}
