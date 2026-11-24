package redeem_test

// 兑换码域集成测试：REDEEM_INTEGRATION=1 门控。
//
// 前置条件：
//   - 本地 MySQL（configs/config.yaml 或 DB_* 环境变量指向的实例）；
//   - 专用测试库（库名须含 test/e2e/bench，由 testdb 隔离守卫校验，
//     本测试固定注入 bugpk_redeem_test）；
//   - Redis 用进程内 miniredis（无需外部实例）。
//
// 覆盖设计文档第三节的并发不变量：双协程同码、同类型双码、限 N 次、
// 每 IP 限领（1 次与 N 次两档）、组合奖励、套餐到账、生成/作废/导出、
// 类型编辑冻结（M-2）与开放 API 来源的 IP 豁免。

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"bugpk-api/internal/app"
	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/domain/redeem"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/testdb"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------- 脚手架

type redeemFixture struct {
	svc *redeem.Service
	db  *sql.DB
	rdb *redis.Client
	ctx context.Context
}

func setupRedeemIntegration(t *testing.T) redeemFixture {
	t.Helper()
	if os.Getenv("REDEEM_INTEGRATION") != "1" {
		t.Skip("set REDEEM_INTEGRATION=1 to run redeem integration tests")
	}
	t.Setenv("DB_NAME", "bugpk_redeem_test")
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	dsn := cfg.Database.DSN()
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(16)
	t.Cleanup(func() { _ = db.Close() })
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		t.Fatalf("integration MySQL unavailable: %v", err)
	}
	if err := app.RunMigrations(db, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	resetRedeemTables(t, db)

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	guard := redeem.NewGuard(rdb, "redeem-integration-secret")
	repo := redeem.NewRepository(db)
	authz := rbac.NewService(rbac.NewRepository(db), nil)
	svc := redeem.NewService(repo, authz, guard, db)
	return redeemFixture{svc: svc, db: db, rdb: rdb, ctx: context.Background()}
}

// resetRedeemTables 清空兑换码域全部表与测试造数（专用测试库内安全）。
func resetRedeemTables(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`delete from bp_redeem_record`,
		`delete from bp_redeem_code`,
		`delete from bp_redeem_generation_batch`,
		`delete from bp_redeem_code_type`,
		`delete from bp_redeem_ip_counter`,
		`delete from bp_redeem_guard_config`,
		`insert into bp_redeem_guard_config (id, ip_minute_limit, ip_hour_limit, ip_day_limit, user_minute_limit)
		 values (1, 60, 600, 3000, 20) on duplicate key update id = id`,
		`delete from bp_wallet_transaction where biz_type = 'REDEEM_CODE'`,
		`delete from bp_user_package_global where user_id in (select id from bp_user where username like 'redeem_it_%')`,
		`delete from bp_user_package_interface where user_id in (select id from bp_user where username like 'redeem_it_%')`,
		`delete from bp_user where username like 'redeem_it_%'`,
		`delete from bp_package_global where name like 'IT-%'`,
		`delete from bp_package_interface_spec where name like 'IT-%'`,
		`delete from bp_package_interface where name like 'IT-%'`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("reset %q: %v", stmt, err)
		}
	}
}

func seedUser(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	result, err := db.Exec(`
		insert into bp_user (username, password_hash, display_name, home_path, balance, points, status, token_version)
		values (?, 'integration-placeholder', ?, '/console/workspace', 0, 0, 1, 0)
	`, name, name)
	if err != nil {
		t.Fatalf("seed user %s: %v", name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

type typeSeed struct {
	code         string
	name         string
	rewardType   int
	points       int64
	balance      string
	refID        int64
	perUserLimit int
	perIPLimit   int
	status       *int
}

func seedType(t *testing.T, db *sql.DB, seed typeSeed) int64 {
	t.Helper()
	if seed.balance == "" {
		seed.balance = "0.0000"
	}
	status := 1
	if seed.status != nil {
		status = *seed.status
	}
	now := time.Now()
	result, err := db.Exec(`
		insert into bp_redeem_code_type
			(code, name, reward_type, reward_points, reward_balance, reward_ref_id,
			 per_user_limit, per_ip_limit, start_at, end_at, status)
		values (?,?,?,?,?,?,?,?,?,?,?)
	`, seed.code, seed.name, seed.rewardType, seed.points, seed.balance, seed.refID,
		seed.perUserLimit, seed.perIPLimit, now.Add(-24*time.Hour), now.Add(30*24*time.Hour), status)
	if err != nil {
		t.Fatalf("seed type %s: %v", seed.code, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func codePlains(t *testing.T, db *sql.DB, typeID int64) []string {
	t.Helper()
	rows, err := db.Query(`select code_plain from bp_redeem_code where type_id=? order by id asc`, typeID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plains []string
	for rows.Next() {
		var plain string
		if err := rows.Scan(&plain); err != nil {
			t.Fatal(err)
		}
		plains = append(plains, plain)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return plains
}

var (
	integrationAdmin = redeem.Principal{ID: 1, Username: "admin", Roles: []string{"admin"}}
	integrationMeta  = redeem.AuditMeta{Method: "POST", Path: "/integration", ClientIP: "127.0.0.1", UserAgent: "integration"}
)

func bizFailure(t *testing.T, err error) (int, string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected business error, got nil")
	}
	biz, ok := apperrors.AsBusinessError(err)
	if !ok {
		t.Fatalf("error is not a BusinessError: %v", err)
	}
	return biz.StatusCode, biz.Message
}

func expectRedeemError(t *testing.T, err error, status int, want string) {
	t.Helper()
	gotStatus, gotMessage := bizFailure(t, err)
	if gotStatus != status {
		t.Fatalf("status = %d, want %d (message %q)", gotStatus, status, gotMessage)
	}
	if !contains(gotMessage, want) {
		t.Fatalf("message = %q, want contains %q", gotMessage, want)
	}
}

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func userPrincipal(userID int64, name string) redeem.Principal {
	return redeem.Principal{ID: userID, Username: name}
}

func assertUserAssets(t *testing.T, db *sql.DB, userID int64, balance string, points int64) {
	t.Helper()
	var gotBalance string
	var gotPoints int64
	if err := db.QueryRow(`select cast(balance as char), points from bp_user where id=?`, userID).
		Scan(&gotBalance, &gotPoints); err != nil {
		t.Fatal(err)
	}
	if gotBalance != balance || gotPoints != points {
		t.Fatalf("user assets = (%s, %d), want (%s, %d)", gotBalance, gotPoints, balance, points)
	}
}

func assertWalletTxCount(t *testing.T, db *sql.DB, userID int64, want int64) {
	t.Helper()
	var count int64
	if err := db.QueryRow(`
		select count(*) from bp_wallet_transaction
		where user_id=? and biz_type='REDEEM_CODE'
	`, userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("wallet tx count = %d, want %d", count, want)
	}
}

func assertRecordCount(t *testing.T, db *sql.DB, userID, typeID int64, want int64) {
	t.Helper()
	var count int64
	if err := db.QueryRow(`
		select count(*) from bp_redeem_record where user_id=? and type_id=?
	`, userID, typeID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("record count = %d, want %d", count, want)
	}
}

// ---------------------------------------------------------------- 奖励矩阵

func TestRedeemPointsRewardIntegration(t *testing.T) {
	fx := setupRedeemIntegration(t)
	typeID := seedType(t, fx.db, typeSeed{
		code: "IT-POINTS", name: "积分类型", rewardType: redeem.RewardTypePoints, points: 100,
	})
	generated, err := fx.svc.GenerateCodes(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: 2}, integrationMeta)
	if err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	if generated.Count != 2 || generated.BatchID == "" {
		t.Fatalf("generate result = %+v", generated)
	}
	plains := codePlains(t, fx.db, typeID)
	if len(plains) != 2 {
		t.Fatalf("plains = %d, want 2", len(plains))
	}

	user := seedUser(t, fx.db, "redeem_it_points_user")
	principal := userPrincipal(user, "redeem_it_points_user")

	result, err := fx.svc.Redeem(fx.ctx, principal, plains[0], "10.1.0.1", redeem.SourceConsole)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if result.RewardType != redeem.RewardTypePoints || result.RewardPoints != 100 || result.PointsAfter != 100 {
		t.Fatalf("redeem result = %+v", result)
	}
	assertUserAssets(t, fx.db, user, "0.0000", 100)
	assertWalletTxCount(t, fx.db, user, 1)
	assertRecordCount(t, fx.db, user, typeID, 1)

	// 同一码重复兑换：已用。
	_, err = fx.svc.Redeem(fx.ctx, principal, plains[0], "10.1.0.1", redeem.SourceConsole)
	expectRedeemError(t, err, 400, "兑换码已被使用")

	// 不限次类型：第二枚码可正常兑换。
	result, err = fx.svc.Redeem(fx.ctx, principal, plains[1], "10.1.0.1", redeem.SourceConsole)
	if err != nil {
		t.Fatalf("second Redeem: %v", err)
	}
	if result.PointsAfter != 200 {
		t.Fatalf("points after second redeem = %d, want 200", result.PointsAfter)
	}
	assertUserAssets(t, fx.db, user, "0.0000", 200)
	assertWalletTxCount(t, fx.db, user, 2)
}

func TestRedeemBalanceRewardIntegration(t *testing.T) {
	fx := setupRedeemIntegration(t)
	typeID := seedType(t, fx.db, typeSeed{
		code: "IT-BALANCE", name: "余额类型", rewardType: redeem.RewardTypeBalance, balance: "12.5000",
	})
	if _, err := fx.svc.GenerateCodes(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: 1}, integrationMeta); err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	plains := codePlains(t, fx.db, typeID)
	user := seedUser(t, fx.db, "redeem_it_balance_user")

	result, err := fx.svc.Redeem(fx.ctx, userPrincipal(user, "redeem_it_balance_user"),
		plains[0], "10.2.0.1", redeem.SourceConsole)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if result.RewardBalance != "12.5000" || result.BalanceAfter != "12.5000" {
		t.Fatalf("redeem result = %+v", result)
	}
	assertUserAssets(t, fx.db, user, "12.5000", 0)
	assertWalletTxCount(t, fx.db, user, 1)

	var amount string
	if err := fx.db.QueryRow(`
		select cast(amount as char) from bp_wallet_transaction
		where user_id=? and biz_type='REDEEM_CODE'
	`, user).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	if amount != "12.5000" {
		t.Fatalf("wallet amount = %s, want 12.5000", amount)
	}
}

func TestRedeemCombinedRewardIntegration(t *testing.T) {
	fx := setupRedeemIntegration(t)
	typeID := seedType(t, fx.db, typeSeed{
		code: "IT-COMBINED", name: "组合类型", rewardType: redeem.RewardTypeCombined,
		points: 50, balance: "5.0000",
	})
	if _, err := fx.svc.GenerateCodes(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: 1}, integrationMeta); err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	plains := codePlains(t, fx.db, typeID)
	user := seedUser(t, fx.db, "redeem_it_combined_user")

	result, err := fx.svc.Redeem(fx.ctx, userPrincipal(user, "redeem_it_combined_user"),
		plains[0], "10.3.0.1", redeem.SourceConsole)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if result.RewardPoints != 50 || result.RewardBalance != "5.0000" {
		t.Fatalf("redeem result = %+v", result)
	}
	if result.PointsAfter != 50 || result.BalanceAfter != "5.0000" {
		t.Fatalf("after values = %+v", result)
	}
	assertUserAssets(t, fx.db, user, "5.0000", 50)
	// 组合奖励两条流水（点数 + 余额）。
	assertWalletTxCount(t, fx.db, user, 2)
}

func TestRedeemGlobalPackageRewardIntegration(t *testing.T) {
	fx := setupRedeemIntegration(t)
	packageID := seedGlobalPackage(t, fx.db, "IT-GlobalPkg", 30)
	typeID := seedType(t, fx.db, typeSeed{
		code: "IT-GLOBAL", name: "全站套餐类型", rewardType: redeem.RewardTypeGlobal, refID: packageID,
	})
	if _, err := fx.svc.GenerateCodes(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: 1}, integrationMeta); err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	plains := codePlains(t, fx.db, typeID)
	user := seedUser(t, fx.db, "redeem_it_global_user")

	result, err := fx.svc.Redeem(fx.ctx, userPrincipal(user, "redeem_it_global_user"),
		plains[0], "10.4.0.1", redeem.SourceConsole)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if result.RewardName != "IT-GlobalPkg" {
		t.Fatalf("reward name = %q, want IT-GlobalPkg", result.RewardName)
	}
	if result.ValidDays == nil || *result.ValidDays != 30 {
		t.Fatalf("valid days = %v, want 30", result.ValidDays)
	}
	assertUserAssets(t, fx.db, user, "0.0000", 0)
	// 套餐类奖励零额流水一条（一码一流水）。
	assertWalletTxCount(t, fx.db, user, 1)

	var packageName string
	var validDays int
	if err := fx.db.QueryRow(`
		select package_name, datediff(end_at, start_at)
		from bp_user_package_global where user_id=?
	`, user).Scan(&packageName, &validDays); err != nil {
		t.Fatalf("user package row: %v", err)
	}
	if packageName != "IT-GlobalPkg" || validDays != 30 {
		t.Fatalf("user package = (%s, %d days)", packageName, validDays)
	}
	// 记录快照防漂移。
	var recordName string
	var recordDays int
	if err := fx.db.QueryRow(`
		select reward_name, valid_days from bp_redeem_record where user_id=?
	`, user).Scan(&recordName, &recordDays); err != nil {
		t.Fatal(err)
	}
	if recordName != "IT-GlobalPkg" || recordDays != 30 {
		t.Fatalf("record snapshot = (%s, %d)", recordName, recordDays)
	}
}

func TestRedeemInterfaceSpecRewardIntegration(t *testing.T) {
	fx := setupRedeemIntegration(t)
	pkgID := seedInterfacePackage(t, fx.db, "IT-InterfacePkg")
	specID := seedInterfaceSpec(t, fx.db, pkgID, "IT-SpecA", 15)
	typeID := seedType(t, fx.db, typeSeed{
		code: "IT-SPEC", name: "接口套餐类型", rewardType: redeem.RewardTypeSpec, refID: specID,
	})
	if _, err := fx.svc.GenerateCodes(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: 1}, integrationMeta); err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	plains := codePlains(t, fx.db, typeID)
	user := seedUser(t, fx.db, "redeem_it_spec_user")

	result, err := fx.svc.Redeem(fx.ctx, userPrincipal(user, "redeem_it_spec_user"),
		plains[0], "10.5.0.1", redeem.SourceConsole)
	if err != nil {
		t.Fatalf("Redeem: %v", err)
	}
	if result.RewardName != "IT-InterfacePkg-IT-SpecA" {
		t.Fatalf("reward name = %q", result.RewardName)
	}
	if result.ValidDays == nil || *result.ValidDays != 15 {
		t.Fatalf("valid days = %v, want 15", result.ValidDays)
	}
	var specName string
	if err := fx.db.QueryRow(`
		select spec_name from bp_user_package_interface where user_id=?
	`, user).Scan(&specName); err != nil {
		t.Fatalf("user spec package row: %v", err)
	}
	if specName != "IT-SpecA" {
		t.Fatalf("spec name = %q", specName)
	}
	assertWalletTxCount(t, fx.db, user, 1)
}

func seedGlobalPackage(t *testing.T, db *sql.DB, name string, validDays int) int64 {
	t.Helper()
	result, err := db.Exec(`
		insert into bp_package_global (name, price, valid_days, daily_limit, qps_limit, status)
		values (?, 0.0000, ?, 0, 0, 1)
	`, name, validDays)
	if err != nil {
		t.Fatalf("seed global package: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func seedInterfacePackage(t *testing.T, db *sql.DB, name string) int64 {
	t.Helper()
	result, err := db.Exec(`
		insert into bp_package_interface (interface_id, name, status)
		values (1, ?, 1)
	`, name)
	if err != nil {
		t.Fatalf("seed interface package: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func seedInterfaceSpec(t *testing.T, db *sql.DB, packageID int64, name string, validDays int) int64 {
	t.Helper()
	result, err := db.Exec(`
		insert into bp_package_interface_spec (package_id, name, price, valid_days, daily_limit, qps_limit, status)
		values (?, ?, 0.0000, ?, 0, 0, 1)
	`, packageID, name, validDays)
	if err != nil {
		t.Fatalf("seed interface spec: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// ---------------------------------------------------------------- 并发不变量

func TestRedeemConcurrentSameCodeOnlyOneWinsIntegration(t *testing.T) {
	fx := setupRedeemIntegration(t)
	typeID := seedType(t, fx.db, typeSeed{
		code: "IT-CONCURRENT", name: "并发类型", rewardType: redeem.RewardTypePoints, points: 100,
	})
	if _, err := fx.svc.GenerateCodes(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: 1}, integrationMeta); err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	plains := codePlains(t, fx.db, typeID)
	user := seedUser(t, fx.db, "redeem_it_concurrent_user")
	principal := userPrincipal(user, "redeem_it_concurrent_user")

	const racers = 8
	var (
		wait    sync.WaitGroup
		mu      sync.Mutex
		success int
		failMsg []string
	)
	wait.Add(racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		go func() {
			defer wait.Done()
			<-start
			_, err := fx.svc.Redeem(fx.ctx, principal, plains[0], "10.6.0.9", redeem.SourceConsole)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				success++
				return
			}
			biz, ok := apperrors.AsBusinessError(err)
			if !ok {
				failMsg = append(failMsg, "non-business error: "+err.Error())
				return
			}
			failMsg = append(failMsg, biz.Message)
		}()
	}
	close(start)
	wait.Wait()

	if success != 1 {
		t.Fatalf("successful redemptions = %d, want 1 (failures: %v)", success, failMsg)
	}
	assertUserAssets(t, fx.db, user, "0.0000", 100)
	assertWalletTxCount(t, fx.db, user, 1)
	assertRecordCount(t, fx.db, user, typeID, 1)

	var codeStatus int
	var usedBy int64
	if err := fx.db.QueryRow(`select status, coalesce(used_by,0) from bp_redeem_code where type_id=?`, typeID).
		Scan(&codeStatus, &usedBy); err != nil {
		t.Fatal(err)
	}
	if codeStatus != redeem.CodeStatusConsumed || usedBy != user {
		t.Fatalf("code status = %d used_by = %d", codeStatus, usedBy)
	}
}

func TestRedeemPerUserLimitOneBlocksSecondCodeIntegration(t *testing.T) {
	fx := setupRedeemIntegration(t)
	typeID := seedType(t, fx.db, typeSeed{
		code: "IT-USER1", name: "每账号一次", rewardType: redeem.RewardTypePoints,
		points: 100, perUserLimit: 1,
	})
	if _, err := fx.svc.GenerateCodes(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: 2}, integrationMeta); err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	plains := codePlains(t, fx.db, typeID)
	user := seedUser(t, fx.db, "redeem_it_user1")
	principal := userPrincipal(user, "redeem_it_user1")

	if _, err := fx.svc.Redeem(fx.ctx, principal, plains[0], "10.7.0.1", redeem.SourceConsole); err != nil {
		t.Fatalf("first Redeem: %v", err)
	}
	// 同类型第二枚码：限领拦截（友好预检 + uk_user_type_lock 兜底）。
	_, err := fx.svc.Redeem(fx.ctx, principal, plains[1], "10.7.0.1", redeem.SourceConsole)
	expectRedeemError(t, err, 400, "已达到该类型兑换码的限领次数")
	assertUserAssets(t, fx.db, user, "0.0000", 100)
	assertRecordCount(t, fx.db, user, typeID, 1)
}

func TestRedeemPerUserLimitNAllowsUpToNIntegration(t *testing.T) {
	fx := setupRedeemIntegration(t)
	typeID := seedType(t, fx.db, typeSeed{
		code: "IT-USERN", name: "每账号两次", rewardType: redeem.RewardTypePoints,
		points: 10, perUserLimit: 2,
	})
	if _, err := fx.svc.GenerateCodes(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: 3}, integrationMeta); err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	plains := codePlains(t, fx.db, typeID)
	user := seedUser(t, fx.db, "redeem_it_usern")
	principal := userPrincipal(user, "redeem_it_usern")

	for i := 0; i < 2; i++ {
		if _, err := fx.svc.Redeem(fx.ctx, principal, plains[i], "10.8.0.1", redeem.SourceConsole); err != nil {
			t.Fatalf("Redeem #%d: %v", i+1, err)
		}
	}
	_, err := fx.svc.Redeem(fx.ctx, principal, plains[2], "10.8.0.1", redeem.SourceConsole)
	expectRedeemError(t, err, 400, "已达到该类型兑换码的限领次数")
	assertUserAssets(t, fx.db, user, "0.0000", 20)
	assertRecordCount(t, fx.db, user, typeID, 2)
}

func TestRedeemPerIPLimitOneIntegration(t *testing.T) {
	fx := setupRedeemIntegration(t)
	typeID := seedType(t, fx.db, typeSeed{
		code: "IT-IP1", name: "每IP一次", rewardType: redeem.RewardTypePoints,
		points: 100, perIPLimit: 1,
	})
	if _, err := fx.svc.GenerateCodes(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: 2}, integrationMeta); err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	plains := codePlains(t, fx.db, typeID)
	userA := seedUser(t, fx.db, "redeem_it_ip_a")
	userB := seedUser(t, fx.db, "redeem_it_ip_b")

	if _, err := fx.svc.Redeem(fx.ctx, userPrincipal(userA, "redeem_it_ip_a"),
		plains[0], "10.9.0.1", redeem.SourceConsole); err != nil {
		t.Fatalf("user A Redeem: %v", err)
	}
	// 同 IP 第二个账号：IP 限领拦截。
	_, err := fx.svc.Redeem(fx.ctx, userPrincipal(userB, "redeem_it_ip_b"),
		plains[1], "10.9.0.1", redeem.SourceConsole)
	expectRedeemError(t, err, 400, "该 IP 已达到限领次数")
	assertUserAssets(t, fx.db, userB, "0.0000", 0)

	// 开放 API 来源豁免 IP 限领（集中出口 NAT）：换账号同 IP 放行。
	userC := seedUser(t, fx.db, "redeem_it_ip_c")
	if _, err := fx.svc.GenerateCodes(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: 1}, integrationMeta); err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	plains = codePlains(t, fx.db, typeID)
	if _, err := fx.svc.Redeem(fx.ctx, userPrincipal(userC, "redeem_it_ip_c"),
		plains[2], "10.9.0.1", redeem.SourceOpenAPI); err != nil {
		t.Fatalf("open API Redeem should bypass IP limit: %v", err)
	}
	assertUserAssets(t, fx.db, userC, "0.0000", 100)
}

func TestRedeemPerIPLimitNCounterIntegration(t *testing.T) {
	fx := setupRedeemIntegration(t)
	typeID := seedType(t, fx.db, typeSeed{
		code: "IT-IPN", name: "每IP两次", rewardType: redeem.RewardTypePoints,
		points: 5, perIPLimit: 2,
	})
	if _, err := fx.svc.GenerateCodes(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: 3}, integrationMeta); err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	plains := codePlains(t, fx.db, typeID)
	const clientIP = "10.10.0.1"
	for i := 0; i < 2; i++ {
		username := fmt.Sprintf("redeem_it_ipn_ok_%d", i)
		user := seedUser(t, fx.db, username)
		if _, err := fx.svc.Redeem(fx.ctx, userPrincipal(user, username),
			plains[i], clientIP, redeem.SourceConsole); err != nil {
			t.Fatalf("Redeem #%d: %v", i+1, err)
		}
	}
	// 第三次同 IP：计数器超限。
	user := seedUser(t, fx.db, "redeem_it_ipn_blocked")
	_, err := fx.svc.Redeem(fx.ctx, userPrincipal(user, "redeem_it_ipn_blocked"),
		plains[2], clientIP, redeem.SourceConsole)
	expectRedeemError(t, err, 400, "该 IP 已达到限领次数")
	assertUserAssets(t, fx.db, user, "0.0000", 0)

	var used int64
	if err := fx.db.QueryRow(`
		select used from bp_redeem_ip_counter where type_id=? and client_ip=?
	`, typeID, clientIP).Scan(&used); err != nil {
		t.Fatal(err)
	}
	if used != 2 {
		t.Fatalf("ip counter used = %d, want 2", used)
	}
}

func TestRedeemIPLimitFailClosedWhenIPMissingIntegration(t *testing.T) {
	fx := setupRedeemIntegration(t)
	typeID := seedType(t, fx.db, typeSeed{
		code: "IT-IPFAIL", name: "IP缺失", rewardType: redeem.RewardTypePoints,
		points: 100, perIPLimit: 1,
	})
	if _, err := fx.svc.GenerateCodes(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: 1}, integrationMeta); err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	plains := codePlains(t, fx.db, typeID)
	user := seedUser(t, fx.db, "redeem_it_ipfail")
	// 生效 IP 限额但取不到可信 IP：fail-closed 503，绝不放行。
	_, err := fx.svc.Redeem(fx.ctx, userPrincipal(user, "redeem_it_ipfail"),
		plains[0], "", redeem.SourceConsole)
	expectRedeemError(t, err, 503, "服务繁忙")
	assertUserAssets(t, fx.db, user, "0.0000", 0)
	assertRecordCount(t, fx.db, user, typeID, 0)
}

// ---------------------------------------------------------------- 状态与窗口

func TestRedeemStateAndWindowIntegration(t *testing.T) {
	fx := setupRedeemIntegration(t)
	admin := integrationAdmin

	// 停用类型。
	disabledStatus := 0
	disabledType := seedType(t, fx.db, typeSeed{
		code: "IT-DISABLED", name: "停用类型", rewardType: redeem.RewardTypePoints,
		points: 100, status: &disabledStatus,
	})
	// 过期窗口类型。
	expiredType := seedExpiredType(t, fx.db, "IT-EXPIRED", "过期类型", redeem.RewardTypePoints, 100)
	// 正常类型（作废码用例）。
	activeType := seedType(t, fx.db, typeSeed{
		code: "IT-ACTIVE", name: "正常类型", rewardType: redeem.RewardTypePoints, points: 100,
	})

	if _, err := fx.svc.GenerateCodes(fx.ctx, admin, disabledType,
		redeem.GenerateCodesRequest{Count: 1}, integrationMeta); err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	if _, err := fx.svc.GenerateCodes(fx.ctx, admin, expiredType,
		redeem.GenerateCodesRequest{Count: 1}, integrationMeta); err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	if _, err := fx.svc.GenerateCodes(fx.ctx, admin, activeType,
		redeem.GenerateCodesRequest{Count: 1}, integrationMeta); err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}

	disabledPlains := codePlains(t, fx.db, disabledType)
	expiredPlains := codePlains(t, fx.db, expiredType)
	activePlains := codePlains(t, fx.db, activeType)

	user := seedUser(t, fx.db, "redeem_it_state_user")
	principal := userPrincipal(user, "redeem_it_state_user")

	_, err := fx.svc.Redeem(fx.ctx, principal, disabledPlains[0], "10.11.0.1", redeem.SourceConsole)
	expectRedeemError(t, err, 400, "该兑换码类型已停用")

	_, err = fx.svc.Redeem(fx.ctx, principal, expiredPlains[0], "10.11.0.1", redeem.SourceConsole)
	expectRedeemError(t, err, 400, "兑换码不在有效期内")

	_, err = fx.svc.Redeem(fx.ctx, principal, "AAAA-2345-EFGH-6789", "10.11.0.1", redeem.SourceConsole)
	expectRedeemError(t, err, 400, "兑换码不存在")

	// 作废后兑换：与不存在统一文案（消除存在性 oracle）。
	var codeID int64
	if err := fx.db.QueryRow(`select id from bp_redeem_code where type_id=?`, activeType).
		Scan(&codeID); err != nil {
		t.Fatal(err)
	}
	if err := fx.svc.VoidCode(fx.ctx, admin, codeID, integrationMeta); err != nil {
		t.Fatalf("VoidCode: %v", err)
	}
	var codeStatus int
	if err := fx.db.QueryRow(`select status from bp_redeem_code where id=?`, codeID).
		Scan(&codeStatus); err != nil {
		t.Fatal(err)
	}
	if codeStatus != redeem.CodeStatusVoided {
		t.Fatalf("voided code status = %d, want %d", codeStatus, redeem.CodeStatusVoided)
	}
	_, err = fx.svc.Redeem(fx.ctx, principal, activePlains[0], "10.11.0.1", redeem.SourceConsole)
	expectRedeemError(t, err, 400, "兑换码不存在")
	assertUserAssets(t, fx.db, user, "0.0000", 0)
	assertRecordCount(t, fx.db, user, activeType, 0)

	// 已作废码再次作废：条件推进拒绝。
	if err := fx.svc.VoidCode(fx.ctx, admin, codeID, integrationMeta); err == nil {
		t.Fatal("voiding a voided code should fail")
	}
}

func seedExpiredType(t *testing.T, db *sql.DB, code, name string, rewardType int, points int64) int64 {
	t.Helper()
	result, err := db.Exec(`
		insert into bp_redeem_code_type
			(code, name, reward_type, reward_points, reward_balance, reward_ref_id,
			 per_user_limit, per_ip_limit, start_at, end_at, status)
		values (?,?,?,?,0,0,0,0,?,?,1)
	`, code, name, rewardType, points, time.Now().Add(-72*time.Hour), time.Now().Add(-48*time.Hour))
	if err != nil {
		t.Fatalf("seed expired type: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// ---------------------------------------------------------------- 生成 / 导出 / 冻结

func TestGenerateVoidAndExportIntegration(t *testing.T) {
	fx := setupRedeemIntegration(t)
	typeID := seedType(t, fx.db, typeSeed{
		code: "IT-GEN", name: "生成导出", rewardType: redeem.RewardTypePoints, points: 100,
	})

	// 管理端生成 5 枚。
	consoleResult, err := fx.svc.GenerateCodes(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: 5, CampaignRef: "  it-campaign  "}, integrationMeta)
	if err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	if consoleResult.Count != 5 {
		t.Fatalf("console generate count = %d", consoleResult.Count)
	}
	plains := codePlains(t, fx.db, typeID)
	if len(plains) != 5 {
		t.Fatalf("plains = %d, want 5", len(plains))
	}
	// 明文入库格式：19 字符带分隔符。
	for _, plain := range plains {
		if len(plain) != 19 {
			t.Fatalf("plain %q length = %d, want 19", plain, len(plain))
		}
	}

	// 批次快照：来源=管理端、campaign_ref 修剪、限额快照。
	var (
		batchSource  int
		batchCount   int
		batchPerUser int
		batchPerIP   int
		batchRef     string
		batchCreator int64
	)
	if err := fx.db.QueryRow(`
		select source, count, per_user_limit_snap, per_ip_limit_snap, coalesce(campaign_ref,''), coalesce(created_by,0)
		from bp_redeem_generation_batch where id=?
	`, consoleResult.BatchID).Scan(&batchSource, &batchCount, &batchPerUser, &batchPerIP, &batchRef, &batchCreator); err != nil {
		t.Fatal(err)
	}
	if batchSource != redeem.SourceConsole || batchCount != 5 || batchRef != "it-campaign" {
		t.Fatalf("batch snapshot = (%d,%d,%q)", batchSource, batchCount, batchRef)
	}
	if batchPerUser != 0 || batchPerIP != 0 || batchCreator != integrationAdmin.ID {
		t.Fatalf("batch limits/creator = (%d,%d,%d)", batchPerUser, batchPerIP, batchCreator)
	}

	// 开放 API 生成：来源=2 + Key 追溯。
	openResult, err := fx.svc.GenerateCodesForOpenAPI(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: 2}, integrationMeta, 4242)
	if err != nil {
		t.Fatalf("GenerateCodesForOpenAPI: %v", err)
	}
	if openResult.Count != 2 {
		t.Fatalf("open api generate count = %d", openResult.Count)
	}
	var openSource int
	var creatorKey int64
	if err := fx.db.QueryRow(`
		select source, coalesce(creator_key_id,0) from bp_redeem_generation_batch where id=?
	`, openResult.BatchID).Scan(&openSource, &creatorKey); err != nil {
		t.Fatal(err)
	}
	if openSource != redeem.SourceOpenAPI || creatorKey != 4242 {
		t.Fatalf("open api batch = (source %d, key %d)", openSource, creatorKey)
	}

	// 单次生成上限。
	_, err = fx.svc.GenerateCodes(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: redeem.MaxGeneratePerBatch + 1}, integrationMeta)
	expectRedeemError(t, err, 400, "单次生成数量")

	// 导出 xlsx（zip 魔数 PK）。
	filename, data, err := fx.svc.ExportCodes(fx.ctx, integrationAdmin, typeID, integrationMeta)
	if err != nil {
		t.Fatalf("ExportCodes: %v", err)
	}
	if len(data) < 4 || data[0] != 'P' || data[1] != 'K' {
		t.Fatalf("xlsx magic wrong (%d bytes)", len(data))
	}
	if !contains(filename, "IT-GEN") {
		t.Fatalf("filename = %q, want contains type code", filename)
	}

	// 作废一枚未用码。
	var codeID int64
	if err := fx.db.QueryRow(`select id from bp_redeem_code where type_id=? order by id asc limit 1`, typeID).
		Scan(&codeID); err != nil {
		t.Fatal(err)
	}
	if err := fx.svc.VoidCode(fx.ctx, integrationAdmin, codeID, integrationMeta); err != nil {
		t.Fatalf("VoidCode: %v", err)
	}
	var statusCount int64
	if err := fx.db.QueryRow(`select count(*) from bp_redeem_code where type_id=? and status=0`, typeID).
		Scan(&statusCount); err != nil {
		t.Fatal(err)
	}
	if statusCount != 1 {
		t.Fatalf("voided count = %d, want 1", statusCount)
	}
}

func TestSaveTypeFreezesRewardAfterGenerationIntegration(t *testing.T) {
	fx := setupRedeemIntegration(t)
	typeID := seedType(t, fx.db, typeSeed{
		code: "IT-FREEZE", name: "冻结类型", rewardType: redeem.RewardTypePoints, points: 100,
	})
	if _, err := fx.svc.GenerateCodes(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: 1}, integrationMeta); err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}

	// 改面值：冻结拒绝（M-2）。
	err := fx.svc.SaveType(fx.ctx, integrationAdmin, typeID, redeem.SaveTypeRequest{
		Code: "IT-FREEZE", Name: "冻结类型", RewardType: redeem.RewardTypePoints,
		RewardPoints: 500,
		StartAt:      time.Now().Add(-24 * time.Hour).Format("2006-01-02 15:04:05"),
		EndAt:        time.Now().Add(30 * 24 * time.Hour).Format("2006-01-02 15:04:05"),
	})
	expectRedeemError(t, err, 409, "奖励与限领配置不可修改")

	// 放宽限领同样冻结。
	err = fx.svc.SaveType(fx.ctx, integrationAdmin, typeID, redeem.SaveTypeRequest{
		Code: "IT-FREEZE", Name: "冻结类型", RewardType: redeem.RewardTypePoints,
		RewardPoints: 100, PerUserLimit: 5,
		StartAt: time.Now().Add(-24 * time.Hour).Format("2006-01-02 15:04:05"),
		EndAt:   time.Now().Add(30 * 24 * time.Hour).Format("2006-01-02 15:04:05"),
	})
	expectRedeemError(t, err, 409, "奖励与限领配置不可修改")

	// 仅改名：放行。
	err = fx.svc.SaveType(fx.ctx, integrationAdmin, typeID, redeem.SaveTypeRequest{
		Code: "IT-FREEZE", Name: "冻结类型-改名", RewardType: redeem.RewardTypePoints,
		RewardPoints: 100,
		StartAt:      time.Now().Add(-24 * time.Hour).Format("2006-01-02 15:04:05"),
		EndAt:        time.Now().Add(30 * 24 * time.Hour).Format("2006-01-02 15:04:05"),
	})
	if err != nil {
		t.Fatalf("rename should be allowed: %v", err)
	}
	var name string
	if err := fx.db.QueryRow(`select name from bp_redeem_code_type where id=?`, typeID).
		Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != "冻结类型-改名" {
		t.Fatalf("name = %q", name)
	}

	// 类型标识撞名：409。
	err = fx.svc.SaveType(fx.ctx, integrationAdmin, 0, redeem.SaveTypeRequest{
		Code: "IT-FREEZE", Name: "重复", RewardType: redeem.RewardTypePoints,
		RewardPoints: 1,
		StartAt:      time.Now().Add(-24 * time.Hour).Format("2006-01-02 15:04:05"),
		EndAt:        time.Now().Add(30 * 24 * time.Hour).Format("2006-01-02 15:04:05"),
	})
	expectRedeemError(t, err, 409, "类型标识已被占用")
}

func TestGenerateCodesRejectsUnknownTypeIntegration(t *testing.T) {
	fx := setupRedeemIntegration(t)
	_, err := fx.svc.GenerateCodes(fx.ctx, integrationAdmin, 999999,
		redeem.GenerateCodesRequest{Count: 1}, integrationMeta)
	expectRedeemError(t, err, 400, "兑换码类型不存在")
}

// ---------------------------------------------------------------- 列表

func TestListTypesCodesAndRecordsIntegration(t *testing.T) {
	fx := setupRedeemIntegration(t)
	typeID := seedType(t, fx.db, typeSeed{
		code: "IT-LIST", name: "列类型", rewardType: redeem.RewardTypePoints, points: 100,
	})
	if _, err := fx.svc.GenerateCodes(fx.ctx, integrationAdmin, typeID,
		redeem.GenerateCodesRequest{Count: 3}, integrationMeta); err != nil {
		t.Fatalf("GenerateCodes: %v", err)
	}
	plains := codePlains(t, fx.db, typeID)
	user := seedUser(t, fx.db, "redeem_it_list_user")
	principal := userPrincipal(user, "redeem_it_list_user")
	if _, err := fx.svc.Redeem(fx.ctx, principal, plains[0], "10.12.0.1", redeem.SourceConsole); err != nil {
		t.Fatalf("Redeem: %v", err)
	}

	types, err := fx.svc.ListTypes(fx.ctx, integrationAdmin, redeem.ListParams{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListTypes: %v", err)
	}
	if types.Total != 1 || len(types.Items) != 1 {
		t.Fatalf("types total = %d items = %d", types.Total, len(types.Items))
	}
	item := types.Items[0]
	if item.GeneratedCount != 3 || item.RedeemedCount != 1 || !item.Frozen {
		t.Fatalf("type item = %+v", item)
	}

	codes, err := fx.svc.ListCodes(fx.ctx, integrationAdmin, redeem.ListParams{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListCodes: %v", err)
	}
	if codes.Total != 3 {
		t.Fatalf("codes total = %d, want 3", codes.Total)
	}
	for _, code := range codes.Items {
		if len(code.CodeMask) != 19 || contains(code.CodeMask, plains[0]) {
			t.Fatalf("code mask leaked plaintext: %q", code.CodeMask)
		}
	}

	records, err := fx.svc.ListRecords(fx.ctx, integrationAdmin, redeem.ListParams{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListRecords: %v", err)
	}
	if records.Total != 1 || len(records.Items) != 1 {
		t.Fatalf("records total = %d", records.Total)
	}
	if records.Items[0].Username != "redeem_it_list_user" || records.Items[0].RewardPoints != 100 {
		t.Fatalf("record = %+v", records.Items[0])
	}

	// 用户端视图只含本人。
	own, err := fx.svc.UserRecords(fx.ctx, principal, redeem.ListParams{Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("UserRecords: %v", err)
	}
	if own.Total != 1 || own.Items[0].Username != "" {
		t.Fatalf("own records = %+v", own)
	}
}
