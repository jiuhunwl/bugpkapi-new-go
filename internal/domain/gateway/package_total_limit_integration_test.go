package gateway

import (
	"context"
	"testing"
)

// 套餐总调用量 + 计量方式（000125）的仓储层回归：
//  1. total_limit 打满后权益不再被选中，扣减回落到余额；
//  2. total_limit 未打满时正常消耗，total_used 与 day_used 同事务递增；
//  3. count_mode='attempt' 时失败释放不回退 day_used/total_used；
//  4. count_mode='success'（默认）保持既有行为：失败释放回退计数。
func TestPackageTotalLimitAndCountMode(t *testing.T) {
	db, _, api := billingOutcomeDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	insertEntitlement := func(t *testing.T, daily, total, dayUsed, totalUsed int64, countMode string) int64 {
		t.Helper()
		result, err := db.Exec(`insert into bp_user_package_global
			(user_id,package_id,package_name,daily_limit,total_limit,count_mode,qps_limit,day_key,day_used,total_used,start_at,end_at,status)
			values(990010,700,'TotalTest',?,?,?,?,curdate(),?,?,date_sub(now(),interval 1 day),date_add(now(),interval 7 day),1)`,
			daily, total, countMode, 4, dayUsed, totalUsed)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	entitlementState := func(t *testing.T, id int64) (int64, int64) {
		t.Helper()
		var dayUsed, totalUsed int64
		if err := db.QueryRow("select day_used,total_used from bp_user_package_global where id=?", id).Scan(&dayUsed, &totalUsed); err != nil {
			t.Fatal(err)
		}
		return dayUsed, totalUsed
	}

	t.Run("exhausted total falls through to balance", func(t *testing.T) {
		id := insertEntitlement(t, 0, 2, 0, 2, "success")
		defer func() { _, _ = db.Exec("delete from bp_user_package_global where id=?", id) }()
		res, err := repo.ReserveBilling(ctx, api, 0, 990010)
		if err != nil {
			t.Fatal(err)
		}
		if res.ChargeType != "BALANCE" {
			t.Fatalf("total-limit exhausted package must be skipped, got charge type %s", res.ChargeType)
		}
		if _, totalUsed := entitlementState(t, id); totalUsed != 2 {
			t.Fatalf("exhausted package must not be consumed, total_used=%d", totalUsed)
		}
		if err := repo.ReleaseBilling(ctx, res, 990010, "test cleanup"); err != nil {
			t.Fatal(err)
		}
		if billingBalance(t, db) != "10.0000" {
			t.Fatalf("balance refund failed: %s", billingBalance(t, db))
		}
	})

	t.Run("total is consumed until cap", func(t *testing.T) {
		id := insertEntitlement(t, 0, 2, 0, 1, "success")
		defer func() { _, _ = db.Exec("delete from bp_user_package_global where id=?", id) }()
		res, err := repo.ReserveBilling(ctx, api, 0, 990010)
		if err != nil {
			t.Fatal(err)
		}
		if res.ChargeType != "GLOBAL_PACKAGE" {
			t.Fatalf("package with remaining total must be consumed, got %s", res.ChargeType)
		}
		dayUsed, totalUsed := entitlementState(t, id)
		if dayUsed != 1 || totalUsed != 2 {
			t.Fatalf("counters must advance together: day_used=%d total_used=%d", dayUsed, totalUsed)
		}
		if err := repo.ConfirmBilling(ctx, res, 0); err != nil {
			t.Fatal(err)
		}
		// 打满后的下一次调用必须回落余额。
		res2, err := repo.ReserveBilling(ctx, api, 0, 990010)
		if err != nil {
			t.Fatal(err)
		}
		if res2.ChargeType != "BALANCE" {
			t.Fatalf("cap-reached package must fall through to balance, got %s", res2.ChargeType)
		}
		if err := repo.ReleaseBilling(ctx, res2, 990010, "test cleanup"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("attempt mode keeps counters on release", func(t *testing.T) {
		id := insertEntitlement(t, 10, 0, 1, 1, "attempt")
		defer func() { _, _ = db.Exec("delete from bp_user_package_global where id=?", id) }()
		res, err := repo.ReserveBilling(ctx, api, 0, 990010)
		if err != nil {
			t.Fatal(err)
		}
		if res.ChargeType != "GLOBAL_PACKAGE" {
			t.Fatalf("expected package reservation, got %s", res.ChargeType)
		}
		if err := repo.ReleaseBilling(ctx, res, 990010, "upstream failed"); err != nil {
			t.Fatal(err)
		}
		dayUsed, totalUsed := entitlementState(t, id)
		// attempt = 失败也计量：释放只改计费记录状态，计数不回退。
		if dayUsed != 2 || totalUsed != 2 {
			t.Fatalf("attempt mode must keep counters: day_used=%d total_used=%d", dayUsed, totalUsed)
		}
		var status string
		if err := db.QueryRow("select status from bp_billing_record where id=?", res.BillingRecordID).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != "RELEASED" {
			t.Fatalf("billing record must still be released, got %s", status)
		}
	})

	t.Run("success mode refunds counters on release", func(t *testing.T) {
		id := insertEntitlement(t, 10, 0, 1, 1, "success")
		defer func() { _, _ = db.Exec("delete from bp_user_package_global where id=?", id) }()
		res, err := repo.ReserveBilling(ctx, api, 0, 990010)
		if err != nil {
			t.Fatal(err)
		}
		if err := repo.ReleaseBilling(ctx, res, 990010, "upstream failed"); err != nil {
			t.Fatal(err)
		}
		dayUsed, totalUsed := entitlementState(t, id)
		if dayUsed != 1 || totalUsed != 1 {
			t.Fatalf("success mode must refund counters: day_used=%d total_used=%d", dayUsed, totalUsed)
		}
	})
}
