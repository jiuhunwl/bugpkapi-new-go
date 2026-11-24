package payment

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/testdb"
)

func TestContinueExpiredTrc20OrderForReceiptVerification(t *testing.T) {
	for _, tc := range []struct {
		name, initial, locked, want string
	}{
		{"already expired", "EXPIRED", "EXPIRED", "EXPIRED"},
		{"expires when opened", "CREATED", "CREATED", "EXPIRED"},
		{"payment wins expiry race", "PAYING", "PAID", "PAID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.initial
			writes := 0
			db := testdb.OpenStub(t, testdb.SQLStub{
				Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return stubTx{}, nil },
				Query: func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
					switch {
					case strings.Contains(query, "left join bp_user u"):
						if len(args) != 2 || args[0].Value != "BP-TRC20" || args[1].Value != int64(7) {
							t.Fatalf("order ownership scope lost: %v", args)
						}
						row := stubOrderRow("BP-TRC20", status, ChannelTrc20)
						row[len(row)-1] = int64(0)
						return testdb.Rows(row), nil
					case strings.Contains(query, "select if(expire_at<=now()"):
						return testdb.Rows([]driver.Value{int64(1)}), nil
					case strings.Contains(query, "for update"):
						status = tc.locked
						return testdb.Rows([]driver.Value{status, ChannelTrc20, "RECHARGE", int64(0), int64(0)}), nil
					default:
						return nil, fmt.Errorf("unexpected query: %s", query)
					}
				},
				Exec: func(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
					if !strings.Contains(query, "set status=?") || args[0].Value != "EXPIRED" {
						return nil, fmt.Errorf("must not extend or recreate order: %s", query)
					}
					status = "EXPIRED"
					writes++
					return driver.RowsAffected(1), nil
				},
			})
			s := &Service{repo: NewRepository(db)}
			order, err := s.ContinueOrder(t.Context(), Principal{ID: 7}, "BP-TRC20")
			if err != nil || order.Status != tc.want || order.RemainingSecs != 0 {
				t.Fatalf("receipt verification must reopen original order: status=%s remaining=%d err=%v", order.Status, order.RemainingSecs, err)
			}
			wantWrites := 0
			if tc.locked == "CREATED" {
				wantWrites = 1
			}
			if writes != wantWrites {
				t.Fatalf("writes=%d, want %d", writes, wantWrites)
			}
		})
	}
}

func TestContinueOrderDoesNotReopenOtherEndedOrders(t *testing.T) {
	for _, tc := range []struct{ channel, status string }{
		{ChannelAlipay, "EXPIRED"}, {ChannelWechat, "EXPIRED"},
		{ChannelTrc20, "CLOSED"}, {ChannelTrc20, "FAILED"}, {ChannelTrc20, "CANCELLED"},
	} {
		t.Run(tc.channel+"/"+tc.status, func(t *testing.T) {
			db := testdb.OpenStub(t, testdb.SQLStub{
				Query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
					return testdb.Rows(stubOrderRow("BP-ended", tc.status, tc.channel)), nil
				},
			})
			s := &Service{repo: NewRepository(db)}
			_, err := s.ContinueOrder(t.Context(), Principal{ID: 7}, "BP-ended")
			business, ok := apperrors.AsBusinessError(err)
			if !ok || business.StatusCode != 409 {
				t.Fatalf("ended order must stay closed: %v", err)
			}
		})
	}
}

func TestSyncExpiredTrc20OrderSeesLateSettlement(t *testing.T) {
	reads := 0
	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			if !strings.Contains(query, "left join bp_user u") || len(args) != 2 || args[1].Value != int64(7) {
				return nil, fmt.Errorf("unexpected query or scope: %s %v", query, args)
			}
			reads++
			status := "EXPIRED"
			if reads > 1 {
				status = "PAID"
			}
			return testdb.Rows(stubOrderRow("BP-late", status, ChannelTrc20)), nil
		},
	})
	s := &Service{repo: NewRepository(db)}
	order, err := s.SyncUserOrder(t.Context(), Principal{ID: 7}, "BP-late")
	if err != nil || order.Status != "PAID" {
		t.Fatalf("late settlement must stay visible: %+v %v", order, err)
	}
}
