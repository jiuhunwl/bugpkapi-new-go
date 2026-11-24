package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"bugpk-api/internal/app"
	"bugpk-api/internal/config"
	"bugpk-api/internal/domain/ipconfig"
	"bugpk-api/internal/domain/ipregion"
	"bugpk-api/internal/domain/observability"
	"bugpk-api/internal/pkg/masking"

	"github.com/redis/go-redis/v9"
)

// runOperationLogRegionCommand 回填历史审计记录的 IP 归属地。
//
// 背景：bp_operation_log 的 country/province/city 三列在 2026-10 之前从未被
// 写入方填充（审计中间件只落 client_ip），因此历史记录的地区列全是空的。
// 本命令按去重后的客户端 IP 补全，复用与在线服务完全相同的解析器与判定逻辑。
//
// 默认只预览（列出待补全的 IP 数量），加 --apply 才真正写库；写入是幂等的
// （仅当三列皆空时才更新），可以安全重跑。
func runOperationLogRegionCommand(args []string, output io.Writer) int {
	flags := flag.NewFlagSet("operation-log-regions", flag.ContinueOnError)
	flags.SetOutput(output)
	apply := flags.Bool("apply", false, "execute the backfill (default: read-only preview)")
	limit := flags.Int("limit", 0, "at most this many distinct IPs per run, 0 means all")
	interval := flags.Duration("interval", 200*time.Millisecond, "pause between upstream lookups to respect provider quotas")
	timeout := flags.Duration("timeout", 30*time.Minute, "maximum duration")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 1
	}
	if flags.NArg() != 0 || *limit < 0 || *interval < 0 || *timeout <= 0 {
		fmt.Fprintln(output, "invalid options: limit>=0, interval>=0, timeout>0, no positional arguments")
		return 1
	}

	cfg, err := config.Load(os.Getenv("CONFIG_FILE"))
	if err != nil {
		fmt.Fprintln(output, "configuration error:", masking.Error(err))
		return 1
	}
	cfg.Database.ReadTimeout = *timeout
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	db, err := app.OpenDatabase(ctx, cfg.Database)
	if err != nil {
		fmt.Fprintln(output, "database error:", masking.Error(err))
		return 1
	}
	defer db.Close()

	repo := observability.NewRepository(db)
	ips, err := repo.PendingOperationLogRegionIPs(ctx, *limit)
	if err != nil {
		fmt.Fprintln(output, "failed to list pending IPs:", masking.Error(err))
		return 1
	}
	fmt.Fprintf(output, "Pending distinct client IPs without region: %d\n", len(ips))
	if len(ips) == 0 {
		fmt.Fprintln(output, "Nothing to backfill.")
		return 0
	}
	if !*apply {
		fmt.Fprintln(output, "Preview only; rerun with --apply to write the region columns.")
		return 0
	}

	// 解析器与在线服务同源配置：同一份 ipRegion 段、同一套后台数据源（bp_ip_config）、
	// 同一个 Redis 缓存前缀，因此命令补出的结论与线上完全一致。
	redisClient, err := openBackfillRedis(ctx, cfg, output)
	if err != nil {
		fmt.Fprintln(output, "redis unavailable, continuing without cache:", masking.Error(err))
		redisClient = nil
	}
	if redisClient != nil {
		defer redisClient.Close()
	}
	resolver := ipregion.NewResolver(cfg.IPRegion, redisClient)
	resolver.SetSourceLoader(ipconfig.NewService(ipconfig.NewRepository(db), nil).ActiveSource)

	var (
		resolved, skipped, failed, rows int64
	)
	for index, ip := range ips {
		if ctx.Err() != nil {
			fmt.Fprintln(output, "deadline reached; rerun to continue the backfill.")
			break
		}
		region, ok := resolveRegionForCLI(ctx, resolver, ip)
		if !ok {
			failed++
			continue
		}
		affected, err := repo.BackfillOperationLogRegion(ctx, ip, region.Country, region.Province, region.City)
		if err != nil {
			fmt.Fprintf(output, "update failed for one IP: %s\n", masking.Error(err))
			failed++
			continue
		}
		if affected == 0 {
			skipped++
		} else {
			resolved++
			rows += affected
		}
		if *interval > 0 && index < len(ips)-1 {
			select {
			case <-ctx.Done():
			case <-time.After(*interval):
			}
		}
	}
	fmt.Fprintf(output, "Backfill finished: ip_resolved=%d ip_unchanged=%d ip_failed=%d rows_updated=%d\n",
		resolved, skipped, failed, rows)
	if failed > 0 {
		fmt.Fprintln(output, "Failed IPs are usually upstream timeouts/quotas; they stay pending and can be retried by rerunning.")
	}
	return 0
}

// openBackfillRedis 尝试连接 Redis。缓存只是加速手段，连不上时解析仍然可用
// （每次直连上游），因此调用方按「可降级」处理。
func openBackfillRedis(ctx context.Context, cfg config.Config, output io.Writer) (*redis.Client, error) {
	pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	client, err := app.OpenRedis(pingCtx, cfg.Redis)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// resolveRegionForCLI 与在线服务共用 ipregion 的归一化/内网判定，避免命令与
// 线上对同一个 IP 得到不同结论。
func resolveRegionForCLI(ctx context.Context, resolver *ipregion.Resolver, rawIP string) (ipregion.Region, bool) {
	ip := ipregion.NormalizeIP(rawIP)
	if ip == "" || ipregion.IsPrivateIP(ip) {
		return ipregion.Region{}, false
	}
	region, err := resolver.Resolve(ctx, ip)
	if err != nil || !ipregion.HasRegionData(region) {
		return ipregion.Region{}, false
	}
	return region, true
}
