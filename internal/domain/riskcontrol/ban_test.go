package riskcontrol

import (
	"testing"
	"time"
)

func TestParseBanExpiryValidatesFutureAndMaximumDuration(t *testing.T) {
	now := time.Date(2026, time.August, 11, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	parsed, normalized, err := parseBanExpiry("2026-08-11 11:30:00", now)
	if err != nil {
		t.Fatalf("parseBanExpiry() error = %v", err)
	}
	if !parsed.After(now) || normalized != "2026-08-11 11:30:00" {
		t.Fatalf("parsed=%v normalized=%q", parsed, normalized)
	}
	if _, _, err := parseBanExpiry("2026-08-11 09:59:59", now); err == nil {
		t.Fatal("past expiry was accepted")
	}
	if _, _, err := parseBanExpiry("2027-08-12 10:00:01", now); err == nil {
		t.Fatal("expiry beyond one year was accepted")
	}
}
