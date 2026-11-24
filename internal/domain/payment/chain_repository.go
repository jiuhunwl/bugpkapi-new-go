package payment

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// 链上充值（TRC20 / BEP20）共用的订单侧校验。
// 两条链的到账流水各自有表，但「订单金额、收款地址、时间窗、确认数」
// 这一层认单规则必须完全一致，避免同一种晚到账在一条链上能补、另一条不能。

type chainTransfer struct {
	TxID           string
	LogIndex       int
	From           string
	To             string
	Amount         string
	BlockTimestamp int64
	Confirmations  int
}

type chainSettlement struct {
	Kind          string
	TxID          string
	Confirmations int
	Transfer      chainTransfer
}

func validateChainOrderTx(ctx context.Context, tx *sql.Tx, orderID int64, chain *chainSettlement) error {
	var address, amount string
	var inWindow bool
	err := tx.QueryRowContext(ctx, `select coalesce(chain_address,''),coalesce(cast(chain_amount as char),''),
 (created_at<=from_unixtime(floor(?/1000)) and created_at>date_sub(from_unixtime(floor(?/1000)), interval 24 hour))
 from bp_payment_order where id=?`, chain.Transfer.BlockTimestamp, chain.Transfer.BlockTimestamp, orderID).Scan(&address, &amount, &inWindow)
	if err != nil {
		return err
	}
	if !inWindow || !strings.EqualFold(address, chain.Transfer.To) || !sameTrc20Amount(amount, chain.Transfer.Amount) {
		return fmt.Errorf("%s order amount, recipient or matching window mismatch", chain.Kind)
	}
	return nil
}

// matchChainOrderCandidates 只返回结算事务真正会接受的候选：未绑 txid、状态可结算、
// 金额与收款地址完全一致、创建时间落在转账前 windowHours 小时内。
func (r *Repository) matchChainOrderCandidates(ctx context.Context, channel, to, amount string, blockTimestampMS int64, windowHours int) ([]trc20MatchCandidate, error) {
	if windowHours <= 0 {
		windowHours = trc20MatchWindowHours
	}
	rows, err := r.db.QueryContext(ctx, `select order_no,user_id
 from bp_payment_order
 where pay_channel=?
   and status in ('CREATED','PAYING','EXPIRED')
   and (chain_txid is null or chain_txid='')
   and binary chain_address=binary ?
   and chain_amount=cast(? as decimal(18,6))
   and created_at<=from_unixtime(floor(?/1000))
   and created_at>date_sub(from_unixtime(floor(?/1000)), interval ? hour)
 order by created_at,id
 limit 2`, channel, to, amount, blockTimestampMS, windowHours)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var candidates []trc20MatchCandidate
	for rows.Next() {
		var item trc20MatchCandidate
		if err := rows.Scan(&item.OrderNo, &item.UserID); err != nil {
			return nil, err
		}
		candidates = append(candidates, item)
	}
	return candidates, rows.Err()
}

// resolveChainOrderCandidate 把候选收敛成唯一订单：同一用户重复下单时取最旧一笔，
// 跨用户同额才判定歧义（交给人工）。
func resolveChainOrderCandidate(candidates []trc20MatchCandidate) (string, error) {
	switch len(candidates) {
	case 0:
		return "", nil
	case 1:
		return candidates[0].OrderNo, nil
	}
	if candidates[0].UserID == candidates[1].UserID {
		return candidates[0].OrderNo, nil
	}
	return "", ErrTrc20AmbiguousOrder
}

// snapshotChainAddresses 返回最近仍有订单快照、但已不是当前配置值的收款地址。
// 改地址之后，旧地址上迟到的转账不会进入主扫描；调用方按链类型过滤地址格式。
func (r *Repository) snapshotChainAddresses(ctx context.Context, channel, currentAddress string, windowHours, limit int) ([]string, error) {
	if windowHours <= 0 {
		windowHours = trc20SnapshotAddressWindowHours
	}
	if limit <= 0 || limit > 20 {
		limit = trc20SnapshotAddressLimit
	}
	rows, err := r.db.QueryContext(ctx, `select distinct chain_address
 from bp_payment_order
 where pay_channel=?
   and chain_address is not null and chain_address<>''
   and binary chain_address<>binary ?
   and created_at>date_sub(now(), interval ? hour)
 order by 1
 limit ?`, channel, strings.TrimSpace(currentAddress), windowHours, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var addresses []string
	for rows.Next() {
		var address string
		if err := rows.Scan(&address); err != nil {
			return nil, err
		}
		address = strings.TrimSpace(address)
		if address != "" {
			addresses = append(addresses, address)
		}
	}
	return addresses, rows.Err()
}
