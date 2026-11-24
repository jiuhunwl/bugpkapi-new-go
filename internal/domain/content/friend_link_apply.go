package content

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// 友链变更请求的三种形态。NEW 沿用改造前的「申请即新增」链路，
// EDIT/REMOVE 由用户对已上线的自有友链发起，审核通过后才落到 bp_friend_link，
// 失败时整笔事务回滚，申请单保持 PENDING，管理员可以重新审核。
const (
	friendLinkChangeNew    = "NEW"
	friendLinkChangeEdit   = "EDIT"
	friendLinkChangeRemove = "REMOVE"
)

func normalizeFriendLinkChangeKind(value string) string {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", friendLinkChangeNew:
		return friendLinkChangeNew
	case friendLinkChangeEdit:
		return friendLinkChangeEdit
	case friendLinkChangeRemove:
		return friendLinkChangeRemove
	default:
		return ""
	}
}

// applyApprovedFriendLink 在审核事务内把申请单落到线上友链表。
// 调用方必须已经对申请单执行 SELECT ... FOR UPDATE，并把 link_id 解析进 app.LinkID。
func applyApprovedFriendLink(ctx context.Context, tx *sql.Tx, app FriendLinkApplication) error {
	userID, err := strconv.ParseInt(strings.TrimSpace(app.UserID), 10, 64)
	if err != nil {
		userID = 0
	}
	switch normalizeFriendLinkChangeKind(app.ChangeKind) {
	case friendLinkChangeNew:
		return applyApprovedFriendLinkNew(ctx, tx, app, userID)
	case friendLinkChangeEdit:
		return applyApprovedFriendLinkEdit(ctx, tx, app, userID)
	case friendLinkChangeRemove:
		return applyApprovedFriendLinkRemove(ctx, tx, app, userID)
	default:
		return apperrors.New(consts.StatusBadRequest, "changeKind is invalid")
	}
}

// applyApprovedFriendLinkNew 上线一条新友链。同一 URL 已存在时沿用历史语义覆盖内容，
// 但 **不** 改写 user_id：管理员手工添加的行（user_id=0）与他人名下的行不会
// 因为谁先申请通过就归谁，否则用户能用一次申请把后台维护的友链抢到自己名下。
func applyApprovedFriendLinkNew(ctx context.Context, tx *sql.Tx, app FriendLinkApplication, userID int64) error {
	_, err := tx.ExecContext(ctx, `
		insert into bp_friend_link(
			site_name,site_url,normalized_site_url,logo_url,description,sort_no,status,
			applicant_name,applicant_email,language,source_application_id,user_id
		) values(?,?,?,?,?,0,1,?,?,?,?,?)
		on duplicate key update site_name=values(site_name),site_url=values(site_url),
			normalized_site_url=values(normalized_site_url),logo_url=values(logo_url),
			description=values(description),status=1,applicant_name=values(applicant_name),
			applicant_email=values(applicant_email),language=values(language),
			source_application_id=values(source_application_id)
	`, app.SiteName, app.SiteURL, app.NormalizedSiteURL, app.LogoURL, app.Description,
		app.ContactName, app.ContactEmail, normalizeFriendLinkLanguage(app.Language), app.ID, userID)
	return err
}

// applyApprovedFriendLinkEdit 只改写申请人自己那条友链的网站信息。
// 联系方式与上下架状态留给管理员：审核通过一次修改，不该顺手把用户下架的友链重新上架。
func applyApprovedFriendLinkEdit(ctx context.Context, tx *sql.Tx, app FriendLinkApplication, userID int64) error {
	linkID, err := lockOwnedFriendLink(ctx, tx, app, userID)
	if err != nil {
		return err
	}
	// 换域名时先确认新地址没被别的友链占用：唯一索引只给一个错误号，
	// 提前检查才能告诉管理员是被谁挡住了。
	var occupied int64
	if err := tx.QueryRowContext(ctx, `
		select count(*) from bp_friend_link where normalized_site_url=? and id<>?
	`, app.NormalizedSiteURL, linkID).Scan(&occupied); err != nil {
		return err
	}
	if occupied > 0 {
		return apperrors.New(consts.StatusConflict, "site url is already used by another friend link")
	}
	// 不使用 requireAffected：MySQL 对「命中但值完全相同」的更新返回 0 行，
	// 那会被误报成申请单不存在（见 requireUpdateAffected 的同款坑）。
	if _, err := tx.ExecContext(ctx, `
		update bp_friend_link
		set site_name=?,site_url=?,normalized_site_url=?,logo_url=?,description=?
		where id=? and user_id=?
	`, app.SiteName, app.SiteURL, app.NormalizedSiteURL, app.LogoURL, app.Description, linkID, userID); err != nil {
		return err
	}
	return insertFriendLinkAudit(ctx, tx, friendLinkAudit{
		Action:     "UPDATE",
		ActorID:    userID,
		ActorName:  app.Username,
		EntityID:   linkID,
		EntityType: "LINK",
		Language:   app.Language,
		Snapshot:   app,
	})
}

// applyApprovedFriendLinkRemove 下架申请人自己的友链。这里是 status=0 而不是 DELETE：
// 后台仍能看到这条并随时重新上架，历史排序与来源申请也不会断链。
func applyApprovedFriendLinkRemove(ctx context.Context, tx *sql.Tx, app FriendLinkApplication, userID int64) error {
	linkID, err := lockOwnedFriendLink(ctx, tx, app, userID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		update bp_friend_link set status=0 where id=? and user_id=?
	`, linkID, userID); err != nil {
		return err
	}
	return insertFriendLinkAudit(ctx, tx, friendLinkAudit{
		Action:     "REMOVE",
		ActorID:    userID,
		ActorName:  app.Username,
		EntityID:   linkID,
		EntityType: "LINK",
		Language:   app.Language,
		Snapshot:   app,
	})
}

// lockOwnedFriendLink 锁定 EDIT/REMOVE 的目标行，并要求它仍然存在且仍属于申请人。
// 任一条不成立都返回冲突：管理员会看到明确原因，事务回滚后申请单仍是 PENDING。
func lockOwnedFriendLink(ctx context.Context, tx *sql.Tx, app FriendLinkApplication, userID int64) (int64, error) {
	linkID, err := parseFriendLinkTargetID(app)
	if err != nil {
		return 0, err
	}
	if userID <= 0 {
		return 0, apperrors.New(consts.StatusConflict, "friend link application has no owning user")
	}
	var storedOwner int64
	err = tx.QueryRowContext(ctx, "select user_id from bp_friend_link where id=? for update", linkID).Scan(&storedOwner)
	if err == sql.ErrNoRows {
		return 0, apperrors.New(consts.StatusConflict, fmt.Sprintf("friend link %d was removed", linkID))
	}
	if err != nil {
		return 0, err
	}
	// user_id=0 是管理员添加的友链，任何自助请求都不能改写它。
	if storedOwner != userID {
		return 0, apperrors.New(consts.StatusConflict, "friend link no longer belongs to the applicant")
	}
	return linkID, nil
}

func parseFriendLinkTargetID(app FriendLinkApplication) (int64, error) {
	linkID, err := strconv.ParseInt(strings.TrimSpace(app.LinkID), 10, 64)
	if err != nil || linkID <= 0 {
		return 0, apperrors.New(consts.StatusConflict, fmt.Sprintf("friend link application %s has no valid target link id", app.ID))
	}
	return linkID, nil
}
