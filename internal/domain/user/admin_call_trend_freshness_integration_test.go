package user

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/domain/analytics"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/testdb"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// Exercise real migrations, source logs, worker checkpoints and the HTTP service
// read policy. The helper owns a new schema on an explicitly disposable server.
func TestAdminCallTrendStaleProjectionRecoveryIntegration(t *testing.T) {
	dsn := os.Getenv("BUGPK_OVERVIEW_ISOLATION_DSN")
	if dsn == "" {
		t.Skip("requires explicit isolated overview MySQL")
	}
	db, _ := testdb.OpenIsolatedMySQL(t, dsn, os.Getenv("BUGPK_OVERVIEW_ISOLATION_DATADIR"), "bugpk_trend_freshness")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	var schema strings.Builder
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		schema.Write(data)
		schema.WriteByte('\n')
	}
	if _, err := db.ExecContext(ctx, schema.String()); err != nil {
		t.Fatal(err)
	}
	// 000092 seeded the original nine-projection repair mask. This fixture
	// tests a fully repaired current schema, including the two 000100 day
	// projections, before deliberately stalling the worker.
	if _, err := db.ExecContext(ctx, "update bp_analytics_checkpoint set repair_projection_mask=? where worker_key='call_log_rollup'", adminAnalyticsProjectionMask); err != nil {
		t.Fatal(err)
	}
	userID := time.Now().UnixNano()
	name := fmt.Sprintf("trend-freshness-%d", userID)
	if _, err := db.ExecContext(ctx, "insert into bp_user(id,username,password_hash,display_name,home_path,status) values(?,?,'test-only',?,'/console/workspace',1)", userID, name, name); err != nil {
		t.Fatal(err)
	}
	api, err := db.ExecContext(ctx, "insert into bp_interface_api(api_code,name,request_url,request_method,status,remark) values(?,?,'https://test.example/api','GET',1,'')", name, name)
	if err != nil {
		t.Fatal(err)
	}
	apiID, err := api.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	insertCall := func(success, status int) {
		_, err := db.ExecContext(ctx, "insert into bp_interface_call_log(interface_id,user_id,request_method,response_status,success,billable,charge_type,charge_amount,point_price,elapsed_ms,client_ip,ip_country,ip_province,ip_city,latitude,longitude,request_size,response_size,user_visible,created_at) values(?,?,'GET',?,?,0,'FREE',0,0,8,'203.0.113.208','CN','Integration Province','Integration City',31.2,121.5,10,20,1,now())", apiID, userID, status, success)
		if err != nil {
			t.Fatal(err)
		}
	}
	worker := analytics.NewRepository(db)
	drain := func() {
		result, err := worker.RunUntilCaughtUp(ctx, 100, 100)
		if err != nil {
			t.Fatal(err)
		}
		if result.HasMore {
			t.Fatal("fixture rollup did not finish")
		}
	}
	svc := &Service{repo: NewRepository(db), now: time.Now, adminWorkbenchCache: newAdminWorkbenchStatsCache()}
	principal := Principal{ID: userID, Username: name, Roles: []string{"admin"}}
	selected := []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}
	insertCall(1, 200)
	drain()
	before, err := svc.AdminWorkbenchStats(ctx, principal, selected...)
	if err != nil || !before.AnalyticsReady || before.CallTrend7d.Total != 1 {
		t.Fatalf("bad healthy control: %+v, err=%v", before, err)
	}
	t.Logf("healthy: source=1 trend=%d analyticsReady=%v", before.CallTrend7d.Total, before.AnalyticsReady)
	warmSingle, err := svc.AdminWorkbenchStats(ctx, principal, AdminWorkbenchSectionCallTrend)
	if err != nil || !warmSingle.AnalyticsReady || warmSingle.CallTrend7d.Total != 1 {
		t.Fatalf("bad healthy single-section cache: %+v, err=%v", warmSingle, err)
	}

	insertCall(0, 500)
	if _, err := db.ExecContext(ctx, "update bp_analytics_checkpoint set last_completed_at=date_sub(now(6), interval 10 minute) where worker_key='call_log_rollup'"); err != nil {
		t.Fatal(err)
	}
	var sourceCalls int64
	if err := db.QueryRowContext(ctx, "select count(*) from bp_interface_call_log where user_id=?", userID).Scan(&sourceCalls); err != nil {
		t.Fatal(err)
	}
	if sourceCalls != 2 {
		t.Fatal("fixture did not write the second real call")
	}
	expireAdminWorkbenchCacheForTest(svc.adminWorkbenchCache)
	stale, err := svc.AdminWorkbenchStats(ctx, principal, selected...)
	if err != nil {
		t.Fatalf("independent metrics were discarded: %v", err)
	}
	if stale.AnalyticsReady || !slices.Contains(stale.DegradedSections, AdminWorkbenchSectionCallTrend) || stale.CallTrend7d.Total != 0 || len(stale.CallTrend7d.TotalCalls) != 0 {
		t.Errorf("stale trend was published or certified: source=%d, trend=%+v, analyticsReady=%v, degradedSections=%v", sourceCalls, stale.CallTrend7d, stale.AnalyticsReady, stale.DegradedSections)
	}
	if stale.Overview.TotalUsers == 0 || slices.Contains(stale.Overview.UnavailableFields, "totalUsers") {
		t.Errorf("unrelated user statistics hidden: %+v", stale.Overview)
	}
	single, err := svc.AdminWorkbenchStats(ctx, principal, AdminWorkbenchSectionCallTrend)
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusServiceUnavailable {
		t.Errorf("stale single-section request not retryable: %+v, err=%v", single, err)
	}
	t.Logf("stalled: source=%d trend=%d analyticsReady=%v degradedSections=%v singleError=%v", sourceCalls, stale.CallTrend7d.Total, stale.AnalyticsReady, stale.DegradedSections, err)

	drain()
	after, err := svc.AdminWorkbenchStats(ctx, principal, selected...)
	if err != nil || !after.AnalyticsReady || after.CallTrend7d.Total != 2 || len(after.DegradedSections) != 0 {
		t.Fatalf("bad recovery: %+v, err=%v", after, err)
	}
	var successes, failures int64
	for _, count := range after.CallTrend7d.SuccessCalls {
		successes += count
	}
	for _, count := range after.CallTrend7d.FailedCalls {
		failures += count
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("wrong recovered series: successes=%d failures=%d", successes, failures)
	}
	t.Logf("recovered: source=%d trend=%d successes=%d failures=%d analyticsReady=%v", sourceCalls, after.CallTrend7d.Total, successes, failures, after.AnalyticsReady)
}
