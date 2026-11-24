package sms

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/pkg/phoneutil"
)

var pinnedNow = time.Date(2026, 11, 4, 8, 30, 0, 0, time.UTC)

const testSecret = "very-secret-value"

type capturedRequest struct {
	method string
	path   string
	query  map[string][]string
	header http.Header
	body   string
	host   string
}

func newCaptureServer(t *testing.T, status int, responseBody string) (*httptest.Server, func() capturedRequest, *int) {
	t.Helper()
	var (
		last  capturedRequest
		calls int
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(r.Body)
		last = capturedRequest{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.Query(),
			header: r.Header,
			body:   string(body),
			host:   r.Host,
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, responseBody)
	}))
	t.Cleanup(server.Close)
	return server, func() capturedRequest { return last }, &calls
}

func testConfig(providerKey, endpoint string) Config {
	return Config{
		ProviderKey:     providerKey,
		AccessKeyID:     "LTAI-test",
		AccessKeySecret: testSecret,
		SignName:        "BugPk",
		TemplateID:      "SMS_1001",
		AppID:           "1400000000",
		Endpoint:        endpoint,
		// 测试注入明文 Client：默认 Client 会拦掉私网目标，而 httptest 服务在 127.0.0.1。
		Client: &http.Client{},
		Now:    func() time.Time { return pinnedNow },
		Nonce:  func() string { return "fixed-nonce" },
	}
}

func mustProvider(t *testing.T, cfg Config) Provider {
	t.Helper()
	provider, err := New(cfg)
	if err != nil {
		t.Fatalf("New(%s) error: %v", cfg.ProviderKey, err)
	}
	return provider
}

func asSMSError(err error) (*Error, bool) {
	var smsErr *Error
	if errors.As(err, &smsErr) {
		return smsErr, true
	}
	return nil, false
}

func aliyunQuerySignatureValid(captured capturedRequest) bool {
	params := map[string]string{}
	for key, values := range captured.query {
		if key == "Signature" || len(values) == 0 {
			continue
		}
		params[key] = values[0]
	}
	got := aliyunSignature(testSecret, aliyunStringToSign(captured.method, params))
	return got == captured.query["Signature"][0]
}

func TestAliyunSendSucceeds(t *testing.T) {
	server, _, calls := newCaptureServer(t, http.StatusOK,
		`{"Code":"OK","Message":"OK","RequestId":"REQ-1","BizId":"BIZ-1"}`)
	provider := mustProvider(t, testConfig(ProviderAliyun, server.URL))

	receipt, err := provider.Send(context.Background(), Message{
		To: "+8613800138000", Scene: "register", Code: "123456",
	})
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("calls = %d, want 1", *calls)
	}
	if receipt.Provider != ProviderAliyun || receipt.RequestID != "REQ-1" || receipt.BizID != "BIZ-1" {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestAliyunRequestShape(t *testing.T) {
	server, lastCall, _ := newCaptureServer(t, http.StatusOK, `{"Code":"OK","RequestId":"REQ-1"}`)
	provider := mustProvider(t, testConfig(ProviderAliyun, server.URL))

	if _, err := provider.Send(context.Background(), Message{To: "+8613800138000", Scene: "login", Code: "123456"}); err != nil {
		t.Fatalf("Send error: %v", err)
	}
	captured := lastCall()

	if captured.method != http.MethodGet || captured.path != "/" {
		t.Fatalf("request = %s %q", captured.method, captured.path)
	}
	query := captured.query
	for _, key := range []string{
		"AccessKeyId", "Action", "Format", "RegionId", "SignatureMethod",
		"SignatureNonce", "SignatureVersion", "Timestamp", "Version",
		"PhoneNumbers", "SignName", "TemplateCode", "TemplateParam", "Signature",
	} {
		if len(query[key]) != 1 {
			t.Fatalf("query param %q missing or repeated: %v", key, query[key])
		}
	}
	if query["Action"][0] != "SendSms" || query["Version"][0] != aliyunAPIVersion {
		t.Fatalf("action/version = %q / %q", query["Action"][0], query["Version"][0])
	}
	// 国内短信要 11 位裸号，带 "+86" 会被上游判成非法号码。
	if query["PhoneNumbers"][0] != "13800138000" {
		t.Fatalf("PhoneNumbers = %q", query["PhoneNumbers"][0])
	}
	if query["TemplateParam"][0] != `{"code":"123456"}` {
		t.Fatalf("TemplateParam = %q", query["TemplateParam"][0])
	}
	if query["Timestamp"][0] != "2026-11-04T08:30:00Z" {
		t.Fatalf("Timestamp = %q", query["Timestamp"][0])
	}
	if query["SignatureNonce"][0] != "fixed-nonce" {
		t.Fatalf("SignatureNonce = %q", query["SignatureNonce"][0])
	}
	if query["RegionId"][0] != "cn-hangzhou" || query["SignName"][0] != "BugPk" {
		t.Fatalf("region/sign = %q / %q", query["RegionId"][0], query["SignName"][0])
	}
	// 参与签名的串必须与真正出网的串一致，否则线上表现为随机验签失败。
	if !aliyunQuerySignatureValid(captured) {
		t.Fatal("signature does not match the request that went out")
	}
	for key, values := range query {
		if key == "AccessKeyId" {
			continue
		}
		for _, value := range values {
			if strings.Contains(value, testSecret) {
				t.Fatalf("secret leaked into query param %q", key)
			}
		}
	}
}

func TestAliyunInternationalKeepsE164(t *testing.T) {
	server, lastCall, _ := newCaptureServer(t, http.StatusOK, `{"Code":"OK"}`)
	provider := mustProvider(t, testConfig(ProviderAliyun, server.URL))

	if _, err := provider.Send(context.Background(), Message{To: "+14155552671", Scene: "login", Code: "1234"}); err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if got := lastCall().query["PhoneNumbers"][0]; got != "+14155552671" {
		t.Fatalf("PhoneNumbers = %q, want E.164 with plus", got)
	}
}

func TestAliyunLocaleTemplate(t *testing.T) {
	server, lastCall, _ := newCaptureServer(t, http.StatusOK, `{"Code":"OK"}`)
	cfg := testConfig(ProviderAliyun, server.URL)
	cfg.TemplatesByLocale = map[string]string{"en": "SMS_en_1001"}
	provider := mustProvider(t, cfg)

	if _, err := provider.Send(context.Background(), Message{
		To: "+8613800138000", Scene: "register", Locale: "en-US", Code: "123456",
	}); err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if got := lastCall().query["TemplateCode"][0]; got != "SMS_en_1001" {
		t.Fatalf("TemplateCode = %q", got)
	}
}

func TestAliyunErrorClassification(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantClass Class
		wantCode  string
		wantRetry bool
	}{
		{name: "quota exhausted", status: http.StatusOK, body: `{"Code":"isv.AMOUNT_NOT_ENOUGH","Message":"余额不足"}`,
			wantClass: ClassPermanent, wantCode: "isv.AMOUNT_NOT_ENOUGH"},
		{name: "signature illegal", status: http.StatusOK, body: `{"Code":"isv.SMS_SIGNATURE_ILLEGAL","Message":"签名待审核"}`,
			wantClass: ClassPermanent, wantCode: "isv.SMS_SIGNATURE_ILLEGAL"},
		{name: "unregistered port", status: http.StatusOK, body: `{"Code":"PORT_NOT_REGISTERED","Message":"资质未报备"}`,
			wantClass: ClassPermanent, wantCode: "PORT_NOT_REGISTERED"},
		{name: "system error", status: http.StatusOK, body: `{"Code":"SYSTEM_ERROR","Message":"system busy"}`,
			wantClass: ClassRetryable, wantCode: "SYSTEM_ERROR", wantRetry: true},
		{name: "throttling", status: http.StatusOK, body: `{"Code":"Throttling.User","Message":"too many"}`,
			wantClass: ClassRetryable, wantCode: "Throttling.User", wantRetry: true},
		{name: "unmapped code", status: http.StatusOK, body: `{"Code":"weird.NEW_CODE","Message":"?"}`,
			wantClass: ClassUnknown, wantCode: "weird.NEW_CODE", wantRetry: true},
		{name: "gateway down", status: http.StatusBadGateway, body: `not json`,
			wantClass: ClassRetryable, wantRetry: true},
		{name: "forbidden", status: http.StatusForbidden, body: `{"Code":"Forbidden","Message":"denied"}`,
			wantClass: ClassPermanent, wantCode: "Forbidden"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _, _ := newCaptureServer(t, tt.status, tt.body)
			provider := mustProvider(t, testConfig(ProviderAliyun, server.URL))
			_, err := provider.Send(context.Background(), Message{To: "+8613800138000", Scene: "register", Code: "123456"})
			if err == nil {
				t.Fatal("expected error")
			}
			smsErr, ok := asSMSError(err)
			if !ok {
				t.Fatalf("error type = %T (%v)", err, err)
			}
			if smsErr.Class != tt.wantClass {
				t.Fatalf("class = %v, want %v (%v)", smsErr.Class, tt.wantClass, smsErr)
			}
			if tt.wantCode != "" && smsErr.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q", smsErr.Code, tt.wantCode)
			}
			if smsErr.IsRetryable() != tt.wantRetry {
				t.Fatalf("retryable = %v, want %v", smsErr.IsRetryable(), tt.wantRetry)
			}
			if smsErr.Provider != ProviderAliyun {
				t.Fatalf("provider = %q", smsErr.Provider)
			}
		})
	}
}

func TestAliyunSendRejectsBadInputBeforeNetwork(t *testing.T) {
	server, _, calls := newCaptureServer(t, http.StatusOK, `{"Code":"OK"}`)
	provider := mustProvider(t, testConfig(ProviderAliyun, server.URL))

	// 号码必须是归一化后的 E.164：原始输入不允许直接出网。
	for _, msg := range []Message{
		{To: "13800138000", Scene: "register", Code: "123456"},
		{To: "+8613800138000", Scene: "", Code: "123456"},
		{To: "+8613800138000", Scene: "register", Code: "12"},
		{To: "+8613800138000", Scene: "register", Code: "12ab"},
		{To: "+8613800138000", Scene: "register", Code: "123456789"},
	} {
		if _, err := provider.Send(context.Background(), msg); err == nil {
			t.Fatalf("Send(%+v) unexpectedly accepted", msg)
		}
	}
	if *calls != 0 {
		t.Fatalf("upstream calls = %d, want 0", *calls)
	}
}

func TestMissingTemplateIsRejectedAtConstruction(t *testing.T) {
	server, _, calls := newCaptureServer(t, http.StatusOK, `{"Code":"OK"}`)
	cfg := testConfig(ProviderAliyun, server.URL)
	cfg.TemplateID = ""
	if _, err := New(cfg); !errors.Is(err, ErrIncompleteConfig) {
		t.Fatalf("New error = %v, want ErrIncompleteConfig", err)
	}
	if *calls != 0 {
		t.Fatalf("upstream calls = %d, want 0", *calls)
	}
}

func TestErrorStringsNeverLeakNumberOrSecret(t *testing.T) {
	const to = "+8613800138000"
	body := `{"Code":"isv.MOBILE_NUMBER_ILLEGAL","Message":"number ` + to + ` invalid, Signature=plainly-visible-secret"}`
	server, _, _ := newCaptureServer(t, http.StatusOK, body)
	provider := mustProvider(t, testConfig(ProviderAliyun, server.URL))

	_, err := provider.Send(context.Background(), Message{To: to, Scene: "register", Code: "123456"})
	if err == nil {
		t.Fatal("expected error")
	}
	text := err.Error()
	if strings.Contains(text, phoneutil.Digits(to)) || strings.Contains(text, strings.TrimPrefix(to, "+")) {
		t.Fatalf("error leaked the full number: %q", text)
	}
	if !strings.Contains(text, phoneutil.Mask(to)) {
		t.Fatalf("error should carry the masked number: %q", text)
	}
	if strings.Contains(text, "plainly-visible-secret") {
		t.Fatalf("error leaked a signature value: %q", text)
	}
	if strings.Contains(text, testSecret) {
		t.Fatalf("error leaked the access key secret: %q", text)
	}
}

func TestTencentSendSucceeds(t *testing.T) {
	const response = `{"Response":{"SendStatusSet":[{"Code":"Ok","Message":"success","SerialNo":"SN-1","PhoneNumber":"+8613800138000"}],"RequestId":"REQ-9"}}`
	server, lastCall, calls := newCaptureServer(t, http.StatusOK, response)
	provider := mustProvider(t, testConfig(ProviderTencent, server.URL))

	receipt, err := provider.Send(context.Background(), Message{To: "+8613800138000", Scene: "register", Code: "123456"})
	if err != nil {
		t.Fatalf("Send error: %v", err)
	}
	if *calls != 1 {
		t.Fatalf("calls = %d, want 1", *calls)
	}
	if receipt.Provider != ProviderTencent || receipt.RequestID != "REQ-9" || receipt.BizID != "SN-1" {
		t.Fatalf("receipt = %+v", receipt)
	}

	captured := lastCall()
	if captured.method != http.MethodPost {
		t.Fatalf("method = %s", captured.method)
	}
	header := captured.header
	if got := header.Get("X-TC-Action"); got != tencentAction {
		t.Fatalf("X-TC-Action = %q", got)
	}
	if got := header.Get("X-TC-Version"); got != tencentVersion {
		t.Fatalf("X-TC-Version = %q", got)
	}
	if got := header.Get("X-TC-Region"); got != "ap-guangzhou" {
		t.Fatalf("X-TC-Region = %q", got)
	}
	if got := header.Get("X-TC-Timestamp"); got != "1793781000" {
		t.Fatalf("X-TC-Timestamp = %q, want unix seconds of the pinned time", got)
	}
	if got := header.Get("Content-Type"); got != tencentContentType {
		t.Fatalf("Content-Type = %q", got)
	}

	authorization := header.Get("Authorization")
	if !strings.HasPrefix(authorization, tencentAlgorithm+" Credential=LTAI-test/2026-11-04/sms/tc3_request, ") {
		t.Fatalf("Authorization = %q", authorization)
	}
	if !strings.Contains(authorization, "SignedHeaders=content-type;host;x-tc-action, Signature=") {
		t.Fatalf("Authorization signed headers = %q", authorization)
	}
	// 用出网后被捕获的请求重算签名，确认 Authorization 覆盖的就是这份内容。
	if !strings.HasSuffix(authorization, rebuildTencentSignature(captured)) {
		t.Fatal("Authorization signature does not match the request that went out")
	}
	if !strings.Contains(captured.body, `"PhoneNumberSet":["+8613800138000"]`) {
		t.Fatalf("body = %q", captured.body)
	}
	if !strings.Contains(captured.body, `"TemplateParamSet":["123456"]`) ||
		!strings.Contains(captured.body, `"SmsSdkAppId":"1400000000"`) ||
		!strings.Contains(captured.body, `"TemplateId":"SMS_1001"`) {
		t.Fatalf("body = %q", captured.body)
	}
	// 验证码走数组参数，模板内容没有拼接进字符串的通道。
	if strings.Contains(captured.body, testSecret) {
		t.Fatal("secret leaked into the request body")
	}
}

func rebuildTencentSignature(captured capturedRequest) string {
	headers := map[string]string{
		"content-type": captured.header.Get("Content-Type"),
		"host":         captured.host,
		"x-tc-action":  captured.header.Get("X-TC-Action"),
	}
	canonical := tencentCanonicalRequest(captured.method, captured.path, "", headers, []byte(captured.body))
	stringToSign := tencentStringToSign(pinnedNow, canonical)
	return tencentSignature(testSecret, "2026-11-04", stringToSign)
}

func TestTencentErrorClassification(t *testing.T) {
	tests := []struct {
		name      string
		status    int
		body      string
		wantClass Class
		wantCode  string
	}{
		{name: "auth failure", status: http.StatusOK,
			body:      `{"Response":{"Error":{"Code":"AuthFailure.SecretIdNotFound","Message":"密钥不存在"},"RequestId":"R"}}`,
			wantClass: ClassPermanent, wantCode: "AuthFailure.SecretIdNotFound"},
		{name: "template not approved", status: http.StatusOK,
			body:      `{"Response":{"Error":{"Code":"FailedToSend.SmsSignatureNotApproved","Message":"模板未审核"},"RequestId":"R"}}`,
			wantClass: ClassPermanent, wantCode: "FailedToSend.SmsSignatureNotApproved"},
		{name: "per number failure", status: http.StatusOK,
			body:      `{"Response":{"SendStatusSet":[{"Code":"InvalidParameterValue.PhoneNumberInvalid","Message":"号码非法","PhoneNumber":"+8613800138000"}],"RequestId":"R"}}`,
			wantClass: ClassPermanent, wantCode: "InvalidParameterValue.PhoneNumberInvalid"},
		{name: "limit exceeded", status: http.StatusOK,
			body:      `{"Response":{"Error":{"Code":"RequestLimitExceeded","Message":"qps 超限"},"RequestId":"R"}}`,
			wantClass: ClassRetryable, wantCode: "RequestLimitExceeded"},
		{name: "internal error", status: http.StatusOK,
			body:      `{"Response":{"Error":{"Code":"InternalError","Message":"内部错误"},"RequestId":"R"}}`,
			wantClass: ClassRetryable, wantCode: "InternalError"},
		{name: "unmapped code", status: http.StatusOK,
			body:      `{"Response":{"Error":{"Code":"Something.New","Message":"?"},"RequestId":"R"}}`,
			wantClass: ClassUnknown, wantCode: "Something.New"},
		{name: "empty status set", status: http.StatusOK,
			body:      `{"Response":{"SendStatusSet":[],"RequestId":"R"}}`,
			wantClass: ClassRetryable},
		{name: "service unavailable", status: http.StatusServiceUnavailable,
			body:      `{"Response":{"Error":{"Code":"InternalError","Message":"unavailable"},"RequestId":"R"}}`,
			wantClass: ClassRetryable, wantCode: "InternalError"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, _, _ := newCaptureServer(t, tt.status, tt.body)
			provider := mustProvider(t, testConfig(ProviderTencent, server.URL))
			_, err := provider.Send(context.Background(), Message{To: "+8613800138000", Scene: "bind_mobile", Code: "123456"})
			if err == nil {
				t.Fatal("expected error")
			}
			smsErr, ok := asSMSError(err)
			if !ok {
				t.Fatalf("error type = %T (%v)", err, err)
			}
			if smsErr.Class != tt.wantClass {
				t.Fatalf("class = %v, want %v (%v)", smsErr.Class, tt.wantClass, smsErr)
			}
			if tt.wantCode != "" && smsErr.Code != tt.wantCode {
				t.Fatalf("code = %q, want %q", smsErr.Code, tt.wantCode)
			}
			if smsErr.Provider != ProviderTencent {
				t.Fatalf("provider = %q", smsErr.Provider)
			}
		})
	}
}

func TestTencentErrorCodeDetailIsMasked(t *testing.T) {
	const to = "+8613800138000"
	body := `{"Response":{"Error":{"Code":"FailedToSend","Message":"cannot deliver ` + to + ` now"},"RequestId":"R"}}`
	server, _, _ := newCaptureServer(t, http.StatusOK, body)
	provider := mustProvider(t, testConfig(ProviderTencent, server.URL))

	_, err := provider.Send(context.Background(), Message{To: to, Scene: "login", Code: "123456"})
	smsErr, ok := asSMSError(err)
	if !ok {
		t.Fatalf("error type = %T (%v)", err, err)
	}
	if strings.Contains(smsErr.Error(), strings.TrimPrefix(to, "+")) {
		t.Fatalf("error leaked the full number: %q", smsErr.Error())
	}
}

func TestProviderHonorsCanceledContext(t *testing.T) {
	server, _, calls := newCaptureServer(t, http.StatusOK, `{"Code":"OK"}`)
	provider := mustProvider(t, testConfig(ProviderAliyun, server.URL))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider.Send(ctx, Message{To: "+8613800138000", Scene: "register", Code: "1234"}); err == nil {
		t.Fatal("canceled context must abort before the request")
	}
	if *calls != 0 {
		t.Fatalf("upstream calls = %d, want 0", *calls)
	}
}

func TestNetworkFailureIsRetryable(t *testing.T) {
	// 端口 1 不会有监听者，用来固定复现连接失败。
	provider := mustProvider(t, testConfig(ProviderAliyun, "http://127.0.0.1:1"))
	_, err := provider.Send(context.Background(), Message{To: "+8613800138000", Scene: "register", Code: "1234"})
	smsErr, ok := asSMSError(err)
	if !ok {
		t.Fatalf("error = %v (type %T)", err, err)
	}
	if smsErr.Class != ClassRetryable || smsErr.Code != "request_failed" {
		t.Fatalf("class/code = %v/%q", smsErr.Class, smsErr.Code)
	}
	// 上层要靠 errors.Is/As 拿到原始网络错误做告警归因，包装不能把它吞掉。
	if smsErr.Unwrap() == nil {
		t.Fatal("wrapped network error is lost")
	}
}

// TestProviderSurface 锁住管理端与路由层要读的三元信息：provider key、能力位、
// 错误分类文案。一键登录本期不接，NumberAuth 必须一直是 false，接进来时由人显式改。
func TestProviderSurface(t *testing.T) {
	for _, providerKey := range Implemented() {
		server, _, _ := newCaptureServer(t, http.StatusOK, `{"Code":"OK"}`)
		provider := mustProvider(t, testConfig(providerKey, server.URL))
		if provider.Key() != providerKey {
			t.Fatalf("Key() = %q, want %q", provider.Key(), providerKey)
		}
		capabilities := provider.Capabilities()
		if !capabilities.International || !capabilities.DeliveryReport {
			t.Fatalf("%s capabilities = %+v", providerKey, capabilities)
		}
		if capabilities.NumberAuth {
			t.Fatalf("%s must not advertise number authentication before it is integrated", providerKey)
		}
	}
	if len(Implemented()) != 2 {
		t.Fatalf("Implemented() = %v", Implemented())
	}

	classNames := map[Class]string{ClassOK: "ok", ClassPermanent: "permanent", ClassRetryable: "retryable", ClassUnknown: "unknown"}
	for class, want := range classNames {
		if got := class.String(); got != want {
			t.Fatalf("Class(%d).String() = %q, want %q", class, got, want)
		}
	}
	if (&Error{Provider: ProviderAliyun}).IsRetryable() {
		t.Fatal("an OK-classified error must not be retryable")
	}
	// 告警与日志里可能拿到一个 nil 的 *Error（typed-nil），不能 panic。
	if got := (*Error)(nil).Error(); got != "sms: <nil>" {
		t.Fatalf("nil error string = %q", got)
	}
	if (*Error)(nil).Unwrap() != nil {
		t.Fatal("nil error must not unwrap")
	}
}
