package gateway

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"bugpk-api/internal/pkg/businessclock"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

// 配额概览把「免费额度」和「付费额度」分成两块返回，二者不合并成一个数字。
//
// 免费额度（Free）来自用户组，按分/时/日/月四个固定窗口计数，窗口一到就重置；
// 付费额度（Paid）来自套餐 / 点数 / 余额，按权益自身有效期存续。网关的裁决顺序
// 是「先消费免费额度，打满后由付费额度承接」，所以这两块在前端必须分开显示，
// 否则用户无法判断自己是在用免费的次数还是在烧买来的额度。
type QuotaOverview struct {
	Role QuotaRoleBrief     `json:"role"`
	Free []QuotaWindowUsage `json:"free"`
	New  *QuotaWindowUsage  `json:"newAccount,omitempty"`
	Paid QuotaPaidUsage     `json:"paid"`
	// CounterAvailable 为 false 表示 Redis 计数不可读（例如计数器不可达），
	// 此时 Used 一律为 0，前端应提示「用量暂时不可读」而不是显示 0 用量。
	CounterAvailable bool `json:"counterAvailable"`
}

type QuotaRoleBrief struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Key          string `json:"key"`
	QuotaEnabled bool   `json:"quotaEnabled"`
	CountMode    string `json:"countMode"`
}

// QuotaWindowUsage 是单个固定窗口的用量。Limit 为 0 表示该窗口不限量，
// 此时 Remaining 固定为 -1（不是 0），避免前端把「不限」渲染成「已用尽」。
type QuotaWindowUsage struct {
	Window    string `json:"window"`
	Limit     int64  `json:"limit"`
	Used      int64  `json:"used"`
	Remaining int64  `json:"remaining"`
	ResetAt   string `json:"resetAt"`
	Exhausted bool   `json:"exhausted"`
}

type QuotaPaidUsage struct {
	Packages  []QuotaPackageUsage `json:"packages"`
	Points    int64               `json:"points"`
	Balance   string              `json:"balance"`
	Available bool                `json:"available"`
}

type QuotaPackageUsage struct {
	// ID 形如 "GLOBAL:12" / "INTERFACE:7"，主套餐接口需要数字部分 + scope。
	ID         string `json:"id"`
	Name       string `json:"name"`
	Scope      string `json:"scope"`
	IsPrimary  bool   `json:"isPrimary"`
	DailyLimit int64  `json:"dailyLimit"`
	UsedToday  int64  `json:"usedToday"`
	// TotalLimit 为套餐生命周期总调用量快照，0 = 不限；
	// TotalUsed 为累计消耗（含失败计量，取决于权益行的 count_mode）。
	TotalLimit int64  `json:"totalLimit"`
	TotalUsed  int64  `json:"totalUsed"`
	CountMode  string `json:"countMode"`
	// Remaining 取日限与总量中更紧的那个：两边都不限时为 -1（前端渲染「不限」），
	// 任一限时时为「min(日剩余, 总剩余)」，与网关「任一打满即不可用」的裁决一致。
	Remaining int64  `json:"remaining"`
	EndAt     string `json:"endAt"`
	Exhausted bool   `json:"exhausted"`
	// 源套餐/规格 ID，仅用于按请求语言解析名称；不进 JSON（小写非导出）。
	packageID int64
	specID    int64
}

type quotaProfile struct {
	UserID             int64
	RoleID             int64
	RoleName           string
	RoleKey            string
	QuotaEnabled       int
	MinuteLimit        int64
	HourLimit          int64
	DayLimit           int64
	MonthLimit         int64
	CountMode          string
	NewAccountDayLimit int64
	UserCreatedAt      time.Time
	Points             int64
	Balance            string
}

// QuotaOverview 组装用户当前的额度画像。只读 Redis 计数，不消费任何额度。
func (s *Service) QuotaOverview(ctx context.Context, userID int64) (QuotaOverview, error) {
	profile, err := s.repo.QuotaProfile(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// 令牌有效但账号已被删除：给 404，避免前端把 500 当成服务故障。
			return QuotaOverview{}, apperrors.New(consts.StatusNotFound, "用户不存在")
		}
		return QuotaOverview{}, err
	}
	overview := QuotaOverview{
		Role: QuotaRoleBrief{
			ID:           profile.RoleID,
			Name:         s.localizeRoleName(ctx, profile.RoleID, profile.RoleName),
			Key:          profile.RoleKey,
			QuotaEnabled: profile.QuotaEnabled == 1,
			CountMode:    profile.CountMode,
		},
		Free: make([]QuotaWindowUsage, 0, 4),
	}
	key := KeyAuth{
		UserID:             profile.UserID,
		RoleID:             profile.RoleID,
		RoleKey:            profile.RoleKey,
		UserCreatedAt:      profile.UserCreatedAt,
		QuotaEnabled:       profile.QuotaEnabled,
		QuotaMinuteLimit:   profile.MinuteLimit,
		QuotaHourLimit:     profile.HourLimit,
		QuotaDayLimit:      profile.DayLimit,
		QuotaMonthLimit:    profile.MonthLimit,
		QuotaCountMode:     profile.CountMode,
		NewAccountDayLimit: profile.NewAccountDayLimit,
	}
	// 复用真实请求的窗口构造器：键名与 TTL 必须与闸门完全一致，否则概览读到的
	// 是一组永远不会被请求自增的键，用量会恒为 0。
	windows := s.requestQuotaWindows(key, InterfaceConfig{})
	used := s.quotaWindowUsages(ctx, windows)
	overview.CounterAvailable = used != nil
	if used == nil {
		used = map[string]int64{}
	}
	now := s.currentTime().In(businessclock.Location())
	for _, window := range windows {
		item := QuotaWindowUsage{
			Window:  window.Name,
			Limit:   window.Limit,
			Used:    used[window.Key],
			ResetAt: now.Add(window.TTL).Format("2006-01-02 15:04:05"),
		}
		if window.Limit <= 0 {
			item.Remaining = -1
		} else {
			item.Remaining = window.Limit - item.Used
			if item.Remaining < 0 {
				item.Remaining = 0
			}
			item.Exhausted = item.Used >= window.Limit
		}
		// TTL 含宽限时间（分钟窗口 +1min、小时 +5min），直接加到 now 会比真实
		// 重置时间晚一点；对「还剩多久重置」的展示足够，且不需要再解析 bucket。
		if window.Name == quotaWindowNewAccount {
			registeredAt := profile.UserCreatedAt.In(businessclock.Location())
			item.ResetAt = registeredAt.Add(24 * time.Hour).Format("2006-01-02 15:04:05")
			overview.New = &item
			continue
		}
		overview.Free = append(overview.Free, item)
	}
	packages, err := s.repo.PaidPackageUsages(ctx, userID)
	if err != nil {
		return QuotaOverview{}, err
	}
	overview.Paid = QuotaPaidUsage{
		Packages: s.localizePackageUsages(ctx, packages),
		Points:   profile.Points,
		Balance:  profile.Balance,
	}
	// 当日或总量已耗尽的套餐仍要展示（用户需要知道「这个包用完了」），
	// 但不能算作「还有可用付费途径」——否则概览会说「可用」，实际调用却会
	// 因 free 打满 + 套餐用尽而 429。
	usablePackages := 0
	for _, item := range packages {
		if (item.DailyLimit <= 0 && item.TotalLimit <= 0) || item.Remaining > 0 {
			usablePackages++
		}
	}
	overview.Paid.Available = usablePackages > 0 || profile.Points > 0 || positiveAmount(profile.Balance)
	return overview, nil
}

// localizeRoleName 按请求语言解析用户组名；无译文或字典不可用时返回原文。
func (s *Service) localizeRoleName(ctx context.Context, roleID int64, fallback string) string {
	if s.localizer == nil || roleID <= 0 || fallback == "" {
		return fallback
	}
	if value, ok := s.localizer.Localize(ctx, "role", strconv.FormatInt(roleID, 10))["roleName"]; ok {
		return value
	}
	return fallback
}

// localizePackageUsages 按请求语言解析套餐名（全站套餐名 / 接口套餐「套餐名 / 规格名」）。
// 名称快照存的是购买时的原文，译文缺失时保留快照原文。
func (s *Service) localizePackageUsages(ctx context.Context, items []QuotaPackageUsage) []QuotaPackageUsage {
	if s.localizer == nil || len(items) == 0 {
		return items
	}
	globalKeys := make([]string, 0, len(items))
	specKeys := make([]string, 0, len(items))
	for _, item := range items {
		if item.packageID > 0 {
			globalKeys = append(globalKeys, strconv.FormatInt(item.packageID, 10))
		}
		if item.specID > 0 {
			specKeys = append(specKeys, strconv.FormatInt(item.specID, 10))
		}
	}
	globalOverrides := s.localizer.LocalizeBatch(ctx, "package_global", globalKeys)
	specOverrides := s.localizer.LocalizeBatch(ctx, "package_interface_spec", specKeys)
	for index := range items {
		item := &items[index]
		if item.Scope == "INTERFACE" {
			name, ok := globalOverrides[strconv.FormatInt(item.packageID, 10)]["name"]
			if !ok || name == "" {
				continue
			}
			spec, ok := specOverrides[strconv.FormatInt(item.specID, 10)]["specName"]
			if !ok || spec == "" {
				continue
			}
			item.Name = name + " / " + spec
			continue
		}
		if name, ok := globalOverrides[strconv.FormatInt(item.packageID, 10)]["name"]; ok && name != "" {
			item.Name = name
		}
	}
	return items
}

// quotaWindowUsages 一次 MGET 读回全部窗口计数。返回 nil 表示计数器不可读，
// 由调用方降级为「用量未知」；不把 Redis 故障放大成 500，否则用户连看额度
// 都看不了。
func (s *Service) quotaWindowUsages(ctx context.Context, windows []quotaWindowSpec) map[string]int64 {
	if len(windows) == 0 {
		return map[string]int64{}
	}
	if s.rdb == nil {
		return nil
	}
	keys := make([]string, len(windows))
	for index, window := range windows {
		keys[index] = window.Key
	}
	values, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		if s.log != nil {
			s.log.Warn("gateway quota overview mget failed: " + err.Error())
		}
		return nil
	}
	result := make(map[string]int64, len(values))
	for index, value := range values {
		if index >= len(windows) {
			break
		}
		text, ok := value.(string)
		if !ok || text == "" {
			continue
		}
		count, err := strconv.ParseInt(text, 10, 64)
		if err != nil || count < 0 {
			continue
		}
		result[windows[index].Key] = count
	}
	return result
}

func positiveAmount(amount string) bool {
	value, err := strconv.ParseFloat(amount, 64)
	if err != nil {
		return false
	}
	return value > 0
}

// QuotaProfile 读取用户组配额、注册时间与余额/点数快照。
// 角色解析顺序与 KeyAuthByHash 一致：主角色优先，未设置时回退注册默认角色，
// 否则概览显示的额度会和闸门实际使用的额度对不上。
func (r *Repository) QuotaProfile(ctx context.Context, userID int64) (quotaProfile, error) {
	var item quotaProfile
	err := r.db.QueryRowContext(ctx, `
		select u.id,
		       coalesce(primary_role.id, default_role.id, 0),
		       coalesce(primary_role.role_name, default_role.role_name, ''),
		       coalesce(primary_role.role_key, default_role.role_key, ''),
		       coalesce(primary_role.request_quota_enabled, default_role.request_quota_enabled, 0),
		       coalesce(primary_role.request_quota_minute, default_role.request_quota_minute, 0),
		       coalesce(primary_role.request_quota_hour, default_role.request_quota_hour, 0),
		       coalesce(primary_role.request_quota_day, default_role.request_quota_day, 0),
		       coalesce(primary_role.request_quota_month, default_role.request_quota_month, 0),
		       coalesce(primary_role.request_quota_count_mode, default_role.request_quota_count_mode, 'attempt'),
		       coalesce(cfg.new_account_day_limit, 0),
		       u.created_at,
		       coalesce(u.points, 0),
		       cast(coalesce(u.balance, 0) as char)
		from bp_user u
		left join bp_role primary_role on primary_role.id = u.primary_role_id
		left join bp_auth_register_config cfg on cfg.id = 1
		left join bp_role default_role on default_role.id = cfg.default_role_id
		where u.id = ?
		limit 1
	`, userID).Scan(
		&item.UserID,
		&item.RoleID,
		&item.RoleName,
		&item.RoleKey,
		&item.QuotaEnabled,
		&item.MinuteLimit,
		&item.HourLimit,
		&item.DayLimit,
		&item.MonthLimit,
		&item.CountMode,
		&item.NewAccountDayLimit,
		&item.UserCreatedAt,
		&item.Points,
		&item.Balance,
	)
	return item, err
}

// PaidPackageUsages 列出仍生效的套餐权益（全站 + 接口两层）。
// 排序与网关扣减顺序一致：主套餐在前，其次快到期的、额度大的，便于用户理解
// 「下一笔会扣哪一个」。
func (r *Repository) PaidPackageUsages(ctx context.Context, userID int64) ([]QuotaPackageUsage, error) {
	rows, err := r.db.QueryContext(ctx, `
		select id, name, scope, is_primary, daily_limit, used_today, total_limit, total_used, count_mode, end_at, package_id, spec_id
		from (
			select concat('GLOBAL:', id) id, package_name name, 'GLOBAL' scope,
			       is_primary, daily_limit,
			       if(day_key = curdate(), day_used, 0) used_today,
			       total_limit, total_used, count_mode,
			       date_format(end_at, '%Y-%m-%d %H:%i:%s') end_at,
			       package_id, 0 spec_id
			from bp_user_package_global
			where user_id = ? and status = 1 and start_at <= now() and end_at > now()
			union all
			select concat('INTERFACE:', id) id, concat(package_name, ' / ', spec_name) name,
			       'INTERFACE' scope, is_primary, daily_limit,
			       if(day_key = curdate(), day_used, 0) used_today,
			       total_limit, total_used, count_mode,
			       date_format(end_at, '%Y-%m-%d %H:%i:%s') end_at,
			       package_id, spec_id
			from bp_user_package_interface
			where user_id = ? and status = 1 and start_at <= now() and end_at > now()
		) t
		order by is_primary desc, end_at asc, daily_limit desc, id asc
	`, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]QuotaPackageUsage, 0, 4)
	for rows.Next() {
		var item QuotaPackageUsage
		var primary int
		if err := rows.Scan(&item.ID, &item.Name, &item.Scope, &primary, &item.DailyLimit, &item.UsedToday,
			&item.TotalLimit, &item.TotalUsed, &item.CountMode, &item.EndAt, &item.packageID, &item.specID); err != nil {
			return nil, err
		}
		item.IsPrimary = primary == 1
		if item.CountMode == "" {
			item.CountMode = "success"
		}
		// 日剩余：不限为 -1；总剩余同理。取两者中更紧的作为展示剩余。
		dailyRemaining := int64(-1)
		if item.DailyLimit > 0 {
			dailyRemaining = item.DailyLimit - item.UsedToday
			if dailyRemaining < 0 {
				dailyRemaining = 0
			}
		}
		totalRemaining := int64(-1)
		if item.TotalLimit > 0 {
			totalRemaining = item.TotalLimit - item.TotalUsed
			if totalRemaining < 0 {
				totalRemaining = 0
			}
		}
		switch {
		case dailyRemaining < 0 && totalRemaining < 0:
			item.Remaining = -1
		case dailyRemaining < 0:
			item.Remaining = totalRemaining
		case totalRemaining < 0:
			item.Remaining = dailyRemaining
		case dailyRemaining < totalRemaining:
			item.Remaining = dailyRemaining
		default:
			item.Remaining = totalRemaining
		}
		item.Exhausted = item.Remaining == 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 用尽当日额度的套餐仍会出现在列表里（用户要看到「这个包今天用完了」），
	// 但不再参与 Paid.Available 判定——是否还有可用付费途径由 HasPaidEntitlement
	// 在请求路径上决定，这里只影响概览文案。
	return items, nil
}
