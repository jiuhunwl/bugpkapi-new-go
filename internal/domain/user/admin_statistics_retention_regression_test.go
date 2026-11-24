package user

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
)

// Retention removes raw/minute rows after three days, never the durable day
// projection. A seven-day trend must retain the certified historical values.
func TestAdminCallTrendRetainsDailyHistoryAfterMinutePrune(t *testing.T) {
	today := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.Contains(q, "c.last_log_id,c.last_completed_at"):
			return testdb.Rows([]driver.Value{int64(200), time.Now(), "", "NONE", "IDLE", int64(0), int64(200), today}), nil
		case strings.Contains(q, "from bp_call_api_metric_day"):
			return testdb.Rows([]driver.Value{"10-15", int64(100)}, []driver.Value{"10-21", int64(10)}), nil
		case strings.Contains(q, "from bp_call_metric_day"):
			// The latest day stays covered by the minute layer, which the day
			// validator cross-checks below.
			return testdb.Rows(
				[]driver.Value{"10-15", int64(100), int64(85), int64(10), int64(30), "7.5000", int64(100)},
				[]driver.Value{"10-21", int64(10), int64(8), int64(2), int64(4), "1.0000", int64(10)},
			), nil
		case strings.Contains(q, "from bp_call_metric_minute"):
			return testdb.Rows([]driver.Value{"10-21", int64(10)}), nil
		case strings.Contains(q, "select exists"):
			return testdb.Rows([]driver.Value{int64(0)}), nil
		default:
			return nil, fmt.Errorf("unexpected query: %s", q)
		}
	}})
	got, err := NewRepository(db).adminCallTrendSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != 110 || got.TotalCalls[0] != 100 || got.SuccessCalls[0] != 85 || got.FailedCalls[0] != 10 || got.BillableCalls[0] != 30 || got.ChargeAmounts[0] != "7.5000" {
		t.Fatalf("retention erased real day-grain metrics: %+v", got)
	}
}

// A broken historical day must not cause healthy today's cards to go blank.
func TestAdminWorkbenchHistoricalFailureDoesNotBlankToday(t *testing.T) {
	p := &overviewIsolationProbe{repair: "NONE"}
	base := &overviewIsolationConn{p: p}
	today := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(q, "from bp_call_api_metric_day") {
			if start, ok := args[0].Value.(time.Time); ok && start.Before(today) {
				return testdb.Rows([]driver.Value{"10-02", int64(5000)}, []driver.Value{"10-03", int64(25)}), nil
			}
		}
		if strings.Contains(q, "select exists") && strings.Contains(q, "from bp_interface_call_log") && len(args) > 0 {
			if start, ok := args[0].Value.(time.Time); ok && start.Before(today) {
				return testdb.Rows([]driver.Value{int64(1)}), nil
			}
		}
		if strings.Contains(q, "from bp_call_metric_day") {
			return testdb.Rows(), nil
		}
		return base.QueryContext(ctx, q, args)
	}})
	svc := &Service{repo: NewRepository(db), now: time.Now}
	got, err := svc.loadAdminWorkbenchStats(context.Background(), []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Overview.TodayCalls != 25 || got.Overview.TodayRevenue != "88.0000" || slices.Contains(got.Overview.UnavailableFields, "todayCalls") {
		t.Fatalf("historical failure contaminated healthy today: %+v", got)
	}
	if !slices.Contains(got.DegradedSections, AdminWorkbenchSectionCallTrend) {
		t.Fatalf("missing history was certified: %+v", got)
	}
}
func TestDailyProjectionRepairProtectsItsActualReaders(t *testing.T) {
	for _, tc := range []struct {
		mask    uint16
		section AdminWorkbenchSection
	}{
		{adminProjectionMetricDay, AdminWorkbenchSectionCallTrend},
		{adminProjectionAPIDay, AdminWorkbenchSectionHotAPIs},
		{adminProjectionUserMetricDay, AdminWorkbenchSectionActiveUsers},
	} {
		state := newAdminAnalyticsRepairState("RUNNING", "CLEAR", uint64(tc.mask), sql.NullTime{}, "", time.Now())
		if !state.degrades(tc.section) {
			t.Fatalf("daily repair bit %d does not protect %s", tc.mask, tc.section)
		}
		if state.degrades(AdminWorkbenchSectionOverview) {
			t.Fatalf("unrelated daily repair blanked today's minute-backed cards: mask=%d", tc.mask)
		}
	}
}
func TestHistoricalDailyBucketsCannotCertifyResetCheckpoint(t *testing.T) {
	today := time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.Contains(q, "c.last_log_id,c.last_completed_at"):
			return testdb.Rows([]driver.Value{int64(0), time.Now(), "", "NONE", "IDLE", int64(0), int64(0), today}), nil
		case strings.Contains(q, "select exists"):
			return testdb.Rows([]driver.Value{int64(0)}), nil
		case strings.Contains(q, "from bp_call_metric_day"):
			return testdb.Rows([]driver.Value{"10-15", int64(10), int64(10), int64(0), int64(0), "0.0000"}), nil
		case strings.Contains(q, "from bp_call_api_metric_day"):
			return testdb.Rows([]driver.Value{"10-15", int64(10)}), nil
		case strings.Contains(q, "from bp_call_metric_minute"):
			return testdb.Rows(), nil
		default:
			return nil, fmt.Errorf("unexpected query: %s", q)
		}
	}})
	if _, err := NewRepository(db).adminCallTrendSnapshot(context.Background()); err == nil {
		t.Fatal("orphan historical daily projection certified without a committed cursor")
	}
}
