package payment

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"

	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/masking"
	"bugpk-api/internal/pkg/pagination"
)

type Repository struct {
	db *sql.DB
}

var (
	ErrBalanceCapacityExceeded = errors.New("balance capacity exceeded")
	ErrPointBalanceLimit       = errors.New("point balance limit exceeded")
	ErrRechargePresetHasOrders = errors.New("recharge preset has payment order history")
)

const (
	maxUserBalanceText  = "99999999999999.9999"
	maxUserPointBalance = int64(1_000_000_000_000_000)
)

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

type storedAlipayConfig struct {
	AlipayConfig
	PrivateKeyCipher string
}

func (r *Repository) AlipayConfig(ctx context.Context) (storedAlipayConfig, error) {
	var item storedAlipayConfig
	var enabled int
	err := r.db.QueryRowContext(ctx, `
		select mode,app_id,gateway_url,coalesce(app_private_key_cipher,''),coalesce(alipay_public_key,''),coalesce(seller_id,''),coalesce(notify_url,''),coalesce(return_url,''),enabled
		from bp_payment_alipay_config where id=1
	`).Scan(&item.Mode, &item.AppID, &item.GatewayURL, &item.PrivateKeyCipher, &item.AlipayPublicKey, &item.SellerID, &item.NotifyURL, &item.ReturnURL, &enabled)
	item.Enabled = enabled == 1
	return item, err
}

func (r *Repository) SaveAlipayConfig(ctx context.Context, cfg AlipayConfig, privateCipher string, updatePrivate bool) error {
	if updatePrivate {
		_, err := r.db.ExecContext(ctx, `
			update bp_payment_alipay_config
			set mode=?,app_id=?,gateway_url=?,app_private_key_cipher=?,alipay_public_key=?,seller_id=?,notify_url=?,return_url=?,enabled=?
			where id=1
		`, cfg.Mode, cfg.AppID, cfg.GatewayURL, privateCipher, cfg.AlipayPublicKey, nullString(cfg.SellerID), nullString(cfg.NotifyURL), nullString(cfg.ReturnURL), boolInt(cfg.Enabled))
		return err
	}
	_, err := r.db.ExecContext(ctx, `
		update bp_payment_alipay_config
		set mode=?,app_id=?,gateway_url=?,alipay_public_key=?,seller_id=?,notify_url=?,return_url=?,enabled=?
		where id=1
	`, cfg.Mode, cfg.AppID, cfg.GatewayURL, cfg.AlipayPublicKey, nullString(cfg.SellerID), nullString(cfg.NotifyURL), nullString(cfg.ReturnURL), boolInt(cfg.Enabled))
	return err
}

func (r *Repository) ListPresets(ctx context.Context, params ListParams, enabledOnly bool) (PageResult[RechargePreset], error) {
	page, size := normalizePage(params.Page, params.PageSize)
	where := " where 1=1"
	args := []any{}
	if strings.TrimSpace(params.Keyword) != "" {
		where += " and name like ?"
		args = append(args, "%"+strings.TrimSpace(params.Keyword)+"%")
	}
	if enabledOnly {
		where += " and status=1"
	} else if strings.TrimSpace(params.Status) != "" {
		where += " and status=?"
		args = append(args, params.Status)
	}
	var (
		total int64
		items []RechargePreset
	)
	countArgs := append([]any(nil), args...)
	listArgs := append(append([]any(nil), args...), size, pagination.Offset(page, size))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, "select count(*) from bp_recharge_preset"+where, countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, `
				select id,name,cast(amount as char),cast(gift_amount as char),sort_order,status,
				       date_format(updated_at,'%Y-%m-%d %H:%i:%s')
				from bp_recharge_preset`+where+` order by sort_order asc,id asc limit ? offset ?`, listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items = make([]RechargePreset, 0, size)
			for rows.Next() {
				var item RechargePreset
				var id int64
				if err := rows.Scan(&id, &item.Name, &item.Amount, &item.GiftAmount, &item.SortOrder, &item.Status, &item.UpdateTime); err != nil {
					return err
				}
				item.ID = strconv.FormatInt(id, 10)
				items = append(items, item)
			}
			return rows.Err()
		},
	)
	if err != nil {
		return PageResult[RechargePreset]{}, err
	}
	return PageResult[RechargePreset]{Items: items, Total: total}, nil
}

func (r *Repository) SavePreset(ctx context.Context, id int64, item RechargePreset) error {
	if id == 0 {
		_, err := r.db.ExecContext(ctx, `insert into bp_recharge_preset(name,amount,gift_amount,sort_order,status) values(?,?,?,?,?)`,
			item.Name, item.Amount, item.GiftAmount, item.SortOrder, item.Status)
		return err
	}
	result, err := r.db.ExecContext(ctx, `update bp_recharge_preset set name=?,amount=?,gift_amount=?,sort_order=?,status=? where id=?`,
		item.Name, item.Amount, item.GiftAmount, item.SortOrder, item.Status, id)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

func (r *Repository) DeletePreset(ctx context.Context, id int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var lockedID int64
	if err := tx.QueryRowContext(ctx, "select id from bp_recharge_preset where id=? for update", id).Scan(&lockedID); err != nil {
		return err
	}
	var referenced int
	if err := tx.QueryRowContext(ctx, `
		select exists(
			select 1 from bp_payment_order
			where order_type='RECHARGE' and target_id=?
			limit 1
		)
	`, id).Scan(&referenced); err != nil {
		return err
	}
	if referenced != 0 {
		return ErrRechargePresetHasOrders
	}
	result, err := tx.ExecContext(ctx, "delete from bp_recharge_preset where id=?", id)
	if err != nil {
		return err
	}
	if err := requireAffected(result); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) BuildRechargeOrder(ctx context.Context, userID, presetID int64, orderNo, product string) (OrderSeed, error) {
	var name, amount, gift string
	if err := r.db.QueryRowContext(ctx, `select name,cast(amount as char),cast(gift_amount as char) from bp_recharge_preset where id=? and status=1`, presetID).Scan(&name, &amount, &gift); err != nil {
		return OrderSeed{}, err
	}
	snapshot, err := json.Marshal(map[string]any{"presetId": presetID, "name": name, "amount": amount, "giftAmount": gift})
	if err != nil {
		return OrderSeed{}, err
	}
	return OrderSeed{OrderNo: orderNo, UserID: userID, OrderType: "RECHARGE", Product: product, TargetID: presetID, PayAmount: amount, GiftAmount: gift, Subject: orderSubject("余额充值", trimDecimalText(amount)+"元"), Snapshot: string(snapshot)}, nil
}

// orderSubject 拼接支付订单的商品名称（微信/支付宝收银台与订单记录均展示），
// 统一为「类型-名称-规格」的中文格式，跳过空段。
func orderSubject(parts ...string) string {
	cleaned := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			cleaned = append(cleaned, part)
		}
	}
	return strings.Join(cleaned, "-")
}

// trimDecimalText 去掉 decimal(12,4) 文本尾部的零："10.0000" -> "10"，"1.5000" -> "1.5"。
func trimDecimalText(value string) string {
	value = strings.TrimSpace(value)
	idx := strings.IndexByte(value, '.')
	if idx < 0 {
		return value
	}
	frac := strings.TrimRight(value[idx+1:], "0")
	if frac == "" {
		return value[:idx]
	}
	return value[:idx+1] + frac
}

func (r *Repository) BuildGlobalPackageOrder(ctx context.Context, userID, packageID int64, orderNo, product string) (OrderSeed, error) {
	var name, price string
	var validDays, qpsLimit int
	var dailyLimit, totalLimit int64
	var countMode string
	if err := r.db.QueryRowContext(ctx, `select name,cast(price as char),valid_days,daily_limit,total_limit,count_mode,qps_limit from bp_package_global where id=? and status=1`, packageID).Scan(&name, &price, &validDays, &dailyLimit, &totalLimit, &countMode, &qpsLimit); err != nil {
		return OrderSeed{}, err
	}
	if countMode == "" {
		countMode = "success"
	}
	// totalLimit/countMode 进快照：结算发放权益时按下单时刻的配置快照，
	// 之后改套餐模板不影响已支付订单的发放内容。
	snapshot, err := json.Marshal(map[string]any{"packageId": packageID, "name": name, "validDays": validDays, "dailyLimit": dailyLimit, "totalLimit": totalLimit, "countMode": countMode, "qpsLimit": qpsLimit})
	if err != nil {
		return OrderSeed{}, err
	}
	return OrderSeed{OrderNo: orderNo, UserID: userID, OrderType: "GLOBAL_PACKAGE", Product: product, TargetID: packageID, PayAmount: price, Subject: orderSubject("全站套餐", name), Snapshot: string(snapshot)}, nil
}

func (r *Repository) BuildInterfacePackageOrder(ctx context.Context, userID, specID int64, orderNo, product string) (OrderSeed, error) {
	var packageID, interfaceID int64
	var packageName, specName, price string
	var validDays, qpsLimit int
	var dailyLimit, totalLimit int64
	var countMode string
	err := r.db.QueryRowContext(ctx, buildInterfacePackageOrderQuery(), specID).Scan(&packageID, &interfaceID, &packageName, &specName, &price, &validDays, &dailyLimit, &totalLimit, &countMode, &qpsLimit)
	if err != nil {
		return OrderSeed{}, err
	}
	if countMode == "" {
		countMode = "success"
	}
	snapshot, err := json.Marshal(map[string]any{"packageId": packageID, "interfaceId": interfaceID, "packageName": packageName, "specName": specName, "validDays": validDays, "dailyLimit": dailyLimit, "totalLimit": totalLimit, "countMode": countMode, "qpsLimit": qpsLimit})
	if err != nil {
		return OrderSeed{}, err
	}
	return OrderSeed{OrderNo: orderNo, UserID: userID, OrderType: "INTERFACE_PACKAGE", Product: product, TargetID: specID, PayAmount: price, Subject: orderSubject("接口套餐", packageName, specName), Snapshot: string(snapshot)}, nil
}

func buildInterfacePackageOrderQuery() string {
	return `
		select p.id,p.interface_id,p.name,s.name,cast(s.price as char),s.valid_days,s.daily_limit,s.total_limit,s.count_mode,s.qps_limit
		from bp_package_interface_spec s
		inner join bp_package_interface p on p.id=s.package_id
		inner join bp_interface_api a on a.id=p.interface_id
		where s.id=? and s.status=1 and p.status=1 and a.status=1
	`
}

func (r *Repository) BuildPointPackageOrder(ctx context.Context, userID, packageID int64, orderNo, product string) (OrderSeed, error) {
	var name, price string
	var points int64
	if err := r.db.QueryRowContext(ctx, `select name,cast(price as char),points from bp_package_point where id=? and status=1`, packageID).Scan(&name, &price, &points); err != nil {
		return OrderSeed{}, err
	}
	snapshot, err := json.Marshal(map[string]any{"packageId": packageID, "name": name, "points": points})
	if err != nil {
		return OrderSeed{}, err
	}
	return OrderSeed{OrderNo: orderNo, UserID: userID, OrderType: "POINT_PACKAGE", Product: product, TargetID: packageID, PayAmount: price, GrantPoints: points, Subject: orderSubject("点数套餐", name), Snapshot: string(snapshot)}, nil
}

func (r *Repository) CreateOrder(ctx context.Context, seed OrderSeed) (PaymentOrder, error) {
	if strings.TrimSpace(seed.GiftAmount) == "" {
		seed.GiftAmount = "0.0000"
	}
	if strings.TrimSpace(seed.PayChannel) == "" {
		seed.PayChannel = ChannelAlipay
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return PaymentOrder{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if seed.PayChannel == ChannelTrc20 {
		if err := reserveTrc20AmountTx(ctx, tx, &seed); err != nil {
			return PaymentOrder{}, err
		}
	}
	if seed.PayChannel == ChannelBep20 {
		if err := reserveBep20AmountTx(ctx, tx, &seed); err != nil {
			return PaymentOrder{}, err
		}
	}

	reserved, err := validateOrderTargetTx(ctx, tx, &seed)
	if err != nil {
		return PaymentOrder{}, err
	}
	seed.StockReserved = reserved
	stockReservedValue := 0
	if reserved {
		stockReservedValue = 1
	}
	expireMinutes := orderExpireMinutes(seed.PayChannel)
	// 货币标签缺省 CNY：迁移前建的老代码路径与存量行为保持一致。
	seedCurrency := strings.ToUpper(strings.TrimSpace(seed.Currency))
	if seedCurrency == "" {
		seedCurrency = CurrencyCNY
	}
	if _, err := tx.ExecContext(ctx, `
		insert into bp_payment_order(order_no,user_id,order_type,product,pay_channel,currency,origin_amount,chain_address,chain_amount,subject,target_id,pay_amount,gift_amount,grant_points,status,expire_at,stock_reserved,snapshot)
		values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'CREATED',date_add(now(), interval ? minute),?,?)
	`, seed.OrderNo, seed.UserID, seed.OrderType, seed.Product, seed.PayChannel, seedCurrency, nullString(seed.OriginAmount), nullString(seed.ChainAddress), nullString(seed.ChainAmount), seed.Subject, nullInt64(seed.TargetID), seed.PayAmount, seed.GiftAmount, seed.GrantPoints, expireMinutes, stockReservedValue, seed.Snapshot); err != nil {
		return PaymentOrder{}, err
	}
	if err := tx.Commit(); err != nil {
		return PaymentOrder{}, err
	}
	return r.OrderByNo(ctx, seed.OrderNo, seed.UserID, false)
}

func (r *Repository) SavePayPayload(ctx context.Context, orderNo string, result AlipayCreateResult) error {
	updated, err := r.db.ExecContext(ctx, `
		update bp_payment_order
		set status='PAYING',pay_form=?,pay_url=?,qr_code=?
		where order_no=? and status='CREATED'
	`, nullString(result.PayForm), nullString(result.PayURL), nullString(result.QRCode), orderNo)
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

func (r *Repository) SaveHupijiaoPayPayload(ctx context.Context, orderNo string, result PayCreateResult) error {
	updated, err := r.db.ExecContext(ctx, `
		update bp_payment_order
		set status='PAYING',pay_url=?,qr_code=?
		where order_no=? and status='CREATED'
	`, nullString(result.H5URL), nullString(result.CodeURL), orderNo)
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
	return fmt.Errorf("payment order status %s cannot accept Hupijiao payload", status)
}

func (r *Repository) MarkOrderClosed(ctx context.Context, orderNo, expectedChannel string) error {
	return r.markOrderEnded(ctx, orderNo, expectedChannel, "CLOSED")
}

// MarkOrderExpired 标记本地到期的未支付订单。与通道主动关单的 CLOSED 区分开：
// 过期只代表本地不再允许发起支付，若支付通道随后确认到账，仍允许补结算。
func (r *Repository) MarkOrderExpired(ctx context.Context, orderNo, expectedChannel string) error {
	return r.markOrderEnded(ctx, orderNo, expectedChannel, "EXPIRED")
}

// MarkOrderCancelled 标记用户主动取消的未支付订单。与 EXPIRED 同类：只代表本地不再
// 允许发起支付，渠道侧的旧支付串可能仍然有效，所以随后确认到账时仍允许补结算。
func (r *Repository) MarkOrderCancelled(ctx context.Context, orderNo, expectedChannel string) error {
	return r.markOrderEnded(ctx, orderNo, expectedChannel, "CANCELLED")
}

func (r *Repository) markOrderEnded(ctx context.Context, orderNo, expectedChannel, targetStatus string) error {
	if targetStatus != "CLOSED" && targetStatus != "EXPIRED" && targetStatus != "CANCELLED" {
		return errors.New("invalid payment order terminal status")
	}
	if _, err := canonicalExternalPaymentChannel(expectedChannel); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status, channel, orderType string
	var targetID int64
	var stockReserved int
	if err := tx.QueryRowContext(ctx, `
		select status,coalesce(pay_channel,'ALIPAY'),order_type,coalesce(target_id,0),coalesce(stock_reserved,0)
		from bp_payment_order where order_no=? for update
	`, orderNo).Scan(&status, &channel, &orderType, &targetID, &stockReserved); err != nil {
		return err
	}
	if err := requirePaymentChannel(channel, expectedChannel); err != nil {
		return err
	}
	if status == "PAID" {
		// PAID 是资金已到账的终态，任何关闭/过期事件都不能降级它。
		return nil
	}
	if status == "CLOSED" || status == "EXPIRED" || status == "CANCELLED" {
		return nil
	}
	if status != "CREATED" && status != "PAYING" {
		return errors.New("payment order status cannot be closed")
	}
	newReserved := stockReserved
	if stockReserved == 1 {
		if err := releaseOrderStockTx(ctx, tx, orderType, targetID); err != nil {
			return err
		}
		newReserved = 2
	}
	result, err := tx.ExecContext(ctx, `
		update bp_payment_order
		set status=?,stock_reserved=?
		where order_no=? and status in ('CREATED','PAYING')
	`, targetStatus, newReserved, orderNo)
	if err != nil {
		return err
	}
	if err := requireAffected(result); err != nil {
		return err
	}
	return tx.Commit()
}

// SoftDeleteOrder 从交易记录里隐藏订单。行不删：支付审计要留，旧支付载荷也还在，
// 迟到到账仍能补结算——只有列表查询过滤 deleted_at。
// 返回 false 表示当前状态或归属不允许删除，由调用方给业务错误。
func (r *Repository) SoftDeleteOrder(ctx context.Context, orderNo string, userID int64, admin bool) (bool, error) {
	query := `update bp_payment_order
		set deleted_at=now()
		where order_no=? and deleted_at is null
		  and status in ('EXPIRED','CLOSED','CANCELLED','FAILED')`
	args := []any{orderNo}
	if !admin {
		query += " and user_id=?"
		args = append(args, userID)
	}
	result, err := r.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// PendingOrderNo 返回同一用户、同一商品未过期且仍可支付的订单号。
func (r *Repository) PendingOrderNo(ctx context.Context, userID int64, orderType string, targetID int64) (string, error) {
	var orderNo string
	err := r.db.QueryRowContext(ctx, `
		select order_no
		from bp_payment_order
		where user_id=? and order_type=? and target_id=?
		  and status in ('CREATED','PAYING')
		  and expire_at > now()
		order by id desc
		limit 1
	`, userID, orderType, targetID).Scan(&orderNo)
	return orderNo, err
}

func (r *Repository) OrderExpired(ctx context.Context, orderNo string) (bool, error) {
	var expired int
	if err := r.db.QueryRowContext(ctx, "select if(expire_at<=now(),1,0) from bp_payment_order where order_no=?", orderNo).Scan(&expired); err != nil {
		return false, err
	}
	return expired == 1, nil
}

// ExpireDueOrders 关闭本地已过期且仍未支付的订单，并释放限量套餐的库存占用。
// 返回实际处理的订单数。逐行加锁可保证与支付回调/主动查单的结算互斥。
func (r *Repository) ExpireDueOrders(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := r.db.QueryContext(ctx, `
		select order_no
		from bp_payment_order
		where status in ('CREATED','PAYING') and expire_at <= now()
		order by expire_at asc, id asc
		limit ?
	`, limit)
	if err != nil {
		return 0, err
	}
	orderNos := make([]string, 0, limit)
	for rows.Next() {
		var orderNo string
		if err := rows.Scan(&orderNo); err != nil {
			_ = rows.Close()
			return 0, err
		}
		orderNos = append(orderNos, orderNo)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	_ = rows.Close()

	expired := 0
	for _, orderNo := range orderNos {
		changed, err := r.expireOrder(ctx, orderNo)
		if err != nil {
			return expired, err
		}
		if changed {
			expired++
		}
	}
	return expired, nil
}

func (r *Repository) expireOrder(ctx context.Context, orderNo string) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var status, orderType string
	var targetID int64
	var stockReserved int
	err = tx.QueryRowContext(ctx, `
		select status,order_type,coalesce(target_id,0),coalesce(stock_reserved,0)
		from bp_payment_order where order_no=? for update
	`, orderNo).Scan(&status, &orderType, &targetID, &stockReserved)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if status != "CREATED" && status != "PAYING" {
		return false, nil
	}
	if stockReserved == 1 {
		if err := releaseOrderStockTx(ctx, tx, orderType, targetID); err != nil {
			return false, err
		}
		stockReserved = 2
	}
	result, err := tx.ExecContext(ctx, `
		update bp_payment_order
		set status='EXPIRED',stock_reserved=?
		where order_no=? and status in ('CREATED','PAYING')
	`, stockReserved, orderNo)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		return false, nil
	}
	return true, tx.Commit()
}

func (r *Repository) OrderByNo(ctx context.Context, orderNo string, userID int64, admin bool) (PaymentOrder, error) {
	where := "where o.order_no=?"
	args := []any{orderNo}
	if !admin {
		where += " and o.user_id=?"
		args = append(args, userID)
	}
	var item PaymentOrder
	var id, targetID, uid int64
	err := r.db.QueryRowContext(ctx, `
		select o.id,o.order_no,o.user_id,o.order_type,o.product,coalesce(o.pay_channel,'ALIPAY'),coalesce(o.currency,'CNY'),coalesce(cast(o.origin_amount as char),''),o.subject,coalesce(o.target_id,0),cast(o.pay_amount as char),cast(o.gift_amount as char),o.grant_points,o.status,
		coalesce(o.alipay_trade_no,''),coalesce(o.buyer_id,''),coalesce(o.pay_form,''),coalesce(o.pay_url,''),coalesce(o.qr_code,''),date_format(o.expire_at,'%Y-%m-%d %H:%i:%s'),
		coalesce(date_format(o.paid_at,'%Y-%m-%d %H:%i:%s'),''),date_format(o.created_at,'%Y-%m-%d %H:%i:%s'),
		coalesce(nullif(u.display_name,''),u.username,''),
		coalesce(o.trade_type,''),coalesce(o.prepay_id,''),coalesce(o.channel_trade_no,''),
		coalesce(o.chain_address,''),coalesce(cast(o.chain_amount as char),''),coalesce(o.chain_txid,''),coalesce(o.chain_confirm_count,0),
		greatest(timestampdiff(second, now(), o.expire_at),0)
		from bp_payment_order o left join bp_user u on u.id=o.user_id `+where, args...).
		Scan(&id, &item.OrderNo, &uid, &item.OrderType, &item.Product, &item.PayChannel, &item.Currency, &item.OriginAmount, &item.Subject, &targetID, &item.PayAmount, &item.GiftAmount, &item.GrantPoints, &item.Status,
			&item.AlipayTradeNo, &item.BuyerID, &item.PayForm, &item.PayURL, &item.QRCode, &item.ExpireTime, &item.PaidTime, &item.CreateTime, &item.Username,
			&item.TradeType, &item.PrepayID, &item.ChannelTradeNo, &item.ChainAddress, &item.ChainAmount, &item.ChainTxID, &item.ChainConfirmCount, &item.RemainingSecs)
	if err != nil {
		return PaymentOrder{}, err
	}
	item.ID = strconv.FormatInt(id, 10)
	item.UserID = strconv.FormatInt(uid, 10)
	if targetID > 0 {
		item.TargetID = strconv.FormatInt(targetID, 10)
	}
	return item, nil
}

// UserEmailByID 返回用户邮箱（支付成功通知用）；邮箱为空或用户不存在时返回空串。
func (r *Repository) UserEmailByID(ctx context.Context, userID int64) (string, error) {
	var email string
	err := r.db.QueryRowContext(ctx,
		`select coalesce(email,'') from bp_user where id=?`, userID).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return email, nil
}

func (r *Repository) ListOrders(ctx context.Context, params ListParams, userID int64, admin bool) (PageResult[PaymentOrder], error) {
	page, size := normalizePage(params.Page, params.PageSize)
	where, args := paymentOrderWhere(params, userID, admin)
	var (
		total int64
		items []PaymentOrder
	)
	countArgs := append(append([]any(nil), args...), size*pagination.MaxPage)
	listArgs := append(append([]any(nil), args...), size, pagination.Offset(page, size))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, paymentOrderCountQuery(params, where), countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, paymentOrderListQuery(params, where), listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items = make([]PaymentOrder, 0, size)
			for rows.Next() {
				var item PaymentOrder
				var id, uid, targetID int64
				if err := rows.Scan(&id, &item.OrderNo, &uid, &item.OrderType, &item.Product, &item.PayChannel, &item.Currency, &item.OriginAmount, &item.Subject, &targetID, &item.PayAmount, &item.GiftAmount, &item.GrantPoints, &item.Status,
					&item.AlipayTradeNo, &item.BuyerID, &item.QRCode, &item.ExpireTime, &item.PaidTime, &item.CreateTime, &item.Username,
					&item.TradeType, &item.PrepayID, &item.ChannelTradeNo, &item.ChainAddress, &item.ChainAmount, &item.ChainTxID, &item.ChainConfirmCount, &item.RemainingSecs); err != nil {
					return err
				}
				item.ID = strconv.FormatInt(id, 10)
				item.UserID = strconv.FormatInt(uid, 10)
				if targetID > 0 {
					item.TargetID = strconv.FormatInt(targetID, 10)
				}
				items = append(items, item)
			}
			return rows.Err()
		},
	)
	if err != nil {
		return PageResult[PaymentOrder]{}, err
	}
	return PageResult[PaymentOrder]{Items: items, Total: total}, nil
}

// assetRecordBizTypes 是管理端「资产变动记录」允许展示的流水类型。
// 刻意排除 OPEN_CALL_* 逐次调用预扣，否则一次调用一条流水会淹没人工审计。
const assetRecordBizTypesSQL = `'ADMIN_USER_INITIAL_ASSET','ADMIN_USER_ASSET_ADJUST','ADMIN_BALANCE_ADJUST','ALIPAY_RECHARGE','ALIPAY_POINT_PACKAGE','REDEEM_CODE','POINTS_GRANT','PURCHASE_GLOBAL_PACKAGE','PURCHASE_INTERFACE_PACKAGE','PURCHASE_POINT_PACKAGE'`

const assetRecordAdminBizTypesSQL = `'ADMIN_USER_INITIAL_ASSET','ADMIN_USER_ASSET_ADJUST','ADMIN_BALANCE_ADJUST'`

func (r *Repository) ListAssetRecords(ctx context.Context, params AssetRecordListParams) (PageResult[AssetRecord], error) {
	page, size := normalizePage(params.Page, params.PageSize)
	where, args := assetRecordWhere(params)
	countArgs := append(append([]any(nil), args...), size*pagination.MaxPage)
	listArgs := append(append([]any(nil), args...), size, pagination.Offset(page, size))
	var (
		total int64
		items []AssetRecord
	)
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, `
				select count(*)
				from (
					select w.id
					from bp_wallet_transaction w
					left join bp_user u on u.id=w.user_id`+where+`
					limit ?
				) counted
			`, countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, `
				select w.id,w.user_id,u.username,coalesce(u.display_name,''),
				       w.biz_type,coalesce(w.biz_id,0),cast(w.amount as char),w.points,
				       cast(w.balance_after as char),w.points_after,coalesce(w.remark,''),
				       date_format(w.created_at,'%Y-%m-%d %H:%i:%s'),coalesce(op.username,'')
				from (
					select w.id,w.created_at
					from bp_wallet_transaction w
					left join bp_user u on u.id=w.user_id`+where+`
					order by w.created_at desc,w.id desc
					limit ? offset ?
				) filtered
				inner join bp_wallet_transaction w on w.id=filtered.id
				inner join bp_user u on u.id=w.user_id
				left join bp_user op on op.id=w.biz_id and w.biz_type in (`+assetRecordAdminBizTypesSQL+`)
				order by w.created_at desc,w.id desc
			`, listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items = make([]AssetRecord, 0, size)
			for rows.Next() {
				var item AssetRecord
				var id, userID, bizID int64
				if err := rows.Scan(
					&id,
					&userID,
					&item.Username,
					&item.DisplayName,
					&item.BizType,
					&bizID,
					&item.Amount,
					&item.Points,
					&item.BalanceAfter,
					&item.PointsAfter,
					&item.Remark,
					&item.CreateTime,
					&item.OperatorName,
				); err != nil {
					return err
				}
				item.ID = strconv.FormatInt(id, 10)
				item.UserID = strconv.FormatInt(userID, 10)
				if bizID > 0 {
					item.BizID = strconv.FormatInt(bizID, 10)
				}
				items = append(items, item)
			}
			return rows.Err()
		},
	)
	if err != nil {
		return PageResult[AssetRecord]{}, err
	}
	return PageResult[AssetRecord]{Items: items, Total: total}, nil
}

func assetRecordWhere(params AssetRecordListParams) (string, []any) {
	where := " where w.biz_type in (" + assetRecordBizTypesSQL + ")"
	args := make([]any, 0, 8)
	if strings.TrimSpace(params.BizType) != "" {
		where += " and w.biz_type=?"
		args = append(args, strings.TrimSpace(params.BizType))
	}
	if strings.TrimSpace(params.Keyword) != "" {
		where += " and (u.username like ? or u.display_name like ? or w.remark like ?)"
		kw := "%" + strings.TrimSpace(params.Keyword) + "%"
		args = append(args, kw, kw, kw)
	}
	if strings.TrimSpace(params.DateFrom) != "" {
		where += " and w.created_at>=?"
		args = append(args, strings.TrimSpace(params.DateFrom))
	}
	if strings.TrimSpace(params.DateTo) != "" {
		where += " and w.created_at<date_add(?, interval 1 day)"
		args = append(args, strings.TrimSpace(params.DateTo))
	}
	return where, args
}

func paymentOrderWhere(params ListParams, userID int64, admin bool) (string, []any) {
	// 交易记录里的「删除」是软隐藏，管理端同样不再列出；订单行仍保留可审计。
	where := " where 1=1 and o.deleted_at is null"
	args := []any{}
	if !admin {
		where += " and o.user_id=?"
		args = append(args, userID)
	}
	if strings.TrimSpace(params.Keyword) != "" {
		where += " and (o.order_no like ? or o.subject like ? or o.alipay_trade_no like ? or u.username like ? or u.display_name like ?)"
		kw := "%" + strings.TrimSpace(params.Keyword) + "%"
		args = append(args, kw, kw, kw, kw, kw)
	}
	if strings.TrimSpace(params.Status) != "" {
		where += " and o.status=?"
		args = append(args, strings.TrimSpace(params.Status))
	}
	if strings.TrimSpace(params.OrderType) != "" {
		where += " and o.order_type=?"
		args = append(args, strings.TrimSpace(params.OrderType))
	}
	if params.PaidFrom != "" {
		where += " and o.paid_at>=?"
		args = append(args, params.PaidFrom)
	}
	if params.PaidTo != "" {
		where += " and o.paid_at<date_add(?, interval 1 day)"
		args = append(args, params.PaidTo)
	}
	if params.MinAmount != "" {
		where += " and o.pay_amount>=cast(? as decimal(12,4))"
		args = append(args, params.MinAmount)
	}
	if params.MaxAmount != "" {
		where += " and o.pay_amount<=cast(? as decimal(12,4))"
		args = append(args, params.MaxAmount)
	}
	return where, args
}

func paymentOrderFilterLookupJoins(params ListParams) string {
	if strings.TrimSpace(params.Keyword) == "" {
		return ""
	}
	return " left join bp_user u on u.id=o.user_id"
}

func paymentOrderCountQuery(params ListParams, where string) string {
	return `select count(*)
		from (
			select o.id
			from bp_payment_order o` + paymentOrderFilterLookupJoins(params) + where + `
			limit ?
		) counted`
}

func paymentOrderListQuery(params ListParams, where string) string {
	orderBy := paymentOrderOrderBy(params)
	return `
		select o.id,o.order_no,o.user_id,o.order_type,o.product,coalesce(o.pay_channel,'ALIPAY'),coalesce(o.currency,'CNY'),coalesce(cast(o.origin_amount as char),''),o.subject,coalesce(o.target_id,0),cast(o.pay_amount as char),cast(o.gift_amount as char),o.grant_points,o.status,
		coalesce(o.alipay_trade_no,''),coalesce(o.buyer_id,''),coalesce(o.qr_code,''),date_format(o.expire_at,'%Y-%m-%d %H:%i:%s'),
		coalesce(date_format(o.paid_at,'%Y-%m-%d %H:%i:%s'),''),date_format(o.created_at,'%Y-%m-%d %H:%i:%s'),
		coalesce(nullif(u.display_name,''),u.username,''),
		coalesce(o.trade_type,''),coalesce(o.prepay_id,''),coalesce(o.channel_trade_no,''),
		coalesce(o.chain_address,''),coalesce(cast(o.chain_amount as char),''),coalesce(o.chain_txid,''),coalesce(o.chain_confirm_count,0),
		greatest(timestampdiff(second, now(), o.expire_at),0)
		from (
			select o.id
			from bp_payment_order o` + paymentOrderFilterLookupJoins(params) + where + `
			order by ` + orderBy + ` limit ? offset ?
		) filtered
		inner join bp_payment_order o on o.id=filtered.id
		left join bp_user u on u.id=o.user_id
		order by ` + orderBy
}

// paymentOrderOrderBy 只拼接白名单字段与方向；未指定时保持历史默认 id desc。
func paymentOrderOrderBy(params ListParams) string {
	direction := "desc"
	if params.SortOrder == "asc" {
		direction = "asc"
	}
	switch params.SortBy {
	case "created_at":
		return "o.created_at " + direction + ",o.id desc"
	case "paid_at":
		return "o.paid_at is null,o.paid_at " + direction + ",o.id desc"
	case "pay_amount":
		return "o.pay_amount " + direction + ",o.id desc"
	case "status":
		return "case o.status when 'CREATED' then 0 when 'PAYING' then 1 when 'PAID' then 2 when 'EXPIRED' then 3 when 'CLOSED' then 4 when 'CANCELLED' then 5 else 6 end " + direction + ",o.id desc"
	default:
		return "o.id desc"
	}
}

func (r *Repository) InsertNotifyLog(ctx context.Context, values map[string]string, verified, processed bool, message string) error {
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
	`, nullString(values["out_trade_no"]), nullString(values["trade_no"]), nullString(values["notify_id"]), "ALIPAY_NOTIFY", boolInt(verified), boolInt(processed), nullString(message), string(payload))
	return err
}

func (r *Repository) SettlePaidOrder(ctx context.Context, orderNo string, trade AlipayTradeStatus) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var id, userID, targetID int64
	var orderType, status, payAmount, giftAmount, snapshot, storedTradeNo, storedBuyerID, storedChannel, currency, originAmount string
	var grantPoints int64
	var stockReserved int
	err = tx.QueryRowContext(ctx, `
		select id,user_id,order_type,coalesce(target_id,0),status,cast(pay_amount as char),cast(gift_amount as char),grant_points,
		       coalesce(snapshot,json_object()),coalesce(alipay_trade_no,''),coalesce(buyer_id,''),coalesce(pay_channel,'ALIPAY'),
		       coalesce(currency,'CNY'),coalesce(cast(origin_amount as char),''),
		       coalesce(stock_reserved,0)
		from bp_payment_order where order_no=? for update
	`, orderNo).Scan(&id, &userID, &orderType, &targetID, &status, &payAmount, &giftAmount, &grantPoints, &snapshot, &storedTradeNo, &storedBuyerID, &storedChannel, &currency, &originAmount, &stockReserved)
	if err != nil {
		return err
	}
	if err := requirePaymentChannel(storedChannel, ChannelAlipay); err != nil {
		return err
	}
	trade.TradeNo = strings.TrimSpace(trade.TradeNo)
	trade.BuyerID = strings.TrimSpace(trade.BuyerID)
	if trade.TradeNo == "" {
		return errors.New("alipay trade number is required for settlement")
	}
	if status == "PAID" {
		if storedTradeNo == "" || storedTradeNo != trade.TradeNo {
			return errors.New("paid order alipay trade number mismatch")
		}
		if storedBuyerID != "" && trade.BuyerID != "" && storedBuyerID != trade.BuyerID {
			return errors.New("paid order alipay buyer mismatch")
		}
		// The matching PAID row is already durable; release the read lock
		// via deferred rollback rather than requiring a redundant COMMIT.
		return nil
	}
	if status != "CREATED" && status != "PAYING" && status != "EXPIRED" && status != "CANCELLED" {
		return errors.New("payment order status cannot be settled")
	}
	if err := r.applySettlement(ctx, tx, id, userID, targetID, orderType, payAmount, giftAmount, grantPoints, snapshot, currency, originAmount); err != nil {
		return err
	}
	if (status == "EXPIRED" || status == "CANCELLED") && stockReserved == 2 {
		// 本地过期或用户取消时库存已释放；通道随后确认到账时补回占用，保证成交量可追溯。
		if err := reserveOrderStockOnLateSettlementTx(ctx, tx, orderType, targetID); err != nil {
			return err
		}
		stockReserved = 3
	} else if stockReserved == 1 {
		stockReserved = 3
	}
	result, err := tx.ExecContext(ctx, `
		update bp_payment_order
		set status='PAID',stock_reserved=?,alipay_trade_no=?,buyer_id=?,paid_at=now(),settled_at=now()
		where id=? and status in ('CREATED','PAYING','EXPIRED','CANCELLED')
	`, stockReserved, trade.TradeNo, nullString(trade.BuyerID), id)
	if err != nil {
		return err
	}
	if err := requireAffected(result); err != nil {
		return err
	}
	return r.commitPaidOrder(ctx, tx, id, trade.TradeNo, trade.BuyerID, ChannelAlipay)
}

func (r *Repository) applySettlement(ctx context.Context, tx *sql.Tx, orderID, userID, targetID int64, orderType, payAmount, giftAmount string, grantPoints int64, snapshotText, currency, originAmount string) error {
	switch orderType {
	case "RECHARGE":
		// 余额按人民币口径记账：USD 订单的 pay_amount 是换算后的美元金额，
		// 入账必须取 origin_amount，否则会把美元和人民币赠金直接相加。
		total := addMoney(rechargeCreditBase(currency, payAmount, originAmount), giftAmount)
		result, err := tx.ExecContext(ctx, `
			update bp_user
			set balance=balance+?
			where id=? and balance<=cast(? as decimal(18,4))-cast(? as decimal(18,4))
		`, total, userID, maxUserBalanceText, total)
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
		if err := insertWalletTx(ctx, tx, userID, "ALIPAY_RECHARGE", orderID, total, 0, "Alipay recharge"); err != nil {
			return err
		}
		// 充值赞助自动升级用户组：与余额入账同事务原子提交（见 sponsor_role.go）。
		return r.applySponsorRoleTx(ctx, tx, userID)
	case "GLOBAL_PACKAGE":
		snapshot, err := decodeSettlementSnapshot(orderType, snapshotText)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `insert into bp_user_package_global(user_id,package_id,package_name,daily_limit,total_limit,count_mode,qps_limit,start_at,end_at,status) values(?,?,?,?,?,?,?,now(),date_add(now(), interval ? day),1)`,
			userID, targetID, snapshot.Name, snapshot.DailyLimit, snapshot.TotalLimit, snapshot.CountMode, snapshot.QPSLimit, snapshot.ValidDays)
		if err != nil {
			return err
		}
		return requireAffected(result)
	case "INTERFACE_PACKAGE":
		snapshot, err := decodeSettlementSnapshot(orderType, snapshotText)
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `insert into bp_user_package_interface(user_id,interface_id,package_id,spec_id,package_name,spec_name,daily_limit,total_limit,count_mode,qps_limit,start_at,end_at,status) values(?,?,?,?,?,?,?,?,?,?,now(),date_add(now(), interval ? day),1)`,
			userID, snapshot.InterfaceID, snapshot.PackageID, targetID, snapshot.PackageName, snapshot.SpecName, snapshot.DailyLimit, snapshot.TotalLimit, snapshot.CountMode, snapshot.QPSLimit, snapshot.ValidDays)
		if err != nil {
			return err
		}
		return requireAffected(result)
	case "POINT_PACKAGE":
		points := grantPoints
		if points <= 0 {
			snapshot, err := decodeSettlementSnapshot(orderType, snapshotText)
			if err != nil {
				return err
			}
			points = snapshot.Points
		}
		if points <= 0 {
			return errors.New("payment point-package snapshot has invalid points")
		}
		if points > maxUserPointBalance {
			return ErrPointBalanceLimit
		}
		remainingCapacity := maxUserPointBalance - points
		result, err := tx.ExecContext(ctx, `
			update bp_user
			set points=points+?
			where id=? and points<=?
		`, points, userID, remainingCapacity)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected == 0 {
			return ErrPointBalanceLimit
		}
		return insertWalletTx(ctx, tx, userID, "ALIPAY_POINT_PACKAGE", orderID, "0.0000", points, "Alipay point package")
	default:
		return errors.New("unsupported payment order type")
	}
}

func insertWalletTx(ctx context.Context, tx *sql.Tx, userID int64, bizType string, bizID int64, amount string, points int64, remark string) error {
	result, err := tx.ExecContext(ctx, `
		insert into bp_wallet_transaction(user_id,biz_type,biz_id,amount,points,balance_after,points_after,remark)
		select id,?,?,?,?,balance,points,? from bp_user where id=?
	`, bizType, nullInt64(bizID), amount, points, remark, userID)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

type settlementSnapshot struct {
	DailyLimit  int64
	TotalLimit  int64
	CountMode   string
	InterfaceID int64
	Name        string
	PackageID   int64
	PackageName string
	Points      int64
	QPSLimit    int
	SpecName    string
	ValidDays   int
}

func decodeSettlementSnapshot(orderType, snapshotText string) (settlementSnapshot, error) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(snapshotText)))
	decoder.UseNumber()
	values := map[string]any{}
	if err := decoder.Decode(&values); err != nil {
		return settlementSnapshot{}, fmt.Errorf("decode payment settlement snapshot: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return settlementSnapshot{}, errors.New("payment settlement snapshot contains multiple JSON values")
		}
		return settlementSnapshot{}, fmt.Errorf("decode payment settlement snapshot tail: %w", err)
	}

	snapshot := settlementSnapshot{}
	var err error
	switch orderType {
	case "GLOBAL_PACKAGE":
		if snapshot.Name, err = requiredSnapshotString(values, "name"); err != nil {
			return settlementSnapshot{}, err
		}
		if snapshot.ValidDays, err = requiredSnapshotPositiveInt(values, "validDays"); err != nil {
			return settlementSnapshot{}, err
		}
		if snapshot.DailyLimit, err = requiredSnapshotNonNegativeInt64(values, "dailyLimit"); err != nil {
			return settlementSnapshot{}, err
		}
		if snapshot.QPSLimit, err = requiredSnapshotNonNegativeInt(values, "qpsLimit"); err != nil {
			return settlementSnapshot{}, err
		}
		// totalLimit/countMode 是 000125 之后新增的快照键：旧订单没有这两个键，
		// 按默认值（0 = 不限 / success）发放，保持旧订单行为不变。
		snapshot.TotalLimit, err = optionalSnapshotNonNegativeInt64(values, "totalLimit")
		if err != nil {
			return settlementSnapshot{}, err
		}
		snapshot.CountMode = optionalSnapshotCountMode(values)
	case "INTERFACE_PACKAGE":
		if snapshot.PackageID, err = requiredSnapshotPositiveInt64(values, "packageId"); err != nil {
			return settlementSnapshot{}, err
		}
		if snapshot.InterfaceID, err = requiredSnapshotPositiveInt64(values, "interfaceId"); err != nil {
			return settlementSnapshot{}, err
		}
		if snapshot.PackageName, err = requiredSnapshotString(values, "packageName"); err != nil {
			return settlementSnapshot{}, err
		}
		if snapshot.SpecName, err = requiredSnapshotString(values, "specName"); err != nil {
			return settlementSnapshot{}, err
		}
		if snapshot.ValidDays, err = requiredSnapshotPositiveInt(values, "validDays"); err != nil {
			return settlementSnapshot{}, err
		}
		if snapshot.DailyLimit, err = requiredSnapshotNonNegativeInt64(values, "dailyLimit"); err != nil {
			return settlementSnapshot{}, err
		}
		if snapshot.QPSLimit, err = requiredSnapshotNonNegativeInt(values, "qpsLimit"); err != nil {
			return settlementSnapshot{}, err
		}
		snapshot.TotalLimit, err = optionalSnapshotNonNegativeInt64(values, "totalLimit")
		if err != nil {
			return settlementSnapshot{}, err
		}
		snapshot.CountMode = optionalSnapshotCountMode(values)
	case "POINT_PACKAGE":
		if snapshot.Points, err = requiredSnapshotPositiveInt64(values, "points"); err != nil {
			return settlementSnapshot{}, err
		}
	default:
		return settlementSnapshot{}, fmt.Errorf("unsupported payment settlement snapshot type %q", orderType)
	}
	return snapshot, nil
}

func requiredSnapshotString(values map[string]any, key string) (string, error) {
	value := strings.TrimSpace(fmt.Sprintf("%v", values[key]))
	if value == "" || value == "<nil>" {
		return "", fmt.Errorf("payment settlement snapshot field %s is required", key)
	}
	return value, nil
}

func requiredSnapshotPositiveInt(values map[string]any, key string) (int, error) {
	value, err := requiredSnapshotPositiveInt64(values, key)
	if err != nil {
		return 0, err
	}
	if value > int64(^uint(0)>>1) {
		return 0, fmt.Errorf("payment settlement snapshot field %s is too large", key)
	}
	return int(value), nil
}

func requiredSnapshotNonNegativeInt(values map[string]any, key string) (int, error) {
	value, err := requiredSnapshotNonNegativeInt64(values, key)
	if err != nil {
		return 0, err
	}
	if value > int64(^uint(0)>>1) {
		return 0, fmt.Errorf("payment settlement snapshot field %s is too large", key)
	}
	return int(value), nil
}

func requiredSnapshotPositiveInt64(values map[string]any, key string) (int64, error) {
	value, err := snapshotInt64(values, key)
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		return 0, fmt.Errorf("payment settlement snapshot field %s must be positive", key)
	}
	return value, nil
}

func requiredSnapshotNonNegativeInt64(values map[string]any, key string) (int64, error) {
	value, err := snapshotInt64(values, key)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, fmt.Errorf("payment settlement snapshot field %s must not be negative", key)
	}
	return value, nil
}

func snapshotInt64(values map[string]any, key string) (int64, error) {
	raw, ok := values[key]
	if !ok {
		return 0, fmt.Errorf("payment settlement snapshot field %s is required", key)
	}
	text := strings.TrimSpace(fmt.Sprintf("%v", raw))
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("payment settlement snapshot field %s is invalid: %w", key, err)
	}
	return value, nil
}

// optionalSnapshotNonNegativeInt64 读取可选的非负整型快照键：键缺失（旧订单）
// 返回 0，键存在但为负数才报错。
func optionalSnapshotNonNegativeInt64(values map[string]any, key string) (int64, error) {
	raw, ok := values[key]
	if !ok || raw == nil {
		return 0, nil
	}
	text := strings.TrimSpace(fmt.Sprintf("%v", raw))
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("payment settlement snapshot field %s is invalid: %w", key, err)
	}
	if value < 0 {
		return 0, fmt.Errorf("payment settlement snapshot field %s must not be negative", key)
	}
	return value, nil
}

// optionalSnapshotCountMode 读取可选的计量方式快照键：缺失或非法值一律落回
// success，保证旧订单与坏快照都按「失败不计量」的既有行为发放。
func optionalSnapshotCountMode(values map[string]any) string {
	if strings.TrimSpace(strings.ToLower(fmt.Sprintf("%v", values["countMode"]))) == "attempt" {
		return "attempt"
	}
	return "success"
}

func requireAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func normalizePage(page, size int) (int, int) {
	return pagination.Normalize(page, size, 100)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func nullInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func addMoney(a, b string) string {
	left, _ := new(big.Rat).SetString(strings.TrimSpace(a))
	right, _ := new(big.Rat).SetString(strings.TrimSpace(b))
	if left == nil {
		left = new(big.Rat)
	}
	if right == nil {
		right = new(big.Rat)
	}
	sum := new(big.Rat).Add(left, right)
	return sum.FloatString(4)
}
