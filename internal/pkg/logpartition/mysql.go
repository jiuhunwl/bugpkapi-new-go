package logpartition

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const advisoryLockNameSQL = "CONCAT('bugpk:log-partitions:', SUBSTRING(SHA2(DATABASE(),256),1,40))"

type Options struct {
	Now          time.Time
	MonthsAhead  int
	Apply        bool
	AllowRebuild bool
}

// Run is read-only unless Apply is set. Initial conversion additionally requires
// AllowRebuild; it is deliberately not part of startup migrations. Maintenance
// only splits p_future, and never drops a retained partition.
func Run(ctx context.Context, db *sql.DB, opts Options) (Plan, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.MonthsAhead < 0 || opts.MonthsAhead > 24 {
		return Plan{}, fmt.Errorf("months-ahead must be between 0 and 24")
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return Plan{}, err
	}
	defer conn.Close()
	if opts.Apply {
		var locked sql.NullInt64
		if err = conn.QueryRowContext(ctx, "SELECT GET_LOCK("+advisoryLockNameSQL+",0)").Scan(&locked); err != nil {
			return Plan{}, err
		}
		if !locked.Valid || locked.Int64 != 1 {
			return Plan{}, fmt.Errorf("another call-log partition maintenance process is running")
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = conn.ExecContext(cleanup, "DO RELEASE_LOCK("+advisoryLockNameSQL+")")
		}()
	}
	plan, err := inspectPlan(ctx, conn, opts)
	if err != nil || !opts.Apply || plan.SQL == "" {
		return plan, err
	}
	if plan.RequiresRebuild && !opts.AllowRebuild {
		return plan, fmt.Errorf("initial partitioning rebuilds the table; back up, stop writers, and explicitly pass --allow-rebuild")
	}
	var oldLockWait int
	if err = conn.QueryRowContext(ctx, "SELECT @@SESSION.lock_wait_timeout").Scan(&oldLockWait); err != nil {
		return plan, err
	}
	if _, err = conn.ExecContext(ctx, "SET SESSION lock_wait_timeout=5"); err != nil {
		return plan, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(cleanup, fmt.Sprintf("SET SESSION lock_wait_timeout=%d", oldLockWait))
	}()
	_, err = conn.ExecContext(ctx, plan.SQL)
	return plan, err
}

func inspectPlan(ctx context.Context, conn *sql.Conn, opts Options) (Plan, error) {
	var engine string
	if err := conn.QueryRowContext(ctx, "SELECT engine FROM information_schema.tables WHERE table_schema=DATABASE() AND table_name=?", table).Scan(&engine); err != nil {
		return Plan{}, fmt.Errorf("inspect call-log engine: %w", err)
	}
	if engine != "InnoDB" {
		return Plan{}, fmt.Errorf("call-log partitioning requires InnoDB")
	}
	var foreignKeys int
	if err := conn.QueryRowContext(ctx, "SELECT count(*) FROM information_schema.key_column_usage WHERE (table_schema=DATABASE() AND table_name=? AND referenced_table_name IS NOT NULL) OR (referenced_table_schema=DATABASE() AND referenced_table_name=?)", table, table).Scan(&foreignKeys); err != nil {
		return Plan{}, err
	}
	if foreignKeys > 0 {
		return Plan{}, fmt.Errorf("call-log table has foreign-key dependencies; partition conversion is not supported")
	}
	rows, err := conn.QueryContext(ctx, "SELECT column_name,data_type,is_nullable,extra FROM information_schema.columns WHERE table_schema=DATABASE() AND table_name=? AND column_name IN ('id','created_at')", table)
	if err != nil {
		return Plan{}, err
	}
	validColumns := 0
	for rows.Next() {
		var name, kind, nullable, extra string
		if err = rows.Scan(&name, &kind, &nullable, &extra); err != nil {
			rows.Close()
			return Plan{}, err
		}
		if name == "id" && kind == "bigint" && nullable == "NO" && strings.Contains(extra, "auto_increment") {
			validColumns++
		}
		if name == "created_at" && kind == "datetime" && nullable == "NO" && !strings.Contains(extra, "VIRTUAL GENERATED") && !strings.Contains(extra, "STORED GENERATED") {
			validColumns++
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Plan{}, err
	}
	if validColumns != 2 {
		return Plan{}, fmt.Errorf("expected non-null AUTO_INCREMENT bigint id and datetime created_at")
	}
	rows, err = conn.QueryContext(ctx, "SELECT index_name,GROUP_CONCAT(column_name ORDER BY seq_in_index) FROM information_schema.statistics WHERE table_schema=DATABASE() AND table_name=? AND non_unique=0 GROUP BY index_name", table)
	if err != nil {
		return Plan{}, err
	}
	primary := ""
	for rows.Next() {
		var name, columns string
		if err = rows.Scan(&name, &columns); err != nil {
			rows.Close()
			return Plan{}, err
		}
		if name != "PRIMARY" {
			rows.Close()
			return Plan{}, fmt.Errorf("unexpected unique index %q; cannot weaken its uniqueness for partitioning", name)
		}
		primary = columns
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Plan{}, err
	}
	rows, err = conn.QueryContext(ctx, "SELECT partition_name,partition_method,partition_expression,partition_description FROM information_schema.partitions WHERE table_schema=DATABASE() AND table_name=? ORDER BY partition_ordinal_position", table)
	if err != nil {
		return Plan{}, err
	}
	var parts []partition
	for rows.Next() {
		var name, method, expression, upper sql.NullString
		if err = rows.Scan(&name, &method, &expression, &upper); err != nil {
			rows.Close()
			return Plan{}, err
		}
		if !name.Valid {
			continue
		}
		if method.String != "RANGE COLUMNS" || strings.TrimSpace(strings.ReplaceAll(expression.String, "`", "")) != "created_at" {
			rows.Close()
			return Plan{}, fmt.Errorf("unexpected partition method or key; manual review required")
		}
		parts = append(parts, partition{Name: name.String, Upper: upper.String})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Plan{}, err
	}
	expectedPrimary := "id"
	if len(parts) > 0 {
		expectedPrimary = "id,created_at"
	}
	if primary != expectedPrimary {
		return Plan{}, fmt.Errorf("unexpected primary key %q; expected %q", primary, expectedPrimary)
	}
	first := calendarMonth(opts.Now)
	if len(parts) == 0 {
		var oldest string
		err = conn.QueryRowContext(ctx, "SELECT DATE_FORMAT(created_at,'%Y-%m-01') FROM bp_interface_call_log ORDER BY created_at,id LIMIT 1").Scan(&oldest)
		if err != nil && err != sql.ErrNoRows {
			return Plan{}, err
		}
		if err == nil {
			first, err = time.Parse("2006-01-02", oldest)
			if err != nil {
				return Plan{}, err
			}
		}
	}
	return buildPlan(first, opts.Now, opts.MonthsAhead, parts)
}
