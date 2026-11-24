package payment

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
)

func TestWechatCachedVerificationKeyHonorsValidityWindow(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	repo := NewRepository(db)
	for _, tc := range []struct {
		name, start, end string
		want             bool
	}{
		{"unbounded public key", "NULL", "NULL", true},
		{"active certificate", "date_sub(now(),interval 1 day)", "date_add(now(),interval 1 day)", true},
		{"not yet active", "date_add(now(),interval 1 day)", "date_add(now(),interval 2 day)", false},
		{"expired", "date_sub(now(),interval 2 day)", "date_sub(now(),interval 1 day)", false},
		{"expiry boundary", "date_sub(now(),interval 1 day)", "now()", false},
		{"start boundary", "now()", "date_add(now(),interval 1 day)", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := db.Exec("insert into bp_payment_wechat_platform_cert(serial_no,public_key_cipher) values(?,?)", tc.name, "fixture-cipher"); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("update bp_payment_wechat_platform_cert set effective_time="+tc.start+",expire_time="+tc.end+" where serial_no=?", tc.name); err != nil {
				t.Fatal(err)
			}
			got, err := repo.LoadWechatPlatformCert(context.Background(), tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if (got == "fixture-cipher") != tc.want {
				t.Errorf("cache ignored validity window: cipher=%q wantUsable=%v", got, tc.want)
			}
		})
	}
}

func TestWechatAuthenticatedTransactionBoundaryPrecedesSettlement(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	f := newChannelNotificationFixture(t, db)
	wf := wechatMessageFixture{cfg: f.wechat, platform: f.platform}
	for index, name := range []string{"valid", "valid lowercase headers", "duplicate mixed-case serial", "wrong merchant", "wrong application", "wrong currency", "unknown key ID", "expired header", "missing serial", "duplicate serial", "tampered body", "unsigned"} {
		t.Run(name, func(t *testing.T) {
			userID := int64(1002100 + index)
			orderNo := seedChannelNotificationOrder(t, db, userID, ChannelWechat)
			tradeNo := fmt.Sprintf("20261009220000000000%07d", userID)
			transaction := messageTransaction(f.wechat)
			transaction["out_trade_no"] = orderNo
			transaction["transaction_id"] = tradeNo
			switch name {
			case "wrong merchant":
				transaction["mchid"] = "1900000999"
			case "wrong application":
				transaction["appid"] = "wx-foreign"
			case "wrong currency":
				transaction["amount"].(map[string]any)["currency"] = "USD"
			}
			body := encryptedWechatMessage(t, wf, transaction, "AEAD_AES_256_GCM", "TRANSACTION.SUCCESS")
			var envelope map[string]any
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatal(err)
			}
			envelope["id"] = fmt.Sprintf("verified-message-%d", userID)
			body = messageJSON(t, envelope)
			ts := time.Now().Unix()
			if name == "expired header" {
				ts -= 301
			}
			h := wechatMessageHeaders(t, f.platform.privateKey, f.wechat.PlatformPublicKeyID, body, strconv.FormatInt(ts, 10))
			if name == "unknown key ID" {
				h.Set("Wechatpay-Serial", "OTHER")
			}
			if name == "missing serial" {
				h.Del("Wechatpay-Serial")
			}
			if name == "duplicate serial" {
				h.Add("Wechatpay-Serial", "OTHER")
			}
			if name == "tampered body" {
				body = append(body, ' ')
			}
			request := hertzapp.NewContext(0)
			request.Request.Header.Set("Content-Type", "application/json")
			request.Request.SetBody(body)
			if name != "unsigned" {
				for k, values := range h {
					for _, v := range values {
						if name == "valid lowercase headers" {
							k = strings.ToLower(k)
						}
						request.Request.Header.Add(k, v)
					}
				}
			}
			if name == "duplicate mixed-case serial" {
				request.Request.Header.Add("wEcHaTpAy-SeRiAl", "OTHER")
			}
			code, response := f.deliver(t, ChannelWechat, request)
			valid := strings.HasPrefix(name, "valid")
			if !channelNotificationResponseMatches(ChannelWechat, code, response, valid) {
				t.Errorf("wrong notification acknowledgement: %d %s", code, response)
			}
			want := "CREATED"
			if valid {
				want = "PAID"
			}
			assertChannelPaymentState(t, db, orderNo, ChannelWechat, tradeNo, want)
			if valid {
				var buyer string
				if err := db.QueryRow("select coalesce(buyer_id,'') from bp_payment_order where order_no=?", orderNo).Scan(&buyer); err != nil {
					t.Fatal(err)
				}
				if buyer != "direct-payer" {
					t.Errorf("verified payer did not reach settlement: %q", buyer)
				}
			}
		})
	}
}

func TestWechatPlatformCacheUpsertSupportsOptionalDates(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	repo := NewRepository(db)
	if err := repo.SaveWechatPlatformCert(context.Background(), "optional-key", "cipher-a", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveWechatPlatformCert(context.Background(), "optional-key", "cipher-b", "2000-01-01 00:00:00", "2099-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveWechatPlatformCert(context.Background(), "optional-key", "cipher-c", "", ""); err != nil {
		t.Fatal(err)
	}
	var key, start, end string
	if err := db.QueryRow("select public_key_cipher,date_format(effective_time,'%Y-%m-%d'),date_format(expire_time,'%Y-%m-%d') from bp_payment_wechat_platform_cert where serial_no='optional-key'").Scan(&key, &start, &end); err != nil {
		t.Fatal(err)
	}
	if key != "cipher-c" || start != "2000-01-01" || end != "2099-01-01" {
		t.Errorf("optional date update lost prior bounds: %q %q %q", key, start, end)
	}
}

type rotateOnWechatConfigRead struct {
	*Repository
	afterRead func() error
}

func (r *rotateOnWechatConfigRead) WechatConfig(ctx context.Context) (storedWechatConfig, error) {
	stored, err := r.Repository.WechatConfig(ctx)
	if err != nil {
		return stored, err
	}
	if fn := r.afterRead; fn != nil {
		r.afterRead = nil
		if err := fn(); err != nil {
			return stored, err
		}
	}
	return stored, nil
}

// Deterministically interleave a second valid rotation after the first edit's
// read. This tests real storage semantics without relying on scheduler timing.
func TestWechatMaskedEditCannotUndoConcurrentVerifierRotation(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	repo := NewRepository(db)
	f := newWechatMessageFixture(t, false)
	cipher := NewConfigCipher("test-only-storage-rotation")
	enc := func(v string) string {
		out, err := cipher.Encrypt(v)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	keyCipher, v3Cipher, platformA := enc(f.cfg.PrivateKeyPEM), enc(f.cfg.APIv3Key), enc(f.cfg.PlatformPublicKey)
	cfgB := f.cfg
	cfgB.PlatformPublicKeyID = "PUB_KEY_ID_ROTATED"
	platformB := enc(newTestAlipayClient(t).cfg.AlipayPublicKey)
	for _, enabled := range []bool{true, false} {
		t.Run(strconv.FormatBool(enabled), func(t *testing.T) {
			if err := repo.SaveWechatConfig(context.Background(), f.cfg, v3Cipher, keyCipher, platformA, true, true); err != nil {
				t.Fatal(err)
			}
			interleaved := &rotateOnWechatConfigRead{Repository: repo, afterRead: func() error {
				return repo.SaveWechatConfig(context.Background(), cfgB, v3Cipher, keyCipher, platformB, true, true)
			}}
			service := NewService(repo, nil, cipher)
			service.wechatConfigRepo = interleaved
			req := SaveWechatConfigRequest{Enabled: enabled, Mode: "PRODUCTION", Mchid: f.cfg.Mchid, AppID: f.cfg.AppID, SerialNo: f.cfg.SerialNo, PlatformPublicKeyID: f.cfg.PlatformPublicKeyID, PrivateKeyPEM: maskedPaymentSecret, APIv3Key: maskedPaymentSecret, PlatformPublicKey: maskedPaymentSecret}
			err := service.saveWechatConfig(context.Background(), req)
			business, ok := apperrors.AsBusinessError(err)
			if !ok || business.StatusCode != 409 {
				t.Errorf("stale masked edit did not report reload conflict: %v", err)
			}
			stored, err := repo.WechatConfig(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if stored.PlatformPublicKeyID != cfgB.PlatformPublicKeyID || stored.PlatformPublicKeyCipher != platformB {
				t.Errorf("stale edit undid a valid key rotation: id=%q keyB=%v", stored.PlatformPublicKeyID, stored.PlatformPublicKeyCipher == platformB)
			}
		})
	}
}

func TestWechatMaskedEditRejectsChangedVerifierStorage(t *testing.T) {
	db, _ := paymentOutcomeDB(t)
	repo := NewRepository(db)
	f := newWechatMessageFixture(t, false)
	cipher := NewConfigCipher("test-only-storage-snapshot")
	enc := func(value string) string {
		value, err := cipher.Encrypt(value)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	merchant, v3, oldVerifier := enc(f.cfg.PrivateKeyPEM), enc(f.cfg.APIv3Key), enc(f.cfg.PlatformPublicKey)
	newVerifier := enc(newTestAlipayClient(t).cfg.AlipayPublicKey)
	if err := repo.SaveWechatPlatformCert(context.Background(), f.cfg.PlatformPublicKeyID, oldVerifier, "", ""); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, before, after string }{
		{"same ID with replacement key", oldVerifier, newVerifier},
		{"inline to cache", oldVerifier, ""},
		{"cache to inline", "", newVerifier},
	} {
		for _, enabled := range []bool{false, true} {
			t.Run(tc.name+"/enabled="+strconv.FormatBool(enabled), func(t *testing.T) {
				if err := repo.SaveWechatConfig(context.Background(), f.cfg, v3, merchant, tc.before, true, true); err != nil {
					t.Fatal(err)
				}
				interleaved := &rotateOnWechatConfigRead{Repository: repo, afterRead: func() error {
					return repo.SaveWechatConfig(context.Background(), f.cfg, v3, merchant, tc.after, true, true)
				}}
				service := NewService(repo, nil, cipher)
				service.wechatConfigRepo = interleaved
				req := SaveWechatConfigRequest{Enabled: enabled, Mode: "PRODUCTION", Mchid: f.cfg.Mchid, AppID: f.cfg.AppID, SerialNo: f.cfg.SerialNo, PlatformPublicKeyID: f.cfg.PlatformPublicKeyID, PrivateKeyPEM: maskedPaymentSecret, APIv3Key: maskedPaymentSecret, PlatformPublicKey: maskedPaymentSecret}
				err := service.saveWechatConfig(context.Background(), req)
				business, ok := apperrors.AsBusinessError(err)
				if !ok || business.StatusCode != 409 {
					t.Fatalf("changed verifier snapshot was saved: %v", err)
				}
				stored, err := repo.WechatConfig(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				if stored.PlatformPublicKeyID != f.cfg.PlatformPublicKeyID || stored.PlatformPublicKeyCipher != tc.after || stored.Enabled != f.cfg.Enabled {
					t.Fatal("conflicting edit overwrote the committed configuration")
				}
			})
		}
	}
}
