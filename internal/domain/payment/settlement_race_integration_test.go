package payment_test

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
	"bugpk-api/internal/domain/payment"
	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

const (
	maxPaymentTestBalance = "99999999999999.9999"
	maxPaymentTestPoints  = int64(1_000_000_000_000_000)
)

func TestPayPayloadCannotRegressSettledOrderIntegration(t *testing.T) {
	if os.Getenv("PAYMENT_INTEGRATION") != "1" {
		t.Skip("set PAYMENT_INTEGRATION=1 to run")
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
	if err := app.RunMigrations(db, cfg.Migration.Path); err != nil {
		t.Fatal(err)
	}
	const username = "payment_race_integration"
	cleanupPaymentTestUserByUsername(t, db, username)
	insert, err := db.Exec(`
		insert into bp_user(username,password_hash,display_name,balance,status)
		values(?,'$2a$10$integration.placeholder.hash','Payment Race',0.0000,1)
	`, username)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := insert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	presetID := insertRechargePreset(t, db, "Race Settlement "+fmt.Sprint(userID))
	t.Cleanup(func() {
		cleanupPaymentTestUserByUsername(t, db, username)
		_, _ = db.Exec(`delete from bp_recharge_preset where id=?`, presetID)
	})
	repo := payment.NewRepository(db)
	const orderCount = 20
	for index := 0; index < orderCount; index++ {
		orderNo := fmt.Sprintf("BP_RACE_%03d", index)
		if _, err := repo.CreateOrder(context.Background(), payment.OrderSeed{
			OrderNo: orderNo, UserID: userID, OrderType: "RECHARGE", Product: "PAGE",
			Subject: "Race recharge", TargetID: presetID, PayAmount: "10.0000", GiftAmount: "0.0000", Snapshot: `{}`,
		}); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		errorsCh := make(chan error, 2)
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			errorsCh <- repo.SettlePaidOrder(context.Background(), orderNo, payment.AlipayTradeStatus{
				BuyerID: "buyer", Success: true, TradeNo: "TRADE_" + orderNo,
			})
		}()
		go func() {
			defer wait.Done()
			<-start
			errorsCh <- repo.SavePayPayload(context.Background(), orderNo, payment.AlipayCreateResult{
				PayForm: "<form>pay</form>",
			})
		}()
		close(start)
		wait.Wait()
		close(errorsCh)
		for operationErr := range errorsCh {
			if operationErr != nil {
				t.Fatalf("order %s concurrent operation error: %v", orderNo, operationErr)
			}
		}
		var status string
		if err := db.QueryRow(`select status from bp_payment_order where order_no=?`, orderNo).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "PAID" {
			t.Fatalf("order %s regressed to %s", orderNo, status)
		}
	}
	var balance string
	if err := db.QueryRow(`select cast(balance as char) from bp_user where id=?`, userID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != "200.0000" {
		t.Fatalf("concurrent settlements credited balance %s, want 200.0000", balance)
	}
	var walletCount int
	if err := db.QueryRow(`select count(*) from bp_wallet_transaction where user_id=? and biz_type='ALIPAY_RECHARGE'`, userID).Scan(&walletCount); err != nil {
		t.Fatal(err)
	}
	if walletCount != orderCount {
		t.Fatalf("wallet transaction count = %d, want %d", walletCount, orderCount)
	}
}

func TestAlipayTradeNumberCannotSettleMultipleOrdersIntegration(t *testing.T) {
	if os.Getenv("PAYMENT_INTEGRATION") != "1" {
		t.Skip("set PAYMENT_INTEGRATION=1 to run")
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
	if err := app.RunMigrations(db, cfg.Migration.Path); err != nil {
		t.Fatal(err)
	}
	const username = "payment_trade_unique_integration"
	cleanupPaymentTestUserByUsername(t, db, username)
	insert, err := db.Exec(`
		insert into bp_user(username,password_hash,display_name,balance,status)
		values(?,'$2a$10$integration.placeholder.hash','Payment Trade Unique',0.0000,1)
	`, username)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := insert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	presetID := insertRechargePreset(t, db, "Trade Unique "+fmt.Sprint(userID))
	t.Cleanup(func() {
		cleanupPaymentTestUserByUsername(t, db, username)
		_, _ = db.Exec(`delete from bp_recharge_preset where id=?`, presetID)
	})
	repo := payment.NewRepository(db)
	for _, seed := range []payment.OrderSeed{
		{OrderNo: "BP_TRADE_UNIQUE_A", UserID: userID, OrderType: "RECHARGE", Product: "PAGE", Subject: "First", TargetID: presetID, PayAmount: "10.0000", GiftAmount: "0.0000", Snapshot: `{}`},
		{OrderNo: "BP_TRADE_UNIQUE_B", UserID: userID, OrderType: "RECHARGE", Product: "PAGE", Subject: "Second", TargetID: presetID, PayAmount: "20.0000", GiftAmount: "0.0000", Snapshot: `{}`},
	} {
		if _, err := repo.CreateOrder(context.Background(), seed); err != nil {
			t.Fatal(err)
		}
	}
	trade := payment.AlipayTradeStatus{BuyerID: "buyer-one", Success: true, TradeNo: "ALIPAY_UNIQUE_TRADE_001"}
	if err := repo.SettlePaidOrder(context.Background(), "BP_TRADE_UNIQUE_A", trade); err != nil {
		t.Fatal(err)
	}
	if err := repo.SettlePaidOrder(context.Background(), "BP_TRADE_UNIQUE_A", trade); err != nil {
		t.Fatalf("same trade idempotency failed: %v", err)
	}
	if err := repo.SettlePaidOrder(context.Background(), "BP_TRADE_UNIQUE_A", payment.AlipayTradeStatus{
		BuyerID: "buyer-one", Success: true, TradeNo: "ALIPAY_DIFFERENT_TRADE",
	}); err == nil {
		t.Fatal("paid order accepted a different Alipay trade number")
	}
	if err := repo.SettlePaidOrder(context.Background(), "BP_TRADE_UNIQUE_A", payment.AlipayTradeStatus{
		BuyerID: "buyer-two", Success: true, TradeNo: trade.TradeNo,
	}); err == nil {
		t.Fatal("paid order accepted a different Alipay buyer")
	}
	if err := repo.SettlePaidOrder(context.Background(), "BP_TRADE_UNIQUE_B", trade); err == nil {
		t.Fatal("one Alipay trade number settled two local orders")
	}
	var firstStatus, secondStatus, balance string
	if err := db.QueryRow(`select status from bp_payment_order where order_no='BP_TRADE_UNIQUE_A'`).Scan(&firstStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`select status from bp_payment_order where order_no='BP_TRADE_UNIQUE_B'`).Scan(&secondStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`select cast(balance as char) from bp_user where id=?`, userID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if firstStatus != "PAID" || secondStatus != "CREATED" || balance != "10.0000" {
		t.Fatalf("duplicate trade settlement state = first:%s second:%s balance:%s", firstStatus, secondStatus, balance)
	}
	var walletCount int
	if err := db.QueryRow(`select count(*) from bp_wallet_transaction where user_id=?`, userID).Scan(&walletCount); err != nil {
		t.Fatal(err)
	}
	if walletCount != 1 {
		t.Fatalf("duplicate trade wallet rows = %d, want 1", walletCount)
	}
}

func TestNotifyReplayIsCoalescedAndAuditStateIsMonotonicIntegration(t *testing.T) {
	if os.Getenv("PAYMENT_INTEGRATION") != "1" {
		t.Skip("set PAYMENT_INTEGRATION=1 to run")
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
	if err := app.RunMigrations(db, cfg.Migration.Path); err != nil {
		t.Fatal(err)
	}

	notifyID := fmt.Sprintf("notify_replay_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = db.Exec(`delete from bp_payment_notify_log where notify_id=?`, notifyID)
	})
	repo := payment.NewRepository(db)
	base := map[string]string{
		"notify_id":    notifyID,
		"out_trade_no": "BP20260727120000ABCDEF123456",
		"trade_no":     "2026072722000000000000000001",
	}
	if err := repo.InsertNotifyLog(context.Background(), base, false, false, "verify failed"); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertNotifyLog(context.Background(), base, true, false, "settlement pending"); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertNotifyLog(context.Background(), base, true, true, "success"); err != nil {
		t.Fatal(err)
	}
	maliciousReplay := map[string]string{
		"notify_id":    notifyID,
		"out_trade_no": "BP20260727120000FFFFFFFFFFFF",
		"trade_no":     "2026072722000000000000009999",
	}
	if err := repo.InsertNotifyLog(context.Background(), maliciousReplay, false, false, "late invalid replay"); err != nil {
		t.Fatal(err)
	}

	var count, verified, processed int
	var attempts uint64
	var orderNo, tradeNo, message string
	if err := db.QueryRow(`
		select count(*),max(order_no),max(trade_no),max(verified),max(processed),max(message),max(attempt_count)
		from bp_payment_notify_log where notify_id=?
	`, notifyID).Scan(&count, &orderNo, &tradeNo, &verified, &processed, &message, &attempts); err != nil {
		t.Fatal(err)
	}
	if count != 1 || attempts != 4 || verified != 1 || processed != 1 || message != "success" {
		t.Fatalf("notify replay audit state count=%d attempts=%d verified=%d processed=%d message=%q", count, attempts, verified, processed, message)
	}
	if orderNo != base["out_trade_no"] || tradeNo != base["trade_no"] {
		t.Fatalf("unverified replay replaced trusted audit identity: order=%q trade=%q", orderNo, tradeNo)
	}
}

func TestRechargeSettlementCapacityFailureRollsBackAndCanRetryIntegration(t *testing.T) {
	if os.Getenv("PAYMENT_INTEGRATION") != "1" {
		t.Skip("set PAYMENT_INTEGRATION=1 to run")
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
	if err := app.RunMigrations(db, cfg.Migration.Path); err != nil {
		t.Fatal(err)
	}
	username := fmt.Sprintf("payment_balance_capacity_%d", time.Now().UnixNano())
	insert, err := db.Exec(`
		insert into bp_user(username,password_hash,display_name,balance,status)
		values(?,'$2a$10$integration.placeholder.hash','Payment Balance Capacity',99999999999995.0000,1)
	`, username)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := insert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	presetID := insertRechargePreset(t, db, "Balance Capacity "+fmt.Sprint(userID))
	t.Cleanup(func() {
		cleanupPaymentTestUserByUsername(t, db, username)
		_, _ = db.Exec(`delete from bp_recharge_preset where id=?`, presetID)
	})
	repo := payment.NewRepository(db)
	orderNo := fmt.Sprintf("BP_BALANCE_CAPACITY_%d", userID)
	if _, err := repo.CreateOrder(context.Background(), payment.OrderSeed{
		OrderNo: orderNo, UserID: userID, OrderType: "RECHARGE", Product: "PAGE",
		Subject: "Balance capacity", TargetID: presetID, PayAmount: "10.0000", GiftAmount: "0.0000", Snapshot: `{}`,
	}); err != nil {
		t.Fatal(err)
	}
	trade := payment.AlipayTradeStatus{BuyerID: "capacity-buyer", Success: true, TradeNo: "TRADE_BALANCE_CAPACITY"}
	if err := repo.SettlePaidOrder(context.Background(), orderNo, trade); !errors.Is(err, payment.ErrBalanceCapacityExceeded) {
		t.Fatalf("settlement error = %v, want ErrBalanceCapacityExceeded", err)
	}
	assertPaymentSettlementState(t, db, userID, orderNo, "99999999999995.0000", 0, "CREATED", 0)
	if _, err := db.Exec(`update bp_user set balance=99999999999989.9999 where id=?`, userID); err != nil {
		t.Fatal(err)
	}
	if err := repo.SettlePaidOrder(context.Background(), orderNo, trade); err != nil {
		t.Fatalf("settlement retry failed: %v", err)
	}
	assertPaymentSettlementState(t, db, userID, orderNo, maxPaymentTestBalance, 0, "PAID", 1)
}

func TestPointSettlementCapacityFailureRollsBackAndCanRetryIntegration(t *testing.T) {
	if os.Getenv("PAYMENT_INTEGRATION") != "1" {
		t.Skip("set PAYMENT_INTEGRATION=1 to run")
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
	if err := app.RunMigrations(db, cfg.Migration.Path); err != nil {
		t.Fatal(err)
	}
	username := fmt.Sprintf("payment_point_capacity_%d", time.Now().UnixNano())
	insert, err := db.Exec(`
		insert into bp_user(username,password_hash,display_name,balance,points,status)
		values(?,'$2a$10$integration.placeholder.hash','Payment Point Capacity',0.0000,?,1)
	`, username, maxPaymentTestPoints-50)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := insert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	packageResult, err := db.Exec(`insert into bp_package_point(name,price,points,status,remark) values(?,1.0000,100,1,'integration')`, "Point Capacity "+fmt.Sprint(userID))
	if err != nil {
		t.Fatal(err)
	}
	packageID, err := packageResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupPaymentTestUserByUsername(t, db, username)
		_, _ = db.Exec(`delete from bp_package_point where id=?`, packageID)
	})
	repo := payment.NewRepository(db)
	orderNo := fmt.Sprintf("BP_POINT_CAPACITY_%d", userID)
	if _, err := repo.CreateOrder(context.Background(), payment.OrderSeed{
		OrderNo: orderNo, UserID: userID, OrderType: "POINT_PACKAGE", Product: "PAGE",
		Subject: "Point capacity", TargetID: packageID, PayAmount: "1.0000", GrantPoints: 100, Snapshot: `{"points":100}`,
	}); err != nil {
		t.Fatal(err)
	}
	trade := payment.AlipayTradeStatus{BuyerID: "point-capacity-buyer", Success: true, TradeNo: "TRADE_POINT_CAPACITY"}
	if err := repo.SettlePaidOrder(context.Background(), orderNo, trade); !errors.Is(err, payment.ErrPointBalanceLimit) {
		t.Fatalf("settlement error = %v, want ErrPointBalanceLimit", err)
	}
	assertPaymentSettlementState(t, db, userID, orderNo, "0.0000", maxPaymentTestPoints-50, "CREATED", 0)
	if _, err := db.Exec(`update bp_user set points=? where id=?`, maxPaymentTestPoints-100, userID); err != nil {
		t.Fatal(err)
	}
	if err := repo.SettlePaidOrder(context.Background(), orderNo, trade); err != nil {
		t.Fatalf("settlement retry failed: %v", err)
	}
	assertPaymentSettlementState(t, db, userID, orderNo, "0.0000", maxPaymentTestPoints, "PAID", 1)
}

func assertPaymentSettlementState(t *testing.T, db *sql.DB, userID int64, orderNo, wantBalance string, wantPoints int64, wantStatus string, wantWalletRows int) {
	t.Helper()
	var balance, status string
	var points int64
	if err := db.QueryRow(`select cast(balance as char),points from bp_user where id=?`, userID).Scan(&balance, &points); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`select status from bp_payment_order where order_no=?`, orderNo).Scan(&status); err != nil {
		t.Fatal(err)
	}
	var walletRows int
	if err := db.QueryRow(`select count(*) from bp_wallet_transaction where user_id=?`, userID).Scan(&walletRows); err != nil {
		t.Fatal(err)
	}
	if balance != wantBalance || points != wantPoints || status != wantStatus || walletRows != wantWalletRows {
		t.Fatalf("settlement state balance=%s points=%d status=%s walletRows=%d", balance, points, status, walletRows)
	}
}

func cleanupPaymentTestUserByUsername(t *testing.T, db *sql.DB, username string) {
	t.Helper()
	rows, err := db.Query(`select id from bp_user where username=?`, username)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		_, _ = db.Exec(`delete from bp_wallet_transaction where user_id=?`, id)
		_, _ = db.Exec(`delete from bp_payment_order where user_id=?`, id)
		_, _ = db.Exec(`delete from bp_user where id=?`, id)
	}
}
