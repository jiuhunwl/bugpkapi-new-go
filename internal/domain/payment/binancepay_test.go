package payment

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// 签名公式一个字节错位就全盘失效，这里钉死官方规则：
// payload = timestamp + "\n" + nonce + "\n" + body + "\n"
func TestBinancePaySignatureMatchesOfficialPayloadLayout(t *testing.T) {
	const secret = "test-secret-key"
	body := []byte(`{"bizType":"PAY","bizStatus":"PAY_SUCCESS","data":"{}"}`)
	got, err := BinancePaySignature(secret, "1700000000000", "nonce-abc", body)
	if err != nil {
		t.Fatal(err)
	}
	// 结尾换行和分隔换行都不能少，否则签名与币安侧不一致。
	manual, err := BinancePaySignature(secret, "1700000000000", "nonce-abc", append(body, '\n'))
	if err != nil {
		t.Fatal(err)
	}
	if got == manual {
		t.Fatal("trailing newline must be part of the signature payload; helper double-counts it")
	}
	if _, err := BinancePaySignature("", "1", "n", body); !errors.Is(err, ErrPaymentSecretMissing) {
		t.Fatalf("empty secret must be rejected: %v", err)
	}
}

// body 必须按原始字节验签：重新序列化 JSON 会改变顺序或空格，
// 签名就对不上。这里用带空格的原始 body 验证。
func TestVerifyBinancePayCallbackUsesRawBody(t *testing.T) {
	const secret = "test-secret-key"
	raw := []byte(`{"bizType": "PAY",  "bizStatus": "PAY_SUCCESS", "data": "{\"merchantTradeNo\":\"BP-1\",\"orderAmount\":\"50.41\",\"currency\":\"USDT\"}"}`)
	ts := "1700000000000"
	nonce := "nonce-1"
	sig, err := BinancePaySignature(secret, ts, nonce, raw)
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1700000000000)
	data, callback, err := VerifyBinancePayCallback(secret, ts, nonce, sig, raw, now)
	if err != nil {
		t.Fatalf("valid callback rejected: %v", err)
	}
	if data.MerchantTradeNo != "BP-1" || data.OrderAmount != "50.41" {
		t.Fatalf("data projection wrong: %+v", data)
	}
	if !BinancePayPaid(callback) {
		t.Fatalf("paid callback not recognised: %+v", callback)
	}
}

func TestVerifyBinancePayCallbackRejectsTampering(t *testing.T) {
	const secret = "test-secret-key"
	raw := []byte(`{"bizType":"PAY","bizStatus":"PAY_SUCCESS","data":"{\"merchantTradeNo\":\"BP-1\"}"}`)
	ts, nonce := "1700000000000", "nonce-1"
	sig, err := BinancePaySignature(secret, ts, nonce, raw)
	if err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(1700000000000)

	// 篡改 body：金额被改大
	tampered := []byte(`{"bizType":"PAY","bizStatus":"PAY_SUCCESS","data":"{\"merchantTradeNo\":\"BP-1\",\"orderAmount\":\"9999\"}"}`)
	if _, _, err := VerifyBinancePayCallback(secret, ts, nonce, sig, tampered, now); !errors.Is(err, ErrBinancePaySignature) {
		t.Fatalf("tampered body accepted: %v", err)
	}
	// 错误密钥
	if _, _, err := VerifyBinancePayCallback("other-secret", ts, nonce, sig, raw, now); !errors.Is(err, ErrBinancePaySignature) {
		t.Fatalf("wrong secret accepted: %v", err)
	}
	// 重放：时间戳超出窗口
	if _, _, err := VerifyBinancePayCallback(secret, ts, nonce, sig, raw, now.Add(binancePayMaxSkew+time.Minute)); !errors.Is(err, ErrBinancePayReplay) {
		t.Fatalf("stale callback accepted: %v", err)
	}
	// 空 body
	if _, _, err := VerifyBinancePayCallback(secret, ts, nonce, sig, nil, now); err == nil {
		t.Fatal("empty body accepted")
	}
}

// 只有 PAY + PAY_SUCCESS 才代表收到钱；其他状态绝不能入账。
func TestBinancePayPaidOnlyAcceptsSuccess(t *testing.T) {
	cases := []struct {
		bizType, status string
		want            bool
	}{
		{"PAY", "PAY_SUCCESS", true},
		{"PAY", "pay_success", true},
		{"PAY", "PAY_CLOSED", false},
		{"PAY", "PAY_FAIL", false},
		{"REFUND", "PAY_SUCCESS", false},
		{"", "", false},
	}
	for _, c := range cases {
		got := BinancePayPaid(BinancePayCallback{BizType: c.bizType, BizStatus: c.status})
		if got != c.want {
			t.Fatalf("%s/%s -> %v, want %v", c.bizType, c.status, got, c.want)
		}
	}
}

func TestBinancePayNonceKeyIsScopedPerMerchant(t *testing.T) {
	a := BinancePayNonceKey("merchant-a", "nonce-1")
	b := BinancePayNonceKey("merchant-b", "nonce-1")
	if a == b {
		t.Fatal("nonce key must be scoped per merchant, otherwise two merchants collide")
	}
	if !strings.Contains(a, "nonce-1") {
		t.Fatalf("nonce lost in key: %s", a)
	}
}

func TestSameBinancePayAmountIgnoresScale(t *testing.T) {
	if !sameBinancePayAmount("50.41", "50.4100") {
		t.Fatal("equal amounts with different scale must compare equal")
	}
	if sameBinancePayAmount("50.41", "50.42") {
		t.Fatal("different amounts must not compare equal")
	}
	if sameBinancePayAmount("", "50.41") {
		t.Fatal("empty amount must not compare equal")
	}
}

// 回调 data 是 JSON 字符串而不是对象，解析失败必须报错而不是静默放过。
func TestVerifyBinancePayCallbackRejectsBrokenData(t *testing.T) {
	const secret = "s"
	raw := []byte(`{"bizType":"PAY","bizStatus":"PAY_SUCCESS","data":"not-json"}`)
	ts, nonce := "1700000000000", "n"
	sig, err := BinancePaySignature(secret, ts, nonce, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := VerifyBinancePayCallback(secret, ts, nonce, sig, raw, time.UnixMilli(1700000000000)); err == nil {
		t.Fatal("broken data accepted")
	}
	// data 为空同样拒绝：没有 merchantTradeNo 就无法定位订单。
	empty := []byte(`{"bizType":"PAY","bizStatus":"PAY_SUCCESS","data":""}`)
	sig2, _ := BinancePaySignature(secret, ts, nonce, empty)
	if _, _, err := VerifyBinancePayCallback(secret, ts, nonce, sig2, empty, time.UnixMilli(1700000000000)); err == nil {
		t.Fatal("empty data accepted")
	}
}

func TestBinancePayCallbackPayloadShape(t *testing.T) {
	// 币安推送是「外层 JSON + data 为字符串」的双层结构，用真实样例固定这个形状。
	raw := []byte(`{"bizType":"PAY","bizStatus":"PAY_SUCCESS","data":"{\"merchantTradeNo\":\"BP20261113001\",\"transactionId\":\"1234567890\",\"orderAmount\":\"50.41\",\"currency\":\"USDT\",\"transactTime\":1794530000000}"}`)
	var outer BinancePayCallback
	if err := json.Unmarshal(raw, &outer); err != nil {
		t.Fatal(err)
	}
	var data BinancePayOrderData
	if err := json.Unmarshal([]byte(outer.Data), &data); err != nil {
		t.Fatal(err)
	}
	if data.MerchantTradeNo != "BP20261113001" || data.Amount() != "50.41" {
		t.Fatalf("double-layer payload not decoded: %+v", data)
	}
}
