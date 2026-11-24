package content

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/pkg/accountstatus"
	"bugpk-api/internal/pkg/dbx"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/pagination"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	mysqlDriver "github.com/go-sql-driver/mysql"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// maxPopupNotices bounds how many announcements the daily popup carousel can
// page through. The response carries the real total so the label never shows a
// count the payload cannot back.
const maxPopupNotices = 5

// noticeLive is the single definition of "visible to a reader": published,
// already effective, and not yet expired.
const noticeLive = "n.status=1 and n.publish_time<=now() and (n.expire_time is null or n.expire_time>now())"

func (r *Repository) ListNotices(ctx context.Context, params ListParams, publicOnly bool) (PageResult[NoticeItem], error) {
	page, pageSize := normalizePage(params.Page, params.PageSize)
	where, args := noticeWhere(params, publicOnly)
	var (
		total int64
		items []NoticeItem
	)
	countArgs := append(append([]any(nil), args...), pageSize*pagination.MaxPage)
	listArgs := append(append([]any(nil), args...), pageSize, pagination.Offset(page, pageSize))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, noticeCountQuery(where), countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, noticeListQuery(where), listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items, err = scanNotices(rows)
			return err
		},
	)
	if err != nil {
		return PageResult[NoticeItem]{}, err
	}
	return PageResult[NoticeItem]{Items: items, Total: total}, nil
}

func noticeCountQuery(where string) string {
	return `select count(*)
		from (
			select n.id
			from bp_notice n
			` + where + `
			limit ?
		) counted`
}

// noticeColumns is the shared read shape. List payloads keep the body because
// the reader list, the admin preview and the workspace popup all render it
// straight from the row they already fetched.
func noticeColumns() string {
	return strings.Join([]string{
		"n.id",
		"n.title",
		"n.summary",
		"n.content_html",
		"n.is_top",
		"n.is_popup",
		"n.is_home_ticker",
		"n.status",
		"date_format(n.publish_time,'%Y-%m-%d %H:%i:%s')",
		"coalesce(date_format(n.expire_time,'%Y-%m-%d %H:%i:%s'),'')",
		"coalesce(nullif(u.display_name,''),u.username,'')",
		"date_format(n.created_at,'%Y-%m-%d %H:%i:%s')",
		"date_format(n.updated_at,'%Y-%m-%d %H:%i:%s')",
	}, ",")
}

func noticeListQuery(where string) string {
	return `
		select ` + noticeColumns() + `
		from (
			select n.id
			from bp_notice n
			` + where + `
			order by n.is_top desc,n.publish_time desc,n.id desc
			limit ? offset ?
		) filtered
		inner join bp_notice n on n.id=filtered.id
		left join bp_user u on u.id=n.creator_id
		order by n.is_top desc,n.publish_time desc,n.id desc
	`
}

func noticePopupQuery(limit int) string {
	return `
		select ` + noticeColumns() + `
		from bp_notice n
		left join bp_user u on u.id=n.creator_id
		where ` + noticeLive + ` and n.is_popup=1
		order by n.is_top desc,n.publish_time desc,n.id desc
		limit ?
	`
}

func noticePopupCountQuery() string {
	return `
		select count(*)
		from bp_notice n
		where ` + noticeLive + ` and n.is_popup=1
	`
}

func (r *Repository) PopupNotices(ctx context.Context) (PopupNoticeResult, error) {
	var result PopupNoticeResult
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, noticePopupCountQuery()).Scan(&result.Total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, noticePopupQuery(maxPopupNotices), maxPopupNotices)
			if err != nil {
				return err
			}
			defer rows.Close()
			result.Items, err = scanNotices(rows)
			return err
		},
	)
	if err != nil {
		return PopupNoticeResult{}, err
	}
	if result.Items == nil {
		result.Items = []NoticeItem{}
	}
	return result, nil
}

func (r *Repository) NoticeByID(ctx context.Context, id int64, publicOnly bool) (NoticeItem, error) {
	where := "where n.id=?"
	args := []any{id}
	if publicOnly {
		where += " and " + noticeLive
	}
	rows, err := r.db.QueryContext(ctx, `
		select `+noticeColumns()+`
		from bp_notice n
		left join bp_user u on u.id=n.creator_id
		`+where, args...)
	if err != nil {
		return NoticeItem{}, err
	}
	defer rows.Close()
	items, err := scanNotices(rows)
	if err != nil {
		return NoticeItem{}, err
	}
	if len(items) == 0 {
		return NoticeItem{}, sql.ErrNoRows
	}
	return items[0], nil
}

func (r *Repository) CreateNotice(ctx context.Context, item NoticeItem, creatorID int64) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		insert into bp_notice(title,summary,content_html,is_top,is_popup,is_home_ticker,status,publish_time,expire_time,creator_id,updater_id)
		values(?,?,?,?,?,?,?,?,?,?,?)
	`, item.Title, item.Summary, item.ContentHTML, item.IsTop, item.IsPopup, item.IsHomeTicker,
		item.Status, time.Now(), noticeTimeArg(item.ExpireTime), creatorID, creatorID)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if item.IsHomeTicker == 1 {
		err = r.clearOtherHomeTicker(ctx, id)
	}
	return id, err
}

func (r *Repository) UpdateNotice(ctx context.Context, id int64, item NoticeItem, updaterID int64) error {
	result, err := r.db.ExecContext(ctx, `
		update bp_notice
		set title=?,summary=?,content_html=?,is_top=?,is_popup=?,is_home_ticker=?,status=?,expire_time=?,updater_id=?
		where id=?
	`, item.Title, item.Summary, item.ContentHTML, item.IsTop, item.IsPopup, item.IsHomeTicker,
		item.Status, noticeTimeArg(item.ExpireTime), updaterID, id)
	if err != nil {
		return err
	}
	if err := r.requireUpdateAffected(ctx, result, "select count(*) from bp_notice where id=?", id); err != nil {
		return err
	}
	if item.IsHomeTicker == 1 {
		return r.clearOtherHomeTicker(ctx, id)
	}
	return nil
}

// clearOtherHomeTicker keeps the homepage marquee a single-slot position: the
// newest claim wins and every other row falls back to a plain announcement.
func (r *Repository) clearOtherHomeTicker(ctx context.Context, keepID int64) error {
	_, err := r.db.ExecContext(ctx, "update bp_notice set is_home_ticker=0 where is_home_ticker=1 and id<>?", keepID)
	return err
}

// noticeTimeArg turns the empty string into SQL NULL so expire_time can be
// cleared from the editor without a second "clear expiry" endpoint.
func noticeTimeArg(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func (r *Repository) DeleteNotice(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, "delete from bp_notice where id=?", id)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

func (r *Repository) UpdateNoticeStatus(ctx context.Context, id int64, value int, updaterID int64) error {
	return r.updateNoticeFlag(ctx, `update bp_notice set status=?,updater_id=? where id=?`, id, value, updaterID)
}

func (r *Repository) UpdateNoticeTop(ctx context.Context, id int64, value int, updaterID int64) error {
	return r.updateNoticeFlag(ctx, `update bp_notice set is_top=?,updater_id=? where id=?`, id, value, updaterID)
}

func (r *Repository) UpdateNoticePopup(ctx context.Context, id int64, value int, updaterID int64) error {
	return r.updateNoticeFlag(ctx, `update bp_notice set is_popup=?,updater_id=? where id=?`, id, value, updaterID)
}

func (r *Repository) updateNoticeFlag(ctx context.Context, query string, id int64, value int, updaterID int64) error {
	result, err := r.db.ExecContext(ctx, query, value, updaterID, id)
	if err != nil {
		return err
	}
	return r.requireUpdateAffected(ctx, result, "select count(*) from bp_notice where id=?", id)
}

func scanNotices(rows *sql.Rows) ([]NoticeItem, error) {
	items := make([]NoticeItem, 0)
	for rows.Next() {
		var id int64
		var item NoticeItem
		if err := rows.Scan(
			&id, &item.Title, &item.Summary, &item.ContentHTML,
			&item.IsTop, &item.IsPopup, &item.IsHomeTicker, &item.Status,
			&item.PublishTime, &item.ExpireTime, &item.CreatorName, &item.CreateTime, &item.UpdateTime,
		); err != nil {
			return nil, err
		}
		item.ID = strconv.FormatInt(id, 10)
		items = append(items, item)
	}
	return items, rows.Err()
}

func noticeWhere(params ListParams, publicOnly bool) (string, []any) {
	where := []string{"where 1=1"}
	args := []any{}
	if publicOnly {
		where = append(where, noticeLive)
	}
	if keyword := strings.TrimSpace(params.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		where = append(where, "(n.title like ? or n.summary like ?)")
		args = append(args, like, like)
	}
	if !publicOnly && params.Status != nil {
		where = append(where, "n.status=?")
		args = append(args, *params.Status)
	}
	if params.IsTop != nil {
		where = append(where, "n.is_top=?")
		args = append(args, *params.IsTop)
	}
	if params.IsPopup != nil {
		where = append(where, "n.is_popup=?")
		args = append(args, *params.IsPopup)
	}
	if params.IsHomeTicker != nil {
		where = append(where, "n.is_home_ticker=?")
		args = append(args, *params.IsHomeTicker)
	}
	return strings.Join(where, " and "), args
}

// HomeTicker reads the rolling bar straight from the announcement flagged for
// it, so there is only one place to write, expire and translate a headline.
func (r *Repository) HomeTicker(ctx context.Context) (HomeTicker, error) {
	var ticker HomeTicker
	err := r.db.QueryRowContext(ctx, homeTickerPublicReadQuery()).Scan(
		&ticker.NoticeID, &ticker.Content, &ticker.UpdateTime,
	)
	if err == sql.ErrNoRows {
		return HomeTicker{Enabled: 0}, nil
	}
	if err != nil {
		return HomeTicker{}, err
	}
	ticker.Enabled = 1
	return ticker, nil
}

// HomeTickerSlot reads the announcement that owns the slot whether or not it is
// live. The editor needs the stored wording after the bar is switched off, so
// only the enabled flag is derived from the live filter here.
func (r *Repository) HomeTickerSlot(ctx context.Context) (HomeTicker, error) {
	var ticker HomeTicker
	var live int
	err := r.db.QueryRowContext(ctx, homeTickerSlotReadQuery()).Scan(
		&ticker.NoticeID, &ticker.Content, &ticker.UpdateTime, &live,
	)
	if err == sql.ErrNoRows {
		return HomeTicker{Enabled: 0}, nil
	}
	if err != nil {
		return HomeTicker{}, err
	}
	ticker.Enabled = live
	return ticker, nil
}

// homeTickerSlotOrder keeps the anchor lookup and both reads on the same row.
const homeTickerSlotOrder = " order by n.is_top desc,n.publish_time desc,n.id desc limit 1"

// homeTickerSlotLookupSQL finds the row the marquee is anchored to. The slot is
// single-ownership, so the ordering only breaks ties for the moment between a
// new claim and clearOtherHomeTicker dropping the previous one.
const homeTickerSlotLookupSQL = "select n.id from bp_notice n where n.is_home_ticker=1" + homeTickerSlotOrder

const homeTickerSlotFields = "select cast(n.id as char),n.title,date_format(n.updated_at,'%Y-%m-%d %H:%i:%s')"

func homeTickerSlotReadQuery() string {
	return homeTickerSlotFields + ",(" + noticeLive + ") from bp_notice n where n.is_home_ticker=1" + homeTickerSlotOrder
}

func homeTickerPublicReadQuery() string {
	return homeTickerSlotFields + " from bp_notice n where " + noticeLive + " and n.is_home_ticker=1" + homeTickerSlotOrder
}

// homeTickerUpdateQuery keeps the slot anchor and only moves its publication
// state, so switching the bar off never leaves the next save without a row.
func homeTickerUpdateQuery(enabled bool) string {
	if !enabled {
		return "update bp_notice set title=?,summary=?,content_html=?,is_home_ticker=1,status=0,updater_id=? where id=?"
	}
	return "update bp_notice set title=?,summary=?,content_html=?,is_home_ticker=1,status=1,expire_time=null,updater_id=? where id=?"
}

// WriteHomeTicker puts the marquee line onto the announcement that owns the
// slot, so the homepage headline and the notice centre stay one record instead
// of a second copy that can drift.
//
// enabled=true publishes that row and drops any stored expiry, matching the
// always-visible switch this replaces. enabled=false keeps the wording and the
// slot on that row but unpublishes it: the headline survives the toggle and
// re-enabling never appends a second marquee announcement to the notice centre.
func (r *Repository) WriteHomeTicker(ctx context.Context, item NoticeItem, updaterID int64, enabled bool) error {
	var id int64
	err := r.db.QueryRowContext(ctx, homeTickerSlotLookupSQL).Scan(&id)
	if err == sql.ErrNoRows {
		if !enabled {
			// A draft saved while the bar is off still lands as an unpublished
			// announcement, otherwise the wording would vanish on reload.
			item.Status = 0
		}
		_, err = r.CreateNotice(ctx, item, updaterID)
		return err
	}
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, homeTickerUpdateQuery(enabled),
		item.Title, item.Summary, item.ContentHTML, updaterID, id)
	if err != nil {
		return err
	}
	if enabled {
		if err := r.clearOtherHomeTicker(ctx, id); err != nil {
			return err
		}
	}
	return r.requireUpdateAffected(ctx, result, "select count(*) from bp_notice where id=?", id)
}

func (r *Repository) SiteConfig(ctx context.Context) (SiteConfig, error) {
	var cfg SiteConfig
	err := r.db.QueryRowContext(ctx, `
		select id,site_name,logo_url,slogan,description,contact_email,contact_phone,contact_qq,contact_wechat,
		       contact_address,icp,copyright,coalesce(analytics_code,''),seo_title,seo_keywords,og_image,site_url,noindex,
		       loading_animation_enabled,loading_animation_preset,loading_animation_url,
		       coalesce(global_loading_preset,'bar'),
		       coalesce(github_url,''),coalesce(author_name,''),coalesce(author_url,''),coalesce(license_type,''),coalesce(license_url,''),
		       date_format(updated_at,'%Y-%m-%d %H:%i:%s')
		from bp_site_config where id=1
	`).Scan(&cfg.ID, &cfg.SiteName, &cfg.LogoURL, &cfg.Slogan, &cfg.Description, &cfg.ContactEmail, &cfg.ContactPhone, &cfg.ContactQq, &cfg.ContactWechat, &cfg.ContactAddress, &cfg.ICP, &cfg.Copyright, &cfg.AnalyticsCode, &cfg.SEOTitle, &cfg.SEOKeywords, &cfg.OGImage, &cfg.SiteURL, &cfg.Noindex, &cfg.LoadingAnimationEnabled, &cfg.LoadingAnimationPreset, &cfg.LoadingAnimationURL, &cfg.GlobalLoadingPreset, &cfg.GitHubURL, &cfg.AuthorName, &cfg.AuthorURL, &cfg.LicenseType, &cfg.LicenseURL, &cfg.UpdateTime)
	if err == sql.ErrNoRows {
		return SiteConfig{ID: 1, SiteName: "BugPk-Api"}, nil
	}
	return cfg, err
}

func (r *Repository) SaveSiteConfig(ctx context.Context, cfg SiteConfig) error {
	_, err := r.db.ExecContext(ctx, `
		insert into bp_site_config(id,site_name,logo_url,slogan,description,contact_email,contact_phone,contact_qq,contact_wechat,contact_address,icp,copyright,analytics_code,seo_title,seo_keywords,og_image,site_url,noindex,loading_animation_enabled,loading_animation_preset,loading_animation_url,global_loading_preset,github_url,author_name,author_url,license_type,license_url)
		values(1,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		on duplicate key update site_name=values(site_name),logo_url=values(logo_url),slogan=values(slogan),description=values(description),
			contact_email=values(contact_email),contact_phone=values(contact_phone),contact_qq=values(contact_qq),contact_wechat=values(contact_wechat),
			contact_address=values(contact_address),icp=values(icp),copyright=values(copyright),analytics_code=values(analytics_code),seo_title=values(seo_title),
			seo_keywords=values(seo_keywords),og_image=values(og_image),site_url=values(site_url),noindex=values(noindex),
			loading_animation_enabled=values(loading_animation_enabled),loading_animation_preset=values(loading_animation_preset),loading_animation_url=values(loading_animation_url),
			global_loading_preset=values(global_loading_preset),
			github_url=values(github_url),author_name=values(author_name),author_url=values(author_url),license_type=values(license_type),license_url=values(license_url)
	`, cfg.SiteName, cfg.LogoURL, cfg.Slogan, cfg.Description, cfg.ContactEmail, cfg.ContactPhone, cfg.ContactQq, cfg.ContactWechat, cfg.ContactAddress, cfg.ICP, cfg.Copyright, cfg.AnalyticsCode, cfg.SEOTitle, cfg.SEOKeywords, cfg.OGImage, cfg.SiteURL, cfg.Noindex, cfg.LoadingAnimationEnabled, cfg.LoadingAnimationPreset, cfg.LoadingAnimationURL, cfg.GlobalLoadingPreset, cfg.GitHubURL, cfg.AuthorName, cfg.AuthorURL, cfg.LicenseType, cfg.LicenseURL)
	return err
}

func (r *Repository) ListFriendLinks(ctx context.Context, params ListParams, publicOnly bool) (PageResult[FriendLinkItem], error) {
	page, pageSize := normalizePage(params.Page, params.PageSize)
	where, args := friendLinkWhere(params, publicOnly)
	var (
		total int64
		items []FriendLinkItem
	)
	countArgs := append(append([]any(nil), args...), pageSize*pagination.MaxPage)
	listArgs := append(append([]any(nil), args...), pageSize, pagination.Offset(page, pageSize))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, friendLinkCountQuery(where), countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, friendLinkListQuery(where), listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items, err = scanFriendLinks(rows)
			return err
		},
	)
	if err != nil {
		return PageResult[FriendLinkItem]{}, err
	}
	return PageResult[FriendLinkItem]{Items: items, Total: total}, nil
}

func friendLinkCountQuery(where string) string {
	return `select count(*)
		from (
			select l.id
			from bp_friend_link l
			` + where + `
			limit ?
		) counted`
}

func friendLinkListQuery(where string) string {
	return `
		select l.id,l.site_name,l.site_url,l.normalized_site_url,l.logo_url,l.description,l.sort_no,l.status,l.applicant_name,l.applicant_email,l.language,
		       date_format(l.created_at,'%Y-%m-%d %H:%i:%s'),date_format(l.updated_at,'%Y-%m-%d %H:%i:%s'),l.user_id
		from (
			select l.id
			from bp_friend_link l
			` + where + `
			order by l.sort_no asc,l.id asc
			limit ? offset ?
		) filtered
		inner join bp_friend_link l on l.id=filtered.id
		order by l.sort_no asc,l.id asc
	`
}

// friendLinkItemColumns 与 scanFriendLinks 的列顺序一一对应（首列必须是 id）。
const friendLinkItemColumns = `site_name,site_url,normalized_site_url,logo_url,description,sort_no,status,
		       applicant_name,applicant_email,language,
		       date_format(created_at,'%Y-%m-%d %H:%i:%s'),date_format(updated_at,'%Y-%m-%d %H:%i:%s'),user_id`

// friendLinkApplicationColumns 与 scanApplications 的列顺序一一对应（首列必须是 id）。
const friendLinkApplicationColumns = `user_id,username,site_name,site_url,normalized_site_url,logo_url,description,
		       contact_name,contact_email,contact_qq,backlink_url,language,status,reject_reason,
		       review_reason_template,review_send_email,reviewer_name,
		       coalesce(date_format(review_time,'%Y-%m-%d %H:%i:%s'),''),
		       date_format(created_at,'%Y-%m-%d %H:%i:%s'),date_format(updated_at,'%Y-%m-%d %H:%i:%s'),
		       link_id,change_kind`

func (r *Repository) FriendLinkByID(ctx context.Context, id int64) (FriendLinkItem, error) {
	rows, err := r.db.QueryContext(ctx, `
		select id,`+friendLinkItemColumns+`
		from bp_friend_link where id=?
	`, id)
	if err != nil {
		return FriendLinkItem{}, err
	}
	defer rows.Close()
	items, err := scanFriendLinks(rows)
	if err != nil {
		return FriendLinkItem{}, err
	}
	if len(items) == 0 {
		return FriendLinkItem{}, sql.ErrNoRows
	}
	return items[0], nil
}

func (r *Repository) CreateFriendLink(ctx context.Context, item FriendLinkItem) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		insert into bp_friend_link(site_name,site_url,normalized_site_url,logo_url,description,sort_no,status,applicant_name,applicant_email,language)
		values(?,?,?,?,?,?,?,?,?,?)
	`, item.SiteName, item.SiteURL, item.NormalizedSiteURL, item.LogoURL, item.Description, item.SortNo, item.Status, item.ApplicantName, item.ApplicantEmail, normalizeFriendLinkLanguage(item.Language))
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (r *Repository) UpdateFriendLink(ctx context.Context, id int64, item FriendLinkItem) error {
	result, err := r.db.ExecContext(ctx, `
		update bp_friend_link set site_name=?,site_url=?,normalized_site_url=?,logo_url=?,description=?,sort_no=?,status=?,applicant_name=?,applicant_email=? where id=?
	`, item.SiteName, item.SiteURL, item.NormalizedSiteURL, item.LogoURL, item.Description, item.SortNo, item.Status, item.ApplicantName, item.ApplicantEmail, id)
	if err != nil {
		return err
	}
	return r.requireUpdateAffected(ctx, result, "select count(*) from bp_friend_link where id=?", id)
}

func (r *Repository) DeleteFriendLink(
	ctx context.Context,
	id int64,
	reviewer Principal,
	decision reviewDecision,
) (FriendLinkItem, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return FriendLinkItem{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var item FriendLinkItem
	err = tx.QueryRowContext(ctx, `
		select site_name,site_url,normalized_site_url,logo_url,description,sort_no,status,
		       applicant_name,applicant_email,language
		from bp_friend_link where id=? for update
	`, id).Scan(
		&item.SiteName,
		&item.SiteURL,
		&item.NormalizedSiteURL,
		&item.LogoURL,
		&item.Description,
		&item.SortNo,
		&item.Status,
		&item.ApplicantName,
		&item.ApplicantEmail,
		&item.Language,
	)
	if err != nil {
		return FriendLinkItem{}, err
	}
	item.ID = strconv.FormatInt(id, 10)
	result, err := tx.ExecContext(ctx, "delete from bp_friend_link where id=?", id)
	if err != nil {
		return FriendLinkItem{}, err
	}
	if err := requireAffected(result); err != nil {
		return FriendLinkItem{}, err
	}
	if err := insertFriendLinkAudit(ctx, tx, friendLinkAudit{
		Action:         "DELETE",
		ActorID:        reviewer.ID,
		ActorName:      reviewer.Username,
		EntityID:       id,
		EntityType:     "LINK",
		Language:       item.Language,
		Reason:         decision.Reason,
		ReasonTemplate: decision.ReasonTemplate,
		SendEmail:      decision.SendEmail,
		Snapshot:       item,
	}); err != nil {
		return FriendLinkItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return FriendLinkItem{}, err
	}
	return item, nil
}

func (r *Repository) UpdateFriendLinkStatus(ctx context.Context, id int64, status int) error {
	result, err := r.db.ExecContext(ctx, "update bp_friend_link set status=? where id=?", status, id)
	if err != nil {
		return err
	}
	return r.requireUpdateAffected(ctx, result, "select count(*) from bp_friend_link where id=?", id)
}

func scanFriendLinks(rows *sql.Rows) ([]FriendLinkItem, error) {
	items := make([]FriendLinkItem, 0)
	for rows.Next() {
		var id int64
		var ownerID int64
		var item FriendLinkItem
		if err := rows.Scan(&id, &item.SiteName, &item.SiteURL, &item.NormalizedSiteURL, &item.LogoURL, &item.Description, &item.SortNo, &item.Status, &item.ApplicantName, &item.ApplicantEmail, &item.Language, &item.CreateTime, &item.UpdateTime, &ownerID); err != nil {
			return nil, err
		}
		item.ID = strconv.FormatInt(id, 10)
		if ownerID > 0 {
			item.UserID = strconv.FormatInt(ownerID, 10)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func friendLinkWhere(params ListParams, publicOnly bool) (string, []any) {
	where := []string{"where 1=1"}
	args := []any{}
	if publicOnly {
		where = append(where, "l.status=1")
	} else if params.Status != nil {
		where = append(where, "l.status=?")
		args = append(args, *params.Status)
	}
	if keyword := strings.TrimSpace(params.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		where = append(where, "(l.site_name like ? or l.site_url like ? or l.description like ?)")
		args = append(args, like, like, like)
	}
	return strings.Join(where, " and "), args
}

func (r *Repository) FriendLinkConfig(ctx context.Context) (FriendLinkConfig, error) {
	var cfg FriendLinkConfig
	err := r.db.QueryRowContext(ctx, `
		select id,apply_enabled,apply_notice,date_format(updated_at,'%Y-%m-%d %H:%i:%s') from bp_friend_link_config where id=1
	`).Scan(&cfg.ID, &cfg.ApplyEnabled, &cfg.ApplyNotice, &cfg.UpdateTime)
	if err == sql.ErrNoRows {
		// 配置表允许为空（新部署/清洗后未插入默认行）：返回默认配置，避免接口 500
		return FriendLinkConfig{ID: 1, ApplyEnabled: 0, ApplyNotice: ""}, nil
	}
	return cfg, err
}

func (r *Repository) SaveFriendLinkConfig(ctx context.Context, cfg FriendLinkConfig) error {
	_, err := r.db.ExecContext(ctx, `
		insert into bp_friend_link_config(id,apply_enabled,apply_notice) values(1,?,?)
		on duplicate key update apply_enabled=values(apply_enabled),apply_notice=values(apply_notice)
	`, cfg.ApplyEnabled, cfg.ApplyNotice)
	return err
}

func (r *Repository) HasPendingApplication(ctx context.Context, userID int64, normalizedURL string) (bool, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, `
		select count(*) from bp_friend_link_application where status='PENDING' and (user_id=? or normalized_site_url=?)
	`, userID, normalizedURL).Scan(&count)
	return count > 0, err
}

func (r *Repository) CreateApplication(ctx context.Context, app FriendLinkApplication) (int64, error) {
	userID, _ := strconv.ParseInt(app.UserID, 10, 64)
	linkID, _ := strconv.ParseInt(app.LinkID, 10, 64)
	changeKind := normalizeFriendLinkChangeKind(app.ChangeKind)
	if changeKind == "" {
		return 0, apperrors.New(consts.StatusBadRequest, "changeKind is invalid")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		insert into bp_friend_link_application(
			user_id,username,site_name,site_url,normalized_site_url,logo_url,description,
			contact_name,contact_email,contact_qq,backlink_url,language,link_id,change_kind,status
		) values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'PENDING')
	`, userID, app.Username, app.SiteName, app.SiteURL, app.NormalizedSiteURL, app.LogoURL, app.Description, app.ContactName, app.ContactEmail, app.ContactQq, app.BacklinkURL, normalizeFriendLinkLanguage(app.Language), linkID, changeKind)
	if err != nil {
		var mysqlErr *mysqlDriver.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return 0, apperrors.New(consts.StatusConflict, "pending friend link application already exists")
		}
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	app.ID = strconv.FormatInt(id, 10)
	app.Status = "PENDING"
	app.ChangeKind = changeKind
	if err := insertFriendLinkAudit(ctx, tx, friendLinkAudit{
		Action:     "APPLY",
		ActorID:    userID,
		ActorName:  app.Username,
		EntityID:   id,
		EntityType: "APPLICATION",
		Language:   app.Language,
		Snapshot:   app,
	}); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (r *Repository) LatestApplicationStatus(ctx context.Context, userID int64) (ApplyStatus, error) {
	var status ApplyStatus
	err := r.db.QueryRowContext(ctx, `
		select status,date_format(created_at,'%Y-%m-%d %H:%i:%s') from bp_friend_link_application where user_id=? order by id desc limit 1
	`, userID).Scan(&status.Status, &status.CreateTime)
	if err == sql.ErrNoRows {
		return ApplyStatus{Submitted: false}, nil
	}
	status.Submitted = true
	return status, err
}

func (r *Repository) ListApplications(ctx context.Context, params ListParams) (PageResult[FriendLinkApplication], error) {
	page, pageSize := normalizePage(params.Page, params.PageSize)
	whereText, args := friendLinkApplicationWhere(params)
	var (
		total int64
		items []FriendLinkApplication
	)
	countArgs := append(append([]any(nil), args...), pageSize*pagination.MaxPage)
	listArgs := append(append([]any(nil), args...), pageSize, pagination.Offset(page, pageSize))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, friendLinkApplicationCountQuery(whereText), countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, friendLinkApplicationListQuery(params, whereText), listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items, err = scanApplications(rows)
			return err
		},
	)
	if err != nil {
		return PageResult[FriendLinkApplication]{}, err
	}
	return PageResult[FriendLinkApplication]{Items: items, Total: total}, nil
}

// 申请列表的状态过滤走仓储层编号：0=待审、1=通过、2=驳回、3=撤回。
// applicationFilterUnknown 用于「筛选词不认识」，条件匹配不到任何行；
// 若退化成不加条件，一个拼错的筛选词会把全部申请显示成某个具体桶。
const (
	applicationFilterPending = iota
	applicationFilterApproved
	applicationFilterRejected
	applicationFilterWithdrawn
	applicationFilterUnknown = -1
)

var applicationFilterStatuses = map[int]string{
	applicationFilterPending:   friendLinkStatusPending,
	applicationFilterApproved:  friendLinkStatusApproved,
	applicationFilterRejected:  friendLinkStatusRejected,
	applicationFilterWithdrawn: friendLinkStatusWithdrawn,
}

// applicationStatusFilter 把管理端传的状态名翻译成仓储层编号，空值表示不过滤。
func applicationStatusFilter(raw string) *int {
	value := applicationFilterUnknown
	switch strings.ToUpper(strings.TrimSpace(raw)) {
	case "":
		return nil
	case friendLinkStatusPending:
		value = applicationFilterPending
	case friendLinkStatusApproved:
		value = applicationFilterApproved
	case friendLinkStatusRejected:
		value = applicationFilterRejected
	case friendLinkStatusWithdrawn:
		value = applicationFilterWithdrawn
	}
	return &value
}

func friendLinkApplicationWhere(params ListParams) (string, []any) {
	where := []string{"where 1=1"}
	args := []any{}
	if keyword := strings.TrimSpace(params.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		where = append(where, "(a.site_name like ? or a.site_url like ? or a.username like ? or a.contact_email like ?)")
		args = append(args, like, like, like, like)
	}
	if params.Status != nil {
		status, known := applicationFilterStatuses[*params.Status]
		if !known {
			where = append(where, "a.status=''")
		} else {
			where = append(where, "a.status=?")
			args = append(args, status)
		}
	}
	return strings.Join(where, " and "), args
}

func friendLinkApplicationCountQuery(where string) string {
	return `select count(*)
		from (
			select a.id
			from bp_friend_link_application a
			` + where + `
			limit ?
		) counted`
}

func friendLinkApplicationListQuery(params ListParams, where string) string {
	innerSelect := "select a.id"
	innerOrder := "order by a.id desc"
	outerOrder := "order by a.id desc"
	if params.Status != nil && strings.TrimSpace(params.Keyword) == "" {
		innerSelect = "select a.id,a.created_at"
		innerOrder = "order by a.created_at desc,a.id desc"
		outerOrder = "order by filtered.created_at desc,filtered.id desc"
	}
	return `
		select a.id,a.user_id,a.username,a.site_name,a.site_url,a.normalized_site_url,a.logo_url,a.description,a.contact_name,a.contact_email,a.contact_qq,a.backlink_url,
		       a.language,a.status,a.reject_reason,a.review_reason_template,a.review_send_email,a.reviewer_name,coalesce(date_format(a.review_time,'%Y-%m-%d %H:%i:%s'),''),
		       date_format(a.created_at,'%Y-%m-%d %H:%i:%s'),date_format(a.updated_at,'%Y-%m-%d %H:%i:%s'),a.link_id,a.change_kind
		from (
			` + innerSelect + `
			from bp_friend_link_application a
			` + where + `
			` + innerOrder + `
			limit ? offset ?
		) filtered
		inner join bp_friend_link_application a on a.id=filtered.id
		` + outerOrder
}

func (r *Repository) ApplicationByID(ctx context.Context, id int64) (FriendLinkApplication, error) {
	rows, err := r.db.QueryContext(ctx, `
		select id,`+friendLinkApplicationColumns+`
		from bp_friend_link_application where id=?
	`, id)
	if err != nil {
		return FriendLinkApplication{}, err
	}
	defer rows.Close()
	items, err := scanApplications(rows)
	if err != nil {
		return FriendLinkApplication{}, err
	}
	if len(items) == 0 {
		return FriendLinkApplication{}, sql.ErrNoRows
	}
	return items[0], nil
}

func scanApplications(rows *sql.Rows) ([]FriendLinkApplication, error) {
	items := make([]FriendLinkApplication, 0)
	for rows.Next() {
		var id, userID int64
		var linkID int64
		var reviewSendEmail int
		var item FriendLinkApplication
		if err := rows.Scan(&id, &userID, &item.Username, &item.SiteName, &item.SiteURL, &item.NormalizedSiteURL, &item.LogoURL, &item.Description, &item.ContactName, &item.ContactEmail, &item.ContactQq, &item.BacklinkURL, &item.Language, &item.Status, &item.RejectReason, &item.ReviewReasonTemplate, &reviewSendEmail, &item.ReviewerName, &item.ReviewTime, &item.CreateTime, &item.UpdateTime, &linkID, &item.ChangeKind); err != nil {
			return nil, err
		}
		item.ID = strconv.FormatInt(id, 10)
		item.ReviewSendEmail = reviewSendEmail == 1
		item.UserID = strconv.FormatInt(userID, 10)
		if linkID > 0 {
			item.LinkID = strconv.FormatInt(linkID, 10)
		}
		if item.ChangeKind == "" {
			item.ChangeKind = friendLinkChangeNew
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) ApproveApplication(
	ctx context.Context,
	id int64,
	reviewer Principal,
	decision reviewDecision,
) (FriendLinkApplication, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return FriendLinkApplication{}, err
	}
	defer func() { _ = tx.Rollback() }()
	app, err := applicationByIDForUpdate(ctx, tx, id)
	if err != nil {
		return FriendLinkApplication{}, err
	}
	if app.Status != "PENDING" {
		return FriendLinkApplication{}, sql.ErrNoRows
	}
	if err := applyApprovedFriendLink(ctx, tx, app); err != nil {
		return FriendLinkApplication{}, err
	}
	result, err := tx.ExecContext(ctx, `
		update bp_friend_link_application
		set status='APPROVED',reviewer_id=?,reviewer_name=?,review_time=now(),reject_reason=?,
		    review_reason_template=?,review_send_email=?
		where id=?
	`, reviewer.ID, reviewer.Username, decision.Reason, decision.ReasonTemplate, boolInt(decision.SendEmail), id)
	if err != nil {
		return FriendLinkApplication{}, err
	}
	if err := requireAffected(result); err != nil {
		return FriendLinkApplication{}, err
	}
	app.Status = "APPROVED"
	app.RejectReason = decision.Reason
	app.ReviewReasonTemplate = decision.ReasonTemplate
	app.ReviewSendEmail = decision.SendEmail
	app.ReviewerName = reviewer.Username
	if err := insertFriendLinkAudit(ctx, tx, friendLinkAudit{
		Action:         "APPROVE",
		ActorID:        reviewer.ID,
		ActorName:      reviewer.Username,
		EntityID:       id,
		EntityType:     "APPLICATION",
		Language:       app.Language,
		Reason:         decision.Reason,
		ReasonTemplate: decision.ReasonTemplate,
		SendEmail:      decision.SendEmail,
		Snapshot:       app,
	}); err != nil {
		return FriendLinkApplication{}, err
	}
	if err := tx.Commit(); err != nil {
		return FriendLinkApplication{}, err
	}
	return app, nil
}

func (r *Repository) RejectApplication(
	ctx context.Context,
	id int64,
	reviewer Principal,
	decision reviewDecision,
) (FriendLinkApplication, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return FriendLinkApplication{}, err
	}
	defer func() { _ = tx.Rollback() }()
	app, err := applicationByIDForUpdate(ctx, tx, id)
	if err != nil {
		return FriendLinkApplication{}, err
	}
	if app.Status != "PENDING" {
		return FriendLinkApplication{}, sql.ErrNoRows
	}
	result, err := tx.ExecContext(ctx, `
		update bp_friend_link_application
		set status='REJECTED',reject_reason=?,review_reason_template=?,review_send_email=?,
		    reviewer_id=?,reviewer_name=?,review_time=now()
		where id=? and status='PENDING'
	`, decision.Reason, decision.ReasonTemplate, boolInt(decision.SendEmail), reviewer.ID, reviewer.Username, id)
	if err != nil {
		return FriendLinkApplication{}, err
	}
	if err := requireAffected(result); err != nil {
		return FriendLinkApplication{}, err
	}
	app.Status = "REJECTED"
	app.RejectReason = decision.Reason
	app.ReviewReasonTemplate = decision.ReasonTemplate
	app.ReviewSendEmail = decision.SendEmail
	app.ReviewerName = reviewer.Username
	if err := insertFriendLinkAudit(ctx, tx, friendLinkAudit{
		Action:         "REJECT",
		ActorID:        reviewer.ID,
		ActorName:      reviewer.Username,
		EntityID:       id,
		EntityType:     "APPLICATION",
		Language:       app.Language,
		Reason:         decision.Reason,
		ReasonTemplate: decision.ReasonTemplate,
		SendEmail:      decision.SendEmail,
		Snapshot:       app,
	}); err != nil {
		return FriendLinkApplication{}, err
	}
	if err := tx.Commit(); err != nil {
		return FriendLinkApplication{}, err
	}
	return app, nil
}

type friendLinkAudit struct {
	Action         string
	ActorID        int64
	ActorName      string
	EntityID       int64
	EntityType     string
	Language       string
	Reason         string
	ReasonTemplate string
	SendEmail      bool
	Snapshot       any
}

type friendLinkAuditExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertFriendLinkAudit(
	ctx context.Context,
	execer friendLinkAuditExecer,
	audit friendLinkAudit,
) error {
	snapshot, err := json.Marshal(audit.Snapshot)
	if err != nil {
		return err
	}
	_, err = execer.ExecContext(ctx, `
		insert into bp_friend_link_audit(
			entity_type,entity_id,action,actor_id,actor_name,reason,reason_template,
			send_email,language,snapshot
		) values(?,?,?,?,?,?,?,?,?,?)
	`,
		strings.ToUpper(strings.TrimSpace(audit.EntityType)),
		audit.EntityID,
		strings.ToUpper(strings.TrimSpace(audit.Action)),
		nullableAuditActorID(audit.ActorID),
		strings.TrimSpace(audit.ActorName),
		strings.TrimSpace(audit.Reason),
		strings.TrimSpace(audit.ReasonTemplate),
		boolInt(audit.SendEmail),
		normalizeFriendLinkLanguage(audit.Language),
		snapshot,
	)
	return err
}

func nullableAuditActorID(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func applicationByIDForUpdate(
	ctx context.Context,
	tx *sql.Tx,
	id int64,
) (FriendLinkApplication, error) {
	var app FriendLinkApplication
	var appID, userID int64
	var linkID int64
	err := tx.QueryRowContext(ctx, `
		select id,user_id,username,site_name,site_url,normalized_site_url,logo_url,description,
		       contact_name,contact_email,contact_qq,backlink_url,language,status,link_id,change_kind
		from bp_friend_link_application where id=? for update
	`, id).Scan(
		&appID,
		&userID,
		&app.Username,
		&app.SiteName,
		&app.SiteURL,
		&app.NormalizedSiteURL,
		&app.LogoURL,
		&app.Description,
		&app.ContactName,
		&app.ContactEmail,
		&app.ContactQq,
		&app.BacklinkURL,
		&app.Language,
		&app.Status,
		&linkID,
		&app.ChangeKind,
	)
	if err != nil {
		return FriendLinkApplication{}, err
	}
	app.ID = strconv.FormatInt(appID, 10)
	app.UserID = strconv.FormatInt(userID, 10)
	if linkID > 0 {
		app.LinkID = strconv.FormatInt(linkID, 10)
	}
	app.ChangeKind = normalizeFriendLinkChangeKind(app.ChangeKind)
	if app.ChangeKind == "" {
		app.ChangeKind = friendLinkChangeNew
	}
	return app, nil
}

func homeOpenTrafficCondition(alias string) string {
	return accountstatus.ActiveTrafficSQL(alias)
}

// HomeOverview builds the analytics projection consumed only by the snapshot
// refresher. Public HTTP requests must use LoadHomeAnalyticsSnapshot instead.
func (r *Repository) HomeOverview(ctx context.Context) (PlatformStats, []HotAPIItem, error) {
	var stats PlatformStats
	stats.AvailabilitySource = "CALL_SUCCESS"
	var success24h, prev24h int64
	var hot []HotAPIItem
	// The four reads are independent; run them concurrently so a background
	// refresh tracks the slowest query instead of their sum.
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, "select count(*),coalesce(sum(status=1),0) from bp_interface_api").Scan(&stats.APITotal, &stats.EnabledAPITotal)
		},
		func() error {
			return r.db.QueryRowContext(ctx, "select count(*) from bp_user u where "+accountstatus.EffectiveStatusSQL("u")+" = 1").Scan(&stats.UserTotal)
		},
		func() error {
			return r.db.QueryRowContext(ctx, homeTrafficStatsQuery()).Scan(
				&stats.Calls24h,
				&success24h,
				&stats.ActiveRegions24h,
				&prev24h,
				&stats.PeakQPS24h,
			)
		},
		func() error {
			var err error
			hot, err = r.hotAPIs(ctx, 8)
			return err
		},
	)
	if err != nil {
		return stats, nil, err
	}
	stats.AvailabilityPercent = percent(success24h, stats.Calls24h)
	stats.Calls24hDeltaPercent = deltaPercent(stats.Calls24h, prev24h)
	return stats, hot, nil
}

func homeTrafficStatsQuery() string {
	return `
		with active_minute as (
			select m.bucket_start,
			       sum(m.call_count) call_count,
			       sum(m.success_count) success_count
			from bp_call_user_metric_minute m force index (primary)
			inner join bp_user u on u.id=m.user_id
			where m.bucket_start>=date_sub(now(), interval 48 hour)
			  and ` + accountstatus.EffectiveStatusSQL("u") + ` = 1
			group by m.bucket_start
		)
		select cast(coalesce(sum(case when m.bucket_start>=date_sub(now(), interval 24 hour) then m.call_count else 0 end),0) as unsigned),
		       cast(coalesce(sum(case when m.bucket_start>=date_sub(now(), interval 24 hour) then m.success_count else 0 end),0) as unsigned),
		       cast(coalesce((
		         select count(distinct case
		           when nullif(r.province,'') is not null then concat('province:',r.province)
		           when nullif(r.city,'') is not null then concat('city:',r.city)
		           when nullif(r.country,'') is not null then concat('country:',r.country)
		         end)
		         from bp_call_region_hour r force index (idx_bp_call_region_hour_time)
		         inner join bp_user region_user on region_user.id=r.user_id
		         where r.bucket_start>=date_sub(now(), interval 24 hour)
		           and ` + accountstatus.EffectiveStatusSQL("region_user") + ` = 1
		       ),0) as unsigned),
		       cast(coalesce(sum(case when m.bucket_start<date_sub(now(), interval 24 hour) then m.call_count else 0 end),0) as unsigned),
	       cast(coalesce(ceil(max(case when m.bucket_start>=date_sub(now(), interval 24 hour) then m.call_count else 0 end)/60),0) as unsigned)
	from active_minute m
`
}

func (r *Repository) hotAPIs(ctx context.Context, limit int) ([]HotAPIItem, error) {
	rows, err := r.db.QueryContext(ctx, homeHotAPIsQuery(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]HotAPIItem, 0)
	for rows.Next() {
		var id int64
		var item HotAPIItem
		if err := rows.Scan(&id, &item.Name, &item.AvatarURL, &item.Description, &item.Price, &item.PointPrice, &item.IsTop, &item.IsFeatured, &item.RequestMethod, &item.CallCount); err != nil {
			return nil, err
		}
		item.ID = strconv.FormatInt(id, 10)
		items = append(items, item)
	}
	return items, rows.Err()
}

func homeHotAPIsQuery() string {
	return `
		select a.id,a.name,coalesce(a.avatar_url,''),coalesce(a.remark,''),a.price,a.point_price,a.is_top,a.is_featured,a.request_method,
		       cast(coalesce(traffic.call_count,0) as unsigned)
		from bp_interface_api a
		left join (
			select d.interface_id,sum(d.call_count) call_count
			from bp_call_api_metric_day d force index (primary)
			inner join bp_user u on u.id=d.user_id
			where d.bucket_date>=date_sub(current_date(), interval 29 day)
			  and ` + accountstatus.EffectiveStatusSQL("u") + ` = 1
			group by d.interface_id
		) traffic on traffic.interface_id=a.id
		where a.status=1
		order by coalesce(traffic.call_count,0) desc,a.is_top desc,a.is_featured desc,a.id desc
		limit ?
	`
}

func normalizePage(page, size int) (int, int) {
	return pagination.Normalize(page, size, 100)
}

func requireAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// requireUpdateAffected treats "matched but unchanged" updates as success.
// MySQL reports RowsAffected==0 when an UPDATE matches a row whose stored
// values are identical to the new ones, which must not surface as a 404.
func (r *Repository) requireUpdateAffected(ctx context.Context, result sql.Result, existsQuery string, args ...any) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	var count int
	if err := r.db.QueryRowContext(ctx, existsQuery, args...).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func percent(value, total int64) string {
	if total <= 0 {
		return "0"
	}
	return strconv.FormatFloat(float64(value)*100/float64(total), 'f', 2, 64)
}

func deltaPercent(current, previous int64) string {
	if previous <= 0 {
		if current > 0 {
			return "100"
		}
		return "0"
	}
	return strconv.FormatFloat(float64(current-previous)*100/float64(previous), 'f', 2, 64)
}
