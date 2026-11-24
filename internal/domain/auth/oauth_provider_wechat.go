package auth

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const (
	wechatAuthorizeEndpoint = "https://open.weixin.qq.com/connect/qrconnect"
	wechatTokenEndpoint     = "https://api.weixin.qq.com/sns/oauth2/access_token"
	wechatUserInfoEndpoint  = "https://api.weixin.qq.com/sns/userinfo"
	wechatDefaultScopes     = "snsapi_login"
)

type wechatOAuthDriver struct{}

func (wechatOAuthDriver) Key() string { return OAuthDriverWeChat }

func (wechatOAuthDriver) DefaultScopes() string { return wechatDefaultScopes }

func (wechatOAuthDriver) AuthorizeURL(cfg OAuthProviderConfig, state, codeVerifier, nonce string) (string, error) {
	endpoint := strings.TrimSpace(cfg.AuthorizationEndpoint)
	if endpoint == "" {
		endpoint = wechatAuthorizeEndpoint + "#wechat_redirect"
	}
	params := url.Values{
		"appid":         {cfg.ClientID},
		"redirect_uri":  {cfg.RedirectURI},
		"response_type": {"code"},
		"scope":         {oauthScopesOrDefault(cfg.Scopes, wechatDefaultScopes)},
		"state":         {state},
	}
	return oauthBuildAuthorizeURL(endpoint, params)
}

func (wechatOAuthDriver) Exchange(ctx context.Context, client *http.Client, cfg OAuthProviderConfig, code, codeVerifier string) (OAuthToken, error) {
	endpoint := strings.TrimSpace(cfg.TokenEndpoint)
	if endpoint == "" {
		endpoint = wechatTokenEndpoint
	}
	query := url.Values{
		"appid":      {cfg.ClientID},
		"secret":     {cfg.ClientSecret},
		"code":       {code},
		"grant_type": {"authorization_code"},
	}
	var payload struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		OpenID      string `json:"openid"`
		UnionID     string `json:"unionid"`
		ErrCode     int    `json:"errcode"`
		ErrMsg      string `json:"errmsg"`
	}
	if err := oauthProviderRequest(ctx, client, http.MethodGet, endpoint+"?"+query.Encode(), nil, nil, &payload); err != nil {
		return OAuthToken{}, err
	}
	if payload.ErrCode != 0 {
		return OAuthToken{}, fmt.Errorf("wechat oauth error %d: %s", payload.ErrCode, payload.ErrMsg)
	}
	if strings.TrimSpace(payload.AccessToken) == "" {
		return OAuthToken{}, fmt.Errorf("wechat oauth response missing access_token")
	}
	return OAuthToken{
		AccessToken: payload.AccessToken,
		TokenType:   "bearer",
		OpenID:      strings.TrimSpace(payload.OpenID),
		ExpiresIn:   payload.ExpiresIn,
	}, nil
}

func (wechatOAuthDriver) FetchIdentity(ctx context.Context, client *http.Client, cfg OAuthProviderConfig, token OAuthToken) (ExternalIdentity, error) {
	openID := strings.TrimSpace(token.OpenID)
	if openID == "" {
		return ExternalIdentity{}, fmt.Errorf("wechat openid is missing")
	}
	endpoint := strings.TrimSpace(cfg.UserinfoEndpoint)
	if endpoint == "" {
		endpoint = wechatUserInfoEndpoint
	}
	query := url.Values{
		"access_token": {token.AccessToken},
		"openid":       {openID},
		"lang":         {"zh_CN"},
	}
	var payload struct {
		OpenID   string `json:"openid"`
		UnionID  string `json:"unionid"`
		Nickname string `json:"nickname"`
		HeadImg  string `json:"headimgurl"`
		ErrCode  int    `json:"errcode"`
		ErrMsg   string `json:"errmsg"`
	}
	if err := oauthProviderRequest(ctx, client, http.MethodGet, endpoint+"?"+query.Encode(), nil, nil, &payload); err != nil {
		return ExternalIdentity{}, err
	}
	if payload.ErrCode != 0 {
		return ExternalIdentity{}, fmt.Errorf("wechat userinfo error %d: %s", payload.ErrCode, payload.ErrMsg)
	}
	subject := strings.TrimSpace(payload.OpenID)
	if subject == "" {
		subject = openID
	}
	return ExternalIdentity{
		Subject:   subject,
		UnionID:   strings.TrimSpace(payload.UnionID),
		Nickname:  strings.TrimSpace(payload.Nickname),
		Avatar:    oauthNormalizeAvatar(payload.HeadImg, "", 0),
		Active:    true,
		ActiveSet: true,
	}, nil
}
