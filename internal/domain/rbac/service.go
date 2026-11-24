package rbac

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/pkg/accountstatus"
	"bugpk-api/internal/pkg/auditprincipal"
	apperrors "bugpk-api/internal/pkg/errors"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

type Service struct {
	repo *Repository
	jwt  *auth.JWTService
}

var roleKeyPattern = regexp.MustCompile(`^[A-Za-z0-9:_\-]{2,64}$`)
var authCodePattern = regexp.MustCompile(`^[A-Za-z0-9:_\-]{2,128}$`)

const (
	maxMenuMetaBytes       = 16 * 1024
	maxMenuRoleIDs         = 100
	maxRoleMenuPermissions = 500
	maxRoleQPSLimit        = 1_000_000
	lastNavigationOrder    = 2_147_483_647

	// These legacy entries point to the same component as Workspace. Keep the
	// API filter until every existing installation has applied the migrations.
	duplicateWorkspaceMenuName = "UserDashboardStats"
	duplicateWorkspaceMenuPath = "/dashboard-stats/user"
	duplicateHomeMenuName      = "HomeOverview"
	duplicateHomeMenuPath      = "/dashboard/home-overview"
	legacyConsoleAPIsPath      = "/console/apis"
)

func NewService(repo *Repository, jwt *auth.JWTService) *Service {
	return &Service{repo: repo, jwt: jwt}
}

func (s *Service) Authenticate(ctx context.Context, accessToken string) (Principal, error) {
	token := strings.TrimSpace(accessToken)
	// Verify signature, issuer, token type and expiry on EVERY invocation,
	// including request-local hits. Context attribution alone is never trusted.
	info, err := s.jwt.ParseAccessToken(token)
	if err != nil {
		return Principal{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
	}
	if err := ctx.Err(); err != nil {
		return Principal{}, authenticationUnavailable(err)
	}
	principal, err := s.authenticateInRequest(ctx, token, func() (Principal, error) {
		principal, err := s.repo.FindPrincipal(ctx, info.UserID)
		if err != nil {
			if errors.Is(err, ErrUserDisabled) {
				return Principal{}, accountstatus.Forbidden(principal.BanPermanent, principal.BanUntil, principal.BanReason)
			}
			if errors.Is(err, sql.ErrNoRows) {
				return Principal{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
			}
			return Principal{}, authenticationUnavailable(err)
		}
		if principal.TokenVersion != info.TokenVersion {
			return Principal{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
		}
		return principal, nil
	})
	if err == nil {
		auditprincipal.Record(ctx, token, auditprincipal.Identity{UserID: principal.ID, Username: principal.Username, Roles: principal.Roles})
	}
	return principal, err
}

func authenticationUnavailable(err error) error {
	return apperrors.Wrap(consts.StatusServiceUnavailable, "认证服务暂时不可用", err)
}

func (s *Service) PermissionCodes(ctx context.Context, userID int64, roles []string) ([]string, error) {
	codes, err := s.repo.PermissionCodesByUser(ctx, userID, hasRole(roles, "admin"))
	if err != nil {
		return nil, err
	}
	return normalizePermissionCodes(codes), nil
}

// normalizePermissionCodes keeps upgraded installations compatible with the
// flattened key menu. Migration 000074 promoted the former key catalog to the
// list page; databases that ran the older form of that migration can still
// carry key:root on the promoted row. Treat that direct page permission as the
// list permission until the corrective migration rewrites the stored code.
func normalizePermissionCodes(codes []string) []string {
	hasKeyRoot := false
	hasKeyList := false
	for _, code := range codes {
		switch code {
		case "key:list":
			hasKeyList = true
		case "key:root":
			hasKeyRoot = true
		}
	}
	if hasKeyRoot && !hasKeyList {
		codes = append(codes, "key:list")
	}
	return codes
}

func (s *Service) HasPermission(ctx context.Context, userID int64, roles []string, code string) (bool, error) {
	if hasRole(roles, "admin") {
		return true, nil
	}
	codes, err := s.PermissionCodes(ctx, userID, roles)
	if err != nil {
		return false, err
	}
	for _, item := range codes {
		if item == code {
			return true, nil
		}
	}
	return false, nil
}

func (s *Service) Require(ctx context.Context, principal Principal, code string) error {
	ok, err := s.HasPermission(ctx, principal.ID, principal.Roles, code)
	if err != nil {
		return err
	}
	if !ok {
		return apperrors.New(consts.StatusForbidden, "Forbidden Exception")
	}
	return nil
}

func (s *Service) UserMenus(ctx context.Context, principal Principal) ([]Menu, error) {
	menus, err := s.repo.EnabledMenus(ctx)
	if err != nil {
		return nil, err
	}
	// Older databases may still have the duplicate menu enabled until the
	// corresponding migration has been applied. Filter it at the API boundary
	// so a frontend refresh cannot recreate the duplicate route.
	menus = filterDuplicateWorkspaceMenus(menus)
	admin := hasRole(principal.Roles, "admin")
	menus = filterMenusForAudience(menus, admin)
	if admin {
		// Collapse legacy catalog/list pairs at the API boundary so older
		// databases still render one direct key-management destination.
		menus = collapseKeyMenu(menus, false)
		menus = normalizeConsoleFeatureMenus(menus)
		menus = normalizeAdminNavigationMenus(menus)
		return BuildTree(menus, false), nil
	}
	allowed, err := s.repo.MenuIDsByUser(ctx, principal.ID)
	if err != nil {
		return nil, err
	}
	menuByID := map[int64]Menu{}
	for _, menu := range menus {
		menuByID[menu.ID] = menu
	}
	for id := range allowed {
		for current := menuByID[id]; current.PID != 0; current = menuByID[current.PID] {
			if _, ok := menuByID[current.PID]; !ok {
				break
			}
			allowed[current.PID] = true
		}
	}
	filtered := make([]Menu, 0, len(menus))
	for _, menu := range menus {
		if allowed[menu.ID] {
			filtered = append(filtered, menu)
		}
	}
	filtered = collapseKeyMenu(filtered, true)
	filtered = normalizeConsoleFeatureMenus(filtered)
	// These self-service pages deliberately do not require management menu
	// permissions. Their handlers enforce current-user scope.
	filtered = injectUserFeatureMenus(filtered)
	filtered = normalizeUserNavigationMenus(filtered)
	return BuildTree(filtered, false), nil
}

func filterDuplicateWorkspaceMenus(menus []Menu) []Menu {
	filtered := make([]Menu, 0, len(menus))
	for _, menu := range menus {
		path := strings.TrimSpace(menu.Path)
		if menu.Name == duplicateWorkspaceMenuName || menu.Name == duplicateHomeMenuName || path == duplicateWorkspaceMenuPath || path == duplicateHomeMenuPath {
			continue
		}
		filtered = append(filtered, menu)
	}
	return filtered
}

// collapseKeyMenu turns both the current menu definition and the legacy
// catalog -> list definition into one top-level route backed by /key/list.
func collapseKeyMenu(menus []Menu, userView bool) []Menu {
	var rootID int64
	var legacyListID int64
	component := "/key/list"
	for _, menu := range menus {
		path := strings.TrimSpace(menu.Path)
		if path == "/key" || menu.Name == "ApiKeyManagement" || menu.Name == "KeyManagement" {
			rootID = menu.ID
		}
		if path == "/key/list" || menu.Name == "ApiKeyList" {
			legacyListID = menu.ID
			if strings.TrimSpace(menu.Component) != "" {
				component = menu.Component
			}
		}
	}
	if rootID == 0 && legacyListID != 0 {
		rootID = legacyListID
	}
	if rootID == 0 {
		return menus
	}

	result := make([]Menu, 0, len(menus))
	for _, menu := range menus {
		path := strings.TrimSpace(menu.Path)
		if menu.ID != rootID && (path == "/key/list" || menu.Name == "ApiKeyList") {
			continue
		}
		if menu.ID == rootID {
			menu.PID = 0
			menu.PIDText = "0"
			menu.Path = "/key"
			menu.Component = component
			menu.Type = "menu"
			menu.AuthCode = "key:list"
			if menu.Meta == nil {
				menu.Meta = map[string]any{}
			}
			if userView {
				menu.Meta["title"] = "system.key.myKeys"
			} else {
				menu.Meta["title"] = "system.key.title"
			}
		}
		result = append(result, menu)
	}
	return result
}

func normalizeConsoleFeatureMenus(menus []Menu) []Menu {
	result := make([]Menu, 0, len(menus))
	seenFeaturePath := map[string]bool{}
	for i := range menus {
		if strings.EqualFold(strings.TrimSpace(menus[i].Path), legacyConsoleAPIsPath) {
			continue
		}
		switch menus[i].Name {
		case "InterfaceCallLog":
			menus[i].Path = "/console/call-logs"
			menus[i].Component = "/interface/log"
		case "InterfaceList":
			menus[i].Path = "/console/interfaces"
			menus[i].Component = "/interface/list"
		}
		path := strings.TrimSpace(menus[i].Path)
		if path == "/console/interfaces" || path == "/console/call-logs" {
			if seenFeaturePath[path] {
				continue
			}
			seenFeaturePath[path] = true
		}
		result = append(result, menus[i])
	}
	return result
}

func normalizeAdminNavigationMenus(menus []Menu) []Menu {
	for i := range menus {
		if menus[i].Name != "SystemAbout" && strings.TrimSpace(menus[i].Path) != "/system/about" {
			continue
		}
		menus[i].PID = 0
		menus[i].PIDText = "0"
		if menus[i].Meta == nil {
			menus[i].Meta = map[string]any{}
		}
		menus[i].Meta["order"] = lastNavigationOrder
	}
	return menus
}

// User self-service navigation intentionally exposes the API catalogue and
// call log as direct destinations. Removing the management catalogue prevents
// a second, visually flattened copy from appearing beside the injected routes.
func normalizeUserNavigationMenus(menus []Menu) []Menu {
	result := make([]Menu, 0, len(menus))
	for _, menu := range menus {
		if menu.Name == "InterfaceManagement" || strings.TrimSpace(menu.Path) == "/interface" {
			continue
		}
		switch strings.TrimSpace(menu.Path) {
		case "/console/interfaces", "/console/call-logs":
			menu.PID = 0
			menu.PIDText = "0"
		case "/system/about":
			menu.PID = 0
			menu.PIDText = "0"
			if menu.Meta == nil {
				menu.Meta = map[string]any{}
			}
			menu.Meta["order"] = lastNavigationOrder
		}
		result = append(result, menu)
	}
	return result
}

// injectUserFeatureMenus 为普通用户注入接口服务菜单条目。
// 调用日志由后端 UserCallLogs 强制 UserVisibleOnly + 脱敏，仅返回当前用户最基础日志
// 使用负数 ID 避免与数据库正数 ID 冲突，BuildTree 以 PID=0 作为顶级容器
func injectUserFeatureMenus(menus []Menu) []Menu {
	hasInterfaces := false
	hasCallLogs := false
	hasKeys := false
	hasFriendLinks := false
	for _, menu := range menus {
		switch strings.TrimSpace(menu.Path) {
		case "/key":
			hasKeys = true
		case "/console/interfaces":
			hasInterfaces = true
		case "/console/call-logs":
			hasCallLogs = true
		case "/friend-link/mine":
			hasFriendLinks = true
		}
	}
	if !hasKeys {
		menus = append(menus, Menu{
			ID: -10030, PID: 0, Type: "menu", Name: "KeyManagement",
			Path: "/key", Component: "/key/list",
			IDText: "-10030", PIDText: "0", Status: 1,
			Meta: map[string]any{
				"hideInMenu": false, "icon": "mdi:key-chain", "order": 3,
				"title": "system.key.myKeys",
			},
		})
	}

	if !hasInterfaces {
		menus = append(menus, Menu{
			ID: -10041, PID: 0, Type: "menu", Name: "InterfaceList",
			Path: "/console/interfaces", Component: "/interface/list",
			IDText: "-10041", PIDText: "0", Status: 1,
			Meta: map[string]any{
				"hideInMenu": false, "icon": "mdi:api", "order": 2,
				"title": "system.interface.listTitle",
			},
		})
	}
	if !hasCallLogs {
		menus = append(menus, Menu{
			ID: -10050, PID: 0, Type: "menu", Name: "InterfaceCallLog",
			Path: "/console/call-logs", Component: "/interface/log",
			IDText: "-10050", PIDText: "0", Status: 1,
			Meta: map[string]any{
				"hideInMenu": false, "icon": "mdi:clipboard-text-clock", "order": 2.1,
				"title": "system.interface.callLogTitle",
			},
		})
	}

	if !hasFriendLinks {
		// 友链自助页同样不依赖 bp_menu 授权：后端只按登录账号收窄数据范围。
		menus = append(menus, Menu{
			ID: -10060, PID: 0, Type: "menu", Name: "FriendLinkMine",
			Path: "/friend-link/mine", Component: "/friend-link/mine",
			IDText: "-10060", PIDText: "0", Status: 1,
			Meta: map[string]any{
				"hideInMenu": false, "icon": "mdi:link-box-variant-outline", "order": 4,
				"title": "system.friendLink.mineTitle",
			},
		})
	}

	return menus
}

func filterMenusForAudience(menus []Menu, admin bool) []Menu {
	audience := "user"
	variantPrefix := "user"
	if admin {
		audience = "admin"
		variantPrefix = "admin"
	}

	filtered := make([]Menu, 0, len(menus))
	for _, menu := range menus {
		menuAudience := ""
		if menu.Meta != nil {
			if rawAudience, ok := menu.Meta["audience"]; ok && rawAudience != nil {
				menuAudience = strings.ToLower(strings.TrimSpace(fmt.Sprint(rawAudience)))
			}
		}
		if menuAudience != "" && menuAudience != "all" && menuAudience != audience {
			continue
		}

		if menu.Meta != nil {
			meta := make(map[string]any, len(menu.Meta))
			for key, value := range menu.Meta {
				meta[key] = value
			}
			if title, ok := meta[variantPrefix+"Title"]; ok && strings.TrimSpace(fmt.Sprint(title)) != "" {
				meta["title"] = title
			}
			if order, ok := meta[variantPrefix+"Order"]; ok {
				meta["order"] = order
			}
			delete(meta, "adminTitle")
			delete(meta, "adminOrder")
			delete(meta, "userTitle")
			delete(meta, "userOrder")
			delete(meta, "audience")
			menu.Meta = meta
		}
		filtered = append(filtered, menu)
	}
	return filtered
}

func (s *Service) SystemMenuList(ctx context.Context, principal Principal) ([]Menu, error) {
	if err := s.Require(ctx, principal, "system:menu:list"); err != nil {
		return nil, err
	}
	menus, err := s.repo.AllMenus(ctx)
	if err != nil {
		return nil, err
	}
	for i := range menus {
		roleIDs, roles, roleNames, err := s.repo.MenuRoleIDs(ctx, menus[i].ID)
		if err != nil {
			return nil, err
		}
		menus[i].RoleIDs = roleIDs
		menus[i].Roles = roles
		menus[i].RoleNames = roleNames
	}
	return BuildTree(menus, true), nil
}

func (s *Service) MenuNameExists(ctx context.Context, principal Principal, name string, id int64) (bool, error) {
	if err := s.Require(ctx, principal, "system:menu:list"); err != nil {
		return false, err
	}
	return s.repo.NameExists(ctx, strings.TrimSpace(name), id)
}

func (s *Service) MenuPathExists(ctx context.Context, principal Principal, path string, id int64) (bool, error) {
	if err := s.Require(ctx, principal, "system:menu:list"); err != nil {
		return false, err
	}
	return s.repo.PathExists(ctx, strings.TrimSpace(path), id)
}

func (s *Service) CreateMenu(ctx context.Context, principal Principal, req SaveMenuRequest) error {
	if err := s.Require(ctx, principal, "system:menu:create"); err != nil {
		return err
	}
	menu, err := normalizeMenu(req, 0)
	if err != nil {
		return err
	}
	menus, err := s.repo.AllMenus(ctx)
	if err != nil {
		return err
	}
	if err := validateMenuParent(0, menu.PID, menus); err != nil {
		return err
	}
	exists, err := s.repo.NameExists(ctx, menu.Name, 0)
	if err != nil {
		return err
	}
	if exists {
		return apperrors.New(consts.StatusBadRequest, "菜单名称已存在")
	}
	pathExists, err := s.repo.PathExists(ctx, menu.Path, 0)
	if err != nil {
		return err
	}
	if pathExists {
		return apperrors.New(consts.StatusBadRequest, "菜单路径已存在")
	}
	return mapMenuError(s.repo.CreateMenu(ctx, menu, menu.RoleIDs))
}

func (s *Service) UpdateMenu(ctx context.Context, principal Principal, menuID int64, req SaveMenuRequest) error {
	if err := s.Require(ctx, principal, "system:menu:update"); err != nil {
		return err
	}
	menu, err := normalizeMenu(req, menuID)
	if err != nil {
		return err
	}
	if menu.PID == menuID {
		return apperrors.New(consts.StatusBadRequest, "父级菜单不能是自身")
	}
	menus, err := s.repo.AllMenus(ctx)
	if err != nil {
		return err
	}
	if err := validateMenuParent(menuID, menu.PID, menus); err != nil {
		return err
	}
	exists, err := s.repo.NameExists(ctx, menu.Name, menuID)
	if err != nil {
		return err
	}
	if exists {
		return apperrors.New(consts.StatusBadRequest, "菜单名称已存在")
	}
	pathExists, err := s.repo.PathExists(ctx, menu.Path, menuID)
	if err != nil {
		return err
	}
	if pathExists {
		return apperrors.New(consts.StatusBadRequest, "菜单路径已存在")
	}
	return mapMenuError(s.repo.UpdateMenu(ctx, menuID, menu, menu.RoleIDs))
}

func (s *Service) DeleteMenu(ctx context.Context, principal Principal, menuID int64) error {
	if err := s.Require(ctx, principal, "system:menu:delete"); err != nil {
		return err
	}
	return mapMenuError(s.repo.DeleteMenu(ctx, menuID))
}

func (s *Service) RoleList(ctx context.Context, principal Principal, params RoleListParams) (RoleListResult, error) {
	if err := s.Require(ctx, principal, "system:role:list"); err != nil {
		return RoleListResult{}, err
	}
	result, err := s.repo.ListRoles(ctx, params)
	if err != nil {
		return RoleListResult{}, err
	}
	needsAdminPermissions := false
	for _, role := range result.Items {
		if role.RoleKey == "admin" {
			needsAdminPermissions = true
			break
		}
	}
	var enabledMenus []Menu
	if needsAdminPermissions {
		enabledMenus, err = s.repo.EnabledMenus(ctx)
		if err != nil {
			return RoleListResult{}, err
		}
	}
	applyEffectiveRolePermissions(result.Items, enabledMenus)
	return result, nil
}

// applyEffectiveRolePermissions keeps the role editor aligned with runtime
// authorization. The built-in admin role is a super-admin role: it always
// receives every enabled menu and button even when legacy bp_role_menu rows
// are incomplete. Runtime authorization bypasses assigned menu relationships
// for administrators, so the editor must also include enabled user-audience
// nodes to avoid showing a false partial authorization state. Assigned
// permissions remain available for compatibility, while effectivePermissions
// is the authoritative UI view.
func applyEffectiveRolePermissions(roles []Role, enabledMenus []Menu) {
	adminMenuIDs := make([]int64, 0, len(enabledMenus))
	for _, menu := range enabledMenus {
		adminMenuIDs = append(adminMenuIDs, menu.ID)
	}
	sort.Slice(adminMenuIDs, func(i, j int) bool { return adminMenuIDs[i] < adminMenuIDs[j] })
	for index := range roles {
		roles[index].PermissionMode = "assigned"
		roles[index].EffectivePermissions = append([]int64(nil), roles[index].Permissions...)
		if roles[index].RoleKey == "admin" {
			roles[index].PermissionMode = "all"
			roles[index].EffectivePermissions = append([]int64(nil), adminMenuIDs...)
		}
	}
}

func (s *Service) RoleOptions(ctx context.Context, principal Principal) ([]RoleOption, error) {
	if err := s.Require(ctx, principal, "system:role:list"); err != nil {
		return nil, err
	}
	return s.repo.RoleOptions(ctx)
}

func (s *Service) CreateRole(ctx context.Context, principal Principal, req SaveRoleRequest) error {
	if err := s.Require(ctx, principal, "system:role:create"); err != nil {
		return err
	}
	role, err := normalizeRole(req)
	if err != nil {
		return err
	}
	_, err = s.repo.CreateRole(ctx, role)
	return mapRoleError(err)
}

func (s *Service) UpdateRole(ctx context.Context, principal Principal, roleID int64, req SaveRoleRequest) error {
	if err := s.Require(ctx, principal, "system:role:update"); err != nil {
		return err
	}
	defaults := defaultRoleQuota()
	current, err := s.repo.RoleQuotaByID(ctx, roleID)
	if err == nil {
		defaults = current
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	role, err := normalizeRoleWithDefaults(req, defaults)
	if err != nil {
		return err
	}
	return mapRoleError(s.repo.UpdateRole(ctx, roleID, role))
}

func (s *Service) DeleteRole(ctx context.Context, principal Principal, roleID int64) error {
	if err := s.Require(ctx, principal, "system:role:delete"); err != nil {
		return err
	}
	return mapRoleError(s.repo.DeleteRole(ctx, roleID))
}

func (s *Service) UpdateRoleStatus(ctx context.Context, principal Principal, roleID int64, status int) error {
	if err := s.Require(ctx, principal, "system:role:update"); err != nil {
		return err
	}
	if status != 0 && status != 1 {
		return apperrors.New(consts.StatusBadRequest, "角色状态不合法")
	}
	return mapRoleError(s.repo.UpdateRoleStatus(ctx, roleID, status))
}

func validateMenuParent(menuID, parentID int64, menus []Menu) error {
	if parentID == 0 {
		return nil
	}
	parents := make(map[int64]int64, len(menus))
	for _, menu := range menus {
		parents[menu.ID] = menu.PID
	}
	if _, ok := parents[parentID]; !ok {
		return apperrors.New(consts.StatusBadRequest, "parent menu does not exist")
	}
	seen := map[int64]bool{}
	for current := parentID; current != 0; current = parents[current] {
		if menuID > 0 && current == menuID {
			return apperrors.New(consts.StatusBadRequest, "parent menu cannot be a descendant")
		}
		if seen[current] {
			return apperrors.New(consts.StatusBadRequest, "menu parent chain contains a cycle")
		}
		seen[current] = true
		next, ok := parents[current]
		if !ok {
			break
		}
		if next == current {
			return apperrors.New(consts.StatusBadRequest, "menu parent chain contains a cycle")
		}
	}
	return nil
}

func normalizeMenu(req SaveMenuRequest, id int64) (Menu, error) {
	menuType := strings.TrimSpace(req.Type)
	switch menuType {
	case "catalog", "menu", "embedded", "link", "button":
	default:
		return Menu{}, apperrors.New(consts.StatusBadRequest, "菜单类型不合法")
	}
	name := strings.TrimSpace(req.Name)
	if utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > 128 {
		return Menu{}, apperrors.New(consts.StatusBadRequest, "菜单名称长度必须为2到128位")
	}
	path := strings.TrimSpace(req.Path)
	if menuType != "button" {
		if !validInternalRoute(path, 255) {
			return Menu{}, apperrors.New(consts.StatusBadRequest, "菜单路径必须以 / 开头")
		}
	} else {
		path = ""
	}
	status := 1
	if req.Status != nil {
		status = *req.Status
	}
	if status != 0 && status != 1 {
		return Menu{}, apperrors.New(consts.StatusBadRequest, "菜单状态不合法")
	}
	meta := req.Meta
	if meta == nil {
		meta = map[string]any{}
	} else {
		copied := make(map[string]any, len(meta)+1)
		for key, value := range meta {
			copied[key] = value
		}
		meta = copied
	}
	linkSrc := strings.TrimSpace(req.LinkSrc)
	if linkSrc != "" {
		if (menuType == "link" || menuType == "embedded") && !validHTTPURL(linkSrc) {
			return Menu{}, apperrors.New(consts.StatusBadRequest, "外链地址必须是 http/https URL")
		}
		if menuType == "link" {
			meta["link"] = linkSrc
		}
		if menuType == "embedded" {
			meta["iframeSrc"] = linkSrc
		}
	}
	component := strings.TrimSpace(req.Component)
	if utf8.RuneCountInString(component) > 255 || (component != "" && (!strings.HasPrefix(component, "/") || strings.Contains(component, "..") || strings.ContainsAny(component, "\r\n\\"))) {
		return Menu{}, apperrors.New(consts.StatusBadRequest, "菜单组件路径不合法")
	}
	redirect := strings.TrimSpace(req.Redirect)
	if redirect != "" && !validInternalRoute(redirect, 255) {
		return Menu{}, apperrors.New(consts.StatusBadRequest, "菜单重定向路径不合法")
	}
	authCode := strings.TrimSpace(req.AuthCode)
	if authCode != "" && !authCodePattern.MatchString(authCode) {
		return Menu{}, apperrors.New(consts.StatusBadRequest, "菜单权限码格式不合法")
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil || len(metaJSON) > maxMenuMetaBytes {
		return Menu{}, apperrors.New(consts.StatusBadRequest, "菜单扩展配置不合法或过大")
	}
	pid, err := parseAnyInt64(req.PID)
	if err != nil || pid < 0 {
		return Menu{}, apperrors.New(consts.StatusBadRequest, "父级菜单不合法")
	}
	roleIDs, ok := normalizePositiveIDs(req.RoleIDs, maxMenuRoleIDs)
	if !ok {
		return Menu{}, apperrors.New(consts.StatusBadRequest, "菜单角色绑定不合法")
	}
	return Menu{
		ID:        id,
		PID:       pid,
		Name:      name,
		Path:      path,
		Component: component,
		Redirect:  redirect,
		Type:      menuType,
		AuthCode:  authCode,
		Meta:      meta,
		RoleIDs:   roleIDs,
		Status:    status,
	}, nil
}

const (
	maxRoleQuotaShortWindow = 1_000_000_000
	maxRoleQuotaLongWindow  = 1_000_000_000_000
)

func defaultRoleQuota() RoleQuota {
	return RoleQuota{
		Enabled:     0,
		MinuteLimit: 60,
		HourLimit:   600,
		DayLimit:    1000,
		MonthLimit:  20000,
		CountMode:   "attempt",
	}
}

func normalizeRole(req SaveRoleRequest) (Role, error) {
	return normalizeRoleWithDefaults(req, defaultRoleQuota())
}

func normalizeRoleWithDefaults(req SaveRoleRequest, defaults RoleQuota) (Role, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return Role{}, apperrors.New(consts.StatusBadRequest, "角色名称不能为空且不能超过64个字符")
	}
	roleKey := strings.TrimSpace(req.RoleKey)
	if !roleKeyPattern.MatchString(roleKey) {
		return Role{}, apperrors.New(consts.StatusBadRequest, "角色标识只能包含字母、数字、冒号、下划线和短横线，长度为2到64位")
	}
	status := 1
	if req.Status != nil {
		status = *req.Status
	}
	if status != 0 && status != 1 {
		return Role{}, apperrors.New(consts.StatusBadRequest, "角色状态不合法")
	}
	qpsLimit := 1
	if req.QPSLimit != nil {
		qpsLimit = *req.QPSLimit
	}
	if qpsLimit < 1 || qpsLimit > maxRoleQPSLimit {
		return Role{}, apperrors.New(consts.StatusBadRequest, "角色 QPS 必须在 1 到 1000000 之间")
	}
	selfRegisterEnabled := 0
	if req.SelfRegisterEnabled != nil {
		selfRegisterEnabled = *req.SelfRegisterEnabled
	}
	if selfRegisterEnabled != 0 && selfRegisterEnabled != 1 {
		return Role{}, apperrors.New(consts.StatusBadRequest, "角色开放注册状态不合法")
	}
	if roleKey == "admin" && selfRegisterEnabled != 0 {
		return Role{}, apperrors.New(consts.StatusBadRequest, "管理员角色不能开放自助注册")
	}
	quota, err := normalizeRoleQuota(req, defaults)
	if err != nil {
		return Role{}, err
	}
	permissions, ok := normalizePositiveIDs(req.Permissions, maxRoleMenuPermissions)
	if !ok {
		return Role{}, apperrors.New(consts.StatusBadRequest, "角色菜单权限不合法")
	}
	remark := strings.TrimSpace(req.Remark)
	if utf8.RuneCountInString(remark) > 255 {
		return Role{}, apperrors.New(consts.StatusBadRequest, "角色备注不能超过255个字符")
	}
	return Role{
		Name:                name,
		Permissions:         permissions,
		QPSLimit:            qpsLimit,
		QuotaCountMode:      quota.CountMode,
		QuotaDayLimit:       quota.DayLimit,
		QuotaEnabled:        quota.Enabled,
		QuotaHourLimit:      quota.HourLimit,
		QuotaMinuteLimit:    quota.MinuteLimit,
		QuotaMonthLimit:     quota.MonthLimit,
		Remark:              remark,
		RoleKey:             roleKey,
		SelfRegisterEnabled: selfRegisterEnabled,
		Status:              status,
	}, nil
}

// normalizeRoleQuota merges a partial save request into the current/default
// tiered quota. Windows are validated as a ladder so "per minute <= per hour <=
// per day <= per month" holds whenever both sides are enabled.
func normalizeRoleQuota(req SaveRoleRequest, defaults RoleQuota) (RoleQuota, error) {
	quota := defaults
	if quota.CountMode != "attempt" && quota.CountMode != "success" {
		quota.CountMode = "attempt"
	}
	if req.QuotaEnabled != nil {
		quota.Enabled = *req.QuotaEnabled
	}
	if quota.Enabled != 0 && quota.Enabled != 1 {
		return RoleQuota{}, apperrors.New(consts.StatusBadRequest, "请求配额开关不合法")
	}
	if req.QuotaMinuteLimit != nil {
		quota.MinuteLimit = *req.QuotaMinuteLimit
	}
	if req.QuotaHourLimit != nil {
		quota.HourLimit = *req.QuotaHourLimit
	}
	if req.QuotaDayLimit != nil {
		quota.DayLimit = *req.QuotaDayLimit
	}
	if req.QuotaMonthLimit != nil {
		quota.MonthLimit = *req.QuotaMonthLimit
	}
	if req.QuotaCountMode != nil {
		quota.CountMode = strings.ToLower(strings.TrimSpace(*req.QuotaCountMode))
	}
	if quota.CountMode != "attempt" && quota.CountMode != "success" {
		return RoleQuota{}, apperrors.New(consts.StatusBadRequest, "请求计量方式只能是 attempt（失败也计）或 success（仅成功计量）")
	}
	if quota.MinuteLimit < 0 || quota.MinuteLimit > maxRoleQuotaShortWindow ||
		quota.HourLimit < 0 || quota.HourLimit > maxRoleQuotaShortWindow {
		return RoleQuota{}, apperrors.New(consts.StatusBadRequest, "每分钟/每小时请求配额必须在 0 到 1000000000 之间")
	}
	if quota.DayLimit < 0 || quota.DayLimit > maxRoleQuotaLongWindow ||
		quota.MonthLimit < 0 || quota.MonthLimit > maxRoleQuotaLongWindow {
		return RoleQuota{}, apperrors.New(consts.StatusBadRequest, "每日/每月请求配额必须在 0 到 1000000000000 之间")
	}
	if quota.MinuteLimit > 0 && quota.HourLimit > 0 && quota.MinuteLimit > quota.HourLimit {
		return RoleQuota{}, apperrors.New(consts.StatusBadRequest, "阶梯配额不合法：每分钟上限不能大于每小时上限")
	}
	if quota.HourLimit > 0 && quota.DayLimit > 0 && quota.HourLimit > quota.DayLimit {
		return RoleQuota{}, apperrors.New(consts.StatusBadRequest, "阶梯配额不合法：每小时上限不能大于每日上限")
	}
	if quota.DayLimit > 0 && quota.MonthLimit > 0 && quota.DayLimit > quota.MonthLimit {
		return RoleQuota{}, apperrors.New(consts.StatusBadRequest, "阶梯配额不合法：每日上限不能大于每月上限")
	}
	return quota, nil
}

func parseAnyInt64(value any) (int64, error) {
	if value == nil {
		return 0, nil
	}
	switch v := value.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || math.Trunc(v) != v || v > math.MaxInt64 || v < 0 {
			return 0, fmt.Errorf("invalid int64")
		}
		return int64(v), nil
	case int64:
		if v < 0 {
			return 0, fmt.Errorf("invalid int64")
		}
		return v, nil
	case int:
		if v < 0 {
			return 0, fmt.Errorf("invalid int64")
		}
		return int64(v), nil
	case string:
		if strings.TrimSpace(v) == "" {
			return 0, nil
		}
		parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil || parsed < 0 {
			return 0, fmt.Errorf("invalid int64")
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("invalid int64")
	}
}

func mapMenuError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrMenuHasChildren):
		return apperrors.New(consts.StatusBadRequest, "存在子菜单，不能删除")
	case errors.Is(err, ErrRoleNotFound):
		return apperrors.New(consts.StatusBadRequest, "角色不存在或已禁用")
	case errors.Is(err, ErrMenuNameExists):
		return apperrors.New(consts.StatusBadRequest, "菜单名称已存在")
	case errors.Is(err, ErrMenuPathExists):
		return apperrors.New(consts.StatusBadRequest, "菜单路径已存在")
	case errors.Is(err, ErrMenuNotFound), errors.Is(err, sql.ErrNoRows):
		return apperrors.New(consts.StatusNotFound, "菜单不存在")
	default:
		return err
	}
}

func mapRoleError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrRoleExists):
		return apperrors.New(consts.StatusConflict, "角色标识已存在")
	case errors.Is(err, ErrMenuNotFound):
		return apperrors.New(consts.StatusBadRequest, "菜单权限不存在")
	case errors.Is(err, ErrProtectedAdminRole):
		return apperrors.New(consts.StatusBadRequest, "不能删除、禁用或改名管理员角色")
	case errors.Is(err, ErrProtectedBuiltinRole):
		return apperrors.New(consts.StatusBadRequest, "系统内置角色不能删除或修改角色标识")
	case errors.Is(err, ErrProtectedDefaultRole):
		return apperrors.New(consts.StatusBadRequest, "默认注册角色不能禁用、删除或关闭自助注册，请先切换默认角色")
	case errors.Is(err, ErrRoleInUse):
		return apperrors.New(consts.StatusConflict, "该角色仍分配给用户，请先调整用户角色")
	case errors.Is(err, sql.ErrNoRows):
		return apperrors.New(consts.StatusNotFound, "角色不存在")
	default:
		return err
	}
}

func validInternalRoute(value string, maxRunes int) bool {
	value = strings.TrimSpace(value)
	if value == "" || utf8.RuneCountInString(value) > maxRunes || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.ContainsAny(value, "\r\n\\") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && !parsed.IsAbs() && parsed.Host == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func validHTTPURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
}

func normalizePositiveIDs(values []int64, limit int) ([]int64, bool) {
	if len(values) > limit {
		return nil, false
	}
	result := make([]int64, 0, len(values))
	seen := make(map[int64]struct{}, len(values))
	for _, value := range values {
		if value <= 0 {
			return nil, false
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result, true
}
