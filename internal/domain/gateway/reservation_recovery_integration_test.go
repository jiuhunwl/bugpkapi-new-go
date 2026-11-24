package gateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/testdb"
	mysql "github.com/go-sql-driver/mysql"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

var errReservationProbeUnavailable = errors.New("reservation probe connection unavailable")

type reservationProbeFault struct {
	commit          *testdb.CommitFault
	refund          *testdb.CommitFault
	failOnce        atomic.Bool
	failRefundProbe atomic.Bool
	persistent      atomic.Bool
	failures        atomic.Int32
}

func reservationRecoveryFaultDB(t *testing.T, cfg *mysql.Config, before, persistent bool) (*sql.DB, *reservationProbeFault) {
	t.Helper()
	fault := &reservationProbeFault{commit: testdb.NewCommitFault(before)}
	fault.persistent.Store(persistent)
	fault.failOnce.Store(!persistent)
	name := fmt.Sprintf("reservation-recovery-%d", billingFaultSequence.Add(1))
	mysql.RegisterDialContext(name, func(ctx context.Context, address string) (net.Conn, error) {
		if fault.commit.Triggered() && (fault.persistent.Load() || fault.failOnce.CompareAndSwap(true, false)) {
			fault.failures.Add(1)
			return nil, errReservationProbeUnavailable
		}
		if fault.refund != nil && fault.refund.Triggered() && fault.failRefundProbe.CompareAndSwap(true, false) {
			fault.failures.Add(1)
			return nil, errors.New("refund commit probe connection unavailable")
		}
		conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		if fault.commit.Triggered() && fault.refund != nil {
			return fault.refund.Wrap(conn), nil
		}
		return fault.commit.Wrap(conn), nil
	})
	t.Cleanup(func() { mysql.DeregisterDialContext(name) })
	copied := *cfg
	copied.Net = name
	db, err := sql.Open("mysql", copied.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db, fault
}
func seedReservationRecovery(t *testing.T, db *sql.DB, api InterfaceConfig, userID int64, kind string) InterfaceConfig {
	t.Helper()
	if _, err := db.Exec("insert into bp_user(id,username,password_hash,display_name,balance,points,primary_role_id) values(?,?,'unused','Recovery',10.0000,100,2)", userID, fmt.Sprintf("reservation-recovery-%d", userID)); err != nil {
		t.Fatal(err)
	}
	api.PointPrice = 0
	api.Price = "1.2500"
	switch kind {
	case "POINTS":
		api.PointPrice = 20
		api.Price = "0.0000"
	case "GLOBAL_PACKAGE":
		if _, err := db.Exec("insert into bp_user_package_global(user_id,package_id,package_name,daily_limit,qps_limit,day_key,day_used,total_used,start_at,end_at,status) values(?,700,'Recovery',10,4,curdate(),0,0,date_sub(now(),interval 1 day),date_add(now(),interval 7 day),1)", userID); err != nil {
			t.Fatal(err)
		}
	case "INTERFACE_PACKAGE":
		if _, err := db.Exec("insert into bp_user_package_interface(user_id,interface_id,package_id,spec_id,package_name,spec_name,daily_limit,qps_limit,day_key,day_used,total_used,start_at,end_at,status) values(?,?,700,701,'Recovery','Monthly',10,4,curdate(),0,0,date_sub(now(),interval 1 day),date_add(now(),interval 7 day),1)", userID, api.ID); err != nil {
			t.Fatal(err)
		}
	}
	return api
}
func assertReservationRecoveryState(t *testing.T, db *sql.DB, userID int64, kind, status string) {
	t.Helper()
	var balance, storedStatus string
	var points, used, dayUsed int64
	var bills, wallet, refunds int
	queries := []struct {
		q   string
		out []any
	}{
		{"select cast(balance as char),points from bp_user where id=?", []any{&balance, &points}},
		{"select count(*),coalesce(max(status),'') from bp_billing_record where user_id=?", []any{&bills, &storedStatus}},
		{"select count(*) from bp_wallet_transaction where user_id=?", []any{&wallet}},
		{"select count(*) from bp_wallet_transaction where user_id=? and biz_type in ('OPEN_CALL_BALANCE_REFUND','OPEN_CALL_POINTS_REFUND')", []any{&refunds}},
	}
	for _, q := range queries {
		if err := db.QueryRow(q.q, userID).Scan(q.out...); err != nil {
			t.Fatal(err)
		}
	}
	if kind == "GLOBAL_PACKAGE" {
		if err := db.QueryRow("select day_used,total_used from bp_user_package_global where user_id=?", userID).Scan(&dayUsed, &used); err != nil {
			t.Fatal(err)
		}
	}
	if kind == "INTERFACE_PACKAGE" {
		if err := db.QueryRow("select day_used,total_used from bp_user_package_interface where user_id=?", userID).Scan(&dayUsed, &used); err != nil {
			t.Fatal(err)
		}
	}
	wantBalance := "10.0000"
	wantPoints := int64(100)
	wantUsed := int64(0)
	wantBills, wantWallet, wantRefunds := 0, 0, 0
	if status != "" {
		wantBills = 1
		if kind == "BALANCE" || kind == "POINTS" {
			wantWallet = 1
		}
	}
	if status == "RESERVED" {
		switch kind {
		case "BALANCE":
			wantBalance = "8.7500"
		case "POINTS":
			wantPoints = 80
		default:
			wantUsed = 1
		}
	}
	if status == "RELEASED" && (kind == "BALANCE" || kind == "POINTS") {
		wantWallet = 2
		wantRefunds = 1
	}
	if balance != wantBalance || points != wantPoints || used != wantUsed || dayUsed != wantUsed || bills != wantBills || wallet != wantWallet || refunds != wantRefunds || storedStatus != status {
		t.Fatalf("wrong recovery state for %s/%s: balance=%s points=%d used=%d bills=%d status=%s wallet=%d refunds=%d", kind, status, balance, points, used, bills, storedStatus, wallet, refunds)
	}
}

func TestUncertainReservationRetainsCompensationHandle(t *testing.T) {
	db, cfg, base := billingOutcomeDB(t)
	for index, kind := range []string{"BALANCE", "POINTS", "GLOBAL_PACKAGE", "INTERFACE_PACKAGE"} {
		t.Run(kind, func(t *testing.T) {
			userID := int64(992000 + index)
			api := seedReservationRecovery(t, db, base, userID, kind)
			faultDB, fault := reservationRecoveryFaultDB(t, cfg, false, false)
			res, err := NewRepository(faultDB).ReserveBilling(context.Background(), api, 0, userID)
			if !fault.commit.Triggered() || fault.failures.Load() != 1 {
				t.Fatal("commit/probe fault not exercised")
			}
			if !errors.Is(err, dbx.ErrCommitUncertain) || res.BillingRecordID <= 0 || !res.Reserved || res.ChargeType != kind {
				t.Fatalf("uncertain debit lost its compensation handle: res=%+v err=%v", res, err)
			}
			assertReservationRecoveryState(t, db, userID, kind, "RESERVED")
			for i := 0; i < 2; i++ {
				if err := NewRepository(db).ReleaseBilling(context.Background(), res, userID, "uncertain reservation rejected"); err != nil {
					t.Fatal(err)
				}
			}
			assertReservationRecoveryState(t, db, userID, kind, "RELEASED")
		})
	}
}

func TestReserveForOpenCompensatesUncertainCommit(t *testing.T) {
	db, cfg, base := billingOutcomeDB(t)
	userID := int64(992100)
	for _, kind := range []string{"BALANCE", "POINTS", "GLOBAL_PACKAGE", "INTERFACE_PACKAGE"} {
		for _, mode := range []string{"probe once", "cancel original", "persistent outage", "rolled back", "refund ack lost", "refund probe once", "concurrent reconcile"} {
			userID++
			t.Run(kind+"/"+mode, func(t *testing.T) {
				api := seedReservationRecovery(t, db, base, userID, kind)
				faultDB, fault := reservationRecoveryFaultDB(t, cfg, mode == "rolled back", mode == "persistent outage")
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "cancel original" {
					fault.commit.AfterCommit = func() error { cancel(); return nil }
				}

				if mode == "refund ack lost" || mode == "refund probe once" {
					fault.refund = testdb.NewCommitFault(false)
					fault.failRefundProbe.Store(mode == "refund probe once")
				}
				reconcileDone := make(chan error, 1)
				reconcileCtx, stopReconcile := context.WithTimeout(context.Background(), 5*time.Second)
				defer stopReconcile()
				var hookErr error
				if mode == "concurrent reconcile" {
					fault.commit.AfterCommit = func() error {
						_, hookErr = db.ExecContext(reconcileCtx, "update bp_billing_record set created_at=date_sub(now(),interval 10 minute) where user_id=?", userID)
						if hookErr != nil {
							return hookErr
						}
						go func() {
							_, err := NewRepository(db).ReconcileStaleBilling(reconcileCtx, time.Now().Add(-5*time.Minute), 100)
							reconcileDone <- err
						}()
						return nil
					}
				}
				core, logs := observer.New(zap.ErrorLevel)
				svc := NewService(NewRepository(faultDB), nil, nil, "test-secret")
				svc.SetLogger(zap.New(core))
				defer svc.Close(context.Background())
				// Each refund transaction and its detached commit probe have separate
				// deadlines. This immediate-failure fixture is not a 10s total SLA.
				res, err := svc.reserveForOpen(ctx, api, 0, userID)
				if !fault.commit.Triggered() || fault.failures.Load() < 1 {
					t.Fatal("expected actual COMMIT error and unavailable probe")
				}
				if err == nil || !errors.Is(err, dbx.ErrCommitUncertain) || res.BillingRecordID != 0 || res.Reserved {
					t.Fatalf("failed pre-call reservation was admitted: res=%+v err=%v", res, err)
				}
				if mode == "cancel original" && ctx.Err() == nil {
					t.Fatal("originating context was not cancelled")
				}
				if hookErr != nil {
					t.Fatal(hookErr)
				}
				if (mode == "refund ack lost" || mode == "refund probe once") && !fault.refund.Triggered() {
					t.Fatal("refund COMMIT fault not triggered")
				}
				if mode == "concurrent reconcile" {
					if err := <-reconcileDone; err != nil {
						t.Fatal(err)
					}
				}
				if mode == "refund probe once" && fault.failures.Load() != 2 {
					t.Fatal("both reservation and refund probes were not exercised")
				}
				switch mode {
				case "persistent outage":
					assertReservationRecoveryState(t, db, userID, kind, "RESERVED")
					var recordID int64
					if err := db.QueryRow("select id from bp_billing_record where user_id=?", userID).Scan(&recordID); err != nil {
						t.Fatal(err)
					}
					entries := logs.FilterMessage("open gateway reservation recovery failed").All()
					if len(entries) != 1 || entries[0].ContextMap()["billing_record_id"] != recordID {
						t.Fatalf("unrecovered reservation lost diagnostic identity: %+v", entries)
					}
					fault.persistent.Store(false)
					fault.failOnce.Store(false)
					if _, err := db.Exec("update bp_billing_record set created_at=date_sub(now(),interval 10 minute) where id=?", recordID); err != nil {
						t.Fatal(err)
					}
					for attempt, want := range []int{1, 0} {
						got, err := NewRepository(faultDB).ReconcileStaleBilling(context.Background(), time.Now().Add(-5*time.Minute), 100)
						if err != nil || got != want {
							t.Fatalf("reconcile attempt %d: released=%d err=%v want=%d", attempt, got, err, want)
						}
					}
					assertReservationRecoveryState(t, db, userID, kind, "RELEASED")
				case "rolled back":
					assertReservationRecoveryState(t, db, userID, kind, "")
				default:
					assertReservationRecoveryState(t, db, userID, kind, "RELEASED")
				}
			})
		}
	}
}
