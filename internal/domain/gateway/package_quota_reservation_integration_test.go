package gateway

import (
	"context"
	"testing"
)

// 零价接口免费额度用尽后，套餐额度必须真实扣减并可释放：
//  1. 预占成功返回 GLOBAL_PACKAGE 与套餐 QPS（覆盖用户组 QPS）；
//  2. 失败释放按 count_mode=success 回退 day_used/total_used；
//  3. 日额度打满后不再预占。
func TestReservePackageQuotaConsumesAndReleases(t *testing.T) {
	db, _, api := billingOutcomeDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	result, err := db.Exec(`
		insert into bp_user_package_global
		(user_id,package_id,package_name,daily_limit,total_limit,count_mode,qps_limit,start_at,end_at,status)
		values(990010,990010,'Quota Test',2,0,'success',50,date_sub(now(),interval 1 day),date_add(now(),interval 7 day),1)
	`)
	if err != nil {
		t.Fatal(err)
	}
	entitlementID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_billing_record where user_id=990010")
		_, _ = db.Exec("delete from bp_user_package_global where id=?", entitlementID)
	})

	usage := func() (int64, int64) {
		t.Helper()
		var dayUsed, totalUsed int64
		if err := db.QueryRow("select day_used,total_used from bp_user_package_global where id=?", entitlementID).Scan(&dayUsed, &totalUsed); err != nil {
			t.Fatal(err)
		}
		return dayUsed, totalUsed
	}

	res, ok, err := repo.ReservePackageQuota(ctx, api, 0, 990010)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || res.ChargeType != "GLOBAL_PACKAGE" || res.BillingRecordID == 0 {
		t.Fatalf("expected global package reservation, got ok=%v res=%+v", ok, res)
	}
	if res.QPSLimit != 50 {
		t.Fatalf("package QPS must override role QPS, got %d", res.QPSLimit)
	}
	if dayUsed, totalUsed := usage(); dayUsed != 1 || totalUsed != 1 {
		t.Fatalf("package counters must advance: day=%d total=%d", dayUsed, totalUsed)
	}

	if err := repo.ReleaseBilling(ctx, res, 990010, "test release"); err != nil {
		t.Fatal(err)
	}
	if dayUsed, totalUsed := usage(); dayUsed != 0 || totalUsed != 0 {
		t.Fatalf("success-mode release must refund counters: day=%d total=%d", dayUsed, totalUsed)
	}

	for i := 0; i < 2; i++ {
		res, ok, err := repo.ReservePackageQuota(ctx, api, 0, 990010)
		if err != nil || !ok {
			t.Fatalf("reservation %d should succeed: ok=%v err=%v", i+1, ok, err)
		}
		if err := repo.ConfirmBilling(ctx, res, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, err := repo.ReservePackageQuota(ctx, api, 0, 990010); err != nil || ok {
		t.Fatalf("exhausted daily quota must not reserve: ok=%v err=%v", ok, err)
	}
}

// 主套餐优先于更早到期的套餐；取消主套餐后按到期时间最早优先。
func TestReservePackageQuotaPrefersPrimaryThenEarliestExpiry(t *testing.T) {
	db, _, api := billingOutcomeDB(t)
	repo := NewRepository(db)
	ctx := context.Background()

	insert := func(t *testing.T, primary int, validDays, qps int64) int64 {
		t.Helper()
		result, err := db.Exec(`
			insert into bp_user_package_global
			(user_id,package_id,package_name,daily_limit,total_limit,count_mode,qps_limit,is_primary,start_at,end_at,status)
			values(990010,990012,'Order Test',10,0,'success',?,?,date_sub(now(),interval 1 day),date_add(now(),interval ? day),1)
		`, qps, primary, validDays)
		if err != nil {
			t.Fatal(err)
		}
		id, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	primaryID := insert(t, 1, 30, 40)
	earlyID := insert(t, 0, 1, 30)
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_billing_record where user_id=990010")
		_, _ = db.Exec("delete from bp_user_package_global where id in (?,?)", primaryID, earlyID)
	})

	res, ok, err := repo.ReservePackageQuota(ctx, api, 0, 990010)
	if err != nil || !ok {
		t.Fatalf("primary package should reserve: ok=%v err=%v", ok, err)
	}
	if res.PackageRecordID != primaryID || res.QPSLimit != 40 {
		t.Fatalf("primary package must win over earlier expiry: %+v", res)
	}
	if err := repo.ReleaseBilling(ctx, res, 990010, "test release"); err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec("update bp_user_package_global set is_primary=0 where id=?", primaryID); err != nil {
		t.Fatal(err)
	}
	res, ok, err = repo.ReservePackageQuota(ctx, api, 0, 990010)
	if err != nil || !ok {
		t.Fatalf("earliest package should reserve after primary cleared: ok=%v err=%v", ok, err)
	}
	if res.PackageRecordID != earlyID || res.QPSLimit != 30 {
		t.Fatalf("earliest expiry must win without primary: %+v", res)
	}
	if err := repo.ConfirmBilling(ctx, res, 0); err != nil {
		t.Fatal(err)
	}
}
