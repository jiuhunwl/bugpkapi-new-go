package payment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// BEP20 到账流水的持久化：与 TRC20 同一套认单规则，幂等键为 (tx_hash,log_index)。

var ErrBep20AmbiguousOrder = errors.New("multiple orders match the BEP20 transfer; manual reconciliation required")

// Bep20Config 读取配置；行不存在（迁移未跑）时返回默认关闭态。
func (r *Repository) Bep20Config(ctx context.Context) (storedBep20Config, error) {
	var stored storedBep20Config
	var enabled int
	err := r.db.QueryRowContext(ctx, `
		select enabled,network,address,contract_address,rpc_url,
		       cast(min_amount as char),cast(max_amount as char),confirmations,cursor_block,retry_cursor_id
		from bp_payment_bep20_config where id=1
	`).Scan(
		&enabled, &stored.Network, &stored.Address, &stored.ContractAddress, &stored.RPCURL,
		&stored.MinAmount, &stored.MaxAmount, &stored.Confirmations, &stored.CursorBlock, &stored.RetryCursorID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedBep20Config{Bep20Config: defaultBep20Config()}, nil
	}
	if err != nil {
		return storedBep20Config{}, err
	}
	stored.Enabled = enabled == 1
	return stored, nil
}

// SaveBep20Config 保存配置；游标与重试游标保持不变。
func (r *Repository) SaveBep20Config(ctx context.Context, cfg Bep20Config) error {
	_, err := r.db.ExecContext(ctx, `
		insert into bp_payment_bep20_config(id,enabled,network,address,contract_address,rpc_url,min_amount,max_amount,confirmations)
		values(1,?,?,?,?,?,?,?,?)
		on duplicate key update
			enabled=values(enabled),network=values(network),address=values(address),
			contract_address=values(contract_address),rpc_url=values(rpc_url),
			min_amount=values(min_amount),max_amount=values(max_amount),confirmations=values(confirmations)
	`, boolInt(cfg.Enabled), cfg.Network, cfg.Address, cfg.ContractAddress, cfg.RPCURL,
		cfg.MinAmount, cfg.MaxAmount, cfg.Confirmations)
	return err
}

func (r *Repository) UpdateBep20Cursor(ctx context.Context, block int64) error {
	_, err := r.db.ExecContext(ctx, `
		update bp_payment_bep20_config set cursor_block=greatest(cursor_block,?) where id=1
	`, block)
	return err
}

func (r *Repository) UpdateBep20RetryCursor(ctx context.Context, previous, next int64) error {
	_, err := r.db.ExecContext(ctx, `update bp_payment_bep20_config set retry_cursor_id=? where id=1 and retry_cursor_id=?`, next, previous)
	return err
}

// Bep20TransferExists 判断某笔日志是否已经被订单认领。
func (r *Repository) Bep20TransferExists(ctx context.Context, transfer Bep20Transfer) (bool, error) {
	var exists int
	err := r.db.QueryRowContext(ctx, `select exists(select 1 from bp_payment_bep20_transfer
 where tx_hash=? and log_index=? and matched_order_no is not null and matched_order_no<>'')`,
		transfer.TxHash, transfer.LogIndex).Scan(&exists)
	return exists == 1, err
}

// PendingBep20Transfers 返回已落库但尚未认领的到账，用于逐笔重试。
func (r *Repository) PendingBep20Transfers(ctx context.Context, address string, afterID int64) ([]Bep20Transfer, error) {
	query := `select t.id,t.tx_hash,t.log_index,t.from_address,t.to_address,cast(t.amount as char),
 t.block_number,t.block_timestamp
 from bp_payment_bep20_transfer t where t.matched_order_no is null and t.id>?`
	args := []any{afterID}
	if strings.TrimSpace(address) != "" {
		query += ` and binary t.to_address=binary ?`
		args = append(args, strings.TrimSpace(address))
	}
	query += `
 and exists(select 1 from bp_payment_order o where o.pay_channel='BEP20' and (
  (o.status='PAID' and o.chain_txid=t.tx_hash) or
  (o.status in ('CREATED','PAYING','EXPIRED') and (o.chain_txid is null or o.chain_txid='')
   and binary o.chain_address=binary t.to_address and o.chain_amount=t.amount
   and o.created_at<=from_unixtime(floor(t.block_timestamp/1000))
   and o.created_at>date_sub(from_unixtime(floor(t.block_timestamp/1000)), interval 24 hour))))
 order by t.id limit 200`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var transfers []Bep20Transfer
	for rows.Next() {
		var item Bep20Transfer
		if err := rows.Scan(&item.ReceiptID, &item.TxHash, &item.LogIndex, &item.From, &item.To, &item.Amount,
			&item.BlockNumber, &item.BlockTimestamp); err != nil {
			return nil, err
		}
		// 流水只会在达到配置确认数后才落库；重试时按「已确认」处理即可，
		// 结算事务仍会复核收款地址、精确金额与时间窗。
		item.Confirmations = 1
		transfers = append(transfers, item)
	}
	return transfers, rows.Err()
}

// RecordBep20Transfer 先落流水再结算；唯一键 (tx_hash,log_index) 保证重复扫描幂等。
func (r *Repository) RecordBep20Transfer(ctx context.Context, transfer Bep20Transfer) error {
	_, err := r.db.ExecContext(ctx, `insert ignore into bp_payment_bep20_transfer
 (tx_hash,log_index,from_address,to_address,amount,block_number,block_timestamp,matched_order_no)
 values(?,?,?,?,?,?,?,NULL)`,
		strings.ToLower(strings.TrimSpace(transfer.TxHash)), transfer.LogIndex, strings.ToLower(transfer.From),
		strings.ToLower(transfer.To), transfer.Amount, transfer.BlockNumber, transfer.BlockTimestamp)
	return err
}

// MatchBep20Order 在到账时间附近找一笔真正可结算的订单。
func (r *Repository) MatchBep20Order(ctx context.Context, transfer Bep20Transfer) (string, error) {
	var paidOrder string
	err := r.db.QueryRowContext(ctx, `select order_no from bp_payment_order
 where pay_channel='BEP20' and status='PAID' and chain_txid=?`, transfer.TxHash).Scan(&paidOrder)
	if err == nil {
		return paidOrder, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	candidates, err := r.matchChainOrderCandidates(ctx, ChannelBep20, transfer.To, transfer.Amount, transfer.BlockTimestamp, trc20MatchWindowHours)
	if err != nil {
		return "", err
	}
	orderNo, err := resolveChainOrderCandidate(candidates)
	if errors.Is(err, ErrTrc20AmbiguousOrder) {
		return "", ErrBep20AmbiguousOrder
	}
	return orderNo, err
}

func (r *Repository) SettleBep20Order(ctx context.Context, orderNo string, transfer Bep20Transfer) error {
	return r.settleChannelPaidOrder(ctx, ChannelBep20, orderNo, PayTradeStatus{
		TradeNo: transfer.TxHash,
		Total:   transfer.Amount,
		Success: true,
	}, &chainSettlement{
		Kind:          ChannelBep20,
		TxID:          transfer.TxHash,
		Confirmations: transfer.Confirmations,
		Transfer: chainTransfer{
			TxID:           transfer.TxHash,
			LogIndex:       transfer.LogIndex,
			From:           transfer.From,
			To:             transfer.To,
			Amount:         transfer.Amount,
			BlockTimestamp: transfer.BlockTimestamp,
			Confirmations:  transfer.Confirmations,
		},
	})
}

func lockBep20TransferTx(ctx context.Context, tx *sql.Tx, orderNo string, chain *chainSettlement) error {
	var from, to, amount, matched string
	var timestamp int64
	err := tx.QueryRowContext(ctx, `select from_address,to_address,cast(amount as char),block_timestamp,coalesce(matched_order_no,'')
 from bp_payment_bep20_transfer where tx_hash=? and log_index=? for update`,
		chain.TxID, chain.Transfer.LogIndex).Scan(&from, &to, &amount, &timestamp, &matched)
	if err != nil {
		return err
	}
	transfer := chain.Transfer
	if (matched != "" && matched != orderNo) || !strings.EqualFold(from, transfer.From) || !strings.EqualFold(to, transfer.To) ||
		timestamp != transfer.BlockTimestamp || !sameTrc20Amount(amount, transfer.Amount) || transfer.Confirmations < 1 {
		return fmt.Errorf("BEP20 receipt identity or confirmed state mismatch")
	}
	return nil
}

func bindBep20TransferTx(ctx context.Context, tx *sql.Tx, orderNo string, chain *chainSettlement) error {
	_, err := tx.ExecContext(ctx, `update bp_payment_bep20_transfer set matched_order_no=?
 where tx_hash=? and log_index=? and (matched_order_no is null or matched_order_no=?)`,
		orderNo, chain.TxID, chain.Transfer.LogIndex, orderNo)
	return err
}

// UnmatchedBep20Transfers 返回已观察但未绑定订单的到账（按观察时间倒序）。
func (r *Repository) UnmatchedBep20Transfers(ctx context.Context, minAgeMinutes, limit int) ([]Trc20UnmatchedReceipt, error) {
	if minAgeMinutes < 0 {
		minAgeMinutes = 0
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := r.db.QueryContext(ctx, `select tx_hash,from_address,to_address,cast(amount as char),block_timestamp,
 date_format(created_at,'%Y-%m-%d %H:%i:%s')
 from bp_payment_bep20_transfer
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
