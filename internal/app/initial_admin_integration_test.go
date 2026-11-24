package app

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"bugpk-api/internal/config"
	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
)

const initialAdminTestPassword = "Sandbox-Only-Bootstrap-42!"

func TestInitialAdminPasswordIntegration(t *testing.T) {
	if os.Getenv("APP_INTEGRATION") != "1" {
		t.Skip("set APP_INTEGRATION=1 to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if err := testdb.ValidateIsolatedMySQLDSN(cfg.Database.DSN()); err != nil {
		t.Fatal(err)
	}
	cfg.Auth.InitialAdminPassword = initialAdminTestPassword
	cfg.Migration.Path = "../../migrations"
	db, err := sql.Open("mysql", cfg.Database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := RunMigrations(db, cfg.Migration.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update bp_user set username='rootadmin',password_hash=?,token_version=0 where id=1`, legacyInitialAdminHash); err != nil {
		t.Fatal(err)
	}

	initialized, err := ensureInitialAdminPassword(context.Background(), db, cfg)
	if err != nil || !initialized {
		t.Fatalf("first initialization = %v, error = %v", initialized, err)
	}
	var hash string
	var tokenVersion int
	var username string
	if err := db.QueryRow(`select username,password_hash,token_version from bp_user where id=1`).Scan(&username, &hash, &tokenVersion); err != nil {
		t.Fatal(err)
	}
	if username != "rootadmin" {
		t.Fatalf("bootstrap changed renamed administrator username to %q", username)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(initialAdminTestPassword)); err != nil {
		t.Fatalf("stored bootstrap password mismatch: %v", err)
	}
	if tokenVersion != 1 {
		t.Fatalf("token_version = %d, want 1", tokenVersion)
	}

	initialized, err = ensureInitialAdminPassword(context.Background(), db, cfg)
	if err != nil || initialized {
		t.Fatalf("second initialization = %v, error = %v", initialized, err)
	}
}

func TestInitialAdminPasswordCreatesMissingDefaultAccount(t *testing.T) {
	if os.Getenv("APP_INTEGRATION") != "1" {
		t.Skip("set APP_INTEGRATION=1 to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if err := testdb.ValidateIsolatedMySQLDSN(cfg.Database.DSN()); err != nil {
		t.Fatal(err)
	}
	cfg.Auth.InitialAdminPassword = initialAdminTestPassword
	cfg.Migration.Path = "../../migrations"
	db, err := sql.Open("mysql", cfg.Database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := RunMigrations(db, cfg.Migration.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`delete from bp_user_role where user_id=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`delete from bp_user where id=1`); err != nil {
		t.Fatal(err)
	}
	initialized, err := ensureInitialAdminPassword(context.Background(), db, cfg)
	if err != nil || !initialized {
		t.Fatalf("missing default account initialization = %v, error = %v", initialized, err)
	}
	var username, displayName, hash string
	var status, tokenVersion int
	if err := db.QueryRow(`
		select username,display_name,password_hash,status,token_version
		from bp_user where id=1
	`).Scan(&username, &displayName, &hash, &status, &tokenVersion); err != nil {
		t.Fatal(err)
	}
	if username != "admin" || displayName != "Administrator" || status != 1 || tokenVersion != 0 {
		t.Fatalf("created administrator = %q, %q, status=%d, tokenVersion=%d", username, displayName, status, tokenVersion)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(initialAdminTestPassword)); err != nil {
		t.Fatalf("created administrator password mismatch: %v", err)
	}
	var roleCount int
	if err := db.QueryRow(`
		select count(*)
		from bp_user_role ur
		inner join bp_role r on r.id=ur.role_id
		where ur.user_id=1 and r.role_key='admin' and r.status=1
	`).Scan(&roleCount); err != nil {
		t.Fatal(err)
	}
	if roleCount != 1 {
		t.Fatalf("administrator role count = %d, want 1", roleCount)
	}

	initialized, err = ensureInitialAdminPassword(context.Background(), db, cfg)
	if err != nil || initialized {
		t.Fatalf("second missing-account initialization = %v, error = %v", initialized, err)
	}
}

func TestPublishedBootstrapPasswordRotationPreservesRolesIntegration(t *testing.T) {
	if os.Getenv("APP_INTEGRATION") != "1" {
		t.Skip("set APP_INTEGRATION=1 in an isolated sandbox to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if err := testdb.ValidateIsolatedMySQLDSN(cfg.Database.DSN()); err != nil {
		t.Fatal(err)
	}
	cfg.Migration.Path = "../../migrations"
	db, err := sql.Open("mysql", cfg.Database.DSN())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := RunMigrations(db, cfg.Migration.Path); err != nil {
		t.Fatal(err)
	}
	publishedHash, err := bcrypt.GenerateFromPassword([]byte("123456"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("update bp_user set password_hash=?,token_version=7,primary_role_id=2 where id=1", string(publishedHash)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("delete from bp_user_role where user_id=1"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into bp_user_role(user_id,role_id) values(1,2)"); err != nil {
		t.Fatal(err)
	}

	cfg.Auth.InitialAdminPassword = ""
	if initialized, err := ensureInitialAdminPassword(context.Background(), db, cfg); err == nil || initialized {
		t.Fatal("published password was accepted without an explicit replacement")
	}
	var hash string
	var tokenVersion int
	if err := db.QueryRow("select password_hash,token_version from bp_user where id=1").Scan(&hash, &tokenVersion); err != nil {
		t.Fatal(err)
	}
	if hash != string(publishedHash) || tokenVersion != 7 {
		t.Fatal("failed bootstrap changed password or token version")
	}

	cfg.Auth.InitialAdminPassword = initialAdminTestPassword
	if initialized, err := ensureInitialAdminPassword(context.Background(), db, cfg); err != nil || !initialized {
		t.Fatalf("published password rotation initialized=%v err=%v", initialized, err)
	}
	if err := db.QueryRow("select password_hash,token_version from bp_user where id=1").Scan(&hash, &tokenVersion); err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(initialAdminTestPassword)) != nil || tokenVersion != 8 {
		t.Fatal("rotation did not replace the password and invalidate prior tokens")
	}
	var adminRoles, userRoles int
	if err := db.QueryRow("select count(*) from bp_user_role where user_id=1 and role_id=1").Scan(&adminRoles); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("select count(*) from bp_user_role where user_id=1 and role_id=2").Scan(&userRoles); err != nil {
		t.Fatal(err)
	}
	if adminRoles != 0 || userRoles != 1 {
		t.Fatal("rotating a published password must not elevate an existing account")
	}
	cfg.Auth.InitialAdminPassword = ""
	if initialized, err := ensureInitialAdminPassword(context.Background(), db, cfg); err != nil || initialized {
		t.Fatalf("custom-password restart initialized=%v err=%v", initialized, err)
	}
}
