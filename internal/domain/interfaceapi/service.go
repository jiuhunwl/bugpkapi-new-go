package interfaceapi

import (
	"bytes"
	"context"
	"database/sql"
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
	"time"
	"unicode/utf8"

	"bugpk-api/internal/domain/keyaccess"
	"bugpk-api/internal/domain/rbac"
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
	"bugpk-api/internal/pkg/requesttimeout"
	"bugpk-api/internal/pkg/requestvalidation"
	"bugpk-api/internal/pkg/secretbox"
	"bugpk-api/internal/pkg/upload"
	"bugpk-api/internal/pkg/upstreamhealth"
	"bugpk-api/internal/pkg/videojson"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

type Service struct {
	repo             *Repository
	authz            *rbac.Service
	localizer        i18n.Localizer
	systemPrefix     string
	httpClient       *http.Client
	proxyResolver    *proxypool.Resolver
	proxyTestURL     string
	keyProvider      MarketKeyProvider
	log              *zap.Logger
	rdb              *redis.Client
	interfaceChanged func(oldAPICode, newAPICode string)
	// healthRecordFn is an optional private test hook; production writes
	// metrics through upstreamhealth.Record.
	healthRecordFn func(context.Context, int64, string, bool, int64) error
	// healthRecordScopedFn is the route-aware test hook. The legacy hook above
	// remains available for existing default-pool tests and callers.
	healthRecordScopedFn func(context.Context, int64, string, string, bool, int64) error
}

func (s *Service) SetHTTPClient(client *http.Client) {
	if client != nil {
		s.httpClient = client
		s.proxyResolver = proxypool.NewResolver(client)
	}
}

func (s *Service) SetProxyTestURL(value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = defaultProxyTestURL
	}
	s.proxyTestURL = value
}

func (s *Service) SetLogger(log *zap.Logger) {
	s.log = log
}

// SetLocalizer 为公共市场接口接入后端数据翻译。
func (s *Service) SetLocalizer(localizer i18n.Localizer) {
	s.localizer = localizer
}

// SetInterfaceChanged registers a callback invoked after an interface write
// succeeds. The callback receives the previous and current API codes; either
// value may be empty for create/delete operations. It is used by the gateway
// to invalidate its in-process configuration cache immediately.
func (s *Service) SetInterfaceChanged(callback func(oldAPICode, newAPICode string)) {
	if s == nil {
		return
	}
	s.interfaceChanged = callback
}

func (s *Service) notifyInterfaceChanged(oldAPICode, newAPICode string) {
	if s == nil || s.interfaceChanged == nil {
		return
	}
	oldAPICode = strings.TrimSpace(oldAPICode)
	newAPICode = strings.TrimSpace(newAPICode)
	if oldAPICode == "" && newAPICode == "" {
		return
	}
	s.interfaceChanged(oldAPICode, newAPICode)
}

func (s *Service) notifyAllInterfacesChanged() {
	if s == nil || s.interfaceChanged == nil {
		return
	}
	// Asterisk is not a valid API code and is reserved for the gateway's
	// in-process cache invalidation of all inherited policies.
	s.interfaceChanged("*", "*")
}

type MarketKeyProvider interface {
	ListInvokeKeys(ctx context.Context, userID, interfaceID int64, keyword string) ([]keyaccess.InvokeKeyOption, error)
	// ValidateInvokeKey 校验指定密钥是否可调用目标接口，返回密钥前缀与密钥类型。
	// keyID 为 0 时表示未选择密钥，直接放行（保持原有管理员调试行为）。
	ValidateInvokeKey(ctx context.Context, userID, keyID, interfaceID int64) (keyPrefix, keyScope string, err error)
}

var (
	apiCodePattern     = regexp.MustCompile(`^[A-Za-z0-9_-]{3,64}$`)
	placeholderPattern = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9_]*)}`)
	reservedAPICodes   = map[string]bool{
		"api": true, "auth": true, "friend-links": true, "healthz": true, "interface": true,
		"key": true, "market": true, "menu": true, "notice": true, "package": true,
		"payment": true, "site": true, "system": true, "upload": true, "user": true,
	}
	validOperators = map[string]bool{"EQ": true, "NE": true, "GT": true, "LT": true, "CONTAINS": true}
)

var responseAdFieldPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

const (
	maxResponseCacheRules = 20
	maxResponseCacheTTL   = 24 * 60 * 60
)

const (
	maxInterfacePointPrice = int64(1_000_000_000_000_000)
	maxInterfaceQPSLimit   = 1_000_000
	maxBatchInterfaceIDs   = 100
)

const upstreamProbeTimeout = 5 * time.Second

// Health metrics are best-effort bookkeeping and must not consume the full
// request budget needed to try the next upstream node.
const upstreamHealthWriteTimeout = 100 * time.Millisecond

// A health read is only routing metadata. Keep it short and reserve a fair
// share of a caller's remaining deadline for the actual upstream candidates.
const upstreamHealthReadTimeout = 75 * time.Millisecond

// upstreamHealthWriteSlots bounds detached health-bookkeeping work. Health
// persistence is deliberately outside the invoke critical path; a degraded
// Redis must not turn each polling request into a blocked goroutine. Probe
// jobs continue to use the synchronous helper because they are maintenance
// work already running outside a user request.
var upstreamHealthWriteSlots = make(chan struct{}, 128)

const (
	// defaultProxyTestURL 是服务端兜底默认出口检测目标。
	// 原先使用的 B 站 /x/web-interface/ip 现已返回 HTTP 404（疑似下线/改版），
	// 改用国内稳定可达、直接返回公网 IP 的 myip.ipip.net（纯文本，extractPublicIPValue 已兼容兜底解析）。
	// 生产仍可通过 PROXY_TEST_URL 环境变量覆盖；若该地址临时不可达，testProxyPoolConfig 会自动回退到 fallbackProxyTestTargets。
	defaultProxyTestURL       = "https://myip.ipip.net"
	maxProxyTestResponseBytes = int64(64 << 10)
	proxyTestTimeout          = 8 * time.Second
)

// proxyTestTargets 预置可选的代理出口检测目标。
// 前端「测试目标」下拉框按 key 选择，custom 时使用请求中的 CustomTestUrl。
// 这些目标均为国内可直连的主流站点接口，避免被墙域名导致必然失败的测试。
// 注意：douyin/kuaishou 需要鉴权、taobao 需要有效 accessKey，作为显式选项可能返回非 2xx；
// 默认（自动）路径走 defaultProxyTestURL 并带 fallbackProxyTestTargets 兜底，不会误报 502。
var proxyTestTargets = map[string]string{
	// bilibili 使用 /x/web-interface/zone（返回 data.addr 即出口 IP，无需鉴权、国内直连）。
	"bilibili": "https://api.bilibili.com/x/web-interface/zone",
	"baidu":    "https://sp0.baidu.com/8aQDcjDXV2JURnC2pXh1p2F1UtnX8vZh.php?query=ip&resource=json",
	"douyin":   "https://www.douyin.com/aweme/v1/web/r/byteapp/info/",
	"kuaishou": "https://www.kuaishou.com/graphql",
	"taobao":   "https://ip.taobao.com/outGetIpInfo?ip=&accessKey=alibaba-inc",
}

// fallbackProxyTestTargets 是默认出口检测地址不可达时的备用列表（仅当使用默认地址时启用）。
// 均为国内可直连、稳定返回公网 IP 的接口，避免单一地址临时 404/故障导致代理测试误报 502。
var fallbackProxyTestTargets = []string{
	"https://sp0.baidu.com/8aQDcjDXV2JURnC2pXh1p2F1UtnX8vZh.php?query=ip&resource=json",
}

// resolveProxyTestTarget 根据请求选择出口检测目标 URL。
// 优先级：CustomTestUrl（testTarget=custom）> TestTarget 预设 > 服务端默认。
func resolveProxyTestTarget(req ProxyPoolTestRequest) (string, error) {
	target := strings.TrimSpace(req.TestTarget)
	custom := strings.TrimSpace(req.CustomTestUrl)
	if target == "custom" && custom != "" {
		if err := validatePublicProxyTestTarget(custom); err != nil {
			return "", err
		}
		return custom, nil
	}
	if target != "" && target != "custom" {
		if preset, ok := proxyTestTargets[target]; ok {
			return preset, nil
		}
		// 未识别的目标名回退默认，避免前端传参被滥用
		return defaultProxyTestURL, nil
	}
	return defaultProxyTestURL, nil
}

var maxInterfaceMoney = new(big.Rat).SetFrac(big.NewInt(999_999_999_999), big.NewInt(10_000))

func NewService(repo *Repository, authz *rbac.Service, systemPrefix string, rdb ...*redis.Client) *Service {
	var redisClient *redis.Client
	if len(rdb) > 0 {
		redisClient = rdb[0]
	}
	return &Service{
		repo:         repo,
		authz:        authz,
		systemPrefix: strings.TrimRight(systemPrefix, "/"),
		httpClient: httpclient.New(httpclient.Options{
			Timeout:               time.Duration(requesttimeout.MaxMilliseconds) * time.Millisecond,
			ResponseHeaderTimeout: time.Duration(requesttimeout.MaxMilliseconds) * time.Millisecond,
		}),
		proxyResolver: proxypool.NewResolver(httpclient.Outbound(5 * time.Second)),
		proxyTestURL:  defaultProxyTestURL,
		rdb:           redisClient,
	}
}

func (s *Service) SetMarketKeyProvider(provider MarketKeyProvider) {
	s.keyProvider = provider
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	principal, err := s.authz.Authenticate(ctx, token)
	if err != nil {
		return Principal{}, err
	}
	return Principal{ID: principal.ID, Username: principal.Username, Roles: principal.Roles, TokenVersion: principal.TokenVersion}, nil
}

func (s *Service) List(ctx context.Context, principal Principal, params ListParams) (PageResult[InterfaceItem], error) {
	// The management list permission controls editing and visibility of the
	// full catalog. Authenticated users still need a read-only list for the
	// console and key binding dialog; keep that fallback limited to enabled
	// interfaces and sanitize the response below.
	admin := hasRole(principal.Roles, "admin")
	if admin {
		if err := s.require(ctx, principal, "interface:list"); err != nil {
			return PageResult[InterfaceItem]{}, err
		}
	}
	if err := normalizeListParams(&params); err != nil {
		return PageResult[InterfaceItem]{}, err
	}
	if !admin {
		params.Status = intPtr(1)
	}
	result, err := s.repo.List(ctx, params, admin)
	if err != nil {
		return PageResult[InterfaceItem]{}, err
	}
	if admin {
		for index := range result.Items {
			result.Items[index].ProxyPoolConfig = redactProxyPoolConfig(result.Items[index].ProxyPoolConfig)
		}
	} else {
		for index := range result.Items {
			result.Items[index] = sanitizePublicInterfaceItem(result.Items[index])
		}
	}
	return result, nil
}

func (s *Service) Create(ctx context.Context, principal Principal, req SaveInterfaceRequest) (InterfaceItem, error) {
	if err := s.require(ctx, principal, "interface:create"); err != nil {
		return InterfaceItem{}, err
	}
	item, err := s.normalizeSave(ctx, req, 0)
	if err != nil {
		return InterfaceItem{}, err
	}
	id, err := s.repo.Create(ctx, item)
	if err != nil {
		return InterfaceItem{}, mapInterfaceWriteError(err)
	}
	created, err := s.repo.ByID(ctx, id)
	if err != nil {
		return InterfaceItem{}, err
	}
	s.notifyInterfaceChanged("", created.APICode)
	created.ProxyPoolConfig = redactProxyPoolConfig(created.ProxyPoolConfig)
	return created, nil
}

func (s *Service) Update(ctx context.Context, principal Principal, id int64, req SaveInterfaceRequest) error {
	if err := s.require(ctx, principal, "interface:update"); err != nil {
		return err
	}
	current, err := s.repo.ByID(ctx, id)
	if err != nil {
		return mapNotFound(err, "接口不存在")
	}
	hasPolling := hasPollingPayload(req)
	if hasPolling && req.ResponseCleaningEnabled == nil {
		value := current.ResponseCleaningEnabled
		req.ResponseCleaningEnabled = &value
	}
	if hasPolling {
		// 接口基础信息编辑只要携带任意一个轮询字段，就会整体重写轮询配置。
		// 表单没有提交的响应校验字段必须沿用库中现值，否则一次「只改了上游地址」
		// 的保存会把失败校验（pollingCheckEnabled/field/expected/stopCode）清空，
		// 导致网关退化成宽松的自动兼容判定、非 404 响应不再轮询。
		// 需要显式清空这些字段时请使用轮询编辑器（SavePolling）。
		preserveMissingPollingCheck(&req, current)
		preserveMissingResponseFieldMappings(&req.ResponseFieldMappings, &req.UpstreamConfigs, req.UpstreamURLs, current)
		preserveMissingDomainRouting(&req.DomainRouting, current.DomainRouting)
		preserveMissingProxyPoolConfig(&req.ProxyPoolConfig, current.ProxyPoolConfig)
	}
	if req.QPSLimit == nil {
		value := current.QPSLimit
		req.QPSLimit = &value
	}
	if req.SharedQPSLimit == nil {
		value := current.SharedQPSLimit
		req.SharedQPSLimit = &value
	}
	item, err := s.normalizeSave(ctx, req, id)
	if err != nil {
		return err
	}
	if !hasPolling {
		copyPollingConfig(&item, current)
	}
	if err := mapNotFound(mapInterfaceWriteError(s.repo.Update(ctx, id, item)), "接口不存在"); err != nil {
		return err
	}
	s.notifyInterfaceChanged(current.APICode, item.APICode)
	return nil
}

func (s *Service) Delete(ctx context.Context, principal Principal, id int64) error {
	if err := s.require(ctx, principal, "interface:delete"); err != nil {
		return err
	}
	current, err := s.repo.ByID(ctx, id)
	if err != nil {
		return mapNotFound(err, "接口不存在")
	}
	err = s.repo.Delete(ctx, id)
	switch {
	case errors.Is(err, ErrInterfaceHasActiveEntitlements):
		return apperrors.New(consts.StatusConflict, "该接口仍有用户生效中的套餐权益，不能删除")
	case errors.Is(err, ErrInterfaceHasPendingPayments):
		return apperrors.New(consts.StatusConflict, "该接口仍有待支付或支付中的套餐订单，不能删除")
	default:
		if mapped := mapNotFound(err, "接口不存在"); mapped != nil {
			return mapped
		}
		s.notifyInterfaceChanged(current.APICode, "")
		return nil
	}
}

func (s *Service) BatchUpdateStatus(ctx context.Context, principal Principal, req BatchInterfaceStatusRequest) error {
	if err := s.require(ctx, principal, "interface:update"); err != nil {
		return err
	}
	ids, err := normalizeBatchInterfaceIDs(req.IDs)
	if err != nil {
		return err
	}
	if req.Status != 0 && req.Status != 1 {
		return apperrors.New(consts.StatusBadRequest, "接口状态不合法")
	}
	if err := s.repo.UpdateStatusBatch(ctx, ids, req.Status); err != nil {
		return err
	}
	for _, id := range ids {
		// Status changes are part of the gateway preflight policy. Resolve the
		// code after the write so a stale cached config is dropped immediately.
		if item, err := s.repo.ByID(ctx, id); err == nil {
			s.notifyInterfaceChanged(item.APICode, item.APICode)
		}
	}
	return nil
}

func (s *Service) BatchDelete(ctx context.Context, principal Principal, req BatchInterfaceIDsRequest) error {
	if err := s.require(ctx, principal, "interface:delete"); err != nil {
		return err
	}
	ids, err := normalizeBatchInterfaceIDs(req.IDs)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.Delete(ctx, principal, id); err != nil {
			return err
		}
	}
	return nil
}

func normalizeBatchInterfaceIDs(ids []int64) ([]int64, error) {
	if len(ids) == 0 || len(ids) > maxBatchInterfaceIDs {
		return nil, apperrors.New(consts.StatusBadRequest, "请选择 1 到 100 个接口")
	}
	seen := make(map[int64]struct{}, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id <= 0 {
			return nil, apperrors.New(consts.StatusBadRequest, "接口 ID 不合法")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, nil
}

func (s *Service) Polling(ctx context.Context, principal Principal, id int64) (PollingConfig, error) {
	if err := s.require(ctx, principal, "interface:polling"); err != nil {
		return PollingConfig{}, err
	}
	item, err := s.repo.ByID(ctx, id)
	if err != nil {
		return PollingConfig{}, mapNotFound(err, "接口不存在")
	}
	s.enrichUpstreamHealth(ctx, &item)
	result := pollingConfig(item)
	result.ProxyPoolConfig = redactProxyPoolConfig(result.ProxyPoolConfig)
	return result, nil
}

func (s *Service) SavePolling(ctx context.Context, principal Principal, id int64, req SavePollingConfig) error {
	if err := s.require(ctx, principal, "interface:polling"); err != nil {
		return err
	}
	current, err := s.repo.ByID(ctx, id)
	if err != nil {
		return mapNotFound(err, "接口不存在")
	}
	if req.ResponseCleaningEnabled == nil {
		value := current.ResponseCleaningEnabled
		req.ResponseCleaningEnabled = &value
	}
	preserveMissingResponseFieldMappings(&req.ResponseFieldMappings, &req.UpstreamConfigs, req.UpstreamURLs, current)
	preserveMissingDomainRouting(&req.DomainRouting, current.DomainRouting)
	preserveMissingProxyPoolConfig(&req.ProxyPoolConfig, current.ProxyPoolConfig)
	cfg, err := normalizePolling(req)
	if err != nil {
		return err
	}
	if err := mapNotFound(mapInterfaceWriteError(s.repo.SavePolling(ctx, id, cfg)), "接口不存在"); err != nil {
		return err
	}
	s.notifyInterfaceChanged(current.APICode, current.APICode)
	return nil
}

func (s *Service) CheckUpstreams(ctx context.Context, principal Principal, id int64) ([]UpstreamCheckResult, error) {
	if err := s.require(ctx, principal, "interface:polling"); err != nil {
		return nil, err
	}
	item, err := s.repo.ByID(ctx, id)
	if err != nil {
		return nil, mapNotFound(err, "接口不存在")
	}
	item.ProxyPoolConfig, err = decryptProxyPoolConfig(s.repo.cipher, item.ProxyPoolConfig)
	if err != nil {
		return nil, mapProxySecretError(err)
	}
	return s.probeInterfaceNodes(ctx, item)
}

func (s *Service) InspectProxyPool(ctx context.Context, principal Principal, req ProxyPoolInspectRequest) (ProxyPoolInspectResult, error) {
	if err := s.require(ctx, principal, "interface:polling"); err != nil {
		return ProxyPoolInspectResult{}, err
	}
	proxyConfig := req.ProxyPoolConfig
	if proxyConfig.Password == "" && proxyConfig.PasswordConfigured && req.InterfaceID > 0 {
		current, err := s.repo.ByID(ctx, req.InterfaceID)
		if err != nil {
			return ProxyPoolInspectResult{}, mapNotFound(err, "接口不存在")
		}
		proxyConfig.Password = current.ProxyPoolConfig.Password
	}
	proxyConfig, err := decryptProxyPoolConfig(s.repo.cipher, proxyConfig)
	if err != nil {
		return ProxyPoolInspectResult{}, mapProxySecretError(err)
	}
	normalized, err := proxypool.Normalize(proxyConfig)
	if err != nil {
		return ProxyPoolInspectResult{}, apperrors.New(consts.StatusBadRequest, masking.Error(err))
	}
	resolver := s.proxyResolver
	if resolver == nil {
		resolver = proxypool.NewResolver(s.httpClient)
	}
	result, err := inspectProxyPoolConfig(ctx, resolver, normalized)
	if err != nil {
		return ProxyPoolInspectResult{}, apperrors.Wrap(consts.StatusBadGateway, "代理池接口检测失败", err)
	}
	return result, nil
}

func inspectProxyPoolConfig(ctx context.Context, resolver *proxypool.Resolver, proxyConfig proxypool.Config) (ProxyPoolInspectResult, error) {
	endpoint, candidates, err := resolver.Inspect(ctx, proxyConfig)
	if err != nil {
		return ProxyPoolInspectResult{}, err
	}
	detectedPath := strings.TrimPrefix(strings.TrimSpace(proxyConfig.JSONPath), "$.")
	if detectedPath == "" {
		value := net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))
		for _, candidate := range candidates {
			if candidate.Value == value {
				detectedPath = candidate.Path
				break
			}
		}
	}
	return ProxyPoolInspectResult{
		Candidates:   candidates,
		DetectedPath: detectedPath,
		Host:         endpoint.Host,
		Port:         endpoint.Port,
		Protocol:     endpoint.Protocol,
	}, nil
}

func (s *Service) TestProxyPool(ctx context.Context, principal Principal, req ProxyPoolTestRequest) (ProxyPoolTestResult, error) {
	if err := s.require(ctx, principal, "interface:polling"); err != nil {
		return ProxyPoolTestResult{}, err
	}
	target, err := resolveProxyTestTarget(req)
	if err != nil {
		return ProxyPoolTestResult{}, apperrors.New(consts.StatusBadRequest, masking.Error(err))
	}
	proxyConfig, err := s.proxyConfigDraft(ctx, req.InterfaceID, req.ProxyPoolConfig)
	if err != nil {
		return ProxyPoolTestResult{}, err
	}
	proxyConfig.Enabled = 1
	normalized, err := proxypool.Normalize(proxyConfig)
	if err != nil {
		return ProxyPoolTestResult{}, apperrors.New(consts.StatusBadRequest, masking.Error(err))
	}
	resolver := s.proxyResolver
	if resolver == nil {
		resolver = proxypool.NewResolver(s.httpClient)
	}
	result, err := testProxyPoolConfig(ctx, resolver, s.httpClient, normalized, target)
	if err != nil {
		return ProxyPoolTestResult{}, apperrors.Wrap(consts.StatusBadGateway, "代理连通性测试失败", err)
	}
	return result, nil
}

func (s *Service) proxyConfigDraft(ctx context.Context, interfaceID int64, proxyConfig proxypool.Config) (proxypool.Config, error) {
	if proxyConfig.Password == "" && proxyConfig.PasswordConfigured && interfaceID > 0 {
		current, err := s.repo.ByID(ctx, interfaceID)
		if err != nil {
			return proxypool.Config{}, mapNotFound(err, "接口不存在")
		}
		proxyConfig.Password = current.ProxyPoolConfig.Password
	}
	proxyConfig, err := decryptProxyPoolConfig(s.repo.cipher, proxyConfig)
	if err != nil {
		return proxypool.Config{}, mapProxySecretError(err)
	}
	return proxyConfig, nil
}

func testProxyPoolConfig(ctx context.Context, resolver *proxypool.Resolver, base *http.Client, proxyConfig proxypool.Config, target string) (ProxyPoolTestResult, error) {
	if base == nil {
		return ProxyPoolTestResult{}, errors.New("代理测试 HTTP 客户端不可用")
	}
	if resolver == nil {
		resolver = proxypool.NewResolver(base)
	}
	proxyConfig.Enabled = 1
	normalized, err := proxypool.Normalize(proxyConfig)
	if err != nil {
		return ProxyPoolTestResult{}, err
	}
	if err := validatePublicProxyTestTarget(target); err != nil {
		return ProxyPoolTestResult{}, err
	}
	endpoint, err := resolver.Resolve(ctx, normalized)
	if err != nil {
		return ProxyPoolTestResult{}, err
	}
	client, err := httpclient.WithProxyURL(base, endpoint.ProxyURL())
	if err != nil {
		return ProxyPoolTestResult{}, err
	}
	testClient := *client
	testClient.Timeout = proxyTestTimeout
	testClient.CheckRedirect = proxyTestRedirectPolicy

	// 候选出口检测地址：首选解析出的目标；若其为默认地址（自动选择），
	// 在其后追加备用地址，避免单一地址临时 404/故障导致代理测试误报 502。
	candidates := []string{strings.TrimSpace(target)}
	if target == defaultProxyTestURL {
		for _, fb := range fallbackProxyTestTargets {
			if fb != target {
				candidates = append(candidates, fb)
			}
		}
	}

	var lastErr error
	for _, cand := range candidates {
		if err := validatePublicProxyTestTarget(cand); err != nil {
			lastErr = err
			continue
		}
		res, err := probeEgress(ctx, &testClient, cand)
		if err != nil {
			lastErr = err
			continue
		}
		res.Protocol = normalized.Protocol
		res.ProxyAddress = strings.ToLower(normalized.Protocol) + "://" + net.JoinHostPort(endpoint.Host, strconv.Itoa(endpoint.Port))
		return res, nil
	}
	if lastErr == nil {
		lastErr = errors.New("出口检测失败：无可用检测地址")
	}
	return ProxyPoolTestResult{}, lastErr
}

// probeEgress 通过代理客户端向指定出口检测地址发起一次 GET，校验 HTTP 2xx 并解析公网出口 IP。
func probeEgress(ctx context.Context, client *http.Client, target string) (ProxyPoolTestResult, error) {
	testCtx, cancel := context.WithTimeout(ctx, proxyTestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(testCtx, http.MethodGet, strings.TrimSpace(target), nil)
	if err != nil {
		return ProxyPoolTestResult{}, err
	}
	request.Header.Set("Accept", "application/json, text/plain;q=0.9")
	startedAt := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return ProxyPoolTestResult{}, err
	}
	defer response.Body.Close()
	data, err := httpclient.ReadBody(response.Body, maxProxyTestResponseBytes)
	if err != nil {
		return ProxyPoolTestResult{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return ProxyPoolTestResult{}, fmt.Errorf("出口检测地址返回 HTTP %d", response.StatusCode)
	}
	exitIP, err := parsePublicExitIP(data)
	if err != nil {
		return ProxyPoolTestResult{}, err
	}
	return ProxyPoolTestResult{
		ElapsedMs:  time.Since(startedAt).Milliseconds(),
		ExitIP:     exitIP,
		StatusCode: response.StatusCode,
		Success:    true,
		Target:     target,
	}, nil
}

func proxyTestRedirectPolicy(request *http.Request, via []*http.Request) error {
	if len(via) > 2 {
		return errors.New("代理测试重定向次数超过 2 次")
	}
	return validatePublicProxyTestTarget(request.URL.String())
}

func validatePublicProxyTestTarget(raw string) error {
	if err := httpclient.ValidateURL(raw); err != nil {
		return err
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if ip := net.ParseIP(parsed.Hostname()); ip != nil && !httpclient.IsPublicIP(ip) {
		return errors.New("代理测试地址必须使用公网 IP")
	}
	return nil
}

func parsePublicExitIP(data []byte) (string, error) {
	value := extractPublicIPValue(data)
	ip := net.ParseIP(value)
	if ip == nil || !httpclient.IsPublicIP(ip) {
		return "", errors.New("出口检测响应未包含合法公网 IP")
	}
	return ip.String(), nil
}

// extractPublicIPValue 从出口检测响应中提取 IP 字符串，兼容多种常见格式：
//   - {"ip":"1.2.3.4"}
//   - {"addr":"1.2.3.4"}（B 站 /x/web-interface/ip 返回 {code:0,data:{addr:"..."}}）
//   - {"data":{"addr":"1.2.3.4"}} / {"data":{"ip":"1.2.3.4"}}
//   - {"query":"1.2.3.4"}（百度 sp0.baidu.com 返回 {"query":"1.2.3.4",...}）
//   - 纯文本 IP（"1.2.3.4" 或带引号）
func extractPublicIPValue(data []byte) string {
	// 先尝试 JSON 结构化提取
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err == nil {
		// 顶层常见字段
		for _, key := range []string{"ip", "addr", "query", "data"} {
			if v, ok := payload[key]; ok {
				switch tv := v.(type) {
				case string:
					if ip := net.ParseIP(strings.TrimSpace(tv)); ip != nil {
						return ip.String()
					}
				case map[string]any:
					// data.addr / data.ip
					for _, subKey := range []string{"addr", "ip"} {
						if sv, ok := tv[subKey].(string); ok {
							if ip := net.ParseIP(strings.TrimSpace(sv)); ip != nil {
								return ip.String()
							}
						}
					}
				}
			}
		}
		// 兼容 {result:{ip:...}} 等其他嵌套
		if v, ok := payload["result"].(map[string]any); ok {
			if sv, ok := v["ip"].(string); ok {
				if ip := net.ParseIP(strings.TrimSpace(sv)); ip != nil {
					return ip.String()
				}
			}
		}
	}
	// 退化为纯文本 / 带引号文本
	value := strings.Trim(strings.TrimSpace(string(data)), "\"")
	if ip := net.ParseIP(value); ip != nil {
		return ip.String()
	}
	// 最后一个兜底：尝试从字符串中提取第一个 IPv4
	if m := regexp.MustCompile(`\b(?:[0-9]{1,3}\.){3}[0-9]{1,3}\b`).FindString(value); m != "" {
		if ip := net.ParseIP(m); ip != nil {
			return ip.String()
		}
	}
	return ""
}

func (s *Service) runUpstreamProbeOnce(ctx context.Context) {
	targets, err := s.repo.ProbeTargets(ctx)
	if err != nil {
		if s.log != nil {
			s.log.Warn("load upstream probe targets failed", zap.String("error", masking.Error(err)))
		}
		return
	}
	for _, target := range targets {
		if ctx.Err() != nil {
			return
		}
		item := InterfaceItem{
			ID:                      target.ID,
			DomainRouting:           target.DomainRouting,
			PollingCheckEnabled:     target.PollingCheckEnabled,
			PollingCheckExpected:    target.PollingCheckExpected,
			PollingCheckField:       target.PollingCheckField,
			PollingEnabled:          target.PollingEnabled,
			PollingMode:             target.PollingMode,
			ProxyPoolConfig:         target.ProxyPoolConfig,
			ResponseCleaningEnabled: target.ResponseCleaningEnabled,
			ResponseFieldMappings:   target.ResponseFieldMappings,
			RequestMethod:           target.RequestMethod,
			RequestURL:              target.RequestURL,
			UpstreamConfigs:         target.UpstreamConfigs,
		}
		s.probeInterfaceNodes(ctx, item)
	}
}

func (s *Service) probeInterfaceNodes(ctx context.Context, item InterfaceItem) ([]UpstreamCheckResult, error) {
	type probeCandidate struct {
		config    UpstreamConfig
		item      InterfaceItem
		routeID   string
		routeName string
	}
	candidates := make([]probeCandidate, 0)
	for _, cfg := range upstreamCandidates(item) {
		candidates = append(candidates, probeCandidate{config: cfg, item: item})
	}
	// Include every enabled domain sub-route pool in the administrative probe
	// result.  The default pool remains first; route labels make it possible for
	// the UI and health records to distinguish otherwise identical node URLs.
	var routes []DomainPollingRoute
	if item.DomainRouting.Enabled == 1 {
		var routeErr error
		routes, routeErr = normalizedRuntimeDomainRoutes(item.DomainRouting.Routes)
		if routeErr != nil {
			return nil, apperrors.New(consts.StatusServiceUnavailable, "域名子轮询配置暂时不可用")
		}
	}
	for _, route := range routes {
		if route.Enabled != 1 {
			continue
		}
		routeItem := applyInterfaceDomainRoute(item, route)
		for _, cfg := range upstreamCandidates(routeItem) {
			candidates = append(candidates, probeCandidate{config: cfg, item: routeItem, routeID: route.ID, routeName: route.Name})
		}
	}
	if len(candidates) == 0 {
		candidates = append(candidates, probeCandidate{config: UpstreamConfig{URL: item.RequestURL, PollingCheckEnabled: item.PollingCheckEnabled, PollingCheckField: item.PollingCheckField, PollingCheckExpected: item.PollingCheckExpected}, item: item})
	}
	results := make([]UpstreamCheckResult, 0, len(candidates))
	for _, candidate := range candidates {
		cfg := candidate.config
		result := s.probeOne(ctx, candidate.item, cfg)
		result.RouteID = candidate.routeID
		result.RouteName = candidate.routeName
		results = append(results, result)
		if s.rdb != nil && ctx.Err() == nil {
			s.recordUpstreamHealth(ctx, parseID(item.ID), cfg.URL, result.HealthStatus == upstreamhealth.StatusHealthy, result.LatencyMs, candidate.routeID)
		}
	}
	return results, nil
}

func (s *Service) probeOne(ctx context.Context, item InterfaceItem, cfg UpstreamConfig) UpstreamCheckResult {
	if strings.TrimSpace(cfg.URL) == "" {
		return UpstreamCheckResult{URL: cfg.URL, HealthStatus: upstreamhealth.StatusUnhealthy, Message: "节点地址为空", CheckedAt: time.Now().Format(time.RFC3339)}
	}
	method := "GET"
	if item.RequestMethod == "POST" {
		method = "POST"
	}
	probeCtx, cancel := context.WithTimeout(ctx, upstreamProbeTimeout)
	defer cancel()
	var body io.Reader
	if method == "POST" {
		body = bytes.NewReader([]byte("{}"))
	}
	request, err := http.NewRequestWithContext(probeCtx, method, strings.TrimSpace(cfg.URL), body)
	if err != nil {
		return UpstreamCheckResult{URL: cfg.URL, HealthStatus: upstreamhealth.StatusUnhealthy, Message: "请求地址不合法", CheckedAt: time.Now().Format(time.RFC3339)}
	}
	if method == "POST" {
		request.Header.Set("Content-Type", "application/json")
	}
	client := s.httpClient
	if item.ProxyPoolConfig.Enabled == 1 {
		resolver := s.proxyResolver
		if resolver == nil {
			resolver = proxypool.NewResolver(s.httpClient)
		}
		client, err = resolver.Client(probeCtx, s.httpClient, item.ProxyPoolConfig)
		if err != nil {
			return UpstreamCheckResult{URL: cfg.URL, HealthStatus: upstreamhealth.StatusUnhealthy, Message: "代理不可用", CheckedAt: time.Now().Format(time.RFC3339)}
		}
	}
	start := time.Now()
	response, err := client.Do(request)
	latencyMs := time.Since(start).Milliseconds()
	if err != nil {
		message := "请求失败"
		if isProbeTimeout(err) {
			message = "连接超时"
		}
		return UpstreamCheckResult{URL: cfg.URL, HealthStatus: upstreamhealth.StatusUnhealthy, LatencyMs: latencyMs, Message: message, CheckedAt: time.Now().Format(time.RFC3339)}
	}
	defer response.Body.Close()
	if _, err := httpclient.ReadBody(response.Body, httpclient.MaxUpstreamResponseBytes); err != nil {
		return UpstreamCheckResult{URL: cfg.URL, HealthStatus: upstreamhealth.StatusUnhealthy, StatusCode: response.StatusCode, LatencyMs: latencyMs, Message: "响应读取失败", CheckedAt: time.Now().Format(time.RFC3339)}
	}
	checkedAt := time.Now().Format(time.RFC3339)
	// 健康探测是无参数请求，业务级响应校验（pollingCheck）只适用于真实调用，
	// 这里仅以 HTTP 状态码判定节点可达性。
	if response.StatusCode >= 200 && response.StatusCode < 400 {
		return UpstreamCheckResult{URL: cfg.URL, HealthStatus: upstreamhealth.StatusHealthy, StatusCode: response.StatusCode, LatencyMs: latencyMs, Message: "ok", CheckedAt: checkedAt}
	}
	return UpstreamCheckResult{URL: cfg.URL, HealthStatus: upstreamhealth.StatusUnhealthy, StatusCode: response.StatusCode, LatencyMs: latencyMs, Message: fmt.Sprintf("HTTP %d", response.StatusCode), CheckedAt: checkedAt}
}

func isProbeTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func (s *Service) Doc(ctx context.Context, principal Principal, id int64) (DocConfig, error) {
	if err := s.require(ctx, principal, "interface:doc"); err != nil {
		return DocConfig{}, err
	}
	item, err := s.repo.ByID(ctx, id)
	if err != nil {
		return DocConfig{}, mapNotFound(err, "接口不存在")
	}
	return docConfig(item), nil
}

func (s *Service) SaveDoc(ctx context.Context, principal Principal, id int64, req DocConfig) error {
	if err := s.require(ctx, principal, "interface:doc"); err != nil {
		return err
	}
	item, err := s.repo.ByID(ctx, id)
	if err != nil {
		return mapNotFound(err, "接口不存在")
	}
	cfg, err := normalizeDoc(req, item.RequestMethod)
	if err != nil {
		return err
	}
	if err := mapNotFound(s.repo.SaveDoc(ctx, id, cfg), "接口不存在"); err != nil {
		return err
	}
	s.notifyInterfaceChanged(item.APICode, item.APICode)
	return nil
}

func (s *Service) BillingRules(ctx context.Context, principal Principal, id int64) ([]BillingRule, error) {
	if err := s.require(ctx, principal, "interface:billing"); err != nil {
		return nil, err
	}
	if _, err := s.repo.ByID(ctx, id); err != nil {
		return nil, mapNotFound(err, "接口不存在")
	}
	return s.repo.BillingRules(ctx, id)
}

func (s *Service) SaveBillingRules(ctx context.Context, principal Principal, id int64, req SaveBillingRulesRequest) error {
	if err := s.require(ctx, principal, "interface:billing"); err != nil {
		return err
	}
	if _, err := s.repo.ByID(ctx, id); err != nil {
		return mapNotFound(err, "接口不存在")
	}
	if err := validateBillingSwitches(req); err != nil {
		return err
	}
	rules, err := normalizeBillingRules(req.Rules)
	if err != nil {
		return err
	}
	if err := s.repo.ReplaceBillingRules(ctx, id, rules, req); err != nil {
		return err
	}
	if item, err := s.repo.ByID(ctx, id); err == nil {
		s.notifyInterfaceChanged(item.APICode, item.APICode)
	}
	return nil
}

func (s *Service) RequestValidationPolicy(ctx context.Context, principal Principal, id int64) (RequestValidationPolicy, error) {
	if err := s.require(ctx, principal, "interface:request-validation"); err != nil {
		return RequestValidationPolicy{}, err
	}
	policy, err := s.repo.RequestValidationPolicy(ctx, id)
	return policy, mapNotFound(err, "接口不存在")
}

func (s *Service) SaveRequestValidationPolicy(ctx context.Context, principal Principal, id int64, req SaveRequestValidationPolicyRequest) error {
	if err := s.require(ctx, principal, "interface:request-validation"); err != nil {
		return err
	}
	if _, err := s.repo.ByID(ctx, id); err != nil {
		return mapNotFound(err, "接口不存在")
	}
	policy, err := normalizeRequestValidationPolicy(req)
	if err != nil {
		return err
	}
	if err := mapNotFound(s.repo.SaveRequestValidationPolicy(ctx, id, policy), "接口不存在"); err != nil {
		return err
	}
	if item, err := s.repo.ByID(ctx, id); err == nil {
		s.notifyInterfaceChanged(item.APICode, item.APICode)
	}
	return nil
}

func (s *Service) DeliveryPolicy(ctx context.Context, principal Principal, id int64) (DeliveryPolicy, error) {
	if err := s.require(ctx, principal, "interface:delivery-policy"); err != nil {
		return DeliveryPolicy{}, err
	}
	policy, err := s.repo.DeliveryPolicy(ctx, id)
	return policy, mapNotFound(err, "接口不存在")
}

func (s *Service) GlobalDeliveryPolicy(ctx context.Context, principal Principal) (DeliveryPolicy, error) {
	if err := s.require(ctx, principal, "interface:delivery-policy"); err != nil {
		return DeliveryPolicy{}, err
	}
	policy, err := s.repo.GlobalDeliveryPolicy(ctx)
	return policy, mapNotFound(err, "全局投放策略不存在")
}

func (s *Service) ResponseAdvertisementOptions(ctx context.Context, principal Principal) ([]ResponseAdvertisementOption, error) {
	if err := s.require(ctx, principal, "interface:delivery-policy"); err != nil {
		return nil, err
	}
	return s.repo.ResponseAdvertisementOptions(ctx)
}

func (s *Service) SaveDeliveryPolicy(ctx context.Context, principal Principal, id int64, req SaveDeliveryPolicyRequest) error {
	if err := s.require(ctx, principal, "interface:delivery-policy"); err != nil {
		return err
	}
	policy, err := s.normalizeDeliveryPolicy(ctx, req)
	if err != nil {
		return err
	}
	if err := mapNotFound(s.repo.SaveDeliveryPolicy(ctx, id, policy), "接口不存在"); err != nil {
		return err
	}
	if item, err := s.repo.ByID(ctx, id); err == nil {
		s.notifyInterfaceChanged(item.APICode, item.APICode)
	}
	return nil
}

func (s *Service) SaveGlobalDeliveryPolicy(ctx context.Context, principal Principal, req SaveDeliveryPolicyRequest) error {
	if err := s.require(ctx, principal, "interface:delivery-policy"); err != nil {
		return err
	}
	if req.requestTimeoutMissing {
		current, err := s.repo.GlobalDeliveryPolicy(ctx)
		if err != nil {
			return mapNotFound(err, "全局投放策略不存在")
		}
		req.RequestTimeoutMs = current.RequestTimeoutMs
	}
	if _, err := requesttimeout.Normalize(req.RequestTimeoutMs, false); err != nil {
		return apperrors.New(consts.StatusBadRequest, "全局请求超时必须在100到120000毫秒之间")
	}
	policy, err := s.normalizeDeliveryPolicy(ctx, req)
	if err != nil {
		return err
	}
	policy.Scope = "GLOBAL"
	policy.CachePolicyOverride = 1
	policy.ResponseAdPolicyOverride = 1
	policy.RequestTimeoutOverride = 1
	err = mapNotFound(s.repo.SaveGlobalDeliveryPolicy(ctx, policy, principal.ID), "全局投放策略不存在")
	if err == nil {
		// A global timeout affects every inherited interface. The callback uses
		// an empty code pair as an explicit "invalidate all" signal.
		s.notifyAllInterfacesChanged()
	}
	return err
}

func (s *Service) normalizeDeliveryPolicy(ctx context.Context, req SaveDeliveryPolicyRequest) (DeliveryPolicy, error) {
	cacheEnabled := flagValue(req.CacheEnabled, 0)
	responseAdEnabled := flagValue(req.ResponseAdEnabled, 0)
	cacheOverride := flagValue(req.CachePolicyOverride, 1)
	responseAdOverride := flagValue(req.ResponseAdPolicyOverride, 1)
	requestTimeoutOverride := flagValue(req.RequestTimeoutOverride, 0)
	// Older clients did not send the inheritance flag. Treat a non-zero
	// interface timeout from those payloads as an intentional local override;
	// an explicit 0 still remains the escape hatch for inheriting the global
	// value while retaining the local setting for later re-enablement.
	if req.RequestTimeoutOverride == nil && req.RequestTimeoutMs != 0 {
		requestTimeoutOverride = 1
	}
	if cacheEnabled < 0 || responseAdEnabled < 0 || cacheOverride < 0 || responseAdOverride < 0 {
		return DeliveryPolicy{}, apperrors.New(consts.StatusBadRequest, "投放策略状态不合法")
	}
	if requestTimeoutOverride < 0 || requestTimeoutOverride > 1 {
		return DeliveryPolicy{}, apperrors.New(consts.StatusBadRequest, "请求超时继承状态不合法")
	}
	requestTimeoutMs, err := requesttimeout.Normalize(req.RequestTimeoutMs, true)
	if err != nil {
		return DeliveryPolicy{}, apperrors.New(consts.StatusBadRequest, "请求超时必须在100到120000毫秒之间，0表示继承全局")
	}
	if req.CacheDefaultTTLSeconds < 0 || req.CacheDefaultTTLSeconds > maxResponseCacheTTL {
		return DeliveryPolicy{}, apperrors.New(consts.StatusBadRequest, "默认缓存时间必须在0到86400秒之间")
	}
	rules, err := normalizeResponseCacheRules(req.CacheRules)
	if err != nil {
		return DeliveryPolicy{}, err
	}
	field := strings.TrimSpace(req.ResponseAdField)
	if field == "" {
		field = "_advertisement"
	}
	if !responseAdFieldPattern.MatchString(field) {
		return DeliveryPolicy{}, apperrors.New(consts.StatusBadRequest, "广告 JSON 字段名不合法")
	}
	responseAdID, err := normalizeNonnegativeID(req.ResponseAdID)
	if err != nil {
		return DeliveryPolicy{}, apperrors.New(consts.StatusBadRequest, "接口广告编号不合法")
	}
	if responseAdID > 0 {
		exists, err := s.repo.ResponseAdvertisementExists(ctx, responseAdID)
		if err != nil {
			return DeliveryPolicy{}, err
		}
		if !exists {
			return DeliveryPolicy{}, apperrors.New(consts.StatusBadRequest, "指定的接口广告不存在或投放位不匹配")
		}
	}
	return DeliveryPolicy{
		CacheDefaultTTLSeconds:   req.CacheDefaultTTLSeconds,
		CacheEnabled:             cacheEnabled,
		CachePolicyOverride:      cacheOverride,
		CacheRules:               rules,
		ResponseAdEnabled:        responseAdEnabled,
		ResponseAdField:          field,
		ResponseAdID:             responseAdID,
		ResponseAdPolicyOverride: responseAdOverride,
		RequestTimeoutMs:         requestTimeoutMs,
		RequestTimeoutOverride:   requestTimeoutOverride,
	}, nil
}

func normalizeNonnegativeID(value any) (int64, error) {
	if value == nil {
		return 0, nil
	}
	text := strings.TrimSpace(fmt.Sprintf("%v", value))
	if text == "" || text == "<nil>" {
		return 0, nil
	}
	id, err := strconv.ParseInt(text, 10, 64)
	if err != nil || id < 0 {
		return 0, errors.New("invalid nonnegative id")
	}
	return id, nil
}

func normalizeResponseCacheRules(rules []ResponseCacheRule) ([]ResponseCacheRule, error) {
	if len(rules) > maxResponseCacheRules {
		return nil, apperrors.New(consts.StatusBadRequest, "缓存条件规则不能超过20条")
	}
	result := make([]ResponseCacheRule, 0, len(rules))
	for index, rule := range rules {
		rule.Name = strings.TrimSpace(rule.Name)
		if utf8.RuneCountInString(rule.Name) > 64 {
			return nil, apperrors.New(consts.StatusBadRequest, "缓存规则名称不能超过64个字符")
		}
		rule.FieldName = strings.TrimSpace(rule.FieldName)
		if len(rule.FieldName) > 128 || strings.ContainsAny(rule.FieldName, "\r\n\x00") {
			return nil, apperrors.New(consts.StatusBadRequest, "缓存规则字段路径不合法")
		}
		rule.Operator = strings.ToUpper(strings.TrimSpace(rule.Operator))
		if rule.Operator == "" {
			rule.Operator = "EQ"
		}
		switch rule.Operator {
		case "CONTAINS", "EQ", "EXISTS", "GT", "LT", "NE":
		default:
			return nil, apperrors.New(consts.StatusBadRequest, "缓存规则运算符不合法")
		}
		if rule.StatusCode != 0 && (rule.StatusCode < 100 || rule.StatusCode > 599) {
			return nil, apperrors.New(consts.StatusBadRequest, "缓存规则状态码不合法")
		}
		if rule.StatusCode == 0 && rule.FieldName == "" {
			return nil, apperrors.New(consts.StatusBadRequest, "缓存规则必须配置状态码或 JSON 字段路径")
		}
		if rule.FieldName == "" && rule.Operator != "EQ" {
			return nil, apperrors.New(consts.StatusBadRequest, "仅状态码规则不需要额外运算符")
		}
		if rule.Operator != "EXISTS" && rule.FieldName != "" && len(rule.ExpectedValue) > 512 {
			return nil, apperrors.New(consts.StatusBadRequest, "缓存规则期望值不能超过512字节")
		}
		if rule.TTLSeconds < 1 || rule.TTLSeconds > maxResponseCacheTTL {
			return nil, apperrors.New(consts.StatusBadRequest, "缓存规则时间必须在1到86400秒之间")
		}
		if rule.SortNo == 0 {
			rule.SortNo = index + 1
		}
		result = append(result, rule)
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].SortNo < result[j].SortNo })
	return result, nil
}

func (s *Service) Invoke(ctx context.Context, principal Principal, id int64, req InvokePayload, clientIP string) (result InvokeResult, err error) {
	ctx = requestid.Ensure(ctx)
	defer func() { result.RequestID = requestid.FromContext(ctx) }()
	if err := s.require(ctx, principal, "interface:invoke"); err != nil {
		return InvokeResult{}, err
	}
	item, err := s.repo.ByID(ctx, id)
	if err != nil {
		return InvokeResult{}, mapNotFound(err, "接口不存在")
	}
	if policy, policyErr := s.repo.DeliveryPolicy(ctx, id); policyErr == nil {
		item.RequestTimeoutMs = policy.EffectiveRequestTimeoutMs
	} else {
		return InvokeResult{}, policyErr
	}
	item.ProxyPoolConfig, err = decryptProxyPoolConfig(s.repo.cipher, item.ProxyPoolConfig)
	if err != nil {
		return InvokeResult{}, mapProxySecretError(err)
	}
	// 选择密钥调试：校验密钥归属、状态与接口绑定关系。
	keyPrefix := ""
	if req.KeyID > 0 {
		if s.keyProvider == nil {
			return InvokeResult{}, apperrors.New(consts.StatusServiceUnavailable, "密钥校验服务不可用")
		}
		keyPrefix, _, err = s.keyProvider.ValidateInvokeKey(ctx, principal.ID, req.KeyID, id)
		if err != nil {
			return InvokeResult{}, err
		}
	}
	method, err := normalizeInvokeMethod(req.Method, item.RequestMethod)
	if err != nil {
		return InvokeResult{}, err
	}
	requestJSON := invokeRequestParamsForLog(req.QueryParams, req.Body)
	requestValue := requestDisplay(req.QueryParams, req.Body)
	validationPolicy, err := s.repo.RequestValidationPolicy(ctx, id)
	if err != nil {
		return InvokeResult{}, mapNotFound(err, "接口不存在")
	}
	if rejection := requestValidationRejection(requestvalidation.Evaluate(validationPolicy.Policy, invokeValidationInput(req))); rejection != nil {
		result := InvokeResult{RequestID: requestid.FromContext(ctx),
			APIName:       item.Name,
			Billable:      false,
			ChargeAmount:  "0.0000",
			PointPrice:    item.PointPrice,
			Price:         item.Price,
			RequestMethod: method,
			RequestSize:   invokeRequestSize(req.QueryParams, req.Body),
			StatusCode:    rejection.StatusCode,
		}
		result = publicInvokeFailure(result, rejection)
		if logErr := s.repo.CreateCallLog(ctx, item, principal, req.KeyID, keyPrefix, result, requestJSON, requestValue, masking.Error(rejection), clientIP); logErr != nil {
			return InvokeResult{}, apperrors.Wrap(consts.StatusServiceUnavailable, "调用日志写入失败", errors.Join(rejection, logErr))
		}
		return result, nil
	}
	start := time.Now()
	result, err = s.invokeUpstreams(ctx, item, method, req)
	result.ElapsedMs = time.Since(start).Milliseconds()
	result.APIName = item.Name
	result.Price = item.Price
	result.PointPrice = item.PointPrice
	result.ChargeAmount = "0.0000"
	result.Billable = false
	result.RequestSize = invokeRequestSize(req.QueryParams, req.Body)
	if err != nil {
		var business *apperrors.BusinessError
		if result.StatusCode == 0 {
			if errors.As(err, &business) {
				result.StatusCode = business.StatusCode
			} else {
				result.StatusCode = 502
			}
		}
		diagnostic := masking.Error(err)
		// 管理端调用日志只保留经过端点脱敏的诊断信息，便于按错误类型排障；
		// 返回给浏览器的结果统一清除上游 URL 与底层网络错误，避免异常路径泄露
		// 内部域名、端口、路由或主接口地址。真实 URL 绝不写入应用日志或调用日志。
		logResult := result
		if s.log != nil {
			// Keep the failure correlated to the API and status without writing the
			// configured target URL into the application log. The URL is an internal
			// routing secret and must never be reconstructed from diagnostics.
			s.log.Warn("management interface invoke failed",
				zap.String("interface_id", item.ID),
				zap.String("request_id", requestid.FromContext(ctx)),
				zap.Int("status_code", result.StatusCode),
				zap.Bool("upstream_switched", result.UpstreamSwitched),
				zap.String("error", diagnostic),
			)
		}
		result = publicInvokeFailure(result, err)
		if logErr := s.repo.CreateCallLog(ctx, item, principal, req.KeyID, keyPrefix, logResult, requestJSON, requestValue, masking.Error(err), clientIP); logErr != nil {
			return InvokeResult{}, apperrors.Wrap(consts.StatusServiceUnavailable, "调用日志写入失败", errors.Join(err, logErr))
		}
		return result, nil
	}
	if err := s.repo.CreateCallLog(ctx, item, principal, req.KeyID, keyPrefix, result, requestJSON, requestValue, "", clientIP); err != nil {
		return InvokeResult{}, apperrors.Wrap(consts.StatusServiceUnavailable, "调用日志写入失败", err)
	}
	return result, nil
}

func publicInvokeFailure(result InvokeResult, err error) InvokeResult {
	message := "服务暂时不可用"
	var business *apperrors.BusinessError
	if errors.As(err, &business) && business.StatusCode >= 400 && business.StatusCode < 500 {
		message = strings.TrimSpace(business.Message)
		if message == "" {
			message = "请求参数不正确"
		}
	}
	// This response is written directly by the open-interface handler and does
	// not pass through response.FailWithData. Keep the same endpoint-redaction
	// guarantee for client-visible business errors on this path.
	message = masking.ErrorText(message)
	if message == "" {
		message = "服务暂时不可用"
	}
	payload, marshalErr := json.Marshal(map[string]any{
		"code":    result.StatusCode,
		"message": message,
	})
	if marshalErr != nil {
		payload = []byte(`{"code":502,"message":"服务暂时不可用"}`)
	}
	result.Body = string(payload)
	result.ResponseSize = int64(len(payload))
	result.Success = false
	result.UpstreamURL = ""
	return result
}

func (s *Service) MarketList(ctx context.Context, params ListParams) (PageResult[InterfaceItem], error) {
	params.Status = intPtr(1)
	if err := normalizeListParams(&params); err != nil {
		return PageResult[InterfaceItem]{}, err
	}
	result, err := s.repo.List(ctx, params, false)
	if err != nil {
		return PageResult[InterfaceItem]{}, err
	}
	s.localizeInterfaceItems(ctx, result.Items)
	for i := range result.Items {
		result.Items[i] = sanitizePublicInterfaceItem(result.Items[i])
		result.Items[i].Description = result.Items[i].Remark
	}
	return result, nil
}

func normalizeListParams(params *ListParams) error {
	params.Keyword = strings.TrimSpace(params.Keyword)
	params.RequestMethod = strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(params.RequestMethod), "/", "_"))
	params.PriceType = strings.ToUpper(strings.TrimSpace(params.PriceType))
	if err := listquery.Validate(params.Keyword, params.Page, params.PageSize, 100); err != nil {
		return err
	}
	for name, value := range map[string]*int{
		"featuredOnly": params.FeaturedOnly,
		"isFeatured":   params.IsFeatured,
		"status":       params.Status,
	} {
		if err := listquery.Binary(name, value); err != nil {
			return err
		}
	}
	if err := listquery.Enum("requestMethod", params.RequestMethod, "GET", "POST", "GET_POST"); err != nil {
		return err
	}
	return listquery.Enum("priceType", params.PriceType, "FREE", "PAID", "POINT")
}

func (s *Service) MarketDetail(ctx context.Context, id int64) (MarketDetail, error) {
	item, err := s.repo.ByID(ctx, id)
	if err != nil {
		return MarketDetail{}, mapNotFound(err, "接口不存在")
	}
	if item.Status != 1 {
		return MarketDetail{}, apperrors.New(consts.StatusNotFound, "接口不存在")
	}
	s.localizeInterfaceItem(ctx, &item, true)
	item = sanitizePublicInterfaceItem(item)
	item.Description = item.Remark
	trend, err := s.repo.InterfaceTrend7d(ctx, id)
	if err != nil {
		return MarketDetail{}, err
	}
	return MarketDetail{
		InterfaceItem:   item,
		Auth:            map[string]string{"type": "API Key", "name": "key", "location": "Query", "description": "接口密钥请登录控制台生成"},
		CallTrend7d:     trend,
		Notice:          item.DocNotice,
		Parameters:      parseDocArray(item.DocRequestParams),
		PreferredMethod: preferredMethod(item.DocPreferredMethod, item.RequestMethod),
		Pricing:         map[string]any{"label": item.PriceLabel, "price": item.Price, "pointPrice": item.PointPrice, "description": "正式扣费规则将在计费模块生效"},
		ResponseExample: item.DocResponseExample,
		ResponseFields:  parseDocArray(item.DocResponseFields),
		ResponseFormats: publicDocResponseFormats(item),
		StatusCodes:     statusCodes(item.DocStatusCodes),
	}, nil
}

func (s *Service) localizeInterfaceItems(ctx context.Context, items []InterfaceItem) {
	if s.localizer == nil || len(items) == 0 {
		return
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.ID)
	}
	overrides := s.localizer.LocalizeBatch(ctx, "interface", keys)
	for index := range items {
		applyInterfaceTranslations(&items[index], overrides[items[index].ID], false)
	}
}

func (s *Service) localizeInterfaceItem(ctx context.Context, item *InterfaceItem, detail bool) {
	if s.localizer == nil || item == nil {
		return
	}
	applyInterfaceTranslations(item, s.localizer.Localize(ctx, "interface", item.ID), detail)
}

func applyInterfaceTranslations(item *InterfaceItem, translations map[string]string, detail bool) {
	if len(translations) == 0 {
		return
	}
	if value, ok := translations["name"]; ok {
		item.Name = value
	}
	if value, ok := translations["remark"]; ok {
		item.Remark = value
		item.Description = value
	}
	if detail {
		if value, ok := translations["docSummary"]; ok {
			item.DocSummary = value
		}
		if value, ok := translations["docNotice"]; ok {
			item.DocNotice = value
		}
	}
}

func (s *Service) MarketTestKey(ctx context.Context, interfaceID int64, token string) (MarketTestKey, error) {
	if s.keyProvider == nil || strings.TrimSpace(token) == "" {
		return MarketTestKey{HasKey: false, KeyScope: "", Keys: []keyaccess.InvokeKeyOption{}, Message: "暂无可用Key，请先登录并创建API Key"}, nil
	}
	principal, err := s.Authenticate(ctx, token)
	if err != nil {
		return MarketTestKey{HasKey: false, KeyScope: "", Keys: []keyaccess.InvokeKeyOption{}, Message: "暂无可用Key，请先登录并创建API Key"}, nil
	}
	items, err := s.keyProvider.ListInvokeKeys(ctx, principal.ID, interfaceID, "")
	if err != nil {
		return MarketTestKey{}, err
	}
	if len(items) == 0 {
		return MarketTestKey{HasKey: false, KeyScope: "", Keys: []keyaccess.InvokeKeyOption{}, Message: "暂无可用Key，请先创建或绑定API Key"}, nil
	}
	return MarketTestKey{
		HasKey:    true,
		KeyScope:  items[0].KeyScope,
		Keys:      items,
		MaskedKey: items[0].MaskedKey,
		Message:   "已找到可用API Key",
	}, nil
}

func (s *Service) InvokeKeys(ctx context.Context, principal Principal, interfaceID int64, keyword string) ([]keyaccess.InvokeKeyOption, error) {
	if err := s.require(ctx, principal, "interface:invoke"); err != nil {
		return nil, err
	}
	if _, err := s.repo.ByID(ctx, interfaceID); err != nil {
		return nil, mapNotFound(err, "接口不存在")
	}
	keyword = strings.TrimSpace(keyword)
	if utf8.RuneCountInString(keyword) > 64 {
		return nil, apperrors.New(consts.StatusBadRequest, "密钥名称搜索不能超过64个字符")
	}
	if s.keyProvider == nil {
		return nil, apperrors.New(consts.StatusServiceUnavailable, "密钥列表服务不可用")
	}
	return s.keyProvider.ListInvokeKeys(ctx, principal.ID, interfaceID, keyword)
}

func (s *Service) ValidateUploadPermission(ctx context.Context, principal Principal) error {
	return s.require(ctx, principal, "interface:upload-avatar")
}

func (s *Service) normalizeSave(ctx context.Context, req SaveInterfaceRequest, excludeID int64) (InterfaceItem, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > 128 {
		return InterfaceItem{}, apperrors.New(consts.StatusBadRequest, "接口名称不能为空且不能超过128个字符")
	}
	apiCode := strings.TrimSpace(req.APICode)
	if apiCode == "" {
		apiCode = "api_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	if !apiCodePattern.MatchString(apiCode) {
		return InterfaceItem{}, apperrors.New(consts.StatusBadRequest, "接口唯一编码只能包含字母、数字、下划线和短横线，长度3到64位")
	}
	if reservedAPICodes[strings.ToLower(apiCode)] {
		return InterfaceItem{}, apperrors.New(consts.StatusBadRequest, "接口唯一编码不能占用系统或开放保留字")
	}
	exists, err := s.repo.APICodeExists(ctx, apiCode, excludeID)
	if err != nil {
		return InterfaceItem{}, err
	}
	if exists {
		return InterfaceItem{}, apperrors.New(consts.StatusConflict, "接口唯一编码已存在")
	}
	requestURL := strings.TrimSpace(req.RequestURL)
	if err := validateHTTPURL(requestURL, 1024, "接口地址"); err != nil {
		return InterfaceItem{}, err
	}
	method, err := normalizeRequestMethod(req.RequestMethod)
	if err != nil {
		return InterfaceItem{}, err
	}
	price, err := normalizeMoney(req.Price)
	if err != nil {
		return InterfaceItem{}, err
	}
	pointPrice, err := normalizePoints(req.PointPrice)
	if err != nil {
		return InterfaceItem{}, err
	}
	if price == "0.0000" {
		pointPrice = 0
	}
	qpsLimit, err := normalizeInterfaceQPSLimit(req.QPSLimit)
	if err != nil {
		return InterfaceItem{}, err
	}
	sharedQPSLimit, err := normalizeSharedQPSLimit(req.SharedQPSLimit)
	if err != nil {
		return InterfaceItem{}, err
	}
	avatarURL := strings.TrimSpace(req.AvatarURL)
	if err := validateAvatarURL(avatarURL, s.systemPrefix); err != nil {
		return InterfaceItem{}, err
	}
	remark := strings.TrimSpace(req.Remark)
	if remark == "" || utf8.RuneCountInString(remark) > 255 {
		return InterfaceItem{}, apperrors.New(consts.StatusBadRequest, "描述不能为空且不能超过255个字符")
	}
	status := flagValue(req.Status, 1)
	isTop := flagValue(req.IsTop, 0)
	isFeatured := flagValue(req.IsFeatured, 0)
	if status < 0 || isTop < 0 || isFeatured < 0 {
		return InterfaceItem{}, apperrors.New(consts.StatusBadRequest, "状态值不合法")
	}
	item := InterfaceItem{
		APICode: apiCode, AvatarURL: avatarURL, IsFeatured: isFeatured, IsTop: isTop, Name: name,
		PointPrice: pointPrice, Price: price, SharedQPSLimit: sharedQPSLimit, QPSLimit: qpsLimit, Remark: remark, RequestMethod: method, RequestURL: requestURL, Status: status,
	}
	if hasPollingPayload(req) {
		cfg, err := normalizePolling(pollingRequestFromSave(req))
		if err != nil {
			return InterfaceItem{}, err
		}
		applyPollingConfig(&item, cfg)
	}
	return item, nil
}

func normalizeInterfaceQPSLimit(value *int) (int, error) {
	if value == nil {
		return 0, nil
	}
	if *value < -1 || *value > maxInterfaceQPSLimit {
		return 0, apperrors.New(consts.StatusBadRequest, "接口 QPS 上限必须为 -1，或在 0 到 1000000 之间")
	}
	return *value, nil
}

// normalizeSharedQPSLimit validates the shared (interface-wide) QPS ceiling.
// Unlike the per-user limit, -1 is not accepted here — 0 already means
// "unlimited" for the shared bucket.
func normalizeSharedQPSLimit(value *int) (int, error) {
	if value == nil {
		return 0, nil
	}
	if *value < 0 || *value > maxInterfaceQPSLimit {
		return 0, apperrors.New(consts.StatusBadRequest, "接口共享 QPS 上限必须在 0 到 1000000 之间")
	}
	return *value, nil
}

func (s *Service) require(ctx context.Context, principal Principal, code string) error {
	return s.authz.Require(ctx, rbac.Principal{ID: principal.ID, Username: principal.Username, Roles: principal.Roles, TokenVersion: principal.TokenVersion}, code)
}

func hasPollingPayload(req SaveInterfaceRequest) bool {
	return req.PollingEnabled != nil ||
		req.PollingCheckEnabled != nil ||
		req.ResponseCleaningEnabled != nil ||
		req.ResponseFieldMappings != nil ||
		strings.TrimSpace(req.PollingMode) != "" ||
		strings.TrimSpace(req.PollingCheckField) != "" ||
		strings.TrimSpace(req.PollingCheckExpected) != "" ||
		strings.TrimSpace(req.PollingStopCode) != "" ||
		req.ProxyPoolConfig.Enabled != 0 ||
		req.ProxyPoolConfig.Source != "" ||
		req.ProxyPoolConfig.PasswordConfigured ||
		req.DomainRouting != nil ||
		len(req.UpstreamConfigs) > 0 ||
		len(req.UpstreamURLs) > 0
}

func pollingRequestFromSave(req SaveInterfaceRequest) SavePollingConfig {
	pollingEnabled := req.PollingEnabled
	if pollingEnabled == nil && (len(req.UpstreamConfigs) > 0 || len(req.UpstreamURLs) > 0) {
		enabled := 1
		pollingEnabled = &enabled
	}
	mode := req.PollingMode
	if strings.TrimSpace(mode) == "" && (len(req.UpstreamConfigs) > 0 || len(req.UpstreamURLs) > 0) {
		mode = "FASTEST"
	}
	return SavePollingConfig{
		DomainRouting:           req.DomainRouting,
		PollingCheckEnabled:     req.PollingCheckEnabled,
		PollingCheckExpected:    req.PollingCheckExpected,
		PollingCheckField:       req.PollingCheckField,
		PollingEnabled:          pollingEnabled,
		PollingMode:             mode,
		PollingStopCode:         req.PollingStopCode,
		ProxyPoolConfig:         req.ProxyPoolConfig,
		ResponseCleaningEnabled: req.ResponseCleaningEnabled,
		ResponseFieldMappings:   req.ResponseFieldMappings,
		UpstreamConfigs:         req.UpstreamConfigs,
		UpstreamURLs:            req.UpstreamURLs,
	}
}

func applyPollingConfig(item *InterfaceItem, cfg PollingConfig) {
	item.DomainRouting = cfg.DomainRouting
	item.PollingEnabled = cfg.PollingEnabled
	item.PollingMode = cfg.PollingMode
	item.PollingCheckEnabled = cfg.PollingCheckEnabled
	item.PollingCheckField = cfg.PollingCheckField
	item.PollingCheckExpected = cfg.PollingCheckExpected
	item.PollingStopCode = cfg.PollingStopCode
	item.ProxyPoolConfig = cfg.ProxyPoolConfig
	item.ResponseCleaningEnabled = cfg.ResponseCleaningEnabled
	item.ResponseFieldMappings = cfg.ResponseFieldMappings
	item.UpstreamConfigs = cfg.UpstreamConfigs
	item.UpstreamURLs = upstreamURLs(cfg.UpstreamConfigs)
	item.UpstreamCount = displayUpstreamCount(*item)
}

func copyPollingConfig(target *InterfaceItem, source InterfaceItem) {
	target.DomainRouting = source.DomainRouting
	target.PollingEnabled = source.PollingEnabled
	target.PollingMode = source.PollingMode
	target.PollingCheckEnabled = source.PollingCheckEnabled
	target.PollingCheckField = source.PollingCheckField
	target.PollingCheckExpected = source.PollingCheckExpected
	target.PollingStopCode = source.PollingStopCode
	target.ProxyPoolConfig = source.ProxyPoolConfig
	target.ResponseCleaningEnabled = source.ResponseCleaningEnabled
	target.ResponseFieldMappings = source.ResponseFieldMappings
	target.UpstreamConfigs = source.UpstreamConfigs
	target.UpstreamURLs = source.UpstreamURLs
	target.UpstreamCount = source.UpstreamCount
	target.CurrentNode = source.CurrentNode
	target.CurrentNodeURL = source.CurrentNodeURL
}

// preserveMissingPollingCheck 在接口基础信息编辑时补齐请求未携带的响应校验字段。
//
// PUT /interface/:id 的 payload 只要出现任意一个轮询相关字段，normalizeSave 就会
// 用请求内容整体重建轮询配置：PollingCheckEnabled 为 nil 会落成 0，
// PollingCheckField/PollingCheckExpected/PollingStopCode 为空串会被写回 NULL。
// 结果是「只调整了上游地址或轮询模式」的一次保存，把失败校验规则一并清空。
//
// 这里按字段粒度回填库中现值：nil / 空串一律视为「本次请求未提交该字段」。
// 需要真正清空这些字段时，请走轮询编辑器 SavePolling（整份替换语义）。
func preserveMissingPollingCheck(req *SaveInterfaceRequest, current InterfaceItem) {
	if req == nil {
		return
	}
	// A route-only/response-only update must not treat backfilled upstreams as
	// newly supplied polling intent. Preserve the current runtime state before
	// preserveMissingResponseFieldMappings copies those upstreams into req.
	if req.PollingEnabled == nil && strings.TrimSpace(req.PollingMode) == "" && req.UpstreamConfigs == nil && req.UpstreamURLs == nil {
		enabled := boolInt(pollingRuntimeEnabled(current))
		req.PollingEnabled = &enabled
		req.PollingMode = normalizedPollingMode(current.PollingMode, enabled)
	}
	if req.PollingCheckEnabled == nil {
		value := current.PollingCheckEnabled
		req.PollingCheckEnabled = &value
	}
	if strings.TrimSpace(req.PollingCheckField) == "" {
		req.PollingCheckField = current.PollingCheckField
	}
	if strings.TrimSpace(req.PollingCheckExpected) == "" {
		req.PollingCheckExpected = current.PollingCheckExpected
	}
	if strings.TrimSpace(req.PollingStopCode) == "" {
		req.PollingStopCode = current.PollingStopCode
	}
	// URL-only legacy forms do not own response-check fields. Preserve each
	// retained node by URL, while allowing explicit JSON toggles (including 0).
	if len(req.UpstreamConfigs) == 0 && len(req.UpstreamURLs) > 0 {
		for _, rawURL := range req.UpstreamURLs {
			req.UpstreamConfigs = append(req.UpstreamConfigs, UpstreamConfig{URL: rawURL, pollingCheckMissing: true})
		}
	}
	currentByURL := make(map[string]UpstreamConfig, len(current.UpstreamConfigs))
	for _, config := range current.UpstreamConfigs {
		currentByURL[strings.TrimSpace(config.URL)] = config
	}
	for index := range req.UpstreamConfigs {
		config := &req.UpstreamConfigs[index]
		stored, exists := currentByURL[strings.TrimSpace(config.URL)]
		if !exists {
			continue
		}
		if config.pollingCheckMissing {
			config.PollingCheckEnabled = stored.PollingCheckEnabled
		}
		if strings.TrimSpace(config.PollingCheckField) == "" {
			config.PollingCheckField = stored.PollingCheckField
		}
		if strings.TrimSpace(config.PollingCheckExpected) == "" {
			config.PollingCheckExpected = stored.PollingCheckExpected
		}
	}
}

func preserveMissingProxyPoolConfig(target *proxypool.Config, current proxypool.Config) {
	if target.Enabled == 0 && strings.TrimSpace(target.Source) == "" && strings.TrimSpace(target.Password) == "" && !target.PasswordConfigured {
		*target = current
	}
}

func preserveMissingDomainRouting(target **DomainRoutingConfig, current DomainRoutingConfig) {
	if target == nil || *target != nil {
		return
	}
	if current.Enabled == 0 && strings.TrimSpace(current.Fallback) == "" && strings.TrimSpace(current.DefaultRouteID) == "" && len(current.Routes) == 0 {
		return
	}
	copyValue := current
	copyValue.Routes = append([]DomainPollingRoute(nil), current.Routes...)
	for index, route := range copyValue.Routes {
		// Only omitted routing is backfilled here. Materialize legacy runtime
		// inference before save normalization, without overriding a newly
		// submitted switch or an existing explicit SINGLE opt-out.
		effective := applyInterfaceDomainRoute(InterfaceItem{}, route)
		copyValue.Routes[index].PollingEnabled = boolInt(pollingRuntimeEnabled(effective))
		copyValue.Routes[index].PollingMode = effective.PollingMode
	}
	*target = &copyValue
}

func mapProxySecretError(err error) error {
	if errors.Is(err, secretbox.ErrSecretMissing) {
		return apperrors.New(consts.StatusServiceUnavailable, "SYSTEM_CONFIG_SECRET 未配置或长度不足32字节")
	}
	if errors.Is(err, secretbox.ErrDecrypt) {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "代理密码无法解密，请检查 SYSTEM_CONFIG_SECRET", err)
	}
	return err
}

func preserveMissingResponseFieldMappings(primary *[]jsoncleaning.FieldMapping, configs *[]UpstreamConfig, legacyURLs []string, current InterfaceItem) {
	if *primary == nil {
		*primary = append([]jsoncleaning.FieldMapping(nil), current.ResponseFieldMappings...)
	}
	if *configs == nil && legacyURLs == nil {
		*configs = append([]UpstreamConfig(nil), current.UpstreamConfigs...)
	}
	if len(*configs) == 0 && len(legacyURLs) > 0 {
		converted := make([]UpstreamConfig, 0, len(legacyURLs))
		for _, rawURL := range legacyURLs {
			converted = append(converted, UpstreamConfig{URL: rawURL})
		}
		*configs = converted
	}
	currentByURL := make(map[string][]jsoncleaning.FieldMapping, len(current.UpstreamConfigs))
	for _, config := range current.UpstreamConfigs {
		currentByURL[strings.TrimSpace(config.URL)] = config.ResponseFieldMappings
	}
	for index := range *configs {
		if (*configs)[index].ResponseFieldMappings != nil {
			continue
		}
		if mappings, exists := currentByURL[strings.TrimSpace((*configs)[index].URL)]; exists {
			(*configs)[index].ResponseFieldMappings = append([]jsoncleaning.FieldMapping(nil), mappings...)
		}
	}
}

func (s *Service) orderCandidatesByHealth(ctx context.Context, apiID int64, configs []UpstreamConfig, routeScope ...string) []UpstreamConfig {
	type candidate struct {
		config    UpstreamConfig
		hasMetric bool
		metric    upstreamhealth.Metric
	}
	items := make([]candidate, 0, len(configs))
	urls := make([]string, len(configs))
	for index, cfg := range configs {
		urls[index] = cfg.URL
	}
	readCtx, cancel := pollingHealthOperationContext(ctx, len(configs))
	scope := optionalDomainRouteScope(routeScope)
	metrics, err := upstreamhealth.ReadManyScoped(readCtx, s.rdb, apiID, scope, urls)
	cancel()
	if err != nil {
		s.warnUpstreamHealth("read", apiID, err)
		metrics = make([]upstreamhealth.ReadResult, len(configs))
	}
	for index, cfg := range configs {
		metric := metrics[index]
		if metric.Err != nil {
			s.warnUpstreamHealth("read", apiID, metric.Err)
		}
		items = append(items, candidate{config: cfg, hasMetric: metric.Found && metric.Err == nil, metric: metric.Metric})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return upstreamhealth.Compare(items[i].metric, items[i].hasMetric, items[j].metric, items[j].hasMetric) < 0
	})
	ordered := make([]UpstreamConfig, 0, len(items))
	for _, item := range items {
		ordered = append(ordered, item.config)
	}
	return ordered
}

// pollingHealthOperationContext bounds advisory health I/O independently from
// the upstream invocation slices. With a caller deadline, at most one fair
// share of the remaining budget (plus one share reserved for the candidates)
// is spent on Redis metadata. This prevents a slow Redis pipeline in FASTEST
// mode from exhausting the request before any upstream request is issued.
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

func (s *Service) recordUpstreamHealth(ctx context.Context, apiID int64, upstreamURL string, healthy bool, latencyMs int64, routeScope ...string) {
	if s == nil {
		return
	}
	if ctx != nil && ctx.Err() != nil {
		return
	}
	baseCtx := context.Background()
	if ctx != nil {
		baseCtx = context.WithoutCancel(ctx)
	}
	writeCtx, cancel := context.WithTimeout(baseCtx, upstreamHealthWriteTimeout)
	defer cancel()
	scope := optionalDomainRouteScope(routeScope)
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

// recordUpstreamHealthAsync persists an invoke result without consuming the
// caller's remaining polling deadline. The detached operation has its own
// short timeout and a bounded slot pool; dropping a sample when the pool is
// full is safe because health data is advisory and the next probe can refresh
// it.
func (s *Service) recordUpstreamHealthAsync(apiID int64, upstreamURL string, healthy bool, latencyMs int64, routeScope ...string) {
	if s == nil {
		return
	}
	select {
	case upstreamHealthWriteSlots <- struct{}{}:
	default:
		return
	}
	go func() {
		defer func() { <-upstreamHealthWriteSlots }()
		s.recordUpstreamHealth(context.Background(), apiID, upstreamURL, healthy, latencyMs, routeScope...)
	}()
}

func optionalDomainRouteScope(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return strings.TrimSpace(values[0])
}

func (s *Service) warnUpstreamHealth(operation string, apiID int64, err error) {
	if s.log == nil || err == nil {
		return
	}
	s.log.Warn("upstream health metric failure", zap.String("operation", operation), zap.Int64("api_id", apiID), zap.String("error", masking.Error(err)))
}

func nodeIndexForInterface(item InterfaceItem, upstreamURL string) int {
	if strings.TrimSpace(upstreamURL) == strings.TrimSpace(item.RequestURL) {
		return 1
	}
	for index, cfg := range item.UpstreamConfigs {
		if strings.TrimSpace(cfg.URL) == strings.TrimSpace(upstreamURL) {
			return index + 2
		}
	}
	return 1
}

func (s *Service) enrichUpstreamHealth(ctx context.Context, item *InterfaceItem) {
	if item == nil || s.rdb == nil {
		return
	}
	apiID := parseID(item.ID)
	enrichConfigs := func(configs []UpstreamConfig, routeScope string) {
		urls := make([]string, len(configs))
		for index := range configs {
			urls[index] = configs[index].URL
		}
		metrics, err := upstreamhealth.ReadManyScoped(ctx, s.rdb, apiID, routeScope, urls)
		if err != nil {
			s.warnUpstreamHealth("read", apiID, err)
			metrics = make([]upstreamhealth.ReadResult, len(configs))
		}
		for i := range configs {
			metric := metrics[i]
			if metric.Err != nil || !metric.Found {
				if metric.Err != nil {
					s.warnUpstreamHealth("read", apiID, metric.Err)
				}
				configs[i].HealthStatus = upstreamhealth.StatusUnknown
				continue
			}
			configs[i].HealthStatus = metric.Metric.Status
			configs[i].LastLatencyMs = metric.Metric.LatencyMs
			configs[i].LastCheckedAt = metric.Metric.CheckedAt
			configs[i].FailureCount = metric.Metric.FailureCount
		}
	}
	enrichConfigs(item.UpstreamConfigs, "")
	if item.DomainRouting.Enabled != 1 || len(item.DomainRouting.Routes) == 0 {
		return
	}
	routes, err := normalizedRuntimeDomainRoutes(item.DomainRouting.Routes)
	if err != nil {
		s.warnUpstreamHealth("read", apiID, err)
		return
	}
	for index, route := range routes {
		enrichConfigs(route.UpstreamConfigs, route.ID)
		item.DomainRouting.Routes[index].UpstreamConfigs = route.UpstreamConfigs
	}
}

func (s *Service) invokeUpstreams(ctx context.Context, item InterfaceItem, method string, req InvokePayload) (result InvokeResult, err error) {
	ctx = requestid.Ensure(ctx)
	defer func() { result.RequestID = requestid.FromContext(ctx) }()
	routedItem, _, routeErr := resolveInterfaceDomainRoute(item, req)
	if routeErr != nil {
		status := consts.StatusServiceUnavailable
		var business *apperrors.BusinessError
		if errors.As(routeErr, &business) {
			status = business.StatusCode
		}
		return InvokeResult{RequestMethod: method, StatusCode: status}, routeErr
	}
	item = routedItem
	requestCtx, cancel := context.WithTimeout(ctx, requesttimeout.Duration(item.RequestTimeoutMs, 0, 1))
	defer cancel()
	ctx = requestCtx
	// Older rows may carry a concrete polling mode/upstream pool or a
	// stop-code contract while the enable bit remained zero. Resolve that
	// legacy shape at runtime so non-terminal business responses always move
	// to the next candidate; an explicit disabled SINGLE mode remains closed.
	runtimeEnabled := pollingRuntimeEnabled(item)
	runtimeMode := normalizedPollingMode(item.PollingMode, boolInt(runtimeEnabled))
	candidates := upstreamCandidates(item)
	if len(candidates) == 0 {
		candidates = []UpstreamConfig{{URL: item.RequestURL, PollingCheckEnabled: item.PollingCheckEnabled, PollingCheckField: item.PollingCheckField, PollingCheckExpected: item.PollingCheckExpected, ResponseFieldMappings: item.ResponseFieldMappings}}
	}
	if runtimeMode == "FASTEST" && len(candidates) > 1 {
		candidates = s.orderCandidatesByHealth(ctx, parseID(item.ID), candidates, item.domainRouteID)
	}
	startIndex := 0
	if runtimeMode == "ROUND_ROBIN" && len(candidates) > 1 {
		if s.rdb != nil {
			// The round-robin cursor is advisory metadata. Bound its Redis
			// operation to a fair slice of the request budget so a degraded
			// Redis cannot prevent the actual upstream polling chain from
			// issuing its first candidate.
			counterCtx, counterCancel := pollingHealthOperationContext(ctx, len(candidates))
			next, err := redisutil.IncrWithTTL(counterCtx, s.rdb, interfaceRoundRobinKey(item.ID, item.domainRouteID), 7*24*time.Hour)
			counterCancel()
			if err == nil {
				startIndex = int((next - 1) % int64(len(candidates)))
			}
		} else {
			startIndex = item.CurrentNode - 1
			if startIndex < 0 || startIndex >= len(candidates) {
				startIndex = 0
			}
		}
	}
	var last InvokeResult
	var lastErr error
	for offset := 0; offset < len(candidates); offset++ {
		// Keep the interface timeout as a hard upper bound while reserving a
		// dynamic slice for every candidate that remains.  A single shared
		// context lets a slow primary consume the complete deadline, causing the
		// loop to return before the configured backup is attempted.
		// Preserve the first-candidate cancellation result for compatibility;
		// after one attempt, a canceled parent must not start another request.
		if offset > 0 {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return last, ctxErr
			}
		}
		attemptCtx, attemptCancel := pollingAttemptContext(ctx, len(candidates)-offset)
		index := (startIndex + offset) % len(candidates)
		cfg := candidates[index]
		res, err := s.invokeOne(attemptCtx, item, method, cfg, req)
		attemptCancel()
		res.PollingMode = runtimeMode
		res.UpstreamSwitched = offset > 0
		last = res
		// A child slice timeout is an ordinary failed candidate and should move
		// to the next node.  Only an error on the parent context is terminal.
		if parentErr := ctx.Err(); parentErr != nil && err != nil {
			return last, parentErr
		}
		var business *apperrors.BusinessError
		if errors.As(err, &business) {
			return last, err
		}
		// Check terminal business codes against the raw response before optional
		// cleaning. Normalization may rewrite legacy values (for example 0 to
		// 200) or move fields, which must not turn a configured terminal response
		// into an apparent success on the first node.
		if shouldStopPolling(item, &res) {
			res.Success = false
			return res, nil
		}
		passed := false
		var cleaningErr error
		if err == nil {
			// A node-level check set to zero means that the node has no
			// override.  Apply the interface-wide check/stop-code policy to
			// every candidate so a non-success business code cannot make a
			// backup node look successful merely because its optional fields
			// were omitted.
			passed, cleaningErr = prepareInvokeResponse(item.ResponseCleaningEnabled, effectivePollingCheck(item, cfg), &res)
		}
		// A configured stop code (or the legacy HTTP 404 default) is a
		// terminal business response, not an unhealthy node.  Check this
		// before recording health and before the normal success/fallback
		// branches so it is passed through unchanged.
		if err == nil && shouldStopPolling(item, &res) {
			res.Success = false
			return res, nil
		}
		s.recordUpstreamHealthAsync(parseID(item.ID), cfg.URL, passed, res.ElapsedMs, item.domainRouteID)
		if cleaningErr != nil {
			lastErr = cleaningErr
			continue
		}
		if passed {
			res.Success = httpstatus.IsSuccess(res.StatusCode)
			if runtimeMode == "ROUND_ROBIN" || runtimeMode == "FASTEST" {
				next := index + 2
				if next > len(candidates) {
					next = 1
				}
				if runtimeMode == "FASTEST" {
					next = nodeIndexForInterface(item, cfg.URL)
				}
				if s.repo != nil && item.domainRouteID == "" {
					if err := s.repo.UpdateCurrentNode(ctx, parseID(item.ID), next); err != nil && s.log != nil {
						s.log.Warn("update interface current upstream node failed", zap.Int64("api_id", parseID(item.ID)), zap.String("error", masking.Error(err)))
					}
				}
			}
			return res, nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("响应未通过上游校验")
		}
	}
	// 与 gateway 保持一致：轮询耗尽后必须清除 invokeOne 基于 HTTP 状态码置位的
	// Success，避免调用方把「HTTP 2xx 但业务校验失败」当成成功调用。
	last.Success = false
	return last, lastErr
}

func interfaceRoundRobinKey(interfaceID, routeScope string) string {
	interfaceID = strings.TrimSpace(interfaceID)
	routeScope = strings.TrimSpace(routeScope)
	if routeScope == "" {
		return fmt.Sprintf("interface:invoke:rr:%s", interfaceID)
	}
	return fmt.Sprintf("interface:invoke:rr:%s:route:%s", interfaceID, routeScope)
}

// pollingAttemptContext creates a fresh child deadline for one fallback
// candidate.  The parent deadline remains the hard cap for the whole invoke;
// the remaining time is divided by the number of candidates still pending.
// This prevents one slow node from starving all configured backups while
// allowing fast failures to pass their unused budget to the next node.
func pollingAttemptContext(parent context.Context, remainingCandidates int) (context.Context, context.CancelFunc) {
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
		// invokeUpstreams always installs a deadline; retain safe cancellation
		// semantics for direct callers and unit tests that use this helper.
		return context.WithCancel(parent)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.WithDeadline(parent, deadline)
	}
	budget := remaining / time.Duration(remainingCandidates)
	if budget <= 0 {
		budget = remaining
	}
	attemptDeadline := time.Now().Add(budget)
	if attemptDeadline.After(deadline) {
		attemptDeadline = deadline
	}
	return context.WithDeadline(parent, attemptDeadline)
}

func (s *Service) invokeOne(ctx context.Context, item InterfaceItem, method string, cfg UpstreamConfig, payload InvokePayload) (result InvokeResult, err error) {
	ctx = requestid.Ensure(ctx)
	defer func() { result.RequestID = requestid.FromContext(ctx) }()
	target, err := applyTemplate(cfg.URL, payload.QueryParams)
	if err != nil {
		status := 0
		var business *apperrors.BusinessError
		if errors.As(err, &business) {
			status = business.StatusCode
		}
		return InvokeResult{RequestMethod: method, StatusCode: status, UpstreamURL: cfg.URL}, err
	}
	var body io.Reader
	if method == "POST" {
		data, err := json.Marshal(payload.Body)
		if err != nil {
			return InvokeResult{RequestMethod: method, UpstreamURL: target}, err
		}
		if string(data) == "null" {
			data = []byte("{}")
		}
		body = bytes.NewReader(data)
	} else {
		target = appendQuery(target, payload.QueryParams)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return InvokeResult{RequestMethod: method, UpstreamURL: target}, err
	}
	if method == "POST" {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set(requestid.Header, requestid.FromContext(ctx))
	client := s.httpClient
	if item.ProxyPoolConfig.Enabled == 1 {
		resolver := s.proxyResolver
		if resolver == nil {
			resolver = proxypool.NewResolver(s.httpClient)
		}
		client, err = resolver.Client(ctx, s.httpClient, item.ProxyPoolConfig)
		if err != nil {
			return InvokeResult{RequestMethod: method, UpstreamURL: target}, fmt.Errorf("resolve upstream proxy: %w", err)
		}
	}
	start := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return InvokeResult{RequestMethod: method, UpstreamURL: target, ElapsedMs: time.Since(start).Milliseconds()}, err
	}
	defer response.Body.Close()
	data, err := httpclient.ReadBody(response.Body, httpclient.MaxUpstreamResponseBytes)
	if err != nil {
		return InvokeResult{RequestMethod: method, UpstreamURL: target, ElapsedMs: time.Since(start).Milliseconds(), StatusCode: consts.StatusBadGateway}, fmt.Errorf("read upstream response: %w", err)
	}
	return InvokeResult{
		Body:          string(data),
		ElapsedMs:     time.Since(start).Milliseconds(),
		RequestMethod: method,
		ResponseSize:  int64(len(data)),
		StatusCode:    response.StatusCode,
		Success:       httpstatus.IsSuccess(response.StatusCode),
		UpstreamURL:   target,
	}, nil
}

func upstreamCandidates(item InterfaceItem) []UpstreamConfig {
	base := UpstreamConfig{URL: item.RequestURL, PollingCheckEnabled: item.PollingCheckEnabled, PollingCheckField: item.PollingCheckField, PollingCheckExpected: item.PollingCheckExpected, ResponseFieldMappings: item.ResponseFieldMappings}
	enabled := pollingRuntimeEnabled(item)
	if !enabled {
		return []UpstreamConfig{base}
	}
	mode := normalizedPollingMode(item.PollingMode, boolInt(enabled))
	switch mode {
	case "PRIMARY":
		return append([]UpstreamConfig{base}, item.UpstreamConfigs...)
	case "FASTEST":
		return append([]UpstreamConfig{base}, item.UpstreamConfigs...)
	case "ROUND_ROBIN":
		if len(item.UpstreamConfigs) > 0 {
			return item.UpstreamConfigs
		}
	}
	return []UpstreamConfig{base}
}

// normalizedPollingMode keeps runtime invocation compatible with values
// persisted before polling mode validation was introduced. Empty mode on an
// enabled interface follows normalizePollingMode's ROUND_ROBIN default.
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
// invocation boundary. An explicit SINGLE mode with the enable bit cleared
// is an intentional opt-out and wins over stale upstream rows; an omitted
// mode with a pool, a concrete non-SINGLE mode, or a stop-code contract
// indicates that the old row intended to participate in polling.
func pollingRuntimeEnabled(item InterfaceItem) bool {
	if item.PollingEnabled == 1 {
		return true
	}
	mode := strings.ToUpper(strings.TrimSpace(item.PollingMode))
	if mode == "SINGLE" {
		return false
	}
	if strings.TrimSpace(item.PollingStopCode) != "" {
		return true
	}
	switch mode {
	case "PRIMARY", "FASTEST", "ROUND_ROBIN", "RANDOM":
		return true
	case "":
		return len(item.UpstreamConfigs) > 0
	default:
		return false
	}
}

func pollingConfig(item InterfaceItem) PollingConfig {
	return PollingConfig{
		CurrentNode: item.CurrentNode, CurrentNodeURL: item.CurrentNodeURL, ID: item.ID, Name: item.Name,
		DomainRouting:       item.DomainRouting,
		PollingCheckEnabled: item.PollingCheckEnabled, PollingCheckExpected: defaultText(item.PollingCheckExpected, "200"),
		PollingCheckField: defaultText(item.PollingCheckField, "code"), PollingEnabled: item.PollingEnabled, PollingMode: item.PollingMode,
		PollingStopCode:         item.PollingStopCode,
		ResponseCleaningEnabled: item.ResponseCleaningEnabled,
		ResponseFieldMappings:   item.ResponseFieldMappings,
		ProxyPoolConfig:         item.ProxyPoolConfig,
		RequestURL:              item.RequestURL, UpstreamConfigs: item.UpstreamConfigs, UpstreamCount: item.UpstreamCount, UpstreamURLs: item.UpstreamURLs,
	}
}

func docConfig(item InterfaceItem) DocConfig {
	return DocConfig{
		APICode: item.APICode, DocNotice: item.DocNotice, DocPreferredMethod: item.DocPreferredMethod,
		DocRequestParams: item.DocRequestParams, DocResponseExample: item.DocResponseExample, DocResponseFields: item.DocResponseFields,
		DocResponseType: item.DocResponseType, DocStatusCodes: item.DocStatusCodes, DocSummary: item.DocSummary,
		ID: item.ID, Name: item.Name, RequestMethod: item.RequestMethod, RequestURL: item.RequestURL, TemplateParameters: parameterNames(item.RequestURL),
		DocResponseFormats: parseDocResponseFormats(item.DocResponseFormats),
	}
}

func normalizePolling(req SavePollingConfig) (PollingConfig, error) {
	enabled := flagValue(req.PollingEnabled, 0)
	checkEnabled := flagValue(req.PollingCheckEnabled, 0)
	cleaningEnabled := flagValue(req.ResponseCleaningEnabled, 0)
	if enabled < 0 || checkEnabled < 0 || cleaningEnabled < 0 {
		return PollingConfig{}, apperrors.New(consts.StatusBadRequest, "轮询状态值不合法")
	}
	mode, err := normalizePollingMode(req.PollingMode, enabled)
	if err != nil {
		return PollingConfig{}, err
	}
	// Persist an explicit disabled switch as the canonical SINGLE mode.  The
	// runtime uses non-SINGLE modes to recognize incomplete historical rows
	// whose enable bit was accidentally left at zero; without canonicalizing
	// new saves, deliberately turning polling off while the form still carries
	// PRIMARY/ROUND_ROBIN would be indistinguishable from those legacy rows.
	if enabled == 0 {
		mode = "SINGLE"
	}
	field := defaultText(strings.TrimSpace(req.PollingCheckField), "code")
	expected := defaultText(strings.TrimSpace(req.PollingCheckExpected), "200")
	stopCode := strings.TrimSpace(req.PollingStopCode)
	if utf8.RuneCountInString(field) > 128 || utf8.RuneCountInString(expected) > 255 {
		return PollingConfig{}, apperrors.New(consts.StatusBadRequest, "响应校验字段或期望值过长")
	}
	if utf8.RuneCountInString(stopCode) > 64 {
		return PollingConfig{}, apperrors.New(consts.StatusBadRequest, "轮询终止响应码过长")
	}
	primaryMappings, err := normalizeResponseFieldMappings(req.ResponseFieldMappings)
	if err != nil {
		return PollingConfig{}, err
	}
	routing, err := normalizeDomainRouting(req.DomainRouting)
	if err != nil {
		return PollingConfig{}, err
	}
	proxyConfig, err := proxypool.Normalize(req.ProxyPoolConfig)
	if err != nil {
		return PollingConfig{}, apperrors.Wrap(consts.StatusBadRequest, "代理池配置不合法", err)
	}
	configs := req.UpstreamConfigs
	if len(configs) == 0 {
		for _, raw := range req.UpstreamURLs {
			configs = append(configs, UpstreamConfig{URL: raw})
		}
	}
	seen := map[string]bool{}
	normalized := make([]UpstreamConfig, 0, len(configs))
	for _, cfg := range configs {
		urlText := strings.TrimSpace(cfg.URL)
		if urlText == "" || seen[urlText] {
			continue
		}
		if err := validateHTTPURL(urlText, 1024, "上游接口地址"); err != nil {
			return PollingConfig{}, err
		}
		seen[urlText] = true
		cfg.URL = urlText
		cfg.PollingCheckField = defaultText(strings.TrimSpace(cfg.PollingCheckField), "code")
		cfg.PollingCheckExpected = defaultText(strings.TrimSpace(cfg.PollingCheckExpected), "200")
		if utf8.RuneCountInString(cfg.PollingCheckField) > 128 || utf8.RuneCountInString(cfg.PollingCheckExpected) > 255 {
			return PollingConfig{}, apperrors.New(consts.StatusBadRequest, "上游响应校验字段或期望值过长")
		}
		cfg.ResponseFieldMappings, err = normalizeResponseFieldMappings(cfg.ResponseFieldMappings)
		if err != nil {
			// Keep validation details useful without echoing the configured upstream
			// host/path/query back in a 4xx response.
			return PollingConfig{}, apperrors.Wrap(consts.StatusBadRequest, "上游接口 "+masking.ErrorText(urlText)+" 的响应字段映射不合法", err)
		}
		cfg.pollingCheckMissing = false
		cfg.HealthStatus = ""
		cfg.LastCheckedAt = ""
		cfg.LastLatencyMs = 0
		cfg.FailureCount = 0
		if cfg.PollingCheckEnabled != 0 && cfg.PollingCheckEnabled != 1 {
			return PollingConfig{}, apperrors.New(consts.StatusBadRequest, "上游响应校验状态不合法")
		}
		normalized = append(normalized, cfg)
	}
	if len(normalized) > 50 {
		return PollingConfig{}, apperrors.New(consts.StatusBadRequest, "上游接口地址不能超过50个")
	}
	return PollingConfig{PollingEnabled: enabled, PollingMode: mode, PollingCheckEnabled: checkEnabled, PollingCheckField: field, PollingCheckExpected: expected, PollingStopCode: stopCode, ResponseCleaningEnabled: cleaningEnabled, ResponseFieldMappings: primaryMappings, ProxyPoolConfig: proxyConfig, DomainRouting: routing, UpstreamConfigs: normalized}, nil
}

func normalizeResponseFieldMappings(mappings []jsoncleaning.FieldMapping) ([]jsoncleaning.FieldMapping, error) {
	normalized, err := jsoncleaning.NormalizeMappings(mappings)
	if err != nil {
		return nil, apperrors.Wrap(consts.StatusBadRequest, "响应字段映射配置不合法", err)
	}
	return normalized, nil
}

func normalizeDoc(req DocConfig, allowedMethod string) (DocConfig, error) {
	responseType := strings.ToUpper(defaultText(req.DocResponseType, "JSON"))
	if !contains([]string{"JSON", "TEXT", "XML", "HTML", "FILE"}, responseType) {
		return DocConfig{}, apperrors.New(consts.StatusBadRequest, "返回方式只能是JSON、TEXT、XML、HTML或FILE")
	}
	preferred := strings.ToUpper(strings.TrimSpace(req.DocPreferredMethod))
	if preferred != "" {
		if _, err := normalizeInvokeMethod(preferred, allowedMethod); err != nil {
			return DocConfig{}, err
		}
	}
	for _, item := range []struct {
		value string
		name  string
		limit int
		json  bool
	}{
		{req.DocSummary, "文档描述", 500, false},
		{req.DocRequestParams, "请求参数文档", 20000, true},
		{req.DocResponseFields, "返回字段文档", 20000, true},
		{req.DocResponseExample, "返回预览示例", 60000, false},
		{req.DocStatusCodes, "状态码文档", 20000, true},
		{req.DocNotice, "文档提示", 500, false},
	} {
		if utf8.RuneCountInString(strings.TrimSpace(item.value)) > item.limit {
			return DocConfig{}, apperrors.New(consts.StatusBadRequest, item.name+"过长")
		}
		if item.json && strings.TrimSpace(item.value) != "" && !validJSONArray(item.value) {
			return DocConfig{}, apperrors.New(consts.StatusBadRequest, item.name+"必须是JSON数组")
		}
	}
	req.DocResponseType = responseType
	req.DocPreferredMethod = preferred
	formats, err := normalizeDocResponseFormats(req.DocResponseFormats)
	if err != nil {
		return DocConfig{}, err
	}
	req.DocResponseFormats = formats
	return req, nil
}

// docResponseFormatTypes is the closed set of declared public docs response
// formats. SSE covers streaming (Server-Sent Events) upstreams; the gateway
// keeps passing their bytes through unchanged.
var docResponseFormatTypes = []string{"JSON", "TEXT", "XML", "HTML", "FILE", "SSE"}

const (
	docResponseFormatMaxEntries = 5
	docResponseFormatExampleMax = 60000
)

func normalizeDocResponseFormats(formats []DocResponseFormat) ([]DocResponseFormat, error) {
	if formats == nil {
		return nil, nil
	}
	if len(formats) == 0 {
		return nil, apperrors.New(consts.StatusBadRequest, "至少保留一种响应格式")
	}
	if len(formats) > docResponseFormatMaxEntries {
		return nil, apperrors.New(consts.StatusBadRequest, "响应格式不能超过"+strconv.Itoa(docResponseFormatMaxEntries)+"种")
	}
	seen := make(map[string]bool, len(formats))
	normalized := make([]DocResponseFormat, 0, len(formats))
	for _, format := range formats {
		format.Type = strings.ToUpper(strings.TrimSpace(format.Type))
		if !contains(docResponseFormatTypes, format.Type) {
			return nil, apperrors.New(consts.StatusBadRequest, "响应格式只支持JSON、TEXT、XML、HTML、FILE或SSE")
		}
		if seen[format.Type] {
			return nil, apperrors.New(consts.StatusBadRequest, "响应格式类型重复："+format.Type)
		}
		seen[format.Type] = true
		if utf8.RuneCountInString(strings.TrimSpace(format.Example)) > docResponseFormatExampleMax {
			return nil, apperrors.New(consts.StatusBadRequest, "返回预览示例过长")
		}
		normalized = append(normalized, DocResponseFormat{Type: format.Type, Example: format.Example})
	}
	return normalized, nil
}

// parseDocResponseFormats decodes the stored JSON column; invalid or empty
// payloads fall back to the legacy single doc_response_type/example pair.
func parseDocResponseFormats(raw string) []DocResponseFormat {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var formats []DocResponseFormat
	if err := json.Unmarshal([]byte(raw), &formats); err != nil {
		return nil
	}
	cleaned := formats[:0:0]
	for _, format := range formats {
		format.Type = strings.ToUpper(strings.TrimSpace(format.Type))
		if !contains(docResponseFormatTypes, format.Type) {
			continue
		}
		cleaned = append(cleaned, format)
	}
	return cleaned
}

// publicDocResponseFormats is the authoritative docs response list: the
// multi-format column when configured, otherwise one synthetic entry built
// from the legacy doc_response_type/doc_response_example pair so every
// interface keeps rendering its existing example.
func publicDocResponseFormats(item InterfaceItem) []DocResponseFormat {
	if formats := parseDocResponseFormats(item.DocResponseFormats); len(formats) > 0 {
		return formats
	}
	if strings.TrimSpace(item.DocResponseExample) == "" && strings.TrimSpace(item.DocResponseType) == "" {
		return nil
	}
	return []DocResponseFormat{{Type: defaultText(item.DocResponseType, "JSON"), Example: item.DocResponseExample}}
}

func validateBillingSwitches(req SaveBillingRulesRequest) error {
	for _, value := range []*int{req.BillingEnabled, req.BillingLogEnabled} {
		if value != nil && *value != 0 && *value != 1 {
			return apperrors.New(consts.StatusBadRequest, "计费开关必须为0或1")
		}
	}
	return nil
}

func normalizeBillingRules(rules []BillingRule) ([]BillingRule, error) {
	if len(rules) > 20 {
		return nil, apperrors.New(consts.StatusBadRequest, "扣费条件最多配置20条")
	}
	for i := range rules {
		rules[i].FieldName = strings.TrimSpace(rules[i].FieldName)
		rules[i].Operator = strings.ToUpper(strings.TrimSpace(rules[i].Operator))
		rules[i].ExpectedValue = strings.TrimSpace(rules[i].ExpectedValue)
		if rules[i].FieldName == "" || rules[i].ExpectedValue == "" || !validOperators[rules[i].Operator] {
			return nil, apperrors.New(consts.StatusBadRequest, "扣费条件格式不正确")
		}
		if utf8.RuneCountInString(rules[i].FieldName) > 128 || utf8.RuneCountInString(rules[i].ExpectedValue) > 255 {
			return nil, apperrors.New(consts.StatusBadRequest, "扣费条件字段或值过长")
		}
		if rules[i].SortNo <= 0 {
			rules[i].SortNo = i + 1
		}
	}
	return rules, nil
}

func normalizeRequestValidationPolicy(req SaveRequestValidationPolicyRequest) (requestvalidation.Policy, error) {
	policy, err := requestvalidation.Normalize(requestvalidation.Policy{
		DefaultResult: req.DefaultResult,
		Enabled:       flagValue(req.Enabled, 0),
		Rules:         req.Rules,
	})
	if err != nil {
		return requestvalidation.Policy{}, apperrors.Wrap(consts.StatusBadRequest, "请求参数检测配置不合法", err)
	}
	return policy, nil
}

func normalizeRequestMethod(value string) (string, error) {
	method := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), "/", "_"))
	if contains([]string{"GET", "POST", "GET_POST"}, method) {
		return method, nil
	}
	return "", apperrors.New(consts.StatusBadRequest, "请求方式只能是GET、POST或GET/POST")
}

func normalizeInvokeMethod(value, allowed string) (string, error) {
	method := strings.ToUpper(strings.TrimSpace(value))
	if method == "" {
		if allowed == "POST" {
			method = "POST"
		} else {
			method = "GET"
		}
	}
	if !contains([]string{"GET", "POST"}, method) {
		return "", apperrors.New(consts.StatusBadRequest, "调用方式只能是GET或POST")
	}
	if allowed != "GET_POST" && allowed != method {
		return "", apperrors.New(consts.StatusBadRequest, "该接口不支持当前请求方式")
	}
	return method, nil
}

func normalizePollingMode(value string, enabled int) (string, error) {
	mode := strings.ToUpper(strings.TrimSpace(value))
	switch mode {
	case "":
		if enabled == 0 {
			return "SINGLE", nil
		}
		return "ROUND_ROBIN", nil
	case "FASTEST":
		return "FASTEST", nil
	case "PRIMARY":
		return "PRIMARY", nil
	case "ROUND_ROBIN", "RANDOM":
		return "ROUND_ROBIN", nil
	case "SINGLE":
		return "SINGLE", nil
	default:
		return "", apperrors.New(consts.StatusBadRequest, "轮询模式只能是 SINGLE、ROUND_ROBIN、PRIMARY 或 FASTEST")
	}
}

func validateHTTPURL(value string, maxLen int, label string) error {
	if strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > maxLen {
		return apperrors.New(consts.StatusBadRequest, label+"不能为空且不能超过"+strconv.Itoa(maxLen)+"个字符")
	}
	if err := httpclient.ValidateURL(value); err != nil {
		return apperrors.New(consts.StatusBadRequest, label+"必须是有效的 HTTP 或 HTTPS 地址")
	}
	return nil
}

func validateAvatarURL(value, systemPrefix string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if utf8.RuneCountInString(value) > 1024 {
		return apperrors.New(consts.StatusBadRequest, "头像地址不能超过1024个字符")
	}
	localPrefix := strings.TrimRight(systemPrefix, "/") + "/upload/interface-avatar/"
	if strings.HasPrefix(value, localPrefix) {
		if !upload.ValidGeneratedImageName(strings.TrimPrefix(value, localPrefix)) {
			return apperrors.New(consts.StatusBadRequest, "头像地址不是有效的系统上传图片")
		}
		return nil
	}
	return validateHTTPURL(value, 1024, "头像地址")
}

func normalizeMoney(value any) (string, error) {
	text := strings.TrimSpace(fmt.Sprintf("%v", value))
	if value == nil || text == "" || text == "<nil>" {
		text = "0"
	}
	number, err := decimalvalue.ParseNonNegative(text)
	if errors.Is(err, decimalvalue.ErrExponentRange) || errors.Is(err, decimalvalue.ErrTextTooLong) {
		return "", apperrors.New(consts.StatusBadRequest, "接口价格超过系统允许范围")
	}
	if err != nil {
		return "", apperrors.New(consts.StatusBadRequest, "接口价格必须是非负数字")
	}
	if number.Cmp(maxInterfaceMoney) > 0 {
		return "", apperrors.New(consts.StatusBadRequest, "接口价格超过系统允许范围")
	}
	return number.FloatString(4), nil
}

func normalizePoints(value any) (int64, error) {
	if value == nil || strings.TrimSpace(fmt.Sprintf("%v", value)) == "" {
		return 0, nil
	}
	points, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprintf("%v", value)), 10, 64)
	if err != nil || points < 0 || points > maxInterfacePointPrice {
		return 0, apperrors.New(consts.StatusBadRequest, "接口点数必须是非负整数")
	}
	return points, nil
}

func flagValue(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	if *value == 0 || *value == 1 {
		return *value
	}
	return -1
}

func validJSONArray(value string) bool {
	var raw any
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return false
	}
	_, ok := raw.([]any)
	return ok
}

func applyTemplate(template string, params map[string]any) (string, error) {
	missing := ""
	result := placeholderPattern.ReplaceAllStringFunc(template, func(match string) string {
		key := strings.TrimSuffix(strings.TrimPrefix(match, "{"), "}")
		value, ok := params[key]
		if ok && strings.TrimSpace(fmt.Sprintf("%v", value)) != "" {
			return url.QueryEscape(fmt.Sprintf("%v", value))
		}
		missing = key
		return match
	})
	if missing != "" {
		return "", apperrors.New(consts.StatusBadRequest, fmt.Sprintf("缺少接口参数: %s", missing))
	}
	if placeholderPattern.MatchString(result) {
		return "", apperrors.New(consts.StatusBadRequest, "接口地址模板参数未填写完整")
	}
	return result, nil
}

func appendQuery(target string, params map[string]any) string {
	if len(params) == 0 {
		return target
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return target
	}
	query := parsed.Query()
	for key, value := range params {
		if strings.Contains(target, "{"+key+"}") {
			continue
		}
		if value != nil {
			query.Set(key, fmt.Sprintf("%v", value))
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func responsePasses(cfg UpstreamConfig, status int, body string) bool {
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

// optionalPollingFieldAbsent 与 gateway 包保持同一判定契约：自动兼容模式下，
// 空响应体与损坏的 JSON 一律判为不通过（继续轮询下一个节点），顶层显式错误信号
// 同样判为不通过；纯文本、合法 JSON 数组与不含错误信号的对象放行。
func optionalPollingFieldAbsent(body string) bool {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return false
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return true
	}
	if !json.Valid([]byte(trimmed)) {
		return false
	}
	if trimmed[0] == '[' {
		return true
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(trimmed), &object); err != nil {
		return true
	}
	return !hasExplicitErrorSignal(object)
}

// hasExplicitErrorSignal 仅识别上游明确表达失败的顶层字段，不做数值猜测。
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

// errorSignalPresent 判断 error 类字段是否携带真实错误内容。
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

// effectivePollingCheck resolves the response-check policy for a candidate
// node. The primary interface fields are the source of truth; a node-level
// check is an explicit override when enabled. Older rows may have polling
// enabled while the response-check toggle is zero, so those rows still inspect
// an explicitly present business code and retry non-success values. Bodies
// without a code field remain pass-through payloads for compatibility.
func effectivePollingCheck(item InterfaceItem, cfg UpstreamConfig) UpstreamConfig {
	if cfg.PollingCheckEnabled == 1 {
		return cfg
	}
	if item.PollingCheckEnabled != 1 && strings.TrimSpace(item.PollingStopCode) == "" {
		if pollingRuntimeEnabled(item) {
			cfg.PollingCheckEnabled = 1
			cfg.pollingCheckAuto = true
			cfg.PollingCheckField = defaultText(strings.TrimSpace(item.PollingCheckField), "code")
			cfg.PollingCheckExpected = defaultText(strings.TrimSpace(item.PollingCheckExpected), "200")
		}
		return cfg
	}
	cfg.PollingCheckEnabled = 1
	cfg.PollingCheckField = defaultText(strings.TrimSpace(item.PollingCheckField), "code")
	cfg.PollingCheckExpected = defaultText(strings.TrimSpace(item.PollingCheckExpected), "200")
	return cfg
}

// shouldStopPolling identifies a terminal business response.  Without a
// custom stop code, an HTTP 404 is the documented legacy terminal response.
// With a custom stop code, only the configured response-body field/value pair
// is terminal; all other statuses and business codes continue through the
// fallback candidates.
func shouldStopPolling(item InterfaceItem, result *InvokeResult) bool {
	if result == nil {
		return false
	}
	stopCode := strings.TrimSpace(item.PollingStopCode)
	if stopCode == "" {
		if result.StatusCode == http.StatusNotFound {
			return true
		}
		if !pollingRuntimeEnabled(item) {
			return false
		}
		actual, ok := valueAtPath(result.Body, defaultText(item.PollingCheckField, "code"))
		return ok && fmt.Sprintf("%v", actual) == "404"
	}
	actual, ok := valueAtPath(result.Body, defaultText(item.PollingCheckField, "code"))
	return ok && fmt.Sprintf("%v", actual) == stopCode
}

// prepareInvokeResponse validates the raw upstream body first so existing
// per-node response checks keep their original semantics. When cleaning is
// enabled, a normalized body may also satisfy the check (for example an
// upstream code of 0 is normalized to the contract's 200). A malformed JSON
// body is treated as a node failure and lets polling continue.
func prepareInvokeResponse(cleaningEnabled int, cfg UpstreamConfig, result *InvokeResult) (bool, error) {
	if result == nil || !httpstatus.IsSuccess(result.StatusCode) {
		return false, nil
	}
	result.ResponseNormalized = false
	rawPassed := responsePasses(cfg, result.StatusCode, result.Body)
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
	normalizedPassed := responsePasses(cfg, result.StatusCode, normalizedBody)
	if !pollingResponsePassesAfterCleaning(cfg, rawBody, normalizedBody, normalized.IsJSON, rawPassed, normalizedPassed) {
		return false, nil
	}
	result.Body = normalizedBody
	if normalized.IsJSON {
		// response_size records the original upstream bytes; the saved body may
		// be shorter because normalization intentionally changes its shape.
		result.ResponseSize = originalSize
		result.ResponseNormalized = true
	}
	return true, nil
}

// pollingResponsePassesAfterCleaning keeps the canonical envelope authoritative
// when cleaning reveals a business field that was missing in the raw payload,
// while preserving explicitly configured legacy successes such as code=0.
// This prevents an automatic raw-body pass from masking code=500, without
// breaking existing interfaces that expect the pre-normalization success code.
func pollingResponsePassesAfterCleaning(cfg UpstreamConfig, rawBody, normalizedBody string, normalizedIsJSON bool, rawPassed, normalizedPassed bool) bool {
	if cfg.PollingCheckEnabled != 1 || !normalizedIsJSON {
		return rawPassed || normalizedPassed
	}
	// Do not let a successful outer wrapper mask an explicit failure exposed by
	// normalization (for example code=200 with a nested status=500).  The
	// canonical response is what the caller receives and must drive polling.
	if !normalizedPassed && normalizedResponseExposesFailure(cfg, normalizedBody) {
		return false
	}
	field := defaultText(cfg.PollingCheckField, "code")
	_, rawHasField := valueAtPath(rawBody, field)
	if !rawHasField {
		return normalizedPassed
	}
	if rawPassed {
		return true
	}
	actual, ok := valueAtPath(rawBody, field)
	if !ok || defaultText(cfg.PollingCheckExpected, "200") != "200" {
		return false
	}
	return normalizedPassed && automaticBusinessCodePasses(actual, "200")
}

// normalizedResponseExposesFailure detects concrete numeric/error tokens while
// leaving arbitrary strings alone.  The latter may be intentional custom
// polling values (such as "pending") and should continue to honor the raw
// configured expectation when normalization cannot interpret them.
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
		if number != 0 && number != 1 && (number < 200 || number >= 300) {
			return true
		}
	}
	return false
}

func valueAtPath(body, path string) (any, bool) {
	return jsonpathvalue.Lookup(body, path)
}

func parameterNames(template string) []string {
	matches := placeholderPattern.FindAllStringSubmatch(template, -1)
	seen := map[string]bool{}
	var names []string
	for _, match := range matches {
		if len(match) < 2 || match[1] == "key" || seen[match[1]] {
			continue
		}
		seen[match[1]] = true
		names = append(names, match[1])
	}
	sort.Strings(names)
	return names
}

func preferredMethod(configured, requestMethod string) string {
	if strings.TrimSpace(configured) != "" {
		return configured
	}
	if requestMethod == "POST" {
		return "POST"
	}
	return "GET"
}

func parseDocArray(text string) []map[string]any {
	var result []map[string]any
	_ = json.Unmarshal([]byte(text), &result)
	if result == nil {
		return []map[string]any{}
	}
	return result
}

func statusCodes(text string) []map[string]any {
	items := parseDocArray(text)
	if len(items) > 0 {
		return items
	}
	return []map[string]any{
		{"code": 200, "description": "调用成功"},
		{"code": 401, "description": "接口密钥无效"},
		{"code": 403, "description": "余额、点数或套餐不足"},
		{"code": 429, "description": "调用频率超过限制"},
		{"code": 500, "description": "服务异常"},
	}
}

func compactJSON(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func invokeRequestParamsForLog(query map[string]any, body any) string {
	return masking.SensitiveJSONTextWithURLs(compactJSON(map[string]any{
		"body":  invokeBodyForLog(body),
		"query": invokeQueryForLog(query),
	}))
}

func invokeValidationInput(req InvokePayload) requestvalidation.Input {
	query := make(map[string][]string, len(req.QueryParams))
	for key, value := range req.QueryParams {
		query[key] = validationStrings(value)
	}
	body, _ := json.Marshal(req.Body)
	if string(body) == "null" {
		body = nil
	}
	return requestvalidation.Input{
		Body:        body,
		ContentType: "application/json",
		Query:       query,
	}
}

func validationStrings(value any) []string {
	switch typed := value.(type) {
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			result = append(result, fmt.Sprintf("%v", item))
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	default:
		return []string{fmt.Sprintf("%v", typed)}
	}
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

func invokeRequestSize(query map[string]any, body any) int64 {
	var size int64
	if len(query) > 0 {
		if data, err := json.Marshal(query); err == nil {
			size += int64(len(data))
		}
	}
	if body != nil {
		if data, err := json.Marshal(body); err == nil {
			size += int64(len(data))
		}
	}
	return size
}

func invokeQueryForLog(query map[string]any) map[string]any {
	if len(query) == 0 {
		return map[string]any{}
	}
	result := make(map[string]any, len(query))
	for key, value := range query {
		result[key] = normalizeLogValue(value)
	}
	return result
}

func invokeBodyForLog(body any) any {
	switch typed := body.(type) {
	case nil:
		return nil
	case string:
		return rawBodyForLog([]byte(typed))
	case []byte:
		return rawBodyForLog(typed)
	case json.RawMessage:
		return rawBodyForLog([]byte(typed))
	default:
		return normalizeLogValue(typed)
	}
}

func rawBodyForLog(body []byte) any {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return nil
	}
	var parsed any
	if jsonrequest.UnmarshalUnambiguous(body, &parsed, jsonrequest.DefaultMaxDepth) == nil {
		return normalizeLogValue(parsed)
	}
	raw := string(body)
	if strings.Contains(raw, "=") {
		if values, err := url.ParseQuery(raw); err == nil && len(values) > 0 {
			return normalizeFormValuesForLog(values)
		}
	}
	return "[NON_STRUCTURED_BODY_REDACTED]"
}

func normalizeFormValuesForLog(values url.Values) map[string]any {
	result := make(map[string]any, len(values))
	for key, items := range values {
		copied := append([]string(nil), items...)
		sort.Strings(copied)
		result[key] = copied
	}
	return result
}

func normalizeLogValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = normalizeLogValue(item)
		}
		return result
	case map[string]string:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = item
		}
		return result
	case []string:
		result := append([]string(nil), typed...)
		sort.Strings(result)
		return result
	case []any:
		if asStrings, ok := stringSlice(typed); ok {
			sort.Strings(asStrings)
			return asStrings
		}
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = normalizeLogValue(item)
		}
		return result
	default:
		return typed
	}
}

func stringSlice(values []any) ([]string, bool) {
	result := make([]string, len(values))
	for index, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		result[index] = text
	}
	return result, true
}

func requestDisplay(query map[string]any, body any) string {
	if len(query) > 0 {
		return masking.SensitiveJSONText(compactJSON(invokeQueryForLog(query)))
	}
	if body != nil {
		return masking.SensitiveJSONText(compactJSON(invokeBodyForLog(body)))
	}
	return ""
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

func mapInterfaceWriteError(err error) error {
	if err == nil {
		return nil
	}
	var mysqlErr *mysqlDriver.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return apperrors.New(consts.StatusConflict, "接口唯一编码已存在")
	}
	return mapProxySecretError(err)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
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

func sanitizePublicInterfaceItem(item InterfaceItem) InterfaceItem {
	item.ParamKeys = parameterNames(item.RequestURL)
	item.RequestURL = ""
	item.CurrentNode = 0
	item.CurrentNodeURL = ""
	item.PollingCheckEnabled = 0
	item.PollingCheckExpected = ""
	item.PollingCheckField = ""
	item.PollingEnabled = 0
	item.PollingMode = ""
	item.ProxyPoolConfig = proxypool.Config{}
	item.ResponseCleaningEnabled = 0
	item.ResponseFieldMappings = nil
	item.UpstreamConfigs = nil
	item.UpstreamCount = 0
	item.UpstreamURLs = nil
	item.DomainRouting = DomainRoutingConfig{}
	return item
}

func hasRole(roles []string, target string) bool {
	for _, role := range roles {
		if role == target {
			return true
		}
	}
	return false
}

func intPtr(value int) *int {
	return &value
}
