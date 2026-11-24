package packagepkg

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"bugpk-api/internal/domain/rbac"
	"bugpk-api/internal/pkg/businessclock"
	"bugpk-api/internal/pkg/decimalvalue"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/i18n"
	"bugpk-api/internal/pkg/listquery"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

var ErrInsufficientBalance = errors.New("insufficient balance")
var ErrFreePackageAlreadyClaimed = errors.New("free package already claimed")
var ErrPackageHasBusinessHistory = errors.New("package has purchase, entitlement, or payment history")
var ErrPackageSpecHasBusinessHistory = errors.New("package spec has purchase history")
var ErrPackageSpecDuplicate = errors.New("duplicate package spec ID")
var ErrPackageSoldOut = errors.New("package stock is exhausted")
var ErrPackageOffSale = errors.New("package is not on sale")
var ErrPackageUserLimit = errors.New("package per-user purchase limit reached")
var ErrPackageStockBelowUsed = errors.New("package stock limit is below reserved count")
var ErrPackageRoleRestricted = errors.New("package is restricted to other user groups")
var ErrPointBalanceLimit = errors.New("point balance limit exceeded")
var ErrPurchaseIdempotencyConflict = errors.New("purchase idempotency key conflicts with another purchase")
var ErrPurchaseInProgress = errors.New("purchase request is still processing")

var (
	groupedPointCountPattern      = regexp.MustCompile(`^[0-9]{1,3}(?:,[0-9]{3})+$`)
	pointCountPattern             = regexp.MustCompile(`^[0-9]+$`)
	purchaseIdempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
)

const (
	maxPackageDailyLimit = int64(1_000_000_000_000)
	maxPackagePoints     = int64(1_000_000_000_000_000)
	maxUserPointBalance  = maxPackagePoints
	maxPackageQPS        = 1_000_000
	maxPackageSortNo     = 1_000_000
	maxPackageSpecs      = 100
	maxPackageValidDays  = 36_500
	maxPackageStock      = 1_000_000_000
	maxPackageRoles      = 200
)

type Service struct {
	repo      *Repository
	authz     *rbac.Service
	localizer i18n.Localizer
}

// SetLocalizer 为商城公开接口接入后端数据翻译。
func (s *Service) SetLocalizer(localizer i18n.Localizer) {
	s.localizer = localizer
}

func NewService(repo *Repository, authz *rbac.Service) *Service {
	return &Service{repo: repo, authz: authz}
}

func (s *Service) Authenticate(ctx context.Context, token string) (Principal, error) {
	principal, err := s.authz.Authenticate(ctx, token)
	if err != nil {
		return Principal{}, err
	}
	return Principal{ID: principal.ID, Username: principal.Username, Roles: principal.Roles, TokenVersion: principal.TokenVersion}, nil
}

func (s *Service) ListGlobal(ctx context.Context, principal Principal, params ListParams) (PageResult[GlobalPackage], error) {
	if err := s.require(ctx, principal, "package:global:list"); err != nil {
		return PageResult[GlobalPackage]{}, err
	}
	if err := validateListParams(&params); err != nil {
		return PageResult[GlobalPackage]{}, err
	}
	return s.repo.ListGlobal(ctx, params, false, nil)
}

func (s *Service) SaveGlobal(ctx context.Context, principal Principal, id int64, req SaveGlobalPackageRequest) error {
	code := "package:global:create"
	if id > 0 {
		code = "package:global:update"
	}
	if err := s.require(ctx, principal, code); err != nil {
		return err
	}
	item, err := normalizeGlobal(req)
	if err != nil {
		return err
	}
	if err := s.ensureRolesExist(ctx, item.RoleIDs); err != nil {
		return err
	}
	return mapErr(s.repo.SaveGlobal(ctx, id, item), "全站套餐不存在")
}

func (s *Service) DeleteGlobal(ctx context.Context, principal Principal, id int64, force bool) error {
	if err := s.require(ctx, principal, "package:global:delete"); err != nil {
		return err
	}
	return mapDeleteErr(s.repo.DeleteGlobal(ctx, id, force), "全站套餐不存在")
}

func (s *Service) ListInterface(ctx context.Context, principal Principal, params ListParams) (PageResult[InterfacePackage], error) {
	if err := s.require(ctx, principal, "package:interface:list"); err != nil {
		return PageResult[InterfacePackage]{}, err
	}
	if err := validateListParams(&params); err != nil {
		return PageResult[InterfacePackage]{}, err
	}
	return s.repo.ListInterface(ctx, params, false, nil)
}

func (s *Service) SaveInterface(ctx context.Context, principal Principal, id int64, req SaveInterfacePackageRequest) error {
	code := "package:interface:create"
	if id > 0 {
		code = "package:interface:update"
	}
	if err := s.require(ctx, principal, code); err != nil {
		return err
	}
	item, err := normalizeInterface(req)
	if err != nil {
		return err
	}
	if err := s.ensureRolesExist(ctx, item.RoleIDs); err != nil {
		return err
	}
	return mapErr(s.repo.SaveInterface(ctx, id, item), "接口套餐不存在")
}

func (s *Service) DeleteInterface(ctx context.Context, principal Principal, id int64, force bool) error {
	if err := s.require(ctx, principal, "package:interface:delete"); err != nil {
		return err
	}
	return mapDeleteErr(s.repo.DeleteInterface(ctx, id, force), "接口套餐不存在")
}

func (s *Service) ListPoint(ctx context.Context, principal Principal, params ListParams) (PageResult[PointPackage], error) {
	if err := s.require(ctx, principal, "package:point:list"); err != nil {
		return PageResult[PointPackage]{}, err
	}
	if err := validateListParams(&params); err != nil {
		return PageResult[PointPackage]{}, err
	}
	return s.repo.ListPoint(ctx, params, false, nil)
}

func (s *Service) SavePoint(ctx context.Context, principal Principal, id int64, req SavePointPackageRequest) error {
	code := "package:point:create"
	if id > 0 {
		code = "package:point:update"
	}
	if err := s.require(ctx, principal, code); err != nil {
		return err
	}
	item, err := normalizePoint(req)
	if err != nil {
		return err
	}
	if err := s.ensureRolesExist(ctx, item.RoleIDs); err != nil {
		return err
	}
	return mapErr(s.repo.SavePoint(ctx, id, item), "点数套餐不存在")
}

func (s *Service) DeletePoint(ctx context.Context, principal Principal, id int64, force bool) error {
	if err := s.require(ctx, principal, "package:point:delete"); err != nil {
		return err
	}
	return mapDeleteErr(s.repo.DeletePoint(ctx, id, force), "点数套餐不存在")
}

// ListUserEntitlements 返回全部用户的套餐权益（管理端）。
func (s *Service) ListUserEntitlements(ctx context.Context, principal Principal, params ListUserEntitlementParams) (PageResult[UserEntitlement], error) {
	if err := s.require(ctx, principal, "package:user:list"); err != nil {
		return PageResult[UserEntitlement]{}, err
	}
	if err := validateUserEntitlementParams(&params); err != nil {
		return PageResult[UserEntitlement]{}, err
	}
	return s.repo.ListUserEntitlements(ctx, params)
}

// UpdateUserEntitlement 编辑单个用户的套餐权益（状态/到期时间/额度）。
func (s *Service) UpdateUserEntitlement(ctx context.Context, principal Principal, id int64, req UpdateUserEntitlementRequest) error {
	if err := s.require(ctx, principal, "package:user:update"); err != nil {
		return err
	}
	edit, err := normalizeUserEntitlementEdit(req)
	if err != nil {
		return err
	}
	return mapErr(s.repo.UpdateUserEntitlement(ctx, id, edit), "用户套餐权益不存在")
}

// DeleteUserEntitlement 撤销（删除）单个用户的套餐权益。
func (s *Service) DeleteUserEntitlement(ctx context.Context, principal Principal, id int64, scope string) error {
	if err := s.require(ctx, principal, "package:user:delete"); err != nil {
		return err
	}
	normalized, err := normalizeEntitlementScope(scope)
	if err != nil {
		return err
	}
	return mapErr(s.repo.DeleteUserEntitlement(ctx, id, normalized), "用户套餐权益不存在")
}

// maxGrantUsers 单次下发最多覆盖的用户数；防止误粘贴超大名单拖死事务。
const maxGrantUsers = 200

// GrantUserEntitlement 管理端把套餐权益直接下发给指定用户：
// 不扣余额、不受商城售卖窗口/用户组限制约束，隐藏套餐正是靠这条通道触达用户。
// 每人各发一份，权益额度与有效期取套餐当前配置（EndAt 非空时覆盖有效期）。
func (s *Service) GrantUserEntitlement(ctx context.Context, principal Principal, req GrantUserEntitlementRequest) (GrantUserEntitlementResult, error) {
	if err := s.require(ctx, principal, "package:user:grant"); err != nil {
		return GrantUserEntitlementResult{}, err
	}
	scope, err := normalizeGrantScope(req.Scope)
	if err != nil {
		return GrantUserEntitlementResult{}, err
	}
	users, err := normalizeGrantUsers(req.Users)
	if err != nil {
		return GrantUserEntitlementResult{}, err
	}
	endAt, err := parseOptionalBusinessTime(req.EndAt)
	if err != nil {
		return GrantUserEntitlementResult{}, apperrors.New(consts.StatusBadRequest, "到期时间格式不合法")
	}
	return s.repo.GrantUserEntitlement(ctx, scope, req.PackageID, req.SpecID, users, endAt)
}

// normalizeGrantScope 下发允许三类套餐；点数套餐下发的是点数余额。
func normalizeGrantScope(scope string) (string, error) {
	switch strings.TrimSpace(strings.ToUpper(scope)) {
	case "GLOBAL":
		return "GLOBAL", nil
	case "INTERFACE":
		return "INTERFACE", nil
	case "POINT":
		return "POINT", nil
	default:
		return "", apperrors.New(consts.StatusBadRequest, "权益类型不合法")
	}
}

// normalizeGrantUsers 去空白、去重；用户名与数字 ID 混填，交由仓储层统一解析。
func normalizeGrantUsers(raw []string) ([]string, error) {
	seen := make(map[string]struct{}, len(raw))
	users := make([]string, 0, len(raw))
	for _, item := range raw {
		text := strings.TrimSpace(item)
		if text == "" {
			continue
		}
		if utf8.RuneCountInString(text) > 64 {
			return nil, apperrors.New(consts.StatusBadRequest, "用户名或用户ID不合法")
		}
		if _, exists := seen[text]; exists {
			continue
		}
		seen[text] = struct{}{}
		users = append(users, text)
	}
	if len(users) == 0 {
		return nil, apperrors.New(consts.StatusBadRequest, "请填写要下发的用户")
	}
	if len(users) > maxGrantUsers {
		return nil, apperrors.New(consts.StatusBadRequest, "单次下发的用户不能超过 200 个")
	}
	return users, nil
}

func validateUserEntitlementParams(params *ListUserEntitlementParams) error {
	params.Keyword = strings.TrimSpace(params.Keyword)
	if err := listquery.Validate(params.Keyword, params.Page, params.PageSize, 100); err != nil {
		return err
	}
	if err := listquery.Binary("status", params.Status); err != nil {
		return err
	}
	return nil
}

func normalizeEntitlementScope(scope string) (string, error) {
	switch strings.TrimSpace(strings.ToUpper(scope)) {
	case "GLOBAL":
		return "GLOBAL", nil
	case "INTERFACE":
		return "INTERFACE", nil
	default:
		return "", apperrors.New(consts.StatusBadRequest, "权益类型不合法")
	}
}

// userEntitlementEdit 是归一化后的权益编辑载荷；指针为 nil 表示保持原值。
type userEntitlementEdit struct {
	Scope      string
	Status     *int
	EndAt      *string
	DailyLimit *int64
	TotalLimit *int64
	CountMode  *string
	QPSLimit   *int
}

func normalizeUserEntitlementEdit(req UpdateUserEntitlementRequest) (userEntitlementEdit, error) {
	scope, err := normalizeEntitlementScope(req.Scope)
	if err != nil {
		return userEntitlementEdit{}, err
	}
	edit := userEntitlementEdit{Scope: scope}
	if req.Status != nil {
		status := *req.Status
		if status != 0 && status != 1 {
			return userEntitlementEdit{}, apperrors.New(consts.StatusBadRequest, "套餐参数不合法")
		}
		edit.Status = &status
	}
	if text := strings.TrimSpace(req.EndAt); text != "" {
		parsed, err := time.ParseInLocation("2006-01-02 15:04:05", text, businessclock.Location())
		if err != nil {
			if parsed, err = time.Parse(time.RFC3339, text); err != nil {
				return userEntitlementEdit{}, apperrors.New(consts.StatusBadRequest, "到期时间格式不合法")
			}
		}
		normalized := parsed.In(businessclock.Location()).Format("2006-01-02 15:04:05")
		edit.EndAt = &normalized
	}
	if req.DailyLimit != nil {
		if *req.DailyLimit < 0 || *req.DailyLimit > maxPackageDailyLimit {
			return userEntitlementEdit{}, apperrors.New(consts.StatusBadRequest, "套餐参数不合法")
		}
		edit.DailyLimit = req.DailyLimit
	}
	if req.TotalLimit != nil {
		if *req.TotalLimit < 0 || *req.TotalLimit > maxPackageDailyLimit {
			return userEntitlementEdit{}, apperrors.New(consts.StatusBadRequest, "套餐参数不合法")
		}
		edit.TotalLimit = req.TotalLimit
	}
	if text := strings.TrimSpace(req.CountMode); text != "" {
		mode, err := normalizeCountModeStrict(text)
		if err != nil {
			return userEntitlementEdit{}, err
		}
		edit.CountMode = &mode
	}
	if req.QPSLimit != nil {
		if *req.QPSLimit < 0 || *req.QPSLimit > maxPackageQPS {
			return userEntitlementEdit{}, apperrors.New(consts.StatusBadRequest, "套餐参数不合法")
		}
		edit.QPSLimit = req.QPSLimit
	}
	if edit.Status == nil && edit.EndAt == nil && edit.DailyLimit == nil && edit.TotalLimit == nil && edit.CountMode == nil && edit.QPSLimit == nil {
		return userEntitlementEdit{}, apperrors.New(consts.StatusBadRequest, "请至少提供一个变更字段")
	}
	return edit, nil
}

func (s *Service) MallGlobal(ctx context.Context, principal Principal, params ListParams) (PageResult[GlobalPackage], error) {
	if err := s.require(ctx, principal, "package:mall:list"); err != nil {
		return PageResult[GlobalPackage]{}, err
	}
	if err := validateListParams(&params); err != nil {
		return PageResult[GlobalPackage]{}, err
	}
	viewer, err := s.viewerFor(ctx, principal.ID)
	if err != nil {
		return PageResult[GlobalPackage]{}, err
	}
	result, err := s.repo.ListGlobal(ctx, params, true, viewer)
	if err != nil {
		return PageResult[GlobalPackage]{}, err
	}
	s.localizeGlobalPackages(ctx, result.Items)
	return result, nil
}

func (s *Service) MallInterface(ctx context.Context, principal Principal, params ListParams) (PageResult[InterfacePackage], error) {
	if err := s.require(ctx, principal, "package:mall:list"); err != nil {
		return PageResult[InterfacePackage]{}, err
	}
	if err := validateListParams(&params); err != nil {
		return PageResult[InterfacePackage]{}, err
	}
	viewer, err := s.viewerFor(ctx, principal.ID)
	if err != nil {
		return PageResult[InterfacePackage]{}, err
	}
	result, err := s.repo.ListInterface(ctx, params, true, viewer)
	if err != nil {
		return PageResult[InterfacePackage]{}, err
	}
	s.localizeInterfacePackages(ctx, result.Items)
	return result, nil
}

func (s *Service) MallInterfaceDetail(ctx context.Context, principal Principal, id int64) (InterfacePackage, error) {
	if err := s.require(ctx, principal, "package:mall:list"); err != nil {
		return InterfacePackage{}, err
	}
	viewer, err := s.viewerFor(ctx, principal.ID)
	if err != nil {
		return InterfacePackage{}, err
	}
	item, err := s.repo.InterfaceByID(ctx, id, true, viewer)
	if err != nil {
		return InterfacePackage{}, mapErr(err, "接口套餐不存在或未启用")
	}
	s.localizeInterfacePackage(ctx, &item)
	return item, nil
}

func (s *Service) MallPoint(ctx context.Context, principal Principal, params ListParams) (PageResult[PointPackage], error) {
	if err := s.require(ctx, principal, "package:mall:list"); err != nil {
		return PageResult[PointPackage]{}, err
	}
	if err := validateListParams(&params); err != nil {
		return PageResult[PointPackage]{}, err
	}
	viewer, err := s.viewerFor(ctx, principal.ID)
	if err != nil {
		return PageResult[PointPackage]{}, err
	}
	result, err := s.repo.ListPoint(ctx, params, true, viewer)
	if err != nil {
		return PageResult[PointPackage]{}, err
	}
	s.localizePointPackages(ctx, result.Items)
	return result, nil
}

func (s *Service) localizeGlobalPackages(ctx context.Context, items []GlobalPackage) {
	if s.localizer == nil || len(items) == 0 {
		return
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.ID)
	}
	overrides := s.localizer.LocalizeBatch(ctx, "package_global", keys)
	for index := range items {
		applyPackageTranslations(&items[index].Name, &items[index].Remark, overrides[items[index].ID])
	}
}

func (s *Service) localizeInterfacePackages(ctx context.Context, items []InterfacePackage) {
	if s.localizer == nil || len(items) == 0 {
		return
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.ID)
	}
	overrides := s.localizer.LocalizeBatch(ctx, "package_interface", keys)
	for index := range items {
		applyPackageTranslations(&items[index].Name, &items[index].Remark, overrides[items[index].ID])
	}
}

func (s *Service) localizeInterfacePackage(ctx context.Context, item *InterfacePackage) {
	if s.localizer == nil || item == nil {
		return
	}
	applyPackageTranslations(&item.Name, &item.Remark, s.localizer.Localize(ctx, "package_interface", item.ID))
	if len(item.Specs) > 0 {
		keys := make([]string, 0, len(item.Specs))
		for _, spec := range item.Specs {
			keys = append(keys, spec.ID)
		}
		overrides := s.localizer.LocalizeBatch(ctx, "package_interface_spec", keys)
		for index := range item.Specs {
			if value, ok := overrides[item.Specs[index].ID]["specName"]; ok {
				item.Specs[index].SpecName = value
			}
		}
	}
}

func (s *Service) localizePointPackages(ctx context.Context, items []PointPackage) {
	if s.localizer == nil || len(items) == 0 {
		return
	}
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.ID)
	}
	overrides := s.localizer.LocalizeBatch(ctx, "package_point", keys)
	for index := range items {
		applyPackageTranslations(&items[index].Name, &items[index].Remark, overrides[items[index].ID])
	}
}

func applyPackageTranslations(name, remark *string, translations map[string]string) {
	if len(translations) == 0 {
		return
	}
	if value, ok := translations["name"]; ok {
		*name = value
	}
	if value, ok := translations["remark"]; ok {
		*remark = value
	}
}

func (s *Service) PurchaseGlobal(ctx context.Context, principal Principal, id int64, idempotencyKey string) error {
	if err := s.require(ctx, principal, "package:mall:purchase"); err != nil {
		return err
	}
	key, err := normalizePurchaseIdempotencyKey(idempotencyKey)
	if err != nil {
		return err
	}
	return mapPurchaseErr(s.repo.PurchaseGlobal(ctx, principal.ID, id, key))
}

func (s *Service) PurchaseInterface(ctx context.Context, principal Principal, specID int64, idempotencyKey string) error {
	if err := s.require(ctx, principal, "package:mall:purchase"); err != nil {
		return err
	}
	key, err := normalizePurchaseIdempotencyKey(idempotencyKey)
	if err != nil {
		return err
	}
	return mapPurchaseErr(s.repo.PurchaseInterface(ctx, principal.ID, specID, key))
}

func (s *Service) PurchasePoint(ctx context.Context, principal Principal, id int64, idempotencyKey string) error {
	if err := s.require(ctx, principal, "package:mall:purchase"); err != nil {
		return err
	}
	key, err := normalizePurchaseIdempotencyKey(idempotencyKey)
	if err != nil {
		return err
	}
	return mapPurchaseErr(s.repo.PurchasePoint(ctx, principal.ID, id, key))
}

func (s *Service) UserPackages(ctx context.Context, principal Principal) ([]UserPackage, error) {
	return s.repo.UserPackages(ctx, principal.ID)
}

// SetPrimaryPackage 由用户自己指定主套餐，或传 id=0 取消。
// 主套餐只影响扣减顺序，不改变权益本身的有效期与额度。
func (s *Service) SetPrimaryPackage(ctx context.Context, principal Principal, req SetPrimaryPackageRequest) error {
	scope, err := normalizeEntitlementScope(req.Scope)
	if err != nil {
		return err
	}
	if req.ID < 0 {
		return apperrors.New(consts.StatusBadRequest, "套餐编号不合法")
	}
	return mapErr(s.repo.SetPrimaryPackage(ctx, principal.ID, scope, req.ID), "套餐权益不存在或已失效")
}

// UserPackageDetails 返回开放 API 账号套餐聚合所需的权益富快照
// （调用方已完成开放 Key 鉴权并强制 owner=本人）。
func (s *Service) UserPackageDetails(ctx context.Context, userID int64) ([]UserPackageDetail, error) {
	return s.repo.UserPackageDetails(ctx, userID)
}

func (s *Service) WalletTransactions(ctx context.Context, principal Principal, params ListParams) (PageResult[WalletTransaction], error) {
	if err := validateListParams(&params); err != nil {
		return PageResult[WalletTransaction]{}, err
	}
	return s.repo.WalletTransactions(ctx, principal.ID, params)
}

func (s *Service) BillingRecords(ctx context.Context, principal Principal, params ListParams) (PageResult[BillingRecord], error) {
	if err := validateListParams(&params); err != nil {
		return PageResult[BillingRecord]{}, err
	}
	return s.repo.BillingRecords(ctx, principal.ID, false, params)
}

func validateListParams(params *ListParams) error {
	params.Keyword = strings.TrimSpace(params.Keyword)
	if err := listquery.Validate(params.Keyword, params.Page, params.PageSize, 100); err != nil {
		return err
	}
	if err := listquery.Binary("status", params.Status); err != nil {
		return err
	}
	return listquery.NonNegativeID("interfaceId", params.InterfaceID)
}

func (s *Service) require(ctx context.Context, principal Principal, code string) error {
	return s.authz.Require(ctx, rbac.Principal{ID: principal.ID, Username: principal.Username, Roles: principal.Roles, TokenVersion: principal.TokenVersion}, code)
}

// viewerFor 解析商城浏览者所属的用户组（角色）。
func (s *Service) viewerFor(ctx context.Context, userID int64) (*PackageViewer, error) {
	roleIDs, err := s.repo.UserRoleIDs(ctx, userID)
	if err != nil {
		return nil, err
	}
	return &PackageViewer{RoleIDs: roleIDs}, nil
}

func (s *Service) ensureRolesExist(ctx context.Context, roleIDs []int64) error {
	if len(roleIDs) == 0 {
		return nil
	}
	count, err := s.repo.CountRolesByIDs(ctx, roleIDs)
	if err != nil {
		return err
	}
	if count != len(roleIDs) {
		return apperrors.New(consts.StatusBadRequest, "用户组不存在或已删除")
	}
	return nil
}

func normalizeGlobal(req SaveGlobalPackageRequest) (GlobalPackage, error) {
	price, err := normalizeMoney(req.Price)
	if err != nil {
		return GlobalPackage{}, err
	}
	status := flag(req.Status, 1)
	name := strings.TrimSpace(req.Name)
	remark := strings.TrimSpace(req.Remark)
	if name == "" || utf8.RuneCountInString(name) > 128 || utf8.RuneCountInString(remark) > 255 ||
		req.ValidDays <= 0 || req.ValidDays > maxPackageValidDays || req.DailyLimit < 0 || req.DailyLimit > maxPackageDailyLimit ||
		req.QPSLimit < 0 || req.QPSLimit > maxPackageQPS || (status != 0 && status != 1) ||
		req.StockLimit < 0 || req.StockLimit > maxPackageStock || req.PerUserLimit < 0 || req.PerUserLimit > maxPackageStock {
		return GlobalPackage{}, apperrors.New(consts.StatusBadRequest, "套餐参数不合法")
	}
	saleStart, saleEnd, err := normalizeSaleWindow(req.SaleStartAt, req.SaleEndAt)
	if err != nil {
		return GlobalPackage{}, err
	}
	if err := validateTotalLimit(req.DailyLimit, req.TotalLimit); err != nil {
		return GlobalPackage{}, err
	}
	countMode, err := normalizeCountModeStrict(req.CountMode)
	if err != nil {
		return GlobalPackage{}, err
	}
	roleRestricted, roleVisibleOnly, roleIDs, err := normalizePackageRoleLimit(req.RoleRestricted, req.RoleVisibleOnly, req.RoleIDs)
	if err != nil {
		return GlobalPackage{}, err
	}
	hidden, err := normalizeHidden(req.Hidden)
	if err != nil {
		return GlobalPackage{}, err
	}
	renewalEnabled, err := normalizeRenewalEnabled(req.RenewalEnabled)
	if err != nil {
		return GlobalPackage{}, err
	}
	return GlobalPackage{
		Name: name, Price: price, ValidDays: req.ValidDays, DailyLimit: req.DailyLimit, TotalLimit: req.TotalLimit,
		CountMode: countMode, QPSLimit: req.QPSLimit,
		Status: status, Remark: remark, StockLimit: req.StockLimit, PerUserLimit: req.PerUserLimit,
		SaleStartAt: saleStart, SaleEndAt: saleEnd,
		RoleRestricted: roleRestricted, RoleVisibleOnly: roleVisibleOnly, RoleIDs: roleIDs,
		Hidden: hidden, RenewalEnabled: renewalEnabled,
	}, nil
}

// normalizePackageRoleLimit 校验并归一化「限制用户组购买」配置。
// 未启用限制时强制清空可见性标记与用户组，避免残留配置在下次开启时意外生效。
// normalizeCountMode 归一化套餐计量方式：空值/未知值一律落回 success
// （与既有「失败释放回退计数」的行为一致），用于入库前的兜底。
// normalizeRenewalEnabled 归一化续费开关：nil 视为开启（与列默认值 1 一致），
// 显式传值必须是 0/1。老客户端不带这个字段时不应把续费意外关掉。
func normalizeRenewalEnabled(value *int) (int, error) {
	if value == nil {
		return 1, nil
	}
	if *value != 0 && *value != 1 {
		return 0, apperrors.New(consts.StatusBadRequest, "续费开关参数不合法")
	}
	return *value, nil
}

func normalizeCountMode(mode string) string {
	if strings.TrimSpace(strings.ToLower(mode)) == "attempt" {
		return "attempt"
	}
	return "success"
}

// normalizeCountModeStrict 供管理端编辑使用：显式传入非法值要报错，不静默纠正。
func normalizeCountModeStrict(mode string) (string, error) {
	switch strings.TrimSpace(strings.ToLower(mode)) {
	case "", "success":
		return "success", nil
	case "attempt":
		return "attempt", nil
	default:
		return "", apperrors.New(consts.StatusBadRequest, "计量方式不合法")
	}
}

// validateTotalLimit 校验总调用量与日限的阶梯关系：两者都配置时总量必须 >= 日限，
// 否则日限永远先于总量触顶，总调用量配置成为死配置；0 = 不限，不参与比较。
func validateTotalLimit(daily, total int64) error {
	if total < 0 || total > maxPackageDailyLimit {
		return apperrors.New(consts.StatusBadRequest, "套餐参数不合法")
	}
	if daily > 0 && total > 0 && total < daily {
		return apperrors.New(consts.StatusBadRequest, "总调用量不能小于每日上限")
	}
	return nil
}

func normalizePackageRoleLimit(restricted, visibleOnly int, roleIDs []int64) (int, int, []int64, error) {
	if (restricted != 0 && restricted != 1) || (visibleOnly != 0 && visibleOnly != 1) {
		return 0, 0, nil, apperrors.New(consts.StatusBadRequest, "用户组限制参数不合法")
	}
	if restricted == 0 {
		return 0, 0, nil, nil
	}
	if visibleOnly != 1 {
		visibleOnly = 0
	}
	if len(roleIDs) == 0 {
		return 0, 0, nil, apperrors.New(consts.StatusBadRequest, "请选择允许购买的用户组")
	}
	if len(roleIDs) > maxPackageRoles {
		return 0, 0, nil, apperrors.New(consts.StatusBadRequest, "允许的用户组不能超过 200 个")
	}
	normalized := make([]int64, 0, len(roleIDs))
	seen := make(map[int64]struct{}, len(roleIDs))
	for _, roleID := range roleIDs {
		if roleID <= 0 {
			return 0, 0, nil, apperrors.New(consts.StatusBadRequest, "用户组参数不合法")
		}
		if _, exists := seen[roleID]; exists {
			continue
		}
		seen[roleID] = struct{}{}
		normalized = append(normalized, roleID)
	}
	return 1, visibleOnly, normalized, nil
}

// normalizeHidden 校验商城隐藏开关：nil 视为 0（不隐藏）。
func normalizeHidden(value *int) (int, error) {
	if value == nil {
		return 0, nil
	}
	if *value != 0 && *value != 1 {
		return 0, apperrors.New(consts.StatusBadRequest, "隐藏开关参数不合法")
	}
	return *value, nil
}

func normalizeSaleWindow(startRaw, endRaw string) (string, string, error) {
	start, err := parseOptionalBusinessTime(startRaw)
	if err != nil {
		return "", "", apperrors.New(consts.StatusBadRequest, "售卖开始时间格式不合法")
	}
	end, err := parseOptionalBusinessTime(endRaw)
	if err != nil {
		return "", "", apperrors.New(consts.StatusBadRequest, "售卖结束时间格式不合法")
	}
	if start != "" && end != "" && end <= start {
		return "", "", apperrors.New(consts.StatusBadRequest, "售卖结束时间必须晚于开始时间")
	}
	return start, end, nil
}

func parseOptionalBusinessTime(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", nil
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04:05", text, businessclock.Location())
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, text)
		if err != nil {
			return "", err
		}
	}
	return parsed.In(businessclock.Location()).Format("2006-01-02 15:04:05"), nil
}

func normalizeInterface(req SaveInterfacePackageRequest) (InterfacePackage, error) {
	status := flag(req.Status, 1)
	name := strings.TrimSpace(req.Name)
	remark := strings.TrimSpace(req.Remark)
	if req.InterfaceID <= 0 || name == "" || utf8.RuneCountInString(name) > 128 || utf8.RuneCountInString(remark) > 255 ||
		(status != 0 && status != 1) || len(req.Specs) == 0 || len(req.Specs) > maxPackageSpecs {
		return InterfacePackage{}, apperrors.New(consts.StatusBadRequest, "接口套餐参数不合法")
	}
	item := InterfacePackage{InterfaceID: fmt.Sprintf("%d", req.InterfaceID), Name: name, Status: status, Remark: remark}
	roleRestricted, roleVisibleOnly, roleIDs, err := normalizePackageRoleLimit(req.RoleRestricted, req.RoleVisibleOnly, req.RoleIDs)
	if err != nil {
		return InterfacePackage{}, err
	}
	item.RoleRestricted = roleRestricted
	item.RoleVisibleOnly = roleVisibleOnly
	item.RoleIDs = roleIDs
	hidden, err := normalizeHidden(req.Hidden)
	if err != nil {
		return InterfacePackage{}, err
	}
	item.Hidden = hidden
	for _, raw := range req.Specs {
		price, err := normalizeMoney(raw.Price)
		if err != nil {
			return InterfacePackage{}, err
		}
		specStatus := flag(raw.Status, 1)
		specName, err := normalizeSpecName(raw)
		if err != nil {
			return InterfacePackage{}, err
		}
		specRemark := strings.TrimSpace(raw.Remark)
		specID, idErr := normalizeOptionalSpecID(raw.ID)
		if idErr != nil {
			return InterfacePackage{}, idErr
		}
		if utf8.RuneCountInString(specName) > 128 || utf8.RuneCountInString(specRemark) > 255 || raw.SortNo < 0 || raw.SortNo > maxPackageSortNo ||
			raw.ValidDays <= 0 || raw.ValidDays > maxPackageValidDays ||
			raw.DailyLimit < 0 || raw.DailyLimit > maxPackageDailyLimit || raw.QPSLimit < 0 || raw.QPSLimit > maxPackageQPS ||
			raw.StockLimit < 0 || raw.StockLimit > maxPackageStock || raw.PerUserLimit < 0 || raw.PerUserLimit > maxPackageStock ||
			(specStatus != 0 && specStatus != 1) {
			return InterfacePackage{}, apperrors.New(consts.StatusBadRequest, "接口套餐规格参数不合法")
		}
		if err := validateTotalLimit(raw.DailyLimit, raw.TotalLimit); err != nil {
			return InterfacePackage{}, err
		}
		countMode, err := normalizeCountModeStrict(raw.CountMode)
		if err != nil {
			return InterfacePackage{}, err
		}
		saleStart, saleEnd, err := normalizeSaleWindow(raw.SaleStartAt, raw.SaleEndAt)
		if err != nil {
			return InterfacePackage{}, err
		}
		item.Specs = append(item.Specs, InterfacePackageSpec{
			ID:           specID,
			DailyLimit:   raw.DailyLimit,
			TotalLimit:   raw.TotalLimit,
			CountMode:    countMode,
			Price:        price,
			QPSLimit:     raw.QPSLimit,
			Remark:       specRemark,
			SortNo:       raw.SortNo,
			SpecName:     specName,
			Status:       specStatus,
			ValidDays:    raw.ValidDays,
			StockLimit:   raw.StockLimit,
			PerUserLimit: raw.PerUserLimit,
			SaleStartAt:  saleStart,
			SaleEndAt:    saleEnd,
		})
	}
	return item, nil
}

func normalizeOptionalSpecID(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", nil
	}
	id, err := strconv.ParseInt(text, 10, 64)
	if err != nil || id <= 0 {
		return "", apperrors.New(consts.StatusBadRequest, "接口套餐规格ID不合法")
	}
	return strconv.FormatInt(id, 10), nil
}

func normalizeSpecName(req SaveInterfacePackageSpecRequest) (string, error) {
	canonical := strings.TrimSpace(req.SpecName)
	legacy := strings.TrimSpace(req.Name)
	if canonical != "" && legacy != "" && canonical != legacy {
		return "", apperrors.New(consts.StatusBadRequest, "接口套餐规格名称冲突")
	}
	if canonical != "" {
		return canonical, nil
	}
	if legacy != "" {
		return legacy, nil
	}
	return "", apperrors.New(consts.StatusBadRequest, "接口套餐规格参数不合法")
}

func normalizePoint(req SavePointPackageRequest) (PointPackage, error) {
	price, err := normalizeMoney(req.Price)
	if err != nil {
		return PointPackage{}, err
	}
	status := flag(req.Status, 1)
	points, ok := normalizePointCount(req.Points)
	if points <= 0 {
		points, ok = normalizePointCount(req.PointAmount)
	}
	name := strings.TrimSpace(req.Name)
	remark := strings.TrimSpace(req.Remark)
	if !ok || name == "" || utf8.RuneCountInString(name) > 128 || utf8.RuneCountInString(remark) > 255 ||
		points <= 0 || points > maxPackagePoints || (status != 0 && status != 1) ||
		req.StockLimit < 0 || req.StockLimit > maxPackageStock || req.PerUserLimit < 0 || req.PerUserLimit > maxPackageStock {
		return PointPackage{}, apperrors.New(consts.StatusBadRequest, "点数套餐参数不合法")
	}
	saleStart, saleEnd, err := normalizeSaleWindow(req.SaleStartAt, req.SaleEndAt)
	if err != nil {
		return PointPackage{}, err
	}
	roleRestricted, roleVisibleOnly, roleIDs, err := normalizePackageRoleLimit(req.RoleRestricted, req.RoleVisibleOnly, req.RoleIDs)
	if err != nil {
		return PointPackage{}, err
	}
	hidden, err := normalizeHidden(req.Hidden)
	if err != nil {
		return PointPackage{}, err
	}
	return PointPackage{
		Name: name, Price: price, Points: points, PointAmount: points, Status: status, Remark: remark,
		StockLimit: req.StockLimit, PerUserLimit: req.PerUserLimit, SaleStartAt: saleStart, SaleEndAt: saleEnd,
		RoleRestricted: roleRestricted, RoleVisibleOnly: roleVisibleOnly, RoleIDs: roleIDs,
		Hidden: hidden,
	}, nil
}

func normalizePointCount(value any) (int64, bool) {
	switch v := value.(type) {
	case nil:
		return 0, false
	case int:
		return int64(v), true
	case int8:
		return int64(v), true
	case int16:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case uint:
		if uint64(v) > math.MaxInt64 {
			return 0, false
		}
		return int64(v), true
	case uint8:
		return int64(v), true
	case uint16:
		return int64(v), true
	case uint32:
		return int64(v), true
	case uint64:
		if v > math.MaxInt64 {
			return 0, false
		}
		return int64(v), true
	case float32:
		return normalizeFloatPointCount(float64(v))
	case float64:
		return normalizeFloatPointCount(v)
	case fmt.Stringer:
		return normalizePointCountString(v.String())
	case string:
		return normalizePointCountString(v)
	default:
		return normalizePointCountString(fmt.Sprintf("%v", value))
	}
}

func normalizeFloatPointCount(value float64) (int64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.Trunc(value) != value || value > math.MaxInt64 || value < math.MinInt64 {
		return 0, false
	}
	return int64(value), true
}

func normalizePointCountString(value string) (int64, bool) {
	text := strings.TrimSpace(value)
	if text == "" {
		return 0, false
	}
	if strings.Contains(text, ",") {
		if !groupedPointCountPattern.MatchString(text) {
			return 0, false
		}
		text = strings.ReplaceAll(text, ",", "")
	} else if !pointCountPattern.MatchString(text) {
		return 0, false
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

func normalizeMoney(value any) (string, error) {
	if value == nil {
		return "0.0000", nil
	}
	text := strings.TrimSpace(fmt.Sprintf("%v", value))
	if text == "" {
		text = "0"
	}
	rat, err := decimalvalue.ParseNonNegative(text)
	if errors.Is(err, decimalvalue.ErrNegative) {
		return "", apperrors.New(consts.StatusBadRequest, "金额不能为负数")
	}
	if errors.Is(err, decimalvalue.ErrExponentRange) || errors.Is(err, decimalvalue.ErrTextTooLong) {
		return "", apperrors.New(consts.StatusBadRequest, "金额超过系统允许范围")
	}
	if err != nil {
		return "", apperrors.New(consts.StatusBadRequest, "金额格式不合法")
	}
	maxMoney := new(big.Rat).SetFrac(big.NewInt(999_999_999_999), big.NewInt(10_000))
	if rat.Cmp(maxMoney) > 0 {
		return "", apperrors.New(consts.StatusBadRequest, "金额超过系统允许范围")
	}
	return rat.FloatString(4), nil
}

func flag(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func mapErr(err error, notFound string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return apperrors.New(consts.StatusNotFound, notFound)
	}
	if errors.Is(err, ErrPackageStockBelowUsed) {
		return apperrors.New(consts.StatusConflict, "限量份数不能低于已占用份数")
	}
	if errors.Is(err, ErrPackageSpecHasBusinessHistory) {
		return apperrors.New(consts.StatusConflict, "该规格已有购买或权益记录，不能删除，请改为禁用")
	}
	if errors.Is(err, ErrPackageSpecDuplicate) {
		return apperrors.New(consts.StatusBadRequest, "接口套餐规格ID重复")
	}
	return err
}

func mapDeleteErr(err error, notFound string) error {
	if errors.Is(err, ErrPackageHasBusinessHistory) {
		return apperrors.New(consts.StatusConflict, "该套餐已有购买、权益或支付记录，不能删除，请改为禁用")
	}
	return mapErr(err, notFound)
}

func mapPurchaseErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrInsufficientBalance) {
		return apperrors.New(consts.StatusBadRequest, "余额不足")
	}
	if errors.Is(err, ErrFreePackageAlreadyClaimed) {
		return apperrors.New(consts.StatusConflict, "免费套餐每个用户只能领取一次")
	}
	if errors.Is(err, ErrPointBalanceLimit) {
		return apperrors.New(consts.StatusBadRequest, "点数余额超过系统允许范围")
	}
	if errors.Is(err, ErrPackageSoldOut) {
		return apperrors.New(consts.StatusConflict, "该套餐已售罄")
	}
	if errors.Is(err, ErrPackageOffSale) {
		return apperrors.New(consts.StatusConflict, "该套餐暂未开售或已停止售卖")
	}
	if errors.Is(err, ErrPackageUserLimit) {
		return apperrors.New(consts.StatusConflict, "已达到该套餐的每人限购数量")
	}
	if errors.Is(err, ErrPackageRoleRestricted) {
		return apperrors.New(consts.StatusConflict, "当前用户组不可购买该套餐")
	}
	if errors.Is(err, ErrPurchaseIdempotencyConflict) {
		return apperrors.New(consts.StatusConflict, "幂等键已用于其他套餐购买")
	}
	if errors.Is(err, ErrPurchaseInProgress) {
		return apperrors.New(consts.StatusConflict, "购买请求正在处理中，请稍后使用相同幂等键重试")
	}
	if errors.Is(err, sql.ErrNoRows) {
		return apperrors.New(consts.StatusNotFound, "套餐不存在或未启用")
	}
	return err
}

func normalizePurchaseIdempotencyKey(value string) (string, error) {
	value = strings.TrimSpace(value)
	if !purchaseIdempotencyKeyPattern.MatchString(value) {
		return "", apperrors.New(consts.StatusBadRequest, "Idempotency-Key 必须是 16 到 64 位字母、数字、下划线或短横线")
	}
	return value, nil
}
