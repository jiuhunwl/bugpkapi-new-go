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

func TestConcurrentPointPackagePurchaseDoesNotOverdrawBalance(t *testing.T) {
	dsn := os.Getenv("BUGPK_PACKAGE_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_PACKAGE_INTEGRATION_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(8)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping integration database: %v", err)
	}

	suffix := time.Now().UnixNano()
	username := fmt.Sprintf("package_concurrency_%d", suffix)
	userResult, err := db.ExecContext(ctx, `
		insert into bp_user(username,password_hash,display_name,balance,points,status)
		values(?, '$2a$10$integration.test.hash.placeholder', ?, 10.0000, 0, 1)
	`, username, username)
	if err != nil {
		t.Fatalf("insert integration user: %v", err)
	}
	userID, err := userResult.LastInsertId()
	if err != nil {
		t.Fatalf("integration user id: %v", err)
	}

	packageResult, err := db.ExecContext(ctx, `
		insert into bp_package_point(name,price,points,status,remark)
		values(?,10.0000,100,1,'concurrency integration test')
	`, fmt.Sprintf("Concurrency package %d", suffix))
	if err != nil {
		t.Fatalf("insert integration package: %v", err)
	}
	packageID, err := packageResult.LastInsertId()
	if err != nil {
		t.Fatalf("integration package id: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_balance_purchase_request where user_id=?", userID)
		_, _ = db.Exec("delete from bp_wallet_transaction where user_id=?", userID)
		_, _ = db.Exec("delete from bp_user where id=?", userID)
		_, _ = db.Exec("delete from bp_package_point where id=?", packageID)
	})

	repo := NewRepository(db)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for index := range 2 {
		wg.Add(1)
		go func(purchaseIndex int) {
			defer wg.Done()
			<-start
			results <- repo.PurchasePoint(ctx, userID, packageID, fmt.Sprintf("purchase-concurrency-%d-%d", suffix, purchaseIndex))
		}(index)
	}
	close(start)
	wg.Wait()
	close(results)

	var successes, insufficient int
	for purchaseErr := range results {
		switch {
		case purchaseErr == nil:
			successes++
		case errors.Is(purchaseErr, ErrInsufficientBalance):
			insufficient++
		default:
			t.Fatalf("unexpected concurrent purchase error: %v", purchaseErr)
		}
	}
	if successes != 1 || insufficient != 1 {
		t.Fatalf("concurrent results success=%d insufficient=%d", successes, insufficient)
	}

	var balance string
	var points int64
	if err := db.QueryRowContext(ctx, `select cast(balance as char),points from bp_user where id=?`, userID).Scan(&balance, &points); err != nil {
		t.Fatalf("query final wallet: %v", err)
	}
	if balance != "0.0000" || points != 100 {
		t.Fatalf("final wallet balance=%s points=%d", balance, points)
	}

	var walletRows int
	if err := db.QueryRowContext(ctx, `
		select count(*) from bp_wallet_transaction
		where user_id=? and biz_id=? and biz_type in ('PURCHASE_POINT_PACKAGE','POINTS_GRANT')
	`, userID, packageID).Scan(&walletRows); err != nil {
		t.Fatalf("query wallet rows: %v", err)
	}
	if walletRows != 2 {
		t.Fatalf("wallet transaction rows=%d, want 2", walletRows)
	}
}

func TestFreePointPackagePurchaseDoesNotReportInsufficientBalance(t *testing.T) {
	dsn := os.Getenv("BUGPK_PACKAGE_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_PACKAGE_INTEGRATION_DSN is not configured")
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
	suffix := time.Now().UnixNano()
	username := fmt.Sprintf("free_point_package_%d", suffix)
	userResult, err := db.ExecContext(ctx, `
		insert into bp_user(username,password_hash,display_name,balance,points,status)
		values(?, '$2a$10$integration.test.hash.placeholder', ?, 0.0000, 0, 1)
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
		values(?,0.0000,50,1,'free package integration test')
	`, fmt.Sprintf("Free point package %d", suffix))
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

	if err := NewRepository(db).PurchasePoint(ctx, userID, packageID, fmt.Sprintf("free-point-purchase-%d", suffix)); err != nil {
		t.Fatalf("free point package purchase failed: %v", err)
	}
	var balance string
	var points int64
	if err := db.QueryRowContext(ctx, "select cast(balance as char),points from bp_user where id=?", userID).Scan(&balance, &points); err != nil {
		t.Fatal(err)
	}
	if balance != "0.0000" || points != 50 {
		t.Fatalf("free purchase wallet balance=%s points=%d", balance, points)
	}
	var purchaseRows, grantRows int
	if err := db.QueryRowContext(ctx, `
		select sum(biz_type='PURCHASE_POINT_PACKAGE'),sum(biz_type='POINTS_GRANT')
		from bp_wallet_transaction where user_id=? and biz_id=?
	`, userID, packageID).Scan(&purchaseRows, &grantRows); err != nil {
		t.Fatal(err)
	}
	if purchaseRows != 1 || grantRows != 1 {
		t.Fatalf("free purchase wallet rows purchase=%d grant=%d", purchaseRows, grantRows)
	}
}

func TestConcurrentFreePointPackagePurchaseAllowsOnlyOneClaimAcrossKeys(t *testing.T) {
	dsn := os.Getenv("BUGPK_PACKAGE_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_PACKAGE_INTEGRATION_DSN is not configured")
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

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	suffix := time.Now().UnixNano()
	username := fmt.Sprintf("free_point_concurrency_%d", suffix)
	userResult, err := db.ExecContext(ctx, `
		insert into bp_user(username,password_hash,display_name,balance,points,status)
		values(?, '$2a$10$integration.test.hash.placeholder', ?, 0.0000, 0, 1)
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
		values(?,0.0000,75,1,'free package concurrency test')
	`, fmt.Sprintf("Free concurrency package %d", suffix))
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
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for index := range 2 {
		wg.Add(1)
		go func(purchaseIndex int) {
			defer wg.Done()
			<-start
			results <- repo.PurchasePoint(ctx, userID, packageID, fmt.Sprintf("free-claim-%d-%d", suffix, purchaseIndex))
		}(index)
	}
	close(start)
	wg.Wait()
	close(results)

	var successes, duplicateClaims int
	for purchaseErr := range results {
		switch {
		case purchaseErr == nil:
			successes++
		case errors.Is(purchaseErr, ErrFreePackageAlreadyClaimed):
			duplicateClaims++
		default:
			t.Fatalf("unexpected concurrent free-claim error: %v", purchaseErr)
		}
	}
	if successes != 1 || duplicateClaims != 1 {
		t.Fatalf("concurrent free claims success=%d duplicate=%d", successes, duplicateClaims)
	}
	if err := repo.PurchasePoint(ctx, userID, packageID, fmt.Sprintf("free-claim-retry-%d", suffix)); !errors.Is(err, ErrFreePackageAlreadyClaimed) {
		t.Fatalf("third free claim error=%v, want ErrFreePackageAlreadyClaimed", err)
	}

	var balance string
	var points int64
	if err := db.QueryRowContext(ctx, `select cast(balance as char),points from bp_user where id=?`, userID).Scan(&balance, &points); err != nil {
		t.Fatal(err)
	}
	if balance != "0.0000" || points != 75 {
		t.Fatalf("free claim wallet balance=%s points=%d", balance, points)
	}
	var walletRows, completedRequests int
	if err := db.QueryRowContext(ctx, `
		select count(*) from bp_wallet_transaction
		where user_id=? and biz_id=? and biz_type in ('PURCHASE_POINT_PACKAGE','POINTS_GRANT')
	`, userID, packageID).Scan(&walletRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `
		select count(*) from bp_balance_purchase_request
		where user_id=? and purchase_type='POINT' and target_id=? and status='COMPLETED'
	`, userID, packageID).Scan(&completedRequests); err != nil {
		t.Fatal(err)
	}
	if walletRows != 2 || completedRequests != 1 {
		t.Fatalf("free claim rows wallet=%d completed=%d", walletRows, completedRequests)
	}
}

func TestPaidPointPackageCanBePurchasedMoreThanOnceWithDifferentKeys(t *testing.T) {
	dsn := os.Getenv("BUGPK_PACKAGE_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_PACKAGE_INTEGRATION_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	suffix := time.Now().UnixNano()
	username := fmt.Sprintf("paid_point_repeat_%d", suffix)
	userResult, err := db.ExecContext(ctx, `
		insert into bp_user(username,password_hash,display_name,balance,points,status)
		values(?, '$2a$10$integration.test.hash.placeholder', ?, 20.0000, 0, 1)
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
		values(?,5.0000,100,1,'paid package repeat test')
	`, fmt.Sprintf("Paid repeat package %d", suffix))
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
	if err := repo.PurchasePoint(ctx, userID, packageID, fmt.Sprintf("paid-repeat-first-%d", suffix)); err != nil {
		t.Fatalf("first paid purchase failed: %v", err)
	}
	if err := repo.PurchasePoint(ctx, userID, packageID, fmt.Sprintf("paid-repeat-second-%d", suffix)); err != nil {
		t.Fatalf("second paid purchase failed: %v", err)
	}

	var balance string
	var points int64
	if err := db.QueryRowContext(ctx, `select cast(balance as char),points from bp_user where id=?`, userID).Scan(&balance, &points); err != nil {
		t.Fatal(err)
	}
	if balance != "10.0000" || points != 200 {
		t.Fatalf("paid repeat wallet balance=%s points=%d", balance, points)
	}
}

func TestFreeGlobalAndInterfacePackagesCannotBeClaimedTwice(t *testing.T) {
	dsn := os.Getenv("BUGPK_PACKAGE_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_PACKAGE_INTEGRATION_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	suffix := time.Now().UnixNano()
	username := fmt.Sprintf("free_scope_claim_%d", suffix)
	userResult, err := db.ExecContext(ctx, `
		insert into bp_user(username,password_hash,display_name,balance,points,status)
		values(?, '$2a$10$integration.test.hash.placeholder', ?, 0.0000, 0, 1)
	`, username, username)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := userResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	globalResult, err := db.ExecContext(ctx, `
		insert into bp_package_global(name,price,valid_days,daily_limit,qps_limit,status,remark)
		values(?,0.0000,7,100,5,1,'free global claim test')
	`, fmt.Sprintf("Free global %d", suffix))
	if err != nil {
		t.Fatal(err)
	}
	globalID, err := globalResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	interfaceResult, err := db.ExecContext(ctx, `
		insert into bp_interface_api(api_code,name,request_url,request_method,remark,status)
		values(?,?,?,'GET','free interface claim test',1)
	`, fmt.Sprintf("free_claim_%d", suffix), fmt.Sprintf("Free API %d", suffix), "https://example.com/free")
	if err != nil {
		t.Fatal(err)
	}
	interfaceID, err := interfaceResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	interfacePackageResult, err := db.ExecContext(ctx, `
		insert into bp_package_interface(interface_id,name,status,remark)
		values(?,?,1,'free interface package claim test')
	`, interfaceID, fmt.Sprintf("Free interface package %d", suffix))
	if err != nil {
		t.Fatal(err)
	}
	interfacePackageID, err := interfacePackageResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	specResult, err := db.ExecContext(ctx, `
		insert into bp_package_interface_spec(package_id,name,price,valid_days,daily_limit,qps_limit,status)
		values(?,?,0.0000,7,100,5,1)
	`, interfacePackageID, fmt.Sprintf("Free spec %d", suffix))
	if err != nil {
		t.Fatal(err)
	}
	specID, err := specResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_balance_purchase_request where user_id=?", userID)
		_, _ = db.Exec("delete from bp_wallet_transaction where user_id=?", userID)
		_, _ = db.Exec("delete from bp_user_package_interface where user_id=?", userID)
		_, _ = db.Exec("delete from bp_user_package_global where user_id=?", userID)
		_, _ = db.Exec("delete from bp_user where id=?", userID)
		_, _ = db.Exec("delete from bp_package_interface_spec where id=?", specID)
		_, _ = db.Exec("delete from bp_package_interface where id=?", interfacePackageID)
		_, _ = db.Exec("delete from bp_package_global where id=?", globalID)
		_, _ = db.Exec("delete from bp_interface_api where id=?", interfaceID)
	})

	repo := NewRepository(db)
	if err := repo.PurchaseGlobal(ctx, userID, globalID, fmt.Sprintf("free-global-first-%d", suffix)); err != nil {
		t.Fatalf("first free global claim failed: %v", err)
	}
	if err := repo.PurchaseGlobal(ctx, userID, globalID, fmt.Sprintf("free-global-second-%d", suffix)); !errors.Is(err, ErrFreePackageAlreadyClaimed) {
		t.Fatalf("second free global claim error=%v, want ErrFreePackageAlreadyClaimed", err)
	}
	if err := repo.PurchaseInterface(ctx, userID, specID, fmt.Sprintf("free-interface-first-%d", suffix)); err != nil {
		t.Fatalf("first free interface claim failed: %v", err)
	}
	if err := repo.PurchaseInterface(ctx, userID, specID, fmt.Sprintf("free-interface-second-%d", suffix)); !errors.Is(err, ErrFreePackageAlreadyClaimed) {
		t.Fatalf("second free interface claim error=%v, want ErrFreePackageAlreadyClaimed", err)
	}

	var globalEntitlements, interfaceEntitlements int
	if err := db.QueryRowContext(ctx, `select count(*) from bp_user_package_global where user_id=? and package_id=?`, userID, globalID).Scan(&globalEntitlements); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `select count(*) from bp_user_package_interface where user_id=? and spec_id=?`, userID, specID).Scan(&interfaceEntitlements); err != nil {
		t.Fatal(err)
	}
	if globalEntitlements != 1 || interfaceEntitlements != 1 {
		t.Fatalf("free entitlement rows global=%d interface=%d", globalEntitlements, interfaceEntitlements)
	}
}

// 回归：余额购买全站套餐曾在发放权益时因 INSERT 列/值数量不一致返回 500。
func TestPaidGlobalPackagePurchaseGrantsEntitlement(t *testing.T) {
	dsn := os.Getenv("BUGPK_PACKAGE_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_PACKAGE_INTEGRATION_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	suffix := time.Now().UnixNano()
	username := fmt.Sprintf("paid_global_package_%d", suffix)
	userResult, err := db.ExecContext(ctx, `
		insert into bp_user(username,password_hash,display_name,balance,points,status)
		values(?, '$2a$10$integration.test.hash.placeholder', ?, 20.0000, 0, 1)
	`, username, username)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := userResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	packageResult, err := db.ExecContext(ctx, `
		insert into bp_package_global(name,price,valid_days,daily_limit,total_limit,count_mode,qps_limit,status,remark)
		values(?,10.0000,30,100,5000,'attempt',5,1,'paid global purchase test')
	`, fmt.Sprintf("Paid global %d", suffix))
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
		_, _ = db.Exec("delete from bp_user_package_global where user_id=?", userID)
		_, _ = db.Exec("delete from bp_user where id=?", userID)
		_, _ = db.Exec("delete from bp_package_global where id=?", packageID)
	})

	repo := NewRepository(db)
	if err := repo.PurchaseGlobal(ctx, userID, packageID, fmt.Sprintf("paid-global-%d", suffix)); err != nil {
		t.Fatalf("paid global purchase failed: %v", err)
	}

	var balance string
	if err := db.QueryRowContext(ctx, `select cast(balance as char) from bp_user where id=?`, userID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != "10.0000" {
		t.Fatalf("balance=%s, want 10.0000", balance)
	}
	var dailyLimit, totalLimit int64
	var countMode string
	var qpsLimit int
	if err := db.QueryRowContext(ctx, `
		select daily_limit,total_limit,count_mode,qps_limit
		from bp_user_package_global where user_id=? and package_id=? order by id desc limit 1
	`, userID, packageID).Scan(&dailyLimit, &totalLimit, &countMode, &qpsLimit); err != nil {
		t.Fatal(err)
	}
	if dailyLimit != 100 || totalLimit != 5000 || countMode != "attempt" || qpsLimit != 5 {
		t.Fatalf("entitlement daily=%d total=%d mode=%s qps=%d", dailyLimit, totalLimit, countMode, qpsLimit)
	}
}

func TestPointPackagePurchaseRollsBackWhenPointBalanceWouldOverflow(t *testing.T) {
	dsn := os.Getenv("BUGPK_PACKAGE_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_PACKAGE_INTEGRATION_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	suffix := time.Now().UnixNano()
	username := fmt.Sprintf("point_limit_%d", suffix)
	initialPoints := maxUserPointBalance - 50
	userResult, err := db.ExecContext(ctx, `
		insert into bp_user(username,password_hash,display_name,balance,points,status)
		values(?,'$2a$10$integration.test.hash.placeholder',?,20.0000,?,1)
	`, username, username, initialPoints)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := userResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	packageResult, err := db.ExecContext(ctx, `
		insert into bp_package_point(name,price,points,status,remark)
		values(?,10.0000,100,1,'point balance limit integration test')
	`, fmt.Sprintf("Point limit package %d", suffix))
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

	purchaseErr := NewRepository(db).PurchasePoint(ctx, userID, packageID, fmt.Sprintf("point-limit-%d", suffix))
	if !errors.Is(purchaseErr, ErrPointBalanceLimit) {
		t.Fatalf("purchase error = %v, want ErrPointBalanceLimit", purchaseErr)
	}

	var balance string
	var points int64
	if err := db.QueryRowContext(ctx, "select cast(balance as char),points from bp_user where id=?", userID).Scan(&balance, &points); err != nil {
		t.Fatal(err)
	}
	if balance != "20.0000" || points != initialPoints {
		t.Fatalf("rolled-back wallet balance=%s points=%d, want 20.0000/%d", balance, points, initialPoints)
	}
	var requestRows, walletRows int
	if err := db.QueryRowContext(ctx, "select count(*) from bp_balance_purchase_request where user_id=?", userID).Scan(&requestRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "select count(*) from bp_wallet_transaction where user_id=?", userID).Scan(&walletRows); err != nil {
		t.Fatal(err)
	}
	if requestRows != 0 || walletRows != 0 {
		t.Fatalf("rolled-back rows idempotency=%d wallet=%d, want 0/0", requestRows, walletRows)
	}
}
