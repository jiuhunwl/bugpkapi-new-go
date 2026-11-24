package notification

import (
	"context"
	"errors"
	"time"

	"bugpk-api/internal/pkg/background"
	"bugpk-api/internal/pkg/masking"
	"go.uber.org/zap"
)

// Preserve the existing worker API while sharing cancellation/close semantics.
type Worker = background.Worker

// Final status updates must finish within the application's shutdown budget,
// including time spent waiting for a database connection.
const deliveryFinalizeTimeout = 2 * time.Second

func (s *Service) Start(ctx context.Context, interval time.Duration) *Worker {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return background.Start(ctx, func(workerCtx context.Context) { s.run(workerCtx, interval) })
}

func (s *Service) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		s.processBatch(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) processBatch(ctx context.Context) {
	if s == nil || s.repo == nil || ctx.Err() != nil {
		return
	}
	entries, err := s.repo.Claim(ctx, 10)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			s.logger.Error("claim notification outbox failed", zap.String("error", masking.Error(err)))
		}
		return
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}
		s.finalizeDelivery(ctx, entry, s.deliver(ctx, entry))
	}
}

func (s *Service) finalizeDelivery(ctx context.Context, entry Entry, deliveryErr error) {
	// SMTP can finish after shutdown cancellation. Persist that outcome without
	// inheriting cancellation, but never allow an unbounded database wait.
	finalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), deliveryFinalizeTimeout)
	defer cancel()
	if deliveryErr != nil {
		nextAttemptAt := time.Now().Add(nextAttemptDelay(entry.Attempts))
		if markErr := s.repo.MarkRetry(finalCtx, entry, deliveryErr, nextAttemptAt, isPermanent(deliveryErr)); markErr != nil {
			s.logger.Error("record notification retry failed", zap.Int64("outbox_id", entry.ID), zap.String("error", masking.Error(markErr)))
		}
		s.logger.Warn("notification delivery failed", zap.Int64("outbox_id", entry.ID), zap.String("template", entry.TemplateKey), zap.Int("attempt", entry.Attempts), zap.String("error", masking.Error(deliveryErr)))
		return
	}
	if err := s.repo.MarkSent(finalCtx, entry.ID); err != nil {
		s.logger.Error("mark notification sent failed", zap.Int64("outbox_id", entry.ID), zap.String("error", masking.Error(err)))
	}
}
