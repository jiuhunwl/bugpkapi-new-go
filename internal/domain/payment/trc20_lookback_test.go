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

type trc20LookbackChain struct {
	transfers []Trc20Transfer
	lowers    []int64
	pageLimit int
	failLower int64
}

func (c *trc20LookbackChain) ListTransfers(_ context.Context, _, _, _ string, lower int64) ([]Trc20Transfer, error) {
	c.lowers = append(c.lowers, lower)
	if c.failLower != 0 && lower < c.failLower {
		return nil, errors.New("temporary provider failure")
	}
	var result []Trc20Transfer
	for _, transfer := range c.transfers {
		if transfer.BlockTimestamp >= lower {
			result = append(result, transfer)
			if c.pageLimit > 0 && len(result) == c.pageLimit {
				break
			}
		}
	}
	return result, nil
}

type trc20LookbackFixture struct {
	cursor     int64
	address    string
	recorded   map[string]bool
	attempts   map[string]int
	failRecord string
}

func newTrc20LookbackFixture(t *testing.T, cursor int64) (*Trc20Reconciler, *trc20LookbackFixture) {
	t.Helper()
	f := &trc20LookbackFixture{cursor: cursor, address: "T" + strings.Repeat("A", 33), recorded: map[string]bool{}, attempts: map[string]int{}}
	db := testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
			switch {
			case strings.Contains(query, "from bp_payment_trc20_config"):
				return testdb.Rows([]driver.Value{1, "TRON", f.address, "", trc20USDTContract, "5.000000", "10000.000000", 1, f.cursor, int64(0)}), nil
			case strings.Contains(query, "select exists"):
				f.attempts[args[0].Value.(string)]++
				return testdb.Rows([]driver.Value{0}), nil
			case strings.Contains(query, "from bp_payment_trc20_transfer"), strings.Contains(query, "from bp_payment_order"):
				return testdb.Rows(), nil
			default:
				return nil, fmt.Errorf("unexpected query: %s", query)
			}
		},
		Exec: func(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			switch {
			case strings.Contains(query, "into bp_payment_trc20_transfer"):
				txid := args[0].Value.(string)
				if txid == f.failRecord {
					return nil, errors.New("temporary receipt storage failure")
				}
				f.recorded[txid] = true
			case strings.Contains(query, "cursor_timestamp_ms"):
				next := args[0].Value.(int64)
				if next < f.cursor {
					t.Errorf("forward cursor moved backwards: %d -> %d", f.cursor, next)
				}
				f.cursor = next
			case strings.Contains(query, "retry_cursor_id"):
			default:
				return nil, fmt.Errorf("unexpected statement: %s", query)
			}
			return driver.RowsAffected(1), nil
		},
	})
	return NewTrc20Reconciler(&Service{repo: NewRepository(db)}), f
}

func lookbackTransfer(id int, timestamp int64, address string) Trc20Transfer {
	return Trc20Transfer{TxID: fmt.Sprintf("%064x", id), From: "sender", To: address, Amount: "5.010000", BlockTimestamp: timestamp, Confirmations: trc20DefaultConfirmations}
}

func TestTrc20LookbackRecoversDelayedIndexing(t *testing.T) {
	base := time.Now().Add(-time.Hour).UnixMilli()
	r, f := newTrc20LookbackFixture(t, base)
	late := lookbackTransfer(1, base+1000, f.address)
	newer := lookbackTransfer(2, base+2000, f.address)
	chain := &trc20LookbackChain{transfers: []Trc20Transfer{newer}}
	r.chain = chain
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	chain.transfers = []Trc20Transfer{late, newer}
	f.attempts = map[string]int{}
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.recorded[late.TxID] {
		t.Fatal("late-indexed payment behind forward cursor was never recorded")
	}
	if f.cursor != newer.BlockTimestamp {
		t.Fatalf("forward cursor=%d, want %d", f.cursor, newer.BlockTimestamp)
	}
	if f.attempts[newer.TxID] != 1 {
		t.Fatalf("overlapping receipt attempted %d times in one pass", f.attempts[newer.TxID])
	}
}

func TestTrc20LookbackMakesProgressWithoutBlockingForwardScan(t *testing.T) {
	base := time.Now().Add(-time.Hour).UnixMilli()
	r, f := newTrc20LookbackFixture(t, base+5000)
	chain := &trc20LookbackChain{pageLimit: 2}
	for i := 1; i <= 6; i++ {
		chain.transfers = append(chain.transfers, lookbackTransfer(i, base+int64(i)*1000, f.address))
	}
	r.chain = chain
	for pass := 0; pass < 6; pass++ {
		if err := r.ReconcileOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if f.cursor != base+6000 {
			t.Fatalf("backlog blocked forward cursor on pass %d: %d", pass, f.cursor)
		}
	}
	for _, transfer := range chain.transfers {
		if !f.recorded[transfer.TxID] {
			t.Fatalf("bounded lookback never progressed to %s", transfer.TxID)
		}
	}
}

func TestTrc20LookbackRetriesProviderAndPersistenceFailures(t *testing.T) {
	base := time.Now().Add(-time.Hour).UnixMilli()
	r, f := newTrc20LookbackFixture(t, base+3000)
	old := lookbackTransfer(1, base+1000, f.address)
	newer := lookbackTransfer(3, base+3000, f.address)
	chain := &trc20LookbackChain{transfers: []Trc20Transfer{old, newer}, failLower: base + 2000}
	r.chain = chain
	if err := r.ReconcileOnce(context.Background()); err == nil {
		t.Fatal("lookback provider failure was not surfaced")
	}
	chain.failLower = 0
	f.failRecord = old.TxID
	if err := r.ReconcileOnce(context.Background()); err == nil {
		t.Fatal("lookback persistence failure was not surfaced")
	}
	f.failRecord = ""
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.recorded[old.TxID] {
		t.Fatal("lookback advanced past failed receipt")
	}
}

func TestTrc20LookbackRestartsForNewAddressAndWorker(t *testing.T) {
	base := time.Now().Add(-time.Hour).UnixMilli()
	r, f := newTrc20LookbackFixture(t, base+5000)
	chain := &trc20LookbackChain{pageLimit: 2}
	for i := 1; i <= 5; i++ {
		chain.transfers = append(chain.transfers, lookbackTransfer(i, base+int64(i)*1000, f.address))
	}
	r.chain = chain
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.address = "T" + strings.Repeat("B", 33)
	newAddressReceipt := lookbackTransfer(7, base+500, f.address)
	chain.transfers = []Trc20Transfer{newAddressReceipt}
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.recorded[newAddressReceipt.TxID] {
		t.Fatal("changed address reused another address's lookback cursor")
	}
	r = NewTrc20Reconciler(r.service)
	r.chain = chain
	f.recorded = map[string]bool{}
	if err := r.ReconcileOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.recorded[newAddressReceipt.TxID] {
		t.Fatal("restarted worker did not safely rescan lookback window")
	}
}
