package payment

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"bugpk-api/internal/pkg/businessclock"
	"bugpk-api/internal/pkg/httpclient"
	"bugpk-api/internal/pkg/jsonrequest"

	htmlnode "golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

const (
	defaultSandboxGateway    = "https://openapi-sandbox.dl.alipaydev.com/gateway.do"
	defaultProductionGateway = "https://openapi.alipay.com/gateway.do"
	minimumAlipayRSAKeyBits  = 2048
)

type AlipayClient struct {
	cfg        AlipayConfig
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
	httpClient *http.Client
	signer     rsaSigner
}

type rsaSigner func(io.Reader, *rsa.PrivateKey, crypto.Hash, []byte) ([]byte, error)

func NewAlipayClient(cfg AlipayConfig) (*AlipayClient, error) {
	if strings.TrimSpace(cfg.AppID) == "" || strings.TrimSpace(cfg.GatewayURL) == "" {
		return nil, errors.New("alipay config is incomplete")
	}
	privateKey, err := parsePrivateKey(cfg.AppPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("parse app private key: %w", err)
	}
	if err := validateRSAKeyStrength(privateKey.N.BitLen(), "app private key"); err != nil {
		return nil, err
	}
	publicKey, err := parsePublicKey(cfg.AlipayPublicKey)
	if err != nil {
		return nil, fmt.Errorf("parse alipay public key: %w", err)
	}
	if err := validateRSAKeyStrength(publicKey.N.BitLen(), "alipay public key"); err != nil {
		return nil, err
	}
	return &AlipayClient{
		cfg:        cfg,
		privateKey: privateKey,
		publicKey:  publicKey,
		httpClient: httpclient.Outbound(10 * time.Second),
		signer:     rsa.SignPKCS1v15,
	}, nil
}

func ValidateAlipayKeyMaterial(privateText, publicText string) error {
	privateKey, err := parsePrivateKey(privateText)
	if err != nil {
		return fmt.Errorf("parse app private key: %w", err)
	}
	if err := validateRSAKeyStrength(privateKey.N.BitLen(), "app private key"); err != nil {
		return err
	}
	publicKey, err := parsePublicKey(publicText)
	if err != nil {
		return fmt.Errorf("parse alipay public key: %w", err)
	}
	return validateRSAKeyStrength(publicKey.N.BitLen(), "alipay public key")
}

func validateRSAKeyStrength(bits int, name string) error {
	if bits < minimumAlipayRSAKeyBits {
		return fmt.Errorf("%s must use RSA-%d or stronger", name, minimumAlipayRSAKeyBits)
	}
	return nil
}

func (c *AlipayClient) Create(ctx context.Context, order PaymentOrder) (AlipayCreateResult, error) {
	switch strings.ToUpper(order.Product) {
	case "PAGE":
		return c.form("alipay.trade.page.pay", order)
	case "WAP":
		return c.form("alipay.trade.wap.pay", order)
	case "FACE":
		qr, err := c.precreate(ctx, order)
		if err != nil {
			return AlipayCreateResult{}, err
		}
		return AlipayCreateResult{QRCode: qr}, nil
	default:
		return AlipayCreateResult{}, errors.New("unsupported alipay product")
	}
}

func (c *AlipayClient) Query(ctx context.Context, orderNo string) (AlipayTradeStatus, error) {
	biz, _ := json.Marshal(map[string]string{"out_trade_no": orderNo})
	params, err := c.commonParams("alipay.trade.query", string(biz), false, false)
	if err != nil {
		return AlipayTradeStatus{}, err
	}
	body, err := c.post(ctx, params)
	if err != nil {
		return AlipayTradeStatus{}, err
	}
	if err := c.verifyOpenAPIResponse(body, "alipay_trade_query_response"); err != nil {
		return AlipayTradeStatus{}, err
	}
	return parseAlipayQueryResponse(body)
}

func parseAlipayQueryResponse(body []byte) (AlipayTradeStatus, error) {
	var payload struct {
		Response struct {
			Code        string `json:"code"`
			Msg         string `json:"msg"`
			SubCode     string `json:"sub_code"`
			SubMsg      string `json:"sub_msg"`
			OutTradeNo  string `json:"out_trade_no"`
			SellerID    string `json:"seller_id"`
			TotalAmount string `json:"total_amount"`
			TradeNo     string `json:"trade_no"`
			BuyerUserID string `json:"buyer_user_id"`
			TradeStatus string `json:"trade_status"`
		} `json:"alipay_trade_query_response"`
	}
	if err := jsonrequest.UnmarshalUnambiguous(body, &payload, jsonrequest.DefaultMaxDepth); err != nil {
		return AlipayTradeStatus{}, err
	}
	if payload.Response.Code != "10000" {
		// 查无此单是「这笔订单从未在支付宝建单」的正常结论，不是服务故障：
		// 返回未支付标记，让同步链路按订单当前状态收尾。
		if isAlipayTradeNotFound(payload.Response.SubCode, payload.Response.SubMsg) {
			return AlipayTradeStatus{TradeStatus: paymentTradeNotFoundState}, nil
		}
		msg := payload.Response.SubMsg
		if msg == "" {
			msg = payload.Response.Msg
		}
		return AlipayTradeStatus{}, fmt.Errorf("alipay query failed: %s", msg)
	}
	return AlipayTradeStatus{
		BuyerID:     payload.Response.BuyerUserID,
		OutTradeNo:  payload.Response.OutTradeNo,
		SellerID:    payload.Response.SellerID,
		TotalAmount: payload.Response.TotalAmount,
		TradeNo:     payload.Response.TradeNo,
		TradeStatus: payload.Response.TradeStatus,
		Success: payload.Response.TradeStatus == "TRADE_SUCCESS" ||
			payload.Response.TradeStatus == "TRADE_FINISHED",
	}, nil
}

func (c *AlipayClient) VerifyNotify(values url.Values) error {
	sign := values.Get("sign")
	if strings.TrimSpace(sign) == "" {
		return errors.New("missing alipay sign")
	}
	return c.verify(values, sign)
}

func (c *AlipayClient) form(method string, order PaymentOrder) (AlipayCreateResult, error) {
	biz := map[string]any{
		"out_trade_no":    order.OrderNo,
		"total_amount":    order.PayAmount,
		"subject":         order.Subject,
		"product_code":    productCode(order.Product),
		"timeout_express": "30m",
	}
	bizContent, _ := json.Marshal(biz)
	params, err := c.commonParams(method, string(bizContent), true, true)
	if err != nil {
		return AlipayCreateResult{}, err
	}
	endpoint := html.EscapeString(c.cfg.GatewayURL)
	var b strings.Builder
	b.WriteString(`<form id="alipay_submit" name="alipay_submit" action="`)
	b.WriteString(endpoint)
	b.WriteString(`" method="post">`)
	keys := sortedKeys(params)
	for _, key := range keys {
		b.WriteString(`<input type="hidden" name="`)
		b.WriteString(html.EscapeString(key))
		b.WriteString(`" value="`)
		b.WriteString(html.EscapeString(params.Get(key)))
		b.WriteString(`"/>`)
	}
	b.WriteString(`</form><script>document.forms['alipay_submit'].submit();</script>`)
	formParams := make(map[string]string, len(params))
	for _, key := range keys {
		formParams[key] = params.Get(key)
	}
	return AlipayCreateResult{
		FormActionURL: c.cfg.GatewayURL,
		FormParams:    formParams,
		PayForm:       b.String(),
	}, nil
}

func paymentOrderForResponse(order PaymentOrder) PaymentOrder {
	if order.PayForm != "" && (order.FormActionURL == "" || len(order.FormParams) == 0) {
		if action, params, ok := parseStoredAlipayForm(order.PayForm); ok {
			order.FormActionURL = action
			order.FormParams = params
		}
	}
	order.PayForm = ""
	return order
}

func parseStoredAlipayForm(value string) (string, map[string]string, bool) {
	if value == "" || len(value) > 256*1024 {
		return "", nil, false
	}
	contextNode := &htmlnode.Node{Type: htmlnode.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := htmlnode.ParseFragment(strings.NewReader(value), contextNode)
	if err != nil {
		return "", nil, false
	}
	var forms []*htmlnode.Node
	for _, node := range nodes {
		collectAlipayForms(node, &forms)
	}
	if len(forms) != 1 || !strings.EqualFold(attrValue(forms[0], "method"), http.MethodPost) {
		return "", nil, false
	}
	action := strings.TrimSpace(attrValue(forms[0], "action"))
	if action != defaultSandboxGateway && action != defaultProductionGateway {
		return "", nil, false
	}
	params := map[string]string{}
	for child := forms[0].FirstChild; child != nil; child = child.NextSibling {
		if child.Type != htmlnode.ElementNode || child.DataAtom != atom.Input {
			continue
		}
		if !strings.EqualFold(attrValue(child, "type"), "hidden") {
			return "", nil, false
		}
		name := strings.TrimSpace(attrValue(child, "name"))
		if name == "" || len(name) > 64 || len(params) >= 64 {
			return "", nil, false
		}
		if _, exists := params[name]; exists {
			return "", nil, false
		}
		fieldValue := attrValue(child, "value")
		if len(fieldValue) > 32*1024 {
			return "", nil, false
		}
		params[name] = fieldValue
	}
	return action, params, len(params) > 0
}

func collectAlipayForms(node *htmlnode.Node, forms *[]*htmlnode.Node) {
	if node.Type == htmlnode.ElementNode && node.DataAtom == atom.Form {
		*forms = append(*forms, node)
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		collectAlipayForms(child, forms)
	}
}

func attrValue(node *htmlnode.Node, name string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, name) {
			return attr.Val
		}
	}
	return ""
}

func (c *AlipayClient) precreate(ctx context.Context, order PaymentOrder) (string, error) {
	biz := map[string]any{
		"out_trade_no":    order.OrderNo,
		"total_amount":    order.PayAmount,
		"subject":         order.Subject,
		"timeout_express": "30m",
	}
	bizContent, _ := json.Marshal(biz)
	params, err := c.commonParams("alipay.trade.precreate", string(bizContent), true, false)
	if err != nil {
		return "", err
	}
	body, err := c.post(ctx, params)
	if err != nil {
		return "", err
	}
	if err := c.verifyOpenAPIResponse(body, "alipay_trade_precreate_response"); err != nil {
		return "", err
	}
	var payload struct {
		Response struct {
			Code   string `json:"code"`
			Msg    string `json:"msg"`
			SubMsg string `json:"sub_msg"`
			QRCode string `json:"qr_code"`
		} `json:"alipay_trade_precreate_response"`
	}
	if err := jsonrequest.UnmarshalUnambiguous(body, &payload, jsonrequest.DefaultMaxDepth); err != nil {
		return "", err
	}
	if payload.Response.Code != "10000" {
		msg := payload.Response.SubMsg
		if msg == "" {
			msg = payload.Response.Msg
		}
		return "", fmt.Errorf("alipay precreate failed: %s", msg)
	}
	return payload.Response.QRCode, nil
}

func (c *AlipayClient) commonParams(method, bizContent string, includeNotify, includeReturn bool) (url.Values, error) {
	params := url.Values{}
	params.Set("app_id", c.cfg.AppID)
	params.Set("method", method)
	params.Set("format", "JSON")
	params.Set("charset", "utf-8")
	params.Set("sign_type", "RSA2")
	params.Set("timestamp", businessclock.Now().Format("2006-01-02 15:04:05"))
	params.Set("version", "1.0")
	if includeNotify && strings.TrimSpace(c.cfg.NotifyURL) != "" {
		params.Set("notify_url", c.cfg.NotifyURL)
	}
	if includeReturn && strings.TrimSpace(c.cfg.ReturnURL) != "" {
		params.Set("return_url", c.cfg.ReturnURL)
	}
	params.Set("biz_content", bizContent)
	signature, err := c.sign(params)
	if err != nil {
		return nil, fmt.Errorf("sign alipay request: %w", err)
	}
	params.Set("sign", signature)
	return params, nil
}

func (c *AlipayClient) post(ctx context.Context, params url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.GatewayURL, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := httpclient.ReadBody(resp.Body, httpclient.MaxPaymentResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("read alipay response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("alipay http %d: %s", resp.StatusCode, string(body))
	}
	return body, nil
}

func (c *AlipayClient) sign(params url.Values) (string, error) {
	return c.signText(signContent(params))
}

func (c *AlipayClient) signText(text string) (string, error) {
	sum := sha256.Sum256([]byte(text))
	signer := c.signer
	if signer == nil {
		signer = rsa.SignPKCS1v15
	}
	data, err := signer(rand.Reader, c.privateKey, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(data), nil
}

func (c *AlipayClient) verify(params url.Values, sign string) error {
	signature, err := base64.StdEncoding.DecodeString(sign)
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(signContent(params)))
	return rsa.VerifyPKCS1v15(c.publicKey, crypto.SHA256, sum[:], signature)
}

func (c *AlipayClient) verifyOpenAPIResponse(body []byte, responseKey string) error {
	var payload map[string]json.RawMessage
	if err := jsonrequest.UnmarshalUnambiguous(body, &payload, jsonrequest.DefaultMaxDepth); err != nil {
		return err
	}
	response, ok := payload[responseKey]
	if !ok || len(bytes.TrimSpace(response)) == 0 {
		return fmt.Errorf("missing alipay response: %s", responseKey)
	}
	var sign string
	if raw, ok := payload["sign"]; ok {
		_ = json.Unmarshal(raw, &sign)
	}
	if strings.TrimSpace(sign) == "" {
		return errors.New("missing alipay response sign")
	}
	return c.verifyContent(bytes.TrimSpace(response), sign)
}

func (c *AlipayClient) verifyContent(content []byte, sign string) error {
	signature, err := base64.StdEncoding.DecodeString(sign)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(content)
	return rsa.VerifyPKCS1v15(c.publicKey, crypto.SHA256, sum[:], signature)
}

func signContent(params url.Values) string {
	keys := sortedKeys(params)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		if key == "sign" || key == "sign_type" {
			continue
		}
		value := params.Get(key)
		if value == "" {
			continue
		}
		parts = append(parts, key+"="+value)
	}
	return strings.Join(parts, "&")
}

func sortedKeys(values url.Values) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func productCode(product string) string {
	if strings.ToUpper(product) == "WAP" {
		return "QUICK_WAP_WAY"
	}
	return "FAST_INSTANT_TRADE_PAY"
}

func parsePrivateKey(text string) (*rsa.PrivateKey, error) {
	block, err := pemBlock(text, "PRIVATE KEY")
	if err != nil {
		return nil, err
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not RSA")
	}
	return key, nil
}

func parsePublicKey(text string) (*rsa.PublicKey, error) {
	block, err := pemBlock(text, "PUBLIC KEY")
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		if cert, certErr := x509.ParseCertificate(block.Bytes); certErr == nil {
			if key, ok := cert.PublicKey.(*rsa.PublicKey); ok {
				return key, nil
			}
		}
		return nil, err
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("public key is not RSA")
	}
	return key, nil
}

func pemBlock(text, suffix string) (*pem.Block, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("empty key")
	}
	if !strings.Contains(text, "-----BEGIN") {
		var b bytes.Buffer
		b.WriteString("-----BEGIN " + suffix + "-----\n")
		for len(text) > 64 {
			b.WriteString(text[:64] + "\n")
			text = text[64:]
		}
		b.WriteString(text + "\n-----END " + suffix + "-----")
		text = b.String()
	}
	block, _ := pem.Decode([]byte(text))
	if block == nil {
		return nil, errors.New("invalid PEM key")
	}
	return block, nil
}

func defaultGateway(mode string) string {
	if strings.ToUpper(strings.TrimSpace(mode)) == "PRODUCTION" {
		return defaultProductionGateway
	}
	return defaultSandboxGateway
}
