package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	githubAuthorizeEndpoint = "https://github.com/login/oauth/authorize"
	githubTokenEndpoint     = "https://github.com/login/oauth/access_token"
	githubUserEndpoint      = "https://api.github.com/user"
	githubEmailsEndpoint    = "https://api.github.com/user/emails"
	githubDefaultScopes     = "read:user user:email"
)

type githubOAuthDriver struct{}

func (githubOAuthDriver) Key() string { return OAuthDriverGitHub }

func (githubOAuthDriver) DefaultScopes() string { return githubDefaultScopes }

func (githubOAuthDriver) AuthorizeURL(cfg OAuthProviderConfig, state, codeVerifier, nonce string) (string, error) {
	endpoint := strings.TrimSpace(cfg.AuthorizationEndpoint)
	if endpoint == "" {
		endpoint = githubAuthorizeEndpoint
	}
	params := url.Values{
		"client_id":    {cfg.ClientID},
		"redirect_uri": {cfg.RedirectURI},
		"state":        {state},
		"scope":        {oauthScopesOrDefault(cfg.Scopes, githubDefaultScopes)},
	}
	if codeVerifier != "" {
		params.Set("code_challenge", oauthPKCEChallenge(codeVerifier))
		params.Set("code_challenge_method", "S256")
	}
	return oauthBuildAuthorizeURL(endpoint, params)
}

func (githubOAuthDriver) Exchange(ctx context.Context, client *http.Client, cfg OAuthProviderConfig, code, codeVerifier string) (OAuthToken, error) {
	endpoint := strings.TrimSpace(cfg.TokenEndpoint)
	if endpoint == "" {
		endpoint = githubTokenEndpoint
	}
	form := url.Values{
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {cfg.RedirectURI},
	}
	if codeVerifier != "" {
		form.Set("code_verifier", codeVerifier)
	}
	var payload struct {
		AccessToken      string `json:"access_token"`
		TokenType        string `json:"token_type"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	header := http.Header{"Accept": {"application/vnd.github+json"}}
	if err := oauthProviderRequest(ctx, client, http.MethodPost, endpoint, header, form, &payload); err != nil {
		return OAuthToken{}, err
	}
	if payload.Error != "" {
		return OAuthToken{}, fmt.Errorf("github oauth error: %s", payload.Error)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return OAuthToken{}, fmt.Errorf("github oauth response missing access_token")
	}
	return OAuthToken{AccessToken: payload.AccessToken, TokenType: payload.TokenType}, nil
}

func (githubOAuthDriver) FetchIdentity(ctx context.Context, client *http.Client, cfg OAuthProviderConfig, token OAuthToken) (ExternalIdentity, error) {
	endpoint := strings.TrimSpace(cfg.UserinfoEndpoint)
	if endpoint == "" {
		endpoint = githubUserEndpoint
	}
	var profile struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		AvatarURL string `json:"avatar_url"`
		Email     string `json:"email"`
		CreatedAt string `json:"created_at"`
	}
	header := http.Header{
		"Authorization": {"Bearer " + token.AccessToken},
		"Accept":        {"application/vnd.github+json"},
	}
	if err := oauthProviderRequest(ctx, client, http.MethodGet, endpoint, header, nil, &profile); err != nil {
		return ExternalIdentity{}, err
	}
	if profile.ID <= 0 {
		return ExternalIdentity{}, fmt.Errorf("github profile missing id")
	}
	identity := ExternalIdentity{
		Subject:          strconv.FormatInt(profile.ID, 10),
		Username:         strings.TrimSpace(profile.Login),
		Nickname:         strings.TrimSpace(profile.Name),
		Avatar:           oauthNormalizeAvatar(profile.AvatarURL, "", 0),
		AccountCreatedAt: parseOAuthTime(profile.CreatedAt),
		Active:           true,
		ActiveSet:        true,
	}
	if identity.Nickname == "" {
		identity.Nickname = identity.Username
	}
	if strings.TrimSpace(profile.Email) != "" {
		identity.Email = strings.ToLower(strings.TrimSpace(profile.Email))
	}
	if email, verified := fetchGitHubVerifiedEmail(ctx, client, cfg, token.AccessToken); verified {
		identity.Email = email
		identity.EmailVerified = true
	}
	return identity, nil
}

func fetchGitHubVerifiedEmail(ctx context.Context, client *http.Client, cfg OAuthProviderConfig, accessToken string) (string, bool) {
	endpoint := githubEmailsEndpoint
	var extra struct {
		EmailsEndpoint string `json:"emails_endpoint"`
	}
	if strings.TrimSpace(cfg.ExtraConfig) != "" {
		_ = json.Unmarshal([]byte(cfg.ExtraConfig), &extra)
	}
	if strings.TrimSpace(extra.EmailsEndpoint) != "" {
		endpoint = strings.TrimSpace(extra.EmailsEndpoint)
	}
	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	header := http.Header{
		"Authorization": {"Bearer " + accessToken},
		"Accept":        {"application/vnd.github+json"},
	}
	if err := oauthProviderRequest(ctx, client, http.MethodGet, endpoint, header, nil, &emails); err != nil {
		return "", false
	}
	for _, candidate := range emails {
		if candidate.Verified && candidate.Primary && strings.TrimSpace(candidate.Email) != "" {
			return strings.ToLower(strings.TrimSpace(candidate.Email)), true
		}
	}
	for _, candidate := range emails {
		if candidate.Verified && strings.TrimSpace(candidate.Email) != "" {
			return strings.ToLower(strings.TrimSpace(candidate.Email)), true
		}
	}
	return "", false
}

func parseOAuthTime(raw string) time.Time {
	value := strings.TrimSpace(raw)
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02 15:04:05"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func oauthScopesOrDefault(configured, fallback string) string {
	value := strings.TrimSpace(configured)
	if value == "" {
		return fallback
	}
	return value
}
