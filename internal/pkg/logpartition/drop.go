package logpartition

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// DropOptions selects which monthly partitions are eligible to be dropped and
// whether to execute the plan. Dropping is the retention-side counterpart to
// Run: Run only ever splits p_future to provision months ahead, while
// DropExpired removes fully-expired calendar months in one metadata operation
// instead of a row-by-row DELETE.
type DropOptions struct {
	Now time.Time
	// RetainDays is the call-log retention window in days. A monthly partition
	// is eligible only when its entire upper bound is older than
	// Now - RetainDays, i.e. the whole month has expired. The current month is
	// never eligible and must be trimmed by the fine-grained delete sweep.
	RetainDays int
	// MaxLogID is the rollup worker's committed high watermark
	// (bp_analytics_checkpoint.last_log_id). A partition is dropped only when
	// every id inside it is <= MaxLogID, so dropping can never erase an
	// un-summarized tail or a repair source.
	MaxLogID int64
	// Apply executes the DROP statements. When false the plan is read-only.
	Apply bool
}

// DropPlan is the set of partitions to drop, as ready-to-execute statements.
type DropPlan struct {
	SQL         string
	Partitions  []string
	RetainedMin time.Time
}

// DropExpired inspects the call-log partitions and returns the statements that
// would drop every fully-expired calendar month whose rows are all below the
// rollup high watermark. It never drops p_before or p_future, and never drops
// the current or a partially-expired month: those must be trimmed by the
// bounded row delete. With Apply set it executes the plan under the same
// advisory lock as Run so the two maintenance paths cannot overlap.
func DropExpired(ctx context.Context, db *sql.DB, opts DropOptions) (DropPlan, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.RetainDays < 0 {
		return DropPlan{}, fmt.Errorf("retain-days must be >= 0")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return DropPlan{}, err
	}
	defer conn.Close()
	plan, err := inspectDrop(ctx, conn, opts)
	if err != nil || !opts.Apply || plan.SQL == "" {
		return plan, err
	}
	if opts.Apply {
		var locked sql.NullInt64
		if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK("+advisoryLockNameSQL+",0)").Scan(&locked); err != nil {
			return DropPlan{}, err
		}
		if !locked.Valid || locked.Int64 != 1 {
			return DropPlan{}, fmt.Errorf("another call-log partition maintenance process is running")
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = conn.ExecContext(cleanup, "DO RELEASE_LOCK("+advisoryLockNameSQL+")")
		}()
	}
	if _, err := conn.ExecContext(ctx, plan.SQL); err != nil {
		return DropPlan{}, err
	}
	return plan, nil
}

// inspectDrop reads the partition layout and computes which months are safe to
// drop. It re-reads the same information_schema.partitions shape as inspectPlan
// so the two views can never disagree on names or bounds.
func inspectDrop(ctx context.Context, conn *sql.Conn, opts DropOptions) (DropPlan, error) {
	rows, err := conn.QueryContext(ctx, "SELECT partition_name,partition_method,partition_expression,partition_description FROM information_schema.partitions WHERE table_schema=DATABASE() AND table_name=? ORDER BY partition_ordinal_position", table)
	if err != nil {
		return DropPlan{}, fmt.Errorf("inspect call-log partitions: %w", err)
	}
	defer rows.Close()
	var parts []partition
	for rows.Next() {
		var name, method, expression, upper sql.NullString
		if err = rows.Scan(&name, &method, &expression, &upper); err != nil {
			return DropPlan{}, err
		}
		if !name.Valid {
			continue
		}
		if method.String != "RANGE COLUMNS" || strings.TrimSpace(strings.ReplaceAll(expression.String, "`", "")) != "created_at" {
			return DropPlan{}, fmt.Errorf("unexpected partition method or key; manual review required")
		}
		parts = append(parts, partition{Name: name.String, Upper: upper.String})
	}
	if err = rows.Err(); err != nil {
		return DropPlan{}, err
	}
	if len(parts) == 0 {
		// Not partitioned: there is nothing to drop via this path. The caller
		// keeps using the bounded row delete as the only cleanup.
		return DropPlan{}, nil
	}
	// The eligible window is [p_before, ..., p_future]. p_before holds legacy
	// rows that predate partitioning and is always retained (dropping it would
	// discard rows whose ids may still be above the watermark or whose dates
	// predate the first partition). p_future is MAXVALUE and must never drop.
	if len(parts) < 3 {
		return DropPlan{}, fmt.Errorf("unrecognized call-log partition layout; manual review required")
	}
	retainedMin := calendarMonth(opts.Now).AddDate(0, 0, -opts.RetainDays)
	var names []string
	for _, part := range parts[1 : len(parts)-1] {
		// Skip p_before (parts[0]) and p_future (last) by iterating the middle.
		upper, err := partitionBound(part.Upper)
		if err != nil {
			return DropPlan{}, err
		}
		// A partition is eligible only when its upper bound (the first instant
		// of the next month) is at or before the retention floor. Using <= means
		// "the whole month has expired".
		if upper.After(retainedMin) {
			break
		}
		// Safety: every row in this partition must be at or below the rollup
		// watermark. A partition can span a full month; querying its max id is a
		// single index seek on the primary key.
		var maxID sql.NullInt64
		if err := conn.QueryRowContext(ctx, "SELECT max(id) FROM "+table+" PARTITION("+part.Name+")").Scan(&maxID); err != nil {
			return DropPlan{}, fmt.Errorf("inspect max id of partition %s: %w", part.Name, err)
		}
		if !maxID.Valid || maxID.Int64 > opts.MaxLogID {
			// Some rows are not yet summarized (or the watermark is zero, which
			// means the worker has not committed anything yet). Keep the
			// partition; the bounded delete will handle it when safe.
			break
		}
		names = append(names, part.Name)
	}
	if len(names) == 0 {
		return DropPlan{RetainedMin: retainedMin}, nil
	}
	var stmts []string
	for _, name := range names {
		stmts = append(stmts, "ALTER TABLE "+table+" DROP PARTITION "+name)
	}
	return DropPlan{SQL: strings.Join(stmts, ";\n"), Partitions: names, RetainedMin: retainedMin}, nil
}
