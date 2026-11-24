package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/pkg/totp"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

// 2FA Redis 键与常量。pending secret 放 Redis 不放库：绑定流程半途
// 放弃时无需清理数据库，TTL 到期自然失效。
const (
	twoFAPendingKeyPrefix = "auth:2fa:pending:"
	twoFARecoveryCount    = 10
)

func twoFAPendingKey(userID int64) string {
	return twoFAPendingKeyPrefix + strconv.FormatInt(userID, 10)
}

// verifyUserPassword 用恒定时间 bcrypt 比较校验当前会话用户的密码。
// 未设置密码（例如纯 OAuth 账号）时直接拒绝。
func verifyUserPassword(user User, password string) error {
	if strings.TrimSpace(password) == "" || user.PasswordHash == "" {
		return apperrors.New(consts.StatusForbidden, "密码验证失败")
	}
	if bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)) != nil {
		return apperrors.New(consts.StatusForbidden, "密码验证失败")
	}
	return nil
}

// IsAdminRole 判断角色列表里是否含管理员。找回密码强化与审计告警
// 都以该判断为准，与 rbac 的 admin role_key 保持一致。
func IsAdminRole(roles []string) bool {
	for _, role := range roles {
		if strings.EqualFold(strings.TrimSpace(role), "admin") {
			return true
		}
	}
	return false
}

// TwoFAStatus 返回当前用户的 2FA 状态（需要有效 access token）。
func (s *Service) TwoFAStatus(ctx context.Context, accessToken string) (TwoFAStatusResult, error) {
	user, err := s.UserFromAccessToken(ctx, accessToken)
	if err != nil {
		return TwoFAStatusResult{}, err
	}
	result := TwoFAStatusResult{Enabled: user.TOTPEnabled}
	if user.TOTPEnabled {
		count, err := s.repo.CountRecoveryCodes(ctx, user.ID)
		if err != nil {
			return TwoFAStatusResult{}, apperrors.Wrap(consts.StatusInternalServerError, "查询 2FA 状态失败", err)
		}
		result.RecoveryCodesRemaining = count
	}
	return result, nil
}

// TwoFASetup 生成新密钥并暂存 Redis。已启用 2FA 的用户必须先关闭才能
// 重新绑定，避免覆盖正在生效的密钥。
func (s *Service) TwoFASetup(ctx context.Context, accessToken string) (TwoFASetupResult, error) {
	user, err := s.UserFromAccessToken(ctx, accessToken)
	if err != nil {
		return TwoFASetupResult{}, err
	}
	if user.TOTPEnabled {
		return TwoFASetupResult{}, apperrors.New(consts.StatusBadRequest, "2FA 已启用，请先关闭后重新绑定")
	}
	if s.rdb == nil {
		return TwoFASetupResult{}, apperrors.New(consts.StatusServiceUnavailable, "认证服务暂不可用")
	}
	secret, err := totp.GenerateSecret()
	if err != nil {
		return TwoFASetupResult{}, apperrors.Wrap(consts.StatusInternalServerError, "生成 2FA 密钥失败", err)
	}
	pendingTTL := s.cfg.TwoFAPendingTTL
	if pendingTTL <= 0 {
		pendingTTL = 10 * time.Minute
	}
	if err := s.rdb.Set(ctx, twoFAPendingKey(user.ID), secret, pendingTTL).Err(); err != nil {
		return TwoFASetupResult{}, apperrors.Wrap(consts.StatusServiceUnavailable, "认证服务暂不可用", err)
	}
	return TwoFASetupResult{
		Secret:     secret,
		OtpauthURL: totp.ProvisioningURL(secret, s.cfg.Issuer, user.Username),
	}, nil
}

// TwoFAEnable 用首次验证码确认绑定：密钥从 Redis 暂存转入数据库
// （加密落库），同事务生成一次性恢复码。确认成功前绑定不生效。
func (s *Service) TwoFAEnable(ctx context.Context, accessToken string, req TwoFAEnableRequest) (TwoFAEnableResult, error) {
	user, err := s.UserFromAccessToken(ctx, accessToken)
	if err != nil {
		return TwoFAEnableResult{}, err
	}
	if user.TOTPEnabled {
		return TwoFAEnableResult{}, apperrors.New(consts.StatusBadRequest, "2FA 已启用，请先关闭后重新绑定")
	}
	if s.rdb == nil {
		return TwoFAEnableResult{}, apperrors.New(consts.StatusServiceUnavailable, "认证服务暂不可用")
	}
	secret, err := s.rdb.Get(ctx, twoFAPendingKey(user.ID)).Result()
	if err == redis.Nil {
		return TwoFAEnableResult{}, apperrors.New(consts.StatusBadRequest, "绑定会话已过期，请重新获取二维码")
	}
	if err != nil {
		return TwoFAEnableResult{}, apperrors.Wrap(consts.StatusServiceUnavailable, "认证服务暂不可用", err)
	}
	ok, err := totp.Verify(secret, strings.TrimSpace(req.Code), s.now())
	if err != nil {
		return TwoFAEnableResult{}, apperrors.New(consts.StatusBadRequest, "验证码格式不正确")
	}
	if !ok {
		return TwoFAEnableResult{}, apperrors.New(consts.StatusBadRequest, "验证码错误，请输入认证器当前显示的 6 位数字")
	}
	encrypted, err := s.repo.cipher.Encrypt(secret)
	if err != nil {
		return TwoFAEnableResult{}, apperrors.Wrap(consts.StatusInternalServerError, "2FA 密钥加密失败", err)
	}
	codes, hashes, err := generateRecoveryCodes(twoFARecoveryCount)
	if err != nil {
		return TwoFAEnableResult{}, apperrors.Wrap(consts.StatusInternalServerError, "生成恢复码失败", err)
	}
	if err := s.repo.EnableTOTP(ctx, user.ID, encrypted, hashes, s.now()); err != nil {
		return TwoFAEnableResult{}, err
	}
	// 绑定生效后暂存即刻作废，防止同一密钥被二次确认。
	_ = s.rdb.Del(ctx, twoFAPendingKey(user.ID)).Err()
	s.auditAuth(ctx, AuditEntry{
		UserID: user.ID, Username: user.Username, Roles: append([]string(nil), user.Roles...),
		Module: "auth", Action: "2fa_enable", Summary: "启用 2FA 二次验证", Status: true,
		StatusCode: consts.StatusOK, ClientIP: "",
	})
	return TwoFAEnableResult{RecoveryCodes: codes}, nil
}

// TwoFADisable 需要密码 + 当前第二因子双确认。单独拿到会话或单独
// 拿到密码都不足以关闭 2FA。
func (s *Service) TwoFADisable(ctx context.Context, accessToken string, req TwoFADisableRequest) error {
	user, err := s.UserFromAccessToken(ctx, accessToken)
	if err != nil {
		return err
	}
	if !user.TOTPEnabled {
		return apperrors.New(consts.StatusBadRequest, "2FA 未启用")
	}
	if err := verifyUserPassword(user, req.Password); err != nil {
		return err
	}
	if err := s.verifySecondFactor(ctx, user, req.Code); err != nil {
		return err
	}
	if err := s.repo.DisableTOTP(ctx, user.ID); err != nil {
		return err
	}
	s.auditAuth(ctx, AuditEntry{
		UserID: user.ID, Username: user.Username, Roles: append([]string(nil), user.Roles...),
		Module: "auth", Action: "2fa_disable", Summary: "关闭 2FA 二次验证", Status: true,
		StatusCode: consts.StatusOK, ClientIP: "",
	})
	return nil
}

// TwoFARegenerateRecoveryCodes 换发恢复码：旧的立即作废。
// 同样要求密码 + 当前第二因子。
func (s *Service) TwoFARegenerateRecoveryCodes(ctx context.Context, accessToken string, req TwoFARecoveryRegenerateRequest) (TwoFAEnableResult, error) {
	user, err := s.UserFromAccessToken(ctx, accessToken)
	if err != nil {
		return TwoFAEnableResult{}, err
	}
	if !user.TOTPEnabled {
		return TwoFAEnableResult{}, apperrors.New(consts.StatusBadRequest, "2FA 未启用")
	}
	if err := verifyUserPassword(user, req.Password); err != nil {
		return TwoFAEnableResult{}, err
	}
	if err := s.verifySecondFactor(ctx, user, req.Code); err != nil {
		return TwoFAEnableResult{}, err
	}
	codes, hashes, err := generateRecoveryCodes(twoFARecoveryCount)
	if err != nil {
		return TwoFAEnableResult{}, apperrors.Wrap(consts.StatusInternalServerError, "生成恢复码失败", err)
	}
	// 复用 EnableTOTP 的密钥写入路径：密钥不变，仅替换恢复码集合。
	encrypted, err := s.repo.cipher.Encrypt(user.TOTPSecret)
	if err != nil {
		return TwoFAEnableResult{}, apperrors.Wrap(consts.StatusInternalServerError, "2FA 密钥加密失败", err)
	}
	if err := s.repo.EnableTOTP(ctx, user.ID, encrypted, hashes, s.now()); err != nil {
		return TwoFAEnableResult{}, err
	}
	s.auditAuth(ctx, AuditEntry{
		UserID: user.ID, Username: user.Username, Roles: append([]string(nil), user.Roles...),
		Module: "auth", Action: "2fa_recovery_codes", Summary: "重新生成 2FA 恢复码", Status: true,
		StatusCode: consts.StatusOK, ClientIP: "",
	})
	return TwoFAEnableResult{RecoveryCodes: codes}, nil
}

// LoginTwoFA 是 2FA 登录的第二步：challenge token + 验证码换正式令牌。
func (s *Service) LoginTwoFA(ctx context.Context, req TwoFALoginRequest, ip, userAgent string) (LoginOutcome, error) {
	if strings.TrimSpace(req.ChallengeToken) == "" || strings.TrimSpace(req.Code) == "" {
		return LoginOutcome{}, apperrors.New(consts.StatusBadRequest, "请输入 2FA 验证码")
	}
	if s.limiter == nil {
		return LoginOutcome{}, apperrors.New(consts.StatusServiceUnavailable, "认证服务暂时不可用")
	}
	info, err := s.jwt.ParseTwoFAChallengeToken(strings.TrimSpace(req.ChallengeToken))
	if err != nil {
		return LoginOutcome{}, apperrors.New(consts.StatusUnauthorized, "2FA 会话已过期，请重新登录")
	}
	user, err := s.repo.FindUserByID(ctx, info.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LoginOutcome{}, apperrors.New(consts.StatusUnauthorized, "2FA 会话已过期，请重新登录")
		}
		return LoginOutcome{}, apperrors.Wrap(consts.StatusServiceUnavailable, "认证服务暂时不可用", err)
	}
	if err := s.limiter.CheckTwoFAVerify(ctx, ip, user.Username); err != nil {
		return LoginOutcome{}, err
	}
	if err := s.checkAccountActive(user); err != nil {
		return LoginOutcome{}, err
	}
	if user.TokenVersion != info.TokenVersion {
		return LoginOutcome{}, apperrors.New(consts.StatusUnauthorized, "2FA 会话已过期，请重新登录")
	}
	if !user.TOTPEnabled {
		return LoginOutcome{}, apperrors.New(consts.StatusUnauthorized, "2FA 会话已过期，请重新登录")
	}
	if err := s.verifySecondFactor(ctx, user, req.Code); err != nil {
		// 错误的验证码也计入登录失败锁定体系，让既有账号锁直接生效。
		if limitErr := s.limiter.RecordLoginFailure(ctx, ip, user.Username); limitErr != nil {
			return LoginOutcome{}, limitErr
		}
		s.auditAuth(ctx, AuditEntry{
			UserID: user.ID, Username: user.Username, Roles: append([]string(nil), user.Roles...),
			Module: "auth", Action: "login_2fa", Summary: "2FA 验证失败", Status: false,
			ErrorMessage: "invalid second factor", StatusCode: consts.StatusForbidden,
			ClientIP: ip, UserAgent: userAgent,
		})
		return LoginOutcome{}, err
	}
	if err := s.limiter.RecordLoginSuccess(ctx, user.Username); err != nil {
		return LoginOutcome{}, err
	}
	if err := s.repo.UpdateLastLogin(ctx, user.ID); err != nil {
		return LoginOutcome{}, err
	}
	pair, err := s.issueTokens(ctx, user, ip, userAgent)
	if err != nil {
		return LoginOutcome{}, err
	}
	s.auditAuth(ctx, AuditEntry{
		UserID: user.ID, Username: user.Username, Roles: append([]string(nil), user.Roles...),
		Module: "auth", Action: "login_2fa", Summary: "登录成功（2FA）", Status: true,
		StatusCode: consts.StatusOK, ClientIP: ip, UserAgent: userAgent,
	})
	return LoginOutcome{Pair: pair}, nil
}

// verifySecondFactor 依次尝试 TOTP 与一次性恢复码。恢复码命中即原子
// 消费；两者都失败返回统一的错误（不区分失败原因，避免给爆破者反馈）。
func (s *Service) verifySecondFactor(ctx context.Context, user User, code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return apperrors.New(consts.StatusForbidden, "请输入 2FA 验证码")
	}
	if user.TOTPSecret != "" {
		ok, err := totp.Verify(user.TOTPSecret, code, s.now())
		if err != nil && s.log != nil {
			s.log.Warn("totp verify failed", zap.Int64("user_id", user.ID), zap.String("error", err.Error()))
		}
		if ok {
			return nil
		}
	}
	// 恢复码形态：8~20 位、去横线后为十六进制。TOTP 6 位数字码在
	// 上一步已经失败，这里只处理恢复码形态，避免无谓的哈希查询。
	normalized := strings.ToLower(strings.ReplaceAll(code, "-", ""))
	if len(normalized) >= 8 && len(normalized) <= 20 && isHex(normalized) {
		sum := sha256.Sum256([]byte(normalized))
		consumed, err := s.repo.ConsumeRecoveryCode(ctx, user.ID, hex.EncodeToString(sum[:]))
		if err == nil && consumed {
			return nil
		}
	}
	return apperrors.New(consts.StatusForbidden, "2FA 验证码错误")
}

// generateRecoveryCodes 生成 n 个形如 "ab12cd34ef" 的一次性恢复码，
// 返回明文（响应给用户一次）与 SHA-256 哈希（落库）。
func generateRecoveryCodes(n int) (codes []string, hashes []string, err error) {
	codes = make([]string, 0, n)
	hashes = make([]string, 0, n)
	for i := 0; i < n; i++ {
		raw := make([]byte, 5)
		if _, err := rand.Read(raw); err != nil {
			return nil, nil, err
		}
		code := hex.EncodeToString(raw)
		codes = append(codes, code[:5]+"-"+code[5:])
		sum := sha256.Sum256([]byte(code))
		hashes = append(hashes, hex.EncodeToString(sum[:]))
	}
	return codes, hashes, nil
}

func isHex(value string) bool {
	for _, r := range value {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
