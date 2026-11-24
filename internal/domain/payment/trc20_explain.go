package payment

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"bugpk-api/internal/pkg/httpclient"
)

// 「币安显示已到账，接口却查不到」的定位器。
//
// 常规对账只能按「收款地址 + 金额」在提供方的账户流水里找，一旦钱落在别的地址、
// 或者金额被手续费改过，就什么也看不到——上层只会回一句「链上暂未确认该交易」，
// 运维和用户都无从判断钱到底去哪了。
//
// 这里按交易号直接读链上交易回执：不依赖收款地址，因此能明确回答三件事——
//
//  1. 这笔交易在链上到底存不存在、成功没有；
//  2. 它的 USDT 实际转给了哪个地址、转了多少；
//  3. 与我们订单要求相比，是地址不符还是金额不符。
const (
	trongridTxInfoPath = "/wallet/gettransactioninfobyid"
	// TRC20 Transfer(address,address,uint256) 的事件签名。
	trc20TransferTopic = "ddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"
	// USDT-TRC20 合约的 21 字节十六进制形式（0x41 + 20 字节）。
	trc20USDTContractHex = "41a614f803b6fd780986a42c78ec9c7f77e6ded13c"
	base58Alphabet       = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
)

// Trc20TxExplanation 是一笔链上交易的 USDT 归属真相。
type Trc20TxExplanation struct {
	TxID           string   `json:"txid"`
	Found          bool     `json:"found"`
	Success        bool     `json:"success"`
	BlockTimestamp int64    `json:"blockTimestamp"`
	Contract       string   `json:"contract"`
	From           string   `json:"from"`
	To             string   `json:"to"`
	Amount         string   `json:"amount"`
	OtherTransfers []string `json:"otherTransfers,omitempty"`
}

type trongridTxInfoResponse struct {
	ID             string `json:"id"`
	BlockTimestamp int64  `json:"blockTimeStamp"`
	Receipt        struct {
		Result string `json:"result"`
	} `json:"receipt"`
	Result string          `json:"result"`
	Logs   []trongridTxLog `json:"log"`
	Log    []trongridTxLog `json:"Log"`
	// 节点把合约返回值报成数组，这里不需要它，用 RawMessage 避免整包解码失败。
	ContractR json.RawMessage `json:"contractResult"`
}

type trongridTxLog struct {
	Address string   `json:"address"`
	Topics  []string `json:"topics"`
	Data    string   `json:"data"`
}

// ExplainTrc20Transaction 按 txid 读取链上回执，返回 USDT 转账的实际收款方与金额。
// 它不查账户流水，所以即使钱进了别的地址也照样能定位。
func ExplainTrc20Transaction(ctx context.Context, txid, apiKey string) (Trc20TxExplanation, error) {
	explanation := Trc20TxExplanation{TxID: strings.TrimSpace(txid)}
	if !trc20TxIDPattern.MatchString(explanation.TxID) {
		return explanation, fmt.Errorf("交易号格式不正确")
	}
	body, err := json.Marshal(map[string]string{"value": explanation.TxID})
	if err != nil {
		return explanation, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, trongridBaseURL+trongridTxInfoPath, bytes.NewReader(body))
	if err != nil {
		return explanation, err
	}
	request.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(apiKey) != "" {
		request.Header.Set("TRON-PRO-API-KEY", strings.TrimSpace(apiKey))
	}
	response, err := httpclient.Outbound(15 * time.Second).Do(request)
	if err != nil {
		return explanation, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return explanation, fmt.Errorf("trongrid returned status %d", response.StatusCode)
	}
	var payload trongridTxInfoResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&payload); err != nil {
		return explanation, fmt.Errorf("decode transaction info: %w", err)
	}
	// 空对象表示节点还没有这笔交易（尚未上链或已被丢弃）。
	if payload.ID == "" {
		return explanation, nil
	}
	explanation.Found = true
	explanation.BlockTimestamp = payload.BlockTimestamp
	explanation.Success = strings.EqualFold(payload.Receipt.Result, "SUCCESS") ||
		(!strings.EqualFold(payload.Result, "FAILED") && payload.Receipt.Result == "")
	logs := payload.Logs
	if len(logs) == 0 {
		logs = payload.Log
	}
	for _, entry := range logs {
		if len(entry.Topics) < 3 || !strings.EqualFold(strings.TrimPrefix(entry.Topics[0], "0x"), trc20TransferTopic) {
			continue
		}
		// 回执里的 log.address 是 20 字节合约地址（不带 41 前缀），统一解码成
		// base58 后再和 USDT 合约比对，避免把其他代币的转账当成到账。
		contractAddress, err := hexToTronAddress(entry.Address)
		if err != nil {
			continue
		}
		from, fromErr := hexToTronAddress(strings.TrimPrefix(entry.Topics[1], "0x"))
		to, toErr := hexToTronAddress(strings.TrimPrefix(entry.Topics[2], "0x"))
		if fromErr != nil || toErr != nil {
			continue
		}
		amount, err := trc20HexAmount(entry.Data, 6)
		if err != nil {
			continue
		}
		if !strings.EqualFold(contractAddress, trc20USDTContract) {
			// 非 USDT 的同类事件留着做旁证，避免把垃圾币当成到账。
			explanation.OtherTransfers = append(explanation.OtherTransfers,
				fmt.Sprintf("token=%s from=%s to=%s amount=%s", contractAddress, from, to, amount))
			continue
		}
		// 一笔交易里可能出现多个 Transfer；第一条 USDT 转账作为主结论。
		if explanation.To == "" {
			explanation.Contract = contractAddress
			explanation.From = from
			explanation.To = to
			explanation.Amount = amount
		}
	}
	return explanation, nil
}

// trc20HexAmount 把 32 字节十六进制数量按精度换算成十进制文本。
func trc20HexAmount(data string, decimals int) (string, error) {
	raw := strings.TrimPrefix(strings.TrimSpace(data), "0x")
	if raw == "" {
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

// hexToTronAddress 把地址十六进制转成 TRON 的 base58check 形式。
// 三种写法都要吃下：40 字符纯地址、带 41 前缀的 42 字符、以及事件 topic 里
// 左补零到 32 字节（64 字符）的 ABI 编码地址。
func hexToTronAddress(raw string) (string, error) {
	trimmed := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "0x")
	if len(trimmed) == 64 {
		// ABI 编码：地址在最低 20 字节，前面是 12 字节零填充。
		if trimmed[:24] != strings.Repeat("0", 24) {
			return "", fmt.Errorf("address topic has non-zero padding")
		}
		trimmed = trimmed[24:]
	}
	if len(trimmed) == 42 && strings.HasPrefix(trimmed, "41") {
		trimmed = trimmed[2:]
	}
	if len(trimmed) != 40 {
		return "", fmt.Errorf("address payload must be 20 bytes, got %d hex chars", len(trimmed))
	}
	payload, err := hex.DecodeString(trimmed)
	if err != nil {
		return "", err
	}
	body := append([]byte{0x41}, payload...)
	return base58CheckEncode(body)
}

func base58CheckEncode(body []byte) (string, error) {
	first := sha256.Sum256(body)
	second := sha256.Sum256(first[:])
	full := append(append([]byte{}, body...), second[:4]...)
	value := new(big.Int).SetBytes(full)
	radix := big.NewInt(58)
	mod := new(big.Int)
	var encoded []byte
	for value.Sign() > 0 {
		value.DivMod(value, radix, mod)
		encoded = append(encoded, base58Alphabet[mod.Int64()])
	}
	// 前导零字节要还原成 '1'，这是 base58 的长度保留规则。
	for _, b := range full {
		if b != 0 {
			break
		}
		encoded = append(encoded, base58Alphabet[0])
	}
	if len(encoded) == 0 {
		return "", fmt.Errorf("empty address payload")
	}
	for left, right := 0, len(encoded)-1; left < right; left, right = left+1, right-1 {
		encoded[left], encoded[right] = encoded[right], encoded[left]
	}
	return string(encoded), nil
}
