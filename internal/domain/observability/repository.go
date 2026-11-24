package observability

import (
	"bugpk-api/internal/domain/revenue"
	"bugpk-api/internal/pkg/businessclock"
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/pkg/accountstatus"
	"bugpk-api/internal/pkg/calloutcome"
	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/masking"
	"bugpk-api/internal/pkg/pagination"
)

const (
	dashboardAggregationDays = 30
	maxCallLogPage           = 1_000
	maxCallLogPageSize       = 200
	defaultQueryTimeout      = 15 * time.Second
	slowQueryThreshold       = time.Second
)

type Repository struct {
	db           *sql.DB
	queryTimeout time.Duration
	slowQuery    func(name string, took time.Duration)
	callLogSlots chan struct{}
}

const callLogListColumns = `
		l.id,l.interface_id,coalesce(nullif(a.name,''),nullif(l.api_code,''),concat('API #',l.interface_id)),l.user_id,
		coalesce(nullif(u.username,''),concat('User #',l.user_id)),coalesce(u.display_name,''),coalesce(l.api_key_id,0),coalesce(l.api_key_prefix,''),coalesce(l.api_code,''),
		l.request_method,coalesce(l.request_params,''),coalesce(l.request_value,''),l.request_size,l.response_status,l.success,l.billable,l.charge_type,
		l.billing_status,coalesce(cast(l.billing_snapshot as char),''),coalesce(l.billing_record_id,0),cast(l.charge_amount as char),l.point_price,
		l.elapsed_ms,coalesce(l.polling_mode,''),coalesce(l.upstream_url,''),l.upstream_switched,coalesce(l.error_message,''),coalesce(l.client_ip,''),
		coalesce(l.client_region,''),coalesce(l.ip_country,''),coalesce(l.ip_province,''),coalesce(l.ip_city,''),coalesce(l.ip_isp,''),
		coalesce(cast(l.latitude as char),''),coalesce(cast(l.longitude as char),''),coalesce(l.region_provider,''),coalesce(l.trace_id,''),
		l.response_size,l.response_saved_size,l.response_truncated,l.user_visible,date_format(l.created_at,'%Y-%m-%d %H:%i:%s')`

const callLogExportColumns = `
		l.id,date_format(l.created_at,'%Y-%m-%d %H:%i:%s'),
		coalesce(nullif(a.name,''),nullif(l.api_code,''),concat('API #',l.interface_id)),coalesce(l.api_code,''),
		coalesce(nullif(u.username,''),concat('User #',l.user_id)),coalesce(l.api_key_prefix,''),l.request_method,
		l.response_status,l.success,l.billable,l.charge_type,cast(l.charge_amount as char),l.point_price,l.elapsed_ms,
		coalesce(l.client_region,''),coalesce(l.client_ip,''),coalesce(l.trace_id,''),coalesce(l.error_message,''),coalesce(l.request_params,'')`

func NewRepository(db *sql.DB) *Repository {
	limit := 8
	// Each admitted list may run two SQL queries. Reserve at least half of a
	// bounded pool for authentication, gateway billing and other domains.
	if db != nil {
		if size := db.Stats().MaxOpenConnections; size > 0 {
			limit = max(1, min(limit, size/4))
		}
	}
	return &Repository{db: db, queryTimeout: defaultQueryTimeout, callLogSlots: make(chan struct{}, limit)}
}

// SetQueryTimeout 覆盖调用日志查询的上下文超时；非正值恢复默认。
func (r *Repository) SetQueryTimeout(d time.Duration) {
	if d <= 0 {
		d = defaultQueryTimeout
	}
	r.queryTimeout = d
}

// SetSlowQueryLogger 注册慢查询回调（耗时 >= slowQueryThreshold 时触发一次）。
func (r *Repository) SetSlowQueryLogger(fn func(name string, took time.Duration)) {
	r.slowQuery = fn
}

// runQuery 为单条（组）查询附加超时并测量耗时；fn 收到的 ctx 已带 deadline。
func (r *Repository) runQuery(ctx context.Context, name string, fn func(ctx context.Context) error) error {
	if r.queryTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.queryTimeout)
		defer cancel()
	}
	start := time.Now()
	err := fn(ctx)
	if took := time.Since(start); r.slowQuery != nil && took >= slowQueryThreshold {
		r.slowQuery(name, took)
	}
	return err
}

func callLogFilterLookupJoins(params CallLogListParams) string {
	keyword := strings.TrimSpace(params.Keyword) != ""
	if !keyword && !params.BillingOnly {
		return ""
	}
	joins := `
		left join bp_interface_api a on a.id = l.interface_id`
	if keyword {
		joins += `
		left join bp_user u on u.id = l.user_id`
	}
	return joins
}

func callLogCountQuery(params CallLogListParams, where string) string {
	// The total is part of the public pagination contract.  Do not apply the
	// page-size/depth guard used by the list query here: doing so reports a
	// truncated value as if it were the number of matching logs.
	return `select count(*)
		from bp_interface_call_log l` + callLogFilterLookupJoins(params) + `
		` + where
}

func callLogListQuery(params CallLogListParams, where string, keyset bool) string {
	limit := "order by l.created_at desc,l.id desc limit ?"
	if !keyset {
		limit += " offset ?"
	}
	return `
		select ` + callLogListColumns + `
		from (
			select l.id,l.created_at
			from bp_interface_call_log l` + callLogFilterLookupJoins(params) + `
			` + where + `
			` + limit + `
		) filtered
		inner join bp_interface_call_log l on l.id=filtered.id and l.created_at=filtered.created_at
		left join bp_interface_api a on a.id = l.interface_id
		left join bp_user u on u.id = l.user_id
		order by l.created_at desc,l.id desc`
}

func callLogNextCursor(items []CallLogItem) string {
	if len(items) == 0 {
		return ""
	}
	last := items[len(items)-1]
	id, err := strconv.ParseInt(last.ID, 10, 64)
	if err != nil || id <= 0 {
		return ""
	}
	return encodeCallLogCursor(callLogCursor{CreatedAt: last.CreateTime, ID: id})
}

func callLogExportQuery(params CallLogListParams, where string) string {
	return `
		select ` + callLogExportColumns + `
		from (
			select l.id,l.created_at
			from bp_interface_call_log l` + callLogFilterLookupJoins(params) + `
			` + where + `
			order by l.created_at desc,l.id desc limit ?
		) filtered
		inner join bp_interface_call_log l on l.id=filtered.id and l.created_at=filtered.created_at
		left join bp_interface_api a on a.id = l.interface_id
		left join bp_user u on u.id = l.user_id
		order by l.created_at desc,l.id desc`
}

func (r *Repository) ListCallLogs(ctx context.Context, params CallLogListParams, admin bool, ownerID int64) (PageResult[CallLogItem], error) {
	budget := 15 * time.Second
	if params.SkipTotal {
		budget = 6 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	release, err := r.acquireCallLogSlot(ctx)
	if err != nil {
		return PageResult[CallLogItem]{}, err
	}
	defer release()
	page, size := normalizePage(params.Page, params.PageSize)
	baseWhere, baseArgs := callLogWhere(params, admin, ownerID)
	where := baseWhere
	args := append([]any(nil), baseArgs...)
	cursor, err := decodeCallLogCursor(params.Cursor)
	if err != nil {
		return PageResult[CallLogItem]{}, err
	}
	keyset := cursor.ID > 0
	if keyset {
		where += " and (l.created_at<? or (l.created_at=? and l.id<?))"
		args = append(args, cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}
	// Always fetch one lookahead row.  Besides detecting the end of a keyset
	// page, this lets the first (page-number) request emit a cursor that the
	// client can use for subsequent deep pages without paying OFFSET costs.
	fetchSize := size + 1
	listArgs := append(append([]any(nil), args...), fetchSize)
	if !keyset {
		listArgs = append(listArgs, pagination.Offset(page, size))
	}
	listQuery := callLogListQuery(params, where, keyset)

	var (
		total int64
		items []CallLogItem
	)
	// Count the complete filter set.  In cursor mode `args` also contains the
	// seek predicate, so use the base arguments instead of counting only the
	// current cursor tail.
	countArgs := append([]any(nil), baseArgs...)
	err = dbx.RunParallelContext(ctx,
		func(ctx context.Context) error {
			if params.SkipTotal {
				total = -1
				return nil
			}
			return r.runQuery(ctx, "call_log_count", func(ctx context.Context) error {
				return r.db.QueryRowContext(ctx, callLogCountQuery(params, baseWhere), countArgs...).Scan(&total)
			})
		},
		func(ctx context.Context) error {
			return r.runQuery(ctx, "call_log_list", func(ctx context.Context) error {
				rows, err := r.db.QueryContext(ctx, listQuery, listArgs...)
				if err != nil {
					return err
				}
				defer rows.Close()
				items = make([]CallLogItem, 0, size)
				for rows.Next() {
					item, err := scanCallLog(rows, false)
					if err != nil {
						return err
					}
					items = append(items, item)
				}
				return rows.Err()
			})
		},
	)
	if err != nil {
		return PageResult[CallLogItem]{}, err
	}
	result := PageResult[CallLogItem]{Items: items, Total: total, TotalOmitted: params.SkipTotal}
	if len(result.Items) > size {
		result.Items = result.Items[:size]
		result.NextCursor = callLogNextCursor(result.Items)
	}
	return result, nil
}

// ExportCallLogBatch 以 keyset 游标分批导出窄列数据；恒取 limit+1 行判定是否还有下一批。
func (r *Repository) ExportCallLogBatch(ctx context.Context, params CallLogListParams, admin bool, ownerID int64, limit int, cursorToken string) ([]CallLogItem, string, error) {
	if limit <= 0 {
		return nil, "", fmt.Errorf("invalid call-log export batch limit")
	}
	release, err := r.acquireCallLogSlot(ctx)
	if err != nil {
		return nil, "", err
	}
	defer release()
	where, args := callLogWhere(params, admin, ownerID)
	cursor, err := decodeCallLogCursor(cursorToken)
	if err != nil {
		return nil, "", err
	}
	if cursor.ID > 0 {
		where += " and (l.created_at<? or (l.created_at=? and l.id<?))"
		args = append(args, cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}
	args = append(args, limit+1)
	var items []CallLogItem
	err = r.runQuery(ctx, "call_log_export", func(ctx context.Context) error {
		rows, err := r.db.QueryContext(ctx, callLogExportQuery(params, where), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		items = make([]CallLogItem, 0, limit)
		for rows.Next() {
			item, err := scanCallLogExport(rows)
			if err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = callLogNextCursor(items)
	}
	return items, next, nil
}

func (r *Repository) CallLogDetail(ctx context.Context, id, ownerID int64, admin bool) (CallLogItem, error) {
	where := "where l.id=?"
	args := []any{id}
	if !admin {
		where += " and l.user_id=? and l.user_visible=1"
		args = append(args, ownerID)
	}
	var item CallLogItem
	err := r.runQuery(ctx, "call_log_detail", func(ctx context.Context) error {
		rows, err := r.db.QueryContext(ctx, `
			select l.id,l.interface_id,coalesce(nullif(a.name,''),nullif(l.api_code,''),concat('API #',l.interface_id)),l.user_id,
			       coalesce(nullif(u.username,''),concat('User #',l.user_id)),coalesce(u.display_name,''),coalesce(l.api_key_id,0),coalesce(l.api_key_prefix,''),coalesce(l.api_code,''),
			       l.request_method,coalesce(l.request_params,''),coalesce(l.request_value,''),l.request_size,l.response_status,l.success,l.billable,l.charge_type,
			       l.billing_status,coalesce(cast(l.billing_snapshot as char),''),coalesce(l.billing_record_id,0),cast(l.charge_amount as char),l.point_price,
			       l.elapsed_ms,coalesce(l.polling_mode,''),coalesce(l.upstream_url,''),l.upstream_switched,coalesce(l.error_message,''),coalesce(l.client_ip,''),
			       coalesce(l.client_region,''),coalesce(l.ip_country,''),coalesce(l.ip_province,''),coalesce(l.ip_city,''),coalesce(l.ip_isp,''),
			       coalesce(cast(l.latitude as char),''),coalesce(cast(l.longitude as char),''),coalesce(l.region_provider,''),coalesce(l.trace_id,''),
			       l.response_size,l.response_saved_size,l.response_truncated,l.user_visible,date_format(l.created_at,'%Y-%m-%d %H:%i:%s'),coalesce(b.response_body,l.response_body,'')
			from bp_interface_call_log l
			left join bp_interface_call_log_body b on b.log_id = l.id
			left join bp_interface_api a on a.id = l.interface_id
			left join bp_user u on u.id = l.user_id
			`+where, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return sql.ErrNoRows
		}
		item, err = scanCallLog(rows, true)
		return err
	})
	if err != nil {
		return CallLogItem{}, err
	}
	return item, nil
}

func callLogWhere(params CallLogListParams, admin bool, ownerID int64) (string, []any) {
	where := []string{"where 1=1"}
	args := []any{}
	if !admin {
		where = append(where, "l.user_id=?")
		args = append(args, ownerID)
		if params.UserVisibleOnly {
			where = append(where, "l.user_visible=1")
		}
	} else if params.UserID > 0 {
		where = append(where, "l.user_id=?")
		args = append(args, params.UserID)
	}
	if params.InterfaceID > 0 {
		where = append(where, "l.interface_id=?")
		args = append(args, params.InterfaceID)
	}
	if strings.TrimSpace(params.APICode) != "" {
		where = append(where, "l.api_code=?")
		args = append(args, strings.TrimSpace(params.APICode))
	}
	if strings.TrimSpace(params.ClientIP) != "" {
		where = append(where, "l.client_ip=?")
		args = append(args, strings.TrimSpace(params.ClientIP))
	}
	if strings.TrimSpace(params.TraceID) != "" {
		where = append(where, "l.trace_id=?")
		args = append(args, strings.TrimSpace(params.TraceID))
	}
	if params.APIKeyID > 0 {
		where = append(where, "l.api_key_id=?")
		args = append(args, params.APIKeyID)
	}
	if params.Success != nil {
		where = append(where, "l.success=?")
		args = append(args, *params.Success)
	}
	if params.BillingOnly {
		// 后台“计费记录”入口复用调用日志页面。这里只保留真实产生扣费的调用，
		// 并跟随接口当前的计费总开关/计费日志开关：任一开关关闭后，该接口的
		// 扣费记录即从该视图隐藏；原始调用日志仍在“调用日志”中完整保留。
		where = append(where,
			"l.billable=1",
			"a.billing_enabled=1",
			"a.billing_log_enabled=1",
		)
	}
	if params.Billable != nil {
		where = append(where, "l.billable=?")
		args = append(args, *params.Billable)
	}
	if strings.TrimSpace(params.ChargeType) != "" {
		where = append(where, "l.charge_type=?")
		args = append(args, strings.TrimSpace(params.ChargeType))
	}
	if strings.TrimSpace(params.BillingStatus) != "" {
		where = append(where, "l.billing_status=?")
		args = append(args, strings.TrimSpace(params.BillingStatus))
	}
	if strings.TrimSpace(params.StartTime) != "" {
		where = append(where, "l.created_at>=?")
		args = append(args, params.StartTime)
	}
	if strings.TrimSpace(params.EndTime) != "" {
		where = append(where, "l.created_at<=?")
		args = append(args, params.EndTime)
	}
	if params.MinElapsedMs > 0 {
		where = append(where, "l.elapsed_ms>=?")
		args = append(args, params.MinElapsedMs)
	}
	if params.MaxElapsedMs > 0 {
		where = append(where, "l.elapsed_ms<=?")
		args = append(args, params.MaxElapsedMs)
	}
	if strings.TrimSpace(params.Region) != "" {
		like := "%" + strings.TrimSpace(params.Region) + "%"
		where = append(where, "(l.client_region like ? or l.ip_country like ? or l.ip_province like ? or l.ip_city like ? or l.ip_isp like ?)")
		args = append(args, like, like, like, like, like)
	}
	if strings.TrimSpace(params.Keyword) != "" {
		like := "%" + strings.TrimSpace(params.Keyword) + "%"
		where = append(where, "(u.username like ? or u.display_name like ? or a.name like ? or a.api_code like ? or l.request_params like ? or l.client_ip like ? or l.trace_id like ?)")
		args = append(args, like, like, like, like, like, like, like)
	}
	return strings.Join(where, " and "), args
}

func scanCallLog(rows interface{ Scan(dest ...any) error }, withBody bool) (CallLogItem, error) {
	var item CallLogItem
	var id, interfaceID, userID, apiKeyID, billingRecordID int64
	var success, billable, switched, truncated, visible int
	dest := []any{&id, &interfaceID, &item.InterfaceName, &userID, &item.Username, &item.RealName, &apiKeyID, &item.APIKeyPrefix, &item.APICode,
		&item.RequestMethod, &item.RequestParams, &item.RequestValue, &item.RequestSize, &item.ResponseStatus, &success, &billable, &item.ChargeType,
		&item.BillingStatus, &item.BillingSnapshot, &billingRecordID, &item.ChargeAmount, &item.PointPrice, &item.ElapsedMs, &item.PollingMode,
		&item.UpstreamURL, &switched, &item.ErrorMessage, &item.ClientIP, &item.ClientRegion, &item.IPCountry, &item.IPProvince, &item.IPCity,
		&item.IPISP, &item.Latitude, &item.Longitude, &item.RegionProvider, &item.TraceID, &item.ResponseSize, &item.ResponseSaved, &truncated, &visible, &item.CreateTime}
	if withBody {
		dest = append(dest, &item.ResponseBody)
	}
	if err := rows.Scan(dest...); err != nil {
		return CallLogItem{}, err
	}
	item.ID = strconv.FormatInt(id, 10)
	item.InterfaceID = strconv.FormatInt(interfaceID, 10)
	item.UserID = strconv.FormatInt(userID, 10)
	if apiKeyID > 0 {
		item.APIKeyID = strconv.FormatInt(apiKeyID, 10)
	}
	if billingRecordID > 0 {
		item.BillingRecordID = strconv.FormatInt(billingRecordID, 10)
	}
	item.Success = success == 1
	item.Billable = billable == 1
	item.UpstreamSwitched = switched == 1
	// Historical rows may predate endpoint redaction.  Sanitize at the read
	// boundary as a second line of defense, including the explicit admin/raw
	// view and detail endpoints.
	item.UpstreamURL = masking.UpstreamReference(item.UpstreamURL)
	item.ResponseTruncated = truncated == 1
	item.UserVisible = visible == 1
	applyResponseMetadata(&item)
	return item, nil
}

func scanCallLogExport(rows interface{ Scan(dest ...any) error }) (CallLogItem, error) {
	var item CallLogItem
	var id int64
	var success, billable int
	if err := rows.Scan(
		&id,
		&item.CreateTime,
		&item.InterfaceName,
		&item.APICode,
		&item.Username,
		&item.APIKeyPrefix,
		&item.RequestMethod,
		&item.ResponseStatus,
		&success,
		&billable,
		&item.ChargeType,
		&item.ChargeAmount,
		&item.PointPrice,
		&item.ElapsedMs,
		&item.ClientRegion,
		&item.ClientIP,
		&item.TraceID,
		&item.ErrorMessage,
		&item.RequestParams,
	); err != nil {
		return CallLogItem{}, err
	}
	item.ID = strconv.FormatInt(id, 10)
	item.Success = success == 1
	item.Billable = billable == 1
	item.UpstreamURL = masking.UpstreamReference(item.UpstreamURL)
	return item, nil
}

func applyResponseMetadata(item *CallLogItem) {
	if item.ResponseSaved == 0 && item.ResponseBody != "" {
		item.ResponseSaved = int64(len(item.ResponseBody))
	}
}

func (r *Repository) HideUserLog(ctx context.Context, id, userID int64) error {
	result, err := r.db.ExecContext(ctx, "update bp_interface_call_log set user_visible=0 where id=? and user_id=?", id, userID)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

const (
	callLogCleanupBatchSize  = 1000
	callLogCleanupMaxBatches = 10
	userAutoHideBatchSize    = 1000
	userAutoHideMaxBatches   = 5
)

func (r *Repository) CleanupLogs(ctx context.Context, before string) (int64, bool, error) {
	return r.cleanupLogsBatched(ctx, before, callLogCleanupBatchSize, callLogCleanupMaxBatches)
}

func (r *Repository) cleanupLogsBatched(ctx context.Context, before string, batchSize, maxBatches int) (int64, bool, error) {
	if batchSize <= 0 || maxBatches <= 0 {
		return 0, false, fmt.Errorf("invalid call-log cleanup batch limits")
	}
	var deleted int64
	for batch := 0; batch < maxBatches; batch++ {
		result, err := r.db.ExecContext(ctx, `
			delete from bp_interface_call_log
			where created_at < ?
			order by created_at,id
			limit ?
		`, before, batchSize)
		if err != nil {
			return deleted, false, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return deleted, false, err
		}
		deleted += affected
		// 响应体副表按冗余 created_at 同保留期清理；历史版本只删主表，
		// 这里顺带回收此前遗留的孤儿 body 行（主行已删、响应体仍在的记录）。
		bodyResult, err := r.db.ExecContext(ctx, `
			delete from bp_interface_call_log_body
			where created_at < ?
			limit ?
		`, before, batchSize)
		if err != nil {
			return deleted, false, err
		}
		bodyAffected, err := bodyResult.RowsAffected()
		if err != nil {
			return deleted, false, err
		}
		deleted += bodyAffected
		if affected < int64(batchSize) && bodyAffected < int64(batchSize) {
			return deleted, false, nil
		}
	}
	var hasMore int
	err := r.db.QueryRowContext(ctx, `
		select exists(
			select 1 from bp_interface_call_log where created_at < ? limit 1
		) or exists(
			select 1 from bp_interface_call_log_body where created_at < ? limit 1
		)
	`, before, before).Scan(&hasMore)
	return deleted, hasMore == 1, err
}

func (r *Repository) CleanupConfig(ctx context.Context, userID int64) (CleanupConfig, error) {
	var cfg CleanupConfig
	err := r.db.QueryRowContext(ctx, `select retention_days,auto_hide_enabled,coalesce(date_format(last_cleanup_at,'%Y-%m-%d %H:%i:%s'),'') from bp_user_log_cleanup_config where user_id=?`, userID).Scan(&cfg.RetentionDays, &cfg.AutoHideEnabled, &cfg.LastCleanupAt)
	if err == sql.ErrNoRows {
		return CleanupConfig{RetentionDays: 90, AutoHideEnabled: 0}, nil
	}
	return cfg, err
}

func (r *Repository) SaveCleanupConfig(ctx context.Context, userID int64, cfg CleanupConfig) error {
	_, err := r.db.ExecContext(ctx, `
		insert into bp_user_log_cleanup_config(user_id,retention_days,auto_hide_enabled)
		values(?,?,?)
		on duplicate key update retention_days=values(retention_days),auto_hide_enabled=values(auto_hide_enabled)
	`, userID, cfg.RetentionDays, cfg.AutoHideEnabled)
	return err
}

func (r *Repository) ApplyUserAutoHide(ctx context.Context, userID int64) (int64, error) {
	affected, _, err := r.applyUserAutoHideBatched(ctx, userID, userAutoHideBatchSize, userAutoHideMaxBatches)
	return affected, err
}

func (r *Repository) applyUserAutoHideBatched(ctx context.Context, userID int64, batchSize, maxBatches int) (int64, bool, error) {
	if batchSize <= 0 || maxBatches <= 0 {
		return 0, false, fmt.Errorf("invalid user log auto-hide batch limits")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()

	var retentionDays, enabled, recentlyCleaned int
	err = tx.QueryRowContext(ctx, `
		select retention_days,auto_hide_enabled,
		       last_cleanup_at is not null and last_cleanup_at >= date_sub(now(), interval 1 hour)
		from bp_user_log_cleanup_config
		where user_id=? for update
	`, userID).Scan(&retentionDays, &enabled, &recentlyCleaned)
	if err == sql.ErrNoRows || enabled != 1 || recentlyCleaned == 1 {
		return 0, false, tx.Commit()
	}
	if err != nil {
		return 0, false, err
	}
	if retentionDays < 1 || retentionDays > 3650 {
		return 0, false, fmt.Errorf("invalid log retention days: %d", retentionDays)
	}

	var affected int64
	for batch := 0; batch < maxBatches; batch++ {
		result, execErr := tx.ExecContext(ctx, `
			update bp_interface_call_log
			set user_visible=0
			where user_id=? and user_visible=1
			  and created_at < date_sub(now(), interval ? day)
			order by created_at,id
			limit ?
		`, userID, retentionDays, batchSize)
		if execErr != nil {
			return affected, false, execErr
		}
		batchAffected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return affected, false, rowsErr
		}
		affected += batchAffected
		if batchAffected < int64(batchSize) {
			if _, execErr = tx.ExecContext(ctx, `
				update bp_user_log_cleanup_config set last_cleanup_at=now() where user_id=?
			`, userID); execErr != nil {
				return affected, false, execErr
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return affected, false, commitErr
			}
			return affected, false, nil
		}
	}

	var hasMore int
	if err = tx.QueryRowContext(ctx, `
		select exists(
			select 1 from bp_interface_call_log
			where user_id=? and user_visible=1
			  and created_at < date_sub(now(), interval ? day)
			limit 1
		)
	`, userID, retentionDays).Scan(&hasMore); err != nil {
		return affected, false, err
	}
	if _, err = tx.ExecContext(ctx, `
		update bp_user_log_cleanup_config set last_cleanup_at=now() where user_id=?
	`, userID); err != nil {
		return affected, false, err
	}
	if err = tx.Commit(); err != nil {
		return affected, false, err
	}
	return affected, hasMore == 1, nil
}

func dashboardOpenTrafficCondition(alias string) string {
	return accountstatus.ActiveTrafficSQL(alias)
}

func dashboardActiveRollupUserCondition(alias string) string {
	return accountstatus.EffectiveStatusSQL(alias) + " = 1"
}

func dashboardOverviewMetricsQuery() string {
	return `
		select cast(coalesce(sum(m.call_count),0) as unsigned),
		       cast(coalesce(sum(m.success_count),0) as unsigned),
		       cast(coalesce(sum(m.failure_count),0) as unsigned),
		       cast(coalesce(sum(m.billable_count),0) as unsigned),
		       cast(coalesce(sum(m.charge_amount),0) as char)
		from bp_call_user_metric_minute m force index (primary)
		inner join bp_user u on u.id=m.user_id
		where m.bucket_start>=current_date()
		  and ` + dashboardActiveRollupUserCondition("u") + `
	`
}

// dashboardOverviewMetrics 读取「平台累计数据」的今日调用相关指标。
// rollup 表存在时直接使用其结果；只有增量升级尚未创建 rollup 表时才从源表兜底。
// 不能把“聚合结果为 0”当作“聚合表缺失”，否则聚合延迟或当天没有调用时，
// 管理员工作台的每次刷新都会扫描高基数的原始调用日志。
func (r *Repository) dashboardOverviewMetrics(ctx context.Context, item *DashboardOverview) error {
	if err := r.db.QueryRowContext(ctx, dashboardOverviewMetricsQuery()).Scan(
		&item.TodayCalls, &item.TodaySuccessCalls, &item.TodayFailedCalls, &item.TodayBillableCalls, &item.TodayChargeAmount,
	); err == nil {
		return nil
	} else if !isMissingTableErr(err) {
		return err
	}
	return r.dashboardOverviewMetricsDirect(ctx, item)
}

// dashboardOverviewMetricsDirect 是「平台累计数据」今日调用的源表直查兜底。
func (r *Repository) dashboardOverviewMetricsDirect(ctx context.Context, item *DashboardOverview) error {
	return r.db.QueryRowContext(ctx, dashboardOverviewMetricsDirectQuery()).Scan(
		&item.TodayCalls, &item.TodaySuccessCalls, &item.TodayFailedCalls, &item.TodayBillableCalls, &item.TodayChargeAmount,
	)
}

// dashboardOverviewMetricsDirectQuery 直接从网关调用日志统计当日调用量（源表真相口径），
// 与 dashboardOverviewMetricsQuery 一致的活跃用户过滤（dashboardActiveRollupUserCondition），
// 仅在 bp_call_user_metric_minute 缺失时兜底。
func dashboardOverviewMetricsDirectQuery() string {
	return `
		select coalesce(count(*),0),
		       coalesce(sum(l.success=1),0),
		       coalesce(sum(` + calloutcome.SQLLegacyFailure("l") + `),0),
		       coalesce(sum(l.billable=1),0),
		       cast(coalesce(sum(l.charge_amount),0) as char)
		from bp_interface_call_log l
		inner join bp_user u on u.id = l.user_id
		where l.created_at >= current_date()
		  and ` + dashboardActiveRollupUserCondition("u") + `
	`
}

// dashboardCallTrendQuery 读日粒度投影：分钟表下一天一个用户 1440 行，日表 1 行，
// 因此 7/30 天窗口的扫描量按用户数 ×1440 下降。仍然 join bp_user，因为活跃账号
// 过滤是读时条件。
func dashboardCallTrendQuery() string {
	return `
		select date_format(m.bucket_date,'%Y-%m-%d'),
		       cast(coalesce(sum(m.call_count),0) as unsigned),
		       cast(coalesce(sum(m.success_count),0) as unsigned),
		       cast(coalesce(sum(m.failure_count),0) as unsigned),
		       cast(coalesce(sum(m.billable_count),0) as unsigned),
		       cast(coalesce(sum(m.charge_amount),0) as char)
		from bp_call_user_metric_day m
		inner join bp_user u on u.id=m.user_id
		where m.bucket_date>=date_sub(current_date(), interval ? day)
		  and ` + dashboardActiveRollupUserCondition("u") + `
		group by date_format(m.bucket_date,'%Y-%m-%d')
	`
}

// dashboardCallTrendMinuteQuery 是日表缺失（000100 迁移尚未应用）时的回退，
// 口径与日表逐字对应。
func dashboardCallTrendMinuteQuery() string {
	return `
		select date_format(m.bucket_start,'%Y-%m-%d'),
		       cast(coalesce(sum(m.call_count),0) as unsigned),
		       cast(coalesce(sum(m.success_count),0) as unsigned),
		       cast(coalesce(sum(m.failure_count),0) as unsigned),
		       cast(coalesce(sum(m.billable_count),0) as unsigned),
		       cast(coalesce(sum(m.charge_amount),0) as char)
		from bp_call_user_metric_minute m force index (primary)
		inner join bp_user u on u.id=m.user_id
		where m.bucket_start>=date_sub(current_date(), interval ? day)
		  and ` + dashboardActiveRollupUserCondition("u") + `
		group by date_format(m.bucket_start,'%Y-%m-%d')
	`
}

func dashboardRegionRankingQuery() string {
	return `
		select coalesce(nullif(concat_ws(' ',nullif(r.country,''),nullif(r.province,''),nullif(r.city,'')),''),'Unknown') name,
		       cast(sum(r.call_count) as unsigned) calls
		from bp_call_region_hour r force index (idx_bp_call_region_hour_time)
		inner join bp_user u on u.id=r.user_id
		where r.bucket_start >= date_sub(now(), interval ? day)
		  and ` + dashboardActiveRollupUserCondition("u") + `
		group by name
		having name <> ''
		order by calls desc
		limit ?
	`
}

// dashboardHotAPIsQuery 读已经存在的按 API/用户/天的投影（000089 引入），
// 把榜单的扫描量从（接口 × 用户 × 分钟）压到（接口 × 用户 × 天）。
func dashboardHotAPIsQuery() string {
	return `
		select a.id,a.api_code,a.name,cast(coalesce(traffic.call_count,0) as unsigned) calls
		from bp_interface_api a
		left join (
			select m.interface_id,sum(m.call_count) call_count
			from bp_call_api_metric_day m
			inner join bp_user u on u.id=m.user_id
			where m.bucket_date >= date_sub(current_date(), interval ? day)
			  and ` + dashboardActiveRollupUserCondition("u") + `
			group by m.interface_id
		) traffic on traffic.interface_id=a.id
		where a.status=1
		order by coalesce(traffic.call_count,0) desc,a.is_top desc,a.is_featured desc,a.id desc
		limit ?
	`
}

// dashboardHotAPIsMinuteQuery 是日表缺失时的回退，口径与日表逐字对应。
func dashboardHotAPIsMinuteQuery() string {
	return `
		select a.id,a.api_code,a.name,cast(coalesce(traffic.call_count,0) as unsigned) calls
		from bp_interface_api a
		left join (
			select m.interface_id,sum(m.call_count) call_count
			from bp_call_api_metric_minute m force index (primary)
			inner join bp_user u on u.id=m.user_id
			where m.bucket_start >= date_sub(current_date(), interval ? day)
			  and m.bucket_start < current_date() + interval 1 day
			  and ` + dashboardActiveRollupUserCondition("u") + `
			group by m.interface_id
		) traffic on traffic.interface_id=a.id
		where a.status=1
		order by coalesce(traffic.call_count,0) desc,a.is_top desc,a.is_featured desc,a.id desc
		limit ?
	`
}

func dashboardTotalRevenueQuery() string { return revenue.TotalQuery }

func (r *Repository) dashboardTotalRevenue(ctx context.Context) (string, error) {
	return revenue.NewRepository(r.db).Total(ctx)
}

// isMissingTableErr reports whether the database error means the queried table
// does not exist (MySQL error 1146), excluding a missing-index error (1176).
func isMissingTableErr(err error) bool {
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

func (r *Repository) Overview(ctx context.Context) (DashboardOverview, error) {
	var item DashboardOverview
	// The five metric queries are independent, but the whole dashboard read is
	// one bounded query unit: timeout and slow-query telemetry must include both
	// the parallel work and any fallback query selected by a metric.
	err := r.runQuery(ctx, "dashboard_overview", func(queryCtx context.Context) error {
		return dbx.RunParallelContext(
			queryCtx,
			func(queryCtx context.Context) error {
				return r.db.QueryRowContext(queryCtx, "select count(*),coalesce(sum(status=1),0) from bp_interface_api").Scan(&item.APITotal, &item.APIEnabled)
			},
			func(queryCtx context.Context) error {
				return r.db.QueryRowContext(queryCtx, "select count(*),coalesce(sum(status=1),0) from bp_api_key").Scan(&item.KeyTotal, &item.KeyEnabled)
			},
			func(queryCtx context.Context) error {
				return r.db.QueryRowContext(queryCtx, `
					select count(*),coalesce(sum(u.created_at>=current_date()),0)
					from bp_user u
					where `+accountstatus.EffectiveStatusSQL("u")+` = 1
				`).Scan(&item.TotalUsers, &item.TodayNewUsers)
			},
			func(queryCtx context.Context) error {
				return r.dashboardOverviewMetrics(queryCtx, &item)
			},
			func(queryCtx context.Context) error {
				total, err := r.dashboardTotalRevenue(queryCtx)
				if err == nil {
					item.TotalRevenue = total
				}
				return err
			},
		)
	})
	if err != nil {
		return item, err
	}
	item.TodayRevenue = item.TodayChargeAmount
	// Align with the admin/user workbench: success rate uses successful plus
	// effective failed calls, so expected-empty 404 traffic cannot dilute the
	// rate. Total calls still include 404s for volume.
	item.TodaySuccessRate = percent(item.TodaySuccessCalls, item.TodaySuccessCalls+item.TodayFailedCalls)
	return item, nil
}

func (r *Repository) CallTrend(ctx context.Context, days int) (TrendResult, error) {
	labels := dayLabels(time.Now(), days)
	result := TrendResult{Labels: labels, TotalCalls: make([]int64, len(labels)), SuccessCalls: make([]int64, len(labels)), FailedCalls: make([]int64, len(labels)), BillableCalls: make([]int64, len(labels)), ChargeAmounts: make([]string, len(labels))}
	for i := range result.ChargeAmounts {
		result.ChargeAmounts[i] = "0.0000"
	}
	err := r.runQuery(ctx, "dashboard_call_trend", func(queryCtx context.Context) error {
		rows, err := r.db.QueryContext(queryCtx, dashboardCallTrendQuery(), days-1)
		if err != nil {
			// 日粒度投影缺失（000100 迁移尚未应用）时退回分钟层，而不是让整个
			// 趋势面板报错。Fallback 复用同一个 deadline，避免一次请求串联两次
			// 无界聚合。
			if !isMissingTableErr(err) {
				return err
			}
			rows, err = r.db.QueryContext(queryCtx, dashboardCallTrendMinuteQuery(), days-1)
			if err != nil {
				return err
			}
		}
		defer rows.Close()
		index := map[string]int{}
		for i, label := range labels {
			index[label] = i
		}
		for rows.Next() {
			var day, amount string
			var total, success, failed, billable int64
			if err := rows.Scan(&day, &total, &success, &failed, &billable, &amount); err != nil {
				return err
			}
			if i, ok := index[day]; ok {
				result.TotalCalls[i] = total
				result.SuccessCalls[i] = success
				result.FailedCalls[i] = failed
				result.BillableCalls[i] = billable
				result.ChargeAmounts[i] = amount
				result.Total += total
			}
		}
		return rows.Err()
	})
	return result, err
}

func (r *Repository) RegionRanking(ctx context.Context, limit int) ([]RankingItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	var items []RankingItem
	err := r.runQuery(ctx, "dashboard_region_ranking", func(queryCtx context.Context) error {
		rows, err := r.db.QueryContext(queryCtx, dashboardRegionRankingQuery(), dashboardAggregationDays, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item RankingItem
			if err := rows.Scan(&item.Name, &item.CallCount); err != nil {
				return err
			}
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func (r *Repository) HotAPIs(ctx context.Context, limit int) ([]RankingItem, error) {
	if limit <= 0 || limit > 100 {
		limit = 10
	}
	var items []RankingItem
	err := r.runQuery(ctx, "dashboard_hot_apis", func(queryCtx context.Context) error {
		rows, err := r.db.QueryContext(queryCtx, dashboardHotAPIsQuery(), dashboardAggregationDays, limit)
		if err != nil {
			// 日粒度投影缺失时退回分钟层，保持榜单可用。Fallback 复用同一
			// deadline，且 runQuery 会把整个 fallback 链路计入慢查询观测。
			if !isMissingTableErr(err) {
				return err
			}
			rows, err = r.db.QueryContext(queryCtx, dashboardHotAPIsMinuteQuery(), dashboardAggregationDays, limit)
			if err != nil {
				return err
			}
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			var item RankingItem
			if err := rows.Scan(&id, &item.APICode, &item.Name, &item.CallCount); err != nil {
				return err
			}
			item.ID = strconv.FormatInt(id, 10)
			items = append(items, item)
		}
		return rows.Err()
	})
	return items, err
}

func normalizePage(page, size int) (int, int) {
	return pagination.Normalize(page, size, maxCallLogPageSize)
}

func requireAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func dayLabels(now time.Time, days int) []string {
	now = now.In(businessclock.Location())
	labels := make([]string, 0, days)
	for i := days - 1; i >= 0; i-- {
		labels = append(labels, now.AddDate(0, 0, -i).Format("2006-01-02"))
	}
	return labels
}

func percent(value, total int64) string {
	if total <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.2f%%", float64(value)*100/float64(total))
}
