package gateway

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"bugpk-api/internal/domain/ipregion"
	"bugpk-api/internal/domain/keyaccess"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/accountstatus"
	"bugpk-api/internal/pkg/calloutcome"
	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/decimalvalue"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/httpclient"
	"bugpk-api/internal/pkg/httpstatus"
	"bugpk-api/internal/pkg/i18n"
	"bugpk-api/internal/pkg/jsoncleaning"
	"bugpk-api/internal/pkg/jsonpathvalue"
	"bugpk-api/internal/pkg/jsonrequest"
	"bugpk-api/internal/pkg/listquery"
	"bugpk-api/internal/pkg/masking"
	"bugpk-api/internal/pkg/proxypool"
	"bugpk-api/internal/pkg/redisutil"
	"bugpk-api/internal/pkg/requestid"
	"bugpk-api/internal/pkg/requestrisk"
	"bugpk-api/internal/pkg/requesttimeout"
	"bugpk-api/internal/pkg/requestvalidation"
	"bugpk-api/internal/pkg/secretbox"
	"bugpk-api/internal/pkg/upstreamhealth"
	"bugpk-api/internal/pkg/videojson"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

type Service struct {
	repo                   *Repository
	authz                  *rbac.Service
	rdb                    *redis.Client
	ipResolver             *ipregion.Resolver
	ipSlots                chan struct{}
	displaySlots           chan struct{}
	httpClient             *http.Client
	proxyResolver          *proxypool.Resolver
	fingerprintKey         []byte
	keyCipher              *secretbox.Cipher
	log                    *zap.Logger
	now                    func() time.Time
	rejectionLimiterWarnAt atomic.Int64
	sameParamsRiskWarnAt   atomic.Int64
	degradeWarnAt          atomic.Int64
	backgroundMu           sync.Mutex
	backgroundWG           sync.WaitGroup
	backgroundDone         chan struct{}
	backgroundWaitOnce     sync.Once
	backgroundClosing      bool
	riskSlots              chan struct{}
	riskSlotsOnce          sync.Once
	// healthSlots bounds detached health-bookkeeping goroutines so a slow
	// Redis instance cannot create an unbounded goroutine queue under load.
	healthSlots            chan struct{}
	responseAdProvider     ResponseAdvertisementProvider
	interfaceCache         *interfaceConfigCache
	keyAuthCache           *keyAuthCache
	keyInterfaceCache      *keyInterfaceCache
	specifiedResponseCache *specifiedResponseCache
	// paidEntitlement 缓存「免费额度打满后是否有付费途径可承接」的判定。
	// 免费额度一旦打满，套餐用户的每个请求都会走到这个判断；不加缓存就是每个
	// 请求三条 exists。条目数有上限，超过阈值后只读不写，避免接口维度把 map 撑大。
	paidEntitlement      sync.Map
	paidEntitlementCount atomic.Int64
	// sameParamsRiskFn is kept injectable for deterministic concurrency tests;
	// production uses recordSameParamsRisk.
	sameParamsRiskFn func(context.Context, OpenResult)
	// healthRecordFn is an optional test hook. Production records metrics via
	// upstreamhealth.Record; keeping the hook private avoids exposing runtime
	// implementation details in the service API.
	healthRecordFn func(context.Context, int64, string, bool, int64) error
	// healthRecordScopedFn is used by route-isolation tests and by future
	// diagnostics that need the selected domain-pool identity.
	healthRecordScopedFn func(context.Context, int64, string, string, bool, int64) error
	// updateCurrentNodeFn is a private test hook for the asynchronous display
	// update. Production uses Repository.UpdateCurrentNode when it is nil.
	updateCurrentNodeFn func(context.Context, int64, int) error
	// localizer 提供后端数据字典译文（用户组名等）；nil 时保持默认语言原样。
	localizer i18n.Localizer
}

// SetLocalizer 接入翻译字典；nil 时配额概览等输出保持默认语言原文。
func (s *Service) SetLocalizer(localizer i18n.Localizer) {
	s.localizer = localizer
}

type qpsReservation struct {
	Keys     []string
	Counted  bool
	Reserved bool
}

var placeholderPattern = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9_]*)}`)

var invalidKeyGuardScript = redis.NewScript(`
local count = tonumber(redis.call("GET", KEYS[1]) or "0")
if count >= tonumber(ARGV[1]) then
  redis.call("INCR", KEYS[3])
  redis.call("EXPIRE", KEYS[3], ARGV[3])
  redis.call("INCR", KEYS[4])
  redis.call("EXPIRE", KEYS[4], ARGV[3])
  return 2
end
if redis.call("EXISTS", KEYS[2]) == 0 then
  return 0
end
count = redis.call("INCR", KEYS[1])
if count == 1 or redis.call("PTTL", KEYS[1]) < 0 then
  redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
redis.call("INCR", KEYS[3])
redis.call("EXPIRE", KEYS[3], ARGV[3])
if count >= tonumber(ARGV[1]) then
  redis.call("INCR", KEYS[4])
  redis.call("EXPIRE", KEYS[4], ARGV[3])
  return 2
end
return 1
`)

var rememberInvalidKeyScript = redis.NewScript(`
redis.call("SET", KEYS[2], "1", "PX", ARGV[1])
local count = redis.call("INCR", KEYS[1])
if count == 1 or redis.call("PTTL", KEYS[1]) < 0 then
  redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
redis.call("INCR", KEYS[3])
redis.call("EXPIRE", KEYS[3], ARGV[3])
return count
`)

var qpsReserveScript = redis.NewScript(`
local limit = tonumber(ARGV[1])
local ttl = tonumber(ARGV[2])
local current = tonumber(redis.call("GET", KEYS[1]) or "0")
if current >= limit then
  return 0
end
current = redis.call("INCR", KEYS[1])
if current == 1 or redis.call("PTTL", KEYS[1]) < 0 then
  redis.call("PEXPIRE", KEYS[1], ttl)
end
return 1
`)

const (
	billingReleaseAttempts = 3
	billingReleaseBackoff  = 50 * time.Millisecond
	billingReleaseTimeout  = 2 * time.Second
	displayUpdateThrottle  = 10 * time.Second
	// qpsScopeAccount / qpsScopeInterface 标识限流桶的归属，用于生成准确的
	// 拒绝文案与日志字段（账号级配额 vs 单接口自定义配额）。
	qpsScopeAccount         = "ACCOUNT"
	qpsScopeInterface       = "INTERFACE"
	qpsScopeInterfaceShared = "INTERFACE_SHARED"
	// ipRegionSoftTimeout 是网关同步等待 IP 归属地解析的软超时：
	// 默认源走公网 HTTPS，单次往返通常 100-500ms（首调更慢），
	// 50ms 会导致每个 IP 的首次调用都超时，日志地区列永远显示 "-"。
	// 调高到 2s 后，首次调用即可同步解析成功并写入 Redis 缓存（TTL 24h）。
	ipRegionSoftTimeout = 2 * time.Second
	// ipRegionWarmTimeout 是后台预热重试的超时：失败时后台重试一次，
	// 给慢速自定义源留足时间（解析器自身超时默认 5s）。
	ipRegionWarmTimeout       = 5 * time.Second
	invalidKeyAttemptLimit    = 30
	invalidKeyAttemptTTL      = time.Minute
	invalidKeyCacheTTL        = 5 * time.Minute
	invalidKeyMetricTTL       = 26 * time.Hour
	nonStructuredBodyRedact   = "[NON_STRUCTURED_BODY_REDACTED]"
	openRejectionFineLimit    = 20
	openRejectionGlobalLimit  = 200
	openRejectionRateTTL      = 2 * time.Minute
	openRejectionWarnThrottle = 30 * time.Second
	qpsReleaseTimeout         = 500 * time.Millisecond
	roundRobinCounterTTL      = 7 * 24 * time.Hour
	sameParamsRiskTimeout     = 50 * time.Millisecond
	upstreamHealthReadTimeout = 75 * time.Millisecond
	// Health metrics are best-effort bookkeeping and must never consume the
	// caller's entire upstream polling budget.
	upstreamHealthWriteTimeout = 100 * time.Millisecond
)

func NewService(repo *Repository, authz *rbac.Service, rdb *redis.Client, fingerprintSecret string, ciphers ...*secretbox.Cipher) *Service {
	keyCipher := secretbox.New(fingerprintSecret)
	if len(ciphers) > 0 && ciphers[0] != nil {
		keyCipher = ciphers[0]
	}
	return &Service{
		repo:           repo,
		authz:          authz,
		rdb:            rdb,
		ipSlots:        make(chan struct{}, 128),
		displaySlots:   make(chan struct{}, 128),
		riskSlots:      make(chan struct{}, 128),
		healthSlots:    make(chan struct{}, 128),
		backgroundDone: make(chan struct{}),
		httpClient: httpclient.New(httpclient.Options{
			Timeout:               time.Duration(requesttimeout.MaxMilliseconds) * time.Millisecond,
			ResponseHeaderTimeout: time.Duration(requesttimeout.MaxMilliseconds) * time.Millisecond,
		}),
		proxyResolver:          proxypool.NewResolver(httpclient.Outbound(5 * time.Second)),
		fingerprintKey:         deriveFingerprintKey(fingerprintSecret),
		keyCipher:              keyCipher,
		now:                    time.Now,
		interfaceCache:         newInterfaceConfigCache(interfaceConfigTTL),
		keyAuthCache:           newKeyAuthCache(keyAuthCacheTTL),
		keyInterfaceCache:      newKeyInterfaceCache(keyInterfaceCacheTTL),
		specifiedResponseCache: newSpecifiedResponseCache(specifiedResponseCacheTTL),
	}
}

// InvalidateSpecifiedResponse removes a user's short-lived gateway response
// configuration after an administrator changes it. The TTL remains the
// fallback for changes made outside this process.
func (s *Service) InvalidateSpecifiedResponse(userID int64) {
	if s == nil || s.specifiedResponseCache == nil {
		return
	}
	s.specifiedResponseCache.invalidate(userID)
}

// InvalidateInterfaceConfig drops one or more API-code entries from the
// gateway's short-lived configuration cache. Interface administration calls
// this after a successful write so new polling/upstream settings take effect
// immediately instead of waiting for the normal TTL.
func (s *Service) InvalidateInterfaceConfig(apiCodes ...string) {
	if s == nil || s.interfaceCache == nil {
		return
	}
	s.interfaceCache.invalidate(apiCodes...)
}

func (s *Service) SetIPRegionResolver(resolver *ipregion.Resolver) {
	s.ipResolver = resolver
}

func (s *Service) SetHTTPClient(client *http.Client) {
	if client != nil {
		s.httpClient = client
		s.proxyResolver = proxypool.NewResolver(client)
	}
}

func (s *Service) SetLogger(log *zap.Logger) {
	s.log = log
	if s.repo != nil {
		s.repo.log = log
	}
}

func (s *Service) SetResponseAdvertisementProvider(provider ResponseAdvertisementProvider) {
	s.responseAdProvider = provider
}

func (s *Service) Authenticate(ctx context.Context, accessToken string) (rbac.Principal, error) {
	return s.authz.Authenticate(ctx, accessToken)
}

func (s *Service) ListKeys(ctx context.Context, principal rbac.Principal, params KeyListParams) (PageResult[APIKeyItem], error) {
	if err := s.authz.Require(ctx, principal, "key:list"); err != nil {
		return PageResult[APIKeyItem]{}, err
	}
	if err := normalizeKeyListParams(&params); err != nil {
		return PageResult[APIKeyItem]{}, err
	}
	admin := hasRole(principal.Roles, "admin")
	listAllUsers, err := resolveKeyListOwnerScope(params.OwnerScope, admin)
	if err != nil {
		return PageResult[APIKeyItem]{}, err
	}
	// Repository.ListKeys uses the admin flag as the explicit ownership
	// bypass. Passing the resolved value here keeps an administrator's
	// personal view owner-scoped while preserving the legacy admin default.
	return s.repo.ListKeys(ctx, principal.ID, listAllUsers, params)
}

// KeyDetail 返回单个密钥的完整元信息。明文通过 KeySecret 按需解密，避免列表加载时批量暴露。
func (s *Service) KeyDetail(ctx context.Context, principal rbac.Principal, id int64) (APIKeyItem, error) {
	if err := s.authz.Require(ctx, principal, "key:list"); err != nil {
		return APIKeyItem{}, err
	}
	item, err := s.repo.KeyByID(ctx, id, principal.ID, hasRole(principal.Roles, "admin"))
	if err != nil {
		return APIKeyItem{}, mapNotFound(err, "Key不存在")
	}
	interfaceIDs, err := s.repo.Interfaces(ctx, id)
	if err != nil {
		return APIKeyItem{}, err
	}
	ipRules, err := s.repo.IPRules(ctx, id)
	if err != nil {
		return APIKeyItem{}, err
	}
	item.InterfaceIDs = interfaceIDs
	item.IPWhitelist = ipRules
	return item, nil
}

func (s *Service) KeySecret(ctx context.Context, principal rbac.Principal, id int64) (KeySecretResult, error) {
	if err := s.authz.Require(ctx, principal, "key:list"); err != nil {
		return KeySecretResult{}, err
	}
	item, err := s.repo.KeyByID(ctx, id, principal.ID, hasRole(principal.Roles, "admin"))
	if err != nil {
		return KeySecretResult{}, mapNotFound(err, "Key不存在")
	}
	if !item.SecretAvailable || strings.TrimSpace(item.secretCiphertext) == "" {
		return KeySecretResult{}, apperrors.New(consts.StatusConflict, "该密钥未保存可恢复明文，请重新创建密钥")
	}
	secret, err := s.keyCipher.DecryptCompatible(item.secretCiphertext)
	if err != nil {
		return KeySecretResult{}, apperrors.Wrap(consts.StatusInternalServerError, "密钥解密失败", err)
	}
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return KeySecretResult{}, apperrors.New(consts.StatusConflict, "该密钥未保存可恢复明文，请重新创建密钥")
	}
	return KeySecretResult{SecretKey: secret}, nil
}

func normalizeKeyListParams(params *KeyListParams) error {
	params.Keyword = strings.TrimSpace(params.Keyword)
	params.KeyType = strings.ToUpper(strings.TrimSpace(params.KeyType))
	params.OwnerScope = strings.ToUpper(strings.TrimSpace(params.OwnerScope))
	if err := listquery.Validate(params.Keyword, params.Page, params.PageSize, 100); err != nil {
		return err
	}
	if err := listquery.Binary("status", params.Status); err != nil {
		return err
	}
	if err := listquery.Enum("keyType", params.KeyType, "GLOBAL", "INTERFACE"); err != nil {
		return err
	}
	return listquery.Enum("scope", params.OwnerScope, keyOwnerScopeAll, keyOwnerScopeMine)
}

// resolveKeyListOwnerScope converts the optional request scope into the
// repository's ownership switch. An administrator may explicitly request
// their own keys, while non-administrators can never broaden the query to all
// users' keys.
func resolveKeyListOwnerScope(requested string, admin bool) (bool, error) {
	switch strings.ToUpper(strings.TrimSpace(requested)) {
	case "":
		return admin, nil
	case keyOwnerScopeMine:
		return false, nil
	case keyOwnerScopeAll:
		if !admin {
			return false, apperrors.New(consts.StatusForbidden, "无权查看全部用户密钥")
		}
		return true, nil
	default:
		// normalizeKeyListParams normally catches this branch. Keep the helper
		// fail-closed for direct callers and future code paths.
		return false, apperrors.New(consts.StatusBadRequest, "scope is invalid")
	}
}

func (s *Service) CreateKey(ctx context.Context, principal rbac.Principal, req SaveKeyRequest) (CreateKeyResult, error) {
	if err := s.authz.Require(ctx, principal, "key:create"); err != nil {
		return CreateKeyResult{}, err
	}
	item, expiresAt, interfaces, ipRules, err := s.normalizeKey(ctx, principal, req)
	if err != nil {
		return CreateKeyResult{}, err
	}
	secret, err := newSecret()
	if err != nil {
		return CreateKeyResult{}, err
	}
	secretCiphertext, err := s.keyCipher.Encrypt(secret)
	if err != nil {
		return CreateKeyResult{}, apperrors.Wrap(consts.StatusInternalServerError, "密钥加密失败", err)
	}
	item.UserID = strconv.FormatInt(principal.ID, 10)
	item.KeyPrefix = secretPrefix(secret)
	item.KeySuffix = secretSuffix(secret)
	id, err := s.repo.CreateKey(ctx, item, secretHash(secret), secretCiphertext, expiresAt, interfaces, ipRules)
	if err != nil {
		return CreateKeyResult{}, mapDBError(err, "创建Key失败")
	}
	item.ID = strconv.FormatInt(id, 10)
	item.InterfaceIDs = interfaces
	item.IPWhitelist = ipRules
	item.MaskedKey = item.KeyPrefix + "****" + item.KeySuffix
	item.SecretAvailable = true
	return CreateKeyResult{APIKeyItem: item, SecretKey: secret}, nil
}

func (s *Service) UpdateKey(ctx context.Context, principal rbac.Principal, id int64, req SaveKeyRequest) error {
	if err := s.authz.Require(ctx, principal, "key:update"); err != nil {
		return err
	}
	current, err := s.repo.KeyByID(ctx, id, principal.ID, hasRole(principal.Roles, "admin"))
	if err != nil {
		return mapNotFound(err, "Key不存在")
	}
	if strings.TrimSpace(req.Name) == "" {
		req.Name = current.Name
	}
	if strings.TrimSpace(req.KeyType) == "" {
		req.KeyType = current.KeyType
	}
	if req.Status == nil {
		status := current.Status
		req.Status = &status
	}
	preserveOmittedKeyExpiration(&req, current.ExpiresAt)
	if req.InterfaceIDs == nil {
		req.InterfaceIDs, err = s.repo.Interfaces(ctx, id)
		if err != nil {
			return err
		}
	}
	if req.IPWhitelist == nil {
		req.IPWhitelist, err = s.repo.IPRules(ctx, id)
		if err != nil {
			return err
		}
	}
	item, expiresAt, interfaces, ipRules, err := s.normalizeKey(ctx, principal, req)
	if err != nil {
		return err
	}
	if err := mapNotFound(s.repo.UpdateKey(ctx, id, item, expiresAt, interfaces, ipRules), "Key不存在"); err != nil {
		return err
	}
	s.invalidateKeyCaches(id)
	return nil
}

func (s *Service) DeleteKey(ctx context.Context, principal rbac.Principal, id int64) error {
	if err := s.authz.Require(ctx, principal, "key:delete"); err != nil {
		return err
	}
	if _, err := s.repo.KeyByID(ctx, id, principal.ID, hasRole(principal.Roles, "admin")); err != nil {
		return mapNotFound(err, "Key不存在")
	}
	if err := mapNotFound(s.repo.DeleteKey(ctx, id), "Key不存在"); err != nil {
		return err
	}
	s.invalidateKeyCaches(id)
	return nil
}

func (s *Service) UpdateStatus(ctx context.Context, principal rbac.Principal, id int64, status int) error {
	if err := s.authz.Require(ctx, principal, "key:status"); err != nil {
		return err
	}
	if status != 0 && status != 1 {
		return apperrors.New(consts.StatusBadRequest, "Key状态不合法")
	}
	if _, err := s.repo.KeyByID(ctx, id, principal.ID, hasRole(principal.Roles, "admin")); err != nil {
		return mapNotFound(err, "Key不存在")
	}
	if err := mapNotFound(s.repo.UpdateStatus(ctx, id, status), "Key不存在"); err != nil {
		return err
	}
	s.invalidateKeyCaches(id)
	return nil
}

// Key 的启停不在这里判定账号封禁。
//
// 封禁是一个账号级开关：所有携带身份的请求在进入业务前由服务器级的账号
// 守卫统一拒绝（app.newAccountStatusGuard），/api 平面由 OpenKey 在解出 Key
// 所属账号后拒绝。若再在这里加一次「账号被封就不能启用 Key」，等于给同一件
// 事造第二个裁决点——账号解封后它仍按 Key 上的历史标记拒绝，运营会看到
// 「账号已解封却无法启用密钥」，而任何功能都恢复不了。Key 的 status 只表达
// 「这把凭证是否被启用」，账号能不能用由守卫表达。

func (s *Service) Interfaces(ctx context.Context, principal rbac.Principal, id int64) ([]int64, error) {
	if err := s.authz.Require(ctx, principal, "key:interface"); err != nil {
		return nil, err
	}
	if _, err := s.repo.KeyByID(ctx, id, principal.ID, hasRole(principal.Roles, "admin")); err != nil {
		return nil, mapNotFound(err, "Key不存在")
	}
	return s.repo.Interfaces(ctx, id)
}

func (s *Service) ReplaceInterfaces(ctx context.Context, principal rbac.Principal, id int64, req InterfaceIDsRequest) error {
	if err := s.authz.Require(ctx, principal, "key:interface"); err != nil {
		return err
	}
	key, err := s.repo.KeyByID(ctx, id, principal.ID, hasRole(principal.Roles, "admin"))
	if err != nil {
		return mapNotFound(err, "Key not found")
	}
	ids := uniqueIDs(req.InterfaceIDs)
	if err := validateInterfaceBindingForKey(key.KeyType, ids); err != nil {
		return err
	}
	exists, err := s.repo.InterfaceIDsExist(ctx, ids)
	if err != nil {
		return err
	}
	if !exists {
		return apperrors.New(consts.StatusBadRequest, "bound interface does not exist")
	}
	if err := s.repo.ReplaceInterfaces(ctx, id, ids); err != nil {
		return err
	}
	s.keyInterfaceCache.invalidateKey(id)
	return nil
}

func (s *Service) IPRules(ctx context.Context, principal rbac.Principal, id int64) ([]string, error) {
	if err := s.authz.Require(ctx, principal, "key:ip-whitelist"); err != nil {
		return nil, err
	}
	if _, err := s.repo.KeyByID(ctx, id, principal.ID, hasRole(principal.Roles, "admin")); err != nil {
		return nil, mapNotFound(err, "Key不存在")
	}
	return s.repo.IPRules(ctx, id)
}

func (s *Service) ReplaceIPRules(ctx context.Context, principal rbac.Principal, id int64, req IPWhitelistRequest) error {
	if err := s.authz.Require(ctx, principal, "key:ip-whitelist"); err != nil {
		return err
	}
	if _, err := s.repo.KeyByID(ctx, id, principal.ID, hasRole(principal.Roles, "admin")); err != nil {
		return mapNotFound(err, "Key不存在")
	}
	rules, err := normalizeIPRules(req.IPWhitelist)
	if err != nil {
		return err
	}
	if err := s.repo.ReplaceIPRules(ctx, id, rules); err != nil {
		return err
	}
	s.keyAuthCache.invalidateKey(id)
	return nil
}

// loadOpenPreflight fetches the interface policy and optional key whitelist
// independently. Both reads are needed before a request can be admitted, but
// neither read depends on the other, so keeping them parallel avoids adding
// their database latency on every cache miss.
func loadOpenPreflight(
	ctx context.Context,
	loadAPI func(context.Context) (InterfaceConfig, error),
	loadRules func(context.Context) ([]string, error),
) (InterfaceConfig, []string, error, error) {
	var (
		api      InterfaceConfig
		rules    []string
		apiErr   error
		rulesErr error
	)
	_ = dbx.RunParallel(
		func() error {
			api, apiErr = loadAPI(ctx)
			return nil
		},
		func() error {
			rules, rulesErr = loadRules(ctx)
			return nil
		},
	)
	return api, rules, apiErr, rulesErr
}

func (s *Service) Open(ctx context.Context, apiCode string, req OpenRequest) (result OpenResult, err error) {
	ctx = requestid.Ensure(ctx)
	defer func() {
		result.TraceID = requestid.FromContext(ctx)
		if log := s.requestLogger(ctx); log != nil {
			status := result.ResponseStatus
			if err != nil {
				status = consts.StatusInternalServerError
				var business *apperrors.BusinessError
				if errors.As(err, &business) {
					status = business.StatusCode
				}
			}
			log.Info("gateway request completed",
				zap.String("api_code", apiCode),
				zap.Int("status", status),
				zap.Bool("success", err == nil && result.Success),
				zap.Bool("degraded", result.Degraded),
				zap.String("degrade_target_kind", result.DegradeTargetKind),
				zap.Int64("degrade_target_interface_id", result.DegradeTargetInterfaceID),
			)
		}
	}()
	apiCode = strings.TrimSpace(apiCode)
	if apiCode == "" || strings.Contains(apiCode, "/") {
		return OpenResult{}, apperrors.New(consts.StatusNotFound, "接口不存在")
	}
	req = cleanOpenParameters(req)
	headerSecret := strings.TrimSpace(headerValue(req.Headers, "X-API-Key"))
	querySecret, queryErr := apiKeyFromQuery(req.Query)
	if queryErr != nil {
		return OpenResult{}, queryErr
	}
	if headerSecret != "" && querySecret != "" && headerSecret != querySecret {
		return OpenResult{}, apperrors.New(consts.StatusBadRequest, "Header与Query中的API Key不一致")
	}
	secret := headerSecret
	if secret == "" {
		secret = querySecret
	}
	if secret == "" {
		return OpenResult{}, apperrors.New(consts.StatusUnauthorized, "API Key不能为空")
	}
	if len(secret) > 256 {
		return OpenResult{}, apperrors.New(consts.StatusBadRequest, "API Key长度不合法")
	}
	keyHash := secretHash(secret)
	if err := s.guardInvalidKeyLookup(ctx, apiCode, req.ClientIP, keyHash); err != nil {
		return OpenResult{}, err
	}
	key, err := s.keyAuthByHash(ctx, keyHash)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			if cacheErr := s.rememberInvalidKey(ctx, apiCode, req.ClientIP, keyHash); cacheErr != nil {
				return OpenResult{}, cacheErr
			}
			return OpenResult{}, apperrors.New(consts.StatusForbidden, "API Key无效")
		}
		return OpenResult{}, err
	}
	// 账号封禁必须先于 Key 状态判定：封禁会把 Key 一并冻结（status=0），
	// 若先判 Key 状态，被封禁的调用方只会看到「API Key已禁用」，
	// 既拿不到封禁原因，也会被误当成「用户自己关了 Key」。
	if !accountstatus.IsActive(key.UserStatus) {
		// Banned-account attempts are denied before they can become call-log
		// evidence, while the authenticated key holder receives the ban details.
		return OpenResult{}, accountstatus.Forbidden(key.BanPermanent, key.BanUntil, key.BanReason)
	}
	if key.Status != 1 {
		rejection := apperrors.New(consts.StatusForbidden, "API Key已禁用")
		s.recordKnownKeyRejection(ctx, key, apiCode, req, rejection)
		return OpenResult{}, rejection
	}
	if keyExpired(key.ExpiresAt, s.now()) {
		rejection := apperrors.New(consts.StatusForbidden, "API Key已过期")
		s.recordKnownKeyRejection(ctx, key, apiCode, req, rejection)
		return OpenResult{}, rejection
	}
	if key.Quarantined {
		// 风险隔离只冻结接口调用，不影响登录和申诉；到期自动恢复，
		// 不需要 worker 或人工解封。
		rejection := apperrors.New(consts.StatusForbidden, "账号处于风险观察期，暂不能调用接口，注册满 24 小时后自动恢复")
		s.recordKnownKeyRejection(ctx, key, apiCode, req, rejection)
		return OpenResult{}, rejection
	}
	api, rules, apiErr, rulesErr := loadOpenPreflight(
		ctx,
		func(loadCtx context.Context) (InterfaceConfig, error) {
			return s.interfaceByCode(loadCtx, apiCode)
		},
		func(loadCtx context.Context) ([]string, error) {
			if !key.HasIPWhitelist {
				return nil, nil
			}
			return s.repo.IPRules(loadCtx, key.ID)
		},
	)
	if rulesErr != nil {
		return OpenResult{}, rulesErr
	}
	if !ipAllowed(req.ClientIP, rules) {
		rejection := apperrors.New(consts.StatusForbidden, "IP不在白名单内")
		if apiErr == nil {
			s.recordOpenRejection(ctx, s.newOpenBaseResult(ctx, key, api, req), rejection)
		}
		return OpenResult{}, rejection
	}
	if apiErr != nil {
		return OpenResult{}, mapNotFound(apiErr, "接口不存在")
	}
	// 计量策略按本次调用者的用户组解析。InterfaceConfig 是值拷贝，改动只影响这一
	// 个请求，后续资产预占、套餐与免费额度闸门都直接读解析结果。
	api = api.withMeteringFor(key.RoleID)
	if err := s.decryptProxyPoolPassword(&api); err != nil {
		return OpenResult{}, apperrors.Wrap(consts.StatusServiceUnavailable, "代理配置无法解密", err)
	}
	baseResult := s.newOpenBaseResult(ctx, key, api, req)
	baseResult.Region = s.resolveIPRegion(ctx, req.ClientIP)
	if api.Status != 1 {
		rejection := apperrors.New(consts.StatusForbidden, "接口已禁用")
		s.recordOpenRejection(ctx, baseResult, rejection)
		return OpenResult{}, rejection
	}
	if key.KeyType == "INTERFACE" {
		ok, err := s.keyAllowsInterface(ctx, key.ID, api.ID)
		if err != nil {
			return OpenResult{}, err
		}
		if !ok {
			rejection := apperrors.New(consts.StatusForbidden, "API Key无权调用该接口")
			s.recordOpenRejection(ctx, baseResult, rejection)
			return OpenResult{}, rejection
		}
	}
	method, err := normalizeOpenMethod(req.Method, api.RequestMethod)
	if err != nil {
		s.recordOpenRejection(ctx, baseResult, err)
		return OpenResult{}, err
	}
	baseResult.RequestMethod = method
	validationDecision := requestvalidation.Evaluate(api.RequestValidation, requestvalidation.Input{
		Body:        req.Body,
		ContentType: headerValue(req.Headers, "Content-Type"),
		Query:       req.Query,
	})
	if rejection := requestValidationRejection(validationDecision); rejection != nil {
		baseResult.RejectionKind = requestValidationRejectionKind(validationDecision)
		if requestValidationRejectionShouldLog(validationDecision) {
			s.recordOpenRejection(ctx, baseResult, rejection)
		}
		return OpenResult{}, rejection
	}
	// Domain routing is an admission policy. Resolve it before local specified
	// responses, billing/QPS reservation, cache reads, and upstream I/O. This
	// keeps REJECT a true no-charge/no-resource-access outcome and makes a
	// configured sub-pool apply consistently to every response path.
	routedAPI, _, routeErr := resolveDomainRoute(api, req)
	if routeErr != nil {
		baseResult.RejectionKind = OpenRejectionDomainRouteMismatch
		s.recordOpenRejection(ctx, baseResult, routeErr)
		return OpenResult{}, routeErr
	}
	api = routedAPI
	// 用户组阶梯配额在计费/缓存/上游之前判定：它的目标是限制「总量」，
	// 与 QPS（并发）互补。attempt 模式在这里消费；success 模式在这里检查，
	// 成功返回后由 defer 补记。
	quotaReservation, err := s.reserveRequestQuota(ctx, key, api)
	if err != nil {
		if s.log != nil {
			s.log.Warn("gateway request quota rejected",
				zap.Int64("user_id", key.UserID),
				zap.Int64("api_key_id", key.ID),
				zap.String("role", key.RoleKey),
				zap.String("api_code", api.APICode),
				zap.String("error", masking.Error(err)),
			)
		}
		return OpenResult{}, err
	}
	defer func() {
		if err == nil && result.Success {
			s.commitRequestQuota(ctx, quotaReservation)
		}
	}()
	// 套餐承接标志决定接口配额是否适用：默认（cover_package=0）套餐优先，
	// 本次已扣套餐时整层跳过接口配额。
	packageCovered := quotaReservation.Billing.BillingRecordID > 0
	interfaceQuotaReservation, err := s.reserveInterfaceQuota(ctx, key, api, packageCovered)
	if err != nil {
		if s.log != nil {
			s.log.Warn("gateway interface quota rejected",
				zap.Int64("user_id", key.UserID),
				zap.Int64("api_key_id", key.ID),
				zap.Int64("interface_id", api.ID),
				zap.String("api_code", api.APICode),
				zap.String("error", masking.Error(err)),
			)
		}
		return OpenResult{}, err
	}
	defer func() {
		if err == nil && result.Success {
			s.commitRequestQuota(ctx, interfaceQuotaReservation)
		}
	}()
	var specified SpecifiedResponse
	if key.SpecifiedResponseEnabled == 1 {
		specified, err = s.specifiedResponse(ctx, key.UserID)
		if err != nil {
			return OpenResult{}, err
		}
	}
	if specified.Enabled == 1 {
		result := baseResult
		var reservation BillingReservation
		var qps qpsReservation
		if specified.Billable == 1 {
			reservation = quotaReservation.Billing
			if reservation.BillingRecordID == 0 {
				reservation, err = s.reserveForOpen(ctx, api, key.ID, key.UserID)
				if err != nil {
					return OpenResult{}, err
				}
			}
			qps, err = s.reserveRequestQPS(ctx, key.UserID, api, reservation)
			if err != nil {
				if releaseErr := s.releaseReservedBilling(ctx, reservation, key.UserID, "QPS limit exceeded"); releaseErr != nil {
					return OpenResult{}, releaseErr
				}
				s.recordQPSRejection(ctx, result, reservation, err)
				return OpenResult{}, err
			}
			result.applyReservation(reservation)
		} else {
			// 不收费的指定返回不应白占一次付费套餐额度。
			if quotaReservation.Billing.BillingRecordID > 0 {
				if releaseErr := s.releaseReservedBilling(ctx, quotaReservation.Billing, key.UserID, "specified response not billable"); releaseErr != nil {
					return OpenResult{}, releaseErr
				}
			}
			qps, err = s.reserveRequestQPS(ctx, key.UserID, api, BillingReservation{})
			if err != nil {
				s.recordQPSRejection(ctx, result, BillingReservation{}, err)
				return OpenResult{}, err
			}
		}
		result.Body = specified.Body
		result.ResponseSize = int64(len(specified.Body))
		result.Billable = specified.Billable == 1 && reservation.BillingRecordID > 0
		result.ContentType = "application/json; charset=utf-8"
		result.ResponseStatus = consts.StatusOK
		result.Success = true
		s.applyResponseAdvertisement(ctx, api, &result)
		if !result.Billable {
			result.BillingSnapshot = billingSnapshot(api, key.ID, key.UserID)
		}
		// 指定返回由平台直接短路生成，不代表真实上游调用。它仍遵守计费和
		// QPS 配置，但不写接口调用日志，避免污染日志列表、活跃用户和调用统计。
		if result.Billable {
			if err := s.confirmSpecifiedResponseBilling(ctx, reservation, key.UserID); err != nil {
				s.releaseReservedQPS(ctx, qps)
				return OpenResult{}, err
			}
		}
		s.updateLastUsedBestEffort(ctx, key.ID)
		return result, nil
	}
	s.recordSameParamsRiskAsync(ctx, baseResult)
	reservation := quotaReservation.Billing
	if reservation.BillingRecordID == 0 {
		reservation, err = s.reserveForOpen(ctx, api, key.ID, key.UserID)
		if err != nil {
			return OpenResult{}, err
		}
	}
	qps, err := s.reserveRequestQPS(ctx, key.UserID, api, reservation)
	if err != nil {
		if releaseErr := s.releaseReservedBilling(ctx, reservation, key.UserID, "QPS limit exceeded"); releaseErr != nil {
			return OpenResult{}, releaseErr
		}
		s.recordQPSRejection(ctx, baseResult, reservation, err)
		return OpenResult{}, err
	}
	// 降级判断放在所有准入闸门之后：只有真正会被服务（或命中缓存）的请求才
	// 计数，配额/QPS 拒绝不会消耗降级次数。目标接口仅替换上游执行配置，
	// 鉴权、计费、配额、限流和日志仍按原接口执行。
	execAPI, degradeRule, degradeConfigured, degraded := s.resolveDegradeExecution(ctx, key, api, req)
	if degradeConfigured {
		defer func() {
			s.recordDegradeAttempt(ctx, key.UserID, api, degradeRule)
		}()
	}
	start := time.Now()
	result, cacheHit := s.loadResponseCache(ctx, execAPI, method, req)
	var invokeErr error
	if !cacheHit {
		result, invokeErr = s.invokeUpstreams(ctx, execAPI, method, req, true)
		result.ElapsedMs = time.Since(start).Milliseconds()
	}
	result.APIKeyID = key.ID
	result.APICode = api.APICode
	result.applyReservation(reservation)
	result.ClientIP = req.ClientIP
	result.InterfaceID = api.ID
	result.APIKeyPrefix = key.KeyPrefix
	result.Region = baseResult.Region
	result.RequestSize = int64(len(req.Body) + len(req.RawQuery))
	result.RequestMethod = method
	result.RequestFingerprint = baseResult.RequestFingerprint
	result.RequestParams = baseResult.RequestParams
	result.RequestValue = baseResult.RequestValue
	result.TraceID = baseResult.TraceID
	result.UserID = key.UserID
	result.UserVisible = true
	result.Degraded = degraded
	if degraded {
		result.DegradeTargetKind = execAPI.degradeTargetKind
		if execAPI.degradeTargetKind == "custom" {
			// 自定义内部地址不对应任何已注册接口，日志里只记录类型，不落 URL。
			result.DegradeTargetInterfaceID = 0
			result.DegradeTargetAPICode = ""
		} else {
			result.DegradeTargetInterfaceID = execAPI.ID
			result.DegradeTargetAPICode = execAPI.APICode
		}
	}
	if invokeErr != nil {
		safeInvokeError := masking.Error(invokeErr)
		var business *apperrors.BusinessError
		if errors.As(invokeErr, &business) {
			result.markNoCharge(reservation)
			applyOpenRejection(&result, business)
			s.releaseReservedQPS(ctx, qps)
			if releaseErr := s.releaseReservedBilling(ctx, reservation, key.UserID, safeInvokeError); releaseErr != nil {
				return OpenResult{}, releaseErr
			}
			s.persistOpenRejection(ctx, result)
			return OpenResult{}, invokeErr
		}
		result.ErrorMessage = safeInvokeError
		result.Success = false
		result.markNoCharge(reservation)
		applySafeUpstreamFailureResponse(&result)
		s.releaseReservedQPS(ctx, qps)
		if err := s.createOpenLogAndRelease(ctx, result, reservation, key.UserID, safeInvokeError); err != nil {
			return OpenResult{}, err
		}
		return result, nil
	}
	billingMatched := result.Success && (api.bypassesBillingRules() || billingRulesMatch(api.BillingRules, result.Body))
	result.Billable = reservation.BillingRecordID > 0 && billingMatched
	if !cacheHit {
		// execAPI 在降级时已关闭缓存，避免目标接口响应污染源接口缓存。
		s.storeResponseCache(ctx, execAPI, method, req, &result)
	}
	s.applyResponseCacheMetadata(&result)
	s.applyResponseAdvertisement(ctx, api, &result)
	if !billingMatched {
		result.markNoCharge(reservation)
		s.releaseReservedQPS(ctx, qps)
		if err := s.createOpenLogAndRelease(ctx, result, reservation, key.UserID, "billing rule not matched"); err != nil {
			return OpenResult{}, err
		}
		s.updateLastUsedBestEffort(ctx, key.ID)
		return result, nil
	}
	if err := s.createOpenLogAndConfirm(ctx, result, reservation, key.UserID); err != nil {
		s.releaseReservedQPS(ctx, qps)
		return OpenResult{}, err
	}
	s.updateLastUsedBestEffort(ctx, key.ID)
	return result, nil
}

func (s *Service) specifiedResponse(ctx context.Context, userID int64) (SpecifiedResponse, error) {
	if s.specifiedResponseCache == nil {
		return s.repo.SpecifiedResponse(ctx, userID)
	}
	return s.specifiedResponseCache.getOrLoad(ctx, s.now(), userID, func() (SpecifiedResponse, error) {
		return s.repo.SpecifiedResponse(ctx, userID)
	})
}

// safeUpstreamFailureMessage 是网关上游调用失败时返回给调用方的统一文案，
// 与平台 5xx 约定一致（"服务暂时不可用"），不透传任何 upstream URL/内部信息。
const safeUpstreamFailureMessage = "服务暂时不可用"

func applySafeUpstreamFailureResponse(result *OpenResult) {
	if result == nil {
		return
	}
	result.ErrorMessage = masking.ErrorText(result.ErrorMessage)
	// 当上游在业务校验失败时自带了明确的错误文案（如余额不足、参数错误），
	// 直接透传该文案以方便调用方定位；否则使用统一的"服务暂时不可用"安全兜底。
	failureMessage := safeUpstreamFailureMessage
	if text := strings.TrimSpace(result.UpstreamBusinessMsg); text != "" {
		failureMessage = masking.ErrorText(text)
		if strings.TrimSpace(failureMessage) == "" {
			failureMessage = safeUpstreamFailureMessage
		}
	}
	result.ResponseStatus = consts.StatusBadGateway
	body, err := json.Marshal(map[string]any{
		"code":    -1,
		"data":    nil,
		"error":   failureMessage,
		"message": failureMessage,
	})
	if err != nil {
		body = []byte(`{"code":-1,"data":null,"error":"服务暂时不可用","message":"服务暂时不可用"}`)
	}
	result.Body = string(body)
	result.ContentType = "application/json; charset=utf-8"
	result.ResponseSize = int64(len(body))
}

// annotateRequestBudgetExhausted 把「请求预算用尽」翻译成调用方能理解的文案，
// 并记录一条可定位的错误日志。
//
// 之前这条路径只返回裸的 context.DeadlineExceeded / 各节点失败原因拼接串，
// applySafeUpstreamFailureResponse 会把它统一压成 "服务暂时不可用"，
// 调用方无法区分「上游超时」与「上游报错」，运维也看不到超时预算是多少。
//
// 文案只包含配置值与已尝试节点数量，不含上游 URL 等内部信息。
// UpstreamBusinessMsg 是 applySafeUpstreamFailureResponse 唯一会透传的字段，
// 仅在为空时填充，避免覆盖上游自带的更精确业务文案。
func (s *Service) annotateRequestBudgetExhausted(
	result *OpenResult,
	api InterfaceConfig,
	budget time.Duration,
	cause error,
	tried []string,
	attempted int,
) {
	if result == nil || cause == nil {
		return
	}
	timedOut := errors.Is(cause, context.DeadlineExceeded)
	message := "调用已被取消，请重试"
	if timedOut {
		message = fmt.Sprintf("上游响应超时：已用尽本次调用的 %dms 请求预算（已尝试 %d 个节点），请稍后重试或调高该接口的超时时间",
			budget.Milliseconds(), attempted)
	}
	if strings.TrimSpace(result.UpstreamBusinessMsg) == "" {
		result.UpstreamBusinessMsg = message
	}
	result.TriedUpstreams = append([]string(nil), tried...)
	if s.log == nil {
		return
	}
	fields := []zap.Field{
		zap.Int64("interface_id", api.ID),
		zap.String("api_code", api.APICode),
		zap.Int64("timeout_budget_ms", budget.Milliseconds()),
		zap.Int("configured_timeout_ms", api.RequestTimeoutMs),
		zap.Int("attempted_candidates", attempted),
		zap.Strings("tried_upstreams", tried),
		zap.Bool("timed_out", timedOut),
		zap.String("cause", masking.Error(cause)),
	}
	if timedOut {
		s.log.Error("gateway request budget exhausted", fields...)
		return
	}
	s.log.Warn("gateway request was canceled before completion", fields...)
}

func (s *Service) guardInvalidKeyLookup(ctx context.Context, apiCode, clientIP, keyHash string) error {
	if s.rdb == nil {
		return apperrors.New(consts.StatusServiceUnavailable, "风控服务暂时不可用")
	}
	result, err := invalidKeyGuardScript.Run(ctx, s.rdb, []string{
		s.invalidKeyAttemptKey(apiCode, clientIP),
		invalidKeyCacheKey(keyHash),
		invalidKeyMetricKey("attempts", s.currentTime()),
		invalidKeyMetricKey("blocked", s.currentTime()),
	}, invalidKeyAttemptLimit, invalidKeyAttemptTTL.Milliseconds(), int64(invalidKeyMetricTTL.Seconds())).Int64()
	if err != nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "风控服务暂时不可用", err)
	}
	if result == 2 {
		return apperrors.New(consts.StatusTooManyRequests, fmt.Sprintf("无效 API Key 尝试过于频繁：同一来源 %s 内失败已达 %d 次，请核对密钥后于 %s 后重试", invalidKeyAttemptTTL, invalidKeyAttemptLimit, invalidKeyAttemptTTL))
	}
	if result == 0 {
		return nil
	}
	return apperrors.New(consts.StatusForbidden, "API Key无效")
}

func (s *Service) rememberInvalidKey(ctx context.Context, apiCode, clientIP, keyHash string) error {
	if s.rdb == nil {
		return apperrors.New(consts.StatusServiceUnavailable, "风控服务暂时不可用")
	}
	if _, err := rememberInvalidKeyScript.Run(ctx, s.rdb, []string{
		s.invalidKeyAttemptKey(apiCode, clientIP),
		invalidKeyCacheKey(keyHash),
		invalidKeyMetricKey("attempts", s.currentTime()),
	}, invalidKeyCacheTTL.Milliseconds(), invalidKeyAttemptTTL.Milliseconds(), int64(invalidKeyMetricTTL.Seconds())).Result(); err != nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "风控服务暂时不可用", err)
	}
	return nil
}

func invalidKeyMetricKey(metric string, now time.Time) string {
	return fmt.Sprintf("gateway:risk:invalid-key:metric:%s:%d", metric, now.Unix()/60)
}

func (s *Service) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Service) invalidKeyAttemptKey(apiCode, clientIP string) string {
	mac := hmac.New(sha256.New, s.fingerprintKey)
	_, _ = mac.Write([]byte(strings.TrimSpace(clientIP) + "\x00" + strings.TrimSpace(apiCode)))
	return "gateway:risk:invalid-key:attempt:" + hex.EncodeToString(mac.Sum(nil)[:16])
}

func invalidKeyCacheKey(keyHash string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(keyHash)))
	return "gateway:risk:invalid-key:cache:" + hex.EncodeToString(sum[:16])
}

func applyOpenRejection(result *OpenResult, rejection *apperrors.BusinessError) {
	if result == nil || rejection == nil {
		return
	}
	result.Success = false
	result.Billable = false
	result.ErrorMessage = rejection.Message
	result.ResponseStatus = rejection.StatusCode
	if result.ResponseStatus <= 0 {
		result.ResponseStatus = consts.StatusBadRequest
	}
}

func (s *Service) newOpenBaseResult(ctx context.Context, key KeyAuth, api InterfaceConfig, req OpenRequest) OpenResult {
	ctx = requestid.Ensure(ctx)
	requestParams, requestFingerprint := requestArtifactsForRisk(s.fingerprintKey, req.Query, req.Body)
	return OpenResult{
		APIKeyID:           key.ID,
		APIKeyPrefix:       key.KeyPrefix,
		APICode:            api.APICode,
		BillingStatus:      "NO_CHARGE",
		ChargeAmount:       "0.0000",
		ChargeType:         "NO_CHARGE",
		ClientIP:           req.ClientIP,
		InterfaceID:        api.ID,
		PointPrice:         0,
		RequestSize:        int64(len(req.Body) + len(req.RawQuery)),
		RequestMethod:      strings.ToUpper(strings.TrimSpace(req.Method)),
		RequestFingerprint: requestFingerprint,
		RequestParams:      requestParams,
		RequestValue:       openRequestDisplay(req),
		TraceID:            requestid.FromContext(ctx),
		UserID:             key.UserID,
		UserVisible:        true,
	}
}

func (s *Service) recordOpenRejection(ctx context.Context, result OpenResult, rejection error) {
	var business *apperrors.BusinessError
	if !errors.As(rejection, &business) {
		return
	}
	applyOpenRejection(&result, business)
	s.persistOpenRejection(ctx, result)
}

func (s *Service) recordKnownKeyRejection(ctx context.Context, key KeyAuth, apiCode string, req OpenRequest, rejection error) {
	api, err := s.repo.InterfaceByCode(ctx, apiCode)
	if err != nil {
		return
	}
	s.recordOpenRejection(ctx, s.newOpenBaseResult(ctx, key, api, req), rejection)
}

func (s *Service) recordQPSRejection(ctx context.Context, result OpenResult, reservation BillingReservation, rejection error) {
	result = qpsRejectionResult(result, reservation)
	s.recordOpenRejection(ctx, result, rejection)
}

func qpsRejectionResult(result OpenResult, reservation BillingReservation) OpenResult {
	if reservation.Reserved || reservation.BillingRecordID > 0 {
		result.markNoCharge(reservation)
	}
	return result
}

func (s *Service) persistOpenRejection(ctx context.Context, result OpenResult) {
	if !shouldRecordCallLog(result) {
		return
	}
	if !s.allowOpenRejectionLog(ctx, result) {
		return
	}
	if _, err := s.repo.CreateOpenLog(ctx, result); err != nil && s.log != nil {
		s.log.Warn("record rejected open gateway request failed",
			zap.Int64("api_id", result.InterfaceID),
			zap.Int64("api_key_id", result.APIKeyID),
			zap.String("trace_id", result.TraceID),
			zap.String("request_id", result.TraceID),
			zap.String("error", masking.Error(err)),
		)
	}
}

func (s *Service) allowOpenRejectionLog(ctx context.Context, result OpenResult) bool {
	if s.rdb == nil {
		return true
	}
	globalKey, fineKey := s.openRejectionLogKeys(result)
	globalCount, err := redisutil.IncrWithTTL(ctx, s.rdb, globalKey, openRejectionRateTTL)
	if err != nil {
		s.warnOpenRejectionLimiter("open gateway rejection log limiter unavailable", err)
		return false
	}
	if globalCount > openRejectionGlobalLimit {
		return false
	}
	fineCount, err := redisutil.IncrWithTTL(ctx, s.rdb, fineKey, openRejectionRateTTL)
	if err != nil {
		s.warnOpenRejectionLimiter("open gateway rejection fingerprint limiter unavailable", err)
		return false
	}
	return fineCount <= openRejectionFineLimit
}

func (s *Service) warnOpenRejectionLimiter(message string, err error) {
	if s.log == nil || !s.acquireOpenRejectionLimiterWarning() {
		return
	}
	s.log.Warn(message, zap.String("error", masking.Error(err)))
}

func (s *Service) recordSameParamsRisk(ctx context.Context, result OpenResult) {
	if s == nil || s.rdb == nil || result.InterfaceID <= 0 || result.APIKeyID <= 0 || strings.TrimSpace(result.RequestFingerprint) == "" {
		return
	}
	riskCtx, cancel := context.WithTimeout(ctx, sameParamsRiskTimeout)
	defer cancel()
	count, thresholdHit, bucket, err := requestrisk.Count(riskCtx, s.rdb, result.InterfaceID, result.APIKeyID, result.RequestFingerprint, s.currentTime())
	if err != nil {
		if s.log != nil && s.acquireSameParamsRiskWarning() {
			s.log.Warn("same-parameter risk counter unavailable", zap.Int64("interface_id", result.InterfaceID), zap.Int64("api_key_id", result.APIKeyID), zap.String("error", masking.Error(err)))
		}
		return
	}
	if !thresholdHit || s.log == nil {
		return
	}
	s.log.Warn("same-parameter request risk threshold reached",
		zap.Int64("interface_id", result.InterfaceID),
		zap.Int64("api_key_id", result.APIKeyID),
		zap.String("fingerprint_prefix", safeFingerprintPrefix(result.RequestFingerprint)),
		zap.Int64("count", count),
		zap.String("bucket", bucket),
	)
}

// recordSameParamsRiskAsync keeps the best-effort Redis counter off the API
// request's critical path. The worker receives a bounded context that is not
// canceled when the client disconnects, so one request cannot abort a shared
// risk observation for concurrent callers.
func (s *Service) recordSameParamsRiskAsync(ctx context.Context, result OpenResult) {
	if s == nil || s.rdb == nil || result.InterfaceID <= 0 || result.APIKeyID <= 0 || strings.TrimSpace(result.RequestFingerprint) == "" {
		return
	}
	if !s.tryAcquireRiskSlot() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !s.startBackground(func() {
		defer s.releaseRiskSlot()
		workerCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sameParamsRiskTimeout)
		defer cancel()
		if s.sameParamsRiskFn != nil {
			s.sameParamsRiskFn(workerCtx, result)
			return
		}
		s.recordSameParamsRisk(workerCtx, result)
	}) {
		s.releaseRiskSlot()
	}
}

func (s *Service) acquireSameParamsRiskWarning() bool {
	nowUnixNano := s.currentTime().UnixNano()
	for {
		last := s.sameParamsRiskWarnAt.Load()
		if last > 0 && nowUnixNano-last < openRejectionWarnThrottle.Nanoseconds() {
			return false
		}
		if s.sameParamsRiskWarnAt.CompareAndSwap(last, nowUnixNano) {
			return true
		}
	}
}

func safeFingerprintPrefix(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func (s *Service) acquireOpenRejectionLimiterWarning() bool {
	now := time.Now()
	if s.now != nil {
		now = s.now()
	}
	nowUnixNano := now.UnixNano()
	for {
		last := s.rejectionLimiterWarnAt.Load()
		if last > 0 && nowUnixNano-last < openRejectionWarnThrottle.Nanoseconds() {
			return false
		}
		if s.rejectionLimiterWarnAt.CompareAndSwap(last, nowUnixNano) {
			return true
		}
	}
}

func (s *Service) openRejectionLogKeys(result OpenResult) (string, string) {
	minute := s.now().Unix() / 60
	globalKey := fmt.Sprintf("gateway:risk:reject:global:%d:%d:%d", result.APIKeyID, result.InterfaceID, minute)
	mac := hmac.New(sha256.New, s.fingerprintKey)
	_, _ = mac.Write([]byte(strings.TrimSpace(result.ClientIP)))
	ipDigest := hex.EncodeToString(mac.Sum(nil)[:8])
	fineKey := fmt.Sprintf("gateway:risk:reject:fine:%d:%d:%s:%s:%d", result.APIKeyID, result.InterfaceID, ipDigest, result.RequestFingerprint, minute)
	return globalKey, fineKey
}

func (s *Service) resolveIPRegion(ctx context.Context, ip string) ipregion.Region {
	if s.ipResolver == nil {
		return ipregion.Region{}
	}
	softCtx, cancel := context.WithTimeout(ctx, ipRegionSoftTimeout)
	defer cancel()
	region, err := s.ipResolver.Resolve(softCtx, ip)
	if err == nil {
		return region
	}
	// 解析失败（上游超时/限流/网络异常/无效 IP）：不阻塞网关、不抛错，
	// 本次返回空地区（调用日志可写空值，前端显示"未知"），并后台预热一次，
	// 让后续请求能命中 Redis 缓存（TTL 24h）。
	s.warmIPRegion(ip)
	return ipregion.Region{Provider: region.Provider}
}

func (s *Service) warmIPRegion(ip string) {
	if s.ipResolver == nil || s.ipSlots == nil || strings.TrimSpace(ip) == "" {
		return
	}
	select {
	case s.ipSlots <- struct{}{}:
		if !s.startBackground(func() {
			defer func() { <-s.ipSlots }()
			ctx, cancel := context.WithTimeout(context.Background(), ipRegionWarmTimeout)
			defer cancel()
			_, _ = s.ipResolver.Resolve(ctx, ip)
		}) {
			<-s.ipSlots
		}
	default:
	}
}

func (s *Service) reserveForOpen(ctx context.Context, api InterfaceConfig, keyID, userID int64) (BillingReservation, error) {
	reservation, err := s.repo.ReserveBilling(ctx, api, keyID, userID)
	if err == nil {
		return reservation, nil
	}
	if errors.Is(err, dbx.ErrCommitUncertain) && reservation.Reserved && reservation.BillingRecordID > 0 {
		// No upstream work has started. Abort a possibly-committed debit using
		// the existing bounded, status-guarded refund path even if ctx ended.
		if recoveryErr := s.releaseReservedBilling(ctx, reservation, userID, "reservation commit outcome uncertain"); recoveryErr != nil {
			if s.log != nil {
				s.log.Error("open gateway reservation recovery failed",
					zap.Int64("billing_record_id", reservation.BillingRecordID),
					zap.Int64("api_id", api.ID),
					zap.String("error", masking.Error(recoveryErr)),
				)
			}
			return BillingReservation{}, errors.Join(err, recoveryErr)
		}
	}
	if errors.Is(err, ErrInsufficientBalance) {
		return BillingReservation{}, apperrors.New(consts.StatusForbidden, "余额、点数或套餐不足")
	}
	return BillingReservation{}, err
}

func (s *Service) releaseReservedBilling(_ context.Context, reservation BillingReservation, userID int64, reason string) error {
	var releaseErrors error
	for attempt := 0; attempt < billingReleaseAttempts; attempt++ {
		releaseCtx, cancel := context.WithTimeout(context.Background(), billingReleaseTimeout)
		err := s.repo.ReleaseBilling(releaseCtx, reservation, userID, reason)
		cancel()
		if err == nil {
			return nil
		}
		releaseErrors = errors.Join(releaseErrors, err)
		if attempt+1 < billingReleaseAttempts {
			time.Sleep(billingReleaseBackoff)
		}
	}
	return apperrors.Wrap(consts.StatusServiceUnavailable, "billing release failed", releaseErrors)
}

func (s *Service) createOpenLogAndConfirm(ctx context.Context, result OpenResult, reservation BillingReservation, userID int64) error {
	if !shouldRecordCallLog(result) {
		if err := s.repo.ConfirmBilling(ctx, reservation, 0); err != nil {
			if releaseErr := s.releaseReservedBilling(ctx, reservation, userID, "billing confirm failed before call-log eligibility"); releaseErr != nil {
				return errors.Join(err, releaseErr)
			}
			return err
		}
		return nil
	}
	_, err := s.repo.CreateOpenLogAndConfirm(ctx, result, reservation)
	if err != nil {
		if releaseErr := s.releaseReservedBilling(ctx, reservation, userID, "call log or billing confirm failed"); releaseErr != nil {
			return errors.Join(err, releaseErr)
		}
		return err
	}
	return nil
}

func (s *Service) confirmSpecifiedResponseBilling(ctx context.Context, reservation BillingReservation, userID int64) error {
	if err := s.repo.ConfirmBilling(ctx, reservation, 0); err != nil {
		if releaseErr := s.releaseReservedBilling(ctx, reservation, userID, "specified response billing confirm failed"); releaseErr != nil {
			return errors.Join(err, releaseErr)
		}
		return err
	}
	return nil
}

func (s *Service) createOpenLogAndRelease(_ context.Context, result OpenResult, reservation BillingReservation, userID int64, reason string) error {
	if !shouldRecordCallLog(result) {
		return s.releaseReservedBilling(context.Background(), reservation, userID, reason)
	}
	// 一次轮询请求尝试了多个节点且全部失败时，合并为一条调用日志。
	// repository 层只会持久化合成的 node-N 引用，绝不保存真实地址。
	logResult := result
	if len(result.TriedUpstreams) > 1 {
		logResult.UpstreamURL = strings.Join(result.TriedUpstreams, ",")
		if logResult.ErrorMessage == "" {
			logResult.ErrorMessage = reason
		}
	}
	var finalizeErrors error
	for attempt := 0; attempt < billingReleaseAttempts; attempt++ {
		finalizeCtx, cancel := context.WithTimeout(context.Background(), billingReleaseTimeout)
		_, err := s.repo.CreateOpenLogAndRelease(finalizeCtx, logResult, reservation, reason)
		cancel()
		if err == nil {
			return nil
		}
		finalizeErrors = errors.Join(finalizeErrors, err)
		if errors.Is(err, dbx.ErrCommitUncertain) {
			break
		}
		if attempt+1 < billingReleaseAttempts {
			time.Sleep(billingReleaseBackoff)
		}
	}
	if s.log != nil {
		s.log.Error("open gateway log and billing release transaction failed",
			zap.Int64("billing_record_id", reservation.BillingRecordID),
			zap.Int64("api_id", logResult.InterfaceID),
			zap.String("trace_id", logResult.TraceID),
			zap.String("request_id", logResult.TraceID),
			zap.String("error", masking.Error(finalizeErrors)),
		)
	}
	if releaseErr := s.releaseReservedBilling(context.Background(), reservation, userID, reason); releaseErr != nil {
		return errors.Join(finalizeErrors, releaseErr)
	}
	return finalizeErrors
}

func shouldRecordCallLog(result OpenResult) bool {
	if result.RejectionKind == OpenRejectionInvalidParams ||
		result.RejectionKind == OpenRejectionPlatformMismatch ||
		result.RejectionKind == OpenRejectionDomainRouteMismatch {
		return false
	}
	return result.CallLogEligible || result.CacheHit
}

func (s *Service) reserveQPS(ctx context.Context, userID int64, reservation BillingReservation) (qpsReservation, error) {
	return s.reserveQPSLimit(ctx, s.accountQPSKey(userID), reservation.QPSLimit, qpsScopeAccount)
}

func (s *Service) reserveRequestQPS(ctx context.Context, userID int64, api InterfaceConfig, reservation BillingReservation) (qpsReservation, error) {
	// Backward-compatibility bypass: user_qps_limit = -1 means skip both the
	// interface buckets and the account-level bucket entirely.
	if api.UserQPSLimit < 0 {
		return qpsReservation{}, nil
	}

	// Role QPS and every active package are independent entitlements. The
	// effective account QPS is their maximum. The billing reservation may have
	// selected a lower-QPS package for consumption by expiry order; it must not
	// lower the QPS entitlement.
	accountQPS := reservation.QPSLimit
	if s.repo != nil {
		effective, err := s.repo.EffectiveUserQPS(ctx, userID, api.ID)
		if err != nil {
			return qpsReservation{}, err
		}
		accountQPS = effective
	}

	var keys []string
	release := func() {
		s.releaseReservedQPS(ctx, qpsReservation{Keys: keys, Counted: true, Reserved: true})
	}

	// 1. Shared bucket — interface-wide capacity shared by ALL users. It is a
	// hard interface ceiling and never participates in the "highest QPS" rule.
	if api.SharedQPSLimit > 0 {
		sharedReservation, err := s.reserveQPSLimit(ctx, s.interfaceSharedQPSKey(api.ID), api.SharedQPSLimit, qpsScopeInterfaceShared)
		if err != nil {
			return qpsReservation{}, err
		}
		if sharedReservation.Reserved && sharedReservation.Counted {
			keys = append(keys, sharedReservation.Keys...)
		}
	}

	// 2. Account bucket — role/package maximum, enforced across all interfaces.
	if accountQPS > 0 {
		accountReservation, err := s.reserveQPS(ctx, userID, BillingReservation{QPSLimit: accountQPS})
		if err != nil {
			release()
			return qpsReservation{}, err
		}
		if accountReservation.Reserved && accountReservation.Counted {
			keys = append(keys, accountReservation.Keys...)
		}
	}

	// 3. Per-interface user bucket — an additional interface cap. It does not
	// replace the account bucket, so a low interface cap still wins while a
	// higher package QPS can still be used on other interfaces.
	if api.UserQPSLimit > 0 {
		userReservation, err := s.reserveQPSLimit(ctx, s.interfaceUserQPSKey(userID, api.ID), api.UserQPSLimit, qpsScopeInterface)
		if err != nil {
			release()
			return qpsReservation{}, err
		}
		if userReservation.Reserved && userReservation.Counted {
			keys = append(keys, userReservation.Keys...)
		}
	}

	if len(keys) == 0 {
		return qpsReservation{}, nil
	}
	return qpsReservation{Keys: keys, Counted: true, Reserved: true}, nil
}

// qpsRejectionMessage 生成面向调用方的限流文案。窗口固定为 1 秒（限流键按
// Unix 秒分桶，TTL 2 秒），因此把「上限 + 单位 + 处置建议」一并写清楚，
// 避免调用方只看到一句 "QPS limit exceeded" 而无法判断该降并发还是该升配额。
func qpsRejectionMessage(scope string, limit int) string {
	switch scope {
	case qpsScopeInterfaceShared:
		return fmt.Sprintf("请求过于频繁：已达该接口共享 QPS 上限 %d 次/秒，请降低并发后重试", limit)
	case qpsScopeInterface:
		return fmt.Sprintf("请求过于频繁：已达该接口 QPS 上限 %d 次/秒，请降低并发后重试", limit)
	default:
		return fmt.Sprintf("请求过于频繁：已达账号 QPS 上限 %d 次/秒，请降低并发或升级套餐后重试", limit)
	}
}

func (s *Service) reserveQPSLimit(ctx context.Context, key string, limit int, scope string) (qpsReservation, error) {
	if limit <= 0 {
		return qpsReservation{}, nil
	}
	if s.rdb == nil {
		// 限流依赖 Redis。计数器不可用时必须 fail-closed，否则配额形同虚设。
		s.warnQPSDegraded(scope, limit, errors.New("redis client is not configured"))
		return qpsReservation{}, apperrors.New(consts.StatusServiceUnavailable, "限流服务暂时不可用（计数器不可达），请稍后重试")
	}
	allowed, err := qpsReserveScript.Run(ctx, s.rdb, []string{key}, limit, (2 * time.Second).Milliseconds()).Int64()
	if err != nil {
		s.warnQPSDegraded(scope, limit, err)
		return qpsReservation{}, apperrors.New(consts.StatusServiceUnavailable, "限流服务暂时不可用（计数器不可达），请稍后重试")
	}
	if allowed != 1 {
		return qpsReservation{}, apperrors.New(consts.StatusTooManyRequests, qpsRejectionMessage(scope, limit))
	}
	return qpsReservation{Keys: []string{key}, Counted: true, Reserved: true}, nil
}

// warnQPSDegraded 记录限流计数器不可用的原因。没有这条日志时，调用方只会看到
// 503，运维无法区分「Redis 掉线」与「业务真的不可用」。
func (s *Service) warnQPSDegraded(scope string, limit int, err error) {
	if s.log == nil || err == nil {
		return
	}
	s.log.Error("qps limiter unavailable, rejecting request fail-closed",
		zap.String("scope", scope),
		zap.Int("limit_per_second", limit),
		zap.String("error", masking.Error(err)),
	)
}

func (s *Service) accountQPSKey(userID int64) string {
	return fmt.Sprintf("openapi:quota:qps:v3:account:%d:%d", userID, s.currentTime().Unix())
}

func (s *Service) interfaceUserQPSKey(userID, interfaceID int64) string {
	return fmt.Sprintf("openapi:quota:qps:v3:interface-user:%d:%d:%d", userID, interfaceID, s.currentTime().Unix())
}

// interfaceSharedQPSKey generates a Redis key for the interface-wide shared
// QPS bucket. It deliberately omits userID so every caller contends on the
// same counter — that is what makes it a "shared" limit.
func (s *Service) interfaceSharedQPSKey(apiID int64) string {
	return fmt.Sprintf("openapi:quota:qps:v3:interface-shared:%d:%d", apiID, s.currentTime().Unix())
}

func (s *Service) releaseReservedQPS(_ context.Context, qps qpsReservation) {
	if !qps.Reserved || !qps.Counted || len(qps.Keys) == 0 || s.rdb == nil {
		return
	}
	releaseCtx, cancel := context.WithTimeout(context.Background(), qpsReleaseTimeout)
	defer cancel()
	for _, key := range qps.Keys {
		if strings.TrimSpace(key) == "" {
			continue
		}
		_, _ = redisutil.DecrFloor(releaseCtx, s.rdb, key)
	}
}

func (s *Service) updateCurrentNodeBestEffort(ctx context.Context, apiID int64, node int) {
	if apiID <= 0 || node <= 0 || !s.tryAcquireDisplaySlot() {
		return
	}
	if !s.acquireDisplayUpdate(ctx, fmt.Sprintf("gateway:display:current-node:%d", apiID)) {
		s.releaseDisplaySlot()
		return
	}
	if !s.startBackground(func() {
		defer s.releaseDisplaySlot()
		bg, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		var err error
		if s.updateCurrentNodeFn != nil {
			err = s.updateCurrentNodeFn(bg, apiID, node)
		} else if s.repo != nil {
			err = s.repo.UpdateCurrentNode(bg, apiID, node)
		}
		if err != nil && s.log != nil {
			s.log.Warn("update gateway current upstream node failed", zap.Int64("api_id", apiID), zap.String("error", masking.Error(err)))
		}
	}) {
		s.releaseDisplaySlot()
	}
}

func (s *Service) updateLastUsedBestEffort(ctx context.Context, keyID int64) {
	if keyID <= 0 || !s.tryAcquireDisplaySlot() {
		return
	}
	if !s.acquireDisplayUpdate(ctx, fmt.Sprintf("gateway:display:last-used:%d", keyID)) {
		s.releaseDisplaySlot()
		return
	}
	if !s.startBackground(func() {
		defer s.releaseDisplaySlot()
		bg, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.repo.UpdateLastUsed(bg, keyID); err != nil && s.log != nil {
			s.log.Warn("update API key last-used time failed", zap.Int64("api_key_id", keyID), zap.String("error", masking.Error(err)))
		}
	}) {
		s.releaseDisplaySlot()
	}
}

func (s *Service) startBackground(fn func()) bool {
	if fn == nil {
		return false
	}
	s.backgroundMu.Lock()
	if s.backgroundClosing {
		s.backgroundMu.Unlock()
		return false
	}
	s.backgroundWG.Add(1)
	s.backgroundMu.Unlock()
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil && s.log != nil {
				s.log.Error("gateway background task panicked", zap.String("error", masking.ErrorText(fmt.Sprint(recovered))))
			}
			s.backgroundWG.Done()
		}()
		fn()
	}()
	return true
}

func (s *Service) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.backgroundMu.Lock()
	s.backgroundClosing = true
	if s.backgroundDone == nil {
		s.backgroundDone = make(chan struct{})
	}
	done := s.backgroundDone
	s.backgroundMu.Unlock()
	s.backgroundWaitOnce.Do(func() {
		go func() {
			s.backgroundWG.Wait()
			close(done)
		}()
	})
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) acquireDisplayUpdate(ctx context.Context, key string) bool {
	if s.rdb == nil {
		return false
	}
	ok, err := s.rdb.SetNX(ctx, key, "1", displayUpdateThrottle).Result()
	return err == nil && ok
}

func (s *Service) tryAcquireDisplaySlot() bool {
	if s.displaySlots == nil {
		return false
	}
	select {
	case s.displaySlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Service) releaseDisplaySlot() {
	if s.displaySlots == nil {
		return
	}
	select {
	case <-s.displaySlots:
	default:
	}
}

func (s *Service) tryAcquireRiskSlot() bool {
	if s == nil {
		return false
	}
	s.riskSlotsOnce.Do(func() {
		if s.riskSlots == nil {
			s.riskSlots = make(chan struct{}, 128)
		}
	})
	select {
	case s.riskSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Service) releaseRiskSlot() {
	if s == nil || s.riskSlots == nil {
		return
	}
	select {
	case <-s.riskSlots:
	default:
	}
}

func (r *OpenResult) applyReservation(reservation BillingReservation) {
	r.Billable = reservation.BillingRecordID > 0
	r.BillingRecordID = reservation.BillingRecordID
	r.BillingSnapshot = reservation.Snapshot
	r.BillingStatus = "CONFIRMED"
	if reservation.BillingRecordID == 0 {
		r.BillingStatus = "NONE"
	}
	r.ChargeAmount = reservation.ChargeAmount
	r.ChargeType = reservation.ChargeType
	r.PointPrice = reservation.PointAmount
}

func (r *OpenResult) markNoCharge(reservation BillingReservation) {
	r.Billable = false
	r.BillingRecordID = reservation.BillingRecordID
	r.BillingSnapshot = reservation.Snapshot
	r.BillingStatus = "RELEASED"
	r.ChargeAmount = "0.0000"
	r.ChargeType = "NO_CHARGE"
	if reservation.BillingRecordID == 0 {
		r.BillingStatus = "NONE"
		if reservation.ChargeType == "FREE" {
			r.ChargeType = "FREE"
		}
	}
	r.PointPrice = 0
}

func (s *Service) MarketTestKey(ctx context.Context, userID, interfaceID int64) (bool, string, string, error) {
	item, ok, err := s.repo.MarketTestKey(ctx, userID, interfaceID)
	if err != nil || !ok {
		return false, "", "暂无可用Key，请先创建或绑定API Key", err
	}
	return true, item.KeyType, "", nil
}

func (s *Service) ListInvokeKeys(ctx context.Context, userID, interfaceID int64, keyword string) ([]keyaccess.InvokeKeyOption, error) {
	return s.repo.ListInvokeKeys(ctx, userID, interfaceID, keyword, 50)
}

// ValidateInvokeKey 供接口调试面板使用：校验密钥归属、状态与接口绑定关系。
// keyID 为 0 时直接放行（未选择密钥）。
func (s *Service) ValidateInvokeKey(ctx context.Context, userID, keyID, interfaceID int64) (string, string, error) {
	if keyID <= 0 {
		return "", "", nil
	}
	// 调试页选择密钥时始终按当前用户过滤。管理员可以不选择密钥直接调试，
	// 但不能借助列表权限使用其他用户的密钥。
	key, err := s.repo.KeyByID(ctx, keyID, userID, false)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", apperrors.New(consts.StatusForbidden, "密钥不存在或无权使用")
		}
		return "", "", err
	}
	if key.Status != 1 {
		return "", "", apperrors.New(consts.StatusForbidden, "密钥已禁用")
	}
	if strings.TrimSpace(key.ExpiresAt) != "" {
		expiresAt, parseErr := parseTime(key.ExpiresAt)
		if parseErr != nil {
			return "", "", parseErr
		}
		if keyExpired(expiresAt, s.now()) {
			return "", "", apperrors.New(consts.StatusForbidden, "密钥已过期")
		}
	}
	if key.KeyType == "INTERFACE" {
		allowed, err := s.repo.KeyAllowsInterface(ctx, keyID, interfaceID)
		if err != nil {
			return "", "", err
		}
		if !allowed {
			return "", "", apperrors.New(consts.StatusForbidden, "该密钥未绑定此接口")
		}
	}
	return key.KeyPrefix, key.KeyType, nil
}

func (s *Service) normalizeKey(ctx context.Context, principal rbac.Principal, req SaveKeyRequest) (APIKeyItem, *time.Time, []int64, []string, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 64 {
		return APIKeyItem{}, nil, nil, nil, apperrors.New(consts.StatusBadRequest, "Key名称不能为空且不能超过64个字符")
	}
	keyType := strings.ToUpper(strings.TrimSpace(req.KeyType))
	if keyType == "" {
		keyType = "GLOBAL"
	}
	if keyType != "GLOBAL" && keyType != "INTERFACE" {
		return APIKeyItem{}, nil, nil, nil, apperrors.New(consts.StatusBadRequest, "Key类型不合法")
	}
	status := 1
	if req.Status != nil {
		status = *req.Status
	}
	if status != 0 && status != 1 {
		return APIKeyItem{}, nil, nil, nil, apperrors.New(consts.StatusBadRequest, "Key状态不合法")
	}
	expiresAtText := ""
	if req.ExpiresAt != nil {
		expiresAtText = *req.ExpiresAt
	}
	expiresAt, err := parseTime(expiresAtText)
	if err != nil {
		return APIKeyItem{}, nil, nil, nil, apperrors.New(consts.StatusBadRequest, "过期时间格式不合法")
	}
	if err := validateKeyExpiration(expiresAt, s.now()); err != nil {
		return APIKeyItem{}, nil, nil, nil, err
	}
	interfaceIDs := uniqueIDs(req.InterfaceIDs)
	if keyType == "GLOBAL" {
		interfaceIDs = nil
	}
	if err := validateInterfaceBindingForKey(keyType, interfaceIDs); err != nil {
		return APIKeyItem{}, nil, nil, nil, err
	}
	exists, err := s.repo.InterfaceIDsExist(ctx, interfaceIDs)
	if err != nil {
		return APIKeyItem{}, nil, nil, nil, err
	}
	if !exists {
		return APIKeyItem{}, nil, nil, nil, apperrors.New(consts.StatusBadRequest, "绑定接口不存在")
	}
	ipRules, err := normalizeIPRules(req.IPWhitelist)
	if err != nil {
		return APIKeyItem{}, nil, nil, nil, err
	}
	return APIKeyItem{
		KeyType: keyType,
		Name:    name,
		Status:  status,
		UserID:  strconv.FormatInt(principal.ID, 10),
	}, expiresAt, interfaceIDs, ipRules, nil
}

func preserveOmittedKeyExpiration(req *SaveKeyRequest, current string) {
	if req == nil || req.ExpiresAt != nil || strings.TrimSpace(current) == "" {
		return
	}
	value := current
	req.ExpiresAt = &value
}

func validateInterfaceBindingForKey(keyType string, interfaceIDs []int64) error {
	switch strings.ToUpper(strings.TrimSpace(keyType)) {
	case "GLOBAL":
		if len(interfaceIDs) > 0 {
			return apperrors.New(consts.StatusBadRequest, "GLOBAL key must not bind interfaces")
		}
	case "INTERFACE":
		if len(interfaceIDs) == 0 {
			return apperrors.New(consts.StatusBadRequest, "INTERFACE key must bind at least one interface")
		}
	default:
		return apperrors.New(consts.StatusBadRequest, "Key类型不合法")
	}
	return nil
}

func (s *Service) invokeUpstreams(ctx context.Context, api InterfaceConfig, method string, req OpenRequest, resolved ...bool) (result OpenResult, err error) {
	ctx = requestid.Ensure(ctx)
	defer func() { result.TraceID = requestid.FromContext(ctx) }()
	log := s.requestLogger(ctx)
	if len(resolved) == 0 || !resolved[0] {
		routedAPI, _, routeErr := resolveDomainRoute(api, req)
		if routeErr != nil {
			status := consts.StatusServiceUnavailable
			var business *apperrors.BusinessError
			if errors.As(routeErr, &business) {
				status = business.StatusCode
			}
			return OpenResult{ResponseStatus: status}, routeErr
		}
		api = routedAPI
	}
	// The timeout is an interface-call budget shared by all polling attempts.
	// Applying it once here prevents every fallback node from receiving a fresh
	// full timeout and makes the configured limit effective for proxy refreshes
	// and response reads as well.
	timeoutBudget := requesttimeout.Duration(api.RequestTimeoutMs, 0, 1)
	requestCtx, cancel := context.WithTimeout(ctx, timeoutBudget)
	defer cancel()
	ctx = requestCtx
	// A few pre-1.2 rows were persisted with polling_enabled=0 even though
	// they contain a concrete polling mode/upstream pool (or a stop-code
	// contract). Resolve that legacy shape once at runtime so a non-terminal
	// business response cannot be accepted by the first node. An explicit
	// disabled SINGLE configuration remains an intentional opt-out.
	runtimeEnabled := pollingRuntimeEnabled(api)
	runtimeMode := normalizedPollingMode(api.PollingMode, boolInt(runtimeEnabled))
	configs := upstreamCandidates(api)
	if len(configs) == 0 {
		return OpenResult{}, errors.New("上游接口未配置")
	}
	candidates := s.orderPollingCandidates(ctx, api, runtimeMode, numberUpstreamCandidates(configs))
	if len(candidates) == 0 {
		return OpenResult{
			PollingMode:    api.PollingMode,
			ResponseStatus: consts.StatusBadGateway,
		}, errors.New("所有上游节点均在冷却期，请稍后重试")
	}
	tried := make([]string, 0, len(candidates))
	failureReasons := make([]string, 0, len(candidates))
	var last OpenResult
	var lastErr error
	for offset := 0; offset < len(candidates); offset++ {
		// Keep the interface timeout as a hard upper bound while reserving
		// half of the remaining budget for fallback candidates. Previously every
		// attempt shared the same context. A slow
		// primary could therefore consume the whole deadline and the loop would
		// return from the ctx.Err check before issuing the backup request.  The
		// dynamic split gives a slow node a bounded slice while still allowing a
		// fast failure to hand its unused time to the next node.
		// Preserve the existing cancellation result for the first candidate: a
		// canceled caller still receives the primary node's context error and
		// metadata.  Once an attempt has run, however, never start another node
		// after the parent request has been canceled.
		if offset > 0 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				last.CallLogEligible = false
				s.annotateRequestBudgetExhausted(&last, api, timeoutBudget, ctxErr, tried, offset)
				return last, ctxErr
			}
		}
		attemptCtx, attemptCancel := pollingAttemptContext(ctx, len(candidates)-offset, offset == 0)
		candidate := candidates[offset]
		cfg := candidate.Config
		res, err := s.invokeOne(attemptCtx, method, cfg, req, api.ProxyPoolConfig)
		attemptCancel()
		res.PollingMode = runtimeMode
		res.UpstreamSwitched = offset > 0
		tried = append(tried, cfg.URL)
		last = res
		// A response obtained before the parent deadline is still evaluated
		// below, even if the child slice raced its deadline.  Only the parent
		// request cancellation is terminal; a child timeout is an ordinary
		// upstream failure and should advance to the next candidate.
		parentErr := ctx.Err()
		if parentErr != nil && err != nil {
			last.CallLogEligible = false
			s.annotateRequestBudgetExhausted(&last, api, timeoutBudget, parentErr, tried, offset+1)
			return last, parentErr
		}
		var business *apperrors.BusinessError
		if errors.As(err, &business) {
			last.TriedUpstreams = tried
			return last, err
		}
		// Evaluate a configured terminal code against the raw upstream body before
		// optional response cleaning. The cleaner intentionally normalizes some
		// legacy success codes (for example code=0 -> code=200) and can rename
		// fields; using the normalized body alone could therefore miss a terminal
		// response and incorrectly return the first node as successful.
		if shouldStopPolling(api, &res) {
			res.Success = false
			// A configured business "not found" code (for example 40401 with
			// HTTP 200) is an expected empty result, not an upstream failure.
			res.OutcomeClass = calloutcome.ExpectedEmpty
			if text := firstBusinessMessage(res.Body); text != "" {
				res.UpstreamBusinessMsg = text
				res.ErrorMessage = text
			} else if stopCode := strings.TrimSpace(api.PollingStopCode); stopCode != "" {
				res.ErrorMessage = "上游返回终止轮询码 " + stopCode
			} else {
				res.ErrorMessage = "上游返回 HTTP 404"
			}
			res.TriedUpstreams = append([]string(nil), tried...)
			return res, nil
		}
		passed := false
		var cleaningErr error
		if err == nil {
			passed, cleaningErr = prepareOpenResponse(api.ResponseCleaningEnabled, effectivePollingCheck(api, cfg), &res)
		}
		if cleaningErr != nil {
			// Response mapping/cleaning is platform-side processing. A local
			// transformation failure must not reduce the upstream API's success rate.
			res.CallLogEligible = false
		}
		last = res
		// 记录轮询决策日志
		if log != nil && offset < len(candidates) {
			pollingCheckCfg := effectivePollingCheck(api, cfg)
			log.Debug("polling response validation",
				zap.Int64("interface_id", api.ID),
				zap.String("upstream_url", cfg.URL),
				zap.Int("attempt", offset+1),
				zap.Int("total_candidates", len(candidates)),
				zap.Int("response_status", res.ResponseStatus),
				zap.Bool("passed", passed),
				zap.Bool("has_error", err != nil),
				zap.Bool("has_cleaning_error", cleaningErr != nil),
				zap.Int("polling_check_enabled", pollingCheckCfg.PollingCheckEnabled),
				zap.Bool("polling_check_auto", pollingCheckCfg.pollingCheckAuto),
				zap.String("polling_check_field", pollingCheckCfg.PollingCheckField),
				zap.String("polling_check_expected", pollingCheckCfg.PollingCheckExpected),
			)
		}
		// 终止轮询判定（可配置），优先级最高：
		//   1) 默认（未配置 polling_stop_code）：上游明确返回 HTTP 404，表示资源在业务上
		//      不存在，再打备份节点结果相同 —— 作为合法业务响应透传，不计健康失败；
		//   2) 配置后：响应体 pollingCheckField（默认 code）取值等于所配置值时，
		//      同样立即终止轮询并透传当前节点响应，适用于上游用自定义业务码表达
		//      "资源不存在/不需要轮询"的场景（如 code=40401 而非 HTTP 404）。
		// 命中即透传（Success=false），且不计入健康失败/冷却，避免把业务语义误判为节点故障。
		stopPolling := shouldStopPolling(api, &res)
		if log != nil {
			log.Debug("polling stop decision",
				zap.Int64("interface_id", api.ID),
				zap.String("upstream_url", cfg.URL),
				zap.Bool("should_stop", stopPolling),
				zap.Int("response_status", res.ResponseStatus),
				zap.String("polling_stop_code", api.PollingStopCode),
			)
		}
		if stopPolling {
			res.Success = false
			res.OutcomeClass = calloutcome.ExpectedEmpty
			if text := firstBusinessMessage(res.Body); text != "" {
				res.UpstreamBusinessMsg = text
				res.ErrorMessage = text
			} else if stopCode := strings.TrimSpace(api.PollingStopCode); stopCode != "" {
				res.ErrorMessage = "上游返回终止轮询码 " + stopCode
			} else {
				res.ErrorMessage = "上游返回 HTTP 404"
			}
			res.TriedUpstreams = append([]string(nil), tried...)
			return res, nil
		}
		if shouldRecord, healthy := classifyUpstreamHealth(res, err, cleaningErr, passed); shouldRecord {
			// Health persistence is maintenance work.  Keep it out of the request
			// critical path so a slow Redis write cannot consume the remaining
			// budget reserved for the next polling candidate.
			s.recordUpstreamHealthAsync(api.ID, cfg.URL, healthy, res.ElapsedMs, api.domainRouteID)
		}
		if !passed {
			failureReasons = append(failureReasons, fmt.Sprintf("节点%d：%s", candidate.Node, upstreamFailureReason(res, err, cleaningErr, passed)))
		}
		if cleaningErr != nil {
			lastErr = cleaningErr
			continue
		}
		if passed {
			// 只有「正确数据」（HTTP 2xx 且响应校验通过）才提前返回。
			// 404/其它 4xx、5xx、网络错误均视为失败，继续尝试下一个候选节点，
			// 直到轮询完所有节点（紧急故障转移语义：一节点不返回正确数据就换下一节点）。
			if log != nil {
				log.Info("polling succeeded on candidate",
					zap.Int64("interface_id", api.ID),
					zap.String("upstream_url", cfg.URL),
					zap.Int("attempt", offset+1),
					zap.Int("total_candidates", len(candidates)),
					zap.Int("response_status", res.ResponseStatus),
					zap.Strings("tried_upstreams", tried),
				)
			}
			res.Success = httpstatus.IsSuccess(res.ResponseStatus)
			// Keep the complete attempt history on successful fallback as well as
			// on exhausted/terminal responses.  The repository turns this internal
			// list into safe node labels for call-log diagnostics; without it a
			// successful switch looked like a primary-only call.
			res.TriedUpstreams = append([]string(nil), tried...)
			if runtimeMode == "ROUND_ROBIN" || runtimeMode == "FASTEST" {
				if api.domainRouteID == "" {
					s.updateCurrentNodeBestEffort(ctx, api.ID, candidate.Node)
				}
			}
			return res, nil
		}
		if text := firstBusinessMessage(res.Body); text != "" {
			res.UpstreamBusinessMsg = text
			last = res
		}
		// 记录当前节点失败，准备尝试下一个节点
		if log != nil && !passed && err == nil && cleaningErr == nil {
			log.Warn("polling candidate failed validation, trying next",
				zap.Int64("interface_id", api.ID),
				zap.String("upstream_url", cfg.URL),
				zap.Int("attempt", offset+1),
				zap.Int("remaining_candidates", len(candidates)-offset-1),
				zap.Int("response_status", res.ResponseStatus),
				zap.String("failure_reason", upstreamFailureReason(res, err, cleaningErr, passed)),
			)
		}
		if err != nil {
			lastErr = err
			continue
		}
		lastErr = errors.New(upstreamValidationMessage(&res))
	}
	if lastErr == nil {
		lastErr = errors.New("上游服务暂时不可用")
	}
	if last.ResponseStatus == 0 {
		last.ResponseStatus = consts.StatusBadGateway
	}
	// 记录所有候选节点均失败的情况。
	// 上游 HTTP 404 是「资源不存在」的正常业务结果（网关按语义透传给调用方，
	// 且不计节点健康失败），不是平台故障，因此降级为 Info，避免错误日志被
	// 404 噪音淹没；其余状态仍按 Error 记录。
	if log != nil {
		if last.ResponseStatus == consts.StatusNotFound {
			log.Info("polling exhausted with upstream 404",
				zap.Int64("interface_id", api.ID),
				zap.String("api_code", api.APICode),
				zap.Int("total_candidates", len(candidates)),
				zap.Strings("tried_upstreams", tried),
				zap.Strings("failure_reasons", failureReasons),
				zap.Int("final_status", last.ResponseStatus),
			)
		} else {
			log.Error("all polling candidates failed",
				zap.Int64("interface_id", api.ID),
				zap.String("api_code", api.APICode),
				zap.Int("total_candidates", len(candidates)),
				zap.Strings("tried_upstreams", tried),
				zap.Strings("failure_reasons", failureReasons),
				zap.String("final_error", masking.Error(lastErr)),
				zap.Int("final_status", last.ResponseStatus),
			)
		}
	}
	// 全部候选均未返回正确数据。优先返回「最后有真实 HTTP 响应的节点」，
	// 保证（例如上游 404）的响应码/响应体可透传给调用方，而不是一律 502。
	// 仅当所有节点都是网络/构造错误（无任何 HTTP 状态）时才回退 502。
	if last.ResponseStatus == 0 {
		last.ResponseStatus = consts.StatusBadGateway
	}
	// invokeOne 按 HTTP 状态码乐观地置位 Success，用于单节点语义。轮询耗尽时
	// 这个值必须被清除：HTTP 2xx 但业务校验未通过的响应不是成功调用，
	// 否则忽略 error 返回值的调用方会把它当成成功而计费/缓存。
	last.Success = false
	last.TriedUpstreams = tried
	if len(failureReasons) > 0 {
		lastErr = errors.New(strings.Join(failureReasons, "; "))
	}
	// 候选耗尽的最常见原因就是请求预算被慢上游吃干。此时 failureReasons 里只有
	// 一串 "context deadline exceeded"，压成安全文案后调用方只能看到
	// "服务暂时不可用"。这里补上「超时 + 预算值」的可读原因并记录错误日志。
	if ctxErr := ctx.Err(); ctxErr != nil {
		s.annotateRequestBudgetExhausted(&last, api, timeoutBudget, ctxErr, tried, len(tried))
		lastErr = ctxErr
	}
	return last, lastErr
}

// pollingAttemptContext returns a child context whose deadline is the next
// bounded share of the parent request's remaining budget.  The parent context is
// always kept as the hard upper bound, so a caller cancellation or the overall
// interface timeout can never be extended by fallback attempts.
//
// A fresh child is created for each candidate.  This is important because a
// child timeout caused by one unhealthy node must not make a subsequent node
// inherit an already-expired context.  The split is dynamic: if a node fails
// quickly, its unused time remains available to later candidates.
func pollingAttemptContext(parent context.Context, remainingCandidates int, firstAttempt bool) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if remainingCandidates <= 1 {
		// The final candidate uses the parent timer directly. A second timer at
		// the same deadline can fire first and hide the overall timeout cause.
		return context.WithCancel(parent)
	}
	deadline, ok := parent.Deadline()
	if !ok {
		// This fallback is defensive; invokeUpstreams always supplies a parent
		// deadline, but retaining cancellation semantics makes the helper safe
		// for direct tests and future callers.
		return context.WithCancel(parent)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.WithDeadline(parent, deadline)
	}
	// Give the preferred first candidate half the budget regardless of pool
	// size; unused backups must not shorten a healthy primary's deadline.
	// Thereafter share the fallback budget evenly, rather than repeatedly
	// halving it and starving the last candidates in a large pool.
	divisor := remainingCandidates
	if firstAttempt {
		divisor = 2
	}
	budget := remaining / time.Duration(divisor)
	if budget <= 0 {
		budget = remaining
	}
	attemptDeadline := time.Now().Add(budget)
	if attemptDeadline.After(deadline) {
		attemptDeadline = deadline
	}
	return context.WithDeadline(parent, attemptDeadline)
}

// upstreamFailureReason 汇总单个节点失败原因，统一并入多节点逗号/分号分隔的错误信息。
func upstreamFailureReason(res OpenResult, invokeErr, cleaningErr error, passed bool) string {
	if invokeErr != nil {
		var timeout interface{ Timeout() bool }
		if errors.Is(invokeErr, context.DeadlineExceeded) || (errors.As(invokeErr, &timeout) && timeout.Timeout()) {
			return "上游响应超时"
		}
		if errors.Is(invokeErr, context.Canceled) {
			return "调用已取消"
		}
		return masking.Error(invokeErr)
	}
	if cleaningErr != nil {
		return masking.Error(cleaningErr)
	}
	if !httpstatus.IsSuccess(res.ResponseStatus) {
		message := fmt.Sprintf("上游返回 HTTP %d", res.ResponseStatus)
		if text := firstBusinessMessage(res.Body); text != "" {
			message += ": " + masking.ErrorText(text)
		}
		return message
	}
	if !passed {
		if text := firstBusinessMessage(res.Body); text != "" {
			return "业务响应校验失败: " + masking.ErrorText(text)
		}
		return "业务响应校验失败（响应字段缺失、业务码不匹配或响应为空）"
	}
	return ""
}

// shouldStopPolling 决定是否终止轮询并透传当前节点响应（合法业务语义，不计健康失败）。
//
//  1. 默认（未配置 PollingStopCode）：HTTP 404 或轮询响应体中明确的业务
//     code=404 都表示资源不存在，立即透传。
//  2. 配置 PollingStopCode 后：改为「响应体 PollingCheckField（默认 code）取值等于所
//     配置值」即终止。该模式由后台配置驱动（如 code=40401），允许上游用自定义业务码
//     表达"资源不存在"，比 HTTP 404 更通用；此时不再对 HTTP 404 特判。
func shouldStopPolling(api InterfaceConfig, result *OpenResult) bool {
	if result == nil {
		return false
	}
	stopCode := strings.TrimSpace(api.PollingStopCode)
	if stopCode == "" {
		if result.ResponseStatus == consts.StatusNotFound {
			return true
		}
		// Legacy rows frequently omitted a stop-code value. For a polling
		// interface, an explicit business code=404 is the equivalent terminal
		// response; non-polling interfaces keep their historical behavior.
		if !pollingRuntimeEnabled(api) {
			return false
		}
		actual, ok := valueAtPath(resultBody(result), defaultText(api.PollingCheckField, "code"))
		return ok && fmt.Sprintf("%v", actual) == "404"
	}
	actual, ok := valueAtPath(resultBody(result), defaultText(api.PollingCheckField, "code"))
	return ok && fmt.Sprintf("%v", actual) == stopCode
}

// effectivePollingCheck resolves the response policy for a candidate node.
// Explicit node/global checks retain strict semantics. Older rows often have
// polling enabled while the response-check toggle is zero; those rows still
// inspect an explicitly present business code so code=500 cannot make the
// first node win. Bodies without a code field remain pass-through payloads.
func effectivePollingCheck(api InterfaceConfig, cfg UpstreamConfig) UpstreamConfig {
	if cfg.PollingCheckEnabled == 1 {
		return cfg
	}
	// A zero-valued node check is an inherit marker whenever the interface
	// enables response validation or configures a terminal business code.
	if api.PollingCheckEnabled != 1 && strings.TrimSpace(api.PollingStopCode) == "" {
		if pollingRuntimeEnabled(api) {
			cfg.PollingCheckEnabled = 1
			cfg.pollingCheckAuto = true
			cfg.PollingCheckField = defaultText(strings.TrimSpace(api.PollingCheckField), "code")
			cfg.PollingCheckExpected = defaultText(strings.TrimSpace(api.PollingCheckExpected), "200")
		}
		return cfg
	}
	cfg.PollingCheckEnabled = 1
	// A disabled per-node check is not an override. Config persistence fills its
	// empty values with code/200, so retaining those values here would silently
	// ignore a global rule such as meta.status=0 on every backup node.
	cfg.PollingCheckField = defaultText(strings.TrimSpace(api.PollingCheckField), "code")
	cfg.PollingCheckExpected = defaultText(strings.TrimSpace(api.PollingCheckExpected), "200")
	return cfg
}

func (s *Service) invokeOne(ctx context.Context, method string, cfg UpstreamConfig, payload OpenRequest, proxyConfigs ...proxypool.Config) (result OpenResult, err error) {
	ctx = requestid.Ensure(ctx)
	defer func() { result.TraceID = requestid.FromContext(ctx) }()
	log := s.requestLogger(ctx)
	values := templateValues(payload)
	target, used, err := applyTemplate(cfg.URL, values)
	if err != nil {
		status := consts.StatusBadGateway
		var business *apperrors.BusinessError
		if errors.As(err, &business) {
			status = business.StatusCode
		}
		return OpenResult{UpstreamURL: cfg.URL, ResponseStatus: status}, err
	}
	target, err = appendQuery(target, payload.Query, used)
	if err != nil {
		return OpenResult{UpstreamURL: target, ResponseStatus: consts.StatusBadGateway}, err
	}
	newRequest := func() (*http.Request, error) {
		var body io.Reader
		if method == http.MethodPost {
			body = bytes.NewReader(payload.Body)
			if len(payload.Body) == 0 {
				body = bytes.NewReader([]byte("{}"))
			}
		}
		request, requestErr := http.NewRequestWithContext(ctx, method, target, body)
		if requestErr != nil {
			return nil, requestErr
		}
		copyForwardHeaders(request, payload.Headers, payload.ClientIP)
		request.Header.Set(requestid.Header, requestid.FromContext(ctx))
		if method == http.MethodPost && request.Header.Get("Content-Type") == "" {
			request.Header.Set("Content-Type", "application/json")
		}
		return request, nil
	}
	client := s.httpClient
	var proxyConfig proxypool.Config
	var resolver *proxypool.Resolver
	proxyEnabled := len(proxyConfigs) > 0 && proxyConfigs[0].Enabled == 1
	apiProxy := false
	if proxyEnabled {
		proxyConfig = proxyConfigs[0]
		apiProxy = strings.EqualFold(strings.TrimSpace(proxyConfig.Source), proxypool.SourceAPI)
		resolver = s.proxyResolver
		if resolver == nil {
			resolver = proxypool.NewResolver(s.httpClient)
		}
		client, err = resolver.Client(ctx, s.httpClient, proxyConfig)
		if err != nil {
			return OpenResult{ResponseStatus: consts.StatusBadGateway, UpstreamURL: target}, fmt.Errorf("resolve upstream proxy: %w", err)
		}
	}
	start := time.Now()
	for attempt := 0; attempt < 2; attempt++ {
		request, requestErr := newRequest()
		if requestErr != nil {
			return OpenResult{UpstreamURL: target, ResponseStatus: consts.StatusBadGateway}, requestErr
		}
		if log != nil {
			log.Debug("gateway upstream request", zap.String("method", method), zap.Int("attempt", attempt+1))
		}
		resp, requestErr := client.Do(request)
		if requestErr != nil {
			result := OpenResult{
				CallLogEligible: ctx.Err() == nil,
				ElapsedMs:       time.Since(start).Milliseconds(),
				ResponseStatus:  consts.StatusBadGateway,
				UpstreamURL:     target,
			}
			if attempt == 0 && apiProxy && ctx.Err() == nil && resolver != nil {
				client, err = resolver.ClientFresh(ctx, s.httpClient, proxyConfig)
				if err == nil {
					continue
				}
				return result, fmt.Errorf("refresh upstream proxy after connection failure: %w", err)
			}
			return result, requestErr
		}
		data, readErr := httpclient.ReadBody(resp.Body, httpclient.MaxUpstreamResponseBytes)
		_ = resp.Body.Close()
		if readErr != nil {
			return OpenResult{
				CallLogEligible: true,
				ElapsedMs:       time.Since(start).Milliseconds(),
				ResponseStatus:  consts.StatusBadGateway,
				UpstreamURL:     target,
			}, fmt.Errorf("read upstream response: %w", readErr)
		}
		return OpenResult{
			Body:            string(data),
			CallLogEligible: true,
			ContentType:     resp.Header.Get("Content-Type"),
			ElapsedMs:       time.Since(start).Milliseconds(),
			ResponseSize:    int64(len(data)),
			ResponseStatus:  resp.StatusCode,
			Success:         httpstatus.IsSuccess(resp.StatusCode),
			UpstreamURL:     target,
		}, nil
	}
	return OpenResult{ResponseStatus: consts.StatusBadGateway, UpstreamURL: target}, errors.New("upstream proxy retry exhausted")
}

func upstreamCandidates(api InterfaceConfig) []UpstreamConfig {
	base := UpstreamConfig{URL: api.RequestURL, PollingCheckEnabled: api.PollingCheckEnabled, PollingCheckField: api.PollingCheckField, PollingCheckExpected: api.PollingCheckExpected, ResponseFieldMappings: api.ResponseFieldMappings}
	enabled := pollingRuntimeEnabled(api)
	if !enabled {
		return []UpstreamConfig{base}
	}
	switch normalizedPollingMode(api.PollingMode, boolInt(enabled)) {
	case "PRIMARY":
		return append([]UpstreamConfig{base}, api.UpstreamConfigs...)
	case "FASTEST":
		return append([]UpstreamConfig{base}, api.UpstreamConfigs...)
	case "ROUND_ROBIN":
		if len(api.UpstreamConfigs) > 0 {
			return api.UpstreamConfigs
		}
	}
	return []UpstreamConfig{base}
}

// normalizedPollingMode applies the same normalization used by the interface
// administration service to values loaded from older/manual database rows.
// An enabled interface with an empty mode historically means ROUND_ROBIN;
// disabled interfaces retain SINGLE semantics.
func normalizedPollingMode(value string, enabled int) string {
	mode := strings.ToUpper(strings.TrimSpace(value))
	if mode == "" {
		if enabled == 1 {
			return "ROUND_ROBIN"
		}
		return "SINGLE"
	}
	if mode == "RANDOM" {
		return "ROUND_ROBIN"
	}
	return mode
}

// pollingRuntimeEnabled repairs legacy/incomplete polling documents at the
// invocation boundary.  The explicit SINGLE mode is the only unambiguous
// opt-out when stale upstream rows are still present; an omitted mode with a
// pool, a concrete non-SINGLE mode, or a stop-code contract all express an
// intent to evaluate/fallback across nodes even if the historical enable bit
// was left at zero.
func pollingRuntimeEnabled(api InterfaceConfig) bool {
	if api.PollingEnabled == 1 {
		return true
	}
	mode := strings.ToUpper(strings.TrimSpace(api.PollingMode))
	if mode == "SINGLE" {
		return false
	}
	if strings.TrimSpace(api.PollingStopCode) != "" {
		return true
	}
	switch mode {
	case "PRIMARY", "FASTEST", "ROUND_ROBIN", "RANDOM":
		return true
	case "":
		return len(api.UpstreamConfigs) > 0
	default:
		return false
	}
}

func (s *Service) orderCandidatesByHealth(ctx context.Context, apiID int64, configs []UpstreamConfig) []UpstreamConfig {
	numbered := s.orderNumberedCandidatesByHealth(ctx, apiID, numberUpstreamCandidates(configs))
	ordered := make([]UpstreamConfig, 0, len(numbered))
	for _, candidate := range numbered {
		ordered = append(ordered, candidate.Config)
	}
	return ordered
}

func (s *Service) orderNumberedCandidatesByHealth(ctx context.Context, apiID int64, candidates []numberedUpstreamCandidate) []numberedUpstreamCandidate {
	configs := make([]UpstreamConfig, len(candidates))
	for index := range candidates {
		configs[index] = candidates[index].Config
	}
	metrics, ok := s.readUpstreamHealth(ctx, apiID, configs)
	if !ok {
		return append([]numberedUpstreamCandidate(nil), candidates...)
	}
	for _, metric := range metrics {
		if metric.Err != nil {
			s.warnUpstreamHealth("read", apiID, metric.Err)
		}
	}
	return orderNumberedCandidatesByHealthMetrics(candidates, metrics, s.currentTime())
}

// orderNumberedCandidatesByHealthMetrics applies a health snapshot that has
// already been read by the caller. Keeping the sorting operation separate from
// the Redis read lets the request path reuse one snapshot for both health
// ordering and cooldown classification; a second HGETALL pipeline used to
// consume enough of a short request deadline to prevent fallback polling.
func orderNumberedCandidatesByHealthMetrics(candidates []numberedUpstreamCandidate, metrics []upstreamhealth.ReadResult, now time.Time) []numberedUpstreamCandidate {
	if len(candidates) == 0 {
		return nil
	}
	type candidate struct {
		value     numberedUpstreamCandidate
		hasMetric bool
		metric    upstreamhealth.Metric
	}
	items := make([]candidate, 0, len(candidates))
	for index, value := range candidates {
		metric := upstreamhealth.ReadResult{}
		if index < len(metrics) {
			metric = metrics[index]
		}
		items = append(items, candidate{value: value, hasMetric: metric.Found && metric.Err == nil, metric: metric.Metric})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return upstreamhealth.CompareAt(items[i].metric, items[i].hasMetric, items[j].metric, items[j].hasMetric, now) < 0
	})
	ordered := make([]numberedUpstreamCandidate, 0, len(items))
	for _, item := range items {
		ordered = append(ordered, item.value)
	}
	return ordered
}

type numberedUpstreamCandidate struct {
	Config UpstreamConfig
	Node   int
}

func numberUpstreamCandidates(configs []UpstreamConfig) []numberedUpstreamCandidate {
	result := make([]numberedUpstreamCandidate, len(configs))
	for index, config := range configs {
		result[index] = numberedUpstreamCandidate{Config: config, Node: index + 1}
	}
	return result
}

func orderRoundRobinHealthCandidates(
	candidates []numberedUpstreamCandidate,
	metrics []upstreamhealth.ReadResult,
	now time.Time,
	pick func(rank, size int) int,
) []numberedUpstreamCandidate {
	if len(candidates) == 0 {
		return nil
	}
	ranks := make([]int, len(candidates))
	for index := range candidates {
		var metric upstreamhealth.Metric
		found := false
		if index < len(metrics) {
			item := metrics[index]
			found = item.Found && item.Err == nil
			metric = item.Metric
		}
		ranks[index] = upstreamhealth.Rank(metric, found, now)
	}
	order := make([]int, len(candidates))
	for index := range order {
		order[index] = index
	}
	sort.SliceStable(order, func(i, j int) bool {
		return ranks[order[i]] < ranks[order[j]]
	})
	result := make([]numberedUpstreamCandidate, 0, len(candidates))
	start := 0
	for start < len(order) {
		rank := ranks[order[start]]
		end := start + 1
		for end < len(order) && ranks[order[end]] == rank {
			end++
		}
		group := order[start:end]
		offset := 0
		if pick != nil && len(group) > 0 {
			offset = pick(rank, len(group)) % len(group)
			if offset < 0 {
				offset += len(group)
			}
		}
		for step := 0; step < len(group); step++ {
			result = append(result, candidates[group[(step+offset)%len(group)]])
		}
		start = end
	}
	return result
}

func orderRoundRobinHealthGroups(
	configs []UpstreamConfig,
	metrics []upstreamhealth.ReadResult,
	now time.Time,
	pick func(rank, size int) int,
) []UpstreamConfig {
	numbered := orderRoundRobinHealthCandidates(numberUpstreamCandidates(configs), metrics, now, pick)
	result := make([]UpstreamConfig, 0, len(numbered))
	for _, candidate := range numbered {
		result = append(result, candidate.Config)
	}
	return result
}

func (s *Service) readUpstreamHealth(ctx context.Context, apiID int64, configs []UpstreamConfig, routeScope ...string) ([]upstreamhealth.ReadResult, bool) {
	if s == nil || s.rdb == nil || apiID <= 0 || len(configs) == 0 {
		return nil, false
	}
	urls := make([]string, len(configs))
	for index, config := range configs {
		urls[index] = config.URL
	}
	// Health metadata is advisory.  Bound this Redis operation to a fair slice
	// of the remaining request budget so a slow/unavailable Redis cannot prevent
	// the actual upstream polling chain from issuing even its first request.
	readCtx, cancel := pollingHealthOperationContext(ctx, len(configs))
	defer cancel()
	metrics, err := upstreamhealth.ReadManyScoped(readCtx, s.rdb, apiID, optionalGatewayRouteScope(routeScope), urls)
	if err != nil {
		s.warnUpstreamHealth("read", apiID, err)
		return nil, false
	}
	return metrics, true
}

// pollingHealthOperationContext returns a short child context for advisory
// health reads/counters.  When the caller has a deadline, at most one fair
// share of the remaining time (plus one share reserved for the upstream
// candidates) is spent on metadata.  A no-deadline caller still receives the
// fixed health timeout.  The parent cancellation remains authoritative.
func pollingHealthOperationContext(parent context.Context, candidateCount int) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if candidateCount < 1 {
		candidateCount = 1
	}
	budget := upstreamHealthReadTimeout
	if deadline, ok := parent.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.WithDeadline(parent, deadline)
		}
		fair := remaining / time.Duration(candidateCount+1)
		if fair < budget {
			budget = fair
		}
		if budget <= 0 {
			budget = time.Nanosecond
		}
	}
	return context.WithTimeout(parent, budget)
}

func (s *Service) orderPollingCandidates(ctx context.Context, api InterfaceConfig, runtimeMode string, candidates []numberedUpstreamCandidate) []numberedUpstreamCandidate {
	if len(candidates) == 0 {
		return nil
	}
	// There is no useful ordering/circuit-breaker decision for a single node.
	// In particular, do not spend the request budget on a Redis health read only
	// to put that same node back into the candidate list.  A single configured
	// node must always receive a probe (including while its advisory cooldown is
	// active).
	if len(candidates) == 1 {
		return append([]numberedUpstreamCandidate(nil), candidates...)
	}
	configs := make([]UpstreamConfig, len(candidates))
	for index := range candidates {
		configs[index] = candidates[index].Config
	}
	// Read health once per request.  The previous implementation read the same
	// Redis pipeline while sorting and again while stripping cooldown nodes;
	// under a short interface timeout that duplicated I/O could consume the
	// entire budget before the first upstream request was issued.
	metrics, healthOK := s.readUpstreamHealth(ctx, api.ID, configs, api.domainRouteID)
	now := s.currentTime()
	var ordered []numberedUpstreamCandidate
	switch runtimeMode {
	case "PRIMARY":
		backups := append([]numberedUpstreamCandidate(nil), candidates[1:]...)
		if healthOK {
			backups = orderNumberedCandidatesByHealthMetrics(backups, healthMetricsRange(metrics, 1, len(candidates)), now)
		}
		ordered = append([]numberedUpstreamCandidate{candidates[0]}, backups...)
	case "FASTEST":
		ordered = append([]numberedUpstreamCandidate(nil), candidates...)
		if healthOK {
			ordered = orderNumberedCandidatesByHealthMetrics(ordered, metrics, now)
		}
	case "ROUND_ROBIN":
		if !healthOK {
			ordered = append([]numberedUpstreamCandidate(nil), candidates...)
			break
		}
		bestRank := int(^uint(0) >> 1)
		bestRankSize := 0
		for index := range candidates {
			item := upstreamhealth.ReadResult{}
			if index < len(metrics) {
				item = metrics[index]
			}
			metric := item.Metric
			found := item.Found && item.Err == nil
			rank := upstreamhealth.Rank(metric, found, now)
			if rank < bestRank {
				bestRank = rank
				bestRankSize = 1
			} else if rank == bestRank {
				bestRankSize++
			}
		}
		bestRankOffset := 0
		if bestRankSize > 1 {
			counterCtx, counterCancel := pollingHealthOperationContext(ctx, len(candidates))
			next, err := redisutil.IncrWithTTL(counterCtx, s.rdb, gatewayRoundRobinKey(api, bestRank), roundRobinCounterTTL)
			counterCancel()
			if err != nil {
				s.warnUpstreamHealth("round-robin counter", api.ID, err)
				ordered = append([]numberedUpstreamCandidate(nil), candidates...)
				break
			}
			bestRankOffset = int((next - 1) % int64(bestRankSize))
		}
		ordered = orderRoundRobinHealthCandidates(candidates, metrics, now, func(rank, _ int) int {
			if rank == bestRank {
				return bestRankOffset
			}
			return 0
		})
	default:
		ordered = append([]numberedUpstreamCandidate(nil), candidates...)
	}
	if !healthOK {
		return ordered
	}
	// Cooldown is an ordering hint, not a hard circuit breaker.  Re-map the
	// one original snapshot to the sorted candidate order before classifying it;
	// using the sorted index directly would associate a node's metric with a
	// different URL and could intermittently skip the wrong fallback.
	return orderCooldownMetrics(ordered, healthMetricsForCandidates(ordered, metrics), now)
}

func healthMetricsRange(metrics []upstreamhealth.ReadResult, start, end int) []upstreamhealth.ReadResult {
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	result := make([]upstreamhealth.ReadResult, end-start)
	for index := range result {
		source := start + index
		if source >= 0 && source < len(metrics) {
			result[index] = metrics[source]
		}
	}
	return result
}

func healthMetricsForCandidates(candidates []numberedUpstreamCandidate, metrics []upstreamhealth.ReadResult) []upstreamhealth.ReadResult {
	result := make([]upstreamhealth.ReadResult, len(candidates))
	for index, candidate := range candidates {
		source := candidate.Node - 1
		if source >= 0 && source < len(metrics) {
			result[index] = metrics[source]
		}
	}
	return result
}

// stripCooldownCandidates 剔除正处于失败冷却期的候选节点，保留健康/未知/过期冷却节点。
func (s *Service) stripCooldownCandidates(ctx context.Context, api InterfaceConfig, candidates []numberedUpstreamCandidate) []numberedUpstreamCandidate {
	if len(candidates) == 0 {
		return nil
	}
	configs := make([]UpstreamConfig, len(candidates))
	for index := range candidates {
		configs[index] = candidates[index].Config
	}
	metrics, ok := s.readUpstreamHealth(ctx, api.ID, configs, api.domainRouteID)
	if !ok {
		return append([]numberedUpstreamCandidate(nil), candidates...)
	}
	return orderCooldownMetrics(candidates, metrics, s.currentTime())
}

// orderCooldownMetrics keeps ready candidates (healthy, unknown, or expired
// cooldown) ahead of active-cooldown candidates while retaining the latter as
// a last-resort fallback.  A cooldown is a traffic-shedding hint, not a hard
// circuit breaker: if a ready node returns a non-terminal business code, the
// request must still be able to probe a cooling backup in the same polling
// chain.  This also guarantees at least one half-open probe when every node is
// cooling.
func orderCooldownMetrics(candidates []numberedUpstreamCandidate, metrics []upstreamhealth.ReadResult, now time.Time) []numberedUpstreamCandidate {
	if len(candidates) == 0 {
		return nil
	}
	ready := make([]numberedUpstreamCandidate, 0, len(candidates))
	cooling := make([]numberedUpstreamCandidate, 0, len(candidates))
	for index, candidate := range candidates {
		isCooling := false
		if index < len(metrics) {
			item := metrics[index]
			isCooling = item.Found && item.Err == nil && upstreamhealth.InCooldown(item.Metric, now)
		}
		if isCooling {
			cooling = append(cooling, candidate)
			continue
		}
		ready = append(ready, candidate)
	}
	return append(ready, cooling...)
}

// stripCooldownMetrics remains as a small compatibility helper for callers
// that need only the ready subset (for example, diagnostics/tests).  Runtime
// polling uses orderCooldownMetrics above so cooling nodes are appended as
// fallback candidates instead of being silently discarded.
func stripCooldownMetrics(candidates []numberedUpstreamCandidate, metrics []upstreamhealth.ReadResult, now time.Time) []numberedUpstreamCandidate {
	if len(candidates) == 0 {
		return nil
	}
	kept := make([]numberedUpstreamCandidate, 0, len(candidates))
	for index, candidate := range candidates {
		if index < len(metrics) {
			item := metrics[index]
			if item.Found && item.Err == nil && upstreamhealth.InCooldown(item.Metric, now) {
				continue
			}
		}
		kept = append(kept, candidate)
	}
	return kept
}

func classifyUpstreamHealth(result OpenResult, invokeErr, cleaningErr error, passed bool) (record, healthy bool) {
	if cleaningErr != nil {
		return false, false
	}
	if invokeErr != nil {
		return true, false
	}
	// Configured/default stop responses return before health classification.
	// Any response reaching this point did not match the stop condition, so a
	// 404, another non-2xx response, or a business validation miss is a node
	// failure and must contribute to the one-hour cooldown threshold.
	if passed && httpstatus.IsSuccess(result.ResponseStatus) {
		return true, true
	}
	return true, false
}

func (s *Service) recordUpstreamHealth(ctx context.Context, apiID int64, upstreamURL string, healthy bool, latencyMs int64, routeScope ...string) {
	if s == nil {
		return
	}
	if ctx != nil && ctx.Err() != nil {
		return
	}
	baseCtx := context.Background()
	if ctx != nil {
		// Preserve context values for tracing while detaching cancellation and
		// deadlines owned by the client request.
		baseCtx = context.WithoutCancel(ctx)
	}
	writeCtx, cancel := context.WithTimeout(baseCtx, upstreamHealthWriteTimeout)
	defer cancel()
	scope := optionalGatewayRouteScope(routeScope)
	var err error
	if scope != "" && s.healthRecordScopedFn != nil {
		err = s.healthRecordScopedFn(writeCtx, apiID, scope, upstreamURL, healthy, latencyMs)
	} else if s.healthRecordFn != nil {
		err = s.healthRecordFn(writeCtx, apiID, upstreamURL, healthy, latencyMs)
	} else if scope != "" {
		err = upstreamhealth.RecordScoped(writeCtx, s.rdb, apiID, scope, upstreamURL, healthy, latencyMs)
	} else {
		err = upstreamhealth.Record(writeCtx, s.rdb, apiID, upstreamURL, healthy, latencyMs)
	}
	if err != nil {
		s.warnUpstreamHealth("record", apiID, err)
	}
}

// recordUpstreamHealthAsync persists a health sample without extending the
// caller's latency.  The write gets its own short timeout and is tracked by
// the service background lifecycle.  A bounded slot pool prevents a degraded
// Redis from turning high request volume into unbounded goroutines.
func (s *Service) recordUpstreamHealthAsync(apiID int64, upstreamURL string, healthy bool, latencyMs int64, routeScope ...string) {
	if s == nil {
		return
	}
	if s.healthSlots != nil {
		select {
		case s.healthSlots <- struct{}{}:
		default:
			return
		}
	}
	started := s.startBackground(func() {
		if s.healthSlots != nil {
			defer func() { <-s.healthSlots }()
		}
		s.recordUpstreamHealth(context.Background(), apiID, upstreamURL, healthy, latencyMs, routeScope...)
	})
	if !started && s.healthSlots != nil {
		<-s.healthSlots
	}
}

func optionalGatewayRouteScope(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func gatewayRoundRobinKey(api InterfaceConfig, rank int) string {
	if scope := strings.TrimSpace(api.domainRouteID); scope != "" {
		return fmt.Sprintf("gateway:rr:%d:route:%s:rank:%d", api.ID, scope, rank)
	}
	return fmt.Sprintf("gateway:rr:%d:rank:%d", api.ID, rank)
}

func (s *Service) warnUpstreamHealth(operation string, apiID int64, err error) {
	if s.log == nil || err == nil {
		return
	}
	s.log.Warn("upstream health metric failure", zap.String("operation", operation), zap.Int64("api_id", apiID), zap.String("error", masking.Error(err)))
}

func nodeIndexForAPI(api InterfaceConfig, upstreamURL string) int {
	if strings.TrimSpace(upstreamURL) == strings.TrimSpace(api.RequestURL) {
		return 1
	}
	for index, cfg := range api.UpstreamConfigs {
		if strings.TrimSpace(cfg.URL) == strings.TrimSpace(upstreamURL) {
			return index + 2
		}
	}
	return 1
}

func normalizeOpenMethod(actual, allowed string) (string, error) {
	method := strings.ToUpper(strings.TrimSpace(actual))
	if method != http.MethodGet && method != http.MethodPost {
		return "", apperrors.New(consts.StatusMethodNotAllowed, "请求方式不支持")
	}
	allowed = strings.ToUpper(strings.TrimSpace(allowed))
	if allowed == "GET_POST" || allowed == method {
		return method, nil
	}
	return "", apperrors.New(consts.StatusMethodNotAllowed, "请求方式不支持")
}

func requestValidationRejection(decision requestvalidation.Decision) *apperrors.BusinessError {
	switch decision.Result {
	case requestvalidation.ResultInvalidParams:
		return apperrors.New(consts.StatusBadRequest, "请求参数不合规")
	case requestvalidation.ResultPlatformMismatch:
		return apperrors.New(consts.StatusUnprocessableEntity, "解析参数与该平台不匹配")
	default:
		return nil
	}
}

func requestValidationRejectionShouldLog(decision requestvalidation.Decision) bool {
	switch decision.Result {
	case requestvalidation.ResultInvalidParams, requestvalidation.ResultPlatformMismatch:
		return false
	default:
		return true
	}
}

func requestValidationRejectionKind(decision requestvalidation.Decision) OpenRejectionKind {
	switch decision.Result {
	case requestvalidation.ResultInvalidParams:
		return OpenRejectionInvalidParams
	case requestvalidation.ResultPlatformMismatch:
		return OpenRejectionPlatformMismatch
	default:
		return OpenRejectionNone
	}
}

func applyTemplate(raw string, values map[string]string) (string, map[string]bool, error) {
	used := map[string]bool{}
	missing := ""
	result := placeholderPattern.ReplaceAllStringFunc(raw, func(match string) string {
		name := strings.TrimSuffix(strings.TrimPrefix(match, "{"), "}")
		value, ok := values[name]
		if !ok || strings.TrimSpace(value) == "" {
			missing = name
			return match
		}
		used[name] = true
		return url.PathEscape(value)
	})
	if missing != "" {
		return "", used, apperrors.New(consts.StatusBadRequest, fmt.Sprintf("缺少接口参数: %s", missing))
	}
	return result, used, nil
}

func appendQuery(target string, query map[string][]string, used map[string]bool) (string, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return "", err
	}
	values := parsed.Query()
	for key, items := range query {
		if strings.EqualFold(key, "key") || used[key] {
			continue
		}
		for _, item := range items {
			values.Add(key, item)
		}
	}
	parsed.RawQuery = values.Encode()
	return parsed.String(), nil
}

func responsePasses(cfg UpstreamConfig, status int, body string) bool {
	// 仅 2xx 视为成功通过；3xx/4xx/5xx 均不通过（与成功率口径一致，避免把 3xx 透传给调用方）。
	if !httpstatus.IsSuccess(status) {
		return false
	}
	if cfg.PollingCheckEnabled != 1 {
		return true
	}
	actual, ok := valueAtPath(body, defaultText(cfg.PollingCheckField, "code"))
	if !ok {
		if cfg.pollingCheckAuto {
			// Automatic compatibility checks only reject malformed structured
			// payloads. Plain text and valid JSON that does not expose a business
			// code remain valid for APIs whose response shape is not an envelope.
			return optionalPollingFieldAbsent(body)
		}
		return false
	}
	if cfg.pollingCheckAuto {
		return automaticBusinessCodePasses(actual, defaultText(cfg.PollingCheckExpected, "200"))
	}
	return fmt.Sprintf("%v", actual) == defaultText(cfg.PollingCheckExpected, "200")
}

// optionalPollingFieldAbsent 判断「自动兼容模式下响应体没有业务码字段」时能否算通过。
//
// 该判定只服务于历史遗留行（轮询已启用但没有显式打开响应校验开关）。为满足
// 「非终止响应必须继续轮询」的语义，这里对三类明确不可用的响应一律判为不通过，
// 让调用方切到下一个候选节点：
//
//  1. 空响应体：HTTP 2xx 但没有任何内容，无法作为业务成功数据透传；
//  2. 以 { 或 [ 开头但 JSON 非法：截断/损坏的结构化响应；
//  3. 顶层显式携带错误信号的 JSON 对象（error/errmsg 非空、success=false）。
//
// 纯文本、HTML、合法 JSON 数组以及不含错误信号的合法 JSON 对象仍然放行——这些
// 接口的响应本身不是 code 信封，不能因为缺少 code 字段就判定失败。
func optionalPollingFieldAbsent(body string) bool {
	trimmed := strings.TrimSpace(body)
	// 空响应体在 2xx 下同样视为不可用数据，必须继续轮询下一个节点。
	if trimmed == "" {
		return false
	}
	// 非结构化响应（纯文本 / HTML / 二进制文本）本来就不含 code 字段，放行。
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return true
	}
	// 声称是 JSON 却无法解析：截断或损坏的响应，继续轮询。
	if !json.Valid([]byte(trimmed)) {
		return false
	}
	// JSON 数组是合法的数据载荷，不做错误信号检查。
	if trimmed[0] == '[' {
		return true
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil {
		// json.Valid 已通过却无法解析成对象：保守放行，交由后续清洗/透传处理。
		return true
	}
	return !hasExplicitErrorSignal(object)
}

// hasExplicitErrorSignal 仅识别「上游明确表达失败」的顶层字段，不做数值猜测。
// 例如 status=0 在不同上游既可能是成功也可能是失败，因此不参与判定；
// 业务码语义统一交给 automaticBusinessCodePasses 处理。
func hasExplicitErrorSignal(object map[string]any) bool {
	for _, key := range []string{"error", "errmsg", "errMsg", "error_msg", "errorMessage"} {
		if value, ok := object[key]; ok && errorSignalPresent(value) {
			return true
		}
	}
	if value, ok := object["success"]; ok {
		switch typed := value.(type) {
		case bool:
			return !typed
		case string:
			return strings.EqualFold(strings.TrimSpace(typed), "false")
		}
	}
	return false
}

// errorSignalPresent 判断 error 类字段是否携带了真实错误内容。
// 空串、null、空对象、空数组、字面量 "null"/"{}" 均视为「没有错误」。
func errorSignalPresent(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		text := strings.TrimSpace(typed)
		return text != "" && text != "null" && text != "{}" && text != "[]"
	case map[string]any:
		return len(typed) > 0
	case []any:
		return len(typed) > 0
	default:
		return true
	}
}

// automaticBusinessCodePasses accepts the success conventions already used by
// the response normalizer while rejecting arbitrary error codes. A configured
// expected value remains authoritative for non-default policies.
func automaticBusinessCodePasses(actual any, expected string) bool {
	actualText := strings.TrimSpace(fmt.Sprintf("%v", actual))
	if actualText == expected {
		return true
	}
	if expected != "200" {
		return false
	}
	switch strings.ToLower(actualText) {
	case "0", "1", "ok", "success", "succeeded", "true":
		return true
	}
	if number, err := strconv.Atoi(actualText); err == nil {
		return number >= 200 && number < 300
	}
	return false
}

// upstreamValidationMessage 返回上游业务校验失败时的可读文案。优先透传上游
// 响应体自带的 msg/message/error 字段，避免用户只看到一条笼统的"校验失败"。
func upstreamValidationMessage(result *OpenResult) string {
	if text := firstBusinessMessage(resultBody(result)); text != "" {
		return text
	}
	return "上游响应校验失败"
}

func resultBody(result *OpenResult) string {
	if result == nil {
		return ""
	}
	return result.Body
}

// firstBusinessMessage 从上游 JSON 响应中提取最常见的业务错误文案，供网关在
// 校验失败时透传给调用方。仅在响应本身阐述了业务错误时返回非空。
func firstBusinessMessage(body string) string {
	if strings.TrimSpace(body) == "" {
		return ""
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return ""
	}
	for _, key := range []string{"msg", "message"} {
		if text := scalarText(root[key]); text != "" {
			return text
		}
	}
	if errObject, ok := root["error"].(map[string]any); ok {
		if text := scalarText(errObject["message"]); text != "" {
			return text
		}
	}
	if text := scalarText(root["error"]); text != "" {
		return text
	}
	return ""
}

// scalarText 把 JSON 叶子值（字符串/数字）转成可读文案，空串、null、{} 视为无。
func scalarText(value any) string {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if text == "" || text == "null" || text == "{}" {
			return ""
		}
		return text
	case json.Number:
		return fmt.Sprintf("%v", typed)
	case float64:
		return fmt.Sprintf("%v", typed)
	default:
		return ""
	}
}

// prepareOpenResponse keeps upstream health checks on the raw response and
// applies the optional short-video JSON contract only after a candidate is
// otherwise usable. The normalized body is then used by billing, caching,
// advertisements, logs, and the public response.
func prepareOpenResponse(cleaningEnabled int, cfg UpstreamConfig, result *OpenResult) (bool, error) {
	if result == nil || !httpstatus.IsSuccess(result.ResponseStatus) {
		return false, nil
	}
	result.ResponseNormalized = false
	rawPassed := responsePasses(cfg, result.ResponseStatus, result.Body)
	if cleaningEnabled != 1 {
		return rawPassed, nil
	}
	rawBody := result.Body
	originalSize := result.ResponseSize
	if originalSize <= 0 {
		originalSize = int64(len(rawBody))
	}
	mapped, err := jsoncleaning.RenameFields([]byte(result.Body), cfg.ResponseFieldMappings)
	if err != nil {
		return false, fmt.Errorf("上游响应字段映射失败: %w", err)
	}
	normalized, err := videojson.NormalizeWithResult(mapped.Body)
	if err != nil {
		return false, fmt.Errorf("上游响应清洗失败: %w", err)
	}
	normalizedBody := string(normalized.Body)
	normalizedPassed := responsePasses(cfg, result.ResponseStatus, normalizedBody)
	if !pollingResponsePassesAfterCleaning(cfg, rawBody, normalizedBody, normalized.IsJSON, rawPassed, normalizedPassed) {
		return false, nil
	}
	result.Body = normalizedBody
	if normalized.IsJSON {
		result.ContentType = "application/json; charset=utf-8"
		// response_size is the original upstream body size. Keep it separate
		// from the normalized body length used for persistence.
		result.ResponseSize = originalSize
		result.ResponseNormalized = true
	}
	return true, nil
}

// pollingResponsePassesAfterCleaning combines the legacy/raw response check
// with the canonical response produced by the optional cleaner.  A normalized
// envelope must win when it discovers a business field that was absent from
// the raw body (otherwise an automatic "field optional" check could accept
// code=500 and skip the fallback node).  Conversely, keep an explicit raw
// success such as code=0 when a configured rule intentionally expects that
// legacy value; normalization rewrites it to code=200 for the public contract.
func pollingResponsePassesAfterCleaning(cfg UpstreamConfig, rawBody, normalizedBody string, normalizedIsJSON bool, rawPassed, normalizedPassed bool) bool {
	if cfg.PollingCheckEnabled != 1 || !normalizedIsJSON {
		return rawPassed || normalizedPassed
	}
	// A canonical envelope can reveal a concrete upstream failure even when
	// the raw wrapper looked successful (for example {code:200,payload:{status:
	// 500}}).  Such a failure must always win over rawPassed; otherwise polling
	// stops on a node whose nested response is explicitly unsuccessful.
	if !normalizedPassed && normalizedResponseExposesFailure(cfg, normalizedBody) {
		return false
	}
	field := defaultText(cfg.PollingCheckField, "code")
	_, rawHasField := valueAtPath(rawBody, field)
	if !rawHasField {
		// The cleaner may have exposed a nested status/code in its canonical
		// envelope.  In that case only the normalized policy result is valid.
		return normalizedPassed
	}
	if rawPassed {
		return true
	}
	// The normalizer accepts the common legacy success conventions (0, 1,
	// 2xx, ok/success) and rewrites them to canonical code=200.  Permit that
	// representation change, but never allow an explicit raw failure code to
	// be rescued by a generic normalized envelope.
	actual, ok := valueAtPath(rawBody, field)
	if !ok || defaultText(cfg.PollingCheckExpected, "200") != "200" {
		return false
	}
	return normalizedPassed && automaticBusinessCodePasses(actual, "200")
}

// normalizedResponseExposesFailure reports only explicit failure values.  It
// intentionally does not classify arbitrary strings as failures because a
// caller may configure a non-standard expected value (for example "pending")
// that the normalizer leaves untouched.  Numeric HTTP/business error codes
// and the common failure tokens are unambiguous.
func normalizedResponseExposesFailure(cfg UpstreamConfig, body string) bool {
	paths := []string{"code"}
	field := defaultText(cfg.PollingCheckField, "code")
	if field != "code" {
		paths = append(paths, field)
	}
	for _, path := range paths {
		actual, ok := valueAtPath(body, path)
		if !ok {
			continue
		}
		text := strings.ToLower(strings.TrimSpace(fmt.Sprintf("%v", actual)))
		switch text {
		case "false", "error", "err", "fail", "failed", "failure":
			return true
		case "true", "ok", "success", "succeeded":
			continue
		}
		number, err := strconv.Atoi(text)
		if err != nil {
			continue
		}
		// 0/1 and the 2xx range are common success conventions. Everything
		// else is an explicit numeric failure for the canonical envelope.
		if number != 0 && number != 1 && (number < 200 || number >= 300) {
			return true
		}
	}
	return false
}

// billingRulesMatch 判定一次调用是否应当计费。每个接口可在后台单独配置
// 「计费管理」规则（bp_interface_billing_rule）。规则集为空时，接口处于
// 「未配置计费」状态：既不产生计费记录（bp_billing_record）、不扣减余额/积分/套餐，
// 调用日志的 billable 也置为 false，避免在管理员未明确启用计费前误扣用户费用。
// 计费规则配置后，只有全部规则均命中才视为计费命中（与历史行为一致）。
func billingRulesMatch(rules []BillingRule, body string) bool {
	if len(rules) == 0 {
		return false
	}
	for _, rule := range rules {
		value, ok := valueAtPath(body, rule.FieldName)
		if !ok {
			return false
		}
		actual := fmt.Sprintf("%v", value)
		expected := rule.ExpectedValue
		switch rule.Operator {
		case "EQ":
			if actual != expected {
				return false
			}
		case "NE":
			if actual == expected {
				return false
			}
		case "CONTAINS":
			if !strings.Contains(actual, expected) {
				return false
			}
		case "GT", "LT":
			left, lerr := decimalvalue.Parse(actual)
			right, rerr := decimalvalue.Parse(expected)
			if lerr != nil || rerr != nil {
				return false
			}
			comparison := left.Cmp(right)
			if rule.Operator == "GT" && comparison <= 0 {
				return false
			}
			if rule.Operator == "LT" && comparison >= 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func valueAtPath(body, path string) (any, bool) {
	return jsonpathvalue.Lookup(body, path)
}

func templateValues(req OpenRequest) map[string]string {
	values := map[string]string{}
	for key, items := range req.Query {
		if strings.EqualFold(key, "key") {
			continue
		}
		if len(items) > 0 {
			values[key] = items[0]
		}
	}
	var body map[string]any
	if len(req.Body) > 0 && json.Unmarshal(req.Body, &body) == nil {
		for key, value := range body {
			values[key] = fmt.Sprintf("%v", value)
		}
	}
	return values
}

func copyForwardHeaders(request *http.Request, headers map[string]string, clientIP string) {
	blocked := map[string]bool{
		"accept-encoding":     true,
		"authorization":       true,
		"cf-connecting-ip":    true,
		"connection":          true,
		"content-length":      true,
		"cookie":              true,
		"forwarded":           true,
		"host":                true,
		"keep-alive":          true,
		"proxy":               true,
		"proxy-authenticate":  true,
		"proxy-authorization": true,
		"proxy-connection":    true,
		"te":                  true,
		"trailer":             true,
		"transfer-encoding":   true,
		"true-client-ip":      true,
		"upgrade":             true,
		"via":                 true,
		"x-api-key":           true,
		"x-client-ip":         true,
		"x-cluster-client-ip": true,
		"x-forwarded-for":     true,
		"x-forwarded-host":    true,
		"x-forwarded-port":    true,
		"x-forwarded-proto":   true,
		"x-forwarded-server":  true,
		"x-real-ip":           true,
	}
	for name, value := range headers {
		if !strings.EqualFold(name, "Connection") {
			continue
		}
		for _, token := range strings.Split(value, ",") {
			blocked[strings.ToLower(strings.TrimSpace(token))] = true
		}
	}
	for key, value := range headers {
		name := strings.ToLower(strings.TrimSpace(key))
		if blocked[name] || strings.TrimSpace(value) == "" || strings.ContainsAny(value, "\r\n") {
			continue
		}
		request.Header.Set(key, value)
	}
	if ip := net.ParseIP(strings.TrimSpace(clientIP)); ip != nil {
		request.Header.Set("X-Forwarded-For", ip.String())
		request.Header.Set("X-Real-IP", ip.String())
	}
}

func apiKeyFromQuery(query map[string][]string) (string, error) {
	secret := ""
	for name, values := range query {
		if !strings.EqualFold(name, "key") {
			continue
		}
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if secret != "" && value != secret {
				return "", apperrors.New(consts.StatusBadRequest, "API Key参数存在冲突")
			}
			secret = value
		}
	}
	return secret, nil
}

func headerValue(headers map[string]string, key string) string {
	for name, value := range headers {
		if strings.EqualFold(name, key) {
			return value
		}
	}
	return ""
}

func billingSnapshot(api InterfaceConfig, keyID, userID int64) string {
	if api.BillingLogDisabled {
		return ""
	}
	data, _ := json.Marshal(BillingContext{
		APIID:        api.ID,
		APICode:      api.APICode,
		BillingRules: api.BillingRules,
		KeyID:        keyID,
		PointPrice:   api.PointPrice,
		Price:        api.Price,
		UserID:       userID,
	})
	return string(data)
}

func requestParamsForLog(query map[string][]string, body []byte) string {
	params, _ := requestArtifactsForRisk(nil, query, body)
	return params
}

func deriveFingerprintKey(secret string) []byte {
	seed := sha256.Sum256([]byte("BugPk-Api:risk-request-fingerprint:v1\x00" + secret))
	return seed[:]
}

func requestFingerprintForRisk(key []byte, query map[string][]string, body []byte) string {
	_, fingerprint := requestArtifactsForRisk(key, query, body)
	return fingerprint
}

func requestArtifactsForRisk(key []byte, query map[string][]string, body []byte) (string, string) {
	fingerprintBody, logBody := bodyArtifacts(body)
	canonicalQuery := queryForLog(query)
	canonical := compactJSON(map[string]any{
		"body":  fingerprintBody,
		"query": canonicalQuery,
	})
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(canonical))
	masked := masking.SensitiveValue(map[string]any{
		"body":  logBody,
		"query": canonicalQuery,
	})
	return compactJSON(masked), hex.EncodeToString(mac.Sum(nil))
}

func bodyArtifacts(body []byte) (any, any) {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil, nil
	}
	if parsed, err := decodeUnambiguousJSON(body); err == nil {
		return normalizeJSONForFingerprint(parsed), parsed
	}
	if strings.Contains(string(body), "=") {
		if values, err := url.ParseQuery(string(body)); err == nil && len(values) > 0 {
			result := make(map[string][]string, len(values))
			for name, items := range values {
				copied := append([]string(nil), items...)
				sort.Strings(copied)
				result[name] = copied
			}
			return result, result
		}
	}
	return string(body), nonStructuredBodyRedact
}

func decodeUnambiguousJSON(body []byte) (any, error) {
	return jsonrequest.DecodeUnambiguous(body, maxOpenJSONNestingDepth)
}

type canonicalJSONNumber string

func (n canonicalJSONNumber) MarshalJSON() ([]byte, error) {
	return []byte(n), nil
}

func normalizeJSONForFingerprint(value any) any {
	switch typed := value.(type) {
	case json.Number:
		return canonicalJSONNumber(canonicalizeJSONNumber(typed.String()))
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = normalizeJSONForFingerprint(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = normalizeJSONForFingerprint(item)
		}
		return result
	default:
		return value
	}
}

func canonicalizeJSONNumber(raw string) string {
	raw = strings.TrimSpace(raw)
	negative := strings.HasPrefix(raw, "-")
	if negative {
		raw = raw[1:]
	}
	exponent := new(big.Int)
	if index := strings.IndexAny(raw, "eE"); index >= 0 {
		if _, ok := exponent.SetString(raw[index+1:], 10); !ok {
			return "0"
		}
		raw = raw[:index]
	}
	fractionDigits := 0
	if index := strings.IndexByte(raw, '.'); index >= 0 {
		fractionDigits = len(raw) - index - 1
		raw = raw[:index] + raw[index+1:]
	}
	digits := strings.TrimLeft(raw, "0")
	if digits == "" {
		return "0"
	}
	trimmedZeros := len(digits) - len(strings.TrimRight(digits, "0"))
	if trimmedZeros > 0 {
		digits = digits[:len(digits)-trimmedZeros]
	}
	exponent.Sub(exponent, big.NewInt(int64(fractionDigits)))
	exponent.Add(exponent, big.NewInt(int64(trimmedZeros)))
	if negative {
		digits = "-" + digits
	}
	return digits + "e" + exponent.String()
}

func queryForLog(query map[string][]string) map[string][]string {
	result := map[string][]string{}
	for key, values := range query {
		if strings.EqualFold(key, "key") {
			continue
		}
		copied := append([]string(nil), values...)
		sort.Strings(copied)
		result[key] = copied
	}
	return result
}

// openRequestDisplay 生成"链接或值"列展示的请求值（Query 优先，其次请求体）。
// 历史缺陷曾把客户端 IP 写入 request_value，导致该列与"调用IP"列显示相同值；
// 此处与后台管理员调试调用的 requestDisplay 语义保持一致。
func openRequestDisplay(req OpenRequest) string {
	if len(req.Query) > 0 {
		return masking.SensitiveJSONText(compactJSON(queryForLog(req.Query)))
	}
	body := bytes.TrimSpace(req.Body)
	if len(body) == 0 {
		return ""
	}
	if parsed, err := decodeUnambiguousJSON(body); err == nil {
		return masking.SensitiveJSONText(compactJSON(parsed))
	}
	return masking.Text(string(body))
}

func compactJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func normalizeIPRules(values []string) ([]string, error) {
	seen := map[string]bool{}
	var result []string
	for _, value := range values {
		rule := strings.TrimSpace(value)
		if rule == "" || seen[rule] {
			continue
		}
		if strings.Contains(rule, "/") {
			if _, _, err := net.ParseCIDR(rule); err != nil {
				return nil, apperrors.New(consts.StatusBadRequest, "IP白名单格式不合法")
			}
		} else if net.ParseIP(rule) == nil {
			return nil, apperrors.New(consts.StatusBadRequest, "IP白名单格式不合法")
		}
		seen[rule] = true
		result = append(result, rule)
	}
	if len(result) > 100 {
		return nil, apperrors.New(consts.StatusBadRequest, "IP白名单不能超过100条")
	}
	return result, nil
}

func ipAllowed(clientIP string, rules []string) bool {
	if len(rules) == 0 {
		return true
	}
	ip := net.ParseIP(strings.TrimSpace(clientIP))
	if ip == nil {
		return false
	}
	for _, rule := range rules {
		if strings.Contains(rule, "/") {
			_, network, err := net.ParseCIDR(rule)
			if err == nil && network.Contains(ip) {
				return true
			}
			continue
		}
		if allowed := net.ParseIP(rule); allowed != nil && allowed.Equal(ip) {
			return true
		}
	}
	return false
}

func uniqueIDs(values []int64) []int64 {
	seen := map[int64]bool{}
	var result []int64
	for _, value := range values {
		if value <= 0 || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func parseTime(value string) (*time.Time, error) {
	text := strings.TrimSpace(value)
	if text == "" {
		return nil, nil
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02", time.RFC3339} {
		parsed, err := time.ParseInLocation(layout, text, time.Local)
		if err == nil {
			return &parsed, nil
		}
	}
	return nil, fmt.Errorf("invalid time")
}

func validateKeyExpiration(expiresAt *time.Time, now time.Time) error {
	if expiresAt != nil && !expiresAt.After(now) {
		return apperrors.New(consts.StatusBadRequest, "过期时间必须晚于当前时间")
	}
	return nil
}

func keyExpired(expiresAt *time.Time, now time.Time) bool {
	return expiresAt != nil && !expiresAt.After(now)
}

func newSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "bp_live_" + hex.EncodeToString(buf), nil
}

func secretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func secretPrefix(secret string) string {
	if len(secret) <= 12 {
		return secret
	}
	return secret[:12]
}

func secretSuffix(secret string) string {
	if len(secret) <= 4 {
		return secret
	}
	return secret[len(secret)-4:]
}

func hasRole(roles []string, role string) bool {
	for _, item := range roles {
		if item == role {
			return true
		}
	}
	return false
}

func defaultText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func mapNotFound(err error, message string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return apperrors.New(consts.StatusNotFound, message)
	}
	return err
}

func mapDBError(err error, message string) error {
	if err == nil {
		return nil
	}
	return apperrors.Wrap(consts.StatusBadRequest, message, err)
}
