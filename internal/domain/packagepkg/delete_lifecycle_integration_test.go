package packagepkg_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"bugpk-api/internal/app"
	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/packagepkg"
	"bugpk-api/internal/domain/payment"
	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestPackageDeleteLifecycleIntegration(t *testing.T) {
	if os.Getenv("PACKAGE_DELETE_INTEGRATION") != "1" {
		t.Skip("set PACKAGE_DELETE_INTEGRATION=1 to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if err := testdb.ValidateIsolatedMySQLDSN(cfg.Database.DSN()); err != nil {
		t.Fatal(err)
	}
	cfg.Migration.Path = "../../../migrations"
	db, err := sql.Open("mysql", cfg.Database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(12)
	if err := app.RunMigrations(db, cfg.Migration.Path); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	repo := packagepkg.NewRepository(db)
	paymentRepo := payment.NewRepository(db)
	suffix := time.Now().UnixNano()

	unusedGlobal := insertGlobalPackage(t, db, fmt.Sprintf("unused-global-%d", suffix), "0.0000")
	if err := repo.DeleteGlobal(ctx, unusedGlobal, false); err != nil {
		t.Fatalf("delete unused global package: %v", err)
	}
	unusedPoint := insertPointPackage(t, db, fmt.Sprintf("unused-point-%d", suffix), "0.0000")
	if err := repo.DeletePoint(ctx, unusedPoint, false); err != nil {
		t.Fatalf("delete unused point package: %v", err)
	}

	interfaceID := insertInterface(t, db, fmt.Sprintf("package-delete-%d", suffix))
	unusedInterfacePackage, _ := insertInterfacePackage(t, db, interfaceID, fmt.Sprintf("unused-interface-%d", suffix))
	if err := repo.DeleteInterface(ctx, unusedInterfacePackage, false); err != nil {
		t.Fatalf("delete unused interface package: %v", err)
	}

	globalID := insertGlobalPackage(t, db, fmt.Sprintf("history-global-%d", suffix), "0.0000")
	if _, err := db.ExecContext(ctx, `
		insert into bp_user_package_global(user_id,package_id,package_name,daily_limit,qps_limit,start_at,end_at,status)
		values(1,?, 'history global',0,0,now(),date_add(now(),interval 30 day),1)
	`, globalID); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteGlobal(ctx, globalID, false); !errors.Is(err, packagepkg.ErrPackageHasBusinessHistory) {
		t.Fatalf("global package history guard error = %v", err)
	}

	pointID := insertPointPackage(t, db, fmt.Sprintf("history-point-%d", suffix), "1.0000")
	if _, err := db.ExecContext(ctx, `
		insert into bp_payment_order(order_no,user_id,order_type,product,subject,target_id,pay_amount,status,expire_at,snapshot)
		values(?,1,'POINT_PACKAGE','PAGE','history point',?,1.0000,'CANCELLED',date_add(now(),interval 30 minute),'{}')
	`, fmt.Sprintf("BP_POINT_HISTORY_%d", suffix), pointID); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeletePoint(ctx, pointID, false); !errors.Is(err, packagepkg.ErrPackageHasBusinessHistory) {
		t.Fatalf("point package history guard error = %v", err)
	}

	interfacePackageID, specID := insertInterfacePackage(t, db, interfaceID, fmt.Sprintf("history-interface-%d", suffix))
	if _, err := db.ExecContext(ctx, `
		insert into bp_wallet_transaction(user_id,biz_type,biz_id,amount,points,balance_after,points_after,remark)
		select id,'PURCHASE_INTERFACE_PACKAGE',?,0,0,balance,points,'history interface' from bp_user where id=1
	`, specID); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteInterface(ctx, interfacePackageID, false); !errors.Is(err, packagepkg.ErrPackageHasBusinessHistory) {
		t.Fatalf("interface package history guard error = %v", err)
	}

	// 强制删除：跳过历史校验，级联移除用户权益行；支付与钱包流水保留审计。
	if err := repo.DeleteGlobal(ctx, globalID, true); err != nil {
		t.Fatalf("force delete global package: %v", err)
	}
	var entitlements int
	if err := db.QueryRowContext(ctx, `select count(*) from bp_user_package_global where package_id=?`, globalID).Scan(&entitlements); err != nil {
		t.Fatal(err)
	}
	if entitlements != 0 {
		t.Fatalf("force delete global should cascade user entitlements, remaining=%d", entitlements)
	}

	if err := repo.DeleteInterface(ctx, interfacePackageID, true); err != nil {
		t.Fatalf("force delete interface package: %v", err)
	}
	var interfaceEntitlements int
	if err := db.QueryRowContext(ctx, `select count(*) from bp_user_package_interface where package_id=?`, interfacePackageID).Scan(&interfaceEntitlements); err != nil {
		t.Fatal(err)
	}
	if interfaceEntitlements != 0 {
		t.Fatalf("force delete interface should cascade user entitlements, remaining=%d", interfaceEntitlements)
	}

	if err := repo.DeletePoint(ctx, pointID, true); err != nil {
		t.Fatalf("force delete point package: %v", err)
	}
	var walletRows int
	if err := db.QueryRowContext(ctx, `select count(*) from bp_wallet_transaction where biz_type='PURCHASE_INTERFACE_PACKAGE' and biz_id=?`, specID).Scan(&walletRows); err != nil {
		t.Fatal(err)
	}
	if walletRows == 0 {
		t.Fatal("force delete must keep wallet transactions for audit")
	}

	staleGlobalID := insertGlobalPackage(t, db, fmt.Sprintf("stale-global-%d", suffix), "2.0000")
	globalSeed, err := paymentRepo.BuildGlobalPackageOrder(ctx, 1, staleGlobalID, fmt.Sprintf("BP_STALE_GLOBAL_%d", suffix), "PAGE")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteGlobal(ctx, staleGlobalID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := paymentRepo.CreateOrder(ctx, globalSeed); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale global seed create error = %v", err)
	}

	stalePointID := insertPointPackage(t, db, fmt.Sprintf("stale-point-%d", suffix), "2.0000")
	pointSeed, err := paymentRepo.BuildPointPackageOrder(ctx, 1, stalePointID, fmt.Sprintf("BP_STALE_POINT_%d", suffix), "PAGE")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.DeletePoint(ctx, stalePointID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := paymentRepo.CreateOrder(ctx, pointSeed); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale point seed create error = %v", err)
	}

	testConcurrentGlobalPurchaseAndDelete(t, ctx, db, repo, suffix)
}

func testConcurrentGlobalPurchaseAndDelete(t *testing.T, ctx context.Context, db *sql.DB, repo *packagepkg.Repository, suffix int64) {
	t.Helper()
	username := fmt.Sprintf("package-delete-race-%d", suffix)
	result, err := db.ExecContext(ctx, `
		insert into bp_user(username,password_hash,display_name,balance,points,status)
		values(?,'$2a$10$integration.placeholder.hash',?,10.0000,0,1)
	`, username, username)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	packageID := insertGlobalPackage(t, db, fmt.Sprintf("race-global-%d", suffix), "1.0000")

	start := make(chan struct{})
	purchaseResult := make(chan error, 1)
	deleteResult := make(chan error, 1)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		purchaseResult <- repo.PurchaseGlobal(ctx, userID, packageID, fmt.Sprintf("delete-race-purchase-%d", suffix))
	}()
	go func() {
		defer wait.Done()
		<-start
		deleteResult <- repo.DeleteGlobal(ctx, packageID, false)
	}()
	close(start)
	wait.Wait()
	purchaseErr := <-purchaseResult
	deleteErr := <-deleteResult

	purchaseWon := purchaseErr == nil && errors.Is(deleteErr, packagepkg.ErrPackageHasBusinessHistory)
	deleteWon := deleteErr == nil && errors.Is(purchaseErr, sql.ErrNoRows)
	if !purchaseWon && !deleteWon {
		t.Fatalf("unexpected purchase/delete race: purchase=%v delete=%v", purchaseErr, deleteErr)
	}
}

func insertGlobalPackage(t *testing.T, db *sql.DB, name, price string) int64 {
	t.Helper()
	result, err := db.Exec(`insert into bp_package_global(name,price,valid_days,status) values(?,?,30,1)`, name, price)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertPointPackage(t *testing.T, db *sql.DB, name, price string) int64 {
	t.Helper()
	result, err := db.Exec(`insert into bp_package_point(name,price,points,status) values(?,?,100,1)`, name, price)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertInterface(t *testing.T, db *sql.DB, apiCode string) int64 {
	t.Helper()
	result, err := db.Exec(`
		insert into bp_interface_api(api_code,name,request_url,request_method,remark,status)
		values(?,'Package Delete API','https://example.com/upstream','GET','integration',1)
	`, apiCode)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertInterfacePackage(t *testing.T, db *sql.DB, interfaceID int64, name string) (int64, int64) {
	t.Helper()
	result, err := db.Exec(`insert into bp_package_interface(interface_id,name,status) values(?,?,1)`, interfaceID, name)
	if err != nil {
		t.Fatal(err)
	}
	packageID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	result, err = db.Exec(`
		insert into bp_package_interface_spec(package_id,name,price,valid_days,status,sort_order)
		values(?,'monthly',1.0000,30,1,1)
	`, packageID)
	if err != nil {
		t.Fatal(err)
	}
	specID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return packageID, specID
}
