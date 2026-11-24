package riskcontrol

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync"

	"bugpk-api/internal/pkg/accountstatus"
	"bugpk-api/internal/pkg/iphash"
	"bugpk-api/internal/pkg/masking"
)

const (
	defaultLimit           = 20
	defaultWindowMinutes   = 10
	maxLimit               = 100
	maxWindowMinutes       = 1440
	minDistributedIPs      = 12
	minDistributedRequests = 30
	minProxyIPs            = 5
	minProxyRequests       = 12
	minSingleIPRequests    = 8
	riskBurstBucketMinutes = 5
)

var (
	// ErrLastActiveAdmin 表示这次封禁会让系统失去最后一个可用管理员。
	// 与用户管理侧的 guardLastActiveAdmin 同义：封禁有两个写入口，
	// 保护规则不能只装在其中一个上。
	ErrLastActiveAdmin = errors.New("last active administrator cannot be banned")

	riskInterfaceJoin  = "left join bp_interface_api a on a.id = l.interface_id"
	riskUserJoin       = "left join bp_user u on u.id = l.user_id"
	riskInterfaceName  = "coalesce(nullif(a.name,''), nullif(l.api_code,''), concat('API #', l.interface_id))"
	riskAPICode        = "coalesce(nullif(a.api_code,''), nullif(l.api_code,''), '')"
	proxyPoolGroupBy   = "l.user_id, l.interface_id, " + riskInterfaceName + ", " + riskAPICode + ", param_signature, " + riskTimeBucketExpression("l")
	distributedGroupBy = "l.interface_id, " + riskInterfaceName + ", " + riskAPICode + ", param_signature, " + riskTimeBucketExpression("l")
)

const maxRiskEvidenceItems = 12

// riskEvidenceSelect is appended to each event aggregation query. The values
// are deliberately bounded and privacy-safe: usernames, IP addresses, key
// prefixes and request methods are useful audit evidence, while raw request
// payloads and secrets remain excluded.
func riskEvidenceSelect(alias string) string {
	prefix := strings.TrimSpace(alias)
	if prefix == "" {
		prefix = "l"
	}
	return `
	       group_concat(distinct case
	         when ` + prefix + `.user_id is null then ''
	         else concat(` + prefix + `.user_id, ':', coalesce(nullif(u.username,''), concat('user-', ` + prefix + `.user_id)))
	       end separator '|') account_samples,
	       group_concat(distinct nullif(` + prefix + `.client_ip,'') separator '|') request_ips,
	       group_concat(distinct case
	         when ` + prefix + `.api_key_id is null then ''
	         else coalesce(nullif(` + prefix + `.api_key_prefix,''), concat('key-', ` + prefix + `.api_key_id))
	       end separator '|') api_key_samples,
	       group_concat(distinct nullif(upper(` + prefix + `.request_method),'') separator '|') request_methods,
	       coalesce(sum(` + prefix + `.success=1),0) success_count,
	       coalesce(sum(` + prefix + `.success=0),0) failed_count`
}

func openTrafficCondition(alias string) string {
	return accountstatus.ActiveTrafficSQL(alias)
}

func activeRollupUserCondition(alias string) string {
	return accountstatus.EffectiveStatusSQL(alias) + " = 1"
}

// riskRollupBaseCTEs reads only fixed five-minute contributor buckets. The
// cutoff is aligned to the same fixed boundary used while writing the bucket,
// so detection remains deterministic and every query is bounded by time even
// when the append-only source log grows into the trillions.
func riskRollupBaseCTEs() string {
	return `base as (
			select f.bucket_start,f.interface_id,f.request_fingerprint,f.user_id,f.api_key_id,
			       f.client_ip_hash,f.has_client_ip,f.client_ip,f.api_code,
			       f.country,f.province,f.city,f.call_count,f.success_count,f.failure_count,
			       f.elapsed_sum,f.first_seen,f.last_seen
			from bp_call_risk_fingerprint_5m f
			inner join bp_user active_user on active_user.id=f.user_id
			where f.bucket_start >= from_unixtime(
				floor(unix_timestamp(date_sub(now(), interval ? minute)) / 300) * 300
			)
			  and ` + activeRollupUserCondition("active_user") + `
		),
		risk_base as (
			select * from base
			where has_client_ip=1 and request_fingerprint<>''
		)`
}

// riskRollupAnalyticsBaseCTEs keeps summary and trend detection on the narrow
// covering column set. Detail, region and evidence columns are loaded only by
// queries that actually render them.
//
// No force index here on purpose: the 2026-09-05 incident fix (see FIXES.md,
// 修复项 3) removed the idx_bp_call_risk_distributed hint because it pinned a
// non-time-leading index and drove MySQL CPU to 190%. The optimizer picks a
// bucket_start-leading index for these bounded time-window scans.
func riskRollupAnalyticsBaseCTEs() string {
	return `base as (
			select f.bucket_start,f.interface_id,f.request_fingerprint,f.user_id,f.api_key_id,
			       f.client_ip_hash,f.has_client_ip,f.call_count
			from bp_call_risk_fingerprint_5m f
			inner join bp_user active_user on active_user.id=f.user_id
			where f.bucket_start >= from_unixtime(
				floor(unix_timestamp(date_sub(now(), interval ? minute)) / 300) * 300
			)
			  and ` + activeRollupUserCondition("active_user") + `
		),
		risk_base as (
			select * from base
			where has_client_ip=1 and request_fingerprint<>''
		)`
}

func riskFingerprintColumn(alias string) string {
	if strings.TrimSpace(alias) == "" {
		return "request_fingerprint"
	}
	return strings.TrimSpace(alias) + ".request_fingerprint"
}

func riskRequestSampleExpression(alias string) string {
	return "concat('fingerprint:', left(min(" + riskFingerprintColumn(alias) + "), 12))"
}

func riskTimeBucketExpression(alias string) string {
	prefix := strings.TrimSpace(alias)
	if prefix != "" {
		prefix += "."
	}
	return "floor(unix_timestamp(" + prefix + "created_at) / " + strconv.Itoa(riskBurstBucketMinutes*60) + ")"
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// ApplyAccountBan 封禁账号，并在同一事务内冻结该账号名下的接口 Key。
//
// 顺序不能反：先写 bp_user（拿到账号行的写锁），再写 bp_api_key。
// user.Repository.UpdateUser 走的是同一顺序，两条封禁入口之间因此不会互相死锁。
//
// Key 冻结后网关仍会在 2 秒内命中本地的 Key 鉴权缓存（keyAuthCacheTTL），
// 这段时间由 key.UserStatus 判定兜住；缓存本身也缓存了账号状态，
// 所以最坏情况下封禁在 2 秒内全链路生效。
func (r *Repository) ApplyAccountBan(ctx context.Context, userID int64, permanent bool, until any, reason string) error {
	permanentValue := 0
	if permanent {
		permanentValue = 1
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// 封禁是账号的最终处置手段，一旦把仅剩的管理员封掉，后台就再没有人能
	// 解封——包括做出这次操作的人自己。用户管理侧的更新接口已有同样的保护，
	// 这里必须保持一致，否则绕开用户管理直接走风控台就能锁死整个系统。
	if err := guardLastActiveAdminBan(ctx, tx, userID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		update bp_user
		set status = 0,
		    ban_permanent = ?,
		    ban_until = ?,
		    ban_reason = ?,
		    token_version = token_version + 1
		where id = ?
	`, permanentValue, until, reason, userID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		var exists int
		if err := tx.QueryRowContext(ctx, "select 1 from bp_user where id = ? limit 1", userID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return sql.ErrNoRows
			}
			return err
		}
	}
	// 全量封禁：账号不可用的同时，调用凭证也必须不可用。
	// 临时封禁记 1（解封恢复），永久封禁记 2（解封后仍需手动启用）。
	if _, err := tx.ExecContext(ctx, accountstatus.SuspendKeysSQL(permanent), userID); err != nil {
		return err
	}
	return tx.Commit()
}

// guardLastActiveAdminBan 阻止封禁最后一个可用管理员。
//
// 语义与 user.guardLastActiveAdmin 对齐：先锁 admin 角色行，把所有并发封禁
// 串行化，再确认目标本身是管理员，最后确认除目标之外还有可用管理员。
// 判定「可用」用 EffectiveStatusSQL，和鉴权、网关读的是同一个表达式，
// 避免出现「后台算作在用、鉴权却认为已停用」的口径分裂。
func guardLastActiveAdminBan(ctx context.Context, tx *sql.Tx, userID int64) error {
	var adminRoleID int64
	err := tx.QueryRowContext(ctx, `select id from bp_role where role_key = 'admin' limit 1 for update`).Scan(&adminRoleID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var membership int
	err = tx.QueryRowContext(ctx, `
		select 1
		from bp_user_role ur
		inner join bp_role r on r.id = ur.role_id
		where ur.user_id = ? and r.id = ? and r.status = 1
		limit 1
	`, userID, adminRoleID).Scan(&membership)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `
		select distinct u.id
		from bp_user u
		inner join bp_user_role ur on ur.user_id = u.id
		inner join bp_role r on r.id = ur.role_id
		where r.id = ? and r.status = 1 and u.id <> ? and `+accountstatus.EffectiveStatusSQL("u")+` = 1
		for update
	`, adminRoleID, userID)
	if err != nil {
		return err
	}
	remaining := 0
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		remaining++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	// 必须先关闭游标再返回：同一事务连接上还有写语句要跑。
	if err := rows.Close(); err != nil {
		return err
	}
	if remaining == 0 {
		return ErrLastActiveAdmin
	}
	return nil
}

func (r *Repository) Overview(ctx context.Context, query RiskQuery) (Overview, error) {
	query = normalizeQuery(query)
	events, err := r.RiskEvents(ctx, query)
	if err != nil {
		return Overview{}, err
	}
	return r.overviewWithEvents(ctx, query, events)
}

func (r *Repository) overviewWithEvents(ctx context.Context, query RiskQuery, events []RiskEvent) (Overview, error) {
	query = normalizeQuery(query)
	var totalRequests, failedRequests, uniqueIPs, uniqueParamSignatures int64
	var earthPointCount int64
	var singleIPEvents, proxyPoolEvents, distributedEvents int
	var riskyRequests int64
	var failedIPs []FailedIPRank
	var failedAccounts []FailedAccountRank
	var trend []TrendPoint
	err := runRiskQueries(ctx,
		func(queryCtx context.Context) error {
			return r.db.QueryRowContext(queryCtx, riskOverviewMetricsQuery(), query.WindowMinutes, query.WindowMinutes, query.WindowMinutes).Scan(&totalRequests, &failedRequests, &uniqueIPs, &uniqueParamSignatures, &earthPointCount)
		},
		func(queryCtx context.Context) error {
			var loadErr error
			failedIPs, loadErr = r.FailedIPRankings(queryCtx, query)
			return loadErr
		},
		func(queryCtx context.Context) error {
			var loadErr error
			failedAccounts, loadErr = r.FailedAccountRankings(queryCtx, query)
			return loadErr
		},
	)
	if err != nil {
		return Overview{}, err
	}
	var ipStats map[string]riskCandidateStats
	var accountStats map[string]riskCandidateStats
	singleIPEvents, proxyPoolEvents, distributedEvents, riskyRequests, trend, ipStats, accountStats, err = r.overviewAnalytics(ctx, query.WindowMinutes, failedIPs, failedAccounts)
	if err != nil {
		return Overview{}, err
	}
	for index := range failedIPs {
		if stats, ok := ipStats[failedIPs[index].ClientIP]; ok {
			failedIPs[index].TotalCalls = stats.TotalCalls
			failedIPs[index].UniqueUsers = stats.UniqueCount
			failedIPs[index].RiskHits = stats.RiskHits
		}
	}
	for index := range failedAccounts {
		if stats, ok := accountStats[failedAccounts[index].UserID]; ok {
			failedAccounts[index].TotalCalls = stats.TotalCalls
			failedAccounts[index].UniqueIPs = stats.UniqueCount
			failedAccounts[index].RiskHits = stats.RiskHits
		}
	}
	overview := Overview{
		DistributedEvents:    distributedEvents,
		EarthPointCount:      earthPointCount,
		Events:               events,
		FailedRequests:       failedRequests,
		Policy:               detectionPolicy(),
		ProxyPoolEvents:      proxyPoolEvents,
		RiskyRequests:        riskyRequests,
		SingleIPEvents:       singleIPEvents,
		TopFailedAccounts:    failedAccounts,
		TopFailedIPs:         failedIPs,
		TotalRequests:        totalRequests,
		Trend:                trend,
		UniqueIPs:            uniqueIPs,
		UniqueParamSignature: uniqueParamSignatures,
		WindowMinutes:        query.WindowMinutes,
	}
	for _, event := range events {
		if overview.TopAttackIP == "" && event.ClientIP != "" {
			overview.TopAttackIP = event.ClientIP
		}
		if overview.TopAttackParam == "" && event.RequestSample != "" {
			overview.TopAttackParam = event.RequestSample
		}
	}
	overview.RiskScore = overviewRiskScore(overview)
	return overview, nil
}

func riskOverviewMetricsQuery() string {
	return `
		with traffic as (
			select coalesce(sum(m.call_count),0) total_requests,
			       coalesce(sum(m.failure_count),0) failed_requests
			from bp_call_user_metric_minute m force index(primary)
			inner join bp_user u on u.id=m.user_id
			where m.bucket_start >= from_unixtime(
				floor(unix_timestamp(date_sub(now(), interval ? minute)) / 60) * 60
			)
			  and ` + activeRollupUserCondition("u") + `
		),
		fingerprints as (
			select count(distinct case when f.has_client_ip=1 then f.client_ip_hash end) unique_ips,
			       count(distinct nullif(f.request_fingerprint,'')) unique_fingerprints
			from bp_call_risk_fingerprint_5m f
			inner join bp_user u on u.id=f.user_id
			where f.bucket_start >= from_unixtime(
				floor(unix_timestamp(date_sub(now(), interval ? minute)) / 300) * 300
			)
			  and ` + activeRollupUserCondition("u") + `
		),
		locations as (
			select count(*) earth_points
			from (
				select r.country,r.province,r.city,r.isp,r.latitude,r.longitude
				from bp_call_region_hour r force index(idx_bp_call_region_hour_time)
				inner join bp_user u on u.id=r.user_id
				where r.bucket_start >= from_unixtime(
					floor(unix_timestamp(date_sub(now(), interval ? minute)) / 3600) * 3600
				)
				  and ` + activeRollupUserCondition("u") + `
				group by r.country,r.province,r.city,r.isp,r.latitude,r.longitude
			) points
		)
		select traffic.total_requests,traffic.failed_requests,
		       fingerprints.unique_ips,fingerprints.unique_fingerprints,locations.earth_points
		from traffic cross join fingerprints cross join locations
	`
}

type riskCandidateStats struct {
	RiskHits    int64
	TotalCalls  int64
	UniqueCount int64
}

func (r *Repository) overviewAnalytics(
	ctx context.Context,
	windowMinutes int,
	failedIPs []FailedIPRank,
	failedAccounts []FailedAccountRank,
) (int, int, int, int64, []TrendPoint, map[string]riskCandidateStats, map[string]riskCandidateStats, error) {
	bucketSeconds, labelFormat := riskTrendBucket(windowMinutes)
	args := make([]any, 0, len(failedIPs)*2+len(failedAccounts)+5)
	for _, rank := range failedIPs {
		digest := iphash.RiskDigest(rank.ClientIP)
		args = append(args, rank.ClientIP, digest[:])
	}
	for _, rank := range failedAccounts {
		userID, err := strconv.ParseInt(rank.UserID, 10, 64)
		if err != nil {
			return 0, 0, 0, 0, nil, nil, nil, err
		}
		args = append(args, userID)
	}
	args = append(args, windowMinutes, bucketSeconds, bucketSeconds, bucketSeconds, labelFormat)
	rows, err := r.db.QueryContext(
		ctx,
		riskOverviewAnalyticsQueryForCandidates(len(failedIPs), len(failedAccounts)),
		args...,
	)
	if err != nil {
		return 0, 0, 0, 0, nil, nil, nil, err
	}
	defer rows.Close()
	points := make([]TrendPoint, 0)
	ipStats := make(map[string]riskCandidateStats, len(failedIPs))
	accountStats := make(map[string]riskCandidateStats, len(failedAccounts))
	var singleIPEvents, proxyPoolEvents, distributedEvents int
	var riskyRequests int64
	for rows.Next() {
		var rowType string
		var point TrendPoint
		var label sql.NullString
		var entityKey sql.NullString
		var stats riskCandidateStats
		if err := rows.Scan(
			&rowType,
			&label,
			&point.TotalCalls,
			&point.RiskyEvents,
			&singleIPEvents,
			&proxyPoolEvents,
			&distributedEvents,
			&riskyRequests,
			&entityKey,
			&stats.TotalCalls,
			&stats.UniqueCount,
			&stats.RiskHits,
		); err != nil {
			return 0, 0, 0, 0, nil, nil, nil, err
		}
		switch rowType {
		case "trend":
			if !label.Valid {
				continue
			}
			point.Label = label.String
			points = append(points, point)
		case "ip":
			if entityKey.Valid {
				ipStats[entityKey.String] = stats
			}
		case "user":
			if entityKey.Valid {
				accountStats[entityKey.String] = stats
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, 0, 0, nil, nil, nil, err
	}
	return singleIPEvents, proxyPoolEvents, distributedEvents, riskyRequests, points, ipStats, accountStats, nil
}

// riskOverviewAnalyticsQuery evaluates the shared detection CTE once and uses
// it for the exact event totals, de-duplicated risky-request count, and chart
// buckets. Previously the summary and trend each reran the complete detection
// graph over the same time window.
func riskOverviewAnalyticsQuery() string {
	return riskOverviewAnalyticsQueryForCandidates(0, 0)
}

func riskOverviewAnalyticsQueryForCandidates(ipCount, userCount int) string {
	return `
		with ` + riskCandidateIPCTE(ipCount) + `,
		` + riskCandidateUserCTE(userCount) + `,
		` + riskRollupAnalyticsBaseCTEs() + `,
		` + riskRollupDetectionCTEs() + `,
		summary as (
			select (select count(*) from single_groups) single_events,
			       (select count(*) from proxy_groups) proxy_events,
			       (select count(*) from distributed_groups) distributed_events,
			       coalesce((select sum(call_count) from risky_contributors),0) risky_requests
		),
		totals as (
			select floor(unix_timestamp(bucket_start) / ?) trend_bucket,
			       sum(call_count) total_calls
			from base
			group by trend_bucket
		),
		risks as (
			select floor(unix_timestamp(bucket_start) / ?) trend_bucket,
			       sum(call_count) risky_requests
			from risky_contributors
			group by trend_bucket
		),
		buckets as (
			select t.trend_bucket,t.total_calls,coalesce(r.risky_requests,0) risky_requests
			from totals t
			left join risks r on r.trend_bucket=t.trend_bucket
			union all
			select null,0,0
			where not exists (select 1 from totals)
		),
		ip_stats as (
			select c.client_ip,
			       coalesce(sum(b.call_count),0) total_calls,
			       count(distinct b.user_id) unique_count,
			       coalesce(sum(r.call_count),0) risk_hits
			from candidate_ips c
			left join base b on b.client_ip_hash=c.client_ip_hash
			left join risky_contributors r
			  on r.bucket_start=b.bucket_start and r.interface_id=b.interface_id
			 and r.request_fingerprint=b.request_fingerprint and r.user_id=b.user_id
			 and r.api_key_id=b.api_key_id and r.client_ip_hash=b.client_ip_hash
			group by c.client_ip
		),
		user_stats as (
			select cast(c.user_id as char) user_id,
			       coalesce(sum(b.call_count),0) total_calls,
			       count(distinct case when b.has_client_ip=1 then b.client_ip_hash end) unique_count,
			       coalesce(sum(r.call_count),0) risk_hits
			from candidate_users c
			left join base b on b.user_id=c.user_id
			left join risky_contributors r
			  on r.bucket_start=b.bucket_start and r.interface_id=b.interface_id
			 and r.request_fingerprint=b.request_fingerprint and r.user_id=b.user_id
			 and r.api_key_id=b.api_key_id and r.client_ip_hash=b.client_ip_hash
			group by c.user_id
		),
		result_rows as (
			select 'trend' row_type,b.trend_bucket,
			       case when b.trend_bucket is null then null
			            else date_format(from_unixtime(b.trend_bucket * ?), ?) end label,
			       b.total_calls,b.risky_requests,
			       s.single_events,s.proxy_events,s.distributed_events,s.risky_requests total_risky_requests,
			       cast(null as char(64)) entity_key,0 entity_total_calls,0 entity_unique_count,0 entity_risk_hits
			from buckets b
			cross join summary s
			union all
			select 'ip',null,null,0,0,
			       s.single_events,s.proxy_events,s.distributed_events,s.risky_requests,
			       i.client_ip,i.total_calls,i.unique_count,i.risk_hits
			from ip_stats i
			cross join summary s
			union all
			select 'user',null,null,0,0,
			       s.single_events,s.proxy_events,s.distributed_events,s.risky_requests,
			       u.user_id,u.total_calls,u.unique_count,u.risk_hits
			from user_stats u
			cross join summary s
		)
		select row_type,label,total_calls,risky_requests,
		       single_events,proxy_events,distributed_events,total_risky_requests,
		       entity_key,entity_total_calls,entity_unique_count,entity_risk_hits
		from result_rows
		order by field(row_type,'trend','ip','user'),trend_bucket,entity_key
	`
}

func riskCandidateIPCTE(count int) string {
	if count <= 0 {
		return `candidate_ips(client_ip,client_ip_hash) as (
			select cast(null as char(64)),cast(null as binary(32)) where 1=0
		)`
	}
	rows := make([]string, count)
	for index := range rows {
		rows[index] = "select ?,?"
	}
	return "candidate_ips(client_ip,client_ip_hash) as (" + strings.Join(rows, " union all ") + ")"
}

func riskCandidateUserCTE(count int) string {
	if count <= 0 {
		return `candidate_users(user_id) as (
			select cast(null as signed) where 1=0
		)`
	}
	rows := make([]string, count)
	for index := range rows {
		rows[index] = "select ?"
	}
	return "candidate_users(user_id) as (" + strings.Join(rows, " union all ") + ")"
}

// FailedIPRankings returns the highest-volume failing client IPs in the
// selected window. The risk CTEs are shared with the event and trend queries so
// riskHits has exactly the same de-duplicated semantics as the rest of the
// dashboard.
func (r *Repository) FailedIPRankings(ctx context.Context, query RiskQuery) ([]FailedIPRank, error) {
	query = normalizeQuery(query)
	rows, err := r.db.QueryContext(
		ctx,
		failedIPRankingsQuery(),
		query.WindowMinutes,
		query.WindowMinutes,
		query.Limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ranks := make([]FailedIPRank, 0, query.Limit)
	for rows.Next() {
		var rank FailedIPRank
		var region, lastSeen sql.NullString
		var interfaceID sql.NullInt64
		var interfaceName, apiCode sql.NullString
		if err := rows.Scan(
			&rank.ClientIP,
			&region,
			&rank.FailedCalls,
			&rank.TotalCalls,
			&rank.UniqueUsers,
			&rank.RiskHits,
			&lastSeen,
			&interfaceID,
			&interfaceName,
			&apiCode,
			&rank.InterfaceFailedCalls,
		); err != nil {
			return nil, err
		}
		rank.Region = riskNullStringValue(region)
		rank.LastSeen = riskNullStringValue(lastSeen)
		if interfaceID.Valid {
			rank.InterfaceID = strconv.FormatInt(interfaceID.Int64, 10)
		}
		rank.InterfaceName = riskNullStringValue(interfaceName)
		rank.APICode = riskNullStringValue(apiCode)
		ranks = append(ranks, rank)
	}
	return ranks, rows.Err()
}

func failedIPRankingsQuery() string {
	return `
		with interface_failure_totals as (
			select f.client_ip,f.interface_id,
			       coalesce(nullif(a.name,''),nullif(max(f.api_code),''),concat('API #',f.interface_id)) interface_name,
			       coalesce(nullif(a.api_code,''),nullif(max(f.api_code),''),'') api_code,
			       sum(f.failure_count) interface_failed_calls,max(f.last_seen) interface_last_seen
			from bp_call_failure_ip_hour f force index(idx_bp_call_failure_ip_rank)
			inner join bp_user u on u.id=f.user_id
			left join bp_interface_api a on a.id=f.interface_id
			where f.bucket_start >= from_unixtime(
				floor(unix_timestamp(date_sub(now(), interval ? minute)) / 3600) * 3600
			)
			  and ` + activeRollupUserCondition("u") + `
			group by f.client_ip,f.interface_id,a.name,a.api_code
		),
		interface_failures as (
			select totals.*,
			       row_number() over(
			         partition by totals.client_ip
			         order by totals.interface_failed_calls desc,totals.interface_last_seen desc,totals.interface_id
			       ) rank_no
			from interface_failure_totals totals
		),
		ranks as (
			select f.client_ip,
			       nullif(concat_ws(' ',max(nullif(f.country,'')),max(nullif(f.province,'')),max(nullif(f.city,''))),'') region,
			       sum(f.failure_count) failed_calls,sum(f.failure_count) total_calls,
			       count(distinct f.user_id) unique_users,
			       0 risk_hits,
			       date_format(max(f.last_seen),'%Y-%m-%d %H:%i:%s') last_seen
			from bp_call_failure_ip_hour f force index(idx_bp_call_failure_ip_rank)
			inner join bp_user u on u.id=f.user_id
			where f.bucket_start >= from_unixtime(
				floor(unix_timestamp(date_sub(now(), interval ? minute)) / 3600) * 3600
			)
			  and ` + activeRollupUserCondition("u") + `
			group by f.client_ip
		)
		select ranks.client_ip,ranks.region,ranks.failed_calls,ranks.total_calls,
		       ranks.unique_users,ranks.risk_hits,ranks.last_seen,
		       failures.interface_id,failures.interface_name,failures.api_code,
		       coalesce(failures.interface_failed_calls,0)
		from ranks
		left join interface_failures failures
		  on failures.client_ip=ranks.client_ip and failures.rank_no=1
		order by ranks.failed_calls desc,ranks.total_calls desc,ranks.last_seen desc,ranks.client_ip
		limit ?
	`
}

// FailedAccountRankings returns the highest-volume failing accounts and joins
// current account metadata for maintenance work. It is intentionally separate
// from event evidence so the dashboard remains useful even when no CC pattern
// has crossed a detection threshold.
func (r *Repository) FailedAccountRankings(ctx context.Context, query RiskQuery) ([]FailedAccountRank, error) {
	query = normalizeQuery(query)
	rows, err := r.db.QueryContext(
		ctx,
		failedAccountRankingsQuery(),
		query.WindowMinutes,
		query.WindowMinutes,
		query.Limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ranks := make([]FailedAccountRank, 0, query.Limit)
	for rows.Next() {
		var rank FailedAccountRank
		var userID int64
		var username, displayName, lastSeen sql.NullString
		var interfaceID sql.NullInt64
		var interfaceName, apiCode sql.NullString
		if err := rows.Scan(
			&userID,
			&username,
			&displayName,
			&rank.Status,
			&rank.FailedCalls,
			&rank.TotalCalls,
			&rank.UniqueIPs,
			&rank.RiskHits,
			&lastSeen,
			&interfaceID,
			&interfaceName,
			&apiCode,
			&rank.InterfaceFailedCalls,
		); err != nil {
			return nil, err
		}
		rank.UserID = strconv.FormatInt(userID, 10)
		rank.Username = riskNullStringValue(username)
		rank.DisplayName = riskNullStringValue(displayName)
		rank.LastSeen = riskNullStringValue(lastSeen)
		if interfaceID.Valid {
			rank.InterfaceID = strconv.FormatInt(interfaceID.Int64, 10)
		}
		rank.InterfaceName = riskNullStringValue(interfaceName)
		rank.APICode = riskNullStringValue(apiCode)
		ranks = append(ranks, rank)
	}
	return ranks, rows.Err()
}

func nullStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return strings.TrimSpace(value.String)
}

func failedAccountRankingsQuery() string {
	return `
		with interface_failure_totals as (
			select f.user_id,f.interface_id,
			       coalesce(nullif(a.name,''),nullif(max(f.api_code),''),concat('API #',f.interface_id)) interface_name,
			       coalesce(nullif(a.api_code,''),nullif(max(f.api_code),''),'') api_code,
			       sum(f.failure_count) interface_failed_calls,max(f.last_seen) interface_last_seen
			from bp_call_failure_user_hour f force index(idx_bp_call_failure_user_rank)
			inner join bp_user active_user on active_user.id=f.user_id
			left join bp_interface_api a on a.id=f.interface_id
			where f.bucket_start >= from_unixtime(
				floor(unix_timestamp(date_sub(now(), interval ? minute)) / 3600) * 3600
			)
			  and ` + activeRollupUserCondition("active_user") + `
			group by f.user_id,f.interface_id,a.name,a.api_code
		),
		interface_failures as (
			select totals.*,
			       row_number() over(
			         partition by totals.user_id
			         order by totals.interface_failed_calls desc,totals.interface_last_seen desc,totals.interface_id
			       ) rank_no
			from interface_failure_totals totals
		),
		ranks as (
			select f.user_id,
			       u.username,
			       u.display_name,
			       coalesce(u.status,0) status,
			       sum(f.failure_count) failed_calls,sum(f.failure_count) total_calls,
			       count(distinct case when f.has_client_ip=1 then f.client_ip_hash end) unique_ips,
			       0 risk_hits,
			       date_format(max(f.last_seen),'%Y-%m-%d %H:%i:%s') last_seen
			from bp_call_failure_user_hour f force index(idx_bp_call_failure_user_rank)
			inner join bp_user u on u.id=f.user_id
			where f.bucket_start >= from_unixtime(
				floor(unix_timestamp(date_sub(now(), interval ? minute)) / 3600) * 3600
			)
			  and ` + activeRollupUserCondition("u") + `
			group by f.user_id,u.username,u.display_name,u.status
		)
		select ranks.user_id,ranks.username,ranks.display_name,ranks.status,
		       ranks.failed_calls,ranks.total_calls,ranks.unique_ips,ranks.risk_hits,ranks.last_seen,
		       failures.interface_id,failures.interface_name,failures.api_code,
		       coalesce(failures.interface_failed_calls,0)
		from ranks
		left join interface_failures failures
		  on failures.user_id=ranks.user_id and failures.rank_no=1
		order by ranks.failed_calls desc,ranks.total_calls desc,ranks.last_seen desc,ranks.user_id
		limit ?
	`
}

func detectionPolicy() DetectionPolicy {
	return DetectionPolicy{
		BurstWindowMinutes:  riskBurstBucketMinutes,
		DistributedIPs:      minDistributedIPs,
		DistributedRequests: minDistributedRequests,
		ProxyPoolIPs:        minProxyIPs,
		ProxyPoolRequests:   minProxyRequests,
		SingleIPRequests:    minSingleIPRequests,
	}
}

func riskSummaryQuery() string {
	return `
		with ` + riskRollupAnalyticsBaseCTEs() + `,
		` + riskRollupDetectionCTEs() + `
		select (select count(*) from single_groups),
		       (select count(*) from proxy_groups),
		       (select count(*) from distributed_groups),
		       coalesce((select sum(call_count) from risky_contributors),0)
	`
}

func riskDetectionCTEs() string {
	return `single_groups as (
			select client_ip,interface_id,request_fingerprint,burst_bucket
			from risk_base
			group by client_ip,interface_id,request_fingerprint,burst_bucket
			having count(*) >= ` + strconv.Itoa(minSingleIPRequests) + `
		),
		proxy_groups as (
			select user_id,interface_id,request_fingerprint,burst_bucket
			from risk_base
			where user_id is not null
			group by user_id,interface_id,request_fingerprint,burst_bucket
			having count(*) >= ` + strconv.Itoa(minProxyRequests) + `
			   and count(distinct client_ip) >= ` + strconv.Itoa(minProxyIPs) + `
		),
		distributed_groups as (
			select interface_id,request_fingerprint,burst_bucket
			from risk_base
			group by interface_id,request_fingerprint,burst_bucket
			having count(*) >= ` + strconv.Itoa(minDistributedRequests) + `
			   and count(distinct client_ip) >= ` + strconv.Itoa(minDistributedIPs) + `
		),
		risky_ids as (
			select b.id
			from risk_base b
			inner join single_groups g
			  on g.client_ip=b.client_ip and g.interface_id=b.interface_id
			 and g.request_fingerprint=b.request_fingerprint and g.burst_bucket=b.burst_bucket
			union
			select b.id
			from risk_base b
			inner join proxy_groups g
			  on g.user_id=b.user_id and g.interface_id=b.interface_id
			 and g.request_fingerprint=b.request_fingerprint and g.burst_bucket=b.burst_bucket
			union
			select b.id
			from risk_base b
			inner join distributed_groups g
			  on g.interface_id=b.interface_id
			 and g.request_fingerprint=b.request_fingerprint and g.burst_bucket=b.burst_bucket
		)`
}

// riskRollupDetectionCTEs applies the same thresholds as the raw-log detector
// to pre-aggregated five-minute contributors. Counts are summed while unique
// identities are derived from the exact contributor dimensions retained by
// bp_call_risk_fingerprint_5m.
//
// Each grouping lists bucket_start first and then the remaining columns in the
// order the matching index already stores them:
//
//	single_groups      -> idx_bp_call_risk_single      (bucket_start, client_ip_hash, ...)
//	proxy_groups       -> idx_bp_call_risk_proxy       (bucket_start, user_id, ...)
//	distributed_groups -> idx_bp_call_risk_distributed (bucket_start, interface_id, ...)
//
// That order is not cosmetic. A GROUP BY whose column order does not match the
// index it reads cannot be streamed, so MySQL materialises the entire window
// into a temporary table and counts there. Reordering distributed_groups onto
// its index prefix removed the temporary table and its filesort on production
// data (1.9M rows per 24-hour window): the same groups in 1.83s instead of
// 5.98s, measured, with no change to the detected set. The joins back into
// risky_contributors match by column name, so the projection order of these
// CTEs carries no meaning.
func riskRollupDetectionCTEs() string {
	return `single_groups as (
			select bucket_start,client_ip_hash,interface_id,request_fingerprint
			from risk_base
			group by bucket_start,client_ip_hash,interface_id,request_fingerprint
			having sum(call_count) >= ` + strconv.Itoa(minSingleIPRequests) + `
		),
		proxy_groups as (
			select bucket_start,user_id,interface_id,request_fingerprint
			from risk_base
			group by bucket_start,user_id,interface_id,request_fingerprint
			having sum(call_count) >= ` + strconv.Itoa(minProxyRequests) + `
			   and count(distinct client_ip_hash) >= ` + strconv.Itoa(minProxyIPs) + `
		),
		distributed_groups as (
			select bucket_start,interface_id,request_fingerprint
			from risk_base
			group by bucket_start,interface_id,request_fingerprint
			having sum(call_count) >= ` + strconv.Itoa(minDistributedRequests) + `
			   and count(distinct client_ip_hash) >= ` + strconv.Itoa(minDistributedIPs) + `
		),
		risky_contributors as (
			select b.* from risk_base b
			inner join single_groups g
			  on g.client_ip_hash=b.client_ip_hash and g.interface_id=b.interface_id
			 and g.request_fingerprint=b.request_fingerprint and g.bucket_start=b.bucket_start
			union
			select b.* from risk_base b
			inner join proxy_groups g
			  on g.user_id=b.user_id and g.interface_id=b.interface_id
			 and g.request_fingerprint=b.request_fingerprint and g.bucket_start=b.bucket_start
			union
			select b.* from risk_base b
			inner join distributed_groups g
			  on g.interface_id=b.interface_id and g.request_fingerprint=b.request_fingerprint
			 and g.bucket_start=b.bucket_start
		)`
}

func (r *Repository) RiskEvents(ctx context.Context, query RiskQuery) ([]RiskEvent, error) {
	query = normalizeQuery(query)
	var single, proxy, distributed []RiskEvent
	err := runRiskQueries(ctx,
		func(queryCtx context.Context) error {
			var loadErr error
			single, loadErr = r.singleIPEvents(queryCtx, query)
			return loadErr
		},
		func(queryCtx context.Context) error {
			var loadErr error
			proxy, loadErr = r.proxyPoolEvents(queryCtx, query)
			return loadErr
		},
		func(queryCtx context.Context) error {
			var loadErr error
			distributed, loadErr = r.distributedEvents(queryCtx, query)
			return loadErr
		},
	)
	if err != nil {
		return nil, err
	}
	events := append(single, proxy...)
	events = append(events, distributed...)
	for index := range events {
		events[index].Severity, events[index].RiskScore = classifyEvent(events[index])
		events[index].RequestSample = sanitizeSample(events[index].RequestSample)
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].RiskScore == events[j].RiskScore {
			return events[i].RequestCount > events[j].RequestCount
		}
		return events[i].RiskScore > events[j].RiskScore
	})
	if len(events) > query.Limit {
		events = events[:query.Limit]
	}
	return events, nil
}

func runRiskQueries(ctx context.Context, queries ...func(context.Context) error) error {
	queryCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, len(queries))
	var wg sync.WaitGroup
	for _, query := range queries {
		if query == nil {
			continue
		}
		wg.Add(1)
		go func(load func(context.Context) error) {
			defer wg.Done()
			if err := load(queryCtx); err != nil {
				select {
				case errCh <- err:
				default:
				}
				cancel()
			}
		}(query)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) Earth(ctx context.Context, query RiskQuery) (EarthOverview, error) {
	query = normalizeQuery(query)
	events, err := r.RiskEvents(ctx, RiskQuery{Limit: query.Limit, WindowMinutes: query.WindowMinutes})
	if err != nil {
		return EarthOverview{}, err
	}
	return r.earthWithEvents(ctx, query, events)
}

func (r *Repository) earthWithEvents(ctx context.Context, query RiskQuery, events []RiskEvent) (EarthOverview, error) {
	query = normalizeQuery(query)
	rows, err := r.db.QueryContext(ctx, riskEarthQuery(), query.WindowMinutes, query.WindowMinutes, query.Limit)
	if err != nil {
		return EarthOverview{}, err
	}
	defer rows.Close()
	points := []EarthPoint{}
	var totalCalls int64
	var totalRisk int64
	var activePoints int64
	for rows.Next() {
		var point EarthPoint
		if err := rows.Scan(
			&point.Country,
			&point.Province,
			&point.City,
			&point.ISP,
			&point.Latitude,
			&point.Longitude,
			&point.CallCount,
			&point.SuccessCalls,
			&point.FailedCalls,
			&point.UniqueIPs,
			&point.RiskHits,
			&activePoints,
			&totalCalls,
			&totalRisk,
		); err != nil {
			return EarthOverview{}, err
		}
		point.Region = joinRegion(point.Country, point.Province, point.City)
		point.RiskLevel = classifyPoint(point)
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return EarthOverview{}, err
	}
	return EarthOverview{
		ActivePoints:  int(activePoints),
		Events:        events,
		Points:        points,
		TotalCalls:    totalCalls,
		TotalRiskHits: totalRisk,
		WindowMinutes: query.WindowMinutes,
	}, nil
}

func (r *Repository) HomeSnapshot(ctx context.Context, query RiskQuery) (HomeSnapshot, error) {
	query = normalizeQuery(query)
	var singleEvents, proxyEvents, distributedEvents int
	var riskyRequests int64
	if err := r.db.QueryRowContext(ctx, riskSummaryQuery(), query.WindowMinutes).Scan(
		&singleEvents,
		&proxyEvents,
		&distributedEvents,
		&riskyRequests,
	); err != nil {
		return HomeSnapshot{}, err
	}
	var totalRequests int64
	if err := r.db.QueryRowContext(ctx, `
		select coalesce(sum(m.call_count),0)
		from bp_call_user_metric_minute m force index(primary)
		inner join bp_user u on u.id=m.user_id
		where m.bucket_start >= from_unixtime(
			floor(unix_timestamp(date_sub(now(), interval ? minute)) / 60) * 60
		)
		  and `+activeRollupUserCondition("u")+`
	`, query.WindowMinutes).Scan(&totalRequests); err != nil {
		return HomeSnapshot{}, err
	}
	rows, err := r.db.QueryContext(ctx, riskHomePointsQuery(), query.WindowMinutes, query.WindowMinutes, query.Limit)
	if err != nil {
		return HomeSnapshot{}, err
	}
	defer rows.Close()
	points := make([]HomeRiskPoint, 0)
	for rows.Next() {
		var point HomeRiskPoint
		var country, province, city string
		if err := rows.Scan(
			&country,
			&province,
			&city,
			&point.Latitude,
			&point.Longitude,
			&point.RiskHits,
		); err != nil {
			return HomeSnapshot{}, err
		}
		point.Region = joinRegion(country, province, city)
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return HomeSnapshot{}, err
	}
	overview := Overview{
		DistributedEvents: distributedEvents,
		ProxyPoolEvents:   proxyEvents,
		RiskyRequests:     riskyRequests,
		SingleIPEvents:    singleEvents,
		TotalRequests:     totalRequests,
	}
	return HomeSnapshot{
		EventCount:    singleEvents + proxyEvents + distributedEvents,
		Points:        points,
		RiskScore:     overviewRiskScore(overview),
		RiskyRequests: riskyRequests,
	}, nil
}

func riskHomePointsQuery() string {
	return `
		with ` + riskRollupBaseCTEs() + `,
		` + riskRollupDetectionCTEs() + `,
		risk_regions as (
			select country,province,city,sum(call_count) risk_hits
			from risky_contributors
			group by country,province,city
		)
		select r.country,r.province,r.city,
		       cast(avg(r.latitude) as char),cast(avg(r.longitude) as char),
		       max(rr.risk_hits) risk_hits
		from bp_call_region_hour r force index(idx_bp_call_region_hour_time)
		inner join bp_user u on u.id=r.user_id
		inner join risk_regions rr
		  on rr.country=r.country and rr.province=r.province and rr.city=r.city
		where r.bucket_start >= from_unixtime(
			floor(unix_timestamp(date_sub(now(), interval ? minute)) / 3600) * 3600
		)
		  and ` + activeRollupUserCondition("u") + `
		group by r.country,r.province,r.city
		order by risk_hits desc,r.country,r.province,r.city
		limit ?
	`
}

func riskEarthQuery() string {
	return `
		with ` + riskRollupBaseCTEs() + `,
		` + riskRollupDetectionCTEs() + `,
		risk_regions as (
			select country,province,city,sum(call_count) risk_hits
			from risky_contributors
			group by country,province,city
		),
		region_points as (
			select r.country,r.province,r.city,r.isp,r.latitude,r.longitude,
			       sum(r.call_count) call_count,sum(r.success_count) success_calls,
			       sum(r.failure_count) failed_calls,count(distinct r.client_ip_hash) unique_ips,
			       coalesce(max(rr.risk_hits),0) risk_hits
			from bp_call_region_ip_hour r force index(idx_bp_call_region_ip_time)
			inner join bp_user u on u.id=r.user_id
			left join risk_regions rr
			  on rr.country=r.country and rr.province=r.province and rr.city=r.city
			where r.bucket_start >= from_unixtime(
				floor(unix_timestamp(date_sub(now(), interval ? minute)) / 3600) * 3600
			)
			  and ` + activeRollupUserCondition("u") + `
			group by r.country,r.province,r.city,r.isp,r.latitude,r.longitude
		)
		select country,province,city,isp,
		       cast(latitude as char),cast(longitude as char),
		       call_count,success_calls,failed_calls,unique_ips,risk_hits,
		       count(*) over() active_points,
		       sum(call_count) over() total_calls,
		       sum(risk_hits) over() total_risk_hits
		from region_points
		order by call_count desc,country,province,city,latitude,longitude
		limit ?
	`
}

func (r *Repository) singleIPEvents(ctx context.Context, query RiskQuery) ([]RiskEvent, error) {
	rows, err := r.db.QueryContext(ctx, riskRollupEventsQuery("single"), query.WindowMinutes, minSingleIPRequests, query.Limit*2)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows, "SINGLE_IP_CC", "单 IP 在短时间内多次请求相同接口和相同参数")
}

func (r *Repository) proxyPoolEvents(ctx context.Context, query RiskQuery) ([]RiskEvent, error) {
	rows, err := r.db.QueryContext(ctx, riskRollupEventsQuery("proxy"), query.WindowMinutes, minProxyRequests, minProxyIPs, query.Limit*2)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows, "PROXY_POOL_SAME_PARAMS", "多 IP 在短时间内请求相同接口和相同参数，疑似代理池 CC")
}

func (r *Repository) distributedEvents(ctx context.Context, query RiskQuery) ([]RiskEvent, error) {
	rows, err := r.db.QueryContext(ctx, riskRollupEventsQuery("distributed"), query.WindowMinutes, minDistributedRequests, minDistributedIPs, query.Limit*2)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEvents(rows, "DISTRIBUTED_SAME_PARAMS", "多账号或多密钥通过大量 IP 在短时间内请求相同接口和相同参数，疑似分布式代理池 CC")
}

func riskRollupEventsQuery(kind string) string {
	clientIP := "''"
	groupBy := "f.bucket_start,f.interface_id,f.request_fingerprint"
	joinDetected := "g.bucket_start=f.bucket_start and g.interface_id=f.interface_id and g.request_fingerprint=f.request_fingerprint"
	having := "sum(f.call_count) >= ? and count(distinct f.client_ip_hash) >= ?"
	orderBy := "unique_ip_count desc,unique_user_count desc,request_count desc"
	switch kind {
	case "single":
		clientIP = "max(f.client_ip)"
		groupBy = "f.bucket_start,f.client_ip_hash,f.interface_id,f.request_fingerprint"
		joinDetected = "g.bucket_start=f.bucket_start and g.client_ip_hash=f.client_ip_hash and g.interface_id=f.interface_id and g.request_fingerprint=f.request_fingerprint"
		having = "sum(f.call_count) >= ?"
		orderBy = "request_count desc"
	case "proxy":
		groupBy = "f.bucket_start,f.user_id,f.interface_id,f.request_fingerprint"
		joinDetected = "g.bucket_start=f.bucket_start and g.user_id=f.user_id and g.interface_id=f.interface_id and g.request_fingerprint=f.request_fingerprint"
		orderBy = "unique_ip_count desc,request_count desc"
	}
	// No force index here on purpose: the 2026-09-05 incident fix (FIXES.md,
	// 修复项 3) removed the per-kind hints so the optimizer can use a
	// bucket_start-leading index for the bounded time window.
	return `
		with detected_groups as (
			select ` + groupBy + `,
			       sum(f.call_count) request_count,
			       count(distinct f.client_ip_hash) unique_ip_count,
			       count(distinct f.user_id) unique_user_count
			from bp_call_risk_fingerprint_5m f
			inner join bp_user active_user on active_user.id=f.user_id
			where f.bucket_start >= from_unixtime(
				floor(unix_timestamp(date_sub(now(), interval ? minute)) / 300) * 300
			)
			  and f.has_client_ip=1 and f.request_fingerprint<>''
			  and ` + activeRollupUserCondition("active_user") + `
			group by ` + groupBy + `
			having ` + having + `
			order by ` + orderBy + `
			limit ?
		)
		select ` + clientIP + ` client_ip,
		       f.interface_id,
		       coalesce(nullif(a.name,''),nullif(max(f.api_code),''),concat('API #',f.interface_id)) interface_name,
		       coalesce(nullif(a.api_code,''),nullif(max(f.api_code),''),'') api_code,
		       f.request_fingerprint param_signature,
		       concat('fingerprint:',left(f.request_fingerprint,12)) request_sample,
		       nullif(concat_ws(' ',max(nullif(f.country,'')),max(nullif(f.province,'')),max(nullif(f.city,''))),'') region,
		       sum(f.call_count) request_count,
		       count(distinct case when f.has_client_ip=1 then f.client_ip_hash end) unique_ip_count,
		       count(distinct f.user_id) unique_user_count,
		       count(distinct nullif(f.api_key_id,0)) unique_key_count,
		       coalesce(sum(f.elapsed_sum)/nullif(sum(f.call_count),0),0) avg_elapsed_ms,
		       date_format(min(f.first_seen),'%Y-%m-%d %H:%i:%s') first_seen,
		       date_format(max(f.last_seen),'%Y-%m-%d %H:%i:%s') last_seen,
		       group_concat(distinct concat(f.user_id,':',coalesce(nullif(u.username,''),concat('user-',f.user_id))) separator '|') account_samples,
		       group_concat(distinct nullif(f.client_ip,'') separator '|') request_ips,
		       group_concat(distinct case when f.api_key_id=0 then '' else concat('key-',f.api_key_id) end separator '|') api_key_samples,
		       '' request_methods,
		       sum(f.success_count) success_count,
		       sum(f.failure_count) failed_count
		from bp_call_risk_fingerprint_5m f
		inner join detected_groups g on ` + joinDetected + `
		inner join bp_user u on u.id=f.user_id
		left join bp_interface_api a on a.id=f.interface_id
		where f.has_client_ip=1 and f.request_fingerprint<>''
		  and ` + activeRollupUserCondition("u") + `
		group by ` + groupBy + `,a.name,a.api_code
		order by ` + orderBy
}

func (r *Repository) trend(ctx context.Context, windowMinutes int) ([]TrendPoint, error) {
	bucketSeconds, labelFormat := riskTrendBucket(windowMinutes)
	rows, err := r.db.QueryContext(ctx, riskTrendQuery(), windowMinutes, bucketSeconds, bucketSeconds, bucketSeconds, bucketSeconds, bucketSeconds, labelFormat)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	points := []TrendPoint{}
	for rows.Next() {
		var point TrendPoint
		if err := rows.Scan(&point.Label, &point.TotalCalls, &point.RiskyEvents); err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	return points, rows.Err()
}

func riskTrendBucket(windowMinutes int) (int, string) {
	switch {
	case windowMinutes <= 60:
		return 60, "%H:%i"
	case windowMinutes <= 360:
		return 300, "%H:%i"
	default:
		return 900, "%m-%d %H:%i"
	}
}

func riskTrendQuery() string {
	return `
		with ` + riskRollupAnalyticsBaseCTEs() + `,
		` + riskRollupDetectionCTEs() + `,
		totals as (
			select floor(unix_timestamp(bucket_start) / ?) trend_bucket,sum(call_count) total_calls
			from base
			group by floor(unix_timestamp(bucket_start) / ?)
		),
		risks as (
			select floor(unix_timestamp(bucket_start) / ?) trend_bucket,sum(call_count) risky_requests
			from risky_contributors
			group by floor(unix_timestamp(bucket_start) / ?)
		)
		select date_format(from_unixtime(t.trend_bucket * ?), ?) label,
		       t.total_calls,
		       coalesce(r.risky_requests,0)
		from totals t
		left join risks r on r.trend_bucket=t.trend_bucket
		order by t.trend_bucket
	`
}

func scanEvents(rows *sql.Rows, eventType, reason string) ([]RiskEvent, error) {
	events := []RiskEvent{}
	for rows.Next() {
		var event RiskEvent
		var interfaceID int64
		var avgElapsed float64
		var accountSamples string
		var requestIPs string
		var apiKeySamples string
		var requestMethods string
		if err := rows.Scan(
			&event.ClientIP,
			&interfaceID,
			&event.InterfaceName,
			&event.APICode,
			&event.ParamSignature,
			&event.RequestSample,
			&event.Region,
			&event.RequestCount,
			&event.UniqueIPCount,
			&event.UniqueUserCount,
			&event.UniqueKeyCount,
			&avgElapsed,
			&event.FirstSeen,
			&event.LastSeen,
			&accountSamples,
			&requestIPs,
			&apiKeySamples,
			&requestMethods,
			&event.SuccessCount,
			&event.FailedCount,
		); err != nil {
			return nil, err
		}
		event.EventType = eventType
		event.InterfaceID = strconv.FormatInt(interfaceID, 10)
		event.Reason = reason
		event.AvgElapsedMs = int64(avgElapsed)
		event.Accounts = parseRiskAccountSamples(accountSamples)
		event.RequestIPs = splitRiskEvidenceSamples(requestIPs)
		event.APIKeys = splitRiskEvidenceSamples(apiKeySamples)
		event.RequestMethods = splitRiskEvidenceSamples(requestMethods)
		events = append(events, event)
	}
	return events, rows.Err()
}

func splitRiskEvidenceSamples(raw string) []string {
	values := make([]string, 0, maxRiskEvidenceItems)
	seen := make(map[string]struct{}, maxRiskEvidenceItems)
	for _, value := range strings.Split(raw, "|") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
		if len(values) >= maxRiskEvidenceItems {
			break
		}
	}
	return values
}

func riskNullStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func parseRiskAccountSamples(raw string) []RiskAccount {
	values := make([]RiskAccount, 0, maxRiskEvidenceItems)
	seen := make(map[string]struct{}, maxRiskEvidenceItems)
	for _, value := range splitRiskEvidenceSamples(raw) {
		parts := strings.SplitN(value, ":", 2)
		account := RiskAccount{ID: strings.TrimSpace(parts[0])}
		if len(parts) == 2 {
			account.Username = strings.TrimSpace(parts[1])
		}
		if account.ID == "" {
			continue
		}
		if account.Username == "" {
			account.Username = "user-" + account.ID
		}
		key := account.ID + ":" + account.Username
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		values = append(values, account)
		if len(values) >= maxRiskEvidenceItems {
			break
		}
	}
	return values
}

func normalizeQuery(query RiskQuery) RiskQuery {
	if query.WindowMinutes <= 0 {
		query.WindowMinutes = defaultWindowMinutes
	}
	if query.WindowMinutes > maxWindowMinutes {
		query.WindowMinutes = maxWindowMinutes
	}
	if query.Limit <= 0 {
		query.Limit = defaultLimit
	}
	if query.Limit > maxLimit {
		query.Limit = maxLimit
	}
	return query
}

func classifyEvent(event RiskEvent) (string, int) {
	score := 0
	switch event.EventType {
	case "SINGLE_IP_CC":
		score = 35 + int(event.RequestCount/2)
	case "PROXY_POOL_SAME_PARAMS":
		score = 45 + int(event.UniqueIPCount*3) + int(event.RequestCount/3)
	case "DISTRIBUTED_SAME_PARAMS":
		score = 55 + int(event.UniqueIPCount*2) + int(event.UniqueUserCount*3) + int(event.RequestCount/5)
	default:
		score = 20
	}
	if score > 100 {
		score = 100
	}
	switch {
	case score >= 80:
		return "CRITICAL", score
	case score >= 60:
		return "HIGH", score
	case score >= 40:
		return "MEDIUM", score
	default:
		return "LOW", score
	}
}

func overviewRiskScore(overview Overview) int {
	score := overview.SingleIPEvents*16 + overview.ProxyPoolEvents*24 + overview.DistributedEvents*30
	if overview.TotalRequests > 0 {
		score += int(float64(overview.RiskyRequests) / float64(overview.TotalRequests) * 60)
	}
	switch {
	case overview.InvalidKeyAttempts >= 100:
		score += 20
	case overview.InvalidKeyAttempts >= 30:
		score += 10
	case overview.InvalidKeyAttempts >= 10:
		score += 5
	}
	blockedScore := int(overview.InvalidKeyBlocked * 2)
	if blockedScore > 30 {
		blockedScore = 30
	}
	score += blockedScore
	if score > 100 {
		return 100
	}
	return score
}

func trimSample(value string) string {
	value = strings.TrimSpace(value)
	if len([]rune(value)) <= 180 {
		return value
	}
	runes := []rune(value)
	return string(runes[:180]) + "..."
}

func sanitizeSample(value string) string {
	return trimSample(masking.ErrorText(value))
}

func joinRegion(parts ...string) string {
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			values = append(values, part)
		}
	}
	if len(values) == 0 {
		return "未知区域"
	}
	return strings.Join(values, " ")
}

func classifyPoint(point EarthPoint) string {
	if point.RiskHits >= 20 || point.FailedCalls*2 >= point.CallCount {
		return "CRITICAL"
	}
	if point.RiskHits >= 8 || point.UniqueIPs >= 10 {
		return "HIGH"
	}
	if point.RiskHits > 0 || point.FailedCalls > 0 {
		return "MEDIUM"
	}
	return "LOW"
}
