package app

import (
	"context"
	"time"

	"bugpk-api/internal/domain/payment"
	"bugpk-api/internal/pkg/masking"

	"go.uber.org/zap"
)

const (
	paymentExpiryInterval   = time.Minute
	paymentExpiryBatchSize  = 500
	paymentExpiryMaxBatches = 20
	paymentExpiryTimeout    = 30 * time.Second
)

// startPaymentExpirySweeper 每分钟把本地已过期的未支付订单标记为 EXPIRED，
// 并释放限量套餐的库存占用。订单是否到账仍以支付通道回调/主动查单为准，
// 过期不会阻止通道后续确认到账时补结算。
func startPaymentExpirySweeper(ctx context.Context, repo *payment.Repository, log *zap.Logger) (context.CancelFunc, <-chan struct{}) {
	if ctx == nil {
		ctx = context.Background()
	}
	if log == nil {
		log = zap.NewNop()
	}
	sweepCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(paymentExpiryInterval)
		defer ticker.Stop()
		for {
			select {
			case <-sweepCtx.Done():
				return
			case <-ticker.C:
				runPaymentExpirySweep(sweepCtx, repo, log)
			}
		}
	}()
	return cancel, done
}

func runPaymentExpirySweep(ctx context.Context, repo *payment.Repository, log *zap.Logger) {
	total := 0
	for batch := 0; batch < paymentExpiryMaxBatches; batch++ {
		timeoutCtx, cancel := context.WithTimeout(ctx, paymentExpiryTimeout)
		count, err := repo.ExpireDueOrders(timeoutCtx, paymentExpiryBatchSize)
		cancel()
		if err != nil {
			log.Warn("payment order expiry sweep failed", zap.String("error", masking.Error(err)))
			return
		}
		total += count
		if count < paymentExpiryBatchSize {
			break
		}
	}
	if total > 0 {
		log.Info("payment orders expired", zap.Int("orders", total))
	}
}
