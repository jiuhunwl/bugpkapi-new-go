package openapi

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"bugpk-api/internal/domain/observability"
	"bugpk-api/internal/domain/rbac"
	userdomain "bugpk-api/internal/domain/user"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/pagination"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

// 管理面权限码（与 000103 菜单种子逐字一致）。自助路径用 openapi:self:read，
// 跨账号路径用 openapi:key:*；admin 角色在 rbac 层直接放行。
const (
	permSelfRead       = "openapi:self:read"
	permKeyList        = "openapi:key:list"
	permKeyCreate      = "openapi:key:create"
	permKeyRevoke      = "openapi:key:revoke"
	permUserDisable    = "openapi:user:disable"
	permConfigRead     = "system:open-api:config"
	permConfigUpdate   = "system:open-api:config:update"
	permRedeemGenerate = "redeem:code:generate"
)

// endpointPermissions 是端点 → 页面权限映射：
//   - 默认 scope 派生（Granted）依据此表；
//   - 表内无映射的端点视为「登录即可用」的页面能力（恒可勾选）；
//   - 勾选超出角色权限的端点并不构成授权：调用时第二门（实时 RBAC）拒绝。
var endpointPermissions = map[string][]string{
	EndpointAPIKeyWrite:        {"key:create"},
	EndpointAPIKeyRead:         {"key:list"},
	EndpointRedeemCodeGenerate: {permRedeemGenerate},
}

// 开放 API 稳定错误码（消费者按 code 分支，不解析文案）。
const (
	CodeDisabled            = "openapi.disabled"
	CodeAccountDisabled     = "openapi.account_disabled"
	CodeAdminDisabled       = "openapi.admin_disabled"
	CodeUserDisabled        = "openapi.user_disabled"
	CodeKeyMissing          = "openapi.key_missing"
	CodeKeyInvalid          = "openapi.key_invalid"
	CodeKeyRevoked          = "openapi.key_revoked"
	CodeKeyExpired          = "openapi.key_expired"
	CodeIPForbidden         = "openapi.ip_forbidden"
	CodeRateLimited         = "openapi.rate_limited"
	CodeScopeDenied         = "openapi.scope_denied"
	CodeOwnerMismatch       = "openapi.owner_mismatch"
	CodeKeyLimit            = "openapi.key_limit"
	CodeIPWhitelistOverflow = "openapi.ip_whitelist_overflow"
	CodeIPWhitelistRequired = "openapi.ip_whitelist_required"
	CodeAdminScopeDenied    = "openapi.admin_scope_denied"
	CodeInvalidRequest      = "openapi.invalid_request"
	CodeTypeNotFound        = "openapi.type_not_found"
)

// ErrorCode 是错误响应中的机器可读载荷（写入 data 与响应头）。
type ErrorCode struct {
	Code string `json:"code"`
}

func (e ErrorCode) ResponseHeaders() map[string]string {
	return map[string]string{"X-Bugpk-Openapi-Code": e.Code}
}

// openError 构造带稳定错误码的业务错误。
func openError(code string, status int, message string) error {
	return apperrors.NewWithData(status, message, ErrorCode{Code: code})
}

// Principal 是管理面调用方身份（JWT 解出，与各域同形）。
type Principal struct {
	ID           int64
	Username     string
	Roles        []string
	TokenVersion int
}

// operationRecorder 是审计写入窄接口（observability.Service 实现）。
type operationRecorder interface {
	RecordOperation(ctx context.Context, rec observability.OperationLogRecord)
}

// auditModule 是操作日志模块标识（与 observability 模块码同源）。
const auditModule = string(observability.OperationLogModuleOpenAPI)

// Service 是系统开放 API 域应用服务。
type Service struct {
	repo      *Repository
	authz     *rbac.Service
	rdb       *redis.Client
	digestKey []byte
	db        *sql.DB
	audit     operationRecorder

	accounts AccountSource
	packages PackageSource
	gateway  GatewaySource
	payments PaymentSource
	redeem   RedeemSource

	configMu       sync.RWMutex
	configCache    Config
	configCachedAt time.Time

	worker sync.WaitGroup
}

func NewService(repo *Repository, authz *rbac.Service, rdb *redis.Client, db *sql.DB, secret string) *Service {
	seed := sha256.Sum256([]byte("BugPk-Api:openapi:v1\x00" + secret))
	return &Service{repo: repo, authz: authz, rdb: rdb, db: db, digestKey: seed[:]}
}

// SetSources 装配跨域编排端口（各域 service 原样实现）。
func (s *Service) SetSources(accounts AccountSource, packages PackageSource, gateway GatewaySource, payments PaymentSource, redeemSvc RedeemSource) {
	s.accounts = accounts
	s.packages = packages
	s.gateway = gateway
	s.payments = payments
	s.redeem = redeemSvc
}

// SetOperationRecorder 装配审计写入（敏感操作强制记录）。
func (s *Service) SetOperationRecorder(recorder operationRecorder) {
	s.audit = recorder
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	principal, err := s.authz.Authenticate(ctx, token)
	if err != nil {
		return Principal{}, err
	}
	return Principal{ID: principal.ID, Username: principal.Username, Roles: principal.Roles, TokenVersion: principal.TokenVersion}, nil
}

func (s *Service) require(ctx context.Context, principal Principal, code string) error {
	return s.authz.Require(ctx, rbac.Principal{
		ID: principal.ID, Username: principal.Username, Roles: principal.Roles, TokenVersion: principal.TokenVersion,
	}, code)
}

func isAdminRoles(roles []string) bool {
	for _, role := range roles {
		if strings.TrimSpace(role) == "admin" {
			return true
		}
	}
	return false
}

func (s *Service) record(ctx context.Context, principal Principal, action, summary, targetType, targetID, detail string) {
	if s.audit == nil {
		return
	}
	rec := observability.OperationLogRecord{
		UserID: principal.ID, Username: principal.Username,
		Module: auditModule, Action: action, Summary: summary,
		TargetType: targetType, TargetID: targetID,
		Detail: detail, Status: true,
	}
	s.audit.RecordOperation(ctx, rec)
}

// ---------------------------------------------------------------- 全局配置

func (s *Service) Config(ctx context.Context, principal Principal) (Config, error) {
	if err := s.require(ctx, principal, permConfigRead); err != nil {
		return Config{}, err
	}
	cfg, err := s.repo.Config(ctx)
	if err != nil {
		return Config{}, err
	}
	s.cacheConfig(cfg)
	return cfg, nil
}

// UpdateConfig 更新全局配置并立即失效本地缓存（同进程即时生效）。
func (s *Service) UpdateConfig(ctx context.Context, principal Principal, req ConfigRequest) error {
	if err := s.require(ctx, principal, permConfigUpdate); err != nil {
		return err
	}
	current, err := s.repo.Config(ctx)
	if err != nil {
		return err
	}
	updated := current
	if req.Enabled != nil {
		updated.Enabled = *req.Enabled
	}
	if req.IPWhitelistMax != nil {
		if *req.IPWhitelistMax < 0 || *req.IPWhitelistMax > maxIPWhitelistHard {
			return apperrors.New(consts.StatusBadRequest, fmt.Sprintf("IP 白名单条数上限需在 0~%d 之间", maxIPWhitelistHard))
		}
		updated.IPWhitelistMax = *req.IPWhitelistMax
	}
	if req.KeyDefaultExpireDays != nil {
		if *req.KeyDefaultExpireDays <= 0 || *req.KeyDefaultExpireDays > maxExpireInDays {
			return apperrors.New(consts.StatusBadRequest, fmt.Sprintf("默认有效期需在 1~%d 天之间", maxExpireInDays))
		}
		updated.KeyDefaultExpireDays = *req.KeyDefaultExpireDays
	}
	if req.AdminKeyExpireDays != nil {
		if *req.AdminKeyExpireDays <= 0 || *req.AdminKeyExpireDays > maxExpireInDays {
			return apperrors.New(consts.StatusBadRequest, fmt.Sprintf("管理员作用域有效期需在 1~%d 天之间", maxExpireInDays))
		}
		updated.AdminKeyExpireDays = *req.AdminKeyExpireDays
	}
	if req.RateLimitNormal != nil {
		if *req.RateLimitNormal < 0 {
			return apperrors.New(consts.StatusBadRequest, "普通 Key 频控不得为负")
		}
		updated.RateLimitNormal = *req.RateLimitNormal
	}
	if req.RateLimitAdmin != nil {
		if *req.RateLimitAdmin < 0 {
			return apperrors.New(consts.StatusBadRequest, "管理员作用域频控不得为负")
		}
		updated.RateLimitAdmin = *req.RateLimitAdmin
	}
	if err := s.repo.UpdateConfig(ctx, updated); err != nil {
		return err
	}
	s.cacheConfig(updated)
	s.record(ctx, principal, string(observability.OperationLogActionUpdate),
		fmt.Sprintf("更新开放 API 全局配置（enabled=%t）", updated.Enabled),
		"open_api_config", "1", configDetail(current, updated))
	return nil
}

func (s *Service) cacheConfig(cfg Config) {
	s.configMu.Lock()
	s.configCache = cfg
	s.configCachedAt = time.Now()
	s.configMu.Unlock()
}

// ---------------------------------------------------------------- 用户开关

// SelfSwitchStatus 返回本账号的三层开关读视图。前端页面的开关初始值必须来自这里：
// 写死成「已开启」会让用户看到与自己账号不符的状态，并在下次点击时覆盖真实值。
func (s *Service) SelfSwitchStatus(ctx context.Context, principal Principal) (SwitchStatus, error) {
	if err := s.require(ctx, principal, permSelfRead); err != nil {
		return SwitchStatus{}, err
	}
	if s.accounts == nil {
		return SwitchStatus{}, apperrors.New(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试")
	}
	snapshot, err := s.accounts.AccountSnapshot(ctx, principal.ID)
	if err != nil {
		return SwitchStatus{}, err
	}
	cfg, err := s.cachedConfig(ctx)
	if err != nil {
		return SwitchStatus{}, err
	}
	return SwitchStatus{
		Enabled:       snapshot.OpenAPIEnabled,
		AdminDisabled: snapshot.OpenAPIAdminDisabled,
		GlobalEnabled: cfg.Enabled,
	}, nil
}

// SetSelfSwitch 用户自助开关（实时判定语义：不级联吊销 Key，重开即恢复）。
func (s *Service) SetSelfSwitch(ctx context.Context, principal Principal, enabled bool) error {
	if err := s.require(ctx, principal, permSelfRead); err != nil {
		return err
	}
	if err := s.repo.UpdateUserSwitches(ctx, principal.ID, &enabled, nil); err != nil {
		return err
	}
	s.record(ctx, principal, string(observability.OperationLogActionUpdate),
		fmt.Sprintf("开放 API 自助开关设为 %t", enabled), "user", strconv.FormatInt(principal.ID, 10), "")
	return nil
}

// SetUserDisabled 管理员强制禁用/恢复某账号的开放 API（优先级最高）。
func (s *Service) SetUserDisabled(ctx context.Context, principal Principal, userID int64, disabled bool) error {
	if err := s.require(ctx, principal, permUserDisable); err != nil {
		return err
	}
	if userID <= 0 {
		return apperrors.New(consts.StatusBadRequest, "ID不合法")
	}
	if err := s.repo.UpdateUserSwitches(ctx, userID, nil, &disabled); err != nil {
		if errors.Is(err, errNotFound) {
			return apperrors.New(consts.StatusBadRequest, "账号不存在")
		}
		return err
	}
	s.record(ctx, principal, string(observability.OperationLogActionUpdate),
		fmt.Sprintf("管理员设置账号 #%d 开放 API 禁用=%t", userID, disabled),
		"user", strconv.FormatInt(userID, 10), "")
	return nil
}

// RevokeAllUserKeys 一键吊销某账号名下全部有效 Key（兜底按钮）。
func (s *Service) RevokeAllUserKeys(ctx context.Context, principal Principal, userID int64) (int64, error) {
	if err := s.require(ctx, principal, permKeyRevoke); err != nil {
		return 0, err
	}
	if userID <= 0 {
		return 0, apperrors.New(consts.StatusBadRequest, "ID不合法")
	}
	count, err := s.repo.RevokeAllUserKeys(ctx, userID)
	if err != nil {
		return 0, err
	}
	s.record(ctx, principal, string(observability.OperationLogActionDelete),
		fmt.Sprintf("一键吊销账号 #%d 的全部开放 Key（%d 把）", userID, count),
		"user", strconv.FormatInt(userID, 10), "")
	return count, nil
}

// ---------------------------------------------------------------- Key 生命周期

type listParams struct {
	Keyword  string
	Page     int
	PageSize int
	Scope    string
}

// ListKeys 列表：非 admin 恒为本人；admin 可跨账号（scope=all）。
func (s *Service) ListKeys(ctx context.Context, principal Principal, params listParams) ([]OpenAPIKey, int64, error) {
	selfView := strings.ToLower(strings.TrimSpace(params.Scope)) != "all"
	code := permSelfRead
	if !selfView {
		code = permKeyList
	}
	if err := s.require(ctx, principal, code); err != nil {
		return nil, 0, err
	}
	adminAll := !selfView && isAdminRoles(principal.Roles)
	rows, total, err := s.repo.ListKeys(ctx, strings.TrimSpace(params.Keyword), principal.ID, adminAll, params.Page, params.PageSize)
	if err != nil {
		return nil, 0, err
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	scopeMap, err := s.repo.ScopesByKeys(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	ruleMap, err := s.repo.IPWhitelistByKeys(ctx, ids)
	if err != nil {
		return nil, 0, err
	}
	items := make([]OpenAPIKey, 0, len(rows))
	for _, row := range rows {
		items = append(items, toOpenAPIKey(row, scopeMap[row.ID], ruleMap[row.ID]))
	}
	return items, total, nil
}

// IssueKey 签发开放 Key。targetUserID 缺省=本人；跨账号签发与管理员作用域
// 仅管理员可执行（管理员作用域还要求目标账号当前具备管理员角色）。
func (s *Service) IssueKey(ctx context.Context, principal Principal, req IssueKeyRequest) (IssuedKey, error) {
	targetID := req.UserID
	selfService := targetID == 0 || targetID == principal.ID
	if selfService {
		targetID = principal.ID
	}
	code := permSelfRead
	if !selfService {
		code = permKeyCreate
	}
	if err := s.require(ctx, principal, code); err != nil {
		return IssuedKey{}, err
	}
	return s.issue(ctx, principal, targetID, req, selfService)
}

// ResetKey 重置：旧行吊销（保审计）+ 新行同配置签发，明文只返回一次。
func (s *Service) ResetKey(ctx context.Context, principal Principal, id int64) (IssuedKey, error) {
	key, ownerSelf, err := s.loadOwnedKey(ctx, principal, id, permSelfRead, permKeyRevoke)
	if err != nil {
		return IssuedKey{}, err
	}
	scopes, err := s.repo.ScopesByKey(ctx, key.ID)
	if err != nil {
		return IssuedKey{}, err
	}
	rules, err := s.repo.IPWhitelistByKey(ctx, key.ID)
	if err != nil {
		return IssuedKey{}, err
	}
	if err := s.repo.RevokeKey(ctx, key.ID); err != nil {
		return IssuedKey{}, err
	}
	s.record(ctx, principal, string(observability.OperationLogActionUpdate),
		fmt.Sprintf("重置开放 Key #%d（账号 #%d）", key.ID, key.UserID),
		"open_api_key", strconv.FormatInt(key.ID, 10), "")
	days := 0
	if key.ExpiresAt.After(time.Now()) {
		days = int(time.Until(key.ExpiresAt).Hours() / 24)
	}
	if days <= 0 {
		days = 0
	}
	req := IssueKeyRequest{UserID: key.UserID, Name: key.Name, Scopes: scopes, IPWhitelist: rules}
	if days > 0 {
		req.ExpiresInDays = &days
	}
	return s.issue(ctx, principal, key.UserID, req, ownerSelf)
}

// RevokeKey 吊销单把 Key（本人或管理员）。
func (s *Service) RevokeKey(ctx context.Context, principal Principal, id int64) error {
	key, _, err := s.loadOwnedKey(ctx, principal, id, permSelfRead, permKeyRevoke)
	if err != nil {
		return err
	}
	if err := s.repo.RevokeKey(ctx, key.ID); err != nil {
		if errors.Is(err, errNotFound) {
			return openError(CodeKeyRevoked, consts.StatusConflict, "该开放 Key 已被吊销")
		}
		return err
	}
	s.record(ctx, principal, string(observability.OperationLogActionDelete),
		fmt.Sprintf("吊销开放 Key #%d（账号 #%d）", key.ID, key.UserID),
		"open_api_key", strconv.FormatInt(key.ID, 10), "")
	return nil
}

// loadOwnedKey 读取 Key 并做归属校验（非本人且非管理员 ⇒ owner_mismatch）。
func (s *Service) loadOwnedKey(ctx context.Context, principal Principal, id int64, selfCode, otherCode string) (keyRow, bool, error) {
	if id <= 0 {
		return keyRow{}, false, apperrors.New(consts.StatusBadRequest, "ID不合法")
	}
	key, err := s.repo.KeyByID(ctx, id, 0)
	if err != nil {
		if errors.Is(err, errNotFound) {
			return keyRow{}, false, apperrors.New(consts.StatusBadRequest, "开放 Key 不存在")
		}
		return keyRow{}, false, err
	}
	ownerSelf := key.UserID == principal.ID
	if !ownerSelf && !isAdminRoles(principal.Roles) {
		return keyRow{}, false, openError(CodeOwnerMismatch, consts.StatusForbidden, "无权操作他人的开放 Key")
	}
	code := selfCode
	if !ownerSelf {
		code = otherCode
	}
	if err := s.require(ctx, principal, code); err != nil {
		return keyRow{}, false, err
	}
	return key, ownerSelf, nil
}

// issue 是全流程实现：配置校验 → scope 归一化/派生 → 白名单校验 →
// 唯一有效 Key 预检 → 落库（Key + scope + 白名单，失败补偿吊销）。
func (s *Service) issue(ctx context.Context, principal Principal, targetID int64, req IssueKeyRequest, selfService bool) (IssuedKey, error) {
	if s.accounts == nil {
		return IssuedKey{}, apperrors.New(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试")
	}
	snapshot, err := s.accounts.AccountSnapshot(ctx, targetID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return IssuedKey{}, apperrors.New(consts.StatusBadRequest, "账号不存在")
		}
		return IssuedKey{}, err
	}
	cfg, err := s.repo.Config(ctx)
	if err != nil {
		return IssuedKey{}, err
	}
	// 签发期同样受全局开关约束：运行期拦截只让 Key 打不通，关闭期间照常签发会
	// 攒下无效 Key，且重新开启后这些 Key 会立刻生效（每账号仅 1 把有效 Key）。
	if !cfg.Enabled {
		return IssuedKey{}, openError(CodeDisabled, consts.StatusForbidden, "开放 API 未开启，暂不能签发密钥")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = defaultKeyNamePrefix
	}
	if len(name) > maxKeyNameLength {
		return IssuedKey{}, apperrors.New(consts.StatusBadRequest, "备注名过长")
	}
	// scope：缺省从持有账号当前角色权限派生（S-13：默认值降低误配）。
	scopes := req.Scopes
	if len(scopes) == 0 {
		scopes, err = s.derivedScopes(ctx, snapshot)
		if err != nil {
			return IssuedKey{}, err
		}
	}
	scopes, err = normalizeScopes(scopes)
	if err != nil {
		return IssuedKey{}, apperrors.New(consts.StatusBadRequest, "scope 含未知端点")
	}
	adminScope := hasAdminScope(scopes)
	if adminScope {
		// 管理员作用域：仅管理员可签发，且目标账号当前必须具备管理员角色。
		if !isAdminRoles(principal.Roles) {
			return IssuedKey{}, openError(CodeAdminScopeDenied, consts.StatusForbidden, "仅管理员可签发管理员作用域 Key")
		}
		if !isAdminRoles(snapshot.Roles) {
			return IssuedKey{}, openError(CodeAdminScopeDenied, consts.StatusBadRequest, "目标账号不具备管理员角色")
		}
	}
	rules, err := normalizeIPRules(req.IPWhitelist)
	if err != nil {
		return IssuedKey{}, err
	}
	if len(rules) > cfg.IPWhitelistMax {
		return IssuedKey{}, openError(CodeIPWhitelistOverflow, consts.StatusBadRequest,
			fmt.Sprintf("IP 白名单最多 %d 条", cfg.IPWhitelistMax))
	}
	if adminScope && len(rules) == 0 {
		return IssuedKey{}, openError(CodeIPWhitelistRequired, consts.StatusBadRequest, "管理员作用域 Key 必须配置 IP 白名单")
	}
	days := cfg.KeyDefaultExpireDays
	if adminScope {
		days = cfg.AdminKeyExpireDays
	}
	if req.ExpiresInDays != nil {
		if *req.ExpiresInDays <= 0 || *req.ExpiresInDays > maxExpireInDays {
			return IssuedKey{}, apperrors.New(consts.StatusBadRequest, fmt.Sprintf("有效期需在 1~%d 天之间", maxExpireInDays))
		}
		days = *req.ExpiresInDays
	}
	// 每账号仅 1 个有效 Key：预检给友好文案，DB 唯一键兜底并发。
	if existing, err := s.repo.ActiveKeyByUser(ctx, targetID); err == nil {
		if !existing.ExpiresAt.After(time.Now()) {
			// 过期行的 status 仍是 1，继续占用唯一键；不说清这一点，用户会以为
			// 「明明过期了为什么还不能签发」。重置即续期，是唯一自助出路。
			return IssuedKey{}, openError(CodeKeyLimit, consts.StatusConflict,
				"该账号的开放 Key 已过期，重置即可获得新密钥，或先吊销后再签发")
		}
		return IssuedKey{}, openError(CodeKeyLimit, consts.StatusConflict, "该账号已有有效开放 Key，请先重置或吊销")
	} else if !errors.Is(err, errNotFound) {
		return IssuedKey{}, err
	}
	secret, err := newKeySecret(adminScope)
	if err != nil {
		return IssuedKey{}, apperrors.Wrap(consts.StatusInternalServerError, "开放 Key 生成失败", err)
	}
	expiresAt := time.Now().Add(time.Duration(days) * 24 * time.Hour)
	keyID, err := s.repo.CreateKey(ctx, targetID, name, hashKey(secret), keyPrefixOf(secret), keySuffixOf(secret), adminScope, expiresAt)
	if err != nil {
		if isDuplicateIndex(err, "uk_open_api_key_active_user") {
			return IssuedKey{}, openError(CodeKeyLimit, consts.StatusConflict, "该账号已有有效开放 Key，请先重置或吊销")
		}
		return IssuedKey{}, err
	}
	// 补偿式写入：scope/白名单失败即吊销新行（空 scope 本就 fail-closed，
	// 吊销只是让状态与审计一致）。
	if err := s.repo.ReplaceScopes(ctx, keyID, scopes); err != nil {
		_ = s.repo.RevokeKey(ctx, keyID)
		return IssuedKey{}, err
	}
	if err := s.repo.ReplaceIPWhitelist(ctx, keyID, rules); err != nil {
		_ = s.repo.RevokeKey(ctx, keyID)
		return IssuedKey{}, err
	}
	key, err := s.repo.KeyByID(ctx, keyID, 0)
	if err != nil {
		return IssuedKey{}, err
	}
	verb := "签发"
	if !selfService {
		verb = "为账号签发"
	}
	s.record(ctx, principal, string(observability.OperationLogActionCreate),
		fmt.Sprintf("%s开放 Key #%d（账号 #%d，管理员作用域=%t，scope=%d 项）", verb, keyID, targetID, adminScope, len(scopes)),
		"open_api_key", strconv.FormatInt(keyID, 10),
		issueDetail(targetID, scopes, rules, adminScope, expiresAt))
	return IssuedKey{OpenAPIKey: toOpenAPIKey(key, scopes, rules), SecretKey: secret}, nil
}

// ScopeOptions 返回签发表单的 scope 选项（Granted=持有账号当前角色是否具备）。
func (s *Service) ScopeOptions(ctx context.Context, principal Principal, targetUserID int64) ([]ScopeOption, error) {
	if err := s.require(ctx, principal, permSelfRead); err != nil {
		return nil, err
	}
	if targetUserID == 0 {
		targetUserID = principal.ID
	}
	snapshot, err := s.accounts.AccountSnapshot(ctx, targetUserID)
	if err != nil {
		return nil, err
	}
	granted, err := s.grantedEndpoints(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	options := make([]ScopeOption, 0, len(EndpointCodes))
	for _, code := range EndpointCodes {
		options = append(options, ScopeOption{
			Code:      code,
			Label:     scopeLabel(code),
			AdminOnly: code == EndpointRedeemCodeGenerate,
			Granted:   granted[code],
		})
	}
	return options, nil
}

// derivedScopes 按持有账号当前角色权限派生默认 scope 清单。
func (s *Service) derivedScopes(ctx context.Context, snapshot userdomain.AccountSnapshot) ([]string, error) {
	granted, err := s.grantedEndpoints(ctx, snapshot)
	if err != nil {
		return nil, err
	}
	scopes := make([]string, 0, len(EndpointCodes))
	for _, code := range EndpointCodes {
		if granted[code] {
			scopes = append(scopes, code)
		}
	}
	return scopes, nil
}

// grantedEndpoints 计算端点→账号当前权限的命中表（无权限要求的端点恒真）。
func (s *Service) grantedEndpoints(ctx context.Context, snapshot userdomain.AccountSnapshot) (map[string]bool, error) {
	codes, err := s.authz.PermissionCodes(ctx, snapshot.UserID, snapshot.Roles)
	if err != nil {
		return nil, err
	}
	owned := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		owned[code] = struct{}{}
	}
	granted := make(map[string]bool, len(EndpointCodes))
	for _, endpoint := range EndpointCodes {
		required := endpointPermissions[endpoint]
		if len(required) == 0 {
			granted[endpoint] = true
			continue
		}
		for _, code := range required {
			if _, ok := owned[code]; ok {
				granted[endpoint] = true
				break
			}
		}
	}
	return granted, nil
}

// ---------------------------------------------------------------- 助手

func toOpenAPIKey(row keyRow, scopes, rules []string) OpenAPIKey {
	return OpenAPIKey{
		ID:           strconv.FormatInt(row.ID, 10),
		UserID:       strconv.FormatInt(row.UserID, 10),
		Username:     row.Username,
		Name:         row.Name,
		MaskedKey:    maskedKeyOf(row),
		IsAdminScope: row.IsAdminScope,
		Status:       row.Status,
		Expired:      row.Status == KeyStatusActive && !row.ExpiresAt.After(time.Now()),
		Scopes:       scopes,
		IPWhitelist:  rules,
		ExpiresAt:    formatTime(row.ExpiresAt),
		LastUsedAt:   formatNullTime(row.LastUsedAt),
		CreateTime:   formatTime(row.CreatedAt),
		RevokedAt:    formatNullTime(row.RevokedAt),
	}
}

// maskedKeyOf 生成带前缀标记的掩码（管理端页面展示，含 opk_/opka_ 一眼可辨）。
func maskedKeyOf(row keyRow) string {
	marker := KeyMarkerNormal
	if row.IsAdminScope {
		marker = KeyMarkerAdmin
	}
	return marker + row.KeyPrefix + "****" + row.KeySuffix
}

func normalizeIPRules(raw []string) ([]string, error) {
	seen := make(map[string]struct{}, len(raw))
	rules := make([]string, 0, len(raw))
	for _, item := range raw {
		rule, err := normalizeIPRule(item)
		if err != nil {
			return nil, apperrors.New(consts.StatusBadRequest, "IP 白名单条目格式不正确")
		}
		if _, ok := seen[rule]; ok {
			continue
		}
		seen[rule] = struct{}{}
		rules = append(rules, rule)
	}
	return rules, nil
}

// isDuplicateIndex 判断唯一键冲突并精确匹配索引名（每账号仅 1 个有效 Key
// 的并发兜底：预检之外由 uk_open_api_key_active_user 拒绝）。
func isDuplicateIndex(err error, index string) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
		return false
	}
	return index == "" || strings.Contains(mysqlErr.Message, index)
}

func scopeLabel(code string) string {
	switch code {
	case EndpointAccountRead:
		return "读取本账号信息"
	case EndpointPackageRead:
		return "读取名下套餐权益"
	case EndpointAPIKeyWrite:
		return "创建接口 Key"
	case EndpointAPIKeyRead:
		return "查询接口 Key"
	case EndpointOrderCreate:
		return "创建支付订单"
	case EndpointRedeemWrite:
		return "兑换 CDK"
	case EndpointRedeemCodeGenerate:
		return "批量生成 CDK（管理员作用域）"
	default:
		return code
	}
}

func configDetail(before, after Config) string {
	return fmt.Sprintf(`{"before":{"enabled":%t,"ipWhitelistMax":%d,"keyDefaultExpireDays":%d,"adminKeyExpireDays":%d,"rateLimitNormal":%d,"rateLimitAdmin":%d},"after":{"enabled":%t,"ipWhitelistMax":%d,"keyDefaultExpireDays":%d,"adminKeyExpireDays":%d,"rateLimitNormal":%d,"rateLimitAdmin":%d}}`,
		before.Enabled, before.IPWhitelistMax, before.KeyDefaultExpireDays, before.AdminKeyExpireDays, before.RateLimitNormal, before.RateLimitAdmin,
		after.Enabled, after.IPWhitelistMax, after.KeyDefaultExpireDays, after.AdminKeyExpireDays, after.RateLimitNormal, after.RateLimitAdmin)
}

func issueDetail(userID int64, scopes, rules []string, adminScope bool, expiresAt time.Time) string {
	return fmt.Sprintf(`{"userId":%d,"scopes":%q,"ipWhitelist":%q,"adminScope":%t,"expiresAt":%q}`,
		userID, scopes, rules, adminScope, formatTime(expiresAt))
}

func normalizePage(page, pageSize int) (int, int) {
	return pagination.Normalize(page, pageSize, 100)
}

func hashOf(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}
