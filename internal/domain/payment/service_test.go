package payment

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type alipayConfigRepositoryStub struct {
	loadErr       error
	saveCalled    bool
	savedCipher   string
	savedConfig   AlipayConfig
	stored        storedAlipayConfig
	updatePrivate bool
}

type alipayNotifyRepositoryStub struct {
	err       error
	called    bool
	processed bool
	verified  bool
	message   string
}

type wechatConfigRepositoryStub struct {
	savedWechatConfig   WechatConfig
	savedPlatformCipher string
	saveCalled          bool
	stored              storedWechatConfig
	updateCert          bool
	updateSecrets       bool
}

func (s *wechatConfigRepositoryStub) WechatConfig(context.Context) (storedWechatConfig, error) {
	return s.stored, nil
}

func (s *wechatConfigRepositoryStub) SaveWechatConfig(_ context.Context, cfg WechatConfig, _, _, certCipher string, updateSecrets, updateCert bool) error {
	s.savedWechatConfig = cfg
	s.savedPlatformCipher = certCipher
	s.saveCalled = true
	s.updateSecrets = updateSecrets
	s.updateCert = updateCert
	return nil
}

func (*wechatConfigRepositoryStub) SaveWechatPayPayload(context.Context, string, PayCreateResult) error {
	return nil
}

func (*wechatConfigRepositoryStub) SettleWechatPaidOrder(context.Context, string, PayTradeStatus) error {
	return nil
}

func (*wechatConfigRepositoryStub) InsertWechatNotifyLog(context.Context, map[string]string, bool, bool, string) error {
	return nil
}

func (*wechatConfigRepositoryStub) SaveWechatPlatformCert(context.Context, string, string, string, string) error {
	return nil
}

func (*wechatConfigRepositoryStub) LoadWechatPlatformCert(context.Context, string) (string, error) {
	return "", nil
}

func (s *alipayNotifyRepositoryStub) InsertNotifyLog(_ context.Context, _ map[string]string, verified, processed bool, message string) error {
	s.called = true
	s.verified = verified
	s.processed = processed
	s.message = message
	return s.err
}

func (s *alipayConfigRepositoryStub) AlipayConfig(context.Context) (storedAlipayConfig, error) {
	return s.stored, s.loadErr
}

func (s *alipayConfigRepositoryStub) SaveAlipayConfig(_ context.Context, cfg AlipayConfig, cipher string, updatePrivate bool) error {
	s.saveCalled = true
	s.savedConfig = cfg
	s.savedCipher = cipher
	s.updatePrivate = updatePrivate
	return nil
}

func TestSaveAlipayConfigStopsWhenCurrentConfigCannotBeLoaded(t *testing.T) {
	loadErr := errors.New("payment config database unavailable")
	repo := &alipayConfigRepositoryStub{loadErr: loadErr}
	service := &Service{
		configRepo: repo,
		cipher:     NewConfigCipher(""),
	}

	err := service.saveAlipayConfig(context.Background(), SaveAlipayConfigRequest{
		AppID:         "sandbox-app-id",
		AppPrivateKey: "new-private-key",
		Enabled:       true,
	})
	if !errors.Is(err, loadErr) {
		t.Fatalf("saveAlipayConfig() error = %v, want %v", err, loadErr)
	}
	if repo.saveCalled {
		t.Fatal("configuration save must not run after the current config load fails")
	}
}

func TestValidateAlipayConfigReusesMaskedStoredPrivateKeyWithoutSaving(t *testing.T) {
	privateKey, publicKey := testAlipayKeyPair(t)
	cipher := NewConfigCipher("payment-test-secret")
	encrypted, err := cipher.Encrypt(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	repo := &alipayConfigRepositoryStub{stored: storedAlipayConfig{PrivateKeyCipher: encrypted}}
	service := &Service{configRepo: repo, cipher: cipher}

	err = service.validateAlipayConfig(context.Background(), SaveAlipayConfigRequest{
		AlipayPublicKey: publicKey,
		AppID:           "sandbox-app-id",
		AppPrivateKey:   maskedAlipayPrivateKey,
		GatewayURL:      defaultSandboxGateway,
		Mode:            "SANDBOX",
		SellerID:        "seller_1",
	})
	if err != nil {
		t.Fatalf("validateAlipayConfig() error = %v", err)
	}
	if repo.saveCalled {
		t.Fatal("configuration validation must never save database state")
	}
}

func TestValidateAlipayConfigRejectsMalformedKeys(t *testing.T) {
	privateKey, publicKey := testAlipayKeyPair(t)
	tests := []struct {
		name       string
		privateKey string
		publicKey  string
	}{
		{name: "malformed private key", privateKey: "not-a-private-key", publicKey: publicKey},
		{name: "malformed public key", privateKey: privateKey, publicKey: "not-a-public-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := &alipayConfigRepositoryStub{}
			service := &Service{configRepo: repo, cipher: NewConfigCipher("payment-test-secret")}
			err := service.validateAlipayConfig(context.Background(), SaveAlipayConfigRequest{
				AlipayPublicKey: tt.publicKey,
				AppID:           "sandbox-app-id",
				AppPrivateKey:   tt.privateKey,
				Mode:            "SANDBOX",
				SellerID:        "seller_1",
			})
			business, ok := apperrors.AsBusinessError(err)
			if !ok || business.StatusCode != consts.StatusBadRequest {
				t.Fatalf("validateAlipayConfig() error = %v, want business 400", err)
			}
			if repo.saveCalled {
				t.Fatal("invalid configuration validation must not save database state")
			}
		})
	}
}

func TestSaveAlipayConfigTreatsMaskAsUnchangedPrivateKey(t *testing.T) {
	privateKey, publicKey := testAlipayKeyPair(t)
	cipher := NewConfigCipher("payment-test-secret")
	encrypted, err := cipher.Encrypt(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	repo := &alipayConfigRepositoryStub{stored: storedAlipayConfig{PrivateKeyCipher: encrypted}}
	service := &Service{configRepo: repo, cipher: cipher}

	err = service.saveAlipayConfig(context.Background(), SaveAlipayConfigRequest{
		AlipayPublicKey: publicKey,
		AppID:           "sandbox-app-id",
		AppPrivateKey:   maskedAlipayPrivateKey,
		Enabled:         true,
		Mode:            "SANDBOX",
		SellerID:        "seller_1",
	})
	if err != nil {
		t.Fatalf("saveAlipayConfig() error = %v", err)
	}
	if !repo.saveCalled {
		t.Fatal("configuration save was not called")
	}
	if repo.updatePrivate || repo.savedCipher != encrypted {
		t.Fatalf("masked key overwrote stored cipher: update=%v cipherChanged=%v", repo.updatePrivate, repo.savedCipher != encrypted)
	}
	if repo.savedConfig.Mode != "SANDBOX" || repo.savedConfig.GatewayURL != defaultSandboxGateway {
		t.Fatalf("sandbox mode was not preserved: %#v", repo.savedConfig)
	}
}

func TestSaveWechatConfigTreatsMaskedSecretsAsUnchanged(t *testing.T) {
	privateKey, publicKey := testAlipayKeyPair(t)
	cipher := NewConfigCipher("payment-test-secret")
	encryptedV3, err := cipher.Encrypt(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	encryptedPrivateKey, err := cipher.Encrypt(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	encryptedPublicKey, err := cipher.Encrypt(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	repo := &wechatConfigRepositoryStub{stored: storedWechatConfig{
		WechatConfig:            WechatConfig{PlatformPublicKeyID: "PUB_KEY_ID_TEST"},
		APIv3KeyCipher:          encryptedV3,
		PrivateKeyCipher:        encryptedPrivateKey,
		PlatformPublicKeyCipher: encryptedPublicKey,
	}}
	service := &Service{wechatConfigRepo: repo, cipher: cipher}

	err = service.saveWechatConfig(context.Background(), SaveWechatConfigRequest{
		APIv3Key:            maskedPaymentSecret,
		AppID:               "wx-app-id",
		Enabled:             true,
		Mchid:               "1900000109",
		Mode:                "PRODUCTION",
		PrivateKeyPEM:       maskedPaymentSecret,
		SerialNo:            "0123456789ABCDEF",
		PlatformPublicKeyID: "PUB_KEY_ID_TEST",
	})
	if err != nil {
		t.Fatalf("saveWechatConfig() error = %v", err)
	}
	if !repo.saveCalled {
		t.Fatal("wechat configuration save was not called")
	}
	if repo.updateSecrets || repo.updateCert {
		t.Fatalf("masked credentials were treated as replacements: secrets=%v cert=%v", repo.updateSecrets, repo.updateCert)
	}
}

func TestNormalizeAlipayConfigRequiresSellerWhenEnabled(t *testing.T) {
	_, err := normalizeAlipayConfig(SaveAlipayConfigRequest{
		AppID:   "sandbox-app-id",
		Enabled: true,
		Mode:    "SANDBOX",
	})
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusBadRequest {
		t.Fatalf("normalizeAlipayConfig() error = %v, want business 400", err)
	}
}

func TestNormalizeAlipayConfigRestrictsGatewayAndCallbackURLs(t *testing.T) {
	tests := []struct {
		name      string
		request   SaveAlipayConfigRequest
		wantError bool
	}{
		{
			name: "official sandbox endpoints",
			request: SaveAlipayConfigRequest{
				GatewayURL: defaultSandboxGateway,
				Mode:       "SANDBOX",
				NotifyURL:  "https://api.example.com/system-api/payment/alipay/notify",
				ReturnURL:  "https://www.example.com/#/console/workspace",
			},
		},
		{
			name:      "arbitrary payment gateway",
			request:   SaveAlipayConfigRequest{Mode: "SANDBOX", GatewayURL: "https://evil.example/gateway.do"},
			wantError: true,
		},
		{
			name:      "insecure notify callback",
			request:   SaveAlipayConfigRequest{Mode: "SANDBOX", NotifyURL: "http://api.example.com/notify"},
			wantError: true,
		},
		{
			name:      "credentialed return callback",
			request:   SaveAlipayConfigRequest{Mode: "SANDBOX", ReturnURL: "https://user:pass@example.com/return"},
			wantError: true,
		},
		{
			name:      "notify callback fragment",
			request:   SaveAlipayConfigRequest{Mode: "SANDBOX", NotifyURL: "https://api.example.com/notify#ignored"},
			wantError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config, err := normalizeAlipayConfig(tt.request)
			if tt.wantError {
				business, ok := apperrors.AsBusinessError(err)
				if !ok || business.StatusCode != consts.StatusBadRequest {
					t.Fatalf("normalizeAlipayConfig() error = %v, want business 400", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeAlipayConfig() error = %v", err)
			}
			if config.GatewayURL != defaultSandboxGateway {
				t.Fatalf("gateway = %q, want %q", config.GatewayURL, defaultSandboxGateway)
			}
		})
	}
}

func TestAlipayClientWithConfigRejectsLegacyMissingSeller(t *testing.T) {
	repo := &alipayConfigRepositoryStub{stored: storedAlipayConfig{
		AlipayConfig: AlipayConfig{Enabled: true},
	}}
	service := &Service{configRepo: repo, cipher: NewConfigCipher("payment-test-secret")}

	_, _, err := service.alipayClientWithConfig(context.Background())
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusServiceUnavailable {
		t.Fatalf("alipayClientWithConfig() error = %v, want business 503", err)
	}
}

func testAlipayKeyPair(t *testing.T) (string, string) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	publicPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})
	return string(privatePEM), string(publicPEM)
}

func TestFinishNotifyRequiresDurableAuditLog(t *testing.T) {
	logErr := errors.New("notify log database unavailable")
	repo := &alipayNotifyRepositoryStub{err: logErr}
	service := &Service{notifyRepo: repo}

	result, err := service.finishNotify(context.Background(), map[string]string{"out_trade_no": "BP1"}, true, true, "success", true, nil)
	if result {
		t.Fatal("finishNotify() must not acknowledge a callback when its audit log was not saved")
	}
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusServiceUnavailable || !errors.Is(err, logErr) {
		t.Fatalf("finishNotify() error = %v, want wrapped service unavailable", err)
	}
	if !repo.called || !repo.verified || !repo.processed {
		t.Fatalf("notify audit call was not preserved: %#v", repo)
	}
}

func TestFinishNotifyReturnsOriginalResultAfterAuditLogSucceeds(t *testing.T) {
	repo := &alipayNotifyRepositoryStub{}
	service := &Service{notifyRepo: repo}
	original := apperrors.New(consts.StatusBadRequest, "invalid callback")

	result, err := service.finishNotify(context.Background(), nil, false, false, "verify failed", false, original)
	if result || !errors.Is(err, original) {
		t.Fatalf("finishNotify() = (%v, %v), want original rejection", result, err)
	}
	if !repo.called || repo.verified || repo.processed {
		t.Fatalf("unexpected notify audit call: %#v", repo)
	}
}

func TestFinishNotifyMasksAuditMessage(t *testing.T) {
	repo := &alipayNotifyRepositoryStub{}
	service := &Service{notifyRepo: repo}

	_, _ = service.finishNotify(context.Background(), nil, true, false, `database password="notify-secret" token=raw-token`, false, nil)
	if strings.Contains(repo.message, "notify-secret") || strings.Contains(repo.message, "raw-token") {
		t.Fatalf("notify audit message leaked sensitive text: %q", repo.message)
	}
}

func TestValuesToMapMasksBuyerAndMerchantPrivacyFields(t *testing.T) {
	values := url.Values{
		"buyer_open_id":  {"buyer-open-id"},
		"buyer_logon_id": {"buyer@example.com"},
		"seller_email":   {"seller@example.com"},
		"trade_no":       {"trade-1"},
	}
	got := valuesToMap(values)
	for _, key := range []string{"buyer_open_id", "buyer_logon_id", "seller_email"} {
		if got[key] != "***" {
			t.Fatalf("%s was not redacted: %#v", key, got)
		}
	}
	if got["trade_no"] != "trade-1" {
		t.Fatalf("trade number should remain searchable in audit metadata: %#v", got)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}

func TestNewOrderNoUsesCompleteCryptographicEntropy(t *testing.T) {
	fixed := time.Date(2026, 7, 10, 12, 34, 56, 0, time.Local)
	orderNo, err := newOrderNoFrom(bytes.NewReader([]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF}), fixed)
	if err != nil {
		t.Fatalf("newOrderNoFrom() error = %v", err)
	}
	if orderNo != "BP20260710123456AABBCCDDEEFF" {
		t.Fatalf("order number = %q", orderNo)
	}

	if _, err := newOrderNoFrom(io.LimitReader(bytes.NewReader([]byte{1, 2}), 2), fixed); err == nil {
		t.Fatal("short entropy read must fail")
	}
	if _, err := newOrderNoFrom(failingReader{}, fixed); err == nil {
		t.Fatal("entropy source failure must propagate")
	}
}

func TestValidateAlipayQuerySettlement(t *testing.T) {
	order := PaymentOrder{
		OrderNo:   "BP202607020001",
		PayAmount: "10.0000",
	}
	cfg := AlipayConfig{SellerID: "seller_1"}

	tests := []struct {
		name    string
		status  AlipayTradeStatus
		wantErr bool
	}{
		{
			name: "valid paid query",
			status: AlipayTradeStatus{
				OutTradeNo:  "BP202607020001",
				SellerID:    "seller_1",
				TotalAmount: "10.00",
				TradeNo:     "2026070222000000001",
				Success:     true,
			},
		},
		{
			name: "blank order number rejected",
			status: AlipayTradeStatus{
				SellerID:    "seller_1",
				TotalAmount: "10",
				TradeNo:     "2026070222000000001",
				Success:     true,
			},
			wantErr: true,
		},
		{
			name: "order number mismatch rejected",
			status: AlipayTradeStatus{
				OutTradeNo:  "BP_OTHER",
				SellerID:    "seller_1",
				TotalAmount: "10.00",
				TradeNo:     "2026070222000000001",
				Success:     true,
			},
			wantErr: true,
		},
		{
			name: "missing trade number rejected",
			status: AlipayTradeStatus{
				OutTradeNo:  "BP202607020001",
				SellerID:    "seller_1",
				TotalAmount: "10.00",
				Success:     true,
			},
			wantErr: true,
		},
		{
			name: "missing amount rejected",
			status: AlipayTradeStatus{
				OutTradeNo: "BP202607020001",
				SellerID:   "seller_1",
				TradeNo:    "2026070222000000001",
				Success:    true,
			},
			wantErr: true,
		},
		{
			name: "amount mismatch rejected",
			status: AlipayTradeStatus{
				OutTradeNo:  "BP202607020001",
				SellerID:    "seller_1",
				TotalAmount: "9.99",
				TradeNo:     "2026070222000000001",
				Success:     true,
			},
			wantErr: true,
		},
		{
			name: "missing seller rejected when configured",
			status: AlipayTradeStatus{
				OutTradeNo:  "BP202607020001",
				TotalAmount: "10.00",
				TradeNo:     "2026070222000000001",
				Success:     true,
			},
			wantErr: true,
		},
		{
			name: "seller mismatch rejected",
			status: AlipayTradeStatus{
				OutTradeNo:  "BP202607020001",
				SellerID:    "seller_2",
				TotalAmount: "10.00",
				TradeNo:     "2026070222000000001",
				Success:     true,
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAlipayQuerySettlement(order, tt.status, cfg)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestAlipaySellerMatches(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		actual     string
		want       bool
	}{
		{name: "not configured allows blank", want: true},
		{name: "not configured allows actual", actual: "seller_1", want: true},
		{name: "configured exact match", configured: "seller_1", actual: "seller_1", want: true},
		{name: "configured trims spaces", configured: " seller_1 ", actual: " seller_1 ", want: true},
		{name: "configured rejects blank actual", configured: "seller_1"},
		{name: "configured rejects mismatch", configured: "seller_1", actual: "seller_2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := alipaySellerMatches(tt.configured, tt.actual); got != tt.want {
				t.Fatalf("alipaySellerMatches() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAlipayTradeStatusHelpers(t *testing.T) {
	if !isAlipayPaidStatus("TRADE_SUCCESS") || !isAlipayPaidStatus("TRADE_FINISHED") {
		t.Fatal("expected paid statuses to match")
	}
	if isAlipayPaidStatus("TRADE_CLOSED") || isAlipayPaidStatus("WAIT_BUYER_PAY") {
		t.Fatal("expected unpaid statuses not to match paid helper")
	}
	if !isAlipayClosedStatus(" TRADE_CLOSED ") {
		t.Fatal("expected closed status to match")
	}
	if isAlipayClosedStatus("TRADE_SUCCESS") {
		t.Fatal("expected paid status not to match closed helper")
	}
}

func TestBuildInterfacePackageOrderQueryRequiresEnabledAPI(t *testing.T) {
	query := buildInterfacePackageOrderQuery()
	required := []string{
		"inner join bp_interface_api a on a.id=p.interface_id",
		"a.status=1",
		"s.status=1",
		"p.status=1",
	}
	for _, fragment := range required {
		if !strings.Contains(query, fragment) {
			t.Fatalf("buildInterfacePackageOrderQuery() missing %q in %s", fragment, query)
		}
	}
}

func TestDecodeSettlementSnapshotRejectsCorruptOrIncompleteEntitlements(t *testing.T) {
	tests := []struct {
		name      string
		orderType string
		snapshot  string
	}{
		{name: "invalid json", orderType: "GLOBAL_PACKAGE", snapshot: `{`},
		{name: "global missing valid days", orderType: "GLOBAL_PACKAGE", snapshot: `{"name":"Global","dailyLimit":0,"qpsLimit":0}`},
		{name: "global negative limit", orderType: "GLOBAL_PACKAGE", snapshot: `{"name":"Global","validDays":30,"dailyLimit":-1,"qpsLimit":0}`},
		{name: "interface missing id", orderType: "INTERFACE_PACKAGE", snapshot: `{"packageId":1,"packageName":"API","specName":"Monthly","validDays":30,"dailyLimit":0,"qpsLimit":0}`},
		{name: "point package zero points", orderType: "POINT_PACKAGE", snapshot: `{"points":0}`},
		{name: "multiple json values", orderType: "POINT_PACKAGE", snapshot: `{"points":10} {"points":20}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeSettlementSnapshot(tt.orderType, tt.snapshot); err == nil {
				t.Fatal("decodeSettlementSnapshot() must reject an invalid entitlement snapshot")
			}
		})
	}
}

func TestDecodeSettlementSnapshotAcceptsValidEntitlements(t *testing.T) {
	global, err := decodeSettlementSnapshot("GLOBAL_PACKAGE", `{"name":"Global","validDays":30,"dailyLimit":0,"qpsLimit":10}`)
	if err != nil {
		t.Fatalf("decode global snapshot: %v", err)
	}
	if global.Name != "Global" || global.ValidDays != 30 || global.QPSLimit != 10 {
		t.Fatalf("unexpected global snapshot: %#v", global)
	}

	apiPackage, err := decodeSettlementSnapshot("INTERFACE_PACKAGE", `{"packageId":2,"interfaceId":3,"packageName":"API","specName":"Monthly","validDays":30,"dailyLimit":1000,"qpsLimit":5}`)
	if err != nil {
		t.Fatalf("decode interface snapshot: %v", err)
	}
	if apiPackage.PackageID != 2 || apiPackage.InterfaceID != 3 || apiPackage.DailyLimit != 1000 {
		t.Fatalf("unexpected interface snapshot: %#v", apiPackage)
	}

	points, err := decodeSettlementSnapshot("POINT_PACKAGE", `{"points":100}`)
	if err != nil {
		t.Fatalf("decode point snapshot: %v", err)
	}
	if points.Points != 100 {
		t.Fatalf("unexpected point snapshot: %#v", points)
	}
}

func TestCreateOrderRejectsFreePaidPackage(t *testing.T) {
	service := &Service{}
	_, err := service.createOrder(context.Background(), OrderSeed{
		OrderType: "GLOBAL_PACKAGE",
		PayAmount: "0.0000",
	})
	if err == nil || !strings.Contains(err.Error(), "免费套餐") {
		t.Fatalf("free package order error = %v, want explicit free-package error", err)
	}
}
