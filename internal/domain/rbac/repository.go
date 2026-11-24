package rbac

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"bugpk-api/internal/pkg/accountstatus"
	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/pagination"

	mysqlDriver "github.com/go-sql-driver/mysql"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) FindPrincipal(ctx context.Context, userID int64) (Principal, error) {
	row := r.db.QueryRowContext(ctx, `
		select id, username, `+accountstatus.EffectiveStatusSQL("")+`,
		       ban_permanent, coalesce(date_format(ban_until, '%Y-%m-%d %H:%i:%s'), ''), coalesce(ban_reason, ''), token_version
		from bp_user
		where id = ?
		limit 1
	`, userID)
	var principal Principal
	if err := row.Scan(
		&principal.ID,
		&principal.Username,
		&principal.Status,
		&principal.BanPermanent,
		&principal.BanUntil,
		&principal.BanReason,
		&principal.TokenVersion,
	); err != nil {
		return Principal{}, err
	}
	if !accountstatus.IsActive(principal.Status) {
		return principal, ErrUserDisabled
	}
	roles, err := r.RoleKeysByUserID(ctx, userID)
	if err != nil {
		return Principal{}, err
	}
	principal.Roles = roles
	return principal, nil
}

func (r *Repository) RoleKeysByUserID(ctx context.Context, userID int64) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `
		select r.role_key
		from bp_role r
		inner join bp_user_role ur on ur.role_id = r.id
		where ur.user_id = ? and r.status = 1
		order by r.id
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		roles = append(roles, role)
	}
	return roles, rows.Err()
}

func (r *Repository) AllMenus(ctx context.Context) ([]Menu, error) {
	rows, err := r.db.QueryContext(ctx, `
		select id, pid, name, path, coalesce(component, ''), coalesce(redirect, ''),
		       type, coalesce(auth_code, ''), coalesce(cast(meta as char), '{}'), status
		from bp_menu
		order by coalesce(json_extract(meta, '$.order'), 999999), id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMenus(rows)
}

func (r *Repository) EnabledMenus(ctx context.Context) ([]Menu, error) {
	rows, err := r.db.QueryContext(ctx, `
		select id, pid, name, path, coalesce(component, ''), coalesce(redirect, ''),
		       type, coalesce(auth_code, ''), coalesce(cast(meta as char), '{}'), status
		from bp_menu
		where status = 1
		order by coalesce(json_extract(meta, '$.order'), 999999), id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMenus(rows)
}

func scanMenus(rows *sql.Rows) ([]Menu, error) {
	var menus []Menu
	for rows.Next() {
		var menu Menu
		var metaText string
		if err := rows.Scan(&menu.ID, &menu.PID, &menu.Name, &menu.Path, &menu.Component, &menu.Redirect, &menu.Type, &menu.AuthCode, &metaText, &menu.Status); err != nil {
			return nil, err
		}
		menu.IDText = strconv.FormatInt(menu.ID, 10)
		menu.PIDText = strconv.FormatInt(menu.PID, 10)
		menu.Meta = map[string]any{}
		if err := json.Unmarshal([]byte(metaText), &menu.Meta); err != nil {
			return nil, err
		}
		menus = append(menus, menu)
	}
	return menus, rows.Err()
}

func (r *Repository) MenuIDsByUser(ctx context.Context, userID int64) (map[int64]bool, error) {
	rows, err := r.db.QueryContext(ctx, `
		select distinct rm.menu_id
		from bp_role_menu rm
		inner join bp_user_role ur on ur.role_id = rm.role_id
		inner join bp_role r on r.id = ur.role_id
		inner join bp_menu m on m.id = rm.menu_id
		where ur.user_id = ? and r.status = 1 and m.status = 1
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids[id] = true
	}
	return ids, rows.Err()
}

func (r *Repository) PermissionCodesByUser(ctx context.Context, userID int64, admin bool) ([]string, error) {
	var rows *sql.Rows
	var err error
	if admin {
		rows, err = r.db.QueryContext(ctx, `
			select distinct auth_code
			from bp_menu
			where status = 1 and auth_code is not null and auth_code <> ''
			order by auth_code
		`)
	} else {
		rows, err = r.db.QueryContext(ctx, `
			select distinct m.auth_code
			from bp_menu m
			inner join bp_role_menu rm on rm.menu_id = m.id
			inner join bp_user_role ur on ur.role_id = rm.role_id
			inner join bp_role r on r.id = ur.role_id
			where ur.user_id = ?
			  and r.status = 1
			  and m.status = 1
			  and m.auth_code is not null
			  and m.auth_code <> ''
			order by m.auth_code
		`, userID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var codes []string
	for rows.Next() {
		var code string
		if err := rows.Scan(&code); err != nil {
			return nil, err
		}
		codes = append(codes, code)
	}
	return codes, rows.Err()
}

func (r *Repository) MenuRoleIDs(ctx context.Context, menuID int64) ([]int64, []string, []string, error) {
	rows, err := r.db.QueryContext(ctx, `
		select r.id, r.role_key, r.role_name
		from bp_role r
		inner join bp_role_menu rm on rm.role_id = r.id
		where rm.menu_id = ?
		order by r.id
	`, menuID)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	var ids []int64
	var keys []string
	var names []string
	for rows.Next() {
		var id int64
		var key, name string
		if err := rows.Scan(&id, &key, &name); err != nil {
			return nil, nil, nil, err
		}
		ids = append(ids, id)
		keys = append(keys, key)
		names = append(names, name)
	}
	return ids, keys, names, rows.Err()
}

func (r *Repository) NameExists(ctx context.Context, name string, excludeID int64) (bool, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, "select count(*) from bp_menu where name = ? and id <> ?", name, excludeID).Scan(&count)
	return count > 0, err
}

func (r *Repository) PathExists(ctx context.Context, path string, excludeID int64) (bool, error) {
	if strings.TrimSpace(path) == "" {
		return false, nil
	}
	var count int64
	err := r.db.QueryRowContext(ctx, "select count(*) from bp_menu where path = ? and path <> '' and id <> ?", path, excludeID).Scan(&count)
	return count > 0, err
}

func (r *Repository) CreateMenu(ctx context.Context, menu Menu, roleIDs []int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	menus, err := lockMenuTree(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateLockedMenu(0, menu, menus); err != nil {
		return err
	}
	meta, err := json.Marshal(menu.Meta)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		insert into bp_menu(pid, name, path, component, redirect, type, auth_code, meta, status)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, menu.PID, menu.Name, menu.Path, nullIfBlank(menu.Component), nullIfBlank(menu.Redirect), menu.Type, nullIfBlank(menu.AuthCode), string(meta), menu.Status)
	if err != nil {
		if isDuplicateKey(err) {
			return ErrMenuNameExists
		}
		return err
	}
	menuID, err := result.LastInsertId()
	if err != nil {
		return err
	}
	if err := replaceMenuRoles(ctx, tx, menuID, roleIDs); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) UpdateMenu(ctx context.Context, menuID int64, menu Menu, roleIDs []int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	menus, err := lockMenuTree(ctx, tx)
	if err != nil {
		return err
	}
	if err := validateLockedMenu(menuID, menu, menus); err != nil {
		return err
	}
	meta, err := json.Marshal(menu.Meta)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		update bp_menu
		set pid = ?, name = ?, path = ?, component = ?, redirect = ?, type = ?, auth_code = ?, meta = ?, status = ?
		where id = ?
	`, menu.PID, menu.Name, menu.Path, nullIfBlank(menu.Component), nullIfBlank(menu.Redirect), menu.Type, nullIfBlank(menu.AuthCode), string(meta), menu.Status, menuID)
	if err != nil {
		if isDuplicateKey(err) {
			return ErrMenuNameExists
		}
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		if err := requireMenuExists(ctx, tx, menuID); err != nil {
			return err
		}
	}
	if err := replaceMenuRoles(ctx, tx, menuID, roleIDs); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) DeleteMenu(ctx context.Context, menuID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	menus, err := lockMenuTree(ctx, tx)
	if err != nil {
		return err
	}
	found := false
	for _, menu := range menus {
		if menu.ID == menuID {
			found = true
		}
		if menu.PID == menuID {
			return ErrMenuHasChildren
		}
	}
	if !found {
		return sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_role_menu where menu_id = ?", menuID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "delete from bp_menu where id = ?", menuID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func replaceMenuRoles(ctx context.Context, tx *sql.Tx, menuID int64, roleIDs []int64) error {
	if _, err := tx.ExecContext(ctx, "delete from bp_role_menu where menu_id = ?", menuID); err != nil {
		return err
	}
	seen := map[int64]struct{}{}
	for _, roleID := range roleIDs {
		if _, exists := seen[roleID]; exists {
			continue
		}
		seen[roleID] = struct{}{}
		result, err := tx.ExecContext(ctx, `
			insert into bp_role_menu(role_id, menu_id)
			select id, ? from bp_role where id = ? and status = 1
		`, menuID, roleID)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return ErrRoleNotFound
		}
	}
	return nil
}

func lockMenuTree(ctx context.Context, tx *sql.Tx) ([]Menu, error) {
	rows, err := tx.QueryContext(ctx, `select id,pid,name,path from bp_menu order by id for update`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var menus []Menu
	for rows.Next() {
		var menu Menu
		if err := rows.Scan(&menu.ID, &menu.PID, &menu.Name, &menu.Path); err != nil {
			return nil, err
		}
		menus = append(menus, menu)
	}
	return menus, rows.Err()
}

func validateLockedMenu(menuID int64, menu Menu, menus []Menu) error {
	if err := validateMenuParent(menuID, menu.PID, menus); err != nil {
		return err
	}
	for _, current := range menus {
		if current.ID == menuID {
			continue
		}
		if current.Name == menu.Name {
			return ErrMenuNameExists
		}
		if menu.Path != "" && current.Path == menu.Path {
			return ErrMenuPathExists
		}
	}
	return nil
}

func requireMenuExists(ctx context.Context, tx *sql.Tx, menuID int64) error {
	var exists int
	if err := tx.QueryRowContext(ctx, "select exists(select 1 from bp_menu where id = ?)", menuID).Scan(&exists); err != nil {
		return err
	}
	if exists != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) ListRoles(ctx context.Context, params RoleListParams) (RoleListResult, error) {
	where := []string{"1=1"}
	args := []any{}
	if params.ID > 0 {
		where = append(where, "r.id = ?")
		args = append(args, params.ID)
	}
	if strings.TrimSpace(params.Name) != "" {
		where = append(where, "r.role_name like ?")
		args = append(args, "%"+strings.TrimSpace(params.Name)+"%")
	}
	if strings.TrimSpace(params.RoleKey) != "" {
		where = append(where, "r.role_key like ?")
		args = append(args, "%"+strings.TrimSpace(params.RoleKey)+"%")
	}
	if params.Status != nil {
		where = append(where, "r.status = ?")
		args = append(args, *params.Status)
	}
	if params.SelfRegisterEnabled != nil {
		where = append(where, "r.self_register_enabled = ?")
		args = append(args, *params.SelfRegisterEnabled)
	}
	whereSQL := strings.Join(where, " and ")
	params.Page, params.PageSize = pagination.Normalize(params.Page, params.PageSize, 200)
	var (
		total   int64
		roles   []Role
		roleIDs []int64
	)
	countArgs := append([]any(nil), args...)
	queryArgs := append(append([]any(nil), args...), params.PageSize, pagination.Offset(params.Page, params.PageSize))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, `
				select count(*)
				from bp_role r
				where `+whereSQL, countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, `
				select r.id, r.role_key, r.role_name, coalesce(r.remark, ''),
				       r.qps_limit, r.self_register_enabled, r.status,
				       r.request_quota_enabled, r.request_quota_minute, r.request_quota_hour,
				       r.request_quota_day, r.request_quota_month, r.request_quota_count_mode,
				       r.role_key in ('admin', 'user') as builtin,
				       coalesce(member_counts.user_count, 0),
				       coalesce(cfg.default_role_id = r.id, 0),
				       date_format(r.created_at, '%Y-%m-%d %H:%i:%s')
				from bp_role r
				left join (
					select role_id, count(*) as user_count
					from bp_user_role
					group by role_id
				) member_counts on member_counts.role_id = r.id
				left join bp_auth_register_config cfg on cfg.id = 1
				where `+whereSQL+`
				order by r.id
				limit ? offset ?
			`, queryArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			roles = make([]Role, 0, params.PageSize)
			roleIDs = make([]int64, 0, params.PageSize)
			for rows.Next() {
				var role Role
				var id int64
				if err := rows.Scan(
					&id,
					&role.RoleKey,
					&role.Name,
					&role.Remark,
					&role.QPSLimit,
					&role.SelfRegisterEnabled,
					&role.Status,
					&role.QuotaEnabled,
					&role.QuotaMinuteLimit,
					&role.QuotaHourLimit,
					&role.QuotaDayLimit,
					&role.QuotaMonthLimit,
					&role.QuotaCountMode,
					&role.Builtin,
					&role.UserCount,
					&role.DefaultRegisterRole,
					&role.CreateTime,
				); err != nil {
					return err
				}
				role.ID = strconv.FormatInt(id, 10)
				roles = append(roles, role)
				roleIDs = append(roleIDs, id)
			}
			return rows.Err()
		},
	)
	if err != nil {
		return RoleListResult{}, err
	}
	if err := r.fillRolePermissions(ctx, roles, roleIDs); err != nil {
		return RoleListResult{}, err
	}
	return RoleListResult{Items: roles, Total: total}, nil
}

func (r *Repository) RoleOptions(ctx context.Context) ([]RoleOption, error) {
	rows, err := r.db.QueryContext(ctx, `
		select id, role_key, role_name, qps_limit, self_register_enabled, status
		from bp_role
		order by case role_key when 'admin' then 0 when 'user' then 1 else 2 end, id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	options := make([]RoleOption, 0)
	for rows.Next() {
		var option RoleOption
		var id int64
		if err := rows.Scan(
			&id,
			&option.RoleKey,
			&option.Name,
			&option.QPSLimit,
			&option.SelfRegisterEnabled,
			&option.Status,
		); err != nil {
			return nil, err
		}
		option.ID = strconv.FormatInt(id, 10)
		options = append(options, option)
	}
	return options, rows.Err()
}

// RoleQuotaByID returns only the quota columns. UpdateRole uses it to merge
// omitted request fields, keeping older admin clients from resetting limits.
func (r *Repository) RoleQuotaByID(ctx context.Context, roleID int64) (RoleQuota, error) {
	var quota RoleQuota
	err := r.db.QueryRowContext(ctx, `
		select request_quota_enabled, request_quota_minute, request_quota_hour,
		       request_quota_day, request_quota_month, request_quota_count_mode
		from bp_role
		where id = ?
	`, roleID).Scan(
		&quota.Enabled,
		&quota.MinuteLimit,
		&quota.HourLimit,
		&quota.DayLimit,
		&quota.MonthLimit,
		&quota.CountMode,
	)
	return quota, err
}

func (r *Repository) fillRolePermissions(ctx context.Context, roles []Role, roleIDs []int64) error {
	if len(roles) == 0 {
		return nil
	}
	if len(roles) != len(roleIDs) {
		return fmt.Errorf("role permission batch shape mismatch: roles=%d ids=%d", len(roles), len(roleIDs))
	}
	positions := make(map[int64]int, len(roleIDs))
	args := make([]any, len(roleIDs))
	for index, id := range roleIDs {
		positions[id] = index
		args[index] = id
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(roleIDs)), ",")
	rows, err := r.db.QueryContext(ctx, `
		select role_id,menu_id
		from bp_role_menu
		where role_id in (`+placeholders+`)
		order by role_id,menu_id
	`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var roleID, menuID int64
		if err := rows.Scan(&roleID, &menuID); err != nil {
			return err
		}
		if index, ok := positions[roleID]; ok {
			roles[index].Permissions = append(roles[index].Permissions, menuID)
		}
	}
	return rows.Err()
}

func (r *Repository) RolePermissions(ctx context.Context, roleID int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, "select menu_id from bp_role_menu where role_id = ? order by menu_id", roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Repository) CreateRole(ctx context.Context, role Role) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := checkRoleDuplicate(ctx, tx, 0, role.RoleKey); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, `
		insert into bp_role(role_key, role_name, remark, qps_limit, self_register_enabled, status,
			request_quota_enabled, request_quota_minute, request_quota_hour,
			request_quota_day, request_quota_month, request_quota_count_mode)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, role.RoleKey, role.Name, nullIfBlank(role.Remark), role.QPSLimit, role.SelfRegisterEnabled, role.Status,
		role.QuotaEnabled, role.QuotaMinuteLimit, role.QuotaHourLimit,
		role.QuotaDayLimit, role.QuotaMonthLimit, role.QuotaCountMode)
	if err != nil {
		if isDuplicateKey(err) {
			return 0, ErrRoleExists
		}
		return 0, err
	}
	roleID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := replaceRoleMenus(ctx, tx, roleID, role.Permissions); err != nil {
		return 0, err
	}
	return roleID, tx.Commit()
}

func (r *Repository) UpdateRole(ctx context.Context, roleID int64, role Role) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var currentRoleKey string
	if err := tx.QueryRowContext(ctx, `select role_key from bp_role where id=? for update`, roleID).Scan(&currentRoleKey); err != nil {
		return err
	}
	if isBuiltinRoleKey(currentRoleKey) && role.RoleKey != currentRoleKey {
		return ErrProtectedBuiltinRole
	}
	if currentRoleKey == "admin" && (role.Status != 1 || role.SelfRegisterEnabled != 0) {
		return ErrProtectedAdminRole
	}
	defaultRole, err := isDefaultRegisterRole(ctx, tx, roleID)
	if err != nil {
		return err
	}
	if defaultRole && (role.Status != 1 || role.SelfRegisterEnabled != 1) {
		return ErrProtectedDefaultRole
	}
	if err := checkRoleDuplicate(ctx, tx, roleID, role.RoleKey); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
		update bp_role
		set role_key = ?, role_name = ?, remark = ?, qps_limit = ?, self_register_enabled = ?, status = ?,
		    request_quota_enabled = ?, request_quota_minute = ?, request_quota_hour = ?,
		    request_quota_day = ?, request_quota_month = ?, request_quota_count_mode = ?
		where id = ?
	`, role.RoleKey, role.Name, nullIfBlank(role.Remark), role.QPSLimit, role.SelfRegisterEnabled, role.Status,
		role.QuotaEnabled, role.QuotaMinuteLimit, role.QuotaHourLimit,
		role.QuotaDayLimit, role.QuotaMonthLimit, role.QuotaCountMode, roleID)
	if err != nil {
		if isDuplicateKey(err) {
			return ErrRoleExists
		}
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		if err := requireRoleExists(ctx, tx, roleID); err != nil {
			return err
		}
	}
	// The built-in administrator uses an implicit all-permissions policy.
	// Preserve legacy relationship rows as compatibility metadata, but never
	// let an incomplete form submission shrink the effective super-admin role.
	if currentRoleKey != "admin" {
		if err := replaceRoleMenus(ctx, tx, roleID, role.Permissions); err != nil {
			return err
		}
	}
	if defaultRole {
		if _, err := tx.ExecContext(ctx, "update bp_auth_register_config set default_user_qps = ? where id = 1", role.QPSLimit); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repository) UpdateRoleStatus(ctx context.Context, roleID int64, status int) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var roleKey string
	var selfRegisterEnabled int
	if err := tx.QueryRowContext(ctx, `
		select role_key, self_register_enabled
		from bp_role
		where id = ?
		for update
	`, roleID).Scan(&roleKey, &selfRegisterEnabled); err != nil {
		return err
	}
	if roleKey == "admin" && status != 1 {
		return ErrProtectedAdminRole
	}
	defaultRole, err := isDefaultRegisterRole(ctx, tx, roleID)
	if err != nil {
		return err
	}
	if defaultRole && status != 1 {
		return ErrProtectedDefaultRole
	}
	result, err := tx.ExecContext(ctx, "update bp_role set status = ? where id = ?", status, roleID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		if err := requireRoleExists(ctx, tx, roleID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repository) DeleteRole(ctx context.Context, roleID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var roleKey string
	if err := tx.QueryRowContext(ctx, `select role_key from bp_role where id=? for update`, roleID).Scan(&roleKey); err != nil {
		return err
	}
	if isBuiltinRoleKey(roleKey) {
		return ErrProtectedBuiltinRole
	}
	defaultRole, err := isDefaultRegisterRole(ctx, tx, roleID)
	if err != nil {
		return err
	}
	if defaultRole {
		return ErrProtectedDefaultRole
	}
	var userCount int64
	if err := tx.QueryRowContext(ctx, "select count(*) from bp_user_role where role_id = ?", roleID).Scan(&userCount); err != nil {
		return err
	}
	if userCount > 0 {
		return ErrRoleInUse
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_role_menu where role_id = ?", roleID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_user_role where role_id = ?", roleID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "delete from bp_role where id = ?", roleID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return tx.Commit()
}

func checkRoleDuplicate(ctx context.Context, tx *sql.Tx, excludeID int64, roleKey string) error {
	var count int64
	err := tx.QueryRowContext(ctx, "select count(*) from bp_role where role_key = ? and id <> ?", roleKey, excludeID).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return ErrRoleExists
	}
	return nil
}

func isBuiltinRoleKey(roleKey string) bool {
	return roleKey == "admin" || roleKey == "user"
}

func isDefaultRegisterRole(ctx context.Context, tx *sql.Tx, roleID int64) (bool, error) {
	var exists int
	if err := tx.QueryRowContext(ctx, `
		select exists(
			select 1
			from bp_auth_register_config
			where id = 1 and default_role_id = ?
		)
	`, roleID).Scan(&exists); err != nil {
		return false, err
	}
	return exists == 1, nil
}

func replaceRoleMenus(ctx context.Context, tx *sql.Tx, roleID int64, menuIDs []int64) error {
	if _, err := tx.ExecContext(ctx, "delete from bp_role_menu where role_id = ?", roleID); err != nil {
		return err
	}
	seen := map[int64]struct{}{}
	for _, menuID := range menuIDs {
		if _, exists := seen[menuID]; exists {
			continue
		}
		seen[menuID] = struct{}{}
		result, err := tx.ExecContext(ctx, `
			insert into bp_role_menu(role_id, menu_id)
			select ?, id from bp_menu where id = ?
		`, roleID, menuID)
		if err != nil {
			return err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if affected != 1 {
			return ErrMenuNotFound
		}
	}
	return nil
}

func isDuplicateKey(err error) bool {
	var mysqlErr *mysqlDriver.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func requireRoleExists(ctx context.Context, tx *sql.Tx, roleID int64) error {
	var exists int
	if err := tx.QueryRowContext(ctx, "select exists(select 1 from bp_role where id = ?)", roleID).Scan(&exists); err != nil {
		return err
	}
	if exists != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func BuildTree(menus []Menu, includeButtons bool) []Menu {
	byID := map[int64]*Menu{}
	children := map[int64][]int64{}
	seenRoutePaths := map[string]bool{}
	for i := range menus {
		if !includeButtons && menus[i].Type == "button" {
			continue
		}
		if !includeButtons && menus[i].Type != "catalog" {
			path := strings.TrimSpace(menus[i].Path)
			if path != "" {
				if seenRoutePaths[path] {
					continue
				}
				seenRoutePaths[path] = true
			}
		}
		menu := menus[i]
		menu.Children = nil
		byID[menu.ID] = &menu
		children[menu.PID] = append(children[menu.PID], menu.ID)
	}
	for pid := range children {
		sort.SliceStable(children[pid], func(i, j int) bool {
			left := byID[children[pid][i]]
			right := byID[children[pid][j]]
			leftOrder := menuOrder(left)
			rightOrder := menuOrder(right)
			if leftOrder == rightOrder {
				return left.ID < right.ID
			}
			return leftOrder < rightOrder
		})
	}
	var build func(pid int64) []Menu
	build = func(pid int64) []Menu {
		var nodes []Menu
		for _, id := range children[pid] {
			menu := *byID[id]
			menu.Children = build(menu.ID)
			nodes = append(nodes, menu)
		}
		return nodes
	}
	return build(0)
}

func menuOrder(menu *Menu) float64 {
	if menu == nil || menu.Meta == nil {
		return 999999
	}
	if raw, ok := menu.Meta["order"]; ok {
		switch value := raw.(type) {
		case float64:
			return value
		case int:
			return float64(value)
		}
	}
	return 999999
}

func nullIfBlank(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func hasRole(roles []string, role string) bool {
	for _, item := range roles {
		if item == role {
			return true
		}
	}
	return false
}

var ErrUserDisabled = errors.New("user disabled")
var ErrRoleExists = errors.New("role exists")
var ErrRoleNotFound = errors.New("role not found")
var ErrMenuNotFound = errors.New("menu not found")
var ErrMenuHasChildren = errors.New("menu has children")
var ErrMenuNameExists = errors.New("menu name exists")
var ErrMenuPathExists = errors.New("menu path exists")
var ErrProtectedAdminRole = errors.New("protected admin role")
var ErrProtectedBuiltinRole = errors.New("protected builtin role")
var ErrProtectedDefaultRole = errors.New("protected default registration role")
var ErrRoleInUse = errors.New("role is assigned to users")
