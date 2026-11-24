// Package revenue maintains an idempotent projection of immutable confirmed
// billing amounts. The marker and total commit together; memory is only a hint.
package revenue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	mysql "github.com/go-sql-driver/mysql"
)

const DefaultBatchSize = 1000
const MaxBatchSize = 2000

// Both terms are evaluated by one consistent SQL read. In-flight projection
// commits therefore cannot expose a doubled or omitted amount. The covering
// index restricts the second term to durable work not yet materialized.
const TotalQuery = `
 select cast(p.confirmed_charge_amount +
   (select coalesce(sum(b.charge_amount),0)
    from bp_billing_record b force index(idx_bp_billing_revenue_pending)
    where b.status='CONFIRMED' and b.revenue_projected=0) as char)
 from bp_billing_revenue_projection p where p.id=1
`
const SourceTotalQuery = `
 select cast(coalesce(sum(charge_amount),0) as char)
 from bp_billing_record where status='CONFIRMED'
`

var ErrProjectionMissing = errors.New("revenue projection metadata is missing")

type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

func (r *Repository) Total(ctx context.Context) (string, error) {
	// Explicitly pin a consistent read view even if the server/session default is
	// READ UNCOMMITTED. An operator's isolation setting must not expose half of
	// a projection transaction as additional income.
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var total string
	err = tx.QueryRowContext(ctx, TotalQuery).Scan(&total)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) && !IsMissingSchema(err) {
			return "", err
		}
		err = tx.QueryRowContext(ctx, SourceTotalQuery).Scan(&total)
		if err != nil {
			return "", err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return total, nil
}

func IsMissingSchema(err error) bool {
	var dbErr *mysql.MySQLError
	if !errors.As(err, &dbErr) {
		return false
	}
	message := strings.ToLower(dbErr.Message)
	return (dbErr.Number == 1146 && strings.Contains(message, "bp_billing_revenue_projection")) ||
		(dbErr.Number == 1054 && strings.Contains(message, "revenue_projected"))
}

func (r *Repository) Process(ctx context.Context, limit int) (int, error) {
	return r.process(ctx, limit, 0)
}
func (r *Repository) ProcessRecord(ctx context.Context, id int64) (int, error) {
	if id <= 0 {
		return 0, nil
	}
	return r.process(ctx, 1, id)
}
func (r *Repository) process(ctx context.Context, limit int, id int64) (int, error) {
	if limit <= 0 {
		limit = DefaultBatchSize
	}
	if limit > MaxBatchSize {
		limit = MaxBatchSize
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var projectionID int
	if err := tx.QueryRowContext(ctx, "select id from bp_billing_revenue_projection where id=1 for update").Scan(&projectionID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrProjectionMissing
		}
		return 0, err
	}
	query := "select id from bp_billing_record force index(idx_bp_billing_revenue_pending) where status='CONFIRMED' and revenue_projected=0"
	args := []any{}
	if id > 0 {
		query += " and id=?"
		args = append(args, id)
	}
	query += " order by id limit ? for update"
	args = append(args, limit)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	ids := make([]any, 0, limit)
	for rows.Next() {
		var recordID int64
		if err := rows.Scan(&recordID); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, recordID)
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, tx.Commit()
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	// Selected ledger rows remain locked, so their status and amount cannot
	// change between the sum and marker update. Confirmation never locks this
	// projection row, so requests do not serialize on the shared counter.
	_, err = tx.ExecContext(ctx, "update bp_billing_revenue_projection set confirmed_charge_amount=confirmed_charge_amount+(select coalesce(sum(charge_amount),0) from bp_billing_record where id in ("+marks+") and status='CONFIRMED' and revenue_projected=0) where id=1", ids...)
	if err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, "update bp_billing_record set revenue_projected=1 where id in ("+marks+") and status='CONFIRMED' and revenue_projected=0", ids...)
	if err != nil {
		return 0, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if changed != int64(len(ids)) {
		return 0, fmt.Errorf("revenue projection marker mismatch: expected %d, changed %d", len(ids), changed)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(ids), nil
}
