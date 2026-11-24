package user

import (
	"bugpk-api/internal/pkg/testdb"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

const adminOverviewPerformanceDriverName = "bugpk-admin-overview-performance"

func init() {
	sql.Register(adminOverviewPerformanceDriverName, adminOverviewPerformanceDriver{})
}

func TestAdminOverviewReadsConstantCardinalityRevenueRollup(t *testing.T) {
	db, err := sql.Open(adminOverviewPerformanceDriverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = NewRepository(db).AdminOverview(context.Background())
	if err != nil {
		t.Fatalf("AdminOverview rejected the indexed historical-revenue query: %v", err)
	}
}

func TestAdminOverviewCallsFallbackDoesNotProbeRawLogsWhenRollupIsPopulated(t *testing.T) {
	db, err := sql.Open(adminOverviewPerformanceDriverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	item := AdminWorkbenchOverview{
		TodayCalls:         12,
		TodaySuccessCalls:  11,
		TodayFailedCalls:   1,
		TodayBillableCalls: 8,
		TodayChargeAmount:  "2.5000",
	}
	// 新语义：rollup 有值且投影新鲜时保持原值、不回退源表。
	// mock driver 对 bp_interface_call_log 返回「无流量」，因此一旦错误回退，
	// TodayCalls 会被改写成 0，本断言即可捕获。
	if err := NewRepository(db).adminOverviewCallsLogFallback(context.Background(), &item); err != nil {
		t.Fatalf("populated rollup fallback returned an error: %v", err)
	}
	if item.TodayCalls != 12 || item.TodayChargeAmount != "2.5000" {
		t.Fatalf("a fresh projection must be authoritative, got: %+v", item)
	}
}

func TestAdminOverviewCallsFallbackReplaysSourceWhenProjectionIsStale(t *testing.T) {
	db, err := sql.Open(adminOverviewPerformanceDriverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// 投影滞后时，即使 TodayCalls 非 0 也必须回退源表，否则控制台会长期显示
	// 一个偏小的数字（这正是「今日调用像被截断的 24 小时数据」的观感来源）。
	item := AdminWorkbenchOverview{TodayCalls: 3, TodaySuccessCalls: 3}
	repo := NewRepository(db)
	repo.forceStaleRollupCheckpoint = true
	if err := repo.adminOverviewCallsLogFallback(context.Background(), &item); err != nil {
		t.Fatalf("stale projection fallback returned an error: %v", err)
	}
	// mock driver 的源表聚合返回全 0，说明确实走了回退路径。
	if item.TodayCalls != 0 {
		t.Fatalf("stale projection must be replaced by the source replay, got: %+v", item)
	}
}

type adminOverviewPerformanceDriver struct{}

func (adminOverviewPerformanceDriver) Open(string) (driver.Conn, error) {
	return adminOverviewPerformanceConn{}, nil
}

type adminOverviewPerformanceConn struct{}

func (adminOverviewPerformanceConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (adminOverviewPerformanceConn) Close() error { return nil }
func (adminOverviewPerformanceConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (adminOverviewPerformanceConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	normalized := strings.ToLower(query)
	switch {
	case strings.Contains(normalized, "from bp_billing_revenue_projection"):
		return &adminOverviewPerformanceRows{columns: []string{"total_revenue"}, values: [][]driver.Value{{"0.0000"}}}, nil
	case strings.Contains(normalized, "from bp_billing_record"):
		return nil, errors.New("historical revenue must not scan billing records")
	case strings.Contains(normalized, "from bp_call_metric_minute"):
		// The all-site rollup backs the overview's today-calls row.
		return &adminOverviewPerformanceRows{columns: []string{"calls", "success", "failed", "billable", "amount"}, values: [][]driver.Value{{int64(0), int64(0), int64(0), int64(0), "0.0000"}}}, nil
	case strings.Contains(normalized, "bp_analytics_checkpoint"):
		// Rollup freshness probe: report a just-completed checkpoint so the
		// populated-projection path is treated as authoritative.
		return &adminOverviewPerformanceRows{
			columns: []string{"last_completed_at"},
			values:  [][]driver.Value{{time.Now()}},
		}, nil
	case strings.Contains(normalized, "select exists") && strings.Contains(normalized, "from bp_interface_call_log"):
		// Empty source window: the bounded recovery probe must avoid the raw
		// aggregate when there is no eligible traffic.
		return &adminOverviewPerformanceRows{columns: []string{"exists"}, values: [][]driver.Value{{int64(0)}}}, nil
	case strings.Contains(normalized, "from bp_interface_call_log"):
		// Today-window live aggregation fallback: 5 summary columns.
		return &adminOverviewPerformanceRows{columns: []string{"calls", "success", "failed", "billable", "amount"}, values: [][]driver.Value{{int64(0), int64(0), int64(0), int64(0), "0.0000"}}}, nil
	case strings.Contains(normalized, "from bp_user u"):
		return &adminOverviewPerformanceRows{columns: []string{"users", "today_users"}, values: [][]driver.Value{{int64(0), int64(0)}}}, nil
	case strings.Contains(normalized, "from bp_interface_api") || strings.Contains(normalized, "from bp_api_key") || strings.Contains(normalized, "from bp_payment_order"):
		return &adminOverviewPerformanceRows{columns: []string{"total", "enabled"}, values: [][]driver.Value{{int64(0), int64(0)}}}, nil
	default:
		return nil, errors.New("unexpected admin overview query")
	}
}

type adminOverviewPerformanceRows struct {
	columns []string
	values  [][]driver.Value
}

func (r adminOverviewPerformanceRows) Columns() []string { return r.columns }
func (r adminOverviewPerformanceRows) Close() error      { return nil }
func (r *adminOverviewPerformanceRows) Next(dest []driver.Value) error {
	if len(r.values) == 0 {
		return io.EOF
	}
	copy(dest, r.values[0])
	r.values = r.values[1:]
	return nil
}

func (adminOverviewPerformanceConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return testdb.ReadOnlyTransaction(ctx, opts)
}
