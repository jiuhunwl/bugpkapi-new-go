package packagepkg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestConcurrentPointPurchaseWithSameIdempotencyKeySettlesOnce(t *testing.T) {
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
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	suffix := time.Now().UnixNano()
	username := fmt.Sprintf("idempotent_purchase_%d", suffix)
	userResult, err := db.ExecContext(ctx, `
		insert into bp_user(username,password_hash,display_name,balance,points,status)
		values(?,'$2a$10$integration.test.hash.placeholder',?,20.0000,0,1)
	`, username, username)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := userResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	packageResult, err := db.ExecContext(ctx, `
		insert into bp_package_point(name,price,points,status,remark)
		values(?,10.0000,100,1,'idempotency integration test')
	`, fmt.Sprintf("Idempotent point package %d", suffix))
	if err != nil {
		t.Fatal(err)
	}
	packageID, err := packageResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_balance_purchase_request where user_id=?", userID)
		_, _ = db.Exec("delete from bp_wallet_transaction where user_id=?", userID)
		_, _ = db.Exec("delete from bp_user where id=?", userID)
		_, _ = db.Exec("delete from bp_package_point where id=?", packageID)
	})

	repo := NewRepository(db)
	key := fmt.Sprintf("same-point-purchase-%d", suffix)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- repo.PurchasePoint(ctx, userID, packageID, key)
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	for purchaseErr := range results {
		if purchaseErr != nil {
			t.Fatalf("idempotent purchase error: %v", purchaseErr)
		}
	}

	var balance string
	var points int64
	if err := db.QueryRowContext(ctx, "select cast(balance as char),points from bp_user where id=?", userID).Scan(&balance, &points); err != nil {
		t.Fatal(err)
	}
	if balance != "10.0000" || points != 100 {
		t.Fatalf("wallet after replay balance=%s points=%d", balance, points)
	}
	var requestRows, completedRows, walletRows int
	if err := db.QueryRowContext(ctx, `
		select count(*),coalesce(sum(status='COMPLETED'),0)
		from bp_balance_purchase_request where user_id=? and idempotency_key=?
	`, userID, key).Scan(&requestRows, &completedRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		select count(*) from bp_wallet_transaction
		where user_id=? and biz_id=? and biz_type in ('PURCHASE_POINT_PACKAGE','POINTS_GRANT')
	`, userID, packageID).Scan(&walletRows); err != nil {
		t.Fatal(err)
	}
	if requestRows != 1 || completedRows != 1 || walletRows != 2 {
		t.Fatalf("ledger rows=%d completed=%d wallet=%d", requestRows, completedRows, walletRows)
	}

	conflictErr := repo.PurchasePoint(ctx, userID, packageID+1, key)
	if !errors.Is(conflictErr, ErrPurchaseIdempotencyConflict) {
		t.Fatalf("reused key for another target error = %v", conflictErr)
	}
}
