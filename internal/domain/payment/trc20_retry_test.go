package payment

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
)

type trc20ChainFixture struct {
	transfers []Trc20Transfer
	calls     int
}

func (c *trc20ChainFixture) ListTransfers(context.Context, string, string, string, int64) ([]Trc20Transfer, error) {
	c.calls++
	return c.transfers, nil
}

func TestTrc20SettlementFailureIsRetried(t *testing.T) {
	const address = "TAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	const txid = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	transfer := Trc20Transfer{TxID: txid, From: "TBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB", To: address, Amount: "5.010000", BlockTimestamp: time.Now().UnixMilli(), Confirmations: 1}
	seen := false
	attempts := 0
	db := testdb.OpenStub(t, testdb.SQLStub{
		Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) {
			attempts++
			return nil, errors.New("temporary settlement failure")
		},
		Query: func(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
			switch {
			case strings.Contains(q, "from bp_payment_trc20_config"):
				return testdb.Rows([]driver.Value{1, "TRON", address, "", trc20USDTContract, "5.000000", "10000.000000", 1, int64(0), int64(0)}), nil
			case strings.Contains(q, "select exists") && strings.Contains(q, "bp_payment_trc20_transfer"):
				processed := seen && !strings.Contains(q, "matched_order_no is not null")
				value := int64(0)
				if processed {
					value = 1
				}
				return testdb.Rows([]driver.Value{value}), nil
			case strings.Contains(q, "from bp_payment_trc20_transfer"):
				return testdb.Rows(), nil
			case strings.Contains(q, "chain_txid=?") && strings.Contains(q, "status='PAID'"):
				return testdb.Rows(), nil
			case strings.Contains(q, "from bp_payment_order"):
				// 候选查询只取 order_no 与 user_id 两列。
				return testdb.Rows([]driver.Value{"BP-fixture", int64(7)}), nil
			default:
				return nil, errors.New("unexpected fixture query: " + q)
			}
		},
		Exec: func(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
			if strings.Contains(q, "into bp_payment_trc20_transfer") {
				seen = true
				return driver.RowsAffected(1), nil
			}
			if strings.Contains(q, "cursor_timestamp_ms") || strings.Contains(q, "retry_cursor_id") {
				return driver.RowsAffected(1), nil
			}
			return nil, errors.New("unexpected fixture statement: " + q)
		},
	})
	reconciler := NewTrc20Reconciler(&Service{repo: NewRepository(db)})
	reconciler.chain = &trc20ChainFixture{transfers: []Trc20Transfer{transfer}}
	for i := 0; i < 2; i++ {
		if err := reconciler.ReconcileOnce(context.Background()); err == nil {
			t.Errorf("attempt %d concealed a settlement failure", i+1)
		}
	}
	if attempts != 2 {
		t.Fatalf("settlement attempts=%d, want 2; an observed transfer must remain retryable", attempts)
	}
}

func TestTrc20RejectsUnsupportedConfirmationDepth(t *testing.T) {
	for _, depth := range []int{2, 3, 20} {
		if _, _, err := normalizeTrc20ConfigRequest(SaveTrc20ConfigRequest{Confirmations: depth}); err == nil {
			t.Errorf("accepted unsupported confirmation depth %d", depth)
		}
	}
}

func TestTrc20CancelledRetryCheckpointsOnlyAttemptedReceipt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const address = "TAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	attempts := 0
	checkpoint := int64(0)
	db := testdb.OpenStub(t, testdb.SQLStub{
		Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) {
			attempts++
			cancel()
			return nil, context.Canceled
		},
		Query: func(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
			switch {
			case strings.Contains(q, "from bp_payment_trc20_config"):
				return testdb.Rows([]driver.Value{1, "TRON", address, "", trc20USDTContract, "5.000000", "10000.000000", 1, int64(0), int64(0)}), nil
			case strings.Contains(q, "select exists"):
				return testdb.Rows([]driver.Value{0}), nil
			case strings.Contains(q, "from bp_payment_trc20_transfer"):
				return testdb.Rows([]driver.Value{int64(7), strings.Repeat("a", 64), "sender", address, "5.010000", time.Now().UnixMilli()}, []driver.Value{int64(8), strings.Repeat("b", 64), "sender", address, "5.020000", time.Now().UnixMilli()}), nil
			case strings.Contains(q, "chain_txid=?") && strings.Contains(q, "status='PAID'"):
				return testdb.Rows(), nil
			case strings.Contains(q, "from bp_payment_order"):
				// 候选查询只取 order_no 与 user_id 两列。
				return testdb.Rows([]driver.Value{"BP-fixture", int64(7)}), nil
			default:
				return nil, errors.New("unexpected fixture query")
			}
		},
		Exec: func(execCtx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
			if strings.Contains(q, "retry_cursor_id") {
				if err := execCtx.Err(); err != nil {
					return nil, err
				}
				checkpoint = args[0].Value.(int64)
				return driver.RowsAffected(1), nil
			}
			return nil, errors.New("unexpected fixture write")
		},
	})
	chain := &trc20ChainFixture{}
	reconciler := NewTrc20Reconciler(&Service{repo: NewRepository(db)})
	reconciler.chain = chain
	if err := reconciler.ReconcileOnce(ctx); err == nil {
		t.Fatal("cancelled retry concealed")
	}
	if attempts != 1 || checkpoint != 7 || chain.calls != 0 {
		t.Fatalf("attempts=%d checkpoint=%d chainCalls=%d; want 1/7/0", attempts, checkpoint, chain.calls)
	}
}
