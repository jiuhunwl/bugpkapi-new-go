package observability

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/pkg/businessclock"
	"bugpk-api/internal/pkg/iphash"
	"bugpk-api/internal/pkg/pagination"
)

// 操作日志查询边界。列表页允许更大的翻页深度（审计场景常有「翻到很后面找一次操作」），
// 但仍设硬上限，避免深分页把库拖垮。
const (
	maxOperationLogPage     = 2_000
	maxOperationLogPageSize = 200
	// 操作日志统计窗口：概览展示近 30 天。
	operationLogStatsDays = 30
	// 单次写入的最大 JSON 明细长度，超出则省略明细，防止破坏 JSON 或撑爆审计表。
	maxOperationLogDetailLen = 8_000
)

// clientIPHashSalt 是操作日志专用盐，与调用日志的区域/失败/风控盐隔离，
// 避免同一 IP 在不同表之间可被关联。实际前缀由 iphash 包按环境密钥派生
// （未配置密钥时与历史公开字面盐逐字节一致）。
func operationLogInsert() string {
	return `insert into bp_operation_log
	(user_id,username,module,action,summary,method,path,target_type,target_id,status,error_message,status_code,
	 client_ip,client_ip_hash,country,province,city,user_agent,elapsed_ms,detail,actor_kind)
	values (?,?,?,?,?,?,?,?,?,?,?,?,?,unhex(sha2(concat('` + iphash.OperationLogSQLPrefix() + `',coalesce(?,'')),256)),?,?,?,?,?,?,?)`
}

// InsertOperationLog 写入一条审计记录，返回自增主键（取不到时为 0）。
//
// client_ip_hash 由本方法统一计算：调用方只传明文 client_ip，避免每个调用点
// 各自实现哈希（写法不一致会导致同一 IP 无法在表内聚合）。
// 写入失败不返回错误给上层业务——审计是旁路，不能因为审计库抖动就让业务失败。
// 调用方若需要感知，可用返回的 error 做日志。
//
// 返回主键是为了让「写入时异步补全 IP 归属地」能在事后精确定位到这一行；
// 驱动不支持 LastInsertId 时返回 0，调用方据此跳过补全（不视为失败）。
func (r *Repository) InsertOperationLog(ctx context.Context, rec OperationLogRecord) (int64, error) {
	if r == nil || r.db == nil {
		return 0, nil
	}
	// JSON 列不能接收空字符串，也不能按字节截断 JSON。可选明细无效或超限
	// 时写 SQL NULL，保留审计主体，避免整条记录被 MySQL 拒绝。
	var detail any
	if len(rec.Detail) <= maxOperationLogDetailLen && json.Valid([]byte(rec.Detail)) {
		detail = rec.Detail
	}
	status := 0
	if rec.Status {
		status = 1
	}
	result, err := r.db.ExecContext(ctx, operationLogInsert(),
		rec.UserID, rec.Username, rec.Module, rec.Action, rec.Summary, rec.Method, rec.Path,
		rec.TargetType, rec.TargetID, status, rec.ErrorMessage, rec.StatusCode,
		rec.ClientIP, rec.ClientIP,
		rec.Country, rec.Province, rec.City, rec.UserAgent, rec.ElapsedMS, detail, rec.ActorKind,
	)
	if err != nil {
		return 0, err
	}
	id, idErr := result.LastInsertId()
	if idErr != nil {
		return 0, nil
	}
	return id, nil
}

// OperationLogRegion 是操作日志里与 IP 归属地相关的字段快照。
// JSON 命名与列表项的 country/province/city/clientIp 保持一致，前端可直接复用。
type OperationLogRegion struct {
	ClientIP string `json:"clientIp,omitempty"`
	Country  string `json:"country,omitempty"`
	Province string `json:"province,omitempty"`
	City     string `json:"city,omitempty"`
}

// OperationLogRegionSnapshot 读取指定记录的地区字段；第二返回值表示记录是否存在。
func (r *Repository) OperationLogRegionSnapshot(ctx context.Context, id int64) (OperationLogRegion, bool, error) {
	var snapshot OperationLogRegion
	err := r.db.QueryRowContext(ctx,
		`select coalesce(client_ip,''),coalesce(country,''),coalesce(province,''),coalesce(city,'')
		   from bp_operation_log where id = ?`, id).
		Scan(&snapshot.ClientIP, &snapshot.Country, &snapshot.Province, &snapshot.City)
	if errors.Is(err, sql.ErrNoRows) {
		return OperationLogRegion{}, false, nil
	}
	if err != nil {
		return OperationLogRegion{}, false, err
	}
	return snapshot, true, nil
}

// UpdateOperationLogRegion 回填一条记录的地区字段。
//
// 「仅当三列皆空才写入」是幂等保护：写入时的异步补全、详情页的按需解析、
// 历史回填命令会并发触碰同一行，条件更新让先到者胜出，避免互相覆盖，
// 也让命令重跑变得安全。
func (r *Repository) UpdateOperationLogRegion(ctx context.Context, id int64, country, province, city string) error {
	if r == nil || r.db == nil || id <= 0 {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `update bp_operation_log
		   set country = ?, province = ?, city = ?
		 where id = ?
		   and coalesce(country,'') = '' and coalesce(province,'') = '' and coalesce(city,'') = ''`,
		country, province, city, id)
	return err
}

// PendingOperationLogRegionIPs 列出尚未解析地区、且留有明文 IP 的去重客户端 IP。
//
// 供历史回填命令使用：按 IP 去重后再解析，同一办公网出口 IP 只查一次上游。
func (r *Repository) PendingOperationLogRegionIPs(ctx context.Context, limit int) ([]string, error) {
	query := `select distinct client_ip from bp_operation_log
		 where coalesce(country,'') = '' and coalesce(province,'') = '' and coalesce(city,'') = ''
		   and coalesce(client_ip,'') <> ''`
	args := []any{}
	if limit > 0 {
		query += " limit ?"
		args = append(args, limit)
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ips := []string{}
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			return nil, err
		}
		ips = append(ips, ip)
	}
	return ips, rows.Err()
}

// BackfillOperationLogRegion 按客户端 IP 回填该 IP 下所有待解析记录的地区。
// 返回实际更新的行数。
func (r *Repository) BackfillOperationLogRegion(ctx context.Context, clientIP, country, province, city string) (int64, error) {
	if r == nil || r.db == nil || strings.TrimSpace(clientIP) == "" {
		return 0, nil
	}
	result, err := r.db.ExecContext(ctx, `update bp_operation_log
		   set country = ?, province = ?, city = ?
		 where client_ip = ?
		   and coalesce(country,'') = '' and coalesce(province,'') = '' and coalesce(city,'') = ''`,
		country, province, city, clientIP)
	if err != nil {
		return 0, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return affected, nil
}

// operationLogListColumns 列表页字段。IP 明文仅管理员可见，由 service 层决定是否清空。
const operationLogListColumns = `
		l.id,coalesce(l.user_id,0),coalesce(l.username,''),coalesce(l.module,''),coalesce(l.action,''),
		coalesce(l.summary,''),coalesce(l.method,''),coalesce(l.path,''),coalesce(l.target_type,''),coalesce(l.target_id,''),
		l.status,coalesce(l.error_message,''),coalesce(l.status_code,0),coalesce(l.client_ip,''),
		coalesce(l.country,''),coalesce(l.province,''),coalesce(l.city,''),coalesce(l.user_agent,''),
		coalesce(l.elapsed_ms,0),coalesce(cast(l.detail as char),''),date_format(l.created_at,'%Y-%m-%d %H:%i:%s')`

// operationLogFilter 把查询条件编译成 WHERE 片段与参数。
//
// 谓词顺序刻意按「最左索引列在前」排列，便于 MySQL 复用 idx_bp_operation_log_created
// 或 *_time 系列索引：created_at 范围始终存在（默认近 30 天），其余条件作为过滤。
func operationLogFilter(q OperationLogListQuery) (string, []any) {
	var (
		where []string
		args  []any
	)
	switch q.Scope {
	case operationLogScopeAdmin:
		where = append(where, "l.actor_kind = 'admin'")
	case operationLogScopePersonal:
		if q.UserID <= 0 {
			where = append(where, "1=0")
		} else {
			where = append(where, "l.actor_kind in ('admin','user')")
		}
	default:
		where = append(where, "1=0") // No implicit unscoped query.
	}
	if q.UserID > 0 {
		where = append(where, "l.user_id = ?")
		args = append(args, q.UserID)
	}
	if v := strings.TrimSpace(q.Username); v != "" {
		where = append(where, "l.username like ?")
		args = append(args, "%"+v+"%")
	}
	if v := strings.TrimSpace(q.Module); v != "" {
		where = append(where, "l.module = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(q.Action); v != "" {
		where = append(where, "l.action = ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(q.ClientIP); v != "" {
		where = append(where, "l.client_ip = ?")
		args = append(args, v)
	}
	if q.Status != nil {
		if *q.Status {
			where = append(where, "l.status = 1")
		} else {
			where = append(where, "l.status = 0")
		}
	}
	if v := strings.TrimSpace(q.Keyword); v != "" {
		// 关键词跨摘要/路径/用户名，用 like 前导通配；审计场景数据量可控且已有时间下界。
		where = append(where, "(l.summary like ? or l.path like ? or l.username like ? or l.target_id like ?)")
		kw := "%" + v + "%"
		args = append(args, kw, kw, kw, kw)
	}
	if v := strings.TrimSpace(q.StartTime); v != "" {
		where = append(where, "l.created_at >= ?")
		args = append(args, v)
	}
	if v := strings.TrimSpace(q.EndTime); v != "" {
		where = append(where, "l.created_at <= ?")
		args = append(args, v)
	}
	if len(where) == 0 {
		return "", args
	}
	return " where " + strings.Join(where, " and "), args
}

// ListOperationLogs 返回操作日志分页列表与总数。
//
// 默认无时间条件时收敛到近 30 天，避免审计表全表扫描；调用方可显式传
// StartTime/EndTime 覆盖该默认（如导出全量时由 service 层控制）。
func (r *Repository) ListOperationLogs(ctx context.Context, q OperationLogListQuery) ([]OperationLogItem, int64, error) {
	page, pageSize := pagination.Normalize(q.Page, q.PageSize, maxOperationLogPageSize)
	if page > maxOperationLogPage {
		page = maxOperationLogPage
	}
	offset := pagination.Offset(page, pageSize)

	effective := q
	if strings.TrimSpace(effective.StartTime) == "" && strings.TrimSpace(effective.EndTime) == "" {
		effective.StartTime = businessclock.Now().AddDate(0, 0, -operationLogStatsDays).Format("2006-01-02 15:04:05")
	}
	filter, filterArgs := operationLogFilter(effective)

	var (
		items []OperationLogItem
		total int64
	)
	err := r.runQuery(ctx, "operation_log.list", func(ctx context.Context) error {
		if e := r.db.QueryRowContext(ctx, "select count(*) from bp_operation_log l"+filter, filterArgs...).Scan(&total); e != nil {
			return e
		}
		if total == 0 {
			return nil
		}
		args := append(append([]any{}, filterArgs...), pageSize, offset)
		rows, e := r.db.QueryContext(ctx,
			"select "+operationLogListColumns+" from bp_operation_log l"+filter+
				" order by l.created_at desc, l.id desc limit ? offset ?", args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var (
				it        OperationLogItem
				id        int64
				uid       int64
				status    int
				statusCde int
				elapsed   int
			)
			if e := rows.Scan(
				&id, &uid, &it.Username, &it.Module, &it.Action,
				&it.Summary, &it.Method, &it.Path, &it.TargetType, &it.TargetID,
				&status, &it.ErrorMessage, &statusCde, &it.ClientIP,
				&it.Country, &it.Province, &it.City, &it.UserAgent,
				&elapsed, &it.Detail, &it.CreatedAt,
			); e != nil {
				return e
			}
			it.ID = strconv.FormatInt(id, 10)
			it.UserID = strconv.FormatInt(uid, 10)
			it.Status = status == 1
			it.StatusCode = statusCde
			it.ElapsedMS = elapsed
			items = append(items, it)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	if items == nil {
		items = []OperationLogItem{}
	}
	return items, total, nil
}

// OperationLogStats uses the same event-time admin scope as the list. The old
// rollup contains unclassified/public noise and cannot answer this question.
// A bounded 30-day, actor_kind/created_at indexed scan keeps all cards consistent.
// Missing schema is an error, not fabricated zero-valued statistics.
func (r *Repository) OperationLogStats(ctx context.Context) (OperationLogStats, error) {
	stats := OperationLogStats{ModuleTotals: []OperationLogModuleStat{}, RecentActions: []OperationLogActionStat{}}
	now := businessclock.Now()
	since := now.AddDate(0, 0, -operationLogStatsDays).Format("2006-01-02 15:04:05")
	today := now.Format("2006-01-02")
	activeSince := now.AddDate(0, 0, -7).Format("2006-01-02 15:04:05")
	filter, args := operationLogFilter(OperationLogListQuery{Scope: operationLogScopeAdmin, StartTime: since})
	err := r.runQuery(ctx, "operation_log.stats", func(ctx context.Context) error {
		q := `select count(*),coalesce(sum(l.created_at >= ?),0),
   coalesce(sum(l.status=0),0),coalesce(sum(l.action='login'),0),
   count(distinct case when l.created_at>=? then l.user_id end)
   from bp_operation_log l` + filter
		totalsArgs := append([]any{today, activeSince}, args...)
		if err := r.db.QueryRowContext(ctx, q, totalsArgs...).Scan(&stats.TotalCount, &stats.TodayCount, &stats.FailureCount, &stats.LoginCount, &stats.ActiveUsers); err != nil {
			return err
		}
		rows, err := r.db.QueryContext(ctx, "select l.module,count(*) c from bp_operation_log l"+filter+" group by l.module order by c desc limit 20", args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v OperationLogModuleStat
			if err := rows.Scan(&v.Module, &v.Count); err != nil {
				rows.Close()
				return err
			}
			stats.ModuleTotals = append(stats.ModuleTotals, v)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		rows, err = r.db.QueryContext(ctx, "select l.action,l.module,count(*) c from bp_operation_log l"+filter+" group by l.action,l.module order by c desc limit 10", args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v OperationLogActionStat
			if err := rows.Scan(&v.Action, &v.Module, &v.Count); err != nil {
				return err
			}
			stats.RecentActions = append(stats.RecentActions, v)
		}
		return rows.Err()
	})
	return stats, err
}

// OperationLogTrend 返回近 days 天按天的操作日志趋势，供运营告警与图表使用。
//
// 与 OperationLogStats 一样只读预聚合表；缺表时返回空切片（不报错），
// 由上层决定是否展示「数据暂不可用」。
func (r *Repository) OperationLogTrend(ctx context.Context, days int) ([]OperationLogTrendPoint, error) {
	if days <= 0 || days > 90 {
		days = operationLogStatsDays
	}
	since := businessclock.Now().AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	points := make([]OperationLogTrendPoint, 0, days)
	err := r.runQuery(ctx, "operation_log.trend", func(ctx context.Context) error {
		rows, e := r.db.QueryContext(ctx,
			`select date_format(bucket_date,'%Y-%m-%d'),
			        coalesce(sum(total_count),0),
			        coalesce(sum(failure_count),0),
			        coalesce(sum(case when action='login' then total_count else 0 end),0)
			 from bp_operation_metric_day where bucket_date >= ?
			 group by bucket_date order by bucket_date asc`, since)
		if e != nil {
			if isMissingTableError(e) {
				return nil
			}
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var p OperationLogTrendPoint
			if e := rows.Scan(&p.Date, &p.TotalCount, &p.FailureCount, &p.LoginCount); e != nil {
				return e
			}
			points = append(points, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return points, nil
}

// rollupOperationLogDay 把明细表的某个自然日增量汇总进 bp_operation_metric_day。
//
// 由 analytics worker 按 checkpoint 高水位调用；使用 INSERT ... ON DUPLICATE KEY
// UPDATE 保证幂等（重复跑同一天不会翻倍）。
// first_seen/last_seen 取当天该 (module,action) 的最早/最晚一条，便于回溯。
func (r *Repository) rollupOperationLogDay(ctx context.Context, day string) (int64, error) {
	const stmt = `insert into bp_operation_metric_day
		(bucket_date,module,action,total_count,success_count,failure_count,first_seen,last_seen,updated_at)
		select date(l.created_at),coalesce(l.module,''),coalesce(l.action,''),
		       count(*),sum(case when l.status=1 then 1 else 0 end),sum(case when l.status=0 then 1 else 0 end),
		       min(l.created_at),max(l.created_at),now()
		from bp_operation_log l
		where l.created_at >= ? and l.created_at < date_add(?, interval 1 day)
		group by date(l.created_at),coalesce(l.module,''),coalesce(l.action,'')
		on duplicate key update
			total_count=values(total_count),
			success_count=values(success_count),
			failure_count=values(failure_count),
			first_seen=least(first_seen,values(first_seen)),
			last_seen=greatest(last_seen,values(last_seen)),
			updated_at=now()`
	res, err := r.db.ExecContext(ctx, stmt, day, day)
	if err != nil {
		return 0, err
	}
	// RowsAffected 在 on duplicate key update 下语义不精确（更新未变更行会返回 0），
	// 因此只作参考返回；驱动报错时按 0 处理，不让统计口径的不精确升级为任务失败。
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return n, nil
}

// countOperationLogBefore 统计某天之前的操作日志条数，供 checkpoint 回填定位。
func (r *Repository) countOperationLogBefore(ctx context.Context, point time.Time) (int64, error) {
	var n int64
	err := r.db.QueryRowContext(ctx,
		"select count(*) from bp_operation_log where created_at < ?", point).Scan(&n)
	return n, err
}

// isMissingTableError 判断错误是否为「表不存在」（MySQL 1146）。
// 迁移未执行时，操作日志相关查询应降级返回空结果而不是把错误抛给控制台。
// 这里在域内自包含一份实现，避免 observability 依赖 user 域的内部工具函数。
func isMissingTableError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "error 1176") || strings.Contains(message, " 1176 ") {
		return false
	}
	return strings.Contains(message, "error 1146") ||
		strings.Contains(message, "no such table") ||
		strings.Contains(message, "doesn't exist") && !strings.Contains(message, "key '")
}

// 保持 database/sql 的显式引用：operation_log_repository 依赖 *sql.DB 的方法集
// （ExecContext/QueryContext/QueryRowContext），类型断言写在编译期而非 init 期。
var _ = sql.ErrNoRows
