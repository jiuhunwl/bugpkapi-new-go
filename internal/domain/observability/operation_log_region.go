package observability

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"bugpk-api/internal/domain/ipregion"
	"bugpk-api/internal/domain/rbac"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/masking"

	"github.com/cloudwego/hertz/pkg/common/hlog"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// 审计记录的地区解析是旁路增强，绝不能拖慢业务请求，因此与网关调用日志
// 采取同一种策略：同步写入时只落 client_ip，地区在后台异步补全。
//
// 与网关的差异在于「补全目标」：网关新建一整行，写什么就是什么；审计行已经
// 落库，只能事后按主键回填，所以 InsertOperationLog 必须回传自增 ID。
const (
	// operationLogRegionTimeout 是单次上游解析预算，与 config 默认的
	// ipRegion.timeout 一致；解析器内部还会按剩余预算在兜底源之间分配。
	operationLogRegionTimeout = 5 * time.Second
	// operationLogRegionWorkers 限制并发解析数。上游地区接口多为免费额度，
	// 审计写入又是突发（一次批量操作可能连写十几条），必须设闸。
	operationLogRegionWorkers = 32
)

// OperationLogRegionResolver 是 observability 对 IP 归属地解析器的最小依赖面。
//
// 用窄接口而不是直接持有 *ipregion.Resolver：单测可注入确定性结果，不必拉起
// HTTP 客户端与 Redis；两个域之间也只共享 Region 这一个值类型。
type OperationLogRegionResolver interface {
	Resolve(ctx context.Context, ip string) (ipregion.Region, error)
}

// regionBackfiller 负责「写入后异步补全地区」这一条旁路。
//
// 生命周期与 Service 一致：随进程启动创建，随优雅关闭排空。补全任务是尽力
// 而为的——丢一条只影响一个列表单元格，而阻塞或 panic 会波及审计主链路，
// 所以队列满时直接丢弃，任务体统一 recover。
type regionBackfiller struct {
	repo     *Repository
	resolver OperationLogRegionResolver
	slots    chan struct{}

	mu      sync.Mutex
	closing bool
	wg      sync.WaitGroup
}

func newRegionBackfiller(repo *Repository, resolver OperationLogRegionResolver) *regionBackfiller {
	if repo == nil || resolver == nil {
		return nil
	}
	return &regionBackfiller{
		repo:     repo,
		resolver: resolver,
		slots:    make(chan struct{}, operationLogRegionWorkers),
	}
}

// SetIPRegionResolver 注入 IP 归属地解析器并启用审计记录的地区自动补全。
// 未注入时地区列保持为空（前端显示「—」），与历史行为一致；详情页的按需
// 解析同样需要该注入，否则返回空快照。
func (s *Service) SetIPRegionResolver(resolver OperationLogRegionResolver) {
	if s == nil {
		return
	}
	s.regionResolver = resolver
	s.region = newRegionBackfiller(s.repo, resolver)
}

// enqueueRegionBackfill 尝试把一条「需要补全地区」的任务排入后台队列。
//
// 非阻塞是刻意的：审计写入位于请求同步路径上，队列拥塞时牺牲的是地区列的
// 完整度，不是请求延迟。被丢弃的记录仍有补救路径（详情页按需解析、回填命令）。
func (s *Service) enqueueRegionBackfill(id int64, clientIP string) {
	if s == nil || s.region == nil || id <= 0 {
		return
	}
	s.region.enqueue(id, clientIP)
}

func (b *regionBackfiller) enqueue(id int64, clientIP string) {
	if b == nil || id <= 0 || strings.TrimSpace(clientIP) == "" {
		return
	}
	b.mu.Lock()
	if b.closing {
		b.mu.Unlock()
		return
	}
	select {
	case b.slots <- struct{}{}:
		b.wg.Add(1)
		b.mu.Unlock()
	default:
		b.mu.Unlock()
		return
	}
	go func() {
		defer func() {
			<-b.slots
			b.wg.Done()
			if recovered := recover(); recovered != nil {
				hlog.Warnf("operation log region backfill panicked: %s", masking.ErrorText(fmt.Sprint(recovered)))
			}
		}()
		b.fill(context.WithoutCancel(context.Background()), id, clientIP)
	}()
}

// fill 解析 IP 并把结果回填到指定记录；解析失败静默放弃。
func (b *regionBackfiller) fill(ctx context.Context, id int64, clientIP string) {
	region, ok := resolveRegion(ctx, b.resolver, clientIP)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, operationLogWriteTimeout)
	defer cancel()
	if err := b.repo.UpdateOperationLogRegion(ctx, id, region.Country, region.Province, region.City); err != nil {
		hlog.CtxWarnf(ctx, "operation log region update failed: %s", masking.Error(err))
	}
}

// Close 停止接收新任务并等待在途任务结束。
func (b *regionBackfiller) Close(ctx context.Context) error {
	if b == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	b.mu.Lock()
	b.closing = true
	b.mu.Unlock()

	done := make(chan struct{})
	go func() {
		b.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close 释放审计侧的后台资源，供优雅关闭流程挂载。
func (s *Service) Close(ctx context.Context) error {
	if s == nil || s.region == nil {
		return nil
	}
	return s.region.Close(ctx)
}

// resolveRegion 归一化 IP → 跳过内网/非法地址 → 调上游；返回 false 表示
// 这次解析没有可写入的结论（调用方应保持地区列为空，而不是写空串）。
func resolveRegion(ctx context.Context, resolver OperationLogRegionResolver, rawIP string) (ipregion.Region, bool) {
	if resolver == nil {
		return ipregion.Region{}, false
	}
	ip := ipregion.NormalizeIP(rawIP)
	if ip == "" || ipregion.IsPrivateIP(ip) {
		return ipregion.Region{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, operationLogRegionTimeout)
	defer cancel()
	region, err := resolver.Resolve(ctx, ip)
	if err != nil {
		return ipregion.Region{}, false
	}
	if !ipregion.HasRegionData(region) {
		return ipregion.Region{}, false
	}
	return region, true
}

// ResolveOperationLogRegion 按需解析单条审计记录的地区并回写，返回最新快照。
//
// 与写入时异步补全共用同一套判定（resolveRegion），区别只在触发时机：这里由
// 管理员在详情页显式点击触发，因此失败要如实上报，而不是静默丢弃。已解析的
// 记录直接返回快照，不重复打上游。
func (s *Service) ResolveOperationLogRegion(ctx context.Context, principal rbac.Principal, id int64) (OperationLogRegion, error) {
	if s == nil || s.repo == nil {
		return OperationLogRegion{}, apperrors.New(consts.StatusInternalServerError, "Operation log repository unavailable")
	}
	if principal.ID <= 0 {
		return OperationLogRegion{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
	}
	if s.authz == nil {
		return OperationLogRegion{}, apperrors.New(consts.StatusForbidden, "Forbidden Exception")
	}
	if err := s.authz.Require(ctx, principal, operationLogPermission); err != nil {
		return OperationLogRegion{}, err
	}
	if id <= 0 {
		return OperationLogRegion{}, apperrors.New(consts.StatusBadRequest, "ID不合法")
	}
	snapshot, found, err := s.repo.OperationLogRegionSnapshot(ctx, id)
	if err != nil {
		return OperationLogRegion{}, err
	}
	if !found {
		return OperationLogRegion{}, apperrors.New(consts.StatusNotFound, "操作记录不存在")
	}
	// 条件更新只在三列皆空时生效，因此重复点击不会覆盖已有地区，也不会
	// 重复消耗上游额度。
	if region, ok := resolveRegion(ctx, s.regionResolver, snapshot.ClientIP); ok {
		if err := s.repo.UpdateOperationLogRegion(ctx, id, region.Country, region.Province, region.City); err != nil {
			return snapshot, err
		}
		snapshot.Country, snapshot.Province, snapshot.City = region.Country, region.Province, region.City
	}
	return snapshot, nil
}
