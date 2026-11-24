package packagepkg

// 端到端验收脚本（手工跑）：在隔离库上走一遍「首购 → 续费 → 过期 → 再购」，
// 把每一步的真实 SQL 结果打印出来，用于人工核对规划文档 §4 的预期输出。
//
// 用法：
//   BUGPK_PACKAGE_INTEGRATION_DSN=... go test ./internal/domain/packagepkg/ -run TestRenewalWalkthrough -v

import (
	"fmt"
	"testing"
)

func TestRenewalWalkthrough(t *testing.T) {
	env := newRenewalTestEnv(t, 30, 1)
	repo := NewRepository(env.db)

	dump := func(step string) {
		rows, err := env.db.QueryContext(env.ctx, `
			select id, start_at, end_at, day_used,
			       timestampdiff(day, start_at, end_at) as span_days
			from bp_user_package_global
			where user_id=? and package_id=? order by id
		`, env.userID, env.packageID)
		if err != nil {
			t.Fatalf("dump %s: %v", step, err)
		}
		defer rows.Close()
		fmt.Printf("\n--- %s ---\n", step)
		fmt.Printf("%-6s %-20s %-20s %-8s %-10s\n", "id", "start_at", "end_at", "day_used", "span_days")
		for rows.Next() {
			var id, dayUsed, span int64
			var start, end string
			if err := rows.Scan(&id, &start, &end, &dayUsed, &span); err != nil {
				t.Fatalf("scan %s: %v", step, err)
			}
			fmt.Printf("%-6d %-20s %-20s %-8d %-10d\n", id, start, end, dayUsed, span)
		}

		var logs int
		var addSum int64
		_ = env.db.QueryRowContext(env.ctx,
			`select count(*), coalesce(sum(add_days),0) from bp_package_renewal_log where user_id=?`,
			env.userID).Scan(&logs, &addSum)
		fmt.Printf("renewal_log: %d 条, 累计顺延 %d 天\n", logs, addSum)
	}

	dump("0. 初始（无权益）")

	if err := repo.PurchaseGlobal(env.ctx, env.userID, env.packageID, fmt.Sprintf("walk-1-%d", env.suffix)); err != nil {
		t.Fatalf("首购失败: %v", err)
	}
	dump("1. 首次购买 30 天")

	if _, err := env.db.ExecContext(env.ctx,
		`update bp_user_package_global set day_key=curdate(), day_used=1200 where user_id=? and package_id=?`,
		env.userID, env.packageID); err != nil {
		t.Fatalf("设置当日用量: %v", err)
	}
	dump("2. 模拟当日已用 1200 次")

	if err := repo.PurchaseGlobal(env.ctx, env.userID, env.packageID, fmt.Sprintf("walk-2-%d", env.suffix)); err != nil {
		t.Fatalf("续费失败: %v", err)
	}
	dump("3. 有效期内续费 30 天（应：同一条 id，span 变 60，day_used 保持 1200）")

	if _, err := env.db.ExecContext(env.ctx,
		`update bp_user_package_global set end_at=date_sub(now(), interval 5 day) where user_id=? and package_id=?`,
		env.userID, env.packageID); err != nil {
		t.Fatalf("模拟过期: %v", err)
	}
	if err := repo.PurchaseGlobal(env.ctx, env.userID, env.packageID, fmt.Sprintf("walk-3-%d", env.suffix)); err != nil {
		t.Fatalf("过期后购买失败: %v", err)
	}
	dump("4. 过期 5 天后重新购买（应：新增一条 id，span=30，不从过期日倒挂）")

	items, err := repo.UserPackages(env.ctx, env.userID)
	if err != nil {
		t.Fatalf("读取用户套餐: %v", err)
	}
	fmt.Printf("\n--- 5. 前端看到的「我的套餐」 ---\n")
	for _, item := range items {
		fmt.Printf("%-22s end=%-20s 剩余=%d 天 可续费=%v 主套餐=%v\n",
			item.Name, item.EndAt, item.RemainDays, item.Renewable, item.IsPrimary)
	}
}
