package user

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/testdb"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type callTrendFreshnessFixture struct {
	checkpointRows  [][]driver.Value
	checkpointError error
	emptyRollup     bool
	noSource        bool
	lastError       string
}

func openCallTrendFreshnessDB(t *testing.T, fixture callTrendFreshnessFixture) (*sql.DB, *overviewIsolationProbe) {
	t.Helper()
	probe := &overviewIsolationProbe{zeroValues: fixture.emptyRollup && fixture.noSource}
	base := &overviewIsolationConn{p: probe}
	db := testdb.OpenStub(t, testdb.SQLStub{Begin: testdb.ReadOnlyTransaction, Query: func(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
		q := strings.ToLower(strings.Join(strings.Fields(query), " "))
		switch {
		case strings.Contains(q, "c.last_log_id,c.last_completed_at"):
			if len(args) != 1 || args[0].Value != "call_log_rollup" {
				return nil, fmt.Errorf("unexpected snapshot arguments: %v", args)
			}
			if fixture.checkpointError != nil {
				return nil, fixture.checkpointError
			}
			var completed, cursor driver.Value
			source := int64(17)
			if len(fixture.checkpointRows) > 0 {
				completed = fixture.checkpointRows[0][0]
				cursor = int64(17)
			}
			if fixture.noSource {
				source = 0
				cursor = nil
			}
			return testdb.Rows([]driver.Value{cursor, completed, fixture.lastError, "NONE", "IDLE", int64(0), source, time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)}), nil
		case strings.Contains(q, "select last_completed_at from bp_analytics_checkpoint"):
			if len(args) != 1 || args[0].Value != "call_log_rollup" {
				return nil, fmt.Errorf("unexpected checkpoint arguments: %v", args)
			}
			if fixture.checkpointError != nil {
				return nil, fixture.checkpointError
			}
			return testdb.Rows(fixture.checkpointRows...), nil
		case strings.Contains(q, "select exists") && strings.Contains(q, "from bp_call_metric_minute"):
			if fixture.emptyRollup {
				return testdb.Rows([]driver.Value{int64(0)}), nil
			}
			return testdb.Rows([]driver.Value{int64(1)}), nil
		case strings.Contains(q, "from bp_call_metric_day"):
			if fixture.emptyRollup {
				return testdb.Rows(), nil
			}
			return testdb.Rows([]driver.Value{"10-03", int64(17), int64(15), int64(2), int64(3), "0.3000", int64(17)}), nil
		case strings.Contains(q, "from bp_call_api_metric_day"):
			if fixture.emptyRollup {
				return testdb.Rows(), nil
			}
			return testdb.Rows([]driver.Value{"10-03", int64(17)}), nil
		case strings.Contains(q, "group by date_format(l.created_at"):
			// The source oracle recomputes what the projections claim; with no
			// source traffic at all there is nothing to recompute.
			if fixture.noSource {
				return testdb.Rows(), nil
			}
			return testdb.Rows([]driver.Value{"10-03", int64(17)}), nil
		case strings.Contains(q, "from bp_call_metric_minute") && !strings.Contains(q, "source.bucket_start") && strings.Contains(q, "group by"):
			if fixture.emptyRollup {
				return testdb.Rows(), nil
			}
			return testdb.Rows([]driver.Value{"10-03", int64(17)}), nil
		case strings.Contains(q, "select exists") && strings.Contains(q, "from bp_interface_call_log"):
			exists := int64(1)
			if strings.Contains(q, "l.created_at>=?") {
				exists = 0
			}
			if fixture.noSource {
				exists = 0
			}
			return testdb.Rows([]driver.Value{exists}), nil
		default:
			return base.QueryContext(ctx, query, args)
		}
	}})
	return db, probe
}

func TestAdminCallTrendRequiresFreshProjection(t *testing.T) {
	checkpointFailure := errors.New("checkpoint read timed out")
	for _, tc := range []struct {
		name      string
		fixture   callTrendFreshnessFixture
		wantErr   error
		wantTotal int64
	}{
		{name: "recent checkpoint keeps nonzero series", fixture: callTrendFreshnessFixture{checkpointRows: [][]driver.Value{{time.Now().Add(-time.Minute)}}}, wantTotal: 17},
		{name: "within tolerance (8m old) checkpoint keeps nonzero series", fixture: callTrendFreshnessFixture{checkpointRows: [][]driver.Value{{time.Now().Add(-8 * time.Minute)}}}, wantTotal: 17},
		{name: "fresh checkpoint with sticky last_error keeps nonzero series", fixture: callTrendFreshnessFixture{checkpointRows: [][]driver.Value{{time.Now().Add(-time.Minute)}}, lastError: "lock wait timeout"}, wantTotal: 17},
		{name: "fifteen minute old checkpoint cannot certify old counts", fixture: callTrendFreshnessFixture{checkpointRows: [][]driver.Value{{time.Now().Add(-15 * time.Minute)}}}, wantErr: errAdminAnalyticsProjectionUnavailable},
		{name: "old checkpoint with sticky last_error cannot certify old counts", fixture: callTrendFreshnessFixture{checkpointRows: [][]driver.Value{{time.Now().Add(-15 * time.Minute)}}, lastError: "lock wait timeout"}, wantErr: errAdminAnalyticsProjectionUnavailable},
		{name: "missing checkpoint cannot certify old counts", wantErr: errAdminAnalyticsProjectionUnavailable},
		{name: "null checkpoint cannot certify old counts", fixture: callTrendFreshnessFixture{checkpointRows: [][]driver.Value{{nil}}}, wantErr: errAdminAnalyticsProjectionUnavailable},
		{name: "null checkpoint with sticky last_error cannot certify old counts", fixture: callTrendFreshnessFixture{checkpointRows: [][]driver.Value{{nil}}, lastError: "connection reset"}, wantErr: errAdminAnalyticsProjectionUnavailable},
		{name: "missing checkpoint table cannot certify old counts", fixture: callTrendFreshnessFixture{checkpointError: errors.New("Error 1146 (42S02): table doesn't exist")}, wantErr: errAdminAnalyticsProjectionUnavailable},
		{name: "checkpoint query failure cannot certify old counts", fixture: callTrendFreshnessFixture{checkpointError: checkpointFailure}, wantErr: checkpointFailure},
		{name: "no source traffic remains a genuine zero without a checkpoint", fixture: callTrendFreshnessFixture{emptyRollup: true, noSource: true}},
		{name: "empty rollup with source traffic remains unavailable", fixture: callTrendFreshnessFixture{emptyRollup: true}, wantErr: errAdminAnalyticsProjectionUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, probe := openCallTrendFreshnessDB(t, tc.fixture)
			got, err := NewRepository(db).AdminCallTrend(withoutAdminRawLogFallback(context.Background()), []string{"10-01", "10-02", "10-03"})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error=%v, want %v; returned total=%d", err, tc.wantErr, got.Total)
			}
			if err == nil {
				if got.Total != tc.wantTotal {
					t.Fatalf("total=%d, want %d", got.Total, tc.wantTotal)
				}
				if tc.wantTotal == 17 && (got.TotalCalls[len(got.TotalCalls)-1] != 17 || got.SuccessCalls[len(got.SuccessCalls)-1] != 15 || got.FailedCalls[len(got.FailedCalls)-1] != 2) {
					t.Fatalf("lost valid series: %+v", got)
				}
			}
			if got := probe.raw.Load(); got != 0 {
				t.Fatalf("dashboard ran %d raw log aggregates", got)
			}
		})
	}
}

func TestAdminWorkbenchDoesNotPublishStaleCallTrend(t *testing.T) {
	for _, singleSection := range []bool{false, true} {
		name := "independent overview metrics remain available"
		sections := []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}
		if singleSection {
			name = "single trend refresh is explicitly retryable"
			sections = []AdminWorkbenchSection{AdminWorkbenchSectionCallTrend}
		}
		t.Run(name, func(t *testing.T) {
			db, probe := openCallTrendFreshnessDB(t, callTrendFreshnessFixture{checkpointRows: [][]driver.Value{{time.Now().Add(-15 * time.Minute)}}})
			service := &Service{repo: NewRepository(db), now: func() time.Time { return time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC) }}
			got, err := service.loadAdminWorkbenchStats(context.Background(), sections, "")
			if singleSection {
				business, ok := apperrors.AsBusinessError(err)
				if !ok || business.StatusCode != consts.StatusServiceUnavailable {
					t.Fatalf("stale single-section refresh returned %+v, err=%v; want retryable HTTP 503", got, err)
				}
			} else {
				if err != nil {
					t.Fatalf("independent metrics must survive: %v", err)
				}
				if got.AnalyticsReady || !slices.Contains(got.DegradedSections, AdminWorkbenchSectionCallTrend) {
					t.Fatalf("stale trend was not degraded: %+v", got)
				}
				if got.CallTrend7d.Total != 0 || len(got.CallTrend7d.TotalCalls) != 0 {
					t.Fatalf("stale series was published: %+v", got.CallTrend7d)
				}
				if got.Overview.TotalUsers != 42 || slices.Contains(got.Overview.UnavailableFields, "totalUsers") {
					t.Fatalf("unrelated user count discarded: %+v", got.Overview)
				}
			}
			if got := probe.raw.Load(); got != 0 {
				t.Fatalf("dashboard ran %d raw log aggregates", got)
			}
		})
	}
}
