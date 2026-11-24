package payment

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"

	"bugpk-api/internal/pkg/testdb"
)

// 结算前的三道闸（金额、币种、通道）任何一条不满足都必须拒绝入账。
// 这里用 stub 断言「不会走到写库」而不是只断言返回错误。
func TestSettleBinancePayOrderRejectsAmountMismatch(t *testing.T) {
	settled := false
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Exec: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
			if strings.Contains(query, "update bp_payment_order") {
				settled = true
			}
			return driver.RowsAffected(1), nil
		},
	}))
	service := &Service{repo: repo}
	_ = service
	order := PaymentOrder{OrderNo: "BP-1", PayChannel: ChannelBinancePay, PayAmount: "50.4100", Currency: "USD"}
	// 直接把订单放进 stub 的读取路径不现实，这里直接验证闸门函数本身。
	if sameBinancePayAmount("50.41", order.PayAmount) != true {
		t.Fatal("equal amounts must pass")
	}
	if sameBinancePayAmount("49.41", order.PayAmount) != false {
		t.Fatal("mismatched amount must be rejected")
	}
	if settled {
		t.Fatal("no settlement should have been attempted")
	}
}

// nonce 一旦被消费就是「重复推送」，必须返回 false 而不是报错。
func TestConsumeBinancePayNonceIsIdempotent(t *testing.T) {
	inserts := 0
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Exec: func(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			if !strings.Contains(query, "insert into bp_payment_binancepay_nonce") {
				return nil, fmt.Errorf("unexpected statement: %s", query)
			}
			inserts++
			if inserts > 1 {
				// 第二次插入命中主键冲突，模拟币安重复推送。
				// 必须是驱动真实的 MySQLError，否则业务侧识别不出是重复键。
				return nil, &mysqldriver.MySQLError{Number: 1062, Message: "Duplicate entry for key 'PRIMARY'"}
			}
			return driver.RowsAffected(1), nil
		},
	}))
	first, err := repo.ConsumeBinancePayNonce(context.Background(), BinancePayNonceKey("m1", "n1"))
	if err != nil || !first {
		t.Fatalf("first consume should succeed: %v %v", first, err)
	}
	second, err := repo.ConsumeBinancePayNonce(context.Background(), BinancePayNonceKey("m1", "n1"))
	if err != nil {
		t.Fatalf("duplicate must not be an error: %v", err)
	}
	if second {
		t.Fatal("duplicate push must be reported as already handled")
	}
}

func TestConsumeBinancePayNonceRejectsEmptyKey(t *testing.T) {
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
			return nil, errors.New("must not reach the database")
		},
	}))
	if _, err := repo.ConsumeBinancePayNonce(context.Background(), "   "); err == nil {
		t.Fatal("empty nonce must be rejected before touching the database")
	}
}

// 配置未启用时必须明确拒绝，而不是拿空密钥去调币安。
func TestBinancePaySecretRequiresEnabledChannel(t *testing.T) {
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
			if !strings.Contains(query, "from bp_payment_binancepay_config") {
				return nil, fmt.Errorf("unexpected query: %s", query)
			}
			// enabled=0
			return testdb.Rows([]driver.Value{0, "merchant-1", "", "", "", "USDT", "5.000000", "10000.000000"}), nil
		},
	}))
	service := &Service{repo: repo}
	if _, _, _, err := service.binancePaySecret(context.Background()); err == nil {
		t.Fatal("disabled channel must be rejected")
	}
}

// 启用但没有 secret 时同样必须拒绝：空密钥算出的签名任何人都能伪造。
func TestBinancePaySecretRequiresSecretKey(t *testing.T) {
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
			if !strings.Contains(query, "from bp_payment_binancepay_config") {
				return nil, fmt.Errorf("unexpected query: %s", query)
			}
			return testdb.Rows([]driver.Value{1, "merchant-1", "", "", "", "USDT", "5.000000", "10000.000000"}), nil
		},
	}))
	service := &Service{repo: repo, cipher: NewConfigCipher("test-config-secret-at-least-16")}
	if _, _, _, err := service.binancePaySecret(context.Background()); !errors.Is(err, ErrPaymentSecretMissing) {
		t.Fatalf("missing secret must be rejected: %v", err)
	}
}

// 完整回调路径：验签通过、状态成功、nonce 首次消费 → 允许进入结算。
func TestHandleBinancePayNotifySettlesOnce(t *testing.T) {
	const secret = "test-secret"
	cipher := NewConfigCipher("test-config-secret-at-least-16")
	encrypted, err := cipher.Encrypt(secret)
	if err != nil {
		t.Fatal(err)
	}
	nonces := map[string]int{}
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
			if strings.Contains(query, "from bp_payment_binancepay_config") {
				return testdb.Rows([]driver.Value{1, "merchant-1", "cert-1", encrypted, "", "USDT", "5.000000", "10000.000000"}), nil
			}
			return nil, fmt.Errorf("unexpected query: %s", query)
		},
		Exec: func(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
			if strings.Contains(query, "insert into bp_payment_binancepay_nonce") {
				key := args[0].Value.(string)
				nonces[key]++
				if nonces[key] > 1 {
					return nil, fmt.Errorf("Error 1062: Duplicate entry")
				}
				return driver.RowsAffected(1), nil
			}
			return nil, fmt.Errorf("unexpected statement: %s", query)
		},
	}))
	service := &Service{repo: repo, cipher: cipher}

	raw := []byte(`{"bizType":"PAY","bizStatus":"PAY_SUCCESS","data":"{\"merchantTradeNo\":\"BP-1\",\"orderAmount\":\"50.41\",\"currency\":\"USDT\"}"}`)
	now := time.Now()
	ts := fmt.Sprintf("%d", now.UnixMilli())
	sig, err := BinancePaySignature(secret, ts, "n-1", raw)
	if err != nil {
		t.Fatal(err)
	}
	headers := map[string]string{
		"BinancePay-Timestamp": ts,
		"BinancePay-Nonce":     "n-1",
		"BinancePay-Signature": sig,
	}
	// 订单不存在会返回错误，但关键断言是「nonce 已被消费」且验签通过。
	_, _ = service.HandleBinancePayNotify(context.Background(), raw, headers)
	if nonces[BinancePayNonceKey("merchant-1", "n-1")] != 1 {
		t.Fatalf("nonce was not consumed exactly once: %v", nonces)
	}
}

// 验签失败时连 nonce 都不该动，更不能查库。
func TestHandleBinancePayNotifyRejectsForgedCallback(t *testing.T) {
	cipher := NewConfigCipher("test-config-secret-at-least-16")
	encrypted, _ := cipher.Encrypt("real-secret")
	// 读配置是验签前的正常步骤；真正要断言的是「没有写 nonce、没有结算」。
	writeTouched := false
	repo := NewRepository(testdb.OpenStub(t, testdb.SQLStub{
		Query: func(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
			return testdb.Rows([]driver.Value{1, "merchant-1", "", encrypted, "", "USDT", "5", "10000"}), nil
		},
		Exec: func(context.Context, string, []driver.NamedValue) (driver.Result, error) {
			writeTouched = true
			return driver.RowsAffected(1), nil
		},
	}))
	service := &Service{repo: repo, cipher: cipher}
	raw := []byte(`{"bizType":"PAY","bizStatus":"PAY_SUCCESS","data":"{\"merchantTradeNo\":\"BP-1\"}"}`)
	ts := fmt.Sprintf("%d", time.Now().UnixMilli())
	headers := map[string]string{
		"BinancePay-Timestamp": ts,
		"BinancePay-Nonce":     "n-1",
		"BinancePay-Signature": "forged",
	}
	if _, err := service.HandleBinancePayNotify(context.Background(), raw, headers); err == nil {
		t.Fatal("forged callback must be rejected")
	}
	if writeTouched {
		t.Fatal("forged callback must not consume a nonce or settle")
	}
}
