package auth

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bugpk-api/internal/config"

	"github.com/redis/go-redis/v9"
)

func TestCaptchaConsumeRedisIntegration(t *testing.T) {
	client := captchaRedisIntegrationClient(t)
	service := &CaptchaService{
		cfg:  config.CaptchaConfig{DefaultProvider: ProviderPOW, TTL: time.Minute, PowDifficulty: 3},
		repo: staticCaptchaConfigRepository{provider: ProviderPOW},
		rdb:  client,
	}
	ctx := context.Background()

	t.Run("verification preserves ttl and can be consumed once", func(t *testing.T) {
		captchaID := "captcha-integration-verify"
		stored := storedCaptcha{
			Provider: "pow", Scene: "login", Challenge: "challenge", Difficulty: 1,
			Binding: "binding",
		}
		storeCaptchaForIntegration(t, client, captchaID, stored)
		beforeTTL := client.PTTL(ctx, captchaKey(captchaID)).Val()
		proof := ""
		for value := 0; value < 100_000; value++ {
			candidate := base36(value)
			if validPowProof(captchaID, stored, "1.2", candidate) {
				proof = candidate
				break
			}
		}
		if proof == "" {
			t.Fatal("failed to find captcha proof")
		}
		if err := service.Verify(ctx, captchaID, CaptchaVerifyRequest{Proof: proof, Time: "1.2"}, "binding"); err != nil {
			t.Fatalf("verify failed: %v", err)
		}
		if err := service.Verify(ctx, captchaID, CaptchaVerifyRequest{Proof: proof, Time: "1.2"}, "binding"); err != nil {
			t.Fatalf("idempotent verify retry failed: %v", err)
		}
		afterTTL := client.PTTL(ctx, captchaKey(captchaID)).Val()
		if afterTTL <= 0 || afterTTL > beforeTTL {
			t.Fatalf("captcha ttl changed from %v to %v", beforeTTL, afterTTL)
		}
		if err := service.Consume(ctx, captchaID, "login", "binding"); err != nil {
			t.Fatalf("consume after verify failed: %v", err)
		}
		if err := service.Consume(ctx, captchaID, "login", "binding"); err == nil {
			t.Fatal("captcha was consumed more than once")
		}
	})

	t.Run("invalid binding does not destroy a verified captcha", func(t *testing.T) {
		captchaID := "captcha-integration-binding"
		storeCaptchaForIntegration(t, client, captchaID, storedCaptcha{
			Provider: "pow", Scene: "login", Challenge: "challenge", Difficulty: 2,
			Binding: "correct-binding", Verified: true,
		})
		if err := service.Consume(ctx, captchaID, "register", "correct-binding"); err == nil {
			t.Fatal("wrong scene unexpectedly consumed captcha")
		}
		if exists := client.Exists(ctx, captchaKey(captchaID)).Val(); exists != 1 {
			t.Fatal("wrong scene destroyed a valid captcha")
		}
		if err := service.Consume(ctx, captchaID, "login", "wrong-binding"); err == nil {
			t.Fatal("wrong binding unexpectedly consumed captcha")
		}
		if exists := client.Exists(ctx, captchaKey(captchaID)).Val(); exists != 1 {
			t.Fatal("wrong binding destroyed a valid captcha")
		}
		if err := service.Consume(ctx, captchaID, "login", "correct-binding"); err != nil {
			t.Fatalf("correct binding consume failed: %v", err)
		}
	})

	t.Run("concurrent consume succeeds once", func(t *testing.T) {
		captchaID := "captcha-integration-concurrent"
		storeCaptchaForIntegration(t, client, captchaID, storedCaptcha{
			Provider: "pow", Scene: "login", Challenge: "challenge", Difficulty: 2,
			Binding: "binding", Verified: true,
		})
		var successes atomic.Int32
		var wait sync.WaitGroup
		for range 32 {
			wait.Add(1)
			go func() {
				defer wait.Done()
				if service.Consume(context.Background(), captchaID, "login", "binding") == nil {
					successes.Add(1)
				}
			}()
		}
		wait.Wait()
		if successes.Load() != 1 {
			t.Fatalf("concurrent consume successes = %d, want 1", successes.Load())
		}
	})
}

type staticCaptchaConfigRepository struct {
	provider string
}

func (staticCaptchaConfigRepository) CaptchaGlobalEnabled(context.Context) (bool, error) {
	return true, nil
}

func (r staticCaptchaConfigRepository) CaptchaProviderConfig(_ context.Context, scene, fallback string) (CaptchaProviderConfig, error) {
	provider := r.provider
	if provider == "" {
		provider = fallback
	}
	return CaptchaProviderConfig{
		ProviderKey: provider,
		Scene:       scene,
		Enabled:     true,
		TimeoutMS:   3000,
		ExtraConfig: `{"difficulty":3}`,
	}, nil
}

func TestCaptchaVerifyCASDoesNotReviveDeletedValueRedisIntegration(t *testing.T) {
	client := captchaRedisIntegrationClient(t)
	ctx := context.Background()
	captchaID := "captcha-integration-no-revive"
	key := captchaKey(captchaID)
	stored := storedCaptcha{
		Provider: "pow", Scene: "login", Challenge: "challenge", Difficulty: 2,
		Binding: "binding", Verified: false,
	}
	raw := marshalStoredCaptcha(t, stored)
	if err := client.Set(ctx, key, raw, time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.Del(ctx, key).Err(); err != nil {
		t.Fatal(err)
	}
	stored.Verified = true
	updated, err := verifyCaptchaScript.Run(ctx, client, []string{key}, raw, marshalStoredCaptcha(t, stored)).Int64()
	if err != nil {
		t.Fatal(err)
	}
	if updated != 0 || client.Exists(ctx, key).Val() != 0 {
		t.Fatal("stale verification revived a deleted captcha")
	}
}

func captchaRedisIntegrationClient(t *testing.T) *redis.Client {
	t.Helper()
	if os.Getenv("AUTH_REDIS_INTEGRATION") != "1" {
		t.Skip("set AUTH_REDIS_INTEGRATION=1 to run")
	}
	cfg, err := config.Load("")
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(&redis.Options{
		Addr: cfg.Redis.Addr(), DB: cfg.Redis.Database, Password: cfg.Redis.Password,
	})
	if err := client.Ping(context.Background()).Err(); err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func storeCaptchaForIntegration(t *testing.T, client *redis.Client, captchaID string, stored storedCaptcha) {
	t.Helper()
	key := captchaKey(captchaID)
	if err := client.Set(context.Background(), key, marshalStoredCaptcha(t, stored), time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Del(context.Background(), key).Err() })
}

func marshalStoredCaptcha(t *testing.T, stored storedCaptcha) []byte {
	t.Helper()
	data, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
