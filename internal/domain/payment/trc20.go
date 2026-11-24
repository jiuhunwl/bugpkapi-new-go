package payment

import (
	"context"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"time"

	"bugpk-api/internal/pkg/decimalvalue"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// TRC20/USDT 链上充值（海外站）。
// 订单金额在预设价换算成 USD 后追加 1~99 分随机分位，作为链上对账的
// 唯一金额；余额仍按下单时的人民币原价（origin_amount）入账。
const (
	ChannelTrc20              = "TRC20"
	trc20NetworkTron          = "TRON"
	trc20USDTContract         = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	trc20DefaultConfirmations = 1
	trc20MaxConfirmations     = trc20DefaultConfirmations
	trc20MaxAmount            = "1000000"
	trc20ExpireMinutes        = 60
	trc20ScanWindow           = 24 * time.Hour
	trc20SubmitCooldown       = 20 * time.Second
)

var (
	trc20AddressPattern = regexp.MustCompile(`^T[1-9A-HJ-NP-Za-km-z]{33}$`)
	trc20TxIDPattern    = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// Trc20Config 管理端/公开只读的 TRC20 通道配置。
type Trc20Config struct {
	Enabled          bool   `json:"enabled"`
	Network          string `json:"network"`
	Address          string `json:"address"`
	ContractAddress  string `json:"contractAddress"`
	MinAmount        string `json:"minAmount"`
	MaxAmount        string `json:"maxAmount"`
	Confirmations    int    `json:"confirmations"`
	APIKeyConfigured bool   `json:"apiKeyConfigured"`
}

type SaveTrc20ConfigRequest struct {
	Enabled       bool   `json:"enabled"`
	Address       string `json:"address"`
	APIKey        string `json:"apiKey"`
	MinAmount     any    `json:"minAmount"`
	MaxAmount     any    `json:"maxAmount"`
	Confirmations int    `json:"confirmations"`
}

type SubmitTrc20TxIDRequest struct {
	TxID string `json:"txid"`
}

type storedTrc20Config struct {
	Trc20Config
	APIKeyCipher      string
	CursorTimestampMS int64
	RetryCursorID     int64
}

func defaultTrc20Config() Trc20Config {
	return Trc20Config{
		Network:         trc20NetworkTron,
		ContractAddress: trc20USDTContract,
		MinAmount:       "5.000000",
		MaxAmount:       "10000.000000",
		Confirmations:   trc20DefaultConfirmations,
	}
}

// Trc20Config 管理端读取配置（不下发 API Key 明文）。
func (s *Service) Trc20Config(ctx context.Context, principal Principal) (Trc20Config, error) {
	if err := s.require(ctx, principal, "payment:config:view"); err != nil {
		return Trc20Config{}, err
	}
	stored, err := s.repo.Trc20Config(ctx)
	if err != nil {
		return Trc20Config{}, err
	}
	return trc20PublicConfig(stored), nil
}

func trc20PublicConfig(stored storedTrc20Config) Trc20Config {
	cfg := stored.Trc20Config
	cfg.APIKeyConfigured = strings.TrimSpace(stored.APIKeyCipher) != ""
	if cfg.Network == "" {
		cfg.Network = trc20NetworkTron
	}
	if cfg.ContractAddress == "" {
		cfg.ContractAddress = trc20USDTContract
	}
	if cfg.MinAmount == "" {
		cfg.MinAmount = defaultTrc20Config().MinAmount
	}
	if cfg.MaxAmount == "" {
		cfg.MaxAmount = defaultTrc20Config().MaxAmount
	}
	if cfg.Confirmations <= 0 {
		cfg.Confirmations = trc20DefaultConfirmations
	}
	return cfg
}

// SaveTrc20Config 管理端保存配置；API Key 留空表示保持不变。
func (s *Service) SaveTrc20Config(ctx context.Context, principal Principal, req SaveTrc20ConfigRequest) error {
	if err := s.require(ctx, principal, "payment:config:update"); err != nil {
		return err
	}
	cfg, apiKey, err := normalizeTrc20ConfigRequest(req)
	if err != nil {
		return err
	}
	if cfg.Enabled {
		currencyCfg, err := s.repo.CurrencyConfig(ctx)
		if err != nil {
			return err
		}
		if !strings.EqualFold(currencyCfg.Currency, CurrencyUSD) {
			return apperrors.New(consts.StatusBadRequest, "请先将站点结算货币设置为 USD 再开启 TRC20")
		}
	}
	apiKeyCipher := ""
	updateKey := false
	if apiKey != "" {
		if s.cipher == nil {
			return apperrors.New(consts.StatusServiceUnavailable, "支付配置加密不可用")
		}
		encrypted, err := s.cipher.Encrypt(apiKey)
		if err != nil {
			return apperrors.Wrap(consts.StatusServiceUnavailable, "支付配置加密不可用", err)
		}
		apiKeyCipher = encrypted
		updateKey = true
	}
	return s.repo.SaveTrc20Config(ctx, cfg, apiKeyCipher, updateKey)
}

func normalizeTrc20ConfigRequest(req SaveTrc20ConfigRequest) (Trc20Config, string, error) {
	cfg := defaultTrc20Config()
	cfg.Enabled = req.Enabled
	cfg.Address = strings.TrimSpace(req.Address)
	if cfg.Address != "" && !trc20AddressPattern.MatchString(cfg.Address) {
		return Trc20Config{}, "", apperrors.New(consts.StatusBadRequest, "TRC20 收款地址格式不正确")
	}
	if cfg.Enabled && cfg.Address == "" {
		return Trc20Config{}, "", apperrors.New(consts.StatusBadRequest, "开启 TRC20 充值前请先填写收款地址")
	}
	minAmount, err := decimalvalue.ParseNonNegative(strings.TrimSpace(toStringValue(req.MinAmount)))
	if err != nil || minAmount.Sign() <= 0 {
		minAmount, _ = decimalvalue.ParseNonNegative(cfg.MinAmount)
	}
	maxAmount, err := decimalvalue.ParseNonNegative(strings.TrimSpace(toStringValue(req.MaxAmount)))
	if err != nil || maxAmount.Sign() <= 0 {
		maxAmount, _ = decimalvalue.ParseNonNegative(cfg.MaxAmount)
	}
	if maxAmount.Cmp(minAmount) < 0 {
		return Trc20Config{}, "", apperrors.New(consts.StatusBadRequest, "单笔上限不能小于最小充值额")
	}
	if limit, _ := decimalvalue.ParseNonNegative(trc20MaxAmount); maxAmount.Cmp(limit) > 0 {
		return Trc20Config{}, "", apperrors.New(consts.StatusBadRequest, "单笔上限超过允许范围")
	}
	cfg.MinAmount = minAmount.FloatString(6)
	cfg.MaxAmount = maxAmount.FloatString(6)
	cfg.Confirmations = req.Confirmations
	if cfg.Confirmations <= 0 {
		cfg.Confirmations = trc20DefaultConfirmations
	}
	if cfg.Confirmations > trc20MaxConfirmations {
		return Trc20Config{}, "", apperrors.New(consts.StatusBadRequest, "当前 TRC20 仅支持提供方已确认状态（确认策略 1），不支持自定义区块深度")
	}
	return cfg, strings.TrimSpace(req.APIKey), nil
}

func toStringValue(value any) string {
	if value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case float64:
		return new(big.Rat).SetFloat64(typed).FloatString(6)
	case int:
		return new(big.Rat).SetInt64(int64(typed)).FloatString(6)
	case int64:
		return new(big.Rat).SetInt64(typed).FloatString(6)
	default:
		return ""
	}
}

// trc20Status 供充值能力接口使用：未启用或未配置地址时视为不可用。
func (s *Service) trc20Status(ctx context.Context) (Trc20Config, bool) {
	stored, err := s.repo.Trc20Config(ctx)
	if err != nil {
		return Trc20Config{}, false
	}
	cfg := trc20PublicConfig(stored)
	return cfg, cfg.Enabled && cfg.Address != "" && cfg.Confirmations == trc20DefaultConfirmations
}

// SubmitTrc20TxID 用户提交链上交易号：触发一次对账，只有链上确认的转账
// 才会入账；重复提交有冷却，避免把 TronGrid 打满。
func (s *Service) SubmitTrc20TxID(ctx context.Context, principal Principal, orderNo, rawTxID string) (PaymentOrder, error) {
	order, err := s.repo.OrderByNo(ctx, strings.TrimSpace(orderNo), principal.ID, false)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	if !strings.EqualFold(order.PayChannel, ChannelTrc20) {
		return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "该订单不是 TRC20 充值订单")
	}
	txid := strings.TrimSpace(rawTxID)
	if !trc20TxIDPattern.MatchString(txid) {
		return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "交易号格式不正确")
	}
	if order.Status == "PAID" {
		if strings.EqualFold(order.ChainTxID, txid) {
			return order, nil
		}
		return PaymentOrder{}, apperrors.New(consts.StatusConflict, "订单已使用其他交易号入账")
	}
	switch order.Status {
	case "CANCELLED", "CLOSED", "FAILED":
		return PaymentOrder{}, apperrors.New(consts.StatusConflict, "该订单已结束，无法提交交易号")
	}
	if !allowTrc20Submit(order.OrderNo) {
		return PaymentOrder{}, apperrors.New(consts.StatusConflict, "提交过于频繁，请稍后再试")
	}
	if err := NewTrc20Reconciler(s).ReconcileOnce(ctx); err != nil {
		return PaymentOrder{}, apperrors.New(consts.StatusServiceUnavailable, "链上查询暂时不可用，请稍后重试")
	}
	fresh, err := s.repo.OrderByNo(ctx, order.OrderNo, principal.ID, false)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	// 对账可能已用同一笔链上转账把订单置为 PAID；此时以到账结果为准。
	if fresh.Status == "PAID" {
		return fresh, nil
	}
	// 用户主动提交 txid 是最强证据：直接按 txid 在订单自己的收款地址上找这笔入账。
	// 常规对账只扫当前配置地址，改过地址之后旧订单的钱永远扫不到——这条路径必须
	// 独立于对账游标，否则用户拿 txid 来也核不出到账。
	settled, err := s.settleTrc20ByTxID(ctx, fresh, txid)
	if err != nil {
		var business *apperrors.BusinessError
		if errors.As(err, &business) {
			return PaymentOrder{}, err
		}
		return PaymentOrder{}, apperrors.New(consts.StatusServiceUnavailable, "链上查询暂时不可用，请稍后重试")
	}
	if settled.Status == "PAID" {
		return settled, nil
	}
	// 查不到时要区分两种情况，否则用户永远只收到一句「暂未确认」：
	// 一是交易还没上链（等一等就好）；二是这笔钱压根不是链上转账（例如交易所
	// 站内转账），此时无论等多久都不会被查到，必须让人改走人工入账。
	verdict := apperrors.New(consts.StatusConflict, "链上暂未确认该交易，请稍后重试")
	expectedAddress := strings.TrimSpace(fresh.ChainAddress)
	if expectedAddress == "" {
		if stored, cfgErr := s.repo.Trc20Config(ctx); cfgErr == nil {
			expectedAddress = strings.TrimSpace(stored.Address)
		}
	}
	explanation, err := s.explainTrc20TxID(ctx, txid)
	if err != nil {
		return PaymentOrder{}, verdict
	}
	switch {
	case !explanation.Found:
		// 回执为空 = 该交易号在 TRON 链上不存在。站内/链下转账永远不会出现，
		// 再等也不会有结果。
		return PaymentOrder{}, apperrors.NewWithData(consts.StatusConflict,
			"该交易号在 TRON 链上不存在：如果是从交易所站内转账或走的是其他网络，链上永远查不到，请联系客服人工入账",
			map[string]any{"reason": "CHAIN_TX_NOT_FOUND", "manualReviewRequired": true})
	case explanation.To != "" && !strings.EqualFold(explanation.To, expectedAddress):
		return PaymentOrder{}, apperrors.NewWithData(consts.StatusConflict,
			"这笔转账的收款地址与订单不一致，无法自动入账，请联系客服核对",
			map[string]any{"reason": "RECIPIENT_MISMATCH", "receivedTo": explanation.To, "amount": explanation.Amount, "manualReviewRequired": true})
	case explanation.Amount != "" && !sameTrc20Amount(explanation.Amount, order.ChainAmount):
		return PaymentOrder{}, apperrors.NewWithData(consts.StatusConflict,
			"到账金额与订单要求不一致，无法自动入账，请联系客服核对",
			map[string]any{"reason": "AMOUNT_MISMATCH", "receivedAmount": explanation.Amount, "expectedAmount": order.ChainAmount, "manualReviewRequired": true})
	case !explanation.Success:
		return PaymentOrder{}, apperrors.NewWithData(consts.StatusConflict,
			"该交易在链上执行失败，资金未转出，请重新发起转账",
			map[string]any{"reason": "CHAIN_TX_FAILED", "manualReviewRequired": false})
	}
	return PaymentOrder{}, verdict
}

// explainTrc20TxID 读链上回执，用于在「查不到」时给出可操作的结论。
func (s *Service) explainTrc20TxID(ctx context.Context, txid string) (Trc20TxExplanation, error) {
	apiKey := ""
	stored, err := s.repo.Trc20Config(ctx)
	if err == nil && strings.TrimSpace(stored.APIKeyCipher) != "" && s.cipher != nil {
		if decrypted, decryptErr := s.cipher.Decrypt(stored.APIKeyCipher); decryptErr == nil {
			apiKey = decrypted
		}
	}
	return ExplainTrc20Transaction(ctx, txid, apiKey)
}

// settleTrc20ByTxID 按订单快照收款地址直接核对该 txid 的已确认入账并结算。
// 只在用户显式提交 txid 时使用：它绕开了金额自动匹配，但结算事务仍会复核
// 收款地址、精确金额、时间窗与确认数，不会把钱记到别的订单上。
func (s *Service) settleTrc20ByTxID(ctx context.Context, order PaymentOrder, txid string) (PaymentOrder, error) {
	stored, err := s.repo.Trc20Config(ctx)
	if err != nil {
		return PaymentOrder{}, err
	}
	address := strings.TrimSpace(order.ChainAddress)
	if address == "" {
		address = strings.TrimSpace(stored.Address)
	}
	if address == "" {
		return PaymentOrder{}, apperrors.New(consts.StatusServiceUnavailable, "TRC20 收款地址未配置")
	}
	apiKey := ""
	if strings.TrimSpace(stored.APIKeyCipher) != "" {
		if s.cipher == nil {
			return PaymentOrder{}, ErrPaymentSecretMissing
		}
		if apiKey, err = s.cipher.Decrypt(stored.APIKeyCipher); err != nil {
			return PaymentOrder{}, err
		}
	}
	// 回看窗口覆盖「订单有效期 + 匹配窗口」即可：更早的转账本来也不会被结算事务接受。
	since := time.Now().Add(-(trc20ExpireMinutes + trc20MatchWindowHours*60) * time.Minute)
	transfer, found, err := FindConfirmedTrc20Transfer(ctx, address, stored.ContractAddress, apiKey, txid, since)
	if err != nil {
		return PaymentOrder{}, err
	}
	if !found {
		return order, nil
	}
	if err := s.repo.RecordTrc20Transfer(ctx, transfer); err != nil {
		return PaymentOrder{}, err
	}
	if err := s.repo.SettleTrc20Order(ctx, order.OrderNo, transfer); err != nil {
		return PaymentOrder{}, err
	}
	s.notifyPaymentPaid(ctx, order.OrderNo)
	// admin=true：订单归属已在调用方用 principal 校验过，这里只按订单号回读。
	return s.repo.OrderByNo(ctx, order.OrderNo, 0, true)
}
