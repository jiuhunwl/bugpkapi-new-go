package gateway

import (
	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/testdb"
	"context"
	"database/sql"
	"errors"
	"fmt"
	mysql "github.com/go-sql-driver/mysql"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var billingFaultSequence atomic.Uint64

func billingOutcomeDB(t *testing.T) (*sql.DB, *mysql.Config, InterfaceConfig) {
	t.Helper()
	dsn := os.Getenv("BUGPK_BILLING_OUTCOME_DSN")
	if dsn == "" {
		t.Skip("requires isolated billing outcome MySQL")
	}
	db, cfg := testdb.OpenIsolatedMySQL(t, dsn, os.Getenv("BUGPK_BILLING_OUTCOME_DATADIR"), "bugpk_bill")
	paths, err := filepath.Glob(filepath.Join("..", "..", "..", "migrations", "*.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	var schema strings.Builder
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		schema.Write(b)
		schema.WriteByte('\n')
	}
	schemaCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if _, err := db.ExecContext(schemaCtx, schema.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into bp_user(id,username,password_hash,display_name,balance,points,primary_role_id) values(990010,'commit-outcome','test-hash','Commit Outcome',10.0000,100,2)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into bp_interface_api(id,api_code,name,request_url,request_method,price,point_price,status,remark) values(990010,'commit-outcome','Commit Outcome','https://example.invalid','GET',1.2500,0,1,'test')"); err != nil {
		t.Fatal(err)
	}
	return db, cfg, InterfaceConfig{ID: 990010, APICode: "commit-outcome", Price: "1.2500", RequestMethod: "GET"}
}
func billingFaultDB(t *testing.T, cfg *mysql.Config, before bool) (*sql.DB, *testdb.CommitFault) {
	t.Helper()
	fault := testdb.NewCommitFault(before)
	name := fmt.Sprintf("billing-fault-%d", billingFaultSequence.Add(1))
	mysql.RegisterDialContext(name, func(ctx context.Context, address string) (net.Conn, error) {
		c, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		return fault.Wrap(c), nil
	})
	t.Cleanup(func() { mysql.DeregisterDialContext(name) })
	copied := *cfg
	copied.Net = name
	db, err := sql.Open("mysql", copied.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, fault
}
func billingBalance(t *testing.T, db *sql.DB) string {
	t.Helper()
	var balance string
	if err := db.QueryRow("select cast(balance as char) from bp_user where id=990010").Scan(&balance); err != nil {
		t.Fatal(err)
	}
	return balance
}
func TestBillingReservationRecoversLostCommitAcknowledgement(t *testing.T) {
	for _, before := range []bool{false, true} {
		t.Run(fmt.Sprint(before), func(t *testing.T) {
			db, cfg, api := billingOutcomeDB(t)
			faultDB, fault := billingFaultDB(t, cfg, before)
			res, err := NewRepository(faultDB).ReserveBilling(context.Background(), api, 0, 990010)
			if !fault.Triggered() {
				t.Fatal("COMMIT fault did not trigger")
			}
			if before {
				if err == nil || billingBalance(t, db) != "10.0000" {
					t.Fatalf("uncommitted debit reported success: %+v %v", res, err)
				}
				return
			}
			if err != nil || !res.Reserved || res.BillingRecordID <= 0 {
				t.Fatalf("committed reservation reported failure/lost its ID: %+v %v", res, err)
			}
			if billingBalance(t, db) != "8.7500" {
				t.Fatal("reservation charged wrong amount")
			}
			if err := NewRepository(db).ReleaseBilling(context.Background(), res, 990010, "test release"); err != nil {
				t.Fatal(err)
			}
			if billingBalance(t, db) != "10.0000" {
				t.Fatal("recovered reservation could not be released once")
			}
		})
	}
}
func TestBillingConfirmationRecoversLostCommitAcknowledgement(t *testing.T) {
	for _, withLog := range []bool{false, true} {
		t.Run(fmt.Sprint(withLog), func(t *testing.T) {
			db, cfg, api := billingOutcomeDB(t)
			res, err := NewRepository(db).ReserveBilling(context.Background(), api, 0, 990010)
			if err != nil {
				t.Fatal(err)
			}
			faultDB, fault := billingFaultDB(t, cfg, false)
			service := NewService(NewRepository(faultDB), nil, nil, "test-secret")
			defer service.Close(context.Background())
			if withLog {
				result := OpenResult{InterfaceID: 990010, UserID: 990010, APICode: "commit-outcome", RequestMethod: "GET", ResponseStatus: 200, Success: true, CallLogEligible: true, UserVisible: true}
				result.applyReservation(res)
				err = service.createOpenLogAndConfirm(context.Background(), result, res, 990010)
			} else {
				err = service.confirmSpecifiedResponseBilling(context.Background(), res, 990010)
			}
			if !fault.Triggered() {
				t.Fatal("COMMIT fault did not trigger")
			}
			if err != nil {
				t.Fatalf("committed successful operation reported a billing error: %v", err)
			}
			var status string
			var logCount, refunds int
			if err := db.QueryRow("select status from bp_billing_record where id=?", res.BillingRecordID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("select count(*) from bp_interface_call_log where billing_record_id=?", res.BillingRecordID).Scan(&logCount); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("select count(*) from bp_wallet_transaction where user_id=990010 and biz_type='OPEN_CALL_BALANCE_REFUND'").Scan(&refunds); err != nil {
				t.Fatal(err)
			}
			expectedLogs := 0
			if withLog {
				expectedLogs = 1
			}
			if status != "CONFIRMED" || logCount != expectedLogs || refunds != 0 || billingBalance(t, db) != "8.7500" {
				t.Fatalf("inconsistent settled state: %s logs=%d refunds=%d", status, logCount, refunds)
			}
		})
	}
}

func TestFreeCallLogCommitRecoveryDoesNotDuplicateLogs(t *testing.T) {
	for _, before := range []bool{false, true} {
		t.Run(fmt.Sprint(before), func(t *testing.T) {
			db, cfg, _ := billingOutcomeDB(t)
			faultDB, fault := billingFaultDB(t, cfg, before)
			service := NewService(NewRepository(faultDB), nil, nil, "test-secret")
			defer service.Close(context.Background())
			result := OpenResult{InterfaceID: 990010, UserID: 990010, APICode: "commit-outcome", RequestMethod: "GET", ResponseStatus: 500, CallLogEligible: true, UserVisible: true, ChargeType: "FREE", ChargeAmount: "0.0000", BillingStatus: "NONE"}
			err := service.createOpenLogAndRelease(context.Background(), result, BillingReservation{ChargeType: "FREE"}, 990010, "upstream failed")
			if !fault.Triggered() {
				t.Fatal("COMMIT fault did not trigger")
			}
			// With no immutable free-call receipt, missing and cleaned-up logs
			// cannot be distinguished. Do not guess rollback and reinsert.
			if before && !errors.Is(err, dbx.ErrCommitUncertain) {
				t.Fatalf("missing log must remain uncertain: %v", err)
			}
			if !before && err != nil {
				t.Fatalf("committed log recovery failed: %v", err)
			}
			var logs, bills, wallet int
			if err := db.QueryRow("select count(*) from bp_interface_call_log where user_id=990010").Scan(&logs); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("select count(*) from bp_billing_record where user_id=990010").Scan(&bills); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("select count(*) from bp_wallet_transaction where user_id=990010").Scan(&wallet); err != nil {
				t.Fatal(err)
			}
			wantLogs := 1
			if before {
				wantLogs = 0
			}
			if logs != wantLogs || bills != 0 || wallet != 0 {
				t.Fatalf("free log duplicated or mutated financial ledger: logs=%d bills=%d wallet=%d", logs, bills, wallet)
			}
		})
	}
}
func TestBillingReleaseLostCommitAcknowledgementRefundsOnlyOnce(t *testing.T) {
	db, cfg, api := billingOutcomeDB(t)
	res, err := NewRepository(db).ReserveBilling(context.Background(), api, 0, 990010)
	if err != nil {
		t.Fatal(err)
	}
	faultDB, fault := billingFaultDB(t, cfg, false)
	service := NewService(NewRepository(faultDB), nil, nil, "test-secret")
	defer service.Close(context.Background())
	result := OpenResult{InterfaceID: 990010, UserID: 990010, APICode: "commit-outcome", RequestMethod: "GET", ResponseStatus: 500, CallLogEligible: true, UserVisible: true}
	result.applyReservation(res)
	result.markNoCharge(res)
	if err := service.createOpenLogAndRelease(context.Background(), result, res, 990010, "upstream failed"); err != nil {
		t.Fatal(err)
	}
	if !fault.Triggered() {
		t.Fatal("release COMMIT fault not triggered")
	}
	if err := NewRepository(db).ReleaseBilling(context.Background(), res, 990010, "retry"); err != nil {
		t.Fatal(err)
	}
	var refunds, logs int
	var status string
	if err := db.QueryRow("select count(*) from bp_wallet_transaction where user_id=990010 and biz_type='OPEN_CALL_BALANCE_REFUND'").Scan(&refunds); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("select count(*) from bp_interface_call_log where billing_record_id=?", res.BillingRecordID).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("select status from bp_billing_record where id=?", res.BillingRecordID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if refunds != 1 || logs != 1 || status != "RELEASED" || billingBalance(t, db) != "10.0000" {
		t.Fatalf("release repeated side effects: refunds=%d logs=%d status=%s", refunds, logs, status)
	}
}

func TestBillingOutcomeProbeWaitsForOriginalTransactionDecision(t *testing.T) {
	db, cfg, api := billingOutcomeDB(t)
	for _, commit := range []bool{true, false} {
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatal(err)
		}
		res, err := NewRepository(db).reserveBillingTx(context.Background(), tx, api, 0, 990010)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		probeDB, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		probeDB.SetMaxOpenConns(1)
		var probeID int64
		if err := probeDB.QueryRow("select connection_id()").Scan(&probeID); err != nil {
			tx.Rollback()
			probeDB.Close()
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		type outcome struct {
			committed bool
			err       error
		}
		done := make(chan outcome, 1)
		go func() {
			ok, err := NewRepository(probeDB).verifyReservationCommit(ctx, res, api, 0, 990010)
			done <- outcome{ok, err}
		}()
		waiting := false
		for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
			var count int
			if err := db.QueryRowContext(ctx, "select count(*) from performance_schema.data_lock_waits w join performance_schema.threads t on t.thread_id=w.requesting_thread_id where t.processlist_id=?", probeID).Scan(&count); err != nil {
				tx.Rollback()
				cancel()
				<-done
				probeDB.Close()
				t.Fatal(err)
			} else if count > 0 {
				waiting = true
				break
			}
			select {
			case result := <-done:
				tx.Rollback()
				cancel()
				probeDB.Close()
				t.Fatalf("probe ended without waiting: id=%d result=%+v", res.BillingRecordID, result)
			default:
			}
			time.Sleep(5 * time.Millisecond)
		}
		if !waiting {
			tx.Rollback()
			cancel()
			<-done
			probeDB.Close()
			t.Fatal("verification did not lock/wait for the original transaction")
		}
		if commit {
			err = tx.Commit()
		} else {
			err = tx.Rollback()
		}
		if err != nil {
			cancel()
			probeDB.Close()
			t.Fatal(err)
		}
		result := <-done
		cancel()
		probeDB.Close()
		if result.err != nil || result.committed != commit {
			t.Fatalf("probe guessed transaction outcome: committed=%v err=%v want=%v", result.committed, result.err, commit)
		}
		if commit {
			if err := NewRepository(db).ReleaseBilling(context.Background(), res, 990010, "test cleanup"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Removing a committed log before its outcome probe must never resurrect it.
func TestLogCleanupDuringLostCommitDoesNotReinsert(t *testing.T) {
	for _, paid := range []bool{false, true} {
		t.Run(fmt.Sprint(paid), func(t *testing.T) {
			db, cfg, api := billingOutcomeDB(t)
			res := BillingReservation{ChargeType: "FREE", ChargeAmount: "0.0000"}
			if paid {
				var err error
				res, err = NewRepository(db).ReserveBilling(context.Background(), api, 0, 990010)
				if err != nil {
					t.Fatal(err)
				}
			}
			faultDB, fault := billingFaultDB(t, cfg, false)
			removed := int64(0)
			var cleanupErr error
			fault.AfterCommit = func() error {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				var result sql.Result
				result, cleanupErr = db.ExecContext(ctx, "delete from bp_interface_call_log where user_id=990010")
				if cleanupErr == nil {
					removed, cleanupErr = result.RowsAffected()
				}
				return cleanupErr
			}
			svc := NewService(NewRepository(faultDB), nil, nil, "test-secret")
			defer svc.Close(context.Background())
			result := OpenResult{InterfaceID: 990010, UserID: 990010, APICode: "commit-outcome", RequestMethod: "GET", ResponseStatus: 500, CallLogEligible: true, UserVisible: true}
			result.applyReservation(res)
			result.markNoCharge(res)
			err := svc.createOpenLogAndRelease(context.Background(), result, res, 990010, "upstream failed")
			if cleanupErr != nil || !fault.Triggered() || removed != 1 {
				t.Fatalf("cleanup did not occur after COMMIT: removed=%d err=%v triggered=%v", removed, cleanupErr, fault.Triggered())
			}
			if paid && err != nil {
				t.Fatalf("durable released bill did not recover outcome: %v", err)
			}
			if !paid && !errors.Is(err, dbx.ErrCommitUncertain) {
				t.Fatalf("missing free log must not prove rollback: %v", err)
			}
			var logs, refunds int
			if err := db.QueryRow("select count(*) from bp_interface_call_log where user_id=990010").Scan(&logs); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow("select count(*) from bp_wallet_transaction where user_id=990010 and biz_type='OPEN_CALL_BALANCE_REFUND'").Scan(&refunds); err != nil {
				t.Fatal(err)
			}
			wantRefunds := 0
			if paid {
				wantRefunds = 1
			}
			if logs != 0 || refunds != wantRefunds || billingBalance(t, db) != "10.0000" {
				t.Fatalf("cleanup resurrected logs or repeated money changes: logs=%d refunds=%d", logs, refunds)
			}
			if paid {
				var status string
				var logID int64
				if err := db.QueryRow("select status,call_log_id from bp_billing_record where id=?", res.BillingRecordID).Scan(&status, &logID); err != nil {
					t.Fatal(err)
				}
				if status != "RELEASED" || logID <= 0 {
					t.Fatalf("durable receipt lost: %s %d", status, logID)
				}
			}
		})
	}
}
