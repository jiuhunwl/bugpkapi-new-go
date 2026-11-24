package user

import (
	"reflect"
	"testing"
	"time"
)

func TestWorkbenchCalendarUsesDatabaseBusinessDay(t *testing.T) {
	now := time.Date(2026, 12, 31, 17, 30, 0, 0, time.UTC) // Jan 1, 01:30 in the stored business zone.
	t.Run("daily labels", func(t *testing.T) {
		if got := dayLabels(now, 2); !reflect.DeepEqual(got, []string{"12-31", "01-01"}) {
			t.Fatalf("UTC host shifted day labels: %v", got)
		}
	})
	t.Run("monthly labels", func(t *testing.T) {
		if got := monthLabels(now, 2); !reflect.DeepEqual(got, []string{"2026-12", "2027-01"}) {
			t.Fatalf("UTC host shifted month labels: %v", got)
		}
	})
	t.Run("daily values match those labels", func(t *testing.T) {
		got := buildDailyCallTrend(now, 2, map[string]int64{"2026-12-31": 2, "2027-01-01": 3})
		if !reflect.DeepEqual(got.Values, []int64{2, 3}) || got.Total != 5 {
			t.Fatalf("correct business-day values lost: %+v", got)
		}
	})
	t.Run("today window", func(t *testing.T) {
		want := time.Date(2027, 1, 1, 0, 0, 0, 0, time.FixedZone("expected +08", 8*60*60))
		got := adminAccessWindowStart(AdminAccessRangeToday, now)
		if !got.Equal(want) {
			t.Fatalf("today includes hours outside the DB business day: got %s want %s", got, want)
		}
	})
	t.Run("seven-day window", func(t *testing.T) {
		want := time.Date(2026, 12, 26, 0, 0, 0, 0, time.FixedZone("expected +08", 8*60*60))
		got := adminAccessWindowStart(AdminAccessRange7d, now)
		if !got.Equal(want) {
			t.Fatalf("seven-day lower bound shifted: got %s want %s", got, want)
		}
	})
}
