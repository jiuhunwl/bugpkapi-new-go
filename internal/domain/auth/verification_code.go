package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"strconv"
	"strings"
	"time"
	"unicode"

	"bugpk-api/internal/config"
	"bugpk-api/internal/pkg/emailaddr"
	"bugpk-api/internal/pkg/emaillang"
	"bugpk-api/internal/pkg/emailpolicy"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/smtpmail"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

const (
	defaultEmailSiteName    = "BugPk-Api"
	emailSceneRegister      = "register"
	emailSceneBind          = "bind_email"
	emailScenePasswordReset = "password_reset"
	mobileSceneRegister     = "register"
	mobileSceneBind         = "bind_mobile"
	emailRevokeTimeout      = 2 * time.Second
	passwordResetTokenTTL   = 10 * time.Minute
	passwordResetRestoreTTL = 2 * time.Second

	// emailCodeReuseWindow 是「同一场景 + 同一邮箱」的静默复用窗口。
	// 窗口内重复请求不再发新邮件、不再消耗目标邮箱配额，直接返回成功：
	// 既避免用户连点导致的重复投递，也让刷接口的脚本无法靠重放放大发信量。
	// 真实用户真正需要新码时会等过这个窗口，或换 scene。
	emailCodeReuseWindow = 60 * time.Second

	// 目标邮箱维度硬上限（与后台配置无关，防止后台配 0 后彻底敞开）。
	emailCodeTargetDayLimit = 20

	// IP 维度兜底下限：后台把三个限额都配成 0 时使用，避免「关闭限流」变成
	// 「无限发信」。后台配了非 0 值时以配置为准。
	emailCodeIPMinuteFallback = 5
	emailCodeIPHourFallback   = 30
	emailCodeIPDayFallback    = 100

	emailSendGlobalMinuteFallback = 30
	emailSendGlobalHourFallback   = 300
)

// errEmailCodeReused 表示窗口内已有一封未过期验证码，本次请求不再发信。
var errEmailCodeReused = errors.New("email verification code reused within cooldown window")

const issuePasswordResetTokenLua = `
local previous = redis.call('GET', KEYS[2])
if previous then
  redis.call('DEL', previous)
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
redis.call('SET', KEYS[2], KEYS[1], 'PX', ARGV[2])
return 1
`

const consumePasswordResetTokenLua = `
local user_id = redis.call('GET', KEYS[1])
if not user_id then
  return false
end
if not string.match(user_id, '^[1-9][0-9]*$') then
  redis.call('DEL', KEYS[1])
  return false
end
local user_key = ARGV[1] .. user_id
local active_token_key = redis.call('GET', user_key)
if active_token_key ~= KEYS[1] then
  redis.call('DEL', KEYS[1])
  return false
end
local ttl = redis.call('PTTL', KEYS[1])
if ttl <= 0 then
  redis.call('DEL', KEYS[1])
  redis.call('DEL', user_key)
  return false
end
redis.call('DEL', KEYS[1])
redis.call('DEL', user_key)
return user_id .. ':' .. ttl
`

const restorePasswordResetTokenLua = `
if redis.call('EXISTS', KEYS[1]) == 1 or redis.call('EXISTS', KEYS[2]) == 1 then
  return 0
end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
redis.call('SET', KEYS[2], KEYS[1], 'PX', ARGV[2])
return 1
`

const peekPasswordResetTokenLua = `
local user_id = redis.call('GET', KEYS[1])
if not user_id then
  return false
end
if not string.match(user_id, '^[1-9][0-9]*$') then
  return false
end
local user_key = ARGV[1] .. user_id
if redis.call('GET', user_key) ~= KEYS[1] then
  return false
end
return user_id
`

var issuePasswordResetTokenScript = redis.NewScript(issuePasswordResetTokenLua)

var consumePasswordResetTokenScript = redis.NewScript(consumePasswordResetTokenLua)

var restorePasswordResetTokenScript = redis.NewScript(restorePasswordResetTokenLua)

var peekPasswordResetTokenScript = redis.NewScript(peekPasswordResetTokenLua)

type verificationCodeRepository interface {
	ConsumeEmailCode(context.Context, string, string, string) error
	ConsumeMobileCode(context.Context, string, string, string) error
	CreateEmailCode(context.Context, string, string, string, time.Time) error
	EmailDomainRules(context.Context) ([]string, []string, error)
	EmailMessageConfig(context.Context) (config.EmailMessageConfig, bool, error)
	HasFreshEmailCode(context.Context, string, string, time.Time) (bool, error)
	RegisterConfig(context.Context) (RegisterConfig, error)
	RevokeEmailCode(context.Context, string, string, string) error
	SiteName(context.Context) (string, error)
}

type VerificationCodeService struct {
	cfg      config.MessageConfig
	authCfg  config.AuthConfig
	repo     verificationCodeRepository
	limiter  *RateLimiter
	rdb      *redis.Client
	now      func() time.Time
	sendMail func(context.Context, config.EmailMessageConfig, string, string, string) error
}

type PasswordResetTokenClaim struct {
	ExpiresAt time.Time
	UserID    int64
}

func NewVerificationCodeService(cfg config.MessageConfig, authCfg config.AuthConfig, repo *Repository, limiter *RateLimiter, rdb *redis.Client) *VerificationCodeService {
	s := &VerificationCodeService{
		cfg:     cfg,
		authCfg: authCfg,
		repo:    repo,
		limiter: limiter,
		rdb:     rdb,
		now:     time.Now,
	}
	s.sendMail = s.sendSMTP
	return s
}

func (s *VerificationCodeService) EmailEnabled(ctx context.Context) bool {
	cfg, err := s.emailConfig(ctx, "")
	return err == nil && emailEnabled(cfg)
}

func (s *VerificationCodeService) MobileEnabled(context.Context) bool {
	return false
}

// SendRegisterEmail 发送注册邮箱验证码邮件。lang 为 ResolveEmailLang 解析出的
// 语言（"zh"/"en"/""），决定使用哪套模板。
func (s *VerificationCodeService) SendRegisterEmail(ctx context.Context, email, ip, lang string) error {
	cfg, err := s.emailConfig(ctx, lang)
	if err != nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "邮箱配置暂不可用", err)
	}
	return s.sendEmailCode(ctx, cfg, emailSceneRegister, normalizeEmail(email), ip, cfg.CodeSubject, cfg.CodeTemplate)
}

func (s *VerificationCodeService) SendBindEmail(ctx context.Context, email, ip, lang string) error {
	cfg, err := s.emailConfig(ctx, lang)
	if err != nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "邮箱配置暂不可用", err)
	}
	return s.sendEmailCode(ctx, cfg, emailSceneBind, normalizeEmail(email), ip, cfg.CodeSubject, cfg.CodeTemplate)
}

func (s *VerificationCodeService) SendPasswordResetEmail(ctx context.Context, email, ip, lang string) error {
	cfg, err := s.PreparePasswordResetEmail(ctx, ip, lang)
	if err != nil {
		return err
	}
	return s.SendPreparedPasswordResetEmail(ctx, cfg, email)
}

func (s *VerificationCodeService) PreparePasswordResetEmail(ctx context.Context, ip, lang string) (config.EmailMessageConfig, error) {
	cfg, err := s.emailConfig(ctx, lang)
	if err != nil {
		return config.EmailMessageConfig{}, apperrors.Wrap(consts.StatusServiceUnavailable, "邮箱配置暂不可用", err)
	}
	if !emailEnabled(cfg) {
		return config.EmailMessageConfig{}, apperrors.New(consts.StatusBadRequest, "管理员未配置邮箱服务")
	}
	if err := s.checkSendLimit(ctx, ip); err != nil {
		return config.EmailMessageConfig{}, err
	}
	return cfg, nil
}

func (s *VerificationCodeService) SendPreparedPasswordResetEmail(ctx context.Context, cfg config.EmailMessageConfig, email string) error {
	email = normalizeEmail(email)
	if len(email) > 128 || !emailaddr.Valid(email) {
		return apperrors.New(consts.StatusBadRequest, "邮箱格式不合法")
	}
	// 找回密码的收件人由系统按账号绑定关系决定，不走白名单严格模式：
	// 老账号可能绑定了一个后来才被移出白名单的域名，严格模式下会永久锁死
	// 找回密码入口。黑名单仍然生效。
	if err := s.checkRecipientPolicy(ctx, email, false); err != nil {
		return err
	}
	if err := s.guardOutboundEmail(ctx, emailScenePasswordReset, email); err != nil {
		if errors.Is(err, errEmailCodeReused) {
			return nil
		}
		return err
	}
	return s.generateAndSendEmailCode(ctx, cfg, emailScenePasswordReset, email, cfg.ResetSubject, cfg.ResetTemplate)
}

func (s *VerificationCodeService) VerifyRegisterEmail(ctx context.Context, email, code string) error {
	return s.verifyEmailCode(ctx, emailSceneRegister, normalizeEmail(email), code)
}

func (s *VerificationCodeService) VerifyBindEmail(ctx context.Context, email, code string) error {
	return s.verifyEmailCode(ctx, emailSceneBind, normalizeEmail(email), code)
}

func (s *VerificationCodeService) VerifyPasswordResetEmail(ctx context.Context, user User, code string) (string, error) {
	if user.Email == nil || strings.TrimSpace(*user.Email) == "" {
		return "", apperrors.New(consts.StatusBadRequest, "该账号未绑定邮箱")
	}
	if s.rdb == nil {
		return "", apperrors.New(consts.StatusServiceUnavailable, "找回密码服务暂不可用")
	}
	if err := s.verifyEmailCode(ctx, emailScenePasswordReset, normalizeEmail(*user.Email), code); err != nil {
		return "", err
	}
	token, err := randomHex(24)
	if err != nil {
		return "", apperrors.Wrap(consts.StatusServiceUnavailable, "找回密码服务暂不可用", err)
	}
	if err := s.issuePasswordResetToken(ctx, user.ID, token); err != nil {
		return "", apperrors.Wrap(consts.StatusServiceUnavailable, "找回密码服务暂不可用", err)
	}
	return token, nil
}

func (s *VerificationCodeService) ConsumePasswordResetToken(ctx context.Context, token string) (int64, error) {
	claim, err := s.ClaimPasswordResetToken(ctx, token)
	return claim.UserID, err
}

func (s *VerificationCodeService) PeekPasswordResetToken(ctx context.Context, token string) (int64, error) {
	return s.passwordResetTokenUser(ctx, token)
}

func (s *VerificationCodeService) passwordResetTokenUser(ctx context.Context, token string) (int64, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return 0, apperrors.New(consts.StatusBadRequest, "重置凭证不能为空")
	}
	if s.rdb == nil {
		return 0, apperrors.New(consts.StatusServiceUnavailable, "找回密码服务暂不可用")
	}
	raw, err := peekPasswordResetTokenScript.Run(
		ctx,
		s.rdb,
		[]string{passwordResetTokenKey(token)},
		passwordResetUserKeyPrefix,
	).Text()
	if err == redis.Nil {
		return 0, apperrors.New(consts.StatusBadRequest, "重置凭证已失效，请重新验证")
	}
	if err != nil {
		return 0, apperrors.Wrap(consts.StatusServiceUnavailable, "找回密码服务暂不可用", err)
	}
	userID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || userID <= 0 {
		return 0, apperrors.New(consts.StatusBadRequest, "重置凭证已失效，请重新验证")
	}
	return userID, nil
}

func (s *VerificationCodeService) ClaimPasswordResetToken(ctx context.Context, token string) (PasswordResetTokenClaim, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return PasswordResetTokenClaim{}, apperrors.New(consts.StatusBadRequest, "重置凭证不能为空")
	}
	if s.rdb == nil {
		return PasswordResetTokenClaim{}, apperrors.New(consts.StatusServiceUnavailable, "找回密码服务暂不可用")
	}
	raw, err := consumePasswordResetTokenScript.Run(
		ctx,
		s.rdb,
		[]string{passwordResetTokenKey(token)},
		passwordResetUserKeyPrefix,
	).Text()
	if err == redis.Nil {
		return PasswordResetTokenClaim{}, apperrors.New(consts.StatusBadRequest, "重置凭证已失效，请重新验证")
	}
	if err != nil {
		return PasswordResetTokenClaim{}, apperrors.Wrap(consts.StatusServiceUnavailable, "找回密码服务暂不可用", err)
	}
	parts := strings.SplitN(raw, ":", 2)
	if len(parts) != 2 {
		return PasswordResetTokenClaim{}, apperrors.New(consts.StatusBadRequest, "重置凭证已失效，请重新验证")
	}
	userID, userErr := strconv.ParseInt(parts[0], 10, 64)
	ttlMillis, ttlErr := strconv.ParseInt(parts[1], 10, 64)
	if userErr != nil || ttlErr != nil || userID <= 0 || ttlMillis <= 0 {
		return PasswordResetTokenClaim{}, apperrors.New(consts.StatusBadRequest, "重置凭证已失效，请重新验证")
	}
	return PasswordResetTokenClaim{ExpiresAt: time.Now().Add(time.Duration(ttlMillis) * time.Millisecond), UserID: userID}, nil
}

func (s *VerificationCodeService) RestorePasswordResetToken(ctx context.Context, token string, claim PasswordResetTokenClaim) error {
	token = strings.TrimSpace(token)
	remaining := time.Until(claim.ExpiresAt)
	if s.rdb == nil || token == "" || claim.UserID <= 0 || remaining < time.Millisecond {
		return errors.New("password reset token restore is not configured")
	}
	restored, err := restorePasswordResetTokenScript.Run(
		ctx,
		s.rdb,
		[]string{passwordResetTokenKey(token), passwordResetUserKey(claim.UserID)},
		strconv.FormatInt(claim.UserID, 10),
		remaining.Milliseconds(),
	).Int()
	if err != nil {
		return err
	}
	if restored != 1 {
		return errors.New("password reset token was superseded and cannot be restored")
	}
	return nil
}

func (s *VerificationCodeService) issuePasswordResetToken(ctx context.Context, userID int64, token string) error {
	if s.rdb == nil || userID <= 0 || strings.TrimSpace(token) == "" {
		return errors.New("password reset token service is not configured")
	}
	tokenKey := passwordResetTokenKey(token)
	userKey := passwordResetUserKey(userID)
	return issuePasswordResetTokenScript.Run(
		ctx,
		s.rdb,
		[]string{tokenKey, userKey},
		strconv.FormatInt(userID, 10),
		passwordResetTokenTTL.Milliseconds(),
	).Err()
}

func (s *VerificationCodeService) SendRegisterMobile(context.Context, string, string) error {
	return apperrors.New(consts.StatusBadRequest, "短信验证码服务未配置")
}

func (s *VerificationCodeService) SendBindMobile(context.Context, string, string) error {
	return apperrors.New(consts.StatusBadRequest, "短信验证码服务未配置")
}

func (s *VerificationCodeService) VerifyRegisterMobile(ctx context.Context, mobile, code string) error {
	return s.verifyMobileCode(ctx, mobileSceneRegister, strings.TrimSpace(mobile), code)
}

func (s *VerificationCodeService) VerifyBindMobile(ctx context.Context, mobile, code string) error {
	return s.verifyMobileCode(ctx, mobileSceneBind, strings.TrimSpace(mobile), code)
}

func (s *VerificationCodeService) sendEmailCode(ctx context.Context, cfg config.EmailMessageConfig, scene, email, ip, subject, template string) error {
	if !emailEnabled(cfg) {
		return apperrors.New(consts.StatusBadRequest, "管理员未配置邮箱服务")
	}
	if len(email) > 128 || !emailaddr.Valid(email) {
		return apperrors.New(consts.StatusBadRequest, "邮箱格式不合法")
	}
	// register / bind_email 的收件人是用户当下新填的地址，执行严格白名单；
	// 这样后台「邮箱域白名单」才真正覆盖全部外发链路，而不只是注册提交。
	// 策略放在一切计数之前：策略外的地址不消耗任何配额就返回。
	if err := s.checkRecipientPolicy(ctx, email, true); err != nil {
		return err
	}
	// IP 计数先于发信判断：即使后续因为冷却窗口直接返回成功，打接口仍有成本。
	if err := s.checkSendLimit(ctx, ip); err != nil {
		return err
	}
	if err := s.guardOutboundEmail(ctx, scene, email); err != nil {
		if errors.Is(err, errEmailCodeReused) {
			return nil
		}
		return err
	}
	return s.generateAndSendEmailCode(ctx, cfg, scene, email, subject, template)
}

// guardOutboundEmail 是外发验证码邮件的第二道闸门：
// 冷却窗口复用 → 目标邮箱配额 → 全站熔断。
//
// 顺序有讲究：复用判断在配额之前，避免「用户连点」把目标邮箱的每小时额度打满。
func (s *VerificationCodeService) guardOutboundEmail(ctx context.Context, scene, email string) error {
	reused, err := s.hasFreshEmailCode(ctx, scene, email)
	if err != nil {
		return err
	}
	if reused {
		return errEmailCodeReused
	}
	if err := s.checkTargetSendLimit(ctx, email); err != nil {
		return err
	}
	return s.checkGlobalSendLimit(ctx)
}

// checkRecipientPolicy 把后台维护的邮箱域白/黑名单应用到外发链路。
// strict=true 时白名单生效（非白名单域名直接拒发）；strict=false 时只挡黑名单。
func (s *VerificationCodeService) checkRecipientPolicy(ctx context.Context, email string, strict bool) error {
	whitelist, blacklist, err := s.repo.EmailDomainRules(ctx)
	if err != nil {
		return err
	}
	if decision := emailpolicy.Evaluate(whitelist, blacklist, email, strict); !decision.Allowed {
		return apperrors.New(consts.StatusForbidden, emailDomainRejectedMessage)
	}
	return nil
}

// hasFreshEmailCode 判断冷却窗口内是否已有一封仍然有效的验证码。
func (s *VerificationCodeService) hasFreshEmailCode(ctx context.Context, scene, email string) (bool, error) {
	since := s.now().Add(-emailCodeReuseWindow)
	return s.repo.HasFreshEmailCode(ctx, scene, email, since)
}

func (s *VerificationCodeService) generateAndSendEmailCode(ctx context.Context, cfg config.EmailMessageConfig, scene, email, subject, template string) error {
	code, err := randomDigits(6)
	if err != nil {
		return err
	}
	ttl := emailTTL(cfg)
	expiresAt := s.now().Add(ttl)
	return s.persistAndSendEmailCode(ctx, cfg, scene, email, subject, template, code, ttl, expiresAt)
}

func (s *VerificationCodeService) persistAndSendEmailCode(
	ctx context.Context,
	cfg config.EmailMessageConfig,
	scene, email, subject, template, code string,
	ttl time.Duration,
	expiresAt time.Time,
) error {
	codeHash := s.hashCode(scene, email, code)
	if err := s.repo.CreateEmailCode(ctx, scene, email, codeHash, expiresAt); err != nil {
		return err
	}
	body := renderCodeTemplate(template, code, ttl)
	if sendErr := s.sendMail(ctx, cfg, email, subject, body); sendErr != nil {
		revokeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), emailRevokeTimeout)
		defer cancel()
		if revokeErr := s.repo.RevokeEmailCode(revokeCtx, scene, email, codeHash); revokeErr != nil {
			return apperrors.Wrap(
				consts.StatusServiceUnavailable,
				"邮件验证码发送失败且撤销失败",
				errors.Join(sendErr, revokeErr),
			)
		}
		return apperrors.Wrap(consts.StatusServiceUnavailable, "邮件验证码发送失败", sendErr)
	}
	return nil
}

func (s *VerificationCodeService) verifyEmailCode(ctx context.Context, scene, email, code string) error {
	if email == "" || strings.TrimSpace(code) == "" {
		return apperrors.New(consts.StatusBadRequest, "邮箱和验证码不能为空")
	}
	if err := s.repo.ConsumeEmailCode(ctx, scene, email, s.hashCode(scene, email, code)); err != nil {
		if err == ErrVerificationCodeInvalid {
			return apperrors.New(consts.StatusBadRequest, "验证码错误或已过期")
		}
		return err
	}
	return nil
}

func (s *VerificationCodeService) verifyMobileCode(ctx context.Context, scene, mobile, code string) error {
	if mobile == "" || strings.TrimSpace(code) == "" {
		return apperrors.New(consts.StatusBadRequest, "手机号和验证码不能为空")
	}
	if err := s.repo.ConsumeMobileCode(ctx, scene, mobile, s.hashCode(scene, mobile, code)); err != nil {
		if err == ErrVerificationCodeInvalid {
			return apperrors.New(consts.StatusBadRequest, "验证码错误或已过期")
		}
		return err
	}
	return nil
}

func (s *VerificationCodeService) checkSendLimit(ctx context.Context, ip string) error {
	cfg, err := s.repo.RegisterConfig(ctx)
	if err != nil {
		return err
	}
	// 后台允许把限额配成 0/负数（历史语义是「不限制」）。但发信是花钱且有
	// SMTP 日额度的动作，0 不能等于敞开：配 0 时退回内置兜底值。
	minuteLimit := positiveOr(cfg.VerificationCodeIPMinuteLimit, emailCodeIPMinuteFallback)
	hourLimit := positiveOr(cfg.VerificationCodeIPHourLimit, emailCodeIPHourFallback)
	dayLimit := positiveOr(cfg.VerificationCodeIPDayLimit, emailCodeIPDayFallback)

	ip = s.limiter.rateLimitIPIdentity(ip)
	if count, err := s.limiter.incr(ctx, "auth:code:send:ip:minute:"+ip, time.Minute); err != nil {
		return err
	} else if int(count) > minuteLimit {
		return apperrors.New(consts.StatusTooManyRequests, "验证码发送过于频繁，请稍后再试")
	}
	if count, err := s.limiter.incr(ctx, "auth:code:send:ip:hour:"+ip, time.Hour); err != nil {
		return err
	} else if int(count) > hourLimit {
		return apperrors.New(consts.StatusTooManyRequests, "当前IP验证码发送次数过多，请1小时后再试")
	}
	if count, err := s.limiter.incr(ctx, "auth:code:send:ip:day:"+ip, 24*time.Hour); err != nil {
		return err
	} else if int(count) > dayLimit {
		return apperrors.New(consts.StatusTooManyRequests, "当前IP验证码发送次数过多，请明天再试")
	}
	return nil
}

func (s *VerificationCodeService) checkTargetSendLimit(ctx context.Context, target string) error {
	targetHash := targetRateLimitKey(target)
	minuteCount, err := s.limiter.incr(ctx, "auth:code:send:target:minute:"+targetHash, time.Minute)
	if err != nil {
		return err
	}
	if minuteCount > 1 {
		return apperrors.New(consts.StatusTooManyRequests, "该邮箱验证码发送过于频繁，请稍后再试")
	}
	hourCount, err := s.limiter.incr(ctx, "auth:code:send:target:hour:"+targetHash, time.Hour)
	if err != nil {
		return err
	}
	if hourCount > 10 {
		return apperrors.New(consts.StatusTooManyRequests, "该邮箱验证码发送次数过多，请一小时后再试")
	}
	// 日上限对准「同一收件人被长期骚扰」：单邮箱一天最多 20 封。
	dayCount, err := s.limiter.incr(ctx, "auth:code:send:target:day:"+targetHash, 24*time.Hour)
	if err != nil {
		return err
	}
	if dayCount > emailCodeTargetDayLimit {
		return apperrors.New(consts.StatusTooManyRequests, "该邮箱今日验证码发送次数已达上限，请明天再试")
	}
	return nil
}

// checkGlobalSendLimit 是全站发信熔断：注册/绑定/找回密码共用一个计数器。
// SMTP 账号（例如 QQ 邮箱）有明确的日发信上限，被刷爆之后正常用户也收不到信，
// 所以必须在源头截断，而不是等供应商封禁。
func (s *VerificationCodeService) checkGlobalSendLimit(ctx context.Context) error {
	minuteLimit := positiveOr(s.cfg.Email.SendGlobalMinuteLimit, emailSendGlobalMinuteFallback)
	hourLimit := positiveOr(s.cfg.Email.SendGlobalHourLimit, emailSendGlobalHourFallback)

	minuteCount, err := s.limiter.incr(ctx, "auth:code:send:global:minute", time.Minute)
	if err != nil {
		return err
	}
	if int(minuteCount) > minuteLimit {
		return apperrors.New(consts.StatusServiceUnavailable, "邮件服务繁忙，请稍后再试")
	}
	hourCount, err := s.limiter.incr(ctx, "auth:code:send:global:hour", time.Hour)
	if err != nil {
		return err
	}
	if int(hourCount) > hourLimit {
		return apperrors.New(consts.StatusServiceUnavailable, "邮件服务繁忙，请稍后再试")
	}
	return nil
}

func positiveOr(value, fallback int) int {
	if value > 0 {
		return value
	}
	return fallback
}

func targetRateLimitKey(target string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(target))))
	return hex.EncodeToString(sum[:])
}

func (s *VerificationCodeService) sendSMTP(ctx context.Context, cfg config.EmailMessageConfig, to, subject, body string) error {
	return smtpmail.Send(ctx, smtpmail.Config{
		FromEmail: cfg.SMTPFrom,
		FromName:  cfg.SMTPFromName,
		Host:      cfg.SMTPHost,
		Password:  cfg.SMTPPassword,
		Port:      cfg.SMTPPort,
		TLS:       cfg.SMTPTLS,
		Timeout:   cfg.SMTPTimeout,
		Username:  cfg.SMTPUsername,
	}, to, subject, body)
}

func (s *VerificationCodeService) hashCode(scene, target, code string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		scene,
		strings.ToLower(strings.TrimSpace(target)),
		strings.TrimSpace(code),
		s.authCfg.Secret,
	}, "|")))
	return hex.EncodeToString(sum[:])
}

func emailTTL(cfg config.EmailMessageConfig) time.Duration {
	if cfg.CodeTTL > 0 {
		return cfg.CodeTTL
	}
	return 10 * time.Minute
}

// emailConfig 返回按语言选择后的邮件配置：先合并 config/env 与 DB 后台配置，
// 再做站点品牌替换，最后按 lang 折叠为默认模板（zh/其它）或英文模板（en）。
func (s *VerificationCodeService) emailConfig(ctx context.Context, lang string) (config.EmailMessageConfig, error) {
	cfg := s.cfg.Email
	if strings.TrimSpace(cfg.Provider) == "" {
		cfg.Provider = "smtp"
	}
	dbCfg, ok, err := s.repo.EmailMessageConfig(ctx)
	if err != nil {
		return config.EmailMessageConfig{}, err
	}
	siteName, err := s.repo.SiteName(ctx)
	if err != nil {
		return config.EmailMessageConfig{}, err
	}
	if ok {
		dbCfg.CodeSubject = defaultText(dbCfg.CodeSubject, cfg.CodeSubject)
		dbCfg.CodeTemplate = defaultText(dbCfg.CodeTemplate, cfg.CodeTemplate)
		dbCfg.CodeTTL = cfg.CodeTTL
		dbCfg.ResetSubject = defaultText(dbCfg.ResetSubject, cfg.ResetSubject)
		dbCfg.ResetTemplate = defaultText(dbCfg.ResetTemplate, cfg.ResetTemplate)
		// DB 后台配置不存模板字段，英文模板同样回退到 config/env。
		dbCfg.CodeSubjectEn = defaultText(dbCfg.CodeSubjectEn, cfg.CodeSubjectEn)
		dbCfg.CodeTemplateEn = defaultText(dbCfg.CodeTemplateEn, cfg.CodeTemplateEn)
		dbCfg.ResetSubjectEn = defaultText(dbCfg.ResetSubjectEn, cfg.ResetSubjectEn)
		dbCfg.ResetTemplateEn = defaultText(dbCfg.ResetTemplateEn, cfg.ResetTemplateEn)
		dbCfg.SMTPTimeout = cfg.SMTPTimeout
		if dbCfg.SMTPPort <= 0 {
			dbCfg.SMTPPort = cfg.SMTPPort
		}
		if strings.TrimSpace(dbCfg.SMTPFromName) == "" {
			dbCfg.SMTPFromName = cfg.SMTPFromName
		}
		return applyEmailLang(applyEmailSiteBrand(dbCfg, siteName), lang), nil
	}
	return applyEmailLang(applyEmailSiteBrand(cfg, siteName), lang), nil
}

// applyEmailLang 按语言折叠邮件模板：
//   - zh/其他/未指定 → 使用默认模板（codeSubject/codeTemplate 等）
//   - en → 优先使用英文模板（codeSubjectEn/codeTemplateEn 等），英文模板为空时回退默认模板
//
// 加固：config/env 的「中文默认模板」字段一旦被误配成英文正文（常见于把英文
// 样例直接粘贴到非 En 字段），中文用户会收到英文邮件。因此对中文模板执行英文
// 正文检测：subject/template 均含英文特征、且不含中文字符时，回退内置中文模板。
// 英文模板（*En 字段）不参与此检测，避免误伤合法英文配置。
func applyEmailLang(cfg config.EmailMessageConfig, lang string) config.EmailMessageConfig {
	if emaillang.Resolve(lang, "", "", "") != emaillang.English {
		cfg.CodeSubject = fallbackChineseTemplate(cfg.CodeSubject, ChineseCodeSubjectFallback)
		cfg.CodeTemplate = fallbackChineseTemplate(cfg.CodeTemplate, ChineseCodeTemplateFallback)
		cfg.ResetSubject = fallbackChineseTemplate(cfg.ResetSubject, ChineseResetSubjectFallback)
		cfg.ResetTemplate = fallbackChineseTemplate(cfg.ResetTemplate, ChineseResetTemplateFallback)
		return cfg
	}
	cfg.CodeSubject = defaultText(cfg.CodeSubjectEn, cfg.CodeSubject)
	cfg.CodeTemplate = defaultText(cfg.CodeTemplateEn, cfg.CodeTemplate)
	cfg.ResetSubject = defaultText(cfg.ResetSubjectEn, cfg.ResetSubject)
	cfg.ResetTemplate = defaultText(cfg.ResetTemplateEn, cfg.ResetTemplate)
	return cfg
}

// 内置中文模板兜底：config/env 默认模板被误配成英文正文时使用。
const (
	ChineseCodeSubjectFallback   = "{{siteName}} 验证码"
	ChineseCodeTemplateFallback  = "您的验证码是 {{code}}，{{minutes}} 分钟内有效。如非本人操作，请忽略本邮件。"
	ChineseResetSubjectFallback  = "{{siteName}} 找回密码验证码"
	ChineseResetTemplateFallback = "您正在找回密码，验证码是 {{code}}，{{minutes}} 分钟内有效。如非本人操作，请忽略本邮件。"
)

// fallbackChineseTemplate 检测一段应属于「中文默认模板」的文案是否其实为英文正文。
// 判定规则：非空、不含任何中文字符、且命中英文特征（正文或主题任一分类），
// 命中后返回内置中文兜底模板；否则原样保留用户配置。
func fallbackChineseTemplate(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	if containsChinese(value) {
		return value
	}
	if looksLikeEnglishSubject(value) || looksLikeEnglishBody(value) {
		return fallback
	}
	return value
}

// containsChinese 判断字符串是否包含任何 CJK 统一表意文字。
func containsChinese(value string) bool {
	for _, r := range value {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// looksLikeEnglishBody 通过英文邮件正文高频词判断文案是否像英文正文模板。
func looksLikeEnglishBody(value string) bool {
	lower := strings.ToLower(value)
	englishMarkers := []string{
		"verification code",
		"password reset code",
		"ignore this email",
		"sign in to the support center",
		"did not request",
		"valid for",
	}
	for _, marker := range englishMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// looksLikeEnglishSubject 通过英文邮件主题高频词判断是否像英文主题。
func looksLikeEnglishSubject(value string) bool {
	lower := strings.ToLower(value)
	englishMarkers := []string{
		"verification code",
		"password reset",
		"reset code",
		"verification",
	}
	for _, marker := range englishMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func applyEmailSiteBrand(cfg config.EmailMessageConfig, siteName string) config.EmailMessageConfig {
	siteName = normalizedEmailSiteName(siteName)
	cfg.SMTPFromName = brandedEmailText(cfg.SMTPFromName, defaultEmailSiteName, siteName, siteName)
	cfg.CodeSubject = brandedEmailText(cfg.CodeSubject, defaultEmailSiteName+" 验证码", siteName+" 验证码", siteName)
	cfg.ResetSubject = brandedEmailText(cfg.ResetSubject, defaultEmailSiteName+" 找回密码验证码", siteName+" 找回密码验证码", siteName)
	cfg.CodeSubjectEn = brandEmailField(cfg.CodeSubjectEn, defaultEmailSiteName+" verification code", siteName+" verification code", siteName)
	cfg.ResetSubjectEn = brandEmailField(cfg.ResetSubjectEn, defaultEmailSiteName+" password reset code", siteName+" password reset code", siteName)
	cfg.CodeTemplate = strings.ReplaceAll(cfg.CodeTemplate, "{{siteName}}", siteName)
	cfg.ResetTemplate = strings.ReplaceAll(cfg.ResetTemplate, "{{siteName}}", siteName)
	cfg.CodeTemplateEn = strings.ReplaceAll(cfg.CodeTemplateEn, "{{siteName}}", siteName)
	cfg.ResetTemplateEn = strings.ReplaceAll(cfg.ResetTemplateEn, "{{siteName}}", siteName)
	return cfg
}

// brandEmailField 对非空字段执行品牌替换；空字段保持为空，
// 保证「英文模板为空时回退默认模板」的语言回退逻辑不受品牌化影响。
func brandEmailField(value, legacyDefault, brandedDefault, siteName string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return brandedEmailText(value, legacyDefault, brandedDefault, siteName)
}

func brandedEmailText(value, legacyDefault, brandedDefault, siteName string) string {
	value = strings.TrimSpace(value)
	if value == "" || value == legacyDefault {
		return brandedDefault
	}
	return strings.ReplaceAll(value, "{{siteName}}", siteName)
}

func normalizedEmailSiteName(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return defaultEmailSiteName
}

func emailEnabled(cfg config.EmailMessageConfig) bool {
	return cfg.Enabled && strings.EqualFold(strings.TrimSpace(cfg.Provider), "smtp")
}

func defaultText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func renderCodeTemplate(template, code string, ttl time.Duration) string {
	if strings.TrimSpace(template) == "" {
		template = "您的验证码是 {{code}}，{{minutes}} 分钟内有效。如非本人操作，请忽略本邮件。"
	}
	minutes := int(ttl / time.Minute)
	if minutes <= 0 {
		minutes = 10
	}
	replacer := strings.NewReplacer("{{code}}", code, "{{minutes}}", strconv.Itoa(minutes))
	return replacer.Replace(template)
}

func randomDigits(length int) (string, error) {
	if length <= 0 {
		length = 6
	}
	var builder strings.Builder
	for i := 0; i < length; i++ {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", err
		}
		builder.WriteByte(byte('0' + n.Int64()))
	}
	return builder.String(), nil
}

func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func passwordResetTokenKey(token string) string {
	return secretRedisKey("auth:password_reset:token:", token)
}

const passwordResetUserKeyPrefix = "auth:password_reset:user:"

func passwordResetUserKey(userID int64) string {
	return passwordResetUserKeyPrefix + strconv.FormatInt(userID, 10)
}
