package content

import (
	"testing"
	"time"
)

func TestDonationDefaultTimeUsesDatabaseBusinessClock(t *testing.T) {
	now := time.Date(2026, 12, 31, 17, 30, 0, 0, time.UTC)
	got, err := normalizeDonationSupporter(CreateDonationSupporterRequest{Nickname: "timezone test", Amount: "1.23"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.DonatedAt != "2027-01-01 01:30:00" {
		t.Fatalf("default donation time used host civil clock: %s", got.DonatedAt)
	}
}
