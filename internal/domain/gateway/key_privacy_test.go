package gateway

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAPIKeyListItemJSONCannotExposePlaintext(t *testing.T) {
	payload, err := json.Marshal(APIKeyItem{
		ID:        "1",
		KeyPrefix: "bp_live_abcd",
		KeySuffix: "1234",
		MaskedKey: "bp_live_abcd****1234",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if strings.Contains(text, "secretKey") || strings.Contains(text, "bp_live_plaintext") {
		t.Fatalf("key list item exposed plaintext contract: %s", text)
	}
	if !strings.Contains(text, `"maskedKey":"bp_live_abcd****1234"`) {
		t.Fatalf("key list item lost masked identifier: %s", text)
	}
}

func TestCreateKeyResultReturnsPlaintext(t *testing.T) {
	payload, err := json.Marshal(CreateKeyResult{
		APIKeyItem: APIKeyItem{ID: "1", MaskedKey: "bp_live_abcd****1234"},
		SecretKey:  "bp_live_plaintext",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if !strings.Contains(text, `"secretKey":"bp_live_plaintext"`) {
		t.Fatalf("create result lost plaintext: %s", text)
	}
}

func TestKeySecretResultReturnsRecoverablePlaintext(t *testing.T) {
	payload, err := json.Marshal(KeySecretResult{SecretKey: "bp_live_plaintext"})
	if err != nil {
		t.Fatal(err)
	}
	if text := string(payload); !strings.Contains(text, `"secretKey":"bp_live_plaintext"`) {
		t.Fatalf("secret result lost recoverable plaintext: %s", text)
	}
}

func TestValidateKeyExpirationRequiresFutureTime(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.Local)
	for _, expiresAt := range []time.Time{now, now.Add(-time.Second)} {
		if err := validateKeyExpiration(&expiresAt, now); err == nil {
			t.Fatalf("accepted non-future expiration %s", expiresAt)
		}
	}
	future := now.Add(time.Second)
	if err := validateKeyExpiration(&future, now); err != nil {
		t.Fatalf("rejected future expiration: %v", err)
	}
	if err := validateKeyExpiration(nil, now); err != nil {
		t.Fatalf("rejected non-expiring key: %v", err)
	}
}

func TestKeyExpiredIncludesExactBoundary(t *testing.T) {
	now := time.Date(2026, time.July, 18, 12, 0, 0, 0, time.Local)
	before := now.Add(-time.Second)
	equal := now
	after := now.Add(time.Second)
	if !keyExpired(&before, now) || !keyExpired(&equal, now) {
		t.Fatal("expired key boundary was accepted")
	}
	if keyExpired(&after, now) || keyExpired(nil, now) {
		t.Fatal("future or non-expiring key was rejected")
	}
}
