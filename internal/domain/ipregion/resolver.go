package ipregion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/pkg/httpclient"

	xdb "github.com/lionsoul2014/ip2region/binding/golang/ip2region"
	"github.com/redis/go-redis/v9"
)

var ErrProviderNotConfigured = errors.New("ip region provider not configured")

// SourceLoader 返回当前生效的 IP 数据源配置（后台 bp_ip_config 表）。
// 读取失败时返回 error，调用方回退内置默认源。
type SourceLoader func(ctx context.Context) (SourceConfig, error)

// sourceConfigCacheTTL 控制后台数据源配置的内存缓存时长，避免网关热路径频繁查库。
const sourceConfigCacheTTL = 30 * time.Second

type sourceCacheEntry struct {
	config   SourceConfig
	loadedAt time.Time
}

type Region struct {
	City      string  `json:"city,omitempty"`
	Country   string  `json:"country,omitempty"`
	ISP       string  `json:"isp,omitempty"`
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
	Provider  string  `json:"provider,omitempty"`
	Region    string  `json:"region,omitempty"`
	Province  string  `json:"province,omitempty"`
}

type Resolver struct {
	cfg          config.IPRegionConfig
	rdb          *redis.Client
	client       *http.Client
	fallback     SourceConfig
	sourceLoader SourceLoader
	sourceMu     sync.Mutex
	sourceCache  *sourceCacheEntry
	flightMu     sync.Mutex
	flights      map[string]*resolveFlight
}

type resolveFlight struct {
	done   chan struct{}
	region Region
	err    error
}

func NewResolver(cfg config.IPRegionConfig, rdb *redis.Client) *Resolver {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &Resolver{
		cfg:      cfg,
		rdb:      rdb,
		client:   httpclient.Outbound(timeout),
		fallback: CountryFallbackSourceConfig(),
		flights:  make(map[string]*resolveFlight),
	}
}

// SetSourceLoader 注入运行时数据源配置读取器（来自后台管理配置）。
func (r *Resolver) SetSourceLoader(loader SourceLoader) {
	r.sourceLoader = loader
}

func (r *Resolver) Resolve(ctx context.Context, ip string) (Region, error) {
	ip = normalizeIP(ip)
	if !r.cfg.Enabled || ip == "" || isPrivateIP(ip) {
		return Region{}, nil
	}
	ctx, cancel := r.resolutionContext(ctx)
	defer cancel()

	provider := strings.ToLower(strings.TrimSpace(r.cfg.Provider))
	if provider == "" {
		provider = "ip-api"
	}
	source, useSource := r.loadSource(ctx)
	providerName := provider
	if useSource && !source.IsDefault() {
		providerName = "custom:" + normalizeSourceName(source.Name)
	} else if provider == "ip-api" {
		providerName = "ip-api"
	}
	if cached, ok := r.cacheGet(ctx, providerName, ip); ok {
		return cached, nil
	}
	flightKey := providerName + "\x00" + ip
	flight, leader := r.joinResolveFlight(flightKey)
	if !leader {
		select {
		case <-flight.done:
			return flight.region, flight.err
		case <-ctx.Done():
			return Region{}, ctx.Err()
		}
	}
	workCtx, workCancel := r.resolutionContext(context.WithoutCancel(ctx))
	defer workCancel()
	region, err := r.resolveUncoalesced(workCtx, ip, provider, source, useSource, providerName)
	r.finishResolveFlight(flightKey, flight, region, err)
	return region, err
}

func (r *Resolver) joinResolveFlight(key string) (*resolveFlight, bool) {
	r.flightMu.Lock()
	defer r.flightMu.Unlock()
	if flight, ok := r.flights[key]; ok {
		return flight, false
	}
	flight := &resolveFlight{done: make(chan struct{})}
	if r.flights == nil {
		r.flights = make(map[string]*resolveFlight)
	}
	r.flights[key] = flight
	return flight, true
}

func (r *Resolver) finishResolveFlight(key string, flight *resolveFlight, region Region, err error) {
	r.flightMu.Lock()
	flight.region = region
	flight.err = err
	delete(r.flights, key)
	close(flight.done)
	r.flightMu.Unlock()
}

func (r *Resolver) resolveUncoalesced(ctx context.Context, ip, provider string, source SourceConfig, useSource bool, providerName string) (Region, error) {
	var (
		region Region
		err    error
	)
	primaryAttempts := 1
	if !isBuiltinIPAPI(providerName) {
		primaryAttempts++
	}
	if r.fallback.Enabled && strings.TrimSpace(r.fallback.APIURL) != "" {
		primaryAttempts++
	}
	primaryCtx, primaryCancel := attemptContext(ctx, primaryAttempts)
	if useSource && !source.IsDefault() {
		region, err = r.resolveConfiguredSource(primaryCtx, source, ip)
	} else {
		switch provider {
		case "ip-api":
			region, err = r.resolveIPAPI(primaryCtx, ip)
		case "bilibili":
			region, err = r.resolveBilibili(primaryCtx, ip)
		case "tencent-map":
			region, err = r.resolveTencentMap(primaryCtx, ip)
		case "amap":
			region, err = r.resolveAmap(primaryCtx, ip)
		case "ip2region":
			region, err = r.resolveIP2Region(primaryCtx, ip)
		default:
			if useSource {
				region, err = r.resolveSource(primaryCtx, source, ip)
			} else {
				err = fmt.Errorf("%w: %s", ErrProviderNotConfigured, provider)
			}
		}
	}
	primaryCancel()
	if err != nil {
		primaryErr := err
		// 配置的数据源失败（超时/限流/网络异常/解析失败）时，先回退内置 HTTPS
		// 精确源；主源本身就是内置源时不重复请求。
		if !isBuiltinIPAPI(providerName) {
			fallbackCtx, fallbackCancel := attemptContext(ctx, 2)
			fallback, fbErr := r.resolveIPAPI(fallbackCtx, ip)
			fallbackCancel()
			if fbErr == nil && hasRegionData(fallback) {
				fallback.Provider = providerName + "+fallback"
				fallback.Region = joinRegion(fallback.Country, fallback.Province, fallback.City)
				r.cacheSet(ctx, providerName, ip, fallback)
				return fallback, nil
			}
		}
		// 精确源不可用时再使用独立 HTTPS 国家级源；所有错误只向调用方表达为
		// “不可用”，具体上游地址与响应不会进入公开接口。
		if r.fallback.Enabled && strings.TrimSpace(r.fallback.APIURL) != "" {
			countryCtx, countryCancel := attemptContext(ctx, 1)
			fallback, fbErr := r.resolveSource(countryCtx, r.fallback, ip)
			countryCancel()
			if fbErr == nil && hasRegionData(fallback) {
				fallback.Provider = providerName + "+country-fallback"
				fallback.Region = joinRegion(fallback.Country, fallback.Province, fallback.City)
				r.cacheSet(ctx, providerName, ip, fallback)
				return fallback, nil
			}
		}
		return Region{Provider: providerName}, primaryErr
	}
	region.Provider = providerName
	region.Region = joinRegion(region.Country, region.Province, region.City)
	r.cacheSet(ctx, providerName, ip, region)
	return region, nil
}

// loadSource 读取后台配置的当前数据源；未配置 SourceLoader 或读取失败时回退内置默认源。
func (r *Resolver) loadSource(ctx context.Context) (SourceConfig, bool) {
	if r.sourceLoader == nil {
		return SourceConfig{}, false
	}
	r.sourceMu.Lock()
	defer r.sourceMu.Unlock()
	if r.sourceCache != nil && time.Since(r.sourceCache.loadedAt) < sourceConfigCacheTTL {
		return r.sourceCache.config, !r.sourceCache.config.IsDefault()
	}
	cfg, err := r.sourceLoader(ctx)
	if err != nil || cfg.IsDefault() || !cfg.Enabled {
		r.sourceCache = &sourceCacheEntry{config: DefaultSourceConfig(), loadedAt: time.Now()}
		return DefaultSourceConfig(), false
	}
	r.sourceCache = &sourceCacheEntry{config: cfg, loadedAt: time.Now()}
	return cfg, true
}

// resolveIPAPI 使用内置默认源 ipwho.is（可通过 IP_REGION_PROVIDER=ip-api 与
// IP_API_ENDPOINT 配置历史兼容地址）。
func (r *Resolver) resolveIPAPI(ctx context.Context, ip string) (Region, error) {
	source := DefaultSourceConfig()
	if endpoint := strings.TrimSpace(r.cfg.IPApiEndpoint); endpoint != "" {
		source.APIURL = endpoint
		if !isIPWhoEndpoint(endpoint) {
			source.ParseRule = LegacyIPAPIParseRule
		}
	}
	return r.resolveSource(ctx, source, ip)
}

func (r *Resolver) resolutionContext(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := r.cfg.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= timeout {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, timeout)
}

// attemptContext 将剩余总预算按剩余尝试次数均分，防止一个慢主源吃光全部
// 超时，导致兜底源永远没有执行机会。
func attemptContext(parent context.Context, attemptsLeft int) (context.Context, context.CancelFunc) {
	if attemptsLeft < 1 {
		attemptsLeft = 1
	}
	deadline, ok := parent.Deadline()
	if !ok {
		return context.WithCancel(parent)
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return context.WithCancel(parent)
	}
	budget := remaining / time.Duration(attemptsLeft)
	if budget < 100*time.Millisecond && remaining >= 100*time.Millisecond {
		budget = 100 * time.Millisecond
	}
	if budget > remaining {
		budget = remaining
	}
	return context.WithTimeout(parent, budget)
}

func isIPWhoEndpoint(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(parsed.Hostname(), "ipwho.is")
}

// resolveSource 通过通用 HTTP 模板源解析（后台自定义源与内置 ip-api 共用）。
func (r *Resolver) resolveSource(ctx context.Context, source SourceConfig, ip string) (Region, error) {
	provider, err := NewProviderFromSource(source, r.client, 0)
	if err != nil {
		return Region{}, err
	}
	return provider.Resolve(ctx, ip)
}

// resolveConfiguredSource keeps generic JSON sources configurable while routing
// Bilibili presets through the stricter provider-specific response validation.
func (r *Resolver) resolveConfiguredSource(ctx context.Context, source SourceConfig, ip string) (Region, error) {
	if isBilibiliSource(source) {
		return r.resolveBilibiliEndpoint(ctx, source.APIURL, ip)
	}
	return r.resolveSource(ctx, source, ip)
}

func (r *Resolver) IgnoreErrors() bool {
	return r.cfg.IgnoreProviderErrorInLog
}

// resolveBilibili 解析 Bilibili get_ip_addr 数据源。
// 当前接口返回 data.addr/country/province/city/isp/latitude/longitude；解析时
// 同时校验 data.addr 与请求 IP，避免上游忽略 ip 参数时把服务器出口位置误记为访客位置。
// 网络失败、限流或响应结构异常时仍会自动回退内置 HTTPS 源（见 Resolve）。
func (r *Resolver) resolveBilibili(ctx context.Context, ip string) (Region, error) {
	endpoint := strings.TrimSpace(r.cfg.BilibiliEndpoint)
	if endpoint == "" {
		return Region{}, ErrProviderNotConfigured
	}
	return r.resolveBilibiliEndpoint(ctx, endpoint, ip)
}

func (r *Resolver) resolveBilibiliEndpoint(ctx context.Context, endpoint, ip string) (Region, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return Region{}, ErrProviderNotConfigured
	}
	hasPlaceholder := strings.Contains(endpoint, "{ip}")
	if hasPlaceholder {
		endpoint = strings.ReplaceAll(endpoint, "{ip}", url.QueryEscape(ip))
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return Region{}, err
	}
	if !hasPlaceholder {
		query := parsed.Query()
		query.Set("ip", ip)
		parsed.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Region{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "BugPk-Api/IPRegion")
	resp, err := r.client.Do(req)
	if err != nil {
		return Region{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Region{}, fmt.Errorf("bilibili ip region http %d", resp.StatusCode)
	}
	var payload struct {
		Code int `json:"code"`
		Data struct {
			Addr      string `json:"addr"`
			City      string `json:"city"`
			Country   string `json:"country"`
			ISP       string `json:"isp"`
			Latitude  any    `json:"latitude"`
			Longitude any    `json:"longitude"`
			Province  string `json:"province"`
		} `json:"data"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
	}
	if err := httpclient.DecodeJSON(resp.Body, httpclient.MaxProviderResponseBytes, &payload); err != nil {
		return Region{}, err
	}
	if payload.Code != 0 {
		msg := payload.Message
		if msg == "" {
			msg = payload.Msg
		}
		return Region{}, fmt.Errorf("bilibili ip region failed: %s", msg)
	}
	if addr := normalizeIP(payload.Data.Addr); addr != "" && addr != ip {
		return Region{}, errors.New("bilibili ip region returned a mismatched address")
	}
	region := Region{
		City:      payload.Data.City,
		Country:   payload.Data.Country,
		ISP:       payload.Data.ISP,
		Latitude:  number(payload.Data.Latitude),
		Longitude: number(payload.Data.Longitude),
		Province:  payload.Data.Province,
	}
	if (region.Latitude != 0 || region.Longitude != 0) && !validCoordinates(region.Latitude, region.Longitude) {
		return Region{}, errors.New("bilibili ip region returned invalid coordinates")
	}
	if !hasRegionData(region) {
		return Region{}, errors.New("bilibili ip region returned empty data")
	}
	return region, nil
}

func isBilibiliSource(source SourceConfig) bool {
	name := strings.ToLower(strings.TrimSpace(source.Name))
	for _, prefix := range []string{"custom:", "custom_", "custom-"} {
		name = strings.TrimPrefix(name, prefix)
	}
	if name == "bilibili" {
		return true
	}
	endpoint := strings.ReplaceAll(strings.TrimSpace(source.APIURL), "{ip}", "8.8.8.8")
	parsed, err := url.Parse(endpoint)
	return err == nil &&
		strings.EqualFold(parsed.Hostname(), "api.live.bilibili.com") &&
		strings.Contains(parsed.EscapedPath(), "/ip_service/")
}

func (r *Resolver) resolveTencentMap(ctx context.Context, ip string) (Region, error) {
	if strings.TrimSpace(r.cfg.TencentMapKey) == "" {
		return Region{}, ErrProviderNotConfigured
	}
	endpoint := defaultText(r.cfg.TencentMapEndpoint, "https://apis.map.qq.com/ws/location/v1/ip")
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return Region{}, err
	}
	query := parsed.Query()
	query.Set("ip", ip)
	query.Set("key", r.cfg.TencentMapKey)
	parsed.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Region{}, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return Region{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Region{}, fmt.Errorf("tencent-map ip region http %d", resp.StatusCode)
	}
	var payload struct {
		Status  int    `json:"status"`
		Message string `json:"message"`
		Result  struct {
			AdInfo struct {
				Nation   string `json:"nation"`
				Province string `json:"province"`
				City     string `json:"city"`
			} `json:"ad_info"`
			Location struct {
				Lat float64 `json:"lat"`
				Lng float64 `json:"lng"`
			} `json:"location"`
		} `json:"result"`
	}
	if err := httpclient.DecodeJSON(resp.Body, httpclient.MaxProviderResponseBytes, &payload); err != nil {
		return Region{}, err
	}
	if payload.Status != 0 {
		return Region{}, fmt.Errorf("tencent-map ip region failed: %s", payload.Message)
	}
	region := Region{Country: payload.Result.AdInfo.Nation, Province: payload.Result.AdInfo.Province, City: payload.Result.AdInfo.City, Latitude: payload.Result.Location.Lat, Longitude: payload.Result.Location.Lng}
	if !hasRegionData(region) {
		return Region{}, errors.New("tencent-map ip region returned empty data")
	}
	return region, nil
}

func (r *Resolver) resolveAmap(ctx context.Context, ip string) (Region, error) {
	if strings.TrimSpace(r.cfg.AmapKey) == "" {
		return Region{}, ErrProviderNotConfigured
	}
	endpoint := defaultText(r.cfg.AmapEndpoint, "https://restapi.amap.com/v3/ip")
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return Region{}, err
	}
	query := parsed.Query()
	query.Set("ip", ip)
	query.Set("key", r.cfg.AmapKey)
	parsed.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Region{}, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return Region{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Region{}, fmt.Errorf("amap ip region http %d", resp.StatusCode)
	}
	var payload struct {
		Status   string `json:"status"`
		Info     string `json:"info"`
		Province string `json:"province"`
		City     any    `json:"city"`
	}
	if err := httpclient.DecodeJSON(resp.Body, httpclient.MaxProviderResponseBytes, &payload); err != nil {
		return Region{}, err
	}
	if payload.Status != "1" {
		return Region{}, fmt.Errorf("amap ip region failed: %s", payload.Info)
	}
	region := Region{Country: "中国", Province: payload.Province, City: stringValue(payload.City)}
	if strings.TrimSpace(region.Province) == "" && strings.TrimSpace(region.City) == "" {
		return Region{}, errors.New("amap ip region returned empty data")
	}
	return region, nil
}

func (r *Resolver) resolveIP2Region(_ context.Context, ip string) (Region, error) {
	path := r.cfg.IP2RegionV4Path
	if strings.Contains(ip, ":") {
		path = r.cfg.IP2RegionV6Path
	}
	if strings.TrimSpace(path) == "" {
		return Region{}, ErrProviderNotConfigured
	}
	if _, err := os.Stat(path); err != nil {
		return Region{}, fmt.Errorf("%w: %s", ErrProviderNotConfigured, path)
	}
	searcher, err := xdb.New(path)
	if err != nil {
		return Region{}, err
	}
	defer searcher.Close()
	info, err := searcher.MemorySearch(ip)
	if err != nil {
		return Region{}, err
	}
	region := Region{
		City:     info.City,
		Country:  info.Country,
		ISP:      info.ISP,
		Province: info.Province,
	}
	if !hasRegionData(region) {
		return Region{}, errors.New("ip2region returned empty data")
	}
	return region, nil
}

func (r *Resolver) cacheGet(ctx context.Context, provider, ip string) (Region, bool) {
	if r.rdb == nil || r.cfg.CacheTTL <= 0 {
		return Region{}, false
	}
	text, err := r.rdb.Get(ctx, cacheKey(provider, ip)).Result()
	if err != nil {
		return Region{}, false
	}
	var region Region
	if json.Unmarshal([]byte(text), &region) != nil {
		return Region{}, false
	}
	return region, true
}

func (r *Resolver) cacheSet(ctx context.Context, provider, ip string, region Region) {
	if r.rdb == nil || r.cfg.CacheTTL <= 0 {
		return
	}
	data, _ := json.Marshal(region)
	_ = r.rdb.Set(ctx, cacheKey(provider, ip), data, r.cfg.CacheTTL).Err()
}

func cacheKey(provider, ip string) string {
	return "ipregion:" + provider + ":" + ip
}

// NormalizeIP 归一化客户端 IP：去除首尾空白、丢弃 XFF 代理链、校验合法性。
// 导出供其他域的审计/日志写入路径复用，保证「同一个 IP 在网关与审计里得到
// 同一种处理」——两处各自实现会随时间漂移，最终表现为同一个 IP 在不同表里
// 归属地不一致。
func NormalizeIP(ip string) string { return normalizeIP(ip) }

// IsPrivateIP 判断是否为内网/回环/保留地址；解析失败按私有处理（不查上游）。
func IsPrivateIP(ip string) bool { return isPrivateIP(ip) }

// JoinRegion 把国家/省/市等片段按空格拼接，跳过空片段。
func JoinRegion(parts ...string) string { return joinRegion(parts...) }

// HasRegionData 判断解析结果是否包含任何有效地区信息。
func HasRegionData(region Region) bool { return hasRegionData(region) }

func normalizeIP(ip string) string {
	ip = strings.TrimSpace(ip)
	if strings.Contains(ip, ",") {
		ip = strings.TrimSpace(strings.Split(ip, ",")[0])
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ""
	}
	return parsed.String()
}

func isPrivateIP(ip string) bool {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return true
	}
	if parsed.IsLoopback() || parsed.IsPrivate() || parsed.IsUnspecified() || !parsed.IsGlobalUnicast() {
		return true
	}
	v4 := parsed.To4()
	return v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127
}

func joinRegion(parts ...string) string {
	var values []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			values = append(values, part)
		}
	}
	return strings.Join(values, " ")
}

func hasRegionData(region Region) bool {
	return strings.TrimSpace(region.Country) != "" ||
		strings.TrimSpace(region.Province) != "" ||
		strings.TrimSpace(region.City) != "" ||
		strings.TrimSpace(region.ISP) != "" ||
		region.Latitude != 0 || region.Longitude != 0
}

func validCoordinates(latitude, longitude float64) bool {
	return latitude >= -90 && latitude <= 90 &&
		longitude >= -180 && longitude <= 180 &&
		(latitude != 0 || longitude != 0)
}

func number(value any) float64 {
	switch typed := value.(type) {
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case string:
		parsed, _ := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed
	default:
		return 0
	}
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []any:
		return ""
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func defaultText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

// isBuiltinIPAPI 判断 providerName 是否为内置默认逻辑名称 ip-api（此时失败无需回退自身）。
func isBuiltinIPAPI(providerName string) bool {
	return strings.EqualFold(strings.TrimSpace(providerName), "ip-api")
}
