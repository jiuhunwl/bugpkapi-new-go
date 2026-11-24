package ticket

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"bugpk-api/internal/pkg/accountstatus"
	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/masking"
	"bugpk-api/internal/pkg/pagination"
)

type Repository struct {
	db        *sql.DB
	readCache *ticketReadCache
}

type ticketUpdate struct {
	AssignedTo    int64
	HasAssignment bool
	HasTags       bool
	Status        string
	Tags          []string
}

type addMessageResult struct {
	MessageID int64
	Status    string
	UserID    int64
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db, readCache: newTicketReadCache(ticketReadCacheTTL)}
}

// AccountActive reports whether the account is currently allowed to use the
// product. The live ticket stream calls it on a timer so a ban closes a stream
// that was already established, instead of only taking effect the next time the
// browser reconnects.
func (r *Repository) AccountActive(ctx context.Context, userID int64) (bool, error) {
	var active int
	err := r.db.QueryRowContext(ctx,
		"select "+accountstatus.EffectiveStatusSQL("")+" from bp_user where id = ? limit 1",
		userID).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return active == accountstatus.Active, nil
}

// UserProfile loads the compact account card for the ticket detail popover.
// Sensitive contact fields are masked before leaving the repository.
func (r *Repository) UserProfile(ctx context.Context, userID int64) (UserProfile, error) {
	var item UserProfile
	var id int64
	if err := r.db.QueryRowContext(ctx, `
		select id,username,coalesce(display_name,''),coalesce(avatar,''),
		       coalesce(email,''),coalesce(mobile,''),status,cast(balance as char),points,
		       coalesce(date_format(last_login_at,'%Y-%m-%d %H:%i:%s'),''),
		       coalesce(date_format(created_at,'%Y-%m-%d %H:%i:%s'),'')
		from bp_user
		where id = ?
	`, userID).Scan(
		&id,
		&item.Username,
		&item.DisplayName,
		&item.Avatar,
		&item.Email,
		&item.Mobile,
		&item.Status,
		&item.Balance,
		&item.Points,
		&item.LastLoginTime,
		&item.CreateTime,
	); err != nil {
		return UserProfile{}, err
	}
	item.ID = strconv.FormatInt(id, 10)
	item.Email = masking.Email(item.Email)
	item.Mobile = masking.Mobile(item.Mobile)
	item.Roles = make([]string, 0)
	rows, err := r.db.QueryContext(ctx, `
		select r.role_name
		from bp_user_role ur
		inner join bp_role r on r.id=ur.role_id
		where ur.user_id=?
		order by r.id
	`, userID)
	if err != nil {
		return UserProfile{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return UserProfile{}, err
		}
		item.Roles = append(item.Roles, name)
	}
	return item, rows.Err()
}

func (r *Repository) Create(ctx context.Context, ticketNo string, userID int64, req CreateRequest) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	preview := messagePreview(req.Content)
	firstResponseMinutes, resolutionMinutes := slaPolicyMinutes(req.Priority)
	result, err := tx.ExecContext(ctx, `
		insert into bp_ticket(
			ticket_no,user_id,subject,category,priority,status,language,
			first_response_due_at,resolution_due_at,
			last_message_preview,last_message_at,last_reply_by,message_count,
			unread_user,unread_admin
		)
		values(?,?,?,?,?,'OPEN',?,
		       date_add(now(), interval ? minute),date_add(now(), interval ? minute),
		       ?,now(),'USER',1,0,1)
	`, ticketNo, userID, req.Subject, req.Category, req.Priority, req.Language, firstResponseMinutes, resolutionMinutes, preview)
	if err != nil {
		return 0, err
	}
	ticketID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into bp_ticket_message(ticket_id,sender_id,sender_type,internal_note,content)
		values(?,?,'USER',0,?)
	`, ticketID, userID, req.Content); err != nil {
		return 0, err
	}
	if err := insertTicketActivity(ctx, tx, ticketID, userID, SenderUser, "CREATED", ActivityPublic, map[string]any{
		"category": req.Category,
		"priority": req.Priority,
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	r.invalidateReadCache()
	return ticketID, nil
}

func (r *Repository) List(ctx context.Context, userID int64, admin bool, params ListParams) (PageResult, error) {
	page, pageSize := pagination.Normalize(params.Page, params.PageSize, 100)
	where, args := ticketWhere(userID, admin, params)
	var total int64
	needsCount := ticketListNeedsCount(params)
	var items []Ticket
	var summary Summary
	queries := []func() error{
		func() error {
			listArgs := append(append([]any(nil), args...), pageSize, pagination.Offset(page, pageSize))
			rows, err := r.db.QueryContext(ctx, ticketListQuery(admin, params, where), listArgs...)
			if err != nil {
				return err
			}
			var scanErr error
			items, scanErr = scanTickets(rows)
			closeErr := rows.Close()
			if scanErr != nil {
				return scanErr
			}
			return closeErr
		},
		func() error {
			var err error
			summary, err = r.Summary(ctx, userID, admin)
			return err
		},
	}
	if needsCount {
		queries = append(queries, func() error {
			countArgs := append(append([]any(nil), args...), pageSize*pagination.MaxPage)
			return r.db.QueryRowContext(ctx, ticketCountQuery(params, where), countArgs...).Scan(&total)
		})
	}
	if err := dbx.RunParallel(queries...); err != nil {
		return PageResult{}, err
	}
	if !needsCount {
		total = summary.All
	}
	return PageResult{Items: items, Summary: summary, Total: total}, nil
}

func ticketListNeedsCount(params ListParams) bool {
	return params.Unread || params.Overdue || params.AssignedTo > 0 ||
		params.CreatedFrom != "" || params.CreatedTo != "" ||
		params.UpdatedFrom != "" || params.UpdatedTo != "" ||
		strings.TrimSpace(params.Keyword) != "" ||
		strings.TrimSpace(params.Status) != "" || strings.TrimSpace(params.Priority) != "" ||
		strings.TrimSpace(params.Category) != ""
}

func ticketFilterLookupJoin(params ListParams) string {
	if strings.TrimSpace(params.Keyword) == "" {
		return ""
	}
	return " inner join bp_user u on u.id=t.user_id"
}

func ticketCountQuery(params ListParams, where string) string {
	return `select count(*)
		from (
			select t.id
			from bp_ticket t` + ticketFilterLookupJoin(params) + `
			` + where + `
			limit ?
		) counted`
}

func ticketListQuery(admin bool, params ListParams, where string) string {
	orderBy := ticketOrderBy(params)
	return ticketSelect(admin) + `
		inner join (
			select t.id
			from bp_ticket t` + ticketFilterLookupJoin(params) + `
			` + where + `
			order by ` + orderBy + `
			limit ? offset ?
		) filtered on filtered.id=t.id
		order by ` + orderBy
}

// ticketOrderBy 只接受白名单字段；方向只拼接 asc/desc，避免把用户输入带进 SQL。
// 未指定排序时保持历史默认：优先级 → 最后消息时间 → id。
func ticketOrderBy(params ListParams) string {
	direction := "desc"
	if params.SortOrder == "asc" {
		direction = "asc"
	}
	const priorityOrder = "case t.priority when 'URGENT' then 0 when 'HIGH' then 1 when 'NORMAL' then 2 else 3 end"
	const statusOrder = "case t.status when 'OPEN' then 0 when 'PROCESSING' then 1 when 'WAITING_USER' then 2 when 'RESOLVED' then 3 else 4 end"
	switch params.SortBy {
	case "created_at":
		return "t.created_at " + direction + ",t.id desc"
	case "updated_at":
		return "t.updated_at " + direction + ",t.id desc"
	case "last_message_at":
		return "t.last_message_at " + direction + ",t.id desc"
	case "priority":
		return priorityOrder + " " + direction + ",t.last_message_at desc,t.id desc"
	case "status":
		return statusOrder + " " + direction + ",t.last_message_at desc,t.id desc"
	case "sla_due":
		return "t.first_response_due_at is null,t.first_response_due_at " + direction + ",t.id desc"
	default:
		return priorityOrder + " asc,t.last_message_at desc,t.id desc"
	}
}

func (r *Repository) Summary(ctx context.Context, userID int64, admin bool) (Summary, error) {
	if r.readCache == nil {
		return r.summaryUncached(ctx, userID, admin)
	}
	key := "summary:admin"
	if !admin {
		key = fmt.Sprintf("summary:user:%d", userID)
	}
	value, err := r.readCache.getOrLoad(ctx, key, func() (any, error) {
		return r.summaryUncached(ctx, userID, admin)
	})
	if err != nil {
		return Summary{}, err
	}
	result, ok := value.(Summary)
	if !ok {
		return Summary{}, fmt.Errorf("ticket summary cache type mismatch")
	}
	return result, nil
}

func (r *Repository) summaryUncached(ctx context.Context, userID int64, admin bool) (Summary, error) {
	where := ""
	args := []any{}
	unreadColumn := "unread_admin"
	if !admin {
		where = "where user_id=?"
		args = append(args, userID)
		unreadColumn = "unread_user"
	}
	var result Summary
	err := r.db.QueryRowContext(ctx, `
		select count(*),
		       coalesce(sum(status='OPEN'),0),
		       coalesce(sum(status='PROCESSING'),0),
		       coalesce(sum(status='WAITING_USER'),0),
		       coalesce(sum(status='RESOLVED'),0),
		       coalesce(sum(status='CLOSED'),0),
		       coalesce(sum(`+unreadColumn+`),0)
		from bp_ticket
		`+where, args...).Scan(
		&result.All,
		&result.Open,
		&result.Processing,
		&result.WaitingUser,
		&result.Resolved,
		&result.Closed,
		&result.Unread,
	)
	return result, err
}

func (r *Repository) Detail(ctx context.Context, ticketID, userID int64, admin bool) (Detail, error) {
	where := "where t.id=?"
	args := []any{ticketID}
	if !admin {
		where += " and t.user_id=?"
		args = append(args, userID)
	}
	ticketItem, err := scanTicket(r.db.QueryRowContext(ctx, ticketSelect(admin)+where, args...))
	if err != nil {
		return Detail{}, err
	}
	rows, err := r.db.QueryContext(ctx, `
		select m.id,m.sender_id,m.sender_type,m.internal_note,m.content,
		       coalesce(nullif(u.display_name,''),u.username),
		       date_format(m.created_at,'%Y-%m-%d %H:%i:%s')
		from bp_ticket_message m
		inner join bp_user u on u.id=m.sender_id
		where m.ticket_id=? and (?=1 or m.internal_note=0)
		order by m.id
	`, ticketID, boolInt(admin))
	if err != nil {
		return Detail{}, err
	}
	messages := make([]Message, 0)
	for rows.Next() {
		var item Message
		var id, senderID int64
		var internal int
		if err := rows.Scan(&id, &senderID, &item.SenderType, &internal, &item.Content, &item.SenderName, &item.CreatedAt); err != nil {
			_ = rows.Close()
			return Detail{}, err
		}
		item.ID = strconv.FormatInt(id, 10)
		item.SenderID = strconv.FormatInt(senderID, 10)
		item.Internal = internal == 1
		messages = append(messages, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return Detail{}, err
	}
	if err := rows.Close(); err != nil {
		return Detail{}, err
	}
	attachments, err := r.Attachments(ctx, ticketID, userID, admin)
	if err != nil {
		return Detail{}, err
	}
	activities, err := r.Activities(ctx, ticketID, admin)
	if err != nil {
		return Detail{}, err
	}
	return Detail{
		Activities:  activities,
		Attachments: attachments,
		Messages:    messages,
		Ticket:      ticketItem,
	}, nil
}

func (r *Repository) AddMessage(ctx context.Context, ticketID, senderID int64, admin bool, content string, attachmentIDs ...int64) (addMessageResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return addMessageResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var ownerID int64
	var status, language string
	query := "select user_id,status,language from bp_ticket where id=?"
	args := []any{ticketID}
	if !admin {
		query += " and user_id=?"
		args = append(args, senderID)
	}
	query += " for update"
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&ownerID, &status, &language); err != nil {
		return addMessageResult{}, err
	}
	if status == StatusClosed {
		return addMessageResult{}, errTicketClosed
	}
	senderType := SenderUser
	nextStatus := nextStatusAfterReply(status, false)
	if admin {
		senderType = SenderStaff
		nextStatus = nextStatusAfterReply(status, true)
	}
	messageResult, err := tx.ExecContext(ctx, `
		insert into bp_ticket_message(ticket_id,sender_id,sender_type,internal_note,content)
		values(?,?,?,0,?)
	`, ticketID, senderID, senderType, content)
	if err != nil {
		return addMessageResult{}, err
	}
	messageID, err := messageResult.LastInsertId()
	if err != nil {
		return addMessageResult{}, err
	}
	if err := bindMessageAttachments(ctx, tx, ticketID, messageID, senderID, attachmentIDs); err != nil {
		return addMessageResult{}, err
	}
	preview := messagePreview(content)
	if preview == "" {
		preview = attachmentPreview(language)
	}
	if admin {
		_, err = tx.ExecContext(ctx, `
			update bp_ticket
			set status=?,last_message_preview=?,last_message_at=now(),last_reply_by='STAFF',
			    message_count=message_count+1,unread_user=unread_user+1,unread_admin=0,
			    first_responded_at=coalesce(first_responded_at,now())
			where id=?
		`, nextStatus, preview, ticketID)
	} else {
		_, err = tx.ExecContext(ctx, `
			update bp_ticket
			set status=?,last_message_preview=?,last_message_at=now(),last_reply_by='USER',
			    message_count=message_count+1,unread_admin=unread_admin+1,unread_user=0
			where id=?
		`, nextStatus, preview, ticketID)
	}
	if err != nil {
		return addMessageResult{}, err
	}
	if err := insertTicketActivity(ctx, tx, ticketID, senderID, senderType, "REPLIED", ActivityPublic, map[string]any{
		"messageId": strconv.FormatInt(messageID, 10),
		"status":    nextStatus,
	}); err != nil {
		return addMessageResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return addMessageResult{}, err
	}
	r.invalidateReadCache()
	return addMessageResult{MessageID: messageID, Status: nextStatus, UserID: ownerID}, nil
}

func (r *Repository) Update(ctx context.Context, ticketID, actorID int64, update ticketUpdate) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	var ownerID, currentAssignedTo int64
	var currentStatus, currentTags string
	if err := tx.QueryRowContext(ctx, `
		select user_id,status,coalesce(assigned_to,0),coalesce(cast(tags as char),'[]')
		from bp_ticket where id=? for update
	`, ticketID).Scan(&ownerID, &currentStatus, &currentAssignedTo, &currentTags); err != nil {
		return 0, err
	}
	sets := make([]string, 0, 6)
	args := make([]any, 0, 7)
	if update.Status != "" {
		sets = append(sets, "status=?")
		args = append(args, update.Status)
		switch update.Status {
		case StatusResolved:
			sets = append(sets, "resolved_at=coalesce(resolved_at,now())")
		case StatusClosed:
			sets = append(sets, "resolved_at=coalesce(resolved_at,now())", "closed_at=coalesce(closed_at,now())")
		}
	}
	if update.HasAssignment {
		if update.AssignedTo > 0 {
			var exists int
			if err := tx.QueryRowContext(ctx, `
				select exists(select 1 from bp_user where id=? and `+accountstatus.EffectiveStatusSQL("")+`=1)
			`, update.AssignedTo).Scan(&exists); err != nil {
				return 0, err
			}
			if exists != 1 {
				return 0, sql.ErrNoRows
			}
			sets = append(sets, "assigned_to=?")
			args = append(args, update.AssignedTo)
		} else {
			sets = append(sets, "assigned_to=null")
		}
	}
	var tagsJSON string
	if update.HasTags {
		encodedTags, err := json.Marshal(update.Tags)
		if err != nil {
			return 0, err
		}
		tagsJSON = string(encodedTags)
		sets = append(sets, "tags=?")
		args = append(args, tagsJSON)
	}
	if len(sets) > 0 {
		args = append(args, ticketID)
		if _, err := tx.ExecContext(ctx, "update bp_ticket set "+strings.Join(sets, ",")+" where id=?", args...); err != nil {
			return 0, err
		}
	}
	if update.Status != "" && update.Status != currentStatus {
		if err := insertTicketActivity(ctx, tx, ticketID, actorID, SenderStaff, "STATUS_CHANGED", ActivityPublic, map[string]any{
			"from": currentStatus,
			"to":   update.Status,
		}); err != nil {
			return 0, err
		}
	}
	if update.HasAssignment && update.AssignedTo != currentAssignedTo {
		if err := insertTicketActivity(ctx, tx, ticketID, actorID, SenderStaff, "ASSIGNED", ActivityPublic, map[string]any{
			"from": nullableActivityID(currentAssignedTo),
			"to":   nullableActivityID(update.AssignedTo),
		}); err != nil {
			return 0, err
		}
	}
	if update.HasTags && tagsJSON != currentTags {
		if err := insertTicketActivity(ctx, tx, ticketID, actorID, SenderStaff, "TAGS_UPDATED", ActivityPublic, map[string]any{
			"tags": update.Tags,
		}); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	r.invalidateReadCache()
	return ownerID, nil
}

func (r *Repository) MarkRead(ctx context.Context, ticketID, userID int64, admin bool, lastMessageAt string, messageCount int) (bool, error) {
	query := "update bp_ticket set unread_admin=0 where id=? and unread_admin>0 and last_message_at=? and message_count=?"
	args := []any{ticketID, lastMessageAt, messageCount}
	if !admin {
		query = "update bp_ticket set unread_user=0 where id=? and user_id=? and unread_user>0 and last_message_at=? and message_count=?"
		args = []any{ticketID, userID, lastMessageAt, messageCount}
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected > 0 {
		r.invalidateReadCache()
		return true, nil
	}
	if affected == 0 {
		var exists int
		check := "select exists(select 1 from bp_ticket where id=?)"
		checkArgs := []any{ticketID}
		if !admin {
			check = "select exists(select 1 from bp_ticket where id=? and user_id=?)"
			checkArgs = append(checkArgs, userID)
		}
		if err := r.db.QueryRowContext(ctx, check, checkArgs...).Scan(&exists); err != nil {
			return false, err
		}
		if exists != 1 {
			return false, sql.ErrNoRows
		}
	}
	return false, nil
}

func (r *Repository) CanView(ctx context.Context, ticketID, userID int64, admin bool) (int64, error) {
	query := "select user_id from bp_ticket where id=?"
	args := []any{ticketID}
	if !admin {
		query += " and user_id=?"
		args = append(args, userID)
	}
	var ownerID int64
	err := r.db.QueryRowContext(ctx, query, args...).Scan(&ownerID)
	return ownerID, err
}

// SLA 超时口径同时用于列表展示与筛选，避免两处表达式漂移。
const (
	ticketFirstResponseOverdueSQL = `(t.first_response_due_at is not null and ((t.first_responded_at is not null and t.first_responded_at>t.first_response_due_at) or (t.first_responded_at is null and now()>t.first_response_due_at)))`
	ticketResolutionOverdueSQL    = `(t.resolution_due_at is not null and ((t.resolved_at is not null and t.resolved_at>t.resolution_due_at) or (t.resolved_at is null and t.status not in ('RESOLVED','CLOSED') and now()>t.resolution_due_at)))`
)

func ticketSelect(admin bool) string {
	unreadColumn := "t.unread_user"
	if admin {
		unreadColumn = "t.unread_admin"
	}
	return `
		select t.id,t.ticket_no,t.user_id,u.username,coalesce(u.display_name,''),
		       t.subject,t.category,t.priority,t.status,
		       coalesce(t.assigned_to,0),coalesce(a.username,''),
		       coalesce(cast(t.tags as char),'[]'),t.language,
		       coalesce(date_format(t.first_response_due_at,'%Y-%m-%d %H:%i:%s'),''),
		       coalesce(date_format(t.resolution_due_at,'%Y-%m-%d %H:%i:%s'),''),
		       coalesce(date_format(t.first_responded_at,'%Y-%m-%d %H:%i:%s'),''),
		       coalesce(date_format(t.resolved_at,'%Y-%m-%d %H:%i:%s'),''),
		       coalesce(date_format(t.closed_at,'%Y-%m-%d %H:%i:%s'),''),
		       coalesce(` + ticketFirstResponseOverdueSQL + `,0),
		       coalesce(` + ticketResolutionOverdueSQL + `,0),
		       t.last_message_preview,date_format(t.last_message_at,'%Y-%m-%d %H:%i:%s'),
		       t.last_reply_by,t.message_count,` + unreadColumn + `,
		       date_format(t.created_at,'%Y-%m-%d %H:%i:%s'),
		       date_format(t.updated_at,'%Y-%m-%d %H:%i:%s')
		from bp_ticket t
		inner join bp_user u on u.id=t.user_id
		left join bp_user a on a.id=t.assigned_to
	`
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTicket(row rowScanner) (Ticket, error) {
	var item Ticket
	var id, userID, assignedTo int64
	var firstResponseLate, resolutionLate int
	var tagsText string
	err := row.Scan(
		&id,
		&item.TicketNo,
		&userID,
		&item.Username,
		&item.DisplayName,
		&item.Subject,
		&item.Category,
		&item.Priority,
		&item.Status,
		&assignedTo,
		&item.AssigneeName,
		&tagsText,
		&item.Language,
		&item.FirstResponseDueAt,
		&item.ResolutionDueAt,
		&item.FirstRespondedAt,
		&item.ResolvedAt,
		&item.ClosedAt,
		&firstResponseLate,
		&resolutionLate,
		&item.LastMessagePreview,
		&item.LastMessageAt,
		&item.LastReplyBy,
		&item.MessageCount,
		&item.UnreadCount,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if err != nil {
		return Ticket{}, err
	}
	item.ID = strconv.FormatInt(id, 10)
	item.UserID = strconv.FormatInt(userID, 10)
	if assignedTo > 0 {
		item.AssignedTo = strconv.FormatInt(assignedTo, 10)
	}
	item.Tags = make([]string, 0)
	if err := json.Unmarshal([]byte(tagsText), &item.Tags); err != nil {
		return Ticket{}, err
	}
	item.FirstResponseLate = firstResponseLate == 1
	item.ResolutionLate = resolutionLate == 1
	item.SLAStatus = ticketSLAStatus(item)
	return item, nil
}

func scanTickets(rows *sql.Rows) ([]Ticket, error) {
	items := make([]Ticket, 0)
	for rows.Next() {
		item, err := scanTicket(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func ticketWhere(userID int64, admin bool, params ListParams) (string, []any) {
	where := []string{"where 1=1"}
	args := make([]any, 0, 8)
	if !admin {
		where = append(where, "t.user_id=?")
		args = append(args, userID)
	}
	if params.Status != "" {
		where = append(where, "t.status=?")
		args = append(args, params.Status)
	}
	if params.Priority != "" {
		where = append(where, "t.priority=?")
		args = append(args, params.Priority)
	}
	if params.Category != "" {
		where = append(where, "t.category=?")
		args = append(args, params.Category)
	}
	if params.Unread {
		unreadColumn := "t.unread_user"
		if admin {
			unreadColumn = "t.unread_admin"
		}
		where = append(where, unreadColumn+">0")
	}
	// 处理人筛选只对管理端有意义；普通用户列表始终按 user_id 收口。
	if admin && params.AssignedTo > 0 {
		where = append(where, "t.assigned_to=?")
		args = append(args, params.AssignedTo)
	}
	if params.CreatedFrom != "" {
		where = append(where, "t.created_at>=?")
		args = append(args, params.CreatedFrom)
	}
	if params.CreatedTo != "" {
		where = append(where, "t.created_at<date_add(?, interval 1 day)")
		args = append(args, params.CreatedTo)
	}
	if params.UpdatedFrom != "" {
		where = append(where, "t.updated_at>=?")
		args = append(args, params.UpdatedFrom)
	}
	if params.UpdatedTo != "" {
		where = append(where, "t.updated_at<date_add(?, interval 1 day)")
		args = append(args, params.UpdatedTo)
	}
	if params.Overdue {
		where = append(where, "("+ticketFirstResponseOverdueSQL+" or "+ticketResolutionOverdueSQL+")")
	}
	if params.Keyword != "" {
		like := "%" + params.Keyword + "%"
		where = append(where, "(t.ticket_no like ? or t.subject like ? or u.username like ? or u.display_name like ?)")
		args = append(args, like, like, like, like)
	}
	return strings.Join(where, " and "), args
}
