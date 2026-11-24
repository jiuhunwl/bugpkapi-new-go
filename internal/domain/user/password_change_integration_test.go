package user

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestUpdateOwnPasswordPreservesProfileAssetsAndRoles(t *testing.T) {
	dsn := os.Getenv("BUGPK_USER_INTEGRATION_DSN")
	if dsn == "" {
		t.Skip("BUGPK_USER_INTEGRATION_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}

	stamp := time.Now().UnixNano()
	username := fmt.Sprintf("password_scope_%d", stamp)
	result, err := db.ExecContext(ctx, `
		insert into bp_user(username,password_hash,display_name,email,mobile,home_path,balance,points,status,token_version)
		values(?, 'old-password-hash', 'Password Scope', ?, '13800000009', '/console/workspace', 88.1250, 77, 1, 4)
	`, username, username+"@example.com")
	if err != nil {
		t.Fatal(err)
	}
	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `insert into bp_user_role(user_id,role_id) values(?,2)`, userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`delete from bp_user_role where user_id=?`, userID)
		_, _ = db.Exec(`delete from bp_user where id=?`, userID)
	})

	if err := NewRepository(db).UpdateOwnPassword(ctx, userID, "new-password-hash"); err != nil {
		t.Fatal(err)
	}

	var passwordHash, displayName, email, mobile, homePath, balance string
	var points int64
	var status, tokenVersion int
	if err := db.QueryRowContext(ctx, `
		select password_hash,display_name,email,mobile,home_path,cast(balance as char),points,status,token_version
		from bp_user where id=?
	`, userID).Scan(&passwordHash, &displayName, &email, &mobile, &homePath, &balance, &points, &status, &tokenVersion); err != nil {
		t.Fatal(err)
	}
	if passwordHash != "new-password-hash" || tokenVersion != 5 {
		t.Fatalf("credential state = %q/version %d, want new hash/version 5", passwordHash, tokenVersion)
	}
	if displayName != "Password Scope" || email != username+"@example.com" || mobile != "13800000009" || homePath != "/console/workspace" || balance != "88.1250" || points != 77 || status != 1 {
		t.Fatalf("password update changed non-credential fields: display=%q email=%q mobile=%q home=%q balance=%q points=%d status=%d", displayName, email, mobile, homePath, balance, points, status)
	}
	var roleCount int
	if err := db.QueryRowContext(ctx, `select count(*) from bp_user_role where user_id=? and role_id=2`, userID).Scan(&roleCount); err != nil {
		t.Fatal(err)
	}
	if roleCount != 1 {
		t.Fatalf("password update changed role assignments: role count = %d", roleCount)
	}
}
