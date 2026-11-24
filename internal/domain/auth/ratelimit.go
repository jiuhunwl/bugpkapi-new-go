package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"strings"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/redisutil"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

type RateLimiter struct {
	rdb       *redis.Client
	digestKey []byte
}

func NewRateLimiter(rdb *redis.Client, secret string) *RateLimiter {
	seed := sha256.Sum256([]byte("BugPk-Api:auth-rate-limit:v1\x00" + secret))
	return &RateLimiter{rdb: rdb, digestKey: seed[:]}
}

func (r *RateLimiter) CheckBeforeLogin(ctx context.Context, ip, account string) error {
	if err := r.checkCurrent(ctx, "auth:login:fail:ip:minute:"+r.rateLimitIPIdentity(ip), 20, "login attempts are too frequent, please try again later"); err != nil {
		return err
	}
	if err := r.checkCurrent(ctx, "auth:login:fail:ip:hour:"+r.rateLimitIPIdentity(ip), 100, "too many login attempts from this IP, please try again later"); err != nil {
		return err
	}
	locked, err := r.rdb.Get(ctx, "auth:login:lock:account:"+r.rateLimitAccountIdentity(account)).Result()
	if err != nil && err != redis.Nil {
		return rateLimitUnavailable("login rate limit service unavailable, please try again later", err)
	}
	if locked != "" {
		return apperrors.New(consts.StatusTooManyRequests, "too many failed login attempts, please try again later")
	}
	return nil
}

func (r *RateLimiter) RecordLoginFailure(ctx context.Context, ip, account string) error {
	if _, err := r.incr(ctx, "auth:login:fail:ip:minute:"+r.rateLimitIPIdentity(ip), time.Minute); err != nil {
		return err
	}
	if _, err := r.incr(ctx, "auth:login:fail:ip:hour:"+r.rateLimitIPIdentity(ip), time.Hour); err != nil {
		return err
	}
	count, err := r.incr(ctx, "auth:login:fail:account:"+r.rateLimitAccountIdentity(account), 15*time.Minute)
	if err != nil {
		return err
	}
	if count >= 5 {
		if err := r.rdb.Set(ctx, "auth:login:lock:account:"+r.rateLimitAccountIdentity(account), "1", 15*time.Minute).Err(); err != nil {
			return rateLimitUnavailable("login rate limit service unavailable, please try again later", err)
		}
	}
	return nil
}

func (r *RateLimiter) RecordLoginSuccess(ctx context.Context, account string) error {
	if err := r.rdb.Del(ctx,
		"auth:login:fail:account:"+r.rateLimitAccountIdentity(account),
		"auth:login:lock:account:"+r.rateLimitAccountIdentity(account),
	).Err(); err != nil {
		return rateLimitUnavailable("login rate limit service unavailable, please try again later", err)
	}
	return nil
}

func (r *RateLimiter) CheckRegister(ctx context.Context, ip string, cfg RegisterConfig) error {
	return r.CheckRegisterChannel(ctx, "local", ip, cfg.RegisterIPHourLimit, cfg.RegisterIPDayLimit)
}

func (r *RateLimiter) CheckRegisterChannel(ctx context.Context, channel, ip string, hourLimit, dayLimit int) error {
	if hourLimit <= 0 && dayLimit <= 0 {
		return nil
	}
	identity := r.rateLimitIPIdentity(ip)
	channel = strings.TrimSpace(channel)
	if channel == "" {
		channel = "local"
	}
	if hourLimit > 0 {
		count, err := r.incr(ctx, "auth:register:"+channel+":submit:ip:hour:"+identity, time.Hour)
		if err != nil {
			return err
		}
		if int(count) > hourLimit {
			return apperrors.New(consts.StatusTooManyRequests, "registration attempts are too frequent, please try again later")
		}
	}
	if dayLimit > 0 {
		count, err := r.incr(ctx, "auth:register:"+channel+":submit:ip:day:"+identity, 24*time.Hour)
		if err != nil {
			return err
		}
		if int(count) > dayLimit {
			return apperrors.New(consts.StatusTooManyRequests, "registration attempts are too frequent, please try again later")
		}
	}
	return nil
}

func (r *RateLimiter) CheckCaptchaVerify(ctx context.Context, ip string) error {
	count, err := r.incr(ctx, "auth:captcha:verify:ip:"+r.rateLimitIPIdentity(ip), time.Minute)
	if err != nil {
		return err
	}
	if count > 60 {
		return apperrors.New(consts.StatusTooManyRequests, "operation is too frequent, please try again later")
	}
	return nil
}

// CheckCaptchaIssue 限制验证码签发本身。注册机批量取 challenge 是前置步骤，
// 不限住这一步，攻击者可以低成本占用 Redis、消耗 POW 签发与前端求解资源。
func (r *RateLimiter) CheckCaptchaIssue(ctx context.Context, ip string) error {
	identity := r.rateLimitIPIdentity(ip)
	minuteCount, err := r.incr(ctx, "auth:captcha:issue:ip:minute:"+identity, time.Minute)
	if err != nil {
		return err
	}
	if minuteCount > 30 {
		return apperrors.New(consts.StatusTooManyRequests, "验证码请求过于频繁，请稍后再试")
	}
	hourCount, err := r.incr(ctx, "auth:captcha:issue:ip:hour:"+identity, time.Hour)
	if err != nil {
		return err
	}
	if hourCount > 300 {
		return apperrors.New(consts.StatusTooManyRequests, "验证码请求次数过多，请一小时后再试")
	}
	return nil
}

func (r *RateLimiter) CheckRefresh(ctx context.Context, ip string) error {
	ip = r.rateLimitIPIdentity(ip)
	minuteCount, err := r.incr(ctx, "auth:refresh:ip:minute:"+ip, time.Minute)
	if err != nil {
		return err
	}
	if minuteCount > 120 {
		return apperrors.New(consts.StatusTooManyRequests, "刷新请求过于频繁，请稍后再试")
	}
	hourCount, err := r.incr(ctx, "auth:refresh:ip:hour:"+ip, time.Hour)
	if err != nil {
		return err
	}
	if hourCount > 1000 {
		return apperrors.New(consts.StatusTooManyRequests, "刷新请求次数过多，请稍后再试")
	}
	return nil
}

func (r *RateLimiter) CheckPasswordResetAccount(ctx context.Context, ip string) error {
	ip = r.rateLimitIPIdentity(ip)
	minuteCount, err := r.incr(ctx, "auth:password-reset:account:ip:minute:"+ip, time.Minute)
	if err != nil {
		return err
	}
	if minuteCount > 20 {
		return apperrors.New(consts.StatusTooManyRequests, "操作过于频繁，请稍后再试")
	}
	hourCount, err := r.incr(ctx, "auth:password-reset:account:ip:hour:"+ip, time.Hour)
	if err != nil {
		return err
	}
	if hourCount > 100 {
		return apperrors.New(consts.StatusTooManyRequests, "操作次数过多，请一小时后再试")
	}
	return nil
}

// CheckPasswordResetSend 限制「同一账号每天最多触发多少次找回密码邮件」。
// 已有的 IP 维度限制挡不住分布式攻击：攻击者换 IP 后可以无上限地给
// 同一个管理员邮箱发重置邮件（骚扰 + 撞验证码）。per-account 维度
// 必须存在。返回本次计数，调用方在达到阈值时可以叠加告警。
func (r *RateLimiter) CheckPasswordResetSend(ctx context.Context, ip, account string, dayLimit int) (int64, error) {
	if dayLimit <= 0 {
		dayLimit = 5
	}
	count, err := r.incr(ctx, "auth:password-reset:send:account:day:"+r.rateLimitAccountIdentity(account), 24*time.Hour)
	if err != nil {
		return 0, err
	}
	if count > int64(dayLimit) {
		return count, apperrors.New(consts.StatusTooManyRequests, "找回密码请求次数过多，请明日再试")
	}
	return count, nil
}

// CheckTwoFAVerify 限制 2FA 验证码尝试频率：验证码只有 10^6 空间且
// 窗口 30s，必须按账号+IP 双维度限速，否则 TOTP 会被在线爆破。
func (r *RateLimiter) CheckTwoFAVerify(ctx context.Context, ip, account string) error {
	ipID := r.rateLimitIPIdentity(ip)
	minuteCount, err := r.incr(ctx, "auth:2fa:verify:ip:minute:"+ipID, time.Minute)
	if err != nil {
		return err
	}
	if minuteCount > 10 {
		return apperrors.New(consts.StatusTooManyRequests, "验证码尝试过于频繁，请稍后再试")
	}
	count, err := r.incr(ctx, "auth:2fa:verify:account:"+r.rateLimitAccountIdentity(account), 15*time.Minute)
	if err != nil {
		return err
	}
	if count > 20 {
		return apperrors.New(consts.StatusTooManyRequests, "验证码错误次数过多，账号验证已临时锁定")
	}
	return nil
}

func (r *RateLimiter) checkCurrent(ctx context.Context, key string, limit int64, message string) error {
	raw, err := r.rdb.Get(ctx, key).Int64()
	if err == redis.Nil {
		return nil
	}
	if err != nil {
		return rateLimitUnavailable("login rate limit service unavailable, please try again later", err)
	}
	if raw > limit {
		return apperrors.New(consts.StatusTooManyRequests, message)
	}
	return nil
}

func (r *RateLimiter) incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	count, err := redisutil.IncrWithTTL(ctx, r.rdb, key, ttl)
	if err != nil {
		return 0, rateLimitUnavailable("rate limit service unavailable, please try again later", err)
	}
	return count, nil
}

func rateLimitUnavailable(message string, err error) error {
	return apperrors.Wrap(consts.StatusServiceUnavailable, message, err)
}

func (r *RateLimiter) rateLimitIPIdentity(ip string) string {
	value := strings.TrimSpace(ip)
	if parsed := net.ParseIP(value); parsed != nil {
		value = parsed.String()
	}
	if value == "" {
		value = "unknown"
	}
	return r.rateLimitDigest("ip", value)
}

func (r *RateLimiter) rateLimitAccountIdentity(account string) string {
	value := strings.ToLower(strings.TrimSpace(account))
	if value == "" {
		value = "unknown"
	}
	return r.rateLimitDigest("account", value)
}

func (r *RateLimiter) rateLimitDigest(scope, value string) string {
	mac := hmac.New(sha256.New, r.digestKey)
	_, _ = mac.Write([]byte(scope + "\x00" + value))
	return hex.EncodeToString(mac.Sum(nil))
}

func refreshRevokedKey(jti string) string {
	return fmt.Sprintf("auth:refresh:revoked:%s", jti)
}

func refreshRotationGraceKey(jti string) string {
	return fmt.Sprintf("auth:refresh:grace:%s", jti)
}

func (r *RateLimiter) CheckOAuthAuthorize(ctx context.Context, ip string) error {
	identity := r.rateLimitIPIdentity(ip)
	minuteCount, err := r.incr(ctx, "auth:oauth:authorize:ip:minute:"+identity, time.Minute)
	if err != nil {
		return err
	}
	if minuteCount > 20 {
		return apperrors.New(consts.StatusTooManyRequests, "第三方登录请求过于频繁，请稍后再试")
	}
	hourCount, err := r.incr(ctx, "auth:oauth:authorize:ip:hour:"+identity, time.Hour)
	if err != nil {
		return err
	}
	if hourCount > 200 {
		return apperrors.New(consts.StatusTooManyRequests, "第三方登录请求次数过多，请稍后再试")
	}
	return nil
}

func (r *RateLimiter) CheckOAuthCallback(ctx context.Context, ip string) error {
	count, err := r.incr(ctx, "auth:oauth:callback:ip:minute:"+r.rateLimitIPIdentity(ip), time.Minute)
	if err != nil {
		return err
	}
	if count > 60 {
		return apperrors.New(consts.StatusTooManyRequests, "第三方登录回调过于频繁，请稍后再试")
	}
	return nil
}

func (r *RateLimiter) CheckOAuthExchange(ctx context.Context, ip string) error {
	count, err := r.incr(ctx, "auth:oauth:exchange:ip:minute:"+r.rateLimitIPIdentity(ip), time.Minute)
	if err != nil {
		return err
	}
	if count > 30 {
		return apperrors.New(consts.StatusTooManyRequests, "登录凭证校验过于频繁，请稍后再试")
	}
	return nil
}

func (r *RateLimiter) CheckOAuthComplete(ctx context.Context, ip string) error {
	count, err := r.incr(ctx, "auth:oauth:complete:ip:minute:"+r.rateLimitIPIdentity(ip), time.Minute)
	if err != nil {
		return err
	}
	if count > 20 {
		return apperrors.New(consts.StatusTooManyRequests, "第三方注册请求过于频繁，请稍后再试")
	}
	return nil
}
