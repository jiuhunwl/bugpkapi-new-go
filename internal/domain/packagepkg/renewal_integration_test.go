package packagepkg

// 续费顺延的集成测试。
//
// 需要设置 BUGPK_PACKAGE_INTEGRATION_DSN 指向一个隔离的 MySQL 库；
// 未配置时整组跳过（与同目录其余 *_integration_test.go 的约定一致）。
//
// 覆盖规划文档 §4 的验证用例：
//   1. 首次购买 → 一条记录，end_at ≈ now+30d
//   2. 有效期内再买 → 同一条 id，end_at ≈ now+60d，流水 +1
//   3. 过期后再买 → 新建一条，不倒挂到过期日
//   4. 不同幂等键重复提交 → 天数各加一次，流水逐条可查
//   5. renewal_enabled=0 的套餐 → 每次新建，不顺延
//   6. 接口套餐规格同样顺延，且按 spec_id 匹配

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

// renewalTestEnv 是这组测试共用的夹具：一个充值充足的用户 + 一个全站套餐。
type renewalTestEnv struct {
	db        *sql.DB
	ctx       context.Context
	userID    int64
	packageID int64
	suffix    int64
}

func newRenewalTestEnv(t *testing.T, validDays int, renewalEnabled int) *renewalTestEnv {
	t.Helper()
	dsn := os.Getenv("BUGPK_PACKAGE_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_PACKAGE_INTEGRATION_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open integration database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	suffix := time.Now().UnixNano()
	username := "renewal_" + itoa(suffix)
	// 余额给足：本组测试关心的是天数累加，不是扣款是否成功。
	userResult, err := db.ExecContext(ctx, `
		insert into bp_user(username,password_hash,display_name,balance,points,status)
		values(?, '$2a$10$integration.test.hash.placeholder', ?, 1000.0000, 0, 1)
	`, username, username)
	if err != nil {
		t.Fatalf("insert test user: %v", err)
	}
	userID, err := userResult.LastInsertId()
	if err != nil {
		t.Fatalf("test user id: %v", err)
	}

	packageResult, err := db.ExecContext(ctx, `
		insert into bp_package_global
			(name,price,valid_days,daily_limit,total_limit,count_mode,qps_limit,status,remark,renewal_enabled)
		values(?,10.0000,?,100,0,'success',5,1,'renewal integration test',?)
	`, "Renewal package "+itoa(suffix), validDays, renewalEnabled)
	if err != nil {
		t.Fatalf("insert test package: %v", err)
	}
	packageID, err := packageResult.LastInsertId()
	if err != nil {
		t.Fatalf("test package id: %v", err)
	}

	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_package_renewal_log where user_id=?", userID)
		_, _ = db.Exec("delete from bp_balance_purchase_request where user_id=?", userID)
		_, _ = db.Exec("delete from bp_wallet_transaction where user_id=?", userID)
		_, _ = db.Exec("delete from bp_user_package_global where user_id=?", userID)
		_, _ = db.Exec("delete from bp_user where id=?", userID)
		_, _ = db.Exec("delete from bp_package_global where id=?", packageID)
	})

	return &renewalTestEnv{db: db, ctx: ctx, userID: userID, packageID: packageID, suffix: suffix}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	pos := len(buf)
	for v > 0 {
		pos--
		buf[pos] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[pos:])
}

// entitlementCount 返回该用户在该套餐下的全部权益行数（不论是否到期）。
func (e *renewalTestEnv) entitlementCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.db.QueryRowContext(e.ctx,
		`select count(*) from bp_user_package_global where user_id=? and package_id=?`,
		e.userID, e.packageID).Scan(&n); err != nil {
		t.Fatalf("count entitlements: %v", err)
	}
	return n
}

// daysToEnd 返回 end_at 距今的天数，四舍五入到整数，用于断言「加了正好 N 天」。
func (e *renewalTestEnv) daysToEnd(t *testing.T) int {
	t.Helper()
	var seconds int64
	if err := e.db.QueryRowContext(e.ctx, `
		select timestampdiff(second, now(), end_at)
		from bp_user_package_global where user_id=? and package_id=? order by id desc limit 1
	`, e.userID, e.packageID).Scan(&seconds); err != nil {
		t.Fatalf("read end_at: %v", err)
	}
	return int((seconds + 43200) / 86400)
}

func (e *renewalTestEnv) renewalLogCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.db.QueryRowContext(e.ctx,
		`select count(*) from bp_package_renewal_log where user_id=? and scope='GLOBAL'`,
		e.userID).Scan(&n); err != nil {
		t.Fatalf("count renewal logs: %v", err)
	}
	return n
}

// 用例 1 + 2：首次购买建一条，有效期内再买顺延同一条。
func TestRenewalExtendsExistingGlobalEntitlement(t *testing.T) {
	env := newRenewalTestEnv(t, 30, 1)
	repo := NewRepository(env.db)

	first := "renewal-first-" + itoa(env.suffix)
	if err := repo.PurchaseGlobal(env.ctx, env.userID, env.packageID, first); err != nil {
		t.Fatalf("first purchase: %v", err)
	}
	if got := env.entitlementCount(t); got != 1 {
		t.Fatalf("after first purchase entitlements=%d, want 1", got)
	}
	if got := env.daysToEnd(t); got != 30 {
		t.Fatalf("after first purchase days=%d, want 30", got)
	}
	if got := env.renewalLogCount(t); got != 0 {
		t.Fatalf("first purchase should not write renewal log, got %d", got)
	}

	second := "renewal-second-" + itoa(env.suffix)
	if err := repo.PurchaseGlobal(env.ctx, env.userID, env.packageID, second); err != nil {
		t.Fatalf("renewal purchase: %v", err)
	}
	// 关键断言：仍是同一条权益，不是新增第二条。
	if got := env.entitlementCount(t); got != 1 {
		t.Fatalf("after renewal entitlements=%d, want 1 (must extend, not insert)", got)
	}
	if got := env.daysToEnd(t); got != 60 {
		t.Fatalf("after renewal days=%d, want 60", got)
	}
	if got := env.renewalLogCount(t); got != 1 {
		t.Fatalf("after renewal logs=%d, want 1", got)
	}

	// 流水里的旧/新到期时间必须能对上，否则退款算不出来。
	var addDays int
	var oldBeforeNew bool
	if err := env.db.QueryRowContext(env.ctx, `
		select add_days, old_end_at < new_end_at
		from bp_package_renewal_log where user_id=? order by id desc limit 1
	`, env.userID).Scan(&addDays, &oldBeforeNew); err != nil {
		t.Fatalf("read renewal log: %v", err)
	}
	if addDays != 30 || !oldBeforeNew {
		t.Fatalf("renewal log add_days=%d old<new=%v", addDays, oldBeforeNew)
	}
}

// 用例 3：权益已过期时再买，应新建一条并且从现在起算，不能倒挂到过期日。
func TestRenewalAfterExpiryCreatesFreshEntitlement(t *testing.T) {
	env := newRenewalTestEnv(t, 30, 1)
	repo := NewRepository(env.db)

	if err := repo.PurchaseGlobal(env.ctx, env.userID, env.packageID, "expired-first-"+itoa(env.suffix)); err != nil {
		t.Fatalf("first purchase: %v", err)
	}
	// 手工把到期时间拨到 3 天前，模拟已过期。
	if _, err := env.db.ExecContext(env.ctx, `
		update bp_user_package_global set end_at = date_sub(now(), interval 3 day)
		where user_id=? and package_id=?
	`, env.userID, env.packageID); err != nil {
		t.Fatalf("expire entitlement: %v", err)
	}

	if err := repo.PurchaseGlobal(env.ctx, env.userID, env.packageID, "expired-second-"+itoa(env.suffix)); err != nil {
		t.Fatalf("purchase after expiry: %v", err)
	}
	// 过期条目仍留在库里（历史凭证），新条目从今天起算。
	if got := env.entitlementCount(t); got != 2 {
		t.Fatalf("entitlements=%d, want 2 (old expired + new)", got)
	}
	if got := env.daysToEnd(t); got != 30 {
		t.Fatalf("new entitlement days=%d, want 30 (must not back-date)", got)
	}
	if got := env.renewalLogCount(t); got != 0 {
		t.Fatalf("renewal logs=%d, want 0 (this was a fresh purchase)", got)
	}
}

// 用例 4：不同幂等键各算一次；同一幂等键重复提交只算一次。
func TestRenewalIsIdempotentPerKey(t *testing.T) {
	env := newRenewalTestEnv(t, 30, 1)
	repo := NewRepository(env.db)

	key := "idem-" + itoa(env.suffix)
	if err := repo.PurchaseGlobal(env.ctx, env.userID, env.packageID, key); err != nil {
		t.Fatalf("purchase: %v", err)
	}
	// 同一 key 重放：应被 claimBalancePurchaseTx 识别为已处理，天数不再增加。
	if err := repo.PurchaseGlobal(env.ctx, env.userID, env.packageID, key); err != nil {
		t.Fatalf("replay purchase: %v", err)
	}
	if got := env.daysToEnd(t); got != 30 {
		t.Fatalf("after replay days=%d, want 30 (idempotent)", got)
	}
}

// 用例 5：renewal_enabled=0 的套餐保持旧行为——每次购买新建一条。
func TestRenewalDisabledInsertsNewEntitlement(t *testing.T) {
	env := newRenewalTestEnv(t, 30, 0)
	repo := NewRepository(env.db)

	if err := repo.PurchaseGlobal(env.ctx, env.userID, env.packageID, "disabled-1-"+itoa(env.suffix)); err != nil {
		t.Fatalf("first purchase: %v", err)
	}
	if err := repo.PurchaseGlobal(env.ctx, env.userID, env.packageID, "disabled-2-"+itoa(env.suffix)); err != nil {
		t.Fatalf("second purchase: %v", err)
	}
	if got := env.entitlementCount(t); got != 2 {
		t.Fatalf("entitlements=%d, want 2 (renewal disabled)", got)
	}
	if got := env.renewalLogCount(t); got != 0 {
		t.Fatalf("renewal logs=%d, want 0", got)
	}
}

// 用例 6：接口套餐规格按 spec_id 顺延，且不与其他规格互相串。
func TestRenewalExtendsInterfaceSpecEntitlement(t *testing.T) {
	dsn := os.Getenv("BUGPK_PACKAGE_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_PACKAGE_INTEGRATION_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	suffix := time.Now().UnixNano()
	username := "renewal_iface_" + itoa(suffix)
	userResult, err := db.ExecContext(ctx, `
		insert into bp_user(username,password_hash,display_name,balance,points,status)
		values(?, '$2a$10$integration.test.hash.placeholder', ?, 1000.0000, 0, 1)
	`, username, username)
	if err != nil {
		t.Fatalf("insert test user: %v", err)
	}
	userID, _ := userResult.LastInsertId()

	ifaceResult, err := db.ExecContext(ctx, `
		insert into bp_interface_api(api_code,name,request_url,request_method,price,point_price,status,remark)
		values(?, 'Renewal IFace','https://example.invalid/x','GET',0,0,1,'renewal test')
	`, "renewal_iface_"+itoa(suffix))
	if err != nil {
		t.Fatalf("insert interface: %v", err)
	}
	interfaceID, _ := ifaceResult.LastInsertId()

	pkgResult, err := db.ExecContext(ctx, `
		insert into bp_package_interface(interface_id,name,status,remark)
		values(?, 'Renewal interface package', 1, 'renewal test')
	`, interfaceID)
	if err != nil {
		t.Fatalf("insert interface package: %v", err)
	}
	packageID, _ := pkgResult.LastInsertId()

	specResult, err := db.ExecContext(ctx, `
		insert into bp_package_interface_spec
			(package_id,name,price,valid_days,daily_limit,total_limit,count_mode,qps_limit,status,renewal_enabled)
		values(?, '月付', 10.0000, 30, 100, 0, 'success', 5, 1, 1)
	`, packageID)
	if err != nil {
		t.Fatalf("insert spec: %v", err)
	}
	specID, _ := specResult.LastInsertId()

	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_package_renewal_log where user_id=?", userID)
		_, _ = db.Exec("delete from bp_balance_purchase_request where user_id=?", userID)
		_, _ = db.Exec("delete from bp_wallet_transaction where user_id=?", userID)
		_, _ = db.Exec("delete from bp_user_package_interface where user_id=?", userID)
		_, _ = db.Exec("delete from bp_user where id=?", userID)
		_, _ = db.Exec("delete from bp_package_interface_spec where id=?", specID)
		_, _ = db.Exec("delete from bp_package_interface where id=?", packageID)
		_, _ = db.Exec("delete from bp_interface_api where id=?", interfaceID)
	})

	repo := NewRepository(db)
	if err := repo.PurchaseInterface(ctx, userID, specID, "iface-1-"+itoa(suffix)); err != nil {
		t.Fatalf("first interface purchase: %v", err)
	}
	if err := repo.PurchaseInterface(ctx, userID, specID, "iface-2-"+itoa(suffix)); err != nil {
		t.Fatalf("interface renewal: %v", err)
	}

	var n int
	if err := db.QueryRowContext(ctx,
		`select count(*) from bp_user_package_interface where user_id=? and spec_id=?`,
		userID, specID).Scan(&n); err != nil {
		t.Fatalf("count interface entitlements: %v", err)
	}
	if n != 1 {
		t.Fatalf("interface entitlements=%d, want 1 (must extend by spec_id)", n)
	}

	var days int
	if err := db.QueryRowContext(ctx, `
		select round(timestampdiff(second, now(), end_at) / 86400)
		from bp_user_package_interface where user_id=? and spec_id=?
	`, userID, specID).Scan(&days); err != nil {
		t.Fatalf("read interface end_at: %v", err)
	}
	if days != 60 {
		t.Fatalf("interface days=%d, want 60", days)
	}

	var logs int
	if err := db.QueryRowContext(ctx,
		`select count(*) from bp_package_renewal_log where user_id=? and scope='INTERFACE' and spec_id=?`,
		userID, specID).Scan(&logs); err != nil {
		t.Fatalf("count interface renewal logs: %v", err)
	}
	if logs != 1 {
		t.Fatalf("interface renewal logs=%d, want 1", logs)
	}
}

// 顺延不能重置当天已用：续费不换计费周期，否则用户按续费节奏刷日限额。
func TestRenewalKeepsCurrentDayUsage(t *testing.T) {
	env := newRenewalTestEnv(t, 30, 1)
	repo := NewRepository(env.db)

	if err := repo.PurchaseGlobal(env.ctx, env.userID, env.packageID, "usage-1-"+itoa(env.suffix)); err != nil {
		t.Fatalf("first purchase: %v", err)
	}
	// 模拟当天已用 88 次。
	if _, err := env.db.ExecContext(env.ctx, `
		update bp_user_package_global set day_key=curdate(), day_used=88
		where user_id=? and package_id=?
	`, env.userID, env.packageID); err != nil {
		t.Fatalf("set day usage: %v", err)
	}

	if err := repo.PurchaseGlobal(env.ctx, env.userID, env.packageID, "usage-2-"+itoa(env.suffix)); err != nil {
		t.Fatalf("renewal purchase: %v", err)
	}

	var dayUsed int64
	if err := env.db.QueryRowContext(env.ctx, `
		select day_used from bp_user_package_global where user_id=? and package_id=?
	`, env.userID, env.packageID).Scan(&dayUsed); err != nil {
		t.Fatalf("read day_used: %v", err)
	}
	if dayUsed != 88 {
		t.Fatalf("day_used=%d after renewal, want 88 (must not reset)", dayUsed)
	}
}

// UserPackages 要能告诉前端「用户已持有同款、剩几天、能否续费」。
func TestUserPackagesExposesRenewalFields(t *testing.T) {
	env := newRenewalTestEnv(t, 30, 1)
	repo := NewRepository(env.db)

	if err := repo.PurchaseGlobal(env.ctx, env.userID, env.packageID, "view-"+itoa(env.suffix)); err != nil {
		t.Fatalf("purchase: %v", err)
	}

	items, err := repo.UserPackages(env.ctx, env.userID)
	if err != nil {
		t.Fatalf("list user packages: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("user packages=%d, want 1", len(items))
	}
	item := items[0]
	if item.PackageID != itoa(env.packageID) {
		t.Fatalf("packageId=%q, want %d", item.PackageID, env.packageID)
	}
	if !item.Renewable {
		t.Fatalf("renewable=false, want true")
	}
	if item.RemainDays != 30 {
		t.Fatalf("remainDays=%d, want 30", item.RemainDays)
	}
}
