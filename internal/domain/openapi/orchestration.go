package openapi

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/domain/gateway"
	"bugpk-api/internal/domain/payment"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/domain/redeem"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// 开放 API 端点的编排层：把 OpenPrincipal 适配为归属账号的 Principal 后
// 调用各域既有 service——各域校验天然生效，不新增任何页面端没有的能力。
// 本层不开事务、不碰他域仓储。

// idempotencyTTL 是开放 API 下单幂等窗口（设计节五：Redis 24h）。
const idempotencyTTL = 24 * time.Hour

// accountPrincipal 把开放 Key 身份适配为某域 Principal（用户端）。
func (s *Service) accountPrincipal(ctx context.Context, principal OpenPrincipal) (Principal, error) {
	if s.accounts == nil {
		return Principal{}, apperrors.New(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试")
	}
	snapshot, err := s.accounts.AccountSnapshot(ctx, principal.UserID)
	if err != nil {
		return Principal{}, err
	}
	return Principal{ID: snapshot.UserID, Username: snapshot.Username, Roles: snapshot.Roles}, nil
}

// AccountInfo 返回本账号基础信息聚合（只读，不写审计）。
func (s *Service) AccountInfo(ctx context.Context, principal OpenPrincipal) (AccountInfo, error) {
	if s.accounts == nil || s.packages == nil {
		return AccountInfo{}, apperrors.New(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试")
	}
	snapshot, err := s.accounts.AccountSnapshot(ctx, principal.UserID)
	if err != nil {
		return AccountInfo{}, err
	}
	details, err := s.packages.UserPackageDetails(ctx, principal.UserID)
	if err != nil {
		return AccountInfo{}, err
	}
	var counts PackageCount
	for _, item := range details {
		if item.Status != 1 {
			continue
		}
		if item.Kind == "global" {
			counts.Global++
		} else {
			counts.Interface++
		}
	}
	keyStatus := "none"
	if key, err := s.repo.KeyByID(ctx, principal.KeyID, 0); err == nil {
		keyStatus = keyStatusText(key, time.Now())
	} else if !errors.Is(err, errNotFound) {
		return AccountInfo{}, err
	}
	return AccountInfo{
		UserID:         strconv.FormatInt(snapshot.UserID, 10),
		Username:       snapshot.Username,
		Status:         snapshot.Status,
		Balance:        snapshot.Balance,
		Points:         snapshot.Points,
		OpenAPIEnabled: snapshot.OpenAPIEnabled && !snapshot.OpenAPIAdminDisabled,
		KeyStatus:      keyStatus,
		PackageCount:   counts,
	}, nil
}

// AccountPackages 名下有效套餐权益分页（status=1，与「我的套餐」同源同语义）。
func (s *Service) AccountPackages(ctx context.Context, principal OpenPrincipal, page, pageSize int) (AccountPackagePage, error) {
	if s.packages == nil {
		return AccountPackagePage{}, apperrors.New(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试")
	}
	details, err := s.packages.UserPackageDetails(ctx, principal.UserID)
	if err != nil {
		return AccountPackagePage{}, err
	}
	active := make([]AccountPackage, 0, len(details))
	for _, item := range details {
		if item.Status != 1 {
			continue
		}
		active = append(active, AccountPackage{
			Kind:        item.Kind,
			PackageName: item.PackageName,
			SpecName:    item.SpecName,
			InterfaceID: interfaceIDText(item.InterfaceID),
			DailyLimit:  item.DailyLimit,
			QPSLimit:    item.QPSLimit,
			StartAt:     item.StartAt,
			EndAt:       item.EndAt,
			DayUsed:     item.DayUsed,
			Status:      item.Status,
		})
	}
	total := int64(len(active))
	page, size := normalizePage(page, pageSize)
	start := (page - 1) * size
	if start >= len(active) {
		return AccountPackagePage{Items: []AccountPackage{}, Total: total}, nil
	}
	end := start + size
	if end > len(active) {
		end = len(active)
	}
	return AccountPackagePage{Items: active[start:end], Total: total}, nil
}

// CreateAPIKey 创建数据面接口 Key（复用 gateway 域全部校验；owner 强制本人）。
// 支持 Idempotency-Key：仅显式提供客户端键时启用，且不同客户端键相互独立。
func (s *Service) CreateAPIKey(ctx context.Context, principal OpenPrincipal, req CreateAPIKeyRequest, idempotencyKey string) (gateway.CreateKeyResult, error) {
	if s.gateway == nil {
		return gateway.CreateKeyResult{}, apperrors.New(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试")
	}
	account, err := s.accountPrincipal(ctx, principal)
	if err != nil {
		return gateway.CreateKeyResult{}, err
	}
	normalized, err := json.Marshal(req)
	if err != nil {
		return gateway.CreateKeyResult{}, apperrors.Wrap(consts.StatusBadRequest, "请求体不合法", err)
	}
	scope := idempotencyScope(principal.KeyID, "POST", "/open-api/api-keys", normalized, idempotencyKey)
	if hit, ok, err := s.idempotencyGet(ctx, scope); err != nil {
		return gateway.CreateKeyResult{}, err
	} else if ok {
		var cached gateway.CreateKeyResult
		if err := json.Unmarshal(hit, &cached); err == nil {
			return cached, nil
		}
		// 缓存形状异常视为未命中（不阻断业务）。
	}
	result, err := s.gateway.CreateKey(ctx, rbac.Principal{
		ID: account.ID, Username: account.Username, Roles: account.Roles, TokenVersion: account.TokenVersion,
	}, gateway.SaveKeyRequest{
		Name:         strings.TrimSpace(req.Name),
		KeyType:      strings.TrimSpace(req.KeyType),
		ExpiresAt:    req.ExpiresAt,
		InterfaceIDs: req.InterfaceIDs,
		IPWhitelist:  req.IPWhitelist,
	})
	if err != nil {
		return gateway.CreateKeyResult{}, err
	}
	if payload, err := json.Marshal(result); err == nil {
		s.idempotencySet(ctx, scope, payload)
	}
	s.record(ctx, Principal{ID: account.ID, Username: account.Username, Roles: account.Roles},
		"create", "经开放 API 创建接口 Key #"+result.ID+"（Key #"+strconv.FormatInt(principal.KeyID, 10)+"）",
		"api_key", result.ID, "")
	return result, nil
}

// APIKeyDetail 查询单把接口 Key 的掩码状态（owner 强制本人）。
func (s *Service) APIKeyDetail(ctx context.Context, principal OpenPrincipal, id int64) (gateway.APIKeyItem, error) {
	if s.gateway == nil {
		return gateway.APIKeyItem{}, apperrors.New(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试")
	}
	if id <= 0 {
		return gateway.APIKeyItem{}, apperrors.New(consts.StatusBadRequest, "ID不合法")
	}
	account, err := s.accountPrincipal(ctx, principal)
	if err != nil {
		return gateway.APIKeyItem{}, err
	}
	return s.gateway.KeyDetail(ctx, rbac.Principal{
		ID: account.ID, Username: account.Username, Roles: account.Roles, TokenVersion: account.TokenVersion,
	}, id)
}

// CreateOrder 创建支付订单（下单≠开通，不代付）。三类套餐按 Product 分派。
func (s *Service) CreateOrder(ctx context.Context, principal OpenPrincipal, req CreateOrderRequest) (payment.PaymentOrder, error) {
	if s.payments == nil {
		return payment.PaymentOrder{}, apperrors.New(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试")
	}
	account, err := s.accountPrincipal(ctx, principal)
	if err != nil {
		return payment.PaymentOrder{}, err
	}
	orderReq := payment.CreateOrderRequest{
		Product:    strings.TrimSpace(req.Product),
		PayChannel: strings.TrimSpace(req.PayChannel),
		TradeType:  strings.TrimSpace(req.TradeType),
		OpenID:     strings.TrimSpace(req.OpenID),
		ClientIP:   principal.ClientIP,
	}
	payPrincipal := payment.Principal{
		ID: account.ID, Username: account.Username, Roles: account.Roles, TokenVersion: account.TokenVersion,
	}
	switch strings.ToUpper(strings.TrimSpace(req.Product)) {
	case "GLOBAL":
		if req.PackageID <= 0 {
			return payment.PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "缺少全站套餐编号")
		}
		return s.payments.CreateGlobalPackageOrder(ctx, payPrincipal, req.PackageID, orderReq)
	case "INTERFACE":
		if req.SpecID <= 0 {
			return payment.PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "缺少接口套餐规格编号")
		}
		return s.payments.CreateInterfacePackageOrder(ctx, payPrincipal, req.SpecID, orderReq)
	case "POINT":
		if req.PackageID <= 0 {
			return payment.PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "缺少点数套餐编号")
		}
		return s.payments.CreatePointPackageOrder(ctx, payPrincipal, req.PackageID, orderReq)
	default:
		return payment.PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "product 需为 GLOBAL / INTERFACE / POINT")
	}
}

// RedeemCDK 兑换 CDK（source=2 豁免 IP 频控：集中出口 NAT 场景）。
func (s *Service) RedeemCDK(ctx context.Context, principal OpenPrincipal, code string) (redeem.RedeemResult, error) {
	if s.redeem == nil {
		return redeem.RedeemResult{}, apperrors.New(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试")
	}
	account, err := s.accountPrincipal(ctx, principal)
	if err != nil {
		return redeem.RedeemResult{}, err
	}
	return s.redeem.Redeem(ctx, redeem.Principal{
		ID: account.ID, Username: account.Username, Roles: account.Roles, TokenVersion: account.TokenVersion,
	}, strings.TrimSpace(code), principal.ClientIP, redeem.SourceOpenAPI)
}

// GenerateCDKs 批量生成 CDK。双层校验（S-8）：
//  1. 本层显式 Require(redeem:code:generate)（第二门实时 RBAC，作用于归属账号角色）；
//  2. redeem 域生成 service 内部兜底 Require（同一权限码），防旁路。
//
// 中间件已按 is_admin_scope + scope 清单拒绝普通 Key（403 scope_denied）。
// resolveRedeemTypeRef 把请求体里的类型引用换成数据库自增 ID。
//
// typeId 优先（显式、零解析成本）；typeCode 供外部插件配置使用（标识
// 比自增 ID 稳）。两者都缺或都查不到是调用方配置错误，返回 4xx 稳定
// 码而不是把领域错误直接抛给外部——外部拿到的是可判定的机器码。
func (s *Service) resolveRedeemTypeRef(ctx context.Context, req GenerateCodesRequest) (int64, error) {
	if req.TypeID > 0 {
		return req.TypeID, nil
	}
	code := strings.TrimSpace(req.TypeCode)
	if code == "" {
		return 0, openError(CodeInvalidRequest, consts.StatusBadRequest, "需指定 typeId 或 typeCode")
	}
	typeID, err := s.redeem.TypeIDByCode(ctx, code)
	if errors.Is(err, redeem.ErrTypeNotFound) {
		return 0, openError(CodeTypeNotFound, consts.StatusBadRequest, "兑换码类型不存在")
	}
	if err != nil {
		return 0, err
	}
	return typeID, nil
}

func (s *Service) GenerateCDKs(ctx context.Context, principal OpenPrincipal, req GenerateCodesRequest) (redeem.GenerateResult, error) {
	if s.redeem == nil {
		return redeem.GenerateResult{}, apperrors.New(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试")
	}
	if !principal.IsAdminScope {
		return redeem.GenerateResult{}, openError(CodeScopeDenied, consts.StatusForbidden, "该开放 Key 未授权此端点")
	}
	account, err := s.accountPrincipal(ctx, principal)
	if err != nil {
		return redeem.GenerateResult{}, err
	}
	if err := s.authz.Require(ctx, rbac.Principal{
		ID: account.ID, Username: account.Username, Roles: account.Roles, TokenVersion: account.TokenVersion,
	}, permRedeemGenerate); err != nil {
		return redeem.GenerateResult{}, err
	}
	redeemPrincipal := redeem.Principal{
		ID: account.ID, Username: account.Username, Roles: account.Roles, TokenVersion: account.TokenVersion,
	}
	typeID, err := s.resolveRedeemTypeRef(ctx, req)
	if err != nil {
		return redeem.GenerateResult{}, err
	}
	return s.redeem.GenerateCodesForOpenAPI(ctx, redeemPrincipal, typeID, redeem.GenerateCodesRequest{
		Count:        req.Count,
		PerUserLimit: req.PerUserLimit,
		PerIPLimit:   req.PerIPLimit,
		CampaignRef:  strings.TrimSpace(req.CampaignRef),
	}, redeem.AuditMeta{
		Method: "POST", Path: "/open-api/redeem-codes", ClientIP: principal.ClientIP,
		UserAgent: "open-api-key/" + strconv.FormatInt(principal.KeyID, 10),
	}, principal.KeyID)
}

// ---------------------------------------------------------------- 幂等（S-5）

// idempotencyScope 同时绑定开放 Key、端点、请求体和客户端请求键。
// 没有客户端键时返回空作用域，读写均跳过缓存。
func idempotencyScope(keyID int64, method, path string, body []byte, clientKey string) string {
	clientKey = strings.TrimSpace(clientKey)
	if clientKey == "" {
		return ""
	}
	return "openapi:idem:" + hashOf(strconv.FormatInt(keyID, 10), strings.ToUpper(method), path, string(body), clientKey)
}

// idempotencyGet 命中返回缓存响应体（未启用幂等或 Redis 故障时不阻断业务：
// 幂等是体验优化，不是安全边界）。
func (s *Service) idempotencyGet(ctx context.Context, scope string) ([]byte, bool, error) {
	if s.rdb == nil || scope == "" {
		return nil, false, nil
	}
	value, err := s.rdb.Get(ctx, scope).Result()
	if err != nil {
		return nil, false, nil
	}
	if !strings.HasPrefix(value, idempotencyPrefix) {
		return nil, false, nil
	}
	return []byte(strings.TrimPrefix(value, idempotencyPrefix)), true, nil
}

func (s *Service) idempotencySet(ctx context.Context, scope string, payload []byte) {
	if s.rdb == nil || scope == "" {
		return
	}
	_ = s.rdb.Set(ctx, scope, idempotencyPrefix+string(payload), idempotencyTTL).Err()
}

const idempotencyPrefix = "json:"

// ---------------------------------------------------------------- 助手

func keyStatusText(row keyRow, now time.Time) string {
	if row.Status != KeyStatusActive {
		return "revoked"
	}
	if !row.ExpiresAt.After(now) {
		return "expired"
	}
	return "active"
}

func interfaceIDText(id int64) string {
	if id <= 0 {
		return ""
	}
	return strconv.FormatInt(id, 10)
}
