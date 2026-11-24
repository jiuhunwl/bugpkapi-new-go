package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHupijiaoMigrationContract(t *testing.T) {
	repoRoot := sqlArtifactRoot()
	contents, err := os.ReadFile(filepath.Join(repoRoot, "migrations", "000086_hupijiao_payment.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(contents))
	for _, required := range []string{
		"create table if not exists `bp_payment_hupijiao_config`",
		"('alipay', 0), ('wechat', 0)",
		"create table if not exists `bp_payment_method`",
		"add column `pay_channel`",
		"idx_bp_payment_order_channel_status",
		"payment:hupijiao:update",
		"payment:method:update",
		"role_key` = 'admin'",
		"@bugpk_payment_order_backfill_max_id",
		"and `pay_channel` = 'alipay'",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration 86 missing %q", required)
		}
	}
}

func TestPollingStopCodeMigrationReassertsCallLogURLCapacity(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join(sqlArtifactRoot(), "migrations", "000087_polling_stop_code.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.ToLower(string(contents))
	for _, required := range []string{
		"add column `polling_stop_code`",
		"modify column `upstream_url` text",
	} {
		if !strings.Contains(sql, required) {
			t.Fatalf("migration 87 missing %q", required)
		}
	}
}

func TestFullSQLIncludesPaymentAndQPSSchema(t *testing.T) {
	// 版本与分发产物新鲜度检查统一在 TestSQLArtifactsMatchCurrentMigrations 中完成。
	sql := readDistributionSQL(t, "full")
	assertContainsAll(t, "full SQL", sql, []string{
		"bp_payment_hupijiao_config",
		"bp_payment_method",
		"pay_channel",
		"idx_bp_payment_order_channel_status",
		"payment:hupijiao:update",
		"payment:method:update",
		"polling_stop_code",
		"global_loading_preset",
		"shared_qps_limit",
		"user_qps_limit",
	})
}
