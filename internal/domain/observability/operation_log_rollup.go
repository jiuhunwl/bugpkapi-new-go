package observability

import (
	"context"
	"sync"
	"time"

	"bugpk-api/internal/pkg/businessclock"
)

// OperationLogRollupWorker 周期性把 bp_operation_log 的明细按天汇总进
// bp_operation_metric_day。
//
// 与调用日志的 rollup 不同，这里不需要 checkpoint 高水位：
//   - 调用日志按「日志 id」推进，因为量级大且只追加；
//   - 操作日志按「自然日」重算，因为审计量级小（每天几十到几千条），
//     直接对该日做 group by 重写一次的成本远低于维护水位线的复杂度，
//     而且天然幂等——重跑任何一天都不会翻倍（on duplicate key update 覆盖）。
//
// 回看窗口取 2 天：今天的数据在持续写入，必须反复重算；昨天用于覆盖跨零点
// 写入的延迟记录（例如 23:59:59 的请求在次日 00:00:00 才落库）。
type OperationLogRollupWorker struct {
	cancel context.CancelFunc
	done   <-chan struct{}
	once   sync.Once
}

// operationLogRollupLookbackDays 是每次重算覆盖的自然天数（含今天）。
const operationLogRollupLookbackDays = 2

// StartOperationLogRollup 启动后台汇总任务。interval 建议取 1~5 分钟：
// 审计数据量小，没必要更频繁；但控制台的「今日操作数」需要及时反映。
func (r *Repository) StartOperationLogRollup(ctx context.Context, interval time.Duration, onError func(error)) *OperationLogRollupWorker {
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		interval = 2 * time.Minute
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		// 启动时先补一次，避免进程重启后控制台长时间显示旧数据。
		r.rollupOperationLogWindow(workerCtx, onError)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-workerCtx.Done():
				return
			case <-ticker.C:
				r.rollupOperationLogWindow(workerCtx, onError)
			}
		}
	}()
	return &OperationLogRollupWorker{cancel: cancel, done: done}
}

// Close 停止后台任务并等待其退出。
func (w *OperationLogRollupWorker) Close(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.once.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	if w.done == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-w.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// rollupOperationLogWindow 重算最近 N 天（含今天）的按天聚合。
//
// 单天失败不中断后续天：审计预聚合是尽力而为的派生数据，
// 某一日因锁冲突失败时，下一次 tick 会自然重试，没有必要整体放弃。
func (r *Repository) rollupOperationLogWindow(ctx context.Context, onError func(error)) {
	if r == nil || r.db == nil {
		return
	}
	today := businessclock.Now()
	for offset := 0; offset < operationLogRollupLookbackDays; offset++ {
		if ctx.Err() != nil {
			return
		}
		day := today.AddDate(0, 0, -offset).Format("2006-01-02")
		if _, err := r.rollupOperationLogDay(ctx, day); err != nil {
			if onError != nil && !isMissingTableError(err) {
				onError(err)
			}
			continue
		}
	}
}
