package interfaceapi

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"

	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/jsonrequest"
	"bugpk-api/internal/pkg/requestid"
	"bugpk-api/internal/pkg/response"
	"bugpk-api/internal/pkg/upload"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
)

type Handler struct {
	service   *Service
	uploadDir string
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service, uploadDir: filepath.Join("uploads", "interface-avatar")}
}

func (h *Handler) RegisterInterface(group *route.RouterGroup) {
	group.GET("/list", h.list)
	group.GET("/response-advertisements", h.responseAdvertisementOptions)
	group.GET("/global-delivery-policy", h.globalDeliveryPolicy)
	group.PUT("/global-delivery-policy", h.saveGlobalDeliveryPolicy)
	group.GET("/field-mapping-presets", h.fieldMappingPresets)
	group.GET("/unified-format-schema", h.unifiedFormatSchema)
	group.GET("/:id/polling", h.polling)
	group.PUT("/:id/polling", h.savePolling)
	group.POST("/:id/upstreams/check", h.checkUpstreams)
	group.POST("/proxy-pool/inspect", h.inspectProxyPool)
	group.POST("/proxy-pool/test", h.testProxyPool)
	group.GET("/:id/doc", h.doc)
	group.PUT("/:id/doc", h.saveDoc)
	group.GET("/:id/billing-rules", h.billingRules)
	group.PUT("/:id/billing-rules", h.saveBillingRules)
	group.GET("/:id/request-validation", h.requestValidationPolicy)
	group.PUT("/:id/request-validation", h.saveRequestValidationPolicy)
	group.GET("/:id/delivery-policy", h.deliveryPolicy)
	group.PUT("/:id/delivery-policy", h.saveDeliveryPolicy)
	group.GET("/:id/quota", h.interfaceQuota)
	group.PUT("/:id/quota", h.saveInterfaceQuota)
	group.GET("/:id/metering", h.interfaceMetering)
	group.PUT("/:id/metering", h.saveInterfaceMetering)
	group.GET("/:id/degrade", h.interfaceDegrade)
	group.PUT("/:id/degrade", h.saveInterfaceDegrade)
	group.GET("/:id/invoke-keys", h.invokeKeys)
	group.POST("/:id/invoke", h.invoke)
	group.PUT("/batch/status", h.batchStatus)
	group.DELETE("/batch", h.batchDelete)
	group.POST("", h.create)
	group.PUT("/:id", h.update)
	group.DELETE("/:id", h.delete)
}

func (h *Handler) responseAdvertisementOptions(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.ResponseAdvertisementOptions(ctx, principal)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

// fieldMappingPresets 返回基于短视频统一 JSON 格式的字段映射预设选项。
// GET /system-api/interface/field-mapping-presets
// 无需认证，用于前端字段映射表单的下拉选择/预设推荐。
func (h *Handler) fieldMappingPresets(ctx context.Context, c *hertzapp.RequestContext) {
	response.OK(c, FieldMappingPresets())
}

// unifiedFormatSchema 返回短视频统一 JSON 格式的完整 schema 描述和 JSON 路径语法说明。
// GET /system-api/interface/unified-format-schema
// 无需认证，用于前端渲染字段映射表单时的格式参考和输入校验提示。
func (h *Handler) unifiedFormatSchema(ctx context.Context, c *hertzapp.RequestContext) {
	response.OK(c, UnifiedFormatSchema())
}

func (h *Handler) deliveryPolicy(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.DeliveryPolicy(ctx, principal, id)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) globalDeliveryPolicy(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.GlobalDeliveryPolicy(ctx, principal)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) interfaceQuota(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.InterfaceQuota(ctx, principal, id)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) saveInterfaceQuota(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	var req InterfaceQuotaPolicy
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.SaveInterfaceQuota(ctx, principal, id, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) interfaceMetering(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.InterfaceMetering(ctx, principal, id)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) saveInterfaceMetering(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	var req InterfaceMeteringPolicy
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.SaveInterfaceMetering(ctx, principal, id, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) interfaceDegrade(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.InterfaceDegrade(ctx, principal, id)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) saveInterfaceDegrade(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	var req InterfaceDegradePolicy
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.SaveInterfaceDegrade(ctx, principal, id, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) saveDeliveryPolicy(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	var req SaveDeliveryPolicyRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.SaveDeliveryPolicy(ctx, principal, id, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) saveGlobalDeliveryPolicy(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req SaveDeliveryPolicyRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.SaveGlobalDeliveryPolicy(ctx, principal, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) RegisterMarket(group *route.RouterGroup) {
	group.GET("/apis", h.marketList)
	group.GET("/apis/:id/test-key", h.marketTestKey)
	group.GET("/apis/:id", h.marketDetail)
}

func (h *Handler) RegisterUpload(group *route.RouterGroup) {
	group.POST("/interface-avatar", h.uploadAvatar)
	group.GET("/interface-avatar/:filename", h.avatarFile)
}

func (h *Handler) list(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.List(ctx, principal, listParams(c))
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) create(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req SaveInterfaceRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.Create(ctx, principal, req)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) update(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	id, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req SaveInterfaceRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.Update(ctx, principal, id, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) delete(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	id, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.Delete(ctx, principal, id); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) batchStatus(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req BatchInterfaceStatusRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.BatchUpdateStatus(ctx, principal, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) batchDelete(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req BatchInterfaceIDsRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.BatchDelete(ctx, principal, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) polling(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.Polling(ctx, principal, id)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) savePolling(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	var req SavePollingConfig
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.SavePolling(ctx, principal, id, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) checkUpstreams(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.CheckUpstreams(ctx, principal, id)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) inspectProxyPool(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req ProxyPoolInspectRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.InspectProxyPool(ctx, principal, req)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) testProxyPool(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	var req ProxyPoolTestRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.TestProxyPool(ctx, principal, req)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) doc(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.Doc(ctx, principal, id)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) saveDoc(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	var req DocConfig
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.SaveDoc(ctx, principal, id, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) billingRules(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.BillingRules(ctx, principal, id)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) saveBillingRules(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	var req SaveBillingRulesRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.SaveBillingRules(ctx, principal, id, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) requestValidationPolicy(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.RequestValidationPolicy(ctx, principal, id)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) saveRequestValidationPolicy(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	var req SaveRequestValidationPolicyRequest
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.SaveRequestValidationPolicy(ctx, principal, id, req); err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, true)
}

func (h *Handler) invoke(ctx context.Context, c *hertzapp.RequestContext) {
	ctx = requestid.Ensure(ctx)
	requestID := requestid.FromContext(ctx)
	c.Header(requestid.Header, requestID)
	defer func() {
		c.Header(requestid.Header, requestID)
		c.Response.SetBody(requestid.Decorate(c.Response.Body(), string(c.Response.Header.ContentType()), requestID))
	}()
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	var req InvokePayload
	if err := bindJSON(c, &req); err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.Invoke(ctx, principal, id, req, c.ClientIP())
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) marketList(ctx context.Context, c *hertzapp.RequestContext) {
	params := listParams(c)
	if raw := strings.TrimSpace(string(c.Query("featuredOnly"))); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			value = 2
		}
		params.FeaturedOnly = &value
	}
	params.PriceType = string(c.Query("priceType"))
	result, err := h.service.MarketList(ctx, params)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) invokeKeys(ctx context.Context, c *hertzapp.RequestContext) {
	principal, id, ok := h.principalAndID(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.InvokeKeys(ctx, principal, id, string(c.Query("keyword")))
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) marketDetail(ctx context.Context, c *hertzapp.RequestContext) {
	id, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.MarketDetail(ctx, id)
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) marketTestKey(ctx context.Context, c *hertzapp.RequestContext) {
	id, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	result, err := h.service.MarketTestKey(ctx, id, bearerToken(c))
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) uploadAvatar(ctx context.Context, c *hertzapp.RequestContext) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return
	}
	if err := h.service.ValidateUploadPermission(ctx, principal); err != nil {
		response.FromError(c, err)
		return
	}
	file, err := c.FormFile("file")
	if err != nil {
		response.FromError(c, apperrors.New(consts.StatusBadRequest, "请选择上传文件"))
		return
	}
	result, err := upload.SaveImage(file, h.uploadDir, h.service.systemPrefix+"/upload/interface-avatar/")
	if err != nil {
		response.FromError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *Handler) avatarFile(ctx context.Context, c *hertzapp.RequestContext) {
	path, err := upload.ResolveImagePath(h.uploadDir, c.Param("filename"))
	if err != nil {
		response.FromError(c, err)
		return
	}
	c.File(path)
}

func (h *Handler) principalAndID(ctx context.Context, c *hertzapp.RequestContext) (Principal, int64, bool) {
	principal, err := h.principal(ctx, c)
	if err != nil {
		response.FromError(c, err)
		return Principal{}, 0, false
	}
	id, err := pathID(c)
	if err != nil {
		response.FromError(c, err)
		return Principal{}, 0, false
	}
	return principal, id, true
}

func (h *Handler) principal(ctx context.Context, c *hertzapp.RequestContext) (Principal, error) {
	return h.service.Authenticate(ctx, bearerToken(c))
}

func listParams(c *hertzapp.RequestContext) ListParams {
	params := ListParams{
		Keyword:       string(c.Query("keyword")),
		Page:          queryInt(c, "page", 1),
		PageSize:      queryInt(c, "pageSize", 20),
		RequestMethod: string(c.Query("requestMethod")),
	}
	if raw := strings.TrimSpace(string(c.Query("status"))); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			value = 2
		}
		params.Status = &value
	}
	if raw := strings.TrimSpace(string(c.Query("isFeatured"))); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			value = 2
		}
		params.IsFeatured = &value
	}
	return params
}

func bindJSON(c *hertzapp.RequestContext, target any) error {
	return jsonrequest.BindHertz(c, target, jsonrequest.DefaultMaxBodyBytes)
}

func bearerToken(c *hertzapp.RequestContext) string {
	value := string(c.Request.Header.Peek("Authorization"))
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func pathID(c *hertzapp.RequestContext) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		return 0, apperrors.New(consts.StatusBadRequest, "ID is invalid")
	}
	return id, nil
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
