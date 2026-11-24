package user

import (
	"testing"
	"time"
)

func TestUserBanCivilTimeDoesNotDependOnInjectedClockZone(t *testing.T) {
	now := time.Date(2026, 12, 31, 17, 30, 0, 0, time.UTC)
	expected := time.Date(2026, 12, 31, 18, 0, 0, 0, time.UTC)
	for _, input := range []string{"2027-01-01 02:00:00", "2026-12-31T18:00:00Z"} {
		parsed, text, err := parseBanUntil(input, now)
		if err != nil || !parsed.Equal(expected) || text != "2027-01-01 02:00:00" {
			t.Errorf("ban expiry shifted: input=%q parsed=%s text=%q err=%v", input, parsed, text, err)
		}
	}
	temporary := 0
	req := SaveUserRequest{BanPermanent: &temporary, BanReason: "test", BanUntil: "2027-01-01 01:00:00"}
	svc := &Service{now: func() time.Time { return now }}
	if err := svc.normalizeBanFields(&req, 0); err == nil {
		t.Fatal("expired business time was accepted as a future temporary ban")
	}
}

func TestUserBanMustRemainFutureAfterStoragePrecision(t *testing.T) {
	now := time.Date(2026, 12, 31, 17, 30, 0, 500_000_000, time.UTC)
	temporary := 0
	req := SaveUserRequest{BanPermanent: &temporary, BanReason: "test", BanUntil: "2026-12-31T17:30:00.900Z"}
	svc := &Service{now: func() time.Time { return now }}
	if err := svc.normalizeBanFields(&req, 0); err == nil {
		t.Fatal("subsecond future input became an already-expired stored ban")
	}
	parsed, text, err := parseBanUntil("2026-12-31T17:30:01.900Z", now)
	expected := time.Date(2026, 12, 31, 17, 30, 1, 0, time.UTC)
	if err != nil || !parsed.Equal(expected) || text != "2027-01-01 01:30:01" {
		t.Fatalf("parsed and stored instants disagree: %s %q %v", parsed, text, err)
	}
}
