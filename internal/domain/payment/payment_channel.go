package payment

import (
	"errors"
	"strings"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

var (
	ErrPaymentChannelMismatch    = errors.New("payment channel mismatch")
	ErrUnsupportedPaymentChannel = errors.New("unsupported payment channel")
)

// paymentTradeNotFoundState 是「渠道侧查无此单」的统一内部标记。
// 主动查单遇到它时按「未支付」处理：订单从未在渠道建单，不是服务故障。
const paymentTradeNotFoundState = "TRADE_NOT_FOUND"

// isAlipayTradeNotFound 认支付宝的官方子码，sub_msg 文案会变但子码是契约。
func isAlipayTradeNotFound(subCode, subMsg string) bool {
	return strings.EqualFold(strings.TrimSpace(subCode), "ACQ.TRADE_NOT_EXIST") ||
		strings.TrimSpace(subMsg) == "交易不存在"
}

// isPaymentTradeNotFoundSignal 认渠道返回文本里的「查无此单」标识：
// 微信用 RESTful 错误码 RESOURCE_NOT_EXIST，虎皮椒只给中文 errmsg。
func isPaymentTradeNotFoundSignal(text string) bool {
	upper := strings.ToUpper(text)
	return strings.Contains(upper, "RESOURCE_NOT_EXIST") ||
		strings.Contains(upper, "TRADE_NOT_EXIST") ||
		strings.Contains(text, "交易不存在") ||
		strings.Contains(text, "订单不存在")
}

// isSyncTerminalStatus 列出已经没有查单意义的订单状态：渠道侧不会再有新的支付结果，
// 继续查单只会把渠道报错透出给管理员。
//
// CANCELLED 不在其中：用户取消只是本地动作，虎皮椒/微信的旧支付串在渠道侧依然可用，
// 用户扫旧码付款后必须还能通过查单把钱结算进来。
func isSyncTerminalStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "CLOSED", "FAILED":
		return true
	default:
		return false
	}
}

// isDeletableOrderStatus 是允许从交易记录里删除（软隐藏）的状态：
// PAID 是资金凭证不能删；CREATED/PAYING 仍可支付，删掉会让用户以为不用付款。
func isDeletableOrderStatus(status string) bool {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "EXPIRED", "CLOSED", "CANCELLED", "FAILED":
		return true
	default:
		return false
	}
}

// channelConfigIssueMessages 是渠道配置结论的白名单。
//
// Why: 这些文案原本是 503，会被 response.FromError 统一抹成「服务暂时不可用」，
// 管理员点同步时既看不到原因也无法行动。它们本质是「这条订单当前同步不了」的业务结论，
// 因此只允许白名单内的手写文案降级为 409；其余 5xx（含上游网络、验签、存储故障）
// 继续走统一掩码，避免把内部细节透出。
var channelConfigIssueMessages = map[string]struct{}{
	"支付宝支付未启用":           {},
	"支付宝商户 sellerId 未配置": {},
	"支付宝配置不可用":           {},
	"微信支付未启用":            {},
	"微信 APIv3Key 未配置":    {},
	"微信商户私钥未配置":          {},
	"微信配置不可用":            {},
	"微信平台验签公钥及对应ID未配置":   {},
	"微信平台验签配置不可用":        {},
	"虎皮椒支付未启用":           {},
	"虎皮椒 appsecret 未配置":  {},
	"虎皮椒配置不可用":           {},
}

// asChannelConfigIssue 把白名单内的渠道配置错误改写成同文案的 409 业务错误。
func asChannelConfigIssue(err error) error {
	business, ok := apperrors.AsBusinessError(err)
	if !ok {
		return err
	}
	if _, known := channelConfigIssueMessages[strings.TrimSpace(business.Message)]; !known {
		return err
	}
	return apperrors.New(consts.StatusConflict, strings.TrimSpace(business.Message))
}

func canonicalExternalPaymentChannel(channel string) (string, error) {
	switch strings.ToUpper(channel) {
	case ChannelAlipay:
		return ChannelAlipay, nil
	case ChannelWechat:
		return ChannelWechat, nil
	case ChannelHupijiaoAlipay:
		return ChannelHupijiaoAlipay, nil
	case ChannelHupijiaoWechat:
		return ChannelHupijiaoWechat, nil
	case ChannelTrc20:
		return ChannelTrc20, nil
	case ChannelBep20:
		return ChannelBep20, nil
	default:
		return "", ErrUnsupportedPaymentChannel
	}
}

// isChainDepositChannel 返回「链上充值」通道：没有第三方收银台，
// 到账以链上确认为准，订单过期后仍允许核验到账。
func isChainDepositChannel(channel string) bool {
	return strings.EqualFold(strings.TrimSpace(channel), ChannelTrc20) ||
		strings.EqualFold(strings.TrimSpace(channel), ChannelBep20)
}

func requirePaymentChannel(stored, expected string) error {
	channel, err := canonicalExternalPaymentChannel(expected)
	if err != nil {
		return err
	}
	if !strings.EqualFold(stored, channel) {
		return ErrPaymentChannelMismatch
	}
	return nil
}
