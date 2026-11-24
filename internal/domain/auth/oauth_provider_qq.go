package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	qqAuthorizeEndpoint = "https://graph.qq.com/oauth2.0/authorize"
	qqTokenEndpoint     = "https://graph.qq.com/oauth2.0/token"
	qqMeEndpoint        = "https://graph.qq.com/oauth2.0/me"
	qqUserInfoEndpoint  = "https://graph.qq.com/user/get_user_info"
	qqDefaultScopes     = "get_user_info"
)

type qqOAuthDriver struct{}

func (qqOAuthDriver) Key() string { return OAuthDriverQQ }

func (qqOAuthDriver) DefaultScopes() string { return qqDefaultScopes }

func (qqOAuthDriver) AuthorizeURL(cfg OAuthProviderConfig, state, codeVerifier, nonce string) (string, error) {
	endpoint := strings.TrimSpace(cfg.AuthorizationEndpoint)
	if endpoint == "" {
		endpoint = qqAuthorizeEndpoint
	}
	params := url.Values{
		"response_type": {"code"},
		"client_id":     {cfg.ClientID},
		"redirect_uri":  {cfg.RedirectURI},
		"state":         {state},
		"scope":         {oauthScopesOrDefault(cfg.Scopes, qqDefaultScopes)},
	}
	return oauthBuildAuthorizeURL(endpoint, params)
}

func (qqOAuthDriver) Exchange(ctx context.Context, client *http.Client, cfg OAuthProviderConfig, code, codeVerifier string) (OAuthToken, error) {
	endpoint := strings.TrimSpace(cfg.TokenEndpoint)
	if endpoint == "" {
		endpoint = qqTokenEndpoint
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {cfg.ClientID},
		"client_secret": {cfg.ClientSecret},
		"code":          {code},
		"redirect_uri":  {cfg.RedirectURI},
		"fmt":           {"json"},
	}
	var payload struct {
		AccessToken      string `json:"access_token"`
		ExpiresIn        int    `json:"expires_in"`
		Error            int    `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	raw, err := oauthProviderRawRequest(ctx, client, http.MethodGet, endpoint, nil, form)
	if err != nil {
		return OAuthToken{}, err
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		values, parseErr := url.ParseQuery(strings.TrimSpace(string(raw)))
		if parseErr != nil {
			return OAuthToken{}, fmt.Errorf("decode qq token response: %w", err)
		}
		payload.AccessToken = strings.TrimSpace(values.Get("access_token"))
		payload.ErrorDescription = strings.TrimSpace(values.Get("error_description"))
	}
	if payload.Error != 0 {
		return OAuthToken{}, fmt.Errorf("qq oauth error: %s", payload.ErrorDescription)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return OAuthToken{}, fmt.Errorf("qq oauth response missing access_token")
	}
	return OAuthToken{AccessToken: payload.AccessToken, TokenType: "bearer", ExpiresIn: payload.ExpiresIn}, nil
}

type qqMeResponse struct {
	ClientID         string `json:"client_id"`
	OpenID           string `json:"openid"`
	UnionID          string `json:"unionid"`
	Error            int    `json:"error"`
	ErrorDescription string `json:"error_description"`
	Code             int    `json:"code"`
	Msg              string `json:"msg"`
}

func (qqOAuthDriver) FetchIdentity(ctx context.Context, client *http.Client, cfg OAuthProviderConfig, token OAuthToken) (ExternalIdentity, error) {
	// unionid is optional and requires extra permission on QQ Connect. If the
	// permission is absent QQ returns error 100048, so fall back to a plain
	// openid lookup instead of failing the whole login.
	me, err := fetchQQMe(ctx, client, token.AccessToken, true)
	if err != nil {
		me, err = fetchQQMe(ctx, client, token.AccessToken, false)
		if err != nil {
			return ExternalIdentity{}, err
		}
	}
	openID := strings.TrimSpace(me.OpenID)
	if openID == "" {
		return ExternalIdentity{}, fmt.Errorf("qq openid response missing openid")
	}

	userInfoEndpoint := strings.TrimSpace(cfg.UserinfoEndpoint)
	if userInfoEndpoint == "" {
		userInfoEndpoint = qqUserInfoEndpoint
	}
	userQuery := url.Values{
		"access_token":       {token.AccessToken},
		"oauth_consumer_key": {cfg.ClientID},
		"openid":             {openID},
		"fmt":                {"json"},
	}
	userURL := userInfoEndpoint + "?" + userQuery.Encode()
	userRaw, err := oauthProviderRawRequest(ctx, client, http.MethodGet, userURL, nil, nil)
	if err != nil {
		return ExternalIdentity{}, err
	}
	var profile struct {
		Ret          int    `json:"ret"`
		Msg          string `json:"msg"`
		Nickname     string `json:"nickname"`
		FigureURLQQ2 string `json:"figureurl_qq_2"`
		FigureURLQQ1 string `json:"figureurl_qq_1"`
		FigureURL2   string `json:"figureurl_2"`
	}
	if err := json.Unmarshal(stripQQJSONP(userRaw), &profile); err != nil {
		return ExternalIdentity{}, fmt.Errorf("decode qq profile response: %w", err)
	}
	if profile.Ret != 0 {
		return ExternalIdentity{}, fmt.Errorf("qq profile error: %s", profile.Msg)
	}
	avatar := profile.FigureURLQQ2
	if strings.TrimSpace(avatar) == "" {
		avatar = profile.FigureURLQQ1
	}
	if strings.TrimSpace(avatar) == "" {
		avatar = profile.FigureURL2
	}
	return ExternalIdentity{
		Subject:   openID,
		UnionID:   strings.TrimSpace(me.UnionID),
		Nickname:  strings.TrimSpace(profile.Nickname),
		Avatar:    oauthNormalizeAvatar(avatar, "", 0),
		Active:    true,
		ActiveSet: true,
	}, nil
}

func fetchQQMe(ctx context.Context, client *http.Client, accessToken string, withUnionID bool) (qqMeResponse, error) {
	query := url.Values{
		"access_token": {accessToken},
		"fmt":          {"json"},
	}
	if withUnionID {
		query.Set("unionid", "1")
	}
	raw, err := oauthProviderRawRequest(ctx, client, http.MethodGet, qqMeEndpoint+"?"+query.Encode(), nil, nil)
	if err != nil {
		return qqMeResponse{}, err
	}
	var payload qqMeResponse
	if err := json.Unmarshal(stripQQJSONP(raw), &payload); err != nil {
		return qqMeResponse{}, fmt.Errorf("decode qq openid response: %w", err)
	}
	if payload.Error != 0 || payload.Code != 0 {
		message := payload.ErrorDescription
		if message == "" {
			message = payload.Msg
		}
		return qqMeResponse{}, fmt.Errorf("qq openid error: %s", message)
	}
	return payload, nil
}

func stripQQJSONP(raw []byte) []byte {
	text := strings.TrimSpace(string(raw))
	if strings.HasPrefix(text, "callback") {
		if index := strings.Index(text, "("); index >= 0 {
			text = text[index+1:]
		}
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), ";"))
		text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), ")"))
	}
	return []byte(text)
}
