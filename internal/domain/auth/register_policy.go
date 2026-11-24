package auth

import (
	"context"
	"errors"
	"strings"
)

// RegisterPolicy is the effective, server-side registration policy for one
// channel. Provider overrides are resolved here; clients never choose role,
// gift points or limits.
type RegisterPolicy struct {
	Channel              string
	ProviderKey          string
	Enabled              bool
	Mode                 string
	DefaultRoleID        int64
	GiftPoints           int64
	IPHourLimit          int
	IPDayLimit           int
	IPMaxAccounts        int
	RequireVerifiedEmail bool
	MinAccountAgeDays    int
	MinTrustLevel        int
	RequireActive        bool
	RejectSilenced       bool
}

func (s *Service) LocalRegisterPolicy(ctx context.Context) (RegisterPolicy, error) {
	cfg, err := s.repo.RegisterConfig(ctx)
	if err != nil {
		return RegisterPolicy{}, err
	}
	if err := s.validatePolicyRole(ctx, cfg.DefaultRoleID); err != nil {
		return RegisterPolicy{}, err
	}
	return RegisterPolicy{
		Channel:       "local",
		Enabled:       cfg.RegisterEnabled,
		Mode:          OAuthRegisterModeConfirm,
		DefaultRoleID: cfg.DefaultRoleID,
		GiftPoints:    cfg.RegisterGiftPoints,
		IPHourLimit:   cfg.RegisterIPHourLimit,
		IPDayLimit:    cfg.RegisterIPDayLimit,
		IPMaxAccounts: cfg.RegisterIPMaxAccounts,
	}, nil
}

func (s *OAuthService) OAuthRegisterPolicy(ctx context.Context, provider OAuthProviderConfig) (RegisterPolicy, error) {
	cfg, err := s.auth.repo.RegisterConfig(ctx)
	if err != nil {
		return RegisterPolicy{}, err
	}
	policy, err := resolveOAuthRegisterPolicy(cfg, provider)
	if err != nil {
		return RegisterPolicy{}, err
	}
	if err := s.auth.validatePolicyRole(ctx, policy.DefaultRoleID); err != nil {
		return RegisterPolicy{}, err
	}
	return policy, nil
}

func resolveOAuthRegisterPolicy(cfg RegisterConfig, provider OAuthProviderConfig) (RegisterPolicy, error) {
	mode := strings.TrimSpace(provider.RegisterMode)
	if mode == "" || mode == OAuthRegisterModeInherit {
		mode = cfg.OAuthRegisterModeDefault
	}
	switch mode {
	case OAuthRegisterModeOff, OAuthRegisterModeConfirm, OAuthRegisterModeAuto:
	default:
		return RegisterPolicy{}, errors.New("第三方注册模式配置不合法")
	}
	roleID := provider.DefaultRoleID
	if roleID <= 0 {
		roleID = cfg.DefaultRoleID
	}
	giftPoints := provider.RegisterGiftPoints
	if giftPoints < 0 {
		giftPoints = cfg.RegisterGiftPoints
	}
	ipHourLimit := provider.RegisterIPHourLimit
	if ipHourLimit < 0 {
		ipHourLimit = cfg.OAuthRegisterIPHourLimit
	}
	ipDayLimit := provider.RegisterIPDayLimit
	if ipDayLimit < 0 {
		ipDayLimit = cfg.OAuthRegisterIPDayLimit
	}
	ipMaxAccounts := provider.RegisterIPMaxAccounts
	if ipMaxAccounts < 0 {
		ipMaxAccounts = cfg.OAuthRegisterIPMaxAccounts
	}
	requireVerifiedEmail := cfg.OAuthRequireVerifiedEmail
	switch provider.RequireVerifiedEmailMode {
	case 0:
		requireVerifiedEmail = false
	case 1:
		requireVerifiedEmail = true
	}
	return RegisterPolicy{
		Channel:              "oauth",
		ProviderKey:          provider.ProviderKey,
		Enabled:              cfg.OAuthRegisterEnabled,
		Mode:                 mode,
		DefaultRoleID:        roleID,
		GiftPoints:           giftPoints,
		IPHourLimit:          ipHourLimit,
		IPDayLimit:           ipDayLimit,
		IPMaxAccounts:        ipMaxAccounts,
		RequireVerifiedEmail: requireVerifiedEmail,
		MinAccountAgeDays:    provider.MinAccountAgeDays,
		MinTrustLevel:        provider.MinTrustLevel,
		RequireActive:        provider.RequireActive,
		RejectSilenced:       provider.RejectSilenced,
	}, nil
}

func (s *Service) validatePolicyRole(ctx context.Context, roleID int64) error {
	if roleID <= 0 {
		return nil
	}
	ok, err := s.repo.RegistrationRoleValid(ctx, roleID)
	if err != nil {
		return err
	}
	if !ok {
		return ErrRegistrationRoleInvalid
	}
	return nil
}

func (s *OAuthService) checkOAuthRisk(policy RegisterPolicy, identity OAuthIdentity) error {
	if policy.RequireActive && (!identity.ActiveSet || !identity.Active) {
		return newOAuthFlowError(OAuthErrorRiskBlocked, "该第三方账号当前不活跃或缺少活跃状态，暂不能注册", nil)
	}
	if policy.RejectSilenced && identity.Silenced {
		return newOAuthFlowError(OAuthErrorRiskBlocked, "该第三方账号当前受限，暂不能注册", nil)
	}
	if policy.MinTrustLevel > 0 && identity.TrustLevel < policy.MinTrustLevel {
		return newOAuthFlowError(OAuthErrorRiskBlocked, "该第三方账号信任等级不足，暂不能注册", nil)
	}
	if policy.MinAccountAgeDays > 0 {
		if identity.AccountCreatedAt.IsZero() {
			return newOAuthFlowError(OAuthErrorRiskBlocked, "该平台未提供账号注册时间，暂不能注册", nil)
		}
		if identity.AccountCreatedAt.AddDate(0, 0, policy.MinAccountAgeDays).After(s.now()) {
			return newOAuthFlowError(OAuthErrorRiskBlocked, "该第三方账号注册时间过短，暂不能注册", nil)
		}
	}
	return nil
}
