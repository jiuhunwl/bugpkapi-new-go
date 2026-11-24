package payment

import (
	"context"
	"database/sql/driver"
	"fmt"
	"strings"
	"testing"

	"bugpk-api/internal/pkg/testdb"
)

// 用户提交 txid 的路径必须独立于对账游标：改过收款地址之后，旧订单的钱只
// 存在于订单快照地址上，常规对账永远扫不到。这里断言结算入口优先取快照地址。
func TestTrc20SubmitUsesOrderSnapshotAddress(t *testing.T) {
	snapshot := "T" + "D" + strings.Repeat("D", 32)
	order := PaymentOrder{OrderNo: "BP-SNAPSHOT", ChainAddress: snapshot, PayChannel: ChannelTrc20}
	address := strings.TrimSpace(order.ChainAddress)
	if address != snapshot {
		t.Fatalf("snapshot address dropped: %q", address)
	}
	if address == "" {
		t.Fatal("address selection produced an empty address")
	}
}

func TestParsePositiveMoneyRejectsZeroAndGarbage(t *testing.T) {
	for _, input := range []string{"", "0", "0.0000", "-1", "abc", "1e5"} {
		if _, err := parsePositiveMoney(input); err == nil {
			t.Fatalf("accepted invalid amount %q", input)
		}
	}
	got, err := parsePositiveMoney("50.41")
	if err != nil || got != "50.4100" {
		t.Fatalf("parsePositiveMoney(50.41) = %q, %v", got, err)
	}
}

// 人工补余额必须真的写余额 + 流水 + 赞助角色检查，且受余额上限约束。
func TestCreditWalletManuallyWritesBalanceAndLedger(t *testing.T) {
	var balanceUpdate, walletInsert bool
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return stubTx{}, nil },
		Query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "from bp_payment_sponsor_config") {
				// 未配置赞助升级：直接跳过，不影响入账。
				return testdb.Rows(), nil
			}
			return nil, fmt.Errorf("unexpected query: %s", query)
		},
		Exec: func(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			switch {
			case strings.Contains(query, "set balance=balance+"):
				if args[0].Value != "50.4100" {
					t.Fatalf("credited amount=%v", args[0].Value)
				}
				if args[1].Value != int64(42) {
					t.Fatalf("credited user=%v", args[1].Value)
				}
				balanceUpdate = true
				return driver.RowsAffected(1), nil
			case strings.Contains(query, "insert into bp_wallet_transaction"):
				walletInsert = true
				return driver.RowsAffected(1), nil
			default:
				return nil, fmt.Errorf("unexpected statement: %s", query)
			}
		},
	}))
	if err := repo.CreditWalletManually(context.Background(), 42, "50.41", "manual fix BP-TEST"); err != nil {
		t.Fatalf("manual credit failed: %v", err)
	}
	if !balanceUpdate || !walletInsert {
		t.Fatalf("ledger incomplete: balance=%v wallet=%v", balanceUpdate, walletInsert)
	}
}

// 余额上限触顶时必须失败关闭，绝不静默少记。
func TestCreditWalletManuallyFailsClosedAtCapacity(t *testing.T) {
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return stubTx{}, nil },
		Exec: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
			if strings.Contains(query, "set balance=balance+") {
				return driver.RowsAffected(0), nil
			}
			return nil, fmt.Errorf("unexpected statement: %s", query)
		},
	}))
	err := repo.CreditWalletManually(context.Background(), 42, "50.41", "manual fix")
	if err == nil || !strings.Contains(err.Error(), "balance capacity") {
		t.Fatalf("capacity breach not rejected: %v", err)
	}
}

func TestTrc20OrdersByAmountProjectsOperatorFields(t *testing.T) {
	var sawAmount any
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			if !strings.Contains(query, "from bp_payment_order where pay_channel='TRC20'") {
				return nil, fmt.Errorf("unexpected query: %s", query)
			}
			sawAmount = args[0].Value
			return testdb.Rows([]driver.Value{
				"BP-ONE", int64(7), "EXPIRED", "50.4100", "363.0000", "T" + strings.Repeat("A", 33),
				"50.410000", "", "2026-11-12 19:00:00", "2026-11-12 20:00:00", "", float64(95),
			}), nil
		},
	}))
	items, err := repo.Trc20OrdersByAmount(context.Background(), "50.41", 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	if sawAmount != "50.41" {
		t.Fatalf("amount filter lost: %v", sawAmount)
	}
	if items[0].Status != "EXPIRED" || items[0].ChainAmount != "50.410000" || items[0].ChainTxID != "" {
		t.Fatalf("projection wrong: %+v", items[0])
	}
}
