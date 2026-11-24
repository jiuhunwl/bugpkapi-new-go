package payment_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"bugpk-api/internal/app"
	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/payment"
	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestRechargePresetDeleteLifecycleIntegration(t *testing.T) {
	if os.Getenv("PAYMENT_INTEGRATION") != "1" {
		t.Skip("set PAYMENT_INTEGRATION=1 to run")
	}
	db := openPaymentIntegrationDB(t)
	db.SetMaxOpenConns(8)
	repo := payment.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)

	unusedID := insertRechargePreset(t, db, "Unused "+stamp)
	if err := repo.DeletePreset(ctx, unusedID); err != nil {
		t.Fatalf("delete unused preset: %v", err)
	}

	historyID := insertRechargePreset(t, db, "History "+stamp)
	if _, err := db.ExecContext(ctx, `
		insert into bp_payment_order(order_no,user_id,order_type,product,subject,target_id,pay_amount,status,expire_at,snapshot)
		values(?,1,'RECHARGE','PAGE','preset history',?,10.0000,'CANCELLED',date_add(now(),interval 30 minute),'{}')
	`, "BPPRESETHISTORY"+stamp, historyID); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeletePreset(ctx, historyID); !errors.Is(err, payment.ErrRechargePresetHasOrders) {
		t.Fatalf("history preset delete error = %v", err)
	}

	staleID := insertRechargePreset(t, db, "Stale "+stamp)
	seed, err := repo.BuildRechargeOrder(ctx, 1, staleID, "BPPRESETSTALE"+stamp, "PAGE")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.DeletePreset(ctx, staleID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateOrder(ctx, seed); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale recharge seed create error = %v", err)
	}

	raceID := insertRechargePreset(t, db, "Race "+stamp)
	raceSeed, err := repo.BuildRechargeOrder(ctx, 1, raceID, "BPPRESETRACE"+stamp, "PAGE")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	createResult := make(chan error, 1)
	deleteResult := make(chan error, 1)
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		_, createErr := repo.CreateOrder(ctx, raceSeed)
		createResult <- createErr
	}()
	go func() {
		defer wait.Done()
		<-start
		deleteResult <- repo.DeletePreset(ctx, raceID)
	}()
	close(start)
	wait.Wait()
	createErr := <-createResult
	deleteErr := <-deleteResult
	createWon := createErr == nil && errors.Is(deleteErr, payment.ErrRechargePresetHasOrders)
	deleteWon := deleteErr == nil && errors.Is(createErr, sql.ErrNoRows)
	if !createWon && !deleteWon {
		t.Fatalf("unexpected preset create/delete race: create=%v delete=%v", createErr, deleteErr)
	}
}

func insertRechargePreset(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	result, err := db.Exec(`
		insert into bp_recharge_preset(name,amount,gift_amount,sort_order,status)
		values(?,10.0000,1.0000,100,1)
	`, name)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(fmt.Errorf("preset id: %w", err))
	}
	return id
}

func TestRechargePresetUpdateTimeIntegration(t *testing.T) {
	if os.Getenv("PAYMENT_INTEGRATION") != "1" {
		t.Skip("set PAYMENT_INTEGRATION=1 to run")
	}
	db := openPaymentIntegrationDB(t)

	repo := payment.NewRepository(db)
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	name := "Update Time " + stamp
	presetID := insertRechargePreset(t, db, name)
	t.Cleanup(func() { _, _ = db.Exec(`delete from bp_recharge_preset where id=?`, presetID) })
	before, err := repo.ListPresets(context.Background(), payment.ListParams{Keyword: name, Page: 1, PageSize: 20}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Items) == 0 || before.Items[0].UpdateTime == "" {
		t.Fatalf("preset list did not return database update time: %#v", before.Items)
	}

	time.Sleep(1100 * time.Millisecond)
	if err := repo.SavePreset(context.Background(), presetID, payment.RechargePreset{
		Amount:     "50.0000",
		GiftAmount: "1.0000",
		Name:       name,
		SortOrder:  1,
		Status:     1,
	}); err != nil {
		t.Fatal(err)
	}
	after, err := repo.ListPresets(context.Background(), payment.ListParams{Keyword: name, Page: 1, PageSize: 20}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Items) != 1 || after.Items[0].UpdateTime == "" {
		t.Fatalf("updated preset is missing update time: %#v", after.Items)
	}
	if after.Items[0].UpdateTime <= before.Items[0].UpdateTime {
		t.Fatalf("update time did not advance: before=%q after=%q", before.Items[0].UpdateTime, after.Items[0].UpdateTime)
	}
}

func TestPaymentOrderListScopeAndFieldsIntegration(t *testing.T) {
	if os.Getenv("PAYMENT_INTEGRATION") != "1" {
		t.Skip("set PAYMENT_INTEGRATION=1 to run")
	}
	db := openPaymentIntegrationDB(t)
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	username := "order-user-" + stamp
	insert, err := db.Exec(`
		insert into bp_user(username,password_hash,display_name,status)
		values(?, 'integration-test-hash', 'Order Viewer', 1)
	`, username)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := insert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	orderNo := "BPORDER" + stamp
	tradeNo := "TRADE" + stamp
	if _, err := db.Exec(`
		insert into bp_payment_order(
			order_no,user_id,order_type,product,subject,target_id,pay_amount,gift_amount,
			grant_points,status,alipay_trade_no,buyer_id,expire_at,paid_at,settled_at
		) values(?,?,'RECHARGE','PAGE','Integration recharge',1,12.3400,1.2300,0,'PAID',?,'buyer-test',date_add(now(),interval 30 minute),now(),now())
	`, orderNo, userID, tradeNo); err != nil {
		t.Fatal(err)
	}

	repo := payment.NewRepository(db)
	adminList, err := repo.ListOrders(context.Background(), payment.ListParams{Keyword: username, Page: 1, PageSize: 20}, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(adminList.Items) != 1 {
		t.Fatalf("admin username search did not find order: %#v", adminList.Items)
	}
	assertPaymentOrderFields(t, adminList.Items[0], orderNo, tradeNo, "Order Viewer")

	userList, err := repo.ListOrders(context.Background(), payment.ListParams{Page: 1, PageSize: 20}, userID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(userList.Items) != 1 {
		t.Fatalf("user-scoped order list = %#v", userList.Items)
	}
	assertPaymentOrderFields(t, userList.Items[0], orderNo, tradeNo, "Order Viewer")

	if _, err := repo.OrderByNo(context.Background(), orderNo, 1, false); err != sql.ErrNoRows {
		t.Fatalf("cross-user order lookup error = %v, want sql.ErrNoRows", err)
	}
	detail, err := repo.OrderByNo(context.Background(), orderNo, userID, false)
	if err != nil {
		t.Fatal(err)
	}
	assertPaymentOrderFields(t, detail, orderNo, tradeNo, "Order Viewer")
}

func assertPaymentOrderFields(t *testing.T, item payment.PaymentOrder, orderNo, tradeNo, username string) {
	t.Helper()
	if item.OrderNo != orderNo || item.AlipayTradeNo != tradeNo || item.Username != username {
		t.Fatalf("unexpected payment order identity fields: %#v", item)
	}
	if item.PayAmount != "12.3400" || item.PayChannel != "ALIPAY" || item.PaidTime == "" || item.CreateTime == "" {
		t.Fatalf("payment order display fields are incomplete: %#v", item)
	}
}

func openPaymentIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()
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
	return db
}
