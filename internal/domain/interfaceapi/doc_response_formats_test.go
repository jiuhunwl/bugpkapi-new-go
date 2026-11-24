package interfaceapi

import (
	"strings"
	"testing"
)

func TestNormalizeDocResponseFormats(t *testing.T) {
	if _, err := normalizeDocResponseFormats(nil); err != nil {
		t.Fatalf("nil slice must mean \"not provided\" for older clients: %v", err)
	}
	if _, err := normalizeDocResponseFormats([]DocResponseFormat{}); err == nil {
		t.Fatal("an explicitly empty list must be rejected: the docs page needs at least one format")
	}

	formats, err := normalizeDocResponseFormats([]DocResponseFormat{
		{Type: "json", Example: `{"code":200}`},
		{Type: " sse ", Example: "data: {\"progress\":1}\n\n"},
	})
	if err != nil {
		t.Fatalf("normalizeDocResponseFormats: %v", err)
	}
	if formats[0].Type != "JSON" || formats[1].Type != "SSE" {
		t.Fatalf("types must be uppercased and trimmed: %+v", formats)
	}

	tooMany := make([]DocResponseFormat, 0, docResponseFormatMaxEntries+1)
	for _, kind := range append([]string{}, docResponseFormatTypes...) {
		tooMany = append(tooMany, DocResponseFormat{Type: kind})
	}
	tooMany = append(tooMany, DocResponseFormat{Type: "JSON"})
	if _, err := normalizeDocResponseFormats(tooMany); err == nil {
		t.Fatal("more than the maximum entries must be rejected")
	}

	duplicates := []DocResponseFormat{{Type: "JSON"}, {Type: "json"}}
	if _, err := normalizeDocResponseFormats(duplicates); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("duplicate types must be rejected, got %v", err)
	}

	if _, err := normalizeDocResponseFormats([]DocResponseFormat{{Type: "GRAPIB"}}); err == nil {
		t.Fatal("unknown types must be rejected")
	}

	long := make([]byte, docResponseFormatExampleMax+1)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := normalizeDocResponseFormats([]DocResponseFormat{{Type: "TEXT", Example: string(long)}}); err == nil {
		t.Fatal("oversized examples must be rejected")
	}
}

func TestPublicDocResponseFormatsFallback(t *testing.T) {
	multi := []DocResponseFormat{{Type: "JSON", Example: "{}"}, {Type: "TEXT", Example: "ok"}}
	if got := publicDocResponseFormats(InterfaceItem{
		DocResponseFormats: `[{"type":"JSON","example":"{}"},{"type":"TEXT","example":"ok"}]`,
	}); len(got) != 2 || got[0].Type != "JSON" || got[1].Type != "TEXT" {
		t.Fatalf("configured formats must win: %+v", got)
	}
	_ = multi

	// Legacy interfaces keep rendering their single example.
	legacy := publicDocResponseFormats(InterfaceItem{DocResponseType: "TEXT", DocResponseExample: "hello"})
	if len(legacy) != 1 || legacy[0].Type != "TEXT" || legacy[0].Example != "hello" {
		t.Fatalf("legacy pair must synthesize one entry: %+v", legacy)
	}

	legacyDefault := publicDocResponseFormats(InterfaceItem{DocResponseType: "JSON"})
	if len(legacyDefault) != 1 || legacyDefault[0].Type != "JSON" {
		t.Fatalf("legacy type without example must still render the format: %+v", legacyDefault)
	}

	// Corrupt JSON silently falls back to the legacy pair.
	if got := publicDocResponseFormats(InterfaceItem{
		DocResponseFormats: `{broken`,
		DocResponseType:    "XML",
		DocResponseExample: "<a/>",
	}); len(got) != 1 || got[0].Type != "XML" {
		t.Fatalf("invalid column payload must fall back to the legacy pair: %+v", got)
	}

	if got := publicDocResponseFormats(InterfaceItem{}); got != nil {
		t.Fatalf("an interface with no docs at all must expose no formats: %+v", got)
	}
}
