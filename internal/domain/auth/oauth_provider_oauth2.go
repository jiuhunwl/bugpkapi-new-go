package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type genericOAuth2Driver struct{}

func (genericOAuth2Driver) Key() string { return OAuthDriverOAuth2 }

func (genericOAuth2Driver) DefaultScopes() string { return "" }

func (genericOAuth2Driver) AuthorizeURL(cfg OAuthProviderConfig, state, codeVerifier, nonce string) (string, error) {
	mapping := parseOAuthGenericMapping(cfg.ExtraConfig)
	params := url.Values{
		"response_type": {"code"},
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {cfg.RedirectURI},
		"state":         {state},
	}
	if scope := strings.TrimSpace(cfg.Scopes); scope != "" {
		params.Set("scope", scope)
	}
	if mapping.PKCE && codeVerifier != "" {
		params.Set("code_challenge", oauthPKCEChallenge(codeVerifier))
		params.Set("code_challenge_method", "S256")
	}
	if nonce != "" {
		params.Set("nonce", nonce)
	}
	return oauthBuildAuthorizeURL(cfg.AuthorizationEndpoint, params)
}

func (genericOAuth2Driver) Exchange(ctx context.Context, client *http.Client, cfg OAuthProviderConfig, code, codeVerifier string) (OAuthToken, error) {
	mapping := parseOAuthGenericMapping(cfg.ExtraConfig)
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {cfg.RedirectURI},
	}
	if mapping.PKCE && codeVerifier != "" {
		form.Set("code_verifier", codeVerifier)
	}
	header := http.Header{}
	if mapping.TokenAuthStyle == "client_secret_basic" {
		credentials := base64.StdEncoding.EncodeToString([]byte(cfg.ClientID + ":" + cfg.ClientSecret))
		header.Set("Authorization", "Basic "+credentials)
	} else {
		form.Set("client_id", cfg.ClientID)
		form.Set("client_secret", cfg.ClientSecret)
	}
	var payload struct {
		AccessToken      string `json:"access_token"`
		TokenType        string `json:"token_type"`
		IDToken          string `json:"id_token"`
		ExpiresIn        int    `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := oauthProviderRequest(ctx, client, http.MethodPost, cfg.TokenEndpoint, header, form, &payload); err != nil {
		return OAuthToken{}, err
	}
	if payload.Error != "" {
		return OAuthToken{}, fmt.Errorf("oauth token error: %s", payload.Error)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return OAuthToken{}, fmt.Errorf("oauth token response missing access_token")
	}
	return OAuthToken{
		AccessToken: payload.AccessToken,
		TokenType:   payload.TokenType,
		IDToken:     payload.IDToken,
		ExpiresIn:   payload.ExpiresIn,
	}, nil
}

func (genericOAuth2Driver) FetchIdentity(ctx context.Context, client *http.Client, cfg OAuthProviderConfig, token OAuthToken) (ExternalIdentity, error) {
	mapping := parseOAuthGenericMapping(cfg.ExtraConfig)
	endpoint := strings.TrimSpace(cfg.UserinfoEndpoint)
	if endpoint == "" {
		return ExternalIdentity{}, fmt.Errorf("userinfo endpoint is not configured")
	}
	header := http.Header{}
	if mapping.UserinfoAuthStyle == "query" {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return ExternalIdentity{}, err
		}
		query := parsed.Query()
		query.Set("access_token", token.AccessToken)
		parsed.RawQuery = query.Encode()
		endpoint = parsed.String()
	} else {
		header.Set("Authorization", "Bearer "+token.AccessToken)
	}
	var payload map[string]any
	if err := oauthProviderRequest(ctx, client, http.MethodGet, endpoint, header, nil, &payload); err != nil {
		return ExternalIdentity{}, err
	}
	subject := oauthMapString(payload, mapping.SubjectField)
	if subject == "" {
		return ExternalIdentity{}, fmt.Errorf("userinfo response missing subject")
	}
	identity := ExternalIdentity{
		Subject:          subject,
		UnionID:          oauthMapString(payload, "unionid"),
		Username:         oauthMapString(payload, mapping.UsernameField),
		Nickname:         oauthMapString(payload, mapping.NicknameField),
		Avatar:           oauthNormalizeAvatar(oauthMapString(payload, mapping.AvatarField), cfg.Issuer, mapping.AvatarSize),
		Email:            strings.ToLower(oauthMapString(payload, mapping.EmailField)),
		TrustLevel:       oauthMapInt(payload, mapping.TrustLevelField),
		AccountCreatedAt: oauthMapTime(payload, mapping.CreatedAtField),
	}
	if verified, ok := oauthMapBool(payload, mapping.EmailVerifiedField); ok {
		identity.EmailVerified = verified
	}
	if active, ok := oauthMapBool(payload, mapping.ActiveField); ok {
		identity.Active = active
		identity.ActiveSet = true
	}
	if silenced, ok := oauthMapBool(payload, mapping.SilencedField); ok {
		identity.Silenced = silenced
	}
	if identity.Nickname == "" {
		identity.Nickname = identity.Username
	}
	return identity, nil
}

func parseOAuthJSON(raw []byte) map[string]any {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}
	return payload
}
