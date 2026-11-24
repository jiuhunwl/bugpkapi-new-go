package payment

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHupijiaoSignUsesSortedNonEmptyFieldsAndExcludesHash(t *testing.T) {
	params := map[string]string{
		"version":        "1.1",
		"appid":          "app-1",
		"trade_order_id": "BP202608240000000000000000",
		"total_fee":      "12.50",
		"empty":          "",
		"hash":           "old-signature",
	}
	got := HupijiaoSign(params, "secret")
	canonical := "appid=app-1&total_fee=12.50&trade_order_id=BP202608240000000000000000&version=1.1secret"
	sum := md5.Sum([]byte(canonical))
	want := hex.EncodeToString(sum[:])
	if got != want {
		t.Fatalf("HupijiaoSign() = %q, want %q (canonical %q)", got, want, canonical)
	}
}

func TestHupijiaoClientCreateMapsQRCodeAndVerifiesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/payment/do.html" {
			t.Fatalf("path = %q, want /payment/do.html", r.URL.Path)
		}
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		for _, key := range []string{"version", "appid", "trade_order_id", "total_fee", "title", "time", "notify_url", "nonce_str", "hash"} {
			if strings.TrimSpace(request[key]) == "" {
				t.Errorf("request field %q is empty: %#v", key, request)
			}
		}
		if request["type"] != "" {
			t.Errorf("alipay request must not contain wechat type: %#v", request)
		}
		response := map[string]string{
			"errcode":    "0",
			"errmsg":     "success!",
			"url_qrcode": "https://pay.example/qr/1",
		}
		response["hash"] = HupijiaoSign(response, "secret")
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	client, err := NewHupijiaoClient(HupijiaoConfig{
		Enabled:    true,
		Method:     "ALIPAY",
		AppID:      "app-1",
		AppSecret:  "secret",
		CreateURL:  server.URL + "/payment/do.html",
		QueryURL:   server.URL + "/payment/query.html",
		NotifyURL:  "https://merchant.example/payment/notify",
		ReturnURL:  "https://merchant.example/payment/return",
		WAPURL:     "https://merchant.example",
		WAPName:    "BugPk API",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewHupijiaoClient() error = %v", err)
	}
	result, err := client.Create(context.Background(), PaymentOrder{
		OrderNo:   "BP202608240000000000000000",
		PayAmount: "12.5000",
		Subject:   "API points",
		Product:   "PAGE",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if result.CodeURL != "https://pay.example/qr/1" {
		t.Fatalf("CodeURL = %q", result.CodeURL)
	}
}

func TestHupijiaoClientQueryMapsStatuses(t *testing.T) {
	for _, tt := range []struct {
		status  string
		success bool
		state   string
	}{
		{status: "OD", success: true, state: "OD"},
		{status: "WP", success: false, state: "WP"},
		{status: "CD", success: false, state: "CD"},
	} {
		t.Run(tt.status, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]string
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Fatalf("decode query request: %v", err)
				}
				if request["out_trade_order"] != "BP202608240000000000000000" {
					t.Fatalf("out_trade_order = %q", request["out_trade_order"])
				}
				if strings.TrimSpace(request["hash"]) == "" {
					t.Fatal("query request hash is empty")
				}
				response := map[string]any{
					"errcode": "0",
					"errmsg":  "success!",
					"data": map[string]string{
						"status":        tt.status,
						"open_order_id": "hp-order-1",
					},
				}
				response["hash"] = HupijiaoSign(map[string]string{
					"errcode":       "0",
					"errmsg":        "success!",
					"status":        tt.status,
					"open_order_id": "hp-order-1",
				}, "secret")
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			client, err := NewHupijiaoClient(HupijiaoConfig{
				Enabled:    true,
				Method:     "ALIPAY",
				AppID:      "app-1",
				AppSecret:  "secret",
				CreateURL:  server.URL + "/payment/do.html",
				QueryURL:   server.URL + "/payment/query.html",
				HTTPClient: server.Client(),
			})
			if err != nil {
				t.Fatalf("NewHupijiaoClient() error = %v", err)
			}
			got, err := client.Query(context.Background(), "BP202608240000000000000000")
			if err != nil {
				t.Fatalf("Query() error = %v", err)
			}
			if got.TradeState != tt.state || got.Success != tt.success {
				t.Fatalf("Query() = %#v, want state=%q success=%v", got, tt.state, tt.success)
			}
		})
	}
}

func TestHupijiaoVerifyNotifyRejectsTamperedAmount(t *testing.T) {
	client, err := NewHupijiaoClient(HupijiaoConfig{Enabled: true, Method: "ALIPAY", AppID: "app-1", AppSecret: "secret"})
	if err != nil {
		t.Fatalf("NewHupijiaoClient() error = %v", err)
	}
	values := map[string]string{
		"trade_order_id": "BP202608240000000000000000",
		"total_fee":      "12.50",
		"transaction_id": "tx-1",
		"status":         "OD",
		"appid":          "app-1",
		"time":           "1720000000",
		"nonce_str":      "nonce",
	}
	values["hash"] = HupijiaoSign(values, "secret")
	values["total_fee"] = "99.00"
	if _, err := client.VerifyNotify(values); err == nil {
		t.Fatal("VerifyNotify() accepted a tampered amount")
	}
}

// 2026-11-04 实测生产：查单接口成功响应不携带 hash，金额字段为
// total_amount 而非 total_fee。此前的实现把「缺 hash」当错误返回
// （missing hupijiao response hash），导致 PAYING 订单主动同步必 500。
func TestHupijiaoClientQueryAcceptsUnsignedResponseWithTotalAmount(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"errcode": "0",
			"errmsg":  "",
			"data": map[string]any{
				"open_order_id":   "20309522654",
				"total_amount":    "50.00",
				"title":           "余额充值-50元",
				"status":          "WP",
				"transaction_id":  nil,
				"out_trade_order": "BP202608240000000000000000",
			},
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	client, err := NewHupijiaoClient(HupijiaoConfig{
		Enabled:    true,
		Method:     "WECHAT",
		AppID:      "app-1",
		AppSecret:  "secret",
		CreateURL:  server.URL + "/payment/do.html",
		QueryURL:   server.URL + "/payment/query.html",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewHupijiaoClient() error = %v", err)
	}
	got, err := client.Query(context.Background(), "BP202608240000000000000000")
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if got.TradeState != "WP" || got.Success {
		t.Fatalf("TradeState/Success = %q/%v, want WP/false", got.TradeState, got.Success)
	}
	if got.Total != "50.00" {
		t.Fatalf("Total = %q, want 50.00 (total_amount field)", got.Total)
	}
}

// 有 hash 时仍必须验签：篡改金额的签名响应要被拒绝。
func TestHupijiaoClientQueryRejectsTamperedSignedResponse(t *testing.T) {
	signedFields := map[string]string{"errcode": "0", "errmsg": ""}
	hash := HupijiaoSign(signedFields, "secret")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := map[string]any{
			"errcode": "0",
			"errmsg":  "",
			"hash":    hash, // 签名不含 data 字段，金额属于被篡改后的 payload
			"data": map[string]any{
				"status":       "OD",
				"total_amount": "99.00",
			},
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	client, err := NewHupijiaoClient(HupijiaoConfig{
		Enabled:    true,
		Method:     "WECHAT",
		AppID:      "app-1",
		AppSecret:  "secret",
		CreateURL:  server.URL + "/payment/do.html",
		QueryURL:   server.URL + "/payment/query.html",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewHupijiaoClient() error = %v", err)
	}
	if _, err := client.Query(context.Background(), "BP202608240000000000000000"); err == nil ||
		!strings.Contains(err.Error(), "invalid hupijiao response signature") {
		t.Fatalf("Query() error = %v, want invalid hupijiao response signature", err)
	}
}

// 2026-11-05 生产实测：虎皮椒微信的 url_qrcode 是「显示二维码的页面」，
// 手机微信扫码后只会再看到一张码，必须二次扫码才能付款。真实支付串藏在
// 该地址 302 的 data 参数里，下单时要解出来直接生成二维码。
func TestHupijiaoClientCreateResolvesWechatQRPageToNativeCode(t *testing.T) {
	const nativeCode = "weixin://wxpay/bizpayurl?pr=5QQN80xixEZnxBSL"
	var probes atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/payment/do.html":
			response := map[string]string{
				"errcode":    "0",
				"errmsg":     "success!",
				"url_qrcode": server.URL + "/payments/wechat/qrcode_v3?id=20309557338",
				"url":        "https://api.xunhupay.com/payments/wechat/index?id=20309557338",
			}
			response["hash"] = HupijiaoSign(response, "secret")
			_ = json.NewEncoder(w).Encode(response)
		case "/payments/wechat/qrcode_v3":
			probes.Add(1)
			w.Header().Set("Location", server.URL+"/qrcode/201906189360.html?data="+
				base64.StdEncoding.EncodeToString([]byte(nativeCode))+"&time=1793845295")
			w.WriteHeader(http.StatusFound)
		default:
			t.Errorf("unexpected probe path %q", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client, err := NewHupijiaoClient(HupijiaoConfig{
		Enabled:    true,
		Method:     "WECHAT",
		AppID:      "app-1",
		AppSecret:  "secret",
		CreateURL:  server.URL + "/payment/do.html",
		QueryURL:   server.URL + "/payment/query.html",
		NotifyURL:  "https://merchant.example/payment/notify",
		HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("NewHupijiaoClient() error = %v", err)
	}
	result, err := client.Create(context.Background(), PaymentOrder{
		OrderNo:   "BP202611051015219E6593A155EA",
		PayAmount: "50.0000",
		Subject:   "余额充值-50元",
		Product:   "PAGE",
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if result.CodeURL != nativeCode {
		t.Fatalf("CodeURL = %q, want native %q", result.CodeURL, nativeCode)
	}
	if probes.Load() != 1 {
		t.Fatalf("qr page probes = %d, want 1", probes.Load())
	}
}

// 解析不出来时必须退回原始 URL：宁可维持旧行为，也不能让下单失败。
func TestHupijiaoClientCreateKeepsQRURLWhenProbeCannotResolve(t *testing.T) {
	for _, tt := range []struct {
		name     string
		location string
	}{
		{name: "cross origin", location: "https://evil.example/qrcode/1.html?data=AAE="},
		{name: "no redirect", location: ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/payment/do.html":
					response := map[string]string{"errcode": "0", "errmsg": "success!", "url_qrcode": server.URL + "/qr"}
					response["hash"] = HupijiaoSign(response, "secret")
					_ = json.NewEncoder(w).Encode(response)
				case "/qr":
					if tt.location != "" {
						w.Header().Set("Location", tt.location)
						w.WriteHeader(http.StatusFound)
					}
				default:
					t.Errorf("unexpected probe path %q", r.URL.Path)
				}
			}))
			defer server.Close()
			client, err := NewHupijiaoClient(HupijiaoConfig{
				Enabled:    true,
				Method:     "WECHAT",
				AppID:      "app-1",
				AppSecret:  "secret",
				CreateURL:  server.URL + "/payment/do.html",
				QueryURL:   server.URL + "/payment/query.html",
				NotifyURL:  "https://merchant.example/payment/notify",
				HTTPClient: server.Client(),
			})
			if err != nil {
				t.Fatalf("NewHupijiaoClient() error = %v", err)
			}
			result, err := client.Create(context.Background(), PaymentOrder{
				OrderNo: "BP202611051015219E6593A155EA", PayAmount: "50.0000", Subject: "余额充值", Product: "PAGE",
			})
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if result.CodeURL != server.URL+"/qr" {
				t.Fatalf("CodeURL = %q, want untouched %q", result.CodeURL, server.URL+"/qr")
			}
		})
	}
}

// base64 的 '+' 在 query 里是字面量：url.QueryUnescape 会把它换成空格，
// 解出来的支付串必错，所以自己切 RawQuery 再用 PathUnescape。
func TestHupijiaoQRDataParamKeepsBase64PlusSign(t *testing.T) {
	const encoded = "d2VpeGluOi8vd3hwYXkvYml6cGF5dXJsP3ByPX5+fg=="
	if got := hupijiaoQRDataParam("nonce_str=1&data=" + encoded + "&time=2"); got != encoded {
		t.Fatalf("hupijiaoQRDataParam() = %q, want %q", got, encoded)
	}
	if got := hupijiaoQRDataParam("data=" + strings.ReplaceAll(encoded, "+", "%2B")); got != encoded {
		t.Fatalf("percent encoded data = %q, want %q", got, encoded)
	}
	payload, ok := hupijiaoQRPayloadFromLocation("https://api.xunhupay.com/qrcode/1.html?data="+encoded, []string{"weixin://"})
	if !ok || !strings.HasPrefix(payload, "weixin://") {
		t.Fatalf("hupijiaoQRPayloadFromLocation() = %q/%v", payload, ok)
	}
}

// 支付串前缀必须与渠道匹配：微信码不能是 https 页面，反之亦然。
func TestHupijiaoQRPayloadRejectsUnexpectedScheme(t *testing.T) {
	page := base64.StdEncoding.EncodeToString([]byte("https://api.xunhupay.com/payments/wechat/index?id=1"))
	if _, ok := hupijiaoQRPayloadFromLocation("https://api.xunhupay.com/qrcode/1.html?data="+page, hupijiaoQRNativePrefixes["WECHAT"]); ok {
		t.Fatal("WECHAT accepted an https page payload")
	}
	if _, ok := hupijiaoQRPayloadFromLocation("https://api.xunhupay.com/qrcode/1.html?data="+page, hupijiaoQRNativePrefixes["ALIPAY"]); ok {
		t.Fatal("ALIPAY accepted a non qr.alipay.com payload")
	}
}
