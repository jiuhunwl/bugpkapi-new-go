package payment

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"bugpk-api/internal/pkg/testdb"
)

type wechatCacheConfigStub struct {
	*wechatConfigRepositoryStub
	cached   string
	cacheErr error
}

func (r *wechatCacheConfigStub) LoadWechatPlatformCert(context.Context, string) (string, error) {
	return r.cached, r.cacheErr
}

func verifierConfigFixture(t *testing.T) (*Service, *wechatCacheConfigStub, SaveWechatConfigRequest) {
	t.Helper()
	f := newWechatMessageFixture(t, false)
	cipher := NewConfigCipher("test-only-config-verifier-secret")
	enc := func(s string) string {
		v, err := cipher.Encrypt(s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	repo := &wechatCacheConfigStub{wechatConfigRepositoryStub: &wechatConfigRepositoryStub{stored: storedWechatConfig{WechatConfig: f.cfg, PrivateKeyCipher: enc(f.cfg.PrivateKeyPEM), APIv3KeyCipher: enc(f.cfg.APIv3Key), PlatformPublicKeyCipher: enc(f.cfg.PlatformPublicKey)}}}
	req := SaveWechatConfigRequest{Enabled: true, Mode: "PRODUCTION", Mchid: f.cfg.Mchid, AppID: f.cfg.AppID, SerialNo: f.cfg.SerialNo, PlatformPublicKeyID: f.cfg.PlatformPublicKeyID, PrivateKeyPEM: maskedPaymentSecret, APIv3Key: maskedPaymentSecret, PlatformPublicKey: maskedPaymentSecret}
	return &Service{wechatConfigRepo: repo, cipher: cipher}, repo, req
}

func TestWechatConfigPreservesMaskedVerifierWithoutReparsingMask(t *testing.T) {
	for _, save := range []bool{false, true} {
		t.Run(map[bool]string{false: "validate", true: "save"}[save], func(t *testing.T) {
			s, repo, req := verifierConfigFixture(t)
			var err error
			if save {
				err = s.saveWechatConfig(context.Background(), req)
			} else {
				err = s.validateWechatConfig(context.Background(), req)
			}
			if err != nil {
				t.Fatalf("masked configured verifier was treated as key material: %v", err)
			}
			if repo.saveCalled != save || repo.updateCert || repo.updateSecrets {
				t.Errorf("mask unexpectedly overwrote stored secrets: save=%v cert=%v secrets=%v", repo.saveCalled, repo.updateCert, repo.updateSecrets)
			}
		})
	}
}

func TestWechatEnabledConfigRequiresUsableVerifier(t *testing.T) {
	for _, name := range []string{"missing ID", "missing key", "cached key", "bad cached key", "cache failure"} {
		t.Run(name, func(t *testing.T) {
			s, repo, req := verifierConfigFixture(t)
			req.PlatformPublicKey = ""
			switch name {
			case "missing ID":
				req.PlatformPublicKeyID = ""
			case "missing key":
				repo.stored.PlatformPublicKeyCipher = ""
			case "cached key":
				repo.cached = repo.stored.PlatformPublicKeyCipher
				repo.stored.PlatformPublicKeyCipher = ""
			case "bad cached key":
				repo.stored.PlatformPublicKeyCipher = ""
				repo.cached = "not-an-encrypted-key"
			case "cache failure":
				repo.stored.PlatformPublicKeyCipher = ""
				repo.cacheErr = errors.New("cache read unavailable")
			}
			err := s.saveWechatConfig(context.Background(), req)
			want := name == "cached key"
			if (err == nil) != want || repo.saveCalled != want {
				t.Errorf("unusable verification configuration persisted: %s err=%v saved=%v", name, err, repo.saveCalled)
			}
		})
	}
}

func TestWechatUnconfiguredVerifierCannotCreateLocalOrder(t *testing.T) {
	s, repo, _ := verifierConfigFixture(t)
	repo.stored.PlatformPublicKeyCipher = ""
	repo.stored.PlatformPublicKeyID = ""
	begins := 0
	s.repo = NewRepository(testdb.OpenStub(t, testdb.SQLStub{Begin: func(context.Context, driver.TxOptions) (driver.Tx, error) {
		begins++
		return nil, errors.New("order database reached")
	}}))
	_, err := s.createOrderWechat(context.Background(), OrderSeed{OrderNo: "BP_VERIFIER_TEST", UserID: 1, OrderType: "RECHARGE", PayAmount: "10.0000", TradeType: TradeTypeNative})
	if err == nil || begins != 0 {
		t.Errorf("order persistence started before verifier readiness: err=%v begins=%d", err, begins)
	}
}

func TestWechatRejectsWeakPlatformVerificationKey(t *testing.T) {
	f := newWechatMessageFixture(t, false)
	_, weak := generateAlipayKeyMaterial(t, 1024, true)
	f.cfg.PlatformPublicKey = weak
	if _, err := NewWechatClient(f.cfg); err == nil {
		t.Error("weak platform verification key accepted")
	}
}

func TestWechatEnabledConfigStillAllowsCompleteDirectKey(t *testing.T) {
	s, repo, req := verifierConfigFixture(t)
	key, err := s.cipher.Decrypt(repo.stored.PlatformPublicKeyCipher)
	if err != nil {
		t.Fatal(err)
	}
	req.PlatformPublicKey = key
	if err := s.saveWechatConfig(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !repo.updateCert || !repo.saveCalled {
		t.Error("real verifier replacement was not saved")
	}
	if strings.TrimSpace(key) == "" {
		t.Fatal("invalid fixture key")
	}
}

func TestWechatVerifierRotationNeverPairsNewIDWithOldMaskedKey(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			s, repo, req := verifierConfigFixture(t)
			req.Enabled = enabled
			req.PlatformPublicKeyID = "PUB_KEY_ID_NEW"
			if err := s.validateWechatConfig(context.Background(), req); err == nil {
				t.Error("validation accepted new ID with the old masked key")
			}
			newKey := newTestAlipayClient(t).cfg.AlipayPublicKey
			repo.cached, _ = s.cipher.Encrypt(newKey)
			if err := s.saveWechatConfig(context.Background(), req); err == nil || repo.saveCalled {
				t.Errorf("ID rotation reused old stored cipher: err=%v saved=%v", err, repo.saveCalled)
			}
		})
	}
}

func TestWechatVerifierRotationPersistsTheValidatedKey(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit replacement", true: "cache-backed configuration"}[cached], func(t *testing.T) {
			s, repo, req := verifierConfigFixture(t)
			req.PlatformPublicKeyID = "PUB_KEY_ID_NEW"
			key := newTestAlipayClient(t).cfg.AlipayPublicKey
			if cached {
				repo.stored.PlatformPublicKeyCipher = ""
				repo.cached, _ = s.cipher.Encrypt(key)
			} else {
				req.PlatformPublicKey = key
			}
			if err := s.saveWechatConfig(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if repo.savedWechatConfig.PlatformPublicKeyID != "PUB_KEY_ID_NEW" {
				t.Error("rotated metadata was not saved")
			}
			if cached {
				if repo.savedPlatformCipher != "" || repo.updateCert {
					t.Error("cache-backed rotation retained an unrelated inline key")
				}
			} else {
				actual, err := s.cipher.Decrypt(repo.savedPlatformCipher)
				if err != nil || actual != strings.TrimSpace(key) || !repo.updateCert {
					t.Errorf("saved key differs from validated key: err=%v changed=%v", err, repo.updateCert)
				}
			}
		})
	}
}
