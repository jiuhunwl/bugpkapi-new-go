package content

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"

	"bugpk-api/internal/domain/auth"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/jsonrequest"
	"bugpk-api/internal/pkg/response"
	"bugpk-api/internal/pkg/upload"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
)

type Handler struct {
	donationQRCodeUploadDir   string
	loadingAnimationUploadDir string
	noticeUploadDir           string
	service                   *Service
	siteUploadDir             string
}

func NewHandler(service *Service) *Handler {
	return &Handler{
		donationQRCodeUploadDir:   filepath.Join("uploads", "donation-qr"),
		loadingAnimationUploadDir: filepath.Join("uploads", "loading-animation"),
		noticeUploadDir:           filepath.Join("uploads", "notice-image"),
		service:                   service,
		siteUploadDir:             filepath.Join("uploads", "site-logo"),
	}
}

func (h *Handler) RegisterNotice(group *route.RouterGroup) {
	group.GET("/user/list", h.publicNoticeList)
	group.GET("/user/popup", h.popupNotices)
	group.GET("/user/:id", h.publicNoticeDetail)
	group.GET("/admin/list", h.adminNoticeList)
	group.GET("/admin/home-config", h.homeTicker)
	group.PUT("/admin/home-config", h.saveHomeTicker)
	group.POST("/admin", h.createNotice)
	group.PUT("/admin/:id/status", h.noticeStatus)
	group.PUT("/admin/:id/top", h.noticeTop)
	group.PUT("/admin/:id/popup", h.noticePopup)
	group.GET("/admin/:id", h.adminNoticeDetail)
	group.PUT("/admin/:id", h.updateNotice)
	group.DELETE("/admin/:id", h.deleteNotice)
}

func (h *Handler) RegisterSite(group *route.RouterGroup) {
	group.GET("/config", h.publicSiteConfig)
	group.GET("/admin/config", h.adminSiteConfig)
	group.PUT("/admin/config", h.saveSiteConfig)
}

func (h *Handler) RegisterAgreement(group *route.RouterGroup) {
	group.GET("/agreements/:key", h.publicAgreement)
	group.GET("/admin/agreements", h.adminAgreements)
	group.PUT("/admin/agreement-mode", h.saveAgreementMode)
	group.PUT("/admin/agreements/:key/:mode/:lang", h.saveAgreementTemplate)
}

func (h *Handler) RegisterDonation(group *route.RouterGroup) {
	group.GET("/config", h.publicDonationConfig)
	group.GET("/supporters", h.publicDonationSupporters)
	group.GET("/admin/config", h.adminDonationConfig)
	group.GET("/admin/supporters", h.adminDonationSupporters)
	group.POST("/admin/supporters", h.createDonationSupporter)
	group.PUT("/admin/config", h.saveDonationConfig)
}

func (h *Handler) RegisterFriendLinks(group *route.RouterGroup) {
	group.GET("/public/list", h.publicFriendLinks)
	group.GET("/public/meta", h.publicFriendMeta)
	group.POST("/apply", h.applyFriendLink)
	group.GET("/apply/status", h.applyStatus)
	group.GET("/admin/list", h.adminFriendLinks)
	group.GET("/admin/applications", h.friendApplications)
	group.PUT("/admin/applications/:id/approve", h.approveApplication)
	group.PUT("/admin/applications/:id/reject", h.rejectApplication)
	group.GET("/admin/config", h.friendLinkConfig)
	group.PUT("/admin/config", h.saveFriendLinkConfig)
	group.POST("/admin", h.createFriendLink)
	group.PUT("/admin/:id/status", h.friendLinkStatus)
	group.GET("/admin/:id", h.adminFriendLinkDetail)
	group.PUT("/admin/:id", h.updateFriendLink)
	group.DELETE("/admin/:id", h.deleteFriendLink)
	// 自助入口只按登录账号收窄数据，不要求任何管理权限码。
	group.GET("/self/list", h.myFriendLinks)
	group.PUT("/self/links/:id", h.submitFriendLinkChange)
	group.DELETE("/self/links/:id", h.submitFriendLinkRemoval)
	group.DELETE("/self/requests/:id", h.withdrawFriendLinkRequest)
}

func (h *Handler) RegisterHome(group *route.RouterGroup) {
	group.GET("/overview", h.homeOverview)
}

func (h *Handler) RegisterUpload(group *route.RouterGroup) {
	group.POST("/donation-qr", h.uploadDonationQRCode)
	group.GET("/donation-qr/:filename", h.donationQRCode)
	group.POST("/loading-animation", h.uploadLoadingAnimation)
	group.GET("/loading-animation/:filename", h.loadingAnimation)
	group.POST("/notice-image", h.uploadNoticeImage)
	group.GET("/notice-image/:filename", h.noticeImage)
	group.POST("/site-logo", h.uploadSiteLogo)
	group.GET("/site-logo/:filename", h.siteLogo)
}

func (h *Handler) publicNoticeList(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.PublicNoticeList(ctx, listParams(c))
	respond(c, result, err)
}

func (h *Handler) publicNoticeDetail(ctx context.Context, c *hertzapp.RequestContext) {
	id, err := pathID(c)
	if err != nil {
		respond(c, nil, err)
		return
	}
	result, err := h.service.PublicNoticeDetail(ctx, id)
	respond(c, result, err)
}

func (h *Handler) popupNotices(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.PopupNotices(ctx)
	respond(c, result, err)
}

func (h *Handler) adminNoticeList(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.AdminNoticeList(ctx, p, listParams(c))
	respond(c, result, err)
}

func (h *Handler) adminNoticeDetail(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.AdminNoticeDetail(ctx, p, id)
	respond(c, result, err)
}

func (h *Handler) createNotice(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveNoticeRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.CreateNotice(ctx, p, req)
	respond(c, result, err)
}

func (h *Handler) updateNotice(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req SaveNoticeRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.UpdateNotice(ctx, p, id, req))
}

func (h *Handler) deleteNotice(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	respond(c, true, h.service.DeleteNotice(ctx, p, id))
}

func (h *Handler) noticeStatus(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req ToggleRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.ToggleNoticeStatus(ctx, p, id, intPtrValue(req.Status, -1)))
}

func (h *Handler) noticeTop(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req ToggleRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.ToggleNoticeTop(ctx, p, id, intPtrValue(req.IsTop, -1)))
}

func (h *Handler) noticePopup(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req ToggleRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.ToggleNoticePopup(ctx, p, id, intPtrValue(req.IsPopup, -1)))
}

func (h *Handler) homeTicker(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.HomeTicker(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveHomeTicker(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req HomeTicker
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveHomeTicker(ctx, p, req))
}

func (h *Handler) publicSiteConfig(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.PublicSiteConfig(ctx)
	respond(c, result, err)
}

func (h *Handler) adminSiteConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.AdminSiteConfig(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveSiteConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SiteConfig
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveSiteConfig(ctx, p, req))
}

func (h *Handler) publicAgreement(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.PublicAgreement(ctx, c.Param("key"), string(c.Query("lang")))
	respond(c, result, err)
}

func (h *Handler) adminAgreements(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.AdminAgreements(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveAgreementMode(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveAgreementModeRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveAgreementMode(ctx, p, req))
}

func (h *Handler) saveAgreementTemplate(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveAgreementTemplateRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveAgreementTemplate(ctx, p, c.Param("key"), c.Param("mode"), c.Param("lang"), req))
}

func (h *Handler) publicDonationConfig(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.PublicDonationConfig(ctx)
	respond(c, result, err)
}

func (h *Handler) publicDonationSupporters(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.PublicDonationSupporters(ctx, listParams(c))
	respond(c, result, err)
}

func (h *Handler) adminDonationConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.AdminDonationConfig(ctx, p)
	respond(c, result, err)
}

func (h *Handler) adminDonationSupporters(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.AdminDonationSupporters(ctx, p, listParams(c))
	respond(c, result, err)
}

func (h *Handler) createDonationSupporter(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req CreateDonationSupporterRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.CreateDonationSupporter(ctx, p, req)
	respond(c, result, err)
}

func (h *Handler) saveDonationConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveDonationConfigRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveDonationConfig(ctx, p, req))
}

func (h *Handler) publicFriendLinks(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.PublicFriendLinks(ctx, listParams(c))
	respond(c, result, err)
}

func (h *Handler) publicFriendMeta(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.PublicFriendMeta(ctx)
	respond(c, result, err)
}

func (h *Handler) applyFriendLink(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req ApplyFriendLinkRequest
	if !bind(c, &req) {
		return
	}
	// Capture the actual page language at submission time. Older clients may
	// omit the JSON field, while current clients also send X-App-Locale. The
	// explicit request field remains the highest-priority snapshot.
	req.Language = auth.ResolveEmailLanguage(
		req.Language,
		"",
		string(c.Request.Header.Peek("X-App-Locale")),
		string(c.Request.Header.Peek("Accept-Language")),
		"",
	)
	binding := auth.CaptchaBinding(
		string(c.Request.Header.Peek("User-Agent")),
		string(c.Request.Header.Peek("Accept-Language")),
	)
	respond(c, true, h.service.ApplyFriendLink(ctx, p, req, binding))
}

func (h *Handler) applyStatus(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.ApplyStatus(ctx, p)
	respond(c, result, err)
}

func (h *Handler) myFriendLinks(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.MyFriendLinks(ctx, p)
	respond(c, result, err)
}

func (h *Handler) submitFriendLinkChange(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req SubmitFriendLinkChangeRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SubmitLinkChange(ctx, p, id, req))
}

func (h *Handler) submitFriendLinkRemoval(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	respond(c, true, h.service.SubmitLinkRemoval(ctx, p, id))
}

func (h *Handler) withdrawFriendLinkRequest(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	respond(c, true, h.service.WithdrawLinkRequest(ctx, p, id))
}

func (h *Handler) adminFriendLinks(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.AdminFriendLinks(ctx, p, listParams(c))
	respond(c, result, err)
}

func (h *Handler) adminFriendLinkDetail(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.AdminFriendLinkDetail(ctx, p, id)
	respond(c, result, err)
}

func (h *Handler) createFriendLink(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveFriendLinkRequest
	if !bind(c, &req) {
		return
	}
	// An administrator may create a link on behalf of an applicant. Persist
	// the current page language as the link's notification snapshot so a later
	// deletion email does not silently fall back to Chinese.
	req.Language = auth.ResolveEmailLanguage(
		req.Language,
		"",
		string(c.Request.Header.Peek("X-App-Locale")),
		string(c.Request.Header.Peek("Accept-Language")),
		"",
	)
	result, err := h.service.CreateFriendLink(ctx, p, req)
	respond(c, result, err)
}

func (h *Handler) updateFriendLink(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req SaveFriendLinkRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.UpdateFriendLink(ctx, p, id, req))
}

func (h *Handler) deleteFriendLink(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req DeleteFriendLinkRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.DeleteFriendLink(ctx, p, id, req))
}

func (h *Handler) friendLinkStatus(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req ToggleRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.UpdateFriendLinkStatus(ctx, p, id, intPtrValue(req.Status, -1)))
}

func (h *Handler) friendApplications(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	params := listParams(c)
	// 申请列表按状态枚举名过滤。listParams 的数字口径会把任何非数字值兜底成 2（驳回），
	// 所以这里始终按名字重新解释一次；未知值交给仓储层收窄成「匹配不到任何行」。
	params.Status = applicationStatusFilter(string(c.Query("status")))
	result, err := h.service.ListApplications(ctx, p, params)
	respond(c, result, err)
}

func (h *Handler) approveApplication(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req ApproveApplicationRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.ApproveApplication(ctx, p, id, req))
}

func (h *Handler) rejectApplication(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req RejectApplicationRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.RejectApplication(ctx, p, id, req))
}

func (h *Handler) friendLinkConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.FriendLinkConfig(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveFriendLinkConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req FriendLinkConfig
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveFriendLinkConfig(ctx, p, req))
}

func (h *Handler) homeOverview(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.HomeOverview(ctx)
	respond(c, result, err)
}

func (h *Handler) uploadNoticeImage(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	if err := h.service.ValidateNoticeUpload(ctx, p); err != nil {
		respond(c, nil, err)
		return
	}
	h.upload(ctx, c, h.noticeUploadDir, h.service.systemPrefix+"/upload/notice-image/")
}

func (h *Handler) uploadSiteLogo(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	if err := h.service.ValidateSiteLogoUpload(ctx, p); err != nil {
		respond(c, nil, err)
		return
	}
	h.upload(ctx, c, h.siteUploadDir, h.service.systemPrefix+"/upload/site-logo/")
}

func (h *Handler) uploadLoadingAnimation(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	if err := h.service.ValidateLoadingAnimationUpload(ctx, p); err != nil {
		respond(c, nil, err)
		return
	}
	h.upload(ctx, c, h.loadingAnimationUploadDir, h.service.systemPrefix+"/upload/loading-animation/")
}

func (h *Handler) uploadDonationQRCode(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	if err := h.service.ValidateDonationQRCodeUpload(ctx, p); err != nil {
		respond(c, nil, err)
		return
	}
	h.upload(ctx, c, h.donationQRCodeUploadDir, h.service.systemPrefix+"/upload/donation-qr/")
}

func (h *Handler) noticeImage(ctx context.Context, c *hertzapp.RequestContext) {
	h.file(c, h.noticeUploadDir)
}

func (h *Handler) siteLogo(ctx context.Context, c *hertzapp.RequestContext) {
	h.file(c, h.siteUploadDir)
}

func (h *Handler) loadingAnimation(ctx context.Context, c *hertzapp.RequestContext) {
	h.file(c, h.loadingAnimationUploadDir)
}

func (h *Handler) donationQRCode(ctx context.Context, c *hertzapp.RequestContext) {
	h.file(c, h.donationQRCodeUploadDir)
}

func (h *Handler) upload(ctx context.Context, c *hertzapp.RequestContext, dir, urlPrefix string) {
	file, err := c.FormFile("file")
	if err != nil {
		respond(c, nil, apperrors.New(consts.StatusBadRequest, "file is required"))
		return
	}
	result, err := upload.SaveImage(file, dir, urlPrefix)
	respond(c, result, err)
}

func (h *Handler) file(c *hertzapp.RequestContext, dir string) {
	path, err := upload.ResolveImagePath(dir, c.Param("filename"))
	if err != nil {
		respond(c, nil, err)
		return
	}
	c.File(path)
}

func (h *Handler) principal(ctx context.Context, c *hertzapp.RequestContext) (Principal, bool) {
	p, err := h.service.Authenticate(ctx, bearer(c))
	if err != nil {
		response.FromError(c, err)
		return Principal{}, false
	}
	return p, true
}

func (h *Handler) principalID(ctx context.Context, c *hertzapp.RequestContext) (Principal, int64, bool) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return Principal{}, 0, false
	}
	id, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return Principal{}, 0, false
	}
	return p, id, true
}

func respond(c *hertzapp.RequestContext, data any, err error) {
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, data)
}

func bind(c *hertzapp.RequestContext, target any) bool {
	if err := jsonrequest.BindHertz(c, target, jsonrequest.DefaultMaxBodyBytes); err != nil {
		response.FromError(c, err)
		return false
	}
	return true
}

func bearer(c *hertzapp.RequestContext) string {
	value := string(c.Request.Header.Peek("Authorization"))
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func pathID(c *hertzapp.RequestContext) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		return 0, apperrors.New(consts.StatusBadRequest, "id is invalid")
	}
	return id, nil
}

func listParams(c *hertzapp.RequestContext) ListParams {
	params := ListParams{
		Keyword:  strings.TrimSpace(string(c.Query("keyword"))),
		Page:     queryInt(c, "page", 1),
		PageSize: queryInt(c, "pageSize", 20),
	}
	params.Status = queryFlag(c, "status")
	params.IsTop = queryFlag(c, "isTop")
	params.IsPopup = queryFlag(c, "isPopup")
	params.IsHomeTicker = queryFlag(c, "isHomeTicker")
	return params
}

// queryFlag reads a 0/1 filter. An unparsable value becomes 2, which matches no
// row, so a malformed flag narrows the result instead of silently ignoring it.
func queryFlag(c *hertzapp.RequestContext, key string) *int {
	raw := strings.TrimSpace(string(c.Query(key)))
	if raw == "" {
		return nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		value = 2
	}
	return &value
}

func queryInt(c *hertzapp.RequestContext, key string, fallback int) int {
	raw := strings.TrimSpace(string(c.Query(key)))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return value
}
