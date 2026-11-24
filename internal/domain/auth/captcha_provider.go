package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

// Provider keys supported by the captcha framework. POW, GeeTest v4, and
// Cloudflare Turnstile are all production implementations behind one lifecycle.
const (
	ProviderPOW        = "pow"
	ProviderGeeTest4   = "geetest4"
	ProviderCloudflare = "cloudflare"
)

// CaptchaProvider abstracts how a challenge is issued and verified for one
// verification service. Every implementation must be safe to call concurrently.
type CaptchaProvider interface {
	// Type returns the provider key: pow / geetest4 / cloudflare.
	Type() string
	// Issue creates a fresh challenge for a scenario. binding identifies the
	// client environment (browser fingerprint) when the provider needs it.
	Issue(ctx context.Context, scenario string, cfg ProviderConfig, binding string) (Challenge, error)
	// Verify validates a client solution against a previously issued challenge.
	// It returns (true, nil) on success; on failure it returns (false, error)
	// with a user-facing error message.
	Verify(ctx context.Context, stored StoredChallenge, cfg ProviderConfig, solution ChallengeSolution) (bool, error)
}

// ProviderConfig is the runtime view of one bp_captcha_provider_config row.
// Extra holds the parsed extra_config JSON.
type ProviderConfig struct {
	ProviderKey   string
	Scene         string
	Enabled       bool
	SiteKey       string
	SecretRef     string
	TimeoutMS     int
	FailurePolicy string
	Extra         map[string]any
}

// ParseProviderConfig converts a repository row into a ProviderConfig.
func ParseProviderConfig(row CaptchaProviderConfig) ProviderConfig {
	cfg := ProviderConfig{
		ProviderKey:   row.ProviderKey,
		Scene:         row.Scene,
		Enabled:       row.Enabled,
		SiteKey:       row.SiteKey,
		SecretRef:     row.SecretRef,
		TimeoutMS:     row.TimeoutMS,
		FailurePolicy: row.FailurePolicy,
	}
	if strings.TrimSpace(row.ExtraConfig) != "" {
		_ = json.Unmarshal([]byte(row.ExtraConfig), &cfg.Extra)
	}
	return cfg
}

// Difficulty returns the POW difficulty from extra_config.
func (c ProviderConfig) Difficulty() int {
	return intField(c.Extra, "difficulty")
}

// CaptchaID returns the geetest4 captcha id (extra_config first, site_key as fallback).
func (c ProviderConfig) CaptchaID() string {
	if value := stringField(c.Extra, "captchaId"); value != "" {
		return value
	}
	return c.SiteKey
}

// APIKey returns the geetest4 api key (stored in secret_ref).
func (c ProviderConfig) APIKey() string {
	return c.SecretRef
}

// SiteKeyValue returns the cloudflare turnstile site key.
func (c ProviderConfig) SiteKeyValue() string {
	if value := stringField(c.Extra, "siteKey"); value != "" {
		return value
	}
	return c.SiteKey
}

// SecretKey returns the cloudflare turnstile secret key (stored in secret_ref).
func (c ProviderConfig) SecretKey() string {
	return c.SecretRef
}

// Challenge is the data returned to the frontend for rendering a captcha.
type Challenge struct {
	Provider   string         `json:"provider"`
	CaptchaID  string         `json:"captchaId"`
	Challenge  string         `json:"challenge"`
	Difficulty int            `json:"difficulty"`
	Scene      string         `json:"scene"`
	Meta       map[string]any `json:"meta,omitempty"`
}

// StoredChallenge is the server-side state needed to verify a solution later.
type StoredChallenge struct {
	CaptchaID  string
	Provider   string
	Scene      string
	Challenge  string
	Difficulty int
	Binding    string
}

// ChallengeSolution carries the client's answer to a challenge. POW uses
// Proof/Time, GeeTest uses its four validation fields, and Turnstile uses Token.
type ChallengeSolution struct {
	Proof         string
	Time          string
	Token         string
	LotNumber     string
	CaptchaOutput string
	PassToken     string
	GenTime       string
	RemoteIP      string
}

// Registry maps provider keys to implementations and provides a POW fallback.
type Registry struct {
	providers map[string]CaptchaProvider
}

// NewRegistry builds the registry with the three supported providers. Challenge
// persistence is owned centrally by CaptchaService for every provider.
func NewRegistry(rdb *redis.Client, ttl time.Duration, defaultDifficulty int) *Registry {
	return &Registry{providers: map[string]CaptchaProvider{
		ProviderPOW:        NewPOWProvider(rdb, ttl, defaultDifficulty),
		ProviderGeeTest4:   NewGeeTest4Provider(),
		ProviderCloudflare: NewCloudflareTurnstileProvider(),
	}}
}

// Provider returns the implementation for a provider key.
func (r *Registry) Provider(kind string) (CaptchaProvider, bool) {
	if r == nil {
		return nil, false
	}
	provider, ok := r.providers[kind]
	return provider, ok
}

// Resolve returns the provider for a key, falling back to POW for unknown keys.
func (r *Registry) Resolve(kind string) CaptchaProvider {
	if provider, ok := r.Provider(kind); ok {
		return provider
	}
	return r.Default()
}

// Default returns the POW provider, the guaranteed-working fallback.
func (r *Registry) Default() CaptchaProvider {
	if r == nil {
		return nil
	}
	return r.providers[ProviderPOW]
}

// Types lists every registered provider key in stable order.
func (r *Registry) Types() []string {
	return []string{ProviderPOW, ProviderGeeTest4, ProviderCloudflare}
}

// POWProvider implements the existing proof-of-work captcha. The verification
// algorithm is unchanged; this is only an adapter to the CaptchaProvider
// interface so POW stays the default and third-party providers can be plugged
// in behind the same interface.
type POWProvider struct {
	defaultDifficulty int
}

// NewPOWProvider creates a POW provider. rdb and ttl remain in the constructor
// for API compatibility; CaptchaService now persists every provider uniformly.
func NewPOWProvider(_ *redis.Client, _ time.Duration, defaultDifficulty int) *POWProvider {
	return &POWProvider{defaultDifficulty: defaultDifficulty}
}

// Type implements CaptchaProvider.
func (p *POWProvider) Type() string { return ProviderPOW }

// Issue implements CaptchaProvider: it generates the data the frontend needs to
// compute the proof. CaptchaService stores the returned challenge atomically.
func (p *POWProvider) Issue(_ context.Context, scenario string, cfg ProviderConfig, _ string) (Challenge, error) {
	difficulty := cfg.Difficulty()
	if difficulty < 1 || difficulty > 6 {
		difficulty = p.defaultDifficulty
	}
	if difficulty < 1 || difficulty > 6 {
		difficulty = 3
	}
	captchaID, err := randomHex(16)
	if err != nil {
		return Challenge{}, apperrors.Wrap(consts.StatusServiceUnavailable, "验证码服务暂不可用", err)
	}
	challenge, err := randomHex(16)
	if err != nil {
		return Challenge{}, apperrors.Wrap(consts.StatusServiceUnavailable, "验证码服务暂不可用", err)
	}
	return Challenge{
		Provider:   ProviderPOW,
		CaptchaID:  captchaID,
		Challenge:  challenge,
		Difficulty: difficulty,
		Scene:      scenario,
	}, nil
}

// Verify implements CaptchaProvider: it checks the client-computed proof using
// the same algorithm as before (validPowProof / validElapsed).
func (p *POWProvider) Verify(ctx context.Context, stored StoredChallenge, cfg ProviderConfig, solution ChallengeSolution) (bool, error) {
	if !validElapsed(solution.Time) || !validPowProof(stored.CaptchaID, storedCaptcha{
		Challenge:  stored.Challenge,
		Difficulty: stored.Difficulty,
	}, solution.Time, solution.Proof) {
		return false, apperrors.New(consts.StatusBadRequest, "验证未通过，请重试")
	}
	return true, nil
}

// GeeTest4Provider implements GeeTest v4 challenge metadata and server-side
// validation against the fixed official validation endpoint.
type GeeTest4Provider struct {
	client   *http.Client
	endpoint string
}

// NewGeeTest4Provider creates the production GeeTest v4 provider.
func NewGeeTest4Provider() *GeeTest4Provider {
	return newGeeTest4Provider(nil, "")
}

// NewGeeTest4ProviderWithHTTPClient is intended for deterministic tests and
// local provider emulators. Production code should use NewGeeTest4Provider.
func NewGeeTest4ProviderWithHTTPClient(client *http.Client, endpoint string) *GeeTest4Provider {
	return newGeeTest4Provider(client, endpoint)
}

func newGeeTest4Provider(client *http.Client, endpoint string) *GeeTest4Provider {
	if client == nil {
		client = defaultCaptchaHTTPClient()
	}
	if strings.TrimSpace(endpoint) == "" {
		endpoint = geeTestValidateEndpoint
	}
	return &GeeTest4Provider{client: client, endpoint: strings.TrimSpace(endpoint)}
}

// Type implements CaptchaProvider.
func (p *GeeTest4Provider) Type() string { return ProviderGeeTest4 }

// Issue implements CaptchaProvider.
func (p *GeeTest4Provider) Issue(ctx context.Context, scenario string, cfg ProviderConfig, binding string) (Challenge, error) {
	return p.issue(ctx, scenario, cfg, binding)
}

// Verify implements CaptchaProvider.
func (p *GeeTest4Provider) Verify(ctx context.Context, stored StoredChallenge, cfg ProviderConfig, solution ChallengeSolution) (bool, error) {
	return p.verify(ctx, stored, cfg, solution)
}

// CloudflareTurnstileProvider implements widget metadata and server-side
// Siteverify validation against the fixed official endpoint.
type CloudflareTurnstileProvider struct {
	client   *http.Client
	endpoint string
}

// NewCloudflareTurnstileProvider creates the production Turnstile provider.
func NewCloudflareTurnstileProvider() *CloudflareTurnstileProvider {
	return newCloudflareTurnstileProvider(nil, "")
}

// NewCloudflareTurnstileProviderWithHTTPClient is intended for deterministic
// tests and local provider emulators.
func NewCloudflareTurnstileProviderWithHTTPClient(client *http.Client, endpoint string) *CloudflareTurnstileProvider {
	return newCloudflareTurnstileProvider(client, endpoint)
}

func newCloudflareTurnstileProvider(client *http.Client, endpoint string) *CloudflareTurnstileProvider {
	if client == nil {
		client = defaultCaptchaHTTPClient()
	}
	if strings.TrimSpace(endpoint) == "" {
		endpoint = cloudflareVerifyEndpoint
	}
	return &CloudflareTurnstileProvider{client: client, endpoint: strings.TrimSpace(endpoint)}
}

// Type implements CaptchaProvider.
func (p *CloudflareTurnstileProvider) Type() string { return ProviderCloudflare }

// Issue implements CaptchaProvider.
func (p *CloudflareTurnstileProvider) Issue(ctx context.Context, scenario string, cfg ProviderConfig, binding string) (Challenge, error) {
	return p.issue(ctx, scenario, cfg, binding)
}

// Verify implements CaptchaProvider.
func (p *CloudflareTurnstileProvider) Verify(ctx context.Context, stored StoredChallenge, cfg ProviderConfig, solution ChallengeSolution) (bool, error) {
	return p.verify(ctx, stored, cfg, solution)
}

func stringField(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func intField(values map[string]any, key string) int {
	if values == nil {
		return 0
	}
	value, _ := values[key].(float64)
	return int(value)
}
