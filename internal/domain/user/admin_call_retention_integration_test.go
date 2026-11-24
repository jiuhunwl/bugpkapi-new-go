package user

import (
	"context"
	"errors"
	"testing"
	"time"

	"bugpk-api/internal/domain/analytics"
)

func TestAdminCallTrendRecoversDayLayerAfterLegacyRepair(t *testing.T) {
	db := statisticsTruthDB(t)
	seedStatisticsTail(t, db, 1, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// Simulate the older migration's persisted mask: it intentionally omits
	// both new day tables. A completed legacy repair is not full day coverage.
	if _, err := db.ExecContext(ctx, "update bp_analytics_checkpoint set repair_status='PENDING',repair_phase='CLEAR',repair_projection_mask=511,repair_clear_projection=0 where worker_key='call_log_rollup'"); err != nil {
		t.Fatal(err)
	}
	if result, err := analytics.NewRepository(db).RunUntilCaughtUp(ctx, 100, 100); err != nil || result.HasMore {
		t.Fatalf("legacy repair: %+v, %v", result, err)
	}
	if _, err := NewRepository(db).AdminCallTrend(withoutAdminRawLogFallback(ctx), nil); !errors.Is(err, errAdminAnalyticsProjectionUnavailable) {
		t.Fatalf("legacy repair falsely certified an empty day layer: %v", err)
	}
	// The next consistency audit must discover and rebuild the missing day
	// layer without manual SQL changes to its metrics or source cursor.
	if result, err := analytics.NewRepository(db).RunUntilCaughtUp(ctx, 100, 100); err != nil || result.HasMore {
		t.Fatalf("day repair: %+v, %v", result, err)
	}
	trend, err := NewRepository(db).AdminCallTrend(withoutAdminRawLogFallback(ctx), nil)
	if err != nil || trend.Total != 1 || trend.FailedCalls[6] != 1 {
		t.Fatalf("day layer did not recover after legacy repair: %+v, %v", trend, err)
	}
}

func TestAdminCallTrendPreservesHistoryWhenRetentionEmptiesSource(t *testing.T) {
	db := statisticsTruthDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, statement := range []string{
		"insert into bp_user(id,username,password_hash,display_name,status) values(990702,'idle-trend','test-only','Idle fixture',1)",
		"insert into bp_interface_api(id,api_code,name,request_url,request_method,status,remark) values(990702,'idle-trend','Idle fixture','https://fixture.invalid','GET',1,'')",
		"update bp_analytics_checkpoint set last_log_id=0,repair_status='NONE',repair_phase='IDLE',repair_projection_mask=0,repair_window_start=null where worker_key='call_log_rollup'",
		"insert into bp_interface_call_log(interface_id,user_id,request_method,response_status,success,billable,charge_type,charge_amount,created_at) values(990702,990702,'GET',200,1,1,'BALANCE',2.50,date_add(date_sub(current_date(),interval 5 day),interval 1 hour))",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	worker := analytics.NewRepository(db)
	if result, err := worker.RunUntilCaughtUp(ctx, 100, 100); err != nil || result.HasMore {
		t.Fatalf("rollup: %+v, %v", result, err)
	}
	if _, err := worker.SweepDatasetRetention(ctx); err != nil {
		t.Fatal(err)
	}
	for _, afterIdleTick := range []bool{false, true} {
		if afterIdleTick {
			if _, err := worker.ProcessBatch(ctx, 100); err != nil {
				t.Fatal(err)
			}
		}
		trend, err := NewRepository(db).AdminCallTrend(withoutAdminRawLogFallback(ctx), nil)
		if err != nil || trend.Total != 1 || trend.ChargeAmounts[1] != "2.5000" {
			t.Fatalf("retained history lost with empty source (idle tick=%v): %+v, %v", afterIdleTick, trend, err)
		}
	}
	if _, err := db.ExecContext(ctx, "insert into bp_call_metric_day(bucket_date,call_count,success_count,first_seen,last_seen) values(current_date(),1,1,now(),now())"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRepository(db).AdminCallTrend(withoutAdminRawLogFallback(ctx), nil); !errors.Is(err, errAdminAnalyticsProjectionUnavailable) {
		t.Fatalf("current day without minute coverage was certified: %v", err)
	}
	if _, err := db.ExecContext(ctx, "delete from bp_call_metric_day where bucket_date=current_date()"); err != nil {
		t.Fatal(err)
	}
	// A reset/absent checkpoint still cannot vouch for arbitrary projections.
	if _, err := db.ExecContext(ctx, "update bp_analytics_checkpoint set last_completed_at=null where worker_key='call_log_rollup'"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRepository(db).AdminCallTrend(withoutAdminRawLogFallback(ctx), nil); !errors.Is(err, errAdminAnalyticsProjectionUnavailable) {
		t.Fatalf("untracked day projection was certified: %v", err)
	}
}

func TestAdminCallTrendPreservesDailyHistoryAfterRetention(t *testing.T) {
	db := statisticsTruthDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, statement := range []string{
		"insert into bp_user(id,username,password_hash,display_name,status) values(990701,'retention-trend','test-only','Retention fixture',1)",
		"insert into bp_interface_api(id,api_code,name,request_url,request_method,status,remark) values(990701,'retention-trend','Retention fixture','https://fixture.invalid','GET',1,'')",
		"update bp_analytics_checkpoint set last_log_id=0,repair_status='NONE',repair_phase='IDLE',repair_projection_mask=0,repair_window_start=null where worker_key='call_log_rollup'",
		// Include historical paid calls and a current 404: retention must preserve
		// historical amounts and must not reclassify 404s as failures.
		"insert into bp_interface_call_log(interface_id,user_id,request_method,response_status,success,billable,charge_type,charge_amount,created_at) values(990701,990701,'GET',200,1,1,'BALANCE',1.25,date_add(date_sub(current_date(),interval 6 day),interval 1 hour)),(990701,990701,'GET',500,0,0,'FREE',0,date_add(date_sub(current_date(),interval 4 day),interval 1 hour)),(990701,990701,'GET',404,0,0,'FREE',0,now())",
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	worker := analytics.NewRepository(db)
	if result, err := worker.RunUntilCaughtUp(ctx, 100, 100); err != nil || result.HasMore {
		t.Fatalf("rollup: %+v, %v", result, err)
	}
	read := func() AdminCallTrend7d {
		t.Helper()
		trend, err := NewRepository(db).AdminCallTrend(withoutAdminRawLogFallback(ctx), nil)
		if err != nil {
			t.Fatal(err)
		}
		return trend
	}
	before := read()
	if _, err := worker.SweepDatasetRetention(ctx); err != nil {
		t.Fatal(err)
	}
	after := read()
	if after.Total != before.Total || after.Total != 3 || after.TotalCalls[0] != 1 ||
		after.ChargeAmounts[0] != "1.2500" || after.FailedCalls[2] != 1 ||
		after.FailedCalls[6] != 0 || after.SuccessCalls[6] != 0 {
		t.Fatalf("retention changed seven-day history: before=%+v after=%+v", before, after)
	}
	if _, err := db.ExecContext(ctx, "insert into bp_interface_call_log(interface_id,user_id,request_method,response_status,success,charge_type,created_at) values(990701,990701,'GET',200,1,'FREE',now())"); err != nil {
		t.Fatal(err)
	}
	if live := read(); live.Total != 4 || live.TotalCalls[6] != 2 {
		t.Fatalf("bounded live tail was omitted or counted twice: %+v", live)
	}
	// A current day table undergoing repair must never publish a partial trend.
	if _, err := db.ExecContext(ctx, "update bp_analytics_checkpoint set repair_status='RUNNING',repair_phase='ROLLUP',repair_projection_mask=512 where worker_key='call_log_rollup'"); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRepository(db).AdminCallTrend(withoutAdminRawLogFallback(ctx), nil); err == nil {
		t.Fatal("day projection under repair was certified")
	}
}
