package user

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type adminWorkbenchHealthProbe struct {
	mu                sync.Mutex
	queries           []string
	repairPhase       string
	repairStatus      string
	repairMask        int64
	repairRequestedAt driver.Value
	repairLastError   string
	repairStateError  error
	hotAPIValues      [][]driver.Value
	rawAggregateCalls int
}

func (p *adminWorkbenchHealthProbe) record(query string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.queries = append(p.queries, strings.ToLower(query))
}

func (p *adminWorkbenchHealthProbe) rawAggregates() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rawAggregateCalls
}

func (p *adminWorkbenchHealthProbe) matchingQueries(fragment string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	matches := make([]string, 0)
	for _, query := range p.queries {
		if strings.Contains(query, fragment) {
			matches = append(matches, query)
		}
	}
	return matches
}

type adminWorkbenchHealthConnector struct {
	probe *adminWorkbenchHealthProbe
}

func (c adminWorkbenchHealthConnector) Connect(context.Context) (driver.Conn, error) {
	return &adminWorkbenchHealthConn{probe: c.probe}, nil
}

func (c adminWorkbenchHealthConnector) Driver() driver.Driver {
	return adminWorkbenchHealthDriver{probe: c.probe}
}

type adminWorkbenchHealthDriver struct {
	probe *adminWorkbenchHealthProbe
}

func (d adminWorkbenchHealthDriver) Open(string) (driver.Conn, error) {
	return &adminWorkbenchHealthConn{probe: d.probe}, nil
}

type adminWorkbenchHealthConn struct {
	probe *adminWorkbenchHealthProbe
}

func (c *adminWorkbenchHealthConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}

func (c *adminWorkbenchHealthConn) Close() error { return nil }

func (c *adminWorkbenchHealthConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (c *adminWorkbenchHealthConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.probe.record(query)
	lower := strings.ToLower(query)
	switch {
	case strings.Contains(lower, "from bp_analytics_checkpoint"):
		if c.probe.repairStateError != nil {
			return nil, c.probe.repairStateError
		}
		return &adminWorkbenchFailureRows{
			columns: []string{"repair_status", "repair_projection_mask", "repair_phase", "repair_requested_at", "last_error"},
			values: [][]driver.Value{{
				c.probe.repairStatus,
				c.probe.repairMask,
				c.probe.repairPhase,
				c.probe.repairRequestedAt,
				c.probe.repairLastError,
			}},
		}, nil
	case strings.Contains(lower, "from bp_payment_order"):
		return &adminWorkbenchFailureRows{
			columns: []string{"day", "count", "amount", "total_orders", "total_amount"},
		}, nil
	case strings.Contains(lower, "bp_call_api_metric_minute"):
		return &adminWorkbenchFailureRows{
			columns: []string{"id", "code", "name", "status", "calls", "success", "failed", "billable", "amount", "avg_ms"},
			values:  c.probe.hotAPIValues,
		}, nil
	case strings.Contains(lower, "select exists") && strings.Contains(lower, "from bp_interface_call_log"):
		return &adminWorkbenchFailureRows{
			columns: []string{"exists"},
			values:  [][]driver.Value{{int64(1)}},
		}, nil
	case strings.Contains(lower, "from bp_interface_call_log") && strings.Contains(lower, "group by a.id"):
		c.probe.mu.Lock()
		c.probe.rawAggregateCalls++
		c.probe.mu.Unlock()
		return &adminWorkbenchFailureRows{
			columns: []string{"id", "code", "name", "status", "calls", "success", "failed", "billable", "amount", "avg_ms"},
			values: [][]driver.Value{{
				int64(9), "raw-fallback", "Raw fallback", int64(1), int64(17),
				int64(16), int64(1), int64(0), "0.0000", int64(8),
			}},
		}, nil
	default:
		return nil, errors.New("unexpected admin workbench health query: " + query)
	}
}

func openAdminWorkbenchHealthDB(t *testing.T, probe *adminWorkbenchHealthProbe) *sql.DB {
	t.Helper()
	db := sql.OpenDB(adminWorkbenchHealthConnector{probe: probe})
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestAdminWorkbenchSkipsAffectedProjectionWhileRepairIsRunning(t *testing.T) {
	probe := &adminWorkbenchHealthProbe{
		repairStatus:      "RUNNING",
		repairMask:        1 << 1, // bp_call_api_metric_minute
		repairPhase:       "ROLLUP",
		repairRequestedAt: time.Now().Add(-time.Minute),
	}
	service := &Service{
		repo: NewRepository(openAdminWorkbenchHealthDB(t, probe)),
		now:  time.Now,
	}

	result, err := service.loadAdminWorkbenchStats(context.Background(), []AdminWorkbenchSection{
		AdminWorkbenchSectionRevenueTrend,
		AdminWorkbenchSectionHotAPIs,
	}, "")
	if err != nil {
		t.Fatalf("unaffected revenue section must remain available during repair: %v", err)
	}
	if result.AnalyticsReady {
		t.Fatal("running projection repair was reported as analyticsReady=true")
	}
	if want := []AdminWorkbenchSection{AdminWorkbenchSectionHotAPIs}; !reflect.DeepEqual(result.DegradedSections, want) {
		t.Fatalf("degraded sections = %v, want %v", result.DegradedSections, want)
	}
	if got := probe.rawAggregates(); got != 0 {
		t.Fatalf("repairing workbench executed %d raw-log aggregates, want 0", got)
	}
	if got := probe.matchingQueries("from bp_analytics_checkpoint"); len(got) != 1 {
		t.Fatalf("repair state probe count = %d, want 1; queries=%v", len(got), probe.matchingQueries(""))
	}
	if got := probe.matchingQueries("from bp_interface_call_log"); len(got) != 0 {
		t.Fatalf("active repair touched the raw call log; queries=%v", got)
	}
}

func TestAdminWorkbenchDoesNotRawAggregateWhenCompletedProjectionIsEmpty(t *testing.T) {
	probe := &adminWorkbenchHealthProbe{
		repairStatus: "COMPLETE",
		repairMask:   511,
		repairPhase:  "IDLE",
	}
	service := &Service{
		repo: NewRepository(openAdminWorkbenchHealthDB(t, probe)),
		now:  time.Now,
	}

	result, err := service.loadAdminWorkbenchStats(context.Background(), []AdminWorkbenchSection{
		AdminWorkbenchSectionRevenueTrend,
		AdminWorkbenchSectionHotAPIs,
	}, "")
	if err != nil {
		t.Fatalf("unaffected revenue section must survive a missing analytics projection: %v", err)
	}
	if result.AnalyticsReady {
		t.Fatal("empty projection with real source traffic was reported as ready")
	}
	if want := []AdminWorkbenchSection{AdminWorkbenchSectionHotAPIs}; !reflect.DeepEqual(result.DegradedSections, want) {
		t.Fatalf("degraded sections = %v, want %v", result.DegradedSections, want)
	}
	if got := probe.rawAggregates(); got != 0 {
		t.Fatalf("HTTP workbench executed %d raw-log aggregates, want 0", got)
	}
	if got := probe.matchingQueries("from bp_analytics_checkpoint"); len(got) != 1 {
		t.Fatalf("repair state probe count = %d, want 1; queries=%v", len(got), probe.matchingQueries(""))
	}
}

// TestAdminWorkbenchStopsSuppressingSectionsWhenRepairStalls 锁定「投影重建卡死时
// 工作台不再永久归零」的契约。
//
// 迁移 000092 会把 repair_status 置为 PENDING/CLEAR，由串行 analytics worker 推进。
// worker 被关闭或崩溃时 repair_started_at 一直是 NULL，旧实现会永久跳过
// overview/callTrend/hotApis/activeUsers/access 五个分区，前端只能看到 0。
// 超过 adminAnalyticsRepairStallAfter 后必须改为读取现有投影。
func TestAdminWorkbenchStopsSuppressingSectionsWhenRepairStalls(t *testing.T) {
	probe := &adminWorkbenchHealthProbe{
		repairStatus:      "PENDING",
		repairMask:        511,
		repairPhase:       "CLEAR",
		repairRequestedAt: time.Now().Add(-adminAnalyticsRepairStallAfter - time.Minute),
	}
	service := &Service{
		repo: NewRepository(openAdminWorkbenchHealthDB(t, probe)),
		now:  time.Now,
	}

	result, err := service.loadAdminWorkbenchStats(context.Background(), []AdminWorkbenchSection{
		AdminWorkbenchSectionRevenueTrend,
		AdminWorkbenchSectionHotAPIs,
	}, "")
	if err != nil {
		t.Fatalf("stalled repair must not fail the whole workbench: %v", err)
	}
	// 卡死后热门接口分区改为真正查询投影表：投影为空（fake 返回 0 行）时，
	// 该分区仍然报告 degraded，但它是「查询后为空」而不是「被跳过」。
	if got := probe.matchingQueries("bp_call_api_metric_minute"); len(got) == 0 {
		t.Fatalf("stalled repair still skipped the hot API projection; queries=%v", probe.matchingQueries(""))
	}
	if result.AnalyticsReady {
		t.Fatal("stalled repair with an empty projection was reported as ready")
	}
	if want := []AdminWorkbenchSection{AdminWorkbenchSectionHotAPIs}; !reflect.DeepEqual(result.DegradedSections, want) {
		t.Fatalf("degraded sections = %v, want %v", result.DegradedSections, want)
	}
}

// TestAdminAnalyticsRepairStateStallDetection 单独锁定卡死判定的边界条件。
func TestAdminAnalyticsRepairStateStallDetection(t *testing.T) {
	now := time.Date(2026, 9, 5, 21, 0, 0, 0, time.UTC)
	fresh := sql.NullTime{Time: now.Add(-time.Minute), Valid: true}
	stale := sql.NullTime{Time: now.Add(-adminAnalyticsRepairStallAfter - time.Second), Valid: true}

	if state := newAdminAnalyticsRepairState("PENDING", "CLEAR", 511, fresh, "", now); !state.suppresses() || state.stalled {
		t.Fatalf("fresh repair must keep suppressing sections: %+v", state)
	}
	if state := newAdminAnalyticsRepairState("PENDING", "CLEAR", 511, stale, "", now); state.suppresses() || !state.stalled {
		t.Fatalf("stale repair must stop suppressing sections: %+v", state)
	}
	// 没有 requested_at 的行无法判断卡死，保持保守抑制。
	if state := newAdminAnalyticsRepairState("RUNNING", "ROLLUP", 511, sql.NullTime{}, "", now); !state.suppresses() {
		t.Fatalf("repair without a request timestamp must stay suppressed: %+v", state)
	}
	// 已完成的重建既不抑制也不算卡死。
	if state := newAdminAnalyticsRepairState("COMPLETE", "IDLE", 511, stale, "", now); state.suppresses() || state.stalled || state.active {
		t.Fatalf("completed repair must not suppress anything: %+v", state)
	}
	if state := newAdminAnalyticsRepairState("RUNNING", "ROLLUP", 511, stale, "rollup step failed", now); state.lastError != "rollup step failed" {
		t.Fatalf("worker last_error was not carried for diagnostics: %+v", state)
	}
}

func TestStalledRepairNeverCertifiesPartialProjectionAsComplete(t *testing.T) {
	probe := &adminWorkbenchHealthProbe{
		repairStatus: "RUNNING", repairPhase: "ROLLUP", repairMask: 511,
		repairRequestedAt: time.Now().Add(-adminAnalyticsRepairStallAfter - time.Minute),
		hotAPIValues:      [][]driver.Value{{int64(9), "partial", "Partial API", int64(1), int64(17), int64(16), int64(1), int64(0), "0.0000", int64(8)}},
	}
	service := &Service{repo: NewRepository(openAdminWorkbenchHealthDB(t, probe)), now: time.Now}
	result, err := service.loadAdminWorkbenchStats(context.Background(), []AdminWorkbenchSection{AdminWorkbenchSectionRevenueTrend, AdminWorkbenchSectionHotAPIs}, "")
	if err != nil {
		t.Fatal(err)
	}
	if result.AnalyticsReady {
		t.Fatal("partial in-progress projection was incorrectly certified complete after 30 minutes")
	}
	if !reflect.DeepEqual(result.DegradedSections, []AdminWorkbenchSection{AdminWorkbenchSectionHotAPIs}) {
		t.Fatalf("degraded sections=%v, want hotApis", result.DegradedSections)
	}
	if probe.rawAggregates() != 0 {
		t.Fatal("HTTP statistics must not rebuild from raw logs")
	}
}

func TestAdminWorkbenchUnknownRepairStateCannotCertifyPartialProjection(t *testing.T) {
	probe := &adminWorkbenchHealthProbe{
		repairStateError: errors.New("checkpoint temporarily unavailable"),
		hotAPIValues:     [][]driver.Value{{int64(9), "partial", "Partial projection", int64(1), int64(17), int64(16), int64(1), int64(0), "0.0000", int64(8)}},
	}
	service := &Service{repo: NewRepository(openAdminWorkbenchHealthDB(t, probe)), now: time.Now}
	result, err := service.loadAdminWorkbenchStats(context.Background(), []AdminWorkbenchSection{AdminWorkbenchSectionRevenueTrend, AdminWorkbenchSectionHotAPIs}, "")
	if err != nil {
		t.Fatalf("independent revenue must remain available when repair health is unknown: %v", err)
	}
	if result.AnalyticsReady || !reflect.DeepEqual(result.DegradedSections, []AdminWorkbenchSection{AdminWorkbenchSectionHotAPIs}) {
		t.Fatalf("unknown repair state certified partial statistics: ready=%v degraded=%v", result.AnalyticsReady, result.DegradedSections)
	}
	if len(result.HotApis) != 0 || len(probe.matchingQueries("bp_call_api_metric_minute")) != 0 || probe.rawAggregates() != 0 {
		t.Fatal("unknown checkpoint must not read or fall back to unverified projections")
	}
}
