package user

import (
	"context"
	"strings"
	"testing"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

func TestOwnPasswordUpdateOnlyChangesCredentialState(t *testing.T) {
	statement := strings.ToLower(updateOwnPasswordSQL)
	for _, required := range []string{"password_hash", "token_version = token_version + 1", "where id = ?"} {
		if !strings.Contains(statement, required) {
			t.Fatalf("own-password update lost required clause %q: %s", required, statement)
		}
	}
	for _, forbidden := range []string{"balance", "points", "role", "status", "username", "display_name", "email", "mobile", "home_path"} {
		if strings.Contains(statement, forbidden) {
			t.Fatalf("own-password update unexpectedly touches %q: %s", forbidden, statement)
		}
	}
}

func TestPasswordChangeFailureAccountingFailsClosedWhenRedisIsUnavailable(t *testing.T) {
	client := redis.NewClient(&redis.Options{
		Addr:         "127.0.0.1:1",
		DialTimeout:  20 * time.Millisecond,
		MaxRetries:   -1,
		ReadTimeout:  20 * time.Millisecond,
		WriteTimeout: 20 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	service := &Service{rdb: client}

	err := service.recordPasswordChangeFailure(context.Background(), 1)
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusServiceUnavailable {
		t.Fatalf("recordPasswordChangeFailure() error = %v, want service unavailable", err)
	}
}

func TestSendBindMobileFailsBeforeRepositoryLookupWhenSMSIsUnavailable(t *testing.T) {
	service := &Service{}
	err := service.SendBindMobileCode(context.Background(), Principal{ID: 1}, SendBindMobileCodeRequest{
		Mobile: "13800138000",
	}, "127.0.0.1")
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusBadRequest || business.Message != "短信验证码服务未配置" {
		t.Fatalf("SendBindMobileCode() error = %v, want unavailable SMS error", err)
	}
}

func TestBindMobileFailsBeforeVerificationWhenSMSIsUnavailable(t *testing.T) {
	service := &Service{}
	_, err := service.BindMobile(context.Background(), Principal{ID: 1}, BindMobileRequest{
		Code:   "123456",
		Mobile: "13800138000",
	})
	business, ok := apperrors.AsBusinessError(err)
	if !ok || business.StatusCode != consts.StatusBadRequest || business.Message != "短信验证码服务未配置" {
		t.Fatalf("BindMobile() error = %v, want unavailable SMS error", err)
	}
}
