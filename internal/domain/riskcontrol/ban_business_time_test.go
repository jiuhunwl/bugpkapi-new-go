package riskcontrol

import (
	"testing"
	"time"
)

func TestRiskBanUsesBusinessTimeAndRetainsExpiryLimits(t *testing.T) {
	now := time.Date(2026, 12, 31, 17, 30, 0, 0, time.UTC)
	expected := time.Date(2026, 12, 31, 18, 0, 0, 0, time.UTC)
	for _, input := range []string{"2027-01-01 02:00:00", "2026-12-31T18:00:00Z"} {
		parsed, text, err := parseBanExpiry(input, now)
		if err != nil || !parsed.Equal(expected) || text != "2027-01-01 02:00:00" {
			t.Errorf("risk ban expiry shifted: input=%q parsed=%s text=%q err=%v", input, parsed, text, err)
		}
	}
	for _, input := range []string{"2027-01-01 01:00:00", now.Add(366 * 24 * time.Hour).Format(time.RFC3339), "invalid"} {
		if _, _, err := parseBanExpiry(input, now); err == nil {
			t.Errorf("invalid or out-of-range expiry accepted: %q", input)
		}
	}
}

func TestRiskBanMustRemainFutureAfterStoragePrecision(t *testing.T) {
	now := time.Date(2026, 12, 31, 17, 30, 0, 500_000_000, time.UTC)
	if _, _, err := parseBanExpiry("2026-12-31T17:30:00.900Z", now); err == nil {
		t.Fatal("subsecond future input became an already-expired stored risk ban")
	}
	parsed, text, err := parseBanExpiry("2026-12-31T17:30:01.900Z", now)
	expected := time.Date(2026, 12, 31, 17, 30, 1, 0, time.UTC)
	if err != nil || !parsed.Equal(expected) || text != "2027-01-01 01:30:01" {
		t.Fatalf("parsed and stored instants disagree: %s %q %v", parsed, text, err)
	}
}

func TestRiskBanUpperBoundChecksRawInputBeforeTruncation(t *testing.T) {
	now := time.Date(2026, 12, 31, 17, 30, 0, 500_000_000, time.UTC)
	edge := now.Add(365 * 24 * time.Hour)
	if _, _, err := parseBanExpiry(edge.Format(time.RFC3339Nano), now); err != nil {
		t.Fatalf("exact maximum rejected: %v", err)
	}
	if _, _, err := parseBanExpiry(edge.Add(time.Nanosecond).Format(time.RFC3339Nano), now); err == nil {
		t.Fatal("raw input over maximum was hidden by storage truncation")
	}
}
