package riskcontrol

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"bugpk-api/internal/config"
)

func useOverviewSnapshot(query RiskQuery) bool {
	return query.WindowMinutes == maxWindowMinutes
}

func overviewFromSnapshot(snapshot Overview, query RiskQuery) Overview {
	query = normalizeQuery(query)
	snapshot.WindowMinutes = query.WindowMinutes
	snapshot.Events = limitedCopy(snapshot.Events, query.Limit)
	snapshot.TopFailedAccounts = limitedCopy(snapshot.TopFailedAccounts, query.Limit)
	snapshot.TopFailedIPs = limitedCopy(snapshot.TopFailedIPs, query.Limit)
	return snapshot
}

func limitedCopy[T any](items []T, limit int) []T {
	if limit < 0 {
		limit = 0
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return append([]T(nil), items...)
}

func (r *Repository) LoadOverviewSnapshot(ctx context.Context, query RiskQuery) (Overview, bool, error) {
	query = normalizeQuery(query)
	var payload []byte
	err := r.db.QueryRowContext(ctx, `
		select cast(payload as char)
		from bp_risk_control_overview_snapshot
		where window_minutes=?
	`, query.WindowMinutes).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Overview{}, false, nil
	}
	if err != nil {
		return Overview{}, false, err
	}
	var snapshot Overview
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return Overview{}, false, fmt.Errorf("decode risk-control overview snapshot: %w", err)
	}
	return overviewFromSnapshot(snapshot, query), true, nil
}

func (r *Repository) SaveOverviewSnapshot(ctx context.Context, overview Overview) error {
	if overview.WindowMinutes <= 0 {
		return errors.New("risk-control overview snapshot window is invalid")
	}
	overview.InvalidKeyAttempts = 0
	overview.InvalidKeyBlocked = 0
	overview.RiskScore = overviewRiskScore(overview)
	payload, err := json.Marshal(overview)
	if err != nil {
		return fmt.Errorf("encode risk-control overview snapshot: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		insert into bp_risk_control_overview_snapshot(window_minutes,payload,source_updated_at)
		values (?,?,now(6))
		on duplicate key update payload=values(payload),source_updated_at=values(source_updated_at)
	`, overview.WindowMinutes, payload)
	return err
}

type OverviewSnapshotWorker struct {
	cancel context.CancelFunc
	done   <-chan struct{}
	once   sync.Once
}

func (s *Service) StartOverviewSnapshotWorker(
	ctx context.Context,
	cfg config.AnalyticsConfig,
	onError func(error),
) *OverviewSnapshotWorker {
	if ctx == nil {
		ctx = context.Background()
	}
	interval := cfg.OverviewRefreshInterval
	if interval <= 0 {
		interval = time.Minute
	}
	timeout := 2 * interval
	if timeout > 5*time.Minute {
		timeout = 5 * time.Minute
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.runOverviewSnapshotWorker(workerCtx, interval, timeout, cfg.OverviewSnapshotWindowMinutes, onError)
	}()
	return &OverviewSnapshotWorker{cancel: cancel, done: done}
}

func (w *OverviewSnapshotWorker) Close(ctx context.Context) error {
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

func (s *Service) runOverviewSnapshotWorker(ctx context.Context, interval, timeout time.Duration, windowMinutes int, onError func(error)) {
	s.refreshOverviewSnapshotWithTimeout(ctx, timeout, windowMinutes, onError)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.refreshOverviewSnapshotWithTimeout(ctx, timeout, windowMinutes, onError)
		}
	}
}

func (s *Service) refreshOverviewSnapshotWithTimeout(ctx context.Context, timeout time.Duration, windowMinutes int, onError func(error)) {
	refreshCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := s.refreshOverviewSnapshot(refreshCtx, windowMinutes); err != nil && !errors.Is(err, context.Canceled) && onError != nil {
		onError(err)
	}
}

func (s *Service) refreshOverviewSnapshot(ctx context.Context, windowMinutes int) error {
	if windowMinutes <= 0 || windowMinutes > maxWindowMinutes {
		windowMinutes = maxWindowMinutes
	}
	query := RiskQuery{Limit: maxLimit, WindowMinutes: windowMinutes}
	overview, err := s.repo.Overview(ctx, query)
	if err != nil {
		return err
	}
	if err := s.repo.SaveOverviewSnapshot(ctx, overview); err != nil {
		return err
	}
	s.overviewCache.invalidate()
	return nil
}
