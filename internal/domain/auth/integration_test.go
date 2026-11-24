package auth_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"bugpk-api/internal/app"
	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/auth"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/testdb"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	_ "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

func TestIntegrationCaptchaCreate(t *testing.T) {
	if os.Getenv("AUTH_INTEGRATION") != "1" {
		t.Skip("set AUTH_INTEGRATION=1 to run")
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
	defer db.Close()
	if err := app.RunMigrations(db, cfg.Migration.Path); err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr(), Password: cfg.Redis.Password, DB: cfg.Redis.Database})
	defer rdb.Close()

	repo := auth.NewRepository(db)
	service := auth.NewCaptchaService(cfg.Captcha, repo, rdb)
	challenge, err := service.Create(context.Background(), "register", auth.CaptchaBinding("ua", "lang"))
	if err != nil {
		t.Fatal(err)
	}
	if challenge.Provider != "pow" || challenge.CaptchaID == "" {
		t.Fatalf("unexpected challenge: %+v", challenge)
	}
}

func TestIntegrationRevokeEmailCodeOnlyRevokesExactHash(t *testing.T) {
	if os.Getenv("AUTH_INTEGRATION") != "1" {
		t.Skip("set AUTH_INTEGRATION=1 to run")
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
	repo := auth.NewRepository(db)
	ctx := context.Background()
	expiresAt := time.Now().Add(10 * time.Minute)
	const (
		email  = "revoke-integration@example.com"
		first  = "first-code-hash"
		scene  = "register"
		second = "second-code-hash"
	)
	if err := repo.CreateEmailCode(ctx, scene, email, first, expiresAt); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateEmailCode(ctx, scene, email, second, expiresAt); err != nil {
		t.Fatal(err)
	}
	if err := repo.RevokeEmailCode(ctx, scene, email, first); err != nil {
		t.Fatal(err)
	}
	if err := repo.ConsumeEmailCode(ctx, scene, email, first); !errors.Is(err, auth.ErrVerificationCodeInvalid) {
		t.Fatalf("revoked code consumption error = %v", err)
	}
	if err := repo.ConsumeEmailCode(ctx, scene, email, second); err != nil {
		t.Fatalf("unrelated concurrent code was revoked: %v", err)
	}

	const newestEmail = "newest-integration@example.com"
	if err := repo.CreateEmailCode(ctx, scene, newestEmail, first, expiresAt); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateEmailCode(ctx, scene, newestEmail, second, expiresAt); err != nil {
		t.Fatal(err)
	}
	if err := repo.ConsumeEmailCode(ctx, scene, newestEmail, first); !errors.Is(err, auth.ErrVerificationCodeInvalid) {
		t.Fatalf("superseded code consumption error = %v", err)
	}
	if err := repo.ConsumeEmailCode(ctx, scene, newestEmail, second); err != nil {
		t.Fatalf("newest code could not be consumed: %v", err)
	}
	if err := repo.ConsumeEmailCode(ctx, scene, newestEmail, first); !errors.Is(err, auth.ErrVerificationCodeInvalid) {
		t.Fatalf("older code became valid again after newest consumption: %v", err)
	}

	const fallbackEmail = "fallback-integration@example.com"
	if err := repo.CreateEmailCode(ctx, scene, fallbackEmail, first, expiresAt); err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateEmailCode(ctx, scene, fallbackEmail, second, expiresAt); err != nil {
		t.Fatal(err)
	}
	if err := repo.RevokeEmailCode(ctx, scene, fallbackEmail, second); err != nil {
		t.Fatal(err)
	}
	if err := repo.ConsumeEmailCode(ctx, scene, fallbackEmail, first); err != nil {
		t.Fatalf("previous delivered code did not recover after newest send failed: %v", err)
	}

	const concurrentEmail = "concurrent-consume@example.com"
	if err := repo.CreateEmailCode(ctx, scene, concurrentEmail, second, expiresAt); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			results <- repo.ConsumeEmailCode(context.Background(), scene, concurrentEmail, second)
		}()
	}
	wait.Wait()
	close(results)
	var successes, invalid int
	for result := range results {
		switch {
		case result == nil:
			successes++
		case errors.Is(result, auth.ErrVerificationCodeInvalid):
			invalid++
		default:
			t.Fatalf("concurrent consume error = %v", result)
		}
	}
	if successes != 1 || invalid != 1 {
		t.Fatalf("concurrent consume results = success:%d invalid:%d", successes, invalid)
	}
}

func TestIntegrationPasswordResetValidationBeforeTokenConsumption(t *testing.T) {
	if os.Getenv("AUTH_RESET_FLOW_INTEGRATION") != "1" {
		t.Skip("set AUTH_RESET_FLOW_INTEGRATION=1 to run")
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
	rdb := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr(), Password: cfg.Redis.Password, DB: cfg.Redis.Database})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	const (
		code     = "654321"
		email    = "reset-flow-integration@example.com"
		password = "BugPk-Api@Reset2026!"
		username = "reset_flow_integration"
	)
	insert, err := db.Exec(`
		insert into bp_user(username,password_hash,display_name,email,email_verified,status,token_version)
		values(?,?,'Reset Flow',?,1,1,0)
	`, username, "$2a$10$invalid.placeholder.hash.for.integration", email)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := insert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	repo := auth.NewRepository(db)
	codeDigest := sha256.Sum256([]byte("password_reset|" + email + "|" + code + "|" + cfg.Auth.Secret))
	if err := repo.CreateEmailCode(context.Background(), "password_reset", email, hex.EncodeToString(codeDigest[:]), time.Now().Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	codeService := auth.NewVerificationCodeService(cfg.Message, cfg.Auth, repo, auth.NewRateLimiter(rdb, cfg.Auth.Secret), rdb)
	service := auth.NewService(cfg.Auth, repo, nil, nil, nil, rdb, codeService)
	verified, err := service.VerifyPasswordResetCode(context.Background(), auth.PasswordResetVerifyRequest{Account: username, Code: code})
	if err != nil || verified.ResetToken == "" {
		t.Fatalf("password reset verification = %+v err:%v", verified, err)
	}
	if err := service.ResetPassword(context.Background(), auth.PasswordResetRequest{
		Password: password, ConfirmPassword: password + "-mismatch", ResetToken: verified.ResetToken,
	}); err == nil {
		t.Fatal("mismatched password confirmation was accepted")
	}
	if peeked, err := codeService.PeekPasswordResetToken(context.Background(), verified.ResetToken); err != nil || peeked != userID {
		t.Fatalf("validation error consumed reset token: user:%d err:%v", peeked, err)
	}
	if _, err := db.Exec(`update bp_user set status=0 where id=?`, userID); err != nil {
		t.Fatal(err)
	}
	if err := service.ResetPassword(context.Background(), auth.PasswordResetRequest{
		Password: password, ConfirmPassword: password, ResetToken: verified.ResetToken,
	}); err == nil {
		t.Fatal("disabled user password reset was accepted")
	}
	if peeked, err := codeService.PeekPasswordResetToken(context.Background(), verified.ResetToken); err != nil || peeked != userID {
		t.Fatalf("disabled account check consumed reset token: user:%d err:%v", peeked, err)
	}
	if _, err := db.Exec(`update bp_user set status=1 where id=?`, userID); err != nil {
		t.Fatal(err)
	}
	if err := service.ResetPassword(context.Background(), auth.PasswordResetRequest{
		Password: password, ConfirmPassword: password, ResetToken: verified.ResetToken,
	}); err != nil {
		t.Fatal(err)
	}
	var passwordHash string
	var tokenVersion int
	if err := db.QueryRow(`select password_hash,token_version from bp_user where id=?`, userID).Scan(&passwordHash, &tokenVersion); err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(passwordHash), []byte(password)) != nil || tokenVersion != 1 {
		t.Fatalf("password reset persistence invalid: tokenVersion=%d", tokenVersion)
	}
	if err := service.ResetPassword(context.Background(), auth.PasswordResetRequest{
		Password: password, ConfirmPassword: password, ResetToken: verified.ResetToken,
	}); err == nil {
		t.Fatal("consumed reset token was replayed")
	}
}

func TestIntegrationConcurrentRefreshRotationSharesGraceWindow(t *testing.T) {
	if os.Getenv("AUTH_REFRESH_INTEGRATION") != "1" {
		t.Skip("set AUTH_REFRESH_INTEGRATION=1 to run")
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
	rdb := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr(), Password: cfg.Redis.Password, DB: cfg.Redis.Database})
	t.Cleanup(func() { _ = rdb.Close() })
	const username = "refresh_rotation_integration"
	insert, err := db.Exec(`
		insert into bp_user(username,password_hash,display_name,status,token_version)
		values(?,'$2a$10$integration.placeholder.hash','Refresh Rotation',1,0)
	`, username)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := insert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	repo := auth.NewRepository(db)
	jwtService, err := auth.NewJWTService(cfg.Auth)
	if err != nil {
		t.Fatal(err)
	}
	user, err := repo.FindUserByID(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	const (
		ip        = "203.0.113.77"
		userAgent = "BugPk-Api refresh integration"
	)
	device := auth.DeviceFingerprint(ip, userAgent)
	now := time.Now()
	refreshToken, jti, expiresAt, err := jwtService.CreateRefreshToken(user, device, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateRefreshAudit(context.Background(), user, jti, device, now, expiresAt); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = rdb.Del(context.Background(), "auth:refresh:revoked:"+jti, "auth:refresh:grace:"+jti).Err()
	})
	service := auth.NewService(cfg.Auth, repo, nil, jwtService, nil, rdb, nil)
	type refreshResult struct {
		pair auth.TokenPair
		err  error
	}
	results := make(chan refreshResult, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			pair, err := service.Refresh(context.Background(), refreshToken, ip, userAgent)
			results <- refreshResult{pair: pair, err: err}
		}()
	}
	wait.Wait()
	close(results)
	var successfulPair auth.TokenPair
	var rotated, replayed int
	for result := range results {
		if result.err == nil {
			if result.pair.AccessToken == "" {
				t.Fatalf("successful refresh returned an empty access token: %+v", result.pair)
			}
			// 宽限窗口内只允许一个请求轮换 refresh token；其余请求补发
			// access token（RefreshToken 为空），不能 401 把标签页踢下线。
			if result.pair.RefreshToken == "" {
				replayed++
			} else {
				rotated++
				successfulPair = result.pair
			}
			continue
		}
		t.Fatalf("concurrent refresh error = %v", result.err)
	}
	if rotated != 1 || replayed != 1 {
		t.Fatalf("concurrent refresh results = rotated:%d replayed:%d", rotated, replayed)
	}
	if err := rdb.Del(context.Background(), "auth:refresh:revoked:"+jti).Err(); err != nil {
		t.Fatal(err)
	}
	if replayed, err := service.Refresh(context.Background(), refreshToken, ip, userAgent); err == nil {
		t.Fatalf("database-inactive refresh token replayed after Redis marker loss: %+v", replayed)
	} else if business, ok := apperrors.AsBusinessError(err); !ok || business.StatusCode != consts.StatusUnauthorized {
		t.Fatalf("database replay guard error = %v, want unauthorized", err)
	}
	if successfulPair.RefreshJTI == "" {
		t.Fatal("successful refresh pair did not retain its JTI")
	}
	var auditCount, revokedCount, usedCount int
	if err := db.QueryRow(`
		select count(*),sum(revoked_at is not null),sum(last_used_at is not null)
		from bp_auth_refresh_token_audit where user_id=?
	`, userID).Scan(&auditCount, &revokedCount, &usedCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 || revokedCount != 1 || usedCount != 1 {
		t.Fatalf("refresh audit state = rows:%d revoked:%d used:%d", auditCount, revokedCount, usedCount)
	}
}

func TestIntegrationRefreshAuditRotationIsTransactional(t *testing.T) {
	if os.Getenv("AUTH_REFRESH_AUDIT_INTEGRATION") != "1" {
		t.Skip("set AUTH_REFRESH_AUDIT_INTEGRATION=1 to run")
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

	insert, err := db.Exec(`
		insert into bp_user(username,password_hash,display_name,status,token_version)
		values('refresh_audit_atomic','$2a$10$integration.placeholder.hash','Refresh Audit Atomic',1,3)
	`)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := insert.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	user := auth.User{ID: userID, Username: "refresh_audit_atomic", Status: 1, TokenVersion: 3}
	repo := auth.NewRepository(db)
	now := time.Now().UTC().Truncate(time.Second)
	oldInfo := auth.RefreshInfo{
		Device:       "ua2_old_device",
		JTI:          "refresh-audit-old-jti",
		TokenVersion: user.TokenVersion,
		UserID:       user.ID,
	}
	if err := repo.CreateRefreshAudit(context.Background(), user, oldInfo.JTI, oldInfo.Device, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repo.RotateRefreshAudit(
		context.Background(), oldInfo, user, "refresh-audit-new-jti", "ua2_new_device", now.Add(time.Minute), now.Add(time.Hour),
	); err != nil {
		t.Fatalf("RotateRefreshAudit() error = %v", err)
	}
	if err := repo.RotateRefreshAudit(
		context.Background(), oldInfo, user, "refresh-audit-replay-jti", "ua2_replay_device", now.Add(2*time.Minute), now.Add(time.Hour),
	); !errors.Is(err, auth.ErrRefreshAuditInactive) {
		t.Fatalf("replayed audit rotation error = %v, want inactive", err)
	}

	rollbackInfo := auth.RefreshInfo{
		Device:       "ua2_rollback_old",
		JTI:          "refresh-audit-rollback-old-jti",
		TokenVersion: user.TokenVersion,
		UserID:       user.ID,
	}
	if err := repo.CreateRefreshAudit(context.Background(), user, rollbackInfo.JTI, rollbackInfo.Device, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := repo.RotateRefreshAudit(
		context.Background(), rollbackInfo, user, "refresh-audit-new-jti", "ua2_duplicate_new", now.Add(time.Minute), now.Add(time.Hour),
	); err == nil {
		t.Fatal("rotation with duplicate new JTI unexpectedly succeeded")
	}
	var revokedAt, lastUsedAt sql.NullTime
	if err := db.QueryRow(`
		select revoked_at,last_used_at from bp_auth_refresh_token_audit where jti=?
	`, rollbackInfo.JTI).Scan(&revokedAt, &lastUsedAt); err != nil {
		t.Fatal(err)
	}
	if revokedAt.Valid || lastUsedAt.Valid {
		t.Fatalf("failed rotation did not roll back old audit: revoked=%v used=%v", revokedAt.Valid, lastUsedAt.Valid)
	}
	var auditCount int
	if err := db.QueryRow(`select count(*) from bp_auth_refresh_token_audit where user_id=?`, user.ID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 3 {
		t.Fatalf("refresh audit rows = %d, want 3", auditCount)
	}
}
