package packagepkg

import (
	"testing"
)

func TestNormalizePurchaseIdempotencyKey(t *testing.T) {
	valid := "550e8400-e29b-41d4-a716-446655440000"
	if got, err := normalizePurchaseIdempotencyKey("  " + valid + "  "); err != nil || got != valid {
		t.Fatalf("valid idempotency key = %q, %v", got, err)
	}
	for _, value := range []string{
		"",
		"too-short",
		"contains spaces 123456",
		"contains/slash/123456",
		"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789___",
	} {
		if _, err := normalizePurchaseIdempotencyKey(value); err == nil {
			t.Fatalf("invalid idempotency key %q was accepted", value)
		}
	}
}
