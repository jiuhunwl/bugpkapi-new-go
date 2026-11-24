package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"bugpk-api/internal/config"

	"github.com/golang-jwt/jwt/v5"
)

const (
	deviceFingerprintV3Prefix = "ua3_"
	deviceFingerprintV2Prefix = "ua2_"
	tokenTypeAccess           = "access"
	tokenTypeRefresh          = "refresh"
	// tokenTypeTwoFA 是密码校验通过后的第二因子验证凭证。
	// 短寿命、无 jti/无刷新审计，唯一合法用途是 POST /auth/2fa/login。
	tokenTypeTwoFA = "2fa"
)

// userAgentVersionPattern 去掉 UA 中的所有数字段（浏览器/系统版本）。
// 浏览器大版本升级不应让已签发的 refresh token 全部失效。
var userAgentVersionPattern = regexp.MustCompile(`\d+(?:[._]\d+)*`)

type JWTService struct {
	cfg config.AuthConfig
	key []byte
	// 后台「登录态配置」的运行时覆盖（秒）。0 表示未覆盖，回落到 cfg 静态值。
	// 覆盖只影响新签发的令牌；已签出的令牌按其自带 exp 自然过渡。
	accessTTLSeconds  atomic.Int64
	refreshTTLSeconds atomic.Int64
}

// ApplySessionTTL 应用后台登录态配置（access 分钟 / refresh 天），任一非正值
// 表示该维度恢复 config.yaml/env 默认。并发安全，签发路径即时生效。
func (s *JWTService) ApplySessionTTL(accessMinutes, refreshDays int) {
	if accessMinutes > 0 {
		s.accessTTLSeconds.Store(int64(accessMinutes) * 60)
	} else {
		s.accessTTLSeconds.Store(0)
	}
	if refreshDays > 0 {
		s.refreshTTLSeconds.Store(int64(refreshDays) * 86400)
	} else {
		s.refreshTTLSeconds.Store(0)
	}
}

// AccessTTL 当前生效的 access token TTL。
func (s *JWTService) AccessTTL() time.Duration {
	if v := s.accessTTLSeconds.Load(); v > 0 {
		return time.Duration(v) * time.Second
	}
	return s.cfg.AccessTokenTTL
}

// RefreshTTL 当前生效的 refresh token TTL（刷新 cookie MaxAge 与签发共用此值）。
func (s *JWTService) RefreshTTL() time.Duration {
	if v := s.refreshTTLSeconds.Load(); v > 0 {
		return time.Duration(v) * time.Second
	}
	return s.cfg.RefreshTokenTTL
}

type RefreshInfo struct {
	UserID       int64
	Username     string
	Roles        []string
	TokenVersion int
	JTI          string
	Device       string
	ExpiresAt    time.Time
}

func NewJWTService(cfg config.AuthConfig) (*JWTService, error) {
	key, err := base64.StdEncoding.DecodeString(cfg.Secret)
	if err != nil {
		key = []byte(cfg.Secret)
	}
	if len(key) < 32 {
		return nil, fmt.Errorf("jwt secret must be at least 32 bytes")
	}
	return &JWTService{cfg: cfg, key: key}, nil
}

func (s *JWTService) CreateAccessToken(user User, now time.Time) (string, error) {
	return s.createToken(user, tokenTypeAccess, "", "", now, now.Add(s.AccessTTL()))
}

func (s *JWTService) CreateRefreshToken(user User, device string, now time.Time) (token, jti string, expiresAt time.Time, err error) {
	jti, err = randomHex(16)
	if err != nil {
		return "", "", time.Time{}, err
	}
	expiresAt = now.Add(s.RefreshTTL())
	token, err = s.createToken(user, tokenTypeRefresh, jti, device, now, expiresAt)
	return token, jti, expiresAt, err
}

// CreateTwoFAChallengeToken 签发 2FA 中间凭证。只带 uid/ver，
// 不带 roles；TTL 由 cfg.TwoFAChallengeTTL 控制（默认 5 分钟）。
func (s *JWTService) CreateTwoFAChallengeToken(user User, now time.Time) (string, time.Time, error) {
	expiresAt := now.Add(s.cfg.TwoFAChallengeTTL)
	claims := jwt.MapClaims{
		"iss":       s.cfg.Issuer,
		"sub":       user.Username,
		"iat":       now.Unix(),
		"exp":       expiresAt.Unix(),
		"uid":       user.ID,
		"ver":       user.TokenVersion,
		"tokenType": tokenTypeTwoFA,
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.key)
	return token, expiresAt, err
}

// ParseTwoFAChallengeToken 校验 2FA 中间凭证，返回 uid/ver。
func (s *JWTService) ParseTwoFAChallengeToken(token string) (RefreshInfo, error) {
	info, tokenType, err := s.parse(token)
	if err != nil {
		return RefreshInfo{}, err
	}
	if tokenType != tokenTypeTwoFA {
		return RefreshInfo{}, fmt.Errorf("invalid token type")
	}
	return info, nil
}

func (s *JWTService) createToken(user User, tokenType, jti, device string, issuedAt, expiresAt time.Time) (string, error) {
	claims := jwt.MapClaims{
		"iss":       s.cfg.Issuer,
		"sub":       user.Username,
		"iat":       issuedAt.Unix(),
		"exp":       expiresAt.Unix(),
		"uid":       user.ID,
		"roles":     user.Roles,
		"ver":       user.TokenVersion,
		"tokenType": tokenType,
	}
	if jti != "" {
		claims["jti"] = jti
	}
	if device != "" {
		claims["device"] = device
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.key)
}

func (s *JWTService) ParseAccessToken(token string) (RefreshInfo, error) {
	info, tokenType, err := s.parse(token)
	if err != nil {
		return RefreshInfo{}, err
	}
	if tokenType != tokenTypeAccess {
		return RefreshInfo{}, fmt.Errorf("invalid token type")
	}
	return info, nil
}

func (s *JWTService) ParseRefreshToken(token, device string) (RefreshInfo, error) {
	info, tokenType, err := s.parse(token)
	if err != nil {
		return RefreshInfo{}, err
	}
	if tokenType != tokenTypeRefresh {
		return RefreshInfo{}, fmt.Errorf("invalid token type")
	}
	if info.JTI == "" {
		return RefreshInfo{}, fmt.Errorf("invalid refresh token id")
	}
	if info.Device == "" || info.Device != device {
		return RefreshInfo{}, fmt.Errorf("invalid refresh token device")
	}
	return info, nil
}

func (s *JWTService) parse(tokenText string) (RefreshInfo, string, error) {
	token, err := jwt.Parse(tokenText, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return s.key, nil
	}, jwt.WithIssuer(s.cfg.Issuer), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return RefreshInfo{}, "", err
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return RefreshInfo{}, "", fmt.Errorf("invalid token")
	}
	issuedAt, err := claims.GetIssuedAt()
	if err != nil || issuedAt == nil {
		return RefreshInfo{}, "", fmt.Errorf("token issued-at is required")
	}

	info := RefreshInfo{
		UserID:       int64(numberClaim(claims, "uid")),
		Username:     stringClaim(claims, "sub"),
		TokenVersion: int(numberClaim(claims, "ver")),
		JTI:          stringClaim(claims, "jti"),
		Device:       stringClaim(claims, "device"),
	}
	if info.UserID <= 0 || info.Username == "" {
		return RefreshInfo{}, "", fmt.Errorf("invalid token subject")
	}
	tokenType := stringClaim(claims, "tokenType")
	if tokenType != tokenTypeAccess && tokenType != tokenTypeRefresh && tokenType != tokenTypeTwoFA {
		return RefreshInfo{}, "", fmt.Errorf("invalid token type")
	}
	if exp, err := claims.GetExpirationTime(); err == nil && exp != nil {
		info.ExpiresAt = exp.Time
	}
	if rawRoles, ok := claims["roles"].([]any); ok {
		for _, role := range rawRoles {
			if text, ok := role.(string); ok {
				info.Roles = append(info.Roles, text)
			}
		}
	}
	return info, tokenType, nil
}

func DeviceFingerprint(ip, userAgent string) string {
	_ = ip
	sum := sha256.Sum256([]byte("BugPk-Api|refresh-device|v3|" + normalizeUserAgent(userAgent)))
	return deviceFingerprintV3Prefix + hex.EncodeToString(sum[:])[:16]
}

// deviceFingerprintV2 是旧版「完整 UA」指纹，仅用于兼容已签发的 refresh
// token；新令牌一律使用 v3。等 refresh 自然过期后可删除。
func deviceFingerprintV2(ip, userAgent string) string {
	_ = ip
	sum := sha256.Sum256([]byte("BugPk-Api|refresh-device|v2|" + strings.TrimSpace(userAgent)))
	return deviceFingerprintV2Prefix + hex.EncodeToString(sum[:])[:16]
}

func normalizeUserAgent(userAgent string) string {
	normalized := strings.ToLower(strings.TrimSpace(userAgent))
	normalized = userAgentVersionPattern.ReplaceAllString(normalized, "")
	return strings.Join(strings.Fields(normalized), " ")
}

func legacyDeviceFingerprint(ip, userAgent string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(ip) + "|" + strings.TrimSpace(userAgent)))
	return hex.EncodeToString(sum[:])[:16]
}

func numberClaim(claims jwt.MapClaims, key string) float64 {
	if value, ok := claims[key].(float64); ok {
		return value
	}
	return 0
}

func stringClaim(claims jwt.MapClaims, key string) string {
	if value, ok := claims[key].(string); ok {
		return value
	}
	return ""
}
