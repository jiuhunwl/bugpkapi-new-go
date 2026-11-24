package sms

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"strings"

	"bugpk-api/internal/pkg/httpclient"
	"bugpk-api/internal/pkg/phoneutil"
)

const (
	aliyunAPIVersion   = "2017-05-25"
	maxAliyunBodyBytes = int64(64 << 10)
)

// aliyunProvider 走阿里云短信服务（Dysmsapi）SendSms。
//
// 注意 endpoint 是 dysmsapi，不是号码认证服务的 dypnsapi：systemsettings 里
// 沿用的旧默认值指向后者，接进来时必须以这里为准。
type aliyunProvider struct {
	cfg    Config
	client *http.Client
}

type aliyunResponse struct {
	Code      string `json:"Code"`
	Message   string `json:"Message"`
	RequestID string `json:"RequestId"`
	BizID     string `json:"BizId"`
}

func newAliyun(cfg Config) (Provider, error) {
	provider := &aliyunProvider{cfg: cfg, client: cfg.httpClient()}
	if provider.client == nil {
		return nil, ErrIncompleteConfig
	}
	return provider, nil
}

func (p *aliyunProvider) Key() string { return ProviderAliyun }

func (p *aliyunProvider) Capabilities() Capabilities {
	// 国际/港澳台短信需要在控制台单独开通权限，能力位只声明接口支持。
	return Capabilities{International: true, DeliveryReport: true}
}

func (p *aliyunProvider) Send(ctx context.Context, msg Message) (Receipt, error) {
	if err := validateMessage(msg); err != nil {
		return Receipt{}, err
	}
	template, ok := selectTemplate(p.cfg.TemplateID, p.cfg.TemplatesByLocale, msg.Locale)
	if !ok {
		return Receipt{}, newError(ProviderAliyun, "missing_template",
			"sms template id is not configured", ClassPermanent, nil)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, newError(ProviderAliyun, "context_canceled", "", ClassRetryable, err)
	}

	templateParam, err := json.Marshal(map[string]string{"code": msg.Code})
	if err != nil {
		return Receipt{}, newError(ProviderAliyun, "invalid_template_param", "", ClassPermanent, err)
	}

	params := map[string]string{
		"AccessKeyId":      p.cfg.AccessKeyID,
		"Action":           "SendSms",
		"Format":           "JSON",
		"RegionId":         p.cfg.Region,
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   p.signatureNonce(),
		"SignatureVersion": "1.0",
		"Timestamp":        p.cfg.currentTime().UTC().Format(timestampLayout),
		"Version":          aliyunAPIVersion,
		"PhoneNumbers":     aliyunRecipient(msg.To),
		"SignName":         p.cfg.SignName,
		"TemplateCode":     template,
		"TemplateParam":    string(templateParam),
	}
	params["Signature"] = aliyunSignature(p.cfg.AccessKeySecret, aliyunStringToSign(http.MethodGet, params))

	target := strings.TrimSuffix(p.cfg.Endpoint, "/") + "/?" + canonicalAliyunQuery(params)
	if err := httpclient.ValidateURL(target); err != nil {
		return Receipt{}, newError(ProviderAliyun, "invalid_endpoint", "", ClassPermanent, err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Receipt{}, newError(ProviderAliyun, "invalid_request", "", ClassPermanent, err,
			func(detail string) string { return maskNumber(detail, msg.To) })
	}
	request.Header.Set("Accept", "application/json")

	response, err := p.client.Do(request)
	if err != nil {
		return Receipt{}, newError(ProviderAliyun, "request_failed",
			"sms service is temporarily unavailable", ClassRetryable, err,
			func(detail string) string { return maskNumber(detail, msg.To) })
	}
	defer func() { _ = response.Body.Close() }()

	var payload aliyunResponse
	decodeErr := httpclient.DecodeJSON(response.Body, maxAliyunBodyBytes, &payload)
	detail := strings.TrimSpace(payload.Message)

	if response.StatusCode != http.StatusOK {
		class := classFromHTTPStatus(response.StatusCode)
		if coded := classifyAliyunCode(payload.Code); coded != ClassUnknown {
			class = coded
		}
		return Receipt{}, newError(ProviderAliyun, payload.Code, detail, class, decodeErr,
			func(text string) string { return maskNumber(text, msg.To) })
	}
	if decodeErr != nil {
		return Receipt{}, newError(ProviderAliyun, "response_undecodable",
			"sms service returned an unexpected response", ClassRetryable, decodeErr)
	}

	class := classifyAliyunCode(payload.Code)
	if class == ClassOK {
		return Receipt{Provider: ProviderAliyun, RequestID: payload.RequestID, BizID: payload.BizID}, nil
	}
	return Receipt{}, newError(ProviderAliyun, payload.Code, detail, class, nil,
		func(text string) string { return maskNumber(text, msg.To) })
}

func (p *aliyunProvider) signatureNonce() string {
	if p.cfg.Nonce != nil {
		if nonce := strings.TrimSpace(p.cfg.Nonce()); nonce != "" {
			return nonce
		}
	}
	return rand.Text()
}

// aliyunRecipient 按上游口径转换号码格式：国内短信要 11 位裸号，
// 国际/港澳台消息要带 "+" 的 E.164。
//
// 这里刻意做成一个函数：阿里云对国际号码是否保留 "+" 的文档口径不一致，
// 首次真实联调若需要改动，只改这一处，不动签名与调用链。
func aliyunRecipient(to string) string {
	if phoneutil.IsMainlandMobile(to) {
		digits := phoneutil.Digits(to)
		return digits[len(phoneutil.CountryCodeCN):]
	}
	return to
}

var aliyunRetryableCodes = map[string]struct{}{
	"SYSTEM_ERROR":       {},
	"InternalError":      {},
	"isv.SMS_OVERLOAD":   {},
	"ServiceUnavailable": {},
}

var aliyunPermanentCodes = map[string]struct{}{
	"MISSING_PARAMETER":               {},
	"INVALID_PARAMETER":               {},
	"INVALID_ACCOUNT_MSG":             {},
	"Forbidden":                       {},
	"isv.SMS_SIGNATURE_ILLEGAL":       {},
	"isv.SMS_TEMPLATE_ILLEGAL":        {},
	"isv.SIGN_NAME_ILLEGAL":           {},
	"isv.MOBILE_NUMBER_ILLEGAL":       {},
	"isv.MOBILE_COUNT_OVER_LIMIT":     {},
	"isv.TEMPLATE_MISSING_PARAMETERS": {},
	"isv.ACCOUNT_NOT_EXISTS":          {},
	"isv.ACCOUNT_ABNORMAL":            {},
	"isv.BLACK_KEY_CONTROL_LIMIT":     {},
	"isv.OUT_OF_SERVICE":              {},
	"isv.AMOUNT_NOT_ENOUGH":           {},
	"PORT_NOT_REGISTERED":             {},
	"isv.PARAM_NOT_SUPPORT_URL":       {},
}

// classifyAliyunCode 把上游返回码归类。未收录的码返回 ClassUnknown：
// 按失败处理但不自动重试，等运营根据发送流水补表，避免把资质类错误当成网络抖动反复烧额度。
func classifyAliyunCode(code string) Class {
	code = strings.TrimSpace(code)
	if code == "" {
		return ClassUnknown
	}
	if code == "OK" {
		return ClassOK
	}
	if _, ok := aliyunRetryableCodes[code]; ok {
		return ClassRetryable
	}
	if _, ok := aliyunPermanentCodes[code]; ok {
		return ClassPermanent
	}
	switch {
	case strings.HasPrefix(code, "Throttling"):
		return ClassRetryable
	case strings.HasPrefix(code, "isv."):
		return ClassPermanent
	default:
		return ClassUnknown
	}
}
