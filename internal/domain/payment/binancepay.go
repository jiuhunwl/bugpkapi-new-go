package payment

import (
	"context"
	"crypto/hmac"
	crand "crypto/rand"
	"crypto/sha512"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/pkg/decimalvalue"
	"bugpk-api/internal/pkg/httpclient"
)

// 币安 Pay 商户通道：覆盖「链下转账」这一类到账。
//
// TRC20 只能看见链上交易，而交易所站内划转根本不产生链上交易，所以永远查不到。
// 唯一直连的办法是让交易所主动告诉我们——这就是币安 Pay 商户的 webhook + 主动查单。
//
// 协议要点（实现必须严格照做，否则会被伪造回调打穿）：
//
//	payload   = timestamp + "\n" + nonce + "\n" + body + "\n"
//	signature = Base64(HMAC-SHA512(secretKey, payload))
//	请求头     = BinancePay-Timestamp / BinancePay-Nonce / BinancePay-Certificate-Snsserial
//
// 关键安全约束：
//   - body 必须用原始字节参与验签，重新序列化会改变字段顺序导致签名永远对不上；
//   - 验签用 constant-time 比较，避免时序侧信道；
//   - 时间戳窗口限制（默认 5 分钟）防止重放；
//   - nonce 落库做幂等，币安会重复推送同一笔；
//   - 只有 bizStatus == "PAY_SUCCESS" 才允许入账。
const (
	ChannelBinancePay = "BINANCE_PAY"

	binancePayBaseURL     = "https://bpay.binanceapi.com"
	binancePayOrderPath   = "/binancepay/openapi/v2/order/query"
	binancePayCurrency    = "USDT"
	binancePayMaxSkew     = 5 * time.Minute
	binancePayMaxBodySize = 256 << 10

	// 回调业务类型与状态，只有这一组组合代表「钱真的收到了」。
	binancePayBizType   = "PAY"
	binancePayStatusOK  = "PAY_SUCCESS"
	binancePayStatusEnd = "PAY_CLOSED"
)

var (
	ErrBinancePaySignature = errors.New("binance pay signature verification failed")
	ErrBinancePayReplay    = errors.New("binance pay callback timestamp outside the allowed window")
	ErrBinancePayNotPaid   = errors.New("binance pay order is not in a paid state")
	ErrBinancePayAmount    = errors.New("binance pay amount does not match the order")
)

// BinancePayConfig 是后台可配置的商户参数。secretKey 与私钥都以密文入库。
type BinancePayConfig struct {
	Enabled         bool   `json:"enabled"`
	MerchantID      string `json:"merchantId"`
	CertSerial      string `json:"certSerial"`
	SecretKeyCipher string `json:"-"`
	APIKeyCipher    string `json:"-"`
	// PayCurrency 允许运营切换收款币种，默认 USDT。
	PayCurrency string `json:"payCurrency"`
	// MinAmount/MaxAmount 与 TRC20 保持同一口径，运营可独立调整。
	MinAmount string `json:"minAmount"`
	MaxAmount string `json:"maxAmount"`
	// NotifyURL 展示给运营确认回调地址，实际以部署配置为准。
	NotifyURL string `json:"notifyUrl"`
}

// BinancePayCallback 是币安推送体的最小必要结构。
type BinancePayCallback struct {
	BizType   string `json:"bizType"`
	BizStatus string `json:"bizStatus"`
	Data      string `json:"data"`
}

// BinancePayOrderData 是 data 字段（JSON 字符串）解析后的内容。
type BinancePayOrderData struct {
	MerchantTradeNo string `json:"merchantTradeNo"`
	PrepayID        string `json:"prepayId"`
	TransactionID   string `json:"transactionId"`
	OrderAmount     string `json:"orderAmount"`
	Currency        string `json:"currency"`
	TransactTime    int64  `json:"transactTime"`
}

// Amount 返回交易所声明的订单金额，空值时由调用方决定如何拒绝。
func (d BinancePayOrderData) Amount() string {
	return strings.TrimSpace(d.OrderAmount)
}

// BinancePaySignature 按官方规则计算回调/请求签名。
func BinancePaySignature(secretKey, timestamp, nonce string, body []byte) (string, error) {
	if strings.TrimSpace(secretKey) == "" {
		return "", ErrPaymentSecretMissing
	}
	mac := hmac.New(sha512.New, []byte(secretKey))
	// payload 的三段分隔符和结尾换行都不能少，缺一个字节签名就不同。
	if _, err := mac.Write([]byte(timestamp + "\n" + nonce + "\n")); err != nil {
		return "", err
	}
	if _, err := mac.Write(body); err != nil {
		return "", err
	}
	if _, err := mac.Write([]byte("\n")); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(mac.Sum(nil)), nil
}

// VerifyBinancePayCallback 校验回调的真实性并解析出订单数据。
// body 必须是请求体的原始字节。
func VerifyBinancePayCallback(secretKey, timestamp, nonce, signature string, body []byte, now time.Time) (BinancePayOrderData, BinancePayCallback, error) {
	var empty BinancePayOrderData
	if len(body) == 0 || len(body) > binancePayMaxBodySize {
		return empty, BinancePayCallback{}, fmt.Errorf("callback body size %d is not acceptable", len(body))
	}
	stamp, err := strconv.ParseInt(strings.TrimSpace(timestamp), 10, 64)
	if err != nil {
		return empty, BinancePayCallback{}, ErrBinancePaySignature
	}
	// 时间戳单位是毫秒；超出窗口的回调一律拒绝，避免旧包被重放。
	skew := now.Sub(time.UnixMilli(stamp))
	if skew < 0 {
		skew = -skew
	}
	if skew > binancePayMaxSkew {
		return empty, BinancePayCallback{}, ErrBinancePayReplay
	}
	expected, err := BinancePaySignature(secretKey, strings.TrimSpace(timestamp), strings.TrimSpace(nonce), body)
	if err != nil {
		return empty, BinancePayCallback{}, err
	}
	// constant-time 比较，避免逐字节比较泄漏签名前缀。
	if subtle.ConstantTimeCompare([]byte(expected), []byte(strings.TrimSpace(signature))) != 1 {
		return empty, BinancePayCallback{}, ErrBinancePaySignature
	}
	var callback BinancePayCallback
	if err := json.Unmarshal(body, &callback); err != nil {
		return empty, BinancePayCallback{}, fmt.Errorf("decode callback: %w", err)
	}
	if strings.TrimSpace(callback.Data) == "" {
		return empty, callback, fmt.Errorf("callback data is empty")
	}
	var data BinancePayOrderData
	if err := json.Unmarshal([]byte(callback.Data), &data); err != nil {
		return empty, callback, fmt.Errorf("decode callback data: %w", err)
	}
	data.MerchantTradeNo = strings.TrimSpace(data.MerchantTradeNo)
	if data.MerchantTradeNo == "" {
		return empty, callback, fmt.Errorf("callback merchantTradeNo is empty")
	}
	return data, callback, nil
}

// BinancePayPaid 判断回调是否代表成功收款。非成功状态一律不入账。
func BinancePayPaid(callback BinancePayCallback) bool {
	return strings.EqualFold(strings.TrimSpace(callback.BizType), binancePayBizType) &&
		strings.EqualFold(strings.TrimSpace(callback.BizStatus), binancePayStatusOK)
}

// BinancePayNonceKey 把回调 nonce 转成幂等键。币安会重复推送同一笔，
// 用 nonce 去重比用订单号更稳（同一订单可能有多条状态变更推送）。
func BinancePayNonceKey(merchantID, nonce string) string {
	return "binancepay:nonce:" + strings.TrimSpace(merchantID) + ":" + strings.TrimSpace(nonce)
}

// QueryBinancePayOrder 主动查单。webhook 丢失时靠它兜底，是「推送 + 拉取」双保险。
func QueryBinancePayOrder(ctx context.Context, cfg BinancePayConfig, secretKey, apiKey, merchantTradeNo string) (BinancePayOrderData, error) {
	var empty BinancePayOrderData
	if strings.TrimSpace(cfg.MerchantID) == "" {
		return empty, errors.New("binance pay merchant id is not configured")
	}
	body, err := json.Marshal(map[string]string{"merchantTradeNo": strings.TrimSpace(merchantTradeNo)})
	if err != nil {
		return empty, err
	}
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	nonce := binancePayNonce()
	signature, err := BinancePaySignature(secretKey, timestamp, nonce, body)
	if err != nil {
		return empty, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, binancePayBaseURL+binancePayOrderPath, strings.NewReader(string(body)))
	if err != nil {
		return empty, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("BinancePay-Timestamp", timestamp)
	request.Header.Set("BinancePay-Nonce", nonce)
	request.Header.Set("BinancePay-Certificate-Snsserial", strings.TrimSpace(cfg.CertSerial))
	request.Header.Set("BinancePay-Signature", signature)
	if strings.TrimSpace(apiKey) != "" {
		request.Header.Set("X-MBX-APIKEY", strings.TrimSpace(apiKey))
	}
	response, err := httpclient.Outbound(15 * time.Second).Do(request)
	if err != nil {
		return empty, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return empty, fmt.Errorf("binance pay returned status %d", response.StatusCode)
	}
	var payload struct {
		Status string `json:"status"`
		Code   string `json:"code"`
		Data   struct {
			MerchantTradeNo string `json:"merchantTradeNo"`
			TransactionID   string `json:"transactionId"`
			OrderAmount     string `json:"orderAmount"`
			Currency        string `json:"currency"`
			TransactTime    int64  `json:"transactTime"`
			OrderStatus     string `json:"orderStatus"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return empty, fmt.Errorf("decode order query: %w", err)
	}
	if !strings.EqualFold(payload.Status, "SUCCESS") {
		return empty, fmt.Errorf("binance pay order query failed: code=%s status=%s", payload.Code, payload.Status)
	}
	// 查单只认已支付状态，其他状态原样返回由调用方决定。
	if !strings.EqualFold(payload.Data.OrderStatus, "PAID") {
		return empty, ErrBinancePayNotPaid
	}
	return BinancePayOrderData{
		MerchantTradeNo: payload.Data.MerchantTradeNo,
		TransactionID:   payload.Data.TransactionID,
		OrderAmount:     payload.Data.OrderAmount,
		Currency:        payload.Data.Currency,
		TransactTime:    payload.Data.TransactTime,
	}, nil
}

// sameBinancePayAmount 复用精确小数比较，避免 "50.41" 与 "50.4100" 被当成不等。
func sameBinancePayAmount(left, right string) bool {
	a, err := decimalvalue.ParseNonNegative(strings.TrimSpace(left))
	if err != nil {
		return false
	}
	b, err := decimalvalue.ParseNonNegative(strings.TrimSpace(right))
	return err == nil && a.Cmp(b) == 0
}

// binancePayNonce 生成请求随机串。用 crypto/rand 而不是时间戳，
// 避免同一毫秒内两次请求拿到相同 nonce 被对方判成重放。
func binancePayNonce() string {
	buf := make([]byte, 16)
	if _, err := crand.Read(buf); err != nil {
		// 随机源不可用时退回时间戳，宁可降低唯一性也不能让下单直接失败。
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(buf)
}
