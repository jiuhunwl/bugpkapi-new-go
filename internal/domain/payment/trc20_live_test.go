package payment

import (
	"context"
	"os"
	"testing"
	"time"
)

// 线上验证：真实收款地址 + 真实 txid 走一遍链上定位器（CLI 与用户提交 txid
// 共用同一条路径）。默认跳过，只有显式给环境变量时才打外网：
//
//	TRC20_LIVE=1 TRC20_LIVE_ADDRESS=TMgE... TRC20_LIVE_TXID=c58c... go test ./internal/domain/payment -run TestLiveTrc20Locator -v
func TestLiveTrc20Locator(t *testing.T) {
	if os.Getenv("TRC20_LIVE") != "1" {
		t.Skip("set TRC20_LIVE=1 to exercise the real TronGrid locator")
	}
	address := os.Getenv("TRC20_LIVE_ADDRESS")
	txid := os.Getenv("TRC20_LIVE_TXID")
	if address == "" || txid == "" {
		t.Skip("TRC20_LIVE_ADDRESS and TRC20_LIVE_TXID are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	transfer, found, err := FindConfirmedTrc20Transfer(ctx, address, trc20USDTContract, os.Getenv("TRC20_LIVE_APIKEY"), txid, time.Now().Add(-365*24*time.Hour))
	if err != nil {
		t.Fatalf("locator failed: %v", err)
	}
	if !found {
		t.Fatalf("txid %s not found on %s", txid, address)
	}
	t.Logf("found txid=%s from=%s to=%s amount=%s block=%d confirmations=%d",
		transfer.TxID, transfer.From, transfer.To, transfer.Amount, transfer.BlockTimestamp, transfer.Confirmations)
	if transfer.Amount != "2.630000" {
		t.Errorf("amount=%s, want 2.630000", transfer.Amount)
	}
}
