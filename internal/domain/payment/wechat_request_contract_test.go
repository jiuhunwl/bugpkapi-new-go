package payment

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The transport is replaced before any API operation. This contract test does
// not contact WeChat and cannot prove acceptance by a real merchant account.
func TestWechatOutgoingRequestsBindMerchantAndProtocol(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	platform := newTestAlipayClient(t)
	const orderNo = "BP20261009123456ABCDEF123456"
	const codeURL = "weixin://wxpay/bizpayurl?pr=fixture"
	for _, partner := range []bool{false, true} {
		for _, operation := range []string{"create", "query", "close"} {
			t.Run(fmt.Sprintf("partner=%v/%s", partner, operation), func(t *testing.T) {
				cfg := WechatConfig{
					Enabled: true, Mode: "PRODUCTION", Mchid: "1900000001", AppID: "wx0000000000000001",
					APIv3Key: "0123456789abcdef0123456789abcdef", PrivateKeyPEM: privatePEM,
					SerialNo: "0123456789ABCDEF", PlatformPublicKeyID: "PUB_KEY_ID_REQUEST_TEST", PlatformPublicKey: platform.cfg.AlipayPublicKey, NotifyURL: "https://merchant.example/notify/wechat",
				}
				prefix := "/v3/pay/transactions"
				if partner {
					// The certificate belongs to the service provider, not to the
					// sub-merchant receiving this particular payment.
					cfg.Mchid, cfg.SpMchid = "1900000009", "1900000002"
					cfg.AppID, cfg.SpAppID = "wx0000000000000002", "wx0000000000000002"
					cfg.SubMchid, cfg.SubAppID = "1900000010", "wx0000000000000010"
					prefix = "/v3/pay/partner/transactions"
				}
				client, err := NewWechatClient(cfg)
				if err != nil {
					t.Fatal(err)
				}
				started := time.Now().Unix()
				calls := 0
				client.httpClient = &http.Client{Transport: paymentRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.URL.Scheme != "https" || req.URL.Host != "api.mch.weixin.qq.com" {
						t.Errorf("default payment API must use its HTTPS origin: %s", req.URL)
					}
					var body []byte
					if req.Body != nil {
						defer req.Body.Close()
						var readErr error
						body, readErr = io.ReadAll(req.Body)
						if readErr != nil {
							return nil, readErr
						}
					}
					signingMerchant := cfg.Mchid
					if partner {
						signingMerchant = cfg.SpMchid
					}
					assertWechatAuthorizationContract(t, req, body, signingMerchant, cfg.SerialNo, started, &key.PublicKey)
					wantMethod, wantPath := http.MethodPost, prefix+"/native"
					wantQuery := make(url.Values)
					var object map[string]json.RawMessage
					if len(body) != 0 {
						if err := json.Unmarshal(body, &object); err != nil {
							t.Errorf("request body is not a JSON object: %v", err)
						}
					}
					wantMerchant := map[string]string{"mchid": cfg.Mchid}
					if partner {
						wantMerchant = map[string]string{"sp_mchid": cfg.SpMchid, "sub_mchid": cfg.SubMchid}
					}
					switch operation {
					case "query":
						wantMethod, wantPath = http.MethodGet, prefix+"/out-trade-no/"+orderNo
						for name, value := range wantMerchant {
							wantQuery.Set(name, value)
						}
						if len(body) != 0 {
							t.Errorf("GET query unexpectedly carried a body: %q", body)
						}
					case "close":
						wantPath = prefix + "/out-trade-no/" + orderNo + "/close"
					case "create":
						wantMerchant["appid"] = cfg.AppID
						if partner {
							delete(wantMerchant, "appid")
							wantMerchant["sp_appid"] = cfg.SpAppID
							wantMerchant["sub_appid"] = cfg.SubAppID
						}
						assertWechatJSONField(t, object, "out_trade_no", orderNo)
						assertWechatJSONField(t, object, "notify_url", cfg.NotifyURL)
						var amount struct {
							Total    int64  `json:"total"`
							Currency string `json:"currency"`
						}
						if err := json.Unmarshal(object["amount"], &amount); err != nil || amount.Total != 1000 || amount.Currency != "CNY" {
							t.Errorf("wrong native charge amount: %+v err=%v", amount, err)
						}
					}
					if operation != "query" {
						for name, value := range wantMerchant {
							assertWechatJSONField(t, object, name, value)
						}
						if partner {
							if _, present := object["mchid"]; present {
								t.Error("partner request contains direct-merchant mchid")
							}
						}
					}
					if req.Method != wantMethod || req.URL.Path != wantPath || req.URL.Query().Encode() != wantQuery.Encode() {
						t.Errorf("wrong payment endpoint: %s %s; want %s %s?%s", req.Method, req.URL.RequestURI(), wantMethod, wantPath, wantQuery.Encode())
					}
					status, response := http.StatusOK, `{"code_url":"`+codeURL+`"}`
					if operation == "query" {
						queryResult := map[string]any{
							"out_trade_no": orderNo, "transaction_id": "test-transaction", "trade_type": "NATIVE",
							"trade_state": "SUCCESS", "trade_state_desc": "支付成功", "success_time": "2026-10-09T12:34:56+08:00",
							"amount": map[string]any{"total": 1000, "payer_total": 1000, "currency": "CNY", "payer_currency": "CNY"},
						}
						if partner {
							queryResult["sp_mchid"], queryResult["sub_mchid"] = cfg.SpMchid, cfg.SubMchid
							queryResult["sp_appid"], queryResult["sub_appid"] = cfg.SpAppID, cfg.SubAppID
							queryResult["payer"] = map[string]string{"sp_openid": "fixture-provider-payer", "sub_openid": "fixture-sub-payer"}
						} else {
							queryResult["mchid"], queryResult["appid"] = cfg.Mchid, cfg.AppID
							queryResult["payer"] = map[string]string{"openid": "fixture-payer"}
						}
						encoded, err := json.Marshal(queryResult)
						if err != nil {
							return nil, err
						}
						response = string(encoded)
					} else if operation == "close" {
						status, response = http.StatusNoContent, ""
					}
					headers := wechatMessageHeaders(t, platform.privateKey, cfg.PlatformPublicKeyID, []byte(response), strconv.FormatInt(time.Now().Unix(), 10))
					return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
				})}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				switch operation {
				case "create":
					result, err := client.Create(ctx, PaymentOrder{OrderNo: orderNo, Subject: "Fixture recharge", PayAmount: "10.0000", TradeType: TradeTypeNative})
					if err != nil || result.CodeURL != codeURL {
						t.Errorf("native create response contract failed: result=%+v err=%v", result, err)
					}
				case "query":
					result, err := client.Query(ctx, orderNo)
					if err != nil || !result.Success || result.OutTradeNo != orderNo || result.TradeNo != "test-transaction" || result.Total != "10.0000" {
						t.Errorf("query core result decoding failed: result=%+v err=%v", result, err)
					}
				case "close":
					if err := client.Close(ctx, orderNo); err != nil {
						t.Errorf("204 close response failed: %v", err)
					}
				}
				if calls != 1 {
					t.Errorf("operation did not issue exactly one captured request: %d", calls)
				}
			})
		}
	}
}

func assertWechatJSONField(t *testing.T, object map[string]json.RawMessage, name, want string) {
	t.Helper()
	var got string
	if err := json.Unmarshal(object[name], &got); err != nil || got != want {
		t.Errorf("wrong %s: got=%q want=%q err=%v", name, got, want, err)
	}
}

func assertWechatAuthorizationContract(t *testing.T, req *http.Request, body []byte, merchant, serial string, started int64, publicKey *rsa.PublicKey) {
	t.Helper()
	scheme, parameters, ok := strings.Cut(req.Header.Get("Authorization"), " ")
	if !ok || scheme != "WECHATPAY2-SHA256-RSA2048" {
		t.Errorf("invalid payment authorization scheme: %q", scheme)
		return
	}
	fields := make(map[string]string)
	for _, part := range strings.Split(parameters, ",") {
		name, quoted, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			t.Errorf("malformed authorization field: %q", name)
			return
		}
		value, err := strconv.Unquote(quoted)
		if err != nil {
			t.Errorf("malformed authorization value for %s: %v", name, err)
			return
		}
		if _, duplicate := fields[name]; duplicate {
			t.Errorf("duplicate authorization field: %s", name)
			return
		}
		fields[name] = value
	}
	if len(fields) != 5 || fields["mchid"] != merchant || fields["serial_no"] != serial || fields["nonce_str"] == "" {
		t.Errorf("authorization does not identify the configured signing merchant: mchid=%q serial=%q fields=%d, want mchid=%q serial=%q", fields["mchid"], fields["serial_no"], len(fields), merchant, serial)
	}
	timestamp, err := strconv.ParseInt(fields["timestamp"], 10, 64)
	if err != nil || timestamp < started || timestamp > time.Now().Unix() {
		t.Errorf("authorization timestamp is not current: %q err=%v", fields["timestamp"], err)
	}
	signature, err := base64.StdEncoding.DecodeString(fields["signature"])
	if err != nil {
		t.Errorf("invalid signature encoding: %v", err)
		return
	}
	// Verify against the actual URI (including its query) and bytes delivered
	// to RoundTrip, not a reconstruction using the production signing helper.
	message := req.Method + "\n" + req.URL.RequestURI() + "\n" + fields["timestamp"] + "\n" + fields["nonce_str"] + "\n" + string(body) + "\n"
	digest := sha256.Sum256([]byte(message))
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Errorf("signature does not cover the transmitted request: %v", err)
	}
}
