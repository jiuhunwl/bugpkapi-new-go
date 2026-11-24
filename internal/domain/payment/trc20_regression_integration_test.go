package payment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"
)

func setupTrc20Regression(t *testing.T, db *sql.DB, address string) *Repository {
	t.Helper()
	_, err := db.Exec(`insert ignore into bp_user(id,username,password_hash,display_name,balance,points,primary_role_id) values(987600,'trc20-regression','fixture-only','TRC20',0,0,2)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`insert ignore into bp_recharge_preset(id,name,amount,gift_amount,sort_order,status) values(987600,'TRC20 fixture',10,0,0,1)`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`update bp_payment_trc20_config set enabled=1,address=?,min_amount=5,max_amount=500,confirmations=1,cursor_timestamp_ms=0 where id=1`, address)
	if err != nil {
		t.Fatal(err)
	}
	return NewRepository(db)
}
func trc20RegressionSeed(orderNo string) OrderSeed {
	return OrderSeed{OrderNo: orderNo, UserID: 987600, OrderType: "RECHARGE", Product: "PAGE", PayChannel: ChannelTrc20, Currency: CurrencyUSD, PayAmount: "10.0000", OriginAmount: "10.0000", GiftAmount: "0.0000", TargetID: 987600, Subject: "TRC20 fixture", Snapshot: "{}"}
}
func seedTrc20Slots(t *testing.T, db *sql.DB, address, prefix string, skip int, ageHours int) {
	t.Helper()
	for slot := 1; slot <= 99; slot++ {
		if slot == skip {
			continue
		}
		_, err := db.Exec(`insert into bp_payment_order(order_no,user_id,order_type,product,pay_channel,currency,origin_amount,subject,target_id,pay_amount,gift_amount,status,expire_at,created_at,chain_address,chain_amount,snapshot,deleted_at)
 values(?,987600,'RECHARGE','PAGE','TRC20','USD',10,'slot',987600,?,0,'EXPIRED',date_sub(now(),interval 1 hour),date_sub(now(),interval ? hour),?,?,'{}',now())`, fmt.Sprintf("%s_%d", prefix, slot), fmt.Sprintf("10.%02d00", slot), ageHours, address, fmt.Sprintf("10.%02d0000", slot))
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTrc20AmountAllocationWindowAndConcurrency(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	t.Run("expired amounts are not reassigned to another invoice", func(t *testing.T) {
		address := "T" + strings.Repeat("A", 33)
		repo := setupTrc20Regression(t, db, address)
		seedTrc20Slots(t, db, address, "OLD", 0, 25)
		_, err := repo.CreateOrder(ctx, trc20RegressionSeed("NEW_AFTER_WINDOW"))
		var business *apperrors.BusinessError
		if !errors.As(err, &business) || business.StatusCode != 409 {
			t.Fatalf("expired amount was reused despite late-payment ambiguity: %v", err)
		}
		var ts int64
		if err := db.QueryRow("select cast(unix_timestamp(date_sub(now(),interval 24 hour))*1000 as signed)").Scan(&ts); err != nil {
			t.Fatal(err)
		}
		historical := Trc20Transfer{TxID: strings.Repeat("a", 64), To: address, Amount: "10.010000", BlockTimestamp: ts}
		old, err := repo.MatchTrc20Order(ctx, historical)
		if err != nil || old != "OLD_1" {
			t.Fatalf("historical receipt lost its original invoice: %q %v", old, err)
		}
		historical.BlockTimestamp = time.Now().UnixMilli()
		order, err := repo.MatchTrc20Order(ctx, historical)
		if err != nil || order != "" {
			t.Fatalf("payment outside original invoice window was auto-credited: %q %v", order, err)
		}
	})
	t.Run("two contenders cannot reserve the last slot", func(t *testing.T) {
		address := "T" + strings.Repeat("B", 33)
		repo := setupTrc20Regression(t, db, address)
		seedTrc20Slots(t, db, address, "BUSY", 42, 0)
		lock, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Rollback()
		var id int
		var connectionID int64
		if err := lock.QueryRowContext(ctx, "select connection_id()").Scan(&connectionID); err != nil {
			t.Fatal(err)
		}
		if err := lock.QueryRowContext(ctx, "select id from bp_payment_trc20_config where id=1 for update").Scan(&id); err != nil {
			t.Fatal(err)
		}
		type result struct {
			order PaymentOrder
			err   error
		}
		results := make(chan result, 2)
		var started sync.WaitGroup
		started.Add(2)
		for i := 0; i < 2; i++ {
			go func(index int) {
				started.Done()
				order, err := repo.CreateOrder(ctx, trc20RegressionSeed(fmt.Sprintf("CONTENDER_%d", index)))
				results <- result{order, err}
			}(i)
		}
		started.Wait()
		if err := awaitOwnedPaymentLockWait(ctx, db, connectionID); err != nil {
			_ = lock.Rollback()
			t.Fatal(err)
		}
		if err := lock.Commit(); err != nil {
			t.Fatal(err)
		}
		succeeded, conflicted := 0, 0
		for i := 0; i < 2; i++ {
			select {
			case r := <-results:
				if r.err == nil {
					succeeded++
					if r.order.ChainAmount != "10.420000" {
						t.Errorf("amount=%s", r.order.ChainAmount)
					}
				} else {
					var business *apperrors.BusinessError
					if !errors.As(r.err, &business) || business.StatusCode != 409 {
						t.Errorf("unexpected allocation error: %v", r.err)
					}
					conflicted++
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		}
		if succeeded != 1 || conflicted != 1 {
			t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
		}
		var count int
		if err := db.QueryRow("select count(*) from bp_payment_order where chain_address=? and chain_amount=10.42", address).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("same amount assigned %d times", count)
		}
	})
	t.Run("legacy ambiguous orders are refused", func(t *testing.T) {
		address := "T" + strings.Repeat("C", 33)
		repo := setupTrc20Regression(t, db, address)
		for i := 0; i < 2; i++ {
			_, err := db.Exec(`insert into bp_payment_order(order_no,user_id,order_type,product,pay_channel,currency,subject,pay_amount,status,expire_at,created_at,chain_address,chain_amount)
 values(?,987600,'RECHARGE','PAGE','TRC20','USD','ambiguous',10.01,'CREATED',date_add(now(),interval 1 hour),date_sub(now(),interval 1 minute),?,10.01)`, fmt.Sprintf("AMBIGUOUS_%d", i), address)
			if err != nil {
				t.Fatal(err)
			}
		}
		_, err := repo.MatchTrc20Order(ctx, Trc20Transfer{TxID: strings.Repeat("c", 64), To: address, Amount: "10.010000", BlockTimestamp: time.Now().UnixMilli()})
		if !errors.Is(err, ErrTrc20AmbiguousOrder) {
			t.Fatalf("ambiguous amount accepted: %v", err)
		}
	})
}

func TestTrc20DurableRetryAndExactlyOnceCredit(t *testing.T) {
	db, cfg := paymentOutcomeDB(t)
	address := "T" + strings.Repeat("D", 33)
	repo := setupTrc20Regression(t, db, address)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	order, err := repo.CreateOrder(ctx, trc20RegressionSeed("RETRY_CREDIT"))
	if err != nil {
		t.Fatal(err)
	}
	transfer := Trc20Transfer{TxID: strings.Repeat("d", 64), From: "T" + strings.Repeat("E", 33), To: address, Amount: order.ChainAmount, BlockTimestamp: time.Now().UnixMilli(), Confirmations: 1}
	faultDB, fault := paymentFaultDB(t, cfg, true)
	first := NewTrc20Reconciler(&Service{repo: NewRepository(faultDB)})
	first.chain = &trc20ChainFixture{transfers: []Trc20Transfer{transfer}}
	if err := first.ReconcileOnce(ctx); err == nil {
		t.Fatal("commit fault concealed")
	}
	if !fault.Triggered() {
		t.Fatal("commit failure was not exercised")
	}
	var matched sql.NullString
	var cursor int64
	var balance string
	if err := db.QueryRow("select matched_order_no from bp_payment_trc20_transfer where txid=?", transfer.TxID).Scan(&matched); err != nil {
		t.Fatal(err)
	}
	if matched.Valid {
		t.Fatal("failed settlement was marked matched")
	}
	if err := db.QueryRow("select cursor_timestamp_ms from bp_payment_trc20_config where id=1").Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if cursor < transfer.BlockTimestamp {
		t.Fatal("fixture did not exercise a durable pending receipt behind the cursor")
	}
	second := NewTrc20Reconciler(&Service{repo: repo})
	second.chain = &trc20ChainFixture{}
	if err := second.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := second.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("select cast(balance as char) from bp_user where id=987600").Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != "10.0000" {
		t.Fatalf("credit not exactly once: %s", balance)
	}
	if err := db.QueryRow("select matched_order_no from bp_payment_trc20_transfer where txid=?", transfer.TxID).Scan(&matched); err != nil {
		t.Fatal(err)
	}
	if !matched.Valid || matched.String != order.OrderNo {
		t.Fatalf("receipt not atomically bound: %+v", matched)
	}
	var walletCount int
	if err := db.QueryRow("select count(*) from bp_wallet_transaction where user_id=987600").Scan(&walletCount); err != nil {
		t.Fatal(err)
	}
	if walletCount != 1 {
		t.Fatalf("wallet writes=%d", walletCount)
	}
}

func seedLegacyTrc20Pair(t *testing.T, db *sql.DB, address string, paidFirst bool) {
	t.Helper()
	for i := 0; i < 2; i++ {
		status := "CREATED"
		var txid any
		if i == 0 && paidFirst {
			status = "PAID"
			txid = strings.Repeat("e", 64)
		}
		_, err := db.Exec(`insert into bp_payment_order(order_no,user_id,order_type,product,pay_channel,currency,subject,pay_amount,status,expire_at,created_at,chain_address,chain_amount,chain_txid)
 values(?,987600,'RECHARGE','PAGE','TRC20','USD','legacy overlap',5.01,?,date_add(now(),interval 1 hour),date_sub(now(),interval 2 minute),?,5.01,?)`, fmt.Sprintf("LEGACY_PAIR_%d", i), status, address, txid)
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestTrc20PaidLegacyOverlapRejected(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	address := "T" + strings.Repeat("F", 33)
	repo := setupTrc20Regression(t, db, address)
	seedLegacyTrc20Pair(t, db, address, true)
	transfer := Trc20Transfer{TxID: strings.Repeat("f", 64), To: address, Amount: "5.010000", BlockTimestamp: time.Now().UnixMilli()}
	if _, err := repo.MatchTrc20Order(context.Background(), transfer); !errors.Is(err, ErrTrc20AmbiguousOrder) {
		t.Fatalf("paid+pending overlap attributed to another order: %v", err)
	}
	transfer.TxID = strings.Repeat("e", 64)
	order, err := repo.MatchTrc20Order(context.Background(), transfer)
	if err != nil || order != "LEGACY_PAIR_0" {
		t.Fatalf("same-txid recovery lost: %q %v", order, err)
	}
}

func TestTrc20RetryQueueEventuallyPassesPersistentFailures(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	address := "T" + strings.Repeat("G", 33)
	repo := setupTrc20Regression(t, db, address)
	seedLegacyTrc20Pair(t, db, address, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ts := time.Now().Add(-time.Minute).UnixMilli()
	for i := 1; i <= 200; i++ {
		err := repo.RecordTrc20Transfer(ctx, Trc20Transfer{TxID: fmt.Sprintf("%064x", i), From: "sender", To: address, Amount: "5.010000", BlockTimestamp: ts, Confirmations: 1})
		if err != nil {
			t.Fatal(err)
		}
	}
	order, err := repo.CreateOrder(ctx, trc20RegressionSeed("AFTER_200_FAILURES"))
	if err != nil {
		t.Fatal(err)
	}
	transfer := Trc20Transfer{TxID: strings.Repeat("b", 64), From: "sender", To: address, Amount: order.ChainAmount, BlockTimestamp: time.Now().UnixMilli(), Confirmations: 1}
	if err := repo.RecordTrc20Transfer(ctx, transfer); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateTrc20Cursor(ctx, transfer.BlockTimestamp+1); err != nil {
		t.Fatal(err)
	}
	reconciler := NewTrc20Reconciler(&Service{repo: repo})
	reconciler.chain = &trc20ChainFixture{}
	if err := reconciler.ReconcileOnce(ctx); err == nil {
		t.Fatal("legacy ambiguity should still report an error")
	}
	// Construct a fresh reconciler: fairness must survive worker recreation.
	reconciler = NewTrc20Reconciler(&Service{repo: repo})
	reconciler.chain = &trc20ChainFixture{}
	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("later valid receipt starved: %v", err)
	}
	var status string
	if err := db.QueryRow("select status from bp_payment_order where order_no=?", order.OrderNo).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "PAID" {
		t.Fatalf("201st receipt was not recovered: %s", status)
	}
}

func TestTrc20RetryCursorMigrationRoundTrip(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	const upPath = "../../../migrations/000136_trc20_retry_cursor.up.sql"
	const downPath = "../../../migrations/000136_trc20_retry_cursor.down.sql"
	down, err := os.ReadFile(downPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(down)); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("select count(*) from information_schema.columns where table_schema=database() and table_name='bp_payment_trc20_config' and column_name='retry_cursor_id'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("down migration retained retry cursor")
	}
	up, err := os.ReadFile(upPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(up)); err != nil {
		t.Fatal(err)
	}
	var cursor int64
	if err := db.QueryRow("select retry_cursor_id from bp_payment_trc20_config where id=1").Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	if cursor != 0 {
		t.Fatalf("new retry cursor=%d", cursor)
	}
}
