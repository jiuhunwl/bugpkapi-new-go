package payment

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
)

// Keep configuration reads, decryption, orders and payload writes real. Only
// the external gateway transport is replaced; it has no network fallback.
type wechatCreateConfigRepository struct {
	*Repository
	httpClient *http.Client
}

func (r *wechatCreateConfigRepository) WechatConfig(ctx context.Context) (storedWechatConfig, error) {
	cfg, err := r.Repository.WechatConfig(ctx)
	cfg.HTTPClient = r.httpClient
	return cfg, err
}

func seedWechatCreateProducts(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, query := range []string{
		"insert into bp_user(id,username,password_hash,display_name,balance,points,primary_role_id) values(990500,'wechat-create-fixture','test-only','Create fixture',0,0,2)",
		"insert into bp_recharge_preset(id,name,amount,gift_amount,sort_order,status) values(990500,'Create recharge',10.0000,0,0,1)",
		"insert into bp_package_global(id,name,price,status) values(990500,'Create global',10.0000,1)",
		"insert into bp_package_point(id,name,price,points,status) values(990500,'Create points',10.0000,100,1)",
		"insert into bp_interface_api(id,api_code,name,request_url,request_method,remark,status) values(990500,'wechat-create-fixture','Create API','https://fixture.invalid/api','GET','Test only',1)",
		"insert into bp_package_interface(id,interface_id,name,status) values(990500,990500,'Create interface',1)",
		"insert into bp_package_interface_spec(id,package_id,name,price,status) values(990500,990500,'Create spec',10.0000,1)",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := NewRepository(db).SavePaymentMethods(context.Background(), []PaymentMethod{{Code: ChannelWechat, Enabled: true}}); err != nil {
		t.Fatal(err)
	}
}

func newWechatCreateService(t *testing.T, db *sql.DB, cfg WechatConfig, transport http.RoundTripper) *Service {
	t.Helper()
	repo := NewRepository(db)
	cipher := NewConfigCipher("test-only-wechat-create-secret")
	encrypt := func(value string) string {
		encrypted, err := cipher.Encrypt(value)
		if err != nil {
			t.Fatal(err)
		}
		return encrypted
	}
	if err := repo.SaveWechatConfig(context.Background(), cfg, encrypt(cfg.APIv3Key), encrypt(cfg.PrivateKeyPEM), encrypt(cfg.PlatformPublicKey), true, true); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	s := NewService(repo, nil, cipher)
	s.wechatConfigRepo = &wechatCreateConfigRepository{Repository: repo, httpClient: client}
	// Fail closed if an accidental constructor regression would otherwise send
	// a test transaction to the real provider.
	loaded, _, err := s.wechatClientWithConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.httpClient != client {
		t.Fatal("test transport not installed; refusing live gateway traffic")
	}
	return s
}

func wechatCreateResponse(t *testing.T, f wechatMessageFixture, body string) *http.Response {
	t.Helper()
	headers := wechatMessageHeaders(t, f.platform.privateKey, f.cfg.PlatformPublicKeyID, []byte(body), strconv.FormatInt(time.Now().Unix(), 10))
	return &http.Response{StatusCode: http.StatusOK, Header: headers, Body: io.NopCloser(strings.NewReader(body))}
}

func decodeWechatCreateBody(t *testing.T, request *http.Request) map[string]json.RawMessage {
	t.Helper()
	if request.Method != http.MethodPost || request.URL.Scheme != "https" || request.URL.Host != "api.mch.weixin.qq.com" {
		t.Fatalf("unexpected gateway request: %s %s", request.Method, request.URL)
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func assertWechatCreateInvocation(t *testing.T, payload, trade, appID, merchantID, prepayID string, key *rsa.PublicKey) {
	t.Helper()
	var params map[string]string
	if err := json.Unmarshal([]byte(payload), &params); err != nil {
		t.Fatalf("usable invocation parameters missing: %v", err)
	}
	var timestamp, nonce, signature, message string
	if trade == TradeTypeAPP {
		timestamp, nonce, signature = params["timestamp"], params["noncestr"], params["sign"]
		if params["appid"] != appID || params["partnerid"] != merchantID || params["prepayid"] != prepayID || params["package"] != "Sign=WXPay" {
			t.Fatalf("incorrect APP invocation: %v", params)
		}
		message = appID + "\n" + timestamp + "\n" + nonce + "\n" + prepayID + "\n"
	} else {
		timestamp, nonce, signature = params["timeStamp"], params["nonceStr"], params["paySign"]
		if params["appId"] != appID || params["package"] != "prepay_id="+prepayID || params["signType"] != "RSA" {
			t.Fatalf("incorrect JSAPI/MINI invocation: %v", params)
		}
		message = appID + "\n" + timestamp + "\n" + nonce + "\nprepay_id=" + prepayID + "\n"
	}
	epoch, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || time.Since(time.Unix(epoch, 0)).Abs() > time.Minute || nonce == "" || signature == "" {
		t.Fatalf("incomplete or stale invocation timestamp/nonce/signature: %v", params)
	}
	signatureBytes, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256([]byte(message))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signatureBytes); err != nil {
		t.Fatalf("invocation signature violates the official four-line contract: %v", err)
	}
}

func createWechatFixtureOrder(t *testing.T, s *Service, kind, trade string) (PaymentOrder, error) {
	t.Helper()
	ctx := hertzapp.NewContext(0)
	ctx.SetClientIPFunc(func(*hertzapp.RequestContext) string { return "203.0.113.42" })
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Request.SetBodyString(fmt.Sprintf("{\"payChannel\":\"WECHAT\",\"product\":%q,\"tradeType\":%q,\"openId\":\"fixture-payer\",\"presetId\":990500,\"clientIP\":\"6.6.6.6\"}", trade, trade))
	var req CreateOrderRequest
	if !bindPaymentOrder(ctx, &req) {
		t.Fatalf("order binding failed: %s", ctx.Response.Body())
	}
	principal := Principal{ID: 990500}
	switch kind {
	case "RECHARGE":
		return s.CreateRechargeOrder(context.Background(), principal, req)
	case "GLOBAL_PACKAGE":
		return s.CreateGlobalPackageOrder(context.Background(), principal, 990500, req)
	case "INTERFACE_PACKAGE":
		return s.CreateInterfacePackageOrder(context.Background(), principal, 990500, req)
	case "POINT_PACKAGE":
		return s.CreatePointPackageOrder(context.Background(), principal, 990500, req)
	default:
		t.Fatalf("unknown test order kind %q", kind)
		return PaymentOrder{}, nil
	}
}

func TestWechatOrderCreationServiceIntegration(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	seedWechatCreateProducts(t, db)
	for _, partner := range []bool{false, true} {
		f := newWechatMessageFixture(t, partner)
		for _, trade := range []string{TradeTypeNative, TradeTypeH5, TradeTypeJSAPI, TradeTypeMini, TradeTypeAPP} {
			for _, kind := range []string{"RECHARGE", "GLOBAL_PACKAGE", "INTERFACE_PACKAGE", "POINT_PACKAGE"} {
				t.Run(fmt.Sprintf("partner=%v/%s/%s", partner, trade, kind), func(t *testing.T) {
					calls := 0
					capturedOrder := ""
					wantApp := map[string]string{TradeTypeNative: "wx-base", TradeTypeH5: "wx-base", TradeTypeJSAPI: "wx-oa", TradeTypeMini: "wx-mini", TradeTypeAPP: "wx-app"}[trade]
					s := newWechatCreateService(t, db, f.cfg, paymentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
						calls++
						body := decodeWechatCreateBody(t, request)
						if err := json.Unmarshal(body["out_trade_no"], &capturedOrder); err != nil || capturedOrder == "" {
							t.Fatalf("missing merchant order number: %v", err)
						}
						var localStatus, localProduct, localChannel string
						if err := db.QueryRow("select status,product,pay_channel from bp_payment_order where order_no=?", capturedOrder).Scan(&localStatus, &localProduct, &localChannel); err != nil {
							t.Fatal(err)
						}
						if localStatus != "CREATED" || localProduct != trade || localChannel != ChannelWechat {
							t.Fatalf("gateway called before a correctly typed order was persisted: %s/%s/%s", localStatus, localProduct, localChannel)
						}
						suffix := map[string]string{TradeTypeNative: "native", TradeTypeH5: "h5", TradeTypeJSAPI: "jsapi", TradeTypeMini: "jsapi", TradeTypeAPP: "app"}[trade]
						prefix := "/v3/pay/transactions/"
						if partner {
							prefix = "/v3/pay/partner/transactions/"
						}
						if request.URL.Path != prefix+suffix {
							t.Fatalf("wrong trade endpoint: %s", request.URL.Path)
						}
						assertWechatJSONField(t, body, "notify_url", "https://merchant.example/notify/wechat")
						var amount map[string]any
						if err := json.Unmarshal(body["amount"], &amount); err != nil {
							t.Fatal(err)
						}
						if amount["total"] != float64(1000) || amount["currency"] != "CNY" {
							t.Fatalf("wrong gateway amount: %v", amount)
						}
						if partner {
							assertWechatJSONField(t, body, "sp_mchid", "1900000001")
							assertWechatJSONField(t, body, "sub_mchid", "1900000002")
							assertWechatJSONField(t, body, "sp_appid", "wx-sp")
							if trade == TradeTypeNative || trade == TradeTypeH5 {
								assertWechatJSONField(t, body, "sub_appid", "wx-sub")
							} else {
								assertWechatJSONField(t, body, "sub_appid", wantApp)
							}
						} else {
							assertWechatJSONField(t, body, "appid", wantApp)
							assertWechatJSONField(t, body, "mchid", "1900000001")
						}
						if trade == TradeTypeJSAPI || trade == TradeTypeMini {
							var payer map[string]string
							if err := json.Unmarshal(body["payer"], &payer); err != nil {
								t.Fatal(err)
							}
							key := "openid"
							if partner {
								key = "sub_openid"
							}
							if len(payer) != 1 || payer[key] != "fixture-payer" {
								t.Fatalf("wrong payer app namespace: %v", payer)
							}
						}
						if trade == TradeTypeH5 {
							var scene map[string]json.RawMessage
							if err := json.Unmarshal(body["scene_info"], &scene); err != nil {
								t.Fatal(err)
							}
							assertWechatJSONField(t, scene, "payer_client_ip", "203.0.113.42")
						}
						response := "{\"prepay_id\":\"prepay-service-fixture\"}"
						if trade == TradeTypeNative {
							response = "{\"code_url\":\"weixin://wxpay/bizpayurl?pr=fixture\"}"
						}
						if trade == TradeTypeH5 {
							response = "{\"h5_url\":\"https://wx.tenpay.com/service-fixture\"}"
						}
						return wechatCreateResponse(t, f, response), nil
					}))
					result, err := createWechatFixtureOrder(t, s, kind, trade)
					if err != nil || calls != 1 {
						t.Fatalf("valid order creation failed: err=%v calls=%d", err, calls)
					}
					if result.OrderNo != capturedOrder || result.OrderType != kind || result.PayChannel != ChannelWechat || result.Product != trade || result.TradeType != trade || result.Status != "PAYING" {
						t.Fatalf("wrong created order response: %+v", result)
					}
					stored, err := s.repo.OrderByNo(context.Background(), result.OrderNo, 990500, false)
					if err != nil {
						t.Fatal(err)
					}
					if stored.Status != "PAYING" || stored.Product != trade || stored.TradeType != trade {
						t.Fatalf("persisted trade/state mismatch: %+v", stored)
					}
					if trade == TradeTypeNative {
						if result.QRCode != "weixin://wxpay/bizpayurl?pr=fixture" || stored.QRCode != result.QRCode || result.PayParams != "" {
							t.Fatalf("Native payload not returned/persisted: %+v", result)
						}
					} else if trade == TradeTypeH5 {
						if result.PayURL != "https://wx.tenpay.com/service-fixture" || stored.PayURL != result.PayURL || result.PayParams != "" {
							t.Fatalf("H5 payload not returned/persisted: %+v", result)
						}
					} else {
						merchantID := "1900000001"
						if partner {
							merchantID = "1900000002"
						}
						assertWechatCreateInvocation(t, result.PayParams, trade, wantApp, merchantID, "prepay-service-fixture", &f.client.privateKey.PublicKey)
						if result.PrepayID != "prepay-service-fixture" || stored.PrepayID != result.PrepayID {
							t.Fatalf("prepay ID not persisted: %+v", stored)
						}
					}
					// Creation supplies ephemeral invocation parameters; a normal order read
					// is deliberately not advertised as a refresh/resume API.
					if stored.PayParams != "" {
						t.Fatal("unexpected persistence of ephemeral invocation parameters")
					}
					encoded, err := json.Marshal(result)
					if err != nil {
						t.Fatal(err)
					}
					if bytes.Contains(encoded, []byte("203.0.113.42")) || bytes.Contains(encoded, []byte("6.6.6.6")) {
						t.Fatal("server-only client IP leaked in order JSON")
					}
				})
			}
		}
	}
}

func TestWechatOrderCreationPreservesTerminalStateIntegration(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	seedWechatCreateProducts(t, db)
	f := newWechatMessageFixture(t, false)
	for _, terminal := range []string{"PAID", "CLOSED"} {
		t.Run(terminal, func(t *testing.T) {
			capturedOrder := ""
			calls := 0
			s := newWechatCreateService(t, db, f.cfg, paymentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				body := decodeWechatCreateBody(t, request)
				if err := json.Unmarshal(body["out_trade_no"], &capturedOrder); err != nil {
					t.Fatal(err)
				}
				if terminal == "PAID" {
					if err := NewRepository(db).SettleWechatPaidOrder(context.Background(), capturedOrder, PayTradeStatus{OutTradeNo: capturedOrder, TradeNo: "create-terminal-paid", Total: "10.0000", BuyerID: "fixture-payer", Success: true}); err != nil {
						t.Fatal(err)
					}
				} else {
					if _, err := db.Exec("update bp_payment_order set status='CLOSED' where order_no=?", capturedOrder); err != nil {
						t.Fatal(err)
					}
				}
				return wechatCreateResponse(t, f, "{\"prepay_id\":\"prepay-terminal-fixture\"}"), nil
			}))
			result, err := createWechatFixtureOrder(t, s, "RECHARGE", TradeTypeJSAPI)
			if calls != 1 {
				t.Fatalf("unexpected gateway calls: %d", calls)
			}
			if terminal == "PAID" && (err != nil || result.Status != "PAID") {
				t.Fatalf("paid notification raced with create: status=%s err=%v", result.Status, err)
			}
			if terminal == "CLOSED" && err == nil {
				t.Fatal("closed order incorrectly accepted a payment payload")
			}
			if result.PayParams != "" {
				t.Fatal("terminal order returned reusable payment invocation parameters")
			}
			var storedStatus string
			if err := db.QueryRow("select status from bp_payment_order where order_no=?", capturedOrder).Scan(&storedStatus); err != nil {
				t.Fatal(err)
			}
			if storedStatus != terminal {
				t.Fatalf("create reverted terminal status: %s", storedStatus)
			}
		})
	}
}

func TestWechatUnusableCreateResponseDoesNotMarkPayingIntegration(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	seedWechatCreateProducts(t, db)
	f := newWechatMessageFixture(t, false)
	for _, trade := range []string{TradeTypeNative, TradeTypeH5, TradeTypeJSAPI, TradeTypeMini, TradeTypeAPP} {
		t.Run(trade, func(t *testing.T) {
			capturedOrder := ""
			s := newWechatCreateService(t, db, f.cfg, paymentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				body := decodeWechatCreateBody(t, request)
				if err := json.Unmarshal(body["out_trade_no"], &capturedOrder); err != nil {
					t.Fatal(err)
				}
				return wechatCreateResponse(t, f, "{}"), nil
			}))
			result, err := createWechatFixtureOrder(t, s, "RECHARGE", trade)
			if err == nil || capturedOrder == "" || result.PayParams != "" {
				t.Fatalf("unusable signed provider response reported success: err=%v", err)
			}
			stored, readErr := s.repo.OrderByNo(context.Background(), capturedOrder, 990500, false)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if stored.Status != "CREATED" || stored.QRCode != "" || stored.PayURL != "" || stored.PrepayID != "" {
				t.Fatalf("unusable payment response persisted as payable: %+v", stored)
			}
		})
	}
}

func TestWechatFractionalCentOrderIsRejectedBeforeGatewayIntegration(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	seedWechatCreateProducts(t, db)
	f := newWechatMessageFixture(t, false)
	for _, query := range []string{
		"update bp_recharge_preset set amount=10.0001 where id=990500",
		"update bp_package_global set price=10.0001 where id=990500",
		"update bp_package_interface_spec set price=10.0001 where id=990500",
		"update bp_package_point set price=10.0001 where id=990500",
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"RECHARGE", "GLOBAL_PACKAGE", "INTERFACE_PACKAGE", "POINT_PACKAGE"} {
		t.Run(kind, func(t *testing.T) {
			calls := 0
			s := newWechatCreateService(t, db, f.cfg, paymentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				decodeWechatCreateBody(t, request)
				return wechatCreateResponse(t, f, "{\"code_url\":\"weixin://wxpay/bizpayurl?pr=fractional\"}"), nil
			}))
			var before, after int
			if err := db.QueryRow("select count(*) from bp_payment_order").Scan(&before); err != nil {
				t.Fatal(err)
			}
			_, err := createWechatFixtureOrder(t, s, kind, TradeTypeNative)
			if err == nil || calls != 0 {
				t.Errorf("unsettleable fractional-cent order reached gateway: err=%v calls=%d", err, calls)
			}
			if err := db.QueryRow("select count(*) from bp_payment_order").Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Errorf("unsettleable order persisted: before=%d after=%d", before, after)
			}
		})
	}
}
