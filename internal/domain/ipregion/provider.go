package ipregion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/pkg/httpclient"
	"bugpk-api/internal/pkg/jsonpathvalue"
)

// SourceConfig 描述一个可配置的 IP 归属地数据源（后台可编辑）。
//
// Name 是数据源名称：
//   - "default"（或空）：使用代码内置默认源 ipwho.is；
//   - 其它名称（如 custom_bilibili / custom_xxx）：使用 APIURL + ParseRule 自定义源。
type SourceConfig struct {
	Name      string
	APIURL    string
	ParseRule string
	Enabled   bool
}

// DefaultSourceConfig 返回内置默认数据源 ipwho.is。
// 免费、HTTPS、无需 token；可直接返回国家、省、市、经纬度和运营商信息。
func DefaultSourceConfig() SourceConfig {
	return SourceConfig{
		Name:      "default",
		APIURL:    "https://ipwho.is/{ip}?lang=zh-CN",
		ParseRule: "country=$.country, province=$.region, city=$.city, isp=$.connection.isp, latitude=$.latitude, longitude=$.longitude",
		Enabled:   true,
	}
}

// LegacyIPAPIParseRule 保留 IP_API_ENDPOINT 历史兼容：旧配置通常指向 ip-api.com，
// 其省份与运营商字段结构和当前内置源不同。
const LegacyIPAPIParseRule = "country=$.country, province=$.regionName, city=$.city, isp=$.isp, latitude=$.lat, longitude=$.lon"

// CountryFallbackSourceConfig 是内置的 HTTPS 二级兜底源。该源通常只能提供国家和
// 运营商信息，但在主源超时、限流或结构异常时仍可避免完全丢失归属地。
func CountryFallbackSourceConfig() SourceConfig {
	return SourceConfig{
		Name:      "country-fallback",
		APIURL:    "https://api.iplocation.net/?ip={ip}",
		ParseRule: "country=$.country_name, isp=$.isp",
		Enabled:   true,
	}
}

// IsDefault 判断是否为内置默认源（名称 default 或空）。
func (c SourceConfig) IsDefault() bool {
	name := strings.ToLower(strings.TrimSpace(c.Name))
	return name == "" || name == "default"
}

// Provider 抽象 IP 归属地数据源：调用方只依赖该接口，未来可自由扩展新源。
type Provider interface {
	// SourceName 返回数据源名称（用于缓存 key 与日志标识）。
	SourceName() string
	// Resolve 解析 IP 归属地；无效 IP / 解析失败返回错误（由调用方决定兜底）。
	Resolve(ctx context.Context, ip string) (Region, error)
}

// NewProviderFromSource 根据配置构造可执行的数据源 Provider。
// apiURL 为空时使用内置默认 ip-api 地址；parseRule 为空时使用默认字段映射。
// client 为空时使用默认出站客户端；timeout 为空时使用 5s。
func NewProviderFromSource(cfg SourceConfig, client *http.Client, timeout time.Duration) (Provider, error) {
	apiURL := strings.TrimSpace(cfg.APIURL)
	if apiURL == "" {
		apiURL = DefaultSourceConfig().APIURL
	}
	if !strings.Contains(apiURL, "{ip}") {
		return nil, errors.New("ip source api_url must contain {ip} placeholder")
	}
	validationURL := strings.ReplaceAll(apiURL, "{ip}", "8.8.8.8")
	if err := httpclient.ValidateURL(validationURL); err != nil {
		return nil, fmt.Errorf("ip source api_url is invalid: %w", err)
	}
	parseRule := strings.TrimSpace(cfg.ParseRule)
	if parseRule == "" {
		parseRule = DefaultSourceConfig().ParseRule
	}
	return &templateProvider{
		name:      normalizeSourceName(cfg.Name),
		apiURL:    apiURL,
		parseRule: parseRule,
		client:    clientOrDefault(client, timeout),
	}, nil
}

// templateProvider 是通用 HTTP 模板源：URL 含 {ip} 占位符，ParseRule 做字段映射。
type templateProvider struct {
	name      string
	apiURL    string
	parseRule string
	client    *http.Client
}

func (p *templateProvider) SourceName() string {
	return p.name
}

func (p *templateProvider) Resolve(ctx context.Context, ip string) (Region, error) {
	endpoint := strings.ReplaceAll(p.apiURL, "{ip}", url.QueryEscape(ip))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Region{}, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return Region{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Region{}, fmt.Errorf("ip source http %d", resp.StatusCode)
	}
	body, err := httpclient.ReadBody(resp.Body, httpclient.MaxProviderResponseBytes)
	if err != nil {
		return Region{}, err
	}
	region, err := applyParseRule(p.parseRule, string(body))
	if err != nil {
		return Region{}, err
	}
	if !hasRegionData(region) {
		return Region{}, errors.New("ip source returned empty data")
	}
	return region, nil
}

func clientOrDefault(client *http.Client, timeout time.Duration) *http.Client {
	if client != nil {
		return client
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return httpclient.Outbound(timeout)
}

// applyParseRule 按照 "country=$.country, province=$.regionName, city=$.city"
// 的字段映射规则从 JSON 响应中提取归属地字段。
func applyParseRule(rule, body string) (Region, error) {
	region := Region{}
	if !json.Valid([]byte(body)) {
		return region, errors.New("ip source response is not valid JSON")
	}
	for _, pair := range strings.Split(rule, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			return region, fmt.Errorf("invalid parse rule item: %q", pair)
		}
		field := strings.ToLower(strings.TrimSpace(parts[0]))
		path := strings.TrimSpace(parts[1])
		value, ok := lookupJSONPath(body, path)
		if !ok {
			continue
		}
		switch field {
		case "country":
			region.Country = scalarString(value)
		case "province":
			region.Province = scalarString(value)
		case "city":
			region.City = scalarString(value)
		case "isp":
			region.ISP = scalarString(value)
		case "latitude":
			region.Latitude = number(value)
		case "longitude":
			region.Longitude = number(value)
		}
	}
	return region, nil
}

// lookupJSONPath 支持 "$.country" / "data.city" / "result[0].name" 形式的路径。
// 复用 jsonpathvalue.Lookup（点分路径），去掉开头的 $ 前缀。
func lookupJSONPath(body, path string) (any, bool) {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "$")
	path = strings.TrimPrefix(path, ".")
	return jsonpathvalue.Lookup(body, path)
}

func scalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	case []any:
		// 数组/对象不是标量字段（如 amap 空 city 返回 []），视为缺失，不写入地区。
		return ""
	case map[string]any:
		return ""
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", typed))
	}
}

func normalizeSourceName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, " ", "-")
	if len(name) > 48 {
		name = name[:48]
	}
	if name == "" {
		return "custom"
	}
	return name
}
