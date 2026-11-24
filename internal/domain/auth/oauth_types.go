package auth

import (
	"fmt"
	"strings"
	"time"

	"bugpk-api/internal/pkg/masking"
)

const (
	OAuthModeLogin = "login"
	OAuthModeBind  = "bind"

	OAuthRegisterModeInherit = "inherit"
	OAuthRegisterModeOff     = "off"
	OAuthRegisterModeConfirm = "confirm"
	OAuthRegisterModeAuto    = "auto"

	OAuthDriverQQ     = "qq"
	OAuthDriverWeChat = "wechat"
	OAuthDriverGitHub = "github"
	OAuthDriverOAuth2 = "oauth2"
	OAuthDriverOIDC   = "oidc"

	oauthFlowCookieName = "bp_oauth_flow"

	oauthStateTTL   = 10 * time.Minute
	oauthTicketTTL  = 60 * time.Second
	oauthPendingTTL = 10 * time.Minute
	oauthCookieTTL  = 10 * time.Minute

	oauthMaxReturnPathBytes = 512
)

const (
	OAuthErrorStateInvalid       = "state_invalid"
	OAuthErrorFlowInvalid        = "flow_invalid"
	OAuthErrorProviderDenied     = "provider_denied"
	OAuthErrorProviderDisabled   = "provider_disabled"
	OAuthErrorProviderError      = "provider_error"
	OAuthErrorNotRegistered      = "not_registered"
	OAuthErrorRegisterDisabled   = "register_disabled"
	OAuthErrorRegisterLimited    = "register_limited"
	OAuthErrorEmailConflict      = "email_conflict"
	OAuthErrorEmailRequired      = "email_required"
	OAuthErrorRiskBlocked        = "risk_blocked"
	OAuthErrorIdentityConflict   = "identity_conflict"
	OAuthErrorAlreadyRegistered  = "already_registered"
	OAuthErrorBanned             = "banned"
	OAuthErrorAccountUnavailable = "account_unavailable"
	OAuthErrorConfig             = "config_error"
	OAuthErrorInternal           = "internal_error"
)

// OAuthFlowError is safe to translate into a callback redirect code. It never
// carries provider tokens, authorization codes or client secrets.
type OAuthFlowError struct {
	Code    string
	Message string
	Err     error
	// Detail 是可选的脱敏上游错误说明（经 masking 处理并截断），仅用于在
	// 回调重定向前端时帮助定位平台配置问题，不包含任何凭证或网络端点。
	Detail string
}

func (e *OAuthFlowError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.Err)
}

func (e *OAuthFlowError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func newOAuthFlowError(code, message string, err error) *OAuthFlowError {
	return &OAuthFlowError{Code: code, Message: message, Err: err}
}

// sanitizeOAuthDetail turns an upstream error into a bounded, masked detail
// string that is safe to carry through a frontend redirect query parameter.
func sanitizeOAuthDetail(err error) string {
	if err == nil {
		return ""
	}
	const maxLen = 120
	detail := masking.Error(err)
	runes := []rune(detail)
	if len(runes) > maxLen {
		runes = runes[:maxLen]
	}
	return strings.TrimSpace(string(runes))
}

type OAuthGlobalConfig struct {
	FrontendBaseURL string
}

type OAuthProviderConfig struct {
	ID                       int64
	ProviderKey              string
	DisplayName              string
	IconKey                  string
	Driver                   string
	ProviderGroup            string
	Enabled                  bool
	RegisterMode             string
	DefaultRoleID            int64
	RegisterGiftPoints       int64
	RegisterIPHourLimit      int
	RegisterIPDayLimit       int
	RegisterIPMaxAccounts    int
	LinkByEmail              bool
	RequireVerifiedEmail     bool
	RequireVerifiedEmailMode int
	MinAccountAgeDays        int
	MinTrustLevel            int
	RequireActive            bool
	RejectSilenced           bool
	ClientID                 string
	ClientSecret             string
	Issuer                   string
	AuthorizationEndpoint    string
	TokenEndpoint            string
	UserinfoEndpoint         string
	JWKSURI                  string
	Scopes                   string
	RedirectURI              string
	ExtraConfig              string
	SortNo                   int
}

type OAuthPublicProvider struct {
	ProviderKey  string `json:"providerKey"`
	DisplayName  string `json:"displayName"`
	IconKey      string `json:"iconKey"`
	Driver       string `json:"driver"`
	RegisterMode string `json:"registerMode"`
}

type OAuthAuthorizeRequest struct {
	Mode       string `json:"mode"`
	ReturnPath string `json:"returnPath"`
}

type OAuthAuthorizeResult struct {
	URL string `json:"url"`
}

type OAuthExchangeRequest struct {
	Ticket string `json:"ticket"`
}

type OAuthLoginResult struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"-"`
	Redirect     string `json:"redirect,omitempty"`
}

type OAuthCompleteRequest struct {
	Ticket          string `json:"ticket"`
	Username        string `json:"username"`
	Password        string `json:"password"`
	ConfirmPassword string `json:"confirmPassword"`
	Email           string `json:"email"`
	EmailCode       string `json:"emailCode"`
	Website         string `json:"website"`
}

type OAuthCompleteInfo struct {
	Provider              string `json:"provider"`
	DisplayName           string `json:"displayName"`
	IconKey               string `json:"iconKey"`
	Nickname              string `json:"nickname"`
	Avatar                string `json:"avatar"`
	SuggestedUsername     string `json:"suggestedUsername"`
	Email                 string `json:"email,omitempty"`
	EmailVerified         bool   `json:"emailVerified"`
	RequiresVerifiedEmail bool   `json:"requiresVerifiedEmail"`
}

type OAuthBindingProvider struct {
	ProviderKey string `json:"providerKey"`
	DisplayName string `json:"displayName"`
	IconKey     string `json:"iconKey"`
	Enabled     bool   `json:"enabled"`
	Bound       bool   `json:"bound"`
	Nickname    string `json:"nickname,omitempty"`
	Avatar      string `json:"avatar,omitempty"`
	BoundAt     string `json:"boundAt,omitempty"`
	LastLoginAt string `json:"lastLoginAt,omitempty"`
}

type OAuthBindings struct {
	HasPassword bool                   `json:"hasPassword"`
	Providers   []OAuthBindingProvider `json:"providers"`
}

type OAuthUnbindRequest struct {
	Password  string `json:"password"`
	EmailCode string `json:"emailCode"`
}

type OAuthGlobalConfigView struct {
	FrontendBaseURL string `json:"frontendBaseUrl"`
}

type OAuthProviderView struct {
	ProviderKey              string `json:"providerKey"`
	DisplayName              string `json:"displayName"`
	IconKey                  string `json:"iconKey"`
	Driver                   string `json:"driver"`
	ProviderGroup            string `json:"providerGroup"`
	Enabled                  bool   `json:"enabled"`
	RegisterMode             string `json:"registerMode"`
	DefaultRoleID            int64  `json:"defaultRoleId,string"`
	RegisterGiftPoints       int64  `json:"registerGiftPoints"`
	RegisterIPHourLimit      int    `json:"registerIpHourLimit"`
	RegisterIPDayLimit       int    `json:"registerIpDayLimit"`
	RegisterIPMaxAccounts    int    `json:"registerIpMaxAccounts"`
	LinkByEmail              bool   `json:"linkByEmail"`
	RequireVerifiedEmail     bool   `json:"requireVerifiedEmail"`
	RequireVerifiedEmailMode int    `json:"requireVerifiedEmailMode"`
	MinAccountAgeDays        int    `json:"minAccountAgeDays"`
	MinTrustLevel            int    `json:"minTrustLevel"`
	RequireActive            bool   `json:"requireActive"`
	RejectSilenced           bool   `json:"rejectSilenced"`
	ClientID                 string `json:"clientId"`
	HasClientSecret          bool   `json:"hasClientSecret"`
	ClientSecretMasked       string `json:"clientSecretMasked"`
	Issuer                   string `json:"issuer"`
	AuthorizationEndpoint    string `json:"authorizationEndpoint"`
	TokenEndpoint            string `json:"tokenEndpoint"`
	UserinfoEndpoint         string `json:"userinfoEndpoint"`
	JWKSURI                  string `json:"jwksUri"`
	Scopes                   string `json:"scopes"`
	RedirectURI              string `json:"redirectUri"`
	ExtraConfig              string `json:"extraConfig"`
	SortNo                   int    `json:"sortNo"`
	CallbackPath             string `json:"callbackPath"`
}

type OAuthAdminConfig struct {
	Global    OAuthGlobalConfigView `json:"global"`
	Providers []OAuthProviderView   `json:"providers"`
}

type OAuthGlobalConfigSaveRequest struct {
	FrontendBaseURL string `json:"frontendBaseUrl"`
}

type OAuthProviderSaveRequest struct {
	ProviderKey              string `json:"providerKey"`
	DisplayName              string `json:"displayName"`
	IconKey                  string `json:"iconKey"`
	Driver                   string `json:"driver"`
	ProviderGroup            string `json:"providerGroup"`
	Enabled                  bool   `json:"enabled"`
	RegisterMode             string `json:"registerMode"`
	DefaultRoleID            int64  `json:"defaultRoleId,string"`
	RegisterGiftPoints       int64  `json:"registerGiftPoints"`
	RegisterIPHourLimit      int    `json:"registerIpHourLimit"`
	RegisterIPDayLimit       int    `json:"registerIpDayLimit"`
	RegisterIPMaxAccounts    int    `json:"registerIpMaxAccounts"`
	LinkByEmail              bool   `json:"linkByEmail"`
	RequireVerifiedEmail     bool   `json:"requireVerifiedEmail"`
	RequireVerifiedEmailMode int    `json:"requireVerifiedEmailMode"`
	MinAccountAgeDays        int    `json:"minAccountAgeDays"`
	MinTrustLevel            int    `json:"minTrustLevel"`
	RequireActive            bool   `json:"requireActive"`
	RejectSilenced           bool   `json:"rejectSilenced"`
	ClientID                 string `json:"clientId"`
	ClientSecret             string `json:"clientSecret"`
	ClearClientSecret        bool   `json:"clearClientSecret"`
	Issuer                   string `json:"issuer"`
	AuthorizationEndpoint    string `json:"authorizationEndpoint"`
	TokenEndpoint            string `json:"tokenEndpoint"`
	UserinfoEndpoint         string `json:"userinfoEndpoint"`
	JWKSURI                  string `json:"jwksUri"`
	Scopes                   string `json:"scopes"`
	RedirectURI              string `json:"redirectUri"`
	ExtraConfig              string `json:"extraConfig"`
	SortNo                   int    `json:"sortNo"`
}

type OAuthProviderTestResult struct {
	AuthorizeURL string `json:"authorizeUrl"`
}

type OAuthToken struct {
	AccessToken string
	TokenType   string
	IDToken     string
	OpenID      string
	ExpiresIn   int
}

type OAuthIdentity struct {
	ID               int64
	UserID           int64
	ProviderKey      string
	ProviderGroup    string
	Subject          string
	UnionID          string
	Username         string
	Nickname         string
	Avatar           string
	Email            string
	EmailVerified    bool
	TrustLevel       int
	Active           bool
	ActiveSet        bool
	Silenced         bool
	AccountCreatedAt time.Time
	BoundAt          string
	LastLoginAt      string
}

type ExternalIdentity struct {
	Subject          string
	UnionID          string
	Username         string
	Nickname         string
	Avatar           string
	Email            string
	EmailVerified    bool
	AccountCreatedAt time.Time
	TrustLevel       int
	Active           bool
	ActiveSet        bool
	Silenced         bool
}

type oauthFlow struct {
	Provider     string `json:"provider"`
	Mode         string `json:"mode"`
	UserID       int64  `json:"userId,omitempty"`
	TokenVersion int    `json:"tokenVersion,omitempty"`
	ReturnPath   string `json:"returnPath,omitempty"`
	CodeVerifier string `json:"codeVerifier,omitempty"`
	Nonce        string `json:"nonce,omitempty"`
	FlowID       string `json:"flowId"`
	IssuedAt     int64  `json:"issuedAt"`
}

type oauthLoginTicket struct {
	UserID       int64  `json:"userId"`
	TokenVersion int    `json:"tokenVersion"`
	Provider     string `json:"provider"`
	ReturnPath   string `json:"returnPath,omitempty"`
	FlowID       string `json:"flowId"`
	IssuedAt     int64  `json:"issuedAt"`
}

type oauthPending struct {
	Provider   string        `json:"provider"`
	FlowID     string        `json:"flowId"`
	ReturnPath string        `json:"returnPath,omitempty"`
	Identity   OAuthIdentity `json:"identity"`
	IssuedAt   int64         `json:"issuedAt"`
}

type OAuthCallbackResult struct {
	RedirectPath string
	ClearFlow    bool
}
