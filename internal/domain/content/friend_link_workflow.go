package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"

	"bugpk-api/internal/domain/notification"
	"bugpk-api/internal/pkg/emaillang"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const friendLinkNotificationTimeout = 2 * time.Second

type captchaConsumer interface {
	Consume(ctx context.Context, captchaID, scene, binding string) error
}

type notificationEnqueuer interface {
	Enqueue(context.Context, notification.EnqueueRequest) error
}

type reviewDecision struct {
	Reason         string
	ReasonTemplate string
	SendEmail      bool
}

func (s *Service) SetCaptchaConsumer(consumer captchaConsumer) {
	if s != nil {
		s.captcha = consumer
	}
}

func (s *Service) SetNotifier(notifier notificationEnqueuer) {
	if s != nil {
		s.notifier = notifier
	}
}

func normalizeReviewDecision(
	req ReviewApplicationRequest,
	reasonRequired bool,
) (reviewDecision, error) {
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = strings.TrimSpace(req.RejectReason)
	}
	if reasonRequired && reason == "" {
		return reviewDecision{}, apperrors.New(consts.StatusBadRequest, "reason is required")
	}
	if utf8.RuneCountInString(reason) > 1000 || strings.ContainsRune(reason, '\x00') {
		return reviewDecision{}, apperrors.New(consts.StatusBadRequest, "reason is too long")
	}
	reasonTemplate := strings.TrimSpace(req.ReasonTemplate)
	if utf8.RuneCountInString(reasonTemplate) > 64 || strings.ContainsRune(reasonTemplate, '\x00') {
		return reviewDecision{}, apperrors.New(consts.StatusBadRequest, "reasonTemplate is invalid")
	}
	sendEmail := true
	if req.SendEmail != nil {
		sendEmail = *req.SendEmail
	}
	return reviewDecision{
		Reason:         reason,
		ReasonTemplate: reasonTemplate,
		SendEmail:      sendEmail,
	}, nil
}

func normalizeFriendLinkLanguage(value string) string {
	return emaillang.Resolve(value, "", "", "")
}

func localizedFriendLinkReason(templateKey, fallback, language string) string {
	templateKey = strings.ToLower(strings.TrimSpace(templateKey))
	fallback = strings.TrimSpace(fallback)
	if templateKey == "" || templateKey == "custom" {
		return fallback
	}

	zh := map[string]string{
		"approved":               "网站信息完整且符合本站友情链接交换条件，申请已通过。",
		"backlink_missing":       "暂未在您填写的页面检测到本站友情链接，请添加后重新提交申请。",
		"backlink_removed":       "检测到贵站已移除本站友情链接，因此本次同步移除对应友链。",
		"content_changed":        "网站内容或定位发生较大变化，当前已不符合本站友情链接交换要求。",
		"content_mismatch":       "当前网站内容或定位暂不符合本站友情链接交换要求。",
		"inactive":               "网站长期无法稳定访问或缺少维护，本次清理对应友情链接。",
		"information_incomplete": "申请信息不完整，请补充可访问的网站地址、联系方式及网站说明后重新提交。",
		"site_unreachable":       "目标网站当前无法正常访问，请确认网站可公开访问后再重新申请。",
	}
	en := map[string]string{
		"approved":               "The site information is complete and meets our friend-link exchange requirements. The application has been approved.",
		"backlink_missing":       "We could not find our link on the provided page. Please add it and submit a new application.",
		"backlink_removed":       "Our link is no longer present on your site, so the corresponding friend link has been removed.",
		"content_changed":        "The site content or focus has changed substantially and no longer meets our friend-link exchange requirements.",
		"content_mismatch":       "The current site content or positioning does not meet our friend-link exchange requirements.",
		"inactive":               "The site has been unstable or unmaintained for an extended period, so the friend link has been removed.",
		"information_incomplete": "The application is incomplete. Please provide an accessible site URL, contact details, and a site description before applying again.",
		"site_unreachable":       "The target site is currently unreachable. Please make it publicly accessible before applying again.",
	}

	translations := zh
	if normalizeFriendLinkLanguage(language) == "en" {
		translations = en
	}
	if reason := translations[templateKey]; reason != "" {
		return reason
	}
	return fallback
}

func (s *Service) notifyFriendApplication(
	ctx context.Context,
	templateKey string,
	event string,
	app FriendLinkApplication,
	reason string,
) {
	if s == nil || s.notifier == nil || strings.TrimSpace(app.ContactEmail) == "" {
		return
	}
	s.enqueueFriendNotification(ctx, notification.EnqueueRequest{
		DedupeKey:   friendLinkNotificationDedupe("application", app.ID, event, app.ContactEmail),
		Language:    app.Language,
		Payload:     friendLinkNotificationPayload(app.SiteName, app.SiteURL, reason),
		Recipient:   app.ContactEmail,
		TemplateKey: templateKey,
	})
}

func (s *Service) notifyFriendApplicationSubmitted(
	ctx context.Context,
	app FriendLinkApplication,
) {
	if s == nil || s.notifier == nil {
		return
	}
	payload := friendLinkNotificationPayload(app.SiteName, app.SiteURL, "")
	payload["applicant"] = strings.TrimSpace(app.Username)
	payload["contactEmail"] = strings.TrimSpace(app.ContactEmail)
	payload["changeKind"] = normalizeFriendLinkChangeKind(app.ChangeKind)

	// 申请人回执：提交成功后立刻确认「已收到」，与审核结果邮件分开。
	if email := strings.TrimSpace(app.ContactEmail); email != "" {
		s.enqueueFriendNotification(ctx, notification.EnqueueRequest{
			DedupeKey:   friendLinkNotificationDedupe("application", app.ID, "received", email),
			Language:    app.Language,
			Payload:     payload,
			Recipient:   email,
			TemplateKey: notification.TemplateFriendReceived,
		})
	}

	// 管理员/审核人待审提醒：复用 ticket 的收件人口径，管理员角色或显式授予
	// friend-link:application:review 权限的账号都会收到。
	if s.repo == nil {
		return
	}
	emails, err := s.repo.FriendLinkReviewerEmails(ctx)
	if err != nil {
		return
	}
	for _, email := range emails {
		s.enqueueFriendNotification(ctx, notification.EnqueueRequest{
			DedupeKey:   friendLinkNotificationDedupe("application", app.ID, "pending", email),
			Language:    app.Language,
			Payload:     payload,
			Recipient:   email,
			TemplateKey: notification.TemplateFriendPending,
		})
	}
}

func (s *Service) notifyDeletedFriendLink(
	ctx context.Context,
	event string,
	item FriendLinkItem,
	reason string,
) {
	if s == nil || s.notifier == nil || strings.TrimSpace(item.ApplicantEmail) == "" {
		return
	}
	s.enqueueFriendNotification(ctx, notification.EnqueueRequest{
		DedupeKey:   friendLinkNotificationDedupe("link", item.ID, event, item.ApplicantEmail),
		Language:    item.Language,
		Payload:     friendLinkNotificationPayload(item.SiteName, item.SiteURL, reason),
		Recipient:   item.ApplicantEmail,
		TemplateKey: notification.TemplateFriendDeleted,
	})
}

func (s *Service) enqueueFriendNotification(
	ctx context.Context,
	req notification.EnqueueRequest,
) {
	// Keep the producer contract deterministic even when a lightweight
	// notifier is injected in tests or another delivery implementation is used.
	req.Language = normalizeFriendLinkLanguage(req.Language)
	enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), friendLinkNotificationTimeout)
	defer cancel()
	_ = s.notifier.Enqueue(enqueueCtx, req)
}

func friendLinkNotificationPayload(siteName, siteURL, reason string) map[string]any {
	return map[string]any{
		"reason":   strings.TrimSpace(reason),
		"siteName": strings.TrimSpace(siteName),
		"siteUrl":  strings.TrimSpace(siteURL),
	}
}

func friendLinkNotificationDedupe(entity, id, event, recipient string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(recipient))))
	return "friend-link:" + entity + ":" + strings.TrimSpace(id) + ":" +
		strings.TrimSpace(event) + ":" + hex.EncodeToString(sum[:8])
}

// FriendLinkReviewerEmails 返回友链待审提醒的收件人：启用中的 admin 角色账号，
// 以及被显式授予 friend-link:application:review 权限的其他角色账号。
func (r *Repository) FriendLinkReviewerEmails(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		select distinct lower(trim(u.email))
		from bp_user u
		inner join bp_user_role ur on ur.user_id=u.id
		inner join bp_role r on r.id=ur.role_id and r.status=1
		left join bp_role_menu rm on rm.role_id=r.id
		left join bp_menu m on m.id=rm.menu_id and m.status=1
		where u.status=1
		  and u.email is not null and trim(u.email)<>''
		  and (r.role_key='admin' or m.auth_code='friend-link:application:review')
		order by lower(trim(u.email))
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	emails := make([]string, 0, 4)
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		if email = strings.TrimSpace(email); email != "" {
			emails = append(emails, email)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return emails, nil
}
