package ipconfig

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bugpk-api/internal/domain/ipregion"
	"bugpk-api/internal/domain/rbac"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/httpclient"
	"bugpk-api/internal/pkg/jsonpathvalue"
	"bugpk-api/internal/pkg/jsonrequest"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	permissionView = "system:ip-config"
	permissionSave = "system:ip-config:update"

	defaultParseRule     = "country=$.country, province=$.region, city=$.city, isp=$.connection.isp, latitude=$.latitude, longitude=$.longitude"
	maxDiscoveredFields  = 200
	maxFieldPathBytes    = 128
	maxFieldPreviewRunes = 120
	probeTimeout         = 5 * time.Second
)

var supportedFields = []string{"country", "province", "city", "isp", "latitude", "longitude"}

// Service 提供 IP 数据源配置的管理（后台）与运行时读取（网关/IP 解析器）。
type Service struct {
	repo        *Repository
	authz       *rbac.Service
	probeClient *http.Client
}

func NewService(repo *Repository, authz *rbac.Service) *Service {
	return &Service{
		repo:        repo,
		authz:       authz,
		probeClient: httpclient.Outbound(probeTimeout),
	}
}

func (s *Service) Authenticate(ctx context.Context, token string) (rbac.Principal, error) {
	return s.authz.Authenticate(ctx, token)
}

// DefaultConfig 返回内置默认配置（default 源 = ipwho.is）。
func DefaultConfig() Config {
	return Config{
		DataSourceName: "default",
		APIURL:         "",
		ParseRule:      defaultParseRule,
		Enabled:        1,
		Remark:         "内置默认源：ipwho.is（HTTPS，含经纬度）；失败时自动回退国家级 HTTPS 数据源",
	}
}

// Config 返回当前数据源配置（后台管理页读取）。
func (s *Service) Config(ctx context.Context, principal rbac.Principal) (Config, error) {
	if err := s.authz.Require(ctx, principal, permissionView); err != nil {
		return Config{}, err
	}
	cfg, err := s.repo.Get(ctx)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// SaveConfig 保存数据源配置（后台管理页保存）。
func (s *Service) SaveConfig(ctx context.Context, principal rbac.Principal, req Config) error {
	if err := s.authz.Require(ctx, principal, permissionSave); err != nil {
		return err
	}
	cfg, err := normalizeConfig(req)
	if err != nil {
		return err
	}
	return s.repo.Save(ctx, cfg)
}

// TestSource performs one non-persistent request through the same hardened
// outbound transport used by runtime providers. It intentionally reuses the
// update permission because probing an arbitrary configured endpoint is a
// privileged configuration action, not a public diagnostics endpoint.
func (s *Service) TestSource(ctx context.Context, principal rbac.Principal, req TestSourceRequest) (TestSourceResult, error) {
	if err := s.authz.Require(ctx, principal, permissionSave); err != nil {
		return TestSourceResult{}, err
	}
	return probeSource(ctx, req, s.probeClient)
}

// ActiveSource 供运行时 IP 解析器读取当前生效的数据源（内部调用，无鉴权）。
// 未配置 / 禁用 / 名称为 default 时回退内置默认源 ipwho.is。
func (s *Service) ActiveSource(ctx context.Context) (ipregion.SourceConfig, error) {
	cfg, err := s.repo.Get(ctx)
	if err != nil {
		return ipregion.DefaultSourceConfig(), err
	}
	if cfg.Enabled != 1 {
		return ipregion.DefaultSourceConfig(), nil
	}
	name := strings.ToLower(strings.TrimSpace(cfg.DataSourceName))
	if name == "" || name == "default" || strings.TrimSpace(cfg.APIURL) == "" {
		return ipregion.DefaultSourceConfig(), nil
	}
	return ipregion.SourceConfig{
		Name:      cfg.DataSourceName,
		APIURL:    cfg.APIURL,
		ParseRule: cfg.ParseRule,
		Enabled:   true,
	}, nil
}

func normalizeConfig(req Config) (Config, error) {
	name := strings.ToLower(strings.TrimSpace(req.DataSourceName))
	if name == "" {
		return Config{}, apperrors.New(consts.StatusBadRequest, "数据源名称不能为空")
	}
	if len(name) > 64 {
		return Config{}, apperrors.New(consts.StatusBadRequest, "数据源名称不能超过64个字符")
	}
	rule := parseRuleFromText(req.ParseRule)
	apiURL := strings.TrimSpace(req.APIURL)
	enabled := 0
	if req.Enabled == 1 {
		enabled = 1
	}
	if name != "default" {
		if apiURL == "" {
			return Config{}, apperrors.New(consts.StatusBadRequest, "自定义数据源必须填写 apiUrl（含 {ip} 占位符）")
		}
		if !strings.Contains(apiURL, "{ip}") {
			return Config{}, apperrors.New(consts.StatusBadRequest, "apiUrl 必须包含 {ip} 占位符")
		}
		if err := validateSourceURL(apiURL); err != nil {
			return Config{}, err
		}
		if len(rule) == 0 {
			return Config{}, apperrors.New(consts.StatusBadRequest, "自定义数据源至少需要配置一个有效字段映射")
		}
		mappings := ruleMap(rule)
		if len(mappings) == 0 {
			return Config{}, apperrors.New(consts.StatusBadRequest, "自定义数据源至少需要配置一个有效字段映射")
		}
		if missingPair := missingCoordinatePair(mappings); len(missingPair) > 0 {
			return Config{}, apperrors.New(consts.StatusBadRequest, "经纬度字段必须同时配置："+strings.Join(missingPair, ", "))
		}
	} else if len(rule) == 0 {
		rule = parseRuleFromText(defaultParseRule)
	}
	if len(req.Remark) > 255 {
		return Config{}, apperrors.New(consts.StatusBadRequest, "备注不能超过255个字符")
	}
	return Config{
		DataSourceName: name,
		APIURL:         apiURL,
		ParseRule:      strings.Join(rule, ", "),
		Enabled:        enabled,
		Remark:         strings.TrimSpace(req.Remark),
	}, nil
}

// parseRuleFromText 解析并校验字段映射规则，仅保留合法字段（country/province/city/isp/latitude/longitude）。
func parseRuleFromText(text string) []string {
	allowed := map[string]bool{
		"country": true, "province": true, "city": true, "isp": true,
		"latitude": true, "longitude": true,
	}
	seen := map[string]bool{}
	var items []string
	for _, pair := range strings.Split(text, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 {
			continue
		}
		field := strings.ToLower(strings.TrimSpace(parts[0]))
		path := strings.TrimSpace(parts[1])
		if !allowed[field] || !validMappingPath(path) || seen[field] {
			continue
		}
		seen[field] = true
		items = append(items, field+"="+path)
	}
	return items
}

func ruleMap(items []string) map[string]string {
	result := make(map[string]string, len(items))
	for _, item := range items {
		parts := strings.SplitN(item, "=", 2)
		if len(parts) == 2 {
			result[parts[0]] = parts[1]
		}
	}
	return result
}

func normalizeFieldMappings(input map[string]string) (map[string]string, error) {
	result := make(map[string]string, len(input))
	allowed := make(map[string]bool, len(supportedFields))
	for _, field := range supportedFields {
		allowed[field] = true
	}
	for rawField, rawPath := range input {
		field := strings.ToLower(strings.TrimSpace(rawField))
		path := strings.TrimSpace(rawPath)
		if !allowed[field] {
			return nil, apperrors.New(consts.StatusBadRequest, "存在不支持的字段映射")
		}
		if path == "" {
			continue
		}
		if !validMappingPath(path) {
			return nil, apperrors.New(consts.StatusBadRequest, field+" 的响应字段路径不合法")
		}
		result[field] = path
	}
	return result, nil
}

func validMappingPath(path string) bool {
	path = strings.TrimSpace(path)
	if len(path) > maxFieldPathBytes || !strings.HasPrefix(path, "$.") {
		return false
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "$."), ".") {
		if strings.TrimSpace(segment) == "" {
			return false
		}
	}
	return true
}

func missingCoordinatePair(mappings map[string]string) []string {
	hasLatitude := strings.TrimSpace(mappings["latitude"]) != ""
	hasLongitude := strings.TrimSpace(mappings["longitude"]) != ""
	if hasLatitude == hasLongitude {
		return nil
	}
	if hasLatitude {
		return []string{"longitude"}
	}
	return []string{"latitude"}
}

func probeSource(ctx context.Context, input TestSourceRequest, client *http.Client) (TestSourceResult, error) {
	apiURL := strings.TrimSpace(input.APIURL)
	if len(apiURL) > 1024 || !strings.Contains(apiURL, "{ip}") {
		return TestSourceResult{}, apperrors.New(consts.StatusBadRequest, "测试接口地址必须包含 {ip} 占位符")
	}
	if err := validateSourceURL(apiURL); err != nil {
		return TestSourceResult{}, err
	}
	testIP := normalizePublicTestIP(input.TestIP)
	if testIP == "" {
		return TestSourceResult{}, apperrors.New(consts.StatusBadRequest, "测试 IP 必须是公网 IPv4 或 IPv6 地址")
	}
	mappings, err := normalizeFieldMappings(input.FieldMappings)
	if err != nil {
		return TestSourceResult{}, err
	}
	endpoint := strings.ReplaceAll(apiURL, "{ip}", url.QueryEscape(testIP))
	if err := httpclient.ValidateURL(endpoint); err != nil {
		return TestSourceResult{}, apperrors.New(consts.StatusBadRequest, "测试接口地址不合法")
	}
	if client == nil {
		client = httpclient.Outbound(probeTimeout)
	}
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(probeCtx, http.MethodGet, endpoint, nil)
	if err != nil {
		return TestSourceResult{}, apperrors.New(consts.StatusBadRequest, "无法创建数据源测试请求")
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("User-Agent", "BugPk-Api-IP-Source-Probe/1.0")
	response, err := client.Do(httpRequest)
	if err != nil {
		if errors.Is(err, httpclient.ErrBlockedAddress) || errors.Is(err, httpclient.ErrCrossOriginRedirect) {
			return TestSourceResult{}, apperrors.New(consts.StatusBadRequest, "测试接口地址指向受保护的网络或发生了跨域重定向")
		}
		return TestSourceResult{}, apperrors.Wrap(consts.StatusBadGateway, "IP 数据源测试请求失败", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return TestSourceResult{}, apperrors.New(consts.StatusUnprocessableEntity, fmt.Sprintf("数据源返回 HTTP %d", response.StatusCode))
	}
	body, err := httpclient.ReadBody(response.Body, httpclient.MaxProviderResponseBytes)
	if err != nil {
		if errors.Is(err, httpclient.ErrResponseTooLarge) {
			return TestSourceResult{}, apperrors.New(consts.StatusUnprocessableEntity, "数据源响应体超过 256 KiB 限制")
		}
		return TestSourceResult{}, apperrors.Wrap(consts.StatusBadGateway, "读取 IP 数据源响应失败", err)
	}
	decoded, err := jsonrequest.DecodeUnambiguous(body, jsonrequest.DefaultMaxDepth)
	if err != nil {
		return TestSourceResult{}, apperrors.New(consts.StatusUnprocessableEntity, "数据源响应不是结构明确的 JSON")
	}
	fields := discoverScalarFields(decoded, maxDiscoveredFields)
	if len(fields) == 0 {
		return TestSourceResult{}, apperrors.New(consts.StatusUnprocessableEntity, "数据源 JSON 中没有可映射的标量字段")
	}
	result := TestSourceResult{
		Fields:            fields,
		MissingPairFields: missingCoordinatePair(mappings),
	}
	result.Preview, result.InvalidFields = previewMappings(string(body), mappings)
	return result, nil
}

func normalizePublicTestIP(raw string) string {
	ip := net.ParseIP(strings.TrimSpace(raw))
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return ""
	}
	if ipv4 := ip.To4(); ipv4 != nil {
		if ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127 {
			return ""
		}
		return ipv4.String()
	}
	return ip.String()
}

func discoverScalarFields(value any, limit int) []SourceFieldOption {
	if limit <= 0 {
		return nil
	}
	result := make([]SourceFieldOption, 0, min(limit, 32))
	var walk func(any, string)
	walk = func(current any, path string) {
		if len(result) >= limit {
			return
		}
		switch typed := current.(type) {
		case map[string]any:
			keys := make([]string, 0, len(typed))
			for key := range typed {
				if key != "" && !strings.Contains(key, ".") {
					keys = append(keys, key)
				}
			}
			sort.Strings(keys)
			for _, key := range keys {
				walk(typed[key], path+"."+key)
			}
		case []any:
			for index, item := range typed {
				walk(item, path+"."+strconv.Itoa(index))
				if len(result) >= limit {
					return
				}
			}
		case string:
			result = append(result, SourceFieldOption{Path: path, ValuePreview: truncatePreview(typed), ValueType: "string"})
		case json.Number:
			result = append(result, SourceFieldOption{Path: path, ValuePreview: typed.String(), ValueType: "number"})
		case bool:
			result = append(result, SourceFieldOption{Path: path, ValuePreview: strconv.FormatBool(typed), ValueType: "boolean"})
		}
	}
	walk(value, "$")
	return result
}

func previewMappings(body string, mappings map[string]string) (SourcePreview, []string) {
	preview := SourcePreview{}
	invalid := make([]string, 0)
	for _, field := range supportedFields {
		path := mappings[field]
		if path == "" {
			continue
		}
		value, ok := jsonpathvalue.Lookup(body, strings.TrimPrefix(strings.TrimPrefix(path, "$"), "."))
		if !ok {
			invalid = append(invalid, field)
			continue
		}
		switch field {
		case "country":
			preview.Country, ok = scalarText(value)
		case "province":
			preview.Province, ok = scalarText(value)
		case "city":
			preview.City, ok = scalarText(value)
		case "isp":
			preview.ISP, ok = scalarText(value)
		case "latitude":
			var number float64
			number, ok = numericValue(value)
			if ok && number >= -90 && number <= 90 {
				preview.Latitude = &number
			} else {
				ok = false
			}
		case "longitude":
			var number float64
			number, ok = numericValue(value)
			if ok && number >= -180 && number <= 180 {
				preview.Longitude = &number
			} else {
				ok = false
			}
		}
		if !ok {
			invalid = append(invalid, field)
		}
	}
	return preview, invalid
}

func scalarText(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		return text, text != ""
	case json.Number:
		return typed.String(), true
	case bool:
		return strconv.FormatBool(typed), true
	default:
		return "", false
	}
}

func numericValue(value any) (float64, bool) {
	var text string
	switch typed := value.(type) {
	case json.Number:
		text = typed.String()
	case string:
		text = strings.TrimSpace(typed)
	default:
		return 0, false
	}
	number, err := strconv.ParseFloat(text, 64)
	return number, err == nil
}

func truncatePreview(value string) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= maxFieldPreviewRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxFieldPreviewRunes]) + "…"
}

func validateSourceURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return apperrors.New(consts.StatusBadRequest, "apiUrl 必须是合法的 http(s) 绝对地址")
	}
	if parsed.User != nil {
		return apperrors.New(consts.StatusBadRequest, "apiUrl 不能包含用户名密码凭据")
	}
	validationURL := strings.ReplaceAll(raw, "{ip}", "8.8.8.8")
	if err := httpclient.ValidateURL(validationURL); err != nil {
		return apperrors.New(consts.StatusBadRequest, "apiUrl 必须是合法的 http(s) 绝对地址")
	}
	return nil
}
