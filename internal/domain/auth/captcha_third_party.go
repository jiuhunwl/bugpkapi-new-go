package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/httpclient"
	"bugpk-api/internal/pkg/jsonrequest"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	geeTestValidateEndpoint  = "https://gcaptcha4.geetest.com/validate"
	cloudflareVerifyEndpoint = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	maxCaptchaProviderBody   = int64(256 << 10)
	defaultCaptchaTimeout    = 3 * time.Second
	minCaptchaTimeout        = 100 * time.Millisecond
	maxCaptchaTimeout        = 30 * time.Second
	maxGeeTestCaptchaIDBytes = 128
	maxGeeTestAPIKeyBytes    = 128
	maxTurnstileSiteKeyBytes = 255
	maxTurnstileSecretBytes  = 128
	maxTurnstileTokenBytes   = 2048
	maxTurnstileActionBytes  = 32
	maxTurnstileCDataBytes   = 255
	maxTurnstilePrefixBytes  = 128
)

var turnstileCustomFieldPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func defaultCaptchaHTTPClient() *http.Client {
	// httpclient.Outbound disables environment proxies, validates every DNS
	// result, blocks private/link-local destinations, limits redirects, and
	// caps response bodies. Captcha endpoints are fixed official HTTPS URLs.
	return httpclient.Outbound(maxCaptchaTimeout)
}

func (p ProviderConfig) geeTestProduct() string {
	value := strings.ToLower(stringField(p.Extra, "product"))
	switch value {
	case "bind", "float", "popup":
		return value
	default:
		return "float"
	}
}

func (p ProviderConfig) geeTestLanguage() string {
	value := stringField(p.Extra, "language")
	if len(value) > 32 {
		return ""
	}
	return value
}

func (p ProviderConfig) cloudflareHostname() (string, error) {
	raw := stringField(p.Extra, "hostname")
	if raw == "" {
		return "", nil
	}
	value := normalizeHostname(raw)
	if value == "" {
		return "", apperrors.New(consts.StatusServiceUnavailable, "Cloudflare Turnstile hostname configuration is invalid")
	}
	return value, nil
}

func (p ProviderConfig) cloudflareAction(scene string) (string, error) {
	value := stringField(p.Extra, "action")
	if value == "" {
		value = strings.TrimSpace(scene)
	}
	if value == "" || len(value) > maxTurnstileActionBytes || !turnstileCustomFieldPattern.MatchString(value) {
		return "", apperrors.New(consts.StatusServiceUnavailable, "Cloudflare Turnstile action configuration is invalid")
	}
	return value, nil
}

func (p ProviderConfig) cloudflareCData(captchaID string) (string, error) {
	prefix := stringField(p.Extra, "cdata")
	if prefix != "" && (len(prefix) > maxTurnstilePrefixBytes || !turnstileCustomFieldPattern.MatchString(prefix)) {
		return "", apperrors.New(consts.StatusServiceUnavailable, "Cloudflare Turnstile cData configuration is invalid")
	}
	value := strings.TrimSpace(captchaID)
	if prefix != "" {
		value = prefix + "_" + value
	}
	if value == "" || len(value) > maxTurnstileCDataBytes || !turnstileCustomFieldPattern.MatchString(value) {
		return "", apperrors.New(consts.StatusServiceUnavailable, "Cloudflare Turnstile cData configuration is invalid")
	}
	return value, nil
}

func boundedConfigString(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > max {
		return ""
	}
	return value
}

func normalizeHostname(value string) string {
	value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if value == "" || len(value) > 253 || strings.ContainsAny(value, "/\\:@ \t\r\n") {
		return ""
	}
	if net.ParseIP(value) != nil || value == "localhost" {
		return value
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return ""
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return ""
			}
		}
	}
	return value
}

func captchaContext(ctx context.Context, configured time.Duration) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if configured < minCaptchaTimeout || configured > maxCaptchaTimeout {
		configured = defaultCaptchaTimeout
	}
	child, cancel := context.WithTimeout(ctx, configured)
	return child, cancel, nil
}

func configuredCaptchaTimeout(cfg ProviderConfig) time.Duration {
	if cfg.TimeoutMS <= 0 {
		return defaultCaptchaTimeout
	}
	return time.Duration(cfg.TimeoutMS) * time.Millisecond
}

func providerRequestError(provider string, err error) error {
	if err == nil {
		return nil
	}
	return apperrors.Wrap(consts.StatusServiceUnavailable, provider+" verification service is temporarily unavailable", err)
}

func providerResponseError(provider string, status int) error {
	return apperrors.New(consts.StatusServiceUnavailable, fmt.Sprintf("%s verification service returned HTTP %d", provider, status))
}

func (p *GeeTest4Provider) issue(ctx context.Context, scenario string, cfg ProviderConfig, _ string) (Challenge, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Challenge{}, providerRequestError("GeeTest", err)
	}
	captchaID := strings.TrimSpace(cfg.CaptchaID())
	apiKey := strings.TrimSpace(cfg.APIKey())
	if captchaID == "" || len(captchaID) > maxGeeTestCaptchaIDBytes || apiKey == "" || len(apiKey) > maxGeeTestAPIKeyBytes {
		return Challenge{}, apperrors.New(consts.StatusServiceUnavailable, "GeeTest v4 is not configured")
	}
	internalID, err := randomHex(16)
	if err != nil {
		return Challenge{}, providerRequestError("GeeTest", err)
	}
	challenge, err := randomHex(16)
	if err != nil {
		return Challenge{}, providerRequestError("GeeTest", err)
	}
	meta := map[string]any{
		"captchaId": captchaID,
		"product":   cfg.geeTestProduct(),
	}
	if language := cfg.geeTestLanguage(); language != "" {
		meta["language"] = language
	}
	return Challenge{
		Provider:  ProviderGeeTest4,
		CaptchaID: internalID,
		Challenge: challenge,
		Scene:     scenario,
		Meta:      meta,
	}, nil
}

func (p *GeeTest4Provider) verify(ctx context.Context, stored StoredChallenge, cfg ProviderConfig, solution ChallengeSolution) (bool, error) {
	captchaID := strings.TrimSpace(cfg.CaptchaID())
	apiKey := strings.TrimSpace(cfg.APIKey())
	if captchaID == "" || len(captchaID) > maxGeeTestCaptchaIDBytes || apiKey == "" || len(apiKey) > maxGeeTestAPIKeyBytes {
		return false, apperrors.New(consts.StatusServiceUnavailable, "GeeTest v4 is not configured")
	}
	if stored.Provider != "" && stored.Provider != ProviderGeeTest4 {
		return false, apperrors.New(consts.StatusBadRequest, "captcha provider mismatch")
	}
	lotNumber := boundedConfigString(solution.LotNumber, 512)
	captchaOutput := boundedConfigString(solution.CaptchaOutput, 4096)
	passToken := boundedConfigString(solution.PassToken, 4096)
	genTime := boundedConfigString(solution.GenTime, 64)
	if lotNumber == "" || captchaOutput == "" || passToken == "" || genTime == "" {
		return false, apperrors.New(consts.StatusBadRequest, "GeeTest verification response is incomplete")
	}
	form := url.Values{}
	form.Set("captcha_id", captchaID)
	form.Set("lot_number", lotNumber)
	form.Set("captcha_output", captchaOutput)
	form.Set("pass_token", passToken)
	form.Set("gen_time", genTime)
	form.Set("sign_token", geeTestSignToken(apiKey, lotNumber))
	return p.postForm(ctx, cfg, form)
}

func geeTestSignToken(apiKey, lotNumber string) string {
	mac := hmac.New(sha256.New, []byte(apiKey))
	_, _ = mac.Write([]byte(lotNumber))
	return hex.EncodeToString(mac.Sum(nil))
}

func (p *GeeTest4Provider) postForm(ctx context.Context, cfg ProviderConfig, form url.Values) (bool, error) {
	if p == nil || p.client == nil {
		return false, apperrors.New(consts.StatusServiceUnavailable, "GeeTest verification service is not configured")
	}
	endpoint := strings.TrimSpace(p.endpoint)
	if err := httpclient.ValidateURL(endpoint); err != nil {
		return false, providerRequestError("GeeTest", err)
	}
	requestCtx, cancel, err := captchaContext(ctx, configuredCaptchaTimeout(cfg))
	if err != nil {
		return false, providerRequestError("GeeTest", err)
	}
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, providerRequestError("GeeTest", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return false, providerRequestError("GeeTest", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return false, providerResponseError("GeeTest", response.StatusCode)
	}
	body, err := httpclient.ReadBody(response.Body, maxCaptchaProviderBody)
	if err != nil {
		return false, providerRequestError("GeeTest", err)
	}
	var result struct {
		Result string `json:"result"`
		Reason string `json:"reason"`
	}
	if err := jsonrequest.UnmarshalUnambiguous(body, &result, jsonrequest.DefaultMaxDepth); err != nil {
		return false, providerRequestError("GeeTest", err)
	}
	if strings.EqualFold(strings.TrimSpace(result.Result), "success") {
		return true, nil
	}
	return false, apperrors.New(consts.StatusBadRequest, "GeeTest verification failed")
}

func (p *CloudflareTurnstileProvider) issue(ctx context.Context, scenario string, cfg ProviderConfig, _ string) (Challenge, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Challenge{}, providerRequestError("Cloudflare Turnstile", err)
	}
	siteKey := strings.TrimSpace(cfg.SiteKeyValue())
	secret := strings.TrimSpace(cfg.SecretKey())
	if siteKey == "" || len(siteKey) > maxTurnstileSiteKeyBytes || secret == "" || len(secret) > maxTurnstileSecretBytes {
		return Challenge{}, apperrors.New(consts.StatusServiceUnavailable, "Cloudflare Turnstile is not configured")
	}
	internalID, err := randomHex(16)
	if err != nil {
		return Challenge{}, providerRequestError("Cloudflare Turnstile", err)
	}
	challenge, err := randomHex(16)
	if err != nil {
		return Challenge{}, providerRequestError("Cloudflare Turnstile", err)
	}
	action, err := cfg.cloudflareAction(scenario)
	if err != nil {
		return Challenge{}, err
	}
	cData, err := cfg.cloudflareCData(internalID)
	if err != nil {
		return Challenge{}, err
	}
	if _, err := cfg.cloudflareHostname(); err != nil {
		return Challenge{}, err
	}
	meta := map[string]any{
		"action":  action,
		"cData":   cData,
		"siteKey": siteKey,
	}
	return Challenge{
		Provider:  ProviderCloudflare,
		CaptchaID: internalID,
		Challenge: challenge,
		Scene:     scenario,
		Meta:      meta,
	}, nil
}

func (p *CloudflareTurnstileProvider) verify(ctx context.Context, stored StoredChallenge, cfg ProviderConfig, solution ChallengeSolution) (bool, error) {
	siteKey := strings.TrimSpace(cfg.SiteKeyValue())
	secret := strings.TrimSpace(cfg.SecretKey())
	if siteKey == "" || len(siteKey) > maxTurnstileSiteKeyBytes || secret == "" || len(secret) > maxTurnstileSecretBytes {
		return false, apperrors.New(consts.StatusServiceUnavailable, "Cloudflare Turnstile is not configured")
	}
	if stored.Provider != "" && stored.Provider != ProviderCloudflare {
		return false, apperrors.New(consts.StatusBadRequest, "captcha provider mismatch")
	}
	token := boundedConfigString(solution.Token, maxTurnstileTokenBytes)
	if token == "" {
		return false, apperrors.New(consts.StatusBadRequest, "Cloudflare Turnstile token is required")
	}
	action, err := cfg.cloudflareAction(stored.Scene)
	if err != nil {
		return false, err
	}
	cData, err := cfg.cloudflareCData(stored.CaptchaID)
	if err != nil {
		return false, err
	}
	hostname, err := cfg.cloudflareHostname()
	if err != nil {
		return false, err
	}
	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", token)
	if remoteIP := net.ParseIP(strings.TrimSpace(solution.RemoteIP)); remoteIP != nil {
		form.Set("remoteip", remoteIP.String())
	}
	return p.postForm(ctx, cfg, form, action, cData, hostname)
}

func (p *CloudflareTurnstileProvider) postForm(ctx context.Context, cfg ProviderConfig, form url.Values, expectedAction, expectedCData, expectedHostname string) (bool, error) {
	if p == nil || p.client == nil {
		return false, apperrors.New(consts.StatusServiceUnavailable, "Cloudflare Turnstile verification service is not configured")
	}
	endpoint := strings.TrimSpace(p.endpoint)
	if err := httpclient.ValidateURL(endpoint); err != nil {
		return false, providerRequestError("Cloudflare Turnstile", err)
	}
	requestCtx, cancel, err := captchaContext(ctx, configuredCaptchaTimeout(cfg))
	if err != nil {
		return false, providerRequestError("Cloudflare Turnstile", err)
	}
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, providerRequestError("Cloudflare Turnstile", err)
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := p.client.Do(request)
	if err != nil {
		return false, providerRequestError("Cloudflare Turnstile", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return false, providerResponseError("Cloudflare Turnstile", response.StatusCode)
	}
	body, err := httpclient.ReadBody(response.Body, maxCaptchaProviderBody)
	if err != nil {
		return false, providerRequestError("Cloudflare Turnstile", err)
	}
	var result struct {
		Success  bool     `json:"success"`
		Action   string   `json:"action"`
		Hostname string   `json:"hostname"`
		CData    string   `json:"cdata"`
		Errors   []string `json:"error-codes"`
	}
	if err := jsonrequest.UnmarshalUnambiguous(body, &result, jsonrequest.DefaultMaxDepth); err != nil {
		return false, providerRequestError("Cloudflare Turnstile", err)
	}
	if !result.Success {
		for _, code := range result.Errors {
			switch strings.TrimSpace(code) {
			case "internal-error", "invalid-input-secret", "missing-input-secret":
				return false, apperrors.New(consts.StatusServiceUnavailable, "Cloudflare Turnstile verification service is temporarily unavailable")
			}
		}
		return false, apperrors.New(consts.StatusBadRequest, "Cloudflare Turnstile verification failed")
	}
	if result.Action != expectedAction {
		return false, apperrors.New(consts.StatusBadRequest, "Cloudflare Turnstile action mismatch")
	}
	if expectedHostname != "" && !strings.EqualFold(result.Hostname, expectedHostname) {
		return false, apperrors.New(consts.StatusBadRequest, "Cloudflare Turnstile hostname mismatch")
	}
	if result.CData != expectedCData {
		return false, apperrors.New(consts.StatusBadRequest, "Cloudflare Turnstile cData mismatch")
	}
	return true, nil
}
