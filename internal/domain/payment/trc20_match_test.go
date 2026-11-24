package payment

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"
)

// matchStub 只回答 MatchTrc20Order 需要的两类查询：已绑定同 txid 的 PAID 行，
// 以及候选订单。其余查询一律失败，避免测试悄悄依赖未断言的 SQL。
type matchStub struct {
	paidOrderNo string
	candidates  [][2]driver.Value
	queries     []string
}

func (s *matchStub) db(t *testing.T) *Repository {
	t.Helper()
	return NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			// 占位符与参数数量必须一致：少传一个时间戳曾导致每轮对账都报
			// "expected 5 arguments, got 4"，订单永远匹配不上。
			if strings.Count(query, "?") != len(args) {
				return nil, fmt.Errorf("placeholder/arg mismatch: %d vs %d", strings.Count(query, "?"), len(args))
			}
			s.queries = append(s.queries, query)
			switch {
			case strings.Contains(query, "status='PAID'") && strings.Contains(query, "chain_txid=?"):
				if s.paidOrderNo == "" {
					return testdb.Rows(), nil
				}
				return testdb.Rows([]driver.Value{s.paidOrderNo}), nil
			case strings.Contains(query, "from bp_payment_order") && strings.Contains(query, "chain_address=binary ?"):
				rows := make([][]driver.Value, 0, len(s.candidates))
				for _, item := range s.candidates {
					rows = append(rows, []driver.Value{item[0], item[1]})
				}
				return testdb.Rows(rows...), nil
			default:
				return nil, fmt.Errorf("unexpected query: %s", query)
			}
		},
	}))
}

func matchTransfer() Trc20Transfer {
	return Trc20Transfer{
		TxID:           strings.Repeat("a", 64),
		From:           "T" + strings.Repeat("B", 33),
		To:             "T" + strings.Repeat("A", 33),
		Amount:         "50.410000",
		BlockTimestamp: time.Now().UnixMilli(),
		Confirmations:  trc20DefaultConfirmations,
	}
}

// 历史同额度订单（已 PAID、已绑自己的 txid）不得把唯一可结算的候选污染成歧义。
// 旧实现会把它们一起算进候选，导致之后每一笔同额度到账都永久 ErrTrc20AmbiguousOrder。
func TestMatchTrc20OrderIgnoresAlreadyPaidSameAmountOrders(t *testing.T) {
	stub := &matchStub{candidates: [][2]driver.Value{{"BP-PENDING", int64(7)}}}
	orderNo, err := stub.db(t).MatchTrc20Order(context.Background(), matchTransfer())
	if err != nil || orderNo != "BP-PENDING" {
		t.Fatalf("pending order not matched: orderNo=%q err=%v", orderNo, err)
	}
}

// 同一用户重复下单（旧单过期、新单重生）撞额度时收敛到最旧的一笔，而不是拒绝入账。
func TestMatchTrc20OrderResolvesSameUserDuplicateToOldest(t *testing.T) {
	stub := &matchStub{candidates: [][2]driver.Value{{"BP-OLD", int64(7)}, {"BP-NEW", int64(7)}}}
	orderNo, err := stub.db(t).MatchTrc20Order(context.Background(), matchTransfer())
	if err != nil || orderNo != "BP-OLD" {
		t.Fatalf("same-user duplicate not resolved to oldest: orderNo=%q err=%v", orderNo, err)
	}
}

// 不同用户同额度仍然必须拒绝自动入账：钱该记给谁无法从链上判定。
func TestMatchTrc20OrderKeepsCrossUserAmbiguity(t *testing.T) {
	stub := &matchStub{candidates: [][2]driver.Value{{"BP-A", int64(7)}, {"BP-B", int64(8)}}}
	orderNo, err := stub.db(t).MatchTrc20Order(context.Background(), matchTransfer())
	if !errors.Is(err, ErrTrc20AmbiguousOrder) || orderNo != "" {
		t.Fatalf("cross-user ambiguity must stay manual: orderNo=%q err=%v", orderNo, err)
	}
}

// 该 txid 已经结算过：直接返回原订单，重复观察保持幂等。
func TestMatchTrc20OrderRecoversSettledTxID(t *testing.T) {
	stub := &matchStub{paidOrderNo: "BP-PAID"}
	orderNo, err := stub.db(t).MatchTrc20Order(context.Background(), matchTransfer())
	if err != nil || orderNo != "BP-PAID" {
		t.Fatalf("settled txid not recovered: orderNo=%q err=%v", orderNo, err)
	}
}

func TestPendingTrc20TransfersScopesAddressOnlyWhenGiven(t *testing.T) {
	var queries []string
	var argsSeen [][]driver.NamedValue
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			queries = append(queries, query)
			argsSeen = append(argsSeen, args)
			return testdb.Rows(), nil
		},
	}))
	if _, err := repo.PendingTrc20Transfers(context.Background(), "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PendingTrc20Transfers(context.Background(), "T"+strings.Repeat("A", 33), 5); err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 {
		t.Fatalf("queries=%d", len(queries))
	}
	if strings.Contains(queries[0], "binary t.to_address=binary ?") {
		t.Fatal("empty address must scan every receiving address")
	}
	if !strings.Contains(queries[1], "binary t.to_address=binary ?") {
		t.Fatal("explicit address must scope the scan")
	}
	if len(argsSeen[0]) != 1 || len(argsSeen[1]) != 2 {
		t.Fatalf("argument shape lost: %d/%d", len(argsSeen[0]), len(argsSeen[1]))
	}
}

func TestUnmatchedTrc20TransfersExcludesFreshRows(t *testing.T) {
	var age int64
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			if !strings.Contains(query, "matched_order_no is null") || !strings.Contains(query, "interval ? minute") {
				return nil, fmt.Errorf("unexpected query: %s", query)
			}
			age = args[0].Value.(int64)
			return testdb.Rows([]driver.Value{
				strings.Repeat("c", 64), "sender", "T" + strings.Repeat("A", 33), "50.410000", int64(1794400000000), "2026-11-12 10:00:00",
			}), nil
		},
	}))
	items, err := repo.UnmatchedTrc20Transfers(context.Background(), trc20UnmatchedWarnMinutes, 50)
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%d err=%v", len(items), err)
	}
	if age != int64(trc20UnmatchedWarnMinutes) {
		t.Fatalf("grace window=%d", age)
	}
	if items[0].Amount != "50.410000" || items[0].TxID != strings.Repeat("c", 64) {
		t.Fatalf("receipt projection lost: %+v", items[0])
	}
}

func TestTrc20SnapshotAddressesSkipConfiguredAddress(t *testing.T) {
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			if !strings.Contains(query, "binary chain_address<>binary ?") {
				return nil, fmt.Errorf("snapshot scan must exclude the configured address: %s", query)
			}
			if args[0].Value != "T"+strings.Repeat("A", 33) {
				return nil, fmt.Errorf("configured address not passed first: %v", args)
			}
			return testdb.Rows(
				[]driver.Value{"T" + strings.Repeat("D", 33)},
				[]driver.Value{"not-a-tron-address"},
			), nil
		},
	}))
	addresses, err := repo.Trc20SnapshotAddresses(context.Background(), "T"+strings.Repeat("A", 33), 72, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(addresses) != 1 || addresses[0] != "T"+strings.Repeat("D", 33) {
		t.Fatalf("snapshot addresses=%v", addresses)
	}
}

// 到账悬空超过宽限期必须变成可见错误，而不是静默躺在流水表里。
func TestReconcilerReportsUnmatchedReceipts(t *testing.T) {
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			switch {
			case strings.Contains(query, "from bp_payment_trc20_transfer") && strings.Contains(query, "matched_order_no is null"):
				return testdb.Rows([]driver.Value{
					strings.Repeat("d", 64), "sender", "T" + strings.Repeat("A", 33), "50.410000", int64(1794400000000), "2026-11-12 10:00:00",
				}), nil
			default:
				return nil, fmt.Errorf("unexpected query: %s", query)
			}
		},
	}))
	reconciler := &Trc20Reconciler{service: &Service{repo: repo}}
	err := reconciler.reportUnmatched(context.Background())
	if err == nil {
		t.Fatal("unmatched receipt was not surfaced")
	}
	if !strings.Contains(err.Error(), strings.Repeat("d", 64)) || !strings.Contains(err.Error(), "50.410000") {
		t.Fatalf("alert lost receipt identity: %v", err)
	}
}

func TestReconcilerSilentWhenEverythingIsSettled(t *testing.T) {
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
			return testdb.Rows(), nil
		},
	}))
	reconciler := &Trc20Reconciler{service: &Service{repo: repo}}
	if err := reconciler.reportUnmatched(context.Background()); err != nil {
		t.Fatalf("clean state must stay quiet: %v", err)
	}
}
