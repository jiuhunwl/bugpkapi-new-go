package gateway

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bugpk-api/internal/domain/keyaccess"
	"bugpk-api/internal/domain/revenue"
	"bugpk-api/internal/pkg/accountstatus"
	"bugpk-api/internal/pkg/calloutcome"
	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/jsoncleaning"
	"bugpk-api/internal/pkg/masking"
	"bugpk-api/internal/pkg/pagination"
	"bugpk-api/internal/pkg/proxypool"
	"bugpk-api/internal/pkg/requestvalidation"
	"github.com/cloudwego/hertz/pkg/common/hlog"
	"go.uber.org/zap"
)

var (
	ErrInsufficientBalance   = errors.New("insufficient balance")
	ErrBalanceRefundCapacity = errors.New("balance refund exceeds capacity")
	ErrPointRefundCapacity   = errors.New("point refund exceeds capacity")
)

const (
	maxGatewayBalanceText = "99999999999999.9999"
	maxGatewayPoints      = int64(1_000_000_000_000_000)
)

type Repository struct {
	db          *sql.DB
	revenueSink RevenueSink
	log         *zap.Logger
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

// SetRevenueSnapshot attaches a scheduler; the ledger marker is the durable work queue.
func (r *Repository) SetRevenueSnapshot(sink RevenueSink) {
	r.revenueSink = sink
}

// notifyConfirmedRevenue runs only after the billing transaction commits. A
// missing/closed scheduler uses the same idempotent projector, never an additive
// amount-only fallback. Failed projection does not change committed billing.
func (r *Repository) notifyConfirmedRevenue(ctx context.Context, reservation BillingReservation) {
	if reservation.BillingRecordID <= 0 {
		return
	}
	if r.revenueSink != nil && r.revenueSink.NotifyConfirmed() {
		return
	}
	fallbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), revenueFlushTimeout)
	defer cancel()
	if _, err := revenue.NewRepository(r.db).ProcessRecord(fallbackCtx, reservation.BillingRecordID); err != nil {
		if r.log != nil {
			r.log.Error("revenue snapshot fallback failed", zap.Int64("billing_record_id", reservation.BillingRecordID), zap.String("error", masking.Error(err)))
		} else {
			hlog.CtxErrorf(ctx, "revenue snapshot fallback failed billing_record_id=%d: %s", reservation.BillingRecordID, masking.Error(err))
		}
	}
}

func (r *Repository) ListKeys(ctx context.Context, userID int64, admin bool, params KeyListParams) (PageResult[APIKeyItem], error) {
	page, pageSize := normalizePage(params.Page, params.PageSize)
	whereSQL, args := keyListWhere(userID, admin, params)
	var (
		total int64
		items []APIKeyItem
	)
	countArgs := append(append([]any(nil), args...), pageSize*pagination.MaxPage)
	listArgs := append(append([]any(nil), args...), pageSize, pagination.Offset(page, pageSize))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, keyListCountQuery(params, whereSQL), countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, keyListQuery(params, whereSQL), listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items = make([]APIKeyItem, 0, pageSize)
			for rows.Next() {
				item, err := scanKeyItem(rows)
				if err != nil {
					return err
				}
				items = append(items, item)
			}
			return rows.Err()
		},
	)
	if err != nil {
		return PageResult[APIKeyItem]{}, err
	}
	if err := r.populateKeyRelationCounts(ctx, items); err != nil {
		return PageResult[APIKeyItem]{}, err
	}
	return PageResult[APIKeyItem]{Items: items, Total: total}, nil
}

func keyListWhere(userID int64, admin bool, params KeyListParams) (string, []any) {
	where := []string{"where 1=1"}
	args := []any{}
	if !admin {
		where = append(where, "k.user_id = ?")
		args = append(args, userID)
	}
	if strings.TrimSpace(params.Keyword) != "" {
		where = append(where, "(k.name like ? or k.key_suffix like ? or u.username like ?)")
		kw := "%" + strings.TrimSpace(params.Keyword) + "%"
		args = append(args, kw, kw, kw)
	}
	if strings.TrimSpace(params.KeyType) != "" {
		where = append(where, "k.key_type = ?")
		args = append(args, strings.TrimSpace(params.KeyType))
	}
	if params.Status != nil {
		where = append(where, "k.status = ?")
		args = append(args, *params.Status)
	}
	return strings.Join(where, " and "), args
}

func keyListFilterLookupJoin(params KeyListParams) string {
	if strings.TrimSpace(params.Keyword) == "" {
		return ""
	}
	return " inner join bp_user u on u.id = k.user_id"
}

func keyListCountQuery(params KeyListParams, where string) string {
	return `select count(*)
		from (
			select k.id
			from bp_api_key k` + keyListFilterLookupJoin(params) + `
			` + where + `
			limit ?
		) counted`
}

func keyListQuery(params KeyListParams, where string) string {
	return `
		select k.id, k.user_id, u.username, k.name, k.key_prefix, k.key_suffix,
		       coalesce(k.secret_ciphertext, ''), k.key_type, k.status, k.suspended_by_ban,
		       coalesce(date_format(k.expires_at, '%Y-%m-%d %H:%i:%s'), ''),
		       coalesce(date_format(k.last_used_at, '%Y-%m-%d %H:%i:%s'), ''),
		       date_format(k.created_at, '%Y-%m-%d %H:%i:%s'),
		       date_format(k.updated_at, '%Y-%m-%d %H:%i:%s'),
		       0, 0
		from (
			select k.id
			from bp_api_key k` + keyListFilterLookupJoin(params) + `
			` + where + `
			order by k.id desc
			limit ? offset ?
		) filtered
		inner join bp_api_key k on k.id=filtered.id
		inner join bp_user u on u.id = k.user_id
		order by k.id desc
	`
}

func (r *Repository) KeyByID(ctx context.Context, id, userID int64, admin bool) (APIKeyItem, error) {
	where := "where k.id = ?"
	args := []any{id}
	if !admin {
		where += " and k.user_id = ?"
		args = append(args, userID)
	}
	rows, err := r.db.QueryContext(ctx, `
		select k.id, k.user_id, u.username, k.name, k.key_prefix, k.key_suffix,
		       coalesce(k.secret_ciphertext, ''), k.key_type, k.status, k.suspended_by_ban,
		       coalesce(date_format(k.expires_at, '%Y-%m-%d %H:%i:%s'), ''),
		       coalesce(date_format(k.last_used_at, '%Y-%m-%d %H:%i:%s'), ''),
		       date_format(k.created_at, '%Y-%m-%d %H:%i:%s'),
		       date_format(k.updated_at, '%Y-%m-%d %H:%i:%s'),
		       0, 0
		from bp_api_key k
		inner join bp_user u on u.id = k.user_id
		`+where+`
	`, args...)
	if err != nil {
		return APIKeyItem{}, err
	}
	if !rows.Next() {
		_ = rows.Close()
		return APIKeyItem{}, sql.ErrNoRows
	}
	item, err := scanKeyItem(rows)
	if err != nil {
		_ = rows.Close()
		return APIKeyItem{}, err
	}
	if err := rows.Close(); err != nil {
		return APIKeyItem{}, err
	}
	items := []APIKeyItem{item}
	if err := r.populateKeyRelationCounts(ctx, items); err != nil {
		return APIKeyItem{}, err
	}
	return items[0], nil
}

func (r *Repository) populateKeyRelationCounts(ctx context.Context, items []APIKeyItem) error {
	if len(items) == 0 {
		return nil
	}
	positions := make(map[int64]int, len(items))
	args := make([]any, 0, len(items)*2)
	for index := range items {
		id := parseID(items[index].ID)
		if id <= 0 {
			return fmt.Errorf("invalid api key id %q while loading relation counts", items[index].ID)
		}
		positions[id] = index
		args = append(args, id)
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(items)), ",")
	for index := range items {
		args = append(args, parseID(items[index].ID))
	}
	rows, err := r.db.QueryContext(ctx, `
		select key_id,'INTERFACE' relation_type,count(*) relation_count
		from bp_api_key_interface
		where key_id in (`+placeholders+`)
		group by key_id
		union all
		select key_id,'WHITELIST' relation_type,count(*) relation_count
		from bp_api_key_ip_whitelist
		where key_id in (`+placeholders+`)
		group by key_id
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var keyID, count int64
		var relationType string
		if err := rows.Scan(&keyID, &relationType, &count); err != nil {
			return err
		}
		index, ok := positions[keyID]
		if !ok {
			continue
		}
		switch relationType {
		case "INTERFACE":
			items[index].InterfaceCount = count
		case "WHITELIST":
			items[index].WhitelistCount = count
		}
	}
	return rows.Err()
}

func scanKeyItem(rows interface{ Scan(dest ...any) error }) (APIKeyItem, error) {
	var item APIKeyItem
	var id, userID int64
	if err := rows.Scan(&id, &userID, &item.Username, &item.Name, &item.KeyPrefix, &item.KeySuffix, &item.secretCiphertext, &item.KeyType, &item.Status, &item.SuspendedByBan, &item.ExpiresAt, &item.LastUsedAt, &item.CreateTime, &item.UpdateTime, &item.InterfaceCount, &item.WhitelistCount); err != nil {
		return APIKeyItem{}, err
	}
	item.ID = strconv.FormatInt(id, 10)
	item.UserID = strconv.FormatInt(userID, 10)
	item.MaskedKey = item.KeyPrefix + "****" + item.KeySuffix
	item.SecretAvailable = strings.TrimSpace(item.secretCiphertext) != ""
	return item, nil
}

func (r *Repository) CreateKey(ctx context.Context, item APIKeyItem, keyHash, secretCiphertext string, expiresAt *time.Time, interfaceIDs []int64, ipRules []string) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		insert into bp_api_key(user_id, name, key_hash, secret_ciphertext, key_prefix, key_suffix, key_type, status, expires_at)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, parseID(item.UserID), item.Name, keyHash, secretCiphertext, item.KeyPrefix, item.KeySuffix, item.KeyType, item.Status, expiresAt)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := replaceInterfaces(ctx, tx, id, interfaceIDs); err != nil {
		return 0, err
	}
	if err := replaceIPRules(ctx, tx, id, ipRules); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return id, nil
}

func (r *Repository) UpdateKey(ctx context.Context, id int64, item APIKeyItem, expiresAt *time.Time, interfaceIDs []int64, ipRules []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	// 启用 Key 时清掉封禁联动标记：账号已解封后管理员手动启用，
	// 该行不应再被展示为「因封禁暂停/作废」。
	result, err := tx.ExecContext(ctx, `
		update bp_api_key
		set name = ?, key_type = ?, status = ?,
		    suspended_by_ban = if(? = 1, 0, suspended_by_ban),
		    expires_at = ?
		where id = ?
	`, item.Name, item.KeyType, item.Status, item.Status, expiresAt, id)
	if err != nil {
		return err
	}
	if err := requireAffected(result); err != nil {
		return err
	}
	if err := replaceInterfaces(ctx, tx, id, interfaceIDs); err != nil {
		return err
	}
	if err := replaceIPRules(ctx, tx, id, ipRules); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) UpdateStatus(ctx context.Context, id int64, status int) error {
	// 启用时同步清掉封禁联动标记（见 UpdateKey 的说明）。
	result, err := r.db.ExecContext(ctx, "update bp_api_key set status = ?, suspended_by_ban = if(? = 1, 0, suspended_by_ban) where id = ?", status, status, id)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

func (r *Repository) DeleteKey(ctx context.Context, id int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "delete from bp_api_key_interface where key_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_api_key_ip_whitelist where key_id = ?", id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "delete from bp_api_key where id = ?", id)
	if err != nil {
		return err
	}
	if err := requireAffected(result); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ReplaceInterfaces(ctx context.Context, id int64, interfaceIDs []int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := replaceInterfaces(ctx, tx, id, interfaceIDs); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ReplaceIPRules(ctx context.Context, id int64, rules []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := replaceIPRules(ctx, tx, id, rules); err != nil {
		return err
	}
	return tx.Commit()
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type queryExecer interface {
	execer
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func replaceInterfaces(ctx context.Context, tx execer, id int64, interfaceIDs []int64) error {
	if _, err := tx.ExecContext(ctx, "delete from bp_api_key_interface where key_id = ?", id); err != nil {
		return err
	}
	for _, interfaceID := range interfaceIDs {
		if _, err := tx.ExecContext(ctx, "insert ignore into bp_api_key_interface(key_id, interface_id) values (?, ?)", id, interfaceID); err != nil {
			return err
		}
	}
	return nil
}

func replaceIPRules(ctx context.Context, tx execer, id int64, rules []string) error {
	if _, err := tx.ExecContext(ctx, "delete from bp_api_key_ip_whitelist where key_id = ?", id); err != nil {
		return err
	}
	for _, rule := range rules {
		if _, err := tx.ExecContext(ctx, "insert ignore into bp_api_key_ip_whitelist(key_id, ip_rule) values (?, ?)", id, rule); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) Interfaces(ctx context.Context, id int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, "select interface_id from bp_api_key_interface where key_id = ? order by interface_id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var value int64
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		ids = append(ids, value)
	}
	return ids, rows.Err()
}

func (r *Repository) IPRules(ctx context.Context, id int64) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, "select ip_rule from bp_api_key_ip_whitelist where key_id = ? order by id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rules []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		rules = append(rules, value)
	}
	return rules, rows.Err()
}

func (r *Repository) InterfaceIDsExist(ctx context.Context, ids []int64) (bool, error) {
	if len(ids) == 0 {
		return true, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	var count int64
	if err := r.db.QueryRowContext(ctx, "select count(*) from bp_interface_api where id in ("+placeholders+")", args...).Scan(&count); err != nil {
		return false, err
	}
	return count == int64(len(ids)), nil
}

func (r *Repository) KeyAuthByHash(ctx context.Context, hash string) (KeyAuth, error) {
	var item KeyAuth
	var expires sql.NullTime
	var quarantined int
	err := r.db.QueryRowContext(ctx, `
		select k.id, k.user_id, u.username, `+accountstatus.EffectiveStatusSQL("u")+`,
		       u.ban_permanent, coalesce(date_format(u.ban_until, '%Y-%m-%d %H:%i:%s'), ''), coalesce(u.ban_reason, ''),
		       u.token_version, k.key_prefix, k.key_type, k.status, k.expires_at,
		       exists(select 1 from bp_api_key_ip_whitelist w where w.key_id = k.id),
		       coalesce(sr.enabled, 0),
		       coalesce(primary_role.id, default_role.id, 0),
		       coalesce(primary_role.role_key, default_role.role_key, ''),
		       u.created_at,
		       coalesce(primary_role.request_quota_enabled, default_role.request_quota_enabled, 0),
		       coalesce(primary_role.request_quota_minute, default_role.request_quota_minute, 0),
		       coalesce(primary_role.request_quota_hour, default_role.request_quota_hour, 0),
		       coalesce(primary_role.request_quota_day, default_role.request_quota_day, 0),
		       coalesce(primary_role.request_quota_month, default_role.request_quota_month, 0),
		       coalesce(primary_role.request_quota_count_mode, default_role.request_quota_count_mode, 'attempt'),
		       coalesce(cfg.new_account_day_limit, 0),
		       exists(select 1 from bp_user_package_global ug
		              where ug.user_id = u.id and ug.status = 1
		                and ug.start_at <= now() and ug.end_at > now()
		                and (ug.daily_limit = 0 or if(ug.day_key = curdate(), ug.day_used, 0) < ug.daily_limit)
		                and (ug.total_limit = 0 or ug.total_used < ug.total_limit)),
		       exists(select 1 from bp_user_package_interface ui
		              where ui.user_id = u.id and ui.status = 1
		                and ui.start_at <= now() and ui.end_at > now()
		                and (ui.daily_limit = 0 or if(ui.day_key = curdate(), ui.day_used, 0) < ui.daily_limit)
		                and (ui.total_limit = 0 or ui.total_used < ui.total_limit)),
		       exists(select 1 from bp_user_risk_state rs where rs.user_id = u.id and rs.quarantine_until > now())
		from bp_api_key k
		inner join bp_user u on u.id = k.user_id
		left join bp_user_specified_response sr on sr.user_id = u.id
		left join bp_role primary_role on primary_role.id = u.primary_role_id
		left join bp_auth_register_config cfg on cfg.id = 1
		left join bp_role default_role on default_role.id = cfg.default_role_id
		where k.key_hash = ?
		limit 1
	`, hash).Scan(
		&item.ID,
		&item.UserID,
		&item.Username,
		&item.UserStatus,
		&item.BanPermanent,
		&item.BanUntil,
		&item.BanReason,
		&item.TokenVersion,
		&item.KeyPrefix,
		&item.KeyType,
		&item.Status,
		&expires,
		&item.HasIPWhitelist,
		&item.SpecifiedResponseEnabled,
		&item.RoleID,
		&item.RoleKey,
		&item.UserCreatedAt,
		&item.QuotaEnabled,
		&item.QuotaMinuteLimit,
		&item.QuotaHourLimit,
		&item.QuotaDayLimit,
		&item.QuotaMonthLimit,
		&item.QuotaCountMode,
		&item.NewAccountDayLimit,
		&item.HasUsableGlobalPackage,
		&item.HasUsableInterfacePackage,
		&quarantined,
	)
	item.Quarantined = quarantined == 1
	if expires.Valid {
		item.ExpiresAt = &expires.Time
	}
	return item, err
}

func (r *Repository) KeyAllowsInterface(ctx context.Context, keyID, interfaceID int64) (bool, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, "select count(*) from bp_api_key_interface where key_id = ? and interface_id = ?", keyID, interfaceID).Scan(&count)
	return count > 0, err
}

func (r *Repository) InterfaceByCode(ctx context.Context, code string) (InterfaceConfig, error) {
	var item InterfaceConfig
	var upstreamText string
	var localCacheRulesText, globalCacheRulesText string
	var requestValidationRulesText string
	var localResponseAdID, globalResponseAdID sql.NullInt64
	var cachePolicyOverride, responseAdPolicyOverride int
	var localPolicyVersion, globalPolicyVersion int64
	var localPolicy, globalPolicy deliveryPolicySnapshot
	var id int64
	var price string
	var billingEnabled, billingLogEnabled int
	row := r.db.QueryRowContext(ctx, `
		select i.id, i.api_code, i.name, i.request_url, i.request_method, cast(i.price as char), i.point_price, i.shared_qps_limit, i.user_qps_limit, i.status,
		       i.request_validation_enabled, coalesce(i.request_validation_default_result, 'PASS'),
		       coalesce(cast(i.request_validation_rules as char), '[]'),
                       i.polling_enabled, i.polling_mode, i.response_cleaning_enabled, coalesce(cast(i.upstream_urls as char), '[]'),
		       i.polling_check_enabled, coalesce(i.polling_check_field, ''), coalesce(i.polling_check_expected, ''), coalesce(i.polling_stop_code, ''),
		       i.cache_policy_override, i.cache_enabled, i.cache_default_ttl_seconds, coalesce(cast(i.cache_rules as char),'[]'),
		       coalesce(i.request_timeout_ms, 0), coalesce(i.request_timeout_override, 0),
		       i.response_ad_enabled, i.response_ad_id, coalesce(i.response_ad_field,'_advertisement'),
		       i.response_ad_policy_override, i.delivery_policy_version,
		       coalesce(g.cache_enabled, 0), coalesce(g.cache_default_ttl_seconds, 0), coalesce(cast(g.cache_rules as char),'[]'),
		       coalesce(g.request_timeout_ms, 15000),
		       coalesce(g.response_ad_enabled, 0), g.response_ad_id, coalesce(g.response_ad_field,'_advertisement'),
		       coalesce(g.policy_version, 1), i.billing_enabled, i.billing_log_enabled
		from bp_interface_api i
		left join bp_interface_global_delivery_policy g on g.id = 1
		where i.api_code = ?
		limit 1
	`, code)
	err := row.Scan(&id, &item.APICode, &item.Name, &item.RequestURL, &item.RequestMethod, &price, &item.PointPrice, &item.SharedQPSLimit, &item.UserQPSLimit, &item.Status,
		&item.RequestValidation.Enabled, &item.RequestValidation.DefaultResult, &requestValidationRulesText,
		&item.PollingEnabled, &item.PollingMode, &item.ResponseCleaningEnabled, &upstreamText, &item.PollingCheckEnabled, &item.PollingCheckField, &item.PollingCheckExpected, &item.PollingStopCode,
		&cachePolicyOverride, &localPolicy.CacheEnabled, &localPolicy.CacheDefaultTTLSeconds, &localCacheRulesText,
		&localPolicy.RequestTimeoutMs, &localPolicy.RequestTimeoutOverride,
		&localPolicy.ResponseAdEnabled, &localResponseAdID, &localPolicy.ResponseAdField,
		&responseAdPolicyOverride, &localPolicyVersion,
		&globalPolicy.CacheEnabled, &globalPolicy.CacheDefaultTTLSeconds, &globalCacheRulesText,
		&globalPolicy.RequestTimeoutMs,
		&globalPolicy.ResponseAdEnabled, &globalResponseAdID, &globalPolicy.ResponseAdField, &globalPolicyVersion, &billingEnabled, &billingLogEnabled)
	if err != nil {
		return InterfaceConfig{}, err
	}
	item.ID = id
	item.Price = price
	item.BillingDisabled = billingEnabled == 0
	item.BillingLogDisabled = billingLogEnabled == 0
	item.ResponseFieldMappings, item.UpstreamConfigs, item.ProxyPoolConfig, item.DomainRouting = decodeUpstreamConfigDocumentFullWithRouting(upstreamText)
	if err := json.Unmarshal([]byte(requestValidationRulesText), &item.RequestValidation.Rules); err != nil {
		return InterfaceConfig{}, fmt.Errorf("decode request validation rules: %w", err)
	}
	item.RequestValidation, err = requestvalidation.Normalize(item.RequestValidation)
	if err != nil {
		return InterfaceConfig{}, fmt.Errorf("normalize stored request validation policy: %w", err)
	}
	localPolicy.CacheRules = decodeResponseCacheRules(localCacheRulesText)
	globalPolicy.CacheRules = decodeResponseCacheRules(globalCacheRulesText)
	if localResponseAdID.Valid {
		localPolicy.ResponseAdID = localResponseAdID.Int64
	}
	if globalResponseAdID.Valid {
		globalPolicy.ResponseAdID = globalResponseAdID.Int64
	}
	localPolicy.PolicyVersion = localPolicyVersion
	globalPolicy.PolicyVersion = globalPolicyVersion
	effective := resolveDeliveryPolicy(localPolicy, globalPolicy, cachePolicyOverride, responseAdPolicyOverride)
	item.CachePolicyOverride = cachePolicyOverride
	item.ResponseAdPolicyOverride = responseAdPolicyOverride
	item.CacheEnabled = effective.CacheEnabled
	item.CacheDefaultTTLSeconds = effective.CacheDefaultTTLSeconds
	item.CacheRules = effective.CacheRules
	item.CachePolicyVersion = effective.PolicyVersion
	item.RequestTimeoutMs = effective.RequestTimeoutMs
	item.ResponseAdEnabled = effective.ResponseAdEnabled
	item.ResponseAdID = effective.ResponseAdID
	item.ResponseAdField = effective.ResponseAdField
	quota, err := r.interfaceQuotaPolicy(ctx, id)
	if err != nil {
		return InterfaceConfig{}, err
	}
	item.QuotaEnabled = quota.Enabled
	item.QuotaCoverPackage = quota.CoverPackage
	item.QuotaCountMode = quota.CountMode
	item.QuotaPerUser = quota.PerUser
	item.QuotaAllUsers = quota.AllUsers
	item.RoleQuotas = quota.RoleQuotas
	metering, err := r.meteringPolicy(ctx, id)
	if err != nil {
		return InterfaceConfig{}, err
	}
	item.AssetMode = metering.AssetMode
	item.PackageMode = metering.PackageMode
	item.GroupMode = metering.GroupMode
	item.RoleMetering = metering.RoleMetering
	rules, err := r.BillingRules(ctx, id)
	if err != nil {
		return InterfaceConfig{}, err
	}
	item.BillingRules = rules
	degrade, err := r.degradePolicy(ctx, id)
	if err != nil {
		return InterfaceConfig{}, err
	}
	item.Degrade = degrade
	return item, nil
}

// interfaceQuotaPolicy loads the per-interface caps and role overrides. It runs
// only on an interface-config cache miss, so two small indexed reads are
// cheaper than denormalising role overrides into the hot path.
func (r *Repository) interfaceQuotaPolicy(ctx context.Context, interfaceID int64) (InterfaceQuotaSnapshot, error) {
	snapshot := InterfaceQuotaSnapshot{
		CountMode:  "attempt",
		RoleQuotas: make(map[int64]InterfaceRoleQuota),
	}
	err := r.db.QueryRowContext(ctx, `
		select enabled, count_mode, cover_package,
		       per_user_hour, per_user_day, per_user_week, per_user_month,
		       all_users_hour, all_users_day, all_users_week, all_users_month
		from bp_interface_quota
		where interface_id = ?
	`, interfaceID).Scan(
		&snapshot.Enabled, &snapshot.CountMode, &snapshot.CoverPackage,
		&snapshot.PerUser.Hour, &snapshot.PerUser.Day, &snapshot.PerUser.Week, &snapshot.PerUser.Month,
		&snapshot.AllUsers.Hour, &snapshot.AllUsers.Day, &snapshot.AllUsers.Week, &snapshot.AllUsers.Month,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, nil
	}
	if err != nil {
		return InterfaceQuotaSnapshot{}, err
	}
	quotaRows, err := r.db.QueryContext(ctx, `
		select role_id, mode,
		       adjust_hour, adjust_day, adjust_week, adjust_month,
		       override_hour, override_day, override_week, override_month
		from bp_interface_role_quota
		where interface_id = ?
	`, interfaceID)
	if err != nil {
		return InterfaceQuotaSnapshot{}, err
	}
	defer quotaRows.Close()
	for quotaRows.Next() {
		var roleID int64
		var quota InterfaceRoleQuota
		if err := quotaRows.Scan(
			&roleID, &quota.Mode,
			&quota.Adjust.Hour, &quota.Adjust.Day, &quota.Adjust.Week, &quota.Adjust.Month,
			&quota.Override.Hour, &quota.Override.Day, &quota.Override.Week, &quota.Override.Month,
		); err != nil {
			return InterfaceQuotaSnapshot{}, err
		}
		snapshot.RoleQuotas[roleID] = quota
	}
	return snapshot, quotaRows.Err()
}

// meteringPolicy loads the three-way metering policy and its per-role overrides.
// 缺行等价于三维全 inherit，因此未配置过策略的接口不需要补种数据。
// 与 interfaceQuotaPolicy 一样，只在配置缓存未命中时各读一次。
func (r *Repository) meteringPolicy(ctx context.Context, interfaceID int64) (InterfaceMeteringSnapshot, error) {
	snapshot := InterfaceMeteringSnapshot{
		AssetMode:    meteringInherit,
		PackageMode:  meteringInherit,
		GroupMode:    meteringInherit,
		RoleMetering: make(map[int64]InterfaceRoleMetering),
	}
	err := r.db.QueryRowContext(ctx, `
		select asset_mode, package_mode, group_mode
		from bp_interface_metering_policy
		where interface_id = ?
	`, interfaceID).Scan(&snapshot.AssetMode, &snapshot.PackageMode, &snapshot.GroupMode)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, nil
	}
	if err != nil {
		return InterfaceMeteringSnapshot{}, err
	}
	overrideRows, err := r.db.QueryContext(ctx, `
		select role_id, asset_mode, package_mode, group_mode
		from bp_interface_role_metering
		where interface_id = ?
	`, interfaceID)
	if err != nil {
		return InterfaceMeteringSnapshot{}, err
	}
	defer overrideRows.Close()
	for overrideRows.Next() {
		var roleID int64
		var override InterfaceRoleMetering
		if err := overrideRows.Scan(&roleID, &override.AssetMode, &override.PackageMode, &override.GroupMode); err != nil {
			return InterfaceMeteringSnapshot{}, err
		}
		snapshot.RoleMetering[roleID] = override
	}
	return snapshot, overrideRows.Err()
}

type upstreamConfigDocument struct {
	DomainRouting                *DomainRoutingConfig        `json:"domainRouting,omitempty"`
	PrimaryResponseFieldMappings []jsoncleaning.FieldMapping `json:"primaryResponseFieldMappings,omitempty"`
	ProxyPoolConfig              proxypool.Config            `json:"proxyPoolConfig,omitempty"`
	Upstreams                    []UpstreamConfig            `json:"upstreams,omitempty"`
}

func decodeUpstreamConfigDocument(text string) ([]jsoncleaning.FieldMapping, []UpstreamConfig) {
	primary, configs, _ := decodeUpstreamConfigDocumentFull(text)
	return primary, configs
}

func decodeUpstreamConfigDocumentFull(text string) ([]jsoncleaning.FieldMapping, []UpstreamConfig, proxypool.Config) {
	primary, configs, proxyConfig, _ := decodeUpstreamConfigDocumentFullWithRouting(text)
	return primary, configs, proxyConfig
}

func decodeUpstreamConfigDocumentFullWithRouting(text string) ([]jsoncleaning.FieldMapping, []UpstreamConfig, proxypool.Config, DomainRoutingConfig) {
	var configs []UpstreamConfig
	if err := json.Unmarshal([]byte(text), &configs); err == nil {
		return nil, configs, proxypool.Config{}, DomainRoutingConfig{}
	}
	var document upstreamConfigDocument
	if err := json.Unmarshal([]byte(text), &document); err == nil && (document.Upstreams != nil || document.PrimaryResponseFieldMappings != nil || document.ProxyPoolConfig.Enabled != 0 || document.DomainRouting != nil) {
		routing := DomainRoutingConfig{}
		if document.DomainRouting != nil {
			routing = *document.DomainRouting
		}
		return document.PrimaryResponseFieldMappings, document.Upstreams, document.ProxyPoolConfig, routing
	}
	configs = nil
	var urls []string
	if err := json.Unmarshal([]byte(text), &urls); err == nil {
		for _, raw := range urls {
			if strings.TrimSpace(raw) != "" {
				configs = append(configs, UpstreamConfig{URL: strings.TrimSpace(raw)})
			}
		}
	}
	return nil, configs, proxypool.Config{}, DomainRoutingConfig{}
}

func decodeUpstreamConfigs(text string) []UpstreamConfig {
	_, configs := decodeUpstreamConfigDocument(text)
	return configs
}

// degradePolicy loads the per-interface degrade policy and its per-role rules.
// It is a separate helper so InterfaceByCode keeps its primary QueryRow read
// closed before billing rules are loaded (see repository_test.go).
func (r *Repository) degradePolicy(ctx context.Context, interfaceID int64) (InterfaceDegradePolicy, error) {
	snapshot := InterfaceDegradePolicy{
		PolicyVersion: 1,
		RoleRules:     make(map[int64]InterfaceDegradeRule),
	}
	err := r.db.QueryRowContext(ctx, `
		select p.enabled, coalesce(p.default_target_interface_id, 0),
		       coalesce(t.api_code, ''), coalesce(p.default_target_url, ''), p.policy_version
		from bp_interface_degrade_policy p
		left join bp_interface_api t on t.id = p.default_target_interface_id
		where p.interface_id = ?
	`, interfaceID).Scan(
		&snapshot.Enabled, &snapshot.DefaultTargetInterfaceID,
		&snapshot.DefaultTargetAPICode, &snapshot.DefaultTargetURL, &snapshot.PolicyVersion,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, nil
	}
	if err != nil {
		return InterfaceDegradePolicy{}, err
	}
	rows, err := r.db.QueryContext(ctx, `
		select d.role_id, d.count_window, d.threshold, coalesce(d.target_interface_id, 0),
		       coalesce(t.api_code, ''), coalesce(d.target_url, '')
		from bp_interface_role_degrade d
		left join bp_interface_api t on t.id = d.target_interface_id
		where d.interface_id = ?
	`, interfaceID)
	if err != nil {
		return InterfaceDegradePolicy{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var roleID int64
		var rule InterfaceDegradeRule
		if err := rows.Scan(&roleID, &rule.Window, &rule.Threshold, &rule.TargetInterfaceID, &rule.TargetAPICode, &rule.TargetURL); err != nil {
			return InterfaceDegradePolicy{}, err
		}
		snapshot.RoleRules[roleID] = rule
	}
	return snapshot, rows.Err()
}

func decodeResponseCacheRules(text string) []ResponseCacheRule {
	var rules []ResponseCacheRule
	if err := json.Unmarshal([]byte(text), &rules); err != nil {
		return nil
	}
	return rules
}

func (r *Repository) BillingRules(ctx context.Context, interfaceID int64) ([]BillingRule, error) {
	rows, err := r.db.QueryContext(ctx, `
		select field_name, operator, expected_value, sort_no
		from bp_interface_billing_rule
		where interface_id = ?
		order by sort_no, id
	`, interfaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rules []BillingRule
	for rows.Next() {
		var rule BillingRule
		if err := rows.Scan(&rule.FieldName, &rule.Operator, &rule.ExpectedValue, &rule.SortNo); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (r *Repository) SpecifiedResponse(ctx context.Context, userID int64) (SpecifiedResponse, error) {
	var item SpecifiedResponse
	err := r.db.QueryRowContext(ctx, `
		select enabled, billable, coalesce(body, '')
		from bp_user_specified_response
		where user_id = ?
		limit 1
	`, userID).Scan(&item.Enabled, &item.Billable, &item.Body)
	if err == sql.ErrNoRows {
		return SpecifiedResponse{}, nil
	}
	return item, err
}

func (r *Repository) EffectiveUserQPS(ctx context.Context, userID, interfaceID int64) (int, error) {
	roleQPS, packageQPS, found, err := loadEffectiveQPS(
		ctx,
		func(loadCtx context.Context) (int, error) {
			return primaryRoleQPS(loadCtx, r.db, userID)
		},
		func(loadCtx context.Context) (int, bool, error) {
			return activePackageQPS(loadCtx, r.db, userID, interfaceID)
		},
	)
	if err != nil {
		return 0, err
	}
	return effectiveQPSLimit(roleQPS, packageQPS, found), nil
}

// loadEffectiveQPS keeps the independent role and package lookups off the
// serial request path. The billing reservation transaction still performs its
// own locked reads; this helper is used only for non-reserved requests.
func loadEffectiveQPS(
	ctx context.Context,
	roleLoader func(context.Context) (int, error),
	packageLoader func(context.Context) (int, bool, error),
) (int, int, bool, error) {
	var (
		roleQPS    int
		packageQPS int
		found      bool
		roleErr    error
		packageErr error
	)
	if err := dbx.RunParallel(
		func() error {
			roleQPS, roleErr = roleLoader(ctx)
			return roleErr
		},
		func() error {
			packageQPS, found, packageErr = packageLoader(ctx)
			return packageErr
		},
	); err != nil {
		return 0, 0, false, err
	}
	return roleQPS, packageQPS, found, nil
}

func (r *Repository) UpdateCurrentNode(ctx context.Context, id int64, node int) error {
	_, err := r.db.ExecContext(ctx, "update bp_interface_api set current_node = ? where id = ?", node, id)
	return err
}

func (r *Repository) UpdateLastUsed(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, "update bp_api_key set last_used_at = now() where id = ?", id)
	return err
}

func (r *Repository) CreateOpenLog(ctx context.Context, result OpenResult) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	logID, err := createOpenLogTx(ctx, tx, result)
	if err != nil {
		return 0, err
	}
	if err := dbx.CommitWithVerification(ctx, tx, func(checkCtx context.Context) (bool, error) {
		return r.verifyOpenLogCommit(checkCtx, logID, result.BillingRecordID)
	}); err != nil {
		return 0, err
	}
	return logID, nil
}

func (r *Repository) CreateOpenLogAndConfirm(ctx context.Context, result OpenResult, reservation BillingReservation) (int64, error) {
	if reservation.BillingRecordID == 0 {
		return r.CreateOpenLog(ctx, result)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	logID, err := createOpenLogTx(ctx, tx, result)
	if err != nil {
		return 0, err
	}
	if reservation.BillingRecordID > 0 {
		result, err := tx.ExecContext(ctx, `update bp_billing_record set status='CONFIRMED', call_log_id=? where id=? and status='RESERVED'`, logID, reservation.BillingRecordID)
		if err != nil {
			return 0, err
		}
		if err := requireAffected(result); err != nil {
			return 0, err
		}
	}
	if err := dbx.CommitWithVerification(ctx, tx, func(checkCtx context.Context) (bool, error) {
		return r.verifyBillingCommit(checkCtx, reservation, "CONFIRMED", logID)
	}); err != nil {
		return 0, err
	}
	// The revenue snapshot is maintained after the billing transaction
	// commits so concurrent confirmations no longer serialize on the single
	// snapshot row inside the hot transaction.
	r.notifyConfirmedRevenue(ctx, reservation)
	return logID, nil
}

func createOpenLogTx(ctx context.Context, exec execer, result OpenResult) (int64, error) {
	body := responseBodyForLog(result.Body)
	requestParams := requestParamsForPersistence(result.RequestParams)
	requestValue := requestValueForPersistence(result.RequestValue)
	outcomeClass := calloutcome.Classify(result.Success, result.ResponseStatus)
	// The only supported override is the polling stop path: a configured
	// business "not found" code returned with HTTP 2xx is an expected empty
	// result, not an upstream failure. Other overrides are ignored so a row can
	// never disagree with its success flag.
	if !result.Success && result.OutcomeClass == calloutcome.ExpectedEmpty {
		outcomeClass = calloutcome.ExpectedEmpty
	}
	insert, err := exec.ExecContext(ctx, `
		insert into bp_interface_call_log(interface_id, user_id, api_key_id, api_key_prefix, api_code, request_method, request_params, request_value, request_fingerprint,
			response_status, success, outcome_class, billable, charge_type, billing_status, billing_snapshot, charge_amount, point_price,
			billing_record_id, elapsed_ms, polling_mode, upstream_url, upstream_switched, error_message, client_ip, client_region,
			ip_country, ip_province, ip_city, ip_isp, latitude, longitude, region_provider, trace_id, request_size, response_size, response_saved_size, response_truncated, user_visible)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, result.InterfaceID, result.UserID, nullInt64(result.APIKeyID), nullIfBlank(result.APIKeyPrefix), nullIfBlank(result.APICode), result.RequestMethod, nullIfBlank(requestParams), nullIfBlank(requestValue), strings.TrimSpace(result.RequestFingerprint),
		result.ResponseStatus, boolInt(result.Success), int(outcomeClass), boolInt(result.Billable), result.ChargeType, result.BillingStatus, jsonTextOrNull(result.BillingSnapshot), result.ChargeAmount, result.PointPrice,
		nullInt64(result.BillingRecordID), result.ElapsedMs, nullIfBlank(result.PollingMode), nullIfBlank(upstreamURLsForPersistence(result)), boolInt(result.UpstreamSwitched), nullIfBlank(safeDiagnosticText(result.ErrorMessage, 500)), nullIfBlank(result.ClientIP), nullIfBlank(result.Region.Region),
		nullIfBlank(result.Region.Country), nullIfBlank(result.Region.Province), nullIfBlank(result.Region.City), nullIfBlank(result.Region.ISP), nullFloat64(result.Region.Latitude), nullFloat64(result.Region.Longitude), nullIfBlank(result.Region.Provider),
		nullIfBlank(result.TraceID), result.RequestSize, responseSizeForLog(result), int64(len(body)), boolInt(responseWasTruncatedForResult(result.Body, result.ResponseSize, result.ResponseNormalized)), boolInt(result.UserVisible))
	if err != nil {
		return 0, err
	}
	logID, err := insert.LastInsertId()
	if err != nil {
		return 0, err
	}
	// The response body lives in the side table keyed by log_id. It is written
	// in the same transaction as the main insert so the two can never diverge.
	// Empty bodies are omitted entirely (the detail read coalesces NULL to "").
	if body != "" {
		if _, err := exec.ExecContext(ctx, `insert into bp_interface_call_log_body(log_id, response_body) values (?, ?)`, logID, body); err != nil {
			return 0, err
		}
	}
	return logID, nil
}

func upstreamURLsForPersistence(result OpenResult) string {
	if len(result.TriedUpstreams) == 0 {
		return truncateUTF8Bytes(masking.UpstreamReference(result.UpstreamURL), 1024)
	}
	// Keep only synthetic node labels.  The configured maximum of 50 nodes fits
	// well below the TEXT limit while preserving attempt count/order without
	// retaining a provider hostname, route, query, port, or IP address.
	return truncateUTF8Bytes(masking.UpstreamReferences(result.TriedUpstreams), 65535)
}

func responseSizeForLog(result OpenResult) int64 {
	if result.ResponseSize > 0 {
		return result.ResponseSize
	}
	return int64(len(result.Body))
}

func responseWasTruncated(body string, originalSize int64) bool {
	if body == "" {
		return false
	}
	if !utf8.ValidString(body) || len(body) > 20000 {
		return true
	}
	return originalSize > int64(len(body))
}

func responseWasTruncatedForResult(body string, originalSize int64, normalized bool) bool {
	if body == "" || !normalized {
		return responseWasTruncated(body, originalSize)
	}
	// A normalized response is intentionally shorter (or otherwise changed),
	// so that size difference is not evidence of truncation. Persistence limits
	// and invalid UTF-8 still count as truncation/omission.
	if !utf8.ValidString(body) || len(body) > 20000 {
		return true
	}
	return false
}

func (r *Repository) ReserveBilling(ctx context.Context, api InterfaceConfig, keyID, userID int64) (BillingReservation, error) {
	// Free interfaces still use the role QPS limit, but have nothing to reserve,
	// confirm or refund. Do not create a zero-value billing/wallet ledger entry.
	if api.assetsFree() {
		roleQPS, err := primaryRoleQPS(ctx, r.db, userID)
		if err != nil {
			return BillingReservation{}, err
		}
		chargeType := "FREE"
		if api.BillingDisabled || api.AssetMode == meteringAssetFree {
			chargeType = "NO_CHARGE"
		}
		return BillingReservation{ChargeType: chargeType, ChargeAmount: "0.0000", QPSLimit: roleQPS, Snapshot: billingSnapshot(api, keyID, userID)}, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return BillingReservation{}, err
	}
	defer func() { _ = tx.Rollback() }()

	reservation, err := r.reserveBillingTx(ctx, tx, api, keyID, userID)
	if err != nil {
		return BillingReservation{}, err
	}
	if err := dbx.CommitWithVerification(ctx, tx, func(checkCtx context.Context) (bool, error) {
		return r.verifyReservationCommit(checkCtx, reservation, api, keyID, userID)
	}); err != nil {
		if errors.Is(err, dbx.ErrCommitUncertain) {
			// The debit may exist. Preserve its ID only as a compensation
			// handle; callers must not admit the request when err != nil.
			return reservation, err
		}
		return BillingReservation{}, err
	}
	return reservation, nil
}

// ReservePackageQuota 在免费额度之前预占套餐额度（主套餐优先，其余按最早到期）。
// 与 ReserveBilling 的区别：不判断接口价格，也不回落到余额/点数，
// 只把这次调用计入套餐日/总量，并生成可释放的计费记录。
func (r *Repository) ReservePackageQuota(ctx context.Context, api InterfaceConfig, keyID, userID int64) (BillingReservation, bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return BillingReservation{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	snapshot := billingSnapshot(api, keyID, userID)
	roleQPS, err := primaryRoleQPS(ctx, tx, userID)
	if err != nil {
		return BillingReservation{}, false, err
	}
	primaryScope, err := primaryPackageScope(ctx, tx, userID)
	if err != nil {
		return BillingReservation{}, false, err
	}
	globalFirst := primaryScope != "INTERFACE"
	var reservation BillingReservation
	var reserved bool
	for attempt := 0; attempt < 2; attempt++ {
		if globalFirst == (attempt == 0) {
			reservation, reserved, err = r.reserveGlobalPackageTx(ctx, tx, api, keyID, userID, snapshot)
		} else {
			reservation, reserved, err = r.reserveInterfacePackageTx(ctx, tx, api, keyID, userID, snapshot)
		}
		if err != nil || reserved {
			break
		}
	}
	if err != nil {
		return BillingReservation{}, false, err
	}
	if !reserved {
		return BillingReservation{}, false, nil
	}
	reservation.QPSLimit = effectiveQPSLimit(roleQPS, reservation.QPSLimit, true)
	if err := dbx.CommitWithVerification(ctx, tx, func(checkCtx context.Context) (bool, error) {
		return r.verifyReservationCommit(checkCtx, reservation, api, keyID, userID)
	}); err != nil {
		if errors.Is(err, dbx.ErrCommitUncertain) {
			return reservation, true, err
		}
		return BillingReservation{}, false, err
	}
	return reservation, true, nil
}

func (r *Repository) reserveBillingTx(ctx context.Context, tx *sql.Tx, api InterfaceConfig, keyID, userID int64) (BillingReservation, error) {
	snapshot := billingSnapshot(api, keyID, userID)
	roleQPS, err := primaryRoleQPS(ctx, tx, userID)
	if err != nil {
		return BillingReservation{}, err
	}
	// 用户指定的主套餐决定两张权益表的尝试顺序：主套餐在接口层时先试接口套餐，
	// 否则维持「全站套餐优先」。表内排序再由 is_primary / end_at / 限额决定。
	primaryScope, err := primaryPackageScope(ctx, tx, userID)
	if err != nil {
		return BillingReservation{}, err
	}
	globalFirst := primaryScope != "INTERFACE"
	for attempt := 0; attempt < 2; attempt++ {
		var res BillingReservation
		var ok bool
		if globalFirst == (attempt == 0) {
			res, ok, err = r.reserveGlobalPackageTx(ctx, tx, api, keyID, userID, snapshot)
		} else {
			res, ok, err = r.reserveInterfacePackageTx(ctx, tx, api, keyID, userID, snapshot)
		}
		if err != nil || ok {
			if err == nil && ok {
				res.QPSLimit = effectiveQPSLimit(roleQPS, res.QPSLimit, true)
			}
			return res, err
		}
	}
	if api.PointPrice > 0 {
		if res, ok, err := r.reservePointsTx(ctx, tx, api, keyID, userID, snapshot); err != nil || ok {
			if err == nil && ok {
				res.QPSLimit = roleQPS
			}
			return res, err
		}
	}
	if api.Price != "0.0000" {
		if res, ok, err := r.reserveBalanceTx(ctx, tx, api, keyID, userID, snapshot); err != nil || ok {
			if err == nil && ok {
				res.QPSLimit = roleQPS
			}
			return res, err
		}
	}
	return BillingReservation{}, ErrInsufficientBalance
}

func primaryRoleQPS(ctx context.Context, tx queryExecer, userID int64) (int, error) {
	var limit int
	err := tx.QueryRowContext(ctx, primaryRoleQPSQuery, userID).Scan(&limit)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	return normalizeRoleQPS(limit), err
}

const primaryRoleQPSQuery = `
	select coalesce(primary_role.qps_limit, default_role.qps_limit, 1)
	from bp_user u
	left join bp_role primary_role on primary_role.id = u.primary_role_id
	left join bp_auth_register_config cfg on cfg.id = 1
	left join bp_role default_role on default_role.id = cfg.default_role_id
	where u.id = ?
	limit 1
`

// primaryPackageScope 返回用户指定的「主套餐」在哪一层权益里：
// GLOBAL / INTERFACE，未指定或已失效时返回空串（走默认的全站套餐优先）。
func primaryPackageScope(ctx context.Context, tx queryExecer, userID int64) (string, error) {
	var scope string
	// CASE 表达式恒定返回一行，避免 UNION + LIMIT 的括号写法在 MySQL 驱动上的歧义。
	err := tx.QueryRowContext(ctx, `
		select case
		  when exists(select 1 from bp_user_package_global
		               where user_id=? and is_primary=1 and status=1 and end_at>now()) then 'GLOBAL'
		  when exists(select 1 from bp_user_package_interface
		               where user_id=? and is_primary=1 and status=1 and end_at>now()) then 'INTERFACE'
		  else '' end
	`, userID, userID).Scan(&scope)
	return scope, err
}

// HasPaidEntitlement 判断用户此刻是否持有一条可用的付费途径：
// 有效且今日未达上限的套餐（全站或本接口），或可用于本次计费的余额/点数。
//
// 用途仅限于「用户组配额闸门是否放行」：配额限制的是免费额度总量，付费途径
// 一旦存在就应由计费事务决定能否调用，不该被免费额度二次拦截。具体扣哪一项
// 由 reserveBillingTx 决定，这里只做存在性判断。
//
// 复杂度：三条 exists，均命中 (user_id,...) 前缀索引，单次约 0.1ms 级。
func (r *Repository) HasPaidEntitlement(ctx context.Context, userID, interfaceID int64) (bool, error) {
	var usable bool
	err := r.db.QueryRowContext(ctx, `
		select
		  exists(select 1 from bp_user_package_global
		          where user_id=? and status=1 and start_at<=now() and end_at>now()
		            and (daily_limit=0 or if(day_key=curdate(),day_used,0)<daily_limit)
		            and (total_limit=0 or total_used<total_limit))
		  or exists(select 1 from bp_user_package_interface
		          where user_id=? and interface_id=? and status=1 and start_at<=now() and end_at>now()
		            and (daily_limit=0 or if(day_key=curdate(),day_used,0)<daily_limit)
		            and (total_limit=0 or total_used<total_limit))
		  or exists(select 1 from bp_user where id=? and (balance>0 or points>0))
	`, userID, userID, interfaceID, userID).Scan(&usable)
	return usable, err
}

func normalizeRoleQPS(limit int) int {
	if limit < 1 {
		return 1
	}
	if limit > 1_000_000 {
		return 1_000_000
	}
	return limit
}

func activePackageQPS(ctx context.Context, tx queryExecer, userID, interfaceID int64) (int, bool, error) {
	globalQPS, globalFound, err := maxActivePackageQPS(ctx, tx, `
		select cast(case when sum(qps_limit = 0) > 0 then 0 else max(qps_limit) end as signed)
		from bp_user_package_global
		where user_id = ? and status = 1 and start_at <= now() and end_at > now()
		  and (daily_limit = 0 or if(day_key = curdate(), day_used, 0) < daily_limit)
		  and (total_limit = 0 or total_used < total_limit)
	`, userID)
	if err != nil {
		return 0, false, err
	}
	interfaceQPS, interfaceFound, err := maxActivePackageQPS(ctx, tx, `
		select cast(case when sum(qps_limit = 0) > 0 then 0 else max(qps_limit) end as signed)
		from bp_user_package_interface
		where user_id = ? and interface_id = ? and status = 1 and start_at <= now() and end_at > now()
		  and (daily_limit = 0 or if(day_key = curdate(), day_used, 0) < daily_limit)
		  and (total_limit = 0 or total_used < total_limit)
	`, userID, interfaceID)
	if err != nil {
		return 0, false, err
	}
	limit, found := combinePackageQPS(globalQPS, globalFound, interfaceQPS, interfaceFound)
	return limit, found, nil
}

func maxActivePackageQPS(ctx context.Context, tx queryExecer, query string, args ...any) (int, bool, error) {
	var value sql.NullInt64
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
		return 0, false, err
	}
	if !value.Valid {
		return 0, false, nil
	}
	return int(value.Int64), true, nil
}

// combinePackageQPS keeps the "0 = unlimited" semantics while still allowing
// several different packages to contribute a per-user QPS. A package with 0
// QPS is stronger than any finite package; otherwise the highest finite value
// wins. The selected billing package may differ: billing consumes packages by
// earliest expiry, QPS is the maximum entitlement across all active packages.
func combinePackageQPS(globalQPS int, globalFound bool, interfaceQPS int, interfaceFound bool) (int, bool) {
	if !globalFound && !interfaceFound {
		return 0, false
	}
	if (globalFound && globalQPS == 0) || (interfaceFound && interfaceQPS == 0) {
		return 0, true
	}
	limit := 0
	if globalFound {
		limit = globalQPS
	}
	if interfaceFound && interfaceQPS > limit {
		limit = interfaceQPS
	}
	return limit, true
}

func effectiveQPSLimit(roleQPS, packageQPS int, hasPackage bool) int {
	roleQPS = normalizeRoleQPS(roleQPS)
	if !hasPackage || packageQPS < 0 {
		return roleQPS
	}
	// Package QPS 0 retains its existing meaning of unlimited. It is an
	// upgrade over any finite role quota, never a downgrade.
	if packageQPS == 0 {
		return 0
	}
	if packageQPS > 1_000_000 {
		packageQPS = 1_000_000
	}
	if packageQPS > roleQPS {
		return packageQPS
	}
	return roleQPS
}

func (r *Repository) reserveGlobalPackageTx(ctx context.Context, tx queryExecer, api InterfaceConfig, keyID, userID int64, snapshot string) (BillingReservation, bool, error) {
	if _, err := tx.ExecContext(ctx, `update bp_user_package_global set day_key=curdate(), day_used=0 where user_id=? and status=1 and (day_key is null or day_key<>curdate())`, userID); err != nil {
		return BillingReservation{}, false, err
	}
	var id int64
	var qps int
	err := tx.QueryRowContext(ctx, `
		select id,qps_limit from bp_user_package_global
		where user_id=? and status=1 and start_at<=now() and end_at>now()
		  and (daily_limit=0 or day_used<daily_limit)
		  and (total_limit=0 or total_used<total_limit)
		order by is_primary desc, end_at asc, daily_limit desc, qps_limit desc, id asc limit 1
	`, userID).Scan(&id, &qps)
	if errors.Is(err, sql.ErrNoRows) {
		return BillingReservation{}, false, nil
	}
	if err != nil {
		return BillingReservation{}, false, err
	}
	// 总量与日限在同一条件里裁决：任一打满都扣不出去（affected=0 → 视为无可用权益）。
	result, err := tx.ExecContext(ctx, `update bp_user_package_global set day_key=curdate(), day_used=day_used+1,total_used=total_used+1 where id=? and (daily_limit=0 or day_used<daily_limit) and (total_limit=0 or total_used<total_limit)`, id)
	if err != nil {
		return BillingReservation{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return BillingReservation{}, false, err
	}
	if affected == 0 {
		return BillingReservation{}, false, nil
	}
	recordID, err := createBillingRecordTx(ctx, tx, userID, keyID, api.ID, api.APICode, "GLOBAL_PACKAGE", "0.0000", 0, id, "RESERVED", "global package reserved", snapshot, !api.BillingLogDisabled)
	return BillingReservation{BillingRecordID: recordID, ChargeType: "GLOBAL_PACKAGE", ChargeAmount: "0.0000", PackageRecordID: id, QPSLimit: qps, Reserved: true, Snapshot: snapshot}, true, err
}

func (r *Repository) reserveInterfacePackageTx(ctx context.Context, tx queryExecer, api InterfaceConfig, keyID, userID int64, snapshot string) (BillingReservation, bool, error) {
	if _, err := tx.ExecContext(ctx, `update bp_user_package_interface set day_key=curdate(), day_used=0 where user_id=? and interface_id=? and status=1 and (day_key is null or day_key<>curdate())`, userID, api.ID); err != nil {
		return BillingReservation{}, false, err
	}
	var id int64
	var qps int
	err := tx.QueryRowContext(ctx, `
		select id,qps_limit from bp_user_package_interface
		where user_id=? and interface_id=? and status=1 and start_at<=now() and end_at>now()
		  and (daily_limit=0 or day_used<daily_limit)
		  and (total_limit=0 or total_used<total_limit)
		order by is_primary desc, end_at asc, daily_limit desc, qps_limit desc, id asc limit 1
	`, userID, api.ID).Scan(&id, &qps)
	if errors.Is(err, sql.ErrNoRows) {
		return BillingReservation{}, false, nil
	}
	if err != nil {
		return BillingReservation{}, false, err
	}
	result, err := tx.ExecContext(ctx, `update bp_user_package_interface set day_key=curdate(), day_used=day_used+1,total_used=total_used+1 where id=? and (daily_limit=0 or day_used<daily_limit) and (total_limit=0 or total_used<total_limit)`, id)
	if err != nil {
		return BillingReservation{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return BillingReservation{}, false, err
	}
	if affected == 0 {
		return BillingReservation{}, false, nil
	}
	recordID, err := createBillingRecordTx(ctx, tx, userID, keyID, api.ID, api.APICode, "INTERFACE_PACKAGE", "0.0000", 0, id, "RESERVED", "interface package reserved", snapshot, !api.BillingLogDisabled)
	return BillingReservation{BillingRecordID: recordID, ChargeType: "INTERFACE_PACKAGE", ChargeAmount: "0.0000", PackageRecordID: id, QPSLimit: qps, Reserved: true, Snapshot: snapshot}, true, err
}

func (r *Repository) reservePointsTx(ctx context.Context, tx queryExecer, api InterfaceConfig, keyID, userID int64, snapshot string) (BillingReservation, bool, error) {
	result, err := tx.ExecContext(ctx, `update bp_user set points=points-? where id=? and points>=?`, api.PointPrice, userID, api.PointPrice)
	if err != nil {
		return BillingReservation{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return BillingReservation{}, false, err
	}
	if affected == 0 {
		return BillingReservation{}, false, nil
	}
	recordID, err := createBillingRecordTx(ctx, tx, userID, keyID, api.ID, api.APICode, "POINTS", "0.0000", api.PointPrice, 0, "RESERVED", "points reserved", snapshot, !api.BillingLogDisabled)
	if err != nil {
		return BillingReservation{}, false, err
	}
	if err := insertWalletTx(ctx, tx, userID, "OPEN_CALL_POINTS", recordID, "0.0000", -api.PointPrice, "open call points reserved"); err != nil {
		return BillingReservation{}, false, err
	}
	return BillingReservation{BillingRecordID: recordID, ChargeType: "POINTS", ChargeAmount: "0.0000", PointAmount: api.PointPrice, Reserved: true, Snapshot: snapshot}, true, nil
}

func (r *Repository) reserveBalanceTx(ctx context.Context, tx queryExecer, api InterfaceConfig, keyID, userID int64, snapshot string) (BillingReservation, bool, error) {
	result, err := tx.ExecContext(ctx, `update bp_user set balance=balance-? where id=? and balance>=?`, api.Price, userID, api.Price)
	if err != nil {
		return BillingReservation{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return BillingReservation{}, false, err
	}
	if affected == 0 {
		return BillingReservation{}, false, nil
	}
	recordID, err := createBillingRecordTx(ctx, tx, userID, keyID, api.ID, api.APICode, "BALANCE", api.Price, 0, 0, "RESERVED", "balance reserved", snapshot, !api.BillingLogDisabled)
	if err != nil {
		return BillingReservation{}, false, err
	}
	if err := insertWalletTx(ctx, tx, userID, "OPEN_CALL_BALANCE", recordID, negativeMoney(api.Price), 0, "open call balance reserved"); err != nil {
		return BillingReservation{}, false, err
	}
	return BillingReservation{BillingRecordID: recordID, ChargeType: "BALANCE", ChargeAmount: api.Price, Reserved: true, Snapshot: snapshot}, true, nil
}

func (r *Repository) ConfirmBilling(ctx context.Context, res BillingReservation, callLogID int64) error {
	if res.BillingRecordID == 0 {
		return nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `update bp_billing_record set status='CONFIRMED', call_log_id=? where id=? and status='RESERVED'`, nullInt64(callLogID), res.BillingRecordID)
	if err != nil {
		return err
	}
	if err := requireAffected(result); err != nil {
		return err
	}
	if err := dbx.CommitWithVerification(ctx, tx, func(checkCtx context.Context) (bool, error) {
		return r.verifyBillingCommit(checkCtx, res, "CONFIRMED", callLogID)
	}); err != nil {
		return err
	}
	// See CreateOpenLogAndConfirm: keep the snapshot update off the billing
	// transaction's row locks.
	r.notifyConfirmedRevenue(ctx, res)
	return nil
}

func (r *Repository) ReleaseBilling(ctx context.Context, res BillingReservation, _ int64, reason string) error {
	if res.BillingRecordID == 0 || !res.Reserved {
		return nil
	}
	_, err := r.releaseBillingRecord(ctx, res.BillingRecordID, reason)
	return err
}

func (r *Repository) CreateOpenLogAndRelease(ctx context.Context, result OpenResult, res BillingReservation, reason string) (int64, error) {
	if res.BillingRecordID == 0 {
		return r.CreateOpenLog(ctx, result)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if res.BillingRecordID > 0 && res.Reserved {
		released, err := releaseBillingRecordTx(ctx, tx, res.BillingRecordID, reason, 0)
		if err != nil {
			return 0, err
		}
		if !released {
			var existingID int64
			err := tx.QueryRowContext(ctx, `
				select id from bp_interface_call_log
				where billing_record_id=?
				order by id limit 1
			`, res.BillingRecordID).Scan(&existingID)
			if err == nil {
				return existingID, nil
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return 0, err
			}
		}
	}
	logID, err := createOpenLogTx(ctx, tx, result)
	if err != nil {
		return 0, err
	}
	if res.BillingRecordID > 0 {
		if _, err := tx.ExecContext(ctx, `
			update bp_billing_record
			set call_log_id=coalesce(call_log_id,?)
			where id=?
		`, logID, res.BillingRecordID); err != nil {
			return 0, err
		}
	}
	if err := dbx.CommitWithVerification(ctx, tx, func(checkCtx context.Context) (bool, error) {
		return r.verifyOpenLogCommit(checkCtx, logID, res.BillingRecordID)
	}); err != nil {
		return 0, err
	}
	return logID, nil
}

func (r *Repository) releaseBillingRecord(ctx context.Context, billingRecordID int64, reason string) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	released, err := releaseBillingRecordTx(ctx, tx, billingRecordID, reason, 0)
	if err != nil {
		return false, err
	}
	if err := dbx.CommitWithVerification(ctx, tx, func(checkCtx context.Context) (bool, error) {
		return r.verifyBillingCommit(checkCtx, BillingReservation{BillingRecordID: billingRecordID}, "RELEASED", -1)
	}); err != nil {
		return false, err
	}
	return released, nil
}

func releaseBillingRecordTx(ctx context.Context, tx *sql.Tx, billingRecordID int64, reason string, callLogID int64) (bool, error) {
	reason = safeDiagnosticText(reason, 255)
	var userID, pointAmount int64
	var packageRecordID sql.NullInt64
	var chargeType, chargeAmount, status string
	if err := tx.QueryRowContext(ctx, `
		select user_id, charge_type, cast(charge_amount as char), point_amount, package_record_id, status
		from bp_billing_record
		where id=?
		for update
	`, billingRecordID).Scan(&userID, &chargeType, &chargeAmount, &pointAmount, &packageRecordID, &status); err != nil {
		return false, err
	}
	if status != "RESERVED" {
		return false, nil
	}
	switch chargeType {
	case "FREE":
	case "GLOBAL_PACKAGE", "INTERFACE_PACKAGE":
		// 计量方式以权益行快照为准：attempt = 失败也计量，释放时不回退
		// day_used/total_used（计费记录仍置 RELEASED，仅计数不退）。
		// 权益行被管理端删除时按 success 处理，让下方 update 的
		// requireAffected 继续报错，保持既有告警路径不变。
		table := "bp_user_package_global"
		if chargeType == "INTERFACE_PACKAGE" {
			table = "bp_user_package_interface"
		}
		var countMode string
		err := tx.QueryRowContext(ctx,
			"select count_mode from "+table+" where id=?", packageRecordID.Int64).Scan(&countMode)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, err
		}
		if countMode == "attempt" {
			break
		}
		result, err := tx.ExecContext(ctx, "update "+table+" set day_used=greatest(day_used-1,0), total_used=greatest(total_used-1,0) where id=?", packageRecordID.Int64)
		if err != nil {
			return false, err
		}
		if err := requireAffected(result); err != nil {
			return false, err
		}
	case "POINTS":
		if pointAmount <= 0 || pointAmount > maxGatewayPoints {
			return false, ErrPointRefundCapacity
		}
		remainingCapacity := maxGatewayPoints - pointAmount
		result, err := tx.ExecContext(ctx, `
			update bp_user
			set points=points+?
			where id=? and points<=?
		`, pointAmount, userID, remainingCapacity)
		if err != nil {
			return false, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		if affected == 0 {
			return false, ErrPointRefundCapacity
		}
		if err := insertWalletTx(ctx, tx, userID, "OPEN_CALL_POINTS_REFUND", billingRecordID, "0.0000", pointAmount, reason); err != nil {
			return false, err
		}
	case "BALANCE":
		result, err := tx.ExecContext(ctx, `
			update bp_user
			set balance=balance+?
			where id=? and balance<=cast(? as decimal(18,4))-cast(? as decimal(18,4))
		`, chargeAmount, userID, maxGatewayBalanceText, chargeAmount)
		if err != nil {
			return false, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		if affected == 0 {
			return false, ErrBalanceRefundCapacity
		}
		if err := insertWalletTx(ctx, tx, userID, "OPEN_CALL_BALANCE_REFUND", billingRecordID, chargeAmount, 0, reason); err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("unsupported reserved billing charge type %q", chargeType)
	}
	result, err := tx.ExecContext(ctx, `
		update bp_billing_record
		set status='RELEASED', reason=?, call_log_id=coalesce(?,call_log_id)
		where id=? and status='RESERVED'
	`, reason, nullInt64(callLogID), billingRecordID)
	if err != nil {
		return false, err
	}
	if err := requireAffected(result); err != nil {
		return false, err
	}
	return true, nil
}

// responseBodyForLog 用于持久化调用日志的 response_body 列。
// 响应体是调用方本来就收到的业务数据（含解析结果里的视频直链），保留其
// 可读性；只有凭据、URL 查询值中的敏感 key 与内网地址被脱敏。上游路由地址
// 由独立的 upstream_url 列 + masking.UpstreamReference 保护，不再使用会
// 无差别端点脱敏的 LogJSONText——那会把 86% 以上的响应体清成
// [UPSTREAM_REDACTED]，让调用日志失去排查价值。
func responseBodyForLog(value string) string {
	if value == "" {
		return ""
	}
	if !utf8.ValidString(value) {
		return "[BINARY_RESPONSE_OMITTED]"
	}
	return truncateUTF8Bytes(masking.ResponseJSONText(value), 20000)
}

// requestParamsForPersistence 用于持久化调用日志的 request_params 列。
// 业务语义：调用方实际提交的请求体 / 查询参数要保留可读性（含业务 URL、参数名、参数值），
// 仅脱敏 JSON 内的敏感字段（token/password/apiKey 等键名对应的值）以及 URL 查询值中的敏感 key。
// 不再使用会无差别端点脱敏的 LogJSONText——那会把用户传入的商品链接等业务 URL
// 全部清成 [UPSTREAM_REDACTED]，让调用日志失去排查价值。上游路由地址由独立的
// upstream_url 列 + masking.UpstreamReference 保护，二者职责分离。
func requestParamsForPersistence(value string) string {
	return truncateUTF8Bytes(masking.SensitiveJSONTextWithURLs(value), 20000)
}

// requestValueForPersistence 同上，用于 request_value 列（链接或值列展示）。
// 保留 URL 主体与普通参数，仅对 URL 查询值中的敏感 key 做 key-based 脱敏，
// 对裸字符串做 JSON 凭证兜底脱敏。
func requestValueForPersistence(value string) string {
	return truncateUTF8Bytes(masking.Text(value), 500)
}

func truncateUTF8Bytes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(value) <= limit {
		return value
	}
	value = value[:limit]
	for value != "" && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func safeDiagnosticText(value string, limit int) string {
	return truncateRunes(strings.TrimSpace(masking.ErrorText(value)), limit)
}

func (r *Repository) ReconcileStaleBilling(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		select id
		from bp_billing_record
		where status='RESERVED' and created_at < ?
		order by id
		limit ?
	`, before, limit)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	released := 0
	var releaseErrors error
	for _, id := range ids {
		didRelease, err := r.releaseBillingRecord(ctx, id, "stale reservation automatically released")
		if err != nil {
			releaseErrors = errors.Join(releaseErrors, fmt.Errorf("release billing record %d: %w", id, err))
			continue
		}
		if didRelease {
			released++
		}
	}
	return released, releaseErrors
}

func createBillingRecordTx(ctx context.Context, exec execer, userID, keyID, interfaceID int64, apiCode, chargeType, amount string, points, packageRecordID int64, status, reason, snapshot string, logVisibility ...bool) (int64, error) {
	logVisible := len(logVisibility) == 0 || logVisibility[0]
	reason = safeDiagnosticText(reason, 255)
	result, err := exec.ExecContext(ctx, `
		insert into bp_billing_record(user_id,api_key_id,interface_id,api_code,charge_type,charge_amount,point_amount,package_record_id,status,reason,snapshot,log_visible)
		values(?,?,?,?,?,?,?,?,?,?,?,?)
	`, userID, nullInt64(keyID), interfaceID, apiCode, chargeType, amount, points, nullInt64(packageRecordID), status, nullIfBlank(reason), jsonTextOrNull(snapshot), boolInt(logVisible))
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func insertWalletTx(ctx context.Context, exec execer, userID int64, bizType string, bizID int64, amount string, points int64, remark string) error {
	result, err := exec.ExecContext(ctx, `
		insert into bp_wallet_transaction(user_id,biz_type,biz_id,amount,points,balance_after,points_after,remark)
		select id,?,?,?,?,balance,points,? from bp_user where id=?
	`, bizType, nullInt64(bizID), amount, points, remark, userID)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

func negativeMoney(amount string) string {
	amount = strings.TrimSpace(amount)
	if isZeroMoney(amount) {
		return "0.0000"
	}
	if strings.HasPrefix(amount, "-") {
		return amount
	}
	return "-" + amount
}

func isZeroMoney(amount string) bool {
	amount = strings.TrimSpace(amount)
	amount = strings.TrimPrefix(amount, "+")
	amount = strings.TrimPrefix(amount, "-")
	return strings.Trim(amount, "0.") == ""
}

func (r *Repository) MarketTestKey(ctx context.Context, userID, interfaceID int64) (APIKeyItem, bool, error) {
	rows, err := r.db.QueryContext(ctx, marketTestKeyQuery(), interfaceID, userID, interfaceID)
	if err != nil {
		return APIKeyItem{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return APIKeyItem{}, false, nil
	}
	item, err := scanKeyItem(rows)
	return item, err == nil, err
}

func (r *Repository) ListInvokeKeys(ctx context.Context, userID, interfaceID int64, keyword string, limit int) ([]keyaccess.InvokeKeyOption, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	keyword = strings.TrimSpace(keyword)
	args := []any{userID, interfaceID}
	query := invokeKeysQuery(keyword != "")
	if keyword != "" {
		args = append(args, "%"+escapeLikeKeyword(keyword)+"%")
	}
	args = append(args, limit)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]keyaccess.InvokeKeyOption, 0)
	for rows.Next() {
		var item keyaccess.InvokeKeyOption
		var id int64
		var prefix, suffix string
		var secretAvailable int
		if err := rows.Scan(&id, &item.Name, &prefix, &suffix, &item.KeyScope, &item.ExpiresAt, &secretAvailable); err != nil {
			return nil, err
		}
		item.ID = strconv.FormatInt(id, 10)
		item.MaskedKey = prefix + "****" + suffix
		item.SecretAvailable = secretAvailable == 1
		items = append(items, item)
	}
	return items, rows.Err()
}

func invokeKeysQuery(withKeyword bool) string {
	keywordSQL := ""
	if withKeyword {
		keywordSQL = ` and k.name like ? escape '\\'`
	}
	return `
		select k.id, k.name, k.key_prefix, k.key_suffix, k.key_type,
		       coalesce(date_format(k.expires_at, '%Y-%m-%d %H:%i:%s'), ''),
		       if(coalesce(k.secret_ciphertext, '') <> '', 1, 0)
		from bp_api_key k
		where k.user_id = ?
		  and k.status = 1
		  and (k.expires_at is null or k.expires_at > now())
		  and (k.key_type = 'GLOBAL' or exists (
		      select 1 from bp_api_key_interface bi
		      where bi.key_id = k.id and bi.interface_id = ?
		  ))` + keywordSQL + `
		order by k.id desc
		limit ?
	`
}

func escapeLikeKeyword(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func marketTestKeyQuery() string {
	return `
		select k.id, k.user_id, u.username, k.name, k.key_prefix, k.key_suffix,
		       coalesce(k.secret_ciphertext, ''), k.key_type, k.status, k.suspended_by_ban,
		       coalesce(date_format(k.expires_at, '%Y-%m-%d %H:%i:%s'), ''),
		       coalesce(date_format(k.last_used_at, '%Y-%m-%d %H:%i:%s'), ''),
		       date_format(k.created_at, '%Y-%m-%d %H:%i:%s'),
		       date_format(k.updated_at, '%Y-%m-%d %H:%i:%s'),
		       ifnull(i.interface_count, 0), ifnull(w.whitelist_count, 0)
		from bp_api_key k
		inner join bp_user u on u.id = k.user_id
		inner join bp_interface_api a on a.id = ? and a.status = 1
		left join (select key_id, count(*) interface_count from bp_api_key_interface group by key_id) i on i.key_id = k.id
		left join (select key_id, count(*) whitelist_count from bp_api_key_ip_whitelist group by key_id) w on w.key_id = k.id
		where k.user_id = ?
		  and k.status = 1
		  and (k.expires_at is null or k.expires_at > now())
		  and (k.key_type = 'GLOBAL' or exists (select 1 from bp_api_key_interface bi where bi.key_id = k.id and bi.interface_id = ?))
		order by k.id desc
		limit 1
	`
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

func normalizePage(page, pageSize int) (int, int) {
	return pagination.Normalize(page, pageSize, 100)
}

func parseID(value string) int64 {
	id, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return id
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func nullIfBlank(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func nullFloat64(value float64) any {
	if value == 0 {
		return nil
	}
	return value
}

func jsonTextOrNull(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
