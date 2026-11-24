package openapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/pkg/accountstatus"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/redisutil"
	"bugpk-api/internal/pkg/response"

	hertzapp "github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	// headerOpenKey 是唯一接受的凭据载体（请求头；禁 query/cookie，
	// 天然免疫 CSRF 自动携带）。
	headerOpenKey = "X-Open-Key"
	// openPrincipalContextKey 是注入请求上下文的 OpenPrincipal 键。
	openPrincipalContextKey = "openapi.principal"
	// configCacheTTL 是全局开关的本地缓存时长（语义：最多 30s 传播延迟；
	// 需即时生效时用管理端「一键吊销全部 Key」或同进程内配置变更即时失效）。
	configCacheTTL = 30 * time.Second
)

// OpenAuthMiddleware 是 /open-api 平面的鉴权链（顺序不可变）：
//
//	X-Open-Key 缺失 401 → SHA-256 查哈希 → 四层开关（全局/账号封禁/管理员禁用/用户开关）
//	→ Key 状态与过期 → IP 白名单（admin 作用域必填）→ Key 维度频控
//	→ 注入 OpenPrincipal（scope 门由 RequireScope 端点级判定）。
//
// 频控故障 fail-closed（503）；每个失败分支都带稳定错误码。
func (s *Service) OpenAuthMiddleware() hertzapp.HandlerFunc {
	return func(ctx context.Context, c *hertzapp.RequestContext) {
		header := strings.TrimSpace(string(c.Request.Header.Peek(headerOpenKey)))
		if header == "" {
			abortOpen(c, openError(CodeKeyMissing, consts.StatusUnauthorized, "缺少 X-Open-Key 请求头"))
			return
		}
		authCtx, err := s.repo.AuthContextByHash(ctx, hashKey(header))
		if err != nil {
			if err == errNotFound {
				abortOpen(c, openError(CodeKeyInvalid, consts.StatusUnauthorized, "开放 Key 无效"))
				return
			}
			abortOpenServer(c, err)
			return
		}
		cfg, err := s.cachedConfig(ctx)
		if err != nil {
			abortOpenServer(c, err)
			return
		}
		// 四层开关：全局 → 账号封禁 → 管理员强制禁用 → 用户自助开关（实时判定）。
		if !cfg.Enabled {
			abortOpen(c, openError(CodeDisabled, consts.StatusForbidden, "开放 API 未开启"))
			return
		}
		// 账号封禁：封禁即切断一切服务入口，独立于开放 API 自身三层开关。
		if !accountstatus.IsActive(authCtx.UserStatus) {
			abortOpen(c, openError(CodeAccountDisabled, consts.StatusForbidden, "该账号已被封禁"))
			return
		}
		if authCtx.UserAdminDisabled == 1 {
			abortOpen(c, openError(CodeAdminDisabled, consts.StatusForbidden, "该账号的开放 API 已被管理员禁用"))
			return
		}
		if authCtx.UserOpenAPIEnabled != 1 {
			abortOpen(c, openError(CodeUserDisabled, consts.StatusForbidden, "该账号已关闭开放 API"))
			return
		}
		if authCtx.Key.Status != KeyStatusActive {
			abortOpen(c, openError(CodeKeyRevoked, consts.StatusUnauthorized, "开放 Key 已吊销"))
			return
		}
		if !authCtx.Key.ExpiresAt.After(time.Now()) {
			abortOpen(c, openError(CodeKeyExpired, consts.StatusUnauthorized, "开放 Key 已过期"))
			return
		}
		// IP 白名单：空清单=不限（仅普通 Key 成立；admin 作用域签发期已强制非空）。
		rules, err := s.repo.IPWhitelistByKey(ctx, authCtx.Key.ID)
		if err != nil {
			abortOpenServer(c, err)
			return
		}
		clientIP := c.ClientIP()
		if len(rules) == 0 && authCtx.Key.IsAdminScope {
			abortOpen(c, openError(CodeIPForbidden, consts.StatusForbidden, "管理员作用域 Key 缺少 IP 白名单"))
			return
		}
		if !ipAllowed(rules, clientIP) {
			abortOpen(c, openError(CodeIPForbidden, consts.StatusForbidden, "来源 IP 不在白名单内"))
			return
		}
		scopes, err := s.repo.ScopesByKey(ctx, authCtx.Key.ID)
		if err != nil {
			abortOpenServer(c, err)
			return
		}
		limit := cfg.RateLimitNormal
		if authCtx.Key.IsAdminScope {
			limit = cfg.RateLimitAdmin
		}
		if err := s.hitRate(ctx, authCtx.Key.ID, limit); err != nil {
			abortOpen(c, err)
			return
		}
		c.Set(openPrincipalContextKey, OpenPrincipal{
			KeyID:        authCtx.Key.ID,
			KeyName:      authCtx.Key.Name,
			UserID:       authCtx.Key.UserID,
			IsAdminScope: authCtx.Key.IsAdminScope,
			Scopes:       toScopeSet(scopes),
			ClientIP:     clientIP,
		})
		s.touchLastUsed(authCtx.Key.ID)
		c.Next(ctx)
	}
}

// RequireScope 端点级 scope 门（第一门）：scope 清单不含本端点 code 即 403。
// 先于一切业务逻辑执行，且不消耗业务配额语义（频控仍按 Key 维度计数）。
func RequireScope(code string) hertzapp.HandlerFunc {
	return func(ctx context.Context, c *hertzapp.RequestContext) {
		principal, ok := OpenPrincipalFrom(c)
		if !ok {
			abortOpen(c, openError(CodeKeyMissing, consts.StatusUnauthorized, "缺少开放 Key 身份"))
			return
		}
		if !principal.HasScope(code) {
			abortOpen(c, openError(CodeScopeDenied, consts.StatusForbidden, "该开放 Key 未授权此端点"))
			return
		}
		c.Next(ctx)
	}
}

// OpenPrincipalFrom 从请求上下文取出鉴权结果。
func OpenPrincipalFrom(c *hertzapp.RequestContext) (OpenPrincipal, bool) {
	value, ok := c.Get(openPrincipalContextKey)
	if !ok {
		return OpenPrincipal{}, false
	}
	principal, ok := value.(OpenPrincipal)
	return principal, ok
}

// cachedConfig 读取全局配置并做 30s 本地缓存（语义明确：最多 30s 传播延迟）。
func (s *Service) cachedConfig(ctx context.Context) (Config, error) {
	s.configMu.RLock()
	cached, ok := s.configCache, s.configCachedAt.Add(configCacheTTL).After(time.Now())
	s.configMu.RUnlock()
	if ok {
		return cached, nil
	}
	cfg, err := s.repo.Config(ctx)
	if err != nil {
		return Config{}, err
	}
	s.cacheConfig(cfg)
	return cfg, nil
}

// hitRate Key 维度分钟档频控：Redis Lua 原子计数（INCR+PEXPIRE 同脚本），
// 故障 fail-closed（503），绝不放行。
func (s *Service) hitRate(ctx context.Context, keyID int64, limit int) error {
	if limit <= 0 {
		return nil
	}
	if s.rdb == nil {
		return openError(CodeRateLimited, consts.StatusServiceUnavailable, "服务繁忙，请稍后重试")
	}
	current, err := redisutil.IncrWithTTL(ctx, s.rdb, s.rateKey(keyID), time.Minute)
	if err != nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试", err)
	}
	if current > int64(limit) {
		return openError(CodeRateLimited, consts.StatusTooManyRequests, "调用过于频繁，请稍后再试")
	}
	return nil
}

// rateKey 频控键：只含 Key 编号的 HMAC 摘要，Redis 不落明文标识。
func (s *Service) rateKey(keyID int64) string {
	mac := hmac.New(sha256.New, s.digestKey)
	mac.Write([]byte("openapi:key:" + strconv.FormatInt(keyID, 10)))
	return "openapi:rl:" + hex.EncodeToString(mac.Sum(nil)[:16])
}

// touchLastUsed 异步写 last_used_at：不阻塞请求、失败静默（不改变业务结果）。
// SQL 自带「仅当更早才写」条件，天然抑制写放大；worker 参与优雅停机。
func (s *Service) touchLastUsed(keyID int64) {
	now := time.Now()
	s.worker.Add(1)
	go func() {
		defer s.worker.Done()
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.repo.TouchLastUsedAt(ctx, keyID, now)
	}()
}

// Close 等待异步 last_used_at 写入收尾（优雅停机钩子，签名对齐 registerCloser）。
func (s *Service) Close(context.Context) error {
	s.worker.Wait()
	return nil
}

func abortOpen(c *hertzapp.RequestContext, err error) {
	response.FromError(c, err)
	c.Abort()
}

func abortOpenServer(c *hertzapp.RequestContext, err error) {
	response.FromError(c, apperrors.Wrap(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试", err))
	c.Abort()
}

func toScopeSet(scopes []string) map[string]struct{} {
	set := make(map[string]struct{}, len(scopes))
	for _, code := range scopes {
		set[code] = struct{}{}
	}
	return set
}
