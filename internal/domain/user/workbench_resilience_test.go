package user

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
	"time"
)

func TestIsMissingTableErrorRecognizesMySQL1146(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("Error 1146 (42S02): Table 'bugpk_api.bp_billing_revenue_total' doesn't exist"), true},
		{errors.New("sql: no rows in result set"), false},
		{errors.New("Error 1176 (42000): Key 'idx_x' doesn't exist"), false},
		{nil, false},
	}
	for i, tc := range cases {
		if got := isMissingTableError(tc.err); got != tc.want {
			t.Fatalf("case %d: isMissingTableError(%v) = %v, want %v", i, tc.err, got, tc.want)
		}
	}
}

func TestIsMissingIndexErrorRecognizesMySQL1176(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errors.New("Error 1176 (42000): Key 'idx_bp_payment_order_paid' doesn't exist in table 'bp_payment_order'"), true},
		{errors.New("Error 1146 (42S02): Table 'x' doesn't exist"), false},
		{errors.New("sql: no rows in result set"), false},
		{nil, false},
	}
	for i, tc := range cases {
		if got := isMissingIndexError(tc.err); got != tc.want {
			t.Fatalf("case %d: isMissingIndexError(%v) = %v, want %v", i, tc.err, got, tc.want)
		}
	}
}

func TestAdminRevenueTrendNoHintQueryKeepsAggregationContract(t *testing.T) {
	query := strings.ToLower(adminRevenueTrendNoHintQuery())
	for _, fragment := range []string{
		"from bp_payment_order",
		"status='paid'",
		"paid_at>=date_sub(current_date(), interval 6 day)",
		"group by date_format(paid_at,'%m-%d')",
		"sum(count(*)) over()",
	} {
		if !strings.Contains(query, fragment) {
			t.Fatalf("no-hint revenue query lost %q: %s", fragment, query)
		}
	}
	if strings.Contains(query, "force index") {
		t.Fatalf("no-hint revenue query must not force an index: %s", query)
	}
}

func TestAdminRevenueTrendFallsBackWithoutIndexHint(t *testing.T) {
	repository := &Repository{db: openResilienceTestDB(t)}
	_, err := repository.AdminRevenueTrend(context.Background(), dayLabels(time.Now(), 7))
	if err != nil {
		t.Fatalf("AdminRevenueTrend() failed after hinted-query fallback: %v", err)
	}
}

func TestAdminOverviewFallsBackWhenRevenueSnapshotTableMissing(t *testing.T) {
	repository := &Repository{db: openResilienceTestDB(t)}
	_, err := repository.AdminOverview(context.Background())
	if err != nil {
		t.Fatalf("AdminOverview() must not fail when the revenue snapshot table is missing: %v", err)
	}
}

const resilienceDriverName = "bugpk-workbench-resilience"

func init() {
	sql.Register(resilienceDriverName, resilienceDriver{})
}

type resilienceDriver struct{}

func (resilienceDriver) Open(dsn string) (driver.Conn, error) {
	return resilienceConn{regionIP: dsn == "region-ip"}, nil
}

type resilienceConn struct {
	regionIP bool
}

func (resilienceConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not supported")
}
func (resilienceConn) Close() error { return nil }
func (resilienceConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (c resilienceConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	lower := strings.ToLower(query)
	if c.regionIP {
		switch {
		case strings.Contains(lower, "from bp_call_region_hour"):
			return &resilienceRows{columns: adminAccessResilienceColumns(), values: nil}, nil
		case strings.Contains(lower, "from bp_call_region_ip_hour"):
			return &resilienceRows{
				columns: adminAccessResilienceColumns(),
				values:  [][]string{{"China", "广东省", "深圳市", "ISP-A", "22.5431", "114.0579", "China 广东省 深圳市", "7", "6", "1", "4", "21", "2026-09-03 00:00:00", "2026-09-03 00:10:00", "7", "6", "1", "4", "1", "1"}},
			}, nil
		case strings.Contains(lower, "bp_interface_call_log"):
			return nil, errors.New("region-IP fallback must not query raw call logs")
		default:
			return nil, errors.New("unexpected region-IP resilience query: " + query)
		}
	}
	switch {
	case strings.Contains(lower, "from bp_billing_revenue_projection"):
		// Simulate an upgrade in progress: the snapshot table does not exist.
		return nil, &mysql.MySQLError{Number: 1146, Message: "Table 'bugpk_db.bp_billing_revenue_projection' doesn't exist"}
	case strings.Contains(lower, "from bp_billing_record"):
		return &resilienceRows{columns: []string{"total"}, values: [][]string{{"42.0000"}}}, nil
	case strings.Contains(lower, "force index(idx_bp_payment_order_paid)"):
		// Simulate the upgrade path that has not created the hinted index yet.
		return nil, errors.New("Error 1176 (42000): Key 'idx_bp_payment_order_paid' doesn't exist in table 'bp_payment_order'")
	case strings.Contains(lower, "from bp_payment_order"):
		if strings.Contains(lower, "status in ('created','paying')") {
			// Pending-order count subquery in AdminOverview.
			return &resilienceRows{columns: []string{"orders", "friends"}, values: [][]string{{"0", "0"}}}, nil
		}
		// Revenue-trend fallback query without the FORCE INDEX hint: an empty
		// result set is a valid aggregation outcome.
		return &resilienceRows{columns: []string{"day", "count", "amount", "total_orders", "total_amount"}, values: nil}, nil
	case strings.Contains(lower, "select exists") && strings.Contains(lower, "from bp_interface_call_log"):
		// Recovery probe: source traffic exists even though the rollup projection
		// is empty. The repository must use its bounded source fallback.
		return &resilienceRows{columns: []string{"exists"}, values: [][]string{{"1"}}}, nil
	case strings.Contains(lower, "from bp_call_metric_day"):
		// The day layer exists but is empty right after the migration. An empty
		// projection is a real zero, not a missing table, so the repository must
		// still reach its bounded source fallback instead of reporting an error
		// or a chart of empty bars.
		return &resilienceRows{columns: []string{"day", "total", "success", "failed", "billable", "amount"}, values: nil}, nil
	case strings.Contains(lower, "bp_call_user_metric_day") && strings.Contains(lower, "u.display_name"):
		// Active-user day layer empty after the migration.
		return &resilienceRows{columns: []string{"id", "username", "display_name", "calls", "success", "amount"}, values: nil}, nil
	case strings.Contains(lower, "from bp_call_metric_minute") && strings.Contains(lower, "group by"):
		// Call-trend all-site rollup is empty after migration.
		return &resilienceRows{columns: []string{"day", "total", "success", "failed", "billable", "amount"}, values: nil}, nil
	case strings.Contains(lower, "from bp_interface_call_log") && strings.Contains(lower, "group by date_format"):
		// Bounded seven-day source fallback for call trend.
		return &resilienceRows{columns: []string{"day", "total", "success", "failed", "billable", "amount"}, values: [][]string{{time.Now().Format("01-02"), "12", "10", "2", "8", "3.5000"}}}, nil
	case strings.Contains(lower, "bp_call_api_metric_minute"):
		// Hot-APIs rollup is empty after migration.
		return &resilienceRows{columns: []string{"id", "code", "name", "status", "calls", "success", "failed", "billable", "amount", "avg_ms"}, values: nil}, nil
	case strings.Contains(lower, "from bp_interface_call_log") && strings.Contains(lower, "group by a.id"):
		// Bounded today-window source fallback for hot APIs.
		return &resilienceRows{columns: []string{"id", "code", "name", "status", "calls", "success", "failed", "billable", "amount", "avg_ms"}, values: [][]string{{"7", "recovered_api", "Recovered API", "1", "9", "8", "1", "6", "2.2500", "42"}}}, nil
	case strings.Contains(lower, "from bp_call_metric_minute"):
		// Today's all-site call summary reads the full-site rollup first.
		return &resilienceRows{columns: []string{"calls", "success", "failed", "billable", "amount"}, values: [][]string{{"0", "0", "0", "0", "0.0000"}}}, nil
	case strings.Contains(lower, "from bp_call_region_hour"):
		// Access rollup is empty after migration.
		return &resilienceRows{columns: []string{"country", "province", "city", "isp", "latitude", "longitude", "region", "calls", "success", "failed", "unique", "avg", "first", "last", "total", "total_success", "total_failed", "total_unique", "countries", "locations"}, values: nil}, nil
	case strings.Contains(lower, "from bp_call_region_ip_hour"):
		// The older exact IP-hash projection is also empty, so the repository
		// must proceed to the bounded raw-log fallback.
		return &resilienceRows{columns: []string{"country", "province", "city", "isp", "latitude", "longitude", "region", "calls", "success", "failed", "unique", "avg", "first", "last", "total", "total_success", "total_failed", "total_unique", "countries", "locations"}, values: nil}, nil
	case strings.Contains(lower, "with filtered as") && strings.Contains(lower, "from bp_interface_call_log"):
		// Bounded source fallback for the access atlas. The query must not return
		// a client IP, only grouped location data and aggregate totals.
		return &resilienceRows{columns: []string{"country", "province", "city", "isp", "latitude", "longitude", "region", "calls", "success", "failed", "unique", "avg", "first", "last", "total", "total_success", "total_failed", "total_unique", "countries", "locations"}, values: [][]string{{"China", "广东省", "深圳市", "ISP-A", "22.5431", "114.0579", "China 广东省 深圳市", "5", "4", "1", "3", "20", "2026-09-03 00:00:00", "2026-09-03 00:10:00", "5", "4", "1", "3", "1", "1"}}}, nil
	case strings.Contains(lower, "bp_analytics_checkpoint"):
		// Rollup lag probe: the checkpoint covers the log, so the fallback
		// keeps the (real) rollup zeros instead of re-aggregating the log.
		return &resilienceRows{columns: []string{"exists"}, values: [][]string{{"0"}}}, nil
	case strings.Contains(lower, "bp_call_user_metric_minute") && strings.Contains(lower, "u.display_name"):
		// AdminActiveUsers rollup is empty after migration.
		return &resilienceRows{columns: []string{"id", "username", "display_name", "calls", "success", "failed", "amount"}, values: nil}, nil
	case strings.Contains(lower, "from bp_interface_call_log") && strings.Contains(lower, "group by u.id"):
		// Bounded today-window source fallback for active users.
		return &resilienceRows{columns: []string{"id", "username", "display_name", "calls", "success", "failed", "amount"}, values: [][]string{{"11", "recovered-user", "Recovered User", "9", "8", "1", "2.2500"}}}, nil
	case strings.Contains(lower, "from bp_interface_call_log"):
		// Today-window live aggregation fallback: 5 summary columns.
		return &resilienceRows{columns: []string{"calls", "success", "failed", "billable", "amount"}, values: [][]string{{"12", "10", "2", "8", "3.5000"}}}, nil
	case strings.Contains(lower, "bp_call_user_metric_minute"):
		// Legacy per-user rollup (adminOverviewCallsQuery): 5 columns.
		return &resilienceRows{columns: []string{"calls", "success", "failed", "billable", "amount"}, values: [][]string{{"0", "0", "0", "0", "0.0000"}}}, nil
	case strings.Contains(lower, "from bp_user u"):
		return &resilienceRows{columns: []string{"users", "today_users"}, values: [][]string{{"3", "0"}}}, nil
	case strings.Contains(lower, "from bp_interface_api") || strings.Contains(lower, "from bp_api_key"):
		return &resilienceRows{columns: []string{"total", "enabled"}, values: [][]string{{"0", "0"}}}, nil
	default:
		return nil, errors.New("unexpected resilience test query: " + query)
	}
}

type resilienceRows struct {
	columns []string
	values  [][]string
	offset  int
}

func (r resilienceRows) Columns() []string { return r.columns }
func (r resilienceRows) Close() error      { return nil }

func (r *resilienceRows) Next(dest []driver.Value) error {
	if r.offset >= len(r.values) {
		return io.EOF
	}
	row := r.values[r.offset]
	r.offset++
	for i, raw := range row {
		if i < len(dest) {
			dest[i] = raw
		}
	}
	return nil
}

func openResilienceTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open(resilienceDriverName, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func openRegionIPResilienceTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open(resilienceDriverName, "region-ip")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func adminAccessResilienceColumns() []string {
	return []string{"country", "province", "city", "isp", "latitude", "longitude", "region", "calls", "success", "failed", "unique", "avg", "first", "last", "total", "total_success", "total_failed", "total_unique", "countries", "locations"}
}

// TestAdminSectionsRecoverFromEmptyRollups verifies that an upgraded database
// with real source traffic does not expose permanent zero/empty workbench data
// while the analytics projections are being rebuilt.
func TestAdminSectionsRecoverFromEmptyRollups(t *testing.T) {
	repository := &Repository{db: openResilienceTestDB(t)}
	ctx := context.Background()

	// 1) Call trend: all-site rollup is empty -> bounded source fallback.
	labels := dayLabels(time.Now(), 7)
	trend, err := repository.AdminCallTrend(ctx, labels)
	if err != nil {
		t.Fatalf("AdminCallTrend must not fail on empty rollup: %v", err)
	}
	if trend.Total != 12 {
		t.Fatalf("AdminCallTrend total = %d, want 12 from source fallback", trend.Total)
	}

	// 2) Hot APIs: bp_call_api_metric_minute empty -> bounded source fallback.
	hot, err := repository.AdminHotAPIs(ctx)
	if err != nil {
		t.Fatalf("AdminHotAPIs must not fail on empty rollup: %v", err)
	}
	if len(hot) != 1 || hot[0].TodayCalls != 9 {
		t.Fatalf("AdminHotAPIs = %+v, want recovered source item", hot)
	}

	// 3) Active users: bp_call_user_metric_minute empty -> bounded source fallback.
	active, err := repository.AdminActiveUsers(ctx)
	if err != nil {
		t.Fatalf("AdminActiveUsers must not fail on empty rollup: %v", err)
	}
	if len(active) != 1 || active[0].TodayCalls != 9 {
		t.Fatalf("AdminActiveUsers = %+v, want recovered source item", active)
	}
}

func TestAdminAccessRecoversFromEmptyRollup(t *testing.T) {
	repository := &Repository{db: openResilienceTestDB(t)}
	summary, locations, err := repository.AdminAccess(context.Background(), "")
	if err != nil {
		t.Fatalf("AdminAccess must recover from an empty rollup: %v", err)
	}
	if summary.TotalCalls != 5 || summary.SuccessCalls != 4 || summary.FailedCalls != 1 || summary.UniqueIPs != 3 {
		t.Fatalf("access summary = %+v, want source fallback totals", summary)
	}
	if len(locations) != 1 || locations[0].CallCount != 5 || locations[0].Country != "China" {
		t.Fatalf("access locations = %+v, want recovered source location", locations)
	}
}

func TestAdminAccessPrefersRegionIPRollupBeforeRawLogs(t *testing.T) {
	repository := &Repository{db: openRegionIPResilienceTestDB(t)}
	summary, locations, err := repository.AdminAccess(context.Background(), "")
	if err != nil {
		t.Fatalf("AdminAccess must use the region-IP rollup before raw logs: %v", err)
	}
	if summary.TotalCalls != 7 || summary.SuccessCalls != 6 || summary.FailedCalls != 1 || summary.UniqueIPs != 4 {
		t.Fatalf("region-IP summary = %+v, want rollup totals", summary)
	}
	if len(locations) != 1 || locations[0].CallCount != 7 || locations[0].Country != "China" {
		t.Fatalf("region-IP locations = %+v, want rollup location", locations)
	}
}

func (resilienceConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return testdb.ReadOnlyTransaction(ctx, opts)
}
