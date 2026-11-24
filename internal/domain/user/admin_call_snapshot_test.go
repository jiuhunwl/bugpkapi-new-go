package user

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	"github.com/go-sql-driver/mysql"
)

// unknownColumn is the real driver error a database without the additive
// checkpoint columns returns, so the fallback is exercised against the exact
// message shape the classifier reads.
func unknownColumn(name string) error {
	return &mysql.MySQLError{
		Number:   1054,
		SQLState: [5]byte{'4', '2', 'S', '2', '2'},
		Message:  "Unknown column '" + name + "' in 'field list'",
	}
}

func TestAdminCallSnapshotToleratesCheckpointWithoutRepairCursorColumns(t *testing.T) {
	// A database that never applied migration 000092 has no repair_phase or
	// repair_projection_mask while its schema version already claims to be
	// current. The overview must keep publishing certifiable numbers instead of
	// failing on an absent additive column.
	probes := 0
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.Contains(q, "c.last_log_id,c.last_completed_at"):
			probes++
			if strings.Contains(q, "repair_phase") {
				return nil, unknownColumn("c.repair_phase")
			}
			return testdb.Rows([]driver.Value{int64(5), time.Now(), "", "COMPLETE", int64(5), time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)}), nil
		case strings.Contains(q, "from bp_call_api_metric_day"):
			return testdb.Rows([]driver.Value{"10-11", int64(5)}), nil
		case strings.Contains(q, "group by date_format(l.created_at"):
			// The source oracle must be able to recompute the same 5 calls the
			// projections claim, or the tolerance gate refuses the snapshot.
			return testdb.Rows([]driver.Value{"10-11", int64(5)}), nil
		case strings.Contains(q, "from bp_interface_call_log l force index"):
			return testdb.Rows([]driver.Value{int64(0)}), nil
		case strings.Contains(q, "from bp_call_metric_minute"):
			return testdb.Rows([]driver.Value{"10-11", int64(5), int64(5), int64(0), int64(0), "0.0000", int64(5)}), nil
		default:
			return nil, fmt.Errorf("unexpected snapshot query: %s", q)
		}
	}})
	var got AdminWorkbenchOverview
	if err := NewRepository(db).adminOverviewCallsSnapshot(context.Background(), &got); err != nil {
		t.Fatalf("000090-era checkpoint shape must not blank the overview: %v", err)
	}
	if probes != 2 {
		t.Fatalf("probe must fall back exactly once, got %d attempts", probes)
	}
	if got.TodayCalls != 5 || got.TodaySuccessCalls != 5 {
		t.Fatalf("certified numbers were not published: %+v", got)
	}
}

func TestAdminCallSnapshotDegradesWhenCheckpointStructureIsUnusable(t *testing.T) {
	// A checkpoint that predates the durable repair task cannot be certified at
	// all. It must surface as the shared unavailable verdict so the section
	// degrades, never as a raw database error.
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
		if strings.Contains(q, "c.last_log_id,c.last_completed_at") {
			return nil, unknownColumn("c.repair_status")
		}
		return nil, fmt.Errorf("unexpected snapshot query: %s", q)
	}})
	var got AdminWorkbenchOverview
	err := NewRepository(db).adminOverviewCallsSnapshot(context.Background(), &got)
	if !errors.Is(err, errAdminAnalyticsProjectionUnavailable) {
		t.Fatalf("missing checkpoint structure must degrade instead of failing raw: %v", err)
	}
}

func TestAdminCallSnapshotCannotCertifyUntrackedProjection(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		cursor                     driver.Value
		source, existing, returned int64
		wantError                  bool
	}{
		{"missing checkpoint with populated projection", nil, 0, 25, 25, true},
		{"reset checkpoint with populated projection", int64(0), 100, 25, 125, true},
		{"true empty database", nil, 0, 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
				switch {
				case strings.Contains(q, "c.last_log_id,c.last_completed_at"):
					return testdb.Rows([]driver.Value{tc.cursor, time.Now(), "", "NONE", "IDLE", int64(0), tc.source, time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)}), nil
				case strings.Contains(q, "select exists") && strings.Contains(q, "from bp_call_metric_minute"):
					return testdb.Rows([]driver.Value{tc.existing}), nil
				case strings.Contains(q, "from bp_call_api_metric_day"):
					return testdb.Rows(), nil
				case strings.Contains(q, "select exists") && strings.Contains(q, "from bp_interface_call_log"):
					// The cheap gap probe: the source still holds processed rows for the
					// uncovered day, so existence is refused without any tolerance.
					return testdb.Rows([]driver.Value{int64(1)}), nil
				case strings.Contains(q, "from bp_call_metric_minute"):
					// The explicit window must come from database midnight, not each later
					// statement's CURRENT_DATE or the application's host timezone.
					if len(args) < 2 || !args[0].Value.(time.Time).Equal(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)) || !args[1].Value.(time.Time).Equal(time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)) {
						return nil, fmt.Errorf("statistics window was not frozen: %v", args)
					}
					if tc.returned == 0 {
						return testdb.Rows(), nil
					}
					return testdb.Rows([]driver.Value{"10-10", tc.returned, tc.returned, int64(0), int64(0), "0.0000", tc.existing}), nil
				default:
					return nil, fmt.Errorf("unexpected snapshot query: %s", q)
				}
			}})
			var got AdminWorkbenchOverview
			err := NewRepository(db).adminOverviewCallsSnapshot(context.Background(), &got)
			if (err != nil) != tc.wantError {
				t.Fatalf("untracked data certified or true zero lost: got=%+v err=%v", got, err)
			}
			if !tc.wantError && (got.TodayCalls != 0 || got.TodayChargeAmount != "0.0000") {
				t.Fatalf("empty source not represented as real zero: %+v", got)
			}
		})
	}
}

func TestAdminCallTrendUsesDatabaseDayAcrossMidnight(t *testing.T) {
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.Contains(q, "c.last_log_id,c.last_completed_at"):
			return testdb.Rows([]driver.Value{int64(5), time.Now(), "", "NONE", "IDLE", int64(0), int64(5), time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)}), nil
		case strings.Contains(q, "from bp_call_api_metric_day"):
			return testdb.Rows([]driver.Value{"10-05", int64(3)}, []driver.Value{"10-11", int64(2)}), nil
		case strings.Contains(q, "group by date_format(l.created_at"):
			// The oracle recomputes exactly what both projections claim; without
			// it the tolerance gate could not certify the seven-day window.
			return testdb.Rows([]driver.Value{"10-05", int64(3)}, []driver.Value{"10-11", int64(2)}), nil
		case strings.Contains(q, "from bp_call_metric_minute"):
			return testdb.Rows([]driver.Value{"10-05", int64(3)}, []driver.Value{"10-11", int64(2)}), nil
		case strings.Contains(q, "from bp_call_metric_day"):
			if len(args) != 2 || !args[0].Value.(time.Time).Equal(time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)) || !args[1].Value.(time.Time).Equal(time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)) {
				return nil, fmt.Errorf("wrong frozen window: %v", args)
			}
			return testdb.Rows([]driver.Value{"10-05", int64(3), int64(2), int64(1), int64(0), "0.0000", int64(3)}, []driver.Value{"10-11", int64(2), int64(1), int64(1), int64(0), "0.0000", int64(2)}), nil
		case strings.Contains(q, "select exists"):
			return testdb.Rows([]driver.Value{int64(0)}), nil
		default:
			return nil, fmt.Errorf("unexpected snapshot query: %s", q)
		}
	}})
	oldLabels := []string{"10-04", "10-05", "10-06", "10-07", "10-08", "10-09", "10-10"}
	got, err := NewRepository(db).AdminCallTrend(withoutAdminRawLogFallback(context.Background()), oldLabels)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Labels) != 7 || got.Labels[0] != "10-05" || got.Labels[6] != "10-11" || got.Total != 5 || got.TotalCalls[0] != 3 || got.TotalCalls[6] != 2 {
		t.Fatalf("midnight silently discarded the new day or invented an old zero day: %+v", got)
	}
}

// The nightly cleanup/worker race legitimately leaves a handful of rows of
// drift between the projections and the source. Exact per-day equality turned
// that into three separate full-console outages over 2- to 38-row diffs.
func TestAdminCallSnapshotToleratesSingleDigitSourceDrift(t *testing.T) {
	testAdminCallSnapshotDrift(t, 5000, 4998, false)
}

func TestAdminCallSnapshotToleratesRecordedCleanupDrift(t *testing.T) {
	testAdminCallSnapshotDrift(t, 5000, 4962, false)
}

func TestAdminCallSnapshotRejectsLostSmallBatch(t *testing.T) {
	testAdminCallSnapshotDrift(t, 301, 1, true)
}

func testAdminCallSnapshotDrift(t *testing.T, dailyCalls, minuteCalls int64, wantUnavailable bool) {
	t.Helper()
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.Contains(q, "c.last_log_id,c.last_completed_at"):
			return testdb.Rows([]driver.Value{int64(5000), time.Now(), "", "NONE", "IDLE", int64(0), int64(5000), time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)}), nil
		case strings.Contains(q, "group by date_format(l.created_at"):
			return testdb.Rows([]driver.Value{"10-11", dailyCalls}), nil
		case strings.Contains(q, "from bp_call_api_metric_day"):
			return testdb.Rows([]driver.Value{"10-11", dailyCalls}), nil
		case strings.Contains(q, "from bp_call_metric_minute"):
			return testdb.Rows([]driver.Value{"10-11", minuteCalls, minuteCalls, int64(0), int64(0), "0.0000", minuteCalls}), nil
		default:
			return nil, fmt.Errorf("unexpected snapshot query: %s", q)
		}
	}})
	var got AdminWorkbenchOverview
	err := NewRepository(db).adminOverviewCallsSnapshot(context.Background(), &got)
	if wantUnavailable {
		if !errors.Is(err, errAdminAnalyticsProjectionUnavailable) {
			t.Fatalf("lost small batch was certified: %+v, %v", got, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("recorded cleanup drift must not blank the console: %v", err)
	}
	if got.TodayCalls != minuteCalls {
		t.Fatalf("certified numbers were not published: %+v", got)
	}
}

// Since v1.2.30 a 404 log row counts toward call_count but toward neither
// success_count nor failure_count, so successes+failures may legitimately fall
// short of calls. The scan must publish such aggregates instead of refusing
// them, while a bucket whose classified counts exceed its call total stays
// rejected.
func TestAdminCallSnapshotToleratesUnclassified404Rows(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		calls, successes, failures int64
		wantError                  bool
	}{
		// calls=10 with successes=7 and failures=2 leaves one row unclassified:
		// the 404 delta the v1.2.30 statistics scope admits.
		{"404 delta leaves successes+failures below calls", 10, 7, 2, false},
		// A genuinely inconsistent bucket still fails: 11 classified rows cannot
		// exceed 10 recorded calls.
		{"classified counts exceeding calls stay rejected", 10, 7, 4, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
				switch {
				case strings.Contains(q, "c.last_log_id,c.last_completed_at"):
					return testdb.Rows([]driver.Value{int64(10), time.Now(), "", "NONE", "IDLE", int64(0), int64(10), time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)}), nil
				case strings.Contains(q, "from bp_call_api_metric_day"):
					return testdb.Rows([]driver.Value{"10-11", tc.calls}), nil
				case strings.Contains(q, "group by date_format(l.created_at"):
					return testdb.Rows([]driver.Value{"10-11", tc.calls}), nil
				case strings.Contains(q, "from bp_call_metric_minute"):
					return testdb.Rows([]driver.Value{"10-11", tc.calls, tc.successes, tc.failures, int64(0), "0.0000", tc.calls}), nil
				default:
					return nil, fmt.Errorf("unexpected snapshot query: %s", q)
				}
			}})
			var got AdminWorkbenchOverview
			err := NewRepository(db).adminOverviewCallsSnapshot(context.Background(), &got)
			if tc.wantError {
				if err == nil {
					t.Fatalf("inconsistent bucket was certified: %+v", got)
				}
				// The rejection must come from the three-way scan invariant itself,
				// not from an unrelated gate: this error is the bare scan verdict,
				// while every checkpoint/tolerance gate wraps
				// errAdminAnalyticsProjectionUnavailable.
				if !strings.Contains(err.Error(), "inconsistent admin call aggregate") || errors.Is(err, errAdminAnalyticsProjectionUnavailable) {
					t.Fatalf("rejection came from an unexpected gate: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("404 delta must not blank the overview: %v", err)
			}
			if got.TodayCalls != tc.calls || got.TodaySuccessCalls != tc.successes || got.TodayFailedCalls != tc.failures {
				t.Fatalf("certified numbers were not published: %+v", got)
			}
		})
	}
}

// The tolerance must stay below one aggregation batch: silently losing a whole
// 2000-row batch is exactly the defect the gate exists to catch.
func TestAdminCallSnapshotRejectsBatchSizedSourceDrift(t *testing.T) {
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.Contains(q, "c.last_log_id,c.last_completed_at"):
			return testdb.Rows([]driver.Value{int64(5000), time.Now(), "", "NONE", "IDLE", int64(0), int64(5000), time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)}), nil
		case strings.Contains(q, "group by date_format(l.created_at"):
			return testdb.Rows([]driver.Value{"10-11", int64(5000)}), nil
		case strings.Contains(q, "from bp_call_api_metric_day"):
			return testdb.Rows([]driver.Value{"10-11", int64(5000)}), nil
		case strings.Contains(q, "from bp_call_metric_minute"):
			return testdb.Rows([]driver.Value{"10-11", int64(2000), int64(2000), int64(0), int64(0), "0.0000", int64(2000)}), nil
		default:
			return nil, fmt.Errorf("unexpected snapshot query: %s", q)
		}
	}})
	var got AdminWorkbenchOverview
	err := NewRepository(db).adminOverviewCallsSnapshot(context.Background(), &got)
	if !errors.Is(err, errAdminAnalyticsProjectionUnavailable) {
		t.Fatalf("a batch-sized drift was certified: %+v err=%v", got, err)
	}
}

// Existence has no tolerance: even a tiny deployment losing a whole day's
// rollup must refuse instead of publishing a fabricated zero.
func TestAdminCallSnapshotRejectsMissingDayInsideSourceRetention(t *testing.T) {
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.Contains(q, "c.last_log_id,c.last_completed_at"):
			return testdb.Rows([]driver.Value{int64(17), time.Now(), "", "NONE", "IDLE", int64(0), int64(17), time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)}), nil
		case strings.Contains(q, "group by date_format(l.created_at"):
			return testdb.Rows([]driver.Value{"10-11", int64(17)}), nil
		case strings.Contains(q, "from bp_call_api_metric_day"):
			return testdb.Rows(), nil
		case strings.Contains(q, "select exists") && strings.Contains(q, "from bp_interface_call_log"):
			// The cheap gap probe: the source still holds processed rows for the
			// uncovered day, so existence is refused without any tolerance.
			return testdb.Rows([]driver.Value{int64(1)}), nil
		case strings.Contains(q, "from bp_call_metric_minute"):
			return testdb.Rows(), nil
		default:
			return nil, fmt.Errorf("unexpected snapshot query: %s", q)
		}
	}})
	var got AdminWorkbenchOverview
	err := NewRepository(db).adminOverviewCallsSnapshot(context.Background(), &got)
	if !errors.Is(err, errAdminAnalyticsProjectionUnavailable) {
		t.Fatalf("an uncovered day inside source retention was certified: %+v err=%v", got, err)
	}
}

// The minute layer follows the retained call log while the metric/day layer
// keeps its history, so days older than the log's retention exist in one layer
// only. Demanding cross-layer equality for them refused every healthy
// deployment once the day layer stopped being truncated; such days must stay
// trusted from the gates that certified them while their rows were still
// present.
func TestAdminCallSnapshotAllowsHistoryOlderThanSourceRetention(t *testing.T) {
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
		switch {
		case strings.Contains(q, "c.last_log_id,c.last_completed_at"):
			return testdb.Rows([]driver.Value{int64(25), time.Now(), "", "NONE", "IDLE", int64(0), int64(25), time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)}), nil
		case strings.Contains(q, "group by date_format(l.created_at"):
			// The log only still holds today; older days are gone from the source.
			return testdb.Rows([]driver.Value{"10-11", int64(25)}), nil
		case strings.Contains(q, "from bp_call_api_metric_day"):
			// The day layer keeps its history: six days the minute layer cannot have.
			return testdb.Rows([]driver.Value{"10-05", int64(5000)}, []driver.Value{"10-11", int64(25)}), nil
		case strings.Contains(q, "from bp_call_metric_day"):
			return testdb.Rows([]driver.Value{"10-05", int64(5000), int64(5000), int64(0), int64(5000), "125.0000", int64(5000)}, []driver.Value{"10-11", int64(25), int64(24), int64(1), int64(0), "0.0000", int64(25)}), nil
		case strings.Contains(q, "from bp_call_metric_minute"):
			return testdb.Rows([]driver.Value{"10-11", int64(25)}), nil
		case strings.Contains(q, "select exists") && strings.Contains(q, "from bp_interface_call_log"):
			// The gap probe for days the minute layer does not cover: the log has
			// already been rebuilt, so nothing can vouch for those days and the
			// retained day-layer history stands.
			return testdb.Rows([]driver.Value{int64(0)}), nil
		default:
			return nil, fmt.Errorf("unexpected snapshot query: %s", q)
		}
	}})
	got, err := NewRepository(db).AdminCallTrend(withoutAdminRawLogFallback(context.Background()), []string{"10-05", "10-06", "10-07", "10-08", "10-09", "10-10", "10-11"})
	if err != nil {
		t.Fatalf("retained day-layer history must stay publishable: %v", err)
	}
	if got.Total != 5025 || got.TotalCalls[0] != 5000 || got.TotalCalls[6] != 25 || got.ChargeAmounts[0] != "125.0000" {
		t.Fatalf("history beyond source retention was altered: %+v", got)
	}
}
