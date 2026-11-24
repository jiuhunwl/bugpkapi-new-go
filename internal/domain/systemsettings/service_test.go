package systemsettings

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/imagehost"
)

type settingsRepositoryStub struct {
	email           EmailConfig
	emailLoadErr    error
	emailSaved      EmailConfig
	emailSaveCalled bool
	mobile          MobileConfig
	mobileLoadErr   error
	mobileSaved     MobileConfig
	qq              QQLoginConfig
	qqLoadErr       error
	qqSaved         QQLoginConfig
	siteName        string
	siteNameErr     error
	captchaRows     []CaptchaProviderRow
	captchaLoadErr  error
	savedCaptcha    []string
	captchaSaveErr  error
	globalCaptcha   bool

	domainRulesSaved      EmailDomainRules
	domainRulesSaveCalled bool
	sessionCfg            SessionConfig
	sessionLoadErr        error
	sessionSaveErr        error
	sessionSaveCalled     bool

	imageHost      imagehost.Config
	imageHostErr   error
	imageHostSaved imagehost.Config
}

func (s *settingsRepositoryStub) RegisterOpenConfig(context.Context) (RegisterOpenConfig, error) {
	return RegisterOpenConfig{}, nil
}

func (s *settingsRepositoryStub) SaveRegisterOpenConfig(context.Context, RegisterOpenConfig) error {
	return nil
}

func (s *settingsRepositoryStub) EmailDomainRules(context.Context) (EmailDomainRules, error) {
	return EmailDomainRules{}, nil
}

func (s *settingsRepositoryStub) SaveEmailDomainRules(_ context.Context, rules EmailDomainRules) error {
	s.domainRulesSaved = rules
	s.domainRulesSaveCalled = true
	return nil
}

func (s *settingsRepositoryStub) EmailConfig(context.Context) (EmailConfig, error) {
	return s.email, s.emailLoadErr
}

func (s *settingsRepositoryStub) SaveEmailConfig(_ context.Context, cfg EmailConfig) error {
	s.emailSaved = cfg
	s.emailSaveCalled = true
	return nil
}

func (s *settingsRepositoryStub) MobileConfig(context.Context) (MobileConfig, error) {
	return s.mobile, s.mobileLoadErr
}

func (s *settingsRepositoryStub) SaveMobileConfig(_ context.Context, cfg MobileConfig) error {
	s.mobileSaved = cfg
	return nil
}

func (s *settingsRepositoryStub) QQLoginConfig(context.Context) (QQLoginConfig, error) {
	return s.qq, s.qqLoadErr
}

func (s *settingsRepositoryStub) SaveQQLoginConfig(_ context.Context, cfg QQLoginConfig) error {
	s.qqSaved = cfg
	return nil
}

func (s *settingsRepositoryStub) ImageHostConfig(context.Context) (imagehost.Config, error) {
	return s.imageHost, s.imageHostErr
}

func (s *settingsRepositoryStub) SaveImageHostConfig(_ context.Context, cfg imagehost.Config) error {
	s.imageHostSaved = cfg
	return nil
}

func (s *settingsRepositoryStub) SiteName(context.Context) (string, error) {
	return s.siteName, s.siteNameErr
}

func (s *settingsRepositoryStub) CaptchaProviderConfigs(context.Context) ([]CaptchaProviderRow, error) {
	if s.captchaLoadErr != nil {
		return nil, s.captchaLoadErr
	}
	return append([]CaptchaProviderRow(nil), s.captchaRows...), nil
}

func (s *settingsRepositoryStub) CaptchaGlobalEnabled(context.Context) (bool, error) {
	return s.globalCaptcha, nil
}

func (s *settingsRepositoryStub) SaveCaptchaGlobalEnabled(_ context.Context, enabled bool) error {
	s.globalCaptcha = enabled
	return nil
}

func (s *settingsRepositoryStub) SaveCaptchaProviderConfig(context.Context, CaptchaSceneConfig) error {
	return nil
}

func (s *settingsRepositoryStub) SaveCaptchaSceneStates(_ context.Context, scenes []string) error {
	s.savedCaptcha = append([]string(nil), scenes...)
	return s.captchaSaveErr
}

func (s *settingsRepositoryStub) CustomCodeConfig(context.Context) (CustomCodeConfig, error) {
	return CustomCodeConfig{}, nil
}

func (s *settingsRepositoryStub) SaveCustomCodeConfig(context.Context, CustomCodeConfig, int64) error {
	return nil
}

func TestSecretHelpersMaskAndPreserveValues(t *testing.T) {
	if got := maskedSecret("smtp-secret"); got != secretMask {
		t.Fatalf("maskedSecret() = %q", got)
	}
	if got := maskedSecret(" "); got != "" {
		t.Fatalf("maskedSecret(blank) = %q", got)
	}
	for _, incoming := range []string{"", " ", secretMask} {
		if got := preservedSecret(incoming, "stored-secret"); got != "stored-secret" {
			t.Fatalf("preservedSecret(%q) = %q", incoming, got)
		}
	}
	if got := preservedSecret("replacement", "stored-secret"); got != "replacement" {
		t.Fatalf("preservedSecret(replacement) = %q", got)
	}
}

func TestTestEmailMessageFollowsResolvedLanguage(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		language    string
		wantSubject string
		wantBody    string
	}{
		{language: "zh-CN", wantSubject: "Acme API 邮箱配置测试", wantBody: "这是一封 Acme API SMTP 配置测试邮件。"},
		{language: "zh-TW", wantSubject: "Acme API 邮箱配置测试", wantBody: "这是一封 Acme API SMTP 配置测试邮件。"},
		{language: "zh-HK", wantSubject: "Acme API 邮箱配置测试", wantBody: "这是一封 Acme API SMTP 配置测试邮件。"},
		{language: "zh-Hant-HK", wantSubject: "Acme API 邮箱配置测试", wantBody: "这是一封 Acme API SMTP 配置测试邮件。"},
		{language: "en-US", wantSubject: "Acme API email configuration test", wantBody: "This is an SMTP configuration test email from Acme API."},
		{language: "", wantSubject: "Acme API 邮箱配置测试", wantBody: "这是一封 Acme API SMTP 配置测试邮件。"},
		{language: "invalid", wantSubject: "Acme API 邮箱配置测试", wantBody: "这是一封 Acme API SMTP 配置测试邮件。"},
	} {
		subject, body := testEmailMessage("Acme API", test.language)
		if subject != test.wantSubject || body != test.wantBody {
			t.Fatalf("testEmailMessage(%q) = %q / %q", test.language, subject, body)
		}
	}
}

func TestNormalizeQQLoginConfigRejectsEnableBeforeOfficialOAuthExists(t *testing.T) {
	_, err := normalizeQQLoginConfig(QQLoginConfig{
		AppID:           "app-id",
		AppKey:          "app-key",
		Enabled:         1,
		FrontendBaseURL: "https://console.example.com",
		RedirectURI:     "https://api.example.com/system-api/auth/qq/callback",
	})
	if err == nil {
		t.Fatal("normalizeQQLoginConfig allowed enabling an unimplemented OAuth flow")
	}
}

func TestNormalizeMobileConfigRejectsEnableBeforeOfficialSDKExists(t *testing.T) {
	_, err := normalizeMobileConfig(MobileConfig{
		AccessKeyID:     "access-key",
		AccessKeySecret: "secret",
		Enabled:         1,
		SignName:        "BugPk-Api",
		TemplateID:      "SMS_123",
	})
	if err == nil {
		t.Fatal("normalizeMobileConfig allowed enabling an unimplemented SMS provider")
	}
}

func TestNormalizeMobileConfigKeepsDisabledConfigurationDisabled(t *testing.T) {
	cfg, err := normalizeMobileConfig(MobileConfig{Enabled: 0})
	if err != nil {
		t.Fatalf("normalizeMobileConfig() error = %v", err)
	}
	if cfg.Enabled != 0 {
		t.Fatalf("normalized mobile provider enabled = %d", cfg.Enabled)
	}
}

func TestNormalizeEmailConfigDefaultsTLSAfterPortFallback(t *testing.T) {
	var req EmailConfig
	if err := json.Unmarshal([]byte(`{}`), &req); err != nil {
		t.Fatal(err)
	}

	cfg, err := normalizeEmailConfig(req, "BugPk-Api")
	if err != nil {
		t.Fatalf("normalizeEmailConfig() error = %v", err)
	}
	if cfg.SMTPPort != 587 || !cfg.SMTPTLS {
		t.Fatalf("default SMTP transport = port %d tls=%t, want port 587 tls=true", cfg.SMTPPort, cfg.SMTPTLS)
	}
}

func TestNormalizeEmailConfigPreservesExplicitTLSFalse(t *testing.T) {
	var req EmailConfig
	if err := json.Unmarshal([]byte(`{"smtpPort":587,"smtpTLS":false}`), &req); err != nil {
		t.Fatal(err)
	}

	cfg, err := normalizeEmailConfig(req, "BugPk-Api")
	if err != nil {
		t.Fatalf("normalizeEmailConfig() error = %v", err)
	}
	if cfg.SMTPPort != 587 || cfg.SMTPTLS {
		t.Fatalf("explicit SMTP transport = port %d tls=%t, want port 587 tls=false", cfg.SMTPPort, cfg.SMTPTLS)
	}
}

func TestSaveEmailConfigPreservesExistingSecret(t *testing.T) {
	repo := &settingsRepositoryStub{email: EmailConfig{AuthCode: "stored-secret"}}
	service := &Service{repo: repo}

	err := service.saveEmailConfig(context.Background(), EmailConfig{
		AuthCode:    secretMask,
		Enabled:     1,
		SenderEmail: "ops@example.com",
		SenderName:  "BugPk-Api",
		SMTPPort:    587,
		SMTPServer:  "smtp.example.com",
		SMTPTLS:     true,
		smtpTLSSet:  true,
	})
	if err != nil {
		t.Fatalf("saveEmailConfig() error = %v", err)
	}
	if repo.emailSaved.AuthCode != "stored-secret" {
		t.Fatalf("saved auth code = %q", repo.emailSaved.AuthCode)
	}
}

func TestSaveEmailConfigUsesConfiguredSiteNameAsDefaultSender(t *testing.T) {
	repo := &settingsRepositoryStub{
		email:    EmailConfig{AuthCode: "stored-secret"},
		siteName: "Acme API",
	}
	service := &Service{repo: repo}

	err := service.saveEmailConfig(context.Background(), EmailConfig{
		AuthCode:   secretMask,
		SMTPPort:   587,
		SMTPServer: "smtp.example.com",
		smtpTLSSet: true,
	})
	if err != nil {
		t.Fatalf("saveEmailConfig() error = %v", err)
	}
	if repo.emailSaved.SenderName != "Acme API" {
		t.Fatalf("saved sender name = %q", repo.emailSaved.SenderName)
	}
}

func TestSaveEmailConfigStopsWhenSiteNameCannotBeLoaded(t *testing.T) {
	siteErr := errors.New("site config unavailable")
	repo := &settingsRepositoryStub{
		email:       EmailConfig{AuthCode: "stored-secret"},
		siteNameErr: siteErr,
	}
	service := &Service{repo: repo}

	err := service.saveEmailConfig(context.Background(), EmailConfig{AuthCode: secretMask})
	if !errors.Is(err, siteErr) {
		t.Fatalf("saveEmailConfig() error = %v, want %v", err, siteErr)
	}
	if repo.emailSaveCalled {
		t.Fatal("email config save must not run after site-name load fails")
	}
}

func TestSaveEmailConfigStopsWhenCurrentConfigCannotBeLoaded(t *testing.T) {
	loadErr := errors.New("settings database unavailable")
	repo := &settingsRepositoryStub{emailLoadErr: loadErr}
	service := &Service{repo: repo}

	err := service.saveEmailConfig(context.Background(), EmailConfig{AuthCode: "replacement"})
	if !errors.Is(err, loadErr) {
		t.Fatalf("saveEmailConfig() error = %v, want %v", err, loadErr)
	}
	if repo.emailSaveCalled {
		t.Fatal("email config save must not run after the current config load fails")
	}
}

func TestSaveCaptchaScenesPassesNormalizedSelectionToRepository(t *testing.T) {
	repo := &settingsRepositoryStub{
		captchaRows: []CaptchaProviderRow{{
			ProviderKey:  captchaProviderPOW,
			Scene:        "default",
			Enabled:      1,
			SceneEnabled: 1,
			ExtraConfig:  `{"difficulty":3}`,
		}},
	}
	service := &Service{
		authz: &rbac.Service{},
		repo:  repo,
	}

	err := service.SaveCaptchaScenes(context.Background(), rbac.Principal{Roles: []string{"admin"}}, SaveCaptchaScenesRequest{
		Scenes: []string{"register", " login ", "register", "default"},
	})
	if err != nil {
		t.Fatalf("SaveCaptchaScenes() error = %v", err)
	}
	want := []string{"default", "login", "register"}
	if len(repo.savedCaptcha) != len(want) {
		t.Fatalf("saved scenes = %#v, want %#v", repo.savedCaptcha, want)
	}
	for index := range want {
		if repo.savedCaptcha[index] != want[index] {
			t.Fatalf("saved scenes = %#v, want %#v", repo.savedCaptcha, want)
		}
	}
}

func TestSaveCaptchaScenesDoesNotPersistInvalidSelection(t *testing.T) {
	repo := &settingsRepositoryStub{}
	service := &Service{authz: &rbac.Service{}, repo: repo}

	err := service.SaveCaptchaScenes(context.Background(), rbac.Principal{Roles: []string{"admin"}}, SaveCaptchaScenesRequest{
		Scenes: []string{"login", "unknown"},
	})
	if err == nil {
		t.Fatal("invalid captcha scene selection was accepted")
	}
	if repo.savedCaptcha != nil {
		t.Fatalf("repository was called for invalid selection: %#v", repo.savedCaptcha)
	}
}

func TestNormalizeEmailDomainRules(t *testing.T) {
	t.Run("normalizes and dedupes", func(t *testing.T) {
		rules, err := normalizeEmailDomainRules(EmailDomainRules{
			Whitelist: []string{" Example.COM ", "@Gmail.com", "gmail.com"},
			Blacklist: []string{"Spam.Example", "*.trash.example"},
		})
		if err != nil {
			t.Fatal(err)
		}
		wantWhitelist := []string{"example.com", "gmail.com"}
		if len(rules.Whitelist) != len(wantWhitelist) || rules.Whitelist[0] != wantWhitelist[0] || rules.Whitelist[1] != wantWhitelist[1] {
			t.Fatalf("whitelist = %v, want %v", rules.Whitelist, wantWhitelist)
		}
		if len(rules.Blacklist) != 2 || rules.Blacklist[0] != "spam.example" || rules.Blacklist[1] != "*.trash.example" {
			t.Fatalf("blacklist = %v", rules.Blacklist)
		}
	})

	t.Run("rejects invalid domain", func(t *testing.T) {
		if _, err := normalizeEmailDomainRules(EmailDomainRules{Whitelist: []string{"not a domain"}}); err == nil {
			t.Fatal("invalid domain must be rejected")
		}
		if _, err := normalizeEmailDomainRules(EmailDomainRules{Blacklist: []string{"bad..domain"}}); err == nil {
			t.Fatal("invalid domain must be rejected")
		}
	})

	t.Run("allows empty lists", func(t *testing.T) {
		rules, err := normalizeEmailDomainRules(EmailDomainRules{})
		if err != nil {
			t.Fatal(err)
		}
		if len(rules.Whitelist) != 0 || len(rules.Blacklist) != 0 {
			t.Fatalf("empty lists became %v", rules)
		}
	})
}

func TestSaveEmailDomainRulesPersistsNormalizedLists(t *testing.T) {
	repo := &settingsRepositoryStub{}
	service := &Service{authz: &rbac.Service{}, repo: repo}
	err := service.SaveEmailDomainRules(context.Background(), rbac.Principal{Roles: []string{"admin"}}, EmailDomainRules{
		Whitelist: []string{"Example.COM"},
		Blacklist: []string{"spam.example"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !repo.domainRulesSaveCalled {
		t.Fatal("repository save was not called")
	}
	if len(repo.domainRulesSaved.Whitelist) != 1 || repo.domainRulesSaved.Whitelist[0] != "example.com" {
		t.Fatalf("saved whitelist = %v", repo.domainRulesSaved.Whitelist)
	}
	if len(repo.domainRulesSaved.Blacklist) != 1 || repo.domainRulesSaved.Blacklist[0] != "spam.example" {
		t.Fatalf("saved blacklist = %v", repo.domainRulesSaved.Blacklist)
	}
}
