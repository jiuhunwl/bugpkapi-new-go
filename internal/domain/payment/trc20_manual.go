package payment

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"bugpk-api/internal/pkg/decimalvalue"
)

// 人工核对 / 补单通道。
//
// 自动对账只认「金额 + 收款地址 + 时间窗」。一旦某笔钱因为地址变更、历史同额度
// 订单、上游索引延迟等原因没被认领，订单就会停在 EXPIRED，而余额永远不动。
// 这里提供现场诊断与按 txid 强制补单，两者都不绕过结算事务的复核。

// Trc20OrderSnapshot 是一笔 TRC20 充值订单在诊断视图里的样子。
type Trc20OrderSnapshot struct {
	OrderNo      string  `json:"orderNo"`
	UserID       int64   `json:"userId"`
	Status       string  `json:"status"`
	PayAmount    string  `json:"payAmount"`
	OriginAmount string  `json:"originAmount"`
	ChainAddress string  `json:"chainAddress"`
	ChainAmount  string  `json:"chainAmount"`
	ChainTxID    string  `json:"chainTxid"`
	CreatedAt    string  `json:"createdAt"`
	ExpireAt     string  `json:"expireAt"`
	PaidAt       string  `json:"paidAt"`
	MinutesSince float64 `json:"minutesSinceCreated"`
}

// Trc20Diagnosis 汇总一次现场排查需要的全部事实。
type Trc20Diagnosis struct {
	Enabled          bool                    `json:"enabled"`
	Address          string                  `json:"address"`
	Contract         string                  `json:"contract"`
	Confirmations    int                     `json:"confirmations"`
	MinAmount        string                  `json:"minAmount"`
	MaxAmount        string                  `json:"maxAmount"`
	CursorTimestamp  int64                   `json:"cursorTimestampMs"`
	CursorAgeMinutes float64                 `json:"cursorAgeMinutes"`
	RetryCursorID    int64                   `json:"retryCursorId"`
	StuckReceipts    []Trc20UnmatchedReceipt `json:"stuckReceipts"`
	Orders           []Trc20OrderSnapshot    `json:"orders"`
}

// Trc20OrdersByAmount 返回某个精确金额下的全部订单（含已过期），用于判断
// 「这笔钱到底该记给谁」。amount 为空时按创建时间倒序取最近的订单。
func (r *Repository) Trc20OrdersByAmount(ctx context.Context, amount string, limit int) ([]Trc20OrderSnapshot, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `select order_no,user_id,status,cast(pay_amount as char),coalesce(cast(origin_amount as char),''),
 coalesce(chain_address,''),coalesce(cast(chain_amount as char),''),coalesce(chain_txid,''),
 date_format(created_at,'%Y-%m-%d %H:%i:%s'),date_format(expire_at,'%Y-%m-%d %H:%i:%s'),
 coalesce(date_format(paid_at,'%Y-%m-%d %H:%i:%s'),''),
 timestampdiff(second,created_at,now())/60
 from bp_payment_order where pay_channel='TRC20'`
	args := []any{}
	if strings.TrimSpace(amount) != "" {
		query += ` and chain_amount=cast(? as decimal(18,6))`
		args = append(args, strings.TrimSpace(amount))
	}
	query += ` order by created_at desc, id desc limit ?`
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Trc20OrderSnapshot
	for rows.Next() {
		var item Trc20OrderSnapshot
		if err := rows.Scan(&item.OrderNo, &item.UserID, &item.Status, &item.PayAmount, &item.OriginAmount,
			&item.ChainAddress, &item.ChainAmount, &item.ChainTxID, &item.CreatedAt, &item.ExpireAt,
			&item.PaidAt, &item.MinutesSince); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// DiagnoseTrc20 一次性取出排查所需的配置、游标与卡住的到账。
func (r *Repository) DiagnoseTrc20(ctx context.Context, amount string) (Trc20Diagnosis, error) {
	stored, err := r.Trc20Config(ctx)
	if err != nil {
		return Trc20Diagnosis{}, err
	}
	diag := Trc20Diagnosis{
		Enabled:         stored.Enabled,
		Address:         stored.Address,
		Contract:        stored.ContractAddress,
		Confirmations:   stored.Confirmations,
		MinAmount:       stored.MinAmount,
		MaxAmount:       stored.MaxAmount,
		CursorTimestamp: stored.CursorTimestampMS,
		RetryCursorID:   stored.RetryCursorID,
	}
	if stored.CursorTimestampMS > 0 {
		diag.CursorAgeMinutes = time.Since(time.UnixMilli(stored.CursorTimestampMS)).Minutes()
	}
	if diag.StuckReceipts, err = r.UnmatchedTrc20Transfers(ctx, 0, 50); err != nil {
		return Trc20Diagnosis{}, err
	}
	if diag.Orders, err = r.Trc20OrdersByAmount(ctx, amount, 50); err != nil {
		return Trc20Diagnosis{}, err
	}
	return diag, nil
}

// FindConfirmedTrc20Transfer 在链上按 txid 找一笔已确认的 USDT-TRC20 入账。
// since 决定回看多久（提供方是时间窗口查询，不能按 hash 直取）。
func FindConfirmedTrc20Transfer(ctx context.Context, address, contract, apiKey, txid string, since time.Time) (Trc20Transfer, bool, error) {
	if !trc20TxIDPattern.MatchString(strings.TrimSpace(txid)) {
		return Trc20Transfer{}, false, fmt.Errorf("交易号格式不正确")
	}
	client := newTrongridClient()
	lower := since.UnixMilli()
	if lower <= 0 {
		lower = time.Now().Add(-trc20ScanWindow).UnixMilli()
	}
	transfers, err := client.ListTransfers(ctx, address, contract, apiKey, lower)
	if err != nil {
		return Trc20Transfer{}, false, err
	}
	wanted := strings.ToLower(strings.TrimSpace(txid))
	for _, transfer := range transfers {
		if strings.ToLower(transfer.TxID) == wanted {
			return transfer, true, nil
		}
	}
	return Trc20Transfer{}, false, nil
}

// ForceSettleTrc20 是人工补单：按 txid 从链上取回转账，落流水并结算指定订单。
//
// 它不做「绕过校验」的事——结算事务仍然复核收款地址、精确金额、时间窗与确认数；
// 这里只是把订单和 txid 明确绑在一起，避免依赖金额自动匹配。
func (s *Service) ForceSettleTrc20(ctx context.Context, orderNo, txid, apiKey string, since time.Time) (PaymentOrder, error) {
	if s == nil || s.repo == nil {
		return PaymentOrder{}, errors.New("payment service is not initialised")
	}
	stored, err := s.repo.Trc20Config(ctx)
	if err != nil {
		return PaymentOrder{}, err
	}
	address := strings.TrimSpace(stored.Address)
	if address == "" {
		return PaymentOrder{}, errors.New("TRC20 收款地址未配置")
	}
	if strings.TrimSpace(apiKey) == "" && strings.TrimSpace(stored.APIKeyCipher) != "" {
		if s.cipher == nil {
			return PaymentOrder{}, ErrPaymentSecretMissing
		}
		if apiKey, err = s.cipher.Decrypt(stored.APIKeyCipher); err != nil {
			return PaymentOrder{}, err
		}
	}
	order, err := s.repo.OrderByNo(ctx, strings.TrimSpace(orderNo), 0, true)
	if err != nil {
		return PaymentOrder{}, err
	}
	if !strings.EqualFold(order.PayChannel, ChannelTrc20) {
		return PaymentOrder{}, errors.New("该订单不是 TRC20 充值订单")
	}
	if order.Status == "PAID" {
		if strings.EqualFold(order.ChainTxID, txid) {
			return order, nil
		}
		return PaymentOrder{}, fmt.Errorf("订单已用其他交易号入账：%s", order.ChainTxID)
	}
	// 订单快照里的收款地址优先：改过配置地址后，旧订单的钱仍在旧地址上。
	target := strings.TrimSpace(order.ChainAddress)
	if target == "" {
		target = address
	}
	transfer, found, err := FindConfirmedTrc20Transfer(ctx, target, stored.ContractAddress, apiKey, txid, since)
	if err != nil {
		return PaymentOrder{}, err
	}
	if !found {
		return PaymentOrder{}, fmt.Errorf("链上未找到该交易号在 %s 上的已确认入账", target)
	}
	if err := s.repo.RecordTrc20Transfer(ctx, transfer); err != nil {
		return PaymentOrder{}, err
	}
	if err := s.repo.SettleTrc20Order(ctx, order.OrderNo, transfer); err != nil {
		return PaymentOrder{}, err
	}
	s.notifyPaymentPaid(ctx, order.OrderNo)
	return s.repo.OrderByNo(ctx, order.OrderNo, 0, true)
}

// CreditWalletManually 是最后的兜底：链上钱确实到了、但订单已无法按规则结算
// （例如订单行被删或金额口径不一致），直接给用户补余额并留审计备注。
func (r *Repository) CreditWalletManually(ctx context.Context, userID int64, amount, remark string) error {
	parsed, err := parsePositiveMoney(amount)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		update bp_user
		set balance=balance+?
		where id=? and balance<=cast(? as decimal(18,4))-cast(? as decimal(18,4))
	`, parsed, userID, maxUserBalanceText, parsed)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrBalanceCapacityExceeded
	}
	if err := insertWalletTx(ctx, tx, userID, "ALIPAY_RECHARGE", 0, parsed, 0, remark); err != nil {
		return err
	}
	if err := r.applySponsorRoleTx(ctx, tx, userID); err != nil {
		return err
	}
	return tx.Commit()
}

var ErrInvalidMoney = errors.New("invalid money amount")

// manualMoneyPattern 只接受朴素十进制写法：人工补单是运维手输的场景，
// 科学计数法（1e5 = 100000）在这里只会制造误操作，一律拒绝。
var manualMoneyPattern = regexp.MustCompile(`^[0-9]{1,12}(?:\.[0-9]{1,4})?$`)

func parsePositiveMoney(text string) (string, error) {
	trimmed := strings.TrimSpace(text)
	if !manualMoneyPattern.MatchString(trimmed) {
		return "", ErrInvalidMoney
	}
	parsed, err := decimalvalue.ParseNonNegative(trimmed)
	if err != nil || parsed.Sign() <= 0 {
		return "", ErrInvalidMoney
	}
	return parsed.FloatString(4), nil
}
