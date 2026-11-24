package payment

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"math/big"
	"strings"

	"bugpk-api/internal/pkg/decimalvalue"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// BEP20 与 TRC20 共用「99 个分位槽位」的金额占用规则。
// 配置行本身是跨进程互斥锁：下单先锁配置行，再读已占用金额并落单。
func reserveBep20AmountTx(ctx context.Context, tx *sql.Tx, seed *OrderSeed) error {
	if seed.Currency != CurrencyUSD || seed.OrderType != "RECHARGE" {
		return apperrors.New(consts.StatusBadRequest, "BEP20 仅支持 USD 余额充值")
	}
	var enabled, confirmations int
	var address, minText, maxText string
	err := tx.QueryRowContext(ctx, `select enabled,address,cast(min_amount as char),cast(max_amount as char),confirmations
 from bp_payment_bep20_config where id=1 for update`).Scan(&enabled, &address, &minText, &maxText, &confirmations)
	if errors.Is(err, sql.ErrNoRows) || err == nil && enabled != 1 {
		return apperrors.New(consts.StatusServiceUnavailable, "BEP20 充值未启用")
	}
	if err != nil {
		return err
	}
	address = strings.ToLower(strings.TrimSpace(address))
	if !bep20AddressPattern.MatchString(address) || confirmations < 1 || confirmations > bep20MaxConfirmations {
		return apperrors.New(consts.StatusServiceUnavailable, "BEP20 收款或确认策略配置不可用")
	}
	base, err := decimalvalue.ParseNonNegative(strings.TrimSpace(seed.PayAmount))
	if err != nil {
		return apperrors.New(consts.StatusBadRequest, "订单金额格式不合法")
	}
	minimum, minErr := decimalvalue.ParseNonNegative(minText)
	maximum, maxErr := decimalvalue.ParseNonNegative(maxText)
	if minErr != nil || maxErr != nil || minimum.Sign() <= 0 || maximum.Cmp(minimum) < 0 {
		return apperrors.New(consts.StatusServiceUnavailable, "BEP20 金额范围配置不可用")
	}
	if base.Cmp(minimum) < 0 || base.Cmp(maximum) > 0 {
		return apperrors.New(consts.StatusBadRequest, "订单金额不在 BEP20 充值范围内")
	}
	upper := new(big.Rat).Add(base, big.NewRat(trc20AmountSlots, 100))
	rows, err := tx.QueryContext(ctx, `select cast(chain_amount as char) from bp_payment_order
 where pay_channel='BEP20' and binary chain_address=binary ?
 and chain_amount>cast(? as decimal(18,6)) and chain_amount<=cast(? as decimal(18,6)) for update`, address, base.FloatString(6), upper.FloatString(6))
	if err != nil {
		return err
	}
	occupied := make(map[string]bool)
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			_ = rows.Close()
			return err
		}
		amount, e := decimalvalue.ParseNonNegative(value)
		if e != nil {
			_ = rows.Close()
			return e
		}
		occupied[amount.FloatString(6)] = true
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	start, err := rand.Int(rand.Reader, big.NewInt(trc20AmountSlots))
	if err != nil {
		return err
	}
	for offset := 0; offset < trc20AmountSlots; offset++ {
		slot := (int(start.Int64())+offset)%trc20AmountSlots + 1
		amount := new(big.Rat).Add(base, big.NewRat(int64(slot), 100))
		if amount.Cmp(maximum) > 0 || occupied[amount.FloatString(6)] {
			continue
		}
		seed.PayAmount = amount.FloatString(4)
		seed.ChainAmount = amount.FloatString(6)
		seed.ChainAddress = address
		seed.Subject = orderSubject("余额充值", trimDecimalText(amount.FloatString(2))+" USDT")
		return nil
	}
	return apperrors.New(consts.StatusConflict, "当前充值人数较多，请稍后重试")
}
