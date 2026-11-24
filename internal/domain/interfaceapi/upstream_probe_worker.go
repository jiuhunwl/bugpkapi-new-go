package interfaceapi

import (
	"context"
	"fmt"
	"time"

	"bugpk-api/internal/pkg/background"
	"bugpk-api/internal/pkg/masking"
	"go.uber.org/zap"
)

func (s *Service) StartUpstreamProbe(ctx context.Context, interval time.Duration) *background.Worker {
	if s == nil || s.repo == nil || s.rdb == nil || interval <= 0 {
		return nil
	}
	return background.Start(ctx, func(workerCtx context.Context) {
		defer func() {
			if recovered := recover(); recovered != nil && s.log != nil {
				s.log.Error("upstream probe loop panicked", zap.String("error", masking.ErrorText(fmt.Sprint(recovered))))
			}
		}()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for workerCtx.Err() == nil {
			// The first sweep obeys the same deadline and cancellation as later sweeps.
			func() {
				sweepCtx, cancel := context.WithTimeout(workerCtx, interval)
				defer cancel()
				s.runUpstreamProbeOnce(sweepCtx)
			}()
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}
