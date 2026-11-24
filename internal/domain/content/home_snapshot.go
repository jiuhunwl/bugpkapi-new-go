package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

const (
	homeAnalyticsSnapshotRefreshInterval = time.Minute
	homeAnalyticsSnapshotRefreshTimeout  = 30 * time.Second
)

// HomeAnalyticsRecord pairs the persisted analytics payload with the freshness
// metadata the truth contract needs. SourceUpdatedAt is the row's own write
// time; only a measured refresh sets it, so a seeded row leaves it NULL and
// there is no way to mistake it for a real measurement.
type HomeAnalyticsRecord struct {
	Snapshot        HomeAnalyticsSnapshot
	SourceUpdatedAt time.Time
	CoveredUpTo     time.Time
}

// LoadHomeAnalyticsSnapshot reads the single persisted analytics row used by
// the public homepage. A seeded row with a NULL source_updated_at is not ready
// yet and is intentionally treated as missing.
func (r *Repository) LoadHomeAnalyticsSnapshot(ctx context.Context) (HomeAnalyticsRecord, bool, error) {
	var payload []byte
	var sourceUpdatedAt time.Time
	err := r.db.QueryRowContext(ctx, `
		select cast(payload as char),source_updated_at
		from bp_home_overview_snapshot
		where id=1 and source_updated_at is not null
	`).Scan(&payload, &sourceUpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return HomeAnalyticsRecord{}, false, nil
	}
	if err != nil {
		return HomeAnalyticsRecord{}, false, err
	}
	var snapshot HomeAnalyticsSnapshot
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return HomeAnalyticsRecord{}, false, fmt.Errorf("decode home analytics snapshot: %w", err)
	}
	// The stored payload must never be trusted for readiness: a verdict written
	// while the worker was healthy would otherwise outlive the condition that
	// justified it. Clear it here and let the live probe stamp it below.
	snapshot.Stats = stripAnalyticsHealth(snapshot.Stats)
	if snapshot.Stats.AvailabilitySource == "" {
		snapshot.Stats.AvailabilitySource = "CALL_SUCCESS"
	}
	if snapshot.HotAPIs == nil {
		snapshot.HotAPIs = []HotAPIItem{}
	}
	return HomeAnalyticsRecord{
		Snapshot:        snapshot,
		SourceUpdatedAt: sourceUpdatedAt,
		CoveredUpTo:     parseCoveredUpTo(snapshot.CoveredUpTo),
	}, true, nil
}

// SaveHomeAnalyticsSnapshot atomically replaces the singleton snapshot. The
// JSON payload is small and one-row reads remain constant-time regardless of
// call-log or rollup cardinality.
//
// The read-time truth envelope is stripped first: readiness and lag are
// properties of the moment a request is served, not of the stored payload.
func (r *Repository) SaveHomeAnalyticsSnapshot(ctx context.Context, snapshot HomeAnalyticsSnapshot) error {
	if snapshot.Stats.AvailabilitySource == "" {
		snapshot.Stats.AvailabilitySource = "CALL_SUCCESS"
	}
	snapshot.Stats = stripAnalyticsHealth(snapshot.Stats)
	if snapshot.HotAPIs == nil {
		snapshot.HotAPIs = []HotAPIItem{}
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return fmt.Errorf("encode home analytics snapshot: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		insert into bp_home_overview_snapshot(id,payload,source_updated_at)
		values (1,?,now(6))
		on duplicate key update
			payload=values(payload),source_updated_at=values(source_updated_at)
	`, payload)
	return err
}

// RefreshHomeAnalyticsSnapshot performs the bounded rollup aggregation off
// the HTTP request path and publishes it as one durable row.
func (s *Service) RefreshHomeAnalyticsSnapshot(ctx context.Context) error {
	// Sample the rollup watermark before aggregating. The rollup tables only
	// hold what the checkpoint had already committed, so sampling afterwards
	// could advertise coverage the published numbers do not actually include.
	checkpoint, err := s.repo.loadRollupCheckpoint(ctx)
	if err != nil {
		return err
	}
	// HomeOverview already fans its four reads out internally, so a second
	// layer of parallelism here would add goroutines without shortening the
	// slowest query.
	stats, hot, err := s.repo.HomeOverview(ctx)
	if err != nil {
		return err
	}
	if err := s.repo.SaveHomeAnalyticsSnapshot(ctx, HomeAnalyticsSnapshot{
		HotAPIs:     hot,
		Stats:       stats,
		CoveredUpTo: formatCoveredUpTo(checkpoint.lastLogCreatedAt),
	}); err != nil {
		return err
	}
	s.invalidateHomeOverview(ctx)
	return nil
}

type HomeAnalyticsSnapshotWorker struct {
	cancel context.CancelFunc
	done   <-chan struct{}
	once   sync.Once
}

// StartHomeAnalyticsSnapshotWorker refreshes the homepage projection on a
// fixed serial cadence. Startup priming is performed explicitly by the app so
// the first HTTP request cannot race an expensive initial aggregation.
func (s *Service) StartHomeAnalyticsSnapshotWorker(
	ctx context.Context,
	interval time.Duration,
	onError func(error),
) *HomeAnalyticsSnapshotWorker {
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = homeAnalyticsSnapshotRefreshInterval
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	wake := s.ensureHomeAnalyticsSnapshotWake()
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				s.refreshHomeAnalyticsSnapshotWithTimeout(workerCtx, onError)
			case <-wake:
				s.refreshHomeAnalyticsSnapshotWithTimeout(workerCtx, onError)
			}
		}
	}()
	return &HomeAnalyticsSnapshotWorker{cancel: cancel, done: done}
}

func (w *HomeAnalyticsSnapshotWorker) Close(ctx context.Context) error {
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

func (s *Service) refreshHomeAnalyticsSnapshotWithTimeout(ctx context.Context, onError func(error)) {
	refreshCtx, cancel := context.WithTimeout(ctx, homeAnalyticsSnapshotRefreshTimeout)
	defer cancel()
	if err := s.RefreshHomeAnalyticsSnapshot(refreshCtx); err != nil &&
		!errors.Is(err, context.Canceled) && onError != nil {
		onError(err)
	}
}

// composeHomeOverview merges the live site content with the persisted analytics
// payload and stamps the analytics truth verdict. The verdict is applied here,
// after the numbers are assembled, because readiness depends on the moment the
// request is served rather than on the stored payload.
func composeHomeOverview(
	site SiteConfig,
	notice HomeTicker,
	snapshot HomeAnalyticsSnapshot,
	health AnalyticsHealth,
) HomeOverview {
	if snapshot.Stats.AvailabilitySource == "" {
		snapshot.Stats.AvailabilitySource = "CALL_SUCCESS"
	}
	hot := append([]HotAPIItem(nil), snapshot.HotAPIs...)
	if hot == nil {
		hot = []HotAPIItem{}
	}
	return HomeOverview{
		HomeNotice: notice,
		HotAPIs:    hot,
		SiteConfig: publicSiteSummary(site),
		Stats:      applyAnalyticsHealth(snapshot.Stats, health),
	}
}
