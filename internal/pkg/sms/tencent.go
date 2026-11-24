package sms

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/pkg/httpclient"
)

const (
	maxTencentBodyBytes      = int64(64 << 10)
	tencentContentType       = "application/json; charset=utf-8"
	tencentSignedHeaderNames = "content-type;host;x-tc-action"
)

// tencentProvider 走腾讯云短信 Sms.SendSms（TC3-HMAC-SHA256 签名）。
type tencentProvider struct {
	cfg    Config
	client *http.Client
	host   string
}

type tencentResponse struct {
	Response struct {
		SendStatusSet []struct {
			Code        string `json:"Code"`
			Message     string `json:"Message"`
			SerialNo    string `json:"SerialNo"`
			PhoneNumber string `json:"PhoneNumber"`
		} `json:"SendStatusSet"`
		RequestID string `json:"RequestId"`
		Error     *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"Response"`
}

func newTencent(cfg Config) (Provider, error) {
	parsed, err := url.Parse(cfg.Endpoint)
	if err != nil || parsed.Hostname() == "" {
		return nil, ErrIncompleteConfig
	}
	provider := &tencentProvider{cfg: cfg, client: cfg.httpClient(), host: parsed.Host}
	if provider.client == nil {
		return nil, ErrIncompleteConfig
	}
	return provider, nil
}

func (p *tencentProvider) Key() string { return ProviderTencent }

func (p *tencentProvider) Capabilities() Capabilities {
	return Capabilities{International: true, DeliveryReport: true}
}

func (p *tencentProvider) Send(ctx context.Context, msg Message) (Receipt, error) {
	if err := validateMessage(msg); err != nil {
		return Receipt{}, err
	}
	template, ok := selectTemplate(p.cfg.TemplateID, p.cfg.TemplatesByLocale, msg.Locale)
	if !ok {
		return Receipt{}, newError(ProviderTencent, "missing_template",
			"sms template id is not configured", ClassPermanent, nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, newError(ProviderTencent, "context_canceled", "", ClassRetryable, err)
	}

	body, err := json.Marshal(map[string]any{
		"PhoneNumberSet": []string{msg.To},
		"SmsSdkAppId":    p.cfg.AppID,
		"SignName":       p.cfg.SignName,
		"TemplateId":     template,
		// 模板参数是有序数组，按 {{1}}/{{2}} 位置匹配：把验证码放进数组而不是
		// 拼字符串，模板内容永远没有机会注入控制字符。
		"TemplateParamSet": []string{msg.Code},
	})
	if err != nil {
		return Receipt{}, newError(ProviderTencent, "invalid_request_body", "", ClassPermanent, err)
	}

	now := p.cfg.currentTime().UTC()
	authorization := p.authorization(now, body)

	target := strings.TrimSuffix(p.cfg.Endpoint, "/") + "/"
	if err := httpclient.ValidateURL(target); err != nil {
		return Receipt{}, newError(ProviderTencent, "invalid_endpoint", "", ClassPermanent, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return Receipt{}, newError(ProviderTencent, "invalid_request", "", ClassPermanent, err,
			func(text string) string { return maskNumber(text, msg.To) })
	}
	request.Header.Set("Content-Type", tencentContentType)
	request.Header.Set("X-TC-Action", tencentAction)
	request.Header.Set("X-TC-Version", tencentVersion)
	request.Header.Set("X-TC-Timestamp", strconv.FormatInt(now.Unix(), 10))
	request.Header.Set("X-TC-Region", p.cfg.Region)
	request.Header.Set("Authorization", authorization)

	response, err := p.client.Do(request)
	if err != nil {
		return Receipt{}, newError(ProviderTencent, "request_failed",
			"sms service is temporarily unavailable", ClassRetryable, err,
			func(text string) string { return maskNumber(text, msg.To) })
	}
	defer func() { _ = response.Body.Close() }()

	payload, readErr := p.readBody(response)
	if readErr != nil {
		return Receipt{}, newError(ProviderTencent, "response_unreadable",
			"sms service returned an unreadable response", ClassRetryable, readErr)
	}
	if payload.Response.Error != nil {
		class := classifyTencentCode(payload.Response.Error.Code)
		if response.StatusCode != http.StatusOK && class == ClassUnknown {
			class = classFromHTTPStatus(response.StatusCode)
		}
		return Receipt{}, newError(ProviderTencent, payload.Response.Error.Code,
			payload.Response.Error.Message, class, nil,
			func(text string) string { return maskNumber(text, msg.To) })
	}

	if response.StatusCode != http.StatusOK {
		return Receipt{}, newError(ProviderTencent, http.StatusText(response.StatusCode),
			"sms service rejected the request", classFromHTTPStatus(response.StatusCode), nil)
	}
	if len(payload.Response.SendStatusSet) == 0 {
		return Receipt{}, newError(ProviderTencent, "empty_send_status",
			"sms service returned no send status", ClassRetryable, nil)
	}

	// SendSms 逐号码返回状态；本通道一次只发一条，取第一个即可。
	status := payload.Response.SendStatusSet[0]
	if classifyTencentCode(status.Code) == ClassOK {
		return Receipt{Provider: ProviderTencent, RequestID: payload.Response.RequestID, BizID: status.SerialNo}, nil
	}
	return Receipt{}, newError(ProviderTencent, status.Code, status.Message,
		classifyTencentCode(status.Code), nil,
		func(text string) string { return maskNumber(text, msg.To) })
}

func (p *tencentProvider) readBody(response *http.Response) (tencentResponse, error) {
	var payload tencentResponse
	// 严格解码：上游返回重复键或超深嵌套时不应该安静接受。
	if err := httpclient.DecodeJSON(response.Body, maxTencentBodyBytes, &payload); err != nil {
		return payload, err
	}
	return payload, nil
}

// authorization 生成 TC3-HMAC-SHA256 Authorization 头。
//
// 规范请求里的头部值必须与实际发送值逐字节一致（含大小写），官方示例
// x-tc-action:DescribeRegions 就是原样参与签名；这里若把值转小写，验签必失败。
func (p *tencentProvider) authorization(now time.Time, payload []byte) string {
	headers := map[string]string{
		"content-type": tencentContentType,
		"host":         p.host,
		"x-tc-action":  tencentAction,
	}
	canonicalRequest := tencentCanonicalRequest(http.MethodPost, "/", "", headers, payload)
	stringToSign := tencentStringToSign(now, canonicalRequest)
	date := now.Format("2006-01-02")
	signature := tencentSignature(p.cfg.AccessKeySecret, date, stringToSign)
	return tencentAuthorization(p.cfg.AccessKeyID, date, tencentSignedHeaderNames, signature)
}

var tencentRetryableCodes = map[string]struct{}{
	"InternalError":                       {},
	"RequestLimitExceeded":                {},
	"FailedToRequestSmsService":           {},
	"FailedToSend.MessageDeliveryTimeout": {},
}

var tencentPermanentCodePrefixes = []string{
	"AuthFailure",
	"InvalidAction",
	"InvalidParameter",
	"InvalidParameterValue",
	"InvalidRegion",
	"MissingParameter",
	"UnsupportedOperation",
	"FailedToSend",
	"LimitCount",
	"FailedToQueryWhitelist",
	"Unregistered",
}

// classifyTencentCode 与阿里侧同策略：未收录的码不自动重试，避免把资质类
// 问题当成网络抖动反复消耗发送额度。
func classifyTencentCode(code string) Class {
	code = strings.TrimSpace(code)
	if code == "" {
		return ClassUnknown
	}
	if code == "Ok" || code == "Success" {
		return ClassOK
	}
	if _, ok := tencentRetryableCodes[code]; ok {
		return ClassRetryable
	}
	for _, prefix := range tencentPermanentCodePrefixes {
		if strings.HasPrefix(code, prefix) {
			return ClassPermanent
		}
	}
	switch {
	case strings.HasPrefix(code, "InternalError"), strings.HasPrefix(code, "RequestLimit"):
		return ClassRetryable
	default:
		return ClassUnknown
	}
}
