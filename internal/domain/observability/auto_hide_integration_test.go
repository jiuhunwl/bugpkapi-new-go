package observability

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestApplyUserAutoHideThrottlesAndSerializesConcurrentCleanup(t *testing.T) {
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
	db.SetMaxOpenConns(4)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const userID = int64(1)
	const interfaceID = int64(99118)
	cleanup := func() {
		_, _ = db.ExecContext(context.Background(), "delete from bp_interface_call_log where interface_id=?", interfaceID)
		_, _ = db.ExecContext(context.Background(), "delete from bp_user_log_cleanup_config where user_id=?", userID)
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := db.ExecContext(ctx, `
		insert into bp_user_log_cleanup_config(user_id,retention_days,auto_hide_enabled,last_cleanup_at)
		values(?,30,1,now())
	`, userID); err != nil {
		t.Fatal(err)
	}
	logID := insertAutoHideTestLog(t, ctx, db, userID, interfaceID)
	repo := NewRepository(db)

	affected, err := repo.ApplyUserAutoHide(ctx, userID)
	if err != nil || affected != 0 {
		t.Fatalf("recent cleanup = %d, %v", affected, err)
	}
	if visible := autoHideTestLogVisible(t, ctx, db, logID); !visible {
		t.Fatal("recently cleaned user log was hidden before the throttle elapsed")
	}

	if _, err := db.ExecContext(ctx, `
		update bp_user_log_cleanup_config
		set last_cleanup_at=date_sub(now(), interval 2 hour)
		where user_id=?
	`, userID); err != nil {
		t.Fatal(err)
	}
	results := make(chan int64, 2)
	errorsCh := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			count, applyErr := repo.ApplyUserAutoHide(ctx, userID)
			results <- count
			errorsCh <- applyErr
		}()
	}
	wg.Wait()
	close(results)
	close(errorsCh)
	var total int64
	for applyErr := range errorsCh {
		if applyErr != nil {
			t.Fatalf("concurrent auto hide failed: %v", applyErr)
		}
	}
	for count := range results {
		total += count
	}
	if total != 1 {
		t.Fatalf("concurrent affected total = %d, want 1", total)
	}
	if visible := autoHideTestLogVisible(t, ctx, db, logID); visible {
		t.Fatal("expired user log remained visible")
	}
}

func TestCleanupLogsUsesBoundedBatches(t *testing.T) {
	dsn := os.Getenv("BUGPK_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("BUGPK_MYSQL_TEST_DSN is not configured")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const userID = int64(1)
	const interfaceID = int64(99119)
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), "delete from bp_interface_call_log where interface_id=?", interfaceID)
	})
	for range 5 {
		insertAutoHideTestLog(t, ctx, db, userID, interfaceID)
	}
	repo := NewRepository(db)
	deleted, hasMore, err := repo.cleanupLogsBatched(ctx, time.Now().Format("2006-01-02 15:04:05"), 2, 2)
	if err != nil || deleted != 4 || !hasMore {
		t.Fatalf("first bounded cleanup = %d, %v, %v", deleted, hasMore, err)
	}
	deleted, hasMore, err = repo.cleanupLogsBatched(ctx, time.Now().Format("2006-01-02 15:04:05"), 2, 2)
	if err != nil || deleted != 1 || hasMore {
		t.Fatalf("second bounded cleanup = %d, %v, %v", deleted, hasMore, err)
	}
}

func TestApplyUserAutoHideUsesBoundedBatches(t *testing.T) {
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const userID = int64(1)
	const interfaceID = int64(99120)
	cleanup := func() {
		_, _ = db.ExecContext(context.Background(), "delete from bp_interface_call_log where interface_id=?", interfaceID)
		_, _ = db.ExecContext(context.Background(), "delete from bp_user_log_cleanup_config where user_id=?", userID)
	}
	cleanup()
	t.Cleanup(cleanup)

	if _, err := db.ExecContext(ctx, `
		insert into bp_user_log_cleanup_config(user_id,retention_days,auto_hide_enabled,last_cleanup_at)
		values(?,30,1,date_sub(now(), interval 2 hour))
	`, userID); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		insertAutoHideTestLog(t, ctx, db, userID, interfaceID)
	}
	repo := NewRepository(db)
	affected, hasMore, err := repo.applyUserAutoHideBatched(ctx, userID, 2, 2)
	if err != nil || affected != 4 || !hasMore {
		t.Fatalf("first bounded auto hide = %d, %v, %v", affected, hasMore, err)
	}
	if visible := countVisibleAutoHideTestLogs(t, ctx, db, interfaceID); visible != 1 {
		t.Fatalf("visible logs after bounded auto hide = %d, want 1", visible)
	}

	if _, err := db.ExecContext(ctx, `
		update bp_user_log_cleanup_config
		set last_cleanup_at=date_sub(now(), interval 2 hour)
		where user_id=?
	`, userID); err != nil {
		t.Fatal(err)
	}
	affected, hasMore, err = repo.applyUserAutoHideBatched(ctx, userID, 2, 2)
	if err != nil || affected != 1 || hasMore {
		t.Fatalf("second bounded auto hide = %d, %v, %v", affected, hasMore, err)
	}
}

func insertAutoHideTestLog(t *testing.T, ctx context.Context, db *sql.DB, userID, interfaceID int64) int64 {
	t.Helper()
	result, err := db.ExecContext(ctx, `
		insert into bp_interface_call_log(
			interface_id,user_id,request_method,response_status,success,elapsed_ms,
			charge_type,billing_status,billable,charge_amount,point_price,user_visible,created_at
		) values(?,?,'GET',200,1,1,'FREE','CONFIRMED',1,0,0,1,date_sub(now(), interval 31 day))
	`, interfaceID, userID)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func autoHideTestLogVisible(t *testing.T, ctx context.Context, db *sql.DB, logID int64) bool {
	t.Helper()
	var visible int
	if err := db.QueryRowContext(ctx, "select user_visible from bp_interface_call_log where id=?", logID).Scan(&visible); err != nil {
		t.Fatal(err)
	}
	return visible == 1
}

func countVisibleAutoHideTestLogs(t *testing.T, ctx context.Context, db *sql.DB, interfaceID int64) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, "select count(*) from bp_interface_call_log where interface_id=? and user_visible=1", interfaceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
