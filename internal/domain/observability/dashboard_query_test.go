package observability

import (
	"bugpk-api/internal/pkg/testdb"
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	mysql "github.com/go-sql-driver/mysql"
	"io"
	"strings"
	"testing"
)

func TestDashboardOverviewDoesNotScanRawLogsWhenRollupReturnsZero(t *testing.T) {
	db, err := sql.Open(observabilityZeroRollupDriverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	overview, err := (&Repository{db: db}).Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview() must accept a zero rollup without raw-log fallback: %v", err)
	}
	if overview.TodayCalls != 0 || overview.TodaySuccessCalls != 0 || overview.TodayFailedCalls != 0 {
		t.Fatalf("zero rollup metrics changed unexpectedly: %+v", overview)
	}
}

func TestDashboardOverviewMetricsQueryUsesActiveUserMinuteRollups(t *testing.T) {
	assertDashboardRollupQuery(t, dashboardOverviewMetricsQuery(), []string{
		"from bp_call_user_metric_minute m force index (primary)",
		"inner join bp_user u on u.id=m.user_id",
		"m.bucket_start>=current_date()",
		"case when u.status = 0",
		"cast(coalesce(sum(m.charge_amount),0) as char)",
	})
}

func TestDashboardTotalRevenueUsesProjectionAndIndexedPendingWork(t *testing.T) {
	query := strings.ToLower(dashboardTotalRevenueQuery())
	if !strings.Contains(query, "from bp_billing_revenue_projection") {
		t.Fatalf("dashboard total revenue does not use the constant-cardinality rollup: %s", query)
	}
	for _, required := range []string{"force index(idx_bp_billing_revenue_pending)", "revenue_projected=0", "status='confirmed'"} {
		if !strings.Contains(query, required) {
			t.Fatalf("revenue read is not restricted to indexed unprocessed work: %s", query)
		}
	}
}

// The trend must read the day layer, which costs one row per user per day
// instead of 1,440, and must keep a minute fallback so a database that has not
// applied migration 000100 still renders the panel.
func TestDashboardCallTrendQueryUsesBoundedActiveUserRollups(t *testing.T) {
	assertDashboardRollupQuery(t, dashboardCallTrendQuery(), []string{
		"from bp_call_user_metric_day m",
		"inner join bp_user u on u.id=m.user_id",
		"m.bucket_date>=date_sub(current_date(), interval ? day)",
		"group by date_format(m.bucket_date,'%y-%m-%d')",
		"cast(coalesce(sum(m.call_count),0) as unsigned)",
		"cast(coalesce(sum(m.charge_amount),0) as char)",
	})
	// The fallback must produce the same numbers, so it must apply the same
	// read-time active-account filter and the same midnight-aligned window.
	assertDashboardRollupQuery(t, dashboardCallTrendMinuteQuery(), []string{
		"from bp_call_user_metric_minute m force index (primary)",
		"inner join bp_user u on u.id=m.user_id",
		"m.bucket_start>=date_sub(current_date(), interval ? day)",
		"group by date_format(m.bucket_start,'%y-%m-%d')",
		"cast(coalesce(sum(m.charge_amount),0) as char)",
	})
}

func TestDashboardRegionRankingQueryUsesBoundedRegionRollups(t *testing.T) {
	assertDashboardRollupQuery(t, dashboardRegionRankingQuery(), []string{
		"from bp_call_region_hour r force index (idx_bp_call_region_hour_time)",
		"inner join bp_user u on u.id=r.user_id",
		"r.bucket_start >= date_sub(now(), interval ? day)",
		"sum(r.call_count)",
		"case when u.status = 0",
	})
}

// The ranking reuses the existing API/user/day projection from migration 000089
// and keeps a minute fallback for schemas that predate it.
func TestDashboardHotAPIsQueryUsesBoundedPerAPIRollups(t *testing.T) {
	assertDashboardRollupQuery(t, dashboardHotAPIsQuery(), []string{
		"from bp_call_api_metric_day m",
		"inner join bp_user u on u.id=m.user_id",
		"m.bucket_date >= date_sub(current_date(), interval ? day)",
		"group by m.interface_id",
		"cast(coalesce(traffic.call_count,0) as unsigned)",
	})
	// The minute fallback uses the same midnight-aligned window as the day
	// projection. The exclusive upper bound prevents a future-dated bucket from
	// leaking into the current ranking and keeps the fallback contract exact.
	assertDashboardRollupQuery(t, dashboardHotAPIsMinuteQuery(), []string{
		"from bp_call_api_metric_minute m force index (primary)",
		"inner join bp_user u on u.id=m.user_id",
		"m.bucket_start >= date_sub(current_date(), interval ? day)",
		"m.bucket_start < current_date() + interval 1 day",
		"group by m.interface_id",
		"cast(coalesce(traffic.call_count,0) as unsigned)",
	})
}

func assertDashboardRollupQuery(t *testing.T, raw string, required []string) {
	t.Helper()
	query := strings.ToLower(raw)
	for _, fragment := range required {
		if !strings.Contains(query, fragment) {
			t.Fatalf("dashboard rollup query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "bp_interface_call_log") {
		t.Fatalf("dashboard aggregate query still scans raw call logs: %s", query)
	}
	if strings.Contains(query, "bp_call_region_ip_hour") {
		t.Fatalf("dashboard aggregate query still scans the high-cardinality region/IP rollup: %s", query)
	}
}

func TestIsMissingTableErrSeparatesTableAndIndexErrors(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("Error 1146 (42S02): Table 'bugpk_db.bp_billing_revenue_total' doesn't exist"), true},
		{errors.New("Error 1176 (42000): Key 'idx_x' doesn't exist in table 'bp_payment_order'"), false},
		{errors.New("sql: no rows in result set"), false},
		{nil, false},
	}
	for i, tc := range cases {
		if got := isMissingTableErr(tc.err); got != tc.want {
			t.Fatalf("case %d: isMissingTableErr(%v) = %v, want %v", i, tc.err, got, tc.want)
		}
	}
}

func TestDashboardOverviewFallsBackWhenRevenueSnapshotTableMissing(t *testing.T) {
	repository := &Repository{db: newObservabilityFallbackDB(t)}
	overview, err := repository.Overview(context.Background())
	if err != nil {
		t.Fatalf("Overview() must not fail when the revenue snapshot table is missing: %v", err)
	}
	if overview.TotalRevenue != "42.0000" {
		t.Fatalf("TotalRevenue = %q, want fallback 42.0000", overview.TotalRevenue)
	}
}

const observabilityFallbackDriverName = "bugpk-observability-fallback"

func init() {
	sql.Register(observabilityFallbackDriverName, observabilityFallbackDriver{})
}

type observabilityFallbackDriver struct{}

func (observabilityFallbackDriver) Open(string) (driver.Conn, error) {
	return observabilityFallbackConn{}, nil
}

type observabilityFallbackConn struct{}

func (observabilityFallbackConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (observabilityFallbackConn) Close() error { return nil }
func (observabilityFallbackConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (observabilityFallbackConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	lower := strings.ToLower(query)
	switch {
	case strings.Contains(lower, "from bp_billing_revenue_projection"):
		return nil, &mysql.MySQLError{Number: 1146, Message: "Table 'bugpk_db.bp_billing_revenue_projection' doesn't exist"}
	case strings.Contains(lower, "from bp_billing_record"):
		return &scanRows{columns: []string{"total"}, values: [][]driver.Value{{"42.0000"}}}, nil
	case strings.Contains(lower, "from bp_interface_api") || strings.Contains(lower, "from bp_api_key") || strings.Contains(lower, "from bp_user u"):
		return &scanRows{columns: []string{"count", "enabled"}, values: [][]driver.Value{{int64(3), int64(3)}}}, nil
	case strings.Contains(lower, "from bp_call_user_metric_minute"):
		return &scanRows{columns: []string{"calls", "success", "failed", "billable", "amount"}, values: [][]driver.Value{{int64(6), int64(6), int64(0), int64(0), "0.0000"}}}, nil
	default:
		return &scanRows{columns: []string{"value"}, values: nil}, nil
	}
}

type scanRows struct {
	columns []string
	values  [][]driver.Value
	offset  int
}

func (r scanRows) Columns() []string { return r.columns }
func (r scanRows) Close() error      { return nil }
func (r *scanRows) Next(dest []driver.Value) error {
	if r.offset >= len(r.values) {
		return io.EOF
	}
	copy(dest, r.values[r.offset])
	r.offset++
	return nil
}

func newObservabilityFallbackDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open(observabilityFallbackDriverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

const observabilityZeroRollupDriverName = "bugpk-observability-zero-rollup"

func init() {
	sql.Register(observabilityZeroRollupDriverName, observabilityZeroRollupDriver{})
}

type observabilityZeroRollupDriver struct{}

func (observabilityZeroRollupDriver) Open(string) (driver.Conn, error) {
	return observabilityZeroRollupConn{}, nil
}

type observabilityZeroRollupConn struct{}

func (observabilityZeroRollupConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (observabilityZeroRollupConn) Close() error { return nil }
func (observabilityZeroRollupConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (observabilityZeroRollupConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	lower := strings.ToLower(query)
	switch {
	case strings.Contains(lower, "from bp_interface_call_log"):
		return nil, errors.New("raw call-log fallback must not run")
	case strings.Contains(lower, "from bp_interface_api") || strings.Contains(lower, "from bp_api_key") || strings.Contains(lower, "from bp_user u"):
		return &scanRows{columns: []string{"count", "enabled"}, values: [][]driver.Value{{int64(3), int64(3)}}}, nil
	case strings.Contains(lower, "from bp_call_user_metric_minute"):
		return &scanRows{columns: []string{"calls", "success", "failed", "billable", "amount"}, values: [][]driver.Value{{int64(0), int64(0), int64(0), int64(0), "0.0000"}}}, nil
	case strings.Contains(lower, "from bp_billing_revenue_projection"):
		return &scanRows{columns: []string{"total"}, values: [][]driver.Value{{"42.0000"}}}, nil
	default:
		return &scanRows{columns: []string{"value"}, values: nil}, nil
	}
}

func (observabilityFallbackConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return testdb.ReadOnlyTransaction(ctx, opts)
}

func (observabilityZeroRollupConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return testdb.ReadOnlyTransaction(ctx, opts)
}
