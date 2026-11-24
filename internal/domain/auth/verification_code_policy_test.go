package auth

import (
	"context"
	"testing"
	"time"

	"bugpk-api/internal/config"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

// newOutboundTestService 构造一个带真实（内存）Redis 的发码服务，
// 并把 sendMail 换成计数器，便于断言「到底有没有真的发信」。
type outboundTestService struct {
	service *VerificationCodeService
	repo    *verificationCodeRepositoryStub
	sent    int
}

func newOutboundTestService(t *testing.T, mutate func(*VerificationCodeService, *verificationCodeRepositoryStub)) *outboundTestService {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	repo := &verificationCodeRepositoryStub{}
	holder := &outboundTestService{repo: repo}
	service := &VerificationCodeService{
		cfg:     config.MessageConfig{Email: config.EmailMessageConfig{Enabled: true, Provider: "smtp"}},
		repo:    repo,
		limiter: NewRateLimiter(rdb, "outbound-email-policy-test-secret"),
		now:     time.Now,
	}
	service.sendMail = func(context.Context, config.EmailMessageConfig, string, string, string) error {
		holder.sent++
		return nil
	}
	if mutate != nil {
		mutate(service, repo)
	}
	holder.service = service
	return holder
}

func statusOf(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return consts.StatusOK
	}
	if business, ok := apperrors.AsBusinessError(err); ok {
		return business.StatusCode
	}
	t.Fatalf("error is not a business error: %v", err)
	return 0
}

// 白名单非空时，绑定邮箱也必须受约束：这正是「后台加了白名单还能给
// example.com 发验证码」的根因。
func TestSendEmailCodeRejectsDomainOutsideWhitelist(t *testing.T) {
	holder := newOutboundTestService(t, func(_ *VerificationCodeService, repo *verificationCodeRepositoryStub) {
		repo.whitelist = []string{"qq.com", "163.com"}
	})
	err := holder.service.sendEmailCode(
		context.Background(),
		holder.service.cfg.Email,
		emailSceneBind,
		"poc_fixed@example.com",
		"203.0.113.9",
		"subject",
		"code={{code}}",
	)
	if got := statusOf(t, err); got != consts.StatusForbidden {
		t.Fatalf("status = %d, want 403 (whitelist must reject example.com)", got)
	}
	if holder.sent != 0 {
		t.Fatalf("outbound mail sent %d times, want 0", holder.sent)
	}
}

func TestSendEmailCodeAllowsWhitelistedDomain(t *testing.T) {
	holder := newOutboundTestService(t, func(_ *VerificationCodeService, repo *verificationCodeRepositoryStub) {
		repo.whitelist = []string{"qq.com"}
	})
	if err := holder.service.sendEmailCode(
		context.Background(),
		holder.service.cfg.Email,
		emailSceneBind,
		"user@qq.com",
		"203.0.113.9",
		"subject",
		"code={{code}}",
	); err != nil {
		t.Fatalf("whitelisted domain rejected: %v", err)
	}
	if holder.sent != 1 {
		t.Fatalf("outbound mail sent %d times, want 1", holder.sent)
	}
}

// 黑名单在「非严格模式」（找回密码）下同样拦，白名单不拦，避免老账号被锁死。
func TestPasswordResetAppliesBlacklistButNotWhitelist(t *testing.T) {
	holder := newOutboundTestService(t, func(_ *VerificationCodeService, repo *verificationCodeRepositoryStub) {
		repo.whitelist = []string{"qq.com"}
		repo.blacklist = []string{"*.mailinator.com"}
	})
	ctx := context.Background()
	cfg := holder.service.cfg.Email

	if err := holder.service.SendPreparedPasswordResetEmail(ctx, cfg, "old@example.com"); err != nil {
		t.Fatalf("non-whitelisted legacy address must still receive reset mail: %v", err)
	}
	err := holder.service.SendPreparedPasswordResetEmail(ctx, cfg, "trash@ab.mailinator.com")
	if got := statusOf(t, err); got != consts.StatusForbidden {
		t.Fatalf("status = %d, want 403 for blacklisted domain", got)
	}
	if holder.sent != 1 {
		t.Fatalf("outbound mail sent %d times, want 1", holder.sent)
	}
}

// 冷却窗口内重复请求不再发信，直接返回成功（幂等）。
func TestSendEmailCodeReusesFreshCodeWithoutSending(t *testing.T) {
	holder := newOutboundTestService(t, func(_ *VerificationCodeService, repo *verificationCodeRepositoryStub) {
		repo.freshCode = true
	})
	if err := holder.service.sendEmailCode(
		context.Background(),
		holder.service.cfg.Email,
		emailSceneRegister,
		"user@qq.com",
		"203.0.113.9",
		"subject",
		"code={{code}}",
	); err != nil {
		t.Fatalf("reuse path must return success: %v", err)
	}
	if holder.sent != 0 {
		t.Fatalf("outbound mail sent %d times, want 0 while reusing", holder.sent)
	}
}

// 全站熔断：一分钟内超过上限后，即使换收件人也拒。
func TestGlobalSendLimitBlocksAfterMinuteCap(t *testing.T) {
	holder := newOutboundTestService(t, func(service *VerificationCodeService, _ *verificationCodeRepositoryStub) {
		service.cfg.Email.SendGlobalMinuteLimit = 2
		service.cfg.Email.SendGlobalHourLimit = 100
	})
	ctx := context.Background()
	cfg := holder.service.cfg.Email
	for i, email := range []string{"a@qq.com", "b@qq.com"} {
		if err := holder.service.sendEmailCode(ctx, cfg, emailSceneRegister, email, "203.0.113.9", "s", "c={{code}}"); err != nil {
			t.Fatalf("send #%d should pass: %v", i+1, err)
		}
	}
	err := holder.service.sendEmailCode(ctx, cfg, emailSceneRegister, "c@qq.com", "203.0.113.9", "s", "c={{code}}")
	if got := statusOf(t, err); got != consts.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 after global minute cap", got)
	}
	if holder.sent != 2 {
		t.Fatalf("outbound mail sent %d times, want 2", holder.sent)
	}
}

// 后台把 IP 限额配成 0 时不能等于敞开：必须退回内置兜底值。
func TestIPLimitFallsBackWhenAdminConfiguresZero(t *testing.T) {
	holder := newOutboundTestService(t, func(_ *VerificationCodeService, repo *verificationCodeRepositoryStub) {
		repo.registerConfig = RegisterConfig{} // 三个限额全 0
	})
	ctx := context.Background()
	cfg := holder.service.cfg.Email
	var lastErr error
	for i := 0; i < emailCodeIPMinuteFallback+1; i++ {
		lastErr = holder.service.sendEmailCode(ctx, cfg, emailSceneRegister, "user@qq.com", "203.0.113.9", "s", "c={{code}}")
	}
	if got := statusOf(t, lastErr); got != consts.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 once the IP fallback cap is exceeded", got)
	}
}

// 单邮箱日上限兜底。
func TestTargetDayLimitBlocksLongTermHarassment(t *testing.T) {
	holder := newOutboundTestService(t, nil)
	ctx := context.Background()
	// 直接对计数函数施压，避开分钟/小时窗口对同一天内累计的干扰。
	for i := 0; i < emailCodeTargetDayLimit; i++ {
		if _, err := holder.service.limiter.incr(ctx, "auth:code:send:target:day:"+targetRateLimitKey("victim@qq.com"), 24*time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	err := holder.service.checkTargetSendLimit(ctx, "victim@qq.com")
	if got := statusOf(t, err); got != consts.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 after daily target cap", got)
	}
}
