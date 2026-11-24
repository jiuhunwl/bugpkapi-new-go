package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

type verificationCodeRepositoryStub struct {
	consumeEmailCalled bool
	createdHash        string
	emailConfig        config.EmailMessageConfig
	emailConfigErr     error
	emailConfigOK      bool
	revokeCtxErr       error
	revokeErr          error
	revokedHash        string
	registerConfig     RegisterConfig
	registerConfigErr  error
	siteName           string
	siteNameErr        error
	whitelist          []string
	blacklist          []string
	domainRulesErr     error
	freshCode          bool
	freshCodeErr       error
}

func (s *verificationCodeRepositoryStub) EmailDomainRules(context.Context) ([]string, []string, error) {
	return s.whitelist, s.blacklist, s.domainRulesErr
}

func (s *verificationCodeRepositoryStub) HasFreshEmailCode(context.Context, string, string, time.Time) (bool, error) {
	return s.freshCode, s.freshCodeErr
}

func (s *verificationCodeRepositoryStub) ConsumeEmailCode(context.Context, string, string, string) error {
	s.consumeEmailCalled = true
	return nil
}

func (s *verificationCodeRepositoryStub) ConsumeMobileCode(context.Context, string, string, string) error {
	return nil
}

func (s *verificationCodeRepositoryStub) CreateEmailCode(_ context.Context, _, _, codeHash string, _ time.Time) error {
	s.createdHash = codeHash
	return nil
}

func (s *verificationCodeRepositoryStub) EmailMessageConfig(context.Context) (config.EmailMessageConfig, bool, error) {
	return s.emailConfig, s.emailConfigOK, s.emailConfigErr
}

func (s *verificationCodeRepositoryStub) RegisterConfig(context.Context) (RegisterConfig, error) {
	return s.registerConfig, s.registerConfigErr
}

func (s *verificationCodeRepositoryStub) RevokeEmailCode(ctx context.Context, _, _, codeHash string) error {
	s.revokeCtxErr = ctx.Err()
	s.revokedHash = codeHash
	return s.revokeErr
}

func (s *verificationCodeRepositoryStub) SiteName(context.Context) (string, error) {
	return s.siteName, s.siteNameErr
}

func TestEmailConfigUsesConfiguredSiteNameForDefaultBrand(t *testing.T) {
	repo := &verificationCodeRepositoryStub{siteName: "Acme API"}
	service := &VerificationCodeService{
		cfg: config.MessageConfig{Email: config.EmailMessageConfig{
			CodeSubject:   "BugPk-Api 验证码",
			CodeTemplate:  "{{siteName}} 验证码 {{code}}",
			ResetSubject:  "{{siteName}} 找回密码",
			ResetTemplate: "{{siteName}} 密码重置 {{code}}",
			SMTPFromName:  "BugPk-Api",
		}},
		repo: repo,
	}
	cfg, err := service.emailConfig(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMTPFromName != "Acme API" || cfg.CodeSubject != "Acme API 验证码" || cfg.ResetSubject != "Acme API 找回密码" {
		t.Fatalf("email brand was not updated: %+v", cfg)
	}
	if cfg.CodeTemplate != "Acme API 验证码 {{code}}" || cfg.ResetTemplate != "Acme API 密码重置 {{code}}" {
		t.Fatalf("email template siteName placeholder was not rendered: %+v", cfg)
	}
}

func TestEmailConfigFailsClosedWhenSiteNameCannotBeLoaded(t *testing.T) {
	siteErr := errors.New("site config unavailable")
	service := &VerificationCodeService{
		cfg:  config.MessageConfig{Email: config.EmailMessageConfig{Enabled: true}},
		repo: &verificationCodeRepositoryStub{siteNameErr: siteErr},
	}
	_, err := service.emailConfig(context.Background(), "")
	if !errors.Is(err, siteErr) {
		t.Fatalf("emailConfig() error = %v, want %v", err, siteErr)
	}
}

func TestTargetSendLimitFailsClosedWhenRedisUnavailable(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond,
		MaxRetries: -1, ReadTimeout: 20 * time.Millisecond, WriteTimeout: 20 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	service := &VerificationCodeService{limiter: NewRateLimiter(client, "verification-code-test-secret-with-at-least-thirty-two-bytes")}
	err := service.checkTargetSendLimit(context.Background(), "user@example.com")
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusServiceUnavailable {
		t.Fatalf("checkTargetSendLimit error = %v, want service unavailable", err)
	}
}

func TestTargetSendLimitUsesHashedRedisKey(t *testing.T) {
	source := targetRateLimitKey("User@Example.com")
	if strings.Contains(source, "user@example.com") || len(source) != 64 {
		t.Fatalf("targetRateLimitKey leaked target or has wrong length: %q", source)
	}
}

func TestPreparePasswordResetEmailAppliesIPLimitBeforeRecipientLookup(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond,
		MaxRetries: -1, ReadTimeout: 20 * time.Millisecond, WriteTimeout: 20 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	repo := &verificationCodeRepositoryStub{
		registerConfig: RegisterConfig{VerificationCodeIPMinuteLimit: 1},
	}
	service := &VerificationCodeService{
		cfg: config.MessageConfig{Email: config.EmailMessageConfig{
			Enabled: true, Provider: "smtp",
		}},
		repo:    repo,
		limiter: NewRateLimiter(client, "password-reset-enumeration-test-secret"),
	}
	_, err := service.PreparePasswordResetEmail(context.Background(), "203.0.113.9", "")
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusServiceUnavailable {
		t.Fatalf("PreparePasswordResetEmail error = %v, want service unavailable", err)
	}
}

func TestPreparePasswordResetEmailRejectsDisabledSMTPConsistently(t *testing.T) {
	service := &VerificationCodeService{
		cfg:     config.MessageConfig{Email: config.EmailMessageConfig{Enabled: false, Provider: "smtp"}},
		repo:    &verificationCodeRepositoryStub{},
		limiter: NewRateLimiter(nil, "password-reset-enumeration-test-secret"),
	}
	_, err := service.PreparePasswordResetEmail(context.Background(), "203.0.113.9", "")
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusBadRequest {
		t.Fatalf("PreparePasswordResetEmail error = %v, want bad request", err)
	}
}

func TestEmailSendFailureRevokesPersistedCodeWithDetachedContext(t *testing.T) {
	repo := &verificationCodeRepositoryStub{}
	sendErr := errors.New("smtp delivery failed")
	service := &VerificationCodeService{
		authCfg: config.AuthConfig{Secret: "verification-test-secret"},
		repo:    repo,
		sendMail: func(context.Context, config.EmailMessageConfig, string, string, string) error {
			return sendErr
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := service.persistAndSendEmailCode(
		ctx,
		config.EmailMessageConfig{},
		emailSceneRegister,
		"user@example.com",
		"subject",
		"code={{code}}",
		"123456",
		10*time.Minute,
		time.Now().Add(10*time.Minute),
	)
	if !errors.Is(err, sendErr) {
		t.Fatalf("send failure error = %v, want SMTP cause", err)
	}
	if repo.createdHash == "" || repo.revokedHash != repo.createdHash {
		t.Fatalf("created/revoked hashes are inconsistent: %q %q", repo.createdHash, repo.revokedHash)
	}
	if repo.revokeCtxErr != nil {
		t.Fatalf("revoke inherited canceled request context: %v", repo.revokeCtxErr)
	}
}

func TestEmailSendAndRevokeFailurePreservesBothCauses(t *testing.T) {
	sendErr := errors.New("smtp delivery failed")
	revokeErr := errors.New("database revoke failed")
	repo := &verificationCodeRepositoryStub{revokeErr: revokeErr}
	service := &VerificationCodeService{
		authCfg: config.AuthConfig{Secret: "verification-test-secret"},
		repo:    repo,
		sendMail: func(context.Context, config.EmailMessageConfig, string, string, string) error {
			return sendErr
		},
	}
	err := service.persistAndSendEmailCode(
		context.Background(),
		config.EmailMessageConfig{},
		emailSceneBind,
		"user@example.com",
		"subject",
		"code={{code}}",
		"123456",
		10*time.Minute,
		time.Now().Add(10*time.Minute),
	)
	if !errors.Is(err, sendErr) || !errors.Is(err, revokeErr) {
		t.Fatalf("combined failure did not preserve both causes: %v", err)
	}
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.Message != "邮件验证码发送失败且撤销失败" {
		t.Fatalf("combined failure is not explicit: %v", err)
	}
}

func TestSuccessfulEmailDeliveryKeepsCodeActive(t *testing.T) {
	repo := &verificationCodeRepositoryStub{}
	service := &VerificationCodeService{
		authCfg: config.AuthConfig{Secret: "verification-test-secret"},
		repo:    repo,
		sendMail: func(context.Context, config.EmailMessageConfig, string, string, string) error {
			return nil
		},
	}
	if err := service.persistAndSendEmailCode(
		context.Background(),
		config.EmailMessageConfig{},
		emailScenePasswordReset,
		"user@example.com",
		"subject",
		"code={{code}}",
		"123456",
		10*time.Minute,
		time.Now().Add(10*time.Minute),
	); err != nil {
		t.Fatal(err)
	}
	if repo.createdHash == "" || repo.revokedHash != "" {
		t.Fatalf("successful delivery code state is incorrect: created=%q revoked=%q", repo.createdHash, repo.revokedHash)
	}
}

func TestPasswordResetVerificationFailsBeforeConsumingCodeWithoutRedis(t *testing.T) {
	repo := &verificationCodeRepositoryStub{}
	email := "user@example.com"
	service := &VerificationCodeService{repo: repo}
	_, err := service.VerifyPasswordResetEmail(context.Background(), User{ID: 7, Email: &email}, "123456")
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusServiceUnavailable {
		t.Fatalf("password reset without Redis error = %v", err)
	}
	if repo.consumeEmailCalled {
		t.Fatal("password reset email code was consumed before Redis availability was checked")
	}
}

func TestPasswordResetTokenScriptsEnforceSingleActiveToken(t *testing.T) {
	for _, fragment := range []string{
		"local previous = redis.call('GET', KEYS[2])",
		"redis.call('DEL', previous)",
		"redis.call('SET', KEYS[1]",
		"redis.call('SET', KEYS[2], KEYS[1]",
	} {
		if !strings.Contains(issuePasswordResetTokenLua, fragment) {
			t.Fatalf("issue script lost %q", fragment)
		}
	}
	for _, fragment := range []string{
		"string.match(user_id, '^[1-9][0-9]*$')",
		"active_token_key ~= KEYS[1]",
		"redis.call('DEL', KEYS[1])",
		"redis.call('DEL', user_key)",
	} {
		if !strings.Contains(consumePasswordResetTokenLua, fragment) {
			t.Fatalf("consume script lost %q", fragment)
		}
	}
	for _, fragment := range []string{
		"local user_id = redis.call('GET', KEYS[1])",
		"redis.call('GET', user_key) ~= KEYS[1]",
		"return user_id",
	} {
		if !strings.Contains(peekPasswordResetTokenLua, fragment) {
			t.Fatalf("peek script lost %q", fragment)
		}
	}
}

func TestPasswordResetTokenOperationsFailClosedWhenRedisUnavailable(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond,
		MaxRetries: -1, ReadTimeout: 20 * time.Millisecond, WriteTimeout: 20 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	service := &VerificationCodeService{rdb: client}
	if err := service.issuePasswordResetToken(context.Background(), 7, "new-reset-token"); err == nil {
		t.Fatal("password reset token issuance succeeded while Redis was unavailable")
	}
	_, err := service.ConsumePasswordResetToken(context.Background(), "new-reset-token")
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusServiceUnavailable {
		t.Fatalf("password reset token consume error = %v", err)
	}
	_, err = service.PeekPasswordResetToken(context.Background(), "new-reset-token")
	business, ok = apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusServiceUnavailable {
		t.Fatalf("password reset token peek error = %v", err)
	}
	if err := service.RestorePasswordResetToken(context.Background(), "new-reset-token", PasswordResetTokenClaim{UserID: 7, ExpiresAt: time.Now().Add(time.Minute)}); err == nil {
		t.Fatal("password reset token restore succeeded while Redis was unavailable")
	}
}

func TestExpiredPasswordResetClaimCannotBeRestored(t *testing.T) {
	service := &VerificationCodeService{}
	err := service.RestorePasswordResetToken(context.Background(), "expired-token", PasswordResetTokenClaim{
		ExpiresAt: time.Now().Add(-time.Second),
		UserID:    7,
	})
	if err == nil {
		t.Fatal("expired password reset claim was accepted for restoration")
	}
}

func TestRefreshRotationClaimFailsClosedWithoutRedis(t *testing.T) {
	service := &Service{}
	if claim, err := service.claimRefreshRotation(context.Background(), "jti", time.Now().Add(time.Minute)); claim != refreshRotationDenied {
		t.Fatal("refresh rotation was claimed without Redis")
	} else if business, ok := apperrors.AsBusinessError(err); !ok || business.StatusCode != consts.StatusServiceUnavailable {
		t.Fatalf("refresh rotation without Redis error = %v", err)
	}
}
