package packagepkg

import (
	"testing"
)

// 「不限量·月付」的核心语义完全由 daily_limit=0 / total_limit=0 承载。
// 这里钉死这些字段组合的合法性，防止后续有人「优化」成非零值而悄悄破坏不限量。
//
// 线上已按此组合落库（bp_package_global id=8），本测试是该配置的可执行规格。

func validUnlimited() SaveGlobalPackageRequest {
	return SaveGlobalPackageRequest{
		Name:         "不限量·月付",
		Price:        "200",
		ValidDays:    30,
		DailyLimit:   0, // 0 = 不限量（gateway SQL: daily_limit=0 or day_used<daily_limit）
		TotalLimit:   0, // 0 = 生命周期总量不限
		CountMode:    "success",
		QPSLimit:     30,
		StockLimit:   10, // 限量发售是成本闸门
		PerUserLimit: 1,
		Remark:       "每日调用不限量，30 QPS，每月限 10 名",
	}
}

// 线上那一行必须能通过校验；被拒绝就说明配置无法复现。
func TestUnlimitedPackageFieldCombinationIsValid(t *testing.T) {
	if _, err := normalizeGlobal(validUnlimited()); err != nil {
		t.Fatalf("unlimited package must be accepted: %v", err)
	}
}

// 0 必须合法：这是不限量的唯一表达方式，被当成「空值/非法」是最容易犯的错误。
// 同时负数必须与 0 区分开——0 是有意义的「不限」，负数才是错误输入。
func TestDailyLimitZeroIsAcceptedAndNegativeRejected(t *testing.T) {
	zero := validUnlimited()
	zero.DailyLimit = 0
	got, err := normalizeGlobal(zero)
	if err != nil {
		t.Fatalf("daily_limit=0 must be legal: %v", err)
	}
	if got.DailyLimit != 0 || got.TotalLimit != 0 {
		t.Fatalf("zero limits must survive normalization: daily=%d total=%d", got.DailyLimit, got.TotalLimit)
	}

	negative := validUnlimited()
	negative.DailyLimit = -1
	if _, err := normalizeGlobal(negative); err == nil {
		t.Fatal("negative daily_limit must be rejected")
	}
}

// 限量发售是不限量档的成本闸门，必须真的生效且不得被一个大数绕过。
func TestUnlimitedPackageStockGateIsEnforced(t *testing.T) {
	got, err := normalizeGlobal(validUnlimited())
	if err != nil {
		t.Fatalf("stock_limit=10 must be accepted: %v", err)
	}
	if got.StockLimit != 10 {
		t.Fatalf("stock limit lost: %d", got.StockLimit)
	}
	tooBig := validUnlimited()
	tooBig.StockLimit = maxPackageStock + 1
	if _, err := normalizeGlobal(tooBig); err == nil {
		t.Fatal("oversized stock_limit must be rejected")
	}
}

// QPS 是不限量档唯一的物理约束。系统允许 qps_limit=0（= 不限并发），
// 但对不限量档这是危险的组合：日不限 + 并发不限 = 完全敞口。
// 这里记录真实语义，并守住「负值非法」这条边界。
func TestUnlimitedPackageQPSBoundaries(t *testing.T) {
	// 0 合法但业务上不应使用：日不限 + 并发不限等于没有闸门。
	zero := validUnlimited()
	zero.QPSLimit = 0
	if _, err := normalizeGlobal(zero); err != nil {
		t.Fatalf("qps_limit=0 is legal by design (unbounded): %v", err)
	}
	// 负数非法。
	negative := validUnlimited()
	negative.QPSLimit = -1
	if _, err := normalizeGlobal(negative); err == nil {
		t.Fatal("negative qps_limit must be rejected")
	}
	// 超过上限非法，防止误填一个大数把并发变成无限制。
	tooBig := validUnlimited()
	tooBig.QPSLimit = maxPackageQPS + 1
	if _, err := normalizeGlobal(tooBig); err == nil {
		t.Fatal("oversized qps_limit must be rejected")
	}
}

// count_mode=success 对不限量档尤其重要：上游失败不扣额度，否则抖动会白吃用户额度。
func TestUnlimitedPackageCountModeIsStrict(t *testing.T) {
	got, err := normalizeGlobal(validUnlimited())
	if err != nil {
		t.Fatalf("count_mode=success must be accepted: %v", err)
	}
	if got.CountMode != "success" {
		t.Fatalf("count_mode lost: %q", got.CountMode)
	}
	bad := validUnlimited()
	bad.CountMode = "whatever"
	if _, err := normalizeGlobal(bad); err == nil {
		t.Fatal("unknown count_mode must be rejected")
	}
}
