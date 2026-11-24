package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"bugpk-api/internal/config"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

func newRefreshRotationTestService(t *testing.T, grace time.Duration) (*Service, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cfg := config.Default().Auth
	cfg.RefreshRotationGrace = grace
	return &Service{cfg: cfg, rdb: client, log: zap.NewNop()}, mr
}

func TestRefreshRotationGraceWindow(t *testing.T) {
	service, mr := newRefreshRotationTestService(t, 60*time.Second)
	ctx := context.Background()
	const jti = "grace-window-jti"
	expiresAt := time.Now().Add(10 * time.Minute)

	if claim, err := service.claimRefreshRotation(ctx, jti, expiresAt); err != nil || claim != refreshRotationClaimed {
		t.Fatalf("first claim = %v, err = %v", claim, err)
	}
	if claim, err := service.claimRefreshRotation(ctx, jti, expiresAt); err != nil || claim != refreshRotationReplayed {
		t.Fatalf("replay inside grace window = %v, err = %v", claim, err)
	}
	mr.FastForward(61 * time.Second)
	if claim, err := service.claimRefreshRotation(ctx, jti, expiresAt); err != nil || claim != refreshRotationDenied {
		t.Fatalf("replay after grace window = %v, err = %v", claim, err)
	}
}

func TestRefreshRotationGraceDisabled(t *testing.T) {
	service, _ := newRefreshRotationTestService(t, 0)
	ctx := context.Background()
	const jti = "grace-disabled-jti"
	expiresAt := time.Now().Add(10 * time.Minute)

	if claim, err := service.claimRefreshRotation(ctx, jti, expiresAt); err != nil || claim != refreshRotationClaimed {
		t.Fatalf("first claim = %v, err = %v", claim, err)
	}
	if claim, err := service.claimRefreshRotation(ctx, jti, expiresAt); err != nil || claim != refreshRotationDenied {
		t.Fatalf("second claim with grace disabled = %v, err = %v", claim, err)
	}
}

func TestRefreshRotationGraceRejectsLogoutRevocation(t *testing.T) {
	service, mr := newRefreshRotationTestService(t, 60*time.Second)
	ctx := context.Background()
	const jti = "grace-logout-jti"
	expiresAt := time.Now().Add(10 * time.Minute)

	if claim, err := service.claimRefreshRotation(ctx, jti, expiresAt); err != nil || claim != refreshRotationClaimed {
		t.Fatalf("first claim = %v, err = %v", claim, err)
	}
	// 登出会把轮换标记改写成不可复用的值；宽限窗口内的其他标签页
	// 也必须立刻失效，不能因为 grace key 还在就继续补发 access token。
	if err := mr.Set(refreshRevokedKey(jti), "1"); err != nil {
		t.Fatal(err)
	}
	if claim, err := service.claimRefreshRotation(ctx, jti, expiresAt); err != nil || claim != refreshRotationDenied {
		t.Fatalf("claim after logout = %v, err = %v", claim, err)
	}
}

func TestRestoreRefreshRotationClearsGraceWindow(t *testing.T) {
	service, mr := newRefreshRotationTestService(t, 60*time.Second)
	ctx := context.Background()
	const jti = "grace-restore-jti"
	expiresAt := time.Now().Add(10 * time.Minute)

	if claim, err := service.claimRefreshRotation(ctx, jti, expiresAt); err != nil || claim != refreshRotationClaimed {
		t.Fatalf("first claim = %v, err = %v", claim, err)
	}
	if err := service.restoreRefreshRotationAfterFailure(jti, errors.New("database unavailable")); err == nil {
		t.Fatal("restore returned nil")
	}
	if mr.Exists(refreshRotationGraceKey(jti)) {
		t.Fatal("restore left the rotation grace marker behind")
	}
	if claim, err := service.claimRefreshRotation(ctx, jti, expiresAt); err != nil || claim != refreshRotationClaimed {
		t.Fatalf("claim after restore = %v, err = %v", claim, err)
	}
}
