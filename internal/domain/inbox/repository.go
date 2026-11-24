package inbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// Recipient 是通知接收人；Email 用于可选的邮件副本。
type Recipient struct {
	ID    int64
	Email string
}

// AllRecipients 返回全部有效用户（status=1）。
func (r *Repository) AllRecipients(ctx context.Context) ([]Recipient, error) {
	rows, err := r.db.QueryContext(ctx,
		`select id, coalesce(email,'') from bp_user where status=1 order by id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Recipient, 0, 64)
	for rows.Next() {
		var item Recipient
		if err := rows.Scan(&item.ID, &item.Email); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repository) Recipient(ctx context.Context, userID int64) (Recipient, error) {
	var item Recipient
	err := r.db.QueryRowContext(ctx,
		`select id, coalesce(email,'') from bp_user where id=? and status=1`, userID).
		Scan(&item.ID, &item.Email)
	if errors.Is(err, sql.ErrNoRows) {
		return Recipient{}, fmt.Errorf("user %d not found", userID)
	}
	return item, err
}

type NewMessage struct {
	UserID int64
	Title  string
	Body   string
	Kind   string
}

const insertChunk = 500

// InsertBatch 批量落库站内消息；按固定分块避免单语句超限。
func (r *Repository) InsertBatch(ctx context.Context, title, body, kind string, userIDs []int64) error {
	for start := 0; start < len(userIDs); start += insertChunk {
		end := start + insertChunk
		if end > len(userIDs) {
			end = len(userIDs)
		}
		chunk := userIDs[start:end]
		var b strings.Builder
		b.WriteString("insert into bp_user_inbox (user_id, title, body, kind) values ")
		args := make([]any, 0, len(chunk)*4)
		for i, id := range chunk {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString("(?,?,?,?)")
			args = append(args, id, title, body, kind)
		}
		if _, err := r.db.ExecContext(ctx, b.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

// ListByUser 返回用户自己的消息（未读优先、新消息在前）。
func (r *Repository) ListByUser(ctx context.Context, userID int64, unreadOnly bool, offset, limit int) ([]Item, int64, error) {
	where := "where user_id=?"
	if unreadOnly {
		where += " and is_read=0"
	}
	var total int64
	if err := r.db.QueryRowContext(ctx,
		"select count(*) from bp_user_inbox "+where, userID).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return []Item{}, 0, nil
	}
	rows, err := r.db.QueryContext(ctx,
		`select id, title, body, kind, is_read, read_at, created_at
		 from bp_user_inbox `+where+`
		 order by is_read asc, id desc limit ? offset ?`, userID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]Item, 0, limit)
	for rows.Next() {
		var item Item
		var isRead int
		var readAt, createdAt sql.NullString
		if err := rows.Scan(&item.ID, &item.Title, &item.Body, &item.Kind, &isRead, &readAt, &createdAt); err != nil {
			return nil, 0, err
		}
		item.IsRead = isRead == 1
		item.ReadAt = readAt.String
		item.CreatedAt = createdAt.String
		items = append(items, item)
	}
	return items, total, rows.Err()
}

func (r *Repository) CountUnread(ctx context.Context, userID int64) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx,
		"select count(*) from bp_user_inbox where user_id=? and is_read=0", userID).Scan(&count)
	return count, err
}

// MarkRead 将指定消息（ids 为空则全部）置为已读；仅允许操作自己的消息。
func (r *Repository) MarkRead(ctx context.Context, userID int64, ids []int64) (int64, error) {
	query := "update bp_user_inbox set is_read=1, read_at=? where user_id=? and is_read=0"
	args := []any{time.Now().Format(time.DateTime), userID}
	if len(ids) > 0 {
		placeholders := make([]string, 0, len(ids))
		for _, id := range ids {
			placeholders = append(placeholders, "?")
			args = append(args, id)
		}
		query += " and id in (" + strings.Join(placeholders, ",") + ")"
	}
	res, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return affected, nil
}
