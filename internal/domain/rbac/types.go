package rbac

type Principal struct {
	BanPermanent int
	BanReason    string
	BanUntil     string
	ID           int64
	Username     string
	Roles        []string
	Status       int
	TokenVersion int
}

type Menu struct {
	ID        int64          `json:"-"`
	PID       int64          `json:"-"`
	IDText    string         `json:"id"`
	AuthCode  string         `json:"authCode"`
	Children  []Menu         `json:"children,omitempty"`
	Component string         `json:"component,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
	Name      string         `json:"name"`
	Path      string         `json:"path"`
	PIDText   string         `json:"pid"`
	Redirect  string         `json:"redirect,omitempty"`
	RoleIDs   []int64        `json:"roleIds,omitempty"`
	RoleNames []string       `json:"roleNames,omitempty"`
	Roles     []string       `json:"roles,omitempty"`
	Status    int            `json:"status"`
	Type      string         `json:"type"`
}

type SaveMenuRequest struct {
	AuthCode  string         `json:"authCode"`
	Component string         `json:"component"`
	LinkSrc   string         `json:"linkSrc"`
	Meta      map[string]any `json:"meta"`
	Name      string         `json:"name"`
	Path      string         `json:"path"`
	PID       any            `json:"pid"`
	Redirect  string         `json:"redirect"`
	RoleIDs   []int64        `json:"roleIds"`
	Status    *int           `json:"status"`
	Type      string         `json:"type"`
}

type Role struct {
	Builtin              bool    `json:"builtin"`
	CreateTime           string  `json:"createTime,omitempty"`
	DefaultRegisterRole  bool    `json:"defaultRegisterRole"`
	EffectivePermissions []int64 `json:"effectivePermissions"`
	ID                   string  `json:"id"`
	Name                 string  `json:"name"`
	PermissionMode       string  `json:"permissionMode"`
	Permissions          []int64 `json:"permissions"`
	QPSLimit             int     `json:"qpsLimit"`
	// QuotaMinuteLimit / QuotaHourLimit / QuotaDayLimit / QuotaMonthLimit form
	// the role's tiered request quota. 0 means unlimited for that window.
	QuotaMinuteLimit int64 `json:"quotaMinuteLimit"`
	QuotaHourLimit   int64 `json:"quotaHourLimit"`
	QuotaDayLimit    int64 `json:"quotaDayLimit"`
	QuotaMonthLimit  int64 `json:"quotaMonthLimit"`
	// QuotaCountMode selects the metering rule:
	//   attempt - count every admitted request, failures included;
	//   success - count only calls that returned success.
	QuotaCountMode      string `json:"quotaCountMode"`
	QuotaEnabled        int    `json:"quotaEnabled"`
	Remark              string `json:"remark,omitempty"`
	RoleKey             string `json:"roleKey"`
	SelfRegisterEnabled int    `json:"selfRegisterEnabled"`
	Status              int    `json:"status"`
	UserCount           int64  `json:"userCount"`
}

// RoleQuota is the current tiered quota of one role. UpdateRole merges omitted
// request fields with it so an older admin client cannot silently reset limits.
type RoleQuota struct {
	CountMode   string
	DayLimit    int64
	Enabled     int
	HourLimit   int64
	MinuteLimit int64
	MonthLimit  int64
}

type SaveRoleRequest struct {
	Name                string  `json:"name"`
	Permissions         []int64 `json:"permissions"`
	QPSLimit            *int    `json:"qpsLimit"`
	QuotaCountMode      *string `json:"quotaCountMode"`
	QuotaDayLimit       *int64  `json:"quotaDayLimit"`
	QuotaEnabled        *int    `json:"quotaEnabled"`
	QuotaHourLimit      *int64  `json:"quotaHourLimit"`
	QuotaMinuteLimit    *int64  `json:"quotaMinuteLimit"`
	QuotaMonthLimit     *int64  `json:"quotaMonthLimit"`
	Remark              string  `json:"remark"`
	RoleKey             string  `json:"roleKey"`
	SelfRegisterEnabled *int    `json:"selfRegisterEnabled"`
	Status              *int    `json:"status"`
}

type RoleListParams struct {
	ID                  int64
	Name                string
	Page                int
	PageSize            int
	RoleKey             string
	SelfRegisterEnabled *int
	Status              *int
}

type RoleListResult struct {
	Items []Role `json:"items"`
	Total int64  `json:"total"`
}

type RoleOption struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	QPSLimit            int    `json:"qpsLimit"`
	RoleKey             string `json:"roleKey"`
	SelfRegisterEnabled int    `json:"selfRegisterEnabled"`
	Status              int    `json:"status"`
}

type UpdateRoleStatusRequest struct {
	Status int `json:"status"`
}
