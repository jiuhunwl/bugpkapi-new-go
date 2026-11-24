package payment

import (
	"context"
	"crypto"
	"crypto/aes"
	cryptocipher "crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/route/param"
)

// These opt-in tests use paymentOutcomeDB's explicit loopback/datadir guard and
// disposable schema. All provider keys are fixtures; no provider HTTP is used.
// They deliberately exercise real signatures, handlers and settlement SQL.
func TestSignedPaymentNotificationsKeepOrderChannelBoundary(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	fixture := newChannelNotificationFixture(t, db)
	channels := []string{ChannelAlipay, ChannelWechat, ChannelHupijiaoAlipay, ChannelHupijiaoWechat}
	targets := append(append([]string(nil), channels...), ChannelBalance)
	id := int64(993000)
	for _, source := range channels {
		phases := channelNotificationPhases(source)
		for _, target := range targets {
			for _, phase := range phases {
				t.Run(source+"/"+target+"/"+phase, func(t *testing.T) {
					id++
					orderNo := seedChannelNotificationOrder(t, db, id, target)
					tradeNo := fmt.Sprintf("20261009220000000000%06d", id)
					notifyID := fmt.Sprintf("channel-notify-%d", id)
					request := fixture.request(t, source, orderNo, tradeNo, notifyID, phase)
					code, body := fixture.deliver(t, source, request)
					matched := source == target
					wantStatus := "CREATED"
					wantProcessed := 0
					if matched {
						wantProcessed = 1
						switch phase {
						case "paid":
							wantStatus = "PAID"
						case "closed":
							wantStatus = "CLOSED"
						case "waiting":
							if source == ChannelAlipay {
								wantProcessed = 0
							}
						}
					}
					if !channelNotificationResponseMatches(source, code, body, matched) {
						t.Errorf("wrong callback acknowledgement for signed %s receipt on %s order: HTTP %d %s, accepted=%v", source, target, code, body, matched)
					}
					assertChannelPaymentState(t, db, orderNo, target, tradeNo, wantStatus)
					var verified, processed int
					var message string
					err := db.QueryRow(`select verified,processed,coalesce(message,'') from bp_payment_notify_log where order_no=? and event_type=? order by id desc limit 1`, orderNo, channelNotifyEvent(source)).Scan(&verified, &processed, &message)
					if err != nil {
						t.Fatalf("signed receipt did not reach an auditable decision: %v", err)
					}
					if verified != 1 || processed != wantProcessed {
						t.Errorf("wrong trusted audit: verified=%d processed=%d message=%q", verified, processed, message)
					}
					if !matched && !strings.Contains(strings.ToLower(message), "channel") {
						t.Errorf("receipt was not rejected for channel ownership: %q", message)
					}
					if matched && phase == "paid" {
						for attempt := 0; attempt < 2; attempt++ {
							replay := hertzapp.NewContext(0)
							request.Request.CopyTo(&replay.Request)
							replay.Params = append(param.Params(nil), request.Params...)
							if code, body := fixture.deliver(t, source, replay); !channelNotificationResponseMatches(source, code, body, true) {
								t.Errorf("same-channel replay failed: HTTP %d %s", code, body)
							}
						}
						assertChannelPaymentState(t, db, orderNo, target, tradeNo, "PAID")
					}
				})
			}
		}
	}
}

// A repository-only replay check does not cover service-level early returns.
func TestSignedPaymentNotificationsRejectForeignPaidOrders(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	fixture := newChannelNotificationFixture(t, db)
	channels := []string{ChannelAlipay, ChannelWechat, ChannelHupijiaoAlipay, ChannelHupijiaoWechat}
	id := int64(996000)
	for _, source := range channels {
		for _, target := range channels {
			if source == target {
				continue
			}
			for _, phase := range channelNotificationPhases(source) {
				t.Run(source+"/"+target+"/"+phase, func(t *testing.T) {
					id++
					orderNo := seedChannelNotificationOrder(t, db, id, target)
					tradeNo := fmt.Sprintf("20261009220000000000%06d", id)
					initial := fixture.request(t, target, orderNo, tradeNo, fmt.Sprintf("initial-paid-%d", id), "paid")
					if code, body := fixture.deliver(t, target, initial); !channelNotificationResponseMatches(target, code, body, true) {
						t.Fatalf("could not prepare a PAID fixture through the matching channel: %d %s", code, body)
					}
					assertChannelPaymentState(t, db, orderNo, target, tradeNo, "PAID")
					foreign := fixture.request(t, source, orderNo, tradeNo, fmt.Sprintf("foreign-paid-%d", id), phase)
					if code, body := fixture.deliver(t, source, foreign); !channelNotificationResponseMatches(source, code, body, false) {
						t.Errorf("already-paid order acknowledged a foreign %s receipt: %d %s", phase, code, body)
					}
					assertChannelPaymentState(t, db, orderNo, target, tradeNo, "PAID")
					var verified, processed int
					var message string
					if err := db.QueryRow(`select verified,processed,coalesce(message,'') from bp_payment_notify_log where order_no=? and event_type=? order by id desc limit 1`, orderNo, channelNotifyEvent(source)).Scan(&verified, &processed, &message); err != nil {
						t.Fatal(err)
					}
					if verified != 1 || processed != 0 || !strings.Contains(strings.ToLower(message), "channel") {
						t.Errorf("wrong foreign replay audit: verified=%d processed=%d message=%q", verified, processed, message)
					}
				})
			}
		}
	}
}

func TestWechatNotifyAuditUsesSignedEventID(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	fixture := newChannelNotificationFixture(t, db)
	orderNo := seedChannelNotificationOrder(t, db, 994001, ChannelWechat)
	tradeNo := "20261009220000000000994001"
	notifyID := "signed-wechat-event-994001"
	signed := fixture.request(t, ChannelWechat, orderNo, tradeNo, notifyID, "paid")
	// The official notification identity is in the signed JSON envelope. A
	// client-supplied unsigned header must neither replace it nor fragment
	// one verified event into multiple audit rows.
	var firstRowID int64
	for attempt, unsignedID := range []string{"", "untrusted-header-one", "untrusted-header-two"} {
		request := hertzapp.NewContext(0)
		signed.Request.CopyTo(&request.Request)
		if unsignedID != "" {
			request.Request.Header.Set("Wechatpay-Id", unsignedID)
		}
		if code, body := fixture.deliver(t, ChannelWechat, request); !channelNotificationResponseMatches(ChannelWechat, code, body, true) {
			t.Fatalf("same-channel synthetic notification failed: HTTP %d %s", code, body)
		}
		if attempt == 0 {
			if err := db.QueryRow("select id from bp_payment_notify_log where order_no=? and event_type='WECHAT_NOTIFY' order by id limit 1", orderNo).Scan(&firstRowID); err != nil {
				t.Fatal(err)
			}
		}
	}
	assertChannelPaymentState(t, db, orderNo, ChannelWechat, tradeNo, "PAID")
	var rows, attempts, verified, processed int
	var lastRowID int64
	var storedID string
	err := db.QueryRow(`select count(*),coalesce(max(id),0),coalesce(max(notify_id),''),coalesce(max(attempt_count),0),coalesce(min(verified),0),coalesce(min(processed),0) from bp_payment_notify_log where order_no=? and event_type='WECHAT_NOTIFY'`, orderNo).Scan(&rows, &lastRowID, &storedID, &attempts, &verified, &processed)
	if err != nil {
		t.Fatal(err)
	}
	if rows != 1 || lastRowID != firstRowID || storedID != notifyID || attempts != 3 || verified != 1 || processed != 1 {
		t.Fatalf("unsigned header or missing header controlled trusted audit identity: rows=%d firstRow=%d lastRow=%d id=%q attempts=%d verified=%d processed=%d", rows, firstRowID, lastRowID, storedID, attempts, verified, processed)
	}
}

func TestNativeSettlementRequiresMatchingOrderChannel(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	fixture := newChannelNotificationFixture(t, db)
	repo := NewRepository(db)
	id := int64(995000)
	for _, source := range []string{ChannelAlipay, ChannelWechat} {
		for _, target := range []string{ChannelAlipay, ChannelWechat, ChannelHupijiaoAlipay, ChannelHupijiaoWechat} {
			if source == target {
				continue
			}
			for _, alreadyPaid := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/paid=%v", source, target, alreadyPaid), func(t *testing.T) {
					id++
					orderNo := seedChannelNotificationOrder(t, db, id, target)
					tradeNo := fmt.Sprintf("20261009220000000000%06d", id)
					wantStatus := "CREATED"
					if alreadyPaid {
						request := fixture.request(t, target, orderNo, tradeNo, fmt.Sprintf("original-channel-%d", id), "paid")
						if code, body := fixture.deliver(t, target, request); !channelNotificationResponseMatches(target, code, body, true) {
							t.Fatalf("could not prepare a PAID fixture through the matching channel: %d %s", code, body)
						}
						wantStatus = "PAID"
					}
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					var err error
					if source == ChannelAlipay {
						err = repo.SettlePaidOrder(ctx, orderNo, AlipayTradeStatus{OutTradeNo: orderNo, TradeNo: tradeNo, TotalAmount: "10.00", Success: true})
					} else {
						err = repo.SettleWechatPaidOrder(ctx, orderNo, PayTradeStatus{OutTradeNo: orderNo, TradeNo: tradeNo, Total: "10.0000", TradeState: "SUCCESS", Success: true})
					}
					if !errors.Is(err, ErrPaymentChannelMismatch) {
						t.Errorf("native %s settlement must reject %s order before grants/replay: %v", source, target, err)
					}
					assertChannelPaymentState(t, db, orderNo, target, tradeNo, wantStatus)
				})
			}
		}
	}
}

type channelNotificationFixture struct {
	handler  *Handler
	alipay   *AlipayClient
	platform *AlipayClient
	wechat   WechatConfig
	hupijiao map[string]HupijiaoConfig
}

func newChannelNotificationFixture(t *testing.T, db *sql.DB) *channelNotificationFixture {
	t.Helper()
	repo := NewRepository(db)
	cipher := NewConfigCipher("test-only-payment-channel-contract-secret")
	encrypt := func(value string) string {
		out, err := cipher.Encrypt(value)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	fixture := &channelNotificationFixture{
		handler:  NewHandler(NewService(repo, nil, cipher)),
		alipay:   newTestAlipayClient(t),
		platform: newTestAlipayClient(t),
		hupijiao: make(map[string]HupijiaoConfig),
	}
	cfg := fixture.alipay.cfg
	cfg.AppID, cfg.SellerID = "2026000000000000", "2088000000000000"
	if err := repo.SaveAlipayConfig(context.Background(), cfg, encrypt(cfg.AppPrivateKey), true); err != nil {
		t.Fatal(err)
	}
	merchantPrivate, _ := testAlipayKeyPair(t)
	fixture.wechat = WechatConfig{
		Enabled: true, Mode: "PRODUCTION", Mchid: "1900000001", AppID: "wx0000000000000001",
		APIv3Key: "0123456789abcdef0123456789abcdef", PrivateKeyPEM: merchantPrivate,
		SerialNo: "TEST_MERCHANT_SERIAL", PlatformPublicKeyID: "TEST_PLATFORM_SERIAL",
		PlatformPublicKey: fixture.platform.cfg.AlipayPublicKey,
	}
	wc := fixture.wechat
	if err := repo.SaveWechatConfig(context.Background(), wc, encrypt(wc.APIv3Key), encrypt(wc.PrivateKeyPEM), encrypt(wc.PlatformPublicKey), true, true); err != nil {
		t.Fatal(err)
	}
	for _, channel := range []string{ChannelHupijiaoAlipay, ChannelHupijiaoWechat} {
		method := "ALIPAY"
		if channel == ChannelHupijiaoWechat {
			method = "WECHAT"
		}
		cfg := HupijiaoConfig{Enabled: true, Method: method, AppID: "fixture-" + method, AppSecret: "test-only-secret-" + method, CreateURL: defaultHupijiaoCreateURL, QueryURL: defaultHupijiaoQueryURL}
		if err := repo.SaveHupijiaoConfig(context.Background(), cfg, encrypt(cfg.AppSecret), true); err != nil {
			t.Fatal(err)
		}
		fixture.hupijiao[channel] = cfg
	}
	return fixture
}

func seedChannelNotificationOrder(t *testing.T, db *sql.DB, id int64, channel string) string {
	t.Helper()
	seeded := seedPaymentOutcome(t, db, id, "RECHARGE", channel)
	orderNo := fmt.Sprintf("BP20261009123456%012X", id)
	if _, err := db.Exec("update bp_payment_order set order_no=? where order_no=?", orderNo, seeded); err != nil {
		t.Fatal(err)
	}
	return orderNo
}

func (f *channelNotificationFixture) request(t *testing.T, channel, orderNo, tradeNo, notifyID, phase string) *hertzapp.RequestContext {
	t.Helper()
	request := hertzapp.NewContext(0)
	request.Request.Header.SetMethod("POST")
	request.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	switch channel {
	case ChannelAlipay:
		values := validNotifyEnvelope()
		values.Set("out_trade_no", orderNo)
		values.Set("trade_no", tradeNo)
		values.Set("notify_id", notifyID)
		values.Set("trade_status", map[string]string{"paid": "TRADE_SUCCESS", "closed": "TRADE_CLOSED", "waiting": "WAIT_BUYER_PAY"}[phase])
		values.Set("sign", mustSignParams(t, f.alipay, values))
		request.Request.SetBodyString(values.Encode())
	case ChannelWechat:
		if phase != "paid" {
			t.Fatal("this fixture only models official WeChat payment-success notifications")
		}
		plain := channelJSON(t, map[string]any{
			"appid": f.wechat.AppID, "mchid": f.wechat.Mchid, "out_trade_no": orderNo,
			"transaction_id": tradeNo, "trade_type": "NATIVE", "trade_state": "SUCCESS",
			"trade_state_desc": "支付成功", "success_time": time.Now().UTC().Format(time.RFC3339),
			"payer":  map[string]string{"openid": "fixture-open-id"},
			"amount": map[string]any{"total": 1000, "payer_total": 1000, "currency": "CNY", "payer_currency": "CNY"},
		})
		block, err := aes.NewCipher([]byte(f.wechat.APIv3Key))
		if err != nil {
			t.Fatal(err)
		}
		gcm, err := cryptocipher.NewGCM(block)
		if err != nil {
			t.Fatal(err)
		}
		var randomNonce [6]byte
		if _, err := rand.Read(randomNonce[:]); err != nil {
			t.Fatal(err)
		}
		nonce := hex.EncodeToString(randomNonce[:])
		associatedData := "transaction"
		sealed := gcm.Seal(nil, []byte(nonce), plain, []byte(associatedData))
		body := channelJSON(t, map[string]any{
			"id": notifyID, "create_time": time.Now().UTC().Format(time.RFC3339),
			"resource_type": "encrypt-resource", "event_type": "TRANSACTION.SUCCESS", "summary": "支付成功",
			"resource": map[string]string{"algorithm": "AEAD_AES_256_GCM", "ciphertext": base64.StdEncoding.EncodeToString(sealed), "nonce": nonce, "associated_data": associatedData, "original_type": "transaction"},
		})
		timestamp := strconv.FormatInt(time.Now().Unix(), 10)
		headerNonce := "channel-notification-header"
		digest := sha256.Sum256([]byte(timestamp + "\n" + headerNonce + "\n" + string(body) + "\n"))
		signature, err := rsa.SignPKCS1v15(rand.Reader, f.platform.privateKey, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		request.Request.Header.Set("Content-Type", "application/json")
		request.Request.Header.Set("Wechatpay-Timestamp", timestamp)
		request.Request.Header.Set("Wechatpay-Nonce", headerNonce)
		request.Request.Header.Set("Wechatpay-Signature", base64.StdEncoding.EncodeToString(signature))
		request.Request.Header.Set("Wechatpay-Serial", f.wechat.PlatformPublicKeyID)
		request.Request.SetBody(body)
	case ChannelHupijiaoAlipay, ChannelHupijiaoWechat:
		cfg := f.hupijiao[channel]
		values := map[string]string{
			"appid": cfg.AppID, "trade_order_id": orderNo, "transaction_id": tradeNo,
			"open_order_id": notifyID, "total_fee": "10.00", "order_title": "Fixture recharge",
			"status": map[string]string{"paid": "OD", "closed": "CD", "waiting": "WP"}[phase],
		}
		values["hash"] = HupijiaoSign(values, cfg.AppSecret)
		form := make(url.Values, len(values))
		for key, value := range values {
			form.Set(key, value)
		}
		request.Request.SetBodyString(form.Encode())
		request.Params = param.Params{{Key: "method", Value: cfg.Method}}
	default:
		t.Fatalf("unsupported notification fixture channel: %q", channel)
	}
	return request
}

func (f *channelNotificationFixture) deliver(t *testing.T, channel string, request *hertzapp.RequestContext) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch channel {
	case ChannelAlipay:
		f.handler.alipayNotify(ctx, request)
	case ChannelWechat:
		f.handler.wechatNotify(ctx, request)
	case ChannelHupijiaoAlipay, ChannelHupijiaoWechat:
		f.handler.hupijiaoNotify(ctx, request)
	default:
		t.Fatalf("unsupported delivery channel: %q", channel)
	}
	return request.Response.StatusCode(), string(request.Response.Body())
}

func channelJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func channelNotificationPhases(channel string) []string {
	if channel == ChannelWechat {
		return []string{"paid"}
	}
	return []string{"paid", "closed", "waiting"}
}

func channelNotificationResponseMatches(channel string, status int, body string, accepted bool) bool {
	wantStatus, wantBody := 400, "failure"
	if accepted {
		wantStatus, wantBody = 200, "success"
	}
	if status != wantStatus {
		return false
	}
	if channel == ChannelWechat {
		var result struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal([]byte(body), &result); err != nil {
			return false
		}
		wantCode := "FAIL"
		if accepted {
			wantCode = "SUCCESS"
		}
		return result.Code == wantCode
	}
	return body == wantBody
}

func channelNotifyEvent(channel string) string {
	switch channel {
	case ChannelAlipay:
		return "ALIPAY_NOTIFY"
	case ChannelWechat:
		return "WECHAT_NOTIFY"
	default:
		return "HUPIJIAO_NOTIFY"
	}
}

func assertChannelPaymentState(t *testing.T, db *sql.DB, orderNo, channel, tradeNo, wantStatus string) {
	t.Helper()
	var status, storedChannel, balance, alipayTrade, otherTrade string
	var points int64
	var wallet, global, api int
	err := db.QueryRow(`select o.status,o.pay_channel,cast(u.balance as char),u.points,
		(select count(*) from bp_wallet_transaction w where w.user_id=u.id),
		(select count(*) from bp_user_package_global g where g.user_id=u.id),
		(select count(*) from bp_user_package_interface i where i.user_id=u.id),
		coalesce(o.alipay_trade_no,''),coalesce(o.channel_trade_no,'')
		from bp_payment_order o inner join bp_user u on u.id=o.user_id where o.order_no=?`, orderNo).Scan(&status, &storedChannel, &balance, &points, &wallet, &global, &api, &alipayTrade, &otherTrade)
	if err != nil {
		t.Fatal(err)
	}
	wantBalance, wantWallet := "0.0000", 0
	wantAlipayTrade, wantOtherTrade := "", ""
	if wantStatus == "PAID" {
		wantBalance, wantWallet = "12.0000", 1
		if channel == ChannelAlipay {
			wantAlipayTrade = tradeNo
		} else {
			wantOtherTrade = tradeNo
		}
	}
	if status != wantStatus || storedChannel != channel || balance != wantBalance || wallet != wantWallet || points != 0 || global != 0 || api != 0 || alipayTrade != wantAlipayTrade || otherTrade != wantOtherTrade {
		t.Errorf("unexpected durable payment state: status=%s channel=%s balance=%s points=%d wallet=%d global=%d api=%d alipayTrade=%q otherTrade=%q; want status=%s channel=%s balance=%s wallet=%d alipayTrade=%q otherTrade=%q", status, storedChannel, balance, points, wallet, global, api, alipayTrade, otherTrade, wantStatus, channel, wantBalance, wantWallet, wantAlipayTrade, wantOtherTrade)
	}
}
