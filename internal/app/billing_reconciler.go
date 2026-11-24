package app

import (
	"context"
	"database/sql"
	"time"

	"bugpk-api/internal/domain/gateway"
	"bugpk-api/internal/pkg/masking"

	"go.uber.org/zap"
)

const (
	billingReconcileBatchSize  = 100
	billingReconcileInterval   = time.Minute
	billingReconcileMaxBatches = 10
	billingReservationMaxAge   = 5 * time.Minute
	billingReconcileTimeout    = 30 * time.Second
)

func startBillingReconciler(parent context.Context, db *sql.DB, log *zap.Logger) (context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	if log == nil {
		log = zap.NewNop()
	}
	repo := gateway.NewRepository(db)
	go func() {
		defer close(done)
		reconcileStaleBilling(ctx, repo, log)
		ticker := time.NewTicker(billingReconcileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				reconcileStaleBilling(ctx, repo, log)
			}
		}
	}()
	return cancel, done
}

func reconcileStaleBilling(parent context.Context, repo *gateway.Repository, log *zap.Logger) {
	ctx, cancel := context.WithTimeout(parent, billingReconcileTimeout)
	defer cancel()
	total := 0
	for batch := 0; batch < billingReconcileMaxBatches; batch++ {
		released, err := repo.ReconcileStaleBilling(ctx, time.Now().Add(-billingReservationMaxAge), billingReconcileBatchSize)
		total += released
		if err != nil {
			log.Error("stale billing reconciliation failed", zap.Int("released", total), zap.String("error", masking.Error(err)))
			return
		}
		if released < billingReconcileBatchSize {
			break
		}
	}
	if total > 0 {
		log.Warn("stale billing reservations released", zap.Int("released", total))
	}
}
