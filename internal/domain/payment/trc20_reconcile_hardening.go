package payment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// TRC20 对账加固。
//
// 线上症状：链上已经到账，订单却停在 EXPIRED，余额永远不进。
// 根因不是「过期」本身（过期订单仍然允许迟到结算），而是这笔到账**从来没有被
// 认领**，而系统对这种「钱到了、单没认」的状态既不告警也不给出处置入口。
//
// 这里补齐三件事：
//  1. 匹配只统计真正可结算的候选，历史同额度的 PAID 行不再把唯一候选污染成
//     「有歧义」（旧实现在这种行存在时会永久卡住，每一轮都失败）。
//  2. 同一用户的多笔同额候选按最旧优先收敛，跨用户才判定为歧义。
//  3. 未匹配到账可查询、可告警；配置里已经删掉的历史收款地址也回头扫。
const (
	// trc20MatchWindowHours 是「订单创建时间必须早于转账时间、且在多少小时以内」
	// 的匹配窗口，与 SQL 中的 date_sub(..., interval N hour) 对应。
	trc20MatchWindowHours = 24
	// trc20SnapshotAddressWindowHours 决定还要回头扫多少个「已不在配置里、但最近
	// 仍有订单快照」的历史收款地址。收款地址一改，旧地址上的钱就再也查不到。
	trc20SnapshotAddressWindowHours = 72
	// trc20SnapshotAddressLimit 是一次对账里最多回头扫几个历史地址。
	trc20SnapshotAddressLimit = 5
	// trc20UnmatchedWarnMinutes：到账超过这个时间还没有订单认领，就必须有人看。
	trc20UnmatchedWarnMinutes = 5
)

// Trc20UnmatchedReceipt 是一笔链上已确认、但没有任何订单认领的到账。
// 出现在这里就意味着钱到了、账没记，必须人工处置。
type Trc20UnmatchedReceipt struct {
	TxID           string `json:"txid"`
	From           string `json:"from"`
	To             string `json:"to"`
	Amount         string `json:"amount"`
	BlockTimestamp int64  `json:"blockTimestamp"`
	CreatedAt      string `json:"createdAt"`
}

type trc20MatchCandidate struct {
	OrderNo string
	UserID  int64
}

// UnmatchedTrc20Transfers 返回已观察但未绑定订单的到账（按观察时间倒序）。
// minAgeMinutes 用于排除刚刚落库、本轮对账还没来得及处理的行。
func (r *Repository) UnmatchedTrc20Transfers(ctx context.Context, minAgeMinutes, limit int) ([]Trc20UnmatchedReceipt, error) {
	if minAgeMinutes < 0 {
		minAgeMinutes = 0
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx, `select txid,from_address,to_address,cast(amount as char),block_timestamp,
 date_format(created_at,'%Y-%m-%d %H:%i:%s')
 from bp_payment_trc20_transfer
 where matched_order_no is null
   and created_at<=date_sub(now(), interval ? minute)
 order by id
 limit ?`, minAgeMinutes, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []Trc20UnmatchedReceipt
	for rows.Next() {
		var item Trc20UnmatchedReceipt
		if err := rows.Scan(&item.TxID, &item.From, &item.To, &item.Amount, &item.BlockTimestamp, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Trc20Receipt 读取某笔链上转账的落库状态：是否存在、是否已被订单认领。
// 用户提交 txid 时用它区分「链上还没有」和「链上有、但没对上」。
func (r *Repository) Trc20Receipt(ctx context.Context, txid string) (bool, string, error) {
	var matched sql.NullString
	err := r.db.QueryRowContext(ctx,
		`select matched_order_no from bp_payment_trc20_transfer where txid=?`, strings.TrimSpace(txid)).Scan(&matched)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return true, strings.TrimSpace(matched.String), nil
}

// Trc20SnapshotAddresses 返回「最近仍有订单快照、但已不是当前配置值」的收款地址。
//
// 订单在创建时把收款地址快照进 chain_address，而 worker 只向提供方查询当前配置
// 地址。后台一旦改过地址，改之前下的单就再也不会被扫到——钱确实进了旧地址，
// 系统却永远看不见。这里把这些地址捞出来，由 worker 回头补扫。
func (r *Repository) Trc20SnapshotAddresses(ctx context.Context, currentAddress string, windowHours, limit int) ([]string, error) {
	if windowHours <= 0 {
		windowHours = trc20SnapshotAddressWindowHours
	}
	if limit <= 0 || limit > 20 {
		limit = trc20SnapshotAddressLimit
	}
	rows, err := r.db.QueryContext(ctx, `select distinct chain_address
 from bp_payment_order
 where pay_channel='TRC20'
   and chain_address is not null and chain_address<>''
   and binary chain_address<>binary ?
   and created_at>date_sub(now(), interval ? hour)
 order by 1
 limit ?`, strings.TrimSpace(currentAddress), windowHours, limit)
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
		if trc20AddressPattern.MatchString(address) {
			addresses = append(addresses, address)
		}
	}
	return addresses, rows.Err()
}

// matchTrc20Candidates 只返回结算事务真正会接受的候选：未绑 txid、状态可结算、
// 金额与收款地址完全一致、创建时间落在转账前 24 小时内。
//
// 旧实现把「金额相同」的全部历史订单都算进候选（含早已 PAID 的行），一旦存在
// 历史同额订单，唯一可结算的那笔也会被判成歧义，于是每一轮对账都失败、订单
// 永远停在 EXPIRED。
func (r *Repository) matchTrc20Candidates(ctx context.Context, transfer Trc20Transfer) ([]trc20MatchCandidate, error) {
	rows, err := r.db.QueryContext(ctx, `select order_no,user_id
 from bp_payment_order
 where pay_channel='TRC20'
   and status in ('CREATED','PAYING','EXPIRED')
   and (chain_txid is null or chain_txid='')
   and binary chain_address=binary ?
   and chain_amount=cast(? as decimal(18,6))
   and created_at<=from_unixtime(floor(?/1000))
   and created_at>date_sub(from_unixtime(floor(?/1000)), interval ? hour)
 order by created_at,id
 limit 2`, transfer.To, transfer.Amount, transfer.BlockTimestamp, transfer.BlockTimestamp, trc20MatchWindowHours)
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

func describeTrc20Unmatched(items []Trc20UnmatchedReceipt) string {
	parts := make([]string, 0, len(items))
	for _, item := range items {
		parts = append(parts, fmt.Sprintf("%s amount=%s to=%s at=%s", item.TxID, item.Amount, item.To, item.CreatedAt))
	}
	return strings.Join(parts, "; ")
}
