package notification

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"bugpk-api/internal/pkg/masking"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) Enqueue(ctx context.Context, req EnqueueRequest) error {
	// Repository-level normalization protects future/internal callers that may
	// enqueue directly instead of passing through Service.Enqueue.
	req.Language = normalizeLanguage(req.Language)
	payload, err := json.Marshal(req.Payload)
	if err != nil {
		return fmt.Errorf("marshal notification payload: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
		insert into bp_notification_outbox(
			channel, template_key, recipient, language, payload, dedupe_key,
			status, attempts, max_attempts, next_attempt_at
		) values('EMAIL', ?, ?, ?, ?, ?, 'PENDING', 0, 12, now())
		on duplicate key update dedupe_key=values(dedupe_key)
	`, req.TemplateKey, req.Recipient, req.Language, payload, req.DedupeKey)
	return err
}

func (r *Repository) Claim(ctx context.Context, limit int) ([]Entry, error) {
	if limit < 1 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		select id, template_key, recipient, language, cast(payload as char), attempts, max_attempts
		from bp_notification_outbox
		where (
			(status in ('PENDING','RETRY') and next_attempt_at <= now())
			or (status='PROCESSING' and locked_at < date_sub(now(), interval 10 minute))
		)
		order by id
		limit ?
		for update skip locked
	`, limit)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, limit)
	for rows.Next() {
		var item Entry
		if err := rows.Scan(
			&item.ID,
			&item.TemplateKey,
			&item.Recipient,
			&item.Language,
			&item.Payload,
			&item.Attempts,
			&item.MaxAttempts,
		); err != nil {
			_ = rows.Close()
			return nil, err
		}
		entries = append(entries, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range entries {
		entries[index].Attempts++
		if _, err := tx.ExecContext(ctx, `
			update bp_notification_outbox
			set status='PROCESSING', attempts=?, locked_at=now(), updated_at=now()
			where id=?
		`, entries[index].Attempts, entries[index].ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return entries, nil
}

func (r *Repository) MarkSent(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `
		update bp_notification_outbox
		set status='SENT', sent_at=now(), locked_at=null, last_error='', updated_at=now()
		where id=?
	`, id)
	return err
}

func (r *Repository) MarkRetry(
	ctx context.Context,
	entry Entry,
	deliveryErr error,
	nextAttemptAt time.Time,
	permanent bool,
) error {
	status := StatusRetry
	if permanent || entry.Attempts >= entry.MaxAttempts {
		status = StatusFailed
	}
	lastError := masking.Error(deliveryErr)
	if len(lastError) > 1000 {
		lastError = lastError[:1000]
	}
	_, err := r.db.ExecContext(ctx, `
		update bp_notification_outbox
		set status=?, next_attempt_at=?, locked_at=null, last_error=?, updated_at=now()
		where id=?
	`, status, nextAttemptAt, lastError, entry.ID)
	return err
}
