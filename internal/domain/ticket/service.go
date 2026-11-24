package ticket

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"bugpk-api/internal/domain/notification"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/emaillang"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

const (
	maxTicketContentRunes = 4000
	maxTicketKeywordRunes = 128
	maxTicketSubjectRunes = 128
	maxTicketAttachments  = 10
	ticketPublishTimeout  = 2 * time.Second
)

var (
	errTicketClosed          = errors.New("ticket is closed")
	errAttachmentUnavailable = errors.New("ticket attachment is unavailable")
)

type Service struct {
	authz    *rbac.Service
	notifier notificationEnqueuer
	rdb      *redis.Client
	repo     *Repository
}

type Subscription struct {
	Events <-chan Event
	close  func() error
}

func NewService(repo *Repository, authz *rbac.Service, rdb *redis.Client) *Service {
	return &Service{authz: authz, rdb: rdb, repo: repo}
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	return s.authz.Authenticate(ctx, token)
}

// AccountActive reports whether an already authenticated account may still use
// the product. The live stream uses it to evict a banned account mid-stream.
func (s *Service) AccountActive(ctx context.Context, userID int64) (bool, error) {
	return s.repo.AccountActive(ctx, userID)
}

func (s *Service) List(ctx context.Context, principal Principal, params ListParams) (PageResult, error) {
	admin, err := s.isAdmin(ctx, principal)
	if err != nil {
		return PageResult{}, err
	}
	if !admin {
		if err := s.authz.Require(ctx, principal, "ticket:user:list"); err != nil {
			return PageResult{}, err
		}
	}
	params, err = normalizeListParams(params)
	if err != nil {
		return PageResult{}, err
	}
	return s.repo.List(ctx, principal.ID, admin, params)
}

func (s *Service) Detail(ctx context.Context, principal Principal, ticketID int64) (Detail, error) {
	admin, err := s.isAdmin(ctx, principal)
	if err != nil {
		return Detail{}, err
	}
	if !admin {
		if err := s.authz.Require(ctx, principal, "ticket:user:list"); err != nil {
			return Detail{}, err
		}
	}
	result, err := s.repo.Detail(ctx, ticketID, principal.ID, admin)
	return result, mapTicketError(err)
}

// UserProfile returns the account card shown when a ticket administrator clicks
// an avatar. Only ticket administrators may read it; contact fields stay masked.
func (s *Service) UserProfile(ctx context.Context, principal Principal, userID int64) (UserProfile, error) {
	admin, err := s.isAdmin(ctx, principal)
	if err != nil {
		return UserProfile{}, err
	}
	if !admin {
		return UserProfile{}, apperrors.New(consts.StatusForbidden, "Forbidden Exception")
	}
	profile, err := s.repo.UserProfile(ctx, userID)
	if errors.Is(err, sql.ErrNoRows) {
		return UserProfile{}, apperrors.New(consts.StatusNotFound, "用户不存在")
	}
	return profile, err
}

func (s *Service) Create(ctx context.Context, principal Principal, req CreateRequest) (Detail, error) {
	if err := s.authz.Require(ctx, principal, "ticket:user:create"); err != nil {
		return Detail{}, err
	}
	normalized, err := normalizeCreateRequest(req)
	if err != nil {
		return Detail{}, err
	}
	ticketID, err := s.repo.Create(ctx, newTicketNo(), principal.ID, normalized)
	if err != nil {
		return Detail{}, err
	}
	result, err := s.repo.Detail(ctx, ticketID, principal.ID, false)
	if err != nil {
		return Detail{}, err
	}
	s.publish(ctx, Event{
		ActorID:  strconv.FormatInt(principal.ID, 10),
		TicketID: strconv.FormatInt(ticketID, 10),
		Type:     "ticket.created",
		UserID:   strconv.FormatInt(principal.ID, 10),
	})
	s.notifyTicketAdmins(
		ctx,
		notification.TemplateTicketCreated,
		"created:"+result.Ticket.ID,
		result.Ticket,
		normalized.Content,
	)
	return result, nil
}

func (s *Service) Reply(ctx context.Context, principal Principal, ticketID int64, req ReplyRequest) (Detail, error) {
	attachmentIDs, err := normalizeAttachmentIDs(req.AttachmentIDs)
	if err != nil {
		return Detail{}, err
	}
	content, err := normalizeReplyContent(req.Content, len(attachmentIDs) > 0)
	if err != nil {
		return Detail{}, err
	}
	admin, err := s.isAdmin(ctx, principal)
	if err != nil {
		return Detail{}, err
	}
	permission := "ticket:user:reply"
	if admin {
		permission = "ticket:admin:reply"
	}
	if err := s.authz.Require(ctx, principal, permission); err != nil {
		return Detail{}, err
	}
	messageResult, err := s.repo.AddMessage(ctx, ticketID, principal.ID, admin, content, attachmentIDs...)
	if err != nil {
		return Detail{}, mapTicketError(err)
	}
	result, err := s.repo.Detail(ctx, ticketID, principal.ID, admin)
	if err != nil {
		return Detail{}, mapTicketError(err)
	}
	preview := content
	if preview == "" {
		preview = attachmentPreview(result.Ticket.Language)
	}
	s.publish(ctx, Event{
		ActorID:   strconv.FormatInt(principal.ID, 10),
		MessageID: strconv.FormatInt(messageResult.MessageID, 10),
		TicketID:  strconv.FormatInt(ticketID, 10),
		Type:      "ticket.message",
		UserID:    strconv.FormatInt(messageResult.UserID, 10),
	})
	if admin {
		s.notifyTicketOwner(
			ctx,
			notification.TemplateTicketStaffReply,
			"staff-reply:"+strconv.FormatInt(messageResult.MessageID, 10),
			result.Ticket,
			preview,
		)
	} else {
		s.notifyTicketAdmins(
			ctx,
			notification.TemplateTicketUserReply,
			"user-reply:"+strconv.FormatInt(messageResult.MessageID, 10),
			result.Ticket,
			preview,
		)
	}
	return result, nil
}

func (s *Service) Update(ctx context.Context, principal Principal, ticketID int64, req UpdateRequest) (Detail, error) {
	if err := s.authz.Require(ctx, principal, "ticket:admin:update"); err != nil {
		return Detail{}, err
	}
	update, err := normalizeUpdateRequest(req)
	if err != nil {
		return Detail{}, err
	}
	ownerID, err := s.repo.Update(ctx, ticketID, principal.ID, update)
	if err != nil {
		return Detail{}, mapTicketError(err)
	}
	result, err := s.repo.Detail(ctx, ticketID, principal.ID, true)
	if err != nil {
		return Detail{}, mapTicketError(err)
	}
	s.publish(ctx, Event{
		ActorID:  strconv.FormatInt(principal.ID, 10),
		TicketID: strconv.FormatInt(ticketID, 10),
		Type:     "ticket.updated",
		UserID:   strconv.FormatInt(ownerID, 10),
	})
	if update.Status != "" {
		s.notifyTicketOwner(
			ctx,
			notification.TemplateTicketStatus,
			"status:"+update.Status+":"+result.Ticket.UpdatedAt,
			result.Ticket,
			"",
		)
	}
	return result, nil
}

func (s *Service) MarkRead(ctx context.Context, principal Principal, ticketID int64, req MarkReadRequest) (bool, error) {
	admin, err := s.isAdmin(ctx, principal)
	if err != nil {
		return false, err
	}
	if !admin {
		if err := s.authz.Require(ctx, principal, "ticket:user:list"); err != nil {
			return false, err
		}
	}
	snapshot, err := normalizeMarkReadRequest(req)
	if err != nil {
		return false, err
	}
	updated, err := s.repo.MarkRead(
		ctx,
		ticketID,
		principal.ID,
		admin,
		snapshot.LastMessageAt,
		snapshot.MessageCount,
	)
	return updated, mapTicketError(err)
}

func (s *Service) Subscribe(ctx context.Context, principal Principal, ticketID int64) (*Subscription, error) {
	admin, err := s.isAdmin(ctx, principal)
	if err != nil {
		return nil, err
	}
	if !admin {
		if err := s.authz.Require(ctx, principal, "ticket:user:list"); err != nil {
			return nil, err
		}
	}
	if ticketID > 0 {
		if _, err := s.repo.CanView(ctx, ticketID, principal.ID, admin); err != nil {
			return nil, mapTicketError(err)
		}
	}
	if s.rdb == nil {
		return nil, apperrors.New(consts.StatusServiceUnavailable, "工单实时消息服务暂不可用")
	}
	channel := userTicketChannel(principal.ID)
	if admin {
		channel = adminTicketChannel()
	}
	subCtx, cancel := context.WithCancel(context.Background())
	pubsub := s.rdb.Subscribe(subCtx, channel)
	if _, err := pubsub.Receive(ctx); err != nil {
		cancel()
		_ = pubsub.Close()
		return nil, apperrors.Wrap(consts.StatusServiceUnavailable, "工单实时消息连接失败", err)
	}
	events := make(chan Event, 32)
	go func() {
		defer close(events)
		for message := range pubsub.Channel(redis.WithChannelSize(64)) {
			var event Event
			if json.Unmarshal([]byte(message.Payload), &event) != nil {
				continue
			}
			if ticketID > 0 && event.TicketID != strconv.FormatInt(ticketID, 10) {
				continue
			}
			select {
			case events <- event:
			case <-subCtx.Done():
				return
			}
		}
	}()
	var once sync.Once
	return &Subscription{
		Events: events,
		close: func() error {
			var closeErr error
			once.Do(func() {
				cancel()
				closeErr = pubsub.Close()
			})
			return closeErr
		},
	}, nil
}

func (s *Subscription) Close() error {
	if s == nil || s.close == nil {
		return nil
	}
	return s.close()
}

func (s *Service) isAdmin(ctx context.Context, principal Principal) (bool, error) {
	return s.authz.HasPermission(ctx, principal.ID, principal.Roles, "ticket:admin:list")
}

func (s *Service) publish(ctx context.Context, event Event) {
	if s.rdb == nil {
		return
	}
	if event.ID == "" {
		event.ID = strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	if event.Timestamp == "" {
		event.Timestamp = time.Now().Format(time.RFC3339Nano)
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	publishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ticketPublishTimeout)
	defer cancel()
	pipe := s.rdb.Pipeline()
	pipe.Publish(publishCtx, adminTicketChannel(), payload)
	if userID, parseErr := strconv.ParseInt(event.UserID, 10, 64); parseErr == nil && userID > 0 {
		pipe.Publish(publishCtx, userTicketChannel(userID), payload)
	}
	_, _ = pipe.Exec(publishCtx)
}

func normalizeCreateRequest(req CreateRequest) (CreateRequest, error) {
	req.Subject = strings.TrimSpace(req.Subject)
	if req.Subject == "" || utf8.RuneCountInString(req.Subject) > maxTicketSubjectRunes || strings.ContainsRune(req.Subject, '\x00') {
		return CreateRequest{}, apperrors.New(consts.StatusBadRequest, "工单标题不能为空且不能超过128个字符")
	}
	req.Category = strings.ToUpper(strings.TrimSpace(req.Category))
	if req.Category == "" {
		req.Category = CategoryGeneral
	}
	if !validTicketCategory(req.Category) {
		return CreateRequest{}, apperrors.New(consts.StatusBadRequest, "工单分类不合法")
	}
	req.Priority = strings.ToUpper(strings.TrimSpace(req.Priority))
	if req.Priority == "" {
		req.Priority = PriorityNormal
	}
	if !validTicketPriority(req.Priority) {
		return CreateRequest{}, apperrors.New(consts.StatusBadRequest, "工单优先级不合法")
	}
	content, err := normalizeTicketContent(req.Content)
	if err != nil {
		return CreateRequest{}, err
	}
	req.Content = content
	req.Language = normalizeTicketLanguage(req.Language)
	return req, nil
}

func normalizeListParams(params ListParams) (ListParams, error) {
	params.Keyword = strings.TrimSpace(params.Keyword)
	if utf8.RuneCountInString(params.Keyword) > maxTicketKeywordRunes {
		return ListParams{}, apperrors.New(consts.StatusBadRequest, "搜索关键字不能超过128个字符")
	}
	params.Status = strings.ToUpper(strings.TrimSpace(params.Status))
	params.Priority = strings.ToUpper(strings.TrimSpace(params.Priority))
	params.Category = strings.ToUpper(strings.TrimSpace(params.Category))
	if params.Status != "" && !validTicketStatus(params.Status) {
		return ListParams{}, apperrors.New(consts.StatusBadRequest, "工单状态不合法")
	}
	if params.Priority != "" && !validTicketPriority(params.Priority) {
		return ListParams{}, apperrors.New(consts.StatusBadRequest, "工单优先级不合法")
	}
	if params.Category != "" && !validTicketCategory(params.Category) {
		return ListParams{}, apperrors.New(consts.StatusBadRequest, "工单分类不合法")
	}
	var err error
	if params.CreatedFrom, err = normalizeTicketDate(params.CreatedFrom); err != nil {
		return ListParams{}, err
	}
	if params.CreatedTo, err = normalizeTicketDate(params.CreatedTo); err != nil {
		return ListParams{}, err
	}
	if params.UpdatedFrom, err = normalizeTicketDate(params.UpdatedFrom); err != nil {
		return ListParams{}, err
	}
	if params.UpdatedTo, err = normalizeTicketDate(params.UpdatedTo); err != nil {
		return ListParams{}, err
	}
	if params.CreatedFrom != "" && params.CreatedTo != "" && params.CreatedFrom > params.CreatedTo {
		return ListParams{}, apperrors.New(consts.StatusBadRequest, "创建时间范围不合法")
	}
	if params.UpdatedFrom != "" && params.UpdatedTo != "" && params.UpdatedFrom > params.UpdatedTo {
		return ListParams{}, apperrors.New(consts.StatusBadRequest, "更新时间范围不合法")
	}
	params.SortBy = strings.ToLower(strings.TrimSpace(params.SortBy))
	switch params.SortBy {
	case "", "created_at", "last_message_at", "priority", "sla_due", "status", "updated_at":
	default:
		return ListParams{}, apperrors.New(consts.StatusBadRequest, "排序字段不合法")
	}
	params.SortOrder = strings.ToLower(strings.TrimSpace(params.SortOrder))
	if params.SortOrder == "" {
		params.SortOrder = "desc"
	}
	if params.SortOrder != "asc" && params.SortOrder != "desc" {
		return ListParams{}, apperrors.New(consts.StatusBadRequest, "排序方向不合法")
	}
	if params.AssignedTo < 0 {
		params.AssignedTo = 0
	}
	return params, nil
}

// normalizeTicketDate 只接受 YYYY-MM-DD，避免把时区/时间戳差异带进 SQL 日期比较。
func normalizeTicketDate(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return "", apperrors.New(consts.StatusBadRequest, "日期格式应为 YYYY-MM-DD")
	}
	return parsed.Format("2006-01-02"), nil
}

func normalizeUpdateRequest(req UpdateRequest) (ticketUpdate, error) {
	var result ticketUpdate
	if req.Status != nil {
		status := strings.ToUpper(strings.TrimSpace(*req.Status))
		if !validTicketStatus(status) {
			return ticketUpdate{}, apperrors.New(consts.StatusBadRequest, "工单状态不合法")
		}
		result.Status = status
	}
	if req.AssignedTo != nil {
		result.HasAssignment = true
		value := strings.TrimSpace(*req.AssignedTo)
		if value != "" {
			assignedTo, err := strconv.ParseInt(value, 10, 64)
			if err != nil || assignedTo <= 0 {
				return ticketUpdate{}, apperrors.New(consts.StatusBadRequest, "处理人账号不合法")
			}
			result.AssignedTo = assignedTo
		}
	}
	if req.Tags != nil {
		tags, err := normalizeTicketTags(*req.Tags)
		if err != nil {
			return ticketUpdate{}, err
		}
		result.HasTags = true
		result.Tags = tags
	}
	if result.Status == "" && !result.HasAssignment && !result.HasTags {
		return ticketUpdate{}, apperrors.New(consts.StatusBadRequest, "没有可更新的工单字段")
	}
	return result, nil
}

func normalizeMarkReadRequest(req MarkReadRequest) (MarkReadRequest, error) {
	const layout = "2006-01-02 15:04:05"
	value := strings.TrimSpace(req.LastMessageAt)
	parsed, err := time.Parse(layout, value)
	if err != nil || parsed.Format(layout) != value || req.MessageCount <= 0 {
		return MarkReadRequest{}, apperrors.New(consts.StatusBadRequest, "工单消息快照不合法")
	}
	req.LastMessageAt = value
	return req, nil
}

func normalizeTicketContent(content string) (string, error) {
	content = strings.TrimSpace(content)
	if content == "" || utf8.RuneCountInString(content) > maxTicketContentRunes || strings.ContainsRune(content, '\x00') {
		return "", apperrors.New(consts.StatusBadRequest, "工单内容不能为空且不能超过4000个字符")
	}
	return content, nil
}

func normalizeReplyContent(content string, hasAttachments bool) (string, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		if hasAttachments {
			return "", nil
		}
		return "", apperrors.New(consts.StatusBadRequest, "工单内容不能为空且不能超过4000个字符")
	}
	if utf8.RuneCountInString(content) > maxTicketContentRunes || strings.ContainsRune(content, '\x00') {
		return "", apperrors.New(consts.StatusBadRequest, "工单内容不能为空且不能超过4000个字符")
	}
	return content, nil
}

func normalizeAttachmentIDs(values []string) ([]int64, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make([]int64, 0, min(len(values), maxTicketAttachments))
	seen := make(map[int64]struct{}, len(values))
	for _, raw := range values {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return nil, apperrors.New(consts.StatusBadRequest, "附件ID不合法")
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	if len(result) > maxTicketAttachments {
		return nil, apperrors.New(consts.StatusBadRequest, "单条消息最多关联10个附件")
	}
	return result, nil
}

func attachmentPreview(language string) string {
	if emaillang.Resolve(language, "", "", "") == emaillang.English {
		return "[Image]"
	}
	return "[图片]"
}

func messagePreview(content string) string {
	value := strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	runes := []rune(value)
	if len(runes) > 120 {
		return string(runes[:120]) + "…"
	}
	return value
}

func nextStatusAfterReply(current string, staff bool) string {
	if staff {
		if current == StatusClosed {
			return StatusClosed
		}
		return StatusWaitingUser
	}
	if current == StatusClosed {
		return StatusClosed
	}
	return StatusOpen
}

func validTicketCategory(value string) bool {
	switch value {
	case CategoryGeneral, CategoryAPI, CategoryBilling, CategoryAccount, CategoryBug, CategoryOther:
		return true
	default:
		return false
	}
}

func validTicketPriority(value string) bool {
	switch value {
	case PriorityLow, PriorityNormal, PriorityHigh, PriorityUrgent:
		return true
	default:
		return false
	}
}

func validTicketStatus(value string) bool {
	switch value {
	case StatusOpen, StatusProcessing, StatusWaitingUser, StatusResolved, StatusClosed:
		return true
	default:
		return false
	}
}

func newTicketNo() string {
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return fmt.Sprintf("TK%s-%d", time.Now().Format("20060102"), time.Now().UnixNano())
	}
	return "TK" + time.Now().Format("20060102") + "-" + strings.ToUpper(hex.EncodeToString(random))
}

func adminTicketChannel() string {
	return "ticket:events:admin"
}

func userTicketChannel(userID int64) string {
	return fmt.Sprintf("ticket:events:user:%d", userID)
}

func mapTicketError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, sql.ErrNoRows):
		return apperrors.New(consts.StatusNotFound, "工单不存在或无权访问")
	case errors.Is(err, errTicketClosed):
		return apperrors.New(consts.StatusConflict, "工单已关闭，不能继续回复")
	case errors.Is(err, errAttachmentUnavailable):
		return apperrors.New(consts.StatusBadRequest, "附件不存在、不属于当前账号或已被其他消息使用")
	default:
		return err
	}
}
