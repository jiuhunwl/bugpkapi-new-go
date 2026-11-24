package payment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"bugpk-api/internal/pkg/decimalvalue"
)

// Trc20Config 读取配置；行不存在（迁移未跑）时返回默认关闭态。
func (r *Repository) Trc20Config(ctx context.Context) (storedTrc20Config, error) {
	var stored storedTrc20Config
	var enabled int
	err := r.db.QueryRowContext(ctx, `
		select enabled,network,address,coalesce(api_key_cipher,''),contract_address,
		       cast(min_amount as char),cast(max_amount as char),confirmations,cursor_timestamp_ms,retry_cursor_id
		from bp_payment_trc20_config where id=1
	`).Scan(
		&enabled, &stored.Network, &stored.Address, &stored.APIKeyCipher, &stored.ContractAddress,
		&stored.MinAmount, &stored.MaxAmount, &stored.Confirmations, &stored.CursorTimestampMS, &stored.RetryCursorID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedTrc20Config{Trc20Config: defaultTrc20Config()}, nil
	}
	if err != nil {
		return storedTrc20Config{}, err
	}
	stored.Enabled = enabled == 1
	return stored, nil
}

// SaveTrc20Config 保存配置；updateAPIKey=false 时保留原 API Key。
func (r *Repository) SaveTrc20Config(ctx context.Context, cfg Trc20Config, apiKeyCipher string, updateAPIKey bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `
		insert into bp_payment_trc20_config(id,enabled,network,address,contract_address,min_amount,max_amount,confirmations)
		values(1,?,?,?,?,?,?,?)
		on duplicate key update
			enabled=values(enabled),network=values(network),address=values(address),
			contract_address=values(contract_address),min_amount=values(min_amount),
			max_amount=values(max_amount),confirmations=values(confirmations)
	`, boolInt(cfg.Enabled), cfg.Network, cfg.Address, cfg.ContractAddress, cfg.MinAmount, cfg.MaxAmount, cfg.Confirmations); err != nil {
		return err
	}
	if updateAPIKey {
		if _, err := tx.ExecContext(ctx, `update bp_payment_trc20_config set api_key_cipher=? where id=1`, nullString(apiKeyCipher)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repository) UpdateTrc20Cursor(ctx context.Context, cursorTimestampMS int64) error {
	_, err := r.db.ExecContext(ctx, `
		update bp_payment_trc20_config
		set cursor_timestamp_ms=greatest(cursor_timestamp_ms,?)
		where id=1
	`, cursorTimestampMS)
	return err
}

// Trc20TransferExists distinguishes a settled receipt from a merely observed one.
func (r *Repository) Trc20TransferExists(ctx context.Context, txid string) (bool, error) {
	var exists int
	err := r.db.QueryRowContext(ctx, `select exists(select 1 from bp_payment_trc20_transfer where txid=? and matched_order_no is not null and matched_order_no<>'')`, txid).Scan(&exists)
	return exists == 1, err
}

// PendingTrc20Transfers also recovers receipts written by older versions after
// a failed settlement, even when the external polling cursor has moved past them.
// address 为空表示不限收款地址：改过收款地址之后，旧地址上的悬空到账同样要重试。
func (r *Repository) PendingTrc20Transfers(ctx context.Context, address string, afterID int64) ([]Trc20Transfer, error) {
	query := `select t.id,t.txid,t.from_address,t.to_address,cast(t.amount as char),t.block_timestamp
 from bp_payment_trc20_transfer t where t.matched_order_no is null and t.id>?`
	args := []any{afterID}
	if strings.TrimSpace(address) != "" {
		query += ` and binary t.to_address=binary ?`
		args = append(args, strings.TrimSpace(address))
	}
	query += `
 and exists(select 1 from bp_payment_order o where o.pay_channel='TRC20' and (
  (o.status='PAID' and o.chain_txid=t.txid) or
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
	var transfers []Trc20Transfer
	for rows.Next() {
		var item Trc20Transfer
		if err := rows.Scan(&item.ReceiptID, &item.TxID, &item.From, &item.To, &item.Amount, &item.BlockTimestamp); err != nil {
			return nil, err
		}
		item.Confirmations = trc20DefaultConfirmations
		transfers = append(transfers, item)
	}
	return transfers, rows.Err()
}

// Persist the receipt before settlement. A failed credit remains NULL/pending;
// only the settlement transaction is allowed to mark it matched.
func (r *Repository) RecordTrc20Transfer(ctx context.Context, transfer Trc20Transfer) error {
	_, err := r.db.ExecContext(ctx, `insert ignore into bp_payment_trc20_transfer(txid,from_address,to_address,amount,block_timestamp,matched_order_no)
 values(?,?,?,?,?,NULL)`, transfer.TxID, transfer.From, transfer.To, transfer.Amount, transfer.BlockTimestamp)
	return err
}

var ErrTrc20AmbiguousOrder = errors.New("multiple orders match the TRC20 transfer; manual reconciliation required")

// MatchTrc20Order 在到账时间附近找一笔真正可结算的订单。
//
// 候选口径 = 未绑 txid + 状态可结算 + 金额/收款地址完全一致 + 创建时间落在转账
// 前 24 小时内。历史同额度的 PAID 行不参与竞争：它们已经有自己的 txid，既不能
// 被这笔转账再结算一次，也不应该把唯一合法的候选污染成「歧义」——旧实现正是
// 这样把后续每一笔同额度到账都永久卡死。
func (r *Repository) MatchTrc20Order(ctx context.Context, transfer Trc20Transfer) (string, error) {
	// Recover an ambiguous commit without crediting an unrelated pending order.
	var paidOrder string
	err := r.db.QueryRowContext(ctx, `select order_no from bp_payment_order where pay_channel='TRC20' and status='PAID' and chain_txid=?`, transfer.TxID).Scan(&paidOrder)
	if err == nil {
		return paidOrder, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	candidates, err := r.matchTrc20Candidates(ctx, transfer)
	if err != nil {
		return "", err
	}
	switch len(candidates) {
	case 0:
		return "", nil
	case 1:
		return candidates[0].OrderNo, nil
	}
	// 同一用户重复下单（旧单过期、新单重生）时金额会撞在一起。按 created_at 升序
	// 取最旧的一笔，结算事务内的金额/窗口复核仍然兜底，不会把钱记到错误用户头上。
	if candidates[0].UserID == candidates[1].UserID {
		return candidates[0].OrderNo, nil
	}
	return "", ErrTrc20AmbiguousOrder
}

func (r *Repository) SettleTrc20Order(ctx context.Context, orderNo string, transfer Trc20Transfer) error {
	return r.settleChannelPaidOrder(ctx, ChannelTrc20, orderNo, PayTradeStatus{TradeNo: transfer.TxID, Total: transfer.Amount, Success: true}, &chainSettlement{
		Kind:          ChannelTrc20,
		TxID:          transfer.TxID,
		Confirmations: transfer.Confirmations,
		Transfer: chainTransfer{
			TxID:           transfer.TxID,
			From:           transfer.From,
			To:             transfer.To,
			Amount:         transfer.Amount,
			BlockTimestamp: transfer.BlockTimestamp,
			Confirmations:  transfer.Confirmations,
		},
	})
}

func lockChainTransferTx(ctx context.Context, tx *sql.Tx, orderNo string, chain *chainSettlement) error {
	if strings.EqualFold(chain.Kind, ChannelBep20) {
		return lockBep20TransferTx(ctx, tx, orderNo, chain)
	}
	return lockTrc20TransferTx(ctx, tx, orderNo, chain)
}

func bindChainTransferTx(ctx context.Context, tx *sql.Tx, orderNo string, chain *chainSettlement) error {
	if strings.EqualFold(chain.Kind, ChannelBep20) {
		return bindBep20TransferTx(ctx, tx, orderNo, chain)
	}
	return bindTrc20TransferTx(ctx, tx, orderNo, chain.TxID)
}

func lockTrc20TransferTx(ctx context.Context, tx *sql.Tx, orderNo string, chain *chainSettlement) error {
	var from, to, amount, matched string
	var timestamp int64
	err := tx.QueryRowContext(ctx, `select from_address,to_address,cast(amount as char),block_timestamp,coalesce(matched_order_no,'')
 from bp_payment_trc20_transfer where txid=? for update`, chain.TxID).Scan(&from, &to, &amount, &timestamp, &matched)
	if err != nil {
		return err
	}
	transfer := chain.Transfer
	if (matched != "" && matched != orderNo) || from != transfer.From || to != transfer.To || timestamp != transfer.BlockTimestamp || !sameTrc20Amount(amount, transfer.Amount) || transfer.Confirmations != trc20DefaultConfirmations {
		return fmt.Errorf("TRC20 receipt identity or confirmed state mismatch")
	}
	return nil
}

func bindTrc20TransferTx(ctx context.Context, tx *sql.Tx, orderNo, txid string) error {
	_, err := tx.ExecContext(ctx, `update bp_payment_trc20_transfer set matched_order_no=? where txid=? and (matched_order_no is null or matched_order_no=?)`, orderNo, txid, orderNo)
	return err
}

func sameTrc20Amount(left, right string) bool {
	a, err := decimalvalue.ParseNonNegative(left)
	if err != nil {
		return false
	}
	b, err := decimalvalue.ParseNonNegative(right)
	return err == nil && a.Cmp(b) == 0
}

// Compare-and-swap prevents a slower overlapping worker from undoing another
// worker's retry progress. Failure to settle does not keep later rows waiting.
func (r *Repository) UpdateTrc20RetryCursor(ctx context.Context, previous, next int64) error {
	_, err := r.db.ExecContext(ctx, `update bp_payment_trc20_config set retry_cursor_id=? where id=1 and retry_cursor_id=?`, next, previous)
	return err
}
