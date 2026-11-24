package ticket

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"bugpk-api/internal/pkg/businessclock"
	"bugpk-api/internal/pkg/dbx"
)

func (r *Repository) AddInternalNote(ctx context.Context, ticketID, actorID int64, content string, attachmentIDs ...int64) (int64, int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var ownerID int64
	if err := tx.QueryRowContext(ctx, "select user_id from bp_ticket where id=? for update", ticketID).Scan(&ownerID); err != nil {
		return 0, 0, err
	}
	result, err := tx.ExecContext(ctx, `
		insert into bp_ticket_message(ticket_id,sender_id,sender_type,internal_note,content)
		values(?,?,'STAFF',1,?)
	`, ticketID, actorID, content)
	if err != nil {
		return 0, 0, err
	}
	messageID, err := result.LastInsertId()
	if err != nil {
		return 0, 0, err
	}
	if err := bindMessageAttachments(ctx, tx, ticketID, messageID, actorID, attachmentIDs); err != nil {
		return 0, 0, err
	}
	if err := insertTicketActivity(ctx, tx, ticketID, actorID, SenderStaff, "INTERNAL_NOTE_ADDED", ActivityInternal, map[string]any{
		"messageId": strconv.FormatInt(messageID, 10),
	}); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return ownerID, messageID, nil
}

func (r *Repository) CloseByOwner(ctx context.Context, ticketID, ownerID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var status string
	if err := tx.QueryRowContext(ctx, `
		select status from bp_ticket where id=? and user_id=? for update
	`, ticketID, ownerID).Scan(&status); err != nil {
		return err
	}
	if status != StatusClosed {
		if _, err := tx.ExecContext(ctx, `
			update bp_ticket
			set status='CLOSED',resolved_at=coalesce(resolved_at,now()),closed_at=coalesce(closed_at,now())
			where id=?
		`, ticketID); err != nil {
			return err
		}
		if err := insertTicketActivity(ctx, tx, ticketID, ownerID, SenderUser, "CLOSED", ActivityPublic, map[string]any{
			"from": status,
			"to":   StatusClosed,
		}); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	r.invalidateReadCache()
	return nil
}

func (r *Repository) Attachments(ctx context.Context, ticketID, viewerID int64, admin bool) ([]Attachment, error) {
	rows, err := r.db.QueryContext(ctx, `
		select a.id,coalesce(a.message_id,0),a.uploader_id,
		       coalesce(nullif(u.display_name,''),u.username),
		       a.original_name,a.content_type,a.size_bytes,
		       a.storage_provider,coalesce(a.external_url,''),
		       date_format(a.created_at,'%Y-%m-%d %H:%i:%s')
		from bp_ticket_attachment a
		inner join bp_user u on u.id=a.uploader_id
		left join bp_ticket_message m on m.id=a.message_id
		where a.ticket_id=? and (
			?=1
			or (a.message_id is null and a.uploader_id=?)
			or (a.message_id is not null and m.internal_note=0)
		)
		order by a.id
	`, ticketID, boolInt(admin), viewerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Attachment, 0)
	for rows.Next() {
		var item Attachment
		var id, messageID, uploaderID int64
		if err := rows.Scan(
			&id,
			&messageID,
			&uploaderID,
			&item.UploaderName,
			&item.OriginalName,
			&item.ContentType,
			&item.SizeBytes,
			&item.Storage,
			&item.URL,
			&item.CreatedAt,
		); err != nil {
			return nil, err
		}
		item.ID = strconv.FormatInt(id, 10)
		item.UploaderID = strconv.FormatInt(uploaderID, 10)
		if messageID > 0 {
			item.MessageID = strconv.FormatInt(messageID, 10)
		}
		item.DownloadURL = fmt.Sprintf("/tickets/%d/attachments/%d", ticketID, id)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) AddAttachment(ctx context.Context, ticketID, uploaderID int64, file AttachmentFile, admin bool) (Attachment, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return Attachment{}, err
	}
	defer func() { _ = tx.Rollback() }()

	query := "select user_id from bp_ticket where id=?"
	args := []any{ticketID}
	if !admin {
		query += " and user_id=?"
		args = append(args, uploaderID)
	}
	query += " for update"
	var ownerID int64
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&ownerID); err != nil {
		return Attachment{}, err
	}
	result, err := tx.ExecContext(ctx, `
		insert into bp_ticket_attachment(
			ticket_id,uploader_id,original_name,stored_name,content_type,size_bytes,storage_provider,external_url
		) values(?,?,?,?,?,?,?,nullif(?,''))
	`, ticketID, uploaderID, file.OriginalName, file.StoredName, file.ContentType, file.SizeBytes, file.Storage, file.ExternalURL)
	if err != nil {
		return Attachment{}, err
	}
	attachmentID, err := result.LastInsertId()
	if err != nil {
		return Attachment{}, err
	}
	actorType := SenderUser
	if admin {
		actorType = SenderStaff
	}
	if err := insertTicketActivity(ctx, tx, ticketID, uploaderID, actorType, "ATTACHMENT_ADDED", ActivityPublic, map[string]any{
		"attachmentId": strconv.FormatInt(attachmentID, 10),
		"name":         file.OriginalName,
		"sizeBytes":    file.SizeBytes,
	}); err != nil {
		return Attachment{}, err
	}
	if err := tx.Commit(); err != nil {
		return Attachment{}, err
	}
	return Attachment{
		ContentType:  file.ContentType,
		DownloadURL:  fmt.Sprintf("/tickets/%d/attachments/%d", ticketID, attachmentID),
		ID:           strconv.FormatInt(attachmentID, 10),
		OriginalName: file.OriginalName,
		SizeBytes:    file.SizeBytes,
		Storage:      file.Storage,
		UploaderID:   strconv.FormatInt(uploaderID, 10),
		URL:          file.ExternalURL,
	}, nil
}

func (r *Repository) AttachmentForDownload(ctx context.Context, ticketID, attachmentID, userID int64, admin bool) (AttachmentFile, error) {
	query := `
		select a.original_name,a.stored_name,a.content_type,a.size_bytes,
		       a.storage_provider,coalesce(a.external_url,'')
		from bp_ticket_attachment a
		inner join bp_ticket t on t.id=a.ticket_id
		left join bp_ticket_message m on m.id=a.message_id
		where a.ticket_id=? and a.id=?
	`
	args := []any{ticketID, attachmentID}
	if !admin {
		query += " and t.user_id=? and ((a.message_id is null and a.uploader_id=?) or (a.message_id is not null and m.internal_note=0))"
		args = append(args, userID, userID)
	}
	var file AttachmentFile
	err := r.db.QueryRowContext(ctx, query, args...).Scan(
		&file.OriginalName,
		&file.StoredName,
		&file.ContentType,
		&file.SizeBytes,
		&file.Storage,
		&file.ExternalURL,
	)
	return file, err
}

func (r *Repository) DeleteAttachment(ctx context.Context, ticketID, attachmentID, actorID int64, admin bool) (string, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	query := `
		select a.stored_name,a.original_name
		from bp_ticket_attachment a
		inner join bp_ticket t on t.id=a.ticket_id
		where a.ticket_id=? and a.id=?
	`
	args := []any{ticketID, attachmentID}
	if !admin {
		query += " and t.user_id=? and a.uploader_id=?"
		args = append(args, actorID, actorID)
	}
	query += " for update"
	var storedName, originalName string
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&storedName, &originalName); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_ticket_attachment where id=? and ticket_id=?", attachmentID, ticketID); err != nil {
		return "", err
	}
	actorType := SenderUser
	if admin {
		actorType = SenderStaff
	}
	if err := insertTicketActivity(ctx, tx, ticketID, actorID, actorType, "ATTACHMENT_REMOVED", ActivityPublic, map[string]any{
		"attachmentId": strconv.FormatInt(attachmentID, 10),
		"name":         originalName,
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return storedName, nil
}

func (r *Repository) Activities(ctx context.Context, ticketID int64, admin bool) ([]Activity, error) {
	rows, err := r.db.QueryContext(ctx, `
		select a.id,a.actor_id,a.actor_type,a.action,a.visibility,
		       coalesce(cast(a.detail as char),'{}'),
		       coalesce(nullif(u.display_name,''),u.username,'System'),
		       date_format(a.created_at,'%Y-%m-%d %H:%i:%s')
		from bp_ticket_activity a
		left join bp_user u on u.id=a.actor_id
		where a.ticket_id=? and (?=1 or a.visibility='PUBLIC')
		order by a.id
	`, ticketID, boolInt(admin))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Activity, 0)
	for rows.Next() {
		var item Activity
		var id, actorID int64
		var detailText string
		if err := rows.Scan(
			&id,
			&actorID,
			&item.ActorType,
			&item.Action,
			&item.Visibility,
			&detailText,
			&item.ActorName,
			&item.CreatedAt,
		); err != nil {
			return nil, err
		}
		item.ID = strconv.FormatInt(id, 10)
		item.ActorID = strconv.FormatInt(actorID, 10)
		item.Detail = map[string]any{}
		if err := json.Unmarshal([]byte(detailText), &item.Detail); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) Statistics(ctx context.Context, days int) (Statistics, error) {
	if r.readCache == nil {
		return r.statisticsUncached(ctx, days)
	}
	key := fmt.Sprintf("statistics:%d", days)
	value, err := r.readCache.getOrLoad(ctx, key, func() (any, error) {
		return r.statisticsUncached(ctx, days)
	})
	if err != nil {
		return Statistics{}, err
	}
	result, ok := value.(Statistics)
	if !ok {
		return Statistics{}, fmt.Errorf("ticket statistics cache type mismatch")
	}
	return result, nil
}

func (r *Repository) statisticsUncached(ctx context.Context, days int) (Statistics, error) {
	since := businessclock.Now().AddDate(0, 0, -days+1).Format("2006-01-02 00:00:00")
	var (
		total, created, resolved, firstResponseOverdue, resolutionOverdue int64
		averageFirstResponse, averageResolution                           sql.NullFloat64
		status                                                            = make(map[string]int64)
		daily                                                             = make([]StatisticsDay, 0)
	)
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, `
		select count(*),
		       coalesce(sum(created_at>=?),0),
		       coalesce(sum(resolved_at is not null and resolved_at>=?),0),
		       coalesce(sum(first_response_due_at is not null and
		         ((first_responded_at is not null and first_responded_at>first_response_due_at) or
		          (first_responded_at is null and now()>first_response_due_at))),0),
		       coalesce(sum(resolution_due_at is not null and
		         ((resolved_at is not null and resolved_at>resolution_due_at) or
		          (resolved_at is null and status not in ('RESOLVED','CLOSED') and now()>resolution_due_at))),0),
		       avg(case when first_responded_at is not null then timestampdiff(second,created_at,first_responded_at)/60 end),
		       avg(case when resolved_at is not null then timestampdiff(second,created_at,resolved_at)/60 end)
		from bp_ticket
		where created_at>=?
	`, since, since, since).Scan(
				&total,
				&created,
				&resolved,
				&firstResponseOverdue,
				&resolutionOverdue,
				&averageFirstResponse,
				&averageResolution,
			)
		},
		func() error {
			statusRows, err := r.db.QueryContext(ctx, `
		select status,count(*) from bp_ticket where created_at>=? group by status
	`, since)
			if err != nil {
				return err
			}
			for statusRows.Next() {
				var name string
				var count int64
				if err := statusRows.Scan(&name, &count); err != nil {
					_ = statusRows.Close()
					return err
				}
				status[name] = count
			}
			// Close alone does not expose an iteration error after Next returned false.
			readErr := statusRows.Err()
			return errors.Join(readErr, statusRows.Close())
		},
		func() error {
			dailyRows, err := r.db.QueryContext(ctx, `
		select date_format(calendar.day,'%Y-%m-%d'),
		       coalesce(created.created_count,0),coalesce(resolved.resolved_count,0)
		from (
		  select date_add(date(?), interval seq.n day) day
		  from (
		    select ones.n + tens.n*10 n
		    from (select 0 n union all select 1 union all select 2 union all select 3 union all select 4 union all select 5 union all select 6 union all select 7 union all select 8 union all select 9) ones
		    cross join (select 0 n union all select 1 union all select 2) tens
		  ) seq
		  where seq.n<?
		) calendar
		left join (
		  select date(created_at) day,count(*) created_count
		  from bp_ticket where created_at>=? group by date(created_at)
		) created on created.day=calendar.day
		left join (
		  select date(resolved_at) day,count(*) resolved_count
		  from bp_ticket where resolved_at>=? group by date(resolved_at)
		) resolved on resolved.day=calendar.day
		order by calendar.day
	`, since, days, since, since)
			if err != nil {
				return err
			}
			defer dailyRows.Close()
			for dailyRows.Next() {
				var item StatisticsDay
				if err := dailyRows.Scan(&item.Date, &item.Created, &item.Resolved); err != nil {
					return err
				}
				daily = append(daily, item)
			}
			return dailyRows.Err()
		},
	)
	if err != nil {
		return Statistics{}, err
	}
	result := Statistics{
		Created:              created,
		Daily:                daily,
		FirstResponseOverdue: firstResponseOverdue,
		RangeDays:            days,
		ResolutionOverdue:    resolutionOverdue,
		Resolved:             resolved,
		Status:               status,
		Total:                total,
	}
	if averageFirstResponse.Valid {
		result.AverageFirstResponseMinutes = averageFirstResponse.Float64
	}
	if averageResolution.Valid {
		result.AverageResolutionMinutes = averageResolution.Float64
	}
	return result, nil
}

func (r *Repository) QuickReplies(ctx context.Context) ([]QuickReply, error) {
	rows, err := r.db.QueryContext(ctx, `
		select id,title,content_zh,content_en,category,sort_no,status
		from bp_ticket_quick_reply
		order by status desc,sort_no,id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]QuickReply, 0)
	for rows.Next() {
		var item QuickReply
		var id int64
		if err := rows.Scan(&id, &item.Title, &item.ContentZH, &item.ContentEN, &item.Category, &item.SortNo, &item.Status); err != nil {
			return nil, err
		}
		item.ID = strconv.FormatInt(id, 10)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) CreateQuickReply(ctx context.Context, actorID int64, req SaveQuickReplyRequest) (QuickReply, error) {
	result, err := r.db.ExecContext(ctx, `
		insert into bp_ticket_quick_reply(title,content_zh,content_en,category,sort_no,status,created_by)
		values(?,?,?,?,?,?,?)
	`, req.Title, req.ContentZH, req.ContentEN, req.Category, req.SortNo, valueOrDefault(req.Status, 1), actorID)
	if err != nil {
		return QuickReply{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return QuickReply{}, err
	}
	return r.quickReply(ctx, id)
}

func (r *Repository) UpdateQuickReply(ctx context.Context, id int64, req SaveQuickReplyRequest) (QuickReply, error) {
	result, err := r.db.ExecContext(ctx, `
		update bp_ticket_quick_reply
		set title=?,content_zh=?,content_en=?,category=?,sort_no=?,status=?
		where id=?
	`, req.Title, req.ContentZH, req.ContentEN, req.Category, req.SortNo, valueOrDefault(req.Status, 1), id)
	if err != nil {
		return QuickReply{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return QuickReply{}, err
	}
	if affected == 0 {
		return QuickReply{}, sql.ErrNoRows
	}
	return r.quickReply(ctx, id)
}

func (r *Repository) DeleteQuickReply(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, "delete from bp_ticket_quick_reply where id=?", id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) quickReply(ctx context.Context, id int64) (QuickReply, error) {
	var item QuickReply
	var rowID int64
	err := r.db.QueryRowContext(ctx, `
		select id,title,content_zh,content_en,category,sort_no,status
		from bp_ticket_quick_reply where id=?
	`, id).Scan(&rowID, &item.Title, &item.ContentZH, &item.ContentEN, &item.Category, &item.SortNo, &item.Status)
	item.ID = strconv.FormatInt(rowID, 10)
	return item, err
}

func bindMessageAttachments(ctx context.Context, tx *sql.Tx, ticketID, messageID, uploaderID int64, attachmentIDs []int64) error {
	if len(attachmentIDs) == 0 {
		return nil
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(attachmentIDs)), ",")
	args := make([]any, 0, 3+len(attachmentIDs))
	args = append(args, messageID, ticketID, uploaderID)
	for _, id := range attachmentIDs {
		args = append(args, id)
	}
	result, err := tx.ExecContext(ctx, `
		update bp_ticket_attachment
		set message_id=?
		where ticket_id=? and uploader_id=? and message_id is null and id in (`+placeholders+`)
	`, args...)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != int64(len(attachmentIDs)) {
		return errAttachmentUnavailable
	}
	return nil
}

func insertTicketActivity(ctx context.Context, tx *sql.Tx, ticketID, actorID int64, actorType, action, visibility string, detail map[string]any) error {
	payload, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		insert into bp_ticket_activity(ticket_id,actor_id,actor_type,action,visibility,detail)
		values(?,?,?,?,?,?)
	`, ticketID, actorID, actorType, action, visibility, string(payload))
	return err
}

func slaPolicyMinutes(priority string) (int, int) {
	switch priority {
	case PriorityUrgent:
		return 15, 4 * 60
	case PriorityHigh:
		return 60, 12 * 60
	case PriorityLow:
		return 8 * 60, 72 * 60
	default:
		return 4 * 60, 48 * 60
	}
}

func ticketSLAStatus(item Ticket) string {
	if item.FirstResponseLate || item.ResolutionLate {
		return "BREACHED"
	}
	if item.Status == StatusResolved || item.Status == StatusClosed {
		return "MET"
	}
	if strings.TrimSpace(item.FirstRespondedAt) == "" {
		return "FIRST_RESPONSE_PENDING"
	}
	return "RESOLUTION_PENDING"
}

func nullableActivityID(value int64) any {
	if value <= 0 {
		return nil
	}
	return strconv.FormatInt(value, 10)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func valueOrDefault(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}
