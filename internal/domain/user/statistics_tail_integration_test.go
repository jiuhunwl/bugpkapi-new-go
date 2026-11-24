package user

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"bugpk-api/internal/domain/analytics"
	"bugpk-api/internal/pkg/businessclock"
	mysql "github.com/go-sql-driver/mysql"
)

func seedStatisticsTail(t *testing.T, db *sql.DB, total int, splitDays bool) {
	t.Helper()
	for _, query := range []string{
		"insert into bp_user(id,username,password_hash,display_name,status) values(990701,'truth-tail','test-only','Statistics tail',1)",
		"insert into bp_interface_api(id,api_code,name,request_url,request_method,status,remark) values(990701,'truth-tail','Statistics tail','https://fixture.invalid','GET',1,'')",
		"insert ignore into bp_analytics_checkpoint(worker_key) values('call_log_rollup')",
		"update bp_analytics_checkpoint set last_log_id=0,repair_status='NONE',repair_phase='IDLE',repair_projection_mask=0,repair_window_start=null,repair_upper_log_id=0,repair_cursor_id=0 where worker_key='call_log_rollup'",
		"create table truth_digit(n int primary key)",
		"insert into truth_digit values(0),(1),(2),(3),(4),(5),(6),(7),(8),(9)",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	query := `
 insert into bp_interface_call_log(id,interface_id,user_id,request_method,response_status,success,billable,charge_type,charge_amount,point_price,elapsed_ms,client_ip,ip_country,ip_province,ip_city,request_size,response_size,created_at)
 select n+1,990701,990701,'GET',if(mod(n,10)=0,500,200),if(mod(n,10)=0,0,1),if(mod(n,10)=0,0,1),'BALANCE',if(mod(n,10)=0,0,0.0125),0,8,'203.0.113.42','CN','Test province','Test city',10,20,
 date_add(date_sub(current_date(),interval if(? and n>=300,2,0) day),interval 37 minute)
 from (select a.n+10*b.n+100*c.n+1000*d.n+10000*e.n n from truth_digit a cross join truth_digit b cross join truth_digit c cross join truth_digit d cross join truth_digit e) seq where n<? order by n
 `
	if _, err := db.Exec(query, splitDays, total); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into bp_billing_record(call_log_id,user_id,interface_id,api_code,charge_type,charge_amount,status,created_at)
 select id,user_id,interface_id,'truth-tail','BALANCE',charge_amount,'CONFIRMED',created_at from bp_interface_call_log where success=1`); err != nil {
		t.Fatal(err)
	}
}

func truthStatsService(db *sql.DB) *Service {
	return &Service{repo: NewRepository(db), now: func() time.Time { return time.Now().In(businessclock.Location()) }}
}

func TestAdminStatisticsBoundedTailMatchesSourceAndBilling(t *testing.T) {
	for _, tc := range []struct {
		name        string
		total       int
		sparse      bool
		wantAmount  string
		unavailable bool
	}{
		{"small live tail", 600, false, "6.7500", false},
		{"maximum tail remains exact", 20_300, false, "228.3750", false},
		{"overflow is not silently truncated", 20_301, false, "", true},
		{"sparse IDs do not imply missing rows", 600, true, "6.7500", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := statisticsTruthDB(t)
			seedStatisticsTail(t, db, tc.total, false)
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			worker := analytics.NewRepository(db)
			if _, err := worker.ProcessBatch(ctx, 300); err != nil {
				t.Fatal(err)
			}
			if tc.sparse {
				if _, err := db.ExecContext(ctx, "update bp_interface_call_log set id=id+1000000 where id>300 order by id desc"); err != nil {
					t.Fatal(err)
				}
			}
			svc := truthStatsService(db)
			stats, err := svc.loadAdminWorkbenchStats(ctx, []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}, "")
			if err != nil {
				t.Fatal(err)
			}
			if tc.unavailable {
				if stats.AnalyticsReady || !slices.Contains(stats.Overview.UnavailableFields, "todayCalls") || !slices.Contains(stats.DegradedSections, AdminWorkbenchSectionCallTrend) {
					t.Fatalf("oversized tail published as complete: %+v", stats)
				}
				return
			}
			if !stats.AnalyticsReady || stats.Overview.TodayCalls != int64(tc.total) || stats.CallTrend7d.Total != int64(tc.total) {
				t.Fatalf("bounded tail omitted or doubled: %+v", stats)
			}
			if stats.Overview.TodayChargeAmount != tc.wantAmount || stats.Overview.TodayRevenue != tc.wantAmount || stats.Overview.TotalRevenue != tc.wantAmount {
				t.Fatalf("exact money mismatch: today=%s revenue=%s total=%s want=%s", stats.Overview.TodayChargeAmount, stats.Overview.TodayRevenue, stats.Overview.TotalRevenue, tc.wantAmount)
			}
			// Read-only tail recovery must not advance any checkpoint or write a rollup.
			checkpoint, err := worker.Checkpoint(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if checkpoint.LastLogID != 300 {
				t.Fatalf("reader advanced worker cursor: %d", checkpoint.LastLogID)
			}
			if _, err := worker.RunUntilCaughtUp(ctx, 20_000, 100); err != nil {
				t.Fatal(err)
			}
			complete, err := svc.loadAdminWorkbenchStats(ctx, []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}, "")
			if err != nil {
				t.Fatal(err)
			}
			if !complete.AnalyticsReady || complete.Overview.TodayCalls != int64(tc.total) || complete.CallTrend7d.Total != int64(tc.total) || complete.Overview.TodayChargeAmount != tc.wantAmount {
				t.Fatalf("tail was counted again after worker catch-up: %+v", complete)
			}
		})
	}
}

func TestAdminStatisticsMissingHistoricalDayIsNotRealZero(t *testing.T) {
	db := statisticsTruthDB(t)
	seedStatisticsTail(t, db, 600, true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	worker := analytics.NewRepository(db)
	if _, err := worker.RunUntilCaughtUp(ctx, 1000, 100); err != nil {
		t.Fatal(err)
	}
	svc := truthStatsService(db)
	if _, err := db.ExecContext(ctx, "delete from bp_call_metric_minute where bucket_start<current_date(); delete from bp_call_metric_day where bucket_date<current_date()"); err != nil {
		t.Fatal(err)
	}
	partial, err := svc.loadAdminWorkbenchStats(ctx, []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}, "")
	if err != nil {
		t.Fatal(err)
	}
	if partial.AnalyticsReady || !slices.Contains(partial.DegradedSections, AdminWorkbenchSectionCallTrend) || partial.CallTrend7d.Total != 0 {
		t.Fatalf("missing historical 300 requests became a real zero day: %+v", partial)
	}
	if partial.Overview.TodayCalls != 300 || slices.Contains(partial.Overview.UnavailableFields, "todayCalls") {
		t.Fatalf("valid today data was discarded: %+v", partial.Overview)
	}
	if _, err := analytics.NewRepository(db).RunUntilCaughtUp(ctx, 1000, 100); err != nil {
		t.Fatal(err)
	}
	repaired, err := svc.loadAdminWorkbenchStats(ctx, []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !repaired.AnalyticsReady || repaired.CallTrend7d.Total != 600 || repaired.Overview.TodayCalls != 300 {
		t.Fatalf("historical data did not recover exactly: %+v", repaired)
	}
}

type statisticsHookConnector struct {
	inner driver.Connector
	hook  *statisticsHook
}
type statisticsHook struct {
	once   sync.Once
	run    func() error
	err    error
	called bool
}

func (c statisticsHookConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &statisticsHookConn{Conn: conn, hook: c.hook}, nil
}
func (c statisticsHookConnector) Driver() driver.Driver { return c.inner.Driver() }

type statisticsHookConn struct {
	driver.Conn
	hook *statisticsHook
}

func (c *statisticsHookConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}
func (c *statisticsHookConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	rows, err := c.Conn.(driver.QueryerContext).QueryContext(ctx, q, args)
	if err == nil && strings.Contains(q, "c.last_log_id,c.last_completed_at") {
		c.hook.once.Do(func() { c.hook.called = true; c.hook.err = c.hook.run() })
		if c.hook.err != nil {
			rows.Close()
			return nil, c.hook.err
		}
	}
	return rows, err
}

func TestAdminStatisticsSnapshotCannotDoubleCountConcurrentWorkerCommit(t *testing.T) {
	db := statisticsTruthDB(t)
	seedStatisticsTail(t, db, 600, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	worker := analytics.NewRepository(db)
	if _, err := worker.ProcessBatch(ctx, 300); err != nil {
		t.Fatal(err)
	}
	cfg, err := mysql.ParseDSN(os.Getenv("BUGPK_STATS_TRUTH_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "select database()").Scan(&cfg.DBName); err != nil {
		t.Fatal(err)
	}
	cfg.InterpolateParams = true
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	hook := &statisticsHook{run: func() error {
		result, err := worker.ProcessBatch(ctx, 1000)
		if err != nil {
			return err
		}
		if result.SourceRows != 300 {
			return errors.New("concurrency fixture did not commit the pending 300 rows")
		}
		return nil
	}}
	observed := sql.OpenDB(statisticsHookConnector{inner: connector, hook: hook})
	defer observed.Close()
	var got AdminWorkbenchOverview
	if err := NewRepository(observed).adminOverviewCallsSnapshot(ctx, &got); err != nil {
		t.Fatal(err)
	}
	if !hook.called {
		t.Fatal("concurrent worker commit was not exercised")
	}
	if got.TodayCalls != 600 || got.TodaySuccessCalls != 540 || got.TodayFailedCalls != 60 || got.TodayChargeAmount != "6.7500" {
		t.Fatalf("cursor/rollup read mixed snapshots: %+v", got)
	}
	var rawCalls, rolledCalls int64
	if err := db.QueryRowContext(ctx, "select (select count(*) from bp_interface_call_log),(select sum(call_count) from bp_call_metric_minute)").Scan(&rawCalls, &rolledCalls); err != nil {
		t.Fatal(err)
	}
	if rawCalls != 600 || rolledCalls != 600 {
		t.Fatalf("write-side truth changed: raw=%d rolled=%d", rawCalls, rolledCalls)
	}
}

func TestAdminStatisticsLiveTailCannotHideMissingProcessedDay(t *testing.T) {
	db := statisticsTruthDB(t)
	seedStatisticsTail(t, db, 301, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := analytics.NewRepository(db).ProcessBatch(ctx, 300); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "delete from bp_call_metric_minute"); err != nil {
		t.Fatal(err)
	}
	svc := truthStatsService(db)
	partial, err := svc.loadAdminWorkbenchStats(ctx, []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}, "")
	if err != nil {
		t.Fatal(err)
	}
	if partial.AnalyticsReady || !slices.Contains(partial.Overview.UnavailableFields, "todayCalls") || !slices.Contains(partial.DegradedSections, AdminWorkbenchSectionCallTrend) {
		t.Fatalf("same-day tail hid 300 lost processed requests: today=%d sevenDay=%d ready=%v", partial.Overview.TodayCalls, partial.CallTrend7d.Total, partial.AnalyticsReady)
	}
	if _, err := analytics.NewRepository(db).RunUntilCaughtUp(ctx, 1000, 100); err != nil {
		t.Fatal(err)
	}
	// Uniform projection loss is now certified by the independent source audit,
	// not an unbounded raw scan inside the ingest checkpoint transaction.
	if scheduled, err := analytics.NewRepository(db).AuditSourceCoverage(ctx); err != nil || !scheduled {
		t.Fatalf("independent source audit did not identify uniform omission: scheduled=%v err=%v", scheduled, err)
	}
	partlyRebuilt, err := svc.loadAdminWorkbenchStats(ctx, []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}, "")
	if err != nil {
		t.Fatal(err)
	}
	if partlyRebuilt.AnalyticsReady {
		t.Fatalf("one newly rolled row hid the still-missing processed day: %+v", partlyRebuilt)
	}
	// The following caught-up worker probe detects projection disagreement and
	// rebuilds the damaged day before it can become authoritative again.
	if _, err := analytics.NewRepository(db).RunUntilCaughtUp(ctx, 1000, 100); err != nil {
		t.Fatal(err)
	}
	recovered, err := svc.loadAdminWorkbenchStats(ctx, []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !recovered.AnalyticsReady || recovered.Overview.TodayCalls != 301 || recovered.CallTrend7d.Total != 301 {
		t.Fatalf("missing day and tail did not recover exactly: %+v", recovered)
	}
}
func TestAdminStatisticsRepairsThreeDayOldDailyHoleAndKeepsRetainedHistory(t *testing.T) {
	db := statisticsTruthDB(t)
	seedStatisticsTail(t, db, 600, true)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, "update bp_interface_call_log set created_at=date_sub(created_at,interval 1 day) where id>300"); err != nil {
		t.Fatal(err)
	}
	worker := analytics.NewRepository(db)
	if _, err := worker.RunUntilCaughtUp(ctx, 1000, 100); err != nil {
		t.Fatal(err)
	}
	// Historical day outside raw retention must not be cleared by the repair.
	for _, q := range []string{
		"insert into bp_call_metric_day(bucket_date,call_count,success_count,failure_count,billable_count,charge_amount,first_seen,last_seen) values(current_date()-interval 6 day,7,6,1,2,0.2500,current_date()-interval 6 day,current_date()-interval 6 day)",
		"insert into bp_call_api_metric_day(bucket_date,interface_id,user_id,call_count,success_count,failure_count,first_seen,last_seen) values(current_date()-interval 6 day,990701,990701,7,6,1,current_date()-interval 6 day,current_date()-interval 6 day)",
		"delete from bp_call_metric_day where bucket_date=current_date()-interval 3 day",
		"delete from bp_call_api_metric_day where bucket_date=current_date()-interval 3 day",
		"delete from bp_call_user_metric_day where bucket_date=current_date()-interval 3 day",
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	svc := truthStatsService(db)
	partial, err := svc.loadAdminWorkbenchStats(ctx, []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}, "")
	if err != nil {
		t.Fatal(err)
	}
	if partial.AnalyticsReady || partial.Overview.TodayCalls != 300 || slices.Contains(partial.Overview.UnavailableFields, "todayCalls") {
		t.Fatalf("daily hole must only degrade history: %+v", partial)
	}
	if _, err := analytics.NewRepository(db).RunUntilCaughtUp(ctx, 1000, 100); err != nil {
		t.Fatal(err)
	}
	got, err := svc.loadAdminWorkbenchStats(ctx, []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.AnalyticsReady || got.Overview.TodayCalls != 300 || got.CallTrend7d.Total != 607 || got.CallTrend7d.TotalCalls[0] != 7 {
		t.Fatalf("three-day repair lost history or omitted source: %+v", got)
	}
	if _, err := analytics.NewRepository(db).RunUntilCaughtUp(ctx, 1000, 100); err != nil {
		t.Fatal(err)
	}
	again, err := svc.loadAdminWorkbenchStats(ctx, []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !again.AnalyticsReady || again.CallTrend7d.Total != 607 {
		t.Fatalf("repair repeated or double counted: %+v", again)
	}
}

func TestAdminStatisticsRetentionPreservesPendingRows(t *testing.T) {
	db := statisticsTruthDB(t)
	seedStatisticsTail(t, db, 301, false)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := db.ExecContext(ctx, "update bp_interface_call_log set created_at=current_date()-interval 4 day"); err != nil {
		t.Fatal(err)
	}
	if _, err := analytics.NewRepository(db).ProcessBatch(ctx, 300); err != nil {
		t.Fatal(err)
	}
	if _, err := analytics.NewRepository(db).SweepDatasetRetention(ctx); err != nil {
		t.Fatal(err)
	}
	var n int64
	if err := db.QueryRowContext(ctx, "select count(*) from bp_interface_call_log where id=301").Scan(&n); err != nil || n != 1 {
		t.Fatalf("retention erased pending tail: n=%d err=%v", n, err)
	}
	if err := db.QueryRowContext(ctx, "select sum(call_count) from bp_call_metric_minute").Scan(&n); err != nil || n != 300 {
		t.Fatalf("minute prune moved before surviving source floor: n=%d err=%v", n, err)
	}
	if _, err := db.ExecContext(ctx, "update bp_analytics_checkpoint set repair_status='PENDING',repair_phase='IDLE' where worker_key='call_log_rollup'"); err != nil {
		t.Fatal(err)
	}
	if _, err := analytics.NewRepository(db).SweepDatasetRetention(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "select count(*) from bp_interface_call_log").Scan(&n); err != nil || n != 1 {
		t.Fatalf("retention erased active repair source: n=%d err=%v", n, err)
	}
}
func TestDailyHoleMigrationQueuesRepairWithoutResettingActiveCursor(t *testing.T) {
	db := statisticsTruthDB(t)
	seedStatisticsTail(t, db, 600, true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := analytics.NewRepository(db).RunUntilCaughtUp(ctx, 1000, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "delete from bp_call_api_metric_day where bucket_date<current_date()"); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../../migrations/000106_analytics_daily_hole_repair.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	var status, phase string
	var mask, cursor, upper int64
	if err := db.QueryRowContext(ctx, "select repair_status,repair_phase,repair_projection_mask,last_log_id from bp_analytics_checkpoint where worker_key='call_log_rollup'").Scan(&status, &phase, &mask, &cursor); err != nil {
		t.Fatal(err)
	}
	if status != "PENDING" || phase != "IDLE" || mask != 2047 || cursor != 600 {
		t.Fatalf("wrong recovery intent: status=%s phase=%s mask=%d cursor=%d", status, phase, mask, cursor)
	}
	if _, err := analytics.NewRepository(db).ProcessBatch(ctx, 100); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "select repair_cursor_id,repair_upper_log_id from bp_analytics_checkpoint where worker_key='call_log_rollup'").Scan(&cursor, &upper); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	var gotCursor, gotUpper int64
	if err := db.QueryRowContext(ctx, "select repair_cursor_id,repair_upper_log_id from bp_analytics_checkpoint where worker_key='call_log_rollup'").Scan(&gotCursor, &gotUpper); err != nil {
		t.Fatal(err)
	}
	if gotCursor != cursor || gotUpper != upper {
		t.Fatal("rerun reset active repair progress")
	}
	if _, err := analytics.NewRepository(db).RunUntilCaughtUp(ctx, 1000, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "select repair_status from bp_analytics_checkpoint where worker_key='call_log_rollup'").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "COMPLETE" {
		t.Fatalf("healthy migration rerun scheduled destructive replay: %s", status)
	}
}
func TestAdminStatisticsMillionCallReadAndIndependentAuditCost(t *testing.T) {
	db := statisticsTruthDB(t)
	seedStatisticsTail(t, db, 1000, false)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if _, err := analytics.NewRepository(db).RunUntilCaughtUp(ctx, 1000, 100); err != nil {
		t.Fatal(err)
	}
	// Replicate ONLY compact buckets, and create a million-row source control.
	// These tables are in a newly-created disposable MySQL instance.
	if _, err := db.ExecContext(ctx, `insert into bp_interface_call_log(id,interface_id,user_id,request_method,response_status,success,billable,charge_type,charge_amount,point_price,elapsed_ms,client_ip,request_fingerprint,created_at)
 select n+1,990701,990701,'GET',if(mod(n,10)=0,500,200),if(mod(n,10)=0,0,1),if(mod(n,10)=0,0,1),'BALANCE',if(mod(n,10)=0,0,0.0125),0,8,'203.0.113.42','million-read-fixture',date_add(current_date(),interval 37 minute)
 from (select a.n+10*b.n+100*c.n+1000*d.n+10000*e.n+100000*f.n n from truth_digit a cross join truth_digit b cross join truth_digit c cross join truth_digit d cross join truth_digit e cross join truth_digit f) seq where n>=1000 order by n`); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"bp_call_metric_minute", "bp_call_api_metric_minute", "bp_call_user_metric_minute", "bp_call_metric_day", "bp_call_user_metric_day"} {
		if _, err := db.ExecContext(ctx, "update "+table+" set call_count=call_count*1000,success_count=success_count*1000,failure_count=failure_count*1000,billable_count=billable_count*1000,charge_amount=charge_amount*1000"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, "update bp_call_api_metric_day set call_count=call_count*1000,success_count=success_count*1000,failure_count=failure_count*1000"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "update bp_analytics_checkpoint set last_log_id=1000000,last_completed_at=now(6),repair_status='COMPLETE',repair_phase='IDLE' where worker_key='call_log_rollup'"); err != nil {
		t.Fatal(err)
	}
	service := truthStatsService(db)
	durations := make([]time.Duration, 0, 5)
	for i := 0; i < 5; i++ {
		start := time.Now()
		got, err := service.loadAdminWorkbenchStats(ctx, []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}, "")
		elapsed := time.Since(start)
		if err != nil || !got.AnalyticsReady || got.Overview.TodayCalls != 1000000 || got.CallTrend7d.Total != 1000000 {
			t.Fatalf("million-row read not exact: %+v err=%v", got, err)
		}
		durations = append(durations, elapsed)
	}
	start := time.Now()
	if _, err := analytics.NewRepository(db).AuditSourceCoverage(ctx); err != nil {
		t.Fatal(err)
	}
	t.Logf("PERFORMANCE source_rows=1000000 cold_uncached_overview_and_trend=%v independent_primary_chunk_audit=%s", durations, time.Since(start))
}
func TestDailyHoleMigrationReconcilesMinuteOmissionsAgainstRealSource(t *testing.T) {
	db := statisticsTruthDB(t)
	seedStatisticsTail(t, db, 2600, true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := analytics.NewRepository(db).RunUntilCaughtUp(ctx, 1000, 100); err != nil {
		t.Fatal(err)
	}
	// Production evidence showed >1000 source/minute drift on the missing day.
	// Repairing only day tables would then loop forever against stale minutes.
	for _, table := range []string{"bp_call_metric_minute", "bp_call_api_metric_minute", "bp_call_user_metric_minute", "bp_call_region_hour", "bp_call_region_ip_hour"} {
		if _, err := db.ExecContext(ctx, "update "+table+" set call_count=600,success_count=540,failure_count=60 where bucket_start<current_date()"); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"bp_call_metric_day", "bp_call_api_metric_day", "bp_call_user_metric_day"} {
		if _, err := db.ExecContext(ctx, "delete from "+table+" where bucket_date<current_date()"); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile("../../../migrations/000106_analytics_daily_hole_repair.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, string(b)); err != nil {
		t.Fatal(err)
	}
	if _, err := analytics.NewRepository(db).RunUntilCaughtUp(ctx, 1000, 100); err != nil {
		t.Fatal(err)
	}
	got, err := truthStatsService(db).loadAdminWorkbenchStats(ctx, []AdminWorkbenchSection{AdminWorkbenchSectionOverview, AdminWorkbenchSectionCallTrend}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.AnalyticsReady || got.CallTrend7d.Total != 2600 || got.Overview.TodayCalls != 300 {
		t.Fatalf("day-only replay kept old minute/source omissions: %+v", got)
	}
}
