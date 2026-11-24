package sms

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"bugpk-api/internal/pkg/httpclient"
)

const (
	defaultTimeout = 5 * time.Second
	minTimeout     = 500 * time.Millisecond
	maxTimeout     = 30 * time.Second

	maxCredentialLen  = 128
	maxSignNameLen    = 64
	maxTemplateIDLen  = 64
	maxRegionLen      = 32
	maxEndpointLen    = 255
	maxLocaleEntries  = 8
	aliyunEndpoint    = "https://dysmsapi.aliyuncs.com"
	tencentEndpoint   = "https://sms.tencentcloudapi.com"
	aliyunDefaultReg  = "cn-hangzhou"
	tencentDefaultReg = "ap-guangzhou"
)

var (
	// ErrNotImplemented 表示 provider_key 不在已接入列表里。
	ErrNotImplemented = errors.New("sms: provider is not implemented")
	// ErrIncompleteConfig 表示必填配置缺失；调用方据此保持 fail-closed，不要降级成"先发一条试试"。
	ErrIncompleteConfig = errors.New("sms: provider configuration is incomplete")
)

// Config 是一个短信通道的运行期配置。
//
// AccessKeySecret 必须由调用方解密后传入：本包不接触密钥存储，
// 也不假设 bp_message_provider_config.extra_config 里的密文格式。
type Config struct {
	ProviderKey       string
	AccessKeyID       string
	AccessKeySecret   string
	SignName          string
	TemplateID        string
	TemplatesByLocale map[string]string
	// AppID 是腾讯的 SmsSdkAppId；阿里通道不使用。
	AppID    string
	Region   string
	Endpoint string
	Timeout  time.Duration

	// Client 为 nil 时使用 httpclient.Outbound：禁用环境代理、校验每个 DNS 结果、
	// 拦截私网与链路本地地址。测试可以注入自己的 Client。
	Client *http.Client
	// Now 与 Nonce 为 nil 时使用真实时钟与随机串，仅为签名可测性保留。
	Now   func() time.Time
	Nonce func() string
}

// extraConfig 对应 bp_message_provider_config.extra_config 的 JSON 形状。
// 字段名沿用 systemsettings 已有的写法，避免同一个键在两处叫不同名字。
type extraConfig struct {
	AccessKeyID     string            `json:"accessKeyId"`
	AccessKeySecret string            `json:"accessKeySecret"`
	SignName        string            `json:"signName"`
	TemplateID      string            `json:"templateId"`
	TemplateIDEn    string            `json:"templateIdEn"`
	Templates       map[string]string `json:"templatesByLocale"`
	AppID           string            `json:"appId"`
	SmsSdkAppID     string            `json:"smsSdkAppId"`
	Region          string            `json:"region"`
	Endpoint        string            `json:"endpoint"`
	TimeoutMS       int               `json:"timeoutMs"`
}

// ParseConfig 从 extra_config JSON 构造 Config。
// 返回的 AccessKeySecret 是存储里的原值（通常是密文），调用方必须解密后再赋值。
func ParseConfig(providerKey, rawJSON string) (Config, error) {
	var extra extraConfig
	trimmed := strings.TrimSpace(rawJSON)
	if trimmed != "" {
		if err := json.Unmarshal([]byte(trimmed), &extra); err != nil {
			return Config{}, fmt.Errorf("sms: decode provider config: %w", err)
		}
	}

	cfg := Config{
		ProviderKey:     strings.TrimSpace(providerKey),
		AccessKeyID:     strings.TrimSpace(extra.AccessKeyID),
		AccessKeySecret: strings.TrimSpace(extra.AccessKeySecret),
		SignName:        strings.TrimSpace(extra.SignName),
		TemplateID:      strings.TrimSpace(extra.TemplateID),
		Region:          strings.TrimSpace(extra.Region),
		Endpoint:        strings.TrimSpace(extra.Endpoint),
		Timeout:         clampTimeout(extra.TimeoutMS),
	}
	// appId 与 smsSdkAppId 是同义字段：管理端早期只写了一个，不能因为改名就让线上配置失效。
	appID := firstNonEmpty(extra.SmsSdkAppID, extra.AppID)
	cfg.AppID = strings.TrimSpace(appID)

	templates := map[string]string{}
	for locale, template := range extra.Templates {
		if len(templates) >= maxLocaleEntries {
			break
		}
		if template = strings.TrimSpace(template); template != "" {
			templates[normalizeLocale(locale)] = template
		}
	}
	if template := strings.TrimSpace(extra.TemplateIDEn); template != "" {
		if _, exists := templates["en"]; !exists {
			templates["en"] = template
		}
	}
	if len(templates) > 0 {
		cfg.TemplatesByLocale = templates
	}
	return cfg, nil
}

// Validate 检查必填项与取值边界，并补齐默认 endpoint/region。
func (c *Config) Validate() error {
	if c == nil {
		return ErrIncompleteConfig
	}
	c.ProviderKey = strings.ToLower(strings.TrimSpace(c.ProviderKey))
	if c.ProviderKey == "" {
		return fmt.Errorf("%w: provider key is required", ErrIncompleteConfig)
	}
	switch c.ProviderKey {
	case ProviderAliyun, ProviderTencent:
	default:
		return fmt.Errorf("%w: %s", ErrNotImplemented, c.ProviderKey)
	}
	if err := bounded("access key id", c.AccessKeyID, 1, maxCredentialLen); err != nil {
		return err
	}
	if err := bounded("access key secret", c.AccessKeySecret, 1, maxCredentialLen); err != nil {
		return err
	}
	if err := bounded("sign name", c.SignName, 1, maxSignNameLen); err != nil {
		return err
	}
	if err := bounded("template id", c.TemplateID, 1, maxTemplateIDLen); err != nil {
		return err
	}
	if c.ProviderKey == ProviderTencent {
		if err := bounded("app id", c.AppID, 1, maxTemplateIDLen); err != nil {
			return err
		}
	}
	for locale, template := range c.TemplatesByLocale {
		if err := bounded("template for locale "+locale, template, 1, maxTemplateIDLen); err != nil {
			return err
		}
	}
	if c.Timeout == 0 {
		c.Timeout = defaultTimeout
	}
	if c.Timeout < minTimeout || c.Timeout > maxTimeout {
		c.Timeout = defaultTimeout
	}
	switch c.ProviderKey {
	case ProviderAliyun:
		c.Endpoint = firstNonEmpty(c.Endpoint, aliyunEndpoint)
		c.Region = firstNonEmpty(c.Region, aliyunDefaultReg)
	case ProviderTencent:
		c.Endpoint = firstNonEmpty(c.Endpoint, tencentEndpoint)
		c.Region = firstNonEmpty(c.Region, tencentDefaultReg)
	}
	if err := bounded("region", c.Region, 1, maxRegionLen); err != nil {
		return err
	}
	if err := bounded("endpoint", c.Endpoint, 1, maxEndpointLen); err != nil {
		return err
	}
	// endpoint 由管理员填写，必须过与出站请求同一套规则：
	// 只允许 http(s)、不允许 userinfo 与异常端口。私网拦截由 Outbound 的
	// DNS 解析策略负责，这里不重复实现。
	if err := httpclient.ValidateURL(c.Endpoint); err != nil {
		return fmt.Errorf("sms: invalid endpoint: %w", err)
	}
	return nil
}

// httpClient 返回配置的 Client，缺省给出出站安全默认值。
func (c Config) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return httpclient.New(httpclient.Options{Timeout: c.Timeout})
}

func (c Config) currentTime() time.Time {
	if c.Now != nil {
		if now := c.Now(); !now.IsZero() {
			return now
		}
	}
	return time.Now()
}

func clampTimeout(timeoutMS int) time.Duration {
	if timeoutMS <= 0 {
		return defaultTimeout
	}
	timeout := time.Duration(timeoutMS) * time.Millisecond
	if timeout < minTimeout {
		return minTimeout
	}
	if timeout > maxTimeout {
		return maxTimeout
	}
	return timeout
}

func bounded(field, value string, minLen, maxLen int) error {
	if len(value) < minLen {
		return fmt.Errorf("%w: %s is required", ErrIncompleteConfig, field)
	}
	if len(value) > maxLen {
		return fmt.Errorf("sms: %s must be at most %d characters", field, maxLen)
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
