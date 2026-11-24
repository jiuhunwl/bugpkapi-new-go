package payment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"bugpk-api/internal/pkg/masking"
)

// wechatConfigRepository 微信配置与微信专属仓储（镜像 alipayConfigRepository）。
type wechatConfigRepository interface {
	WechatConfig(context.Context) (storedWechatConfig, error)
	SaveWechatConfig(context.Context, WechatConfig, string, string, string, bool, bool) error
	SaveWechatPayPayload(context.Context, string, PayCreateResult) error
	SettleWechatPaidOrder(context.Context, string, PayTradeStatus) error
	InsertWechatNotifyLog(context.Context, map[string]string, bool, bool, string) error
	// 平台证书缓存读写（000037 表）。
	SaveWechatPlatformCert(context.Context, string, string, string, string) error
	LoadWechatPlatformCert(context.Context, string) (string, error)
}

// storedWechatConfig 微信配置落库投影（含各 cipher 字段 + WechatConfig 投影）。
type storedWechatConfig struct {
	WechatConfig
	APIv3KeyCipher          string
	PrivateKeyCipher        string
	PlatformPublicKeyCipher string
}

func (r *Repository) WechatConfig(ctx context.Context) (storedWechatConfig, error) {
	var item storedWechatConfig
	var enabled int
	err := r.db.QueryRowContext(ctx, `
		select enabled,mode,mchid,app_id,coalesce(apiv3_key_cipher,''),coalesce(private_key_cipher,''),
		       coalesce(serial_no,''),coalesce(platform_public_key_id,''),coalesce(platform_public_key_cipher,''),
		       coalesce(notify_url,''),coalesce(sp_mchid,''),coalesce(sp_app_id,''),coalesce(sub_mchid,''),
		       coalesce(sub_app_id,''),coalesce(oa_app_id,''),coalesce(mini_app_id,''),coalesce(app_app_id,'')
		from bp_payment_wechat_config where id=1
	`).Scan(&enabled, &item.Mode, &item.Mchid, &item.AppID, &item.APIv3KeyCipher, &item.PrivateKeyCipher,
		&item.SerialNo, &item.PlatformPublicKeyID, &item.PlatformPublicKeyCipher, &item.NotifyURL,
		&item.SpMchid, &item.SpAppID, &item.SubMchid, &item.SubAppID, &item.OAAppID, &item.MiniAppID, &item.AppAppID)
	item.Enabled = enabled == 1
	return item, err
}

var ErrWechatVerifierStateChanged = errors.New("wechat verification identity changed during configuration update")

func (r *Repository) SaveWechatConfig(ctx context.Context, cfg WechatConfig, apiV3Cipher, keyCipher, certCipher string, updateKey, updateCert bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current storedWechatConfig
	if err := tx.QueryRowContext(ctx, "select coalesce(platform_public_key_id,''),coalesce(platform_public_key_cipher,'') from bp_payment_wechat_config where id=1 for update").Scan(&current.PlatformPublicKeyID, &current.PlatformPublicKeyCipher); err != nil {
		return err
	}
	// Recheck against the locked row, not only the service's earlier snapshot.
	// A masked edit must retain exactly the verifier it was validated against,
	// including same-ID key replacements and inline/cache transitions. Comparing
	// ciphertext also detects re-encryption without exposing key material.
	if !updateCert {
		if certCipher != current.PlatformPublicKeyCipher {
			return ErrWechatVerifierStateChanged
		}
		if err := validateWechatVerifierRotation(cfg.PlatformPublicKeyID, "", current); err != nil {
			return ErrWechatVerifierStateChanged
		}
	}
	if err := r.saveWechatConfigTx(ctx, tx, cfg, apiV3Cipher, keyCipher, certCipher, updateKey, updateCert); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) saveWechatConfigTx(ctx context.Context, tx *sql.Tx, cfg WechatConfig, apiV3Cipher, keyCipher, certCipher string, updateKey, updateCert bool) error {
	if updateKey {
		if updateCert {
			_, err := tx.ExecContext(ctx, `
				update bp_payment_wechat_config
				set enabled=?,mode=?,mchid=?,app_id=?,apiv3_key_cipher=?,private_key_cipher=?,
				    serial_no=?,platform_public_key_id=?,platform_public_key_cipher=?,notify_url=?,
				    sp_mchid=?,sp_app_id=?,sub_mchid=?,sub_app_id=?,oa_app_id=?,mini_app_id=?,app_app_id=?
				where id=1
			`, boolInt(cfg.Enabled), strings.ToUpper(strings.TrimSpace(cfg.Mode)), nullString(cfg.Mchid), nullString(cfg.AppID),
				nullString(apiV3Cipher), nullString(keyCipher), nullString(cfg.SerialNo), nullString(cfg.PlatformPublicKeyID),
				nullString(certCipher), nullString(cfg.NotifyURL), nullString(cfg.SpMchid), nullString(cfg.SpAppID),
				nullString(cfg.SubMchid), nullString(cfg.SubAppID), nullString(cfg.OAAppID), nullString(cfg.MiniAppID), nullString(cfg.AppAppID))
			return err
		}
		_, err := tx.ExecContext(ctx, `
			update bp_payment_wechat_config
			set enabled=?,mode=?,mchid=?,app_id=?,apiv3_key_cipher=?,private_key_cipher=?,
			    serial_no=?,platform_public_key_id=?,notify_url=?,
			    sp_mchid=?,sp_app_id=?,sub_mchid=?,sub_app_id=?,oa_app_id=?,mini_app_id=?,app_app_id=?
			where id=1
		`, boolInt(cfg.Enabled), strings.ToUpper(strings.TrimSpace(cfg.Mode)), nullString(cfg.Mchid), nullString(cfg.AppID),
			nullString(apiV3Cipher), nullString(keyCipher), nullString(cfg.SerialNo), nullString(cfg.PlatformPublicKeyID),
			nullString(cfg.NotifyURL), nullString(cfg.SpMchid), nullString(cfg.SpAppID), nullString(cfg.SubMchid),
			nullString(cfg.SubAppID), nullString(cfg.OAAppID), nullString(cfg.MiniAppID), nullString(cfg.AppAppID))
		return err
	}
	if updateCert {
		_, err := tx.ExecContext(ctx, `
			update bp_payment_wechat_config
			set enabled=?,mode=?,mchid=?,app_id=?,serial_no=?,platform_public_key_id=?,platform_public_key_cipher=?,notify_url=?,
			    sp_mchid=?,sp_app_id=?,sub_mchid=?,sub_app_id=?,oa_app_id=?,mini_app_id=?,app_app_id=?
			where id=1
		`, boolInt(cfg.Enabled), strings.ToUpper(strings.TrimSpace(cfg.Mode)), nullString(cfg.Mchid), nullString(cfg.AppID),
			nullString(cfg.SerialNo), nullString(cfg.PlatformPublicKeyID), nullString(certCipher), nullString(cfg.NotifyURL),
			nullString(cfg.SpMchid), nullString(cfg.SpAppID), nullString(cfg.SubMchid), nullString(cfg.SubAppID),
			nullString(cfg.OAAppID), nullString(cfg.MiniAppID), nullString(cfg.AppAppID))
		return err
	}
	_, err := tx.ExecContext(ctx, `
		update bp_payment_wechat_config
		set enabled=?,mode=?,mchid=?,app_id=?,serial_no=?,platform_public_key_id=?,notify_url=?,
		    sp_mchid=?,sp_app_id=?,sub_mchid=?,sub_app_id=?,oa_app_id=?,mini_app_id=?,app_app_id=?
		where id=1
	`, boolInt(cfg.Enabled), strings.ToUpper(strings.TrimSpace(cfg.Mode)), nullString(cfg.Mchid), nullString(cfg.AppID),
		nullString(cfg.SerialNo), nullString(cfg.PlatformPublicKeyID), nullString(cfg.NotifyURL),
		nullString(cfg.SpMchid), nullString(cfg.SpAppID), nullString(cfg.SubMchid), nullString(cfg.SubAppID),
		nullString(cfg.OAAppID), nullString(cfg.MiniAppID), nullString(cfg.AppAppID))
	return err
}

// SaveWechatPayPayload 写入微信下单结果（状态置 PAYING + 二维码/跳转/预支付信息）。
// trade_type 直接复用 product 列（createOrderWechat 已将 product 设为对应交易类型）。
func (r *Repository) SaveWechatPayPayload(ctx context.Context, orderNo string, result PayCreateResult) error {
	updated, err := r.db.ExecContext(ctx, `
		update bp_payment_order
		set status='PAYING',trade_type=product,prepay_id=?,qr_code=?,pay_url=?,channel_trade_no=''
		where order_no=? and status='CREATED'
	`, nullString(result.PrepayID), nullString(result.CodeURL), nullString(result.H5URL), orderNo)
	if err != nil {
		return err
	}
	affected, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	var status string
	if err := r.db.QueryRowContext(ctx, `select status from bp_payment_order where order_no=?`, orderNo).Scan(&status); err != nil {
		return err
	}
	if status == "PAYING" || status == "PAID" {
		return nil
	}
	return fmt.Errorf("payment order status %s cannot accept pay payload", status)
}

// SettleWechatPaidOrder 结算微信支付成功订单（复用既有结算逻辑，多写 channel_trade_no）。
func (r *Repository) SettleWechatPaidOrder(ctx context.Context, orderNo string, status PayTradeStatus) error {
	return r.settleChannelPaidOrder(ctx, ChannelWechat, orderNo, status, nil)
}

func (r *Repository) SettleHupijiaoPaidOrder(ctx context.Context, channel, orderNo string, status PayTradeStatus) error {
	expected, err := canonicalExternalPaymentChannel(channel)
	if err != nil || (expected != ChannelHupijiaoAlipay && expected != ChannelHupijiaoWechat) {
		return ErrUnsupportedPaymentChannel
	}
	return r.settleChannelPaidOrder(ctx, expected, orderNo, status, nil)
}

func (r *Repository) settleChannelPaidOrder(ctx context.Context, expectedChannel, orderNo string, status PayTradeStatus, chain *chainSettlement) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if chain != nil {
		if err := lockChainTransferTx(ctx, tx, orderNo, chain); err != nil {
			return err
		}
	}
	var id, userID, targetID int64
	var orderType, dbStatus, payAmount, giftAmount, snapshot, storedTradeNo, storedBuyerID, storedChannel, currency, originAmount string
	var grantPoints int64
	var stockReserved int
	err = tx.QueryRowContext(ctx, `
		select id,user_id,order_type,coalesce(target_id,0),status,cast(pay_amount as char),cast(gift_amount as char),grant_points,
		       coalesce(snapshot,json_object()),coalesce(channel_trade_no,''),coalesce(buyer_id,''),coalesce(pay_channel,'ALIPAY'),
		       coalesce(currency,'CNY'),coalesce(cast(origin_amount as char),''),
		       coalesce(stock_reserved,0)
		from bp_payment_order where order_no=? for update
	`, orderNo).Scan(&id, &userID, &orderType, &targetID, &dbStatus, &payAmount, &giftAmount, &grantPoints, &snapshot, &storedTradeNo, &storedBuyerID, &storedChannel, &currency, &originAmount, &stockReserved)
	if err != nil {
		return err
	}
	if err := requirePaymentChannel(storedChannel, expectedChannel); err != nil {
		return err
	}
	status.TradeNo = strings.TrimSpace(status.TradeNo)
	status.BuyerID = strings.TrimSpace(status.BuyerID)
	if status.TradeNo == "" {
		return errors.New("wechat trade number is required for settlement")
	}
	if dbStatus == "PAID" {
		if storedTradeNo == "" || storedTradeNo != status.TradeNo {
			return errors.New("paid order wechat trade number mismatch")
		}
		if storedBuyerID != "" && status.BuyerID != "" && storedBuyerID != status.BuyerID {
			return errors.New("paid order wechat buyer mismatch")
		}
		if chain != nil {
			if err := bindChainTransferTx(ctx, tx, orderNo, chain); err != nil {
				return err
			}
			return r.commitPaidOrder(ctx, tx, id, status.TradeNo, status.BuyerID, expectedChannel)
		}
		// The matching PAID row is already durable; release the read lock
		// via deferred rollback rather than requiring a redundant COMMIT.
		return nil
	}
	if dbStatus != "CREATED" && dbStatus != "PAYING" && dbStatus != "EXPIRED" && dbStatus != "CANCELLED" {
		return errors.New("payment order status cannot be settled")
	}
	if chain != nil {
		if err := validateChainOrderTx(ctx, tx, id, chain); err != nil {
			return err
		}
	}
	if err := r.applySettlement(ctx, tx, id, userID, targetID, orderType, payAmount, giftAmount, grantPoints, snapshot, currency, originAmount); err != nil {
		return err
	}
	if (dbStatus == "EXPIRED" || dbStatus == "CANCELLED") && stockReserved == 2 {
		if err := reserveOrderStockOnLateSettlementTx(ctx, tx, orderType, targetID); err != nil {
			return err
		}
		stockReserved = 3
	} else if stockReserved == 1 {
		stockReserved = 3
	}
	chainTxID := ""
	confirmations := 0
	if chain != nil {
		chainTxID = strings.TrimSpace(chain.TxID)
		confirmations = chain.Confirmations
	}
	result, err := tx.ExecContext(ctx, `
		update bp_payment_order
		set status='PAID',stock_reserved=?,channel_trade_no=?,buyer_id=?,paid_at=now(),settled_at=now(),
		    chain_txid=coalesce(nullif(?, ''), chain_txid),
		    chain_confirm_count=if(? > 0, ?, chain_confirm_count)
		where id=? and status in ('CREATED','PAYING','EXPIRED','CANCELLED')
	`, stockReserved, status.TradeNo, nullString(status.BuyerID), chainTxID, confirmations, confirmations, id)
	if err != nil {
		return err
	}
	if err := requireAffected(result); err != nil {
		return err
	}
	if chain != nil {
		if err := bindChainTransferTx(ctx, tx, orderNo, chain); err != nil {
			return err
		}
	}
	return r.commitPaidOrder(ctx, tx, id, status.TradeNo, status.BuyerID, expectedChannel)
}

// InsertWechatNotifyLog 写入微信通知审计日志（复用 00035 replay guard 唯一键，event_type='WECHAT_NOTIFY'）。
func (r *Repository) InsertWechatNotifyLog(ctx context.Context, values map[string]string, verified, processed bool, message string) error {
	payload, err := json.Marshal(sanitizeNotifyValues(values))
	if err != nil {
		return err
	}
	message = masking.ErrorText(message)
	_, err = r.db.ExecContext(ctx, `
		insert into bp_payment_notify_log(order_no,trade_no,notify_id,event_type,verified,processed,message,payload)
		values(?,?,?,?,?,?,?,?)
		on duplicate key update
			order_no=if(values(processed)>processed or (values(processed)=processed and values(verified)>=verified),values(order_no),order_no),
			trade_no=if(values(processed)>processed or (values(processed)=processed and values(verified)>=verified),values(trade_no),trade_no),
			message=if(values(processed)>processed or (values(processed)=processed and values(verified)>=verified),values(message),message),
			payload=if(values(processed)>processed or (values(processed)=processed and values(verified)>=verified),values(payload),payload),
			verified=greatest(verified,values(verified)),
			processed=greatest(processed,values(processed)),
			attempt_count=if(attempt_count<4294967295,attempt_count+1,attempt_count),
			last_seen_at=now()
	`, nullString(values["out_trade_no"]), nullString(values["trade_no"]), nullString(values["notify_id"]), "WECHAT_NOTIFY", boolInt(verified), boolInt(processed), nullString(message), string(payload))
	return err
}

// SaveWechatPlatformCert 缓存微信平台证书（000037 表），按 serial_no 幂等写入。
func (r *Repository) SaveWechatPlatformCert(ctx context.Context, serialNo, publicKeyCipher, effectiveTime, expireTime string) error {
	_, err := r.db.ExecContext(ctx, `
		insert into bp_payment_wechat_platform_cert(serial_no,public_key_cipher,effective_time,expire_time)
		values(?,?,?,?)
		on duplicate key update public_key_cipher=values(public_key_cipher),
			effective_time=coalesce(values(effective_time),effective_time),
			expire_time=coalesce(values(expire_time),expire_time)
	`, serialNo, publicKeyCipher, nullString(effectiveTime), nullString(expireTime))
	return err
}

// LoadWechatPlatformCert 读取缓存的微信平台证书密文（按 serial_no）。
func (r *Repository) LoadWechatPlatformCert(ctx context.Context, serialNo string) (string, error) {
	var cipher string
	err := r.db.QueryRowContext(ctx, `
		select coalesce(public_key_cipher,'') from bp_payment_wechat_platform_cert where serial_no=?
		  and (effective_time is null or effective_time<=now())
		  and (expire_time is null or expire_time>now())
	`, serialNo).Scan(&cipher)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return cipher, nil
}
