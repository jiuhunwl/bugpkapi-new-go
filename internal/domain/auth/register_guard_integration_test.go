package auth_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"bugpk-api/internal/app"
	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

// newRegisterGuardRepo 应用迁移并返回仓储，供注册风控集成用例复用。
func newRegisterGuardRepo(t *testing.T) (*sql.DB, *auth.Repository, context.Context) {
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
	return db, auth.NewRepository(db), context.Background()
}

func cleanupRegisterIPUser(t *testing.T, db *sql.DB, userID int64) {
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `delete from bp_register_ip_used where user_id = ?`, userID)
		_, _ = db.ExecContext(context.Background(), `delete from bp_user_role where user_id = ?`, userID)
		_, _ = db.ExecContext(context.Background(), `delete from bp_user where id = ?`, userID)
	})
}

// fixedDigest returns a 32-byte digest with a distinct tag. bp_register_ip_used
// is binary(32); comparisons against shorter literals do not match the
// zero-padded stored value, so fixtures must be full-length — exactly like the
// sha256 digests written by production code.
func fixedDigest(tag byte) []byte {
	digest := make([]byte, 32)
	digest[0] = tag
	copy(digest[1:], []byte("register-guard-integration"))
	return digest
}

func TestIntegrationCreateLocalUserRecordsRegisterIP(t *testing.T) {
	if os.Getenv("AUTH_INTEGRATION") != "1" {
		t.Skip("set AUTH_INTEGRATION=1 to run")
	}
	db, repo, ctx := newRegisterGuardRepo(t)

	ipHash := fixedDigest(0x01)
	user, err := repo.CreateLocalUser(ctx, auth.RegisterRequest{
		Username: "guard_ip_user",
	}, "$2a$10$integration.placeholder.hash", 0, ipHash)
	if err != nil {
		t.Fatal(err)
	}
	cleanupRegisterIPUser(t, db, user.ID)

	var storedUserID int64
	if err := db.QueryRowContext(ctx, `select user_id from bp_register_ip_used where ip_hash = ?`, ipHash).Scan(&storedUserID); err != nil {
		t.Fatalf("registration ip row missing: %v", err)
	}
	if storedUserID != user.ID {
		t.Fatalf("registered ip user_id = %d, want %d", storedUserID, user.ID)
	}
	count, err := repo.CountRegisterIPAccounts(ctx, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	// The count is keyed by iphash.RiskDigest; the integration digest above is
	// a distinct value, so the count for an unrelated IP stays zero.
	if count != 0 {
		t.Fatalf("unrelated ip count = %d, want 0", count)
	}
}

func TestIntegrationCreateLocalUserIPLimitRollsBack(t *testing.T) {
	if os.Getenv("AUTH_INTEGRATION") != "1" {
		t.Skip("set AUTH_INTEGRATION=1 to run")
	}
	db, repo, ctx := newRegisterGuardRepo(t)

	// Simulate the cap being hit concurrently: the IP row already exists, so
	// the primary key must abort the second registration and roll it back.
	ipHash := fixedDigest(0x02)
	first, err := repo.CreateLocalUser(ctx, auth.RegisterRequest{
		Username: "guard_ip_race_first",
	}, "$2a$10$integration.placeholder.hash", 0, ipHash)
	if err != nil {
		t.Fatal(err)
	}
	cleanupRegisterIPUser(t, db, first.ID)

	_, err = repo.CreateLocalUser(ctx, auth.RegisterRequest{
		Username: "guard_ip_race_second",
	}, "$2a$10$integration.placeholder.hash", 0, ipHash)
	if !errors.Is(err, auth.ErrRegisterIPLimit) {
		t.Fatalf("duplicate ip registration error = %v, want ErrRegisterIPLimit", err)
	}
	var created int
	if err := db.QueryRowContext(ctx, `select exists(select 1 from bp_user where username = 'guard_ip_race_second')`).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatal("registration hitting the ip cap must roll back the user creation")
	}
}

func TestIntegrationEmailDomainRulesRoundTrip(t *testing.T) {
	if os.Getenv("AUTH_INTEGRATION") != "1" {
		t.Skip("set AUTH_INTEGRATION=1 to run")
	}
	db, repo, ctx := newRegisterGuardRepo(t)

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `delete from bp_email_domain_rule`)
	})
	if _, err := db.ExecContext(ctx, `
		insert into bp_email_domain_rule(rule_kind, domain) values ('whitelist', 'allowed.example'), ('blacklist', 'spam.example')
	`); err != nil {
		t.Fatal(err)
	}
	whitelist, blacklist, err := repo.EmailDomainRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(whitelist) != 1 || whitelist[0] != "allowed.example" {
		t.Fatalf("whitelist = %v, want [allowed.example]", whitelist)
	}
	if len(blacklist) != 1 || blacklist[0] != "spam.example" {
		t.Fatalf("blacklist = %v, want [spam.example]", blacklist)
	}
}
