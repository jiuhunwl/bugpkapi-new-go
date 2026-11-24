package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"bugpk-api/internal/pkg/iphash"
)

// ---------------------------------------------------------------------------
// 运营告警
// ---------------------------------------------------------------------------
//
// 设计原则：告警必须由真实数据推导，且每条告警都能回答两个问题——
// 「发生了什么」与「去哪儿处理」。因此每条告警都带 url。
//
// 数据源覆盖五类（与产品确认的范围一致）：
//  1. 失败率 / 失败量异动     —— 今日 vs 前一日 rollup 对比
//  2. 访问异常               —— 单 IP 高频失败、地区失败率异常
//  3. 待处理业务积压         —— 工单、订单、友链申请
//  4. 统计降级与数据健康     —— analytics 投影新鲜度 / 修复状态
//  5. 系统操作日志           —— 登录失败突增、高权限操作、账号封禁
//
// 生成过程只读预聚合表与常量级 COUNT，不做大表扫描。

// 告警阈值。集中定义便于调参时一处改动。
const (
	// 服务可用性 SLO：99% 成功意味着 1% 的误差预算。
	alertServiceFailureSLO = 0.99
	// 最小样本量：低于此调用量不做失败率判断（统计噪声）。
	alertFailureRateMinSample = 20
	// 多窗口燃烧率：1 小时 14.4 倍 = 2% 预算/小时；6 小时 6 倍 = 5% 预算/6 小时。
	alertServiceFastBurnWindow   = "1 hour"
	alertServiceSlowBurnWindow   = "6 hour"
	alertServiceFastBurnMultiple = 14.4
	alertServiceSlowBurnMultiple = 6.0
	// 今日失败量相对昨日增长的倍数阈值。
	alertFailureVolumeGrowth = 2.0
	// 待处理业务积压阈值。
	alertPendingTicketsThreshold = 10
	alertPendingOrdersThreshold  = 5
	alertPendingLinksThreshold   = 5
	// 登录失败次数阈值（近 24 小时），用于发现凭证暴力破解。
	alertLoginFailureThreshold = 20
	// 账号禁用/删除类高权限操作的近 24 小时次数阈值。
	alertPrivilegedActionThreshold = 10
	// 投影滞后超过该时长即视为数据健康异常。
	alertRollupLagThreshold = 10 * time.Minute
)

// adminWorkbenchAlerts 生成运营告警列表。
//
// 任何单个数据源失败都只跳过对应类别的告警，不影响其他类别——
// 告警面板在依赖部分不可用时仍然可用，这比「全有或全无」更有价值。
func (r *Repository) adminWorkbenchAlerts(ctx context.Context, now time.Time) ([]AdminWorkbenchAlert, error) {
	loaders := []func() ([]AdminWorkbenchAlert, error){
		func() ([]AdminWorkbenchAlert, error) { return r.alertTrafficAnomalies(ctx) },
		func() ([]AdminWorkbenchAlert, error) { return r.alertAccessAnomalies(ctx) },
		func() ([]AdminWorkbenchAlert, error) { return r.alertBacklog(ctx) },
		func() ([]AdminWorkbenchAlert, error) { return r.alertDataHealth(ctx, now) },
		func() ([]AdminWorkbenchAlert, error) { return r.alertOperationAnomalies(ctx) },
	}
	values := make([][]AdminWorkbenchAlert, len(loaders))
	failures := make([]error, len(loaders))
	// The workbench already admits alerts as one bounded statistics section.
	// Keep the five source groups sequential so the section slot reflects the
	// actual database work instead of hiding another unbounded fan-out inside it.
	for i, load := range loaders {
		values[i], failures[i] = load()
	}
	alerts := make([]AdminWorkbenchAlert, 0, 8)
	available := false
	for i, err := range failures {
		if err == nil {
			available = true
			alerts = append(alerts, values[i]...)
		}
	}
	// A successful zero-result source is valid; five unavailable sources are
	// not an empty healthy alert feed and must not defeat the full-outage 503.
	if !available {
		return alerts, errors.Join(failures...)
	}
	return alerts, nil
}

// serviceFailureWindow is one SLO evaluation window. Only upstream-attributable
// calls enter the sample: client mistakes, throttling and expected empty
// results cannot burn the upstream availability budget.
type serviceFailureWindow struct {
	sample   int64
	upstream int64
}

type serviceFailureInterface struct {
	interfaceID int64
	code        string
	name        string
	sample      int64
	upstream    int64
}

func (w serviceFailureWindow) rate() float64 {
	if w.sample <= 0 {
		return 0
	}
	return float64(w.upstream) / float64(w.sample)
}

func (w serviceFailureWindow) burn() float64 {
	return w.rate() / (1 - alertServiceFailureSLO)
}

// alertTrafficAnomalies evaluates the 99% service SLO with multi-window burn
// rates instead of a fixed daily failure-rate threshold. A daily aggregate
// either misses a short outage or keeps firing for long-tail interfaces that
// are naturally noisy; burn rate answers "how fast is the error budget being
// consumed" and is comparable across windows.
func (r *Repository) alertTrafficAnomalies(ctx context.Context) ([]AdminWorkbenchAlert, error) {
	fast, err := r.readServiceFailureWindow(ctx, alertServiceFastBurnWindow)
	if err != nil {
		return nil, err
	}
	slow, err := r.readServiceFailureWindow(ctx, alertServiceSlowBurnWindow)
	if err != nil {
		return nil, err
	}
	worst, worstErr := r.worstServiceFailureInterface(ctx)
	if worstErr != nil && !errors.Is(worstErr, sql.ErrNoRows) {
		return nil, worstErr
	}
	todayFailed, yestFailed, err := r.failureVolumeToday(ctx)
	if err != nil {
		return nil, err
	}

	var alerts []AdminWorkbenchAlert
	fastActive := fast.sample >= alertFailureRateMinSample && fast.burn() >= alertServiceFastBurnMultiple
	slowActive := slow.sample >= alertFailureRateMinSample && slow.burn() >= alertServiceSlowBurnMultiple
	worstActive := worstErr == nil && worst.sample >= alertFailureRateMinSample

	if fastActive {
		content := fmt.Sprintf("近 1 小时有效样本 %d 次，上游失败 %d 次，服务失败率 %.1f%%，达到 99%% SLO 误差预算的 %.1f 倍。",
			fast.sample, fast.upstream, fast.rate()*100, fast.burn())
		if worstActive {
			content += fmt.Sprintf("最严重接口 %s（%s）1 小时失败率 %.1f%%。",
				displayInterfaceName(worst), worst.code, float64(worst.upstream)*100/float64(worst.sample))
		}
		alerts = append(alerts, AdminWorkbenchAlert{
			Level:   "danger",
			Title:   "服务失败率快速燃烧",
			Content: content,
			URL:     "/console/call-logs",
		})
	} else if worstActive {
		alerts = append(alerts, AdminWorkbenchAlert{
			Level: "warning",
			Title: "接口服务失败率异常",
			Content: fmt.Sprintf("接口 %s（%s）近 1 小时有效样本 %d 次，上游失败 %d 次，失败率 %.1f%%，达到误差预算的 %.1f 倍，建议优先核查该上游节点。",
				displayInterfaceName(worst), worst.code, worst.sample, worst.upstream,
				float64(worst.upstream)*100/float64(worst.sample),
				float64(worst.upstream)/float64(worst.sample)/(1-alertServiceFailureSLO)),
			URL: "/console/call-logs",
		})
	} else if slowActive {
		alerts = append(alerts, AdminWorkbenchAlert{
			Level: "warning",
			Title: "服务失败率持续偏高",
			Content: fmt.Sprintf("近 6 小时有效样本 %d 次，上游失败 %d 次，服务失败率 %.1f%%，达到 99%% SLO 误差预算的 %.1f 倍。",
				slow.sample, slow.upstream, slow.rate()*100, slow.burn()),
			URL: "/console/call-logs",
		})
	}
	// 失败量异动仍保留为绝对值信号：昨日同时段有可比样本时，今日上游失败量
	// 增长超过阈值倍数，即使占比暂时不高也值得核查。
	if yestFailed > 0 && todayFailed > 0 {
		growth := float64(todayFailed) / float64(yestFailed)
		if growth >= alertFailureVolumeGrowth && todayFailed >= alertFailureRateMinSample {
			alerts = append(alerts, AdminWorkbenchAlert{
				Level: "warning",
				Title: "上游失败量较昨日同期激增",
				Content: fmt.Sprintf("今日上游失败 %d 次，昨日同期 %d 次，增长 %.1f 倍，建议核查上游接口状态。",
					todayFailed, yestFailed, growth),
				URL: "/console/call-logs",
			})
		}
	}
	return alerts, nil
}

func (r *Repository) readServiceFailureWindow(ctx context.Context, window string) (serviceFailureWindow, error) {
	var success, upstream int64
	err := r.db.QueryRowContext(ctx, `
		select coalesce(sum(success_count),0),coalesce(sum(upstream_error_count),0)
		from bp_call_metric_minute
		where bucket_start >= date_sub(now(), interval `+window+`)
	`).Scan(&success, &upstream)
	if err != nil {
		return serviceFailureWindow{}, err
	}
	// Client errors and throttles are deliberately absent from this sample:
	// the SLO measures upstream-attributable availability only.
	return serviceFailureWindow{sample: success + upstream, upstream: upstream}, nil
}

func (r *Repository) worstServiceFailureInterface(ctx context.Context) (serviceFailureInterface, error) {
	threshold := (1 - alertServiceFailureSLO) * alertServiceFastBurnMultiple
	var item serviceFailureInterface
	err := r.db.QueryRowContext(ctx, fmt.Sprintf(`
		select o.interface_id,coalesce(a.api_code,''),coalesce(a.name,''),
		       sum(o.success_count)+sum(o.upstream_error_count),
		       sum(o.upstream_error_count)
		from bp_call_api_metric_minute o
		left join bp_interface_api a on a.id=o.interface_id
		where o.bucket_start >= date_sub(now(), interval %s)
		group by o.interface_id,a.api_code,a.name
		having sum(o.success_count)+sum(o.upstream_error_count) >= %d
		   and sum(o.upstream_error_count) >= %.6f * (sum(o.success_count)+sum(o.upstream_error_count))
		order by sum(o.upstream_error_count)/(sum(o.success_count)+sum(o.upstream_error_count)) desc,
		         sum(o.upstream_error_count) desc
		limit 1
	`, alertServiceFastBurnWindow, alertFailureRateMinSample, threshold)).Scan(
		&item.interfaceID, &item.code, &item.name, &item.sample, &item.upstream,
	)
	return item, err
}

func (r *Repository) failureVolumeToday(ctx context.Context) (int64, int64, error) {
	var today, yesterday int64
	err := r.db.QueryRowContext(ctx, `
		select
		  coalesce(sum(case when bucket_start>=current_date() then upstream_error_count else 0 end),0),
		  coalesce(sum(case when bucket_start>=date_sub(current_date(), interval 1 day)
		                     and bucket_start<current_date()
		                     and time(bucket_start)<=current_time()
		                    then upstream_error_count else 0 end),0)
		from bp_call_metric_minute
		where bucket_start>=date_sub(current_date(), interval 1 day)
		  and bucket_start<date_add(current_date(), interval 1 day)
	`).Scan(&today, &yesterday)
	return today, yesterday, err
}

func displayInterfaceName(item serviceFailureInterface) string {
	if item.name != "" {
		return item.name
	}
	if item.code != "" {
		return item.code
	}
	return fmt.Sprintf("#%d", item.interfaceID)
}

// alertAccessAnomalies 检测访问异常：单 IP 高频失败与地区失败率异常。
//
// 只读小时级失败聚合表 bp_call_failure_ip_hour，因此即便量级很大也不会拖慢面板。
func (r *Repository) alertAccessAnomalies(ctx context.Context) ([]AdminWorkbenchAlert, error) {
	var (
		topIPFailures int64
		topIPHash     string
	)
	err := r.db.QueryRowContext(ctx, `
		select coalesce(max(c),0), coalesce(max(h),'')
		from (
			select sha2(concat('`+iphash.FailureSQLPrefix()+`',client_ip),256) h, sum(failure_count) c
			from bp_call_failure_ip_hour
			where bucket_start >= date_sub(now(), interval 1 day)
			  and client_ip <> ''
			group by client_ip
			order by c desc
			limit 1
		) t
	`).Scan(&topIPFailures, &topIPHash)
	if err != nil {
		return nil, err
	}

	var alerts []AdminWorkbenchAlert
	if topIPFailures >= alertFailureRateMinSample*5 {
		// 只暴露哈希前缀：审计面板无需还原具体 IP，避免在任何前端可见处泄露地址。
		alerts = append(alerts, AdminWorkbenchAlert{
			Level: "warning",
			Title: "存在异常高频失败的来源 IP",
			Content: fmt.Sprintf("近 24 小时单个来源（指纹 %s…）产生 %d 次失败调用，疑似探测或密钥泄露，建议核查风控记录。",
				shortHash(topIPHash), topIPFailures),
			URL: "/risk-control/dashboard",
		})
	}
	return alerts, nil
}

// shortHash 取哈希前 12 个字符用于展示。
func shortHash(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

// alertBacklog 检测待处理业务积压。
//
// 三类积压并发统计：工单、待支付订单、友链申请。
// 刻意都带时间下界或状态索引，保证是索引范围计数而非全表扫描。
func (r *Repository) alertBacklog(ctx context.Context) ([]AdminWorkbenchAlert, error) {
	var tickets, orders, links int64
	failures := make([]error, 3)
	failures[0] = r.db.QueryRowContext(ctx, `
		select count(*) from bp_ticket where status in ('OPEN','PROCESSING')
	`).Scan(&tickets)
	failures[1] = r.db.QueryRowContext(ctx, `
		select count(*) from bp_payment_order where status in ('CREATED','PAYING')
	`).Scan(&orders)
	failures[2] = r.db.QueryRowContext(ctx, `
		select count(*) from bp_friend_link_application where status='PENDING'
	`).Scan(&links)
	if failures[0] != nil && failures[1] != nil && failures[2] != nil {
		return nil, errors.Join(failures...)
	}

	var alerts []AdminWorkbenchAlert
	if failures[0] == nil && tickets >= alertPendingTicketsThreshold {
		alerts = append(alerts, AdminWorkbenchAlert{
			Level:   alertLevelForBacklog(tickets, alertPendingTicketsThreshold),
			Title:   "工单积压待处理",
			Content: fmt.Sprintf("当前有 %d 个工单处于待受理或处理中，建议及时跟进以免影响用户满意度。", tickets),
			URL:     "/admin/tickets",
		})
	}
	if failures[1] == nil && orders >= alertPendingOrdersThreshold {
		alerts = append(alerts, AdminWorkbenchAlert{
			Level:   "info",
			Title:   "存在未完成订单",
			Content: fmt.Sprintf("当前有 %d 笔订单处于待支付或支付中状态，长时间未完成可能意味着支付通道异常。", orders),
			URL:     "/payment/orders",
		})
	}
	if failures[2] == nil && links >= alertPendingLinksThreshold {
		alerts = append(alerts, AdminWorkbenchAlert{
			Level:   "info",
			Title:   "友链申请待审核",
			Content: fmt.Sprintf("当前有 %d 条友情链接申请等待审核。", links),
			URL:     "/content/friend-link",
		})
	}
	return alerts, nil
}

// alertLevelForBacklog 积压越多级别越高（达到阈值 3 倍升级为 warning）。
func alertLevelForBacklog(count, threshold int64) string {
	if threshold > 0 && count >= threshold*3 {
		return "warning"
	}
	return "info"
}

// alertDataHealth 检测统计投影与数据健康。
//
// 这是「让其他告警可信」的元告警：如果 rollup 已经滞后，那么基于 rollup 的
// 失败率/访问量告警本身就不可信，必须显式告知运维先去修数据链路。
func (r *Repository) alertDataHealth(ctx context.Context, now time.Time) ([]AdminWorkbenchAlert, error) {
	var alerts []AdminWorkbenchAlert
	fresh, err := r.adminRollupCheckpointFresh(ctx, "call_log_rollup", alertRollupLagThreshold)
	if err == nil && !fresh {
		alerts = append(alerts, AdminWorkbenchAlert{
			Level: "warning",
			Title: "调用统计投影更新滞后",
			Content: fmt.Sprintf("调用日志聚合已超过 %d 分钟未更新，控制台统计可能偏低。"+
				"请确认 analytics 已启用（analytics.enabled=true / ANALYTICS_ENABLED=true）并检查 bp_analytics_checkpoint 的 last_error。",
				int(alertRollupLagThreshold.Minutes())),
			URL: "/console/call-logs",
		})
	}
	// 操作日志投影独立检查：两者由不同 worker 维护，任一滞后都应单独提示。
	auditFresh, auditErr := r.adminRollupCheckpointFresh(ctx, "operation_log_rollup", alertRollupLagThreshold)
	if auditErr == nil && !auditFresh {
		alerts = append(alerts, AdminWorkbenchAlert{
			Level: "info",
			Title: "操作日志统计更新滞后",
			Content: "系统操作日志的按天聚合尚未追平最新数据，概览中的操作次数可能偏低。" +
				"审计明细不受影响，仍可在操作日志页面完整查询。",
			URL: "/system/operation-log",
		})
	}
	if err != nil && auditErr != nil {
		return nil, errors.Join(err, auditErr)
	}
	return alerts, nil
}

// alertOperationAnomalies 依据系统操作日志生成安全类告警。
//
// 关注两类信号：
//   - 登录失败突增：凭证暴力破解或撞库的典型特征；
//   - 账号处置类操作密集：批量封禁/删除通常意味着风控响应，也可能是越权操作，
//     两种情况都值得运营人员确认。
//
// 只读近 24 小时且走 idx_bp_operation_log_action_time / _status_time，
// 因此即便审计表很大也只扫描受限范围。
func (r *Repository) alertOperationAnomalies(ctx context.Context) ([]AdminWorkbenchAlert, error) {
	var (
		loginFailures  int64
		privilegedActs int64
	)
	failures := make([]error, 2)
	failures[0] = r.db.QueryRowContext(ctx, `
		select count(*) from bp_operation_log
		where action='login' and status=0 and created_at >= date_sub(now(), interval 1 day)
	`).Scan(&loginFailures)
	failures[1] = r.db.QueryRowContext(ctx, `
		select count(*) from bp_operation_log
		where action in ('delete','status') and created_at >= date_sub(now(), interval 1 day)
	`).Scan(&privilegedActs)
	if failures[0] != nil && failures[1] != nil {
		return nil, errors.Join(failures...)
	}

	var alerts []AdminWorkbenchAlert
	if failures[0] == nil && loginFailures >= alertLoginFailureThreshold {
		alerts = append(alerts, AdminWorkbenchAlert{
			Level: "danger",
			Title: "登录失败次数异常偏高",
			Content: fmt.Sprintf("近 24 小时共记录 %d 次登录失败，可能存在凭证暴力破解或撞库攻击，建议检查来源 IP 并考虑启用更严格的登录限流。",
				loginFailures),
			URL: "/system/operation-log?action=login&status=false",
		})
	}
	if failures[1] == nil && privilegedActs >= alertPrivilegedActionThreshold {
		alerts = append(alerts, AdminWorkbenchAlert{
			Level: "warning",
			Title: "账号处置类操作密集",
			Content: fmt.Sprintf("近 24 小时发生 %d 次删除或状态变更操作，请确认这些变更为预期行为，避免误操作或越权。",
				privilegedActs),
			URL: "/system/operation-log?module=user",
		})
	}
	return alerts, nil
}
