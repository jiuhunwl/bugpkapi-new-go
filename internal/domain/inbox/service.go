package inbox

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bugpk-api/internal/domain/notification"
	"bugpk-api/internal/domain/rbac"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/pagination"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	KindSystem       = "SYSTEM"
	maxTitleChars    = 128
	maxBodyChars     = 4000
	defaultPageSize  = 20
	maxPageSize      = 100
	emailChunkSize   = 200
	emailTimeout     = 5 * time.Second
	broadcastTimeout = 30 * time.Second
)

// EmailNotifier 复用统一邮件 outbox（与 ticket/content/payment 相同的注入模式）。
type EmailNotifier interface {
	Enqueue(context.Context, notification.EnqueueRequest) error
}

type Principal struct {
	ID           int64
	Username     string
	Roles        []string
	TokenVersion int
}

type Service struct {
	repo     *Repository
	authz    *rbac.Service
	notifier EmailNotifier
}

func NewService(repo *Repository, authz *rbac.Service) *Service {
	return &Service{repo: repo, authz: authz}
}

// SetNotifier 注入邮件发送器；未注入时广播的邮件副本静默跳过。
func (s *Service) SetNotifier(notifier EmailNotifier) {
	if s != nil {
		s.notifier = notifier
	}
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	principal, err := s.authz.Authenticate(ctx, token)
	if err != nil {
		return Principal{}, err
	}
	return Principal{ID: principal.ID, Username: principal.Username, Roles: principal.Roles, TokenVersion: principal.TokenVersion}, nil
}

func (s *Service) require(ctx context.Context, principal Principal, code string) error {
	return s.authz.Require(ctx, rbac.Principal{ID: principal.ID, Username: principal.Username, Roles: principal.Roles, TokenVersion: principal.TokenVersion}, code)
}

// Broadcast 向全部有效用户或指定用户发送站内消息，可选同时发送邮件副本。
// 站内消息落库失败会直接报错；邮件副本逐条投递到 outbox，失败仅计数。
func (s *Service) Broadcast(ctx context.Context, principal Principal, req BroadcastRequest) (BroadcastResult, error) {
	if err := s.require(ctx, principal, "system:inbox:broadcast"); err != nil {
		return BroadcastResult{}, err
	}
	title := strings.TrimSpace(req.Title)
	body := strings.TrimSpace(req.Body)
	if title == "" {
		return BroadcastResult{}, apperrors.New(consts.StatusBadRequest, "通知标题不能为空")
	}
	if len([]rune(title)) > maxTitleChars {
		return BroadcastResult{}, apperrors.New(consts.StatusBadRequest, fmt.Sprintf("通知标题不能超过 %d 字", maxTitleChars))
	}
	if len([]rune(body)) > maxBodyChars {
		return BroadcastResult{}, apperrors.New(consts.StatusBadRequest, fmt.Sprintf("通知内容不能超过 %d 字", maxBodyChars))
	}
	var recipients []Recipient
	if req.UserID > 0 {
		recipient, err := s.repo.Recipient(ctx, req.UserID)
		if err != nil {
			return BroadcastResult{}, apperrors.Wrap(consts.StatusBadRequest, "接收用户不存在或已停用", err)
		}
		recipients = []Recipient{recipient}
	} else {
		all, err := s.repo.AllRecipients(ctx)
		if err != nil {
			return BroadcastResult{}, err
		}
		recipients = all
	}
	if len(recipients) == 0 {
		return BroadcastResult{}, apperrors.New(consts.StatusBadRequest, "没有可接收通知的用户")
	}
	userIDs := make([]int64, 0, len(recipients))
	for _, recipient := range recipients {
		userIDs = append(userIDs, recipient.ID)
	}
	if err := s.repo.InsertBatch(ctx, title, body, KindSystem, userIDs); err != nil {
		return BroadcastResult{}, err
	}
	result := BroadcastResult{Recipients: int64(len(recipients))}
	if !req.SendEmail || s.notifier == nil {
		return result, nil
	}
	// 邮件副本与广播内容绑定去重：同一内容对同一用户只投递一次。
	sum := md5.Sum([]byte(title + "\x00" + body))
	base := hex.EncodeToString(sum[:8])
	for start := 0; start < len(recipients); start += emailChunkSize {
		end := start + emailChunkSize
		if end > len(recipients) {
			end = len(recipients)
		}
		chunk := recipients[start:end]
		batchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), broadcastTimeout)
		var wg sync.WaitGroup
		for _, recipient := range chunk {
			if strings.TrimSpace(recipient.Email) == "" {
				continue
			}
			wg.Add(1)
			go func(recipient Recipient) {
				defer wg.Done()
				emailCtx, emailCancel := context.WithTimeout(batchCtx, emailTimeout)
				defer emailCancel()
				if err := s.notifier.Enqueue(emailCtx, notification.EnqueueRequest{
					DedupeKey: "inbox.broadcast:" + base + ":" + strconv.FormatInt(recipient.ID, 10),
					Payload: map[string]any{
						"body":  body,
						"title": title,
					},
					Recipient:   strings.TrimSpace(recipient.Email),
					TemplateKey: notification.TemplateBroadcast,
				}); err == nil {
					atomic.AddInt64(&result.Emails, 1)
				}
			}(recipient)
		}
		wg.Wait()
		cancel()
	}
	return result, nil
}

// MyMessages 返回用户自己的消息分页（unreadOnly=true 时仅未读）。
func (s *Service) MyMessages(ctx context.Context, principal Principal, unreadOnly bool, page, pageSize int) (Page, error) {
	if principal.ID <= 0 {
		return Page{}, apperrors.New(consts.StatusUnauthorized, "请先登录")
	}
	page, pageSize = pagination.Normalize(page, pageSize, maxPageSize)
	items, total, err := s.repo.ListByUser(ctx, principal.ID, unreadOnly, (page-1)*pageSize, pageSize)
	if err != nil {
		return Page{}, err
	}
	unread, err := s.repo.CountUnread(ctx, principal.ID)
	if err != nil {
		return Page{}, err
	}
	return Page{Items: items, Total: total, Unread: unread, Page: page, PageSize: pageSize}, nil
}

// UnreadCount 返回当前用户未读消息数（工作台徽标轮询用）。
func (s *Service) UnreadCount(ctx context.Context, principal Principal) (int64, error) {
	if principal.ID <= 0 {
		return 0, apperrors.New(consts.StatusUnauthorized, "请先登录")
	}
	return s.repo.CountUnread(ctx, principal.ID)
}

// MarkRead 将用户自己的消息置为已读；ids 为空表示全部已读。
func (s *Service) MarkRead(ctx context.Context, principal Principal, ids []int64) (int64, error) {
	if principal.ID <= 0 {
		return 0, apperrors.New(consts.StatusUnauthorized, "请先登录")
	}
	if len(ids) > 200 {
		return 0, apperrors.New(consts.StatusBadRequest, "单次最多标记 200 条")
	}
	for _, id := range ids {
		if id <= 0 {
			return 0, apperrors.New(consts.StatusBadRequest, "消息 ID 不合法")
		}
	}
	return s.repo.MarkRead(ctx, principal.ID, ids)
}
