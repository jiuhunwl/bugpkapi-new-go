package user

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"bugpk-api/internal/pkg/calloutcome"
)

// Only a small, bounded source tail may be read on an administrator request.
// Large backlogs remain the analytics worker's responsibility, never a truncated
// response advertised as a complete total.
const adminCallTailLimit = 20_000

type adminCallBucket struct {
	label                                string
	calls, successes, failures, billable int64
	amount                               string
}

type adminCallSnapshot struct {
	today   time.Time
	buckets []adminCallBucket
}

type adminCallCheckpoint struct {
	cursor                   sql.NullInt64
	completed                sql.NullTime
	lastError, status, phase string
	repairMask               uint64
	sourceID                 int64
	today                    time.Time
}

// adminCallSnapshotProbeFull and adminCallSnapshotProbeLegacy read the same
// decision inputs. The durable repair columns are additive (migration 000090
// added the task, 000092 added the resumable cursor), so a database that never
// received 000092 keeps a legitimate, supported checkpoint shape. One shared
// statement would make that shape fail outright, which is why each shape has
// its own statement and the older one is tried before any verdict is published.
const adminCallSnapshotProbeFull = `
  select c.last_log_id,c.last_completed_at,coalesce(c.last_error,''),
         coalesce(c.repair_status,'NONE'),coalesce(c.repair_phase,'IDLE'),
         coalesce(c.repair_projection_mask,0),
         (select coalesce(max(id),0) from bp_interface_call_log),current_date()
  from (select 1) singleton
  left join bp_analytics_checkpoint c on c.worker_key=?
 `

const adminCallSnapshotProbeLegacy = `
  select c.last_log_id,c.last_completed_at,coalesce(c.last_error,''),
         coalesce(c.repair_status,'NONE'),
         (select coalesce(max(id),0) from bp_interface_call_log),current_date()
  from (select 1) singleton
  left join bp_analytics_checkpoint c on c.worker_key=?
 `

// readAdminCallSnapshot reads the cursor, rollups and any unprocessed tail in
// one read-only consistent snapshot. A worker commit between separate reads
// must not certify an older aggregate or count the same tail twice.
func (r *Repository) readAdminCallSnapshot(ctx context.Context, days int) (adminCallSnapshot, error) {
	if days != 1 && days != 7 {
		return adminCallSnapshot{}, fmt.Errorf("unsupported admin call window: %d", days)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return adminCallSnapshot{}, err
	}
	defer tx.Rollback()
	var checkpoint adminCallCheckpoint
	err = tx.QueryRowContext(ctx, adminCallSnapshotProbeFull, adminAnalyticsCheckpointKey).Scan(&checkpoint.cursor, &checkpoint.completed, &checkpoint.lastError, &checkpoint.status, &checkpoint.phase, &checkpoint.repairMask, &checkpoint.sourceID, &checkpoint.today)
	if err != nil && isMissingAdminAnalyticsCursorColumnError(err) {
		// A 000090-era checkpoint has no phase or projection mask. Treating an
		// absent additive column as an outage would blank the whole overview on a
		// database whose migration version already claims to be current, so fall
		// back to the older shape and let the remaining checks certify or refuse.
		// An unknown phase cannot hide an active repair: newAdminAnalyticsRepairState
		// keeps protecting every projection while the status says PENDING/RUNNING.
		checkpoint.phase, checkpoint.repairMask = "IDLE", 0
		err = tx.QueryRowContext(ctx, adminCallSnapshotProbeLegacy, adminAnalyticsCheckpointKey).Scan(&checkpoint.cursor, &checkpoint.completed, &checkpoint.lastError, &checkpoint.status, &checkpoint.sourceID, &checkpoint.today)
	}
	if err != nil {
		// A missing table or a missing checkpoint column is an environment gap,
		// never a query defect: report it through the shared unavailable verdict
		// so the section degrades instead of leaking a raw database error.
		if isMissingAdminAnalyticsStructureError(err) {
			return adminCallSnapshot{}, errAdminAnalyticsProjectionUnavailable
		}
		return adminCallSnapshot{}, err
	}
	active := newAdminAnalyticsRepairState(checkpoint.status, checkpoint.phase, checkpoint.repairMask, sql.NullTime{}, checkpoint.lastError, time.Time{})
	section := AdminWorkbenchSectionOverview
	if days == 7 {
		section = AdminWorkbenchSectionCallTrend
	}
	// CallTrend degrades on the minute/day metric layers only: the seven-day
	// window now reads the durable day layer, so a rebuilding compact api/day
	// projection no longer blocks it (see the minute-path guard below).
	if active.degrades(section) {
		return adminCallSnapshot{}, errAdminAnalyticsProjectionUnavailable
	}
	if checkpoint.sourceID > 0 && (!checkpoint.cursor.Valid || !checkpoint.completed.Valid) {
		return adminCallSnapshot{}, errAdminAnalyticsProjectionUnavailable
	}
	// A completed worker can legitimately retain a cursor after the last raw
	// row expires. Its next idle tick rewinds that cursor to zero, while the
	// durable daily history remains. Neither state implies a corrupt restore.
	emptyCompletedSource := checkpoint.sourceID == 0 && checkpoint.cursor.Valid && checkpoint.completed.Valid
	if checkpoint.cursor.Int64 < 0 || (checkpoint.cursor.Int64 > checkpoint.sourceID && !emptyCompletedSource) || r.forceStaleRollupCheckpoint {
		return adminCallSnapshot{}, errAdminAnalyticsProjectionUnavailable
	}
	// checkpoint.lastError is intentionally NOT used to refuse a snapshot
	// while the checkpoint age is still fresh. processBatchWithRetry only
	// clears the field on the next successful tick, so a single transient
	// failure during log cleanup — lock wait, connection reset, a lock held
	// by a nightly retention job — used to pin the whole admin overview and
	// call trend to "—" until the worker happened to run again and overwrite
	// the marker. That is why the console "frequently" degraded right after
	// log maintenance even though last_completed_at was still healthy. A
	// fresh last_completed_at proves the worker has recovered; the previous
	// error is now informational and the aggregated projections are still
	// trustworthy.
	if checkpoint.completed.Valid {
		age := time.Since(checkpoint.completed.Time)
		if age < -time.Minute || age > adminWorkbenchRollupLagTolerance {
			return adminCallSnapshot{}, errAdminAnalyticsProjectionUnavailable
		}
	} else if checkpoint.lastError != "" {
		// No completed timestamp at all: without an age to compare against, a
		// recorded error is the only signal we have. Keep the conservative
		// refusal so an uncommitted/failed first run never publishes a partial
		// snapshot.
		return adminCallSnapshot{}, errAdminAnalyticsProjectionUnavailable
	}
	if emptyCompletedSource {
		checkpoint.cursor.Int64 = 0
	}
	tail := checkpoint.sourceID > checkpoint.cursor.Int64
	if tail && checkpoint.sourceID-checkpoint.cursor.Int64 > adminCallTailLimit {
		var overflowID int64
		err := tx.QueryRowContext(ctx, `
   select id from bp_interface_call_log force index(primary)
   where id>? and id<=? order by id limit 1 offset ?
  `, checkpoint.cursor.Int64, checkpoint.sourceID, adminCallTailLimit).Scan(&overflowID)
		if err == nil {
			return adminCallSnapshot{}, fmt.Errorf("%w: more than %d source rows await aggregation", errAdminAnalyticsProjectionUnavailable, adminCallTailLimit)
		}
		if err != sql.ErrNoRows {
			return adminCallSnapshot{}, err
		}
	}
	windowStart := checkpoint.today.AddDate(0, 0, 1-days)
	windowEnd := checkpoint.today.AddDate(0, 0, 1)
	if checkpoint.cursor.Int64 == 0 {
		var existingProjection int
		if err := tx.QueryRowContext(ctx, `select exists(select 1 from bp_call_metric_minute where bucket_start>=? and bucket_start<? limit 1)`, windowStart, windowEnd).Scan(&existingProjection); err != nil {
			return adminCallSnapshot{}, err
		}
		if existingProjection != 0 {
			return adminCallSnapshot{}, fmt.Errorf("%w: projection exists without a committed source cursor", errAdminAnalyticsProjectionUnavailable)
		}
	}
	// A seven-day window outlives the minute layer's retention. Its values must
	// come from the durable day layer, including amounts and 404 classification.
	// Older schemas may still use the minute reader, but cannot certify days
	// whose full metrics have already expired.
	useDay := days == 7
	query := adminCallSnapshotQuery(tail, useDay)
	args := []any{windowStart, windowEnd}
	if tail {
		args = append(args, checkpoint.cursor.Int64, checkpoint.sourceID, windowStart, windowEnd)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil && useDay && isMissingTableError(err) {
		useDay = false
		rows, err = tx.QueryContext(ctx, adminCallSnapshotQuery(tail, false), args...)
	}
	if err != nil {
		return adminCallSnapshot{}, err
	}
	buckets := make([]adminCallBucket, 0, days)
	projectedByDay := make(map[string]int64, days)
	for rows.Next() {
		var bucket adminCallBucket
		var projectedCalls int64
		if err := rows.Scan(&bucket.label, &bucket.calls, &bucket.successes, &bucket.failures, &bucket.billable, &bucket.amount, &projectedCalls); err != nil {
			rows.Close()
			return adminCallSnapshot{}, err
		}
		// Since v1.2.30 a 404 log row counts toward call_count but toward neither
		// success_count nor failure_count, so the three-way equality no longer
		// holds; only the upper bound successes+failures <= calls is guaranteed.
		if bucket.calls < 0 || bucket.successes < 0 || bucket.failures < 0 || bucket.calls < bucket.successes+bucket.failures || projectedCalls < 0 || projectedCalls > bucket.calls {
			rows.Close()
			return adminCallSnapshot{}, fmt.Errorf("inconsistent admin call aggregate")
		}
		if checkpoint.cursor.Int64 == 0 && projectedCalls > 0 && !(useDay && emptyCompletedSource) {
			rows.Close()
			return adminCallSnapshot{}, fmt.Errorf("%w: projection exists without a committed source cursor", errAdminAnalyticsProjectionUnavailable)
		}
		buckets = append(buckets, bucket)
		// Live tail rows cannot certify that older processed rows exist in the projection.
		projectedByDay[bucket.label] = projectedCalls
	}
	scanErr := rows.Err()
	closeErr := rows.Close()
	if scanErr != nil {
		return adminCallSnapshot{}, scanErr
	}
	if closeErr != nil {
		return adminCallSnapshot{}, closeErr
	}
	if useDay {
		if err := validateAdminDaySnapshot(ctx, tx, checkpoint, windowStart, windowEnd, projectedByDay); err != nil {
			return adminCallSnapshot{}, err
		}
		if err := tx.Commit(); err != nil {
			return adminCallSnapshot{}, err
		}
		return adminCallSnapshot{today: checkpoint.today, buckets: buckets}, nil
	}
	if active.active && active.projectionMask&adminProjectionAPIDay != 0 {
		return adminCallSnapshot{}, errAdminAnalyticsProjectionUnavailable
	}
	// The compact API/day projection covers the same source cursor. A damaged
	// minute projection can become nonzero after a new row is rolled; existence
	// alone would then hide the older missing rows until the next worker audit.
	dailyRows, err := tx.QueryContext(ctx, `
  select date_format(bucket_date,'%m-%d'),sum(call_count)
  from bp_call_api_metric_day where bucket_date>=? and bucket_date<?
  group by bucket_date
 `, windowStart, windowEnd)
	if err != nil {
		return adminCallSnapshot{}, err
	}
	dailyByDay := make(map[string]int64, days)
	for dailyRows.Next() {
		var label string
		var calls int64
		if err := dailyRows.Scan(&label, &calls); err != nil {
			dailyRows.Close()
			return adminCallSnapshot{}, err
		}
		dailyByDay[label] = calls
	}
	dailyErr := dailyRows.Err()
	dailyCloseErr := dailyRows.Close()
	if dailyErr != nil {
		return adminCallSnapshot{}, dailyErr
	}
	if dailyCloseErr != nil {
		return adminCallSnapshot{}, dailyCloseErr
	}
	// Compare the selected committed read layer (durable days for history,
	// minutes for today) against API/day contributions at the same cursor.
	// Small legacy cleanup drift is tolerated; absent layers are not. A
	// call-only API bucket cannot reconstruct billable counts or money.
	//
	// This intentionally avoids recomputing a per-day source oracle: counting
	// two million eligible log rows inside an administrator request blew the
	// section deadline (production, 2026-10-14). Existence is instead probed —
	// per day, with LIMIT 1 — only for days neither layer covers, which is the
	// cheap check the original gap probe already used.
	for label, minuteCalls := range projectedByDay {
		dailyCalls, covered := dailyByDay[label]
		if minuteCalls <= 0 && (!covered || dailyCalls <= 0) {
			// Both sides empty: a genuine zero unless the gap probe below finds
			// processed source rows the projections dropped.
			continue
		}
		if minuteCalls > 0 && covered && dailyCalls > 0 {
			if absDiff64(minuteCalls, dailyCalls) > adminCallGateTolerance {
				return adminCallSnapshot{}, fmt.Errorf("%w: minute/day counts drift beyond tolerance for %s", errAdminAnalyticsProjectionUnavailable, label)
			}
			continue
		}
		if minuteCalls > 0 {
			return adminCallSnapshot{}, fmt.Errorf("%w: minute projection exists without the api/day layer for %s", errAdminAnalyticsProjectionUnavailable, label)
		}
		// The selected layer is empty but the independent day projection is not.
		// Refuse instead of silently turning retained historical traffic to zero.
		if covered {
			return adminCallSnapshot{}, fmt.Errorf("%w: api/day projection exists without the complete metric layer for %s", errAdminAnalyticsProjectionUnavailable, label)
		}
	}
	for label := range dailyByDay {
		if _, covered := projectedByDay[label]; covered {
			continue
		}
		return adminCallSnapshot{}, fmt.Errorf("%w: full metrics expired for retained day %s; daily metrics schema is required", errAdminAnalyticsProjectionUnavailable, label)
	}
	// A missing/zero day is not automatically a real zero: probe that specific
	// source interval, using its time index and stopping at the first row. Do not
	// discard a failed gap check and publish a deceptively complete seven-day sum.
	for day := 0; day < days; day++ {
		start := checkpoint.today.AddDate(0, 0, -day)
		if projectedByDay[start.Format("01-02")] > 0 || checkpoint.cursor.Int64 == 0 {
			continue
		}
		var hasProcessedSource int
		if err := tx.QueryRowContext(ctx, `
   select exists(select 1 from bp_interface_call_log l force index(idx_bp_interface_call_log_created_id)
    where l.created_at>=? and l.created_at<?
      and l.id<=? and l.charge_type<>'ADMIN'
      and exists(select 1 from bp_user u where u.id=l.user_id) limit 1)
  `, start, start.AddDate(0, 0, 1), checkpoint.cursor.Int64).Scan(&hasProcessedSource); err != nil {
			return adminCallSnapshot{}, err
		}
		if hasProcessedSource != 0 {
			return adminCallSnapshot{}, fmt.Errorf("%w: processed source exists for missing day %s", errAdminAnalyticsProjectionUnavailable, start.Format("2006-01-02"))
		}
	}

	if err := tx.Commit(); err != nil {
		return adminCallSnapshot{}, err
	}
	return adminCallSnapshot{today: checkpoint.today, buckets: buckets}, nil
}

// adminCallGateTolerance caps how far the minute and api/day layers may drift
// apart on a day both of them cover before the console refuses to publish. It
// must cover the recorded cleanup-race drift (2–38 rows), while remaining
// below small worker batches too. The previous 1000-row allowance certified
// a 301-call day as complete after all 300 processed rows disappeared and
// only one new row was rolled. A 100-row cap retains headroom for those
// observed small differences but refuses that confirmed data-loss case.
// This remains a bounded projection check, not a full raw-log recount.
// Retained history is read from the full daily metric layer; API-only
// history is never sufficient to certify money-bearing metrics.
const adminCallGateTolerance = 100

func absDiff64(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}

// validateAdminDaySnapshot compares at most seven daily rows with the bounded
// minute layer. Retained day-only history is valid once its processed source
// rows have expired; a missing live projection still refuses certification.
func validateAdminDaySnapshot(ctx context.Context, tx *sql.Tx, checkpoint adminCallCheckpoint, start, end time.Time, daily map[string]int64) error {
	rows, err := tx.QueryContext(ctx, `
		select date_format(bucket_start,'%m-%d'),sum(call_count)
		from bp_call_metric_minute where bucket_start>=? and bucket_start<?
		group by date_format(bucket_start,'%m-%d')
	`, start, end)
	if err != nil {
		return err
	}
	minute := make(map[string]int64, 7)
	for rows.Next() {
		var label string
		var calls int64
		if err := rows.Scan(&label, &calls); err != nil {
			rows.Close()
			return err
		}
		minute[label] = calls
	}
	scanErr, closeErr := rows.Err(), rows.Close()
	if scanErr != nil {
		return scanErr
	}
	if closeErr != nil {
		return closeErr
	}
	for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
		label := day.Format("01-02")
		dayCalls, minuteCalls := daily[label], minute[label]
		if minuteCalls > 0 {
			if dayCalls <= 0 || absDiff64(dayCalls, minuteCalls) > adminCallGateTolerance {
				return fmt.Errorf("%w: day/minute counts disagree for %s", errAdminAnalyticsProjectionUnavailable, label)
			}
			continue
		}
		if dayCalls > 0 && day.Equal(checkpoint.today) {
			return fmt.Errorf("%w: current daily metrics lack minute coverage", errAdminAnalyticsProjectionUnavailable)
		}
		if checkpoint.cursor.Int64 == 0 {
			continue
		}
		var hasSource int
		if err := tx.QueryRowContext(ctx, `
			select exists(select 1 from bp_interface_call_log l force index(idx_bp_interface_call_log_created_id)
				where l.created_at>=? and l.created_at<? and l.id<=?
				  and l.charge_type<>'ADMIN'
				  and exists(select 1 from bp_user u where u.id=l.user_id) limit 1)
		`, day, day.AddDate(0, 0, 1), checkpoint.cursor.Int64).Scan(&hasSource); err != nil {
			return err
		}
		if hasSource != 0 {
			return fmt.Errorf("%w: processed source lacks minute coverage for %s", errAdminAnalyticsProjectionUnavailable, label)
		}
	}
	return nil
}

func adminCallSnapshotQuery(tail, useDay bool) string {
	projection := `
  select m.bucket_start,m.call_count,m.success_count,m.failure_count,m.billable_count,m.charge_amount,m.call_count as projected_calls
  from bp_call_metric_minute m
  where m.bucket_start>=? and m.bucket_start<?
 `
	if useDay {
		projection = `
   select cast(m.bucket_date as datetime) as bucket_start,m.call_count,m.success_count,m.failure_count,m.billable_count,m.charge_amount,m.call_count as projected_calls
   from bp_call_metric_day m
   where m.bucket_date>=? and m.bucket_date<?
  `
	}
	if tail {
		projection += `
   union all
   select l.created_at,1,if(l.success=1,1,0),if(` + calloutcome.SQLLegacyFailure("l") + `,1,0),if(l.billable=1,1,0),l.charge_amount,0
   from bp_interface_call_log l force index(primary)
   where l.id>? and l.id<=? and l.created_at>=? and l.created_at<?
     and l.charge_type<>'ADMIN'
     and exists(select 1 from bp_user u where u.id=l.user_id)
  `
	}
	return `
  select date_format(source.bucket_start,'%m-%d'),sum(source.call_count),sum(source.success_count),
         sum(source.failure_count),sum(source.billable_count),cast(coalesce(sum(source.charge_amount),0) as char),sum(source.projected_calls)
  from (` + projection + `) source
  group by date_format(source.bucket_start,'%m-%d')
 `
}

func (r *Repository) adminOverviewCallsSnapshot(ctx context.Context, item *AdminWorkbenchOverview) error {
	window, err := r.readAdminCallSnapshot(ctx, 1)
	if err != nil {
		return err
	}
	snapshot, err := adminOverviewCallsFromSnapshot(window)
	if err != nil {
		return err
	}
	*item = snapshot
	return nil
}

// adminOverviewCallsFromSnapshot derives today's card from the same frozen
// window. HTTP overview and trend have independent deadlines and availability;
// historical failures must not blank healthy today-only cards.
func adminOverviewCallsFromSnapshot(window adminCallSnapshot) (AdminWorkbenchOverview, error) {
	var snapshot AdminWorkbenchOverview
	snapshot.TodayChargeAmount = "0.0000"
	todayLabel := window.today.Format("01-02")
	for _, b := range window.buckets {
		if b.label != todayLabel {
			continue
		}
		snapshot.TodayCalls = b.calls
		snapshot.TodaySuccessCalls = b.successes
		snapshot.TodayFailedCalls = b.failures
		snapshot.TodayBillableCalls = b.billable
		snapshot.TodayChargeAmount = b.amount
		return snapshot, nil
	}
	return snapshot, nil
}

func (r *Repository) adminCallTrendSnapshot(ctx context.Context) (AdminCallTrend7d, error) {
	window, err := r.readAdminCallSnapshot(ctx, 7)
	if err != nil {
		return AdminCallTrend7d{}, err
	}
	return adminCallTrendFromSnapshot(window)
}

// adminCallTrendFromSnapshot turns a certified snapshot into the API shape.
// Labels and counts use the same database midnight, so a request crossing
// midnight cannot pair yesterday's labels with today's aggregate.
func adminCallTrendFromSnapshot(window adminCallSnapshot) (AdminCallTrend7d, error) {
	labels := dayLabels(window.today, 7)
	result := AdminCallTrend7d{Labels: labels, TotalCalls: make([]int64, len(labels)), SuccessCalls: make([]int64, len(labels)), FailedCalls: make([]int64, len(labels)), BillableCalls: make([]int64, len(labels)), ChargeAmounts: zeroMoneyList(len(labels))}
	index := make(map[string]int, len(labels))
	for i, label := range labels {
		index[label] = i
	}
	for _, b := range window.buckets {
		i, ok := index[b.label]
		if !ok {
			return AdminCallTrend7d{}, fmt.Errorf("aggregate day %s is outside the frozen statistics window", b.label)
		}
		result.TotalCalls[i] = b.calls
		result.SuccessCalls[i] = b.successes
		result.FailedCalls[i] = b.failures
		result.BillableCalls[i] = b.billable
		result.ChargeAmounts[i] = b.amount
		result.Total += b.calls
	}
	return result, nil
}

func adminUsesBoundedAnalyticsSnapshot(ctx context.Context) bool {
	disabled, _ := ctx.Value(adminWorkbenchReadPolicyKey{}).(bool)
	return disabled
}
