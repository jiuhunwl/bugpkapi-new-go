package analytics

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

type repairCheckpointSnapshot struct {
	status          string
	phase           string
	upperLogID      int64
	cursorID        int64
	projectionMask  uint64
	clearProjection int
}

// TestProcessBatchHonorsPersistedRepairRequest covers the state that caused
// migrated workbenches to stay at zero: every projection contains the same
// recent subset, the checkpoint already equals the source high-water mark,
// and a migration has persisted a repair request. The repair must advance in
// bounded committed steps, survive repository recreation, include no rows
// above its frozen upper id, and never double count a completed source range.
func TestProcessBatchHonorsPersistedRepairRequest(t *testing.T) {
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
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(2)
	db.SetMaxIdleConns(2)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if !hasAnalyticsRepairColumns(ctx, db) {
		t.Skip("migration 000092 is not installed in integration database")
	}

	clearAnalyticsIntegrationTables(t, ctx, db)
	resetAnalyticsRepairCheckpoint(t, ctx, db)

	userID := time.Now().UnixNano()
	apiCode := fmt.Sprintf("analytics-repair-task-%d", userID)
	if _, err := db.ExecContext(ctx, `
		insert into bp_user(id,username,password_hash,display_name,home_path,balance,points,status)
		values(?,?,'not-used',?,'/console/workspace',0,0,1)
	`, userID, apiCode, apiCode); err != nil {
		t.Fatal(err)
	}
	result, err := db.ExecContext(ctx, `
		insert into bp_interface_api(api_code,name,request_url,request_method,status,remark)
		values(?,?,'https://127.0.0.1/analytics-repair-task','GET',1,'repair task regression')
	`, apiCode, apiCode)
	if err != nil {
		t.Fatal(err)
	}
	interfaceID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clearAnalyticsRowsForUser(db, userID)
		_, _ = db.Exec("delete from bp_user where id=?", userID)
		_, _ = db.Exec("delete from bp_interface_api where id=?", interfaceID)
		_, _ = db.Exec(`
			update bp_analytics_checkpoint
			set last_log_id=0,last_log_created_at=null,source_rows=0,contribution_rows=0,
			    last_batch_size=0,last_error='',repair_status='NONE',repair_window_days=0,
			    repair_requested_at=null,repair_started_at=null,repair_completed_at=null,
			    repair_upper_log_id=0,repair_cursor_id=0,repair_projection_mask=511,
			    repair_phase='IDLE',repair_window_start=null,repair_clear_projection=9
			where worker_key=?
		`, CheckpointKey)
	})

	createdAt := time.Now().Add(-time.Minute).Truncate(time.Minute)
	insertLog := func(fingerprint string, at time.Time) int64 {
		t.Helper()
		result, err := db.ExecContext(ctx, `
			insert into bp_interface_call_log(
				interface_id,user_id,request_method,response_status,success,billable,charge_type,
				charge_amount,point_price,elapsed_ms,client_ip,ip_country,ip_province,ip_city,
				latitude,longitude,request_fingerprint,request_size,response_size,user_visible,created_at
			) values(?,?,'GET',200,1,1,'FREE',0,0,13,'203.0.113.200',
				'CN','Repair Province','Repair City',31.2,121.5,repeat(?,64),10,20,1,?)
		`, interfaceID, userID, fingerprint, at)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	const batchSize = 2
	logIDs := make([]int64, 0, 6)
	for index, fingerprint := range []string{"a", "b", "c", "d", "e"} {
		logIDs = append(logIDs, insertLog(fingerprint, createdAt.Add(time.Duration(index)*time.Second)))
	}

	// Populate every projection with only the first batch, then emulate a
	// migrated checkpoint that incorrectly claims all five rows were consumed.
	if _, err := db.ExecContext(ctx, `
		update bp_analytics_checkpoint
		set last_log_id=?,repair_status='NONE',repair_window_days=0,repair_phase='IDLE'
		where worker_key=?
	`, logIDs[0]-1, CheckpointKey); err != nil {
		t.Fatal(err)
	}
	if batch, err := NewRepository(db).ProcessBatch(ctx, batchSize); err != nil {
		t.Fatal(err)
	} else if batch.SourceRows != batchSize {
		t.Fatalf("seed batch rows=%d, want %d", batch.SourceRows, batchSize)
	}
	assertProjectionCalls(t, ctx, db, 2)

	if _, err := db.ExecContext(ctx, `
		update bp_analytics_checkpoint
		set last_log_id=?,last_log_created_at=?,source_rows=5,contribution_rows=5,
		    repair_status='PENDING',repair_window_days=31,repair_requested_at=now(6),
		    repair_started_at=null,repair_completed_at=null,last_error='',
		    repair_upper_log_id=0,repair_cursor_id=0,repair_projection_mask=511,
		    repair_phase='CLEAR',repair_window_start=null,repair_clear_projection=0
		where worker_key=?
	`, logIDs[4], createdAt.Add(4*time.Second), CheckpointKey); err != nil {
		t.Fatal(err)
	}

	repository := NewRepository(db)
	firstStep, err := repository.ProcessBatch(ctx, batchSize)
	if err != nil {
		t.Fatal(err)
	}
	if !firstStep.HasMore || firstStep.SourceRows != 0 {
		t.Fatalf("first repair step=%+v, want one metadata/delete step with HasMore", firstStep)
	}
	firstSnapshot := readRepairCheckpoint(t, ctx, db)
	if firstSnapshot.status != repairStatusRunning || firstSnapshot.phase != repairPhaseClear {
		t.Fatalf("first repair state=%+v, want RUNNING/CLEAR", firstSnapshot)
	}
	if firstSnapshot.upperLogID != logIDs[4] {
		t.Fatalf("frozen repair upper id=%d, want %d", firstSnapshot.upperLogID, logIDs[4])
	}
	if calls := projectionCalls(t, ctx, db, "bp_call_metric_minute"); calls == 5 {
		t.Fatal("one ProcessBatch call rebuilt the complete repair window")
	}

	// Append traffic after the repair has frozen its upper id. It must remain
	// outside the repair and be consumed once by the normal worker afterwards.
	logIDs = append(logIDs, insertLog("f", createdAt.Add(10*time.Second)))

	seenPhases := map[string]bool{firstSnapshot.phase: true}
	recreated := false
	completed := false
	for step := 0; step < 100; step++ {
		batch, err := repository.ProcessBatch(ctx, batchSize)
		if err != nil {
			t.Fatal(err)
		}
		if batch.SourceRows > batchSize {
			t.Fatalf("repair step processed %d source rows, batch limit is %d", batch.SourceRows, batchSize)
		}
		snapshot := readRepairCheckpoint(t, ctx, db)
		seenPhases[snapshot.phase] = true
		if !recreated && snapshot.phase == repairPhaseRollup &&
			snapshot.cursorID > logIDs[0]-1 && snapshot.cursorID < snapshot.upperLogID {
			// A process restart creates a new repository with no in-memory probe
			// state. The persisted cursor must be sufficient to continue exactly once.
			repository = NewRepository(db)
			recreated = true
		}
		if snapshot.status == repairStatusComplete {
			completed = true
			if snapshot.phase != repairPhaseIdle {
				t.Fatalf("completed repair phase=%q, want IDLE", snapshot.phase)
			}
			break
		}
	}
	if !completed {
		t.Fatal("resumable repair did not complete within 100 bounded steps")
	}
	if !recreated {
		t.Fatal("test did not recreate the repository in the middle of a persisted source cursor")
	}
	for _, phase := range []string{repairPhaseClear, repairPhaseRollup, repairPhaseFinalize, repairPhaseIdle} {
		if !seenPhases[phase] {
			t.Fatalf("repair never exposed phase %q; seen=%v", phase, seenPhases)
		}
	}

	// The frozen repair contains five rows; the sixth row is still pending.
	assertProjectionCalls(t, ctx, db, 5)
	if riskCalls := projectionCalls(t, ctx, db, "bp_call_risk_fingerprint_5m"); riskCalls != 5 {
		t.Fatalf("risk projection calls=%d, want 5", riskCalls)
	}

	normalBatch, err := repository.ProcessBatch(ctx, batchSize)
	if err != nil {
		t.Fatal(err)
	}
	if normalBatch.SourceRows != 1 || normalBatch.ToLogID != logIDs[5] {
		t.Fatalf("post-repair incremental batch=%+v, want the one row above frozen upper id", normalBatch)
	}
	assertProjectionCalls(t, ctx, db, 6)

	if _, err := NewRepository(db).ProcessBatch(ctx, batchSize); err != nil {
		t.Fatal(err)
	}
	assertProjectionCalls(t, ctx, db, 6)
}

func clearAnalyticsIntegrationTables(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	clearDurableDayIntegrationTables(t, ctx, db)
	for _, table := range []string{
		"bp_call_risk_fingerprint_5m",
		"bp_call_failure_user_hour",
		"bp_call_failure_ip_hour",
		"bp_call_region_ip_hour",
		"bp_call_region_hour",
		"bp_call_user_metric_minute",
		"bp_call_api_metric_day",
		"bp_call_api_metric_minute",
		"bp_call_metric_minute",
		"bp_interface_call_log",
	} {
		if _, err := db.ExecContext(ctx, "delete from "+table); err != nil {
			t.Fatal(err)
		}
	}
}

// Migration 000100 added two durable day projections. Leaving them behind
// between cases makes the next zero-cursor fixture correctly request repair,
// instead of exercising its intended incremental-ingestion or rewind path.
func clearDurableDayIntegrationTables(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"bp_call_metric_day", "bp_call_user_metric_day"} {
		if _, err := db.ExecContext(ctx, "delete from "+table); err != nil && !isMissingOptionalRollupTable(err, table) {
			t.Fatal(err)
		}
	}
}

func clearAnalyticsRowsForUser(db *sql.DB, userID int64) {
	for _, table := range []string{
		"bp_call_risk_fingerprint_5m",
		"bp_call_failure_user_hour",
		"bp_call_failure_ip_hour",
		"bp_call_region_ip_hour",
		"bp_call_region_hour",
		"bp_call_user_metric_minute",
		"bp_call_api_metric_day",
		"bp_call_api_metric_minute",
	} {
		_, _ = db.Exec("delete from "+table+" where user_id=?", userID)
	}
	_, _ = db.Exec("delete from bp_interface_call_log where user_id=?", userID)
}

func resetAnalyticsRepairCheckpoint(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		update bp_analytics_checkpoint
		set last_log_id=0,last_log_created_at=null,source_rows=0,contribution_rows=0,
		    last_batch_size=0,last_error='',repair_status='NONE',repair_window_days=0,
		    repair_requested_at=null,repair_started_at=null,repair_completed_at=null,
		    repair_upper_log_id=0,repair_cursor_id=0,repair_projection_mask=511,
		    repair_phase='IDLE',repair_window_start=null,repair_clear_projection=9
		where worker_key=?
	`, CheckpointKey); err != nil {
		t.Fatal(err)
	}
}

func readRepairCheckpoint(t *testing.T, ctx context.Context, db *sql.DB) repairCheckpointSnapshot {
	t.Helper()
	var snapshot repairCheckpointSnapshot
	if err := db.QueryRowContext(ctx, `
		select repair_status,repair_phase,repair_upper_log_id,repair_cursor_id,
		       repair_projection_mask,repair_clear_projection
		from bp_analytics_checkpoint
		where worker_key=?
	`, CheckpointKey).Scan(
		&snapshot.status,
		&snapshot.phase,
		&snapshot.upperLogID,
		&snapshot.cursorID,
		&snapshot.projectionMask,
		&snapshot.clearProjection,
	); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func projectionCalls(t *testing.T, ctx context.Context, db *sql.DB, table string) int64 {
	t.Helper()
	var calls int64
	if err := db.QueryRowContext(ctx, "select coalesce(sum(call_count),0) from "+table).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	return calls
}

func assertProjectionCalls(t *testing.T, ctx context.Context, db *sql.DB, want int64) {
	t.Helper()
	for _, table := range []string{
		"bp_call_metric_minute",
		"bp_call_api_metric_minute",
		"bp_call_api_metric_day",
		"bp_call_user_metric_minute",
		"bp_call_region_ip_hour",
		"bp_call_region_hour",
	} {
		if calls := projectionCalls(t, ctx, db, table); calls != want {
			t.Fatalf("%s calls=%d, want %d", table, calls, want)
		}
	}
}

func processAllAvailableForIntegrationTest(
	t *testing.T,
	ctx context.Context,
	repository *Repository,
	batchSize int,
) BatchResult {
	t.Helper()
	var result BatchResult
	for step := 0; step < 100; step++ {
		var err error
		result, err = repository.ProcessBatch(ctx, batchSize)
		if err != nil {
			t.Fatal(err)
		}
		if !result.HasMore {
			return result
		}
	}
	t.Fatal("analytics worker did not drain within 100 bounded steps")
	return result
}

func hasAnalyticsRepairColumns(ctx context.Context, db *sql.DB) bool {
	var count int
	err := db.QueryRowContext(ctx, `
		select count(*)
		from information_schema.columns
		where table_schema=database()
		  and table_name='bp_analytics_checkpoint'
		  and column_name in (
		    'repair_status','repair_window_days','repair_requested_at','repair_started_at',
		    'repair_completed_at','repair_upper_log_id','repair_cursor_id',
		    'repair_projection_mask','repair_phase','repair_window_start','repair_clear_projection'
		  )
	`).Scan(&count)
	return err == nil && count == 11
}
