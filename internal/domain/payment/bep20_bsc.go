package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/pkg/httpclient"
)

// BEP20（BSC）链上客户端。
//
// 不依赖 BscScan 之类的索引 API：直接读公共/自建 BSC 节点的 JSON-RPC。
//   - eth_getLogs 按 USDT Transfer 事件 + 收款地址 topic 增量扫描；
//   - eth_getTransactionReceipt 按交易号精确核验（用户提交 txid 的路径）；
//   - eth_getBlockByNumber 取区块时间，用于订单时间窗匹配。
//
// 只依赖标准 EVM 方法，因此换成任何自建 BSC 节点都能工作（RPC URL 后台可配）。
const (
	bep20ChainID             = int64(56)
	bep20LogChunkBlocks      = int64(5000)
	bep20MaxLogChunksPerScan = 48
	bep20MaxLogsPerScan      = 512
	bep20TransferTopic       = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
)

// Bep20Transfer 是一笔已确认（或待确认）的 USDT-BEP20 入账。
type Bep20Transfer struct {
	ReceiptID      int64
	TxHash         string
	LogIndex       int
	From           string
	To             string
	Amount         string
	BlockNumber    int64
	BlockTimestamp int64
	Confirmations  int
}

// Bep20TxInspection 是按交易号核验后的链上真相。
type Bep20TxInspection struct {
	TxHash         string          `json:"txHash"`
	Found          bool            `json:"found"`
	Success        bool            `json:"success"`
	BlockNumber    int64           `json:"blockNumber"`
	BlockTimestamp int64           `json:"blockTimestamp"`
	Confirmations  int             `json:"confirmations"`
	Transfers      []Bep20Transfer `json:"transfers,omitempty"`
	OtherTransfers []string        `json:"otherTransfers,omitempty"`
}

type Bep20ChainClient interface {
	LatestBlock(context.Context) (int64, error)
	ListTransfers(context.Context, string, string, int64, int64, int64) ([]Bep20Transfer, error)
	InspectTransaction(context.Context, string, string) (Bep20TxInspection, error)
}

type bep20RPCClient struct {
	rpcURL     string
	httpClient *http.Client
	chainOK    bool
	chainID    int64
}

func newBep20RPCClient(rpcURL string) *bep20RPCClient {
	if strings.TrimSpace(rpcURL) == "" {
		rpcURL = bep20DefaultRPCURL
	}
	return &bep20RPCClient{rpcURL: strings.TrimSpace(rpcURL), httpClient: httpclient.Outbound(20 * time.Second)}
}

type bep20RPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type bep20RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type bep20RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *bep20RPCError  `json:"error"`
}

type bep20rpcLog struct {
	Address         string   `json:"address"`
	Topics          []string `json:"topics"`
	Data            string   `json:"data"`
	BlockNumber     string   `json:"blockNumber"`
	TransactionHash string   `json:"transactionHash"`
	LogIndex        string   `json:"logIndex"`
	Removed         bool     `json:"removed"`
}

type bep20RPCReceipt struct {
	TransactionHash string        `json:"transactionHash"`
	BlockNumber     string        `json:"blockNumber"`
	Status          string        `json:"status"`
	Logs            []bep20rpcLog `json:"logs"`
}

func (c *bep20RPCClient) call(ctx context.Context, method string, params []any, target any) error {
	body, err := json.Marshal(bep20RPCRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.rpcURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("bsc rpc %s returned status %d", method, response.StatusCode)
	}
	var payload bep20RPCResponse
	if err := httpclient.DecodeJSON(response.Body, httpclient.MaxPaymentResponseBytes*4, &payload); err != nil {
		return fmt.Errorf("decode bsc rpc %s: %w", method, err)
	}
	if payload.Error != nil {
		return fmt.Errorf("bsc rpc %s failed: %s", method, payload.Error.Message)
	}
	if target == nil || len(payload.Result) == 0 || string(payload.Result) == "null" {
		return nil
	}
	if raw, ok := target.(*json.RawMessage); ok {
		*raw = payload.Result
		return nil
	}
	if err := json.Unmarshal(payload.Result, target); err != nil {
		return fmt.Errorf("decode bsc rpc %s result: %w", method, err)
	}
	return nil
}

// ensureChain 确认 RPC 节点确实在 BSC 主网，避免把地址配到测试网/其他 EVM 链上。
func (c *bep20RPCClient) ensureChain(ctx context.Context) error {
	if c.chainOK {
		return nil
	}
	var raw string
	if err := c.call(ctx, "eth_chainId", []any{}, &raw); err != nil {
		return err
	}
	value, err := parseHexInt64(raw)
	if err != nil || value != bep20ChainID {
		return fmt.Errorf("bsc rpc chain id %q is not BSC mainnet (56)", strings.TrimSpace(raw))
	}
	c.chainID = value
	c.chainOK = true
	return nil
}

func (c *bep20RPCClient) LatestBlock(ctx context.Context) (int64, error) {
	if err := c.ensureChain(ctx); err != nil {
		return 0, err
	}
	var raw string
	if err := c.call(ctx, "eth_blockNumber", []any{}, &raw); err != nil {
		return 0, err
	}
	value, err := parseHexInt64(raw)
	if err != nil {
		return 0, fmt.Errorf("invalid bsc block number %q", raw)
	}
	return value, nil
}

// Probe 验证节点确实支持本通道依赖的 eth_getLogs（部分公共节点只开放区块查询）。
func (c *bep20RPCClient) Probe(ctx context.Context, contract, address string) error {
	latest, err := c.LatestBlock(ctx)
	if err != nil {
		return err
	}
	from := latest - 20
	if from < 1 {
		from = 1
	}
	to := latest - 1
	if to < from {
		to = from
	}
	_, err = c.ListTransfers(ctx, contract, address, from, to, latest)
	return err
}

// ListTransfers 扫描 [fromBlock,toBlock] 内转入 address 的 USDT-BEP20 转账。
// 结果按区块号、日志序号升序；任何一页读取失败都整体失败，调用方不得推进游标。
func (c *bep20RPCClient) ListTransfers(ctx context.Context, contract, address string, fromBlock, toBlock, latest int64) ([]Bep20Transfer, error) {
	contract = strings.ToLower(strings.TrimSpace(contract))
	address = strings.ToLower(strings.TrimSpace(address))
	if !bep20AddressPattern.MatchString(contract) || !bep20AddressPattern.MatchString(address) {
		return nil, fmt.Errorf("invalid BEP20 contract or receiving address")
	}
	if fromBlock < 0 || toBlock < fromBlock {
		return nil, fmt.Errorf("invalid BEP20 block range %d-%d", fromBlock, toBlock)
	}
	if latest < toBlock {
		return nil, fmt.Errorf("BEP20 scan upper bound is ahead of the chain head")
	}
	if err := c.ensureChain(ctx); err != nil {
		return nil, err
	}
	topicTo := "0x" + strings.Repeat("0", 24) + strings.TrimPrefix(address, "0x")
	var transfers []Bep20Transfer
	timestampCache := make(map[int64]int64)
	chunks := 0
	for start := fromBlock; start <= toBlock; start += bep20LogChunkBlocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		chunks++
		if chunks > bep20MaxLogChunksPerScan {
			return nil, fmt.Errorf("BEP20 scan range exceeds the per-run chunk budget")
		}
		end := start + bep20LogChunkBlocks - 1
		if end > toBlock {
			end = toBlock
		}
		var logs []bep20rpcLog
		params := []any{map[string]any{
			"fromBlock": fmt.Sprintf("0x%x", start),
			"toBlock":   fmt.Sprintf("0x%x", end),
			"address":   contract,
			"topics":    []any{bep20TransferTopic, nil, topicTo},
		}}
		if err := c.call(ctx, "eth_getLogs", params, &logs); err != nil {
			return nil, err
		}
		for _, entry := range logs {
			transfer, err := c.parseTransferLog(ctx, entry, contract, address, latest, timestampCache)
			if err != nil {
				return nil, err
			}
			transfers = append(transfers, transfer)
			if len(transfers) > bep20MaxLogsPerScan {
				return nil, fmt.Errorf("BEP20 scan returned too many receipts in one run")
			}
		}
	}
	return transfers, nil
}

// InspectTransaction 按交易号读取回执：不依赖收款地址扫描，因此能回答
// 「这笔钱到底转给了谁、转了多少、确认没有」。
func (c *bep20RPCClient) InspectTransaction(ctx context.Context, txHash, contract string) (Bep20TxInspection, error) {
	inspection := Bep20TxInspection{TxHash: normalizeBep20TxID(txHash)}
	if !bep20TxIDPattern.MatchString(inspection.TxHash) {
		return inspection, fmt.Errorf("BEP20 交易号格式不正确")
	}
	contract = strings.ToLower(strings.TrimSpace(contract))
	if !bep20AddressPattern.MatchString(contract) {
		return inspection, fmt.Errorf("BEP20 USDT 合约地址配置不正确")
	}
	if err := c.ensureChain(ctx); err != nil {
		return inspection, err
	}
	var receipt *bep20RPCReceipt
	if err := c.call(ctx, "eth_getTransactionReceipt", []any{inspection.TxHash}, &receipt); err != nil {
		return inspection, err
	}
	// nil 回执 = 节点还没有这笔交易（尚未上链、已被丢弃，或根本不是 BSC 交易）。
	if receipt == nil || strings.TrimSpace(receipt.TransactionHash) == "" {
		return inspection, nil
	}
	inspection.Found = true
	inspection.Success = strings.EqualFold(strings.TrimSpace(receipt.Status), "0x1")
	blockNumber, err := parseHexInt64(receipt.BlockNumber)
	if err != nil {
		return inspection, fmt.Errorf("invalid BEP20 receipt block number %q", receipt.BlockNumber)
	}
	inspection.BlockNumber = blockNumber
	timestamp, err := c.blockTimestamp(ctx, blockNumber)
	if err != nil {
		return inspection, err
	}
	inspection.BlockTimestamp = timestamp
	latest, err := c.LatestBlock(ctx)
	if err != nil {
		return inspection, err
	}
	if latest >= blockNumber {
		inspection.Confirmations = int(latest - blockNumber + 1)
	}
	for _, entry := range receipt.Logs {
		if entry.Removed || len(entry.Topics) < 3 || !sameHex(entry.Topics[0], bep20TransferTopic) {
			continue
		}
		from, to, amount, ok := parseBep20TransferTopics(entry)
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(entry.Address), contract) {
			// 同一交易里可能有垃圾币的 Transfer 事件，留作旁证。
			inspection.OtherTransfers = append(inspection.OtherTransfers,
				fmt.Sprintf("token=%s from=%s to=%s amount=%s", strings.TrimSpace(entry.Address), from, to, amount))
			continue
		}
		logIndex, _ := parseHexInt64(entry.LogIndex)
		inspection.Transfers = append(inspection.Transfers, Bep20Transfer{
			TxHash:         inspection.TxHash,
			LogIndex:       int(logIndex),
			From:           from,
			To:             to,
			Amount:         amount,
			BlockNumber:    blockNumber,
			BlockTimestamp: inspection.BlockTimestamp,
			Confirmations:  inspection.Confirmations,
		})
	}
	return inspection, nil
}

func (c *bep20RPCClient) parseTransferLog(ctx context.Context, entry bep20rpcLog, contract, address string, latest int64, timestamps map[int64]int64) (Bep20Transfer, error) {
	if entry.Removed {
		return Bep20Transfer{}, fmt.Errorf("BEP20 receipt was reorged out")
	}
	if !strings.EqualFold(strings.TrimSpace(entry.Address), contract) {
		return Bep20Transfer{}, fmt.Errorf("BEP20 log contract mismatch")
	}
	if len(entry.Topics) < 3 || !sameHex(entry.Topics[0], bep20TransferTopic) {
		return Bep20Transfer{}, fmt.Errorf("BEP20 log is not a Transfer event")
	}
	from, to, amount, ok := parseBep20TransferTopics(entry)
	if !ok || !strings.EqualFold(to, address) {
		return Bep20Transfer{}, fmt.Errorf("BEP20 log recipient mismatch")
	}
	blockNumber, err := parseHexInt64(entry.BlockNumber)
	if err != nil || blockNumber > latest {
		return Bep20Transfer{}, fmt.Errorf("invalid BEP20 log block number %q", entry.BlockNumber)
	}
	logIndex, err := parseHexInt64(entry.LogIndex)
	if err != nil || logIndex < 0 {
		return Bep20Transfer{}, fmt.Errorf("invalid BEP20 log index %q", entry.LogIndex)
	}
	txHash := normalizeBep20TxID(entry.TransactionHash)
	if !bep20TxIDPattern.MatchString(txHash) {
		return Bep20Transfer{}, fmt.Errorf("invalid BEP20 transaction hash")
	}
	timestamp, ok := timestamps[blockNumber]
	if !ok {
		if len(timestamps) >= bep20MaxLogsPerScan {
			return Bep20Transfer{}, fmt.Errorf("too many distinct BEP20 blocks in one scan")
		}
		timestamp, err = c.blockTimestamp(ctx, blockNumber)
		if err != nil {
			return Bep20Transfer{}, err
		}
		timestamps[blockNumber] = timestamp
	}
	return Bep20Transfer{
		TxHash:         txHash,
		LogIndex:       int(logIndex),
		From:           from,
		To:             strings.ToLower(to),
		Amount:         amount,
		BlockNumber:    blockNumber,
		BlockTimestamp: timestamp,
		Confirmations:  int(latest - blockNumber + 1),
	}, nil
}

func (c *bep20RPCClient) blockTimestamp(ctx context.Context, blockNumber int64) (int64, error) {
	var block struct {
		Timestamp string `json:"timestamp"`
	}
	if err := c.call(ctx, "eth_getBlockByNumber", []any{fmt.Sprintf("0x%x", blockNumber), false}, &block); err != nil {
		return 0, err
	}
	value, err := parseHexInt64(block.Timestamp)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("invalid BEP20 block timestamp %q", block.Timestamp)
	}
	return value * 1000, nil
}

func parseBep20TransferTopics(entry bep20rpcLog) (string, string, string, bool) {
	if len(entry.Topics) < 3 {
		return "", "", "", false
	}
	from, okFrom := bep20TopicAddress(entry.Topics[1])
	to, okTo := bep20TopicAddress(entry.Topics[2])
	if !okFrom || !okTo {
		return "", "", "", false
	}
	amount, err := hexTokenAmount(entry.Data, bep20TokenDecimals)
	if err != nil {
		return "", "", "", false
	}
	return from, to, amount, true
}

func bep20TopicAddress(topic string) (string, bool) {
	raw := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(topic), "0x"))
	if len(raw) != 64 || strings.Trim(raw[:24], "0") != "" {
		return "", false
	}
	return "0x" + raw[24:], true
}

func parseHexInt64(raw string) (int64, error) {
	trimmed := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "0x")
	if trimmed == "" || len(trimmed) > 16 {
		return 0, fmt.Errorf("invalid hex integer %q", raw)
	}
	value, err := strconv.ParseInt(trimmed, 16, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("invalid hex integer %q", raw)
	}
	return value, nil
}

func sameHex(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

// hexTokenAmount 把 32 字节十六进制数量按精度换算成十进制文本。
func hexTokenAmount(data string, decimals int) (string, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(data), "0x")
	if raw == "" || len(raw) > 64 {
		return "", fmt.Errorf("empty transfer data")
	}
	units, ok := new(big.Int).SetString(raw, 16)
	if !ok || units.Sign() < 0 {
		return "", fmt.Errorf("invalid transfer data %q", data)
	}
	if decimals < 0 || decimals > 18 {
		decimals = 6
	}
	divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	return new(big.Rat).SetFrac(units, divisor).FloatString(6), nil
}
