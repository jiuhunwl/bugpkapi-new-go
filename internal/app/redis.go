package app

import (
	"context"
	"fmt"

	"bugpk-api/internal/config"

	"github.com/redis/go-redis/v9"
)

// OpenRedis 建立并校验 Redis 连接，供 CLI 维护命令复用在线进程的同一套参数。
// 在线服务走 view 侧缓存，命令行工具也需要缓存来避免重复打上游。
func OpenRedis(ctx context.Context, cfg config.RedisConfig) (*redis.Client, error) {
	return openRedis(ctx, cfg)
}

func openRedis(ctx context.Context, cfg config.RedisConfig) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:                  cfg.Addr(),
		Password:              cfg.Password,
		DB:                    cfg.Database,
		MaxRetries:            cfg.MaxRetries,
		MinIdleConns:          cfg.MinIdleConns,
		PoolSize:              cfg.PoolSize,
		PoolTimeout:           cfg.PoolTimeout,
		DialTimeout:           cfg.DialTimeout,
		ReadTimeout:           cfg.ReadTimeout,
		WriteTimeout:          cfg.WriteTimeout,
		ContextTimeoutEnabled: true,
	})

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("ping redis %s db=%d: %w", cfg.Addr(), cfg.Database, err)
	}

	return client, nil
}
