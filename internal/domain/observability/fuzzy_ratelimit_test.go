package observability

import (
	"testing"
	"time"
)

func TestFuzzyInputTooShort(t *testing.T) {
	cases := map[string]bool{
		"":       false,
		"  ":     false,
		"ab":     false,
		"京":      false,
		"a":      true,
		"1":      true,
		" trace": false,
		"t":      true,
	}
	for input, want := range cases {
		if got := fuzzyInputTooShort(input); got != want {
			t.Fatalf("fuzzyInputTooShort(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestFuzzyRateLimiterAllowsBurstThenRejects(t *testing.T) {
	limiter := newFuzzyRateLimiter(time.Second, 3)
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !limiter.allow(7, now.Add(time.Duration(i)*time.Millisecond)) {
			t.Fatalf("request %d should be allowed", i)
		}
	}
	if limiter.allow(7, now.Add(10*time.Millisecond)) {
		t.Fatalf("4th request within window should be rejected")
	}
	if !limiter.allow(7, now.Add(2*time.Second)) {
		t.Fatalf("request after window should be allowed")
	}
	if !limiter.allow(8, now.Add(11*time.Millisecond)) {
		t.Fatalf("different principal has independent budget")
	}
}
