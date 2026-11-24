package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/pkg/httpclient"
)

type OAuthDriver interface {
	Key() string
	DefaultScopes() string
	AuthorizeURL(cfg OAuthProviderConfig, state, codeVerifier, nonce string) (string, error)
	Exchange(ctx context.Context, client *http.Client, cfg OAuthProviderConfig, code, codeVerifier string) (OAuthToken, error)
	FetchIdentity(ctx context.Context, client *http.Client, cfg OAuthProviderConfig, token OAuthToken) (ExternalIdentity, error)
}

var oauthDriverRegistry = map[string]OAuthDriver{
	OAuthDriverQQ:     qqOAuthDriver{},
	OAuthDriverWeChat: wechatOAuthDriver{},
	OAuthDriverGitHub: githubOAuthDriver{},
	OAuthDriverOAuth2: genericOAuth2Driver{},
}

func oauthDriverExists(driver string) bool {
	_, ok := oauthDriverRegistry[driver]
	return ok
}

func oauthDriverFor(driver string) (OAuthDriver, bool) {
	value, ok := oauthDriverRegistry[driver]
	return value, ok
}

type oauthProviderHTTPError struct {
	Status int
	Body   string
}

func (e *oauthProviderHTTPError) Error() string {
	return fmt.Sprintf("provider returned HTTP %d", e.Status)
}

func oauthPKCEChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func oauthBuildAuthorizeURL(endpoint string, params url.Values) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Host == "" {
		return "", errors.New("authorization endpoint is invalid")
	}
	query := parsed.Query()
	for key, values := range params {
		query.Del(key)
		for _, value := range values {
			query.Add(key, value)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func oauthProviderRawRequest(
	ctx context.Context,
	client *http.Client,
	method, endpoint string,
	header http.Header,
	form url.Values,
) ([]byte, error) {
	if client == nil {
		return nil, errors.New("oauth http client is not configured")
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	if form != nil {
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "BugPk-Api/oauth")
	for key, values := range header {
		request.Header.Del(key)
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()

	payload, err := io.ReadAll(io.LimitReader(response.Body, httpclient.MaxProviderResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) > httpclient.MaxProviderResponseBytes {
		return nil, errors.New("provider response is too large")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, &oauthProviderHTTPError{Status: response.StatusCode, Body: strings.TrimSpace(string(payload))}
	}
	return payload, nil
}

func oauthProviderRequest(
	ctx context.Context,
	client *http.Client,
	method, endpoint string,
	header http.Header,
	form url.Values,
	out any,
) error {
	payload, err := oauthProviderRawRequest(ctx, client, method, endpoint, header, form)
	if err != nil {
		return err
	}
	if out == nil || len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("decode provider response: %w", err)
	}
	return nil
}

type oauthGenericMapping struct {
	SubjectField       string `json:"subject_field"`
	UsernameField      string `json:"username_field"`
	NicknameField      string `json:"nickname_field"`
	AvatarField        string `json:"avatar_field"`
	EmailField         string `json:"email_field"`
	EmailVerifiedField string `json:"email_verified_field"`
	TrustLevelField    string `json:"trust_level_field"`
	ActiveField        string `json:"active_field"`
	SilencedField      string `json:"silenced_field"`
	CreatedAtField     string `json:"created_at_field"`
	TokenAuthStyle     string `json:"token_auth_style"`
	UserinfoAuthStyle  string `json:"userinfo_auth_style"`
	AvatarSize         int    `json:"avatar_size"`
	PKCE               bool   `json:"pkce"`
}

func parseOAuthGenericMapping(raw string) oauthGenericMapping {
	mapping := oauthGenericMapping{
		SubjectField:       "id",
		UsernameField:      "username",
		NicknameField:      "name",
		AvatarField:        "avatar_url",
		EmailField:         "email",
		EmailVerifiedField: "email_verified",
		TokenAuthStyle:     "client_secret_post",
		UserinfoAuthStyle:  "bearer",
		AvatarSize:         96,
		PKCE:               true,
	}
	if strings.TrimSpace(raw) == "" {
		return mapping
	}
	_ = json.Unmarshal([]byte(raw), &mapping)
	if mapping.SubjectField == "" {
		mapping.SubjectField = "id"
	}
	if mapping.TokenAuthStyle == "" {
		mapping.TokenAuthStyle = "client_secret_post"
	}
	if mapping.UserinfoAuthStyle == "" {
		mapping.UserinfoAuthStyle = "bearer"
	}
	if mapping.AvatarSize <= 0 {
		mapping.AvatarSize = 96
	}
	return mapping
}

func oauthMapValue(data map[string]any, path string) any {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	var current any = data
	for _, segment := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = object[segment]
		if !ok {
			return nil
		}
	}
	return current
}

func oauthMapString(data map[string]any, path string) string {
	value := oauthMapValue(data, path)
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(html.UnescapeString(typed))
	case json.Number:
		return typed.String()
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}

func oauthMapBool(data map[string]any, path string) (bool, bool) {
	value := oauthMapValue(data, path)
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed, err == nil
	case float64:
		return typed != 0, true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed != 0, err == nil
	default:
		return false, false
	}
}

func oauthMapInt(data map[string]any, path string) int {
	value := oauthMapValue(data, path)
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return int(parsed)
	case string:
		parsed, _ := strconv.Atoi(strings.TrimSpace(typed))
		return parsed
	default:
		return 0
	}
}

func oauthMapTime(data map[string]any, path string) time.Time {
	raw := oauthMapString(data, path)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func oauthNormalizeAvatar(raw, issuer string, size int) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if size > 0 {
		value = strings.ReplaceAll(value, "{size}", strconv.Itoa(size))
	}
	if strings.HasPrefix(value, "/") && strings.TrimSpace(issuer) != "" {
		if parsed, err := url.Parse(strings.TrimSpace(issuer)); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			value = parsed.Scheme + "://" + parsed.Host + value
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	if parsed.Scheme == "http" {
		parsed.Scheme = "https"
	}
	if parsed.Scheme != "https" {
		return ""
	}
	result := parsed.String()
	if len(result) > 1024 {
		return ""
	}
	return result
}

func oauthSuggestedUsername(providerKey, raw string) string {
	var builder strings.Builder
	for _, char := range strings.TrimSpace(raw) {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9', char == '_', char == '-':
			builder.WriteRune(char)
		default:
			builder.WriteByte('_')
		}
	}
	value := strings.Trim(builder.String(), "_-")
	if len(value) >= 3 && len(value) <= 32 {
		return value
	}
	suffix, err := randomHex(4)
	if err != nil {
		suffix = "00000000"
	}
	prefix := strings.TrimSpace(providerKey)
	if prefix == "" {
		prefix = "oauth"
	}
	value = prefix + "_" + suffix
	if len(value) > 32 {
		value = value[:32]
	}
	return value
}
