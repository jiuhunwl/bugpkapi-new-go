package packagepkg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"bugpk-api/internal/pkg/dbx"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/pagination"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/go-sql-driver/mysql"
)

type Repository struct{ db *sql.DB }

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }

func (r *Repository) ListGlobal(ctx context.Context, params ListParams, onlyEnabled bool, viewer *PackageViewer) (PageResult[GlobalPackage], error) {
	page, size := normalizePage(params.Page, params.PageSize)
	where, args := packageWhere("name", params, onlyEnabled)
	if onlyEnabled {
		if clause, clauseArgs := packageVisibilityClause("GLOBAL", "bp_package_global.id", "", viewer); clause != "" {
			where += " and " + clause
			args = append(args, clauseArgs...)
		}
	}
	var (
		total      int64
		items      []GlobalPackage
		packageIDs []int64
	)
	countArgs := append([]any(nil), args...)
	listArgs := append(append([]any(nil), args...), size, pagination.Offset(page, size))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, "select count(*) from bp_package_global "+where, countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, `
				select id, name, cast(price as char), valid_days, daily_limit, total_limit, count_mode, qps_limit, status, coalesce(remark, ''),
				       coalesce(stock_limit,0), coalesce(stock_used,0), coalesce(per_user_limit,0),
				       coalesce(date_format(sale_start_at,'%Y-%m-%d %H:%i:%s'),''),
				       coalesce(date_format(sale_end_at,'%Y-%m-%d %H:%i:%s'),''),
				       coalesce(role_restricted,0), coalesce(role_visible_only,0), coalesce(hidden,0),
				       date_format(created_at,'%Y-%m-%d %H:%i:%s'), date_format(updated_at,'%Y-%m-%d %H:%i:%s')
				from bp_package_global `+where+`
				order by price asc, id desc limit ? offset ?
			`, listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items = make([]GlobalPackage, 0, size)
			packageIDs = make([]int64, 0, size)
			for rows.Next() {
				var item GlobalPackage
				var id int64
				if err := rows.Scan(&id, &item.Name, &item.Price, &item.ValidDays, &item.DailyLimit, &item.TotalLimit, &item.CountMode, &item.QPSLimit, &item.Status, &item.Remark, &item.StockLimit, &item.StockUsed, &item.PerUserLimit, &item.SaleStartAt, &item.SaleEndAt, &item.RoleRestricted, &item.RoleVisibleOnly, &item.Hidden, &item.CreateTime, &item.UpdateTime); err != nil {
					return err
				}
				item.ID = strconv.FormatInt(id, 10)
				items = append(items, item)
				packageIDs = append(packageIDs, id)
			}
			return rows.Err()
		},
	)
	if err != nil {
		return PageResult[GlobalPackage]{}, err
	}
	assignments, err := r.packageRoleAssignments(ctx, "GLOBAL", packageIDs)
	if err != nil {
		return PageResult[GlobalPackage]{}, err
	}
	for index := range items {
		assignment := assignments[packageIDs[index]]
		items[index].RoleIDs = assignment.RoleIDs
		items[index].RoleNames = assignment.RoleNames
		items[index].CanPurchase = viewer.Allows(items[index].RoleRestricted, assignment.RoleIDs)
	}
	return PageResult[GlobalPackage]{Items: items, Total: total}, nil
}

func packageWhere(nameColumn string, params ListParams, onlyEnabled bool) (string, []any) {
	where := []string{"where 1=1"}
	args := []any{}
	if onlyEnabled {
		where = append(where, "status = 1", "hidden = 0")
	}
	if params.Status != nil {
		where = append(where, "status = ?")
		args = append(args, *params.Status)
	}
	if strings.TrimSpace(params.Keyword) != "" {
		where = append(where, nameColumn+" like ?")
		args = append(args, "%"+strings.TrimSpace(params.Keyword)+"%")
	}
	return strings.Join(where, " and "), args
}

func (r *Repository) SaveGlobal(ctx context.Context, id int64, item GlobalPackage) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if id == 0 {
		result, err := tx.ExecContext(ctx, `
			insert into bp_package_global(name, price, valid_days, daily_limit, total_limit, count_mode, qps_limit, status, remark,
			                              stock_limit, per_user_limit, sale_start_at, sale_end_at,
			                              role_restricted, role_visible_only, hidden, renewal_enabled)
			values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		`, item.Name, item.Price, item.ValidDays, item.DailyLimit, item.TotalLimit, normalizeCountMode(item.CountMode), item.QPSLimit, item.Status, nullIfBlank(item.Remark),
			item.StockLimit, item.PerUserLimit, nullIfBlank(item.SaleStartAt), nullIfBlank(item.SaleEndAt),
			item.RoleRestricted, item.RoleVisibleOnly, item.Hidden, item.RenewalEnabled)
		if err != nil {
			return err
		}
		if err := requireAffected(result); err != nil {
			return err
		}
		packageID, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if err := replacePackageRolesTx(ctx, tx, "GLOBAL", packageID, item.RoleIDs); err != nil {
			return err
		}
		return tx.Commit()
	}
	var stockUsed int
	if err := tx.QueryRowContext(ctx, "select stock_used from bp_package_global where id=? for update", id).Scan(&stockUsed); err != nil {
		return err
	}
	if item.StockLimit > 0 && item.StockLimit < stockUsed {
		return ErrPackageStockBelowUsed
	}
	if _, err := tx.ExecContext(ctx, `
		update bp_package_global
		set name=?, price=?, valid_days=?, daily_limit=?, total_limit=?, count_mode=?, qps_limit=?, status=?, remark=?,
		    stock_limit=?, per_user_limit=?, sale_start_at=?, sale_end_at=?,
		    role_restricted=?, role_visible_only=?, hidden=?, renewal_enabled=?
		where id=?
	`, item.Name, item.Price, item.ValidDays, item.DailyLimit, item.TotalLimit, normalizeCountMode(item.CountMode), item.QPSLimit, item.Status, nullIfBlank(item.Remark),
		item.StockLimit, item.PerUserLimit, nullIfBlank(item.SaleStartAt), nullIfBlank(item.SaleEndAt),
		item.RoleRestricted, item.RoleVisibleOnly, item.Hidden, item.RenewalEnabled, id); err != nil {
		return err
	}
	if err := replacePackageRolesTx(ctx, tx, "GLOBAL", id, item.RoleIDs); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteGlobal 删除全站套餐。force=false 时只要存在购买、支付或钱包流水即拒绝；
// force=true 时跳过流水校验并级联删除用户权益行，支付与钱包流水作为财务凭证保留
// （其中的 target_id 将指向已不存在的套餐，仅供历史审计）。
func (r *Repository) DeleteGlobal(ctx context.Context, id int64, force bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockPackageRow(ctx, tx, "global", id); err != nil {
		return err
	}
	if !force {
		var referenced int
		if err := tx.QueryRowContext(ctx, `
			select exists(select 1 from bp_user_package_global where package_id=? limit 1)
			    or exists(select 1 from bp_payment_order where order_type='GLOBAL_PACKAGE' and target_id=? limit 1)
			    or exists(select 1 from bp_wallet_transaction where biz_type='PURCHASE_GLOBAL_PACKAGE' and biz_id=? limit 1)
		`, id, id, id).Scan(&referenced); err != nil {
			return err
		}
		if referenced != 0 {
			return ErrPackageHasBusinessHistory
		}
	} else {
		if _, err := tx.ExecContext(ctx, "delete from bp_user_package_global where package_id=?", id); err != nil {
			return err
		}
	}
	result, err := tx.ExecContext(ctx, "delete from bp_package_global where id=?", id)
	if err != nil {
		return err
	}
	if err := requireAffected(result); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_package_role where package_type='GLOBAL' and package_id=?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_package_visible_role where package_type='GLOBAL' and package_id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ListPoint(ctx context.Context, params ListParams, onlyEnabled bool, viewer *PackageViewer) (PageResult[PointPackage], error) {
	page, size := normalizePage(params.Page, params.PageSize)
	where, args := packageWhere("name", params, onlyEnabled)
	if onlyEnabled {
		if clause, clauseArgs := packageVisibilityClause("POINT", "bp_package_point.id", "", viewer); clause != "" {
			where += " and " + clause
			args = append(args, clauseArgs...)
		}
	}
	var (
		total      int64
		items      []PointPackage
		packageIDs []int64
	)
	countArgs := append([]any(nil), args...)
	listArgs := append(append([]any(nil), args...), size, pagination.Offset(page, size))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, "select count(*) from bp_package_point "+where, countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, `
				select id,name,cast(price as char),points,status,coalesce(remark,''),
				       coalesce(stock_limit,0),coalesce(stock_used,0),coalesce(per_user_limit,0),
				       coalesce(date_format(sale_start_at,'%Y-%m-%d %H:%i:%s'),''),
				       coalesce(date_format(sale_end_at,'%Y-%m-%d %H:%i:%s'),''),
				       coalesce(role_restricted,0), coalesce(role_visible_only,0), coalesce(hidden,0),
				       date_format(created_at,'%Y-%m-%d %H:%i:%s'), date_format(updated_at,'%Y-%m-%d %H:%i:%s')
				from bp_package_point `+where+` order by price asc,id desc limit ? offset ?
			`, listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items = make([]PointPackage, 0, size)
			packageIDs = make([]int64, 0, size)
			for rows.Next() {
				var item PointPackage
				var id int64
				if err := rows.Scan(&id, &item.Name, &item.Price, &item.Points, &item.Status, &item.Remark,
					&item.StockLimit, &item.StockUsed, &item.PerUserLimit, &item.SaleStartAt, &item.SaleEndAt,
					&item.RoleRestricted, &item.RoleVisibleOnly, &item.Hidden, &item.CreateTime, &item.UpdateTime); err != nil {
					return err
				}
				item.ID = strconv.FormatInt(id, 10)
				item.PointAmount = item.Points
				items = append(items, item)
				packageIDs = append(packageIDs, id)
			}
			return rows.Err()
		},
	)
	if err != nil {
		return PageResult[PointPackage]{}, err
	}
	assignments, err := r.packageRoleAssignments(ctx, "POINT", packageIDs)
	if err != nil {
		return PageResult[PointPackage]{}, err
	}
	for index := range items {
		assignment := assignments[packageIDs[index]]
		items[index].RoleIDs = assignment.RoleIDs
		items[index].RoleNames = assignment.RoleNames
		items[index].CanPurchase = viewer.Allows(items[index].RoleRestricted, assignment.RoleIDs)
	}
	return PageResult[PointPackage]{Items: items, Total: total}, nil
}

func (r *Repository) SavePoint(ctx context.Context, id int64, item PointPackage) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if id == 0 {
		result, err := tx.ExecContext(ctx, `
			insert into bp_package_point(name, price, points, status, remark, stock_limit, per_user_limit, sale_start_at, sale_end_at,
			                             role_restricted, role_visible_only, hidden)
			values(?,?,?,?,?,?,?,?,?,?,?,?)
		`, item.Name, item.Price, item.Points, item.Status, nullIfBlank(item.Remark),
			item.StockLimit, item.PerUserLimit, nullIfBlank(item.SaleStartAt), nullIfBlank(item.SaleEndAt),
			item.RoleRestricted, item.RoleVisibleOnly, item.Hidden)
		if err != nil {
			return err
		}
		if err := requireAffected(result); err != nil {
			return err
		}
		packageID, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if err := replacePackageRolesTx(ctx, tx, "POINT", packageID, item.RoleIDs); err != nil {
			return err
		}
		return tx.Commit()
	}
	var stockUsed int
	if err := tx.QueryRowContext(ctx, "select stock_used from bp_package_point where id=? for update", id).Scan(&stockUsed); err != nil {
		return err
	}
	if item.StockLimit > 0 && item.StockLimit < stockUsed {
		return ErrPackageStockBelowUsed
	}
	if _, err := tx.ExecContext(ctx, `
		update bp_package_point
		set name=?, price=?, points=?, status=?, remark=?,
		    stock_limit=?, per_user_limit=?, sale_start_at=?, sale_end_at=?,
		    role_restricted=?, role_visible_only=?, hidden=?
		where id=?
	`, item.Name, item.Price, item.Points, item.Status, nullIfBlank(item.Remark),
		item.StockLimit, item.PerUserLimit, nullIfBlank(item.SaleStartAt), nullIfBlank(item.SaleEndAt),
		item.RoleRestricted, item.RoleVisibleOnly, item.Hidden, id); err != nil {
		return err
	}
	if err := replacePackageRolesTx(ctx, tx, "POINT", id, item.RoleIDs); err != nil {
		return err
	}
	return tx.Commit()
}

// DeletePoint 删除点数套餐。force=false 时只要存在支付或钱包流水即拒绝；
// force=true 时跳过校验直接删除。点数套餐发放的点数已计入用户余额，
// 不做回收；流水作为财务凭证保留。
func (r *Repository) DeletePoint(ctx context.Context, id int64, force bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockPackageRow(ctx, tx, "point", id); err != nil {
		return err
	}
	if !force {
		var referenced int
		if err := tx.QueryRowContext(ctx, `
			select exists(select 1 from bp_payment_order where order_type='POINT_PACKAGE' and target_id=? limit 1)
			    or exists(select 1 from bp_wallet_transaction where biz_type in ('PURCHASE_POINT_PACKAGE','POINTS_GRANT') and biz_id=? limit 1)
		`, id, id).Scan(&referenced); err != nil {
			return err
		}
		if referenced != 0 {
			return ErrPackageHasBusinessHistory
		}
	}
	result, err := tx.ExecContext(ctx, "delete from bp_package_point where id=?", id)
	if err != nil {
		return err
	}
	if err := requireAffected(result); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_package_role where package_type='POINT' and package_id=?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_package_visible_role where package_type='POINT' and package_id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) ListInterface(ctx context.Context, params ListParams, onlyEnabled bool, viewer *PackageViewer) (PageResult[InterfacePackage], error) {
	page, size := normalizePage(params.Page, params.PageSize)
	where := interfacePackageWhere(onlyEnabled)
	args := []any{}
	if onlyEnabled {
		if clause, clauseArgs := packageVisibilityClause("INTERFACE", "p.id", "p.", viewer); clause != "" {
			where = append(where, clause)
			args = append(args, clauseArgs...)
		}
	}
	if params.Status != nil {
		where = append(where, "p.status=?")
		args = append(args, *params.Status)
	}
	if params.InterfaceID > 0 {
		where = append(where, "p.interface_id=?")
		args = append(args, params.InterfaceID)
	}
	if strings.TrimSpace(params.Keyword) != "" {
		where = append(where, "(p.name like ? or a.name like ?)")
		kw := "%" + strings.TrimSpace(params.Keyword) + "%"
		args = append(args, kw, kw)
	}
	whereSQL := strings.Join(where, " and ")
	var (
		total      int64
		items      []InterfacePackage
		packageIDs []int64
	)
	countArgs := append([]any(nil), args...)
	listArgs := append(append([]any(nil), args...), size, pagination.Offset(page, size))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, "select count(*) from bp_package_interface p inner join bp_interface_api a on a.id=p.interface_id "+whereSQL, countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, `
				select p.id,p.interface_id,a.api_code,a.name,coalesce(a.avatar_url,''),a.request_method,coalesce(a.remark,''),
				       p.name,p.status,coalesce(p.remark,''),p.role_restricted,p.role_visible_only,coalesce(p.hidden,0),
				       date_format(p.created_at,'%Y-%m-%d %H:%i:%s'), date_format(p.updated_at,'%Y-%m-%d %H:%i:%s')
				from bp_package_interface p inner join bp_interface_api a on a.id=p.interface_id
				`+whereSQL+` order by p.id desc limit ? offset ?`, listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items = make([]InterfacePackage, 0, size)
			packageIDs = make([]int64, 0, size)
			for rows.Next() {
				item, id, err := scanInterfacePackage(rows)
				if err != nil {
					return err
				}
				items = append(items, item)
				packageIDs = append(packageIDs, id)
			}
			return rows.Err()
		},
	)
	if err != nil {
		return PageResult[InterfacePackage]{}, err
	}
	specsByPackage, err := r.specsByPackageIDs(ctx, packageIDs, onlyEnabled)
	if err != nil {
		return PageResult[InterfacePackage]{}, err
	}
	attachInterfacePackageSpecs(items, packageIDs, specsByPackage)
	assignments, err := r.packageRoleAssignments(ctx, "INTERFACE", packageIDs)
	if err != nil {
		return PageResult[InterfacePackage]{}, err
	}
	for index := range items {
		assignment := assignments[packageIDs[index]]
		items[index].RoleIDs = assignment.RoleIDs
		items[index].RoleNames = assignment.RoleNames
		items[index].CanPurchase = viewer.Allows(items[index].RoleRestricted, assignment.RoleIDs)
	}
	return PageResult[InterfacePackage]{Items: items, Total: total}, nil
}

func (r *Repository) InterfaceByID(ctx context.Context, id int64, onlyEnabled bool, viewer *PackageViewer) (InterfacePackage, error) {
	where := interfacePackageWhere(onlyEnabled)
	var args []any
	if onlyEnabled {
		if clause, clauseArgs := packageVisibilityClause("INTERFACE", "p.id", "p.", viewer); clause != "" {
			where = append(where, clause)
			args = append(args, clauseArgs...)
		}
	}
	where = append(where, "p.id=?")
	args = append(args, id)
	row := r.db.QueryRowContext(ctx, `
		select p.id,p.interface_id,a.api_code,a.name,coalesce(a.avatar_url,''),a.request_method,coalesce(a.remark,''),
		       p.name,p.status,coalesce(p.remark,''),p.role_restricted,p.role_visible_only,coalesce(p.hidden,0),
		       date_format(p.created_at,'%Y-%m-%d %H:%i:%s'), date_format(p.updated_at,'%Y-%m-%d %H:%i:%s')
		from bp_package_interface p inner join bp_interface_api a on a.id=p.interface_id
		`+strings.Join(where, " and "), args...)
	item, packageID, err := scanInterfacePackage(row)
	if err != nil {
		return InterfacePackage{}, err
	}
	specs, err := r.specs(ctx, packageID, onlyEnabled)
	if err != nil {
		return InterfacePackage{}, err
	}
	item.Specs = specs
	item.SpecCount = len(specs)
	item.MinPrice = minimumSpecPrice(specs)
	assignments, err := r.packageRoleAssignments(ctx, "INTERFACE", []int64{packageID})
	if err != nil {
		return InterfacePackage{}, err
	}
	assignment := assignments[packageID]
	item.RoleIDs = assignment.RoleIDs
	item.RoleNames = assignment.RoleNames
	item.CanPurchase = viewer.Allows(item.RoleRestricted, assignment.RoleIDs)
	return item, nil
}

type interfacePackageScanner interface {
	Scan(dest ...any) error
}

func scanInterfacePackage(scanner interfacePackageScanner) (InterfacePackage, int64, error) {
	var item InterfacePackage
	var id, interfaceID int64
	if err := scanner.Scan(
		&id,
		&interfaceID,
		&item.APICode,
		&item.InterfaceName,
		&item.AvatarURL,
		&item.RequestMethod,
		&item.InterfaceRemark,
		&item.Name,
		&item.Status,
		&item.Remark,
		&item.RoleRestricted,
		&item.RoleVisibleOnly,
		&item.Hidden,
		&item.CreateTime,
		&item.UpdateTime,
	); err != nil {
		return InterfacePackage{}, 0, err
	}
	item.ID = strconv.FormatInt(id, 10)
	item.InterfaceID = strconv.FormatInt(interfaceID, 10)
	return item, id, nil
}

func interfacePackageWhere(onlyEnabled bool) []string {
	where := []string{"where 1=1"}
	if onlyEnabled {
		// hidden 是套餐级开关：隐藏时全部规格在商城不可见。
		where = append(where, "p.status=1", "a.status=1", "p.hidden=0")
	}
	return where
}

func (r *Repository) specs(ctx context.Context, packageID int64, onlyEnabled bool) ([]InterfacePackageSpec, error) {
	where := "where package_id=?"
	if onlyEnabled {
		where += " and status=1"
	}
	rows, err := r.db.QueryContext(ctx, `
		select id,package_id,name,cast(price as char),valid_days,daily_limit,total_limit,count_mode,qps_limit,status,
		       coalesce(remark,''),sort_order,
		       coalesce(stock_limit,0),coalesce(stock_used,0),coalesce(per_user_limit,0),
		       coalesce(date_format(sale_start_at,'%Y-%m-%d %H:%i:%s'),''),
		       coalesce(date_format(sale_end_at,'%Y-%m-%d %H:%i:%s'),''),
		       date_format(created_at,'%Y-%m-%d %H:%i:%s'), date_format(updated_at,'%Y-%m-%d %H:%i:%s')
		from bp_package_interface_spec `+where+`
		order by sort_order asc,price asc,id asc
	`, packageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var specs []InterfacePackageSpec
	for rows.Next() {
		spec, _, err := scanInterfacePackageSpec(rows)
		if err != nil {
			return nil, err
		}
		specs = append(specs, spec)
	}
	return specs, rows.Err()
}

func (r *Repository) specsByPackageIDs(ctx context.Context, packageIDs []int64, onlyEnabled bool) (map[int64][]InterfacePackageSpec, error) {
	result := make(map[int64][]InterfacePackageSpec, len(packageIDs))
	query, args := interfaceSpecBatchQuery(packageIDs, onlyEnabled)
	if query == "" {
		return result, nil
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		spec, packageID, err := scanInterfacePackageSpec(rows)
		if err != nil {
			return nil, err
		}
		result[packageID] = append(result[packageID], spec)
	}
	return result, rows.Err()
}

func interfaceSpecBatchQuery(packageIDs []int64, onlyEnabled bool) (string, []any) {
	if len(packageIDs) == 0 {
		return "", nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(packageIDs)), ",")
	args := make([]any, len(packageIDs))
	for index, packageID := range packageIDs {
		args[index] = packageID
	}
	statusWhere := ""
	if onlyEnabled {
		statusWhere = " and status=1"
	}
	return `
		select id,package_id,name,cast(price as char),valid_days,daily_limit,total_limit,count_mode,qps_limit,status,
		       coalesce(remark,''),sort_order,
		       coalesce(stock_limit,0),coalesce(stock_used,0),coalesce(per_user_limit,0),
		       coalesce(date_format(sale_start_at,'%Y-%m-%d %H:%i:%s'),''),
		       coalesce(date_format(sale_end_at,'%Y-%m-%d %H:%i:%s'),''),
		       date_format(created_at,'%Y-%m-%d %H:%i:%s'), date_format(updated_at,'%Y-%m-%d %H:%i:%s')
		from bp_package_interface_spec
		where package_id in (` + placeholders + `)` + statusWhere + `
		order by package_id asc,sort_order asc,price asc,id asc
	`, args
}

func attachInterfacePackageSpecs(items []InterfacePackage, packageIDs []int64, specsByPackage map[int64][]InterfacePackageSpec) {
	limit := len(items)
	if len(packageIDs) < limit {
		limit = len(packageIDs)
	}
	for index := 0; index < limit; index++ {
		specs := specsByPackage[packageIDs[index]]
		items[index].Specs = specs
		items[index].SpecCount = len(specs)
		items[index].MinPrice = minimumSpecPrice(specs)
	}
}

func scanInterfacePackageSpec(scanner interfacePackageScanner) (InterfacePackageSpec, int64, error) {
	var spec InterfacePackageSpec
	var id, packageID int64
	if err := scanner.Scan(
		&id,
		&packageID,
		&spec.SpecName,
		&spec.Price,
		&spec.ValidDays,
		&spec.DailyLimit,
		&spec.TotalLimit,
		&spec.CountMode,
		&spec.QPSLimit,
		&spec.Status,
		&spec.Remark,
		&spec.SortNo,
		&spec.StockLimit,
		&spec.StockUsed,
		&spec.PerUserLimit,
		&spec.SaleStartAt,
		&spec.SaleEndAt,
		&spec.CreateTime,
		&spec.UpdateTime,
	); err != nil {
		return InterfacePackageSpec{}, 0, err
	}
	spec.ID = strconv.FormatInt(id, 10)
	spec.PackageID = strconv.FormatInt(packageID, 10)
	return spec, packageID, nil
}

func (r *Repository) SaveInterface(ctx context.Context, id int64, item InterfacePackage) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var packageID int64
	if id == 0 {
		result, err := tx.ExecContext(ctx, `insert into bp_package_interface(interface_id,name,status,remark,role_restricted,role_visible_only,hidden) values(?,?,?,?,?,?,?)`,
			parseID(item.InterfaceID), item.Name, item.Status, nullIfBlank(item.Remark), item.RoleRestricted, item.RoleVisibleOnly, item.Hidden)
		if err != nil {
			return err
		}
		packageID, err = result.LastInsertId()
		if err != nil {
			return err
		}
	} else {
		var lockedID int64
		if err := tx.QueryRowContext(ctx, "select id from bp_package_interface where id=? for update", id).Scan(&lockedID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `update bp_package_interface set interface_id=?,name=?,status=?,remark=?,role_restricted=?,role_visible_only=?,hidden=? where id=?`,
			parseID(item.InterfaceID), item.Name, item.Status, nullIfBlank(item.Remark), item.RoleRestricted, item.RoleVisibleOnly, item.Hidden, id); err != nil {
			return err
		}
		packageID = id
	}
	if err := replacePackageRolesTx(ctx, tx, "INTERFACE", packageID, item.RoleIDs); err != nil {
		return err
	}

	existing := map[int64]int{}
	rows, err := tx.QueryContext(ctx, `select id,stock_used from bp_package_interface_spec where package_id=? for update`, packageID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var specID int64
		var stockUsed int
		if err := rows.Scan(&specID, &stockUsed); err != nil {
			_ = rows.Close()
			return err
		}
		existing[specID] = stockUsed
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	kept := make(map[int64]struct{}, len(item.Specs))
	for _, spec := range item.Specs {
		if strings.TrimSpace(spec.ID) == "" {
			result, err := tx.ExecContext(ctx, `
				insert into bp_package_interface_spec(package_id,name,price,valid_days,daily_limit,total_limit,count_mode,qps_limit,status,remark,sort_order,
				                                        stock_limit,per_user_limit,sale_start_at,sale_end_at)
				values(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			`, packageID, spec.SpecName, spec.Price, spec.ValidDays, spec.DailyLimit, spec.TotalLimit, normalizeCountMode(spec.CountMode), spec.QPSLimit, spec.Status, nullIfBlank(spec.Remark), spec.SortNo,
				spec.StockLimit, spec.PerUserLimit, nullIfBlank(spec.SaleStartAt), nullIfBlank(spec.SaleEndAt))
			if err != nil {
				return err
			}
			if err := requireAffected(result); err != nil {
				return err
			}
			continue
		}
		specID, err := strconv.ParseInt(spec.ID, 10, 64)
		if err != nil || specID <= 0 {
			return sql.ErrNoRows
		}
		stockUsed, ok := existing[specID]
		if !ok {
			return sql.ErrNoRows
		}
		if _, duplicate := kept[specID]; duplicate {
			return ErrPackageSpecDuplicate
		}
		if spec.StockLimit > 0 && spec.StockLimit < stockUsed {
			return ErrPackageStockBelowUsed
		}
		if _, err := tx.ExecContext(ctx, `
			update bp_package_interface_spec
			set name=?,price=?,valid_days=?,daily_limit=?,total_limit=?,count_mode=?,qps_limit=?,status=?,remark=?,sort_order=?,
			    stock_limit=?,per_user_limit=?,sale_start_at=?,sale_end_at=?
			where id=? and package_id=?
		`, spec.SpecName, spec.Price, spec.ValidDays, spec.DailyLimit, spec.TotalLimit, normalizeCountMode(spec.CountMode), spec.QPSLimit, spec.Status, nullIfBlank(spec.Remark), spec.SortNo,
			spec.StockLimit, spec.PerUserLimit, nullIfBlank(spec.SaleStartAt), nullIfBlank(spec.SaleEndAt), specID, packageID); err != nil {
			return err
		}
		kept[specID] = struct{}{}
	}

	for specID := range existing {
		if _, ok := kept[specID]; ok {
			continue
		}
		var referenced int
		if err := tx.QueryRowContext(ctx, `
			select exists(select 1 from bp_payment_order where order_type='INTERFACE_PACKAGE' and target_id=? limit 1)
			    or exists(select 1 from bp_user_package_interface where spec_id=? limit 1)
			    or exists(select 1 from bp_wallet_transaction where biz_type='PURCHASE_INTERFACE_PACKAGE' and biz_id=? limit 1)
		`, specID, specID, specID).Scan(&referenced); err != nil {
			return err
		}
		if referenced != 0 {
			return ErrPackageSpecHasBusinessHistory
		}
		if _, err := tx.ExecContext(ctx, "delete from bp_package_interface_spec where id=? and package_id=?", specID, packageID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteInterface 删除接口套餐。force=false 时只要存在购买、支付或钱包流水即拒绝；
// force=true 时跳过校验并级联删除用户接口套餐权益行，支付与钱包流水保留用于审计。
func (r *Repository) DeleteInterface(ctx context.Context, id int64, force bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockPackageRow(ctx, tx, "interface", id); err != nil {
		return err
	}
	if !force {
		var referenced int
		if err := tx.QueryRowContext(ctx, `
			select exists(select 1 from bp_user_package_interface where package_id=? limit 1)
			    or exists(
					select 1
					from bp_payment_order o
					inner join bp_package_interface_spec s on s.id=o.target_id
					where o.order_type='INTERFACE_PACKAGE' and s.package_id=?
					limit 1
				)
			    or exists(
					select 1
					from bp_wallet_transaction w
					inner join bp_package_interface_spec s on s.id=w.biz_id
					where w.biz_type='PURCHASE_INTERFACE_PACKAGE' and s.package_id=?
					limit 1
				)
		`, id, id, id).Scan(&referenced); err != nil {
			return err
		}
		if referenced != 0 {
			return ErrPackageHasBusinessHistory
		}
	} else {
		if _, err := tx.ExecContext(ctx, "delete from bp_user_package_interface where package_id=?", id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_package_interface_spec where package_id=?", id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "delete from bp_package_interface where id=?", id)
	if err != nil {
		return err
	}
	if err := requireAffected(result); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_package_role where package_type='INTERFACE' and package_id=?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_package_visible_role where package_type='INTERFACE' and package_id=?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) PurchaseGlobal(ctx context.Context, userID, packageID int64, idempotencyKey string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	replayed, err := claimBalancePurchaseTx(ctx, tx, userID, idempotencyKey, "GLOBAL", packageID)
	if err != nil || replayed {
		return err
	}
	var pkg GlobalPackage
	var price string
	var stockLimit, stockUsed, perUserLimit, roleRestricted, renewalEnabled int
	var saleStart, saleEnd sql.NullTime
	err = tx.QueryRowContext(ctx, `
		select name,cast(price as char),valid_days,daily_limit,total_limit,count_mode,qps_limit,
		       stock_limit,stock_used,per_user_limit,sale_start_at,sale_end_at,role_restricted,
		       coalesce(renewal_enabled,1)
		from bp_package_global where id=? and status=1 and hidden=0 for update
	`, packageID).Scan(&pkg.Name, &price, &pkg.ValidDays, &pkg.DailyLimit, &pkg.TotalLimit, &pkg.CountMode, &pkg.QPSLimit,
		&stockLimit, &stockUsed, &perUserLimit, &saleStart, &saleEnd, &roleRestricted, &renewalEnabled)
	if err != nil {
		return err
	}
	if err := ensurePackageSaleableTx(ctx, tx, userID, "GLOBAL_PACKAGE", packageID, stockLimit, stockUsed, perUserLimit, saleStart, saleEnd); err != nil {
		return err
	}
	if err := ensurePackageRoleAllowedTx(ctx, tx, userID, "GLOBAL", packageID, roleRestricted); err != nil {
		return err
	}
	if err := ensureFreePackageNotClaimedTx(ctx, tx, userID, price, "PURCHASE_GLOBAL_PACKAGE", packageID); err != nil {
		return err
	}
	if err := deductBalanceTx(ctx, tx, userID, price, "PURCHASE_GLOBAL_PACKAGE", packageID, "购买全站套餐"); err != nil {
		return err
	}
	if err := consumePackageStockTx(ctx, tx, "bp_package_global", packageID, stockLimit); err != nil {
		return err
	}
	if err := renewOrInsertTx(ctx, tx, "GLOBAL", userID, packageID, renewalEnabled, pkg.ValidDays, price, "", func() error {
		return insertGlobalEntitlementTx(ctx, tx, userID, packageID, pkg.Name, pkg.DailyLimit, pkg.TotalLimit, pkg.CountMode, pkg.QPSLimit, pkg.ValidDays, "")
	}); err != nil {
		return err
	}
	if err := completeBalancePurchaseTx(ctx, tx, userID, idempotencyKey); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) PurchasePoint(ctx context.Context, userID, packageID int64, idempotencyKey string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	replayed, err := claimBalancePurchaseTx(ctx, tx, userID, idempotencyKey, "POINT", packageID)
	if err != nil || replayed {
		return err
	}
	var price string
	var points int64
	var stockLimit, stockUsed, perUserLimit, roleRestricted int
	var saleStart, saleEnd sql.NullTime
	if err := tx.QueryRowContext(ctx, `
		select cast(price as char),points,stock_limit,stock_used,per_user_limit,sale_start_at,sale_end_at,role_restricted
		from bp_package_point where id=? and status=1 and hidden=0 for update
	`, packageID).Scan(&price, &points, &stockLimit, &stockUsed, &perUserLimit, &saleStart, &saleEnd, &roleRestricted); err != nil {
		return err
	}
	if err := ensurePackageSaleableTx(ctx, tx, userID, "POINT_PACKAGE", packageID, stockLimit, stockUsed, perUserLimit, saleStart, saleEnd); err != nil {
		return err
	}
	if err := ensurePackageRoleAllowedTx(ctx, tx, userID, "POINT", packageID, roleRestricted); err != nil {
		return err
	}
	if err := ensureFreePackageNotClaimedTx(ctx, tx, userID, price, "PURCHASE_POINT_PACKAGE", packageID); err != nil {
		return err
	}
	if err := deductBalanceTx(ctx, tx, userID, price, "PURCHASE_POINT_PACKAGE", packageID, "购买点数套餐"); err != nil {
		return err
	}
	if err := consumePackageStockTx(ctx, tx, "bp_package_point", packageID, stockLimit); err != nil {
		return err
	}
	remainingCapacity := maxUserPointBalance - points
	result, err := tx.ExecContext(ctx, `
		update bp_user
		set points=points+?
		where id=? and points>=0 and points<=?
	`, points, userID, remainingCapacity)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrPointBalanceLimit
	}
	if err := insertWalletTx(ctx, tx, userID, "POINTS_GRANT", packageID, "0.0000", points, "点数套餐发放"); err != nil {
		return err
	}
	if err := completeBalancePurchaseTx(ctx, tx, userID, idempotencyKey); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) PurchaseInterface(ctx context.Context, userID, specID int64, idempotencyKey string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	replayed, err := claimBalancePurchaseTx(ctx, tx, userID, idempotencyKey, "INTERFACE", specID)
	if err != nil || replayed {
		return err
	}
	var packageID, interfaceID int64
	var packageName, specName, price string
	var validDays, qpsLimit int
	var dailyLimit, totalLimit int64
	var countMode string
	var stockLimit, stockUsed, perUserLimit, roleRestricted, renewalEnabled int
	var saleStart, saleEnd sql.NullTime
	err = tx.QueryRowContext(ctx, `
		select p.id,p.interface_id,p.name,s.name,cast(s.price as char),s.valid_days,s.daily_limit,s.total_limit,s.count_mode,s.qps_limit,
		       s.stock_limit,s.stock_used,s.per_user_limit,s.sale_start_at,s.sale_end_at,p.role_restricted,
		       coalesce(s.renewal_enabled,1)
		from bp_package_interface_spec s
		inner join bp_package_interface p on p.id=s.package_id
		inner join bp_interface_api a on a.id=p.interface_id
		where s.id=? and s.status=1 and p.status=1 and a.status=1 and p.hidden=0
		for update
	`, specID).Scan(&packageID, &interfaceID, &packageName, &specName, &price, &validDays, &dailyLimit, &totalLimit, &countMode, &qpsLimit,
		&stockLimit, &stockUsed, &perUserLimit, &saleStart, &saleEnd, &roleRestricted, &renewalEnabled)
	if err != nil {
		return err
	}
	if err := ensurePackageSaleableTx(ctx, tx, userID, "INTERFACE_PACKAGE", specID, stockLimit, stockUsed, perUserLimit, saleStart, saleEnd); err != nil {
		return err
	}
	if err := ensurePackageRoleAllowedTx(ctx, tx, userID, "INTERFACE", packageID, roleRestricted); err != nil {
		return err
	}
	if err := ensureFreePackageNotClaimedTx(ctx, tx, userID, price, "PURCHASE_INTERFACE_PACKAGE", specID); err != nil {
		return err
	}
	if err := deductBalanceTx(ctx, tx, userID, price, "PURCHASE_INTERFACE_PACKAGE", specID, "购买接口套餐"); err != nil {
		return err
	}
	if err := consumePackageStockTx(ctx, tx, "bp_package_interface_spec", specID, stockLimit); err != nil {
		return err
	}
	if err := renewOrInsertTx(ctx, tx, "INTERFACE", userID, specID, renewalEnabled, validDays, price, strconv.FormatInt(packageID, 10), func() error {
		result, err := tx.ExecContext(ctx, `insert into bp_user_package_interface(user_id,interface_id,package_id,spec_id,package_name,spec_name,daily_limit,total_limit,count_mode,qps_limit,start_at,end_at,status) values(?,?,?,?,?,?,?,?,?,?,now(),date_add(now(), interval ? day),1)`,
			userID, interfaceID, packageID, specID, packageName, specName, dailyLimit, totalLimit, normalizeCountMode(countMode), qpsLimit, validDays)
		if err != nil {
			return err
		}
		return requireAffected(result)
	}); err != nil {
		return err
	}
	if err := completeBalancePurchaseTx(ctx, tx, userID, idempotencyKey); err != nil {
		return err
	}
	return tx.Commit()
}

func ensurePackageSaleableTx(ctx context.Context, tx *sql.Tx, userID int64, orderType string, targetID int64, stockLimit, stockUsed, perUserLimit int, saleStart, saleEnd sql.NullTime) error {
	now := time.Now()
	if saleStart.Valid && now.Before(saleStart.Time) {
		return ErrPackageOffSale
	}
	if saleEnd.Valid && !now.Before(saleEnd.Time) {
		return ErrPackageOffSale
	}
	if stockLimit > 0 && stockUsed >= stockLimit {
		return ErrPackageSoldOut
	}
	if perUserLimit > 0 {
		count, err := userPackagePurchaseCountTx(ctx, tx, userID, orderType, targetID)
		if err != nil {
			return err
		}
		if count >= int64(perUserLimit) {
			return ErrPackageUserLimit
		}
	}
	return nil
}

// ensurePackageRoleAllowedTx 校验用户是否属于套餐允许的用户组。
// 用户组取 bp_user_role 与 primary_role_id 的并集，与商城可见性口径一致。
func ensurePackageRoleAllowedTx(ctx context.Context, tx *sql.Tx, userID int64, packageType string, packageID int64, roleRestricted int) error {
	if roleRestricted == 0 {
		return nil
	}
	var allowed bool
	if err := tx.QueryRowContext(ctx, `
		select exists(
			select 1 from bp_package_role pr
			where pr.package_type=? and pr.package_id=?
			  and (
			    pr.role_id in (select role_id from bp_user_role where user_id=?)
			    or pr.role_id = coalesce((select primary_role_id from bp_user where id=?), 0)
			  )
		)
	`, packageType, packageID, userID, userID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return ErrPackageRoleRestricted
	}
	return nil
}

// packageVisibilityClause 返回套餐商城的可见性过滤条件：
// 未限制用户组、未勾选「仅允许组可见」或浏览者属于可见组时才可见。
// 可见组优先取 bp_package_visible_role；该套餐没有配置可见组时回退购买组
// bp_package_role，保持历史套餐的行为不变。viewer 为 nil（管理端）时不追加条件。
func packageVisibilityClause(packageType, packageRef, columnPrefix string, viewer *PackageViewer) (string, []any) {
	if viewer == nil {
		return "", nil
	}
	restrictedCol := columnPrefix + "role_restricted"
	visibleCol := columnPrefix + "role_visible_only"
	if len(viewer.RoleIDs) == 0 {
		return "(" + restrictedCol + "=0 or " + visibleCol + "=0)", nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(viewer.RoleIDs)), ",")
	args := make([]any, 0, len(viewer.RoleIDs)*2+3)
	args = append(args, packageType)
	for _, roleID := range viewer.RoleIDs {
		args = append(args, roleID)
	}
	args = append(args, packageType, packageType)
	for _, roleID := range viewer.RoleIDs {
		args = append(args, roleID)
	}
	return "(" + restrictedCol + "=0 or " + visibleCol + "=0 or exists(" +
		"select 1 from bp_package_visible_role vr where vr.package_type=? and vr.package_id=" + packageRef +
		" and vr.role_id in (" + placeholders + ")) or (not exists(" +
		"select 1 from bp_package_visible_role vx where vx.package_type=? and vx.package_id=" + packageRef +
		") and exists(select 1 from bp_package_role pr where pr.package_type=? and pr.package_id=" + packageRef +
		" and pr.role_id in (" + placeholders + "))))", args
}

// UserRoleIDs 返回用户所属的全部用户组（角色）ID。
func (r *Repository) UserRoleIDs(ctx context.Context, userID int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
		select role_id from bp_user_role where user_id=?
		union
		select primary_role_id from bp_user where id=? and primary_role_id is not null
	`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	roleIDs := make([]int64, 0, 4)
	for rows.Next() {
		var roleID int64
		if err := rows.Scan(&roleID); err != nil {
			return nil, err
		}
		roleIDs = append(roleIDs, roleID)
	}
	return roleIDs, rows.Err()
}

// CountRolesByIDs 统计存在的角色数量，供保存前校验用户组是否有效。
func (r *Repository) CountRolesByIDs(ctx context.Context, roleIDs []int64) (int, error) {
	if len(roleIDs) == 0 {
		return 0, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(roleIDs)), ",")
	args := make([]any, len(roleIDs))
	for index, roleID := range roleIDs {
		args[index] = roleID
	}
	var count int
	err := r.db.QueryRowContext(ctx, "select count(*) from bp_role where id in ("+placeholders+")", args...).Scan(&count)
	return count, err
}

type packageRoleAssignment struct {
	RoleIDs   []int64
	RoleNames []string
}

// packageRoleAssignments 批量读取套餐允许的用户组，缺失的套餐返回空切片
// （JSON 输出为 [] 而不是 null）。
func (r *Repository) packageRoleAssignments(ctx context.Context, packageType string, packageIDs []int64) (map[int64]packageRoleAssignment, error) {
	result := make(map[int64]packageRoleAssignment, len(packageIDs))
	for _, packageID := range packageIDs {
		result[packageID] = packageRoleAssignment{RoleIDs: []int64{}, RoleNames: []string{}}
	}
	if len(packageIDs) == 0 {
		return result, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(packageIDs)), ",")
	args := make([]any, 0, len(packageIDs)+1)
	args = append(args, packageType)
	for _, packageID := range packageIDs {
		args = append(args, packageID)
	}
	rows, err := r.db.QueryContext(ctx, `
		select pr.package_id, pr.role_id, coalesce(r.role_name, '')
		from bp_package_role pr
		left join bp_role r on r.id = pr.role_id
		where pr.package_type=? and pr.package_id in (`+placeholders+`)
		order by pr.package_id asc, pr.role_id asc
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var packageID, roleID int64
		var roleName string
		if err := rows.Scan(&packageID, &roleID, &roleName); err != nil {
			return nil, err
		}
		assignment := result[packageID]
		assignment.RoleIDs = append(assignment.RoleIDs, roleID)
		if roleName != "" {
			assignment.RoleNames = append(assignment.RoleNames, roleName)
		}
		result[packageID] = assignment
	}
	return result, rows.Err()
}

func replacePackageRolesTx(ctx context.Context, tx *sql.Tx, packageType string, packageID int64, roleIDs []int64) error {
	if _, err := tx.ExecContext(ctx, "delete from bp_package_role where package_type=? and package_id=?", packageType, packageID); err != nil {
		return err
	}
	for _, roleID := range roleIDs {
		if _, err := tx.ExecContext(ctx, "insert into bp_package_role(package_type,package_id,role_id) values(?,?,?)", packageType, packageID, roleID); err != nil {
			return err
		}
	}
	return nil
}

func consumePackageStockTx(ctx context.Context, tx *sql.Tx, table string, targetID int64, stockLimit int) error {
	if stockLimit <= 0 {
		return nil
	}
	result, err := tx.ExecContext(ctx, "update "+table+" set stock_used=stock_used+1 where id=? and stock_used<?", targetID, stockLimit)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrPackageSoldOut
	}
	return nil
}

// userPackagePurchaseCountTx 统计同一用户已购买与待支付的数量。
// 全站/接口套餐以权益行为购买凭证；点数套餐以已支付订单与余额购买流水为准。
func userPackagePurchaseCountTx(ctx context.Context, tx *sql.Tx, userID int64, orderType string, targetID int64) (int64, error) {
	var pending int64
	if err := tx.QueryRowContext(ctx, `
		select count(*)
		from bp_payment_order
		where user_id=? and order_type=? and target_id=?
		  and status in ('CREATED','PAYING') and expire_at > now()
	`, userID, orderType, targetID).Scan(&pending); err != nil {
		return 0, err
	}
	switch orderType {
	case "GLOBAL_PACKAGE":
		var bought int64
		if err := tx.QueryRowContext(ctx, `
			select count(*) from bp_user_package_global where user_id=? and package_id=?
		`, userID, targetID).Scan(&bought); err != nil {
			return 0, err
		}
		return bought + pending, nil
	case "INTERFACE_PACKAGE":
		var bought int64
		if err := tx.QueryRowContext(ctx, `
			select count(*) from bp_user_package_interface where user_id=? and spec_id=?
		`, userID, targetID).Scan(&bought); err != nil {
			return 0, err
		}
		return bought + pending, nil
	case "POINT_PACKAGE":
		var paid, balance int64
		if err := tx.QueryRowContext(ctx, `
			select count(*) from bp_payment_order
			where user_id=? and order_type='POINT_PACKAGE' and target_id=? and status='PAID'
		`, userID, targetID).Scan(&paid); err != nil {
			return 0, err
		}
		if err := tx.QueryRowContext(ctx, `
			select count(*) from bp_wallet_transaction
			where user_id=? and biz_type='PURCHASE_POINT_PACKAGE' and biz_id=?
		`, userID, targetID).Scan(&balance); err != nil {
			return 0, err
		}
		return paid + pending + balance, nil
	default:
		return pending, nil
	}
}

func claimBalancePurchaseTx(ctx context.Context, tx *sql.Tx, userID int64, idempotencyKey, purchaseType string, targetID int64) (bool, error) {
	_, err := tx.ExecContext(ctx, `
		insert into bp_balance_purchase_request(user_id,idempotency_key,purchase_type,target_id,status)
		values(?,?,?,?,'PROCESSING')
	`, userID, idempotencyKey, purchaseType, targetID)
	if err == nil {
		return false, nil
	}
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
		return false, err
	}
	var existingType, status string
	var existingTarget int64
	if queryErr := tx.QueryRowContext(ctx, `
		select purchase_type,target_id,status
		from bp_balance_purchase_request
		where user_id=? and idempotency_key=?
	`, userID, idempotencyKey).Scan(&existingType, &existingTarget, &status); queryErr != nil {
		return false, queryErr
	}
	if existingType != purchaseType || existingTarget != targetID {
		return false, ErrPurchaseIdempotencyConflict
	}
	if status == "COMPLETED" {
		return true, nil
	}
	return false, ErrPurchaseInProgress
}

func completeBalancePurchaseTx(ctx context.Context, tx *sql.Tx, userID int64, idempotencyKey string) error {
	result, err := tx.ExecContext(ctx, `
		update bp_balance_purchase_request
		set status='COMPLETED',completed_at=now()
		where user_id=? and idempotency_key=? and status='PROCESSING'
	`, userID, idempotencyKey)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

func ensureFreePackageNotClaimedTx(ctx context.Context, tx *sql.Tx, userID int64, price, bizType string, bizID int64) error {
	if !isZeroMoney(price) {
		return nil
	}
	var lockedUserID int64
	if err := tx.QueryRowContext(ctx, "select id from bp_user where id=? for update", userID).Scan(&lockedUserID); err != nil {
		return err
	}
	var claimed bool
	if err := tx.QueryRowContext(ctx, `
		select exists(
			select 1
			from bp_wallet_transaction
			where user_id=? and biz_type=? and biz_id=?
			limit 1
		)
	`, userID, bizType, bizID).Scan(&claimed); err != nil {
		return err
	}
	if claimed {
		return ErrFreePackageAlreadyClaimed
	}
	return nil
}

func deductBalanceTx(ctx context.Context, tx *sql.Tx, userID int64, amount, bizType string, bizID int64, remark string) error {
	if isZeroMoney(amount) {
		var lockedUserID int64
		if err := tx.QueryRowContext(ctx, "select id from bp_user where id=? for update", userID).Scan(&lockedUserID); err != nil {
			return err
		}
		return insertWalletTx(ctx, tx, userID, bizType, bizID, "0.0000", 0, remark)
	}
	result, err := tx.ExecContext(ctx, `update bp_user set balance=balance-? where id=? and balance >= ?`, amount, userID, amount)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrInsufficientBalance
	}
	return insertWalletTx(ctx, tx, userID, bizType, bizID, negativeMoney(amount), 0, remark)
}

func insertWalletTx(ctx context.Context, tx *sql.Tx, userID int64, bizType string, bizID int64, amount string, points int64, remark string) error {
	result, err := tx.ExecContext(ctx, `
		insert into bp_wallet_transaction(user_id,biz_type,biz_id,amount,points,balance_after,points_after,remark)
		select id,?,?,?,?,balance,points,? from bp_user where id=?
	`, bizType, nullInt64(bizID), amount, points, remark, userID)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

func (r *Repository) UserPackages(ctx context.Context, userID int64) ([]UserPackage, error) {
	// renewable 用关联套餐/规格的开关判定；套餐行被删时 coalesce 兜底为 1，
	// 与购买路径 default 1 保持一致，避免老数据因为关联缺失就续不了费。
	rows, err := r.db.QueryContext(ctx, `
		select id, name, daily_limit, total_limit, total_used, count_mode, qps_limit,
		       date_format(end_at,'%Y-%m-%d %H:%i:%s'), day_used, scope, is_primary,
		       package_id, spec_id, renewable, greatest(timestampdiff(second, now(), end_at), 0)
		from (
			select g.id, g.package_name name, g.daily_limit, g.total_limit, g.total_used, g.count_mode, g.qps_limit,
			       g.end_at, g.day_used, 'GLOBAL' scope, g.is_primary,
			       cast(g.package_id as char) package_id, '' spec_id,
			       coalesce(p.renewal_enabled, 1) renewable
			from bp_user_package_global g
			left join bp_package_global p on p.id = g.package_id
			where g.user_id=? and g.status=1 and g.end_at>now()
			union all
			select i.id, concat(i.package_name,' / ',i.spec_name) name, i.daily_limit, i.total_limit, i.total_used, i.count_mode, i.qps_limit,
			       i.end_at, i.day_used, 'INTERFACE' scope, i.is_primary,
			       cast(i.package_id as char) package_id, cast(i.spec_id as char) spec_id,
			       coalesce(s.renewal_enabled, 1) renewable
			from bp_user_package_interface i
			left join bp_package_interface_spec s on s.id = i.spec_id
			where i.user_id=? and i.status=1 and i.end_at>now()
		) active_packages
		order by is_primary desc, end_at asc, scope asc, id asc
	`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []UserPackage
	for rows.Next() {
		var item UserPackage
		var id int64
		var primary int
		var renewable int
		var remainSeconds int64
		if err := rows.Scan(&id, &item.Name, &item.DailyLimit, &item.TotalLimit, &item.TotalUsed, &item.CountMode, &item.QPSLimit, &item.EndAt, &item.UsedToday, &item.Scope, &primary,
			&item.PackageID, &item.SpecID, &renewable, &remainSeconds); err != nil {
			return nil, err
		}
		item.ID = item.Scope + ":" + strconv.FormatInt(id, 10)
		item.IsPrimary = primary == 1
		item.Renewable = renewable == 1
		// 向上取整：还剩 3 小时也显示「剩余 1 天」，避免用户看到 0 天以为已过期。
		item.RemainDays = int((remainSeconds + 86399) / 86400)
		if item.CountMode == "" {
			item.CountMode = "success"
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// SetPrimaryPackage 设置或取消用户的主套餐。
//
// 主套餐是「用户自己指定的优先扣减顺序」，全局唯一：设置任一层的套餐都会清掉
// 另一层的标记，否则网关无法判定该先试哪张权益表。id=0 表示取消。
//
// 只允许指定仍生效（status=1 且未到期）且属于本人的权益，避免用户把过期条目
// 钉成主套餐后网关反复查一条永不命中的记录。
func (r *Repository) SetPrimaryPackage(ctx context.Context, userID int64, scope string, id int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if id > 0 {
		checkTable := "bp_user_package_global"
		if scope == "INTERFACE" {
			checkTable = "bp_user_package_interface"
		}
		var exists int
		if err := tx.QueryRowContext(ctx,
			"select 1 from "+checkTable+" where id=? and user_id=? and status=1 and end_at>now() for update",
			id, userID).Scan(&exists); err != nil {
			return err
		}
	}
	// 先清两层再打标：单事务内完成，避免出现「两条主套餐」的中间态被并发读路径看到。
	if _, err := tx.ExecContext(ctx, `update bp_user_package_global set is_primary=0 where user_id=? and is_primary=1`, userID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `update bp_user_package_interface set is_primary=0 where user_id=? and is_primary=1`, userID); err != nil {
		return err
	}
	if id <= 0 {
		return tx.Commit()
	}
	table := "bp_user_package_global"
	if scope == "INTERFACE" {
		table = "bp_user_package_interface"
	}
	if _, err := tx.ExecContext(ctx, "update "+table+" set is_primary=1 where id=? and user_id=?", id, userID); err != nil {
		return err
	}
	return tx.Commit()
}

// UserPackageDetails 返回用户权益富快照（全量，不分页；分页在编排层完成）。
// 与 UserPackages 同源两表，额外投影规格名/接口编号/起始时间/状态。
func (r *Repository) UserPackageDetails(ctx context.Context, userID int64) ([]UserPackageDetail, error) {
	rows, err := r.db.QueryContext(ctx, `
		select kind, package_name, spec_name, interface_id, daily_limit, total_limit, total_used, count_mode, qps_limit,
		       date_format(start_at,'%Y-%m-%d %H:%i:%s'), date_format(end_at,'%Y-%m-%d %H:%i:%s'),
		       day_used, status
		from (
			select 'global' kind, package_name, '' spec_name, 0 interface_id, daily_limit, total_limit, total_used, count_mode, qps_limit,
			       start_at, end_at, day_used, status
			from bp_user_package_global where user_id=?
			union all
			select 'interface' kind, package_name, spec_name, interface_id, daily_limit, total_limit, total_used, count_mode, qps_limit,
			       start_at, end_at, day_used, status
			from bp_user_package_interface where user_id=?
		) details
		order by end_at asc, kind asc
	`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]UserPackageDetail, 0, 16)
	for rows.Next() {
		var item UserPackageDetail
		if err := rows.Scan(&item.Kind, &item.PackageName, &item.SpecName, &item.InterfaceID,
			&item.DailyLimit, &item.TotalLimit, &item.TotalUsed, &item.CountMode, &item.QPSLimit,
			&item.StartAt, &item.EndAt, &item.DayUsed, &item.Status); err != nil {
			return nil, err
		}
		if item.CountMode == "" {
			item.CountMode = "success"
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// userEntitlementSource 是管理端用户权益列表的 UNION 数据源。
// scope 为空时返回两表合并，否则只取对应一表。
func userEntitlementSource(scope string, keyword string, status *int) (string, []any) {
	conditions := func(alias string) (string, []any) {
		where := []string{}
		args := []any{}
		if keyword != "" {
			where = append(where, "(u.username like ? or u.display_name like ? or cast(u.id as char) = ? or "+alias+".package_name like ?)")
			kw := "%" + keyword + "%"
			args = append(args, kw, kw, keyword, kw)
		}
		if status != nil {
			where = append(where, alias+".status=?")
			args = append(args, *status)
		}
		if len(where) == 0 {
			return "", args
		}
		return " and " + strings.Join(where, " and "), args
	}

	globalSelect := `
		select 'GLOBAL' scope, ug.id, ug.user_id, u.username, u.display_name,
		       0 interface_id, '' api_code, '' interface_name,
		       ug.package_id, ug.package_name, 0 spec_id, '' spec_name,
		       ug.daily_limit, ug.total_limit, ug.count_mode, ug.qps_limit, ug.day_used, ug.total_used,
		       ug.start_at, ug.end_at, ug.status, ug.created_at
		from bp_user_package_global ug
		inner join bp_user u on u.id=ug.user_id`
	interfaceSelect := `
		select 'INTERFACE' scope, ui.id, ui.user_id, u.username, u.display_name,
		       ui.interface_id, coalesce(a.api_code,''), coalesce(a.name,''),
		       ui.package_id, ui.package_name, ui.spec_id, ui.spec_name,
		       ui.daily_limit, ui.total_limit, ui.count_mode, ui.qps_limit, ui.day_used, ui.total_used,
		       ui.start_at, ui.end_at, ui.status, ui.created_at
		from bp_user_package_interface ui
		inner join bp_user u on u.id=ui.user_id
		left join bp_interface_api a on a.id=ui.interface_id`

	args := []any{}
	parts := []string{}
	if scope != "INTERFACE" {
		where, condArgs := conditions("ug")
		parts = append(parts, globalSelect+where)
		args = append(args, condArgs...)
	}
	if scope != "GLOBAL" {
		where, condArgs := conditions("ui")
		parts = append(parts, interfaceSelect+where)
		args = append(args, condArgs...)
	}
	return strings.Join(parts, " union all "), args
}

func (r *Repository) ListUserEntitlements(ctx context.Context, params ListUserEntitlementParams) (PageResult[UserEntitlement], error) {
	page, size := normalizePage(params.Page, params.PageSize)
	scope := strings.TrimSpace(strings.ToUpper(params.Scope))
	if scope != "GLOBAL" && scope != "INTERFACE" {
		scope = ""
	}
	source, args := userEntitlementSource(scope, strings.TrimSpace(params.Keyword), params.Status)
	var (
		total int64
		items []UserEntitlement
	)
	countArgs := append([]any(nil), args...)
	countArgs = append(countArgs, size*pagination.MaxPage)
	listArgs := append(append([]any(nil), args...), size, pagination.Offset(page, size))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, `select count(*) from (select id from (`+source+`) entitlements limit ?) counted`, countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, `
				select scope,id,user_id,username,coalesce(display_name,''),interface_id,api_code,interface_name,
				       package_id,package_name,spec_id,spec_name,daily_limit,total_limit,count_mode,qps_limit,day_used,total_used,
				       date_format(start_at,'%Y-%m-%d %H:%i:%s'),date_format(end_at,'%Y-%m-%d %H:%i:%s'),status,
				       date_format(created_at,'%Y-%m-%d %H:%i:%s')
				from (`+source+`) entitlements
				order by end_at desc, scope asc, id desc limit ? offset ?
			`, listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items = make([]UserEntitlement, 0, size)
			for rows.Next() {
				var item UserEntitlement
				var id, userID, interfaceID, packageID, specID int64
				var apiCode, interfaceName, specName, displayName sql.NullString
				if err := rows.Scan(&item.Scope, &id, &userID, &item.Username, &displayName,
					&interfaceID, &apiCode, &interfaceName,
					&packageID, &item.PackageName, &specID, &specName, &item.DailyLimit, &item.TotalLimit, &item.CountMode, &item.QPSLimit, &item.DayUsed, &item.TotalUsed,
					&item.StartAt, &item.EndAt, &item.Status, &item.CreateTime); err != nil {
					return err
				}
				item.ID = strconv.FormatInt(id, 10)
				item.UserID = strconv.FormatInt(userID, 10)
				item.PackageID = strconv.FormatInt(packageID, 10)
				item.APICode = apiCode.String
				item.InterfaceName = interfaceName.String
				item.DisplayName = displayName.String
				if interfaceID > 0 {
					item.InterfaceID = strconv.FormatInt(interfaceID, 10)
				}
				if specID > 0 {
					item.SpecID = strconv.FormatInt(specID, 10)
				}
				if item.Scope == "INTERFACE" {
					item.SpecName = specName.String
				}
				items = append(items, item)
			}
			return rows.Err()
		},
	)
	if err != nil {
		return PageResult[UserEntitlement]{}, err
	}
	return PageResult[UserEntitlement]{Items: items, Total: total}, nil
}

// UpdateUserEntitlement 按编辑载荷更新单条用户套餐权益（字段可选，动态 SET）。
// 返回 sql.ErrNoRows 表示记录不存在。
func (r *Repository) UpdateUserEntitlement(ctx context.Context, id int64, edit userEntitlementEdit) error {
	table := "bp_user_package_global"
	if edit.Scope == "INTERFACE" {
		table = "bp_user_package_interface"
	}
	sets := []string{}
	args := []any{}
	if edit.Status != nil {
		sets = append(sets, "status=?")
		args = append(args, *edit.Status)
	}
	if edit.EndAt != nil {
		sets = append(sets, "end_at=?")
		args = append(args, *edit.EndAt)
	}
	if edit.DailyLimit != nil {
		sets = append(sets, "daily_limit=?")
		args = append(args, *edit.DailyLimit)
	}
	if edit.TotalLimit != nil {
		sets = append(sets, "total_limit=?")
		args = append(args, *edit.TotalLimit)
	}
	if edit.CountMode != nil {
		sets = append(sets, "count_mode=?")
		args = append(args, *edit.CountMode)
	}
	if edit.QPSLimit != nil {
		sets = append(sets, "qps_limit=?")
		args = append(args, *edit.QPSLimit)
	}
	args = append(args, id)
	result, err := r.db.ExecContext(ctx, "update "+table+" set "+strings.Join(sets, ",")+" where id=?", args...)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// DeleteUserEntitlement 撤销（删除）单条用户套餐权益。
func (r *Repository) DeleteUserEntitlement(ctx context.Context, id int64, scope string) error {
	table := "bp_user_package_global"
	if scope == "INTERFACE" {
		table = "bp_user_package_interface"
	}
	result, err := r.db.ExecContext(ctx, "delete from "+table+" where id=?", id)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// grantTarget 是解析后的下发对象：userID 必填，username 仅用于回显。
type grantTarget struct {
	UserID   int64
	Username string
}

// resolveGrantUsers 把「用户名或数字 ID」混合列表解析成用户 ID。
// 数字串优先按 ID 匹配，其次按用户名匹配；两种都命不中的进 NotFound。
// 单条 SQL 完成，避免 200 人名单逐个查询（N+1）。
func (r *Repository) resolveGrantUsers(ctx context.Context, users []string) (map[string]grantTarget, []string, error) {
	numeric := make([]int64, 0, len(users))
	names := make([]string, 0, len(users))
	for _, item := range users {
		if id, err := strconv.ParseInt(item, 10, 64); err == nil && id > 0 {
			numeric = append(numeric, id)
		} else {
			names = append(names, item)
		}
	}
	resolved := make(map[string]grantTarget, len(users))
	collect := func(rows *sql.Rows) error {
		for rows.Next() {
			var target grantTarget
			if err := rows.Scan(&target.UserID, &target.Username); err != nil {
				return err
			}
			resolved[strconv.FormatInt(target.UserID, 10)] = target
			if target.Username != "" {
				resolved[target.Username] = target
			}
		}
		return rows.Err()
	}
	if len(names) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(names)), ",")
		args := make([]any, 0, len(names)+2)
		query := "select id, username from bp_user where username in (" + placeholders + ")"
		args = append(args, toAnySlice(names)...)
		if len(numeric) > 0 {
			ph := strings.TrimRight(strings.Repeat("?,", len(numeric)), ",")
			query += " or id in (" + ph + ")"
			args = append(args, toAnySlice(numeric)...)
		}
		rows, err := r.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, nil, err
		}
		if err := collect(rows); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		_ = rows.Close()
	} else if len(numeric) > 0 {
		ph := strings.TrimRight(strings.Repeat("?,", len(numeric)), ",")
		args := toAnySlice(numeric)
		rows, err := r.db.QueryContext(ctx, "select id, username from bp_user where id in ("+ph+")", args...)
		if err != nil {
			return nil, nil, err
		}
		if err := collect(rows); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		_ = rows.Close()
	}
	var missing []string
	for _, item := range users {
		if _, ok := resolved[item]; !ok {
			missing = append(missing, item)
		}
	}
	return resolved, missing, nil
}

func toAnySlice[T any](values []T) []any {
	args := make([]any, len(values))
	for index, value := range values {
		args[index] = value
	}
	return args
}

// GrantUserEntitlement 单事务完成一次下发：锁定套餐行校验后，为每个解析到的用户
// 各插一条权益（或点数入账）。不校验商城售卖窗口/用户组限制/隐藏开关——
// 下发本身就是管理端通道，隐藏套餐只能从这里触达用户。
func (r *Repository) GrantUserEntitlement(ctx context.Context, scope string, packageID, specID int64, users []string, endAt string) (GrantUserEntitlementResult, error) {
	targets, missing, err := r.resolveGrantUsers(ctx, users)
	if err != nil {
		return GrantUserEntitlementResult{}, err
	}
	if len(missing) == len(users) {
		return GrantUserEntitlementResult{NotFound: missing}, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return GrantUserEntitlementResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	granted := 0
	switch scope {
	case "GLOBAL":
		if packageID <= 0 {
			return GrantUserEntitlementResult{}, apperrors.New(consts.StatusBadRequest, "请选择要下发的全站套餐")
		}
		var name, countMode string
		var validDays int
		var dailyLimit, totalLimit int64
		var qpsLimit, stockLimit, stockUsed int
		if err := tx.QueryRowContext(ctx, `
			select name,valid_days,daily_limit,total_limit,count_mode,qps_limit,stock_limit,stock_used
			from bp_package_global where id=? and status=1 for update
		`, packageID).Scan(&name, &validDays, &dailyLimit, &totalLimit, &countMode, &qpsLimit, &stockLimit, &stockUsed); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return GrantUserEntitlementResult{}, apperrors.New(consts.StatusBadRequest, "全站套餐不存在或未启用")
			}
			return GrantUserEntitlementResult{}, err
		}
		for _, item := range users {
			target, ok := targets[item]
			if !ok {
				continue
			}
			if err := consumePackageStockTx(ctx, tx, "bp_package_global", packageID, stockLimit); err != nil {
				return GrantUserEntitlementResult{}, err
			}
			if err := insertGlobalEntitlementTx(ctx, tx, target.UserID, packageID, name, dailyLimit, totalLimit, countMode, qpsLimit, validDays, endAt); err != nil {
				return GrantUserEntitlementResult{}, err
			}
			granted++
		}
	case "INTERFACE":
		if specID <= 0 {
			return GrantUserEntitlementResult{}, apperrors.New(consts.StatusBadRequest, "请选择要下发的接口套餐规格")
		}
		var packageID, interfaceID int64
		var packageName, specName, countMode string
		var validDays int
		var dailyLimit, totalLimit int64
		var qpsLimit, stockLimit, stockUsed int
		if err := tx.QueryRowContext(ctx, `
			select p.id,p.interface_id,p.name,s.name,s.valid_days,s.daily_limit,s.total_limit,s.count_mode,s.qps_limit,s.stock_limit,s.stock_used
			from bp_package_interface_spec s
			inner join bp_package_interface p on p.id=s.package_id
			where s.id=? and s.status=1 and p.status=1 for update
		`, specID).Scan(&packageID, &interfaceID, &packageName, &specName, &validDays, &dailyLimit, &totalLimit, &countMode, &qpsLimit, &stockLimit, &stockUsed); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return GrantUserEntitlementResult{}, apperrors.New(consts.StatusBadRequest, "接口套餐规格不存在或未启用")
			}
			return GrantUserEntitlementResult{}, err
		}
		for _, item := range users {
			target, ok := targets[item]
			if !ok {
				continue
			}
			if err := consumePackageStockTx(ctx, tx, "bp_package_interface_spec", specID, stockLimit); err != nil {
				return GrantUserEntitlementResult{}, err
			}
			if err := insertInterfaceEntitlementTx(ctx, tx, target.UserID, interfaceID, packageID, specID, packageName, specName, dailyLimit, totalLimit, countMode, qpsLimit, validDays, endAt); err != nil {
				return GrantUserEntitlementResult{}, err
			}
			granted++
		}
	case "POINT":
		if packageID <= 0 {
			return GrantUserEntitlementResult{}, apperrors.New(consts.StatusBadRequest, "请选择要下发的点数套餐")
		}
		var points int64
		var stockLimit, stockUsed int
		if err := tx.QueryRowContext(ctx, `
			select points,stock_limit,stock_used from bp_package_point where id=? and status=1 for update
		`, packageID).Scan(&points, &stockLimit, &stockUsed); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return GrantUserEntitlementResult{}, apperrors.New(consts.StatusBadRequest, "点数套餐不存在或未启用")
			}
			return GrantUserEntitlementResult{}, err
		}
		for _, item := range users {
			target, ok := targets[item]
			if !ok {
				continue
			}
			if err := consumePackageStockTx(ctx, tx, "bp_package_point", packageID, stockLimit); err != nil {
				return GrantUserEntitlementResult{}, err
			}
			if err := grantPointsTx(ctx, tx, target.UserID, packageID, points); err != nil {
				return GrantUserEntitlementResult{}, err
			}
			granted++
		}
	default:
		return GrantUserEntitlementResult{}, apperrors.New(consts.StatusBadRequest, "权益类型不合法")
	}
	if err := tx.Commit(); err != nil {
		return GrantUserEntitlementResult{}, err
	}
	return GrantUserEntitlementResult{Granted: granted, NotFound: missing}, nil
}

// insertGlobalEntitlementTx 落一条全站套餐权益；endAt 非空时覆盖套餐有效天数。
func insertGlobalEntitlementTx(ctx context.Context, tx *sql.Tx, userID, packageID int64, name string, dailyLimit, totalLimit int64, countMode string, qpsLimit, validDays int, endAt string) error {
	endExpression := "date_add(now(), interval ? day)"
	args := []any{userID, packageID, name, dailyLimit, totalLimit, normalizeCountMode(countMode), qpsLimit, validDays}
	if endAt != "" {
		endExpression = "?"
		args = append(args, endAt)
	}
	result, err := tx.ExecContext(ctx, `insert into bp_user_package_global(user_id,package_id,package_name,daily_limit,total_limit,count_mode,qps_limit,start_at,end_at,status) values(?,?,?,?,?,?,?,now(),`+endExpression+`,1)`,
		args...)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// insertInterfaceEntitlementTx 落一条接口套餐规格权益；endAt 非空时覆盖有效天数。
// 与购买路径同列：package_id/spec_id 都落库，便于管理端回溯套餐来源。
func insertInterfaceEntitlementTx(ctx context.Context, tx *sql.Tx, userID, interfaceID, packageID, specID int64, packageName, specName string, dailyLimit, totalLimit int64, countMode string, qpsLimit, validDays int, endAt string) error {
	endExpression := "date_add(now(), interval ? day)"
	args := []any{userID, interfaceID, packageID, specID, packageName, specName, dailyLimit, totalLimit, normalizeCountMode(countMode), qpsLimit, validDays}
	if endAt != "" {
		endExpression = "?"
		args = append(args, endAt)
	}
	result, err := tx.ExecContext(ctx, `insert into bp_user_package_interface(user_id,interface_id,package_id,spec_id,package_name,spec_name,daily_limit,total_limit,count_mode,qps_limit,start_at,end_at,status) values(?,?,?,?,?,?,?,?,?,now(),`+endExpression+`,1)`,
		args...)
	if err != nil {
		return err
	}
	return requireAffected(result)
}

// renewOrInsertTx 决定这次购买是「顺延已有权益」还是「新建一条权益」。
//
// 为什么需要这一层：原购买路径无条件 INSERT，用户在有效期内再买同一套餐会得到
// 两条各自从 now() 起算的权益；网关按 end_at asc 先扣最早到期的那张，后买的那张
// 在前一周期内一直排队，用户付了两次钱却拿不到叠加后的天数。
//
// 顺延条件（全部满足才顺延）：
//  1. 套餐开了 renewal_enabled；
//  2. 用户存在同一 package_id/spec_id、status=1、end_at > now() 的权益。
//
// 顺延只改 end_at，不动 start_at / day_used / total_used：续费不换计费周期，
// 当天已用仍按当日统计，次日照常重置；总限量同理不重置（这是有意的，见迁移注释）。
//
// keyID 对全站套餐传 package_id，对接口套餐传 spec_id——两者在各自权益表里
// 都是「同一款商品」的唯一标识，用它命中才是用户认知里的「同一个套餐」。
func renewOrInsertTx(ctx context.Context, tx *sql.Tx, scope string, userID, keyID int64,
	renewalEnabled, addDays int, amount, packageIDText string, insert func() error) error {

	table := "bp_user_package_global"
	matchColumn := "package_id"
	if scope == "INTERFACE" {
		table = "bp_user_package_interface"
		matchColumn = "spec_id"
	}

	if renewalEnabled == 0 {
		return insert()
	}

	var (
		entID  int64
		oldEnd time.Time
	)
	// for update 锁住候选行：并发双击时第二个事务会等前者提交后再读，
	// 于是拿到已经 +30 天的 end_at，在此基础上再 +30，不会互相覆盖。
	err := tx.QueryRowContext(ctx, `
		select id, end_at from `+table+`
		where user_id=? and `+matchColumn+`=? and status=1 and end_at>now()
		order by end_at desc, id desc limit 1
		for update
	`, userID, keyID).Scan(&entID, &oldEnd)
	if errors.Is(err, sql.ErrNoRows) {
		return insert()
	}
	if err != nil {
		return err
	}

	result, err := tx.ExecContext(ctx, `
		update `+table+` set end_at = date_add(end_at, interval ? day)
		where id=? and user_id=? and status=1 and end_at>now()
	`, addDays, entID, userID)
	if err != nil {
		return err
	}
	// 行被并发改动（刚好到期/被禁用）时 RowsAffected=0：退回到新建，
	// 绝不能当成成功——否则用户付了钱却没有权益。
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return insert()
	}

	var newEnd time.Time
	if err := tx.QueryRowContext(ctx,
		`select end_at from `+table+` where id=?`, entID).Scan(&newEnd); err != nil {
		return err
	}

	var pkgID int64
	if packageIDText != "" {
		pkgID, _ = strconv.ParseInt(packageIDText, 10, 64)
	}
	var specID int64
	if scope == "INTERFACE" {
		specID = keyID
	}
	if _, err := tx.ExecContext(ctx, `
		insert into bp_package_renewal_log
		  (user_id,scope,entitlement_id,package_id,spec_id,add_days,old_end_at,new_end_at,amount)
		values(?,?,?,?,?,?,?,?,?)
	`, userID, scope, entID, pkgID, specID, addDays, oldEnd, newEnd, amount); err != nil {
		return err
	}
	return nil
}

func grantPointsTx(ctx context.Context, tx *sql.Tx, userID, packageID, points int64) error {
	remainingCapacity := maxUserPointBalance - points
	result, err := tx.ExecContext(ctx, `
		update bp_user
		set points=points+?
		where id=? and points>=0 and points<=?
	`, points, userID, remainingCapacity)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrPointBalanceLimit
	}
	return insertWalletTx(ctx, tx, userID, "POINTS_GRANT", packageID, "0.0000", points, "管理员下发点数套餐")
}

func (r *Repository) WalletTransactions(ctx context.Context, userID int64, params ListParams) (PageResult[WalletTransaction], error) {
	page, size := normalizePage(params.Page, params.PageSize)
	var (
		total int64
		items []WalletTransaction
	)
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, walletTransactionCountQuery(), userID, size*pagination.MaxPage).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, walletTransactionListQuery(), userID, size, pagination.Offset(page, size))
			if err != nil {
				return err
			}
			defer rows.Close()
			items = make([]WalletTransaction, 0, size)
			for rows.Next() {
				var item WalletTransaction
				var id, bizID int64
				if err := rows.Scan(&id, &item.BizType, &bizID, &item.Amount, &item.Points, &item.BalanceAfter, &item.PointsAfter, &item.Remark, &item.CreateTime); err != nil {
					return err
				}
				item.ID = strconv.FormatInt(id, 10)
				if bizID > 0 {
					item.BizID = strconv.FormatInt(bizID, 10)
				}
				items = append(items, item)
			}
			return rows.Err()
		},
	)
	if err != nil {
		return PageResult[WalletTransaction]{}, err
	}
	return PageResult[WalletTransaction]{Items: items, Total: total}, nil
}

func walletTransactionCountQuery() string {
	return `select count(*)
		from (
			select id
			from bp_wallet_transaction
			where user_id=?
			limit ?
		) counted`
}

func walletTransactionListQuery() string {
	return `select id,biz_type,coalesce(biz_id,0),cast(amount as char),points,cast(balance_after as char),points_after,coalesce(remark,''),date_format(created_at,'%Y-%m-%d %H:%i:%s')
		from bp_wallet_transaction
		where user_id=?
		order by created_at desc,id desc limit ? offset ?`
}

func (r *Repository) BillingRecords(ctx context.Context, userID int64, admin bool, params ListParams) (PageResult[BillingRecord], error) {
	page, size := normalizePage(params.Page, params.PageSize)
	where := "where log_visible=1"
	args := []any{}
	if !admin {
		where += " and user_id=?"
		args = append(args, userID)
	}
	var (
		total int64
		items []BillingRecord
	)
	countArgs := append(append([]any(nil), args...), size*pagination.MaxPage)
	listArgs := append(append([]any(nil), args...), size, pagination.Offset(page, size))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, billingRecordCountQuery(where), countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, billingRecordListQuery(where), listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items = make([]BillingRecord, 0, size)
			for rows.Next() {
				var item BillingRecord
				var id, userID, interfaceID int64
				if err := rows.Scan(&id, &userID, &interfaceID, &item.APICode, &item.ChargeType, &item.ChargeAmount, &item.PointAmount, &item.Status, &item.Reason, &item.CreateTime); err != nil {
					return err
				}
				item.ID = strconv.FormatInt(id, 10)
				item.UserID = strconv.FormatInt(userID, 10)
				item.InterfaceID = strconv.FormatInt(interfaceID, 10)
				items = append(items, item)
			}
			return rows.Err()
		},
	)
	if err != nil {
		return PageResult[BillingRecord]{}, err
	}
	return PageResult[BillingRecord]{Items: items, Total: total}, nil
}

func billingRecordCountQuery(where string) string {
	return `select count(*)
		from (
			select id
			from bp_billing_record ` + where + `
			limit ?
		) counted`
}

func billingRecordListQuery(where string) string {
	return `select id,user_id,interface_id,api_code,charge_type,cast(charge_amount as char),point_amount,status,coalesce(reason,''),date_format(created_at,'%Y-%m-%d %H:%i:%s')
		from bp_billing_record ` + where + `
		order by created_at desc,id desc limit ? offset ?`
}

func normalizePage(page, size int) (int, int) {
	return pagination.Normalize(page, size, 100)
}

func lockPackageRow(ctx context.Context, tx *sql.Tx, packageType string, id int64) error {
	query := ""
	switch packageType {
	case "global":
		query = "select id from bp_package_global where id=? for update"
	case "interface":
		query = "select id from bp_package_interface where id=? for update"
	case "point":
		query = "select id from bp_package_point where id=? for update"
	default:
		return fmt.Errorf("unsupported package type %q", packageType)
	}
	var lockedID int64
	return tx.QueryRowContext(ctx, query, id).Scan(&lockedID)
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

func parseID(value string) int64 {
	id, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	return id
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

func minimumSpecPrice(specs []InterfacePackageSpec) string {
	var minimum *big.Rat
	value := ""
	for _, spec := range specs {
		price, ok := new(big.Rat).SetString(strings.TrimSpace(spec.Price))
		if !ok {
			continue
		}
		if minimum == nil || price.Cmp(minimum) < 0 {
			minimum = price
			value = spec.Price
		}
	}
	return value
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
