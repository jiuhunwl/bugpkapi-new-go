package openapi

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"bugpk-api/internal/pkg/accountstatus"
	"bugpk-api/internal/pkg/pagination"
)

// Repository 承载开放 API 域的全部 SQL：全局配置单例、Key 生命周期、
// scope 清单、IP 白名单与 bp_user 两态开关。跨域读写（账号快照/权益/下单/
// 兑换）一律走各域 service，本仓储只碰自己这五张表。
type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

var errNotFound = errors.New("openapi: row not found")

// ---------------------------------------------------------------- 全局配置

func (r *Repository) Config(ctx context.Context) (Config, error) {
	var item Config
	var enabled int
	err := r.db.QueryRowContext(ctx, `
		select enabled, ip_whitelist_max, key_default_expire_days, admin_key_expire_days,
		       rate_limit_normal, rate_limit_admin
		from bp_open_api_config where id = 1
	`).Scan(&enabled, &item.IPWhitelistMax, &item.KeyDefaultExpireDays, &item.AdminKeyExpireDays,
		&item.RateLimitNormal, &item.RateLimitAdmin)
	if err != nil {
		return Config{}, err
	}
	item.Enabled = enabled == 1
	return item, nil
}

func (r *Repository) UpdateConfig(ctx context.Context, item Config) error {
	enabled := 0
	if item.Enabled {
		enabled = 1
	}
	result, err := r.db.ExecContext(ctx, `
		update bp_open_api_config
		set enabled=?, ip_whitelist_max=?, key_default_expire_days=?, admin_key_expire_days=?,
		    rate_limit_normal=?, rate_limit_admin=?
		where id = 1
	`, enabled, item.IPWhitelistMax, item.KeyDefaultExpireDays, item.AdminKeyExpireDays,
		item.RateLimitNormal, item.RateLimitAdmin)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// ---------------------------------------------------------------- 用户开关

// UpdateUserSwitches 更新 bp_user 的开放 API 两态开关（nil 表示不改）。
func (r *Repository) UpdateUserSwitches(ctx context.Context, userID int64, enabled, adminDisabled *bool) error {
	sets := make([]string, 0, 2)
	args := make([]any, 0, 3)
	if enabled != nil {
		sets = append(sets, "open_api_enabled=?")
		args = append(args, boolToInt(*enabled))
	}
	if adminDisabled != nil {
		sets = append(sets, "open_api_admin_disabled=?")
		args = append(args, boolToInt(*adminDisabled))
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, userID)
	result, err := r.db.ExecContext(ctx, `update bp_user set `+strings.Join(sets, ", ")+` where id=?`, args...)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// RevokeAllUserKeys 一键吊销某账号名下全部有效 Key（兜底按钮）。
func (r *Repository) RevokeAllUserKeys(ctx context.Context, userID int64) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		update bp_open_api_key set status=0, revoked_at=now() where user_id=? and status=1
	`, userID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// ---------------------------------------------------------------- Key

type keyRow struct {
	ID           int64
	UserID       int64
	Username     string
	Name         string
	KeyHash      string
	KeyPrefix    string
	KeySuffix    string
	IsAdminScope bool
	Status       int
	ExpiresAt    time.Time
	LastUsedAt   sql.NullTime
	CreatedAt    time.Time
	RevokedAt    sql.NullTime
}

const keyColumns = `
	k.id, k.user_id, coalesce(u.username,''), k.name, k.key_hash, k.key_prefix, k.key_suffix,
	k.is_admin_scope, k.status, k.expires_at, k.last_used_at, k.created_at, k.revoked_at`

func scanKeyRow(scanner interface {
	Scan(dest ...any) error
}) (keyRow, error) {
	var item keyRow
	var adminScope int
	err := scanner.Scan(&item.ID, &item.UserID, &item.Username, &item.Name, &item.KeyHash,
		&item.KeyPrefix, &item.KeySuffix, &adminScope, &item.Status, &item.ExpiresAt,
		&item.LastUsedAt, &item.CreatedAt, &item.RevokedAt)
	if err != nil {
		return keyRow{}, err
	}
	item.IsAdminScope = adminScope == 1
	return item, nil
}

// CreateKey 落一行 Key（唯一键 uk_open_api_key_active_user 保证每账号
// 至多一个有效 Key；撞键由 service 映射为 openapi.key_limit）。
func (r *Repository) CreateKey(ctx context.Context, userID int64, name, hash, prefix, suffix string,
	adminScope bool, expiresAt time.Time) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		insert into bp_open_api_key (user_id, name, key_hash, key_prefix, key_suffix, is_admin_scope, status, expires_at)
		values (?,?,?,?,?,?,1,?)
	`, userID, name, hash, prefix, suffix, boolToInt(adminScope), expiresAt)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// RevokeKey 吊销单把 Key（status 1→0，不可改回；条件推进防并发重复吊销）。
func (r *Repository) RevokeKey(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, `
		update bp_open_api_key set status=?, revoked_at=now() where id=? and status=?
	`, KeyStatusRevoked, id, KeyStatusActive)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// KeyByID 读单把 Key（可选限定归属账号）。
func (r *Repository) KeyByID(ctx context.Context, id int64, ownerID int64) (keyRow, error) {
	query := `select ` + keyColumns + ` from bp_open_api_key k left join bp_user u on u.id=k.user_id where k.id=?`
	args := []any{id}
	if ownerID > 0 {
		query += ` and k.user_id=?`
		args = append(args, ownerID)
	}
	item, err := scanKeyRow(r.db.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return keyRow{}, errNotFound
	}
	return item, err
}

// ActiveKeyByUser 读账号当前有效 Key（不存在返回 errNotFound）。
func (r *Repository) ActiveKeyByUser(ctx context.Context, userID int64) (keyRow, error) {
	item, err := scanKeyRow(r.db.QueryRowContext(ctx, `
		select `+keyColumns+`
		from bp_open_api_key k left join bp_user u on u.id=k.user_id
		where k.user_id=? and k.status=?
	`, userID, KeyStatusActive))
	if errors.Is(err, sql.ErrNoRows) {
		return keyRow{}, errNotFound
	}
	return item, err
}

// KeyAuthContext 是鉴权链一次 JOIN 取齐的判定输入：Key + 归属账号三层开关状态。
type KeyAuthContext struct {
	Key                keyRow
	UserStatus         int
	UserOpenAPIEnabled int
	UserAdminDisabled  int
	Username           string
}

// AuthContextByHash 用摘要一次取齐 Key 与账号开关状态（鉴权链唯一查询）。
func (r *Repository) AuthContextByHash(ctx context.Context, hash string) (KeyAuthContext, error) {
	var item KeyAuthContext
	var adminScope int
	err := r.db.QueryRowContext(ctx, `
		select k.id, k.user_id, coalesce(u.username,''), k.name, k.key_hash, k.key_prefix, k.key_suffix,
		       k.is_admin_scope, k.status, k.expires_at, k.last_used_at, k.created_at, k.revoked_at,
		       coalesce(`+accountstatus.EffectiveStatusSQL("u")+`,0), coalesce(u.open_api_enabled,0), coalesce(u.open_api_admin_disabled,0)
		from bp_open_api_key k
		left join bp_user u on u.id = k.user_id
		where k.key_hash = ?
	`, hash).Scan(&item.Key.ID, &item.Key.UserID, &item.Key.Username, &item.Key.Name, &item.Key.KeyHash,
		&item.Key.KeyPrefix, &item.Key.KeySuffix, &adminScope, &item.Key.Status, &item.Key.ExpiresAt,
		&item.Key.LastUsedAt, &item.Key.CreatedAt, &item.Key.RevokedAt,
		&item.UserStatus, &item.UserOpenAPIEnabled, &item.UserAdminDisabled)
	if errors.Is(err, sql.ErrNoRows) {
		return KeyAuthContext{}, errNotFound
	}
	if err != nil {
		return KeyAuthContext{}, err
	}
	item.Key.IsAdminScope = adminScope == 1
	item.Username = item.Key.Username
	return item, nil
}

// TouchLastUsedAt 异步更新最近使用时间（失败不影响业务，不阻塞请求）。
func (r *Repository) TouchLastUsedAt(ctx context.Context, keyID int64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		update bp_open_api_key set last_used_at=? where id=? and (last_used_at is null or last_used_at < ?)
	`, at, keyID, at)
	return err
}

// ListKeys 分页查询（ownerID>0 时限定本人，adminAll=true 时跨账号）。
func (r *Repository) ListKeys(ctx context.Context, keyword string, ownerID int64, adminAll bool,
	page, pageSize int) ([]keyRow, int64, error) {
	page, size := pagination.Normalize(page, pageSize, 100)
	where := "where 1=1"
	var args []any
	if !adminAll {
		where += " and k.user_id=?"
		args = append(args, ownerID)
	}
	if keyword != "" {
		where += " and (k.name like ? or u.username like ?)"
		like := "%" + keyword + "%"
		args = append(args, like, like)
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, `
		select count(*) from bp_open_api_key k left join bp_user u on u.id=k.user_id `+where, args...).
		Scan(&total); err != nil {
		return nil, 0, err
	}
	listArgs := append(append([]any(nil), args...), size, pagination.Offset(page, size))
	rows, err := r.db.QueryContext(ctx, `
		select `+keyColumns+`
		from bp_open_api_key k left join bp_user u on u.id=k.user_id `+where+`
		order by k.id desc limit ? offset ?
	`, listArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]keyRow, 0, size)
	for rows.Next() {
		item, err := scanKeyRow(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, item)
	}
	return items, total, rows.Err()
}

// ---------------------------------------------------------------- scope

// ReplaceScopes 覆盖式写入 Key 的 scope 清单（先删后插，事务由调用方提供）。
func (r *Repository) ReplaceScopes(ctx context.Context, keyID int64, scopes []string) error {
	if _, err := r.db.ExecContext(ctx, `delete from bp_open_api_key_scope where key_id=?`, keyID); err != nil {
		return err
	}
	if len(scopes) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(scopes))
	args := make([]any, 0, len(scopes)*2)
	for _, code := range scopes {
		placeholders = append(placeholders, "(?,?)")
		args = append(args, keyID, code)
	}
	_, err := r.db.ExecContext(ctx,
		`insert into bp_open_api_key_scope (key_id, endpoint_code) values `+strings.Join(placeholders, ","), args...)
	return err
}

// ScopesByKey 读单 Key 的 scope 清单。
func (r *Repository) ScopesByKey(ctx context.Context, keyID int64) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		select endpoint_code from bp_open_api_key_scope where key_id=? order by endpoint_code
	`, keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	scopes := make([]string, 0, 8)
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		scopes = append(scopes, code)
	}
	return scopes, rows.Err()
}

// ScopesByKeys 批量读 scope（列表页避免 N+1）。
func (r *Repository) ScopesByKeys(ctx context.Context, keyIDs []int64) (map[int64][]string, error) {
	result := make(map[int64][]string, len(keyIDs))
	if len(keyIDs) == 0 {
		return result, nil
	}
	placeholders := make([]string, 0, len(keyIDs))
	args := make([]any, 0, len(keyIDs))
	for _, id := range keyIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	rows, err := r.db.QueryContext(ctx, `
		select key_id, endpoint_code from bp_open_api_key_scope
		where key_id in (`+strings.Join(placeholders, ",")+`)
		order by key_id, endpoint_code
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var keyID int64
		var code string
		if err := rows.Scan(&keyID, &code); err != nil {
			return nil, err
		}
		result[keyID] = append(result[keyID], code)
	}
	return result, rows.Err()
}

// ---------------------------------------------------------------- IP 白名单

// ReplaceIPWhitelist 覆盖式写入白名单（先删后插）。
func (r *Repository) ReplaceIPWhitelist(ctx context.Context, keyID int64, rules []string) error {
	if _, err := r.db.ExecContext(ctx, `delete from bp_open_api_key_ip_whitelist where key_id=?`, keyID); err != nil {
		return err
	}
	if len(rules) == 0 {
		return nil
	}
	placeholders := make([]string, 0, len(rules))
	args := make([]any, 0, len(rules)*2)
	for _, rule := range rules {
		placeholders = append(placeholders, "(?,?)")
		args = append(args, keyID, rule)
	}
	_, err := r.db.ExecContext(ctx,
		`insert into bp_open_api_key_ip_whitelist (key_id, ip_rule) values `+strings.Join(placeholders, ","), args...)
	return err
}

// IPWhitelistByKey 读单 Key 白名单。
func (r *Repository) IPWhitelistByKey(ctx context.Context, keyID int64) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		select ip_rule from bp_open_api_key_ip_whitelist where key_id=? order by id
	`, keyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rules := make([]string, 0, 4)
	for rows.Next() {
		var rule string
		if err := rows.Scan(&rule); err != nil {
			return nil, err
		}
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

// IPWhitelistByKeys 批量读白名单（列表页避免 N+1）。
func (r *Repository) IPWhitelistByKeys(ctx context.Context, keyIDs []int64) (map[int64][]string, error) {
	result := make(map[int64][]string, len(keyIDs))
	if len(keyIDs) == 0 {
		return result, nil
	}
	placeholders := make([]string, 0, len(keyIDs))
	args := make([]any, 0, len(keyIDs))
	for _, id := range keyIDs {
		placeholders = append(placeholders, "?")
		args = append(args, id)
	}
	rows, err := r.db.QueryContext(ctx, `
		select key_id, ip_rule from bp_open_api_key_ip_whitelist
		where key_id in (`+strings.Join(placeholders, ",")+`)
		order by key_id, id
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var keyID int64
		var rule string
		if err := rows.Scan(&keyID, &rule); err != nil {
			return nil, err
		}
		result[keyID] = append(result[keyID], rule)
	}
	return result, rows.Err()
}

// ---------------------------------------------------------------- 工具

func requireAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errNotFound
	}
	return nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func formatTime(value time.Time) string {
	return value.Format("2006-01-02 15:04:05")
}

func formatNullTime(value sql.NullTime) string {
	if !value.Valid {
		return ""
	}
	return formatTime(value.Time)
}
