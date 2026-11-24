package systemsettings

import (
	"context"
	"testing"
)

// 补齐 settingsRepositoryStub 的登录态配置两个接口方法（接口定义在 service.go）。
func (s *settingsRepositoryStub) SessionConfig(context.Context) (SessionConfig, error) {
	return s.sessionCfg, s.sessionLoadErr
}

func (s *settingsRepositoryStub) SaveSessionConfig(_ context.Context, cfg SessionConfig) error {
	s.sessionCfg = cfg
	s.sessionSaveCalled = true
	return s.sessionSaveErr
}

func TestNormalizeSessionConfigBounds(t *testing.T) {
	cases := []struct {
		name    string
		req     SessionConfig
		wantErr bool
		want    SessionConfig
	}{
		{name: "defaults", req: SessionConfig{AccessTokenMinutes: 30, RefreshTokenDays: 7}, want: SessionConfig{AccessTokenMinutes: 30, RefreshTokenDays: 7}},
		{name: "access-too-short", req: SessionConfig{AccessTokenMinutes: 4, RefreshTokenDays: 7}, wantErr: true},
		{name: "access-too-long", req: SessionConfig{AccessTokenMinutes: 24*60 + 1, RefreshTokenDays: 7}, wantErr: true},
		{name: "refresh-too-short", req: SessionConfig{AccessTokenMinutes: 30, RefreshTokenDays: 0}, wantErr: true},
		{name: "refresh-too-long", req: SessionConfig{AccessTokenMinutes: 30, RefreshTokenDays: 366}, wantErr: true},
		{name: "refresh-not-after-access", req: SessionConfig{AccessTokenMinutes: 24 * 60, RefreshTokenDays: 1}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeSessionConfig(tc.req)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("normalizeSessionConfig(%v) accepted, want error", tc.req)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeSessionConfig(%v) error = %v", tc.req, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeSessionConfig(%v) = %v, want %v", tc.req, got, tc.want)
			}
		})
	}
}

// svc 直接构造（NewService 收 *Repository，桩进不去）；权限门不在此测。
func TestSessionConfigObserverNotifiedOnNotify(t *testing.T) {
	svc := &Service{repo: &settingsRepositoryStub{}, systemPrefix: "/system-api"}
	var notified SessionConfig
	svc.SetSessionConfigObserver(func(cfg SessionConfig) { notified = cfg })
	want := SessionConfig{AccessTokenMinutes: 60, RefreshTokenDays: 14}
	svc.notifySessionConfig(want)
	if notified != want {
		t.Fatalf("observer notified %+v, want %+v", notified, want)
	}
}

// 启动装载：LoadSessionConfig 把库里的值推给观察者。
func TestLoadSessionConfigNotifiesObserver(t *testing.T) {
	repo := &settingsRepositoryStub{sessionCfg: SessionConfig{AccessTokenMinutes: 45, RefreshTokenDays: 21}}
	svc := &Service{repo: repo, systemPrefix: "/system-api"}
	var notified SessionConfig
	svc.SetSessionConfigObserver(func(cfg SessionConfig) { notified = cfg })
	if err := svc.LoadSessionConfig(context.Background()); err != nil {
		t.Fatalf("LoadSessionConfig() error = %v", err)
	}
	if notified != (SessionConfig{AccessTokenMinutes: 45, RefreshTokenDays: 21}) {
		t.Fatalf("observer notified %+v, want 45m/21d", notified)
	}
}
