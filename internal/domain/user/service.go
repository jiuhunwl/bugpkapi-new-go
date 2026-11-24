package user

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"bugpk-api/internal/domain/auth"
	"bugpk-api/internal/pkg/accountstatus"
	"bugpk-api/internal/pkg/auditprincipal"
	"bugpk-api/internal/pkg/businessclock"
	"bugpk-api/internal/pkg/dbx"
	"bugpk-api/internal/pkg/decimalvalue"
	"bugpk-api/internal/pkg/emailaddr"
	apperrors "bugpk-api/internal/pkg/errors"
	"bugpk-api/internal/pkg/i18n"
	"bugpk-api/internal/pkg/listquery"
	"bugpk-api/internal/pkg/masking"
	"bugpk-api/internal/pkg/passwordpolicy"
	"bugpk-api/internal/pkg/redisutil"

	"go.uber.org/zap"

	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

type Service struct {
	repo                     *Repository
	jwt                      *auth.JWTService
	captcha                  *auth.CaptchaService
	code                     *auth.VerificationCodeService
	rdb                      *redis.Client
	authorizer               Authorizer
	accountTrafficChanged    func(context.Context)
	specifiedResponseChanged func(context.Context, int64)
	adminAccessCache         *adminAccessResultCache
	adminAccessCacheMu       sync.Mutex
	adminWorkbenchCache      *adminWorkbenchStatsCache
	adminWorkbenchCacheMu    sync.Mutex
	log                      *zap.Logger
	now                      func() time.Time
	// localizer 提供字典译文（用户组名等）；nil 时输出默认语言原文。
	localizer i18n.Localizer
}

// SetLocalizer 接入翻译字典；nil 时保持默认语言原文。
func (s *Service) SetLocalizer(localizer i18n.Localizer) {
	s.localizer = localizer
}

// localizeRoleName 按请求语言解析用户组名；无译文时返回原文。
func (s *Service) localizeRoleName(ctx context.Context, roleID int64, fallback string) string {
	if s.localizer == nil || roleID <= 0 || fallback == "" {
		return fallback
	}
	if value, ok := s.localizer.Localize(ctx, "role", strconv.FormatInt(roleID, 10))["roleName"]; ok {
		return value
	}
	return fallback
}

type Authorizer interface {
	HasPermission(ctx context.Context, userID int64, roles []string, code string) (bool, error)
}

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9_\-]{3,32}$`)
var mobilePattern = regexp.MustCompile(`^\+?[0-9]{6,32}$`)

const (
	maxUserBalance = int64(999_999_999_999_999_999)
	maxUserPoints  = int64(1_000_000_000_000_000)
	maxUserRoles   = 100
)

func NewService(repo *Repository, jwt *auth.JWTService, captcha *auth.CaptchaService, code *auth.VerificationCodeService, rdb *redis.Client) *Service {
	return &Service{
		repo:                repo,
		jwt:                 jwt,
		captcha:             captcha,
		code:                code,
		rdb:                 rdb,
		adminAccessCache:    newAdminAccessResultCache(),
		adminWorkbenchCache: newAdminWorkbenchStatsCache(),
		now:                 time.Now,
	}
}

func (s *Service) SetAuthorizer(authorizer Authorizer) {
	s.authorizer = authorizer
}

// SetLogger attaches the application logger so degraded workbench sections can
// be reported without failing the whole dashboard request.
func (s *Service) SetLogger(log *zap.Logger) {
	s.log = log
}

func (s *Service) SetAccountTrafficChanged(callback func(context.Context)) {
	s.accountTrafficChanged = callback
}

func (s *Service) SetSpecifiedResponseChanged(callback func(context.Context, int64)) {
	s.specifiedResponseChanged = callback
}

func (s *Service) Authenticate(ctx context.Context, accessToken string) (Principal, UserRecord, error) {
	info, err := s.jwt.ParseAccessToken(strings.TrimSpace(accessToken))
	if err != nil {
		return Principal{}, UserRecord{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
	}
	record, err := s.repo.FindByID(ctx, info.UserID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Principal{}, UserRecord{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
		}
		return Principal{}, UserRecord{}, apperrors.Wrap(consts.StatusServiceUnavailable, "认证服务暂时不可用", err)
	}
	if !accountstatus.IsActive(record.Status) {
		return Principal{}, UserRecord{}, accountstatus.Forbidden(record.BanPermanent, record.BanUntil, record.BanReason)
	}
	if record.TokenVersion != info.TokenVersion {
		return Principal{}, UserRecord{}, apperrors.New(consts.StatusUnauthorized, "Unauthorized Exception")
	}
	auditprincipal.Record(ctx, accessToken, auditprincipal.Identity{UserID: record.ID, Username: record.Username, Roles: record.Roles})
	return Principal{ID: record.ID, Username: record.Username, Roles: record.Roles, TokenVersion: record.TokenVersion}, record, nil
}

func (s *Service) UserInfo(ctx context.Context, principal Principal) (UserInfo, error) {
	record, err := s.repo.FindByID(ctx, principal.ID)
	if err != nil {
		return UserInfo{}, err
	}
	return toUserInfo(record), nil
}

// AccountSnapshot 返回开放 API 账号聚合所需的只读快照。调用方（openapi 域）
// 已完成开放 Key 鉴权并强制 owner=本人，故此处不做 RBAC 校验。
func (s *Service) AccountSnapshot(ctx context.Context, userID int64) (AccountSnapshot, error) {
	return s.repo.AccountSnapshot(ctx, userID)
}

func (s *Service) UpdateProfile(ctx context.Context, principal Principal, req UpdateProfileRequest) (UserInfo, error) {
	req.RealName = strings.TrimSpace(req.RealName)
	if req.RealName == "" {
		return UserInfo{}, apperrors.New(consts.StatusBadRequest, "姓名不能为空")
	}
	if utf8.RuneCountInString(req.RealName) > 64 {
		return UserInfo{}, apperrors.New(consts.StatusBadRequest, "姓名不能超过64个字符")
	}
	req.Avatar = strings.TrimSpace(req.Avatar)
	if utf8.RuneCountInString(req.Avatar) > 512 || !validUserURL(req.Avatar, true) {
		return UserInfo{}, apperrors.New(consts.StatusBadRequest, "头像地址不合法或超过512个字符")
	}
	if err := s.repo.UpdateProfile(ctx, principal.ID, req.RealName, req.Avatar); err != nil {
		return UserInfo{}, err
	}
	return s.UserInfo(ctx, principal)
}

func (s *Service) SendBindEmailCode(ctx context.Context, principal Principal, req SendBindEmailCodeRequest, ip, lang string) error {
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if !validUserEmail(req.Email) {
		return apperrors.New(consts.StatusBadRequest, "邮箱格式不合法")
	}
	exists, err := s.repo.EmailExists(ctx, req.Email, principal.ID)
	if err != nil {
		return err
	}
	if exists {
		return apperrors.New(consts.StatusBadRequest, "邮箱已被其他账号绑定")
	}
	return s.code.SendBindEmail(ctx, req.Email, ip, lang)
}

func (s *Service) BindEmail(ctx context.Context, principal Principal, req BindEmailRequest) (UserInfo, error) {
	email := strings.ToLower(strings.TrimSpace(req.Email))
	if !validUserEmail(email) {
		return UserInfo{}, apperrors.New(consts.StatusBadRequest, "邮箱格式不合法")
	}
	if err := s.code.VerifyBindEmail(ctx, email, req.Code); err != nil {
		return UserInfo{}, err
	}
	exists, err := s.repo.EmailExists(ctx, email, principal.ID)
	if err != nil {
		return UserInfo{}, err
	}
	if exists {
		return UserInfo{}, apperrors.New(consts.StatusBadRequest, "绑定失败，请重新获取验证码")
	}
	if err := s.repo.BindEmail(ctx, principal.ID, email); err != nil {
		return UserInfo{}, err
	}
	return s.UserInfo(ctx, principal)
}

func (s *Service) SendBindMobileCode(ctx context.Context, principal Principal, req SendBindMobileCodeRequest, ip string) error {
	if s.code == nil || !s.code.MobileEnabled(ctx) {
		return apperrors.New(consts.StatusBadRequest, "短信验证码服务未配置")
	}
	req.Mobile = strings.TrimSpace(req.Mobile)
	if !mobilePattern.MatchString(req.Mobile) {
		return apperrors.New(consts.StatusBadRequest, "手机号格式不合法")
	}
	exists, err := s.repo.MobileExists(ctx, req.Mobile, principal.ID)
	if err != nil {
		return err
	}
	if exists {
		return apperrors.New(consts.StatusBadRequest, "手机号已被其他账号绑定")
	}
	return s.code.SendBindMobile(ctx, req.Mobile, ip)
}

func (s *Service) BindMobile(ctx context.Context, principal Principal, req BindMobileRequest) (UserInfo, error) {
	if s.code == nil || !s.code.MobileEnabled(ctx) {
		return UserInfo{}, apperrors.New(consts.StatusBadRequest, "短信验证码服务未配置")
	}
	mobile := strings.TrimSpace(req.Mobile)
	if !mobilePattern.MatchString(mobile) {
		return UserInfo{}, apperrors.New(consts.StatusBadRequest, "手机号格式不合法")
	}
	if err := s.code.VerifyBindMobile(ctx, mobile, req.Code); err != nil {
		return UserInfo{}, err
	}
	exists, err := s.repo.MobileExists(ctx, mobile, principal.ID)
	if err != nil {
		return UserInfo{}, err
	}
	if exists {
		return UserInfo{}, apperrors.New(consts.StatusBadRequest, "绑定失败，请重新获取验证码")
	}
	if err := s.repo.BindMobile(ctx, principal.ID, mobile); err != nil {
		return UserInfo{}, err
	}
	return s.UserInfo(ctx, principal)
}

func (s *Service) ChangePassword(ctx context.Context, principal Principal, req ChangePasswordRequest, binding string) error {
	if err := s.captcha.Consume(ctx, req.CaptchaID, "change_password", binding); err != nil {
		return err
	}
	if err := s.checkPasswordChangeAllowed(ctx, principal.ID); err != nil {
		return err
	}
	if !passwordpolicy.Valid(req.NewPassword) {
		return apperrors.New(consts.StatusBadRequest, "新密码必须为8到64个字符，且不能超过72字节")
	}
	if req.NewPassword != req.ConfirmPassword {
		return apperrors.New(consts.StatusBadRequest, "两次输入的新密码不一致")
	}
	if req.NewPassword == req.OldPassword {
		return apperrors.New(consts.StatusBadRequest, "新密码不能与旧密码相同")
	}
	record, err := s.repo.FindByID(ctx, principal.ID)
	if err != nil {
		return err
	}
	if bcrypt.CompareHashAndPassword([]byte(record.PasswordHash), []byte(req.OldPassword)) != nil {
		if err := s.recordPasswordChangeFailure(ctx, principal.ID); err != nil {
			return err
		}
		return apperrors.New(consts.StatusBadRequest, "当前密码不正确")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.NewPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := s.rdb.Del(ctx, passwordChangeAttemptKey(principal.ID)).Err(); err != nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "密码修改风控服务暂不可用", err)
	}
	if err := s.repo.UpdateOwnPassword(ctx, principal.ID, string(hash)); err != nil {
		return err
	}
	return nil
}

func (s *Service) SetInitialPassword(ctx context.Context, principal Principal, req SetPasswordRequest, binding string) error {
	if s.captcha != nil {
		if err := s.captcha.Consume(ctx, req.CaptchaID, "change_password", binding); err != nil {
			return err
		}
	}
	if !passwordpolicy.Valid(req.Password) {
		return apperrors.New(consts.StatusBadRequest, "密码必须为8到64个字符，且不能超过72字节")
	}
	if req.Password != req.ConfirmPassword {
		return apperrors.New(consts.StatusBadRequest, "两次输入的密码不一致")
	}
	hasPassword, err := s.repo.PasswordSet(ctx, principal.ID)
	if err != nil {
		return err
	}
	if hasPassword {
		return apperrors.New(consts.StatusBadRequest, "该账号已设置密码，请使用修改密码")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	if err := s.repo.SetInitialPassword(ctx, principal.ID, string(hash)); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apperrors.New(consts.StatusBadRequest, "密码已设置，请刷新页面")
		}
		return err
	}
	return nil
}

func (s *Service) QQBindingStatus(context.Context, Principal) QQBindingStatus {
	return QQBindingStatus{Enabled: false, Bound: false}
}

func (s *Service) QQBindingAuthorize(context.Context, Principal) (QQBindingAuthorizeResult, error) {
	return QQBindingAuthorizeResult{}, apperrors.New(consts.StatusBadRequest, "QQ绑定暂未启用")
}

func (s *Service) UnbindQQ(context.Context, Principal) (bool, error) {
	return false, apperrors.New(consts.StatusBadRequest, "QQ绑定暂未启用")
}

func (s *Service) UserWorkbenchStats(ctx context.Context, principal Principal) (UserWorkbenchStats, error) {
	var (
		record            UserRecord
		summary           UserCallSummary
		memberPackage     *UserWorkbenchMemberPackage
		interfacePackages []UserWorkbenchInterfacePackage
		dailyCalls        map[string]int64
		primaryRoleQPS    int
		roleQuota         UserRoleQuota
		outcomes          CallOutcomeStats
	)
	if err := dbx.RunParallel(
		func() error {
			var err error
			record, err = s.repo.FindByID(ctx, principal.ID)
			return err
		},
		func() error {
			var err error
			summary, err = s.repo.UserCallSummary(ctx, principal.ID)
			return err
		},
		func() error {
			var err error
			memberPackage, interfacePackages, err = s.repo.UserPackagesForWorkbench(ctx, principal.ID)
			return err
		},
		func() error {
			var err error
			dailyCalls, err = s.repo.UserDailyCallCounts(ctx, principal.ID)
			return err
		},
		func() error {
			var err error
			primaryRoleQPS, err = s.repo.PrimaryRoleQPS(ctx, principal.ID)
			return err
		},
		func() error {
			var err error
			roleQuota, err = s.repo.PrimaryRoleQuota(ctx, principal.ID)
			return err
		},
		func() error {
			// Composition is supplementary: an old or rebuilding outcome layer
			// must not blank today's calls, quota and balance cards.
			if value, err := s.repo.userTodayOutcomes(ctx, principal.ID); err == nil {
				outcomes = value
			}
			return nil
		},
	); err != nil {
		return UserWorkbenchStats{}, err
	}
	now := s.now()
	trends := buildUserCallTrends(now, dailyCalls)
	hasMember := memberPackage != nil
	hasAny := hasMember || len(interfacePackages) > 0
	roleDayUsed := summary.TodayCalls
	if roleQuota.CountMode == "success" {
		roleDayUsed = summary.TodaySuccessCalls
	}
	roleDayRemaining, roleDayUnlimited := roleQuotaDayRemaining(roleQuota, roleDayUsed)
	quota := summarizeWorkbenchQuota(memberPackage, interfacePackages, primaryRoleQPS, record.Points)
	quota = applyWorkbenchRoleCap(quota, roleQuota, roleDayUsed)
	packageName := currentPackageName(memberPackage)
	packageType := currentPackageType(hasMember)
	roleName := s.localizeRoleName(ctx, roleQuota.RoleID, roleQuota.RoleName)
	if roleName != "" {
		packageType = roleName
		if memberPackage == nil {
			packageName = roleName
		}
	}
	return UserWorkbenchStats{
		Balance:             record.Balance,
		CallTrends:          trends,
		CurrentPackageName:  packageName,
		CurrentPackageType:  packageType,
		DailyLimit:          quota.DailyLimit,
		DailyLimitUnlimited: quota.DailyLimitUnlimited,
		FailedCalls:         summary.FailedCalls,
		// 404 不计入成功/失败率（v1.2.30 统计口径）：分母 = 成功 + 有效失败。
		FailedRate:            percent(summary.FailedCalls, summary.SuccessCalls+summary.FailedCalls),
		HasAnyPackage:         hasAny,
		HasMemberPackage:      hasMember,
		InterfacePackageCount: len(interfacePackages),
		InterfacePackages:     interfacePackages,
		MemberPackage:         memberPackage,
		Outcomes:              outcomes,
		Points:                record.Points,
		PackageQuotaPreferred: quota.FromPackage,
		QPSLimit:              quota.QPSLimit,
		QPSLimitUnlimited:     quota.QPSLimitUnlimited,
		RemainingCalls:        quota.RemainingCalls,
		RemainingUnlimited:    quota.RemainingUnlimited,
		RoleQuotaEnabled:      roleQuota.Enabled == 1,
		RoleQuotaMinuteLimit:  roleQuota.Minute,
		RoleQuotaHourLimit:    roleQuota.Hour,
		RoleQuotaDayLimit:     roleQuota.Day,
		RoleQuotaMonthLimit:   roleQuota.Month,
		RoleQuotaCountMode:    roleQuota.CountMode,
		RoleQuotaDayUsed:      roleDayUsed,
		RoleQuotaDayRemaining: roleDayRemaining,
		RoleQuotaDayUnlimited: roleDayUnlimited,
		RoleKey:               roleQuota.RoleKey,
		RoleName:              roleName,
		SuccessCalls:          summary.SuccessCalls,
		SuccessRate:           percent(summary.SuccessCalls, summary.SuccessCalls+summary.FailedCalls),
		TodayCalls:            summary.TodayCalls,
	}, nil
}

var allAdminWorkbenchSections = []AdminWorkbenchSection{
	AdminWorkbenchSectionAccess,
	AdminWorkbenchSectionActiveUsers,
	AdminWorkbenchSectionAlerts,
	AdminWorkbenchSectionCallTrend,
	AdminWorkbenchSectionHotAPIs,
	AdminWorkbenchSectionOverview,
	AdminWorkbenchSectionRevenueTrend,
}

func ParseAdminWorkbenchSections(raw string) ([]AdminWorkbenchSection, error) {
	if strings.TrimSpace(raw) == "" {
		return append([]AdminWorkbenchSection(nil), allAdminWorkbenchSections...), nil
	}
	allowed := make(map[AdminWorkbenchSection]struct{}, len(allAdminWorkbenchSections))
	for _, section := range allAdminWorkbenchSections {
		allowed[section] = struct{}{}
	}
	selected := make(map[AdminWorkbenchSection]struct{})
	for _, item := range strings.Split(raw, ",") {
		section := AdminWorkbenchSection(strings.TrimSpace(item))
		if _, ok := allowed[section]; !ok {
			return nil, fmt.Errorf("unknown admin workbench section %q", item)
		}
		selected[section] = struct{}{}
	}
	sections := make([]AdminWorkbenchSection, 0, len(selected))
	for section := range selected {
		sections = append(sections, section)
	}
	sort.Slice(sections, func(i, j int) bool { return sections[i] < sections[j] })
	return sections, nil
}

func (s *Service) AdminWorkbenchStats(ctx context.Context, principal Principal, sections ...AdminWorkbenchSection) (AdminWorkbenchStats, error) {
	return s.AdminWorkbenchStatsWithAccessRange(ctx, principal, "", sections...)
}

// AdminWorkbenchStatsWithAccessRange 与 AdminWorkbenchStats 相同，但允许指定
// 访问态势图册的时间范围（today / 7d / 30d / all）。
//
// 单独开一个方法而不是给原方法加参数：保持既有调用点与测试不变，
// 避免为新增能力引入签名破坏。
func (s *Service) AdminWorkbenchStatsWithAccessRange(ctx context.Context, principal Principal, accessRange string, sections ...AdminWorkbenchSection) (AdminWorkbenchStats, error) {
	if err := s.requirePermission(ctx, principal, "system:user:list"); err != nil {
		return AdminWorkbenchStats{}, err
	}
	sections = normalizeAdminWorkbenchSections(sections)
	normalizedRange := string(normalizeAdminAccessRange(accessRange))
	cache := s.adminWorkbenchCache
	if cache == nil {
		s.adminWorkbenchCacheMu.Lock()
		cache = s.adminWorkbenchCache
		if cache == nil {
			cache = newAdminWorkbenchStatsCache()
			s.adminWorkbenchCache = cache
		}
		s.adminWorkbenchCacheMu.Unlock()
	}
	// 缓存 key 必须带上范围：同一次刷新里切换范围时，若不区分 key
	// 会直接命中上一个范围的结果，表现为「点了没反应」。
	key := adminWorkbenchStatsCacheKey(sections) + "|access:" + normalizedRange
	return cache.getOrLoad(ctx, key, func(loadCtx context.Context) (AdminWorkbenchStats, error) {
		return s.loadAdminWorkbenchStats(loadCtx, sections, normalizedRange)
	})
}

func (s *Service) loadAdminWorkbenchStats(ctx context.Context, sections []AdminWorkbenchSection, accessRange string) (AdminWorkbenchStats, error) {
	// Dashboard aggregation spans several independent queries. Bound the whole
	// fan-out so a slow location or trend query cannot hold the HTTP request
	// open indefinitely.
	statsCtx, cancel := context.WithTimeout(ctx, adminWorkbenchStatsLoadTimeout)
	defer cancel()
	// Administrator HTTP requests must never recover an empty projection by
	// grouping the complete high-cardinality source log. The analytics worker
	// owns backfills; overview/trend may add only a size-checked pending tail in
	// one read-only snapshot. Oversized or inconsistent windows stay degraded
	// instead of returning a truncated count as complete.
	statsCtx = withoutAdminRawLogFallback(statsCtx)
	now := s.now()
	selected := make(map[AdminWorkbenchSection]struct{}, len(sections))
	for _, section := range sections {
		selected[section] = struct{}{}
	}
	result := AdminWorkbenchStats{
		AnalyticsReady:   true,
		DegradedSections: []AdminWorkbenchSection{},
	}
	var resultMu sync.Mutex
	var wg sync.WaitGroup
	// sectionFailures carries per-section query errors so one broken rollup
	// table does not hide independent data that loaded successfully. A complete
	// dependency outage is handled differently below: returning HTTP 200 with
	// every field set to its Go zero value would be indistinguishable from real
	// business data and must therefore become an explicit retryable error.
	sectionFailures := make([]AdminWorkbenchSection, 0, len(sections))
	sectionErrors := make([]error, 0, len(sections))
	querySectionCount := 0
	overviewHasData := false
	repairState, repairStateErr := s.repo.adminAnalyticsRepairState(statsCtx, now)
	if repairStateErr != nil {
		// Unknown repair health cannot certify even non-zero rollups as complete.
		// Protect projection-backed sections, but keep independent payment/alert
		// queries available. Their own failures still turn a full outage into 503.
		s.reportAdminSectionError("analyticsState", repairStateErr)
		repairState = adminAnalyticsRepairState{
			active: true, projectionMask: adminAnalyticsProjectionMask, status: "UNKNOWN",
		}
	}
	s.reportAdminRepairState(repairState)
	run := func(section AdminWorkbenchSection, query func(context.Context) error) {
		if _, ok := selected[section]; !ok {
			return
		}
		querySectionCount++
		if section != AdminWorkbenchSectionOverview && repairState.suppresses() && repairState.degrades(section) {
			resultMu.Lock()
			sectionFailures = append(sectionFailures, section)
			sectionErrors = append(sectionErrors, fmt.Errorf("%s: %w", section, errAdminAnalyticsProjectionUnavailable))
			resultMu.Unlock()
			return
		}
		// Only the section goroutine acquires/releases the global slot. Repository
		// methods used here must not start unbounded child queries behind this
		// gate; otherwise the slot count would under-report real DB work.
		if err := acquireAdminStatsSlot(statsCtx); err != nil {
			resultMu.Lock()
			sectionFailures = append(sectionFailures, section)
			sectionErrors = append(sectionErrors, fmt.Errorf("%s: %w", section, err))
			resultMu.Unlock()
			return
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer releaseAdminStatsSlot()
			sectionCtx, cancelSection := context.WithTimeout(statsCtx, adminStatsQueryTimeout)
			defer cancelSection()
			err := query(sectionCtx)
			// A stalled repair may expose partial projections for diagnostics, but
			// those values must not replace a client's last complete statistics.
			if section != AdminWorkbenchSectionOverview && err == nil && repairState.degrades(section) {
				err = errAdminAnalyticsProjectionUnavailable
			}
			if err != nil {
				resultMu.Lock()
				sectionFailures = append(sectionFailures, section)
				sectionErrors = append(sectionErrors, fmt.Errorf("%s: %w", section, err))
				resultMu.Unlock()
				s.reportAdminSectionError(section, err)
			}
		}()
	}
	labels := dayLabels(now, 7)
	run(AdminWorkbenchSectionOverview, func(statsCtx context.Context) error {
		value, available, err := s.repo.adminOverview(statsCtx, repairState.degrades(AdminWorkbenchSectionOverview), nil)
		suppressedOutcomes := repairState.suppresses() && repairState.degrades(AdminWorkbenchSectionOverview)
		outcomes, outcomeErr := s.repo.adminTodayOutcomes(statsCtx, suppressedOutcomes)
		if outcomeErr != nil && s.log != nil {
			s.log.Warn("admin outcome composition unavailable",
				zap.String("error", masking.Error(outcomeErr)),
			)
			outcomes = CallOutcomeStats{}
		}
		resultMu.Lock()
		result.Overview = value
		result.Outcomes = outcomes
		overviewHasData = available
		resultMu.Unlock()
		if err == nil {
			// 记录概览数据，帮助诊断返回0的问题
			if s.log != nil {
				s.log.Debug("admin overview loaded",
					zap.Int64("today_calls", value.TodayCalls),
					zap.Int64("today_success_calls", value.TodaySuccessCalls),
					zap.Int64("today_failed_calls", value.TodayFailedCalls),
					zap.Int64("total_users", value.TotalUsers),
					zap.Int64("today_new_users", value.TodayNewUsers),
					zap.String("today_charge_amount", value.TodayChargeAmount),
					zap.String("total_revenue", value.TotalRevenue),
				)
			}
		} else if s.log != nil {
			s.log.Error("admin overview query failed",
				zap.String("error", masking.Error(err)),
			)
		}
		return err
	})
	run(AdminWorkbenchSectionCallTrend, func(statsCtx context.Context) error {
		value, err := s.repo.AdminCallTrend(statsCtx, labels)
		if err == nil {
			resultMu.Lock()
			result.CallTrend7d = value
			resultMu.Unlock()
			if s.log != nil {
				s.log.Debug("admin call trend loaded",
					zap.Int("days", len(value.Labels)),
					zap.Int64s("total_calls", value.TotalCalls),
					zap.Int64("total", value.Total),
				)
			}
		} else if s.log != nil {
			s.log.Error("admin call trend query failed",
				zap.String("error", masking.Error(err)),
			)
		}
		return err
	})
	run(AdminWorkbenchSectionRevenueTrend, func(statsCtx context.Context) error {
		value, err := s.repo.AdminRevenueTrend(statsCtx, labels)
		if err == nil {
			resultMu.Lock()
			result.RevenueTrend7d = value
			resultMu.Unlock()
			if s.log != nil {
				s.log.Debug("admin revenue trend loaded",
					zap.Int("days", len(value.Labels)),
					zap.Int64s("order_counts", value.OrderCounts),
					zap.String("total_amount", value.TotalAmount),
				)
			}
		} else if s.log != nil {
			s.log.Error("admin revenue trend query failed",
				zap.String("error", masking.Error(err)),
			)
		}
		return err
	})
	run(AdminWorkbenchSectionHotAPIs, func(statsCtx context.Context) error {
		value, err := s.repo.AdminHotAPIs(statsCtx)
		if err == nil {
			resultMu.Lock()
			result.HotApis = value
			resultMu.Unlock()
			if s.log != nil {
				s.log.Debug("admin hot APIs loaded",
					zap.Int("api_count", len(value)),
				)
			}
		} else if s.log != nil {
			s.log.Error("admin hot APIs query failed",
				zap.String("error", masking.Error(err)),
			)
		}
		return err
	})
	run(AdminWorkbenchSectionActiveUsers, func(statsCtx context.Context) error {
		value, err := s.repo.AdminActiveUsers(statsCtx)
		if err == nil {
			resultMu.Lock()
			result.ActiveUsers = value
			resultMu.Unlock()
			if s.log != nil {
				s.log.Debug("admin active users loaded",
					zap.Int("user_count", len(value)),
				)
			}
		} else if s.log != nil {
			s.log.Error("admin active users query failed",
				zap.String("error", masking.Error(err)),
			)
		}
		return err
	})
	run(AdminWorkbenchSectionAccess, func(statsCtx context.Context) error {
		value, err := s.loadAdminAccess(statsCtx, accessRange)
		if err == nil {
			resultMu.Lock()
			result.AccessSummary = value.Summary
			result.AccessLocations = value.Locations
			resultMu.Unlock()
		}
		return err
	})
	run(AdminWorkbenchSectionAlerts, func(statsCtx context.Context) error {
		value, err := s.repo.adminWorkbenchAlerts(statsCtx, now)
		if err == nil {
			resultMu.Lock()
			result.Alerts = value
			resultMu.Unlock()
			if s.log != nil {
				s.log.Debug("admin alerts loaded", zap.Int("alert_count", len(value)))
			}
		}
		return err
	})
	wg.Wait()
	if querySectionCount > 0 && len(sectionFailures) == querySectionCount && !overviewHasData {
		return AdminWorkbenchStats{}, apperrors.Wrap(
			consts.StatusServiceUnavailable,
			"管理员统计暂时不可用，请稍后重试",
			errors.Join(sectionErrors...),
		)
	}
	sort.Slice(sectionFailures, func(i, j int) bool { return sectionFailures[i] < sectionFailures[j] })
	result.DegradedSections = append(result.DegradedSections, sectionFailures...)
	result.AnalyticsReady = len(result.DegradedSections) == 0
	return result, nil
}

// reportAdminSectionError records a failed workbench section. It never
// escalates to the HTTP response: a dashboard section that cannot aggregate
// today must not hide the sections that can.
func (s *Service) reportAdminSectionError(section AdminWorkbenchSection, err error) {
	log := s.log
	if log == nil {
		return
	}
	log.Warn("admin workbench section degraded",
		zap.String("section", string(section)),
		zap.String("error", masking.Error(err)),
	)
}

// reportAdminRepairState makes the single most common cause of an all-zero
// admin workbench visible in the logs.
//
// Migration 000092 marks a bounded 31-day projection rebuild as PENDING/CLEAR.
// Until the serial analytics worker finishes it, projection-backed sections
// and overview call fields remain degraded; independent overview metrics still load.
// Without this log an operator only sees zeros and cannot tell the difference
// between "rebuild in progress", "worker disabled" and "no traffic today".
func (s *Service) reportAdminRepairState(state adminAnalyticsRepairState) {
	log := s.log
	if log == nil || (!state.active && state.lastError == "") {
		return
	}
	fields := []zap.Field{
		zap.String("checkpoint", adminAnalyticsCheckpointKey),
		zap.String("repair_status", state.status),
		zap.String("repair_phase", state.phase),
		zap.Uint16("repair_projection_mask", state.projectionMask),
		zap.Bool("suppressing_sections", state.suppresses()),
	}
	if !state.requestedAt.IsZero() {
		fields = append(fields,
			zap.Time("repair_requested_at", state.requestedAt),
			zap.Duration("repair_age", s.now().Sub(state.requestedAt)),
		)
	}
	if state.lastError != "" {
		fields = append(fields, zap.String("worker_last_error", masking.ErrorText(state.lastError)))
	}
	if state.stalled {
		log.Warn("admin workbench analytics repair looks stalled; serving current projections instead of suppressing sections. Verify the analytics worker is enabled (analytics.enabled=true / ANALYTICS_ENABLED=true) and check bp_analytics_checkpoint.last_error", fields...)
		return
	}
	log.Info("admin workbench analytics projections are rebuilding; affected sections report degraded until the worker completes", fields...)
}

func (s *Service) ListUsers(ctx context.Context, principal Principal, params UserListParams) (UserListResult, error) {
	if err := s.requirePermission(ctx, principal, "system:user:list"); err != nil {
		return UserListResult{}, err
	}
	if params.IncludeSensitive && !hasRole(principal.Roles, "admin") {
		return UserListResult{}, apperrors.New(consts.StatusForbidden, "Only administrators can view unmasked sensitive data")
	}
	if err := normalizeUserListParams(&params); err != nil {
		return UserListResult{}, err
	}
	result, err := s.repo.ListUsers(ctx, params)
	if err != nil || params.IncludeSensitive {
		return result, err
	}
	for index := range result.Items {
		result.Items[index] = maskSystemUser(result.Items[index])
	}
	return result, nil
}

func normalizeUserListParams(params *UserListParams) error {
	params.Keyword = strings.TrimSpace(params.Keyword)
	if err := listquery.Validate(params.Keyword, params.Page, params.PageSize, 200); err != nil {
		return err
	}
	if err := listquery.Binary("status", params.Status); err != nil {
		return err
	}
	return listquery.NonNegativeID("roleId", params.RoleID)
}

func (s *Service) CreateUser(ctx context.Context, principal Principal, req SaveUserRequest) error {
	if err := s.requirePermission(ctx, principal, "system:user:create"); err != nil {
		return err
	}
	normalized, passwordHash, status, points, err := s.normalizeSaveUser(req, true)
	if err != nil {
		return err
	}
	_, err = s.repo.CreateUser(ctx, principal.ID, normalized, passwordHash, status, points)
	return mapSaveUserError(err)
}

func (s *Service) GetSystemUser(ctx context.Context, principal Principal, userID int64) (SystemUser, error) {
	if err := s.requirePermission(ctx, principal, "system:user:list"); err != nil {
		return SystemUser{}, err
	}
	record, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SystemUser{}, apperrors.New(consts.StatusNotFound, "User not found")
		}
		return SystemUser{}, err
	}
	record.TotalRequests, err = s.repo.UserTotalRequests(ctx, userID)
	if err != nil {
		return SystemUser{}, err
	}
	return toSystemUser(record), nil
}

func (s *Service) UpdateUser(ctx context.Context, principal Principal, userID int64, req SaveUserRequest) error {
	if err := s.requirePermission(ctx, principal, "system:user:update"); err != nil {
		return err
	}
	// 绝对赋值会覆盖「打开编辑表单后用户又完成充值」的入账结果。资产只允许
	// 走带行锁的增量调整接口，编辑接口一旦收到 balance/points 就直接拒绝。
	if req.Balance != nil || req.Points != nil {
		return apperrors.New(consts.StatusBadRequest, "余额与点数请通过「余额调整」功能增减，避免覆盖用户同时充值的资产")
	}
	previous, err := s.repo.FindByID(ctx, userID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apperrors.New(consts.StatusNotFound, "用户不存在")
		}
		return err
	}
	applyUpdateDefaults(&req, previous)
	normalized, passwordHashText, status, _, err := s.normalizeSaveUser(req, false)
	if err != nil {
		return err
	}
	var passwordHash *string
	if passwordHashText != "" {
		passwordHash = &passwordHashText
	}
	if principal.ID == userID && status != 1 {
		return apperrors.New(consts.StatusBadRequest, "不能禁用当前登录管理员")
	}
	if err := mapSaveUserError(s.repo.UpdateUser(ctx, principal.ID, userID, normalized, passwordHash, status)); err != nil {
		return err
	}
	if s.accountTrafficChanged != nil && accountStateChanged(previous, normalized, status) {
		s.accountTrafficChanged(ctx)
	}
	return nil
}

// AdjustUserAssets 以增量方式调整余额与点数。正数增加、负数扣减；
// 空字符串或 0 表示该资产不变。实现位于仓储层事务内，会对用户行加锁，
// 与充值结算的 balance=balance+? 串行化，避免覆盖并发充值。
func (s *Service) AdjustUserAssets(ctx context.Context, principal Principal, userID int64, req AdjustUserAssetRequest) (UserAssetAdjustment, error) {
	if err := s.requirePermission(ctx, principal, "system:user:balance-adjust"); err != nil {
		return UserAssetAdjustment{}, err
	}
	normalized, err := normalizeAssetAdjustment(req)
	if err != nil {
		return UserAssetAdjustment{}, err
	}
	result, err := s.repo.AdjustUserAssets(ctx, principal.ID, userID, normalized.BalanceDelta, normalized.PointsDelta, normalized.Reason)
	if err != nil {
		return UserAssetAdjustment{}, mapAssetAdjustError(err)
	}
	return result, nil
}

func normalizeAssetAdjustment(req AdjustUserAssetRequest) (AdjustUserAssetRequest, error) {
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" || utf8.RuneCountInString(req.Reason) > 100 {
		return AdjustUserAssetRequest{}, apperrors.New(consts.StatusBadRequest, "调整原因不能为空且不能超过100个字符")
	}
	text := strings.TrimSpace(req.BalanceDelta)
	if text == "" {
		text = "0"
	}
	amount, err := decimalvalue.Parse(text)
	if errors.Is(err, decimalvalue.ErrExponentRange) || errors.Is(err, decimalvalue.ErrTextTooLong) {
		return AdjustUserAssetRequest{}, apperrors.New(consts.StatusBadRequest, "调整金额超过系统允许范围")
	}
	if err != nil {
		return AdjustUserAssetRequest{}, apperrors.New(consts.StatusBadRequest, "调整金额必须是数字")
	}
	scaled := new(big.Rat).Mul(new(big.Rat).Set(amount), big.NewRat(10_000, 1))
	if !scaled.IsInt() {
		return AdjustUserAssetRequest{}, apperrors.New(consts.StatusBadRequest, "调整金额最多支持4位小数")
	}
	maxBalance := new(big.Rat).SetFrac(big.NewInt(maxUserBalance), big.NewInt(10_000))
	if new(big.Rat).Abs(amount).Cmp(maxBalance) > 0 {
		return AdjustUserAssetRequest{}, apperrors.New(consts.StatusBadRequest, "调整金额超过系统允许范围")
	}
	if req.PointsDelta < -maxUserPoints || req.PointsDelta > maxUserPoints {
		return AdjustUserAssetRequest{}, apperrors.New(consts.StatusBadRequest, "调整点数超过系统允许范围")
	}
	if amount.Sign() == 0 && req.PointsDelta == 0 {
		return AdjustUserAssetRequest{}, apperrors.New(consts.StatusBadRequest, "调整金额不能为 0")
	}
	req.BalanceDelta = amount.FloatString(4)
	return req, nil
}

func mapAssetAdjustError(err error) error {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return apperrors.New(consts.StatusNotFound, "用户不存在")
	case errors.Is(err, ErrInsufficientBalance):
		return apperrors.New(consts.StatusBadRequest, "余额不足，扣减后余额不能为负数")
	case errors.Is(err, ErrInsufficientPoints):
		return apperrors.New(consts.StatusBadRequest, "点数不足，扣减后点数不能为负数")
	case errors.Is(err, ErrAssetCapacityExceeded):
		return apperrors.New(consts.StatusBadRequest, "调整后资产超过系统允许范围")
	case errors.Is(err, ErrInvalidAssetState):
		return apperrors.New(consts.StatusConflict, "用户资产状态异常，请刷新后重试")
	default:
		return err
	}
}

func accountStateChanged(previous UserRecord, next SaveUserRequest, nextStatus int) bool {
	nextPermanent, _, nextReason := userBanValues(next, nextStatus)
	nextUntil := strings.TrimSpace(next.BanUntil)
	if nextPermanent == 1 || nextStatus == accountstatus.Active {
		nextUntil = ""
	}
	return previous.Status != nextStatus ||
		previous.BanPermanent != nextPermanent ||
		previous.BanUntil != nextUntil ||
		previous.BanReason != nextReason
}

func (s *Service) DeleteUser(ctx context.Context, principal Principal, userID int64) error {
	if err := s.requirePermission(ctx, principal, "system:user:delete"); err != nil {
		return err
	}
	if principal.ID == userID {
		return apperrors.New(consts.StatusBadRequest, "不能删除当前登录用户")
	}
	return mapSaveUserError(s.repo.DeleteUser(ctx, userID))
}

func (s *Service) RoleList(ctx context.Context, principal Principal) ([]RoleOption, error) {
	if err := requireAdmin(principal); err != nil {
		return nil, err
	}
	return s.repo.ListRoles(ctx)
}

func (s *Service) GetSpecifiedResponse(ctx context.Context, principal Principal, userID int64) (SpecifiedResponseConfig, error) {
	if err := s.requirePermission(ctx, principal, "system:user:specified-response"); err != nil {
		return SpecifiedResponseConfig{}, err
	}
	if _, err := s.repo.FindByID(ctx, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return SpecifiedResponseConfig{}, apperrors.New(consts.StatusNotFound, "用户不存在")
		}
		return SpecifiedResponseConfig{}, err
	}
	return s.repo.GetSpecifiedResponse(ctx, userID)
}

func (s *Service) UpdateSpecifiedResponse(ctx context.Context, principal Principal, userID int64, cfg SpecifiedResponseConfig) error {
	if err := s.requirePermission(ctx, principal, "system:user:specified-response"); err != nil {
		return err
	}
	if cfg.SpecifiedResponseEnabled != 0 && cfg.SpecifiedResponseEnabled != 1 {
		return apperrors.New(consts.StatusBadRequest, "指定返回启用状态不合法")
	}
	if cfg.SpecifiedResponseBillable != 0 && cfg.SpecifiedResponseBillable != 1 {
		return apperrors.New(consts.StatusBadRequest, "指定返回计费状态不合法")
	}
	if cfg.SpecifiedResponseEnabled == 1 && strings.TrimSpace(cfg.SpecifiedResponseBody) == "" {
		return apperrors.New(consts.StatusBadRequest, "指定返回内容不能为空")
	}
	if len([]byte(cfg.SpecifiedResponseBody)) > 1024*1024 {
		return apperrors.New(consts.StatusBadRequest, "指定返回内容不能超过1MB")
	}
	if _, err := s.repo.FindByID(ctx, userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return apperrors.New(consts.StatusNotFound, "用户不存在")
		}
		return err
	}
	if err := s.repo.UpdateSpecifiedResponse(ctx, userID, cfg); err != nil {
		return err
	}
	if s.specifiedResponseChanged != nil {
		s.specifiedResponseChanged(ctx, userID)
	}
	return nil
}

func (s *Service) normalizeSaveUser(req SaveUserRequest, creating bool) (SaveUserRequest, string, int, int64, error) {
	req.Username = strings.TrimSpace(req.Username)
	req.RealName = strings.TrimSpace(req.RealName)
	req.Email = strings.TrimSpace(req.Email)
	req.Mobile = strings.TrimSpace(req.Mobile)
	req.HomePath = strings.TrimSpace(req.HomePath)
	if !usernamePattern.MatchString(req.Username) {
		return SaveUserRequest{}, "", 0, 0, apperrors.New(consts.StatusBadRequest, "用户名只能包含字母、数字、下划线和短横线，长度为3到32位")
	}
	if req.RealName == "" || utf8.RuneCountInString(req.RealName) > 64 {
		return SaveUserRequest{}, "", 0, 0, apperrors.New(consts.StatusBadRequest, "姓名不能为空且不能超过64个字符")
	}
	if req.Email != "" {
		req.Email = strings.ToLower(req.Email)
		if !validUserEmail(req.Email) {
			return SaveUserRequest{}, "", 0, 0, apperrors.New(consts.StatusBadRequest, "邮箱格式不合法")
		}
	}
	if req.Mobile != "" && !mobilePattern.MatchString(req.Mobile) {
		return SaveUserRequest{}, "", 0, 0, apperrors.New(consts.StatusBadRequest, "手机号格式不合法")
	}
	if !validUserURL(req.HomePath, false) || utf8.RuneCountInString(req.HomePath) > 128 {
		return SaveUserRequest{}, "", 0, 0, apperrors.New(consts.StatusBadRequest, "登录首页路径不合法")
	}
	// Keep absence distinct from an explicit zero. The repository preserves
	// omitted assets at the locked database row, not from a stale service read.
	if creating || req.Balance != nil {
		balance, err := normalizeBalance(req.Balance)
		if err != nil {
			return SaveUserRequest{}, "", 0, 0, err
		}
		req.Balance = balance
	}
	points := int64(0)
	if req.Points != nil {
		points = *req.Points
	}
	if points < 0 || points > maxUserPoints {
		return SaveUserRequest{}, "", 0, 0, apperrors.New(consts.StatusBadRequest, "点数不能为负数")
	}
	status := 1
	if req.Status != nil {
		status = *req.Status
	}
	if status != 0 && status != 1 {
		return SaveUserRequest{}, "", 0, 0, apperrors.New(consts.StatusBadRequest, "用户状态不合法")
	}
	if err := s.normalizeBanFields(&req, status); err != nil {
		return SaveUserRequest{}, "", 0, 0, err
	}
	var roleIDsValid bool
	req.RoleIDs, roleIDsValid = uniquePositiveIDs(req.RoleIDs)
	if !roleIDsValid || len(req.RoleIDs) == 0 || len(req.RoleIDs) > maxUserRoles {
		return SaveUserRequest{}, "", 0, 0, apperrors.New(consts.StatusBadRequest, "用户角色不能为空")
	}
	if req.PrimaryRoleID <= 0 {
		req.PrimaryRoleID = req.RoleIDs[0]
	}
	if !containsID(req.RoleIDs, req.PrimaryRoleID) {
		return SaveUserRequest{}, "", 0, 0, apperrors.New(consts.StatusBadRequest, "主角色必须属于当前用户角色")
	}
	passwordHash := ""
	shouldChangePassword := creating || req.ChangePassword
	if shouldChangePassword {
		password := req.Password
		if strings.TrimSpace(password) == "" {
			return SaveUserRequest{}, "", 0, 0, apperrors.New(consts.StatusBadRequest, "密码不能为空")
		}
		if !passwordpolicy.Valid(password) {
			return SaveUserRequest{}, "", 0, 0, apperrors.New(consts.StatusBadRequest, "密码必须为8到64个字符，且不能超过72字节")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return SaveUserRequest{}, "", 0, 0, err
		}
		passwordHash = string(hash)
		req.Password = password
	} else {
		req.Password = ""
	}
	return req, passwordHash, status, points, nil
}

func (s *Service) normalizeBanFields(req *SaveUserRequest, status int) error {
	if status == 1 {
		permanent := 0
		req.BanPermanent = &permanent
		req.BanUntil = ""
		req.BanReason = ""
		return nil
	}

	req.BanReason = strings.TrimSpace(req.BanReason)
	if req.BanReason == "" {
		return apperrors.New(consts.StatusBadRequest, "封禁原因不能为空")
	}
	if utf8.RuneCountInString(req.BanReason) > 100 {
		return apperrors.New(consts.StatusBadRequest, "封禁原因请用一句话说明主要原因（不超过100个字符）")
	}
	permanent := 1
	if req.BanPermanent != nil {
		permanent = *req.BanPermanent
	}
	if permanent != 0 && permanent != 1 {
		return apperrors.New(consts.StatusBadRequest, "封禁类型不合法")
	}
	req.BanPermanent = &permanent
	if permanent == 1 {
		req.BanUntil = ""
		return nil
	}

	now := s.currentTime()
	parsed, normalized, err := parseBanUntil(req.BanUntil, now)
	if err != nil || !parsed.After(now) {
		return apperrors.New(consts.StatusBadRequest, "临时封禁时间必须晚于当前时间")
	}
	req.BanUntil = normalized
	return nil
}

// applyUpdateDefaults 为「更新」请求补齐缺省字段。
//
// 局部更新绝不能把省略的字段解释成「启用」。normalizeSaveUser 在 status 缺省时
// 取 1，这对创建是正确的，对更新是致命的：一次只改昵称、只调余额的请求会把
// status=0 的账号改回 1，normalizeBanFields 随即清空封禁字段，UpdateUser 的
// 凭证联动又把名下 Key 恢复——漏传一个字段就悄悄绕过了整套封禁，
// 而日志里看不出发生过一次解封。显式传值仍然优先。
func applyUpdateDefaults(req *SaveUserRequest, previous UserRecord) {
	if req.Status == nil {
		current := previous.Status
		req.Status = &current
	}
	inheritBanFieldsForPartialUpdate(req, previous)
}

// inheritBanFieldsForPartialUpdate 在请求没有显式给出封禁字段时沿用当前值。
//
// 账号已经处于封禁态时，运营常做的操作是改昵称、调余额、换角色，这些请求
// 通常不会带上封禁原因和到期时间。若直接把空值交给 normalizeBanFields，
// 要么被「封禁原因不能为空」拒绝（挡住正常运营），要么在 status 缺省取 1 时
// 变成一次静默解封。沿用当前值可以让局部更新既不改变封禁语义，也不要求
// 运营重填一遍封禁信息；要解封必须显式传 status=1。
func inheritBanFieldsForPartialUpdate(req *SaveUserRequest, previous UserRecord) {
	if previous.Status != accountstatus.Banned {
		return
	}
	if req.BanPermanent == nil {
		permanent := previous.BanPermanent
		req.BanPermanent = &permanent
	}
	if strings.TrimSpace(req.BanReason) == "" {
		req.BanReason = previous.BanReason
	}
	// 永久封禁没有到期时间；临时封禁沿用原到期时间，仍需通过「晚于当前时间」
	// 校验——已到期的临时封禁在 FindByID 里已经被视为启用，不会走到这里。
	if previous.BanPermanent == 0 && strings.TrimSpace(req.BanUntil) == "" {
		req.BanUntil = previous.BanUntil
	}
}

func (s *Service) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func parseBanUntil(value string, now time.Time) (time.Time, string, error) {
	now = now.In(businessclock.Location())
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, "", errors.New("ban expiry is required")
	}
	formats := []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"}
	for _, format := range formats {
		var parsed time.Time
		var err error
		if strings.Contains(format, "Z07") {
			parsed, err = time.Parse(format, value)
		} else {
			parsed, err = time.ParseInLocation(format, value, now.Location())
		}
		if err == nil {
			// Validate the same second-precision instant that will be persisted.
			parsed = parsed.In(now.Location()).Truncate(time.Second)
			return parsed, parsed.Format("2006-01-02 15:04:05"), nil
		}
	}
	return time.Time{}, "", errors.New("invalid ban expiry")
}

func (s *Service) checkPasswordChangeAllowed(ctx context.Context, userID int64) error {
	raw, err := s.rdb.Get(ctx, passwordChangeAttemptKey(userID)).Int()
	if err != nil && err != redis.Nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "密码修改风控服务暂不可用", err)
	}
	if raw >= 3 {
		return apperrors.New(consts.StatusTooManyRequests, "密码修改尝试次数过多，请5分钟后再试")
	}
	return nil
}

func (s *Service) recordPasswordChangeFailure(ctx context.Context, userID int64) error {
	key := passwordChangeAttemptKey(userID)
	_, err := redisutil.IncrWithTTL(ctx, s.rdb, key, 5*time.Minute)
	if err != nil {
		return apperrors.Wrap(consts.StatusServiceUnavailable, "密码修改风控服务暂不可用", err)
	}
	return nil
}

func normalizeBalance(value any) (string, error) {
	if value == nil {
		return "0.0000", nil
	}
	var text string
	switch v := value.(type) {
	case float64:
		text = strconv.FormatFloat(v, 'f', 4, 64)
	case string:
		text = strings.TrimSpace(v)
	default:
		text = fmt.Sprintf("%v", v)
	}
	if text == "" {
		text = "0"
	}
	amount, err := decimalvalue.ParseNonNegative(text)
	if errors.Is(err, decimalvalue.ErrExponentRange) || errors.Is(err, decimalvalue.ErrTextTooLong) {
		return "", apperrors.New(consts.StatusBadRequest, "余额超过系统允许范围")
	}
	if err != nil {
		return "", apperrors.New(consts.StatusBadRequest, "余额必须是非负数字")
	}
	maxBalance := new(big.Rat).SetFrac(big.NewInt(maxUserBalance), big.NewInt(10_000))
	if amount.Cmp(maxBalance) > 0 {
		return "", apperrors.New(consts.StatusBadRequest, "余额超过系统允许范围")
	}
	return amount.FloatString(4), nil
}

func mapSaveUserError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrUserExists) {
		return apperrors.New(consts.StatusConflict, "用户名、邮箱或手机号已存在")
	}
	if errors.Is(err, ErrRoleNotFound) {
		return apperrors.New(consts.StatusBadRequest, "角色不存在或已禁用")
	}
	if errors.Is(err, ErrPrimaryRoleInvalid) {
		return apperrors.New(consts.StatusBadRequest, "主角色必须属于当前用户角色")
	}
	if errors.Is(err, ErrLastAdmin) {
		return apperrors.New(consts.StatusBadRequest, "不能移除最后一个管理员")
	}
	if errors.Is(err, ErrUserHasFunds) {
		return apperrors.New(consts.StatusConflict, "该用户仍有余额或点数，请先完成资金处理后再删除")
	}
	if errors.Is(err, ErrUserHasPendingPayments) {
		return apperrors.New(consts.StatusConflict, "该用户仍有待支付或支付中的订单，请先处理订单后再删除")
	}
	if errors.Is(err, ErrUserHasBusinessHistory) {
		return apperrors.New(consts.StatusConflict, "该用户存在调用、交易、套餐或内容审计记录，不能物理删除，请改为禁用")
	}
	if errors.Is(err, sql.ErrNoRows) {
		return apperrors.New(consts.StatusNotFound, "用户不存在")
	}
	return err
}

func validUserEmail(value string) bool {
	if value == "" || utf8.RuneCountInString(value) > 128 {
		return false
	}
	return emailaddr.Valid(value)
}

func validUserURL(value string, allowAbsolute bool) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	if strings.ContainsAny(value, "\r\n\\") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	if parsed.IsAbs() {
		return allowAbsolute && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != ""
	}
	return strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//")
}

func uniquePositiveIDs(values []int64) ([]int64, bool) {
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

func toUserInfo(record UserRecord) UserInfo {
	return UserInfo{
		Avatar:      record.Avatar,
		Desc:        "",
		Email:       masking.Email(record.Email),
		HomePath:    record.HomePath,
		Mobile:      masking.Mobile(record.Mobile),
		RealName:    record.RealName,
		Roles:       record.Roles,
		Token:       "",
		UserID:      fmt.Sprintf("%d", record.ID),
		Username:    record.Username,
		PasswordSet: record.PasswordSet,
	}
}

func maskSystemUser(item SystemUser) SystemUser {
	item.Email = masking.Email(item.Email)
	item.Mobile = masking.Mobile(item.Mobile)
	return item
}

func requireAdmin(principal Principal) error {
	if !hasRole(principal.Roles, "admin") {
		return apperrors.New(consts.StatusForbidden, "仅管理员可访问")
	}
	return nil
}

func (s *Service) requirePermission(ctx context.Context, principal Principal, code string) error {
	if s.authorizer == nil {
		return requireAdmin(principal)
	}
	ok, err := s.authorizer.HasPermission(ctx, principal.ID, principal.Roles, code)
	if err != nil {
		return err
	}
	if !ok {
		return apperrors.New(consts.StatusForbidden, "Forbidden Exception")
	}
	return nil
}

func hasRole(roles []string, target string) bool {
	for _, role := range roles {
		if role == target {
			return true
		}
	}
	return false
}

func emptyTrend(labels []string) UserCallTrendStats {
	return UserCallTrendStats{Labels: labels, Values: make([]int64, len(labels)), Total: 0}
}

func buildUserCallTrends(now time.Time, dailyCalls map[string]int64) map[string]UserCallTrendStats {
	return map[string]UserCallTrendStats{
		"sevenDays":  buildDailyCallTrend(now, 7, dailyCalls),
		"thirtyDays": buildDailyCallTrend(now, 30, dailyCalls),
		"oneYear":    buildMonthlyCallTrend(now, 12, dailyCalls),
	}
}

func buildDailyCallTrend(now time.Time, count int, dailyCalls map[string]int64) UserCallTrendStats {
	now = now.In(businessclock.Location())
	result := emptyTrend(dayLabels(now, count))
	start := now.AddDate(0, 0, -(count - 1))
	for i := 0; i < count; i++ {
		day := start.AddDate(0, 0, i).Format("2006-01-02")
		result.Values[i] = dailyCalls[day]
		result.Total += result.Values[i]
	}
	return result
}

func buildMonthlyCallTrend(now time.Time, count int, dailyCalls map[string]int64) UserCallTrendStats {
	labels := monthLabels(now, count)
	result := emptyTrend(labels)
	index := make(map[string]int, len(labels))
	for i, label := range labels {
		index[label] = i
	}
	for day, value := range dailyCalls {
		if len(day) < len("2006-01") {
			continue
		}
		if i, ok := index[day[:len("2006-01")]]; ok {
			result.Values[i] += value
			result.Total += value
		}
	}
	return result
}

func dayLabels(now time.Time, count int) []string {
	now = now.In(businessclock.Location())
	labels := make([]string, 0, count)
	start := now.AddDate(0, 0, -(count - 1))
	for i := 0; i < count; i++ {
		labels = append(labels, start.AddDate(0, 0, i).Format("01-02"))
	}
	return labels
}

func monthLabels(now time.Time, count int) []string {
	now = now.In(businessclock.Location())
	labels := make([]string, 0, count)
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, -(count - 1), 0)
	for i := 0; i < count; i++ {
		labels = append(labels, start.AddDate(0, i, 0).Format("2006-01"))
	}
	return labels
}

func zeroMoneyList(count int) []string {
	values := make([]string, count)
	for i := range values {
		values[i] = "0.00"
	}
	return values
}

type workbenchQuotaSummary struct {
	DailyLimit          int64
	DailyLimitUnlimited bool
	QPSLimit            int
	QPSLimitUnlimited   bool
	RemainingCalls      int64
	RemainingUnlimited  bool
	// FromPackage 表示本次展示取的是可用套餐（主套餐优先，其次最早到期）的配置。
	// 套餐扣减发生在用户组免费额度之前，此时网关不校验用户组的分/时/日/月窗口。
	FromPackage bool
}

func summarizeWorkbenchQuota(member *UserWorkbenchMemberPackage, interfaces []UserWorkbenchInterfacePackage, primaryRoleQPS int, points int64) workbenchQuotaSummary {
	if member != nil && workbenchPackageHasDailyCapacity(member.DailyLimit, member.RemainingCalls, member.RemainingUnlimited) {
		qpsLimit, qpsUnlimited := effectiveWorkbenchQPS(primaryRoleQPS, member.QPSLimit)
		return workbenchQuotaSummary{
			DailyLimit:          member.DailyLimit,
			DailyLimitUnlimited: member.DailyLimit <= 0,
			QPSLimit:            qpsLimit,
			QPSLimitUnlimited:   qpsUnlimited,
			RemainingCalls:      member.RemainingCalls,
			RemainingUnlimited:  member.RemainingUnlimited,
			FromPackage:         true,
		}
	}
	availableInterfaces := make([]UserWorkbenchInterfacePackage, 0, len(interfaces))
	for _, item := range interfaces {
		if workbenchPackageHasDailyCapacity(item.DailyLimit, item.RemainingCalls, item.RemainingUnlimited) {
			availableInterfaces = append(availableInterfaces, item)
		}
	}
	if len(availableInterfaces) == 1 {
		item := availableInterfaces[0]
		qpsLimit, qpsUnlimited := effectiveWorkbenchQPS(primaryRoleQPS, item.QPSLimit)
		return workbenchQuotaSummary{
			DailyLimit:          item.DailyLimit,
			DailyLimitUnlimited: item.DailyLimit <= 0,
			QPSLimit:            qpsLimit,
			QPSLimitUnlimited:   qpsUnlimited,
			RemainingCalls:      item.RemainingCalls,
			RemainingUnlimited:  item.RemainingUnlimited,
			FromPackage:         true,
		}
	}
	if len(availableInterfaces) > 1 {
		return workbenchQuotaSummary{}
	}
	return workbenchQuotaSummary{
		DailyLimitUnlimited: true,
		QPSLimit:            normalizeRoleQPS(primaryRoleQPS),
		RemainingCalls:      points,
	}
}

func effectiveWorkbenchQPS(primaryRoleQPS, packageQPS int) (int, bool) {
	roleQPS := normalizeRoleQPS(primaryRoleQPS)
	if packageQPS == 0 {
		return 0, true
	}
	if packageQPS > roleQPS {
		if packageQPS > 1_000_000 {
			packageQPS = 1_000_000
		}
		return packageQPS, false
	}
	return roleQPS, false
}

func roleQuotaDayRemaining(quota UserRoleQuota, used int64) (int64, bool) {
	if quota.Enabled != 1 || quota.Day <= 0 {
		return 0, true
	}
	remaining := quota.Day - used
	if remaining < 0 {
		remaining = 0
	}
	return remaining, false
}

// applyWorkbenchRoleCap 只在配额来自用户组时套用角色日上限。
// 有可用套餐时网关先扣套餐，角色日上限不会提前拦截，不能用它覆盖套餐配置。
func applyWorkbenchRoleCap(quota workbenchQuotaSummary, role UserRoleQuota, used int64) workbenchQuotaSummary {
	if quota.FromPackage {
		return quota
	}
	return applyRoleQuotaCap(quota, role, used)
}

// applyRoleQuotaCap keeps package/points fallback logic intact, then applies the
// role's daily cap. The console must never claim "unlimited" when the gateway
// would reject the account at the role day limit.
func applyRoleQuotaCap(quota workbenchQuotaSummary, role UserRoleQuota, used int64) workbenchQuotaSummary {
	if role.Enabled != 1 || role.Day <= 0 {
		return quota
	}
	remaining, _ := roleQuotaDayRemaining(role, used)
	wasUnlimited := quota.DailyLimitUnlimited
	if wasUnlimited || quota.DailyLimit <= 0 || role.Day < quota.DailyLimit {
		quota.DailyLimit = role.Day
	}
	quota.DailyLimitUnlimited = false
	if wasUnlimited || quota.RemainingUnlimited || quota.RemainingCalls > remaining {
		quota.RemainingCalls = remaining
	}
	quota.RemainingUnlimited = false
	return quota
}

func hasUsableWorkbenchPackage(member *UserWorkbenchMemberPackage, interfaces []UserWorkbenchInterfacePackage) bool {
	if member != nil && workbenchPackageHasDailyCapacity(member.DailyLimit, member.RemainingCalls, member.RemainingUnlimited) {
		return true
	}
	for _, item := range interfaces {
		if workbenchPackageHasDailyCapacity(item.DailyLimit, item.RemainingCalls, item.RemainingUnlimited) {
			return true
		}
	}
	return false
}

func workbenchPackageHasDailyCapacity(limit, remaining int64, unlimited bool) bool {
	return limit <= 0 || unlimited || remaining > 0
}

func currentPackageName(member *UserWorkbenchMemberPackage) string {
	if member != nil && strings.TrimSpace(member.PackageName) != "" {
		return member.PackageName
	}
	return "普通用户"
}

func currentPackageType(hasMember bool) string {
	if hasMember {
		return "全站套餐"
	}
	return "基础账户"
}

func percent(value, total int64) string {
	if total <= 0 {
		return "0%"
	}
	return fmt.Sprintf("%.2f%%", float64(value)*100/float64(total))
}

func passwordChangeAttemptKey(userID int64) string {
	return fmt.Sprintf("user:password:change:attempt:%d", userID)
}
