package payment

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestHexToTronAddressMatchesKnownAddress(t *testing.T) {
	// 本机核验过的收款地址与其 EVM 形式一一对应：
	// TMgEhEBSmLjgMkv6vQwLyhkCRQXSDieuSK == 0x806bd9d7483f1411396c816030aec0ecb45f36ab
	cases := map[string]string{
		"806bd9d7483f1411396c816030aec0ecb45f36ab":   "TMgEhEBSmLjgMkv6vQwLyhkCRQXSDieuSK",
		"0x806bd9d7483f1411396c816030aec0ecb45f36ab": "TMgEhEBSmLjgMkv6vQwLyhkCRQXSDieuSK",
		"41806bd9d7483f1411396c816030aec0ecb45f36ab": "TMgEhEBSmLjgMkv6vQwLyhkCRQXSDieuSK",
		"a614f803b6fd780986a42c78ec9c7f77e6ded13c":   "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t",
		// 事件 topic 里的 ABI 编码形式（左补零到 32 字节），链上回执就是这么给的。
		"000000000000000000000000806bd9d7483f1411396c816030aec0ecb45f36ab": "TMgEhEBSmLjgMkv6vQwLyhkCRQXSDieuSK",
	}
	for input, want := range cases {
		got, err := hexToTronAddress(input)
		if err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if got != want {
			t.Fatalf("%s -> %s, want %s", input, got, want)
		}
	}
	if _, err := hexToTronAddress("zz"); err == nil {
		t.Fatal("short payload must fail")
	}
	// 非零填充说明不是合法地址，必须拒绝而不是解出个假地址。
	if _, err := hexToTronAddress("ff0000000000000000000000806bd9d7483f1411396c816030aec0ecb45f36ab"); err == nil {
		t.Fatal("non-zero padded topic must fail")
	}
}

func TestTrc20USDTContractHexMatchesBase58Contract(t *testing.T) {
	got, err := hexToTronAddress(trc20USDTContractHex)
	if err != nil {
		t.Fatal(err)
	}
	if got != trc20USDTContract {
		t.Fatalf("contract hex %s decodes to %s, want %s", trc20USDTContractHex, got, trc20USDTContract)
	}
}

func TestTrc20HexAmountConvertsTokenUnits(t *testing.T) {
	// 2.63 USDT = 2630000 (= 0x282170) 最小单位，链上以 32 字节十六进制存放。
	got, err := trc20HexAmount("0000000000000000000000000000000000000000000000000000000000282170", 6)
	if err != nil || got != "2.630000" {
		t.Fatalf("got %q err=%v", got, err)
	}
	// 50.41 USDT = 50410000 (= 0x3013210)
	got, err = trc20HexAmount("0000000000000000000000000000000000000000000000000000000003013210", 6)
	if err != nil || got != "50.410000" {
		t.Fatalf("50.41 decode: got %q err=%v", got, err)
	}
	if _, err := trc20HexAmount("", 6); err == nil {
		t.Fatal("empty data must fail")
	}
}

// 真实链上回执核对：默认跳过，显式给环境变量才打外网。
//
//	TRC20_LIVE=1 TRC20_LIVE_TXID=c58c... go test ./internal/domain/payment -run TestLiveExplainTrc20 -v
func TestLiveExplainTrc20(t *testing.T) {
	if os.Getenv("TRC20_LIVE") != "1" {
		t.Skip("set TRC20_LIVE=1 to query the real transaction receipt")
	}
	txid := os.Getenv("TRC20_LIVE_TXID")
	if txid == "" {
		t.Skip("TRC20_LIVE_TXID is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := ExplainTrc20Transaction(ctx, txid, os.Getenv("TRC20_LIVE_APIKEY"))
	if err != nil {
		t.Fatalf("explain failed: %v", err)
	}
	t.Logf("found=%v success=%v from=%s to=%s amount=%s contract=%s block=%d other=%v",
		got.Found, got.Success, got.From, got.To, got.Amount, got.Contract, got.BlockTimestamp, got.OtherTransfers)
	if !got.Found {
		t.Fatal("transaction not found on chain")
	}
	if got.To == "" || got.Amount == "" {
		t.Fatalf("no USDT transfer decoded: %+v", got)
	}
}
