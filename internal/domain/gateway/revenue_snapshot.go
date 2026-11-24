package gateway

import (
	"bugpk-api/internal/domain/revenue"
	"bugpk-api/internal/pkg/masking"
	"context"
	"errors"
	"go.uber.org/zap"
	"sync"
	"time"
)

type RevenueSink interface {
	NotifyConfirmed() bool
}

const revenueFlushTimeout = 2 * time.Second

// RevenueSnapshot schedules durable ledger work. Notifications are hints only;
// process restarts and lost hints cannot lose or duplicate confirmed revenue.
type RevenueSnapshot struct {
	projector        RevenueProjector
	mu               sync.Mutex
	wake             chan struct{}
	log              *zap.Logger
	lastErrorLog     time.Time
	started, closing bool
	runCtx           context.Context
	drainCtx         context.Context
	closeErr         error
	stop, done       chan struct{}
	flushGate        chan struct{}
}
type RevenueProjector interface {
	Process(context.Context, int) (int, error)
}

func NewRevenueSnapshot(projector RevenueProjector, logs ...*zap.Logger) *RevenueSnapshot {
	log := zap.NewNop()
	if len(logs) > 0 && logs[0] != nil {
		log = logs[0]
	}
	return &RevenueSnapshot{projector: projector, log: log, wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}), flushGate: make(chan struct{}, 1)}
}
func (s *RevenueSnapshot) NotifyConfirmed() bool {
	if s == nil || s.projector == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || (s.runCtx != nil && s.runCtx.Err() != nil) {
		return false
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
	return true
}
func (s *RevenueSnapshot) refresh(ctx context.Context) {
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if closing {
		return
	}
	if err := s.Flush(ctx); err != nil && !errors.Is(err, context.Canceled) {
		s.mu.Lock()
		now := time.Now()
		log := s.log
		report := now.Sub(s.lastErrorLog) >= 30*time.Second
		if report {
			s.lastErrorLog = now
		}
		s.mu.Unlock()
		if report {
			log.Error("revenue projection refresh failed", zap.String("error", masking.Error(err)))
		}
	}
}

// Start is idempotent, and cannot restart an already closed snapshot.
func (s *RevenueSnapshot) Start(ctx context.Context, interval time.Duration) *RevenueSnapshot {
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = time.Second
	}
	s.mu.Lock()
	if s.started || s.closing {
		s.mu.Unlock()
		return s
	}
	s.started = true
	s.runCtx = ctx
	runCtx := s.runCtx
	s.mu.Unlock()
	go s.run(runCtx, interval)
	return s
}
func (s *RevenueSnapshot) run(ctx context.Context, interval time.Duration) {
	s.refresh(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.requestStop(context.Background())
			s.finish()
			return
		case <-s.stop:
			s.finish()
			return
		case <-ticker.C:
			s.refresh(ctx)
		case <-s.wake:
			s.refresh(ctx)
		}
	}
}

// requestStop freezes acceptance. Let an already-running bounded SQL write
// finish rather than cancelling it mid-commit and introducing a needless retry.
func (s *RevenueSnapshot) requestStop(ctx context.Context) {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	s.closing = true
	s.drainCtx = ctx
	startFinal := !s.started
	s.started = true
	close(s.stop)
	s.mu.Unlock()
	if startFinal {
		go s.finish()
	}
}
func (s *RevenueSnapshot) finish() {
	s.mu.Lock()
	ctx := s.drainCtx
	s.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	err := s.Flush(ctx)
	s.mu.Lock()
	s.closeErr = err
	s.mu.Unlock()
	close(s.done)
}

// Close keeps the old API but now reports an incomplete/failed final flush.
func (s *RevenueSnapshot) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.CloseContext(ctx)
}
func (s *RevenueSnapshot) CloseContext(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.requestStop(ctx)
	select {
	case <-s.done:
		s.mu.Lock()
		err := s.closeErr
		s.mu.Unlock()
		return err
	default:
	}
	select {
	case <-s.done:
		s.mu.Lock()
		err := s.closeErr
		s.mu.Unlock()
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Flush advances a bounded number of transactional batches. Remaining work is
// durable and is included in exact reads; shutdown never needs an in-memory sum.
func (s *RevenueSnapshot) Flush(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, revenueFlushTimeout)
	defer cancel()
	select {
	case s.flushGate <- struct{}{}:
		defer func() { <-s.flushGate }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for batch := 0; batch < 16; batch++ {
		processed, err := s.projector.Process(ctx, revenue.DefaultBatchSize)
		if err != nil {
			return err
		}
		if processed < revenue.DefaultBatchSize {
			return nil
		}
	}
	return nil
}
