package payment

import (
	"context"
	"encoding/base64"
	"mime"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/jsonrequest"
	"bugpk-api/internal/pkg/response"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/route"
)

type Handler struct {
	service *Service
}

const (
	maxAlipayNotifyBodyBytes    = 64 * 1024
	maxAlipayNotifyFieldCount   = 64
	maxAlipayNotifyKeyBytes     = 64
	maxAlipayNotifyIDBytes      = 128
	maxAlipayNotifyValueBytes   = 8192
	maxHupijiaoNotifyBodyBytes  = 32 * 1024
	maxHupijiaoNotifyFieldCount = 32
	maxHupijiaoNotifyKeyBytes   = 64
	maxHupijiaoNotifyValueBytes = 4096
)

var (
	alipayNotifyAccountIDPattern = regexp.MustCompile(`^[0-9]{8,32}$`)
	alipayNotifyAmountPattern    = regexp.MustCompile(`^[0-9]+(?:\.[0-9]{1,4})?$`)
	alipayNotifyOrderNoPattern   = regexp.MustCompile(`^BP[0-9]{14}[A-F0-9]{12}$`)
	alipayNotifyStatusPattern    = regexp.MustCompile(`^[A-Z_]{3,32}$`)
	alipayNotifyTradeNoPattern   = regexp.MustCompile(`^[0-9]{16,64}$`)
)

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Register(group *route.RouterGroup) {
	group.GET("/alipay/config", h.alipayConfig)
	group.PUT("/alipay/config", h.saveAlipayConfig)
	group.POST("/alipay/config/validate", h.validateAlipayConfig)
	group.GET("/wechat/config", h.wechatConfig)
	group.PUT("/wechat/config", h.saveWechatConfig)
	group.POST("/wechat/config/validate", h.validateWechatConfig)
	group.GET("/hupijiao/config", h.hupijiaoConfig)
	group.PUT("/hupijiao/config", h.saveHupijiaoConfig)
	group.POST("/hupijiao/config/validate", h.validateHupijiaoConfig)
	group.GET("/methods", h.paymentMethods)
	group.PUT("/methods", h.savePaymentMethods)
	group.GET("/currency/config", h.paymentCurrency)
	group.PUT("/currency/config", h.savePaymentCurrency)
	group.GET("/currency", h.publicCurrency)
	group.GET("/trc20/config", h.trc20Config)
	group.PUT("/trc20/config", h.saveTrc20Config)
	group.GET("/bep20/config", h.bep20Config)
	group.PUT("/bep20/config", h.saveBep20Config)
	group.GET("/sponsor/config", h.sponsorRoleConfig)
	group.PUT("/sponsor/config", h.saveSponsorRoleConfig)
	group.GET("/recharge/presets", h.listPresets)
	group.POST("/recharge/presets", h.createPreset)
	group.PUT("/recharge/presets/:id", h.updatePreset)
	group.DELETE("/recharge/presets/:id", h.deletePreset)
	group.GET("/orders/list", h.adminOrders)
	group.GET("/asset-records", h.assetRecords)
	group.GET("/orders/my", h.userOrders)
	group.GET("/orders/pending", h.pendingOrder)
	group.POST("/orders/:orderNo/continue", h.continueOrder)
	group.GET("/orders/:orderNo", h.adminOrder)
	group.POST("/orders/:orderNo/sync", h.adminSyncOrder)
	group.DELETE("/orders/:orderNo", h.adminDeleteOrder)
	group.GET("/recharge/capabilities", h.capabilities)
	group.POST("/recharge/orders", h.createRechargeOrder)
	group.GET("/recharge/orders/:orderNo", h.userOrder)
	group.POST("/recharge/orders/:orderNo/sync", h.userSyncOrder)
	group.POST("/recharge/orders/:orderNo/cancel", h.userCancelOrder)
	group.DELETE("/recharge/orders/:orderNo", h.userDeleteOrder)
	group.POST("/recharge/orders/:orderNo/txid", h.submitTrc20TxID)
	group.POST("/package/global/:id/orders", h.createGlobalPackageOrder)
	group.POST("/package/interface/:id/orders", h.createInterfacePackageOrder)
	group.POST("/package/point/:id/orders", h.createPointPackageOrder)
	group.POST("/alipay/notify", h.alipayNotify)
	group.POST("/wechat/notify", h.wechatNotify)
	group.POST("/hupijiao/notify/:method", h.hupijiaoNotify)
	// 币安 Pay 回调：覆盖交易所「链下转账」这类链上永远看不到的到账。
	group.POST("/binancepay/notify", h.binancePayNotify)
}

func (h *Handler) alipayConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.AlipayConfig(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveAlipayConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveAlipayConfigRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveAlipayConfig(ctx, p, req))
}

func (h *Handler) validateAlipayConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveAlipayConfigRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.ValidateAlipayConfig(ctx, p, req))
}

func (h *Handler) wechatConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.WechatConfig(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveWechatConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveWechatConfigRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveWechatConfig(ctx, p, req))
}

func (h *Handler) validateWechatConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveWechatConfigRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.ValidateWechatConfig(ctx, p, req))
}

func (h *Handler) hupijiaoConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.HupijiaoConfig(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveHupijiaoConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveHupijiaoConfigRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveHupijiaoConfig(ctx, p, req))
}

func (h *Handler) validateHupijiaoConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveHupijiaoConfigRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.ValidateHupijiaoConfig(ctx, p, req))
}

func (h *Handler) paymentCurrency(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.PaymentCurrency(ctx, p)
	respond(c, result, err)
}

func (h *Handler) savePaymentCurrency(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SavePaymentCurrencyConfigRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SavePaymentCurrency(ctx, p, req))
}

// publicCurrency 公开只读货币配置：未登录页面也要按站点货币展示价格。
func (h *Handler) publicCurrency(ctx context.Context, c *hertzapp.RequestContext) {
	result, err := h.service.PublicCurrencyConfig(ctx)
	respond(c, result, err)
}

func (h *Handler) trc20Config(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.Trc20Config(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveTrc20Config(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveTrc20ConfigRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveTrc20Config(ctx, p, req))
}

func (h *Handler) submitTrc20TxID(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SubmitTrc20TxIDRequest
	if !bind(c, &req) {
		return
	}
	result, err := h.service.SubmitChainTxID(ctx, p, c.Param("orderNo"), req.TxID)
	respond(c, result, err)
}

func (h *Handler) bep20Config(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.Bep20Config(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveBep20Config(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveBep20ConfigRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveBep20Config(ctx, p, req))
}

// binancePayNotify 接收币安 Pay 推送。
//
// 回调必须满足三条才入账：验签通过、bizStatus 为成功、订单金额与币种一致。
// 其余一律不入账；非成功状态回 200 让币安停止重推，验签/系统错误回非 200 触发重推。
func (h *Handler) binancePayNotify(ctx context.Context, c *hertzapp.RequestContext) {
	if contentLength := c.Request.Header.ContentLength(); contentLength > maxAlipayNotifyBodyBytes {
		c.JSON(consts.StatusRequestEntityTooLarge, map[string]any{"code": "FAIL", "message": "invalid request"})
		return
	}
	// 必须用原始字节验签：重新序列化会改变字段顺序导致签名永远对不上。
	body, err := c.Body()
	if err != nil || len(body) == 0 || len(body) > maxAlipayNotifyBodyBytes {
		c.JSON(consts.StatusBadRequest, map[string]any{"code": "FAIL", "message": "invalid request"})
		return
	}
	// 每个签名头都必须恰好出现一次，多值意味着请求被中间件动过。
	headers := make(map[string]string, 3)
	for _, name := range []string{"BinancePay-Timestamp", "BinancePay-Nonce", "BinancePay-Signature"} {
		values := c.Request.Header.PeekAll(name)
		if len(values) != 1 {
			c.JSON(consts.StatusBadRequest, map[string]any{"code": "FAIL", "message": "invalid request"})
			return
		}
		headers[name] = string(values[0])
	}
	handled, err := h.service.HandleBinancePayNotify(ctx, body, headers)
	if err != nil {
		// 验签失败/系统异常：回非 200，让币安重推并留痕。
		c.JSON(consts.StatusBadRequest, map[string]any{"code": "FAIL", "message": "invalid request"})
		return
	}
	_ = handled
	c.JSON(consts.StatusOK, map[string]any{"code": "SUCCESS", "message": "success"})
}
func (h *Handler) sponsorRoleConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.SponsorRoleConfig(ctx, p)
	respond(c, result, err)
}

func (h *Handler) saveSponsorRoleConfig(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveSponsorRoleConfigRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SaveSponsorRoleConfig(ctx, p, req))
}

func (h *Handler) paymentMethods(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.PaymentMethods(ctx, p)
	respond(c, result, err)
}

func (h *Handler) savePaymentMethods(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SavePaymentMethodsRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SavePaymentMethods(ctx, p, req))
}

func (h *Handler) listPresets(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.ListPresets(ctx, p, listParams(c))
	respond(c, result, err)
}

func (h *Handler) createPreset(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req SaveRechargePresetRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SavePreset(ctx, p, 0, req))
}

func (h *Handler) updatePreset(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req SaveRechargePresetRequest
	if !bind(c, &req) {
		return
	}
	respond(c, true, h.service.SavePreset(ctx, p, id, req))
}

func (h *Handler) deletePreset(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	respond(c, true, h.service.DeletePreset(ctx, p, id))
}

func (h *Handler) adminOrders(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.AdminOrders(ctx, p, listParams(c))
	respond(c, result, err)
}

func (h *Handler) assetRecords(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.AssetRecords(ctx, p, AssetRecordListParams{
		BizType:  strings.TrimSpace(string(c.Query("bizType"))),
		DateFrom: strings.TrimSpace(string(c.Query("dateFrom"))),
		DateTo:   strings.TrimSpace(string(c.Query("dateTo"))),
		Keyword:  strings.TrimSpace(string(c.Query("keyword"))),
		Page:     queryInt(c, "page", 1),
		PageSize: queryInt(c, "pageSize", 20),
	})
	respond(c, result, err)
}

func (h *Handler) userOrders(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.UserOrders(ctx, p, listParams(c))
	respond(c, result, err)
}

func (h *Handler) pendingOrder(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	targetID, err := strconv.ParseInt(strings.TrimSpace(string(c.Query("targetId"))), 10, 64)
	if err != nil || targetID <= 0 {
		respond(c, nil, apperrors.New(consts.StatusBadRequest, "商品ID不合法"))
		return
	}
	result, err := h.service.PendingOrder(ctx, p, string(c.Query("orderType")), targetID)
	respond(c, result, err)
}

func (h *Handler) continueOrder(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.ContinueOrder(ctx, p, c.Param("orderNo"))
	respond(c, result, err)
}

func (h *Handler) adminOrder(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.AdminOrder(ctx, p, c.Param("orderNo"))
	respond(c, result, err)
}

func (h *Handler) adminSyncOrder(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.SyncAdminOrder(ctx, p, c.Param("orderNo"))
	respond(c, result, err)
}

func (h *Handler) capabilities(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.RechargeCapabilities(ctx, p)
	respond(c, result, err)
}

func (h *Handler) createRechargeOrder(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	var req CreateOrderRequest
	if !bindPaymentOrder(c, &req) {
		return
	}
	result, err := h.service.CreateRechargeOrder(ctx, p, req)
	respond(c, result, err)
}

func (h *Handler) userOrder(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.UserOrder(ctx, p, c.Param("orderNo"))
	respond(c, result, err)
}

func (h *Handler) userSyncOrder(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.SyncUserOrder(ctx, p, c.Param("orderNo"))
	respond(c, result, err)
}

func (h *Handler) userCancelOrder(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.CancelOrder(ctx, p, c.Param("orderNo"))
	respond(c, result, err)
}

func (h *Handler) userDeleteOrder(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.DeleteUserOrder(ctx, p, c.Param("orderNo"))
	respond(c, result, err)
}

func (h *Handler) adminDeleteOrder(ctx context.Context, c *hertzapp.RequestContext) {
	p, ok := h.principal(ctx, c)
	if !ok {
		return
	}
	result, err := h.service.DeleteAdminOrder(ctx, p, c.Param("orderNo"))
	respond(c, result, err)
}

func (h *Handler) createGlobalPackageOrder(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req CreateOrderRequest
	if !bindPaymentOrder(c, &req) {
		return
	}
	result, err := h.service.CreateGlobalPackageOrder(ctx, p, id, req)
	respond(c, result, err)
}

func (h *Handler) createInterfacePackageOrder(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req CreateOrderRequest
	if !bindPaymentOrder(c, &req) {
		return
	}
	result, err := h.service.CreateInterfacePackageOrder(ctx, p, id, req)
	respond(c, result, err)
}

func (h *Handler) createPointPackageOrder(ctx context.Context, c *hertzapp.RequestContext) {
	p, id, ok := h.principalID(ctx, c)
	if !ok {
		return
	}
	var req CreateOrderRequest
	if !bindPaymentOrder(c, &req) {
		return
	}
	result, err := h.service.CreatePointPackageOrder(ctx, p, id, req)
	respond(c, result, err)
}

func (h *Handler) alipayNotify(ctx context.Context, c *hertzapp.RequestContext) {
	values, err := notifyValues(c)
	if err != nil {
		c.String(consts.StatusBadRequest, "failure")
		return
	}
	ok, err := h.service.HandleAlipayNotify(ctx, values)
	if err != nil || !ok {
		c.String(consts.StatusBadRequest, "failure")
		return
	}
	c.String(consts.StatusOK, "success")
}

func (h *Handler) wechatNotify(ctx context.Context, c *hertzapp.RequestContext) {
	if contentLength := c.Request.Header.ContentLength(); contentLength > maxAlipayNotifyBodyBytes {
		c.JSON(consts.StatusRequestEntityTooLarge, map[string]any{"code": "FAIL", "message": "invalid request"})
		return
	}
	body, err := c.Body()
	if err != nil || len(body) == 0 || len(body) > maxAlipayNotifyBodyBytes {
		c.JSON(consts.StatusBadRequest, map[string]any{"code": "FAIL", "message": "invalid request"})
		return
	}
	headers := make(map[string]string, 4)
	for _, name := range []string{"Wechatpay-Serial", "Wechatpay-Nonce", "Wechatpay-Signature", "Wechatpay-Timestamp"} {
		values := c.Request.Header.PeekAll(name)
		if len(values) != 1 {
			c.JSON(consts.StatusBadRequest, map[string]any{"code": "FAIL", "message": "invalid request"})
			return
		}
		headers[name] = string(values[0])
	}

	ok, err := h.service.HandleWechatNotify(ctx, body, headers)
	if err != nil || !ok {
		c.JSON(consts.StatusBadRequest, map[string]any{"code": "FAIL", "message": "invalid request"})
		return
	}
	c.JSON(consts.StatusOK, map[string]any{"code": "SUCCESS", "message": "成功"})
}

func (h *Handler) hupijiaoNotify(ctx context.Context, c *hertzapp.RequestContext) {
	channel := ChannelHupijiaoAlipay
	switch strings.ToUpper(strings.TrimSpace(c.Param("method"))) {
	case "ALIPAY":
	case "WECHAT":
		channel = ChannelHupijiaoWechat
	default:
		c.String(consts.StatusBadRequest, "failure")
		return
	}
	values, err := hupijiaoNotifyValues(c)
	if err != nil {
		c.String(consts.StatusBadRequest, "failure")
		return
	}
	ok, err := h.service.HandleHupijiaoNotify(ctx, channel, values)
	if err != nil || !ok {
		c.String(consts.StatusBadRequest, "failure")
		return
	}
	c.String(consts.StatusOK, "success")
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

func bindPaymentOrder(c *hertzapp.RequestContext, target *CreateOrderRequest) bool {
	if !bind(c, target) {
		return false
	}
	// ClientIP has json:"-": caller JSON cannot override the trusted proxy resolver.
	target.ClientIP = c.ClientIP()
	return true
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
		return 0, apperrors.New(consts.StatusBadRequest, "ID不合法")
	}
	return id, nil
}

func listParams(c *hertzapp.RequestContext) ListParams {
	return ListParams{
		Keyword:   strings.TrimSpace(string(c.Query("keyword"))),
		Page:      queryInt(c, "page", 1),
		PageSize:  queryInt(c, "pageSize", 20),
		Status:    strings.TrimSpace(string(c.Query("status"))),
		OrderType: strings.TrimSpace(string(c.Query("orderType"))),
		SortBy:    strings.TrimSpace(string(c.Query("sortBy"))),
		SortOrder: strings.TrimSpace(string(c.Query("sortOrder"))),
		PaidFrom:  strings.TrimSpace(string(c.Query("paidFrom"))),
		PaidTo:    strings.TrimSpace(string(c.Query("paidTo"))),
		MinAmount: strings.TrimSpace(string(c.Query("minAmount"))),
		MaxAmount: strings.TrimSpace(string(c.Query("maxAmount"))),
	}
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

func notifyValues(c *hertzapp.RequestContext) (url.Values, error) {
	if err := validateNotifyContentType(string(c.Request.Header.ContentType())); err != nil {
		return nil, err
	}
	if contentLength := c.Request.Header.ContentLength(); contentLength > maxAlipayNotifyBodyBytes {
		return nil, apperrors.New(consts.StatusRequestEntityTooLarge, "支付宝通知请求体大小不合法")
	}
	body, err := c.Body()
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || len(body) > maxAlipayNotifyBodyBytes {
		return nil, apperrors.New(consts.StatusRequestEntityTooLarge, "支付宝通知请求体大小不合法")
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	if err := validateNotifyValues(values); err != nil {
		return nil, err
	}
	if err := validateNotifyEnvelope(values); err != nil {
		return nil, err
	}
	return values, nil
}

func validateNotifyContentType(raw string) error {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(raw))
	if err != nil || !strings.EqualFold(mediaType, "application/x-www-form-urlencoded") {
		return apperrors.New(consts.StatusUnsupportedMediaType, "支付宝通知 Content-Type 不合法")
	}
	return nil
}

func validateNotifyValues(values url.Values) error {
	if len(values) == 0 || len(values) > maxAlipayNotifyFieldCount {
		return apperrors.New(consts.StatusBadRequest, "支付宝通知字段数量不合法")
	}
	for key, items := range values {
		if strings.TrimSpace(key) == "" || len(key) > maxAlipayNotifyKeyBytes || len(items) != 1 || len(items[0]) > maxAlipayNotifyValueBytes {
			return apperrors.New(consts.StatusBadRequest, "支付宝通知参数不合法")
		}
	}
	return nil
}

func validateNotifyEnvelope(values url.Values) error {
	for _, key := range []string{"sign", "sign_type", "notify_id", "app_id", "seller_id", "out_trade_no", "total_amount", "trade_status"} {
		if strings.TrimSpace(values.Get(key)) == "" {
			return apperrors.New(consts.StatusBadRequest, "支付宝通知缺少必要参数")
		}
	}
	if len(values.Get("notify_id")) > maxAlipayNotifyIDBytes {
		return apperrors.New(consts.StatusBadRequest, "支付宝通知标识长度不合法")
	}
	if !strings.EqualFold(strings.TrimSpace(values.Get("sign_type")), "RSA2") {
		return apperrors.New(consts.StatusBadRequest, "支付宝通知签名类型不合法")
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(values.Get("sign")))
	if err != nil || len(signature) < 256 || len(signature) > 1024 {
		return apperrors.New(consts.StatusBadRequest, "支付宝通知签名格式不合法")
	}
	if !alipayNotifyAccountIDPattern.MatchString(strings.TrimSpace(values.Get("app_id"))) ||
		!alipayNotifyAccountIDPattern.MatchString(strings.TrimSpace(values.Get("seller_id"))) {
		return apperrors.New(consts.StatusBadRequest, "支付宝通知账户参数不合法")
	}
	if !alipayNotifyOrderNoPattern.MatchString(strings.TrimSpace(values.Get("out_trade_no"))) {
		return apperrors.New(consts.StatusBadRequest, "支付宝通知订单号不合法")
	}
	amount := strings.TrimSpace(values.Get("total_amount"))
	if !alipayNotifyAmountPattern.MatchString(amount) {
		return apperrors.New(consts.StatusBadRequest, "支付宝通知金额格式不合法")
	}
	if _, err := normalizeMoney(amount); err != nil {
		return err
	}
	status := strings.TrimSpace(values.Get("trade_status"))
	if !alipayNotifyStatusPattern.MatchString(status) {
		return apperrors.New(consts.StatusBadRequest, "支付宝通知交易状态不合法")
	}
	if isAlipayPaidStatus(status) && !alipayNotifyTradeNoPattern.MatchString(strings.TrimSpace(values.Get("trade_no"))) {
		return apperrors.New(consts.StatusBadRequest, "支付宝通知交易号不合法")
	}
	return nil
}

func hupijiaoNotifyValues(c *hertzapp.RequestContext) (map[string]string, error) {
	if err := validateNotifyContentType(string(c.Request.Header.ContentType())); err != nil {
		return nil, err
	}
	if contentLength := c.Request.Header.ContentLength(); contentLength > maxHupijiaoNotifyBodyBytes {
		return nil, apperrors.New(consts.StatusRequestEntityTooLarge, "虎皮椒通知请求体大小不合法")
	}
	body, err := c.Body()
	if err != nil {
		return nil, err
	}
	if len(body) == 0 || len(body) > maxHupijiaoNotifyBodyBytes {
		return nil, apperrors.New(consts.StatusRequestEntityTooLarge, "虎皮椒通知请求体大小不合法")
	}
	parsed, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	if len(parsed) == 0 || len(parsed) > maxHupijiaoNotifyFieldCount {
		return nil, apperrors.New(consts.StatusBadRequest, "虎皮椒通知字段数量不合法")
	}
	values := make(map[string]string, len(parsed))
	for key, items := range parsed {
		if strings.TrimSpace(key) == "" || len(key) > maxHupijiaoNotifyKeyBytes || len(items) != 1 || len(items[0]) > maxHupijiaoNotifyValueBytes {
			return nil, apperrors.New(consts.StatusBadRequest, "虎皮椒通知参数不合法")
		}
		values[key] = items[0]
	}
	for _, key := range []string{"appid", "trade_order_id", "total_fee", "status", "hash"} {
		if strings.TrimSpace(values[key]) == "" {
			return nil, apperrors.New(consts.StatusBadRequest, "虎皮椒通知缺少必要参数")
		}
	}
	if !alipayNotifyOrderNoPattern.MatchString(strings.TrimSpace(values["trade_order_id"])) || !alipayNotifyAmountPattern.MatchString(strings.TrimSpace(values["total_fee"])) {
		return nil, apperrors.New(consts.StatusBadRequest, "虎皮椒通知订单或金额不合法")
	}
	if _, err := normalizeMoney(values["total_fee"]); err != nil {
		return nil, err
	}
	status := strings.ToUpper(strings.TrimSpace(values["status"]))
	if status != "OD" && status != "WP" && status != "CD" {
		return nil, apperrors.New(consts.StatusBadRequest, "虎皮椒通知状态不合法")
	}
	if len(strings.TrimSpace(values["hash"])) != 32 {
		return nil, apperrors.New(consts.StatusBadRequest, "虎皮椒通知签名格式不合法")
	}
	return values, nil
}
