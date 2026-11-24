package systemsettings

import "encoding/json"

type RegisterOpenConfig struct {
	DefaultRoleID       int64  `json:"defaultRoleId,string"`
	DefaultRoleName     string `json:"defaultRoleName"`
	DefaultRoleQps      int    `json:"defaultRoleQps"`
	DefaultUserQps      int    `json:"defaultUserQps"`
	RegisterEnabled     int    `json:"registerEnabled"`
	RegisterGiftPoints  int64  `json:"registerGiftPoints"`
	RegisterIPHourLimit int    `json:"registerIpHourLimit"`
	RegisterIPDayLimit  int    `json:"registerIpDayLimit"`
	// RegisterIPMaxAccounts caps accounts per registration IP for their
	// lifetime; 0 disables the cap (legacy behavior).
	RegisterIPMaxAccounts int `json:"registerIpMaxAccounts"`
	// NewAccountDayLimit caps gateway calls during the first 24h after
	// registration; 0 disables the cap.
	NewAccountDayLimit int64 `json:"newAccountDayLimit"`
	// Registration risk controls (signed device cookie + /24 or /64 prefix).
	RegisterDeviceDayLimit        int    `json:"registerDeviceDayLimit"`
	RegisterIPPrefixDayLimit      int    `json:"registerIpPrefixDayLimit"`
	RegisterRiskEnabled           int    `json:"registerRiskEnabled"`
	RegisterRiskShadowMode        int    `json:"registerRiskShadowMode"`
	RegisterRiskQuarantineScore   int    `json:"registerRiskQuarantineScore"`
	RegisterRiskRejectScore       int    `json:"registerRiskRejectScore"`
	OAuthRegisterEnabled          int    `json:"oauthRegisterEnabled"`
	OAuthRegisterModeDefault      string `json:"oauthRegisterModeDefault"`
	OAuthRegisterIPHourLimit      int    `json:"oauthRegisterIpHourLimit"`
	OAuthRegisterIPDayLimit       int    `json:"oauthRegisterIpDayLimit"`
	OAuthRegisterIPMaxAccounts    int    `json:"oauthRegisterIpMaxAccounts"`
	OAuthRequireVerifiedEmail     int    `json:"oauthRequireVerifiedEmail"`
	VerificationCodeIPDayLimit    int    `json:"verificationCodeIpDayLimit"`
	VerificationCodeIPHourLimit   int    `json:"verificationCodeIpHourLimit"`
	VerificationCodeIPMinuteLimit int    `json:"verificationCodeIpMinuteLimit"`
}

// EmailDomainRules is the admin-managed registration email domain policy.
// A non-empty whitelist makes registration exclusive to those domains; the
// blacklist always rejects. Both lists are lower-cased bare domains.
type EmailDomainRules struct {
	Blacklist []string `json:"blacklist"`
	Whitelist []string `json:"whitelist"`
}

type EmailConfig struct {
	AuthCode    string `json:"authCode"`
	Enabled     int    `json:"enabled"`
	Language    string `json:"language,omitempty"`
	SenderEmail string `json:"senderEmail"`
	SenderName  string `json:"senderName"`
	SMTPPort    int    `json:"smtpPort"`
	SMTPServer  string `json:"smtpServer"`
	SMTPTLS     bool   `json:"smtpTLS"`
	TestEmail   string `json:"testEmail,omitempty"`
	smtpTLSSet  bool
}

func (c *EmailConfig) UnmarshalJSON(data []byte) error {
	type Alias EmailConfig
	raw := struct {
		*Alias
		SMTPTLS *bool `json:"smtpTLS"`
	}{
		Alias: (*Alias)(c),
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if raw.SMTPTLS != nil {
		c.SMTPTLS = *raw.SMTPTLS
		c.smtpTLSSet = true
	}
	return nil
}

type MobileConfig struct {
	AccessKeyID     string `json:"accessKeyId"`
	AccessKeySecret string `json:"accessKeySecret"`
	Enabled         int    `json:"enabled"`
	Endpoint        string `json:"endpoint"`
	Provider        string `json:"provider"`
	Region          string `json:"region"`
	SignName        string `json:"signName"`
	TemplateID      string `json:"templateId"`
	TestMobile      string `json:"testMobile,omitempty"`
}

type QQLoginConfig struct {
	AppID                string `json:"appId"`
	AppKey               string `json:"appKey"`
	Enabled              int    `json:"enabled"`
	FrontendBaseURL      string `json:"frontendBaseUrl"`
	RedirectURI          string `json:"redirectUri"`
	SuggestedRedirectURI string `json:"suggestedRedirectUri,omitempty"`
}
