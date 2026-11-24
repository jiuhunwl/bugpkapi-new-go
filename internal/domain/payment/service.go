package payment

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bugpk-api/internal/domain/notification"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/decimalvalue"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/i18n"
	"bugpk-api/internal/pkg/listquery"
	"bugpk-api/internal/pkg/masking"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type Service struct {
	repo               *Repository
	configRepo         alipayConfigRepository
	wechatConfigRepo   wechatConfigRepository
	hupijiaoConfigRepo hupijiaoConfigRepository
	notifyRepo         alipayNotifyRepository
	authz              *rbac.Service
	cipher             *ConfigCipher
	notifier           paymentNotifier
	localizer          i18n.Localizer
}

// SetLocalizer 接入翻译字典；nil 时充值档位等名称保持默认语言原文。
func (s *Service) SetLocalizer(localizer i18n.Localizer) {
	s.localizer = localizer
}

// paymentNotifier 复用全局邮件 outbox（与 ticket/content 域相同的注入模式）。
type paymentNotifier interface {
	Enqueue(context.Context, notification.EnqueueRequest) error
}

// SetNotifier 注入支付成功邮件通知发送器；未注入时静默跳过。
func (s *Service) SetNotifier(notifier paymentNotifier) {
	if s != nil {
		s.notifier = notifier
	}
}

// notifyPaymentPaid 在订单完成结算后向下单用户发送支付成功邮件。
// 幂等保障：以订单号为去重键，outbox 层对重复投递天然去重；任何失败
// 只记录日志，绝不影响支付主流程。
func (s *Service) notifyPaymentPaid(ctx context.Context, orderNo string) {
	if s == nil || s.notifier == nil || strings.TrimSpace(orderNo) == "" {
		return
	}
	order, err := s.repo.OrderByNo(ctx, orderNo, 0, true)
	if err != nil {
		return
	}
	if order.Status != "PAID" {
		return
	}
	userID, err := strconv.ParseInt(order.UserID, 10, 64)
	if err != nil || userID <= 0 {
		return
	}
	email, err := s.repo.UserEmailByID(ctx, userID)
	if err != nil || strings.TrimSpace(email) == "" {
		return
	}
	enqueueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.notifier.Enqueue(enqueueCtx, notification.EnqueueRequest{
		DedupeKey: "payment.paid:" + order.OrderNo,
		Language:  "",
		Payload: map[string]any{
			"amount":  normalizeAmount(order.PayAmount),
			"orderNo": order.OrderNo,
			"paidAt":  order.PaidTime,
			"subject": order.Subject,
		},
		Recipient:   email,
		TemplateKey: notification.TemplatePaymentPaid,
	}); err != nil {
		return
	}
}

type alipayConfigRepository interface {
	AlipayConfig(context.Context) (storedAlipayConfig, error)
	SaveAlipayConfig(context.Context, AlipayConfig, string, bool) error
}

type alipayNotifyRepository interface {
	InsertNotifyLog(context.Context, map[string]string, bool, bool, string) error
}

type hupijiaoConfigRepository interface {
	HupijiaoConfig(context.Context, string) (storedHupijiaoConfig, error)
	HupijiaoConfigs(context.Context) ([]storedHupijiaoConfig, error)
	SaveHupijiaoConfig(context.Context, HupijiaoConfig, string, bool) error
	PaymentMethods(context.Context) ([]PaymentMethod, error)
	SavePaymentMethods(context.Context, []PaymentMethod) error
}

const (
	maskedAlipayPrivateKey  = "******"
	maskedPaymentSecret     = "******"
	maxRechargePresetName   = 128
	maxRechargePresetSortNo = 1_000_000
	maxPaymentMethodSortNo  = 1_000_000
)

var maxPaymentMoney = new(big.Rat).SetFrac(big.NewInt(999_999_999_999), big.NewInt(10_000))

func NewService(repo *Repository, authz *rbac.Service, cipher *ConfigCipher) *Service {
	return &Service{repo: repo, configRepo: repo, wechatConfigRepo: repo, hupijiaoConfigRepo: repo, notifyRepo: repo, authz: authz, cipher: cipher}
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	principal, err := s.authz.Authenticate(ctx, token)
	if err != nil {
		return Principal{}, err
	}
	return Principal{ID: principal.ID, Username: principal.Username, Roles: principal.Roles, TokenVersion: principal.TokenVersion}, nil
}

func (s *Service) AlipayConfig(ctx context.Context, principal Principal) (AlipayConfig, error) {
	if err := s.require(ctx, principal, "payment:config:view"); err != nil {
		return AlipayConfig{}, err
	}
	cfg, err := s.configRepo.AlipayConfig(ctx)
	if err != nil {
		return AlipayConfig{}, err
	}
	out := cfg.AlipayConfig
	if cfg.PrivateKeyCipher != "" {
		out.AppPrivateKey = "******"
	}
	return out, nil
}

func (s *Service) SaveAlipayConfig(ctx context.Context, principal Principal, req SaveAlipayConfigRequest) error {
	if err := s.require(ctx, principal, "payment:config:update"); err != nil {
		return err
	}
	return s.saveAlipayConfig(ctx, req)
}

func (s *Service) ValidateAlipayConfig(ctx context.Context, principal Principal, req SaveAlipayConfigRequest) error {
	if err := s.require(ctx, principal, "payment:config:update"); err != nil {
		return err
	}
	return s.validateAlipayConfig(ctx, req)
}

func (s *Service) saveAlipayConfig(ctx context.Context, req SaveAlipayConfigRequest) error {
	current, err := s.configRepo.AlipayConfig(ctx)
	if err != nil {
		return err
	}
	cfg, err := normalizeAlipayConfig(req)
	if err != nil {
		return err
	}
	submittedPrivateKey := normalizeSubmittedPrivateKey(req.AppPrivateKey)
	updatePrivate := submittedPrivateKey != ""
	privateCipher := current.PrivateKeyCipher
	if updatePrivate {
		privateCipher, err = s.cipher.Encrypt(submittedPrivateKey)
		if err != nil {
			return mapSecretErr(err)
		}
	}
	if cfg.Enabled {
		if privateCipher == "" {
			return apperrors.New(consts.StatusBadRequest, "支付宝应用私钥未配置")
		}
		if strings.TrimSpace(cfg.AlipayPublicKey) == "" {
			return apperrors.New(consts.StatusBadRequest, "支付宝公钥未配置")
		}
		privateKey := submittedPrivateKey
		if privateKey == "" {
			privateKey, err = s.cipher.Decrypt(privateCipher)
			if err != nil {
				return mapSecretErr(err)
			}
		}
		validateCfg := cfg
		validateCfg.AppPrivateKey = privateKey
		if err := validateAlipaySigning(validateCfg); err != nil {
			return apperrors.Wrap(consts.StatusBadRequest, "支付宝配置不可用", err)
		}
	}
	return s.configRepo.SaveAlipayConfig(ctx, cfg, privateCipher, updatePrivate)
}

func (s *Service) validateAlipayConfig(ctx context.Context, req SaveAlipayConfigRequest) error {
	current, err := s.configRepo.AlipayConfig(ctx)
	if err != nil {
		return err
	}
	cfg, err := normalizeAlipayConfig(req)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.AppID) == "" {
		return apperrors.New(consts.StatusBadRequest, "支付宝 appId 不能为空")
	}
	if strings.TrimSpace(cfg.SellerID) == "" {
		return apperrors.New(consts.StatusBadRequest, "支付宝商户 sellerId 不能为空")
	}
	if strings.TrimSpace(cfg.AlipayPublicKey) == "" {
		return apperrors.New(consts.StatusBadRequest, "支付宝公钥未配置")
	}
	privateKey := normalizeSubmittedPrivateKey(req.AppPrivateKey)
	if privateKey == "" {
		if current.PrivateKeyCipher == "" {
			return apperrors.New(consts.StatusBadRequest, "支付宝应用私钥未配置")
		}
		privateKey, err = s.cipher.Decrypt(current.PrivateKeyCipher)
		if err != nil {
			return mapSecretErr(err)
		}
	}
	cfg.AppPrivateKey = privateKey
	if err := validateAlipaySigning(cfg); err != nil {
		return apperrors.Wrap(consts.StatusBadRequest, "支付宝配置不可用", err)
	}
	return nil
}

func validateAlipaySigning(cfg AlipayConfig) error {
	client, err := NewAlipayClient(cfg)
	if err != nil {
		return err
	}
	params, err := client.commonParams("alipay.trade.query", `{"out_trade_no":"BP_CONFIG_VALIDATION"}`, false, false)
	if err != nil {
		return err
	}
	if strings.TrimSpace(params.Get("sign")) == "" || params.Get("sign_type") != "RSA2" {
		return errors.New("alipay RSA2 signature was not generated")
	}
	return nil
}

func normalizeSubmittedPrivateKey(value string) string {
	return normalizeSubmittedSecret(value)
}

func normalizeSubmittedSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == maskedAlipayPrivateKey || value == maskedPaymentSecret {
		return ""
	}
	return value
}

func (s *Service) HupijiaoConfig(ctx context.Context, principal Principal) (HupijiaoConfigView, error) {
	if err := s.require(ctx, principal, "payment:config:view"); err != nil {
		return HupijiaoConfigView{}, err
	}
	configs, err := s.hupijiaoConfigRepo.HupijiaoConfigs(ctx)
	if err != nil {
		return HupijiaoConfigView{}, err
	}
	view := HupijiaoConfigView{}
	for _, stored := range configs {
		item := hupijiaoChannelConfigView(stored)
		switch item.Method {
		case "ALIPAY":
			view.Alipay = item
		case "WECHAT":
			view.Wechat = item
		}
	}
	return view, nil
}

func hupijiaoChannelConfigView(stored storedHupijiaoConfig) HupijiaoChannelConfig {
	secret := ""
	if strings.TrimSpace(stored.AppSecretCipher) != "" {
		secret = maskedPaymentSecret
	}
	return HupijiaoChannelConfig{
		AppID:       stored.AppID,
		AppSecret:   secret,
		CallbackURL: stored.CallbackURL,
		CreateURL:   stored.CreateURL,
		Enabled:     stored.Enabled,
		Method:      stored.Method,
		NotifyURL:   stored.NotifyURL,
		QueryURL:    stored.QueryURL,
		ReturnURL:   stored.ReturnURL,
		WAPName:     stored.WAPName,
		WAPURL:      stored.WAPURL,
	}
}

func (s *Service) SaveHupijiaoConfig(ctx context.Context, principal Principal, req SaveHupijiaoConfigRequest) error {
	if err := s.require(ctx, principal, "payment:hupijiao:update"); err != nil {
		return err
	}
	return s.saveHupijiaoConfig(ctx, req)
}

func (s *Service) ValidateHupijiaoConfig(ctx context.Context, principal Principal, req SaveHupijiaoConfigRequest) error {
	if err := s.require(ctx, principal, "payment:hupijiao:update"); err != nil {
		return err
	}
	return s.validateHupijiaoConfig(ctx, req)
}

func (s *Service) saveHupijiaoConfig(ctx context.Context, req SaveHupijiaoConfigRequest) error {
	cfg, err := normalizeHupijiaoConfig(req)
	if err != nil {
		return err
	}
	current, err := s.hupijiaoConfigRepo.HupijiaoConfig(ctx, cfg.Method)
	if err != nil {
		return err
	}
	submitted := normalizeSubmittedPrivateKey(req.AppSecret)
	secretCipher := current.AppSecretCipher
	updateSecret := submitted != ""
	if updateSecret {
		secretCipher, err = s.cipher.Encrypt(submitted)
		if err != nil {
			return mapSecretErr(err)
		}
	}
	if cfg.Enabled {
		if strings.TrimSpace(secretCipher) == "" {
			return apperrors.New(consts.StatusBadRequest, "虎皮椒 appsecret 未配置")
		}
		secret := submitted
		if secret == "" {
			secret, err = s.cipher.Decrypt(secretCipher)
			if err != nil {
				return mapSecretErr(err)
			}
		}
		validateCfg := cfg
		validateCfg.AppSecret = secret
		if _, err := NewHupijiaoClient(validateCfg); err != nil {
			return apperrors.Wrap(consts.StatusBadRequest, "虎皮椒配置不可用", err)
		}
	}
	return s.hupijiaoConfigRepo.SaveHupijiaoConfig(ctx, cfg, secretCipher, updateSecret)
}

func (s *Service) validateHupijiaoConfig(ctx context.Context, req SaveHupijiaoConfigRequest) error {
	cfg, err := normalizeHupijiaoConfig(req)
	if err != nil {
		return err
	}
	current, err := s.hupijiaoConfigRepo.HupijiaoConfig(ctx, cfg.Method)
	if err != nil {
		return err
	}
	secret := normalizeSubmittedPrivateKey(req.AppSecret)
	if secret == "" {
		if strings.TrimSpace(current.AppSecretCipher) == "" {
			return apperrors.New(consts.StatusBadRequest, "虎皮椒 appsecret 未配置")
		}
		secret, err = s.cipher.Decrypt(current.AppSecretCipher)
		if err != nil {
			return mapSecretErr(err)
		}
	}
	cfg.AppSecret = secret
	if _, err := NewHupijiaoClient(cfg); err != nil {
		return apperrors.Wrap(consts.StatusBadRequest, "虎皮椒配置不可用", err)
	}
	return nil
}

func normalizeHupijiaoConfig(req SaveHupijiaoConfigRequest) (HupijiaoConfig, error) {
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method != "ALIPAY" && method != "WECHAT" {
		return HupijiaoConfig{}, apperrors.New(consts.StatusBadRequest, "虎皮椒支付方式不合法")
	}
	createURL := strings.TrimSpace(req.CreateURL)
	if createURL == "" {
		createURL = defaultHupijiaoCreateURL
	}
	queryURL := strings.TrimSpace(req.QueryURL)
	if queryURL == "" {
		queryURL = defaultHupijiaoQueryURL
	}
	if err := validateOfficialHupijiaoEndpoint(createURL); err != nil {
		return HupijiaoConfig{}, apperrors.Wrap(consts.StatusBadRequest, "虎皮椒下单地址不合法", err)
	}
	if err := validateOfficialHupijiaoEndpoint(queryURL); err != nil {
		return HupijiaoConfig{}, apperrors.Wrap(consts.StatusBadRequest, "虎皮椒查单地址不合法", err)
	}
	for _, callback := range []struct {
		value         string
		label         string
		allowFragment bool
	}{
		{req.NotifyURL, "虎皮椒异步通知地址", false},
		{req.ReturnURL, "虎皮椒同步跳转地址", true},
		{req.CallbackURL, "虎皮椒回调跳转地址", true},
		{req.WAPURL, "虎皮椒 WAP 网站地址", true},
	} {
		if err := validateAlipayCallbackURL(strings.TrimSpace(callback.value), callback.label, callback.allowFragment); err != nil {
			return HupijiaoConfig{}, err
		}
	}
	appid := strings.TrimSpace(req.AppID)
	if req.Enabled && appid == "" {
		return HupijiaoConfig{}, apperrors.New(consts.StatusBadRequest, "虎皮椒 appid 不能为空")
	}
	if req.Enabled && strings.TrimSpace(req.NotifyURL) == "" {
		return HupijiaoConfig{}, apperrors.New(consts.StatusBadRequest, "虎皮椒异步通知地址不能为空")
	}
	if method == "WECHAT" && strings.TrimSpace(req.WAPURL) != "" && strings.TrimSpace(req.WAPName) == "" {
		return HupijiaoConfig{}, apperrors.New(consts.StatusBadRequest, "虎皮椒微信 WAP 名称不能为空")
	}
	return HupijiaoConfig{
		Enabled:     req.Enabled,
		Method:      method,
		AppID:       appid,
		CreateURL:   createURL,
		QueryURL:    queryURL,
		NotifyURL:   strings.TrimSpace(req.NotifyURL),
		ReturnURL:   strings.TrimSpace(req.ReturnURL),
		CallbackURL: strings.TrimSpace(req.CallbackURL),
		WAPURL:      strings.TrimSpace(req.WAPURL),
		WAPName:     strings.TrimSpace(req.WAPName),
	}, nil
}

func (s *Service) PaymentMethods(ctx context.Context, principal Principal) ([]PaymentMethod, error) {
	if err := s.require(ctx, principal, "payment:config:view"); err != nil {
		return nil, err
	}
	return s.hupijiaoConfigRepo.PaymentMethods(ctx)
}

func (s *Service) SavePaymentMethods(ctx context.Context, principal Principal, req SavePaymentMethodsRequest) error {
	if err := s.require(ctx, principal, "payment:method:update"); err != nil {
		return err
	}
	return s.savePaymentMethods(ctx, req)
}

func (s *Service) savePaymentMethods(ctx context.Context, req SavePaymentMethodsRequest) error {
	methods, err := normalizePaymentMethods(req)
	if err != nil {
		return err
	}
	return s.hupijiaoConfigRepo.SavePaymentMethods(ctx, methods)
}

func normalizePaymentMethods(req SavePaymentMethodsRequest) ([]PaymentMethod, error) {
	allowed := map[string]struct{}{
		ChannelBalance: {}, ChannelAlipay: {}, ChannelWechat: {},
		ChannelHupijiaoAlipay: {}, ChannelHupijiaoWechat: {}, ChannelTrc20: {}, ChannelBep20: {},
	}
	if len(req.Methods) == 0 || len(req.Methods) > len(allowed) {
		return nil, apperrors.New(consts.StatusBadRequest, "支付方式配置不完整")
	}
	seen := make(map[string]struct{}, len(req.Methods))
	methods := make([]PaymentMethod, 0, len(req.Methods))
	for _, item := range req.Methods {
		item.Code = strings.ToUpper(strings.TrimSpace(item.Code))
		if _, ok := allowed[item.Code]; !ok {
			return nil, apperrors.New(consts.StatusBadRequest, "支付方式不合法")
		}
		if _, ok := seen[item.Code]; ok {
			return nil, apperrors.New(consts.StatusBadRequest, "支付方式重复")
		}
		if item.SortOrder < 0 || item.SortOrder > maxPaymentMethodSortNo {
			return nil, apperrors.New(consts.StatusBadRequest, "支付方式排序不合法")
		}
		seen[item.Code] = struct{}{}
		methods = append(methods, item)
	}
	sort.Slice(methods, func(i, j int) bool {
		if methods[i].SortOrder == methods[j].SortOrder {
			return methods[i].Code < methods[j].Code
		}
		return methods[i].SortOrder < methods[j].SortOrder
	})
	return methods, nil
}

func (s *Service) isPaymentMethodEnabled(ctx context.Context, code string) (bool, error) {
	methods, err := s.hupijiaoConfigRepo.PaymentMethods(ctx)
	if err != nil {
		return false, err
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	for _, method := range methods {
		if strings.EqualFold(method.Code, code) {
			return method.Enabled, nil
		}
	}
	return false, nil
}

func (s *Service) RechargeCapabilities(ctx context.Context, principal Principal) (PaymentCapabilities, error) {
	cfg, err := s.configRepo.AlipayConfig(ctx)
	if err != nil {
		return PaymentCapabilities{}, err
	}
	presets, err := s.repo.ListPresets(ctx, ListParams{Page: 1, PageSize: 100}, true)
	if err != nil {
		return PaymentCapabilities{}, err
	}
	caps := PaymentCapabilities{
		AlipayEnabled: cfg.Enabled,
		Mode:          cfg.Mode,
		Products:      []string{"PAGE", "WAP", "FACE"},
		Presets:       s.localizePresetNames(ctx, presets.Items),
	}
	wcfg, err := s.wechatConfigRepo.WechatConfig(ctx)
	if err != nil {
		return caps, nil
	}
	caps.WechatEnabled = wcfg.Enabled
	if wcfg.Enabled {
		products := []string{}
		products = append(products, TradeTypeNative, TradeTypeH5)
		if strings.TrimSpace(wcfg.OAAppID) != "" || strings.TrimSpace(wcfg.AppID) != "" {
			products = append(products, TradeTypeJSAPI)
		}
		if strings.TrimSpace(wcfg.MiniAppID) != "" || strings.TrimSpace(wcfg.AppID) != "" {
			products = append(products, TradeTypeMini)
		}
		if strings.TrimSpace(wcfg.AppAppID) != "" || strings.TrimSpace(wcfg.AppID) != "" {
			products = append(products, TradeTypeAPP)
		}
		caps.WechatProducts = products
	}
	trc20Cfg, trc20Enabled := s.trc20Status(ctx)
	bep20Cfg, bep20Enabled := s.bep20Status(ctx)
	// 站点结算货币：读取失败时留空，前端按 CNY 兜底渲染。
	if currencyCfg, currencyErr := s.repo.CurrencyConfig(ctx); currencyErr == nil {
		caps.Currency = currencyCfg.Currency
		caps.USDCNYRate = currencyCfg.USDCNYRate
		caps.DisplayName = currencyCfg.DisplayName
	}
	// 链上充值以 USDT 计价，只有站点货币为 USD 时才对外展示。
	trc20Enabled = trc20Enabled && strings.EqualFold(caps.Currency, CurrencyUSD)
	bep20Enabled = bep20Enabled && strings.EqualFold(caps.Currency, CurrencyUSD)
	if methods, methodErr := s.hupijiaoConfigRepo.PaymentMethods(ctx); methodErr == nil {
		for index := range methods {
			switch methods[index].Code {
			case ChannelAlipay:
				methods[index].Enabled = methods[index].Enabled && cfg.Enabled
			case ChannelWechat:
				methods[index].Enabled = methods[index].Enabled && caps.WechatEnabled
			case ChannelHupijiaoAlipay:
				if hp, hpErr := s.hupijiaoConfigRepo.HupijiaoConfig(ctx, "ALIPAY"); hpErr != nil || !hp.Enabled {
					methods[index].Enabled = false
				}
			case ChannelHupijiaoWechat:
				if hp, hpErr := s.hupijiaoConfigRepo.HupijiaoConfig(ctx, "WECHAT"); hpErr != nil || !hp.Enabled {
					methods[index].Enabled = false
				}
			case ChannelTrc20:
				methods[index].Enabled = methods[index].Enabled && trc20Enabled
			case ChannelBep20:
				methods[index].Enabled = methods[index].Enabled && bep20Enabled
			}
		}
		caps.Methods = methods
	}
	if trc20Enabled {
		caps.Trc20Enabled = true
		caps.Trc20Address = trc20Cfg.Address
		caps.Trc20Network = trc20Cfg.Network
		caps.Trc20MinAmount = trc20Cfg.MinAmount
		caps.Trc20MaxAmount = trc20Cfg.MaxAmount
		caps.Trc20Confirmations = trc20Cfg.Confirmations
	}
	if bep20Enabled {
		caps.Bep20Enabled = true
		caps.Bep20Address = bep20Cfg.Address
		caps.Bep20Network = bep20Cfg.Network
		caps.Bep20MinAmount = bep20Cfg.MinAmount
		caps.Bep20MaxAmount = bep20Cfg.MaxAmount
		caps.Bep20Confirmations = bep20Cfg.Confirmations
	}
	return caps, nil
}

// localizePresetNames 按请求语言解析充值档位名；无译文时保留原文。
func (s *Service) localizePresetNames(ctx context.Context, items []RechargePreset) []RechargePreset {
	if s.localizer == nil || len(items) == 0 {
		return items
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.ID)
	}
	overrides := s.localizer.LocalizeBatch(ctx, "recharge_preset", keys)
	for index := range items {
		if name, ok := overrides[items[index].ID]["name"]; ok && name != "" {
			items[index].Name = name
		}
	}
	return items
}

func (s *Service) normalizeEnabledPayChannel(ctx context.Context, raw string) (string, error) {
	channel := strings.ToUpper(strings.TrimSpace(raw))
	useFirstEnabled := channel == ""
	if !useFirstEnabled {
		switch channel {
		case ChannelAlipay, ChannelWechat, ChannelHupijiaoAlipay, ChannelHupijiaoWechat, ChannelTrc20, ChannelBep20:
		default:
			return "", apperrors.New(consts.StatusBadRequest, "支付方式不合法")
		}
	}
	methods, err := s.hupijiaoConfigRepo.PaymentMethods(ctx)
	if err != nil {
		return "", err
	}
	if useFirstEnabled {
		for _, method := range methods {
			if method.Enabled && method.Code != ChannelBalance {
				channel = method.Code
				break
			}
		}
		if channel == "" {
			return "", apperrors.New(consts.StatusServiceUnavailable, "未启用在线支付方式")
		}
	}
	for _, method := range methods {
		if strings.EqualFold(method.Code, channel) && method.Enabled {
			providerEnabled := true
			switch channel {
			case ChannelAlipay:
				stored, configErr := s.configRepo.AlipayConfig(ctx)
				if configErr != nil {
					return "", configErr
				}
				if !stored.Enabled {
					providerEnabled = false
				}
			case ChannelWechat:
				stored, configErr := s.wechatConfigRepo.WechatConfig(ctx)
				if configErr != nil {
					return "", configErr
				}
				if !stored.Enabled {
					providerEnabled = false
				}
			case ChannelHupijiaoAlipay, ChannelHupijiaoWechat:
				hpMethod := "ALIPAY"
				if channel == ChannelHupijiaoWechat {
					hpMethod = "WECHAT"
				}
				stored, configErr := s.hupijiaoConfigRepo.HupijiaoConfig(ctx, hpMethod)
				if configErr != nil {
					return "", configErr
				}
				if !stored.Enabled {
					providerEnabled = false
				}
			case ChannelTrc20:
				if _, ok := s.trc20Status(ctx); !ok {
					providerEnabled = false
				}
			case ChannelBep20:
				if _, ok := s.bep20Status(ctx); !ok {
					providerEnabled = false
				}
			}
			if providerEnabled {
				return channel, nil
			}
			break
		}
	}
	if channel != "" {
		return "", apperrors.New(consts.StatusServiceUnavailable, "所选支付方式未启用")
	}
	return "", apperrors.New(consts.StatusServiceUnavailable, "未启用在线支付方式")
}

// WechatConfig 返回微信支付配置（敏感字段掩码 ******）。
func (s *Service) WechatConfig(ctx context.Context, principal Principal) (WechatConfig, error) {
	if err := s.require(ctx, principal, "payment:config:view"); err != nil {
		return WechatConfig{}, err
	}
	cfg, err := s.wechatConfigRepo.WechatConfig(ctx)
	if err != nil {
		return WechatConfig{}, err
	}
	out := cfg.WechatConfig
	if cfg.PrivateKeyCipher != "" {
		out.PrivateKeyPEM = "******"
	}
	if cfg.APIv3KeyCipher != "" {
		out.APIv3Key = "******"
	}
	if cfg.PlatformPublicKeyCipher != "" {
		out.PlatformPublicKey = "******"
	}
	return out, nil
}

// SaveWechatConfig 保存并校验微信支付配置。
func (s *Service) SaveWechatConfig(ctx context.Context, principal Principal, req SaveWechatConfigRequest) error {
	if err := s.require(ctx, principal, "payment:wechat:update"); err != nil {
		return err
	}
	return s.saveWechatConfig(ctx, req)
}

// ValidateWechatConfig 校验微信支付配置（不落库）。
func (s *Service) ValidateWechatConfig(ctx context.Context, principal Principal, req SaveWechatConfigRequest) error {
	if err := s.require(ctx, principal, "payment:wechat:update"); err != nil {
		return err
	}
	return s.validateWechatConfig(ctx, req)
}

func (s *Service) saveWechatConfig(ctx context.Context, req SaveWechatConfigRequest) error {
	current, err := s.wechatConfigRepo.WechatConfig(ctx)
	if err != nil {
		return err
	}
	cfg, err := normalizeWechatConfig(req)
	if err != nil {
		return err
	}
	submittedKey := normalizeSubmittedPrivateKey(req.PrivateKeyPEM)
	submittedV3 := normalizeSubmittedSecret(req.APIv3Key)
	submittedCert := normalizeSubmittedPrivateKey(req.PlatformPublicKey)
	updateKey := submittedKey != "" || submittedV3 != ""
	updateCert := submittedCert != ""
	if err := validateWechatVerifierRotation(cfg.PlatformPublicKeyID, submittedCert, current); err != nil {
		return apperrors.Wrap(consts.StatusBadRequest, "微信平台验签配置不可用", err)
	}

	keyCipher := current.PrivateKeyCipher
	v3Cipher := current.APIv3KeyCipher
	certCipher := current.PlatformPublicKeyCipher

	if submittedV3 != "" {
		if len(submittedV3) != 32 {
			return apperrors.New(consts.StatusBadRequest, "微信 APIv3Key 长度必须为 32 字节")
		}
		v3Cipher, err = s.cipher.Encrypt(submittedV3)
		if err != nil {
			return mapSecretErr(err)
		}
	}
	if submittedKey != "" {
		keyCipher, err = s.cipher.Encrypt(submittedKey)
		if err != nil {
			return mapSecretErr(err)
		}
	}
	if submittedCert != "" {
		certCipher, err = s.cipher.Encrypt(submittedCert)
		if err != nil {
			return mapSecretErr(err)
		}
	}

	if cfg.Enabled {
		if v3Cipher == "" {
			return apperrors.New(consts.StatusBadRequest, "微信 APIv3Key 未配置")
		}
		if keyCipher == "" {
			return apperrors.New(consts.StatusBadRequest, "微信商户私钥未配置")
		}
		validateCfg := cfg
		if submittedKey != "" {
			validateCfg.PrivateKeyPEM = submittedKey
		} else {
			validateCfg.PrivateKeyPEM, err = s.cipher.Decrypt(keyCipher)
			if err != nil {
				return mapSecretErr(err)
			}
		}
		if submittedV3 != "" {
			validateCfg.APIv3Key = submittedV3
		} else {
			validateCfg.APIv3Key, err = s.cipher.Decrypt(v3Cipher)
			if err != nil {
				return mapSecretErr(err)
			}
		}
		validateCfg.PlatformPublicKey, err = s.resolveWechatPlatformKey(ctx, cfg.PlatformPublicKeyID, submittedCert, current)
		if err != nil {
			return apperrors.Wrap(consts.StatusBadRequest, "微信平台验签配置不可用", err)
		}
		if err := validateWechatSigning(validateCfg); err != nil {
			return apperrors.Wrap(consts.StatusBadRequest, "微信配置不可用", err)
		}
	}
	err = s.wechatConfigRepo.SaveWechatConfig(ctx, cfg, v3Cipher, keyCipher, certCipher, updateKey, updateCert)
	if errors.Is(err, ErrWechatVerifierStateChanged) {
		return apperrors.Wrap(consts.StatusConflict, "微信平台验签配置已被更新，请重新加载后重试", err)
	}
	return err
}

func (s *Service) validateWechatConfig(ctx context.Context, req SaveWechatConfigRequest) error {
	current, err := s.wechatConfigRepo.WechatConfig(ctx)
	if err != nil {
		return err
	}
	cfg, err := normalizeWechatConfig(req)
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Mchid) == "" {
		return apperrors.New(consts.StatusBadRequest, "微信商户号 mchid 不能为空")
	}
	if strings.TrimSpace(cfg.AppID) == "" && strings.TrimSpace(cfg.OAAppID) == "" &&
		strings.TrimSpace(cfg.MiniAppID) == "" && strings.TrimSpace(cfg.AppAppID) == "" &&
		strings.TrimSpace(cfg.SpAppID) == "" && strings.TrimSpace(cfg.SubAppID) == "" {
		return apperrors.New(consts.StatusBadRequest, "微信 AppID 至少需配置一个")
	}
	if strings.TrimSpace(cfg.SerialNo) == "" {
		return apperrors.New(consts.StatusBadRequest, "微信商户证书序列号 serialNo 不能为空")
	}
	key := normalizeSubmittedPrivateKey(req.PrivateKeyPEM)
	v3 := normalizeSubmittedSecret(req.APIv3Key)
	if key == "" && current.PrivateKeyCipher == "" {
		return apperrors.New(consts.StatusBadRequest, "微信商户私钥未配置")
	}
	if v3 == "" && current.APIv3KeyCipher == "" {
		return apperrors.New(consts.StatusBadRequest, "微信 APIv3Key 未配置")
	}
	if len(v3) != 32 && v3 != "" {
		return apperrors.New(consts.StatusBadRequest, "微信 APIv3Key 长度必须为 32 字节")
	}
	validateCfg := cfg
	if key != "" {
		validateCfg.PrivateKeyPEM = key
	} else {
		validateCfg.PrivateKeyPEM, err = s.cipher.Decrypt(current.PrivateKeyCipher)
		if err != nil {
			return mapSecretErr(err)
		}
	}
	if v3 != "" {
		validateCfg.APIv3Key = v3
	} else {
		validateCfg.APIv3Key, err = s.cipher.Decrypt(current.APIv3KeyCipher)
		if err != nil {
			return mapSecretErr(err)
		}
	}
	validateCfg.PlatformPublicKey, err = s.resolveWechatPlatformKey(ctx, cfg.PlatformPublicKeyID, normalizeSubmittedPrivateKey(req.PlatformPublicKey), current)
	if err != nil {
		return apperrors.Wrap(consts.StatusBadRequest, "微信平台验签配置不可用", err)
	}
	if err := validateWechatSigning(validateCfg); err != nil {
		return apperrors.Wrap(consts.StatusBadRequest, "微信配置不可用", err)
	}
	return nil
}

func validateWechatSigning(cfg WechatConfig) error {
	client, err := NewWechatClient(cfg)
	if err != nil {
		return err
	}
	if err := client.requirePlatformVerifier(); err != nil {
		return err
	}
	// 构造一个最小下单体，验证商户私钥可对 v3 签名头正确生成。
	_, err = client.buildProbeAuthorization()
	return err
}

func normalizeWechatConfig(req SaveWechatConfigRequest) (WechatConfig, error) {
	mode := strings.ToUpper(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "SANDBOX"
	}
	if mode != "SANDBOX" && mode != "PRODUCTION" {
		return WechatConfig{}, apperrors.New(consts.StatusBadRequest, "微信支付模式不合法")
	}
	notifyURL := strings.TrimSpace(req.NotifyURL)
	if err := validateAlipayCallbackURL(notifyURL, "微信异步通知地址", false); err != nil {
		return WechatConfig{}, err
	}
	return WechatConfig{
		Enabled:             req.Enabled,
		Mode:                mode,
		Mchid:               strings.TrimSpace(req.Mchid),
		AppID:               strings.TrimSpace(req.AppID),
		SerialNo:            strings.TrimSpace(req.SerialNo),
		PlatformPublicKeyID: strings.TrimSpace(req.PlatformPublicKeyID),
		NotifyURL:           notifyURL,
		SpMchid:             strings.TrimSpace(req.SpMchid),
		SpAppID:             strings.TrimSpace(req.SpAppID),
		SubMchid:            strings.TrimSpace(req.SubMchid),
		SubAppID:            strings.TrimSpace(req.SubAppID),
		OAAppID:             strings.TrimSpace(req.OAAppID),
		MiniAppID:           strings.TrimSpace(req.MiniAppID),
		AppAppID:            strings.TrimSpace(req.AppAppID),
	}, nil
}

func (s *Service) ListPresets(ctx context.Context, principal Principal, params ListParams) (PageResult[RechargePreset], error) {
	if err := s.require(ctx, principal, "payment:preset:list"); err != nil {
		return PageResult[RechargePreset]{}, err
	}
	if err := normalizeListParams(&params, false); err != nil {
		return PageResult[RechargePreset]{}, err
	}
	return s.repo.ListPresets(ctx, params, false)
}

func (s *Service) SavePreset(ctx context.Context, principal Principal, id int64, req SaveRechargePresetRequest) error {
	code := "payment:preset:create"
	if id > 0 {
		code = "payment:preset:update"
	}
	if err := s.require(ctx, principal, code); err != nil {
		return err
	}
	item, err := normalizePreset(req)
	if err != nil {
		return err
	}
	return mapNotFound(s.repo.SavePreset(ctx, id, item), "充值预设不存在")
}

func (s *Service) DeletePreset(ctx context.Context, principal Principal, id int64) error {
	if err := s.require(ctx, principal, "payment:preset:delete"); err != nil {
		return err
	}
	err := s.repo.DeletePreset(ctx, id)
	if errors.Is(err, ErrRechargePresetHasOrders) {
		return apperrors.New(consts.StatusConflict, "该充值预设已有支付订单，不能删除，请改为禁用")
	}
	return mapNotFound(err, "充值预设不存在")
}

func (s *Service) CreateRechargeOrder(ctx context.Context, principal Principal, req CreateOrderRequest) (PaymentOrder, error) {
	channel, err := s.normalizeEnabledPayChannel(ctx, req.PayChannel)
	if err != nil {
		return PaymentOrder{}, err
	}
	req, err = normalizeOnlineOrderRequest(channel, req)
	if err != nil {
		return PaymentOrder{}, err
	}
	product := req.Product
	if req.PresetID <= 0 {
		return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "充值预设不合法")
	}
	if err := s.ensureNoPendingOrder(ctx, principal.ID, "RECHARGE", req.PresetID, req.ForceNew); err != nil {
		return PaymentOrder{}, err
	}
	orderNo, err := newOrderNo()
	if err != nil {
		return PaymentOrder{}, apperrors.Wrap(consts.StatusServiceUnavailable, "支付订单号生成失败", err)
	}
	seed, err := s.repo.BuildRechargeOrder(ctx, principal.ID, req.PresetID, orderNo, product)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	seed.TradeType = req.TradeType
	seed.OpenID = req.OpenID
	seed.ClientIP = req.ClientIP
	seed.PayChannel = channel
	if err := s.applyOrderCurrency(ctx, &seed); err != nil {
		return PaymentOrder{}, err
	}
	if channel == ChannelTrc20 {
		return s.createOrderTrc20(ctx, seed)
	}
	if channel == ChannelBep20 {
		return s.createOrderBep20(ctx, seed)
	}
	if channel == ChannelWechat {
		return s.createOrderWechat(ctx, seed)
	}
	if channel == ChannelHupijiaoAlipay || channel == ChannelHupijiaoWechat {
		return s.createOrderHupijiao(ctx, seed)
	}
	return s.createOrder(ctx, seed)
}

func (s *Service) CreateGlobalPackageOrder(ctx context.Context, principal Principal, packageID int64, req CreateOrderRequest) (PaymentOrder, error) {
	channel, err := s.normalizeEnabledPayChannel(ctx, req.PayChannel)
	if err != nil {
		return PaymentOrder{}, err
	}
	if channel == ChannelTrc20 || channel == ChannelBep20 {
		return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "链上 USDT 仅用于余额充值")
	}
	req, err = normalizeOnlineOrderRequest(channel, req)
	if err != nil {
		return PaymentOrder{}, err
	}
	product := req.Product
	if err := s.ensureNoPendingOrder(ctx, principal.ID, "GLOBAL_PACKAGE", packageID, req.ForceNew); err != nil {
		return PaymentOrder{}, err
	}
	orderNo, err := newOrderNo()
	if err != nil {
		return PaymentOrder{}, apperrors.Wrap(consts.StatusServiceUnavailable, "支付订单号生成失败", err)
	}
	seed, err := s.repo.BuildGlobalPackageOrder(ctx, principal.ID, packageID, orderNo, product)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	seed.TradeType = req.TradeType
	seed.OpenID = req.OpenID
	seed.ClientIP = req.ClientIP
	seed.PayChannel = channel
	if err := s.applyOrderCurrency(ctx, &seed); err != nil {
		return PaymentOrder{}, err
	}
	if channel == ChannelWechat {
		return s.createOrderWechat(ctx, seed)
	}
	if channel == ChannelHupijiaoAlipay || channel == ChannelHupijiaoWechat {
		return s.createOrderHupijiao(ctx, seed)
	}
	return s.createOrder(ctx, seed)
}

func (s *Service) CreateInterfacePackageOrder(ctx context.Context, principal Principal, specID int64, req CreateOrderRequest) (PaymentOrder, error) {
	channel, err := s.normalizeEnabledPayChannel(ctx, req.PayChannel)
	if err != nil {
		return PaymentOrder{}, err
	}
	if channel == ChannelTrc20 || channel == ChannelBep20 {
		return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "链上 USDT 仅用于余额充值")
	}
	req, err = normalizeOnlineOrderRequest(channel, req)
	if err != nil {
		return PaymentOrder{}, err
	}
	product := req.Product
	if err := s.ensureNoPendingOrder(ctx, principal.ID, "INTERFACE_PACKAGE", specID, req.ForceNew); err != nil {
		return PaymentOrder{}, err
	}
	orderNo, err := newOrderNo()
	if err != nil {
		return PaymentOrder{}, apperrors.Wrap(consts.StatusServiceUnavailable, "支付订单号生成失败", err)
	}
	seed, err := s.repo.BuildInterfacePackageOrder(ctx, principal.ID, specID, orderNo, product)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	seed.TradeType = req.TradeType
	seed.OpenID = req.OpenID
	seed.ClientIP = req.ClientIP
	seed.PayChannel = channel
	if err := s.applyOrderCurrency(ctx, &seed); err != nil {
		return PaymentOrder{}, err
	}
	if channel == ChannelWechat {
		return s.createOrderWechat(ctx, seed)
	}
	if channel == ChannelHupijiaoAlipay || channel == ChannelHupijiaoWechat {
		return s.createOrderHupijiao(ctx, seed)
	}
	return s.createOrder(ctx, seed)
}

func (s *Service) CreatePointPackageOrder(ctx context.Context, principal Principal, packageID int64, req CreateOrderRequest) (PaymentOrder, error) {
	channel, err := s.normalizeEnabledPayChannel(ctx, req.PayChannel)
	if err != nil {
		return PaymentOrder{}, err
	}
	if channel == ChannelTrc20 || channel == ChannelBep20 {
		return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "链上 USDT 仅用于余额充值")
	}
	req, err = normalizeOnlineOrderRequest(channel, req)
	if err != nil {
		return PaymentOrder{}, err
	}
	product := req.Product
	if err := s.ensureNoPendingOrder(ctx, principal.ID, "POINT_PACKAGE", packageID, req.ForceNew); err != nil {
		return PaymentOrder{}, err
	}
	orderNo, err := newOrderNo()
	if err != nil {
		return PaymentOrder{}, apperrors.Wrap(consts.StatusServiceUnavailable, "支付订单号生成失败", err)
	}
	seed, err := s.repo.BuildPointPackageOrder(ctx, principal.ID, packageID, orderNo, product)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	seed.TradeType = req.TradeType
	seed.OpenID = req.OpenID
	seed.ClientIP = req.ClientIP
	seed.PayChannel = channel
	if err := s.applyOrderCurrency(ctx, &seed); err != nil {
		return PaymentOrder{}, err
	}
	if channel == ChannelWechat {
		return s.createOrderWechat(ctx, seed)
	}
	if channel == ChannelHupijiaoAlipay || channel == ChannelHupijiaoWechat {
		return s.createOrderHupijiao(ctx, seed)
	}
	return s.createOrder(ctx, seed)
}

func normalizePendingOrderType(value string) (string, error) {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "RECHARGE":
		return "RECHARGE", nil
	case "GLOBAL_PACKAGE":
		return "GLOBAL_PACKAGE", nil
	case "INTERFACE_PACKAGE":
		return "INTERFACE_PACKAGE", nil
	case "POINT_PACKAGE":
		return "POINT_PACKAGE", nil
	default:
		return "", apperrors.New(consts.StatusBadRequest, "订单类型不合法")
	}
}

// PendingOrder 返回同一商品仍可支付的旧订单；没有时返回 nil。
func (s *Service) PendingOrder(ctx context.Context, principal Principal, orderType string, targetID int64) (*PaymentOrder, error) {
	normalizedType, err := normalizePendingOrderType(orderType)
	if err != nil {
		return nil, err
	}
	if targetID <= 0 {
		return nil, apperrors.New(consts.StatusBadRequest, "商品ID不合法")
	}
	orderNo, err := s.repo.PendingOrderNo(ctx, principal.ID, normalizedType, targetID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	order, err := s.repo.OrderByNo(ctx, orderNo, principal.ID, false)
	if err != nil {
		return nil, mapPackageErr(err)
	}
	mapped := paymentOrderForResponse(order)
	return &mapped, nil
}

// ContinueOrder 返回仍可支付的订单；过期 TRC20 原单仅供已转账用户继续核验到账。
func (s *Service) ContinueOrder(ctx context.Context, principal Principal, orderNo string) (PaymentOrder, error) {
	order, err := s.repo.OrderByNo(ctx, strings.TrimSpace(orderNo), principal.ID, false)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	if order.Status == "PAID" {
		return mapOrder(order, nil)
	}
	if order.Status == "EXPIRED" && isChainDepositChannel(order.PayChannel) {
		return mapOrder(order, nil)
	}
	if order.Status != "CREATED" && order.Status != "PAYING" {
		return PaymentOrder{}, apperrors.New(consts.StatusConflict, "订单已关闭或已过期，请重新下单")
	}
	expired, err := s.repo.OrderExpired(ctx, order.OrderNo)
	if err != nil {
		return PaymentOrder{}, err
	}
	if expired {
		if err := s.repo.MarkOrderExpired(ctx, order.OrderNo, order.PayChannel); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return PaymentOrder{}, err
		}
		if isChainDepositChannel(order.PayChannel) {
			// Re-read after expiry: settlement may have won the row lock and set PAID.
			return mapOrder(s.repo.OrderByNo(ctx, order.OrderNo, principal.ID, false))
		}
		return PaymentOrder{}, apperrors.New(consts.StatusConflict, "订单已过期，请重新下单")
	}
	return mapOrder(order, nil)
}

// CancelOrder 用户主动取消自己的未支付订单：释放限量套餐的库存占用，
// 并让同商品的「待支付订单」不再挡住重新下单。
// 渠道侧的旧支付串仍然可用，所以取消后到账依然会补结算，用户不会丢钱。
func (s *Service) CancelOrder(ctx context.Context, principal Principal, orderNo string) (PaymentOrder, error) {
	order, err := s.repo.OrderByNo(ctx, strings.TrimSpace(orderNo), principal.ID, false)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	if order.Status == "PAID" {
		return PaymentOrder{}, apperrors.New(consts.StatusConflict, "订单已支付，无法取消")
	}
	if order.Status == "CANCELLED" {
		return mapOrder(order, nil)
	}
	if order.Status != "CREATED" && order.Status != "PAYING" {
		return PaymentOrder{}, apperrors.New(consts.StatusConflict, "订单已结束，无需取消")
	}
	// 链上充值靠「精确金额 + 时间窗」在链上认单，取消会让金额复用规则变模糊，
	// 因此只允许等本地过期。
	if isChainDepositChannel(order.PayChannel) {
		return PaymentOrder{}, apperrors.New(consts.StatusConflict, "USDT 充值订单不支持取消，请等待过期或完成转账")
	}
	if err := s.repo.MarkOrderCancelled(ctx, order.OrderNo, order.PayChannel); err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	return mapOrder(s.repo.OrderByNo(ctx, order.OrderNo, principal.ID, false))
}

// DeleteUserOrder 把一笔已结束的订单从用户交易记录里隐藏。
func (s *Service) DeleteUserOrder(ctx context.Context, principal Principal, orderNo string) (bool, error) {
	return s.deleteOrder(ctx, strings.TrimSpace(orderNo), principal.ID, false)
}

// DeleteAdminOrder 管理员删除任意一笔已结束的订单，需要 payment:order:delete 权限。
func (s *Service) DeleteAdminOrder(ctx context.Context, principal Principal, orderNo string) (bool, error) {
	if err := s.require(ctx, principal, "payment:order:delete"); err != nil {
		return false, err
	}
	return s.deleteOrder(ctx, strings.TrimSpace(orderNo), 0, true)
}

// deleteOrder 只做软隐藏：订单行保留，支付审计与旧支付载荷不丢，
// 迟到到账仍能补结算（仅列表查询过滤 deleted_at）。
func (s *Service) deleteOrder(ctx context.Context, orderNo string, userID int64, admin bool) (bool, error) {
	order, err := s.repo.OrderByNo(ctx, orderNo, userID, admin)
	if err != nil {
		return false, mapPackageErr(err)
	}
	if !isDeletableOrderStatus(order.Status) {
		return false, apperrors.New(consts.StatusConflict, "只有已结束的订单可以删除")
	}
	deleted, err := s.repo.SoftDeleteOrder(ctx, order.OrderNo, userID, admin)
	if err != nil {
		return false, mapPackageErr(err)
	}
	if !deleted {
		// 读取与删除之间状态变了（例如迟到到账），让前端刷新后再看。
		return false, apperrors.New(consts.StatusConflict, "订单状态已变化，请刷新后重试")
	}
	return true, nil
}

func (s *Service) ensureNoPendingOrder(ctx context.Context, userID int64, orderType string, targetID int64, forceNew bool) error {
	if forceNew {
		return nil
	}
	_, err := s.repo.PendingOrderNo(ctx, userID, orderType, targetID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return apperrors.New(consts.StatusConflict, "您有相同商品的待支付订单，请继续支付旧订单或确认开启新订单")
}

// createOrderTrc20 TRC20 没有第三方收银台：收款地址与精确金额已在 seed 上，
// 落库即待支付，认款交给链上对账。绝不能复用 createOrder —— 那条路径会先要求
// 支付宝配置并向支付宝下单，海外站未启用支付宝时直接 503。
func (s *Service) createOrderTrc20(ctx context.Context, seed OrderSeed) (PaymentOrder, error) {
	order, err := s.repo.CreateOrder(ctx, seed)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	return mapOrder(order, nil)
}

// createOrderBep20 与 TRC20 相同：没有收银台，落库即待支付，由 BEP20 对账 worker 认款。
func (s *Service) createOrderBep20(ctx context.Context, seed OrderSeed) (PaymentOrder, error) {
	order, err := s.repo.CreateOrder(ctx, seed)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	return mapOrder(order, nil)
}

func (s *Service) createOrder(ctx context.Context, seed OrderSeed) (PaymentOrder, error) {
	if seed.OrderType != "RECHARGE" && normalizeAmount(seed.PayAmount) == "0.0000" {
		return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "免费套餐请使用商城免费领取功能")
	}
	client, err := s.alipayClient(ctx)
	if err != nil {
		return PaymentOrder{}, err
	}
	order, err := s.repo.CreateOrder(ctx, seed)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	// 商品名只改写这次请求：库里仍留明细名，支付宝账单按站点中性名展示。
	if err := s.applyPayGoodsName(ctx, &order); err != nil {
		return PaymentOrder{}, err
	}
	pay, err := client.Create(ctx, order)
	if err != nil {
		// A timeout can happen after Alipay accepted a FACE precreate request. Keep
		// the local order settleable so a signed notify or later sync can recover it.
		return PaymentOrder{}, apperrors.Wrap(consts.StatusServiceUnavailable, "支付宝下单结果暂不确定，请稍后查询订单状态", err)
	}
	if err := s.repo.SavePayPayload(ctx, order.OrderNo, pay); err != nil {
		// The remote order may already exist even when persisting its presentation
		// payload fails. Do not move it into a terminal state before reconciliation.
		return PaymentOrder{}, apperrors.Wrap(consts.StatusServiceUnavailable, "支付信息保存失败，请稍后查询订单状态", err)
	}
	return mapOrder(s.repo.OrderByNo(ctx, order.OrderNo, seed.UserID, false))
}

// wechatClientWithConfig 解密微信敏感字段并构造 WechatClient。
// 若配置未配置平台证书，则尝试从 bp_payment_wechat_platform_cert 缓存加载。
func (s *Service) wechatClientWithConfig(ctx context.Context) (*WechatClient, WechatConfig, error) {
	stored, err := s.wechatConfigRepo.WechatConfig(ctx)
	if err != nil {
		return nil, WechatConfig{}, err
	}
	if !stored.Enabled {
		return nil, WechatConfig{}, apperrors.New(consts.StatusServiceUnavailable, "微信支付未启用")
	}
	cfg := stored.WechatConfig
	if strings.TrimSpace(stored.APIv3KeyCipher) == "" {
		return nil, WechatConfig{}, apperrors.New(consts.StatusServiceUnavailable, "微信 APIv3Key 未配置")
	}
	if strings.TrimSpace(stored.PrivateKeyCipher) == "" {
		return nil, WechatConfig{}, apperrors.New(consts.StatusServiceUnavailable, "微信商户私钥未配置")
	}
	apiV3Key, err := s.cipher.Decrypt(stored.APIv3KeyCipher)
	if err != nil {
		return nil, WechatConfig{}, mapSecretErr(err)
	}
	privateKey, err := s.cipher.Decrypt(stored.PrivateKeyCipher)
	if err != nil {
		return nil, WechatConfig{}, mapSecretErr(err)
	}
	cfg.APIv3Key = apiV3Key
	cfg.PrivateKeyPEM = privateKey
	cfg.PlatformPublicKey, err = s.resolveWechatPlatformKey(ctx, cfg.PlatformPublicKeyID, "", stored)
	if err != nil {
		return nil, WechatConfig{}, apperrors.Wrap(consts.StatusServiceUnavailable, "微信平台验签配置不可用", err)
	}

	client, err := NewWechatClient(cfg)
	if err != nil {
		return nil, WechatConfig{}, apperrors.Wrap(consts.StatusServiceUnavailable, "微信配置不可用", err)
	}
	return client, cfg, nil
}

// A retained inline key may be reused only for its existing identity. This
// also applies to disabled drafts, so disabling cannot bypass the rotation gate.
func validateWechatVerifierRotation(id, submitted string, current storedWechatConfig) error {
	if current.PlatformPublicKeyCipher != "" && strings.TrimSpace(id) != strings.TrimSpace(current.PlatformPublicKeyID) && submitted == "" {
		return errors.New("changing the WeChat platform key ID requires a matching new public key")
	}
	return nil
}

// resolveWechatPlatformKey does not parse UI masks as keys, relabel retained
// inline keys, or silently ignore an unavailable configured certificate cache.
func (s *Service) resolveWechatPlatformKey(ctx context.Context, id, submitted string, current storedWechatConfig) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return "", errors.New("wechat platform verification key ID is required")
	}
	if err := validateWechatVerifierRotation(id, submitted, current); err != nil {
		return "", err
	}
	if submitted != "" {
		return submitted, nil
	}
	cipherText := current.PlatformPublicKeyCipher
	if cipherText == "" {
		var err error
		cipherText, err = s.wechatConfigRepo.LoadWechatPlatformCert(ctx, id)
		if err != nil {
			return "", err
		}
	}
	if cipherText == "" {
		return "", errors.New("wechat platform verification key is required")
	}
	return s.cipher.Decrypt(cipherText)
}

// createOrderWechat 微信通道下单：建单 → 调微信 → 落支付载荷。
func (s *Service) createOrderWechat(ctx context.Context, seed OrderSeed) (PaymentOrder, error) {
	if seed.OrderType != "RECHARGE" && normalizeAmount(seed.PayAmount) == "0.0000" {
		return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "免费套餐请使用商城免费领取功能")
	}
	client, _, err := s.wechatClientWithConfig(ctx)
	if err != nil {
		return PaymentOrder{}, err
	}
	// Run pure gateway validation before creating a local payment order.
	candidate := PaymentOrder{OrderNo: seed.OrderNo, Subject: seed.Subject, PayAmount: seed.PayAmount, TradeType: seed.TradeType, BuyerID: seed.OpenID, ClientIP: seed.ClientIP}
	plan, err := client.prepareCreate(candidate)
	if err != nil {
		return PaymentOrder{}, err
	}
	seed.Product, seed.TradeType = plan.tradeType, plan.tradeType
	order, err := s.repo.CreateOrder(ctx, seed)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	order.TradeType = seed.TradeType
	order.BuyerID = strings.TrimSpace(seed.OpenID)
	order.ClientIP = seed.ClientIP
	// description 进微信风控判定：换成站点中性名，库里 subject 仍留可对账的明细名。
	if err := s.applyPayGoodsName(ctx, &order); err != nil {
		return PaymentOrder{}, err
	}
	pay, err := client.Create(ctx, order)
	if err != nil {
		return PaymentOrder{}, apperrors.Wrap(consts.StatusServiceUnavailable, "微信下单结果暂不确定，请稍后查询订单状态", err)
	}
	if err := s.repo.SaveWechatPayPayload(ctx, order.OrderNo, pay); err != nil {
		return PaymentOrder{}, apperrors.Wrap(consts.StatusServiceUnavailable, "支付信息保存失败，请稍后查询订单状态", err)
	}
	stored, err := mapOrder(s.repo.OrderByNo(ctx, order.OrderNo, seed.UserID, false))
	if err != nil {
		return PaymentOrder{}, err
	}
	if stored.Status == "CREATED" || stored.Status == "PAYING" {
		stored.PayParams = pay.PayParams
	}
	return stored, nil
}

func (s *Service) hupijiaoClientWithConfig(ctx context.Context, channel string) (*HupijiaoClient, HupijiaoConfig, error) {
	method := "ALIPAY"
	if strings.EqualFold(channel, ChannelHupijiaoWechat) {
		method = "WECHAT"
	} else if !strings.EqualFold(channel, ChannelHupijiaoAlipay) {
		return nil, HupijiaoConfig{}, apperrors.New(consts.StatusBadRequest, "虎皮椒支付方式不合法")
	}
	stored, err := s.hupijiaoConfigRepo.HupijiaoConfig(ctx, method)
	if err != nil {
		return nil, HupijiaoConfig{}, err
	}
	if !stored.Enabled {
		return nil, HupijiaoConfig{}, apperrors.New(consts.StatusServiceUnavailable, "虎皮椒支付未启用")
	}
	if strings.TrimSpace(stored.AppSecretCipher) == "" {
		return nil, HupijiaoConfig{}, apperrors.New(consts.StatusServiceUnavailable, "虎皮椒 appsecret 未配置")
	}
	secret, err := s.cipher.Decrypt(stored.AppSecretCipher)
	if err != nil {
		return nil, HupijiaoConfig{}, mapSecretErr(err)
	}
	cfg := stored.HupijiaoConfig
	cfg.AppSecret = secret
	client, err := NewHupijiaoClient(cfg)
	if err != nil {
		return nil, HupijiaoConfig{}, apperrors.Wrap(consts.StatusServiceUnavailable, "虎皮椒配置不可用", err)
	}
	return client, cfg, nil
}

func (s *Service) createOrderHupijiao(ctx context.Context, seed OrderSeed) (PaymentOrder, error) {
	if seed.OrderType != "RECHARGE" && normalizeAmount(seed.PayAmount) == "0.0000" {
		return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "免费套餐请使用商城免费领取功能")
	}
	client, cfg, err := s.hupijiaoClientWithConfig(ctx, seed.PayChannel)
	if err != nil {
		return PaymentOrder{}, err
	}
	if cfg.Method == "WECHAT" {
		tradeType := strings.ToUpper(strings.TrimSpace(seed.TradeType))
		if tradeType == "" {
			tradeType = TradeTypeNative
		}
		if tradeType != TradeTypeNative && tradeType != TradeTypeH5 && tradeType != TradeTypeJSAPI {
			return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "虎皮椒微信交易类型不合法")
		}
		seed.TradeType = tradeType
	}
	order, err := s.repo.CreateOrder(ctx, seed)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	order.PayChannel = seed.PayChannel
	order.TradeType = seed.TradeType
	order.BuyerID = strings.TrimSpace(seed.OpenID)
	// 虎皮椒走微信聚合通道，title 直接进风控判定：用站点中性名替换订单明细名。
	if err := s.applyPayGoodsName(ctx, &order); err != nil {
		return PaymentOrder{}, err
	}
	pay, err := client.Create(ctx, order)
	if err != nil {
		return PaymentOrder{}, apperrors.Wrap(consts.StatusServiceUnavailable, "虎皮椒下单结果暂不确定，请稍后查询订单状态", err)
	}
	if err := s.repo.SaveHupijiaoPayPayload(ctx, order.OrderNo, pay); err != nil {
		return PaymentOrder{}, apperrors.Wrap(consts.StatusServiceUnavailable, "支付信息保存失败，请稍后查询订单状态", err)
	}
	return mapOrder(s.repo.OrderByNo(ctx, order.OrderNo, seed.UserID, false))
}

func (s *Service) syncOrderHupijiao(ctx context.Context, order PaymentOrder, userID int64, admin bool) (PaymentOrder, error) {
	channel, channelErr := canonicalExternalPaymentChannel(order.PayChannel)
	if channelErr != nil || (channel != ChannelHupijiaoAlipay && channel != ChannelHupijiaoWechat) {
		return PaymentOrder{}, mapPackageErr(ErrUnsupportedPaymentChannel)
	}
	if order.Status == "PAID" {
		return order, nil
	}
	expired, _ := s.repo.OrderExpired(ctx, order.OrderNo)
	client, _, err := s.hupijiaoClientWithConfig(ctx, channel)
	if err != nil {
		return PaymentOrder{}, asChannelConfigIssue(err)
	}
	status, err := client.Query(ctx, order.OrderNo)
	if err != nil {
		return PaymentOrder{}, err
	}
	if status.Success {
		if strings.TrimSpace(status.OutTradeNo) != order.OrderNo {
			return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "虎皮椒查单订单号不匹配")
		}
		if normalizeAmount(status.Total) != normalizeAmount(order.PayAmount) {
			return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "虎皮椒查单金额不匹配")
		}
		if strings.TrimSpace(status.TradeNo) == "" {
			return PaymentOrder{}, apperrors.New(consts.StatusServiceUnavailable, "虎皮椒查单交易号缺失")
		}
		if err := s.repo.SettleHupijiaoPaidOrder(ctx, channel, order.OrderNo, status); err != nil {
			return PaymentOrder{}, mapPackageErr(err)
		}
		s.notifyPaymentPaid(ctx, order.OrderNo)
	} else if strings.EqualFold(status.TradeState, "CD") {
		if err := s.repo.MarkOrderClosed(ctx, order.OrderNo, channel); err != nil {
			return PaymentOrder{}, err
		}
	} else if expired {
		if err := s.repo.MarkOrderExpired(ctx, order.OrderNo, channel); err != nil {
			return PaymentOrder{}, err
		}
	}
	return mapOrder(s.repo.OrderByNo(ctx, order.OrderNo, userID, admin))
}

// syncOrderWechat 微信通道主动查单并结算。
func (s *Service) syncOrderWechat(ctx context.Context, order PaymentOrder, userID int64, admin bool) (PaymentOrder, error) {
	if err := requirePaymentChannel(order.PayChannel, ChannelWechat); err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	if order.Status == "PAID" {
		return order, nil
	}
	expired, _ := s.repo.OrderExpired(ctx, order.OrderNo)
	client, _, err := s.wechatClientWithConfig(ctx)
	if err != nil {
		return PaymentOrder{}, asChannelConfigIssue(err)
	}
	status, err := client.Query(ctx, order.OrderNo)
	if err != nil {
		return PaymentOrder{}, err
	}
	if status.Success {
		if strings.TrimSpace(status.OutTradeNo) != order.OrderNo {
			return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "wechat query order number mismatch")
		}
		if normalizeAmount(status.Total) != normalizeAmount(order.PayAmount) {
			return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "wechat query amount mismatch")
		}
		if err := s.repo.SettleWechatPaidOrder(ctx, order.OrderNo, status); err != nil {
			return PaymentOrder{}, mapPackageErr(err)
		}
		s.notifyPaymentPaid(ctx, order.OrderNo)
	} else if strings.EqualFold(status.TradeState, "CLOSED") {
		if strings.TrimSpace(status.OutTradeNo) != order.OrderNo {
			return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "wechat query order number mismatch")
		}
		if err := s.repo.MarkOrderClosed(ctx, order.OrderNo, ChannelWechat); err != nil {
			return PaymentOrder{}, err
		}
	} else if expired {
		if err := s.repo.MarkOrderExpired(ctx, order.OrderNo, ChannelWechat); err != nil {
			return PaymentOrder{}, err
		}
	}
	return mapOrder(s.repo.OrderByNo(ctx, order.OrderNo, userID, admin))
}

// HandleWechatNotify 处理微信支付回调：验签 → 解密 → 审计 → 幂等 → 结算。
func (s *Service) HandleWechatNotify(ctx context.Context, body []byte, headers map[string]string) (bool, error) {
	client, _, err := s.wechatClientWithConfig(ctx)
	if err != nil {
		return s.finishWechatNotify(ctx, map[string]string{}, false, false, masking.Error(err), false, err)
	}
	notify, err := client.VerifyNotify(ctx, body, headers)
	if err != nil {
		return s.finishWechatNotify(ctx, map[string]string{}, false, false, "verify failed", false, apperrors.New(consts.StatusBadRequest, "微信通知验签/解密失败"))
	}
	payload := map[string]string{
		"out_trade_no":   notify.OutTradeNo,
		"transaction_id": notify.TradeNo,
		"trade_state":    notify.TradeState,
		"total":          notify.Total,
		"event_type":     "WECHAT_NOTIFY",
	}
	if notify.NotifyID == "" || len(notify.NotifyID) > maxAlipayNotifyIDBytes || strings.TrimSpace(notify.NotifyID) != notify.NotifyID {
		return s.finishWechatNotify(ctx, payload, true, false, "invalid notification ID", false, apperrors.New(consts.StatusBadRequest, "微信通知事件标识不合法"))
	}
	payload["id"], payload["notify_id"] = notify.NotifyID, notify.NotifyID
	if notify.OutTradeNo == "" {
		return s.finishWechatNotify(ctx, payload, true, false, "out_trade_no missing", false, apperrors.New(consts.StatusBadRequest, "微信通知缺少商户单号"))
	}
	order, err := s.repo.OrderByNo(ctx, notify.OutTradeNo, 0, true)
	if err != nil {
		return s.finishWechatNotify(ctx, payload, true, false, "order not found", false, mapPackageErr(err))
	}
	if err := requirePaymentChannel(order.PayChannel, ChannelWechat); err != nil {
		return s.finishWechatNotify(ctx, payload, true, false, "channel mismatch", false, mapPackageErr(err))
	}
	if normalizeAmount(notify.Total) != normalizeAmount(order.PayAmount) {
		return s.finishWechatNotify(ctx, payload, true, false, "amount mismatch", false, apperrors.New(consts.StatusBadRequest, "微信通知金额不匹配"))
	}
	if !notify.Success {
		return s.finishWechatNotify(ctx, payload, true, true, "not paid", true, nil)
	}
	if strings.TrimSpace(notify.TradeNo) == "" {
		return s.finishWechatNotify(ctx, payload, true, false, "trade number missing", false, apperrors.New(consts.StatusBadRequest, "微信通知交易号缺失"))
	}
	settleStatus := PayTradeStatus{
		OutTradeNo: notify.OutTradeNo,
		TradeNo:    notify.TradeNo,
		TradeState: notify.TradeState,
		Total:      notify.Total,
		Success:    notify.Success,
		BuyerID:    notify.BuyerID,
	}
	err = s.repo.SettleWechatPaidOrder(ctx, order.OrderNo, settleStatus)
	if err != nil {
		mappedErr := mapPackageErr(err)
		return s.finishWechatNotify(ctx, payload, true, false, masking.Error(mappedErr), false, mappedErr)
	}
	s.notifyPaymentPaid(ctx, order.OrderNo)
	return s.finishWechatNotify(ctx, payload, true, true, "success", true, nil)
}

func (s *Service) finishWechatNotify(ctx context.Context, payload map[string]string, verified, processed bool, message string, result bool, resultErr error) (bool, error) {
	message = masking.ErrorText(message)
	if err := s.wechatConfigRepo.InsertWechatNotifyLog(ctx, payload, verified, processed, message); err != nil {
		return false, apperrors.Wrap(consts.StatusServiceUnavailable, "微信通知审计日志写入失败", errors.Join(resultErr, err))
	}
	return result, resultErr
}

func (s *Service) UserOrder(ctx context.Context, principal Principal, orderNo string) (PaymentOrder, error) {
	return mapOrder(s.repo.OrderByNo(ctx, strings.TrimSpace(orderNo), principal.ID, false))
}

func (s *Service) AdminOrder(ctx context.Context, principal Principal, orderNo string) (PaymentOrder, error) {
	if err := s.require(ctx, principal, "payment:order:detail"); err != nil {
		return PaymentOrder{}, err
	}
	return mapOrder(s.repo.OrderByNo(ctx, strings.TrimSpace(orderNo), 0, true))
}

func (s *Service) AdminOrders(ctx context.Context, principal Principal, params ListParams) (PageResult[PaymentOrder], error) {
	if err := s.require(ctx, principal, "payment:order:list"); err != nil {
		return PageResult[PaymentOrder]{}, err
	}
	if err := normalizeListParams(&params, true); err != nil {
		return PageResult[PaymentOrder]{}, err
	}
	return s.repo.ListOrders(ctx, params, 0, true)
}

var assetRecordBizTypes = []string{
	"ADMIN_USER_INITIAL_ASSET",
	"ADMIN_USER_ASSET_ADJUST",
	"ADMIN_BALANCE_ADJUST",
	"ALIPAY_RECHARGE",
	"ALIPAY_POINT_PACKAGE",
	"REDEEM_CODE",
	"POINTS_GRANT",
	"PURCHASE_GLOBAL_PACKAGE",
	"PURCHASE_INTERFACE_PACKAGE",
	"PURCHASE_POINT_PACKAGE",
}

// AssetRecords 返回管理端可见的资产变动流水（充值、套餐购买、兑换码、
// 管理员增减余额等）。逐次调用预扣不在列表内，避免淹没人工审计。
func (s *Service) AssetRecords(ctx context.Context, principal Principal, params AssetRecordListParams) (PageResult[AssetRecord], error) {
	if err := s.require(ctx, principal, "payment:order:list"); err != nil {
		return PageResult[AssetRecord]{}, err
	}
	normalized, err := normalizeAssetRecordListParams(params)
	if err != nil {
		return PageResult[AssetRecord]{}, err
	}
	return s.repo.ListAssetRecords(ctx, normalized)
}

func normalizeAssetRecordListParams(params AssetRecordListParams) (AssetRecordListParams, error) {
	params.Keyword = strings.TrimSpace(params.Keyword)
	if err := listquery.Validate(params.Keyword, params.Page, params.PageSize, 100); err != nil {
		return AssetRecordListParams{}, err
	}
	params.BizType = strings.ToUpper(strings.TrimSpace(params.BizType))
	if params.BizType != "" && !containsString(assetRecordBizTypes, params.BizType) {
		return AssetRecordListParams{}, apperrors.New(consts.StatusBadRequest, "资产变动类型不合法")
	}
	params.DateFrom = strings.TrimSpace(params.DateFrom)
	params.DateTo = strings.TrimSpace(params.DateTo)
	for _, value := range []string{params.DateFrom, params.DateTo} {
		if value == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", value); err != nil {
			return AssetRecordListParams{}, apperrors.New(consts.StatusBadRequest, "日期格式不合法")
		}
	}
	if params.DateFrom != "" && params.DateTo != "" && params.DateFrom > params.DateTo {
		return AssetRecordListParams{}, apperrors.New(consts.StatusBadRequest, "开始日期不能晚于结束日期")
	}
	return params, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (s *Service) UserOrders(ctx context.Context, principal Principal, params ListParams) (PageResult[PaymentOrder], error) {
	if err := normalizeListParams(&params, true); err != nil {
		return PageResult[PaymentOrder]{}, err
	}
	return s.repo.ListOrders(ctx, params, principal.ID, false)
}

func normalizeListParams(params *ListParams, orders bool) error {
	params.Keyword = strings.TrimSpace(params.Keyword)
	params.Status = strings.ToUpper(strings.TrimSpace(params.Status))
	params.OrderType = strings.ToUpper(strings.TrimSpace(params.OrderType))
	if err := listquery.Validate(params.Keyword, params.Page, params.PageSize, 100); err != nil {
		return err
	}
	if !orders {
		if params.Status != "" && params.Status != "0" && params.Status != "1" {
			return apperrors.New(consts.StatusBadRequest, "status must be 0 or 1")
		}
		return nil
	}
	if err := listquery.Enum("status", params.Status, "CREATED", "PAYING", "PAID", "CLOSED", "EXPIRED", "FAILED", "CANCELLED"); err != nil {
		return err
	}
	if err := listquery.Enum("orderType", params.OrderType, "RECHARGE", "GLOBAL_PACKAGE", "INTERFACE_PACKAGE", "POINT_PACKAGE"); err != nil {
		return err
	}
	params.SortBy = strings.ToLower(strings.TrimSpace(params.SortBy))
	switch params.SortBy {
	case "", "created_at", "paid_at", "pay_amount", "status":
	default:
		return apperrors.New(consts.StatusBadRequest, "排序字段不合法")
	}
	params.SortOrder = strings.ToLower(strings.TrimSpace(params.SortOrder))
	if params.SortOrder == "" {
		params.SortOrder = "desc"
	}
	if params.SortOrder != "asc" && params.SortOrder != "desc" {
		return apperrors.New(consts.StatusBadRequest, "排序方向不合法")
	}
	for _, item := range []*string{&params.PaidFrom, &params.PaidTo} {
		value := strings.TrimSpace(*item)
		if value == "" {
			*item = ""
			continue
		}
		parsed, err := time.Parse("2006-01-02", value)
		if err != nil {
			return apperrors.New(consts.StatusBadRequest, "支付日期格式应为 YYYY-MM-DD")
		}
		*item = parsed.Format("2006-01-02")
	}
	if params.PaidFrom != "" && params.PaidTo != "" && params.PaidFrom > params.PaidTo {
		return apperrors.New(consts.StatusBadRequest, "支付时间范围不合法")
	}
	for _, item := range []*string{&params.MinAmount, &params.MaxAmount} {
		value := strings.TrimSpace(*item)
		if value == "" {
			*item = ""
			continue
		}
		parsed, err := decimalvalue.ParseNonNegative(value)
		if err != nil {
			return apperrors.New(consts.StatusBadRequest, "金额筛选格式不合法")
		}
		*item = parsed.FloatString(4)
	}
	if params.MinAmount != "" && params.MaxAmount != "" {
		minAmount, _ := decimalvalue.ParseNonNegative(params.MinAmount)
		maxAmount, _ := decimalvalue.ParseNonNegative(params.MaxAmount)
		if minAmount.Cmp(maxAmount) > 0 {
			return apperrors.New(consts.StatusBadRequest, "金额范围不合法")
		}
	}
	return nil
}

func (s *Service) SyncUserOrder(ctx context.Context, principal Principal, orderNo string) (PaymentOrder, error) {
	order, err := s.repo.OrderByNo(ctx, strings.TrimSpace(orderNo), principal.ID, false)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	return s.syncPaymentOrder(ctx, order, principal.ID, false)
}

func (s *Service) SyncAdminOrder(ctx context.Context, principal Principal, orderNo string) (PaymentOrder, error) {
	if err := s.require(ctx, principal, "payment:order:sync"); err != nil {
		return PaymentOrder{}, err
	}
	order, err := s.repo.OrderByNo(ctx, strings.TrimSpace(orderNo), 0, true)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	return s.syncPaymentOrder(ctx, order, 0, true)
}

func (s *Service) syncPaymentOrder(ctx context.Context, order PaymentOrder, userID int64, admin bool) (PaymentOrder, error) {
	if isSyncTerminalStatus(order.Status) {
		return PaymentOrder{}, apperrors.New(consts.StatusConflict, "该订单已结束，无需向支付渠道同步")
	}
	channel, err := canonicalExternalPaymentChannel(order.PayChannel)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	switch channel {
	case ChannelAlipay:
		return s.syncOrder(ctx, order, userID, admin)
	case ChannelWechat:
		return s.syncOrderWechat(ctx, order, userID, admin)
	case ChannelHupijiaoAlipay, ChannelHupijiaoWechat:
		return s.syncOrderHupijiao(ctx, order, userID, admin)
	case ChannelTrc20:
		// TRC20 没有可主动查询的收银台：链上状态由对账 worker 落库，
		// 这里只返回本地最新状态，避免前端轮询打满 TronGrid。
		return mapOrder(s.repo.OrderByNo(ctx, order.OrderNo, userID, admin))
	case ChannelBep20:
		// BEP20 同理：链上状态由 BEP20 对账 worker 落库。
		return mapOrder(s.repo.OrderByNo(ctx, order.OrderNo, userID, admin))
	default:
		return PaymentOrder{}, mapPackageErr(ErrUnsupportedPaymentChannel)
	}
}

func (s *Service) syncOrder(ctx context.Context, order PaymentOrder, userID int64, admin bool) (PaymentOrder, error) {
	if err := requirePaymentChannel(order.PayChannel, ChannelAlipay); err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	if order.Status == "PAID" {
		return order, nil
	}
	expired, _ := s.repo.OrderExpired(ctx, order.OrderNo)
	client, cfg, err := s.alipayClientWithConfig(ctx)
	if err != nil {
		return PaymentOrder{}, asChannelConfigIssue(err)
	}
	status, err := client.Query(ctx, order.OrderNo)
	if err != nil {
		return PaymentOrder{}, err
	}
	if status.Success {
		if err := validateAlipayQuerySettlement(order, status, cfg); err != nil {
			return PaymentOrder{}, err
		}
		if err := s.repo.SettlePaidOrder(ctx, order.OrderNo, status); err != nil {
			return PaymentOrder{}, mapPackageErr(err)
		}
		s.notifyPaymentPaid(ctx, order.OrderNo)
	} else if isAlipayClosedStatus(status.TradeStatus) {
		if strings.TrimSpace(status.OutTradeNo) != order.OrderNo {
			return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "alipay query order number mismatch")
		}
		if err := s.repo.MarkOrderClosed(ctx, order.OrderNo, ChannelAlipay); err != nil {
			return PaymentOrder{}, err
		}
	} else if expired {
		if err := s.repo.MarkOrderExpired(ctx, order.OrderNo, ChannelAlipay); err != nil {
			return PaymentOrder{}, err
		}
	}
	return mapOrder(s.repo.OrderByNo(ctx, order.OrderNo, userID, admin))
}

func (s *Service) HandleAlipayNotify(ctx context.Context, values url.Values) (bool, error) {
	payload := valuesToMap(values)
	client, cfg, err := s.alipayClientWithConfig(ctx)
	if err != nil {
		return s.finishNotify(ctx, payload, false, false, masking.Error(err), false, err)
	}
	if err := client.VerifyNotify(values); err != nil {
		return s.finishNotify(ctx, payload, false, false, "verify failed", false, apperrors.New(consts.StatusBadRequest, "支付宝通知验签失败"))
	}
	if values.Get("app_id") != cfg.AppID {
		return s.finishNotify(ctx, payload, true, false, "app_id mismatch", false, apperrors.New(consts.StatusBadRequest, "支付宝通知 appId 不匹配"))
	}
	if !alipaySellerMatches(cfg.SellerID, values.Get("seller_id")) {
		return s.finishNotify(ctx, payload, true, false, "seller mismatch", false, apperrors.New(consts.StatusBadRequest, "支付宝通知商户不匹配"))
	}
	order, err := s.repo.OrderByNo(ctx, values.Get("out_trade_no"), 0, true)
	if err != nil {
		return s.finishNotify(ctx, payload, true, false, "order not found", false, mapPackageErr(err))
	}
	if err := requirePaymentChannel(order.PayChannel, ChannelAlipay); err != nil {
		return s.finishNotify(ctx, payload, true, false, "channel mismatch", false, mapPackageErr(err))
	}
	if normalizeAmount(values.Get("total_amount")) != normalizeAmount(order.PayAmount) {
		return s.finishNotify(ctx, payload, true, false, "amount mismatch", false, apperrors.New(consts.StatusBadRequest, "支付宝通知金额不匹配"))
	}
	status := values.Get("trade_status")
	if isAlipayClosedStatus(status) {
		if err := s.repo.MarkOrderClosed(ctx, order.OrderNo, ChannelAlipay); err != nil {
			return s.finishNotify(ctx, payload, true, false, masking.Error(err), false, err)
		}
		return s.finishNotify(ctx, payload, true, true, "closed", true, nil)
	}
	if !isAlipayPaidStatus(status) {
		return s.finishNotify(ctx, payload, true, false, "trade not paid", true, nil)
	}
	if strings.TrimSpace(values.Get("trade_no")) == "" {
		return s.finishNotify(ctx, payload, true, false, "trade number missing", false, apperrors.New(consts.StatusBadRequest, "alipay notify trade number missing"))
	}
	err = s.repo.SettlePaidOrder(ctx, order.OrderNo, AlipayTradeStatus{BuyerID: values.Get("buyer_id"), TradeNo: values.Get("trade_no"), Success: true})
	if err != nil {
		mappedErr := mapPackageErr(err)
		return s.finishNotify(ctx, payload, true, false, masking.Error(mappedErr), false, mappedErr)
	}
	s.notifyPaymentPaid(ctx, order.OrderNo)
	return s.finishNotify(ctx, payload, true, true, "success", true, nil)
}

func (s *Service) HandleHupijiaoNotify(ctx context.Context, channel string, values map[string]string) (bool, error) {
	payload := make(map[string]string, len(values)+4)
	for key, value := range values {
		payload[key] = value
	}
	payload["event_type"] = "HUPIJIAO_NOTIFY"
	client, _, err := s.hupijiaoClientWithConfig(ctx, channel)
	if err != nil {
		return s.finishHupijiaoNotify(ctx, payload, false, false, masking.Error(err), false, err)
	}
	notify, err := client.VerifyNotify(values)
	if err != nil {
		return s.finishHupijiaoNotify(ctx, payload, false, false, "verify failed", false, apperrors.New(consts.StatusBadRequest, "虎皮椒通知验签失败"))
	}
	payload["out_trade_no"] = notify.OutTradeNo
	payload["trade_no"] = notify.TradeNo
	payload["notify_id"] = notify.NotifyID
	order, err := s.repo.OrderByNo(ctx, notify.OutTradeNo, 0, true)
	if err != nil {
		return s.finishHupijiaoNotify(ctx, payload, true, false, "order not found", false, mapPackageErr(err))
	}
	if err := requirePaymentChannel(order.PayChannel, channel); err != nil {
		return s.finishHupijiaoNotify(ctx, payload, true, false, "channel mismatch", false, apperrors.New(consts.StatusBadRequest, "虎皮椒通知支付方式不匹配"))
	}
	if normalizeAmount(notify.Total) != normalizeAmount(order.PayAmount) {
		return s.finishHupijiaoNotify(ctx, payload, true, false, "amount mismatch", false, apperrors.New(consts.StatusBadRequest, "虎皮椒通知金额不匹配"))
	}
	switch notify.Status {
	case "CD":
		if err := s.repo.MarkOrderClosed(ctx, order.OrderNo, channel); err != nil {
			return s.finishHupijiaoNotify(ctx, payload, true, false, masking.Error(err), false, err)
		}
		return s.finishHupijiaoNotify(ctx, payload, true, true, "closed", true, nil)
	case "WP":
		return s.finishHupijiaoNotify(ctx, payload, true, true, "waiting", true, nil)
	case "OD":
		if strings.TrimSpace(notify.TradeNo) == "" {
			return s.finishHupijiaoNotify(ctx, payload, true, false, "trade number missing", false, apperrors.New(consts.StatusBadRequest, "虎皮椒通知交易号缺失"))
		}
		status := PayTradeStatus{
			OutTradeNo: notify.OutTradeNo,
			TradeNo:    notify.TradeNo,
			TradeState: notify.Status,
			Total:      notify.Total,
			Success:    true,
		}
		if err := s.repo.SettleHupijiaoPaidOrder(ctx, channel, order.OrderNo, status); err != nil {
			mappedErr := mapPackageErr(err)
			return s.finishHupijiaoNotify(ctx, payload, true, false, masking.Error(mappedErr), false, mappedErr)
		}
		s.notifyPaymentPaid(ctx, order.OrderNo)
		return s.finishHupijiaoNotify(ctx, payload, true, true, "success", true, nil)
	default:
		return s.finishHupijiaoNotify(ctx, payload, true, false, "unknown status", false, apperrors.New(consts.StatusBadRequest, "虎皮椒通知状态不合法"))
	}
}

func (s *Service) finishHupijiaoNotify(ctx context.Context, payload map[string]string, verified, processed bool, message string, result bool, resultErr error) (bool, error) {
	message = masking.ErrorText(message)
	if err := s.repo.InsertHupijiaoNotifyLog(ctx, payload, verified, processed, message); err != nil {
		return false, apperrors.Wrap(consts.StatusServiceUnavailable, "虎皮椒通知审计日志写入失败", errors.Join(resultErr, err))
	}
	return result, resultErr
}

func (s *Service) finishNotify(ctx context.Context, payload map[string]string, verified, processed bool, message string, result bool, resultErr error) (bool, error) {
	message = masking.ErrorText(message)
	if err := s.notifyRepo.InsertNotifyLog(ctx, payload, verified, processed, message); err != nil {
		return false, apperrors.Wrap(consts.StatusServiceUnavailable, "支付宝通知审计日志写入失败", errors.Join(resultErr, err))
	}
	return result, resultErr
}

func (s *Service) alipayClient(ctx context.Context) (*AlipayClient, error) {
	client, _, err := s.alipayClientWithConfig(ctx)
	return client, err
}

func validateAlipayQuerySettlement(order PaymentOrder, status AlipayTradeStatus, cfg AlipayConfig) error {
	if strings.TrimSpace(status.OutTradeNo) != order.OrderNo {
		return apperrors.New(consts.StatusBadRequest, "alipay query order number mismatch")
	}
	if strings.TrimSpace(status.TradeNo) == "" {
		return apperrors.New(consts.StatusServiceUnavailable, "alipay query trade number missing")
	}
	if strings.TrimSpace(status.TotalAmount) == "" {
		return apperrors.New(consts.StatusServiceUnavailable, "alipay query amount missing")
	}
	if normalizeAmount(status.TotalAmount) != normalizeAmount(order.PayAmount) {
		return apperrors.New(consts.StatusBadRequest, "alipay query amount mismatch")
	}
	if !alipaySellerMatches(cfg.SellerID, status.SellerID) {
		return apperrors.New(consts.StatusBadRequest, "alipay query seller mismatch")
	}
	return nil
}

func alipaySellerMatches(configuredSellerID, actualSellerID string) bool {
	configuredSellerID = strings.TrimSpace(configuredSellerID)
	if configuredSellerID == "" {
		return true
	}
	return strings.TrimSpace(actualSellerID) == configuredSellerID
}

func isAlipayPaidStatus(status string) bool {
	status = strings.TrimSpace(status)
	return status == "TRADE_SUCCESS" || status == "TRADE_FINISHED"
}

func isAlipayClosedStatus(status string) bool {
	return strings.TrimSpace(status) == "TRADE_CLOSED"
}

func (s *Service) alipayClientWithConfig(ctx context.Context) (*AlipayClient, AlipayConfig, error) {
	stored, err := s.configRepo.AlipayConfig(ctx)
	if err != nil {
		return nil, AlipayConfig{}, err
	}
	if !stored.Enabled {
		return nil, AlipayConfig{}, apperrors.New(consts.StatusServiceUnavailable, "支付宝支付未启用")
	}
	if strings.TrimSpace(stored.SellerID) == "" {
		return nil, AlipayConfig{}, apperrors.New(consts.StatusServiceUnavailable, "支付宝商户 sellerId 未配置")
	}
	privateKey, err := s.cipher.Decrypt(stored.PrivateKeyCipher)
	if err != nil {
		return nil, AlipayConfig{}, mapSecretErr(err)
	}
	cfg := stored.AlipayConfig
	cfg.AppPrivateKey = privateKey
	client, err := NewAlipayClient(cfg)
	if err != nil {
		return nil, AlipayConfig{}, apperrors.Wrap(consts.StatusServiceUnavailable, "支付宝配置不可用", err)
	}
	return client, cfg, nil
}

func (s *Service) require(ctx context.Context, principal Principal, code string) error {
	return s.authz.Require(ctx, rbac.Principal{ID: principal.ID, Username: principal.Username, Roles: principal.Roles, TokenVersion: principal.TokenVersion}, code)
}

func normalizeAlipayConfig(req SaveAlipayConfigRequest) (AlipayConfig, error) {
	mode := strings.ToUpper(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "SANDBOX"
	}
	if mode != "SANDBOX" && mode != "PRODUCTION" {
		return AlipayConfig{}, apperrors.New(consts.StatusBadRequest, "支付宝模式不合法")
	}
	gateway := strings.TrimSpace(req.GatewayURL)
	officialGateway := defaultGateway(mode)
	if gateway != "" && gateway != officialGateway {
		return AlipayConfig{}, apperrors.New(consts.StatusBadRequest, "支付宝网关必须使用当前模式对应的官方地址")
	}
	gateway = officialGateway
	if req.Enabled && strings.TrimSpace(req.AppID) == "" {
		return AlipayConfig{}, apperrors.New(consts.StatusBadRequest, "支付宝 appId 不能为空")
	}
	if req.Enabled && strings.TrimSpace(req.SellerID) == "" {
		return AlipayConfig{}, apperrors.New(consts.StatusBadRequest, "支付宝商户 sellerId 不能为空")
	}
	notifyURL := strings.TrimSpace(req.NotifyURL)
	if err := validateAlipayCallbackURL(notifyURL, "支付宝异步通知地址", false); err != nil {
		return AlipayConfig{}, err
	}
	returnURL := strings.TrimSpace(req.ReturnURL)
	if err := validateAlipayCallbackURL(returnURL, "支付宝同步跳转地址", true); err != nil {
		return AlipayConfig{}, err
	}
	return AlipayConfig{
		AlipayPublicKey: strings.TrimSpace(req.AlipayPublicKey),
		AppID:           strings.TrimSpace(req.AppID),
		Enabled:         req.Enabled,
		GatewayURL:      gateway,
		Mode:            mode,
		NotifyURL:       notifyURL,
		ReturnURL:       returnURL,
		SellerID:        strings.TrimSpace(req.SellerID),
	}, nil
}

func validateAlipayCallbackURL(raw, field string, allowFragment bool) error {
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || !parsed.IsAbs() || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" {
		return apperrors.New(consts.StatusBadRequest, field+"必须是无账号密码的 HTTPS 绝对地址")
	}
	if !allowFragment && parsed.Fragment != "" {
		return apperrors.New(consts.StatusBadRequest, field+"不能包含 URL 片段")
	}
	return nil
}

func normalizePreset(req SaveRechargePresetRequest) (RechargePreset, error) {
	amount, err := normalizeMoney(req.Amount)
	if err != nil {
		return RechargePreset{}, err
	}
	gift, err := normalizeMoney(req.GiftAmount)
	if err != nil {
		return RechargePreset{}, err
	}
	status := 1
	if req.Status != nil {
		status = *req.Status
	}
	name := strings.TrimSpace(req.Name)
	amountRat, amountOK := new(big.Rat).SetString(amount)
	giftRat, giftOK := new(big.Rat).SetString(gift)
	creditWithinRange := amountOK && giftOK && new(big.Rat).Add(amountRat, giftRat).Cmp(maxPaymentMoney) <= 0
	if name == "" || utf8.RuneCountInString(name) > maxRechargePresetName || req.SortOrder < 0 || req.SortOrder > maxRechargePresetSortNo || status < 0 || status > 1 || normalizeAmount(amount) == "0.0000" || !creditWithinRange {
		return RechargePreset{}, apperrors.New(consts.StatusBadRequest, "充值预设参数不合法")
	}
	return RechargePreset{Name: name, Amount: amount, GiftAmount: gift, SortOrder: req.SortOrder, Status: status}, nil
}

func normalizeProduct(product string) (string, error) {
	product = strings.ToUpper(strings.TrimSpace(product))
	if product == "" {
		product = "PAGE"
	}
	switch product {
	case "PAGE", "WAP", "FACE":
		return product, nil
	default:
		return "", apperrors.New(consts.StatusBadRequest, "支付宝产品类型不合法")
	}
}

func normalizeMoney(value any) (string, error) {
	if value == nil {
		return "0.0000", nil
	}
	text := strings.TrimSpace(fmt.Sprintf("%v", value))
	if text == "" {
		text = "0"
	}
	rat, err := decimalvalue.ParseNonNegative(text)
	if errors.Is(err, decimalvalue.ErrNegative) {
		return "", apperrors.New(consts.StatusBadRequest, "金额不能为负数")
	}
	if errors.Is(err, decimalvalue.ErrExponentRange) || errors.Is(err, decimalvalue.ErrTextTooLong) {
		return "", apperrors.New(consts.StatusBadRequest, "金额超过系统允许范围")
	}
	if err != nil {
		return "", apperrors.New(consts.StatusBadRequest, "金额格式不合法")
	}
	if rat.Cmp(maxPaymentMoney) > 0 {
		return "", apperrors.New(consts.StatusBadRequest, "金额超过系统允许范围")
	}
	return rat.FloatString(4), nil
}

func normalizeAmount(value string) string {
	out, err := normalizeMoney(value)
	if err != nil {
		return ""
	}
	return out
}

func newOrderNo() (string, error) {
	return newOrderNoFrom(rand.Reader, time.Now())
}

func newOrderNoFrom(reader io.Reader, now time.Time) (string, error) {
	data := make([]byte, 6)
	if _, err := io.ReadFull(reader, data); err != nil {
		return "", err
	}
	return "BP" + now.Format("20060102150405") + strings.ToUpper(hex.EncodeToString(data)), nil
}

func mapSecretErr(err error) error {
	if errors.Is(err, ErrPaymentSecretMissing) {
		return apperrors.New(consts.StatusServiceUnavailable, "PAYMENT_CONFIG_SECRET 未配置")
	}
	return err
}

func mapNotFound(err error, message string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return apperrors.New(consts.StatusNotFound, message)
	}
	return err
}

func mapPackageErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return apperrors.New(consts.StatusNotFound, "订单或商品不存在")
	}
	if errors.Is(err, ErrPaymentChannelMismatch) || errors.Is(err, ErrUnsupportedPaymentChannel) {
		return apperrors.Wrap(consts.StatusBadRequest, "支付渠道与订单不匹配或不受支持", err)
	}
	if errors.Is(err, ErrBalanceCapacityExceeded) {
		return apperrors.New(consts.StatusConflict, "账户余额已达到系统上限，支付暂未结算，请联系管理员处理")
	}
	if errors.Is(err, ErrPointBalanceLimit) {
		return apperrors.New(consts.StatusConflict, "账户点数已达到系统上限，支付暂未结算，请联系管理员处理")
	}
	if errors.Is(err, ErrPackageSoldOut) {
		return apperrors.New(consts.StatusConflict, "该套餐已售罄")
	}
	if errors.Is(err, ErrPackageOffSale) {
		return apperrors.New(consts.StatusConflict, "该套餐暂未开售或已停止售卖")
	}
	if errors.Is(err, ErrPackageUserLimit) {
		return apperrors.New(consts.StatusConflict, "已达到该套餐的每人限购数量")
	}
	if errors.Is(err, ErrPackageRoleRestricted) {
		return apperrors.New(consts.StatusConflict, "当前用户组不可购买该套餐")
	}
	if errors.Is(err, ErrPackageStockBelowUsed) {
		return apperrors.New(consts.StatusConflict, "限量份数不能低于已占用份数")
	}
	if errors.Is(err, ErrPaymentOrderPending) {
		return apperrors.New(consts.StatusConflict, "您有相同商品的待支付订单，请继续支付旧订单或确认开启新订单")
	}
	if errors.Is(err, ErrPaymentOrderExpired) || errors.Is(err, ErrPaymentOrderNotReusable) {
		return apperrors.New(consts.StatusConflict, "订单已关闭或已过期，请重新下单")
	}
	return err
}

func mapOrder(order PaymentOrder, err error) (PaymentOrder, error) {
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	return paymentOrderForResponse(order), nil
}

func valuesToMap(values url.Values) map[string]string {
	raw := make(map[string]string, len(values))
	for key := range values {
		raw[key] = values.Get(key)
	}
	return sanitizeNotifyValues(raw)
}

func sanitizeNotifyValues(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		normalizedKey := strings.ToLower(strings.TrimSpace(key))
		switch normalizedKey {
		case "appsecret", "app_secret", "buyer_id", "buyer_logon_id", "buyer_open_id", "fund_bill_list", "hash", "passback_params", "seller_email", "sign", "voucher_detail_list":
			value = "***"
		default:
			// Payment callbacks are persisted for replay/audit diagnostics.  A
			// provider may echo arbitrary text (including a callback URL or a
			// nested user-supplied URL), so never write those endpoint details
			// verbatim even when the field is not one of the fixed payment
			// secrets above.  ErrorText also masks credentials embedded in a
			// free-form callback value. Preserve provider identity fields (order,
			// trade, and notification IDs) byte-for-byte apart from endpoint
			// removal: some providers use an 11-digit numeric ID, which must not
			// be mistaken for a mobile number by the general error masker or
			// replay/audit correlation would break.
			if notifyIdentityKey(normalizedKey) {
				value = masking.EndpointText(value)
			} else {
				value = masking.ErrorText(value)
			}
		}
		out[key] = value
	}
	return out
}

func notifyIdentityKey(key string) bool {
	switch key {
	case "out_trade_no", "trade_no", "notify_id", "transaction_id", "open_order_id", "trade_order_id":
		return true
	default:
		return false
	}
}
