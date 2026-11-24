package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"

	"bugpk-api/internal/pkg/emailpolicy"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// 注册风控 guard：邮箱域白/黑名单 + 同 IP 账号数硬上限 + 蜜罐。
//
// 统一约定：
//   - 对客户端只返回可行动的普通业务文案，不暴露具体命中的风控规则细节；
//   - 每次拦截都通过 auditAuth 留痕（Summary 面向管理员），便于离线分析。
//
// 蜜罐字段（RegisterRequest.Website）由前端以 CSS 隐藏，真实用户不会填写。
// 检测到非空即判定为盲填表单的自动化脚本：不创建账号、不消费验证码，
// 返回与真实成功完全一致的响应形状（假令牌，后续任何鉴权调用都会失败），
// 让攻击者无法通过响应区分「注册成功」与「被风控丢弃」。

const registerIPTooFrequentMessage = "注册请求过于频繁，请稍后再试"

// 同一句话同时用于注册、绑定邮箱与找回密码三个入口，所以不能写「不支持注册」。
const emailDomainRejectedMessage = "该邮箱域名不受当前站点支持，请更换其他邮箱"

// honeypotTripped reports whether the honeypot field was filled.
func honeypotTripped(req RegisterRequest) bool {
	return strings.TrimSpace(req.Website) != ""
}

// fakeTokenPair 生成形状与真实令牌一致但无法通过任何鉴权的假令牌对。
// 访问令牌与刷新令牌都是三段式 JWT 形状；签名段为随机字节，
// 服务端验签必然失败；JTI 不存在于刷新审计表，refresh 必然 401。
func fakeTokenPair() TokenPair {
	now := time.Now()
	return TokenPair{
		AccessToken:  fakeJWT(now),
		RefreshToken: fakeJWT(now.Add(15 * time.Minute)),
		RefreshJTI:   fakeRandomBase64(16),
		RefreshExp:   now.Add(15 * time.Minute),
	}
}

func fakeJWT(exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"exp": exp.Unix(),
		"iat": time.Now().Unix(),
		"jti": fakeRandomBase64(16),
	})
	if err != nil {
		claims = []byte("{}")
	}
	payload := base64.RawURLEncoding.EncodeToString(claims)
	signature := fakeRandomBase64(32)
	return header + "." + payload + "." + signature
}

func fakeRandomBase64(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败在实际部署中不会发生；退化为零值仍保持形状正确。
		buf = make([]byte, n)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// checkRegisterEmailDomain enforces the admin-managed email domain policy.
// A non-empty whitelist makes registration exclusive to those domains; the
// blacklist always rejects. A `*.example.com` rule matches every sub-domain
// of example.com (but not example.com itself). The check runs before the
// verification code is verified so invalid-policy requests never consume
// email/SMS quota.
func (s *Service) checkRegisterEmailDomain(ctx context.Context, email string) error {
	whitelist, blacklist, err := s.repo.EmailDomainRules(ctx)
	if err != nil {
		return err
	}
	if decision := emailpolicy.Evaluate(whitelist, blacklist, email, true); !decision.Allowed {
		return apperrors.New(consts.StatusForbidden, emailDomainRejectedMessage)
	}
	return nil
}

// domainRuleMatches / emailDomain 保留为薄包装，供注册风控与既有测试使用，
// 规则实现统一放在 pkg/emailpolicy，避免多处拷贝后语义漂移。
func domainRuleMatches(rules []string, domain string) bool {
	return emailpolicy.Matches(rules, domain)
}

func emailDomain(email string) (string, bool) {
	return emailpolicy.Domain(email)
}

// checkRegisterIPQuota enforces the lifetime cap of accounts per registration
// IP. It is a pre-check; the authoritative 1-account backstop is the primary
// key on bp_register_ip_used inside CreateLocalUser's transaction.
func (s *Service) checkRegisterIPQuota(ctx context.Context, ip string, cfg RegisterConfig) error {
	if cfg.RegisterIPMaxAccounts <= 0 {
		return nil
	}
	count, err := s.repo.CountRegisterIPAccounts(ctx, ip)
	if err != nil {
		return err
	}
	if count >= int64(cfg.RegisterIPMaxAccounts) {
		return apperrors.New(consts.StatusTooManyRequests, registerIPTooFrequentMessage)
	}
	return nil
}
