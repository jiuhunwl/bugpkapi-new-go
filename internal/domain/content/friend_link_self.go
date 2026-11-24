package content

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// 自助链路只补充「用户能看到自己什么、能不能撤回」这两件事，
// 请求落库与审核通过后的写入全部复用 friend_link_apply.go 的既有实现。
const (
	friendLinkStatusPending   = "PENDING"
	friendLinkStatusWithdrawn = "WITHDRAWN"
	friendLinkStatusApproved  = "APPROVED"
	friendLinkStatusRejected  = "REJECTED"

	// myFriendLinkRequestLimit 给自助面板兜底：申请历史按倒序取最近若干条，
	// 用户不需要翻页也能看清当前待审的是哪一条。
	myFriendLinkRequestLimit = 50
)

// ListFriendLinksByUser 只返回该账号名下的友链。管理员手工添加的行（user_id=0）
// 不属于任何自助面板，只能在后台维护。
func (r *Repository) ListFriendLinksByUser(ctx context.Context, userID int64) ([]FriendLinkItem, error) {
	rows, err := r.db.QueryContext(ctx, `
		select id,`+friendLinkItemColumns+`
		from bp_friend_link
		where user_id=? and user_id>0
		order by id desc
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFriendLinks(rows)
}

// ListApplicationsByUser 返回该账号发起过的全部变更请求，供自助面板展示审核进度。
func (r *Repository) ListApplicationsByUser(ctx context.Context, userID int64, limit int) ([]FriendLinkApplication, error) {
	if limit <= 0 || limit > myFriendLinkRequestLimit {
		limit = myFriendLinkRequestLimit
	}
	rows, err := r.db.QueryContext(ctx, `
		select id,`+friendLinkApplicationColumns+`
		from bp_friend_link_application
		where user_id=?
		order by id desc
		limit ?
	`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanApplications(rows)
}

// WithdrawApplication 让申请人撤回自己仍在等待审核的请求。
// 撤回后唯一索引上的待审槽位立即释放，用户可以马上提交新请求。
func (r *Repository) WithdrawApplication(ctx context.Context, id int64, applicant Principal) (FriendLinkApplication, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return FriendLinkApplication{}, err
	}
	defer func() { _ = tx.Rollback() }()
	app, err := applicationByIDForUpdate(ctx, tx, id)
	if err != nil {
		return FriendLinkApplication{}, err
	}
	ownerID, _ := strconv.ParseInt(app.UserID, 10, 64)
	// 他人的请求与不存在的请求返回同一个错误，避免枚举出「哪些请求号存在」。
	if ownerID != applicant.ID || app.Status != friendLinkStatusPending {
		return FriendLinkApplication{}, sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `
		update bp_friend_link_application
		set status=?
		where id=? and status=?
	`, friendLinkStatusWithdrawn, id, friendLinkStatusPending); err != nil {
		return FriendLinkApplication{}, err
	}
	app.Status = friendLinkStatusWithdrawn
	if err := insertFriendLinkAudit(ctx, tx, friendLinkAudit{
		Action:     "WITHDRAW",
		ActorID:    applicant.ID,
		ActorName:  applicant.Username,
		EntityID:   id,
		EntityType: "APPLICATION",
		Language:   app.Language,
		Snapshot:   app,
	}); err != nil {
		return FriendLinkApplication{}, err
	}
	if err := tx.Commit(); err != nil {
		return FriendLinkApplication{}, err
	}
	return app, nil
}

// MyFriendLinks 是普通用户的友链中心数据。它只按登录账号收窄范围，
// 不调用 authz.Require —— 自助管理自己的友链不需要任何管理权限码。
func (s *Service) MyFriendLinks(ctx context.Context, principal Principal) (MyFriendLinks, error) {
	cfg, err := s.repo.FriendLinkConfig(ctx)
	if err != nil {
		return MyFriendLinks{}, err
	}
	links, err := s.repo.ListFriendLinksByUser(ctx, principal.ID)
	if err != nil {
		return MyFriendLinks{}, err
	}
	requests, err := s.repo.ListApplicationsByUser(ctx, principal.ID, myFriendLinkRequestLimit)
	if err != nil {
		return MyFriendLinks{}, err
	}
	if links == nil {
		links = []FriendLinkItem{}
	}
	if requests == nil {
		requests = []FriendLinkApplication{}
	}
	return MyFriendLinks{ApplyEnabled: cfg.ApplyEnabled == 1, Links: links, Requests: requests}, nil
}

// SubmitLinkChange 把自己已上线友链的修改内容登记为待审请求，线上行保持原样直到管理员通过。
func (s *Service) SubmitLinkChange(
	ctx context.Context,
	principal Principal,
	linkID int64,
	req SubmitFriendLinkChangeRequest,
) error {
	link, err := s.ownedFriendLink(ctx, principal, linkID)
	if err != nil {
		return err
	}
	fields, err := validateFriendLinkSiteFields(req.SiteName, req.SiteURL, req.LogoURL, req.Description, req.BacklinkURL)
	if err != nil {
		return err
	}
	app := friendLinkChangeApplication(link, fields, principal)
	app.ChangeKind = friendLinkChangeEdit
	return s.createSelfApplication(ctx, principal, app)
}

// SubmitLinkRemoval 把「请撤下我的站」登记为待审请求，通过后才真正删除。
func (s *Service) SubmitLinkRemoval(ctx context.Context, principal Principal, linkID int64) error {
	link, err := s.ownedFriendLink(ctx, principal, linkID)
	if err != nil {
		return err
	}
	fields := friendLinkSiteFields{
		Description:       link.Description,
		LogoURL:           link.LogoURL,
		NormalizedSiteURL: link.NormalizedSiteURL,
		SiteName:          link.SiteName,
		SiteURL:           link.SiteURL,
	}
	app := friendLinkChangeApplication(link, fields, principal)
	app.ChangeKind = friendLinkChangeRemove
	return s.createSelfApplication(ctx, principal, app)
}

// WithdrawLinkRequest 撤回自己仍在等待审核的申请或变更请求。
func (s *Service) WithdrawLinkRequest(ctx context.Context, principal Principal, requestID int64) error {
	_, err := s.repo.WithdrawApplication(ctx, requestID, principal)
	return mapNotFound(err, "pending friend link request not found")
}

func (s *Service) ownedFriendLink(ctx context.Context, principal Principal, linkID int64) (FriendLinkItem, error) {
	link, err := s.repo.FriendLinkByID(ctx, linkID)
	if err != nil {
		return FriendLinkItem{}, mapNotFound(err, "friend link not found")
	}
	// user_id=0 是管理员添加的友链，任何人都不能在自助面板里改它。
	if link.UserID != strconv.FormatInt(principal.ID, 10) {
		return FriendLinkItem{}, apperrors.New(consts.StatusForbidden, "you can only change your own friend link")
	}
	return link, nil
}

func friendLinkChangeApplication(link FriendLinkItem, fields friendLinkSiteFields, principal Principal) FriendLinkApplication {
	return FriendLinkApplication{
		BacklinkURL:       fields.BacklinkURL,
		ContactEmail:      link.ApplicantEmail,
		ContactName:       link.ApplicantName,
		Description:       fields.Description,
		Language:          link.Language,
		LinkID:            link.ID,
		LogoURL:           fields.LogoURL,
		NormalizedSiteURL: fields.NormalizedSiteURL,
		SiteName:          fields.SiteName,
		SiteURL:           fields.SiteURL,
		UserID:            strconv.FormatInt(principal.ID, 10),
		Username:          principal.Username,
	}
}

func (s *Service) createSelfApplication(ctx context.Context, principal Principal, app FriendLinkApplication) error {
	if strings.TrimSpace(app.ContactEmail) == "" {
		return apperrors.New(consts.StatusConflict, "this friend link has no contact email, ask an administrator to update it")
	}
	exists, err := s.repo.HasPendingApplication(ctx, principal.ID, app.NormalizedSiteURL)
	if err != nil {
		return err
	}
	if exists {
		// 同一账号同时只能有一条待审请求（唯一索引兜底）：先撤回，再提交新的。
		return apperrors.New(consts.StatusConflict, "pending friend link application already exists")
	}
	id, err := s.repo.CreateApplication(ctx, app)
	if err != nil {
		return err
	}
	app.ID = strconv.FormatInt(id, 10)
	// 回执与待审提醒属于尽力而为的通知，失败不阻断申请成功返回。
	s.notifyFriendApplicationSubmitted(ctx, app)
	return nil
}
