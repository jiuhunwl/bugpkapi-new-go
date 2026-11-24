package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/config"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

type CaptchaService struct {
	cfg      config.CaptchaConfig
	repo     captchaConfigRepository
	rdb      *redis.Client
	registry *Registry
}

type captchaConfigRepository interface {
	CaptchaGlobalEnabled(context.Context) (bool, error)
	CaptchaProviderConfig(context.Context, string, string) (CaptchaProviderConfig, error)
}

type storedCaptcha struct {
	Provider   string `json:"provider"`
	Scene      string `json:"scene"`
	Challenge  string `json:"challenge"`
	Difficulty int    `json:"difficulty"`
	Binding    string `json:"binding"`
	Verified   bool   `json:"verified"`
	// IssuedAt is the issue time in Unix milliseconds. It lets the register
	// risk scorer measure how fast the caller moved from "get captcha" to
	// "submit form"; it is never returned to the client.
	IssuedAt int64 `json:"iat,omitempty"`
}

var proofPattern = regexp.MustCompile(`^[0-9a-zA-Z]{1,32}$`)

const (
	defaultCaptchaTTL        = 5 * time.Minute
	minCaptchaTTL            = 30 * time.Second
	maxCaptchaTTL            = 10 * time.Minute
	maxCaptchaIDBytes        = 128
	maxCaptchaChallengeBytes = 256
)

var verifyCaptchaScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current or current ~= ARGV[1] then
  return 0
end
local ttl = redis.call("PTTL", KEYS[1])
if ttl <= 0 then
  return 0
end
redis.call("SET", KEYS[1], ARGV[2], "PX", ttl)
return 1
`)

var consumeCaptchaScript = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)

func NewCaptchaService(cfg config.CaptchaConfig, repo *Repository, rdb *redis.Client) *CaptchaService {
	return &CaptchaService{
		cfg:      cfg,
		repo:     repo,
		rdb:      rdb,
		registry: NewRegistry(rdb, cfg.TTL, cfg.PowDifficulty),
	}
}

// registryOrNew returns the injected registry, building a default one on the
// fly for callers that construct CaptchaService directly (e.g. tests).
func (s *CaptchaService) registryOrNew() *Registry {
	if s.registry != nil {
		return s.registry
	}
	return NewRegistry(s.rdb, s.cfg.TTL, s.cfg.PowDifficulty)
}

func (s *CaptchaService) Create(ctx context.Context, scene, binding string) (CaptchaChallenge, error) {
	scene = normalizeScene(scene)
	if s == nil || s.repo == nil {
		return CaptchaChallenge{}, apperrors.New(consts.StatusServiceUnavailable, "captcha service is temporarily unavailable")
	}
	globalEnabled, err := s.repo.CaptchaGlobalEnabled(ctx)
	if err != nil {
		return CaptchaChallenge{}, err
	}
	if !globalEnabled {
		return CaptchaChallenge{Scene: scene, Enabled: false}, nil
	}
	providerCfg, err := s.repo.CaptchaProviderConfig(ctx, scene, s.cfg.DefaultProvider)
	if err != nil {
		return CaptchaChallenge{}, err
	}
	if !providerCfg.Enabled {
		return CaptchaChallenge{Scene: scene, Enabled: false}, nil
	}
	if s.rdb == nil {
		return CaptchaChallenge{}, apperrors.New(consts.StatusServiceUnavailable, "captcha service is temporarily unavailable")
	}
	provider, ok := s.registryOrNew().Provider(providerCfg.ProviderKey)
	if !ok {
		return CaptchaChallenge{}, apperrors.New(consts.StatusServiceUnavailable, "验证码 Provider 未配置或待开发")
	}
	challenge, err := provider.Issue(ctx, scene, ParseProviderConfig(providerCfg), binding)
	if err != nil {
		return CaptchaChallenge{}, err
	}
	challenge.CaptchaID = strings.TrimSpace(challenge.CaptchaID)
	if challenge.CaptchaID == "" || len(challenge.CaptchaID) > maxCaptchaIDBytes || challenge.Provider != providerCfg.ProviderKey {
		return CaptchaChallenge{}, apperrors.New(consts.StatusServiceUnavailable, "captcha provider returned an invalid challenge")
	}
	if len(challenge.Challenge) > maxCaptchaChallengeBytes {
		return CaptchaChallenge{}, apperrors.New(consts.StatusServiceUnavailable, "captcha provider returned an invalid challenge")
	}
	challenge.Scene = scene
	if challenge.Provider == ProviderPOW {
		if challenge.Challenge == "" || challenge.Difficulty < 1 || challenge.Difficulty > 6 {
			return CaptchaChallenge{}, apperrors.New(consts.StatusServiceUnavailable, "captcha provider returned an invalid challenge")
		}
	} else {
		challenge.Difficulty = 0
	}
	challenge.Meta, err = sanitizeCaptchaMeta(challenge.Provider, challenge.Meta)
	if err != nil {
		return CaptchaChallenge{}, err
	}
	stored := storedCaptcha{
		Provider:   challenge.Provider,
		Scene:      scene,
		Challenge:  challenge.Challenge,
		Difficulty: challenge.Difficulty,
		Binding:    binding,
		IssuedAt:   time.Now().UnixMilli(),
	}
	data, err := json.Marshal(stored)
	if err != nil {
		return CaptchaChallenge{}, err
	}
	if err := s.rdb.Set(ctx, captchaKey(challenge.CaptchaID), data, s.captchaTTL()).Err(); err != nil {
		return CaptchaChallenge{}, apperrors.Wrap(consts.StatusServiceUnavailable, "captcha service is temporarily unavailable", err)
	}
	return CaptchaChallenge{
		CaptchaID:  challenge.CaptchaID,
		Provider:   challenge.Provider,
		Challenge:  challenge.Challenge,
		Difficulty: challenge.Difficulty,
		Scene:      challenge.Scene,
		Enabled:    true,
		Meta:       challenge.Meta,
	}, nil
}

func (s *CaptchaService) Verify(ctx context.Context, captchaID string, req CaptchaVerifyRequest, binding string, remoteIP ...string) error {
	if s == nil || s.repo == nil {
		return apperrors.New(consts.StatusServiceUnavailable, "captcha service is temporarily unavailable")
	}
	globalEnabled, err := s.repo.CaptchaGlobalEnabled(ctx)
	if err != nil {
		return err
	}
	if !globalEnabled {
		return nil
	}
	captchaID = strings.TrimSpace(captchaID)
	if captchaID == "" || len(captchaID) > maxCaptchaIDBytes {
		return apperrors.New(consts.StatusBadRequest, "验证码ID不能为空")
	}
	stored, raw, err := s.load(ctx, captchaID)
	if err != nil {
		return err
	}
	if stored.Binding != binding {
		return apperrors.New(consts.StatusBadRequest, "验证环境已变化，请刷新后重试")
	}
	if stored.Verified {
		// Verification only marks the challenge as ready for one later consume.
		// Treat network retries as idempotent; Consume remains atomic and single-use.
		return nil
	}
	if s.repo == nil {
		return apperrors.New(consts.StatusServiceUnavailable, "captcha service is temporarily unavailable")
	}
	provider, ok := s.registryOrNew().Provider(stored.Provider)
	if !ok {
		return apperrors.New(consts.StatusBadRequest, "验证码 Provider 未配置或待开发")
	}
	providerCfg, err := s.repo.CaptchaProviderConfig(ctx, stored.Scene, s.cfg.DefaultProvider)
	if err != nil {
		return err
	}
	if !providerCfg.Enabled || providerCfg.ProviderKey != stored.Provider {
		return apperrors.New(consts.StatusBadRequest, "captcha configuration changed; please retry")
	}
	clientIP := ""
	if len(remoteIP) > 0 {
		clientIP = remoteIP[0]
	}
	verified, err := provider.Verify(ctx, StoredChallenge{
		CaptchaID:  captchaID,
		Provider:   stored.Provider,
		Scene:      stored.Scene,
		Challenge:  stored.Challenge,
		Difficulty: stored.Difficulty,
		Binding:    stored.Binding,
	}, ParseProviderConfig(providerCfg), ChallengeSolution{
		Proof:         req.Proof,
		Time:          req.Time,
		Token:         req.Token,
		LotNumber:     req.LotNumber,
		CaptchaOutput: req.CaptchaOutput,
		PassToken:     req.PassToken,
		GenTime:       req.GenTime,
		RemoteIP:      clientIP,
	})
	if err != nil {
		return err
	}
	if !verified {
		return apperrors.New(consts.StatusBadRequest, "验证未通过，请重试")
	}
	stored.Verified = true
	data, err := json.Marshal(stored)
	if err != nil {
		return err
	}
	updated, err := verifyCaptchaScript.Run(ctx, s.rdb, []string{captchaKey(captchaID)}, raw, data).Int64()
	if err != nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "验证码服务暂不可用", err)
	}
	if updated != 1 {
		latest, _, latestErr := s.load(ctx, captchaID)
		if latestErr == nil && latest.Verified && latest.Binding == binding && latest.Scene == stored.Scene {
			return nil
		}
		return apperrors.New(consts.StatusBadRequest, "验证码已失效，请刷新后重试")
	}
	return nil
}

func (s *CaptchaService) Consume(ctx context.Context, captchaID, scene, binding string) error {
	_, err := s.ConsumeWithElapsed(ctx, captchaID, scene, binding)
	return err
}

// ConsumeWithElapsed 与 Consume 行为一致，但额外返回「验证码签发到本次消费」
// 的耗时，供注册风险评分使用。耗时只在服务端可信时钟上计算，客户端无法伪造。
func (s *CaptchaService) ConsumeWithElapsed(ctx context.Context, captchaID, scene, binding string) (time.Duration, error) {
	scene = normalizeScene(scene)
	if s == nil || s.repo == nil {
		return 0, apperrors.New(consts.StatusServiceUnavailable, "验证码服务暂时不可用")
	}
	globalEnabled, err := s.repo.CaptchaGlobalEnabled(ctx)
	if err != nil {
		return 0, err
	}
	if !globalEnabled {
		return 0, nil
	}
	providerCfg, err := s.repo.CaptchaProviderConfig(ctx, scene, s.cfg.DefaultProvider)
	if err != nil {
		return 0, err
	}
	if !providerCfg.Enabled {
		return 0, nil
	}
	captchaID = strings.TrimSpace(captchaID)
	if captchaID == "" || len(captchaID) > maxCaptchaIDBytes {
		return 0, apperrors.New(consts.StatusBadRequest, "请先完成验证码")
	}
	stored, raw, err := s.load(ctx, captchaID)
	if err != nil {
		return 0, err
	}
	if !stored.Verified || stored.Binding != binding || stored.Scene != scene {
		return 0, apperrors.New(consts.StatusBadRequest, "请先完成验证码")
	}
	elapsed := time.Duration(0)
	if stored.IssuedAt > 0 {
		elapsed = time.Since(time.UnixMilli(stored.IssuedAt))
		if elapsed < 0 || elapsed > 24*time.Hour {
			elapsed = 0
		}
	}
	deleted, err := consumeCaptchaScript.Run(ctx, s.rdb, []string{captchaKey(captchaID)}, raw).Int64()
	if err != nil {
		return 0, apperrors.Wrap(consts.StatusServiceUnavailable, "验证码服务暂时不可用", err)
	}
	if deleted != 1 {
		return 0, apperrors.New(consts.StatusBadRequest, "验证码已失效")
	}
	return elapsed, nil
}

func (s *CaptchaService) load(ctx context.Context, captchaID string) (storedCaptcha, []byte, error) {
	if s == nil || s.rdb == nil {
		return storedCaptcha{}, nil, apperrors.New(consts.StatusServiceUnavailable, "验证码服务暂不可用")
	}
	raw, err := s.rdb.Get(ctx, captchaKey(captchaID)).Bytes()
	if err == redis.Nil {
		return storedCaptcha{}, nil, apperrors.New(consts.StatusBadRequest, "验证码已过期，请刷新后重试")
	}
	if err != nil {
		return storedCaptcha{}, nil, apperrors.Wrap(consts.StatusServiceUnavailable, "验证码服务暂不可用", err)
	}
	var stored storedCaptcha
	if err := json.Unmarshal(raw, &stored); err != nil {
		return storedCaptcha{}, nil, apperrors.New(consts.StatusBadRequest, "验证码已失效，请刷新后重试")
	}
	return stored, raw, nil
}

func captchaKey(captchaID string) string {
	return secretRedisKey("auth:captcha:", captchaID)
}

func normalizeScene(scene string) string {
	scene = strings.TrimSpace(scene)
	if scene == "" {
		return "login"
	}
	return scene
}

func (s *CaptchaService) captchaTTL() time.Duration {
	if s == nil || s.cfg.TTL < minCaptchaTTL || s.cfg.TTL > maxCaptchaTTL {
		return defaultCaptchaTTL
	}
	return s.cfg.TTL
}

func sanitizeCaptchaMeta(provider string, values map[string]any) (map[string]any, error) {
	switch provider {
	case ProviderPOW:
		return nil, nil
	case ProviderGeeTest4:
		captchaID := boundedConfigString(stringField(values, "captchaId"), maxGeeTestCaptchaIDBytes)
		product := strings.ToLower(boundedConfigString(stringField(values, "product"), 16))
		if captchaID == "" || (product != "bind" && product != "float" && product != "popup") {
			return nil, apperrors.New(consts.StatusServiceUnavailable, "captcha provider returned invalid public configuration")
		}
		meta := map[string]any{"captchaId": captchaID, "product": product}
		if language := boundedConfigString(stringField(values, "language"), 32); language != "" {
			meta["language"] = language
		}
		return meta, nil
	case ProviderCloudflare:
		siteKey := boundedConfigString(stringField(values, "siteKey"), maxTurnstileSiteKeyBytes)
		action := boundedConfigString(stringField(values, "action"), maxTurnstileActionBytes)
		cData := boundedConfigString(stringField(values, "cData"), maxTurnstileCDataBytes)
		if siteKey == "" || action == "" || cData == "" || !turnstileCustomFieldPattern.MatchString(action) || !turnstileCustomFieldPattern.MatchString(cData) {
			return nil, apperrors.New(consts.StatusServiceUnavailable, "captcha provider returned invalid public configuration")
		}
		return map[string]any{"siteKey": siteKey, "action": action, "cData": cData}, nil
	default:
		return nil, apperrors.New(consts.StatusServiceUnavailable, "captcha provider returned invalid public configuration")
	}
}

func CaptchaBinding(userAgent, acceptLanguage string) string {
	sum := sha256.Sum256([]byte(userAgent + "\n" + acceptLanguage))
	return hex.EncodeToString(sum[:])
}

func validElapsed(value string) bool {
	elapsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	// Elapsed time is supplied by the client and only bounds malformed input; it
	// cannot provide anti-bot security. Keep it within the longest challenge TTL
	// so a paused/backgrounded browser is not rejected while its PoW challenge is
	// still valid; the proof remains the authoritative verification signal.
	return err == nil && elapsed >= 0 && elapsed <= maxCaptchaTTL.Seconds()
}

func validPowProof(captchaID string, stored storedCaptcha, timeText, proof string) bool {
	proof = strings.TrimSpace(proof)
	if !proofPattern.MatchString(proof) || stored.Difficulty < 1 || stored.Difficulty > 6 {
		return false
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%s:%s", captchaID, stored.Challenge, strings.TrimSpace(timeText), proof)))
	return strings.HasPrefix(hex.EncodeToString(sum[:]), strings.Repeat("0", stored.Difficulty))
}
