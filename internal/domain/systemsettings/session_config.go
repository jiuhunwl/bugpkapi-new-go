package systemsettings

import (
	"context"
	"fmt"

	"bugpk-api/internal/domain/rbac"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// SessionConfig 后台可配的登录态有效期。
// AccessTokenMinutes：access token 分钟数；RefreshTokenDays：refresh token 天数。
// 保存后经 observer 推给 auth.JWTService，只影响新签发令牌。
type SessionConfig struct {
	AccessTokenMinutes int `json:"accessTokenMinutes"`
	RefreshTokenDays   int `json:"refreshTokenDays"`
}

const (
	// sessionTTLBounds 上下界：access 最短 5 分钟（防误配把用户立刻全踢下线）、
	// 最长 24 小时；refresh 最短 1 天、最长 365 天。
	sessionAccessMinMinutes = 5
	sessionAccessMaxMinutes = 24 * 60
	sessionRefreshMinDays   = 1
	sessionRefreshMaxDays   = 365
)

// SessionConfigObserver 在登录态配置变化后收到新值（保存成功与启动装载各一次）。
type SessionConfigObserver func(SessionConfig)

// SetSessionConfigObserver 注入观察者（router 装配期调用一次），nil 安全。
func (s *Service) SetSessionConfigObserver(fn SessionConfigObserver) {
	s.sessionConfigMu.Lock()
	s.sessionConfigObserver = fn
	s.sessionConfigMu.Unlock()
}

func (s *Service) notifySessionConfig(cfg SessionConfig) {
	s.sessionConfigMu.RLock()
	fn := s.sessionConfigObserver
	s.sessionConfigMu.RUnlock()
	if fn != nil {
		fn(cfg)
	}
}

// LoadSessionConfig 读取登录态配置并推给观察者（启动装配用，不走权限门）。
func (s *Service) LoadSessionConfig(ctx context.Context) error {
	cfg, err := s.repo.SessionConfig(ctx)
	if err != nil {
		return err
	}
	s.notifySessionConfig(cfg)
	return nil
}

// SessionConfig 管理端读取（带权限门）。
func (s *Service) SessionConfig(ctx context.Context, p rbac.Principal) (SessionConfig, error) {
	if err := s.authz.Require(ctx, p, "system:session-config:view"); err != nil {
		return SessionConfig{}, err
	}
	return s.repo.SessionConfig(ctx)
}

// SaveSessionConfig 管理端保存：校验合法区间后落库，成功后即时通知 JWT 服务。
func (s *Service) SaveSessionConfig(ctx context.Context, p rbac.Principal, req SessionConfig) (SessionConfig, error) {
	if err := s.authz.Require(ctx, p, "system:session-config:update"); err != nil {
		return SessionConfig{}, err
	}
	cfg, err := normalizeSessionConfig(req)
	if err != nil {
		return SessionConfig{}, err
	}
	if err := s.repo.SaveSessionConfig(ctx, cfg); err != nil {
		return SessionConfig{}, err
	}
	s.notifySessionConfig(cfg)
	return cfg, nil
}

func normalizeSessionConfig(req SessionConfig) (SessionConfig, error) {
	cfg := SessionConfig{
		AccessTokenMinutes: req.AccessTokenMinutes,
		RefreshTokenDays:   req.RefreshTokenDays,
	}
	if cfg.AccessTokenMinutes < sessionAccessMinMinutes || cfg.AccessTokenMinutes > sessionAccessMaxMinutes {
		return SessionConfig{}, apperrors.New(consts.StatusBadRequest,
			fmt.Sprintf("access token 有效期须在 %d 分钟～%d 小时之间", sessionAccessMinMinutes, sessionAccessMaxMinutes/60))
	}
	if cfg.RefreshTokenDays < sessionRefreshMinDays || cfg.RefreshTokenDays > sessionRefreshMaxDays {
		return SessionConfig{}, apperrors.New(consts.StatusBadRequest,
			fmt.Sprintf("refresh token 有效期须在 %d～%d 天之间", sessionRefreshMinDays, sessionRefreshMaxDays))
	}
	// refresh 必须长于 access，否则会出现「刷新令牌先于访问令牌过期」的倒挂。
	if int64(cfg.RefreshTokenDays)*86400 <= int64(cfg.AccessTokenMinutes)*60 {
		return SessionConfig{}, apperrors.New(consts.StatusBadRequest, "refresh token 有效期必须长于 access token")
	}
	return cfg, nil
}
