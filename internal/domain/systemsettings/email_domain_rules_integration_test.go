package systemsettings_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"bugpk-api/internal/app"
	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/domain/systemsettings"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/testdb"

	"github.com/go-sql-driver/mysql"
)

// newEmailDomainRepo 应用迁移并返回 systemsettings 仓储与 db 句柄，供邮箱域策略
// 集成用例复用。库必须含 test/e2e/bench 才通过 testdb 守卫。
func newEmailDomainRepo(t *testing.T) (*sql.DB, *systemsettings.Repository, context.Context) {
	t.Helper()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if err := testdb.ValidateIsolatedMySQLDSN(cfg.Database.DSN()); err != nil {
		t.Fatal(err)
	}
	cfg.Migration.Path = "../../../migrations"
	db, err := sql.Open("mysql", cfg.Database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := app.RunMigrations(db, cfg.Migration.Path); err != nil {
		t.Fatal(err)
	}
	return db, systemsettings.NewRepository(db), context.Background()
}

// newDomainRulesService 通过公开构造函数组装 Service。rbac.HasPermission 在
// principal.Roles 含 "admin" 时直接短路返回 true，不会触碰 repo/jwt 字段，
// 因此即便内部依赖为零值也能放行本测试使用的 admin Principal。
func newDomainRulesService(repo *systemsettings.Repository) *systemsettings.Service {
	return systemsettings.NewService(repo, rbac.NewService(nil, nil), "")
}

// TestIntegrationEmailDomainRulesAdminRoundTrip 锁死后台设置 → 读取 的端到端链路：
// 管理员通过 SaveEmailDomainRules 写入白名单后，Repository.EmailDomainRules
// 必须精确还原；白名单为空时不限制任何域名；重新保存为替换语义（idempotent replace）。
func TestIntegrationEmailDomainRulesAdminRoundTrip(t *testing.T) {
	if os.Getenv("SYSTEMSETTINGS_INTEGRATION") != "1" {
		t.Skip("set SYSTEMSETTINGS_INTEGRATION=1 to run")
	}
	db, repo, ctx := newEmailDomainRepo(t)

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `delete from bp_email_domain_rule`)
	})

	// 1) 空策略：任何域名都放行（与未配置时行为一致）
	rules, err := repo.EmailDomainRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules.Whitelist) != 0 || len(rules.Blacklist) != 0 {
		t.Fatalf("fresh rules = %+v, want both lists empty", rules)
	}

	// 2) 管理员保存白名单 + 黑名单
	if err := repo.SaveEmailDomainRules(ctx, systemsettings.EmailDomainRules{
		Whitelist: []string{"allowed.example", "partner.example"},
		Blacklist: []string{"spam.example", "*.disposable.example"},
	}); err != nil {
		t.Fatal(err)
	}

	rules, err = repo.EmailDomainRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules.Whitelist) != 2 || rules.Whitelist[0] != "allowed.example" || rules.Whitelist[1] != "partner.example" {
		t.Fatalf("whitelist round-trip = %v", rules.Whitelist)
	}
	if len(rules.Blacklist) != 2 || rules.Blacklist[0] != "spam.example" || rules.Blacklist[1] != "*.disposable.example" {
		t.Fatalf("blacklist round-trip = %v", rules.Blacklist)
	}

	// 3) 重新保存应替换而非追加（idempotent replace 语义）
	if err := repo.SaveEmailDomainRules(ctx, systemsettings.EmailDomainRules{
		Whitelist: []string{"only.example"},
	}); err != nil {
		t.Fatal(err)
	}
	rules, err = repo.EmailDomainRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules.Whitelist) != 1 || rules.Whitelist[0] != "only.example" {
		t.Fatalf("whitelist after replace = %v, want [only.example]", rules.Whitelist)
	}
	if len(rules.Blacklist) != 0 {
		t.Fatalf("blacklist after replace = %v, want empty", rules.Blacklist)
	}
}

// TestIntegrationEmailDomainRulesUniqueConstraint 验证唯一键约束生效，
// 防止并发保存导致重复行（重复会让 SaveEmailDomainRules 的「先 delete 再 insert」
// 在并发下产生不可重复的副作用）。
func TestIntegrationEmailDomainRulesUniqueConstraint(t *testing.T) {
	if os.Getenv("SYSTEMSETTINGS_INTEGRATION") != "1" {
		t.Skip("set SYSTEMSETTINGS_INTEGRATION=1 to run")
	}
	db, _, ctx := newEmailDomainRepo(t)
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `delete from bp_email_domain_rule`)
	})

	if _, err := db.ExecContext(ctx, `
		insert into bp_email_domain_rule(rule_kind, domain) values ('whitelist', 'dup.example')
	`); err != nil {
		t.Fatal(err)
	}
	_, err := db.ExecContext(ctx, `
		insert into bp_email_domain_rule(rule_kind, domain) values ('whitelist', 'dup.example')
	`)
	if err == nil {
		t.Fatal("duplicate (rule_kind, domain) must be rejected by the unique key")
	}
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
		t.Fatalf("duplicate insert error = %v, want MySQL 1062", err)
	}
}

// TestIntegrationSaveEmailDomainRulesThroughService 锁住 service.normalize
// 与 repo 落库的端到端：经 service 处理后的输入（混合大小写/前后空格/@前缀/通配）
// 必须以规范化形态写入数据库。
func TestIntegrationSaveEmailDomainRulesThroughService(t *testing.T) {
	if os.Getenv("SYSTEMSETTINGS_INTEGRATION") != "1" {
		t.Skip("set SYSTEMSETTINGS_INTEGRATION=1 to run")
	}
	db, repo, ctx := newEmailDomainRepo(t)
	service := newDomainRulesService(repo)

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `delete from bp_email_domain_rule`)
	})

	if err := service.SaveEmailDomainRules(ctx, rbac.Principal{Roles: []string{"admin"}}, systemsettings.EmailDomainRules{
		Whitelist: []string{"  @Example.COM  ", "partner.example"},
		Blacklist: []string{"SPAM.example"},
	}); err != nil {
		t.Fatal(err)
	}

	type row struct {
		kind, domain string
	}
	var rows []row
	rs, err := db.QueryContext(ctx, `select rule_kind, domain from bp_email_domain_rule order by rule_kind, domain`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.kind, &r.domain); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
	if len(rows) != 3 {
		t.Fatalf("stored rows = %+v, want 3 normalized rows", rows)
	}
	want := []row{
		{"blacklist", "spam.example"},
		{"whitelist", "example.com"},
		{"whitelist", "partner.example"},
	}
	for i, w := range want {
		if rows[i].kind != w.kind || rows[i].domain != w.domain {
			t.Fatalf("row[%d] = {%s,%s}, want {%s,%s}", i, rows[i].kind, rows[i].domain, w.kind, w.domain)
		}
	}
}

// TestIntegrationSaveEmailDomainRulesRejectsInvalidDomain 验证 service 校验
// 在落库前拦截非法域名，避免污染策略表。
func TestIntegrationSaveEmailDomainRulesRejectsInvalidDomain(t *testing.T) {
	if os.Getenv("SYSTEMSETTINGS_INTEGRATION") != "1" {
		t.Skip("set SYSTEMSETTINGS_INTEGRATION=1 to run")
	}
	_, repo, ctx := newEmailDomainRepo(t)
	service := newDomainRulesService(repo)

	t.Cleanup(func() {
		_ = repo.SaveEmailDomainRules(ctx, systemsettings.EmailDomainRules{})
	})

	err := service.SaveEmailDomainRules(ctx, rbac.Principal{Roles: []string{"admin"}}, systemsettings.EmailDomainRules{
		Whitelist: []string{"not a domain"},
	})
	if err == nil {
		t.Fatal("invalid domain must be rejected before persisting")
	}
	var bizErr *apperrors.BusinessError
	if !errors.As(err, &bizErr) || bizErr.StatusCode != 400 {
		t.Fatalf("invalid domain error = %v, want *BusinessError with status 400", err)
	}
}
