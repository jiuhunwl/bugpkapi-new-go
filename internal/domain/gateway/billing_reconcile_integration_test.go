package gateway

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestConfirmBillingWithoutCallLogIntegration(t *testing.T) {
	dsn := os.Getenv("BUGPK_GATEWAY_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_GATEWAY_INTEGRATION_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	var revenueBefore string
	if err := db.QueryRowContext(ctx, `select cast(confirmed_charge_amount as char) from bp_billing_revenue_projection where id=1`).Scan(&revenueBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `update bp_billing_revenue_projection set confirmed_charge_amount=100.0000 where id=1`); err != nil {
		t.Fatal(err)
	}
	result, err := db.ExecContext(ctx, `
		insert into bp_billing_record(user_id,interface_id,api_code,charge_type,charge_amount,point_amount,status,reason)
		values(990019,1,'specified-response-test','BALANCE','1.2500',0,'RESERVED','specified response without call log')
	`)
	if err != nil {
		t.Fatal(err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`delete from bp_billing_record where id=?`, recordID)
		_, _ = db.Exec(`update bp_billing_revenue_projection set confirmed_charge_amount=? where id=1`, revenueBefore)
	})

	repo := NewRepository(db)
	if err := repo.ConfirmBilling(ctx, BillingReservation{BillingRecordID: recordID, Reserved: true}, 0); err != nil {
		t.Fatal(err)
	}
	var status string
	var callLogID sql.NullInt64
	if err := db.QueryRowContext(ctx, `select status,call_log_id from bp_billing_record where id=?`, recordID).Scan(&status, &callLogID); err != nil {
		t.Fatal(err)
	}
	if status != "CONFIRMED" || callLogID.Valid {
		t.Fatalf("specified-response billing = status %q callLogID %+v, want CONFIRMED without call log", status, callLogID)
	}
	var revenueAfter string
	if err := db.QueryRowContext(ctx, `select cast(confirmed_charge_amount as char) from bp_billing_revenue_projection where id=1`).Scan(&revenueAfter); err != nil {
		t.Fatal(err)
	}
	if revenueAfter != "101.2500" {
		t.Fatalf("confirmed revenue = %s, want 101.2500", revenueAfter)
	}
	if err := repo.ConfirmBilling(ctx, BillingReservation{BillingRecordID: recordID, Reserved: true}, 0); err == nil {
		t.Fatal("duplicate confirmation was accepted")
	}
	var projected int
	if err := db.QueryRowContext(ctx, "select revenue_projected from bp_billing_record where id=?", recordID).Scan(&projected); err != nil || projected != 1 {
		t.Fatalf("confirmation did not project its durable record: %d %v", projected, err)
	}
}

func TestReconcileStaleBillingIntegration(t *testing.T) {
	dsn := os.Getenv("BUGPK_GATEWAY_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_GATEWAY_INTEGRATION_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const userID int64 = 990017
	if _, err := db.ExecContext(ctx, `delete from bp_wallet_transaction where user_id=?`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `delete from bp_billing_record where user_id=?`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `delete from bp_user where id=?`, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`delete from bp_wallet_transaction where user_id=?`, userID)
		_, _ = db.Exec(`delete from bp_billing_record where user_id=?`, userID)
		_, _ = db.Exec(`delete from bp_user where id=?`, userID)
		_ = db.Close()
	})
	if _, err := db.ExecContext(ctx, `
		insert into bp_user(id,username,password_hash,display_name,balance,points,status)
		values(?,?,?,?,?,?,1)
	`, userID, "billing-reconcile-test", "not-used", "Billing Reconcile Test", "9.0000", 0); err != nil {
		t.Fatal(err)
	}
	result, err := db.ExecContext(ctx, `
		insert into bp_billing_record(user_id,interface_id,api_code,charge_type,charge_amount,point_amount,status,reason,created_at)
		values(?,1,'reconcile-test','BALANCE','1.0000',0,'RESERVED','integration test',date_sub(now(), interval 10 minute))
	`, userID)
	if err != nil {
		t.Fatal(err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	released, err := repo.ReconcileStaleBilling(ctx, time.Now().Add(-5*time.Minute), 100)
	if err != nil {
		t.Fatalf("ReconcileStaleBilling() error = %v", err)
	}
	if released != 1 {
		t.Fatalf("released = %d, want 1", released)
	}
	var balance, status string
	var refunds int
	if err := db.QueryRowContext(ctx, `select cast(balance as char) from bp_user where id=?`, userID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `select status from bp_billing_record where id=?`, recordID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `select count(*) from bp_wallet_transaction where user_id=? and biz_type='OPEN_CALL_BALANCE_REFUND' and biz_id=?`, userID, recordID).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if balance != "10.0000" || status != "RELEASED" || refunds != 1 {
		t.Fatalf("unexpected reconciliation result: balance=%s status=%s refunds=%d", balance, status, refunds)
	}
	released, err = repo.ReconcileStaleBilling(ctx, time.Now(), 100)
	if err != nil || released != 0 {
		t.Fatalf("idempotent reconcile = (%d, %v), want (0, nil)", released, err)
	}
}

func TestConcurrentReconcileStaleBillingIntegration(t *testing.T) {
	dsn := os.Getenv("BUGPK_GATEWAY_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_GATEWAY_INTEGRATION_DSN is not configured")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const userID int64 = 990018
	cleanupBillingReconcileUser(t, db, userID)
	t.Cleanup(func() {
		cleanupBillingReconcileUser(t, db, userID)
		_ = db.Close()
	})

	if _, err := db.ExecContext(ctx, `
		insert into bp_user(id,username,password_hash,display_name,balance,points,status)
		values(?,?,?,?,?,?,1)
	`, userID, "billing-reconcile-race", "not-used", "Billing Reconcile Race", "9.0000", 0); err != nil {
		t.Fatal(err)
	}
	result, err := db.ExecContext(ctx, `
		insert into bp_billing_record(user_id,interface_id,api_code,charge_type,charge_amount,point_amount,status,reason,created_at)
		values(?,1,'reconcile-race','BALANCE','1.0000',0,'RESERVED','integration race test',date_sub(now(), interval 10 minute))
	`, userID)
	if err != nil {
		t.Fatal(err)
	}
	recordID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	repo := NewRepository(db)
	start := make(chan struct{})
	results := make(chan int, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			released, err := repo.ReconcileStaleBilling(ctx, time.Now().Add(-5*time.Minute), 100)
			results <- released
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	totalReleased := 0
	for released := range results {
		totalReleased += released
	}
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent reconcile error = %v", err)
		}
	}
	if totalReleased != 1 {
		t.Fatalf("concurrent released total = %d, want 1", totalReleased)
	}
	var balance, status string
	var refunds int
	if err := db.QueryRowContext(ctx, `select cast(balance as char) from bp_user where id=?`, userID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `select status from bp_billing_record where id=?`, recordID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `select count(*) from bp_wallet_transaction where user_id=? and biz_type='OPEN_CALL_BALANCE_REFUND' and biz_id=?`, userID, recordID).Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if balance != "10.0000" || status != "RELEASED" || refunds != 1 {
		t.Fatalf("unexpected concurrent reconciliation: balance=%s status=%s refunds=%d", balance, status, refunds)
	}
}

func TestBillingRefundCapacityFailureKeepsReservationIntegration(t *testing.T) {
	dsn := os.Getenv("BUGPK_GATEWAY_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_GATEWAY_INTEGRATION_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const balanceUserID int64 = 990021
	const pointsUserID int64 = 990022
	cleanupBillingReconcileUser(t, db, balanceUserID)
	cleanupBillingReconcileUser(t, db, pointsUserID)
	t.Cleanup(func() {
		cleanupBillingReconcileUser(t, db, balanceUserID)
		cleanupBillingReconcileUser(t, db, pointsUserID)
		_ = db.Close()
	})
	for _, user := range []struct {
		id      int64
		name    string
		balance string
		points  int64
	}{
		{balanceUserID, "billing-refund-balance-capacity", "99999999999999.9999", 0},
		{pointsUserID, "billing-refund-point-capacity", "0.0000", maxGatewayPoints - 50},
	} {
		if _, err := db.ExecContext(ctx, `
			insert into bp_user(id,username,password_hash,display_name,balance,points,status)
			values(?,?,?,?,?,?,1)
		`, user.id, user.name, "not-used", user.name, user.balance, user.points); err != nil {
			t.Fatal(err)
		}
	}
	balanceInsert, err := db.ExecContext(ctx, `
		insert into bp_billing_record(user_id,interface_id,api_code,charge_type,charge_amount,point_amount,status,reason)
		values(?,1,'refund-capacity-balance','BALANCE','1.0000',0,'RESERVED','integration test')
	`, balanceUserID)
	if err != nil {
		t.Fatal(err)
	}
	balanceRecordID, err := balanceInsert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	pointInsert, err := db.ExecContext(ctx, `
		insert into bp_billing_record(user_id,interface_id,api_code,charge_type,charge_amount,point_amount,status,reason)
		values(?,1,'refund-capacity-points','POINTS','0.0000',100,'RESERVED','integration test')
	`, pointsUserID)
	if err != nil {
		t.Fatal(err)
	}
	pointRecordID, err := pointInsert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	if _, err := repo.releaseBillingRecord(ctx, balanceRecordID, "capacity test"); !errors.Is(err, ErrBalanceRefundCapacity) {
		t.Fatalf("balance refund error = %v, want ErrBalanceRefundCapacity", err)
	}
	if _, err := repo.releaseBillingRecord(ctx, pointRecordID, "capacity test"); !errors.Is(err, ErrPointRefundCapacity) {
		t.Fatalf("point refund error = %v, want ErrPointRefundCapacity", err)
	}
	var balanceStatus, pointStatus string
	if err := db.QueryRowContext(ctx, `select status from bp_billing_record where id=?`, balanceRecordID).Scan(&balanceStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `select status from bp_billing_record where id=?`, pointRecordID).Scan(&pointStatus); err != nil {
		t.Fatal(err)
	}
	if balanceStatus != "RESERVED" || pointStatus != "RESERVED" {
		t.Fatalf("capacity failure changed reservation status: balance=%s points=%s", balanceStatus, pointStatus)
	}
}

func TestCreateOpenLogAndReleaseIsAtomicIntegration(t *testing.T) {
	dsn := os.Getenv("BUGPK_GATEWAY_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_GATEWAY_INTEGRATION_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const userID int64 = 990019
	cleanupBillingReconcileUser(t, db, userID)
	t.Cleanup(func() {
		_, _ = db.Exec(`delete from bp_interface_call_log where user_id=?`, userID)
		cleanupBillingReconcileUser(t, db, userID)
		_ = db.Close()
	})
	if _, err := db.ExecContext(ctx, `
		insert into bp_user(id,username,password_hash,display_name,balance,points,status)
		values(?,?,?,?,?,?,1)
	`, userID, "billing-log-release", "not-used", "Billing Log Release", "9.0000", 0); err != nil {
		t.Fatal(err)
	}
	insert, err := db.ExecContext(ctx, `
		insert into bp_billing_record(user_id,interface_id,api_code,charge_type,charge_amount,point_amount,status,reason)
		values(?,1,'atomic-release','BALANCE','1.0000',0,'RESERVED','integration test')
	`, userID)
	if err != nil {
		t.Fatal(err)
	}
	recordID, _ := insert.LastInsertId()
	reservation := BillingReservation{BillingRecordID: recordID, ChargeType: "BALANCE", ChargeAmount: "1.0000", Reserved: true}
	result := OpenResult{
		APICode: "atomic-release", BillingRecordID: recordID, BillingStatus: "RELEASED",
		ChargeAmount: "0.0000", ChargeType: "NO_CHARGE", InterfaceID: 1,
		RequestFingerprint: "fingerprint:atomic-release", RequestMethod: "GET", ResponseStatus: 200, Success: true, TraceID: "atomic_release_trace",
		UserID: userID, UserVisible: true,
	}
	logID, err := NewRepository(db).CreateOpenLogAndRelease(ctx, result, reservation, "billing rule not matched")
	if err != nil {
		t.Fatal(err)
	}
	retryLogID, err := NewRepository(db).CreateOpenLogAndRelease(ctx, result, reservation, "billing rule not matched")
	if err != nil {
		t.Fatal(err)
	}
	if retryLogID != logID {
		t.Fatalf("idempotent retry log=%d, want %d", retryLogID, logID)
	}
	var balance, status string
	var storedLogID int64
	var logCount, refundCount int
	if err := db.QueryRowContext(ctx, `select cast(balance as char) from bp_user where id=?`, userID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `select status,coalesce(call_log_id,0) from bp_billing_record where id=?`, recordID).Scan(&status, &storedLogID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `select count(*) from bp_interface_call_log where billing_record_id=?`, recordID).Scan(&logCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `select count(*) from bp_wallet_transaction where biz_type='OPEN_CALL_BALANCE_REFUND' and biz_id=?`, recordID).Scan(&refundCount); err != nil {
		t.Fatal(err)
	}
	if balance != "10.0000" || status != "RELEASED" || storedLogID != logID || logCount != 1 || refundCount != 1 {
		t.Fatalf("atomic release mismatch balance=%s status=%s log=%d wantLog=%d logs=%d refunds=%d", balance, status, storedLogID, logID, logCount, refundCount)
	}
}

func cleanupBillingReconcileUser(t *testing.T, db *sql.DB, userID int64) {
	t.Helper()
	if _, err := db.Exec(`delete from bp_interface_call_log where user_id=?`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`delete from bp_wallet_transaction where user_id=?`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`delete from bp_billing_record where user_id=?`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`delete from bp_user where id=?`, userID); err != nil {
		t.Fatal(err)
	}
}
