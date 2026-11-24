package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

func TestDisabledBillingKeepsPricesWithoutReservingAssets(t *testing.T) {
	var api InterfaceConfig
	if err := json.Unmarshal([]byte(`{"ID":3,"APICode":"paid","Price":"2.5000","PointPrice":7,"BillingDisabled":true}`), &api); err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(freeBillingConnector{})
	defer db.Close()
	res, err := NewRepository(db).ReserveBilling(context.Background(), api, 2, 1)
	if err != nil {
		t.Fatalf("disabled billing must not start a transaction or reserve assets: %v", err)
	}
	if res.Reserved || res.BillingRecordID != 0 || res.ChargeType != "NO_CHARGE" || res.QPSLimit != 17 {
		t.Fatalf("unexpected disabled billing reservation: %+v", res)
	}
	if api.Price != "2.5000" || api.PointPrice != 7 {
		t.Fatalf("saved pricing was changed: %+v", api)
	}
}

func TestBillingLogDisabledOmitsSnapshotButKeepsLedger(t *testing.T) {
	api := InterfaceConfig{BillingLogDisabled: true, Price: "2.5000", PointPrice: 7}
	if got := billingSnapshot(api, 2, 1); got != "" {
		t.Fatalf("disabled snapshot persisted: %s", got)
	}
	exec := &billingLogCapture{}
	id, err := createBillingRecordTx(context.Background(), exec, 1, 2, 3, "paid", "BALANCE", "2.5000", 0, 0, "RESERVED", "balance reserved", billingSnapshot(api, 2, 1), false)
	if err != nil || id != 19 {
		t.Fatalf("financial ledger must remain durable: id=%d err=%v", id, err)
	}
	if !strings.Contains(exec.query, "log_visible") || len(exec.args) != 12 || exec.args[11] != 0 {
		t.Fatalf("log switch not captured with reservation: %s %+v", exec.query, exec.args)
	}
	if exec.args[10] != nil {
		t.Fatalf("disabled detailed snapshot must be NULL: %+v", exec.args[10])
	}
	api.BillingLogDisabled = false
	if got := billingSnapshot(api, 2, 1); got == "" {
		t.Fatal("enabled snapshot missing")
	}
}

type billingLogCapture struct {
	query string
	args  []any
}

func (e *billingLogCapture) ExecContext(_ context.Context, q string, args ...any) (sql.Result, error) {
	e.query = q
	e.args = args
	return billingLogResult{}, nil
}

type billingLogResult struct{}

func (billingLogResult) LastInsertId() (int64, error) { return 19, nil }
func (billingLogResult) RowsAffected() (int64, error) { return 1, nil }
