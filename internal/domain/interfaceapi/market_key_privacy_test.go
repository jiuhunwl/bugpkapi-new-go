package interfaceapi

import (
	"encoding/json"
	"strings"
	"testing"

	"bugpk-api/internal/domain/keyaccess"
)

func TestMarketTestKeyReturnsSelectableMetadataWithoutPlaintext(t *testing.T) {
	payload, err := json.Marshal(MarketTestKey{
		HasKey:   true,
		KeyScope: "GLOBAL",
		Keys: []keyaccess.InvokeKeyOption{{
			ID:              "1",
			Name:            "production",
			MaskedKey:       "bp_live_abcd****1234",
			KeyScope:        "GLOBAL",
			SecretAvailable: true,
		}},
		MaskedKey: "bp_live_abcd****1234",
		Message:   "available",
	})
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if strings.Contains(text, "secretKey") {
		t.Fatalf("market key summary exposed a plaintext-shaped field: %s", text)
	}
	if !strings.Contains(text, `"maskedKey":"bp_live_abcd****1234"`) {
		t.Fatalf("market key summary lost masked identifier: %s", text)
	}
	if !strings.Contains(text, `"name":"production"`) || !strings.Contains(text, `"secretAvailable":true`) {
		t.Fatalf("market key summary lost selectable metadata: %s", text)
	}
}
