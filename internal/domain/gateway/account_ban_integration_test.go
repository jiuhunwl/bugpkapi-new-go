package gateway_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	authdomain "bugpk-api/internal/domain/auth"
	gatewaydomain "bugpk-api/internal/domain/gateway"
	rbacdomain "bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/testdb"

	_ "github.com/go-sql-driver/mysql"
)

func TestAccountBanAppliesToAuthenticationRBACAndAPIKeys(t *testing.T) {
	dsn := os.Getenv("BUGPK_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("BUGPK_MYSQL_TEST_DSN is not configured")
	}
	if err := testdb.ValidateIsolatedMySQLDSN(dsn); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	username := fmt.Sprintf("ban_scope_%d", time.Now().UnixNano())
	hashBytes := sha256.Sum256([]byte(username))
	keyHash := hex.EncodeToString(hashBytes[:])

	result, err := db.ExecContext(ctx, `
		insert into bp_user(
			username,password_hash,display_name,home_path,balance,points,status,
			ban_permanent,ban_until,ban_reason,token_version
		)
		values(?,'not-used',?,'/console/workspace',0,0,0,0,date_add(now(),interval 10 minute),'integration test',7)
	`, username, username)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("delete from bp_api_key where user_id=?", userID)
		_, _ = db.Exec("delete from bp_user_role where user_id=?", userID)
		_, _ = db.Exec("delete from bp_user where id=?", userID)
	})
	if _, err := db.ExecContext(ctx, "insert into bp_user_role(user_id,role_id) values(?,2)", userID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		insert into bp_api_key(user_id,name,key_hash,key_prefix,key_suffix,key_type,status)
		values(?,'ban-scope-test',?,'ban_scope','test','GLOBAL',1)
	`, userID, keyHash); err != nil {
		t.Fatal(err)
	}

	authRepo := authdomain.NewRepository(db)
	rbacRepo := rbacdomain.NewRepository(db)
	gatewayRepo := gatewaydomain.NewRepository(db)
	assertEffectiveAccountStatus(t, ctx, authRepo, rbacRepo, gatewayRepo, userID, keyHash, 0, "integration test", 0)

	if _, err := db.ExecContext(ctx, `
		update bp_user
		set ban_until=date_sub(now(),interval 1 second)
		where id=?
	`, userID); err != nil {
		t.Fatal(err)
	}
	assertEffectiveAccountStatus(t, ctx, authRepo, rbacRepo, gatewayRepo, userID, keyHash, 1, "", 0)

	if _, err := db.ExecContext(ctx, `
		update bp_user
		set status=0,ban_permanent=1,ban_until=null,ban_reason='permanent integration test'
		where id=?
	`, userID); err != nil {
		t.Fatal(err)
	}
	assertEffectiveAccountStatus(t, ctx, authRepo, rbacRepo, gatewayRepo, userID, keyHash, 0, "permanent integration test", 1)
}

func assertEffectiveAccountStatus(
	t *testing.T,
	ctx context.Context,
	authRepo *authdomain.Repository,
	rbacRepo *rbacdomain.Repository,
	gatewayRepo *gatewaydomain.Repository,
	userID int64,
	keyHash string,
	want int,
	wantReason string,
	wantPermanent int,
) {
	t.Helper()

	user, err := authRepo.FindUserByID(ctx, userID)
	if err != nil {
		t.Fatalf("auth user lookup: %v", err)
	}
	if user.Status != want {
		t.Fatalf("auth effective status = %d, want %d", user.Status, want)
	}
	if want == 0 && (user.BanReason != wantReason || user.BanPermanent != wantPermanent) {
		t.Fatalf("auth ban metadata = reason %q permanent %d", user.BanReason, user.BanPermanent)
	}

	principal, principalErr := rbacRepo.FindPrincipal(ctx, userID)
	if want == 0 {
		if !errors.Is(principalErr, rbacdomain.ErrUserDisabled) {
			t.Fatalf("RBAC principal error = %v, want ErrUserDisabled", principalErr)
		}
		if principal.BanReason != wantReason || principal.BanPermanent != wantPermanent {
			t.Fatalf("RBAC ban metadata = reason %q permanent %d", principal.BanReason, principal.BanPermanent)
		}
	} else {
		if principalErr != nil {
			t.Fatalf("RBAC principal lookup: %v", principalErr)
		}
		if principal.ID != userID {
			t.Fatalf("RBAC principal id = %d, want %d", principal.ID, userID)
		}
	}

	key, err := gatewayRepo.KeyAuthByHash(ctx, keyHash)
	if err != nil {
		t.Fatalf("gateway key lookup: %v", err)
	}
	if key.UserStatus != want {
		t.Fatalf("gateway effective user status = %d, want %d", key.UserStatus, want)
	}
	if want == 0 && (key.BanReason != wantReason || key.BanPermanent != wantPermanent) {
		t.Fatalf("gateway ban metadata = reason %q permanent %d", key.BanReason, key.BanPermanent)
	}
}
