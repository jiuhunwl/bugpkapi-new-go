package payment

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// 币安 Pay 回调的入库与结算。
//
// 这里只做三件事，且顺序不能变：
//  1. 验签（服务层已完成，这里拿到的 data 一定是币安签过名的）；
//  2. 用 merchantTradeNo 定位订单并做金额/币种复核；
//  3. 走与其它通道相同的结算事务入账。
//
// 任何一步不满足都返回错误，让币安重推——绝不「先记下来再说」。

// BinancePayConfig 读取商户配置（secretKey 已解密）。
func (s *Service) binancePaySecret(ctx context.Context) (BinancePayConfig, string, string, error) {
	stored, err := s.repo.BinancePayConfig(ctx)
	if err != nil {
		return BinancePayConfig{}, "", "", err
	}
	if !stored.Enabled {
		return stored.BinancePayConfig, "", "", errors.New("binance pay channel is disabled")
	}
	if strings.TrimSpace(stored.SecretKeyCipher) == "" {
		return stored.BinancePayConfig, "", "", ErrPaymentSecretMissing
	}
	if s.cipher == nil {
		return stored.BinancePayConfig, "", "", ErrPaymentSecretMissing
	}
	secret, err := s.cipher.Decrypt(stored.SecretKeyCipher)
	if err != nil {
		return stored.BinancePayConfig, "", "", err
	}
	apiKey := ""
	if strings.TrimSpace(stored.APIKeyCipher) != "" {
		if apiKey, err = s.cipher.Decrypt(stored.APIKeyCipher); err != nil {
			return stored.BinancePayConfig, "", "", err
		}
	}
	return stored.BinancePayConfig, secret, apiKey, nil
}

// HandleBinancePayNotify 处理币安推送。返回 (是否已入账, 错误)。
// 返回错误时 handler 会回非 200，触发币安重推。
func (s *Service) HandleBinancePayNotify(ctx context.Context, body []byte, headers map[string]string) (bool, error) {
	stored, secret, _, err := s.binancePaySecret(ctx)
	if err != nil {
		return false, err
	}
	data, callback, err := VerifyBinancePayCallback(
		secret,
		headers["BinancePay-Timestamp"],
		headers["BinancePay-Nonce"],
		headers["BinancePay-Signature"],
		body,
		time.Now(),
	)
	if err != nil {
		// 验签失败一律当作伪造回调，不入账也不泄漏细节。
		return false, apperrors.New(consts.StatusBadRequest, "币安支付通知验签失败")
	}
	// 非成功状态也要回 200：否则币安会对一个永远不会成功的状态反复重推。
	if !BinancePayPaid(callback) {
		return false, nil
	}
	// nonce 幂等：币安会重复推送同一笔，重复推送必须被识别为「已处理」而不是再入一次账。
	consumed, err := s.repo.ConsumeBinancePayNonce(ctx, BinancePayNonceKey(stored.MerchantID, headers["BinancePay-Nonce"]))
	if err != nil {
		return false, err
	}
	if !consumed {
		// 已经处理过：仍然返回成功，让币安停止重推。
		return true, nil
	}
	if err := s.settleBinancePayOrder(ctx, data); err != nil {
		return false, err
	}
	return true, nil
}

// settleBinancePayOrder 按推送数据结算订单，金额与币种必须与订单一致。
func (s *Service) settleBinancePayOrder(ctx context.Context, data BinancePayOrderData) error {
	order, err := s.repo.OrderByNo(ctx, data.MerchantTradeNo, 0, true)
	if err != nil {
		return err
	}
	if !strings.EqualFold(order.PayChannel, ChannelBinancePay) {
		return fmt.Errorf("order %s is not a binance pay order", data.MerchantTradeNo)
	}
	// 已入账且交易号一致 = 重复推送，直接成功。
	if order.Status == "PAID" {
		if strings.EqualFold(order.ChainTxID, data.TransactionID) || data.TransactionID == "" {
			return nil
		}
		return fmt.Errorf("order %s already settled with another transaction", data.MerchantTradeNo)
	}
	// 金额是最后一道闸：币安说收了多少，就必须等于订单要多少。
	if !sameBinancePayAmount(data.Amount(), order.PayAmount) {
		return fmt.Errorf("%w: received %s, want %s", ErrBinancePayAmount, data.Amount(), order.PayAmount)
	}
	if data.Currency != "" && !strings.EqualFold(data.Currency, order.Currency) {
		return fmt.Errorf("binance pay currency %s does not match order currency %s", data.Currency, order.Currency)
	}
	if err := s.repo.SettleBinancePayOrder(ctx, order.OrderNo, PayTradeStatus{
		TradeNo: data.TransactionID,
		Total:   data.Amount(),
		Success: true,
	}); err != nil {
		return err
	}
	s.notifyPaymentPaid(ctx, order.OrderNo)
	return nil
}

// SyncBinancePayOrder 主动查单兜底：webhook 丢失时用它把订单补上。
func (s *Service) SyncBinancePayOrder(ctx context.Context, orderNo string) (PaymentOrder, error) {
	stored, secret, apiKey, err := s.binancePaySecret(ctx)
	if err != nil {
		return PaymentOrder{}, err
	}
	order, err := s.repo.OrderByNo(ctx, strings.TrimSpace(orderNo), 0, true)
	if err != nil {
		return PaymentOrder{}, err
	}
	if !strings.EqualFold(order.PayChannel, ChannelBinancePay) {
		return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "该订单不是币安支付订单")
	}
	if order.Status == "PAID" {
		return order, nil
	}
	data, err := QueryBinancePayOrder(ctx, stored, secret, apiKey, order.OrderNo)
	if errors.Is(err, ErrBinancePayNotPaid) {
		return order, nil
	}
	if err != nil {
		return PaymentOrder{}, err
	}
	if err := s.settleBinancePayOrder(ctx, data); err != nil {
		return PaymentOrder{}, err
	}
	return s.repo.OrderByNo(ctx, order.OrderNo, 0, true)
}
