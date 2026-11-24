package payment

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"
)

const bep20TestAddress = "0xc3f7cd896e2b5c462ac1f579bcf1abe8c333dc5b"

func TestNormalizeBep20ConfigRequest(t *testing.T) {
	cfg, err := normalizeBep20ConfigRequest(SaveBep20ConfigRequest{
		Enabled:       true,
		Address:       " 0xC3F7CD896E2B5C462AC1F579BCF1ABE8C333DC5B ",
		MinAmount:     "5",
		MaxAmount:     "5000",
		Confirmations: 15,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Address != bep20TestAddress {
		t.Fatalf("address not normalized: %q", cfg.Address)
	}
	if cfg.RPCURL != bep20DefaultRPCURL || cfg.Network != bep20NetworkBSC {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if cfg.MinAmount != "5.000000" || cfg.MaxAmount != "5000.000000" || cfg.Confirmations != 15 {
		t.Fatalf("unexpected limits: %+v", cfg)
	}
	if _, err := normalizeBep20ConfigRequest(SaveBep20ConfigRequest{Enabled: true, Address: "0x1234", MinAmount: "5", MaxAmount: "10"}); err == nil {
		t.Fatal("accepted an invalid BEP20 address")
	}
	if _, err := normalizeBep20ConfigRequest(SaveBep20ConfigRequest{Enabled: true, Address: bep20TestAddress, RPCURL: "http://example.com", MinAmount: "5", MaxAmount: "10"}); err == nil {
		t.Fatal("accepted a non-https RPC url")
	}
	if _, err := normalizeBep20ConfigRequest(SaveBep20ConfigRequest{Enabled: true, Address: bep20TestAddress, MinAmount: "10", MaxAmount: "5"}); err == nil {
		t.Fatal("accepted max below min")
	}
	if _, err := normalizeBep20ConfigRequest(SaveBep20ConfigRequest{Enabled: true, Address: bep20TestAddress, MinAmount: "5", MaxAmount: "10", Confirmations: bep20MaxConfirmations + 1}); err == nil {
		t.Fatal("accepted an out-of-range confirmation depth")
	}
}

func TestNormalizeBep20TxID(t *testing.T) {
	raw := strings.Repeat("AB", 32)
	if got := normalizeBep20TxID(raw); got != "0x"+strings.Repeat("ab", 32) {
		t.Fatalf("unexpected normalized txid: %q", got)
	}
	if got := normalizeBep20TxID("0x" + raw); got != "0x"+strings.Repeat("ab", 32) {
		t.Fatalf("unexpected prefixed txid: %q", got)
	}
	if bep20TxIDPattern.MatchString(normalizeBep20TxID("0x1234")) {
		t.Fatal("accepted a short txid")
	}
}

func TestHexTokenAmountUsesTokenDecimals(t *testing.T) {
	units := new(big.Int)
	units.SetString("50410000000000000000", 10) // 50.41 * 1e18
	amount, err := hexTokenAmount("0x"+units.Text(16), bep20TokenDecimals)
	if err != nil {
		t.Fatal(err)
	}
	if amount != "50.410000" {
		t.Fatalf("amount=%q", amount)
	}
	if _, err := hexTokenAmount("0xzz", bep20TokenDecimals); err == nil {
		t.Fatal("accepted malformed transfer data")
	}
}

func TestBep20TopicAddressRejectsPaddedGarbage(t *testing.T) {
	topic := "0x" + strings.Repeat("0", 24) + strings.TrimPrefix(bep20TestAddress, "0x")
	address, ok := bep20TopicAddress(topic)
	if !ok || address != bep20TestAddress {
		t.Fatalf("address=%q ok=%v", address, ok)
	}
	if _, ok := bep20TopicAddress("0x" + strings.Repeat("1", 64)); ok {
		t.Fatal("accepted non-zero topic padding")
	}
}

func bep20TestServer(t *testing.T, logs []map[string]any) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var request struct {
			ID     int64             `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode rpc request: %v", err)
			return
		}
		var result any
		switch request.Method {
		case "eth_chainId":
			result = "0x38"
		case "eth_blockNumber":
			result = "0x72" // 114
		case "eth_getLogs":
			result = logs
		case "eth_getBlockByNumber":
			result = map[string]any{"timestamp": "0x6553f100"} // 1700000000
		default:
			t.Errorf("unexpected rpc method %s", request.Method)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func bep20TransferLog(txHash string, block int64, logIndex int, to, amountUnits string) map[string]any {
	topicTo := "0x" + strings.Repeat("0", 24) + strings.TrimPrefix(to, "0x")
	topicFrom := "0x" + strings.Repeat("0", 24) + strings.TrimPrefix("0x1111111111111111111111111111111111111111", "0x")
	return map[string]any{
		"address":         bep20USDTContract,
		"topics":          []any{bep20TransferTopic, topicFrom, topicTo},
		"data":            "0x" + amountUnits,
		"blockNumber":     fmt.Sprintf("0x%x", block),
		"transactionHash": txHash,
		"logIndex":        fmt.Sprintf("0x%x", logIndex),
	}
}

func TestBep20RPCListTransfersParsesConfirmedUSDT(t *testing.T) {
	units := new(big.Int)
	units.SetString("50410000000000000000", 10)
	txHash := "0x" + strings.Repeat("a", 64)
	server, calls := bep20TestServer(t, []map[string]any{bep20TransferLog(txHash, 100, 2, bep20TestAddress, units.Text(16))})
	client := &bep20RPCClient{rpcURL: server.URL, httpClient: server.Client()}
	transfers, err := client.ListTransfers(t.Context(), bep20USDTContract, bep20TestAddress, 90, 110, 114)
	if err != nil {
		t.Fatal(err)
	}
	if len(transfers) != 1 {
		t.Fatalf("transfers=%d, calls=%d", len(transfers), *calls)
	}
	transfer := transfers[0]
	if transfer.TxHash != txHash || transfer.LogIndex != 2 || transfer.Amount != "50.410000" ||
		transfer.BlockNumber != 100 || transfer.BlockTimestamp != 1700000000000 || transfer.Confirmations != 15 {
		t.Fatalf("unexpected transfer: %+v", transfer)
	}
	if *calls < 3 {
		t.Fatalf("expected chain id, logs and block header calls, got %d", *calls)
	}
}

func TestBep20RPCRejectsForeignChain(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": "0x1"})
	}))
	defer server.Close()
	client := &bep20RPCClient{rpcURL: server.URL, httpClient: server.Client()}
	if _, err := client.LatestBlock(t.Context()); err == nil {
		t.Fatal("accepted a non-BSC chain id")
	}
}

func TestBep20RPCInspectTransaction(t *testing.T) {
	units := new(big.Int)
	units.SetString("50410000000000000000", 10)
	txHash := "0x" + strings.Repeat("b", 64)
	receipt := map[string]any{
		"transactionHash": txHash,
		"blockNumber":     "0x64",
		"status":          "0x1",
		"logs":            []any{bep20TransferLog(txHash, 100, 0, bep20TestAddress, units.Text(16))},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		var result any
		switch request.Method {
		case "eth_chainId":
			result = "0x38"
		case "eth_blockNumber":
			result = "0x72"
		case "eth_getTransactionReceipt":
			result = receipt
		case "eth_getBlockByNumber":
			result = map[string]any{"timestamp": "0x6553f100"}
		default:
			t.Errorf("unexpected rpc method %s", request.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer server.Close()
	client := &bep20RPCClient{rpcURL: server.URL, httpClient: server.Client()}
	inspection, err := client.InspectTransaction(t.Context(), txHash, bep20USDTContract)
	if err != nil {
		t.Fatal(err)
	}
	if !inspection.Found || !inspection.Success || inspection.Confirmations != 15 || len(inspection.Transfers) != 1 {
		t.Fatalf("unexpected inspection: %+v", inspection)
	}
	if inspection.Transfers[0].Amount != "50.410000" || inspection.BlockTimestamp != 1700000000000 {
		t.Fatalf("unexpected transfer: %+v", inspection.Transfers[0])
	}
}

func TestBep20RPCInspectMissingTransaction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&request)
		result := any(nil)
		switch request.Method {
		case "eth_chainId":
			result = "0x38"
		case "eth_getTransactionReceipt":
			result = nil
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer server.Close()
	client := &bep20RPCClient{rpcURL: server.URL, httpClient: server.Client()}
	inspection, err := client.InspectTransaction(t.Context(), "0x"+strings.Repeat("c", 64), bep20USDTContract)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Found {
		t.Fatal("missing transaction reported as found")
	}
}

func TestBep20SubmitVerdict(t *testing.T) {
	order := PaymentOrder{OrderNo: "BP1", ChainAddress: bep20TestAddress, ChainAmount: "50.410000"}
	cases := []struct {
		name       string
		inspection Bep20TxInspection
		required   int
		reason     string
	}{
		{name: "missing", inspection: Bep20TxInspection{}, reason: "CHAIN_TX_NOT_FOUND"},
		{name: "failed", inspection: Bep20TxInspection{Found: true}, reason: "CHAIN_TX_FAILED"},
		{name: "confirming", inspection: Bep20TxInspection{Found: true, Success: true, Confirmations: 3, Transfers: []Bep20Transfer{{To: bep20TestAddress, Amount: "50.410000"}}}, required: 15, reason: "CHAIN_CONFIRMING"},
		{name: "amount", inspection: Bep20TxInspection{Found: true, Success: true, Confirmations: 20, Transfers: []Bep20Transfer{{To: bep20TestAddress, Amount: "50.400000"}}}, required: 15, reason: "AMOUNT_MISMATCH"},
		{name: "recipient", inspection: Bep20TxInspection{Found: true, Success: true, Confirmations: 20, Transfers: []Bep20Transfer{{To: "0x1111111111111111111111111111111111111111", Amount: "50.410000"}}}, required: 15, reason: "RECIPIENT_MISMATCH"},
		{name: "not-usdt", inspection: Bep20TxInspection{Found: true, Success: true, Confirmations: 20}, required: 15, reason: "NOT_USDT_TRANSFER"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := bep20SubmitVerdict(order, tc.inspection, tc.required)
			business, ok := err.(*apperrors.BusinessError)
			if !ok {
				t.Fatalf("expected business error, got %T %v", err, err)
			}
			data, ok := business.Data.(map[string]any)
			if !ok || data["reason"] != tc.reason {
				t.Fatalf("reason=%v, want %s", business.Data, tc.reason)
			}
		})
	}
}
