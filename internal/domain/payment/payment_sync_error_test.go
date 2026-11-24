package payment

import (
	"errors"
	"strings"
	"testing"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// 渠道「查无此单」必须是「未支付」，不能升级成 5xx：
// 管理员同步一笔从未在渠道建单的订单，看到的是订单当前状态，而不是「服务暂时不可用」。
func TestParseAlipayQueryResponseTreatsMissingTradeAsUnpaid(t *testing.T) {
	for _, body := range []string{
		`{"alipay_trade_query_response":{"code":"40004","msg":"Business Failed","sub_code":"ACQ.TRADE_NOT_EXIST","sub_msg":"交易不存在"}}`,
		`{"alipay_trade_query_response":{"code":"40004","msg":"Business Failed","sub_msg":"交易不存在"}}`,
	} {
		status, err := parseAlipayQueryResponse([]byte(body))
		if err != nil {
			t.Fatalf("missing trade must not be an error, got %v for %s", err, body)
		}
		if status.Success || status.TradeStatus != paymentTradeNotFoundState {
			t.Fatalf("unexpected parsed status: %+v", status)
		}
	}
}

// 白名单之外的渠道业务失败仍然是错误，避免把真实故障当成「未支付」静默吞掉。
func TestParseAlipayQueryResponseKeepsOtherFailuresAsErrors(t *testing.T) {
	_, err := parseAlipayQueryResponse([]byte(`{
		"alipay_trade_query_response":{"code":"40004","msg":"Business Failed","sub_msg":"buyer not found"}
	}`))
	if err == nil || !strings.Contains(err.Error(), "buyer not found") {
		t.Fatalf("expected query failure message, got %v", err)
	}
}

func TestWechatQueryErrorClassifiesResourceNotExist(t *testing.T) {
	notFound := parseWechatError([]byte(`{"code":"RESOURCE_NOT_EXIST","message":"订单不存在或该订单不属于当前商户"}`))
	if !isWechatTradeNotFound(notFound) {
		t.Fatalf("expected RESOURCE_NOT_EXIST to classify as missing trade, got %v", notFound)
	}
	if isWechatTradeNotFound(parseWechatError([]byte(`{"code":"FREQUENCY_LIMIT_EXCEED","message":"接口调用超过限制"}`))) {
		t.Fatal("rate limiting must stay a channel failure")
	}
	if isWechatTradeNotFound(errors.New("context deadline exceeded")) {
		t.Fatal("non-wechat errors must not be reclassified")
	}
}

func TestIsPaymentTradeNotFoundSignal(t *testing.T) {
	for _, text := range []string{"订单不存在", "交易不存在", "RESOURCE_NOT_EXIST", "ACQ.TRADE_NOT_EXIST"} {
		if !isPaymentTradeNotFoundSignal(text) {
			t.Fatalf("expected %q to mean missing trade", text)
		}
	}
	for _, text := range []string{"签名错误", "appid 无效", ""} {
		if isPaymentTradeNotFoundSignal(text) {
			t.Fatalf("did not expect %q to mean missing trade", text)
		}
	}
}

// 已结束订单不再打渠道查单：结果已经定论，查单只会把渠道报错透出给操作者。
func TestSyncPaymentOrderSkipsTerminalOrders(t *testing.T) {
	s := &Service{}
	for _, status := range []string{"CLOSED", "FAILED", "closed"} {
		_, err := s.syncPaymentOrder(t.Context(), PaymentOrder{OrderNo: "BP1", PayChannel: ChannelAlipay, Status: status}, 0, true)
		business, ok := apperrors.AsBusinessError(err)
		if !ok || business.StatusCode != consts.StatusConflict {
			t.Fatalf("status %s: expected 409 business error, got %v", status, err)
		}
	}
	// CANCELLED 只是本地取消：渠道侧旧支付串仍然可用，用户扫旧码付的钱必须还能查单结算。
	if isSyncTerminalStatus("CANCELLED") {
		t.Fatal("cancelled orders must stay syncable so a late payment still settles")
	}
	order := PaymentOrder{OrderNo: "BP1", PayChannel: ChannelAlipay, Status: "PAID"}
	got, err := s.syncPaymentOrder(t.Context(), order, 0, true)
	if err != nil || got.Status != "PAID" {
		t.Fatalf("paid order must return unchanged, got %+v err %v", got, err)
	}
}

// 渠道配置结论降级为 409 才能被业务文案通道显示；其余 5xx 继续走统一掩码。
func TestAsChannelConfigIssueOnlyDowngradesKnownMessages(t *testing.T) {
	for _, message := range []string{"支付宝支付未启用", "微信支付未启用", "虎皮椒 appsecret 未配置"} {
		err := asChannelConfigIssue(apperrors.New(consts.StatusServiceUnavailable, message))
		business, ok := apperrors.AsBusinessError(err)
		if !ok || business.StatusCode != consts.StatusConflict || business.Message != message {
			t.Fatalf("expected %q to surface as 409, got %+v", message, err)
		}
	}
	secret := apperrors.New(consts.StatusServiceUnavailable, "PAYMENT_CONFIG_SECRET 未配置")
	if err := asChannelConfigIssue(secret); err != secret {
		t.Fatalf("unknown 5xx must stay masked, got %v", err)
	}
	plain := errors.New("dial tcp: i/o timeout")
	if err := asChannelConfigIssue(plain); err != plain {
		t.Fatalf("non-business errors must pass through, got %v", err)
	}
}
