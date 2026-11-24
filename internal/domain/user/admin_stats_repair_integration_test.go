package user

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"bugpk-api/internal/domain/analytics"
	"bugpk-api/internal/pkg/testdb"
)

// Exercise the migration's PENDING/CLEAR state through the actual worker and
// all seven admin-stats sections. The test never runs against the application
// database: its opt-in DSN must name an isolated test/e2e/bench schema.
func TestAdminStatsRecoverRealDataAfterMigrationRepair(t *testing.T) {
	dsn := os.Getenv("BUGPK_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("BUGPK_MYSQL_TEST_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	userID := time.Now().UnixNano()
	name := fmt.Sprintf("admin-repair-%d", userID)
	if _, err := db.ExecContext(ctx, "insert into bp_user(id,username,password_hash,display_name,home_path,status) values(?,?,'test-only',?,'/console/workspace',1)", userID, name, name); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("delete from bp_user where id=?", userID)
	created, err := db.ExecContext(ctx, "insert into bp_interface_api(api_code,name,request_url,request_method,status,remark) values(?,?,'https://test.example/api','GET',1,'')", name, name)
	if err != nil {
		t.Fatal(err)
	}
	apiID, err := created.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	requestRepair := func(c context.Context) error {
		if _, err := db.ExecContext(c, "insert ignore into bp_analytics_checkpoint(worker_key) values('call_log_rollup')"); err != nil {
			return err
		}
		_, err := db.ExecContext(c, `update bp_analytics_checkpoint set repair_status='PENDING',repair_phase='CLEAR',repair_projection_mask=?,repair_window_days=31,repair_requested_at=now(6),repair_started_at=null,repair_completed_at=null,repair_window_start=null,repair_cursor_id=0,repair_upper_log_id=0,repair_clear_projection=0,last_error='' where worker_key='call_log_rollup'`, adminAnalyticsProjectionMask)
		return err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = db.ExecContext(cleanupCtx, "delete from bp_interface_call_log where user_id=?", userID)
		_, _ = db.ExecContext(cleanupCtx, "delete from bp_interface_api where id=?", apiID)
		if err := requestRepair(cleanupCtx); err != nil {
			t.Errorf("schedule cleanup repair: %v", err)
			return
		}
		if _, err := analytics.NewRepository(db).RunUntilCaughtUp(cleanupCtx, 1000, 100); err != nil {
			t.Errorf("cleanup rollups: %v", err)
		}
	}()
	for range 3 {
		if _, err := db.ExecContext(ctx, `insert into bp_interface_call_log(interface_id,user_id,request_method,response_status,success,billable,charge_type,charge_amount,point_price,elapsed_ms,client_ip,ip_country,ip_province,ip_city,latitude,longitude,request_size,response_size,user_visible,created_at) values(?,?,'GET',200,1,0,'FREE',0,0,8,'203.0.113.208','CN','Integration Province','Integration City',31.2,121.5,10,20,1,now())`, apiID, userID); err != nil {
			t.Fatal(err)
		}
	}
	if err := requestRepair(ctx); err != nil {
		t.Fatal(err)
	}
	service := &Service{repo: NewRepository(db), now: time.Now}
	rebuilding, err := service.loadAdminWorkbenchStats(ctx, allAdminWorkbenchSections, "")
	if err != nil {
		t.Fatalf("independent sections should survive a pending repair: %v", err)
	}
	if rebuilding.AnalyticsReady || len(rebuilding.DegradedSections) == 0 {
		t.Fatal("pending migration repair was falsely reported complete")
	}
	drained, err := analytics.NewRepository(db).RunUntilCaughtUp(ctx, 2, 100)
	if err != nil {
		t.Fatal(err)
	}
	if drained.HasMore {
		t.Fatal("bounded migration repair did not finish")
	}
	ready, err := service.loadAdminWorkbenchStats(ctx, allAdminWorkbenchSections, "")
	if err != nil {
		t.Fatal(err)
	}
	if !ready.AnalyticsReady || len(ready.DegradedSections) != 0 {
		t.Fatalf("completed repair still suppresses statistics: %v", ready.DegradedSections)
	}
	if ready.Overview.TodayCalls < 3 || ready.Overview.TotalUsers == 0 || ready.CallTrend7d.Total < 3 || ready.AccessSummary.TotalCalls < 3 {
		t.Fatalf("real calls disappeared after repair: overview=%+v trend=%+v access=%+v", ready.Overview, ready.CallTrend7d, ready.AccessSummary)
	}
	apiFound, userFound := false, false
	for _, item := range ready.HotApis {
		if item.ID == strconv.FormatInt(apiID, 10) {
			apiFound = true
			if item.TodayCalls != 3 {
				t.Fatalf("API calls=%d, want 3", item.TodayCalls)
			}
		}
	}
	for _, item := range ready.ActiveUsers {
		if item.ID == strconv.FormatInt(userID, 10) {
			userFound = true
			if item.TodayCalls != 3 {
				t.Fatalf("user calls=%d, want 3", item.TodayCalls)
			}
		}
	}
	if !apiFound || !userFound {
		t.Fatalf("repaired API/user statistics missing: API=%v user=%v", apiFound, userFound)
	}
	t.Logf("repair completed; today_calls=%d, test_api_calls=3, test_user_calls=3, access_calls=%d", ready.Overview.TodayCalls, ready.AccessSummary.TotalCalls)
}
