package auth

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"bugpk-api/internal/config"
)

func TestResolveEmailLang(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{"中文浏览器", "zh-CN", "zh"},
		{"中文简写", "zh", "zh"},
		{"中文下划线", "zh_CN", "zh"},
		{"中文大写", "ZH-CN", "zh"},
		{"中文繁体也归中文", "zh-Hant-TW", "zh"},
		{"中国香港中文也归中文", "zh-HK", "zh"},
		{"英文浏览器", "en-US", "en"},
		{"英文简写", "en", "en"},
		{"英文大写", "EN", "en"},
		{"多语言中文优先", "zh-CN,zh;q=0.9,en;q=0.8", "zh"},
		{"多语言英文优先", "en-US,en;q=0.9,zh-CN;q=0.8", "en"},
		{"权重反转取英文", "en;q=0.8,zh-CN;q=0.9", "zh"},
		{"权重反转取中文", "zh-CN;q=0.8,en;q=0.9", "en"},
		{"其它语言", "fr-FR", ""},
		{"其它语言带英文回退", "fr-FR,en;q=0.8", "en"},
		{"空头", "", ""},
		{"q为0被排除", "en;q=0", ""},
		{"通配符", "*", ""},
		{"带空格与大小写混合", " ZH-CN , en-US ; q=0.5 ", "zh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveEmailLang(tt.header); got != tt.want {
				t.Fatalf("ResolveEmailLang(%q) = %q, want %q", tt.header, got, tt.want)
			}
		})
	}
}

func TestResolveRequestEmailLang(t *testing.T) {
	tests := []struct {
		name           string
		appLocale      string
		acceptLanguage string
		want           string
	}{
		{name: "web page wins", appLocale: "zh-CN", acceptLanguage: "en-US", want: "zh"},
		{name: "english web page wins", appLocale: "en-US", acceptLanguage: "zh-CN", want: "en"},
		{name: "browser fallback", acceptLanguage: "zh-CN,zh;q=0.9", want: "zh"},
		{name: "traditional browser is Chinese", acceptLanguage: "zh-TW", want: "zh"},
		{name: "invalid page falls through to browser", appLocale: "invalid", acceptLanguage: "en-US", want: "en"},
		{name: "safe chinese default", acceptLanguage: "fr-FR", want: "zh"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ResolveRequestEmailLang(test.appLocale, test.acceptLanguage); got != test.want {
				t.Fatalf("ResolveRequestEmailLang(%q, %q) = %q, want %q", test.appLocale, test.acceptLanguage, got, test.want)
			}
		})
	}
}

func TestResolveEmailLanguagePriority(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                           string
		explicit, target, app, browser, site, wantLang string
	}{
		{name: "explicit page snapshot wins", explicit: "zh-TW", target: "en-US", app: "en-US", browser: "en-US", site: "en", wantLang: "zh"},
		{name: "app locale supplies missing page snapshot", app: "en-US", browser: "zh-CN", site: "zh", wantLang: "en"},
		{name: "target record wins over browser", target: "en-US", browser: "zh-CN", site: "zh", wantLang: "en"},
		{name: "browser fallback", browser: "en-US", site: "zh", wantLang: "en"},
		{name: "site fallback", browser: "invalid", site: "en-US", wantLang: "en"},
		{name: "invalid and missing default Chinese", explicit: "xx", target: "fr", app: "broken", browser: "", site: "", wantLang: "zh"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveEmailLanguage(test.explicit, test.target, test.app, test.browser, test.site)
			if got != test.wantLang {
				t.Fatalf("ResolveEmailLanguage() = %q, want %q", got, test.wantLang)
			}
		})
	}
}

// emailLangTestConfig 构造一套同时配置了中英文模板的邮件配置。
func emailLangTestConfig() config.EmailMessageConfig {
	return config.EmailMessageConfig{
		CodeSubject:     "BugPk-Api 验证码",
		CodeTemplate:    "您的验证码是 {{code}}，{{minutes}} 分钟内有效。",
		ResetSubject:    "BugPk-Api 找回密码验证码",
		ResetTemplate:   "您正在找回密码，验证码是 {{code}}，{{minutes}} 分钟内有效。",
		CodeSubjectEn:   "BugPk-Api verification code",
		CodeTemplateEn:  "Your verification code is {{code}} and is valid for {{minutes}} minutes.",
		ResetSubjectEn:  "BugPk-Api password reset code",
		ResetTemplateEn: "Your password reset code is {{code}} and is valid for {{minutes}} minutes.",
		SMTPFromName:    "BugPk-Api",
		SMTPFrom:        "no-reply@example.com",
		SMTPHost:        "smtp.example.com",
		SMTPPort:        587,
		SMTPUsername:    "no-reply@example.com",
		SMTPPassword:    "secret",
		Provider:        "smtp",
		Enabled:         true,
		CodeTTL:         10 * time.Minute,
		SMTPTimeout:     10 * time.Second,
	}
}

func TestEmailConfigSelectsChineseTemplateByDefaultLang(t *testing.T) {
	repo := &verificationCodeRepositoryStub{siteName: "Acme API"}
	service := &VerificationCodeService{
		cfg:  config.MessageConfig{Email: emailLangTestConfig()},
		repo: repo,
	}
	for _, lang := range []string{"", "zh"} {
		cfg, err := service.emailConfig(context.Background(), lang)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.CodeSubject != "Acme API 验证码" || cfg.CodeTemplate != "您的验证码是 {{code}}，{{minutes}} 分钟内有效。" {
			t.Fatalf("lang=%q code template is not Chinese: subject=%q template=%q", lang, cfg.CodeSubject, cfg.CodeTemplate)
		}
		if cfg.ResetSubject != "Acme API 找回密码验证码" || cfg.ResetTemplate != "您正在找回密码，验证码是 {{code}}，{{minutes}} 分钟内有效。" {
			t.Fatalf("lang=%q reset template is not Chinese: subject=%q template=%q", lang, cfg.ResetSubject, cfg.ResetTemplate)
		}
	}
}

func TestEmailConfigSelectsEnglishTemplateWhenLangIsEnglish(t *testing.T) {
	repo := &verificationCodeRepositoryStub{siteName: "Acme API"}
	service := &VerificationCodeService{
		cfg:  config.MessageConfig{Email: emailLangTestConfig()},
		repo: repo,
	}
	cfg, err := service.emailConfig(context.Background(), "en")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CodeSubject != "Acme API verification code" || cfg.CodeTemplate != "Your verification code is {{code}} and is valid for {{minutes}} minutes." {
		t.Fatalf("english code template was not selected: subject=%q template=%q", cfg.CodeSubject, cfg.CodeTemplate)
	}
	if cfg.ResetSubject != "Acme API password reset code" || cfg.ResetTemplate != "Your password reset code is {{code}} and is valid for {{minutes}} minutes." {
		t.Fatalf("english reset template was not selected: subject=%q template=%q", cfg.ResetSubject, cfg.ResetTemplate)
	}
}

func TestEmailConfigNormalizesRegionalEnglishAtTemplateBoundary(t *testing.T) {
	repo := &verificationCodeRepositoryStub{siteName: "Acme API"}
	service := &VerificationCodeService{
		cfg:  config.MessageConfig{Email: emailLangTestConfig()},
		repo: repo,
	}
	for _, language := range []string{"en-US", "EN_us", "zh-HK;q=0.4,en-GB;q=0.9"} {
		cfg, err := service.emailConfig(context.Background(), language)
		if err != nil {
			t.Fatal(err)
		}
		if cfg.CodeSubject != "Acme API verification code" || !strings.Contains(cfg.CodeTemplate, "Your verification code") {
			t.Fatalf("language %q did not select English templates: subject=%q template=%q", language, cfg.CodeSubject, cfg.CodeTemplate)
		}
	}
}

func TestEmailConfigFallsBackToChineseWhenEnglishTemplateEmpty(t *testing.T) {
	repo := &verificationCodeRepositoryStub{siteName: "Acme API"}
	service := &VerificationCodeService{
		cfg: config.MessageConfig{Email: config.EmailMessageConfig{
			CodeSubject:   "BugPk-Api 验证码",
			CodeTemplate:  "您的验证码是 {{code}}，{{minutes}} 分钟内有效。",
			ResetSubject:  "BugPk-Api 找回密码验证码",
			ResetTemplate: "您正在找回密码，验证码是 {{code}}，{{minutes}} 分钟内有效。",
			SMTPFromName:  "BugPk-Api",
		}},
		repo: repo,
	}
	cfg, err := service.emailConfig(context.Background(), "en")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CodeSubject != "Acme API 验证码" || cfg.CodeTemplate != "您的验证码是 {{code}}，{{minutes}} 分钟内有效。" {
		t.Fatalf("fallback code template is not Chinese: subject=%q template=%q", cfg.CodeSubject, cfg.CodeTemplate)
	}
	if cfg.ResetSubject != "Acme API 找回密码验证码" || cfg.ResetTemplate != "您正在找回密码，验证码是 {{code}}，{{minutes}} 分钟内有效。" {
		t.Fatalf("fallback reset template is not Chinese: subject=%q template=%q", cfg.ResetSubject, cfg.ResetTemplate)
	}
}

// capturedMail 记录 sendMail mock 实际收到的主题与正文。
type capturedMail struct {
	subject string
	body    string
}

var sixDigitCodePattern = regexp.MustCompile(`\d{6}`)

// newCapturingEmailService 构造带 sendMail mock 的服务，用于断言最终发送的
// subject/body 语言。emailConfig 与 generateAndSendEmailCode 正是三个发送场景
// （注册/绑定邮箱/找回密码）共用的「选模板 + 渲染 + 发送」链路，限流检查与
// 语言选择无关，故此处直接经由该链路验证语言落点。
func newCapturingEmailService(cfg config.EmailMessageConfig, captured *capturedMail) *VerificationCodeService {
	return &VerificationCodeService{
		cfg:  config.MessageConfig{Email: cfg},
		repo: &verificationCodeRepositoryStub{siteName: "Acme API"},
		now:  time.Now,
		sendMail: func(_ context.Context, _ config.EmailMessageConfig, _, subject, body string) error {
			captured.subject = subject
			captured.body = body
			return nil
		},
	}
}

func TestSendMailReceivesEnglishSubjectAndBodyWhenLangEnglish(t *testing.T) {
	for _, scene := range []struct {
		name             string
		scene            string
		wantSubject      string
		wantBodyFragment string
	}{
		{name: "注册", scene: emailSceneRegister, wantSubject: "Acme API verification code", wantBodyFragment: "Your verification code is"},
		{name: "绑定邮箱", scene: emailSceneBind, wantSubject: "Acme API verification code", wantBodyFragment: "Your verification code is"},
		{name: "找回密码", scene: emailScenePasswordReset, wantSubject: "Acme API password reset code", wantBodyFragment: "Your password reset code is"},
	} {
		t.Run(scene.name, func(t *testing.T) {
			captured := &capturedMail{}
			service := newCapturingEmailService(emailLangTestConfig(), captured)
			cfg, err := service.emailConfig(context.Background(), "en")
			if err != nil {
				t.Fatal(err)
			}
			subject, template := cfg.CodeSubject, cfg.CodeTemplate
			if scene.scene == emailScenePasswordReset {
				subject, template = cfg.ResetSubject, cfg.ResetTemplate
			}
			if err := service.generateAndSendEmailCode(context.Background(), cfg, scene.scene, "user@example.com", subject, template); err != nil {
				t.Fatal(err)
			}
			if captured.subject != scene.wantSubject {
				t.Fatalf("subject = %q, want %q", captured.subject, scene.wantSubject)
			}
			if !strings.Contains(captured.body, scene.wantBodyFragment) {
				t.Fatalf("body is not English: %q", captured.body)
			}
			if strings.Contains(captured.body, "您的验证码") || strings.Contains(captured.body, "您正在找回密码") {
				t.Fatalf("body leaked Chinese template: %q", captured.body)
			}
			if !sixDigitCodePattern.MatchString(captured.body) {
				t.Fatalf("body did not render the code: %q", captured.body)
			}
		})
	}
}

func TestSendMailReceivesChineseSubjectAndBodyByDefaultLang(t *testing.T) {
	for _, lang := range []string{"", "zh"} {
		captured := &capturedMail{}
		service := newCapturingEmailService(emailLangTestConfig(), captured)
		cfg, err := service.emailConfig(context.Background(), lang)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.generateAndSendEmailCode(context.Background(), cfg, emailSceneRegister, "user@example.com", cfg.CodeSubject, cfg.CodeTemplate); err != nil {
			t.Fatal(err)
		}
		if captured.subject != "Acme API 验证码" {
			t.Fatalf("lang=%q subject = %q, want Chinese", lang, captured.subject)
		}
		if !strings.Contains(captured.body, "您的验证码是") {
			t.Fatalf("lang=%q body is not Chinese: %q", lang, captured.body)
		}
		if strings.Contains(captured.body, "Your verification code") {
			t.Fatalf("lang=%q body leaked English template: %q", lang, captured.body)
		}
	}
}

func TestSendMailFallsBackToChineseWhenEnglishTemplateEmpty(t *testing.T) {
	captured := &capturedMail{}
	service := newCapturingEmailService(config.EmailMessageConfig{
		CodeSubject:  "BugPk-Api 验证码",
		CodeTemplate: "您的验证码是 {{code}}，{{minutes}} 分钟内有效。",
		SMTPFromName: "BugPk-Api",
	}, captured)
	cfg, err := service.emailConfig(context.Background(), "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.generateAndSendEmailCode(context.Background(), cfg, emailSceneRegister, "user@example.com", cfg.CodeSubject, cfg.CodeTemplate); err != nil {
		t.Fatal(err)
	}
	if captured.subject != "Acme API 验证码" {
		t.Fatalf("fallback subject = %q, want Chinese", captured.subject)
	}
	if !strings.Contains(captured.body, "您的验证码是") {
		t.Fatalf("fallback body is not Chinese: %q", captured.body)
	}
}

func TestSendMailRendersDBBackedEnglishTemplates(t *testing.T) {
	// DB 后台配置携带英文模板时，emailConfig 应合并 *En 字段并正确选择英文模板。
	repo := &verificationCodeRepositoryStub{
		siteName: "Acme API",
		emailConfig: config.EmailMessageConfig{
			CodeSubject:     "DB 中文主题",
			CodeTemplate:    "DB 中文模板 {{code}}",
			CodeSubjectEn:   "DB English subject",
			CodeTemplateEn:  "DB English template {{code}}",
			ResetSubjectEn:  "DB English reset subject",
			ResetTemplateEn: "DB English reset template {{code}}",
		},
		emailConfigOK: true,
	}
	captured := &capturedMail{}
	service := &VerificationCodeService{
		cfg:  config.MessageConfig{Email: emailLangTestConfig()},
		repo: repo,
		now:  time.Now,
		sendMail: func(_ context.Context, _ config.EmailMessageConfig, _, subject, body string) error {
			captured.subject = subject
			captured.body = body
			return nil
		},
	}
	cfg, err := service.emailConfig(context.Background(), "en")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.generateAndSendEmailCode(context.Background(), cfg, emailSceneRegister, "user@example.com", cfg.CodeSubject, cfg.CodeTemplate); err != nil {
		t.Fatal(err)
	}
	if captured.subject != "DB English subject" {
		t.Fatalf("DB-backed english subject = %q, want DB value", captured.subject)
	}
	if !strings.Contains(captured.body, "DB English template") {
		t.Fatalf("DB-backed english body = %q, want DB template rendered", captured.body)
	}
}

func TestEmailConfigDBBackedFallsBackToChineseWhenEnglishTemplateEmpty(t *testing.T) {
	// DB 配置存在但不含 *En 字段，且 config/env 也未配置英文模板时，
	// applyEmailSiteBrand 不应把空的英文模板品牌化（brandEmailField 保持空），
	// applyEmailLang 仍应回退中文模板。
	repo := &verificationCodeRepositoryStub{
		siteName: "Acme API",
		emailConfig: config.EmailMessageConfig{
			CodeSubject:  "DB 中文主题",
			CodeTemplate: "DB 中文模板 {{code}}",
		},
		emailConfigOK: true,
	}
	service := &VerificationCodeService{
		cfg: config.MessageConfig{Email: config.EmailMessageConfig{
			CodeSubject:   "BugPk-Api 验证码",
			CodeTemplate:  "您的验证码是 {{code}}，{{minutes}} 分钟内有效。",
			ResetSubject:  "BugPk-Api 找回密码验证码",
			ResetTemplate: "您正在找回密码，验证码是 {{code}}，{{minutes}} 分钟内有效。",
			SMTPFromName:  "BugPk-Api",
		}},
		repo: repo,
	}
	cfg, err := service.emailConfig(context.Background(), "en")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CodeSubject != "DB 中文主题" || cfg.CodeTemplate != "DB 中文模板 {{code}}" {
		t.Fatalf("DB-backed fallback is not Chinese: subject=%q template=%q", cfg.CodeSubject, cfg.CodeTemplate)
	}
}

func TestEmailConfigForcesChineseTemplateWhenDefaultFieldIsMistakenlyEnglish(t *testing.T) {
	// 回归：线上 config.yaml 把英文模板误配到「中文默认模板」字段时，
	// zh 语言也必须在中文模板上兜底，不能把英文正文发给中文用户。
	repo := &verificationCodeRepositoryStub{siteName: "Acme API"}
	service := &VerificationCodeService{
		cfg: config.MessageConfig{Email: config.EmailMessageConfig{
			CodeSubject:   "BugPk-Api verification code",
			CodeTemplate:  "Your verification code is {{code}} and is valid for {{minutes}} minutes. Ignore this email if you did not request it.",
			ResetSubject:  "BugPk-Api password reset code",
			ResetTemplate: "Your password reset code is {{code}} and is valid for {{minutes}} minutes. Ignore this email if you did not request it.",
			SMTPFromName:  "BugPk-Api",
		}},
		repo: repo,
	}
	for _, lang := range []string{"", "zh", "zh-CN", "invalid"} {
		cfg, err := service.emailConfig(context.Background(), lang)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(cfg.CodeSubject, "验证码") || !strings.Contains(cfg.CodeTemplate, "您的验证码是") {
			t.Fatalf("lang=%q code template is not Chinese after fallback: subject=%q template=%q", lang, cfg.CodeSubject, cfg.CodeTemplate)
		}
		if !strings.Contains(cfg.ResetSubject, "找回密码") || !strings.Contains(cfg.ResetTemplate, "您正在找回密码") {
			t.Fatalf("lang=%q reset template is not Chinese after fallback: subject=%q template=%q", lang, cfg.ResetSubject, cfg.ResetTemplate)
		}
	}
}

func TestEmailConfigKeepsLegitimateChineseTemplatesWhenLangIsChinese(t *testing.T) {
	// 中文模板字段正常中文配置时，加固不得误伤。
	repo := &verificationCodeRepositoryStub{siteName: "Acme API"}
	service := &VerificationCodeService{
		cfg: config.MessageConfig{Email: config.EmailMessageConfig{
			CodeSubject:   "BugPk-Api 验证码",
			CodeTemplate:  "您的验证码是 {{code}}，{{minutes}} 分钟内有效。如非本人操作，请忽略本邮件。",
			ResetSubject:  "BugPk-Api 找回密码验证码",
			ResetTemplate: "您正在找回密码，验证码是 {{code}}，{{minutes}} 分钟内有效。如非本人操作，请忽略本邮件。",
			SMTPFromName:  "BugPk-Api",
		}},
		repo: repo,
	}
	cfg, err := service.emailConfig(context.Background(), "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CodeSubject != "Acme API 验证码" || !strings.Contains(cfg.CodeTemplate, "您的验证码是") {
		t.Fatalf("legitimate Chinese template was damaged: subject=%q template=%q", cfg.CodeSubject, cfg.CodeTemplate)
	}
}

func TestEmailConfigStillSelectsEnglishTemplateWhenLangIsEnglish(t *testing.T) {
	// lang=en 时仍正确使用英文模板（*En 字段优先），不受中文兜底影响。
	repo := &verificationCodeRepositoryStub{siteName: "Acme API"}
	service := &VerificationCodeService{
		cfg:  config.MessageConfig{Email: emailLangTestConfig()},
		repo: repo,
	}
	cfg, err := service.emailConfig(context.Background(), "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cfg.CodeTemplate, "Your verification code") || !strings.Contains(cfg.ResetTemplate, "Your password reset code") {
		t.Fatalf("english template was not selected: code=%q reset=%q", cfg.CodeTemplate, cfg.ResetTemplate)
	}
}
