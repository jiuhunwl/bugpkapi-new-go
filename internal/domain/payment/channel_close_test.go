package payment

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"bugpk-api/internal/pkg/testdb"
)

type closeBoundaryTx struct{ commits, rollbacks int }

func (t *closeBoundaryTx) Commit() error   { t.commits++; return nil }
func (t *closeBoundaryTx) Rollback() error { t.rollbacks++; return nil }

func TestPaymentCloseDoesNotCommitUnchangedOrRejectedRows(t *testing.T) {
	for _, tc := range []struct {
		name, status, channel string
		missing, wantOK       bool
		wantExec              int
		wantErr               error
	}{
		{name: "paid replay", status: "PAID", channel: ChannelWechat, wantOK: true},
		{name: "closed replay", status: "CLOSED", channel: ChannelWechat, wantOK: true},
		{name: "foreign paid", status: "PAID", channel: ChannelHupijiaoWechat, wantErr: ErrPaymentChannelMismatch},
		{name: "foreign closed", status: "CLOSED", channel: ChannelHupijiaoWechat, wantErr: ErrPaymentChannelMismatch},
		{name: "invalid state", status: "REFUNDED", channel: ChannelWechat},
		{name: "zero updated rows", status: "CREATED", channel: ChannelWechat, wantExec: 1},
		{name: "missing", missing: true, wantErr: sql.ErrNoRows},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := &closeBoundaryTx{}
			execs := 0
			db := testdb.OpenStub(t, testdb.SQLStub{
				Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return tx, nil },
				Query: func(_ context.Context, _ string, args []driver.NamedValue) (driver.Rows, error) {
					if len(args) != 1 || args[0].Value != "close-order" {
						t.Errorf("wrong locked order scope: %v", args)
					}
					if tc.missing {
						return testdb.Rows(), nil
					}
					return testdb.Rows([]driver.Value{tc.status, tc.channel, "RECHARGE", int64(0), int64(0)}), nil
				},
				Exec: func(_ context.Context, _ string, args []driver.NamedValue) (driver.Result, error) {
					execs++
					if len(args) != 3 || args[0].Value != "CLOSED" || args[1].Value != int64(0) || args[2].Value != "close-order" {
						t.Errorf("wrong close write scope: %v", args)
					}
					return driver.RowsAffected(0), nil
				},
			})
			err := NewRepository(db).MarkOrderClosed(context.Background(), "close-order", ChannelWechat)
			if (err == nil) != tc.wantOK || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) {
				t.Errorf("wrong close result: %v", err)
			}
			if tx.commits != 0 || tx.rollbacks != 1 || execs != tc.wantExec {
				t.Errorf("unexpected transaction effects: commit=%d rollback=%d exec=%d", tx.commits, tx.rollbacks, execs)
			}
		})
	}
}
