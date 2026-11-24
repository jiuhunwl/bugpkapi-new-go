package payment

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

	"bugpk-api/internal/pkg/testdb"
)

// 人工补记最危险的失误是「重复加钱」。这里钉死：这条路径绝不写余额、绝不记流水。
func TestMarkOrderPaidManuallyNeverTouchesBalance(t *testing.T) {
	var statements []string
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return stubTx{}, nil },
		Query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
			switch {
			case strings.Contains(query, "from bp_payment_order where order_no=? for update"):
				return testdb.Rows([]driver.Value{int64(11), int64(875), "EXPIRED", "50.4100", "TRC20", nil}), nil
			case strings.Contains(query, "select created_at from bp_payment_order"):
				return testdb.Rows([]driver.Value{time.Date(2026, 11, 12, 0, 10, 11, 0, time.UTC)}), nil
			case strings.Contains(query, "from bp_wallet_transaction"):
				// 余额确实补过（管理员调整 50.41）
				return testdb.Rows([]driver.Value{int64(1)}), nil
			case strings.Contains(query, "left join bp_user u on u.id=o.user_id"):
				return testdb.Rows(orderRow("BP20261112001011C1C17678B651", "PAID")), nil
			default:
				return nil, fmt.Errorf("unexpected query: %s", query)
			}
		},
		Exec: func(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			statements = append(statements, query)
			return driver.RowsAffected(1), nil
		},
	}))
	if _, err := repo.MarkOrderPaidManually(context.Background(), "BP20261112001011C1C17678B651", "binance-internal-50.41", "ops"); err != nil {
		t.Fatalf("manual mark failed: %v", err)
	}
	if len(statements) != 1 {
		t.Fatalf("expected exactly one write, got %d: %v", len(statements), statements)
	}
	written := statements[0]
	// 唯一允许的写入必须是订单状态更新。
	if !strings.Contains(written, "update bp_payment_order") {
		t.Fatalf("unexpected write target: %s", written)
	}
	// 任何触碰余额/流水的语句都是重复入账，必须零出现。
	for _, forbidden := range []string{"bp_user", "balance", "bp_wallet_transaction", "points"} {
		if strings.Contains(written, forbidden) {
			t.Fatalf("manual mark must not touch %q: %s", forbidden, written)
		}
	}
	if !strings.Contains(written, "status='PAID'") {
		t.Fatalf("status not set to PAID: %s", written)
	}
}

// 没有等额补款记录时，绝不能把订单改成已支付——否则账目上凭空多一笔收入。
func TestMarkOrderPaidManuallyRequiresExistingCredit(t *testing.T) {
	wrote := false
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return stubTx{}, nil },
		Query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
			switch {
			case strings.Contains(query, "from bp_payment_order where order_no=? for update"):
				return testdb.Rows([]driver.Value{int64(11), int64(875), "EXPIRED", "50.4100", "TRC20", nil}), nil
			case strings.Contains(query, "select created_at from bp_payment_order"):
				return testdb.Rows([]driver.Value{time.Date(2026, 11, 12, 0, 10, 11, 0, time.UTC)}), nil
			case strings.Contains(query, "from bp_wallet_transaction"):
				return testdb.Rows([]driver.Value{int64(0)}), nil
			default:
				return nil, fmt.Errorf("unexpected query: %s", query)
			}
		},
		Exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
			wrote = true
			return driver.RowsAffected(1), nil
		},
	}))
	_, err := repo.MarkOrderPaidManually(context.Background(), "BP-1", "ref", "ops")
	if err == nil {
		t.Fatal("must refuse when no matching admin credit exists")
	}
	if !strings.Contains(err.Error(), "拒绝改状态") {
		t.Fatalf("error should explain the refusal: %v", err)
	}
	if wrote {
		t.Fatal("must not write anything when the credit evidence is missing")
	}
}

// 已 PAID 的订单再补记必须幂等，不能覆盖原有 txid。
func TestMarkOrderPaidManuallyIsIdempotent(t *testing.T) {
	wrote := false
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return stubTx{}, nil },
		Query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "from bp_payment_order where order_no=? for update") {
				return testdb.Rows([]driver.Value{int64(11), int64(875), "PAID", "50.4100", "TRC20", "abc123"}), nil
			}
			return testdb.Rows(), nil
		},
		Exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
			wrote = true
			return driver.RowsAffected(1), nil
		},
	}))
	_, _ = repo.MarkOrderPaidManually(context.Background(), "BP-1", "ref", "ops")
	if wrote {
		t.Fatal("already-paid order must not be rewritten")
	}
}

// CREATED/PAYING 说明还在等支付，直接改 PAID 会掩盖真实未付款。
func TestMarkOrderPaidManuallyRejectsPendingOrders(t *testing.T) {
	for _, status := range []string{"CREATED", "PAYING"} {
		repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
			Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) { return stubTx{}, nil },
			Query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
				return testdb.Rows([]driver.Value{int64(11), int64(875), status, "50.4100", "TRC20", nil}), nil
			},
		}))
		if _, err := repo.MarkOrderPaidManually(context.Background(), "BP-1", "ref", "ops"); err == nil {
			t.Fatalf("status %s must be rejected", status)
		}
	}
}

func TestManualReceiptMarkerIsDistinguishableFromChainTxID(t *testing.T) {
	marker := ManualReceiptMarker("binance-internal-50.41")
	if !strings.HasPrefix(marker, trc20ManualReceiptPrefix) {
		t.Fatalf("marker must carry the manual prefix: %s", marker)
	}
	// 人工凭据绝不能长得像链上 txid，否则会被当成真实交易去链上核验。
	if trc20TxIDPattern.MatchString(marker) {
		t.Fatalf("marker must not match the on-chain txid pattern: %s", marker)
	}
	if got := ManualReceiptMarker("   "); got != trc20ManualReceiptPrefix+"UNSPECIFIED" {
		t.Fatalf("empty reference must be explicit: %s", got)
	}
	long := ManualReceiptMarker(strings.Repeat("x", 300))
	if len(long) > 120 {
		t.Fatalf("marker must be bounded: %d", len(long))
	}
}

func TestWalletCreditedSinceAcceptsBothAdminAdjustmentTypes(t *testing.T) {
	var query string
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
			query = q
			if args[1].Value != "50.4100" {
				t.Fatalf("amount arg=%v", args[1].Value)
			}
			return testdb.Rows([]driver.Value{int64(1)}), nil
		},
	}))
	ok, err := repo.WalletCreditedSince(context.Background(), 875, time.Now(), "50.4100")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	// 新旧两条管理员补款路径都必须认：编辑用户（绝对值）与余额调整（增量）。
	if !strings.Contains(query, "biz_type in ('ADMIN_USER_ASSET_ADJUST','ADMIN_BALANCE_ADJUST')") {
		t.Fatalf("credit check must accept both admin adjustment types: %s", query)
	}
	// 充值、购买、兑换等流水不能当作补款证据。
	for _, forbidden := range []string{"'ALIPAY_RECHARGE'", "'REDEEM_CODE'", "'PURCHASE_GLOBAL_PACKAGE'"} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("credit check must not accept %s: %s", forbidden, query)
		}
	}
}

var _ = errors.Is
var _ = mysqldriver.MySQLError{}

// orderRow 造一行 OrderByNo 需要的 26 列数据，避免每个测试重复拼列。
func orderRow(orderNo, status string) []driver.Value {
	return []driver.Value{
		int64(11), orderNo, int64(875), "RECHARGE", "AUTO", "TRC20", "USD",
		"50.0000", "余额充值", int64(0), "50.4100", "0.0000", int64(0), status,
		"", "", "", "", "", "2026-11-12 01:10:11",
		"2026-11-13 13:40:00", "2026-11-12 00:10:11", "anning",
		"MANUAL", "", "MANUAL-binance-internal-50.41",
		"TMgEhEBSmLjgMkv6vQwLyhkCRQXSDieuSK", "50.410000", "MANUAL-binance-internal-50.41", int64(1),
		int64(0),
	}
}
