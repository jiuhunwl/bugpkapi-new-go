package user

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	adminWorkbenchStatsCacheTTL = 30 * time.Second
	// adminWorkbenchStatsLoadTimeout 是整个统计扇出的上限，也是缓存单飞
	// （single-flight）leader 的存活时间。
	//
	// 7 个 section 共用 adminStatsReadConcurrency 个槽位，因此至少要走两轮：
	// 4 条并发 + 3 条。上限必须容得下两轮完整的 adminStatsQueryTimeout，否则
	// 第二轮的 section 无论数据库是否健康都会被判降级。健康路径实测是秒级
	// （缓存 30s + 预聚合表），这里只是天花板。
	adminWorkbenchStatsLoadTimeout = 12 * time.Second
)

const (
	// adminStatsQueryTimeout 是单条统计聚合的预算。
	//
	// 控制台统计全部走预聚合表，健康时是几百毫秒级；一条查询超过 5s 基本只有
	// 一个解释——计划外扫描。继续等下去只会把连接长期占住，让网关记账排在后面。
	// 因此宁可让这一个 section 如实降级，也不把连接池让给它。
	adminStatsQueryTimeout = 5 * time.Second
	// adminStatsReadConcurrency 限制同时执行的统计聚合条数。
	//
	// 控制台的扇出是 7 段独立查询，每段都是一次大聚合。全部同时开跑会瞬间占满
	// 连接池并挤占 buffer pool，把网关写入拖慢——这正是需要解决的「写入被统计
	// 读拖慢」。4 条足以让扇出仍然并行，又给业务读写留出余量。
	adminStatsReadConcurrency = 4
)

// adminStatsSemaphore 是全站统计读的并发闸门。取不到槽位就排队，排队到统计
// 上下文超时仍拿不到，则该 section 如实降级而不是继续加压。
var adminStatsSemaphore = make(chan struct{}, adminStatsReadConcurrency)

// acquireAdminStatsSlot 在 ctx 结束前等待一个统计读槽位。
func acquireAdminStatsSlot(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case adminStatsSemaphore <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// releaseAdminStatsSlot 归还槽位。必须与 acquire 成对，且只能在真正执行查询的
// goroutine 里释放。
func releaseAdminStatsSlot() {
	select {
	case <-adminStatsSemaphore:
	default:
	}
}

type adminWorkbenchStatsCall struct {
	done  chan struct{}
	err   error
	value AdminWorkbenchStats
}

type adminWorkbenchStatsCacheEntry struct {
	call      *adminWorkbenchStatsCall
	value     AdminWorkbenchStats
	expiresAt time.Time
}

type adminWorkbenchStatsCache struct {
	mu      sync.Mutex
	entries map[string]adminWorkbenchStatsCacheEntry
}

func newAdminWorkbenchStatsCache() *adminWorkbenchStatsCache {
	return &adminWorkbenchStatsCache{
		entries: make(map[string]adminWorkbenchStatsCacheEntry),
	}
}

func (c *adminWorkbenchStatsCache) getOrLoad(
	ctx context.Context,
	key string,
	loader func(context.Context) (AdminWorkbenchStats, error),
) (AdminWorkbenchStats, error) {
	if err := ctx.Err(); err != nil {
		return AdminWorkbenchStats{}, err
	}

	now := time.Now()
	c.mu.Lock()
	entry := c.entries[key]
	if now.Before(entry.expiresAt) {
		value := entry.value
		c.mu.Unlock()
		return value, nil
	}
	if entry.call != nil {
		call := entry.call
		c.mu.Unlock()
		select {
		case <-call.done:
			return call.value, call.err
		case <-ctx.Done():
			return AdminWorkbenchStats{}, ctx.Err()
		}
	}

	call := &adminWorkbenchStatsCall{done: make(chan struct{})}
	c.entries[key] = adminWorkbenchStatsCacheEntry{call: call}
	c.mu.Unlock()

	go func() {
		loadCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			adminWorkbenchStatsLoadTimeout,
		)
		defer cancel()
		value, err := loader(loadCtx)

		c.mu.Lock()
		current := c.entries[key]
		call.value = value
		call.err = err
		cacheable := err == nil && (value.AnalyticsReady || len(value.DegradedSections) == 0)
		if cacheable {
			current.value = value
			current.expiresAt = time.Now().Add(adminWorkbenchStatsCacheTTL)
		}
		if current.call == call {
			if cacheable {
				// Only a running load may bypass TTL through the in-flight branch.
				current.call = nil
				c.entries[key] = current
			} else {
				delete(c.entries, key)
			}
		}
		close(call.done)
		c.mu.Unlock()
	}()

	select {
	case <-call.done:
		return call.value, call.err
	case <-ctx.Done():
		return AdminWorkbenchStats{}, ctx.Err()
	}
}

func normalizeAdminWorkbenchSections(sections []AdminWorkbenchSection) []AdminWorkbenchSection {
	if len(sections) == 0 {
		return append([]AdminWorkbenchSection(nil), allAdminWorkbenchSections...)
	}
	allowed := make(map[AdminWorkbenchSection]struct{}, len(allAdminWorkbenchSections))
	for _, section := range allAdminWorkbenchSections {
		allowed[section] = struct{}{}
	}
	selected := make(map[AdminWorkbenchSection]struct{}, len(sections))
	for _, section := range sections {
		if _, ok := allowed[section]; ok {
			selected[section] = struct{}{}
		}
	}
	normalized := make([]AdminWorkbenchSection, 0, len(selected))
	for section := range selected {
		normalized = append(normalized, section)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i] < normalized[j] })
	return normalized
}

func adminWorkbenchStatsCacheKey(sections []AdminWorkbenchSection) string {
	normalized := normalizeAdminWorkbenchSections(sections)
	values := make([]string, len(normalized))
	for i, section := range normalized {
		values[i] = string(section)
	}
	return strings.Join(values, ",")
}
