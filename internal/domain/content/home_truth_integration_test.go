package content

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
)

// homeTruthDB provisions an isolated MySQL with the full migration chain. It is
// skipped unless the operator supplies an explicit loopback fixture, so the
// default `go test` run never touches a shared database.
func homeTruthDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("BUGPK_HOME_TRUTH_DSN")
	if dsn == "" {
		t.Skip("requires explicit isolated homepage truth MySQL")
	}
	db, _ := testdb.OpenIsolatedMySQL(t, dsn, os.Getenv("BUGPK_HOME_TRUTH_DATADIR"), "bugpk_home_truth")
	files, err := filepath.Glob(filepath.Join("..", "..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	var schema strings.Builder
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		schema.Write(raw)
		schema.WriteByte('\n')
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, schema.String()); err != nil {
		t.Fatal(err)
	}
	return db
}

func homeTruthExec(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.ExecContext(ctx, statement, args...); err != nil {
		t.Fatalf("fixture statement failed: %v\n%s", err, statement)
	}
}

func homeTruthService(db *sql.DB) *Service {
	return NewService(NewRepository(db), nil, "")
}

// setRollupCheckpoint writes the durable rollup position. A zero logID with no
// timestamps models a checkpoint that has never produced a watermark.
func setRollupCheckpoint(t *testing.T, db *sql.DB, logID int64, createdAt, completedAt any) {
	t.Helper()
	homeTruthExec(t, db, "insert ignore into bp_analytics_checkpoint(worker_key) values('call_log_rollup')")
	homeTruthExec(t, db,
		"update bp_analytics_checkpoint set last_log_id=?,last_log_created_at=?,last_completed_at=?,last_error='' where worker_key='call_log_rollup'",
		logID, createdAt, completedAt,
	)
}

// The homepage must never publish traffic metrics it cannot certify. This walks
// the states an operator can actually produce: a freshly migrated database, a
// healthy pipeline, a stalled snapshot refresh, a stalled rollup and a
// switched-off worker.
func TestHomepageTruthContractAgainstRealSchema(t *testing.T) {
	db := homeTruthDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	repo := NewRepository(db)

	t.Run("seeded_snapshot_is_not_ready", func(t *testing.T) {
		// Migration 000089 seeds the singleton with a NULL source_updated_at.
		_, ok, err := repo.LoadHomeAnalyticsSnapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatal("seeded snapshot row was reported as measured")
		}
		health, err := repo.LoadAnalyticsHealth(ctx, time.Time{}, time.Time{}, true, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if health.Ready {
			t.Fatalf("seeded snapshot certified itself: %+v", health)
		}
		if !containsString(health.DegradedSections, AnalyticsSectionSnapshot) {
			t.Fatalf("seeded snapshot did not report the snapshot section: %+v", health)
		}

		// This is the regression that mattered: the homepage used to compose an
		// all-zero payload and call it ready.
		overview, err := homeTruthService(db).buildHomeOverview(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if overview.Stats.AnalyticsReady {
			t.Fatalf("homepage certified an unmeasured snapshot: %+v", overview.Stats)
		}
		if !containsString(overview.Stats.UnavailableFields, "calls24h") {
			t.Fatalf("homepage did not mark traffic fields unavailable: %+v", overview.Stats)
		}
	})

	var (
		logID      int64
		logCreated time.Time
	)
	t.Run("prepare_one_call_log", func(t *testing.T) {
		homeTruthExec(t, db,
			"insert into bp_interface_call_log(interface_id,user_id,request_method,response_status,success) values(1,1,'GET',200,1)",
		)
		if err := db.QueryRowContext(ctx, "select id,created_at from bp_interface_call_log order by id desc limit 1").Scan(&logID, &logCreated); err != nil {
			t.Fatal(err)
		}
		if logID == 0 {
			t.Fatal("fixture call log was not created")
		}
	})

	t.Run("measured_snapshot_is_certified", func(t *testing.T) {
		setRollupCheckpoint(t, db, logID, logCreated, time.Now())
		if err := repo.SaveHomeAnalyticsSnapshot(ctx, HomeAnalyticsSnapshot{
			Stats:       PlatformStats{Calls24h: 42, PeakQPS24h: 3},
			CoveredUpTo: formatCoveredUpTo(logCreated),
		}); err != nil {
			t.Fatal(err)
		}
		record, ok, err := repo.LoadHomeAnalyticsSnapshot(ctx)
		if err != nil || !ok {
			t.Fatalf("measured snapshot not readable: ok=%v err=%v", ok, err)
		}
		if record.SourceUpdatedAt.IsZero() {
			t.Fatal("measured snapshot lost its write time")
		}
		if !record.CoveredUpTo.Equal(logCreated.Truncate(time.Second)) {
			t.Fatalf("coverage round trip = %v, want %v", record.CoveredUpTo, logCreated.Truncate(time.Second))
		}
		if record.Snapshot.Stats.Calls24h != 42 {
			t.Fatalf("measured snapshot lost its payload: %+v", record.Snapshot.Stats)
		}
		// The stored payload must not carry the read-time verdict.
		if record.Snapshot.Stats.AnalyticsReady || record.Snapshot.Stats.UnavailableFields != nil {
			t.Fatalf("stored payload carries a verdict: %+v", record.Snapshot.Stats)
		}

		overview, err := homeTruthService(db).buildHomeOverview(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !overview.Stats.AnalyticsReady {
			t.Fatalf("healthy pipeline was not certified: %+v", overview.Stats)
		}
		if overview.Stats.Calls24h != 42 || len(overview.Stats.UnavailableFields) != 0 {
			t.Fatalf("certified overview lost its numbers: %+v", overview.Stats)
		}
		if overview.Stats.CoveredUpTo != formatCoveredUpTo(logCreated) {
			t.Fatalf("certified overview lost its coverage: %+v", overview.Stats)
		}
	})

	t.Run("stalled_snapshot_refresh_is_not_ready", func(t *testing.T) {
		homeTruthExec(t, db,
			"update bp_home_overview_snapshot set source_updated_at=date_sub(now(), interval 1 hour) where id=1",
		)
		overview, err := homeTruthService(db).buildHomeOverview(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if overview.Stats.AnalyticsReady {
			t.Fatalf("stale snapshot refresh was certified: %+v", overview.Stats)
		}
		if !containsString(overview.Stats.DegradedSections, AnalyticsSectionSnapshot) {
			t.Fatalf("stalled refresh did not report the snapshot section: %+v", overview.Stats)
		}
		// The last measured numbers stay visible; only the verdict changes.
		if overview.Stats.Calls24h != 42 {
			t.Fatalf("stalled refresh discarded the last measurement: %+v", overview.Stats)
		}
	})

	t.Run("stalled_rollup_behind_traffic_is_not_ready", func(t *testing.T) {
		homeTruthExec(t, db, "update bp_home_overview_snapshot set source_updated_at=now(6) where id=1")
		setRollupCheckpoint(t, db, 0, nil, nil)
		overview, err := homeTruthService(db).buildHomeOverview(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if overview.Stats.AnalyticsReady {
			t.Fatalf("stalled rollup behind a non-empty log was certified: %+v", overview.Stats)
		}
		if !containsString(overview.Stats.DegradedSections, AnalyticsSectionRollup) {
			t.Fatalf("stalled rollup did not report the rollup section: %+v", overview.Stats)
		}
	})

	t.Run("disabled_worker_is_not_ready", func(t *testing.T) {
		setRollupCheckpoint(t, db, logID, logCreated, time.Now())
		service := homeTruthService(db)
		service.SetAnalyticsEnabled(false)
		overview, err := service.buildHomeOverview(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if overview.Stats.AnalyticsReady {
			t.Fatalf("disabled worker was certified: %+v", overview.Stats)
		}
		if !containsString(overview.Stats.DegradedSections, AnalyticsSectionDisabled) {
			t.Fatalf("disabled worker did not report the analytics section: %+v", overview.Stats)
		}
	})
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
