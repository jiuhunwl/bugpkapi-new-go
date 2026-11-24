package redeem

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"strconv"
	"time"

	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/redisutil"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
)

// Guard 是兑换防刷频控（镜像 auth 验证码频控口径）：
//   - 阈值存 DB 单例 bp_redeem_guard_config，每次兑换前读取；
//   - 计数走 Redis Lua 原子脚本（INCR+PEXPIRE 同脚本，防「INCR 后崩溃键永存」）；
//   - Redis 故障 fail-closed（503），绝不放行——缓存故障期间的放行等于
//     给撞码/滥刷加速；
//   - IP / 账号标识用 HMAC-SHA256 摘要，不明文落 Redis；
//   - 成功与失败都计数（先 INCR 后比较，尝试即计数）。
type Guard struct {
	rdb       *redis.Client
	digestKey []byte
}

// guardConfig 是频控阈值。任一项 <=0 表示该档不限。
type guardConfig struct {
	IPMinuteLimit   int
	IPHourLimit     int
	IPDayLimit      int
	UserMinuteLimit int
}

func NewGuard(rdb *redis.Client, secret string) *Guard {
	seed := sha256.Sum256([]byte("BugPk-Api:redeem-guard:v1\x00" + secret))
	return &Guard{rdb: rdb, digestKey: seed[:]}
}

// loadConfig 读取单例频控配置。行缺失时返回默认值（与迁移种子一致），
// 查询失败返回错误由调用方 fail-closed。
func (g *Guard) loadConfig(ctx context.Context, db *sql.DB) (guardConfig, error) {
	cfg := guardConfig{IPMinuteLimit: 60, IPHourLimit: 600, IPDayLimit: 3000, UserMinuteLimit: 20}
	err := db.QueryRowContext(ctx, `
		select ip_minute_limit, ip_hour_limit, ip_day_limit, user_minute_limit
		from bp_redeem_guard_config where id = 1
	`).Scan(&cfg.IPMinuteLimit, &cfg.IPHourLimit, &cfg.IPDayLimit, &cfg.UserMinuteLimit)
	if err != nil {
		if err == sql.ErrNoRows {
			return cfg, nil
		}
		return guardConfig{}, err
	}
	return cfg, nil
}

// check 按「账号分钟 → IP 分钟 → IP 小时 → IP 天」顺序检查并计数。
// 任一档超限返回 429；Redis 故障返回 503（fail-closed）。
func (g *Guard) check(ctx context.Context, db *sql.DB, userID int64, clientIP string) error {
	cfg, err := g.loadConfig(ctx, db)
	if err != nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试", err)
	}
	if g.rdb == nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试", errGuardUnavailable)
	}
	userDigest := g.digest("user:" + strconv.FormatInt(userID, 10))
	if cfg.UserMinuteLimit > 0 {
		if err := g.hit(ctx, "redeem:guard:user:minute:"+userDigest, cfg.UserMinuteLimit, time.Minute); err != nil {
			return err
		}
	}
	if clientIP != "" {
		ipDigest := g.digest("ip:" + clientIP)
		if cfg.IPMinuteLimit > 0 {
			if err := g.hit(ctx, "redeem:guard:ip:minute:"+ipDigest, cfg.IPMinuteLimit, time.Minute); err != nil {
				return err
			}
		}
		if cfg.IPHourLimit > 0 {
			if err := g.hit(ctx, "redeem:guard:ip:hour:"+ipDigest, cfg.IPHourLimit, time.Hour); err != nil {
				return err
			}
		}
		if cfg.IPDayLimit > 0 {
			if err := g.hit(ctx, "redeem:guard:ip:day:"+ipDigest, cfg.IPDayLimit, 24*time.Hour); err != nil {
				return err
			}
		}
	}
	return nil
}

// hit 对单档执行原子计数并比较。计数失败（Redis 故障）fail-closed 503。
func (g *Guard) hit(ctx context.Context, key string, limit int, ttl time.Duration) error {
	current, err := redisutil.IncrWithTTL(ctx, g.rdb, key, ttl)
	if err != nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "服务繁忙，请稍后重试", err)
	}
	if current > int64(limit) {
		return apperrors.New(consts.StatusTooManyRequests, "操作过于频繁，请稍后再试")
	}
	return nil
}

func (g *Guard) digest(identity string) string {
	mac := hmac.New(sha256.New, g.digestKey)
	mac.Write([]byte(identity))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}
