package payment

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// 币安 Pay 通道的持久化：配置读取/保存 + 回调幂等。

type storedBinancePayConfig struct {
	BinancePayConfig
}

// BinancePayConfig 读取商户配置（密文原样返回，由服务层解密）。
func (r *Repository) BinancePayConfig(ctx context.Context) (storedBinancePayConfig, error) {
	var stored storedBinancePayConfig
	var enabled int
	err := r.db.QueryRowContext(ctx, `
		select enabled,coalesce(merchant_id,''),coalesce(cert_serial,''),
		       coalesce(secret_key_cipher,''),coalesce(api_key_cipher,''),
		       pay_currency,cast(min_amount as char),cast(max_amount as char)
		from bp_payment_binancepay_config where id=1
	`).Scan(
		&enabled, &stored.MerchantID, &stored.CertSerial,
		&stored.SecretKeyCipher, &stored.APIKeyCipher,
		&stored.PayCurrency, &stored.MinAmount, &stored.MaxAmount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		// 未初始化时给一份可用默认值，避免后台打开配置页就报错。
		return storedBinancePayConfig{BinancePayConfig: BinancePayConfig{
			PayCurrency: binancePayCurrency,
			MinAmount:   "5.000000",
			MaxAmount:   "10000.000000",
		}}, nil
	}
	if err != nil {
		return storedBinancePayConfig{}, err
	}
	stored.Enabled = enabled == 1
	return stored, nil
}

// SaveBinancePayConfig 保存配置；updateSecret/updateAPIKey 为 false 时保留原密文。
func (r *Repository) SaveBinancePayConfig(ctx context.Context, cfg BinancePayConfig, secretCipher, apiKeyCipher string, updateSecret, updateAPIKey bool) error {
	currency := strings.TrimSpace(cfg.PayCurrency)
	if currency == "" {
		currency = binancePayCurrency
	}
	setClauses := []string{
		"enabled=?", "merchant_id=?", "cert_serial=?", "pay_currency=?",
		"min_amount=cast(? as decimal(18,6))", "max_amount=cast(? as decimal(18,6))",
	}
	args := []any{
		boolToTinyInt(cfg.Enabled), strings.TrimSpace(cfg.MerchantID), strings.TrimSpace(cfg.CertSerial),
		currency, strings.TrimSpace(cfg.MinAmount), strings.TrimSpace(cfg.MaxAmount),
	}
	if updateSecret {
		setClauses = append(setClauses, "secret_key_cipher=?")
		args = append(args, nullableCipher(secretCipher))
	}
	if updateAPIKey {
		setClauses = append(setClauses, "api_key_cipher=?")
		args = append(args, nullableCipher(apiKeyCipher))
	}
	args = append(args, 1)
	_, err := r.db.ExecContext(ctx, `
		update bp_payment_binancepay_config set `+strings.Join(setClauses, ", ")+` where id=?
	`, args...)
	return err
}

// ConsumeBinancePayNonce 记录一次回调 nonce。返回 false 表示这个 nonce 已经用过
// （币安重复推送），调用方必须把它当成「已处理」而不是再入一次账。
func (r *Repository) ConsumeBinancePayNonce(ctx context.Context, key string) (bool, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return false, errors.New("binance pay nonce is empty")
	}
	_, err := r.db.ExecContext(ctx, `insert into bp_payment_binancepay_nonce(nonce_key) values(?)`, key)
	if err == nil {
		return true, nil
	}
	// 唯一键冲突 = 重复推送，这是正常路径，不是错误。
	var mysqlErr *mysqldriver.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return false, nil
	}
	return false, err
}

// PurgeBinancePayNonces 清理过期幂等记录。窗口只需覆盖币安重推周期（默认 24 小时），
// 无限增长会让这张表变成第二张大表。
func (r *Repository) PurgeBinancePayNonces(ctx context.Context, olderThanHours int) error {
	if olderThanHours <= 0 {
		olderThanHours = 24
	}
	_, err := r.db.ExecContext(ctx,
		`delete from bp_payment_binancepay_nonce where created_at < date_sub(now(), interval ? hour)`, olderThanHours)
	return err
}

// SettleBinancePayOrder 走与其它通道相同的结算事务，只是没有链上转账要校验。
func (r *Repository) SettleBinancePayOrder(ctx context.Context, orderNo string, status PayTradeStatus) error {
	return r.settleChannelPaidOrder(ctx, ChannelBinancePay, orderNo, status, nil)
}

func boolToTinyInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullableCipher(value string) any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}
