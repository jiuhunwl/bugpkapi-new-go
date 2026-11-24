package interfaceapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"bugpk-api/internal/pkg/accountstatus"
	"bugpk-api/internal/pkg/businessclock"
	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/jsoncleaning"
	"bugpk-api/internal/pkg/masking"
	"bugpk-api/internal/pkg/pagination"
	"bugpk-api/internal/pkg/proxypool"
	"bugpk-api/internal/pkg/requesttimeout"
	"bugpk-api/internal/pkg/requestvalidation"
	"bugpk-api/internal/pkg/secretbox"
)

var (
	ErrInterfaceHasActiveEntitlements = errors.New("interface has active user package entitlements")
	ErrInterfaceHasPendingPayments    = errors.New("interface has pending payment orders")
	ErrInterfaceQuotaRoleInvalid      = errors.New("interface quota references an unknown role")
	ErrInterfaceMeteringRoleInvalid   = errors.New("interface metering policy references an unknown role")
	ErrInterfaceDegradeRoleInvalid    = errors.New("interface degrade policy references an unknown role")
	ErrInterfaceDegradeTargetInvalid  = errors.New("interface degrade policy references an unavailable target interface")
)

const interfaceCallCountWindowDays = 30

type Repository struct {
	cipher *secretbox.Cipher
	db     *sql.DB
}

func NewRepository(db *sql.DB, ciphers ...*secretbox.Cipher) *Repository {
	cipher := secretbox.New("")
	if len(ciphers) > 0 && ciphers[0] != nil {
		cipher = ciphers[0]
	}
	return &Repository{cipher: cipher, db: db}
}

func interfaceOpenTrafficCondition(alias string) string {
	return accountstatus.ActiveTrafficSQL(alias)
}

func recentCallCountsQuery(placeholders string) string {
	return `
		select m.interface_id,cast(coalesce(sum(m.call_count),0) as unsigned)
		from bp_call_api_metric_minute m force index (idx_bp_call_api_metric_interface_time)
		inner join bp_user u on u.id=m.user_id
		where m.interface_id in (` + placeholders + `)
		  and m.bucket_start >= date_sub(now(), interval ? day)
		  and ` + accountstatus.EffectiveStatusSQL("u") + ` = 1
		group by m.interface_id
	`
}

func interfaceTrend7dQuery() string {
	return `
		select date_format(m.bucket_start,'%Y-%m-%d'),
		       cast(coalesce(sum(m.call_count),0) as unsigned)
		from bp_call_api_metric_minute m force index (idx_bp_call_api_metric_interface_time)
		inner join bp_user u on u.id=m.user_id
		where m.interface_id=?
		  and m.bucket_start>=? and m.bucket_start<?
		  and ` + accountstatus.EffectiveStatusSQL("u") + ` = 1
		group by date_format(m.bucket_start,'%Y-%m-%d')
	`
}

func (r *Repository) List(ctx context.Context, params ListParams, admin bool) (PageResult[InterfaceItem], error) {
	page, pageSize := normalizePage(params.Page, params.PageSize)
	where, args := listWhere(params, admin)
	var (
		total int64
		items []InterfaceItem
	)
	countArgs := append(append([]any(nil), args...), pageSize*pagination.MaxPage)
	listArgs := append(append([]any(nil), args...), pageSize, pagination.Offset(page, pageSize))
	err := dbx.RunParallel(
		func() error {
			return r.db.QueryRowContext(ctx, interfaceCountQuery(where), countArgs...).Scan(&total)
		},
		func() error {
			rows, err := r.db.QueryContext(ctx, interfaceListQuery(where), listArgs...)
			if err != nil {
				return err
			}
			defer rows.Close()
			items, err = scanInterfaceItems(rows)
			return err
		},
	)
	if err != nil {
		return PageResult[InterfaceItem]{}, err
	}
	if err := r.populateRecentCallCounts(ctx, items); err != nil {
		return PageResult[InterfaceItem]{}, err
	}
	return PageResult[InterfaceItem]{Items: items, Total: total}, nil
}

func interfaceCountQuery(where string) string {
	return `select count(*)
		from (
			select a.id
			from bp_interface_api a
			` + where + `
			limit ?
		) counted`
}

func interfaceListQuery(where string) string {
	return `
		select a.id, a.api_code, a.name, coalesce(a.avatar_url, ''), a.request_url, a.request_method,
		       a.price, a.point_price, a.shared_qps_limit, a.user_qps_limit, a.is_top, a.is_featured, a.status, a.remark,
                       a.polling_enabled, a.polling_mode, a.response_cleaning_enabled, coalesce(cast(a.upstream_urls as char), '[]'),
		       a.polling_check_enabled, coalesce(a.polling_check_field, ''), coalesce(a.polling_check_expected, ''),
		       coalesce(a.polling_stop_code, ''),
		       a.current_node,
		       coalesce(a.doc_summary, ''), a.doc_response_type, coalesce(a.doc_preferred_method, ''),
		       coalesce(cast(a.doc_request_params as char), ''), coalesce(cast(a.doc_response_fields as char), ''),
		       coalesce(a.doc_response_example, ''), coalesce(cast(a.doc_status_codes as char), ''), coalesce(a.doc_notice, ''),
		       coalesce(cast(a.doc_response_formats as char), ''),
		       date_format(a.created_at, '%Y-%m-%d %H:%i:%s'), date_format(a.updated_at, '%Y-%m-%d %H:%i:%s'), a.billing_enabled, a.billing_log_enabled,
		       0
		from (
			select a.id
			from bp_interface_api a
			` + where + `
			order by a.is_top desc, a.is_featured desc, a.id desc
			limit ? offset ?
		) filtered
		inner join bp_interface_api a on a.id=filtered.id
		order by a.is_top desc, a.is_featured desc, a.id desc
	`
}

func listWhere(params ListParams, admin bool) (string, []any) {
	where := []string{"where 1=1"}
	args := []any{}
	keyword := strings.TrimSpace(params.Keyword)
	if keyword != "" {
		like := "%" + keyword + "%"
		if admin {
			where = append(where, "(a.name like ? or a.api_code like ? or a.request_url like ? or a.remark like ?)")
			args = append(args, like, like, like, like)
		} else {
			where = append(where, "(a.name like ? or a.api_code like ? or a.remark like ?)")
			args = append(args, like, like, like)
		}
	}
	if strings.TrimSpace(params.RequestMethod) != "" {
		where = append(where, "a.request_method = ?")
		args = append(args, strings.TrimSpace(params.RequestMethod))
	}
	if admin {
		if params.Status != nil {
			where = append(where, "a.status = ?")
			args = append(args, *params.Status)
		}
		if params.IsFeatured != nil {
			where = append(where, "a.is_featured = ?")
			args = append(args, *params.IsFeatured)
		}
	} else {
		where = append(where, "a.status = 1")
	}
	if params.FeaturedOnly != nil && *params.FeaturedOnly == 1 {
		where = append(where, "a.is_featured = 1")
	}
	switch strings.ToUpper(strings.TrimSpace(params.PriceType)) {
	case "FREE":
		where = append(where, "a.price <= 0 and a.point_price <= 0")
	case "PAID":
		where = append(where, "a.price > 0")
	case "POINT":
		where = append(where, "a.point_price > 0")
	}
	return strings.Join(where, " and "), args
}

func scanInterfaceItems(rows *sql.Rows) ([]InterfaceItem, error) {
	var items []InterfaceItem
	for rows.Next() {
		var item InterfaceItem
		var id int64
		var price string
		var upstreamText string
		var requestParams, responseFields, statusCodes string
		if err := rows.Scan(
			&id, &item.APICode, &item.Name, &item.AvatarURL, &item.RequestURL, &item.RequestMethod,
			&price, &item.PointPrice, &item.SharedQPSLimit, &item.QPSLimit, &item.IsTop, &item.IsFeatured, &item.Status, &item.Remark,
			&item.PollingEnabled, &item.PollingMode, &item.ResponseCleaningEnabled, &upstreamText,
			&item.PollingCheckEnabled, &item.PollingCheckField, &item.PollingCheckExpected,
			&item.PollingStopCode,
			&item.CurrentNode,
			&item.DocSummary, &item.DocResponseType, &item.DocPreferredMethod,
			&requestParams, &responseFields, &item.DocResponseExample, &statusCodes, &item.DocNotice,
			&item.DocResponseFormats,
			&item.CreateTime, &item.UpdateTime, &item.BillingEnabled, &item.BillingLogEnabled, &item.CallCount,
		); err != nil {
			return nil, err
		}
		item.ID = strconv.FormatInt(id, 10)
		item.Price = price
		item.Description = item.Remark
		item.DocRequestParams = requestParams
		item.DocResponseFields = responseFields
		item.DocStatusCodes = statusCodes
		item.ResponseType = item.DocResponseType
		item.GatewayPath = "/api/" + item.APICode
		item.GatewayURLTemplate = item.GatewayPath
		item.ResponseFieldMappings, item.UpstreamConfigs, item.ProxyPoolConfig, item.DomainRouting = decodeUpstreamConfigDocumentFullWithRouting(upstreamText)
		item.UpstreamURLs = upstreamURLs(item.UpstreamConfigs)
		item.UpstreamCount = displayUpstreamCount(item)
		item.CurrentNodeURL = currentNodeURL(item)
		item.PriceLabel = priceLabel(item.Price, item.PointPrice)
		item.TemplateParameters = parameterNames(item.RequestURL)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *Repository) ByID(ctx context.Context, id int64) (InterfaceItem, error) {
	rows, err := r.db.QueryContext(ctx, `
		select a.id, a.api_code, a.name, coalesce(a.avatar_url, ''), a.request_url, a.request_method,
		       a.price, a.point_price, a.shared_qps_limit, a.user_qps_limit, a.is_top, a.is_featured, a.status, a.remark,
                       a.polling_enabled, a.polling_mode, a.response_cleaning_enabled, coalesce(cast(a.upstream_urls as char), '[]'),
		       a.polling_check_enabled, coalesce(a.polling_check_field, ''), coalesce(a.polling_check_expected, ''),
		       coalesce(a.polling_stop_code, ''),
		       a.current_node,
		       coalesce(a.doc_summary, ''), a.doc_response_type, coalesce(a.doc_preferred_method, ''),
		       coalesce(cast(a.doc_request_params as char), ''), coalesce(cast(a.doc_response_fields as char), ''),
		       coalesce(a.doc_response_example, ''), coalesce(cast(a.doc_status_codes as char), ''), coalesce(a.doc_notice, ''),
		       coalesce(cast(a.doc_response_formats as char), ''),
		       date_format(a.created_at, '%Y-%m-%d %H:%i:%s'), date_format(a.updated_at, '%Y-%m-%d %H:%i:%s'), a.billing_enabled, a.billing_log_enabled,
		       0
		from bp_interface_api a
		where a.id = ?
	`, id)
	if err != nil {
		return InterfaceItem{}, err
	}
	items, err := scanInterfaceItems(rows)
	closeErr := rows.Close()
	if err != nil {
		return InterfaceItem{}, err
	}
	if closeErr != nil {
		return InterfaceItem{}, closeErr
	}
	if len(items) == 0 {
		return InterfaceItem{}, sql.ErrNoRows
	}
	if err := r.populateRecentCallCounts(ctx, items); err != nil {
		return InterfaceItem{}, err
	}
	return items[0], nil
}

func (r *Repository) populateRecentCallCounts(ctx context.Context, items []InterfaceItem) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(items))
	positions := make(map[int64]int, len(items))
	for index := range items {
		id := parseID(items[index].ID)
		if id <= 0 {
			return fmt.Errorf("invalid interface id %q while loading call counts", items[index].ID)
		}
		ids = append(ids, id)
		positions[id] = index
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, 0, len(ids)+1)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, interfaceCallCountWindowDays)
	rows, err := r.db.QueryContext(ctx, recentCallCountsQuery(placeholders), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, count int64
		if err := rows.Scan(&id, &count); err != nil {
			return err
		}
		if index, ok := positions[id]; ok {
			items[index].CallCount = count
		}
	}
	return rows.Err()
}

func (r *Repository) Create(ctx context.Context, item InterfaceItem) (int64, error) {
	upstream, err := encodeUpstreamConfigDocumentForStorageWithRouting(r.cipher, item.ResponseFieldMappings, item.UpstreamConfigs, item.ProxyPoolConfig, item.DomainRouting, "")
	if err != nil {
		return 0, err
	}
	result, err := r.db.ExecContext(ctx, `
		insert into bp_interface_api(api_code, name, avatar_url, request_url, request_method, price, point_price, shared_qps_limit, user_qps_limit, is_top, is_featured, status, remark,
			polling_enabled, polling_mode, response_cleaning_enabled, upstream_urls, polling_check_enabled, polling_check_field, polling_check_expected, polling_stop_code)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, item.APICode, item.Name, nullIfBlank(item.AvatarURL), item.RequestURL, item.RequestMethod, item.Price, item.PointPrice, item.SharedQPSLimit, item.QPSLimit, item.IsTop, item.IsFeatured, item.Status, item.Remark,
		item.PollingEnabled, defaultText(item.PollingMode, "SINGLE"), item.ResponseCleaningEnabled, jsonOrNull(upstream), item.PollingCheckEnabled, nullIfBlank(item.PollingCheckField), nullIfBlank(item.PollingCheckExpected), nullIfBlank(item.PollingStopCode))
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (r *Repository) Update(ctx context.Context, id int64, item InterfaceItem) error {
	existingPassword, err := r.storedProxyPoolPassword(ctx, id)
	if err != nil {
		return err
	}
	upstream, err := encodeUpstreamConfigDocumentForStorageWithRouting(r.cipher, item.ResponseFieldMappings, item.UpstreamConfigs, item.ProxyPoolConfig, item.DomainRouting, existingPassword)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		update bp_interface_api
		set api_code = ?, name = ?, avatar_url = ?, request_url = ?, request_method = ?, price = ?, point_price = ?, shared_qps_limit = ?, user_qps_limit = ?,
		    is_top = ?, is_featured = ?, status = ?, remark = ?, polling_enabled = ?, polling_mode = ?, response_cleaning_enabled = ?, upstream_urls = ?,
		    polling_check_enabled = ?, polling_check_field = ?, polling_check_expected = ?, polling_stop_code = ?
		where id = ?
	`, item.APICode, item.Name, nullIfBlank(item.AvatarURL), item.RequestURL, item.RequestMethod, item.Price, item.PointPrice, item.SharedQPSLimit, item.QPSLimit,
		item.IsTop, item.IsFeatured, item.Status, item.Remark, item.PollingEnabled, defaultText(item.PollingMode, "SINGLE"), item.ResponseCleaningEnabled, jsonOrNull(upstream),
		item.PollingCheckEnabled, nullIfBlank(item.PollingCheckField), nullIfBlank(item.PollingCheckExpected), nullIfBlank(item.PollingStopCode), id)
	if err != nil {
		return err
	}
	return r.requireAffectedOrExisting(ctx, id, result)
}

func (r *Repository) UpdateStatusBatch(ctx context.Context, ids []int64, status int) error {
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, status)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	result, err := r.db.ExecContext(ctx,
		"update bp_interface_api set status = ? where id in ("+strings.Join(placeholders, ",")+")",
		args...,
	)
	if err != nil {
		return err
	}
	if _, err := result.RowsAffected(); err != nil {
		return err
	}
	countArgs := make([]any, 0, len(ids))
	for _, id := range ids {
		countArgs = append(countArgs, id)
	}
	var count int64
	if err := r.db.QueryRowContext(ctx,
		"select count(*) from bp_interface_api where id in ("+strings.Join(placeholders, ",")+")",
		countArgs...,
	).Scan(&count); err != nil {
		return err
	}
	if count != int64(len(ids)) {
		return sql.ErrNoRows
	}
	return nil
}

// InterfaceQuota loads the per-interface quota and role overrides. A missing
// policy row is a valid state: the interface simply has no quota (enabled=0).
func (r *Repository) InterfaceQuota(ctx context.Context, interfaceID int64) (InterfaceQuotaPolicy, error) {
	policy := InterfaceQuotaPolicy{
		CountMode:     "attempt",
		RoleOverrides: make([]InterfaceRoleQuota, 0),
	}
	err := r.db.QueryRowContext(ctx, `
		select enabled, count_mode, cover_package,
		       per_user_hour, per_user_day, per_user_week, per_user_month,
		       all_users_hour, all_users_day, all_users_week, all_users_month
		from bp_interface_quota
		where interface_id = ?
	`, interfaceID).Scan(
		&policy.Enabled, &policy.CountMode, &policy.CoverPackage,
		&policy.PerUserHour, &policy.PerUserDay, &policy.PerUserWeek, &policy.PerUserMonth,
		&policy.AllUsersHour, &policy.AllUsersDay, &policy.AllUsersWeek, &policy.AllUsersMonth,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return policy, nil
	}
	if err != nil {
		return InterfaceQuotaPolicy{}, err
	}
	rows, err := r.db.QueryContext(ctx, `
		select q.role_id, coalesce(r.role_key, ''), coalesce(r.role_name, ''), q.mode,
		       q.adjust_hour, q.adjust_day, q.adjust_week, q.adjust_month,
		       q.override_hour, q.override_day, q.override_week, q.override_month
		from bp_interface_role_quota q
		left join bp_role r on r.id = q.role_id
		where q.interface_id = ?
		order by q.role_id
	`, interfaceID)
	if err != nil {
		return InterfaceQuotaPolicy{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item InterfaceRoleQuota
		if err := rows.Scan(
			&item.RoleID, &item.RoleKey, &item.RoleName, &item.Mode,
			&item.AdjustHour, &item.AdjustDay, &item.AdjustWeek, &item.AdjustMonth,
			&item.OverrideHour, &item.OverrideDay, &item.OverrideWeek, &item.OverrideMonth,
		); err != nil {
			return InterfaceQuotaPolicy{}, err
		}
		policy.RoleOverrides = append(policy.RoleOverrides, item)
	}
	return policy, rows.Err()
}

// SaveInterfaceQuota replaces the policy and role overrides in one transaction.
// Overrides with mode=inherit are omitted so the table only stores deviations.
func (r *Repository) SaveInterfaceQuota(ctx context.Context, interfaceID int64, policy InterfaceQuotaPolicy) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	roleIDs := make([]int64, 0, len(policy.RoleOverrides))
	seen := make(map[int64]struct{}, len(policy.RoleOverrides))
	for _, item := range policy.RoleOverrides {
		if strings.EqualFold(strings.TrimSpace(item.Mode), "inherit") {
			continue
		}
		if item.RoleID <= 0 {
			return ErrInterfaceQuotaRoleInvalid
		}
		if _, ok := seen[item.RoleID]; ok {
			return ErrInterfaceQuotaRoleInvalid
		}
		seen[item.RoleID] = struct{}{}
		roleIDs = append(roleIDs, item.RoleID)
	}
	if len(roleIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(roleIDs)), ",")
		args := make([]any, len(roleIDs))
		for index, id := range roleIDs {
			args[index] = id
		}
		var count int
		if err := tx.QueryRowContext(ctx, "select count(*) from bp_role where id in ("+placeholders+")", args...).Scan(&count); err != nil {
			return err
		}
		if count != len(roleIDs) {
			return ErrInterfaceQuotaRoleInvalid
		}
	}

	if _, err := tx.ExecContext(ctx, `
		insert into bp_interface_quota(
			interface_id, enabled, count_mode, cover_package,
			per_user_hour, per_user_day, per_user_week, per_user_month,
			all_users_hour, all_users_day, all_users_week, all_users_month
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on duplicate key update
			enabled = values(enabled),
			count_mode = values(count_mode),
			cover_package = values(cover_package),
			per_user_hour = values(per_user_hour),
			per_user_day = values(per_user_day),
			per_user_week = values(per_user_week),
			per_user_month = values(per_user_month),
			all_users_hour = values(all_users_hour),
			all_users_day = values(all_users_day),
			all_users_week = values(all_users_week),
			all_users_month = values(all_users_month)
	`, interfaceID, policy.Enabled, policy.CountMode, policy.CoverPackage,
		policy.PerUserHour, policy.PerUserDay, policy.PerUserWeek, policy.PerUserMonth,
		policy.AllUsersHour, policy.AllUsersDay, policy.AllUsersWeek, policy.AllUsersMonth); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_interface_role_quota where interface_id = ?", interfaceID); err != nil {
		return err
	}
	for _, item := range policy.RoleOverrides {
		if strings.EqualFold(strings.TrimSpace(item.Mode), "inherit") {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			insert into bp_interface_role_quota(
				interface_id, role_id, mode,
				adjust_hour, adjust_day, adjust_week, adjust_month,
				override_hour, override_day, override_week, override_month
			) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, interfaceID, item.RoleID, item.Mode,
			item.AdjustHour, item.AdjustDay, item.AdjustWeek, item.AdjustMonth,
			item.OverrideHour, item.OverrideDay, item.OverrideWeek, item.OverrideMonth); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Metering loads the interface's three-way metering policy plus per-role
// overrides. A missing policy row means every dimension is inherit.
func (r *Repository) Metering(ctx context.Context, interfaceID int64) (InterfaceMeteringPolicy, error) {
	policy := InterfaceMeteringPolicy{
		AssetMode:   meteringInherit,
		PackageMode: meteringInherit,
		GroupMode:   meteringInherit,
	}
	err := r.db.QueryRowContext(ctx, `
		select asset_mode, package_mode, group_mode
		from bp_interface_metering_policy
		where interface_id = ?
	`, interfaceID).Scan(&policy.AssetMode, &policy.PackageMode, &policy.GroupMode)
	if errors.Is(err, sql.ErrNoRows) {
		policy.AssetMode, policy.PackageMode, policy.GroupMode = meteringInherit, meteringInherit, meteringInherit
	} else if err != nil {
		return InterfaceMeteringPolicy{}, err
	}
	rows, err := r.db.QueryContext(ctx, `
		select m.role_id, coalesce(r.role_key, ''), coalesce(r.role_name, ''),
		       m.asset_mode, m.package_mode, m.group_mode
		from bp_interface_role_metering m
		left join bp_role r on r.id = m.role_id
		where m.interface_id = ?
		order by m.role_id
	`, interfaceID)
	if err != nil {
		return InterfaceMeteringPolicy{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item InterfaceRoleMetering
		if err := rows.Scan(&item.RoleID, &item.RoleKey, &item.RoleName,
			&item.AssetMode, &item.PackageMode, &item.GroupMode); err != nil {
			return InterfaceMeteringPolicy{}, err
		}
		policy.RoleOverrides = append(policy.RoleOverrides, item)
	}
	return policy, rows.Err()
}

// SaveMetering replaces the policy and its role overrides in one transaction.
// Overrides whose three dimensions are all inherit are omitted, so the table
// only stores actual deviations from the interface-level policy.
func (r *Repository) SaveMetering(ctx context.Context, interfaceID int64, policy InterfaceMeteringPolicy) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	roleIDs := make([]int64, 0, len(policy.RoleOverrides))
	seen := make(map[int64]struct{}, len(policy.RoleOverrides))
	for _, item := range policy.RoleOverrides {
		if isAllInheritMetering(item) {
			continue
		}
		if item.RoleID <= 0 {
			return ErrInterfaceMeteringRoleInvalid
		}
		if _, ok := seen[item.RoleID]; ok {
			return ErrInterfaceMeteringRoleInvalid
		}
		seen[item.RoleID] = struct{}{}
		roleIDs = append(roleIDs, item.RoleID)
	}
	if len(roleIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(roleIDs)), ",")
		args := make([]any, len(roleIDs))
		for index, id := range roleIDs {
			args[index] = id
		}
		var count int
		if err := tx.QueryRowContext(ctx, "select count(*) from bp_role where id in ("+placeholders+")", args...).Scan(&count); err != nil {
			return err
		}
		if count != len(roleIDs) {
			return ErrInterfaceMeteringRoleInvalid
		}
	}

	if _, err := tx.ExecContext(ctx, `
		insert into bp_interface_metering_policy(interface_id, asset_mode, package_mode, group_mode)
		values (?, ?, ?, ?)
		on duplicate key update
			asset_mode = values(asset_mode),
			package_mode = values(package_mode),
			group_mode = values(group_mode)
	`, interfaceID, policy.AssetMode, policy.PackageMode, policy.GroupMode); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_interface_role_metering where interface_id = ?", interfaceID); err != nil {
		return err
	}
	for _, item := range policy.RoleOverrides {
		if isAllInheritMetering(item) {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			insert into bp_interface_role_metering(interface_id, role_id, asset_mode, package_mode, group_mode)
			values (?, ?, ?, ?, ?)
		`, interfaceID, item.RoleID, item.AssetMode, item.PackageMode, item.GroupMode); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func isAllInheritMetering(item InterfaceRoleMetering) bool {
	inherited := func(mode string) bool {
		mode = strings.ToLower(strings.TrimSpace(mode))
		return mode == "" || mode == meteringInherit
	}
	return inherited(item.AssetMode) && inherited(item.PackageMode) && inherited(item.GroupMode)
}

// DegradePolicy loads the per-interface degrade policy and its per-role
// thresholds. A missing policy row is equivalent to "degrade disabled".
func (r *Repository) DegradePolicy(ctx context.Context, interfaceID int64) (InterfaceDegradePolicy, error) {
	policy := InterfaceDegradePolicy{
		PolicyVersion: 1,
		RoleOverrides: make([]InterfaceRoleDegrade, 0),
	}
	err := r.db.QueryRowContext(ctx, `
		select p.enabled, coalesce(p.default_target_interface_id, 0),
		       coalesce(t.api_code, ''), coalesce(t.name, ''),
		       coalesce(p.default_target_url, ''), p.policy_version
		from bp_interface_degrade_policy p
		left join bp_interface_api t on t.id = p.default_target_interface_id
		where p.interface_id = ?
	`, interfaceID).Scan(
		&policy.Enabled, &policy.DefaultTargetInterfaceID,
		&policy.DefaultTargetInterfaceCode, &policy.DefaultTargetInterfaceName,
		&policy.DefaultTargetURL, &policy.PolicyVersion,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return policy, nil
	}
	if err != nil {
		return InterfaceDegradePolicy{}, err
	}
	rows, err := r.db.QueryContext(ctx, `
		select d.role_id, coalesce(r.role_key, ''), coalesce(r.role_name, ''),
		       d.count_window, d.threshold, coalesce(d.target_interface_id, 0),
		       coalesce(t.api_code, ''), coalesce(t.name, ''), coalesce(d.target_url, '')
		from bp_interface_role_degrade d
		left join bp_role r on r.id = d.role_id
		left join bp_interface_api t on t.id = d.target_interface_id
		where d.interface_id = ?
		order by d.role_id
	`, interfaceID)
	if err != nil {
		return InterfaceDegradePolicy{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var item InterfaceRoleDegrade
		if err := rows.Scan(
			&item.RoleID, &item.RoleKey, &item.RoleName,
			&item.Window, &item.Threshold, &item.TargetInterfaceID,
			&item.TargetInterfaceCode, &item.TargetInterfaceName, &item.TargetURL,
		); err != nil {
			return InterfaceDegradePolicy{}, err
		}
		policy.RoleOverrides = append(policy.RoleOverrides, item)
	}
	return policy, rows.Err()
}

// SaveDegradePolicy replaces the policy and role thresholds in one transaction.
// Role rows with threshold 0 are not stored; a missing row means that role does
// not degrade.
func (r *Repository) SaveDegradePolicy(ctx context.Context, interfaceID int64, policy InterfaceDegradePolicy) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	roleIDs := make([]int64, 0, len(policy.RoleOverrides))
	targetIDs := make([]int64, 0, len(policy.RoleOverrides)+1)
	seenRoles := make(map[int64]struct{}, len(policy.RoleOverrides))
	seenTargets := make(map[int64]struct{}, len(policy.RoleOverrides)+1)
	addTarget := func(targetID int64) {
		if targetID <= 0 {
			return
		}
		if _, ok := seenTargets[targetID]; ok {
			return
		}
		seenTargets[targetID] = struct{}{}
		targetIDs = append(targetIDs, targetID)
	}
	addTarget(policy.DefaultTargetInterfaceID)
	for _, item := range policy.RoleOverrides {
		if item.Threshold <= 0 {
			continue
		}
		if item.RoleID <= 0 {
			return ErrInterfaceDegradeRoleInvalid
		}
		if _, ok := seenRoles[item.RoleID]; ok {
			return ErrInterfaceDegradeRoleInvalid
		}
		seenRoles[item.RoleID] = struct{}{}
		roleIDs = append(roleIDs, item.RoleID)
		addTarget(item.TargetInterfaceID)
	}
	if len(roleIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(roleIDs)), ",")
		args := make([]any, len(roleIDs))
		for index, id := range roleIDs {
			args[index] = id
		}
		var count int
		if err := tx.QueryRowContext(ctx, "select count(*) from bp_role where id in ("+placeholders+")", args...).Scan(&count); err != nil {
			return err
		}
		if count != len(roleIDs) {
			return ErrInterfaceDegradeRoleInvalid
		}
	}
	if len(targetIDs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(targetIDs)), ",")
		args := make([]any, len(targetIDs)+1)
		args[0] = interfaceID
		for index, id := range targetIDs {
			args[index+1] = id
		}
		var count int
		if err := tx.QueryRowContext(ctx,
			"select count(*) from bp_interface_api where status = 1 and id <> ? and id in ("+placeholders+")",
			args...,
		).Scan(&count); err != nil {
			return err
		}
		if count != len(targetIDs) {
			return ErrInterfaceDegradeTargetInvalid
		}
	}

	version := policy.PolicyVersion
	if version < 1 {
		version = 1
	}
	if _, err := tx.ExecContext(ctx, `
		insert into bp_interface_degrade_policy(interface_id, enabled, default_target_interface_id, default_target_url, policy_version)
		values (?, ?, ?, ?, ?)
		on duplicate key update
			enabled = values(enabled),
			default_target_interface_id = values(default_target_interface_id),
			default_target_url = values(default_target_url),
			policy_version = values(policy_version)
	`, interfaceID, policy.Enabled, nullInt64Default(policy.DefaultTargetInterfaceID), nullIfBlank(policy.DefaultTargetURL), version); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_interface_role_degrade where interface_id = ?", interfaceID); err != nil {
		return err
	}
	for _, item := range policy.RoleOverrides {
		if item.Threshold <= 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			insert into bp_interface_role_degrade(interface_id, role_id, count_window, threshold, target_interface_id, target_url)
			values (?, ?, ?, ?, ?, ?)
		`, interfaceID, item.RoleID, item.Window, item.Threshold, nullInt64Default(item.TargetInterfaceID), nullIfBlank(item.TargetURL)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nullInt64Default(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func (r *Repository) Delete(ctx context.Context, id int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var lockedID int64
	if err := tx.QueryRowContext(ctx, "select id from bp_interface_api where id = ? for update", id).Scan(&lockedID); err != nil {
		return err
	}
	var activeEntitlements int64
	if err := tx.QueryRowContext(ctx, `
		select count(*)
		from bp_user_package_interface
		where interface_id = ? and status = 1 and end_at > now()
	`, id).Scan(&activeEntitlements); err != nil {
		return err
	}
	if activeEntitlements > 0 {
		return ErrInterfaceHasActiveEntitlements
	}
	var pendingPayments int64
	if err := tx.QueryRowContext(ctx, `
		select count(*)
		from bp_payment_order o
		inner join bp_package_interface_spec s on s.id = o.target_id
		inner join bp_package_interface p on p.id = s.package_id
		where p.interface_id = ?
		  and o.order_type = 'INTERFACE_PACKAGE'
		  and o.status in ('CREATED','PAYING')
	`, id).Scan(&pendingPayments); err != nil {
		return err
	}
	if pendingPayments > 0 {
		return ErrInterfaceHasPendingPayments
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_api_key_interface where interface_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		delete s
		from bp_package_interface_spec s
		inner join bp_package_interface p on p.id = s.package_id
		where p.interface_id = ?
	`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_package_interface where interface_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_interface_billing_rule where interface_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_interface_role_quota where interface_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_interface_quota where interface_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_interface_role_metering where interface_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_interface_metering_policy where interface_id = ?", id); err != nil {
		return err
	}
	// 目标接口被删除时，引用它的降级配置自动失效（运行时 fail-open 回源接口），
	// 避免因为存在降级引用而无法删除目标接口。
	if _, err := tx.ExecContext(ctx, "update bp_interface_degrade_policy set default_target_interface_id = null where default_target_interface_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "update bp_interface_role_degrade set target_interface_id = null where target_interface_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_interface_role_degrade where interface_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_interface_degrade_policy where interface_id = ?", id); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "delete from bp_interface_api where id = ?", id)
	if err != nil {
		return err
	}
	if err := requireAffected(result); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) APICodeExists(ctx context.Context, code string, excludeID int64) (bool, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, "select count(*) from bp_interface_api where api_code = ? and id <> ?", code, excludeID).Scan(&count)
	return count > 0, err
}

func (r *Repository) SavePolling(ctx context.Context, id int64, cfg PollingConfig) error {
	existingPassword, err := r.storedProxyPoolPassword(ctx, id)
	if err != nil {
		return err
	}
	upstream, err := encodeUpstreamConfigDocumentForStorageWithRouting(r.cipher, cfg.ResponseFieldMappings, cfg.UpstreamConfigs, cfg.ProxyPoolConfig, cfg.DomainRouting, existingPassword)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		update bp_interface_api
                set polling_enabled = ?, polling_mode = ?, response_cleaning_enabled = ?, upstream_urls = ?, polling_check_enabled = ?,
		    polling_check_field = ?, polling_check_expected = ?, polling_stop_code = ?
		where id = ?
        `, cfg.PollingEnabled, cfg.PollingMode, cfg.ResponseCleaningEnabled, jsonOrNull(upstream), cfg.PollingCheckEnabled, nullIfBlank(cfg.PollingCheckField), nullIfBlank(cfg.PollingCheckExpected), nullIfBlank(cfg.PollingStopCode), id)
	if err != nil {
		return err
	}
	return r.requireAffectedOrExisting(ctx, id, result)
}

func (r *Repository) ProbeTargets(ctx context.Context) ([]ProbeTarget, error) {
	rows, err := r.db.QueryContext(ctx, `
                select id, request_url, request_method, polling_enabled, polling_mode, response_cleaning_enabled,
		       coalesce(cast(upstream_urls as char), '[]'),
		       polling_check_enabled, coalesce(polling_check_field, ''), coalesce(polling_check_expected, ''), coalesce(polling_stop_code, '')
		from bp_interface_api
		where status = 1 and (polling_enabled = 1 or (upstream_urls is not null and upstream_urls <> '[]'))
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []ProbeTarget
	for rows.Next() {
		var target ProbeTarget
		var id int64
		var upstreamText string
		if err := rows.Scan(&id, &target.RequestURL, &target.RequestMethod, &target.PollingEnabled, &target.PollingMode, &target.ResponseCleaningEnabled,
			&upstreamText, &target.PollingCheckEnabled, &target.PollingCheckField, &target.PollingCheckExpected, &target.PollingStopCode); err != nil {
			return nil, err
		}
		target.ID = strconv.FormatInt(id, 10)
		target.ResponseFieldMappings, target.UpstreamConfigs, target.ProxyPoolConfig, target.DomainRouting = decodeUpstreamConfigDocumentFullWithRouting(upstreamText)
		target.ProxyPoolConfig, err = decryptProxyPoolConfig(r.cipher, target.ProxyPoolConfig)
		if err != nil {
			return nil, fmt.Errorf("decrypt interface %d proxy password: %w", id, err)
		}
		targets = append(targets, target)
	}
	return targets, rows.Err()
}

func (r *Repository) SaveDoc(ctx context.Context, id int64, cfg DocConfig) error {
	result, err := r.db.ExecContext(ctx, `
		update bp_interface_api
		set doc_summary = ?, doc_response_type = ?, doc_preferred_method = ?, doc_request_params = ?,
		    doc_response_fields = ?, doc_response_example = ?, doc_status_codes = ?, doc_notice = ?,
		    doc_response_formats = ?
		where id = ?
	`, nullIfBlank(cfg.DocSummary), cfg.DocResponseType, nullIfBlank(cfg.DocPreferredMethod), jsonTextOrNull(cfg.DocRequestParams), jsonTextOrNull(cfg.DocResponseFields), nullIfBlank(cfg.DocResponseExample), jsonTextOrNull(cfg.DocStatusCodes), nullIfBlank(cfg.DocNotice), docFormatsJSONOrNull(cfg.DocResponseFormats), id)
	if err != nil {
		return err
	}
	return r.requireAffectedOrExisting(ctx, id, result)
}

// docFormatsJSONOrNull serializes the multi-format docs response. A nil slice
// binds SQL NULL so older clients that omit the field keep the stored value;
// a non-nil slice is stored verbatim (normalized by the service layer).
func docFormatsJSONOrNull(formats []DocResponseFormat) any {
	if formats == nil {
		return nil
	}
	encoded, err := json.Marshal(formats)
	if err != nil {
		return nil
	}
	return string(encoded)
}

func (r *Repository) BillingRules(ctx context.Context, id int64) ([]BillingRule, error) {
	rows, err := r.db.QueryContext(ctx, `
		select id, field_name, operator, expected_value, sort_no
		from bp_interface_billing_rule
		where interface_id = ?
		order by sort_no, id
	`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// 预初始化为空切片而非 nil，避免空结果被 JSON 编码为 null。
	// 前端契约要求返回 []，null 会在 `result.map(...)` 触发 TypeError 导致抽屉显示
	// "扣费规则加载失败"。参考 Go 惯例：切片 nil 与 empty 在语义等价，但 JSON 不同。
	rules := make([]BillingRule, 0)
	for rows.Next() {
		var rule BillingRule
		var ruleID int64
		if err := rows.Scan(&ruleID, &rule.FieldName, &rule.Operator, &rule.ExpectedValue, &rule.SortNo); err != nil {
			return nil, err
		}
		rule.ID = strconv.FormatInt(ruleID, 10)
		rules = append(rules, rule)
	}
	return rules, rows.Err()
}

func (r *Repository) ReplaceBillingRules(ctx context.Context, id int64, rules []BillingRule, settings ...SaveBillingRulesRequest) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if len(settings) > 0 {
		setting := settings[0]
		if _, err := tx.ExecContext(ctx, "update bp_interface_api set billing_enabled=coalesce(?,billing_enabled), billing_log_enabled=coalesce(?,billing_log_enabled) where id=?", setting.BillingEnabled, setting.BillingLogEnabled, id); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "delete from bp_interface_billing_rule where interface_id = ?", id); err != nil {
		return err
	}
	for i, rule := range rules {
		sortNo := rule.SortNo
		if sortNo <= 0 {
			sortNo = i + 1
		}
		if _, err := tx.ExecContext(ctx, `
			insert into bp_interface_billing_rule(interface_id, field_name, operator, expected_value, sort_no)
			values (?, ?, ?, ?, ?)
		`, id, rule.FieldName, rule.Operator, rule.ExpectedValue, sortNo); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (r *Repository) RequestValidationPolicy(ctx context.Context, id int64) (RequestValidationPolicy, error) {
	var item RequestValidationPolicy
	var numericID int64
	var rulesText string
	err := r.db.QueryRowContext(ctx, `
		select id, name, request_validation_enabled,
		       coalesce(request_validation_default_result, 'PASS'),
		       coalesce(cast(request_validation_rules as char), '[]')
		from bp_interface_api
		where id = ?
	`, id).Scan(&numericID, &item.Name, &item.Enabled, &item.DefaultResult, &rulesText)
	if err != nil {
		return RequestValidationPolicy{}, err
	}
	if err := json.Unmarshal([]byte(rulesText), &item.Rules); err != nil {
		return RequestValidationPolicy{}, fmt.Errorf("decode request validation rules: %w", err)
	}
	normalized, err := requestvalidation.Normalize(item.Policy)
	if err != nil {
		return RequestValidationPolicy{}, fmt.Errorf("normalize stored request validation policy: %w", err)
	}
	item.Policy = normalized
	item.ID = strconv.FormatInt(numericID, 10)
	return item, nil
}

func (r *Repository) SaveRequestValidationPolicy(ctx context.Context, id int64, policy requestvalidation.Policy) error {
	rules, err := json.Marshal(policy.Rules)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		update bp_interface_api
		set request_validation_enabled = ?, request_validation_default_result = ?, request_validation_rules = ?
		where id = ?
	`, policy.Enabled, policy.DefaultResult, jsonOrNull(rules), id)
	if err != nil {
		return err
	}
	return r.requireAffectedOrExisting(ctx, id, result)
}

func (r *Repository) DeliveryPolicy(ctx context.Context, id int64) (DeliveryPolicy, error) {
	var policy DeliveryPolicy
	var cacheRulesText string
	var responseAdID sql.NullInt64
	var rawID int64
	var globalRequestTimeout int
	err := r.db.QueryRowContext(ctx, `
		select id,name,cache_policy_override,cache_enabled,cache_default_ttl_seconds,coalesce(cast(cache_rules as char),'[]'),
		       response_ad_enabled,response_ad_id,response_ad_field,response_ad_policy_override,delivery_policy_version,
		       coalesce(request_timeout_ms,0),coalesce(request_timeout_override,0),
		       coalesce((select request_timeout_ms from bp_interface_global_delivery_policy where id=1),15000)
		from bp_interface_api where id=?
	`, id).Scan(&rawID, &policy.Name, &policy.CachePolicyOverride, &policy.CacheEnabled, &policy.CacheDefaultTTLSeconds, &cacheRulesText,
		&policy.ResponseAdEnabled, &responseAdID, &policy.ResponseAdField, &policy.ResponseAdPolicyOverride, &policy.PolicyVersion,
		&policy.RequestTimeoutMs, &policy.RequestTimeoutOverride, &globalRequestTimeout)
	if err != nil {
		return DeliveryPolicy{}, err
	}
	policy.ID = strconv.FormatInt(rawID, 10)
	policy.Scope = "INTERFACE"
	if responseAdID.Valid {
		policy.ResponseAdID = responseAdID.Int64
	}
	policy.CacheRules = decodeResponseCacheRules(cacheRulesText)
	policy.EffectiveRequestTimeoutMs = requesttimeout.ResolveMilliseconds(
		policy.RequestTimeoutMs, globalRequestTimeout, policy.RequestTimeoutOverride,
	)
	return policy, nil
}

func (r *Repository) SaveDeliveryPolicy(ctx context.Context, id int64, policy DeliveryPolicy) error {
	rules, err := json.Marshal(policy.CacheRules)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		update bp_interface_api
		set cache_policy_override=?,cache_enabled=?,cache_default_ttl_seconds=?,cache_rules=?,
		    response_ad_enabled=?,response_ad_id=?,response_ad_field=?,response_ad_policy_override=?,
		    request_timeout_ms=?,request_timeout_override=?,
		    delivery_policy_version=delivery_policy_version+1
		where id=?
	`, policy.CachePolicyOverride, policy.CacheEnabled, policy.CacheDefaultTTLSeconds, jsonOrNull(rules), policy.ResponseAdEnabled,
		nullIfZero(policy.ResponseAdID), policy.ResponseAdField, policy.ResponseAdPolicyOverride,
		policy.RequestTimeoutMs, policy.RequestTimeoutOverride, id)
	if err != nil {
		return err
	}
	return r.requireAffectedOrExisting(ctx, id, result)
}

func (r *Repository) GlobalDeliveryPolicy(ctx context.Context) (DeliveryPolicy, error) {
	var policy DeliveryPolicy
	var cacheRulesText string
	var responseAdID sql.NullInt64
	var rawID int64
	var requestTimeoutMs int
	err := r.db.QueryRowContext(ctx, `
		select id,cache_enabled,cache_default_ttl_seconds,coalesce(cast(cache_rules as char),'[]'),
		       response_ad_enabled,response_ad_id,response_ad_field,policy_version,
		       coalesce(request_timeout_ms,15000)
		from bp_interface_global_delivery_policy where id=1
	`).Scan(&rawID, &policy.CacheEnabled, &policy.CacheDefaultTTLSeconds, &cacheRulesText,
		&policy.ResponseAdEnabled, &responseAdID, &policy.ResponseAdField, &policy.PolicyVersion,
		&requestTimeoutMs)
	if err != nil {
		return DeliveryPolicy{}, err
	}
	policy.ID = strconv.FormatInt(rawID, 10)
	policy.Name = "GLOBAL"
	policy.Scope = "GLOBAL"
	policy.CachePolicyOverride = 1
	policy.ResponseAdPolicyOverride = 1
	policy.RequestTimeoutMs = requestTimeoutMs
	policy.RequestTimeoutOverride = 1
	policy.EffectiveRequestTimeoutMs = requesttimeout.ResolveMilliseconds(requestTimeoutMs, requestTimeoutMs, 1)
	if responseAdID.Valid {
		policy.ResponseAdID = responseAdID.Int64
	}
	policy.CacheRules = decodeResponseCacheRules(cacheRulesText)
	return policy, nil
}

func (r *Repository) SaveGlobalDeliveryPolicy(ctx context.Context, policy DeliveryPolicy, updaterID int64) error {
	rules, err := json.Marshal(policy.CacheRules)
	if err != nil {
		return err
	}
	result, err := r.db.ExecContext(ctx, `
		update bp_interface_global_delivery_policy
		set cache_enabled=?,cache_default_ttl_seconds=?,cache_rules=?,
		    response_ad_enabled=?,response_ad_id=?,response_ad_field=?,
		    request_timeout_ms=?,
		    policy_version=policy_version+1,updated_by=?
		where id=1
	`, policy.CacheEnabled, policy.CacheDefaultTTLSeconds, jsonOrNull(rules), policy.ResponseAdEnabled,
		nullIfZero(policy.ResponseAdID), policy.ResponseAdField, policy.RequestTimeoutMs, updaterID)
	if err != nil {
		return err
	}
	return r.requireGlobalAffectedOrExisting(ctx, result)
}

func (r *Repository) ResponseAdvertisementExists(ctx context.Context, id int64) (bool, error) {
	var count int64
	err := r.db.QueryRowContext(ctx, `
		select count(*) from bp_advertisement where id=? and placement='API_RESPONSE'
	`, id).Scan(&count)
	return count > 0, err
}

func (r *Repository) ResponseAdvertisementOptions(ctx context.Context) ([]ResponseAdvertisementOption, error) {
	rows, err := r.db.QueryContext(ctx, `
		select id,name,title,status,
		       coalesce(date_format(start_at,'%Y-%m-%d %H:%i:%s'),''),
		       coalesce(date_format(end_at,'%Y-%m-%d %H:%i:%s'),'')
		from bp_advertisement
		where placement='API_RESPONSE'
		order by status desc,sort_no desc,id desc
		limit 200
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]ResponseAdvertisementOption, 0)
	for rows.Next() {
		var item ResponseAdvertisementOption
		var id int64
		if err := rows.Scan(&id, &item.Name, &item.Title, &item.Status, &item.StartTime, &item.EndTime); err != nil {
			return nil, err
		}
		item.ID = strconv.FormatInt(id, 10)
		items = append(items, item)
	}
	return items, rows.Err()
}

func decodeResponseCacheRules(text string) []ResponseCacheRule {
	var rules []ResponseCacheRule
	if json.Unmarshal([]byte(text), &rules) != nil {
		return nil
	}
	return rules
}

func (r *Repository) UpdateCurrentNode(ctx context.Context, id int64, node int) error {
	_, err := r.db.ExecContext(ctx, "update bp_interface_api set current_node = ? where id = ?", node, id)
	return err
}

func (r *Repository) CreateCallLog(ctx context.Context, item InterfaceItem, principal Principal, keyID int64, keyPrefix string, result InvokeResult, requestParams, requestValue, errorMessage, clientIP string) error {
	body := responseBodyForLog(result.Body)
	requestParams = requestParamsForPersistence(requestParams)
	requestValue = requestValueForPersistence(requestValue)
	errorMessage = safeDiagnosticText(errorMessage, 500)
	upstreamURL := masking.UpstreamReference(result.UpstreamURL)
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	resultExec, err := tx.ExecContext(ctx, `
		insert into bp_interface_call_log(interface_id, user_id, api_key_id, api_key_prefix, api_code, request_method, request_params, request_value,
			request_size, response_status, success, billable, charge_type, charge_amount, point_price, elapsed_ms,
			polling_mode, upstream_url, upstream_switched, error_message, client_ip, response_size, response_saved_size, response_truncated, user_visible, trace_id)
		values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'ADMIN', 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?)
	`, parseID(item.ID), principal.ID, nullIfZero(keyID), nullIfBlank(keyPrefix), nullIfBlank(item.APICode), result.RequestMethod, nullIfBlank(requestParams), nullIfBlank(requestValue),
		result.RequestSize, result.StatusCode, boolInt(result.Success), boolInt(result.Billable), result.PointPrice, result.ElapsedMs,
		nullIfBlank(result.PollingMode), nullIfBlank(upstreamURL), boolInt(result.UpstreamSwitched), nullIfBlank(errorMessage), nullIfBlank(clientIP), responseSizeForInvokeLog(result), int64(len(body)), boolInt(responseWasTruncatedForResult(result.Body, result.ResponseSize, result.ResponseNormalized)), nullIfBlank(result.RequestID))
	if err != nil {
		return err
	}
	// The response body lives in the side table, written in the same
	// transaction as the main insert so they can never diverge.
	if body != "" {
		logID, err := resultExec.LastInsertId()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `insert into bp_interface_call_log_body(log_id, response_body) values (?, ?)`, logID, body); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func responseSizeForInvokeLog(result InvokeResult) int64 {
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
	// Normalization is an intentional representation change, not persistence
	// truncation. Keep the size-limit and invalid-byte checks active.
	if !utf8.ValidString(body) || len(body) > 20000 {
		return true
	}
	return false
}

// responseBodyForLog 同 gateway 侧约定：响应体保留调用方可读的业务 URL
// （解析结果里的直链等），仅脱敏凭据、URL 查询敏感值与内网地址；上游地址
// 由独立的 upstream_url 列保护，不做无差别端点脱敏。
func responseBodyForLog(value string) string {
	if value == "" {
		return ""
	}
	if !utf8.ValidString(value) {
		return "[BINARY_RESPONSE_OMITTED]"
	}
	return truncateUTF8Bytes(masking.ResponseJSONText(value), 20000)
}

// requestParamsForPersistence 同 gateway 侧约定：保留业务 URL/参数可读，
// 仅做 JSON 内敏感字段 + URL 查询敏感 key 的脱敏，不再端点脱敏。
func requestParamsForPersistence(value string) string {
	return truncateUTF8Bytes(masking.SensitiveJSONTextWithURLs(value), 20000)
}

func requestValueForPersistence(value string) string {
	return truncateUTF8Bytes(masking.Text(value), 500)
}

func safeDiagnosticText(value string, limit int) string {
	return truncateRunes(strings.TrimSpace(masking.ErrorText(value)), limit)
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

func (r *Repository) InterfaceTrend7d(ctx context.Context, interfaceID int64) ([]map[string]any, error) {
	now := businessclock.Now()
	result := make([]map[string]any, 0, 7)
	index := map[string]int{}
	for i := 6; i >= 0; i-- {
		day := now.AddDate(0, 0, -i)
		key := day.Format("2006-01-02")
		index[key] = len(result)
		result = append(result, map[string]any{"date": key, "label": day.Format("01-02"), "value": int64(0)})
	}
	// Use the same captured business date for the grid and SQL bounds; a
	// database midnight between these steps must not drop the first visible day.
	start := now.AddDate(0, 0, -6).Format("2006-01-02")
	end := now.AddDate(0, 0, 1).Format("2006-01-02")
	rows, err := r.db.QueryContext(ctx, interfaceTrend7dQuery(), interfaceID, start, end)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var count int64
		if err := rows.Scan(&day, &count); err != nil {
			return result, err
		}
		if i, ok := index[day]; ok {
			result[i]["value"] = count
		}
	}
	return result, rows.Err()
}

func normalizePage(page, pageSize int) (int, int) {
	return pagination.Normalize(page, pageSize, 100)
}

func requireAffected(result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) requireAffectedOrExisting(ctx context.Context, id int64, result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	var count int
	if err := r.db.QueryRowContext(ctx, "select count(*) from bp_interface_api where id = ?", id).Scan(&count); err != nil {
		return err
	}
	if count == 1 {
		return nil
	}
	return sql.ErrNoRows
}

func (r *Repository) requireGlobalAffectedOrExisting(ctx context.Context, result sql.Result) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	var count int
	if err := r.db.QueryRowContext(ctx, "select count(*) from bp_interface_global_delivery_policy where id=1").Scan(&count); err != nil {
		return err
	}
	if count == 1 {
		return nil
	}
	return sql.ErrNoRows
}

type upstreamConfigDocument struct {
	DomainRouting                *DomainRoutingConfig        `json:"domainRouting,omitempty"`
	PrimaryResponseFieldMappings []jsoncleaning.FieldMapping `json:"primaryResponseFieldMappings,omitempty"`
	ProxyPoolConfig              proxypool.Config            `json:"proxyPoolConfig,omitempty"`
	Upstreams                    []UpstreamConfig            `json:"upstreams,omitempty"`
}

func encodeUpstreamConfigDocument(primaryMappings []jsoncleaning.FieldMapping, configs []UpstreamConfig) ([]byte, error) {
	return encodeUpstreamConfigDocumentWithProxy(primaryMappings, configs, proxypool.Config{})
}

func encodeUpstreamConfigDocumentWithProxy(primaryMappings []jsoncleaning.FieldMapping, configs []UpstreamConfig, proxyConfig proxypool.Config) ([]byte, error) {
	return encodeUpstreamConfigDocumentWithProxyAndRouting(primaryMappings, configs, proxyConfig, DomainRoutingConfig{})
}

func encodeUpstreamConfigDocumentWithProxyAndRouting(primaryMappings []jsoncleaning.FieldMapping, configs []UpstreamConfig, proxyConfig proxypool.Config, routing DomainRoutingConfig) ([]byte, error) {
	// Keep the historical array representation when the primary interface has
	// no aliases. Existing installations and manual database tooling therefore
	// continue to see the same JSON shape unless the new setting is used.
	if len(primaryMappings) == 0 && proxyConfig.Enabled == 0 && proxyConfig.Source == "" && !domainRoutingConfigured(routing) {
		return json.Marshal(configs)
	}
	document := upstreamConfigDocument{
		PrimaryResponseFieldMappings: primaryMappings,
		ProxyPoolConfig:              proxyConfig,
		Upstreams:                    configs,
	}
	if domainRoutingConfigured(routing) {
		document.DomainRouting = &routing
	}
	return json.Marshal(document)
}

func encodeUpstreamConfigDocumentForStorage(cipher *secretbox.Cipher, primaryMappings []jsoncleaning.FieldMapping, configs []UpstreamConfig, proxyConfig proxypool.Config, existingPassword string) ([]byte, error) {
	return encodeUpstreamConfigDocumentForStorageWithRouting(cipher, primaryMappings, configs, proxyConfig, DomainRoutingConfig{}, existingPassword)
}

func encodeUpstreamConfigDocumentForStorageWithRouting(cipher *secretbox.Cipher, primaryMappings []jsoncleaning.FieldMapping, configs []UpstreamConfig, proxyConfig proxypool.Config, routing DomainRoutingConfig, existingPassword string) ([]byte, error) {
	password := strings.TrimSpace(proxyConfig.Password)
	if password == "" && proxyConfig.PasswordConfigured {
		password = strings.TrimSpace(existingPassword)
	}
	if password != "" {
		if secretbox.IsEncrypted(password) {
			if _, err := cipher.DecryptCompatible(password); err != nil {
				return nil, err
			}
		} else {
			encrypted, err := cipher.Encrypt(password)
			if err != nil {
				return nil, err
			}
			password = encrypted
		}
	}
	proxyConfig.Password = password
	proxyConfig.PasswordConfigured = false
	return encodeUpstreamConfigDocumentWithProxyAndRouting(primaryMappings, configs, proxyConfig, routing)
}

func decryptProxyPoolConfig(cipher *secretbox.Cipher, proxyConfig proxypool.Config) (proxypool.Config, error) {
	stored := strings.TrimSpace(proxyConfig.Password)
	if stored == "" {
		proxyConfig.Password = ""
		proxyConfig.PasswordConfigured = false
		return proxyConfig, nil
	}
	password, err := cipher.DecryptCompatible(stored)
	if err != nil {
		return proxypool.Config{}, err
	}
	proxyConfig.Password = password
	proxyConfig.PasswordConfigured = true
	return proxyConfig, nil
}

func redactProxyPoolConfig(proxyConfig proxypool.Config) proxypool.Config {
	proxyConfig.PasswordConfigured = proxyConfig.PasswordConfigured || strings.TrimSpace(proxyConfig.Password) != ""
	proxyConfig.Password = ""
	return proxyConfig
}

func (r *Repository) storedProxyPoolPassword(ctx context.Context, id int64) (string, error) {
	var text string
	if err := r.db.QueryRowContext(ctx, "select coalesce(cast(upstream_urls as char), '[]') from bp_interface_api where id = ?", id).Scan(&text); err != nil {
		return "", err
	}
	_, _, proxyConfig := decodeUpstreamConfigDocumentFull(text)
	return strings.TrimSpace(proxyConfig.Password), nil
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
	if err := json.Unmarshal([]byte(text), &document); err == nil && (document.Upstreams != nil || document.PrimaryResponseFieldMappings != nil || document.ProxyPoolConfig.Source != "" || document.DomainRouting != nil) {
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

func domainRoutingConfigured(config DomainRoutingConfig) bool {
	return config.Enabled != 0 || strings.TrimSpace(config.Fallback) != "" || strings.TrimSpace(config.DefaultRouteID) != "" || len(config.Routes) > 0
}

func decodeUpstreamConfigs(text string) []UpstreamConfig {
	_, configs := decodeUpstreamConfigDocument(text)
	return configs
}

func upstreamURLs(configs []UpstreamConfig) []string {
	urls := make([]string, 0, len(configs))
	for _, cfg := range configs {
		if strings.TrimSpace(cfg.URL) != "" {
			urls = append(urls, cfg.URL)
		}
	}
	return urls
}

func currentNodeURL(item InterfaceItem) string {
	if item.PollingEnabled != 1 {
		return ""
	}
	mode := normalizedPollingMode(item.PollingMode, item.PollingEnabled)
	if mode == "PRIMARY" || mode == "FASTEST" {
		if item.CurrentNode <= 1 {
			return item.RequestURL
		}
		index := item.CurrentNode - 2
		if index >= 0 && index < len(item.UpstreamURLs) {
			return item.UpstreamURLs[index]
		}
		return item.RequestURL
	}
	if len(item.UpstreamURLs) == 0 {
		return item.RequestURL
	}
	index := item.CurrentNode - 1
	if index < 0 || index >= len(item.UpstreamURLs) {
		index = 0
	}
	return item.UpstreamURLs[index]
}

func displayUpstreamCount(item InterfaceItem) int {
	if item.PollingEnabled != 1 {
		return 1
	}
	switch normalizedPollingMode(item.PollingMode, item.PollingEnabled) {
	case "FASTEST", "PRIMARY":
		return len(item.UpstreamURLs) + 1
	case "ROUND_ROBIN":
		if len(item.UpstreamURLs) > 0 {
			return len(item.UpstreamURLs)
		}
	}
	return 1
}

func priceLabel(price string, points int64) string {
	if points > 0 {
		return fmt.Sprintf("%d点/次", points)
	}
	if price == "" || price == "0.0000" {
		return "免费"
	}
	return price + "元/次"
}

func nullIfBlank(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func nullIfZero(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func jsonOrNull(value []byte) any {
	if len(value) == 0 || string(value) == "null" || string(value) == "[]" {
		return nil
	}
	return string(value)
}

func jsonTextOrNull(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.TrimSpace(value)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 || value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

func parseID(value string) int64 {
	id, _ := strconv.ParseInt(value, 10, 64)
	return id
}
