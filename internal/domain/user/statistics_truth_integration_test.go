package user

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/domain/analytics"
	"bugpk-api/internal/pkg/businessclock"
	"bugpk-api/internal/pkg/testdb"
)

func statisticsTruthDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("BUGPK_STATS_TRUTH_DSN")
	if dsn == "" {
		t.Skip("requires explicit isolated statistics truth MySQL")
	}
	db, _ := testdb.OpenIsolatedMySQL(t, dsn, os.Getenv("BUGPK_STATS_TRUTH_DATADIR"), "bugpk_stats_truth")
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	var schema strings.Builder
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		schema.Write(b)
		schema.WriteByte('\n')
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, schema.String()); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestAdminStatisticsMillionCallsAreNotCertifiedAfterOneBatch(t *testing.T) {
	db := statisticsTruthDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	for _, q := range []string{
		"insert into bp_user(id,username,password_hash,display_name,status) values(990601,'truth-million','test-only','Statistics fixture',1)",
		"insert into bp_interface_api(id,api_code,name,request_url,request_method,status,remark) values(990601,'truth-million','Statistics fixture','https://fixture.invalid','GET',1,'')",
		"insert ignore into bp_analytics_checkpoint(worker_key) values('call_log_rollup')",
		"update bp_analytics_checkpoint set last_log_id=0,repair_status='NONE',repair_phase='IDLE',repair_projection_mask=0,repair_window_start=null,repair_upper_log_id=0,repair_cursor_id=0 where worker_key='call_log_rollup'",
		"create table truth_digit(n int primary key)",
		"insert into truth_digit values(0),(1),(2),(3),(4),(5),(6),(7),(8),(9)",
		"insert into bp_interface_call_log(interface_id,user_id,request_method,response_status,success,billable,charge_type,charge_amount,point_price,elapsed_ms,client_ip,request_fingerprint,ip_country,ip_province,ip_city,request_size,response_size,created_at) select 990601,990601,'GET',if(a.n=0,500,200),if(a.n=0,0,1),0,'FREE',0,0,8,'203.0.113.42','million-truth-fixture','CN','Test province','Test city',10,20,date_add(current_date(),interval 37 minute) from truth_digit a cross join truth_digit b cross join truth_digit c cross join truth_digit d cross join truth_digit e cross join truth_digit f",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	var rawCalls, rawSuccess, rawFailures int64
	if err := db.QueryRowContext(ctx, "select count(*),sum(success=1),sum(success=0) from bp_interface_call_log").Scan(&rawCalls, &rawSuccess, &rawFailures); err != nil {
		t.Fatal(err)
	}
	if rawCalls != 1_000_000 || rawSuccess != 900_000 || rawFailures != 100_000 {
		t.Fatalf("invalid source fixture: %d/%d/%d", rawCalls, rawSuccess, rawFailures)
	}
	worker := analytics.NewRepository(db)
	first, err := worker.ProcessBatch(ctx, 300)
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceRows != 300 || !first.HasMore {
		t.Fatalf("invalid partial control: %+v", first)
	}
	svc := &Service{repo: NewRepository(db), now: func() time.Time { return time.Now().In(businessclock.Location()) }}
	t.Run("recent_batch_is_not_full_coverage", func(t *testing.T) {
		partial, err := svc.loadAdminWorkbenchStats(ctx, []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}, "")
		if err != nil {
			t.Fatal(err)
		}
		if partial.AnalyticsReady || !slices.Contains(partial.Overview.UnavailableFields, "todayCalls") || !slices.Contains(partial.DegradedSections, AdminWorkbenchSectionCallTrend) {
			t.Fatalf("million-row source certified after only 300 rows: overview=%d trend=%d ready=%v unavailable=%v degraded=%v", partial.Overview.TodayCalls, partial.CallTrend7d.Total, partial.AnalyticsReady, partial.Overview.UnavailableFields, partial.DegradedSections)
		}
		encoded, marshalErr := json.Marshal(partial)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		t.Logf("STATS_TRUTH_PENDING=%s", encoded)
	})
	if t.Failed() {
		return
	}
	drained, err := worker.RunUntilCaughtUp(ctx, 20_000, 100)
	if err != nil {
		t.Fatal(err)
	}
	if drained.HasMore {
		t.Fatal("million-row worker did not catch up within bounded batches")
	}
	// Force multiple independent consistency probes after catch-up. A healthy
	// non-hour-aligned source must not enter another CLEAR/ROLLUP cycle.
	for i := 0; i < 3; i++ {
		result, err := analytics.NewRepository(db).ProcessBatch(ctx, 20_000)
		if err != nil {
			t.Fatal(err)
		}
		if result.HasMore {
			t.Fatalf("healthy data scheduled another rebuild on probe %d: %+v", i, result)
		}
	}
	t.Run("complete_million_is_exact", func(t *testing.T) {
		stats, err := svc.loadAdminWorkbenchStats(ctx, []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}, "")
		if err != nil {
			t.Fatal(err)
		}
		if !stats.AnalyticsReady || len(stats.Overview.UnavailableFields) != 0 || len(stats.DegradedSections) != 0 {
			t.Fatalf("complete source remains unavailable: %+v", stats)
		}
		if stats.Overview.TodayCalls != rawCalls || stats.Overview.TodaySuccessCalls != rawSuccess || stats.Overview.TodayFailedCalls != rawFailures || stats.CallTrend7d.Total != rawCalls {
			t.Fatalf("source/rollup/API mismatch: source=%d overview=%+v trend=%+v", rawCalls, stats.Overview, stats.CallTrend7d)
		}
		var success, failed, total int64
		for i := range stats.CallTrend7d.TotalCalls {
			total += stats.CallTrend7d.TotalCalls[i]
			success += stats.CallTrend7d.SuccessCalls[i]
			failed += stats.CallTrend7d.FailedCalls[i]
		}
		if total != rawCalls || success != rawSuccess || failed != rawFailures {
			t.Fatalf("trend sums differ from raw source: total=%d success=%d failures=%d", total, success, failed)
		}
		t.Logf("source=%d successful=%d failed=%d overview=%d seven-day=%d", rawCalls, rawSuccess, rawFailures, stats.Overview.TodayCalls, stats.CallTrend7d.Total)
		encoded, marshalErr := json.Marshal(stats)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		t.Logf("STATS_TRUTH_COMPLETE=%s", encoded)
	})
}
