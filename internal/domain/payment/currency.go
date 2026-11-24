package payment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode/utf8"

	"bugpk-api/internal/pkg/decimalvalue"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// 站点结算货币（CNY/USD）。货币是后台单选配置：
// CNY 为默认值，行为与历史完全一致（pay_amount 即商品原价）；
// USD 模式下下单时按 usd_cny_rate 把商品原价（人民币口径）换算成美元
// 写入 pay_amount，原价保留在 origin_amount 供审计与对账。
// 商品/充值档位的额度口径（余额、点数）不受货币切换影响。
const (
	CurrencyCNY = "CNY"
	CurrencyUSD = "USD"
)

// defaultUSDCNYRate 管理端未显式设置汇率时的兜底值（1 美元兑人民币）。
const defaultUSDCNYRate = "7.2000"

// maxPayGoodsNameBytes 是发给渠道的商品名长度上限。虎皮椒官方限制 title
// 不超过 127 字节，直连微信/支付宝的 description/subject 也在同一量级，
// 因此按最严的虎皮椒口径校验，避免配置能存下去但下单被渠道拒。
const maxPayGoodsNameBytes = 127

// maxCurrencyDisplayNameRunes 是用户端货币展示名称的长度上限。
// 该名称只替换 ¥/人民币 的称呼，不参与金额换算，限制在 16 个字符内避免撑破表格与标签。
const maxCurrencyDisplayNameRunes = 16

// PaymentCurrencyConfig 站点结算货币配置。
// GoodsName 是发给支付渠道的商品名（风控友好的中性名称）：非空时覆盖订单明细名，
// 为空时沿用 bp_payment_order.subject。明细名本身不受影响，对账与通知照旧。
// DisplayName 是用户端货币称呼（如积分、赞助额度），只改展示文案：
// 金额、汇率换算与后台财务口径全部保持不变。
type PaymentCurrencyConfig struct {
	Currency    string `json:"currency"`
	USDCNYRate  string `json:"usdCnyRate"`
	GoodsName   string `json:"goodsName,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}

// SavePaymentCurrencyConfigRequest 货币配置保存请求。
type SavePaymentCurrencyConfigRequest struct {
	Currency    string `json:"currency"`
	USDCNYRate  any    `json:"usdCnyRate,omitempty"`
	GoodsName   string `json:"goodsName,omitempty"`
	DisplayName string `json:"displayName,omitempty"`
}

func defaultPaymentCurrencyConfig() PaymentCurrencyConfig {
	return PaymentCurrencyConfig{Currency: CurrencyCNY, USDCNYRate: defaultUSDCNYRate}
}

// normalizePaymentCurrencyConfig 规整读库结果：货币白名单、汇率兜底、商品名去空白。
func normalizePaymentCurrencyConfig(cfg PaymentCurrencyConfig) PaymentCurrencyConfig {
	cfg.Currency = strings.ToUpper(strings.TrimSpace(cfg.Currency))
	if cfg.Currency != CurrencyUSD {
		cfg.Currency = CurrencyCNY
	}
	rate := strings.TrimSpace(cfg.USDCNYRate)
	if rate == "" {
		rate = defaultUSDCNYRate
	}
	cfg.USDCNYRate = rate
	cfg.GoodsName = strings.TrimSpace(cfg.GoodsName)
	cfg.DisplayName = strings.TrimSpace(cfg.DisplayName)
	return cfg
}

// CurrencyConfig 读取货币配置；行不存在（迁移未跑或首次部署）返回默认 CNY。
func (r *Repository) CurrencyConfig(ctx context.Context) (PaymentCurrencyConfig, error) {
	var cfg PaymentCurrencyConfig
	var rate string
	err := r.db.QueryRowContext(ctx,
		`select currency,cast(usd_cny_rate as char),coalesce(goods_name,''),coalesce(display_name,'') from bp_payment_currency_config where id=1`,
	).Scan(&cfg.Currency, &rate, &cfg.GoodsName, &cfg.DisplayName)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultPaymentCurrencyConfig(), nil
	}
	if err != nil {
		return PaymentCurrencyConfig{}, err
	}
	cfg.USDCNYRate = rate
	return normalizePaymentCurrencyConfig(cfg), nil
}

// SaveCurrencyConfig 保存货币配置（单例 upsert）。
func (r *Repository) SaveCurrencyConfig(ctx context.Context, cfg PaymentCurrencyConfig) error {
	_, err := r.db.ExecContext(ctx, `
		insert into bp_payment_currency_config(id,currency,usd_cny_rate,goods_name,display_name)
		values(1,?,?,?,?)
		on duplicate key update currency=values(currency),usd_cny_rate=values(usd_cny_rate),goods_name=values(goods_name),display_name=values(display_name)
	`, cfg.Currency, cfg.USDCNYRate, cfg.GoodsName, cfg.DisplayName)
	return err
}

// PaymentCurrency 管理端读取货币配置。
func (s *Service) PaymentCurrency(ctx context.Context, principal Principal) (PaymentCurrencyConfig, error) {
	if err := s.require(ctx, principal, "payment:config:view"); err != nil {
		return PaymentCurrencyConfig{}, err
	}
	return s.repo.CurrencyConfig(ctx)
}

// PublicCurrencyConfig 公开只读的站点货币与汇率。
// 前台价格展示（含未登录的公开页面）需要按当前汇率换算，汇率随运营调整，
// 因此不能由前端写死或只在下单时读取。
// 商品名只服务于渠道下单请求，不属于公开配置，这里抹掉避免多一个公开可读字段。
func (s *Service) PublicCurrencyConfig(ctx context.Context) (PaymentCurrencyConfig, error) {
	cfg, err := s.repo.CurrencyConfig(ctx)
	if err != nil {
		return PaymentCurrencyConfig{}, err
	}
	cfg.GoodsName = ""
	return cfg, nil
}

// SavePaymentCurrency 管理端保存货币配置。
func (s *Service) SavePaymentCurrency(ctx context.Context, principal Principal, req SavePaymentCurrencyConfigRequest) error {
	if err := s.require(ctx, principal, "payment:config:update"); err != nil {
		return err
	}
	cfg, err := normalizeSavePaymentCurrencyRequest(req)
	if err != nil {
		return err
	}
	return s.repo.SaveCurrencyConfig(ctx, cfg)
}

// normalizeSavePaymentCurrencyRequest 校验货币白名单与汇率合法性。
// CNY 模式下汇率仍允许保存（切换回 USD 时直接生效），缺省用兜底值。
func normalizeSavePaymentCurrencyRequest(req SavePaymentCurrencyConfigRequest) (PaymentCurrencyConfig, error) {
	cfg := defaultPaymentCurrencyConfig()
	cfg.Currency = strings.ToUpper(strings.TrimSpace(req.Currency))
	switch cfg.Currency {
	case CurrencyCNY:
	case CurrencyUSD:
	default:
		return PaymentCurrencyConfig{}, apperrors.New(consts.StatusBadRequest, "货币类型仅支持 CNY 或 USD")
	}
	if req.USDCNYRate != nil {
		rateText := strings.TrimSpace(fmt.Sprintf("%v", req.USDCNYRate))
		if rateText != "" {
			rate, err := decimalvalue.ParseNonNegative(rateText)
			if err != nil {
				return PaymentCurrencyConfig{}, apperrors.New(consts.StatusBadRequest, "汇率格式不合法")
			}
			if rate.Sign() == 0 {
				return PaymentCurrencyConfig{}, apperrors.New(consts.StatusBadRequest, "汇率必须大于 0")
			}
			if rate.Cmp(maxPaymentMoney) > 0 {
				return PaymentCurrencyConfig{}, apperrors.New(consts.StatusBadRequest, "汇率超过系统允许范围")
			}
			cfg.USDCNYRate = rate.FloatString(4)
		}
	}
	name := strings.TrimSpace(req.GoodsName)
	if name != "" && !utf8.ValidString(name) {
		return PaymentCurrencyConfig{}, apperrors.New(consts.StatusBadRequest, "支付商品名格式不合法")
	}
	if len(name) > maxPayGoodsNameBytes {
		return PaymentCurrencyConfig{}, apperrors.New(consts.StatusBadRequest, "支付商品名过长（上限 127 字节，约 42 个汉字）")
	}
	cfg.GoodsName = name
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName != "" {
		if !utf8.ValidString(displayName) || strings.ContainsAny(displayName, "\r\n\t") {
			return PaymentCurrencyConfig{}, apperrors.New(consts.StatusBadRequest, "货币展示名称格式不合法")
		}
		if utf8.RuneCountInString(displayName) > maxCurrencyDisplayNameRunes {
			return PaymentCurrencyConfig{}, apperrors.New(consts.StatusBadRequest, "货币展示名称过长（上限 16 个字符）")
		}
	}
	cfg.DisplayName = displayName
	return cfg, nil
}

// payGoodsName 决定发给支付渠道的商品名：站点配置了中性名就用它，否则沿用订单明细名。
// 只作用于渠道请求，bp_payment_order.subject 仍是「余额充值-50元」这类可对账的明细名。
func payGoodsName(configured, subject string) string {
	if name := strings.TrimSpace(configured); name != "" {
		return name
	}
	return strings.TrimSpace(subject)
}

// applyPayGoodsName 在调用渠道下单前改写请求用的商品名。
// 读配置失败会让整笔下单失败：宁可不下单，也不能带着不可控的名称去打渠道，
// 那正是本次改动要规避的风控面。
func (s *Service) applyPayGoodsName(ctx context.Context, order *PaymentOrder) error {
	cfg, err := s.repo.CurrencyConfig(ctx)
	if err != nil {
		return err
	}
	order.Subject = payGoodsName(cfg.GoodsName, order.Subject)
	return nil
}

// applyOrderCurrency 在建单前按站点结算货币改写 seed：
// - CNY：仅记录货币标签，pay_amount 不动（历史行为零变化）；
// - USD：pay_amount 换算为美元，原价进 OriginAmount 一起落库。
// 余额/点数等入账口径取自商品定义，不做换算。
func (s *Service) applyOrderCurrency(ctx context.Context, seed *OrderSeed) error {
	cfg, err := s.repo.CurrencyConfig(ctx)
	if err != nil {
		return err
	}
	seed.Currency = cfg.Currency
	if cfg.Currency != CurrencyUSD {
		seed.Currency = CurrencyCNY
		return nil
	}
	seed.OriginAmount = seed.PayAmount
	converted, err := convertCNYToUSD(seed.PayAmount, cfg.USDCNYRate)
	if err != nil {
		return err
	}
	seed.PayAmount = converted
	return nil
}

// rechargeCreditBase 返回充值入账使用的余额基数（人民币口径）。
// USD 订单的 pay_amount 已按汇率换算成美元，但余额始终以人民币基数记账，
// 因此必须使用下单时留档的 origin_amount；缺失时才回退回 pay_amount。
func rechargeCreditBase(currency, payAmount, originAmount string) string {
	if strings.EqualFold(strings.TrimSpace(currency), CurrencyUSD) {
		if origin := strings.TrimSpace(originAmount); origin != "" {
			return origin
		}
	}
	return payAmount
}

// convertCNYToUSD 按汇率把人民币金额换算成美元，四舍五入到 4 位小数，
// 结果向下保底 0.0100、向上受 maxPaymentMoney 约束（与 pay_amount 列口径一致）。
func convertCNYToUSD(originCNY, rate string) (string, error) {
	origin, err := decimalvalue.ParseNonNegative(strings.TrimSpace(originCNY))
	if err != nil {
		return "", apperrors.New(consts.StatusBadRequest, "订单金额格式不合法")
	}
	exchange, err := decimalvalue.ParseNonNegative(strings.TrimSpace(rate))
	if err != nil || exchange.Sign() == 0 {
		return "", apperrors.New(consts.StatusServiceUnavailable, "美元汇率未配置或不可用")
	}
	usd := new(big.Rat).Quo(origin, exchange)
	if usd.Cmp(maxPaymentMoney) > 0 {
		return "", apperrors.New(consts.StatusBadRequest, "换算后金额超过系统允许范围")
	}
	text := usd.FloatString(4)
	// 保底一分钱：避免极小金额换算后四舍五入为 0，通道拒单。
	if minUSD, _ := new(big.Rat).SetString("0.01"); usd.Sign() > 0 && usd.Cmp(minUSD) < 0 {
		return "0.0100", nil
	}
	return text, nil
}
