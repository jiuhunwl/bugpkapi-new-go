package observability

import (
	"strings"
	"testing"
)

func TestSuggestLimitClampedToMax(t *testing.T) {
	if got := clampSuggestLimit(1); got != 1 {
		t.Fatalf("clampSuggestLimit(1) = %d, want 1", got)
	}
	if got := clampSuggestLimit(0); got != defaultSuggestLimit {
		t.Fatalf("clampSuggestLimit(0) = %d, want %d", got, defaultSuggestLimit)
	}
	if got := clampSuggestLimit(-5); got != defaultSuggestLimit {
		t.Fatalf("clampSuggestLimit(-5) = %d, want %d", got, defaultSuggestLimit)
	}
	if got := clampSuggestLimit(100); got != maxSuggestLimit {
		t.Fatalf("clampSuggestLimit(100) = %d, want %d", got, maxSuggestLimit)
	}
}

func TestSuggestKeywordTooShort(t *testing.T) {
	if suggestKeywordTooShort("") {
		t.Fatal("empty keyword should be allowed")
	}
	if !suggestKeywordTooShort("a") {
		t.Fatal("single ASCII character should be rejected")
	}
	if suggestKeywordTooShort("天气") {
		t.Fatal("two CJK characters should be accepted")
	}
	if suggestKeywordTooShort("ab") {
		t.Fatal("two ASCII characters should be accepted")
	}
}

func TestSuggestKeywordOverlong(t *testing.T) {
	if suggestKeywordOverlong("") {
		t.Fatal("empty keyword should not be overlong")
	}
	if suggestKeywordOverlong("abcdefgh") {
		t.Fatal("8-char keyword should be accepted")
	}
	// suggestMaxKeywordLen is 64; a 65-char ASCII keyword must be rejected
	long := strings.Repeat("a", suggestMaxKeywordLen+1)
	if !suggestKeywordOverlong(long) {
		t.Fatal("keyword longer than the max should be rejected")
	}
}
