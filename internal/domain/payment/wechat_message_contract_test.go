package payment

import (
	"bytes"
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

type wechatMessageFixture struct {
	cfg      WechatConfig
	platform *AlipayClient
	client   *WechatClient
}

func newWechatMessageFixture(t *testing.T, partner bool) wechatMessageFixture {
	t.Helper()
	merchant, platform := newTestAlipayClient(t), newTestAlipayClient(t)
	cfg := WechatConfig{NotifyURL: "https://merchant.example/notify/wechat", Enabled: true, Mode: "PRODUCTION", Mchid: "1900000001", AppID: "wx-base", OAAppID: "wx-oa", MiniAppID: "wx-mini", AppAppID: "wx-app", APIv3Key: strings.Repeat("k", 32), PrivateKeyPEM: merchant.cfg.AppPrivateKey, SerialNo: "MERCHANT_SERIAL", PlatformPublicKeyID: "PUB_KEY_ID_TEST", PlatformPublicKey: platform.cfg.AlipayPublicKey}
	if partner {
		cfg.SpMchid, cfg.SubMchid, cfg.SpAppID, cfg.SubAppID = "1900000001", "1900000002", "wx-sp", "wx-sub"
	}
	client, err := NewWechatClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return wechatMessageFixture{cfg: cfg, platform: platform, client: client}
}

func wechatMessageHeaders(t *testing.T, key *rsa.PrivateKey, serial string, body []byte, timestamp string) http.Header {
	t.Helper()
	nonce := "message-fixture-nonce"
	digest := sha256.Sum256([]byte(timestamp + "\n" + nonce + "\n" + string(body) + "\n"))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return http.Header{"Wechatpay-Serial": []string{serial}, "Wechatpay-Timestamp": []string{timestamp}, "Wechatpay-Nonce": []string{nonce}, "Wechatpay-Signature": []string{base64.StdEncoding.EncodeToString(signature)}}
}

func messageTransaction(cfg WechatConfig) map[string]any {
	result := map[string]any{"out_trade_no": "BP_MESSAGE_TEST", "transaction_id": "message-trade", "trade_type": "NATIVE", "trade_state": "SUCCESS", "amount": map[string]any{"total": int64(1000), "currency": "CNY"}, "payer": map[string]any{"openid": "direct-payer"}}
	if cfg.SpMchid != "" {
		result["sp_mchid"], result["sub_mchid"], result["sp_appid"], result["sub_appid"] = cfg.SpMchid, cfg.SubMchid, cfg.SpAppID, cfg.SubAppID
		result["payer"] = map[string]any{"sp_openid": "sp-payer", "sub_openid": "sub-payer"}
	} else {
		result["mchid"], result["appid"] = cfg.Mchid, cfg.AppID
	}
	return result
}

func messageJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func queryWechatMessage(t *testing.T, f wechatMessageFixture, body []byte, headers http.Header) (PayTradeStatus, error) {
	t.Helper()
	f.client.httpClient = &http.Client{Transport: paymentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: headers, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})}
	return f.client.Query(context.Background(), "BP_MESSAGE_TEST")
}

func TestWechatSuccessResponseMustBeAuthenticated(t *testing.T) {
	f := newWechatMessageFixture(t, false)
	body := messageJSON(t, messageTransaction(f.cfg))
	other := newTestAlipayClient(t)
	for _, name := range []string{"valid", "unsigned", "tampered body", "wrong key", "unknown serial", "missing timestamp", "expired", "future", "overflow timestamp", "bad signature encoding", "duplicate serial", "missing nonce"} {
		t.Run(name, func(t *testing.T) {
			now := time.Now().Unix()
			ts := strconv.FormatInt(now, 10)
			sent := append([]byte(nil), body...)
			if name == "expired" {
				ts = strconv.FormatInt(now-301, 10)
			}
			if name == "future" {
				ts = strconv.FormatInt(now+301, 10)
			}
			if name == "overflow timestamp" {
				ts = "9223372036854775807"
			}
			key := f.platform.privateKey
			if name == "wrong key" {
				key = other.privateKey
			}
			h := wechatMessageHeaders(t, key, f.cfg.PlatformPublicKeyID, body, ts)
			switch name {
			case "unsigned":
				h = make(http.Header)
			case "tampered body":
				sent = bytes.Replace(sent, []byte("1000"), []byte("2000"), 1)
			case "unknown serial":
				h.Set("Wechatpay-Serial", "PUB_KEY_ID_OTHER")
			case "missing timestamp":
				h.Del("Wechatpay-Timestamp")
			case "missing nonce":
				h.Del("Wechatpay-Nonce")
			case "bad signature encoding":
				h.Set("Wechatpay-Signature", "not-base64")
			case "duplicate serial":
				h.Add("Wechatpay-Serial", "PUB_KEY_ID_OTHER")
			}
			result, err := queryWechatMessage(t, f, sent, h)
			if name == "valid" {
				if err != nil || !result.Success || result.Total != "10.0000" {
					t.Fatalf("valid signed response failed: %+v %v", result, err)
				}
			} else if err == nil || result.Success {
				t.Errorf("untrusted success response escaped validation: %+v %v", result, err)
			}
		})
	}
}

func TestWechatOperationsRequireConfiguredVerifierBeforeTransport(t *testing.T) {
	f := newWechatMessageFixture(t, false)
	for _, missing := range []string{"key", "id"} {
		t.Run(missing, func(t *testing.T) {
			cfg := f.cfg
			if missing == "key" {
				cfg.PlatformPublicKey = ""
			} else {
				cfg.PlatformPublicKeyID = ""
			}
			client, err := NewWechatClient(cfg)
			if err != nil {
				return
			}
			calls := 0
			client.httpClient = &http.Client{Transport: paymentRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code_url":"weixin://fixture"}`))}, nil
			})}
			_, err = client.Create(context.Background(), PaymentOrder{OrderNo: "BP_MESSAGE_TEST", Subject: "Fixture", TradeType: TradeTypeNative, PayAmount: "10.0000"})
			if err == nil || calls != 0 {
				t.Errorf("unverifiable payment request reached network boundary: err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestWechatCloseVerifiesSignedEmptyResponse(t *testing.T) {
	f := newWechatMessageFixture(t, false)
	for _, signed := range []bool{false, true} {
		t.Run(strconv.FormatBool(signed), func(t *testing.T) {
			headers := make(http.Header)
			if signed {
				headers = wechatMessageHeaders(t, f.platform.privateKey, f.cfg.PlatformPublicKeyID, nil, strconv.FormatInt(time.Now().Unix(), 10))
			}
			f.client.httpClient = &http.Client{Transport: paymentRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 204, Header: headers, Body: io.NopCloser(strings.NewReader(""))}, nil
			})}
			err := f.client.Close(context.Background(), "BP_MESSAGE_TEST")
			if (err == nil) != signed {
				t.Errorf("204 response authentication mismatch: signed=%v err=%v", signed, err)
			}
		})
	}
}

func TestWechatSignedTransactionBindsMerchantApplicationAndCurrency(t *testing.T) {
	for _, partner := range []bool{false, true} {
		f := newWechatMessageFixture(t, partner)
		cases := []string{"valid", "missing amount", "negative amount", "zero amount", "missing currency", "wrong currency", "wrong order"}
		if partner {
			cases = append(cases, "wrong provider merchant", "wrong sub merchant", "wrong provider app", "wrong sub app", "missing provider merchant", "alternate sub app")
		} else {
			cases = append(cases, "wrong merchant", "missing merchant", "wrong app", "missing app", "alternate app")
		}
		for _, name := range cases {
			t.Run(strconv.FormatBool(partner)+"/"+name, func(t *testing.T) {
				transaction := messageTransaction(f.cfg)
				amount := transaction["amount"].(map[string]any)
				switch name {
				case "missing amount":
					delete(amount, "total")
				case "negative amount":
					amount["total"] = int64(-1000)
				case "zero amount":
					amount["total"] = int64(0)
				case "missing currency":
					delete(amount, "currency")
				case "wrong currency":
					amount["currency"] = "USD"
				case "wrong order":
					transaction["out_trade_no"] = "BP_OTHER"
				case "wrong merchant":
					transaction["mchid"] = "1900000999"
				case "missing merchant":
					delete(transaction, "mchid")
				case "wrong app":
					transaction["appid"] = "wx-other"
				case "missing app":
					delete(transaction, "appid")
				case "alternate app":
					transaction["appid"] = f.cfg.OAAppID
				case "wrong provider merchant":
					transaction["sp_mchid"] = "1900000999"
				case "missing provider merchant":
					delete(transaction, "sp_mchid")
				case "wrong sub merchant":
					transaction["sub_mchid"] = "1900000999"
				case "wrong provider app":
					transaction["sp_appid"] = "wx-other"
				case "wrong sub app":
					transaction["sub_appid"] = "wx-other"
				case "alternate sub app":
					transaction["sub_appid"] = f.cfg.MiniAppID
				}
				body := messageJSON(t, transaction)
				h := wechatMessageHeaders(t, f.platform.privateKey, f.cfg.PlatformPublicKeyID, body, strconv.FormatInt(time.Now().Unix(), 10))
				result, err := queryWechatMessage(t, f, body, h)
				want := name == "valid" || strings.HasPrefix(name, "alternate")
				if want {
					if err != nil || !result.Success {
						t.Errorf("valid configured transaction failed: %+v %v", result, err)
					}
				} else if err == nil || result.Success {
					t.Errorf("foreign/malformed transaction accepted: %+v %v", result, err)
				}
			})
		}
	}
}

func encryptedWechatMessage(t *testing.T, f wechatMessageFixture, transaction map[string]any, algorithm, eventType string) []byte {
	t.Helper()
	block, err := aes.NewCipher([]byte(f.cfg.APIv3Key))
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	var random [6]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	nonce := hex.EncodeToString(random[:])
	aad := "transaction"
	sealed := gcm.Seal(nil, []byte(nonce), messageJSON(t, transaction), []byte(aad))
	return messageJSON(t, map[string]any{"id": "verified-message-id", "create_time": "2020-01-01T00:00:00+08:00", "resource_type": "encrypt-resource", "event_type": eventType, "resource": map[string]string{"algorithm": algorithm, "ciphertext": base64.StdEncoding.EncodeToString(sealed), "nonce": nonce, "associated_data": aad}})
}

func TestWechatNotificationReusesMessageAndTransactionValidation(t *testing.T) {
	for _, partner := range []bool{false, true} {
		f := newWechatMessageFixture(t, partner)
		for _, name := range []string{"valid old event fresh delivery", "wrong merchant", "wrong app", "wrong currency", "unknown serial", "expired delivery", "wrong algorithm", "wrong event type", "non-success transaction"} {
			t.Run(strconv.FormatBool(partner)+"/"+name, func(t *testing.T) {
				transaction := messageTransaction(f.cfg)
				algorithm, eventType := "AEAD_AES_256_GCM", "TRANSACTION.SUCCESS"
				ts := time.Now().Unix()
				switch name {
				case "wrong merchant":
					if partner {
						transaction["sub_mchid"] = "foreign"
					} else {
						transaction["mchid"] = "foreign"
					}
				case "wrong app":
					if partner {
						transaction["sp_appid"] = "foreign"
					} else {
						transaction["appid"] = "foreign"
					}
				case "wrong currency":
					transaction["amount"].(map[string]any)["currency"] = "USD"
				case "expired delivery":
					ts -= 301
				case "wrong algorithm":
					algorithm = "OTHER"
				case "wrong event type":
					eventType = "REFUND.SUCCESS"
				case "non-success transaction":
					transaction["trade_state"] = "CLOSED"
				}
				body := encryptedWechatMessage(t, f, transaction, algorithm, eventType)
				h := wechatMessageHeaders(t, f.platform.privateKey, f.cfg.PlatformPublicKeyID, body, strconv.FormatInt(ts, 10))
				if name == "unknown serial" {
					h.Set("Wechatpay-Serial", "OTHER")
				}
				headers := make(map[string]string)
				for k, v := range h {
					headers[k] = v[0]
				}
				result, err := f.client.VerifyNotify(context.Background(), body, headers)
				if name == "valid old event fresh delivery" {
					if err != nil || result == nil || !result.Success {
						t.Errorf("valid retry rejected by event creation age: %+v %v", result, err)
					}
				} else if err == nil || result != nil {
					t.Errorf("invalid notification accepted: %+v %v", result, err)
				}
			})
		}
	}
}

func TestWechatMessageTimestampHasExactBoundaries(t *testing.T) {
	now := time.Unix(1800000000, 0)
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{"1800000000", true}, {"1799999701", true}, {"1800000299", true},
		{"1799999700", false}, {"1800000300", false}, {"9223372036854775807", false}, {"-9223372036854775808", false},
		{"", false}, {"not-a-time", false}, {"+1800000000", false}, {"01800000000", false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if err := validateWechatMessageTimestamp(tc.raw, now); (err == nil) != tc.want {
				t.Errorf("timestamp result: %v wantValid=%v", err, tc.want)
			}
		})
	}
}

func TestWechatUnsignedErrorResponseCannotProduceSuccessfulResult(t *testing.T) {
	f := newWechatMessageFixture(t, false)
	f.client.httpClient = &http.Client{Transport: paymentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 400, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"code":"PARAM_ERROR","message":"rejected"}`))}, nil
	})}
	result, err := f.client.Query(context.Background(), "BP_MESSAGE_TEST")
	if err == nil || result.Success || result.TradeNo != "" {
		t.Errorf("error response became trusted business data: %+v %v", result, err)
	}
}

func TestWechatRequestAdvertisesAcceptablePlatformIdentity(t *testing.T) {
	f := newWechatMessageFixture(t, false)
	f.client.httpClient = &http.Client{Transport: paymentRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		selected := req.Header.Get("Wechatpay-Serial")
		if selected != f.cfg.PlatformPublicKeyID {
			t.Errorf("request did not declare verification identity: %q", selected)
			selected = "PLATFORM_DEFAULT_OTHER"
		}
		if selected == f.cfg.SerialNo {
			t.Error("merchant certificate serial confused with platform verification ID")
		}
		headers := wechatMessageHeaders(t, f.platform.privateKey, selected, nil, strconv.FormatInt(time.Now().Unix(), 10))
		return &http.Response{StatusCode: 204, Header: headers, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	if err := f.client.Close(context.Background(), "BP_MESSAGE_TEST"); err != nil {
		t.Errorf("platform-selected response could not be verified: %v", err)
	}
}

func TestWechatPayerIdentityUsesTheAuthenticatedApplicationScope(t *testing.T) {
	for _, kind := range []string{"direct", "partner sub-app", "partner provider-app"} {
		t.Run(kind, func(t *testing.T) {
			f := newWechatMessageFixture(t, kind != "direct")
			tx := messageTransaction(f.cfg)
			want := "direct-payer"
			if kind == "partner sub-app" {
				want = "sub-payer"
			}
			if kind == "partner provider-app" {
				delete(tx, "sub_appid")
				want = "sp-payer"
			}
			body := messageJSON(t, tx)
			h := wechatMessageHeaders(t, f.platform.privateKey, f.cfg.PlatformPublicKeyID, body, strconv.FormatInt(time.Now().Unix(), 10))
			got, err := queryWechatMessage(t, f, body, h)
			if err != nil || got.BuyerID != want {
				t.Errorf("query payer scope mismatch: %+v %v, want %s", got, err, want)
			}
			encrypted := encryptedWechatMessage(t, f, tx, "AEAD_AES_256_GCM", "TRANSACTION.SUCCESS")
			h = wechatMessageHeaders(t, f.platform.privateKey, f.cfg.PlatformPublicKeyID, encrypted, strconv.FormatInt(time.Now().Unix(), 10))
			headers := map[string]string{}
			for k, v := range h {
				headers[k] = v[0]
			}
			notify, err := f.client.VerifyNotify(context.Background(), encrypted, headers)
			if err != nil || notify == nil || notify.BuyerID != want {
				t.Errorf("notification payer scope mismatch: %+v %v, want %s", notify, err, want)
			}
		})
	}
}

func TestWechatAuthenticatedUnpaidQueryRemainsNonSettling(t *testing.T) {
	for _, partner := range []bool{false, true} {
		for _, state := range []string{"NOTPAY", "CLOSED"} {
			t.Run(strconv.FormatBool(partner)+"/"+state, func(t *testing.T) {
				f := newWechatMessageFixture(t, partner)
				tx := messageTransaction(f.cfg)
				tx["trade_state"] = state
				delete(tx, "transaction_id")
				body := messageJSON(t, tx)
				h := wechatMessageHeaders(t, f.platform.privateKey, f.cfg.PlatformPublicKeyID, body, strconv.FormatInt(time.Now().Unix(), 10))
				got, err := queryWechatMessage(t, f, body, h)
				if err != nil || got.Success || got.TradeState != state || got.OutTradeNo != "BP_MESSAGE_TEST" || got.Total != "10.0000" {
					t.Errorf("valid non-paid query was rejected or reported paid: %+v %v", got, err)
				}
			})
		}
	}
}
