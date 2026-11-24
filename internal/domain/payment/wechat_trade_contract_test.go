package payment

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"database/sql/driver"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	hertzapp "github.com/cloudwego/hertz/pkg/app"

	"bugpk-api/internal/pkg/testdb"
)

func TestOnlineWechatProductsReachAllOrderBuilders(t *testing.T) {
	stop := errors.New("controlled product lookup boundary")
	for _, channel := range []string{ChannelWechat, ChannelHupijiaoWechat} {
		for _, product := range []string{TradeTypeNative, TradeTypeH5} {
			for _, kind := range []string{"recharge", "global", "interface", "points"} {
				t.Run(channel+"/"+product+"/"+kind, func(t *testing.T) {
					queries := 0
					db := testdb.OpenStub(t, testdb.SQLStub{Query: func(context.Context, string, []driver.NamedValue) (driver.Rows, error) { queries++; return nil, stop }})
					s := &Service{repo: NewRepository(db), wechatConfigRepo: &wechatConfigRepositoryStub{stored: storedWechatConfig{WechatConfig: WechatConfig{Enabled: true}}}, hupijiaoConfigRepo: &hupijiaoConfigRepositoryStub{methods: []PaymentMethod{{Code: channel, Enabled: true}}, configs: map[string]storedHupijiaoConfig{"WECHAT": {HupijiaoConfig: HupijiaoConfig{Enabled: true}}}}}
					req := CreateOrderRequest{PayChannel: channel, Product: product, TradeType: product, PresetID: 1}
					var err error
					switch kind {
					case "recharge":
						_, err = s.CreateRechargeOrder(context.Background(), Principal{ID: 1}, req)
					case "global":
						_, err = s.CreateGlobalPackageOrder(context.Background(), Principal{ID: 1}, 1, req)
					case "interface":
						_, err = s.CreateInterfacePackageOrder(context.Background(), Principal{ID: 1}, 1, req)
					case "points":
						_, err = s.CreatePointPackageOrder(context.Background(), Principal{ID: 1}, 1, req)
					}
					if !errors.Is(err, stop) || queries != 1 {
						t.Errorf("valid WeChat product was rejected as Alipay input: err=%v lookups=%d", err, queries)
					}
				})
			}
		}
	}
}

func TestWechatInvalidCreateInputsDoNotPersistOrder(t *testing.T) {
	for _, kind := range []string{"unknown type", "missing JSAPI payer", "missing MINI payer", "missing native app", "missing APP app", "missing H5 client IP"} {
		t.Run(kind, func(t *testing.T) {
			s, repo, _ := verifierConfigFixture(t)
			seed := OrderSeed{OrderNo: "BP_TRADE_TEST", UserID: 1, OrderType: "RECHARGE", PayAmount: "10.0000", TradeType: TradeTypeNative}
			switch kind {
			case "unknown type":
				seed.TradeType = "OTHER"
			case "missing JSAPI payer":
				seed.TradeType = TradeTypeJSAPI
			case "missing MINI payer":
				seed.TradeType = TradeTypeMini
			case "missing native app":
				repo.stored.AppID = ""
			case "missing APP app":
				seed.TradeType = TradeTypeAPP
				repo.stored.AppID = ""
				repo.stored.AppAppID = ""
			case "missing H5 client IP":
				seed.TradeType = TradeTypeH5
			}
			begins := 0
			s.repo = NewRepository(testdb.OpenStub(t, testdb.SQLStub{Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) {
				begins++
				return nil, errors.New("order persistence attempted")
			}}))
			_, err := s.createOrderWechat(context.Background(), seed)
			if err == nil || begins != 0 {
				t.Errorf("invalid payment input reached order persistence: %v begins=%d", err, begins)
			}
		})
	}
}

func TestWechatClientTradePayerAndInvocationContract(t *testing.T) {
	for _, partner := range []bool{false, true} {
		for _, trade := range []string{TradeTypeJSAPI, TradeTypeMini, TradeTypeAPP} {
			t.Run(strconv.FormatBool(partner)+"/"+trade, func(t *testing.T) {
				f := newWechatMessageFixture(t, partner)
				wantApp := map[string]string{TradeTypeJSAPI: f.cfg.OAAppID, TradeTypeMini: f.cfg.MiniAppID, TradeTypeAPP: f.cfg.AppAppID}[trade]
				called := 0
				f.client.httpClient = &http.Client{Transport: paymentRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					called++
					body, err := io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					var payload map[string]json.RawMessage
					if err := json.Unmarshal(body, &payload); err != nil {
						t.Fatal(err)
					}
					appKey := "appid"
					if partner {
						appKey = "sub_appid"
					}
					assertWechatJSONField(t, payload, appKey, wantApp)
					if trade != TradeTypeAPP {
						var payer map[string]string
						if err := json.Unmarshal(payload["payer"], &payer); err != nil {
							t.Fatal(err)
						}
						key := "openid"
						if partner {
							key = "sub_openid"
						}
						if len(payer) != 1 || payer[key] != "fixture-payer" {
							t.Errorf("wrong payment payer namespace: %v want %s", payer, key)
						}
					}
					response := []byte(`{"prepay_id":"prepay-fixture"}`)
					h := wechatMessageHeaders(t, f.platform.privateKey, f.cfg.PlatformPublicKeyID, response, strconv.FormatInt(time.Now().Unix(), 10))
					return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(bytes.NewReader(response))}, nil
				})}
				result, err := f.client.Create(context.Background(), PaymentOrder{OrderNo: "BP_TRADE_TEST", Subject: "Trade fixture", PayAmount: "10.0000", TradeType: trade, BuyerID: "fixture-payer"})
				if err != nil || called != 1 {
					t.Fatalf("trade request failed: %v calls=%d", err, called)
				}
				var params map[string]string
				if err := json.Unmarshal([]byte(result.PayParams), &params); err != nil {
					t.Fatalf("complete invocation parameters missing: %q err=%v", result.PayParams, err)
				}
				var app, ts, nonce, message, signature string
				if trade == TradeTypeAPP {
					app, ts, nonce, signature = params["appid"], params["timestamp"], params["noncestr"], params["sign"]
					partnerID := f.cfg.Mchid
					if partner {
						partnerID = f.cfg.SubMchid
					}
					if params["partnerid"] != partnerID || params["prepayid"] != "prepay-fixture" || params["package"] != "Sign=WXPay" {
						t.Errorf("incomplete APP invocation: %v", params)
					}
					message = app + "\n" + ts + "\n" + nonce + "\nprepay-fixture\n"
				} else {
					app, ts, nonce, signature = params["appId"], params["timeStamp"], params["nonceStr"], params["paySign"]
					if params["package"] != "prepay_id=prepay-fixture" || params["signType"] != "RSA" {
						t.Errorf("incomplete JSAPI invocation: %v", params)
					}
					message = app + "\n" + ts + "\n" + nonce + "\n" + params["package"] + "\n"
				}
				if app != wantApp || ts == "" || nonce == "" || signature == "" {
					t.Errorf("invocation identity incomplete: %v", params)
				}
				block, _ := pem.Decode([]byte(f.cfg.PrivateKeyPEM))
				key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
				if err != nil {
					t.Fatal(err)
				}
				sig, err := base64.StdEncoding.DecodeString(signature)
				if err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256([]byte(message))
				if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sig); err != nil {
					t.Errorf("invocation signature is not API v3's exact four-line message: %v", err)
				}
			})
		}
	}
}

func TestWechatCreateRequiresUsableProviderPayload(t *testing.T) {
	f := newWechatMessageFixture(t, false)
	f.client.httpClient = &http.Client{Transport: paymentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		body := []byte(`{}`)
		h := wechatMessageHeaders(t, f.platform.privateKey, f.cfg.PlatformPublicKeyID, body, strconv.FormatInt(time.Now().Unix(), 10))
		return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
	_, err := f.client.Create(context.Background(), PaymentOrder{OrderNo: "BP_TRADE_TEST", Subject: "Fixture", PayAmount: "10.0000", TradeType: TradeTypeNative})
	if err == nil {
		t.Error("signed but empty Native payment payload was accepted")
	}
}

func TestWechatH5CarriesValidatedCallerScene(t *testing.T) {
	for _, partner := range []bool{false, true} {
		for _, ip := range []string{"203.0.113.42", "2001:db8::42"} {
			t.Run(strconv.FormatBool(partner)+"/"+ip, func(t *testing.T) {
				f := newWechatMessageFixture(t, partner)
				calls := 0
				f.client.httpClient = &http.Client{Transport: paymentRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					body, err := io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					var got struct {
						Scene struct {
							IP string `json:"payer_client_ip"`
							H5 struct {
								Type string `json:"type"`
							} `json:"h5_info"`
						} `json:"scene_info"`
					}
					if err := json.Unmarshal(body, &got); err != nil {
						t.Fatal(err)
					}
					if got.Scene.IP != ip || got.Scene.H5.Type != "Wap" {
						t.Errorf("incorrect H5 payer scene: %+v", got.Scene)
					}
					response := []byte(`{"h5_url":"https://wx.tenpay.com/fixture"}`)
					h := wechatMessageHeaders(t, f.platform.privateKey, f.cfg.PlatformPublicKeyID, response, strconv.FormatInt(time.Now().Unix(), 10))
					return &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(bytes.NewReader(response))}, nil
				})}
				result, err := f.client.Create(context.Background(), PaymentOrder{OrderNo: "BP_H5_TEST", Subject: "H5", PayAmount: "10.0000", TradeType: TradeTypeH5, ClientIP: ip})
				if err != nil || calls != 1 || result.H5URL == "" {
					t.Errorf("H5 request failed: %+v %v calls=%d", result, err, calls)
				}
			})
		}
	}
}

func TestPaymentOrderBindingCannotTakeClientIPFromJSON(t *testing.T) {
	for _, key := range []string{"clientIP", "clientIp", "client_ip"} {
		t.Run(key, func(t *testing.T) {
			c := hertzapp.NewContext(0)
			c.SetClientIPFunc(func(*hertzapp.RequestContext) string { return "203.0.113.42" })
			c.Request.Header.Set("Content-Type", "application/json")
			c.Request.Header.Set("X-Forwarded-For", "1.1.1.1")
			c.Request.SetBodyString(`{"payChannel":"WECHAT","product":"H5","tradeType":"H5","` + key + `":"6.6.6.6"}`)
			var req CreateOrderRequest
			if !bindPaymentOrder(c, &req) {
				t.Fatalf("valid order binding failed: %s", c.Response.Body())
			}
			if req.ClientIP != "203.0.113.42" {
				t.Errorf("caller JSON replaced server IP: %q", req.ClientIP)
			}
			body, err := json.Marshal(req)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(body, []byte("203.0.113.42")) || bytes.Contains(body, []byte("6.6.6.6")) {
				t.Errorf("private transport context leaked into public DTO: %s", body)
			}
		})
	}
}

func TestWechatCallbackMustBeReadyBeforeCreatingOrder(t *testing.T) {
	for _, callback := range []string{"", " \t ", "https://merchant.example/notify#fragment", "https://merchant.example/notify#", "https://merchant.example/notify?session=private", "https://merchant.example/notify?", "http://merchant.example/notify", "https://user:pass@merchant.example/notify", "/notify", "https://:443/notify", "https://[]/notify"} {
		t.Run(callback, func(t *testing.T) {
			s, repo, _ := verifierConfigFixture(t)
			repo.stored.NotifyURL = callback
			begins := 0
			s.repo = NewRepository(testdb.OpenStub(t, testdb.SQLStub{Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) {
				begins++
				return nil, errors.New("database reached")
			}}))
			_, err := s.createOrderWechat(context.Background(), OrderSeed{OrderNo: "BP_BAD_NOTIFY", UserID: 1, OrderType: "RECHARGE", PayAmount: "10.0000", TradeType: TradeTypeNative})
			if err == nil || begins != 0 {
				t.Errorf("invalid callback reached order persistence: %v begins=%d", err, begins)
			}
		})
	}
}
