package payment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// 订单状态补记：把「已经人工补过余额」的订单标记为已支付。
//
// 为什么必须单独一条路径：正常结算事务 settleChannelPaidOrder 会给用户加余额。
// 一旦余额已经由管理员手工补过，再走结算就是**重复入账**——这是财务事故。
// 这里的语义严格相反：只写订单状态与审计字段，余额、点数、流水一律不碰。
//
// 三条硬约束：
//  1. 必须在同一个事务里先复核「余额确实已经补过」，否则拒绝；
//  2. 只允许 EXPIRED/CANCELLED/CLOSED/FAILED → PAID，已 PAID 直接幂等返回；
//  3. 必须写入 chain_txid（或人工凭据标记），让这笔订单在账目上可追溯。
const (
	// 人工补记时使用的凭据前缀，与真实链上 txid（64 位十六进制）区分开。
	trc20ManualReceiptPrefix = "MANUAL-"
	// adminCreditBizTypesSQL 可视为「管理员已补款」的流水类型：
	// ADMIN_USER_ASSET_ADJUST 是旧版编辑用户写入的带符号差额，
	// ADMIN_BALANCE_ADJUST 是「余额调整」按增量写入的流水，两者都来自管理员操作。
	adminCreditBizTypesSQL = `'ADMIN_USER_ASSET_ADJUST','ADMIN_BALANCE_ADJUST'`
)

// ManualReceiptMarker 生成人工补记的凭据标记。
func ManualReceiptMarker(reference string) string {
	trimmed := strings.TrimSpace(reference)
	if trimmed == "" {
		trimmed = "UNSPECIFIED"
	}
	if len(trimmed) > 100 {
		trimmed = trimmed[:100]
	}
	return trc20ManualReceiptPrefix + trimmed
}

// WalletCreditedSince 判断某用户在指定时间点之后是否收到过足够金额的人工补款。
// 这是「余额已经补过」的机器可验证证据，避免仅凭操作人声明就改状态。
func (r *Repository) WalletCreditedSince(ctx context.Context, userID int64, since time.Time, minAmount string) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
		select count(*) from bp_wallet_transaction
		where user_id=? and biz_type in (`+adminCreditBizTypesSQL+`)
		  and amount>=cast(? as decimal(18,4))
		  and created_at>=?
	`, userID, strings.TrimSpace(minAmount), since).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// MarkOrderPaidManually 只把订单状态改成 PAID 并记录人工凭据，**不加余额、不记流水**。
//
// 调用方必须先自行确认余额已经补过；这里再用 WalletCreditedSince 复核一次，
// 拿不到证据就拒绝，防止「以为补过了其实没补」和「补过了又改状态」两种误操作。
func (r *Repository) MarkOrderPaidManually(ctx context.Context, orderNo, reference, operator string) (PaymentOrder, error) {
	orderNo = strings.TrimSpace(orderNo)
	if orderNo == "" {
		return PaymentOrder{}, errors.New("order number is required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return PaymentOrder{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var (
		orderID  int64
		userID   int64
		status   string
		payAmt   string
		channel  string
		existing sql.NullString
	)
	err = tx.QueryRowContext(ctx, `
		select id,user_id,status,cast(pay_amount as char),pay_channel,chain_txid
		from bp_payment_order where order_no=? for update
	`, orderNo).Scan(&orderID, &userID, &status, &payAmt, &channel, &existing)
	if errors.Is(err, sql.ErrNoRows) {
		return PaymentOrder{}, fmt.Errorf("订单不存在: %s", orderNo)
	}
	if err != nil {
		return PaymentOrder{}, err
	}
	// 已 PAID 直接幂等返回，绝不重复写。
	if status == "PAID" {
		if err := tx.Commit(); err != nil {
			return PaymentOrder{}, err
		}
		return r.OrderByNo(ctx, orderNo, 0, true)
	}
	// 只有「付了钱但本地判死」的状态才允许补记；CREATED/PAYING 说明还在等支付，
	// 直接改 PAID 会掩盖真实未支付。
	switch status {
	case "EXPIRED", "CANCELLED", "CLOSED", "FAILED":
	default:
		return PaymentOrder{}, fmt.Errorf("订单状态 %s 不允许人工补记（仅限 EXPIRED/CANCELLED/CLOSED/FAILED）", status)
	}
	// 复核余额确实已经补过：以订单创建时间为起点找管理员调整流水。
	var createdAt time.Time
	if err := tx.QueryRowContext(ctx, `select created_at from bp_payment_order where id=?`, orderID).Scan(&createdAt); err != nil {
		return PaymentOrder{}, err
	}
	var credited int
	if err := tx.QueryRowContext(ctx, `
		select count(*) from bp_wallet_transaction
		where user_id=? and biz_type in (`+adminCreditBizTypesSQL+`)
		  and amount>=cast(? as decimal(18,4))
		  and created_at>=?
	`, userID, payAmt, createdAt).Scan(&credited); err != nil {
		return PaymentOrder{}, err
	}
	if credited == 0 {
		return PaymentOrder{}, fmt.Errorf(
			"用户 %d 在订单创建后没有等额（>=%s）的管理员余额调整记录，拒绝改状态以免掩盖未补款", userID, payAmt)
	}
	marker := ManualReceiptMarker(reference)
	// 订单表没有独立备注列，审计说明并入 snapshot（JSON），不覆盖原有内容。
	auditNote := fmt.Sprintf("人工补记：余额已由管理员补入 %s，凭据=%s，操作人=%s",
		payAmt, marker, strings.TrimSpace(operator))
	// 只更新状态与审计字段：余额、点数、库存全部不动。
	if _, err := tx.ExecContext(ctx, `
		update bp_payment_order
		set status='PAID', paid_at=now(), chain_txid=?, trade_type='MANUAL', channel_trade_no=?,
		    snapshot=json_set(coalesce(snapshot,json_object()), '$.manualPaidNote', ?)
		where id=? and status=?
	`, marker, marker, auditNote, orderID, status); err != nil {
		return PaymentOrder{}, err
	}
	if err := tx.Commit(); err != nil {
		return PaymentOrder{}, err
	}
	return r.OrderByNo(ctx, orderNo, 0, true)
}
