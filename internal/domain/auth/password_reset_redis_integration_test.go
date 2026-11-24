package auth

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"bugpk-api/internal/config"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

func TestPasswordResetTokenRotationRedisIntegration(t *testing.T) {
	if os.Getenv("AUTH_REDIS_INTEGRATION") != "1" {
		t.Skip("set AUTH_REDIS_INTEGRATION=1 to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr(),
		DB:       cfg.Redis.Database,
		Password: cfg.Redis.Password,
	})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	service := &VerificationCodeService{rdb: client}
	const userID int64 = 918273645
	tokens := []string{"reset-integration-first", "reset-integration-second", "reset-integration-concurrent"}
	t.Cleanup(func() {
		keys := []string{passwordResetUserKey(userID)}
		for _, token := range tokens {
			keys = append(keys, passwordResetTokenKey(token))
		}
		_ = client.Del(context.Background(), keys...).Err()
	})

	if err := service.issuePasswordResetToken(ctx, userID, tokens[0]); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		peekedUserID, err := service.PeekPasswordResetToken(ctx, tokens[0])
		if err != nil || peekedUserID != userID {
			t.Fatalf("password reset token peek = user:%d err:%v", peekedUserID, err)
		}
	}
	if err := service.issuePasswordResetToken(ctx, userID, tokens[1]); err != nil {
		t.Fatal(err)
	}
	assertInvalidResetToken(t, service, tokens[0])
	consumedUserID, err := service.ConsumePasswordResetToken(ctx, tokens[1])
	if err != nil || consumedUserID != userID {
		t.Fatalf("newest reset token consume = user:%d err:%v", consumedUserID, err)
	}
	assertInvalidResetToken(t, service, tokens[1])

	if err := service.issuePasswordResetToken(ctx, userID, tokens[2]); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			resultUserID, err := service.ConsumePasswordResetToken(context.Background(), tokens[2])
			if err == nil && resultUserID != userID {
				err = errors.New("password reset token returned wrong user")
			}
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	var successes, invalid int
	for result := range results {
		switch {
		case result == nil:
			successes++
		case isInvalidResetToken(result):
			invalid++
		default:
			t.Fatalf("concurrent reset token consume error = %v", result)
		}
	}
	if successes != 1 || invalid != 1 {
		t.Fatalf("concurrent reset token results = success:%d invalid:%d", successes, invalid)
	}
}

func TestRefreshRotationClaimRedisIntegration(t *testing.T) {
	if os.Getenv("AUTH_REDIS_INTEGRATION") != "1" {
		t.Skip("set AUTH_REDIS_INTEGRATION=1 to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{
		Addr: cfg.Redis.Addr(), DB: cfg.Redis.Database, Password: cfg.Redis.Password,
	})
	t.Cleanup(func() { _ = client.Close() })
	service := &Service{rdb: client}
	const jti = "refresh-rotation-integration-jti"
	t.Cleanup(func() {
		_ = client.Del(context.Background(), refreshRevokedKey(jti), refreshRotationGraceKey(jti)).Err()
	})

	results := make(chan struct {
		claim refreshRotationClaim
		err   error
	}, 32)
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			claim, err := service.claimRefreshRotation(context.Background(), jti, time.Now().Add(time.Minute))
			results <- struct {
				claim refreshRotationClaim
				err   error
			}{claim: claim, err: err}
		}()
	}
	wait.Wait()
	close(results)
	claimedCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.claim == refreshRotationClaimed {
			claimedCount++
		}
	}
	if claimedCount != 1 {
		t.Fatalf("refresh rotation claims = %d, want 1", claimedCount)
	}
}

func TestRefreshRotationRestoresClaimAfterDatabaseFailureRedisIntegration(t *testing.T) {
	if os.Getenv("AUTH_REDIS_INTEGRATION") != "1" {
		t.Skip("set AUTH_REDIS_INTEGRATION=1 to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr(), DB: cfg.Redis.Database, Password: cfg.Redis.Password})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("mysql", "root:root@tcp(127.0.0.1:1)/unreachable")
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	repo := NewRepository(db)
	jwtService, err := NewJWTService(cfg.Auth)
	if err != nil {
		t.Fatal(err)
	}
	user := User{ID: 919293, Username: "refresh-restore", Status: 1, TokenVersion: 7}
	const ip = "203.0.113.202"
	const userAgent = "BugPk-Api refresh restore integration"
	token, jti, expiresAt, err := jwtService.CreateRefreshToken(user, DeviceFingerprint(ip, userAgent), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	key := refreshRevokedKey(jti)
	graceKey := refreshRotationGraceKey(jti)
	t.Cleanup(func() { _ = client.Del(context.Background(), key, graceKey).Err() })
	limiter := NewRateLimiter(client, cfg.Auth.Secret)
	minuteKey := "auth:refresh:ip:minute:" + limiter.rateLimitIPIdentity(ip)
	hourKey := "auth:refresh:ip:hour:" + limiter.rateLimitIPIdentity(ip)
	t.Cleanup(func() { _ = client.Del(context.Background(), minuteKey, hourKey).Err() })
	service := NewService(cfg.Auth, repo, nil, jwtService, limiter, client, nil)
	if _, err := service.Refresh(context.Background(), token, ip, userAgent); err == nil {
		t.Fatal("refresh unexpectedly succeeded with a closed database")
	}
	claimed, err := service.claimRefreshRotation(context.Background(), jti, expiresAt)
	if err != nil || claimed != refreshRotationClaimed {
		t.Fatalf("refresh claim was not restored after database failure: claim=%v err=%v", claimed, err)
	}
}

func TestRefreshRotationRestoreDoesNotOverrideLogoutRedisIntegration(t *testing.T) {
	if os.Getenv("AUTH_REDIS_INTEGRATION") != "1" {
		t.Skip("set AUTH_REDIS_INTEGRATION=1 to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr(), DB: cfg.Redis.Database, Password: cfg.Redis.Password})
	t.Cleanup(func() { _ = client.Close() })
	const jti = "refresh-restore-must-not-overwrite-logout"
	key := refreshRevokedKey(jti)
	t.Cleanup(func() { _ = client.Del(context.Background(), key).Err() })
	if err := client.Set(context.Background(), key, "logout", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	service := &Service{rdb: client}
	wantErr := errors.New("database unavailable")
	if err := service.restoreRefreshRotationAfterFailure(jti, wantErr); !errors.Is(err, wantErr) {
		t.Fatalf("restore returned %v, want original error", err)
	}
	if value, err := client.Get(context.Background(), key).Result(); err != nil || value != "logout" {
		t.Fatalf("logout revocation was overwritten: value=%q err=%v", value, err)
	}
}

func TestRefreshRateLimitRedisIntegration(t *testing.T) {
	if os.Getenv("AUTH_REDIS_INTEGRATION") != "1" {
		t.Skip("set AUTH_REDIS_INTEGRATION=1 to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr(),
		DB:       cfg.Redis.Database,
		Password: cfg.Redis.Password,
	})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	const ip = "203.0.113.201"
	limiter := NewRateLimiter(client, cfg.Auth.Secret)
	minuteKey := "auth:refresh:ip:minute:" + limiter.rateLimitIPIdentity(ip)
	hourKey := "auth:refresh:ip:hour:" + limiter.rateLimitIPIdentity(ip)
	t.Cleanup(func() { _ = client.Del(context.Background(), minuteKey, hourKey).Err() })
	if err := client.Set(ctx, minuteKey, 120, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	err = limiter.CheckRefresh(ctx, ip)
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusTooManyRequests {
		t.Fatalf("refresh rate limit error = %v, want 429", err)
	}
}

func TestPasswordResetTokenRestoreRedisIntegration(t *testing.T) {
	if os.Getenv("AUTH_REDIS_INTEGRATION") != "1" {
		t.Skip("set AUTH_REDIS_INTEGRATION=1 to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{Addr: cfg.Redis.Addr(), DB: cfg.Redis.Database, Password: cfg.Redis.Password})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	service := &VerificationCodeService{rdb: client}
	const userID int64 = 909102
	const oldToken = "restore-reset-token-old"
	const newToken = "restore-reset-token-new"
	t.Cleanup(func() {
		_ = client.Del(context.Background(), passwordResetUserKey(userID), passwordResetTokenKey(oldToken), passwordResetTokenKey(newToken)).Err()
	})

	if err := service.issuePasswordResetToken(ctx, userID, oldToken); err != nil {
		t.Fatal(err)
	}
	claim, err := service.ClaimPasswordResetToken(ctx, oldToken)
	remaining := time.Until(claim.ExpiresAt)
	if err != nil || claim.UserID != userID || remaining <= 0 || remaining > passwordResetTokenTTL {
		t.Fatalf("reset claim = %+v, err=%v", claim, err)
	}
	if err := service.RestorePasswordResetToken(ctx, oldToken, claim); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	if restoredUserID, err := service.PeekPasswordResetToken(ctx, oldToken); err != nil || restoredUserID != userID {
		t.Fatalf("restored token peek = user:%d err:%v", restoredUserID, err)
	}

	claim, err = service.ClaimPasswordResetToken(ctx, oldToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.issuePasswordResetToken(ctx, userID, newToken); err != nil {
		t.Fatal(err)
	}
	if err := service.RestorePasswordResetToken(ctx, oldToken, claim); err == nil {
		t.Fatal("superseded reset token was restored over a newer token")
	}
	assertInvalidResetToken(t, service, oldToken)
	if newestUserID, err := service.PeekPasswordResetToken(ctx, newToken); err != nil || newestUserID != userID {
		t.Fatalf("newest token was damaged by stale restore: user:%d err:%v", newestUserID, err)
	}
}

func assertInvalidResetToken(t *testing.T, service *VerificationCodeService, token string) {
	t.Helper()
	if _, err := service.ConsumePasswordResetToken(context.Background(), token); !isInvalidResetToken(err) {
		t.Fatalf("reset token %q error = %v, want invalid", token, err)
	}
}

func isInvalidResetToken(err error) bool {
	var business *apperrors.BusinessError
	return errors.As(err, &business) && business.StatusCode == consts.StatusBadRequest
}
