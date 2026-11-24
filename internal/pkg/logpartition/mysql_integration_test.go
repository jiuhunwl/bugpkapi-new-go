package logpartition

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
	"github.com/go-sql-driver/mysql"
)

func TestMySQLPartitionConversionAndPruning(t *testing.T) {
	dsn := os.Getenv("BUGPK_PARTITION_TEST_DSN")
	if dsn == "" {
		t.Skip("BUGPK_PARTITION_TEST_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(cfg.DBName, "bugpk_partition_test") {
		t.Fatal("requires dedicated bugpk_partition_test* database")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(3)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err = db.ExecContext(ctx, "CREATE TABLE bp_interface_call_log (id BIGINT NOT NULL AUTO_INCREMENT, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, user_id BIGINT NOT NULL DEFAULT 1, response_body MEDIUMTEXT, PRIMARY KEY(id), KEY idx_created(created_at,id), KEY idx_user(user_id,created_at)) ENGINE=InnoDB")
	if err != nil {
		t.Fatalf("fixture table must not already exist: %v", err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP TABLE bp_interface_call_log") })
	_, err = db.ExecContext(ctx, "INSERT INTO bp_interface_call_log(created_at) VALUES ('2026-08-10'),('2026-09-10'),('2026-12-10')")
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{Now: month("2026-09"), MonthsAhead: 1}
	plan, err := Run(ctx, db, opts)
	if err != nil || !plan.RequiresRebuild {
		t.Fatalf("preview: %+v %v", plan, err)
	}
	opts.Apply = true
	if _, err := Run(ctx, db, opts); err == nil {
		t.Fatal("rebuild ran without explicit authorization")
	}
	var primary string
	if err := db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(column_name ORDER BY seq_in_index) FROM information_schema.statistics WHERE table_schema=database() AND table_name='bp_interface_call_log' AND index_name='PRIMARY'").Scan(&primary); err != nil || primary != "id" {
		t.Fatalf("preview/refused apply modified primary key: %q %v", primary, err)
	}
	// Refuse constraints that native partitioning would otherwise invalidate.
	if _, err := db.ExecContext(ctx, "ALTER TABLE bp_interface_call_log ADD UNIQUE test_unique_id(id)"); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, db, Options{Now: opts.Now, MonthsAhead: 1}); err == nil {
		t.Fatal("unexpected unique index was silently weakened")
	}
	if _, err := db.ExecContext(ctx, "ALTER TABLE bp_interface_call_log DROP INDEX test_unique_id"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE bp_partition_test_link(log_id BIGINT, FOREIGN KEY(log_id) REFERENCES bp_interface_call_log(id)) ENGINE=InnoDB"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP TABLE IF EXISTS bp_partition_test_link") })
	if _, err := Run(ctx, db, Options{Now: opts.Now, MonthsAhead: 1}); err == nil {
		t.Fatal("foreign key dependency was ignored")
	}
	if _, err := db.ExecContext(ctx, "DROP TABLE bp_partition_test_link"); err != nil {
		t.Fatal(err)
	}
	guard, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Close()
	var locked int
	if err := guard.QueryRowContext(ctx, "SELECT GET_LOCK("+advisoryLockNameSQL+",0)").Scan(&locked); err != nil || locked != 1 {
		t.Fatalf("lock setup: %d %v", locked, err)
	}
	opts.AllowRebuild = true
	if _, err := Run(ctx, db, opts); err == nil {
		t.Fatal("concurrent maintenance was not excluded")
	}
	if _, err := guard.ExecContext(ctx, "DO RELEASE_LOCK("+advisoryLockNameSQL+")"); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, db, opts); err != nil {
		t.Fatal(err)
	}
	var ids string
	if err := db.QueryRowContext(ctx, "SELECT GROUP_CONCAT(id ORDER BY id) FROM bp_interface_call_log").Scan(&ids); err != nil || ids != "1,2,3" {
		t.Fatalf("IDs changed: %q %v", ids, err)
	}
	var explain string
	if err := db.QueryRowContext(ctx, "EXPLAIN FORMAT=JSON SELECT id FROM bp_interface_call_log WHERE created_at >= '2026-09-01' AND created_at < '2026-10-01' ORDER BY created_at,id LIMIT 20").Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "p202609") || strings.Contains(explain, "p202608") || strings.Contains(explain, "p_future") {
		t.Fatalf("partition pruning failed: %s", explain)
	}
	if plan, err := Run(ctx, db, opts); err != nil || plan.SQL != "" {
		t.Fatalf("second apply not idempotent: %+v %v", plan, err)
	}
	// Even legacy COPY preference must not turn maintenance into a table rebuild.
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, "SET SESSION old_alter_table=ON"); err != nil {
		t.Fatal(err)
	}
	opts.Now = month("2026-11")
	if plan, err := Run(ctx, db, opts); err != nil || plan.RequiresRebuild {
		t.Fatalf("extension failed: %+v %v", plan, err)
	}
	var december int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM bp_interface_call_log PARTITION(p202612) WHERE id=3").Scan(&december); err != nil || december != 1 {
		t.Fatalf("future row lost: %d %v", december, err)
	}
	result, err := db.ExecContext(ctx, "INSERT INTO bp_interface_call_log(created_at) VALUES ('2025-01-01')")
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil || id != 4 {
		t.Fatalf("AUTO_INCREMENT sequence changed: %d %v", id, err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO bp_interface_call_log(created_at) VALUES ('2026-09-20')"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM bp_interface_call_log").Scan(&count); err != nil || count != 4 {
		t.Fatalf("rollback semantics changed: %d %v", count, err)
	}
	t.Log("month pruning, retained IDs, MAXVALUE split, backfill, AUTO_INCREMENT and rollback verified")
}

func TestMySQLPartitionDropExpired(t *testing.T) {
	dsn := os.Getenv("BUGPK_PARTITION_TEST_DSN")
	if dsn == "" {
		t.Skip("BUGPK_PARTITION_TEST_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(cfg.DBName, "bugpk_partition_test") {
		t.Fatal("requires dedicated bugpk_partition_test* database")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(3)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err = db.ExecContext(ctx, "CREATE TABLE bp_interface_call_log (id BIGINT NOT NULL AUTO_INCREMENT, created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP, user_id BIGINT NOT NULL DEFAULT 1, PRIMARY KEY(id, created_at)) ENGINE=InnoDB PARTITION BY RANGE COLUMNS (created_at) (PARTITION p_before VALUES LESS THAN ('2026-08-01'), PARTITION p202608 VALUES LESS THAN ('2026-09-01'), PARTITION p202609 VALUES LESS THAN ('2026-10-01'), PARTITION p202610 VALUES LESS THAN ('2026-11-01'), PARTITION p_future VALUES LESS THAN (MAXVALUE))")
	if err != nil {
		t.Fatalf("fixture table must not already exist: %v", err)
	}
	t.Cleanup(func() { _, _ = db.ExecContext(context.Background(), "DROP TABLE bp_interface_call_log") })
	_, err = db.ExecContext(ctx, "INSERT INTO bp_interface_call_log(created_at) VALUES ('2026-08-10'),('2026-08-20'),('2026-09-10'),('2026-10-15')")
	if err != nil {
		t.Fatal(err)
	}
	// With a 31-day retention from 2026-11-01, only p202608 has fully expired
	// (its upper bound 2026-09-01 is <= 2026-10-01). p202609 and p202610 are
	// retained, and p_before/p_future are never dropped.
	opts := DropOptions{Now: month("2026-11"), RetainDays: 31, MaxLogID: 2, Apply: false}
	plan, err := DropExpired(ctx, db, opts)
	if err != nil {
		t.Fatalf("DropExpired preview: %v", err)
	}
	if len(plan.Partitions) != 1 || plan.Partitions[0] != "p202608" {
		t.Fatalf("preview dropped %v, want [p202608]", plan.Partitions)
	}
	if !strings.Contains(plan.SQL, "DROP PARTITION p202608") {
		t.Fatalf("preview SQL missing drop: %q", plan.SQL)
	}
	// A watermark that has not reached the partition's max id must block the drop.
	plan, err = DropExpired(ctx, db, DropOptions{Now: month("2026-11"), RetainDays: 31, MaxLogID: 1, Apply: false})
	if err != nil {
		t.Fatalf("DropExpired low watermark: %v", err)
	}
	if len(plan.Partitions) != 0 {
		t.Fatalf("low watermark must not drop a partition with unsummarized rows: %v", plan.Partitions)
	}
	// Apply the drop and confirm only p202608 rows are gone.
	if _, err = DropExpired(ctx, db, DropOptions{Now: month("2026-11"), RetainDays: 31, MaxLogID: 2, Apply: true}); err != nil {
		t.Fatalf("DropExpired apply: %v", err)
	}
	var august int
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM bp_interface_call_log WHERE created_at < '2026-09-01'").Scan(&august); err != nil || august != 0 {
		t.Fatalf("p202608 rows survived drop: %d %v", august, err)
	}
	var september int
	if err = db.QueryRowContext(ctx, "SELECT count(*) FROM bp_interface_call_log WHERE created_at >= '2026-09-01'").Scan(&september); err != nil || september != 2 {
		t.Fatalf("retained rows changed: %d %v", september, err)
	}
	t.Log("expired partition dropped, watermark guard and partial months verified")
}
