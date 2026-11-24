package payment

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/testdb"
)

type failedPaymentCommit struct{ err error }

func (t failedPaymentCommit) Commit() error { return t.err }
func (failedPaymentCommit) Rollback() error { return nil }

func TestPaymentCommitWitnessRequiresPaidMatchingIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, status, alipay, other, buyer, provider string
		channel, wantOK, wantUnknown                 bool
		queryErr                                     error
		missing                                      bool
	}{
		{name: "alipay matched", status: "PAID", alipay: "trade", buyer: "buyer", wantOK: true},
		{name: "channel matched", status: "PAID", other: "trade", buyer: "buyer", channel: true, wantOK: true},
		{name: "foreign provider same trade column", status: "PAID", other: "trade", buyer: "buyer", channel: true, provider: ChannelHupijiaoWechat, wantUnknown: true},
		{name: "uncommitted", status: "CREATED", alipay: "trade", buyer: "buyer"},
		{name: "closed", status: "CLOSED", alipay: "trade", buyer: "buyer"},
		{name: "different trade", status: "PAID", alipay: "other", buyer: "buyer"},
		{name: "different buyer", status: "PAID", alipay: "trade", buyer: "other"},
		{name: "wrong trade column", status: "PAID", other: "trade", buyer: "buyer", wantUnknown: true},
		{name: "wrong channel column", status: "PAID", alipay: "trade", buyer: "buyer", channel: true, wantUnknown: true},
		{name: "query unavailable", queryErr: errors.New("probe unavailable"), wantUnknown: true},
		{name: "deleted order", missing: true, wantUnknown: true},
		{name: "missing paid alipay trade", status: "PAID", wantUnknown: true},
		{name: "missing paid channel trade", status: "PAID", channel: true, wantUnknown: true},
		{name: "blank paid trade", status: "PAID", alipay: "  ", wantUnknown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expected := ChannelAlipay
			if tc.channel {
				expected = ChannelWechat
			}
			storedChannel := expected
			if tc.provider != "" {
				storedChannel = tc.provider
			}
			lostACK := errors.New("lost commit ACK")
			begins := 0
			db := testdb.OpenStub(t, testdb.SQLStub{
				Begin: func(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
					begins++
					if begins == 2 && (opts.ReadOnly || opts.Isolation != driver.IsolationLevel(sql.LevelReadCommitted)) {
						t.Error("probe must use locking RC transaction")
					}
					return failedPaymentCommit{lostACK}, nil
				},
				Query: func(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
					if ctx.Err() != nil {
						t.Error("probe inherited cancellation")
					}
					if _, ok := ctx.Deadline(); !ok {
						t.Error("probe must be bounded")
					}
					if len(args) != 1 || args[0].Value != int64(7) {
						t.Fatalf("wrong order witness: %v", args)
					}
					if tc.queryErr != nil {
						return nil, tc.queryErr
					}
					if tc.missing {
						return testdb.Rows(), nil
					}
					return testdb.Rows([]driver.Value{tc.status, tc.alipay, tc.other, tc.buyer, storedChannel}), nil
				},
			})
			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			err = NewRepository(db).commitPaidOrder(ctx, tx, 7, "trade", "buyer", expected)
			if (err == nil) != tc.wantOK || errors.Is(err, dbx.ErrCommitUncertain) != tc.wantUnknown {
				t.Fatalf("wrong outcome: %v", err)
			}
			if !tc.wantOK && !errors.Is(err, lostACK) {
				t.Fatal("lost original commit error")
			}
		})
	}
}
