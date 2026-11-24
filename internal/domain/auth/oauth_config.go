package auth

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	oauthProviderKeyPattern   = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)
	oauthProviderGroupPattern = regexp.MustCompile(`^[a-z0-9_]{0,32}$`)
	oauthIconKeyPattern       = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)
)

func isHTTPSRequired(appEnv string) bool {
	switch strings.ToLower(strings.TrimSpace(appEnv)) {
	case "development", "test":
		return false
	default:
		return true
	}
}

func oauthSchemeAllowed(scheme, appEnv string) bool {
	if isHTTPSRequired(appEnv) {
		return scheme == "https"
	}
	return scheme == "https" || scheme == "http"
}

func validateOAuthFrontendBaseURL(raw, appEnv string) (string, error) {
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	if value == "" {
		return "", nil
	}
	if len(value) > 1024 {
		return "", fmt.Errorf("前端基础地址不能超过1024个字符")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("前端基础地址格式不合法")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return "", fmt.Errorf("前端基础地址不能包含路径、查询参数或锚点")
	}
	if !oauthSchemeAllowed(parsed.Scheme, appEnv) {
		return "", fmt.Errorf("前端基础地址必须是 https 地址")
	}
	return value, nil
}

func validateOAuthRedirectURI(raw, appEnv string) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fmt.Errorf("回调地址不能为空")
	}
	if len(value) > 1024 {
		return fmt.Errorf("回调地址不能超过1024个字符")
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("回调地址格式不合法")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("回调地址不能包含查询参数或锚点")
	}
	if !oauthSchemeAllowed(parsed.Scheme, appEnv) {
		return fmt.Errorf("回调地址必须是 https 地址")
	}
	return nil
}

func validateOAuthEndpoint(raw, label, appEnv string, required bool) error {
	value := strings.TrimSpace(raw)
	if value == "" {
		if required {
			return fmt.Errorf("%s不能为空", label)
		}
		return nil
	}
	if len(value) > 1024 {
		return fmt.Errorf("%s不能超过1024个字符", label)
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return fmt.Errorf("%s格式不合法", label)
	}
	if parsed.Fragment != "" {
		return fmt.Errorf("%s不能包含锚点", label)
	}
	if !oauthSchemeAllowed(parsed.Scheme, appEnv) {
		return fmt.Errorf("%s必须是 https 地址", label)
	}
	return nil
}

func normalizeOAuthExtraConfig(raw string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	if len(value) > 16*1024 {
		return "", fmt.Errorf("扩展配置不能超过16 KiB")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(value), &parsed); err != nil {
		return "", fmt.Errorf("扩展配置必须是合法的 JSON 对象")
	}
	if parsed == nil {
		return "", fmt.Errorf("扩展配置必须是 JSON 对象")
	}
	return value, nil
}

func validateOAuthProviderConfig(cfg *OAuthProviderConfig, appEnv string, hasClientSecret bool) error {
	key := strings.TrimSpace(cfg.ProviderKey)
	if !oauthProviderKeyPattern.MatchString(key) {
		return fmt.Errorf("Provider 标识只能包含小写字母、数字和下划线，长度为2到32位")
	}
	if name := strings.TrimSpace(cfg.DisplayName); name == "" || utf8.RuneCountInString(name) > 64 {
		return fmt.Errorf("显示名称不能为空且不能超过64个字符")
	}
	if !oauthDriverExists(cfg.Driver) {
		return fmt.Errorf("不支持的 Provider 驱动")
	}
	if !oauthIconKeyPattern.MatchString(strings.TrimSpace(cfg.IconKey)) {
		return fmt.Errorf("图标标识不合法")
	}
	if !oauthProviderGroupPattern.MatchString(strings.TrimSpace(cfg.ProviderGroup)) {
		return fmt.Errorf("Provider 分组不合法")
	}
	switch cfg.RegisterMode {
	case OAuthRegisterModeInherit, OAuthRegisterModeOff, OAuthRegisterModeConfirm, OAuthRegisterModeAuto:
	default:
		return fmt.Errorf("注册模式不合法")
	}
	if cfg.LinkByEmail {
		return fmt.Errorf("暂不支持按已验证邮箱自动合并账号")
	}
	if cfg.RequireVerifiedEmailMode < -1 || cfg.RequireVerifiedEmailMode > 1 {
		return fmt.Errorf("已验证邮箱策略不合法")
	}
	if cfg.RequireVerifiedEmailMode == 1 && (cfg.Driver == OAuthDriverQQ || cfg.Driver == OAuthDriverWeChat) {
		return fmt.Errorf("QQ/微信不提供邮箱，不能要求已验证邮箱")
	}
	if cfg.DefaultRoleID < 0 {
		return fmt.Errorf("默认角色不合法")
	}
	if cfg.RegisterGiftPoints < -1 || cfg.RegisterGiftPoints > 1_000_000_000 {
		return fmt.Errorf("注册赠送点数必须在 -1 到 1000000000 之间")
	}
	if cfg.RegisterIPHourLimit < -1 || cfg.RegisterIPHourLimit > 1_000_000 ||
		cfg.RegisterIPDayLimit < -1 || cfg.RegisterIPDayLimit > 1_000_000 ||
		cfg.RegisterIPMaxAccounts < -1 || cfg.RegisterIPMaxAccounts > 1_000_000 {
		return fmt.Errorf("注册 IP 上限不合法")
	}
	if cfg.MinAccountAgeDays < 0 || cfg.MinAccountAgeDays > 36500 {
		return fmt.Errorf("第三方账号最小注册天数不合法")
	}
	if cfg.MinTrustLevel < 0 || cfg.MinTrustLevel > 100 {
		return fmt.Errorf("最低信任等级不合法")
	}
	if cfg.Enabled {
		if strings.TrimSpace(cfg.ClientID) == "" {
			return fmt.Errorf("启用前必须填写 Client ID")
		}
		if !hasClientSecret {
			return fmt.Errorf("启用前必须填写 Client Secret")
		}
		if err := validateOAuthRedirectURI(cfg.RedirectURI, appEnv); err != nil {
			return err
		}
	}
	if len(cfg.ClientID) > 255 {
		return fmt.Errorf("Client ID 不能超过255个字符")
	}
	if len(cfg.ClientSecret) > 1024 {
		return fmt.Errorf("Client Secret 不能超过1024个字符")
	}
	if len(strings.TrimSpace(cfg.Scopes)) > 512 {
		return fmt.Errorf("Scopes 不能超过512个字符")
	}
	if cfg.Driver == OAuthDriverOAuth2 || cfg.Driver == OAuthDriverOIDC {
		required := cfg.Enabled
		if err := validateOAuthEndpoint(cfg.AuthorizationEndpoint, "授权端点", appEnv, required); err != nil {
			return err
		}
		if err := validateOAuthEndpoint(cfg.TokenEndpoint, "Token 端点", appEnv, required); err != nil {
			return err
		}
		if err := validateOAuthEndpoint(cfg.UserinfoEndpoint, "用户信息端点", appEnv, required); err != nil {
			return err
		}
		if cfg.Driver == OAuthDriverOIDC {
			if err := validateOAuthEndpoint(cfg.Issuer, "Issuer", appEnv, required); err != nil {
				return err
			}
		}
	}
	normalizedExtra, err := normalizeOAuthExtraConfig(cfg.ExtraConfig)
	if err != nil {
		return err
	}
	cfg.ExtraConfig = normalizedExtra
	return nil
}

func validateOAuthGlobalConfig(cfg OAuthGlobalConfig, appEnv string) (OAuthGlobalConfig, error) {
	frontend, err := validateOAuthFrontendBaseURL(cfg.FrontendBaseURL, appEnv)
	if err != nil {
		return OAuthGlobalConfig{}, err
	}
	cfg.FrontendBaseURL = frontend
	return cfg, nil
}

func validateOAuthReturnPath(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > oauthMaxReturnPathBytes {
		return ""
	}
	if !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\\\r\n\t") {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil {
		return ""
	}
	return value
}

func oauthProviderView(cfg OAuthProviderConfig, systemPrefix string) OAuthProviderView {
	masked := ""
	if strings.TrimSpace(cfg.ClientSecret) != "" {
		masked = "****"
	}
	base := strings.TrimRight(strings.TrimSpace(systemPrefix), "/")
	return OAuthProviderView{
		ProviderKey:              cfg.ProviderKey,
		DisplayName:              cfg.DisplayName,
		IconKey:                  cfg.IconKey,
		Driver:                   cfg.Driver,
		ProviderGroup:            cfg.ProviderGroup,
		Enabled:                  cfg.Enabled,
		RegisterMode:             cfg.RegisterMode,
		DefaultRoleID:            cfg.DefaultRoleID,
		RegisterGiftPoints:       cfg.RegisterGiftPoints,
		RegisterIPHourLimit:      cfg.RegisterIPHourLimit,
		RegisterIPDayLimit:       cfg.RegisterIPDayLimit,
		RegisterIPMaxAccounts:    cfg.RegisterIPMaxAccounts,
		LinkByEmail:              cfg.LinkByEmail,
		RequireVerifiedEmail:     cfg.RequireVerifiedEmail,
		RequireVerifiedEmailMode: cfg.RequireVerifiedEmailMode,
		MinAccountAgeDays:        cfg.MinAccountAgeDays,
		MinTrustLevel:            cfg.MinTrustLevel,
		RequireActive:            cfg.RequireActive,
		RejectSilenced:           cfg.RejectSilenced,
		ClientID:                 cfg.ClientID,
		HasClientSecret:          strings.TrimSpace(cfg.ClientSecret) != "",
		ClientSecretMasked:       masked,
		Issuer:                   cfg.Issuer,
		AuthorizationEndpoint:    cfg.AuthorizationEndpoint,
		TokenEndpoint:            cfg.TokenEndpoint,
		UserinfoEndpoint:         cfg.UserinfoEndpoint,
		JWKSURI:                  cfg.JWKSURI,
		Scopes:                   cfg.Scopes,
		RedirectURI:              cfg.RedirectURI,
		ExtraConfig:              cfg.ExtraConfig,
		SortNo:                   cfg.SortNo,
		CallbackPath:             base + "/auth/oauth/" + cfg.ProviderKey + "/callback",
	}
}
