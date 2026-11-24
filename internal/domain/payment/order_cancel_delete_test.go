package payment

import (
	"context"
	"database/sql/driver"
	"strings"
	"testing"

	"bugpk-api/internal/pkg/testdb"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type stubTx struct{}

func (stubTx) Commit() error   { return nil }
func (stubTx) Rollback() error { return nil }

// stubOrderRow 按 OrderByNo 的投影列顺序造一行订单（31 列）。
func stubOrderRow(orderNo, status, channel string) []driver.Value {
	return []driver.Value{
		int64(11), orderNo, int64(7), "RECHARGE", "PAGE", channel, CurrencyCNY, "", "余额充值",
		int64(0), "50.0000", "0.0000", int64(0), status,
		"", "", "", "https://merchant.example/pay", "weixin://wxpay/bizpayurl?pr=abc",
		"2026-11-05 10:20:00", "", "2026-11-05 10:05:00", "tester",
		"", "", "", "", "", "", int64(0), int64(600),
	}
}

func TestCancelOrderMarksPayableOrdersAndLeavesTheRest(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      string
		channel     string
		wantWrite   bool
		wantIDOrder bool
		wantMessage string
	}{
		{name: "paying order is cancelled", status: "PAYING", channel: ChannelHupijiaoWechat, wantWrite: true},
		{name: "created order is cancelled", status: "CREATED", channel: ChannelAlipay, wantWrite: true},
		{name: "cancelled order stays unchanged", status: "CANCELLED", channel: ChannelAlipay, wantIDOrder: true},
		{name: "paid order is never cancelled", status: "PAID", channel: ChannelAlipay, wantMessage: "订单已支付，无法取消"},
		{name: "expired order is already ended", status: "EXPIRED", channel: ChannelAlipay, wantMessage: "订单已结束，无需取消"},
		{name: "trc20 waits for expiry", status: "PAYING", channel: ChannelTrc20, wantMessage: "USDT 充值订单不支持取消，请等待过期或完成转账"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes := 0
			db := testdb.OpenStub(t, testdb.SQLStub{
				Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return stubTx{}, nil },
				Query: func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
					switch {
					case strings.Contains(query, "left join bp_user u"):
						return testdb.Rows(stubOrderRow("BP1", tc.status, tc.channel)), nil
					case strings.Contains(query, "for update"):
						if len(args) != 1 || args[0].Value != "BP1" {
							t.Errorf("locked read scope = %v", args)
						}
						return testdb.Rows([]driver.Value{tc.status, tc.channel, "RECHARGE", int64(0), int64(1)}), nil
					default:
						t.Errorf("unexpected query %q", query)
						return testdb.Rows(), nil
					}
				},
				Exec: func(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
					writes++
					if !strings.Contains(query, "set status=?") || args[0].Value != "CANCELLED" {
						t.Errorf("cancel write = %q %v", query, args)
					}
					// 限量套餐占用随取消释放：stock_reserved 1 -> 2。
					if args[1].Value != int64(2) {
						t.Errorf("stock_reserved = %v, want released 2", args[1].Value)
					}
					return driver.RowsAffected(1), nil
				},
			})
			s := &Service{repo: NewRepository(db)}
			order, err := s.CancelOrder(context.Background(), Principal{ID: 7}, "BP1")
			switch {
			case tc.wantWrite:
				if err != nil {
					t.Fatalf("CancelOrder() error = %v", err)
				}
				if writes != 1 {
					t.Fatalf("cancel writes = %d, want 1", writes)
				}
			case tc.wantIDOrder:
				if err != nil || order.Status != "CANCELLED" || writes != 0 {
					t.Fatalf("repeated cancel must succeed without writing, got %+v writes=%d err=%v", order, writes, err)
				}
			default:
				if writes != 0 {
					t.Fatalf("status %s must not be written, writes = %d", tc.status, writes)
				}
				business, ok := apperrors.AsBusinessError(err)
				if !ok || business.StatusCode != consts.StatusConflict || business.Message != tc.wantMessage {
					t.Fatalf("CancelOrder() error = %+v, want 409 %q", err, tc.wantMessage)
				}
			}
		})
	}
}

// 删除只做软隐藏：状态白名单写死在 SQL 里，用户端还必须锁死归属。
func TestSoftDeleteOrderScope(t *testing.T) {
	for _, tc := range []struct {
		name  string
		admin bool
	}{
		{name: "user scope"},
		{name: "admin scope", admin: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotSQL string
			var gotArgs []driver.NamedValue
			db := testdb.OpenStub(t, testdb.SQLStub{
				Exec: func(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
					gotSQL, gotArgs = query, args
					return driver.RowsAffected(1), nil
				},
			})
			deleted, err := NewRepository(db).SoftDeleteOrder(context.Background(), "BP1", 7, tc.admin)
			if err != nil || !deleted {
				t.Fatalf("SoftDeleteOrder() = %v/%v", deleted, err)
			}
			if !strings.Contains(gotSQL, "set deleted_at=now()") || !strings.Contains(gotSQL, "deleted_at is null") {
				t.Fatalf("delete must stay a soft hide, got %q", gotSQL)
			}
			for _, status := range []string{"EXPIRED", "CLOSED", "CANCELLED", "FAILED"} {
				if !strings.Contains(gotSQL, "'"+status+"'") {
					t.Fatalf("status %s must be deletable, got %q", status, gotSQL)
				}
			}
			if strings.Contains(gotSQL, "'PAID'") || strings.Contains(gotSQL, "'PAYING'") {
				t.Fatalf("payable or paid statuses must not be deletable, got %q", gotSQL)
			}
			if tc.admin {
				if len(gotArgs) != 1 {
					t.Fatalf("admin args = %v, want only the order number", gotArgs)
				}
			} else if len(gotArgs) != 2 || gotArgs[1].Value != int64(7) {
				t.Fatalf("user args = %v, want order number plus user 7", gotArgs)
			}
		})
	}
}

func TestDeleteUserOrderRejectsLivePaidAndRacedOrders(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      string
		affected    int64
		wantMessage string
	}{
		{name: "paid", status: "PAID", wantMessage: "只有已结束的订单可以删除"},
		{name: "paying", status: "PAYING", wantMessage: "只有已结束的订单可以删除"},
		{name: "settled between read and delete", status: "EXPIRED", wantMessage: "订单状态已变化，请刷新后重试"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			affected := int64(1)
			if tc.status == "EXPIRED" {
				affected = 0
			}
			var writes int
			db := testdb.OpenStub(t, testdb.SQLStub{
				Query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
					if !strings.Contains(query, "left join bp_user u") {
						t.Errorf("unexpected query %q", query)
					}
					return testdb.Rows(stubOrderRow("BP1", tc.status, ChannelAlipay)), nil
				},
				Exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
					writes++
					return driver.RowsAffected(affected), nil
				},
			})
			s := &Service{repo: NewRepository(db)}
			deleted, err := s.DeleteUserOrder(context.Background(), Principal{ID: 7}, "BP1")
			if deleted {
				t.Fatal("delete must not report success")
			}
			business, ok := apperrors.AsBusinessError(err)
			if !ok || business.StatusCode != consts.StatusConflict || business.Message != tc.wantMessage {
				t.Fatalf("DeleteUserOrder() error = %+v, want 409 %q", err, tc.wantMessage)
			}
			if tc.status != "EXPIRED" && writes != 0 {
				t.Fatalf("status %s must not reach the delete write, writes = %d", tc.status, writes)
			}
		})
	}
}

func TestDeleteUserOrderHidesEndedOrder(t *testing.T) {
	var execs int
	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, _ string, _ []driver.NamedValue) (driver.Rows, error) {
			return testdb.Rows(stubOrderRow("BP1", "CANCELLED", ChannelAlipay)), nil
		},
		Exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
			execs++
			return driver.RowsAffected(1), nil
		},
	})
	s := &Service{repo: NewRepository(db)}
	deleted, err := s.DeleteUserOrder(context.Background(), Principal{ID: 7}, "BP1")
	if err != nil || !deleted {
		t.Fatalf("DeleteUserOrder() = %v/%v", deleted, err)
	}
	if execs != 1 {
		t.Fatalf("delete writes = %d, want 1", execs)
	}
}

// 软隐藏只作用于列表：详情/结算按订单号读取仍能看到并补结算，钱不会丢。
func TestPaymentOrderListHidesSoftDeletedRows(t *testing.T) {
	where, args := paymentOrderWhere(ListParams{Keyword: "BP1"}, 7, false)
	if !strings.Contains(where, "o.deleted_at is null") {
		t.Fatalf("user list must hide deleted orders, got %q", where)
	}
	if !strings.Contains(where, "o.user_id=?") {
		t.Fatalf("user list must stay scoped, got %q", where)
	}
	if len(args) != 6 {
		t.Fatalf("user args = %v, want user id plus five keyword matches", args)
	}
	adminWhere, adminArgs := paymentOrderWhere(ListParams{}, 0, true)
	if !strings.Contains(adminWhere, "o.deleted_at is null") {
		t.Fatalf("admin list must hide deleted orders too, got %q", adminWhere)
	}
	if len(adminArgs) != 0 {
		t.Fatalf("admin args = %v", adminArgs)
	}
}

func TestOrderActionStatusRules(t *testing.T) {
	for _, status := range []string{"EXPIRED", "CLOSED", "CANCELLED", "FAILED"} {
		if !isDeletableOrderStatus(status) {
			t.Fatalf("status %s must be deletable", status)
		}
	}
	for _, status := range []string{"CREATED", "PAYING", "PAID", ""} {
		if isDeletableOrderStatus(status) {
			t.Fatalf("status %s must not be deletable", status)
		}
	}
}
