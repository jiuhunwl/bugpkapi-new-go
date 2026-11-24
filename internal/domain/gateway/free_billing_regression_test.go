package gateway

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"testing"
)

// The driver refuses transactions and writes: free calls may only read role QPS.
func TestFreeInterfaceDoesNotCreateBillingReservation(t *testing.T) {
	db := sql.OpenDB(freeBillingConnector{})
	defer db.Close()
	repo := NewRepository(db)
	reservation, err := repo.ReserveBilling(context.Background(), InterfaceConfig{ID: 3, APICode: "free", Price: "0.0000"}, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.Reserved || reservation.BillingRecordID != 0 || reservation.ChargeType != "FREE" || reservation.ChargeAmount != "0.0000" || reservation.QPSLimit != 17 {
		t.Fatalf("free call must preserve role QPS without a billing record: %+v", reservation)
	}
	if err := repo.ConfirmBilling(context.Background(), reservation, 0); err != nil {
		t.Fatal(err)
	}
	if err := repo.ReleaseBilling(context.Background(), reservation, 1, "upstream failed"); err != nil {
		t.Fatal(err)
	}
	result := OpenResult{CallLogEligible: true, Success: true}
	result.applyReservation(reservation)
	if result.BillingStatus != "NONE" || result.Billable || !shouldRecordCallLog(result) {
		t.Fatalf("free call must remain a call log, not a billable call: %+v", result)
	}
}

func TestFreeCallLogsAreStillWrittenOnSuccessAndFailure(t *testing.T) {
	db := sql.OpenDB(freeCallLogConnector{})
	defer db.Close()
	repo := NewRepository(db)
	reservation := BillingReservation{ChargeType: "FREE", ChargeAmount: "0.0000"}
	result := OpenResult{CallLogEligible: true, Success: true}
	result.applyReservation(reservation)
	if _, err := repo.CreateOpenLogAndConfirm(context.Background(), result, reservation); err != nil {
		t.Fatal(err)
	}
	result.Success = false
	result.markNoCharge(reservation)
	if result.BillingStatus != "NONE" || result.Billable || result.ChargeType != "FREE" {
		t.Fatalf("free failure metadata = %+v", result)
	}
	if _, err := repo.CreateOpenLogAndRelease(context.Background(), result, reservation, "failed"); err != nil {
		t.Fatal(err)
	}
}

func TestPricedInterfacesStillRequireBillingTransaction(t *testing.T) {
	for _, api := range []InterfaceConfig{{Price: "1.0000"}, {Price: "0.0000", PointPrice: 1}} {
		db := sql.OpenDB(freeBillingConnector{})
		_, err := NewRepository(db).ReserveBilling(context.Background(), api, 2, 1)
		db.Close()
		if err == nil || !strings.Contains(err.Error(), "billing transaction attempted") {
			t.Fatalf("paid API bypassed reservation: %v", err)
		}
	}
}

type freeBillingConnector struct{}

func (freeBillingConnector) Connect(context.Context) (driver.Conn, error) {
	return freeBillingConn{}, nil
}
func (freeBillingConnector) Driver() driver.Driver { return freeBillingDriver{} }

type freeBillingDriver struct{}

func (freeBillingDriver) Open(string) (driver.Conn, error) { return freeBillingConn{}, nil }

type freeBillingConn struct{}

func (freeBillingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected SQL write/prepare")
}
func (freeBillingConn) Close() error { return nil }
func (freeBillingConn) Begin() (driver.Tx, error) {
	return nil, errors.New("billing transaction attempted")
}
func (freeBillingConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.Contains(query, "primary_role.qps_limit") {
		return nil, errors.New("unexpected free billing query")
	}
	return &freeBillingRows{}, nil
}

type freeBillingRows struct{ done bool }

func (*freeBillingRows) Columns() []string { return []string{"qps_limit"} }
func (*freeBillingRows) Close() error      { return nil }
func (r *freeBillingRows) Next(values []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	values[0] = int64(17)
	return nil
}

func (freeBillingConn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	if !strings.Contains(query, "insert into bp_interface_call_log(") {
		return nil, errors.New("unexpected billing/wallet write")
	}
	return freeCallLogInsert{}, nil
}

type freeCallLogInsert struct{}

func (freeCallLogInsert) LastInsertId() (int64, error) { return 7, nil }
func (freeCallLogInsert) RowsAffected() (int64, error) { return 1, nil }

// Logging may use a transaction to resolve an uncertain COMMIT, but the free
// path still cannot write any billing or wallet rows (ExecContext enforces it).
type freeCallLogConnector struct{ freeBillingConnector }

func (freeCallLogConnector) Connect(context.Context) (driver.Conn, error) {
	return freeCallLogConn{}, nil
}

type freeCallLogConn struct{ freeBillingConn }

func (freeCallLogConn) Begin() (driver.Tx, error) { return freeCallLogTx{}, nil }

type freeCallLogTx struct{}

func (freeCallLogTx) Commit() error   { return nil }
func (freeCallLogTx) Rollback() error { return nil }
