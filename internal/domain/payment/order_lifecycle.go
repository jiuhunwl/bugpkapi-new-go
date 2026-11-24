package payment

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// 订单有效期按支付通道区分：虎皮椒官方订单只有 10 分钟有效，
// 支付宝/微信保留项目既有的 30 分钟窗口。过期订单不再允许继续支付，
// 也不会出现在「相同商品待支付订单」提示中。
const (
	defaultOrderExpireMinutes  = 30
	hupijiaoOrderExpireMinutes = 10
)

var (
	ErrPaymentOrderPending     = errors.New("pending payment order exists for the same product")
	ErrPaymentOrderExpired     = errors.New("payment order expired")
	ErrPaymentOrderNotReusable = errors.New("payment order is not reusable")
	ErrPackageSoldOut          = errors.New("package stock is exhausted")
	ErrPackageOffSale          = errors.New("package is not on sale")
	ErrPackageUserLimit        = errors.New("package per-user purchase limit reached")
	ErrPackageRoleRestricted   = errors.New("package is restricted to other user groups")
	ErrPackageStockBelowUsed   = errors.New("package stock limit is below reserved count")
)

func orderExpireMinutes(channel string) int {
	switch strings.ToUpper(strings.TrimSpace(channel)) {
	case ChannelHupijiaoAlipay, ChannelHupijiaoWechat:
		return hupijiaoOrderExpireMinutes
	case ChannelTrc20:
		return trc20ExpireMinutes
	case ChannelBep20:
		return bep20ExpireMinutes
	default:
		return defaultOrderExpireMinutes
	}
}

func isPackageOrderType(orderType string) bool {
	switch orderType {
	case "GLOBAL_PACKAGE", "INTERFACE_PACKAGE", "POINT_PACKAGE":
		return true
	default:
		return false
	}
}

func packageStockTable(orderType string) string {
	switch orderType {
	case "GLOBAL_PACKAGE":
		return "bp_package_global"
	case "INTERFACE_PACKAGE":
		return "bp_package_interface_spec"
	case "POINT_PACKAGE":
		return "bp_package_point"
	default:
		return ""
	}
}

// validateOrderTargetTx 在创建订单前锁定商品行，校验可售状态、售卖时间、
// 库存与每人限购，并在限量套餐上原子预占一份库存。
// 返回的 bool 表示该订单是否持有库存占用。
func validateOrderTargetTx(ctx context.Context, tx *sql.Tx, seed *OrderSeed) (bool, error) {
	switch seed.OrderType {
	case "RECHARGE":
		var lockedID int64
		return false, tx.QueryRowContext(ctx, `
			select id from bp_recharge_preset
			where id=? and status=1
			for update
		`, seed.TargetID).Scan(&lockedID)
	case "GLOBAL_PACKAGE":
		return reservePackageStockTx(ctx, tx, seed, "GLOBAL_PACKAGE", "GLOBAL", `
			select id,status,stock_limit,stock_used,per_user_limit,sale_start_at,sale_end_at,role_restricted
			from bp_package_global where id=? for update
		`, seed.TargetID)
	case "INTERFACE_PACKAGE":
		return reservePackageStockTx(ctx, tx, seed, "INTERFACE_PACKAGE", "INTERFACE", `
			select p.id, s.status, s.stock_limit, s.stock_used, s.per_user_limit, s.sale_start_at, s.sale_end_at, p.role_restricted
			from bp_package_interface_spec s
			inner join bp_package_interface p on p.id=s.package_id
			inner join bp_interface_api a on a.id=p.interface_id
			where s.id=? and p.status=1 and a.status=1
			for update
		`, seed.TargetID)
	case "POINT_PACKAGE":
		return reservePackageStockTx(ctx, tx, seed, "POINT_PACKAGE", "POINT", `
			select id,status,stock_limit,stock_used,per_user_limit,sale_start_at,sale_end_at,role_restricted
			from bp_package_point where id=? for update
		`, seed.TargetID)
	default:
		return false, nil
	}
}

func reservePackageStockTx(ctx context.Context, tx *sql.Tx, seed *OrderSeed, orderType, rolePackageType, query string, targetID int64) (bool, error) {
	var rolePackageID int64
	var status, stockLimit, stockUsed, perUserLimit, roleRestricted int
	var saleStart, saleEnd sql.NullTime
	if err := tx.QueryRowContext(ctx, query, targetID).Scan(&rolePackageID, &status, &stockLimit, &stockUsed, &perUserLimit, &saleStart, &saleEnd, &roleRestricted); err != nil {
		return false, err
	}
	if status != 1 {
		return false, sql.ErrNoRows
	}
	if err := ensurePackageRoleAllowedTx(ctx, tx, seed.UserID, rolePackageType, rolePackageID, roleRestricted); err != nil {
		return false, err
	}
	now := time.Now()
	if saleStart.Valid && now.Before(saleStart.Time) {
		return false, ErrPackageOffSale
	}
	if saleEnd.Valid && !now.Before(saleEnd.Time) {
		return false, ErrPackageOffSale
	}
	if stockLimit > 0 && int64(stockUsed) >= int64(stockLimit) {
		return false, ErrPackageSoldOut
	}
	if perUserLimit > 0 {
		count, err := userPackagePurchaseCountTx(ctx, tx, seed.UserID, orderType, targetID)
		if err != nil {
			return false, err
		}
		if count >= int64(perUserLimit) {
			return false, ErrPackageUserLimit
		}
	}
	if stockLimit <= 0 {
		return false, nil
	}
	table := packageStockTable(orderType)
	result, err := tx.ExecContext(ctx, "update "+table+" set stock_used=stock_used+1 where id=? and stock_used<?", targetID, stockLimit)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected == 0 {
		return false, ErrPackageSoldOut
	}
	return true, nil
}

// ensurePackageRoleAllowedTx 校验用户是否属于套餐允许的用户组；
// 用户组取 bp_user_role 与 primary_role_id 的并集，与套餐商城口径一致。
func ensurePackageRoleAllowedTx(ctx context.Context, tx *sql.Tx, userID int64, packageType string, packageID int64, roleRestricted int) error {
	if roleRestricted == 0 {
		return nil
	}
	var allowed bool
	if err := tx.QueryRowContext(ctx, `
		select exists(
			select 1 from bp_package_role pr
			where pr.package_type=? and pr.package_id=?
			  and (
			    pr.role_id in (select role_id from bp_user_role where user_id=?)
			    or pr.role_id = coalesce((select primary_role_id from bp_user where id=?), 0)
			  )
		)
	`, packageType, packageID, userID, userID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrPackageRoleRestricted
	}
	return nil
}

// userPackagePurchaseCountTx 统计用户对同一商品的已购与待支付数量。
// 全站/接口套餐的实际购买会落成权益行，因此已支付订单不重复计数；
// 点数套餐没有权益行，改为统计已支付订单与余额购买流水。
func userPackagePurchaseCountTx(ctx context.Context, tx *sql.Tx, userID int64, orderType string, targetID int64) (int64, error) {
	var pending int64
	if err := tx.QueryRowContext(ctx, `
		select count(*)
		from bp_payment_order
		where user_id=? and order_type=? and target_id=?
		  and status in ('CREATED','PAYING') and expire_at > now()
	`, userID, orderType, targetID).Scan(&pending); err != nil {
		return 0, err
	}
	switch orderType {
	case "GLOBAL_PACKAGE":
		var bought int64
		if err := tx.QueryRowContext(ctx, `
			select count(*) from bp_user_package_global where user_id=? and package_id=?
		`, userID, targetID).Scan(&bought); err != nil {
			return 0, err
		}
		return bought + pending, nil
	case "INTERFACE_PACKAGE":
		var bought int64
		if err := tx.QueryRowContext(ctx, `
			select count(*) from bp_user_package_interface where user_id=? and spec_id=?
		`, userID, targetID).Scan(&bought); err != nil {
			return 0, err
		}
		return bought + pending, nil
	case "POINT_PACKAGE":
		var paid, balance int64
		if err := tx.QueryRowContext(ctx, `
			select count(*) from bp_payment_order
			where user_id=? and order_type='POINT_PACKAGE' and target_id=? and status='PAID'
		`, userID, targetID).Scan(&paid); err != nil {
			return 0, err
		}
		if err := tx.QueryRowContext(ctx, `
			select count(*) from bp_wallet_transaction
			where user_id=? and biz_type='PURCHASE_POINT_PACKAGE' and biz_id=?
		`, userID, targetID).Scan(&balance); err != nil {
			return 0, err
		}
		return paid + pending + balance, nil
	default:
		return pending, nil
	}
}

func releaseOrderStockTx(ctx context.Context, tx *sql.Tx, orderType string, targetID int64) error {
	table := packageStockTable(orderType)
	if table == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, "update "+table+" set stock_used=greatest(stock_used-1,0) where id=?", targetID)
	return err
}

func reserveOrderStockOnLateSettlementTx(ctx context.Context, tx *sql.Tx, orderType string, targetID int64) error {
	table := packageStockTable(orderType)
	if table == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, "update "+table+" set stock_used=stock_used+1 where id=?", targetID)
	return err
}
