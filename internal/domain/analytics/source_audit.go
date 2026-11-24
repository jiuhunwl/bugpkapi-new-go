package analytics

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"bugpk-api/internal/pkg/calloutcome"
	"bugpk-api/internal/pkg/masking"
	"go.uber.org/zap"
)

const (
	sourceAuditChunkSize     = 2_000
	sourceAuditTimeout       = 15 * time.Second
	sourceAuditRetryInterval = 10 * time.Minute
	sourceAuditStartDelay    = 30 * time.Second
)

// Audit queries read contiguous PRIMARY ranges. In particular they never
// perform millions of random table lookups through a created_at secondary
// index while holding the ingestion checkpoint row lock.
var sourceAuditChunkQuery = `
 select /*+ MAX_EXECUTION_TIME(1000) */ coalesce(max(s.id),0),count(*),
        coalesce(sum(s.created_at>=? and s.charge_type<>'ADMIN' and u.id is not null),0),
        coalesce(sum(s.created_at>=? and s.charge_type<>'ADMIN' and u.id is not null and ` + calloutcome.SQLLegacyFailure("s") + `),0),
        coalesce(sum(s.created_at>=? and s.charge_type<>'ADMIN' and u.id is not null and ` + calloutcome.SQLLegacyFailure("s") + ` and coalesce(s.client_ip,'')<>''),0),
        coalesce(sum(s.created_at>=? and s.charge_type<>'ADMIN' and u.id is not null and coalesce(s.request_fingerprint,'')<>''),0),
        max(if(s.charge_type<>'ADMIN' and u.id is not null,s.created_at,null)),
        max(if(s.charge_type<>'ADMIN' and u.id is not null and ` + calloutcome.SQLLegacyFailure("s") + `,s.created_at,null)),
        max(if(s.charge_type<>'ADMIN' and u.id is not null and ` + calloutcome.SQLLegacyFailure("s") + ` and coalesce(s.client_ip,'')<>'',s.created_at,null)),
        max(if(s.charge_type<>'ADMIN' and u.id is not null and coalesce(s.request_fingerprint,'')<>'',s.created_at,null))
 from (select id,created_at,charge_type,user_id,success,response_status,outcome_class,client_ip,request_fingerprint
       from bp_interface_call_log force index(primary)
       where id>? and id<=? order by id limit ?) s
 left join bp_user u on u.id=s.user_id
`

func auditSourceChunks(ctx context.Context, tx *sql.Tx, upperID int64, windowStart, riskStart time.Time) (rollupRepairCoverage, error) {
	var result rollupRepairCoverage
	var cursor int64
	for {
		var next, rows int64
		var calls, failures, ipFailures, risk uint64
		var latest, failureLatest, ipLatest, riskLatest sql.NullTime
		if err := tx.QueryRowContext(ctx, sourceAuditChunkQuery, windowStart, windowStart, windowStart, riskStart, cursor, upperID, sourceAuditChunkSize).Scan(&next, &rows, &calls, &failures, &ipFailures, &risk, &latest, &failureLatest, &ipLatest, &riskLatest); err != nil {
			return rollupRepairCoverage{}, fmt.Errorf("read bounded source audit chunk: %w", err)
		}
		result.sourceCallCount += calls
		result.sourceFailureCount += failures
		result.sourceFailureIPCount += ipFailures
		result.sourceRiskCallCount += risk
		merge := func(dst *sql.NullTime, value sql.NullTime) {
			if value.Valid && !value.Time.Before(windowStart) && (!dst.Valid || value.Time.After(dst.Time)) {
				*dst = value
			}
		}
		merge(&result.sourceLatest, latest)
		merge(&result.failureLatest, failureLatest)
		merge(&result.failureWithIPLatest, ipLatest)
		merge(&result.riskLatest, riskLatest)
		if rows == 0 {
			result.sourceCoverageReady = true
			return result, nil
		}
		if next <= cursor {
			return rollupRepairCoverage{}, errors.New("source audit cursor did not advance")
		}
		cursor = next
		// Yield between ranges; ingestion remains independent on another connection.
		select {
		case <-ctx.Done():
			return rollupRepairCoverage{}, ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}

// AuditSourceCoverage is an independent bounded maintenance operation. Its
// consistent snapshot freezes source and projections at one checkpoint. After
// the read finishes it may only schedule repair if that checkpoint is still
// current; stale observations are discarded, never applied to newer writes.
func (r *Repository) AuditSourceCoverage(ctx context.Context) (bool, error) {
	if r == nil || r.db == nil {
		return false, errors.New("analytics repository database is nil")
	}
	ctx, cancel := context.WithTimeout(ctx, sourceAuditTimeout)
	defer cancel()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var cursor int64
	var status, phase string
	var start, riskStart time.Time
	err = tx.QueryRowContext(ctx, `select last_log_id,coalesce(repair_status,'NONE'),coalesce(repair_phase,'IDLE'),date_sub(current_date(),interval 31 day),date_sub(now(),interval 26 hour) from bp_analytics_checkpoint where worker_key=?`, CheckpointKey).Scan(&cursor, &status, &phase, &start, &riskStart)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if cursor <= 0 || repairIsActive(status, phase) {
		return false, errors.New("source audit deferred until ingest/repair has a committed checkpoint")
	}
	counts, err := auditSourceChunks(ctx, tx, cursor, start, riskStart)
	if err != nil {
		return false, err
	}
	coverage, err := readProjectionCoverage(ctx, tx, counts)
	if err != nil {
		return false, err
	}
	// The projection totals are compared from the dataset floor's midnight, but
	// the surviving log may start mid-day: the external retention sweep removes
	// everything older than its rolling bound, including the floor day's first
	// hours that the minute and day projections already counted. Comparing those
	// totals against a truncated source reports a drift that does not exist and
	// used to schedule a destructive repair on every truncation. Only certify the
	// source side when the log actually covers the floor day completely.
	floor, err := readDatasetFloor(ctx, tx)
	if err != nil {
		return false, err
	}
	earliest, err := readReplayFloor(ctx, tx)
	if err != nil {
		return false, err
	}
	sourceComplete := !earliest.Valid
	if earliest.Valid && floor.Valid && earliest.Time.After(floor.Time) {
		sourceComplete = false
	}
	coverage.sourceCoverageReady = sourceComplete
	if sourceComplete {
		coverage.sourceCallCount = counts.sourceCallCount
		coverage.sourceFailureCount = counts.sourceFailureCount
		coverage.sourceFailureIPCount = counts.sourceFailureIPCount
		coverage.sourceRiskCallCount = counts.sourceRiskCallCount
	}
	mask := coverage.repairMask()
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if mask == 0 {
		return false, nil
	}
	// The lock is acquired only AFTER all potentially expensive reads finished.
	write, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return false, err
	}
	defer write.Rollback()
	var current int64
	if err := write.QueryRowContext(ctx, `select last_log_id,coalesce(repair_status,'NONE'),coalesce(repair_phase,'IDLE') from bp_analytics_checkpoint where worker_key=? for update`, CheckpointKey).Scan(&current, &status, &phase); err != nil {
		return false, err
	}
	if current != cursor || repairIsActive(status, phase) {
		return false, errors.New("source audit observation superseded by concurrent ingest or repair; retry later")
	}
	// Persist intent only. The existing resumable worker freezes source bounds
	// and performs bounded clear/replay steps on its next turn.
	_, err = write.ExecContext(ctx, `update bp_analytics_checkpoint set repair_status='PENDING',repair_phase='IDLE',repair_projection_mask=?,repair_window_days=?,repair_requested_at=now(6),repair_started_at=null,repair_completed_at=null,repair_window_start=null,repair_cursor_id=0,repair_upper_log_id=0,repair_clear_projection=0 where worker_key=?`, mask, rollupRepairWindowDays, CheckpointKey)
	if err != nil {
		return false, err
	}
	return true, write.Commit()
}

func repairIsActive(status, phase string) bool {
	return status == repairStatusPending || status == repairStatusRunning || phase == repairPhaseClear || phase == repairPhaseRollup || phase == repairPhaseFinalize
}

func (r *Repository) runSourceAudit(ctx context.Context) {
	timer := time.NewTimer(sourceAuditStartDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		start := time.Now()
		scheduled, err := r.AuditSourceCoverage(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			r.auditLogger().Warn("analytics source audit deferred; ingestion continues", zap.Duration("duration", time.Since(start)), zap.String("error", masking.Error(err)))
			timer.Reset(sourceAuditRetryInterval)
		} else {
			r.auditLogger().Info("analytics source audit finished", zap.Duration("duration", time.Since(start)), zap.Bool("repair_scheduled", scheduled))
			timer.Reset(rollupSourceCoverageProbeInterval)
		}
	}
}
func (r *Repository) SetLogger(log *zap.Logger) *Repository { r.log = log; return r }
func (r *Repository) auditLogger() *zap.Logger {
	if r.log != nil {
		return r.log
	}
	return zap.NewNop()
}
