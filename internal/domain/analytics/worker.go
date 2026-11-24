package analytics

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"sync"
	"time"
)

type Worker struct {
	cancel context.CancelFunc
	done   <-chan struct{}
	once   sync.Once
}

// Start launches one incremental worker. It immediately drains a bounded
// number of batches, then polls. A busy database cannot create overlapping
// iterations because the loop is strictly serial.
func (r *Repository) Start(ctx context.Context, interval time.Duration, batchSize int) *Worker {
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = time.Second
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var maintenance sync.WaitGroup
		maintenance.Add(1)
		go func() { defer maintenance.Done(); r.runSourceAudit(workerCtx) }()
		r.run(workerCtx, interval, batchSize)
		cancel()
		maintenance.Wait()
	}()
	return &Worker{cancel: cancel, done: done}
}

func (w *Worker) Close(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.once.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	if w.done == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// analyticsTickBatchLimit bounds how many batches one tick may drain before it
// yields, so sustained ingestion cannot monopolize a connection.
const analyticsTickBatchLimit = 20

// backlogPollInterval is used instead of the configured interval while the
// worker is still draining history. Polling every second with a small batch
// while millions of rows are queued is what makes a long catch-up compete with
// gateway accounting for the same database; a backlog gets fewer, larger bites
// instead.
const backlogPollInterval = 5 * time.Second

func (r *Repository) run(ctx context.Context, interval time.Duration, batchSize int) {
	baseInterval := interval
	baseBatchSize := batchSize

	r.processAvailable(ctx, batchSize)
	nextPrune := time.Now().Add(riskFingerprintPruneInterval)
	nextSweep := time.Now().Add(datasetSweepInterval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if r.processAvailable(ctx, batchSize) {
				interval = backlogPollInterval
				batchSize = MaxBatchSize
			} else {
				interval = baseInterval
				batchSize = baseBatchSize
			}
			// The configured interval is a floor: while catching up the worker
			// polls less often, never more often than requested.
			if interval < baseInterval {
				interval = baseInterval
			}
			ticker.Reset(interval)

			if !time.Now().Before(nextPrune) {
				nextPrune = time.Now().Add(riskFingerprintPruneInterval)
				r.pruneRiskFingerprintWithTimeout(ctx)
			}
			if !time.Now().Before(nextSweep) {
				nextSweep = time.Now().Add(datasetSweepInterval)
				r.sweepDatasetRetentionWithTimeout(ctx)
			}
		}
	}
}

// processAvailable drains a bounded number of batches and reports whether
// source rows were still waiting when the bound was reached.
func (r *Repository) processAvailable(ctx context.Context, batchSize int) bool {
	for range analyticsTickBatchLimit {
		result, err := r.processBatchWithRetry(ctx, batchSize)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				errorCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
				_ = r.RecordError(errorCtx, err)
				cancel()
			}
			return false
		}
		// CLEAR/FINALIZE repair steps intentionally process no source rows while
		// still advertising HasMore. Continue the bounded serial loop so a repair
		// advances promptly instead of waiting one poll interval per projection.
		if !result.HasMore {
			return false
		}
	}
	return true
}

// processBatchWithRetry gives a single batch one extra attempt after a
// transient connection failure. The MySQL driver reports a read/write timeout
// or a server-closed connection as "invalid connection"; database/sql does not
// auto-retry a statement that fails mid-transaction, so we back off briefly and
// retry once on a fresh connection instead of dropping the whole tick.
func (r *Repository) processBatchWithRetry(ctx context.Context, batchSize int) (BatchResult, error) {
	result, err := r.ProcessBatch(ctx, batchSize)
	if err == nil || !isTransientConnectionError(err) {
		return result, err
	}
	select {
	case <-ctx.Done():
		return result, err
	case <-time.After(retryBackoff):
	}
	return r.ProcessBatch(ctx, batchSize)
}

const retryBackoff = 500 * time.Millisecond

// isTransientConnectionError reports whether the error indicates the underlying
// MySQL connection was lost rather than a query or logic failure. Such errors
// are safe to retry because ProcessBatch re-reads the checkpoint and is
// idempotent by construction.
func isTransientConnectionError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, driver.ErrBadConn) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "invalid connection") ||
		strings.Contains(message, "bad connection") ||
		strings.Contains(message, "connection reset") ||
		strings.Contains(message, "broken pipe")
}
