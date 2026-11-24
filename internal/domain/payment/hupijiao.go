package payment

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bugpk-api/internal/pkg/httpclient"
	"bugpk-api/internal/pkg/jsonrequest"
)

const (
	ChannelHupijiaoAlipay = "HUPIJIAO_ALIPAY"
	ChannelHupijiaoWechat = "HUPIJIAO_WECHAT"

	defaultHupijiaoCreateURL = "https://api.xunhupay.com/payment/do.html"
	defaultHupijiaoQueryURL  = "https://api.xunhupay.com/payment/query.html"
	maxHupijiaoResponseBytes = 256 * 1024
	maxHupijiaoTitleBytes    = 127

	hupijiaoQRProbeTimeout = 3 * time.Second
	maxHupijiaoQRProbeHops = 3
	// bp_payment_order.qr_code 是 varchar(1024)：超长的解码结果一律丢弃。
	maxHupijiaoQRPayloadBytes = 1024
)

// HupijiaoConfig is the runtime projection of one Hupijiao channel. Alipay
// and WeChat deliberately use separate rows/configurations because Hupijiao
// issues channel-specific appid/appsecret pairs.
type HupijiaoConfig struct {
	Enabled     bool
	Method      string
	AppID       string
	AppSecret   string
	CreateURL   string
	QueryURL    string
	NotifyURL   string
	ReturnURL   string
	CallbackURL string
	WAPURL      string
	WAPName     string
	HTTPClient  *http.Client
}

type HupijiaoClient struct {
	cfg        HupijiaoConfig
	httpClient *http.Client
}

// NewHupijiaoClient validates a channel configuration and creates a pooled
// outbound client. Tests and local contract probes may inject HTTPClient.
func NewHupijiaoClient(cfg HupijiaoConfig) (*HupijiaoClient, error) {
	cfg.Method = strings.ToUpper(strings.TrimSpace(cfg.Method))
	if cfg.Method != "ALIPAY" && cfg.Method != "WECHAT" {
		return nil, errors.New("hupijiao method must be ALIPAY or WECHAT")
	}
	cfg.AppID = strings.TrimSpace(cfg.AppID)
	cfg.AppSecret = strings.TrimSpace(cfg.AppSecret)
	if cfg.AppID == "" || cfg.AppSecret == "" {
		return nil, errors.New("hupijiao appid and appsecret are required")
	}
	if cfg.CreateURL == "" {
		cfg.CreateURL = defaultHupijiaoCreateURL
	}
	if cfg.QueryURL == "" {
		cfg.QueryURL = defaultHupijiaoQueryURL
	}
	if err := validateHupijiaoEndpoint(cfg.CreateURL); err != nil {
		return nil, fmt.Errorf("invalid hupijiao create URL: %w", err)
	}
	if err := validateHupijiaoEndpoint(cfg.QueryURL); err != nil {
		return nil, fmt.Errorf("invalid hupijiao query URL: %w", err)
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = httpclient.Outbound(10 * time.Second)
	}
	return &HupijiaoClient{cfg: cfg, httpClient: cfg.HTTPClient}, nil
}

// HupijiaoSign implements the provider's legacy protocol exactly: sort
// parameter names by byte-wise ASCII order, omit hash and empty values, join
// with &, append the secret without a separator, then calculate lowercase MD5.
func HupijiaoSign(params map[string]string, secret string) string {
	keys := make([]string, 0, len(params))
	for key, value := range params {
		if key == "hash" || value == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, key := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(params[key])
	}
	b.WriteString(secret)
	sum := md5.Sum([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// Create creates a Hupijiao order and returns either a QR code URL or a
// mobile redirect URL. The provider accepts JSON POST for this endpoint.
func (c *HupijiaoClient) Create(ctx context.Context, order PaymentOrder) (PayCreateResult, error) {
	if c == nil {
		return PayCreateResult{}, errors.New("hupijiao client is nil")
	}
	if strings.TrimSpace(order.OrderNo) == "" {
		return PayCreateResult{}, errors.New("hupijiao order number is required")
	}
	amount, err := normalizeHupijiaoAmount(order.PayAmount)
	if err != nil {
		return PayCreateResult{}, err
	}
	title := normalizeHupijiaoTitle(order.Subject)
	if title == "" {
		return PayCreateResult{}, errors.New("hupijiao order title is required")
	}
	params := map[string]string{
		"version":        "1.1",
		"appid":          c.cfg.AppID,
		"trade_order_id": order.OrderNo,
		"total_fee":      amount,
		"title":          title,
		"time":           strconv.FormatInt(time.Now().Unix(), 10),
		"notify_url":     strings.TrimSpace(c.cfg.NotifyURL),
		"return_url":     strings.TrimSpace(c.cfg.ReturnURL),
		"callback_url":   strings.TrimSpace(c.cfg.CallbackURL),
		"nonce_str":      newHupijiaoNonce(),
	}
	if c.cfg.Method == "WECHAT" {
		tradeType := strings.ToUpper(strings.TrimSpace(order.TradeType))
		switch tradeType {
		case TradeTypeH5:
			params["type"] = "WAP"
			params["wap_url"] = strings.TrimSpace(c.cfg.WAPURL)
			params["wap_name"] = strings.TrimSpace(c.cfg.WAPName)
		case TradeTypeJSAPI:
			params["type"] = "JSAPI"
			params["openid"] = strings.TrimSpace(order.BuyerID)
		case "", TradeTypeNative:
			// No type requests the provider's PC/QR payment flow.
		default:
			return PayCreateResult{}, fmt.Errorf("unsupported hupijiao wechat trade type %q", tradeType)
		}
	}
	params["hash"] = HupijiaoSign(params, c.cfg.AppSecret)
	body, err := c.postJSON(ctx, c.cfg.CreateURL, params)
	if err != nil {
		return PayCreateResult{}, err
	}
	fields, err := decodeHupijiaoObject(body)
	if err != nil {
		return PayCreateResult{}, err
	}
	if err := verifyHupijiaoResponse(fields, c.cfg.AppSecret, nil); err != nil {
		return PayCreateResult{}, err
	}
	if code := scalarString(fields["errcode"]); code != "0" {
		return PayCreateResult{}, fmt.Errorf("hupijiao create failed: %s", scalarString(fields["errmsg"]))
	}
	result := PayCreateResult{
		CodeURL: scalarString(fields["url_qrcode"]),
		H5URL:   scalarString(fields["url"]),
	}
	if native, ok := c.resolveQRNativePayload(ctx, result.CodeURL); ok {
		result.CodeURL = native
	}
	if result.CodeURL == "" && result.H5URL == "" {
		return PayCreateResult{}, errors.New("hupijiao create response has no payment URL")
	}
	return result, nil
}

// Query queries a provider order by the merchant order number.
func (c *HupijiaoClient) Query(ctx context.Context, orderNo string) (PayTradeStatus, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return PayTradeStatus{}, errors.New("hupijiao order number is required")
	}
	params := map[string]string{
		"appid":           c.cfg.AppID,
		"out_trade_order": orderNo,
		"time":            strconv.FormatInt(time.Now().Unix(), 10),
		"nonce_str":       newHupijiaoNonce(),
	}
	params["hash"] = HupijiaoSign(params, c.cfg.AppSecret)
	body, err := c.postJSON(ctx, c.cfg.QueryURL, params)
	if err != nil {
		return PayTradeStatus{}, err
	}
	fields, err := decodeHupijiaoObject(body)
	if err != nil {
		return PayTradeStatus{}, err
	}
	data, _ := objectFields(fields["data"])
	if err := verifyHupijiaoResponse(fields, c.cfg.AppSecret, data); err != nil {
		return PayTradeStatus{}, err
	}
	if code := scalarString(fields["errcode"]); code != "0" {
		msg := scalarString(fields["errmsg"])
		// 虎皮椒没有稳定的错误码表，只能按文案识别「查无此单」，
		// 其余 errcode 仍按渠道故障处理。
		if isPaymentTradeNotFoundSignal(msg) {
			return PayTradeStatus{OutTradeNo: orderNo, TradeState: paymentTradeNotFoundState}, nil
		}
		return PayTradeStatus{}, fmt.Errorf("hupijiao query failed: %s", msg)
	}
	status := strings.ToUpper(strings.TrimSpace(scalarString(data["status"])))
	if status == "" {
		return PayTradeStatus{}, errors.New("hupijiao query response has no status")
	}
	tradeNo := scalarString(data["transaction_id"])
	if tradeNo == "" {
		tradeNo = scalarString(data["open_order_id"])
	}
	// 查单响应金额字段为 total_amount（实测 2026-11-04），旧文档字段
	// total_fee 仅在下单请求里使用；两者都兼容防止 provider 变体。
	total := scalarString(data["total_amount"])
	if total == "" {
		total = scalarString(data["total_fee"])
	}
	return PayTradeStatus{
		OutTradeNo: orderNo,
		TradeNo:    tradeNo,
		TradeState: status,
		Total:      total,
		Success:    status == "OD",
	}, nil
}

// HupijiaoNotify is the verified form callback payload.
type HupijiaoNotify struct {
	AppID       string
	OutTradeNo  string
	Total       string
	TradeNo     string
	OpenOrderID string
	OrderTitle  string
	Status      string
	Attach      string
	NotifyID    string
}

// VerifyNotify checks the provider form callback signature and required
// merchant identity fields. Settlement-specific amount checks remain in the
// service because they require loading the local order.
func (c *HupijiaoClient) VerifyNotify(values map[string]string) (HupijiaoNotify, error) {
	if c == nil {
		return HupijiaoNotify{}, errors.New("hupijiao client is nil")
	}
	if strings.TrimSpace(values["hash"]) == "" {
		return HupijiaoNotify{}, errors.New("missing hupijiao hash")
	}
	if values["appid"] != c.cfg.AppID {
		return HupijiaoNotify{}, errors.New("hupijiao appid mismatch")
	}
	if strings.TrimSpace(values["trade_order_id"]) == "" || strings.TrimSpace(values["total_fee"]) == "" {
		return HupijiaoNotify{}, errors.New("hupijiao notify is missing order or amount")
	}
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(values["hash"])), []byte(HupijiaoSign(values, c.cfg.AppSecret))) != 1 {
		return HupijiaoNotify{}, errors.New("invalid hupijiao notify signature")
	}
	status := strings.ToUpper(strings.TrimSpace(values["status"]))
	if status == "" {
		return HupijiaoNotify{}, errors.New("hupijiao notify status is missing")
	}
	return HupijiaoNotify{
		AppID:       values["appid"],
		OutTradeNo:  values["trade_order_id"],
		Total:       values["total_fee"],
		TradeNo:     values["transaction_id"],
		OpenOrderID: values["open_order_id"],
		OrderTitle:  values["order_title"],
		Status:      status,
		Attach:      values["attach"],
		NotifyID:    values["open_order_id"],
	}, nil
}

// resolveQRNativePayload 把虎皮椒的 url_qrcode 还原成真正的支付串。
//
// url_qrcode 指向的是「显示二维码的页面」：微信扫它只会再看到一张码，
// 用户必须二次扫码才能付款（2026-11-05 生产实测）。该页面用 302 把
// 真实内容放在 Location 的 data 参数里（base64），解出来形如
// weixin://wxpay/bizpayurl?pr=...，扫码即直接进入支付确认页。
// 解析失败时保留原 URL：最差退化成修复前的行为，不影响下单。
func (c *HupijiaoClient) resolveQRNativePayload(ctx context.Context, qrURL string) (string, bool) {
	prefixes := hupijiaoQRNativePrefixes[c.cfg.Method]
	if len(prefixes) == 0 {
		return "", false
	}
	target, ok := hupijiaoQRProbeTarget(qrURL, c.cfg.CreateURL)
	if !ok {
		return "", false
	}
	probe := *c.httpClient
	probe.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	probe.Timeout = hupijiaoQRProbeTimeout
	ctx, cancel := context.WithTimeout(ctx, hupijiaoQRProbeTimeout)
	defer cancel()
	for hop := 0; hop < maxHupijiaoQRProbeHops; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return "", false
		}
		resp, err := probe.Do(req)
		if err != nil {
			return "", false
		}
		location, ok := hupijiaoQRProbeTarget(resp.Header.Get("Location"), c.cfg.CreateURL)
		_ = resp.Body.Close()
		if !ok {
			return "", false
		}
		if payload, ok := hupijiaoQRPayloadFromLocation(location, prefixes); ok {
			return payload, true
		}
		target = location
	}
	return "", false
}

// hupijiaoQRNativePrefixes 限定每个支付方式可被扫码器直接识别的内容前缀，
// 只接受这些前缀，避免把 provider 改版后返回的普通页面地址当成支付串。
var hupijiaoQRNativePrefixes = map[string][]string{
	"WECHAT": {"weixin://"},
	"ALIPAY": {"alipays://", "https://qr.alipay.com"},
}

// hupijiaoQRProbeTarget 只允许回访下单接口同源的地址，防止响应里的
// url_qrcode 把服务端引出站外。
func hupijiaoQRProbeTarget(raw, createURL string) (string, bool) {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || target.Host == "" || target.User != nil || !target.IsAbs() {
		return "", false
	}
	if err := httpclient.ValidateURL(target.String()); err != nil {
		return "", false
	}
	base, err := url.Parse(strings.TrimSpace(createURL))
	if err != nil || !strings.EqualFold(base.Host, target.Host) {
		return "", false
	}
	return target.String(), true
}

func hupijiaoQRPayloadFromLocation(raw string, prefixes []string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	data := hupijiaoQRDataParam(parsed.RawQuery)
	if data == "" {
		return "", false
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		decoded, err := encoding.DecodeString(data)
		if err != nil || len(decoded) == 0 || len(decoded) > maxHupijiaoQRPayloadBytes {
			continue
		}
		payload := strings.TrimSpace(string(decoded))
		if !utf8.ValidString(payload) || strings.ContainsAny(payload, "\r\n\x00") {
			continue
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(payload, prefix) {
				return payload, true
			}
		}
	}
	return "", false
}

// hupijiaoQRDataParam 自己切 query：url.QueryUnescape 会把 base64 里的
// '+' 当成空格，直接用它解出来的码必定校验失败。
func hupijiaoQRDataParam(rawQuery string) string {
	for _, pair := range strings.Split(rawQuery, "&") {
		name, value, found := strings.Cut(pair, "=")
		if !found || name != "data" || value == "" {
			continue
		}
		decoded, err := url.PathUnescape(value)
		if err != nil {
			return ""
		}
		return strings.TrimSpace(decoded)
	}
	return ""
}

func (c *HupijiaoClient) postJSON(ctx context.Context, endpoint string, params map[string]string) ([]byte, error) {
	payload, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := httpclient.ReadBody(resp.Body, maxHupijiaoResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("read hupijiao response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("hupijiao HTTP %d", resp.StatusCode)
	}
	return body, nil
}

func decodeHupijiaoObject(body []byte) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := jsonrequest.UnmarshalUnambiguous(body, &fields, jsonrequest.DefaultMaxDepth); err != nil {
		return nil, fmt.Errorf("decode hupijiao response: %w", err)
	}
	if len(fields) == 0 {
		return nil, errors.New("empty hupijiao response")
	}
	return fields, nil
}

// verifyHupijiaoResponse accepts both the flat create response and the query
// response whose business fields are nested under data. The provider has
// shipped both variants over time, so verify the canonical set emitted by the
// response while ignoring the hash itself.
//
// 2026-11-04 实测：查单接口（payment/query.html）的成功响应只含
// errcode/errmsg/data，不携带 hash 字段，因此不能把「缺 hash」当错误——
// 只在 provider 返回了 hash 时才做签名校验；防篡改依赖 HTTPS 传输 +
// 结算前的订单号/金额二次比对（service 层 settle 检查）。
func verifyHupijiaoResponse(fields map[string]json.RawMessage, secret string, data map[string]json.RawMessage) error {
	want := scalarString(fields["hash"])
	if want == "" {
		// 查单响应无 hash：跳过签名校验，交给结算前的订单号/金额检查兜底。
		return nil
	}
	flat := make(map[string]string, len(fields)+len(data))
	for key, value := range fields {
		if key == "hash" || key == "data" {
			continue
		}
		flat[key] = scalarString(value)
	}
	for key, value := range data {
		if key == "hash" {
			continue
		}
		flat[key] = scalarString(value)
	}
	got := HupijiaoSign(flat, secret)
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(strings.TrimSpace(want))), []byte(got)) != 1 {
		return errors.New("invalid hupijiao response signature")
	}
	return nil
}

func objectFields(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return map[string]json.RawMessage{}, false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return map[string]json.RawMessage{}, false
	}
	return fields, true
}

func scalarString(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return ""
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return strings.TrimSpace(string(raw))
}

func normalizeHupijiaoAmount(value string) (string, error) {
	normalized, err := normalizeMoney(value)
	if err != nil {
		return "", err
	}
	// Hupijiao accepts two decimal places; round using the already validated
	// four-decimal project amount and reject values that would round to zero.
	parts := strings.SplitN(normalized, ".", 2)
	frac := "0000"
	if len(parts) == 2 {
		frac = parts[1]
	}
	if len(frac) < 4 {
		frac += strings.Repeat("0", 4-len(frac))
	}
	if frac[2] >= '5' {
		// Delegate carry handling to a decimal-friendly existing helper.
		return formatHupijiaoCents(normalized)
	}
	result := parts[0] + "." + frac[:2]
	if result == "0.00" {
		return "", errors.New("hupijiao amount must be greater than zero")
	}
	return result, nil
}

func formatHupijiaoCents(value string) (string, error) {
	// Avoid floating point conversion: normalizeMoney bounds the input, and
	// use integer decimal text for the two-decimal carry.
	parts := strings.SplitN(value, ".", 2)
	whole := parts[0]
	frac := "0000"
	if len(parts) == 2 {
		frac = parts[1]
	}
	if len(frac) < 4 {
		frac += strings.Repeat("0", 4-len(frac))
	}
	cents, err := strconv.Atoi(frac[:2])
	if err != nil {
		return "", err
	}
	cents++
	if cents == 100 {
		whole = incrementDecimalInteger(whole)
		cents = 0
	}
	result := fmt.Sprintf("%s.%02d", whole, cents)
	if result == "0.00" {
		return "", errors.New("hupijiao amount must be greater than zero")
	}
	return result, nil
}

func incrementDecimalInteger(value string) string {
	if value == "" {
		return "1"
	}
	b := []byte(value)
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] < '9' {
			b[i]++
			return string(b)
		}
		b[i] = '0'
	}
	return "1" + string(b)
}

func normalizeHupijiaoTitle(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "%", ""))
	if value == "" {
		return ""
	}
	if !utf8.ValidString(value) {
		return ""
	}
	// 官方限制：标题不超过 127 字节（约 42 个汉字），且不能含表情符号与 %。
	if len(value) <= maxHupijiaoTitleBytes {
		return value
	}
	runes := []rune(value)
	for size := len(runes) - 1; size > 0; size-- {
		candidate := string(runes[:size])
		if len(candidate) <= maxHupijiaoTitleBytes {
			return candidate
		}
	}
	return ""
}

func newHupijiaoNonce() string {
	return strconv.FormatInt(time.Now().UnixNano(), 10)
}

func validateHupijiaoEndpoint(raw string) error {
	if err := httpclient.ValidateURL(raw); err != nil {
		return err
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("hupijiao endpoint must not contain query or fragment")
	}
	return nil
}

func validateOfficialHupijiaoEndpoint(raw string) error {
	if err := validateHupijiaoEndpoint(raw); err != nil {
		return err
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "api.xunhupay.com" && host != "api.dpweixin.com" {
		return errors.New("hupijiao endpoint must use an official provider host")
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return errors.New("hupijiao endpoint must use https")
	}
	return nil
}
