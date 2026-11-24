package systemsettings

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/emailaddr"
	"bugpk-api/internal/pkg/emaillang"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/imagehost"
	"bugpk-api/internal/pkg/secretbox"
	"bugpk-api/internal/pkg/smtpmail"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type Service struct {
	authz        *rbac.Service
	repo         settingsRepository
	systemPrefix string
	// 登录态配置观察者（见 session_config.go）；保存成功与启动装载后回调。
	sessionConfigMu       sync.RWMutex
	sessionConfigObserver SessionConfigObserver
	imageHostUploader     *imagehost.Uploader
}

type settingsRepository interface {
	RegisterOpenConfig(context.Context) (RegisterOpenConfig, error)
	SaveRegisterOpenConfig(context.Context, RegisterOpenConfig) error
	SessionConfig(context.Context) (SessionConfig, error)
	SaveSessionConfig(context.Context, SessionConfig) error
	EmailDomainRules(context.Context) (EmailDomainRules, error)
	SaveEmailDomainRules(context.Context, EmailDomainRules) error
	EmailConfig(context.Context) (EmailConfig, error)
	SaveEmailConfig(context.Context, EmailConfig) error
	MobileConfig(context.Context) (MobileConfig, error)
	SaveMobileConfig(context.Context, MobileConfig) error
	QQLoginConfig(context.Context) (QQLoginConfig, error)
	SaveQQLoginConfig(context.Context, QQLoginConfig) error
	ImageHostConfig(context.Context) (imagehost.Config, error)
	SaveImageHostConfig(context.Context, imagehost.Config) error
	SiteName(context.Context) (string, error)
	CaptchaGlobalEnabled(context.Context) (bool, error)
	CaptchaProviderConfigs(context.Context) ([]CaptchaProviderRow, error)
	SaveCaptchaGlobalEnabled(context.Context, bool) error
	SaveCaptchaProviderConfig(context.Context, CaptchaSceneConfig) error
	SaveCaptchaSceneStates(context.Context, []string) error
	CustomCodeConfig(context.Context) (CustomCodeConfig, error)
	SaveCustomCodeConfig(context.Context, CustomCodeConfig, int64) error
}

const (
	defaultSettingsSiteName = "BugPk-Api"
	maxRoleQPSLimit         = 1_000_000
)

func NewService(repo *Repository, authz *rbac.Service, systemPrefix string) *Service {
	return &Service{repo: repo, authz: authz, systemPrefix: strings.TrimRight(systemPrefix, "/")}
}

func (s *Service) SetImageHostUploader(uploader *imagehost.Uploader) {
	s.imageHostUploader = uploader
}

func (s *Service) Authenticate(ctx context.Context, token string) (rbac.Principal, error) {
	return s.authz.Authenticate(ctx, token)
}

func (s *Service) RegisterOpenConfig(ctx context.Context, p rbac.Principal) (RegisterOpenConfig, error) {
	if err := s.authz.Require(ctx, p, "system:register:open-config"); err != nil {
		return RegisterOpenConfig{}, err
	}
	return s.repo.RegisterOpenConfig(ctx)
}

func (s *Service) SaveRegisterOpenConfig(ctx context.Context, p rbac.Principal, req RegisterOpenConfig) error {
	if err := s.authz.Require(ctx, p, "system:register:open-config:update"); err != nil {
		return err
	}
	cfg := normalizeOpenConfig(req)
	err := s.repo.SaveRegisterOpenConfig(ctx, cfg)
	if errors.Is(err, ErrInvalidDefaultRegisterRole) {
		return apperrors.New(consts.StatusBadRequest, "默认注册角色必须是已启用且允许自助注册的非管理员角色")
	}
	return err
}

func (s *Service) EmailDomainRules(ctx context.Context, p rbac.Principal) (EmailDomainRules, error) {
	if err := s.authz.Require(ctx, p, "system:register:open-config"); err != nil {
		return EmailDomainRules{}, err
	}
	return s.repo.EmailDomainRules(ctx)
}

// SaveEmailDomainRules replaces the registration email domain policy.
// Domains are normalized (trimmed, lower-cased, leading '@' stripped) and
// validated; a non-empty whitelist makes registration exclusive to those
// domains while the blacklist always rejects.
func (s *Service) SaveEmailDomainRules(ctx context.Context, p rbac.Principal, req EmailDomainRules) error {
	if err := s.authz.Require(ctx, p, "system:register:open-config:update"); err != nil {
		return err
	}
	rules, err := normalizeEmailDomainRules(req)
	if err != nil {
		return err
	}
	return s.repo.SaveEmailDomainRules(ctx, rules)
}

func (s *Service) EmailConfig(ctx context.Context, p rbac.Principal) (EmailConfig, error) {
	if err := s.authz.Require(ctx, p, "system:register:email-config"); err != nil {
		return EmailConfig{}, err
	}
	cfg, err := s.repo.EmailConfig(ctx)
	if err != nil {
		return EmailConfig{}, mapConfigSecretErr(err)
	}
	siteName, err := s.repo.SiteName(ctx)
	if err != nil {
		return EmailConfig{}, err
	}
	cfg.SenderName = defaultSettingsSenderName(cfg.SenderName, siteName)
	cfg.AuthCode = maskedSecret(cfg.AuthCode)
	return cfg, nil
}

func (s *Service) SaveEmailConfig(ctx context.Context, p rbac.Principal, req EmailConfig) error {
	if err := s.authz.Require(ctx, p, "system:register:email-config:update"); err != nil {
		return err
	}
	return s.saveEmailConfig(ctx, req)
}

func (s *Service) saveEmailConfig(ctx context.Context, req EmailConfig) error {
	current, err := s.repo.EmailConfig(ctx)
	if err != nil {
		return mapConfigSecretErr(err)
	}
	req.AuthCode = preservedSecret(req.AuthCode, current.AuthCode)
	siteName, err := s.repo.SiteName(ctx)
	if err != nil {
		return err
	}
	cfg, err := normalizeEmailConfig(req, siteName)
	if err != nil {
		return err
	}
	return mapConfigSecretErr(s.repo.SaveEmailConfig(ctx, cfg))
}

func (s *Service) TestEmailConfig(ctx context.Context, p rbac.Principal, req EmailConfig) error {
	if err := s.authz.Require(ctx, p, "system:register:email-config:test"); err != nil {
		return err
	}
	current, err := s.repo.EmailConfig(ctx)
	if err != nil {
		return mapConfigSecretErr(err)
	}
	req.AuthCode = preservedSecret(req.AuthCode, current.AuthCode)
	siteName, err := s.repo.SiteName(ctx)
	if err != nil {
		return err
	}
	cfg, err := normalizeEmailConfig(req, siteName)
	if err != nil {
		return err
	}
	testEmail := strings.ToLower(strings.TrimSpace(req.TestEmail))
	if !validEmail(testEmail) {
		return apperrors.New(consts.StatusBadRequest, "测试邮箱格式不正确")
	}
	subject, body := testEmailMessage(siteName, req.Language)
	if err := sendSMTP(ctx, cfg, testEmail, subject, body); err != nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "SMTP 测试邮件发送失败", err)
	}
	return nil
}

func testEmailMessage(siteName, language string) (string, string) {
	siteName = normalizedSettingsSiteName(siteName)
	if emaillang.Resolve(language, "", "", "") == emaillang.English {
		return siteName + " email configuration test", "This is an SMTP configuration test email from " + siteName + "."
	}
	return siteName + " 邮箱配置测试", "这是一封 " + siteName + " SMTP 配置测试邮件。"
}

func (s *Service) MobileConfig(ctx context.Context, p rbac.Principal) (MobileConfig, error) {
	if err := s.authz.Require(ctx, p, "system:register:mobile-config"); err != nil {
		return MobileConfig{}, err
	}
	cfg, err := s.repo.MobileConfig(ctx)
	if err != nil {
		return MobileConfig{}, mapConfigSecretErr(err)
	}
	cfg.AccessKeySecret = maskedSecret(cfg.AccessKeySecret)
	return cfg, nil
}

func (s *Service) SaveMobileConfig(ctx context.Context, p rbac.Principal, req MobileConfig) error {
	if err := s.authz.Require(ctx, p, "system:register:mobile-config:update"); err != nil {
		return err
	}
	current, err := s.repo.MobileConfig(ctx)
	if err != nil {
		return mapConfigSecretErr(err)
	}
	req.AccessKeySecret = preservedSecret(req.AccessKeySecret, current.AccessKeySecret)
	cfg, err := normalizeMobileConfig(req)
	if err != nil {
		return err
	}
	return mapConfigSecretErr(s.repo.SaveMobileConfig(ctx, cfg))
}

func (s *Service) TestMobileConfig(ctx context.Context, p rbac.Principal, req MobileConfig) error {
	if err := s.authz.Require(ctx, p, "system:register:mobile-config:test"); err != nil {
		return err
	}
	return apperrors.New(consts.StatusBadRequest, "短信验证码暂未接入官方 SDK，不能发送测试验证码")
}

func (s *Service) QQLoginConfig(ctx context.Context, p rbac.Principal) (QQLoginConfig, error) {
	if err := s.authz.Require(ctx, p, "system:login:qq-config"); err != nil {
		return QQLoginConfig{}, err
	}
	cfg, err := s.repo.QQLoginConfig(ctx)
	if err != nil {
		return QQLoginConfig{}, mapConfigSecretErr(err)
	}
	cfg.SuggestedRedirectURI = s.systemPrefix + "/auth/qq/callback"
	cfg.AppKey = maskedSecret(cfg.AppKey)
	return cfg, nil
}

func (s *Service) SaveQQLoginConfig(ctx context.Context, p rbac.Principal, req QQLoginConfig) error {
	if err := s.authz.Require(ctx, p, "system:login:qq-config:update"); err != nil {
		return err
	}
	current, err := s.repo.QQLoginConfig(ctx)
	if err != nil {
		return mapConfigSecretErr(err)
	}
	req.AppKey = preservedSecret(req.AppKey, current.AppKey)
	cfg, err := normalizeQQLoginConfig(req)
	if err != nil {
		return err
	}
	return mapConfigSecretErr(s.repo.SaveQQLoginConfig(ctx, cfg))
}

func normalizeOpenConfig(req RegisterOpenConfig) RegisterOpenConfig {
	mode := strings.TrimSpace(req.OAuthRegisterModeDefault)
	if mode != "off" && mode != "confirm" && mode != "auto" {
		mode = "confirm"
	}
	cfg := RegisterOpenConfig{
		DefaultRoleID:                 req.DefaultRoleID,
		DefaultUserQps:                clampInt(req.DefaultUserQps, 1, maxRoleQPSLimit, 1),
		RegisterEnabled:               boolInt(req.RegisterEnabled),
		RegisterGiftPoints:            clampInt64(req.RegisterGiftPoints, 0, 1_000_000_000, 0),
		RegisterIPHourLimit:           clampInt(req.RegisterIPHourLimit, 0, 1_000_000, 10),
		RegisterIPDayLimit:            clampInt(req.RegisterIPDayLimit, 0, 1_000_000, 0),
		RegisterIPMaxAccounts:         clampInt(req.RegisterIPMaxAccounts, 0, 1_000_000, 0),
		NewAccountDayLimit:            clampInt64(req.NewAccountDayLimit, 0, 100_000_000, 0),
		RegisterDeviceDayLimit:        clampInt(req.RegisterDeviceDayLimit, 0, 1_000_000, 5),
		RegisterIPPrefixDayLimit:      clampInt(req.RegisterIPPrefixDayLimit, 0, 1_000_000, 10),
		RegisterRiskEnabled:           boolInt(req.RegisterRiskEnabled),
		RegisterRiskShadowMode:        boolInt(req.RegisterRiskShadowMode),
		RegisterRiskQuarantineScore:   clampInt(req.RegisterRiskQuarantineScore, 0, 200, 40),
		RegisterRiskRejectScore:       clampInt(req.RegisterRiskRejectScore, 0, 200, 75),
		OAuthRegisterEnabled:          boolInt(req.OAuthRegisterEnabled),
		OAuthRegisterModeDefault:      mode,
		OAuthRegisterIPHourLimit:      clampInt(req.OAuthRegisterIPHourLimit, 0, 1_000_000, 0),
		OAuthRegisterIPDayLimit:       clampInt(req.OAuthRegisterIPDayLimit, 0, 1_000_000, 10),
		OAuthRegisterIPMaxAccounts:    clampInt(req.OAuthRegisterIPMaxAccounts, 0, 1_000_000, 0),
		OAuthRequireVerifiedEmail:     boolInt(req.OAuthRequireVerifiedEmail),
		VerificationCodeIPDayLimit:    clampInt(req.VerificationCodeIPDayLimit, 0, 1_000_000, 50),
		VerificationCodeIPHourLimit:   clampInt(req.VerificationCodeIPHourLimit, 0, 1_000_000, 20),
		VerificationCodeIPMinuteLimit: clampInt(req.VerificationCodeIPMinuteLimit, 0, 1_000_000, 5),
	}
	// 拒绝阈值必须高于隔离阈值，否则「隔离」永远不可达。
	if cfg.RegisterRiskRejectScore <= cfg.RegisterRiskQuarantineScore {
		cfg.RegisterRiskRejectScore = cfg.RegisterRiskQuarantineScore + 1
		if cfg.RegisterRiskRejectScore > 200 {
			cfg.RegisterRiskRejectScore = 200
			cfg.RegisterRiskQuarantineScore = 199
		}
	}
	return cfg
}

const (
	maxEmailDomainRulesPerList = 500
	maxEmailDomainLength       = 255
)

var emailDomainPattern = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,63}$`)

// normalizeEmailDomainRules validates and normalizes both domain lists.
// A `*.example.com` entry allows every sub-domain of example.com; exact
// entries must match the email domain exactly.
func normalizeEmailDomainRules(req EmailDomainRules) (EmailDomainRules, error) {
	normalize := func(kind string, values []string) ([]string, error) {
		seen := make(map[string]struct{}, len(values))
		result := make([]string, 0, len(values))
		for _, value := range values {
			domain := strings.ToLower(strings.TrimSpace(value))
			domain = strings.TrimPrefix(domain, "@")
			if domain == "" {
				continue
			}
			if len(domain) > maxEmailDomainLength {
				return nil, apperrors.New(consts.StatusBadRequest, "邮箱域名过长: "+value)
			}
			if !emailDomainPattern.MatchString(domain) {
				return nil, apperrors.New(consts.StatusBadRequest, "邮箱域名格式不合法: "+value)
			}
			if _, dup := seen[domain]; dup {
				continue
			}
			seen[domain] = struct{}{}
			result = append(result, domain)
		}
		if len(result) > maxEmailDomainRulesPerList {
			return nil, apperrors.New(consts.StatusBadRequest, kind+"最多 "+strconv.Itoa(maxEmailDomainRulesPerList)+" 条")
		}
		return result, nil
	}
	whitelist, err := normalize("白名单", req.Whitelist)
	if err != nil {
		return EmailDomainRules{}, err
	}
	blacklist, err := normalize("黑名单", req.Blacklist)
	if err != nil {
		return EmailDomainRules{}, err
	}
	return EmailDomainRules{Blacklist: blacklist, Whitelist: whitelist}, nil
}

func normalizeEmailConfig(req EmailConfig, siteName ...string) (EmailConfig, error) {
	smtpPort := clampInt(req.SMTPPort, 1, 65535, 587)
	smtpTLS := req.SMTPTLS
	if !req.smtpTLSSet {
		smtpTLS = defaultSMTPTLS(smtpPort)
	}
	cfg := EmailConfig{
		AuthCode:    strings.TrimSpace(req.AuthCode),
		Enabled:     boolInt(req.Enabled),
		SenderEmail: strings.ToLower(strings.TrimSpace(req.SenderEmail)),
		SenderName:  defaultSettingsSenderName(req.SenderName, firstSettingsSiteName(siteName)),
		SMTPPort:    smtpPort,
		SMTPServer:  strings.TrimSpace(req.SMTPServer),
		SMTPTLS:     smtpTLS,
		TestEmail:   strings.TrimSpace(req.TestEmail),
	}
	if cfg.Enabled == 1 {
		if cfg.SMTPServer == "" {
			return EmailConfig{}, apperrors.New(consts.StatusBadRequest, "SMTP 服务器不能为空")
		}
		if !validEmail(cfg.SenderEmail) {
			return EmailConfig{}, apperrors.New(consts.StatusBadRequest, "发件邮箱格式不正确")
		}
		if cfg.AuthCode == "" {
			return EmailConfig{}, apperrors.New(consts.StatusBadRequest, "邮箱授权码不能为空")
		}
	}
	return cfg, nil
}

func defaultSettingsSenderName(value, siteName string) string {
	value = strings.TrimSpace(value)
	siteName = normalizedSettingsSiteName(siteName)
	if value == "" || value == defaultSettingsSiteName || value == "{{siteName}}" {
		return siteName
	}
	return strings.ReplaceAll(value, "{{siteName}}", siteName)
}

func firstSettingsSiteName(values []string) string {
	if len(values) > 0 {
		return values[0]
	}
	return defaultSettingsSiteName
}

func normalizedSettingsSiteName(value string) string {
	if value = strings.TrimSpace(value); value != "" {
		return value
	}
	return defaultSettingsSiteName
}

func normalizeMobileConfig(req MobileConfig) (MobileConfig, error) {
	if boolInt(req.Enabled) == 1 {
		return MobileConfig{}, apperrors.New(consts.StatusBadRequest, "短信尚未接入官方 SDK，当前不能启用")
	}
	endpoint := trimDefault(req.Endpoint, "dypnsapi.aliyuncs.com")
	return MobileConfig{
		AccessKeyID:     strings.TrimSpace(req.AccessKeyID),
		AccessKeySecret: strings.TrimSpace(req.AccessKeySecret),
		Enabled:         0,
		Endpoint:        endpoint,
		Provider:        "aliyun",
		Region:          trimDefault(req.Region, "cn-hangzhou"),
		SignName:        strings.TrimSpace(req.SignName),
		TemplateID:      strings.TrimSpace(req.TemplateID),
		TestMobile:      strings.TrimSpace(req.TestMobile),
	}, nil
}

func normalizeQQLoginConfig(req QQLoginConfig) (QQLoginConfig, error) {
	cfg := QQLoginConfig{
		AppID:           strings.TrimSpace(req.AppID),
		AppKey:          strings.TrimSpace(req.AppKey),
		Enabled:         boolInt(req.Enabled),
		FrontendBaseURL: strings.TrimRight(strings.TrimSpace(req.FrontendBaseURL), "/"),
		RedirectURI:     strings.TrimSpace(req.RedirectURI),
	}
	if cfg.Enabled == 1 {
		return QQLoginConfig{}, apperrors.New(consts.StatusBadRequest, "QQ 登录尚未接入官方 OAuth，当前不能启用")
	}
	if len([]rune(cfg.AppKey)) > 128 {
		return QQLoginConfig{}, apperrors.New(consts.StatusBadRequest, "QQ 登录 AppKey 长度不能超过128个字符")
	}
	return cfg, nil
}

func sendSMTP(ctx context.Context, cfg EmailConfig, to, subject, body string) error {
	return smtpmail.Send(ctx, smtpmail.Config{
		FromEmail: cfg.SenderEmail,
		FromName:  cfg.SenderName,
		Host:      cfg.SMTPServer,
		Password:  cfg.AuthCode,
		Port:      cfg.SMTPPort,
		TLS:       cfg.SMTPTLS,
		Timeout:   10 * time.Second,
		Username:  cfg.SenderEmail,
	}, to, subject, body)
}

func validEmail(value string) bool {
	return emailaddr.Valid(value)
}

func boolInt(value int) int {
	if value == 1 {
		return 1
	}
	return 0
}

func clampInt(value, minValue, maxValue, fallback int) int {
	if value < minValue || value > maxValue {
		return fallback
	}
	return value
}

func clampInt64(value, minValue, maxValue, fallback int64) int64 {
	if value < minValue || value > maxValue {
		return fallback
	}
	return value
}

func trimDefault(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func defaultSMTPTLS(port int) bool {
	return port == 465 || port == 587
}

const secretMask = "******"

func maskedSecret(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return secretMask
}

func preservedSecret(incoming, current string) string {
	incoming = strings.TrimSpace(incoming)
	if incoming == "" || incoming == secretMask {
		return strings.TrimSpace(current)
	}
	return incoming
}

func mapConfigSecretErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, secretbox.ErrSecretMissing) {
		return apperrors.New(consts.StatusServiceUnavailable, "SYSTEM_CONFIG_SECRET 未配置或长度不足32字节")
	}
	if errors.Is(err, secretbox.ErrDecrypt) {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "系统敏感配置无法解密，请检查 SYSTEM_CONFIG_SECRET", err)
	}
	return err
}
