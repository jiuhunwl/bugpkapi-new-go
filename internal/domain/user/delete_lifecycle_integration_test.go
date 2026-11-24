package user_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"bugpk-api/internal/app"
	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/user"
	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestUserDeleteLifecycleIntegration(t *testing.T) {
	if os.Getenv("USER_DELETE_INTEGRATION") != "1" {
		t.Skip("set USER_DELETE_INTEGRATION=1 to run")
	}
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
	ctx := context.Background()
	repo := user.NewRepository(db)

	interfaceResult, err := db.Exec(`
		insert into bp_interface_api(api_code,name,request_url,request_method,remark)
		values('user-delete-audit','User Delete Audit','https://example.com/audit','GET','integration test')
	`)
	if err != nil {
		t.Fatal(err)
	}
	interfaceID, err := interfaceResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}

	cleanUserID := insertDeleteTestUser(t, db, "clean_delete_user", "clean-delete@example.com", "13800000001", "0.0000", 0)
	keyResult, err := db.Exec(`
		insert into bp_api_key(user_id,name,key_hash,key_prefix,key_suffix,key_type,status)
		values(?,'Disposable Key',?,'bp_live_clean','clean','INTERFACE',1)
	`, cleanUserID, strings.Repeat("c", 64))
	if err != nil {
		t.Fatal(err)
	}
	keyID, err := keyResult.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{"insert into bp_api_key_interface(key_id,interface_id) values(?,?)", []any{keyID, interfaceID}},
		{"insert into bp_api_key_ip_whitelist(key_id,ip_rule) values(?,'127.0.0.1')", []any{keyID}},
		{"insert into bp_user_specified_response(user_id,enabled,billable,body) values(?,1,0,'ok')", []any{cleanUserID}},
		{"insert into bp_user_log_cleanup_config(user_id,retention_days,auto_hide_enabled) values(?,30,1)", []any{cleanUserID}},
		{"insert into bp_auth_refresh_token_audit(user_id,jti,token_version,issued_at,expires_at) values(?,'delete-clean-jti',0,now(),date_add(now(),interval 1 day))", []any{cleanUserID}},
		{"insert into bp_auth_email_code(scene,email,code_hash,expires_at) values('bind','clean-delete@example.com','hash',date_add(now(),interval 10 minute))", nil},
		{"insert into bp_auth_mobile_code(scene,mobile,code_hash,expires_at) values('bind','13800000001','hash',date_add(now(),interval 10 minute))", nil},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.DeleteUser(ctx, cleanUserID); err != nil {
		t.Fatal(err)
	}
	assertUserDeleteCount(t, db, "select count(*) from bp_user where id=?", 0, cleanUserID)
	assertUserDeleteCount(t, db, "select count(*) from bp_api_key where id=?", 0, keyID)
	assertUserDeleteCount(t, db, "select count(*) from bp_api_key_interface where key_id=?", 0, keyID)
	assertUserDeleteCount(t, db, "select count(*) from bp_api_key_ip_whitelist where key_id=?", 0, keyID)
	assertUserDeleteCount(t, db, "select count(*) from bp_auth_refresh_token_audit where user_id=?", 0, cleanUserID)
	assertUserDeleteCount(t, db, "select count(*) from bp_auth_email_code where email='clean-delete@example.com'", 0)
	assertUserDeleteCount(t, db, "select count(*) from bp_auth_mobile_code where mobile='13800000001'", 0)

	fundedUserID := insertDeleteTestUser(t, db, "funded_delete_user", "funded-delete@example.com", "13800000002", "1.0000", 0)
	if err := repo.DeleteUser(ctx, fundedUserID); !errors.Is(err, user.ErrUserHasFunds) {
		t.Fatalf("funded user delete error = %v", err)
	}
	assertUserDeleteCount(t, db, "select count(*) from bp_user where id=?", 1, fundedUserID)

	pendingUserID := insertDeleteTestUser(t, db, "pending_delete_user", "pending-delete@example.com", "13800000003", "0.0000", 0)
	if _, err := db.Exec(`
		insert into bp_payment_order(order_no,user_id,order_type,product,subject,pay_amount,status,expire_at)
		values('BP_USER_DELETE_PENDING',?,'RECHARGE','PAGE','Pending delete recharge',10.0000,'PAYING',date_add(now(),interval 30 minute))
	`, pendingUserID); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteUser(ctx, pendingUserID); !errors.Is(err, user.ErrUserHasPendingPayments) {
		t.Fatalf("pending-payment user delete error = %v", err)
	}
	assertUserDeleteCount(t, db, "select count(*) from bp_user where id=?", 1, pendingUserID)

	historyUserID := insertDeleteTestUser(t, db, "history_delete_user", "history-delete@example.com", "13800000004", "0.0000", 0)
	if _, err := db.Exec(`
		insert into bp_interface_call_log(interface_id,user_id,api_code,request_method,response_status,success,charge_type)
		values(?,?,'user-delete-audit','GET',200,1,'FREE')
	`, interfaceID, historyUserID); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteUser(ctx, historyUserID); !errors.Is(err, user.ErrUserHasBusinessHistory) {
		t.Fatalf("history user delete error = %v", err)
	}
	assertUserDeleteCount(t, db, "select count(*) from bp_user where id=?", 1, historyUserID)
}

func insertDeleteTestUser(t *testing.T, db *sql.DB, username, email, mobile, balance string, points int64) int64 {
	t.Helper()
	result, err := db.Exec(`
		insert into bp_user(username,password_hash,display_name,email,mobile,balance,points,status)
		values(?,'$2a$10$integration.placeholder.hash',?,?,?, ?,?,1)
	`, username, username, email, mobile, balance, points)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("insert into bp_user_role(user_id,role_id) values(?,2)", userID); err != nil {
		t.Fatal(err)
	}
	return userID
}

func assertUserDeleteCount(t *testing.T, db *sql.DB, query string, want int64, args ...any) {
	t.Helper()
	var got int64
	if err := db.QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("count for %q = %d, want %d", query, got, want)
	}
}
