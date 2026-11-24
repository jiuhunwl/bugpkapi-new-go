package auth

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"bugpk-api/internal/pkg/emailaddr"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/iphash"
	"bugpk-api/internal/pkg/masking"
	"bugpk-api/internal/pkg/passwordpolicy"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

type AdminPermissionChecker interface {
	HasPermission(ctx context.Context, userID int64, roles []string, code string) (bool, error)
}

type OAuthService struct {
	appEnv       string
	systemPrefix string
	auth         *Service
	repo         *OAuthRepository
	states       *oauthStateStore
	httpClient   *http.Client
	log          *zap.Logger
	adminChecker AdminPermissionChecker
	now          func() time.Time
}

func NewOAuthService(appEnv, systemPrefix string, auth *Service, repo *OAuthRepository, rdb *redis.Client, httpClient *http.Client) *OAuthService {
	return &OAuthService{
		appEnv:       appEnv,
		systemPrefix: strings.TrimRight(strings.TrimSpace(systemPrefix), "/"),
		auth:         auth,
		repo:         repo,
		states:       newOAuthStateStore(rdb),
		httpClient:   httpClient,
		log:          zap.NewNop(),
		now:          time.Now,
	}
}

func (s *OAuthService) SetLogger(log *zap.Logger) {
	if log == nil {
		log = zap.NewNop()
	}
	s.log = log
}

func (s *OAuthService) SetAdminPermissionChecker(checker AdminPermissionChecker) {
	s.adminChecker = checker
}

func (s *OAuthService) FrontendBaseURL(ctx context.Context) (string, error) {
	global, err := s.repo.GlobalConfig(ctx)
	if err != nil {
		return "", err
	}
	return validateOAuthFrontendBaseURL(global.FrontendBaseURL, s.appEnv)
}

func (s *OAuthService) PublicProviders(ctx context.Context) ([]OAuthPublicProvider, error) {
	providers, err := s.repo.Providers(ctx, true)
	if err != nil {
		return nil, apperrors.Wrap(consts.StatusServiceUnavailable, "第三方登录配置暂不可用", err)
	}
	result := make([]OAuthPublicProvider, 0, len(providers))
	for _, provider := range providers {
		result = append(result, OAuthPublicProvider{
			ProviderKey:  provider.ProviderKey,
			DisplayName:  provider.DisplayName,
			IconKey:      provider.IconKey,
			Driver:       provider.Driver,
			RegisterMode: provider.RegisterMode,
		})
	}
	return result, nil
}

func (s *OAuthService) Authorize(ctx context.Context, providerKey, mode, returnPath string, userID int64, ip, ua string) (string, string, error) {
	provider, err := s.enabledProvider(ctx, providerKey)
	if err != nil {
		return "", "", err
	}
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = OAuthModeLogin
	}
	if mode != OAuthModeLogin && mode != OAuthModeBind {
		return "", "", apperrors.New(consts.StatusBadRequest, "授权模式不合法")
	}
	if err := s.checkRateLimit(ctx, "authorize", ip); err != nil {
		return "", "", err
	}
	var user User
	if mode == OAuthModeBind {
		if userID <= 0 {
			return "", "", apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
		}
		user, err = s.auth.repo.FindUserByID(ctx, userID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", "", apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
			}
			return "", "", apperrors.Wrap(consts.StatusServiceUnavailable, "认证服务暂时不可用", err)
		}
		if err := s.auth.checkAccountActive(user); err != nil {
			return "", "", err
		}
	}

	driver, ok := oauthDriverFor(provider.Driver)
	if !ok {
		return "", "", apperrors.New(consts.StatusBadRequest, "该登录方式暂不支持")
	}
	state, err := randomHex(32)
	if err != nil {
		return "", "", err
	}
	flowID, err := randomHex(32)
	if err != nil {
		return "", "", err
	}
	codeVerifier, err := randomHex(32)
	if err != nil {
		return "", "", err
	}
	flow := oauthFlow{
		Provider:     provider.ProviderKey,
		Mode:         mode,
		UserID:       userID,
		TokenVersion: user.TokenVersion,
		ReturnPath:   validateOAuthReturnPath(returnPath),
		CodeVerifier: codeVerifier,
		FlowID:       flowID,
		IssuedAt:     s.now().Unix(),
	}
	if err := s.states.save(ctx, oauthStateKey(state), flow, oauthStateTTL); err != nil {
		return "", "", apperrors.Wrap(consts.StatusServiceUnavailable, "第三方登录服务暂不可用", err)
	}
	authorizeURL, err := driver.AuthorizeURL(provider, state, codeVerifier, "")
	if err != nil {
		_, _ = s.states.consume(ctx, oauthStateKey(state), &flow)
		return "", "", apperrors.Wrap(consts.StatusBadRequest, "授权地址生成失败", err)
	}
	return authorizeURL, flowID, nil
}

func (s *OAuthService) Callback(ctx context.Context, providerKey, code, state, providerError, flowID, ip, ua string) (OAuthCallbackResult, error) {
	if err := s.checkRateLimit(ctx, "callback", ip); err != nil {
		return OAuthCallbackResult{}, err
	}
	provider, err := s.enabledProvider(ctx, providerKey)
	if err != nil {
		return OAuthCallbackResult{}, err
	}
	if strings.TrimSpace(state) == "" {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorStateInvalid, "授权状态已失效，请重新登录", nil)
	}
	var flow oauthFlow
	ok, err := s.states.consume(ctx, oauthStateKey(state), &flow)
	if err != nil {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "第三方登录服务暂不可用", err)
	}
	if !ok || flow.Provider != providerKey || flow.FlowID != strings.TrimSpace(flowID) ||
		(flow.Mode != OAuthModeLogin && flow.Mode != OAuthModeBind) {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorStateInvalid, "授权状态已失效，请重新登录", nil)
	}
	if strings.TrimSpace(providerError) != "" {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorProviderDenied, "已取消第三方授权", nil)
	}
	if strings.TrimSpace(code) == "" {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorProviderError, "第三方平台未返回授权码", nil)
	}
	driver, ok := oauthDriverFor(provider.Driver)
	if !ok {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorProviderError, "该登录方式暂不支持", nil)
	}
	token, err := driver.Exchange(ctx, s.httpClient, provider, code, flow.CodeVerifier)
	if err != nil {
		s.log.Warn("oauth token exchange failed",
			zap.String("provider", provider.ProviderKey),
			zap.String("error", masking.Error(err)),
		)
		flowErr := newOAuthFlowError(OAuthErrorProviderError, "第三方平台授权失败，请重试", err)
		flowErr.Detail = sanitizeOAuthDetail(err)
		return OAuthCallbackResult{}, flowErr
	}
	external, err := driver.FetchIdentity(ctx, s.httpClient, provider, token)
	if err != nil {
		s.log.Warn("oauth identity fetch failed",
			zap.String("provider", provider.ProviderKey),
			zap.String("error", masking.Error(err)),
		)
		flowErr := newOAuthFlowError(OAuthErrorProviderError, "第三方平台用户信息获取失败，请重试", err)
		flowErr.Detail = sanitizeOAuthDetail(err)
		return OAuthCallbackResult{}, flowErr
	}
	identity, err := normalizeExternalIdentity(provider, external)
	if err != nil {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorProviderError, "第三方账号信息不完整，请重试", err)
	}
	if external.ActiveSet && !external.Active {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorAccountUnavailable, "该第三方账号当前不可用", nil)
	}
	if flow.Mode == OAuthModeBind {
		return s.bind(ctx, provider, flow, identity, ip, ua)
	}
	return s.loginOrRegister(ctx, provider, flow, identity, ip, ua)
}

func (s *OAuthService) Exchange(ctx context.Context, ticket, flowID, ip, ua string) (OAuthLoginResult, error) {
	if err := s.checkRateLimit(ctx, "exchange", ip); err != nil {
		return OAuthLoginResult{}, err
	}
	if strings.TrimSpace(ticket) == "" || strings.TrimSpace(flowID) == "" {
		return OAuthLoginResult{}, newOAuthFlowError(OAuthErrorFlowInvalid, "登录凭证已失效，请重新登录", nil)
	}
	var login oauthLoginTicket
	ok, err := s.states.consume(ctx, oauthLoginKey(ticket), &login)
	if err != nil {
		return OAuthLoginResult{}, newOAuthFlowError(OAuthErrorInternal, "第三方登录服务暂不可用", err)
	}
	if !ok || login.FlowID != flowID {
		return OAuthLoginResult{}, newOAuthFlowError(OAuthErrorFlowInvalid, "登录凭证已失效，请重新登录", nil)
	}
	if _, err := s.enabledProvider(ctx, login.Provider); err != nil {
		return OAuthLoginResult{}, err
	}
	user, err := s.auth.repo.FindUserByID(ctx, login.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OAuthLoginResult{}, newOAuthFlowError(OAuthErrorAccountUnavailable, "账号不可用", err)
		}
		return OAuthLoginResult{}, newOAuthFlowError(OAuthErrorInternal, "认证服务暂时不可用", err)
	}
	if user.Status != 1 {
		return OAuthLoginResult{}, newOAuthFlowError(OAuthErrorBanned, "该账号当前不可用", nil)
	}
	if user.TokenVersion != login.TokenVersion {
		return OAuthLoginResult{}, newOAuthFlowError(OAuthErrorFlowInvalid, "登录状态已变化，请重新登录", nil)
	}
	pair, err := s.auth.issueTokens(ctx, user, ip, ua)
	if err != nil {
		return OAuthLoginResult{}, newOAuthFlowError(OAuthErrorInternal, "登录服务暂时不可用", err)
	}
	if err := s.auth.repo.UpdateLastLogin(ctx, user.ID); err != nil {
		s.log.Warn("oauth update last login failed", zap.Int64("user_id", user.ID), zap.String("error", masking.Error(err)))
	}
	s.auditLogin(ctx, user, ip, ua)
	return OAuthLoginResult{AccessToken: pair.AccessToken, RefreshToken: pair.RefreshToken, Redirect: validateOAuthReturnPath(login.ReturnPath)}, nil
}

func (s *OAuthService) PendingInfo(ctx context.Context, ticket, flowID, ip string) (OAuthCompleteInfo, error) {
	if err := s.checkRateLimit(ctx, "complete", ip); err != nil {
		return OAuthCompleteInfo{}, err
	}
	var pending oauthPending
	ok, err := s.states.peek(ctx, oauthPendingKey(ticket), &pending)
	if err != nil {
		return OAuthCompleteInfo{}, apperrors.Wrap(consts.StatusServiceUnavailable, "第三方注册服务暂不可用", err)
	}
	if !ok || pending.FlowID != flowID {
		return OAuthCompleteInfo{}, apperrors.New(consts.StatusBadRequest, "注册凭证已失效，请重新授权")
	}
	provider, err := s.repo.ProviderByKey(ctx, pending.Provider)
	if err != nil {
		return OAuthCompleteInfo{}, apperrors.Wrap(consts.StatusBadRequest, "第三方登录配置不可用", err)
	}
	suggested, err := s.suggestUsername(ctx, provider.ProviderKey, pending.Identity)
	if err != nil {
		return OAuthCompleteInfo{}, err
	}
	email := ""
	if pending.Identity.EmailVerified {
		email = pending.Identity.Email
	}
	requiresVerifiedEmail := provider.RequireVerifiedEmail
	if policy, policyErr := s.OAuthRegisterPolicy(ctx, provider); policyErr == nil {
		requiresVerifiedEmail = policy.RequireVerifiedEmail
	}
	return OAuthCompleteInfo{
		Provider:              provider.ProviderKey,
		DisplayName:           provider.DisplayName,
		IconKey:               provider.IconKey,
		Nickname:              pending.Identity.Nickname,
		Avatar:                pending.Identity.Avatar,
		SuggestedUsername:     suggested,
		Email:                 email,
		EmailVerified:         pending.Identity.EmailVerified,
		RequiresVerifiedEmail: requiresVerifiedEmail,
	}, nil
}

func (s *OAuthService) Complete(ctx context.Context, req OAuthCompleteRequest, flowID, ip, ua string) (result OAuthLoginResult, err error) {
	if err := s.checkRateLimit(ctx, "complete", ip); err != nil {
		return OAuthLoginResult{}, err
	}
	if strings.TrimSpace(req.Website) != "" {
		s.auth.auditAuth(ctx, AuditEntry{
			Module: "auth", Action: "register", Summary: "第三方注册被蜜罐拦截（返回假成功）",
			Status: false, ErrorMessage: "honeypot", StatusCode: consts.StatusOK, ClientIP: ip, UserAgent: ua,
		})
		pair := fakeTokenPair()
		return OAuthLoginResult{AccessToken: pair.AccessToken, RefreshToken: pair.RefreshToken}, nil
	}
	var pending oauthPending
	ok, err := s.states.consume(ctx, oauthPendingKey(req.Ticket), &pending)
	if err != nil {
		return OAuthLoginResult{}, apperrors.Wrap(consts.StatusServiceUnavailable, "第三方注册服务暂不可用", err)
	}
	if !ok || pending.FlowID != flowID {
		return OAuthLoginResult{}, apperrors.New(consts.StatusBadRequest, "注册凭证已失效，请重新授权")
	}
	restored := false
	defer func() {
		if err != nil && !restored {
			restoreCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if restoreErr := s.states.restore(restoreCtx, oauthPendingKey(req.Ticket), pending, oauthPendingTTL); restoreErr != nil {
				s.log.Warn("oauth pending ticket restore failed", zap.String("error", masking.Error(restoreErr)))
			}
		}
	}()
	provider, err := s.enabledProvider(ctx, pending.Provider)
	if err != nil {
		return OAuthLoginResult{}, err
	}
	policy, err := s.OAuthRegisterPolicy(ctx, provider)
	if err != nil {
		if errors.Is(err, ErrRegistrationRoleInvalid) {
			return OAuthLoginResult{}, newOAuthFlowError(OAuthErrorRegisterDisabled, "默认注册角色不可用，请联系管理员", err)
		}
		return OAuthLoginResult{}, apperrors.Wrap(consts.StatusServiceUnavailable, "注册配置暂不可用", err)
	}
	if !policy.Enabled || policy.Mode != OAuthRegisterModeConfirm {
		return OAuthLoginResult{}, newOAuthFlowError(OAuthErrorRegisterDisabled, "该登录方式已关闭注册", nil)
	}
	if err := s.checkOAuthRisk(policy, pending.Identity); err != nil {
		return OAuthLoginResult{}, err
	}
	req.Username = strings.TrimSpace(req.Username)
	if !usernamePattern.MatchString(req.Username) {
		return OAuthLoginResult{}, apperrors.New(consts.StatusBadRequest, "用户名只能包含字母、数字、下划线和短横线，长度为3到32位")
	}
	if !passwordpolicy.Valid(req.Password) {
		return OAuthLoginResult{}, apperrors.New(consts.StatusBadRequest, "密码必须为8到64个字符，且不能超过72字节")
	}
	if req.Password != req.ConfirmPassword {
		return OAuthLoginResult{}, apperrors.New(consts.StatusBadRequest, "两次输入的密码不一致")
	}
	email := ""
	emailVerified := false
	if pending.Identity.EmailVerified && strings.TrimSpace(pending.Identity.Email) != "" {
		email = strings.ToLower(strings.TrimSpace(pending.Identity.Email))
		emailVerified = true
	}
	if strings.TrimSpace(req.Email) != "" && !strings.EqualFold(strings.TrimSpace(req.Email), email) {
		return OAuthLoginResult{}, apperrors.New(consts.StatusBadRequest, "邮箱必须使用第三方平台已验证的邮箱")
	}
	if policy.RequireVerifiedEmail && !emailVerified {
		return OAuthLoginResult{}, newOAuthFlowError(OAuthErrorEmailRequired, "该登录方式要求已验证邮箱，请绑定邮箱后重试", nil)
	}
	if emailVerified {
		if err := s.auth.checkRegisterEmailDomain(ctx, email); err != nil {
			return OAuthLoginResult{}, err
		}
		if existing, found, lookupErr := s.repo.UserIDByEmail(ctx, email); lookupErr != nil {
			return OAuthLoginResult{}, apperrors.Wrap(consts.StatusServiceUnavailable, "注册服务暂不可用", lookupErr)
		} else if found {
			_ = existing
			return OAuthLoginResult{}, newOAuthFlowError(OAuthErrorEmailConflict, "该邮箱已注册，请先登录原账号后再绑定第三方账号", nil)
		}
	}
	if err := s.checkOAuthRegisterIPLimits(ctx, provider, policy, ip); err != nil {
		return OAuthLoginResult{}, err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return OAuthLoginResult{}, err
	}
	displayName := truncateRunes(strings.TrimSpace(pending.Identity.Nickname), 64)
	if displayName == "" {
		displayName = req.Username
	}
	avatar := safeUserAvatar(pending.Identity.Avatar)
	identity := pending.Identity
	identity.ProviderKey = provider.ProviderKey
	identity.ProviderGroup = provider.ProviderGroup
	identity.Email = email
	identity.EmailVerified = emailVerified
	identity.Nickname = truncateRunes(identity.Nickname, 128)
	userID, err := s.repo.CreateExternalUser(ctx, OAuthExternalUser{
		Username:      req.Username,
		DisplayName:   displayName,
		Avatar:        avatar,
		Email:         email,
		EmailVerified: emailVerified,
		PasswordHash:  string(hash),
		PasswordSet:   true,
		GiftPoints:    policy.GiftPoints,
		RoleID:        policy.DefaultRoleID,
		IPHash:        iphash.RiskDigest(ip),
		IPChannel:     "oauth:" + provider.ProviderKey,
		IPMaxAccounts: policy.IPMaxAccounts,
		Identity:      identity,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrOAuthUsernameTaken):
			return OAuthLoginResult{}, apperrors.New(consts.StatusConflict, "用户名已被使用")
		case errors.Is(err, ErrOAuthEmailConflict):
			return OAuthLoginResult{}, newOAuthFlowError(OAuthErrorEmailConflict, "该邮箱已注册，请先登录原账号后再绑定第三方账号", nil)
		case errors.Is(err, ErrOAuthIdentityExists), errors.Is(err, ErrOAuthAlreadyBound):
			return OAuthLoginResult{}, newOAuthFlowError(OAuthErrorAlreadyRegistered, "该第三方账号已完成注册，请直接登录", nil)
		case errors.Is(err, ErrRegisterIPLimit):
			return OAuthLoginResult{}, newOAuthFlowError(OAuthErrorRegisterLimited, "该 IP 的第三方注册数量已达上限，请稍后再试", nil)
		default:
			return OAuthLoginResult{}, apperrors.Wrap(consts.StatusServiceUnavailable, "注册服务暂不可用", err)
		}
	}
	user, err := s.auth.repo.FindUserByID(ctx, userID)
	if err != nil {
		return OAuthLoginResult{}, apperrors.Wrap(consts.StatusServiceUnavailable, "注册服务暂不可用", err)
	}
	pair, err := s.auth.issueTokens(ctx, user, ip, ua)
	if err != nil {
		return OAuthLoginResult{}, apperrors.Wrap(consts.StatusServiceUnavailable, "登录服务暂时不可用", err)
	}
	if err := s.auth.repo.UpdateLastLogin(ctx, user.ID); err != nil {
		s.log.Warn("oauth update last login failed", zap.Int64("user_id", user.ID), zap.String("error", masking.Error(err)))
	}
	s.auth.auditAuth(ctx, AuditEntry{
		UserID: user.ID, Username: user.Username, Roles: append([]string(nil), user.Roles...),
		Module: "auth", Action: "register", Summary: "第三方注册成功",
		Status: true, StatusCode: consts.StatusOK, ClientIP: ip, UserAgent: ua,
	})
	s.auditLogin(ctx, user, ip, ua)
	return OAuthLoginResult{AccessToken: pair.AccessToken, RefreshToken: pair.RefreshToken, Redirect: validateOAuthReturnPath(pending.ReturnPath)}, nil
}

func (s *OAuthService) Bindings(ctx context.Context, userID int64) (OAuthBindings, error) {
	if userID <= 0 {
		return OAuthBindings{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
	}
	providers, err := s.repo.Providers(ctx, false)
	if err != nil {
		return OAuthBindings{}, apperrors.Wrap(consts.StatusServiceUnavailable, "第三方登录配置暂不可用", err)
	}
	identities, err := s.repo.IdentitiesByUser(ctx, userID)
	if err != nil {
		return OAuthBindings{}, apperrors.Wrap(consts.StatusServiceUnavailable, "第三方绑定信息暂不可用", err)
	}
	hasPassword, err := s.repo.PasswordSet(ctx, userID)
	if err != nil {
		return OAuthBindings{}, apperrors.Wrap(consts.StatusServiceUnavailable, "账号信息暂不可用", err)
	}
	bound := make(map[string]OAuthIdentity, len(identities))
	for _, identity := range identities {
		bound[identity.ProviderKey] = identity
	}
	result := OAuthBindings{HasPassword: hasPassword, Providers: make([]OAuthBindingProvider, 0, len(providers))}
	for _, provider := range providers {
		item := OAuthBindingProvider{
			ProviderKey: provider.ProviderKey,
			DisplayName: provider.DisplayName,
			IconKey:     provider.IconKey,
			Enabled:     provider.Enabled,
		}
		if identity, ok := bound[provider.ProviderKey]; ok {
			item.Bound = true
			item.Nickname = identity.Nickname
			item.Avatar = identity.Avatar
			item.BoundAt = identity.BoundAt
			item.LastLoginAt = identity.LastLoginAt
		}
		result.Providers = append(result.Providers, item)
	}
	return result, nil
}

func (s *OAuthService) Unbind(ctx context.Context, userID int64, providerKey string, req OAuthUnbindRequest, ip, ua string) error {
	user, err := s.auth.repo.FindUserByID(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
		}
		return apperrors.Wrap(consts.StatusServiceUnavailable, "认证服务暂时不可用", err)
	}
	if err := s.auth.checkAccountActive(user); err != nil {
		return err
	}
	if _, err := s.repo.IdentityByUserProvider(ctx, userID, providerKey); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apperrors.New(consts.StatusBadRequest, "该第三方账号尚未绑定")
		}
		return apperrors.Wrap(consts.StatusServiceUnavailable, "第三方绑定信息暂不可用", err)
	}
	hasPassword, err := s.repo.PasswordSet(ctx, userID)
	if err != nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "账号信息暂不可用", err)
	}
	if hasPassword {
		if strings.TrimSpace(req.Password) == "" || bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)) != nil {
			return apperrors.New(consts.StatusBadRequest, "当前密码不正确")
		}
	} else if user.Email != nil && strings.TrimSpace(*user.Email) != "" {
		if strings.TrimSpace(req.EmailCode) == "" {
			return apperrors.New(consts.StatusBadRequest, "请输入邮箱验证码")
		}
		if err := s.auth.code.VerifyBindEmail(ctx, *user.Email, req.EmailCode); err != nil {
			return err
		}
	} else {
		return apperrors.New(consts.StatusBadRequest, "请先设置密码或绑定邮箱后再解绑第三方账号")
	}
	count, err := s.repo.CountIdentitiesByUser(ctx, userID)
	if err != nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "第三方绑定信息暂不可用", err)
	}
	if !hasPassword && count <= 1 {
		return apperrors.New(consts.StatusBadRequest, "至少需要保留一种登录方式")
	}
	removed, err := s.repo.UnbindIdentity(ctx, userID, providerKey)
	if err != nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "解绑失败，请稍后重试", err)
	}
	if !removed {
		return apperrors.New(consts.StatusBadRequest, "该第三方账号尚未绑定")
	}
	s.log.Info("oauth identity unbound",
		zap.Int64("user_id", userID),
		zap.String("provider", providerKey),
		zap.String("client_ip", masking.IP(ip)),
	)
	return nil
}

func (s *OAuthService) AdminList(ctx context.Context) (OAuthAdminConfig, error) {
	global, err := s.repo.GlobalConfig(ctx)
	if err != nil {
		return OAuthAdminConfig{}, apperrors.Wrap(consts.StatusServiceUnavailable, "第三方登录配置暂不可用", err)
	}
	providers, err := s.repo.Providers(ctx, false)
	if err != nil {
		return OAuthAdminConfig{}, apperrors.Wrap(consts.StatusServiceUnavailable, "第三方登录配置暂不可用", err)
	}
	result := OAuthAdminConfig{
		Global: OAuthGlobalConfigView{
			FrontendBaseURL: global.FrontendBaseURL,
		},
		Providers: make([]OAuthProviderView, 0, len(providers)),
	}
	for _, provider := range providers {
		result.Providers = append(result.Providers, oauthProviderView(provider, s.systemPrefix))
	}
	return result, nil
}

func (s *OAuthService) AdminSaveGlobal(ctx context.Context, req OAuthGlobalConfigSaveRequest) (OAuthGlobalConfigView, error) {
	cfg, err := validateOAuthGlobalConfig(OAuthGlobalConfig{
		FrontendBaseURL: req.FrontendBaseURL,
	}, s.appEnv)
	if err != nil {
		return OAuthGlobalConfigView{}, apperrors.New(consts.StatusBadRequest, err.Error())
	}
	if cfg.FrontendBaseURL == "" {
		providers, err := s.repo.Providers(ctx, true)
		if err != nil {
			return OAuthGlobalConfigView{}, apperrors.Wrap(consts.StatusServiceUnavailable, "第三方登录配置暂不可用", err)
		}
		if len(providers) > 0 {
			return OAuthGlobalConfigView{}, apperrors.New(consts.StatusBadRequest, "仍有已启用的第三方登录方式，不能清空前端基础地址")
		}
	}
	if err := s.repo.SaveGlobalConfig(ctx, cfg); err != nil {
		return OAuthGlobalConfigView{}, apperrors.Wrap(consts.StatusServiceUnavailable, "保存第三方登录配置失败", err)
	}
	return OAuthGlobalConfigView{FrontendBaseURL: cfg.FrontendBaseURL}, nil
}

func (s *OAuthService) AdminCreateProvider(ctx context.Context, req OAuthProviderSaveRequest) (OAuthProviderView, error) {
	key := strings.TrimSpace(req.ProviderKey)
	if _, err := s.repo.ProviderByKey(ctx, key); err == nil {
		return OAuthProviderView{}, apperrors.New(consts.StatusConflict, "Provider 已存在")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return OAuthProviderView{}, apperrors.Wrap(consts.StatusServiceUnavailable, "第三方登录配置暂不可用", err)
	}
	cfg := oauthProviderConfigFromRequest(req)
	cfg.ProviderKey = key
	if err := s.validateProviderForSave(ctx, &cfg, false); err != nil {
		return OAuthProviderView{}, err
	}
	if err := s.repo.CreateProvider(ctx, cfg); err != nil {
		return OAuthProviderView{}, apperrors.Wrap(consts.StatusServiceUnavailable, "保存第三方登录配置失败", err)
	}
	saved, err := s.repo.ProviderByKey(ctx, cfg.ProviderKey)
	if err != nil {
		return OAuthProviderView{}, apperrors.Wrap(consts.StatusServiceUnavailable, "读取第三方登录配置失败", err)
	}
	return oauthProviderView(saved, s.systemPrefix), nil
}

func (s *OAuthService) AdminUpdateProvider(ctx context.Context, providerKey string, req OAuthProviderSaveRequest) (OAuthProviderView, error) {
	current, err := s.repo.ProviderByKey(ctx, providerKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OAuthProviderView{}, apperrors.New(consts.StatusNotFound, "Provider 不存在")
		}
		return OAuthProviderView{}, apperrors.Wrap(consts.StatusServiceUnavailable, "第三方登录配置暂不可用", err)
	}
	cfg := oauthProviderConfigFromRequest(req)
	cfg.ProviderKey = current.ProviderKey
	if req.ClearClientSecret {
		cfg.ClientSecret = ""
	} else if strings.TrimSpace(req.ClientSecret) == "" {
		cfg.ClientSecret = current.ClientSecret
	}
	if err := s.validateProviderForSave(ctx, &cfg, strings.TrimSpace(cfg.ClientSecret) != ""); err != nil {
		return OAuthProviderView{}, err
	}
	if err := s.repo.UpdateProvider(ctx, cfg); err != nil {
		return OAuthProviderView{}, apperrors.Wrap(consts.StatusServiceUnavailable, "保存第三方登录配置失败", err)
	}
	saved, err := s.repo.ProviderByKey(ctx, cfg.ProviderKey)
	if err != nil {
		return OAuthProviderView{}, apperrors.Wrap(consts.StatusServiceUnavailable, "读取第三方登录配置失败", err)
	}
	return oauthProviderView(saved, s.systemPrefix), nil
}

func (s *OAuthService) AdminTestProvider(ctx context.Context, providerKey string) (OAuthProviderTestResult, error) {
	provider, err := s.repo.ProviderByKey(ctx, providerKey)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OAuthProviderTestResult{}, apperrors.New(consts.StatusNotFound, "Provider 不存在")
		}
		return OAuthProviderTestResult{}, apperrors.Wrap(consts.StatusServiceUnavailable, "第三方登录配置暂不可用", err)
	}
	driver, ok := oauthDriverFor(provider.Driver)
	if !ok {
		return OAuthProviderTestResult{}, apperrors.New(consts.StatusBadRequest, "该登录方式暂不支持")
	}
	state, err := randomHex(16)
	if err != nil {
		return OAuthProviderTestResult{}, err
	}
	verifier, err := randomHex(32)
	if err != nil {
		return OAuthProviderTestResult{}, err
	}
	authorizeURL, err := driver.AuthorizeURL(provider, state, verifier, "")
	if err != nil {
		return OAuthProviderTestResult{}, apperrors.Wrap(consts.StatusBadRequest, "授权地址生成失败", err)
	}
	return OAuthProviderTestResult{AuthorizeURL: authorizeURL}, nil
}

func (s *OAuthService) validateProviderForSave(ctx context.Context, cfg *OAuthProviderConfig, hasClientSecret bool) error {
	if err := validateOAuthProviderConfig(cfg, s.appEnv, hasClientSecret); err != nil {
		return apperrors.New(consts.StatusBadRequest, err.Error())
	}
	if cfg.DefaultRoleID > 0 {
		ok, err := s.auth.repo.RegistrationRoleValid(ctx, cfg.DefaultRoleID)
		if err != nil {
			return apperrors.Wrap(consts.StatusServiceUnavailable, "注册角色校验暂不可用", err)
		}
		if !ok {
			return apperrors.New(consts.StatusBadRequest, "默认注册角色必须是已启用且允许自助注册的非管理员角色")
		}
	}
	if cfg.Enabled {
		global, err := s.repo.GlobalConfig(ctx)
		if err != nil {
			return apperrors.Wrap(consts.StatusServiceUnavailable, "第三方登录配置暂不可用", err)
		}
		if strings.TrimSpace(global.FrontendBaseURL) == "" {
			return apperrors.New(consts.StatusBadRequest, "启用前必须先在全局配置中填写前端基础地址")
		}
	}
	return nil
}

func oauthProviderConfigFromRequest(req OAuthProviderSaveRequest) OAuthProviderConfig {
	return OAuthProviderConfig{
		ProviderKey:              strings.TrimSpace(req.ProviderKey),
		DisplayName:              strings.TrimSpace(req.DisplayName),
		IconKey:                  strings.TrimSpace(req.IconKey),
		Driver:                   strings.TrimSpace(req.Driver),
		ProviderGroup:            strings.TrimSpace(req.ProviderGroup),
		Enabled:                  req.Enabled,
		RegisterMode:             strings.TrimSpace(req.RegisterMode),
		DefaultRoleID:            req.DefaultRoleID,
		RegisterGiftPoints:       req.RegisterGiftPoints,
		RegisterIPHourLimit:      req.RegisterIPHourLimit,
		RegisterIPDayLimit:       req.RegisterIPDayLimit,
		RegisterIPMaxAccounts:    req.RegisterIPMaxAccounts,
		LinkByEmail:              req.LinkByEmail,
		RequireVerifiedEmail:     req.RequireVerifiedEmailMode == 1,
		RequireVerifiedEmailMode: req.RequireVerifiedEmailMode,
		MinAccountAgeDays:        req.MinAccountAgeDays,
		MinTrustLevel:            req.MinTrustLevel,
		RequireActive:            req.RequireActive,
		RejectSilenced:           req.RejectSilenced,
		ClientID:                 strings.TrimSpace(req.ClientID),
		ClientSecret:             strings.TrimSpace(req.ClientSecret),
		Issuer:                   strings.TrimSpace(req.Issuer),
		AuthorizationEndpoint:    strings.TrimSpace(req.AuthorizationEndpoint),
		TokenEndpoint:            strings.TrimSpace(req.TokenEndpoint),
		UserinfoEndpoint:         strings.TrimSpace(req.UserinfoEndpoint),
		JWKSURI:                  strings.TrimSpace(req.JWKSURI),
		Scopes:                   strings.TrimSpace(req.Scopes),
		RedirectURI:              strings.TrimSpace(req.RedirectURI),
		ExtraConfig:              strings.TrimSpace(req.ExtraConfig),
		SortNo:                   req.SortNo,
	}
}

func (s *OAuthService) enabledProvider(ctx context.Context, providerKey string) (OAuthProviderConfig, error) {
	provider, err := s.repo.ProviderByKey(ctx, strings.TrimSpace(providerKey))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OAuthProviderConfig{}, apperrors.New(consts.StatusBadRequest, "该登录方式未启用")
		}
		return OAuthProviderConfig{}, apperrors.Wrap(consts.StatusServiceUnavailable, "第三方登录配置暂不可用", err)
	}
	if !provider.Enabled {
		return OAuthProviderConfig{}, newOAuthFlowError(OAuthErrorProviderDisabled, "该登录方式未启用", nil)
	}
	return provider, nil
}

func (s *OAuthService) loginOrRegister(ctx context.Context, provider OAuthProviderConfig, flow oauthFlow, identity OAuthIdentity, ip, ua string) (OAuthCallbackResult, error) {
	found, lookupErr := s.repo.IdentityBySubject(ctx, provider.ProviderKey, identity.Subject)
	if errors.Is(lookupErr, sql.ErrNoRows) && strings.TrimSpace(identity.UnionID) != "" {
		found, lookupErr = s.repo.IdentityByUnion(ctx, provider.ProviderGroup, identity.UnionID)
	}
	if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "第三方登录服务暂不可用", lookupErr)
	}
	if lookupErr == nil {
		user, err := s.auth.repo.FindUserByID(ctx, found.UserID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorAccountUnavailable, "账号不可用", err)
			}
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "认证服务暂时不可用", err)
		}
		if user.Status != 1 {
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorBanned, "该账号当前不可用", nil)
		}
		found.Nickname = identity.Nickname
		found.Avatar = identity.Avatar
		if identity.Email != "" && identity.EmailVerified {
			found.Email = identity.Email
			found.EmailVerified = true
		}
		if err := s.repo.UpdateIdentityProfile(ctx, found); err != nil {
			s.log.Warn("oauth identity profile update failed", zap.String("error", masking.Error(err)))
		}
		ticket, err := randomHex(32)
		if err != nil {
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "登录凭证生成失败", err)
		}
		if err := s.states.save(ctx, oauthLoginKey(ticket), oauthLoginTicket{
			UserID:       user.ID,
			TokenVersion: user.TokenVersion,
			Provider:     provider.ProviderKey,
			ReturnPath:   flow.ReturnPath,
			FlowID:       flow.FlowID,
			IssuedAt:     s.now().Unix(),
		}, oauthTicketTTL); err != nil {
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "登录凭证生成失败", err)
		}
		return OAuthCallbackResult{
			RedirectPath: "/oauth/callback?ticket=" + url.QueryEscape(ticket),
		}, nil
	}
	policy, err := s.OAuthRegisterPolicy(ctx, provider)
	if err != nil {
		if errors.Is(err, ErrRegistrationRoleInvalid) {
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorRegisterDisabled, "默认注册角色不可用，请联系管理员", err)
		}
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "注册配置暂不可用", err)
	}
	if !policy.Enabled {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorRegisterDisabled, "管理员未开放第三方注册", nil)
	}
	if err := s.checkOAuthRisk(policy, identity); err != nil {
		return OAuthCallbackResult{}, err
	}
	if policy.RequireVerifiedEmail && !identity.EmailVerified {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorEmailRequired, "该登录方式要求已验证邮箱，请绑定邮箱后重试", nil)
	}
	if identity.Email != "" && identity.EmailVerified {
		if _, found, err := s.repo.UserIDByEmail(ctx, identity.Email); err != nil {
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "注册服务暂不可用", err)
		} else if found {
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorEmailConflict, "该邮箱已注册，请先登录原账号后再绑定第三方账号", nil)
		}
	}
	switch policy.Mode {
	case OAuthRegisterModeOff:
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorNotRegistered, "该第三方账号尚未注册，请先登录站内账号后绑定", nil)
	case OAuthRegisterModeConfirm:
		ticket, err := randomHex(32)
		if err != nil {
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "注册凭证生成失败", err)
		}
		pending := oauthPending{
			Provider:   provider.ProviderKey,
			FlowID:     flow.FlowID,
			ReturnPath: flow.ReturnPath,
			Identity:   identity,
			IssuedAt:   s.now().Unix(),
		}
		if err := s.states.save(ctx, oauthPendingKey(ticket), pending, oauthPendingTTL); err != nil {
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "注册凭证生成失败", err)
		}
		return OAuthCallbackResult{
			RedirectPath: "/oauth/complete?ticket=" + url.QueryEscape(ticket),
		}, nil
	case OAuthRegisterModeAuto:
		return s.autoRegister(ctx, provider, flow, identity, policy, ip, ua)
	default:
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorRegisterDisabled, "该登录方式暂不支持注册", nil)
	}
}

func (s *OAuthService) bind(ctx context.Context, provider OAuthProviderConfig, flow oauthFlow, identity OAuthIdentity, ip, ua string) (OAuthCallbackResult, error) {
	user, err := s.auth.repo.FindUserByID(ctx, flow.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorAccountUnavailable, "账号不可用", err)
		}
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "认证服务暂时不可用", err)
	}
	if user.Status != 1 {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorBanned, "该账号当前不可用", nil)
	}
	if user.TokenVersion != flow.TokenVersion {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorFlowInvalid, "登录状态已变化，请重新绑定", nil)
	}
	if existing, err := s.repo.IdentityBySubject(ctx, provider.ProviderKey, identity.Subject); err == nil {
		if existing.UserID != user.ID {
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorIdentityConflict, "该第三方账号已绑定其他站内账号", nil)
		}
		existing.Nickname = identity.Nickname
		existing.Avatar = identity.Avatar
		_ = s.repo.UpdateIdentityProfile(ctx, existing)
		return OAuthCallbackResult{RedirectPath: "/profile?oauth=bound&provider=" + url.QueryEscape(provider.ProviderKey), ClearFlow: true}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "第三方绑定信息暂不可用", err)
	}
	if current, err := s.repo.IdentityByUserProvider(ctx, user.ID, provider.ProviderKey); err == nil {
		if current.Subject != identity.Subject {
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorIdentityConflict, "当前账号已绑定其他第三方身份，请先解绑", nil)
		}
		return OAuthCallbackResult{RedirectPath: "/profile?oauth=bound&provider=" + url.QueryEscape(provider.ProviderKey), ClearFlow: true}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "第三方绑定信息暂不可用", err)
	}
	identity.UserID = user.ID
	identity.ProviderKey = provider.ProviderKey
	identity.ProviderGroup = provider.ProviderGroup
	if _, err := s.repo.BindIdentity(ctx, identity); err != nil {
		switch {
		case errors.Is(err, ErrOAuthIdentityExists):
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorIdentityConflict, "该第三方账号已绑定其他站内账号", nil)
		case errors.Is(err, ErrOAuthAlreadyBound):
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorIdentityConflict, "当前账号已绑定其他第三方身份，请先解绑", nil)
		default:
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "绑定失败，请稍后重试", err)
		}
	}
	s.log.Info("oauth identity bound",
		zap.Int64("user_id", user.ID),
		zap.String("provider", provider.ProviderKey),
		zap.String("client_ip", masking.IP(ip)),
	)
	return OAuthCallbackResult{RedirectPath: "/profile?oauth=bound&provider=" + url.QueryEscape(provider.ProviderKey), ClearFlow: true}, nil
}

func (s *OAuthService) suggestUsername(ctx context.Context, providerKey string, identity OAuthIdentity) (string, error) {
	base := oauthSuggestedUsername(providerKey, identity.Username)
	if base == "" {
		base = oauthSuggestedUsername(providerKey, identity.Nickname)
	}
	candidate := base
	for attempt := 0; attempt < 3; attempt++ {
		exists, err := s.repo.UsernameExists(ctx, candidate)
		if err != nil {
			return "", apperrors.Wrap(consts.StatusServiceUnavailable, "注册服务暂不可用", err)
		}
		if !exists {
			return candidate, nil
		}
		suffix, err := randomHex(3)
		if err != nil {
			return "", err
		}
		candidate = base + "_" + suffix
		if len(candidate) > 32 {
			candidate = candidate[:32]
		}
	}
	return candidate, nil
}

func (s *OAuthService) autoRegister(
	ctx context.Context,
	provider OAuthProviderConfig,
	flow oauthFlow,
	identity OAuthIdentity,
	policy RegisterPolicy,
	ip, ua string,
) (OAuthCallbackResult, error) {
	if err := s.checkOAuthRegisterIPLimits(ctx, provider, policy, ip); err != nil {
		return OAuthCallbackResult{}, err
	}
	username, err := s.autoOAuthUsername(ctx, provider.ProviderKey, identity)
	if err != nil {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "自动注册用户名生成失败", err)
	}
	password, err := randomHex(32)
	if err != nil {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "自动注册失败", err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "自动注册失败", err)
	}
	displayName := truncateRunes(strings.TrimSpace(identity.Nickname), 64)
	if displayName == "" {
		displayName = username
	}
	email := ""
	emailVerified := false
	if identity.EmailVerified && strings.TrimSpace(identity.Email) != "" {
		email = strings.ToLower(strings.TrimSpace(identity.Email))
		emailVerified = true
	}
	if emailVerified {
		if err := s.auth.checkRegisterEmailDomain(ctx, email); err != nil {
			return OAuthCallbackResult{}, err
		}
	}
	userID, err := s.repo.CreateExternalUser(ctx, OAuthExternalUser{
		Username:      username,
		DisplayName:   displayName,
		Avatar:        safeUserAvatar(identity.Avatar),
		Email:         email,
		EmailVerified: emailVerified,
		PasswordHash:  string(hash),
		PasswordSet:   false,
		GiftPoints:    policy.GiftPoints,
		RoleID:        policy.DefaultRoleID,
		IPHash:        iphash.RiskDigest(ip),
		IPChannel:     "oauth:" + provider.ProviderKey,
		IPMaxAccounts: policy.IPMaxAccounts,
		Identity:      identity,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrOAuthUsernameTaken):
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "自动注册用户名冲突，请重试", err)
		case errors.Is(err, ErrOAuthEmailConflict):
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorEmailConflict, "该邮箱已注册，请先登录原账号后再绑定第三方账号", nil)
		case errors.Is(err, ErrOAuthIdentityExists):
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorAlreadyRegistered, "该第三方账号已完成注册，请直接登录", nil)
		case errors.Is(err, ErrRegisterIPLimit):
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorRegisterLimited, "该 IP 的第三方注册数量已达上限，请稍后再试", nil)
		default:
			return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "自动注册失败，请稍后重试", err)
		}
	}
	user, err := s.auth.repo.FindUserByID(ctx, userID)
	if err != nil {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "自动注册后读取账号失败", err)
	}
	ticket, err := randomHex(32)
	if err != nil {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "登录凭证生成失败", err)
	}
	// Automatic registration must land on the password setup tab once; the
	// original return path is intentionally not used here.
	returnPath := "/profile?oauth=auto"
	if err := s.states.save(ctx, oauthLoginKey(ticket), oauthLoginTicket{
		UserID:       user.ID,
		TokenVersion: user.TokenVersion,
		Provider:     provider.ProviderKey,
		ReturnPath:   returnPath,
		FlowID:       flow.FlowID,
		IssuedAt:     s.now().Unix(),
	}, oauthTicketTTL); err != nil {
		return OAuthCallbackResult{}, newOAuthFlowError(OAuthErrorInternal, "登录凭证生成失败", err)
	}
	s.auth.auditAuth(ctx, AuditEntry{
		UserID: user.ID, Username: user.Username, Roles: append([]string(nil), user.Roles...),
		Module: "auth", Action: "register", Summary: "第三方自动注册成功",
		Status: true, StatusCode: consts.StatusOK, ClientIP: ip, UserAgent: ua,
	})
	return OAuthCallbackResult{
		RedirectPath: "/oauth/callback?ticket=" + url.QueryEscape(ticket),
	}, nil
}

func (s *OAuthService) autoOAuthUsername(ctx context.Context, providerKey string, identity OAuthIdentity) (string, error) {
	base := oauthSuggestedUsername(providerKey, identity.Username)
	if base == "" {
		base = oauthSuggestedUsername(providerKey, identity.Nickname)
	}
	candidate := base
	for attempt := 0; attempt < 8; attempt++ {
		exists, err := s.repo.UsernameExists(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
		suffix, err := randomHex(3)
		if err != nil {
			return "", err
		}
		candidate = base + "_" + suffix
		if len(candidate) > 32 {
			candidate = candidate[:32]
		}
	}
	return "", errors.New("unable to generate a unique username")
}

func (s *OAuthService) auditLogin(ctx context.Context, user User, ip, ua string) {
	s.auth.auditAuth(ctx, AuditEntry{
		UserID: user.ID, Username: user.Username, Roles: append([]string(nil), user.Roles...),
		Module: "auth", Action: "login", Summary: "快捷登录成功",
		Status: true, StatusCode: consts.StatusOK, ClientIP: ip, UserAgent: ua,
	})
}

func (s *OAuthService) checkOAuthRegisterIPLimits(ctx context.Context, provider OAuthProviderConfig, policy RegisterPolicy, ip string) error {
	if s.auth == nil || s.auth.limiter == nil {
		return nil
	}
	err := s.auth.limiter.CheckRegisterChannel(
		ctx,
		"oauth:"+provider.ProviderKey,
		ip,
		policy.IPHourLimit,
		policy.IPDayLimit,
	)
	if err == nil {
		return nil
	}
	if business, ok := apperrors.AsBusinessError(err); ok && business.StatusCode == consts.StatusTooManyRequests {
		return newOAuthFlowError(OAuthErrorRegisterLimited, "第三方注册请求过于频繁，请稍后再试", err)
	}
	return newOAuthFlowError(OAuthErrorInternal, "第三方注册服务暂不可用", err)
}

func (s *OAuthService) checkRateLimit(ctx context.Context, action, ip string) error {
	if s.auth == nil || s.auth.limiter == nil {
		return nil
	}
	switch action {
	case "authorize":
		return s.auth.limiter.CheckOAuthAuthorize(ctx, ip)
	case "callback":
		return s.auth.limiter.CheckOAuthCallback(ctx, ip)
	case "exchange":
		return s.auth.limiter.CheckOAuthExchange(ctx, ip)
	case "complete":
		return s.auth.limiter.CheckOAuthComplete(ctx, ip)
	default:
		return nil
	}
}

func normalizeExternalIdentity(provider OAuthProviderConfig, external ExternalIdentity) (OAuthIdentity, error) {
	subject := strings.TrimSpace(external.Subject)
	if subject == "" {
		return OAuthIdentity{}, errors.New("third-party subject is empty")
	}
	if len(subject) > 255 {
		return OAuthIdentity{}, errors.New("third-party subject is too long")
	}
	unionID := strings.TrimSpace(external.UnionID)
	if len(unionID) > 255 {
		unionID = unionID[:255]
	}
	email := strings.ToLower(strings.TrimSpace(external.Email))
	if email != "" && (!external.EmailVerified || !emailaddr.Valid(email)) {
		email = ""
	}
	return OAuthIdentity{
		ProviderKey:      provider.ProviderKey,
		ProviderGroup:    provider.ProviderGroup,
		Subject:          subject,
		UnionID:          unionID,
		Username:         strings.TrimSpace(external.Username),
		Nickname:         truncateRunes(strings.TrimSpace(external.Nickname), 128),
		Avatar:           oauthNormalizeAvatar(external.Avatar, provider.Issuer, 96),
		Email:            email,
		EmailVerified:    email != "",
		TrustLevel:       external.TrustLevel,
		Active:           external.Active,
		ActiveSet:        external.ActiveSet,
		Silenced:         external.Silenced,
		AccountCreatedAt: external.AccountCreatedAt,
	}, nil
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func safeUserAvatar(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" || len(value) > 512 {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Scheme != "https" {
		return ""
	}
	return value
}
