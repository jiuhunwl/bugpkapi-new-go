package user

import (
	"bugpk-api/internal/domain/revenue"
	"bugpk-api/internal/pkg/businessclock"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"bugpk-api/internal/pkg/accountstatus"
	"bugpk-api/internal/pkg/calloutcome"
	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/iphash"
	"bugpk-api/internal/pkg/pagination"
)

const (
	userWorkbenchTrendDays  = 366
	adminAccessLocationDays = 30
	adminAccessLocationMax  = 5000
	adminSourceTodayWindow  = "current_date()"
	adminSourceTrendWindow  = "date_sub(current_date(), interval 6 day)"
	adminSourceAccessWindow = "date_format(date_sub(now(), interval 30 day),'%Y-%m-%d %H:00:00')"
	// adminAccessAllWindowDays 是「全部」范围实际读取的天数上界。见
	// adminAccessWindowStart 的 AdminAccessRangeAll 分支。
	adminAccessAllWindowDays = 90
)

type Repository struct {
	db *sql.DB
	// forceStaleRollupCheckpoint 仅供测试注入：强制把 analytics 投影判定为滞后，
	// 以便在没有真实 checkpoint 表的单元测试里覆盖「投影有值但不新鲜」的分支。
	forceStaleRollupCheckpoint bool
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) FindByID(ctx context.Context, userID int64) (UserRecord, error) {
	row := r.db.QueryRowContext(ctx, `
		select id, username, password_hash, password_set, display_name, coalesce(avatar, ''),
		       coalesce(email, ''), coalesce(mobile, ''), home_path,
		       cast(balance as char), points, `+accountstatus.EffectiveStatusSQL("")+`,
		       ban_permanent, date_format(ban_until, '%Y-%m-%d %H:%i:%s'), coalesce(ban_reason, ''),
		       token_version,
		       coalesce(primary_role_id, 0),
		       date_format(last_login_at, '%Y-%m-%d %H:%i:%s'),
		       date_format(created_at, '%Y-%m-%d %H:%i:%s')
		from bp_user
		where id = ?
		limit 1
	`, userID)
	record, err := scanUserRecord(row)
	if err != nil {
		return UserRecord{}, err
	}
	if err := r.fillRoles(ctx, &record); err != nil {
		return UserRecord{}, err
	}
	if err := r.fillSpecifiedFlags(ctx, &record); err != nil {
		return UserRecord{}, err
	}
	return record, nil
}

// AccountSnapshot 读取开放 API 账号聚合快照（单账户只读，无 RBAC）。
func (r *Repository) AccountSnapshot(ctx context.Context, userID int64) (AccountSnapshot, error) {
	var item AccountSnapshot
	var enabled, adminDisabled int
	err := r.db.QueryRowContext(ctx, `
		select id, username, `+accountstatus.EffectiveStatusSQL("")+`, cast(balance as char), points,
		       open_api_enabled, open_api_admin_disabled
		from bp_user
		where id = ?
		limit 1
	`, userID).Scan(&item.UserID, &item.Username, &item.Status, &item.Balance, &item.Points,
		&enabled, &adminDisabled)
	if err != nil {
		return AccountSnapshot{}, err
	}
	item.OpenAPIEnabled = enabled == 1
	item.OpenAPIAdminDisabled = adminDisabled == 1
	record := UserRecord{ID: item.UserID}
	if err := r.fillRoles(ctx, &record); err != nil {
		return AccountSnapshot{}, err
	}
	item.Roles = record.Roles
	return item, nil
}

func (r *Repository) PasswordSet(ctx context.Context, userID int64) (bool, error) {
	var value int
	if err := r.db.QueryRowContext(ctx, `select coalesce(password_set, 1) from bp_user where id = ?`, userID).Scan(&value); err != nil {
		return false, err
	}
	return value == 1, nil
}

func (r *Repository) SetInitialPassword(ctx context.Context, userID int64, passwordHash string) error {
	result, err := r.db.ExecContext(ctx, `
		update bp_user
		set password_hash = ?, password_set = 1
		where id = ? and password_set = 0
	`, passwordHash, userID)
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
	return nil
}

func (r *Repository) UpdateProfile(ctx context.Context, userID int64, realName, avatar string) error {
	result, err := r.db.ExecContext(ctx, `
		update bp_user
		set display_name = ?, avatar = ?
		where id = ?
	`, realName, nullIfBlank(avatar), userID)
	if err != nil {
		return err
	}
	return requireExistingUser(ctx, r.db, userID, result)
}

const updateOwnPasswordSQL = `
	update bp_user
	set password_hash = ?, token_version = token_version + 1
	where id = ?
`

func (r *Repository) UpdateOwnPassword(ctx context.Context, userID int64, passwordHash string) error {
	result, err := r.db.ExecContext(ctx, updateOwnPasswordSQL, passwordHash, userID)
	if err != nil {
		return err
	}
	return requireExistingUser(ctx, r.db, userID, result)
}

func (r *Repository) BindEmail(ctx context.Context, userID int64, email string) error {
	result, err := r.db.ExecContext(ctx, `
		update bp_user
		set email = ?, email_verified = 1
		where id = ?
	`, nullIfBlank(email), userID)
	if err != nil {
		return err
	}
	return requireExistingUser(ctx, r.db, userID, result)
}

func (r *Repository) BindMobile(ctx context.Context, userID int64, mobile string) error {
	result, err := r.db.ExecContext(ctx, `
		update bp_user
		set mobile = ?, mobile_verified = 1
		where id = ?
	`, nullIfBlank(mobile), userID)
	if err != nil {
		return err
	}
	return requireExistingUser(ctx, r.db, userID, result)
}

func (r *Repository) EmailExists(ctx context.Context, email string, excludeUserID int64) (bool, error) {
	if strings.TrimSpace(email) == "" {
		return false, nil
	}
	var count int64
	err := r.db.QueryRowContext(ctx, `
		select count(*)
		from bp_user
		where email = ? and id <> ?
	`, strings.TrimSpace(email), excludeUserID).Scan(&count)
	return count > 0, err
}

func (r *Repository) MobileExists(ctx context.Context, mobile string, excludeUserID int64) (bool, error) {
	if strings.TrimSpace(mobile) == "" {
		return false, nil
	}
	var count int64
	err := r.db.QueryRowContext(ctx, `
		select count(*)
		from bp_user
		where mobile = ? and id <> ?
	`, strings.TrimSpace(mobile), excludeUserID).Scan(&count)
	return count > 0, err
}

func (r *Repository) ListUsers(ctx context.Context, params UserListParams) (UserListResult, error) {
	params.Page, params.PageSize = pagination.Normalize(params.Page, params.PageSize, 200)
	where, args := buildUserWhere(params)
	var (
		total   int64
		records []UserRecord
	)
	countArgs := append(append([]any(nil), args...), params.PageSize*pagination.MaxPage)
	offset := pagination.Offset(params.Page, params.PageSize)
	queryArgs := append(append([]any{}, args...), params.PageSize, offset)
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, userCountQuery(where), countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, userListQuery(where), queryArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			records = make([]UserRecord, 0, params.PageSize)
			for rows.Next() {
				record, err := scanUserRecord(rows)
				if err != nil {
					return err
				}
				records = append(records, record)
			}
			return rows.Err()
		},
	)
	if err != nil {
		return UserListResult{}, err
	}
	if err := r.fillListRelations(ctx, records); err != nil {
		return UserListResult{}, err
	}
	items := make([]SystemUser, 0, len(records))
	for _, record := range records {
		items = append(items, toSystemUser(record))
	}
	return UserListResult{Items: items, Total: total}, nil
}

func userCountQuery(where string) string {
	return `select count(*)
		from (
			select u.id
			from bp_user u
			` + where + `
			limit ?
		) counted`
}

func userListQuery(where string) string {
	return `
		select u.id, u.username, u.password_hash, u.password_set, u.display_name, coalesce(u.avatar, ''),
		       coalesce(u.email, ''), coalesce(u.mobile, ''), u.home_path,
		       cast(u.balance as char), u.points, ` + accountstatus.EffectiveStatusSQL("u") + `,
		       u.ban_permanent, date_format(u.ban_until, '%Y-%m-%d %H:%i:%s'), coalesce(u.ban_reason, ''),
		       u.token_version,
		       coalesce(u.primary_role_id, 0),
		       date_format(u.last_login_at, '%Y-%m-%d %H:%i:%s'),
		       date_format(u.created_at, '%Y-%m-%d %H:%i:%s')
		from (
			select u.id
			from bp_user u
			` + where + `
			order by u.id desc
			limit ? offset ?
		) filtered
		inner join bp_user u on u.id=filtered.id
		order by u.id desc
	`
}

func (r *Repository) fillListRelations(ctx context.Context, records []UserRecord) error {
	if len(records) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(records))
	positions := make(map[int64]int, len(records))
	for index := range records {
		ids = append(ids, records[index].ID)
		positions[records[index].ID] = index
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for index, id := range ids {
		args[index] = id
	}

	roleRows, err := r.db.QueryContext(ctx, `
		select ur.user_id,r.id,r.role_key,r.role_name
		from bp_user_role ur
		inner join bp_role r on r.id=ur.role_id
		where ur.user_id in (`+placeholders+`) and r.status=1
		order by ur.user_id,r.id
	`, args...)
	if err != nil {
		return err
	}
	for roleRows.Next() {
		var userID, roleID int64
		var roleKey, roleName string
		if err := roleRows.Scan(&userID, &roleID, &roleKey, &roleName); err != nil {
			_ = roleRows.Close()
			return err
		}
		if index, ok := positions[userID]; ok {
			records[index].RoleIDs = append(records[index].RoleIDs, roleID)
			records[index].Roles = append(records[index].Roles, roleKey)
			records[index].RoleNames = append(records[index].RoleNames, roleName)
		}
	}
	if err := roleRows.Err(); err != nil {
		_ = roleRows.Close()
		return err
	}
	if err := roleRows.Close(); err != nil {
		return err
	}

	responseRows, err := r.db.QueryContext(ctx, `
		select user_id,coalesce(enabled,0),coalesce(billable,0)
		from bp_user_specified_response
		where user_id in (`+placeholders+`)
	`, args...)
	if err != nil {
		return err
	}
	for responseRows.Next() {
		var userID int64
		var enabled, billable int
		if err := responseRows.Scan(&userID, &enabled, &billable); err != nil {
			return err
		}
		if index, ok := positions[userID]; ok {
			records[index].SpecifiedResponseEnabled = enabled
			records[index].SpecifiedResponseBillable = billable
		}
	}
	if err := responseRows.Err(); err != nil {
		_ = responseRows.Close()
		return err
	}
	if err := responseRows.Close(); err != nil {
		return err
	}

	requestRows, err := r.db.QueryContext(ctx, `
		select user_id, coalesce(sum(call_count),0)
		from bp_call_user_metric_minute force index(idx_bp_call_user_metric_user_time)
		where user_id in (`+placeholders+`)
		group by user_id
	`, args...)
	if err != nil {
		return err
	}
	for requestRows.Next() {
		var userID int64
		var count int64
		if err := requestRows.Scan(&userID, &count); err != nil {
			_ = requestRows.Close()
			return err
		}
		if index, ok := positions[userID]; ok {
			records[index].TotalRequests = count
		}
	}
	if err := requestRows.Err(); err != nil {
		_ = requestRows.Close()
		return err
	}
	return requestRows.Close()
}

func (r *Repository) UserTotalRequests(ctx context.Context, userID int64) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, userTotalRequestsQuery(), userID).Scan(&count)
	return count, err
}

func userTotalRequestsQuery() string {
	return `
		select coalesce(sum(call_count),0)
		from bp_call_user_metric_minute force index(idx_bp_call_user_metric_user_time)
		where user_id = ?
	`
}

func buildUserWhere(params UserListParams) (string, []any) {
	var clauses []string
	var args []any
	if keyword := strings.TrimSpace(params.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		clauses = append(clauses, "(u.username like ? or u.display_name like ? or u.email like ? or u.mobile like ?)")
		args = append(args, like, like, like, like)
	}
	if params.Status != nil {
		clauses = append(clauses, accountstatus.EffectiveStatusSQL("u")+" = ?")
		args = append(args, *params.Status)
	}
	if params.RoleID > 0 {
		clauses = append(clauses, "exists (select 1 from bp_user_role ur where ur.user_id = u.id and ur.role_id = ?)")
		args = append(args, params.RoleID)
	}
	if len(clauses) == 0 {
		return "where 1=1", args
	}
	return "where " + strings.Join(clauses, " and "), args
}

func (r *Repository) CreateUser(ctx context.Context, operatorID int64, req SaveUserRequest, passwordHash string, status int, points int64) (int64, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if err := checkDuplicateUser(ctx, tx, 0, req.Username, req.Email, req.Mobile); err != nil {
		return 0, err
	}
	banPermanent, banUntil, banReason := userBanValues(req, status)
	result, err := tx.ExecContext(ctx, `
		insert into bp_user(primary_role_id, username, password_hash, display_name, avatar, email, mobile, home_path, balance, points, status, ban_permanent, ban_until, ban_reason)
		values (?, ?, ?, ?, null, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, req.PrimaryRoleID, req.Username, passwordHash, req.RealName, nullIfBlank(req.Email), nullIfBlank(req.Mobile), defaultHomePath(req.HomePath), req.Balance, points, status, banPermanent, banUntil, banReason)
	if err != nil {
		return 0, err
	}
	userID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	if err := replaceUserRoles(ctx, tx, userID, req.RoleIDs, req.PrimaryRoleID); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into bp_wallet_transaction(user_id,biz_type,biz_id,amount,points,balance_after,points_after,remark)
		select id,'ADMIN_USER_INITIAL_ASSET',?,balance,points,balance,points,'管理员创建用户时设置初始资产'
		from bp_user
		where id = ? and (balance <> 0 or points <> 0)
	`, nullPositiveID(operatorID), userID); err != nil {
		return 0, err
	}
	return userID, tx.Commit()
}

func (r *Repository) UpdateUser(ctx context.Context, operatorID, userID int64, req SaveUserRequest, passwordHash *string, status int) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	if err := checkDuplicateUser(ctx, tx, userID, req.Username, req.Email, req.Mobile); err != nil {
		return err
	}
	if err := guardLastActiveAdmin(ctx, tx, userID, status, req.RoleIDs, false); err != nil {
		return err
	}
	var previousBalance string
	var previousPoints int64
	var previousStatus, previousBanPermanent int
	var previousBanUntil, previousBanReason string
	if err := tx.QueryRowContext(ctx, `
		select cast(balance as char),points,status,ban_permanent,
		       coalesce(date_format(ban_until, '%Y-%m-%d %H:%i:%s'),''),coalesce(ban_reason,'')
		from bp_user
		where id = ?
		for update
	`, userID).Scan(&previousBalance, &previousPoints, &previousStatus, &previousBanPermanent, &previousBanUntil, &previousBanReason); err != nil {
		return err
	}
	banPermanent, banUntil, banReason := userBanValues(req, status)
	banUntilText := strings.TrimSpace(req.BanUntil)
	if banPermanent == 1 || status == 1 {
		banUntilText = ""
	}
	tokenVersionDelta := 0
	if passwordHash != nil || previousStatus != status || previousBanPermanent != banPermanent || previousBanUntil != banUntilText || previousBanReason != banReason {
		tokenVersionDelta = 1
	}
	// NULL means omitted; COALESCE uses the current locked values. Explicit
	// zero still updates the asset and is recorded by the existing ledger SQL.
	var updateResult sql.Result
	if passwordHash == nil {
		updateResult, err = tx.ExecContext(ctx, `
			update bp_user
			set primary_role_id = ?, username = ?, display_name = ?, email = ?, mobile = ?, home_path = ?, balance = coalesce(?,balance), points = coalesce(?,points), status = ?,
			    ban_permanent = ?, ban_until = ?, ban_reason = ?, token_version = token_version + ?
			where id = ?
		`, req.PrimaryRoleID, req.Username, req.RealName, nullIfBlank(req.Email), nullIfBlank(req.Mobile), defaultHomePath(req.HomePath), req.Balance, req.Points, status, banPermanent, banUntil, banReason, tokenVersionDelta, userID)
	} else {
		updateResult, err = tx.ExecContext(ctx, `
			update bp_user
			set primary_role_id = ?, username = ?, password_hash = ?, display_name = ?, email = ?, mobile = ?, home_path = ?, balance = coalesce(?,balance), points = coalesce(?,points), status = ?,
			    ban_permanent = ?, ban_until = ?, ban_reason = ?, token_version = token_version + ?
			where id = ?
		`, req.PrimaryRoleID, req.Username, *passwordHash, req.RealName, nullIfBlank(req.Email), nullIfBlank(req.Mobile), defaultHomePath(req.HomePath), req.Balance, req.Points, status, banPermanent, banUntil, banReason, tokenVersionDelta, userID)
	}
	if err != nil {
		return err
	}
	if err := requireExistingUser(ctx, tx, userID, updateResult); err != nil {
		return err
	}
	// 全量封禁：账号状态的每一次翻转都要同步到调用凭证。
	// status=0 时按封禁类型冻结（临时=1 可恢复 / 永久=2 作废）；
	// status=1 时只恢复因临时封禁暂停的 Key，不碰用户自己禁用的和永久封禁作废的。
	// 条件与 token_version 的递增条件一致：只有账号/封禁字段真的变了才动 Key。
	if tokenVersionDelta > 0 {
		if status == accountstatus.Banned {
			if _, err := tx.ExecContext(ctx, accountstatus.SuspendKeysSQL(banPermanent == 1), userID); err != nil {
				return err
			}
		} else {
			if _, err := tx.ExecContext(ctx, accountstatus.RestoreSuspendedKeysSQL, userID); err != nil {
				return err
			}
		}
	}
	if req.RoleIDs != nil {
		if err := replaceUserRoles(ctx, tx, userID, req.RoleIDs, req.PrimaryRoleID); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		insert into bp_wallet_transaction(user_id,biz_type,biz_id,amount,points,balance_after,points_after,remark)
		select id,'ADMIN_USER_ASSET_ADJUST',?,balance-cast(? as decimal(18,4)),points-?,balance,points,'管理员调整用户余额或点数'
		from bp_user
		where id = ? and (balance <> cast(? as decimal(18,4)) or points <> ?)
	`, nullPositiveID(operatorID), previousBalance, previousPoints, userID, previousBalance, previousPoints); err != nil {
		return err
	}
	return tx.Commit()
}

func nullPositiveID(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

// AdjustUserAssets applies a signed delta under a row lock. Reading and writing
// the same locked row keeps a concurrent recharge settlement (balance=balance+?)
// serialized: whichever transaction commits second builds on the first one's
// value, so an administrator adjustment can never erase a simultaneous credit.
func (r *Repository) AdjustUserAssets(ctx context.Context, operatorID, userID int64, balanceDelta string, pointsDelta int64, reason string) (UserAssetAdjustment, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return UserAssetAdjustment{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	var currentBalance string
	var currentPoints int64
	if err := tx.QueryRowContext(ctx, `
		select cast(balance as char),points
		from bp_user
		where id = ?
		for update
	`, userID).Scan(&currentBalance, &currentPoints); err != nil {
		return UserAssetAdjustment{}, err
	}
	nextBalance, nextPoints, err := applyAssetDelta(currentBalance, currentPoints, balanceDelta, pointsDelta)
	if err != nil {
		return UserAssetAdjustment{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		update bp_user
		set balance = cast(? as decimal(18,4)), points = ?
		where id = ?
	`, nextBalance, nextPoints, userID); err != nil {
		return UserAssetAdjustment{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		insert into bp_wallet_transaction(user_id,biz_type,biz_id,amount,points,balance_after,points_after,remark)
		values(?,?,?,cast(? as decimal(18,4)),?,cast(? as decimal(18,4)),?,?)
	`, userID, "ADMIN_BALANCE_ADJUST", nullPositiveID(operatorID), balanceDelta, pointsDelta, nextBalance, nextPoints, reason); err != nil {
		return UserAssetAdjustment{}, err
	}
	if err := tx.Commit(); err != nil {
		return UserAssetAdjustment{}, err
	}
	return UserAssetAdjustment{
		Balance:      nextBalance,
		Points:       nextPoints,
		BalanceDelta: balanceDelta,
		PointsDelta:  pointsDelta,
	}, nil
}

func applyAssetDelta(currentBalance string, currentPoints int64, balanceDelta string, pointsDelta int64) (string, int64, error) {
	current, ok := new(big.Rat).SetString(strings.TrimSpace(currentBalance))
	if !ok {
		return "", 0, ErrInvalidAssetState
	}
	delta, ok := new(big.Rat).SetString(strings.TrimSpace(balanceDelta))
	if !ok {
		return "", 0, ErrInvalidAssetState
	}
	next := new(big.Rat).Add(current, delta)
	if next.Sign() < 0 {
		return "", 0, ErrInsufficientBalance
	}
	maxBalance := new(big.Rat).SetFrac(big.NewInt(maxUserBalance), big.NewInt(10_000))
	if next.Cmp(maxBalance) > 0 {
		return "", 0, ErrAssetCapacityExceeded
	}
	nextPoints := currentPoints + pointsDelta
	if nextPoints < 0 {
		return "", 0, ErrInsufficientPoints
	}
	if nextPoints > maxUserPoints {
		return "", 0, ErrAssetCapacityExceeded
	}
	return next.FloatString(4), nextPoints, nil
}

func checkDuplicateUser(ctx context.Context, tx *sql.Tx, excludeID int64, username, email, mobile string) error {
	var count int64
	err := tx.QueryRowContext(ctx, `
		select count(*)
		from bp_user
		where id <> ?
		  and (
		    username = ?
		    or (? <> '' and email = ?)
		    or (? <> '' and mobile = ?)
		  )
	`, excludeID, strings.TrimSpace(username), strings.TrimSpace(email), strings.TrimSpace(email), strings.TrimSpace(mobile), strings.TrimSpace(mobile)).Scan(&count)
	if err != nil {
		return err
	}
	if count > 0 {
		return ErrUserExists
	}
	return nil
}

func replaceUserRoles(ctx context.Context, tx *sql.Tx, userID int64, roleIDs []int64, primaryRoleID int64) error {
	if primaryRoleID <= 0 || !containsID(roleIDs, primaryRoleID) {
		return ErrPrimaryRoleInvalid
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_user_role where user_id = ?", userID); err != nil {
		return err
	}
	for _, roleID := range roleIDs {
		result, err := tx.ExecContext(ctx, `
			insert into bp_user_role(user_id, role_id)
			select ?, id from bp_role where id = ? and status = 1
		`, userID, roleID)
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

func guardLastActiveAdmin(ctx context.Context, tx *sql.Tx, userID int64, nextStatus int, nextRoleIDs []int64, deleting bool) error {
	var adminRoleID int64
	if err := tx.QueryRowContext(ctx, `select id from bp_role where role_key='admin' limit 1 for update`).Scan(&adminRoleID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrRoleNotFound
		}
		return err
	}
	var activeAdminID int64
	err := tx.QueryRowContext(ctx, `
		select u.id
		from bp_user u
		inner join bp_user_role ur on ur.user_id=u.id
		inner join bp_role r on r.id=ur.role_id
		where u.id=? and `+accountstatus.EffectiveStatusSQL("u")+`=1 and r.id=? and r.status=1
		limit 1
		for update
	`, userID, adminRoleID).Scan(&activeAdminID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	willRemainAdmin := !deleting && nextStatus == 1 && containsID(nextRoleIDs, adminRoleID)
	if willRemainAdmin {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `
		select distinct u.id
		from bp_user u
		inner join bp_user_role ur on ur.user_id=u.id
		inner join bp_role r on r.id=ur.role_id
		where `+accountstatus.EffectiveStatusSQL("u")+`=1 and r.status=1 and r.id=?
		for update
	`, adminRoleID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var activeAdmins int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		activeAdmins++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if activeAdmins <= 1 {
		return ErrLastAdmin
	}
	return nil
}

func containsID(values []int64, target int64) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (r *Repository) DeleteUser(ctx context.Context, userID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		_ = tx.Rollback()
	}()
	var email, mobile string
	var hasFunds bool
	if err := tx.QueryRowContext(ctx, `
		select coalesce(email,''),coalesce(mobile,''),(balance > 0 or points > 0)
		from bp_user
		where id = ?
		for update
	`, userID).Scan(&email, &mobile, &hasFunds); err != nil {
		return err
	}
	if err := guardLastActiveAdmin(ctx, tx, userID, 0, nil, true); err != nil {
		return err
	}
	if hasFunds {
		return ErrUserHasFunds
	}
	var pendingPayments bool
	if err := tx.QueryRowContext(ctx, `
		select exists(
			select 1 from bp_payment_order
			where user_id = ? and status in ('CREATED','PAYING')
		)
	`, userID).Scan(&pendingPayments); err != nil {
		return err
	}
	if pendingPayments {
		return ErrUserHasPendingPayments
	}
	var hasBusinessHistory bool
	if err := tx.QueryRowContext(ctx, `
		select
			exists(select 1 from bp_payment_order where user_id = ?)
			or exists(select 1 from bp_wallet_transaction where user_id = ?)
			or exists(select 1 from bp_billing_record where user_id = ?)
			or exists(select 1 from bp_interface_call_log where user_id = ?)
			or exists(select 1 from bp_user_package_global where user_id = ?)
			or exists(select 1 from bp_user_package_interface where user_id = ?)
			or exists(select 1 from bp_friend_link_application where user_id = ? or reviewer_id = ?)
			or exists(select 1 from bp_notice where creator_id = ? or updater_id = ?)
	`, userID, userID, userID, userID, userID, userID, userID, userID, userID, userID).Scan(&hasBusinessHistory); err != nil {
		return err
	}
	if hasBusinessHistory {
		return ErrUserHasBusinessHistory
	}
	if _, err := tx.ExecContext(ctx, `
		delete w
		from bp_api_key_ip_whitelist w
		inner join bp_api_key k on k.id = w.key_id
		where k.user_id = ?
	`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		delete b
		from bp_api_key_interface b
		inner join bp_api_key k on k.id = b.key_id
		where k.user_id = ?
	`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_api_key where user_id = ?", userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_user_log_cleanup_config where user_id = ?", userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_auth_refresh_token_audit where user_id = ?", userID); err != nil {
		return err
	}
	if email != "" {
		if _, err := tx.ExecContext(ctx, "delete from bp_auth_email_code where email = ?", email); err != nil {
			return err
		}
	}
	if mobile != "" {
		if _, err := tx.ExecContext(ctx, "delete from bp_auth_mobile_code where mobile = ?", mobile); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_user_specified_response where user_id = ?", userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_auth_oauth_identity where user_id = ?", userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_user_role where user_id = ?", userID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "delete from bp_user where id = ?", userID)
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

func (r *Repository) CountActiveAdmins(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, `
		select count(distinct u.id)
		from bp_user u
		inner join bp_user_role ur on ur.user_id = u.id
		inner join bp_role r on r.id = ur.role_id
		where `+accountstatus.EffectiveStatusSQL("u")+` = 1 and r.status = 1 and r.role_key = 'admin'
	`).Scan(&count)
	return count, err
}

func (r *Repository) ListRoles(ctx context.Context) ([]RoleOption, error) {
	rows, err := r.db.QueryContext(ctx, `
		select id, role_key, role_name, qps_limit, self_register_enabled, status
		from bp_role
		order by id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var roles []RoleOption
	for rows.Next() {
		var id int64
		var key, name string
		var status int
		var qpsLimit, selfRegisterEnabled int
		if err := rows.Scan(&id, &key, &name, &qpsLimit, &selfRegisterEnabled, &status); err != nil {
			return nil, err
		}
		roles = append(roles, RoleOption{
			ID:                  fmt.Sprintf("%d", id),
			Name:                name,
			Permissions:         []string{},
			QPSLimit:            qpsLimit,
			RoleKey:             key,
			SelfRegisterEnabled: selfRegisterEnabled,
			Status:              status,
		})
	}
	return roles, rows.Err()
}

func (r *Repository) GetSpecifiedResponse(ctx context.Context, userID int64) (SpecifiedResponseConfig, error) {
	var cfg SpecifiedResponseConfig
	err := r.db.QueryRowContext(ctx, `
		select enabled, billable, coalesce(body, '')
		from bp_user_specified_response
		where user_id = ?
	`, userID).Scan(&cfg.SpecifiedResponseEnabled, &cfg.SpecifiedResponseBillable, &cfg.SpecifiedResponseBody)
	if err == sql.ErrNoRows {
		return SpecifiedResponseConfig{}, nil
	}
	return cfg, err
}

func (r *Repository) UpdateSpecifiedResponse(ctx context.Context, userID int64, cfg SpecifiedResponseConfig) error {
	_, err := r.db.ExecContext(ctx, `
		insert into bp_user_specified_response(user_id, enabled, billable, body)
		values (?, ?, ?, ?)
		on duplicate key update enabled = values(enabled), billable = values(billable), body = values(body)
	`, userID, cfg.SpecifiedResponseEnabled, cfg.SpecifiedResponseBillable, cfg.SpecifiedResponseBody)
	return err
}

func (r *Repository) UserCount(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, "select count(*) from bp_user").Scan(&count)
	return count, err
}

func (r *Repository) TodayNewUserCount(ctx context.Context) (int64, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, "select count(*) from bp_user where created_at >= current_date()").Scan(&count)
	return count, err
}

// workbenchOpenTrafficCondition remains the canonical raw-log predicate for
// the few audit/lifecycle paths that still inspect source rows. Dashboard
// queries below consume rollups whose writer has already excluded ADMIN rows.
func workbenchOpenTrafficCondition(alias string) string {
	return accountstatus.ActiveTrafficSQL(alias)
}

func (r *Repository) UserCallSummary(ctx context.Context, userID int64) (UserCallSummary, error) {
	var summary UserCallSummary
	err := r.db.QueryRowContext(ctx, userCallSummaryQuery(), userID).Scan(
		&summary.TotalCalls,
		&summary.SuccessCalls,
		&summary.FailedCalls,
		&summary.TodayCalls,
		&summary.TodaySuccessCalls,
	)
	return summary, err
}

func userCallSummaryQuery() string {
	return `
		select coalesce(sum(call_count),0),
		       coalesce(sum(success_count),0),
		       coalesce(sum(failure_count),0),
		       coalesce(sum(if(bucket_start>=current_date(),call_count,0)),0),
		       coalesce(sum(if(bucket_start>=current_date(),success_count,0)),0)
		from bp_call_user_metric_minute force index(idx_bp_call_user_metric_user_time)
		where user_id=?
		  and exists (select 1 from bp_user u where u.id=bp_call_user_metric_minute.user_id
		      and ` + accountstatus.EffectiveStatusSQL("u") + `=1)
	`
}

func (r *Repository) UserDailyCallCounts(ctx context.Context, userID int64) (map[string]int64, error) {
	rows, err := r.db.QueryContext(ctx, userDailyCallCountsQuery(), userID, userWorkbenchTrendDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]int64)
	for rows.Next() {
		var day string
		var count int64
		if err := rows.Scan(&day, &count); err != nil {
			return nil, err
		}
		result[day] = count
	}
	return result, rows.Err()
}

func userDailyCallCountsQuery() string {
	return `
		select date_format(bucket_start,'%Y-%m-%d'),sum(call_count)
		from bp_call_user_metric_minute force index(idx_bp_call_user_metric_user_time)
		where user_id=?
		  and exists (select 1 from bp_user u where u.id=bp_call_user_metric_minute.user_id
		      and ` + accountstatus.EffectiveStatusSQL("u") + `=1)
		  and bucket_start>=date_sub(current_date(), interval ? day)
		group by date_format(bucket_start,'%Y-%m-%d')
	`
}

func (r *Repository) PrimaryRoleQPS(ctx context.Context, userID int64) (int, error) {
	var limit int
	err := r.db.QueryRowContext(ctx, `
		select coalesce(primary_role.qps_limit, default_role.qps_limit, 1)
		from bp_user u
		left join bp_role primary_role on primary_role.id = u.primary_role_id
		left join bp_auth_register_config cfg on cfg.id = 1
		left join bp_role default_role on default_role.id = cfg.default_role_id
		where u.id = ?
		limit 1
	`, userID).Scan(&limit)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, err
	}
	return normalizeRoleQPS(limit), nil
}

// PrimaryRoleQuota returns the same quota snapshot the gateway applies to the
// user's primary/default role. The console must show this ceiling even when the
// user has no package; otherwise package-only summary code reports unlimited.
func (r *Repository) PrimaryRoleQuota(ctx context.Context, userID int64) (UserRoleQuota, error) {
	var quota UserRoleQuota
	err := r.db.QueryRowContext(ctx, `
		select coalesce(primary_role.request_quota_enabled, default_role.request_quota_enabled, 0),
		       coalesce(primary_role.request_quota_minute, default_role.request_quota_minute, 0),
		       coalesce(primary_role.request_quota_hour, default_role.request_quota_hour, 0),
		       coalesce(primary_role.request_quota_day, default_role.request_quota_day, 0),
		       coalesce(primary_role.request_quota_month, default_role.request_quota_month, 0),
		       coalesce(primary_role.request_quota_count_mode, default_role.request_quota_count_mode, 'attempt'),
		       coalesce(primary_role.role_key, default_role.role_key, ''),
		       coalesce(primary_role.role_name, default_role.role_name, ''),
		       coalesce(primary_role.id, default_role.id, 0)
		from bp_user u
		left join bp_role primary_role on primary_role.id = u.primary_role_id
		left join bp_auth_register_config cfg on cfg.id = 1
		left join bp_role default_role on default_role.id = cfg.default_role_id
		where u.id = ?
		limit 1
	`, userID).Scan(
		&quota.Enabled,
		&quota.Minute,
		&quota.Hour,
		&quota.Day,
		&quota.Month,
		&quota.CountMode,
		&quota.RoleKey,
		&quota.RoleName,
		&quota.RoleID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return UserRoleQuota{}, nil
	}
	if err != nil {
		return UserRoleQuota{}, err
	}
	if quota.CountMode != "success" {
		quota.CountMode = "attempt"
	}
	return quota, nil
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

// 控制台的「当前套餐」展示与网关扣减顺序保持一致：主套餐优先，其余按到期最早。
// 两边排序一旦分叉，页面显示的套餐就会和实际被扣的对不上。
const workbenchMemberPackageQuery = `
	select id,package_id,package_name,daily_limit,total_limit,total_used,count_mode,qps_limit,
	       date_format(start_at,'%Y-%m-%d %H:%i:%s'),date_format(end_at,'%Y-%m-%d %H:%i:%s'),
	       if(day_key=curdate(),day_used,0)
	from bp_user_package_global
	where user_id=? and status=1 and start_at<=now() and end_at>now()
	order by is_primary desc, end_at asc, id asc
	limit 1
`

const workbenchInterfacePackagesQuery = `
	select p.id,p.interface_id,p.package_id,p.spec_id,coalesce(a.api_code,''),coalesce(a.name,''),
	       p.package_name,p.spec_name,p.daily_limit,p.total_limit,p.total_used,p.count_mode,p.qps_limit,
	       date_format(p.start_at,'%Y-%m-%d %H:%i:%s'),date_format(p.end_at,'%Y-%m-%d %H:%i:%s'),
	       if(p.day_key=curdate(),p.day_used,0)
	from bp_user_package_interface p
	left join bp_interface_api a on a.id=p.interface_id
	where p.user_id=? and p.status=1 and p.start_at<=now() and p.end_at>now()
	order by p.is_primary desc, p.end_at asc, p.id asc
`

func (r *Repository) UserPackagesForWorkbench(ctx context.Context, userID int64) (*UserWorkbenchMemberPackage, []UserWorkbenchInterfacePackage, error) {
	var member *UserWorkbenchMemberPackage
	row := r.db.QueryRowContext(ctx, workbenchMemberPackageQuery, userID)
	var memberRecord UserWorkbenchMemberPackage
	var memberID, memberPackageID int64
	if err := row.Scan(
		&memberID,
		&memberPackageID,
		&memberRecord.PackageName,
		&memberRecord.DailyLimit,
		&memberRecord.TotalLimit,
		&memberRecord.TotalUsed,
		&memberRecord.CountMode,
		&memberRecord.QPSLimit,
		&memberRecord.StartTime,
		&memberRecord.ExpireTime,
		&memberRecord.TodayCalls,
	); err == nil {
		memberRecord.UserPackageID = fmt.Sprintf("%d", memberID)
		memberRecord.PackageID = fmt.Sprintf("%d", memberPackageID)
		if memberRecord.CountMode == "" {
			memberRecord.CountMode = "success"
		}
		memberRecord.RemainingCalls, memberRecord.RemainingUnlimited = remainingPackageCalls(
			memberRecord.DailyLimit, memberRecord.TodayCalls, memberRecord.TotalLimit, memberRecord.TotalUsed)
		member = &memberRecord
	} else if err != sql.ErrNoRows {
		return nil, nil, err
	}
	rows, err := r.db.QueryContext(ctx, workbenchInterfacePackagesQuery, userID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	interfaces := make([]UserWorkbenchInterfacePackage, 0)
	for rows.Next() {
		var item UserWorkbenchInterfacePackage
		var rowID, interfaceID, packageID, specID int64
		if err := rows.Scan(
			&rowID,
			&interfaceID,
			&packageID,
			&specID,
			&item.APICode,
			&item.InterfaceName,
			&item.PackageName,
			&item.SpecName,
			&item.DailyLimit,
			&item.TotalLimit,
			&item.TotalUsed,
			&item.CountMode,
			&item.QPSLimit,
			&item.StartTime,
			&item.ExpireTime,
			&item.TodayCalls,
		); err != nil {
			return nil, nil, err
		}
		item.UserPackageID = fmt.Sprintf("%d", rowID)
		item.InterfaceID = fmt.Sprintf("%d", interfaceID)
		item.PackageID = fmt.Sprintf("%d", packageID)
		item.SpecID = fmt.Sprintf("%d", specID)
		if item.CountMode == "" {
			item.CountMode = "success"
		}
		item.RemainingCalls, item.RemainingUnlimited = remainingPackageCalls(
			item.DailyLimit, item.TodayCalls, item.TotalLimit, item.TotalUsed)
		interfaces = append(interfaces, item)
	}
	return member, interfaces, rows.Err()
}

func remainingDailyCalls(limit, used int64) (int64, bool) {
	if limit <= 0 {
		return 0, true
	}
	if used >= limit {
		return 0, false
	}
	return limit - used, false
}

// remainingPackageCalls 按「日限与总量中更紧的那个」计算剩余，与网关
// 「任一上限打满即不可用」的裁决一致；两侧都不限时返回 unlimited。
// 复用 remainingDailyCalls 保证单侧语义（-1 剩余渲染、0 = 用尽）不变。
func remainingPackageCalls(daily, dayUsed, total, totalUsed int64) (int64, bool) {
	dailyRemaining, dailyUnlimited := remainingDailyCalls(daily, dayUsed)
	totalRemaining, totalUnlimited := remainingDailyCalls(total, totalUsed)
	switch {
	case dailyUnlimited && totalUnlimited:
		return 0, true
	case dailyUnlimited:
		return totalRemaining, false
	case totalUnlimited:
		return dailyRemaining, false
	case dailyRemaining < totalRemaining:
		return dailyRemaining, false
	default:
		return totalRemaining, false
	}
}

func (r *Repository) AdminOverview(ctx context.Context) (AdminWorkbenchOverview, error) {
	item, _, err := r.adminOverview(ctx, false, nil)
	return item, err
}

// adminOverview isolates the six independent metric groups. A blocked or failed
// projection must not discard users, keys, orders or independently loaded revenue.
// The bool reports whether any group was actually read, including legitimate zeros.
func (r *Repository) adminOverview(ctx context.Context, callsUnavailable bool, callSnapshot *adminCallSnapshot) (AdminWorkbenchOverview, bool, error) {
	item := AdminWorkbenchOverview{UnavailableFields: []string{}}
	// Scan can write earlier destinations before reporting a later conversion
	// error. Publish a group only after its complete read succeeds.
	groups := []struct {
		fields []string
		load   func() error
	}{
		{[]string{"totalUsers", "todayNewUsers"}, func() error {
			var total, today int64
			err := r.db.QueryRowContext(ctx, "select count(*),coalesce(sum(u.created_at>=current_date()),0) from bp_user u where "+accountstatus.EffectiveStatusSQL("u")+" = 1").Scan(&total, &today)
			if err == nil {
				item.TotalUsers, item.TodayNewUsers = total, today
			}
			return err
		}},
		{[]string{"apiTotal", "apiEnabled"}, func() error {
			var total, enabled int64
			err := r.db.QueryRowContext(ctx, "select count(*),coalesce(sum(status=1),0) from bp_interface_api").Scan(&total, &enabled)
			if err == nil {
				item.APITotal, item.APIEnabled = total, enabled
			}
			return err
		}},
		{[]string{"keyTotal", "keyEnabled"}, func() error {
			var total, enabled int64
			err := r.db.QueryRowContext(ctx, "select count(*),coalesce(sum(status=1),0) from bp_api_key").Scan(&total, &enabled)
			if err == nil {
				item.KeyTotal, item.KeyEnabled = total, enabled
			}
			return err
		}},
		{[]string{"todayCalls", "todaySuccessCalls", "todayFailedCalls", "todayBillableCalls", "todayChargeAmount", "todayRevenue", "todaySuccessRate"}, func() error {
			if callsUnavailable {
				return errAdminAnalyticsProjectionUnavailable
			}
			var calls AdminWorkbenchOverview
			if callSnapshot != nil {
				var err error
				calls, err = adminOverviewCallsFromSnapshot(*callSnapshot)
				if err != nil {
					return err
				}
			} else if err := r.adminOverviewCalls(ctx, &calls); err != nil {
				return err
			}
			item.TodayCalls = calls.TodayCalls
			item.TodaySuccessCalls, item.TodayFailedCalls = calls.TodaySuccessCalls, calls.TodayFailedCalls
			item.TodayBillableCalls, item.TodayChargeAmount = calls.TodayBillableCalls, calls.TodayChargeAmount
			// 「今日收入」与收入趋势(adminRevenueTrendQuery)同口径：今日 PAID
			// 充值订单实付金额。平台收入主要来自套餐充值而非按量计费，若仍取
			// charge_amount 会出现趋势有收入、卡片为 0 的口径分裂。查询失败
			// （老库缺列等）时保留计费金额作为降级值。
			if paid, err := adminOverviewPaidRevenue(ctx, r.db, true); err == nil {
				item.TodayRevenue = paid
			} else {
				item.TodayRevenue = calls.TodayChargeAmount
			}
			// 成功率分母 = 成功 + 有效失败（404 不计入，见 v1.2.30 统计口径）。
			item.TodaySuccessRate = percent(calls.TodaySuccessCalls, calls.TodaySuccessCalls+calls.TodayFailedCalls)
			return nil
		}},
		{[]string{"totalRevenue"}, func() error {
			// 累计收入对齐趋势口径：全部 PAID 充值订单实付金额；充值订单
			// 查询不可用时降级接口计费投影（revenue.TotalQuery）。
			if paid, err := adminOverviewPaidRevenue(ctx, r.db, false); err == nil {
				item.TotalRevenue = paid
				return nil
			}
			total, err := r.adminOverviewTotalRevenue(ctx)
			if err == nil {
				item.TotalRevenue = total
			}
			return err
		}},
		{[]string{"pendingOrders", "pendingFriendLinks"}, func() error {
			var orders, links int64
			err := r.db.QueryRowContext(ctx, "select (select count(*) from bp_payment_order where status in ('CREATED','PAYING')), (select count(*) from bp_friend_link_application where status='PENDING')").Scan(&orders, &links)
			if err == nil {
				item.PendingOrders, item.PendingFriendLinks = orders, links
			}
			return err
		}},
	}
	// The outer workbench semaphore admits this section as one database slot.
	// Keep its metric groups sequential so nested fan-out cannot exceed that
	// admission limit or starve gateway traffic.
	failures := make([]error, len(groups))
	for i, group := range groups {
		failures[i] = group.load()
	}
	available := false
	for i, failure := range failures {
		if failure == nil {
			available = true
		} else {
			item.UnavailableFields = append(item.UnavailableFields, groups[i].fields...)
		}
	}
	return item, available, errors.Join(failures...)
}

// adminOverviewTotalRevenueQuery combines the durable projection with only the
// indexed, not-yet-projected confirmed rows in one consistent database read.
func adminOverviewTotalRevenueQuery() string { return revenue.TotalQuery }

func (r *Repository) adminOverviewTotalRevenue(ctx context.Context) (string, error) {
	return revenue.NewRepository(r.db).Total(ctx)
}

// adminOverviewPaidRevenue 汇总 PAID 充值订单实付金额，与收入趋势
// (adminRevenueTrendQuery) 保持同一数据源。today=true 限定 paid_at>=今日；
// 不加 FORCE INDEX：sum(pa-id) 走优化器自选计划，且老库缺索引时不至于
// 因 1176 直接废掉整个收入卡片。
func adminOverviewPaidRevenue(ctx context.Context, db *sql.DB, today bool) (string, error) {
	query := "select cast(coalesce(sum(coalesce(origin_amount,pay_amount)),0) as char) from bp_payment_order where status='PAID'"
	if today {
		query += " and paid_at>=current_date()"
	}
	var total string
	err := db.QueryRowContext(ctx, query).Scan(&total)
	return total, err
}

// isMissingTableError reports whether the database error means the queried
// table does not exist (MySQL error 1146). The repository layer uses it to
// degrade cleanly while schema upgrades are applied out-of-band. A missing
// index (Error 1176) is a different failure mode and is never classified as a
// missing table here.
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

func adminOverviewCallsQuery() string {
	return `
		select coalesce(sum(m.call_count),0),
		       coalesce(sum(m.success_count),0),
		       coalesce(sum(m.failure_count),0),
		       coalesce(sum(m.billable_count),0),
		       cast(coalesce(sum(m.charge_amount),0) as char)
		from bp_call_user_metric_minute m
		inner join bp_user u on u.id=m.user_id
		where m.bucket_start>=current_date()
		  and ` + accountstatus.EffectiveStatusSQL("u") + `=1
	`
}

// adminOverviewCallsAllQuery 是全站口径的今日调用统计：直接聚合
// bp_call_metric_minute（发布 000078 的全量聚合，含所有用户流量）。
// 性能优于 bp_call_user_metric_minute join bp_user（免 join、按
// bucket_start 主键范围扫描），且正确反映“整个系统今日调用数”。
// 该表创建于 migration 000078，若老库缺失由调用方降级回用户口径。
func adminOverviewCallsAllQuery() string {
	return `
		select coalesce(sum(m.call_count),0),
		       coalesce(sum(m.success_count),0),
		       coalesce(sum(m.failure_count),0),
		       coalesce(sum(m.billable_count),0),
		       cast(coalesce(sum(m.charge_amount),0) as char)
		from bp_call_metric_minute m
		where m.bucket_start>=current_date()
	`
}

// adminOverviewCalls 读取今日全站调用统计。优先使用全量聚合表
// bp_call_metric_minute（免 join、主键范围扫描）；当该表缺失（升级迁移尚未
// 完成）时，降级到用户口径 rollup（bp_call_user_metric_minute）。两部分聚合数据
// 均由 analytics 滚动聚合 worker 实时填充；若 worker 尚未追上当日流量，本段返回
// 0 值——工作台展示真实的 0，而非伪造数据。要得到真实数据，请确保部署启用
// analytics（analytics.enabled=true 或 ANALYTICS_ENABLED=true），worker 会在
// 数秒内补齐当日聚合。
func (r *Repository) adminOverviewCalls(ctx context.Context, item *AdminWorkbenchOverview) error {
	if adminUsesBoundedAnalyticsSnapshot(ctx) {
		return r.adminOverviewCallsSnapshot(ctx, item)
	}
	scan := func(q string) error {
		return r.db.QueryRowContext(ctx, q).Scan(
			&item.TodayCalls,
			&item.TodaySuccessCalls,
			&item.TodayFailedCalls,
			&item.TodayBillableCalls,
			&item.TodayChargeAmount,
		)
	}
	// 第一优先：全站 rollup 表
	if err := scan(adminOverviewCallsAllQuery()); err == nil {
		// rollup 表只有 analytics worker 运行时才会填充。worker 被关闭或
		// 尚未追平当日流量时，rollup 读数偏低甚至为 0，但当日调用日志里
		// 已有真实流量。checkpoint 落后于日志时回退到对日志的有界实时聚合，
		// 避免工作台在 “analytics.enabled=false” 的部署中长期显示 0。
		return r.adminOverviewCallsLogFallback(ctx, item)
	} else if !isMissingTableError(err) {
		return err
	}
	// 全站 rollup 表缺失（升级迁移进行中）：试用户口径 rollup
	if err := scan(adminOverviewCallsQuery()); err == nil {
		return r.adminOverviewCallsLogFallback(ctx, item)
	} else if !isMissingTableError(err) {
		return err
	}
	// Neither projection produced a row. HTTP must report unavailability;
	// non-HTTP recovery may explicitly aggregate a complete result from source.
	// An empty EXISTS probe cannot supply a valid amount for this metric group.
	return r.adminOverviewCallsReplayFromSource(ctx, item)
}

// adminSourceTrafficExistsQuery is a constant-shape, time-bounded probe used
// only when a rollup result is empty. The created_at predicate matches the
// analytics window indexes, so it does not turn the normal dashboard path into
// an unbounded source-log scan.
func adminSourceTrafficExistsQuery(windowSQL string) string {
	return `
		select exists(
			select 1
			from bp_interface_call_log l
			inner join bp_user u on u.id=l.user_id
			where l.created_at>=` + windowSQL + `
			  and l.charge_type<>'ADMIN'
			  and ` + accountstatus.EffectiveStatusSQL("u") + `=1
			limit 1
		)
	`
}

func (r *Repository) adminSourceTrafficExists(ctx context.Context, windowSQL string) (bool, error) {
	var exists int
	if err := r.db.QueryRowContext(ctx, adminSourceTrafficExistsQuery(windowSQL)).Scan(&exists); err != nil {
		return false, err
	}
	return exists != 0, nil
}

// adminOverviewCallsLogQuery aggregates today's traffic straight from the
// append-only call log. It mirrors the adminOverviewCallsAllQuery metrics and
// is only used when the minute rollups are empty while today's log holds
// open traffic.
func adminOverviewCallsLogQuery() string {
	return `
		select coalesce(count(*),0),
		       coalesce(sum(l.success=1),0),
		       coalesce(sum(` + calloutcome.SQLLegacyFailure("l") + `),0),
		       coalesce(sum(l.billable=1),0),
		       cast(coalesce(sum(l.charge_amount),0) as char)
		from bp_interface_call_log l
		inner join bp_user u on u.id=l.user_id
		where l.created_at>=current_date()
		  and l.charge_type<>'ADMIN'
		  and ` + accountstatus.EffectiveStatusSQL("u") + ` = 1
	`
}

// adminWorkbenchRollupLagTolerance 是概览和调用趋势共用的聚合滞后容忍时间。
// 非零投影超过该阈值不能视为当前完整统计；HTTP 读取必须降级，
// 原始日志聚合仍只允许在既有的非 HTTP 恢复流程中执行。
//
// 阈值取 10 分钟：worker 默认 1 秒 tick 一次，健康态 last_completed_at 年龄
// 在 0~1s 区间。生产上观察到日志清理（大事务 / FTWRL / 锁等待 / 短暂无新日志
// 让单次 tick 拉长）会让单次 tick 耗时或错误恢复耗时偶发冲到分钟量级，3 分钟的
// 容差会把这些"自愈中"的短暂窗口都判成不可用，导致运营概览和调用趋势在清理
// 期间频繁降级。放到 10 分钟后 worker 有充裕的自愈空间；真的停摆时降级仍会
// 触发（因为 tick 间隔 = 1s，10 分钟意味着连续 600 次 tick 都失败，此时控制台
// 也早该通过 analytics_health 面板告警）。
const adminWorkbenchRollupLagTolerance = 10 * time.Minute

// adminRollupCheckpointFresh 判断 analytics 投影是否已追平。
//
// 表缺失（老库未升级）或行缺失（worker 从未运行）都返回 false，
// 由调用方降级；允许的非 HTTP 恢复流程可以继续回退源表。
func (r *Repository) adminRollupCheckpointFresh(ctx context.Context, workerKey string, tolerance time.Duration) (bool, error) {
	if r.forceStaleRollupCheckpoint {
		// 测试钩子：强制判定为滞后，用于覆盖「投影有值但不新鲜」的分支。
		return false, nil
	}
	var completedAt sql.NullTime
	err := r.db.QueryRowContext(ctx, `
		select last_completed_at from bp_analytics_checkpoint where worker_key=?
	`, workerKey).Scan(&completedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || isMissingTableError(err) {
			return false, nil
		}
		return false, err
	}
	if !completedAt.Valid {
		return false, nil
	}
	return time.Since(completedAt.Time) <= tolerance, nil
}

func (r *Repository) adminOverviewCallsLogFallback(ctx context.Context, item *AdminWorkbenchOverview) error {
	// 快路径：rollup 已追平当日流量时，直接采信投影结果。
	// 与旧实现的关键差异：旧逻辑只在 TodayCalls == 0 时才探测源表，于是
	// 「投影有数但不完整」（worker 中途启用、当日 rollup 只覆盖了下午）
	// 会静默显示一个偏低的值。现在改为按投影的新鲜度判定。
	if item.TodayCalls != 0 {
		fresh, err := r.adminRollupCheckpointFresh(ctx, "call_log_rollup", adminWorkbenchRollupLagTolerance)
		if err != nil {
			return err
		}
		if fresh {
			return nil
		}
		// 投影滞后：用源表的有界实时聚合覆盖，保证控制台数字与真实流量一致。
		if err := r.adminOverviewCallsReplayFromSource(ctx, item); err != nil {
			// 回退失败不应让整个概览报错——保留已有的投影读数，
			// 它至少是个下界，比空白更有用。
			if isMissingTableError(err) {
				return nil
			}
			return err
		}
		return nil
	}

	hasSource, err := r.adminSourceTrafficExists(ctx, adminSourceTodayWindow)
	if err != nil {
		return err
	}
	if !hasSource {
		return nil
	}
	// A zero rollup with real source traffic is the migration/rebuild failure
	// mode. Do not wait for a checkpoint comparison that may itself have been
	// restored from the old dataset.
	return r.adminOverviewCallsReplayFromSource(ctx, item)
}

// adminOverviewCallsReplayFromSource 用当日源日志重算今日调用指标。
// 单独的提取方法让「投影滞后」与「投影为空」两条路径共用同一段写入逻辑，
// 避免两处驱动同一组字段时出现字段错位。
func (r *Repository) adminOverviewCallsReplayFromSource(ctx context.Context, item *AdminWorkbenchOverview) error {
	if err := rejectAdminRawLogFallback(ctx); err != nil {
		return err
	}
	return r.db.QueryRowContext(ctx, adminOverviewCallsLogQuery()).Scan(
		&item.TodayCalls,
		&item.TodaySuccessCalls,
		&item.TodayFailedCalls,
		&item.TodayBillableCalls,
		&item.TodayChargeAmount,
	)
}

func (r *Repository) AdminRevenueTrend(ctx context.Context, labels []string) (AdminRevenueTrend7d, error) {
	result := AdminRevenueTrend7d{
		Labels:      labels,
		OrderCounts: make([]int64, len(labels)),
		PaidAmounts: zeroMoneyList(len(labels)),
		TotalAmount: "0.0000",
	}
	rows, err := r.db.QueryContext(ctx, adminRevenueTrendQuery())
	if err != nil {
		// MySQL Error 1176: the FORCE INDEX hint references an index that an
		// upgraded database has not created yet. Retry without the hint so the
		// revenue trend still renders while the schema catches up.
		if isMissingIndexError(err) {
			rows, err = r.db.QueryContext(ctx, adminRevenueTrendNoHintQuery())
		}
		if err != nil {
			return result, err
		}
	}
	defer rows.Close()
	index := make(map[string]int, len(labels))
	for i, label := range labels {
		index[label] = i
	}
	for rows.Next() {
		var label, amount, totalAmount string
		var count, totalOrders int64
		if err := rows.Scan(&label, &count, &amount, &totalOrders, &totalAmount); err != nil {
			return result, err
		}
		if i, ok := index[label]; ok {
			result.OrderCounts[i] = count
			result.PaidAmounts[i] = amount
		}
		result.TotalOrders = totalOrders
		result.TotalAmount = totalAmount
	}
	return result, rows.Err()
}

func adminRevenueTrendQuery() string {
	return `
		select date_format(paid_at,'%m-%d'),
		       count(*),
		       cast(coalesce(sum(coalesce(origin_amount,pay_amount)),0) as char),
		       sum(count(*)) over(),
		       cast(coalesce(sum(sum(coalesce(origin_amount,pay_amount))) over(),0) as char)
		from bp_payment_order force index(idx_bp_payment_order_paid)
		where status='PAID'
		  and paid_at>=date_sub(current_date(), interval 6 day)
		group by date_format(paid_at,'%m-%d')
		order by min(paid_at)
	`
}

// adminRevenueTrendNoHintQuery is the identical aggregation without the FORCE
// INDEX hint. It is used only when the hinted query fails with MySQL Error
// 1176 because the upgrade path has not yet created idx_bp_payment_order_paid.
func adminRevenueTrendNoHintQuery() string {
	return `
		select date_format(paid_at,'%m-%d') as day,
		       count(*),
		       cast(coalesce(sum(coalesce(origin_amount,pay_amount)),0) as char),
		       sum(count(*)) over(),
		       cast(coalesce(sum(sum(coalesce(origin_amount,pay_amount))) over(),0) as char)
		from bp_payment_order
		where status='PAID'
		  and paid_at>=date_sub(current_date(), interval 6 day)
		group by date_format(paid_at,'%m-%d')
		order by min(paid_at)
	`
}

// isMissingIndexError reports whether the database error means a FORCE INDEX
// hint referenced a non-existent index (MySQL error 1176). The repository
// layer retries such queries without the hint.
func isMissingIndexError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "1176") && strings.Contains(message, "doesn't exist") {
		return true
	}
	return false
}

func (r *Repository) AdminCallTrend(ctx context.Context, labels []string) (AdminCallTrend7d, error) {
	if adminUsesBoundedAnalyticsSnapshot(ctx) {
		return r.adminCallTrendSnapshot(ctx)
	}
	result := AdminCallTrend7d{Labels: labels, TotalCalls: make([]int64, len(labels)), SuccessCalls: make([]int64, len(labels)), FailedCalls: make([]int64, len(labels)), BillableCalls: make([]int64, len(labels)), ChargeAmounts: zeroMoneyList(len(labels))}
	// 由粗到细：日表 → 全站分钟表 → 用户分钟表。任一层缺失都只是回退一层，
	// 只有当三层投影全都不可读时才落到源表。
	rows, err := r.openPreferredProjection(
		ctx,
		adminCallTrendAllDayQuery(),
		adminCallTrendAllQuery(),
		adminCallTrendQuery(),
	)
	if err != nil {
		if isMissingTableError(err) {
			// Every projection can be absent during an out-of-band upgrade.
			return r.adminCallTrendSourceFallback(ctx, result)
		}
		return result, err
	}
	index := map[string]int{}
	for i, label := range labels {
		index[label] = i
	}
	if err := scanAdminCallTrendRows(rows, &result, index); err != nil {
		return result, err
	}
	// 逐日缺口回退：rollup 只有部分日期有数据时（例如预聚合 worker 是在某一天
	// 才上线的，更早的日子永远不会有 rollup），只靠「全量为 0」判定会让图表出现
	// 中间几根柱子恒为 0 的假象。这里检查是否所有日期都为空，是则整体回退；
	// 否则只对缺失的具体日期补一次源表查询，成本与缺口数成正比。
	if result.Total == 0 {
		return r.adminCallTrendSourceFallback(ctx, result)
	}
	// Nonzero buckets can outlive a stopped worker. Like today's overview,
	// require a recent checkpoint before treating them as complete statistics.
	// Keep the empty-source path above valid without a running worker.
	fresh, err := r.adminRollupCheckpointFresh(ctx, adminAnalyticsCheckpointKey, adminWorkbenchRollupLagTolerance)
	if err != nil {
		return result, err
	}
	if !fresh {
		return result, errAdminAnalyticsProjectionUnavailable
	}
	if err := r.fillAdminCallTrendGaps(ctx, &result); err != nil {
		// The rollup above is valid. Optional gap recovery must not discard it,
		// particularly on the request path where raw-log scans are prohibited.
		return result, nil
	}
	return result, nil
}

// fillAdminCallTrendGaps 为 rollup 缺失的具体日期补齐真实数据。
//
// 只查缺失的那几天，且复用同一条源表查询（通过 where 收窄到缺口日期集合），
// 因此最坏情况仍是一次查询，而不是每天一次。任何失败都不影响已取得的 rollup
// 数据——补缺口是「尽力而为」，宁可少几天也不要把整张图变成错误状态。
func (r *Repository) fillAdminCallTrendGaps(ctx context.Context, result *AdminCallTrend7d) error {
	var missing []string
	for index, label := range result.Labels {
		if result.TotalCalls[index] == 0 && result.FailedCalls[index] == 0 {
			missing = append(missing, label)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	hasSource, err := r.adminSourceTrafficExists(ctx, adminSourceTrendWindow)
	if err != nil || !hasSource {
		return err
	}
	if err := rejectAdminRawLogFallback(ctx); err != nil {
		return err
	}
	rows, err := r.db.QueryContext(ctx, adminCallTrendGapLogQuery(missing))
	if err != nil {
		// 源表不可用（迁移中途、权限不足）时保留 rollup 已有数据。
		if isMissingTableError(err) {
			return nil
		}
		return err
	}
	defer rows.Close()
	index := make(map[string]int, len(result.Labels))
	for i, label := range result.Labels {
		index[label] = i
	}
	for rows.Next() {
		var label, amount string
		var total, success, failed, billable int64
		if err := rows.Scan(&label, &total, &success, &failed, &billable, &amount); err != nil {
			return err
		}
		i, ok := index[label]
		if !ok {
			continue
		}
		result.TotalCalls[i] = total
		result.SuccessCalls[i] = success
		result.FailedCalls[i] = failed
		result.BillableCalls[i] = billable
		result.ChargeAmounts[i] = amount
		result.Total += total
	}
	return rows.Err()
}

// adminCallTrendGapLogQuery 只回退缺失日期的源表查询。
//
// 标签集合来自上层已知的缺口（形如 "09-18"），直接以 in 列表收窄，
// 保证扫描范围仍受 created_at 下界约束、不会退化成全表扫描。
func adminCallTrendGapLogQuery(labels []string) string {
	quoted := make([]string, 0, len(labels))
	for _, label := range labels {
		// label 由本包按 %m-%d 生成，不含引号；仍做一次防御性转义避免拼接风险。
		quoted = append(quoted, "'"+strings.ReplaceAll(label, "'", "")+"'")
	}
	return `
		select date_format(l.created_at,'%m-%d'),
		       count(*),
		       sum(l.success=1),
		       sum(` + calloutcome.SQLLegacyFailure("l") + `),
		       sum(l.billable=1),
		       cast(coalesce(sum(l.charge_amount),0) as char)
		from bp_interface_call_log l
		inner join bp_user u on u.id=l.user_id
		where l.created_at>=date_sub(current_date(), interval 6 day)
		  and l.charge_type<>'ADMIN'
		  and date_format(l.created_at,'%m-%d') in (` + strings.Join(quoted, ",") + `)
		  and ` + accountstatus.EffectiveStatusSQL("u") + `=1
		group by date_format(l.created_at,'%m-%d')
	`
}

func scanAdminCallTrendRows(rows *sql.Rows, result *AdminCallTrend7d, index map[string]int) error {
	defer rows.Close()
	for rows.Next() {
		var label, amount string
		var total, success, failed, billable int64
		if err := rows.Scan(&label, &total, &success, &failed, &billable, &amount); err != nil {
			return err
		}
		if i, ok := index[label]; ok {
			result.TotalCalls[i] = total
			result.SuccessCalls[i] = success
			result.FailedCalls[i] = failed
			result.BillableCalls[i] = billable
			result.ChargeAmounts[i] = amount
			result.Total += total
		}
	}
	return rows.Err()
}

func (r *Repository) adminCallTrendSourceFallback(ctx context.Context, result AdminCallTrend7d) (AdminCallTrend7d, error) {
	hasSource, err := r.adminSourceTrafficExists(ctx, adminSourceTrendWindow)
	if err != nil {
		return result, err
	}
	if !hasSource {
		return result, nil
	}
	if err := rejectAdminRawLogFallback(ctx); err != nil {
		return result, err
	}
	rows, err := r.db.QueryContext(ctx, adminCallTrendLogQuery())
	if err != nil {
		return result, err
	}
	index := make(map[string]int, len(result.Labels))
	for i, label := range result.Labels {
		index[label] = i
	}
	// The result passed here is empty for the recovery path. Keeping the scan
	// helper shared ensures rollup and source responses have identical fields.
	result.Total = 0
	if err := scanAdminCallTrendRows(rows, &result, index); err != nil {
		return result, err
	}
	return result, nil
}

// adminCallTrendDayQuery 是用户维度的 7 天趋势，读日粒度投影（每用户每天 1 行）。
//
// 保留 bp_user join：账号状态是「读时」条件，冻结进聚合会算错。窗口
// bucket_date>=current_date()-6 与分钟表口径完全一致，只是把 7×用户×1440 行
// 的扫描压到 7×用户 行。
func adminCallTrendDayQuery() string {
	return `
		select date_format(m.bucket_date,'%m-%d'),
		       sum(m.call_count),
		       sum(m.success_count),
		       sum(m.failure_count),
		       sum(m.billable_count),
		       cast(coalesce(sum(m.charge_amount),0) as char)
		from bp_call_user_metric_day m
		inner join bp_user u on u.id=m.user_id
		where m.bucket_date>=date_sub(current_date(), interval 6 day)
		  and ` + accountstatus.EffectiveStatusSQL("u") + `=1
		group by date_format(m.bucket_date,'%m-%d')
	`
}

// adminCallTrendQuery 是用户维度的分钟口径读法。日表缺失（000100 迁移尚未应用）
// 时作为回退，数字与日表同源。
func adminCallTrendQuery() string {
	return `
		select date_format(m.bucket_start,'%m-%d'),
		       sum(m.call_count),
		       sum(m.success_count),
		       sum(m.failure_count),
		       sum(m.billable_count),
		       cast(coalesce(sum(m.charge_amount),0) as char)
		from bp_call_user_metric_minute m
		inner join bp_user u on u.id=m.user_id
		where m.bucket_start>=date_sub(current_date(), interval 6 day)
		  and ` + accountstatus.EffectiveStatusSQL("u") + `=1
		group by date_format(m.bucket_start,'%m-%d')
	`
}

// adminCallTrendAllDayQuery 是全站口径的 7 天调用趋势：直接聚合
// bp_call_metric_day（1 天 1 行，免 join），比分钟表少扫最多 1440 倍。
func adminCallTrendAllDayQuery() string {
	return `
		select date_format(m.bucket_date,'%m-%d'),
		       sum(m.call_count),
		       sum(m.success_count),
		       sum(m.failure_count),
		       sum(m.billable_count),
		       cast(coalesce(sum(m.charge_amount),0) as char)
		from bp_call_metric_day m
		where m.bucket_date>=date_sub(current_date(), interval 6 day)
		group by date_format(m.bucket_date,'%m-%d')
	`
}

// adminCallTrendAllQuery 是全站口径的分钟读法，日表缺失时作为回退。它按
// bucket_start 主键范围扫描且免 join，仍优于用户表 join bp_user。
func adminCallTrendAllQuery() string {
	return `
		select date_format(m.bucket_start,'%m-%d'),
		       sum(m.call_count),
		       sum(m.success_count),
		       sum(m.failure_count),
		       sum(m.billable_count),
		       cast(coalesce(sum(m.charge_amount),0) as char)
		from bp_call_metric_minute m
		where m.bucket_start>=date_sub(current_date(), interval 6 day)
		group by date_format(m.bucket_start,'%m-%d')
	`
}

// openPreferredProjection runs the first projection query the schema supports.
//
// The queries must be ordered from the coarsest projection to the finest, so a
// database that has not yet applied a layer's migration transparently reads the
// next layer down instead of dropping all the way to a raw-log scan. A returned
// error that isMissingTableError classifies as "no projection is readable" and
// lets the caller decide whether a source fallback is warranted.
func (r *Repository) openPreferredProjection(ctx context.Context, queries ...string) (*sql.Rows, error) {
	var lastErr error
	for _, query := range queries {
		rows, err := r.db.QueryContext(ctx, query)
		if err == nil {
			return rows, nil
		}
		if !isMissingTableError(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// adminCallTrendLogQuery is a bounded recovery query. It is deliberately kept
// separate from the normal rollup reader so the high-cardinality source table
// is touched only when the projection is empty but the window has real traffic.
func adminCallTrendLogQuery() string {
	return `
		select date_format(l.created_at,'%m-%d'),
		       count(*),
		       sum(l.success=1),
		       sum(` + calloutcome.SQLLegacyFailure("l") + `),
		       sum(l.billable=1),
		       cast(coalesce(sum(l.charge_amount),0) as char)
		from bp_interface_call_log l
		inner join bp_user u on u.id=l.user_id
		where l.created_at>=date_sub(current_date(), interval 6 day)
		  and l.charge_type<>'ADMIN'
		  and ` + accountstatus.EffectiveStatusSQL("u") + `=1
		group by date_format(l.created_at,'%m-%d')
		order by min(l.created_at)
	`
}

func (r *Repository) AdminHotAPIs(ctx context.Context) ([]AdminHotAPIStats, error) {
	rows, err := r.db.QueryContext(ctx, adminHotAPIsQuery())
	if err != nil {
		if isMissingTableError(err) {
			return r.adminHotAPIsSourceFallback(ctx)
		}
		return nil, err
	}
	items, scanErr := scanAdminHotAPIs(rows)
	if scanErr != nil {
		return nil, scanErr
	}
	if len(items) == 0 {
		return r.adminHotAPIsSourceFallback(ctx)
	}
	return items, nil
}

func (r *Repository) adminHotAPIsSourceFallback(ctx context.Context) ([]AdminHotAPIStats, error) {
	hasSource, err := r.adminSourceTrafficExists(ctx, adminSourceTodayWindow)
	if err != nil {
		return nil, err
	}
	if !hasSource {
		return []AdminHotAPIStats{}, nil
	}
	if err := rejectAdminRawLogFallback(ctx); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, adminHotAPIsLogQuery())
	if err != nil {
		return nil, err
	}
	return scanAdminHotAPIs(rows)
}

func scanAdminHotAPIs(rows *sql.Rows) ([]AdminHotAPIStats, error) {
	defer rows.Close()
	items := make([]AdminHotAPIStats, 0, 10)
	for rows.Next() {
		var id int64
		var item AdminHotAPIStats
		if err := rows.Scan(
			&id,
			&item.APICode,
			&item.Name,
			&item.APIStatus,
			&item.TodayCalls,
			&item.SuccessCalls,
			&item.FailedCalls,
			&item.BillableCalls,
			&item.ChargeAmount,
			&item.AvgElapsedMS,
		); err != nil {
			return nil, err
		}
		item.ID = fmt.Sprintf("%d", id)
		// 成功率分母 = 成功 + 有效失败（404 不计入，见 v1.2.30 统计口径）。
		item.SuccessRate = percent(item.SuccessCalls, item.SuccessCalls+item.FailedCalls)
		items = append(items, item)
	}
	return items, rows.Err()
}

func adminHotAPIsQuery() string {
	return `
		select a.id,
		       a.api_code,
		       a.name,
		       a.status,
		       sum(m.call_count) today_calls,
		       coalesce(sum(m.success_count),0) success_calls,
		       coalesce(sum(m.failure_count),0) failed_calls,
		       coalesce(sum(m.billable_count),0) billable_calls,
		       cast(coalesce(sum(m.charge_amount),0) as char) charge_amount,
		       cast(coalesce(sum(m.elapsed_sum)/nullif(sum(m.call_count),0),0) as unsigned) avg_elapsed_ms
		from bp_interface_api a
		inner join bp_call_api_metric_minute m on m.interface_id=a.id
		  and m.bucket_start>=current_date()
		inner join bp_user u on u.id=m.user_id
		  and ` + accountstatus.EffectiveStatusSQL("u") + `=1
		where a.status=1
		group by a.id,a.api_code,a.name,a.status
		order by today_calls desc,sum(m.charge_amount) desc,a.id desc
		limit 10
	`
}

// adminHotAPIsLogQuery is used only during rollup recovery. Its current-day
// predicate keeps the fallback bounded while preserving the same response
// contract as the minute projection.
func adminHotAPIsLogQuery() string {
	return `
		select a.id,
		       a.api_code,
		       a.name,
		       a.status,
		       count(*) today_calls,
		       coalesce(sum(l.success=1),0) success_calls,
		       coalesce(sum(` + calloutcome.SQLLegacyFailure("l") + `),0) failed_calls,
		       coalesce(sum(l.billable=1),0) billable_calls,
		       cast(coalesce(sum(l.charge_amount),0) as char) charge_amount,
		       cast(coalesce(sum(l.elapsed_ms)/nullif(count(*),0),0) as unsigned) avg_elapsed_ms
		from bp_interface_call_log l
		inner join bp_interface_api a on a.id=l.interface_id
		inner join bp_user u on u.id=l.user_id
		where l.created_at>=current_date()
		  and l.charge_type<>'ADMIN'
		  and ` + accountstatus.EffectiveStatusSQL("u") + `=1
		  and a.status=1
		group by a.id,a.api_code,a.name,a.status
		order by today_calls desc,sum(l.charge_amount) desc,a.id desc
		limit 10
	`
}

func (r *Repository) AdminActiveUsers(ctx context.Context) ([]AdminActiveUserStats, error) {
	rows, err := r.openPreferredProjection(ctx, adminActiveUsersQuery(), adminActiveUsersMinuteQuery())
	if err != nil {
		if isMissingTableError(err) {
			return r.adminActiveUsersSourceFallback(ctx)
		}
		return nil, err
	}
	items, scanErr := scanAdminActiveUsers(rows)
	if scanErr != nil {
		return nil, scanErr
	}
	if len(items) == 0 {
		return r.adminActiveUsersSourceFallback(ctx)
	}
	return items, nil
}

func (r *Repository) adminActiveUsersSourceFallback(ctx context.Context) ([]AdminActiveUserStats, error) {
	hasSource, err := r.adminSourceTrafficExists(ctx, adminSourceTodayWindow)
	if err != nil {
		return nil, err
	}
	if !hasSource {
		return []AdminActiveUserStats{}, nil
	}
	if err := rejectAdminRawLogFallback(ctx); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, adminActiveUsersLogQuery())
	if err != nil {
		return nil, err
	}
	return scanAdminActiveUsers(rows)
}

func scanAdminActiveUsers(rows *sql.Rows) ([]AdminActiveUserStats, error) {
	defer rows.Close()
	items := make([]AdminActiveUserStats, 0, 10)
	for rows.Next() {
		var id int64
		var item AdminActiveUserStats
		if err := rows.Scan(
			&id,
			&item.Username,
			&item.RealName,
			&item.TodayCalls,
			&item.SuccessCalls,
			&item.FailedCalls,
			&item.ChargeAmount,
		); err != nil {
			return nil, err
		}
		item.ID = fmt.Sprintf("%d", id)
		// 成功率分母 = 成功 + 有效失败（404 不计入，见 v1.2.30 统计口径）。
		item.SuccessRate = percent(item.SuccessCalls, item.SuccessCalls+item.FailedCalls)
		items = append(items, item)
	}
	return items, rows.Err()
}

// adminActiveUsersQuery 读日粒度投影：当天每个用户只有 1 行，因此这条「只看
// 前 10 名」的榜单不再需要扫描（用户数 × 1440）行再排序。
func adminActiveUsersQuery() string {
	return `
		select u.id,
		       u.username,
		       u.display_name,
		       sum(m.call_count) today_calls,
		       coalesce(sum(m.success_count),0) success_calls,
		       coalesce(sum(m.failure_count),0) failed_calls,
		       cast(coalesce(sum(m.charge_amount),0) as char) charge_amount
		from bp_user u
		inner join bp_call_user_metric_day m on m.user_id=u.id
		  and m.bucket_date=current_date()
		where ` + accountstatus.EffectiveStatusSQL("u") + `=1
		group by u.id,u.username,u.display_name
		order by today_calls desc,sum(m.charge_amount) desc,u.id desc
		limit 10
	`
}

// adminActiveUsersMinuteQuery 是日表缺失时的回退，口径与日表一致。
func adminActiveUsersMinuteQuery() string {
	return `
		select u.id,
		       u.username,
		       u.display_name,
		       sum(m.call_count) today_calls,
		       coalesce(sum(m.success_count),0) success_calls,
		       coalesce(sum(m.failure_count),0) failed_calls,
		       cast(coalesce(sum(m.charge_amount),0) as char) charge_amount
		from bp_user u
		inner join bp_call_user_metric_minute m on m.user_id=u.id
		  and m.bucket_start>=current_date()
		where ` + accountstatus.EffectiveStatusSQL("u") + `=1
		group by u.id,u.username,u.display_name
		order by today_calls desc,sum(m.charge_amount) desc,u.id desc
		limit 10
	`
}

// adminActiveUsersLogQuery is the bounded source-table counterpart used while
// the user-minute projection is empty during migration recovery.
func adminActiveUsersLogQuery() string {
	return `
		select u.id,
		       u.username,
		       u.display_name,
		       count(*) today_calls,
		       coalesce(sum(l.success=1),0) success_calls,
		       coalesce(sum(` + calloutcome.SQLLegacyFailure("l") + `),0) failed_calls,
		       cast(coalesce(sum(l.charge_amount),0) as char) charge_amount
		from bp_interface_call_log l
		inner join bp_user u on u.id=l.user_id
		where l.created_at>=current_date()
		  and l.charge_type<>'ADMIN'
		  and ` + accountstatus.EffectiveStatusSQL("u") + `=1
		group by u.id,u.username,u.display_name
		order by today_calls desc,sum(l.charge_amount) desc,u.id desc
		limit 10
	`
}

// AdminAccessSummary returns the totals used by the access atlas. Keep this
// query separate from the grouped point query so an empty/coordinate-less data
// set still produces a useful summary. UniqueIPs is retained for response
// compatibility but now represents additive per-batch/hour unique-IP
// contributions, not an exact distinct count across the full 30-day window.
// AdminAccessRange 是访问态势图册的时间范围。
//
// 语义：range 只决定 bucket_start 的下界，因此四种取值命中同一条
// idx_bp_call_region_hour_time 索引，SQL 结构完全一致。这样切换范围不会
// 引入新的执行计划，也不需要为「全部」单独建表或物化视图——
// 「全部」的下界就是该表自身的最小 bucket_start（见 adminAccessRangeStart）。
type AdminAccessRange string

const (
	AdminAccessRangeToday AdminAccessRange = "today"
	AdminAccessRange7d    AdminAccessRange = "7d"
	AdminAccessRange30d   AdminAccessRange = "30d"
	AdminAccessRangeAll   AdminAccessRange = "all"
)

// normalizeAdminAccessRange 把外部输入收敛到已知取值，默认 30 天。
func normalizeAdminAccessRange(raw string) AdminAccessRange {
	switch AdminAccessRange(strings.ToLower(strings.TrimSpace(raw))) {
	case AdminAccessRangeToday:
		return AdminAccessRangeToday
	case AdminAccessRange7d:
		return AdminAccessRange7d
	case AdminAccessRange30d, "":
		return AdminAccessRange30d
	case AdminAccessRangeAll:
		return AdminAccessRangeAll
	default:
		return AdminAccessRange30d
	}
}

// adminAccessWindowStart 计算某个范围对应的下界时间（本地时区，整点对齐）。
//
// 与 rollup 的写入粒度（整点桶）对齐：例如 bucket_start >= 今天 00:00 才可能
// 覆盖「今天」的全部小时桶；用 now() 会让 10:30 开始的查询漏掉 10:00 桶。
func adminAccessWindowStart(rng AdminAccessRange, now time.Time) time.Time {
	now = now.In(businessclock.Location())
	switch rng {
	case AdminAccessRangeToday:
		return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	case AdminAccessRange7d:
		// 含今天在内共 7 个自然日。
		start := now.AddDate(0, 0, -6)
		return time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, now.Location())
	case AdminAccessRange30d:
		start := now.AddDate(0, 0, -29)
		return time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, now.Location())
	case AdminAccessRangeAll:
		// 「全部」保留近端语义，但下界钳制到最近 90 天。
		//
		// 这里曾用 select min(bucket_start) 取「表里最早的小时桶」：表里存了
		// 多少历史，这条聚合读就扫多少历史，是整个访问态势链路里唯一还能无界
		// 增长的扫描。90 天覆盖了所有交互窗口（今天/7 天/30 天），同时把最坏
		// 扫描量变成一个确定上界；早于 90 天的历史不再进入图册。
		start := now.AddDate(0, 0, -(adminAccessAllWindowDays - 1))
		return time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, now.Location())
	default:
		return time.Time{}
	}
}

// adminAccessRangeWindowDays 返回该范围覆盖的自然天数，用于响应里的 windowDays。
func adminAccessRangeWindowDays(rng AdminAccessRange) int {
	switch rng {
	case AdminAccessRangeToday:
		return 1
	case AdminAccessRange7d:
		return 7
	case AdminAccessRange30d:
		return adminAccessLocationDays
	case AdminAccessRangeAll:
		// 与 adminAccessWindowStart 的钳制保持一致：窗口天数必须如实反映实际
		// 读取的长度，否则前端会把 90 天的数据标成「全部」。
		return adminAccessAllWindowDays
	default:
		return 0
	}
}

// AdminAccess 返回访问态势图册的汇总与地点明细。
func (r *Repository) AdminAccess(ctx context.Context, rawRange string) (AdminAccessSummary, []AdminAccessLocation, error) {
	rng := normalizeAdminAccessRange(rawRange)
	start, err := r.adminAccessWindowStart(ctx, rng)
	if err != nil {
		return AdminAccessSummary{WindowDays: adminAccessRangeWindowDays(rng)}, nil, err
	}
	result := AdminAccessSummary{WindowDays: adminAccessRangeWindowDays(rng)}
	rows, err := r.db.QueryContext(ctx, adminAccessQuery(), start, adminAccessLocationMax)
	if err != nil {
		if isMissingTableError(err) {
			return r.adminAccessRollupFallback(ctx, rng, start)
		}
		return result, nil, err
	}
	locations, scanErr := scanAdminAccessRows(rows, &result)
	if scanErr != nil {
		return result, nil, scanErr
	}
	if result.TotalCalls == 0 {
		return r.adminAccessRollupFallback(ctx, rng, start)
	}
	return result, locations, nil
}

// adminAccessWindowStart 把范围解析成查询下界。
//
// 「全部」需要先向数据库问一次该表的最早桶时间：这是一次常量代价的索引
// min() 查询（主键有序，O(1) 定位），远低于让后续聚合自己从 1970 扫起。
// adminAccessWindowStart 解析范围下界。每个范围的下界都是纯函数，因此不再需要
// 先探测 min(bucket_start)：那次探测本身既要读表，又让最坏扫描量跟着表里积攒的
// 历史一起增长。表缺失的情况由调用方的 isMissingTableError 降级链处理。
func (r *Repository) adminAccessWindowStart(_ context.Context, rng AdminAccessRange) (time.Time, error) {
	return adminAccessWindowStart(rng, time.Now()), nil
}

func scanAdminAccessRows(rows *sql.Rows, result *AdminAccessSummary) ([]AdminAccessLocation, error) {
	defer rows.Close()
	locations := make([]AdminAccessLocation, 0)
	for rows.Next() {
		var location AdminAccessLocation
		var country, province, city, isp, latitude, longitude, region, firstSeen, lastSeen sql.NullString
		if err := rows.Scan(
			&country,
			&province,
			&city,
			&isp,
			&latitude,
			&longitude,
			&region,
			&location.CallCount,
			&location.SuccessCalls,
			&location.FailedCalls,
			&location.UniqueIPs,
			&location.AverageElapsedMs,
			&firstSeen,
			&lastSeen,
			&result.TotalCalls,
			&result.SuccessCalls,
			&result.FailedCalls,
			&result.UniqueIPs,
			&result.ActiveCountries,
			&result.ActiveLocations,
		); err != nil {
			return nil, err
		}
		location.Country = nullStringValue(country)
		location.Province = nullStringValue(province)
		location.City = nullStringValue(city)
		location.ISP = nullStringValue(isp)
		location.Latitude = nullStringValue(latitude)
		location.Longitude = nullStringValue(longitude)
		location.Region = nullStringValue(region)
		location.FirstSeen = nullStringValue(firstSeen)
		location.LastSeen = nullStringValue(lastSeen)
		locations = append(locations, location)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return locations, nil
}

func (r *Repository) adminAccessSourceFallback(ctx context.Context, rng AdminAccessRange, start time.Time) (AdminAccessSummary, []AdminAccessLocation, error) {
	result := AdminAccessSummary{WindowDays: adminAccessRangeWindowDays(rng)}
	hasSource, err := r.adminSourceTrafficExistsSince(ctx, start)
	if err != nil {
		return result, nil, err
	}
	if !hasSource {
		return result, []AdminAccessLocation{}, nil
	}
	if err := rejectAdminRawLogFallback(ctx); err != nil {
		return result, nil, err
	}
	rows, err := r.db.QueryContext(ctx, adminAccessLogQuery(), start, adminAccessLocationMax)
	if err != nil {
		return result, nil, err
	}
	locations, err := scanAdminAccessRows(rows, &result)
	if err != nil {
		return result, nil, err
	}
	return result, locations, nil
}

// adminSourceTrafficExistsSince 是「该时间点之后是否有真实流量」的存在性探测。
// 与既有 adminSourceTrafficExists 的差别仅在窗口由调用方给定下界，
// 让访问态势的四种范围共用同一探测逻辑。
func (r *Repository) adminSourceTrafficExistsSince(ctx context.Context, start time.Time) (bool, error) {
	var exists int
	err := r.db.QueryRowContext(ctx, `
		select exists(
			select 1
			from bp_interface_call_log l
			inner join bp_user u on u.id=l.user_id
			where l.created_at >= ?
			  and l.charge_type<>'ADMIN'
			  and `+accountstatus.EffectiveStatusSQL("u")+`=1
			limit 1
		)
	`, start).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists != 0, nil
}

// adminAccessRollupFallback first tries the exact IP-hash hourly projection.
// It is much smaller than bp_interface_call_log and remains privacy-safe while
// the compact location projection is being rebuilt. Only when both rollups are
// empty does the request use the bounded raw-log recovery query.
func (r *Repository) adminAccessRollupFallback(ctx context.Context, rng AdminAccessRange, start time.Time) (AdminAccessSummary, []AdminAccessLocation, error) {
	result, locations, err := r.adminAccessRegionIPFallback(ctx, rng, start)
	if err == nil && (result.TotalCalls > 0 || len(locations) > 0) {
		return result, locations, nil
	}
	if err != nil && !isMissingTableError(err) {
		return result, nil, err
	}
	return r.adminAccessSourceFallback(ctx, rng, start)
}

func (r *Repository) adminAccessRegionIPFallback(ctx context.Context, rng AdminAccessRange, start time.Time) (AdminAccessSummary, []AdminAccessLocation, error) {
	result := AdminAccessSummary{WindowDays: adminAccessRangeWindowDays(rng)}
	rows, err := r.db.QueryContext(ctx, adminAccessRegionIPQuery(), start, adminAccessLocationMax)
	if err != nil {
		return result, nil, err
	}
	locations, err := scanAdminAccessRows(rows, &result)
	if err != nil {
		return result, nil, err
	}
	return result, locations, nil
}

// adminAccessRegionIPQuery reads the 000078 hourly projection while the newer
// compact 000079 location projection is empty or unavailable. The source keeps
// only a binary IP hash, so the fallback can still calculate distinct-IP
// totals without returning a plaintext address.
func adminAccessRegionIPQuery() string {
	return `
		with filtered as (
			select r.country,r.province,r.city,r.isp,r.latitude,r.longitude,r.client_ip_hash,
			       r.call_count,r.success_count,r.failure_count,r.elapsed_sum,r.first_seen,r.last_seen
			from bp_call_region_ip_hour r force index(idx_bp_call_region_ip_time)
			inner join bp_user u on u.id=r.user_id
			where r.bucket_start >= ?
			  and ` + accountstatus.EffectiveStatusSQL("u") + ` = 1
		),
		location_groups as (
			select country,province,city,isp,
			       case when latitude=0 and longitude=0 then null else cast(latitude as char) end latitude,
			       case when latitude=0 and longitude=0 then null else cast(longitude as char) end longitude,
			       nullif(concat_ws(' ',nullif(country,''),nullif(province,''),nullif(city,'')),'') region,
			       sum(call_count) call_count,
			       coalesce(sum(success_count),0) success_calls,
			       coalesce(sum(failure_count),0) failed_calls,
			       count(distinct case
			           when client_ip_hash <> unhex(sha2('` + iphash.RegionSQLPrefix() + `',256))
			           then client_ip_hash end) unique_ips,
			       cast(coalesce(sum(elapsed_sum)/nullif(sum(call_count),0),0) as unsigned) average_elapsed_ms,
			       date_format(min(first_seen),'%Y-%m-%d %H:%i:%s') first_seen,
			       date_format(max(last_seen),'%Y-%m-%d %H:%i:%s') last_seen
			from filtered
			group by country,province,city,isp,latitude,longitude
		),
		totals as (
			select coalesce(sum(call_count),0) total_calls,
			       coalesce(sum(success_count),0) success_calls,
			       coalesce(sum(failure_count),0) failed_calls,
			       count(distinct case
			           when client_ip_hash <> unhex(sha2('` + iphash.RegionSQLPrefix() + `',256))
			           then client_ip_hash end) unique_ips,
			       count(distinct nullif(country,'')) active_countries,
			       (select count(*) from location_groups) active_locations
			from filtered
		)
		select g.country,g.province,g.city,g.isp,g.latitude,g.longitude,g.region,
		       g.call_count,g.success_calls,g.failed_calls,g.unique_ips,g.average_elapsed_ms,
		       g.first_seen,g.last_seen,
		       t.total_calls,t.success_calls,t.failed_calls,t.unique_ips,t.active_countries,t.active_locations
		from location_groups g
		cross join totals t
		order by g.call_count desc,g.country,g.province,g.city,g.latitude,g.longitude
		limit ?
	`
}

// adminAccessQuery materializes the bounded traffic window once, then derives
// both the atlas totals and every grouped location from that same snapshot.
// This avoids the previous pair of full 30-day scans and keeps summary values
// exactly aligned with the rows rendered by the administrator workbench.
//
// 第一个占位符是窗口下界（时间值），不再用 `interval ? day`：把范围换算成
// 具体的下界时间后，MySQL 能把它当作常量下推，四种范围共享同一执行计划。
func adminAccessQuery() string {
	return `
		with filtered as (
			select r.country,r.province,r.city,r.isp,r.latitude,r.longitude,r.unique_ip_count,
			       r.call_count,r.success_count,r.failure_count,r.elapsed_sum,r.first_seen,r.last_seen
			from bp_call_region_hour r force index(idx_bp_call_region_hour_time)
			inner join bp_user u on u.id=r.user_id
			where r.bucket_start >= ?
			  and ` + accountstatus.EffectiveStatusSQL("u") + ` = 1
		),
		location_groups as (
			select country,province,city,isp,
			       case when latitude=0 and longitude=0 then null else cast(latitude as char) end latitude,
			       case when latitude=0 and longitude=0 then null else cast(longitude as char) end longitude,
			       nullif(concat_ws(' ',nullif(country,''),nullif(province,''),nullif(city,'')),'') region,
			       sum(call_count) call_count,
			       coalesce(sum(success_count),0) success_calls,
			       coalesce(sum(failure_count),0) failed_calls,
			       coalesce(sum(unique_ip_count),0) unique_ips,
			       cast(coalesce(sum(elapsed_sum)/nullif(sum(call_count),0),0) as unsigned) average_elapsed_ms,
			       date_format(min(first_seen),'%Y-%m-%d %H:%i:%s') first_seen,
			       date_format(max(last_seen),'%Y-%m-%d %H:%i:%s') last_seen
			from filtered
			group by country,province,city,isp,latitude,longitude
		),
		totals as (
			select coalesce(sum(call_count),0) total_calls,
			       coalesce(sum(success_count),0) success_calls,
			       coalesce(sum(failure_count),0) failed_calls,
			       coalesce(sum(unique_ip_count),0) unique_ips,
			       count(distinct nullif(country,'')) active_countries,
			       (select count(*) from location_groups) active_locations
			from filtered
		)
		select g.country,g.province,g.city,g.isp,g.latitude,g.longitude,g.region,
		       g.call_count,g.success_calls,g.failed_calls,g.unique_ips,g.average_elapsed_ms,
		       g.first_seen,g.last_seen,
		       t.total_calls,t.success_calls,t.failed_calls,t.unique_ips,t.active_countries,t.active_locations
		from location_groups g
		cross join totals t
		order by g.call_count desc,g.country,g.province,g.city,g.latitude,g.longitude
		limit ?
	`
}

// adminAccessLogQuery is a bounded recovery query for the atlas. It groups
// directly from the source log but returns only regional aggregates; client IP
// values are used solely inside COUNT(DISTINCT) and never leave the database.
func adminAccessLogQuery() string {
	return `
		with filtered as (
			select l.ip_country country,l.ip_province province,l.ip_city city,l.ip_isp isp,
			       l.latitude,l.longitude,l.client_ip,l.success,l.response_status,l.elapsed_ms,l.created_at
			from bp_interface_call_log l
			inner join bp_user u on u.id=l.user_id
			where l.created_at >= ?
			  and l.charge_type<>'ADMIN'
			  and ` + accountstatus.EffectiveStatusSQL("u") + `=1
		),
		location_groups as (
			select country,province,city,isp,
			       case when latitude=0 and longitude=0 then null else cast(latitude as char) end latitude,
			       case when latitude=0 and longitude=0 then null else cast(longitude as char) end longitude,
			       nullif(concat_ws(' ',nullif(country,''),nullif(province,''),nullif(city,'')),'') region,
			       count(*) call_count,
			       coalesce(sum(success=1),0) success_calls,
			       coalesce(sum(` + calloutcome.SQLLegacyFailure("") + `),0) failed_calls,
			       count(distinct nullif(trim(coalesce(client_ip,'')),'')) unique_ips,
			       cast(coalesce(sum(elapsed_ms)/nullif(count(*),0),0) as unsigned) average_elapsed_ms,
			       date_format(min(created_at),'%Y-%m-%d %H:%i:%s') first_seen,
			       date_format(max(created_at),'%Y-%m-%d %H:%i:%s') last_seen
			from filtered
			group by country,province,city,isp,latitude,longitude
		),
		totals as (
			select count(*) total_calls,
			       coalesce(sum(success=1),0) success_calls,
			       coalesce(sum(` + calloutcome.SQLLegacyFailure("") + `),0) failed_calls,
			       count(distinct nullif(trim(coalesce(client_ip,'')),'')) unique_ips,
			       count(distinct nullif(country,'')) active_countries,
			       (select count(*) from location_groups) active_locations
			from filtered
		)
		select g.country,g.province,g.city,g.isp,g.latitude,g.longitude,g.region,
		       g.call_count,g.success_calls,g.failed_calls,g.unique_ips,g.average_elapsed_ms,
		       g.first_seen,g.last_seen,
		       t.total_calls,t.success_calls,t.failed_calls,t.unique_ips,t.active_countries,t.active_locations
		from location_groups g
		cross join totals t
		order by g.call_count desc,g.country,g.province,g.city,g.latitude,g.longitude
		limit ?
	`
}

func (r *Repository) AdminAccessSummary(ctx context.Context) (AdminAccessSummary, error) {
	var result AdminAccessSummary
	err := r.db.QueryRowContext(ctx, adminAccessSummaryQuery(), adminAccessLocationDays).Scan(
		&result.TotalCalls,
		&result.SuccessCalls,
		&result.FailedCalls,
		&result.UniqueIPs,
		&result.ActiveCountries,
		&result.ActiveLocations,
	)
	result.WindowDays = adminAccessLocationDays
	return result, err
}

func adminAccessSummaryQuery() string {
	return `
		with filtered as (
			select r.country,r.province,r.city,r.isp,r.latitude,r.longitude,r.unique_ip_count,
			       r.call_count,r.success_count,r.failure_count
			from bp_call_region_hour r force index(idx_bp_call_region_hour_time)
			inner join bp_user u on u.id=r.user_id
			where r.bucket_start >= date_format(date_sub(now(), interval ? day),'%Y-%m-%d %H:00:00')
			  and ` + accountstatus.EffectiveStatusSQL("u") + `=1
		),
		location_groups as (
			select country,province,city,isp,latitude,longitude
			from filtered
			group by country,province,city,isp,latitude,longitude
		)
		select coalesce(sum(call_count),0),
		       coalesce(sum(success_count),0),
		       coalesce(sum(failure_count),0),
		       coalesce(sum(unique_ip_count),0),
		       count(distinct nullif(country,'')),
		       (select count(*) from location_groups)
		from filtered
	`
}

// AdminAccessLocations groups calls by the complete location tuple instead of
// selecting one IP. The result intentionally includes rows without coordinates
// so the administrator can still inspect provider/region coverage in the list.
// UniqueIPs is the compact rollup's additive estimate.
func (r *Repository) AdminAccessLocations(ctx context.Context) ([]AdminAccessLocation, error) {
	rows, err := r.db.QueryContext(ctx, adminAccessLocationsQuery(), adminAccessLocationDays, adminAccessLocationMax)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	locations := make([]AdminAccessLocation, 0)
	for rows.Next() {
		var location AdminAccessLocation
		var country, province, city, isp, latitude, longitude, region, firstSeen, lastSeen sql.NullString
		if err := rows.Scan(
			&country,
			&province,
			&city,
			&isp,
			&latitude,
			&longitude,
			&region,
			&location.CallCount,
			&location.SuccessCalls,
			&location.FailedCalls,
			&location.UniqueIPs,
			&location.AverageElapsedMs,
			&firstSeen,
			&lastSeen,
		); err != nil {
			return nil, err
		}
		location.Country = nullStringValue(country)
		location.Province = nullStringValue(province)
		location.City = nullStringValue(city)
		location.ISP = nullStringValue(isp)
		location.Latitude = nullStringValue(latitude)
		location.Longitude = nullStringValue(longitude)
		location.Region = nullStringValue(region)
		location.FirstSeen = nullStringValue(firstSeen)
		location.LastSeen = nullStringValue(lastSeen)
		locations = append(locations, location)
	}
	return locations, rows.Err()
}

func adminAccessLocationsQuery() string {
	return `
		select r.country,r.province,r.city,r.isp,
		       case when r.latitude=0 and r.longitude=0 then null else cast(r.latitude as char) end,
		       case when r.latitude=0 and r.longitude=0 then null else cast(r.longitude as char) end,
		       nullif(concat_ws(' ',nullif(r.country,''),nullif(r.province,''),nullif(r.city,'')),''),
		       sum(r.call_count),
		       coalesce(sum(r.success_count),0),
		       coalesce(sum(r.failure_count),0),
		       coalesce(sum(r.unique_ip_count),0),
		       cast(coalesce(sum(r.elapsed_sum)/nullif(sum(r.call_count),0),0) as unsigned),
		       date_format(min(r.first_seen),'%Y-%m-%d %H:%i:%s'),
		       date_format(max(r.last_seen),'%Y-%m-%d %H:%i:%s')
		from bp_call_region_hour r force index(idx_bp_call_region_hour_time)
		inner join bp_user u on u.id=r.user_id
		where r.bucket_start >= date_format(date_sub(now(), interval ? day),'%Y-%m-%d %H:00:00')
		  and ` + accountstatus.EffectiveStatusSQL("u") + `=1
		group by r.country,r.province,r.city,r.isp,r.latitude,r.longitude
		order by sum(r.call_count) desc,r.country,r.province,r.city,r.latitude,r.longitude
		limit ?
	`
}

func (r *Repository) fillRoles(ctx context.Context, record *UserRecord) error {
	rows, err := r.db.QueryContext(ctx, `
		select r.id, r.role_key, r.role_name
		from bp_role r
		inner join bp_user_role ur on ur.role_id = r.id
		where ur.user_id = ? and r.status = 1
		order by r.id
	`, record.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var key, name string
		if err := rows.Scan(&id, &key, &name); err != nil {
			return err
		}
		record.RoleIDs = append(record.RoleIDs, id)
		record.Roles = append(record.Roles, key)
		record.RoleNames = append(record.RoleNames, name)
	}
	return rows.Err()
}

func (r *Repository) fillSpecifiedFlags(ctx context.Context, record *UserRecord) error {
	err := r.db.QueryRowContext(ctx, `
		select coalesce(enabled, 0), coalesce(billable, 0)
		from bp_user_specified_response
		where user_id = ?
	`, record.ID).Scan(&record.SpecifiedResponseEnabled, &record.SpecifiedResponseBillable)
	if err == sql.ErrNoRows {
		return nil
	}
	return err
}

func scanUserRecord(scanner interface{ Scan(dest ...any) error }) (UserRecord, error) {
	var record UserRecord
	var banUntil, lastLogin, created sql.NullString
	var passwordSet int
	if err := scanner.Scan(
		&record.ID,
		&record.Username,
		&record.PasswordHash,
		&passwordSet,
		&record.RealName,
		&record.Avatar,
		&record.Email,
		&record.Mobile,
		&record.HomePath,
		&record.Balance,
		&record.Points,
		&record.Status,
		&record.BanPermanent,
		&banUntil,
		&record.BanReason,
		&record.TokenVersion,
		&record.PrimaryRoleID,
		&lastLogin,
		&created,
	); err != nil {
		return UserRecord{}, err
	}
	if banUntil.Valid {
		record.BanUntil = banUntil.String
	}
	if lastLogin.Valid {
		record.LastLoginTime = lastLogin.String
	}
	if created.Valid {
		record.CreateTime = created.String
	}
	record.PasswordSet = passwordSet == 1
	return record, nil
}

func toSystemUser(record UserRecord) SystemUser {
	banPermanent := record.BanPermanent
	banReason := record.BanReason
	banUntil := record.BanUntil
	if record.Status == accountstatus.Active {
		banPermanent = 0
		banReason = ""
		banUntil = ""
	}
	return SystemUser{
		Balance:                   record.Balance,
		BanPermanent:              banPermanent,
		BanReason:                 banReason,
		BanUntil:                  banUntil,
		CreateTime:                record.CreateTime,
		Email:                     record.Email,
		HomePath:                  record.HomePath,
		ID:                        fmt.Sprintf("%d", record.ID),
		LastLoginTime:             record.LastLoginTime,
		Mobile:                    record.Mobile,
		PrimaryRoleID:             record.PrimaryRoleID,
		RealName:                  record.RealName,
		RoleIDs:                   record.RoleIDs,
		RoleNames:                 record.RoleNames,
		Roles:                     record.Roles,
		Points:                    record.Points,
		SpecifiedResponseBillable: record.SpecifiedResponseBillable,
		SpecifiedResponseEnabled:  record.SpecifiedResponseEnabled,
		Status:                    record.Status,
		TotalRequests:             record.TotalRequests,
		Username:                  record.Username,
	}
}

func nullIfBlank(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func userBanValues(req SaveUserRequest, status int) (int, any, string) {
	if status != 0 {
		return 0, nil, ""
	}
	permanent := 1
	if req.BanPermanent != nil {
		permanent = *req.BanPermanent
	}
	if permanent != 0 {
		return 1, nil, strings.TrimSpace(req.BanReason)
	}
	return 0, nullIfBlank(req.BanUntil), strings.TrimSpace(req.BanReason)
}

func nullStringValue(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return strings.TrimSpace(value.String)
}

func defaultHomePath(value string) string {
	if strings.TrimSpace(value) == "" {
		return "/console/workspace"
	}
	return strings.TrimSpace(value)
}

type queryExecer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func requireExistingUser(ctx context.Context, query queryExecer, userID int64, result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected > 0 {
		return nil
	}
	var exists int
	if err := query.QueryRowContext(ctx, `select exists(select 1 from bp_user where id=?)`, userID).Scan(&exists); err != nil {
		return err
	}
	if exists != 1 {
		return sql.ErrNoRows
	}
	return nil
}

var ErrUserExists = errors.New("user already exists")
var ErrRoleNotFound = errors.New("role not found")
var ErrPrimaryRoleInvalid = errors.New("primary role is invalid")
var ErrLastAdmin = errors.New("last active admin cannot be removed")
var ErrUserHasFunds = errors.New("user still has balance or points")
var ErrUserHasPendingPayments = errors.New("user has pending payment orders")
var ErrUserHasBusinessHistory = errors.New("user has business or audit history")
var ErrInsufficientBalance = errors.New("balance would become negative")
var ErrInsufficientPoints = errors.New("points would become negative")
var ErrAssetCapacityExceeded = errors.New("asset capacity exceeded")
var ErrInvalidAssetState = errors.New("stored asset state is invalid")
