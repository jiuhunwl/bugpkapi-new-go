package auth

import "time"

type User struct {
	BanPermanent int
	BanReason    string
	BanUntil     string
	ID           int64
	Username     string
	PasswordHash string
	DisplayName  string
	Email        *string
	Mobile       *string
	Status       int
	TokenVersion int
	Roles        []string
	// TOTPEnabled 表示 2FA 已绑定并确认；登录与找回密码验证都必须校验。
	TOTPEnabled bool
	// TOTPSecret 是解密后的 Base32 密钥；仅服务端使用，禁止出现在任何响应里。
	TOTPSecret string
}

type RegisterConfig struct {
	DefaultRoleID                 int64
	RegisterEnabled               bool
	EmailRegisterEnabled          bool
	MobileRegisterEnabled         bool
	RegisterGiftPoints            int64
	VerificationCodeIPMinuteLimit int
	VerificationCodeIPHourLimit   int
	VerificationCodeIPDayLimit    int
	RegisterIPHourLimit           int
	RegisterIPDayLimit            int
	// RegisterIPMaxAccounts caps how many accounts may share one registration
	// IP for their lifetime (bp_register_ip_used). 0 disables the cap.
	RegisterIPMaxAccounts int
	// NewAccountDayLimit caps gateway calls during the first 24h after
	// registration, independently of the role quota. 0 disables the cap.
	NewAccountDayLimit int64
	// Registration risk controls. Device/prefix caps count only accounts that
	// were actually created; rejected attempts are bounded by the IP rate
	// limiter instead.
	RegisterDeviceDayLimit      int
	RegisterIPPrefixDayLimit    int
	RegisterRiskEnabled         bool
	RegisterRiskShadowMode      bool
	RegisterRiskQuarantineScore int
	RegisterRiskRejectScore     int
	OAuthRegisterEnabled        bool
	OAuthRegisterModeDefault    string
	OAuthRegisterIPHourLimit    int
	OAuthRegisterIPDayLimit     int
	OAuthRegisterIPMaxAccounts  int
	OAuthRequireVerifiedEmail   bool
}

type CaptchaProviderConfig struct {
	ProviderKey   string
	Scene         string
	Enabled       bool
	SiteKey       string
	SecretRef     string
	TimeoutMS     int
	FailurePolicy string
	ExtraConfig   string
}

type CaptchaGlobalConfig struct {
	Enabled bool `json:"enabled"`
}

type CaptchaChallenge struct {
	CaptchaID  string         `json:"captchaId"`
	Provider   string         `json:"provider"`
	Challenge  string         `json:"challenge"`
	Difficulty int            `json:"difficulty"`
	Scene      string         `json:"scene"`
	Enabled    bool           `json:"enabled"`
	Meta       map[string]any `json:"meta,omitempty"`
}

type CaptchaVerifyRequest struct {
	// POW uses proof/time.  Third-party providers use the provider-specific
	// fields below; keeping them on the same endpoint lets every auth scene
	// share one atomic, single-use captcha lifecycle.
	Proof         string `json:"proof"`
	Time          string `json:"time"`
	Token         string `json:"token"`
	LotNumber     string `json:"lot_number"`
	CaptchaOutput string `json:"captcha_output"`
	PassToken     string `json:"pass_token"`
	GenTime       string `json:"gen_time"`
}

type LoginRequest struct {
	CaptchaID string `json:"captchaId"`
	Password  string `json:"password"`
	Username  string `json:"username"`
}

type LoginResult struct {
	AccessToken string `json:"accessToken,omitempty"`
	// TwoFARequired=true 表示密码已通过、等待第二因子：前端拿
	// challengeToken 调 POST /auth/2fa/login。
	TwoFARequired  bool   `json:"twoFARequired,omitempty"`
	ChallengeToken string `json:"challengeToken,omitempty"`
	ExpiresIn      int    `json:"expiresIn,omitempty"`
}

type RegisterRequest struct {
	CaptchaID       string `json:"captchaId"`
	ConfirmPassword string `json:"confirmPassword"`
	EmailCode       string `json:"emailCode"`
	Email           string `json:"email"`
	MobileCode      string `json:"mobileCode"`
	Mobile          string `json:"mobile"`
	Password        string `json:"password"`
	// RoleID is retained only so older clients can keep sending the field.
	// Public registration never trusts or applies it; the server-controlled
	// default registration role is resolved inside the user creation transaction.
	RoleID string `json:"roleId"`
	// Website is a honeypot field: it is never rendered by the real client.
	// Automated clients that blindly fill every form input trip the guard and
	// receive a success-looking response without any account being created.
	Website  string `json:"website"`
	Username string `json:"username"`
}

type RegisterRole struct {
	Default  bool   `json:"default"`
	ID       string `json:"id"`
	Name     string `json:"name"`
	QPSLimit int    `json:"qpsLimit"`
	RoleKey  string `json:"roleKey"`
}

type PublicRegisterConfig struct {
	EmailRegisterEnabled  bool     `json:"emailRegisterEnabled"`
	EmailServiceEnabled   bool     `json:"emailServiceEnabled"`
	MobileRegisterEnabled bool     `json:"mobileRegisterEnabled"`
	RegisterEnabled       bool     `json:"registerEnabled"`
	CaptchaProviders      []string `json:"captchaProviders"`
}

type TokenPair struct {
	AccessToken  string
	RefreshToken string
	RefreshJTI   string
	RefreshExp   time.Time
}

type SendRegisterEmailCodeRequest struct {
	CaptchaID string `json:"captchaId"`
	Email     string `json:"email"`
}

type SendRegisterMobileCodeRequest struct {
	CaptchaID string `json:"captchaId"`
	Mobile    string `json:"mobile"`
}

type PasswordResetAccountRequest struct {
	Account string `json:"account"`
}

type PasswordResetAccountResult struct {
	MaskedEmail string `json:"maskedEmail"`
}

type PasswordResetEmailCodeRequest struct {
	Account   string `json:"account"`
	CaptchaID string `json:"captchaId"`
}

type PasswordResetVerifyRequest struct {
	Account string `json:"account"`
	Code    string `json:"code"`
	// TOTPCode 在目标账号启用 2FA 时必填：邮箱验证码之外的第二道校验，
	// 防止邮箱被控后直接重置管理员密码。
	TOTPCode string `json:"totpCode"`
}

type PasswordResetVerifyResult struct {
	ResetToken string `json:"resetToken"`
}

type PasswordResetRequest struct {
	ConfirmPassword string `json:"confirmPassword"`
	Password        string `json:"password"`
	ResetToken      string `json:"resetToken"`
}

// TwoFAChallenge 是密码校验通过、等待第二因子时的响应：
// challengeToken 是短寿命一次性 JWT（tokenType=2fa），只允许调用
// POST /auth/2fa/login 换取正式令牌。
type TwoFAChallenge struct {
	ChallengeToken string `json:"challengeToken"`
	ExpiresIn      int    `json:"expiresIn"`
}

// LoginOutcome 是登录入口的联合结果：要么直接发令牌，要么进入 2FA 步骤。
type LoginOutcome struct {
	Pair     TokenPair
	TwoFA    *TwoFAChallenge
	TwoFARequired bool
}

type TwoFAStatusResult struct {
	Enabled                 bool   `json:"enabled"`
	ConfirmedAt             string `json:"confirmedAt,omitempty"`
	RecoveryCodesRemaining  int    `json:"recoveryCodesRemaining"`
}

type TwoFASetupRequest struct{}

type TwoFASetupResult struct {
	Secret      string `json:"secret"`
	OtpauthURL  string `json:"otpauthUrl"`
}

type TwoFAEnableRequest struct {
	Code string `json:"code"`
}

type TwoFAEnableResult struct {
	RecoveryCodes []string `json:"recoveryCodes"`
}

type TwoFADisableRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

type TwoFARecoveryRegenerateRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

type TwoFALoginRequest struct {
	ChallengeToken string `json:"challengeToken"`
	Code           string `json:"code"`
}
