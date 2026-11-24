package openapi

// 系统开放 API 域集成测试：OPENAPI_INTEGRATION=1 门控。
//
// 前置条件：
//   - 本地 MySQL（configs/config.yaml 或 DB_* 环境变量指向的实例）；
//   - 专用测试库 bugpk_openapi_test（库名须含 test，由 testdb 隔离守卫校验）；
//   - Redis 用进程内 miniredis（无需外部实例）。
//
// 覆盖设计文档第二节「判定链」与第四节「鉴权中间件」：
//   Key 生命周期（签发/列表/重置/吊销/槽位释放）、每账号单一有效 Key 的
//   生成列唯一键并发兜底、三层开关判定链（全局→管理员禁用→用户自助）、
//   Key 状态与过期、IP 白名单（CIDR / 单 IP / IPv4-mapped 归一 / 全网段拒绝）、
//   scope 门、频控（429 与 Redis 故障 fail-closed）、管理员作用域签发约束、
//   owner_mismatch、账号聚合。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/packagepkg"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/domain/redeem"
	userdomain "bugpk-api/internal/domain/user"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/testdb"

	"github.com/alicebob/miniredis/v2"
	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/golang-migrate/migrate/v4"
	migratedbmysql "github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------- 脚手架

const (
	openIntegrationDBName = "bugpk_openapi_test"
	openIntegrationPrefix = "openapi-it-"
	openIntegrationSecret = "openapi-integration-secret"
)

type openFixture struct {
	svc   *Service
	repo  *Repository
	authz *rbac.Service
	db    *sql.DB
	rdb   *redis.Client
	ctx   context.Context

	adminRoleID int64
	userRoleID  int64
}

func setupOpenIntegration(t *testing.T) *openFixture {
	t.Helper()
	if os.Getenv("OPENAPI_INTEGRATION") != "1" {
		t.Skip("set OPENAPI_INTEGRATION=1 to run openapi integration tests")
	}
	t.Setenv("DB_NAME", openIntegrationDBName)
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
	if err := runOpenIntegrationMigrations(t, db, "../../../migrations"); err != nil {
		t.Fatal(err)
	}
	resetOpenTables(t, db)

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	authz := rbac.NewService(rbac.NewRepository(db), nil)
	repo := NewRepository(db)
	svc := NewService(repo, authz, rdb, db, openIntegrationSecret)
	// 账号聚合需要 accounts / packages 端口；CDK 生成需要 redeem 端口（其
	// 编排层先判端口可用、再判管理员作用域）。gateway / payment 本文件不涉及。
	userSvc := userdomain.NewService(userdomain.NewRepository(db), nil, nil, nil, nil)
	packageSvc := packagepkg.NewService(packagepkg.NewRepository(db), authz)
	redeemSvc := redeem.NewService(redeem.NewRepository(db), authz, redeem.NewGuard(rdb, openIntegrationSecret), db)
	svc.SetSources(userSvc, packageSvc, nil, nil, redeemSvc)
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	return &openFixture{
		svc:         svc,
		repo:        repo,
		authz:       authz,
		db:          db,
		rdb:         rdb,
		ctx:         context.Background(),
		adminRoleID: roleIDByKey(t, db, "admin"),
		userRoleID:  roleIDByKey(t, db, "user"),
	}
}

// runOpenIntegrationMigrations 应用 migrations 目录。
// 本测试位于 openapi 包内，不能 import internal/app（app → openapi 成环），
// 故直接使用 golang-migrate（与 app.RunMigrations 同形）。
func runOpenIntegrationMigrations(t *testing.T, db *sql.DB, path string) error {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve migration path: %w", err)
	}
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration database connection: %w", err)
	}
	driver, err := migratedbmysql.WithConnection(ctx, conn, &migratedbmysql.Config{})
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("create migration mysql driver: %w", err)
	}
	migrator, err := migrate.NewWithDatabaseInstance("file://"+filepath.ToSlash(abs), "mysql", driver)
	if err != nil {
		_ = driver.Close()
		return fmt.Errorf("create migrator: %w", err)
	}
	migrator.LockTimeout = 60 * time.Second
	upErr := migrator.Up()
	sourceCloseErr, databaseCloseErr := migrator.Close()
	if upErr != nil && !errors.Is(upErr, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", upErr)
	}
	return errors.Join(sourceCloseErr, databaseCloseErr)
}

// resetOpenTables 复位全局配置并清空本域全部表与测试造数（专用测试库内安全）。
func resetOpenTables(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`update bp_open_api_config set enabled=0, ip_whitelist_max=10, key_default_expire_days=90,
		    admin_key_expire_days=30, rate_limit_normal=60, rate_limit_admin=30 where id=1`,
		`delete from bp_open_api_key_scope`,
		`delete from bp_open_api_key_ip_whitelist`,
		`delete from bp_open_api_key`,
		`delete from bp_user_package_global where user_id in (select id from bp_user where username like '` + openIntegrationPrefix + `%')`,
		`delete from bp_user_package_interface where user_id in (select id from bp_user where username like '` + openIntegrationPrefix + `%')`,
		`delete from bp_user_role where user_id in (select id from bp_user where username like '` + openIntegrationPrefix + `%')`,
		`delete from bp_user where username like '` + openIntegrationPrefix + `%'`,
		`delete rm from bp_role_menu rm join bp_role r on r.id = rm.role_id where r.role_key like '` + openIntegrationPrefix + `%'`,
		`delete from bp_role where role_key like '` + openIntegrationPrefix + `%'`,
		`delete from bp_package_global where name like 'OI-%'`,
		`delete from bp_redeem_code_type where code like 'OI-%'`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("reset %q: %v", stmt, err)
		}
	}
}

func roleIDByKey(t *testing.T, db *sql.DB, key string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`select id from bp_role where role_key=?`, key).Scan(&id); err != nil {
		t.Fatalf("query %s role: %v", key, err)
	}
	return id
}

// seedOpenRole 造一个只带指定菜单权限的角色（用于确定性验证 scope 派生，
// 避免依赖内置 user/admin 角色随迁移演进的权限集合）。
func seedOpenRole(t *testing.T, db *sql.DB, key string, menuIDs ...int64) int64 {
	t.Helper()
	result, err := db.Exec(`
		insert into bp_role (role_key, role_name, remark, status) values (?, ?, 'openapi integration', 1)
	`, key, key)
	if err != nil {
		t.Fatalf("seed role %s: %v", key, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	for _, menuID := range menuIDs {
		if _, err := db.Exec(`insert into bp_role_menu (role_id, menu_id) values (?,?)`, id, menuID); err != nil {
			t.Fatalf("assign menu %d to %s: %v", menuID, key, err)
		}
	}
	return id
}

// menuIDByAuthCode 取某权限码对应的菜单 id。
func menuIDByAuthCode(t *testing.T, db *sql.DB, authCode string) int64 {
	t.Helper()
	var id int64
	if err := db.QueryRow(`select id from bp_menu where auth_code=? and status=1 limit 1`, authCode).Scan(&id); err != nil {
		t.Fatalf("query menu %s: %v", authCode, err)
	}
	return id
}

// seedOpenUser 造一个账号并绑定角色；balance / points 用固定值便于断言聚合。
func seedOpenUser(t *testing.T, db *sql.DB, name string, roleID int64, enabled, adminDisabled bool) int64 {
	t.Helper()
	result, err := db.Exec(`
		insert into bp_user (username, password_hash, display_name, home_path, balance, points, status, token_version,
		                     open_api_enabled, open_api_admin_disabled)
		values (?, 'integration-placeholder', ?, '/console/workspace', 12.5000, 500, 1, 0, ?, ?)
	`, name, name, boolToInt(enabled), boolToInt(adminDisabled))
	if err != nil {
		t.Fatalf("seed user %s: %v", name, err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if roleID > 0 {
		if _, err := db.Exec(`insert into bp_user_role (user_id, role_id) values (?,?)`, id, roleID); err != nil {
			t.Fatalf("assign role to %s: %v", name, err)
		}
	}
	return id
}

func (f *openFixture) newUser(t *testing.T, suffix string) (int64, Principal) {
	t.Helper()
	name := openIntegrationPrefix + suffix
	id := seedOpenUser(t, f.db, name, f.userRoleID, true, false)
	return id, Principal{ID: id, Username: name, Roles: []string{"user"}}
}

func (f *openFixture) newAdmin(t *testing.T, suffix string) (int64, Principal) {
	t.Helper()
	name := openIntegrationPrefix + suffix
	id := seedOpenUser(t, f.db, name, f.adminRoleID, true, false)
	return id, Principal{ID: id, Username: name, Roles: []string{"admin"}}
}

func (f *openFixture) issue(t *testing.T, p Principal, req IssueKeyRequest) IssuedKey {
	t.Helper()
	issued, err := f.svc.IssueKey(f.ctx, p, req)
	if err != nil {
		t.Fatalf("issue key for #%d: %v", p.ID, err)
	}
	return issued
}

// enableGlobal 打开全局开关（admin 角色绕过权限码校验）。
func (f *openFixture) enableGlobal(t *testing.T) {
	t.Helper()
	if err := f.svc.UpdateConfig(f.ctx, Principal{Roles: []string{"admin"}}, ConfigRequest{Enabled: boolPtr(true)}); err != nil {
		t.Fatalf("enable global open api: %v", err)
	}
}

// runOpenChain 手工串联鉴权链：OpenAuthMiddleware → RequireScope(可选) → 业务桩。
// 客户端 IP 由 SetClientIPFunc 固定，避免依赖真实连接。
func runOpenChain(t *testing.T, svc *Service, secret, method, uri, clientIP, scopeCode string) *hertzapp.RequestContext {
	t.Helper()
	ctx := hertzapp.NewContext(0)
	ctx.SetClientIPFunc(func(*hertzapp.RequestContext) string { return clientIP })
	ctx.Request.SetRequestURI(uri)
	ctx.Request.SetMethod(method)
	if secret != "" {
		ctx.Request.Header.Set(headerOpenKey, secret)
	}
	chain := hertzapp.HandlersChain{svc.OpenAuthMiddleware()}
	if scopeCode != "" {
		chain = append(chain, RequireScope(scopeCode))
	}
	chain = append(chain, func(_ context.Context, c *hertzapp.RequestContext) {
		c.String(consts.StatusOK, "ok")
	})
	ctx.SetHandlers(chain)
	ctx.Next(context.Background())
	return ctx
}

// openCode 读鉴权链写入的稳定错误码头（仅 4xx 会带；5xx 按仓库策略抹平）。
func openCode(ctx *hertzapp.RequestContext) string {
	return ctx.Response.Header.Get("X-Bugpk-Openapi-Code")
}

func expectCode(t *testing.T, ctx *hertzapp.RequestContext, status int, code string) {
	t.Helper()
	if got := ctx.Response.StatusCode(); got != status {
		t.Fatalf("status = %d, want %d (body=%s)", got, status, ctx.Response.Body())
	}
	if got := openCode(ctx); got != code {
		t.Fatalf("openapi code = %q, want %q", got, code)
	}
}

func expectOK(t *testing.T, ctx *hertzapp.RequestContext) {
	t.Helper()
	if got := ctx.Response.StatusCode(); got != consts.StatusOK {
		t.Fatalf("status = %d, want 200 (code=%q body=%s)", got, openCode(ctx), ctx.Response.Body())
	}
}

func expectOpenErr(t *testing.T, err error, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s, got nil", code)
	}
	got, ok := errDetails(err)
	if !ok || got.Code != code {
		t.Fatalf("error = %v, want code %s", err, code)
	}
}

func expectOpenStatus(t *testing.T, err error, status int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with status %d, got nil", status)
	}
	var business *apperrors.BusinessError
	if !errors.As(err, &business) || business.StatusCode != status {
		t.Fatalf("error = %v, want status %d", err, status)
	}
}

func mustID(t *testing.T, text string) int64 {
	t.Helper()
	id, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
	if err != nil || id <= 0 {
		t.Fatalf("invalid id %q", text)
	}
	return id
}

func boolPtr(v bool) *bool { return &v }
func intPtr(v int) *int    { return &v }

// ---------------------------------------------------------------- Key 生命周期

func TestOpenIntegrationKeyLifecycle(t *testing.T) {
	f := setupOpenIntegration(t)
	f.enableGlobal(t)
	userID, p := f.newUser(t, "life")

	first := f.issue(t, p, IssueKeyRequest{})
	if !strings.HasPrefix(first.SecretKey, KeyMarkerNormal) {
		t.Fatalf("normal key marker missing: %q", first.SecretKey)
	}
	if !strings.Contains(first.MaskedKey, "****") || strings.Contains(first.MaskedKey, keyBody(first.SecretKey)) {
		t.Fatalf("masked key leaks body: %q", first.MaskedKey)
	}
	if len(first.Scopes) == 0 {
		t.Fatal("default scopes should be derived from the holder's live RBAC")
	}
	if first.IsAdminScope {
		t.Fatal("self-service key must not be admin scope")
	}

	// 每账号仅 1 个有效 Key：第二次直接签发被预检拒绝。
	_, err := f.svc.IssueKey(f.ctx, p, IssueKeyRequest{})
	expectOpenErr(t, err, CodeKeyLimit)

	items, total, err := f.svc.ListKeys(f.ctx, p, listParams{})
	if err != nil {
		t.Fatalf("list keys: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].ID != first.ID {
		t.Fatalf("self list = %d/%d items, want the single issued key", total, len(items))
	}

	// 重置：旧行吊销 + 新行同配置，明文必须轮换。
	firstID := mustID(t, first.ID)
	second, err := f.svc.ResetKey(f.ctx, p, firstID)
	if err != nil {
		t.Fatalf("reset key: %v", err)
	}
	if second.ID == first.ID {
		t.Fatal("reset must issue a new key row")
	}
	if second.SecretKey == first.SecretKey {
		t.Fatal("reset must rotate the plaintext secret")
	}
	old, err := f.repo.KeyByID(f.ctx, firstID, 0)
	if err != nil {
		t.Fatalf("reload old key: %v", err)
	}
	if old.Status != KeyStatusRevoked || !old.RevokedAt.Valid {
		t.Fatalf("old key status=%d revokedAt=%v, want revoked with timestamp", old.Status, old.RevokedAt)
	}

	// 吊销新 Key；重复吊销返回稳定码，而非静默成功。
	secondID := mustID(t, second.ID)
	if err := f.svc.RevokeKey(f.ctx, p, secondID); err != nil {
		t.Fatalf("revoke key: %v", err)
	}
	expectOpenErr(t, f.svc.RevokeKey(f.ctx, p, secondID), CodeKeyRevoked)

	// 吊销释放唯一键槽位：可再次签发。
	third := f.issue(t, p, IssueKeyRequest{})
	if third.ID == second.ID {
		t.Fatal("revocation must free the single-active-key slot")
	}
	var active int64
	if err := f.db.QueryRow(`select count(*) from bp_open_api_key where user_id=? and status=1`, userID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active keys = %d, want exactly 1", active)
	}
}

func TestOpenIntegrationConcurrentIssueKeepsSingleActiveKey(t *testing.T) {
	f := setupOpenIntegration(t)
	f.enableGlobal(t)
	userID, p := f.newUser(t, "race")

	const workers = 8
	results := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(slot int) {
			defer wg.Done()
			_, results[slot] = f.svc.IssueKey(f.ctx, p, IssueKeyRequest{})
		}(i)
	}
	wg.Wait()

	var okCount, limitCount int
	for _, err := range results {
		switch {
		case err == nil:
			okCount++
		default:
			if code, ok := errDetails(err); ok && code.Code == CodeKeyLimit {
				limitCount++
				continue
			}
			t.Fatalf("unexpected issue error: %v", err)
		}
	}
	if okCount != 1 || limitCount != workers-1 {
		t.Fatalf("ok=%d limit=%d, want ok=1 limit=%d (生成列唯一键必须拦住并发)", okCount, limitCount, workers-1)
	}
	var active int64
	if err := f.db.QueryRow(`select count(*) from bp_open_api_key where user_id=? and status=1`, userID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("active keys = %d, want 1", active)
	}
}

// ---------------------------------------------------------------- 三层开关判定链

func TestOpenIntegrationMiddlewareSwitchChain(t *testing.T) {
	f := setupOpenIntegration(t)
	_, adminP := f.newAdmin(t, "switch-admin")
	userID, userP := f.newUser(t, "switch-user")

	const uri = "/open-api/account"

	// 0) 全局关闭时连签发都不允许：关闭功能应当是不发放新凭据，而不是
	//    只让发出去的凭据打不通。
	if _, err := f.svc.IssueKey(f.ctx, userP, IssueKeyRequest{}); err == nil {
		t.Fatal("issuance accepted while the global switch is off")
	} else {
		expectOpenErr(t, err, CodeDisabled)
	}

	f.enableGlobal(t)
	issued := f.issue(t, userP, IssueKeyRequest{IPWhitelist: []string{"203.0.113.9"}})

	// 1) 关闭全局后，已签发的 Key 运行期立即拒绝。
	if err := f.svc.UpdateConfig(f.ctx, Principal{Roles: []string{"admin"}}, ConfigRequest{Enabled: boolPtr(false)}); err != nil {
		t.Fatalf("disable global: %v", err)
	}
	expectCode(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead),
		consts.StatusForbidden, CodeDisabled)

	// 2) 开启全局后放行。
	f.enableGlobal(t)
	expectOK(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead))

	// 3) 用户自助关闭（实时判定，不吊销 Key）。
	if err := f.svc.SetSelfSwitch(f.ctx, userP, false); err != nil {
		t.Fatalf("self switch off: %v", err)
	}
	// 自助开关的读视图必须反映真实状态（前端页面初始值同源）。
	if status, err := f.svc.SelfSwitchStatus(f.ctx, userP); err != nil || status.Enabled {
		t.Fatalf("switch status after self-off = %+v/%v, want enabled=false", status, err)
	}
	expectCode(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead),
		consts.StatusForbidden, CodeUserDisabled)

	// 4) 用户恢复后重新可用。
	if err := f.svc.SetSelfSwitch(f.ctx, userP, true); err != nil {
		t.Fatalf("self switch on: %v", err)
	}
	expectOK(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead))

	// 5) 管理员强制禁用优先级最高。
	if err := f.svc.SetUserDisabled(f.ctx, adminP, userID, true); err != nil {
		t.Fatalf("admin disable: %v", err)
	}
	if status, err := f.svc.SelfSwitchStatus(f.ctx, userP); err != nil || !status.AdminDisabled {
		t.Fatalf("switch status after admin disable = %+v/%v, want adminDisabled=true", status, err)
	}
	expectCode(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead),
		consts.StatusForbidden, CodeAdminDisabled)

	// 6) 解除后恢复。
	if err := f.svc.SetUserDisabled(f.ctx, adminP, userID, false); err != nil {
		t.Fatalf("admin re-enable: %v", err)
	}
	expectOK(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead))
}

// TestOpenIntegrationMiddlewareAccountStatusGate 覆盖「账号封禁」这道独立门：
// 封禁即切断开放 API 入口，不依赖开放 API 自身三层开关；且过期临时封禁
// 借助共享表达式 EffectiveStatusSQL 在线自动恢复，无需回写 status。
func TestOpenIntegrationMiddlewareAccountStatusGate(t *testing.T) {
	f := setupOpenIntegration(t)
	f.enableGlobal(t)
	userID, userP := f.newUser(t, "acct-gate")
	issued := f.issue(t, userP, IssueKeyRequest{IPWhitelist: []string{"203.0.113.9"}})
	const uri = "/open-api/account"

	// 1) 基线：正常账号放行。
	expectOK(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead))

	// 2) 永久封禁（status=0 + ban_permanent=1）→ 账号封禁门拒绝。
	if _, err := f.db.Exec(`
		update bp_user set status=0, ban_permanent=1, ban_reason='integration' where id=?
	`, userID); err != nil {
		t.Fatalf("permanent ban: %v", err)
	}
	expectCode(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead),
		consts.StatusForbidden, CodeAccountDisabled)

	// 3) 已过期的临时封禁（ban_until 早于 now）→ EffectiveStatusSQL 视为有效，放行。
	if _, err := f.db.Exec(`
		update bp_user set status=0, ban_permanent=0, ban_until=date_sub(now(), interval 1 second) where id=?
	`, userID); err != nil {
		t.Fatalf("expired temporary ban: %v", err)
	}
	expectOK(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead))

	// 4) 未过期的临时封禁（ban_until 晚于 now）→ 仍视为被封禁，拒绝。
	if _, err := f.db.Exec(`
		update bp_user set status=0, ban_permanent=0, ban_until=date_add(now(), interval 1 minute) where id=?
	`, userID); err != nil {
		t.Fatalf("active temporary ban: %v", err)
	}
	expectCode(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead),
		consts.StatusForbidden, CodeAccountDisabled)
}

// TestOpenIntegrationAccountStatusGatePrecedence 对抗性覆盖：账号封禁门
// 与相邻门的**优先次序**，以及「封禁状态」判定的边界数据形态。
//
// 依据 middleware.go 的四层开关顺序 全局 → 账号封禁 → 管理员禁用 → 用户自助：
//   - 全局关闭优先级最高（排在账号封禁之前）；
//   - 账号封禁优先于管理员禁用与用户自助；
//   - EffectiveStatusSQL 只在 status=0 且「临时封禁已到期」时才回落为有效，
//     ban_permanent=1 或 ban_until=NULL 的畸形行必须仍视为封禁。
func TestOpenIntegrationAccountStatusGatePrecedence(t *testing.T) {
	f := setupOpenIntegration(t)
	f.enableGlobal(t)
	userID, userP := f.newUser(t, "gate-order")
	issued := f.issue(t, userP, IssueKeyRequest{})
	const uri = "/open-api/account"

	// (1) 全局关闭 + 账号封禁 同时成立 → 全局门在前，先返回 disabled。
	if _, err := f.db.Exec(`update bp_user set status=0, ban_permanent=1, ban_reason='x' where id=?`, userID); err != nil {
		t.Fatalf("ban user: %v", err)
	}
	if err := f.svc.UpdateConfig(f.ctx, Principal{Roles: []string{"admin"}}, ConfigRequest{Enabled: boolPtr(false)}); err != nil {
		t.Fatalf("disable global: %v", err)
	}
	expectCode(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead),
		consts.StatusForbidden, CodeDisabled)
	f.enableGlobal(t)

	// (2) 账号封禁 优先于 管理员强制禁用：同时置位时应报 account_disabled。
	if err := f.svc.SetUserDisabled(f.ctx, Principal{Roles: []string{"admin"}}, userID, true); err != nil {
		t.Fatalf("admin disable: %v", err)
	}
	expectCode(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead),
		consts.StatusForbidden, CodeAccountDisabled)
	if err := f.svc.SetUserDisabled(f.ctx, Principal{Roles: []string{"admin"}}, userID, false); err != nil {
		t.Fatalf("admin re-enable: %v", err)
	}

	// (3) 账号封禁 优先于 用户自助开关：把自助开关显式置为开启后仍不放行。
	//     注意 seedOpenUser 默认 open_api_enabled=1，直接置 true 是 no-op 会被
	//     requireAffected 拒绝，故先关再开以产生两次真实变更。
	if err := f.svc.SetSelfSwitch(f.ctx, userP, false); err != nil {
		t.Fatalf("self switch off: %v", err)
	}
	if err := f.svc.SetSelfSwitch(f.ctx, userP, true); err != nil {
		t.Fatalf("self switch on: %v", err)
	}
	expectCode(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead),
		consts.StatusForbidden, CodeAccountDisabled)

	// (4) 边界：status=0 + ban_permanent=0 + ban_until=NULL（畸形行）必须仍视为封禁，
	//     因为 EffectiveStatusSQL 的 case 要求 ban_until 非空才可能回落有效。
	if _, err := f.db.Exec(`update bp_user set status=0, ban_permanent=0, ban_until=NULL where id=?`, userID); err != nil {
		t.Fatalf("malformed ban row: %v", err)
	}
	expectCode(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead),
		consts.StatusForbidden, CodeAccountDisabled)

	// (5) 边界：ban_until 恰为当下（<= now()）→ 临时封禁已到期，回落有效、放行。
	if _, err := f.db.Exec(`update bp_user set status=0, ban_permanent=0, ban_until=now() where id=?`, userID); err != nil {
		t.Fatalf("boundary ban row: %v", err)
	}
	expectOK(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead))

	// (6) 恢复为正常账号后放行，确认新门不会误伤。
	if _, err := f.db.Exec(`update bp_user set status=1, ban_permanent=0, ban_until=NULL, ban_reason='' where id=?`, userID); err != nil {
		t.Fatalf("unban: %v", err)
	}
	expectOK(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead))
}

func TestOpenIntegrationMiddlewareKeyState(t *testing.T) {
	f := setupOpenIntegration(t)
	f.enableGlobal(t)
	_, userP := f.newUser(t, "state")
	issued := f.issue(t, userP, IssueKeyRequest{})
	const uri = "/open-api/account"

	// 缺头 → key_missing（凭据只认请求头）。
	expectCode(t, runOpenChain(t, f.svc, "", "GET", uri, "203.0.113.9", EndpointAccountRead),
		consts.StatusUnauthorized, CodeKeyMissing)
	// 未知明文 → key_invalid。
	expectCode(t, runOpenChain(t, f.svc, "opk_"+strings.Repeat("dead", 16), "GET", uri, "203.0.113.9", EndpointAccountRead),
		consts.StatusUnauthorized, CodeKeyInvalid)

	// 吊销 → key_revoked。
	keyID := mustID(t, issued.ID)
	if err := f.svc.RevokeKey(f.ctx, userP, keyID); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	expectCode(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead),
		consts.StatusUnauthorized, CodeKeyRevoked)

	// 过期 → key_expired（直接种一行，绕开发签期默认有效期）。
	_, expiredP := f.newUser(t, "expired")
	secret := KeyMarkerNormal + strings.Repeat("b", 64)
	if _, err := f.db.Exec(`
		insert into bp_open_api_key (user_id, name, key_hash, key_prefix, key_suffix, is_admin_scope, status, expires_at)
		values (?, ?, ?, ?, ?, 0, 1, ?)
	`, expiredP.ID, "expired", hashKey(secret), "bbbb", "bbbb", time.Now().Add(-time.Hour)); err != nil {
		t.Fatalf("seed expired key: %v", err)
	}
	expectCode(t, runOpenChain(t, f.svc, secret, "GET", uri, "203.0.113.9", EndpointAccountRead),
		consts.StatusUnauthorized, CodeKeyExpired)
}

// ---------------------------------------------------------------- IP 白名单

func TestOpenIntegrationMiddlewareIPWhitelist(t *testing.T) {
	f := setupOpenIntegration(t)
	f.enableGlobal(t)
	const uri = "/open-api/account"

	_, cidrUser := f.newUser(t, "ip-cidr")
	cidrKey := f.issue(t, cidrUser, IssueKeyRequest{
		Scopes:      []string{EndpointAccountRead},
		IPWhitelist: []string{"203.0.113.0/24"},
	})
	if len(cidrKey.IPWhitelist) != 1 || cidrKey.IPWhitelist[0] != "203.0.113.0/24" {
		t.Fatalf("cidr rule not normalized: %v", cidrKey.IPWhitelist)
	}
	expectOK(t, runOpenChain(t, f.svc, cidrKey.SecretKey, "GET", uri, "203.0.113.77", EndpointAccountRead))
	expectCode(t, runOpenChain(t, f.svc, cidrKey.SecretKey, "GET", uri, "198.51.100.7", EndpointAccountRead),
		consts.StatusForbidden, CodeIPForbidden)

	// IPv4-mapped 写法必须折叠后命中，防同一地址两种写法绕过白名单。
	_, mappedUser := f.newUser(t, "ip-mapped")
	mappedKey := f.issue(t, mappedUser, IssueKeyRequest{
		Scopes:      []string{EndpointAccountRead},
		IPWhitelist: []string{"203.0.113.9"},
	})
	expectOK(t, runOpenChain(t, f.svc, mappedKey.SecretKey, "GET", uri, "::ffff:203.0.113.9", EndpointAccountRead))

	// 全网段等于「不限」，签发期必须拒绝。
	_, fullUser := f.newUser(t, "ip-full")
	_, err := f.svc.IssueKey(f.ctx, fullUser, IssueKeyRequest{IPWhitelist: []string{"0.0.0.0/0"}})
	expectOpenStatus(t, err, consts.StatusBadRequest)
	_, err = f.svc.IssueKey(f.ctx, fullUser, IssueKeyRequest{IPWhitelist: []string{"::/0"}})
	expectOpenStatus(t, err, consts.StatusBadRequest)
}

// ---------------------------------------------------------------- scope 门

func TestOpenIntegrationMiddlewareScopeGate(t *testing.T) {
	f := setupOpenIntegration(t)
	f.enableGlobal(t)

	// 专用角色：只授自助读（openapi:self:read），因此能签发 Key，但默认派生
	// scope 不含 key:create / key:list / redeem:code:generate 三项。
	selfRole := seedOpenRole(t, f.db, openIntegrationPrefix+"selfonly", menuIDByAuthCode(t, f.db, "openapi:self:read"))
	name := openIntegrationPrefix + "scope"
	userID := seedOpenUser(t, f.db, name, selfRole, true, false)
	userP := Principal{ID: userID, Username: name, Roles: []string{openIntegrationPrefix + "selfonly"}}
	issued := f.issue(t, userP, IssueKeyRequest{IPWhitelist: []string{"203.0.113.9"}})

	// 派生默认值：无权限要求的端点被授予。
	if !containsCode(issued.Scopes, EndpointAccountRead) || !containsCode(issued.Scopes, EndpointPackageRead) {
		t.Fatalf("derived scopes missing open endpoints: %v", issued.Scopes)
	}
	// 未持有 key:create → api-key:write 不在清单内。
	if containsCode(issued.Scopes, EndpointAPIKeyWrite) {
		t.Fatalf("derived scopes must not include api-key:write without key:create: %v", issued.Scopes)
	}
	expectOK(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", "/open-api/account", "203.0.113.9", EndpointAccountRead))
	expectCode(t, runOpenChain(t, f.svc, issued.SecretKey, "POST", "/open-api/api-keys", "203.0.113.9", EndpointAPIKeyWrite),
		consts.StatusForbidden, CodeScopeDenied)

	// 显式只授 account:read：packages 端点被 scope 门拒绝。
	_, limitedP := f.newUser(t, "scope-limited")
	limited := f.issue(t, limitedP, IssueKeyRequest{
		Scopes:      []string{EndpointAccountRead},
		IPWhitelist: []string{"203.0.113.9"},
	})
	expectOK(t, runOpenChain(t, f.svc, limited.SecretKey, "GET", "/open-api/account", "203.0.113.9", EndpointAccountRead))
	expectCode(t, runOpenChain(t, f.svc, limited.SecretKey, "GET", "/open-api/account/packages", "203.0.113.9", EndpointPackageRead),
		consts.StatusForbidden, CodeScopeDenied)
}

func containsCode(codes []string, want string) bool {
	for _, code := range codes {
		if code == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- 频控

func TestOpenIntegrationRateLimit(t *testing.T) {
	f := setupOpenIntegration(t)
	adminP := Principal{ID: 1, Username: "seed-admin", Roles: []string{"admin"}}
	if err := f.svc.UpdateConfig(f.ctx, adminP, ConfigRequest{Enabled: boolPtr(true), RateLimitNormal: intPtr(2)}); err != nil {
		t.Fatalf("set rate limit: %v", err)
	}
	_, userP := f.newUser(t, "rate")
	issued := f.issue(t, userP, IssueKeyRequest{IPWhitelist: []string{"203.0.113.9"}})
	const uri = "/open-api/account"

	for i := 0; i < 2; i++ {
		expectOK(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead))
	}
	expectCode(t, runOpenChain(t, f.svc, issued.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead),
		consts.StatusTooManyRequests, CodeRateLimited)

	// Redis 故障 fail-closed：绝不放行（5xx 按仓库策略抹平为统一 503 体，故断言状态码）。
	_, failUser := f.newUser(t, "rate-fail")
	failKey := f.issue(t, failUser, IssueKeyRequest{IPWhitelist: []string{"203.0.113.9"}})
	broken := NewService(f.repo, f.authz, nil, f.db, openIntegrationSecret)
	broken.SetSources(nil, nil, nil, nil, nil)
	failCtx := runOpenChain(t, broken, failKey.SecretKey, "GET", uri, "203.0.113.9", EndpointAccountRead)
	if got := failCtx.Response.StatusCode(); got != consts.StatusServiceUnavailable {
		t.Fatalf("redis-down status = %d, want 503 (fail-closed)", got)
	}
}

// ---------------------------------------------------------------- 管理员作用域

func TestOpenIntegrationAdminScopeIssuance(t *testing.T) {
	f := setupOpenIntegration(t)
	f.enableGlobal(t)
	adminID, adminP := f.newAdmin(t, "admin-scope")
	userID, userP := f.newUser(t, "admin-scope-target")
	adminScopes := []string{EndpointRedeemCodeGenerate}

	// 普通用户签发管理员作用域 → 拒绝。
	_, err := f.svc.IssueKey(f.ctx, userP, IssueKeyRequest{Scopes: adminScopes})
	expectOpenErr(t, err, CodeAdminScopeDenied)

	// 管理员为「无管理员角色」的账号签发 → 拒绝。
	_, err = f.svc.IssueKey(f.ctx, adminP, IssueKeyRequest{
		UserID: userID, Scopes: adminScopes, IPWhitelist: []string{"203.0.113.9"},
	})
	expectOpenErr(t, err, CodeAdminScopeDenied)

	// 管理员为管理员账号签发但缺白名单 → 强制非空。
	_, err = f.svc.IssueKey(f.ctx, adminP, IssueKeyRequest{UserID: adminID, Scopes: adminScopes})
	expectOpenErr(t, err, CodeIPWhitelistRequired)

	// 合法签发：opka_ 前缀 + IsAdminScope 派生。
	issued := f.issue(t, adminP, IssueKeyRequest{
		UserID:      adminID,
		Scopes:      append(adminScopes, EndpointAccountRead),
		IPWhitelist: []string{"203.0.113.0/24"},
	})
	if !strings.HasPrefix(issued.SecretKey, KeyMarkerAdmin) {
		t.Fatalf("admin scope key marker missing: %q", issued.SecretKey)
	}
	if !issued.IsAdminScope {
		t.Fatal("is_admin_scope must be derived from the scope list")
	}
	if !strings.HasPrefix(issued.MaskedKey, KeyMarkerAdmin) {
		t.Fatalf("masked admin key must keep opka_ marker: %q", issued.MaskedKey)
	}
}

func TestOpenIntegrationOwnerMismatchAndCrossAccountGate(t *testing.T) {
	f := setupOpenIntegration(t)
	f.enableGlobal(t)
	_, ownerP := f.newUser(t, "owner")
	_, strangerP := f.newUser(t, "stranger")
	issued := f.issue(t, ownerP, IssueKeyRequest{})
	keyID := mustID(t, issued.ID)

	// 非管理员操作他人 Key → owner_mismatch（而非 404，避免探测）。
	expectOpenErr(t, f.svc.RevokeKey(f.ctx, strangerP, keyID), CodeOwnerMismatch)
	_, err := f.svc.ResetKey(f.ctx, strangerP, keyID)
	expectOpenErr(t, err, CodeOwnerMismatch)

	// 跨账号列表需要 openapi:key:list，普通用户不具备 → 403（rbac 抛错）。
	_, _, err = f.svc.ListKeys(f.ctx, strangerP, listParams{Scope: "all"})
	expectOpenStatus(t, err, consts.StatusForbidden)

	// 管理员跨账号可见且带 username。
	_, adminP := f.newAdmin(t, "owner-admin")
	items, total, err := f.svc.ListKeys(f.ctx, adminP, listParams{Scope: "all"})
	if err != nil {
		t.Fatalf("admin cross-account list: %v", err)
	}
	if total < 1 {
		t.Fatalf("admin list total = %d, want >=1", total)
	}
	found := false
	for _, item := range items {
		if item.ID == issued.ID {
			found = true
			if item.Username == "" {
				t.Fatal("admin view must include username")
			}
		}
	}
	if !found {
		t.Fatal("admin cross-account list did not include the issued key")
	}
}

// ---------------------------------------------------------------- 编排层守卫

func TestOpenIntegrationGenerateCDKsRequiresAdminScope(t *testing.T) {
	f := setupOpenIntegration(t)
	userID, _ := f.newUser(t, "gen-guard")
	// 伪造一个「带 admin 端点 scope 但 IsAdminScope=false」的身份，
	// 验证编排层自身也拦（不能只依赖中间件的 is_admin_scope + scope 清单）。
	_, err := f.svc.GenerateCDKs(f.ctx, OpenPrincipal{
		KeyID:        1,
		UserID:       userID,
		IsAdminScope: false,
		Scopes:       map[string]struct{}{EndpointRedeemCodeGenerate: {}},
	}, GenerateCodesRequest{TypeID: 1, Count: 1})
	expectOpenErr(t, err, CodeScopeDenied)
}

// TestOpenIntegrationResolveRedeemTypeRef 覆盖外部插件的类型引用解析：
// typeId 优先、typeCode 兜底（AstrBot 活动配置里写的是标识），
// 两者都缺/都查不到必须是 4xx 稳定码而不是领域错误直出。
func TestOpenIntegrationResolveRedeemTypeRef(t *testing.T) {
	f := setupOpenIntegration(t)
	if _, err := f.db.Exec(`
		insert into bp_redeem_code_type
			(code, name, reward_type, reward_points, reward_balance, reward_ref_id,
			 per_user_limit, per_ip_limit, start_at, end_at, status)
		values ('OI-NEWBIE', '开放活动新手', 1, 100, 0, 0,
		        1, 0, date_sub(now(), interval 1 day), date_add(now(), interval 1 day), 1)
	`); err != nil {
		t.Fatalf("seed redeem type: %v", err)
	}

	// typeId 优先：即便 typeCode 同时给出也不做二次解析。
	if id, err := f.svc.resolveRedeemTypeRef(f.ctx, GenerateCodesRequest{TypeID: 7, TypeCode: "OI-NEWBIE"}); err != nil || id != 7 {
		t.Fatalf("typeId precedence: id=%d err=%v", id, err)
	}

	id, err := f.svc.resolveRedeemTypeRef(f.ctx, GenerateCodesRequest{TypeCode: "  OI-NEWBIE "})
	if err != nil || id == 0 {
		t.Fatalf("typeCode resolve: id=%d err=%v", id, err)
	}

	for name, req := range map[string]GenerateCodesRequest{
		"两者都缺":  {Count: 1},
		"空白标识":  {TypeCode: "  "},
		"标识不存在": {TypeCode: "OI-NO-SUCH"},
	} {
		_, err := f.svc.resolveRedeemTypeRef(f.ctx, req)
		if err == nil {
			t.Fatalf("%s 必须失败", name)
		}
		want := CodeInvalidRequest
		if name == "标识不存在" {
			want = CodeTypeNotFound
		}
		expectOpenErr(t, err, want)
	}
}

// ---------------------------------------------------------------- 账号聚合

func TestOpenIntegrationAccountInfoAggregation(t *testing.T) {
	f := setupOpenIntegration(t)
	f.enableGlobal(t)
	userID, userP := f.newUser(t, "aggregate")
	issued := f.issue(t, userP, IssueKeyRequest{})
	principal := OpenPrincipal{
		KeyID:  mustID(t, issued.ID),
		UserID: userID,
		Scopes: map[string]struct{}{EndpointAccountRead: {}, EndpointPackageRead: {}},
	}

	// 名下 1 个有效全站套餐。
	now := time.Now()
	if _, err := f.db.Exec(`
		insert into bp_user_package_global
			(user_id, package_id, package_name, daily_limit, qps_limit, start_at, end_at, day_used, total_used, status)
		values (?, 0, 'OI-全站', 1000, 5, ?, ?, 12, 12, 1)
	`, userID, now.Add(-time.Hour), now.Add(24*time.Hour)); err != nil {
		t.Fatalf("seed global package: %v", err)
	}

	info, err := f.svc.AccountInfo(f.ctx, principal)
	if err != nil {
		t.Fatalf("account info: %v", err)
	}
	if info.UserID != strconv.FormatInt(userID, 10) {
		t.Fatalf("userId = %q", info.UserID)
	}
	if info.Points != 500 {
		t.Fatalf("points = %d, want 500", info.Points)
	}
	if !strings.HasPrefix(info.Balance, "12.5") {
		t.Fatalf("balance = %q, want 12.5", info.Balance)
	}
	if !info.OpenAPIEnabled {
		t.Fatal("openApiEnabled should be true after issue")
	}
	if info.KeyStatus != "active" {
		t.Fatalf("keyStatus = %q, want active", info.KeyStatus)
	}
	if info.PackageCount.Global != 1 || info.PackageCount.Interface != 0 {
		t.Fatalf("packageCount = %+v, want global=1 interface=0", info.PackageCount)
	}

	page, err := f.svc.AccountPackages(f.ctx, principal, 1, 10)
	if err != nil {
		t.Fatalf("account packages: %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("packages total=%d items=%d, want 1/1", page.Total, len(page.Items))
	}
	if page.Items[0].PackageName != "OI-全站" || page.Items[0].DayUsed != 12 {
		t.Fatalf("unexpected package item: %+v", page.Items[0])
	}
}

// ---------------------------------------------------------------- 配置校验

func TestOpenIntegrationConfigValidation(t *testing.T) {
	f := setupOpenIntegration(t)
	adminP := Principal{Roles: []string{"admin"}}

	// 越界值必须被拒（且不落库）。
	cases := []ConfigRequest{
		{IPWhitelistMax: intPtr(-1)},
		{IPWhitelistMax: intPtr(maxIPWhitelistHard + 1)},
		{KeyDefaultExpireDays: intPtr(0)},
		{KeyDefaultExpireDays: intPtr(maxExpireInDays + 1)},
		{AdminKeyExpireDays: intPtr(0)},
		{RateLimitNormal: intPtr(-1)},
		{RateLimitAdmin: intPtr(-1)},
	}
	for _, req := range cases {
		if err := f.svc.UpdateConfig(f.ctx, adminP, req); err == nil {
			t.Fatalf("invalid config accepted: %+v", req)
		}
	}
	cfg, err := f.repo.Config(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled || cfg.IPWhitelistMax != 10 || cfg.KeyDefaultExpireDays != 90 {
		t.Fatalf("config mutated by rejected updates: %+v", cfg)
	}

	// 合法更新生效。
	if err := f.svc.UpdateConfig(f.ctx, adminP, ConfigRequest{Enabled: boolPtr(true), IPWhitelistMax: intPtr(20)}); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	cfg, err = f.repo.Config(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || cfg.IPWhitelistMax != 20 {
		t.Fatalf("valid config not applied: %+v", cfg)
	}
}

// ---------------------------------------------------------------- 白名单条数上限

func TestOpenIntegrationIPWhitelistOverflow(t *testing.T) {
	f := setupOpenIntegration(t)
	f.enableGlobal(t)
	adminP := Principal{Roles: []string{"admin"}}
	if err := f.svc.UpdateConfig(f.ctx, adminP, ConfigRequest{IPWhitelistMax: intPtr(2)}); err != nil {
		t.Fatalf("set ip whitelist max: %v", err)
	}
	_, userP := f.newUser(t, "ip-overflow")
	_, err := f.svc.IssueKey(f.ctx, userP, IssueKeyRequest{
		IPWhitelist: []string{"203.0.113.1", "203.0.113.2", "203.0.113.3"},
	})
	expectOpenErr(t, err, CodeIPWhitelistOverflow)

	// 去重后不超限即可放行。
	issued := f.issue(t, userP, IssueKeyRequest{
		IPWhitelist: []string{"203.0.113.1", "203.0.113.1", " 203.0.113.2 "},
	})
	if len(issued.IPWhitelist) != 2 {
		t.Fatalf("dedup failed: %v", issued.IPWhitelist)
	}
}

// ---------------------------------------------------------------- 自检

func TestOpenIntegrationFixtureIsIsolated(t *testing.T) {
	// 该用例刻意在未开启集成门控时也能静态自检：确保库名始终含 test。
	if err := testdb.ValidateIsolatedMySQLDSN(fmt.Sprintf("root:root@tcp(127.0.0.1:3306)/%s?parseTime=true", openIntegrationDBName)); err != nil {
		t.Fatalf("openapi integration target is not isolated: %v", err)
	}
}

// TestOpenIntegrationGenerateCDKReachability 论证审计发现二的可达性边界：
// 「CDK 生成不校验类型归属」在当前的单运营模型下不可达。 redeem:code:generate
// 权限码只由菜单 226 授予 role_id=1（系统管理员），且 EndpointRedeemCodeGenerate
// 被标为 AdminOnly——签发需双重管理员校验、运行时需 IsAdminScope 闸门。因此只有
// 系统管理员能持 admin 作用域 Key 调 GenerateCDKs；而单运营模型下 redeem 类型无
// owner 概念，管理员本就可操作全部类型。该缺陷目前不构成可利用漏洞。
func TestOpenIntegrationGenerateCDKReachability(t *testing.T) {
	f := setupOpenIntegration(t)
	f.enableGlobal(t)

	// 普通运营账号（非管理员角色）。
	userID, userP := f.newUser(t, "gen-reach")

	// 1) ScopeOptions 必须显示该端点 Granted=false：普通角色不持有
	//    redeem:code:generate 权限码，故派生不出该 scope。
	opts, err := f.svc.ScopeOptions(f.ctx, userP, userID)
	if err != nil {
		t.Fatalf("scope options: %v", err)
	}
	var gen *ScopeOption
	for i := range opts {
		if opts[i].Code == EndpointRedeemCodeGenerate {
			gen = &opts[i]
		}
	}
	if gen == nil {
		t.Fatal("EndpointRedeemCodeGenerate missing from scope options")
	}
	if !gen.AdminOnly {
		t.Fatal("EndpointRedeemCodeGenerate must be flagged AdminOnly")
	}
	if gen.Granted {
		t.Fatal("non-admin account granted redeem code generate endpoint: finding #2 IS reachable (regression)")
	}

	// 2) 非管理员即便显式请求该 scope，签发也会被双重 admin 校验拒绝。
	_, err = f.svc.IssueKey(f.ctx, userP, IssueKeyRequest{Scopes: []string{EndpointRedeemCodeGenerate}})
	expectOpenErr(t, err, CodeAdminScopeDenied)

	// 3) 即便构造带该 scope 的非 admin 作用域 Principal，编排层 GenerateCDKs
	//    也会先被 IsAdminScope 闸门挡下（orchestration.go:269）。
	nonAdmin := OpenPrincipal{
		KeyID:        1,
		UserID:       userID,
		IsAdminScope: false,
		Scopes:       map[string]struct{}{EndpointRedeemCodeGenerate: {}},
		ClientIP:     "203.0.113.7",
	}
	_, err = f.svc.GenerateCDKs(f.ctx, nonAdmin, GenerateCodesRequest{TypeID: 1, Count: 1})
	expectOpenErr(t, err, CodeScopeDenied)
}
