package riskcontrol

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestRiskDashboardQueryPerformanceIntegration(t *testing.T) {
	dsn := os.Getenv("BUGPK_RISK_BENCH_DSN")
	if dsn == "" {
		t.Skip("BUGPK_RISK_BENCH_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	windowMinutes := 1440
	if raw := os.Getenv("BUGPK_RISK_BENCH_WINDOW_MINUTES"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			t.Fatalf("invalid BUGPK_RISK_BENCH_WINDOW_MINUTES %q", raw)
		}
		windowMinutes = value
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open benchmark database: %v", err)
	}
	defer db.Close()
	if err := db.Ping(); err != nil {
		t.Fatalf("ping benchmark database: %v", err)
	}

	bucketSeconds, labelFormat := riskTrendBucket(windowMinutes)
	queries := []struct {
		name string
		sql  string
		args []any
	}{
		{name: "metrics", sql: riskOverviewMetricsQuery(), args: []any{windowMinutes, windowMinutes, windowMinutes}},
		{name: "single_events", sql: riskRollupEventsQuery("single"), args: []any{windowMinutes, minSingleIPRequests, 24}},
		{name: "proxy_events", sql: riskRollupEventsQuery("proxy"), args: []any{windowMinutes, minProxyRequests, minProxyIPs, 24}},
		{name: "distributed_events", sql: riskRollupEventsQuery("distributed"), args: []any{windowMinutes, minDistributedRequests, minDistributedIPs, 24}},
		{name: "failed_ip_rankings", sql: failedIPRankingsQuery(), args: []any{windowMinutes, windowMinutes, 12}},
		{name: "failed_account_rankings", sql: failedAccountRankingsQuery(), args: []any{windowMinutes, windowMinutes, 12}},
		{
			name: "analytics",
			sql:  riskOverviewAnalyticsQuery(),
			args: []any{windowMinutes, bucketSeconds, bucketSeconds, bucketSeconds, labelFormat},
		},
	}

	for _, query := range queries {
		if filter := os.Getenv("BUGPK_RISK_BENCH_QUERY"); filter != "" && filter != query.name {
			continue
		}
		t.Run(query.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			started := time.Now()
			rows, err := db.QueryContext(ctx, query.sql, query.args...)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			count, err := consumeBenchmarkRows(rows)
			if err != nil {
				t.Fatalf("consume rows: %v", err)
			}
			t.Logf("window_minutes=%d rows=%d elapsed=%s", windowMinutes, count, time.Since(started).Round(time.Microsecond))
			if os.Getenv("BUGPK_RISK_BENCH_EXPLAIN") == "1" {
				logBenchmarkPlan(t, db, query.sql, query.args...)
			}
		})
	}
}

func TestOverviewSnapshotRoundTripPerformanceIntegration(t *testing.T) {
	dsn := os.Getenv("BUGPK_RISK_BENCH_DSN")
	if dsn == "" {
		t.Skip("BUGPK_RISK_BENCH_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := NewRepository(db)
	stored := Overview{
		Events:            []RiskEvent{{ParamSignature: "one"}, {ParamSignature: "two"}, {ParamSignature: "three"}},
		TopFailedAccounts: []FailedAccountRank{{UserID: "1"}, {UserID: "2"}, {UserID: "3"}},
		TopFailedIPs:      []FailedIPRank{{ClientIP: "192.0.2.1"}, {ClientIP: "192.0.2.2"}, {ClientIP: "192.0.2.3"}},
		WindowMinutes:     maxWindowMinutes,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := repo.SaveOverviewSnapshot(ctx, stored); err != nil {
		t.Fatalf("save snapshot: %v", err)
	}
	started := time.Now()
	loaded, found, err := repo.LoadOverviewSnapshot(ctx, RiskQuery{Limit: 2, WindowMinutes: maxWindowMinutes})
	if err != nil || !found {
		t.Fatalf("load snapshot found=%t err=%v", found, err)
	}
	if len(loaded.Events) != 2 || len(loaded.TopFailedAccounts) != 2 || len(loaded.TopFailedIPs) != 2 {
		t.Fatalf("loaded snapshot was not limited: %+v", loaded)
	}
	t.Logf("snapshot_read_elapsed=%s", time.Since(started).Round(time.Microsecond))
	service := NewService(repo, nil)
	started = time.Now()
	served, err := service.loadOverview(ctx, RiskQuery{Limit: 2, WindowMinutes: maxWindowMinutes})
	if err != nil || len(served.Events) != 2 {
		t.Fatalf("serve snapshot events=%d err=%v", len(served.Events), err)
	}
	t.Logf("snapshot_service_elapsed=%s", time.Since(started).Round(time.Microsecond))
}

func TestRiskOverviewEndToEndPerformanceIntegration(t *testing.T) {
	dsn := os.Getenv("BUGPK_RISK_BENCH_DSN")
	if dsn == "" {
		t.Skip("BUGPK_RISK_BENCH_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	started := time.Now()
	overview, err := NewRepository(db).Overview(ctx, RiskQuery{Limit: 12, WindowMinutes: maxWindowMinutes})
	if err != nil {
		t.Fatalf("load overview: %v", err)
	}
	if overview.WindowMinutes != maxWindowMinutes || len(overview.TopFailedIPs) == 0 || len(overview.TopFailedAccounts) == 0 {
		t.Fatalf("unexpected overview result: %+v", overview)
	}
	t.Logf("overview_elapsed=%s events=%d failed_ips=%d failed_accounts=%d", time.Since(started).Round(time.Microsecond), len(overview.Events), len(overview.TopFailedIPs), len(overview.TopFailedAccounts))
}

func logBenchmarkPlan(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, "explain analyze "+statement, args...)
	if err != nil {
		t.Fatalf("explain analyze: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var plan string
		if err := rows.Scan(&plan); err != nil {
			t.Fatalf("scan explain plan: %v", err)
		}
		t.Logf("EXPLAIN ANALYZE\n%s", plan)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read explain plan: %v", err)
	}
}

func consumeBenchmarkRows(rows *sql.Rows) (int, error) {
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return 0, err
	}
	count := 0
	for rows.Next() {
		values := make([]sql.RawBytes, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return 0, err
		}
		count++
	}
	return count, rows.Err()
}
