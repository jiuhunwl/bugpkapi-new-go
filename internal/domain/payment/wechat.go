package payment

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/httpclient"
	"bugpk-api/internal/pkg/jsonrequest"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	// 微信支付 v3 API 域名（沙箱与生产共用同一域名，按商户号/证书区分）。
	wechatAPIBaseURL   = "https://api.mch.weixin.qq.com"
	wechatNonceLen     = 16
	wechatTimeFormat   = "2006-01-02T15:04:05+08:00"
	wechatDefaultCur   = "CNY"
	wechatTokenExpire  = 10 * time.Second
	wechatNotifyOK     = `{"code":"SUCCESS","message":"成功"}`
	wechatNotifyFailed = `{"code":"FAIL","message":"失败"}`
)

// WechatClient 微信支付 v3 网关客户端（商户模式 / 服务商模式）。
// 不引入第三方 SDK，签名/解密/验签全部基于标准库实现。
type WechatClient struct {
	mode          string // SANDBOX / PRODUCTION
	mchid         string
	appid         string
	apiv3Key      string
	privateKey    *rsa.PrivateKey
	serialNo      string
	platformKeyID string
	platformPub   *rsa.PublicKey
	notifyURL     string
	httpClient    *http.Client

	// 服务商模式（可选）。
	spMchid  string
	spAppID  string
	subMchid string
	subAppID string

	// 多端 AppID（可选）。
	oaAppID   string
	miniAppID string
	appAppID  string
}

// NewWechatClient 构造微信 v3 客户端。
//   - 校验 APIv3Key 长度必须为 32 字节；
//   - 解析商户私钥 PEM（复用 parsePrivateKey）；
//   - 解析平台证书公钥（若配置）；
//   - HTTP 客户端默认复用 httpclient.Outbound，可注入自定义传输。
func NewWechatClient(cfg WechatConfig) (*WechatClient, error) {
	if strings.TrimSpace(cfg.Mchid) == "" || strings.TrimSpace(cfg.SerialNo) == "" {
		return nil, errors.New("wechat config is incomplete: mchid and serialNo are required")
	}
	if len(strings.TrimSpace(cfg.APIv3Key)) != 32 {
		return nil, apperrors.New(consts.StatusBadRequest, "微信 APIv3Key 长度必须为 32 字节")
	}
	privateKey, err := parsePrivateKey(cfg.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse wechat merchant private key: %w", err)
	}
	if err := validateRSAKeyStrength(privateKey.N.BitLen(), "wechat merchant private key"); err != nil {
		return nil, err
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = httpclient.Outbound(wechatTokenExpire)
	}
	client := &WechatClient{
		mode:          strings.ToUpper(strings.TrimSpace(cfg.Mode)),
		mchid:         strings.TrimSpace(cfg.Mchid),
		appid:         strings.TrimSpace(cfg.AppID),
		apiv3Key:      strings.TrimSpace(cfg.APIv3Key),
		privateKey:    privateKey,
		serialNo:      strings.TrimSpace(cfg.SerialNo),
		platformKeyID: strings.TrimSpace(cfg.PlatformPublicKeyID),
		notifyURL:     strings.TrimSpace(cfg.NotifyURL),
		httpClient:    httpClient,
		spMchid:       strings.TrimSpace(cfg.SpMchid),
		spAppID:       strings.TrimSpace(cfg.SpAppID),
		subMchid:      strings.TrimSpace(cfg.SubMchid),
		subAppID:      strings.TrimSpace(cfg.SubAppID),
		oaAppID:       strings.TrimSpace(cfg.OAAppID),
		miniAppID:     strings.TrimSpace(cfg.MiniAppID),
		appAppID:      strings.TrimSpace(cfg.AppAppID),
	}
	if strings.TrimSpace(cfg.PlatformPublicKey) != "" {
		pub, perr := parsePublicKey(cfg.PlatformPublicKey)
		if perr != nil {
			return nil, fmt.Errorf("parse wechat platform public key: %w", perr)
		}
		if err := validateRSAKeyStrength(pub.N.BitLen(), "wechat platform public key"); err != nil {
			return nil, err
		}
		client.platformPub = pub
	}
	return client, nil
}

// isServiceProvider 是否为服务商模式。
func (c *WechatClient) isServiceProvider() bool {
	return strings.TrimSpace(c.spMchid) != "" && strings.TrimSpace(c.subMchid) != ""
}

// signingMerchantID identifies the owner of the request-signing certificate.
// Partner requests are signed by the service provider, never the sub-merchant.
func (c *WechatClient) signingMerchantID() string {
	if c.isServiceProvider() {
		return c.spMchid
	}
	return c.mchid
}

// buildProbeAuthorization 生成一次最小 v3 签名头，用于校验商户私钥/序列号可用性（不触网）。
func (c *WechatClient) buildProbeAuthorization() (string, error) {
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonce := newWechatNonce()
	_, err := buildAuthorization(http.MethodPost, "/v3/pay/transactions/native",
		`{"out_trade_no":"PROBE"}`, timestamp, nonce, c.signingMerchantID(), c.privateKey, c.serialNo)
	return "", err
}

// wechatAmountBody 微信金额结构（单位为「分」整数）。
type wechatAmountBody struct {
	Total    int64  `json:"total"`
	Currency string `json:"currency"`
}

// wechatTransactionResponse 微信下单响应通用结构。
type wechatTransactionResponse struct {
	CodeURL  string `json:"code_url,omitempty"`
	H5URL    string `json:"h5_url,omitempty"`
	PrepayID string `json:"prepay_id,omitempty"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// Create 按 order.TradeType 路由到对应微信下单接口，返回统一结果。
func (c *WechatClient) Create(ctx context.Context, order PaymentOrder) (PayCreateResult, error) {
	plan, err := c.prepareCreate(order)
	if err != nil {
		return PayCreateResult{}, err
	}
	var response wechatTransactionResponse
	if err := c.postJSON(ctx, http.MethodPost, plan.path, plan.body, &response); err != nil {
		return PayCreateResult{}, err
	}
	result := PayCreateResult{CodeURL: response.CodeURL, H5URL: response.H5URL, PrepayID: response.PrepayID}
	switch plan.tradeType {
	case TradeTypeNative:
		if strings.TrimSpace(result.CodeURL) == "" {
			return PayCreateResult{}, apperrors.New(consts.StatusServiceUnavailable, "微信Native下单未返回code_url")
		}
	case TradeTypeH5:
		if strings.TrimSpace(result.H5URL) == "" {
			return PayCreateResult{}, apperrors.New(consts.StatusServiceUnavailable, "微信H5下单未返回h5_url")
		}
	case TradeTypeJSAPI, TradeTypeMini:
		if strings.TrimSpace(result.PrepayID) == "" {
			return PayCreateResult{}, apperrors.New(consts.StatusServiceUnavailable, "微信下单未返回prepay_id")
		}
		result.PaySign, result.NonceStr, result.Timestamp, err = c.jsapiPaySign(plan.appID, result.PrepayID)
		if err != nil {
			return PayCreateResult{}, err
		}
		data, marshalErr := json.Marshal(map[string]string{"appId": plan.appID, "timeStamp": result.Timestamp, "nonceStr": result.NonceStr, "package": "prepay_id=" + result.PrepayID, "signType": "RSA", "paySign": result.PaySign})
		if marshalErr != nil {
			return PayCreateResult{}, marshalErr
		}
		result.PayParams = string(data)
	case TradeTypeAPP:
		if strings.TrimSpace(result.PrepayID) == "" {
			return PayCreateResult{}, apperrors.New(consts.StatusServiceUnavailable, "微信下单未返回prepay_id")
		}
		result.PayParams, result.NonceStr, result.Timestamp, err = c.appPayParams(plan.appID, result.PrepayID)
		if err != nil {
			return PayCreateResult{}, err
		}
	}
	return result, nil
}

// Query 按商户单号查单（服务商走 partner 路径）。
func (c *WechatClient) Query(ctx context.Context, outTradeNo string) (PayTradeStatus, error) {
	base := "/v3/pay/transactions/out-trade-no/"
	if c.isServiceProvider() {
		base = "/v3/pay/partner/transactions/out-trade-no/"
	}
	path := base + outTradeNo
	query := map[string]string{}
	if c.isServiceProvider() {
		query["sp_mchid"] = c.spMchid
		query["sub_mchid"] = c.subMchid
	} else {
		query["mchid"] = c.mchid
	}
	var resp wechatTransaction
	if err := c.postJSON(ctx, http.MethodGet, path, nil, &resp, query); err != nil {
		if isWechatTradeNotFound(err) {
			return PayTradeStatus{OutTradeNo: outTradeNo, TradeState: paymentTradeNotFoundState}, nil
		}
		return PayTradeStatus{}, err
	}
	if err := c.validateWechatTransaction(resp); err != nil {
		return PayTradeStatus{}, err
	}
	if resp.OutTradeNo != outTradeNo {
		return PayTradeStatus{}, errors.New("wechat query order number mismatch")
	}
	total := fenToAmount(*resp.Amount.Total)
	return PayTradeStatus{
		OutTradeNo: resp.OutTradeNo,
		TradeNo:    resp.TransactionID,
		TradeState: resp.TradeState,
		Total:      total,
		BuyerID:    c.wechatTransactionBuyer(resp),
		Success:    strings.EqualFold(resp.TradeState, "SUCCESS"),
	}, nil
}

// Close 关单（服务商走 partner 路径）。
func (c *WechatClient) Close(ctx context.Context, outTradeNo string) error {
	base := "/v3/pay/transactions/out-trade-no/" + outTradeNo + "/close"
	if c.isServiceProvider() {
		base = "/v3/pay/partner/transactions/out-trade-no/" + outTradeNo + "/close"
	}
	body := map[string]string{}
	if c.isServiceProvider() {
		body["sp_mchid"] = c.spMchid
		body["sub_mchid"] = c.subMchid
	} else {
		body["mchid"] = c.mchid
	}
	return c.postJSON(ctx, http.MethodPost, base, body, nil)
}

// VerifyNotify 校验并解密微信回调：
//  1. 用 Wechatpay-Signature + Wechatpay-Timestamp + Wechatpay-Nonce + 平台证书公钥 验签；
//  2. 用 APIv3Key 对 resource.ciphertext 做 AES-256-GCM 解密，得到明文 JSON；
//  3. 返回标准化 PayNotify。
func (c *WechatClient) VerifyNotify(ctx context.Context, body []byte, headers map[string]string) (*PayNotify, error) {
	header := make(http.Header, len(headers))
	for name, value := range headers {
		header.Add(name, value)
	}
	if err := c.verifyPlatformMessage(ctx, header, body); err != nil {
		return nil, err
	}

	var envelope struct {
		ID           string `json:"id"`
		CreateTime   string `json:"create_time"`
		ResourceType string `json:"resource_type"`
		EventType    string `json:"event_type"`
		Summary      string `json:"summary"`
		Resource     struct {
			Algorithm      string `json:"algorithm"`
			Ciphertext     string `json:"ciphertext"`
			Nonce          string `json:"nonce"`
			AssociatedData string `json:"associated_data"`
		} `json:"resource"`
	}
	if err := jsonrequest.UnmarshalUnambiguous(body, &envelope, jsonrequest.DefaultMaxDepth); err != nil {
		return nil, apperrors.Wrap(consts.StatusBadRequest, "微信回调报文解析失败", err)
	}
	if envelope.Resource.Algorithm != "AEAD_AES_256_GCM" || envelope.EventType != "TRANSACTION.SUCCESS" || envelope.ResourceType != "encrypt-resource" || strings.TrimSpace(envelope.Resource.Ciphertext) == "" {
		return nil, apperrors.New(consts.StatusBadRequest, "微信回调资源为空")
	}
	rawCipher, err := base64.StdEncoding.DecodeString(strings.TrimSpace(envelope.Resource.Ciphertext))
	if err != nil {
		return nil, apperrors.New(consts.StatusBadRequest, "微信回调密文格式不合法")
	}
	plain, err := decryptResource(string(rawCipher), envelope.Resource.Nonce, envelope.Resource.AssociatedData, c.apiv3Key)
	if err != nil {
		return nil, err
	}
	var decrypted wechatTransaction
	if err := jsonrequest.UnmarshalUnambiguous(plain, &decrypted, jsonrequest.DefaultMaxDepth); err != nil {
		return nil, apperrors.Wrap(consts.StatusBadRequest, "微信回调明文解析失败", err)
	}
	if err := c.validateWechatTransaction(decrypted); err != nil {
		return nil, err
	}
	if decrypted.TradeState != "SUCCESS" {
		return nil, errors.New("wechat notification event and transaction state disagree")
	}
	return &PayNotify{
		NotifyID:   envelope.ID,
		OutTradeNo: decrypted.OutTradeNo,
		TradeNo:    decrypted.TransactionID,
		TradeState: decrypted.TradeState,
		Total:      fenToAmount(*decrypted.Amount.Total),
		BuyerID:    c.wechatTransactionBuyer(decrypted),
		Success:    strings.EqualFold(decrypted.TradeState, "SUCCESS"),
	}, nil
}

// fenToAmount 将「分」整数转换为 4 位小数字符串。
func fenToAmount(fen int64) string {
	if fen < 0 {
		fen = 0
	}
	rat := new(big.Rat).SetFrac(big.NewInt(fen), big.NewInt(100))
	return rat.FloatString(4)
}

// jsapiPaySign 生成 JSAPI/小程序的二次签名与调起参数。
func (c *WechatClient) jsapiPaySign(appid, prepayID string) (paySign, nonceStr, timestamp string, err error) {
	nonceStr = newWechatNonce()
	timestamp = fmt.Sprintf("%d", time.Now().Unix())
	message := strings.Join([]string{appid, timestamp, nonceStr, "prepay_id=" + prepayID, ""}, "\n")
	paySign, err = signWechatPay(message, c.privateKey)
	if err != nil {
		return "", "", "", err
	}
	return paySign, nonceStr, timestamp, nil
}

// appPayParams 生成 APP 调起参数字典 JSON 及其二次签名。
func (c *WechatClient) appPayParams(appid, prepayID string) (payParams, nonceStr, timestamp string, err error) {
	nonceStr = newWechatNonce()
	timestamp = fmt.Sprintf("%d", time.Now().Unix())
	partnerID := c.mchid
	if c.isServiceProvider() {
		partnerID = c.subMchid
	}
	message := strings.Join([]string{appid, timestamp, nonceStr, prepayID, ""}, "\n")
	sign, err := signWechatPay(message, c.privateKey)
	if err != nil {
		return "", "", "", err
	}
	params := map[string]string{
		"appid":     appid,
		"partnerid": partnerID,
		"prepayid":  prepayID,
		"package":   "Sign=WXPay",
		"noncestr":  nonceStr,
		"timestamp": timestamp,
		"sign":      sign,
	}
	data, err := json.Marshal(params)
	if err != nil {
		return "", "", "", err
	}
	return string(data), nonceStr, timestamp, nil
}

// postJSON 统一发送微信 v3 请求：构造 Authorization 头、序列化 JSON、发送、解析响应。
// query 仅在 GET 请求时作为查询参数拼接。
func (c *WechatClient) postJSON(ctx context.Context, method, path string, bodyObj any, out any, query ...map[string]string) error {
	if err := c.requirePlatformVerifier(); err != nil {
		return err
	}
	var bodyBytes []byte
	if bodyObj != nil {
		data, err := json.Marshal(bodyObj)
		if err != nil {
			return fmt.Errorf("marshal wechat request: %w", err)
		}
		bodyBytes = data
	}
	requestPath := path
	if len(query) > 0 && len(query[0]) > 0 {
		parts := make([]string, 0, len(query[0]))
		for k, v := range query[0] {
			parts = append(parts, k+"="+v)
		}
		if len(parts) > 0 {
			requestPath += "?" + strings.Join(parts, "&")
		}
	}
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonce := newWechatNonce()
	auth, err := buildAuthorization(method, requestPath, string(bodyBytes), timestamp, nonce, c.signingMerchantID(), c.privateKey, c.serialNo)
	if err != nil {
		return err
	}
	fullURL := wechatAPIBaseURL + requestPath
	req, err := http.NewRequestWithContext(ctx, method, fullURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", auth)
	// Ask the platform to use the verification identity pinned by this client.
	req.Header.Set("Wechatpay-Serial", c.platformKeyID)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "bugpk-api/1.0")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, err := httpclient.ReadBody(resp.Body, httpclient.MaxProviderResponseBytes)
	if err != nil {
		return fmt.Errorf("read wechat response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return parseWechatError(respBody)
	}
	if err := c.verifyPlatformMessage(ctx, resp.Header, respBody); err != nil {
		return err
	}
	if out != nil && len(respBody) > 0 {
		if err := jsonrequest.UnmarshalUnambiguous(respBody, out, jsonrequest.DefaultMaxDepth); err != nil {
			return fmt.Errorf("decode wechat response: %w", err)
		}
	}
	return nil
}

// wechatAPIError 承载微信 V3 接口的机器可读错误码与文案。
type wechatAPIError struct {
	code    string
	message string
}

func (e *wechatAPIError) Error() string { return e.message }

func parseWechatError(body []byte) error {
	var payload struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	_ = json.Unmarshal(body, &payload)
	msg := payload.Message
	if msg == "" {
		msg = payload.Detail
	}
	if msg == "" {
		msg = string(body)
	}
	// 保留机器可读错误码：上层要区分「查无此单」这类业务结论和真正的渠道故障，
	// 只看中文 message 无法稳定判断。
	return apperrors.Wrap(consts.StatusServiceUnavailable, "微信支付接口调用失败: "+msg, &wechatAPIError{code: payload.Code, message: msg})
}

// isWechatTradeNotFound 认微信查单接口的 RESOURCE_NOT_EXIST：这笔订单在微信侧不存在，
// 属于「尚未在渠道建单」，不是服务故障。
func isWechatTradeNotFound(err error) bool {
	var apiErr *wechatAPIError
	return errors.As(err, &apiErr) && strings.EqualFold(strings.TrimSpace(apiErr.code), "RESOURCE_NOT_EXIST")
}

// newWechatNonce 生成微信请求随机串（16 字节十六进制）。
func newWechatNonce() string {
	data := make([]byte, wechatNonceLen)
	if _, err := io.ReadFull(rand.Reader, data); err != nil {
		// 退化到时间+随机，避免阻塞（极少触发）。
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	return hex.EncodeToString(data)
}
