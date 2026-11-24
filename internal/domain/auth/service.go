package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"bugpk-api/internal/config"
	"bugpk-api/internal/pkg/accountstatus"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/iphash"
	"bugpk-api/internal/pkg/masking"
	"bugpk-api/internal/pkg/passwordpolicy"
	"bugpk-api/internal/pkg/totp"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

type Service struct {
	cfg                        config.AuthConfig
	repo                       *Repository
	captcha                    *CaptchaService
	jwt                        *JWTService
	limiter                    *RateLimiter
	rdb                        *redis.Client
	code                       *VerificationCodeService
	perm                       PermissionProvider
	audit                      AuditRecorder
	now                        func() time.Time
	log                        *zap.Logger
	passwordResetResponseFloor time.Duration
	passwordResetSlots         chan struct{}
	backgroundMu               sync.Mutex
	backgroundWG               sync.WaitGroup
	backgroundDone             chan struct{}
	backgroundWaitOnce         sync.Once
	backgroundClosing          bool
	backgroundCtx              context.Context
	backgroundCancel           context.CancelFunc
}

// AuditEntry 是认证域投递给审计子系统的一条记录。
//
// 刻意不复用 observability.OperationLogRecord：auth 是基础设施域，
// 不应该反向依赖业务模块的审计类型。由 router 层做一次字段搬运。
type AuditEntry struct {
	Roles        []string
	UserID       int64
	Username     string
	Module       string
	Action       string
	Summary      string
	Method       string
	Path         string
	Status       bool
	ErrorMessage string
	StatusCode   int
	ClientIP     string
	UserAgent    string
	ElapsedMS    int
}

// AuditRecorder 是审计写入的窄接口。实现方负责排序/落库/容错，
// auth 域只保证「调用一次、不因失败改变业务流程」。
type AuditRecorder interface {
	RecordAuth(ctx context.Context, entry AuditEntry)
}

// SetAuditRecorder 注入审计书写器；未注入时所有审计调用为无操作。
func (s *Service) SetAuditRecorder(recorder AuditRecorder) {
	s.audit = recorder
}

// auditAuth 是内部便捷方法：未注入书写器时静默返回。
func (s *Service) auditAuth(ctx context.Context, entry AuditEntry) {
	if s.audit == nil {
		return
	}
	s.audit.RecordAuth(ctx, entry)
}

type PermissionProvider interface {
	PermissionCodes(ctx context.Context, userID int64, roles []string) ([]string, error)
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_\-]{3,32}$`)

var releaseRefreshRotationScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == "rotated" then
  redis.call("DEL", KEYS[1])
  redis.call("DEL", KEYS[2])
  return 1
end
return 0
`)

// claimRefreshRotationScript 原子地区分三种消费结果：
//
//	1 = 首次消费（写入轮换标记与宽限标记）
//	2 = 宽限窗口内重复消费（多标签页并发续期），只补发 access token
//	0 = 拒绝（已登出/宽限外重复消费）
//
// 轮换标记的值 "rotated" 与登出写入的值（"1"/"logout"）必须保持区分，
// 否则登出后的令牌会重新获得宽限资格。
var claimRefreshRotationScript = redis.NewScript(`
local value = redis.call("GET", KEYS[1])
if value == false then
  redis.call("SET", KEYS[1], "rotated", "PX", ARGV[1])
  if tonumber(ARGV[2]) > 0 then
    redis.call("SET", KEYS[2], "1", "PX", ARGV[2])
  end
  return 1
end
if value == "rotated" and tonumber(ARGV[2]) > 0 and redis.call("EXISTS", KEYS[2]) == 1 then
  return 2
end
return 0
`)

type refreshRotationClaim int

const (
	refreshRotationDenied refreshRotationClaim = iota
	refreshRotationClaimed
	refreshRotationReplayed
)

const (
	maxLoginAccountBytes               = 256
	passwordResetDeliveryConcurrency   = 32
	passwordResetDeliveryMinTimeout    = 5 * time.Second
	passwordResetDeliveryMaxTimeout    = time.Minute
	passwordResetDeliveryTimeoutBuffer = 2 * time.Second
)

func NewService(cfg config.AuthConfig, repo *Repository, captcha *CaptchaService, jwt *JWTService, limiter *RateLimiter, rdb *redis.Client, code *VerificationCodeService) *Service {
	if limiter == nil && rdb != nil {
		limiter = NewRateLimiter(rdb, cfg.Secret)
	}
	service := &Service{
		cfg:                        cfg,
		repo:                       repo,
		captcha:                    captcha,
		jwt:                        jwt,
		limiter:                    limiter,
		rdb:                        rdb,
		code:                       code,
		now:                        time.Now,
		log:                        zap.NewNop(),
		passwordResetResponseFloor: 150 * time.Millisecond,
		passwordResetSlots:         make(chan struct{}, passwordResetDeliveryConcurrency),
		backgroundDone:             make(chan struct{}),
	}
	service.backgroundCtx, service.backgroundCancel = context.WithCancel(context.Background())
	return service
}

func (s *Service) SetPermissionProvider(provider PermissionProvider) {
	s.perm = provider
}

// RefreshTTL 当前生效的 refresh token TTL（后台登录态配置可运行时覆盖），
// 供 handler 写刷新 cookie 的 MaxAge 与签发保持一致。
func (s *Service) RefreshTTL() time.Duration { return s.jwt.RefreshTTL() }

func (s *Service) SetLogger(log *zap.Logger) {
	if log == nil {
		s.log = zap.NewNop()
		return
	}
	s.log = log
}

func (s *Service) Captcha(ctx context.Context, scene, binding string) (CaptchaChallenge, error) {
	return s.captcha.Create(ctx, scene, binding)
}

// CaptchaWithIP 在签发验证码前先做按 IP 的签发限流。公开注册机的第一个
// 动作通常是批量拉 challenge，这一步不设闸时后续所有注册防护都会被前置成本
// 稀释。
func (s *Service) CaptchaWithIP(ctx context.Context, scene, binding, ip string) (CaptchaChallenge, error) {
	if s.limiter != nil {
		if err := s.limiter.CheckCaptchaIssue(ctx, ip); err != nil {
			return CaptchaChallenge{}, err
		}
	}
	return s.captcha.Create(ctx, scene, binding)
}

func (s *Service) VerifyCaptcha(ctx context.Context, captchaID string, req CaptchaVerifyRequest, binding, ip string) error {
	if err := s.limiter.CheckCaptchaVerify(ctx, ip); err != nil {
		return err
	}
	return s.captcha.Verify(ctx, captchaID, req, binding, ip)
}

// Login 保留原有签名供旧调用方（审计测试等）使用；正式入口是
// LoginWithOutcome。2FA 启用的账号经由 Login 会得到空令牌对。
func (s *Service) Login(ctx context.Context, req LoginRequest, binding, ip, userAgent string) (TokenPair, error) {
	outcome, err := s.LoginWithOutcome(ctx, req, binding, ip, userAgent)
	return outcome.Pair, err
}

// LoginWithOutcome 是登录的完整入口：密码通过后若账号启用了 2FA，
// 不直接发令牌，而是返回短寿命 challenge，由 POST /auth/2fa/login 完成
// 第二因子验证后再发令牌。
func (s *Service) LoginWithOutcome(ctx context.Context, req LoginRequest, binding, ip, userAgent string) (LoginOutcome, error) {
	account := strings.TrimSpace(req.Username)
	if account == "" || strings.TrimSpace(req.Password) == "" {
		return LoginOutcome{}, apperrors.New(consts.StatusBadRequest, "用户名和密码不能为空")
	}
	if len(account) > maxLoginAccountBytes {
		return LoginOutcome{}, apperrors.New(consts.StatusBadRequest, "账号长度不合法")
	}
	if !passwordpolicy.WithinMaximum(req.Password) {
		return LoginOutcome{}, apperrors.New(consts.StatusBadRequest, "密码长度不合法")
	}
	if err := s.captcha.Consume(ctx, req.CaptchaID, "login", binding); err != nil {
		return LoginOutcome{}, err
	}
	if err := s.limiter.CheckBeforeLogin(ctx, ip, account); err != nil {
		return LoginOutcome{}, err
	}

	user, err := s.repo.FindUserByAccount(ctx, account)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return LoginOutcome{}, apperrors.Wrap(consts.StatusServiceUnavailable, "认证服务暂时不可用", err)
	}
	if errors.Is(err, sql.ErrNoRows) || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
		limitErr := s.limiter.RecordLoginFailure(ctx, ip, account)
		auditStatusCode := consts.StatusForbidden
		if limitErr != nil {
			auditStatusCode = consts.StatusInternalServerError
			if business, ok := apperrors.AsBusinessError(limitErr); ok {
				auditStatusCode = business.StatusCode
			}
		}
		// Only a known account can own a personal operation log. Keep the
		// public error and rate-limiter behavior identical for unknown accounts.
		if err == nil {
			s.auditAuth(ctx, AuditEntry{
				UserID: user.ID, Username: user.Username, Roles: append([]string(nil), user.Roles...),
				Module: "auth", Action: "login", Summary: "登录失败：账号或密码错误", Status: false,
				ErrorMessage: "用户名/邮箱或密码错误", StatusCode: auditStatusCode,
				ClientIP: ip, UserAgent: userAgent,
			})
		}
		// The credential outcome is already known. Audit it before returning the
		// SAME limiter error; do not bypass its lockout or availability protection.
		if limitErr != nil {
			return LoginOutcome{}, limitErr
		}
		return LoginOutcome{}, apperrors.New(consts.StatusForbidden, "用户名/邮箱或密码错误")
	}
	if user.Status != 1 {
		return LoginOutcome{}, accountstatus.Forbidden(user.BanPermanent, user.BanUntil, user.BanReason)
	}
	// 2FA 已启用的账号：密码只是第一因子，这里不发令牌、不更新
	// last_login，等 /auth/2fa/login 通过第二因子后再走完整发令牌路径。
	if user.TOTPEnabled {
		challenge, err := s.newTwoFAChallenge(user)
		if err != nil {
			return LoginOutcome{}, err
		}
		return LoginOutcome{TwoFA: challenge, TwoFARequired: true}, nil
	}
	if err := s.limiter.RecordLoginSuccess(ctx, account); err != nil {
		return LoginOutcome{}, err
	}
	if err := s.repo.UpdateLastLogin(ctx, user.ID); err != nil {
		return LoginOutcome{}, err
	}
	pair, err := s.issueTokens(ctx, user, ip, userAgent)
	if err != nil {
		return LoginOutcome{}, err
	}
	s.auditAuth(ctx, AuditEntry{
		UserID: user.ID, Username: user.Username, Roles: append([]string(nil), user.Roles...), Module: "auth", Action: "login",
		Summary: "登录成功", Status: true, StatusCode: consts.StatusOK,
		ClientIP: ip, UserAgent: userAgent,
	})
	return LoginOutcome{Pair: pair}, nil
}

// newTwoFAChallenge 签发登录第二步的短寿命凭证。
func (s *Service) newTwoFAChallenge(user User) (*TwoFAChallenge, error) {
	if s.rdb == nil {
		return nil, apperrors.New(consts.StatusServiceUnavailable, "认证服务暂时不可用")
	}
	token, _, err := s.jwt.CreateTwoFAChallengeToken(user, s.now())
	if err != nil {
		return nil, apperrors.Wrap(consts.StatusInternalServerError, "2FA 会话签发失败", err)
	}
	ttl := s.cfg.TwoFAChallengeTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &TwoFAChallenge{
		ChallengeToken: token,
		ExpiresIn:      int(ttl.Seconds()),
	}, nil
}

func (s *Service) Register(ctx context.Context, req RegisterRequest, binding, ip, userAgent string) (TokenPair, error) {
	return s.RegisterWithRisk(ctx, req, binding, ip, userAgent, RegisterRiskContext{
		ClientIP:  ip,
		UserAgent: userAgent,
	})
}

// RegisterWithRisk is the public registration entry point. It keeps the legacy
// Register signature for callers/tests while letting the HTTP handler supply
// server-observable risk signals (signed device cookie, headers, captcha
// timing) that the client cannot forge.
func (s *Service) RegisterWithRisk(ctx context.Context, req RegisterRequest, binding, ip, userAgent string, risk RegisterRiskContext) (TokenPair, error) {
	req.Username = strings.TrimSpace(req.Username)
	req.Email = strings.TrimSpace(req.Email)
	req.Mobile = strings.TrimSpace(req.Mobile)
	if risk.ClientIP == "" {
		risk.ClientIP = ip
	}
	if risk.UserAgent == "" {
		risk.UserAgent = userAgent
	}
	// Honeypot first: nothing is consumed, nothing is created, and the caller
	// receives a success-shaped response with unusable fake tokens.
	if honeypotTripped(req) {
		s.auditAuth(ctx, AuditEntry{
			Username: req.Username, Module: "auth", Action: "register",
			Summary: "注册被蜜罐拦截（返回假成功）", Status: false,
			ErrorMessage: "蜜罐字段非空", StatusCode: consts.StatusOK,
			ClientIP: ip, UserAgent: userAgent,
		})
		return fakeTokenPair(), nil
	}
	cfg, err := s.repo.RegisterConfig(ctx)
	if err != nil {
		return TokenPair{}, err
	}
	if !cfg.RegisterEnabled {
		return TokenPair{}, apperrors.New(consts.StatusBadRequest, "管理员未开放注册")
	}
	// 注册 IP 限流必须先于验证码消费：否则攻击者用随机 captchaId 反复打
	// /auth/register，既不计入注册频率，又能持续触发 Redis/DB 查询。
	if err := s.limiter.CheckRegister(ctx, ip, cfg); err != nil {
		return TokenPair{}, err
	}
	elapsed, err := s.captcha.ConsumeWithElapsed(ctx, req.CaptchaID, "register", binding)
	if err != nil {
		return TokenPair{}, err
	}
	if err := s.checkRegisterIPQuota(ctx, ip, cfg); err != nil {
		s.auditAuth(ctx, AuditEntry{
			Username: req.Username, Module: "auth", Action: "register",
			Summary: "注册被拒：同 IP 账号数达上限", Status: false,
			ErrorMessage: "register_ip_max_accounts", StatusCode: consts.StatusTooManyRequests,
			ClientIP: ip, UserAgent: userAgent,
		})
		return TokenPair{}, err
	}
	if req.Email != "" {
		if err := s.checkRegisterEmailDomain(ctx, req.Email); err != nil {
			s.auditAuth(ctx, AuditEntry{
				Username: req.Username, Module: "auth", Action: "register",
				Summary: "注册被拒：邮箱域不在允许范围", Status: false,
				ErrorMessage: "email domain policy", StatusCode: consts.StatusForbidden,
				ClientIP: ip, UserAgent: userAgent,
			})
			return TokenPair{}, err
		}
	}
	assessment, err := s.assessRegisterRisk(ctx, cfg, req, risk, elapsed)
	if err != nil {
		return TokenPair{}, err
	}
	if assessment.Decision == registerDecisionReject {
		s.auditAuth(ctx, AuditEntry{
			Username: req.Username, Module: "auth", Action: "register",
			Summary: "注册被风控拒绝（返回假成功）：评分 " + strconv.Itoa(assessment.Score) +
				"，信号 " + strings.Join(assessment.Signals, ","),
			Status: false, ErrorMessage: "registration risk score", StatusCode: consts.StatusOK,
			ClientIP: ip, UserAgent: userAgent,
		})
		s.recordRegisterEvent(ctx, assessment, req, risk, elapsed, 0)
		return fakeTokenPair(), nil
	}
	if !usernamePattern.MatchString(req.Username) {
		return TokenPair{}, apperrors.New(consts.StatusBadRequest, "用户名只能包含字母、数字、下划线和短横线，长度为3到32位")
	}
	if !passwordpolicy.Valid(req.Password) {
		return TokenPair{}, apperrors.New(consts.StatusBadRequest, "密码必须为8到64个字符，且不能超过72字节")
	}
	if req.Password != req.ConfirmPassword {
		return TokenPair{}, apperrors.New(consts.StatusBadRequest, "两次输入的密码不一致")
	}
	emailRegisterEnabled := cfg.EmailRegisterEnabled && s.code.EmailEnabled(ctx)
	mobileRegisterEnabled := cfg.MobileRegisterEnabled && s.code.MobileEnabled(ctx)
	if emailRegisterEnabled {
		if req.Email == "" {
			return TokenPair{}, apperrors.New(consts.StatusBadRequest, "邮箱不能为空")
		}
		if err := s.code.VerifyRegisterEmail(ctx, req.Email, req.EmailCode); err != nil {
			return TokenPair{}, err
		}
	}
	if mobileRegisterEnabled {
		if req.Mobile == "" {
			return TokenPair{}, apperrors.New(consts.StatusBadRequest, "手机号不能为空")
		}
		if err := s.code.VerifyRegisterMobile(ctx, req.Mobile, req.MobileCode); err != nil {
			return TokenPair{}, err
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return TokenPair{}, err
	}
	user, err := s.repo.CreateLocalUser(ctx, req, string(hash), cfg.RegisterGiftPoints, iphash.RiskDigest(ip))
	if errors.Is(err, ErrUserExists) {
		s.auditAuth(ctx, AuditEntry{
			Username: req.Username, Module: "auth", Action: "register",
			Summary: "注册失败：账号已存在", Status: false,
			ErrorMessage: "用户名、邮箱或手机号已存在", StatusCode: consts.StatusConflict,
			ClientIP: ip, UserAgent: userAgent,
		})
		return TokenPair{}, apperrors.New(consts.StatusConflict, "用户名、邮箱或手机号已存在")
	}
	if errors.Is(err, ErrRegisterIPLimit) {
		s.auditAuth(ctx, AuditEntry{
			Username: req.Username, Module: "auth", Action: "register",
			Summary: "注册被拒：同 IP 账号数达上限（并发兜底）", Status: false,
			ErrorMessage: "bp_register_ip_used duplicate ip_hash", StatusCode: consts.StatusTooManyRequests,
			ClientIP: ip, UserAgent: userAgent,
		})
		return TokenPair{}, apperrors.New(consts.StatusTooManyRequests, registerIPTooFrequentMessage)
	}
	if errors.Is(err, ErrRegistrationRoleInvalid) {
		return TokenPair{}, apperrors.New(consts.StatusBadRequest, "默认注册角色不可用，请联系管理员")
	}
	if err != nil {
		return TokenPair{}, err
	}
	if assessment.Decision == registerDecisionQuarantine {
		until := s.nowTime().Add(registerRiskWindow24h)
		if err := s.repo.SetUserQuarantine(ctx, user.ID, until, assessment.Score, "registration risk score"); err != nil && s.log != nil {
			s.log.Warn("set new-account quarantine failed",
				zap.Int64("user_id", user.ID),
				zap.Int("risk_score", assessment.Score),
				zap.Error(err),
			)
		}
	}
	s.recordRegisterEvent(ctx, assessment, req, risk, elapsed, user.ID)
	pair, err := s.issueTokens(ctx, user, ip, userAgent)
	if err != nil {
		return TokenPair{}, err
	}
	summary := "新用户注册成功"
	if assessment.Decision == registerDecisionQuarantine {
		summary = "新用户注册成功，进入 24h 风险隔离：评分 " + strconv.Itoa(assessment.Score) +
			"，信号 " + strings.Join(assessment.Signals, ",")
	}
	s.auditAuth(ctx, AuditEntry{
		UserID: user.ID, Username: user.Username, Module: "auth", Action: "register",
		Summary: summary, Status: true, StatusCode: consts.StatusOK,
		ClientIP: ip, UserAgent: userAgent,
	})
	return pair, nil
}

func (s *Service) RegisterConfig(ctx context.Context) (PublicRegisterConfig, error) {
	cfg, err := s.repo.RegisterConfig(ctx)
	if err != nil {
		return PublicRegisterConfig{}, err
	}
	roles, err := s.repo.PublicRegisterRoles(ctx)
	if err != nil {
		return PublicRegisterConfig{}, err
	}
	return PublicRegisterConfig{
		RegisterEnabled:      cfg.RegisterEnabled && len(roles) > 0,
		EmailRegisterEnabled: cfg.EmailRegisterEnabled && s.code.EmailEnabled(ctx),
		EmailServiceEnabled:  s.code.EmailEnabled(ctx),
		// Mobile registration stays fail-closed until an official SMS SDK is integrated.
		MobileRegisterEnabled: false,
		CaptchaProviders:      implementedCaptchaProviders(),
	}, nil
}

func implementedCaptchaProviders() []string {
	return []string{ProviderPOW, ProviderGeeTest4, ProviderCloudflare}
}

func (s *Service) SendRegisterEmailCode(ctx context.Context, req SendRegisterEmailCodeRequest, binding, ip, lang string) error {
	if err := s.captcha.Consume(ctx, req.CaptchaID, "register_email", binding); err != nil {
		return err
	}
	cfg, err := s.repo.RegisterConfig(ctx)
	if err != nil {
		return err
	}
	if !cfg.EmailRegisterEnabled || !s.code.EmailEnabled(ctx) {
		return apperrors.New(consts.StatusBadRequest, "管理员未开启邮箱注册")
	}
	// Enforce the domain policy at code-send time as well, so out-of-policy
	// domains never receive a verification email in the first place.
	if req.Email != "" {
		if err := s.checkRegisterEmailDomain(ctx, req.Email); err != nil {
			return err
		}
	}
	return s.code.SendRegisterEmail(ctx, req.Email, ip, lang)
}

func (s *Service) SendRegisterMobileCode(ctx context.Context, req SendRegisterMobileCodeRequest, binding, ip string) error {
	if err := s.captcha.Consume(ctx, req.CaptchaID, "register_mobile", binding); err != nil {
		return err
	}
	cfg, err := s.repo.RegisterConfig(ctx)
	if err != nil {
		return err
	}
	if !cfg.MobileRegisterEnabled || !s.code.MobileEnabled(ctx) {
		return apperrors.New(consts.StatusBadRequest, "管理员未开启手机号注册")
	}
	return s.code.SendRegisterMobile(ctx, req.Mobile, ip)
}

func (s *Service) CheckPasswordResetAccount(ctx context.Context, req PasswordResetAccountRequest, ip string) (PasswordResetAccountResult, error) {
	if err := s.limiter.CheckPasswordResetAccount(ctx, ip); err != nil {
		return PasswordResetAccountResult{}, err
	}
	account := strings.TrimSpace(req.Account)
	if account == "" {
		return PasswordResetAccountResult{}, apperrors.New(consts.StatusBadRequest, "账号不能为空")
	}
	user, err := s.repo.FindPasswordResetUserByAccount(ctx, account)
	if errors.Is(err, sql.ErrNoRows) {
		return PasswordResetAccountResult{MaskedEmail: "***@***"}, nil
	}
	if err != nil {
		return PasswordResetAccountResult{}, apperrors.Wrap(consts.StatusServiceUnavailable, "找回密码服务暂时不可用", err)
	}
	if user.Status != 1 || user.Email == nil || strings.TrimSpace(*user.Email) == "" {
		return PasswordResetAccountResult{MaskedEmail: "***@***"}, nil
	}
	return PasswordResetAccountResult{MaskedEmail: passwordResetMaskedEmail(user)}, nil
}

func passwordResetMaskedEmail(user User) string {
	if user.Status != 1 || user.Email == nil {
		return "***@***"
	}
	return masking.PasswordResetEmail(*user.Email)
}

func (s *Service) SendPasswordResetEmailCode(ctx context.Context, req PasswordResetEmailCodeRequest, binding, ip, lang string) error {
	if err := s.captcha.Consume(ctx, req.CaptchaID, "password_reset", binding); err != nil {
		return err
	}
	startedAt := time.Now()
	defer s.waitForPasswordResetResponse(ctx, startedAt)
	emailConfig, err := s.code.PreparePasswordResetEmail(ctx, ip, lang)
	if err != nil {
		return err
	}
	if !s.acquirePasswordResetSlot() {
		return apperrors.New(consts.StatusServiceUnavailable, "找回密码邮件服务繁忙，请稍后再试")
	}
	backgroundStarted := false
	defer func() {
		if !backgroundStarted {
			s.releasePasswordResetSlot()
		}
	}()
	user, err := s.repo.FindPasswordResetUserByAccount(ctx, strings.TrimSpace(req.Account))
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "找回密码服务暂时不可用", err)
	}
	email := ""
	knownAccount := err == nil && user.Status == 1 && user.Email != nil
	if knownAccount {
		email = strings.TrimSpace(*user.Email)
		// Per-account 每日发送上限：IP 维度限制挡不住换 IP 的攻击者，
		// 管理员邮箱被反复触发重置邮件必须在这一层截断。未登录状态下
		// 该限制对任意账号生效，响应不区分「触发限流」与「正常发送」，
		// 避免泄露账号存在性。
		if _, limitErr := s.limiter.CheckPasswordResetSend(ctx, ip, strings.TrimSpace(req.Account), s.cfg.PasswordResetAccountDayLimit); limitErr != nil {
			if IsAdminRole(user.Roles) {
				s.auditAuth(ctx, AuditEntry{
					UserID: user.ID, Username: user.Username, Roles: append([]string(nil), user.Roles...),
					Module: "auth", Action: "password_reset_request", Summary: "管理员账号找回密码触发每日上限（已拦截）", Status: false,
					ErrorMessage: "password_reset_account_day_limit", StatusCode: consts.StatusTooManyRequests,
					ClientIP: ip, UserAgent: "",
				})
			}
			return limitErr
		}
		if IsAdminRole(user.Roles) {
			// 管理员账号的每一次找回尝试都进审计：这既是告警信号，
			// 也是事后取证时的触发时间线。
			s.auditAuth(ctx, AuditEntry{
				UserID: user.ID, Username: user.Username, Roles: append([]string(nil), user.Roles...),
				Module: "auth", Action: "password_reset_request", Summary: "管理员账号触发找回密码邮件", Status: true,
				StatusCode: consts.StatusOK, ClientIP: ip, UserAgent: "",
			})
			if s.log != nil {
				s.log.Warn("password reset requested for admin account",
					zap.Int64("user_id", user.ID),
					zap.String("client_ip", masking.IP(ip)),
				)
			}
		}
	}
	if !s.startPasswordResetBackground(func() {
		defer s.releasePasswordResetSlot()
		if email == "" {
			return
		}
		deliveryCtx, cancel := context.WithTimeout(s.passwordResetBackgroundContext(), passwordResetDeliveryTimeout(emailConfig.SMTPTimeout))
		defer cancel()
		if err := s.code.SendPreparedPasswordResetEmail(deliveryCtx, emailConfig, email); err != nil && s.log != nil {
			s.log.Warn("password reset email delivery failed",
				zap.String("email", masking.Email(email)),
				zap.String("client_ip", masking.IP(ip)),
				zap.String("error", masking.Error(err)),
			)
		}
	}) {
		return apperrors.New(consts.StatusServiceUnavailable, "找回密码邮件服务正在关闭，请稍后再试")
	}
	backgroundStarted = true
	return nil
}

func passwordResetDeliveryTimeout(smtpTimeout time.Duration) time.Duration {
	timeout := smtpTimeout + passwordResetDeliveryTimeoutBuffer
	if timeout < passwordResetDeliveryMinTimeout {
		return passwordResetDeliveryMinTimeout
	}
	if timeout > passwordResetDeliveryMaxTimeout {
		return passwordResetDeliveryMaxTimeout
	}
	return timeout
}

func (s *Service) acquirePasswordResetSlot() bool {
	if s.passwordResetSlots == nil {
		return false
	}
	select {
	case s.passwordResetSlots <- struct{}{}:
		return true
	default:
		return false
	}
}

func (s *Service) releasePasswordResetSlot() {
	if s.passwordResetSlots == nil {
		return
	}
	select {
	case <-s.passwordResetSlots:
	default:
	}
}

func (s *Service) startPasswordResetBackground(fn func()) bool {
	if fn == nil {
		return false
	}
	s.backgroundMu.Lock()
	if s.backgroundClosing {
		s.backgroundMu.Unlock()
		return false
	}
	s.backgroundWG.Add(1)
	s.backgroundMu.Unlock()
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil && s.log != nil {
				s.log.Error("password reset background task panicked", zap.String("error", masking.ErrorText(fmt.Sprint(recovered))))
			}
			s.backgroundWG.Done()
		}()
		fn()
	}()
	return true
}

func (s *Service) passwordResetBackgroundContext() context.Context {
	s.backgroundMu.Lock()
	defer s.backgroundMu.Unlock()
	if s.backgroundCtx == nil {
		return context.Background()
	}
	return s.backgroundCtx
}

func (s *Service) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	s.backgroundMu.Lock()
	s.backgroundClosing = true
	cancelBackground := s.backgroundCancel
	if s.backgroundDone == nil {
		s.backgroundDone = make(chan struct{})
	}
	done := s.backgroundDone
	s.backgroundMu.Unlock()
	if cancelBackground != nil {
		cancelBackground()
	}
	s.backgroundWaitOnce.Do(func() {
		go func() {
			s.backgroundWG.Wait()
			close(done)
		}()
	})
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) waitForPasswordResetResponse(ctx context.Context, startedAt time.Time) {
	floor := s.passwordResetResponseFloor
	if floor <= 0 {
		return
	}
	remaining := floor - time.Since(startedAt)
	if remaining <= 0 {
		return
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (s *Service) VerifyPasswordResetCode(ctx context.Context, req PasswordResetVerifyRequest) (PasswordResetVerifyResult, error) {
	user, err := s.repo.FindPasswordResetUserByAccount(ctx, strings.TrimSpace(req.Account))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PasswordResetVerifyResult{}, apperrors.New(consts.StatusBadRequest, "验证码错误或已过期")
		}
		return PasswordResetVerifyResult{}, apperrors.Wrap(consts.StatusServiceUnavailable, "找回密码服务暂时不可用", err)
	}
	if user.Status != 1 || user.Email == nil || strings.TrimSpace(*user.Email) == "" {
		return PasswordResetVerifyResult{}, apperrors.New(consts.StatusBadRequest, "验证码错误或已过期")
	}
	token, err := s.code.VerifyPasswordResetEmail(ctx, user, req.Code)
	if err != nil {
		return PasswordResetVerifyResult{}, err
	}
	// 启用了 2FA 的账号（尤其是管理员）：邮箱验证码通过后还必须提交
	// 当前 TOTP。邮箱被控时攻击者只有邮箱验证码，拿不到认证器动态码，
	// 管理员密码依然重置不了。
	if user.TOTPEnabled {
		ok, verr := totp.Verify(user.TOTPSecret, strings.TrimSpace(req.TOTPCode), s.now())
		if verr != nil || !ok {
			return PasswordResetVerifyResult{}, apperrors.New(consts.StatusBadRequest, "验证码错误或已过期")
		}
	}
	return PasswordResetVerifyResult{ResetToken: token}, nil
}

func (s *Service) ResetPassword(ctx context.Context, req PasswordResetRequest) error {
	if !passwordpolicy.Valid(req.Password) {
		return apperrors.New(consts.StatusBadRequest, "密码必须为8到64个字符，且不能超过72字节")
	}
	if req.Password != req.ConfirmPassword {
		return apperrors.New(consts.StatusBadRequest, "两次输入的密码不一致")
	}
	userID, err := s.code.PeekPasswordResetToken(ctx, req.ResetToken)
	if err != nil {
		return err
	}
	user, err := s.repo.FindUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apperrors.New(consts.StatusBadRequest, "账号状态异常，无法重置密码")
		}
		return apperrors.Wrap(consts.StatusServiceUnavailable, "找回密码服务暂时不可用", err)
	}
	if user.Status != 1 {
		return accountstatus.Forbidden(user.BanPermanent, user.BanUntil, user.BanReason)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	claim, err := s.code.ClaimPasswordResetToken(ctx, req.ResetToken)
	if err != nil {
		return err
	}
	if claim.UserID != userID {
		return apperrors.New(consts.StatusBadRequest, "重置凭证已失效，请重新验证")
	}
	if err := s.repo.UpdatePasswordHashAndBumpVersion(ctx, userID, string(hash)); err != nil {
		if errors.Is(err, ErrUserUnavailable) {
			return apperrors.New(consts.StatusBadRequest, "账号状态异常，无法重置密码")
		}
		restoreCtx, cancel := context.WithTimeout(context.Background(), passwordResetRestoreTTL)
		restoreErr := s.code.RestorePasswordResetToken(restoreCtx, req.ResetToken, claim)
		cancel()
		if restoreErr != nil {
			return errors.Join(err, restoreErr)
		}
		return err
	}
	// 重置完成即吊销全部会话（token_version 已 +1 使旧 access/refresh
	// 失效）；管理员账号额外落审计，攻击者成功重置后这里一定有记录。
	if IsAdminRole(user.Roles) {
		s.auditAuth(ctx, AuditEntry{
			UserID: userID, Username: user.Username, Roles: append([]string(nil), user.Roles...),
			Module: "auth", Action: "password_reset_complete", Summary: "管理员账号密码通过找回流程重置，全部会话已吊销", Status: true,
			StatusCode: consts.StatusOK,
		})
		if s.log != nil {
			s.log.Warn("admin password reset completed via public flow", zap.Int64("user_id", userID))
		}
	}
	return nil
}

func (s *Service) Refresh(ctx context.Context, refreshToken, ip, userAgent string) (TokenPair, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return TokenPair{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
	}
	if s.limiter == nil {
		return TokenPair{}, apperrors.New(consts.StatusServiceUnavailable, "认证服务暂时不可用")
	}
	if err := s.limiter.CheckRefresh(ctx, ip); err != nil {
		return TokenPair{}, err
	}
	info, err := s.parseRefreshTokenForDevice(refreshToken, ip, userAgent)
	if err != nil {
		return TokenPair{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
	}
	claim, err := s.claimRefreshRotation(ctx, info.JTI, info.ExpiresAt)
	if err != nil {
		return TokenPair{}, err
	}
	if claim == refreshRotationDenied {
		return TokenPair{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
	}
	user, err := s.repo.FindUserByID(ctx, info.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return TokenPair{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
		}
		if claim == refreshRotationReplayed {
			return TokenPair{}, err
		}
		return TokenPair{}, s.restoreRefreshRotationAfterFailure(info.JTI, err)
	}
	if user.Status != 1 {
		return TokenPair{}, accountstatus.Forbidden(user.BanPermanent, user.BanUntil, user.BanReason)
	}
	if user.TokenVersion != info.TokenVersion {
		return TokenPair{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
	}
	// 宽限窗口内的重复消费（并发标签页）不再轮换 refresh token：刷新 cookie
	// 已由首次消费的响应更新，这里只补发一个 access token，避免会话分叉。
	if claim == refreshRotationReplayed {
		access, err := s.jwt.CreateAccessToken(user, s.now())
		if err != nil {
			return TokenPair{}, err
		}
		if s.log != nil {
			s.log.Debug("refresh token replayed within rotation grace window", zap.Int64("user_id", user.ID))
		}
		return TokenPair{AccessToken: access}, nil
	}
	issued, err := s.createTokens(user, ip, userAgent)
	if err != nil {
		return TokenPair{}, s.restoreRefreshRotationAfterFailure(info.JTI, err)
	}
	if err := s.repo.RotateRefreshAudit(
		ctx,
		info,
		user,
		issued.Pair.RefreshJTI,
		issued.Device,
		issued.IssuedAt,
		issued.Pair.RefreshExp,
	); err != nil {
		if errors.Is(err, ErrRefreshAuditInactive) {
			return TokenPair{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
		}
		return TokenPair{}, s.restoreRefreshRotationAfterFailure(info.JTI, err)
	}
	return issued.Pair, nil
}

func (s *Service) claimRefreshRotation(ctx context.Context, jti string, expiresAt time.Time) (refreshRotationClaim, error) {
	if s.rdb == nil {
		return refreshRotationDenied, apperrors.New(consts.StatusServiceUnavailable, "认证服务暂不可用")
	}
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		ttl = time.Second
	}
	ttlMilliseconds := ttl.Milliseconds()
	if ttlMilliseconds <= 0 {
		ttlMilliseconds = 1000
	}
	grace := s.cfg.RefreshRotationGrace
	if grace < 0 {
		grace = 0
	}
	result, err := claimRefreshRotationScript.Run(
		ctx,
		s.rdb,
		[]string{refreshRevokedKey(jti), refreshRotationGraceKey(jti)},
		ttlMilliseconds,
		grace.Milliseconds(),
	).Int()
	if err != nil {
		return refreshRotationDenied, apperrors.Wrap(consts.StatusServiceUnavailable, "认证服务暂不可用", err)
	}
	claim := refreshRotationClaim(result)
	switch claim {
	case refreshRotationClaimed, refreshRotationReplayed:
		return claim, nil
	default:
		return refreshRotationDenied, nil
	}
}

func (s *Service) restoreRefreshRotationAfterFailure(jti string, cause error) error {
	if s.rdb == nil {
		return cause
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := releaseRefreshRotationScript.Run(
		ctx,
		s.rdb,
		[]string{refreshRevokedKey(jti), refreshRotationGraceKey(jti)},
	).Err(); err != nil {
		return errors.Join(cause, apperrors.Wrap(consts.StatusServiceUnavailable, "认证会话恢复失败", err))
	}
	return cause
}

func (s *Service) Logout(ctx context.Context, refreshToken, ip, userAgent string) error {
	info, err := s.parseRefreshTokenForDevice(refreshToken, ip, userAgent)
	if err != nil {
		// 令牌无法解析时登出是幂等无操作，无需审计（也不该在日志里留噪声）。
		return nil
	}
	// Snapshot live roles/name only when optional auditing is configured.
	// Missing audit dependencies or lookup failure must not stop logout,
	// but stale/deleted/disabled identities must not create attributed operations.
	var user User
	canAudit := false
	if s.audit != nil && s.repo != nil {
		var lookupErr error
		user, lookupErr = s.repo.FindUserByID(ctx, info.UserID)
		canAudit = lookupErr == nil && user.Status == 1 && user.TokenVersion == info.TokenVersion
	}
	revokeErr := s.revokeRefresh(ctx, info.JTI, info.ExpiresAt, "logout")
	if canAudit {
		statusCode := consts.StatusOK
		if revokeErr != nil {
			statusCode = consts.StatusInternalServerError
			if business, ok := apperrors.AsBusinessError(revokeErr); ok {
				statusCode = business.StatusCode
			}
		}
		s.auditAuth(ctx, AuditEntry{
			UserID: user.ID, Username: user.Username, Roles: append([]string(nil), user.Roles...), Module: "auth", Action: "logout",
			Summary: "退出登录", Status: revokeErr == nil, StatusCode: statusCode, ClientIP: ip, UserAgent: userAgent,
		})
	}
	return revokeErr
}

func (s *Service) parseRefreshTokenForDevice(refreshToken, ip, userAgent string) (RefreshInfo, error) {
	// 依次尝试 v3（UA 归一化）、v2（完整 UA）、v1（IP+UA），让旧会话在
	// 下一次刷新前继续可用；刷新成功后会自动升级到 v3。
	for _, device := range []string{
		DeviceFingerprint(ip, userAgent),
		deviceFingerprintV2(ip, userAgent),
		legacyDeviceFingerprint(ip, userAgent),
	} {
		if info, err := s.jwt.ParseRefreshToken(refreshToken, device); err == nil {
			return info, nil
		}
	}
	return RefreshInfo{}, fmt.Errorf("invalid refresh token device")
}

func (s *Service) checkAccountActive(user User) error {
	if user.Status != 1 {
		return accountstatus.Forbidden(user.BanPermanent, user.BanUntil, user.BanReason)
	}
	return nil
}

func (s *Service) UserFromAccessToken(ctx context.Context, accessToken string) (User, error) {
	info, err := s.jwt.ParseAccessToken(strings.TrimSpace(accessToken))
	if err != nil {
		return User{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
	}
	user, err := s.repo.FindUserByID(ctx, info.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return User{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
		}
		return User{}, apperrors.Wrap(consts.StatusServiceUnavailable, "认证服务暂时不可用", err)
	}
	if user.Status != 1 {
		return User{}, accountstatus.Forbidden(user.BanPermanent, user.BanUntil, user.BanReason)
	}
	if user.TokenVersion != info.TokenVersion {
		return User{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
	}
	return user, nil
}

func (s *Service) Codes(ctx context.Context, accessToken string) ([]string, error) {
	user, err := s.UserFromAccessToken(ctx, accessToken)
	if err != nil {
		return nil, err
	}
	if s.perm == nil {
		return []string{}, nil
	}
	return s.perm.PermissionCodes(ctx, user.ID, user.Roles)
}

func (s *Service) issueTokens(ctx context.Context, user User, ip, userAgent string) (TokenPair, error) {
	issued, err := s.createTokens(user, ip, userAgent)
	if err != nil {
		return TokenPair{}, err
	}
	if err := s.repo.CreateRefreshAudit(
		ctx,
		user,
		issued.Pair.RefreshJTI,
		issued.Device,
		issued.IssuedAt,
		issued.Pair.RefreshExp,
	); err != nil {
		return TokenPair{}, err
	}
	return issued.Pair, nil
}

type issuedTokens struct {
	Pair     TokenPair
	Device   string
	IssuedAt time.Time
}

func (s *Service) createTokens(user User, ip, userAgent string) (issuedTokens, error) {
	now := s.now()
	access, err := s.jwt.CreateAccessToken(user, now)
	if err != nil {
		return issuedTokens{}, err
	}
	device := DeviceFingerprint(ip, userAgent)
	refresh, jti, expiresAt, err := s.jwt.CreateRefreshToken(user, device, now)
	if err != nil {
		return issuedTokens{}, err
	}
	return issuedTokens{
		Device:   device,
		IssuedAt: now,
		Pair: TokenPair{
			AccessToken:  access,
			RefreshToken: refresh,
			RefreshJTI:   jti,
			RefreshExp:   expiresAt,
		},
	}, nil
}

func (s *Service) revokeRefresh(ctx context.Context, jti string, expiresAt time.Time, reason string) error {
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		ttl = time.Second
	}
	if err := s.rdb.Set(ctx, refreshRevokedKey(jti), "1", ttl).Err(); err != nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "认证服务暂不可用", err)
	}
	return s.repo.RevokeRefreshAudit(ctx, jti, reason)
}
