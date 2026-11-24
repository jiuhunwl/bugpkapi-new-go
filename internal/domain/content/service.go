package content

import (
	"bytes"
	"context"
	"database/sql"
	htmlstd "html"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"bugpk-api/internal/domain/notification"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/emailaddr"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/i18n"
	"bugpk-api/internal/pkg/listquery"
	"bugpk-api/internal/pkg/upload"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/microcosm-cc/bluemonday"
	"github.com/redis/go-redis/v9"
	htmlnode "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	maxAnalyticsCodeBytes = 256 << 10
	maxNoticeHTMLBytes    = 1 << 20
	// A notice title doubles as the homepage marquee line, so it keeps the
	// 300-character allowance the rolling-notice config used to have.
	maxNoticeTitleRunes = 300
	maxHomeTickerRunes  = 300
	noticeTimeLayout    = "2006-01-02 15:04:05"
)

// 启动动画（首屏 splash）预设：6 套内置，另兼容历史值 pulse/bars。
var loadingAnimationPresets = map[string]struct{}{
	"dots": {}, "forge": {}, "gateway": {}, "orbit": {}, "rail": {}, "scan": {},
	"bars": {}, "pulse": {},
}

// 全局加载动画（路由切换 / 请求挂起）预设。
var globalLoadingPresets = map[string]struct{}{
	"bar": {}, "dots": {}, "logo": {}, "packet": {}, "ring": {}, "skeleton": {},
}

type Service struct {
	authz            *rbac.Service
	captcha          captchaConsumer
	localizer        i18n.Localizer
	notifier         notificationEnqueuer
	publicCache      *redis.Client
	publicCacheTTL   time.Duration
	publicCacheMu    sync.Mutex
	publicCacheNow   func() time.Time
	homeSnapshotWake chan struct{}
	publicHomeCall   *homeOverviewCall
	publicHomeData   homeOverviewCacheEntry
	publicHomeEpoch  uint64
	repo             *Repository
	systemPrefix     string
	// analyticsEnabled mirrors analytics.enabled. It defaults to true so an
	// unwired caller is not silently degraded; the rollup and snapshot probes
	// independently catch a pipeline that is configured on but not running.
	analyticsEnabled bool
}

// SetAnalyticsEnabled records whether the background rollup worker is enabled.
// A disabled worker never fills the rollup tables, so the homepage must report
// its traffic metrics as unavailable rather than as real zeros.
func (s *Service) SetAnalyticsEnabled(enabled bool) {
	s.analyticsEnabled = enabled
}

// SetLocalizer 接入后端数据翻译；localizer 为 nil 时保持默认语言行为不变。
func (s *Service) SetLocalizer(localizer i18n.Localizer) {
	s.localizer = localizer
}

func NewService(repo *Repository, authz *rbac.Service, systemPrefix string) *Service {
	return &Service{
		repo:             repo,
		authz:            authz,
		systemPrefix:     systemPrefix,
		homeSnapshotWake: make(chan struct{}, 1),
		analyticsEnabled: true,
	}
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	return s.authz.Authenticate(ctx, token)
}

func (s *Service) PublicNoticeList(ctx context.Context, params ListParams) (PageResult[NoticeItem], error) {
	if err := validateListParams(&params); err != nil {
		return PageResult[NoticeItem]{}, err
	}
	result, err := s.repo.ListNotices(ctx, params, true)
	if err != nil {
		return PageResult[NoticeItem]{}, err
	}
	result = sanitizeNoticePage(result)
	result.Items = publicNoticeItems(result.Items)
	s.localizeNoticeItems(ctx, result.Items)
	return result, nil
}

func (s *Service) PublicNoticeDetail(ctx context.Context, id int64) (NoticeItem, error) {
	item, err := s.repo.NoticeByID(ctx, id, true)
	if err != nil {
		return NoticeItem{}, mapNotFound(err, "notice not found")
	}
	item = publicNoticeItem(sanitizeNoticeItem(item))
	s.localizeNoticeItem(ctx, &item)
	return item, nil
}

func (s *Service) PopupNotices(ctx context.Context) (PopupNoticeResult, error) {
	result, err := s.repo.PopupNotices(ctx)
	if err != nil {
		return PopupNoticeResult{}, err
	}
	result.Items = publicNoticeItems(sanitizeNoticeItems(result.Items))
	s.localizeNoticeItems(ctx, result.Items)
	return result, nil
}

// publicNoticeItems drops the publisher's account name from reader responses.
// It is an internal identifier, no public surface renders it, and the admin
// list keeps it because editors need to see who wrote a headline.
func publicNoticeItems(items []NoticeItem) []NoticeItem {
	for index := range items {
		items[index] = publicNoticeItem(items[index])
	}
	return items
}

func publicNoticeItem(item NoticeItem) NoticeItem {
	item.CreatorName = ""
	return item
}

func (s *Service) AdminNoticeList(ctx context.Context, principal Principal, params ListParams) (PageResult[NoticeItem], error) {
	if err := s.authz.Require(ctx, principal, "notice:list"); err != nil {
		return PageResult[NoticeItem]{}, err
	}
	if err := validateListParams(&params); err != nil {
		return PageResult[NoticeItem]{}, err
	}
	result, err := s.repo.ListNotices(ctx, params, false)
	return sanitizeNoticePage(result), err
}

func (s *Service) AdminNoticeDetail(ctx context.Context, principal Principal, id int64) (NoticeItem, error) {
	if err := s.authz.Require(ctx, principal, "notice:list"); err != nil {
		return NoticeItem{}, err
	}
	item, err := s.repo.NoticeByID(ctx, id, false)
	return sanitizeNoticeItem(item), mapNotFound(err, "notice not found")
}

func (s *Service) CreateNotice(ctx context.Context, principal Principal, req SaveNoticeRequest) (NoticeItem, error) {
	if err := s.authz.Require(ctx, principal, "notice:create"); err != nil {
		return NoticeItem{}, err
	}
	item, err := normalizeNotice(req)
	if err != nil {
		return NoticeItem{}, err
	}
	id, err := s.repo.CreateNotice(ctx, item, principal.ID)
	if err != nil {
		return NoticeItem{}, err
	}
	s.invalidateHomeOverview(ctx)
	created, err := s.repo.NoticeByID(ctx, id, false)
	return sanitizeNoticeItem(created), err
}

func sanitizeNoticePage(result PageResult[NoticeItem]) PageResult[NoticeItem] {
	result.Items = sanitizeNoticeItems(result.Items)
	return result
}

func sanitizeNoticeItems(items []NoticeItem) []NoticeItem {
	for index := range items {
		items[index] = sanitizeNoticeItem(items[index])
	}
	return items
}

func sanitizeNoticeItem(item NoticeItem) NoticeItem {
	item.ContentHTML = sanitizeHTML(item.ContentHTML)
	return item
}

func (s *Service) localizeNoticeItems(ctx context.Context, items []NoticeItem) {
	if s.localizer == nil || len(items) == 0 {
		return
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.ID)
	}
	overrides := s.localizer.LocalizeBatch(ctx, "notice", keys)
	for index := range items {
		applyNoticeTranslations(&items[index], overrides[items[index].ID])
	}
}

func (s *Service) localizeNoticeItem(ctx context.Context, item *NoticeItem) {
	if s.localizer == nil || item == nil {
		return
	}
	applyNoticeTranslations(item, s.localizer.Localize(ctx, "notice", item.ID))
}

func applyNoticeTranslations(item *NoticeItem, translations map[string]string) {
	if len(translations) == 0 {
		return
	}
	if value, ok := translations["title"]; ok {
		item.Title = value
	}
	if value, ok := translations["summary"]; ok {
		item.Summary = value
	}
	if value, ok := translations["contentHtml"]; ok {
		item.ContentHTML = value
	}
}

func (s *Service) localizeSiteConfig(ctx context.Context, cfg *SiteConfig) {
	if s.localizer == nil || cfg == nil {
		return
	}
	translations := s.localizer.Localize(ctx, "site", "1")
	if len(translations) == 0 {
		return
	}
	if value, ok := translations["siteName"]; ok {
		cfg.SiteName = value
	}
	if value, ok := translations["slogan"]; ok {
		cfg.Slogan = value
	}
	if value, ok := translations["description"]; ok {
		cfg.Description = value
	}
	if value, ok := translations["seoTitle"]; ok {
		cfg.SEOTitle = value
	}
	if value, ok := translations["seoKeywords"]; ok {
		cfg.SEOKeywords = value
	}
}

func (s *Service) localizeHomeOverview(ctx context.Context, overview HomeOverview) HomeOverview {
	if s.localizer == nil {
		return overview
	}
	s.localizeSiteConfig(ctx, &overview.SiteConfig)
	// The marquee line is the title of the notice that owns the slot, so it
	// translates with the announcement instead of a separate dictionary key.
	if overview.HomeNotice.Enabled == 1 && overview.HomeNotice.NoticeID != "" {
		if value, ok := s.localizer.Localize(ctx, "notice", overview.HomeNotice.NoticeID)["title"]; ok {
			overview.HomeNotice.Content = value
		}
	}

	hotAPIs := make([]HotAPIItem, len(overview.HotAPIs))
	copy(hotAPIs, overview.HotAPIs)
	if len(hotAPIs) > 0 {
		keys := make([]string, 0, len(hotAPIs))
		for _, item := range hotAPIs {
			keys = append(keys, item.ID)
		}
		overrides := s.localizer.LocalizeBatch(ctx, "interface", keys)
		for index := range hotAPIs {
			translations := overrides[hotAPIs[index].ID]
			if value, ok := translations["name"]; ok {
				hotAPIs[index].Name = value
			}
			if value, ok := translations["remark"]; ok {
				hotAPIs[index].Description = value
			}
		}
	}
	overview.HotAPIs = hotAPIs
	return overview
}

func (s *Service) UpdateNotice(ctx context.Context, principal Principal, id int64, req SaveNoticeRequest) error {
	if err := s.authz.Require(ctx, principal, "notice:update"); err != nil {
		return err
	}
	item, err := normalizeNotice(req)
	if err != nil {
		return err
	}
	return s.applyNoticeWrite(ctx, s.repo.UpdateNotice(ctx, id, item, principal.ID))
}

// applyNoticeWrite maps a failed notice mutation to 404 and otherwise busts the
// public homepage snapshot: the rolling bar, the popup and the reader list are
// all served from it, so a publish, edit or flag flip must show on next read.
func (s *Service) applyNoticeWrite(ctx context.Context, err error) error {
	if err != nil {
		return mapNotFound(err, "notice not found")
	}
	s.invalidateHomeOverview(ctx)
	return nil
}

func (s *Service) DeleteNotice(ctx context.Context, principal Principal, id int64) error {
	if err := s.authz.Require(ctx, principal, "notice:delete"); err != nil {
		return err
	}
	return s.applyNoticeWrite(ctx, s.repo.DeleteNotice(ctx, id))
}

func (s *Service) ToggleNoticeStatus(ctx context.Context, principal Principal, id int64, value int) error {
	if err := s.authz.Require(ctx, principal, "notice:status"); err != nil {
		return err
	}
	if value != 0 && value != 1 {
		return apperrors.New(consts.StatusBadRequest, "status must be 0 or 1")
	}
	return s.applyNoticeWrite(ctx, s.repo.UpdateNoticeStatus(ctx, id, value, principal.ID))
}

func (s *Service) ToggleNoticeTop(ctx context.Context, principal Principal, id int64, value int) error {
	if err := s.authz.Require(ctx, principal, "notice:top"); err != nil {
		return err
	}
	if value != 0 && value != 1 {
		return apperrors.New(consts.StatusBadRequest, "isTop must be 0 or 1")
	}
	return s.applyNoticeWrite(ctx, s.repo.UpdateNoticeTop(ctx, id, value, principal.ID))
}

func (s *Service) ToggleNoticePopup(ctx context.Context, principal Principal, id int64, value int) error {
	if err := s.authz.Require(ctx, principal, "notice:popup"); err != nil {
		return err
	}
	if value != 0 && value != 1 {
		return apperrors.New(consts.StatusBadRequest, "isPopup must be 0 or 1")
	}
	return s.applyNoticeWrite(ctx, s.repo.UpdateNoticePopup(ctx, id, value, principal.ID))
}

func (s *Service) HomeTicker(ctx context.Context, principal Principal) (HomeTicker, error) {
	if err := s.authz.Require(ctx, principal, "notice:home-config"); err != nil {
		return HomeTicker{}, err
	}
	return s.repo.HomeTickerSlot(ctx)
}

// SaveHomeTicker keeps the notice:home-config permission it had as a standalone
// page, so a role that may run the rolling bar still cannot reach the rest of
// the announcement list. The text lands on the notice that owns the slot.
func (s *Service) SaveHomeTicker(ctx context.Context, principal Principal, req HomeTicker) error {
	if err := s.authz.Require(ctx, principal, "notice:home-config"); err != nil {
		return err
	}
	if req.Enabled != 0 && req.Enabled != 1 {
		return apperrors.New(consts.StatusBadRequest, "enabled must be 0 or 1")
	}
	content := strings.TrimSpace(req.Content)
	if len([]rune(content)) > maxHomeTickerRunes {
		return apperrors.New(consts.StatusBadRequest, "content is too long")
	}
	if req.Enabled == 1 && content == "" {
		return apperrors.New(consts.StatusBadRequest, "content is required when enabled")
	}
	item := homeTickerNotice(content)
	if err := s.repo.WriteHomeTicker(ctx, item, principal.ID, req.Enabled == 1); err != nil {
		return err
	}
	s.invalidateHomeOverview(ctx)
	return nil
}

// homeTickerNotice renders the marquee line as a one-paragraph announcement.
// Escaping here keeps a headline that contains markup from becoming live HTML.
func homeTickerNotice(content string) NoticeItem {
	html := "<p>" + htmlstd.EscapeString(content) + "</p>"
	return NoticeItem{
		ContentHTML:  html,
		IsHomeTicker: 1,
		IsPopup:      0,
		IsTop:        1,
		Status:       1,
		Summary:      makeSummary(html),
		Title:        content,
	}
}

func (s *Service) PublicSiteConfig(ctx context.Context) (SiteConfig, error) {
	cfg, err := s.repo.SiteConfig(ctx)
	if err != nil {
		return SiteConfig{}, err
	}
	s.localizeSiteConfig(ctx, &cfg)
	return cfg, nil
}

func (s *Service) AdminSiteConfig(ctx context.Context, principal Principal) (SiteConfig, error) {
	if err := s.authz.Require(ctx, principal, "site:config:view"); err != nil {
		return SiteConfig{}, err
	}
	return s.repo.SiteConfig(ctx)
}

func (s *Service) SaveSiteConfig(ctx context.Context, principal Principal, req SiteConfig) error {
	if err := s.authz.Require(ctx, principal, "site:config:update"); err != nil {
		return err
	}
	cfg, err := normalizeSiteConfig(req, s.systemPrefix)
	if err != nil {
		return err
	}
	if err := s.repo.SaveSiteConfig(ctx, cfg); err != nil {
		return err
	}
	s.invalidateHomeOverview(ctx)
	return nil
}

func (s *Service) PublicFriendLinks(ctx context.Context, params ListParams) (PageResult[FriendLinkItem], error) {
	if err := validateListParams(&params); err != nil {
		return PageResult[FriendLinkItem]{}, err
	}
	result, err := s.repo.ListFriendLinks(ctx, params, true)
	if err != nil {
		return PageResult[FriendLinkItem]{}, err
	}
	s.localizeFriendLinks(ctx, result.Items)
	// 访客只需要看到「谁在链我们」，账号归属属于站内信息。
	for i := range result.Items {
		result.Items[i].UserID = ""
	}
	return result, nil
}

// localizeFriendLinks 按请求语言解析友链简介；无译文时保留原文。
func (s *Service) localizeFriendLinks(ctx context.Context, items []FriendLinkItem) {
	if s.localizer == nil || len(items) == 0 {
		return
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		if item.ID != "" {
			keys = append(keys, item.ID)
		}
	}
	overrides := s.localizer.LocalizeBatch(ctx, "friend_link", keys)
	for index := range items {
		if value, ok := overrides[items[index].ID]["description"]; ok && value != "" {
			items[index].Description = value
		}
	}
}

func (s *Service) PublicFriendMeta(ctx context.Context) (PublicMeta, error) {
	cfg, err := s.repo.FriendLinkConfig(ctx)
	if err != nil {
		return PublicMeta{}, err
	}
	site, err := s.repo.SiteConfig(ctx)
	if err != nil {
		return PublicMeta{}, err
	}
	s.localizeSiteConfig(ctx, &site)
	return PublicMeta{Config: cfg, SiteInfo: publicSiteSummary(site)}, nil
}

func (s *Service) ApplyFriendLink(
	ctx context.Context,
	principal Principal,
	req ApplyFriendLinkRequest,
	binding ...string,
) error {
	cfg, err := s.repo.FriendLinkConfig(ctx)
	if err != nil {
		return err
	}
	if cfg.ApplyEnabled != 1 {
		return apperrors.New(consts.StatusBadRequest, "friend link application is disabled")
	}
	app, err := normalizeApplication(req, principal)
	if err != nil {
		return err
	}
	if s.captcha == nil {
		return apperrors.New(consts.StatusServiceUnavailable, "captcha service is temporarily unavailable")
	}
	captchaBinding := ""
	if len(binding) > 0 {
		captchaBinding = binding[0]
	}
	if err := s.captcha.Consume(ctx, req.CaptchaID, "friend_link_apply", captchaBinding); err != nil {
		return err
	}
	return s.createSelfApplication(ctx, principal, app)
}

func (s *Service) ApplyStatus(ctx context.Context, principal Principal) (ApplyStatus, error) {
	return s.repo.LatestApplicationStatus(ctx, principal.ID)
}

func (s *Service) AdminFriendLinks(ctx context.Context, principal Principal, params ListParams) (PageResult[FriendLinkItem], error) {
	if err := s.authz.Require(ctx, principal, "friend-link:list"); err != nil {
		return PageResult[FriendLinkItem]{}, err
	}
	if err := validateListParams(&params); err != nil {
		return PageResult[FriendLinkItem]{}, err
	}
	return s.repo.ListFriendLinks(ctx, params, false)
}

func (s *Service) AdminFriendLinkDetail(ctx context.Context, principal Principal, id int64) (FriendLinkItem, error) {
	if err := s.authz.Require(ctx, principal, "friend-link:list"); err != nil {
		return FriendLinkItem{}, err
	}
	item, err := s.repo.FriendLinkByID(ctx, id)
	return item, mapNotFound(err, "friend link not found")
}

func (s *Service) CreateFriendLink(ctx context.Context, principal Principal, req SaveFriendLinkRequest) (FriendLinkItem, error) {
	if err := s.authz.Require(ctx, principal, "friend-link:create"); err != nil {
		return FriendLinkItem{}, err
	}
	item, err := normalizeFriendLink(req)
	if err != nil {
		return FriendLinkItem{}, err
	}
	id, err := s.repo.CreateFriendLink(ctx, item)
	if err != nil {
		return FriendLinkItem{}, err
	}
	return s.repo.FriendLinkByID(ctx, id)
}

func (s *Service) UpdateFriendLink(ctx context.Context, principal Principal, id int64, req SaveFriendLinkRequest) error {
	if err := s.authz.Require(ctx, principal, "friend-link:update"); err != nil {
		return err
	}
	item, err := normalizeFriendLink(req)
	if err != nil {
		return err
	}
	return mapNotFound(s.repo.UpdateFriendLink(ctx, id, item), "friend link not found")
}

func (s *Service) DeleteFriendLink(
	ctx context.Context,
	principal Principal,
	id int64,
	requests ...DeleteFriendLinkRequest,
) error {
	if err := s.authz.Require(ctx, principal, "friend-link:delete"); err != nil {
		return err
	}
	var req DeleteFriendLinkRequest
	if len(requests) > 0 {
		req = requests[0]
	}
	decision, err := normalizeReviewDecision(req, true)
	if err != nil {
		return err
	}
	item, err := s.repo.DeleteFriendLink(ctx, id, principal, decision)
	if err != nil {
		return mapNotFound(err, "friend link not found")
	}
	if decision.SendEmail {
		s.notifyDeletedFriendLink(
			ctx,
			"deleted:"+time.Now().UTC().Format(time.RFC3339Nano),
			item,
			localizedFriendLinkReason(decision.ReasonTemplate, decision.Reason, item.Language),
		)
	}
	s.invalidateHomeOverview(ctx)
	return nil
}

func (s *Service) UpdateFriendLinkStatus(ctx context.Context, principal Principal, id int64, status int) error {
	if err := s.authz.Require(ctx, principal, "friend-link:status"); err != nil {
		return err
	}
	if status != 0 && status != 1 {
		return apperrors.New(consts.StatusBadRequest, "status must be 0 or 1")
	}
	return mapNotFound(s.repo.UpdateFriendLinkStatus(ctx, id, status), "friend link not found")
}

func (s *Service) ListApplications(ctx context.Context, principal Principal, params ListParams) (PageResult[FriendLinkApplication], error) {
	if err := s.authz.Require(ctx, principal, "friend-link:application:list"); err != nil {
		return PageResult[FriendLinkApplication]{}, err
	}
	if err := validateApplicationListParams(&params); err != nil {
		return PageResult[FriendLinkApplication]{}, err
	}
	return s.repo.ListApplications(ctx, params)
}

// Application statuses have three states (pending, approved, rejected), so the
// generic binary list validation used by notices does not apply here.
func validateApplicationListParams(params *ListParams) error {
	params.Keyword = strings.TrimSpace(params.Keyword)
	if err := listquery.Validate(params.Keyword, params.Page, params.PageSize, 100); err != nil {
		return err
	}
	// 仓储层编号见 applicationFilterStatuses：0=待审、1=通过、2=驳回、3=撤回。
	// 不认识的状态词直接 400，比返回一个空列表更容易让调用方发现拼错。
	if params.Status != nil {
		if _, known := applicationFilterStatuses[*params.Status]; !known {
			return apperrors.New(consts.StatusBadRequest, "status must be PENDING, APPROVED, REJECTED or WITHDRAWN")
		}
	}
	return nil
}

func validateListParams(params *ListParams) error {
	params.Keyword = strings.TrimSpace(params.Keyword)
	if err := listquery.Validate(params.Keyword, params.Page, params.PageSize, 100); err != nil {
		return err
	}
	for name, value := range map[string]*int{
		"isPopup": params.IsPopup,
		"isTop":   params.IsTop,
		"status":  params.Status,
	} {
		if err := listquery.Binary(name, value); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ApproveApplication(
	ctx context.Context,
	principal Principal,
	id int64,
	requests ...ApproveApplicationRequest,
) error {
	if err := s.authz.Require(ctx, principal, "friend-link:application:review"); err != nil {
		return err
	}
	var req ApproveApplicationRequest
	if len(requests) > 0 {
		req = requests[0]
	}
	decision, err := normalizeReviewDecision(req, false)
	if err != nil {
		return err
	}
	app, err := s.repo.ApproveApplication(ctx, id, principal, decision)
	if err != nil {
		return mapNotFound(err, "pending application not found")
	}
	if decision.SendEmail {
		// 撤下请求用「友链已移除」模板，新增与修改仍用「申请通过」模板。
		templateKey := notification.TemplateFriendApproved
		event := "approved"
		if app.ChangeKind == friendLinkChangeRemove {
			templateKey = notification.TemplateFriendDeleted
			event = "removed"
		}
		s.notifyFriendApplication(
			ctx,
			templateKey,
			event,
			app,
			localizedFriendLinkReason(decision.ReasonTemplate, decision.Reason, app.Language),
		)
	}
	s.invalidateHomeOverview(ctx)
	return nil
}

func (s *Service) RejectApplication(ctx context.Context, principal Principal, id int64, req RejectApplicationRequest) error {
	if err := s.authz.Require(ctx, principal, "friend-link:application:review"); err != nil {
		return err
	}
	decision, err := normalizeReviewDecision(req, true)
	if err != nil {
		return err
	}
	app, err := s.repo.RejectApplication(ctx, id, principal, decision)
	if err != nil {
		return mapNotFound(err, "pending application not found")
	}
	if decision.SendEmail {
		s.notifyFriendApplication(
			ctx,
			notification.TemplateFriendRejected,
			"rejected",
			app,
			localizedFriendLinkReason(decision.ReasonTemplate, decision.Reason, app.Language),
		)
	}
	return nil
}

func (s *Service) FriendLinkConfig(ctx context.Context, principal Principal) (FriendLinkConfig, error) {
	if err := s.authz.Require(ctx, principal, "friend-link:config:update"); err != nil {
		return FriendLinkConfig{}, err
	}
	return s.repo.FriendLinkConfig(ctx)
}

func (s *Service) SaveFriendLinkConfig(ctx context.Context, principal Principal, req FriendLinkConfig) error {
	if err := s.authz.Require(ctx, principal, "friend-link:config:update"); err != nil {
		return err
	}
	applyNotice, err := boundedText("applyNotice", req.ApplyNotice, 500, false)
	if err != nil {
		return err
	}
	cfg := FriendLinkConfig{ApplyEnabled: boolInt(req.ApplyEnabled == 1), ApplyNotice: applyNotice}
	return s.repo.SaveFriendLinkConfig(ctx, cfg)
}

func (s *Service) HomeOverview(ctx context.Context) (HomeOverview, error) {
	if cached, ok := s.cachedHomeOverview(ctx); ok {
		return s.localizeHomeOverview(ctx, cached), nil
	}
	result, err := s.loadHomeOverviewSingleflight(ctx, func(loadCtx context.Context, epoch uint64) (HomeOverview, error) {
		loaded, loadErr := s.buildHomeOverview(loadCtx)
		// Only a certified response is cacheable. Caching a degraded verdict
		// would let "statistics are unavailable" outlive the condition that
		// caused it, including after the rollups recover.
		if loadErr == nil && loaded.Stats.AnalyticsReady {
			s.storeHomeOverview(loadCtx, loaded, epoch)
		}
		return loaded, loadErr
	})
	if err != nil {
		if stale, ok := s.staleHomeOverview(); ok {
			return s.localizeHomeOverview(ctx, stale), nil
		}
		return HomeOverview{}, err
	}
	return s.localizeHomeOverview(ctx, result), nil
}

func (s *Service) buildHomeOverview(ctx context.Context) (HomeOverview, error) {
	var site SiteConfig
	var notice HomeTicker
	var record HomeAnalyticsRecord
	var snapshotReady bool
	var firstErr error
	var errMu sync.Mutex
	var wg sync.WaitGroup
	recordError := func(err error) {
		errMu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		errMu.Unlock()
	}
	run := func(load func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if recover() != nil {
					recordError(errHomeOverviewLoaderPanic)
				}
			}()
			if err := load(); err != nil {
				recordError(err)
			}
		}()
	}
	run(func() error {
		var err error
		site, err = s.repo.SiteConfig(ctx)
		return err
	})
	run(func() error {
		var err error
		notice, err = s.repo.HomeTicker(ctx)
		return err
	})
	run(func() error {
		loaded, ok, err := s.repo.LoadHomeAnalyticsSnapshot(ctx)
		record = loaded
		snapshotReady = ok
		return err
	})
	wg.Wait()
	if firstErr != nil {
		return HomeOverview{}, firstErr
	}
	health, healthErr := s.repo.LoadAnalyticsHealth(
		ctx, record.SourceUpdatedAt, record.CoveredUpTo, s.analyticsEnabled, time.Now(),
	)
	if healthErr != nil {
		// Failing the health probe must not be reported as a confident zero.
		// Publish the numbers only with an explicit unavailable verdict so the
		// reader still renders a placeholder.
		health = unavailableAnalyticsHealth(s.analyticsEnabled)
	}
	if !snapshotReady {
		// The seeded row carries no measurement yet. Prefer the last measured
		// payload this process still holds over an all-zero answer, and stamp it
		// with the current verdict so it is never mistaken for fresh.
		if stale, ok := s.staleHomeOverview(); ok {
			stale.SiteConfig = publicSiteSummary(site)
			stale.HomeNotice = notice
			stale.Stats = applyAnalyticsHealth(stale.Stats, health)
			return stale, nil
		}
		return composeHomeOverview(site, notice, HomeAnalyticsSnapshot{}, health), nil
	}
	// The redesigned public homepage no longer renders the historical signal
	// monitor; those fields were removed from the response contract entirely
	// and the snapshot no longer pays for the queries behind them.
	return composeHomeOverview(site, notice, record.Snapshot, health), nil
}

func (s *Service) ValidateNoticeUpload(ctx context.Context, principal Principal) error {
	return s.authz.Require(ctx, principal, "notice:upload-image")
}

func (s *Service) ValidateSiteLogoUpload(ctx context.Context, principal Principal) error {
	return s.authz.Require(ctx, principal, "site:upload-logo")
}

func (s *Service) ValidateLoadingAnimationUpload(ctx context.Context, principal Principal) error {
	return s.authz.Require(ctx, principal, "site:upload-loading-animation")
}

func normalizeNotice(req SaveNoticeRequest) (NoticeItem, error) {
	title := strings.TrimSpace(req.Title)
	if title == "" {
		return NoticeItem{}, apperrors.New(consts.StatusBadRequest, "title is required")
	}
	if len([]rune(title)) > maxNoticeTitleRunes {
		return NoticeItem{}, apperrors.New(consts.StatusBadRequest, "title is too long")
	}
	expireTime, err := normalizeNoticeTime(req.ExpireTime)
	if err != nil {
		return NoticeItem{}, err
	}
	if len(req.ContentHTML) > maxNoticeHTMLBytes {
		return NoticeItem{}, apperrors.New(consts.StatusRequestEntityTooLarge, "contentHtml is too large")
	}
	html := sanitizeHTML(req.ContentHTML)
	if len(html) > maxNoticeHTMLBytes {
		return NoticeItem{}, apperrors.New(consts.StatusRequestEntityTooLarge, "sanitized contentHtml is too large")
	}
	if strings.TrimSpace(stripTags(html)) == "" && !strings.Contains(strings.ToLower(html), "<img") && !strings.Contains(strings.ToLower(html), "<video") {
		return NoticeItem{}, apperrors.New(consts.StatusBadRequest, "contentHtml is required")
	}
	item := NoticeItem{
		ContentHTML:  html,
		ExpireTime:   expireTime,
		IsHomeTicker: intPtrValue(req.IsHomeTicker, 0),
		IsPopup:      intPtrValue(req.IsPopup, 0),
		IsTop:        intPtrValue(req.IsTop, 0),
		Status:       intPtrValue(req.Status, 1),
		Summary:      makeSummary(html),
		Title:        title,
	}
	if item.IsPopup != 0 && item.IsPopup != 1 || item.IsTop != 0 && item.IsTop != 1 ||
		item.IsHomeTicker != 0 && item.IsHomeTicker != 1 || item.Status != 0 && item.Status != 1 {
		return NoticeItem{}, apperrors.New(consts.StatusBadRequest, "status fields must be 0 or 1")
	}
	return item, nil
}

// normalizeNoticeTime accepts the two formats the editors post and stores the
// canonical one, so a mistyped expiry is a 400 rather than a driver error.
func normalizeNoticeTime(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	for _, layout := range []string{noticeTimeLayout, time.RFC3339} {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return parsed.Format(noticeTimeLayout), nil
		}
	}
	return "", apperrors.New(consts.StatusBadRequest, "expireTime must be YYYY-MM-DD HH:mm:ss")
}

func normalizeSiteConfig(req SiteConfig, systemPrefix string) (SiteConfig, error) {
	fields := []struct {
		name  string
		value string
		limit int
	}{
		{"contactAddress", req.ContactAddress, 255}, {"contactEmail", req.ContactEmail, 128},
		{"contactPhone", req.ContactPhone, 64}, {"contactQq", req.ContactQq, 64},
		{"contactWechat", req.ContactWechat, 64}, {"copyright", req.Copyright, 128},
		{"description", req.Description, 255}, {"icp", req.ICP, 128},
		{"authorName", req.AuthorName, 128}, {"licenseType", req.LicenseType, 64},
		{"seoTitle", req.SEOTitle, 128}, {"siteName", req.SiteName, 64},
		{"slogan", req.Slogan, 120},
	}
	values := make(map[string]string, len(fields))
	for _, field := range fields {
		value, err := boundedText(field.name, field.value, field.limit, false)
		if err != nil {
			return SiteConfig{}, err
		}
		values[field.name] = value
	}
	if values["contactEmail"] != "" && !validEmail(values["contactEmail"]) {
		return SiteConfig{}, apperrors.New(consts.StatusBadRequest, "contactEmail is invalid")
	}
	seoKeywords, err := normalizeSEOKeywords(req.SEOKeywords)
	if err != nil {
		return SiteConfig{}, err
	}
	analyticsCode, err := normalizeAnalyticsCode(req.AnalyticsCode)
	if err != nil {
		return SiteConfig{}, err
	}
	ogImage, err := normalizeOGImage(req.OGImage, systemPrefix+"/upload/")
	if err != nil {
		return SiteConfig{}, err
	}
	siteURL, err := normalizeSiteBaseURL(req.SiteURL)
	if err != nil {
		return SiteConfig{}, err
	}
	githubURL, err := normalizeOptionalHTTPURL("githubUrl", req.GitHubURL)
	if err != nil {
		return SiteConfig{}, err
	}
	authorURL, err := normalizeOptionalHTTPURL("authorUrl", req.AuthorURL)
	if err != nil {
		return SiteConfig{}, err
	}
	licenseURL, err := normalizeOptionalHTTPURL("licenseUrl", req.LicenseURL)
	if err != nil {
		return SiteConfig{}, err
	}
	if req.Noindex != 0 && req.Noindex != 1 {
		return SiteConfig{}, apperrors.New(consts.StatusBadRequest, "noindex must be 0 or 1")
	}
	if req.LoadingAnimationEnabled != 0 && req.LoadingAnimationEnabled != 1 {
		return SiteConfig{}, apperrors.New(consts.StatusBadRequest, "loadingAnimationEnabled must be 0 or 1")
	}
	loadingAnimationPreset := strings.ToLower(strings.TrimSpace(req.LoadingAnimationPreset))
	if loadingAnimationPreset == "" {
		loadingAnimationPreset = "gateway"
	}
	if _, ok := loadingAnimationPresets[loadingAnimationPreset]; !ok {
		return SiteConfig{}, apperrors.New(consts.StatusBadRequest, "loadingAnimationPreset is invalid")
	}
	globalLoadingPreset := strings.ToLower(strings.TrimSpace(req.GlobalLoadingPreset))
	if globalLoadingPreset == "" {
		globalLoadingPreset = "bar"
	}
	if _, ok := globalLoadingPresets[globalLoadingPreset]; !ok {
		return SiteConfig{}, apperrors.New(consts.StatusBadRequest, "globalLoadingPreset is invalid")
	}
	cfg := SiteConfig{
		ContactAddress: values["contactAddress"], ContactEmail: values["contactEmail"],
		ContactPhone: values["contactPhone"], ContactQq: values["contactQq"],
		ContactWechat: values["contactWechat"], Copyright: values["copyright"],
		AnalyticsCode: analyticsCode,
		AuthorName:    values["authorName"], AuthorURL: authorURL,
		Description: values["description"], ICP: values["icp"],
		GitHubURL:   githubURL,
		LicenseType: values["licenseType"], LicenseURL: licenseURL,
		GlobalLoadingPreset:     globalLoadingPreset,
		LoadingAnimationEnabled: req.LoadingAnimationEnabled,
		LoadingAnimationPreset:  loadingAnimationPreset,
		LoadingAnimationURL:     strings.TrimSpace(req.LoadingAnimationURL),
		LogoURL:                 strings.TrimSpace(req.LogoURL),
		Noindex:                 req.Noindex,
		OGImage:                 ogImage,
		SEOKeywords:             seoKeywords,
		SEOTitle:                values["seoTitle"],
		SiteName:                values["siteName"], SiteURL: siteURL, Slogan: values["slogan"],
	}
	if cfg.SiteName == "" {
		return SiteConfig{}, apperrors.New(consts.StatusBadRequest, "siteName is required")
	}
	if cfg.LogoURL != "" && !validImageURL(cfg.LogoURL, systemPrefix+"/upload/site-logo/") {
		return SiteConfig{}, apperrors.New(consts.StatusBadRequest, "logoUrl is invalid")
	}
	if cfg.LoadingAnimationURL != "" && !validImageURL(cfg.LoadingAnimationURL, systemPrefix+"/upload/loading-animation/") {
		return SiteConfig{}, apperrors.New(consts.StatusBadRequest, "loadingAnimationUrl is invalid")
	}
	if cfg.LoadingAnimationEnabled == 1 && cfg.LoadingAnimationURL == "" {
		return SiteConfig{}, apperrors.New(consts.StatusBadRequest, "loadingAnimationUrl is required when custom loading animation is enabled")
	}
	return cfg, nil
}

func normalizeAnalyticsCode(raw string) (string, error) {
	value := strings.TrimPrefix(raw, "\ufeff")
	if len(value) > maxAnalyticsCodeBytes {
		return "", apperrors.New(consts.StatusRequestEntityTooLarge, "analyticsCode is too large")
	}
	if strings.ContainsRune(value, '\x00') {
		return "", apperrors.New(consts.StatusBadRequest, "analyticsCode cannot contain NUL")
	}
	return value, nil
}

func publicSiteSummary(site SiteConfig) SiteConfig {
	site.AnalyticsCode = ""
	return site
}

// normalizeSEOKeywords 规范化 SEO 关键词：trim、≤512、中文逗号「，」统一为英文逗号。
func normalizeSEOKeywords(raw string) (string, error) {
	value, err := boundedText("seoKeywords", raw, 512, false)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(value, "，", ","), nil
}

// normalizeOGImage 校验 OG 分享图 URL：可空；非空时必须是 https?:// 绝对地址，
// 或以系统上传前缀开头的相对路径（复用 validImageURL 思路）。长度 ≤1024。
func normalizeOGImage(raw, localPrefix string) (string, error) {
	value, err := boundedText("ogImage", raw, 1024, false)
	if err != nil {
		return "", err
	}
	if value != "" && !validImageURL(value, localPrefix) {
		return "", apperrors.New(consts.StatusBadRequest, "ogImage is invalid")
	}
	return value, nil
}

// normalizeSiteBaseURL 规范化站点对外基础地址：可空；非空时必须是 https?:// 开头，
// 长度 ≤255，并去掉结尾斜杠。
func normalizeSiteBaseURL(raw string) (string, error) {
	value, err := boundedText("siteUrl", raw, 255, false)
	if err != nil {
		return "", err
	}
	if value == "" {
		return "", nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", apperrors.New(consts.StatusBadRequest, "siteUrl must be http or https")
	}
	return strings.TrimRight(value, "/"), nil
}

func normalizeOptionalHTTPURL(name, raw string) (string, error) {
	value, err := boundedText(name, raw, 1024, false)
	if err != nil || value == "" {
		return value, err
	}
	if containsUnsafeURLCharacter(value) {
		return "", apperrors.New(consts.StatusBadRequest, name+" must be http or https")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", apperrors.New(consts.StatusBadRequest, name+" must be http or https")
	}
	return value, nil
}

func normalizeFriendLink(req SaveFriendLinkRequest) (FriendLinkItem, error) {
	siteName := strings.TrimSpace(req.SiteName)
	if siteName == "" {
		return FriendLinkItem{}, apperrors.New(consts.StatusBadRequest, "siteName is required")
	}
	if len([]rune(siteName)) > 100 {
		return FriendLinkItem{}, apperrors.New(consts.StatusBadRequest, "siteName is too long")
	}
	siteURL, normalized, err := normalizeSiteURL(req.SiteURL)
	if err != nil {
		return FriendLinkItem{}, err
	}
	status := intPtrValue(req.Status, 1)
	if status != 0 && status != 1 {
		return FriendLinkItem{}, apperrors.New(consts.StatusBadRequest, "status must be 0 or 1")
	}
	logo := strings.TrimSpace(req.LogoURL)
	if logo != "" && !validImageURL(logo, "") {
		return FriendLinkItem{}, apperrors.New(consts.StatusBadRequest, "logoUrl is invalid")
	}
	applicantName, err := boundedText("applicantName", req.ApplicantName, 64, false)
	if err != nil {
		return FriendLinkItem{}, err
	}
	description, err := boundedText("description", req.Description, 255, false)
	if err != nil {
		return FriendLinkItem{}, err
	}
	applicantEmail, err := boundedText("applicantEmail", req.ApplicantEmail, 128, false)
	if err != nil {
		return FriendLinkItem{}, err
	}
	if applicantEmail != "" && !validEmail(applicantEmail) {
		return FriendLinkItem{}, apperrors.New(consts.StatusBadRequest, "applicantEmail is invalid")
	}
	if req.SortNo < -1_000_000 || req.SortNo > 1_000_000 {
		return FriendLinkItem{}, apperrors.New(consts.StatusBadRequest, "sortNo is out of range")
	}
	return FriendLinkItem{
		ApplicantEmail: applicantEmail, ApplicantName: applicantName, Description: description,
		LogoURL:           logo,
		Language:          normalizeFriendLinkLanguage(req.Language),
		NormalizedSiteURL: normalized,
		SiteName:          siteName,
		SiteURL:           siteURL,
		SortNo:            req.SortNo,
		Status:            status,
	}, nil
}

// friendLinkSiteFields 是友链「网站侧」字段校验后的结果。公开申请与用户自助修改共用
// 同一套规则，避免两条入口对同一字段给出不同的宽严。
type friendLinkSiteFields struct {
	BacklinkURL       string
	Description       string
	LogoURL           string
	NormalizedSiteURL string
	SiteName          string
	SiteURL           string
}

func validateFriendLinkSiteFields(siteName, siteURL, logoURL, description, backlinkURL string) (friendLinkSiteFields, error) {
	normalizedURL, normalized, err := normalizeSiteURL(siteURL)
	if err != nil {
		return friendLinkSiteFields{}, err
	}
	trimmedSiteName := strings.TrimSpace(siteName)
	if trimmedSiteName == "" {
		return friendLinkSiteFields{}, apperrors.New(consts.StatusBadRequest, "siteName is required")
	}
	backlink := strings.TrimSpace(backlinkURL)
	if backlink != "" {
		normalizedBacklink, _, err := normalizeSiteURL(backlink)
		if err != nil {
			return friendLinkSiteFields{}, apperrors.New(consts.StatusBadRequest, "backlinkUrl is invalid")
		}
		backlink = normalizedBacklink
	}
	logo := strings.TrimSpace(logoURL)
	if logo != "" && !validImageURL(logo, "") {
		return friendLinkSiteFields{}, apperrors.New(consts.StatusBadRequest, "logoUrl is invalid")
	}
	typedDescription, err := boundedText("description", description, 255, false)
	if err != nil {
		return friendLinkSiteFields{}, err
	}
	if len([]rune(trimmedSiteName)) > 100 {
		return friendLinkSiteFields{}, apperrors.New(consts.StatusBadRequest, "siteName is too long")
	}
	return friendLinkSiteFields{
		BacklinkURL:       backlink,
		Description:       typedDescription,
		LogoURL:           logo,
		NormalizedSiteURL: normalized,
		SiteName:          trimmedSiteName,
		SiteURL:           normalizedURL,
	}, nil
}

func normalizeApplication(req ApplyFriendLinkRequest, principal Principal) (FriendLinkApplication, error) {
	fields, err := validateFriendLinkSiteFields(req.SiteName, req.SiteURL, req.LogoURL, req.Description, req.BacklinkURL)
	if err != nil {
		return FriendLinkApplication{}, err
	}
	contactName, err := boundedText("contactName", req.ContactName, 64, false)
	if err != nil {
		return FriendLinkApplication{}, err
	}
	contactQq, err := boundedText("contactQq", req.ContactQq, 64, false)
	if err != nil {
		return FriendLinkApplication{}, err
	}
	contactEmail, err := boundedText("contactEmail", req.ContactEmail, 128, false)
	if err != nil {
		return FriendLinkApplication{}, err
	}
	if contactEmail != "" && !validEmail(contactEmail) {
		return FriendLinkApplication{}, apperrors.New(consts.StatusBadRequest, "contactEmail is invalid")
	}
	if contactEmail == "" {
		return FriendLinkApplication{}, apperrors.New(consts.StatusBadRequest, "contactEmail is required")
	}
	return FriendLinkApplication{
		BacklinkURL:  fields.BacklinkURL,
		ChangeKind:   friendLinkChangeNew,
		ContactEmail: contactEmail, ContactName: contactName, ContactQq: contactQq, Description: fields.Description,
		LogoURL:           fields.LogoURL,
		Language:          normalizeFriendLinkLanguage(req.Language),
		NormalizedSiteURL: fields.NormalizedSiteURL,
		SiteName:          fields.SiteName,
		SiteURL:           fields.SiteURL,
		UserID:            strconv.FormatInt(principal.ID, 10),
		Username:          principal.Username,
	}, nil
}

func normalizeSiteURL(raw string) (string, string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", "", apperrors.New(consts.StatusBadRequest, "siteUrl is required")
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	if len(value) > 1024 || containsUnsafeURLCharacter(value) {
		return "", "", apperrors.New(consts.StatusBadRequest, "url is too long")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", "", apperrors.New(consts.StatusBadRequest, "url must be http or https")
	}
	host, err := normalizeHostname(parsed.Hostname())
	if err != nil {
		return "", "", apperrors.New(consts.StatusBadRequest, "url host is invalid")
	}
	port := parsed.Port()
	if port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return "", "", apperrors.New(consts.StatusBadRequest, "url port is invalid")
		}
		if (parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443") {
			port = ""
		}
	}
	displayHost := host
	if strings.Contains(host, ":") {
		displayHost = "[" + host + "]"
	}
	if port != "" {
		displayHost = net.JoinHostPort(host, port)
	}
	parsed.Host = displayHost
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	display := parsed.String()
	dedupeHost := strings.TrimPrefix(host, "www.")
	if strings.Contains(dedupeHost, ":") {
		dedupeHost = "[" + dedupeHost + "]"
	}
	if port != "" {
		dedupeHost = net.JoinHostPort(host, port)
	}
	normalized := parsed.Scheme + "://" + dedupeHost
	if parsed.Path != "" && parsed.Path != "/" {
		normalized += strings.TrimRight(parsed.EscapedPath(), "/")
	}
	return display, normalized, nil
}

func validImageURL(value, localPrefix string) bool {
	if len(value) > 1024 {
		return false
	}
	if localPrefix != "" && strings.HasPrefix(value, localPrefix) {
		name := strings.TrimPrefix(value, localPrefix)
		return upload.ValidGeneratedImageName(name)
	}
	_, _, err := normalizeSiteURL(value)
	return err == nil
}

func normalizeHostname(value string) (string, error) {
	value = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if value == "" || len(value) > 253 {
		return "", url.InvalidHostError(value)
	}
	if ip := net.ParseIP(value); ip != nil {
		return strings.ToLower(ip.String()), nil
	}
	if strings.IndexFunc(value, func(r rune) bool { return r > unicode.MaxASCII }) >= 0 {
		return "", url.InvalidHostError(value)
	}
	ascii := value
	for _, label := range strings.Split(ascii, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", url.InvalidHostError(value)
		}
	}
	return strings.ToLower(ascii), nil
}

func containsUnsafeURLCharacter(value string) bool {
	return strings.Contains(value, "\\") || strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0
}

func boundedText(name, value string, limit int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", apperrors.New(consts.StatusBadRequest, name+" is required")
	}
	if len([]rune(value)) > limit {
		return "", apperrors.New(consts.StatusBadRequest, name+" is too long")
	}
	return value, nil
}

func validEmail(value string) bool {
	return emailaddr.Valid(value)
}

func intPtrValue(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func boolInt(ok bool) int {
	if ok {
		return 1
	}
	return 0
}

func mapNotFound(err error, message string) error {
	if err == nil {
		return nil
	}
	if err == sql.ErrNoRows {
		return apperrors.New(consts.StatusNotFound, message)
	}
	return err
}

var (
	spaceRe               = regexp.MustCompile(`\s+`)
	safeContentURLPattern = regexp.MustCompile(`(?i)^(?:https?://|/[^/\\]|/$)`)
	numericAttrPattern    = regexp.MustCompile(`^[0-9]{1,5}$`)
	targetBlankPattern    = regexp.MustCompile(`^_blank$`)
	mediaTypePattern      = regexp.MustCompile(`(?i)^[a-z0-9.+-]+/[a-z0-9.+-]+$`)
	noticeHTMLPolicy      = newNoticeHTMLPolicy()
	plainTextPolicy       = newPlainTextPolicy()
)

func sanitizeHTML(input string) string {
	return stripUnsafeContentURLs(noticeHTMLPolicy.Sanitize(input))
}

// SanitizeNoticeHTML 暴露共享富文本策略，供翻译字典按默认语言同样的规则
// 校验公告译文。
func SanitizeNoticeHTML(input string) string {
	return sanitizeHTML(input)
}

func stripUnsafeContentURLs(input string) string {
	contextNode := &htmlnode.Node{Type: htmlnode.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := htmlnode.ParseFragment(strings.NewReader(input), contextNode)
	if err != nil {
		return ""
	}
	for _, node := range nodes {
		walkHTMLNodes(node, func(current *htmlnode.Node) {
			if current.Type != htmlnode.ElementNode {
				return
			}
			attrs := current.Attr[:0]
			for _, attr := range current.Attr {
				name := strings.ToLower(strings.TrimSpace(attr.Key))
				if (name == "href" || name == "src") && !safeContentURL(attr.Val) {
					continue
				}
				attrs = append(attrs, attr)
			}
			current.Attr = attrs
		})
	}
	var output bytes.Buffer
	for _, node := range nodes {
		if err := htmlnode.Render(&output, node); err != nil {
			return ""
		}
	}
	return output.String()
}

func walkHTMLNodes(node *htmlnode.Node, visit func(*htmlnode.Node)) {
	if node == nil {
		return
	}
	visit(node)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkHTMLNodes(child, visit)
	}
}

func safeContentURL(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "\\") || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	if strings.HasPrefix(value, "/") {
		return !strings.HasPrefix(value, "//")
	}
	parsed, err := url.Parse(value)
	return err == nil &&
		parsed.IsAbs() &&
		(strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) &&
		parsed.Host != "" &&
		parsed.User == nil &&
		parsed.Opaque == ""
}

func newNoticeHTMLPolicy() *bluemonday.Policy {
	policy := bluemonday.NewPolicy()
	policy.AllowElements(
		"a", "b", "blockquote", "br", "code", "div", "em", "h1", "h2", "h3", "h4", "h5", "h6",
		"hr", "i", "img", "li", "ol", "p", "pre", "source", "span", "strong", "table", "tbody", "td",
		"th", "thead", "tr", "u", "ul", "video",
	)
	policy.AllowAttrs("href").Matching(safeContentURLPattern).OnElements("a")
	policy.AllowAttrs("title").OnElements("a", "img")
	policy.AllowAttrs("target").Matching(targetBlankPattern).OnElements("a")
	policy.AllowAttrs("alt").OnElements("img")
	policy.AllowAttrs("src").Matching(safeContentURLPattern).OnElements("img", "source", "video")
	policy.AllowAttrs("height", "width").Matching(numericAttrPattern).OnElements("img", "video")
	policy.AllowAttrs("type").Matching(mediaTypePattern).OnElements("source")
	policy.AllowAttrs("controls").OnElements("video")
	policy.AllowURLSchemes("http", "https")
	policy.AllowRelativeURLs(true)
	policy.RequireParseableURLs(true)
	policy.RequireNoFollowOnFullyQualifiedLinks(true)
	policy.RequireNoReferrerOnLinks(true)
	policy.AddSpaceWhenStrippingTag(true)
	policy.SkipElementsContent("script", "style", "iframe", "object", "embed", "svg", "math", "template", "noscript")
	return policy
}

func newPlainTextPolicy() *bluemonday.Policy {
	policy := bluemonday.StrictPolicy()
	policy.AddSpaceWhenStrippingTag(true)
	return policy
}

func makeSummary(html string) string {
	text := strings.TrimSpace(spaceRe.ReplaceAllString(stripTags(html), " "))
	runes := []rune(text)
	if len(runes) > 120 {
		return string(runes[:120])
	}
	return text
}

func stripTags(html string) string {
	return htmlstd.UnescapeString(plainTextPolicy.Sanitize(html))
}
