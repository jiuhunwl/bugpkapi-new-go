package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"bugpk-api/internal/config"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestValidateOAuthReturnPathRejectsOpenRedirects(t *testing.T) {
	valid := []string{"/console/workspace", "/profile?tab=security"}
	for _, value := range valid {
		if got := validateOAuthReturnPath(value); got != value {
			t.Fatalf("validateOAuthReturnPath(%q) = %q", value, got)
		}
	}
	invalid := []string{"//evil.example", "https://evil.example", "/\\evil.example", "javascript:alert(1)", "/ok\r\nX-Test: 1"}
	for _, value := range invalid {
		if got := validateOAuthReturnPath(value); got != "" {
			t.Fatalf("validateOAuthReturnPath(%q) = %q, want empty", value, got)
		}
	}
}

func TestOAuthSuggestedUsernameIsSafeAndBounded(t *testing.T) {
	cases := map[string]string{
		"linuxdo": "alice",
		"github":  "alice",
	}
	for provider, raw := range cases {
		got := oauthSuggestedUsername(provider, raw)
		if !usernamePattern.MatchString(got) {
			t.Fatalf("suggested username %q does not match pattern", got)
		}
	}
	got := oauthSuggestedUsername("github", "!!!")
	if !usernamePattern.MatchString(got) {
		t.Fatalf("fallback username %q does not match pattern", got)
	}
	if len(oauthSuggestedUsername("github", "a-very-long-github-login-that-is-way-too-long")) > 32 {
		t.Fatal("suggested username exceeds 32 characters")
	}
}

func TestStripQQJSONP(t *testing.T) {
	raw := []byte(`callback( {"client_id":"123","openid":"abc"} );`)
	var payload map[string]any
	if err := json.Unmarshal(stripQQJSONP(raw), &payload); err != nil {
		t.Fatalf("stripQQJSONP produced invalid JSON: %v", err)
	}
	if payload["openid"] != "abc" {
		t.Fatalf("openid = %v", payload["openid"])
	}
}

func TestGenericOAuth2LinuxDoMapping(t *testing.T) {
	mapping := oauthGenericMapping{
		SubjectField:      "id",
		UsernameField:     "username",
		NicknameField:     "name",
		AvatarField:       "avatar_template",
		TrustLevelField:   "trust_level",
		ActiveField:       "active",
		SilencedField:     "silenced",
		AvatarSize:        96,
		TokenAuthStyle:    "client_secret_post",
		UserinfoAuthStyle: "bearer",
	}
	payload := map[string]any{
		"id":              float64(124),
		"username":        "Bee",
		"name":            "Bee Name",
		"avatar_template": "/user_avatar/linux.do/bee/{size}.png",
		"trust_level":     float64(2),
		"active":          true,
		"silenced":        false,
	}
	identity := ExternalIdentity{
		Subject:    oauthMapString(payload, mapping.SubjectField),
		Username:   oauthMapString(payload, mapping.UsernameField),
		Nickname:   oauthMapString(payload, mapping.NicknameField),
		Avatar:     oauthNormalizeAvatar(oauthMapString(payload, mapping.AvatarField), "https://linux.do", mapping.AvatarSize),
		TrustLevel: oauthMapInt(payload, mapping.TrustLevelField),
		Active:     true,
		ActiveSet:  true,
	}
	if identity.Subject != "124" || identity.Username != "Bee" || identity.TrustLevel != 2 {
		t.Fatalf("unexpected mapped identity: %+v", identity)
	}
	if identity.Avatar != "https://linux.do/user_avatar/linux.do/bee/96.png" {
		t.Fatalf("avatar = %q", identity.Avatar)
	}
}

func TestGitHubDriverUsesVerifiedPrimaryEmail(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("unexpected authorization %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"id":42,"login":"octocat","name":"Octo Cat","avatar_url":"https://avatars.example/u.png","email":null,"created_at":"2020-01-02T03:04:05Z"}`))
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"email":"secondary@example.com","primary":false,"verified":true},{"email":"primary@example.com","primary":true,"verified":true}]`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	cfg := OAuthProviderConfig{
		ProviderKey:      OAuthDriverGitHub,
		UserinfoEndpoint: server.URL + "/user",
		ExtraConfig:      `{"emails_endpoint":"` + server.URL + `/user/emails"}`,
	}
	identity, err := (githubOAuthDriver{}).FetchIdentity(context.Background(), server.Client(), cfg, OAuthToken{AccessToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "42" || identity.Email != "primary@example.com" || !identity.EmailVerified {
		t.Fatalf("unexpected identity: %+v", identity)
	}
}

func TestGenericOAuth2DriverFetchesLinuxDoProfile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Fatalf("unexpected authorization %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"id":124,"username":"Bee","name":"Bee Name","avatar_template":"/user_avatar/linux.do/bee/{size}.png","active":true,"trust_level":2,"silenced":false}`))
	}))
	defer server.Close()
	cfg := OAuthProviderConfig{
		ProviderKey:      "linuxdo",
		Driver:           OAuthDriverOAuth2,
		Issuer:           "https://linux.do",
		UserinfoEndpoint: server.URL + "/api/user",
		ExtraConfig:      `{"subject_field":"id","username_field":"username","nickname_field":"name","avatar_field":"avatar_template","active_field":"active","trust_level_field":"trust_level","silenced_field":"silenced","userinfo_auth_style":"bearer","avatar_size":96,"pkce":false}`,
	}
	identity, err := (genericOAuth2Driver{}).FetchIdentity(context.Background(), server.Client(), cfg, OAuthToken{AccessToken: "token"})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "124" || identity.Username != "Bee" || identity.TrustLevel != 2 {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	if identity.Avatar != "https://linux.do/user_avatar/linux.do/bee/96.png" {
		t.Fatalf("avatar = %q", identity.Avatar)
	}
}

func TestWeChatDriverUsesTokenOpenID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("openid") != "openid-1" {
			t.Fatalf("openid query = %q", r.URL.Query().Get("openid"))
		}
		_, _ = w.Write([]byte(`{"openid":"openid-1","unionid":"union-1","nickname":"微信用户","headimgurl":"http://thirdwx.qlogo.cn/a/0"}`))
	}))
	defer server.Close()
	cfg := OAuthProviderConfig{ProviderKey: OAuthDriverWeChat, UserinfoEndpoint: server.URL}
	identity, err := (wechatOAuthDriver{}).FetchIdentity(context.Background(), server.Client(), cfg, OAuthToken{AccessToken: "token", OpenID: "openid-1"})
	if err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "openid-1" || identity.UnionID != "union-1" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	if identity.Avatar != "https://thirdwx.qlogo.cn/a/0" {
		t.Fatalf("avatar = %q", identity.Avatar)
	}
}

func TestOAuthStateConsumeIsSingleUse(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	store := newOAuthStateStore(rdb)
	ctx := context.Background()
	flow := oauthFlow{Provider: "github", Mode: OAuthModeLogin, FlowID: "flow"}
	if err := store.save(ctx, oauthStateKey("state-1"), flow, oauthStateTTL); err != nil {
		t.Fatal(err)
	}
	var first oauthFlow
	ok, err := store.consume(ctx, oauthStateKey("state-1"), &first)
	if err != nil || !ok || first.FlowID != "flow" {
		t.Fatalf("first consume = (%v, %v, %+v)", ok, err, first)
	}
	var second oauthFlow
	ok, err = store.consume(ctx, oauthStateKey("state-1"), &second)
	if err != nil || ok {
		t.Fatalf("second consume = (%v, %v)", ok, err)
	}
}

func TestValidateOAuthProviderConfigRequiresHTTPSInProduction(t *testing.T) {
	cfg := OAuthProviderConfig{
		ProviderKey:   "github",
		DisplayName:   "GitHub",
		IconKey:       "github",
		Driver:        OAuthDriverGitHub,
		ProviderGroup: "github",
		RegisterMode:  OAuthRegisterModeConfirm,
		ClientID:      "client",
		ClientSecret:  "secret",
		RedirectURI:   "http://localhost:8080/system-api/auth/oauth/github/callback",
		Enabled:       true,
	}
	if err := validateOAuthProviderConfig(&cfg, "production", true); err == nil {
		t.Fatal("production accepted an http redirect URI")
	}
	if err := validateOAuthProviderConfig(&cfg, "development", true); err != nil {
		t.Fatalf("development rejected http redirect URI: %v", err)
	}
}

func TestOAuthConfigDefaults(t *testing.T) {
	cfg := config.Default()
	if cfg.OAuth.HTTPTimeout <= 0 {
		t.Fatal("default oauth http timeout must be positive")
	}
}
