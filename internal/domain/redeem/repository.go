package redeem

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/pagination"

	"github.com/go-sql-driver/mysql"
)

// Repository 承载兑换码全部 SQL。并发安全不依赖应用锁：
// 码行锁（FOR UPDATE）+ 三个唯一键（user_code / user_type_lock / type_ip）
// + 条件核销，发奖镜像 payment 守卫式口径。
type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

// ---------------------------------------------------------------- 类型模板

type typeRow struct {
	ID             int64
	Code           string
	Name           string
	RewardType     int
	RewardPoints   int64
	RewardBalance  string
	RewardRefID    int64
	PerUserLimit   int
	PerIPLimit     int
	StartAt        time.Time
	EndAt          time.Time
	Status         int
	GeneratedCount int64
	RedeemedCount  int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

const typeColumns = `
	id, code, name, reward_type, reward_points, cast(reward_balance as char), reward_ref_id,
	per_user_limit, per_ip_limit, start_at, end_at, status, created_at, updated_at`

func (r *Repository) CreateType(ctx context.Context, item typeRow) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		insert into bp_redeem_code_type
			(code, name, reward_type, reward_points, reward_balance, reward_ref_id,
			 per_user_limit, per_ip_limit, start_at, end_at, status)
		values (?,?,?,?,?,?,?,?,?,?,?)
	`, item.Code, item.Name, item.RewardType, item.RewardPoints, item.RewardBalance,
		item.RewardRefID, item.PerUserLimit, item.PerIPLimit, item.StartAt, item.EndAt, item.Status)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (r *Repository) UpdateType(ctx context.Context, id int64, item typeRow) error {
	result, err := r.db.ExecContext(ctx, `
		update bp_redeem_code_type
		set name=?, start_at=?, end_at=?, status=?
		where id=?
	`, item.Name, item.StartAt, item.EndAt, item.Status, id)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

func (r *Repository) UpdateTypeStatus(ctx context.Context, id int64, status int) error {
	result, err := r.db.ExecContext(ctx, `
		update bp_redeem_code_type set status=? where id=?
	`, status, id)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

func (r *Repository) GetType(ctx context.Context, id int64) (typeRow, error) {
	var item typeRow
	err := r.db.QueryRowContext(ctx, `select `+typeColumns+` from bp_redeem_code_type where id=?`, id).
		Scan(&item.ID, &item.Code, &item.Name, &item.RewardType, &item.RewardPoints, &item.RewardBalance,
			&item.RewardRefID, &item.PerUserLimit, &item.PerIPLimit, &item.StartAt, &item.EndAt,
			&item.Status, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return typeRow{}, err
	}
	return item, nil
}

// GetTypeByCode 按业务标识（如 NEWBIE）解析类型。开放 API 的外部调用方
// （AstrBot 活动插件）在配置里写的是标识而不是数据库自增 ID，二者必须
// 都能用；标识唯一性由建表唯一索引保证，limit 1 只是防御性冗余。
func (r *Repository) GetTypeByCode(ctx context.Context, code string) (typeRow, error) {
	var item typeRow
	err := r.db.QueryRowContext(ctx, `select `+typeColumns+` from bp_redeem_code_type where code=? limit 1`, code).
		Scan(&item.ID, &item.Code, &item.Name, &item.RewardType, &item.RewardPoints, &item.RewardBalance,
			&item.RewardRefID, &item.PerUserLimit, &item.PerIPLimit, &item.StartAt, &item.EndAt,
			&item.Status, &item.CreatedAt, &item.UpdatedAt)
	if err != nil {
		return typeRow{}, err
	}
	return item, nil
}

// ListTypes 返回类型分页与聚合计数（生成数/已兑换数走关联子查询，
// 单类型 10 万码上限内子查询成本可忽略）。
func (r *Repository) ListTypes(ctx context.Context, params ListParams) (PageResult[typeRow], error) {
	page, size := normalizePage(params.Page, params.PageSize)
	where := "where 1=1"
	var args []any
	if params.Keyword != "" {
		where += " and (t.code like ? or t.name like ?)"
		like := "%" + params.Keyword + "%"
		args = append(args, like, like)
	}
	if params.Status != nil {
		where += " and t.status=?"
		args = append(args, *params.Status)
	}
	var (
		total int64
		items []typeRow
	)
	countArgs := append([]any(nil), args...)
	listArgs := append(append([]any(nil), args...), size, pagination.Offset(page, size))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, "select count(*) from bp_redeem_code_type t "+where, countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, `
				select `+typeColumns+`,
					(select count(*) from bp_redeem_code c where c.type_id=t.id),
					(select count(*) from bp_redeem_code c where c.type_id=t.id and c.status=2)
				from bp_redeem_code_type t `+where+`
				order by t.id desc limit ? offset ?
			`, listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items = make([]typeRow, 0, size)
			for rows.Next() {
				var item typeRow
				if err := rows.Scan(&item.ID, &item.Code, &item.Name, &item.RewardType, &item.RewardPoints,
					&item.RewardBalance, &item.RewardRefID, &item.PerUserLimit, &item.PerIPLimit,
					&item.StartAt, &item.EndAt, &item.Status, &item.CreatedAt, &item.UpdatedAt,
					&item.GeneratedCount, &item.RedeemedCount); err != nil {
					return err
				}
				items = append(items, item)
			}
			return rows.Err()
		},
	)
	if err != nil {
		return PageResult[typeRow]{}, err
	}
	return PageResult[typeRow]{Items: items, Total: total}, nil
}

// TypeHasCodes 判断类型是否已生成过码（生成过即冻结奖励/限领字段，M-2）。
func (r *Repository) TypeHasCodes(ctx context.Context, typeID int64) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `
		select exists(select 1 from bp_redeem_code where type_id=? limit 1)
	`, typeID).Scan(&exists)
	return exists, err
}

func (r *Repository) CountCodesByType(ctx context.Context, typeID int64) (int64, error) {
	var total int64
	err := r.db.QueryRowContext(ctx, `
		select count(*) from bp_redeem_code where type_id=?
	`, typeID).Scan(&total)
	return total, err
}

// TypeCodeTaken 检查类型标识是否被他类型占用（排除自身）。
func (r *Repository) TypeCodeTaken(ctx context.Context, code string, excludeID int64) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `
		select exists(select 1 from bp_redeem_code_type where code=? and id<>? limit 1)
	`, code, excludeID).Scan(&exists)
	return exists, err
}

// ---------------------------------------------------------------- 套餐引用

// GetGlobalPackageRef 读取全站套餐引用（类型保存校验用）。
func (r *Repository) GetGlobalPackageRef(ctx context.Context, refID int64) (packageRef, error) {
	var ref packageRef
	var status int
	err := r.db.QueryRowContext(ctx, `
		select name, valid_days, daily_limit, qps_limit, status
		from bp_package_global where id=?
	`, refID).Scan(&ref.Name, &ref.ValidDays, &ref.DailyLimit, &ref.QPSLimit, &status)
	if err != nil {
		return packageRef{}, err
	}
	if status != 1 {
		return packageRef{}, errPackageDisabled
	}
	return ref, nil
}

// GetInterfaceSpecRef 读取接口套餐规格引用（类型保存校验用，双启用校验）。
func (r *Repository) GetInterfaceSpecRef(ctx context.Context, refID int64) (packageRef, error) {
	var ref packageRef
	var specStatus, pkgStatus int
	err := r.db.QueryRowContext(ctx, `
		select p.name, s.name, s.valid_days, s.daily_limit, s.qps_limit, p.interface_id, p.id, s.status, p.status
		from bp_package_interface_spec s
		join bp_package_interface p on p.id = s.package_id
		where s.id=?
	`, refID).Scan(&ref.Name, &ref.SpecName, &ref.ValidDays, &ref.DailyLimit, &ref.QPSLimit,
		&ref.InterfaceID, &ref.PackageID, &specStatus, &pkgStatus)
	if err != nil {
		return packageRef{}, err
	}
	if specStatus != 1 || pkgStatus != 1 {
		return packageRef{}, errPackageDisabled
	}
	return ref, nil
}

// ---------------------------------------------------------------- 批次与码

func (r *Repository) CreateBatch(ctx context.Context, batch batchRow) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		insert into bp_redeem_generation_batch
			(type_id, count, per_user_limit_snap, per_ip_limit_snap, campaign_ref, source, creator_key_id, created_by)
		values (?,?,?,?,?,?,?,?)
	`, batch.TypeID, batch.Count, batch.PerUserLimitSnap, batch.PerIPLimitSnap,
		nullString(batch.CampaignRef), batch.Source, nullInt64(batch.CreatorKeyID), nullInt64(batch.CreatedBy))
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

type batchRow struct {
	TypeID           int64
	Count            int
	PerUserLimitSnap int
	PerIPLimitSnap   int
	CampaignRef      string
	Source           int
	CreatorKeyID     int64
	CreatedBy        int64
}

// InsertCodes 批量落码。ON DUPLICATE KEY UPDATE id=id 让撞唯一键的行
// affected=0（不报错也不改写），返回真实新插入行数；调用方按差额补生成。
func (r *Repository) InsertCodes(ctx context.Context, typeID, batchID int64, plains []string) (int64, error) {
	const chunk = 500
	var inserted int64
	for start := 0; start < len(plains); start += chunk {
		end := start + chunk
		if end > len(plains) {
			end = len(plains)
		}
		part := plains[start:end]
		placeholders := make([]string, 0, len(part))
		args := make([]any, 0, len(part)*3)
		for _, plain := range part {
			placeholders = append(placeholders, "(?,?,?)")
			args = append(args, typeID, plain, batchID)
		}
		result, err := r.db.ExecContext(ctx, `
			insert into bp_redeem_code (type_id, code_plain, batch_id) values `+
			strings.Join(placeholders, ",")+`
			on duplicate key update id=id
		`, args...)
		if err != nil {
			return inserted, err
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return inserted, err
		}
		inserted += affected
	}
	return inserted, nil
}

// FirstCodesOfBatch 取批次前 N 个明文码（生成审计的「前 N 个码」）。
func (r *Repository) FirstCodesOfBatch(ctx context.Context, batchID int64, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		select code_plain from bp_redeem_code where batch_id=? order by id asc limit ?
	`, batchID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	codes := make([]string, 0, limit)
	for rows.Next() {
		var plain string
		if err := rows.Scan(&plain); err != nil {
			return nil, err
		}
		codes = append(codes, plain)
	}
	return codes, rows.Err()
}

type codeRow struct {
	ID        int64
	TypeID    int64
	TypeName  string
	CodePlain string
	Status    int
	BatchID   sql.NullInt64
	Source    int
	UsedBy    sql.NullInt64
	UsedAt    sql.NullTime
	CreatedAt time.Time
}

func (r *Repository) ListCodes(ctx context.Context, params ListParams) (PageResult[codeRow], error) {
	page, size := normalizePage(params.Page, params.PageSize)
	where := "where 1=1"
	var args []any
	if params.TypeID > 0 {
		where += " and c.type_id=?"
		args = append(args, params.TypeID)
	}
	if params.Status != nil {
		where += " and c.status=?"
		args = append(args, *params.Status)
	}
	if params.Source != nil {
		where += " and b.source=?"
		args = append(args, *params.Source)
	}
	if params.Keyword != "" {
		where += " and c.code_plain like ?"
		args = append(args, "%"+strings.ToUpper(params.Keyword)+"%")
	}
	var (
		total int64
		items []codeRow
	)
	countArgs := append([]any(nil), args...)
	listArgs := append(append([]any(nil), args...), size, pagination.Offset(page, size))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, `
				select count(*) from bp_redeem_code c
				left join bp_redeem_generation_batch b on b.id=c.batch_id `+where, countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, `
				select c.id, c.type_id, t.name, c.code_plain, c.status, c.batch_id,
				       coalesce(b.source, 1), c.used_by, c.used_at, c.created_at
				from bp_redeem_code c
				join bp_redeem_code_type t on t.id=c.type_id
				left join bp_redeem_generation_batch b on b.id=c.batch_id `+where+`
				order by c.id desc limit ? offset ?
			`, listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items = make([]codeRow, 0, size)
			for rows.Next() {
				var item codeRow
				if err := rows.Scan(&item.ID, &item.TypeID, &item.TypeName, &item.CodePlain, &item.Status,
					&item.BatchID, &item.Source, &item.UsedBy, &item.UsedAt, &item.CreatedAt); err != nil {
					return err
				}
				items = append(items, item)
			}
			return rows.Err()
		},
	)
	if err != nil {
		return PageResult[codeRow]{}, err
	}
	return PageResult[codeRow]{Items: items, Total: total}, nil
}

// VoidCode 作废单码：仅未用可作废（条件推进，affected!=1 即并发/状态不符）。
func (r *Repository) VoidCode(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, `
		update bp_redeem_code set status=0 where id=? and status=1
	`, id)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// ListCodePlainsForExport 导出全量明文（独立 redeem:code:export 权限的调用方）。
func (r *Repository) ListCodePlainsForExport(ctx context.Context, typeID int64) ([]codeRow, error) {
	rows, err := r.db.QueryContext(ctx, `
		select c.id, c.type_id, t.name, c.code_plain, c.status, c.batch_id,
		       coalesce(b.source, 1), c.used_by, c.used_at, c.created_at
		from bp_redeem_code c
		join bp_redeem_code_type t on t.id=c.type_id
		left join bp_redeem_generation_batch b on b.id=c.batch_id
		where c.type_id=?
		order by c.id asc
	`, typeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]codeRow, 0, 128)
	for rows.Next() {
		var item codeRow
		if err := rows.Scan(&item.ID, &item.TypeID, &item.TypeName, &item.CodePlain, &item.Status,
			&item.BatchID, &item.Source, &item.UsedBy, &item.UsedAt, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ---------------------------------------------------------------- 兑换记录

type recordRow struct {
	ID            int64
	UserID        int64
	Username      string
	CodePlain     string
	TypeID        int64
	TypeName      string
	RewardType    int
	RewardPoints  int64
	RewardBalance string
	RewardName    sql.NullString
	ValidDays     sql.NullInt64
	Source        int
	CreatedAt     time.Time
}

// ListRecords 兑换记录分页。scopeUser>0 时限定本人（用户端视图）。
func (r *Repository) ListRecords(ctx context.Context, params ListParams, scopeUser int64) (PageResult[recordRow], error) {
	page, size := normalizePage(params.Page, params.PageSize)
	where := "where 1=1"
	var args []any
	if scopeUser > 0 {
		where += " and r.user_id=?"
		args = append(args, scopeUser)
	}
	if params.TypeID > 0 {
		where += " and r.type_id=?"
		args = append(args, params.TypeID)
	}
	if params.Source != nil {
		where += " and r.source=?"
		args = append(args, *params.Source)
	}
	if params.Keyword != "" {
		where += " and (c.code_plain like ? or u.username like ?)"
		like := "%" + params.Keyword + "%"
		args = append(args, like, like)
	}
	var (
		total int64
		items []recordRow
	)
	countArgs := append([]any(nil), args...)
	listArgs := append(append([]any(nil), args...), size, pagination.Offset(page, size))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, `
				select count(*) from bp_redeem_record r
				join bp_redeem_code c on c.id=r.code_id
				join bp_user u on u.id=r.user_id `+where, countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, `
				select r.id, r.user_id, u.username, c.code_plain, r.type_id, t.name,
				       r.reward_type, r.reward_points, cast(r.reward_balance as char),
				       r.reward_name, r.valid_days, r.source, r.created_at
				from bp_redeem_record r
				join bp_redeem_code c on c.id=r.code_id
				join bp_redeem_code_type t on t.id=r.type_id
				join bp_user u on u.id=r.user_id `+where+`
				order by r.id desc limit ? offset ?
			`, listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items = make([]recordRow, 0, size)
			for rows.Next() {
				var item recordRow
				if err := rows.Scan(&item.ID, &item.UserID, &item.Username, &item.CodePlain, &item.TypeID,
					&item.TypeName, &item.RewardType, &item.RewardPoints, &item.RewardBalance,
					&item.RewardName, &item.ValidDays, &item.Source, &item.CreatedAt); err != nil {
					return err
				}
				items = append(items, item)
			}
			return rows.Err()
		},
	)
	if err != nil {
		return PageResult[recordRow]{}, err
	}
	return PageResult[recordRow]{Items: items, Total: total}, nil
}

// ---------------------------------------------------------------- 兑换事务

// lockedCode 是行锁读出的码+类型+批次快照（限领/发放决策的全部输入）。
// 套餐名与有效期随 JOIN 实时读取（行锁下无竞态），供记录快照使用。
type lockedCode struct {
	CodeID        int64
	TypeID        int64
	CodeStatus    int
	TypeStatus    int
	RewardType    int
	RewardPoints  int64
	RewardBalance string
	RewardRefID   int64
	RewardRefName sql.NullString
	// RefPackageID 是接口套餐规格所属的套餐 ID（reward_type=4 时有效），
	// 用于按请求语言重组「套餐名-规格名」。
	RefPackageID int64
	PerUserLimit int
	PerIPLimit   int
	ValidDays    sql.NullInt64
	BatchID      sql.NullInt64
	StartAt      time.Time
	EndAt        time.Time
}

// LockCodeByPlain 行锁读码（JOIN 类型、批次快照与套餐引用）。
// 码不存在返回 sql.ErrNoRows。
func (r *Repository) LockCodeByPlain(ctx context.Context, tx *sql.Tx, plain string) (lockedCode, error) {
	var item lockedCode
	err := tx.QueryRowContext(ctx, `
		select c.id, c.type_id, c.status, t.status, t.reward_type, t.reward_points,
		       cast(t.reward_balance as char), t.reward_ref_id,
		       case when t.reward_type=3 then g.name
		            when t.reward_type=4 then concat(p.name,'-',s.name) end,
		       case when t.reward_type=4 then s.package_id else 0 end,
		       coalesce(b.per_user_limit_snap, t.per_user_limit),
		       coalesce(b.per_ip_limit_snap, t.per_ip_limit),
		       case when t.reward_type=3 then g.valid_days
		            when t.reward_type=4 then s.valid_days end,
		       c.batch_id,
		       t.start_at, t.end_at
		from bp_redeem_code c
		join bp_redeem_code_type t on t.id=c.type_id
		left join bp_redeem_generation_batch b on b.id=c.batch_id
		left join bp_package_global g on g.id=t.reward_ref_id and t.reward_type=3
		left join bp_package_interface_spec s on s.id=t.reward_ref_id and t.reward_type=4
		left join bp_package_interface p on p.id=s.package_id
		where c.code_plain=? for update
	`, plain).Scan(&item.CodeID, &item.TypeID, &item.CodeStatus, &item.TypeStatus, &item.RewardType,
		&item.RewardPoints, &item.RewardBalance, &item.RewardRefID, &item.RewardRefName,
		&item.RefPackageID,
		&item.PerUserLimit, &item.PerIPLimit, &item.ValidDays, &item.BatchID, &item.StartAt, &item.EndAt)
	if err != nil {
		return lockedCode{}, err
	}
	return item, nil
}

// CountUserTypeRecords 统计账号在某类型下的历史兑换数（限 N 次友好预检；
// 并发由 uk_user_type_lock 兜底）。
func (r *Repository) CountUserTypeRecords(ctx context.Context, tx *sql.Tx, userID, typeID int64) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `
		select count(*) from bp_redeem_record where user_id=? and type_id=?
	`, userID, typeID).Scan(&count)
	return count, err
}

// LockUserRow 锁用户行：同一账号的兑换事务串行化（限 N 次 COUNT 的竞态解）。
func (r *Repository) LockUserRow(ctx context.Context, tx *sql.Tx, userID int64) error {
	var id int64
	return tx.QueryRowContext(ctx, `select id from bp_user where id=? for update`, userID).Scan(&id)
}

// IPRecordExists 检查同 IP 同类型是否已有兑换（per_ip_limit=1 友好预检）。
func (r *Repository) IPRecordExists(ctx context.Context, tx *sql.Tx, typeID int64, clientIP string) (bool, error) {
	var exists bool
	err := tx.QueryRowContext(ctx, `
		select exists(select 1 from bp_redeem_record where type_id=? and ip_lock=? limit 1)
	`, typeID, clientIP).Scan(&exists)
	return exists, err
}

// BumpIPCounter 每 IP 计数器 upsert（行锁持至提交，同 IP 并发串行化），
// 返回 upsert 后的计数值。
func (r *Repository) BumpIPCounter(ctx context.Context, tx *sql.Tx, typeID int64, clientIP string) (int64, error) {
	if _, err := tx.ExecContext(ctx, `
		insert into bp_redeem_ip_counter (type_id, client_ip, used)
		values (?,?,1)
		on duplicate key update used=used+1
	`, typeID, clientIP); err != nil {
		return 0, err
	}
	var used int64
	err := tx.QueryRowContext(ctx, `
		select used from bp_redeem_ip_counter where type_id=? and client_ip=?
	`, typeID, clientIP).Scan(&used)
	return used, err
}

// AddPointsGuarded 守卫式加点：points<=剩余容量才放行，0 行受影响=超限。
func (r *Repository) AddPointsGuarded(ctx context.Context, tx *sql.Tx, userID, points int64) error {
	result, err := tx.ExecContext(ctx, `
		update bp_user set points=points+? where id=? and points<=?
	`, points, userID, maxUserPointBalance-points)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// AddBalanceGuarded 守卫式加余额（与 payment applySettlement 同口径）。
func (r *Repository) AddBalanceGuarded(ctx context.Context, tx *sql.Tx, userID int64, amount string) error {
	result, err := tx.ExecContext(ctx, `
		update bp_user
		set balance=balance+?
		where id=? and balance<=cast(? as decimal(18,4))-cast(? as decimal(18,4))
	`, amount, userID, maxUserBalanceText, amount)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// InsertWalletTx 写钱包流水（biz_type=REDEEM_CODE），返回变动后余额/点数。
// INSERT...SELECT 原子读 after 值（与 payment insertWalletTx 完全同形：
// ExecContext + requireAffected，绝不用 QueryRow 扫 INSERT——INSERT 无
// 结果集，Scan 必返回 sql.ErrNoRows）；after 展示值再于同事务内读回。
func (r *Repository) InsertWalletTx(ctx context.Context, tx *sql.Tx, userID int64, bizID int64, amount string, points int64, remark string) (string, int64, error) {
	result, err := tx.ExecContext(ctx, `
		insert into bp_wallet_transaction(user_id,biz_type,biz_id,amount,points,balance_after,points_after,remark)
		select id,?,?,?,?,balance,points,? from bp_user where id=?
	`, walletBizType, nullInt64(bizID), amount, points, remark, userID)
	if err != nil {
		return "", 0, err
	}
	if err := requireAffected(result); err != nil {
		return "", 0, err
	}
	var balanceAfter string
	var pointsAfter int64
	if err := tx.QueryRowContext(ctx, `
		select cast(balance as char), points from bp_user where id=?
	`, userID).Scan(&balanceAfter, &pointsAfter); err != nil {
		return "", 0, err
	}
	return balanceAfter, pointsAfter, nil
}

// InsertGlobalPackage 全站套餐到账（INSERT...SELECT 原子校验启用与有效期，
// affected=0 ⇒ 套餐已停用/不允许参与兑换）。
// total_limit/count_mode 随模板直接快照，与购买/支付结算口径一致。
func (r *Repository) InsertGlobalPackage(ctx context.Context, tx *sql.Tx, userID, refID int64) error {
	result, err := tx.ExecContext(ctx, `
		insert into bp_user_package_global(user_id,package_id,package_name,daily_limit,total_limit,count_mode,qps_limit,start_at,end_at,status)
		select ?,id,name,daily_limit,total_limit,count_mode,qps_limit,now(),date_add(now(), interval valid_days day),1
		from bp_package_global where id=? and status=1 and valid_days>0
	`, userID, refID)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// InsertInterfacePackage 接口套餐规格到账（JOIN 一步取齐，双启用+有效期
// 原子校验，镜像 payment applySettlement INTERFACE_PACKAGE 分支）。
func (r *Repository) InsertInterfacePackage(ctx context.Context, tx *sql.Tx, userID, refID int64) error {
	result, err := tx.ExecContext(ctx, `
		insert into bp_user_package_interface(user_id,interface_id,package_id,spec_id,package_name,spec_name,daily_limit,total_limit,count_mode,qps_limit,start_at,end_at,status)
		select ?,p.interface_id,p.id,s.id,p.name,s.name,s.daily_limit,s.total_limit,s.count_mode,s.qps_limit,now(),date_add(now(), interval s.valid_days day),1
		from bp_package_interface_spec s
		join bp_package_interface p on p.id=s.package_id
		where s.id=? and s.status=1 and p.status=1 and s.valid_days>0
	`, userID, refID)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// InsertRecord 落兑换记录。撞 uk_user_type_lock / uk_type_ip 时返回
// 1062 错误，由 service 映射为友好限领文案（并发重复的 DB 层兜底）。
func (r *Repository) InsertRecord(ctx context.Context, tx *sql.Tx, rec recordInsert) error {
	_, err := tx.ExecContext(ctx, `
		insert into bp_redeem_record
			(user_id, code_id, type_id, type_lock, ip_lock, reward_type, reward_points,
			 reward_balance, reward_name, valid_days, client_ip, batch_id, source)
		values (?,?,?,?,?,?,?,?,?,?,?,?,?)
	`, rec.UserID, rec.CodeID, rec.TypeID, nullInt64(rec.TypeLock), nullString(rec.IPLock),
		rec.RewardType, rec.RewardPoints, rec.RewardBalance, nullString(rec.RewardName),
		nullInt64(rec.ValidDays), nullString(rec.ClientIP), nullInt64(rec.BatchID), rec.Source)
	return err
}

type recordInsert struct {
	UserID        int64
	CodeID        int64
	TypeID        int64
	TypeLock      int64
	IPLock        string
	RewardType    int
	RewardPoints  int64
	RewardBalance string
	RewardName    string
	ValidDays     int64
	ClientIP      string
	BatchID       int64
	Source        int
}

// ConsumeCode 条件核销：status 1→2，affected!=1 即并发已用，回滚。
func (r *Repository) ConsumeCode(ctx context.Context, tx *sql.Tx, codeID, userID int64) error {
	result, err := tx.ExecContext(ctx, `
		update bp_redeem_code set status=2, used_by=?, used_at=now() where id=? and status=1
	`, userID, codeID)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// ---------------------------------------------------------------- 工具

// IsDuplicateKey 判断是否唯一键冲突（可附带索引名精确匹配）。
func IsDuplicateKey(err error, indexName string) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
		return false
	}
	return indexName == "" || strings.Contains(mysqlErr.Message, indexName)
}

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

var errNotFound = errors.New("redeem: row not found")

func nullString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func nullInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func normalizePage(page, pageSize int) (int, int) {
	return pagination.Normalize(page, pageSize, 100)
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
