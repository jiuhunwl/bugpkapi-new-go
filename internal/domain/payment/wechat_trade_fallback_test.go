package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestWechatTradeAppFallbackKeepsPayerAndInvocationIdentity(t *testing.T) {
	for _, choice := range []string{"direct base", "partner sub", "partner sp", "explicit sp"} {
		for _, trade := range []string{TradeTypeJSAPI, TradeTypeMini, TradeTypeAPP} {
			t.Run(choice+"/"+trade, func(t *testing.T) {
				partner := choice != "direct base"
				f := newWechatMessageFixture(t, partner)
				f.cfg.OAAppID, f.cfg.MiniAppID, f.cfg.AppAppID = "", "", ""
				wantApp, wantPayer, wantMerchant := "wx-base", "openid", "1900000001"
				if partner {
					wantApp, wantPayer, wantMerchant = "wx-sub", "sub_openid", "1900000002"
				}
				if choice == "partner sp" {
					f.cfg.SubAppID = ""
					wantApp, wantPayer = "wx-sp", "sp_openid"
				}
				if choice == "explicit sp" {
					f.cfg.OAAppID, f.cfg.MiniAppID, f.cfg.AppAppID = "wx-sp", "wx-sp", "wx-sp"
					wantApp, wantPayer = "wx-sp", "sp_openid"
				}
				calls := 0
				f.cfg.HTTPClient = &http.Client{Transport: paymentRoundTripFunc(func(request *http.Request) (*http.Response, error) {
					calls++
					body := decodeWechatCreateBody(t, request)
					if partner {
						assertWechatJSONField(t, body, "sp_appid", "wx-sp")
						if wantPayer == "sub_openid" {
							assertWechatJSONField(t, body, "sub_appid", wantApp)
						} else if _, ok := body["sub_appid"]; ok {
							t.Fatal("service-provider OpenID was paired with a sub-app ID")
						}
					} else {
						assertWechatJSONField(t, body, "appid", wantApp)
					}
					if trade != TradeTypeAPP {
						var payer map[string]string
						if err := json.Unmarshal(body["payer"], &payer); err != nil {
							t.Fatal(err)
						}
						if len(payer) != 1 || payer[wantPayer] != "fixture-payer" {
							t.Fatalf("payer namespace mismatch: %v", payer)
						}
					} else if _, ok := body["payer"]; ok {
						t.Fatal("APP prepay unexpectedly included an OpenID payer")
					}
					return wechatCreateResponse(t, f, "{\"prepay_id\":\"prepay-fallback\"}"), nil
				})}
				client, err := NewWechatClient(f.cfg)
				if err != nil {
					t.Fatal(err)
				}
				if client.httpClient != f.cfg.HTTPClient {
					t.Fatal("test transport not installed; refusing live gateway")
				}
				result, err := client.Create(context.Background(), PaymentOrder{OrderNo: fmt.Sprintf("BP_FALLBACK_%s", trade), Subject: "Fallback", PayAmount: "10.0000", TradeType: trade, BuyerID: "fixture-payer"})
				if err != nil || calls != 1 {
					t.Fatalf("fallback create failed: %v calls=%d", err, calls)
				}
				assertWechatCreateInvocation(t, result.PayParams, trade, wantApp, wantMerchant, "prepay-fallback", &client.privateKey.PublicKey)
			})
		}
	}
}
