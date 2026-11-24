package payment

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"bugpk-api/internal/pkg/httpclient"
)

type paymentRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn paymentRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

type paymentFailingReadCloser struct{}

func (paymentFailingReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("alipay body read failed")
}

func (paymentFailingReadCloser) Close() error { return nil }

func TestAlipayPostPropagatesResponseReadFailure(t *testing.T) {
	client := newTestAlipayClient(t)
	client.httpClient = &http.Client{Transport: paymentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: paymentFailingReadCloser{}}, nil
	})}
	body, err := client.post(context.Background(), url.Values{"test": {"value"}})
	if err == nil || !strings.Contains(err.Error(), "read alipay response") {
		t.Fatalf("expected Alipay response read error, got body=%q err=%v", body, err)
	}
}

func TestAlipayPostRejectsOversizedResponse(t *testing.T) {
	client := newTestAlipayClient(t)
	client.httpClient = &http.Client{Transport: paymentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		body := strings.Repeat("x", int(httpclient.MaxPaymentResponseBytes)+1)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	_, err := client.post(context.Background(), url.Values{"test": {"value"}})
	if !errors.Is(err, httpclient.ErrResponseTooLarge) {
		t.Fatalf("oversized Alipay response error = %v, want ErrResponseTooLarge", err)
	}
}

func TestAlipayNotifyVerify(t *testing.T) {
	client := newTestAlipayClient(t)
	values := url.Values{}
	values.Set("app_id", "app")
	values.Set("out_trade_no", "BP123")
	values.Set("total_amount", "10.00")
	values.Set("trade_status", "TRADE_SUCCESS")
	values.Set("sign_type", "RSA2")
	values.Set("sign", mustSignParams(t, client, values))
	if err := client.VerifyNotify(values); err != nil {
		t.Fatalf("verify signed notify: %v", err)
	}
	values.Set("total_amount", "11.00")
	if err := client.VerifyNotify(values); err == nil {
		t.Fatal("expected tampered notify verification to fail")
	}
	values.Set("sign", base64.StdEncoding.EncodeToString([]byte("bad")))
	if err := client.VerifyNotify(values); err == nil {
		t.Fatal("expected invalid signature to fail")
	}
}

func TestSignContentPreservesParameterWhitespace(t *testing.T) {
	values := url.Values{
		"app_id":     {"2026000000000000"},
		"empty":      {""},
		"sign":       {"excluded"},
		"sign_type":  {"RSA2"},
		"subject":    {"  paid order  "},
		"whitespace": {" "},
	}
	want := "app_id=2026000000000000&subject=  paid order  &whitespace= "
	if got := signContent(values); got != want {
		t.Fatalf("signContent() = %q, want exact callback values %q", got, want)
	}
}

func TestAlipayOpenAPIResponseVerify(t *testing.T) {
	client := newTestAlipayClient(t)
	response := `{"code":"10000","msg":"Success","out_trade_no":"BP123","total_amount":"10.00","trade_status":"TRADE_SUCCESS"}`
	body := signedAlipayBody(t, "alipay_trade_query_response", response, mustSignText(t, client, response))
	if err := client.verifyOpenAPIResponse(body, "alipay_trade_query_response"); err != nil {
		t.Fatalf("verify signed response: %v", err)
	}

	tampered := `{"code":"10000","msg":"Success","out_trade_no":"BP123","total_amount":"11.00","trade_status":"TRADE_SUCCESS"}`
	body = signedAlipayBody(t, "alipay_trade_query_response", tampered, mustSignText(t, client, response))
	if err := client.verifyOpenAPIResponse(body, "alipay_trade_query_response"); err == nil {
		t.Fatal("expected tampered response verification to fail")
	}

	body = signedAlipayBody(t, "alipay_trade_query_response", response, "")
	if err := client.verifyOpenAPIResponse(body, "alipay_trade_query_response"); err == nil {
		t.Fatal("expected missing response sign to fail")
	}
}

func TestValidateAlipayKeyMaterialRejectsWeakRSA(t *testing.T) {
	privateText, publicText := generateAlipayKeyMaterial(t, 1024, true)
	if err := ValidateAlipayKeyMaterial(privateText, publicText); err == nil || !strings.Contains(err.Error(), "RSA-2048") {
		t.Fatalf("weak RSA key error = %v", err)
	}
}

func TestValidateAlipayKeyMaterialRejectsSwappedKeys(t *testing.T) {
	privateText, publicText := generateAlipayKeyMaterial(t, 2048, true)
	if err := ValidateAlipayKeyMaterial(publicText, privateText); err == nil {
		t.Fatal("swapped public/private keys were accepted")
	}
}

func TestValidateAlipayKeyMaterialAcceptsRawBase64(t *testing.T) {
	privateText, publicText := generateAlipayKeyMaterial(t, 2048, false)
	if err := ValidateAlipayKeyMaterial(privateText, publicText); err != nil {
		t.Fatalf("raw Base64 key material rejected: %v", err)
	}
}

func newTestAlipayClient(t *testing.T) *AlipayClient {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})
	publicBytes, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicBytes})
	client, err := NewAlipayClient(AlipayConfig{
		AlipayPublicKey: string(publicPEM),
		AppID:           "app",
		AppPrivateKey:   string(privatePEM),
		Enabled:         true,
		GatewayURL:      defaultSandboxGateway,
		Mode:            "SANDBOX",
	})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

func generateAlipayKeyMaterial(t *testing.T, bits int, withPEM bool) (string, string) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, bits)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	privateDER := x509.MarshalPKCS1PrivateKey(privateKey)
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	if !withPEM {
		return base64.StdEncoding.EncodeToString(privateDER), base64.StdEncoding.EncodeToString(publicDER)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: privateDER})
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	return string(privatePEM), string(publicPEM)
}

func mustSignParams(t *testing.T, client *AlipayClient, values url.Values) string {
	t.Helper()
	signature, err := client.sign(values)
	if err != nil {
		t.Fatalf("sign params: %v", err)
	}
	return signature
}

func mustSignText(t *testing.T, client *AlipayClient, value string) string {
	t.Helper()
	signature, err := client.signText(value)
	if err != nil {
		t.Fatalf("sign text: %v", err)
	}
	return signature
}

func TestAlipayRequestSigningFailureStopsCreation(t *testing.T) {
	client := newTestAlipayClient(t)
	client.signer = func(io.Reader, *rsa.PrivateKey, crypto.Hash, []byte) ([]byte, error) {
		return nil, errors.New("signer unavailable")
	}
	result, err := client.Create(context.Background(), PaymentOrder{
		OrderNo:   "BP20260710123456AABBCCDDEEFF",
		PayAmount: "10.0000",
		Product:   "PAGE",
		Subject:   "test order",
	})
	if err == nil || !strings.Contains(err.Error(), "sign alipay request") {
		t.Fatalf("expected signing error, got result=%+v err=%v", result, err)
	}
	if result.PayForm != "" || result.PayURL != "" || result.QRCode != "" {
		t.Fatalf("signing failure returned payment payload: %+v", result)
	}
}

func TestAlipayCreateUsesProviderExpiryAndScopedCallbacks(t *testing.T) {
	client := newTestAlipayClient(t)
	client.cfg.NotifyURL = "https://merchant.example/alipay/notify"
	client.cfg.ReturnURL = "https://merchant.example/payment/result"
	result, err := client.Create(context.Background(), PaymentOrder{
		OrderNo:   "BP20260710123456AABBCCDDEEFF",
		PayAmount: "10.0000",
		Product:   "PAGE",
		Subject:   "test order",
	})
	if err != nil {
		t.Fatal(err)
	}
	var biz map[string]any
	if err := json.Unmarshal([]byte(result.FormParams["biz_content"]), &biz); err != nil {
		t.Fatalf("decode biz_content: %v", err)
	}
	if biz["timeout_express"] != "30m" {
		t.Fatalf("timeout_express=%v, want 30m", biz["timeout_express"])
	}
	if result.FormParams["notify_url"] != client.cfg.NotifyURL || result.FormParams["return_url"] != client.cfg.ReturnURL {
		t.Fatalf("payment callbacks missing: %#v", result.FormParams)
	}

	query, err := client.commonParams("alipay.trade.query", `{"out_trade_no":"BP_TEST"}`, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if query.Has("notify_url") || query.Has("return_url") {
		t.Fatalf("query request leaked payment callbacks: %s", query.Encode())
	}
	precreate, err := client.commonParams("alipay.trade.precreate", `{}`, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if precreate.Get("notify_url") != client.cfg.NotifyURL || precreate.Has("return_url") {
		t.Fatalf("precreate callback scope is invalid: %s", precreate.Encode())
	}
}

func TestPaymentOrderResponseUsesStructuredOfficialForm(t *testing.T) {
	order := paymentOrderForResponse(PaymentOrder{
		OrderNo: "BP123",
		PayForm: `<form action="https://openapi.alipay.com/gateway.do" method="post"><input type="hidden" name="app_id" value="123"><input type="hidden" name="sign" value="signed"></form><script>document.forms[0].submit()</script>`,
	})
	if order.FormActionURL != defaultProductionGateway || order.FormParams["app_id"] != "123" || order.FormParams["sign"] != "signed" {
		t.Fatalf("structured payment form was not recovered: %+v", order)
	}
	if order.PayForm != "" {
		t.Fatalf("script-bearing payForm remained in response: %q", order.PayForm)
	}
	data, err := json.Marshal(order)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "payForm") || strings.Contains(string(data), "<script") {
		t.Fatalf("payment response leaked raw form HTML: %s", data)
	}
}

func TestStoredAlipayFormRejectsUntrustedOrAmbiguousMarkup(t *testing.T) {
	for _, value := range []string{
		`<form action="https://evil.example/gateway.do" method="post"><input type="hidden" name="sign" value="a"></form>`,
		`<form action="https://openapi.alipay.com/gateway.do" method="post"><input type="hidden" name="sign" value="a"><input type="hidden" name="sign" value="b"></form>`,
		`<form action="https://openapi.alipay.com/gateway.do" method="post"><input type="text" name="sign" value="a"></form>`,
		`<form action="https://openapi.alipay.com/gateway.do" method="get"><input type="hidden" name="sign" value="a"></form>`,
	} {
		if action, params, ok := parseStoredAlipayForm(value); ok || action != "" || params != nil {
			t.Fatalf("unsafe stored pay form accepted: %q -> %q %#v", value, action, params)
		}
	}
}

func signedAlipayBody(t *testing.T, responseKey, response, sign string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		responseKey: json.RawMessage(response),
		"sign":      sign,
	})
	if err != nil {
		t.Fatalf("marshal signed body: %v", err)
	}
	return body
}

func TestParseAlipayQueryResponseIncludesSettlementFields(t *testing.T) {
	body := []byte(`{
		"alipay_trade_query_response": {
			"code": "10000",
			"msg": "Success",
			"out_trade_no": "BP202607020001",
			"seller_id": "seller_1",
			"total_amount": "10.00",
			"trade_no": "2026070222000000001",
			"buyer_user_id": "buyer_1",
			"trade_status": "TRADE_SUCCESS"
		}
	}`)
	status, err := parseAlipayQueryResponse(body)
	if err != nil {
		t.Fatalf("parse query response: %v", err)
	}
	if !status.Success {
		t.Fatal("expected paid status")
	}
	if status.OutTradeNo != "BP202607020001" ||
		status.SellerID != "seller_1" ||
		status.TotalAmount != "10.00" ||
		status.TradeNo != "2026070222000000001" ||
		status.BuyerID != "buyer_1" ||
		status.TradeStatus != "TRADE_SUCCESS" {
		t.Fatalf("unexpected parsed status: %+v", status)
	}
}

func TestParseAlipayQueryResponseFailureMessage(t *testing.T) {
	_, err := parseAlipayQueryResponse([]byte(`{
		"alipay_trade_query_response": {
			"code": "40004",
			"msg": "Business Failed",
			"sub_msg": "order not found"
		}
	}`))
	if err == nil || !strings.Contains(err.Error(), "order not found") {
		t.Fatalf("expected query failure message, got %v", err)
	}
}
