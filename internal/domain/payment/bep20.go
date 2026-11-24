package payment

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"bugpk-api/internal/pkg/decimalvalue"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/httpclient"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// BEP20（BSC）USDT 链上充值（海外站）。
// 与 TRC20 同一套「精确到分位金额 + 固定收款地址」认单模型；区别只在链：
// EVM 用 eth_getLogs 按区块增量扫描，不依赖第三方索引服务。
const (
	ChannelBep20 = "BEP20"

	bep20NetworkBSC           = "BSC"
	bep20USDTContract         = "0x55d398326f99059ff775485246999027b3197955"
	bep20DefaultRPCURL        = "https://bsc-rpc.publicnode.com"
	bep20TokenDecimals        = 18
	bep20DefaultConfirmations = 15
	bep20MaxConfirmations     = 100
	bep20MaxAmount            = "1000000"

	bep20ExpireMinutes       = 60
	bep20SubmitCooldown      = 20
	bep20InitialBackfill     = int64(5000) // 首次启用时回看的区块数（约 1 小时）；更早的充值下单前不存在
	bep20SnapshotWindowBlock = int64(9000) // 历史收款地址每轮补扫的最近区块数
)

var (
	bep20AddressPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	bep20TxIDPattern    = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
)

// Bep20Config 管理端/公开只读的 BEP20 通道配置。
type Bep20Config struct {
	Enabled         bool   `json:"enabled"`
	Network         string `json:"network"`
	Address         string `json:"address"`
	ContractAddress string `json:"contractAddress"`
	RPCURL          string `json:"rpcUrl"`
	MinAmount       string `json:"minAmount"`
	MaxAmount       string `json:"maxAmount"`
	Confirmations   int    `json:"confirmations"`
}

type SaveBep20ConfigRequest struct {
	Enabled       bool   `json:"enabled"`
	Address       string `json:"address"`
	RPCURL        string `json:"rpcUrl"`
	MinAmount     any    `json:"minAmount"`
	MaxAmount     any    `json:"maxAmount"`
	Confirmations int    `json:"confirmations"`
}

type storedBep20Config struct {
	Bep20Config
	CursorBlock   int64
	RetryCursorID int64
}

func defaultBep20Config() Bep20Config {
	return Bep20Config{
		Network:         bep20NetworkBSC,
		ContractAddress: bep20USDTContract,
		RPCURL:          bep20DefaultRPCURL,
		MinAmount:       "5.000000",
		MaxAmount:       "10000.000000",
		Confirmations:   bep20DefaultConfirmations,
	}
}

// Bep20Config 管理端读取配置。
func (s *Service) Bep20Config(ctx context.Context, principal Principal) (Bep20Config, error) {
	if err := s.require(ctx, principal, "payment:config:view"); err != nil {
		return Bep20Config{}, err
	}
	stored, err := s.repo.Bep20Config(ctx)
	if err != nil {
		return Bep20Config{}, err
	}
	return bep20PublicConfig(stored), nil
}

func bep20PublicConfig(stored storedBep20Config) Bep20Config {
	cfg := stored.Bep20Config
	if cfg.Network == "" {
		cfg.Network = bep20NetworkBSC
	}
	if cfg.ContractAddress == "" {
		cfg.ContractAddress = bep20USDTContract
	}
	if strings.TrimSpace(cfg.RPCURL) == "" {
		cfg.RPCURL = bep20DefaultRPCURL
	}
	if cfg.MinAmount == "" {
		cfg.MinAmount = defaultBep20Config().MinAmount
	}
	if cfg.MaxAmount == "" {
		cfg.MaxAmount = defaultBep20Config().MaxAmount
	}
	if cfg.Confirmations <= 0 {
		cfg.Confirmations = bep20DefaultConfirmations
	}
	return cfg
}

// SaveBep20Config 管理端保存配置。
func (s *Service) SaveBep20Config(ctx context.Context, principal Principal, req SaveBep20ConfigRequest) error {
	if err := s.require(ctx, principal, "payment:config:update"); err != nil {
		return err
	}
	cfg, err := normalizeBep20ConfigRequest(req)
	if err != nil {
		return err
	}
	if cfg.Enabled {
		currencyCfg, err := s.repo.CurrencyConfig(ctx)
		if err != nil {
			return err
		}
		if !strings.EqualFold(currencyCfg.Currency, CurrencyUSD) {
			return apperrors.New(consts.StatusBadRequest, "请先将站点结算货币设置为 USD 再开启 BEP20")
		}
		// 保存即验证：节点不可达/不是 BSC 主网/不支持 eth_getLogs 时直接拒绝，
		// 避免开启后到账永远不认领。
		client := newBep20RPCClient(cfg.RPCURL)
		if err := client.Probe(ctx, cfg.ContractAddress, cfg.Address); err != nil {
			return apperrors.New(consts.StatusBadRequest, "BSC 节点校验失败（需支持 eth_getLogs 且为主网），请检查 RPC 地址")
		}
	}
	return s.repo.SaveBep20Config(ctx, cfg)
}

func normalizeBep20ConfigRequest(req SaveBep20ConfigRequest) (Bep20Config, error) {
	cfg := defaultBep20Config()
	cfg.Enabled = req.Enabled
	cfg.Address = strings.ToLower(strings.TrimSpace(req.Address))
	if cfg.Address != "" && !bep20AddressPattern.MatchString(cfg.Address) {
		return Bep20Config{}, apperrors.New(consts.StatusBadRequest, "BEP20 收款地址格式不正确")
	}
	if cfg.Enabled && cfg.Address == "" {
		return Bep20Config{}, apperrors.New(consts.StatusBadRequest, "开启 BEP20 充值前请先填写收款地址")
	}
	rpcURL, err := normalizeBep20RPCURL(req.RPCURL)
	if err != nil {
		return Bep20Config{}, err
	}
	cfg.RPCURL = rpcURL
	minAmount, err := decimalvalue.ParseNonNegative(strings.TrimSpace(toStringValue(req.MinAmount)))
	if err != nil || minAmount.Sign() <= 0 {
		return Bep20Config{}, apperrors.New(consts.StatusBadRequest, "最小充值金额不合法")
	}
	maxAmount, err := decimalvalue.ParseNonNegative(strings.TrimSpace(toStringValue(req.MaxAmount)))
	if err != nil || maxAmount.Sign() <= 0 {
		return Bep20Config{}, apperrors.New(consts.StatusBadRequest, "单笔上限不合法")
	}
	if maxAmount.Cmp(minAmount) < 0 {
		return Bep20Config{}, apperrors.New(consts.StatusBadRequest, "单笔上限不能小于最小充值额")
	}
	if limit, _ := decimalvalue.ParseNonNegative(bep20MaxAmount); maxAmount.Cmp(limit) > 0 {
		return Bep20Config{}, apperrors.New(consts.StatusBadRequest, "单笔上限超过允许范围")
	}
	cfg.MinAmount = minAmount.FloatString(6)
	cfg.MaxAmount = maxAmount.FloatString(6)
	cfg.Confirmations = req.Confirmations
	if cfg.Confirmations <= 0 {
		cfg.Confirmations = bep20DefaultConfirmations
	}
	if cfg.Confirmations > bep20MaxConfirmations {
		return Bep20Config{}, apperrors.New(consts.StatusBadRequest, "BEP20 确认数超出允许范围")
	}
	return cfg, nil
}

func normalizeBep20RPCURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return bep20DefaultRPCURL, nil
	}
	if err := httpclient.ValidateURL(trimmed); err != nil {
		return "", apperrors.New(consts.StatusBadRequest, "BSC RPC 地址不合法")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") {
		return "", apperrors.New(consts.StatusBadRequest, "BSC RPC 地址必须是 https")
	}
	return strings.TrimRight(trimmed, "/"), nil
}

// bep20Status 供充值能力接口使用：未启用或未配置地址时视为不可用。
func (s *Service) bep20Status(ctx context.Context) (Bep20Config, bool) {
	stored, err := s.repo.Bep20Config(ctx)
	if err != nil {
		return Bep20Config{}, false
	}
	cfg := bep20PublicConfig(stored)
	ok := cfg.Enabled && cfg.Address != "" &&
		cfg.Confirmations >= 1 && cfg.Confirmations <= bep20MaxConfirmations
	return cfg, ok
}

func normalizeBep20TxID(raw string) string {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "0x") {
		trimmed = "0x" + trimmed
	}
	return trimmed
}

// SubmitChainTxID 是用户提交 txid 的统一入口：按订单实际通道分发到 TRC20/BEP20。
func (s *Service) SubmitChainTxID(ctx context.Context, principal Principal, orderNo, txid string) (PaymentOrder, error) {
	order, err := s.repo.OrderByNo(ctx, strings.TrimSpace(orderNo), principal.ID, false)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	switch {
	case strings.EqualFold(order.PayChannel, ChannelTrc20):
		return s.SubmitTrc20TxID(ctx, principal, order.OrderNo, txid)
	case strings.EqualFold(order.PayChannel, ChannelBep20):
		return s.SubmitBep20TxID(ctx, principal, order.OrderNo, txid)
	default:
		return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "该订单不是链上充值订单")
	}
}

// SubmitBep20TxID 用户提交 BSC 交易哈希：触发一次对账，只有链上确认的转账才入账。
func (s *Service) SubmitBep20TxID(ctx context.Context, principal Principal, orderNo, rawTxID string) (PaymentOrder, error) {
	order, err := s.repo.OrderByNo(ctx, strings.TrimSpace(orderNo), principal.ID, false)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	if !strings.EqualFold(order.PayChannel, ChannelBep20) {
		return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "该订单不是 BEP20 充值订单")
	}
	txid := normalizeBep20TxID(rawTxID)
	if !bep20TxIDPattern.MatchString(txid) {
		return PaymentOrder{}, apperrors.New(consts.StatusBadRequest, "BEP20 交易号格式不正确")
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
	if !allowBep20Submit(order.OrderNo) {
		return PaymentOrder{}, apperrors.New(consts.StatusConflict, "提交过于频繁，请稍后再试")
	}
	if err := NewBep20Reconciler(s).ReconcileOnce(ctx); err != nil {
		return PaymentOrder{}, apperrors.New(consts.StatusServiceUnavailable, "链上查询暂时不可用，请稍后重试")
	}
	fresh, err := s.repo.OrderByNo(ctx, order.OrderNo, principal.ID, false)
	if err != nil {
		return PaymentOrder{}, mapPackageErr(err)
	}
	if fresh.Status == "PAID" {
		return fresh, nil
	}
	settled, inspection, required, err := s.applyBep20TxID(ctx, fresh, txid)
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
	return PaymentOrder{}, bep20SubmitVerdict(fresh, inspection, required)
}

// applyBep20TxID 按 txid 读取链上回执，找到订单收款地址上的精确金额入账并结算。
func (s *Service) applyBep20TxID(ctx context.Context, order PaymentOrder, txid string) (PaymentOrder, Bep20TxInspection, int, error) {
	stored, err := s.repo.Bep20Config(ctx)
	if err != nil {
		return PaymentOrder{}, Bep20TxInspection{}, 0, err
	}
	cfg := bep20PublicConfig(stored)
	address := strings.ToLower(strings.TrimSpace(order.ChainAddress))
	if address == "" {
		address = strings.ToLower(strings.TrimSpace(cfg.Address))
	}
	if address == "" {
		return PaymentOrder{}, Bep20TxInspection{}, 0, apperrors.New(consts.StatusServiceUnavailable, "BEP20 收款地址未配置")
	}
	client := newBep20RPCClient(cfg.RPCURL)
	inspection, err := client.InspectTransaction(ctx, txid, cfg.ContractAddress)
	if err != nil {
		return PaymentOrder{}, Bep20TxInspection{}, 0, err
	}
	if !inspection.Found || !inspection.Success {
		return order, inspection, cfg.Confirmations, nil
	}
	if inspection.Confirmations < cfg.Confirmations {
		return order, inspection, cfg.Confirmations, nil
	}
	var matched *Bep20Transfer
	for index := range inspection.Transfers {
		transfer := inspection.Transfers[index]
		if strings.EqualFold(transfer.To, address) && sameTrc20Amount(transfer.Amount, order.ChainAmount) {
			matched = &transfer
			break
		}
	}
	if matched == nil {
		return order, inspection, cfg.Confirmations, nil
	}
	matched.Confirmations = inspection.Confirmations
	if err := s.repo.RecordBep20Transfer(ctx, *matched); err != nil {
		return PaymentOrder{}, inspection, cfg.Confirmations, err
	}
	if err := s.repo.SettleBep20Order(ctx, order.OrderNo, *matched); err != nil {
		return PaymentOrder{}, inspection, cfg.Confirmations, err
	}
	s.notifyPaymentPaid(ctx, order.OrderNo)
	fresh, err := s.repo.OrderByNo(ctx, order.OrderNo, 0, true)
	return fresh, inspection, cfg.Confirmations, err
}

// bep20SubmitVerdict 把「查不到/核对不上」变成可操作的结论。
func bep20SubmitVerdict(order PaymentOrder, inspection Bep20TxInspection, required int) error {
	if !inspection.Found {
		return apperrors.NewWithData(consts.StatusConflict,
			"该交易号在 BSC 链上不存在：如果是从交易所站内转账或走的是其他网络，链上永远查不到，请联系客服人工入账",
			map[string]any{"reason": "CHAIN_TX_NOT_FOUND", "manualReviewRequired": true})
	}
	if !inspection.Success {
		return apperrors.NewWithData(consts.StatusConflict,
			"该交易在链上执行失败，资金未转出，请重新发起转账",
			map[string]any{"reason": "CHAIN_TX_FAILED", "manualReviewRequired": false})
	}
	expectedAddress := strings.ToLower(strings.TrimSpace(order.ChainAddress))
	var toExpected *Bep20Transfer
	for index := range inspection.Transfers {
		if expectedAddress != "" && strings.EqualFold(inspection.Transfers[index].To, expectedAddress) {
			toExpected = &inspection.Transfers[index]
			break
		}
	}
	if toExpected != nil {
		if sameTrc20Amount(toExpected.Amount, order.ChainAmount) {
			return apperrors.NewWithData(consts.StatusConflict,
				"链上确认中，暂未达到入账所需确认数，请稍后重试",
				map[string]any{"reason": "CHAIN_CONFIRMING", "confirmations": inspection.Confirmations, "requiredConfirmations": required})
		}
		return apperrors.NewWithData(consts.StatusConflict,
			"到账金额与订单要求不一致，无法自动入账，请联系客服核对",
			map[string]any{"reason": "AMOUNT_MISMATCH", "receivedAmount": toExpected.Amount, "expectedAmount": order.ChainAmount, "manualReviewRequired": true})
	}
	if len(inspection.Transfers) > 0 {
		return apperrors.NewWithData(consts.StatusConflict,
			"这笔转账的收款地址与订单不一致，无法自动入账，请联系客服核对",
			map[string]any{"reason": "RECIPIENT_MISMATCH", "receivedTo": inspection.Transfers[0].To, "amount": inspection.Transfers[0].Amount, "manualReviewRequired": true})
	}
	return apperrors.NewWithData(consts.StatusConflict,
		"该交易不是 USDT-BEP20 转账，无法自动入账，请联系客服核对",
		map[string]any{"reason": "NOT_USDT_TRANSFER", "manualReviewRequired": true})
}
