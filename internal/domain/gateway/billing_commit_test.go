package gateway

import (
	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/testdb"
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"
)

type uncertainLogTx struct{}

func (uncertainLogTx) Commit() error   { return errors.New("commit result lost") }
func (uncertainLogTx) Rollback() error { return nil }

type outcomeLogResult struct{}

func (outcomeLogResult) LastInsertId() (int64, error) { return 7, nil }
func (outcomeLogResult) RowsAffected() (int64, error) { return 1, nil }
func TestUncertainLogCommitDoesNotTriggerBlindInsertRetries(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			inserts := 0
			db := testdb.OpenStub(t, testdb.SQLStub{Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return uncertainLogTx{}, nil }, Exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
				inserts++
				return outcomeLogResult{}, nil
			}, Query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
				if missing {
					return testdb.Rows(), nil
				}
				return nil, errors.New("receipt verification unavailable")
			}})
			service := NewService(NewRepository(db), nil, nil, "test-secret")
			defer service.Close(context.Background())
			result := OpenResult{InterfaceID: 1, UserID: 1, RequestMethod: "GET", ResponseStatus: 500, CallLogEligible: true, ChargeType: "FREE", ChargeAmount: "0.0000", BillingStatus: "NONE"}
			err := service.createOpenLogAndRelease(context.Background(), result, BillingReservation{ChargeType: "FREE"}, 1, "upstream failed")
			if !errors.Is(err, dbx.ErrCommitUncertain) || inserts != 1 {
				t.Fatalf("unknown outcome was retried as a new insert: err=%v inserts=%d", err, inserts)
			}
		})
	}
}
