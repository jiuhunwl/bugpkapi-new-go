package analytics

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestBatchBounds(t *testing.T) {
	if MaxBatchSize != 20_000 {
		t.Fatalf("unsafe analytics maximum batch size = %d, want 20000", MaxBatchSize)
	}
	if got := normalizeBatchSize(0); got != DefaultBatchSize {
		t.Fatalf("default batch size = %d", got)
	}
	if got := normalizeBatchSize(MaxBatchSize); got != MaxBatchSize {
		t.Fatalf("exact maximum batch size = %d", got)
	}
	if got := normalizeBatchSize(MaxBatchSize + 1); got != MaxBatchSize {
		t.Fatalf("maximum batch size = %d", got)
	}
	if got := normalizeBatchSize(73); got != 73 {
		t.Fatalf("custom batch size = %d", got)
	}
}

func TestRollupRepairDecisionReplaysWhenCheckpointAdvancedButRollupsAreEmpty(t *testing.T) {
	cases := []struct {
		name        string
		state       rollupState
		wantRebuild bool
	}{
		{
			name: "advanced checkpoint with eligible source and no rollups",
			state: rollupState{
				CheckpointID:      900,
				NewestLogID:       900,
				HasEligibleSource: true,
				HasAnyRollup:      false,
			},
			wantRebuild: true,
		},
		{
			name: "empty database",
			state: rollupState{
				CheckpointID:      0,
				NewestLogID:       0,
				HasEligibleSource: false,
				HasAnyRollup:      false,
			},
			wantRebuild: false,
		},
		{
			name: "only admin traffic",
			state: rollupState{
				CheckpointID:      900,
				NewestLogID:       900,
				HasEligibleSource: false,
				HasAnyRollup:      false,
			},
			wantRebuild: false,
		},
		{
			name: "do not replay populated rollups",
			state: rollupState{
				CheckpointID:      900,
				NewestLogID:       900,
				HasEligibleSource: true,
				HasAnyRollup:      true,
			},
			wantRebuild: false,
		},
		{
			name: "checkpoint not yet advanced",
			state: rollupState{
				CheckpointID:      0,
				NewestLogID:       900,
				HasEligibleSource: true,
				HasAnyRollup:      false,
			},
			wantRebuild: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.state.needsReplay(); got != tc.wantRebuild {
				t.Fatalf("needsReplay() = %v, want %v for %+v", got, tc.wantRebuild, tc.state)
			}
		})
	}
}

func TestRollupConsistencyProbeCoversEveryDashboardProjection(t *testing.T) {
	query := strings.ToLower(rollupConsistencyQuery())
	for _, table := range []string{
		"bp_call_metric_minute",
		"bp_call_api_metric_minute",
		"bp_call_api_metric_day",
		"bp_call_user_metric_minute",
		"bp_call_region_ip_hour",
		"bp_call_region_hour",
		"bp_call_failure_ip_hour",
		"bp_call_failure_user_hour",
		"bp_call_risk_fingerprint_5m",
		"bp_call_metric_day",
		"bp_call_user_metric_day",
	} {
		if !strings.Contains(query, "from "+table) {
			t.Fatalf("rollup consistency probe does not inspect %s: %s", table, query)
		}
	}
	if !strings.Contains(query, "exists(select 1") {
		t.Fatalf("rollup consistency probe must use bounded exists checks: %s", query)
	}
}

func TestRequiredRollupConsistencyProbeOmitsOptionalDailyProjection(t *testing.T) {
	query := strings.ToLower(requiredRollupConsistencyQuery())
	// Optional projections are probed separately so an older schema can keep
	// processing the core projections. If any of them leaked into the required
	// probe, a database without the corresponding migration would fail every
	// worker tick instead of degrading that one projection.
	for _, optional := range []string{
		"bp_call_api_metric_day",
		"bp_call_metric_day",
		"bp_call_user_metric_day",
	} {
		if strings.Contains(query, optional) {
			t.Fatalf("required consistency probe must not depend on %s: %s", optional, query)
		}
	}
	for _, table := range []string{
		"bp_call_metric_minute",
		"bp_call_api_metric_minute",
		"bp_call_user_metric_minute",
		"bp_call_region_ip_hour",
		"bp_call_region_hour",
		"bp_call_failure_ip_hour",
		"bp_call_failure_user_hour",
		"bp_call_risk_fingerprint_5m",
	} {
		if !strings.Contains(query, "from "+table) {
			t.Fatalf("required consistency probe lost %s: %s", table, query)
		}
	}
}

func TestConsistencyProbeDueHonorsOneMinuteInterval(t *testing.T) {
	now := time.Date(2026, time.September, 3, 12, 0, 0, 0, time.Local)
	repository := &Repository{lastConsistencyProbe: now.Add(-rollupConsistencyProbeInterval + time.Millisecond)}
	if repository.consistencyProbeDue(now) {
		t.Fatal("consistency probe became due before the one-minute interval elapsed")
	}
	repository.lastConsistencyProbe = now.Add(-rollupConsistencyProbeInterval - time.Millisecond)
	if !repository.consistencyProbeDue(now) {
		t.Fatal("consistency probe did not become due after the one-minute interval elapsed")
	}
}

// Every projection introduced by a late migration must stay optional, and every
// pre-existing projection must stay required. A required statement whose table
// does not exist fails the whole batch, which freezes the checkpoint; an
// optional statement that is actually core would let a real error go unnoticed.
func TestMissingDayProjectionDoesNotBlockCoreRollups(t *testing.T) {
	optional := map[int]string{
		projectionAPIDay:        "bp_call_api_metric_day",
		projectionMetricDay:     "bp_call_metric_day",
		projectionUserMetricDay: "bp_call_user_metric_day",
	}
	for index, table := range optional {
		if !isOptionalRollupStatement(index) {
			t.Fatalf("projection %d (%s) must be optional while older schemas are upgrading", index, table)
		}
		if got, ok := optionalRollupTable(index); !ok || got != table {
			t.Fatalf("optionalRollupTable(%d) = %q, %v; want %q, true", index, got, ok, table)
		}
		if index >= len(rollupStatements()) {
			t.Fatalf("projection %d has no matching rollup statement", index)
		}
		if !strings.Contains(strings.ToLower(rollupStatements()[index]), table) {
			t.Fatalf("rollup statement %d must write %s: %s", index, table, rollupStatements()[index])
		}
	}
	for index := range rollupStatements() {
		if _, ok := optional[index]; ok {
			continue
		}
		if isOptionalRollupStatement(index) {
			t.Fatalf("rollup statement %d must remain a required core projection", index)
		}
	}
	if len(rollupStatements()) != projectionCount {
		t.Fatalf("rollupStatements has %d entries but projectionCount is %d; every projection needs exactly one statement", len(rollupStatements()), projectionCount)
	}
	if got, want := len(repairProjectionDescriptors), projectionCount; got != want {
		t.Fatalf("repairProjectionDescriptors has %d entries, want %d", got, want)
	}
	for index, descriptor := range repairProjectionDescriptors {
		if descriptor.index != index {
			t.Fatalf("descriptor %d carries index %d; masks are derived from positional order", index, descriptor.index)
		}
	}
}

func TestRollupSQLContract(t *testing.T) {
	joined := strings.ToLower(strings.Join(rollupStatements(), "\n"))
	for _, required := range []string{
		"l.id>? and l.id<=?",
		"l.charge_type<>'admin'",
		"exists(select 1 from bp_user source_user",
		"on duplicate key update",
		"bp_call_metric_minute",
		"bp_call_api_metric_minute",
		"bp_call_api_metric_day",
		"bp_call_user_metric_minute",
		"bp_call_region_ip_hour",
		"bp_call_region_hour",
		"bp_call_failure_ip_hour",
		"bp_call_failure_user_hour",
		"bp_call_risk_fingerprint_5m",
		"bp_call_metric_day",
		"bp_call_user_metric_day",
		"unhex(sha2(",
	} {
		if !strings.Contains(joined, required) {
			t.Fatalf("rollup SQL missing %q", required)
		}
	}
	regionInsertColumns := strings.ToLower(strings.Split(regionIPHourUpsert(), ")\n\tselect")[0])
	if strings.Contains(regionInsertColumns, "`client_ip`") || strings.Contains(regionInsertColumns, ",client_ip,") {
		t.Fatal("region rollup destination must retain only a privacy-safe IP hash")
	}
	for _, query := range []string{overallMinuteUpsert, apiMinuteUpsert, userMinuteUpsert} {
		for _, metric := range []string{"request_bytes", "response_bytes", "elapsed_sum"} {
			if !strings.Contains(query, metric) {
				t.Fatalf("minute rollup missing %s", metric)
			}
		}
	}
	compactRegion := strings.ToLower(regionHourUpsert)
	for _, required := range []string{
		"count(distinct nullif(trim(coalesce(l.client_ip,'')),''))",
		"unique_ip_count=unique_ip_count+values(unique_ip_count)",
		"group by from_unixtime(floor(unix_timestamp(l.created_at)/3600)*3600),l.user_id",
	} {
		if !strings.Contains(compactRegion, required) {
			t.Fatalf("compact region rollup missing %q", required)
		}
	}
	if strings.Contains(strings.Split(compactRegion, ")\n\tselect")[0], "client_ip_hash") {
		t.Fatal("compact region rollup key must not retain the IP hash dimension")
	}
}

func TestMigrationContract(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	path := filepath.Join(filepath.Dir(filename), "..", "..", "..", "migrations", "000078_call_log_rollups.up.sql")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sqlText := strings.ToLower(string(contents))
	for _, required := range []string{
		"primary key (`bucket_start`, `interface_id`, `user_id`)",
		"primary key (`bucket_start`, `user_id`)",
		"`client_ip_hash` binary(32)",
		"`last_log_id` bigint unsigned",
		"'call_log_rollup'",
	} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("migration missing %q", required)
		}
	}
}

func TestCompactRegionMigrationContract(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	path := filepath.Join(filepath.Dir(filename), "..", "..", "..", "migrations", "000079_region_hour_rollup.up.sql")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sqlText := strings.ToLower(string(contents))
	for _, required := range []string{
		"create table if not exists `bp_call_region_hour`",
		"`unique_ip_count` bigint unsigned",
		"primary key (`bucket_start`, `user_id`, `country`, `province`, `city`, `isp`, `latitude`, `longitude`)",
		"key `idx_bp_call_region_hour_time` (`bucket_start`, `country`, `province`, `city`)",
		"not cross-window exact",
		"from `bp_call_region_ip_hour` force index (`idx_bp_call_region_ip_time`)",
		"interval 31 day",
		"when `client_ip_hash` <> unhex(sha2('bugpk-region-ip-v1:',256)) then `client_ip_hash`",
		"on duplicate key update `bucket_start`=values(`bucket_start`)",
	} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("compact region migration missing %q", required)
		}
	}
	definition := strings.Split(sqlText, "comment='compact hourly regional traffic")[0]
	if strings.Contains(definition, "client_ip_hash") {
		t.Fatal("compact region table unexpectedly retains an IP hash")
	}
}

func TestProcessBatchIsIdempotentAndAdvancesAcrossIneligibleRows(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
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
	} {
		if _, err := db.ExecContext(ctx, "delete from "+table); err != nil {
			if table == "bp_call_api_metric_day" && isMissingOptionalRollupTable(err, table) {
				continue
			}
			t.Skipf("analytics projection cleanup is unavailable in integration database: %v", err)
		}
	}
	var baselineLogID int64
	if err := db.QueryRowContext(ctx, "select coalesce(max(id),0) from bp_interface_call_log").Scan(&baselineLogID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		update bp_analytics_checkpoint
		set last_log_id=?,source_rows=0,contribution_rows=0
		where worker_key='call_log_rollup'
	`, baselineLogID); err != nil {
		t.Fatal(err)
	}

	var userID, interfaceID int64
	if err := db.QueryRowContext(ctx, "select id from bp_user order by id limit 1").Scan(&userID); err != nil {
		t.Skipf("integration database has no user: %v", err)
	}
	interfaceCreated := false
	if err := db.QueryRowContext(ctx, "select id from bp_interface_api order by id limit 1").Scan(&interfaceID); err != nil {
		if err != sql.ErrNoRows {
			t.Fatal(err)
		}
		created, createErr := db.ExecContext(ctx, `
			insert into bp_interface_api(api_code,name,request_url,request_method,status,remark)
			values('analytics_rollup_test','Analytics Rollup Test','https://127.0.0.1/never','GET',1,'integration fixture')
		`)
		if createErr != nil {
			t.Fatal(createErr)
		}
		interfaceID, createErr = created.LastInsertId()
		if createErr != nil {
			t.Fatal(createErr)
		}
		interfaceCreated = true
	}
	if interfaceCreated {
		t.Cleanup(func() { _, _ = db.Exec("delete from bp_interface_api where id=?", interfaceID) })
	}
	result, err := db.ExecContext(ctx, `
		insert into bp_interface_call_log(
			interface_id,user_id,request_method,response_status,success,billable,charge_type,
			charge_amount,point_price,elapsed_ms,client_ip,ip_country,ip_province,ip_city,
			latitude,longitude,request_fingerprint,request_size,response_size,user_visible,created_at
		) values
		(?,?,'GET',200,1,1,'FREE',0,0,12,'203.0.113.7','China','Test','City',31.2,121.5,repeat('a',64),10,20,1,now()),
		(?,?,'GET',200,1,0,'ADMIN',0,0,2,'127.0.0.1','','','',null,null,'',0,0,0,now())
	`, interfaceID, userID, interfaceID, userID)
	if err != nil {
		t.Fatal(err)
	}
	lastID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	// Multi-row LastInsertId is the first ID. Cleanup both inserted records.
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_interface_call_log where id>=? and id<=?", lastID, lastID+1)
	})

	repo := NewRepository(db)
	first, err := repo.ProcessBatch(ctx, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if first.SourceRows != 2 || first.ToLogID < lastID+1 || first.ContributionRows != 1 {
		t.Fatalf("unexpected first batch: %+v", first)
	}
	var calls int64
	if err := db.QueryRowContext(ctx, `select coalesce(sum(call_count),0) from bp_call_user_metric_minute where user_id=?`, userID).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	var compactCalls, compactUniqueIPs int64
	if err := db.QueryRowContext(ctx, `
		select coalesce(sum(call_count),0),coalesce(sum(unique_ip_count),0)
		from bp_call_region_hour where user_id=?
	`, userID).Scan(&compactCalls, &compactUniqueIPs); err != nil {
		t.Fatal(err)
	}
	if compactCalls != 1 || compactUniqueIPs != 1 {
		t.Fatalf("compact region aggregate = calls:%d unique-estimate:%d, want 1/1", compactCalls, compactUniqueIPs)
	}
	second, err := repo.ProcessBatch(ctx, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	var callsAfter int64
	if err := db.QueryRowContext(ctx, `select coalesce(sum(call_count),0) from bp_call_user_metric_minute where user_id=?`, userID).Scan(&callsAfter); err != nil {
		t.Fatal(err)
	}
	if callsAfter != calls || second.SourceRows != 0 {
		t.Fatalf("retry changed aggregates: before=%d after=%d batch=%+v", calls, callsAfter, second)
	}
	var compactCallsAfter, compactUniqueIPsAfter int64
	if err := db.QueryRowContext(ctx, `
		select coalesce(sum(call_count),0),coalesce(sum(unique_ip_count),0)
		from bp_call_region_hour where user_id=?
	`, userID).Scan(&compactCallsAfter, &compactUniqueIPsAfter); err != nil {
		t.Fatal(err)
	}
	if compactCallsAfter != compactCalls || compactUniqueIPsAfter != compactUniqueIPs {
		t.Fatalf(
			"retry changed compact region aggregate: before=%d/%d after=%d/%d",
			compactCalls,
			compactUniqueIPs,
			compactCallsAfter,
			compactUniqueIPsAfter,
		)
	}
}

// TestProcessBatchRepairsPartiallyPopulatedProjections reproduces the
// migration failure mode seen in production: the checkpoint already points
// at the newest source row, one projection contains a leftover row, and the
// other dashboard projections are empty.  The old global HasAnyRollup guard
// treats this state as healthy and never replays the source log, so the
// workbench keeps returning zeroes forever.
func TestProcessBatchRepairsPartiallyPopulatedProjections(t *testing.T) {
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
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	clearDurableDayIntegrationTables(t, ctx, db)

	// This test asserts the stale-watermark path in isolation. Previous
	// integration cases may have left a bounded projection row behind, which
	// would correctly trigger the zero-checkpoint repair path instead of the
	// behavior being tested here.
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
	} {
		if _, err := db.ExecContext(ctx, "delete from "+table); err != nil {
			if table == "bp_call_api_metric_day" && isMissingOptionalRollupTable(err, table) {
				continue
			}
			t.Skipf("analytics projection cleanup is unavailable in integration database: %v", err)
		}
	}
	if hasAnalyticsRepairColumns(ctx, db) {
		resetAnalyticsRepairCheckpoint(t, ctx, db)
	} else if _, err := db.ExecContext(ctx, `
		update bp_analytics_checkpoint
		set repair_status='NONE',repair_window_days=0,repair_requested_at=null,
		    repair_started_at=null,repair_completed_at=null
		where worker_key=?
	`, CheckpointKey); err != nil && !isMissingRepairColumnError(err) {
		t.Fatal(err)
	}

	// This regression requires the compact region projection introduced by
	// migration 000079.  Keep older upgrade fixtures compatible by skipping
	// rather than turning the package's normal unit run into an environment
	// failure.
	var tableCount int
	if err := db.QueryRowContext(ctx, `
		select count(*)
		from information_schema.tables
		where table_schema=database()
		  and table_name in (
			'bp_call_metric_minute', 'bp_call_api_metric_minute',
			'bp_call_user_metric_minute', 'bp_call_region_hour',
			'bp_analytics_checkpoint'
		  )
	`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount != 5 {
		t.Skip("analytics projection migrations are not installed in integration database")
	}

	// Use a clean projection state so the single sentinel below is the only
	// destination row.  The database name is guarded as an isolated test DB.
	for _, table := range []string{
		"bp_call_risk_fingerprint_5m",
		"bp_call_failure_user_hour",
		"bp_call_failure_ip_hour",
		"bp_call_region_ip_hour",
		"bp_call_region_hour",
		"bp_call_user_metric_minute",
		"bp_call_api_metric_minute",
		"bp_call_metric_minute",
	} {
		if _, err := db.ExecContext(ctx, "delete from "+table); err != nil {
			t.Fatal(err)
		}
	}
	// The daily projection is optional during rolling upgrades.
	if _, err := db.ExecContext(ctx, "delete from bp_call_api_metric_day"); err != nil && !isMissingOptionalRollupTable(err, "bp_call_api_metric_day") {
		t.Fatal(err)
	}

	userID := time.Now().UnixNano()
	apiCode := fmt.Sprintf("analytics-partial-%d", userID)
	var interfaceID int64
	if _, err := db.ExecContext(ctx, `
		insert into bp_user(id,username,password_hash,display_name,home_path,balance,points,status)
		values(?,?,'not-used',?,'/console/workspace',0,0,1)
	`, userID, apiCode, apiCode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_interface_call_log where user_id=?", userID)
		_, _ = db.Exec("delete from bp_user where id=?", userID)
		_, _ = db.Exec("delete from bp_interface_api where id=?", interfaceID)
	})
	result, err := db.ExecContext(ctx, `
		insert into bp_interface_api(api_code,name,request_url,request_method,status,remark)
		values(?,?,'https://127.0.0.1/analytics-partial','GET',1,'partial projection regression')
	`, apiCode, apiCode)
	if err != nil {
		t.Fatal(err)
	}
	interfaceID, err = result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	result, err = db.ExecContext(ctx, `
		insert into bp_interface_call_log(
			interface_id,user_id,request_method,response_status,success,billable,charge_type,
			charge_amount,point_price,elapsed_ms,client_ip,ip_country,ip_province,ip_city,
			latitude,longitude,request_fingerprint,request_size,response_size,user_visible,created_at
		) values(?,?, 'GET',200,1,1,'FREE',0,0,17,'203.0.113.88',
			'CN','Regression Province','Regression City',31.2,121.5,repeat('b',64),10,20,1,now())
	`, interfaceID, userID)
	if err != nil {
		t.Fatal(err)
	}
	logID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	// Leave one unrelated destination row behind.  This is precisely what
	// makes the old all-or-nothing HasAnyRollup decision return false for the
	// repair even though the current source row has never reached most tables.
	if _, err := db.ExecContext(ctx, `
		insert into bp_call_region_hour(
			bucket_start,user_id,country,province,city,isp,latitude,longitude,
			call_count,success_count,failure_count,unique_ip_count,
			request_bytes,response_bytes,elapsed_sum,first_seen,last_seen
		) values(date_sub(now(),interval 2 day),?,?,?,?,?,0,0,1,1,0,1,0,0,1,date_sub(now(),interval 2 day),date_sub(now(),interval 2 day))
	`, userID, "CN", "Old Province", "Old City", "Regression ISP"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		update bp_analytics_checkpoint
		set last_log_id=?,last_log_created_at=now(),source_rows=0,contribution_rows=0,last_batch_size=0,last_error=''
		where worker_key=?
	`, logID, CheckpointKey); err != nil {
		t.Fatal(err)
	}

	processAllAvailableForIntegrationTest(t, ctx, NewRepository(db), 10_000)

	var calls int64
	if err := db.QueryRowContext(ctx, `
		select coalesce(sum(call_count),0)
		from bp_call_metric_minute
		where bucket_start>=current_date()
	`).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("overall rollup calls=%d, want repaired current-day call", calls)
	}
	var userCalls int64
	if err := db.QueryRowContext(ctx, `
		select coalesce(sum(call_count),0)
		from bp_call_user_metric_minute
		where user_id=? and bucket_start>=current_date()
	`, userID).Scan(&userCalls); err != nil {
		t.Fatal(err)
	}
	if userCalls != 1 {
		t.Fatalf("user rollup calls=%d, want 1", userCalls)
	}
	var apiCalls int64
	if err := db.QueryRowContext(ctx, `
		select coalesce(sum(call_count),0)
		from bp_call_api_metric_minute
		where interface_id=? and user_id=? and bucket_start>=current_date()
	`, interfaceID, userID).Scan(&apiCalls); err != nil {
		t.Fatal(err)
	}
	if apiCalls != 1 {
		t.Fatalf("API rollup calls=%d, want 1", apiCalls)
	}

	// Recreate the repository to model an application restart. The consistency
	// probe timestamp is process-local, so a fresh repository must still see a
	// healthy window and must not add the repaired source row a second time.
	var beforeRetry int64
	if err := db.QueryRowContext(ctx, `
		select coalesce(sum(call_count),0)
		from bp_call_metric_minute
		where bucket_start>=current_date()
	`).Scan(&beforeRetry); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRepository(db).ProcessBatch(ctx, 10_000); err != nil {
		t.Fatal(err)
	}
	var afterRetry int64
	if err := db.QueryRowContext(ctx, `
		select coalesce(sum(call_count),0)
		from bp_call_metric_minute
		where bucket_start>=current_date()
	`).Scan(&afterRetry); err != nil {
		t.Fatal(err)
	}
	if afterRetry != beforeRetry {
		t.Fatalf("cross-repository retry changed repaired calls: before=%d after=%d", beforeRetry, afterRetry)
	}
}

// TestProcessBatchDoesNotDoubleCountPrepopulatedRollupWithZeroCheckpoint
// covers a restore/import state in which the checkpoint row is reset to zero
// while a migration has already populated one or more rollup projections.
// Replaying from zero would duplicate the pre-populated row; recovery must
// rebuild the bounded window and move the checkpoint to the source high-water
// mark instead.
func TestProcessBatchDoesNotDoubleCountPrepopulatedRollupWithZeroCheckpoint(t *testing.T) {
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
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	clearDurableDayIntegrationTables(t, ctx, db)

	// Keep this fixture isolated from other integration tests. The test DSN is
	// explicitly required to be disposable by ValidateIsolatedMySQLDSN.
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
	} {
		if _, err := db.ExecContext(ctx, "delete from "+table); err != nil {
			if table == "bp_call_api_metric_day" && isMissingOptionalRollupTable(err, table) {
				continue
			}
			t.Fatal(err)
		}
	}
	if hasAnalyticsRepairColumns(ctx, db) {
		resetAnalyticsRepairCheckpoint(t, ctx, db)
	} else if _, err := db.ExecContext(ctx, `
		update bp_analytics_checkpoint
		set last_log_id=0,last_log_created_at=null,source_rows=0,contribution_rows=0,
		    last_batch_size=0,last_error=''
		where worker_key=?
	`, CheckpointKey); err != nil {
		t.Fatal(err)
	}

	userID := time.Now().UnixNano()
	apiCode := fmt.Sprintf("analytics-zero-checkpoint-%d", userID)
	var interfaceID int64
	if _, err := db.ExecContext(ctx, `
		insert into bp_user(id,username,password_hash,display_name,home_path,balance,points,status)
		values(?,?,'not-used',?,'/console/workspace',0,0,1)
	`, userID, apiCode, apiCode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_interface_call_log where user_id=?", userID)
		_, _ = db.Exec("delete from bp_call_risk_fingerprint_5m where user_id=?", userID)
		_, _ = db.Exec("delete from bp_call_failure_user_hour where user_id=?", userID)
		_, _ = db.Exec("delete from bp_call_failure_ip_hour where user_id=?", userID)
		_, _ = db.Exec("delete from bp_call_region_ip_hour where user_id=?", userID)
		_, _ = db.Exec("delete from bp_call_region_hour where user_id=?", userID)
		_, _ = db.Exec("delete from bp_call_user_metric_minute where user_id=?", userID)
		_, _ = db.Exec("delete from bp_call_api_metric_day where user_id=?", userID)
		_, _ = db.Exec("delete from bp_call_api_metric_minute where user_id=?", userID)
		_, _ = db.Exec("delete from bp_interface_api where id=?", interfaceID)
		_, _ = db.Exec("update bp_analytics_checkpoint set last_log_id=0,source_rows=0,contribution_rows=0 where worker_key=?", CheckpointKey)
	})
	result, err := db.ExecContext(ctx, `
		insert into bp_interface_api(api_code,name,request_url,request_method,status,remark)
		values(?,?,'https://127.0.0.1/analytics-zero-checkpoint','GET',1,'zero checkpoint regression')
	`, apiCode, apiCode)
	if err != nil {
		t.Fatal(err)
	}
	interfaceID, err = result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	result, err = db.ExecContext(ctx, `
		insert into bp_interface_call_log(
			interface_id,user_id,request_method,response_status,success,billable,charge_type,
			charge_amount,point_price,elapsed_ms,client_ip,ip_country,ip_province,ip_city,
			latitude,longitude,request_fingerprint,request_size,response_size,user_visible,created_at
		) values(?,?,'GET',200,1,1,'FREE',0,0,19,'203.0.113.99',
			'CN','Zero Province','Zero City',31.2,121.5,repeat('z',64),10,20,1,now())
	`, interfaceID, userID)
	if err != nil {
		t.Fatal(err)
	}
	logID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	// Simulate migration 000079 having populated only the compact region
	// projection before the checkpoint row was restored.
	if _, err := db.ExecContext(ctx, `
		insert into bp_call_region_hour(
			bucket_start,user_id,country,province,city,isp,latitude,longitude,
			call_count,success_count,failure_count,unique_ip_count,
			request_bytes,response_bytes,elapsed_sum,first_seen,last_seen
		) values(date_format(now(),'%Y-%m-%d %H:00:00'),?,?,?,?,?,31.2,121.5,1,1,0,1,10,20,19,now(),now())
	`, userID, "CN", "Zero Province", "Zero City", "Regression ISP"); err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(ctx, `
		update bp_analytics_checkpoint
		set last_log_id=0,last_log_created_at=null,source_rows=0,contribution_rows=0,last_batch_size=0
		where worker_key=?
	`, CheckpointKey); err != nil {
		t.Fatal(err)
	}

	batch := processAllAvailableForIntegrationTest(t, ctx, NewRepository(db), 10_000)
	if batch.ToLogID < logID {
		t.Fatalf("zero-checkpoint recovery did not advance to source high-water mark: %+v", batch)
	}
	var calls int64
	if err := db.QueryRowContext(ctx, `
		select coalesce(sum(call_count),0)
		from bp_call_region_hour
		where user_id=? and country='CN' and province='Zero Province'
	`, userID).Scan(&calls); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("prepopulated region calls=%d, want exactly 1 after recovery", calls)
	}
}

// TestProcessBatchRewindsCheckpointPastNewestLog covers the restore/migrate
// failure mode: the checkpoint carries an id from a previous dataset that is
// larger than every row in the current bp_interface_call_log. Without the
// rewind guard the worker spins forever on zero source rows and every
// dashboard rollup stays empty. After the guard, the checkpoint lands on the
// newest existing id and a subsequently appended log row is aggregated.
func TestProcessBatchRewindsCheckpointPastNewestLog(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	clearDurableDayIntegrationTables(t, ctx, db)

	// This test exercises only the stale-watermark guard.  Use a completely
	// empty source/projection state so a row left by another integration case
	// cannot legitimately trigger the zero-checkpoint recovery branch during
	// the second ProcessBatch call.  The DSN is required to be an isolated,
	// disposable database by ValidateIsolatedMySQLDSN above.
	if _, err := db.ExecContext(ctx, "delete from bp_interface_call_log"); err != nil {
		t.Fatal(err)
	}
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
	} {
		if _, err := db.ExecContext(ctx, "delete from "+table); err != nil {
			if table == "bp_call_api_metric_day" && isMissingOptionalRollupTable(err, table) {
				continue
			}
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		update bp_analytics_checkpoint
		set last_log_id=0,last_log_created_at=null,source_rows=0,contribution_rows=0,
		    last_batch_size=0,last_error=''
		where worker_key='call_log_rollup'
	`); err != nil {
		t.Fatal(err)
	}
	// 000090 columns are optional for this rolling-upgrade regression.  Reset
	// them when present, but keep the fixture runnable against an 89 schema.
	if _, err := db.ExecContext(ctx, `
		update bp_analytics_checkpoint
		set repair_status='NONE',repair_window_days=0,repair_requested_at=null,
		    repair_started_at=null,repair_completed_at=null
		where worker_key='call_log_rollup'
	`); err != nil && !isMissingRepairColumnError(err) {
		t.Fatal(err)
	}

	var baselineLogID int64
	if err := db.QueryRowContext(ctx, "select coalesce(max(id),0) from bp_interface_call_log").Scan(&baselineLogID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		update bp_analytics_checkpoint
		set last_log_id=?,last_log_created_at=null,source_rows=0,contribution_rows=0
		where worker_key='call_log_rollup'
	`, baselineLogID+1_000_000); err != nil {
		t.Fatal(err)
	}

	var userID, interfaceID int64
	if err := db.QueryRowContext(ctx, "select id from bp_user order by id limit 1").Scan(&userID); err != nil {
		t.Skipf("integration database has no user: %v", err)
	}
	if err := db.QueryRowContext(ctx, "select id from bp_interface_api order by id limit 1").Scan(&interfaceID); err != nil {
		t.Skipf("integration database has no interface: %v", err)
	}

	repo := NewRepository(db)
	// The stale watermark must be rewound to the newest existing id without
	// aggregating anything.
	rewound, err := repo.ProcessBatch(ctx, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if rewound.FromLogID != baselineLogID || rewound.ToLogID != baselineLogID {
		t.Fatalf("stale watermark not rewound: %+v (baseline=%d)", rewound, baselineLogID)
	}
	var checkpoint int64
	if err := db.QueryRowContext(ctx, `
		select last_log_id from bp_analytics_checkpoint where worker_key='call_log_rollup'
	`).Scan(&checkpoint); err != nil {
		t.Fatal(err)
	}
	if checkpoint != baselineLogID {
		t.Fatalf("checkpoint=%d, want rewound baseline=%d", checkpoint, baselineLogID)
	}

	result, err := db.ExecContext(ctx, `
		insert into bp_interface_call_log(
			interface_id,user_id,request_method,response_status,success,billable,charge_type,
			charge_amount,point_price,elapsed_ms,client_ip,request_size,response_size,user_visible,created_at
		) values (?,?, 'GET',200,1,0,'FREE',0,0,5,'203.0.113.9',0,0,1,now())
	`, interfaceID, userID)
	if err != nil {
		t.Fatal(err)
	}
	newID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_interface_call_log where id=?", newID)
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
	})

	batch, err := repo.ProcessBatch(ctx, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	if batch.SourceRows != 1 || batch.ToLogID < newID {
		t.Fatalf("post-rewind batch did not pick up the new row: %+v", batch)
	}
}

func TestWorkerClose(t *testing.T) {
	worker := (*Worker)(nil)
	if err := worker.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	w := &Worker{cancel: cancel, done: done}
	go func() {
		<-cancelled.Done()
		close(done)
	}()
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := w.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(ctx); err != nil {
		t.Fatalf("second close: %v", err)
	}
}
